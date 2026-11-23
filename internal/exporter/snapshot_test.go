package exporter

import (
	"errors"
	"strings"
	"testing"
	"time"

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
	for _, c := range Build(p, true, valid(p), nil, 0).Controllers {
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
	out := Render(Build(p, true, nil, nil, 0))
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
		c := Build(p, true, tc.res, nil, 0).Controllers[0]
		if !c.NodeChecked || c.NodeValid != tc.valid || (!tc.valid && c.NodeError == "") {
			t.Fatalf("%s : %+v", name, c)
		}
	}
	// Une erreur de planification l'emporte sur un résultat valide.
	p.Cards[0].Err = errors.New("major absent")
	if c := Build(p, true, valid(p), nil, 0).Controllers[0]; c.NodeValid {
		t.Fatalf("carte en erreur de planification jugée valide : %+v", c)
	}
}

func TestRenderExposition(t *testing.T) {
	if out := Render(Snapshot{}); out != "# HELP hwraid_snapshot_ready 1 une fois la première découverte publiée.\n# TYPE hwraid_snapshot_ready gauge\nhwraid_snapshot_ready 0\n" {
		t.Fatalf("vue initiale :\n%s", out)
	}
	p := plan(aacHost("host0", "0000:01:00.0", `A"B\C`))
	out := Render(Build(p, true, valid(p), nil, 0))
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

// Un collecteur non pris en charge par la carte est publié à 0 ; un échec
// de lecture ne publie rien sur la prise en charge et n'invalide pas le nœud.
func TestCollectorSupported(t *testing.T) {
	p := plan(aacHost("host0", "0000:01:00.0", "S1"))
	res := map[string]module.Result{"0000:01:00.0": {NodeValid: true, Unsupported: []string{"volumes", "disks"}}}
	out := Render(Build(p, true, res, nil, 0))
	for _, line := range []string{
		`hwraid_collector_supported{controller="serial:S1",collector="volumes"} 0`,
		`hwraid_collector_supported{controller="serial:S1",collector="disks"} 0`,
	} {
		if !strings.Contains(out, line+"\n") {
			t.Fatalf("ligne absente : %s\n%s", line, out)
		}
	}
	res = map[string]module.Result{"0000:01:00.0": {NodeValid: true, CollectErr: errors.New("EIO")}}
	s := Build(p, true, res, nil, 0)
	if c := s.Controllers[0]; !c.NodeValid || c.CollectError == "" {
		t.Fatalf("%+v", c)
	}
	if out := Render(s); strings.Contains(out, "hwraid_collector_supported{") {
		t.Fatalf("prise en charge publiée malgré l'échec :\n%s", out)
	}
	// Carte qui annonce l'interface : aucune série à 0.
	if out := Render(Build(p, true, valid(p), nil, 0)); strings.Contains(out, "hwraid_collector_supported{") {
		t.Fatalf("série inattendue :\n%s", out)
	}
}

// hwraid_collection_success reprend le résultat de chaque collecteur tenté,
// dans un ordre stable ; rien n'est publié pour une carte sans collecte.
func TestCollectionSuccess(t *testing.T) {
	p := plan(aacHost("host0", "0000:01:00.0", "S1"))
	res := map[string]module.Result{"0000:01:00.0": {NodeValid: true,
		Collected: map[string]bool{"volumes": true, "capabilities": false}}}
	out := Render(Build(p, true, res, nil, 0))
	a := `hwraid_collection_success{controller="serial:S1",collector="capabilities"} 0` + "\n"
	b := `hwraid_collection_success{controller="serial:S1",collector="volumes"} 1` + "\n"
	if !strings.Contains(out, a+b) {
		t.Fatalf("séries absentes ou dans le désordre :\n%s", out)
	}
	res = map[string]module.Result{"0000:01:00.0": {Err: errors.New("EPERM")}}
	if out := Render(Build(p, true, res, nil, 0)); strings.Contains(out, "hwraid_collection_success{") {
		t.Fatalf("série publiée sans collecte :\n%s", out)
	}
}

// Protection du cache : présence et état publiés seulement quand ils ont été
// lus ; un module non installé publie present=0 sans état.
func TestCacheProtectionRender(t *testing.T) {
	p := plan(aacHost("host0", "0000:01:00.0", "S1"))
	res := map[string]module.Result{"0000:01:00.0": {NodeValid: true,
		CacheProtection: &module.CacheProtection{Present: true, Type: "flash_supercap", Vendor: "zmm", State: "failed"}}}
	out := Render(Build(p, true, res, nil, 0))
	for _, line := range []string{
		`hwraid_cache_protection_present{controller="serial:S1",type="flash_supercap",vendor_type="zmm"} 1`,
		`hwraid_cache_protection_state{controller="serial:S1",state="failed"} 1`,
	} {
		if !strings.Contains(out, line+"\n") {
			t.Fatalf("ligne absente : %s\n%s", line, out)
		}
	}
	res["0000:01:00.0"] = module.Result{NodeValid: true, CacheProtection: &module.CacheProtection{Type: "flash_supercap", Vendor: "zmm"}}
	out = Render(Build(p, true, res, nil, 0))
	if !strings.Contains(out, `hwraid_cache_protection_present{controller="serial:S1",type="flash_supercap",vendor_type="zmm"} 0`+"\n") || strings.Contains(out, "hwraid_cache_protection_state{") {
		t.Fatalf("non installé :\n%s", out)
	}
	res["0000:01:00.0"] = module.Result{NodeValid: true}
	if out := Render(Build(p, true, res, nil, 0)); strings.Contains(out, "hwraid_cache_protection_present{") {
		t.Fatalf("présence publiée sans lecture :\n%s", out)
	}
}

func TestVolumeRender(t *testing.T) {
	p := plan(aacHost("host0", "0000:01:00.0", "S1"))
	res := map[string]module.Result{"0000:01:00.0": {NodeValid: true,
		Volumes: []module.Volume{
			{ID: "410734af", Name: "Array0", RaidLevel: "concat", VendorLevel: "RAID_volume", CapacityBytes: 1024, State: "unknown", Members: []string{"410734b3"}},
			{ID: "410734b3", Name: "Array0", RaidLevel: "raid1", VendorLevel: "1", Parent: "410734af", CapacityBytes: 512, State: "degraded", Members: []string{"d3", "d7"}},
		},
		Disks: []module.Disk{{ID: "d3", State: "online"}, {ID: "d7", State: "failed"}}}}
	out := Render(Build(p, true, res, nil, 0))
	for _, line := range []string{
		`hwraid_volume_info{controller="serial:S1",volume="410734af",name="Array0",raid_level="concat",vendor_level="RAID_volume",parent=""} 1`,
		`hwraid_volume_info{controller="serial:S1",volume="410734b3",name="Array0",raid_level="raid1",vendor_level="1",parent="410734af"} 1`,
		`hwraid_volume_state{controller="serial:S1",volume="410734b3",state="degraded"} 1`,
		`hwraid_volume_capacity_bytes{controller="serial:S1",volume="410734af"} 1024`,
		`hwraid_volume_member{controller="serial:S1",volume="410734b3",member="d7"} 1`,
		`hwraid_disk_state{controller="serial:S1",disk="d7",state="failed"} 1`,
	} {
		if !strings.Contains(out, line+"\n") {
			t.Fatalf("ligne absente : %s\n%s", line, out)
		}
	}
}

// Dernier succès : mis à jour seulement par un succès, conservé après un
// échec, oublié quand la carte disparaît ; intervalle publié pour chaque
// carte prise en charge.
func TestLastSuccessAndInterval(t *testing.T) {
	p := plan(aacHost("host0", "0000:01:00.0", "S1"))
	tr := NewTracker()
	t0 := time.Unix(1_000_000, 0)
	ok := map[string]module.Result{"0000:01:00.0": {NodeValid: true, Collected: map[string]bool{"volumes": true, "disks": true}}}
	tr.Update(ok, t0)
	failed := map[string]module.Result{"0000:01:00.0": {NodeValid: true, Collected: map[string]bool{"volumes": false, "disks": true}}}
	tr.Update(failed, t0.Add(time.Minute))
	out := Render(Build(p, true, failed, tr, 90*time.Second))
	for _, line := range []string{
		`hwraid_collection_interval_seconds{controller="serial:S1"} 90`,
		`hwraid_collection_last_success_timestamp_seconds{controller="serial:S1",collector="disks"} 1000060`,
		`hwraid_collection_last_success_timestamp_seconds{controller="serial:S1",collector="volumes"} 1000000`,
	} {
		if !strings.Contains(out, line+"\n") {
			t.Fatalf("ligne absente : %s\n%s", line, out)
		}
	}
	tr.Update(map[string]module.Result{}, t0.Add(2*time.Minute))
	if tr.Last("0000:01:00.0") != nil {
		t.Fatal("carte retirée encore suivie")
	}
	// Carte sans module : pas d'intervalle publié.
	unsupported := plan(sysfs.SCSIHost{Name: "host7", ProcName: "inconnu", PCI: sysfs.PCIFunction{Address: "0000:05:00.0", Driver: "inconnu", Class: "0x010400"}})
	if out := Render(Build(unsupported, true, nil, tr, time.Minute)); strings.Contains(out, "hwraid_collection_interval_seconds{") {
		t.Fatalf("intervalle publié pour une carte sans module :\n%s", out)
	}
}
