// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import "testing"

// vol construit un volume ARCIO de type typ sur les disques 0..n-1, avec
// les drapeaux donnés par membre (rang = position).
func vol(uid, typ uint32, flags ...uint32) Container {
	ms := make([]Member, len(flags))
	for i, f := range flags {
		ms[i] = Member{Disk: uid*100 + uint32(i), ChildUID: NoUID, Rank: uint16(i), Flags: f}
	}
	return Container{Info: ContainerInfo{UID: uid, Type: typ, Parent: NoUID, Members: len(flags)}, Members: ms}
}

func presentOf(cs ...Container) []uint32 {
	var d []uint32
	for _, c := range cs {
		for _, m := range c.Members {
			if m.Disk != NoUID {
				d = append(d, m.Disk)
			}
		}
	}
	return d
}

const (
	ok   = 0
	rbd  = MemberRebuilding
	dead = MemberDead
)

// Tolérance par famille (section 6 bis), états attendus recopiés de la
// table de la documentation. Types ARCIO : 2 RAID 0, 3 RAID 1, 7 RAID 5,
// 11 RAID 6.
func TestEvaluateTolerance(t *testing.T) {
	for _, c := range []struct {
		name  string
		v     Container
		state VolumeState
	}{
		{"RAID 0 intact", vol(1, 2, ok, ok), VolumeOptimal},
		{"RAID 0, un mort", vol(1, 2, ok, dead), VolumeFailed},
		{"RAID 0, une reconstruction", vol(1, 2, rbd, ok), VolumeFailed},
		{"RAID 1, un mort", vol(1, 3, dead, ok), VolumeDegraded},
		{"RAID 1, une reconstruction", vol(1, 3, ok, rbd), VolumeDegradedRebuilding},
		{"RAID 1, deux morts", vol(1, 3, dead, dead), VolumeFailed},
		{"RAID 5 intact", vol(1, 7, ok, ok, ok), VolumeOptimal},
		{"RAID 5, un mort", vol(1, 7, ok, dead, ok), VolumeDegraded},
		{"RAID 5, deux morts", vol(1, 7, dead, ok, dead), VolumeFailed},
		{"RAID 6, un mort", vol(1, 11, ok, ok, dead, ok), VolumeReducedRedundancy},
		{"RAID 6, une reconstruction", vol(1, 11, rbd, ok, ok, ok), VolumeReducedRedundancyRebuilding},
		{"RAID 6, deux morts", vol(1, 11, dead, ok, dead, ok), VolumeDegraded},
		{"RAID 6, un mort et une reconstruction", vol(1, 11, dead, rbd, ok, ok), VolumeDegraded}, // un mort reste à remplacer
		{"RAID 6, deux reconstructions", vol(1, 11, rbd, rbd, ok, ok), VolumeDegradedRebuilding},
		{"RAID 10, deux morts (grappes ignorées)", vol(1, 4, dead, ok, dead, ok), VolumeFailed},
		{"RAID 50, deux morts", vol(1, 8, dead, ok, ok, dead, ok, ok), VolumeFailed},
		{"RAID 50, un mort", vol(1, 8, dead, ok, ok, ok, ok, ok), VolumeDegraded},
		{"RAID 6, trois morts", vol(1, 11, dead, dead, dead, ok), VolumeFailed},
		{"drapeau inconnu", vol(1, 7, ok, 0x10, ok), VolumeUnknown},
		{"type sans niveau", vol(1, 6, ok, ok), VolumeUnknown},
	} {
		if st := Evaluate([]Container{c.v}, presentOf(c.v), nil).Volumes[1]; st != c.state {
			t.Errorf("%s : %s, attendu %s", c.name, st, c.state)
		}
	}
}

// Écart retenu : un membre en reconstruction compte toujours parmi les
// membres perdus, quel que soit son rang (l'outil de référence ne le
// compterait que s'il est parcouru avant le membre mort).
func TestEvaluateRebuildingAlwaysCounts(t *testing.T) {
	for _, v := range []Container{vol(1, 3, rbd, dead), vol(1, 3, dead, rbd), vol(1, 7, rbd, ok, dead), vol(1, 7, dead, ok, rbd)} {
		if st := Evaluate([]Container{v}, presentOf(v), nil).Volumes[1]; st != VolumeFailed {
			t.Errorf("%+v : %s", v.Members, st)
		}
	}
}

