// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"sync"
	"sync/atomic"
)

// Publisher garde la vue servie par /metrics. À l'arrêt du service, Stop la
// remplace par une vue sans valeurs matérielles et refuse toute publication
// ultérieure : pendant l'attente d'une commande bloquée, qui peut durer,
// /metrics ne sert pas un état de la carte devenu périmé.
type Publisher struct {
	mu      sync.Mutex
	cur     atomic.Pointer[string]
	stopped bool
}

// NewPublisher crée un publieur qui sert initial.
func NewPublisher(initial string) *Publisher {
	p := &Publisher{}
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
	return true
}

// Stop sert désormais final et refuse les publications suivantes.
func (p *Publisher) Stop(final string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
	p.cur.Store(&final)
}

// Load rend la vue servie.
func (p *Publisher) Load() string { return *p.cur.Load() }
