// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DisasteR/hwraid_exporter/internal/module"
)

// Une carte bloquée ne retarde pas les autres, n'est pas relancée tant
// qu'elle n'a pas rendu la main, et son résultat tardif est ignoré.
func TestRunnerDeadlineAndSingleFlight(t *testing.T) {
	const fast, slow = "0000:01:00.0", "0000:02:00.0"
	release := make(chan struct{})
	var slowCalls atomic.Int32
	jobs := map[string]func() module.Result{
		fast: func() module.Result {
			return module.Result{NodeValid: true, Collected: map[string]bool{"volumes": true}}
		},
		slow: func() module.Result {
			n := slowCalls.Add(1)
			if n == 1 {
				<-release // premier appel : bloqué jusqu'à libération
				return module.Result{NodeValid: true, Collected: map[string]bool{"volumes": true}, Volumes: []module.Volume{{ID: "périmé"}}, CloseUnconfirmed: 2}
			}
			return module.Result{NodeValid: true, Collected: map[string]bool{"volumes": true}}
		},
	}
	cols := map[string][]string{fast: {"volumes"}, slow: {"volumes", "disks"}}
	r := NewRunner()

	start := time.Now()
	out := r.Run(within(t, 50*time.Millisecond), jobs, cols)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Run a attendu %v", d)
	}
	if !out[fast].NodeValid || out[fast].TimedOut {
		t.Fatalf("carte rapide : %+v", out[fast])
	}
	if s := out[slow]; !s.TimedOut || len(s.Collected) != 2 || s.Collected["volumes"] || s.Collected["disks"] {
		t.Fatalf("carte lente : %+v", s)
	}

	// Cycle suivant, toujours bloquée : pas de deuxième appel.
	out = r.Run(within(t, 10*time.Millisecond), jobs, cols)
	if !out[slow].TimedOut || slowCalls.Load() != 1 {
		t.Fatalf("relance d'une collecte bloquée (%d appels) : %+v", slowCalls.Load(), out[slow])
	}

	// Libérée : le résultat tardif est ignoré et une nouvelle collecte part.
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.mu.Lock()
		ch := r.inflight[slow]
		r.mu.Unlock()
		if len(ch) == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	out = r.Run(within(t, time.Second), jobs, cols)
	if s := out[slow]; s.TimedOut || len(s.Volumes) != 0 || slowCalls.Load() != 2 {
		t.Fatalf("résultat tardif repris ou pas de relance (%d appels) : %+v", slowCalls.Load(), s)
	}
	// Seules ses lectures rejetées sont reportées, une seule fois.
	if out[slow].CloseUnconfirmed != 2 {
		t.Fatalf("rejets 0xc5 du résultat tardif perdus : %+v", out[slow])
	}
	if out = r.Run(within(t, time.Second), jobs, cols); out[slow].CloseUnconfirmed != 0 {
		t.Fatalf("rejets tardifs comptés deux fois : %+v", out[slow])
	}
}

// Rendu d'une carte hors délai : collecteurs en échec, nœud non publié,
// dates de dernier succès conservées, aucune donnée d'état.
func TestTimedOutRender(t *testing.T) {
	const addr = "0000:01:00.0"
	p := plan(aacHost("host0", addr, "S1"))
	tr := NewTracker()
	t0 := time.Unix(1_000_000, 0)
	tr.Update(map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"volumes": true}}}, []string{addr}, t0)
	res := map[string]module.Result{addr: {TimedOut: true, Collected: map[string]bool{"volumes": false}}}
	tr.Update(res, []string{addr}, t0.Add(time.Minute))
	s := Build(p, true, res, tr, time.Minute)
	if !s.Controllers[0].TimedOut {
		t.Fatal("TimedOut non reporté")
	}
	out := Render(s)
	for _, want := range []string{
		`hwraid_collection_success{controller="serial:S1",collector="volumes"} 0`,
		`hwraid_collection_last_success_timestamp_seconds{controller="serial:S1",collector="volumes"} 1000000`,
	} {
		if !strings.Contains(out, want+"\n") {
			t.Fatalf("ligne absente : %s\n%s", want, out)
		}
	}
	if strings.Contains(out, "hwraid_management_node_valid{") {
		t.Fatalf("validité du nœud publiée sans observation :\n%s", out)
	}
}

// Drain attend une collecte abandonnée à l'échéance du cycle, et seulement
// elle : il rend false tant qu'elle est bloquée, true dès qu'elle finit.
func TestRunnerDrain(t *testing.T) {
	r := NewRunner()
	if !r.Drain(10 * time.Millisecond) {
		t.Fatal("aucune collecte : Drain doit réussir")
	}
	release := make(chan struct{})
	var finished atomic.Bool
	jobs := map[string]func() module.Result{"0000:01:00.0": func() module.Result {
		<-release
		finished.Store(true)
		return module.Result{}
	}}
	out := r.Run(within(t, 10*time.Millisecond), jobs, map[string][]string{"0000:01:00.0": {"volumes"}})
	if !out["0000:01:00.0"].TimedOut {
		t.Fatal("la collecte aurait dû dépasser l'échéance")
	}
	start := time.Now()
	if r.Drain(50 * time.Millisecond) {
		t.Fatal("Drain réussit alors que la collecte est bloquée")
	}
	if d := time.Since(start); d < 50*time.Millisecond || d > 2*time.Second {
		t.Fatalf("Drain n'a pas respecté son délai : %v", d)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(release)
	}()
	if !r.Drain(2 * time.Second) {
		t.Fatal("Drain échoue après la fin de la collecte")
	}
	if !finished.Load() {
		t.Fatal("Drain a rendu la main avant la fin de la collecte")
	}
}

