// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacio_test

import (
	"context"
	"encoding/binary"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/DisasteR/hwraid_exporter/internal/aacraid"
	"github.com/DisasteR/hwraid_exporter/internal/exporter"
	"github.com/DisasteR/hwraid_exporter/internal/module"
	"github.com/DisasteR/hwraid_exporter/internal/modules/aacmod"
	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
	"github.com/DisasteR/hwraid_exporter/internal/transport/aacio"
)

// recorder sert GET_HBA_INFO pour la carte de test et journalise chaque
// ioctl atteignant le « noyau ». Le premier GET_HBA_INFO attend release :
// c'est la commande en cours au moment de l'arrêt.
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
		r.blockOne.Do(func() {
			close(r.blocked)
			<-r.release
		})
		buf[50], buf[51], buf[52] = 0, 1, 0
		for off, v := range map[int]uint32{60: 0x9005, 64: 0x028b, 68: 0x9005, 72: 0x0301} {
			binary.LittleEndian.PutUint32(buf[off:], v)
		}
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

// Arrêt du service pendant qu'une commande attend la carte, avec le module,
// l'ordonnanceur et le transport réels : la commande en cours n'est pas
// interrompue, aucune autre n'atteint le noyau, et l'attente d'arrêt
// (Drain) dure jusqu'à la fin de cette commande. Deux cas : arrêt pendant
// le cycle (résultat attendu par Run) et collecte déjà déclarée hors délai
// à l'échéance (résultat abandonné, attendu seulement par Drain).
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
		stopping, stop := context.WithCancel(context.Background())
		timeout := time.Hour
		if late {
			timeout = 20 * time.Millisecond
		}
		cycle, end := context.WithTimeout(stopping, timeout)
		jobs := map[string]func() module.Result{c.PCI.Address: func() module.Result { return m.Collect(cycle, c, refs) }}
		ran := make(chan map[string]module.Result, 1)
		go func() { ran <- runner.Run(cycle, jobs, map[string][]string{c.PCI.Address: m.Collectors()}) }()
		<-rec.blocked
		if late {
			if out := <-ran; !out[c.PCI.Address].TimedOut {
				t.Fatalf("hors délai attendu : %+v", out)
			}
		}
		stop() // SIGTERM
		if runner.Drain(50 * time.Millisecond) {
			t.Fatalf("late=%v : l'arrêt n'attend pas la commande en cours", late)
		}
		close(rec.release)
		if !runner.Drain(2 * time.Second) {
			t.Fatalf("late=%v : la collecte ne rend pas la main après la commande", late)
		}
		if !late {
			<-ran
		}
		end()
		if got := rec.sent(); len(got) != 1 || got[0] != aacraid.IoctlGetHBAInfo {
			t.Fatalf("late=%v : ioctl après l'arrêt : %#x", late, got)
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
