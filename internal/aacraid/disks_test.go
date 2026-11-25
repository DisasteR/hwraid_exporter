// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"encoding/binary"
	"fmt"
	"testing"
)

// Données de l'hôte de référence, numéros de série et WWN remplacés par des
// valeurs fictives. Valeurs attendues relevées dans l'outil témoin (même
// capture) : fabricant, modèle, firmware, capacité.
func TestDecodeDiskInfoReference(t *testing.T) {
	want := map[uint32][3]string{
		0: {"ST8000VN0022-2EL", "SC61"}, 1: {"ST8000VN004-3CP1", "SC60"}, 2: {"ST8000VN004-3CP1", "SC60"},
		3: {"ST8000VN0022-2EL", "SC61"}, 4: {"ST8000VN0022-2EL", "SC61"}, 5: {"ST8000VN004-3CP1", "SC60"},
		6: {"ST8000VN0022-2EL", "SC61"}, 7: {"ST8000VN0022-2EL", "SC61"},
	}
	for n, w := range want {
		i, err := DecodeDiskInfo(fixture(t, fmt.Sprintf("248-%x", n)))
		if err != nil {
			t.Fatal(err)
		}
		exp := DiskInfo{Number: n, Vendor: "ATA", Model: w[0], Firmware: w[1], Serial: fmt.Sprintf("TESTSN0%d", n),
			WWN: fmt.Sprintf("0x5000c5000000000%d", n), Blocks: 15628053168, BlockSize: 512,
			// Phy relevé par l'outil témoin (Attached PHY Identifier).
			Phy: uint8(4*(n/4) + 3 - n%4),
			// Octets d'état identiques sur les huit disques, « Online » pour
			// l'outil témoin (docs/aacraid-interface.md, section 6 bis).
			Flags0: 0x03, Status68: 0x70, Status69: 0x10}
		if i != exp {
			t.Fatalf("disque %d :\n got %+v\nwant %+v", n, i, exp)
		}
		// 7 630 885 Mo affichés par l'outil témoin.
		if got := i.CapacityBytes() >> 20; got != 7630885 {
			t.Fatalf("disque %d : %d Mio", n, got)
		}
	}
}

// Champs limites : série de 20 caractères sans octet nul (ne déborde pas
// sur le champ suivant), WWN nul, taille de bloc inconnue, texte non
// affichable, réponse trop courte.
func TestDecodeDiskInfoEdges(t *testing.T) {
	d := fixture(t, "248-0")
	copy(d[116:136], "ABCDEFGHIJKLMNOPQRST")
	copy(d[136:140], "XXXX")
	copy(d[288:296], make([]byte, 8))
	d[64], d[65] = 0x01, 0x02 // 0x201 octets par bloc : inconnu
	d[96] = 0xff
	i, err := DecodeDiskInfo(d)
	if err != nil {
		t.Fatal(err)
	}
	if i.Serial != "ABCDEFGHIJKLMNOPQRST" || i.WWN != "" || i.CapacityBytes() != 0 || i.Model != "?T8000VN0022-2EL" {
		t.Fatalf("%+v", i)
	}
	if _, err := DecodeDiskInfo(d[:DiskInfoNeed-1]); err == nil {
		t.Fatal("réponse trop courte acceptée")
	}
	d[64], d[65] = 0, 0x10 // 4 096 octets par bloc
	if i, _ := DecodeDiskInfo(d); i.CapacityBytes() != i.Blocks*4096 {
		t.Fatal("blocs de 4 096 octets non pris en compte")
	}
}

// Chaque octet d'état est lu à sa position : une valeur distincte posée à
// chaque position se retrouve dans le bon champ, et seulement là.
func TestDecodeDiskInfoStatusOffsets(t *testing.T) {
	d := fixture(t, "248-0")
	d[0], d[68], d[69], d[70], d[80] = 0xa1, 0xa2, 0xa3, 0xa4, 0xa5
	binary.LittleEndian.PutUint32(d[332:], 0xa6a7a8a9)
	i, err := DecodeDiskInfo(d)
	if err != nil {
		t.Fatal(err)
	}
	if i.Flags0 != 0xa1 || i.Status68 != 0xa2 || i.Status69 != 0xa3 || i.Status70 != 0xa4 || i.Qualifier != 0xa5 || i.Status332 != 0xa6a7a8a9 {
		t.Fatalf("%+v", i)
	}
	if _, err := DecodeDiskInfo(d[:335]); err == nil {
		t.Fatal("réponse sans les octets 332-335 acceptée")
	}
}
