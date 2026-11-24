// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

// Package exporter assemble l'instantané publié par /metrics à partir de
// l'inventaire sysfs et des lectures du transport. Il ne fait aucun appel
// système direct.
package exporter

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/DisasteR/hwraid_exporter/internal/module"
	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
)

// Controller est une carte inventoriée.
type Controller struct {
	Label          string // valeur du label controller
	IdentitySource string // serial ou pci
	PCIAddress     string
	Driver         string
	Backend        string // module de collecte, vide si aucun
	Model          string
	Firmware       string
	// NodeChecked indique qu'une vérification du nœud a été tentée ;
	// NodeValid en donne le résultat.
	NodeChecked, NodeValid bool
	TimedOut               bool // collecte hors délai pendant ce cycle
	NodeError              string
	Unsupported            []string // collecteurs non pris en charge par la carte
	CollectError           string   // échec d'une lecture firmware
	LateError              string   // erreurs d'une collecte tardive non publiée
	Collected              map[string]bool
	LastSuccess            map[string]time.Time // collecteur → date du dernier succès
	CloseUnconfirmed       uint64               // clôtures de transfert non nominales cumulées
	CacheProtection        *module.CacheProtection
	Volumes                []module.Volume
	Disks                  []module.Disk
	DiskInventory          []module.DiskInfo
	DiskLocations          []module.DiskLocation
	DiskOSDevices          []module.DiskOSDevice
}

// Snapshot est une vue complète et immuable.
type Snapshot struct {
	Ready            bool
	DiscoverySuccess bool
	Controllers      []Controller
	// Interval est l'intervalle de collecte effectif.
	Interval time.Duration
}

// Version est la version de l'exporter, fixée au démarrage
// (hwraid_exporter_build_info).
var Version = "dev"

// Revision est le commit de la compilation, vide s'il est inconnu.
var Revision string

