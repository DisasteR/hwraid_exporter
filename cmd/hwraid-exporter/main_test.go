// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package main

import (
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
