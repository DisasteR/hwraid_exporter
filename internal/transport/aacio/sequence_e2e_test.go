// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacio_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/DisasteR/hwraid_exporter/internal/aacraid"
	"github.com/DisasteR/hwraid_exporter/internal/modules/aacmod"
	"github.com/DisasteR/hwraid_exporter/internal/transport/aacio"
)

// Séquence de référence de la v0.2.0, enregistrée sur le transport avant
// toute modification ; elle n'est jamais réécrite : les évolutions voulues
// sont décrites par expectedSequence.
const sequenceGolden = "testdata/sequence-6805.txt"

// cardFirmware simule la carte de référence (Adaptec 6805, 5.3-0[19204])
// sur ses réponses nominales : 706, 703, 803 et protection du cache
// construites sur les champs décodés ; lectures paginées servies depuis les
// données de référence pseudonymisées (internal/aacraid/testdata), comme le
// firmware (marques 0xc9, 0xc8, 0xc6, 432 octets par morceau). Chaque ioctl
// est journalisé avec la requête reçue, octet par octet.
type cardFirmware struct {
	t    *testing.T
	log  []string
	xfer uint32
	// maxContainers : réponse au comptage (0 : 256, carte de référence).
	maxContainers uint32
	// Variantes des capacités : bits ajoutés à FeatureBits (706) et à
	// SupportedOptions (703), 703 en échec, contenus de transfert remplacés
	// (clé « sous-commande-paramètre »).
	extraFeatureBits, extraOptions uint32
	clearFeatureBits               uint32
	fail703                        bool
	payloads                       map[string][]byte
	// transfert en cours
	payload []byte
}

func (f *cardFirmware) fixture(name string) []byte {
	f.t.Helper()
	s, err := os.ReadFile("../../aacraid/testdata/" + name + ".hex")
	if err != nil {
		f.t.Fatalf("donnée de référence %s : %v", name, err)
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(s)))
	if err != nil {
		f.t.Fatalf("donnée de référence %s : %v", name, err)
	}
	return b
}

func (f *cardFirmware) ioctl(req uint32, buf []byte) error {
	f.log = append(f.log, fmt.Sprintf("%#x %s", req, hex.EncodeToString(buf)))
	le := binary.LittleEndian
	switch req {
	case aacraid.IoctlGetHBAInfo:
		buf[50], buf[51], buf[52] = 0, 1, 0
		for off, v := range map[int]uint32{60: 0x9005, 64: 0x028b, 68: 0x9005, 72: 0x0301} {
			le.PutUint32(buf[off:], v)
		}
		return nil
	case aacraid.IoctlSendLargeFIB:
		copy(buf, f.fixture("708"))
		return nil
	case aacraid.IoctlSendFIB:
	default:
		f.t.Fatalf("ioctl inattendu %#x", req)
	}
	d := buf[aacraid.FIBHeaderSize:]
	switch cmd := le.Uint16(buf[4:]); cmd {
	case aacraid.CmdRequestSupplementAdapterInfo:
		le.PutUint16(buf[8:], aacraid.FIBSize)
		le.PutUint32(d[0x20:], 15)                                                // Version
		le.PutUint32(d[0x24:], 0x0fff57df&^f.clearFeatureBits|f.extraFeatureBits) // FeatureBits capturé : bits 8 et 9, sans bit 13
		le.PutUint32(d[0x90:], 0x00c8693c)                                        // SupportedOptions2 : lectures ARCIO
	case aacraid.CmdRequestAdapterInfo:
		if f.fail703 {
			return unix.EIO
		}
		le.PutUint16(buf[8:], aacraid.FIBSize)
		le.PutUint32(d[80:], 0x141f1d7c|f.extraOptions) // SupportedOptions capturé : capteur, sans bit 15
	case aacraid.CmdAdapterEnvironmentInfo:
		le.PutUint16(buf[8:], aacraid.FIBSize)
		le.PutUint32(d[0:], 0)
		le.PutUint16(d[36:], 48)
		le.PutUint16(d[40:], 95)
		le.PutUint16(d[42:], 100)
	case 500: // ContainerCommand
		switch le.Uint32(d[0:]) {
		case 3: // VM_Ioctl : protection du cache (ZMM installé, prêt)
			le.PutUint16(buf[8:], aacraid.FIBSize)
			le.PutUint32(d[0:], 0)
			switch le.Uint32(d[16:]) {
			case 0x30000:
				le.PutUint16(d[42:], 0x2000)
			case 0x700000:
				le.PutUint32(d[20:], 1)
				le.PutUint32(d[48:], 0x40000089)
			}
		case 2: // VM_ContainerConfig
			if le.Uint32(d[4:]) == 4 { // CT_GET_CONTAINER_COUNT, réponse capturée
				le.PutUint16(buf[8:], aacraid.ChunkRspSize)
				n := f.maxContainers
				if n == 0 {
					n = 256
				}
				for i, w := range []uint32{0, 4, n, 0x40, 0x201, 0xffffffff} {
					le.PutUint32(d[4*i:], w)
				}
				return nil
			}
			f.paged(buf)
		default:
			f.t.Fatalf("ContainerCommand inattendue : %x", d[:8])
		}
	default:
		f.t.Fatalf("commande FIB inattendue %d", cmd)
	}
	return nil
}

