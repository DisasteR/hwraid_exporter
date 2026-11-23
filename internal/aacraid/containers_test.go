package aacraid

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"
)

// Données reconstituées des transferts enregistrés sur l'hôte de référence
// (GETCONFIG AL de l'outil de référence) : volume « Array0 » assemblant
// quatre miroirs de deux disques, tous optimaux.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	s, err := os.ReadFile("testdata/" + name + ".hex")
	if err != nil {
		t.Fatal(err)
	}
	return mustHex(t, strings.TrimSpace(string(s)))
}

func referenceContainers(t *testing.T) ([]Container, []uint32) {
	t.Helper()
	nums, err := DecodeContainerList(fixture(t, "229-0"))
	if err != nil {
		t.Fatal(err)
	}
	var cs []Container
	for _, n := range nums {
		info, err := DecodeContainerInfo(fixture(t, "234-"+hexs(n)))
		if err != nil {
			t.Fatal(err)
		}
		ms, err := DecodeMemberList(fixture(t, "249-"+hexs(info.UID)))
		if err != nil {
			t.Fatal(err)
		}
		cs = append(cs, Container{Number: n, Info: info, Members: ms})
	}
	disks, err := DecodeDiskList(fixture(t, "247-0"))
	if err != nil {
		t.Fatal(err)
	}
	return cs, disks
}

func hexs(v uint32) string {
	const digits = "0123456789abcdef"
	if v == 0 {
		return "0"
	}
	var b []byte
	for ; v > 0; v >>= 4 {
		b = append([]byte{digits[v&15]}, b...)
	}
	return string(b)
}

// Décodage confronté à l'affichage de l'outil de référence (arcconf-AL).
func TestDecodeReferenceConfiguration(t *testing.T) {
	cs, disks := referenceContainers(t)
	if len(cs) != 5 || len(disks) != 8 {
		t.Fatalf("%d conteneurs, %d disques", len(cs), len(disks))
	}
	vol := cs[0].Info
	if vol.UID != 0x1d000000 || vol.Type != TypeAssembly || vol.Parent != NoUID || vol.Members != 4 ||
		vol.Blocks != 62495109120 || vol.Name != "Array0" {
		t.Fatalf("volume : %+v", vol)
	}
	// Miroirs et leurs disques, dans l'ordre de l'outil de référence.
	want := map[uint32][2]uint32{0x1d000001: {0, 4}, 0x1d000002: {5, 1}, 0x1d000003: {6, 2}, 0x1d000004: {3, 7}}
	for _, c := range cs[1:] {
		if c.Info.Type != TypeMirror || c.Info.Parent != 0x1d000000 || c.Info.Members != 2 {
			t.Fatalf("miroir : %+v", c.Info)
		}
		w, ok := want[c.Info.UID]
		if !ok || c.Members[0].Disk != w[0] || c.Members[1].Disk != w[1] || c.Members[0].Flags != 0 || c.Members[1].Flags != 0 {
			t.Fatalf("miroir %#x : %+v", c.Info.UID, c.Members)
		}
	}
	// Le volume assemble les quatre miroirs.
	got := map[uint32]bool{}
	for _, m := range cs[0].Members {
		if m.Disk != NoUID {
			t.Fatalf("membre disque dans l'assemblage : %+v", m)
		}
		got[m.ChildUID] = true
	}
	for uid := range want {
		if !got[uid] {
			t.Fatalf("miroir %#x absent de l'assemblage", uid)
		}
	}
	ev := Evaluate(cs, disks)
	for uid, st := range ev.Volumes {
		if st != VolumeOptimal {
			t.Fatalf("conteneur %#x : %s", uid, st)
		}
	}
	if len(ev.Volumes) != 5 || len(ev.Disks) != 8 {
		t.Fatalf("%+v", ev)
	}
	for d, st := range ev.Disks {
		if st != DiskOnline {
			t.Fatalf("disque %d : %s", d, st)
		}
	}
}

