package exporter

import (
	"errors"
	"strings"
	"testing"

	"github.com/DisasteR/hwraid_exporter/internal/module"
	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
)

type fakeMod struct{}

func (fakeMod) Name() string                                    { return "aacraid" }
func (fakeMod) Match(c module.Card) bool                        { return c.PCI.Driver == "aacraid" }
func (fakeMod) Capabilities() uint64                            { return 1 << 17 }
func (fakeMod) Nodes(module.Card) ([]module.NodeRequest, error) { return nil, nil }
func (fakeMod) Collect(module.Card, []module.NodeRef) module.Result {
	return module.Result{NodeValid: true}
}

func aacHost(name, addr, serial string) sysfs.SCSIHost {
	return sysfs.SCSIHost{Name: name, ProcName: "aacraid", SerialNumber: serial, Model: "6805", Firmware: "5.3-0[19204]",
		PCI: sysfs.PCIFunction{Address: addr, Driver: "aacraid", Class: "0x010400"}}
}

func plan(hosts ...sysfs.SCSIHost) module.Plan {
	return module.Build(hosts, []module.Module{fakeMod{}})
}

func valid(p module.Plan) map[string]module.Result {
	r := map[string]module.Result{}
	for _, c := range p.Cards {
		r[c.Card.PCI.Address] = module.Result{NodeValid: true}
	}
	return r
}

func TestIdentityPolicy(t *testing.T) {
	p := plan(
		aacHost("host0", "0000:01:00.0", "AAA"),
		aacHost("host1", "0000:02:00.0", "DUP"),
		aacHost("host2", "0000:03:00.0", "DUP"),
		aacHost("host3", "0000:04:00.0", ""),
	)
	got := map[string]string{}
	for _, c := range Build(p, true, valid(p)).Controllers {
		got[c.PCIAddress] = c.Label
	}
	want := map[string]string{
		"0000:01:00.0": "serial:AAA",
		"0000:02:00.0": "pci:0000:02:00.0", // série dupliquée : repli PCI pour les deux
		"0000:03:00.0": "pci:0000:03:00.0",
		"0000:04:00.0": "pci:0000:04:00.0", // série vide
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s : label %q, attendu %q (tous : %v)", k, got[k], v, got)
		}
	}
}

// Une carte RAID sans module est inventoriée, sans vérification de nœud.
func TestUnsupportedRAIDCard(t *testing.T) {
	p := plan(sysfs.SCSIHost{Name: "host7", ProcName: "inconnu", PCI: sysfs.PCIFunction{Address: "0000:05:00.0", Driver: "inconnu", Class: "0x010400"}})
	out := Render(Build(p, true, nil))
	if !strings.Contains(out, `hwraid_controller_backend_supported{controller="pci:0000:05:00.0"} 0`+"\n") {
		t.Fatalf("carte non prise en charge absente :\n%s", out)
	}
	if strings.Contains(out, "hwraid_management_node_valid{") {
		t.Fatalf("validité de nœud publiée pour une carte sans module :\n%s", out)
	}
}

func TestNodeValidity(t *testing.T) {
	p := plan(aacHost("host0", "0000:01:00.0", "A"))
	cases := map[string]struct {
		res   map[string]module.Result
		valid bool
	}{
		"valide":           {valid(p), true},
		"échec de lecture": {map[string]module.Result{"0000:01:00.0": {Err: errors.New("EPERM")}}, false},
		"aucune collecte":  {nil, false},
	}
	for name, tc := range cases {
		c := Build(p, true, tc.res).Controllers[0]
		if !c.NodeChecked || c.NodeValid != tc.valid || (!tc.valid && c.NodeError == "") {
			t.Fatalf("%s : %+v", name, c)
		}
	}
	// Une erreur de planification l'emporte sur un résultat valide.
	p.Cards[0].Err = errors.New("major absent")
	if c := Build(p, true, valid(p)).Controllers[0]; c.NodeValid {
		t.Fatalf("carte en erreur de planification jugée valide : %+v", c)
	}
}

func TestRenderExposition(t *testing.T) {
	if out := Render(Snapshot{}); out != "# HELP hwraid_snapshot_ready 1 une fois la première découverte publiée.\n# TYPE hwraid_snapshot_ready gauge\nhwraid_snapshot_ready 0\n" {
		t.Fatalf("vue initiale :\n%s", out)
	}
	p := plan(aacHost("host0", "0000:01:00.0", `A"B\C`))
	out := Render(Build(p, true, valid(p)))
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