func (f *cardFirmware) paged(buf []byte) {
	le := binary.LittleEndian
	d := buf[aacraid.FIBHeaderSize:]
	le.PutUint16(buf[8:], aacraid.ChunkRspSize)
	sub := le.Uint32(d[4:])
	switch sub {
	case 83: // morceau
		n := le.Uint32(d[24:])
		le.PutUint32(d[0:], 0)
		le.PutUint32(d[8:], aacraid.MarkChunk)
		chunk := make([]byte, aacraid.ChunkData)
		if off := int(n) * aacraid.ChunkData; off < len(f.payload) {
			copy(chunk, f.payload[off:])
		}
		copy(d[32:], chunk)
	case 84: // clôture
		le.PutUint32(d[0:], 0)
		le.PutUint32(d[8:], 0xc6)
	default: // ouverture
		name := fmt.Sprintf("%d-%x", sub, le.Uint32(d[8:]))
		if p, ok := f.payloads[name]; ok {
			f.payload = p
		} else {
			f.payload = f.fixture(name)
		}
		f.xfer++
		total := uint32(len(f.payload))
		le.PutUint32(d[0:], 0)
		le.PutUint32(d[8:], 0xc9)
		le.PutUint32(d[12:], total)
		le.PutUint32(d[16:], aacraid.CodeOK)
		le.PutUint32(d[20:], 0x00060200+f.xfer)
		le.PutUint32(d[24:], 0)
		le.PutUint32(d[28:], total)
	}
}

// containerCountRequest est la requête CT_GET_CONTAINER_COUNT de l'outil de
// référence (capture de la carte de référence, variante aux résidus nuls),
// écrite ici telle quelle plutôt que construite par le code testé.
func containerCountRequest() string {
	b := make([]byte, aacraid.FIBSize)
	h, _ := hex.DecodeString("01001000f4010100e0010002")
	w, _ := hex.DecodeString("0200000004000000000000000000000000000000ffffffff0000000000000000")
	copy(b, h)
	copy(b[aacraid.FIBHeaderSize:], w)
	return fmt.Sprintf("%#x %s", aacraid.IoctlSendFIB, hex.EncodeToString(b))
}