// Build construit l'instantané d'un cycle à partir du plan (cartes et
// modules) et des résultats de collecte, indexés par adresse PCI. Une carte
// RAID sans module est inventoriée avec backend_supported à 0. tracker peut
// être nil (ni dates de dernier succès ni mémoire d'identité).
func Build(p module.Plan, discoveryOK bool, results map[string]module.Result, tracker *Tracker, interval time.Duration) Snapshot {
	serials := map[string]int{}
	for _, pc := range p.Cards {
		if s := firstAttr(pc.Card, func(h sysfs.SCSIHost) string { return h.SerialNumber }); s != "" {
			serials[s]++
		}
	}
	var ctrls []Controller
	for _, pc := range p.Cards {
		addr := pc.Card.PCI.Address
		c := Controller{
			PCIAddress: addr, Driver: pc.Card.PCI.Driver,
			Model:    firstAttr(pc.Card, func(h sysfs.SCSIHost) string { return h.Model }),
			Firmware: firstAttr(pc.Card, func(h sysfs.SCSIHost) string { return h.Firmware }),
		}
		// Une série n'identifie une carte que si elle est non vide et unique.
		s := firstAttr(pc.Card, func(h sysfs.SCSIHost) string { return h.SerialNumber })
		switch known := tracker.serialLabel(addr); {
		case s != "" && serials[s] == 1:
			c.Label, c.IdentitySource = "serial:"+s, "serial"
		case s == "" && known != "" && serials[strings.TrimPrefix(known, "serial:")] == 0:
			// Numéro de série illisible ce cycle : identité établie conservée,
			// sauf si une autre carte présente ce numéro.
			c.Label, c.IdentitySource = known, "serial"
		default:
			c.Label, c.IdentitySource = "pci:"+addr, "pci"
		}
		if pc.Module != nil {
			c.Backend = pc.Module.Name()
			c.CloseUnconfirmed = tracker.Closes(addr)
			c.NodeChecked = true
			if lr, ok := results[addr]; ok && lr.LateErr != nil {
				c.LateError = lr.LateErr.Error()
			}
			switch r, ok := results[addr]; {
			case pc.Err != nil:
				c.NodeError = pc.Err.Error()
			case !ok:
				c.NodeError = "no collection for this card"
			case r.TimedOut:
				// Nœud non observé : management_node_valid n'est pas publié.
				c.NodeChecked, c.TimedOut = false, true
				// Les collecteurs servis par une réponse gardée en mémoire
				// (capabilities) restent valides : pas d'échec publié pour eux.
				// Les collecteurs connus comme non pris en charge le restent
				// (même carte, même firmware) : ni échec ni succès publié.
				c.Unsupported = tracker.Unsupported(addr, identity(c))
				skip := map[string]bool{}
				for _, col := range c.Unsupported {
					skip[col] = true
				}
				c.Collected = map[string]bool{}
				for col, ok := range r.Collected {
					if !skip[col] {
						c.Collected[col] = ok || tracker.Held(addr, identity(c))[col]
					}
				}
				c.LastSuccess = tracker.Last(addr)
				c.CollectError = "collection timed out"
			case r.Err != nil:
				c.NodeError = r.Err.Error()
			default:
				c.NodeValid = r.NodeValid
				c.Unsupported = r.Unsupported
				c.Collected = r.Collected
				c.CacheProtection = r.CacheProtection
				c.LastSuccess = tracker.Last(addr)
				c.Volumes, c.Disks, c.DiskInventory, c.DiskLocations, c.DiskOSDevices = r.Volumes, r.Disks, r.DiskInventory, r.DiskLocations, r.DiskOSDevices
				if r.CollectErr != nil {
					c.CollectError = r.CollectErr.Error()
				}
			}
		}
		ctrls = append(ctrls, c)
	}
	// Un label serial: ne désigne qu'une carte : si plusieurs le portent
	// (numéro lu et numéro mémorisé), toutes repassent en pci:.
	count := map[string]int{}
	for _, c := range ctrls {
		count[c.Label]++
	}
	for i, c := range ctrls {
		if c.IdentitySource == "serial" && count[c.Label] > 1 {
			ctrls[i].Label, ctrls[i].IdentitySource = "pci:"+c.PCIAddress, "pci"
		}
	}
	return Snapshot{Ready: true, DiscoverySuccess: discoveryOK, Controllers: ctrls, Interval: interval}
}

func firstAttr(c module.Card, get func(sysfs.SCSIHost) string) string {
	for _, h := range c.Hosts {
		if v := get(h); v != "" {
			return v
		}
	}
	return ""
}

