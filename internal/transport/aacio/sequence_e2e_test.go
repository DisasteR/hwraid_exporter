// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacio_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/DisasteR/hwraid_exporter/internal/module"
	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
	"os"
	"slices"
	"sort"
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
	clearOptions                   uint32 // bits retirés de SupportedOptions (703)
	clearFeatureBits               uint32
	fail703                        bool
	payloads                       map[string][]byte
	// fail706 : la 706 échoue (EIO).
	fail706 bool
	// Alarme (CT_ALARM_GET_STATE) : alarmByte, premier octet de
	// _ALARM_INFO (0 : désactivée, réponse capturée) ; alarmStatus, statut
	// rendu ; alarmEIO, ioctl en échec.
	alarmByte   byte
	alarmStatus uint32
	alarmEIO    bool
	alarmWord2  uint32 // mot 2 de la réponse (0 : 1, valeur capturée)
	// Ouverture : paused, carte en pause (704) ; revBuild, build annoncé par
	// la 701 (0 : 19204, carte de référence) ; noSupplement, 703 sans le
	// bit 16 (706 non annoncée).
	paused       bool
	revBuild     uint32
	noSupplement bool
	// jobs : tâches de fond par conteneur (101), rapports rendus par la
	// 103 ; sans entrée, réponse capturée sans tâche.
	jobs map[uint32][][]byte
	// battery : batterie classique (réponse 0x30000 sans 0x2000, indicateurs
	// 0x17, octets 20 à 22) ; nil : module ZMM de la carte de référence.
	battery *[3]byte
	// subsystem : identifiant de sous-système annoncé par GET_HBA_INFO
	// (0 : 0x0301, carte de référence).
	subsystem uint32
	// totals : taille annoncée à chaque ouverture, dans l'ordre.
	totals []uint32
	// pairs : requête et réponse de chaque VM_ContainerConfig de
	// transfert (comptage exclu), dans l'ordre.
	pairs [][2][]byte
	// transfert en cours : contenu, taille et identifiant tenus par le faux
	// firmware, indépendamment des requêtes reçues
	payload        []byte
	curTotal, curX uint32
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
		sub := f.subsystem
		if sub == 0 {
			sub = 0x0301
		}
		for off, v := range map[int]uint32{60: 0x9005, 64: 0x028b, 68: 0x9005, 72: sub} {
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
	case aacraid.CmdIsAdapterPaused: // réponse capturée : 4 octets de données
		le.PutUint32(buf[0:], 0)
		le.PutUint16(buf[8:], aacraid.FIBHeaderSize+4)
		if f.paused {
			d[0] = 1
		}
	case aacraid.CmdCheckRevision: // réponse capturée
		le.PutUint32(buf[0:], 0)
		le.PutUint16(buf[8:], aacraid.FIBHeaderSize+12)
		build := f.revBuild
		if build == 0 {
			build = 19204
		}
		for i, w := range []uint32{1, 0x05030400, build, 0xffffffff} {
			le.PutUint32(d[4*i:], w)
		}
	case aacraid.CmdAifRequest: // réponse capturée : deux mots (0, 1), puis les blocs
		sub, arg := le.Uint32(d[0:]), le.Uint32(d[8:])
		var blocks [][]byte
		switch sub {
		case aacraid.AifReqJobsForCtr:
			blocks = f.jobs[arg]
		case aacraid.AifReqJobReport:
			for _, bs := range f.jobs {
				for _, b := range bs {
					if le.Uint32(b) == arg {
						blocks = [][]byte{b}
					}
				}
			}
		default:
			f.t.Fatalf("sous-commande AifRequest inattendue %d", sub)
		}
		le.PutUint32(buf[0:], 0)
		for i := range d[:aacraid.FIBSize-aacraid.FIBHeaderSize] {
			d[i] = 0
		}
		le.PutUint32(d[4:], 1)
		for i, b := range blocks {
			copy(d[8+48*i:], b)
		}
		le.PutUint16(buf[8:], uint16(aacraid.FIBHeaderSize+8+48*len(blocks)))
	case aacraid.CmdRequestSupplementAdapterInfo:
		if f.fail706 {
			return unix.EIO
		}
		le.PutUint16(buf[8:], aacraid.FIBSize)
		le.PutUint32(d[0x20:], 15)                                                // Version
		le.PutUint32(d[0x24:], 0x0fff57df&^f.clearFeatureBits|f.extraFeatureBits) // FeatureBits capturé : bits 8 et 9, sans bit 13
		le.PutUint32(d[0x90:], 0x00c8693c)                                        // SupportedOptions2 : lectures ARCIO
	case aacraid.CmdRequestAdapterInfo:
		if f.fail703 {
			return unix.EIO
		}
		le.PutUint16(buf[8:], aacraid.FIBSize)
		opts := 0x141f1d7c&^f.clearOptions | f.extraOptions // SupportedOptions capturé : capteur, bit 16, alarme (bit 11), sans bit 15
		if f.noSupplement {
			opts = 0x141e1d7c | f.extraOptions // valeur capturée sans le bit 16 (0x10000), écrite en clair
		}
		le.PutUint32(d[80:], opts)
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
				if f.battery != nil {
					le.PutUint16(d[42:], 0x17)
					copy(d[20:23], f.battery[:])
				}
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
			if le.Uint32(d[4:]) == 190 { // CT_ALARM_GET_STATE, réponse capturée
				if f.alarmEIO {
					return unix.EIO
				}
				le.PutUint16(buf[8:], aacraid.ChunkRspSize)
				w2 := f.alarmWord2
				if w2 == 0 {
					w2 = 1
				}
				for i, w := range []uint32{f.alarmStatus, 190, w2, 0, 0, 0xffffffff, 0, 8} {
					le.PutUint32(d[4*i:], w)
				}
				d[0x20] = f.alarmByte
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
	req := append([]byte(nil), buf...)
	defer func() { f.pairs = append(f.pairs, [2][]byte{req, append([]byte(nil), buf...)}) }()
	le := binary.LittleEndian
	d := buf[aacraid.FIBHeaderSize:]
	le.PutUint16(buf[8:], aacraid.ChunkRspSize)
	sub := le.Uint32(d[4:])
	switch sub {
	case 83: // morceau : mots de réponse écrits depuis l'état du transfert
		n := le.Uint32(d[24:])
		le.PutUint32(d[0:], 0)
		le.PutUint32(d[8:], aacraid.MarkChunk)
		for off, v := range map[int]uint32{12: f.curTotal, 16: aacraid.CodeOK, 20: f.curX, 24: n, 28: f.curTotal} {
			le.PutUint32(d[off:], v)
		}
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
		f.totals = append(f.totals, total)
		f.curTotal, f.curX = total, 0x00060200+f.xfer
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
// modification) avec les seules évolutions attendues depuis : l'ouverture
// de la carte comme l'outil (704 à chaque cycle, puis au premier cycle 701,
// et la 703 avant la 706) ; la protection du cache (0x30000 puis 0x700000)
// lue après la 703 et la 803 de chaque cycle, et non plus juste après la
// 706 ; la requête CT_GET_CONTAINER_COUNT avant la première ouverture de la
// 229 du premier cycle (réponse gardée au second) ; à la fin de chaque
// cycle, la liste des tâches (101) de chaque conteneur lu ; dans chaque
// cycle, CT_ALARM_GET_STATE (190) juste avant la protection du cache.
func expectedSequence(t *testing.T, golden []byte) []byte {
	t.Helper()
	lines := appendTaskLists(t, openAsTool(t, moveCacheAfter803(t, strings.SplitAfter(string(golden), "\n"))))
	// Ouverture de la 229 : en-tête des requêtes paginées, puis [2, 229, …].
	open229 := fmt.Sprintf("%#x 01001000f4010100e0010002%s02000000e5000000", aacraid.IoctlSendFIB, strings.Repeat("00", aacraid.FIBHeaderSize-12))
	for i, l := range lines {
		if strings.HasPrefix(l, open229) {
			out := append(append(append([]string(nil), lines[:i]...), containerCountRequest()+"\n"), lines[i:]...)
			return []byte(strings.Join(insertAlarm(t, reorderAsTool(t, out)), ""))
		}
	}
	t.Fatal("ouverture de la 229 absente de la séquence de référence")
	return nil
}

// reorderAsTool range, dans chaque cycle, les lectures de configuration
// dans l'ordre de l'outil de référence (captures fibcap-AL et
// fibcap-STATUS ; CDC, section 6.11) : comptage, 708, 247, les 248 dans
// l'ordre de la liste rendue par la carte de référence (3, 4, 5, 7, 6, 1, 2,
// 0), 229, les 234, les 249, puis la protection du cache ; les tâches (101)
// restent à la fin. Une lecture paginée (ouverture, morceaux, clôture) est
// déplacée d'un bloc ; les identifiants de transfert, que le faux firmware
// attribue dans l'ordre des ouvertures, sont réattribués dans le nouvel
// ordre. Aucune requête n'est ajoutée ni retirée (vérifié ici).
func reorderAsTool(t *testing.T, lines []string) []string {
	t.Helper()
	le := binary.LittleEndian
	type block struct {
		label string
		lines []string
	}
	decode := func(l string) (string, []byte) {
		ioc, h, _ := strings.Cut(strings.TrimSuffix(l, "\n"), " ")
		b, _ := hex.DecodeString(h)
		return ioc, b
	}
	labelOf := func(l string) string {
		ioc, b := decode(l)
		switch {
		case ioc == fmt.Sprintf("%#x", aacraid.IoctlSendLargeFIB):
			return "708"
		case len(b) < 64 || le.Uint16(b[4:]) != 500:
			return ""
		case le.Uint32(b[aacraid.FIBHeaderSize:]) != 2:
			return "cache"
		}
		sub := le.Uint32(b[aacraid.FIBHeaderSize+4:])
		switch sub {
		case 83, 84:
			return "chunk"
		case 4, 229, 247:
			return fmt.Sprint(sub)
		}
		return fmt.Sprintf("%d:%x", sub, le.Uint32(b[aacraid.FIBHeaderSize+8:]))
	}
	rank := func(label string) int {
		disks := map[string]int{"248:3": 0, "248:4": 1, "248:5": 2, "248:7": 3, "248:6": 4, "248:1": 5, "248:2": 6, "248:0": 7}
		switch {
		case label == "4":
			return 0
		case label == "708":
			return 1
		case label == "247":
			return 2
		case strings.HasPrefix(label, "248:"):
			return 10 + disks[label]
		case label == "229":
			return 20
		case strings.HasPrefix(label, "234:"):
			return 21
		case strings.HasPrefix(label, "249:"):
			return 22
		case label == "cache":
			return 30
		}
		t.Fatalf("lecture inattendue dans la configuration : %s", label)
		return 0
	}
	var out []string
	var oldX, region []block
	before := 0
	flush := func() {
		sort.SliceStable(region, func(i, j int) bool { return rank(region[i].label) < rank(region[j].label) })
		for _, b := range region {
			out = append(out, b.lines...)
		}
		region = nil
	}
	in := false
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		lab := labelOf(l)
		switch {
		case strings.HasPrefix(l, "# cycle") || (lab == "" && in):
			if in {
				flush()
				in = false
			}
			out = append(out, l)
			continue
		case lab == "4" || lab == "708" || lab == "cache" || strings.HasPrefix(lab, "2"):
			in = true
		}
		if !in {
			out = append(out, l)
			continue
		}
		b := block{label: lab, lines: []string{l}}
		for i+1 < len(lines) && labelOf(lines[i+1]) == "chunk" {
			i++
			b.lines = append(b.lines, lines[i])
		}
		if len(b.lines) > 1 {
			oldX = append(oldX, b)
		}
		before += len(b.lines)
		region = append(region, b)
	}
	if in {
		flush()
	}
	// Identifiants de transfert : ceux de la séquence d'origine, dans
	// l'ordre où ils ont été attribués, donnés aux lectures dans le nouvel
	// ordre.
	var ids []uint32
	for _, b := range oldX {
		_, c := decode(b.lines[1])
		ids = append(ids, le.Uint32(c[aacraid.FIBHeaderSize+20:]))
	}
	k := 0
	for i := 0; i < len(out); i++ {
		if labelOf(out[i]) != "chunk" {
			continue
		}
		id := ids[k]
		for ; i < len(out) && labelOf(out[i]) == "chunk"; i++ {
			ioc, b := decode(out[i])
			le.PutUint32(b[aacraid.FIBHeaderSize+20:], id)
			out[i] = ioc + " " + hex.EncodeToString(b) + "\n"
		}
		i--
		k++
	}
	if len(out) != len(lines) || k != len(ids) {
		t.Fatalf("réordonnancement : %d lignes pour %d, %d transferts pour %d", len(out), len(lines), k, len(ids))
	}
	return out
}

// insertAlarm place, dans chaque cycle, la requête CT_ALARM_GET_STATE de
// l'outil de référence (capture fibcap-*, séquences 25 et 481 : en-tête des
// requêtes paginées, mots [2, 190, 1, 0, 0, 0xffffffff, 0, 8]) juste avant
// la première lecture de la protection du cache, après les listes des
// membres : place du rafraîchissement de l'alarme chez l'outil.
func insertAlarm(t *testing.T, lines []string) []string {
	t.Helper()
	alarm := capturedRequest("01001000f4010100e0010002", "02000000be000000010000000000000000000000ffffffff0000000008000000")
	cache := fmt.Sprintf("%#x 01001000f40101002c000002", aacraid.IoctlSendFIB)
	var out []string
	inserted, cycles := 0, 0
	pending := false
	for _, l := range lines {
		if strings.HasPrefix(l, "# cycle") {
			if pending {
				t.Fatal("cycle sans protection du cache")
			}
			cycles++
			pending = true
		}
		if pending && strings.HasPrefix(l, cache) {
			out = append(out, alarm)
			inserted++
			pending = false
		}
		out = append(out, l)
	}
	if cycles != 2 || inserted != 2 {
		t.Fatalf("alarme insérée %d fois pour %d cycles", inserted, cycles)
	}
	return out
}

// capturedRequest écrit une requête de l'outil telle que capturée (en-tête
// sans les octets d'adresse, données significatives), le reste à zéro.
func capturedRequest(head, data string) string {
	b := make([]byte, aacraid.FIBSize)
	h, _ := hex.DecodeString(head)
	d, _ := hex.DecodeString(data)
	copy(b, h)
	copy(b[aacraid.FIBHeaderSize:], d)
	return fmt.Sprintf("%#x %s\n", aacraid.IoctlSendFIB, hex.EncodeToString(b))
}

// appendTaskLists ajoute à la fin de chaque cycle les requêtes 101 des cinq
// conteneurs de la carte de référence, dans l'ordre de sa liste (0, puis
// 0xfc à 0xff) ; en-tête et mots écrits en clair d'après la capture (octet
// 7 et mots non initialisés par l'outil mis à zéro).
func appendTaskLists(t *testing.T, lines []string) []string {
	t.Helper()
	var tl []string
	for _, c := range []string{"00000000", "fc000000", "fd000000", "fe000000", "ff000000"} {
		tl = append(tl, capturedRequest("01001000bc02010048000002", "6500000000000000"+c))
	}
	var out []string
	cycles := 0
	for i, l := range lines {
		if strings.HasPrefix(l, "# cycle") && i > 0 {
			out = append(out, tl...)
		}
		if strings.HasPrefix(l, "# cycle") {
			cycles++
		}
		out = append(out, l)
	}
	if cycles != 2 {
		t.Fatalf("%d cycles", cycles)
	}
	return append(out, tl...)
}

// openAsTool applique l'ouverture de l'outil à la séquence de la v0.2.0 :
// après GET_HBA_INFO, la 704 à chaque cycle ; au premier cycle, la 701 puis
// la 703 avant la 706 (la 701 acceptée est gardée au second).
func openAsTool(t *testing.T, lines []string) []string {
	t.Helper()
	paused := capturedRequest("01001000c002010004000002", "")
	revision := capturedRequest("01001000bd0201000c000002", "04000000000404070a510000")
	send := fmt.Sprintf("%#x ", aacraid.IoctlSendFIB)
	hba := fmt.Sprintf("%#x ", aacraid.IoctlGetHBAInfo)
	var out []string
	cycle := 0
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		switch {
		case strings.HasPrefix(l, "# cycle"):
			cycle++
			out = append(out, l)
		case strings.HasPrefix(l, hba):
			out = append(out, l, paused)
			if cycle == 1 {
				// v0.2.0 : 706 puis 703 juste après GET_HBA_INFO.
				if i+2 >= len(lines) || !strings.HasPrefix(lines[i+1], send+"01000000c2020100") || !strings.HasPrefix(lines[i+2], send+"01000000bf020100") {
					t.Fatal("cycle 1 : 706 puis 703 attendues après GET_HBA_INFO dans la séquence de référence")
				}
				out = append(out, revision, lines[i+2], lines[i+1])
				i += 2
			}
		default:
			out = append(out, l)
		}
	}
	if cycle != 2 {
		t.Fatalf("%d cycles dans la séquence de référence", cycle)
	}
	return out
}

// moveCacheAfter803 déplace, dans chaque cycle, les deux requêtes de la
// protection du cache juste après la requête 803. Préfixes écrits ici en
// clair (en-tête FIB et premier mot) plutôt que construits par le code testé.
func moveCacheAfter803(t *testing.T, lines []string) []string {
	t.Helper()
	send := fmt.Sprintf("%#x ", aacraid.IoctlSendFIB)
	cache := []string{send + "01001000f40101002c000002", send + "01001000f40101004c000002"}
	env := send + "010010002303010004000002"
	var out, held []string
	moved := 0
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "# cycle"):
			if len(held) != 0 {
				t.Fatal("protection du cache sans 803 dans le cycle précédent")
			}
			out = append(out, l)
		case len(held) < 2 && strings.HasPrefix(l, cache[len(held)]):
			held = append(held, l)
		case strings.HasPrefix(l, env):
			out = append(append(out, l), held...)
			moved += len(held)
			held = nil
		default:
			out = append(out, l)
		}
	}
	if moved != 4 || len(held) != 0 {
		t.Fatalf("protection du cache : %d requêtes déplacées, 4 attendues", moved)
	}
	return out
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
		// Cycle ordinaire (réponses gardées) : 70 commandes firmware
		// (docs/deploiement.md), GET_HBA_INFO (réponse du pilote) exclue.
		if cycle == 2 {
			n := 0
			for _, l := range fw.log {
				if !strings.HasPrefix(l, fmt.Sprintf("%#x ", aacraid.IoctlGetHBAInfo)) {
					n++
				}
			}
			if n != 70 {
				t.Fatalf("cycle ordinaire : %d commandes firmware, 70 attendues", n)
			}
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
// effectivement émis par le module et le transport réels : 703 en échec ou
// bit 15 de SupportedOptions : aucune commande VM_ContainerConfig
// (comptage, transferts) ni 708 n'atteint le noyau.
func TestCapabilityRefusalsSendNothing(t *testing.T) {
	list, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	for name, fw := range map[string]*cardFirmware{
		"703 en échec":               {fail703: true},
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
		// 703 en échec : l'ouverture s'arrête, rien après la 703.
		if fw.fail703 {
			want := []string{fmt.Sprintf("%#x", aacraid.IoctlGetHBAInfo)}
			for _, cmd := range []int{aacraid.CmdIsAdapterPaused, aacraid.CmdCheckRevision, aacraid.CmdRequestAdapterInfo} {
				want = append(want, fmt.Sprintf("%#x/%d", aacraid.IoctlSendFIB, cmd))
			}
			if got := fibCommands(t, fw.log); !slices.Equal(got, want) {
				t.Errorf("703 en échec : ioctl %v, attendu %v", got, want)
			}
		}
		// Protection du cache : jamais sans 703 valide ; lue sinon, les
		// refus par capacité ne concernant que les transferts paginés.
		if n, want := cacheProtectionSent(t, fw.log), !fw.fail703; (n != 0) != want {
			t.Errorf("%s : protection du cache envoyée %d fois", name, n)
		}
	}
}

// Liste des conteneurs annonçant plus d'entrées que MaxContainers : seul le
// premier morceau est demandé, puis le transfert est clôturé (rejet propre à
// l'exporter : l'outil, qui ne contrôle pas ce contenu, serait allé jusqu'à
// la clôture).
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
	// Rejet propre à l'exporter, clôture réussie : la carte n'est pas
	// arrêtée, la protection du cache est lue ensuite.
	if !r.Collected["cache_protection"] || errors.Is(r.CollectErr, aacraid.ErrExchange) {
		t.Fatalf("collecte arrêtée après le rejet de la 229 : %+v", r.Collected)
	}
	subs, _ := containerConfigSent(t, fw.log)
	var after229 [][2]uint32
	for i, s := range subs {
		if s[0] == 229 {
			after229 = subs[i:min(i+3, len(subs))]
			break
		}
	}
	// Ouverture, morceau 0 et clôture après le morceau 0 pour la 229 ; la
	// chaîne des volumes s'arrête là (les disques, lus avant elle comme chez
	// l'outil, ont déjà été lus) : aucune lecture de configuration ensuite.
	// Refus de l'exporter, pas échec d'échange : la carte n'est pas arrêtée,
	// l'alarme (190, hors configuration) part ensuite, après la clôture.
	if i := slices.Index(subs, [2]uint32{229, 0}); len(after229) < 3 || after229[1] != [2]uint32{83, 0} || after229[2] != [2]uint32{84, 0} || len(subs) != i+4 || subs[i+3] != [2]uint32{190, 0} {
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

// Sans bit 9 de FeatureBits : ni lecture de conteneurs ni de membres (le
// comptage part, comme à l'ouverture de l'outil), chaîne des disques lue ; sans bit 8 : pas de 708. Vérifié sur les
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
		// Comptage envoyé à l'ouverture, comme l'outil, avec ou sans bit 9.
		if !seen[4] || seen[229] != containers || seen[234] != containers || seen[249] != containers ||
			!seen[247] || !seen[248] || cm != tc.cm {
			t.Errorf("%s : sous-commandes %v, 708 %v", tc.name, subs, cm)
		}
		if r.CollectErr != nil || !r.Collected[aacmod.DiskInventoryCollector] {
			t.Errorf("%s : %v, %v", tc.name, r.CollectErr, r.Collected)
		}
	}
}

