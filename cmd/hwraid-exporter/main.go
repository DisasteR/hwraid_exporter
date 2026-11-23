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
	"sync/atomic"
	"time"

	"github.com/DisasteR/hwraid_exporter/internal/aacraid"
	"github.com/DisasteR/hwraid_exporter/internal/exporter"
	"github.com/DisasteR/hwraid_exporter/internal/lifecycle"
	"github.com/DisasteR/hwraid_exporter/internal/module"
	"github.com/DisasteR/hwraid_exporter/internal/modules/aacmod"
	"github.com/DisasteR/hwraid_exporter/internal/nodes"
	"github.com/DisasteR/hwraid_exporter/internal/privilege"
	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
)

// nodesDir est fixe (section 6.2 du cahier des charges).
const nodesDir = "/dev/hwraid_exporter"

// exitNodeChanged : un nœud valide au démarrage ne l'est plus ; le
// superviseur doit relancer le processus pour refaire la préparation.
const exitNodeChanged = 3

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "discover":
		discover(os.Args[2:])
	case "serve":
		os.Exit(serve(os.Args[2:]))
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage : hwraid-exporter discover [--sysfs /sys] | serve [options]")
	os.Exit(2)
}

func discover(args []string) {
	fs := flag.NewFlagSet("discover", flag.ExitOnError)
	root := fs.String("sysfs", "/sys", "racine de sysfs")
	_ = fs.Parse(args)
	hosts, herrs, err := sysfs.Discover(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "découverte impossible :", err)
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
	listen := fs.String("listen", "127.0.0.1:9799", "adresse d'écoute HTTP")
	root := fs.String("sysfs", "/sys", "racine de sysfs")
	uid := fs.Uint("uid", 65534, "identité d'exécution après abandon des privilèges")
	gid := fs.Uint("gid", 65534, "groupe d'exécution après abandon des privilèges")
	interval := fs.Duration("interval", time.Minute, "intervalle de collecte")
	_ = fs.Parse(args)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *interval <= 0 {
		log.Error("intervalle invalide", "interval", *interval)
		return 2
	}
	if *uid == 0 || *gid == 0 || *uid >= 1<<32-1 || *gid >= 1<<32-1 {
		log.Error("identité d'exécution invalide : ni root ni hors plage", "uid", *uid, "gid", *gid)
		return 2
	}

	// --- Phase de préparation (root) -------------------------------------
	if os.Geteuid() != 0 {
		log.Error("la phase de préparation exige de démarrer en root")
		return 1
	}
	list, err := aacraid.NewAllowlist()
	if err != nil {
		log.Error("liste blanche invalide", "err", err)
		return 1
	}
	hosts, herrs, err := sysfs.Discover(*root)
	if err != nil {
		log.Error("découverte impossible", "err", err)
		return 1
	}
	for _, e := range herrs {
		log.Warn("hôte SCSI ignoré", "host", e.Host, "err", e.Err)
	}
	majors, err := readMajors()
	if err != nil {
		log.Error("lecture de /proc/devices", "err", err)
		return 1
	}
	// Chaque module déclare ses nœuds et ses capacités ; le cœur crée seul.
	registry := []module.Module{aacmod.New(list)}
	plan := module.Build(hosts, registry)
	specs := plan.Specs(majors)
	for _, pc := range plan.Cards {
		if pc.Err != nil {
			log.Warn("carte non préparée", "pci", pc.Card.PCI.Address, "err", pc.Err)
		}
	}
	rep, err := nodes.Sync(nodesDir, specs, majors, int(*uid), int(*gid))
	if err != nil {
		log.Error("préparation des nœuds", "dir", nodesDir, "err", err)
		return 1
	}
	log.Info("nœuds préparés", "créés", rep.Created, "remplacés", rep.Replaced, "corrigés", rep.Fixed, "supprimés", rep.Removed)

	// --- Abandon des privilèges ------------------------------------------
	// Union des capacités des modules ayant une carte : aucune sans carte.
	keep := plan.Keep
	if err := privilege.Drop(uint32(*uid), uint32(*gid), keep); err != nil {
		log.Error("abandon des privilèges", "err", err)
		return 1
	}
	if err := privilege.VerifyAllThreads(uint32(*uid), uint32(*gid), keep); err != nil {
		log.Error("vérification des privilèges", "err", err)
		return 1
	}
	log.Info("privilèges abandonnés", "uid", *uid, "gid", *gid, "capacités", fmt.Sprintf("%#x", keep))

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

	collect := func() {
		hosts, _, err := sysfs.Discover(*root)
		ok := err == nil
		if err != nil {
			log.Warn("découverte en échec", "err", err)
		}
		// L'ensemble des nœuds attendus a-t-il changé depuis la préparation ?
		// (carte ajoutée ou retirée, module rechargé, unique_id réattribué)
		cur := module.Build(hosts, registry)
		if ok {
			if m, cerr := readMajors(); cerr == nil && !sameSpecs(cur.Specs(m), specs) {
				log.Error("les nœuds attendus ont changé depuis la préparation : relance nécessaire")
				requestExit(exitNodeChanged)
			}
		}
		results := map[string]module.Result{}
		for _, pc := range cur.Cards {
			if pc.Module == nil || pc.Err != nil {
				continue
			}
			results[pc.Card.PCI.Address] = pc.Module.Collect(pc.Card, module.Refs(nodesDir, pc, specs))
		}
		s := exporter.Build(cur, ok, results)
		for _, c := range s.Controllers {
			if !c.NodeValid {
				// Adresse PCI et non label : le label peut contenir la série (S9).
				log.Warn("nœud de gestion invalide", "pci", c.PCIAddress, "err", c.NodeError)
			}
			if c.CollectError != "" {
				log.Warn("lecture en échec", "pci", c.PCIAddress, "err", c.CollectError)
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
			log.Error("serveur HTTP", "err", err)
			requestExit(1)
		}
	}()
	log.Info("écoute", "listen", *listen)

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
