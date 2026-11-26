// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

// Package aacmod est le module du pilote Linux aacraid (Adaptec série 6 et
// apparentées). Il déclare ses nœuds et ses capacités, puis lit la carte au
// travers de la couche de transport aacio.
package aacmod

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
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
	HBAInfo(ctx context.Context) (aacraid.HBAInfo, error)
	SupplementInfo(ctx context.Context) (aacraid.SupplementInfo, error)
	AdapterInfo(ctx context.Context) (aacraid.AdapterInfo, error)
	Paused(ctx context.Context) (bool, error)
	Revision(ctx context.Context) (aacraid.Revision, error)
	Environment(ctx context.Context) (aacraid.Environment, error)
	CacheProtection(ctx context.Context) (bool, aacraid.ZMMStatus, error)
	ContainerCount(ctx context.Context) (aacraid.ContainerCount, error)
	ExpectContainerList(c aacraid.ContainerCount)
	ReadPaged(ctx context.Context, r aacraid.PagedRead, param, size uint32, need func([]byte) int) ([]byte, error)
	ConnectorMap(ctx context.Context) (aacraid.ConnectorMap, error)
	CloseUnconfirmed() int
	SetValidatedCouple(v bool)
	Close() error
}

// reader est le périphérique lié au contexte d'une collecte.
type reader interface {
	HBAInfo() (aacraid.HBAInfo, error)
	SupplementInfo() (aacraid.SupplementInfo, error)
	AdapterInfo() (aacraid.AdapterInfo, error)
	Paused() (bool, error)
	Revision() (aacraid.Revision, error)
	Environment() (aacraid.Environment, error)
	CacheProtection() (bool, aacraid.ZMMStatus, error)
	ContainerCount() (aacraid.ContainerCount, error)
	ExpectContainerList(c aacraid.ContainerCount)
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

// TemperatureCollector est le collecteur du capteur de température de la
// carte (AdapterEnvironmentInfo, si RequestAdapterInfo l'annonce).
const TemperatureCollector = "temperature"

// Module implémente module.Module pour aacraid.
type Module struct {
	open func(path string, dev uint64) (device, error)

	mu sync.Mutex
	// supplement garde, par carte et par nœud, la réponse à
	// RequestSupplementAdapterInfo : elle ne change pas sans réinitialisation
	// de la carte, qui change aussi le nœud. Une lecture en échec n'est pas
	// gardée et sera retentée au cycle suivant.
	supplement map[string]aacraid.SupplementInfo
	// adapterInfo garde de même la réponse à RequestAdapterInfo (703), et
	// revision la réponse acceptée à CheckRevision (701). IsAdapterPaused
	// (704) est envoyée à chaque cycle.
	adapterInfo map[string]aacraid.AdapterInfo
	revision    map[string]aacraid.Revision
	// maxContainers garde de même MaxContainers (CT_GET_CONTAINER_COUNT),
	// qui fixe la taille de la liste des conteneurs.
	maxContainers map[string]aacraid.ContainerCount
	// connectors garde de même la carte des connecteurs (708), fixe tant que
	// la carte n'est pas réinitialisée.
	connectors map[string]keptMap
	// keyOf garde, par adresse PCI, la clé d'identité de la dernière
	// collecte : quand elle change, les réponses gardées sous l'ancienne clé
	// sont oubliées, pour qu'un retour à une identité antérieure (firmware
	// rétrogradé, carte remise) les relise.
	keyOf map[string]string
	// allowAll : option -allow-unqualified=aacraid, toutes les cartes du
	// module lues au-delà de la 706 quel que soit leur modèle.
	allowAll bool
}

// Qualified implémente module.Qualifier : modèle qualifié (QualifiedModel).
func (*Module) Qualified(c module.Card) bool { return QualifiedModel(c) }

// AllowUnqualified active l'option -allow-unqualified=aacraid (CDC, section
// 6.12) : une carte dont le modèle n'est pas qualifié est lue comme les
// autres, selon la règle de conduite (suite de commandes de l'outil, sans
// les écarts E1 et E2). À appeler avant la première collecte.
func (m *Module) AllowUnqualified() { m.allowAll = true }

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
	return append([]string{CapabilitiesCollector, CacheProtectionCollector, TemperatureCollector, DiskInventoryCollector, DiskLocationCollector}, StateCollectors...)
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

func (b bound) HBAInfo() (aacraid.HBAInfo, error) { return b.dev.HBAInfo(b.ctx) }
func (b bound) SupplementInfo() (aacraid.SupplementInfo, error) {
	return b.dev.SupplementInfo(b.ctx)
}
func (b bound) AdapterInfo() (aacraid.AdapterInfo, error) { return b.dev.AdapterInfo(b.ctx) }
func (b bound) Paused() (bool, error)                     { return b.dev.Paused(b.ctx) }
func (b bound) Revision() (aacraid.Revision, error)       { return b.dev.Revision(b.ctx) }
func (b bound) Environment() (aacraid.Environment, error) { return b.dev.Environment(b.ctx) }
func (b bound) CacheProtection() (bool, aacraid.ZMMStatus, error) {
	return b.dev.CacheProtection(b.ctx)
}
func (b bound) ContainerCount() (aacraid.ContainerCount, error) {
	return b.dev.ContainerCount(b.ctx)
}
func (b bound) ExpectContainerList(c aacraid.ContainerCount) { b.dev.ExpectContainerList(c) }
func (b bound) ReadPaged(r aacraid.PagedRead, param, size uint32, need func([]byte) int) ([]byte, error) {
	return b.dev.ReadPaged(b.ctx, r, param, size, need)
}
func (b bound) ConnectorMap() (aacraid.ConnectorMap, error) { return b.dev.ConnectorMap(b.ctx) }

// Collect ouvre le nœud préparé, lit FSACTL_GET_HBA_INFO (réponse du pilote,
// sans commande firmware) et vérifie qu'il désigne bien cette carte. Sur un
// modèle qualifié ou une carte désignée par -allow-unqualified, il ouvre
// ensuite la carte comme l'outil de référence (704, 701, 703, puis 706 si
// la 703 l'annonce) ; si la carte n'annonce pas l'interface des lectures
// d'état, les collecteurs d'état sont déclarés non pris en charge. Une fois
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
	// Écarts E1 et E2 : seulement sur un couple modèle et firmware validé.
	validated := ValidatedCouple(c)
	raw.SetValidatedCouple(validated)
	// Réponses gardées (706, 708) : rattachées à l'adresse, au nœud et à
	// l'identité matérielle de la carte (module.Card.Identity : numéro de
	// série, identifiants PCI, modèle, firmware). Une carte remplacée ou un
	// firmware mis à jour change la clé : tout est relu. Identité
	// incomplète : deux cartes successives pourraient partager la clé ; rien
	// n'est gardé, les réponses sont relues à chaque cycle.
	fields, complete := c.Identity()
	key := ""
	if complete {
		key = strings.Join(append([]string{c.PCI.Address, strconv.FormatUint(refs[0].Dev, 10)}, fields...), "|")
	}
	m.forgetOtherKeys(c.PCI.Address, key)
	// Toute commande firmware : seulement vers un modèle qualifié (exigence
	// S4) ou une carte désignée par -allow-unqualified (S4 bis). Toute autre
	// carte ne reçoit rien au-delà de GET_HBA_INFO (réponse du pilote) ; ses
	// collecteurs sont publiés non pris en charge. Ce classement ne dépend
	// que de sysfs et de l'option.
	all := append([]string{CapabilitiesCollector}, append(append([]string(nil), StateCollectors...), DiskInventoryCollector, DiskLocationCollector, CacheProtectionCollector, TemperatureCollector)...)
	if !QualifiedModel(c) && !m.allowAll {
		r.Unsupported = all
		return r
	}
	r.Collected = map[string]bool{}
	// Ouverture comme l'outil de référence : 704 (carte en pause : l'outil
	// ne l'ouvre pas), 701 (révision refusée : idem), 703, puis 706 si la
	// 703 l'annonce. Un échec arrête la collecte de la carte pour ce cycle :
	// aucune autre commande, tous les collecteurs en échec.
	fail := func(err error) module.Result {
		r.CollectErr = fmt.Errorf("%w; no other card read attempted this cycle", err)
		for _, col := range all {
			r.Collected[col] = false
		}
		return r
	}
	if paused, err := d.Paused(); err != nil {
		return fail(fmt.Errorf("IsAdapterPaused: %w", err))
	} else if paused {
		return fail(aacraid.ErrAdapterPaused)
	}
	if rev, err := m.revisionFor(key, d); err != nil {
		return fail(fmt.Errorf("CheckRevision: %w", err))
	} else if !rev.Accepted() {
		return fail(fmt.Errorf("%w: compatible %v, firmware build %d", aacraid.ErrRevisionRefused, rev.Compatible, rev.FirmwareBuild))
	}
	ainfo, err := m.adapterInfoFor(key, d)
	if err != nil {
		return fail(fmt.Errorf("RequestAdapterInfo: %w", err))
	}
	// Réponses gardées en mémoire : pas de date de dernier succès.
	if key != "" {
		r.Cached = []string{CapabilitiesCollector}
	}
	var sup aacraid.SupplementInfo
	if ainfo.SupplementInfo() {
		sup, err = m.supplementInfo(key, d)
		if err != nil {
			return fail(fmt.Errorf("RequestSupplementAdapterInfo: %w", err))
		}
		r.Collected[CapabilitiesCollector] = true
	} else {
		// 706 non annoncée : ni l'outil ni le pilote ne l'envoient ; les
		// lectures d'état, qu'elle seule annonce, ne sont pas prises en charge.
		r.Unsupported = append(r.Unsupported, CapabilitiesCollector)
		r.Cached = nil
	}
	if !sup.ArcIoReads() {
		r.Unsupported = append(append(r.Unsupported, StateCollectors...), DiskInventoryCollector, DiskLocationCollector)
	}
	m.collectTemperature(&r, d, ainfo)
	// L'outil de référence lit la protection du cache sur une carte qu'il a
	// ouverte, après ses informations générales (CDC, section 6.11).
	collectCacheProtection(&r, d)
	if sup.ArcIoReads() {
		_, h, _ := hostOf(c)
		m.collectArcIo(&r, d, key, h, sup, ainfo)
	}
	return r
}

