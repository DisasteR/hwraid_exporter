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
}

// NewTracker crée un suivi vide.
func NewTracker() *Tracker {
	return &Tracker{last: map[string]map[string]time.Time{}, serial: map[string]string{}}
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
	for addr, r := range results {
		for col, ok := range r.Collected {
			if !ok {
				continue
			}
			if t.last[addr] == nil {
				t.last[addr] = map[string]time.Time{}
			}
			t.last[addr][col] = now
		}
	}
}

// Remember enregistre les identités serial: établies dans un instantané.
func (t *Tracker) Remember(s Snapshot) {
	if t == nil {
		return
	}
	for _, c := range s.Controllers {
		if c.IdentitySource == "serial" {
			t.serial[c.PCIAddress] = c.Label
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
