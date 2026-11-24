// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

// Package aacmod est le module du pilote Linux aacraid (Adaptec série 6 et
// apparentées). Il déclare ses nœuds et ses capacités, puis lit la carte au
// travers de la couche de transport aacio.
package aacmod

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/DisasteR/hwraid_exporter/internal/aacraid"
	"github.com/DisasteR/hwraid_exporter/internal/allowlist"
	"github.com/DisasteR/hwraid_exporter/internal/module"
	"github.com/DisasteR/hwraid_exporter/internal/procstatus"
	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
	"github.com/DisasteR/hwraid_exporter/internal/transport/aacio"
)

// device est la partie du transport utilisée par le module (injectable).
// Chaque commande firmware est soumise au contexte de la collecte : une fois
// celui-ci terminé (échéance du cycle ou arrêt du service), le transport
// n'en envoie plus de nouvelle (aacio.ErrStopped).
type device interface {
	HBAInfo() (aacraid.HBAInfo, error)
	SupplementInfo(ctx context.Context) (aacraid.SupplementInfo, error)
	CacheProtection(ctx context.Context) (bool, aacraid.ZMMStatus, error)
	ReadPaged(ctx context.Context, r aacraid.PagedRead, param, size uint32, need func([]byte) int) ([]byte, error)
	ConnectorMap(ctx context.Context) (aacraid.ConnectorMap, error)
	CloseUnconfirmed() int
	Close() error
}

// reader est le périphérique lié au contexte d'une collecte.
type reader interface {
	HBAInfo() (aacraid.HBAInfo, error)
	SupplementInfo() (aacraid.SupplementInfo, error)
	CacheProtection() (bool, aacraid.ZMMStatus, error)
	ReadPaged(r aacraid.PagedRead, param, size uint32, need func([]byte) int) ([]byte, error)
	ConnectorMap() (aacraid.ConnectorMap, error)
}

// StateCollectors sont les collecteurs qui reposent sur les lectures de
// volume, de membres et de disque (docs/aacraid-interface.md, section 7).
var StateCollectors = []string{"volumes", "disks"}

// DiskInventoryCollector est le collecteur de l'inventaire des disques
// présents (fabricant, modèle, firmware, numéro de série, WWN, capacité).
const DiskInventoryCollector = "disk_inventory"

// DiskLocationCollector est le collecteur de l'emplacement des disques
// reliés directement à la carte (connecteur, position, phy).
const DiskLocationCollector = "disk_location"

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
	// connectors garde de même la carte des connecteurs (708), fixe tant que
	// la carte n'est pas réinitialisée.
	connectors map[string]keptMap
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
	return append([]string{CapabilitiesCollector, CacheProtectionCollector, DiskInventoryCollector, DiskLocationCollector}, StateCollectors...)
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
			return 0, 0, fmt.Errorf("%s: invalid unique_id %q", h.Name, h.UniqueID)
		}
		return i, uint32(uid), nil
	}
	return 0, 0, errors.New("no aacraid SCSI host on the card")
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

// bound lie le périphérique au contexte de la collecte.
type bound struct {
	dev device
	ctx context.Context
}

func (b bound) HBAInfo() (aacraid.HBAInfo, error) { return b.dev.HBAInfo() }
func (b bound) SupplementInfo() (aacraid.SupplementInfo, error) {
	return b.dev.SupplementInfo(b.ctx)
}
func (b bound) CacheProtection() (bool, aacraid.ZMMStatus, error) {
	return b.dev.CacheProtection(b.ctx)
}
func (b bound) ReadPaged(r aacraid.PagedRead, param, size uint32, need func([]byte) int) ([]byte, error) {
	return b.dev.ReadPaged(b.ctx, r, param, size, need)
}
func (b bound) ConnectorMap() (aacraid.ConnectorMap, error) { return b.dev.ConnectorMap(b.ctx) }