// pagedCollectors sont les collecteurs qui reposent sur des transferts
// paginés : états des volumes et des disques, inventaire et emplacement des
// disques (la 708 n'est lue qu'après un inventaire réussi).
func pagedCollectors() []string {
	return append(append([]string(nil), StateCollectors...), DiskInventoryCollector, DiskLocationCollector)
}

// collectArcIo applique les prérequis des transferts paginés (CDC, section
// 6.11), puis lit volumes et disques. Formes à bit de poids fort (bit 13 de
// FeatureBits) ou en blocs de 2 048 octets (bit 15 de SupportedOptions) :
// non implémentées, collecteurs non pris en charge, rien n'est envoyé. La
// 703 est toujours lue à ce stade (ouverture de la carte).
func (m *Module) collectArcIo(r *module.Result, d reader, key string, h sysfs.SCSIHost, sup aacraid.SupplementInfo, ainfo aacraid.AdapterInfo) {
	switch {
	case sup.PagedHighBit():
		r.Unsupported = append(r.Unsupported, pagedCollectors()...)
		return
	case ainfo.LargeTransfers():
		r.Unsupported = append(r.Unsupported, pagedCollectors()...)
		return
	}
	caps := aacraid.DiskCaps{DirectDisks: sup.DirectDisks(), JBOD: sup.JBOD(), NativeHBA: ainfo.NativeHBA(), NativeKnown: true}
	m.collectVolumes(r, d, key, h, sup.ContainerReads(), sup.ConnectorMapRead(), caps)
}

