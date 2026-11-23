package procstatus

import (
	"os"
	"strings"
	"testing"
)

const rawio = "0000000000020000" // CAP_SYS_RAWIO seule

var groups = ""

func status(uid, gid, inh, prm, eff, bnd, amb, nnp string) string {
	return "Name:\thwraid-exporter\nUmask:\t0022\nState:\tS (sleeping)\n" +
		"Uid:\t" + uid + "\nGid:\t" + gid + "\nGroups:\t" + groups + "\n" +
		"CapInh:\t" + inh + "\nCapPrm:\t" + prm + "\nCapEff:\t" + eff +
		"\nCapBnd:\t" + bnd + "\nCapAmb:\t" + amb + "\nNoNewPrivs:\t" + nnp + "\nSeccomp:\t2\n"
}

const none = "0000000000000000"

var target = AfterDrop(65534, 65534, Mask(CapSysRawIO))

func TestMaskRawIO(t *testing.T) {
	if Mask(CapSysRawIO) != 0x20000 {
		t.Fatalf("masque CAP_SYS_RAWIO = %#x", Mask(CapSysRawIO))
	}
}

func TestVerifyConformingState(t *testing.T) {
	s, err := Parse(strings.NewReader(status("65534\t65534\t65534\t65534", "65534\t65534\t65534\t65534", none, rawio, rawio, rawio, none, "1")))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(target); err != nil {
		t.Fatal(err)
	}
}

// Chaque écart isolé doit être détecté, un par un.
func TestVerifyDetectsEachDeviation(t *testing.T) {
	ok := "65534\t65534\t65534\t65534"
	mknod := "0000000008020000" // RAWIO + MKNOD
	cases := map[string]string{
		"UID sauvegardé resté root":    status("65534\t65534\t0\t65534", ok, none, rawio, rawio, rawio, none, "1"),
		"GID effectif root":            status(ok, "65534\t0\t65534\t65534", none, rawio, rawio, rawio, none, "1"),
		"MKNOD dans l'effectif":        status(ok, ok, none, rawio, mknod, rawio, none, "1"),
		"MKNOD dans le permis":         status(ok, ok, none, mknod, rawio, rawio, none, "1"),
		"MKNOD dans l'hérité":          status(ok, ok, mknod, rawio, rawio, rawio, none, "1"),
		"MKNOD dans l'ensemble limite": status(ok, ok, none, rawio, rawio, mknod, none, "1"),
		"MKNOD dans l'ambiant":         status(ok, ok, none, rawio, rawio, rawio, mknod, "1"),
		"RAWIO perdue":                 status(ok, ok, none, rawio, none, rawio, none, "1"),
		"RAWIO laissée dans l'hérité":  status(ok, ok, rawio, rawio, rawio, rawio, none, "1"),
		"RAWIO laissée dans l'ambiant": status(ok, ok, none, rawio, rawio, rawio, rawio, "1"),
		"no_new_privs absent":          status(ok, ok, none, rawio, rawio, rawio, none, "0"),
	}
	for name, txt := range cases {
		s, err := Parse(strings.NewReader(txt))
		if err != nil {
			t.Fatalf("%s : %v", name, err)
		}
		if s.Verify(target) == nil {
			t.Fatalf("%s : écart non détecté", name)
		}
	}
}

// Groupes supplémentaires restants et identité root : toujours des écarts,
// même si l'attendu demandait root par erreur.
func TestVerifyGroupsAndRoot(t *testing.T) {
	ok := "65534\t65534\t65534\t65534"
	groups = "4 24 65534"
	s, err := Parse(strings.NewReader(status(ok, ok, none, rawio, rawio, rawio, none, "1")))
	groups = ""
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Groups) != 3 || s.Verify(target) == nil {
		t.Fatalf("groupes non détectés : %v", s.Groups)
	}
	root := "0\t0\t0\t0"
	s, err = Parse(strings.NewReader(status(root, root, none, rawio, rawio, rawio, none, "1")))
	if err != nil {
		t.Fatal(err)
	}
	if s.Verify(AfterDrop(0, 0, Mask(CapSysRawIO))) == nil {
		t.Fatal("un attendu root ne doit jamais être validé")
	}
}

// Un champ manquant ou illisible fait échouer l'analyse : la vérification
// ne doit jamais passer faute d'avoir pu lire.
func TestParseRejectsIncompleteStatus(t *testing.T) {
	full := status("1\t1\t1\t1", "1\t1\t1\t1", rawio, rawio, rawio, rawio, rawio, "1")
	for _, field := range []string{"Uid", "Gid", "Groups", "CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb", "NoNewPrivs"} {
		var kept []string
		for _, l := range strings.Split(full, "\n") {
			if !strings.HasPrefix(l, field+":") {
				kept = append(kept, l)
			}
		}
		if _, err := Parse(strings.NewReader(strings.Join(kept, "\n"))); err == nil {
			t.Fatalf("champ %s absent non détecté", field)
		}
	}
	if _, err := Parse(strings.NewReader(strings.Replace(full, "CapEff:\t"+rawio, "CapEff:\tzz", 1))); err == nil {
		t.Fatal("valeur hexadécimale invalide acceptée")
	}
	if _, err := Parse(strings.NewReader(strings.Replace(full, "Uid:\t1\t1\t1\t1", "Uid:\t1\t1", 1))); err == nil {
		t.Fatal("Uid à deux champs accepté")
	}
}

// Le format réel du noyau doit être lisible.
func TestParseRealProcSelf(t *testing.T) {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		t.Skip("pas de /proc :", err)
	}
	defer f.Close()
	if _, err := Parse(f); err != nil {
		t.Fatal(err)
	}
}
