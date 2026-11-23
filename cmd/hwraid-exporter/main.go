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
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/DisasteR/hwraid_exporter/internal/aacraid"
	"github.com/DisasteR/hwraid_exporter/internal/allowlist"
	"github.com/DisasteR/hwraid_exporter/internal/exporter"
	"github.com/DisasteR/hwraid_exporter/internal/lifecycle"
	"github.com/DisasteR/hwraid_exporter/internal/nodes"
	"github.com/DisasteR/hwraid_exporter/internal/privilege"
	"github.com/DisasteR/hwraid_exporter/internal/procstatus"
	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
	"github.com/DisasteR/hwraid_exporter/internal/transport/aacio"
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
	specs, serrs := nodes.Desired(hosts, majors)
	for _, e := range serrs {
		log.Warn("nœud non calculable", "err", e)
	}
	rep, err := nodes.Sync(nodesDir, specs, int(*uid), int(*gid))
	if err != nil {
		log.Error("préparation des nœuds", "dir", nodesDir, "err", err)
		return 1
	}
	log.Info("nœuds préparés", "créés", rep.Created, "remplacés", rep.Replaced, "corrigés", rep.Fixed, "supprimés", rep.Removed)

	// --- Abandon des privilèges ------------------------------------------
	keep := procstatus.Mask(procstatus.CapSysRawIO)
	if err := privilege.Drop(uint32(*uid), uint32(*gid), keep); err != nil {
		log.Error("abandon des privilèges", "err", err)
		return 1
	}
	if err := privilege.VerifyAllThreads(uint32(*uid), uint32(*gid), keep); err != nil {
		log.Error("vérification des privilèges", "err", err)
		return 1
	}
	log.Info("privilèges abandonnés", "uid", *uid, "gid", *gid, "capacités", "CAP_SYS_RAWIO")

	// --- Collecte et service ---------------------------------------------
	specByUID := map[string]nodes.Spec{}
	for _, s := range specs {
		specByUID[s.Name] = s
	}
	var snap atomic.Pointer[string]
	initial := exporter.Render(exporter.Snapshot{})
	snap.Store(&initial)

	ctx, stop := signal.NotifyContext(context.Background(), lifecycle.TerminationSignals()...)
	defer stop()
	exitCode := make(chan int, 1)
	startValid := map[string]bool{} // validité du nœud au premier cycle

	collect := func(first bool) {
		hosts, herrs, err := sysfs.Discover(*root)
		ok := err == nil
		if err != nil {
			log.Warn("découverte en échec", "err", err)
		}
		for _, e := range herrs {
			log.Warn("hôte SCSI ignoré", "host", e.Host, "err", e.Err)
		}
		check := func(h sysfs.SCSIHost) (aacraid.HBAInfo, error) {
			return checkNode(h, specByUID, list)
		}
		s := exporter.Build(hosts, ok, check)
		for _, c := range s.Controllers {
			if !c.NodeValid {
				log.Warn("nœud de gestion invalide", "controller", c.Label, "err", c.NodeError)
			}
			if first {
				startValid[c.PCIAddress] = c.NodeValid
			} else if startValid[c.PCIAddress] && !c.NodeValid {
				log.Error("nœud valide au démarrage devenu invalide : relance nécessaire", "controller", c.Label)
				select {
				case exitCode <- exitNodeChanged:
				default:
				}
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
	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("serveur HTTP", "err", err)
			exitCode <- 1
		}
	}()
	log.Info("écoute", "listen", *listen)

	go func() {
		collect(true)
		t := time.NewTicker(*interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				collect(false)
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

// checkNode vérifie le nœud dédié d'un hôte aacraid et lit GET_HBA_INFO.
// Le major courant est relu dans /proc/devices : un rechargement du module
// rend le nœud préparé invalide.
func checkNode(h sysfs.SCSIHost, specs map[string]nodes.Spec, list *allowlist.List) (aacraid.HBAInfo, error) {
	name := "aac" + h.UniqueID
	spec, ok := specs[name]
	if !ok {
		return aacraid.HBAInfo{}, fmt.Errorf("aucun nœud préparé pour %s", h.Name)
	}
	majors, err := readMajors()
	if err != nil {
		return aacraid.HBAInfo{}, err
	}
	if majors["aac"] != spec.Major {
		return aacraid.HBAInfo{}, fmt.Errorf("%w : major aac %d, préparé %d", aacio.ErrNodeMismatch, majors["aac"], spec.Major)
	}
	d, err := aacio.Open(filepath.Join(nodesDir, name), spec.Dev(), list)
	if err != nil {
		return aacraid.HBAInfo{}, err
	}
	defer d.Close()
	return d.HBAInfo()
}

func readMajors() (map[string]uint32, error) {
	f, err := os.Open("/proc/devices")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return nodes.CharMajors(f)
}
