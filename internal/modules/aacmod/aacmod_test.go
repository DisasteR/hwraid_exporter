package aacmod

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/DisasteR/hwraid_exporter/internal/aacraid"
	"github.com/DisasteR/hwraid_exporter/internal/module"
	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
)

// Le module doit satisfaire le contrat.
var _ module.Module = (*Module)(nil)

func card(addr, uid string) module.Card {
	return module.Card{
		PCI:   sysfs.PCIFunction{Address: addr, Driver: "aacraid", Class: "0x010400"},
		Hosts: []sysfs.SCSIHost{{Name: "host3", Number: 3, ProcName: "aacraid", UniqueID: uid}},
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
	info    aacraid.HBAInfo
	err     error
	sup     aacraid.SupplementInfo
	supErr  error
	supCall int
	closed  bool
	cp      *cpAnswer
	cpCall  int
	paged   func(r aacraid.PagedRead, param, size uint32) ([]byte, error)
	reads   []string
}

func (f *fakeDev) HBAInfo() (aacraid.HBAInfo, error) { return f.info, f.err }
func (f *fakeDev) SupplementInfo() (aacraid.SupplementInfo, error) {
	f.supCall++
	return f.sup, f.supErr
}
func (f *fakeDev) Close() error { f.closed = true; return nil }

func (f *fakeDev) CacheProtection() (bool, aacraid.ZMMStatus, error) {
	f.cpCall++
	if f.cp == nil {
		return true, aacraid.ZMMStatus{Word: 0x40000081, State: aacraid.CacheProtectionFailed}, nil
	}
	return f.cp.described, f.cp.st, f.cp.err
}

func (f *fakeDev) ReadPaged(r aacraid.PagedRead, param, size uint32, need func([]byte) int) ([]byte, error) {
	f.reads = append(f.reads, fmt.Sprintf("%d-%x", r, param))
	if f.paged == nil {
		return referencePaged(r, param, size)
	}
	return f.paged(r, param, size)
}

// referencePaged sert les données enregistrées sur l'hôte de référence
// (internal/aacraid/testdata), et vérifie la taille demandée pour une liste
// de membres.
func referencePaged(r aacraid.PagedRead, param, size uint32) ([]byte, error) {
	s, err := os.ReadFile(fmt.Sprintf("../../aacraid/testdata/%d-%x.hex", r, param))
	if err != nil {
		return nil, err
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(s)))
	if err != nil {
		return nil, err
	}
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

var arcIo = aacraid.SupplementInfo{Version: 15, SupportedOptions2: 0x00c8693c}

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

	ok := &fakeDev{info: aacraid.HBAInfo{AdapterNumber: 0, Bus: 1, Device: 0, Function: 0}, sup: arcIo}
	if r := withDev(ok, nil).Collect(c, ref); !r.NodeValid || r.Err != nil || !ok.closed {
		t.Fatalf("carte cohérente : %+v (fermé=%v)", r, ok.closed)
	}
	other := &fakeDev{info: aacraid.HBAInfo{AdapterNumber: 1, Bus: 2}}
	if r := withDev(other, nil).Collect(c, ref); r.NodeValid || r.Err == nil || !other.closed {
		t.Fatalf("nœud d'une autre carte jugé valide : %+v", r)
	}
	failing := &fakeDev{err: errors.New("EPERM")}
	if r := withDev(failing, nil).Collect(c, ref); r.NodeValid || r.Err == nil || !failing.closed {
		t.Fatalf("échec de lecture non remonté : %+v", r)
	}
	if r := withDev(nil, errors.New("nœud incohérent")).Collect(c, ref); r.NodeValid || r.Err == nil {
		t.Fatalf("échec d'ouverture non remonté : %+v", r)
	}
	if r := withDev(ok, nil).Collect(c, nil); r.NodeValid || r.Err == nil {
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
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1}

	d := &fakeDev{info: hba, sup: arcIo}
	m := withDev(d, nil)
	for i := 0; i < 3; i++ {
		if r := m.Collect(c, ref); !r.NodeValid || r.CollectErr != nil || len(r.Unsupported) != 0 {
			t.Fatalf("cycle %d : %+v", i, r)
		}
	}
	if d.supCall != 1 {
		t.Fatalf("706 envoyée %d fois en 3 cycles, attendu 1", d.supCall)
	}

	for name, sup := range map[string]aacraid.SupplementInfo{
		"bit absent": {Version: 15, SupportedOptions2: 0x00c86934},
		"version 1":  {Version: 1, SupportedOptions2: 0x08},
	} {
		r := withDev(&fakeDev{info: hba, sup: sup}, nil).Collect(c, ref)
		if !r.NodeValid || len(r.Unsupported) != 2 || r.Unsupported[0] != "volumes" || r.Unsupported[1] != "disks" {
			t.Fatalf("%s : %+v", name, r)
		}
	}

	f := &fakeDev{info: hba, supErr: errors.New("EIO")}
	m = withDev(f, nil)
	if r := m.Collect(c, ref); !r.NodeValid || r.CollectErr == nil || r.Unsupported != nil {
		t.Fatalf("échec 706 : %+v", r)
	}
	f.supErr, f.sup = nil, arcIo
	if r := m.Collect(c, ref); r.CollectErr != nil || f.supCall != 2 {
		t.Fatalf("échec gardé en cache : %+v (appels=%d)", r, f.supCall)
	}

	// Nœud d'une autre carte : aucune commande firmware n'est envoyée.
	other := &fakeDev{info: aacraid.HBAInfo{AdapterNumber: 1, Bus: 2}, sup: arcIo}
	withDev(other, nil).Collect(c, ref)
	if other.supCall != 0 {
		t.Fatal("706 envoyée sur un nœud qui ne désigne pas la carte")
	}
}

