// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

// Requête 101 de l'outil sur l'hôte de référence (conteneur 0xfd) : en-tête
// sans les octets d'adresse ni l'octet 7, puis les mots 0 à 2 ; le reste des
// 0x48 octets n'est pas initialisé par l'outil. Réponse capturée, sans
// tâche : Size 40, données 0 et 1.
const (
	capturedTaskHeader = "01001000bc02010048000002"
	capturedTaskData   = "6500000000000000fd000000"
	capturedTaskRspHdr = "00000000bc02010028000002"
	capturedTaskRsp    = "0000000001000000"
)

func TestTaskRequestsMatchCapture(t *testing.T) {
	req := TaskListRequest(0xfd)
	if want := mustHex(t, capturedTaskHeader); !bytes.Equal(req[:12], want) {
		t.Fatalf("en-tête %x, capture %x", req[:12], want)
	}
	data := mustHex(t, capturedTaskData)
	if !bytes.Equal(req[FIBHeaderSize:FIBHeaderSize+len(data)], data) {
		t.Fatalf("données %x, capture %x", req[FIBHeaderSize:FIBHeaderSize+len(data)], data)
	}
	for i := 12; i < FIBSize; i++ {
		if (i < FIBHeaderSize || i >= FIBHeaderSize+len(data)) && req[i] != 0 {
			t.Fatalf("octet %d = %#x, attendu 0", i, req[i])
		}
	}
	// 103 : même forme, mot 0 = 103 (0x67), identifiant de tâche au mot 2.
	rep := TaskReportRequest(0x1234)
	want := append([]byte(nil), req...)
	binary.LittleEndian.PutUint32(want[FIBHeaderSize:], 0x67)
	binary.LittleEndian.PutUint32(want[FIBHeaderSize+8:], 0x1234)
	if !bytes.Equal(rep, want) {
		t.Fatal("requête 103 différente de la forme de la 101")
	}
	for _, b := range [][]byte{req, rep, TaskListRequest(1023)} {
		if err := CheckFIBRequest(b); err != nil {
			t.Fatalf("refusée par la liste blanche : %v", err)
		}
	}
}

func TestTaskRequestVariantsRefused(t *testing.T) {
	for name, b := range map[string][]byte{
		"conteneur au-delà de 1023": TaskListRequest(1024),
		"terminer une tâche (104)":  aifRequest(104, 1),
		"suspendre (105)":           aifRequest(105, 1),
		"liste complète (100)":      aifRequest(100, 0),
	} {
		if err := CheckFIBRequest(b); !errors.Is(err, ErrFIBNotAllowed) {
			t.Errorf("%s : acceptée (%v)", name, err)
		}
	}
	for name, mut := range map[string]func([]byte){
		"résidu":            func(b []byte) { b[FIBHeaderSize+12] = 1 },
		"mot 1":             func(b []byte) { b[FIBHeaderSize+4] = 1 },
		"octet 7":           func(b []byte) { b[7] = 0x30 },
		"sans 0x100000":     func(b []byte) { binary.LittleEndian.PutUint32(b, 1) },
		"taille 0x40":       func(b []byte) { binary.LittleEndian.PutUint16(b[8:], 0x40) },
		"adresse d'en-tête": func(b []byte) { b[16] = 1 },
	} {
		b := TaskListRequest(0xfd)
		mut(b)
		if err := CheckFIBRequest(b); !errors.Is(err, ErrFIBNotAllowed) {
			t.Errorf("%s : variante acceptée (%v)", name, err)
		}
	}
}

// taskRsp construit une réponse AifRequest : en-tête capturé, deux mots,
// puis les blocs.
func taskRsp(t *testing.T, w1 uint32, blocks ...[]byte) []byte {
	b := make([]byte, FIBSize)
	copy(b, mustHex(t, capturedTaskRspHdr))
	binary.LittleEndian.PutUint32(b[FIBHeaderSize+4:], w1)
	for i, blk := range blocks {
		copy(b[FIBHeaderSize+8+48*i:], blk)
	}
	binary.LittleEndian.PutUint16(b[8:], uint16(40+48*len(blocks)))
	return b
}

// block écrit un bloc aux positions lues par l'outil (valeurs en clair).
func block(job, typ, container, status uint32, final, current uint64) []byte {
	b := make([]byte, 48)
	le := binary.LittleEndian
	le.PutUint32(b[0:], job)
	le.PutUint32(b[4:], typ)
	le.PutUint32(b[8:], container)
	le.PutUint32(b[0x10:], status)
	le.PutUint64(b[0x18:], final)
	le.PutUint64(b[0x20:], current)
	return b
}

