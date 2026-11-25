// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

// Calcul des états (docs/aacraid-interface.md, sections 6 et 6 bis) : règle
// de tolérance de l'outil de référence, avec deux écarts plus prudents (un
// membre en reconstruction compte toujours parmi les membres perdus ; dans
// un assemblage, « en échec » l'emporte toujours). Ce qui n'est pas établi
// est « indéterminé », jamais deviné.

// VolumeState est l'état normalisé d'un conteneur.
type VolumeState string

const (
	VolumeOptimal            VolumeState = "optimal"
	VolumeDegraded           VolumeState = "degraded"
	VolumeDegradedRebuilding VolumeState = "degraded_rebuilding"
	VolumeFailed             VolumeState = "failed"
	VolumeUnknown            VolumeState = "unknown"
	// Double redondance (RAID 6, 60) privée d'un membre : encore tolérante
	// à une panne.
	VolumeReducedRedundancy           VolumeState = "reduced_redundancy"
	VolumeReducedRedundancyRebuilding VolumeState = "reduced_redundancy_rebuilding"
)

// DiskState est l'état normalisé d'un disque.
type DiskState string

const (
	DiskOnline     DiskState = "online"
	DiskRebuilding DiskState = "rebuilding"
	DiskFailed     DiskState = "failed"
)

// Container est un conteneur avec ses membres.
type Container struct {
	Number  uint32
	Info    ContainerInfo
	Members []Member
}

// Evaluation est le résultat du calcul pour une carte.
type Evaluation struct {
	Volumes map[uint32]VolumeState // par UID de conteneur
	Disks   map[uint32]DiskState   // disques membres, présents ou non
}

type memberState int

const (
	memberNormal memberState = iota
	memberRebuilding
	memberDead
	memberUnknown
)

// diskMemberState classe un membre disque : disque absent ou déclaré en
// panne (preuve de perte, qui l'emporte sur les drapeaux, comme la panne
// l'emporte sur la reconstruction dans l'outil de référence), puis drapeau
// inconnu, puis drapeaux 0x2 (reconstruction, prioritaire) et 0x4 (mort).
func diskMemberState(m Member, present, failed map[uint32]bool) memberState {
	switch {
	case !present[m.Disk], failed[m.Disk]:
		return memberDead
	case m.Flags&^(MemberRebuilding|MemberDead) != 0:
		return memberUnknown
	case m.Flags&MemberRebuilding != 0:
		return memberRebuilding
	case m.Flags&MemberDead != 0:
		return memberDead
	default:
		return memberNormal
	}
}

// Evaluate calcule l'état de chaque conteneur et de chaque disque membre.
// disks est la liste complète des disques présents, lue dans le même cycle ;
// failed, les disques que leur information (248) déclare en panne (nil si
// elle n'a pas été lue).
func Evaluate(containers []Container, disks []uint32, failed map[uint32]bool) Evaluation {
	present := map[uint32]bool{}
	for _, d := range disks {
		present[d] = true
	}
	ev := Evaluation{Volumes: map[uint32]VolumeState{}, Disks: map[uint32]DiskState{}}

	// Disques : état déduit des membres qui les désignent.
	undetermined := map[uint32]bool{}
	for _, c := range containers {
		for _, m := range c.Members {
			if m.Disk == NoUID {
				continue
			}
			switch st := diskMemberState(m, present, failed); {
			case !present[m.Disk], failed[m.Disk]:
				ev.Disks[m.Disk] = DiskFailed
			case st == memberRebuilding:
				ev.Disks[m.Disk] = DiskRebuilding
			case st == memberNormal:
				if _, seen := ev.Disks[m.Disk]; !seen {
					ev.Disks[m.Disk] = DiskOnline
				}
			default:
				// Disque présent dont le membre est marqué défaillant ou
				// porte un drapeau inconnu : état non établi, non publié.
				undetermined[m.Disk] = true
			}
		}
	}
	for d := range undetermined {
		if ev.Disks[d] != DiskFailed {
			delete(ev.Disks, d)
		}
	}

	// Conteneurs : même classification que le niveau publié
	// (ContainerLevel), numérotation ARCIO seulement.
	e := evaluator{present: present, failed: failed, byUID: map[uint32]Container{},
		infos: map[uint32]ContainerInfo{}, levels: map[uint32]Level{}, out: ev.Volumes, busy: map[uint32]bool{}}
	for _, c := range containers {
		e.byUID[c.Info.UID] = c
		e.infos[c.Info.UID] = c.Info
	}
	for _, c := range containers {
		if c.Info.ClassicNumbering() {
			e.levels[c.Info.UID] = Level{Redundancy: RedundancyUnknown}
		} else {
			e.levels[c.Info.UID] = ContainerLevel(c, e.infos)
		}
	}
	// Conteneurs pris dans une boucle d'assemblages : indéterminés, décidé
	// avant le calcul pour que le résultat ne dépende pas de l'ordre.
	for uid := range cyclic(containers, e.levels) {
		e.out[uid] = VolumeUnknown
	}
	for _, c := range containers {
		e.state(c.Info.UID)
	}
	return ev
}

