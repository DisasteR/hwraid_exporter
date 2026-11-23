package nodes

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
)

const requireRoot = "HWRAID_REQUIRE_ROOT_TESTS"

const procDevices = `Character devices:
  1 mem
  4 /dev/vc/0
 21 sg
247 aac
250 megaraid_sas_ioctl

Block devices:
  8 sd
247 aac
`

func TestCharMajors(t *testing.T) {
	m, err := CharMajors(strings.NewReader(procDevices))
	if err != nil {
		t.Fatal(err)
	}
	if m["aac"] != 247 || m["sg"] != 21 || m["megaraid_sas_ioctl"] != 250 {
		t.Fatalf("majors : %v", m)
	}
	if _, ok := m["sd"]; ok {
		t.Fatal("un périphérique bloc ne doit pas apparaître parmi les majors caractère")
	}
	if _, err := CharMajors(strings.NewReader("Character devices:\n  x y z\n")); err == nil {
		t.Fatal("ligne malformée acceptée")
	}
}

func TestDesired(t *testing.T) {
	majors := map[string]uint32{"aac": 247}
	hosts := []sysfs.SCSIHost{
		{Name: "host2", ProcName: "aacraid", UniqueID: "1"},
		{Name: "host0", ProcName: "aacraid", UniqueID: "0"},
		{Name: "host14", ProcName: "ahci", UniqueID: "14"},
	}
	specs, errs := Desired(hosts, majors)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	want := []Spec{{"aac0", 247, 0}, {"aac1", 247, 1}}
	if len(specs) != 2 || specs[0] != want[0] || specs[1] != want[1] {
		t.Fatalf("specs : %+v", specs)
	}
	// Major absent et unique_id invalide : erreurs par hôte, aucun nœud.
	_, errs = Desired(hosts[:1], map[string]uint32{})
	if len(errs) != 1 {
		t.Fatalf("major absent non signalé : %v", errs)
	}
	specs, errs = Desired([]sysfs.SCSIHost{{Name: "host3", ProcName: "aacraid", UniqueID: "x"}}, majors)
	if len(specs) != 0 || len(errs) != 1 {
		t.Fatalf("unique_id invalide : specs=%v errs=%v", specs, errs)
	}
}

func TestSchema(t *testing.T) {
	for _, ok := range []string{"aac0", "aac15", "megaraid_sas_ioctl", "hpsa-0000_05_00.0", "smartpqi-0001_3b_00.1"} {
		if !schema.MatchString(ok) {
			t.Errorf("%s refusé", ok)
		}
	}
	for _, bad := range []string{"aac", "aac0x", "sda", "../aac0", "aac0/..", "hpsa-0000:05:00.0", "null"} {
		if schema.MatchString(bad) {
			t.Errorf("%s accepté", bad)
		}
	}
}

// Refus sans privilège : une entrée étrangère bloque tout, rien n'est modifié.
func TestSyncRefusesForeignEntries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(dir string)
	}{
		{"fichier ordinaire au nom du schéma", func(d string) { must(t, os.WriteFile(filepath.Join(d, "aac0"), []byte("x"), 0o644)) }},
		{"nom inconnu", func(d string) { must(t, os.WriteFile(filepath.Join(d, "autre"), nil, 0o644)) }},
		{"lien symbolique", func(d string) { must(t, os.Symlink("/dev/null", filepath.Join(d, "aac1"))) }},
		{"sous-répertoire", func(d string) { must(t, os.Mkdir(filepath.Join(d, "aac2"), 0o755)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(dir)
			before := snapshot(t, dir)
			_, err := Sync(dir, []Spec{{"aac0", 1, 3}}, 65534, 65534)
			if !errors.Is(err, ErrForeignEntry) {
				t.Fatalf("ErrForeignEntry attendu, obtenu %v", err)
			}
			if after := snapshot(t, dir); after != before {
				t.Fatalf("répertoire modifié :\navant %s\naprès %s", before, after)
			}
		})
	}
}

func TestSyncRefusesSymlinkDirectory(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "ailleurs")
	must(t, os.Mkdir(target, 0o755))
	link := filepath.Join(base, "hwraid_exporter")
	must(t, os.Symlink(target, link))
	if _, err := Sync(link, nil, 65534, 65534); err == nil {
		t.Fatal("un lien symbolique à la place du répertoire doit être refusé")
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Fatal("la cible du lien a été modifiée")
	}
}

func TestSyncRejectsInvalidSpecs(t *testing.T) {
	dir := t.TempDir()
	for _, specs := range [][]Spec{
		{{"../evil", 1, 3}},
		{{"sda", 8, 0}},
		{{"aac0", 1, 3}, {"aac0", 1, 5}},
	} {
		if _, err := Sync(dir, specs, 65534, 65534); err == nil {
			t.Fatalf("specs acceptées : %+v", specs)
		}
	}
}

