// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacmod

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DisasteR/hwraid_exporter/internal/aacraid"
	"github.com/DisasteR/hwraid_exporter/internal/module"
	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
	"github.com/DisasteR/hwraid_exporter/internal/transport/aacio"
)

// Le module doit satisfaire le contrat.
var _ module.Module = (*Module)(nil)

// card construit une Adaptec 6805 au firmware qualifié (hôte de référence).
func card(addr, uid string) module.Card {
	return module.Card{
		PCI: sysfs.PCIFunction{Address: addr, Driver: "aacraid", Class: "0x010400",
			Vendor: "0x9005", Device: "0x028b", SubsystemVendor: "0x9005", SubsystemDevice: "0x0301"},
		Hosts: []sysfs.SCSIHost{{Name: "host3", Number: 3, ProcName: "aacraid", UniqueID: uid, Firmware: "5.3-0[19204]", SerialNumber: "TESTSERIAL", Model: "6805"}},
	}
}

func TestMatchAndCapabilities(t *testing.T) {
	m := &Module{}
	if !m.Match(card("0000:01:00.0", "0")) {
		t.Fatal("carte aacraid non retenue")
	}
	if m.Match(module.Card{PCI: sysfs.PCIFunction{Driver: "megaraid_sas"}}) {
		t.Fatal("carte d'un autre pilote retenue")
	}
	if m.Capabilities() != 1<<17 {
		t.Fatalf("capacités %#x, attendu CAP_SYS_RAWIO", m.Capabilities())
	}
}

// Le minor suit le unique_id, pas le numéro d'hôte (host3 ici).
func TestNodesFollowUniqueID(t *testing.T) {
	reqs, err := (&Module{}).Nodes(card("0000:01:00.0", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 || reqs[0] != (module.NodeRequest{Name: "aac1", MajorOf: "aac", Minor: 1}) {
		t.Fatalf("déclaration : %+v", reqs)
	}
	for _, bad := range []string{"", "x", "256", "-1"} {
		if _, err := (&Module{}).Nodes(card("0000:01:00.0", bad)); err == nil {
			t.Fatalf("unique_id %q accepté", bad)
		}
	}
	if _, err := (&Module{}).Nodes(module.Card{PCI: sysfs.PCIFunction{Driver: "aacraid"}}); err == nil {
		t.Fatal("carte sans hôte aacraid acceptée")
	}
}

type fakeDev struct {
	// validated : dernière valeur reçue par SetValidatedCouple.
	validated, validatedSet bool
	info                    aacraid.HBAInfo
	err                     error
	sup                     aacraid.SupplementInfo
	supErr                  error
	supCall                 int
	supWait                 chan struct{} // si non nil, SupplementInfo attend sa fermeture
	started                 atomic.Int32  // appels à SupplementInfo commencés (lecture concurrente)
	closed                  bool
	cp                      *cpAnswer
	cpCall                  int
	paged                   func(r aacraid.PagedRead, param, size uint32) ([]byte, error)
	reads                   []string
	cmap                    func() (aacraid.ConnectorMap, error) // nil : réponse de référence
	cmCall                  int
	closeOther              int // clôtures 0xc5 simulées
	refused                 int // commandes refusées après la fin du contexte
	// ai : réponse 703 (nil : capteur annoncé, comme la 6805 de référence) ;
	// env : réponse 803 (nil : 49 °C, seuils 95 et 100, valeurs capturées).
	ai      *aacraid.AdapterInfo
	aiErr   error
	aiCall  int
	env     *aacraid.Environment
	envErr  error
	envCall int
	// count : MaxContainers (0 : 256, valeur de la carte de référence).
	count     uint32
	countErr  error
	countCall int
	expected  aacraid.ContainerCount // comptage fixé par ExpectContainerList
	chunks    map[string]int         // morceaux servis (432 octets), par lecture
	// Ouverture : 704 (paused, pausedErr) et 701 (rev : nil, réponse de la
	// carte de référence, compatible, build 19204).
	paused     bool
	pausedErr  error
	pausedCall int
	rev        *aacraid.Revision
	revErr     error
	revCall    int
}

func (f *fakeDev) Paused(ctx context.Context) (bool, error) {
	if f.stopped(ctx) {
		return false, aacio.ErrStopped
	}
	f.pausedCall++
	return f.paused, f.pausedErr
}

func (f *fakeDev) Revision(ctx context.Context) (aacraid.Revision, error) {
	if f.stopped(ctx) {
		return aacraid.Revision{}, aacio.ErrStopped
	}
	f.revCall++
	if f.rev != nil {
		return *f.rev, f.revErr
	}
	return aacraid.Revision{Compatible: true, FirmwareBuild: 19204}, f.revErr
}

func (f *fakeDev) maxContainers() uint32 {
	if f.count == 0 {
		return 256
	}
	return f.count
}

func (f *fakeDev) ExpectContainerList(c aacraid.ContainerCount) { f.expected = c }

func (f *fakeDev) ContainerCount(ctx context.Context) (aacraid.ContainerCount, error) {
	if f.stopped(ctx) {
		return aacraid.ContainerCount{}, aacio.ErrStopped
	}
	f.countCall++
	if f.countErr != nil {
		return aacraid.ContainerCount{}, f.countErr
	}
	return countOf(f.maxContainers())
}

// countOf décode une réponse CT_GET_CONTAINER_COUNT construite pour n
// conteneurs : seule voie pour obtenir un comptage utilisable.
func countOf(n uint32) (aacraid.ContainerCount, error) {
	b := make([]byte, aacraid.FIBSize)
	le := binary.LittleEndian
	le.PutUint16(b[4:], 500)
	le.PutUint16(b[8:], aacraid.ChunkRspSize)
	le.PutUint32(b[aacraid.FIBHeaderSize+4:], 4)
	le.PutUint32(b[aacraid.FIBHeaderSize+8:], n)
	return aacraid.DecodeContainerCount(b)
}

func (f *fakeDev) AdapterInfo(ctx context.Context) (aacraid.AdapterInfo, error) {
	if f.stopped(ctx) {
		return aacraid.AdapterInfo{}, aacio.ErrStopped
	}
	f.aiCall++
	if f.ai != nil {
		return *f.ai, f.aiErr
	}
	return aacraid.AdapterInfo{SupportedOptions: 0x141f1d7c}, f.aiErr
}

func (f *fakeDev) Environment(ctx context.Context) (aacraid.Environment, error) {
	if f.stopped(ctx) {
		return aacraid.Environment{}, aacio.ErrStopped
	}
	f.envCall++
	if f.env != nil {
		return *f.env, f.envErr
	}
	return aacraid.Environment{TemperatureCelsius: 49, ThresholdLoCelsius: 95, ThresholdHiCelsius: 100}, f.envErr
}

// stopped imite l'admission du transport (aacio) : une fois le contexte
// terminé, la commande n'est pas envoyée.
func (f *fakeDev) stopped(ctx context.Context) bool {
	if ctx.Err() != nil {
		f.refused++
		return true
	}
	return false
}

func (f *fakeDev) ConnectorMap(ctx context.Context) (aacraid.ConnectorMap, error) {
	if f.stopped(ctx) {
		return aacraid.ConnectorMap{}, aacio.ErrStopped
	}
	f.cmCall++
	if f.cmap != nil {
		return f.cmap()
	}
	return referenceConnectorMap()
}

// referenceConnectorMap décode la réponse 708 enregistrée sur l'hôte de
// référence (adresse SAS de la carte remplacée).
func referenceConnectorMap() (aacraid.ConnectorMap, error) {
	s, err := os.ReadFile("../../aacraid/testdata/708.hex")
	if err != nil {
		return aacraid.ConnectorMap{}, err
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(s)))
	if err != nil {
		return aacraid.ConnectorMap{}, err
	}
	b := make([]byte, aacraid.LargeFIBSize)
	copy(b, raw)
	return aacraid.DecodeConnectorMap(b)
}

func (f *fakeDev) HBAInfo(ctx context.Context) (aacraid.HBAInfo, error) {
	if err := ctx.Err(); err != nil {
		return aacraid.HBAInfo{}, errors.Join(aacio.ErrStopped, err) // comme l'admission du transport
	}
	return f.info, f.err
}
func (f *fakeDev) SupplementInfo(ctx context.Context) (aacraid.SupplementInfo, error) {
	if f.stopped(ctx) {
		return aacraid.SupplementInfo{}, aacio.ErrStopped
	}
	f.supCall++
	f.started.Add(1)
	if f.supWait != nil {
		<-f.supWait
	}
	return f.sup, f.supErr
}
func (f *fakeDev) Close() error              { f.closed = true; return nil }
func (f *fakeDev) CloseUnconfirmed() int     { return f.closeOther }
func (f *fakeDev) SetValidatedCouple(v bool) { f.validated, f.validatedSet = v, true }

func (f *fakeDev) CacheProtection(ctx context.Context) (bool, aacraid.ZMMStatus, error) {
	if f.stopped(ctx) {
		return false, aacraid.ZMMStatus{}, aacio.ErrStopped
	}
	f.cpCall++
	if f.cp == nil {
		return true, aacraid.ZMMStatus{Word: 0x40000081, State: aacraid.CacheProtectionFailed}, nil
	}
	return f.cp.described, f.cp.st, f.cp.err
}

func (f *fakeDev) ReadPaged(ctx context.Context, r aacraid.PagedRead, param, size uint32, need func([]byte) int) ([]byte, error) {
	if f.stopped(ctx) {
		return nil, aacio.ErrStopped
	}
	f.reads = append(f.reads, fmt.Sprintf("%d-%x", r, param))
	// Comme le transport : la liste des conteneurs est demandée à la taille
	// que fixe MaxContainers.
	if r == aacraid.ReadContainerList && (size != aacraid.ContainerListSize(f.maxContainers()) || f.expected.Max() != f.maxContainers()) {
		return nil, fmt.Errorf("liste des conteneurs : taille demandée %d, %d attendue (ExpectContainerList : %d)", size, aacraid.ContainerListSize(f.maxContainers()), f.expected.Max())
	}
	var b []byte
	var err error
	if f.paged == nil {
		b, err = referencePaged(r, param, size)
	} else {
		b, err = f.paged(r, param, size)
	}
	if err != nil {
		return b, err
	}
	// Comme le transport réel : des morceaux entiers de 432 octets, jusqu'à
	// couvrir ce que demande need. Un besoin sous-estimé par le module se
	// voit donc dans les tests.
	n := 0
	if f.chunks == nil {
		f.chunks = map[string]int{}
	}
	// Comme le transport, la lecture s'arrête aussi quand need demande plus
	// que le transfert entier.
	for n < len(b) && (n == 0 || (need(b[:n]) > n && need(b[:n]) <= len(b))) {
		n = min(n+aacraid.ChunkData, len(b))
		f.chunks[fmt.Sprintf("%d-%x", r, param)]++
	}
	if want := need(b[:n]); want > n {
		return nil, fmt.Errorf("read %d: %d bytes needed, transfer of %d", r, want, n)
	}
	return b[:n], nil
}

// referencePaged sert les données enregistrées sur l'hôte de référence
// (internal/aacraid/testdata), et vérifie la taille demandée pour une liste
// de membres.
func referencePaged(r aacraid.PagedRead, param, size uint32) ([]byte, error) {
	b, err := referenceRaw(r, param)
	if err != nil {
		return nil, err
	}
	return checkMemberListSize(r, size, b)
}

// referenceRaw sert les données enregistrées, sans contrôle.
func referenceRaw(r aacraid.PagedRead, param uint32) ([]byte, error) {
	s, err := os.ReadFile(fmt.Sprintf("../../aacraid/testdata/%d-%x.hex", r, param))
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(strings.TrimSpace(string(s)))
}

// checkMemberListSize : comme la carte, une liste des membres a exactement
// la taille annoncée par la 234 et demandée par le module.
func checkMemberListSize(r aacraid.PagedRead, size uint32, b []byte) ([]byte, error) {
	if r == aacraid.ReadMemberList && uint32(len(b)) != size {
		return nil, fmt.Errorf("taille demandée %d, liste de %d octets", size, len(b))
	}
	return b, nil
}

type cpAnswer struct {
	described bool
	st        aacraid.ZMMStatus
	err       error
}

// arcIo : lectures ARCIO (SupportedOptions2 de la carte de référence),
// carte des connecteurs et lectures de conteneurs (bits 8 et 9 de
// FeatureBits), sans autre bit.
var arcIo = aacraid.SupplementInfo{Version: 15, SupportedOptions2: 0x00c8693c, FeatureBits: aacraid.FeatureConnectorMap | aacraid.FeatureContainerReads}

func withDev(d *fakeDev, openErr error) *Module {
	return &Module{open: func(string, uint64) (device, error) {
		if openErr != nil {
			return nil, openErr
		}
		return d, nil
	}}
}

func TestCollect(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	c := card("0000:01:00.0", "0")

	ok := &fakeDev{info: aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, Device: 0, Function: 0, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}, sup: arcIo}
	if r := withDev(ok, nil).Collect(context.Background(), c, ref); !r.NodeValid || r.Err != nil || !ok.closed {
		t.Fatalf("carte cohérente : %+v (fermé=%v)", r, ok.closed)
	}
	other := &fakeDev{info: aacraid.HBAInfo{AdapterNumber: 1, Bus: 2, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}}
	if r := withDev(other, nil).Collect(context.Background(), c, ref); r.NodeValid || r.Err == nil || !other.closed {
		t.Fatalf("nœud d'une autre carte jugé valide : %+v", r)
	}
	failing := &fakeDev{err: errors.New("EPERM")}
	if r := withDev(failing, nil).Collect(context.Background(), c, ref); r.NodeValid || r.Err == nil || !failing.closed {
		t.Fatalf("échec de lecture non remonté : %+v", r)
	}
	if r := withDev(nil, errors.New("nœud incohérent")).Collect(context.Background(), c, ref); r.NodeValid || r.Err == nil {
		t.Fatalf("échec d'ouverture non remonté : %+v", r)
	}
	if r := withDev(ok, nil).Collect(context.Background(), c, nil); r.NodeValid || r.Err == nil {
		t.Fatal("collecte sans nœud préparé acceptée")
	}
}