func TestDecodeTaskList(t *testing.T) {
	captured := make([]byte, FIBSize)
	copy(captured, mustHex(t, capturedTaskRspHdr))
	copy(captured[FIBHeaderSize:], mustHex(t, capturedTaskRsp))
	if ts, err := DecodeTaskList(captured); err != nil || len(ts) != 0 {
		t.Fatalf("réponse capturée : %v, %v", ts, err)
	}
	ts, err := DecodeTaskList(taskRsp(t, 1, block(7, 110, 0xfd, 102, 0, 0), block(9, 107, 0, 102, 0, 0)))
	if err != nil || len(ts) != 2 || ts[0].Job != 7 || ts[0].Type != 110 || ts[0].Container != 0xfd || ts[1].Job != 9 {
		t.Fatalf("deux tâches : %+v, %v", ts, err)
	}
	for name, b := range map[string][]byte{
		"erreur 4": taskRsp(t, 4),
		"erreur 5": taskRsp(t, 5),
		"taille hors blocs": func() []byte {
			b := taskRsp(t, 1, block(7, 110, 0xfd, 102, 0, 0))
			binary.LittleEndian.PutUint16(b[8:], 40+47)
			return b
		}(),
		"taille sous l'en-tête": func() []byte {
			b := taskRsp(t, 1)
			binary.LittleEndian.PutUint16(b[8:], 36)
			return b
		}(),
		"autre commande": func() []byte {
			b := taskRsp(t, 1)
			binary.LittleEndian.PutUint16(b[4:], 703)
			return b
		}(),
	} {
		if _, err := DecodeTaskList(b); err == nil {
			t.Errorf("%s : acceptée", name)
		}
	}
}

func TestDecodeTaskReport(t *testing.T) {
	rep, err := DecodeTaskReport(taskRsp(t, 1, block(7, 110, 0xfd, 102, 0x1_0000_0000, 0x4000_0000)), 7)
	if err != nil || rep.Final != 0x1_0000_0000 || rep.Current != 0x4000_0000 || rep.Status != 102 {
		t.Fatalf("rapport : %+v, %v", rep, err)
	}
	// Compteur courant au-delà de 32 bits : lu sur 64 bits, comme l'outil.
	if rep, err := DecodeTaskReport(taskRsp(t, 1, block(7, 110, 0xfd, 102, 0x4_0000_0000, 0x1_0000_0001)), 7); err != nil || rep.Current != 0x1_0000_0001 {
		t.Fatalf("courant 64 bits : %+v, %v", rep, err)
	}
	if _, err := DecodeTaskReport(taskRsp(t, 1, block(8, 110, 0xfd, 102, 10, 5)), 7); err == nil {
		t.Error("rapport d'une autre tâche accepté")
	}
	if _, err := DecodeTaskReport(taskRsp(t, 1), 7); err == nil {
		t.Error("rapport vide accepté")
	}
}

func TestTaskRatio(t *testing.T) {
	for _, tc := range []struct {
		name           string
		status         uint32
		final, current uint64
		ok             bool
		ratio          float64
	}{
		{"en cours", 102, 1000, 420, true, 0.42},
		{"en cours, compteurs au-delà de 32 bits", 102, 0x2_0000_0000, 0x1_0000_0000, true, 0.5},
		{"en cours, courant seul au-delà de 32 bits", 102, 0x4_0000_0000, 0x1_0000_0000, true, 0.25},
		{"suspendue", 101, 4, 1, true, 0.25},
		{"démarrée à 0", 102, 50, 0, true, 0},
		{"terminée (1)", 1, 0, 0, false, 0},
		{"terminée (2)", 2, 10, 3, false, 0},
		{"finalTick nul", 102, 0, 0, false, 0},
		{"currentTick au-delà", 102, 10, 11, false, 0},
		{"abandonnée", 3, 10, 5, false, 0},
		{"échouée", 4, 10, 5, false, 0},
		{"statut inconnu", 0, 10, 5, false, 0},
	} {
		r, ok, why := Task{Status: tc.status, Final: tc.final, Current: tc.current}.Ratio()
		if ok != tc.ok || math.Abs(r-tc.ratio) > 1e-12 || (!ok && why == "") {
			t.Errorf("%s : %v %v %q", tc.name, r, ok, why)
		}
	}
	// Terminée : série retirée (statuts 1 et 2 seulement).
	for st, want := range map[uint32]bool{1: true, 2: true, 3: false, 101: false, 102: false} {
		if (Task{Status: st}).Finished() != want {
			t.Errorf("statut %d : terminée %v", st, !want)
		}
	}
}

func TestTaskNames(t *testing.T) {
	for typ, want := range map[uint32]string{
		103: "rebuild", 106: "rebuild", 110: "rebuild", 115: "rebuild",
		105: "verify", 107: "verify", 116: "verify",
		101: "zero", 108: "migration", 112: "copyback", 113: "compaction", 114: "expansion", 117: "snapshot_backup",
		102: "other", 104: "other", 111: "other", 118: "other", 200: "other", 0: "other",
	} {
		if got := (Task{Type: typ}).Name(); got != want {
			t.Errorf("type %d : %s, attendu %s", typ, got, want)
		}
	}
}