// expectedSequence est la séquence de référence de la v0.2.0
// (testdata/sequence-6805.txt, enregistrée sur le transport avant toute
// modification) avec les seules évolutions attendues depuis : la requête
// CT_GET_CONTAINER_COUNT avant la première ouverture de la 229 du premier
// cycle (réponse gardée au second).
func expectedSequence(t *testing.T, golden []byte) []byte {
	t.Helper()
	lines := strings.SplitAfter(string(golden), "\n")
	// Ouverture de la 229 : en-tête des requêtes paginées, puis [2, 229, …].
	open229 := fmt.Sprintf("%#x 01001000f4010100e0010002%s02000000e5000000", aacraid.IoctlSendFIB, strings.Repeat("00", aacraid.FIBHeaderSize-12))
	for i, l := range lines {
		if strings.HasPrefix(l, open229) {
			out := append(append(append([]string(nil), lines[:i]...), containerCountRequest()+"\n"), lines[i:]...)
			return []byte(strings.Join(out, ""))
		}
	}
	t.Fatal("ouverture de la 229 absente de la séquence de référence")
	return nil
}

// Séquence de référence de la carte qualifiée (CDC, section 6.11,
// « Non-régression ») : toutes les requêtes émises, octet par octet, sur
// deux cycles du même module (premier cycle : 706, 703, 708 et comptage
// lus ; second : réponses gardées). Toute modification du transport ou du
// module qui change un octet, l'ordre ou le nombre des commandes envoyées à
// la carte de référence fait échouer ce test, hors des évolutions attendues
// que décrit expectedSequence.
func TestReferenceCardSequence(t *testing.T) {
	list, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	fw := &cardFirmware{t: t}
	aacio.ReplaceIoctl(t, fw.ioctl)
	c, refs := e2eCard()
	// Identité complète (numéro de série fictif) : le second cycle réutilise
	// les réponses gardées (706, 708).
	c.Hosts[0].SerialNumber, c.Hosts[0].Model = "TEST0001", "ASR6805"
	m := aacmod.New(list)
	var got bytes.Buffer
	for cycle := 1; cycle <= 2; cycle++ {
		fw.log = nil
		r := m.Collect(context.Background(), c, refs)
		if r.Err != nil || r.CollectErr != nil || !r.NodeValid {
			t.Fatalf("cycle %d : collecte en échec : %v / %v", cycle, r.Err, r.CollectErr)
		}
		for col, ok := range r.Collected {
			if !ok {
				t.Fatalf("cycle %d : collecteur %s en échec", cycle, col)
			}
		}
		fmt.Fprintf(&got, "# cycle %d\n", cycle)
		for _, l := range fw.log {
			got.WriteString(l + "\n")
		}
	}
	golden, err := os.ReadFile(sequenceGolden)
	if err != nil {
		t.Fatal(err)
	}
	want := expectedSequence(t, golden)
	if !bytes.Equal(got.Bytes(), want) {
		gl, wl := strings.Split(got.String(), "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gl) || i < len(wl); i++ {
			var g, w string
			if i < len(gl) {
				g = gl[i]
			}
			if i < len(wl) {
				w = wl[i]
			}
			if g != w {
				t.Fatalf("séquence différente de %s à la ligne %d :\nobtenu : %.160s\nattendu : %.160s", sequenceGolden, i+1, g, w)
			}
		}
	}
}

// Carte qui annonce 64 conteneurs : la liste des conteneurs est demandée à
// 8 + 4 × 64 = 0x108 octets (valeur littérale, au niveau de l'ioctl), comme
// le fait l'outil de référence.
func TestContainerListSizeFromCount(t *testing.T) {
	list, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	fw := &cardFirmware{t: t, maxContainers: 64}
	aacio.ReplaceIoctl(t, fw.ioctl)
	c, refs := e2eCard()
	c.Hosts[0].SerialNumber, c.Hosts[0].Model = "TEST0001", "ASR6805"
	r := aacmod.New(list).Collect(context.Background(), c, refs)
	if r.CollectErr != nil || !r.Collected["volumes"] {
		t.Fatalf("collecte en échec : %v", r.CollectErr)
	}
	open229 := fmt.Sprintf("%#x 01001000f4010100e0010002%s02000000e50000000000000000000000da000000ffffffff0000000008010000", aacraid.IoctlSendFIB, strings.Repeat("00", aacraid.FIBHeaderSize-12))
	for _, l := range fw.log {
		if strings.HasPrefix(l, open229) {
			return
		}
	}
	t.Fatal("aucune ouverture de la liste des conteneurs à 0x108 octets")
}

