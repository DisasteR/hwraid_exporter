// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacio

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/DisasteR/hwraid_exporter/internal/aacraid"
	"github.com/DisasteR/hwraid_exporter/internal/allowlist"
)

// readSigBlk lit le masque du thread appelant dans /proc. Sans testing.T :
// appelée sur le thread des ioctl, elle ne doit pas mettre fin à sa
// goroutine (t.Skip et t.Fatal appellent runtime.Goexit).
func readSigBlk() (uint64, error) {
	b, err := os.ReadFile("/proc/thread-self/status")
	if err != nil {
		return 0, err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "SigBlk:"); ok {
			return strconv.ParseUint(strings.TrimSpace(v), 16, 64)
		}
	}
	return 0, errors.New("SigBlk absent")
}

// requireProc saute le test si /proc ne donne pas l'état des threads.
func requireProc(t *testing.T) {
	t.Helper()
	if _, err := readSigBlk(); err != nil {
		t.Skipf("/proc indisponible : %v", err)
	}
}

// blockedSignals lit le masque du thread de la goroutine de test.
func blockedSignals(t *testing.T) uint64 {
	t.Helper()
	m, err := readSigBlk()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func bit(s unix.Signal) uint64 { return 1 << (uint(s) - 1) }

// Le thread des ioctl bloque les signaux asynchrones, dont ceux que le
// runtime Go et l'arrêt du service envoient, mais pas ceux d'une faute.
func TestQuietThreadMask(t *testing.T) {
	q, err := startQuietThread()
	if err != nil {
		t.Fatal(err)
	}
	defer q.stop()
	requireProc(t)
	var m uint64
	var merr error
	q.run(func() { m, merr = readSigBlk() })
	if merr != nil {
		t.Fatal(merr)
	}
	for _, s := range []unix.Signal{unix.SIGURG, unix.SIGTERM, unix.SIGINT, unix.SIGHUP, unix.SIGQUIT, unix.SIGUSR1, unix.SIGCHLD, unix.SIGPROF} {
		if m&bit(s) == 0 {
			t.Errorf("%v non bloqué sur le thread des ioctl (masque %#x)", s, m)
		}
	}
	for _, s := range syncSignals {
		if m&bit(s) != 0 {
			t.Errorf("%v bloqué : une faute du thread ne serait plus traitée (masque %#x)", s, m)
		}
	}
	// Le masque ne fuit pas vers les autres goroutines.
	if blockedSignals(t)&bit(unix.SIGURG) != 0 {
		t.Error("SIGURG bloqué hors du thread des ioctl")
	}
}

// Un signal adressé au thread pendant un appel bloquant ne l'interrompt
// pas. ppoll n'est jamais relancé par le noyau : sans masque, il rendrait
// EINTR, comme l'attente du pilote aacraid rend -ERESTARTSYS. Les signaux
// partent quand /proc montre le thread dans ppoll. Témoin : le même
// scénario sur un thread ordinaire rend EINTR.
func TestQuietThreadNotInterrupted(t *testing.T) {
	requireProc(t)
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, unix.SIGTERM, unix.SIGUSR1)
	defer signal.Stop(sigs)

	q, err := startQuietThread()
	if err != nil {
		t.Fatal(err)
	}
	defer q.stop()
	if n, err := pollUnderSignals(t, q.run); err != nil || n != 1 {
		t.Fatalf("attente interrompue : n=%d, err=%v", n, err)
	}
	plain := func(f func()) {
		done := make(chan struct{})
		go func() {
			defer close(done)
			runtime.LockOSThread() // thread rendu au système en fin de goroutine
			f()
		}()
		<-done
	}
	if _, err := pollUnderSignals(t, plain); !errors.Is(err, unix.EINTR) {
		t.Fatalf("témoin : ppoll non interrompu sur un thread ordinaire (err=%v) : le test n'exerce pas la fenêtre", err)
	}
}

