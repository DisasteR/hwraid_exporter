// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import "testing"

// Configuration de référence : l'assemblage est « RAID_volume », les
// miroirs « 1 », comme l'affiche l'outil témoin.
func TestContainerLevelReference(t *testing.T) {
	cs, _ := referenceContainers(t)
	by := map[uint32]ContainerInfo{}
	for _, c := range cs {
		by[c.Info.UID] = c.Info
	}
	if l := ContainerLevel(cs[0], by); l != (Level{"concat", "RAID_volume", RedundancyAssembly}) {
		t.Fatalf("assemblage : %+v", l)
	}
	for _, c := range cs[1:] {
		if l := ContainerLevel(c, by); l != (Level{"raid1", "1", RedundancySingle}) {
			t.Fatalf("miroir %#x : %+v", c.Info.UID, l)
		}
	}
}

func diskMembers(n int) []Member {
	ms := make([]Member, n)
	for i := range ms {
		ms[i] = Member{Disk: uint32(i), ChildUID: NoUID, Rank: uint16(i)}
	}
	return ms
}

// Conversion de base, type par type, dans les deux numérotations (tableau
// de la section 6 bis de docs/aacraid-interface.md, recopié ici sans passer
// par les variables du code).
func TestContainerLevelTables(t *testing.T) {
	type want struct{ raid, vendor string }
	arcio := map[uint32]want{
		2: {"raid0", "0"}, 3: {"raid1", "1"}, 4: {"raid10", "10"}, 5: {"raid1e", "1E"},
		6: {"unknown", "type 6"}, 7: {"raid5", "5"}, 8: {"raid50", "50"}, 9: {"raid5", "5EE"}, 10: {"raid5", "5EE"},
		11: {"raid6", "6"}, 12: {"raid60", "60"}, 13: {"raid6", "6 Reed-Solomon"}, 14: {"raid60", "60 Reed-Solomon"},
		15: {"unknown", "type 15"}, 16: {"unknown", "type 16"}, 17: {"unknown", "type 17"}, 18: {"unknown", "type 18"},
		19: {"unknown", "type 19"}, 20: {"unknown", "type 20"},
	}
	classic := map[uint32]want{
		0: {"unknown", "classic type 0"}, 2: {"raid1", "1"}, 3: {"raid0", "0"}, 4: {"raid5", "5"},
		5: {"unknown", "classic type 5"}, 6: {"unknown", "classic type 6"}, 7: {"unknown", "classic type 7"},
		9: {"unknown", "classic type 9"}, 10: {"raid10", "10"}, 11: {"raid0", "0"}, 12: {"unknown", "classic type 12"}, // membres disques
		13: {"unknown", "classic type 13"}, 14: {"raid50", "50"}, 15: {"raid5", "5EE"}, 16: {"raid5", "5EE"},
		17: {"raid1e", "1E"}, 18: {"raid6", "6"}, 19: {"raid60", "60"}, 20: {"unknown", "classic type 20"},
	}
	for _, sel := range []struct {
		name string
		set  func(*ContainerInfo)
		tab  map[uint32]want
	}{
		{"ARCIO", func(*ContainerInfo) {}, arcio},
		{"classique (octets 96-99 = 3)", func(i *ContainerInfo) { i.SelectA = 3 }, classic},
		{"classique (octets 100-103 = 1)", func(i *ContainerInfo) { i.SelectB = 1 }, classic},
	} {
		for typ, w := range sel.tab {
			c := Container{Info: ContainerInfo{UID: 1, Type: typ, Parent: NoUID, Members: 8}, Members: diskMembers(8)}
			sel.set(&c.Info)
			l := ContainerLevel(c, map[uint32]ContainerInfo{1: c.Info})
			if l.Raid != w.raid || l.Vendor != w.vendor {
				t.Errorf("%s, type %d : %+v, attendu %+v", sel.name, typ, l, w)
			}
		}
	}
}

// Familles de tolérance : elles décideront du calcul de l'état.
func TestContainerLevelRedundancy(t *testing.T) {
	for typ, r := range map[uint32]Redundancy{2: RedundancyNone, 3: RedundancySingle, 4: RedundancySingle, 5: RedundancySingle,
		7: RedundancySingle, 8: RedundancySingle, 9: RedundancySingle, 11: RedundancyDouble, 12: RedundancyDouble,
		13: RedundancyDouble, 14: RedundancyDouble, 6: RedundancyUnknown} {
		c := Container{Info: ContainerInfo{UID: 1, Type: typ, Parent: NoUID, Members: 8}, Members: diskMembers(8)}
		if l := ContainerLevel(c, nil); l.Redundancy != r {
			t.Errorf("type %d : %+v, attendu %d", typ, l, r)
		}
	}
}

