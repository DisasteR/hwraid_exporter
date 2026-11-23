package aacmod

import (
	"errors"
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
}

func (f *fakeDev) HBAInfo() (aacraid.HBAInfo, error) { return f.info, f.err }
func (f *fakeDev) SupplementInfo() (aacraid.SupplementInfo, error) {
	f.supCall++
	return f.sup, f.supErr
}
func (f *fakeDev) Close() error { f.closed = true; return nil }

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