func TestParsePCI(t *testing.T) {
	b, s, f, err := parsePCI("0000:3b:1f.7")
	if err != nil || b != 0x3b || s != 0x1f || f != 7 {
		t.Fatalf("%d %d %d %v", b, s, f, err)
	}
	for _, bad := range []string{"", "01:00.0", "0000:01:00.8", "zz"} {
		if _, _, _, err := parsePCI(bad); err == nil {
			t.Fatalf("%q accepté", bad)
		}
	}
}

// La condition de domaine de validité : sans l'interface annoncée, les
// collecteurs d'état sont non pris en charge ; la réponse 706 n'est lue
// qu'une fois par carte ; un échec n'est pas gardé et n'invalide pas le nœud.
func TestCollectSupplementGate(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	c := card("0000:01:00.0", "0")
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}

	d := &fakeDev{info: hba, sup: arcIo}
	m := withDev(d, nil)
	for i := 0; i < 3; i++ {
		r := m.Collect(context.Background(), c, ref)
		if !r.NodeValid || r.CollectErr != nil || len(r.Unsupported) != 0 {
			t.Fatalf("cycle %d : %+v", i, r)
		}
		// Réponse 706 gardée : collecteur sans date de dernier succès.
		if len(r.Cached) != 1 || r.Cached[0] != CapabilitiesCollector {
			t.Fatalf("cycle %d : collecteurs en mémoire %v", i, r.Cached)
		}
	}
	if d.supCall != 1 {
		t.Fatalf("706 envoyée %d fois en 3 cycles, attendu 1", d.supCall)
	}
	// Même adresse et même nœud, firmware mis à jour, puis carte remplacée
	// (autre numéro de série) : la réponse gardée n'est pas réutilisée.
	c2 := c
	c2.Hosts = append([]sysfs.SCSIHost(nil), c.Hosts...)
	c2.Hosts[0].Firmware = "5.3-0[19205]"
	m.Collect(context.Background(), c2, ref)
	c3 := c
	c3.Hosts = append([]sysfs.SCSIHost(nil), c.Hosts...)
	c3.Hosts[0].SerialNumber = "AUTRE"
	m.Collect(context.Background(), c3, ref)
	if d.supCall != 3 {
		t.Fatalf("706 relue %d fois après changements de firmware et de carte, attendu 3", d.supCall)
	}
	// Retour à l'identité d'origine : l'ancienne réponse a été oubliée.
	m.Collect(context.Background(), c, ref)
	if d.supCall != 4 {
		t.Fatalf("ancienne réponse 706 réutilisée au retour à l'identité d'origine (%d lectures)", d.supCall)
	}
	// Même règle pour la 703 : lue pour c, c2 (modèle qualifié, autre
	// firmware), c3, puis relue au retour.
	if d.aiCall != 4 {
		t.Fatalf("703 lue %d fois (attendu 4 : c, c2, c3, puis c de nouveau, l'ancienne réponse étant oubliée)", d.aiCall)
	}

	for name, sup := range map[string]aacraid.SupplementInfo{
		"bit absent": {Version: 15, SupportedOptions2: 0x00c86934},
		"version 1":  {Version: 1, SupportedOptions2: 0x08},
	} {
		r := withDev(&fakeDev{info: hba, sup: sup}, nil).Collect(context.Background(), c, ref)
		if !r.NodeValid || len(r.Unsupported) != 4 || r.Unsupported[0] != "volumes" || r.Unsupported[1] != "disks" ||
			r.Unsupported[2] != DiskInventoryCollector || r.Unsupported[3] != DiskLocationCollector {
			t.Fatalf("%s : %+v", name, r)
		}
	}

	f := &fakeDev{info: hba, supErr: errors.New("EIO")}
	m = withDev(f, nil)
	if r := m.Collect(context.Background(), c, ref); !r.NodeValid || r.CollectErr == nil || r.Unsupported != nil {
		t.Fatalf("échec 706 : %+v", r)
	}
	f.supErr, f.sup = nil, arcIo
	if r := m.Collect(context.Background(), c, ref); r.CollectErr != nil || f.supCall != 2 {
		t.Fatalf("échec gardé en cache : %+v (appels=%d)", r, f.supCall)
	}

	// Nœud d'une autre carte : aucune commande firmware n'est envoyée.
	other := &fakeDev{info: aacraid.HBAInfo{AdapterNumber: 1, Bus: 2, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}, sup: arcIo}
	withDev(other, nil).Collect(context.Background(), c, ref)
	if other.supCall != 0 {
		t.Fatal("706 envoyée sur un nœud qui ne désigne pas la carte")
	}
}

// hwraid_collection_success : la lecture des capacités est rapportée à
// chaque cycle (1 si la valeur est connue, 0 si la lecture vient d'échouer),
// et n'est pas rapportée si elle n'a pas été tentée (nœud d'une autre carte).
// Quand elle échoue, une carte qualifiée publie en échec les lectures
// qu'elle conditionne ; une carte non qualifiée ne reçoit aucune commande
// firmware et publie tous ses collecteurs non pris en charge (classement
// tiré de sysfs seul).
func TestCollectReportsCapabilitiesCollection(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	c := card("0000:01:00.0", "0")
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	after := []string{"volumes", "disks", DiskInventoryCollector, DiskLocationCollector, CacheProtectionCollector, TemperatureCollector}

	unq, unqHBA := otherModel(hba)
	fu := &fakeDev{info: unqHBA, supErr: errors.New("EIO")}
	if r := withDev(fu, nil).Collect(context.Background(), unq, ref); len(r.Collected) != 0 ||
		len(r.Unsupported) != 1+len(after) || !slices.Contains(r.Unsupported, CapabilitiesCollector) ||
		fu.pausedCall+fu.revCall+fu.aiCall+fu.supCall != 0 {
		t.Fatalf("non qualifiée : %+v / non pris en charge %v ; 704 %d, 701 %d, 703 %d, 706 %d", r.Collected, r.Unsupported, fu.pausedCall, fu.revCall, fu.aiCall, fu.supCall)
	}

	f := &fakeDev{info: hba, supErr: errors.New("EIO")}
	m := withDev(f, nil)
	r := m.Collect(context.Background(), c, ref)
	if len(r.Collected) != 1+len(after) || r.Collected[CapabilitiesCollector] || r.Unsupported != nil {
		t.Fatalf("échec : %+v / non pris en charge %v", r.Collected, r.Unsupported)
	}
	for _, col := range after {
		if ok, seen := r.Collected[col]; !seen || ok {
			t.Fatalf("lecture %s empêchée par la 706 non publiée en échec : %+v", col, r.Collected)
		}
	}
	f.supErr, f.sup = nil, arcIo
	for i := 0; i < 2; i++ { // lecture réelle, puis valeur gardée
		if r := m.Collect(context.Background(), c, ref); !r.Collected[CapabilitiesCollector] {
			t.Fatalf("cycle %d : %+v", i, r.Collected)
		}
	}
	other := &fakeDev{info: aacraid.HBAInfo{AdapterNumber: 1, Bus: 2, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}, sup: arcIo}
	if r := withDev(other, nil).Collect(context.Background(), c, ref); r.Collected != nil {
		t.Fatalf("lecture non tentée rapportée : %+v", r.Collected)
	}
}

// Protection du cache : lue à chaque cycle (l'état change sans
// redémarrage), traduite en état normalisé ; un format non décrit ou un
// autre type de module est « non pris en charge », un échec n'invente rien.
func TestCollectCacheProtection(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	c := card("0000:01:00.0", "0")
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	run := func(a *cpAnswer) (module.Result, *fakeDev) {
		d := &fakeDev{info: hba, sup: arcIo, cp: a}
		return withDev(d, nil).Collect(context.Background(), c, ref), d
	}

	r, d := run(nil) // état de l'hôte de référence
	if r.CacheProtection == nil || !r.CacheProtection.Present || r.CacheProtection.State != "failed" || !r.Collected[CacheProtectionCollector] {
		t.Fatalf("référence : %+v", r)
	}
	m := withDev(d, nil)
	m.Collect(context.Background(), c, ref)
	m.Collect(context.Background(), c, ref)
	if d.cpCall != 3 {
		t.Fatalf("protection du cache lue %d fois en 3 cycles, attendu 3", d.cpCall)
	}

	cases := []struct {
		name      string
		a         cpAnswer
		cp        *module.CacheProtection
		collected *bool
		unsup     bool
	}{
		{"optimal", cpAnswer{described: true, st: aacraid.ZMMStatus{State: aacraid.CacheProtectionOptimal}}, &module.CacheProtection{Present: true, Type: "flash_supercap", Vendor: "zmm", State: "optimal"}, ptr(true), false},
		{"non installé", cpAnswer{described: true, st: aacraid.ZMMStatus{State: aacraid.CacheProtectionNotInstalled}}, &module.CacheProtection{Type: "flash_supercap", Vendor: "zmm"}, ptr(true), false},
		{"état inconnu, installé", cpAnswer{described: true, st: aacraid.ZMMStatus{Word: 0x40000019, State: aacraid.CacheProtectionUnknown}}, &module.CacheProtection{Present: true, Type: "flash_supercap", Vendor: "zmm", State: "unknown"}, ptr(true), false},
		{"état inconnu, présence non établie", cpAnswer{described: true, st: aacraid.ZMMStatus{Word: 0x40000008, State: aacraid.CacheProtectionUnknown}}, &module.CacheProtection{PresenceUnknown: true, Type: "flash_supercap", Vendor: "zmm", State: "unknown"}, ptr(true), false},
		{"non décrit", cpAnswer{described: false}, nil, nil, true},
		{"autre type", cpAnswer{described: true, st: aacraid.ZMMStatus{State: aacraid.CacheProtectionOtherType}}, nil, nil, true},
		{"échec", cpAnswer{err: errors.New("EIO")}, nil, ptr(false), false},
	}
	for _, tc := range cases {
		r, _ := run(&tc.a)
		if (r.CacheProtection == nil) != (tc.cp == nil) || (tc.cp != nil && *r.CacheProtection != *tc.cp) {
			t.Fatalf("%s : protection %+v, attendu %+v", tc.name, r.CacheProtection, tc.cp)
		}
		got, ok := r.Collected[CacheProtectionCollector]
		if (tc.collected == nil) != !ok || (tc.collected != nil && got != *tc.collected) {
			t.Fatalf("%s : collected=%v (présent=%v)", tc.name, got, ok)
		}
		has := false
		for _, u := range r.Unsupported {
			has = has || u == CacheProtectionCollector
		}
		if has != tc.unsup || !r.NodeValid {
			t.Fatalf("%s : unsupported=%v %+v", tc.name, r.Unsupported, r)
		}
	}

	// Lecture des capacités en échec : pas de lecture du cache non plus.
	f := &fakeDev{info: hba, supErr: errors.New("EIO")}
	withDev(f, nil).Collect(context.Background(), c, ref)
	if f.cpCall != 0 {
		t.Fatal("protection du cache lue alors que la carte n'a pas répondu à 706")
	}
}

func ptr(b bool) *bool { return &b }