// Variantes construites à partir de la configuration de référence : chaque
// branche de la règle.
func TestEvaluateBranches(t *testing.T) {
	const mirror = 0x1d000004 // disques 3 et 7
	type tc struct {
		name   string
		mod    func(cs []Container, disks []uint32) []uint32
		mirror VolumeState
		top    VolumeState
		disk7  DiskState // "" : non publié
	}
	setFlags := func(cs []Container, disk, flags uint32) {
		for i := range cs {
			for j := range cs[i].Members {
				if cs[i].Members[j].Disk == disk {
					cs[i].Members[j].Flags = flags
				}
			}
		}
	}
	without := func(disks []uint32, d uint32) []uint32 {
		var out []uint32
		for _, x := range disks {
			if x != d {
				out = append(out, x)
			}
		}
		return out
	}
	cases := []tc{
		{"disque 7 absent", func(cs []Container, d []uint32) []uint32 { return without(d, 7) }, VolumeDegraded, VolumeUnknown, DiskFailed},
		{"membre 7 défaillant, disque présent", func(cs []Container, d []uint32) []uint32 { setFlags(cs, 7, MemberDead); return d }, VolumeDegraded, VolumeUnknown, ""},
		{"membre 7 en reconstruction", func(cs []Container, d []uint32) []uint32 { setFlags(cs, 7, MemberRebuilding); return d }, VolumeDegradedRebuilding, VolumeUnknown, DiskRebuilding},
		{"reconstruction prioritaire sur défaillance", func(cs []Container, d []uint32) []uint32 {
			setFlags(cs, 7, MemberRebuilding|MemberDead)
			return d
		}, VolumeDegradedRebuilding, VolumeUnknown, DiskRebuilding},
		{"drapeau inconnu", func(cs []Container, d []uint32) []uint32 { setFlags(cs, 7, 0x10); return d }, VolumeUnknown, VolumeUnknown, ""},
		{"deux disques absents", func(cs []Container, d []uint32) []uint32 { return without(without(d, 7), 3) }, VolumeFailed, VolumeUnknown, DiskFailed},
		{"liste des membres incomplète", func(cs []Container, d []uint32) []uint32 {
			for i := range cs {
				if cs[i].Info.UID == mirror {
					cs[i].Members = cs[i].Members[:1]
				}
			}
			return d
		}, VolumeUnknown, VolumeUnknown, ""},
	}
	for _, c := range cases {
		cs, disks := referenceContainers(t)
		disks = c.mod(cs, disks)
		ev := Evaluate(cs, disks)
		if ev.Volumes[mirror] != c.mirror || ev.Volumes[0x1d000000] != c.top || ev.Disks[7] != c.disk7 {
			t.Fatalf("%s : miroir %s, volume %s, disque 7 %q", c.name, ev.Volumes[mirror], ev.Volumes[0x1d000000], ev.Disks[7])
		}
		for _, other := range []uint32{0x1d000001, 0x1d000002, 0x1d000003} {
			if ev.Volumes[other] != VolumeOptimal {
				t.Fatalf("%s : miroir %#x touché : %s", c.name, other, ev.Volumes[other])
			}
		}
	}
	// Type non qualifié : indéterminé, jamais deviné.
	cs, disks := referenceContainers(t)
	cs[1].Info.Type = 4
	if st := Evaluate(cs, disks).Volumes[cs[1].Info.UID]; st != VolumeUnknown {
		t.Fatalf("type 4 : %s", st)
	}
}

