// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/DisasteR/hwraid_exporter/internal/module"
)

// Runner collecte les cartes en parallèle, chacune dans sa goroutine, et
// n'attend aucune au-delà de la fin du contexte du cycle (son échéance ou
// l'arrêt du service) : une carte qui ne répond pas ne retarde pas la
// publication des autres.
//
// Un appel au firmware bloqué ne peut pas être interrompu. Tant que la
// collecte d'une carte n'a pas rendu la main, aucune autre n'est lancée pour
// cette carte : les cycles suivants la publient hors délai sans empiler de
// requêtes sur un firmware en difficulté.
type Runner struct {
	mu       sync.Mutex
	inflight map[string]chan done // adresse PCI → collecte en cours
	// active compte les collectes non terminées ; idle, s'il existe, est
	// fermé quand active retombe à zéro. Aucune goroutine d'attente : un
	// appel répété à Drain ne laisse rien derrière lui.
	active int
	idle   chan struct{}
	// late garde, par carte, les lectures rejetées (clôture 0xc5) portées
	// par un résultat tardif écarté : elles ont bien eu lieu et sont
	// reportées dans le prochain résultat de la carte.
	late map[string]int
	// lateErr garde de même les erreurs de ces résultats tardifs, pour le
	// journal.
	lateErr map[string]error
}

// done est le résultat d'une collecte et l'heure à laquelle elle a fini.
type done struct {
	res module.Result
	at  time.Time
}

// NewRunner crée un ordonnanceur sans collecte en cours.
func NewRunner() *Runner {
	return &Runner{inflight: map[string]chan done{}, late: map[string]int{}, lateErr: map[string]error{}}
}

// keepLate garde ce qui reste utile d'un résultat tardif écarté : ses
// lectures rejetées et ses erreurs. Appelé verrou tenu.
func (r *Runner) keepLate(addr string, res module.Result) {
	r.late[addr] += res.CloseUnconfirmed
	if e := errors.Join(res.Err, res.CollectErr); e != nil {
		r.lateErr[addr] = errors.Join(r.lateErr[addr], e)
	}
}

// harvest récupère, sans attendre, les collectes tardives terminées, pour
// toutes les cartes : leurs données sont périmées, seules leurs lectures
// rejetées sont gardées. Appelé verrou tenu.
func (r *Runner) harvest() {
	for addr, ch := range r.inflight {
		select {
		case d := <-ch:
			r.keepLate(addr, d.res)
			delete(r.inflight, addr)
		default:
		}
	}
}

// Run collecte les cartes données (adresse PCI → fonction de collecte) et
// renvoie un résultat pour chacune : celui de la collecte si elle a fini
// avant l'échéance de ctx, sinon un résultat TimedOut dont tous les
// collecteurs (collectors[adresse]) sont en échec. Si ctx est annulé (arrêt
// du service), aucun résultat n'est retenu. ctx est le contexte transmis aux
// collectes : une seule échéance décide de l'arrêt des commandes et de la
// publication.
func (r *Runner) Run(ctx context.Context, jobs map[string]func() module.Result, collectors map[string][]string) map[string]module.Result {
	waiting := map[string]chan done{}
	r.mu.Lock()
	r.harvest()
	for addr, job := range jobs {
		if _, busy := r.inflight[addr]; busy {
			continue // toujours bloquée : pas de nouvelle collecte
		}
		ch := make(chan done, 1)
		r.inflight[addr] = ch
		waiting[addr] = ch
		r.active++
		go func() {
			defer r.finished()
			res := job()
			ch <- done{res: res, at: time.Now()}
		}()
	}
	r.mu.Unlock()

	dl, _ := ctx.Deadline()
	inTime := func(d done) bool {
		return finishedInTime(d.at, dl, errors.Is(ctx.Err(), context.Canceled))
	}
	out := make(map[string]module.Result, len(jobs))
	for addr, ch := range waiting {
		var d done
		select {
		case d = <-ch:
		case <-ctx.Done():
			select {
			case d = <-ch:
			default:
				continue // toujours en cours : récoltée plus tard
			}
		}
		r.mu.Lock()
		delete(r.inflight, addr)
		if inTime(d) {
			out[addr] = d.res
		} else {
			r.keepLate(addr, d.res)
		}
		r.mu.Unlock()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for addr := range jobs {
		if _, ok := out[addr]; !ok {
			failed := map[string]bool{}
			for _, n := range collectors[addr] {
				failed[n] = false
			}
			out[addr] = module.Result{TimedOut: true, Collected: failed}
		}
		// À l'arrêt (contexte annulé), les erreurs restent dans l'ordonnanceur :
		// le cycle ne sera pas publié, LateErrors les rendra pour le journal.
		if errors.Is(ctx.Err(), context.Canceled) {
			continue
		}
		if n, e := r.late[addr], r.lateErr[addr]; n > 0 || e != nil {
			res := out[addr]
			res.CloseUnconfirmed += n
			res.LateErr = e
			out[addr] = res
			delete(r.late, addr)
			delete(r.lateErr, addr)
		}
	}
	return out
}

// finishedInTime : une collecte finie à at n'est publiée que si elle a fini
// avant l'échéance dl (zéro : aucune), bornes comprises, et si le contexte
// n'a pas été annulé (arrêt du service : rien n'est publié). Un résultat
// reçu en même temps que l'échéance n'est donc retenu que s'il l'a précédée.
func finishedInTime(at, dl time.Time, canceled bool) bool {
	if canceled {
		return false
	}
	return dl.IsZero() || !at.After(dl)
}

// LateErrors récolte les collectes terminées après leur échéance et rend,
// par adresse PCI, les erreurs qu'elles portent et qu'aucun cycle n'a encore
// rendues. Appelé à l'arrêt, après Drain, pour qu'aucune erreur de clôture
// ne disparaisse du journal.
func (r *Runner) LateErrors() map[string]error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.harvest()
	out := r.lateErr
	r.lateErr = map[string]error{}
	return out
}

// finished retire une collecte terminée et réveille Drain s'il n'en reste
// aucune.
func (r *Runner) finished() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active--
	if r.active == 0 && r.idle != nil {
		close(r.idle)
		r.idle = nil
	}
}

// Drain attend, au plus timeout, la fin des collectes encore en cours,
// y compris celles abandonnées à l'échéance d'un cycle. Il rend false si
// l'une d'elles n'a pas rendu la main à temps. Il peut être rappelé autant
// de fois que nécessaire : il ne crée aucune goroutine.
//
// À l'arrêt du service, sortir pendant une commande de gestion l'interrompt
// dans le pilote, qui perd alors un bloc de commande (voir aacio).
func (r *Runner) Drain(timeout time.Duration) bool {
	r.mu.Lock()
	if r.active == 0 {
		r.mu.Unlock()
		return true
	}
	if r.idle == nil {
		r.idle = make(chan struct{})
	}
	idle := r.idle
	r.mu.Unlock()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-idle:
		return true
	case <-t.C:
		return false
	}
}
