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
	// Identifiant sentinelle : jamais un transfert établi (morceau 0, morceau
	// suivant, clôture) ; la valeur voisine reste admise.
	for name, b := range map[string][]byte{
		"morceau 0": ChunkRequest(0x1000, XferNone, 0),
		"morceau 1": ChunkRequest(0x1000, XferNone, 1),
		"clôture":   StopRequest(0x1000, XferNone, 0),
	} {
		if check(b) {
			t.Fatalf("%s sur l'identifiant 0xffffffff accepté", name)
		}
	}
	if !check(ChunkRequest(0x1000, XferNone-1, 0)) || !check(StopRequest(0x1000, XferNone-1, 0)) {
		t.Fatal("identifiant 0xfffffffe refusé")
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

// Clôture après l'ouverture (docs/aacraid-interface.md, section 4) : forme
// de l'outil de référence, mots 2 à 7 de la réponse d'ouverture repris, ici
// ceux de la clôture observée sur carte émulée pour une 229 ouverte avec le
// code 0xe2 (marque 0xc9, 1 032 octets, code 0xe2, morceau 0, taille
// 0x408). La liste blanche n'admet que les ouvertures que l'exporter clôt
// ainsi, dans les bornes de ses mots ; jamais une ouverture CT_OK à total
// non nul (lecture nominale abrégée).
func TestStopAfterOpen(t *testing.T) {
	open := PagedResponse{Mark: 0xc9, Total: 0x408, Code: 0xe2, Xfer: 0x20623, Chunk: 0, Size: 0x408}
	want := make([]byte, FIBSize)
	copy(want, mustHex(t, "01001000f4010100e0010002"))
	copy(want[FIBHeaderSize:], mustHex(t, "0200000054000000c900000008040000e2000000230602000000000008040000"))
	if got := StopAfterOpenRequest(open); !bytes.Equal(got, want) {
		t.Fatalf("clôture après 0xe2 :\n got %x\nwant %x", got[:64], want[:64])
	}
	l := mustList(t)
	check := func(r PagedResponse) bool {
		_, err := l.Check(allowlist.Request{Ioctl: IoctlSendFIB, Buf: StopAfterOpenRequest(r)})
		if err != nil && !errors.Is(err, allowlist.ErrDenied) {
			t.Fatal(err)
		}
		return err == nil
	}
	with := func(f func(*PagedResponse)) PagedResponse { r := open; f(&r); return r }
	for name, tc := range map[string]struct {
		r    PagedResponse
		want bool
	}{
		"0xe2":                       {open, true},
		"0xe2 sous 0xc8":             {with(func(r *PagedResponse) { r.Mark = 0xc8 }), true},
		"total nul, CT_OK":           {with(func(r *PagedResponse) { r.Code, r.Total, r.Size = 0xda, 0, 0 }), true},
		"code 0xdb":                  {with(func(r *PagedResponse) { r.Code = 0xdb }), true},
		"code nul":                   {with(func(r *PagedResponse) { r.Code = 0 }), true},
		"total 0x1004":               {with(func(r *PagedResponse) { r.Total, r.Size = 0x1004, 0x1004 }), true},
		"CT_OK, total non nul":       {with(func(r *PagedResponse) { r.Code = 0xda }), false},
		"0xc8, autre code":           {with(func(r *PagedResponse) { r.Mark, r.Code = 0xc8, 0xdb }), false},
		"autre marque":               {with(func(r *PagedResponse) { r.Mark = 0xc6 }), false},
		"sans transfert":             {with(func(r *PagedResponse) { r.Xfer = XferNone }), false},
		"morceau 1":                  {with(func(r *PagedResponse) { r.Chunk = 1 }), false},
		"taille différente du total": {with(func(r *PagedResponse) { r.Size = 0x800 }), false},
		"total au-delà de 0x1004":    {with(func(r *PagedResponse) { r.Total, r.Size = 0x1008, 0x1008 }), false},
	} {
		if got := tc.r.StopAfterOpen(); got != tc.want {
			t.Errorf("%s : StopAfterOpen %v", name, got)
		}
		if got := check(tc.r); got != tc.want {
			t.Errorf("%s : liste blanche %v", name, got)
		}
	}
	// 0xc8 et CT_OK : pas une clôture après l'ouverture, mais la forme de la
	// clôture nominale après le morceau 0, déjà admise.
	nominal := with(func(r *PagedResponse) { r.Mark, r.Code = 0xc8, 0xda })
	if nominal.StopAfterOpen() || !check(nominal) || !bytes.Equal(StopAfterOpenRequest(nominal), StopRequest(0x408, 0x20623, 0)) {
		t.Fatal("0xc8 et CT_OK : clôture nominale du morceau 0 attendue")
	}
	// Résidu hors des huit mots : refusé.
	b := StopAfterOpenRequest(open)
	b[FIBHeaderSize+40] = 1
	if _, err := l.Check(allowlist.Request{Ioctl: IoctlSendFIB, Buf: b}); !errors.Is(err, allowlist.ErrDenied) {
		t.Fatalf("résidu accepté : %v", err)
	}
}

// Bit 13 de FeatureBits et parcours 0xc8 (formes 2 et 4, CDC section 6.11),
// liste blanche : ouvertures et comptage avec le bit de poids fort admis
// pour leurs seules tailles qualifiées ; morceau du parcours 0xc8 admis
// avec n ≥ 1, marque 0xc8, CT_OK, total et taille demandée bornés ;
// chacune de leurs variantes refusée. WithSizeHighBit ne touche que le
// mot 7 ; RequestedSize l'ôte.
func TestHighBitAndInlineForms(t *testing.T) {
	l := mustList(t)
	check := func(b []byte) bool {
		_, err := l.Check(allowlist.Request{Ioctl: IoctlSendFIB, Buf: b})
		if err != nil && !errors.Is(err, allowlist.ErrDenied) {
			t.Fatal(err)
		}
		return err == nil
	}
	le := binary.LittleEndian
	base, _ := OpenRequest(ReadDiskList, 0, 0)
	high := WithSizeHighBit(base)
	if le.Uint32(high[FIBHeaderSize+28:]) != 0x80001004 || RequestedSize(high) != 0x1004 || le.Uint32(base[FIBHeaderSize+28:]) != 0x1004 {
		t.Fatalf("mot 7 : %#x (original %#x)", le.Uint32(high[FIBHeaderSize+28:]), le.Uint32(base[FIBHeaderSize+28:]))
	}
	high[FIBHeaderSize+28] = base[FIBHeaderSize+28]
	if !bytes.Equal(high[:FIBHeaderSize+28], base[:FIBHeaderSize+28]) || !bytes.Equal(high[FIBHeaderSize+32:], base[FIBHeaderSize+32:]) {
		t.Fatal("WithSizeHighBit modifie autre chose que le mot 7")
	}
	for _, mk := range []func() ([]byte, error){
		func() ([]byte, error) { return OpenRequest(ReadContainerList, 0, ContainerListSize(256)) },
		func() ([]byte, error) { return OpenRequest(ReadContainerInfo, 0xfd, 0) },
		func() ([]byte, error) { return OpenRequest(ReadDiskList, 0, 0) },
		func() ([]byte, error) { return OpenRequest(ReadDiskInfo, 7, 0) },
		func() ([]byte, error) { return OpenRequest(ReadMemberList, 0x1d000000, MemberListSize(4)) },
		func() ([]byte, error) { return ContainerCountRequest(), nil },
	} {
		b, _ := mk()
		if !check(WithSizeHighBit(b)) {
			t.Fatalf("forme du bit 13 refusée : %x", b[FIBHeaderSize:FIBHeaderSize+32])
		}
	}
	wrong := WithSizeHighBit(base)
	le.PutUint32(wrong[FIBHeaderSize+28:], 0x80001000) // 247 : taille 0x1004 seulement
	if check(wrong) {
		t.Fatal("ouverture 247 de taille 0x1000 avec le bit de poids fort acceptée")
	}
	count := ContainerCountRequest()
	le.PutUint32(count[FIBHeaderSize+28:], 0x80000001)
	if check(count) {
		t.Fatal("comptage avec une taille non nulle acceptée")
	}
	ok := InlineChunkRequest(0x1000, 0x2061e, 1, 0x1000)
	if !check(ok) || !check(InlineChunkRequest(0x1004, 0x2061e, 0x1003, 0x1004)) {
		t.Fatal("morceau du parcours 0xc8 refusé")
	}
	for name, mut := range map[string]func([]byte){
		"morceau 0":                              func(b []byte) { le.PutUint32(b[FIBHeaderSize+24:], 0) },
		"marque 0xc9":                            func(b []byte) { le.PutUint32(b[FIBHeaderSize+8:], 0xc9) },
		"code 0xdb":                              func(b []byte) { le.PutUint32(b[FIBHeaderSize+16:], 0xdb) },
		"sans transfert":                         func(b []byte) { le.PutUint32(b[FIBHeaderSize+20:], XferNone) },
		"total nul":                              func(b []byte) { le.PutUint32(b[FIBHeaderSize+12:], 0) },
		"total au-delà":                          func(b []byte) { le.PutUint32(b[FIBHeaderSize+12:], 0x1005) },
		"numéro au-delà du total":                func(b []byte) { le.PutUint32(b[FIBHeaderSize+24:], 0x1000) },
		"taille demandée nulle":                  func(b []byte) { le.PutUint32(b[FIBHeaderSize+28:], SizeHighBit) },
		"taille demandée au-delà":                func(b []byte) { le.PutUint32(b[FIBHeaderSize+28:], SizeHighBit|0x1005) },
		"taille demandée d'un octet":             func(b []byte) { le.PutUint32(b[FIBHeaderSize+28:], SizeHighBit|1) },
		"taille demandée non qualifiée":          func(b []byte) { le.PutUint32(b[FIBHeaderSize+28:], SizeHighBit|0x0ffd) },
		"numéro au-delà du total (bien au-delà)": func(b []byte) { le.PutUint32(b[FIBHeaderSize+24:], 0x2000) },
		"résidu":                                 func(b []byte) { b[FIBHeaderSize+40] = 1 },
		"clôture avec le bit":                    func(b []byte) { le.PutUint32(b[FIBHeaderSize+4:], 84) },
	} {
		b := append([]byte(nil), ok...)
		mut(b)
		if check(b) {
			t.Errorf("%s : accepté", name)
		}
	}
	// Pas du parcours 0xc8 : min(mot 6, SenderSize − 64), comme l'outil ;
	// refusé si nul, si SenderSize ≤ 64 ou au-delà de ChunkData.
	for _, tc := range []struct {
		word6, sender, step uint32
		ok                  bool
	}{
		{432, 512, 432, true}, {1, 512, 1, true}, {500, 400, 336, true}, {432, 256, 192, true},
		{0, 512, 0, false}, {433, 512, 0, false}, {432, 64, 0, false}, {432, 0, 0, false}, {600, 600, 0, false},
	} {
		step, ok := (PagedResponse{Chunk: tc.word6, SenderSize: tc.sender}).InlineStep()
		if ok != tc.ok || (ok && step != tc.step) {
			t.Errorf("mot 6 %d, SenderSize %d : pas %d %v, attendu %d %v", tc.word6, tc.sender, step, ok, tc.step, tc.ok)
		}
	}
}

// Statut non nul ou mauvais écho : échec d'échange (ErrExchange), examinés
// avant le reste de l'en-tête, qui ne doit pas les masquer ; un en-tête
// inattendu seul n'en est pas un.
func TestDecodePagedResponseExchange(t *testing.T) {
	le := binary.LittleEndian
	req := ChunkRequest(0x1000, 9, 0)
	rsp := func(f func([]byte)) []byte {
		b := append([]byte(nil), req...)
		le.PutUint16(b[fibSize:], ChunkRspSize)
		le.PutUint32(b[FIBHeaderSize:], 0)
		f(b)
		return b
	}
	for name, tc := range map[string]struct {
		f    func([]byte)
		want bool
	}{
		"statut":             {func(b []byte) { le.PutUint32(b[FIBHeaderSize:], 1) }, true},
		"écho":               {func(b []byte) { le.PutUint32(b[FIBHeaderSize+4:], 84) }, true},
		"commande et statut": {func(b []byte) { le.PutUint16(b[fibCommand:], 700); le.PutUint32(b[FIBHeaderSize:], 1) }, true},
		"taille et écho":     {func(b []byte) { le.PutUint16(b[fibSize:], 100); le.PutUint32(b[FIBHeaderSize+4:], 84) }, true},
		"commande seule":     {func(b []byte) { le.PutUint16(b[fibCommand:], 700) }, false},
		"taille seule":       {func(b []byte) { le.PutUint16(b[fibSize:], 100) }, false},
	} {
		_, err := DecodePagedResponse(req, rsp(tc.f))
		if err == nil || errors.Is(err, ErrExchange) != tc.want {
			t.Errorf("%s : %v (échec d'échange attendu %v)", name, err, tc.want)
		}
	}
}
