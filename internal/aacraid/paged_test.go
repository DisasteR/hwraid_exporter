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

// Requêtes de l'outil de référence (GETCONFIG AL) : 12 premiers octets
// d'en-tête et huit premiers mots de données. Au-delà, l'outil laisse des
// résidus de ses tampons ; l'exporter envoie des zéros.
var capturedPaged = []struct {
	name, hdr, words string
	build            func() ([]byte, error)
}{
	{"229 ouverture", "01001000f4010100e0010002", "02000000e50000000000000000000000da000000ffffffff0000000008040000",
		func() ([]byte, error) { return OpenRequest(ReadContainerList, 0, ContainerListSize(256)) }},
	// CT_GET_CONTAINER_COUNT : requête de l'outil de référence aux mots 3 et
	// 7 nuls, après mise à zéro des résidus de l'en-tête (octets 7, 12-19 et
	// 24-31), qui varient sans effet sur la réponse.
	{"comptage des conteneurs", "01001000f4010100e0010002", "0200000004000000000000000000000000000000ffffffff0000000000000000",
		func() ([]byte, error) { return ContainerCountRequest(), nil }},
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
		{ReadContainerList, 0, 0},
		{ReadContainerList, 0, ContainerListSize(0)},
		{ReadContainerList, 0, ContainerListSize(MaxContainersLimit + 1)},
		{ReadContainerList, 0, ContainerListSize(256) + 2},
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
		"229 ouverture": {7: true}, "247 ouverture": {}, "comptage des conteneurs": {},
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
	// Taille de la liste des conteneurs : 8 + 4 × n, n dans 1..1023.
	open229 := func(size uint32) []byte {
		b := pagedRequest(vmContainerConfig, uint32(ReadContainerList), 0, 0, markDA, 0xffffffff, 0, size)
		return b
	}
	for _, size := range []uint32{ContainerListSize(1), ContainerListSize(256), ContainerListSize(MaxContainersLimit)} {
		if !check(open229(size)) {
			t.Fatalf("liste des conteneurs de %d octets refusée", size)
		}
	}
	for _, size := range []uint32{0, 8, ContainerListSize(1) + 1, ContainerListSize(256) + 2, ContainerListSize(MaxContainersLimit + 1)} {
		if check(open229(size)) {
			t.Fatalf("liste des conteneurs de %d octets acceptée", size)
		}
	}
	// Comptage : variantes résiduelles de l'outil refusées (seule la forme
	// aux résidus nuls est émise).
	for _, word := range []int{3, 7} {
		b := ContainerCountRequest()
		b[FIBHeaderSize+4*word] = 0x40
		if check(b) {
			t.Fatalf("comptage avec le mot %d non nul accepté", word)
		}
	}
}

// Réponse capturée à CT_GET_CONTAINER_COUNT sur la carte de référence
// (champs utiles identiques sur les 21 réponses, en-têtes distincts) : Size 496, statut 0, mot 1 = 4,
// MaxContainers = 256, puis ContainerSwitchEntries, MaxPartitions et
// MaxSimpleVolumes (Linux aacraid.h).
func containerCountResponse(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, FIBSize)
	copy(b, mustHex(t, "00000000f4010100f0010002")) // en-tête : commande et Size (adresses du pilote omises)
	copy(b[FIBHeaderSize:], mustHex(t, "0000000004000000000100004000000001020000ffffffff"))
	return b
}

func TestDecodeContainerCount(t *testing.T) {
	le := binary.LittleEndian
	c, err := DecodeContainerCount(containerCountResponse(t))
	if err != nil || c.Max() != 256 {
		t.Fatalf("réponse capturée : %d, %v", c.Max(), err)
	}
	if c.ListSize() != 0x408 {
		t.Fatalf("taille de la liste : %#x, 0x408 attendu (capture)", c.ListSize())
	}
	if (ContainerCount{}).ListSize() != 0 {
		t.Fatal("valeur nulle : taille de liste non nulle")
	}
	ok := func(mut func(b []byte)) (uint32, error) {
		b := containerCountResponse(t)
		mut(b)
		c, err := DecodeContainerCount(b)
		return c.Max(), err
	}
	if n, err := ok(func(b []byte) { le.PutUint32(b[FIBHeaderSize+8:], MaxContainersLimit) }); err != nil || n != MaxContainersLimit {
		t.Fatalf("MaxContainers = %d : %d, %v", MaxContainersLimit, n, err)
	}
	if n, err := ok(func(b []byte) { le.PutUint32(b[FIBHeaderSize+8:], 1) }); err != nil || n != 1 {
		t.Fatalf("MaxContainers = 1 : %d, %v", n, err)
	}
	bad := map[string]func(b []byte){
		"MaxContainers nul":       func(b []byte) { le.PutUint32(b[FIBHeaderSize+8:], 0) },
		"MaxContainers 1024":      func(b []byte) { le.PutUint32(b[FIBHeaderSize+8:], MaxContainersLimit+1) },
		"statut non nul":          func(b []byte) { le.PutUint32(b[FIBHeaderSize:], 1) },
		"mauvais écho":            func(b []byte) { le.PutUint32(b[FIBHeaderSize+4:], 229) },
		"champ Size de 32 octets": func(b []byte) { le.PutUint16(b[fibSize:], 32) },
		"autre commande":          func(b []byte) { le.PutUint16(b[fibCommand:], 706) },
	}
	for name, mut := range bad {
		if _, err := ok(mut); err == nil {
			t.Fatalf("%s accepté", name)
		}
	}
	if _, err := DecodeContainerCount(make([]byte, 100)); err == nil {
		t.Fatal("réponse tronquée acceptée")
	}
}
