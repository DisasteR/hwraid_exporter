// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/DisasteR/hwraid_exporter/internal/aacraid"
	"github.com/DisasteR/hwraid_exporter/internal/exporter"
	"github.com/DisasteR/hwraid_exporter/internal/module"
	"github.com/DisasteR/hwraid_exporter/internal/modules/aacmod"
	"github.com/DisasteR/hwraid_exporter/internal/nodes"
	"github.com/DisasteR/hwraid_exporter/internal/privilege"
	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
)

// prepared est le résultat de la phase de préparation : modules (avec leurs
// autorisations), nœuds préparés et répertoire qui les contient.
type prepared struct {
	registry []module.Module
	specs    []nodes.Spec
	dir      string
}

// prepare fait la phase de préparation, en root, commune à serve et
// oneshot : découverte, autorisations (-allow-unqualified), plan, nœuds
// dédiés dans dir, puis abandon des privilèges (uid, gid et capacités des
// seuls modules dont une carte est préparée). code non nul : arrêt avec ce
// code de sortie, cause journalisée.
func prepare(log *slog.Logger, root, dir string, uid, gid uint32, allow []string) (p prepared, code int) {
	if os.Geteuid() != 0 {
		log.Error("the preparation phase requires starting as root")
		return p, 1
	}
	list, err := aacraid.NewAllowlist()
	if err != nil {
		log.Error("invalid allowlist", "err", err)
		return p, 1
	}
	hosts, herrs, err := sysfs.Discover(root)
	if err != nil {
		log.Error("discovery failed", "err", err)
		return p, 1
	}
	for _, e := range herrs {
		log.Warn("SCSI host skipped", "host", e.Host, "err", e.Err)
	}
	majors, err := readMajors()
	if err != nil {
		log.Error("reading /proc/devices", "err", err)
		return p, 1
	}
	// Chaque module déclare ses nœuds et ses capacités ; le cœur crée seul.
	registry := []module.Module{aacmod.New(list)}
	allowed, serials, err := allowUnqualified(allow, registry)
	if err != nil {
		log.Error("invalid -allow-unqualified", "err", err)
		return p, 2
	}
	for _, name := range allowed {
		log.Warn("cards of an unqualified model will be read: commands the vendor does not document, checked only against constructed answers on such a model", "module", name)
	}
	plan := buildPlan(hosts, herrs, nil, registry)
	// Journaux sans numéro de série (exigence S9) : le sélecteur est désigné
	// par son rang dans les valeurs serial: de l'option.
	for i, s := range serials {
		log.Warn("the card selected by serial number may be read even if its model is not qualified: commands the vendor does not document, checked only against constructed answers on such a model", "serial_selector", i+1)
		if !slices.ContainsFunc(plan.Cards, func(pc module.Planned) bool {
			fields, _ := pc.Card.Identity()
			_, qualifies := pc.Module.(module.Qualifier)
			return qualifies && pc.Card.UniqueSerial && fields[0] == s
		}) {
			log.Warn("no card of a module that qualifies its cards announces this serial number alone in a complete inventory: this selector designates no card until one does", "serial_selector", i+1)
		}
	}
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
	rep, err := nodes.Sync(dir, specs, majors, int(uid), int(gid), complete)
	if err != nil {
		log.Error("node preparation", "dir", dir, "err", err)
		return p, 1
	}
	log.Info("nodes prepared", "created", rep.Created, "replaced", rep.Replaced, "fixed", rep.Fixed, "removed", rep.Removed)

	// Union des capacités des modules dont une carte est préparée : aucune
	// sans carte collectable.
	keep := plan.KeepPrepared(majors, specs)
	if err := privilege.Drop(uid, gid, keep); err != nil {
		log.Error("privilege drop", "err", err)
		return p, 1
	}
	if err := privilege.VerifyAllThreads(uid, gid, keep); err != nil {
		log.Error("privilege check", "err", err)
		return p, 1
	}
	log.Info("privileges dropped", "uid", uid, "gid", gid, "capabilities", fmt.Sprintf("%#x", keep))
	return prepared{registry: registry, specs: specs, dir: dir}, 0
}

// cycler fait les cycles de collecte sur les nœuds préparés : un seul pour
// oneshot, un par intervalle pour serve. Ni lui ni ses champs ne sont
// partagés entre goroutines : les cycles se suivent.
type cycler struct {
	log      *slog.Logger
	root     string
	p        prepared
	timeout  time.Duration
	interval time.Duration // publié (hwraid_collection_interval_seconds) ; 0 : non publié
	tracker  *exporter.Tracker
	runner   *exporter.Runner
	// requestExit demande la sortie du service (code 3, nœuds changés).
	requestExit  func(int)
	lastHostErrs string
}