// Qualification est un modèle (quatre identifiants PCI) dont les lectures
// ont été vérifiées contre l'outil de référence (exigence S4 ;
// docs/aacraid-interface.md, section 8). Firmwares liste les versions de ce
// modèle validées sur matériel : seul un couple modèle et firmware validé
// reçoit les écarts E1 et E2.
type Qualification struct {
	Model                                            string   // nom commercial (documentation)
	Vendor, Device, SubsystemVendor, SubsystemDevice string   // tels que publiés par sysfs
	Firmwares                                        []string // hba_kernel_version validés
}

// Qualifications liste les modèles qualifiés ; docs/aacraid-interface.md
// (section 8) en reprend chaque entrée (vérifié par un test).
var Qualifications = []Qualification{
	// Hôte de référence, 2026-11-23.
	{Model: "Adaptec 6805", Vendor: "0x9005", Device: "0x028b", SubsystemVendor: "0x9005", SubsystemDevice: "0x0301", Firmwares: []string{"5.3-0[19204]"}},
}

// qualification rend l'entrée du modèle de la carte.
func qualification(c module.Card) (Qualification, bool) {
	for _, q := range Qualifications {
		if c.PCI.Vendor == q.Vendor && c.PCI.Device == q.Device && c.PCI.SubsystemVendor == q.SubsystemVendor && c.PCI.SubsystemDevice == q.SubsystemDevice {
			return q, true
		}
	}
	return Qualification{}, false
}

