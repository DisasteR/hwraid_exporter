// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

// Commande hwraid-exporter : exporter Prometheus pour contrôleurs RAID matériels.
//
//	discover : cartes telles que serve les voit (module, statut de
//	           lecture), depuis sysfs seul, sans privilège ni commande.
//	serve    : préparation des nœuds en root, abandon des privilèges,
//	           puis collecte périodique et service de /metrics.
//	oneshot  : même préparation (nœuds propres), une collecte, puis le
//	           texte de /metrics sur la sortie standard.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/DisasteR/hwraid_exporter/internal/buildinfo"
	"github.com/DisasteR/hwraid_exporter/internal/exporter"
	"github.com/DisasteR/hwraid_exporter/internal/lifecycle"
	"github.com/DisasteR/hwraid_exporter/internal/module"
	"github.com/DisasteR/hwraid_exporter/internal/nodes"
	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
)

// version et revision sont fixées à la compilation
// (-ldflags "-X main.version=v0.1.0 -X main.revision=<commit>") ; à défaut,
// elles sont déduites des informations de compilation (buildinfo.Resolve).
var version, revision string

// nodesDir est fixe (section 6.2 du cahier des charges).
const nodesDir = "/dev/hwraid_exporter"

// exitNodeChanged : un nœud valide au démarrage ne l'est plus ; le
// superviseur doit relancer le processus pour refaire la préparation.
const exitNodeChanged = 3

// shutdownWarnEvery : à l'arrêt, intervalle des avertissements tant qu'une
// commande de la carte n'a pas rendu la main.
const shutdownWarnEvery = 10 * time.Second

func main() {
	bi, _ := debug.ReadBuildInfo()
	version, revision = buildinfo.Resolve(version, revision, bi)
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "discover":
		os.Exit(discover(os.Args[2:], os.Stdout, os.Stderr))
	case "serve":
		os.Exit(serve(os.Args[2:]))
	case "oneshot":
		os.Exit(oneshot(os.Args[2:], os.Stdout))
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: hwraid-exporter discover [-sysfs /sys] [-allow-unqualified …] | serve [options] | oneshot [options] | version")
	os.Exit(2)
}

func serve(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:9799", "HTTP listen address")
	root := fs.String("sysfs", "/sys", "sysfs root")
	uid := fs.Uint("uid", 65534, "user ID after the privilege drop")
	gid := fs.Uint("gid", 65534, "group ID after the privilege drop")
	interval := fs.Duration("interval", time.Minute, "collection interval")
	timeout := fs.Duration("collect-timeout", 0, "maximum time to wait for one card in a cycle (default: the smaller of 30s and -interval)")
	var allow []string
	fs.Func("allow-unqualified", "also read the cards of this module whose model is not qualified, with the vendor tool's command sequence (risks: docs/deploiement.md); repeatable; value: a module name (aacraid), or serial:<number> for the card whose sysfs serial number is <number> (as in its controller label)", func(v string) error {
		allow = append(allow, v)
		return nil
	})
	_ = fs.Parse(args)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	exporter.Version, exporter.Revision = version, revision
	// Les options s'arrêtent au premier argument positionnel : en accepter
	// un laisserait passer, sans la valider, une option placée après.
	if fs.NArg() > 0 {
		log.Error("unexpected argument: options only", "position", len(args)-fs.NArg()+1)
		return 2
	}
	if *interval <= 0 {
		log.Error("invalid interval", "interval", *interval)
		return 2
	}
	if *timeout == 0 {
		*timeout = min(30*time.Second, *interval)
	}
	if *timeout < 0 || *timeout > *interval {
		log.Error("invalid collect timeout: must be positive and at most the interval", "collect-timeout", *timeout, "interval", *interval)
		return 2
	}
	if *uid == 0 || *gid == 0 || *uid >= 1<<32-1 || *gid >= 1<<32-1 {
		log.Error("invalid run-as identity: must be neither root nor out of range", "uid", *uid, "gid", *gid)
		return 2
	}

	// --- Phase de préparation (root), puis abandon des privilèges --------
	p, code := prepare(log, *root, nodesDir, uint32(*uid), uint32(*gid), allow)
	if code != 0 {
		return code
	}

	// --- Collecte et service ---------------------------------------------
	// Vue sans valeur matérielle : avant la première collecte, à l'arrêt, et
	// si la boucle de collecte ne publie plus depuis trois intervalles et un
	// délai de cycle (exporter.ViewMaxAge).
	empty := exporter.Render(exporter.Snapshot{})
	pub := exporter.NewPublisher(empty, empty, exporter.ViewMaxAge(*interval, *timeout))

	lifecycle.IgnoreBrokenPipe()
	ctx, stop := signal.NotifyContext(context.Background(), lifecycle.TerminationSignals()...)
	defer stop()
	exitCode := make(chan int, 2)
	requestExit := func(code int) {
		select {
		case exitCode <- code:
		default:
		}
	}

	runner := exporter.NewRunner()
	cyc := &cycler{log: log, root: *root, p: p, timeout: *timeout, interval: *interval,
		tracker: exporter.NewTracker(), runner: runner, requestExit: requestExit}
	// collect fait un cycle et publie son instantané, sauf s'il a été
	// interrompu par l'arrêt.
	collect := func(stopping context.Context) {
		if s, publish := cyc.run(stopping); publish {
			pub.Publish(exporter.Render(s))
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(pub.Load()))
	})
	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP server", "err", err)
			requestExit(1)
		}
	}()
	log.Info("listening", "listen", *listen, "version", version, "revision", revision)

	// La boucle s'arrête au signal comme à une demande de sortie (code 3).
	loopCtx, stopLoop := context.WithCancel(ctx)
	defer stopLoop()
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		lifecycle.RunLoop(loopCtx, *interval, collect)
	}()

	code = 0
	select {
	case <-ctx.Done():
	case code = <-exitCode:
	}
	// Sortir pendant une commande de gestion l'interromprait dans le pilote,
	// qui perdrait un bloc de commande jusqu'au redémarrage de la machine.
	// Aucun nouveau cycle ne démarre et le cycle en cours n'envoie plus de
	// nouvelle commande : seules les commandes déjà envoyées restent à
	// attendre, y compris celles des collectes abandonnées à leur échéance.
	// L'attente n'a pas de limite interne : seul le délai du superviseur
	// (stop_grace_period, TimeoutStopSec) l'abrège, par SIGKILL. Les signaux
	// suivants restent captés.
	stopLoop()
	// Plus aucune valeur matérielle n'est servie pendant l'attente : la vue
	// n'est plus mise à jour, et une attente longue (commande bloquée,
	// sortie sur changement de nœuds) ne doit pas faire passer un état
	// ancien pour un état courant.
	pub.Stop()
	log.Info("stopping: waiting for the card commands in progress")
	lifecycle.WaitIdle(loopDone, runner.Drain, shutdownWarnEvery, func(waited time.Duration) {
		log.Warn("stopping: a card command has not returned yet; killing the process now would make the aacraid driver lose a management command until the next reboot", "waited", waited.Round(time.Second))
	})
	for addr, err := range runner.LateErrors() {
		log.Warn("a collection finished during the stop, not published", "pci", addr, "err", err)
	}
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shut)
	return code
}

