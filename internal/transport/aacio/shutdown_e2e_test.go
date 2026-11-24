// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacio_test

import (
	"context"
	"encoding/binary"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/DisasteR/hwraid_exporter/internal/aacraid"
	"github.com/DisasteR/hwraid_exporter/internal/exporter"
	"github.com/DisasteR/hwraid_exporter/internal/lifecycle"
	"github.com/DisasteR/hwraid_exporter/internal/module"
	"github.com/DisasteR/hwraid_exporter/internal/modules/aacmod"
	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
	"github.com/DisasteR/hwraid_exporter/internal/transport/aacio"
)

// recorder sert GET_HBA_INFO pour la carte de test et journalise chaque
// ioctl atteignant le « noyau ». La première commande firmware (706, par
// FSACTL_SEND_FIB) attend release : c'est la commande en cours au moment
// de l'arrêt ; elle réussit ensuite. Les autres commandes firmware
// échouent (EIO).
type recorder struct {
	mu       sync.Mutex
	reqs     []uint32
	blocked  chan struct{}
	release  chan struct{}
	blockOne sync.Once
}

func (r *recorder) ioctl(req uint32, buf []byte) error {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	if req == aacraid.IoctlGetHBAInfo {
		buf[50], buf[51], buf[52] = 0, 1, 0
		for off, v := range map[int]uint32{60: 0x9005, 64: 0x028b, 68: 0x9005, 72: 0x0301} {
			binary.LittleEndian.PutUint32(buf[off:], v)
		}
		return nil
	}
	first := false
	r.blockOne.Do(func() { first = true })
	if first {
		close(r.blocked)
		<-r.release
		// Réponse 706 valide d'une carte qui annonce les lectures d'état :
		// sans l'arrêt, le module enverrait ensuite les lectures de la
		// protection du cache et des volumes.
		binary.LittleEndian.PutUint16(buf[8:], aacraid.FIBSize)
		binary.LittleEndian.PutUint32(buf[aacraid.FIBHeaderSize+0x20:], 15)
		binary.LittleEndian.PutUint32(buf[aacraid.FIBHeaderSize+0x90:], 0x00c8693c)
		return nil
	}
	return unix.EIO
}

func (r *recorder) sent() []uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uint32(nil), r.reqs...)
}

// Adaptec 6805 au firmware qualifié, nœud de test /dev/null.
func e2eCard() (module.Card, []module.NodeRef) {
	c := module.Card{
		PCI: sysfs.PCIFunction{Address: "0000:01:00.0", Driver: "aacraid", Class: "0x010400",
			Vendor: "0x9005", Device: "0x028b", SubsystemVendor: "0x9005", SubsystemDevice: "0x0301"},
		Hosts: []sysfs.SCSIHost{{Name: "host3", Number: 3, ProcName: "aacraid", UniqueID: "0", Firmware: "5.3-0[19204]"}},
	}
	return c, []module.NodeRef{{Path: "/dev/null", Dev: unix.Mkdev(1, 3)}}
}

