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

// CT_ALARM_GET_STATE capturée sur la carte de référence (outil 2.05, 14
// envois, séquences 25 et 481 de chaque capture) : une seule forme de
// requête et une seule réponse. Requête : identique sur les 512 octets
// (octets 12 à 31 de l'en-tête nuls dans la capture), écrite ici en clair.
// Réponse : en-tête sans les adresses du pilote (octets 12 à 31), données
// jusqu'à _ALARM_INFO (8 octets nuls, offset 0x20) ; le reste est nul.
const (
	capturedAlarmHeader = "01001000f4010100e0010002"
	capturedAlarmWords  = "02000000be000000010000000000000000000000ffffffff0000000008000000"
	capturedAlarmRsp    = "00000000f4010100f0010002" + "0000000000000000000000000000000000000000" +
		"00000000be000000010000000000000000000000ffffffff0000000008000000" + "0000000000000000"
)

func alarmResponse(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, FIBSize)
	copy(b, mustHex(t, capturedAlarmRsp))
	return b
}

// Exigence S4 : requête identique, octet pour octet, à celle de l'outil ;
// variante du bit 13 : seul le mot 7 change (8 | 0x80000000). Les deux sont
// acceptées par la liste blanche.
func TestAlarmRequestMatchesCapture(t *testing.T) {
	want := make([]byte, FIBSize)
	copy(want, mustHex(t, capturedAlarmHeader))
	copy(want[FIBHeaderSize:], mustHex(t, capturedAlarmWords))
	got := AlarmRequest()
	if !bytes.Equal(got, want) {
		t.Fatalf("requête :\n got %x\nwant %x", got[:64], want[:64])
	}
	if err := CheckFIBRequest(got); err != nil {
		t.Fatalf("requête capturée refusée : %v", err)
	}
	high := WithSizeHighBit(got)
	binary.LittleEndian.PutUint32(want[FIBHeaderSize+28:], 0x80000008)
	if !bytes.Equal(high, want) {
		t.Fatalf("variante du bit 13 :\n got %x\nwant %x", high[:64], want[:64])
	}
	if err := CheckFIBRequest(high); err != nil {
		t.Fatalf("variante du bit 13 refusée : %v", err)
	}
}

// Liste blanche : chaque mot de la requête est fixe ; toute variation, y
// compris d'autres tailles avec ou sans le bit de poids fort, et tout
// résidu au-delà des huit mots, est refusé.
func TestAlarmAllowlistMutations(t *testing.T) {
	l := mustList(t)
	check := func(b []byte) bool {
		_, err := l.Check(allowlist.Request{Ioctl: IoctlSendFIB, Buf: b})
		if err != nil && !errors.Is(err, allowlist.ErrDenied) {
			t.Fatal(err)
		}
		return err == nil
	}
	if !check(AlarmRequest()) || !check(WithSizeHighBit(AlarmRequest())) {
		t.Fatal("forme qualifiée refusée")
	}
	le := binary.LittleEndian
	word := func(i int, v uint32) func([]byte) { return func(b []byte) { le.PutUint32(b[FIBHeaderSize+4*i:], v) } }
	for name, mut := range map[string]func([]byte){
		"paramètre 0":                   word(2, 0),
		"paramètre 2":                   word(2, 2),
		"mot 3":                         word(3, 1),
		"marque 0xda":                   word(4, 0xda),
		"transfert établi":              word(5, 0x2061e),
		"mot 6":                         word(6, 1),
		"taille nulle":                  word(7, 0),
		"taille 9":                      word(7, 9),
		"taille 0x1000":                 word(7, 0x1000),
		"bit de poids fort seul":        word(7, SizeHighBit),
		"taille 9 avec le bit":          word(7, SizeHighBit|9),
		"autre bit de poids fort":       word(7, 0x40000008),
		"résidu après les huit mots":    func(b []byte) { b[FIBHeaderSize+32] = 1 },
		"résidu en fin de tampon":       func(b []byte) { b[FIBSize-1] = 1 },
		"adresse dans l'en-tête":        func(b []byte) { b[16] = 1 },
		"Size de l'en-tête":             func(b []byte) { le.PutUint16(b[fibSize:], 4) },
		"VM_Ioctl au lieu de VM_Config": word(0, 3),
	} {
		b := AlarmRequest()
		mut(b)
		if check(b) {
			t.Errorf("%s : accepté", name)
		}
	}
	// Tout octet modifié est refusé, sauf la seule variante du bit 13.
	high := WithSizeHighBit(AlarmRequest())
	for i := range FIBSize {
		for _, v := range []byte{0x00, 0x01, 0x02, 0x16, 0x80, 0xbe, 0xff} {
			b := AlarmRequest()
			if b[i] == v {
				continue
			}
			b[i] = v
			if check(b) != bytes.Equal(b, high) {
				t.Errorf("octet %d = %#x : accepté %v", i, v, !bytes.Equal(b, high))
			}
		}
	}
}

