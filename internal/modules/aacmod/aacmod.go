// Package aacmod est le module du pilote Linux aacraid (Adaptec série 6 et
// apparentées). Il déclare ses nœuds et ses capacités, puis lit la carte au
// travers de la couche de transport aacio.
package aacmod

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"

	"github.com/DisasteR/hwraid_exporter/internal/aacraid"
	"github.com/DisasteR/hwraid_exporter/internal/allowlist"
	"github.com/DisasteR/hwraid_exporter/internal/module"
	"github.com/DisasteR/hwraid_exporter/internal/procstatus"
	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
	"github.com/DisasteR/hwraid_exporter/internal/transport/aacio"
)

// device est la partie du transport utilisée par le module (injectable).
type device interface {
	HBAInfo() (aacraid.HBAInfo, error)
	SupplementInfo() (aacraid.SupplementInfo, error)
	CacheProtection() (bool, aacraid.ZMMStatus, error)
	ReadPaged(r aacraid.PagedRead, param, size uint32, need func([]byte) int) ([]byte, error)
	Close() error
}

// StateCollectors sont les collecteurs qui reposent sur les lectures de
// volume, de membres et de disque (docs/aacraid-interface.md, section 7).
var StateCollectors = []string{"volumes", "disks"}

// CapabilitiesCollector est le collecteur de la lecture des capacités de la
// carte (RequestSupplementAdapterInfo), préalable aux collecteurs d'état.
const CapabilitiesCollector = "capabilities"

// CacheProtectionCollector est le collecteur de l'état du module de
// protection du cache (ZMM).
const CacheProtectionCollector = "cache_protection"

// Module implémente module.Module pour aacraid.
type Module struct {
	open func(path string, dev uint64) (device, error)

	mu sync.Mutex
	// supplement garde, par carte et par nœud, la réponse à
	// RequestSupplementAdapterInfo : elle ne change pas sans réinitialisation
	// de la carte, qui change aussi le nœud. Une lecture en échec n'est pas
	// gardée et sera retentée au cycle suivant.
	supplement map[string]aacraid.SupplementInfo
}

// New construit le module avec le transport réel et la liste blanche aacraid.
func New(list *allowlist.List) *Module {
	return &Module{open: func(path string, dev uint64) (device, error) {
		return aacio.Open(path, dev, list)
	}}
}

// Name renvoie le nom du module.
func (*Module) Name() string { return "aacraid" }

// Match retient les cartes liées au pilote aacraid.
func (*Module) Match(c module.Card) bool { return c.PCI.Driver == "aacraid" }

// Collectors liste tous les collecteurs du module.
func (*Module) Collectors() []string {
	return append([]string{CapabilitiesCollector, CacheProtectionCollector}, StateCollectors...)
}

// Capabilities : CAP_SYS_RAWIO, exigée par le pilote à chaque ioctl.
func (*Module) Capabilities() uint64 { return procstatus.Mask(procstatus.CapSysRawIO) }

// host renvoie l'hôte SCSI aacraid de la carte (un seul par carte).
func host(c module.Card) (int, uint32, error) {
	for i, h := range c.Hosts {
		if h.ProcName != "aacraid" {
			continue
		}
		uid, err := strconv.ParseUint(h.UniqueID, 10, 8)
		if err != nil {
			return 0, 0, fmt.Errorf("%s : unique_id %q invalide", h.Name, h.UniqueID)
		}
		return i, uint32(uid), nil
	}
	return 0, 0, errors.New("aucun hôte SCSI aacraid sur la carte")
}

// Nodes déclare le nœud aac<unique_id> : major du pilote « aac », minor égal
// au unique_id de l'hôte SCSI (le pilote associe le minor à aac->id).
func (*Module) Nodes(c module.Card) ([]module.NodeRequest, error) {
	_, uid, err := host(c)
	if err != nil {
		return nil, err
	}
	return []module.NodeRequest{{Name: "aac" + strconv.FormatUint(uint64(uid), 10), MajorOf: "aac", Minor: uid}}, nil
}

