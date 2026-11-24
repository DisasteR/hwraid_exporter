// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"context"
	"errors"
	"runtime"
	"strconv"
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
	if out[slow].LateCloseUnconfirmed != 2 {
		t.Fatalf("rejets 0xc5 du résultat tardif perdus : %+v", out[slow])
	}
	if out = r.Run(within(t, time.Second), jobs, cols); out[slow].LateCloseUnconfirmed != 0 {
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
	tr.Update(map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"volumes": true, "capabilities": true},
		Cached: []string{"capabilities"}}}, []string{addr}, t0)
	res := map[string]module.Result{addr: {TimedOut: true, Collected: map[string]bool{"volumes": false, "capabilities": false}}}
	tr.Remember(Build(p, true, map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"volumes": true, "capabilities": true},
		Cached: []string{"capabilities"}}}, tr, time.Minute))
	tr.Update(res, []string{addr}, t0.Add(time.Minute))
	s := Build(p, true, res, tr, time.Minute)
	if !s.Controllers[0].TimedOut {
		t.Fatal("TimedOut non reporté")
	}
	out := Render(s)
	for _, want := range []string{
		`hwraid_collection_success{controller="serial:S1",collector="volumes"} 0`,
		`hwraid_collection_last_success_timestamp_seconds{controller="serial:S1",collector="volumes"} 1000000`,
		// Réponse 706 gardée en mémoire : toujours valide pendant le blocage.
		`hwraid_collection_success{controller="serial:S1",collector="capabilities"} 1`,
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
		if n := res.CloseUnconfirmed + res.LateCloseUnconfirmed + out[addr].LateCloseUnconfirmed; n != 1 {
			t.Fatalf("%s : rejet tardif compté %d fois", name, n)
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
	if out[a].LateCloseUnconfirmed != 3 {
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

// Une réponse gardée en mémoire dont la dernière lecture a échoué n'est pas
// publiée comme réussie pendant un dépassement de délai.
func TestTimedOutHeldOnlyIfLastSucceeded(t *testing.T) {
	const addr = "0000:01:00.0"
	p := plan(aacHost("host0", addr, "S1"))
	tr := NewTracker()
	t0 := time.Unix(1_000_000, 0)
	good := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"capabilities": true}, Cached: []string{"capabilities"}}}
	tr.Update(good, []string{addr}, t0)
	tr.Remember(Build(p, true, good, tr, time.Minute))
	bad := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"capabilities": false}, Cached: []string{"capabilities"}}}
	tr.Update(bad, []string{addr}, t0.Add(time.Minute))
	tr.Remember(Build(p, true, bad, tr, time.Minute))
	res := map[string]module.Result{addr: {TimedOut: true, Collected: map[string]bool{"capabilities": false}}}
	out := Render(Build(p, true, res, tr, time.Minute))
	if want := `hwraid_collection_success{controller="serial:S1",collector="capabilities"} 0`; !strings.Contains(out, want+"\n") {
		t.Fatalf("ligne absente : %s\n%s", want, out)
	}
}

// Une autre carte à la même adresse PCI (nouveau numéro de série) n'hérite
// pas du succès gardé en mémoire de la précédente.
func TestTimedOutHeldNotInheritedByNewCard(t *testing.T) {
	const addr = "0000:01:00.0"
	tr := NewTracker()
	t0 := time.Unix(1_000_000, 0)
	ok := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"capabilities": true}, Cached: []string{"capabilities"}}}
	tr.Update(ok, []string{addr}, t0)
	tr.Remember(Build(plan(aacHost("host0", addr, "S1")), true, ok, tr, time.Minute))
	res := map[string]module.Result{addr: {TimedOut: true, Collected: map[string]bool{"capabilities": false}}}
	tr.Update(res, []string{addr}, t0.Add(time.Minute))
	out := Render(Build(plan(aacHost("host0", addr, "S2")), true, res, tr, time.Minute))
	if want := `hwraid_collection_success{controller="serial:S2",collector="capabilities"} 0`; !strings.Contains(out, want+"\n") {
		t.Fatalf("ligne absente : %s\n%s", want, out)
	}
}

