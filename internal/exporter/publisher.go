// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"sync"
	"sync/atomic"
	"time"
)

// Publisher garde la vue servie par /metrics. À l'arrêt du service, Stop la
// remplace par une vue sans valeurs matérielles et refuse toute publication
// ultérieure : pendant l'attente d'une commande bloquée, qui peut durer,
// /metrics ne sert pas un état de la carte devenu périmé.
//
// Une vue plus ancienne que maxAge (boucle de collecte bloquée, par
// exemple dans la découverte) n'est plus servie : empty la remplace.
type Publisher struct {
	mu      sync.Mutex
	cur     atomic.Pointer[view]
	stopped bool
	empty   string
	maxAge  time.Duration
	now     func() time.Time
}

// view est une vue servie et l'heure de sa publication (horloge monotone ;
// zéro : vue initiale ou d'arrêt, jamais périmée).
type view struct {
	text string
	at   time.Time
}

// ViewMaxAge est l'âge au-delà duquel la vue n'est plus servie, pour un
// intervalle de collecte et un délai de cycle donnés. Une nouvelle vue est
// publiée au plus « intervalle + délai » après la précédente, même hors
// délai (la boucle n'attend pas une carte bloquée au-delà de l'échéance) :
// un délai de plus sert de marge. Au-delà, la boucle elle-même est figée,
// et une observation resservie ne doit pas suffire à remplir le « for » des
// règles d'état (5 min) tant que l'intervalle reste sous 4 min (délai de
// 30 s).
func ViewMaxAge(interval, timeout time.Duration) time.Duration {
	return interval + 2*timeout
}

// NewPublisher crée un publieur qui sert initial ; empty est la vue sans
// valeur matérielle servie à l'arrêt ou quand la vue a plus de maxAge.
func NewPublisher(initial, empty string, maxAge time.Duration) *Publisher {
	p := &Publisher{empty: empty, maxAge: maxAge, now: time.Now}
	p.cur.Store(&view{text: initial})
	return p
}

// Publish remplace la vue servie, sauf après Stop ; il rend false s'il l'a
// refusée.
func (p *Publisher) Publish(s string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return false
	}
	p.cur.Store(&view{text: s, at: p.now()})
	return true
}

// Stop sert désormais la vue sans valeur matérielle et refuse les
// publications suivantes.
func (p *Publisher) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
	p.cur.Store(&view{text: p.empty})
}

// Load rend la vue servie, ou la vue sans valeur matérielle si la dernière
// publication a plus de maxAge.
func (p *Publisher) Load() string {
	v := p.cur.Load()
	if !v.at.IsZero() && p.maxAge > 0 && p.now().Sub(v.at) > p.maxAge {
		return p.empty
	}
	return v.text
}
