package aacraid

// Calcul des états (docs/aacraid-interface.md, section 6). Seuls les types
// de conteneur qualifiés sur matériel sont évalués ; les autres sont
// « indéterminés », jamais devinés.

// VolumeState est l'état normalisé d'un conteneur.
type VolumeState string

const (
	VolumeOptimal            VolumeState = "optimal"
	VolumeDegraded           VolumeState = "degraded"
	VolumeDegradedRebuilding VolumeState = "degraded_rebuilding"
	VolumeFailed             VolumeState = "failed"
	VolumeUnknown            VolumeState = "unknown"
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

func diskMemberState(m Member, present map[uint32]bool) memberState {
	switch {
	case m.Flags&^(MemberRebuilding|MemberDead) != 0:
		return memberUnknown
	case m.Flags&MemberRebuilding != 0:
		return memberRebuilding
	case m.Flags&MemberDead != 0, !present[m.Disk]:
		return memberDead
	default:
		return memberNormal
	}
}

// Evaluate calcule l'état de chaque conteneur et de chaque disque membre.
// disks est la liste complète des disques présents, lue dans le même cycle.
func Evaluate(containers []Container, disks []uint32) Evaluation {
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
			switch st := diskMemberState(m, present); {
			case !present[m.Disk]:
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

	// Conteneurs de disques d'abord (miroirs), puis assemblages.
	for _, c := range containers {
		if c.Info.Type == TypeMirror {
			ev.Volumes[c.Info.UID] = mirrorState(c, present)
		}
	}
	byUID := map[uint32]Container{}
	for _, c := range containers {
		byUID[c.Info.UID] = c
	}
	for _, c := range containers {
		if c.Info.Type == TypeMirror {
			continue
		}
		ev.Volumes[c.Info.UID] = assemblyState(c, ev.Volumes, byUID)
	}
	return ev
}

// mirrorState : tolérance d'un membre. Un membre en reconstruction ne
// contribue pas à la redondance et compte avec les membres défaillants.
func mirrorState(c Container, present map[uint32]bool) VolumeState {
	if len(c.Members) != c.Info.Members || len(c.Members) != 2 {
		return VolumeUnknown
	}
	counted, rebuilding := 0, false
	if c.Members[0].Disk == c.Members[1].Disk {
		return VolumeUnknown // les deux membres désignent le même disque
	}
	for _, m := range c.Members {
		if m.Disk == NoUID {
			return VolumeUnknown
		}
		switch diskMemberState(m, present) {
		case memberUnknown:
			return VolumeUnknown
		case memberRebuilding:
			counted++
			rebuilding = true
		case memberDead:
			counted++
		}
	}
	switch {
	case counted == 0:
		return VolumeOptimal
	case counted == 1 && rebuilding:
		return VolumeDegradedRebuilding
	case counted == 1:
		return VolumeDegraded
	default:
		return VolumeFailed
	}
}

// assemblyState : un conteneur qui assemble des conteneurs est optimal si
// tous ses membres le sont ; sinon indéterminé, la propagation de l'état
// d'un membre n'étant pas établie. Tout autre type est indéterminé.
//
// Seule la forme observée est évaluée : un assemblage de miroirs distincts,
// chacun déclarant l'assemblage comme parent. Toute autre forme (membre
// disque, assemblage imbriqué, membre en double, parenté incohérente) est
// indéterminée, indépendamment de l'ordre de lecture.
func assemblyState(c Container, volumes map[uint32]VolumeState, byUID map[uint32]Container) VolumeState {
	if c.Info.Type != TypeAssembly || len(c.Members) != c.Info.Members {
		return VolumeUnknown
	}
	seen := map[uint32]bool{}
	for _, m := range c.Members {
		child, ok := byUID[m.ChildUID]
		if m.ChildUID == NoUID || !ok || child.Info.Type != TypeMirror || child.Info.Parent != c.Info.UID || seen[m.ChildUID] {
			return VolumeUnknown
		}
		seen[m.ChildUID] = true
		if volumes[m.ChildUID] != VolumeOptimal {
			return VolumeUnknown
		}
	}
	return VolumeOptimal
}