// À l'arrêt, une collecte qui finit pendant l'attente rend ses erreurs
// (par exemple une clôture en échec) : elles ne disparaissent pas du journal.
func TestRunnerLateErrorsAtStop(t *testing.T) {
	const a = "0000:01:00.0"
	release := make(chan struct{})
	closeErr := errors.New("read 234: stop: EIO")
	r := NewRunner()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx, map[string]func() module.Result{a: func() module.Result {
			<-release
			return module.Result{CollectErr: closeErr}
		}}, nil)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel() // arrêt : Run rend la main sans attendre la collecte
	<-done
	close(release)
	if !r.Drain(2 * time.Second) {
		t.Fatal("collecte non terminée")
	}
	if errs := r.LateErrors(); !errors.Is(errs[a], closeErr) {
		t.Fatalf("erreur de fin de collecte perdue : %v", errs)
	}
	if errs := r.LateErrors(); len(errs) != 0 {
		t.Fatalf("erreurs rendues deux fois : %v", errs)
	}
}

// Changement d'identité à la même adresse PCI, y compris pci: puis
// serial: : rien ne prouve qu'il s'agit de la même carte, le succès gardé en
// mémoire n'est pas transmis. Un cycle sans collecte effective (nœud non
// vérifié) n'établit pas non plus de nouvelle identité.
func TestTimedOutHeldIdentityChange(t *testing.T) {
	const addr = "0000:01:00.0"
	tr := NewTracker()
	t0 := time.Unix(1_000_000, 0)
	ok := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"capabilities": true}, Cached: []string{"capabilities"}}}
	tr.Update(ok, []string{addr}, t0)
	tr.Remember(Build(plan(aacHost("host0", addr, "")), true, ok, tr, time.Minute))
	timedOut := map[string]module.Result{addr: {TimedOut: true, Collected: map[string]bool{"capabilities": false}}}
	tr.Update(timedOut, []string{addr}, t0.Add(time.Minute)) // comme serve : Update avant Build
	out := Render(Build(plan(aacHost("host0", addr, "S1")), true, timedOut, tr, time.Minute))
	if want := `hwraid_collection_success{controller="serial:S1",collector="capabilities"} 0`; !strings.Contains(out, want+"\n") {
		t.Fatalf("succès transmis à une identité nouvelle : %s\n%s", want, out)
	}
	// Succès établi sous S1 ; un cycle, même sans collecte effective, sous
	// S2 montre une autre carte à cette adresse : l'historique est oublié,
	// et le retour de S1 ne le réactive pas.
	tr2 := NewTracker()
	tr2.Update(ok, []string{addr}, t0)
	tr2.Remember(Build(plan(aacHost("host0", addr, "S1")), true, ok, tr2, time.Minute))
	noNode := map[string]module.Result{addr: {NodeValid: false}}
	tr2.Update(noNode, []string{addr}, t0.Add(time.Minute))
	tr2.Remember(Build(plan(aacHost("host0", addr, "S2")), true, noNode, tr2, time.Minute))
	tr2.Update(timedOut, []string{addr}, t0.Add(2*time.Minute))
	out = Render(Build(plan(aacHost("host0", addr, "S1")), true, timedOut, tr2, time.Minute))
	if want := `hwraid_collection_success{controller="serial:S1",collector="capabilities"} 0`; !strings.Contains(out, want+"\n") {
		t.Fatalf("historique réactivé après un passage par une autre carte : %s\n%s", want, out)
	}
}

