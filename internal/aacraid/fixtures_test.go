// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"encoding/binary"
	"fmt"
	"testing"
)

// Les données de référence sont pseudonymisées (UID de conteneurs, numéros
// de série, WWN, adresse SAS) : ce test vérifie qu'elles restent cohérentes
// entre elles, comme le sont les réponses d'une même carte. Il échoue si une
// valeur fictive n'a pas été reportée partout.
func TestReferenceFixturesConsistent(t *testing.T) {
	le := binary.LittleEndian
	nums, err := DecodeContainerList(fixture(t, "229-0"))
	if err != nil {
		t.Fatal(err)
	}
	disks, err := DecodeDiskList(fixture(t, "247-0"))
	if err != nil {
		t.Fatal(err)
	}
	present := map[uint32]bool{}
	for _, d := range disks {
		present[d] = true
	}
	infos := map[uint32]ContainerInfo{}
	for _, n := range nums {
		info, err := DecodeContainerInfo(fixture(t, "234-"+hexs(n)))
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := infos[info.UID]; dup {
			t.Fatalf("UID %#x en double", info.UID)
		}
		infos[info.UID] = info
	}
	members := map[uint32]bool{}
	for uid, info := range infos {
		ms, err := DecodeMemberList(fixture(t, "249-"+hexs(uid)))
		if err != nil {
			t.Fatalf("liste des membres de %#x : %v", uid, err)
		}
		if len(ms) != info.Members {
			t.Fatalf("%#x : %d membres annoncés, %d listés", uid, info.Members, len(ms))
		}
		for _, m := range ms {
			if m.Disk == NoUID {
				child, ok := infos[m.ChildUID]
				if !ok || child.Parent != uid {
					t.Fatalf("%#x : membre %#x inconnu ou de parent %#x", uid, m.ChildUID, child.Parent)
				}
				continue
			}
			if !present[m.Disk] || members[m.Disk] {
				t.Fatalf("%#x : disque %d absent de 247 ou membre deux fois", uid, m.Disk)
			}
			members[m.Disk] = true
		}
		if info.Parent != NoUID {
			if _, ok := infos[info.Parent]; !ok {
				t.Fatalf("%#x : parent %#x inconnu", uid, info.Parent)
			}
		}
	}
	raw := make([]byte, LargeFIBSize)
	copy(raw, fixture(t, "708"))
	cm, err := DecodeConnectorMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	cmSAS := raw[FIBHeaderSize+cmapEntryBase+8 : FIBHeaderSize+cmapEntryBase+16]
	serials := map[string]bool{}
	for _, n := range disks {
		d := fixture(t, fmt.Sprintf("248-%x", n))
		i, err := DecodeDiskInfo(d)
		if err != nil {
			t.Fatal(err)
		}
		if i.Number != n || le.Uint32(d[16:]) != n {
			t.Fatalf("248-%x désigne le disque %d", n, i.Number)
		}
		if serials[i.Serial] || i.Serial == "" {
			t.Fatalf("disque %d : numéro de série %q vide ou en double", n, i.Serial)
		}
		serials[i.Serial] = true
		if got := string(d[304:328]); got != fmt.Sprintf("%24s", i.Serial+"    ") {
			t.Fatalf("disque %d : seconde copie du numéro de série %q", n, got)
		}
		if d[191] != i.Phy {
			t.Fatalf("disque %d : phy %d à l'octet 191, %d à l'octet 200", n, d[191], i.Phy)
		}
		e, ok := cm.Locate(n)
		if !ok || e.Phy != i.Phy {
			t.Fatalf("disque %d : phy %d dans 248, entrée 708 %+v", n, i.Phy, e)
		}
		if string(d[192:200]) != string(cmSAS) {
			t.Fatalf("disque %d : adresse SAS de la carte différente entre 248 et 708", n)
		}
	}
	for i := range cm.Entries {
		e := raw[FIBHeaderSize+cmapEntryBase+i*cmapEntrySize:]
		if string(e[8:16]) != string(cmSAS) {
			t.Fatalf("708 : entrée %d avec une autre adresse SAS", i)
		}
	}
}
