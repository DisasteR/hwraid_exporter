package exporter

import (
	"errors"
	"strings"
	"testing"

	"github.com/DisasteR/hwraid_exporter/internal/aacraid"
	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
)

func aacHost(name, addr, uid, serial string) sysfs.SCSIHost {
	return sysfs.SCSIHost{Name: name, ProcName: "aacraid", UniqueID: uid, SerialNumber: serial,
		Model: "6805", Firmware: "5.3-0[19204]", PCI: sysfs.PCIFunction{Address: addr, Driver: "aacraid"}}
}

func okChecker(h sysfs.SCSIHost) (aacraid.HBAInfo, error) {
	bus, slot, fn, _ := ParsePCI(h.PCI.Address)
	uid := h.UniqueID[0] - '0'
	return aacraid.HBAInfo{AdapterNumber: uid, Bus: bus, Device: slot, Function: uint32(fn)}, nil
}

func TestIdentityPolicy(t *testing.T) {
	hosts := []sysfs.SCSIHost{
		aacHost("host0", "0000:01:00.0", "0", "AAA"),
		aacHost("host1", "0000:02:00.0", "1", "DUP"),
		aacHost("host2", "0000:03:00.0", "2", "DUP"),
		aacHost("host3", "0000:04:00.0", "3", ""),
		{Name: "host9", ProcName: "ahci", PCI: sysfs.PCIFunction{Address: "0000:00:14.0"}},
	}
	s := Build(hosts, true, nil)
	got := map[string]string{}
	for _, c := range s.Controllers {
		got[c.PCIAddress] = c.Label
	}
	want := map[string]string{
		"0000:01:00.0": "serial:AAA",
		"0000:02:00.0": "pci:0000:02:00.0", // série dupliquée : repli PCI pour les deux
		"0000:03:00.0": "pci:0000:03:00.0",
		"0000:04:00.0": "pci:0000:04:00.0", // série vide
	}
	if len(got) != len(want) {
		t.Fatalf("contrôleurs : %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s : label %q, attendu %q", k, got[k], v)
		}
	}
}

// Deux hôtes SCSI de la même carte ne donnent qu'un contrôleur.
func TestHostsDeduplicatedByPCI(t *testing.T) {
	s := Build([]sysfs.SCSIHost{aacHost("host0", "0000:01:00.0", "0", "A"), aacHost("host5", "0000:01:00.0", "0", "A")}, true, nil)
	if len(s.Controllers) != 1 {
		t.Fatalf("%d contrôleurs", len(s.Controllers))
	}
}

func TestNodeVerification(t *testing.T) {
	h := aacHost("host0", "0000:01:00.0", "0", "A")
	if c := Build([]sysfs.SCSIHost{h}, true, okChecker).Controllers[0]; !c.NodeChecked || !c.NodeValid {
		t.Fatalf("nœud cohérent jugé invalide : %+v", c)
	}
	wrongCard := func(sysfs.SCSIHost) (aacraid.HBAInfo, error) {
		return aacraid.HBAInfo{AdapterNumber: 1, Bus: 1}, nil
	}
	if c := Build([]sysfs.SCSIHost{h}, true, wrongCard).Controllers[0]; c.NodeValid {
		t.Fatal("un nœud désignant une autre carte doit être invalide")
	}
	failing := func(sysfs.SCSIHost) (aacraid.HBAInfo, error) { return aacraid.HBAInfo{}, errors.New("EPERM") }
	if c := Build([]sysfs.SCSIHost{h}, true, failing).Controllers[0]; c.NodeValid || c.NodeError == "" {
		t.Fatalf("échec d'ouverture non signalé : %+v", c)
	}
}

func TestRenderExposition(t *testing.T) {
	if out := Render(Snapshot{}); out != "# HELP hwraid_snapshot_ready 1 une fois la première découverte publiée.\n# TYPE hwraid_snapshot_ready gauge\nhwraid_snapshot_ready 0\n" {
		t.Fatalf("vue initiale :\n%s", out)
	}
	h := aacHost("host0", "0000:01:00.0", "0", `A"B\C`)
	out := Render(Build([]sysfs.SCSIHost{h}, true, okChecker))
	for _, line := range []string{
		"hwraid_snapshot_ready 1",
		"hwraid_discovery_success 1",
		`hwraid_controller_info{controller="serial:A\"B\\C",identity_source="serial",pci_address="0000:01:00.0",driver="aacraid",backend="aacraid",model="6805",firmware="5.3-0[19204]"} 1`,
		`hwraid_controller_backend_supported{controller="serial:A\"B\\C"} 1`,
		`hwraid_management_node_valid{controller="serial:A\"B\\C"} 1`,
	} {
		if !strings.Contains(out, line+"\n") {
			t.Fatalf("ligne absente : %s\n--- sortie ---\n%s", line, out)
		}
	}
}

func TestParsePCI(t *testing.T) {
	b, s, f, err := ParsePCI("0000:3b:1f.7")
	if err != nil || b != 0x3b || s != 0x1f || f != 7 {
		t.Fatalf("%d %d %d %v", b, s, f, err)
	}
	for _, bad := range []string{"01:00.0", "0000:01:00.8", "0000:1:00.0", ""} {
		if _, _, _, err := ParsePCI(bad); err == nil {
			t.Fatalf("%q accepté", bad)
		}
	}
}