// Numéro de série momentanément illisible : le module change la clé de ses
// réponses gardées et relit la 706. Si cette relecture se bloque, au cycle
// illisible comme au retour du numéro, le succès gardé de l'ancienne clé
// n'est pas publié. Les dates, elles, ne sont pas oubliées : rien ne prouve
// un changement de carte.
func TestTimedOutHeldAfterUnreadableSerial(t *testing.T) {
	const addr = "0000:01:00.0"
	t0 := time.Unix(1_000_000, 0)
	ok := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"capabilities": true, "volumes": true}, Cached: []string{"capabilities"}}}
	timedOut := map[string]module.Result{addr: {TimedOut: true, Collected: map[string]bool{"capabilities": false, "volumes": false}}}
	capFail := `hwraid_collection_success{controller="serial:S1",collector="capabilities"} 0`
	for _, tc := range []struct {
		name   string
		middle map[string]module.Result // cycle au numéro illisible
	}{
		{"relecture bloquée au cycle illisible", nil},
		{"relecture bloquée au retour du numéro", ok},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewTracker()
			tr.Update(ok, []string{addr}, t0)
			tr.Remember(Build(plan(aacHost("host0", addr, "S1")), true, ok, tr, time.Minute))
			at := t0.Add(time.Minute)
			if tc.middle != nil {
				tr.Update(tc.middle, []string{addr}, at)
				tr.Remember(Build(plan(aacHost("host0", addr, "")), true, tc.middle, tr, time.Minute))
				at = at.Add(time.Minute)
			}
			// Le label serial:S1 est conservé pendant le cycle illisible.
			serial, want := "S1", capFail
			if tc.middle == nil {
				serial = ""
			}
			// Deux cycles hors délai : la relecture 706 reste bloquée, le
			// succès de l'ancienne clé n'est publié à aucun des deux.
			for i := 0; i < 2; i++ {
				tr.Update(timedOut, []string{addr}, at)
				s := Build(plan(aacHost("host0", addr, serial)), true, timedOut, tr, time.Minute)
				out := Render(s)
				if !strings.Contains(out, want+"\n") {
					t.Fatalf("cycle hors délai %d : succès 706 d'une autre clé publié : %s\n%s", i, want, out)
				}
				if s.Controllers[0].LastSuccess["volumes"].IsZero() {
					t.Fatal("date du dernier succès oubliée sans preuve d'un changement de carte")
				}
				tr.Remember(s)
				at = at.Add(time.Minute)
			}
			// Une collecte effective sous S1 relit la 706 : la réponse
			// gardée sert de nouveau au cycle hors délai suivant.
			tr.Update(ok, []string{addr}, at)
			tr.Remember(Build(plan(aacHost("host0", addr, "S1")), true, ok, tr, time.Minute))
			tr.Update(timedOut, []string{addr}, at.Add(time.Minute))
			if out := Render(Build(plan(aacHost("host0", addr, "S1")), true, timedOut, tr, time.Minute)); !strings.Contains(out, strings.Replace(capFail, "} 0", "} 1", 1)+"\n") {
				t.Fatalf("réponse gardée non servie après une collecte effective\n%s", out)
			}
		})
	}
	// Témoin : sans passage par un numéro illisible, la réponse gardée sert.
	tr := NewTracker()
	tr.Update(ok, []string{addr}, t0)
	tr.Remember(Build(plan(aacHost("host0", addr, "S1")), true, ok, tr, time.Minute))
	tr.Update(timedOut, []string{addr}, t0.Add(time.Minute))
	if out := Render(Build(plan(aacHost("host0", addr, "S1")), true, timedOut, tr, time.Minute)); !strings.Contains(out, strings.Replace(capFail, "} 0", "} 1", 1)+"\n") {
		t.Fatalf("témoin : réponse gardée non servie\n%s", out)
	}
}

// Arrêt alors que la collecte finit au même moment : quel que soit l'ordre
// dans lequel Run voit l'annulation et le résultat, l'erreur n'est pas
// rendue dans le cycle (qui ne sera pas publié) mais reste dans
// l'ordonnanceur, et LateErrors la rend. Répété pour rencontrer les deux
// ordres.
func TestRunnerLateErrorsBeforeReturn(t *testing.T) {
	const a = "0000:01:00.0"
	closeErr := errors.New("read 234: stop: EIO")
	for i := 0; i < 50; i++ {
		r := NewRunner()
		ctx, cancel := context.WithCancel(context.Background())
		out := r.Run(ctx, map[string]func() module.Result{a: func() module.Result {
			cancel() // arrêt reçu pendant la clôture
			return module.Result{CollectErr: closeErr}
		}}, nil)
		if out[a].LateErr != nil {
			t.Fatalf("essai %d : erreur rendue dans un cycle qui ne sera pas publié : %v", i, out[a].LateErr)
		}
		if !r.Drain(2 * time.Second) {
			t.Fatal("collecte non terminée")
		}
		if errs := r.LateErrors(); !errors.Is(errs[a], closeErr) {
			t.Fatalf("essai %d : erreur perdue : %v", i, errs)
		}
	}
}

// Même carte (même numéro de série), firmware mis à jour, première
// collecte hors délai : le module doit relire ses capacités, leur succès
// n'est pas reporté.
func TestTimedOutHeldNotAfterFirmwareUpdate(t *testing.T) {
	const addr = "0000:01:00.0"
	tr := NewTracker()
	t0 := time.Unix(1_000_000, 0)
	ok := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"capabilities": true}, Cached: []string{"capabilities"}}}
	tr.Update(ok, []string{addr}, t0)
	tr.Remember(Build(plan(aacHost("host0", addr, "S1")), true, ok, tr, time.Minute))
	h := aacHost("host0", addr, "S1")
	h.Firmware = "5.3-0[19205]"
	timedOut := map[string]module.Result{addr: {TimedOut: true, Collected: map[string]bool{"capabilities": false}}}
	tr.Update(timedOut, []string{addr}, t0.Add(time.Minute))
	out := Render(Build(plan(h), true, timedOut, tr, time.Minute))
	if want := `hwraid_collection_success{controller="serial:S1",collector="capabilities"} 0`; !strings.Contains(out, want+"\n") {
		t.Fatalf("succès reporté malgré le nouveau firmware : %s\n%s", want, out)
	}
}