// QualifiedModel indique si le modèle de la carte est qualifié (identifiants
// PCI ; le firmware n'est pas une condition de lecture).
func QualifiedModel(c module.Card) bool {
	_, ok := qualification(c)
	return ok
}

// ValidatedCouple indique si la carte est un couple modèle et firmware
// validé sur matériel.
func ValidatedCouple(c module.Card) bool {
	q, ok := qualification(c)
	if !ok {
		return false
	}
	_, h, err := hostOf(c)
	return err == nil && h.Firmware != "" && slices.Contains(q.Firmwares, h.Firmware)
}

// hostOf renvoie l'hôte SCSI aacraid de la carte.
func hostOf(c module.Card) (int, sysfs.SCSIHost, error) {
	i, _, err := host(c)
	if err != nil {
		return 0, sysfs.SCSIHost{}, err
	}
	return i, c.Hosts[i], nil
}

// collectVolumes lit les deux chaînes de lecture, dans l'ordre de l'outil de
// référence repris par l'exporter (229, les 234, 247, les 249, puis les
// 248 et la 708), avec les dépendances de la section 6.11 du CDC :
//   - chaîne des volumes (comptage, 229, les 234), seulement si la carte
//     annonce les lectures de conteneurs (bit 9 de FeatureBits) ; sans lui,
//     les collecteurs d'état sont non pris en charge ;
//   - chaîne des disques (247, les 248, la 708), lue même si celle des
//     volumes échoue ; la 708 seulement si la carte l'annonce (bit 8 de
//     FeatureBits), sinon l'emplacement est non pris en charge ;
//   - listes des membres (249) : seulement si les deux chaînes ont réussi,
//     car les états utilisent la présence des disques.
//
// Toute erreur annule la publication de ce qu'elle concerne pour ce cycle :
// jamais de configuration ni d'inventaire partiels.
func (m *Module) collectVolumes(r *module.Result, d reader, key string, h sysfs.SCSIHost, containers, connectors bool, caps aacraid.DiskCaps) {
	var cs []aacraid.Container
	var volErr error
	// Sans carte des connecteurs annoncée, l'emplacement est non pris en
	// charge, quel que soit le sort des autres lectures.
	if !connectors {
		r.Unsupported = append(r.Unsupported, DiskLocationCollector)
	}
	failLocation := func() {
		if connectors {
			r.Collected[DiskLocationCollector] = false
		}
	}
	if containers {
		if cs, volErr = m.readContainers(d, key); volErr != nil {
			volErr = fmt.Errorf("containers: %w", volErr)
		}
	} else {
		r.Unsupported = append(r.Unsupported, StateCollectors...)
	}
	present, diskErr := readDiskList(d)
	if diskErr != nil {
		diskErr = fmt.Errorf("disk list: %w", diskErr)
	}
	if containers && volErr == nil && diskErr == nil {
		if err := readMembers(d, cs); err != nil {
			volErr = fmt.Errorf("member lists: %w", err)
		}
	}
	states := containers && volErr == nil && diskErr == nil
	if containers {
		for _, col := range StateCollectors {
			r.Collected[col] = states
		}
	}
	if volErr != nil {
		r.CollectErr = errors.Join(r.CollectErr, fmt.Errorf("configuration: %w", volErr))
	}
	if diskErr != nil {
		r.Collected[DiskInventoryCollector] = false
		failLocation()
		r.CollectErr = errors.Join(r.CollectErr, fmt.Errorf("configuration: %w; disk inventory and location not read", diskErr))
		return
	}
	// L'inventaire (248) est lu avant le calcul des états : un disque qu'il
	// déclare en panne compte comme membre perdu, comme dans l'outil de
	// référence. Chaque réponse lue est une preuve, même si une lecture
	// suivante échoue ; les disques non lus sont évalués par les drapeaux de
	// leurs membres et la liste des disques présents. L'échec reste visible
	// par le collecteur de l'inventaire.
	inv, err := readInventory(d, present)
	if states {
		m.publishStates(r, cs, present, inv, err == nil, caps)
	}
	r.Collected[DiskInventoryCollector] = err == nil
	if err != nil {
		failLocation()
		r.CollectErr = errors.Join(r.CollectErr, fmt.Errorf("disk inventory: %w; disk location not read", err))
		return
	}
	// Inventaire, périphérique Linux et emplacement : disques seulement. Un
	// autre périphérique de la liste (boîtier, support amovible) n'est pas
	// publié, comme pour l'état (« disque » a le même sens partout).
	hard := make([]aacraid.DiskInfo, 0, len(inv))
	for _, i := range inv {
		if i.HardDrive() {
			hard = append(hard, i)
		}
	}
	for _, i := range hard {
		r.DiskInventory = append(r.DiskInventory, module.DiskInfo{ID: diskID(i.Number), Vendor: i.Vendor, Model: i.Model,
			Firmware: i.Firmware, Serial: i.Serial, WWN: i.WWN, CapacityBytes: i.CapacityBytes()})
	}
	r.DiskOSDevices = osDevices(h, hard)
	if !connectors {
		return
	}
	locs, err := m.locations(key, d, hard)
	r.Collected[DiskLocationCollector] = err == nil
	if err != nil {
		r.CollectErr = errors.Join(r.CollectErr, fmt.Errorf("disk location: %w", err))
		return
	}
	r.DiskLocations = locs
}

