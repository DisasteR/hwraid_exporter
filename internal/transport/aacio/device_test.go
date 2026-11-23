package aacio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/DisasteR/hwraid_exporter/internal/aacraid"
	"github.com/DisasteR/hwraid_exporter/internal/allowlist"
)

var devNull = unix.Mkdev(1, 3)

func list(t *testing.T) *allowlist.List {
	t.Helper()
	l, err := aacraid.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// fakeIoctl remplace l'appel système, compte les appels et vérifie que le
// tampon reçu a exactement la taille attendue par le noyau.
func fakeIoctl(t *testing.T, fn func(req uint32, buf []byte) error) *int {
	t.Helper()
	calls := 0
	orig := ioctlFn
	ioctlFn = func(fd int, req uint32, buf []byte) error {
		calls++
		if req == aacraid.IoctlGetHBAInfo && len(buf) != aacraid.HBAInfoSize {
			t.Fatalf("tampon de %d octets transmis, %d attendus", len(buf), aacraid.HBAInfoSize)
		}
		return fn(req, buf)
	}
	t.Cleanup(func() { ioctlFn = orig })
	return &calls
}

func TestOpenChecksDevice(t *testing.T) {
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("pas de /dev/null")
	}
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatalf("ouverture du bon périphérique : %v", err)
	}
	d.Close()
	if _, err := Open("/dev/null", unix.Mkdev(1, 5), list(t)); !errors.Is(err, ErrNodeMismatch) {
		t.Fatalf("mauvais minor : ErrNodeMismatch attendu, obtenu %v", err)
	}
	reg := filepath.Join(t.TempDir(), "fichier")
	if err := os.WriteFile(reg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(reg, devNull, list(t)); !errors.Is(err, ErrNodeMismatch) {
		t.Fatalf("fichier ordinaire : ErrNodeMismatch attendu, obtenu %v", err)
	}
	link := filepath.Join(t.TempDir(), "lien")
	if err := os.Symlink("/dev/null", link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link, devNull, list(t)); !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("lien symbolique : ELOOP attendu (O_NOFOLLOW), obtenu %v", err)
	}
	if _, err := Open("/dev/null", devNull, nil); err == nil {
		t.Fatal("ouverture sans liste blanche acceptée")
	}
}

// Close est idempotent et Do après Close est refusé sans appel système.
func TestCloseIsFinal(t *testing.T) {
	calls := fakeIoctl(t, func(uint32, []byte) error { return nil })
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Skip(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("second Close : %v", err)
	}
	if _, err := d.HBAInfo(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Do après Close : ErrClosed attendu, obtenu %v", err)
	}
	if *calls != 0 {
		t.Fatal("appel système émis après Close")
	}
}

// La requête vérifiée est celle qui part : modifier le tampon de l'appelant
// pendant l'appel n'atteint pas le noyau, et la réponse revient bien.
func TestDoUsesPrivateCopy(t *testing.T) {
	var caller []byte
	fakeIoctl(t, func(_ uint32, buf []byte) error {
		caller[0] = 0xAA // l'appelant écrit dans son tampon pendant l'appel
		if buf[0] == 0xAA {
			t.Fatal("le tampon émis est celui de l'appelant")
		}
		buf[1] = 0x55
		return nil
	})
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Skip(err)
	}
	defer d.Close()
	caller = make([]byte, aacraid.HBAInfoSize)
	if err := d.Do(allowlist.Request{Ioctl: aacraid.IoctlGetHBAInfo, Buf: caller}); err != nil {
		t.Fatal(err)
	}
	if caller[1] != 0x55 {
		t.Fatal("la réponse n'a pas été recopiée")
	}
}

// Une requête hors liste n'atteint jamais l'appel système.
func TestDeniedRequestNeverReachesKernel(t *testing.T) {
	calls := fakeIoctl(t, func(uint32, []byte) error { return nil })
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Skip(err)
	}
	defer d.Close()
	for _, req := range []allowlist.Request{
		{Ioctl: 4<<16 | 2140<<2, Buf: make([]byte, 1)},           // FSACTL_RESET_IOP
		{Ioctl: 4<<16 | 2050<<2, Buf: make([]byte, 512)},         // FSACTL_SENDFIB
		{Ioctl: aacraid.IoctlGetHBAInfo, Buf: make([]byte, 199)}, // bonne commande, mauvaise taille
	} {
		if err := d.Do(req); !errors.Is(err, allowlist.ErrDenied) {
			t.Fatalf("%#x : refus attendu, obtenu %v", req.Ioctl, err)
		}
	}
	if *calls != 0 {
		t.Fatalf("%d appel(s) système émis pour des requêtes refusées", *calls)
	}
}

