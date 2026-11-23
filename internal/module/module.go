// Package module définit le contrat d'un module de pilote (section 6.10 du
// cahier des charges) et la planification qui associe chaque carte à son
// module.
//
// Un module ne crée rien lui-même : il déclare, par des fonctions pures, les
// nœuds dont il a besoin et les capacités à conserver. Le cœur crée seul les
// nœuds pendant la phase privilégiée, puis abandonne ses privilèges en
// gardant l'union des capacités déclarées par les modules qui ont une carte.
package module

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/DisasteR/hwraid_exporter/internal/nodes"
	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
)

// Card est une fonction PCI et les hôtes SCSI qui en dépendent.
type Card struct {
	PCI   sysfs.PCIFunction
	Hosts []sysfs.SCSIHost // triés par numéro d'hôte
}

// NodeRequest est la déclaration d'un nœud par un module.
type NodeRequest struct {
	Name    string // nom dans le répertoire des nœuds dédiés
	MajorOf string // nom du pilote dans /proc/devices dont lire le major
	Minor   uint32
}

// NodeRef désigne un nœud préparé, transmis au module pour la collecte.
type NodeRef struct {
	Path string
	Dev  uint64
}

// Result est le résultat d'un cycle de collecte pour une carte.
type Result struct {
	NodeValid bool  // le nœud ouvert désigne bien la carte
	Err       error // cause d'un échec, sans donnée identifiante
	// Unsupported liste les collecteurs que la carte ne permet pas
	// (hwraid_collector_supported = 0).
	Unsupported []string
	// CollectErr : échec d'une lecture firmware, sans effet sur NodeValid.
	CollectErr error
	// CacheProtection est l'état du module de protection du cache lu
	// pendant le cycle ; nil si la lecture n'a pas abouti ou n'a pas eu lieu.
	CacheProtection *CacheProtection
	// Volumes et Disks : configuration lue pendant le cycle ; publiés
	// seulement si la lecture complète a abouti.
	Volumes []Volume
	Disks   []Disk
	// DiskInventory : inventaire de tous les disques présents (membres ou
	// non), publié seulement si la lecture complète a abouti.
	DiskInventory []DiskInfo
	// DiskLocations : emplacement des disques reliés directement à la carte.
	DiskLocations []DiskLocation
	// DiskOSDevices : nœud Linux de chaque disque présenté par le pilote.
	DiskOSDevices []DiskOSDevice
	// Collected donne, pour chaque collecteur tenté pendant le cycle, le
	// résultat de sa lecture (hwraid_collection_success). Un collecteur
	// absent n'a pas été tenté.
	Collected map[string]bool
	// Cached liste les collecteurs servis par une réponse gardée en mémoire
	// (capacités de la carte) : leur succès ne date pas une lecture du
	// cycle, ils n'ont pas de date de dernier succès.
	Cached []string
	// TimedOut : la collecte n'a pas rendu la main avant l'échéance du
	// cycle. Rien n'a été observé, pas même la validité du nœud.
	TimedOut bool
}

// Volume est un conteneur RAID et son état.
type Volume struct {
	ID            string // identifiant stable sur la carte (UID en hexadécimal)
	Name          string
	RaidLevel     string // niveau normalisé (docs/metrics.md) : raid1, concat… ou unknown
	VendorLevel   string // terme de l'outil constructeur, tel qu'il l'affiche
	Parent        string // ID du conteneur qui l'assemble, vide sinon
	CapacityBytes uint64
	State         string
	Members       []string // ID des conteneurs membres ou « d<N> » des disques
}

// Disk est un disque membre d'un conteneur et son état.
type Disk struct {
	ID    string // « d<N> », numéro de disque de la carte
	State string
}

// DiskInfo est l'inventaire d'un disque présent sur la carte.
type DiskInfo struct {
	ID                                   string // « d<N> », comme Disk.ID
	Vendor, Model, Firmware, Serial, WWN string // vides si non publiés par la carte
	CapacityBytes                        uint64 // 0 si inconnue
}

// DiskLocation est l'emplacement d'un disque relié directement à la carte,
// tel que la carte le décrit et que l'outil du constructeur l'affiche.
type DiskLocation struct {
	ID            string // « d<N> »
	Connector     string // numéro du connecteur
	ConnectorName string // nom donné par la carte (« CN1 »)
	Device        string // position dans le connecteur
	Phy           string // phy de la carte
}

// DiskOSDevice relie un disque de la carte au périphérique SCSI que le
// pilote présente à Linux.
type DiskOSDevice struct {
	ID      string // « d<N> »
	Device  string // nœud SCSI générique (sg8)
	Address string // adresse SCSI Linux H:C:T:L
}

// CacheProtection décrit le module de protection du cache (ZMM, batterie).
type CacheProtection struct {
	Present bool   // module installé
	Type    string // type normalisé : flash_supercap, battery
	Vendor  string // nom constructeur du module : « zmm »
	State   string // état normalisé, vide si Present est faux
}

// Module est le contrat d'un module de pilote.
type Module interface {
	// Name est le nom du module, publié dans le label backend.
	Name() string
	// Match indique si le module prend en charge la carte.
	Match(c Card) bool
	// Nodes déclare les nœuds nécessaires à la carte. Fonction pure.
	Nodes(c Card) ([]NodeRequest, error)
	// Capabilities est le masque des capacités à conserver après l'abandon
	// des privilèges si le module a au moins une carte.
	Capabilities() uint64
	// Collectors liste les collecteurs que Collect peut renseigner : ils
	// sont publiés en échec quand la collecte d'une carte dépasse son délai.
	Collectors() []string
	// Collect lit la carte au travers de ses nœuds préparés. Appelé après
	// l'abandon des privilèges, éventuellement en parallèle pour des cartes
	// différentes : l'état partagé du module doit être protégé, sans tenir
	// de verrou pendant un appel au firmware.
	Collect(c Card, refs []NodeRef) Result
}

