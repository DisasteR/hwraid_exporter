package sysfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fixture reproduit la structure réelle de sysfs : les entrées de
// class/scsi_host sont des liens symboliques vers l'arbre devices/,
// et le lien driver d'une fonction PCI pointe vers bus/pci/drivers/<nom>.
type fixture struct {
	t    *testing.T
	root string
}

func newFixture(t *testing.T) *fixture {
	root := t.TempDir()
	for _, d := range []string{"class/scsi_host", "bus/pci/drivers/aacraid", "bus/pci/drivers/ahci"} {
		mustMkdir(t, filepath.Join(root, d))
	}
	return &fixture{t: t, root: root}
}

// pciFunc crée une fonction PCI sous un chemin de pont et la lie à un pilote.
func (f *fixture) pciFunc(path, driver string, ids map[string]string) string {
	dir := filepath.Join(f.root, "devices", path)
	mustMkdir(f.t, dir)
	for k, v := range ids {
		mustWrite(f.t, filepath.Join(dir, k), v+"\n")
	}
	if driver != "" {
		mustSymlink(f.t, filepath.Join(f.root, "bus/pci/drivers", driver), filepath.Join(dir, "driver"))
	}
	return dir
}

// host crée hostN sous la fonction PCI et son lien dans class/scsi_host.
func (f *fixture) host(pciDir, name string, attrs map[string]string) {
	dir := filepath.Join(pciDir, name, "scsi_host", name)
	mustMkdir(f.t, dir)
	for k, v := range attrs {
		mustWrite(f.t, filepath.Join(dir, k), v+"\n")
	}
	rel, err := filepath.Rel(filepath.Join(f.root, "class/scsi_host"), dir)
	if err != nil {
		f.t.Fatal(err)
	}
	mustSymlink(f.t, rel, filepath.Join(f.root, "class/scsi_host", name))
}

var aacIDs = map[string]string{"vendor": "0x9005", "device": "0x028b", "subsystem_vendor": "0x9005", "subsystem_device": "0x0800", "class": "0x010400"}

func TestDiscoverReferenceLayout(t *testing.T) {
	f := newFixture(t)
	// Le pont 0000:00:09.0 publie aussi un fichier vendor : la découverte
	// doit retenir l'ancêtre PCI le plus proche, pas le pont.
	bridge := f.pciFunc("pci0000:00/0000:00:09.0", "", map[string]string{"vendor": "0x8086"})
	_ = bridge
	aac := f.pciFunc("pci0000:00/0000:00:09.0/0000:01:00.0", "aacraid", aacIDs)
	f.host(aac, "host0", map[string]string{
		"proc_name": "aacraid", "unique_id": "0", "serial_number": "ABC123",
		"model": "6805", "hba_kernel_version": "5.3-0[19204]",
	})
	ahci := f.pciFunc("pci0000:00/0000:00:14.0", "ahci", map[string]string{"vendor": "0x8086", "class": "0x010601"})
	f.host(ahci, "host14", map[string]string{"proc_name": "ahci", "unique_id": "14"})

	hosts, herrs, err := Discover(f.root)
	if err != nil || len(herrs) != 0 {
		t.Fatalf("err=%v herrs=%v", err, herrs)
	}
	if len(hosts) != 2 {
		t.Fatalf("attendu 2 hôtes, obtenu %d", len(hosts))
	}
	h := hosts[0]
	want := SCSIHost{
		Name: "host0", Number: 0, ProcName: "aacraid",
		PCI: PCIFunction{Address: "0000:01:00.0", Driver: "aacraid", Class: "0x010400", Vendor: "0x9005", Device: "0x028b",
			SubsystemVendor: "0x9005", SubsystemDevice: "0x0800"},
		UniqueID: "0", SerialNumber: "ABC123", Model: "6805", Firmware: "5.3-0[19204]",
	}
	if h != want {
		t.Fatalf("hôte AAC :\n obtenu %+v\n attendu %+v", h, want)
	}
	if hosts[1].PCI.Address != "0000:00:14.0" || hosts[1].PCI.Driver != "ahci" {
		t.Fatalf("hôte AHCI mal rattaché : %+v", hosts[1].PCI)
	}
	if !h.PCI.IsRAIDClass() || hosts[1].PCI.IsRAIDClass() {
		t.Fatalf("classes : AAC=%s AHCI=%s", h.PCI.Class, hosts[1].PCI.Class)
	}
}