// Cycle de vie réel, en root (CI) : création, idempotence, ouverture,
// remplacement atomique, suppression, temporaire abandonné, concurrence.
func TestSyncLifecycleAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		if os.Getenv(requireRoot) == "1" {
			t.Fatal("test root exigé mais processus non root")
		}
		t.Skip("exige root")
	}
	dir := filepath.Join(t.TempDir(), "hwraid_exporter")
	null, zero := Spec{"aac0", 1, 3}, Spec{"aac0", 1, 5} // /dev/null puis /dev/zero

	rep, err := Sync(dir, []Spec{null}, 65534, 65534)
	if err != nil || len(rep.Created) != 1 {
		t.Fatalf("création : rep=%+v err=%v", rep, err)
	}
	checkNode(t, filepath.Join(dir, "aac0"), null.Dev(), 65534, 65534)
	checkDir(t, dir, 0, 65534)
	f, err := os.Open(filepath.Join(dir, "aac0"))
	if err != nil {
		t.Fatalf("ouverture du nœud créé : %v (montage nodev ?)", err)
	}
	f.Close()

	rep, err = Sync(dir, []Spec{null}, 65534, 65534)
	if err != nil || len(rep.Created)+len(rep.Replaced)+len(rep.Fixed)+len(rep.Removed) != 0 {
		t.Fatalf("idempotence : rep=%+v err=%v", rep, err)
	}

	must(t, os.Chmod(filepath.Join(dir, "aac0"), 0o666))
	rep, err = Sync(dir, []Spec{null}, 65534, 65534)
	if err != nil || len(rep.Fixed) != 1 {
		t.Fatalf("correction du mode : rep=%+v err=%v", rep, err)
	}
	checkNode(t, filepath.Join(dir, "aac0"), null.Dev(), 65534, 65534)

	rep, err = Sync(dir, []Spec{zero}, 65534, 65534)
	if err != nil || len(rep.Replaced) != 1 {
		t.Fatalf("remplacement : rep=%+v err=%v", rep, err)
	}
	checkNode(t, filepath.Join(dir, "aac0"), zero.Dev(), 65534, 65534)

	must(t, unix.Mknod(filepath.Join(dir, tmpPrefix+"aac9"), unix.S_IFCHR|0o600, int(unix.Mkdev(1, 3))))
	rep, err = Sync(dir, nil, 65534, 65534)
	if err != nil || len(rep.Removed) != 2 {
		t.Fatalf("suppression : rep=%+v err=%v", rep, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("entrées restantes : %v", entries)
	}

	// Un nœud bloc portant un nom du schéma est refusé.
	must(t, unix.Mknod(filepath.Join(dir, "aac3"), unix.S_IFBLK|0o600, int(unix.Mkdev(7, 0))))
	if _, err := Sync(dir, nil, 65534, 65534); !errors.Is(err, ErrForeignEntry) {
		t.Fatalf("nœud bloc : ErrForeignEntry attendu, obtenu %v", err)
	}
	must(t, os.Remove(filepath.Join(dir, "aac3")))

	// Exécutions concurrentes : sérialisées par le verrou, état final cohérent.
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := null
			if i%2 == 1 {
				s = zero
			}
			_, err := Sync(dir, []Spec{s}, 65534, 65534)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("exécution concurrente : %v", err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "aac0" {
		t.Fatalf("état final incohérent : %v", entries)
	}
}

func checkNode(t *testing.T, p string, dev uint64, uid, gid uint32) {
	t.Helper()
	var st unix.Stat_t
	must(t, unix.Lstat(p, &st))
	if st.Mode&unix.S_IFMT != unix.S_IFCHR || st.Rdev != dev || st.Uid != uid || st.Gid != gid || st.Mode&0o7777 != 0o600 {
		t.Fatalf("%s : mode=%o rdev=%x uid=%d gid=%d", p, st.Mode, st.Rdev, st.Uid, st.Gid)
	}
}

func checkDir(t *testing.T, p string, uid, gid uint32) {
	t.Helper()
	var st unix.Stat_t
	must(t, unix.Lstat(p, &st))
	if st.Uid != uid || st.Gid != gid || st.Mode&0o7777 != 0o750 {
		t.Fatalf("répertoire %s : mode=%o uid=%d gid=%d", p, st.Mode&0o7777, st.Uid, st.Gid)
	}
}

func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	entries, err := os.ReadDir(dir)
	must(t, err)
	for _, e := range entries {
		var st unix.Stat_t
		must(t, unix.Lstat(filepath.Join(dir, e.Name()), &st))
		b.WriteString(e.Name())
		b.WriteString(":")
		b.WriteString(strings.TrimSpace(strings.Repeat(" ", 0)))
		b.WriteString(os.FileMode(st.Mode).String())
		b.WriteString(";")
	}
	return b.String()
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