// Verrou de qualification (exigence S4), vérifié sur les ioctl émis : une
// carte d'un modèle non qualifié et non désignée, qui annonce pourtant
// toutes les lectures, ne reçoit que GET_HBA_INFO (réponse du pilote).
func TestUnqualifiedCardReceivesNoFirmwareCommand(t *testing.T) {
	list, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	fw := &cardFirmware{t: t}
	aacio.ReplaceIoctl(t, fw.ioctl)
	c, refs := e2eCard()
	c.Hosts[0].SerialNumber, c.Hosts[0].Model = "TEST0001", "ASR6805"
	// Modèle non qualifié (sous-système), firmware de la carte de
	// référence : le firmware seul ne qualifie pas.
	c.PCI.SubsystemDevice, fw.subsystem = "0x0300", 0x0300
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
		want := fmt.Sprintf("%#x", aacraid.IoctlGetHBAInfo) // réponse du pilote, aucune FIB
		{
		}
		if got := strings.Join(cmds, " "); got != want {
			t.Fatalf("cycle %d : ioctl %s, attendu %s", cycle, got, want)
		}
	}
}

// toolSequence vérifie que chaque transfert paginé suit la suite de
// commandes de l'outil de référence : ouverture, morceaux 0 à n-1 sans
// interruption (n : taille annoncée divisée par 432, valeur littérale,
// arrondie au-dessus), puis clôture. Rend le nombre de transferts.
func toolSequence(t *testing.T, subs [][2]uint32, totals []uint32) int {
	t.Helper()
	opens := 0
	for i := 0; i < len(subs); {
		if subs[i][0] == 4 || subs[i][0] == 190 { // CT_GET_CONTAINER_COUNT, CT_ALARM_GET_STATE : sans transfert
			i++
			continue
		}
		if opens >= len(totals) {
			t.Fatalf("ouverture %d sans taille enregistrée : %v", opens, subs)
		}
		n := int((totals[opens] + 431) / 432)
		want := [][2]uint32{{subs[i][0], 0}}
		for k := 0; k < n; k++ {
			want = append(want, [2]uint32{83, uint32(k)})
		}
		if i+n+2 > len(subs) || !slices.Equal(subs[i+1:i+1+n], want[1:]) || subs[i+1+n][0] != 84 {
			t.Fatalf("transfert %d (lecture %d, %d octets, %d morceaux attendus) : %v", opens, subs[i][0], totals[opens], n, subs[i:min(len(subs), i+n+3)])
		}
		opens++
		i += n + 2
	}
	if opens != len(totals) {
		t.Fatalf("%d transferts suivis, %d ouverts", opens, len(totals))
	}
	return opens
}