// Configuration de l'hôte de référence : un volume assemblant quatre
// miroirs, huit disques, tout optimal.
func TestCollectVolumesReference(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	c := card("0000:01:00.0", "0")
	d := &fakeDev{info: aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}, sup: arcIo}
	r := withDev(d, nil).Collect(context.Background(), c, ref)
	// Type sans niveau connu (6 en numérotation ARCIO) : niveau inconnu,
	// jamais deviné.
	d2 := &fakeDev{info: aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}, sup: arcIo, paged: func(rd aacraid.PagedRead, p, size uint32) ([]byte, error) {
		b, err := referencePaged(rd, p, size)
		if err == nil && rd == aacraid.ReadContainerInfo && p == 0xfc {
			b[48] = 6
		}
		return b, err
	}}
	if r2 := withDev(d2, nil).Collect(context.Background(), c, ref); r2.Volumes[1].RaidLevel != "unknown" || r2.Volumes[1].VendorLevel != "type 6" || r2.Volumes[1].State != "unknown" {
		t.Fatalf("type non observé : %+v", r2.Volumes[1])
	}
	// Bout en bout, le niveau et l'état publiés suivent la même lecture du
	// type : le miroir 0xfc (UID 1d000004, disques 3 et 7) relu en
	// numérotation classique est un RAID 0 ; sans son disque 7, il n'est
	// jamais publié « dégradé ». En migration (octets 120-123 = 3), ni son
	// niveau ni son état ne sont établis.
	for _, tc := range []struct {
		name          string
		mod           func(r aacraid.PagedRead, p uint32, b []byte) []byte
		level, vendor string
		state, topSt  string
	}{
		{"RAID 0 classique sans disque 7", func(r aacraid.PagedRead, p uint32, b []byte) []byte {
			switch {
			case r == aacraid.ReadContainerInfo && p == 0xfc:
				b[96] = 3
			case r == aacraid.ReadDiskList:
				var out []byte
				n := binary.LittleEndian.Uint32(b)
				for i := uint32(0); i < n; i++ {
					if d := binary.LittleEndian.Uint32(b[4+4*i:]); d != 7 {
						out = binary.LittleEndian.AppendUint32(out, d)
					}
				}
				b = append(binary.LittleEndian.AppendUint32(nil, uint32(len(out)/4)), out...)
			}
			return b
		}, "raid0", "0", "unknown", "unknown"},
		{"type RAID 5 à un seul membre", func(r aacraid.PagedRead, p uint32, b []byte) []byte {
			switch {
			case r == aacraid.ReadContainerInfo && p == 0xfc:
				b[48], b[56] = 7, 1
			case r == aacraid.ReadMemberList && p == 0x1d000004:
				b[4] = 1
				b = b[:8+32]
			}
			return b
		}, "unknown", "type 7", "unknown", "unknown"},
		{"RAID 6 Reed-Solomon classique", func(r aacraid.PagedRead, p uint32, b []byte) []byte {
			switch {
			case r == aacraid.ReadContainerInfo && p == 0xfc:
				b[48], b[56], b[96], b[89] = 18, 4, 3, 0x04
			case r == aacraid.ReadMemberList && p == 0x1d000004:
				// Quatre membres disques (3, 7, 0, 4), rangs 0 à 3.
				e := append([]byte(nil), b[8:8+64]...)
				for i, dsk := range []uint32{0, 4} {
					x := append([]byte(nil), b[8:8+32]...)
					binary.LittleEndian.PutUint32(x[0:], dsk)
					binary.LittleEndian.PutUint16(x[10:], uint16(2+i))
					e = append(e, x...)
				}
				b = append(binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, 0), 4), e...)
			}
			return b
		}, "raid6", "6 Reed-Solomon", "unknown", "unknown"},
		{"RAID 6 Reed-Solomon ARCIO, un membre mort", func(r aacraid.PagedRead, p uint32, b []byte) []byte {
			switch {
			case r == aacraid.ReadContainerInfo && p == 0xfc:
				b[48], b[56] = 13, 4
			case r == aacraid.ReadMemberList && p == 0x1d000004:
				// Quatre membres disques (3, 7, 0, 4), rangs 0 à 3 ; le
				// disque 3 (premier membre) marqué mort.
				e := append([]byte(nil), b[8:8+64]...)
				for i, dsk := range []uint32{0, 4} {
					x := append([]byte(nil), b[8:8+32]...)
					binary.LittleEndian.PutUint32(x[0:], dsk)
					binary.LittleEndian.PutUint16(x[10:], uint16(2+i))
					e = append(e, x...)
				}
				e[12] = 0x04
				b = append(binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, 0), 4), e...)
			}
			return b
		}, "raid6", "6 Reed-Solomon", "reduced_redundancy", "reduced_redundancy"},
		{"miroir en migration", func(r aacraid.PagedRead, p uint32, b []byte) []byte {
			if r == aacraid.ReadContainerInfo && p == 0xfc {
				b[120] = 3
			}
			return b
		}, "unknown", "type 3", "unknown", "unknown"},
	} {
		d3 := &fakeDev{info: d.info, sup: arcIo, paged: func(rd aacraid.PagedRead, p, size uint32) ([]byte, error) {
			b, err := referenceRaw(rd, p)
			if err != nil {
				return b, err
			}
			return checkMemberListSize(rd, size, tc.mod(rd, p, b))
		}}
		r3 := withDev(d3, nil).Collect(context.Background(), c, ref)
		var got, top module.Volume
		for _, v := range r3.Volumes {
			switch v.ID {
			case "1d000004":
				got = v
			case "1d000000":
				top = v
			}
		}
		if r3.CollectErr != nil || got.RaidLevel != tc.level || got.VendorLevel != tc.vendor || got.State != tc.state || top.State != tc.topSt {
			t.Fatalf("%s : %+v ; assemblage %s ; erreur %v", tc.name, got, top.State, r3.CollectErr)
		}
	}
	// Migration par conteneur associé : le miroir 0xfc, en migration,
	// désigne aux octets 224-227 l'assemblage, qui n'a pas de parent ;
	// l'assemblage n'a alors ni niveau ni état établis.
	d4 := &fakeDev{info: d.info, sup: arcIo, paged: func(rd aacraid.PagedRead, p, size uint32) ([]byte, error) {
		b, err := referencePaged(rd, p, size)
		if err == nil && rd == aacraid.ReadContainerInfo && p == 0xfc {
			b[120] = 3
			binary.LittleEndian.PutUint32(b[224:], 0x1d000000)
		}
		return b, err
	}}
	r4 := withDev(d4, nil).Collect(context.Background(), c, ref)
	found := false
	for _, v := range r4.Volumes {
		if v.ID == "1d000000" {
			found = true
			if v.RaidLevel != "unknown" || v.State != "unknown" {
				t.Fatalf("assemblage désigné par une migration : %+v", v)
			}
		}
	}
	if !found || r4.CollectErr != nil || len(r4.Volumes) != 5 {
		t.Fatalf("migration associée : %+v", r4)
	}
	if !r.Collected["volumes"] || !r.Collected["disks"] || r.CollectErr != nil {
		t.Fatalf("%+v", r)
	}
	if len(r.Volumes) != 5 || len(r.Disks) != 8 {
		t.Fatalf("%d volumes, %d disques", len(r.Volumes), len(r.Disks))
	}
	top := r.Volumes[0]
	if top.ID != "1d000000" || top.Name != "Array0" || top.RaidLevel != "concat" || top.VendorLevel != "RAID_volume" || top.Parent != "" ||
		top.CapacityBytes != 62495109120*512 || top.State != "optimal" || len(top.Members) != 4 {
		t.Fatalf("volume : %+v", top)
	}
	for _, v := range r.Volumes[1:] {
		if v.RaidLevel != "raid1" || v.VendorLevel != "1" || v.Parent != "1d000000" || v.State != "optimal" || len(v.Members) != 2 {
			t.Fatalf("miroir : %+v", v)
		}
	}
	for _, v := range r.Volumes {
		if v.WriteCachePolicy != "write_back_always" || v.VendorWriteCachePolicy != "Enabled" || v.WriteCacheEnabled == nil || !*v.WriteCacheEnabled {
			t.Fatalf("cache d'écriture : %+v", v)
		}
	}
	for i, dk := range r.Disks {
		if dk.ID != fmt.Sprintf("d%d", i) || dk.State != "online" {
			t.Fatalf("disque : %+v", dk)
		}
	}
	// Séquence : liste, 5 informations, disques, 5 listes de membres, puis
	// l'information de chacun des 8 disques présents.
	if len(d.reads) != 20 || d.reads[0] != "229-0" || d.reads[6] != "247-0" || d.reads[12] != "248-0" || d.reads[19] != "248-7" {
		t.Fatalf("lectures %v", d.reads)
	}
	// Inventaire : tous les disques présents, dans l'ordre, valeurs de la
	// capture anonymisée.
	if !r.Collected[DiskInventoryCollector] || len(r.DiskInventory) != 8 {
		t.Fatalf("inventaire : %+v", r)
	}
	want := module.DiskInfo{ID: "d7", Vendor: "ATA", Model: "ST8000VN0022-2EL", Firmware: "SC61", Serial: "TESTSN07",
		WWN: "0x5000c50000000007", CapacityBytes: 15628053168 * 512}
	if got := r.DiskInventory[7]; got != want {
		t.Fatalf("d7 :\n got %+v\nwant %+v", got, want)
	}
}

// Cache d'écriture : chaque réglage observé avec l'outil de référence
// (wb, wbb, wt), une valeur non observée publiée unknown ; état effectif
// publié seulement pour 0 ou 1. Les deux champs sont des mots de 4 octets :
// un mot dont seul l'octet bas est reconnu (0x00000101) reste inconnu.
func TestCollectWriteCache(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	c := card("0000:01:00.0", "0")
	for _, tc := range []struct {
		policy, enabled         uint32
		wantPolicy, wantVendor  string
		wantEnabled, publishSet bool
	}{
		{2, 1, "write_back_always", "Enabled", true, true},
		{1, 0, "write_back_when_protected", "On when protected by battery/ZMM", false, true},
		{1, 1, "write_back_when_protected", "On when protected by battery/ZMM", true, true},
		{0, 0, "write_through", "Disabled", false, true},
		{3, 2, "unknown", "value 3", false, false},
		{0x101, 0x101, "unknown", "value 257", false, false},
	} {
		d := &fakeDev{info: aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}, sup: arcIo, paged: func(rd aacraid.PagedRead, p, size uint32) ([]byte, error) {
			b, err := referencePaged(rd, p, size)
			if err == nil && rd == aacraid.ReadContainerInfo && p == 0 {
				binary.LittleEndian.PutUint32(b[148:], tc.policy)
				binary.LittleEndian.PutUint32(b[156:], tc.enabled)
			}
			return b, err
		}}
		r := withDev(d, nil).Collect(context.Background(), c, ref)
		v := r.Volumes[0]
		if !r.Collected["volumes"] || v.WriteCachePolicy != tc.wantPolicy || v.VendorWriteCachePolicy != tc.wantVendor {
			t.Fatalf("réglage %d : %+v", tc.policy, v)
		}
		if (v.WriteCacheEnabled != nil) != tc.publishSet || (v.WriteCacheEnabled != nil && *v.WriteCacheEnabled != tc.wantEnabled) {
			t.Fatalf("état %d : %v", tc.enabled, v.WriteCacheEnabled)
		}
		// Les miroirs gardent la valeur capturée.
		if m := r.Volumes[1]; m.WriteCachePolicy != "write_back_always" || m.WriteCacheEnabled == nil || !*m.WriteCacheEnabled {
			t.Fatalf("miroir : %+v", m)
		}
	}
}

// Inventaire : une lecture en échec ou une réponse qui désigne un autre
// disque annule l'inventaire du cycle, sans toucher aux volumes ni aux
// états ; un échec de configuration ne lit pas l'inventaire.
func TestCollectDiskInventoryFailure(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	c := card("0000:01:00.0", "0")
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	cases := map[string]func(r aacraid.PagedRead, p, size uint32) ([]byte, error){
		"lecture en échec": func(r aacraid.PagedRead, p, size uint32) ([]byte, error) {
			if r == aacraid.ReadDiskInfo && p == 5 {
				return nil, errors.New("EIO")
			}
			return referencePaged(r, p, size)
		},
		"autre disque": func(r aacraid.PagedRead, p, size uint32) ([]byte, error) {
			if r == aacraid.ReadDiskInfo && p == 5 {
				return referencePaged(r, 6, size)
			}
			return referencePaged(r, p, size)
		},
	}
	for name, paged := range cases {
		d := &fakeDev{info: hba, sup: arcIo, paged: paged}
		r := withDev(d, nil).Collect(context.Background(), c, ref)
		if r.DiskInventory != nil || r.Collected[DiskInventoryCollector] || r.CollectErr == nil {
			t.Fatalf("%s : %+v", name, r)
		}
		if len(r.Volumes) != 5 || !r.Collected["volumes"] || !r.Collected["disks"] {
			t.Fatalf("%s : volumes touchés : %+v", name, r)
		}
	}
	d := &fakeDev{info: hba, sup: arcIo, paged: func(r aacraid.PagedRead, p, size uint32) ([]byte, error) {
		if r == aacraid.ReadContainerList {
			return nil, errors.New("EIO")
		}
		return referencePaged(r, p, size)
	}}
	// Liste des conteneurs en échec : la chaîne des disques est lue quand
	// même (CDC, section 6.11, dépendances des résultats).
	r := withDev(d, nil).Collect(context.Background(), c, ref)
	if r.Collected["volumes"] || r.Collected["disks"] || !r.Collected[DiskInventoryCollector] || len(r.DiskInventory) != 8 ||
		!r.Collected[DiskLocationCollector] || len(r.DiskLocations) != 8 {
		t.Fatalf("échec de la liste des conteneurs : %+v", r)
	}
}

// Une lecture en échec annule toute la configuration du cycle ; une carte
// sans l'interface annoncée n'est pas interrogée.
func TestCollectVolumesFailureAndGate(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	c := card("0000:01:00.0", "0")
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	for _, failing := range []string{"229-0", "234-fd", "247-0", "249-1d000003"} {
		d := &fakeDev{info: hba, sup: arcIo, paged: func(r aacraid.PagedRead, p, size uint32) ([]byte, error) {
			if fmt.Sprintf("%d-%x", r, p) == failing {
				return nil, errors.New("EIO")
			}
			return referencePaged(r, p, size)
		}}
		r := withDev(d, nil).Collect(context.Background(), c, ref)
		if r.Volumes != nil || r.Disks != nil || r.Collected["volumes"] || r.Collected["disks"] || r.CollectErr == nil || !r.NodeValid {
			t.Fatalf("échec sur %s : %+v", failing, r)
		}
	}
	d := &fakeDev{info: hba, sup: aacraid.SupplementInfo{Version: 15, SupportedOptions2: 0}}
	r := withDev(d, nil).Collect(context.Background(), c, ref)
	if len(d.reads) != 0 || r.Volumes != nil {
		t.Fatalf("carte sans interface interrogée : %v", d.reads)
	}
}

// Exigence S4 : une carte aacraid dont le modèle ou le firmware n'est pas
// qualifié ne reçoit aucune lecture firmware au-delà de 706 ; ses
// collecteurs sont publiés non pris en charge.
func TestCollectRequiresQualification(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	// Modèle non qualifié : rien au-delà de la 706 (les variantes de
	// firmware d'un modèle qualifié sont traitées par
	// TestQualifiedModelOtherFirmware).
	variants := map[string]func(c *module.Card){
		"autre modèle": func(c *module.Card) { c.PCI.SubsystemDevice = "0x0300" },
		"autre puce":   func(c *module.Card) { c.PCI.Device = "0x028c" },
	}
	for name, mod := range variants {
		c := card("0000:01:00.0", "0")
		mod(&c)
		// Carte cohérente : le pilote annonce les identifiants de sysfs.
		h := hba
		id, err := pciIdentity(c)
		if err != nil {
			t.Fatal(err)
		}
		h.VendorID, h.DeviceID, h.SubVendorID, h.SubSystemID = id.Vendor, id.Device, id.SubVendor, id.SubDevice
		d := &fakeDev{info: h, sup: arcIo}
		r := withDev(d, nil).Collect(context.Background(), c, ref)
		if !r.NodeValid {
			t.Fatalf("%s : nœud refusé : %+v", name, r)
		}
		if d.pausedCall+d.revCall+d.supCall != 0 || d.cpCall != 0 || len(d.reads) != 0 || d.cmCall != 0 || d.countCall != 0 || d.aiCall != 0 || d.envCall != 0 {
			t.Fatalf("%s : commandes firmware envoyées (704 %d, 701 %d, 706 %d, ZMM %d, paginées %v, 708 %d, comptage %d, 703 %d, 803 %d)", name, d.pausedCall, d.revCall, d.supCall, d.cpCall, d.reads, d.cmCall, d.countCall, d.aiCall, d.envCall)
		}
		want := map[string]bool{CapabilitiesCollector: true, "volumes": true, "disks": true, DiskInventoryCollector: true, DiskLocationCollector: true, CacheProtectionCollector: true, TemperatureCollector: true}
		for _, u := range r.Unsupported {
			delete(want, u)
		}
		if len(want) != 0 || !r.NodeValid || len(r.Collected) != 0 {
			t.Fatalf("%s : %+v", name, r)
		}
	}
	// La carte de référence est un couple validé.
	if !ValidatedCouple(card("0000:01:00.0", "0")) {
		t.Fatal("6805 de référence non validée")
	}
}

