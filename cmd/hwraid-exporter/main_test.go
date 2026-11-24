// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"
	"time"
)

func TestWaitIdle(t *testing.T) {
	drained := func(got *time.Duration, ok bool) func(time.Duration) bool {
		return func(d time.Duration) bool { *got = d; return ok }
	}

	// Boucle bloquée : échec au bout du délai, drain jamais appelé.
	var left time.Duration = -1
	start := time.Now()
	if waitIdle(make(chan struct{}), drained(&left, true), 50*time.Millisecond) {
		t.Fatal("boucle bloquée : waitIdle doit échouer")
	}
	if d := time.Since(start); d < 50*time.Millisecond || d > 2*time.Second {
		t.Fatalf("délai non respecté : %v", d)
	}
	if left != -1 {
		t.Fatal("drain appelé alors que la boucle n'a pas fini")
	}

	// Boucle finie après 30 ms : drain reçoit le reste du délai commun.
	loop := make(chan struct{})
	go func() { time.Sleep(30 * time.Millisecond); close(loop) }()
	if !waitIdle(loop, drained(&left, true), time.Second) {
		t.Fatal("tout est fini : waitIdle doit réussir")
	}
	if left <= 0 || left > 970*time.Millisecond {
		t.Fatalf("drain a reçu %v, reste du délai attendu (< 970 ms)", left)
	}

	// Collecte abandonnée encore bloquée : l'échec de drain est rendu.
	done := make(chan struct{})
	close(done)
	if waitIdle(done, drained(&left, false), time.Second) {
		t.Fatal("échec de drain ignoré")
	}
}

// Arrêt déjà demandé au démarrage : aucun cycle, pas même le premier.
func TestRunLoopStoppedBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	runLoop(ctx, time.Millisecond, func(context.Context) { calls++ })
	if calls != 0 {
		t.Fatalf("%d cycle(s) lancé(s) après l'arrêt", calls)
	}
}

// Arrêt pendant un cycle bloqué sur une commande, au premier cycle comme à
// un cycle lancé par l'horloge : le cycle en cours voit l'arrêt (il cesse
// d'envoyer des commandes), la boucle attend sa fin et n'en lance pas
// d'autre.
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
			runLoop(ctx, time.Millisecond, func(c context.Context) {
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
