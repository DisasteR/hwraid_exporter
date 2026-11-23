// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"sync"
	"time"

	"github.com/DisasteR/hwraid_exporter/internal/module"
)

// Runner collecte les cartes en parallèle, chacune dans sa goroutine, et
// n'attend aucune au-delà de l'échéance du cycle : une carte qui ne répond
// pas ne retarde pas la publication des autres.
//
// Un appel au firmware bloqué ne peut pas être interrompu. Tant que la
// collecte d'une carte n'a pas rendu la main, aucune autre n'est lancée pour
// cette carte : les cycles suivants la publient hors délai sans empiler de
// requêtes sur un firmware en difficulté.
type Runner struct {
	mu       sync.Mutex
	inflight map[string]chan module.Result // adresse PCI → collecte en cours
	running  sync.WaitGroup                // collectes non terminées
}

// NewRunner crée un ordonnanceur sans collecte en cours.
func NewRunner() *Runner { return &Runner{inflight: map[string]chan module.Result{}} }

// Run collecte les cartes données (adresse PCI → fonction de collecte) et
// renvoie un résultat pour chacune : celui de la collecte si elle a abouti
// avant timeout, sinon un résultat TimedOut dont tous les collecteurs
// (collectors[adresse]) sont en échec.
func (r *Runner) Run(jobs map[string]func() module.Result, collectors map[string][]string, timeout time.Duration) map[string]module.Result {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	waiting := map[string]chan module.Result{}
	r.mu.Lock()
	for addr, job := range jobs {
		if ch, busy := r.inflight[addr]; busy {
			select {
			case <-ch:
				// Résultat tardif d'un cycle passé : périmé, ignoré.
			default:
				continue // toujours bloquée : pas de nouvelle collecte
			}
		}
		ch := make(chan module.Result, 1)
		r.inflight[addr] = ch
		waiting[addr] = ch
		r.running.Add(1)
		go func() {
			defer r.running.Done()
			ch <- job()
		}()
	}
	r.mu.Unlock()

	out := make(map[string]module.Result, len(jobs))
	expired := false
	for addr, ch := range waiting {
		if !expired {
			select {
			case res := <-ch:
				out[addr] = res
				continue
			case <-deadline.C:
				expired = true
			}
		}
		// Échéance passée : ne prendre que ce qui est déjà arrivé.
		select {
		case res := <-ch:
			out[addr] = res
		default:
		}
	}
	r.mu.Lock()
	for addr := range out {
		delete(r.inflight, addr)
	}
	r.mu.Unlock()
	for addr := range jobs {
		if _, ok := out[addr]; !ok {
			failed := map[string]bool{}
			for _, n := range collectors[addr] {
				failed[n] = false
			}
			out[addr] = module.Result{TimedOut: true, Collected: failed}
		}
	}
	return out
}

// Drain attend, au plus timeout, la fin des collectes encore en cours,
// y compris celles abandonnées à l'échéance d'un cycle. Il rend false si
// l'une d'elles n'a pas rendu la main à temps.
//
// À l'arrêt du service, sortir pendant une commande de gestion l'interrompt
// dans le pilote, qui perd alors un bloc de commande (voir aacio).
func (r *Runner) Drain(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		r.running.Wait()
		close(done)
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}
