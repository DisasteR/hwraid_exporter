package procstatus

import (
	"os"
	"strings"
	"testing"
)

const rawio = "0000000000020000" // CAP_SYS_RAWIO seule

func status(uid, gid, inh, prm, eff, bnd, amb, nnp string) string {
	return "Name:\thwraid-exporter\nUmask:\t0022\nState:\tS (sleeping)\n" +
		"Uid:\t" + uid + "\nGid:\t" + gid + "\nGroups:\t\n" +
		"CapInh:\t" + inh + "\nCapPrm:\t" + prm + "\nCapEff:\t" + eff +
		"\nCapBnd:\t" + bnd + "\nCapAmb:\t" + amb + "\nNoNewPrivs:\t" + nnp + "\nSeccomp:\t2\n"
}

var target = Expected{UID: 65534, GID: 65534, Caps: Mask(CapSysRawIO)}

func TestMaskRawIO(t *testing.T) {
	if Mask(CapSysRawIO) != 0x20000 {
		t.Fatalf("masque CAP_SYS_RAWIO = %#x", Mask(CapSysRawIO))
	}
}

func TestVerifyConformingState(t *testing.T) {
	s, err := Parse(strings.NewReader(status("65534\t65534\t65534\t65534", "65534\t65534\t65534\t65534", rawio, rawio, rawio, rawio, rawio, "1")))
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
		"UID sauvegardé resté root":    status("65534\t65534\t0\t65534", ok, rawio, rawio, rawio, rawio, rawio, "1"),
		"GID effectif root":            status(ok, "65534\t0\t65534\t65534", rawio, rawio, rawio, rawio, rawio, "1"),
		"MKNOD dans l'effectif":        status(ok, ok, rawio, rawio, mknod, rawio, rawio, "1"),
		"MKNOD dans le permis":         status(ok, ok, rawio, mknod, rawio, rawio, rawio, "1"),
		"MKNOD dans l'hérité":          status(ok, ok, mknod, rawio, rawio, rawio, rawio, "1"),
		"MKNOD dans l'ensemble limite": status(ok, ok, rawio, rawio, rawio, mknod, rawio, "1"),
		"MKNOD dans l'ambiant":         status(ok, ok, rawio, rawio, rawio, rawio, mknod, "1"),
		"RAWIO perdue":                 status(ok, ok, rawio, rawio, "0000000000000000", rawio, rawio, "1"),
		"no_new_privs absent":          status(ok, ok, rawio, rawio, rawio, rawio, rawio, "0"),
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

// Un champ manquant ou illisible fait échouer l'analyse : la vérification
// ne doit jamais passer faute d'avoir pu lire.
func TestParseRejectsIncompleteStatus(t *testing.T) {
	full := status("1\t1\t1\t1", "1\t1\t1\t1", rawio, rawio, rawio, rawio, rawio, "1")
	for _, field := range []string{"Uid", "Gid", "CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb", "NoNewPrivs"} {
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
