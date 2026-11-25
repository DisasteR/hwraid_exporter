// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"encoding/binary"
	"testing"
)

// Champs de la section 6 bis de docs/aacraid-interface.md sur les réponses
// de l'hôte de référence : tous nuls (numérotation ARCIO, aucune tâche),
// rangs de l'assemblage 0, 3, 1, 2 dans l'ordre reçu.
func TestReferenceSection6bisFields(t *testing.T) {
	cs, _ := referenceContainers(t)
	for _, c := range cs {
		i := c.Info
		if i.Visible|i.SelectA|i.SelectB|i.Task|i.TaskActive|i.MigrationA|i.MigrationB != 0 ||
			i.ClassicNumbering() || i.Migrating() || i.RebuildCountInhibited() {
			t.Fatalf("conteneur %#x : %+v", i.UID, i)
		}
	}
	wantRanks := map[uint32]uint16{0x1d000001: 0, 0x1d000004: 3, 0x1d000002: 1, 0x1d000003: 2}
	for i, m := range cs[0].Members {
		if r, ok := wantRanks[m.ChildUID]; !ok || m.Rank != r {
			t.Fatalf("entrée %d : %+v", i, m)
		}
	}
	if cs[0].Members[1].ChildUID != 0x1d000004 {
		t.Fatalf("ordre reçu modifié : %+v", cs[0].Members)
	}
	for _, c := range cs[1:] {
		if c.Members[0].Rank != 0 || c.Members[1].Rank != 1 {
			t.Fatalf("miroir %#x : %+v", c.Info.UID, c.Members)
		}
	}
}

// Chaque champ est lu à sa position : une valeur distincte posée à chaque
// position se retrouve dans le bon champ, et seulement là.
func TestDecodeContainerInfoSection6bisOffsets(t *testing.T) {
	d := fixture(t, "234-0")
	le := binary.LittleEndian
	// Valeurs sur quatre octets, toutes différentes : lire un seul octet,
	// ou un mot décalé, se voit.
	for off, v := range map[int]uint32{88: 0xb1c1d1e1, 96: 0xb2c2d2e2, 100: 0xb3c3d3e3, 120: 0xb4c4d4e4, 128: 0xb5c5d5e5, 224: 0xb6c6d6e6, 228: 0xb7c7d7e7} {
		le.PutUint32(d[off:], v)
	}
	i, err := DecodeContainerInfo(d)
	if err != nil {
		t.Fatal(err)
	}
	if i.Visible != 0xb1c1d1e1 || i.SelectA != 0xb2c2d2e2 || i.SelectB != 0xb3c3d3e3 || i.Task != 0xb4c4d4e4 ||
		i.TaskActive != 0xb5c5d5e5 || i.MigrationA != 0xb6c6d6e6 || i.MigrationB != 0xb7c7d7e7 {
		t.Fatalf("%+v", i)
	}
}

// Le rang occupe deux octets (l'octet 11 compte) et doit être, pour chaque
// entrée, distinct et inférieur au nombre d'entrées.
func TestDecodeMemberRanks(t *testing.T) {
	d := fixture(t, "249-1d000000")
	d[8+10], d[8+11] = 0x00, 0x01 // rang 256 : hors limites
	if _, err := DecodeMemberList(d); err == nil {
		t.Fatal("rang 256 accepté : l'octet 11 n'est pas lu")
	}
	for name, rank := range map[string]uint16{"rang égal au nombre": 4, "rang répété": 3} {
		d := fixture(t, "249-1d000000")
		binary.LittleEndian.PutUint16(d[8+10:], rank) // entrée 0 ; l'entrée 1 a le rang 3
		if _, err := DecodeMemberList(d); err == nil {
			t.Fatalf("%s accepté", name)
		}
	}
	d = fixture(t, "249-1d000000")
	binary.LittleEndian.PutUint16(d[8+10:], 3)
	binary.LittleEndian.PutUint16(d[8+32+10:], 0) // permutation valide
	if ms, err := DecodeMemberList(d); err != nil || ms[0].Rank != 3 || ms[1].Rank != 0 {
		t.Fatalf("permutation : %+v %v", ms, err)
	}
}