// Volumes 0 et 1 (ARCIO) : simple sous deux membres, étendu à partir de
// deux, assemblage si un membre est un conteneur lu dans le cycle.
func TestContainerLevelVolumes(t *testing.T) {
	child := ContainerInfo{UID: 2, Type: 3, Parent: 1, Members: 2}
	for _, typ := range []uint32{0, 1} {
		one := Container{Info: ContainerInfo{UID: 1, Type: typ, Parent: NoUID, Members: 1}, Members: diskMembers(1)}
		two := Container{Info: ContainerInfo{UID: 1, Type: typ, Parent: NoUID, Members: 2}, Members: diskMembers(2)}
		asm := Container{Info: ContainerInfo{UID: 1, Type: typ, Parent: NoUID, Members: 2}, Members: []Member{{Disk: NoUID, ChildUID: 2}, {Disk: NoUID, ChildUID: 3, Rank: 1}}}
		child3 := child
		child3.UID = 3
		by := map[uint32]ContainerInfo{1: asm.Info, 2: child, 3: child3}
		for _, c := range []struct {
			c    Container
			by   map[uint32]ContainerInfo
			want Level
		}{
			{one, nil, Level{"single", "Simple_volume", RedundancyNone}},
			{two, nil, Level{"concat", "Spanned_volume", RedundancyNone}},
			{asm, by, Level{"concat", "RAID_volume", RedundancyAssembly}},
			// Membre conteneur absent de la liste des conteneurs : ni
			// assemblage, ni volume de disques.
			{asm, map[uint32]ContainerInfo{1: asm.Info}, Level{"unknown", "type " + string(rune('0'+typ)), RedundancyUnknown}},
			{asm, map[uint32]ContainerInfo{1: asm.Info, 2: child}, Level{"concat", "RAID_volume", RedundancyAssembly}}, // un membre conteneur lu suffit
			// Assemblage mixte (un disque et un conteneur lu) : assemblage
			// pour l'outil de référence.
			{Container{Info: ContainerInfo{UID: 1, Type: typ, Parent: NoUID, Members: 2}, Members: []Member{{Disk: 0, ChildUID: NoUID}, {Disk: NoUID, ChildUID: 2, Rank: 1}}}, by, Level{"concat", "RAID_volume", RedundancyAssembly}},
		} {
			if l := ContainerLevel(c.c, c.by); l != c.want {
				t.Errorf("type %d, %+v : %+v, attendu %+v", typ, c.c.Members, l, c.want)
			}
		}
	}
	// En numérotation classique (types 1 et 8), un membre conteneur ne fait
	// pas un assemblage : niveau non établi. Un disque : volume simple.
	for _, typ := range []uint32{1, 8} {
		asm := Container{Info: ContainerInfo{UID: 1, Type: typ, Parent: NoUID, Members: 1, SelectA: 3}, Members: []Member{{Disk: NoUID, ChildUID: 2}}}
		if l := ContainerLevel(asm, map[uint32]ContainerInfo{1: asm.Info, 2: child}); l.Raid != "unknown" || l.Vendor != "classic type "+string(rune('0'+typ)) {
			t.Fatalf("classique %d, un membre conteneur : %+v", typ, l)
		}
		one := Container{Info: ContainerInfo{UID: 1, Type: typ, Parent: NoUID, Members: 1, SelectA: 3}, Members: diskMembers(1)}
		if l := ContainerLevel(one, nil); l != (Level{"single", "Simple_volume", RedundancyNone}) {
			t.Fatalf("classique %d, un disque : %+v", typ, l)
		}
		two := Container{Info: ContainerInfo{UID: 1, Type: typ, Parent: NoUID, Members: 2, SelectB: 1}, Members: diskMembers(2)}
		if l := ContainerLevel(two, nil); l != (Level{"concat", "Spanned_volume", RedundancyNone}) {
			t.Fatalf("classique %d, deux membres : %+v", typ, l)
		}
	}
}

// Reed-Solomon en numérotation classique : bit 0x4 de l'octet 89.
func TestContainerLevelClassicReedSolomon(t *testing.T) {
	for typ, vendor := range map[uint32]string{18: "6 Reed-Solomon", 19: "60 Reed-Solomon"} {
		c := Container{Info: ContainerInfo{UID: 1, Type: typ, Parent: NoUID, Members: 8, SelectB: 1, Visible: 0x4 << 8}, Members: diskMembers(8)}
		raid := map[uint32]string{18: "raid6", 19: "raid60"}[typ]
		if l := ContainerLevel(c, nil); l.Vendor != vendor || l.Raid != raid || l.Redundancy != RedundancyDouble {
			t.Errorf("type %d : %+v", typ, l)
		}
		c.Info.Visible = 0x8 << 8
		if l := ContainerLevel(c, nil); l.Vendor != vendor[:len(vendor)-len(" Reed-Solomon")] {
			t.Errorf("type %d, bit voisin : %+v", typ, l)
		}
	}
}