// Inhibition de l'outil de référence : tâche 7 ou 8 et octets 128-131 non
// nuls, le membre en reconstruction n'est pas compté.
func TestEvaluateRebuildInhibited(t *testing.T) {
	for _, c := range []struct {
		task, active uint32
		state        VolumeState
	}{
		{7, 1, VolumeOptimal}, {8, 2, VolumeOptimal}, {7, 0, VolumeDegradedRebuilding}, {9, 1, VolumeDegradedRebuilding},
	} {
		v := vol(1, 7, rbd, ok, ok)
		v.Info.Task, v.Info.TaskActive = c.task, c.active
		if st := Evaluate([]Container{v}, presentOf(v), nil).Volumes[1]; st != c.state {
			t.Errorf("tâche %d, actif %d : %s, attendu %s", c.task, c.active, st, c.state)
		}
	}
	// Inhibé, mais avec un membre mort : seul le mort compte, et la
	// variante « en reconstruction » s'efface (un membre reste à remplacer).
	v := vol(1, 7, rbd, dead, ok)
	v.Info.Task, v.Info.TaskActive = 8, 1
	if st := Evaluate([]Container{v}, presentOf(v), nil).Volumes[1]; st != VolumeDegraded {
		t.Errorf("inhibé avec un mort : %s", st)
	}
}

// Disque absent ou déclaré en panne par son information (248) : le membre
// est perdu, même s'il porte le drapeau de reconstruction ; le disque est
// publié en panne.
func TestEvaluateDiskFailedOrAbsent(t *testing.T) {
	v := vol(1, 3, ok, rbd)
	ev := Evaluate([]Container{v}, []uint32{100}, nil) // disque 101 absent
	if ev.Volumes[1] != VolumeDegraded || ev.Disks[101] != DiskFailed {
		t.Errorf("disque en reconstruction absent : %s, disque %s", ev.Volumes[1], ev.Disks[101])
	}
	ev = Evaluate([]Container{v}, presentOf(v), map[uint32]bool{101: true})
	if ev.Volumes[1] != VolumeDegraded || ev.Disks[101] != DiskFailed {
		t.Errorf("disque en reconstruction déclaré en panne : %s, disque %s", ev.Volumes[1], ev.Disks[101])
	}
	v = vol(1, 3, ok, ok)
	ev = Evaluate([]Container{v}, presentOf(v), map[uint32]bool{100: true})
	if ev.Volumes[1] != VolumeDegraded || ev.Disks[100] != DiskFailed || ev.Disks[101] != DiskOnline {
		t.Errorf("disque déclaré en panne : %+v", ev)
	}
	// Panne déclarée pour un autre disque : sans effet.
	if st := Evaluate([]Container{v}, presentOf(v), map[uint32]bool{7: true}).Volumes[1]; st != VolumeOptimal {
		t.Errorf("panne d'un disque étranger : %s", st)
	}
	// Deux membres sur le même disque : indéterminé.
	v.Members[1].Disk = 100
	if st := Evaluate([]Container{v}, []uint32{100}, nil).Volumes[1]; st != VolumeUnknown {
		t.Errorf("disque en double : %s", st)
	}
}

// asm assemble les conteneurs donnés, dans l'ordre de la liste, avec les
// rangs donnés.
func asm(uid uint32, children []Container, ranks ...uint16) []Container {
	a := Container{Info: ContainerInfo{UID: uid, Type: 1, Parent: NoUID, Members: len(children)}}
	out := []Container{a}
	for i, c := range children {
		c.Info.Parent = uid
		out = append(out, c)
		out[0].Members = append(out[0].Members, Member{Disk: NoUID, ChildUID: c.Info.UID, Rank: ranks[i]})
	}
	return out
}

func mirror(uid uint32, flags ...uint32) Container { return vol(uid, 3, flags...) }

