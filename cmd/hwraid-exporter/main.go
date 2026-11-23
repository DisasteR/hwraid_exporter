// Commande hwraid-exporter (prototype 0).
//
//	discover : inventaire sysfs en lecture seule, sans privilège.
//	serve    : préparation des nœuds en root, abandon des privilèges,
//	           puis collecte périodique et service de /metrics.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"github.com/DisasteR/hwraid_exporter/internal/aacraid"
	"github.com/DisasteR/hwraid_exporter/internal/buildinfo"
	"github.com/DisasteR/hwraid_exporter/internal/exporter"
	"github.com/DisasteR/hwraid_exporter/internal/lifecycle"
	"github.com/DisasteR/hwraid_exporter/internal/module"
	"github.com/DisasteR/hwraid_exporter/internal/modules/aacmod"
	"github.com/DisasteR/hwraid_exporter/internal/nodes"
	"github.com/DisasteR/hwraid_exporter/internal/privilege"
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

func main() {
	bi, _ := debug.ReadBuildInfo()
	version, revision = buildinfo.Resolve(version, revision, bi)
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "discover":
		discover(os.Args[2:])
	case "serve":
		os.Exit(serve(os.Args[2:]))
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: hwraid-exporter discover [--sysfs /sys] | serve [options] | version")
	os.Exit(2)
}

