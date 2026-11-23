package exporter

import (
	"time"

	"github.com/DisasteR/hwraid_exporter/internal/module"
)

// Tracker garde, d'un cycle à l'autre, la date du dernier succès de chaque
// collecteur de chaque carte (hwraid_collection_last_success_timestamp_seconds).
// Il n'est utilisé que par la boucle de collecte, sans accès concurrent.
type Tracker struct {
	last map[string]map[string]time.Time // adresse PCI → collecteur → date
}

// NewTracker crée un suivi vide.
func NewTracker() *Tracker { return &Tracker{last: map[string]map[string]time.Time{}} }

// Update enregistre les succès du cycle terminé à now et oublie les cartes
// qui n'ont plus de résultat (carte retirée).
func (t *Tracker) Update(results map[string]module.Result, now time.Time) {
	for addr := range t.last {
		if _, ok := results[addr]; !ok {
			delete(t.last, addr)
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

// Last renvoie les dates connues pour une carte.
func (t *Tracker) Last(addr string) map[string]time.Time { return t.last[addr] }
