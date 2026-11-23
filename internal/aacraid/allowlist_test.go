package aacraid

import (
	"errors"
	"math/rand/v2"
	"testing"

	"github.com/DisasteR/hwraid_exporter/internal/allowlist"
)

// ctl reproduit la macro CTL_CODE de aacraid.h.
func ctl(fn, method uint32) uint32 { return 4<<16 | fn<<2 | method }

// Tous les ioctl connus du pilote (aacraid.h), qualifiés ou non.
var knownIoctls = map[string]uint32{
	"FSACTL_SENDFIB":               ctl(2050, 0),
	"FSACTL_SEND_RAW_SRB":          ctl(2067, 0),
	"FSACTL_DELETE_DISK":           0x163,
	"FSACTL_QUERY_DISK":            0x173,
	"FSACTL_OPEN_GET_ADAPTER_FIB":  ctl(2100, 0),
	"FSACTL_GET_NEXT_ADAPTER_FIB":  ctl(2101, 0),
	"FSACTL_CLOSE_GET_ADAPTER_FIB": ctl(2102, 0),
	"FSACTL_MINIPORT_REV_CHECK":    ctl(2107, 0),
	"FSACTL_GET_PCI_INFO":          ctl(2119, 0),
	"FSACTL_FORCE_DELETE_DISK":     ctl(2120, 3),
	"FSACTL_GET_CONTAINERS":        2131,
	"FSACTL_SEND_LARGE_FIB":        ctl(2138, 0),
	"FSACTL_RESET_IOP":             ctl(2140, 0),
	"FSACTL_GET_HBA_INFO":          ctl(2150, 0),
}

// Seul FSACTL_GET_HBA_INFO est qualifié dans le prototype 0.
var expectedAllowed = map[string]bool{"FSACTL_GET_HBA_INFO": true}

func mustList(t *testing.T) *allowlist.List {
	t.Helper()
	l, err := NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// La liste chargée doit être exactement la liste attendue : un ajout non
// prévu fait échouer ce test et impose de le mettre à jour en revue.
func TestAllowlistIsExactlyExpected(t *testing.T) {
	got := mustList(t).Entries()
	if len(got) != len(expectedAllowed) {
		t.Fatalf("%d entrées chargées, %d attendues", len(got), len(expectedAllowed))
	}
	for name := range expectedAllowed {
		if _, ok := got[knownIoctls[name]]; !ok {
			t.Fatalf("%s absent de la liste", name)
		}
	}
}

// Chaque ioctl connu est testé avec la taille de l'entrée qualifiée et
// d'autres tailles : seul GET_HBA_INFO à 200 octets passe.
func TestKnownIoctls(t *testing.T) {
	l := mustList(t)
	for name, code := range knownIoctls {
		for _, size := range []int{0, 1, 4, 199, HBAInfoSize, 201, 512, 4096} {
			_, err := l.Check(allowlist.Request{Ioctl: code, Buf: make([]byte, size)})
			allowed := expectedAllowed[name] && size == HBAInfoSize
			if allowed && err != nil {
				t.Fatalf("%s/%d refusé : %v", name, size, err)
			}
			if !allowed && !errors.Is(err, allowlist.ErrDenied) {
				t.Fatalf("%s/%d accepté à tort (err=%v)", name, size, err)
			}
		}
	}
}

// Test à base de propriétés : sur un million de codes et de tailles tirés au
// hasard, seule la combinaison qualifiée est acceptée.
func TestRandomRequestsDenied(t *testing.T) {
	l := mustList(t)
	r := rand.New(rand.NewPCG(1, 2))
	n := 1_000_000
	if testing.Short() {
		n = 10_000
	}
	buf := make([]byte, 8192)
	for i := 0; i < n; i++ {
		code := r.Uint32()
		if i%4 == 0 { // concentrer une partie des tirages autour des codes aacraid
			code = ctl(r.Uint32N(4096), r.Uint32N(4))
		}
		size := r.IntN(len(buf) + 1)
		_, err := l.Check(allowlist.Request{Ioctl: code, Buf: buf[:size]})
		allowed := code == IoctlGetHBAInfo && size == HBAInfoSize
		if allowed != (err == nil) {
			t.Fatalf("code %#x taille %d : accepté=%v, attendu=%v", code, size, err == nil, allowed)
		}
	}
}

func TestNewRejectsInvalidEntries(t *testing.T) {
	cases := map[string][]allowlist.Entry{
		"sans nom":    {{Ioctl: 1, Size: 1, Proof: "p"}},
		"taille 0":    {{Name: "a", Ioctl: 1, Size: 0, Proof: "p"}},
		"sans preuve": {{Name: "a", Ioctl: 1, Size: 1}},
		"doublon":     {{Name: "a", Ioctl: 1, Size: 1, Proof: "p"}, {Name: "b", Ioctl: 1, Size: 2, Proof: "p"}},
	}
	for name, entries := range cases {
		if _, err := allowlist.New(entries...); err == nil {
			t.Fatalf("%s : liste acceptée", name)
		}
	}
}