// Planned associe une carte à son module.
type Planned struct {
	Card     Card
	Module   Module // nil si aucun module ou correspondance ambiguë
	Requests []NodeRequest
	Err      error // ambiguïté, déclaration invalide, conflit de nom
}

// Plan est le résultat de la planification.
type Plan struct {
	Cards []Planned // triées par adresse PCI
	Keep  uint64    // union des capacités des modules ayant une carte
}

// ErrAmbiguous : plusieurs modules revendiquent la même carte.
var ErrAmbiguous = errors.New("ambiguous match between modules")

// ErrNameConflict : deux cartes déclarent le même nom de nœud.
var ErrNameConflict = errors.New("node name declared by several cards")

// Build regroupe les hôtes par fonction PCI et associe chaque carte à son
// module. Une carte est retenue si sa classe PCI est RAID ou si un module la
// revendique : une carte RAID sans module reste inventoriée.
func Build(hosts []sysfs.SCSIHost, mods []Module) Plan {
	byPCI := map[string]*Card{}
	for _, h := range hosts {
		c, ok := byPCI[h.PCI.Address]
		if !ok {
			c = &Card{PCI: h.PCI}
			byPCI[h.PCI.Address] = c
		}
		c.Hosts = append(c.Hosts, h)
	}
	addrs := make([]string, 0, len(byPCI))
	for a := range byPCI {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)

	var p Plan
	owners := map[string][]int{} // nom de nœud → index des cartes
	for _, a := range addrs {
		c := *byPCI[a]
		sort.Slice(c.Hosts, func(i, j int) bool { return c.Hosts[i].Number < c.Hosts[j].Number })
		var matched []Module
		for _, m := range mods {
			if m.Match(c) {
				matched = append(matched, m)
			}
		}
		if len(matched) == 0 && !c.PCI.IsRAIDClass() {
			continue // ni RAID ni pris en charge : hors inventaire (SATA, etc.)
		}
		pl := Planned{Card: c}
		switch len(matched) {
		case 0:
		case 1:
			pl.Module = matched[0]
			p.Keep |= matched[0].Capabilities()
			reqs, err := matched[0].Nodes(c)
			if err != nil {
				pl.Err = err
			} else {
				pl.Requests = reqs
			}
		default:
			names := make([]string, len(matched))
			for i, m := range matched {
				names[i] = m.Name()
			}
			pl.Err = fmt.Errorf("%w: %v", ErrAmbiguous, names)
		}
		for _, r := range pl.Requests {
			owners[r.Name] = append(owners[r.Name], len(p.Cards))
		}
		p.Cards = append(p.Cards, pl)
	}
	// Un même nom déclaré par deux cartes : aucune des deux n'obtient ce nœud.
	for name, idx := range owners {
		if len(idx) < 2 {
			continue
		}
		for _, i := range idx {
			p.Cards[i].Err = fmt.Errorf("%w: %s", ErrNameConflict, name)
			p.Cards[i].Requests = nil
		}
	}
	return p
}

// Specs résout les déclarations en nœuds à créer, avec les majors lus dans
// /proc/devices. Une carte dont un major est introuvable reçoit une erreur
// et ne produit aucun nœud.
func (p *Plan) Specs(majors map[string]uint32) []nodes.Spec {
	var out []nodes.Spec
	for i := range p.Cards {
		pc := &p.Cards[i]
		if pc.Err != nil {
			continue
		}
		var specs []nodes.Spec
		for _, r := range pc.Requests {
			m, ok := majors[r.MajorOf]
			if !ok || m == 0 {
				pc.Err = fmt.Errorf("major %q missing from /proc/devices", r.MajorOf)
				specs = nil
				break
			}
			specs = append(specs, nodes.Spec{Name: r.Name, MajorOf: r.MajorOf, Major: m, Minor: r.Minor})
		}
		out = append(out, specs...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Prepared indique que chaque nœud demandé par la carte, avec le major lu
// dans majors, est exactement un nœud préparé au démarrage : la carte peut
// être collectée même si le reste de l'inventaire a changé ou n'a pas pu
// être lu.
func Prepared(pc Planned, majors map[string]uint32, prepared []nodes.Spec) bool {
	if pc.Err != nil {
		return false
	}
	have := map[nodes.Spec]bool{}
	for _, s := range prepared {
		have[s] = true
	}
	for _, r := range pc.Requests {
		m, ok := majors[r.MajorOf]
		if !ok || m == 0 || !have[nodes.Spec{Name: r.Name, MajorOf: r.MajorOf, Major: m, Minor: r.Minor}] {
			return false
		}
	}
	return true
}

// Refs renvoie les nœuds préparés d'une carte, pour Collect.
func Refs(dir string, pc Planned, specs []nodes.Spec) []NodeRef {
	byName := map[string]nodes.Spec{}
	for _, s := range specs {
		byName[s.Name] = s
	}
	var refs []NodeRef
	for _, r := range pc.Requests {
		if s, ok := byName[r.Name]; ok {
			refs = append(refs, NodeRef{Path: filepath.Join(dir, s.Name), Dev: s.Dev()})
		}
	}
	return refs
}