// Collecteur non pris en charge, puis collecte hors délai sur la même
// carte : il reste non pris en charge, sans succès ni échec publié.
func TestTimedOutKeepsUnsupported(t *testing.T) {
	const addr = "0000:01:00.0"
	p := plan(aacHost("host0", addr, "S1"))
	tr := NewTracker()
	t0 := time.Unix(1_000_000, 0)
	ok := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"volumes": true}, Unsupported: []string{"cache_protection"}}}
	tr.Update(ok, []string{addr}, t0)
	tr.Remember(Build(p, true, ok, tr, time.Minute))
	timedOut := map[string]module.Result{addr: {TimedOut: true, Collected: map[string]bool{"volumes": false, "cache_protection": false}}}
	out := Render(Build(p, true, timedOut, tr, time.Minute))
	if want := `hwraid_collector_supported{controller="serial:S1",collector="cache_protection"} 0`; !strings.Contains(out, want+"\n") {
		t.Fatalf("non-prise en charge perdue : %s\n%s", want, out)
	}
	if strings.Contains(out, `hwraid_collection_success{controller="serial:S1",collector="cache_protection"}`) {
		t.Fatalf("collecteur non pris en charge publié en échec :\n%s", out)
	}
	if want := `hwraid_collection_success{controller="serial:S1",collector="volumes"} 0`; !strings.Contains(out, want+"\n") {
		t.Fatalf("ligne absente : %s\n%s", want, out)
	}
}

// Carte remplacée à la même adresse (S1 puis S2), premier cycle de S2 en
// échec : ni les dates de succès ni le compteur 0xc5 de S1 ne sont publiés
// pour S2.
func TestNoHistoryInheritedByReplacementCard(t *testing.T) {
	const addr = "0000:01:00.0"
	tr := NewTracker()
	t0 := time.Unix(1_000_000, 0)
	ok := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"volumes": true}, CloseUnconfirmed: 2}}
	tr.Update(ok, []string{addr}, t0)
	tr.Remember(Build(plan(aacHost("host0", addr, "S1")), true, ok, tr, time.Minute))
	failed := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"volumes": false}}}
	tr.Update(failed, []string{addr}, t0.Add(time.Minute))
	out := Render(Build(plan(aacHost("host0", addr, "S2")), true, failed, tr, time.Minute))
	if strings.Contains(out, `hwraid_collection_last_success_timestamp_seconds{controller="serial:S2"`) {
		t.Fatalf("date de succès de S1 publiée pour S2 :\n%s", out)
	}
	if want := `hwraid_transfer_close_unconfirmed_total{controller="serial:S2"} 0`; !strings.Contains(out, want+"\n") {
		t.Fatalf("compteur de S1 repris pour S2 : %s\n%s", want, out)
	}
	// Cycles suivants de S2 : l'historique de S1 a été oublié, il ne
	// réapparaît pas une fois l'identité S2 établie.
	s2 := plan(aacHost("host0", addr, "S2"))
	tr.Remember(Build(s2, true, failed, tr, time.Minute))
	tr.Update(failed, []string{addr}, t0.Add(2*time.Minute))
	out = Render(Build(s2, true, failed, tr, time.Minute))
	if strings.Contains(out, `hwraid_collection_last_success_timestamp_seconds{controller="serial:S2"`) {
		t.Fatalf("date de succès de S1 réapparue pour S2 :\n%s", out)
	}
	if want := `hwraid_transfer_close_unconfirmed_total{controller="serial:S2"} 0`; !strings.Contains(out, want+"\n") {
		t.Fatalf("compteur de S1 réapparu pour S2 : %s\n%s", want, out)
	}
}

