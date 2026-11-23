// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"bytes"
	"errors"
	"testing"

	"github.com/DisasteR/hwraid_exporter/internal/allowlist"
)

// Requêtes de l'outil de référence (GETCONFIG AL) : 12 premiers octets
// d'en-tête et huit premiers mots de données. Au-delà, l'outil laisse des
// résidus de ses tampons ; l'exporter envoie des zéros.
var capturedPaged = []struct {
	name, hdr, words string
	build            func() ([]byte, error)
}{
	{"229 ouverture", "01001000f4010100e0010002", "02000000e50000000000000000000000da000000ffffffff0000000008040000",
		func() ([]byte, error) { return OpenRequest(ReadContainerList, 0, 0) }},
	{"234 ouverture, conteneur 0", "01001000f4010100e0010002", "02000000ea0000000000000000000000da000000ffffffff0000000000100000",
		func() ([]byte, error) { return OpenRequest(ReadContainerInfo, 0, 0) }},
	{"234 ouverture, conteneur 0xfc", "01001000f4010100e0010002", "02000000ea000000fc00000000000000da000000ffffffff0000000000100000",
		func() ([]byte, error) { return OpenRequest(ReadContainerInfo, 0xfc, 0) }},
	{"247 ouverture", "01001000f4010100e0010002", "02000000f7000000000000000000000000000000ffffffff0000000004100000",
		func() ([]byte, error) { return OpenRequest(ReadDiskList, 0, 0) }},
	{"248 ouverture, disque 0", "01001000f4010100e0010002", "02000000f8000000000000000000000000000000ffffffff0000000000100000",
		func() ([]byte, error) { return OpenRequest(ReadDiskInfo, 0, 0) }},
	{"248 ouverture, disque 7", "01001000f4010100e0010002", "02000000f8000000070000000000000000000000ffffffff0000000000100000",
		func() ([]byte, error) { return OpenRequest(ReadDiskInfo, 7, 0) }},
	{"249 ouverture, 4 membres", "01001000f4010100e0010002", "02000000f90000000000001d0000000000000000ffffffff0000000088000000",
		func() ([]byte, error) { return OpenRequest(ReadMemberList, 0x1d000000, MemberListSize(4)) }},
	{"249 ouverture, 2 membres", "01001000f4010100e0010002", "02000000f90000000400001d0000000000000000ffffffff0000000048000000",
		func() ([]byte, error) { return OpenRequest(ReadMemberList, 0x1d000004, MemberListSize(2)) }},
	{"morceau 0", "01001000f4010100e0010002", "0200000053000000c900000000100000da0000001e0602000000000000100000",
		func() ([]byte, error) { return ChunkRequest(0x1000, 0x2061e, 0), nil }},
	{"morceau 1", "01001000f4010100e0010002", "0200000053000000c800000000100000da0000001e0602000100000000100000",
		func() ([]byte, error) { return ChunkRequest(0x1000, 0x2061e, 1), nil }},
	{"clôture après le morceau 9", "01001000f4010100e0010002", "0200000054000000c800000000100000da0000001e0602000900000000100000",
		func() ([]byte, error) { return StopRequest(0x1000, 0x2061e, 9), nil }},
	{"clôture après le morceau 0", "01001000f4010100e0010002", "0200000054000000c800000088000000da000000230602000000000088000000",
		func() ([]byte, error) { return StopRequest(0x88, 0x20623, 0), nil }},
}

// Exigence S4 : partie significative identique, reste nul.
func TestPagedRequestsMatchCapture(t *testing.T) {
	for _, c := range capturedPaged {
		got, err := c.build()
		if err != nil {
			t.Fatalf("%s : %v", c.name, err)
		}
		want := make([]byte, FIBSize)
		copy(want, mustHex(t, c.hdr))
		copy(want[FIBHeaderSize:], mustHex(t, c.words))
		if !bytes.Equal(got, want) {
			t.Fatalf("%s :\n got %x\nwant %x", c.name, got[:64], want[:64])
		}
		if err := CheckFIBRequest(got); err != nil {
			t.Fatalf("%s refusée par la liste blanche : %v", c.name, err)
		}
	}
}