// publishStates calcule les états des volumes et des disques à partir de la
// configuration et des informations de disque lues (invOK : toutes lues).
func (m *Module) publishStates(r *module.Result, cs []aacraid.Container, present []uint32, inv []aacraid.DiskInfo, invOK bool, caps aacraid.DiskCaps) {
	failed := map[uint32]bool{}
	complete := invOK
	for _, i := range inv {
		// Un périphérique qui n'est pas un disque compte, pour un membre
		// qui le désigne, comme un disque en panne (outil de référence).
		if i.Failed() || !i.HardDrive() {
			failed[i.Number] = true
		}
		complete = complete && i.HasStatus332
	}
	ev := aacraid.Evaluate(cs, present, failed)
	r.Volumes, r.Disks = volumesOf(cs, ev), disksOf(ev)
	if complete {
		// Avec l'information complète de chaque disque (octets 332-335
		// compris) : état de tous les disques présents, membres ou non
		// (section 6 bis). Sans elle : états déduits des membres seulement,
		// plutôt qu'un état « prêt » qui ignorerait « non pris en charge ».
		byNum := make(map[uint32]aacraid.DiskInfo, len(inv))
		for _, i := range inv {
			byNum[i.Number] = i
		}
		r.Disks = diskStatesOf(aacraid.DiskStates(cs, present, byNum, caps))
		return
	}
	// Information absente, partielle ou incomplète : états déduits des
	// membres, et tout disque qu'une réponse lue déclare en panne est
	// publié en panne, membre ou non (plus prudent que de ne rien publier
	// pour lui ; un périphérique qui n'est pas un disque reste hors des
	// disques).
	st := map[uint32]aacraid.DiskStatus{}
	for n, ds := range ev.Disks {
		st[n] = aacraid.DiskStatus{State: ds}
	}
	for _, i := range inv {
		if i.Failed() && i.HardDrive() {
			st[i.Number] = aacraid.DiskStatus{State: aacraid.DiskFailed}
		}
	}
	r.Disks = diskStatesOf(st)
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
	if err != nil || key == "" { // key vide : rien n'est gardé
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
// de chaque réponse est lu (DiskInventoryNeed), puis le transfert est clos.
//
// En cas d'erreur, readInventory rend aussi les réponses déjà lues et
// vérifiées : elles ne servent pas à l'inventaire (tout ou rien), mais leur
// bit de panne reste une preuve pour le calcul des états.
func readInventory(d reader, nums []uint32) ([]aacraid.DiskInfo, error) {
	sorted := append([]uint32(nil), nums...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	out := make([]aacraid.DiskInfo, 0, len(sorted))
	for _, n := range sorted {
		raw, err := d.ReadPaged(aacraid.ReadDiskInfo, n, 0, func([]byte) int { return aacraid.DiskInventoryNeed })
		if err != nil {
			return out, err
		}
		i, err := aacraid.DecodeDiskInfo(raw)
		if err != nil {
			return out, err
		}
		if i.Number != n {
			return out, fmt.Errorf("disk %d info: the response is for disk %d", n, i.Number)
		}
		out = append(out, i)
	}
	return out, nil
}

// readContainers lit la chaîne des volumes : MaxContainers, la liste des
// conteneurs à la taille qu'il fixe (8 + 4 × MaxContainers, comme l'outil de
// référence ; CDC, section 6.11, forme 1), puis l'information de chacun.
func (m *Module) readContainers(d reader, key string) ([]aacraid.Container, error) {
	count, err := m.containerCount(key, d)
	if err != nil {
		return nil, fmt.Errorf("container count: %w", err)
	}
	maxContainers := count.Max()
	// Taille liée au comptage validé de cette carte, vérifiée par le
	// transport au moment de l'envoi.
	d.ExpectContainerList(count)
	// Une liste qui annonce plus d'entrées que MaxContainers est refusée dès
	// le morceau qui porte ce nombre : need rend alors plus que tout
	// transfert accepté, et le transport arrête la lecture sans demander de
	// morceau suivant.
	var announced uint32
	need := func(b []byte) int {
		if len(b) >= 8 {
			if announced = binary.LittleEndian.Uint32(b[4:]); announced > maxContainers {
				return math.MaxInt32
			}
		}
		return aacraid.ContainerListNeed(b)
	}
	raw, err := d.ReadPaged(aacraid.ReadContainerList, 0, count.ListSize(), need)
	if announced > maxContainers {
		return nil, fmt.Errorf("container list: %d entries, MaxContainers %d", announced, maxContainers)
	}
	if err != nil {
		return nil, err
	}
	nums, err := aacraid.DecodeContainerList(raw)
	if err != nil {
		return nil, err
	}
	var cs []aacraid.Container
	for _, n := range nums {
		raw, err := d.ReadPaged(aacraid.ReadContainerInfo, n, 0, func([]byte) int { return aacraid.ContainerInfoNeed })
		if err != nil {
			return nil, err
		}
		info, err := aacraid.DecodeContainerInfo(raw)
		if err != nil {
			return nil, err
		}
		for _, prev := range cs {
			if prev.Info.UID == info.UID {
				return nil, fmt.Errorf("duplicate container UID %#x (containers %d and %d)", info.UID, prev.Number, n)
			}
		}
		cs = append(cs, aacraid.Container{Number: n, Info: info})
	}
	return cs, nil
}

// readDiskList lit la liste des disques présents (247).
func readDiskList(d reader) ([]uint32, error) {
	raw, err := d.ReadPaged(aacraid.ReadDiskList, 0, 0, aacraid.DiskListNeed)
	if err != nil {
		return nil, err
	}
	return aacraid.DecodeDiskList(raw)
}

// readMembers lit la liste des membres de chaque conteneur (249).
func readMembers(d reader, cs []aacraid.Container) error {
	for i := range cs {
		size := aacraid.MemberListSize(cs[i].Info.Members)
		raw, err := d.ReadPaged(aacraid.ReadMemberList, cs[i].Info.UID, size, func([]byte) int { return int(size) })
		if err != nil {
			return err
		}
		if cs[i].Members, err = aacraid.DecodeMemberList(raw); err != nil {
			return err
		}
		if len(cs[i].Members) != cs[i].Info.Members {
			return fmt.Errorf("container %#x: %d members announced, %d listed", cs[i].Info.UID, cs[i].Info.Members, len(cs[i].Members))
		}
		seen := map[[2]uint32]bool{}
		for _, mb := range cs[i].Members {
			k := [2]uint32{mb.Disk, mb.ChildUID}
			if seen[k] {
				return fmt.Errorf("container %#x: member listed twice (disk %#x, container %#x)", cs[i].Info.UID, mb.Disk, mb.ChildUID)
			}
			seen[k] = true
		}
	}
	return nil
}

func uidID(uid uint32) string { return fmt.Sprintf("%08x", uid) }

func volumesOf(cs []aacraid.Container, ev aacraid.Evaluation) []module.Volume {
	out := make([]module.Volume, 0, len(cs))
	byUID := make(map[uint32]aacraid.ContainerInfo, len(cs))
	for _, c := range cs {
		byUID[c.Info.UID] = c.Info
	}
	for _, c := range cs {
		v := module.Volume{
			ID:            uidID(c.Info.UID),
			Name:          c.Info.Name,
			CapacityBytes: c.Info.Blocks * 512,
			State:         string(ev.Volumes[c.Info.UID]),
		}
		// Niveau normalisé et libellé de l'outil constructeur
		// (docs/aacraid-interface.md, section 6 bis) ; traitements
		// particuliers de l'outil : unknown, jamais deviné.
		lv := aacraid.ContainerLevel(c, byUID)
		v.RaidLevel, v.VendorLevel = lv.Raid, lv.Vendor
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

// diskStatesOf trie les états de disque par numéro.
func diskStatesOf(st map[uint32]aacraid.DiskStatus) []module.Disk {
	nums := make([]uint32, 0, len(st))
	for n := range st {
		nums = append(nums, n)
	}
	sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })
	out := make([]module.Disk, 0, len(nums))
	for _, n := range nums {
		out = append(out, module.Disk{ID: diskID(n), State: string(st[n].State), VendorState: st[n].Vendor})
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

// collectTemperature lit le capteur de température, comme l'outil de
// référence, seulement si la carte l'annonce (AAC_SUPPORTED_HEAT_SENSOR de
// RequestAdapterInfo) ; sinon le collecteur n'est pas pris en charge.
func (m *Module) collectTemperature(r *module.Result, d reader, info aacraid.AdapterInfo) {
	if !info.HeatSensor() {
		r.Unsupported = append(r.Unsupported, TemperatureCollector)
		return
	}
	env, err := d.Environment()
	r.Collected[TemperatureCollector] = err == nil
	if err != nil {
		r.CollectErr = errors.Join(r.CollectErr, fmt.Errorf("temperature: %w", err))
		return
	}
	r.Temperature = &module.Temperature{Celsius: float64(env.TemperatureCelsius),
		ThresholdLo: float64(env.ThresholdLoCelsius), ThresholdHi: float64(env.ThresholdHiCelsius)}
}

// revisionFor lit la réponse 701 une seule fois par carte et par nœud, sous
// la même clé que la 703 ; seule une révision acceptée est gardée.
func (m *Module) revisionFor(key string, d reader) (aacraid.Revision, error) {
	m.mu.Lock()
	rv, ok := m.revision[key]
	m.mu.Unlock()
	if ok {
		return rv, nil
	}
	rv, err := d.Revision()
	if err != nil || key == "" || !rv.Accepted() {
		return rv, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.revision == nil {
		m.revision = map[string]aacraid.Revision{}
	}
	m.revision[key] = rv
	return rv, nil
}

// adapterInfoFor lit la réponse 703 une seule fois par carte et par nœud,
// sous la même clé que la 706 (rien n'est gardé sous une clé vide).
func (m *Module) adapterInfoFor(key string, d reader) (aacraid.AdapterInfo, error) {
	m.mu.Lock()
	a, ok := m.adapterInfo[key]
	m.mu.Unlock()
	if ok {
		return a, nil
	}
	a, err := d.AdapterInfo()
	if err != nil || key == "" {
		return a, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.adapterInfo == nil {
		m.adapterInfo = map[string]aacraid.AdapterInfo{}
	}
	m.adapterInfo[key] = a
	return a, nil
}

// supplementInfo lit la réponse 706 une seule fois par carte et par nœud.
// forgetOtherKeys oublie les réponses gardées pour addr sous une autre clé
// que key (identité de la carte changée depuis la dernière collecte, ou
// devenue incomplète : key vide).
func (m *Module) forgetOtherKeys(addr, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.keyOf == nil {
		m.keyOf = map[string]string{}
	}
	if old, ok := m.keyOf[addr]; ok && old != key {
		delete(m.supplement, old)
		delete(m.adapterInfo, old)
		delete(m.revision, old)
		delete(m.maxContainers, old)
		delete(m.connectors, old)
	}
	m.keyOf[addr] = key
}

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
	if err != nil || key == "" { // key vide : rien n'est gardé
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

// containerCount rend MaxContainers, gardé comme la réponse 706 ; une lecture
// en échec n'est pas gardée.
func (m *Module) containerCount(key string, d reader) (aacraid.ContainerCount, error) {
	m.mu.Lock()
	n, ok := m.maxContainers[key]
	m.mu.Unlock()
	if ok {
		return n, nil
	}
	n, err := d.ContainerCount()
	if err != nil || key == "" {
		return n, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.maxContainers == nil {
		m.maxContainers = map[string]aacraid.ContainerCount{}
	}
	m.maxContainers[key] = n
	return n, nil
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