// Render produit le format d'exposition texte de Prometheus.
func Render(s Snapshot) string {
	var b strings.Builder
	gauge := func(name, help string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
	}
	gauge("hwraid_exporter_build_info", "Exporter version, source revision and Go version, value 1.")
	fmt.Fprintf(&b, "hwraid_exporter_build_info{%s} 1\n", labels("version", Version, "revision", Revision, "goversion", runtime.Version()))
	gauge("hwraid_snapshot_ready", "1 when the view holds the latest collection; 0, with no hardware value, before the first one, while stopping, and when the collection loop has not published for too long.")
	fmt.Fprintf(&b, "hwraid_snapshot_ready %d\n", b2i(s.Ready))
	if !s.Ready {
		return b.String()
	}
	gauge("hwraid_discovery_success", "1 if the last discovery succeeded for every SCSI host.")
	fmt.Fprintf(&b, "hwraid_discovery_success %d\n", b2i(s.DiscoverySuccess))

	gauge("hwraid_controller_info", "RAID controller inventory, value 1.")
	for _, c := range s.Controllers {
		fmt.Fprintf(&b, "hwraid_controller_info{%s} 1\n", labels(
			"controller", c.Label, "identity_source", c.IdentitySource, "pci_address", c.PCIAddress,
			"driver", c.Driver, "backend", c.Backend, "model", c.Model, "firmware", c.Firmware))
	}
	gauge("hwraid_controller_backend_supported", "1 if a module can collect this card.")
	for _, c := range s.Controllers {
		fmt.Fprintf(&b, "hwraid_controller_backend_supported{%s} %d\n", labels("controller", c.Label), b2i(c.Backend != ""))
	}
	gauge("hwraid_management_node_valid", "1 if the dedicated management node matches the card, 0 otherwise.")
	for _, c := range s.Controllers {
		if c.NodeChecked {
			fmt.Fprintf(&b, "hwraid_management_node_valid{%s} %d\n", labels("controller", c.Label), b2i(c.NodeValid))
		}
	}
	gauge("hwraid_collector_supported", "0 for a collector the card does not support.")
	for _, c := range s.Controllers {
		for _, col := range c.Unsupported {
			fmt.Fprintf(&b, "hwraid_collector_supported{%s} 0\n", labels("controller", c.Label, "collector", col))
		}
	}
	gauge("hwraid_volume_info", "Volumes (containers) of the card, value 1.")
	for _, c := range s.Controllers {
		for _, v := range c.Volumes {
			fmt.Fprintf(&b, "hwraid_volume_info{%s} 1\n", labels("controller", c.Label, "volume", v.ID, "name", v.Name, "raid_level", v.RaidLevel, "vendor_level", v.VendorLevel, "parent", v.Parent))
		}
	}
	gauge("hwraid_volume_state", "Volume state, value 1.")
	for _, c := range s.Controllers {
		for _, v := range c.Volumes {
			fmt.Fprintf(&b, "hwraid_volume_state{%s} 1\n", labels("controller", c.Label, "volume", v.ID, "state", v.State))
		}
	}
	gauge("hwraid_volume_capacity_bytes", "Volume size in bytes.")
	for _, c := range s.Controllers {
		for _, v := range c.Volumes {
			fmt.Fprintf(&b, "hwraid_volume_capacity_bytes{%s} %d\n", labels("controller", c.Label, "volume", v.ID), v.CapacityBytes)
		}
	}
	gauge("hwraid_volume_write_cache_policy", "Configured write cache policy of the volume, value 1.")
	for _, c := range s.Controllers {
		for _, v := range c.Volumes {
			if v.WriteCachePolicy != "" {
				fmt.Fprintf(&b, "hwraid_volume_write_cache_policy{%s} 1\n", labels("controller", c.Label, "volume", v.ID, "policy", v.WriteCachePolicy, "vendor_policy", v.VendorWriteCachePolicy))
			}
		}
	}
	gauge("hwraid_volume_write_cache_enabled", "1 if the volume's write cache is currently active (write-back), 0 if not.")
	for _, c := range s.Controllers {
		for _, v := range c.Volumes {
			if v.WriteCacheEnabled != nil {
				fmt.Fprintf(&b, "hwraid_volume_write_cache_enabled{%s} %d\n", labels("controller", c.Label, "volume", v.ID), b2i(*v.WriteCacheEnabled))
			}
		}
	}
	gauge("hwraid_volume_member", "Members of each volume (disk or volume), value 1.")
	for _, c := range s.Controllers {
		for _, v := range c.Volumes {
			for _, m := range v.Members {
				fmt.Fprintf(&b, "hwraid_volume_member{%s} 1\n", labels("controller", c.Label, "volume", v.ID, "member", m))
			}
		}
	}
	gauge("hwraid_disk_state", "Member disk state, value 1.")
	for _, c := range s.Controllers {
		for _, d := range c.Disks {
			fmt.Fprintf(&b, "hwraid_disk_state{%s} 1\n", labels("controller", c.Label, "disk", d.ID, "state", d.State))
		}
	}
	gauge("hwraid_disk_info", "Disks present on the card, value 1.")
	for _, c := range s.Controllers {
		for _, d := range c.DiskInventory {
			fmt.Fprintf(&b, "hwraid_disk_info{%s} 1\n", labels("controller", c.Label, "disk", d.ID, "vendor", d.Vendor,
				"model", d.Model, "firmware", d.Firmware, "serial", d.Serial, "wwn", d.WWN))
		}
	}
	gauge("hwraid_disk_location", "Location of disks attached directly to the card, value 1.")
	for _, c := range s.Controllers {
		for _, d := range c.DiskLocations {
			fmt.Fprintf(&b, "hwraid_disk_location{%s} 1\n", labels("controller", c.Label, "disk", d.ID, "connector", d.Connector,
				"connector_name", d.ConnectorName, "device", d.Device, "phy", d.Phy))
		}
	}
	gauge("hwraid_disk_os_device", "Linux SCSI device exposing the disk, value 1.")
	for _, c := range s.Controllers {
		for _, d := range c.DiskOSDevices {
			fmt.Fprintf(&b, "hwraid_disk_os_device{%s} 1\n", labels("controller", c.Label, "disk", d.ID, "device", d.Device, "scsi_address", d.Address))
		}
	}
	gauge("hwraid_disk_capacity_bytes", "Raw disk capacity in bytes.")
	for _, c := range s.Controllers {
		for _, d := range c.DiskInventory {
			if d.CapacityBytes > 0 {
				fmt.Fprintf(&b, "hwraid_disk_capacity_bytes{%s} %d\n", labels("controller", c.Label, "disk", d.ID), d.CapacityBytes)
			}
		}
	}
	gauge("hwraid_cache_protection_present", "1 if a cache protection module is installed.")
	for _, c := range s.Controllers {
		if cp := c.CacheProtection; cp != nil && !cp.PresenceUnknown {
			fmt.Fprintf(&b, "hwraid_cache_protection_present{%s} %d\n", labels("controller", c.Label, "type", cp.Type, "vendor_type", cp.Vendor), b2i(cp.Present))
		}
	}
	gauge("hwraid_cache_protection_state", "Cache protection module state, value 1.")
	for _, c := range s.Controllers {
		if cp := c.CacheProtection; cp != nil && (cp.Present || cp.PresenceUnknown) {
			fmt.Fprintf(&b, "hwraid_cache_protection_state{%s} 1\n", labels("controller", c.Label, "state", cp.State))
		}
	}
	gauge("hwraid_collection_interval_seconds", "Effective collection interval for the card.")
	for _, c := range s.Controllers {
		if c.Backend != "" && s.Interval > 0 {
			fmt.Fprintf(&b, "hwraid_collection_interval_seconds{%s} %g\n", labels("controller", c.Label), s.Interval.Seconds())
		}
	}
	gauge("hwraid_collection_last_success_timestamp_seconds", "Unix time of the collector's last success.")
	for _, c := range s.Controllers {
		names := make([]string, 0, len(c.LastSuccess))
		for n := range c.LastSuccess {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(&b, "hwraid_collection_last_success_timestamp_seconds{%s} %d\n", labels("controller", c.Label, "collector", n), c.LastSuccess[n].Unix())
		}
	}
	fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n", "hwraid_transfer_close_unconfirmed_total",
		"Paged transfers whose close was acknowledged with a non-nominal mark (aacraid: 0xc5).", "hwraid_transfer_close_unconfirmed_total")
	for _, c := range s.Controllers {
		if c.Backend != "" {
			fmt.Fprintf(&b, "hwraid_transfer_close_unconfirmed_total{%s} %d\n", labels("controller", c.Label), c.CloseUnconfirmed)
		}
	}
	gauge("hwraid_collection_success", "1 if the collector's last read succeeded, 0 otherwise.")
	for _, c := range s.Controllers {
		names := make([]string, 0, len(c.Collected))
		for n := range c.Collected {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(&b, "hwraid_collection_success{%s} %d\n", labels("controller", c.Label, "collector", n), b2i(c.Collected[n]))
		}
	}
	return b.String()
}

// labels formate des paires clé/valeur avec l'échappement exigé par le
// format d'exposition (barre oblique inverse, guillemet, saut de ligne).
func labels(kv ...string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	parts := make([]string, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, kv[i]+`="`+r.Replace(kv[i+1])+`"`)
	}
	return strings.Join(parts, ",")
}

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}