// Assemblage : l'état le plus grave de ses membres, quel que soit leur rang
// (écart prudent avec la fusion par rang de l'outil de référence).
func TestEvaluateAssemblyMerge(t *testing.T) {
	healthy := mirror(2, ok, ok)
	degraded := mirror(3, dead, ok)
	rebuilding := mirror(4, ok, rbd)
	failed := mirror(5, dead, dead)
	raid6 := vol(6, 11, dead, ok, ok, ok) // redondance réduite
	for _, c := range []struct {
		name     string
		children []Container
		ranks    []uint16
		state    VolumeState
	}{
		{"tout optimal", []Container{healthy, mirror(7, ok, ok)}, []uint16{0, 1}, VolumeOptimal},
		{"un dégradé", []Container{healthy, degraded}, []uint16{0, 1}, VolumeDegraded},
		{"reconstruction puis dégradé", []Container{rebuilding, degraded}, []uint16{0, 1}, VolumeDegraded},
		{"dégradé puis reconstruction (écart retenu)", []Container{rebuilding, degraded}, []uint16{1, 0}, VolumeDegraded},
		{"dégradé puis reconstruction, ordre de liste inversé", []Container{degraded, rebuilding}, []uint16{0, 1}, VolumeDegraded},
		{"échec puis reconstruction (écart retenu)", []Container{failed, rebuilding}, []uint16{0, 1}, VolumeFailed},
		{"reconstruction puis échec", []Container{failed, rebuilding}, []uint16{1, 0}, VolumeFailed},
		{"redondance réduite seule", []Container{healthy, raid6}, []uint16{0, 1}, VolumeReducedRedundancy},
		{"redondance réduite après dégradé", []Container{degraded, raid6}, []uint16{0, 1}, VolumeDegraded},
		{"redondance réduite et redondance réduite en reconstruction", []Container{raid6, vol(8, 11, rbd, ok, ok, ok)}, []uint16{1, 0}, VolumeReducedRedundancy},
	} {
		cs := asm(1, c.children, c.ranks...)
		if st := Evaluate(cs, presentOf(cs...), nil).Volumes[1]; st != c.state {
			t.Errorf("%s : %s, attendu %s", c.name, st, c.state)
		}
	}
}

// Assemblage indéterminé : membre indéterminé, parent incohérent, membre en
// double, membre disque, boucle.
func TestEvaluateAssemblyUnknown(t *testing.T) {
	cs := asm(1, []Container{mirror(2, ok, ok), mirror(3, ok, 0x10)}, 0, 1)
	if st := Evaluate(cs, presentOf(cs...), nil).Volumes[1]; st != VolumeUnknown {
		t.Errorf("membre indéterminé : %s", st)
	}
	cs = asm(1, []Container{mirror(2, ok, ok), mirror(3, ok, ok)}, 0, 1)
	cs[2].Info.Parent = 9
	if st := Evaluate(cs, presentOf(cs...), nil).Volumes[1]; st != VolumeUnknown {
		t.Errorf("parent incohérent : %s", st)
	}
	cs = asm(1, []Container{mirror(2, ok, ok), mirror(3, ok, ok)}, 0, 1)
	cs[0].Members[1].ChildUID = 2
	if st := Evaluate(cs, presentOf(cs...), nil).Volumes[1]; st != VolumeUnknown {
		t.Errorf("membre en double : %s", st)
	}
	cs = asm(1, []Container{mirror(2, ok, ok), mirror(3, ok, ok)}, 0, 1)
	cs[0].Members[1] = Member{Disk: 42, ChildUID: NoUID, Rank: 1}
	if st := Evaluate(cs, append(presentOf(cs...), 42), nil).Volumes[1]; st != VolumeUnknown {
		t.Errorf("membre disque : %s", st)
	}
	// Deux assemblages qui se désignent mutuellement, parentés cohérentes
	// (seule la détection de boucle les rend indéterminés).
	a := Container{Info: ContainerInfo{UID: 1, Type: 1, Parent: 2, Members: 2},
		Members: []Member{{Disk: NoUID, ChildUID: 2}, {Disk: NoUID, ChildUID: 3, Rank: 1}}}
	b := Container{Info: ContainerInfo{UID: 2, Type: 1, Parent: 1, Members: 2},
		Members: []Member{{Disk: NoUID, ChildUID: 1}, {Disk: NoUID, ChildUID: 4, Rank: 1}}}
	m3, m4 := mirror(3, ok, ok), mirror(4, ok, ok)
	m3.Info.Parent, m4.Info.Parent = 1, 2
	ev := Evaluate([]Container{a, b, m3, m4}, presentOf(m3, m4), nil)
	if ev.Volumes[1] != VolumeUnknown || ev.Volumes[2] != VolumeUnknown {
		t.Errorf("boucle : %+v", ev.Volumes)
	}
}

