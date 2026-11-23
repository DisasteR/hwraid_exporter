// Package exporter assemble l'instantané publié par /metrics à partir de
// l'inventaire sysfs et des lectures du transport. Il ne fait aucun appel
// système direct.
package exporter

import (
	"fmt"
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
	Collected              map[string]bool
	LastSuccess            map[string]time.Time // collecteur → date du dernier succès
	CacheProtection        *module.CacheProtection
	Volumes                []module.Volume
	Disks                  []module.Disk
	DiskInventory          []module.DiskInfo
	DiskLocations          []module.DiskLocation
}

// Snapshot est une vue complète et immuable.
type Snapshot struct {
	Ready            bool
	DiscoverySuccess bool
	Controllers      []Controller
	// Interval est l'intervalle de collecte effectif.
	Interval time.Duration
}

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
			c.NodeChecked = true
			switch r, ok := results[addr]; {
			case pc.Err != nil:
				c.NodeError = pc.Err.Error()
			case !ok:
				c.NodeError = "aucune collecte pour cette carte"
			case r.TimedOut:
				// Nœud non observé : management_node_valid n'est pas publié.
				c.NodeChecked, c.TimedOut = false, true
				c.Collected = r.Collected
				c.LastSuccess = tracker.Last(addr)
				c.CollectError = "collecte hors délai"
			case r.Err != nil:
				c.NodeError = r.Err.Error()
			default:
				c.NodeValid = r.NodeValid
				c.Unsupported = r.Unsupported
				c.Collected = r.Collected
				c.CacheProtection = r.CacheProtection
				c.LastSuccess = tracker.Last(addr)
				c.Volumes, c.Disks, c.DiskInventory, c.DiskLocations = r.Volumes, r.Disks, r.DiskInventory, r.DiskLocations
				if r.CollectErr != nil {
					c.CollectError = r.CollectErr.Error()
				}
			}
		}
		ctrls = append(ctrls, c)
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
	gauge("hwraid_snapshot_ready", "1 une fois la première découverte publiée.")
	fmt.Fprintf(&b, "hwraid_snapshot_ready %d\n", b2i(s.Ready))
	if !s.Ready {
		return b.String()
	}
	gauge("hwraid_discovery_success", "1 si la dernière découverte a abouti.")
	fmt.Fprintf(&b, "hwraid_discovery_success %d\n", b2i(s.DiscoverySuccess))

	gauge("hwraid_controller_info", "Inventaire des contrôleurs RAID.")
	for _, c := range s.Controllers {
		fmt.Fprintf(&b, "hwraid_controller_info{%s} 1\n", labels(
			"controller", c.Label, "identity_source", c.IdentitySource, "pci_address", c.PCIAddress,
			"driver", c.Driver, "backend", c.Backend, "model", c.Model, "firmware", c.Firmware))
	}
	gauge("hwraid_controller_backend_supported", "1 si un module sait collecter cette carte.")
	for _, c := range s.Controllers {
		fmt.Fprintf(&b, "hwraid_controller_backend_supported{%s} %d\n", labels("controller", c.Label), b2i(c.Backend != ""))
	}
	gauge("hwraid_management_node_valid", "0 si le nœud dédié est absent ou ne correspond pas à la carte.")
	for _, c := range s.Controllers {
		if c.NodeChecked {
			fmt.Fprintf(&b, "hwraid_management_node_valid{%s} %d\n", labels("controller", c.Label), b2i(c.NodeValid))
		}
	}
	gauge("hwraid_collector_supported", "0 pour une fonction explicitement non prise en charge par la carte.")
	for _, c := range s.Controllers {
		for _, col := range c.Unsupported {
			fmt.Fprintf(&b, "hwraid_collector_supported{%s} 0\n", labels("controller", c.Label, "collector", col))
		}
	}
	gauge("hwraid_volume_info", "Inventaire des volumes (conteneurs) de la carte.")
	for _, c := range s.Controllers {
		for _, v := range c.Volumes {
			fmt.Fprintf(&b, "hwraid_volume_info{%s} 1\n", labels("controller", c.Label, "volume", v.ID, "name", v.Name, "raid_level", v.RaidLevel, "vendor_level", v.VendorLevel, "parent", v.Parent))
		}
	}
	gauge("hwraid_volume_state", "État calculé du volume, valeur 1.")
	for _, c := range s.Controllers {
		for _, v := range c.Volumes {
			fmt.Fprintf(&b, "hwraid_volume_state{%s} 1\n", labels("controller", c.Label, "volume", v.ID, "state", v.State))
		}
	}
	gauge("hwraid_volume_capacity_bytes", "Taille du volume en octets.")
	for _, c := range s.Controllers {
		for _, v := range c.Volumes {
			fmt.Fprintf(&b, "hwraid_volume_capacity_bytes{%s} %d\n", labels("controller", c.Label, "volume", v.ID), v.CapacityBytes)
		}
	}
	gauge("hwraid_volume_member", "Membres de chaque volume (disque ou volume), valeur 1.")
	for _, c := range s.Controllers {
		for _, v := range c.Volumes {
			for _, m := range v.Members {
				fmt.Fprintf(&b, "hwraid_volume_member{%s} 1\n", labels("controller", c.Label, "volume", v.ID, "member", m))
			}
		}
	}
	gauge("hwraid_disk_state", "État calculé des disques membres, valeur 1.")
	for _, c := range s.Controllers {
		for _, d := range c.Disks {
			fmt.Fprintf(&b, "hwraid_disk_state{%s} 1\n", labels("controller", c.Label, "disk", d.ID, "state", d.State))
		}
	}
	gauge("hwraid_disk_info", "Inventaire des disques présents sur la carte, valeur 1.")
	for _, c := range s.Controllers {
		for _, d := range c.DiskInventory {
			fmt.Fprintf(&b, "hwraid_disk_info{%s} 1\n", labels("controller", c.Label, "disk", d.ID, "vendor", d.Vendor,
				"model", d.Model, "firmware", d.Firmware, "serial", d.Serial, "wwn", d.WWN))
		}
	}
	gauge("hwraid_disk_location", "Emplacement des disques reliés directement à la carte, valeur 1.")
	for _, c := range s.Controllers {
		for _, d := range c.DiskLocations {
			fmt.Fprintf(&b, "hwraid_disk_location{%s} 1\n", labels("controller", c.Label, "disk", d.ID, "connector", d.Connector,
				"connector_name", d.ConnectorName, "device", d.Device, "phy", d.Phy))
		}
	}
	gauge("hwraid_disk_capacity_bytes", "Capacité brute du disque en octets.")
	for _, c := range s.Controllers {
		for _, d := range c.DiskInventory {
			if d.CapacityBytes > 0 {
				fmt.Fprintf(&b, "hwraid_disk_capacity_bytes{%s} %d\n", labels("controller", c.Label, "disk", d.ID), d.CapacityBytes)
			}
		}
	}
	gauge("hwraid_cache_protection_present", "1 si un module de protection du cache est installé.")
	for _, c := range s.Controllers {
		if cp := c.CacheProtection; cp != nil {
			fmt.Fprintf(&b, "hwraid_cache_protection_present{%s} %d\n", labels("controller", c.Label, "type", cp.Type, "vendor_type", cp.Vendor), b2i(cp.Present))
		}
	}
	gauge("hwraid_cache_protection_state", "État observé du module de protection du cache, valeur 1.")
	for _, c := range s.Controllers {
		if cp := c.CacheProtection; cp != nil && cp.Present {
			fmt.Fprintf(&b, "hwraid_cache_protection_state{%s} 1\n", labels("controller", c.Label, "state", cp.State))
		}
	}
	gauge("hwraid_collection_interval_seconds", "Intervalle de collecte effectif de la carte.")
	for _, c := range s.Controllers {
		if c.Backend != "" && s.Interval > 0 {
			fmt.Fprintf(&b, "hwraid_collection_interval_seconds{%s} %g\n", labels("controller", c.Label), s.Interval.Seconds())
		}
	}
	gauge("hwraid_collection_last_success_timestamp_seconds", "Date Unix du dernier succès du collecteur.")
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
	gauge("hwraid_collection_success", "Résultat de la dernière lecture du collecteur.")
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