// Nouvelle carte à la même adresse dont le premier cycle réussit : sa date
// de succès et ses rejets 0xc5 sont publiés et conservés ; un échec au cycle
// suivant ne la fait pas passer pour jamais réussie.
func TestReplacementCardFirstCycleKept(t *testing.T) {
	const addr = "0000:01:00.0"
	tr := NewTracker()
	t0 := time.Unix(1_000_000, 0)
	s1 := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"volumes": true}, CloseUnconfirmed: 5}}
	tr.Update(s1, []string{addr}, t0)
	tr.Remember(Build(plan(aacHost("host0", addr, "S1")), true, s1, tr, time.Minute))
	p2 := plan(aacHost("host0", addr, "S2"))
	s2 := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"volumes": true}, CloseUnconfirmed: 1}}
	tr.Update(s2, []string{addr}, t0.Add(time.Minute))
	first := Build(p2, true, s2, tr, time.Minute)
	tr.Remember(first)
	out := Render(first)
	for _, want := range []string{
		`hwraid_collection_last_success_timestamp_seconds{controller="serial:S2",collector="volumes"} 1000060`,
		`hwraid_transfer_close_unconfirmed_total{controller="serial:S2"} 1`,
	} {
		if !strings.Contains(out, want+"\n") {
			t.Fatalf("premier cycle de S2 : %s absent\n%s", want, out)
		}
	}
	failed := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"volumes": false}}}
	tr.Update(failed, []string{addr}, t0.Add(2*time.Minute))
	out = Render(Build(p2, true, failed, tr, time.Minute))
	for _, want := range []string{
		`hwraid_collection_last_success_timestamp_seconds{controller="serial:S2",collector="volumes"} 1000060`,
		`hwraid_transfer_close_unconfirmed_total{controller="serial:S2"} 1`,
	} {
		if !strings.Contains(out, want+"\n") {
			t.Fatalf("cycle suivant de S2 : %s absent\n%s", want, out)
		}
	}
}

// Même carte à la même adresse, mais nœud de gestion différent (autre
// minor, donc autre numéro de carte pour le pilote) : l'historique n'est
// pas repris.
func TestNoHistoryAcrossNodeChange(t *testing.T) {
	const addr = "0000:01:00.0"
	withNode := func(minor uint32) module.Plan {
		p := plan(aacHost("host0", addr, "S1"))
		p.Cards[0].Requests = []module.NodeRequest{{Name: "aac" + strconv.FormatUint(uint64(minor), 10), MajorOf: "aac", Minor: minor}}
		return p
	}
	tr := NewTracker()
	t0 := time.Unix(1_000_000, 0)
	ok := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"capabilities": true}, Cached: []string{"capabilities"}}}
	tr.Update(ok, []string{addr}, t0)
	tr.Remember(Build(withNode(0), true, ok, tr, time.Minute))
	timedOut := map[string]module.Result{addr: {TimedOut: true, Collected: map[string]bool{"capabilities": false}}}
	tr.Update(timedOut, []string{addr}, t0.Add(time.Minute))
	out := Render(Build(withNode(1), true, timedOut, tr, time.Minute))
	if want := `hwraid_collection_success{controller="serial:S1",collector="capabilities"} 0`; !strings.Contains(out, want+"\n") {
		t.Fatalf("succès repris malgré un autre nœud : %s\n%s", want, out)
	}
}

// Nœud momentanément illisible (inventaire incomplet, pas de Requests) :
// rien ne prouve un changement de carte, l'historique n'est pas effacé. Au
// retour du même nœud, un échec de collecte publie toujours la date du
// dernier succès.
func TestNoForgetOnUnreadableNode(t *testing.T) {
	const addr = "0000:01:00.0"
	tr := NewTracker()
	t0 := time.Unix(1_000_000, 0)
	ok := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"capabilities": true, "volumes": true}, Cached: []string{"capabilities"}}}
	tr.Update(ok, []string{addr}, t0)
	tr.Remember(Build(plan(aacHost("host0", addr, "S1")), true, ok, tr, time.Minute))
	noNode := plan(aacHost("host0", addr, "S1"))
	noNode.Cards[0].Requests = nil
	failed := map[string]module.Result{addr: {NodeValid: false}}
	tr.Update(failed, []string{addr}, t0.Add(time.Minute))
	tr.Remember(Build(noNode, true, failed, tr, time.Minute))
	tr.Update(failed, []string{addr}, t0.Add(2*time.Minute))
	s := Build(plan(aacHost("host0", addr, "S1")), true, failed, tr, time.Minute)
	if got := s.Controllers[0].LastSuccess["volumes"]; !got.Equal(t0) {
		t.Fatalf("date du dernier succès perdue après un nœud illisible : %v", got)
	}
}