// Un membre indéterminé : l'état connu l'emporte s'il exige déjà une action
// (« en échec », ou « dégradé », variante « en reconstruction » retirée),
// dans les deux ordres de liste et de rang ; sinon indéterminé.
func TestEvaluateAssemblyKnownBeatsUnknown(t *testing.T) {
	unk := mirror(3, ok, 0x10)
	for _, c := range []struct {
		name  string
		known Container
		state VolumeState
	}{
		{"en échec", mirror(2, dead, dead), VolumeFailed},
		{"dégradé", mirror(2, dead, ok), VolumeDegraded},
		{"en reconstruction", mirror(2, rbd, ok), VolumeDegraded},
		{"optimal", mirror(2, ok, ok), VolumeUnknown},
		{"redondance réduite", vol(2, 11, dead, ok, ok, ok), VolumeUnknown},
	} {
		for _, order := range [][]Container{{c.known, unk}, {unk, c.known}} {
			for _, ranks := range [][]uint16{{0, 1}, {1, 0}} {
				cs := asm(1, order, ranks...)
				if st := Evaluate(cs, presentOf(cs...), nil).Volumes[1]; st != c.state {
					t.Errorf("%s, liste %d %d, rangs %v : %s, attendu %s", c.name, order[0].Info.UID, order[1].Info.UID, ranks, st, c.state)
				}
			}
		}
	}
}

// Même règle dans un volume de disques : deux pertes avérées sur un RAID 5
// le mettent en échec malgré un troisième membre au drapeau inconnu.
func TestEvaluateDiskVolumeKnownBeatsUnknown(t *testing.T) {
	for _, c := range []struct {
		name   string
		v      Container
		failed map[uint32]bool
		state  VolumeState
	}{
		{"RAID 5, deux disques en panne, un inconnu", vol(1, 7, ok, ok, 0x10), map[uint32]bool{100: true, 101: true}, VolumeFailed},
		{"RAID 5, deux inconnus en dernier, deux morts", vol(1, 7, dead, dead, 0x10, 0x10), nil, VolumeFailed},
		{"RAID 5, un mort, un inconnu", vol(1, 7, 0x10, dead, ok), nil, VolumeDegraded},
		{"RAID 5, une reconstruction, un inconnu", vol(1, 7, rbd, 0x10, ok), nil, VolumeDegraded},
		{"RAID 5, un inconnu seul", vol(1, 7, ok, 0x10, ok), nil, VolumeUnknown},
		{"RAID 6, un mort, un inconnu", vol(1, 11, dead, 0x10, ok, ok), nil, VolumeUnknown},
		{"RAID 0, un inconnu", vol(1, 2, ok, 0x10), nil, VolumeUnknown},
	} {
		if st := Evaluate([]Container{c.v}, presentOf(c.v), c.failed).Volumes[1]; st != c.state {
			t.Errorf("%s : %s, attendu %s", c.name, st, c.state)
		}
	}
}

// Assemblages imbriqués valides : l'état remonte à travers les niveaux.
func TestEvaluateNestedAssembly(t *testing.T) {
	inner := asm(10, []Container{mirror(11, ok, ok), mirror(12, dead, ok)}, 0, 1)
	outer := asm(20, []Container{inner[0], mirror(21, ok, ok)}, 0, 1)
	cs := append(outer, inner[1:]...)
	ev := Evaluate(cs, presentOf(cs...), nil)
	if ev.Volumes[10] != VolumeDegraded || ev.Volumes[20] != VolumeDegraded {
		t.Errorf("%+v", ev.Volumes)
	}
}

// Seuls les liens d'assemblage forment une boucle : un conteneur de disques
// qui désigne à tort un assemblage ne masque pas l'échec de ce dernier.
func TestEvaluateFalseLoop(t *testing.T) {
	// bad, membre de l'assemblage 1, se déclare RAID 1 mais liste 1 comme
	// membre : sans lien d'assemblage de bad vers 1, pas de boucle ; bad est
	// indéterminé, 1 en échec par son miroir 3.
	bad := Container{Info: ContainerInfo{UID: 4, Type: 3, Members: 2},
		Members: []Member{{Disk: 400, ChildUID: NoUID}, {Disk: NoUID, ChildUID: 1, Rank: 1}}}
	cs := asm(1, []Container{mirror(2, ok, ok), mirror(3, dead, dead), bad}, 0, 1, 2)
	ev := Evaluate(cs, append(presentOf(cs...), 400), nil)
	if ev.Volumes[1] != VolumeFailed || ev.Volumes[4] != VolumeUnknown {
		t.Errorf("%+v", ev.Volumes)
	}
}

