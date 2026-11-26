// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package sysfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	ahci := f.pciFunc("pci0000:00/0000:00:14.0", "ahci", map[string]string{"vendor": "0x8086", "class": "0x010601", "device": "0xa102", "subsystem_vendor": "0x1043", "subsystem_device": "0x8694"})
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
	if !reflect.DeepEqual(h, want) {
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
	// Hôte non PCI : hors périmètre, ni retenu ni signalé (sinon la
	// découverte serait en échec permanent sur un hôte avec un disque USB
	// relié hors PCI, iSCSI ou VMBus).
	if len(herrs) != 0 {
		t.Fatalf("erreurs = %v", herrs)
	}
}

// Un ancêtre à adresse PCI sans fichier vendor lisible est une anomalie de
// lecture : l'hôte reste en erreur, il ne disparaît pas silencieusement de
// l'inventaire (ce qui autoriserait la suppression de ses nœuds).
func TestDiscoverUnreadablePCIParent(t *testing.T) {
	f := newFixture(t)
	aac := f.pciFunc("pci0000:00/0000:00:09.0/0000:01:00.0", "aacraid", map[string]string{"class": "0x010400"})
	f.host(aac, "host0", map[string]string{"proc_name": "aacraid", "unique_id": "0"})
	hosts, herrs, err := Discover(f.root)
	if err != nil || len(hosts) != 0 {
		t.Fatalf("err=%v hôtes=%+v", err, hosts)
	}
	if len(herrs) != 1 || herrs[0].Host != "host0" || !errors.Is(herrs[0], ErrPCIUnreadable) {
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

// device crée un périphérique SCSI H:C:T:L sous l'hôte, avec son nœud
// générique (sg vide : aucun) et sa page VPD 0x80 (nil : absente).
func (f *fixture) device(pciDir, host, addr, sg string, vpd []byte) string {
	var h, c, tg, l int
	if _, err := fmt.Sscanf(addr, "%d:%d:%d:%d", &h, &c, &tg, &l); err != nil {
		f.t.Fatal(err)
	}
	dir := filepath.Join(pciDir, host, fmt.Sprintf("target%d:%d:%d", h, c, tg), addr)
	mustMkdir(f.t, dir)
	if sg != "" {
		mustMkdir(f.t, filepath.Join(dir, "scsi_generic", sg))
	}
	if vpd != nil {
		if err := os.WriteFile(filepath.Join(dir, "vpd_pg80"), vpd, 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	return dir
}

// vpd80 construit une page VPD 0x80 de 20 caractères cadrés à droite,
// comme celle d'un disque SATA présenté par aacraid.
func vpd80(serial string) []byte {
	s := fmt.Sprintf("%20s", serial)
	return append([]byte{0, 0x80, 0, byte(len(s))}, s...)
}

// Hôte de référence : volume sur le canal 0 (sans page VPD), disques sur le
// canal 1 avec leur nœud générique et leur numéro de série.
func TestDiscoverDevices(t *testing.T) {
	f := newFixture(t)
	aac := f.pciFunc("pci0000:00/0000:00:09.0/0000:01:00.0", "aacraid", aacIDs)
	f.host(aac, "host0", map[string]string{"proc_name": "aacraid", "unique_id": "0"})
	f.device(aac, "host0", "0:0:0:0", "sg0", nil)
	f.device(aac, "host0", "0:1:7:0", "sg8", vpd80("TESTSN07"))
	f.device(aac, "host0", "0:1:0:0", "sg1", vpd80("TESTSN00"))
	mustMkdir(t, filepath.Join(aac, "host0", "power"))           // entrée sans rapport
	f.device(aac, "host0", "1:1:5:0", "sg5", vpd80("AUTREHOTE")) // autre hôte : ignoré
	hosts, herrs, err := Discover(f.root)
	if err != nil || len(herrs) != 0 || len(hosts) != 1 {
		t.Fatalf("err=%v herrs=%v hôtes=%d", err, herrs, len(hosts))
	}
	want := []SCSIDevice{
		{Address: "0:0:0:0", Channel: 0, Target: 0, LUN: 0, Generic: "sg0"},
		{Address: "0:1:0:0", Channel: 1, Target: 0, LUN: 0, Generic: "sg1", Serial: "TESTSN00"},
		{Address: "0:1:7:0", Channel: 1, Target: 7, LUN: 0, Generic: "sg8", Serial: "TESTSN07"},
	}
	if !reflect.DeepEqual(hosts[0].Devices, want) {
		t.Fatalf("périphériques :\n got %+v\nwant %+v", hosts[0].Devices, want)
	}
}

// Valeurs facultatives mal formées : champ vide, sans erreur (elles
// concernent aussi les disques d'autres pilotes, dont l'anomalie ne doit pas
// mettre la découverte en échec).
func TestDiscoverDevicesMalformed(t *testing.T) {
	cases := map[string]func(f *fixture, aac string){
		"deux nœuds génériques": func(f *fixture, aac string) {
			d := f.device(aac, "host0", "0:1:7:0", "sg8", vpd80("TESTSN07"))
			mustMkdir(f.t, filepath.Join(d, "scsi_generic", "sg9"))
		},
		"page VPD d'un autre code": func(f *fixture, aac string) {
			b := vpd80("TESTSN07")
			b[1] = 0x83
			f.device(aac, "host0", "0:1:7:0", "sg8", b)
		},
		// Numéro cadré à gauche, coupé au milieu : seule la borne de
		// longueur le refuse.
		"page VPD tronquée": func(f *fixture, aac string) {
			b := append([]byte{0, 0x80, 0, 20}, "TESTSN07            "...)
			f.device(aac, "host0", "0:1:7:0", "sg8", b[:9])
		},
		"page VPD vide": func(f *fixture, aac string) {
			f.device(aac, "host0", "0:1:7:0", "sg8", []byte{})
		},
		"série non affichable": func(f *fixture, aac string) {
			f.device(aac, "host0", "0:1:7:0", "sg8", vpd80("TEST\x01N07"))
		},
	}
	for name, build := range cases {
		f := newFixture(t)
		aac := f.pciFunc("pci0000:00/0000:00:09.0/0000:01:00.0", "aacraid", aacIDs)
		f.host(aac, "host0", map[string]string{"proc_name": "aacraid", "unique_id": "0"})
		build(f, aac)
		hosts, herrs, err := Discover(f.root)
		if err != nil || len(herrs) != 0 || len(hosts) != 1 || len(hosts[0].Devices) != 1 {
			t.Fatalf("%s : err=%v herrs=%v hôtes=%+v", name, err, herrs, hosts)
		}
		d := hosts[0].Devices[0]
		if d.Target != 7 || (d.Generic == "") == (d.Serial == "") {
			t.Fatalf("%s : %+v (un seul des deux champs doit être vide)", name, d)
		}
	}
}

// Liste illisible (ici une cible qui n'est pas un répertoire) : l'hôte reste
// dans l'inventaire, sans périphériques, et l'erreur est signalée.
func TestDiscoverDevicesUnreadable(t *testing.T) {
	f := newFixture(t)
	aac := f.pciFunc("pci0000:00/0000:00:09.0/0000:01:00.0", "aacraid", aacIDs)
	f.host(aac, "host0", map[string]string{"proc_name": "aacraid", "unique_id": "0"})
	f.device(aac, "host0", "0:1:0:0", "sg1", vpd80("TESTSN00"))
	mustWrite(t, filepath.Join(aac, "host0", "target0:1:7"), "")
	hosts, herrs, err := Discover(f.root)
	if err != nil || len(hosts) != 1 || hosts[0].Devices != nil || len(herrs) != 1 || herrs[0].Host != "host0" {
		t.Fatalf("err=%v hôtes=%+v herrs=%v", err, hosts, herrs)
	}
}

// Attribut PCI obligatoire absent, illisible ou vide : erreur d'hôte (la
// carte n'est pas publiée avec un pilote ou des identifiants vides). Lien
// driver absent : aucun pilote lié, sans erreur.
func TestDiscoverRequiredPCIAttributes(t *testing.T) {
	for _, attr := range []string{"class", "vendor", "device", "subsystem_vendor", "subsystem_device"} {
		for _, mode := range []string{"absent", "vide"} {
			f := newFixture(t)
			aac := f.pciFunc("pci0000:00/0000:00:09.0/0000:01:00.0", "aacraid", aacIDs)
			f.host(aac, "host0", map[string]string{"proc_name": "aacraid", "unique_id": "0"})
			p := filepath.Join(aac, attr)
			if mode == "absent" {
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
			} else {
				mustWrite(t, p, "\n")
			}
			hosts, herrs, err := Discover(f.root)
			if err != nil || len(hosts) != 0 || len(herrs) != 1 || herrs[0].Host != "host0" {
				t.Fatalf("%s %s : err=%v hôtes=%+v herrs=%v", attr, mode, err, hosts, herrs)
			}
		}
	}
	// Lien driver illisible (boucle de liens) : erreur ; absent : sans pilote.
	f := newFixture(t)
	aac := f.pciFunc("pci0000:00/0000:00:09.0/0000:01:00.0", "", aacIDs)
	f.host(aac, "host0", map[string]string{"proc_name": "aacraid", "unique_id": "0"})
	hosts, herrs, err := Discover(f.root)
	if err != nil || len(herrs) != 0 || len(hosts) != 1 || hosts[0].PCI.Driver != "" {
		t.Fatalf("sans pilote : err=%v hôtes=%+v herrs=%v", err, hosts, herrs)
	}
	mustSymlink(t, "driver", filepath.Join(aac, "driver"))
	if hosts, herrs, _ := Discover(f.root); len(hosts) != 0 || len(herrs) != 1 {
		t.Fatalf("lien driver illisible : hôtes=%+v herrs=%v", hosts, herrs)
	}
}

// serial_number présent mais illisible : signalé (SerialUnreadable), à la
// différence d'un attribut absent ou d'une valeur vide.
func TestDiscoverUnreadableSerial(t *testing.T) {
	f := newFixture(t)
	a := f.pciFunc("pci0000:00/0000:00:09.0/0000:01:00.0", "aacraid", aacIDs)
	f.host(a, "host0", map[string]string{"proc_name": "aacraid"})
	mustMkdir(t, filepath.Join(a, "host0/scsi_host/host0/serial_number")) // lecture : EISDIR
	b := f.pciFunc("pci0000:00/0000:00:0a.0/0000:02:00.0", "aacraid", aacIDs)
	f.host(b, "host1", map[string]string{"proc_name": "aacraid", "serial_number": ""})
	c := f.pciFunc("pci0000:00/0000:00:0b.0/0000:03:00.0", "aacraid", aacIDs)
	f.host(c, "host2", map[string]string{"proc_name": "aacraid"})
	hosts, herrs, err := Discover(f.root)
	if err != nil || len(herrs) != 0 || len(hosts) != 3 {
		t.Fatalf("hosts=%v herrs=%v err=%v", hosts, herrs, err)
	}
	for i, want := range []bool{true, false, false} {
		if hosts[i].SerialUnreadable != want || hosts[i].SerialNumber != "" {
			t.Errorf("%s : illisible %v, numéro %q", hosts[i].Name, hosts[i].SerialUnreadable, hosts[i].SerialNumber)
		}
	}
}