// Collect ouvre le nœud préparé, lit FSACTL_GET_HBA_INFO (réponse du pilote,
// sans commande firmware) et vérifie qu'il désigne bien cette carte. Il lit
// ensuite, une fois par carte, RequestSupplementAdapterInfo : si la carte
// n'annonce pas l'interface des lectures d'état, les collecteurs d'état sont
// déclarés non pris en charge et aucune autre lecture n'est tentée.
func (m *Module) Collect(c module.Card, refs []module.NodeRef) module.Result {
	if len(refs) != 1 {
		return module.Result{Err: fmt.Errorf("%d nœud(s) préparé(s), 1 attendu", len(refs))}
	}
	_, uid, err := host(c)
	if err != nil {
		return module.Result{Err: err}
	}
	bus, slot, fn, err := parsePCI(c.PCI.Address)
	if err != nil {
		return module.Result{Err: err}
	}
	d, err := m.open(refs[0].Path, refs[0].Dev)
	if err != nil {
		return module.Result{Err: err}
	}
	defer d.Close()
	info, err := d.HBAInfo()
	if err != nil {
		return module.Result{Err: err}
	}
	if !info.MatchesPCI(uint8(uid), bus, slot, fn) {
		return module.Result{Err: fmt.Errorf("GET_HBA_INFO désigne la carte %d en %02x:%02x.%d",
			info.AdapterNumber, info.Bus, info.Device, info.Function)}
	}
	r := module.Result{NodeValid: true}
	sup, err := m.supplementInfo(c.PCI.Address+"|"+strconv.FormatUint(refs[0].Dev, 10), d)
	r.Collected = map[string]bool{CapabilitiesCollector: err == nil}
	if err != nil {
		r.CollectErr = fmt.Errorf("RequestSupplementAdapterInfo : %w", err)
		return r
	}
	// Lectures firmware au-delà de 706 : seulement vers un couple modèle et
	// firmware qualifié (exigence S4) ; toute autre carte est publiée avec
	// ces collecteurs non pris en charge.
	if !Qualified(c) {
		r.Unsupported = append(append([]string(nil), StateCollectors...), CacheProtectionCollector)
		return r
	}
	if !sup.ArcIoReads() {
		r.Unsupported = append([]string(nil), StateCollectors...)
	}
	collectCacheProtection(&r, d)
	if sup.ArcIoReads() {
		collectVolumes(&r, d)
	}
	return r
}

// Qualification est un couple modèle et firmware sur lequel les lectures
// (volumes, membres, disques, protection du cache) ont été vérifiées contre
// l'outil de référence (docs/aacraid-interface.md, section 8).
type Qualification struct {
	Vendor, Device, SubsystemVendor, SubsystemDevice string // tels que publiés par sysfs
	Firmware                                         string // hba_kernel_version
}

// Qualifications liste les couples qualifiés.
var Qualifications = []Qualification{
	// Adaptec 6805, firmware 5.3-0 build 19204 (hôte de référence, 2026-11-23).
	{Vendor: "0x9005", Device: "0x028b", SubsystemVendor: "0x9005", SubsystemDevice: "0x0301", Firmware: "5.3-0[19204]"},
}

// Qualified indique si la carte correspond à un couple qualifié.
func Qualified(c module.Card) bool {
	_, h, err := hostOf(c)
	if err != nil {
		return false
	}
	for _, q := range Qualifications {
		if c.PCI.Vendor == q.Vendor && c.PCI.Device == q.Device && c.PCI.SubsystemVendor == q.SubsystemVendor &&
			c.PCI.SubsystemDevice == q.SubsystemDevice && h.Firmware == q.Firmware {
			return true
		}
	}
	return false
}

// hostOf renvoie l'hôte SCSI aacraid de la carte.
func hostOf(c module.Card) (int, sysfs.SCSIHost, error) {
	i, _, err := host(c)
	if err != nil {
		return 0, sysfs.SCSIHost{}, err
	}
	return i, c.Hosts[i], nil
}

// collectVolumes lit la configuration (conteneurs, membres, disques
// présents) et calcule les états. Toute erreur annule la publication des
// volumes et des disques pour ce cycle : jamais de configuration partielle.
func collectVolumes(r *module.Result, d device) {
	vols, disks, err := readConfiguration(d)
	for _, col := range StateCollectors {
		r.Collected[col] = err == nil
	}
	if err != nil {
		if r.CollectErr == nil {
			r.CollectErr = fmt.Errorf("configuration : %w", err)
		}
		return
	}
	r.Volumes, r.Disks = vols, disks
}

func readConfiguration(d device) ([]module.Volume, []module.Disk, error) {
	raw, err := d.ReadPaged(aacraid.ReadContainerList, 0, 0, aacraid.ContainerListNeed)
	if err != nil {
		return nil, nil, err
	}
	nums, err := aacraid.DecodeContainerList(raw)
	if err != nil {
		return nil, nil, err
	}
	var cs []aacraid.Container
	for _, n := range nums {
		raw, err := d.ReadPaged(aacraid.ReadContainerInfo, n, 0, func([]byte) int { return aacraid.ContainerInfoNeed })
		if err != nil {
			return nil, nil, err
		}
		info, err := aacraid.DecodeContainerInfo(raw)
		if err != nil {
			return nil, nil, err
		}
		for _, prev := range cs {
			if prev.Info.UID == info.UID {
				return nil, nil, fmt.Errorf("UID de conteneur %#x en double (conteneurs %d et %d)", info.UID, prev.Number, n)
			}
		}
		cs = append(cs, aacraid.Container{Number: n, Info: info})
	}
	raw, err = d.ReadPaged(aacraid.ReadDiskList, 0, 0, aacraid.DiskListNeed)
	if err != nil {
		return nil, nil, err
	}
	disks, err := aacraid.DecodeDiskList(raw)
	if err != nil {
		return nil, nil, err
	}
	for i := range cs {
		size := aacraid.MemberListSize(cs[i].Info.Members)
		raw, err := d.ReadPaged(aacraid.ReadMemberList, cs[i].Info.UID, size, func([]byte) int { return int(size) })
		if err != nil {
			return nil, nil, err
		}
		if cs[i].Members, err = aacraid.DecodeMemberList(raw); err != nil {
			return nil, nil, err
		}
	}
	ev := aacraid.Evaluate(cs, disks)
	return volumesOf(cs, ev), disksOf(ev), nil
}

