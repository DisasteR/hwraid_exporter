// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/DisasteR/hwraid_exporter/internal/exporter"
)

// serveRunning : seul un autre processus dont le programme s'appelle
// hwraid-exporter et dont le premier argument est serve est trouvé, quel
// que soit son chemin ; le processus courant, oneshot, discover, un autre
// programme qui passe « serve » ailleurs, une entrée non numérique ou un
// cmdline illisible ne le sont pas ; /proc illisible est une erreur.
func TestServeRunning(t *testing.T) {
	proc := t.TempDir()
	add := func(pid string, argv ...string) {
		dir := filepath.Join(proc, pid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		var b []byte
		for _, a := range argv {
			b = append(append(b, a...), 0)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	add("1", "/sbin/init")
	add("10", "/usr/local/bin/hwraid-exporter", "oneshot")
	add("11", "hwraid-exporter", "discover")
	add("12", "/usr/bin/python3", "serve")
	add("13", "/opt/hwraid-exporter-old", "serve")
	add("14", "/hwraid-exporter", "-v", "serve")
	add("self", "/hwraid-exporter", "serve")
	if err := os.MkdirAll(filepath.Join(proc, "15"), 0o755); err != nil { // sans cmdline
		t.Fatal(err)
	}
	add("20", "/usr/local/bin/hwraid-exporter", "oneshot")
	if pid, err := serveRunning(proc, 20); err != nil || pid != 0 {
		t.Fatalf("aucun serve : %d, %v", pid, err)
	}
	add("30", "/hwraid-exporter", "serve", "-listen", "0.0.0.0:9799")
	if pid, err := serveRunning(proc, 20); err != nil || pid != 30 {
		t.Fatalf("serve en conteneur : %d, %v", pid, err)
	}
	if pid, err := serveRunning(proc, 30); err != nil || pid != 0 {
		t.Fatalf("processus courant compté : %d, %v", pid, err)
	}
	if _, err := serveRunning(filepath.Join(proc, "absent"), 1); err == nil {
		t.Fatal("/proc illisible accepté")
	}
}

// oneshotOutcome : 0 seulement si la découverte est complète et si chaque
// carte d'un module a un nœud valide, une collecte dans le délai et toutes
// ses lectures réussies ; une carte sans module ou aux collecteurs non pris
// en charge n'est pas un échec.
func TestOneshotOutcome(t *testing.T) {
	ok := func() exporter.Controller {
		return exporter.Controller{Backend: "aacraid", NodeChecked: true, NodeValid: true, Collected: map[string]bool{"volumes": true, "disks": true}}
	}
	snap := func(discovery bool, cs ...exporter.Controller) exporter.Snapshot {
		return exporter.Snapshot{Ready: true, DiscoverySuccess: discovery, Controllers: cs}
	}
	unsupported := exporter.Controller{Backend: "aacraid", NodeChecked: true, NodeValid: true, Unsupported: []string{"volumes"}}
	noModule := exporter.Controller{}
	for name, tc := range map[string]struct {
		s    exporter.Snapshot
		want int
	}{
		"tout réussi":                {snap(true, ok(), noModule, unsupported), 0},
		"aucune carte":               {snap(true), 0},
		"découverte partielle":       {snap(false, ok()), 1},
		"nœud invalide":              {snap(true, func() exporter.Controller { c := ok(); c.NodeValid = false; return c }()), 1},
		"nœud non vérifié":           {snap(true, exporter.Controller{Backend: "aacraid"}), 1},
		"hors délai":                 {snap(true, func() exporter.Controller { c := ok(); c.TimedOut = true; return c }()), 1},
		"une lecture en échec":       {snap(true, func() exporter.Controller { c := ok(); c.Collected["disks"] = false; return c }()), 1},
		"échec sur la seconde carte": {snap(true, ok(), func() exporter.Controller { c := ok(); c.Collected["volumes"] = false; return c }()), 1},
	} {
		if got := oneshotOutcome(tc.s); got != tc.want {
			t.Errorf("%s : %d, attendu %d", name, got, tc.want)
		}
	}
}

// Options invalides : code 2, avant toute préparation (ni root exigé, ni
// recherche d'un serve), rien sur la sortie.
func TestOneshotRejectsInvalidOptions(t *testing.T) {
	for _, args := range [][]string{{"inattendu"}, {"-collect-timeout", "0"}, {"-collect-timeout", "-1s"}, {"-uid", "0"}, {"-gid", "0"}, {"-inconnue"}, {"-format", "json"}, {"-format", ""}} {
		var out bytes.Buffer
		if code := oneshot(args, &out); code != 2 || out.Len() != 0 {
			t.Fatalf("%v : code %d, sortie %q", args, code, out.String())
		}
	}
}

// oneshotFinish : rien n'est écrit si la collecte a été interrompue (signal
// pendant le cycle ou pendant l'attente des commandes en cours) ou si les
// nœuds attendus ont changé (code 1) ; sinon, le rendu choisi est écrit et
// le code suit oneshotOutcome.
func TestOneshotFinish(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	render := func(exporter.Snapshot) string { return "RENDU\n" }
	good := exporter.Snapshot{Ready: true, DiscoverySuccess: true}
	bad := exporter.Snapshot{Ready: true}
	for name, tc := range map[string]struct {
		s                      exporter.Snapshot
		complete, nodesChanged bool
		code                   int
		written                bool
	}{
		"réussie":              {good, true, false, 0, true},
		"découverte partielle": {bad, true, false, 1, true},
		"interrompue":          {good, false, false, 1, false},
		"nœuds changés":        {good, true, true, 1, false},
	} {
		var out bytes.Buffer
		if code := oneshotFinish(log, &out, render, tc.s, tc.complete, tc.nodesChanged); code != tc.code || (out.Len() > 0) != tc.written {
			t.Errorf("%s : code %d, sortie %q", name, code, out.String())
		}
	}
}

// Nœuds de oneshot distincts de ceux de serve, que sa préparation ne doit
// jamais synchroniser.
func TestOneshotNodesDir(t *testing.T) {
	if oneshotNodesDir == nodesDir || filepath.Dir(oneshotNodesDir) != "/dev" {
		t.Fatalf("répertoire de nœuds de oneshot : %s (serve : %s)", oneshotNodesDir, nodesDir)
	}
}
