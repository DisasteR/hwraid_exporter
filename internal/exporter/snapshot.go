// Package exporter assemble l'instantané publié par /metrics à partir de
// l'inventaire sysfs et des lectures du transport. Il ne fait aucun appel
// système direct.
package exporter

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/DisasteR/hwraid_exporter/internal/aacraid"
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
	NodeError              string
}

// Snapshot est une vue complète et immuable.
type Snapshot struct {
	Ready            bool
	DiscoverySuccess bool
	Controllers      []Controller
}

// NodeChecker vérifie le nœud dédié d'une carte et renvoie la réponse
// GET_HBA_INFO ; il est fourni par la commande (transport réel ou simulé).
type NodeChecker func(h sysfs.SCSIHost) (aacraid.HBAInfo, error)

var pciRe = regexp.MustCompile(`^([0-9a-f]{4}):([0-9a-f]{2}):([0-9a-f]{2})\.([0-7])$`)

// ParsePCI décompose une adresse domaine:bus:slot.fonction.
func ParsePCI(addr string) (bus, slot, fn uint8, err error) {
	m := pciRe.FindStringSubmatch(addr)
	if m == nil {
		return 0, 0, 0, fmt.Errorf("adresse PCI invalide %q", addr)
	}
	b, _ := strconv.ParseUint(m[2], 16, 8)
	s, _ := strconv.ParseUint(m[3], 16, 8)
	f, _ := strconv.ParseUint(m[4], 16, 8)
	return uint8(b), uint8(s), uint8(f), nil
}

// Build construit l'instantané. Les hôtes SCSI d'une même fonction PCI sont
// regroupés ; seuls les contrôleurs aacraid sont pris en charge.
func Build(hosts []sysfs.SCSIHost, discoveryOK bool, check NodeChecker) Snapshot {
	byPCI := map[string]sysfs.SCSIHost{}
	for _, h := range hosts {
		if h.ProcName != "aacraid" { // prototype : seule famille connue
			continue
		}
		if _, seen := byPCI[h.PCI.Address]; !seen {
			byPCI[h.PCI.Address] = h
		}
	}
	// Une série n'identifie une carte que si elle est non vide et unique.
	serials := map[string]int{}
	for _, h := range byPCI {
		if h.SerialNumber != "" {
			serials[h.SerialNumber]++
		}
	}
	var ctrls []Controller
	for _, addr := range sortedKeys(byPCI) {
		h := byPCI[addr]
		c := Controller{
			PCIAddress: addr, Driver: h.PCI.Driver, Backend: "aacraid",
			Model: h.Model, Firmware: h.Firmware,
		}
		if h.SerialNumber != "" && serials[h.SerialNumber] == 1 {
			c.Label, c.IdentitySource = "serial:"+h.SerialNumber, "serial"
		} else {
			c.Label, c.IdentitySource = "pci:"+addr, "pci"
		}
		if check != nil {
			c.NodeChecked = true
			c.NodeValid, c.NodeError = verify(h, check)
		}
		ctrls = append(ctrls, c)
	}
	return Snapshot{Ready: true, DiscoverySuccess: discoveryOK, Controllers: ctrls}
}

func verify(h sysfs.SCSIHost, check NodeChecker) (bool, string) {
	info, err := check(h)
	if err != nil {
		return false, err.Error()
	}
	bus, slot, fn, err := ParsePCI(h.PCI.Address)
	if err != nil {
		return false, err.Error()
	}
	uid, err := strconv.ParseUint(h.UniqueID, 10, 8)
	if err != nil {
		return false, "unique_id invalide"
	}
	if !info.MatchesPCI(uint8(uid), bus, slot, fn) {
		return false, fmt.Sprintf("GET_HBA_INFO désigne la carte %d en %02x:%02x.%d", info.AdapterNumber, info.Bus, info.Device, info.Function)
	}
	return true, ""
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

func sortedKeys[V any](m map[string]V) []string {
	k := make([]string, 0, len(m))
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}