// hwraid_collection_success : la lecture des capacités est rapportée à
// chaque cycle (1 si la valeur est connue, 0 si la lecture vient d'échouer),
// et n'est pas rapportée si elle n'a pas été tentée (nœud d'une autre carte).
func TestCollectReportsCapabilitiesCollection(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	c := card("0000:01:00.0", "0")
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1}

	f := &fakeDev{info: hba, supErr: errors.New("EIO")}
	m := withDev(f, nil)
	if r := m.Collect(c, ref); len(r.Collected) != 1 || r.Collected[CapabilitiesCollector] {
		t.Fatalf("échec : %+v", r.Collected)
	}
	f.supErr, f.sup = nil, arcIo
	for i := 0; i < 2; i++ { // lecture réelle, puis valeur gardée
		if r := m.Collect(c, ref); !r.Collected[CapabilitiesCollector] {
			t.Fatalf("cycle %d : %+v", i, r.Collected)
		}
	}
	other := &fakeDev{info: aacraid.HBAInfo{AdapterNumber: 1, Bus: 2}, sup: arcIo}
	if r := withDev(other, nil).Collect(c, ref); r.Collected != nil {
		t.Fatalf("lecture non tentée rapportée : %+v", r.Collected)
	}
}

// Protection du cache : lue à chaque cycle (l'état change sans
// redémarrage), traduite en état normalisé ; un format non décrit ou un
// autre type de module est « non pris en charge », un échec n'invente rien.
func TestCollectCacheProtection(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	c := card("0000:01:00.0", "0")
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1}
	run := func(a *cpAnswer) (module.Result, *fakeDev) {
		d := &fakeDev{info: hba, sup: arcIo, cp: a}
		return withDev(d, nil).Collect(c, ref), d
	}

	r, d := run(nil) // état de l'hôte de référence
	if r.CacheProtection == nil || !r.CacheProtection.Present || r.CacheProtection.State != "failed" || !r.Collected[CacheProtectionCollector] {
		t.Fatalf("référence : %+v", r)
	}
	m := withDev(d, nil)
	m.Collect(c, ref)
	m.Collect(c, ref)
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
		{"optimal", cpAnswer{described: true, st: aacraid.ZMMStatus{State: aacraid.CacheProtectionOptimal}}, &module.CacheProtection{Present: true, Type: "zmm", State: "optimal"}, ptr(true), false},
		{"non installé", cpAnswer{described: true, st: aacraid.ZMMStatus{State: aacraid.CacheProtectionNotInstalled}}, &module.CacheProtection{Type: "zmm"}, ptr(true), false},
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
	withDev(f, nil).Collect(c, ref)
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
	d := &fakeDev{info: aacraid.HBAInfo{AdapterNumber: 0, Bus: 1}, sup: arcIo}
	r := withDev(d, nil).Collect(c, ref)
	if !r.Collected["volumes"] || !r.Collected["disks"] || r.CollectErr != nil {
		t.Fatalf("%+v", r)
	}
	if len(r.Volumes) != 5 || len(r.Disks) != 8 {
		t.Fatalf("%d volumes, %d disques", len(r.Volumes), len(r.Disks))
	}
	top := r.Volumes[0]
	if top.ID != "410734af" || top.Name != "Array0" || top.Type != "assembly" || top.Parent != "" ||
		top.CapacityBytes != 62495109120*512 || top.State != "optimal" || len(top.Members) != 4 {
		t.Fatalf("volume : %+v", top)
	}
	for _, v := range r.Volumes[1:] {
		if v.Type != "mirror" || v.Parent != "410734af" || v.State != "optimal" || len(v.Members) != 2 {
			t.Fatalf("miroir : %+v", v)
		}
	}
	for i, dk := range r.Disks {
		if dk.ID != fmt.Sprintf("d%d", i) || dk.State != "online" {
			t.Fatalf("disque : %+v", dk)
		}
	}
	// Séquence : liste, 5 informations, disques, 5 listes de membres.
	if len(d.reads) != 12 || d.reads[0] != "229-0" || d.reads[6] != "247-0" {
		t.Fatalf("lectures %v", d.reads)
	}
}

// Une lecture en échec annule toute la configuration du cycle ; une carte
// sans l'interface annoncée n'est pas interrogée.
func TestCollectVolumesFailureAndGate(t *testing.T) {
	ref := []module.NodeRef{{Path: "/dev/hwraid_exporter/aac0", Dev: 1}}
	c := card("0000:01:00.0", "0")
	hba := aacraid.HBAInfo{AdapterNumber: 0, Bus: 1}
	for _, failing := range []string{"229-0", "234-fd", "247-0", "249-410734b2"} {
		d := &fakeDev{info: hba, sup: arcIo, paged: func(r aacraid.PagedRead, p, size uint32) ([]byte, error) {
			if fmt.Sprintf("%d-%x", r, p) == failing {
				return nil, errors.New("EIO")
			}
			return referencePaged(r, p, size)
		}}
		r := withDev(d, nil).Collect(c, ref)
		if r.Volumes != nil || r.Disks != nil || r.Collected["volumes"] || r.Collected["disks"] || r.CollectErr == nil || !r.NodeValid {
			t.Fatalf("échec sur %s : %+v", failing, r)
		}
	}
	d := &fakeDev{info: hba, sup: aacraid.SupplementInfo{Version: 15, SupportedOptions2: 0}}
	r := withDev(d, nil).Collect(c, ref)
	if len(d.reads) != 0 || r.Volumes != nil {
		t.Fatalf("carte sans interface interrogée : %v", d.reads)
	}
}
