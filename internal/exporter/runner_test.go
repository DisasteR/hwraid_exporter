package exporter

import (
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
				return module.Result{NodeValid: true, Collected: map[string]bool{"volumes": true}, Volumes: []module.Volume{{ID: "périmé"}}}
			}
			return module.Result{NodeValid: true, Collected: map[string]bool{"volumes": true}}
		},
	}
	cols := map[string][]string{fast: {"volumes"}, slow: {"volumes", "disks"}}
	r := NewRunner()

	start := time.Now()
	out := r.Run(jobs, cols, 50*time.Millisecond)
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
	out = r.Run(jobs, cols, 10*time.Millisecond)
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
	out = r.Run(jobs, cols, time.Second)
	if s := out[slow]; s.TimedOut || len(s.Volumes) != 0 || slowCalls.Load() != 2 {
		t.Fatalf("résultat tardif repris ou pas de relance (%d appels) : %+v", slowCalls.Load(), s)
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