func discover(args []string) {
	fs := flag.NewFlagSet("discover", flag.ExitOnError)
	root := fs.String("sysfs", "/sys", "sysfs root")
	_ = fs.Parse(args)
	hosts, herrs, err := sysfs.Discover(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "discovery failed:", err)
		os.Exit(1)
	}
	out := struct {
		Hosts  []sysfs.SCSIHost `json:"hosts"`
		Errors []string         `json:"errors,omitempty"`
	}{Hosts: append([]sysfs.SCSIHost{}, hosts...)}
	for _, e := range herrs {
		out.Errors = append(out.Errors, e.Error())
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func serve(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:9799", "HTTP listen address")
	root := fs.String("sysfs", "/sys", "sysfs root")
	uid := fs.Uint("uid", 65534, "user ID after the privilege drop")
	gid := fs.Uint("gid", 65534, "group ID after the privilege drop")
	interval := fs.Duration("interval", time.Minute, "collection interval")
	timeout := fs.Duration("collect-timeout", 0, "maximum time to wait for one card in a cycle (default: the smaller of 30s and -interval)")
	_ = fs.Parse(args)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	exporter.Version, exporter.Revision = version, revision
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

	// --- Phase de préparation (root) -------------------------------------
	if os.Geteuid() != 0 {
		log.Error("the preparation phase requires starting as root")
		return 1
	}
	list, err := aacraid.NewAllowlist()
	if err != nil {
		log.Error("invalid allowlist", "err", err)
		return 1
	}
	hosts, herrs, err := sysfs.Discover(*root)
	if err != nil {
		log.Error("discovery failed", "err", err)
		return 1
	}
	for _, e := range herrs {
		log.Warn("SCSI host skipped", "host", e.Host, "err", e.Err)
	}
	majors, err := readMajors()
	if err != nil {
		log.Error("reading /proc/devices", "err", err)
		return 1
	}
	// Chaque module déclare ses nœuds et ses capacités ; le cœur crée seul.
	registry := []module.Module{aacmod.New(list)}
	plan := module.Build(hosts, registry)
	specs := plan.Specs(majors)
	complete := len(herrs) == 0
	for _, pc := range plan.Cards {
		if pc.Err != nil {
			log.Warn("card not prepared", "pci", pc.Card.PCI.Address, "err", pc.Err)
			complete = false
		}
	}
	if !complete {
		log.Warn("incomplete inventory: no existing node will be removed")
	}
	rep, err := nodes.Sync(nodesDir, specs, majors, int(*uid), int(*gid), complete)
	if err != nil {
		log.Error("node preparation", "dir", nodesDir, "err", err)
		return 1
	}
	log.Info("nodes prepared", "created", rep.Created, "replaced", rep.Replaced, "fixed", rep.Fixed, "removed", rep.Removed)

	// --- Abandon des privilèges ------------------------------------------
	// Union des capacités des modules ayant une carte : aucune sans carte.
	keep := plan.Keep
	if err := privilege.Drop(uint32(*uid), uint32(*gid), keep); err != nil {
		log.Error("privilege drop", "err", err)
		return 1
	}
	if err := privilege.VerifyAllThreads(uint32(*uid), uint32(*gid), keep); err != nil {
		log.Error("privilege check", "err", err)
		return 1
	}
	log.Info("privileges dropped", "uid", *uid, "gid", *gid, "capabilities", fmt.Sprintf("%#x", keep))

	// --- Collecte et service ---------------------------------------------
	var snap atomic.Pointer[string]
	initial := exporter.Render(exporter.Snapshot{})
	snap.Store(&initial)

	ctx, stop := signal.NotifyContext(context.Background(), lifecycle.TerminationSignals()...)
	defer stop()
	exitCode := make(chan int, 2)
	requestExit := func(code int) {
		select {
		case exitCode <- code:
		default:
		}
	}

	tracker := exporter.NewTracker()
	runner := exporter.NewRunner()
	lastHostErrs := ""
	collect := func() {
		hosts, herrs, err := sysfs.Discover(*root)
		// Découverte réussie : sans erreur globale ni hôte illisible.
		ok := err == nil && len(herrs) == 0
		if err != nil {
			log.Warn("discovery failed", "err", err)
		}
		var msgs []string
		for _, e := range herrs {
			msgs = append(msgs, e.Host+": "+e.Err.Error())
		}
		if cur := strings.Join(msgs, "; "); cur != lastHostErrs {
			if cur != "" {
				log.Warn("unreadable SCSI hosts", "err", cur)
			} else if lastHostErrs != "" {
				log.Info("SCSI hosts readable again")
			}
			lastHostErrs = cur
		}
		cur := module.Build(hosts, registry)
		// L'ensemble des nœuds attendus a-t-il changé depuis la préparation ?
		// (carte ajoutée ou retirée, module rechargé, major réattribué) Si
		// oui, ou si le major ne peut être confirmé, aucun nœud n'est ouvert.
		// Inventaire incomplet (hôte illisible) : le changement n'est pas
		// prouvé, pas de relance ; seules les cartes dont les nœuds sont
		// exactement ceux préparés sont collectées.
		collectOK := err == nil
		var majors map[string]uint32
		if err == nil {
			var cerr error
			switch majors, cerr = readMajors(); {
			case cerr != nil:
				log.Error("reading /proc/devices: collection suspended", "err", cerr)
				collectOK = false
			case !sameSpecs(cur.Specs(majors), specs) && ok:
				log.Error("expected nodes changed since preparation: restart required")
				requestExit(exitNodeChanged)
				collectOK = false
			}
		}
		jobs := map[string]func() module.Result{}
		collectors := map[string][]string{}
		for _, pc := range cur.Cards {
			if !collectOK || pc.Module == nil || !module.Prepared(pc, majors, specs) {
				continue
			}
			addr := pc.Card.PCI.Address
			jobs[addr] = func() module.Result { return pc.Module.Collect(pc.Card, module.Refs(nodesDir, pc, specs)) }
			collectors[addr] = pc.Module.Collectors()
		}
		results := runner.Run(jobs, collectors, *timeout)
		if err == nil {
			present := map[string]bool{}
			for _, pc := range cur.Cards {
				present[pc.Card.PCI.Address] = true
			}
			if !ok {
				// Hôte illisible : une carte peut manquer à l'inventaire de ce
				// cycle sans avoir été retirée. Rien n'est oublié.
				for _, a := range tracker.Known() {
					present[a] = true
				}
			}
			addrs := make([]string, 0, len(present))
			for a := range present {
				addrs = append(addrs, a)
			}
			tracker.Update(results, addrs, time.Now())
		}
		s := exporter.Build(cur, ok, results, tracker, *interval)
		tracker.Remember(s)
		for _, c := range s.Controllers {
			if c.NodeChecked && !c.NodeValid {
				// Adresse PCI et non label : le label peut contenir la série (S9).
				log.Warn("invalid management node", "pci", c.PCIAddress, "err", c.NodeError)
			}
			if c.TimedOut {
				log.Warn("collection timed out", "pci", c.PCIAddress, "timeout", *timeout)
			} else if c.CollectError != "" {
				log.Warn("read failed", "pci", c.PCIAddress, "err", c.CollectError)
			}
		}
		out := exporter.Render(s)
		snap.Store(&out)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(*snap.Load()))
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

	go func() {
		collect()
		t := time.NewTicker(*interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				collect()
			}
		}
	}()

	code := 0
	select {
	case <-ctx.Done():
	case code = <-exitCode:
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