// cyclic renvoie les conteneurs qui se retrouvent eux-mêmes en suivant les
// liens que le calcul de l'état emprunte : d'un assemblage vers ses membres
// conteneurs.
func cyclic(containers []Container, levels map[uint32]Level) map[uint32]bool {
	children := map[uint32][]uint32{}
	for _, c := range containers {
		if levels[c.Info.UID].Redundancy != RedundancyAssembly {
			continue
		}
		for _, m := range c.Members {
			if m.Disk == NoUID {
				children[c.Info.UID] = append(children[c.Info.UID], m.ChildUID)
			}
		}
	}
	out := map[uint32]bool{}
	for _, c := range containers {
		start := c.Info.UID
		seen := map[uint32]bool{}
		stack := append([]uint32(nil), children[start]...)
		for len(stack) > 0 {
			u := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if u == start {
				out[start] = true
				break
			}
			if !seen[u] {
				seen[u] = true
				stack = append(stack, children[u]...)
			}
		}
	}
	return out
}

type evaluator struct {
	present, failed map[uint32]bool
	byUID           map[uint32]Container
	infos           map[uint32]ContainerInfo
	levels          map[uint32]Level
	out             map[uint32]VolumeState
	busy            map[uint32]bool // conteneurs en cours d'évaluation (boucle)
}

func (e *evaluator) state(uid uint32) VolumeState {
	if st, ok := e.out[uid]; ok {
		return st
	}
	if e.busy[uid] {
		return VolumeUnknown // boucle (déjà marquée par cyclic ; garde)
	}
	e.busy[uid] = true
	c := e.byUID[uid]
	var st VolumeState
	switch l := e.levels[uid]; l.Redundancy {
	case RedundancyNone, RedundancySingle, RedundancyDouble:
		st = e.diskVolume(c, l.Redundancy)
	case RedundancyAssembly:
		st = e.assembly(c)
	default:
		st = VolumeUnknown
	}
	delete(e.busy, uid)
	e.out[uid] = st
	return st
}

// diskVolume : nombre k de membres perdus (morts, plus ceux en
// reconstruction sauf inhibition de l'outil de référence), comparé à la
// tolérance du niveau. Le niveau garantit des membres tous disques.
func (e *evaluator) diskVolume(c Container, r Redundancy) VolumeState {
	if len(c.Members) != c.Info.Members {
		return VolumeUnknown
	}
	k, rebuilding, dead, unknown := 0, false, false, false
	seen := map[uint32]bool{}
	for _, m := range c.Members {
		if seen[m.Disk] {
			return VolumeUnknown // deux membres sur le même disque
		}
		seen[m.Disk] = true
		switch diskMemberState(m, e.present, e.failed) {
		case memberUnknown:
			unknown = true
		case memberDead:
			k++
			dead = true
		case memberRebuilding:
			rebuilding = true
			if !c.Info.RebuildCountInhibited() {
				k++
			}
		}
	}
	// Variante « en reconstruction » seulement sans membre mort : un membre
	// mort reste à remplacer (écart prudent ; l'outil de référence la garde
	// parfois, selon l'ordre de parcours des disques).
	rebuilding = rebuilding && !dead
	return withUnknown(tolerance(k, r, rebuilding), unknown)
}

// withUnknown : un membre indéterminé (peut-être perdu) laisse publier l'état
// connu seulement s'il exige déjà une action : « en échec », ou « dégradé »
// (sans la variante « en reconstruction », que le membre inconnu peut
// démentir) ; sinon, indéterminé.
func withUnknown(st VolumeState, unknown bool) VolumeState {
	switch {
	case !unknown, st == VolumeFailed:
		return st
	case st == VolumeDegraded, st == VolumeDegradedRebuilding:
		return VolumeDegraded
	default:
		return VolumeUnknown
	}
}

// tolerance compare k membres perdus à la tolérance de la famille r.
func tolerance(k int, r Redundancy, rebuilding bool) VolumeState {
	switch {
	case k == 0:
		return VolumeOptimal
	case r == RedundancySingle && k == 1, r == RedundancyDouble && k == 2:
		if rebuilding {
			return VolumeDegradedRebuilding
		}
		return VolumeDegraded
	case r == RedundancyDouble && k == 1:
		if rebuilding {
			return VolumeReducedRedundancyRebuilding
		}
		return VolumeReducedRedundancy
	default:
		return VolumeFailed
	}
}

// severity ordonne les états d'un volume, du moins grave au plus grave.
var severity = map[VolumeState]int{
	VolumeOptimal: 0, VolumeReducedRedundancyRebuilding: 1, VolumeReducedRedundancy: 2,
	VolumeDegradedRebuilding: 3, VolumeDegraded: 4, VolumeFailed: 5,
}

// assembly : l'état le plus grave de ses conteneurs membres (écart prudent :
// l'outil de référence fusionne par rang croissant, si bien qu'un membre
// rencontré ensuite peut masquer une perte, par exemple « en
// reconstruction » après « en échec »). Un membre indéterminé : voir
// withUnknown, qui s'applique aussi à un membre incohérent. Une boucle :
// indéterminé.
func (e *evaluator) assembly(c Container) VolumeState {
	if len(c.Members) != c.Info.Members {
		return VolumeUnknown
	}
	st, unknown := VolumeOptimal, false
	seen := map[uint32]bool{}
	for _, m := range c.Members {
		// Membre incohérent (disque, absent du relevé, en double, lui-même,
		// ou qui ne désigne pas l'assemblage comme parent) : traité comme
		// un membre indéterminé, qui ne masque pas une perte avérée.
		child, ok := e.byUID[m.ChildUID]
		if m.Disk != NoUID || !ok || child.Info.Parent != c.Info.UID || seen[m.ChildUID] || m.ChildUID == c.Info.UID {
			unknown = true
			continue
		}
		seen[m.ChildUID] = true
		cs := e.state(m.ChildUID)
		if cs == VolumeUnknown {
			unknown = true
		} else if severity[cs] > severity[st] {
			st = cs
		}
	}
	return withUnknown(st, unknown)
}