// cacheProtectionSent compte les lectures de la protection du cache
// (VM_Ioctl, mot 0 = 3) parmi les ioctl émis.
func cacheProtectionSent(t *testing.T, log []string) int {
	t.Helper()
	n := 0
	for _, l := range log {
		req, h, _ := strings.Cut(l, " ")
		b, err := hex.DecodeString(h)
		if err != nil {
			t.Fatal(err)
		}
		if req == fmt.Sprintf("%#x", aacraid.IoctlSendFIB) && binary.LittleEndian.Uint16(b[4:]) == 500 && binary.LittleEndian.Uint32(b[aacraid.FIBHeaderSize:]) == 3 {
			n++
		}
	}
	return n
}

// Hors du couple validé, les ioctl émis suivent la suite de l'outil : tous
// les morceaux de chaque transfert, puis la clôture (sans l'écart E1), et la
// protection du cache est lue comme sur la carte de référence (CDC,
// sections 6.11 et 6.12).
// Deux cas : modèle qualifié avec un autre firmware (lu par défaut), et
// modèle non qualifié désigné par -allow-unqualified=aacraid. Sur la carte
// de référence, chaque transfert s'arrête au morceau 0 (E1), et la
// protection du cache est lue.
func TestOutsideValidatedPairFollowsToolSequence(t *testing.T) {
	list, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		mod   func(c *module.Card, fw *cardFirmware)
		allow bool
	}{
		"modèle qualifié, autre firmware": {mod: func(c *module.Card, _ *cardFirmware) { c.Hosts[0].Firmware = "5.2-0[19109]" }},
		"modèle non qualifié, option": {mod: func(c *module.Card, fw *cardFirmware) {
			c.PCI.SubsystemDevice, fw.subsystem = "0x0300", 0x0300
		}, allow: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fw := &cardFirmware{t: t}
			aacio.ReplaceIoctl(t, fw.ioctl)
			c, refs := e2eCard()
			c.Hosts[0].SerialNumber, c.Hosts[0].Model = "TEST0001", "ASR6805"
			tc.mod(&c, fw)
			m := aacmod.New(list)
			if tc.allow {
				m.AllowUnqualified()
			}
			r := m.Collect(context.Background(), c, refs)
			if !r.Collected["volumes"] || !r.Collected["disks"] || !r.Collected[aacmod.DiskInventoryCollector] {
				t.Fatalf("carte non lue : %+v", r)
			}
			subs, _ := containerConfigSent(t, fw.log)
			// Au moins un transfert de plusieurs morceaux, pour que la
			// différence avec l'arrêt anticipé soit observable.
			multi := false
			for _, total := range fw.totals {
				multi = multi || total > 432
			}
			if !multi || toolSequence(t, subs, fw.totals) == 0 {
				t.Fatalf("aucun transfert de plusieurs morceaux : %v", fw.totals)
			}
			toolWords(t, fw.pairs)
			if n := cacheProtectionSent(t, fw.log); n == 0 || len(r.Unsupported) != 0 || !r.Collected[aacmod.CacheProtectionCollector] {
				t.Fatalf("protection du cache : %d lectures, non pris en charge %v", n, r.Unsupported)
			}
		})
	}
	t.Run("carte de référence", func(t *testing.T) {
		fw := &cardFirmware{t: t}
		aacio.ReplaceIoctl(t, fw.ioctl)
		c, refs := e2eCard()
		c.Hosts[0].SerialNumber, c.Hosts[0].Model = "TEST0001", "ASR6805"
		aacmod.New(list).Collect(context.Background(), c, refs)
		subs, _ := containerConfigSent(t, fw.log)
		for _, s := range subs {
			if s == ([2]uint32{83, 1}) {
				t.Fatalf("morceau 1 demandé sur le couple validé : %v", subs)
			}
		}
		if cacheProtectionSent(t, fw.log) == 0 {
			t.Fatal("protection du cache non lue sur le couple validé")
		}
		toolWords(t, fw.pairs)
	})
}

