// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"strings"
	"testing"

	"github.com/DisasteR/hwraid_exporter/internal/module"
)

// Topologie de l'hôte de référence (identifiants fictifs) : un assemblage de
// quatre miroirs, huit disques, ZMM en panne, une reconstruction en cours
// (construite), et une carte RAID sans module. Le texte est comparé mot pour
// mot : chaque champ publié, l'arbre des volumes (membres conteneurs
// imbriqués, membres disques avec leur état) et le résumé de collecte.
func TestRenderTextReferenceTopology(t *testing.T) {
	yes, no := true, false
	ref := Controller{
		Label: "serial:EXAMPLE6805A01", PCIAddress: "0000:01:00.0", Driver: "aacraid", Backend: "aacraid", Qualified: &yes,
		Model: "6805", Firmware: "5.3-0[19204]", NodeChecked: true, NodeValid: true,
		Collected:       map[string]bool{"volumes": true, "disks": true, "tasks": true, "temperature": true},
		Temperature:     &module.Temperature{Celsius: 52, ThresholdLo: 95, ThresholdHi: 100},
		Alarm:           module.AlarmDisabled,
		CacheProtection: &module.CacheProtection{Present: true, Type: "flash_supercap", Vendor: "zmm", State: "failed"},
		Volumes: []module.Volume{
			{ID: "1d000000", Name: "Array0", RaidLevel: "concat", VendorLevel: "RAID_volume", State: "degraded_rebuilding", CapacityBytes: 15_998_000_000_000,
				Members: []string{"1d000001", "1d000002"}, WriteCachePolicy: "write_back_when_protected", VendorWriteCachePolicy: "wbb", WriteCacheEnabled: &no},
			{ID: "1d000001", Name: "Array0", RaidLevel: "raid1", VendorLevel: "1", State: "degraded_rebuilding", Parent: "1d000000", Members: []string{"d0", "d1"}},
			{ID: "1d000002", Name: "Array0", RaidLevel: "raid1", State: "optimal", Parent: "1d000000", Members: []string{"d2", "d9"}},
		},
		Disks: []module.Disk{{ID: "d0", State: "online", VendorState: "Online"}, {ID: "d1", State: "rebuilding", VendorState: "Rebuilding"}, {ID: "d2", State: "online", VendorState: "online"}},
		DiskInventory: []module.DiskInfo{{ID: "d0", Vendor: "HGST", Model: "HUS724040ALS640", Serial: "EXAMPLEDISK0", CapacityBytes: 4_000_787_030_016, LinkSpeedBps: 6_000_000_000},
			{ID: "d3", Model: "SPARE"}},
		DiskLocations: []module.DiskLocation{{ID: "d0", Connector: "0", ConnectorName: "CN0", Device: "0"}, {ID: "d1", Connector: "0", Device: "1"}},
		DiskOSDevices: []module.DiskOSDevice{{ID: "d0", Device: "sg1", Address: "0:1:0:0"}},
		Tasks:         []module.Task{{Volume: "1d000001", Task: "rebuild", Ratio: 0.426}},
	}
	other := Controller{Label: "pci:0000:03:00.0", PCIAddress: "0000:03:00.0", Driver: "megaraid_sas"}
	got := RenderText(Snapshot{Ready: true, DiscoverySuccess: true, Controllers: []Controller{ref, other}})
	want := `Controller serial:EXAMPLE6805A01
  PCI address       0000:01:00.0 (driver aacraid)
  Model             6805  firmware 5.3-0[19204]
  Module            aacraid, qualified model
  Collection        ok
  Temperature       52 °C (thresholds 95 / 100 °C)
  Alarm             disabled
  Cache protection  flash_supercap (zmm), failed
  Volumes
    1d000000  Array0  concat (RAID_volume)  degraded_rebuilding  16.0 TB
      write cache write_back_when_protected (wbb), inactive
      1d000001  Array0  raid1 (1)  degraded_rebuilding
        d0  online (Online)
        d1  rebuilding (Rebuilding)
      1d000002  Array0  raid1  optimal
        d2  online
        d9  no state read
  Disks
    d0  online (Online)  HGST HUS724040ALS640  4.0 TB  link 6 Gb/s  serial EXAMPLEDISK0  connector CN0 device 0  sg1 (0:1:0:0)
    d1  rebuilding (Rebuilding)  connector 0 device 1
    d2  online
    d3  no state read  SPARE
  Background tasks
    1d000001  rebuild  43 %

Controller pci:0000:03:00.0
  PCI address       0000:03:00.0 (driver megaraid_sas)
  Model             none
  Module            none: not monitored by this exporter
`
	if got != want {
		t.Fatalf("texte :\n%s\nattendu :\n%s", got, want)
	}
}