// otherModel rend une carte d'un modèle non qualifié (sous-système 0x0300),
// avec la réponse GET_HBA_INFO cohérente.
func otherModel(hba aacraid.HBAInfo) (module.Card, aacraid.HBAInfo) {
	c := card("0000:01:00.0", "0")
	c.PCI.SubsystemDevice = "0x0300"
	hba.SubSystemID = 0x0300
	return c, hba
}

// Modèle qualifié, firmware autre que ceux validés (ou non publié) : la
// carte est lue (la qualification porte sur le modèle), protection du cache
// comprise comme l'outil de référence, sans les écarts E1 et E2 (couple non
// validé transmis au transport ; CDC, sections 6.11 et 6.12). La carte de
// référence est un couple validé.
func TestQualifiedModelOtherFirmware(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	for name, fw := range map[string]string{"autre firmware": "5.2-0[19109]", "firmware non publié": ""} {
		c := card("0000:01:00.0", "0")
		c.Hosts[0].Firmware = fw
		d := &fakeDev{info: hba, sup: arcIo}
		r := withDev(d, nil).Collect(context.Background(), c, ref)
		if !d.validatedSet || d.validated {
			t.Fatalf("%s : couple validé transmis au transport (%v, %v)", name, d.validatedSet, d.validated)
		}
		if d.cpCall == 0 || len(r.Unsupported) != 0 || !r.Collected[CacheProtectionCollector] {
			t.Fatalf("%s : protection du cache envoyée %d fois ; non pris en charge %v", name, d.cpCall, r.Unsupported)
		}
		if len(d.reads) == 0 || d.countCall != 1 || d.aiCall != 1 || !r.Collected["volumes"] || !r.Collected[DiskInventoryCollector] {
			t.Fatalf("%s : carte non lue (paginées %v, comptage %d, 703 %d) : %+v", name, d.reads, d.countCall, d.aiCall, r.Collected)
		}
	}
	d := &fakeDev{info: hba, sup: arcIo}
	withDev(d, nil).Collect(context.Background(), card("0000:01:00.0", "0"), ref)
	if !d.validatedSet || !d.validated || d.cpCall == 0 {
		t.Fatalf("carte de référence : couple validé %v/%v, protection du cache %d", d.validatedSet, d.validated, d.cpCall)
	}
}

// Deux conteneurs au même UID : relevé rejeté, rien publié.
func TestCollectRejectsDuplicateUID(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	d := &fakeDev{info: aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}, sup: arcIo, paged: func(r aacraid.PagedRead, p, size uint32) ([]byte, error) {
		if r == aacraid.ReadContainerInfo && p == 0xfd {
			p = 0xfc // même information, donc même UID
		}
		return referencePaged(r, p, size)
	}}
	r := withDev(d, nil).Collect(context.Background(), card("0000:01:00.0", "0"), ref)
	if r.Volumes != nil || r.Collected["volumes"] || r.CollectErr == nil {
		t.Fatalf("%+v", r)
	}
}

// Une carte bloquée dans la lecture 706 ne bloque pas la collecte d'une
// autre carte : aucun verrou du module n'est tenu pendant un appel firmware.
func TestBlockedCardDoesNotBlockOthers(t *testing.T) {
	wait := make(chan struct{})
	devs := map[string]*fakeDev{
		"/n/aac0": {info: aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}, sup: arcIo, supWait: wait},
		"/n/aac1": {info: aacraid.HBAInfo{AdapterNumber: 1, Bus: 2, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}, sup: arcIo},
	}
	m := &Module{open: func(path string, _ uint64) (device, error) { return devs[path], nil }}
	go m.Collect(context.Background(), card("0000:01:00.0", "0"), []module.NodeRef{{Path: "/n/aac0", Dev: 1}})
	for i := 0; i < 1000 && devs["/n/aac0"].started.Load() == 0; i++ {
		time.Sleep(time.Millisecond)
	}
	done := make(chan module.Result, 1)
	go func() {
		done <- m.Collect(context.Background(), card("0000:02:00.0", "1"), []module.NodeRef{{Path: "/n/aac1", Dev: 2}})
	}()
	select {
	case r := <-done:
		if !r.NodeValid || !r.Collected[CapabilitiesCollector] {
			t.Fatalf("seconde carte : %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("la seconde carte attend la première")
	}
	close(wait)
}

// Emplacement : valeurs de la carte des connecteurs, telles que l'outil
// témoin les affiche (d7 = connecteur 1 « CN1 », device 3, phy 4) ; carte
// lue une seule fois en trois cycles.
func TestCollectDiskLocations(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	c := card("0000:01:00.0", "0")
	d := &fakeDev{info: aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}, sup: arcIo}
	m := withDev(d, nil)
	var r module.Result
	for i := 0; i < 3; i++ {
		r = m.Collect(context.Background(), c, ref)
	}
	if d.cmCall != 1 {
		t.Fatalf("carte des connecteurs lue %d fois en 3 cycles", d.cmCall)
	}
	if !r.Collected[DiskLocationCollector] || len(r.DiskLocations) != 8 {
		t.Fatalf("%+v", r)
	}
	for n, l := range r.DiskLocations {
		want := module.DiskLocation{ID: fmt.Sprintf("d%d", n), Connector: fmt.Sprint(n / 4), ConnectorName: fmt.Sprintf("CN%d", n/4),
			Device: fmt.Sprint(n % 4), Phy: fmt.Sprint(4*(n/4) + 3 - n%4)}
		if l != want {
			t.Fatalf("%+v, attendu %+v", l, want)
		}
	}
}

// Échecs : carte des connecteurs illisible (relue au cycle suivant), phy
// incohérent (carte oubliée), disque sans entrée (pas d'emplacement pour
// lui, les autres publiés). L'inventaire reste publié dans tous les cas.
func TestCollectDiskLocationFailures(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	c := card("0000:01:00.0", "0")
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}

	fail := true
	d := &fakeDev{info: hba, sup: arcIo, cmap: func() (aacraid.ConnectorMap, error) {
		if fail {
			return aacraid.ConnectorMap{}, errors.New("EIO")
		}
		return referenceConnectorMap()
	}}
	m := withDev(d, nil)
	r := m.Collect(context.Background(), c, ref)
	if r.Collected[DiskLocationCollector] || r.DiskLocations != nil || len(r.DiskInventory) != 8 || r.CollectErr == nil {
		t.Fatalf("échec de lecture : %+v", r)
	}
	fail = false
	if r = m.Collect(context.Background(), c, ref); !r.Collected[DiskLocationCollector] || d.cmCall != 2 {
		t.Fatalf("pas de nouvelle lecture après échec (%d) : %+v", d.cmCall, r)
	}

	bad := func() (aacraid.ConnectorMap, error) {
		cm, err := referenceConnectorMap()
		cm.Entries[0].Phy = 7 // disque 3 : phy 0 dans 248
		return cm, err
	}
	d = &fakeDev{info: hba, sup: arcIo, cmap: bad}
	m = withDev(d, nil)
	r = m.Collect(context.Background(), c, ref)
	if r.Collected[DiskLocationCollector] || r.DiskLocations != nil || len(r.DiskInventory) != 8 {
		t.Fatalf("phy incohérent : %+v", r)
	}
	m.Collect(context.Background(), c, ref)
	if d.cmCall != 2 {
		t.Fatalf("carte incohérente gardée (%d lectures)", d.cmCall)
	}

	partial := func() (aacraid.ConnectorMap, error) {
		cm, err := referenceConnectorMap()
		cm.Entries = cm.Entries[1:] // disque 3 derrière un expandeur
		return cm, err
	}
	d = &fakeDev{info: hba, sup: arcIo, cmap: partial}
	r = withDev(d, nil).Collect(context.Background(), c, ref)
	if !r.Collected[DiskLocationCollector] || len(r.DiskLocations) != 7 {
		t.Fatalf("disque sans entrée : %+v", r)
	}
	for _, l := range r.DiskLocations {
		if l.ID == "d3" {
			t.Fatal("emplacement publié pour un disque sans entrée")
		}
	}
}

// Lien disque → périphérique Linux : disque N de la carte = adresse
// H:1:N:0, publié seulement si le numéro de série du noyau est celui de
// l'information de disque. Le volume (canal 0, même cible) n'est jamais
// pris pour un disque.
func TestCollectDiskOSDevices(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	devices := func() []sysfs.SCSIDevice {
		out := []sysfs.SCSIDevice{{Address: "0:0:0:0", Channel: 0, Target: 0, Generic: "sg0", Serial: "TESTSN00"}}
		for n := 0; n < 8; n++ {
			out = append(out, sysfs.SCSIDevice{Address: fmt.Sprintf("0:1:%d:0", n), Channel: 1, Target: n,
				Generic: fmt.Sprintf("sg%d", n+1), Serial: fmt.Sprintf("TESTSN0%d", n)})
		}
		return out
	}
	run := func(devs []sysfs.SCSIDevice) map[string]module.DiskOSDevice {
		c := card("0000:01:00.0", "0")
		c.Hosts[0].Devices = devs
		r := withDev(&fakeDev{info: hba, sup: arcIo}, nil).Collect(context.Background(), c, ref)
		got := map[string]module.DiskOSDevice{}
		for _, o := range r.DiskOSDevices {
			got[o.ID] = o
		}
		return got
	}
	got := run(devices())
	if len(got) != 8 || got["d7"] != (module.DiskOSDevice{ID: "d7", Device: "sg8", Address: "0:1:7:0"}) ||
		got["d0"] != (module.DiskOSDevice{ID: "d0", Device: "sg1", Address: "0:1:0:0"}) {
		t.Fatalf("%+v", got)
	}
	// Le disque 7 n'est pas un disque (octet 81 bit 0x80) : pas de
	// périphérique Linux publié pour lui.
	c := card("0000:01:00.0", "0")
	c.Hosts[0].Devices = devices()
	r := withDev(&fakeDev{info: hba, sup: arcIo, paged: func(rd aacraid.PagedRead, p, size uint32) ([]byte, error) {
		b, err := referencePaged(rd, p, size)
		if err == nil && rd == aacraid.ReadDiskInfo && p == 7 {
			b[81] = 0x80
		}
		return b, err
	}}, nil).Collect(context.Background(), c, ref)
	for _, o := range r.DiskOSDevices {
		if o.ID == "d7" {
			t.Fatalf("autre périphérique relié à Linux : %+v", o)
		}
	}
	if len(r.DiskOSDevices) != 7 {
		t.Fatalf("%+v", r.DiskOSDevices)
	}
	devs := devices()
	devs[8].Serial = "AUTRE" // 0:1:7:0 : autre disque que d7
	devs[1].Generic = ""     // 0:1:0:0 : pas de nœud générique
	devs[4].LUN = 1          // 0:1:3:1 : LUN non nul
	if got := run(devs); len(got) != 5 || got["d7"].ID != "" || got["d0"].ID != "" || got["d3"].ID != "" {
		t.Fatalf("contrôles : %+v", got)
	}
	// Disque absent du canal 1 : le volume au canal 0, même cible et même
	// numéro de série, n'est pas retenu.
	if got := run(devices()[:1]); len(got) != 0 {
		t.Fatalf("volume pris pour un disque : %+v", got)
	}
	if got := run(nil); len(got) != 0 {
		t.Fatalf("sans liste : %+v", got)
	}
}

// Liste de membres plus courte que le nombre annoncé par 234 : la
// configuration du cycle est en erreur, rien n'est publié.
func TestCollectMemberCountMismatch(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	d := &fakeDev{info: aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}, sup: arcIo, paged: func(r aacraid.PagedRead, p, size uint32) ([]byte, error) {
		if r == aacraid.ReadMemberList && p == 0x1d000000 {
			b, err := referencePaged(r, p, size)
			if err != nil {
				return nil, err
			}
			binary.LittleEndian.PutUint32(b[4:], 3) // 3 listés, 4 annoncés
			return b[:8+32*3], nil
		}
		return referencePaged(r, p, size)
	}}
	r := withDev(d, nil).Collect(context.Background(), card("0000:01:00.0", "0"), ref)
	if r.Volumes != nil || r.Collected["volumes"] || r.Collected["disks"] || r.CollectErr == nil {
		t.Fatalf("%+v", r)
	}
}

// Carte des connecteurs relue quand l'ensemble des disques présents change
// (ajout à chaud), et seulement dans ce cas.
func TestConnectorMapRereadOnDiskChange(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	c := card("0000:01:00.0", "0")
	seven := true
	d := &fakeDev{info: aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}, sup: arcIo, paged: func(r aacraid.PagedRead, p, size uint32) ([]byte, error) {
		b, err := referencePaged(r, p, size)
		if err == nil && r == aacraid.ReadDiskList && seven {
			binary.LittleEndian.PutUint32(b[0:], 7) // d7 absent
		}
		return b, err
	}}
	m := withDev(d, nil)
	m.Collect(context.Background(), c, ref)
	m.Collect(context.Background(), c, ref)
	if d.cmCall != 1 {
		t.Fatalf("%d lectures pour une liste inchangée", d.cmCall)
	}
	seven = false
	r := m.Collect(context.Background(), c, ref)
	m.Collect(context.Background(), c, ref)
	if d.cmCall != 2 || len(r.DiskLocations) != 8 {
		t.Fatalf("%d lectures, %d emplacements après l'ajout de d7", d.cmCall, len(r.DiskLocations))
	}
}