// toolWords vérifie, sur les requêtes et réponses enregistrées, que chaque
// requête de morceau ou de clôture est celle que l'outil de référence
// construit à partir de la réponse précédente : il réécrit les mots 0, 1,
// 5 et 6 (morceau) ou 0, 1 et 5 (clôture) et reprend les autres mots de la
// réponse (preuve : analyse de l'outil, versions 2.05 et 3.07). Vérification
// indépendante des constructeurs de requêtes de l'exporter.
func toolWords(t *testing.T, pairs [][2][]byte) {
	t.Helper()
	le := binary.LittleEndian
	w := func(b []byte, i int) uint32 { return le.Uint32(b[aacraid.FIBHeaderSize+4*i:]) }
	var xfer uint32
	checked := 0
	for i, p := range pairs {
		req, rsp := p[0], p[1]
		switch sub := w(req, 1); sub {
		case 83, 84:
			if i == 0 {
				t.Fatal("morceau sans ouverture")
			}
			prev := pairs[i-1][1]
			keep := []int{2, 3, 4, 7}
			if sub == 84 {
				keep = append(keep, 6)
			}
			for _, k := range keep {
				if w(req, k) != w(prev, k) {
					t.Fatalf("requête %d (sous-commande %d) : mot %d = %#x, réponse précédente %#x", i, sub, k, w(req, k), w(prev, k))
				}
			}
			if w(req, 0) != 2 || w(req, 5) != xfer {
				t.Fatalf("requête %d : mots 0 et 5 = %#x, %#x (transfert %#x)", i, w(req, 0), w(req, 5), xfer)
			}
			// Requête entière : en-tête des requêtes paginées de l'outil
			// (capture), le reste de l'en-tête et du tampon à zéro.
			if hex.EncodeToString(req[:12]) != "01001000f4010100e0010002" {
				t.Fatalf("requête %d : en-tête %x", i, req[:12])
			}
			for j, b := range req {
				if (j >= 12 && j < aacraid.FIBHeaderSize || j >= aacraid.FIBHeaderSize+32) && b != 0 {
					t.Fatalf("requête %d : octet %d non nul (%#x)", i, j, b)
				}
			}
			checked++
		default: // ouverture : identifiant rendu au mot 5
			xfer = w(rsp, 5)
		}
	}
	if checked == 0 {
		t.Fatal("aucune requête de morceau vérifiée")
	}
}