// Rejet 0xc5 tardif de l'ancienne carte récolté au cycle où une autre carte
// est observée à la même adresse : il n'est ni publié ni conservé pour la
// nouvelle carte, qui garde ses propres rejets du cycle.
func TestLateCloseNotLeftToReplacementCard(t *testing.T) {
	const addr = "0000:01:00.0"
	tr := NewTracker()
	t0 := time.Unix(1_000_000, 0)
	ok := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"capabilities": true}, Cached: []string{"capabilities"}}}
	tr.Update(ok, []string{addr}, t0)
	tr.Remember(Build(plan(aacHost("host0", addr, "S1")), true, ok, tr, time.Minute))
	mixed := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"capabilities": true}, Cached: []string{"capabilities"},
		CloseUnconfirmed: 1, LateCloseUnconfirmed: 2}}
	tr.Update(mixed, []string{addr}, t0.Add(time.Minute))
	s := Build(plan(aacHost("host0", addr, "S2")), true, mixed, tr, time.Minute)
	if got := s.Controllers[0].CloseUnconfirmed; got != 1 {
		t.Fatalf("rejets publiés pour la nouvelle carte : %d, attendu 1 (les siens)", got)
	}
	tr.Remember(s)
	tr.Update(ok, []string{addr}, t0.Add(2*time.Minute))
	if got := Build(plan(aacHost("host0", addr, "S2")), true, ok, tr, time.Minute).Controllers[0].CloseUnconfirmed; got != 1 {
		t.Fatalf("rejets conservés pour la nouvelle carte : %d, attendu 1", got)
	}
}

// Contexte déjà terminé au lancement : aucune collecte n'est démarrée, donc
// aucune commande ne part après l'arrêt ; rien ne reste à attendre.
func TestRunnerNoJobAfterStop(t *testing.T) {
	const a = "0000:01:00.0"
	r := NewRunner()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := false
	r.Run(ctx, map[string]func() module.Result{a: func() module.Result {
		started = true
		return module.Result{}
	}}, nil)
	if !r.Drain(time.Second) {
		t.Fatal("collecte en cours après un lancement sous contexte terminé")
	}
	if started {
		t.Fatal("collecte démarrée après l'arrêt")
	}
}

// Numéro de série illisible et firmware changé au même cycle : le champ lu
// (firmware) établit le changement, l'historique est oublié. Au retour de
// la carte initiale (autre firmware encore), les apports du cycle
// intermédiaire ne lui sont pas attribués.
func TestFirmwareChangeWithUnreadableSerial(t *testing.T) {
	const addr = "0000:01:00.0"
	withFw := func(serial, fw string) module.Plan {
		h := aacHost("host0", addr, serial)
		h.Firmware = fw
		return plan(h)
	}
	tr := NewTracker()
	t0 := time.Unix(1_000_000, 0)
	ok := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"capabilities": true, "volumes": true}, Cached: []string{"capabilities"}, CloseUnconfirmed: 1}}
	failed := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"capabilities": true, "volumes": false}, Cached: []string{"capabilities"}}}
	tr.Update(ok, []string{addr}, t0)
	tr.Remember(Build(withFw("S1", "5.3-0[19204]"), true, ok, tr, time.Minute))
	tr.Update(ok, []string{addr}, t0.Add(time.Minute))
	s := Build(withFw("", "5.3-0[19205]"), true, ok, tr, time.Minute)
	if got := s.Controllers[0].LastSuccess["volumes"]; !got.Equal(t0.Add(time.Minute)) || s.Controllers[0].CloseUnconfirmed != 1 {
		t.Fatalf("historique de l'ancien firmware repris : dernier succès %v, rejets %d", got, s.Controllers[0].CloseUnconfirmed)
	}
	tr.Remember(s)
	// Second cycle sous le firmware intermédiaire, toujours sans numéro :
	// seuls ses apports et ceux du cycle précédent comptent.
	tr.Update(failed, []string{addr}, t0.Add(2*time.Minute))
	s = Build(withFw("", "5.3-0[19205]"), true, failed, tr, time.Minute)
	if got := s.Controllers[0].LastSuccess["volumes"]; !got.Equal(t0.Add(time.Minute)) || s.Controllers[0].CloseUnconfirmed != 1 {
		t.Fatalf("historique de l'ancien firmware réapparu : dernier succès %v, rejets %d", got, s.Controllers[0].CloseUnconfirmed)
	}
	tr.Remember(s)
	tr.Update(failed, []string{addr}, t0.Add(3*time.Minute))
	s = Build(withFw("S1", "5.3-0[19204]"), true, failed, tr, time.Minute)
	if got := s.Controllers[0].LastSuccess["volumes"]; !got.IsZero() || s.Controllers[0].CloseUnconfirmed != 0 {
		t.Fatalf("apports du firmware intermédiaire attribués à la carte revenue : dernier succès %v, rejets %d", got, s.Controllers[0].CloseUnconfirmed)
	}
}