// Le nœud doit désigner la carte par ses quatre identifiants PCI aussi :
// une réponse dont seul un identifiant diffère est refusée, sans aucune
// commande firmware.
func TestCollectRequiresPCIIdentifiers(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	good := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	if r := withDev(&fakeDev{info: good, sup: arcIo}, nil).Collect(context.Background(), card("0000:01:00.0", "0"), ref); !r.NodeValid {
		t.Fatalf("carte de référence refusée : %+v", r)
	}
	for name, mod := range map[string]func(h *aacraid.HBAInfo){
		"fabricant":      func(h *aacraid.HBAInfo) { h.VendorID = 0x1000 },
		"puce":           func(h *aacraid.HBAInfo) { h.DeviceID = 0x028c },
		"sous-fabricant": func(h *aacraid.HBAInfo) { h.SubVendorID = 0x103c },
		"sous-système":   func(h *aacraid.HBAInfo) { h.SubSystemID = 0x0300 },
	} {
		h := good
		mod(&h)
		d := &fakeDev{info: h, sup: arcIo}
		r := withDev(d, nil).Collect(context.Background(), card("0000:01:00.0", "0"), ref)
		if r.NodeValid || r.Err == nil || d.supCall != 0 || d.cpCall != 0 || len(d.reads) != 0 || d.cmCall != 0 {
			t.Fatalf("%s différent : %+v (706 %d, ZMM %d, paginées %v)", name, r, d.supCall, d.cpCall, d.reads)
		}
	}
	// Identifiant illisible ou hors du format sysfs : refus, sans commande.
	for _, v := range []string{"", "0301", "0xzz"} {
		c := card("0000:01:00.0", "0")
		c.PCI.SubsystemDevice = v
		d := &fakeDev{info: good, sup: arcIo}
		if r := withDev(d, nil).Collect(context.Background(), c, ref); r.Err == nil || d.supCall != 0 {
			t.Fatalf("identifiant sysfs %q accepté : %+v", v, r)
		}
	}
}

// Un membre listé deux fois dans un volume (cardinalité correcte) rend la
// configuration du cycle en erreur : sinon deux séries identiques seraient
// publiées.
func TestCollectDuplicateMember(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	d := &fakeDev{info: aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301},
		sup: arcIo, paged: func(r aacraid.PagedRead, p, size uint32) ([]byte, error) {
			b, err := referencePaged(r, p, size)
			if err == nil && r == aacraid.ReadMemberList && p == 0x1d000004 {
				rank := [2]byte{b[8+32+10], b[8+32+11]}
				copy(b[8+32:8+64], b[8:8+32])             // second membre = premier,
				b[8+32+10], b[8+32+11] = rank[0], rank[1] // rang conservé : seul le doublon est en cause
			}
			return b, err
		}}
	r := withDev(d, nil).Collect(context.Background(), card("0000:01:00.0", "0"), ref)
	if r.Volumes != nil || r.Collected["volumes"] || r.CollectErr == nil {
		t.Fatalf("%+v", r)
	}
}

// Fin du contexte (échéance du cycle ou arrêt) : plus aucune commande
// firmware n'est envoyée, les collecteurs non lus sont en échec et les
// lectures faites avant sont gardées.
func TestCollectStopsWithContext(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var d *fakeDev
	d = &fakeDev{info: hba, sup: arcIo, paged: func(r aacraid.PagedRead, p, size uint32) ([]byte, error) {
		if len(d.reads) == 4 {
			cancel() // fin du contexte pendant la quatrième lecture
		}
		return referencePaged(r, p, size)
	}}
	r := withDev(d, nil).Collect(ctx, card("0000:01:00.0", "0"), ref)
	// 229 puis trois 234 ; la quatrième 234 et 708 ne sont pas envoyées.
	if len(d.reads) != 4 || d.cmCall != 0 || d.refused == 0 {
		t.Fatalf("lectures après la fin du contexte : %v (708 : %d, refusées %d)", d.reads, d.cmCall, d.refused)
	}
	if !errors.Is(r.CollectErr, aacio.ErrStopped) || r.Collected["volumes"] || r.Volumes != nil || r.DiskInventory != nil {
		t.Fatalf("%+v", r)
	}
	if !r.Collected[CacheProtectionCollector] || !r.NodeValid {
		t.Fatalf("lectures faites avant la fin perdues : %+v", r)
	}

	// Contexte déjà terminé : aucune commande firmware, pas même 706, et le
	// nœud n'est pas vérifié (GET_HBA_INFO refusée elle aussi).
	d = &fakeDev{info: hba, sup: arcIo}
	r = withDev(d, nil).Collect(ctx, card("0000:01:00.0", "0"), ref)
	if d.supCall != 0 || d.cpCall != 0 || len(d.reads) != 0 || d.cmCall != 0 || d.aiCall != 0 || d.envCall != 0 {
		t.Fatalf("commandes après la fin (706 %d, ZMM %d, 703 %d, 803 %d, paginées %v)", d.supCall, d.cpCall, d.aiCall, d.envCall, d.reads)
	}
	if r.NodeValid || !errors.Is(r.Err, aacio.ErrStopped) || r.Collected[CapabilitiesCollector] {
		t.Fatalf("%+v", r)
	}
}

// Arrêt pendant une commande bloquée : Collect attend la fin de cette
// commande, n'en envoie aucune autre et rend la main aussitôt après.
func TestCollectStopDuringBlockedCommand(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	blocked, release := make(chan struct{}), make(chan struct{})
	d := &fakeDev{info: hba, sup: arcIo}
	d.paged = func(r aacraid.PagedRead, p, size uint32) ([]byte, error) {
		if len(d.reads) == 2 {
			close(blocked)
			<-release // commande en attente de la carte
		}
		return referencePaged(r, p, size)
	}
	done := make(chan module.Result, 1)
	go func() { done <- withDev(d, nil).Collect(ctx, card("0000:01:00.0", "0"), ref) }()
	<-blocked
	cancel()
	select {
	case <-done:
		t.Fatal("Collect rend la main pendant une commande")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	var r module.Result
	select {
	case r = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Collect ne rend pas la main après la commande en cours")
	}
	if len(d.reads) != 2 || d.cmCall != 0 || !errors.Is(r.CollectErr, aacio.ErrStopped) {
		t.Fatalf("lectures %v, 708 %d, err %v", d.reads, d.cmCall, r.CollectErr)
	}
}

// Le nombre de clôtures confirmées par 0xc5 est reporté dans le résultat,
// y compris quand la collecte s'arrête avant la fin.
func TestCollectReportsCloseUnconfirmed(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	d := &fakeDev{info: hba, sup: arcIo, closeOther: 2}
	if r := withDev(d, nil).Collect(context.Background(), card("0000:01:00.0", "0"), ref); r.CloseUnconfirmed != 2 {
		t.Fatalf("%+v", r)
	}
	d = &fakeDev{info: hba, sup: arcIo, closeOther: 1, supErr: errors.New("EIO")}
	if r := withDev(d, nil).Collect(context.Background(), card("0000:01:00.0", "0"), ref); r.CloseUnconfirmed != 1 {
		t.Fatalf("retour anticipé : %+v", r)
	}
}

// Chaque collecteur en échec garde sa propre erreur : un ENOMEM sur la
// protection du cache ne masque pas une autre cause sur la configuration.
func TestCollectKeepsEveryError(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	nomem := fmt.Errorf("ioctl: %w", aacio.ErrDriverNoMemory)
	eio := errors.New("EIO sur la lecture 229")
	d := &fakeDev{info: hba, sup: arcIo, cp: &cpAnswer{err: nomem},
		paged: func(aacraid.PagedRead, uint32, uint32) ([]byte, error) { return nil, eio }}
	r := withDev(d, nil).Collect(context.Background(), card("0000:01:00.0", "0"), ref)
	if !errors.Is(r.CollectErr, aacio.ErrDriverNoMemory) || !errors.Is(r.CollectErr, eio) {
		t.Fatalf("erreurs perdues : %v", r.CollectErr)
	}
	if !strings.Contains(r.CollectErr.Error(), "cache protection") || !strings.Contains(r.CollectErr.Error(), "configuration") {
		t.Fatalf("collecteurs non nommés : %v", r.CollectErr)
	}
}

// Numéro de série ou firmware illisible : la clé des réponses gardées ne
// distinguerait pas deux cartes successives. Rien n'est gardé : la 706 est
// relue à chaque cycle et n'est pas déclarée servie depuis la mémoire ; au
// retour du numéro, la réponse lue sans lui n'est pas réutilisée.
func TestNoCacheWithIncompleteIdentity(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	for name, blank := range map[string]func(*module.Card){
		"numéro de série": func(c *module.Card) { c.Hosts[0].SerialNumber = "" },
		"firmware":        func(c *module.Card) { c.Hosts[0].Firmware = "" },
		"modèle":          func(c *module.Card) { c.Hosts[0].Model = "" },
	} {
		t.Run(name, func(t *testing.T) {
			c := card("0000:01:00.0", "0")
			blank(&c)
			f := &fakeDev{info: hba, sup: arcIo}
			m := withDev(f, nil)
			for i := 0; i < 2; i++ {
				if r := m.Collect(context.Background(), c, ref); len(r.Cached) != 0 || !r.Collected[CapabilitiesCollector] {
					t.Fatalf("cycle %d : %+v", i, r)
				}
			}
			if f.supCall != 2 {
				t.Fatalf("706 lue %d fois en 2 cycles sous identité incomplète", f.supCall)
			}
			m.Collect(context.Background(), card("0000:01:00.0", "0"), ref)
			if f.supCall != 3 {
				t.Fatalf("réponse lue sous identité incomplète réutilisée (706 lue %d fois)", f.supCall)
			}
		})
	}
}

// Collecteur temperature : la 703 est lue une fois et gardée (même clé que
// la 706), la 803 à chaque cycle, seulement si la carte annonce son capteur
// (AAC_SUPPORTED_HEAT_SENSOR), comme l'outil de référence.
func TestCollectTemperature(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	c := card("0000:01:00.0", "0")

	t.Run("nominal", func(t *testing.T) {
		f := &fakeDev{info: hba, sup: arcIo}
		m := withDev(f, nil)
		for i := 0; i < 2; i++ {
			r := m.Collect(context.Background(), c, ref)
			if !r.Collected[TemperatureCollector] || r.Temperature == nil ||
				*r.Temperature != (module.Temperature{Celsius: 49, ThresholdLo: 95, ThresholdHi: 100}) {
				t.Fatalf("cycle %d : %+v / %+v", i, r.Collected, r.Temperature)
			}
		}
		if f.aiCall != 1 || f.envCall != 2 {
			t.Fatalf("703 lue %d fois, 803 %d fois en 2 cycles (attendu 1 et 2)", f.aiCall, f.envCall)
		}
	})
	t.Run("capteur non annoncé", func(t *testing.T) {
		f := &fakeDev{info: hba, sup: arcIo, ai: &aacraid.AdapterInfo{SupportedOptions: 0x141f1d7c &^ aacraid.SupportedHeatSensor}}
		r := withDev(f, nil).Collect(context.Background(), c, ref)
		if f.envCall != 0 || r.Temperature != nil || !slices.Contains(r.Unsupported, TemperatureCollector) {
			t.Fatalf("803 envoyée %d fois sans capteur annoncé ; non pris en charge %v", f.envCall, r.Unsupported)
		}
		if _, seen := r.Collected[TemperatureCollector]; seen {
			t.Fatal("collecteur non pris en charge publié en succès ou en échec")
		}
	})
	t.Run("703 en échec", func(t *testing.T) {
		f := &fakeDev{info: hba, sup: arcIo, aiErr: errors.New("EIO")}
		m := withDev(f, nil)
		r := m.Collect(context.Background(), c, ref)
		if f.envCall != 0 || r.Collected[TemperatureCollector] || r.CollectErr == nil {
			t.Fatalf("803 envoyée sans 703 : %d ; %+v", f.envCall, r.Collected)
		}
		if _, seen := r.Collected[TemperatureCollector]; !seen {
			t.Fatal("échec de la 703 non publié")
		}
		// Protection du cache : lue seulement après une 703 valide ; publiée
		// en échec, pas en non prise en charge.
		if f.cpCall != 0 || slices.Contains(r.Unsupported, CacheProtectionCollector) {
			t.Fatalf("protection du cache sans 703 : %d lecture(s) ; non prises en charge %v", f.cpCall, r.Unsupported)
		}
		if ok, seen := r.Collected[CacheProtectionCollector]; !seen || ok {
			t.Fatalf("protection du cache non publiée en échec : %+v", r.Collected)
		}
		f.aiErr = nil
		if r := m.Collect(context.Background(), c, ref); !r.Collected[TemperatureCollector] || f.aiCall != 2 ||
			!r.Collected[CacheProtectionCollector] || f.cpCall != 1 {
			t.Fatalf("échec de la 703 gardé en mémoire : 703 %d, cache %d, %+v", f.aiCall, f.cpCall, r.Collected)
		}
	})
	t.Run("803 en échec", func(t *testing.T) {
		f := &fakeDev{info: hba, sup: arcIo, envErr: errors.New("EIO")}
		r := withDev(f, nil).Collect(context.Background(), c, ref)
		if r.Collected[TemperatureCollector] || r.Temperature != nil || r.CollectErr == nil {
			t.Fatalf("%+v / %+v", r.Collected, r.Temperature)
		}
		if !r.Collected["volumes"] {
			t.Fatal("échec de la température propagé aux autres collecteurs")
		}
	})
	t.Run("carte non qualifiée", func(t *testing.T) {
		unq, unqHBA := otherModel(hba)
		f := &fakeDev{info: unqHBA, sup: arcIo}
		r := withDev(f, nil).Collect(context.Background(), unq, ref)
		if f.aiCall != 0 || f.envCall != 0 || !slices.Contains(r.Unsupported, TemperatureCollector) {
			t.Fatalf("703/803 envoyées à une carte non qualifiée (%d, %d) ; %v", f.aiCall, f.envCall, r.Unsupported)
		}
	})
	t.Run("identité incomplète", func(t *testing.T) {
		inc := card("0000:01:00.0", "0")
		inc.Hosts[0].SerialNumber = ""
		f := &fakeDev{info: hba, sup: arcIo}
		m := withDev(f, nil)
		m.Collect(context.Background(), inc, ref)
		m.Collect(context.Background(), inc, ref)
		if f.aiCall != 2 {
			t.Fatalf("703 gardée sous identité incomplète : %d lectures en 2 cycles", f.aiCall)
		}
	})
}

// Une réponse 248 plus courte que les octets d'état (ici 300 octets, la
// carte de référence en renvoie 448) garde son inventaire : seuls les
// champs d'inventaire sont exigés.
func TestCollectDiskInventoryShortAnswer(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	d := &fakeDev{info: aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301},
		sup: arcIo, paged: func(r aacraid.PagedRead, p, size uint32) ([]byte, error) {
			b, err := referencePaged(r, p, size)
			if err == nil && r == aacraid.ReadDiskInfo {
				b = b[:300]
			}
			return b, err
		}}
	r := withDev(d, nil).Collect(context.Background(), card("0000:01:00.0", "0"), ref)
	if !r.Collected["disk_inventory"] || len(r.DiskInventory) != 8 || r.DiskInventory[0].Serial == "" {
		t.Fatalf("%+v", r)
	}
}

