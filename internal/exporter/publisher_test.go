// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"strings"
	"testing"
)

// Après Stop, la vue servie est celle de l'arrêt, sans valeur matérielle,
// et un cycle qui se termine plus tard ne la remplace pas.
func TestPublisherStop(t *testing.T) {
	p := NewPublisher("initiale")
	if !p.Publish("cycle 1") || p.Load() != "cycle 1" {
		t.Fatalf("publication refusée avant l'arrêt : %q", p.Load())
	}
	stopped := Render(Snapshot{})
	p.Stop(stopped)
	if p.Publish("cycle 2, terminé pendant l'arrêt") {
		t.Fatal("publication acceptée après l'arrêt")
	}
	if got := p.Load(); got != stopped {
		t.Fatalf("vue servie après l'arrêt : %q", got)
	}
	if !strings.Contains(stopped, "hwraid_snapshot_ready 0\n") || strings.Contains(stopped, "hwraid_volume_state{") {
		t.Fatalf("vue d'arrêt inattendue :\n%s", stopped)
	}
}