// Numéro illisible un cycle, puis un autre numéro : le champ lu avant le
// trou (S1) reste dans l'identité gardée, et S2 établit le remplacement.
func TestReplacementAcrossUnreadableSerial(t *testing.T) {
	const addr = "0000:01:00.0"
	tr := NewTracker()
	t0 := time.Unix(1_000_000, 0)
	ok := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"capabilities": true, "volumes": true}, Cached: []string{"capabilities"}}}
	failed := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"capabilities": true, "volumes": false}, Cached: []string{"capabilities"}}}
	tr.Update(ok, []string{addr}, t0)
	tr.Remember(Build(plan(aacHost("host0", addr, "S1")), true, ok, tr, time.Minute))
	tr.Update(failed, []string{addr}, t0.Add(time.Minute))
	s := Build(plan(aacHost("host0", addr, "")), true, failed, tr, time.Minute)
	if s.Controllers[0].LastSuccess["volumes"].IsZero() {
		t.Fatal("historique oublié sur un simple numéro illisible")
	}
	tr.Remember(s)
	tr.Update(failed, []string{addr}, t0.Add(2*time.Minute))
	if got := Build(plan(aacHost("host0", addr, "S2")), true, failed, tr, time.Minute).Controllers[0].LastSuccess["volumes"]; !got.IsZero() {
		t.Fatalf("historique de S1 transmis à S2 après un numéro illisible : %v", got)
	}
}

// Collecte lancée dans le cycle, finie après l'échéance mais récoltée par le
// même appel : son rejet 0xc5 appartient à la carte de ce cycle. Il est
// compté dans le résultat hors délai (CloseUnconfirmed), jamais parmi les
// rejets tardifs d'un cycle antérieur (LateCloseUnconfirmed), que le suivi
// écarte au changement de carte.
func TestRunnerOwnLateCloseStaysWithCycle(t *testing.T) {
	const addr = "0000:01:00.0"
	cols := map[string][]string{addr: {"volumes"}}
	// La collecte rend son résultat avant la récolte.
	afterDeadline = func(delivered func() bool) {
		for !delivered() {
			time.Sleep(time.Millisecond)
		}
	}
	t.Cleanup(func() { afterDeadline = nil })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	r := NewRunner()
	res := r.Run(ctx, map[string]func() module.Result{addr: func() module.Result {
		<-ctx.Done()
		return module.Result{NodeValid: true, CloseUnconfirmed: 1}
	}}, cols)[addr]
	if !res.TimedOut || res.CloseUnconfirmed != 1 || res.LateCloseUnconfirmed != 0 {
		t.Fatalf("rejet du cycle mal attribué : %+v", res)
	}
	if next := r.Run(within(t, time.Second), map[string]func() module.Result{addr: func() module.Result { return module.Result{} }}, cols)[addr]; next.LateCloseUnconfirmed != 0 {
		t.Fatalf("rejet compté deux fois : %+v", next)
	}
}

// Firmware momentanément illisible (numéro de série et nœud lus) : le
// module a relu la 706 sous une autre clé. Au retour du firmware, si la
// relecture se bloque, l'ancien succès n'est pas publié, et les collecteurs
// déclarés non pris en charge au cycle illisible (carte crue non qualifiée)
// ne sont pas reconduits.
func TestTimedOutHeldAfterUnreadableFirmware(t *testing.T) {
	const addr = "0000:01:00.0"
	withFw := func(fw string) module.Plan {
		h := aacHost("host0", addr, "S1")
		h.Firmware = fw
		return plan(h)
	}
	t0 := time.Unix(1_000_000, 0)
	ok := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"capabilities": true, "volumes": true}, Cached: []string{"capabilities"}}}
	unq := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"capabilities": true}, Cached: []string{"capabilities"}, Unsupported: []string{"volumes"}}}
	timedOut := map[string]module.Result{addr: {TimedOut: true, Collected: map[string]bool{"capabilities": false, "volumes": false}}}
	tr := NewTracker()
	tr.Update(ok, []string{addr}, t0)
	tr.Remember(Build(withFw("5.3-0[19204]"), true, ok, tr, time.Minute))
	tr.Update(unq, []string{addr}, t0.Add(time.Minute))
	tr.Remember(Build(withFw(""), true, unq, tr, time.Minute))
	tr.Update(timedOut, []string{addr}, t0.Add(2*time.Minute))
	out := Render(Build(withFw("5.3-0[19204]"), true, timedOut, tr, time.Minute))
	for _, want := range []string{
		`hwraid_collection_success{controller="serial:S1",collector="capabilities"} 0`,
		`hwraid_collection_success{controller="serial:S1",collector="volumes"} 0`,
	} {
		if !strings.Contains(out, want+"\n") {
			t.Fatalf("réponse ou déclaration d'une autre clé reprise : %s\n%s", want, out)
		}
	}
}