// Formes non qualifiées refusées à la construction.
func TestOpenRequestRejects(t *testing.T) {
	bad := []struct {
		r           PagedRead
		param, size uint32
	}{
		{ReadContainerList, 1, 0},
		{ReadDiskList, 1, 0},
		{ReadDiskInfo, 0x10000, 0},
		{ReadContainerInfo, 0x10000, 0},
		{ReadMemberList, 0, MemberListSize(2)},
		{ReadMemberList, NoUID, MemberListSize(2)},
		{ReadMemberList, 0x1d000000, 7},
		{ReadMemberList, 0x1d000000, MemberListSize(MaxMembers + 1)},
		{PagedRead(244), 0, 0}, // statistiques : non qualifiées
		{PagedRead(ctStopData), 0, 0},
	}
	for _, b := range bad {
		if _, err := OpenRequest(b.r, b.param, b.size); err == nil {
			t.Fatalf("%+v acceptée", b)
		}
	}
}

// Liste blanche structurelle : chaque octet d'une forme qualifiée est
// vérifié ; seuls les champs libres (paramètre, transfert, morceau,
// taille) peuvent varier, dans leurs bornes.
func TestPagedAllowlistMutations(t *testing.T) {
	l := mustList(t)
	check := func(b []byte) bool {
		_, err := l.Check(allowlist.Request{Ioctl: IoctlSendFIB, Buf: b})
		if err != nil && !errors.Is(err, allowlist.ErrDenied) {
			t.Fatal(err)
		}
		return err == nil
	}
	// Mots libres par forme (indices de mots de données).
	free := map[string]map[int]bool{
		"229 ouverture": {}, "247 ouverture": {},
		"248 ouverture, disque 0": {2: true}, "248 ouverture, disque 7": {2: true},
		"234 ouverture, conteneur 0": {2: true}, "234 ouverture, conteneur 0xfc": {2: true},
		"249 ouverture, 4 membres": {2: true, 7: true}, "249 ouverture, 2 membres": {2: true, 7: true},
		"morceau 0": {3: true, 5: true, 6: true, 7: true}, "morceau 1": {3: true, 5: true, 6: true, 7: true},
		"clôture après le morceau 9": {3: true, 5: true, 6: true, 7: true}, "clôture après le morceau 0": {3: true, 5: true, 6: true, 7: true},
	}
	for _, c := range capturedPaged {
		ref, _ := c.build()
		for i := range ref {
			word := (i - FIBHeaderSize) / 4
			if i >= FIBHeaderSize && i < FIBHeaderSize+32 && free[c.name][word] {
				continue
			}
			for _, v := range []byte{0x00, 0x01, 0x16, 0xff} {
				if ref[i] == v {
					continue
				}
				b := append([]byte(nil), ref...)
				b[i] = v
				if check(b) {
					t.Fatalf("%s : octet %d = %#x accepté", c.name, i, v)
				}
			}
		}
	}
	// Bornes des champs libres.
	if check(ChunkRequest(0x1000, 1, 10)) {
		t.Fatal("morceau hors du transfert accepté")
	}
	if check(ChunkRequest(0x2000, 1, 0)) {
		t.Fatal("transfert trop grand accepté")
	}
	b := ChunkRequest(0x1000, 1, 1)
	b[FIBHeaderSize+8] = markFirst // 0xc9 réservé au premier morceau
	if check(b) {
		t.Fatal("marque de premier morceau sur le morceau 1 acceptée")
	}
	if !check(ChunkRequest(0x1004, 0x12345, 9)) || !check(StopRequest(0x1004, 0x12345, 9)) {
		t.Fatal("dernier morceau d'un transfert de 4 100 octets refusé")
	}
}
