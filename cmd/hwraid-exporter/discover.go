// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/DisasteR/hwraid_exporter/internal/aacraid"
	"github.com/DisasteR/hwraid_exporter/internal/module"
	"github.com/DisasteR/hwraid_exporter/internal/modules/aacmod"
	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
)

// Statut de lecture d'une carte de l'inventaire (discover).
const (
	statusRead         = "read"                    // modèle qualifié, ou module qui ne qualifie pas ses cartes
	statusReadAllowed  = "read_allowed"            // modèle non qualifié, lu grâce à -allow-unqualified
	statusNeedsAllow   = "needs_allow_unqualified" // modèle non qualifié : aucune commande sans l'option
	statusNoModule     = "no_module"               // carte RAID qu'aucun module ne prend en charge
	statusPlanningFail = "error"                   // ambiguïté entre modules ou déclaration de nœud invalide
)

// discoveredCard est une carte de l'inventaire telle que serve la voit.
type discoveredCard struct {
	PCIAddress       string   `json:"pci_address"`
	Driver           string   `json:"driver"`
	Module           string   `json:"module,omitempty"`
	Model            string   `json:"model,omitempty"`
	Firmware         string   `json:"firmware,omitempty"`
	Serial           string   `json:"serial,omitempty"`
	SerialUnreadable bool     `json:"serial_unreadable,omitempty"`
	Hosts            []string `json:"hosts"`
	Status           string   `json:"status"`
	Error            string   `json:"error,omitempty"`
}

// cardStatus rend le statut de lecture d'une carte planifiée, d'après le
// verrou que le module applique lui-même avant toute commande (Reads).
func cardStatus(pc module.Planned) string {
	switch q, qualifies := pc.Module.(module.Qualifier); {
	case pc.Err != nil:
		return statusPlanningFail
	case pc.Module == nil:
		return statusNoModule
	case !qualifies, q.Qualified(pc.Card):
		return statusRead
	case q.Reads(pc.Card):
		return statusReadAllowed
	default:
		return statusNeedsAllow
	}
}

// discover affiche, en JSON, les cartes de l'inventaire telles que serve
// les voit : même plan (cartes RAID et cartes revendiquées par un module),
// mêmes autorisations (-allow-unqualified), même unicité des numéros de
// série. Ne lit que sysfs : ni privilège, ni nœud, ni commande à la carte.
// Code de sortie : 0, ou 1 si sysfs est illisible, 2 pour une option
// invalide ; une découverte partielle est signalée dans "errors".
func discover(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("discover", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("sysfs", "/sys", "sysfs root")
	var allow []string
	fs.Func("allow-unqualified", "as for serve: show the cards it would read (a module name, or serial:<number>)", func(v string) error {
		allow = append(allow, v)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "unexpected argument: options only")
		return 2
	}
	list, err := aacraid.NewAllowlist()
	if err != nil {
		fmt.Fprintln(stderr, "invalid allowlist:", err)
		return 1
	}
	registry := []module.Module{aacmod.New(list)}
	if _, _, err := allowUnqualified(allow, registry); err != nil {
		fmt.Fprintln(stderr, "invalid -allow-unqualified:", err)
		return 2
	}
	hosts, herrs, err := sysfs.Discover(*root)
	if err != nil {
		fmt.Fprintln(stderr, "discovery failed:", err)
		return 1
	}
	plan := buildPlan(hosts, herrs, nil, registry)
	out := struct {
		Cards  []discoveredCard `json:"cards"`
		Errors []string         `json:"errors,omitempty"`
	}{Cards: []discoveredCard{}}
	for _, pc := range plan.Cards {
		fields, _ := pc.Card.Identity()
		c := discoveredCard{PCIAddress: pc.Card.PCI.Address, Driver: pc.Card.PCI.Driver,
			Serial: fields[0], Model: fields[5], Firmware: fields[6], Hosts: []string{}, Status: cardStatus(pc)}
		if pc.Module != nil {
			c.Module = pc.Module.Name()
		}
		if pc.Err != nil {
			c.Error = pc.Err.Error()
		}
		for _, h := range pc.Card.Hosts {
			c.Hosts = append(c.Hosts, h.Name)
			c.SerialUnreadable = c.SerialUnreadable || h.SerialUnreadable
		}
		out.Cards = append(out.Cards, c)
	}
	for _, e := range herrs {
		out.Errors = append(out.Errors, e.Error())
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