// sameSpecs compare deux ensembles de nœuds attendus (déjà triés par nom).
func sameSpecs(a, b []nodes.Spec) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func readMajors() (map[string]uint32, error) {
	f, err := os.Open("/proc/devices")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return nodes.CharMajors(f)
}

// buildPlan construit le plan d'une découverte (hôtes, hôtes illisibles,
// erreur globale) et y établit l'unicité des numéros de série (sélecteur
// -allow-unqualified=serial:) : seulement si la découverte n'a eu ni
// erreur globale ni hôte illisible.
func buildPlan(hosts []sysfs.SCSIHost, herrs []sysfs.HostError, err error, registry []module.Module) module.Plan {
	p := module.Build(hosts, registry)
	p.ResolveSerials(err == nil && len(herrs) == 0)
	return p
}

// allowUnqualified applique les valeurs de -allow-unqualified (CDC, section
// 6.12) : chacune nomme un module du registre qui qualifie ses cartes, ou
// un numéro de série (serial:<numéro>, transmis à chacun de ces modules).
// Une valeur invalide fait échouer le démarrage, sans rien autoriser. Rend
// les modules et les numéros autorisés.
func allowUnqualified(values []string, registry []module.Module) (modules, serials []string, err error) {
	var qualifiers []module.Qualifier
	for _, m := range registry {
		if q, ok := m.(module.Qualifier); ok {
			qualifiers = append(qualifiers, q)
		}
	}
	var allowed []string
	var apply []func()
	for _, v := range values {
		if s, ok := strings.CutPrefix(v, "serial:"); ok {
			switch {
			// Messages sans le numéro (exigence S9).
			case s == "" || s != strings.TrimSpace(s):
				return nil, nil, errors.New("serial: empty serial number, or surrounded by spaces")
			case strings.HasPrefix(s, "pci:") || strings.HasPrefix(s, "serial:"):
				return nil, nil, errors.New("serial: a label prefix follows serial:; give the serial number only (a card labelled pci: cannot be selected by serial number)")
			case len(qualifiers) == 0:
				return nil, nil, errors.New("serial: no module qualifies its cards")
			}
			if !slices.Contains(serials, s) {
				serials = append(serials, s)
				for _, q := range qualifiers {
					apply = append(apply, func() { q.AllowUnqualifiedSerial(s) })
				}
			}
			continue
		}
		var found module.Module
		for _, m := range registry {
			if m.Name() == v {
				found = m
			}
		}
		q, ok := found.(module.Qualifier)
		switch {
		case found == nil:
			return nil, nil, errors.New("unknown module (a serial number is given as serial:<number>, in lower case)")
		case !ok:
			return nil, nil, fmt.Errorf("%q: module does not qualify its cards", v)
		}
		if !slices.Contains(allowed, v) {
			apply = append(apply, q.AllowUnqualified)
			allowed = append(allowed, v)
		}
	}
	for _, f := range apply {
		f()
	}
	return allowed, serials, nil
}
