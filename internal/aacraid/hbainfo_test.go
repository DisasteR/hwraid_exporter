package aacraid

import (
	"encoding/binary"
	"testing"
)

// Valeurs de référence calculées par le compilateur C sur la définition
// de struct aac_hba_info (aacraid.h) et sur la macro CTL_CODE.
func TestConstantsMatchKernelABI(t *testing.T) {
	if IoctlGetHBAInfo != 0x42198 {
		t.Fatalf("FSACTL_GET_HBA_INFO = %#x, attendu 0x42198", IoctlGetHBAInfo)
	}
	if HBAInfoSize != 200 {
		t.Fatalf("sizeof(aac_hba_info) = %d, attendu 200", HBAInfoSize)
	}
}

// Chaque champ est écrit avec une valeur distincte à son décalage réel :
// une erreur de décalage ou d'ordre des octets produit une valeur fausse.
func TestDecodeHBAInfoFields(t *testing.T) {
	b := make([]byte, HBAInfoSize)
	for i := range b {
		b[i] = 0xEE // bruit : tout octet lu au mauvais endroit se voit
	}
	le := binary.LittleEndian
	b[50], b[51], b[52] = 0, 0x01, 0x00
	le.PutUint32(b[56:], 0)
	le.PutUint32(b[60:], 0x9005)
	le.PutUint32(b[64:], 0x028b)
	le.PutUint32(b[68:], 0x9005)
	le.PutUint32(b[72:], 0x0800)

	got, err := DecodeHBAInfo(b)
	if err != nil {
		t.Fatal(err)
	}
	want := HBAInfo{AdapterNumber: 0, Bus: 1, Device: 0, Function: 0,
		VendorID: 0x9005, DeviceID: 0x028b, SubVendorID: 0x9005, SubSystemID: 0x0800}
	if got != want {
		t.Fatalf("obtenu %+v, attendu %+v", got, want)
	}
	id := PCIIdentity{Bus: 1, Slot: 0, Function: 0, Vendor: 0x9005, Device: 0x028b, SubVendor: 0x9005, SubDevice: 0x0800}
	if !got.Matches(0, id) {
		t.Fatal("la carte 0000:01:00.0, unique_id 0, doit correspondre")
	}
	for name, mod := range map[string]func(*PCIIdentity){
		"bus": func(i *PCIIdentity) { i.Bus = 2 }, "slot": func(i *PCIIdentity) { i.Slot = 1 }, "fonction": func(i *PCIIdentity) { i.Function = 1 },
		"fabricant": func(i *PCIIdentity) { i.Vendor = 0x1000 }, "puce": func(i *PCIIdentity) { i.Device = 0x028c },
		"sous-fabricant": func(i *PCIIdentity) { i.SubVendor = 0x103c }, "sous-système": func(i *PCIIdentity) { i.SubDevice = 0x0301 },
	} {
		other := id
		mod(&other)
		if got.Matches(0, other) {
			t.Fatalf("%s différent accepté", name)
		}
	}
	if got.Matches(1, id) {
		t.Fatal("un autre numéro de carte accepté")
	}
}

// Toute taille autre que 200 octets est refusée, y compris tous les préfixes.
func TestDecodeHBAInfoRejectsWrongSizes(t *testing.T) {
	full := make([]byte, HBAInfoSize+1)
	for n := 0; n <= len(full); n++ {
		if n == HBAInfoSize {
			continue
		}
		if _, err := DecodeHBAInfo(full[:n]); err == nil {
			t.Fatalf("taille %d acceptée", n)
		}
	}
}
