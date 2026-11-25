// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import "testing"

// Règles de l'état d'un disque (tableau de la section 6 bis), une par une
// puis leurs priorités. États et libellés attendus écrits en dur.
func TestDiskStateRules(t *testing.T) {
	jbod := DiskCaps{JBOD: true, NativeKnown: true}
	direct := DiskCaps{DirectDisks: true, NativeKnown: true}
	hba := DiskCaps{NativeHBA: true, NativeKnown: true}
	none := DiskCaps{NativeKnown: true}
	for _, c := range []struct {
		name          string
		i             DiskInfo
		role          memberRole
		caps          DiskCaps
		state, vendor string
	}{
		{"prêt, sans capacité", DiskInfo{Status68: 0x70}, memberRole{}, none, "ready", "Ready"},
		{"membre : en ligne", DiskInfo{Status68: 0x70}, memberRole{member: true}, none, "online", "Online"},
		{"membre en reconstruction", DiskInfo{Status68: 0x70}, memberRole{member: true, rebuilding: true}, none, "rebuilding", "Rebuilding"},
		{"en panne (octet 68 bit 0x1)", DiskInfo{Status68: 0x71}, memberRole{member: true}, none, "failed", "Failed"},
		{"en panne prime sur la reconstruction", DiskInfo{Status68: 0x71}, memberRole{member: true, rebuilding: true}, none, "failed", "Failed"},
		{"non pris en charge (332 bit 0x2)", DiskInfo{Status68: 0x70, HasStatus332: true, Status332: 0x2}, memberRole{}, none, "unsupported", "Unsupported"},
		{"en panne et non pris en charge : en panne (écart prudent)", DiskInfo{Status68: 0x71, HasStatus332: true, Status332: 0x2}, memberRole{}, none, "failed", "Failed"},
		{"332 absent : non lu", DiskInfo{Status68: 0x70, Status332: 0x2}, memberRole{}, none, "ready", "Ready"},
		{"JBOD, bits 0x10 et 0x40 : en ligne", DiskInfo{Status68: 0x50}, memberRole{}, jbod, "online", "Online"},
		{"JBOD, sans 0x40 : brut", DiskInfo{Status68: 0x10}, memberRole{}, jbod, "passthrough", "Raw (Pass Through)"},
		{"JBOD, qualificateur non nul : prêt", DiskInfo{Status68: 0x50, Qualifier: 0x20}, memberRole{}, jbod, "ready", "Ready"},
		{"JBOD, disque en panne : en panne", DiskInfo{Status68: 0x51}, memberRole{}, jbod, "failed", "Failed"},
		{"exposition directe, sans 0x10 : brut", DiskInfo{Status68: 0x40}, memberRole{}, direct, "passthrough", "Raw (Pass Through)"},
		{"exposition directe, deux bits : prêt", DiskInfo{Status68: 0x50}, memberRole{}, direct, "ready", "Ready"},
		{"HBA natif, sans 0x40 : brut", DiskInfo{Status68: 0x10}, memberRole{}, hba, "passthrough", "Raw (Pass Through)"},
		{"HBA natif, Smart HBA", DiskInfo{Status68: 0x10, Status69: 0x80, Status70: 0x1}, memberRole{}, hba, "passthrough", "Raw (Smart HBA)"},
		{"HBA natif, octet 70 nul : brut", DiskInfo{Status68: 0x10, Status69: 0x80}, memberRole{}, hba, "passthrough", "Raw (Pass Through)"},
		{"HBA natif, 0x40 présent : prêt", DiskInfo{Status68: 0x50}, memberRole{}, hba, "ready", "Ready"},
		{"HBA natif non lu (703)", DiskInfo{Status68: 0x10}, memberRole{}, DiskCaps{NativeHBA: true}, "ready", "Ready"},
		{"copie de retour", DiskInfo{Status68: 0x70, Flags0: 0x1, Status69: 0x08}, memberRole{member: true}, none, "copyback", "Copying Back"},
		{"copie de retour, octet 0 nul", DiskInfo{Status68: 0x70, Status69: 0x08}, memberRole{}, none, "ready", "Ready"},
		{"copie de retour, disque en panne", DiskInfo{Status68: 0x71, Flags0: 0x1, Status69: 0x08}, memberRole{}, none, "failed", "Failed"},
		{"secours global (octet 68 bit 0x4)", DiskInfo{Status68: 0x74}, memberRole{}, none, "spare", "Hot Spare"},
		{"secours en panne : en panne", DiskInfo{Status68: 0x75}, memberRole{}, none, "failed", "Failed"},
		{"secours qui reconstruit", DiskInfo{Status68: 0x74}, memberRole{member: true, rebuilding: true}, none, "rebuilding", "Rebuilding"},
		{"membre en ligne par JBOD reste en ligne", DiskInfo{Status68: 0x70}, memberRole{member: true}, jbod, "online", "Online"},
		{"non pris en charge puis en ligne par JBOD", DiskInfo{Status68: 0x50, HasStatus332: true, Status332: 0x2}, memberRole{}, jbod, "online", "Online"},
		{"copie de retour puis secours", DiskInfo{Status68: 0x74, Flags0: 0x1, Status69: 0x08}, memberRole{}, none, "spare", "Hot Spare"},
		{"non pris en charge et membre : reste non pris en charge", DiskInfo{Status68: 0x70, HasStatus332: true, Status332: 0x2}, memberRole{member: true}, none, "unsupported", "Unsupported"},
		{"en panne, non pris en charge, secours : en panne", DiskInfo{Status68: 0x75, HasStatus332: true, Status332: 0x2}, memberRole{}, none, "failed", "Failed"},
		{"HBA natif sur un disque non pris en charge : brut", DiskInfo{Status68: 0x10, HasStatus332: true, Status332: 0x2}, memberRole{}, hba, "passthrough", "Raw (Pass Through)"},
		{"exposition directe d'un disque en panne : en panne", DiskInfo{Status68: 0x41}, memberRole{}, direct, "failed", "Failed"},
		{"brut membre : reste brut", DiskInfo{Status68: 0x10}, memberRole{member: true}, jbod, "passthrough", "Raw (Pass Through)"},
	} {
		st := diskState(c.i, c.role, c.caps)
		if string(st.State) != c.state || st.Vendor != c.vendor {
			t.Errorf("%s : %+v, attendu %s / %s", c.name, st, c.state, c.vendor)
		}
	}
}

