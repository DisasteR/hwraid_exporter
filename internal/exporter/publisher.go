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
	cur     atomic.Pointer[string]
	at      atomic.Int64 // date de la dernière publication (UnixNano), 0 : aucune
	stopped bool
	empty   string
	maxAge  time.Duration
	now     func() time.Time
}

// NewPublisher crée un publieur qui sert initial ; empty est la vue sans
// valeur matérielle servie à l'arrêt ou quand la vue a plus de maxAge.
func NewPublisher(initial, empty string, maxAge time.Duration) *Publisher {
	p := &Publisher{empty: empty, maxAge: maxAge, now: time.Now}
	p.cur.Store(&initial)
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
	p.cur.Store(&s)
	p.at.Store(p.now().UnixNano())
	return true
}

// Stop sert désormais la vue sans valeur matérielle et refuse les
// publications suivantes.
func (p *Publisher) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
	p.cur.Store(&p.empty)
}

// Load rend la vue servie, ou la vue sans valeur matérielle si la dernière
// publication a plus de maxAge.
func (p *Publisher) Load() string {
	if at := p.at.Load(); at != 0 && p.maxAge > 0 && p.now().Sub(time.Unix(0, at)) > p.maxAge {
		return p.empty
	}
	return *p.cur.Load()
}
