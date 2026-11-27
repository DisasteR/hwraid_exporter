// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// RenderText rend un instantané en texte lisible (oneshot -format text) :
// par carte, son identité et sa collecte, puis ses volumes en arbre (un
// assemblage, ses volumes membres, leurs disques), ses disques et ses
// tâches de fond. Mêmes données et même vocabulaire normalisé que les
// métriques (docs/metrics.md), quel que soit le pilote ; le terme de
// l'outil du constructeur (vendor_level, vendor_state, réglage du cache)
// suit entre parenthèses quand il est publié et diffère. Les identifiants (label controller, volume,
// d<N>) sont ceux des labels des métriques.
func RenderText(s Snapshot) string {
	var b strings.Builder
	if len(s.Controllers) == 0 {
		b.WriteString("No RAID controller found.\n")
	}
	if !s.DiscoverySuccess {
		b.WriteString("Warning: discovery incomplete; a card may be missing.\n")
	}
	for i, c := range s.Controllers {
		if i > 0 || !s.DiscoverySuccess {
			b.WriteString("\n")
		}
		textController(&b, c)
	}
	return b.String()
}

func textController(b *strings.Builder, c Controller) {
	fmt.Fprintf(b, "Controller %s\n", c.Label)
	fmt.Fprintf(b, "  PCI address       %s (driver %s)\n", c.PCIAddress, orNone(c.Driver))
	id := strings.TrimSpace(strings.Join([]string{c.Model, firmware(c.Firmware)}, "  "))
	fmt.Fprintf(b, "  Model             %s\n", orNone(id))
	switch {
	case c.Backend == "":
		fmt.Fprintf(b, "  Module            none: not monitored by this exporter\n")
		return
	case c.Qualified == nil:
		fmt.Fprintf(b, "  Module            %s\n", c.Backend)
	case *c.Qualified:
		fmt.Fprintf(b, "  Module            %s, qualified model\n", c.Backend)
	default:
		fmt.Fprintf(b, "  Module            %s, model not qualified\n", c.Backend)
	}
	fmt.Fprintf(b, "  Collection        %s\n", textCollection(c))
	if c.Temperature != nil {
		t := c.Temperature
		fmt.Fprintf(b, "  Temperature       %g °C (thresholds %g / %g °C)\n", t.Celsius, t.ThresholdLo, t.ThresholdHi)
	}
	if c.Alarm != "" {
		fmt.Fprintf(b, "  Alarm             %s\n", c.Alarm)
	}
	if cp := c.CacheProtection; cp != nil {
		switch {
		case cp.PresenceUnknown:
			fmt.Fprintf(b, "  Cache protection  %s (%s), presence unknown, %s\n", cp.Type, cp.Vendor, cp.State)
		case !cp.Present:
			fmt.Fprintf(b, "  Cache protection  none\n")
		default:
			fmt.Fprintf(b, "  Cache protection  %s (%s), %s\n", cp.Type, cp.Vendor, cp.State)
		}
	}
	textVolumes(b, c)
	textDisks(b, c)
	// Tâches : « none » seulement si leur lecture a réussi ; en échec, la
	// section le dit ; non prise en charge, elle est absente (la ligne
	// Collection le dit).
	ok, tried := c.Collected["tasks"]
	switch {
	case len(c.Tasks) > 0, ok:
		b.WriteString("  Background tasks\n")
		if len(c.Tasks) == 0 {
			b.WriteString("    none\n")
		}
	case tried:
		b.WriteString("  Background tasks\n    not known: read failed\n")
	}
	if len(c.Tasks) > 0 {
		for _, t := range c.Tasks {
			fmt.Fprintf(b, "    %s  %s  %.0f %%\n", t.Volume, t.Task, t.Ratio*100)
		}
	}
}

// textCollection résume la collecte de la carte : nœud, délai, lectures en
// échec, collecteurs non pris en charge.
func textCollection(c Controller) string {
	switch {
	case c.TimedOut:
		return "failed: timed out"
	case c.NodeChecked && !c.NodeValid:
		return "failed: invalid management node"
	case !c.NodeChecked:
		return "not done: management node not prepared"
	}
	var failed []string
	for col, ok := range c.Collected {
		if !ok {
			failed = append(failed, col)
		}
	}
	sort.Strings(failed)
	out := "ok"
	if len(failed) > 0 {
		out = "failed: " + strings.Join(failed, ", ")
	}
	if len(c.Unsupported) > 0 {
		uns := slices.Sorted(slices.Values(c.Unsupported))
		out += "; not supported: " + strings.Join(uns, ", ")
	}
	return out
}

