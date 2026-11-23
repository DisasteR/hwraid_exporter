// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacio

import (
	"errors"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/DisasteR/hwraid_exporter/internal/aacraid"
	"github.com/DisasteR/hwraid_exporter/internal/allowlist"
)

// blockedSignals lit le masque du thread appelant dans /proc.
func blockedSignals(t *testing.T) uint64 {
	t.Helper()
	b, err := os.ReadFile("/proc/thread-self/status")
	if err != nil {
		t.Skipf("/proc indisponible : %v", err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "SigBlk:"); ok {
			m, err := strconv.ParseUint(strings.TrimSpace(v), 16, 64)
			if err != nil {
				t.Fatal(err)
			}
			return m
		}
	}
	t.Fatal("SigBlk absent")
	return 0
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
	var m uint64
	q.run(func() { m = blockedSignals(t) })
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
// EINTR, comme l'attente du pilote aacraid rend -ERESTARTSYS.
func TestQuietThreadNotInterrupted(t *testing.T) {
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, unix.SIGTERM, unix.SIGUSR1)
	defer signal.Stop(sigs)
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(p[0])
	defer unix.Close(p[1])

	q, err := startQuietThread()
	if err != nil {
		t.Fatal(err)
	}
	defer q.stop()
	tid := make(chan int, 1)
	var n int
	var perr error
	go func() {
		// Les signaux sont envoyés pendant l'attente, puis l'attente est
		// libérée par une écriture.
		id := <-tid
		time.Sleep(20 * time.Millisecond)
		for _, s := range []unix.Signal{unix.SIGURG, unix.SIGTERM, unix.SIGUSR1} {
			if err := unix.Tgkill(unix.Getpid(), id, s); err != nil {
				t.Errorf("tgkill %v : %v", s, err)
			}
		}
		time.Sleep(50 * time.Millisecond)
		unix.Write(p[1], []byte{1})
	}()
	q.run(func() {
		tid <- unix.Gettid()
		fds := []unix.PollFd{{Fd: int32(p[0]), Events: unix.POLLIN}}
		n, perr = unix.Ppoll(fds, nil, nil)
	})
	if perr != nil || n != 1 {
		t.Fatalf("attente interrompue : n=%d, err=%v", n, perr)
	}
}

// Chaque ioctl de Device est émis depuis le thread masqué.
func TestDeviceIoctlOnQuietThread(t *testing.T) {
	var m uint64
	fakeIoctl(t, func(uint32, []byte) error { m = blockedSignals(t); return nil })
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Do(allowlist.Request{Ioctl: aacraid.IoctlGetHBAInfo, Buf: make([]byte, aacraid.HBAInfoSize)}); err != nil {
		t.Fatal(err)
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
	_, err = d.SupplementInfo()
	if !errors.Is(err, ErrDriverPoolExhausted) || !errors.Is(err, unix.ENOMEM) {
		t.Fatalf("SENDFIB : ErrDriverPoolExhausted et ENOMEM attendus, obtenu %v", err)
	}
	if _, err = d.ConnectorMap(); !errors.Is(err, ErrDriverPoolExhausted) {
		t.Fatalf("SEND_LARGE_FIB : ErrDriverPoolExhausted attendu, obtenu %v", err)
	}
	_, err = d.HBAInfo()
	if !errors.Is(err, unix.ENOMEM) || errors.Is(err, ErrDriverPoolExhausted) {
		t.Fatalf("GET_HBA_INFO : ENOMEM seul attendu, obtenu %v", err)
	}
	fakeIoctl(t, func(uint32, []byte) error { return unix.EIO })
	if _, err = d.SupplementInfo(); errors.Is(err, ErrDriverPoolExhausted) {
		t.Fatalf("EIO qualifié à tort : %v", err)
	}
}

// Close termine le thread : pas de thread perdu à chaque cycle.
func TestCloseStopsQuietThread(t *testing.T) {
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	q := d.thread
	d.Close()
	defer func() {
		if recover() == nil {
			t.Fatal("le thread accepte encore du travail après Close")
		}
	}()
	q.work <- func() {}
}