// Chemin d'arrêt du service, tel que serve l'assemble (boucle, cycle,
// ordonnanceur, module, transport), avec un vrai SIGTERM envoyé au
// processus pendant qu'une commande firmware attend la carte : la commande
// n'est pas interrompue, aucune autre n'atteint le noyau, l'attente d'arrêt
// dure jusqu'à sa fin en avertissant, puis rend la main. Deux cas : arrêt
// pendant le cycle, et collecte déjà déclarée hors délai à l'échéance (les
// cycles suivants ne la relancent pas, seul l'arrêt l'attend).
func TestShutdownDuringBlockedCommand(t *testing.T) {
	list, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	for _, late := range []bool{false, true} {
		rec := &recorder{blocked: make(chan struct{}), release: make(chan struct{})}
		aacio.ReplaceIoctl(t, rec.ioctl)
		c, refs := e2eCard()
		m := aacmod.New(list)
		runner := exporter.NewRunner()
		timeout := time.Hour
		if late {
			timeout = 20 * time.Millisecond
		}
		cycles := make(chan map[string]module.Result, 100)
		collect := func(stopping context.Context) {
			cycle, end := context.WithTimeout(stopping, timeout)
			defer end()
			jobs := map[string]func() module.Result{c.PCI.Address: func() module.Result { return m.Collect(cycle, c, refs) }}
			cycles <- runner.Run(cycle, jobs, map[string][]string{c.PCI.Address: m.Collectors()})
		}
		sig, stopSig := signal.NotifyContext(context.Background(), lifecycle.TerminationSignals()...)
		release := sync.OnceFunc(func() { close(rec.release) })
		loopDone := make(chan struct{})
		go func() {
			defer close(loopDone)
			lifecycle.RunLoop(sig, 5*time.Millisecond, collect)
		}()
		// Sur tous les chemins, échec compris : arrêt de la boucle, libération
		// de la commande simulée et fin de la collecte, avant que l'ioctl
		// réel ne soit rétabli (nettoyage enregistré après ReplaceIoctl, donc
		// exécuté avant lui).
		t.Cleanup(func() {
			stopSig()
			release()
			// L'ioctl réel n'est rétabli qu'après la fin effective de la boucle
			// et de la collecte : un retard est signalé, puis attendu.
			select {
			case <-loopDone:
			case <-time.After(10 * time.Second):
				t.Errorf("late=%v : boucle de collecte toujours active au nettoyage", late)
				<-loopDone
			}
			if !runner.Drain(10 * time.Second) {
				t.Errorf("late=%v : collecte toujours en cours au nettoyage", late)
				for !runner.Drain(time.Second) {
				}
			}
		})
		select {
		case <-rec.blocked:
		case <-time.After(5 * time.Second):
			t.Fatalf("late=%v : la commande firmware attendue n'est jamais partie (ioctl : %#x)", late, rec.sent())
		}
		if late {
			if out := <-cycles; !out[c.PCI.Address].TimedOut {
				t.Fatalf("hors délai attendu : %+v", out)
			}
			time.Sleep(30 * time.Millisecond) // quelques cycles sur une carte occupée
		}
		if err := unix.Kill(os.Getpid(), unix.SIGTERM); err != nil {
			t.Fatal(err)
		}
		<-sig.Done()
		var warns atomic.Int32
		idle := make(chan struct{})
		go func() {
			defer close(idle)
			lifecycle.WaitIdle(loopDone, runner.Drain, 10*time.Millisecond, func(time.Duration) { warns.Add(1) })
		}()
		select {
		case <-idle:
			t.Fatalf("late=%v : l'arrêt n'attend pas la commande en cours", late)
		case <-time.After(60 * time.Millisecond):
		}
		release()
		select {
		case <-idle:
		case <-time.After(2 * time.Second):
			t.Fatalf("late=%v : l'arrêt ne rend pas la main après la commande", late)
		}
		stopSig()
		if warns.Load() == 0 {
			t.Fatalf("late=%v : aucun avertissement pendant l'attente", late)
		}
		got := rec.sent()
		if len(got) != 2 || got[0] != aacraid.IoctlGetHBAInfo || got[1] != aacraid.IoctlSendFIB {
			t.Fatalf("late=%v : ioctl atteignant le noyau : %#x (GET_HBA_INFO puis une seule commande firmware attendus)", late, got)
		}
	}
}

// Arrêt demandé avant le premier cycle : seul GET_HBA_INFO, servi par le
// pilote sans commande à la carte, atteint le noyau.
func TestShutdownBeforeFirstCycle(t *testing.T) {
	list, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{blocked: make(chan struct{}), release: make(chan struct{})}
	close(rec.release)
	aacio.ReplaceIoctl(t, rec.ioctl)
	c, refs := e2eCard()
	m := aacmod.New(list)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := m.Collect(ctx, c, refs)
	if !r.NodeValid || r.CollectErr == nil {
		t.Fatalf("%+v", r)
	}
	if got := rec.sent(); len(got) != 1 || got[0] != aacraid.IoctlGetHBAInfo {
		t.Fatalf("commandes firmware après l'arrêt : %#x", got)
	}
}

// Témoin du test précédent : sans arrêt, la même réponse 706 est suivie
// d'autres commandes firmware. Sans ce témoin, l'absence de commande après
// l'arrêt pourrait venir du faux firmware et non de l'arrêt.
func TestBlockedCommandIsFollowedWithoutShutdown(t *testing.T) {
	list, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{blocked: make(chan struct{}), release: make(chan struct{})}
	close(rec.release)
	aacio.ReplaceIoctl(t, rec.ioctl)
	c, refs := e2eCard()
	aacmod.New(list).Collect(context.Background(), c, refs)
	if got := rec.sent(); len(got) < 3 {
		t.Fatalf("ioctl : %#x (des commandes après 706 attendues)", got)
	}
}