// Une preuve de perte (disque absent ou déclaré en panne) l'emporte sur un
// drapeau inconnu : le membre est perdu.
func TestEvaluateLossBeatsUnknownFlag(t *testing.T) {
	v := mirror(1, 0x10, ok)
	if st := Evaluate([]Container{v}, []uint32{101}, nil).Volumes[1]; st != VolumeDegraded {
		t.Errorf("disque absent, drapeau inconnu : %s", st)
	}
	if st := Evaluate([]Container{v}, presentOf(v), map[uint32]bool{100: true}).Volumes[1]; st != VolumeDegraded {
		t.Errorf("disque en panne, drapeau inconnu : %s", st)
	}
	v = mirror(1, 0x10, 0x10)
	if st := Evaluate([]Container{v}, nil, nil).Volumes[1]; st != VolumeFailed {
		t.Errorf("deux disques absents, drapeaux inconnus : %s", st)
	}
	if st := Evaluate([]Container{v}, presentOf(v), nil).Volumes[1]; st != VolumeUnknown {
		t.Errorf("drapeaux inconnus seuls : %s", st)
	}
}

// Libellés publiés, écrits en dur (vocabulaire de docs/metrics.md).
func TestVolumeStateLabels(t *testing.T) {
	for st, want := range map[VolumeState]string{VolumeOptimal: "optimal", VolumeDegraded: "degraded",
		VolumeDegradedRebuilding: "degraded_rebuilding", VolumeFailed: "failed", VolumeUnknown: "unknown",
		VolumeReducedRedundancy: "reduced_redundancy", VolumeReducedRedundancyRebuilding: "reduced_redundancy_rebuilding"} {
		if string(st) != want {
			t.Errorf("%q, attendu %q", st, want)
		}
	}
}

// Boucle d'assemblages dont l'un contient un miroir en échec : les deux
// sont indéterminés, quel que soit l'ordre des conteneurs dans le relevé.
func TestEvaluateCycleOrderIndependent(t *testing.T) {
	a := Container{Info: ContainerInfo{UID: 1, Type: 1, Parent: 2, Members: 2},
		Members: []Member{{Disk: NoUID, ChildUID: 2}, {Disk: NoUID, ChildUID: 3, Rank: 1}}}
	b := Container{Info: ContainerInfo{UID: 2, Type: 1, Parent: 1, Members: 2},
		Members: []Member{{Disk: NoUID, ChildUID: 1}, {Disk: NoUID, ChildUID: 4, Rank: 1}}}
	m3, m4 := mirror(3, dead, dead), mirror(4, ok, ok)
	m3.Info.Parent, m4.Info.Parent = 1, 2
	// Assemblage hors de la boucle : non touché.
	top := Container{Info: ContainerInfo{UID: 9, Type: 1, Parent: NoUID, Members: 2},
		Members: []Member{{Disk: NoUID, ChildUID: 5}, {Disk: NoUID, ChildUID: 6, Rank: 1}}}
	m5, m6 := mirror(5, ok, ok), mirror(6, ok, ok)
	m5.Info.Parent, m6.Info.Parent = 9, 9
	for _, order := range [][]Container{{a, b, m3, m4, top, m5, m6}, {b, a, m4, m3, m6, m5, top}} {
		ev := Evaluate(order, presentOf(m3, m4, m5, m6), nil)
		if ev.Volumes[1] != VolumeUnknown || ev.Volumes[2] != VolumeUnknown || ev.Volumes[3] != VolumeFailed || ev.Volumes[9] != VolumeOptimal {
			t.Errorf("ordre %d, %d : %+v", order[0].Info.UID, order[1].Info.UID, ev.Volumes)
		}
	}
}

// Un membre incohérent de l'assemblage (introuvable, mauvais parent, disque,
// en double) est traité comme indéterminé : il ne masque pas un miroir en
// échec, ni une perte qui rend l'assemblage dégradé.
func TestEvaluateAssemblyInconsistentMember(t *testing.T) {
	for _, c := range []struct {
		name  string
		known Container
		state VolumeState
	}{
		{"en échec", mirror(2, dead, dead), VolumeFailed},
		{"en reconstruction", mirror(2, rbd, ok), VolumeDegraded},
		{"optimal", mirror(2, ok, ok), VolumeUnknown},
	} {
		for _, bad := range []struct {
			name string
			m    Member
		}{
			{"introuvable", Member{Disk: NoUID, ChildUID: 77}},
			{"disque", Member{Disk: 42, ChildUID: NoUID}},
			{"en double", Member{Disk: NoUID, ChildUID: 2}},
		} {
			cs := asm(1, []Container{c.known}, 0)
			cs[0].Info.Members = 2
			bad.m.Rank = 1
			cs[0].Members = append(cs[0].Members, bad.m)
			if st := Evaluate(cs, append(presentOf(cs...), 42), nil).Volumes[1]; st != c.state {
				t.Errorf("%s et membre %s : %s, attendu %s", c.name, bad.name, st, c.state)
			}
		}
	}
}
