// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSysfs construit une arborescence sysfs : fonctions PCI sous devices/,
// liens class/scsi_host et bus/pci/drivers, comme le noyau.
type fakeSysfs struct {
	t    *testing.T
	root string
}

func newFakeSysfs(t *testing.T) *fakeSysfs {
	root := t.TempDir()
	for _, d := range []string{"class/scsi_host", "bus/pci/drivers/aacraid", "bus/pci/drivers/megaraid_sas", "bus/pci/drivers/ahci"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &fakeSysfs{t, root}
}

func (f *fakeSysfs) write(path, v string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(v+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// card crée une fonction PCI liée à driver et son hôte SCSI ; attrs : attributs
// de l'hôte (serial_number absent si non fourni).
func (f *fakeSysfs) card(addr, driver, class, subdev, host string, attrs map[string]string) string {
	dir := filepath.Join(f.root, "devices/pci0000:00", addr)
	for k, v := range map[string]string{"vendor": "0x9005", "device": "0x028b", "subsystem_vendor": "0x9005", "subsystem_device": subdev, "class": class} {
		f.write(filepath.Join(dir, k), v)
	}
	if err := os.Symlink(filepath.Join(f.root, "bus/pci/drivers", driver), filepath.Join(dir, "driver")); err != nil {
		f.t.Fatal(err)
	}
	hdir := filepath.Join(dir, host, "scsi_host", host)
	if err := os.MkdirAll(hdir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	for k, v := range attrs {
		f.write(filepath.Join(hdir, k), v)
	}
	if err := os.Symlink(hdir, filepath.Join(f.root, "class/scsi_host", host)); err != nil {
		f.t.Fatal(err)
	}
	return hdir
}

type discoverOut struct {
	Cards  []discoveredCard `json:"cards"`
	Errors []string         `json:"errors"`
}

func runDiscover(t *testing.T, args ...string) (discoverOut, int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := discover(args, &out, &errb)
	var d discoverOut
	if code == 0 {
		if err := json.Unmarshal(out.Bytes(), &d); err != nil {
			t.Fatalf("sortie non JSON : %v\n%s", err, out.String())
		}
	}
	return d, code, errb.String()
}

// discover : cartes de l'inventaire de serve seulement (le contrôleur SATA
// en est absent), avec module, identité et statut de lecture tiré du verrou
// du module ; l'option -allow-unqualified change le statut d'une carte non
// qualifiée (module entier, ou numéro de série unique) ; un numéro de série
// illisible est signalé et suspend les sélecteurs serial:.
func TestDiscoverStatuses(t *testing.T) {
	f := newFakeSysfs(t)
	aac := map[string]string{"proc_name": "aacraid", "model": "6805", "hba_kernel_version": "5.3-0[19204]", "serial_number": "QUAL1", "unique_id": "0"}
	f.card("0000:01:00.0", "aacraid", "0x010400", "0x0301", "host1", aac)
	other := map[string]string{"proc_name": "aacraid", "model": "6405", "hba_kernel_version": "5.2-0[18000]", "serial_number": "OTHER1", "unique_id": "1"}
	f.card("0000:02:00.0", "aacraid", "0x010400", "0x0300", "host2", other)
	f.card("0000:03:00.0", "megaraid_sas", "0x010400", "0x0300", "host3", map[string]string{"proc_name": "megaraid_sas"})
	f.card("0000:04:00.0", "ahci", "0x010601", "0x0300", "host4", map[string]string{"proc_name": "ahci"})

	status := func(d discoverOut) map[string]string {
		m := map[string]string{}
		for _, c := range d.Cards {
			m[c.PCIAddress] = c.Status
		}
		return m
	}
	d, code, stderr := runDiscover(t, "-sysfs", f.root)
	if code != 0 {
		t.Fatalf("code %d : %s", code, stderr)
	}
	// Valeurs littérales : ce sont celles que lisent les exploitants.
	want := map[string]string{"0000:01:00.0": "read", "0000:02:00.0": "needs_allow_unqualified", "0000:03:00.0": "no_module"}
	if got := status(d); len(got) != len(want) || got["0000:01:00.0"] != want["0000:01:00.0"] || got["0000:02:00.0"] != want["0000:02:00.0"] || got["0000:03:00.0"] != want["0000:03:00.0"] {
		t.Fatalf("statuts %v, attendu %v (contrôleur SATA absent)", got, want)
	}
	c := d.Cards[0]
	if c.Module != "aacraid" || c.Model != "6805" || c.Firmware != "5.3-0[19204]" || c.Serial != "QUAL1" || c.Driver != "aacraid" || len(c.Hosts) != 1 || c.Hosts[0] != "host1" {
		t.Fatalf("carte qualifiée : %+v", c)
	}
	if d.Cards[2].Module != "" {
		t.Fatalf("carte sans module : %+v", d.Cards[2])
	}
	for _, args := range [][]string{{"-allow-unqualified=aacraid"}, {"-allow-unqualified=serial:OTHER1"}} {
		d, code, stderr := runDiscover(t, append([]string{"-sysfs", f.root}, args...)...)
		if code != 0 || status(d)["0000:02:00.0"] != "read_allowed" || status(d)["0000:01:00.0"] != "read" {
			t.Fatalf("%v : code %d, statuts %v, %s", args, code, status(d), stderr)
		}
	}
	if d, _, _ := runDiscover(t, "-sysfs", f.root, "-allow-unqualified=serial:QUAL1"); status(d)["0000:02:00.0"] != statusNeedsAllow {
		t.Fatalf("autre numéro : %v", status(d))
	}
	// Numéro illisible sur la carte sans module : signalé, sélecteur suspendu
	// pour toutes les cartes ; l'autorisation du module entier n'en dépend pas.
	if err := os.Mkdir(filepath.Join(f.root, "devices/pci0000:00/0000:03:00.0/host3/scsi_host/host3/serial_number"), 0o755); err != nil {
		t.Fatal(err)
	}
	d, _, _ = runDiscover(t, "-sysfs", f.root, "-allow-unqualified=serial:OTHER1")
	if status(d)["0000:02:00.0"] != statusNeedsAllow || !d.Cards[2].SerialUnreadable || d.Cards[1].SerialUnreadable {
		t.Fatalf("numéro illisible : %v, %+v", status(d), d.Cards)
	}
	if d, _, _ := runDiscover(t, "-sysfs", f.root, "-allow-unqualified=aacraid"); status(d)["0000:02:00.0"] != statusReadAllowed {
		t.Fatalf("module entier : %v", status(d))
	}
	// Carte aacraid sans unique_id : nœud non déclarable, statut error avec
	// la cause, même avec l'option.
	f.card("0000:05:00.0", "aacraid", "0x010400", "0x0301", "host5", map[string]string{"proc_name": "aacraid", "model": "6805"})
	d, _, _ = runDiscover(t, "-sysfs", f.root, "-allow-unqualified=aacraid")
	if c := d.Cards[len(d.Cards)-1]; c.PCIAddress != "0000:05:00.0" || c.Status != "error" || c.Error == "" {
		t.Fatalf("déclaration invalide : %+v", c)
	}
	// Hôte illisible (lien vers un répertoire absent) : signalé dans errors,
	// et les sélecteurs serial: suspendus.
	if err := os.Symlink(filepath.Join(f.root, "absent"), filepath.Join(f.root, "class/scsi_host", "host9")); err != nil {
		t.Fatal(err)
	}
	d, _, _ = runDiscover(t, "-sysfs", f.root)
	if len(d.Errors) != 1 || !strings.Contains(d.Errors[0], "host9") {
		t.Fatalf("hôte illisible non signalé : %v", d.Errors)
	}
}

// Options invalides ou argument positionnel : code 2, rien sur la sortie ;
// sysfs illisible : code 1.
func TestDiscoverErrors(t *testing.T) {
	f := newFakeSysfs(t)
	for _, args := range [][]string{{"-allow-unqualified=megaraid"}, {"-allow-unqualified=serial:"}, {"inattendu"}, {"-inconnue"}} {
		var out, errb bytes.Buffer
		if code := discover(append([]string{"-sysfs", f.root}, args...), &out, &errb); code != 2 || out.Len() != 0 {
			t.Fatalf("%v : code %d, sortie %q", args, code, out.String())
		}
	}
	var out, errb bytes.Buffer
	if code := discover([]string{"-sysfs", filepath.Join(f.root, "absent")}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "discovery failed") {
		t.Fatalf("sysfs absent : code %d, %q", code, errb.String())
	}
}
