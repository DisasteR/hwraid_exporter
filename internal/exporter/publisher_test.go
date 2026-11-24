// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"strings"
	"testing"
	"time"
)

// Après Stop, la vue servie est celle de l'arrêt, sans valeur matérielle,
// et un cycle qui se termine plus tard ne la remplace pas.
func TestPublisherStop(t *testing.T) {
	empty := Render(Snapshot{})
	p := NewPublisher("initiale", empty, time.Hour)
	if !p.Publish("cycle 1") || p.Load() != "cycle 1" {
		t.Fatalf("publication refusée avant l'arrêt : %q", p.Load())
	}
	p.Stop()
	if p.Publish("cycle 2, terminé pendant l'arrêt") {
		t.Fatal("publication acceptée après l'arrêt")
	}
	if got := p.Load(); got != empty {
		t.Fatalf("vue servie après l'arrêt : %q", got)
	}
	if !strings.Contains(empty, "hwraid_snapshot_ready 0\n") || strings.Contains(empty, "hwraid_volume_state{") {
		t.Fatalf("vue d'arrêt inattendue :\n%s", empty)
	}
}

// Boucle de collecte bloquée : la dernière vue n'est plus servie au-delà de
// maxAge, et l'est de nouveau dès la publication suivante.
func TestPublisherStaleView(t *testing.T) {
	clock := time.Unix(1_000_000, 0)
	p := NewPublisher("initiale", "vide", 5*time.Minute)
	p.now = func() time.Time { return clock }
	if p.Load() != "initiale" {
		t.Fatal("vue initiale non servie")
	}
	p.Publish("cycle")
	clock = clock.Add(5 * time.Minute)
	if p.Load() != "cycle" {
		t.Fatal("vue fraîche non servie à la limite")
	}
	clock = clock.Add(time.Second)
	if p.Load() != "vide" {
		t.Fatal("vue périmée encore servie")
	}
	p.Publish("cycle suivant")
	if p.Load() != "cycle suivant" {
		t.Fatal("vue non rétablie après une publication")
	}
}

// ViewMaxAge : une vue n'est retirée que si la boucle n'a rien publié
// pendant au moins l'écart normal entre deux vues (intervalle + délai) plus
// un intervalle entier de marge pour la découverte et le rendu, qui ne
// comptent pas dans le délai de cycle. Un délai court ne réduit pas cette
// marge.
func TestViewMaxAge(t *testing.T) {
	for _, interval := range []time.Duration{15 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute} {
		for _, timeout := range []time.Duration{time.Second, min(30*time.Second, interval)} {
			got := ViewMaxAge(interval, timeout)
			if need := 2*interval + timeout; got < need {
				t.Errorf("intervalle %v, délai %v : vue retirée après %v, avant %v (cycle lent mais vivant)", interval, timeout, got, need)
			}
			// Borne annoncée (docs/metrics.md, CHANGELOG) : une boucle figée
			// ne sert pas sa vue plus longtemps.
			if promised := 3*interval + timeout; got > promised {
				t.Errorf("intervalle %v, délai %v : vue servie %v, au-delà des %v annoncés", interval, timeout, got, promised)
			}
		}
	}
}
