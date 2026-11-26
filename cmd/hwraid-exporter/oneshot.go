// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DisasteR/hwraid_exporter/internal/exporter"
	"github.com/DisasteR/hwraid_exporter/internal/lifecycle"
	"github.com/DisasteR/hwraid_exporter/internal/nodes"
)

// oneshotNodesDir : nœuds dédiés de oneshot, distincts de ceux de serve
// (nodesDir), que la préparation de oneshot ne doit jamais toucher : la
// synchronisation retire les nœuds qu'elle n'attend pas.
const oneshotNodesDir = "/dev/hwraid_exporter_oneshot"

// serveRunning cherche, dans procRoot (/proc), un autre processus
// « hwraid-exporter serve » : nom de programme hwraid-exporter, premier
// argument serve. Rend son PID, ou 0. Un processus disparu ou illisible
// pendant le parcours est ignoré ; procRoot illisible est une erreur.
// Limites : un binaire renommé, ou un serve que ce /proc ne montre pas
// (oneshot dans un conteneur, serve sur l'hôte), n'est pas vu.
func serveRunning(procRoot string, self int) (int, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		b, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		argv := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		if len(argv) >= 2 && filepath.Base(argv[0]) == "hwraid-exporter" && argv[1] == "serve" {
			return pid, nil
		}
	}
	return 0, nil
}

// oneshotOutcome rend le code de sortie d'une collecte unique : 0 si la
// découverte est complète et si chaque carte prise en charge par un module
// a un nœud valide, a été collectée dans le délai et a réussi chacune de
// ses lectures ; 1 sinon. Une carte sans module, ou dont les collecteurs
// sont non pris en charge (modèle non qualifié sans option), n'est pas un
// échec : les métriques le montrent.
func oneshotOutcome(s exporter.Snapshot) int {
	if !s.DiscoverySuccess {
		return 1
	}
	for _, c := range s.Controllers {
		if c.Backend == "" {
			continue
		}
		if !c.NodeValid || c.TimedOut {
			return 1
		}
		for _, ok := range c.Collected {
			if !ok {
				return 1
			}
		}
	}
	return 0
}

// oneshot fait une seule collecte, comme un cycle de serve, et écrit sur
// stdout le texte que /metrics servirait alors (sans intervalle de
// collecte), ou, avec -format text, un résumé lisible des mêmes valeurs. Préparation en root dans ses propres nœuds (oneshotNodesDir),
// puis abandon des privilèges. Refuse de démarrer si un serve tourne : deux
// exporters intercaleraient leurs transferts sur la carte. Un signal
// d'arrêt laisse finir la commande en cours, puis rien n'est écrit. Code de
// sortie : oneshotOutcome, 1 si interrompu ou impossible, 2 pour une option
// invalide.
func oneshot(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("oneshot", flag.ContinueOnError)
	root := fs.String("sysfs", "/sys", "sysfs root")
	uid := fs.Uint("uid", 65534, "user ID after the privilege drop")
	gid := fs.Uint("gid", 65534, "group ID after the privilege drop")
	timeout := fs.Duration("collect-timeout", 30*time.Second, "maximum time to wait for one card")
	format := fs.String("format", "prometheus", "output: prometheus (the text /metrics would serve) or text (readable summary, same normalized values)")
	var allow []string
	fs.Func("allow-unqualified", "as for serve (a module name, or serial:<number>)", func(v string) error {
		allow = append(allow, v)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return 2
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	exporter.Version, exporter.Revision = version, revision
	if fs.NArg() > 0 {
		log.Error("unexpected argument: options only", "position", len(args)-fs.NArg()+1)
		return 2
	}
	render := map[string]func(exporter.Snapshot) string{"prometheus": exporter.Render, "text": exporter.RenderText}[*format]
	if render == nil {
		log.Error("invalid format: prometheus or text", "format", *format)
		return 2
	}
	if *timeout <= 0 {
		log.Error("invalid collect timeout: must be positive", "collect-timeout", *timeout)
		return 2
	}
	if *uid == 0 || *gid == 0 || *uid >= 1<<32-1 || *gid >= 1<<32-1 {
		log.Error("invalid run-as identity: must be neither root nor out of range", "uid", *uid, "gid", *gid)
		return 2
	}
	switch pid, err := serveRunning("/proc", os.Getpid()); {
	case err != nil:
		log.Error("cannot check for a running serve: refused", "err", err)
		return 1
	case pid != 0:
		log.Error("hwraid-exporter serve is running: refused, both would interleave their transfers on the card; use its /metrics instead", "pid", pid)
		return 1
	}
	p, code := prepare(log, *root, oneshotNodesDir, uint32(*uid), uint32(*gid), allow)
	if code != 0 {
		return code
	}
	// Un seul oneshot à la fois : verrou tenu sur son répertoire de nœuds
	// jusqu'à la sortie (un second, sur la même carte, intercalerait ses
	// transferts). Pris après la préparation, qui ne fait que reproduire les
	// mêmes nœuds.
	unlock, err := nodes.LockDir(oneshotNodesDir)
	if err != nil {
		log.Error("another oneshot is running, or its node directory cannot be locked: refused", "dir", oneshotNodesDir, "err", err)
		return 1
	}
	defer unlock()
	lifecycle.IgnoreBrokenPipe()
	ctx, stop := signal.NotifyContext(context.Background(), lifecycle.TerminationSignals()...)
	defer stop()
	runner := exporter.NewRunner()
	nodesChanged := false
	cyc := &cycler{log: log, root: *root, p: p, timeout: *timeout, tracker: exporter.NewTracker(), runner: runner,
		requestExit: func(int) { nodesChanged = true }}
	s, publish := cyc.run(ctx)
	// Comme serve à l'arrêt : sortir pendant une commande de gestion
	// l'interromprait dans le pilote (collecte abandonnée à son délai).
	done := make(chan struct{})
	close(done)
	lifecycle.WaitIdle(done, runner.Drain, shutdownWarnEvery, func(waited time.Duration) {
		log.Warn("a card command has not returned yet; killing the process now would make the aacraid driver lose a management command until the next reboot", "waited", waited.Round(time.Second))
	})
	for addr, err := range runner.LateErrors() {
		log.Warn("a collection finished after the cycle ended (timeout or stop signal), not included", "pci", addr, "err", err)
	}
	return oneshotFinish(log, stdout, render, s, publish && ctx.Err() == nil, nodesChanged)
}

// oneshotFinish écrit le résultat d'une collecte unique et rend le code de
// sortie : rien n'est écrit si la collecte a été interrompue (signal reçu
// pendant le cycle ou pendant l'attente des commandes en cours) ou si les
// nœuds attendus ont changé depuis la préparation (aucune carte collectée).
func oneshotFinish(log *slog.Logger, stdout io.Writer, render func(exporter.Snapshot) string, s exporter.Snapshot, complete, nodesChanged bool) int {
	switch {
	case !complete:
		log.Error("collection interrupted: nothing written")
		return 1
	case nodesChanged:
		log.Error("expected nodes changed since preparation (card added or removed, driver reloaded): nothing collected, nothing written")
		return 1
	}
	if _, err := io.WriteString(stdout, render(s)); err != nil {
		log.Error("writing the result", "err", err)
		return 1
	}
	return oneshotOutcome(s)
}