// Collect ouvre le nœud préparé, lit FSACTL_GET_HBA_INFO (réponse du pilote,
// sans commande firmware) et vérifie qu'il désigne bien cette carte. Il lit
// ensuite, une fois par carte, RequestSupplementAdapterInfo : si la carte
// n'annonce pas l'interface des lectures d'état, les collecteurs d'état sont
// déclarés non pris en charge et aucune autre lecture n'est tentée. Une fois
// ctx terminé, aucune nouvelle commande firmware n'est envoyée ; les
// collecteurs non lus sont en échec (aacio.ErrStopped).
func (m *Module) Collect(ctx context.Context, c module.Card, refs []module.NodeRef) (res module.Result) {
	if len(refs) != 1 {
		return module.Result{Err: fmt.Errorf("%d prepared node(s), 1 expected", len(refs))}
	}
	_, uid, err := host(c)
	if err != nil {
		return module.Result{Err: err}
	}
	id, err := pciIdentity(c)
	if err != nil {
		return module.Result{Err: err}
	}
	raw, err := m.open(refs[0].Path, refs[0].Dev)
	if err != nil {
		return module.Result{Err: err}
	}
	defer raw.Close()
	// Clôtures confirmées autrement que par 0xc6 pendant ce cycle, quel que
	// soit le chemin de retour.
	defer func() { res.CloseUnconfirmed = raw.CloseUnconfirmed() }()
	d := bound{dev: raw, ctx: ctx}
	info, err := d.HBAInfo()
	if err != nil {
		return module.Result{Err: err}
	}
	// Aucune commande firmware avant que le nœud ouvert désigne la carte
	// attendue : numéro, adresse et identifiants PCI.
	if !info.Matches(uint8(uid), id) {
		return module.Result{Err: fmt.Errorf("GET_HBA_INFO points to card %d at %02x:%02x.%d (%04x:%04x %04x:%04x)",
			info.AdapterNumber, info.Bus, info.Device, info.Function, info.VendorID, info.DeviceID, info.SubVendorID, info.SubSystemID)}
	}
	r := module.Result{NodeValid: true}
	// Réponses gardées (706, 708) : rattachées à la carte telle que sysfs la
	// décrit (adresse, nœud, identifiants PCI, numéro de série, firmware).
	// Une carte remplacée ou un firmware mis à jour change la clé : tout est
	// relu.
	_, h, _ := hostOf(c)
	key := strings.Join([]string{c.PCI.Address, strconv.FormatUint(refs[0].Dev, 10),
		c.PCI.Vendor, c.PCI.Device, c.PCI.SubsystemVendor, c.PCI.SubsystemDevice, h.SerialNumber, h.Firmware}, "|")
	sup, err := m.supplementInfo(key, d)
	r.Collected = map[string]bool{CapabilitiesCollector: err == nil}
	// Réponse 706 gardée en mémoire : pas de date de dernier succès.
	r.Cached = []string{CapabilitiesCollector}
	if err != nil {
		r.CollectErr = fmt.Errorf("RequestSupplementAdapterInfo: %w", err)
		return r
	}
	// Lectures firmware au-delà de 706 : seulement vers un couple modèle et
	// firmware qualifié (exigence S4) ; toute autre carte est publiée avec
	// ces collecteurs non pris en charge.
	if !Qualified(c) {
		r.Unsupported = append(append([]string(nil), StateCollectors...), DiskInventoryCollector, DiskLocationCollector, CacheProtectionCollector)
		return r
	}
	if !sup.ArcIoReads() {
		r.Unsupported = append(append([]string(nil), StateCollectors...), DiskInventoryCollector, DiskLocationCollector)
	}
	collectCacheProtection(&r, d)
	if sup.ArcIoReads() {
		_, h, _ := hostOf(c)
		m.collectVolumes(&r, d, key, h)
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
// présents) et calcule les états, puis l'inventaire des disques présents.
// Toute erreur annule la publication de ce qu'elle concerne pour ce cycle :
// jamais de configuration ni d'inventaire partiels. L'inventaire réutilise
// la liste des disques présents : il n'est pas lu si la configuration ne
// l'a pas été.
func (m *Module) collectVolumes(r *module.Result, d reader, key string, h sysfs.SCSIHost) {
	vols, disks, present, err := readConfiguration(d)
	for _, col := range StateCollectors {
		r.Collected[col] = err == nil
	}
	if err != nil {
		r.Collected[DiskInventoryCollector] = false
		r.Collected[DiskLocationCollector] = false
		r.CollectErr = errors.Join(r.CollectErr, fmt.Errorf("configuration: %w; disk inventory and location not read", err))
		return
	}
	r.Volumes, r.Disks = vols, disks
	inv, err := readInventory(d, present)
	r.Collected[DiskInventoryCollector] = err == nil
	if err != nil {
		r.Collected[DiskLocationCollector] = false
		r.CollectErr = errors.Join(r.CollectErr, fmt.Errorf("disk inventory: %w; disk location not read", err))
		return
	}
	for _, i := range inv {
		r.DiskInventory = append(r.DiskInventory, module.DiskInfo{ID: diskID(i.Number), Vendor: i.Vendor, Model: i.Model,
			Firmware: i.Firmware, Serial: i.Serial, WWN: i.WWN, CapacityBytes: i.CapacityBytes()})
	}
	r.DiskOSDevices = osDevices(h, inv)
	locs, err := m.locations(key, d, inv)
	r.Collected[DiskLocationCollector] = err == nil
	if err != nil {
		r.CollectErr = errors.Join(r.CollectErr, fmt.Errorf("disk location: %w", err))
		return
	}
	r.DiskLocations = locs
}

func diskID(n uint32) string { return fmt.Sprintf("d%d", n) }

// osDevices relie chaque disque à son périphérique SCSI Linux. Le pilote
// présente le disque (canal c, cible t) de la carte à l'adresse Linux
// (c + 1, t), LUN 0 (aac_phys_to_logical, drivers/scsi/aacraid/aacraid.h) ;
// le lien n'est publié que si le numéro de série de la page VPD 0x80 gardée
// par le noyau est celui de l'information de disque du cycle. Lecture de
// sysfs seulement, aucune commande à la carte.
func osDevices(h sysfs.SCSIHost, inv []aacraid.DiskInfo) []module.DiskOSDevice {
	var out []module.DiskOSDevice
	for _, i := range inv {
		channel, target := int(i.Number>>24)+1, int(i.Number&0xffffff)
		for _, dev := range h.Devices {
			if dev.Channel != channel || dev.Target != target || dev.LUN != 0 {
				continue
			}
			if dev.Generic != "" && dev.Serial != "" && dev.Serial == i.Serial {
				out = append(out, module.DiskOSDevice{ID: diskID(i.Number), Device: dev.Generic, Address: dev.Address})
			}
		}
	}
	return out
}

// locations donne l'emplacement de chaque disque relié directement à la
// carte, tel que la carte des connecteurs le décrit et tel que l'outil de
// référence l'affiche. Le phy de la carte des connecteurs doit être celui
// de l'information de disque lue pendant le cycle : sinon la carte a changé
// ou une réponse est incohérente, et aucun emplacement n'est publié. Un
// disque sans entrée (derrière un expandeur) n'a pas d'emplacement publié.
func (m *Module) locations(key string, d reader, inv []aacraid.DiskInfo) ([]module.DiskLocation, error) {
	cm, err := m.connectorMap(key, d, inv)
	if err != nil {
		return nil, err
	}
	var out []module.DiskLocation
	for _, i := range inv {
		e, ok := cm.Locate(i.Number)
		if !ok {
			continue
		}
		if e.Phy != i.Phy {
			m.forgetConnectorMap(key)
			return nil, fmt.Errorf("disk %d: phy %d in the connector map, %d in the disk info", i.Number, e.Phy, i.Phy)
		}
		out = append(out, module.DiskLocation{ID: diskID(i.Number), Connector: strconv.Itoa(int(e.Connector)),
			ConnectorName: cm.Names[e.Connector], Device: strconv.Itoa(int(e.Device)), Phy: strconv.Itoa(int(e.Phy))})
	}
	return out, nil
}

// keptMap est une carte des connecteurs gardée, avec l'ensemble des disques
// présents au moment de sa lecture.
type keptMap struct {
	cm    aacraid.ConnectorMap
	disks string
}

// diskSet résume l'ensemble des disques présents de l'inventaire.
func diskSet(inv []aacraid.DiskInfo) string {
	nums := make([]string, 0, len(inv))
	for _, i := range inv {
		nums = append(nums, strconv.FormatUint(uint64(i.Number), 10))
	}
	sort.Strings(nums)
	return strings.Join(nums, ",")
}

// connectorMap lit la carte des connecteurs une fois par carte et par nœud,
// hors du verrou, et la relit quand l'ensemble des disques présents change
// (disque ajouté ou retiré à chaud). Une lecture en échec n'est pas gardée.
func (m *Module) connectorMap(key string, d reader, inv []aacraid.DiskInfo) (aacraid.ConnectorMap, error) {
	set := diskSet(inv)
	m.mu.Lock()
	k, ok := m.connectors[key]
	m.mu.Unlock()
	if ok && k.disks == set {
		return k.cm, nil
	}
	cm, err := d.ConnectorMap()
	if err != nil {
		return cm, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.connectors == nil {
		m.connectors = map[string]keptMap{}
	}
	m.connectors[key] = keptMap{cm: cm, disks: set}
	return cm, nil
}

// forgetConnectorMap oublie la carte gardée : elle sera relue au cycle
// suivant.
func (m *Module) forgetConnectorMap(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.connectors, key)
}

// readInventory lit l'information de chaque disque présent. Seul le début
// de chaque réponse est lu (DiskInfoNeed), puis le transfert est clos.
func readInventory(d reader, nums []uint32) ([]aacraid.DiskInfo, error) {
	sorted := append([]uint32(nil), nums...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	out := make([]aacraid.DiskInfo, 0, len(sorted))
	for _, n := range sorted {
		raw, err := d.ReadPaged(aacraid.ReadDiskInfo, n, 0, func([]byte) int { return aacraid.DiskInfoNeed })
		if err != nil {
			return nil, err
		}
		i, err := aacraid.DecodeDiskInfo(raw)
		if err != nil {
			return nil, err
		}
		if i.Number != n {
			return nil, fmt.Errorf("disk %d info: the response is for disk %d", n, i.Number)
		}
		out = append(out, i)
	}
	return out, nil
}

func readConfiguration(d reader) ([]module.Volume, []module.Disk, []uint32, error) {
	raw, err := d.ReadPaged(aacraid.ReadContainerList, 0, 0, aacraid.ContainerListNeed)
	if err != nil {
		return nil, nil, nil, err
	}
	nums, err := aacraid.DecodeContainerList(raw)
	if err != nil {
		return nil, nil, nil, err
	}
	var cs []aacraid.Container
	for _, n := range nums {
		raw, err := d.ReadPaged(aacraid.ReadContainerInfo, n, 0, func([]byte) int { return aacraid.ContainerInfoNeed })
		if err != nil {
			return nil, nil, nil, err
		}
		info, err := aacraid.DecodeContainerInfo(raw)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, prev := range cs {
			if prev.Info.UID == info.UID {
				return nil, nil, nil, fmt.Errorf("duplicate container UID %#x (containers %d and %d)", info.UID, prev.Number, n)
			}
		}
		cs = append(cs, aacraid.Container{Number: n, Info: info})
	}
	raw, err = d.ReadPaged(aacraid.ReadDiskList, 0, 0, aacraid.DiskListNeed)
	if err != nil {
		return nil, nil, nil, err
	}
	disks, err := aacraid.DecodeDiskList(raw)
	if err != nil {
		return nil, nil, nil, err
	}
	for i := range cs {
		size := aacraid.MemberListSize(cs[i].Info.Members)
		raw, err := d.ReadPaged(aacraid.ReadMemberList, cs[i].Info.UID, size, func([]byte) int { return int(size) })
		if err != nil {
			return nil, nil, nil, err
		}
		if cs[i].Members, err = aacraid.DecodeMemberList(raw); err != nil {
			return nil, nil, nil, err
		}
		if len(cs[i].Members) != cs[i].Info.Members {
			return nil, nil, nil, fmt.Errorf("container %#x: %d members announced, %d listed", cs[i].Info.UID, cs[i].Info.Members, len(cs[i].Members))
		}
		seen := map[[2]uint32]bool{}
		for _, mb := range cs[i].Members {
			k := [2]uint32{mb.Disk, mb.ChildUID}
			if seen[k] {
				return nil, nil, nil, fmt.Errorf("container %#x: member listed twice (disk %#x, container %#x)", cs[i].Info.UID, mb.Disk, mb.ChildUID)
			}
			seen[k] = true
		}
	}
	ev := aacraid.Evaluate(cs, disks)
	return volumesOf(cs, ev), disksOf(ev), disks, nil
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
		v.WriteCachePolicy, v.VendorWriteCachePolicy = writeCachePolicy(c.Info.WriteCachePolicy)
		v.WriteCacheEnabled = writeCacheEnabled(c.Info.WriteCacheEnabled)
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

// writeCachePolicy traduit le réglage du cache d'écriture : valeurs
// observées avec l'outil de référence (docs/aacraid-interface.md) ; autre
// valeur publiée unknown, jamais devinée.
func writeCachePolicy(p uint32) (policy, vendor string) {
	switch p {
	case aacraid.WriteCacheAlways:
		return "write_back_always", "Enabled"
	case aacraid.WriteCacheWhenProtected:
		return "write_back_when_protected", "On when protected by battery/ZMM"
	case aacraid.WriteCacheDisabled:
		return "write_through", "Disabled"
	}
	return "unknown", fmt.Sprintf("value %d", p)
}

// writeCacheEnabled traduit l'état effectif du cache d'écriture ; une autre
// valeur que 0 ou 1 n'établit rien.
func writeCacheEnabled(e uint32) *bool {
	switch e {
	case 0, 1:
		on := e == 1
		return &on
	}
	return nil
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
func collectCacheProtection(r *module.Result, d reader) {
	described, st, err := d.CacheProtection()
	switch {
	case err != nil:
		r.Collected[CacheProtectionCollector] = false
		r.CollectErr = errors.Join(r.CollectErr, fmt.Errorf("cache protection: %w", err))
	case !described || st.State == aacraid.CacheProtectionOtherType:
		// Format non décrit ou autre type de module : non décodé.
		r.Unsupported = append(r.Unsupported, CacheProtectionCollector)
	case st.State == aacraid.CacheProtectionNotInstalled:
		r.Collected[CacheProtectionCollector] = true
		r.CacheProtection = &module.CacheProtection{Type: "flash_supercap", Vendor: "zmm"}
	case !st.InstallKnown():
		// Bits d'état sans le bit d'installation : présence non établie.
		r.Collected[CacheProtectionCollector] = true
		r.CacheProtection = &module.CacheProtection{PresenceUnknown: true, Type: "flash_supercap", Vendor: "zmm", State: string(st.State)}
	default:
		r.Collected[CacheProtectionCollector] = true
		r.CacheProtection = &module.CacheProtection{Present: true, Type: "flash_supercap", Vendor: "zmm", State: string(st.State)}
	}
}

// supplementInfo lit la réponse 706 une seule fois par carte et par nœud.
// La lecture firmware se fait hors du verrou : une carte qui ne répond pas
// ne doit pas bloquer la collecte des autres.
func (m *Module) supplementInfo(key string, d reader) (aacraid.SupplementInfo, error) {
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

// pciIdentity relève l'identité attendue de la carte dans sysfs.
func pciIdentity(c module.Card) (aacraid.PCIIdentity, error) {
	bus, slot, fn, err := parsePCI(c.PCI.Address)
	if err != nil {
		return aacraid.PCIIdentity{}, err
	}
	id := aacraid.PCIIdentity{Bus: bus, Slot: slot, Function: fn}
	for _, f := range []struct {
		s   string
		dst *uint32
	}{{c.PCI.Vendor, &id.Vendor}, {c.PCI.Device, &id.Device}, {c.PCI.SubsystemVendor, &id.SubVendor}, {c.PCI.SubsystemDevice, &id.SubDevice}} {
		v, err := strconv.ParseUint(strings.TrimPrefix(f.s, "0x"), 16, 16)
		if err != nil || !strings.HasPrefix(f.s, "0x") {
			return aacraid.PCIIdentity{}, fmt.Errorf("invalid PCI identifier %q for %s", f.s, c.PCI.Address)
		}
		*f.dst = uint32(v)
	}
	return id, nil
}

func parsePCI(addr string) (bus, slot, fn uint8, err error) {
	var dom, b, s, f uint
	if n, e := fmt.Sscanf(addr, "%04x:%02x:%02x.%1x", &dom, &b, &s, &f); e != nil || n != 4 || f > 7 {
		return 0, 0, 0, fmt.Errorf("invalid PCI address %q", addr)
	}
	return uint8(b), uint8(s), uint8(f), nil
}
