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
	// unsupported : collecteurs déclarés non pris en charge par la dernière
	// collecte effective ; conservés pendant un dépassement de délai.
	unsupported map[string][]string
	// ident : identité de la carte (Controller.Identity) vue au dernier
	// instantané. Quand elle change à la même adresse, tout l'historique
	// de l'adresse (dates, compteur, held, unsupported) est oublié : il
	// appartenait à une autre carte, ou à un autre firmware.
	ident map[string]string
	// cycle : apports du dernier Update seul, par adresse (succès,
	// clôtures 0xc5, réponses gardées). Au premier cycle d'une nouvelle
	// identité, ils sont publiés et conservés, sans l'historique de l'ancienne.
	cycle map[string]contrib
}

// contrib est l'apport d'un cycle pour une carte.
type contrib struct {
	last   map[string]time.Time
	closes uint64
	held   map[string]bool
}

// NewTracker crée un suivi vide.
func NewTracker() *Tracker {
	return &Tracker{last: map[string]map[string]time.Time{}, serial: map[string]string{}, closes: map[string]uint64{},
		held: map[string]map[string]bool{}, unsupported: map[string][]string{}, ident: map[string]string{}}
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
	for addr := range t.unsupported {
		if !keep[addr] {
			delete(t.unsupported, addr)
		}
	}
	for addr := range t.ident {
		if !keep[addr] {
			delete(t.ident, addr)
		}
	}
	t.cycle = map[string]contrib{}
	for addr, r := range results {
		c := contrib{last: map[string]time.Time{}, held: map[string]bool{}, closes: uint64(r.CloseUnconfirmed)}
		cached := map[string]bool{}
		for _, col := range r.Cached {
			cached[col] = true
			if r.Collected[col] {
				c.held[col] = true
			}
		}
		for col, ok := range r.Collected {
			if ok && !cached[col] {
				c.last[col] = now
			}
		}
		t.cycle[addr] = c
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

// Same indique si l'historique gardé pour addr appartient à la carte
// d'identité id (ou s'il n'y a pas encore d'identité connue).
func (t *Tracker) Same(addr, id string) bool {
	if t == nil {
		return false
	}
	old, ok := t.ident[addr]
	return !ok || old == id
}

// Held rend les collecteurs de la carte servis par une réponse gardée en
// mémoire et réussis lors de leur dernière collecte. À n'utiliser que si
// Same est vrai.
func (t *Tracker) Held(addr string) map[string]bool {
	if t == nil {
		return nil
	}
	return t.held[addr]
}

// Unsupported rend les collecteurs déclarés non pris en charge par la
// dernière collecte effective de la carte. À n'utiliser que si Same est vrai.
func (t *Tracker) Unsupported(addr string) []string {
	if t == nil {
		return nil
	}
	return t.unsupported[addr]
}

// forget oublie l'historique d'une adresse, puis y reporte les apports du
// dernier cycle, qui appartiennent à la carte présente.
func (t *Tracker) forget(addr string) {
	delete(t.last, addr)
	delete(t.closes, addr)
	delete(t.held, addr)
	delete(t.unsupported, addr)
	c, ok := t.cycle[addr]
	if !ok {
		return
	}
	if len(c.last) > 0 {
		t.last[addr] = c.last
	}
	if c.closes > 0 {
		t.closes[addr] = c.closes
	}
	if len(c.held) > 0 {
		t.held[addr] = c.held
	}
}

// CycleOnly rend, pour une carte dont l'identité vient de changer, une vue
// limitée aux apports du dernier cycle.
func (t *Tracker) CycleOnly(addr string) *Tracker {
	v := NewTracker()
	if t == nil {
		return v
	}
	if c, ok := t.cycle[addr]; ok {
		if len(c.last) > 0 {
			v.last[addr] = c.last
		}
		v.closes[addr] = c.closes
		if len(c.held) > 0 {
			v.held[addr] = c.held
		}
	}
	return v
}

// Remember enregistre les identités serial: établies dans un instantané.
func (t *Tracker) Remember(s Snapshot) {
	if t == nil {
		return
	}
	for _, c := range s.Controllers {
		// Changement d'identité observé, même pendant un dépassement de
		// délai ou sans collecte effective : l'historique est oublié.
		if old, ok := t.ident[c.PCIAddress]; ok && old != c.Identity {
			t.forget(c.PCIAddress)
		}
		t.ident[c.PCIAddress] = c.Identity
		if !c.TimedOut && c.NodeValid {
			t.unsupported[c.PCIAddress] = append([]string(nil), c.Unsupported...)
		}
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
