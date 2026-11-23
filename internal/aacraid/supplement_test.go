// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// capturedRequestHeader : les 12 premiers octets de la requête 706 émise par
// l'outil de référence sur l'Adaptec 6805 (firmware 5.3-0[19204]). Les
// octets 12 à 31 de la capture sont des adresses propres à l'outil,
// différentes à chaque appel ; ses 480 octets de données sont nuls.
const capturedRequestHeader = "01000000" + "c202" + "01" + "00" + "0400" + "0002"

// capturedResponse : réponse matérielle enregistrée au même moment, tronquée
// après le dernier octet non nul. Champs identifiants mis à zéro : octets
// 12-31 de l'en-tête, vpd_info, mfg_pcba_serial_no et mfg_wwn_name.
const capturedResponse = "00000000c202010000020002000000000000000000000000000000000000000041646170746563203638303500000000000000000000800072000000000000000f000000df57ff0fff00020841707220203720323031370008000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000044b0000ffff7f0000000000044b000000000000000000000000000000000000000000003c69c8000100000000000000090800000301881308"

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Exigence S4 : partie significative identique à la capture, reste à zéro.
func TestSupplementRequestMatchesCapture(t *testing.T) {
	req := SupplementInfoRequest()
	if len(req) != FIBSize {
		t.Fatalf("%d octets", len(req))
	}
	if want := mustHex(t, capturedRequestHeader); !bytes.Equal(req[:len(want)], want) {
		t.Fatalf("en-tête %x, capture %x", req[:len(want)], want)
	}
	for i := 12; i < FIBSize; i++ {
		if req[i] != 0 {
			t.Fatalf("octet %d = %#x, attendu 0", i, req[i])
		}
	}
	// Le pilote copie max(Size + 32, SenderSize) octets : la plage doit
	// être exactement celle du tampon, sinon il lirait ou écrirait au-delà.
	size := int(binary.LittleEndian.Uint16(req[8:])) + FIBHeaderSize
	if sender := int(binary.LittleEndian.Uint16(req[10:])); sender > size {
		size = sender
	}
	if size != FIBSize {
		t.Fatalf("plage copiée par le pilote : %d octets, tampon %d", size, FIBSize)
	}
}

func recorded(t *testing.T) []byte {
	b := make([]byte, FIBSize)
	copy(b, mustHex(t, capturedResponse))
	return b
}

func TestDecodeSupplementInfoRecorded(t *testing.T) {
	s, err := DecodeSupplementInfo(recorded(t))
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != 15 || s.SupportedOptions2 != 0x00c8693c || !s.ArcIoReads() {
		t.Fatalf("%+v ArcIo=%v", s, s.ArcIoReads())
	}
}

func TestArcIoReadsRule(t *testing.T) {
	cases := []struct {
		s    SupplementInfo
		want bool
	}{
		{SupplementInfo{Version: 2, SupportedOptions2: 0x08}, true},
		{SupplementInfo{Version: 15, SupportedOptions2: 0xfffffff7}, false}, // tous les bits sauf 0x08
		{SupplementInfo{Version: 1, SupportedOptions2: 0x08}, false},        // champ non valide
		{SupplementInfo{Version: 0, SupportedOptions2: 0xffffffff}, false},
	}
	for _, c := range cases {
		if c.s.ArcIoReads() != c.want {
			t.Fatalf("%+v : %v, attendu %v", c.s, !c.want, c.want)
		}
	}
}

// Les valeurs sont lues aux décalages publics : déplacer le bit hors de
// SupportedOptions2 ou modifier Version change le résultat.
func TestDecodeSupplementInfoOffsets(t *testing.T) {
	b := recorded(t)
	binary.LittleEndian.PutUint32(b[FIBHeaderSize+0x90:], 0)
	binary.LittleEndian.PutUint32(b[FIBHeaderSize+0x94:], 0x08) // champ voisin
	if s, err := DecodeSupplementInfo(b); err != nil || s.ArcIoReads() {
		t.Fatalf("bit lu hors de SupportedOptions2 : %+v %v", s, err)
	}
	b = recorded(t)
	binary.LittleEndian.PutUint32(b[FIBHeaderSize+0x20:], 1)
	if s, err := DecodeSupplementInfo(b); err != nil || s.ArcIoReads() {
		t.Fatalf("Version 1 acceptée : %+v %v", s, err)
	}
}

func TestDecodeSupplementInfoRejects(t *testing.T) {
	mod := func(f func(b []byte)) []byte {
		b := recorded(t)
		f(b)
		return b
	}
	cases := map[string][]byte{
		"tampon court":        recorded(t)[:FIBSize-1],
		"autre commande":      mod(func(b []byte) { binary.LittleEndian.PutUint16(b[4:], 703) }),
		"taille trop petite":  mod(func(b []byte) { binary.LittleEndian.PutUint16(b[8:], FIBHeaderSize+0x93) }),
		"taille hors tampon":  mod(func(b []byte) { binary.LittleEndian.PutUint16(b[8:], FIBSize+1) }),
		"requête non traitée": SupplementInfoRequest(), // Size = 4 : rien n'a été renvoyé
	}
	for name, b := range cases {
		if _, err := DecodeSupplementInfo(b); err == nil {
			t.Fatalf("%s : réponse acceptée", name)
		}
	}
	// Taille limite exacte : acceptée.
	if _, err := DecodeSupplementInfo(mod(func(b []byte) { binary.LittleEndian.PutUint16(b[8:], FIBHeaderSize+0x94) })); err != nil {
		t.Fatalf("taille minimale refusée : %v", err)
	}
}