// Nœud en erreur (GET_HBA_INFO en échec) après une collecte réussie : les
// dates de dernier succès restent publiées, hors collecteurs connus comme
// non pris en charge, pour que la perte de visibilité reste continue même
// si ces cycles alternent avec des cycles hors délai.
func TestNodeErrorKeepsLastSuccess(t *testing.T) {
	const addr = "0000:01:00.0"
	t0 := time.Unix(1_000_000, 0)
	tr := NewTracker()
	ok := map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"capabilities": true, "volumes": true, "disks": true}, Cached: []string{"capabilities"}, Unsupported: []string{"cache_protection"}}}
	tr.Update(map[string]module.Result{addr: {NodeValid: true, Collected: map[string]bool{"cache_protection": true}}}, []string{addr}, t0.Add(-time.Minute))
	tr.Update(ok, []string{addr}, t0)
	tr.Remember(Build(plan(aacHost("host0", addr, "S1")), true, ok, tr, time.Minute))
	nodeErr := map[string]module.Result{addr: {Err: errors.New("GET_HBA_INFO: EIO")}}
	tr.Update(nodeErr, []string{addr}, t0.Add(time.Minute))
	out := Render(Build(plan(aacHost("host0", addr, "S1")), true, nodeErr, tr, time.Minute))
	for _, want := range []string{
		`hwraid_management_node_valid{controller="serial:S1"} 0`,
		`hwraid_collection_last_success_timestamp_seconds{controller="serial:S1",collector="volumes"} 1000000`,
		`hwraid_collection_last_success_timestamp_seconds{controller="serial:S1",collector="disks"} 1000000`,
	} {
		if !strings.Contains(out, want+"\n") {
			t.Fatalf("ligne absente : %s\n%s", want, out)
		}
	}
	if strings.Contains(out, `last_success_timestamp_seconds{controller="serial:S1",collector="cache_protection"}`) ||
		strings.Contains(out, `hwraid_collection_success{controller="serial:S1",collector="cache_protection"}`) {
		t.Fatalf("date ou échec publié pour un collecteur non pris en charge\n%s", out)
	}
	if want := `hwraid_collector_supported{controller="serial:S1",collector="cache_protection"} 0`; !strings.Contains(out, want+"\n") {
		t.Fatalf("non-prise en charge perdue en erreur de nœud : %s\n%s", want, out)
	}
}

// Avant tout premier succès, des cycles qui alternent erreur de nœud et
// dépassement de délai publient chacun hwraid_collection_success 0 pour
// chaque collecteur : la série reste continue et la règle « jamais réussi »
// peut atteindre son délai.
func TestNodeErrorAndTimeoutAlternateBeforeFirstSuccess(t *testing.T) {
	const addr = "0000:01:00.0"
	t0 := time.Unix(1_000_000, 0)
	tr := NewTracker()
	nodeErr := map[string]module.Result{addr: {Err: errors.New("GET_HBA_INFO: EIO")}}
	timedOut := map[string]module.Result{addr: {TimedOut: true, Collected: map[string]bool{"volumes": false}}}
	want := `hwraid_collection_success{controller="serial:S1",collector="volumes"} 0`
	for i := 0; i < 4; i++ {
		res := nodeErr
		if i%2 == 1 {
			res = timedOut
		}
		tr.Update(res, []string{addr}, t0.Add(time.Duration(i)*time.Minute))
		s := Build(plan(aacHost("host0", addr, "S1")), true, res, tr, time.Minute)
		if out := Render(s); !strings.Contains(out, want+"\n") {
			t.Fatalf("cycle %d : série absente : %s\n%s", i, want, out)
		}
		tr.Remember(s)
	}
}