// Un rang invalide dans la liste des membres d'un seul volume (rang 5 pour
// un miroir de deux membres) refuse toute la configuration du cycle : ni
// volumes, ni états des disques ; ces collecteurs sont en échec. La chaîne
// des disques (inventaire, emplacement) en est indépendante (CDC, section
// 6.11). Les lectures indépendantes de la configuration
// (protection du cache, température) ne sont pas touchées.
func TestCollectInvalidRank(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	d := &fakeDev{info: aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301},
		sup: arcIo, paged: func(r aacraid.PagedRead, p, size uint32) ([]byte, error) {
			b, err := referencePaged(r, p, size)
			if err == nil && r == aacraid.ReadMemberList && p == 0x1d000004 {
				binary.LittleEndian.PutUint16(b[8+32+10:], 5)
			}
			return b, err
		}}
	r := withDev(d, nil).Collect(context.Background(), card("0000:01:00.0", "0"), ref)
	if r.CollectErr == nil || r.Volumes != nil || r.Disks != nil || len(r.DiskInventory) != 8 || len(r.DiskLocations) != 8 ||
		!r.Collected[DiskInventoryCollector] || !r.Collected[DiskLocationCollector] {
		t.Fatalf("%+v", r)
	}
	for _, name := range []string{"volumes", "disks"} {
		if r.Collected[name] {
			t.Fatalf("collecteur %s publié : %+v", name, r.Collected)
		}
	}
	if !r.Collected[CapabilitiesCollector] {
		t.Fatalf("capacités touchées : %+v", r.Collected)
	}
}

// Bout en bout : la 248 du disque 7 le déclare en panne (bit 0x1 de
// l'octet 68), les drapeaux de son membre restent nuls. Son miroir est
// dégradé, l'assemblage aussi, le disque est publié en panne. Si la 248 du
// disque 7 échoue, la panne déjà lue sur le disque 3 (même miroir) compte
// encore : miroir et assemblage dégradés, disque 3 en panne, disque 7 en
// ligne d'après ses membres ; l'inventaire est en échec.
func TestCollectDiskDeclaredFailed(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	for _, readable := range []bool{true, false} {
		d := &fakeDev{info: hba, sup: arcIo, paged: func(r aacraid.PagedRead, p, size uint32) ([]byte, error) {
			b, err := referencePaged(r, p, size)
			if err == nil && r == aacraid.ReadDiskInfo && p == 3 && !readable {
				b[68] |= 0x01 // lu avant l'échec : compte
			}
			if err == nil && r == aacraid.ReadDiskInfo && p == 7 {
				if !readable {
					return nil, errors.New("EIO")
				}
				b[68] |= 0x01
			}
			return b, err
		}}
		res := withDev(d, nil).Collect(context.Background(), card("0000:01:00.0", "0"), ref)
		want := map[string]string{"1d000004": "degraded", "1d000000": "degraded"}
		disk, disk3 := "failed", "online"
		if !readable {
			disk, disk3 = "online", "failed"
		}
		for _, v := range res.Volumes {
			if w, ok := want[v.ID]; ok && v.State != w {
				t.Errorf("248 lisible %v : volume %s %s, attendu %s", readable, v.ID, v.State, w)
			}
		}
		states := map[string]string{}
		for _, dk := range res.Disks {
			states[dk.ID] = dk.State
		}
		if states["d7"] != disk || states["d3"] != disk3 {
			t.Errorf("248 lisible %v : disques %v", readable, states)
		}
		seen := 0
		for _, v := range res.Volumes {
			if _, ok := want[v.ID]; ok {
				seen++
			}
		}
		if seen != 2 || len(res.Volumes) != 5 || res.Collected["disk_inventory"] != readable {
			t.Errorf("248 lisible %v : %+v", readable, res)
		}
	}
}

// Bout en bout : trois disques hors volume ajoutés à la configuration de
// référence (copies de la 248 du disque 0, renumérotées). Le disque 8
// (octet 68 = 0x50, qualificateur nul) est en ligne parce que la carte
// annonce AAC_FEATURE_JBOD ; le disque 9 (octet 68 = 0x74) est un secours ;
// le disque 10 (octet 68 = 0x10) est brut.
// Les membres gardent « Online ». Sans JBOD annoncé, le disque 8 est prêt.
func TestCollectNonMemberDisks(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	paged := func(r aacraid.PagedRead, p, size uint32) ([]byte, error) {
		switch {
		case r == aacraid.ReadDiskList:
			b, err := referencePaged(r, p, size)
			if err != nil {
				return nil, err
			}
			n := binary.LittleEndian.Uint32(b)
			b = append([]byte(nil), b[:4+4*n]...)
			for _, x := range []uint32{8, 9, 10} {
				b = binary.LittleEndian.AppendUint32(b, x)
			}
			binary.LittleEndian.PutUint32(b, n+3)
			return b, nil
		case r == aacraid.ReadDiskInfo && p >= 8 && p <= 10:
			b, err := referencePaged(r, 0, size)
			if err != nil {
				return nil, err
			}
			binary.LittleEndian.PutUint32(b[8:], p)
			binary.LittleEndian.PutUint32(b[16:], p)
			b[68] = map[uint32]byte{8: 0x50, 9: 0x74, 10: 0x10}[p]
			b[116] = 'X' + byte(p) // numéro de série distinct
			return b, nil
		}
		return referencePaged(r, p, size)
	}
	for _, jbod := range []bool{true, false} {
		sup := arcIo
		sup.FeatureBits = 0x0fff57df // valeur de la carte de référence (JBOD annoncé)
		if !jbod {
			sup.FeatureBits = arcIo.FeatureBits
		}
		d := &fakeDev{info: hba, sup: sup, paged: paged}
		res := withDev(d, nil).Collect(context.Background(), card("0000:01:00.0", "0"), ref)
		got := map[string][2]string{}
		for _, dk := range res.Disks {
			got[dk.ID] = [2]string{dk.State, dk.VendorState}
		}
		// Le disque 10 (octet 68 = 0x10) est brut par JBOD ; sans JBOD, prêt
		// (la carte n'annonce pas le bit 16 de SupportedOptions2).
		want8, want10 := [2]string{"online", "Online"}, [2]string{"passthrough", "Raw (Pass Through)"}
		if !jbod {
			want8, want10 = [2]string{"ready", "Ready"}, [2]string{"ready", "Ready"}
		}
		if got["d8"] != want8 || got["d10"] != want10 || got["d9"] != [2]string{"spare", "Hot Spare"} || got["d0"] != [2]string{"online", "Online"} || len(got) != 11 {
			t.Errorf("JBOD %v : %v", jbod, got)
		}
	}
}

// Bout en bout, cas particuliers de l'état des disques :
//   - référence : huit disques online, vendor_state Online ;
//   - 248 sans les octets 332-335 (300 octets) : repli par les membres,
//     vendor_state vide ;
//   - disque 7 qui n'est pas un disque (octet 81 bit 0x80) : membre perdu,
//     disque en panne, miroir dégradé ;
//   - option HBA natif annoncée par la 703 : un disque hors volume sans le bit
//     0x40 de l'octet 68 est brut ; 703 illisible : la règle n'est pas
//     appliquée.
func TestCollectDiskStateCases(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	collect := func(d *fakeDev) module.Result {
		return withDev(d, nil).Collect(context.Background(), card("0000:01:00.0", "0"), ref)
	}
	states := func(res module.Result) map[string][2]string {
		out := map[string][2]string{}
		for _, dk := range res.Disks {
			out[dk.ID] = [2]string{dk.State, dk.VendorState}
		}
		return out
	}
	r := collect(&fakeDev{info: hba, sup: arcIo})
	for n := 0; n < 8; n++ {
		if got := states(r)[fmt.Sprintf("d%d", n)]; got != [2]string{"online", "Online"} {
			t.Errorf("référence, d%d : %v", n, got)
		}
	}
	r = collect(&fakeDev{info: hba, sup: arcIo, paged: func(rd aacraid.PagedRead, p, size uint32) ([]byte, error) {
		b, err := referencePaged(rd, p, size)
		if err == nil && rd == aacraid.ReadDiskInfo {
			b = b[:300]
		}
		return b, err
	}})
	if got := states(r); got["d0"] != [2]string{"online", ""} || len(got) != 8 {
		t.Errorf("248 incomplète : %v", got)
	}
	// 248 incomplète, disque 2 (membre) déclaré en panne : publié en panne
	// (non-régression ; la branche du repli est prouvée par le disque 8 hors
	// volume, plus bas).
	r = collect(&fakeDev{info: hba, sup: arcIo, paged: func(rd aacraid.PagedRead, p, size uint32) ([]byte, error) {
		b, err := referencePaged(rd, p, size)
		if err == nil && rd == aacraid.ReadDiskInfo {
			if p == 2 {
				b[68] |= 0x01
			}
			b = b[:300]
		}
		return b, err
	}})
	if got := states(r); got["d2"] != [2]string{"failed", ""} || got["d0"] != [2]string{"online", ""} {
		t.Errorf("248 incomplète, disque en panne : %v", got)
	}
	r = collect(&fakeDev{info: hba, sup: arcIo, paged: func(rd aacraid.PagedRead, p, size uint32) ([]byte, error) {
		b, err := referencePaged(rd, p, size)
		if err == nil && rd == aacraid.ReadDiskInfo && p == 7 {
			b[81] = 0x80
		}
		return b, err
	}})
	vols := map[string]string{}
	for _, v := range r.Volumes {
		vols[v.ID] = v.State
	}
	if got := states(r); got["d7"] != [2]string{"failed", "Failed"} || vols["1d000004"] != "degraded" {
		t.Errorf("autre périphérique membre : %v, volumes %v", got, vols)
	}
	// Ni inventaire, ni emplacement, ni périphérique Linux pour lui.
	for _, i := range r.DiskInventory {
		if i.ID == "d7" {
			t.Errorf("autre périphérique dans l'inventaire : %+v", i)
		}
	}
	for _, l := range r.DiskLocations {
		if l.ID == "d7" {
			t.Errorf("autre périphérique localisé : %+v", l)
		}
	}
	if len(r.DiskInventory) != 7 || !r.Collected["disk_inventory"] {
		t.Errorf("inventaire : %d disques, %v", len(r.DiskInventory), r.Collected)
	}
	// Disque 8 hors volume, octet 68 = 0x10 (sans 0x40), carte sans JBOD.
	extra := func(rd aacraid.PagedRead, p, size uint32) ([]byte, error) {
		switch {
		case rd == aacraid.ReadDiskList:
			b, err := referencePaged(rd, p, size)
			if err != nil {
				return nil, err
			}
			n := binary.LittleEndian.Uint32(b)
			b = binary.LittleEndian.AppendUint32(append([]byte(nil), b[:4+4*n]...), 8)
			binary.LittleEndian.PutUint32(b, n+1)
			return b, nil
		case rd == aacraid.ReadDiskInfo && p == 8:
			b, err := referencePaged(rd, 0, size)
			if err != nil {
				return nil, err
			}
			binary.LittleEndian.PutUint32(b[8:], 8)
			binary.LittleEndian.PutUint32(b[16:], 8)
			b[68], b[116] = 0x10, 'Z'
			return b, nil
		}
		return referencePaged(rd, p, size)
	}
	// 248 incomplète et disque 8 hors volume déclaré en panne : publié en
	// panne malgré le repli.
	r = collect(&fakeDev{info: hba, sup: arcIo, paged: func(rd aacraid.PagedRead, p, size uint32) ([]byte, error) {
		b, err := extra(rd, p, size)
		if err == nil && rd == aacraid.ReadDiskInfo {
			if p == 8 {
				b[68] = 0x11
			}
			b = b[:300]
		}
		return b, err
	}})
	if got := states(r)["d8"]; got != [2]string{"failed", ""} {
		t.Errorf("248 incomplète, disque hors volume en panne : %v", got)
	}
	// Lecture interrompue : la 248 du disque 8 (hors volume) le déclare en
	// panne, puis celle du disque 9 échoue. Le disque 8 reste publié en
	// panne (réponse lue avant l'échec), l'inventaire est en échec.
	r = collect(&fakeDev{info: hba, sup: arcIo, paged: func(rd aacraid.PagedRead, p, size uint32) ([]byte, error) {
		if rd == aacraid.ReadDiskList {
			b, err := extra(rd, p, size)
			if err != nil {
				return nil, err
			}
			n := binary.LittleEndian.Uint32(b)
			b = binary.LittleEndian.AppendUint32(b, 9)
			binary.LittleEndian.PutUint32(b, n+1)
			return b, nil
		}
		if rd == aacraid.ReadDiskInfo && p == 9 {
			return nil, errors.New("EIO")
		}
		b, err := extra(rd, p, size)
		if err == nil && rd == aacraid.ReadDiskInfo && p == 8 {
			b[68] = 0x11
		}
		return b, err
	}})
	if got := states(r)["d8"]; got != [2]string{"failed", ""} || r.Collected["disk_inventory"] {
		t.Errorf("lecture interrompue, disque hors volume en panne : %v, %v", got, r.Collected)
	}
	// Même cas, mais le périphérique 8 est un fond de panier (type 0x0d) :
	// il n'est pas publié comme disque, même avec le bit de panne.
	r = collect(&fakeDev{info: hba, sup: arcIo, paged: func(rd aacraid.PagedRead, p, size uint32) ([]byte, error) {
		b, err := extra(rd, p, size)
		if err == nil && rd == aacraid.ReadDiskInfo {
			if p == 8 {
				b[68], b[80] = 0x11, 0x0d
			}
			b = b[:300]
		}
		return b, err
	}})
	if got, ok := states(r)["d8"]; ok {
		t.Errorf("248 incomplète, fond de panier en panne publié : %v", got)
	}
	native := aacraid.AdapterInfo{SupportedOptions: 1<<25 | aacraid.SupportedSupplementInfo}
	r = collect(&fakeDev{info: hba, sup: arcIo, ai: &native, paged: extra})
	if got := states(r)["d8"]; got != [2]string{"passthrough", "Raw (Pass Through)"} {
		t.Errorf("HBA natif : %v", got)
	}
	// 703 illisible : ouverture arrêtée, aucune lecture paginée, collecteurs
	// en échec.
	fd := &fakeDev{info: hba, sup: arcIo, ai: &native, aiErr: errors.New("EIO"), paged: extra}
	r = collect(fd)
	if len(states(r)) != 0 || len(fd.reads) != 0 || fd.countCall != 0 || r.Collected["disks"] || r.Collected[DiskInventoryCollector] {
		t.Errorf("703 illisible : états %v, lectures %v, comptages %d", states(r), fd.reads, fd.countCall)
	}
}