// fibCommands rend la suite des ioctl émis (FIB : ioctl/commande).
func fibCommands(t *testing.T, log []string) []string {
	t.Helper()
	var cmds []string
	for _, l := range log {
		req, h, _ := strings.Cut(l, " ")
		b, err := hex.DecodeString(h)
		if err != nil {
			t.Fatal(err)
		}
		if req == fmt.Sprintf("%#x", aacraid.IoctlSendFIB) {
			req += fmt.Sprintf("/%d", binary.LittleEndian.Uint16(b[4:]))
		}
		cmds = append(cmds, req)
	}
	return cmds
}

// Carte non qualifiée autorisée par l'option, 706 en échec : seuls
// GET_HBA_INFO, l'ouverture (704, 701, 703) et la 706 atteignent le noyau.
func TestAllowedUnqualifiedSupplementFailureIoctls(t *testing.T) {
	list, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	fw := &cardFirmware{t: t, fail706: true, subsystem: 0x0300}
	aacio.ReplaceIoctl(t, fw.ioctl)
	c, refs := e2eCard()
	c.Hosts[0].SerialNumber, c.Hosts[0].Model = "TEST0001", "ASR6805"
	c.PCI.SubsystemDevice = "0x0300"
	m := aacmod.New(list)
	m.AllowUnqualified()
	m.Collect(context.Background(), c, refs)
	want := []string{fmt.Sprintf("%#x", aacraid.IoctlGetHBAInfo)}
	for _, cmd := range []int{aacraid.CmdIsAdapterPaused, aacraid.CmdCheckRevision, aacraid.CmdRequestAdapterInfo, aacraid.CmdRequestSupplementAdapterInfo} {
		want = append(want, fmt.Sprintf("%#x/%d", aacraid.IoctlSendFIB, cmd))
	}
	if got := fibCommands(t, fw.log); !slices.Equal(got, want) {
		t.Fatalf("ioctl %v, attendu %v", got, want)
	}
}

