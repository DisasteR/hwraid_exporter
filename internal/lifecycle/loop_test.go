// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Arrêt déjà demandé au démarrage : aucun cycle, pas même le premier.
func TestRunLoopStoppedBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	RunLoop(ctx, time.Millisecond, func(context.Context) { calls++ })
	if calls != 0 {
		t.Fatalf("%d cycle(s) lancé(s) après l'arrêt", calls)
	}
}

// Arrêt pendant un cycle bloqué sur une commande, au premier cycle comme à
// un cycle lancé par l'horloge : le cycle en cours voit l'arrêt, la boucle
// attend sa fin et n'en lance pas d'autre.
func TestRunLoopStopsDuringCycle(t *testing.T) {
	for _, blockAt := range []int{1, 2} {
		ctx, cancel := context.WithCancel(context.Background())
		inCycle := make(chan struct{})
		release := make(chan struct{})
		sawStop := make(chan bool, 1)
		calls := 0
		done := make(chan struct{})
		go func() {
			defer close(done)
			RunLoop(ctx, time.Millisecond, func(c context.Context) {
				calls++
				if calls != blockAt {
					return
				}
				close(inCycle)
				<-release // commande en cours
				sawStop <- c.Err() != nil
			})
		}()
		<-inCycle
		cancel()
		select {
		case <-done:
			t.Fatalf("cycle %d : la boucle rend la main pendant une commande", blockAt)
		case <-time.After(50 * time.Millisecond):
		}
		close(release)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("cycle %d : la boucle ne s'arrête pas après le cycle", blockAt)
		}
		if !<-sawStop {
			t.Fatalf("cycle %d : le cycle en cours ne voit pas l'arrêt", blockAt)
		}
		if calls != blockAt {
			t.Fatalf("%d cycles, %d attendus", calls, blockAt)
		}
	}
}

// WaitIdle n'abandonne jamais : il attend la boucle puis les collectes en
// cours, quelle que soit la durée, en avertissant à intervalle régulier.
func TestWaitIdleWaitsWithoutLimit(t *testing.T) {
	loop := make(chan struct{})
	var mu sync.Mutex
	drainCalls, warns := 0, 0
	drained := make(chan struct{})
	drain := func(d time.Duration) bool {
		select {
		case <-drained:
			return true
		case <-time.After(d):
			mu.Lock()
			drainCalls++
			mu.Unlock()
			return false
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		WaitIdle(loop, drain, 10*time.Millisecond, func(time.Duration) { mu.Lock(); warns++; mu.Unlock() })
	}()
	time.Sleep(50 * time.Millisecond) // boucle encore active
	mu.Lock()
	early := warns
	mu.Unlock()
	if early < 2 {
		t.Fatalf("%d avertissement(s) pendant que la boucle tourne encore", early)
	}
	close(loop)
	time.Sleep(50 * time.Millisecond) // collecte encore bloquée
	select {
	case <-done:
		t.Fatal("WaitIdle rend la main pendant une collecte bloquée")
	default:
	}
	close(drained)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitIdle ne rend pas la main après la fin de la collecte")
	}
	mu.Lock()
	defer mu.Unlock()
	if warns < 4 || drainCalls == 0 {
		t.Fatalf("%d avertissement(s), %d attente(s) de drain", warns, drainCalls)
	}
}