func uidID(uid uint32) string { return fmt.Sprintf("%08x", uid) }

func volumesOf(cs []aacraid.Container, ev aacraid.Evaluation) []module.Volume {
	out := make([]module.Volume, 0, len(cs))
	for _, c := range cs {
		v := module.Volume{
			ID:            uidID(c.Info.UID),
			Name:          c.Info.Name,
			CapacityBytes: c.Info.Blocks * 512,
			State:         string(ev.Volumes[c.Info.UID]),
		}
		// Niveau normalisé et terme de l'outil constructeur : son JSON
		// donne raidLevel 1 aux miroirs ; il affiche « RAID_volume » pour
		// l'assemblage, qu'Adaptec définit comme une concaténation de
		// grappes. Autre type : non observé, jamais deviné.
		switch c.Info.Type {
		case aacraid.TypeMirror:
			v.RaidLevel, v.VendorLevel = "raid1", "1"
		case aacraid.TypeAssembly:
			v.RaidLevel, v.VendorLevel = "concat", "RAID_volume"
		default:
			v.RaidLevel, v.VendorLevel = "unknown", fmt.Sprintf("type %d", c.Info.Type)
		}
		if c.Info.Parent != aacraid.NoUID {
			v.Parent = uidID(c.Info.Parent)
		}
		for _, m := range c.Members {
			if m.Disk != aacraid.NoUID {
				v.Members = append(v.Members, fmt.Sprintf("d%d", m.Disk))
			} else {
				v.Members = append(v.Members, uidID(m.ChildUID))
			}
		}
		out = append(out, v)
	}
	return out
}

func disksOf(ev aacraid.Evaluation) []module.Disk {
	nums := make([]uint32, 0, len(ev.Disks))
	for n := range ev.Disks {
		nums = append(nums, n)
	}
	sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })
	out := make([]module.Disk, 0, len(nums))
	for _, n := range nums {
		out = append(out, module.Disk{ID: fmt.Sprintf("d%d", n), State: string(ev.Disks[n])})
	}
	return out
}

// collectCacheProtection lit l'état du module de protection du cache, à
// chaque cycle : cet état change (charge, panne) sans redémarrage.
func collectCacheProtection(r *module.Result, d device) {
	described, st, err := d.CacheProtection()
	switch {
	case err != nil:
		r.Collected[CacheProtectionCollector] = false
		if r.CollectErr == nil {
			r.CollectErr = fmt.Errorf("protection du cache : %w", err)
		}
	case !described || st.State == aacraid.CacheProtectionOtherType:
		// Format non décrit ou autre type de module : non décodé.
		r.Unsupported = append(r.Unsupported, CacheProtectionCollector)
	case st.State == aacraid.CacheProtectionNotInstalled:
		r.Collected[CacheProtectionCollector] = true
		r.CacheProtection = &module.CacheProtection{Type: "flash_supercap", Vendor: "zmm"}
	default:
		r.Collected[CacheProtectionCollector] = true
		r.CacheProtection = &module.CacheProtection{Present: true, Type: "flash_supercap", Vendor: "zmm", State: string(st.State)}
	}
}

// supplementInfo lit la réponse 706 une seule fois par carte et par nœud.
// La lecture firmware se fait hors du verrou : une carte qui ne répond pas
// ne doit pas bloquer la collecte des autres.
func (m *Module) supplementInfo(key string, d device) (aacraid.SupplementInfo, error) {
	m.mu.Lock()
	s, ok := m.supplement[key]
	m.mu.Unlock()
	if ok {
		return s, nil
	}
	s, err := d.SupplementInfo()
	if err != nil {
		return s, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.supplement == nil {
		m.supplement = map[string]aacraid.SupplementInfo{}
	}
	m.supplement[key] = s
	return s, nil
}

func parsePCI(addr string) (bus, slot, fn uint8, err error) {
	var dom, b, s, f uint
	if n, e := fmt.Sscanf(addr, "%04x:%02x:%02x.%1x", &dom, &b, &s, &f); e != nil || n != 4 || f > 7 {
		return 0, 0, 0, fmt.Errorf("adresse PCI invalide %q", addr)
	}
	return uint8(b), uint8(s), uint8(f), nil
}
