// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"time"

	"github.com/DisasteR/hwraid_exporter/internal/module"
)

// Tracker garde, d'un cycle à l'autre, la date du dernier succès de chaque
// collecteur de chaque carte (hwraid_collection_last_success_timestamp_seconds).
// Il n'est utilisé que par la boucle de collecte, sans accès concurrent.
//
// Il garde aussi l'identité `serial:` établie pour chaque adresse PCI : une
// lecture temporairement impossible du numéro de série (attribut vide) ne
// renomme pas toutes les séries de la carte.
type Tracker struct {
	last   map[string]map[string]time.Time // adresse PCI → collecteur → date
	serial map[string]string               // adresse PCI → label serial:… établi
	closes map[string]uint64               // adresse PCI → clôtures non nominales cumulées
	// held : collecteurs servis par une réponse gardée en mémoire par le
	// module (Cached) et réussis lors de leur dernière collecte. Une carte
	// hors délai les sert toujours : ils ne sont pas publiés en échec.
	held map[string]map[string]bool
}

// NewTracker crée un suivi vide.
func NewTracker() *Tracker {
	return &Tracker{last: map[string]map[string]time.Time{}, serial: map[string]string{}, closes: map[string]uint64{},
		held: map[string]map[string]bool{}}
}

// Update enregistre les succès du cycle terminé à now. Une carte n'est
// oubliée que lorsqu'elle quitte l'inventaire (present), pas lorsque sa
// collecte n'a pas eu lieu.
func (t *Tracker) Update(results map[string]module.Result, present []string, now time.Time) {
	keep := map[string]bool{}
	for _, a := range present {
		keep[a] = true
	}
	for addr := range t.last {
		if !keep[addr] {
			delete(t.last, addr)
		}
	}
	for addr := range t.serial {
		if !keep[addr] {
			delete(t.serial, addr)
		}
	}
	for addr := range t.closes {
		if !keep[addr] {
			delete(t.closes, addr)
		}
	}
	for addr := range t.held {
		if !keep[addr] {
			delete(t.held, addr)
		}
	}
	for addr, r := range results {
		if r.CloseUnconfirmed > 0 {
			t.closes[addr] += uint64(r.CloseUnconfirmed)
		}
	}
	for addr, r := range results {
		cached := map[string]bool{}
		for _, c := range r.Cached {
			cached[c] = true
			delete(t.last[addr], c)
			if r.Collected[c] {
				if t.held[addr] == nil {
					t.held[addr] = map[string]bool{}
				}
				t.held[addr][c] = true
			} else {
				delete(t.held[addr], c)
			}
		}
		for col, ok := range r.Collected {
			if !ok || cached[col] {
				continue
			}
			if t.last[addr] == nil {
				t.last[addr] = map[string]time.Time{}
			}
			t.last[addr][col] = now
		}
	}
}

// Held rend les collecteurs de la carte servis par une réponse gardée en
// mémoire et réussis lors de leur dernière collecte.
func (t *Tracker) Held(addr string) map[string]bool {
	if t == nil {
		return nil
	}
	return t.held[addr]
}

// Remember enregistre les identités serial: établies dans un instantané.
func (t *Tracker) Remember(s Snapshot) {
	if t == nil {
		return
	}
	for _, c := range s.Controllers {
		if c.IdentitySource != "serial" {
			continue
		}
		t.serial[c.PCIAddress] = c.Label
		// Un numéro désormais porté par une autre carte n'identifie plus
		// celle qui l'avait.
		for addr, l := range t.serial {
			if l == c.Label && addr != c.PCIAddress {
				delete(t.serial, addr)
			}
		}
	}
}

// serialLabel renvoie l'identité serial: déjà établie pour une adresse.
func (t *Tracker) serialLabel(addr string) string {
	if t == nil {
		return ""
	}
	return t.serial[addr]
}

// Closes renvoie le cumul des clôtures non nominales d'une carte.
func (t *Tracker) Closes(addr string) uint64 {
	if t == nil {
		return 0
	}
	return t.closes[addr]
}

// Known renvoie les adresses des cartes suivies.
func (t *Tracker) Known() []string {
	var out []string
	for a := range t.last {
		out = append(out, a)
	}
	for a := range t.serial {
		if _, ok := t.last[a]; !ok {
			out = append(out, a)
		}
	}
	return out
}

// Last renvoie les dates connues pour une carte.
func (t *Tracker) Last(addr string) map[string]time.Time {
	if t == nil {
		return nil
	}
	return t.last[addr]
}
