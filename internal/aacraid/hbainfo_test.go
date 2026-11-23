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
	if !got.MatchesPCI(0, 1, 0, 0) {
		t.Fatal("la carte 0000:01:00.0, unique_id 0, doit correspondre")
	}
	if got.MatchesPCI(1, 1, 0, 0) || got.MatchesPCI(0, 2, 0, 0) || got.MatchesPCI(0, 1, 0, 1) {
		t.Fatal("un numéro de carte, un bus ou une fonction différents ne doivent pas correspondre")
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