// Même module, firmware mis à jour entre deux cycles : au premier cycle
// (couple validé), arrêt au morceau 0 ; au second (autre firmware), suite
// de l'outil ; protection du cache lue aux deux.
func TestFirmwareChangeIoctls(t *testing.T) {
	list, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	fw := &cardFirmware{t: t}
	aacio.ReplaceIoctl(t, fw.ioctl)
	c, refs := e2eCard()
	c.Hosts[0].SerialNumber, c.Hosts[0].Model = "TEST0001", "ASR6805"
	m := aacmod.New(list)
	m.Collect(context.Background(), c, refs)
	subs, _ := containerConfigSent(t, fw.log)
	if cacheProtectionSent(t, fw.log) == 0 || slices.Contains(subs, [2]uint32{83, 1}) {
		t.Fatalf("premier cycle : protection du cache %d, morceaux %v", cacheProtectionSent(t, fw.log), subs)
	}
	fw.log, fw.totals, fw.pairs = nil, nil, nil
	c.Hosts = append([]sysfs.SCSIHost(nil), c.Hosts...)
	c.Hosts[0].Firmware = "5.3-0[19205]"
	r := m.Collect(context.Background(), c, refs)
	if r.CollectErr != nil || !r.Collected["volumes"] || !r.Collected["disks"] {
		t.Fatalf("second cycle : carte non lue après la mise à jour : %v, %v", r.CollectErr, r.Collected)
	}
	// Identité changée : l'ouverture complète est relue (aucune réponse
	// gardée sous l'ancien firmware n'est réutilisée).
	open := []string{fmt.Sprintf("%#x", aacraid.IoctlGetHBAInfo)}
	for _, cmd := range []int{aacraid.CmdIsAdapterPaused, aacraid.CmdCheckRevision, aacraid.CmdRequestAdapterInfo, aacraid.CmdRequestSupplementAdapterInfo} {
		open = append(open, fmt.Sprintf("%#x/%d", aacraid.IoctlSendFIB, cmd))
	}
	if got := fibCommands(t, fw.log); len(got) < len(open) || !slices.Equal(got[:len(open)], open) {
		t.Fatalf("second cycle : ouverture %v, attendu %v", got, open)
	}
	subs, _ = containerConfigSent(t, fw.log)
	if n := cacheProtectionSent(t, fw.log); n == 0 {
		t.Fatal("second cycle : protection du cache non lue")
	}
	toolSequence(t, subs, fw.totals)
	toolWords(t, fw.pairs)
}