// DiskStates : tous les disques présents, membres ou non ; un disque
// désigné par un membre mais absent est en panne.
func TestDiskStatesPresentAndMissing(t *testing.T) {
	v := vol(1, 3, ok, rbd) // disques 100 et 101
	inv := map[uint32]DiskInfo{100: {Number: 100, Status68: 0x70}, 7: {Number: 7, Status68: 0x74}}
	got := DiskStates([]Container{v}, []uint32{100, 7}, inv, DiskCaps{NativeKnown: true})
	want := map[uint32]DiskStatus{100: {"online", "Online"}, 7: {"spare", "Hot Spare"}, 101: {"failed", "Failed"}}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for n, w := range want {
		if got[n] != w {
			t.Errorf("disque %d : %+v, attendu %+v", n, got[n], w)
		}
	}
}

// Un périphérique qui n'est pas un disque (type non nul à l'octet 80, ou bit
// 0x80 de l'octet 81) n'a pas d'état de disque ; désigné par un membre, il
// est en panne.
func TestDiskStatesNotHardDrive(t *testing.T) {
	v := vol(1, 3, ok, ok) // disques 100 et 101
	for _, other := range []DiskInfo{{Number: 101, Status68: 0x70, Qualifier: 0x0d}, {Number: 101, Status68: 0x70, Status81: 0x80}} {
		inv := map[uint32]DiskInfo{100: {Number: 100, Status68: 0x70}, 101: other, 7: other}
		got := DiskStates([]Container{v}, []uint32{100, 101, 7}, inv, DiskCaps{JBOD: true, NativeKnown: true})
		if got[101] != (DiskStatus{"failed", "Failed"}) || got[100] != (DiskStatus{"online", "Online"}) {
			t.Errorf("%+v : %+v", other, got)
		}
		if _, ok := got[7]; ok {
			t.Errorf("autre périphérique hors volume publié : %+v", got)
		}
		// Portant le bit de secours, il est en panne (disque « manquant »
		// de l'outil de référence).
		spare := other
		spare.Number, spare.Status68 = 7, 0x74
		inv[7] = spare
		if st := DiskStates([]Container{v}, []uint32{100, 101, 7}, inv, DiskCaps{NativeKnown: true}); st[7] != (DiskStatus{"failed", "Failed"}) {
			t.Errorf("autre périphérique secours : %+v", st)
		}
	}
	// Disque présent absent de l'inventaire : pas d'état.
	if st := DiskStates(nil, []uint32{5}, map[uint32]DiskInfo{}, DiskCaps{}); len(st) != 0 {
		t.Errorf("disque sans inventaire : %+v", st)
	}
	if !(DiskInfo{}).HardDrive() || (DiskInfo{Qualifier: 0x01}).HardDrive() || !(DiskInfo{Qualifier: 0xe0}).HardDrive() || (DiskInfo{Status81: 0x80}).HardDrive() {
		t.Error("règle du disque mal lue")
	}
}