// Résumé de collecte et cas limites : modèle non qualifié lu ou non,
// lecture en échec, nœud invalide ou non préparé, hors délai, protection du
// cache absente ou de présence inconnue, aucune carte, découverte partielle,
// volumes qui se désignent mutuellement (chacun affiché une fois).
func TestRenderTextCases(t *testing.T) {
	yes, no := true, false
	base := func() Controller {
		return Controller{Label: "pci:0000:01:00.0", PCIAddress: "0000:01:00.0", Driver: "aacraid", Backend: "aacraid", Qualified: &no,
			NodeChecked: true, NodeValid: true}
	}
	for name, tc := range map[string]struct {
		c    Controller
		want []string
	}{
		"non qualifié, non lu": {func() Controller { c := base(); c.Unsupported = []string{"volumes", "disks"}; return c }(),
			[]string{"aacraid, model not qualified", "Collection        ok; not supported: disks, volumes"}},
		"lecture en échec": {func() Controller {
			c := base()
			c.Qualified = &yes
			c.Collected = map[string]bool{"volumes": false, "disks": true, "tasks": false}
			return c
		}(), []string{"Collection        failed: tasks, volumes"}},
		"nœud invalide":    {func() Controller { c := base(); c.NodeValid = false; return c }(), []string{"failed: invalid management node"}},
		"nœud non préparé": {func() Controller { c := base(); c.NodeChecked, c.NodeValid = false, false; return c }(), []string{"not done: management node not prepared"}},
		"hors délai":       {func() Controller { c := base(); c.TimedOut = true; return c }(), []string{"failed: timed out"}},
		"sans protection": {func() Controller { c := base(); c.CacheProtection = &module.CacheProtection{}; return c }(),
			[]string{"Cache protection  none"}},
		"présence inconnue": {func() Controller {
			c := base()
			c.CacheProtection = &module.CacheProtection{PresenceUnknown: true, Type: "battery", Vendor: "bbu", State: "unknown"}
			return c
		}(), []string{"battery (bbu), presence unknown, unknown"}},
		"boucle": {func() Controller {
			c := base()
			c.Volumes = []module.Volume{{ID: "a", RaidLevel: "unknown", State: "unknown", Parent: "b", Members: []string{"b"}},
				{ID: "b", RaidLevel: "unknown", State: "unknown", Parent: "a", Members: []string{"a"}}}
			return c
		}(), []string{"    a  unknown  unknown\n      b  unknown  unknown\n"}},
		"tâches en échec": {func() Controller {
			c := base()
			c.Collected = map[string]bool{"tasks": false, "volumes": true}
			return c
		}(), []string{"  Background tasks\n    not known: read failed\n"}},
		"tâches lues, aucune": {func() Controller { c := base(); c.Collected = map[string]bool{"tasks": true}; return c }(),
			[]string{"  Background tasks\n    none\n"}},
		"hors délai réel (nœud non vérifié)": {func() Controller { c := base(); c.NodeChecked, c.NodeValid, c.TimedOut = false, false, true; return c }(),
			[]string{"Collection        failed: timed out"}},
	} {
		got := RenderText(Snapshot{DiscoverySuccess: true, Controllers: []Controller{tc.c}})
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s : %q absent de\n%s", name, w, got)
			}
		}
	}
	uns := base()
	uns.Unsupported = []string{"tasks"}
	if got := RenderText(Snapshot{DiscoverySuccess: true, Controllers: []Controller{uns}}); strings.Contains(got, "Background tasks") {
		t.Fatalf("tâches non prises en charge : section présente\n%s", got)
	}
	if got := RenderText(Snapshot{DiscoverySuccess: true}); got != "No RAID controller found.\n" {
		t.Fatalf("aucune carte : %q", got)
	}
	if got := RenderText(Snapshot{}); got != "No RAID controller found.\nWarning: discovery incomplete; a card may be missing.\n" {
		t.Fatalf("découverte partielle : %q", got)
	}
}