// Conditions de l'outil de référence, valeur par valeur.
func TestContainerInfoRules(t *testing.T) {
	for _, c := range []struct {
		i                        ContainerInfo
		classic, migr, inhibited bool
	}{
		{ContainerInfo{}, false, false, false},
		{ContainerInfo{SelectA: 3}, true, false, false},
		{ContainerInfo{SelectA: 1}, false, false, false},
		{ContainerInfo{SelectB: 1}, true, false, false},
		{ContainerInfo{SelectB: 3}, false, false, false},
		{ContainerInfo{Task: 3}, false, true, false},
		{ContainerInfo{Task: 3, TaskActive: 1}, false, true, false},
		{ContainerInfo{Task: 7, TaskActive: 1}, false, false, true},
		{ContainerInfo{Task: 8, TaskActive: 0xffffffff}, false, false, true},
		{ContainerInfo{Task: 7}, false, false, false},
		{ContainerInfo{Task: 9, TaskActive: 1}, false, false, false},
		{ContainerInfo{Task: 6, TaskActive: 1}, false, false, false},
	} {
		if c.i.ClassicNumbering() != c.classic || c.i.Migrating() != c.migr || c.i.RebuildCountInhibited() != c.inhibited {
			t.Fatalf("%+v : classique %v, migration %v, inhibé %v", c.i, c.i.ClassicNumbering(), c.i.Migrating(), c.i.RebuildCountInhibited())
		}
	}
}

// FeatureBits de la réponse 706 enregistrée : AAC_FEATURE_JBOD positionné,
// bit 16 de SupportedOptions2 nul.
func TestSupplementCapabilitiesRecorded(t *testing.T) {
	s, err := DecodeSupplementInfo(recorded(t))
	if err != nil {
		t.Fatal(err)
	}
	if s.FeatureBits != 0x0fff57df || !s.JBOD() || s.DirectDisks() {
		t.Fatalf("%+v JBOD=%v direct=%v", s, s.JBOD(), s.DirectDisks())
	}
	b := recorded(t)
	binary.LittleEndian.PutUint32(b[FIBHeaderSize+0x24:], ^uint32(FeatureJBOD))
	binary.LittleEndian.PutUint32(b[FIBHeaderSize+0x28:], FeatureJBOD) // champ voisin
	if s, err := DecodeSupplementInfo(b); err != nil || s.JBOD() {
		t.Fatalf("bit lu hors de FeatureBits : %+v %v", s, err)
	}
	for _, c := range []struct {
		s             SupplementInfo
		jbod, directD bool
	}{
		{SupplementInfo{Version: 2, FeatureBits: FeatureJBOD}, true, false},
		// Numéros de bit écrits en dur (documentation), pas les constantes.
		{SupplementInfo{Version: 2, SupportedOptions2: 1 << 16}, false, true},
		{SupplementInfo{Version: 2, FeatureBits: 1 << 27}, true, false},
		{SupplementInfo{Version: 2, SupportedOptions2: ^uint32(1 << 16), FeatureBits: ^uint32(1 << 27)}, false, false},
		{SupplementInfo{Version: 1, FeatureBits: FeatureJBOD, SupportedOptions2: SupportedDirectDisks}, false, false},
		{SupplementInfo{Version: 15, FeatureBits: ^uint32(FeatureJBOD), SupportedOptions2: ^uint32(SupportedDirectDisks)}, false, false},
	} {
		if c.s.JBOD() != c.jbod || c.s.DirectDisks() != c.directD {
			t.Fatalf("%+v : JBOD %v, direct %v", c.s, c.s.JBOD(), c.s.DirectDisks())
		}
	}
}

// Option HBA natif : bit 25 de SupportedOptions, nul sur la carte de
// référence.
func TestAdapterInfoNativeHBA(t *testing.T) {
	a, err := DecodeAdapterInfo(padFIB(t, capturedAdapterInfo))
	if err != nil {
		t.Fatal(err)
	}
	if a.NativeHBA() {
		t.Fatalf("%#x", a.SupportedOptions)
	}
	// Numéro de bit écrit en dur (documentation), pas la constante testée.
	if !(AdapterInfo{SupportedOptions: 1 << 25}).NativeHBA() || (AdapterInfo{SupportedOptions: ^uint32(1 << 25)}).NativeHBA() {
		t.Fatal("bit 25 mal testé")
	}
}

// Bornes exactes, écrites en dur : la 234 exige 312 octets (nom inclus),
// la 248 décode les octets d'état à partir de 336 octets.
func TestDecodeSizeBounds(t *testing.T) {
	c := fixture(t, "234-0")
	if _, err := DecodeContainerInfo(c[:311]); err == nil {
		t.Fatal("234 de 311 octets acceptée")
	}
	if _, err := DecodeContainerInfo(c[:312]); err != nil {
		t.Fatalf("234 de 312 octets : %v", err)
	}
	d := fixture(t, "248-0")
	if i, err := DecodeDiskInfo(d[:336]); err != nil || !i.HasStatus332 || i.Status68 != 0x70 {
		t.Fatalf("248 de 336 octets : %+v %v", i, err)
	}
	if i, err := DecodeDiskInfo(d[:296]); err != nil || i.HasStatus332 || i.Status68 != 0x70 {
		t.Fatalf("248 de 296 octets : %+v %v", i, err)
	}
}