func TestHBAInfoDecodesKernelResponse(t *testing.T) {
	calls := fakeIoctl(t, func(req uint32, buf []byte) error {
		if req != aacraid.IoctlGetHBAInfo {
			t.Fatalf("ioctl inattendu %#x", req)
		}
		buf[50], buf[51], buf[52] = 0, 1, 0
		binary.LittleEndian.PutUint32(buf[60:], 0x9005)
		return nil
	})
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Skip(err)
	}
	defer d.Close()
	info, err := d.HBAInfo()
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 1 || info.VendorID != 0x9005 || !info.MatchesPCI(0, 1, 0, 0) {
		t.Fatalf("calls=%d info=%+v", *calls, info)
	}
}

// EINTR est remonté comme un échec, sans nouvel essai.
func TestInterruptedCallIsNotRetried(t *testing.T) {
	calls := fakeIoctl(t, func(uint32, []byte) error { return syscall.EINTR })
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Skip(err)
	}
	defer d.Close()
	if _, err := d.HBAInfo(); !errors.Is(err, syscall.EINTR) {
		t.Fatalf("EINTR attendu, obtenu %v", err)
	}
	if *calls != 1 {
		t.Fatalf("%d appels : aucun nouvel essai ne doit avoir lieu", *calls)
	}
}

// Sur /dev/null, le vrai appel système passe et le noyau répond ENOTTY :
// le chemin réel jusqu'à ioctl est exercé.
func TestRealIoctlPath(t *testing.T) {
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Skip(err)
	}
	defer d.Close()
	if _, err := d.HBAInfo(); !errors.Is(err, syscall.ENOTTY) {
		t.Fatalf("ENOTTY attendu de /dev/null, obtenu %v", err)
	}
}

// La requête 706 part par FSACTL_SENDFIB, telle que construite, et la
// réponse écrite par le noyau dans le tampon est décodée.
func TestSupplementInfoRoundTrip(t *testing.T) {
	calls := fakeIoctl(t, func(req uint32, buf []byte) error {
		if req != aacraid.IoctlSendFIB {
			t.Fatalf("ioctl inattendu %#x", req)
		}
		if !bytes.Equal(buf, aacraid.SupplementInfoRequest()) {
			t.Fatal("requête transmise différente de la requête qualifiée")
		}
		// Réponse du firmware : Size inclut l'en-tête.
		binary.LittleEndian.PutUint16(buf[8:], aacraid.FIBSize)
		binary.LittleEndian.PutUint32(buf[aacraid.FIBHeaderSize+0x20:], 15)
		binary.LittleEndian.PutUint32(buf[aacraid.FIBHeaderSize+0x90:], 0x08)
		return nil
	})
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Skip(err)
	}
	defer d.Close()
	s, err := d.SupplementInfo()
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 1 || !s.ArcIoReads() {
		t.Fatalf("calls=%d %+v", *calls, s)
	}
}

// Une réponse que le firmware n'a pas remplie (Size reste à 4) est refusée.
func TestSupplementInfoUnansweredRejected(t *testing.T) {
	fakeIoctl(t, func(uint32, []byte) error { return nil })
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Skip(err)
	}
	defer d.Close()
	if _, err := d.SupplementInfo(); err == nil {
		t.Fatal("réponse vide acceptée")
	}
}

// Protection du cache : la lecture d'état n'est envoyée que si la lecture
// de présence décrit le module ; les deux requêtes sont les requêtes
// qualifiées.
func TestCacheProtectionSequence(t *testing.T) {
	for _, described := range []bool{true, false} {
		var sent [][]byte
		fakeIoctl(t, func(req uint32, buf []byte) error {
			sent = append(sent, append([]byte(nil), buf...))
			le := binary.LittleEndian
			d := buf[aacraid.FIBHeaderSize:]
			le.PutUint32(d[0:], 0) // statut du firmware : succès
			switch {
			case bytes.Equal(buf[:aacraid.FIBHeaderSize], aacraid.CacheProtectionPresenceRequest()[:aacraid.FIBHeaderSize]):
				le.PutUint16(buf[8:], aacraid.FIBHeaderSize+44)
				if described {
					le.PutUint16(d[42:], 0x2000)
				}
			default:
				le.PutUint16(buf[8:], aacraid.FIBHeaderSize+76)
				d[20] = 1
				le.PutUint32(d[48:], 0x40000009)
			}
			return nil
		})
		dev, err := Open("/dev/null", devNull, list(t))
		if err != nil {
			t.Skip(err)
		}
		present, st, err := dev.CacheProtection()
		dev.Close()
		if err != nil || present != described {
			t.Fatalf("décrit=%v : present=%v err=%v", described, present, err)
		}
		want := 1
		if described {
			want = 2
			if st.State != aacraid.CacheProtectionOptimal {
				t.Fatalf("état %+v", st)
			}
		}
		if len(sent) != want || !bytes.Equal(sent[0], aacraid.CacheProtectionPresenceRequest()) ||
			(described && !bytes.Equal(sent[1], aacraid.CacheProtectionStateRequest())) {
			t.Fatalf("décrit=%v : %d requêtes envoyées", described, len(sent))
		}
	}
}

