// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DisasteR/hwraid_exporter/internal/module"
	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
)

type fakeMod struct{}

func (fakeMod) Name() string             { return "aacraid" }
func (fakeMod) Match(c module.Card) bool { return c.PCI.Driver == "aacraid" }
func (fakeMod) Capabilities() uint64     { return 1 << 17 }
func (fakeMod) Collectors() []string     { return []string{"capabilities", "volumes"} }

// Nodes déclare, comme le module aacraid, un nœud par carte (nom tiré de
// l'adresse PCI pour rester unique).
func (fakeMod) Nodes(c module.Card) ([]module.NodeRequest, error) {
	name := "aac" + strings.NewReplacer(":", "", ".", "").Replace(c.PCI.Address)
	return []module.NodeRequest{{Name: name, MajorOf: "aac", Minor: 0}}, nil
}
func (fakeMod) Collect(context.Context, module.Card, []module.NodeRef) module.Result {
	return module.Result{NodeValid: true}
}

func aacHost(name, addr, serial string) sysfs.SCSIHost {
	return sysfs.SCSIHost{Name: name, ProcName: "aacraid", SerialNumber: serial, Model: "6805", Firmware: "5.3-0[19204]",
		PCI: sysfs.PCIFunction{Address: addr, Driver: "aacraid", Class: "0x010400",
			Vendor: "0x9005", Device: "0x028b", SubsystemVendor: "0x9005", SubsystemDevice: "0x0301"}}
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
	initial := "# HELP hwraid_exporter_build_info Exporter version, source revision and Go version, value 1.\n# TYPE hwraid_exporter_build_info gauge\n" +
		`hwraid_exporter_build_info{version="` + Version + `",revision="` + Revision + `",goversion="` + runtime.Version() + `"} 1` + "\n" +
		"# HELP hwraid_snapshot_ready 1 when the view holds the latest collection; 0, with no hardware value, before the first one, while stopping, and when the collection loop has not published for too long.\n# TYPE hwraid_snapshot_ready gauge\nhwraid_snapshot_ready 0\n"
	if out := Render(Snapshot{}); out != initial {
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
	// Nœud en erreur : aucune lecture tentée, faute de leur prérequis ;
	// chaque collecteur du module est publié en échec, aucun en succès.
	res = map[string]module.Result{"0000:01:00.0": {Err: errors.New("EPERM")}}
	out = Render(Build(p, true, res, nil, 0))
	if !strings.Contains(out, `hwraid_collection_success{controller="serial:S1",collector="capabilities"} 0`+"\n") ||
		!strings.Contains(out, `hwraid_collection_success{controller="serial:S1",collector="volumes"} 0`+"\n") ||
		strings.Count(out, "hwraid_collection_success{") != 2 {
		t.Fatalf("nœud en erreur : collecteurs non publiés en échec :\n%s", out)
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
	// Présence non établie : état unknown publié, présence omise.
	res["0000:01:00.0"] = module.Result{NodeValid: true, CacheProtection: &module.CacheProtection{PresenceUnknown: true, Type: "flash_supercap", Vendor: "zmm", State: "unknown"}}
	out = Render(Build(p, true, res, nil, 0))
	if strings.Contains(out, "hwraid_cache_protection_present{") || !strings.Contains(out, `hwraid_cache_protection_state{controller="serial:S1",state="unknown"} 1`+"\n") {
		t.Fatalf("présence non établie :\n%s", out)
	}
}

// Température : valeur et seuils publiés seulement si la lecture a abouti
// dans le cycle ; jamais sur un cycle hors délai (valeur non observée).
func TestTemperatureRender(t *testing.T) {
	p := plan(aacHost("host0", "0000:01:00.0", "S1"))
	res := map[string]module.Result{"0000:01:00.0": {NodeValid: true,
		Temperature: &module.Temperature{Celsius: 49, ThresholdLo: 95, ThresholdHi: 100}}}
	out := Render(Build(p, true, res, nil, 0))
	for _, line := range []string{
		`hwraid_controller_temperature_celsius{controller="serial:S1"} 49`,
		`hwraid_controller_temperature_threshold_celsius{controller="serial:S1",level="low"} 95`,
		`hwraid_controller_temperature_threshold_celsius{controller="serial:S1",level="high"} 100`,
	} {
		if !strings.Contains(out, line+"\n") {
			t.Fatalf("ligne absente : %s\n%s", line, out)
		}
	}
	for name, r := range map[string]module.Result{
		"lecture absente": {NodeValid: true},
		"hors délai":      {TimedOut: true, Collected: map[string]bool{"temperature": false}},
	} {
		res["0000:01:00.0"] = r
		if out := Render(Build(p, true, res, nil, 0)); strings.Contains(out, "hwraid_controller_temperature_celsius{") ||
			strings.Contains(out, "hwraid_controller_temperature_threshold_celsius{") {
			t.Fatalf("%s : température publiée sans lecture :\n%s", name, out)
		}
	}
}

func TestVolumeRender(t *testing.T) {
	off := false
	p := plan(aacHost("host0", "0000:01:00.0", "S1"))
	res := map[string]module.Result{"0000:01:00.0": {NodeValid: true,
		Volumes: []module.Volume{
			{ID: "1d000000", Name: "Array0", RaidLevel: "concat", VendorLevel: "RAID_volume", CapacityBytes: 1024, State: "unknown", Members: []string{"1d000004"}},
			{ID: "1d000004", Name: "Array0", RaidLevel: "raid1", VendorLevel: "1", Parent: "1d000000", CapacityBytes: 512, State: "degraded", Members: []string{"d3", "d7"},
				WriteCachePolicy: "write_back_when_protected", VendorWriteCachePolicy: "On when protected by battery/ZMM", WriteCacheEnabled: &off},
		},
		Disks: []module.Disk{{ID: "d3", State: "online"}, {ID: "d7", State: "failed"}}}}
	out := Render(Build(p, true, res, nil, 0))
	for _, line := range []string{
		`hwraid_volume_info{controller="serial:S1",volume="1d000000",name="Array0",raid_level="concat",vendor_level="RAID_volume",parent=""} 1`,
		`hwraid_volume_info{controller="serial:S1",volume="1d000004",name="Array0",raid_level="raid1",vendor_level="1",parent="1d000000"} 1`,
		`hwraid_volume_state{controller="serial:S1",volume="1d000004",state="degraded"} 1`,
		`hwraid_volume_capacity_bytes{controller="serial:S1",volume="1d000000"} 1024`,
		`hwraid_volume_member{controller="serial:S1",volume="1d000004",member="d7"} 1`,
		`hwraid_disk_state{controller="serial:S1",disk="d7",state="failed"} 1`,
		`hwraid_volume_write_cache_policy{controller="serial:S1",volume="1d000004",policy="write_back_when_protected",vendor_policy="On when protected by battery/ZMM"} 1`,
		`hwraid_volume_write_cache_enabled{controller="serial:S1",volume="1d000004"} 0`,
	} {
		if !strings.Contains(out, line+"\n") {
			t.Fatalf("ligne absente : %s\n%s", line, out)
		}
	}
	// Volume sans cache d'écriture établi : aucune série.
	for _, absent := range []string{
		`hwraid_volume_write_cache_policy{controller="serial:S1",volume="1d000000"`,
		`hwraid_volume_write_cache_enabled{controller="serial:S1",volume="1d000000"}`,
	} {
		if strings.Contains(out, absent) {
			t.Fatalf("série inattendue : %s\n%s", absent, out)
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
	present := []string{"0000:01:00.0"}
	tr.Update(ok, present, t0)
	failed := map[string]module.Result{"0000:01:00.0": {NodeValid: true, Collected: map[string]bool{"volumes": false, "disks": true}}}
	tr.Update(failed, present, t0.Add(time.Minute))
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
	// Carte présente mais non collectée (collecte suspendue) : dates conservées.
	tr.Update(map[string]module.Result{}, present, t0.Add(2*time.Minute))
	if tr.Last("0000:01:00.0")["volumes"] != t0 {
		t.Fatal("dates perdues pour une carte présente non collectée")
	}
	tr.Update(map[string]module.Result{}, nil, t0.Add(3*time.Minute))
	if tr.Last("0000:01:00.0") != nil {
		t.Fatal("carte retirée encore suivie")
	}
	// Carte sans module : pas d'intervalle publié.
	unsupported := plan(sysfs.SCSIHost{Name: "host7", ProcName: "inconnu", PCI: sysfs.PCIFunction{Address: "0000:05:00.0", Driver: "inconnu", Class: "0x010400"}})
	if out := Render(Build(unsupported, true, nil, tr, time.Minute)); strings.Contains(out, "hwraid_collection_interval_seconds{") {
		t.Fatalf("intervalle publié pour une carte sans module :\n%s", out)
	}
}

// Identité : un numéro de série illisible ne renomme pas la carte ; un autre
// numéro, ou une carte retirée de l'inventaire, met fin à la mémoire.
func TestIdentityStableOnUnreadableSerial(t *testing.T) {
	const addr = "0000:01:00.0"
	label := func(tr *Tracker, hosts ...sysfs.SCSIHost) string {
		p := plan(hosts...)
		s := Build(p, true, valid(p), tr, 0)
		tr.Remember(s)
		for _, c := range s.Controllers {
			if c.PCIAddress == addr {
				return c.Label + "/" + c.IdentitySource
			}
		}
		return ""
	}
	tr := NewTracker()
	if got := label(tr, aacHost("host0", addr, "S1")); got != "serial:S1/serial" {
		t.Fatal(got)
	}
	if got := label(tr, aacHost("host0", addr, "")); got != "serial:S1/serial" {
		t.Fatalf("série illisible : %s", got)
	}
	// Une autre carte présente maintenant S1 : pas de doublon de label.
	if got := label(tr, aacHost("host0", addr, ""), aacHost("host1", "0000:02:00.0", "S1")); got != "pci:"+addr+"/pci" {
		t.Fatalf("doublon : %s", got)
	}
	// Autre numéro : autre carte.
	if got := label(tr, aacHost("host0", addr, "S2")); got != "serial:S2/serial" {
		t.Fatal(got)
	}
	// Carte retirée puis revenue sans numéro : plus de mémoire.
	tr.Update(nil, nil, time.Now())
	if got := label(tr, aacHost("host0", addr, "")); got != "pci:"+addr+"/pci" {
		t.Fatalf("mémoire après retrait : %s", got)
	}
	// Sans suivi : comportement d'origine.
	if got := label(nil, aacHost("host0", addr, "")); got != "pci:"+addr+"/pci" {
		t.Fatal(got)
	}
}

// Inventaire des disques : une série d'information par disque présent ;
// capacité publiée seulement si elle est connue ; texte échappé.
func TestDiskInventoryRender(t *testing.T) {
	p := plan(aacHost("host0", "0000:01:00.0", "S1"))
	res := map[string]module.Result{"0000:01:00.0": {NodeValid: true, DiskInventory: []module.DiskInfo{
		{ID: "d0", Vendor: "ATA", Model: "ST8000VN0022-2EL", Firmware: "SC61", Serial: "SN0", WWN: "0x5000c50000000000", CapacityBytes: 8001563222016},
		{ID: "d1", Vendor: "ATA", Model: `X"Y`, Firmware: "1", Serial: "SN1"},
	}}}
	out := Render(Build(p, true, res, nil, 0))
	for _, line := range []string{
		`hwraid_disk_info{controller="serial:S1",disk="d0",vendor="ATA",model="ST8000VN0022-2EL",firmware="SC61",serial="SN0",wwn="0x5000c50000000000"} 1`,
		`hwraid_disk_info{controller="serial:S1",disk="d1",vendor="ATA",model="X\"Y",firmware="1",serial="SN1",wwn=""} 1`,
		`hwraid_disk_capacity_bytes{controller="serial:S1",disk="d0"} 8001563222016`,
	} {
		if !strings.Contains(out, line+"\n") {
			t.Fatalf("ligne absente : %s\n%s", line, out)
		}
	}
	if strings.Contains(out, `hwraid_disk_capacity_bytes{controller="serial:S1",disk="d1"}`) {
		t.Fatalf("capacité inconnue publiée :\n%s", out)
	}
}

// Emplacement : une série par disque localisé.
func TestDiskLocationRender(t *testing.T) {
	p := plan(aacHost("host0", "0000:01:00.0", "S1"))
	res := map[string]module.Result{"0000:01:00.0": {NodeValid: true, DiskLocations: []module.DiskLocation{
		{ID: "d7", Connector: "1", ConnectorName: "CN1", Device: "3", Phy: "4"},
	}}}
	out := Render(Build(p, true, res, nil, 0))
	want := `hwraid_disk_location{controller="serial:S1",disk="d7",connector="1",connector_name="CN1",device="3",phy="4"} 1`
	if !strings.Contains(out, want+"\n") || strings.Count(out, "hwraid_disk_location{") != 1 {
		t.Fatalf("ligne absente : %s\n%s", want, out)
	}
}

// Périphérique Linux : une série par disque relié.
func TestDiskOSDeviceRender(t *testing.T) {
	p := plan(aacHost("host0", "0000:01:00.0", "S1"))
	res := map[string]module.Result{"0000:01:00.0": {NodeValid: true, DiskOSDevices: []module.DiskOSDevice{
		{ID: "d7", Device: "sg8", Address: "0:1:7:0"},
	}}}
	out := Render(Build(p, true, res, nil, 0))
	want := `hwraid_disk_os_device{controller="serial:S1",disk="d7",device="sg8",scsi_address="0:1:7:0"} 1`
	if !strings.Contains(out, want+"\n") || strings.Count(out, "hwraid_disk_os_device{") != 1 {
		t.Fatalf("ligne absente : %s\n%s", want, out)
	}
}

// Version publiée dès l'instantané initial, avant toute collecte.
func TestBuildInfo(t *testing.T) {
	oldV, oldR := Version, Revision
	Version, Revision = "v9.9.9", "2bc8c58f0e1d"
	defer func() { Version, Revision = oldV, oldR }()
	out := Render(Snapshot{})
	want := `hwraid_exporter_build_info{version="v9.9.9",revision="2bc8c58f0e1d",goversion="` + runtime.Version() + `"} 1`
	if !strings.Contains(out, want+"\n") {
		t.Fatalf("ligne absente : %s\n%s", want, out)
	}
}

// Scénario de collision : A porte S ; A devient illisible pendant que B
// présente S ; puis les deux deviennent illisibles. Aucun cycle ne doit
// publier deux cartes sous le même label.
func TestIdentityNeverDuplicated(t *testing.T) {
	const a, b = "0000:01:00.0", "0000:02:00.0"
	tr := NewTracker()
	cycle := func(sa, sb string) map[string]string {
		p := plan(aacHost("host0", a, sa), aacHost("host1", b, sb))
		s := Build(p, true, valid(p), tr, 0)
		tr.Remember(s)
		got, seen := map[string]string{}, map[string]bool{}
		for _, c := range s.Controllers {
			if seen[c.Label] {
				t.Fatalf("label %s publié deux fois (A=%q, B=%q)", c.Label, sa, sb)
			}
			seen[c.Label] = true
			got[c.PCIAddress] = c.Label
		}
		return got
	}
	if got := cycle("S", ""); got[a] != "serial:S" {
		t.Fatal(got)
	}
	if got := cycle("", "S"); got[a] != "pci:"+a || got[b] != "serial:S" {
		t.Fatal(got)
	}
	// Seule B garde la mémoire de S.
	if got := cycle("", ""); got[a] != "pci:"+a || got[b] != "serial:S" {
		t.Fatalf("après les deux lectures ratées : %v", got)
	}
}

// Collecteur servi par une réponse gardée : jamais de date de dernier
// succès, et une date déjà enregistrée est retirée.
func TestCachedCollectorHasNoLastSuccess(t *testing.T) {
	const addr = "0000:01:00.0"
	tr := NewTracker()
	t0 := time.Unix(1_000_000, 0)
	tr.Update(map[string]module.Result{addr: {Collected: map[string]bool{"capabilities": true, "volumes": true}}}, []string{addr}, t0)
	tr.Update(map[string]module.Result{addr: {Collected: map[string]bool{"capabilities": true, "volumes": true}, Cached: []string{"capabilities"}}}, []string{addr}, t0.Add(time.Minute))
	last := tr.Last(addr)
	if _, ok := last["capabilities"]; ok || last["volumes"] != t0.Add(time.Minute) {
		t.Fatalf("%v", last)
	}
}

// Seconde ligne de défense : même si deux adresses mémorisaient le même
// numéro (état que Remember ne doit pas produire), aucun label n'est publié
// deux fois.
func TestIdentityDuplicateFallsBackToPCI(t *testing.T) {
	const a, b = "0000:01:00.0", "0000:02:00.0"
	tr := NewTracker()
	tr.serial[a], tr.serial[b] = "serial:S", "serial:S"
	p := plan(aacHost("host0", a, ""), aacHost("host1", b, ""))
	got := map[string]string{}
	for _, c := range Build(p, true, valid(p), tr, 0).Controllers {
		got[c.PCIAddress] = c.Label + "/" + c.IdentitySource
	}
	if got[a] != "pci:"+a+"/pci" || got[b] != "pci:"+b+"/pci" {
		t.Fatalf("%v", got)
	}
}

// Clôtures non nominales : cumulées d'un cycle à l'autre, publiées à 0 dès
// le départ pour une carte prise en charge, oubliées avec la carte.
func TestCloseUnconfirmedCounter(t *testing.T) {
	const addr = "0000:01:00.0"
	p := plan(aacHost("host0", addr, "S1"))
	tr := NewTracker()
	line := func(n int) string {
		return fmt.Sprintf(`hwraid_transfer_close_unconfirmed_total{controller="serial:S1"} %d`, n) + "\n"
	}
	if out := Render(Build(p, true, valid(p), tr, 0)); !strings.Contains(out, line(0)) || !strings.Contains(out, "# TYPE hwraid_transfer_close_unconfirmed_total counter\n") {
		t.Fatalf("compteur absent au départ :\n%s", out)
	}
	for _, n := range []int{1, 0, 2} {
		tr.Update(map[string]module.Result{addr: {NodeValid: true, CloseUnconfirmed: n}}, []string{addr}, time.Now())
	}
	if out := Render(Build(p, true, valid(p), tr, 0)); !strings.Contains(out, line(3)) {
		t.Fatalf("cumul attendu à 3 :\n%s", out)
	}
	tr.Update(nil, nil, time.Now())
	if tr.Closes(addr) != 0 {
		t.Fatal("cumul conservé après le retrait de la carte")
	}
}
