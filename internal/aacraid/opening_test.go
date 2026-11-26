// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// Requêtes et réponses 704 et 701 de l'outil de référence sur la carte de
// référence (sept consultations capturées, identiques) : en-tête sans les
// octets d'adresse, puis données significatives (Size).
const (
	capturedPausedHeader   = "01001000c002010004000002"
	capturedRevisionHeader = "01001000bd0201000c000002"
	capturedRevisionData   = "04000000000404070a510000"
	capturedPausedRsp      = "00000000c002010024000002"
	capturedRevisionRspHdr = "00000000bd0201002c000002"
	capturedRevisionRsp    = "0100000000040305044b0000ffffffff"
)

func TestOpeningRequestsMatchCapture(t *testing.T) {
	for _, tc := range []struct {
		name       string
		req        []byte
		head, data string
	}{
		{"704", PausedRequest(), capturedPausedHeader, ""},
		{"701", RevisionRequest(), capturedRevisionHeader, capturedRevisionData},
	} {
		if len(tc.req) != FIBSize {
			t.Fatalf("%s : %d octets", tc.name, len(tc.req))
		}
		if want := mustHex(t, tc.head); !bytes.Equal(tc.req[:len(want)], want) {
			t.Fatalf("%s : en-tête %x, capture %x", tc.name, tc.req[:len(want)], want)
		}
		data := mustHex(t, tc.data)
		if !bytes.Equal(tc.req[FIBHeaderSize:FIBHeaderSize+len(data)], data) {
			t.Fatalf("%s : données %x, capture %x", tc.name, tc.req[FIBHeaderSize:FIBHeaderSize+len(data)], data)
		}
		// Octets d'adresse et au-delà de Size : à zéro.
		for i := 12; i < FIBSize; i++ {
			if (i < FIBHeaderSize || i >= FIBHeaderSize+len(data)) && tc.req[i] != 0 {
				t.Fatalf("%s : octet %d = %#x, attendu 0", tc.name, i, tc.req[i])
			}
		}
		if err := CheckFIBRequest(tc.req); err != nil {
			t.Fatalf("%s : refusée par la liste blanche : %v", tc.name, err)
		}
	}
}

func TestOpeningRequestVariantsRefused(t *testing.T) {
	for name, mut := range map[string]func([]byte){
		"704 sans 0x100000":     func(b []byte) { binary.LittleEndian.PutUint32(b, 1) },
		"704 avec donnée":       func(b []byte) { b[FIBHeaderSize] = 1 },
		"701 build de l'outil":  func(b []byte) { binary.LittleEndian.PutUint32(b[FIBHeaderSize+8:], 0x5194) },
		"701 résidu":            func(b []byte) { b[FIBHeaderSize+12] = 0xff },
		"701 taille de données": func(b []byte) { binary.LittleEndian.PutUint16(b[8:], 16) },
	} {
		var b []byte
		if name[:3] == "704" {
			b = PausedRequest()
		} else {
			b = RevisionRequest()
		}
		mut(b)
		if err := CheckFIBRequest(b); !errors.Is(err, ErrFIBNotAllowed) {
			t.Errorf("%s : variante acceptée (%v)", name, err)
		}
	}
}

func openingResponse(t *testing.T, head, data string) []byte {
	b := make([]byte, FIBSize)
	copy(b, mustHex(t, head))
	copy(b[FIBHeaderSize:], mustHex(t, data))
	return b
}

func TestDecodePaused(t *testing.T) {
	b := openingResponse(t, capturedPausedRsp, "")
	if p, err := DecodePaused(b); err != nil || p {
		t.Fatalf("réponse capturée : pause %v, %v", p, err)
	}
	b[FIBHeaderSize] = 1
	if p, err := DecodePaused(b); err != nil || !p {
		t.Fatalf("octet 0 non nul : pause %v, %v", p, err)
	}
	// Seul le premier octet compte (l'outil teste un octet, pas un mot).
	b = openingResponse(t, capturedPausedRsp, "00010000")
	if p, err := DecodePaused(b); err != nil || p {
		t.Fatalf("octet 1 non nul : pause %v, %v", p, err)
	}
	b = openingResponse(t, capturedPausedRsp, "")
	binary.LittleEndian.PutUint16(b[4:], CmdCheckRevision)
	if _, err := DecodePaused(b); err == nil {
		t.Fatal("réponse d'une autre commande acceptée")
	}
	b = openingResponse(t, capturedPausedRsp, "")
	binary.LittleEndian.PutUint16(b[8:], FIBHeaderSize)
	if _, err := DecodePaused(b); err == nil {
		t.Fatal("réponse sans données acceptée")
	}
}

func TestDecodeRevision(t *testing.T) {
	rv, err := DecodeRevision(openingResponse(t, capturedRevisionRspHdr, capturedRevisionRsp))
	if err != nil || rv != (Revision{Compatible: true, FirmwareBuild: 19204}) || !rv.Accepted() {
		t.Fatalf("réponse capturée : %+v, %v", rv, err)
	}
	// Seuil écrit en clair (table de l'outil) : indépendant de la constante.
	for _, tc := range []struct {
		rv   Revision
		want bool
	}{
		{Revision{true, 2120}, true},
		{Revision{true, 2119}, false},
		{Revision{false, 19204}, false},
	} {
		if tc.rv.Accepted() != tc.want {
			t.Errorf("%+v : acceptée %v", tc.rv, !tc.want)
		}
	}
	// Premier mot à 2 : compatible (l'outil teste « non nul », pas « égal à 1 »).
	if rv, err := DecodeRevision(openingResponse(t, capturedRevisionRspHdr, "0200000000040305044b0000")); err != nil || !rv.Compatible {
		t.Fatalf("premier mot 2 : %+v, %v", rv, err)
	}
	b := openingResponse(t, capturedRevisionRspHdr, capturedRevisionRsp)
	binary.LittleEndian.PutUint16(b[8:], FIBHeaderSize+8)
	if _, err := DecodeRevision(b); err == nil {
		t.Fatal("réponse sans le build acceptée")
	}
}