// Ouverture comme l'outil, vérifiée sur les ioctl émis : carte en pause
// (704) ou révision refusée (701) : rien après ; 703 sans le bit 16 : pas de
// 706, ni comptage ni lecture paginée, protection du cache lue.
func TestOpeningIoctls(t *testing.T) {
	list, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	fib := func(cmds ...int) []string {
		out := []string{fmt.Sprintf("%#x", aacraid.IoctlGetHBAInfo)}
		for _, c := range cmds {
			out = append(out, fmt.Sprintf("%#x/%d", aacraid.IoctlSendFIB, c))
		}
		return out
	}
	for name, tc := range map[string]struct {
		fw   *cardFirmware
		want []string
	}{
		"carte en pause":   {&cardFirmware{paused: true}, fib(aacraid.CmdIsAdapterPaused)},
		"révision refusée": {&cardFirmware{revBuild: aacraid.MinFirmwareBuild - 1}, fib(aacraid.CmdIsAdapterPaused, aacraid.CmdCheckRevision)},
	} {
		tc.fw.t = t
		aacio.ReplaceIoctl(t, tc.fw.ioctl)
		c, refs := e2eCard()
		c.Hosts[0].SerialNumber, c.Hosts[0].Model = "TEST0001", "ASR6805"
		r := aacmod.New(list).Collect(context.Background(), c, refs)
		if got := fibCommands(t, tc.fw.log); !slices.Equal(got, tc.want) || r.CollectErr == nil {
			t.Errorf("%s : ioctl %v, attendu %v ; erreur %v", name, got, tc.want, r.CollectErr)
		}
	}
	fw := &cardFirmware{t: t, noSupplement: true}
	aacio.ReplaceIoctl(t, fw.ioctl)
	c, refs := e2eCard()
	c.Hosts[0].SerialNumber, c.Hosts[0].Model = "TEST0001", "ASR6805"
	aacmod.New(list).Collect(context.Background(), c, refs)
	got := fibCommands(t, fw.log)
	if slices.Contains(got, fmt.Sprintf("%#x/%d", aacraid.IoctlSendFIB, aacraid.CmdRequestSupplementAdapterInfo)) {
		t.Errorf("sans bit 16 : 706 envoyée : %v", got)
	}
	// Seuls le comptage (ouverture de l'outil, sans condition de capacité)
	// et l'alarme (bit 11 de la 703 seulement) partent : ni transfert ni 708.
	if subs, cm := containerConfigSent(t, fw.log); !slices.Equal(subs, [][2]uint32{{4, 0}, {190, 0}}) || cm || cacheProtectionSent(t, fw.log) == 0 {
		t.Errorf("sans bit 16 : VM_ContainerConfig %v, 708 %v, cache %d", subs, cm, cacheProtectionSent(t, fw.log))
	}
}