func textVolumes(b *strings.Builder, c Controller) {
	if len(c.Volumes) == 0 {
		return
	}
	b.WriteString("  Volumes\n")
	byID := map[string]int{}
	for i, v := range c.Volumes {
		byID[v.ID] = i
	}
	disks := map[string]string{}
	for _, d := range c.Disks {
		disks[d.ID] = withVendor(d.State, d.VendorState)
	}
	shown := map[string]bool{}
	var walk func(i int, depth int)
	walk = func(i int, depth int) {
		v := c.Volumes[i]
		if shown[v.ID] {
			return // boucle : déjà affiché plus haut dans l'arbre
		}
		shown[v.ID] = true
		pad := strings.Repeat("  ", depth+2)
		line := []string{v.ID}
		if v.Name != "" {
			line = append(line, v.Name)
		}
		line = append(line, withVendor(v.RaidLevel, v.VendorLevel), v.State)
		if v.CapacityBytes > 0 {
			line = append(line, bytesText(v.CapacityBytes))
		}
		fmt.Fprintf(b, "%s%s\n", pad, strings.Join(line, "  "))
		if v.WriteCachePolicy != "" || v.WriteCacheEnabled != nil {
			wc := withVendor(orNone(v.WriteCachePolicy), v.VendorWriteCachePolicy)
			if v.WriteCacheEnabled != nil {
				wc += map[bool]string{true: ", active", false: ", inactive"}[*v.WriteCacheEnabled]
			}
			fmt.Fprintf(b, "%s  write cache %s\n", pad, wc)
		}
		for _, m := range v.Members {
			if j, ok := byID[m]; ok {
				walk(j, depth+1)
				continue
			}
			state, ok := disks[m]
			if !ok {
				state = "no state read"
			}
			fmt.Fprintf(b, "%s  %s  %s\n", pad, m, state)
		}
	}
	for i, v := range c.Volumes { // racines d'abord
		if v.Parent == "" {
			walk(i, 0)
		}
	}
	for i := range c.Volumes { // puis tout volume non encore affiché (parent absent, boucle)
		walk(i, 0)
	}
}

func textDisks(b *strings.Builder, c Controller) {
	if len(c.Disks) == 0 && len(c.DiskInventory) == 0 {
		return
	}
	b.WriteString("  Disks\n")
	info := map[string]int{}
	for i, d := range c.DiskInventory {
		info[d.ID] = i
	}
	loc := map[string]int{}
	for i, d := range c.DiskLocations {
		loc[d.ID] = i
	}
	osdev := map[string]int{}
	for i, d := range c.DiskOSDevices {
		osdev[d.ID] = i
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, d := range c.Disks {
		ids, seen[d.ID] = append(ids, d.ID), true
	}
	for _, d := range c.DiskInventory {
		if !seen[d.ID] {
			ids, seen[d.ID] = append(ids, d.ID), true
		}
	}
	state := map[string]string{}
	for _, d := range c.Disks {
		state[d.ID] = withVendor(d.State, d.VendorState)
	}
	for _, id := range ids {
		line := []string{id, orValue(state[id], "no state read")}
		if i, ok := info[id]; ok {
			d := c.DiskInventory[i]
			if m := strings.TrimSpace(d.Vendor + " " + d.Model); m != "" {
				line = append(line, m)
			}
			if d.CapacityBytes > 0 {
				line = append(line, bytesText(d.CapacityBytes))
			}
			if d.Serial != "" {
				line = append(line, "serial "+d.Serial)
			}
		}
		if i, ok := loc[id]; ok {
			l := c.DiskLocations[i]
			name := l.Connector
			if l.ConnectorName != "" {
				name = l.ConnectorName
			}
			line = append(line, fmt.Sprintf("connector %s device %s", name, l.Device))
		}
		if i, ok := osdev[id]; ok {
			o := c.DiskOSDevices[i]
			line = append(line, fmt.Sprintf("%s (%s)", o.Device, o.Address))
		}
		fmt.Fprintf(b, "    %s\n", strings.Join(line, "  "))
	}
}

// bytesText écrit une taille en unités décimales (Go, To), comme les
// capacités annoncées des disques.
func bytesText(n uint64) string {
	units := []string{"B", "kB", "MB", "GB", "TB", "PB"}
	v, u := float64(n), 0
	for v >= 1000 && u < len(units)-1 {
		v, u = v/1000, u+1
	}
	if u == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", v, units[u])
}

// withVendor précise une valeur normalisée par le terme du constructeur,
// entre parenthèses, s'il est publié et diffère.
func withVendor(normalized, vendor string) string {
	if vendor == "" || vendor == normalized {
		return normalized
	}
	return normalized + " (" + vendor + ")"
}

func firmware(f string) string {
	if f == "" {
		return ""
	}
	return "firmware " + f
}

func orNone(s string) string { return orValue(s, "none") }

func orValue(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