// within rend un contexte qui se termine après d.
func within(t *testing.T, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

// Une collecte qui finit juste après l'échéance (ici, dès qu'elle la voit)
// n'est pas publiée, même reçue en même temps qu'elle ; ses lectures
// rejetées sont comptées. Même règle à l'arrêt du service (contexte annulé,
// sans échéance).
func TestRunnerSingleDeadline(t *testing.T) {
	const addr = "0000:01:00.0"
	cols := map[string][]string{addr: {"volumes"}}
	for _, name := range []string{"échéance", "arrêt"} {
		var ctx context.Context
		if name == "échéance" {
			ctx = within(t, 20*time.Millisecond)
		} else {
			c, cancel := context.WithCancel(context.Background())
			time.AfterFunc(20*time.Millisecond, cancel)
			ctx = c
		}
		jobs := map[string]func() module.Result{addr: func() module.Result {
			<-ctx.Done()
			return module.Result{NodeValid: true, Collected: map[string]bool{"volumes": true}, CloseUnconfirmed: 1}
		}}
		r := NewRunner()
		out := r.Run(ctx, jobs, cols)
		res := out[addr]
		if res.Collected["volumes"] || res.NodeValid {
			t.Fatalf("%s : résultat tardif publié : %+v", name, res)
		}
		r.Drain(time.Second)
		out = r.Run(within(t, time.Second), map[string]func() module.Result{addr: func() module.Result { return module.Result{} }}, cols)
		if res.CloseUnconfirmed+out[addr].CloseUnconfirmed != 1 {
			t.Fatalf("%s : rejet tardif compté %d fois", name, res.CloseUnconfirmed+out[addr].CloseUnconfirmed)
		}
	}
}

// Le rejet porté par un résultat tardif est récolté même si la carte ne
// figure pas au cycle suivant, puis reporté quand elle revient.
func TestRunnerHarvestsAbsentCard(t *testing.T) {
	const a, b = "0000:01:00.0", "0000:02:00.0"
	release := make(chan struct{})
	r := NewRunner()
	r.Run(within(t, 10*time.Millisecond), map[string]func() module.Result{a: func() module.Result {
		<-release
		return module.Result{CloseUnconfirmed: 3}
	}}, nil)
	close(release)
	r.Drain(time.Second)
	r.Run(within(t, time.Second), map[string]func() module.Result{b: func() module.Result { return module.Result{} }}, nil)
	r.mu.Lock()
	_, still := r.inflight[a]
	r.mu.Unlock()
	if still {
		t.Fatal("collecte tardive d'une carte absente non récoltée")
	}
	out := r.Run(within(t, time.Second), map[string]func() module.Result{a: func() module.Result { return module.Result{} }}, nil)
	if out[a].CloseUnconfirmed != 3 {
		t.Fatalf("rejets perdus : %+v", out[a])
	}
}

// Décision de publication : fin avant l'échéance, bornes comprises, et
// jamais après une annulation (arrêt du service).
func TestFinishedInTime(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	at := func(d time.Duration) time.Time { return t0.Add(d) }
	for _, c := range []struct {
		name     string
		at, dl   time.Time
		canceled bool
		want     bool
	}{
		{"sans échéance", at(0), time.Time{}, false, true},
		{"avant l'échéance", at(5 * time.Second), at(10 * time.Second), false, true},
		{"à l'échéance", at(10 * time.Second), at(10 * time.Second), false, true},
		{"après l'échéance", at(11 * time.Second), at(10 * time.Second), false, false},
		{"arrêt", at(5 * time.Second), at(10 * time.Second), true, false},
		{"arrêt sans échéance", at(0), time.Time{}, true, false},
	} {
		if got := finishedInTime(c.at, c.dl, c.canceled); got != c.want {
			t.Errorf("%s : %v, attendu %v", c.name, got, c.want)
		}
	}
}

// Les erreurs d'une collecte tardive écartée ne sont pas perdues : elles
// sont rendues une fois, pour le journal, avec le résultat suivant.
func TestRunnerKeepsLateErrors(t *testing.T) {
	const a = "0000:01:00.0"
	release := make(chan struct{})
	nomem := errors.New("ENOMEM sur la lecture 229")
	r := NewRunner()
	r.Run(within(t, 10*time.Millisecond), map[string]func() module.Result{a: func() module.Result {
		<-release
		return module.Result{CollectErr: nomem}
	}}, nil)
	close(release)
	r.Drain(time.Second)
	ok := map[string]func() module.Result{a: func() module.Result { return module.Result{} }}
	if out := r.Run(within(t, time.Second), ok, nil); !errors.Is(out[a].LateErr, nomem) {
		t.Fatalf("erreur tardive perdue : %+v", out[a])
	}
	if out := r.Run(within(t, time.Second), ok, nil); out[a].LateErr != nil {
		t.Fatalf("erreur tardive rendue deux fois : %v", out[a].LateErr)
	}
}

// Drain rappelé pendant une collecte bloquée, comme à l'arrêt toutes les
// 10 s : aucune goroutine ne s'accumule.
func TestRunnerDrainLeavesNothing(t *testing.T) {
	const a = "0000:01:00.0"
	release := make(chan struct{})
	r := NewRunner()
	r.Run(within(t, 10*time.Millisecond), map[string]func() module.Result{a: func() module.Result {
		<-release
		return module.Result{}
	}}, nil)
	r.Drain(time.Millisecond)
	before := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		if r.Drain(time.Millisecond) {
			t.Fatal("Drain réussit pendant une collecte bloquée")
		}
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("%d goroutines après 50 appels à Drain, %d avant", after, before)
	}
	close(release)
	if !r.Drain(2 * time.Second) {
		t.Fatal("Drain échoue après la fin de la collecte")
	}
}