// Tâches vérifiées sur les ioctl émis : une reconstruction sur le
// conteneur 0xfe ; la 103 part une fois, après les 101, avec l'identifiant
// de la tâche (requête écrite en clair), et l'avancement est publié.
func TestTaskIoctls(t *testing.T) {
	list, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	blk := make([]byte, 48)
	le := binary.LittleEndian
	le.PutUint32(blk[0:], 0x2a)         // identifiant
	le.PutUint32(blk[4:], 110)          // reconstruction d'un miroir
	le.PutUint32(blk[8:], 0xfe)         // conteneur
	le.PutUint32(blk[0x10:], 102)       // en cours
	le.PutUint64(blk[0x18:], 4_000_000) // finalTick
	le.PutUint64(blk[0x20:], 1_000_000) // currentTick
	fw := &cardFirmware{t: t, jobs: map[uint32][][]byte{0xfe: {blk}}}
	aacio.ReplaceIoctl(t, fw.ioctl)
	c, refs := e2eCard()
	c.Hosts[0].SerialNumber, c.Hosts[0].Model = "TEST0001", "ASR6805"
	r := aacmod.New(list).Collect(context.Background(), c, refs)
	want := capturedRequest("01001000bc02010048000002", "67000000000000002a000000")
	n := 0
	for _, l := range fw.log {
		if l+"\n" == want {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("103 envoyée %d fois", n)
	}
	if len(r.Tasks) != 1 || r.Tasks[0].Task != "rebuild" || r.Tasks[0].Ratio != 0.25 || !r.Collected["tasks"] {
		t.Fatalf("tâches : %+v, %v", r.Tasks, r.Collected)
	}
}

// Batterie classique vérifiée sur les ioctl émis : seule la lecture de
// présence part (ni 0x700000 ni 0xa0000), l'état est décodé de sa réponse.
func TestBatteryIoctls(t *testing.T) {
	list, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	fw := &cardFirmware{t: t, battery: &[3]byte{1, 1, 1}}
	aacio.ReplaceIoctl(t, fw.ioctl)
	c, refs := e2eCard()
	c.Hosts[0].SerialNumber, c.Hosts[0].Model = "TEST0001", "ASR6805"
	r := aacmod.New(list).Collect(context.Background(), c, refs)
	var cmds []uint32
	for _, l := range fw.log {
		_, h, _ := strings.Cut(l, " ")
		b, _ := hex.DecodeString(h)
		if binary.LittleEndian.Uint16(b[4:]) == 500 && binary.LittleEndian.Uint32(b[aacraid.FIBHeaderSize:]) == 3 {
			cmds = append(cmds, binary.LittleEndian.Uint32(b[aacraid.FIBHeaderSize+16:]))
		}
	}
	if !slices.Equal(cmds, []uint32{0x30000}) {
		t.Fatalf("VM_Ioctl envoyés : %#x", cmds)
	}
	cp := r.CacheProtection
	if cp == nil || !cp.Present || cp.Type != "battery" || cp.Vendor != "bbu" || cp.State != "maintenance" || !r.Collected["cache_protection"] {
		t.Fatalf("protection du cache : %+v, %v", cp, r.Collected)
	}
}

// Bit 13 de FeatureBits (forme 2, CDC section 6.11), sur les ioctl émis par
// le module et le transport réels : le mot 7 de CT_GET_CONTAINER_COUNT et de
// chaque ouverture (229, 234, 247, 248, 249) porte 0x80000000 en plus de la
// taille de la forme habituelle ; morceaux et clôtures reprennent le total
// de la réponse, sans ce bit (confirmé sur carte émulée, variante V2) ; la
// collecte aboutit. Sans le bit 13, aucun mot 7 ne porte ce bit.
func TestHighBitIoctls(t *testing.T) {
	list, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	le := binary.LittleEndian
	for _, high := range []bool{true, false} {
		fw := &cardFirmware{t: t}
		if high {
			fw.extraFeatureBits = aacraid.FeaturePagedHighBit
		}
		aacio.ReplaceIoctl(t, fw.ioctl)
		c, refs := e2eCard()
		c.Hosts[0].SerialNumber, c.Hosts[0].Model = "TEST0001", "ASR6805"
		r := aacmod.New(list).Collect(context.Background(), c, refs)
		if !r.Collected["volumes"] || !r.Collected["disks"] {
			t.Fatalf("bit 13 %v : collecteurs %v", high, r.Collected)
		}
		opens, alarms := 0, 0
		for _, l := range fw.log {
			req, h, _ := strings.Cut(l, " ")
			b, _ := hex.DecodeString(h)
			if req != fmt.Sprintf("%#x", aacraid.IoctlSendFIB) || le.Uint16(b[4:]) != 500 || le.Uint32(b[aacraid.FIBHeaderSize:]) != 2 {
				continue
			}
			sub, w7 := le.Uint32(b[aacraid.FIBHeaderSize+4:]), le.Uint32(b[aacraid.FIBHeaderSize+28:])
			switch sub {
			case 83, 84:
				if w7&aacraid.SizeHighBit != 0 {
					t.Errorf("bit 13 %v : sous-commande %d avec le bit de poids fort (%#x)", high, sub, w7)
				}
			default:
				opens++
				if (w7&aacraid.SizeHighBit != 0) != high {
					t.Errorf("bit 13 %v : sous-commande %d, mot 7 %#x", high, sub, w7)
				}
				if sub == 4 && w7&^aacraid.SizeHighBit != 0 {
					t.Errorf("comptage : taille %#x", w7)
				}
				if sub == 190 {
					alarms++
					if w7&^aacraid.SizeHighBit != 8 {
						t.Errorf("alarme : taille %#x", w7)
					}
				}
			}
		}
		if opens < 10 || alarms != 1 || r.Alarm != "disabled" {
			t.Fatalf("bit 13 %v : %d ouvertures vues, %d alarmes, état %q", high, opens, alarms, r.Alarm)
		}
	}
}

// Alarme sonore sur les ioctl émis par le module et le transport réels :
// une requête 190 par cycle, identique à la capture ; état décodé de la
// réponse (octet 0x20, bit 0x02). Statut non nul ou ioctl en échec : seul le
// collecteur alarm est en échec, et les requêtes émises sont exactement
// celles du cycle nominal (la carte n'est pas arrêtée : protection du cache
// et tâches suivent). Sans le bit 11 de SupportedOptions : aucune 190.
func TestAlarmIoctls(t *testing.T) {
	list, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	alarm := capturedRequest("01001000f4010100e0010002", "02000000be000000010000000000000000000000ffffffff0000000008000000")
	run := func(fw *cardFirmware) (module.Result, []string) {
		fw.t = t
		aacio.ReplaceIoctl(t, fw.ioctl)
		c, refs := e2eCard()
		c.Hosts[0].SerialNumber, c.Hosts[0].Model = "TEST0001", "ASR6805"
		return aacmod.New(list).Collect(context.Background(), c, refs), fw.log
	}
	count := func(log []string) int {
		n := 0
		for _, l := range log {
			if l+"\n" == alarm {
				n++
			}
		}
		return n
	}
	r, nominal := run(&cardFirmware{})
	if count(nominal) != 1 || r.Alarm != "disabled" || !r.Collected["alarm"] {
		t.Fatalf("nominal : %d requêtes 190, état %q, %v", count(nominal), r.Alarm, r.Collected)
	}
	if r, _ := run(&cardFirmware{alarmByte: 0x02}); r.Alarm != "enabled" || !r.Collected["alarm"] {
		t.Fatalf("bit 0x02 : état %q, %v", r.Alarm, r.Collected)
	}
	for name, fw := range map[string]*cardFirmware{
		"statut non nul": {alarmStatus: 1},
		"ioctl en échec": {alarmEIO: true},
		"mot 2 à 2":      {alarmWord2: 2, alarmByte: 0x02},
	} {
		r, log := run(fw)
		ok, tried := r.Collected["alarm"]
		if !tried || ok || r.Alarm != "" || r.CollectErr == nil {
			t.Errorf("%s : %v, état %q, %v", name, r.Collected, r.Alarm, r.CollectErr)
		}
		for col, ok := range r.Collected {
			if !ok && col != "alarm" {
				t.Errorf("%s : %s en échec", name, col)
			}
		}
		if !slices.Equal(log, nominal) {
			t.Errorf("%s : requêtes différentes du cycle nominal (%d contre %d)", name, len(log), len(nominal))
		}
	}
	r, log := run(&cardFirmware{clearOptions: aacraid.SupportedAlarm})
	if count(log) != 0 || !slices.Contains(r.Unsupported, "alarm") || cacheProtectionSent(t, log) == 0 || len(log) != len(nominal)-1 {
		t.Fatalf("sans bit 11 : %d requêtes 190, non pris en charge %v, %d requêtes (nominal %d)", count(log), r.Unsupported, len(log), len(nominal))
	}
}