// fakeFirmware sert un transfert paginé de payload, comme le firmware de
// l'hôte de référence (marques 0xc9 / 0xc8 / 0xc6, 448 octets par morceau),
// et journalise les requêtes reçues.
type fakeFirmware struct {
	payload  []byte
	xfer     uint32
	failAt   int // numéro de morceau en erreur (-1 : aucun)
	requests [][]byte
}

func (f *fakeFirmware) ioctl(t *testing.T) {
	fakeIoctl(t, func(req uint32, buf []byte) error {
		f.requests = append(f.requests, append([]byte(nil), buf...))
		le := binary.LittleEndian
		d := buf[aacraid.FIBHeaderSize:]
		sub, n := le.Uint32(d[4:]), le.Uint32(d[24:])
		le.PutUint32(d[0:], 0)
		total := uint32(len(f.payload))
		switch sub {
		case 83:
			if int(n) == f.failAt {
				return syscall.EIO
			}
			le.PutUint32(d[8:], 0xc8)
			off := int(n) * aacraid.ChunkData
			chunk := make([]byte, aacraid.ChunkData)
			if off < len(f.payload) {
				copy(chunk, f.payload[off:])
			}
			copy(d[32:], chunk)
		case 84:
			le.PutUint32(d[8:], 0xc6)
		default: // ouverture
			le.PutUint32(d[8:], 0xc9)
			le.PutUint32(d[12:], total)
			le.PutUint32(d[16:], 0xda)
			le.PutUint32(d[20:], f.xfer)
			le.PutUint32(d[28:], total)
		}
		return nil
	})
}

func (f *fakeFirmware) subs() []uint32 {
	var out []uint32
	for _, r := range f.requests {
		out = append(out, binary.LittleEndian.Uint32(r[aacraid.FIBHeaderSize+4:]))
	}
	return out
}

// La lecture s'arrête dès que les données suffisent et clôture toujours le
// transfert ; chaque requête passe la liste blanche.
func TestReadPagedStopsWhenEnough(t *testing.T) {
	payload := make([]byte, 0x1000)
	for i := range payload {
		payload[i] = byte(i)
	}
	f := &fakeFirmware{payload: payload, xfer: 0x2061e, failAt: -1}
	f.ioctl(t)
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Skip(err)
	}
	defer d.Close()
	data, err := d.ReadPaged(aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return aacraid.ContainerInfoNeed })
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != aacraid.ChunkData || !bytes.Equal(data, payload[:aacraid.ChunkData]) {
		t.Fatalf("%d octets lus", len(data))
	}
	if got := f.subs(); len(got) != 3 || got[0] != 234 || got[1] != 83 || got[2] != 84 {
		t.Fatalf("séquence %v, attendu ouverture, morceau 0, clôture", got)
	}
	// Tout le transfert quand la fonction le demande.
	f.requests = nil
	data, err = d.ReadPaged(aacraid.ReadContainerInfo, 0, 0, func(b []byte) int { return 0x1000 })
	if err != nil || !bytes.Equal(data, payload) || len(f.requests) != 12 {
		t.Fatalf("lecture complète : %d octets, %d requêtes, %v", len(data), len(f.requests), err)
	}
}

// Une erreur au milieu du transfert est remontée, et la clôture est envoyée
// malgré tout.
func TestReadPagedClosesOnError(t *testing.T) {
	f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 7, failAt: 1}
	f.ioctl(t)
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Skip(err)
	}
	defer d.Close()
	if _, err := d.ReadPaged(aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return 0x1000 }); err == nil {
		t.Fatal("erreur de morceau non remontée")
	}
	got := f.subs()
	if got[len(got)-1] != 84 {
		t.Fatalf("pas de clôture après l'erreur : %v", got)
	}
}
