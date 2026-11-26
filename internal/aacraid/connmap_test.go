// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/DisasteR/hwraid_exporter/internal/allowlist"
)

// Requête de l'outil de référence (fibcap, FSACTL_SEND_LARGE_FIB) : douze
// premiers octets d'en-tête ; données nulles. Au-delà, des adresses propres
// à l'outil, que l'exporter met à zéro.
func TestConnectorMapRequestMatchesCapture(t *testing.T) {
	want := make([]byte, LargeFIBSize)
	copy(want, mustHex(t, "01001000c402010004000008"))
	if got := ConnectorMapRequest(); !bytes.Equal(got, want) {
		t.Fatalf("got %x", got[:32])
	}
}

// Seul FSACTL_SEND_LARGE_FIB accepte la requête 708, et il n'accepte
// qu'elle : toute modification d'un octet, toute requête de FSACTL_SENDFIB
// et tout tampon nul sont refusés.
func TestLargeFIBAllowlist(t *testing.T) {
	l := mustList(t)
	ok := func(code uint32, b []byte) bool {
		_, err := l.Check(allowlist.Request{Ioctl: code, Buf: b})
		if err != nil && !errors.Is(err, allowlist.ErrDenied) {
			t.Fatal(err)
		}
		return err == nil
	}
	ref := ConnectorMapRequest()
	for name, code := range knownIoctls {
		if want := name == "FSACTL_SEND_LARGE_FIB"; ok(code, ref) != want {
			t.Fatalf("%s : acceptation %v, attendue %v", name, !want, want)
		}
	}
	for i := range ref {
		for _, v := range []byte{0x00, 0x01, 0x16, 0x80, 0xff} {
			if ref[i] == v {
				continue
			}
			b := append([]byte(nil), ref...)
			b[i] = v
			if ok(IoctlSendLargeFIB, b) {
				t.Fatalf("octet %d = %#x accepté", i, v)
			}
		}
	}
	// Requêtes de FSACTL_SENDFIB, complétées à 2 048 octets : refusées.
	for _, q := range append(qualifiedRequests(), ChunkRequest(0x1000, 1, 0)) {
		b := make([]byte, LargeFIBSize)
		copy(b, q)
		if ok(IoctlSendLargeFIB, b) {
			t.Fatal("requête FSACTL_SENDFIB acceptée par FSACTL_SEND_LARGE_FIB")
		}
	}
	if ok(IoctlSendLargeFIB, make([]byte, LargeFIBSize)) || ok(IoctlSendLargeFIB, ref[:FIBSize]) {
		t.Fatal("tampon nul ou tronqué accepté")
	}
	// La requête 708 ne passe pas par FSACTL_SENDFIB, même tronquée.
	small := append([]byte(nil), ref[:FIBSize]...)
	binary.LittleEndian.PutUint16(small[fibSenderSize:], FIBSize)
	if ok(IoctlSendFIB, small) {
		t.Fatal("708 acceptée par FSACTL_SENDFIB")
	}
}

func connectorMapFixture(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, LargeFIBSize)
	copy(b, fixture(t, "708"))
	return b
}

// Réponse de l'hôte de référence (adresse SAS de la carte remplacée) :
// entrées identiques à « Reported Location » et « Attached PHY Identifier »
// de l'outil témoin pour les huit disques.
func TestDecodeConnectorMapReference(t *testing.T) {
	m, err := DecodeConnectorMap(connectorMapFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Names) != 2 || m.Names[0] != "CN0" || m.Names[1] != "CN1" {
		t.Fatalf("connecteurs %v", m.Names)
	}
	if len(m.Entries) != 8 {
		t.Fatalf("%d entrées", len(m.Entries))
	}
	for n := uint32(0); n < 8; n++ {
		e, ok := m.Locate(n)
		want := ConnectorEntry{Disk: n, Connector: uint8(n / 4), Device: uint8(n % 4), Phy: uint8(4*(n/4) + 3 - n%4)}
		if !ok || e != want {
			t.Fatalf("disque %d : %+v, attendu %+v", n, e, want)
		}
	}
	if _, ok := m.Locate(8); ok {
		t.Fatal("disque absent localisé")
	}
	if _, ok := m.Locate(1<<24 | 7); ok {
		t.Fatal("disque d'un autre canal localisé")
	}
}

// Formes non qualifiées refusées.
func TestDecodeConnectorMapRejects(t *testing.T) {
	le := binary.LittleEndian
	cases := map[string]func(b []byte) []byte{
		"tampon court":      func(b []byte) []byte { return b[:FIBSize] },
		"autre commande":    func(b []byte) []byte { le.PutUint16(b[fibCommand:], 706); return b },
		"premier mot":       func(b []byte) []byte { b[FIBHeaderSize] = 2; return b },
		"trois connecteurs": func(b []byte) []byte { b[FIBHeaderSize+cmapConnectors] = 3; return b },
		// Neuvième entrée valide et distincte, mais au-delà de la taille
		// annoncée : seule la borne de taille la refuse.
		"entrées hors taille": func(b []byte) []byte {
			b[FIBHeaderSize+cmapEntries] = 9
			e := b[FIBHeaderSize+cmapEntryBase+8*cmapEntrySize:]
			e[0], e[4], e[5], e[6] = 8, 0, 0, 8
			return b
		},
		"taille trop petite": func(b []byte) []byte { le.PutUint16(b[fibSize:], 40); return b },
		// Filtre de l'outil : aucune entrée.
		"aucune entrée":      func(b []byte) []byte { b[FIBHeaderSize+cmapEntries] = 0; return b },
		"connecteur inconnu": func(b []byte) []byte { b[FIBHeaderSize+cmapEntryBase+4] = 5; return b },
		"disque en double":   func(b []byte) []byte { b[FIBHeaderSize+cmapEntryBase+16] = 3; return b },
		// Deux connecteurs numérotés 0, toutes les entrées sur le 0 : seul le
		// contrôle des doublons de connecteur la refuse.
		"connecteur en double": func(b []byte) []byte {
			b[FIBHeaderSize+cmapConnectorBase+cmapConnectorSize] = 0
			for i := 0; i < 8; i++ {
				b[FIBHeaderSize+cmapEntryBase+i*cmapEntrySize+4] = 0
			}
			return b
		},
	}
	for name, mut := range cases {
		if _, err := DecodeConnectorMap(mut(connectorMapFixture(t))); err == nil {
			t.Fatalf("%s : accepté", name)
		}
	}
}