// pollUnderSignals attend sur un tube, dans run, et adresse SIGURG,
// SIGTERM et SIGUSR1 au thread une fois qu'il est entré dans ppoll, puis
// libère l'attente par une écriture.
func pollUnderSignals(t *testing.T, run func(func())) (int, error) {
	t.Helper()
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(p[0])
	defer unix.Close(p[1])
	tid := make(chan int, 1)
	sent := make(chan error, 1)
	go func() {
		id := <-tid
		task := fmt.Sprintf("/proc/self/task/%d/syscall", id)
		in := false
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
			b, err := os.ReadFile(task)
			if f := strings.Fields(string(b)); err == nil && len(f) > 0 && f[0] == strconv.Itoa(unix.SYS_PPOLL) {
				in = true
				break
			}
		}
		var err error
		if !in {
			err = errors.New("thread jamais vu dans ppoll")
		}
		for _, s := range []unix.Signal{unix.SIGURG, unix.SIGTERM, unix.SIGUSR1} {
			if e := unix.Tgkill(unix.Getpid(), id, s); e != nil && err == nil {
				err = fmt.Errorf("tgkill %v : %w", s, e)
			}
		}
		time.Sleep(50 * time.Millisecond)
		unix.Write(p[1], []byte{1})
		sent <- err
	}()
	var n int
	var perr error
	run(func() {
		tid <- unix.Gettid()
		fds := []unix.PollFd{{Fd: int32(p[0]), Events: unix.POLLIN}}
		n, perr = unix.Ppoll(fds, nil, nil)
	})
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	return n, perr
}

// Chaque ioctl de Device est émis depuis le thread masqué.
func TestDeviceIoctlOnQuietThread(t *testing.T) {
	requireProc(t)
	var m uint64
	var merr error
	fakeIoctl(t, func(uint32, []byte) error { m, merr = readSigBlk(); return nil })
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Do(allowlist.Request{Ioctl: aacraid.IoctlGetHBAInfo, Buf: make([]byte, aacraid.HBAInfoSize)}); err != nil {
		t.Fatal(err)
	}
	if merr != nil {
		t.Fatal(merr)
	}
	if m&bit(unix.SIGURG) == 0 || m&bit(unix.SIGTERM) == 0 {
		t.Fatalf("ioctl émis hors du thread masqué (masque %#x)", m)
	}
}

// ENOMEM sur une commande firmware est qualifié ; pas sur GET_HBA_INFO,
// que le pilote sert sans bloc de commande.
func TestDriverPoolExhausted(t *testing.T) {
	fakeIoctl(t, func(uint32, []byte) error { return unix.ENOMEM })
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	_, err = d.SupplementInfo(context.Background())
	if !errors.Is(err, ErrDriverNoMemory) || !errors.Is(err, unix.ENOMEM) {
		t.Fatalf("SENDFIB : ErrDriverNoMemory et ENOMEM attendus, obtenu %v", err)
	}
	if _, err = d.ConnectorMap(context.Background()); !errors.Is(err, ErrDriverNoMemory) {
		t.Fatalf("SEND_LARGE_FIB : ErrDriverNoMemory attendu, obtenu %v", err)
	}
	_, err = d.HBAInfo(context.Background())
	if !errors.Is(err, unix.ENOMEM) || errors.Is(err, ErrDriverNoMemory) {
		t.Fatalf("GET_HBA_INFO : ENOMEM seul attendu, obtenu %v", err)
	}
	fakeIoctl(t, func(uint32, []byte) error { return unix.EIO })
	if _, err = d.SupplementInfo(context.Background()); errors.Is(err, ErrDriverNoMemory) {
		t.Fatalf("EIO qualifié à tort : %v", err)
	}
}

// Close termine le thread : la goroutine sort et le thread système, jamais
// rendu au runtime, est détruit. Pas de thread perdu à chaque cycle.
func TestCloseStopsQuietThread(t *testing.T) {
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	q := d.thread
	var tid int
	q.run(func() { tid = unix.Gettid() })
	task := fmt.Sprintf("/proc/self/task/%d", tid)
	if _, err := os.Stat(task); err != nil {
		t.Fatalf("thread %d introuvable avant Close : %v", tid, err)
	}
	d.Close()
	select {
	case <-q.done:
	case <-time.After(2 * time.Second):
		t.Fatal("la goroutine du thread ne se termine pas")
	}
	// Thread initial du processus (TID = PID) : le runtime Go le gare au lieu
	// de le détruire quand la goroutine verrouillée se termine. Seule la fin
	// de la goroutine est vérifiable.
	if tid == os.Getpid() {
		t.Logf("thread %d : thread initial, gardé par le runtime", tid)
		return
	}
	for end := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(task); os.IsNotExist(err) {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("thread système %d toujours présent après Close", tid)
		}
	}
}
