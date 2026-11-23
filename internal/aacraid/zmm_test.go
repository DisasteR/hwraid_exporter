// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// Échanges enregistrés sur l'Adaptec 6805 de référence (firmware 5.3-0
// [19204], module AFM-600 déclaré en panne par le firmware), capture de
// l'outil de référence lors de GETCONFIG AD. Requêtes : 12 premiers octets
// d'en-tête puis les données couvertes par Size. Réponses : idem ; elles ne
// contiennent aucun identifiant.
const (
	capPresenceReqHdr  = "01001000f40101002c000002"
	capPresenceReqData = "0300000009000000030000000000000000000300000000000000000000000000000000000000000000000000"
	capPresenceRspHdr  = "00000000f40101004c000002"
	capPresenceRspData = "0000000009000000030000000000000000000300000000000000000000000000000000000000000000000020"

	capStateReqHdr  = "01001000f40101004c000002"
	capStateReqData = "03000000090000000300000000000000000070000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
	capStateRspHdr  = "00000000f40101006c000002"
	capStateRspData = "00000000090000000300000000000000000070004100020000000000000000000000000000000000000000000000000081000040000000000000000000000000000000000000000000000000"
)

// recordedFIB reconstitue une FIB de 512 octets : en-tête enregistré,
// données enregistrées, reste à zéro.
func recordedFIB(t *testing.T, hdr, data string) []byte {
	b := make([]byte, FIBSize)
	copy(b, mustHex(t, hdr))
	copy(b[FIBHeaderSize:], mustHex(t, data))
	return b
}

// Exigence S4 : partie significative identique à la capture (12 octets
// d'en-tête et données couvertes par Size), tout le reste à zéro.
func TestZMMRequestsMatchCapture(t *testing.T) {
	cases := []struct {
		name      string
		got       []byte
		hdr, data string
	}{
		{"présence", CacheProtectionPresenceRequest(), capPresenceReqHdr, capPresenceReqData},
		{"état", CacheProtectionStateRequest(), capStateReqHdr, capStateReqData},
	}
	for _, c := range cases {
		if want := recordedFIB(t, c.hdr, c.data); !bytes.Equal(c.got, want) {
			t.Fatalf("%s : requête différente de la capture\n got %x\nwant %x", c.name, c.got[:FIBHeaderSize+80], want[:FIBHeaderSize+80])
		}
		size := int(binary.LittleEndian.Uint16(c.got[8:])) + FIBHeaderSize
		if sender := int(binary.LittleEndian.Uint16(c.got[10:])); sender > size {
			size = sender
		}
		if size != FIBSize {
			t.Fatalf("%s : plage copiée par le pilote %d, tampon %d", c.name, size, FIBSize)
		}
	}
}

func TestDecodeZMMRecorded(t *testing.T) {
	present, err := DecodeCacheProtectionPresence(recordedFIB(t, capPresenceRspHdr, capPresenceRspData))
	if err != nil || !present {
		t.Fatalf("présence : %v %v", present, err)
	}
	st, err := DecodeCacheProtectionState(recordedFIB(t, capStateRspHdr, capStateRspData))
	if err != nil {
		t.Fatal(err)
	}
	// Affichage de l'outil de référence au même instant : « ZMM Failed ».
	if st.Word != 0x40000081 || st.State != CacheProtectionFailed {
		t.Fatalf("%+v", st)
	}
}

// Règle complète, y compris les états non encore observés sur matériel.
func TestZMMStateRule(t *testing.T) {
	cases := map[uint32]CacheProtectionState{
		0x40000081: CacheProtectionFailed,       // référence : installé, pas prêt
		0x40000001: CacheProtectionFailed,       // installé, pas prêt
		0x40100001: CacheProtectionFailed,       // installé, pas prêt, bit inconnu : en panne quand même
		0x40000000: CacheProtectionNotInstalled, // aucun bit d'état
		0x40000008: CacheProtectionUnknown,      // prêt sans être installé : ambigu, pas « absent »
		0x40000080: CacheProtectionUnknown,      // bit 0x80 seul : ambigu, pas « absent »
		0x40000009: CacheProtectionOptimal,
		0x4000000D: CacheProtectionCharging, // prêt et en charge
		0x40000005: CacheProtectionFailed,   // en charge mais pas prêt : en panne d'abord
		0x40000089: CacheProtectionUnknown,  // prêt avec 0x80 : 0x80 non qualifié sur un module sain
		0x4000008D: CacheProtectionUnknown,  // en charge avec 0x80
		0x40000019: CacheProtectionUnknown,  // prêt avec un bit inconnu : jamais « optimal »
		0x4100000D: CacheProtectionUnknown,  // en charge avec un bit inconnu
		0x30000009: CacheProtectionOtherType,
		0x00000009: CacheProtectionOtherType,
	}
	for r, want := range cases {
		if got := zmmState(r); got != want {
			t.Fatalf("R=%#x : %s, attendu %s", r, got, want)
		}
	}
}

func TestDecodeZMMRejects(t *testing.T) {
	mod := func(hdr, data string, f func(b []byte)) []byte {
		b := recordedFIB(t, hdr, data)
		f(b)
		return b
	}
	st := func(f func(b []byte)) []byte { return mod(capStateRspHdr, capStateRspData, f) }
	le := binary.LittleEndian
	cases := map[string][]byte{
		"statut non nul":      st(func(b []byte) { le.PutUint32(b[32:], 1) }),
		"autre commande":      st(func(b []byte) { le.PutUint16(b[4:], 706) }),
		"autre IoctlCmd":      st(func(b []byte) { le.PutUint32(b[32+16:], 0x30000) }),
		"autre ObjType":       st(func(b []byte) { le.PutUint32(b[32+4:], 1) }),
		"autre MethId":        st(func(b []byte) { le.PutUint32(b[32+8:], 1) }),
		"bloc non valide":     st(func(b []byte) { b[32+20] &^= 1 }),
		"taille trop petite":  st(func(b []byte) { le.PutUint16(b[8:], FIBHeaderSize+51) }),
		"taille hors tampon":  st(func(b []byte) { le.PutUint16(b[8:], FIBSize+1) }),
		"requête non traitée": CacheProtectionStateRequest(),
		"réponse de présence": recordedFIB(t, capPresenceRspHdr, capPresenceRspData),
	}
	for name, b := range cases {
		if _, err := DecodeCacheProtectionState(b); err == nil {
			t.Fatalf("%s : réponse acceptée", name)
		}
	}
	if _, err := DecodeCacheProtectionPresence(recordedFIB(t, capStateRspHdr, capStateRspData)); err == nil {
		t.Fatal("réponse d'état acceptée comme présence")
	}
	// Bit 0x2000 absent : pas d'erreur, module non décrit.
	b := mod(capPresenceRspHdr, capPresenceRspData, func(b []byte) { b[32+43] = 0 })
	if p, err := DecodeCacheProtectionPresence(b); err != nil || p {
		t.Fatalf("présence sans le bit : %v %v", p, err)
	}
}
