package privilege

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/DisasteR/hwraid_exporter/internal/procstatus"
)

const (
	childEnv    = "HWRAID_PRIV_CHILD"
	dirEnv      = "HWRAID_PRIV_DIR"
	secretEnv   = "HWRAID_PRIV_SECRET"
	requireRoot = "HWRAID_REQUIRE_ROOT_TESTS" // positionnée en CI : un saut devient un échec
	nobody      = 65534
)

var keep = procstatus.Mask(procstatus.CapSysRawIO)

func TestCapDataLayout(t *testing.T) {
	d := capData(keep | 1<<40)
	if d[0].Effective != 0x20000 || d[0].Permitted != 0x20000 || d[0].Inheritable != 0 {
		t.Fatalf("mot bas : %+v", d[0])
	}
	if d[1].Effective != 1<<8 || d[1].Permitted != 1<<8 || d[1].Inheritable != 0 {
		t.Fatalf("mot haut : %+v", d[1])
	}
}

// Sans root, Drop refuse avant toute modification.
func TestDropRequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("test réservé à un utilisateur non root")
	}
	if err := Drop(nobody, nobody, keep); !errors.Is(err, ErrNotRoot) {
		t.Fatalf("erreur attendue ErrNotRoot, obtenu %v", err)
	}
}

// Test réel, en root : l'abandon s'exécute dans un processus enfant, car il
// est irréversible. L'enfant vérifie l'état de tous ses threads, puis tente
// des opérations qui doivent toutes échouer.
func TestDropPrivilegesForReal(t *testing.T) {
	if os.Geteuid() != 0 {
		if os.Getenv(requireRoot) == "1" {
			t.Fatal("test root exigé mais processus non root")
		}
		t.Skip("exige root")
	}
	// Répertoire directement sous /tmp (1777) et en 0777 : l'enfant, une fois
	// en UID 65534, doit pouvoir le traverser. Sinon chaque refus viendrait
	// du chemin (EACCES) et non de l'absence de privilège qu'on veut prouver.
	dir, err := os.MkdirTemp("/tmp", "hwraid-priv-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	// Témoin positif : un fichier lisible par tous doit rester lisible
	// après l'abandon, ce qui prouve que le refus du secret vient de son mode.
	if err := os.WriteFile(filepath.Join(dir, "public"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDropChild$", "-test.v")
	cmd.Env = append(os.Environ(), childEnv+"=1", dirEnv+"="+dir, secretEnv+"="+secret)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "CHILD-OK") {
		t.Fatalf("enfant en échec (%v) :\n%s", err, out)
	}
}

// TestDropChild n'est exécuté que par TestDropPrivilegesForReal.
func TestDropChild(t *testing.T) {
	if os.Getenv(childEnv) != "1" {
		t.Skip("exécuté seulement comme processus enfant")
	}
	// Multiplier les threads système avant l'abandon, pour vérifier que
	// le changement s'applique à tous.
	var ready, release sync.WaitGroup
	for i := 0; i < 8; i++ {
		ready.Add(1)
		release.Add(1)
		go func() {
			runtime.LockOSThread()
			ready.Done()
			release.Wait()
		}()
	}
	ready.Wait()
	defer func() {
		for i := 0; i < 8; i++ {
			release.Done()
		}
	}()

	if err := Drop(nobody, nobody, keep); err != nil {
		t.Fatalf("Drop : %v", err)
	}
	if err := VerifyAllThreads(nobody, nobody, keep); err != nil {
		t.Fatalf("vérification : %v", err)
	}
	tasks, _ := filepath.Glob("/proc/self/task/*")
	if len(tasks) < 9 {
		t.Fatalf("seulement %d threads : le test ne couvre pas le multi-thread", len(tasks))
	}

	dir, secret := os.Getenv(dirEnv), os.Getenv(secretEnv)
	// Créer un nœud de périphérique : refusé faute de CAP_MKNOD.
	err := syscall.Mknod(filepath.Join(dir, "null"), syscall.S_IFCHR|0o600, 1<<8|3)
	if !errors.Is(err, syscall.EPERM) {
		t.Fatalf("mknod : EPERM attendu, obtenu %v", err)
	}
	// Témoin : un fichier lisible par tous reste lisible.
	if _, err := os.ReadFile(filepath.Join(dir, "public")); err != nil {
		t.Fatalf("lecture du témoin public : %v (le répertoire n'est pas traversable)", err)
	}
	// Lire un fichier root en 0600 : refusé, plus de contournement des droits.
	if _, err := os.ReadFile(secret); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("lecture d'un fichier root : refus attendu, obtenu %v", err)
	}
	// Redevenir root : refusé.
	if err := syscall.Setresuid(0, 0, 0); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("setresuid(0) : EPERM attendu, obtenu %v", err)
	}
	t.Log("CHILD-OK")
}