// CT_GET_CONTAINER_COUNT (CDC, section 6.11, forme 1) : MaxContainers fixe
// la taille demandée pour la liste des conteneurs (le faux périphérique
// refuse toute autre taille) ; la réponse est gardée comme la 706, sous la
// même clé d'identité, et relue quand cette clé change ou est incomplète.
func TestContainerCountSizesContainerList(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	c := card("0000:01:00.0", "0")
	// Taille autre que celle de la carte de référence : la liste de
	// référence (5 conteneurs) tient dans 8 + 4 × 5 octets.
	f := &fakeDev{info: hba, sup: arcIo, count: 5}
	m := withDev(f, nil)
	for i := 1; i <= 2; i++ {
		r := m.Collect(context.Background(), c, ref)
		if r.CollectErr != nil || !r.Collected["volumes"] || len(r.Volumes) == 0 {
			t.Fatalf("cycle %d : %v, %+v", i, r.CollectErr, r.Collected)
		}
	}
	if f.countCall != 1 {
		t.Fatalf("comptage lu %d fois en 2 cycles sous identité complète, 1 attendu", f.countCall)
	}
	// Nouvelle identité (carte remplacée, même modèle qualifié) : la
	// réponse gardée est oubliée.
	c2 := card("0000:01:00.0", "0")
	c2.Hosts[0].SerialNumber = "TESTSERIAL2"
	m.Collect(context.Background(), c2, ref)
	m.Collect(context.Background(), c, ref)
	if f.countCall != 3 {
		t.Fatalf("comptage lu %d fois après deux changements d'identité, 3 attendu", f.countCall)
	}
	// Identité incomplète : rien n'est gardé.
	c3 := card("0000:01:00.0", "0")
	c3.Hosts[0].SerialNumber = ""
	f3 := &fakeDev{info: hba, sup: arcIo}
	m3 := withDev(f3, nil)
	m3.Collect(context.Background(), c3, ref)
	m3.Collect(context.Background(), c3, ref)
	if f3.countCall != 2 {
		t.Fatalf("comptage lu %d fois en 2 cycles sous identité incomplète, 2 attendu", f3.countCall)
	}
}

// Comptage en échec, ou liste plus longue que MaxContainers : aucune liste
// des conteneurs n'est demandée (échec) ou ses données sont refusées ; les
// collecteurs des volumes sont en échec, la chaîne des disques est lue, et
// la réponse en échec n'est pas gardée.
func TestContainerCountFailures(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	c := card("0000:01:00.0", "0")
	// Volumes en échec, chaîne des disques maintenue (CDC, section 6.11).
	failed := func(r module.Result) bool {
		return r.Volumes == nil && r.Disks == nil && !r.Collected["volumes"] && !r.Collected["disks"] &&
			r.Collected[DiskInventoryCollector] && r.Collected[DiskLocationCollector] && r.CollectErr != nil && r.NodeValid
	}
	f := &fakeDev{info: hba, sup: arcIo, countErr: errors.New("EIO")}
	m := withDev(f, nil)
	r := m.Collect(context.Background(), c, ref)
	if !failed(r) || strings.Join(f.reads, " ") != "247-0 248-0 248-1 248-2 248-3 248-4 248-5 248-6 248-7" {
		t.Fatalf("comptage en échec : lectures %v, %+v", f.reads, r)
	}
	if !strings.Contains(r.CollectErr.Error(), "container count") {
		t.Fatalf("cause non signalée : %v", r.CollectErr)
	}
	f.countErr = nil
	if r := m.Collect(context.Background(), c, ref); r.CollectErr != nil || f.countCall != 2 {
		t.Fatalf("comptage non relu après un échec : %d appels, %v", f.countCall, r.CollectErr)
	}
	// 5 conteneurs dans la liste de référence, MaxContainers = 4.
	f4 := &fakeDev{info: hba, sup: arcIo, count: 4}
	if r := withDev(f4, nil).Collect(context.Background(), c, ref); !failed(r) || !strings.Contains(r.CollectErr.Error(), "MaxContainers 4") {
		t.Fatalf("liste plus longue que MaxContainers acceptée : %+v", r)
	}
}

// Dépendances des résultats (CDC, section 6.11) : chaîne des volumes
// (comptage, 229, les 234) et chaîne des disques (247, les 248, la 708)
// lues indépendamment, dans l'ordre nominal ; listes des membres (249)
// seulement si les deux ont réussi. Pour chaque échec : lectures envoyées,
// dans l'ordre, et collecteurs publiés.
func TestReadDependencies(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	c := card("0000:01:00.0", "0")
	run := func(failing string) (*fakeDev, module.Result) {
		d := &fakeDev{info: hba, sup: arcIo, paged: func(r aacraid.PagedRead, p, size uint32) ([]byte, error) {
			if fmt.Sprintf("%d-%x", r, p) == failing {
				return nil, errors.New("EIO")
			}
			return referencePaged(r, p, size)
		}}
		return d, withDev(d, nil).Collect(context.Background(), c, ref)
	}
	nominal, r := run("")
	if r.CollectErr != nil {
		t.Fatal(r.CollectErr)
	}
	// Ordre nominal conservé : 229, les 234, 247, les 249, puis les 248.
	want := "229-0 234-0 234-fc 234-fd 234-fe 234-ff 247-0 249-1d000000 249-1d000004 249-1d000003 249-1d000002 249-1d000001 " +
		"248-0 248-1 248-2 248-3 248-4 248-5 248-6 248-7"
	if got := strings.Join(nominal.reads, " "); got != want || nominal.countCall != 1 || nominal.cmCall != 1 {
		t.Fatalf("séquence nominale : %s (comptage %d, 708 %d)", got, nominal.countCall, nominal.cmCall)
	}
	upTo := func(last string) []string {
		for i, rd := range nominal.reads {
			if rd == last {
				return append([]string(nil), nominal.reads[:i+1]...)
			}
		}
		t.Fatalf("%s absent de la séquence nominale", last)
		return nil
	}
	var diskChain, inv []string
	for _, rd := range nominal.reads {
		if strings.HasPrefix(rd, "247-") || strings.HasPrefix(rd, "248-") {
			diskChain = append(diskChain, rd)
		}
		if strings.HasPrefix(rd, "248-") {
			inv = append(inv, rd)
		}
	}
	cases := []struct {
		failing           string
		reads             []string
		volumes, inv, loc bool
		cm                int
	}{
		{"229-0", append([]string{"229-0"}, diskChain...), false, true, true, 1},
		{"234-fd", append(upTo("234-fd"), diskChain...), false, true, true, 1},
		{"247-0", upTo("247-0"), false, false, false, 0},
		{"249-1d000002", append(upTo("249-1d000002"), inv...), false, true, true, 1},
		{"248-3", upTo("248-3"), true, false, false, 0},
	}
	for _, tc := range cases {
		d, r := run(tc.failing)
		if got, w := strings.Join(d.reads, " "), strings.Join(tc.reads, " "); got != w {
			t.Errorf("%s en échec : lectures\n%s\nattendu\n%s", tc.failing, got, w)
		}
		if r.Collected["volumes"] != tc.volumes || r.Collected["disks"] != tc.volumes ||
			r.Collected[DiskInventoryCollector] != tc.inv || r.Collected[DiskLocationCollector] != tc.loc || d.cmCall != tc.cm || r.CollectErr == nil {
			t.Errorf("%s en échec : collecteurs %v, 708 %d, erreur %v", tc.failing, r.Collected, d.cmCall, r.CollectErr)
		}
		if tc.volumes != (len(r.Volumes) == 5) || tc.inv != (len(r.DiskInventory) == 8) {
			t.Errorf("%s en échec : %d volumes, %d disques inventoriés publiés", tc.failing, len(r.Volumes), len(r.DiskInventory))
		}
	}
}

// Prérequis des transferts paginés (CDC, section 6.11) : bit 13 de
// FeatureBits ou bit 15 de SupportedOptions (formes non implémentées) :
// collecteurs non pris en charge, aucune lecture ; sans le bit 9 de
// FeatureBits : ni comptage ni lecture de conteneurs, collecteurs d'état non
// pris en charge, chaîne des disques lue.
func TestPagedPrerequisites(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	c := card("0000:01:00.0", "0")
	unsupported := func(r module.Result, cols ...string) bool {
		set := map[string]bool{}
		for _, u := range r.Unsupported {
			set[u] = true
		}
		for _, col := range cols {
			if !set[col] {
				return false
			}
			if _, published := r.Collected[col]; published {
				return false
			}
		}
		return true
	}
	all := []string{"volumes", "disks", DiskInventoryCollector, DiskLocationCollector}
	high := arcIo
	high.FeatureBits |= aacraid.FeaturePagedHighBit
	large := aacraid.AdapterInfo{SupportedOptions: aacraid.SupportedHeatSensor | aacraid.SupportedLargeTransfers}
	for name, d := range map[string]*fakeDev{
		"bit 13 de FeatureBits":      {info: hba, sup: high},
		"bit 15 de SupportedOptions": {info: hba, sup: arcIo, ai: &large},
	} {
		r := withDev(d, nil).Collect(context.Background(), c, ref)
		if !unsupported(r, all...) || len(d.reads) != 0 || d.countCall != 0 || d.cmCall != 0 || r.CollectErr != nil {
			t.Errorf("%s : lectures %v, comptage %d, %+v", name, d.reads, d.countCall, r)
		}
		if !r.Collected[CacheProtectionCollector] || !r.Collected[TemperatureCollector] {
			t.Errorf("%s : lectures indépendantes touchées : %v", name, r.Collected)
		}
	}
	// Sans bit 9, avec et sans bit 8 (carte des connecteurs).
	for _, bit8 := range []bool{true, false} {
		noVol := arcIo
		noVol.FeatureBits = 0
		if bit8 {
			noVol.FeatureBits = aacraid.FeatureConnectorMap
		}
		d := &fakeDev{info: hba, sup: noVol}
		r := withDev(d, nil).Collect(context.Background(), c, ref)
		if !unsupported(r, "volumes", "disks") || d.countCall != 0 || r.CollectErr != nil ||
			strings.Join(d.reads, " ") != "247-0 248-0 248-1 248-2 248-3 248-4 248-5 248-6 248-7" ||
			!r.Collected[DiskInventoryCollector] || len(r.DiskInventory) != 8 || r.Volumes != nil || r.Disks != nil {
			t.Errorf("sans bit 9 (bit 8 : %v) : lectures %v, comptage %d, %+v", bit8, d.reads, d.countCall, r)
		}
		if bit8 && (!r.Collected[DiskLocationCollector] || d.cmCall != 1 || len(r.DiskLocations) != 8) {
			t.Errorf("sans bit 9, avec bit 8 : 708 %d, %+v", d.cmCall, r)
		}
		if !bit8 && (!unsupported(r, DiskLocationCollector) || d.cmCall != 0 || r.DiskLocations != nil) {
			t.Errorf("sans bits 8 et 9 : 708 %d, %+v", d.cmCall, r)
		}
	}
	// Sans bit 8, l'emplacement reste non pris en charge même si la liste
	// des disques ou une information de disque échoue.
	for _, failing := range []string{"247-0", "248-3"} {
		noMap := arcIo
		noMap.FeatureBits = aacraid.FeatureContainerReads
		d := &fakeDev{info: hba, sup: noMap, paged: func(r aacraid.PagedRead, p, size uint32) ([]byte, error) {
			if fmt.Sprintf("%d-%x", r, p) == failing {
				return nil, errors.New("EIO")
			}
			return referencePaged(r, p, size)
		}}
		r := withDev(d, nil).Collect(context.Background(), c, ref)
		if !unsupported(r, DiskLocationCollector) || r.Collected[DiskInventoryCollector] || d.cmCall != 0 {
			t.Errorf("sans bit 8, %s en échec : %+v", failing, r)
		}
	}
	// 703 en échec : ouverture arrêtée avant la 706 ; aucune autre commande,
	// tous les collecteurs en échec (aucun n'est non pris en charge : la 706
	// n'a pas pu les écarter).
	d703 := &fakeDev{info: hba, sup: arcIo, aiErr: errors.New("EIO")}
	r703 := withDev(d703, nil).Collect(context.Background(), c, ref)
	if len(r703.Unsupported) != 0 || len(d703.reads) != 0 || d703.countCall != 0 || d703.cmCall != 0 || d703.supCall != 0 || d703.cpCall != 0 || d703.envCall != 0 {
		t.Errorf("703 en échec : 706 %d, cache %d, 803 %d, lectures %v, %+v", d703.supCall, d703.cpCall, d703.envCall, d703.reads, r703)
	}
	for _, col := range []string{CapabilitiesCollector, "volumes", "disks", DiskInventoryCollector, DiskLocationCollector, CacheProtectionCollector, TemperatureCollector} {
		if ok, published := r703.Collected[col]; !published || ok {
			t.Errorf("703 en échec : %s non publié en échec : %v", col, r703.Collected)
		}
	}
	// Bit 9 sans bit 8 : volumes lus, carte des connecteurs non envoyée.
	noMap := arcIo
	noMap.FeatureBits = aacraid.FeatureContainerReads
	d := &fakeDev{info: hba, sup: noMap}
	r := withDev(d, nil).Collect(context.Background(), c, ref)
	if !unsupported(r, DiskLocationCollector) || d.cmCall != 0 || !r.Collected["volumes"] || !r.Collected[DiskInventoryCollector] || r.CollectErr != nil {
		t.Errorf("sans bit 8 : 708 %d, %+v", d.cmCall, r)
	}
}

