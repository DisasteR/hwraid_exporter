package aacraid

import (
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
			Phy: uint8(4*(n/4) + 3 - n%4)}
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