// Réponse capturée (8 octets de _ALARM_INFO nuls) : désactivée, comme
// l'affiche l'outil (« Controller Alarm : Disabled ») ; bit 0x02 du premier
// octet : activée. Seul ce bit, à cet offset, compte.
func TestDecodeAlarm(t *testing.T) {
	st, err := DecodeAlarm(alarmResponse(t))
	if err != nil || st != AlarmDisabled {
		t.Fatalf("réponse capturée : %q, %v", st, err)
	}
	set := func(off int, v byte) AlarmState {
		b := alarmResponse(t)
		b[FIBHeaderSize+off] = v
		st, err := DecodeAlarm(b)
		if err != nil {
			t.Fatalf("octet %#x = %#x : %v", off, v, err)
		}
		return st
	}
	if st := set(0x20, 0x02); st != AlarmEnabled {
		t.Fatalf("bit 0x02 : %q", st)
	}
	if st := set(0x20, 0xff); st != AlarmEnabled {
		t.Fatalf("octet 0xff : %q", st)
	}
	// Autres bits du premier octet (0x04 : sens non établi) : sans effet.
	if st := set(0x20, 0xfd); st != AlarmDisabled {
		t.Fatalf("tous les bits sauf 0x02 : %q", st)
	}
	// Bit 0x02 aux octets voisins de _ALARM_INFO[0] : sans effet.
	for _, off := range []int{0x1c, 0x1f, 0x21, 0x27} {
		if st := set(off, 0x02); st != AlarmDisabled {
			t.Fatalf("bit 0x02 à l'offset %#x : %q", off, st)
		}
	}
}

// Statut non nul, mauvais écho, mot 2 autre que 1 (refusé par l'outil),
// autre commande, autre Size, tampon tronqué : refusés, sans état.
func TestDecodeAlarmRejects(t *testing.T) {
	le := binary.LittleEndian
	for name, mut := range map[string]func([]byte){
		"statut non nul":            func(b []byte) { le.PutUint32(b[FIBHeaderSize:], 1) },
		"statut 0xc5":               func(b []byte) { le.PutUint32(b[FIBHeaderSize:], 0xc5) },
		"écho du comptage":          func(b []byte) { le.PutUint32(b[FIBHeaderSize+4:], ctGetContainerCount) },
		"écho 229":                  func(b []byte) { le.PutUint32(b[FIBHeaderSize+4:], 229) },
		"autre commande":            func(b []byte) { le.PutUint16(b[fibCommand:], 706) },
		"Size de la requête (480)":  func(b []byte) { le.PutUint16(b[fibSize:], pagedDataSize) },
		"Size de 32 octets":         func(b []byte) { le.PutUint16(b[fibSize:], FIBHeaderSize) },
		"statut non nul, bit 0x02":  func(b []byte) { le.PutUint32(b[FIBHeaderSize:], 1); b[FIBHeaderSize+0x20] = 2 },
		"mauvais écho, bit 0x02":    func(b []byte) { le.PutUint32(b[FIBHeaderSize+4:], 191); b[FIBHeaderSize+0x20] = 2 },
		"mot 2 nul":                 func(b []byte) { le.PutUint32(b[FIBHeaderSize+8:], 0) },
		"mot 2 à 2, bit 0x02":       func(b []byte) { le.PutUint32(b[FIBHeaderSize+8:], 2); b[FIBHeaderSize+0x20] = 2 },
		"mot 2 à 4":                 func(b []byte) { le.PutUint32(b[FIBHeaderSize+8:], 4) },
		"requête non traitée (480)": func(b []byte) { copy(b, AlarmRequest()) },
	} {
		b := alarmResponse(t)
		mut(b)
		if st, err := DecodeAlarm(b); err == nil {
			t.Errorf("%s : accepté (%q)", name, st)
		}
	}
	if _, err := DecodeAlarm(alarmResponse(t)[:FIBSize-1]); err == nil {
		t.Error("tampon tronqué accepté")
	}
}

// Bit 11 de SupportedOptions (AAC_OPT_ALARM) : annoncé par la réponse 703
// capturée (0x141f1d7c) ; seul ce bit compte.
func TestAdapterInfoAlarm(t *testing.T) {
	a, err := DecodeAdapterInfo(padFIB(t, capturedAdapterInfo))
	if err != nil || !a.Alarm() {
		t.Fatalf("703 capturée : %#x, alarme %v, %v", a.SupportedOptions, a.Alarm(), err)
	}
	for opts, want := range map[uint32]bool{
		0x141f1d7c &^ SupportedAlarm: false,
		0xffffffff &^ 0x800:          false,
		0x800:                        true,
		0x400:                        false,
		0x1000:                       false,
	} {
		if got := (AdapterInfo{SupportedOptions: opts}).Alarm(); got != want {
			t.Errorf("SupportedOptions %#x : %v, attendu %v", opts, got, want)
		}
	}
}