// Migration : niveau inconnu, que le conteneur la signale lui-même ou qu'un
// autre conteneur le désigne (numérotation ARCIO, sans parent).
func TestContainerLevelMigration(t *testing.T) {
	base := ContainerInfo{UID: 1, Type: 3, Parent: NoUID, Members: 2}
	c := Container{Info: base, Members: diskMembers(2)}
	self := base
	self.Task = 3
	if l := ContainerLevel(Container{Info: self, Members: c.Members}, nil); l.Raid != "unknown" {
		t.Fatalf("migration propre : %+v", l)
	}
	for _, slot := range []int{0, 1} {
		other := ContainerInfo{UID: 9, Type: 3, Parent: NoUID, Members: 2, Task: 3}
		if slot == 0 {
			other.MigrationA = 1
		} else {
			other.MigrationB = 1
		}
		by := map[uint32]ContainerInfo{1: base, 9: other}
		if l := ContainerLevel(c, by); l.Raid != "unknown" {
			t.Fatalf("désigné aux octets %d : %+v", 224+4*slot, l)
		}
		// Sans tâche 3 chez l'autre, avec un parent, ou en numérotation
		// classique : pas de migration.
		o2 := other
		o2.Task = 0
		if l := ContainerLevel(c, map[uint32]ContainerInfo{1: base, 9: o2}); l.Raid != "raid1" {
			t.Fatalf("autre sans migration : %+v", l)
		}
		withParent := c
		withParent.Info.Parent = 9
		if l := ContainerLevel(withParent, by); l.Raid != "raid1" {
			t.Fatalf("avec parent : %+v", l)
		}
		classic := c
		classic.Info.SelectA = 3
		classic.Info.Type = 2
		if l := ContainerLevel(classic, by); l.Raid != "raid1" {
			t.Fatalf("classique : %+v", l)
		}
	}
}

// Nombre minimal de membres (table de création de l'outil de référence,
// recopiée ici) : en dessous, la topologie contredit le type et le niveau
// n'est pas établi ; au minimum, il l'est.
func TestContainerLevelMinimumMembers(t *testing.T) {
	for typ, min := range map[uint32]int{2: 2, 3: 2, 4: 4, 5: 3, 7: 3, 8: 6, 9: 4, 10: 4, 11: 4, 12: 8, 13: 4, 14: 8} {
		for _, n := range []int{min - 1, min} {
			c := Container{Info: ContainerInfo{UID: 1, Type: typ, Parent: NoUID, Members: n}, Members: diskMembers(n)}
			l := ContainerLevel(c, nil)
			if (l.Raid == "unknown") != (n < min) {
				t.Errorf("type %d, %d membres : %+v", typ, n, l)
			}
		}
	}
	// Volume étendu : deux membres au moins ; un seul fait un volume simple.
	asm := Container{Info: ContainerInfo{UID: 1, Type: 1, Parent: NoUID, Members: 1}, Members: []Member{{Disk: NoUID, ChildUID: 2}}}
	if l := ContainerLevel(asm, map[uint32]ContainerInfo{2: {UID: 2, Type: 3, Parent: 1, Members: 2}}); l.Raid != "unknown" {
		t.Errorf("assemblage d'un seul conteneur : %+v", l)
	}
}

// Miroir classique en migration propre : niveau non établi, libellé
// « classic type 2 ».
func TestContainerLevelClassicMigration(t *testing.T) {
	c := Container{Info: ContainerInfo{UID: 1, Type: 2, Parent: NoUID, Members: 2, SelectB: 1, Task: 3}, Members: diskMembers(2)}
	if l := ContainerLevel(c, nil); l != (Level{"unknown", "classic type 2", RedundancyUnknown}) {
		t.Fatalf("%+v", l)
	}
}

