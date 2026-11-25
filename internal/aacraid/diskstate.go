// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

// État de tous les disques présents (docs/aacraid-interface.md, section
// 6 bis, « État d'un disque ») : règles de l'outil de référence sur la
// lecture 248, appliquées dans son ordre, chacune pouvant remplacer l'état
// posé par les précédentes.

// États de disque supplémentaires du vocabulaire normalisé (docs/metrics.md).
const (
	DiskReady       DiskState = "ready"
	DiskSpare       DiskState = "spare"
	DiskCopyback    DiskState = "copyback"
	DiskPassthrough DiskState = "passthrough"
	DiskUnsupported DiskState = "unsupported"
)

// DiskCaps regroupe les capacités de la carte qui changent l'état affiché
// d'un disque : bit 16 de SupportedOptions2 et AAC_FEATURE_JBOD (706),
// option HBA natif (703, NativeKnown faux si la 703 n'a pas été lue).
type DiskCaps struct {
	DirectDisks, JBOD      bool
	NativeHBA, NativeKnown bool
}

// DiskStatus est l'état normalisé d'un disque et le libellé de l'outil de
// référence.
type DiskStatus struct {
	State  DiskState
	Vendor string
}

// memberRole résume, pour un disque, ce que disent les membres qui le
// désignent.
type memberRole struct {
	member, rebuilding bool
}

func memberRoles(containers []Container) map[uint32]memberRole {
	roles := map[uint32]memberRole{}
	for _, c := range containers {
		for _, m := range c.Members {
			if m.Disk == NoUID {
				continue
			}
			r := roles[m.Disk]
			r.member = true
			if m.Flags&MemberRebuilding != 0 {
				r.rebuilding = true
			}
			roles[m.Disk] = r
		}
	}
	return roles
}

// DiskStates calcule l'état de chaque disque présent à partir de son
// information (248) ; un disque désigné par un membre mais absent est en
// panne. Un périphérique qui n'est pas un disque (HardDrive) désigné par un
// membre ou portant le bit de secours est en panne (l'outil de référence le
// remplace par un disque « manquant ») ; sinon il n'a pas d'état de disque.
// Un disque présent absent de inv n'a pas d'état.
func DiskStates(containers []Container, present []uint32, inv map[uint32]DiskInfo, caps DiskCaps) map[uint32]DiskStatus {
	roles := memberRoles(containers)
	out := map[uint32]DiskStatus{}
	isPresent := map[uint32]bool{}
	for _, n := range present {
		isPresent[n] = true
		i, ok := inv[n]
		switch {
		case !ok:
		case i.HardDrive():
			out[n] = diskState(i, roles[n], caps)
		case roles[n].member, i.Status68&0x04 != 0:
			out[n] = DiskStatus{DiskFailed, "Failed"}
		}
	}
	for n := range roles {
		if !isPresent[n] {
			out[n] = DiskStatus{DiskFailed, "Failed"}
		}
	}
	return out
}

func diskState(i DiskInfo, role memberRole, caps DiskCaps) DiskStatus {
	st := DiskStatus{DiskReady, "Ready"}
	failed := i.Failed()
	both := i.Status68&0x50 == 0x50 // bits 0x10 et 0x40 de l'octet 68
	if failed {
		st = DiskStatus{DiskFailed, "Failed"}
	}
	if i.HasStatus332 && i.Status332&0x2 != 0 {
		st = DiskStatus{DiskUnsupported, "Unsupported"}
	}
	exposed := (caps.DirectDisks && !failed && !both) || (caps.JBOD && i.Qualifier&0xe0 == 0)
	if exposed && !failed {
		if both {
			st = DiskStatus{DiskOnline, "Online"}
		} else {
			st = DiskStatus{DiskPassthrough, "Raw (Pass Through)"}
		}
	}
	if caps.NativeKnown && caps.NativeHBA && !failed && i.Status68&0x40 == 0 {
		if i.Status69&0x80 != 0 && i.Status70&0x1 != 0 {
			st = DiskStatus{DiskPassthrough, "Raw (Smart HBA)"}
		} else {
			st = DiskStatus{DiskPassthrough, "Raw (Pass Through)"}
		}
	}
	if !failed && i.Flags0&0x1 != 0 && i.Status69&0x08 != 0 {
		st = DiskStatus{DiskCopyback, "Copying Back"}
	}
	if i.Status68&0x04 != 0 && st.State != DiskFailed {
		st = DiskStatus{DiskSpare, "Hot Spare"}
	}
	if role.member && st.State == DiskReady {
		st = DiskStatus{DiskOnline, "Online"}
	}
	if role.rebuilding && st.State != DiskFailed {
		st = DiskStatus{DiskRebuilding, "Rebuilding"}
	}
	return st
}