func TestDecodeRejects(t *testing.T) {
	le := binary.LittleEndian
	if _, err := DecodeContainerList([]byte{1, 0, 0, 0, 9, 0, 0, 0}); err == nil {
		t.Fatal("liste de conteneurs tronquée acceptée")
	}
	if _, err := DecodeDiskList([]byte{9, 0, 0, 0}); err == nil {
		t.Fatal("liste de disques tronquée acceptée")
	}
	m := fixture(t, "249-1d000004")
	le.PutUint32(m[8+4:], 7) // le membre désigne aussi un conteneur
	if _, err := DecodeMemberList(m); err == nil {
		t.Fatal("membre à la fois disque et conteneur accepté")
	}
	m = fixture(t, "249-1d000004")
	le.PutUint32(m[4:], 3) // trois membres annoncés, deux présents
	if _, err := DecodeMemberList(m); err == nil {
		t.Fatal("liste des membres tronquée acceptée")
	}
	c := fixture(t, "234-0")
	le.PutUint32(c[56:], 0)
	if _, err := DecodeContainerInfo(c); err == nil {
		t.Fatal("conteneur sans membre accepté")
	}
	if _, err := DecodeContainerInfo(fixture(t, "234-0")[:ContainerInfoNeed-1]); err == nil {
		t.Fatal("information tronquée acceptée")
	}
}

// Configurations incohérentes : indéterminées, quel que soit l'ordre.
func TestEvaluateInconsistent(t *testing.T) {
	const top, b3 = 0x1d000000, 0x1d000004
	cases := map[string]func(cs []Container){
		"miroir à disque dupliqué": func(cs []Container) {
			for i := range cs {
				if cs[i].Info.UID == b3 {
					cs[i].Members[1].Disk = cs[i].Members[0].Disk
				}
			}
		},
		"miroir membre en double dans l'assemblage": func(cs []Container) { cs[0].Members[1].ChildUID = cs[0].Members[0].ChildUID },
		"parenté incohérente": func(cs []Container) {
			for i := range cs {
				if cs[i].Info.UID == b3 {
					cs[i].Info.Parent = 0x12345678
				}
			}
		},
		"membre inconnu": func(cs []Container) { cs[0].Members[0].ChildUID = 0x0bad0bad },
	}
	for name, mod := range cases {
		cs, disks := referenceContainers(t)
		mod(cs)
		if st := Evaluate(cs, disks).Volumes[top]; st != VolumeUnknown {
			t.Fatalf("%s : volume %s", name, st)
		}
	}
	// Ordre de lecture inversé : même résultat.
	cs, disks := referenceContainers(t)
	for i, j := 0, len(cs)-1; i < j; i, j = i+1, j-1 {
		cs[i], cs[j] = cs[j], cs[i]
	}
	if st := Evaluate(cs, disks).Volumes[top]; st != VolumeOptimal {
		t.Fatalf("ordre inversé : %s", st)
	}
	// Assemblage imbriqué : indéterminé, même si l'assemblage enfant est optimal.
	cs, disks = referenceContainers(t)
	inner := cs[0]
	inner.Info.UID, inner.Info.Parent = 0x99, top
	outer := Container{Info: ContainerInfo{UID: 0x98, Type: TypeAssembly, Parent: NoUID, Members: 1},
		Members: []Member{{Disk: NoUID, ChildUID: 0x99}}}
	ev := Evaluate(append(append([]Container{outer}, cs[1:]...), inner), disks)
	if ev.Volumes[0x98] != VolumeUnknown {
		t.Fatalf("assemblage imbriqué : %s", ev.Volumes[0x98])
	}
}

func TestContainerNamePrintable(t *testing.T) {
	c := fixture(t, "234-0")
	copy(c[296:], []byte{'A', 0xff, 0x01, 'b', 0})
	info, err := DecodeContainerInfo(c)
	if err != nil || info.Name != "A??b" {
		t.Fatalf("%q %v", info.Name, err)
	}
}

// Un disque en double dans la liste 247 est refusé : il donnerait deux
// séries d'inventaire pour un même disque.
func TestDiskListDuplicate(t *testing.T) {
	d := fixture(t, "247-0")
	binary.LittleEndian.PutUint32(d[8:], binary.LittleEndian.Uint32(d[4:]))
	if _, err := DecodeDiskList(d); err == nil {
		t.Fatal("disque en double accepté")
	}
}