// Membres incompatibles avec le niveau : tout membre conteneur, lu ou non,
// hors assemblage (y compris les RAID 10 et 50 classiques sur grappes, forme
// non observée).
func TestContainerLevelMemberKinds(t *testing.T) {
	ctr := func(uids ...uint32) []Member {
		ms := make([]Member, len(uids))
		for i, u := range uids {
			ms[i] = Member{Disk: NoUID, ChildUID: u, Rank: uint16(i)}
		}
		return ms
	}
	grappes := map[uint32]ContainerInfo{2: {UID: 2, Type: 3, Parent: 1, Members: 2}, 3: {UID: 3, Type: 3, Parent: 1, Members: 2}}
	for _, c := range []struct {
		name string
		info ContainerInfo
		ms   []Member
		by   map[uint32]ContainerInfo
		raid string
	}{
		{"RAID 6 ARCIO sur conteneurs introuvables", ContainerInfo{Type: 11, Members: 4}, ctr(7, 8, 9, 10), nil, "unknown"},
		{"RAID 6 ARCIO sur conteneurs lus", ContainerInfo{Type: 11, Members: 4}, ctr(2, 3, 2, 3), grappes, "unknown"},
		{"RAID 5 ARCIO mêlant disques et conteneur", ContainerInfo{Type: 7, Members: 3}, append(diskMembers(2), Member{Disk: NoUID, ChildUID: 2, Rank: 2}), grappes, "unknown"},
		{"RAID 10 ARCIO sur grappes", ContainerInfo{Type: 4, Members: 2}, ctr(2, 3), grappes, "unknown"},
		{"RAID 10 classique sur deux miroirs lus", ContainerInfo{Type: 10, Members: 2, SelectA: 3}, ctr(2, 3), grappes, "unknown"},
		{"RAID 50 classique sur deux grappes lues", ContainerInfo{Type: 14, Members: 2, SelectB: 1}, ctr(2, 3), grappes, "unknown"},
		{"RAID 10 classique qui se désigne lui-même", ContainerInfo{Type: 10, Members: 2, SelectA: 3}, ctr(1, 2), map[uint32]ContainerInfo{1: {UID: 1}, 2: grappes[2]}, "unknown"},
		{"RAID 10 classique, une grappe introuvable", ContainerInfo{Type: 10, Members: 2, SelectA: 3}, ctr(2, 9), grappes, "unknown"},
		{"RAID 10 classique sur une seule grappe", ContainerInfo{Type: 10, Members: 1, SelectA: 3}, ctr(2), grappes, "unknown"},
		{"RAID 10 classique sur quatre disques", ContainerInfo{Type: 10, Members: 4, SelectA: 3}, diskMembers(4), nil, "raid10"},
		{"RAID 5 classique sur conteneurs lus", ContainerInfo{Type: 4, Members: 2, SelectA: 3}, ctr(2, 3), grappes, "unknown"},
		{"RAID 5 ARCIO, trois disques et un conteneur", ContainerInfo{Type: 7, Members: 4}, append(diskMembers(3), Member{Disk: NoUID, ChildUID: 2, Rank: 3}), grappes, "unknown"},
		{"RAID 10 classique, une grappe sur trois introuvable", ContainerInfo{Type: 10, Members: 3, SelectA: 3}, ctr(2, 3, 9), grappes, "unknown"},
		{"type ARCIO 10 (5EE) sur grappes lues", ContainerInfo{Type: 10, Members: 2}, ctr(2, 3), grappes, "unknown"},
	} {
		c.info.UID, c.info.Parent = 1, NoUID
		if l := ContainerLevel(Container{Info: c.info, Members: c.ms}, c.by); l.Raid != c.raid {
			t.Errorf("%s : %+v", c.name, l)
		}
	}
}

// Assemblage : il faut un membre qui soit un autre conteneur lu dans le
// cycle. Un conteneur qui se désigne lui-même n'en est pas un ; le type
// classique 12 (volume de miroirs) fait de disques non plus.
func TestContainerLevelAssemblyCoherence(t *testing.T) {
	self := Container{Info: ContainerInfo{UID: 1, Type: 1, Parent: NoUID, Members: 2},
		Members: []Member{{Disk: NoUID, ChildUID: 1}, {Disk: NoUID, ChildUID: 1, Rank: 1}}}
	if l := ContainerLevel(self, map[uint32]ContainerInfo{1: self.Info}); l.Raid != "unknown" {
		t.Errorf("assemblage de lui-même : %+v", l)
	}
	mirrors := map[uint32]ContainerInfo{2: {UID: 2, Type: 3, Parent: 1, Members: 2}, 3: {UID: 3, Type: 3, Parent: 1, Members: 2}}
	c12 := Container{Info: ContainerInfo{UID: 1, Type: 12, Parent: NoUID, Members: 2, SelectA: 3},
		Members: []Member{{Disk: NoUID, ChildUID: 2}, {Disk: NoUID, ChildUID: 3, Rank: 1}}}
	if l := ContainerLevel(c12, mirrors); l != (Level{"concat", "RAID_volume", RedundancyAssembly}) {
		t.Errorf("type classique 12 sur miroirs : %+v", l)
	}
	c12.Members = []Member{{Disk: NoUID, ChildUID: 1}, {Disk: NoUID, ChildUID: 1, Rank: 1}}
	if l := ContainerLevel(c12, map[uint32]ContainerInfo{1: c12.Info}); l.Raid != "unknown" {
		t.Errorf("type classique 12 de lui-même : %+v", l)
	}
	c12.Members = diskMembers(2)
	if l := ContainerLevel(c12, mirrors); l.Raid != "unknown" {
		t.Errorf("type classique 12 sur disques : %+v", l)
	}
}