// Deux cartes du même pilote : chaque hôte doit rester rattaché à sa propre
// fonction PCI, et le minor (unique_id) ne suit pas le numéro d'hôte.
func TestDiscoverTwoCardsSameDriver(t *testing.T) {
	f := newFixture(t)
	a := f.pciFunc("pci0000:00/0000:00:09.0/0000:01:00.0", "aacraid", aacIDs)
	b := f.pciFunc("pci0000:00/0000:00:10.0/0000:02:00.0", "aacraid", aacIDs)
	f.host(a, "host3", map[string]string{"proc_name": "aacraid", "unique_id": "1", "serial_number": "AAA"})
	f.host(b, "host2", map[string]string{"proc_name": "aacraid", "unique_id": "0", "serial_number": "BBB"})

	hosts, herrs, err := Discover(f.root)
	if err != nil || len(herrs) != 0 {
		t.Fatalf("err=%v herrs=%v", err, herrs)
	}
	got := map[string][2]string{}
	for _, h := range hosts {
		got[h.Name] = [2]string{h.PCI.Address, h.UniqueID + "/" + h.SerialNumber}
	}
	if got["host3"] != [2]string{"0000:01:00.0", "1/AAA"} || got["host2"] != [2]string{"0000:02:00.0", "0/BBB"} {
		t.Fatalf("rattachement incorrect : %v", got)
	}
	if hosts[0].Name != "host2" {
		t.Fatalf("tri par numéro d'hôte attendu, premier = %s", hosts[0].Name)
	}
}

// Un hôte sans parent PCI (hôte virtuel) est signalé sans bloquer les autres.
func TestDiscoverHostWithoutPCIParent(t *testing.T) {
	f := newFixture(t)
	aac := f.pciFunc("pci0000:00/0000:00:09.0/0000:01:00.0", "aacraid", aacIDs)
	f.host(aac, "host0", map[string]string{"proc_name": "aacraid", "unique_id": "0"})
	virt := filepath.Join(f.root, "devices", "virtual")
	mustMkdir(t, virt)
	f.host(virt, "host5", map[string]string{"proc_name": "usb-storage"})

	hosts, herrs, err := Discover(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].Name != "host0" {
		t.Fatalf("hôtes = %+v", hosts)
	}
	if len(herrs) != 1 || herrs[0].Host != "host5" || !errors.Is(herrs[0], ErrNoPCIParent) {
		t.Fatalf("erreurs = %v", herrs)
	}
}

// Un attribut absent (série non publiée) donne une valeur vide, pas une erreur.
func TestDiscoverMissingOptionalAttributes(t *testing.T) {
	f := newFixture(t)
	aac := f.pciFunc("pci0000:00/0000:00:09.0/0000:01:00.0", "", aacIDs)
	f.host(aac, "host0", map[string]string{"proc_name": "aacraid"})
	hosts, herrs, err := Discover(f.root)
	if err != nil || len(herrs) != 0 || len(hosts) != 1 {
		t.Fatalf("hosts=%v herrs=%v err=%v", hosts, herrs, err)
	}
	h := hosts[0]
	if h.SerialNumber != "" || h.UniqueID != "" || h.PCI.Driver != "" {
		t.Fatalf("valeurs inattendues : %+v", h)
	}
}

func TestDiscoverUnreadableClassDir(t *testing.T) {
	if _, _, err := Discover(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("une racine absente doit être une erreur globale")
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