// Une liste des conteneurs qui annonce plus d'entrées que MaxContainers est
// refusée sans lire de morceau au-delà de ce que MaxContainers permet.
func TestContainerListCappedByCount(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	// 300 entrées annoncées (1 208 octets, trois morceaux) pour
	// MaxContainers 256 (la liste autorisée, 1 032 octets, en demanderait
	// trois aussi) : refus après le premier morceau.
	big := make([]byte, 8+4*300)
	binary.LittleEndian.PutUint32(big[4:], 300)
	for i := 0; i < 300; i++ {
		binary.LittleEndian.PutUint32(big[8+4*i:], uint32(i))
	}
	d := &fakeDev{info: hba, sup: arcIo, count: 256, paged: func(r aacraid.PagedRead, p, size uint32) ([]byte, error) {
		if r == aacraid.ReadContainerList {
			return big, nil
		}
		return referencePaged(r, p, size)
	}}
	r := withDev(d, nil).Collect(context.Background(), card("0000:01:00.0", "0"), ref)
	if r.Collected["volumes"] || !strings.Contains(fmt.Sprint(r.CollectErr), "300 entries, MaxContainers 256") {
		t.Fatalf("liste de 300 entrées acceptée pour MaxContainers 256 : %+v", r)
	}
	if n := d.chunks["229-0"]; n != 1 {
		t.Fatalf("%d morceaux de la liste lus, 1 attendu", n)
	}
}

// La table des modèles qualifiés de docs/aacraid-interface.md (section 8)
// reprend exactement la liste du code : une ligne par modèle, mêmes
// identifiants PCI et mêmes firmwares validés.
func TestQualificationTableDocumented(t *testing.T) {
	doc, err := os.ReadFile("../../../docs/aacraid-interface.md")
	if err != nil {
		t.Fatal(err)
	}
	_, table, ok := strings.Cut(string(doc), "| Modèle | Identifiants PCI | Firmwares validés | Validé le |\n|---|---|---|---|\n")
	if !ok {
		t.Fatal("table des modèles qualifiés absente")
	}
	table, _, _ = strings.Cut(table, "\n\n")
	rows := strings.Split(strings.TrimSpace(table), "\n")
	if len(rows) != len(Qualifications) {
		t.Fatalf("%d ligne(s) documentée(s), %d modèle(s) qualifié(s)", len(rows), len(Qualifications))
	}
	for _, q := range Qualifications {
		fws := make([]string, len(q.Firmwares))
		for i, f := range q.Firmwares {
			fws[i] = "`" + f + "`"
		}
		want := fmt.Sprintf("| %s | `%s` `%s` `%s` `%s` | %s | ", q.Model, q.Vendor, q.Device, q.SubsystemVendor, q.SubsystemDevice, strings.Join(fws, ", "))
		if !slices.ContainsFunc(rows, func(r string) bool { return strings.HasPrefix(r, want) }) {
			t.Fatalf("modèle %s absent ou différent de la table : %q", q.Model, want)
		}
	}
}

// Carte non qualifiée autorisée par l'option, 706 en échec : l'ouverture
// (704, 701, 703) a eu lieu, aucune autre lecture n'est tentée ; tous les
// collecteurs, protection du cache comprise, sont publiés en échec.
func TestAllowedUnqualifiedSupplementFailure(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	c, h := otherModel(hba)
	d := &fakeDev{info: h, supErr: errors.New("EIO")}
	m := withDev(d, nil)
	m.AllowUnqualified()
	r := m.Collect(context.Background(), c, ref)
	if d.pausedCall != 1 || d.revCall != 1 || d.aiCall != 1 || d.supCall != 1 {
		t.Fatalf("ouverture : 704 %d, 701 %d, 703 %d, 706 %d (1 attendu chacune)", d.pausedCall, d.revCall, d.aiCall, d.supCall)
	}
	if d.cpCall != 0 || d.envCall != 0 || len(d.reads) != 0 || d.countCall != 0 {
		t.Fatalf("lectures après l'échec de la 706 : cache %d, 803 %d, paginées %v, comptage %d", d.cpCall, d.envCall, d.reads, d.countCall)
	}
	if len(r.Unsupported) != 0 {
		t.Fatalf("%+v / non pris en charge %v", r.Collected, r.Unsupported)
	}
	for _, col := range []string{"volumes", "disks", DiskInventoryCollector, DiskLocationCollector, TemperatureCollector, CacheProtectionCollector} {
		if ok, tried := r.Collected[col]; !tried || ok {
			t.Fatalf("%s non publié en échec : %+v", col, r.Collected)
		}
	}
	if d.validated || !d.validatedSet {
		t.Fatal("couple validé transmis pour un modèle non qualifié")
	}
}

// Même carte, firmware mis à jour entre deux cycles du même module : le
// premier cycle transmet un couple validé, le second un couple non validé ;
// la protection du cache est lue aux deux.
func TestFirmwareChangeLeavesValidatedPair(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	d := &fakeDev{info: hba, sup: arcIo}
	m := withDev(d, nil)
	c := card("0000:01:00.0", "0")
	m.Collect(context.Background(), c, ref)
	if !d.validated || d.cpCall == 0 {
		t.Fatalf("premier cycle : couple validé %v, protection du cache %d", d.validated, d.cpCall)
	}
	calls := d.cpCall
	c.Hosts = append([]sysfs.SCSIHost(nil), c.Hosts...)
	c.Hosts[0].Firmware = "5.3-0[19205]"
	r := m.Collect(context.Background(), c, ref)
	if d.validated || d.cpCall <= calls || len(r.Unsupported) != 0 || !r.Collected["volumes"] {
		t.Fatalf("second cycle : couple validé %v, protection du cache %d→%d, %v / %+v", d.validated, calls, d.cpCall, r.Unsupported, r.Collected)
	}
}

// Ouverture de la carte comme l'outil de référence (CDC, section 6.11) :
// 704, 701, 703, puis 706 seulement si la 703 annonce le bit 16. Une carte
// en pause ou une révision refusée arrête la collecte avant toute autre
// commande ; la 704 part à chaque cycle, la 701 acceptée est gardée.
func TestOpeningSequence(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	c := card("0000:01:00.0", "0")
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	all := []string{CapabilitiesCollector, "volumes", "disks", DiskInventoryCollector, DiskLocationCollector, CacheProtectionCollector, TemperatureCollector}
	unsupported := func(r module.Result, cols ...string) bool {
		for _, col := range cols {
			if _, published := r.Collected[col]; published || !slices.Contains(r.Unsupported, col) {
				return false
			}
		}
		return true
	}
	stopped := func(name string, d *fakeDev, r module.Result, wantRev, wantAI int) {
		t.Helper()
		if d.revCall != wantRev || d.aiCall != wantAI || d.supCall+d.cpCall+d.envCall+d.countCall+d.cmCall != 0 || len(d.reads) != 0 {
			t.Errorf("%s : 701 %d, 703 %d, 706 %d, cache %d, 803 %d, paginées %v", name, d.revCall, d.aiCall, d.supCall, d.cpCall, d.envCall, d.reads)
		}
		if r.CollectErr == nil || len(r.Unsupported) != 0 {
			t.Errorf("%s : erreur %v, non pris en charge %v", name, r.CollectErr, r.Unsupported)
		}
		for _, col := range all {
			if ok, seen := r.Collected[col]; !seen || ok {
				t.Errorf("%s : %s non publié en échec : %v", name, col, r.Collected)
			}
		}
	}
	d := &fakeDev{info: hba, sup: arcIo, paused: true}
	r := withDev(d, nil).Collect(context.Background(), c, ref)
	stopped("carte en pause", d, r, 0, 0)
	if !errors.Is(r.CollectErr, aacraid.ErrAdapterPaused) {
		t.Errorf("carte en pause : %v", r.CollectErr)
	}
	d = &fakeDev{info: hba, sup: arcIo, pausedErr: errors.New("EIO")}
	stopped("704 en échec", d, withDev(d, nil).Collect(context.Background(), c, ref), 0, 0)
	for name, rv := range map[string]aacraid.Revision{
		"révision incompatible": {Compatible: false, FirmwareBuild: 19204},
		"build sous le minimum": {Compatible: true, FirmwareBuild: aacraid.MinFirmwareBuild - 1},
	} {
		d = &fakeDev{info: hba, sup: arcIo, rev: &rv}
		r = withDev(d, nil).Collect(context.Background(), c, ref)
		stopped(name, d, r, 1, 0)
		if !errors.Is(r.CollectErr, aacraid.ErrRevisionRefused) {
			t.Errorf("%s : %v", name, r.CollectErr)
		}
	}
	d = &fakeDev{info: hba, sup: arcIo, revErr: errors.New("EIO")}
	stopped("701 en échec", d, withDev(d, nil).Collect(context.Background(), c, ref), 1, 0)

	// Build au minimum : accepté ; 701 gardée, 704 renvoyée à chaque cycle.
	min := aacraid.Revision{Compatible: true, FirmwareBuild: aacraid.MinFirmwareBuild}
	d = &fakeDev{info: hba, sup: arcIo, rev: &min}
	m := withDev(d, nil)
	for i := 0; i < 2; i++ {
		if r := m.Collect(context.Background(), c, ref); r.CollectErr != nil {
			t.Fatalf("cycle %d : %v", i+1, r.CollectErr)
		}
	}
	if d.pausedCall != 2 || d.revCall != 1 || d.aiCall != 1 || d.supCall != 1 {
		t.Errorf("deux cycles : 704 %d (2), 701 %d (1), 703 %d (1), 706 %d (1)", d.pausedCall, d.revCall, d.aiCall, d.supCall)
	}
	// Révision refusée : non gardée, relue au cycle suivant.
	bad := aacraid.Revision{Compatible: false}
	d = &fakeDev{info: hba, sup: arcIo, rev: &bad}
	m = withDev(d, nil)
	m.Collect(context.Background(), c, ref)
	d.rev = nil
	if r := m.Collect(context.Background(), c, ref); r.CollectErr != nil || d.revCall != 2 {
		t.Errorf("refus gardé : 701 %d, %v", d.revCall, r.CollectErr)
	}

	// 703 sans bit 16 : pas de 706 (ni l'outil ni le pilote ne l'envoient) ;
	// capacités et lectures d'état non prises en charge ; protection du
	// cache et température lues.
	noSup := aacraid.AdapterInfo{SupportedOptions: 0x141e1d7c} // valeur capturée sans le bit 16, écrite en clair
	d = &fakeDev{info: hba, sup: arcIo, ai: &noSup}
	r = withDev(d, nil).Collect(context.Background(), c, ref)
	if d.supCall != 0 || len(d.reads) != 0 || d.countCall != 0 || d.cpCall == 0 || d.envCall != 1 {
		t.Errorf("sans bit 16 : 706 %d, paginées %v, comptage %d, cache %d, 803 %d", d.supCall, d.reads, d.countCall, d.cpCall, d.envCall)
	}
	if !unsupported(r, CapabilitiesCollector, "volumes", "disks", DiskInventoryCollector, DiskLocationCollector) ||
		!r.Collected[CacheProtectionCollector] || !r.Collected[TemperatureCollector] || r.CollectErr != nil {
		t.Errorf("sans bit 16 : %+v", r)
	}
	if _, seen := r.Collected[CapabilitiesCollector]; seen || len(r.Cached) != 0 {
		t.Errorf("sans bit 16 : capacités publiées %v, gardées %v", r.Collected, r.Cached)
	}
}

// Carte autorisée par -allow-unqualified : mêmes refus d'ouverture que le
// modèle qualifié (704 en pause, 701 refusée), rien n'est envoyé ensuite.
func TestAllowedUnqualifiedOpeningRefusals(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0301}
	c, h := otherModel(hba)
	refused := aacraid.Revision{Compatible: false, FirmwareBuild: 19204}
	for name, d := range map[string]*fakeDev{
		"carte en pause":   {info: h, sup: arcIo, paused: true},
		"révision refusée": {info: h, sup: arcIo, rev: &refused},
	} {
		m := withDev(d, nil)
		m.AllowUnqualified()
		r := m.Collect(context.Background(), c, ref)
		if d.pausedCall != 1 || d.aiCall+d.supCall+d.cpCall+d.envCall+d.countCall+d.cmCall != 0 || len(d.reads) != 0 || r.CollectErr == nil {
			t.Errorf("%s : 704 %d, 701 %d, 703 %d, 706 %d, cache %d, paginées %v ; %v", name, d.pausedCall, d.revCall, d.aiCall, d.supCall, d.cpCall, d.reads, r.CollectErr)
		}
	}
}