// containerConfigSent rend, pour chaque requête VM_ContainerConfig émise,
// sa sous-commande (mot 1) et son mot 6 (numéro de morceau), et indique si
// une 708 est partie.
func containerConfigSent(t *testing.T, log []string) (subs [][2]uint32, connectorMap bool) {
	t.Helper()
	le := binary.LittleEndian
	for _, l := range log {
		req, h, _ := strings.Cut(l, " ")
		b, err := hex.DecodeString(h)
		if err != nil {
			t.Fatal(err)
		}
		if req == fmt.Sprintf("%#x", aacraid.IoctlSendLargeFIB) {
			connectorMap = true
		}
		if req != fmt.Sprintf("%#x", aacraid.IoctlSendFIB) || le.Uint16(b[4:]) != 500 {
			continue
		}
		d := b[aacraid.FIBHeaderSize:]
		if le.Uint32(d[0:]) == 2 {
			subs = append(subs, [2]uint32{le.Uint32(d[4:]), le.Uint32(d[24:])})
		}
	}
	return subs, connectorMap
}

// Refus par capacité (CDC, section 6.11, prérequis), vérifiés sur les ioctl
// effectivement émis par le module et le transport réels : 703 en échec,
// bit 13 de FeatureBits ou bit 15 de SupportedOptions : aucune commande
// VM_ContainerConfig (comptage, transferts) ni 708 n'atteint le noyau.
func TestCapabilityRefusalsSendNothing(t *testing.T) {
	list, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	for name, fw := range map[string]*cardFirmware{
		"703 en échec":               {fail703: true},
		"bit 13 de FeatureBits":      {extraFeatureBits: aacraid.FeaturePagedHighBit},
		"bit 15 de SupportedOptions": {extraOptions: aacraid.SupportedLargeTransfers},
	} {
		fw.t = t
		aacio.ReplaceIoctl(t, fw.ioctl)
		c, refs := e2eCard()
		c.Hosts[0].SerialNumber, c.Hosts[0].Model = "TEST0001", "ASR6805"
		r := aacmod.New(list).Collect(context.Background(), c, refs)
		subs, cm := containerConfigSent(t, fw.log)
		if len(subs) != 0 || cm {
			t.Errorf("%s : VM_ContainerConfig %v, 708 %v", name, subs, cm)
		}
		if len(fw.log) == 0 || r.Collected["volumes"] {
			t.Errorf("%s : %d ioctl, collecteurs %v", name, len(fw.log), r.Collected)
		}
	}
}

// Liste des conteneurs annonçant plus d'entrées que MaxContainers : seul le
// premier morceau est demandé (ioctl émis), puis le transfert est clos.
func TestOversizedContainerListStopsAfterFirstChunk(t *testing.T) {
	list, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 8+4*300)
	binary.LittleEndian.PutUint32(big[4:], 300)
	fw := &cardFirmware{t: t, payloads: map[string][]byte{"229-0": big}}
	aacio.ReplaceIoctl(t, fw.ioctl)
	c, refs := e2eCard()
	c.Hosts[0].SerialNumber, c.Hosts[0].Model = "TEST0001", "ASR6805"
	r := aacmod.New(list).Collect(context.Background(), c, refs)
	if r.Collected["volumes"] {
		t.Fatal("liste de 300 entrées acceptée pour MaxContainers 256")
	}
	subs, _ := containerConfigSent(t, fw.log)
	var after229 [][2]uint32
	for i, s := range subs {
		if s[0] == 229 {
			after229 = subs[i : i+3]
			break
		}
	}
	// Ouverture et morceau 0 seulement pour la 229. La requête qui suit est
	// aujourd'hui une clôture (le transport clôture après toute erreur de
	// morceau) ; le CDC prévoit l'abandon sans clôture avec le point 19 : ce
	// test n'en fait pas un attendu, il exige seulement qu'aucun autre
	// morceau de ce transfert ne soit demandé.
	if len(after229) != 3 || after229[1] != [2]uint32{83, 0} || after229[2][0] == 83 {
		t.Fatalf("requêtes après l'ouverture de la 229 : %v", subs)
	}
	// Aucune autre lecture de la carte de référence ne dépasse un morceau
	// (arrêt anticipé) : aucun morceau 1 ne doit apparaître.
	for _, s := range subs {
		if s == ([2]uint32{83, 1}) {
			t.Fatalf("morceau 1 demandé : %v", subs)
		}
	}
}