// run fait un cycle. stopping est terminé à l'arrêt : aucune nouvelle
// commande n'est alors envoyée à une carte, la commande en cours se
// termine. publish est faux si l'arrêt a interrompu le cycle : ses lectures
// sont incomplètes et ne doivent pas être publiées.
func (c *cycler) run(stopping context.Context) (s exporter.Snapshot, publish bool) {
	log := c.log
	hosts, herrs, err := sysfs.Discover(c.root)
	// Découverte réussie : sans erreur globale ni hôte illisible.
	ok := err == nil && len(herrs) == 0
	if err != nil {
		log.Warn("discovery failed", "err", err)
	}
	var msgs []string
	for _, e := range herrs {
		msgs = append(msgs, e.Host+": "+e.Err.Error())
	}
	if cur := strings.Join(msgs, "; "); cur != c.lastHostErrs {
		if cur != "" {
			log.Warn("unreadable SCSI hosts", "err", cur)
		} else if c.lastHostErrs != "" {
			log.Info("SCSI hosts readable again")
		}
		c.lastHostErrs = cur
	}
	cur := buildPlan(hosts, herrs, err, c.p.registry)
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
		case !sameSpecs(cur.Specs(majors), c.p.specs) && ok:
			log.Error("expected nodes changed since preparation: restart required")
			c.requestExit(exitNodeChanged)
			collectOK = false
		}
	}
	jobs := map[string]func() module.Result{}
	// Contexte commun du cycle, transmis aux modules : terminé à
	// l'échéance du cycle ou à l'arrêt, après quoi plus aucune nouvelle
	// commande n'est envoyée.
	cycle, endCycle := context.WithTimeout(stopping, c.timeout)
	defer endCycle()
	collectors := map[string][]string{}
	for _, pc := range cur.Cards {
		if !collectOK || pc.Module == nil || !module.Prepared(pc, majors, c.p.specs) {
			continue
		}
		addr := pc.Card.PCI.Address
		jobs[addr] = func() module.Result {
			return pc.Module.Collect(cycle, pc.Card, module.Refs(c.p.dir, pc, c.p.specs))
		}
		collectors[addr] = pc.Module.Collectors()
	}
	results := c.runner.Run(cycle, jobs, collectors)
	if stopping.Err() != nil {
		// Arrêt pendant le cycle : lectures incomplètes, non publiées. Les
		// erreurs tardives déjà rendues dans ce cycle sont journalisées
		// ici, puisque le cycle n'est pas publié.
		for addr, r := range results {
			if err := errors.Join(r.Err, r.CollectErr, r.LateErr); err != nil {
				log.Warn("collection errors in the cycle interrupted by the stop, not published", "pci", addr, "err", err)
			}
		}
		return exporter.Snapshot{}, false
	}
	if err == nil {
		present := map[string]bool{}
		for _, pc := range cur.Cards {
			present[pc.Card.PCI.Address] = true
		}
		if !ok {
			// Hôte illisible : une carte peut manquer à l'inventaire de ce
			// cycle sans avoir été retirée. Rien n'est oublié.
			for _, a := range c.tracker.Known() {
				present[a] = true
			}
		}
		addrs := make([]string, 0, len(present))
		for a := range present {
			addrs = append(addrs, a)
		}
		c.tracker.Update(results, addrs, time.Now())
	}
	s = exporter.Build(cur, ok, results, c.tracker, c.interval)
	for _, ch := range c.tracker.Remember(s) {
		log.Warn("card identity changed at the same PCI address while running: a card is neither replaced nor updated hot; driver reload or anomaly, kept answers and history of the address discarded", "pci", ch.PCIAddress, "fields", strings.Join(ch.Fields, ","))
	}
	for _, ctl := range s.Controllers {
		if ctl.NodeChecked && !ctl.NodeValid {
			// Adresse PCI et non label : le label peut contenir la série (S9).
			log.Warn("invalid management node", "pci", ctl.PCIAddress, "err", ctl.NodeError)
		}
		if ctl.LateError != "" {
			log.Warn("a collection finished after its timeout, not published", "pci", ctl.PCIAddress, "err", ctl.LateError)
		}
		if ctl.TimedOut {
			log.Warn("collection timed out", "pci", ctl.PCIAddress, "timeout", c.timeout)
		} else if ctl.CollectError != "" {
			log.Warn("read failed", "pci", ctl.PCIAddress, "err", ctl.CollectError)
		}
	}
	return s, true
}