// Sans bit 9 de FeatureBits : ni comptage ni lecture de conteneurs ou de
// membres, chaîne des disques lue ; sans bit 8 : pas de 708. Vérifié sur les
// ioctl émis.
func TestFeatureBitsGateReads(t *testing.T) {
	list, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		clear uint32
		cm    bool
	}{
		{"sans bit 9", aacraid.FeatureContainerReads, true},
		{"sans bits 8 et 9", aacraid.FeatureContainerReads | aacraid.FeatureConnectorMap, false},
		{"sans bit 8", aacraid.FeatureConnectorMap, false},
	} {
		fw := &cardFirmware{t: t, clearFeatureBits: tc.clear}
		aacio.ReplaceIoctl(t, fw.ioctl)
		c, refs := e2eCard()
		c.Hosts[0].SerialNumber, c.Hosts[0].Model = "TEST0001", "ASR6805"
		r := aacmod.New(list).Collect(context.Background(), c, refs)
		subs, cm := containerConfigSent(t, fw.log)
		seen := map[uint32]bool{}
		for _, s := range subs {
			seen[s[0]] = true
		}
		containers := tc.clear&aacraid.FeatureContainerReads == 0
		if seen[4] != containers || seen[229] != containers || seen[234] != containers || seen[249] != containers ||
			!seen[247] || !seen[248] || cm != tc.cm {
			t.Errorf("%s : sous-commandes %v, 708 %v", tc.name, subs, cm)
		}
		if r.CollectErr != nil || !r.Collected[aacmod.DiskInventoryCollector] {
			t.Errorf("%s : %v, %v", tc.name, r.CollectErr, r.Collected)
		}
	}
}

// Verrou de qualification (exigence S4), vérifié sur les ioctl émis : une
// carte d'un autre firmware, qui annonce pourtant toutes les lectures, ne
// reçoit que GET_HBA_INFO puis la 706.
func TestUnqualifiedCardReceivesOnly706(t *testing.T) {
	list, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	fw := &cardFirmware{t: t}
	aacio.ReplaceIoctl(t, fw.ioctl)
	c, refs := e2eCard()
	c.Hosts[0].SerialNumber, c.Hosts[0].Model = "TEST0001", "ASR6805"
	c.Hosts[0].Firmware = "5.2-0[19109]"
	m := aacmod.New(list)
	for cycle := 1; cycle <= 2; cycle++ {
		fw.log = nil
		r := m.Collect(context.Background(), c, refs)
		if !r.NodeValid || len(r.Unsupported) == 0 {
			t.Fatalf("cycle %d : %+v", cycle, r)
		}
		var cmds []string
		for _, l := range fw.log {
			req, h, _ := strings.Cut(l, " ")
			b, _ := hex.DecodeString(h)
			if req == fmt.Sprintf("%#x", aacraid.IoctlSendFIB) {
				req += fmt.Sprintf("/%d", binary.LittleEndian.Uint16(b[4:]))
			}
			cmds = append(cmds, req)
		}
		want := fmt.Sprintf("%#x %#x/706", aacraid.IoctlGetHBAInfo, aacraid.IoctlSendFIB)
		if cycle == 2 {
			want = fmt.Sprintf("%#x", aacraid.IoctlGetHBAInfo) // 706 gardée
		}
		if got := strings.Join(cmds, " "); got != want {
			t.Fatalf("cycle %d : ioctl %s, attendu %s", cycle, got, want)
		}
	}
}
