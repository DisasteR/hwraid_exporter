// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

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
			// Errorf : la fonction s'exécute sur le thread des ioctl, pas
			// dans la goroutine du test.
			t.Errorf("tampon de %d octets transmis, %d attendus", len(buf), aacraid.HBAInfoSize)
			return errors.New("taille de tampon")
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
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("second Close : %v", err)
	}
	if _, err := d.HBAInfo(context.Background()); !errors.Is(err, ErrClosed) {
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
		t.Fatal(err)
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
		t.Fatal(err)
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
		for off, v := range map[int]uint32{60: 0x9005, 64: 0x028b, 68: 0x9005, 72: 0x0301} {
			binary.LittleEndian.PutUint32(buf[off:], v)
		}
		return nil
	})
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	info, err := d.HBAInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 1 || info.VendorID != 0x9005 || !info.Matches(0, aacraid.PCIIdentity{Bus: 1, Vendor: 0x9005, Device: 0x028b, SubVendor: 0x9005, SubDevice: 0x0301}) {
		t.Fatalf("calls=%d info=%+v", *calls, info)
	}
}

// EINTR est remonté comme un échec, sans nouvel essai.
func TestInterruptedCallIsNotRetried(t *testing.T) {
	calls := fakeIoctl(t, func(uint32, []byte) error { return syscall.EINTR })
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.HBAInfo(context.Background()); !errors.Is(err, syscall.EINTR) {
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
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.HBAInfo(context.Background()); !errors.Is(err, syscall.ENOTTY) {
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
		t.Fatal(err)
	}
	defer d.Close()
	s, err := d.SupplementInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 1 || !s.ArcIoReads() {
		t.Fatalf("calls=%d %+v", *calls, s)
	}
}

// Les requêtes 703 et 803 partent par FSACTL_SENDFIB, telles que
// construites, et leurs réponses sont décodées ; une réponse non remplie
// (Size de la requête) est refusée.
func TestAdapterInfoAndEnvironmentRoundTrip(t *testing.T) {
	answer := true
	calls := fakeIoctl(t, func(req uint32, buf []byte) error {
		if req != aacraid.IoctlSendFIB {
			t.Fatalf("ioctl inattendu %#x", req)
		}
		le := binary.LittleEndian
		switch {
		case bytes.Equal(buf, aacraid.AdapterInfoRequest()):
			if answer {
				le.PutUint16(buf[8:], 120)
				le.PutUint32(buf[aacraid.FIBHeaderSize+80:], aacraid.SupportedHeatSensor)
			}
		case bytes.Equal(buf, aacraid.EnvironmentRequest()):
			if answer {
				le.PutUint16(buf[8:], aacraid.FIBSize)
				le.PutUint32(buf[aacraid.FIBHeaderSize:], 0)
				le.PutUint16(buf[aacraid.FIBHeaderSize+36:], 49)
				le.PutUint16(buf[aacraid.FIBHeaderSize+40:], 95)
				le.PutUint16(buf[aacraid.FIBHeaderSize+42:], 100)
			}
		default:
			t.Fatal("requête transmise différente des requêtes qualifiées")
		}
		return nil
	})
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	a, err := d.AdapterInfo(context.Background())
	if err != nil || !a.HeatSensor() {
		t.Fatalf("703 : %+v, %v", a, err)
	}
	e, err := d.Environment(context.Background())
	if err != nil || e != (aacraid.Environment{TemperatureCelsius: 49, ThresholdLoCelsius: 95, ThresholdHiCelsius: 100}) {
		t.Fatalf("803 : %+v, %v", e, err)
	}
	answer = false
	if _, err := d.AdapterInfo(context.Background()); err == nil {
		t.Fatal("703 non remplie acceptée")
	}
	if _, err := d.Environment(context.Background()); err == nil {
		t.Fatal("803 non remplie acceptée")
	}
	if *calls != 4 {
		t.Fatalf("%d appels, attendu 4", *calls)
	}
}

// La requête 708 part par FSACTL_SEND_LARGE_FIB, sur un tampon de 2 048
// octets, telle que construite ; une réponse non remplie est refusée.
func TestConnectorMapRoundTrip(t *testing.T) {
	answer := true
	calls := fakeIoctl(t, func(req uint32, buf []byte) error {
		if req != aacraid.IoctlSendLargeFIB || len(buf) != aacraid.LargeFIBSize {
			t.Fatalf("ioctl %#x, %d octets", req, len(buf))
		}
		if !bytes.Equal(buf, aacraid.ConnectorMapRequest()) {
			t.Fatal("requête transmise différente de la requête qualifiée")
		}
		if !answer {
			return nil
		}
		le := binary.LittleEndian
		le.PutUint16(buf[8:], 32+32+16)
		d := buf[aacraid.FIBHeaderSize:]
		le.PutUint32(d[0:], 1)
		d[4], d[5] = 1, 2
		d[8], d[20] = 0, 1
		copy(d[12:], "CN0")
		copy(d[24:], "CN1")
		le.PutUint32(d[32:], 7)
		d[36], d[37], d[38] = 1, 3, 4
		return nil
	})
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	m, err := d.ConnectorMap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := m.Locate(7); *calls != 1 || !ok || e.Connector != 1 || e.Device != 3 || e.Phy != 4 || m.Names[1] != "CN1" {
		t.Fatalf("calls=%d %+v", *calls, m)
	}
	answer = false
	if _, err := d.ConnectorMap(context.Background()); err == nil {
		t.Fatal("réponse vide acceptée")
	}
}

// Une réponse que le firmware n'a pas remplie (Size reste à 4) est refusée.
func TestSupplementInfoUnansweredRejected(t *testing.T) {
	fakeIoctl(t, func(uint32, []byte) error { return nil })
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.SupplementInfo(context.Background()); err == nil {
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
				le.PutUint32(d[48:], 0x4000008b) // module sain observé : « ZMM Optimal »
			}
			return nil
		})
		dev, err := Open("/dev/null", devNull, list(t))
		if err != nil {
			t.Fatal(err)
		}
		rd, err := dev.CacheProtection(context.Background())
		dev.Close()
		if err != nil || rd.Flash != described {
			t.Fatalf("décrit=%v : flash=%v err=%v", described, rd.Flash, err)
		}
		want := 1
		if described {
			want = 2
			if rd.ZMM.State != aacraid.CacheProtectionOptimal {
				t.Fatalf("état %+v", rd.ZMM)
			}
		} else if rd.Battery.State != aacraid.CacheProtectionUnknown || rd.Battery.PresenceKnown {
			// Sans indicateur : batterie décodée, présence non établie.
			t.Fatalf("batterie %+v", rd.Battery)
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
	payload    []byte
	xfer       uint32
	failAt     int // numéro de morceau en erreur (-1 : aucun)
	requests   [][]byte
	openMark   uint32         // marque d'ouverture (0 : 0xc9)
	total      uint32         // taille annoncée (0 : taille de payload)
	stopXfer   uint32         // transfert annoncé à la clôture (0 : écho)
	stopMark   uint32         // marque de la clôture (0 : 0xc6)
	stopMarks  []uint32       // marques des clôtures successives, prioritaires sur stopMark
	chunkMark  uint32         // marque des réponses de morceau (0 : 0xc8)
	chunkSize  uint16         // champ Size des réponses de morceau (0 : 496)
	payloads   [][]byte       // contenus des transferts successifs, prioritaires sur payload
	onStop     func()         // appelé à chaque clôture reçue
	onChunk    func()         // appelé à chaque demande de morceau reçue
	onOpen     func()         // appelé à chaque ouverture reçue
	openCode   uint32         // mot 4 de la réponse d'ouverture (0 : 0xda)
	openSize   uint16         // champ Size de la réponse d'ouverture (0 : 496)
	openSender uint16         // champ SenderSize de la réponse d'ouverture (0 : celui de la requête)
	openErr    error          // erreur de l'ioctl d'ouverture (nil : aucune)
	stopHook   func(d []byte) // modifie la réponse de clôture une fois construite
	stopSize   uint16         // champ Size de la réponse de clôture (0 : 496)
	failErr    error          // erreur du morceau failAt (nil : EIO)
	// chunkHook modifie la réponse au morceau n une fois construite.
	chunkHook func(n uint32, d []byte)
	// openHook modifie la réponse d'ouverture une fois construite.
	openHook func(d []byte)
}

func (f *fakeFirmware) ioctl(t *testing.T) {
	fakeIoctl(t, func(req uint32, buf []byte) error {
		f.requests = append(f.requests, append([]byte(nil), buf...))
		le := binary.LittleEndian
		d := buf[aacraid.FIBHeaderSize:]
		sub, n := le.Uint32(d[4:]), le.Uint32(d[24:])
		le.PutUint32(d[0:], 0)
		total := uint32(len(f.payload))
		if f.total != 0 {
			total = f.total
		}
		switch sub {
		case 83:
			if f.onChunk != nil {
				f.onChunk()
			}
			if int(n) == f.failAt {
				if f.failErr != nil {
					return f.failErr
				}
				return syscall.EIO
			}
			mark, size := uint32(0xc8), uint16(496)
			if f.chunkMark != 0 {
				mark = f.chunkMark
			}
			if f.chunkSize != 0 {
				size = f.chunkSize
			}
			le.PutUint32(d[8:], mark)
			le.PutUint16(buf[8:], size)
			// Comme la carte : 432 octets de données par morceau (valeur
			// littérale, indépendante du code), puis des octets que la carte
			// n'écrit pas, ici des parasites que le transport doit ignorer.
			const perChunk = 432
			off := int(n) * perChunk
			chunk := make([]byte, perChunk)
			if off < len(f.payload) {
				copy(chunk, f.payload[off:])
			}
			copy(d[32:], chunk)
			for i := 32 + perChunk; i < len(d); i++ {
				d[i] = 0xee
			}
			if f.chunkHook != nil {
				f.chunkHook(n, d)
			}
		case 84:
			if f.onStop != nil {
				f.onStop()
			}
			le.PutUint16(buf[8:], 496)
			if f.stopSize != 0 {
				le.PutUint16(buf[8:], f.stopSize)
			}
			le.PutUint32(d[8:], 0xc6)
			if f.stopMark != 0 {
				le.PutUint32(d[8:], f.stopMark)
			}
			if len(f.stopMarks) > 0 {
				le.PutUint32(d[8:], f.stopMarks[0])
				f.stopMarks = f.stopMarks[1:]
			}
			if f.stopXfer != 0 {
				le.PutUint32(d[20:], f.stopXfer)
			}
			if f.stopHook != nil {
				f.stopHook(d)
			}
		default: // ouverture
			if f.onOpen != nil {
				f.onOpen()
			}
			if f.openErr != nil {
				return f.openErr
			}
			if len(f.payloads) > 0 {
				f.payload = f.payloads[0]
				f.payloads = f.payloads[1:]
				total = uint32(len(f.payload))
			}
			mark := uint32(0xc9)
			if f.openMark != 0 {
				mark = f.openMark
			}
			le.PutUint32(d[8:], mark)
			le.PutUint32(d[12:], total)
			le.PutUint16(buf[8:], 496)
			if f.openSize != 0 {
				le.PutUint16(buf[8:], f.openSize)
			}
			if f.openSender != 0 {
				le.PutUint16(buf[10:], f.openSender)
			}
			code := uint32(0xda)
			if f.openCode != 0 {
				code = f.openCode
			}
			le.PutUint32(d[16:], code)
			le.PutUint32(d[20:], f.xfer)
			le.PutUint32(d[28:], total)
			if f.openHook != nil {
				f.openHook(d)
			}
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
		t.Fatal(err)
	}
	defer d.Close()
	d.SetValidatedCouple(true) // écarts E1 et E2 du couple validé
	data, err := d.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return aacraid.ContainerInfoNeed })
	if err != nil {
		t.Fatal(err)
	}
	// Premier morceau seul : 432 octets (valeur littérale), sans parasite.
	if len(data) != 432 || !bytes.Equal(data, payload[:432]) {
		t.Fatalf("%d octets lus", len(data))
	}
	if got := f.subs(); len(got) != 3 || got[0] != 234 || got[1] != 83 || got[2] != 84 {
		t.Fatalf("séquence %v, attendu ouverture, morceau 0, clôture", got)
	}
	// Tout le transfert quand la fonction le demande.
	f.requests = nil
	data, err = d.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, func(b []byte) int { return 0x1000 })
	if err != nil || !bytes.Equal(data, payload) || len(f.requests) != 12 {
		t.Fatalf("lecture complète : %d octets, %d requêtes, %v", len(data), len(f.requests), err)
	}
}

// Une erreur d'ioctl au milieu du transfert est remontée et le transfert
// est abandonné sans clôture (CDC, section 6.11, abandon sans clôture) :
// aucune commande de ce transfert ne suit le morceau en échec.
func TestReadPagedAbandonsOnChunkError(t *testing.T) {
	f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 7, failAt: 1}
	f.ioctl(t)
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	_, err = d.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return 0x1000 })
	if err == nil || !errors.Is(err, syscall.EIO) {
		t.Fatalf("erreur de morceau non remontée : %v", err)
	}
	// Ouverture, morceau 0, morceau 1 (en échec) : rien d'autre.
	if got := f.subs(); !slices.Equal(got, []uint32{234, 83, 83}) {
		t.Fatalf("séquence %v, attendu [234 83 83] sans clôture", got)
	}
}

// Ouverture rejetée : clôture envoyée si la taille annoncée le permet,
// sinon erreur explicite ; données insuffisantes en fin de transfert :
// erreur, jamais une observation tronquée.
func TestReadPagedRejectedOpenAndShortData(t *testing.T) {
	open := func(t *testing.T) *Device {
		d, err := Open("/dev/null", devNull, list(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		return d
	}
	need := func([]byte) int { return aacraid.ContainerInfoNeed }

	f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1, openMark: 0xc8}
	f.ioctl(t)
	if _, err := open(t).ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, need); err == nil {
		t.Fatal("ouverture à marque inattendue acceptée")
	}
	// Ouverture non conforme : aucune clôture sur un identifiant non prouvé.
	if got := f.subs(); len(got) != 1 {
		t.Fatalf("requête envoyée après une ouverture rejetée : %v", got)
	}

	// Rejet propre à l'exporter puis clôture confirmée par 0xc5 : la lecture
	// est déjà en échec, la confirmation n'est pas comptée (pas de relecture,
	// pas de clôture non confirmée imputée à une lecture valide).
	for _, validated := range []bool{false, true} {
		fc := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1, chunkMark: 0xc9, stopMark: aacraid.MarkClosedOther}
		fc.ioctl(t)
		dv := open(t)
		dv.SetValidatedCouple(validated)
		if _, err := dv.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, need); err == nil {
			t.Fatalf("validé %v : morceau anormal accepté", validated)
		}
		if got := fc.subs(); !slices.Equal(got, []uint32{234, 83, 84}) || dv.CloseUnconfirmed() != 0 {
			t.Fatalf("validé %v : séquence %v, clôtures non confirmées comptées %d", validated, got, dv.CloseUnconfirmed())
		}
	}

	// Morceau de marque inattendue, ou réponse incomplète (Size) : refusé
	// par un contrôle propre à l'exporter (statut et écho conformes), puis
	// transfert clôturé après le morceau 0, comme l'outil l'aurait atteint.
	for name, ff := range map[string]*fakeFirmware{
		"marque": {payload: make([]byte, 0x1000), xfer: 9, failAt: -1, chunkMark: 0xc9},
		"taille": {payload: make([]byte, 0x1000), xfer: 9, failAt: -1, chunkSize: 100},
	} {
		ff.ioctl(t)
		if _, err := open(t).ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, need); err == nil {
			t.Fatalf("morceau anormal (%s) accepté", name)
		}
		if got := ff.subs(); !slices.Equal(got, []uint32{234, 83, 84}) {
			t.Fatalf("%s : séquence %v, attendu ouverture, morceau et clôture", name, got)
		}
		if stop := ff.requests[len(ff.requests)-1]; !bytes.Equal(stop, aacraid.StopRequest(0x1000, 9, 0)) {
			t.Fatalf("%s : clôture %x", name, stop[:64])
		}
	}

	f = &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1, total: 0x2000}
	f.ioctl(t)
	if _, err := open(t).ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, need); err == nil {
		t.Fatal("taille hors bornes acceptée")
	}
	if got := f.subs(); len(got) != 1 {
		t.Fatalf("requête envoyée après une taille hors bornes : %v", got)
	}

	// Transfert complet plus court que les champs demandés : données
	// rejetées, transfert clôturé.
	f = &fakeFirmware{payload: make([]byte, 100), xfer: 9, failAt: -1}
	f.ioctl(t)
	if _, err := open(t).ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, need); err == nil {
		t.Fatal("données insuffisantes acceptées")
	}
	if got := f.subs(); !slices.Equal(got, []uint32{234, 83, 84}) {
		t.Fatalf("séquence %v, attendu ouverture, morceau et clôture", got)
	}
}

// Chaque écart sur un morceau, après un morceau 0 conforme : un cas par
// contrôle du transport, mots de réponse aux positions de la capture
// (statut 0, écho 4, total 12, identifiant 20, numéro 24). Échec d'échange
// (ioctl, statut, écho) : abandon sans clôture, comme l'outil sur une
// exception de morceau. Écart que seul l'exporter contrôle : clôture, que
// l'outil (qui ne contrôle que statut et écho) aurait atteinte, formée de
// l'ouverture validée et du numéro du morceau reçu (1), jamais des mots du
// morceau rejeté.
func TestReadPagedAbandonsOnEachChunkFault(t *testing.T) {
	le := binary.LittleEndian
	at1 := func(off int, v func(uint32) uint32) func(uint32, []byte) {
		return func(n uint32, d []byte) {
			if n == 1 {
				le.PutUint32(d[off:], v(le.Uint32(d[off:])))
			}
		}
	}
	exchange := map[string]bool{"EINTR": true, "statut": true, "écho": true}
	for name, f := range map[string]*fakeFirmware{
		"EINTR":       {failAt: 1, failErr: syscall.EINTR},
		"statut":      {failAt: -1, chunkHook: at1(0, func(uint32) uint32 { return 1 })},
		"écho":        {failAt: -1, chunkHook: at1(4, func(uint32) uint32 { return 82 })},
		"total":       {failAt: -1, chunkHook: at1(12, func(v uint32) uint32 { return v - 4 })},
		"identifiant": {failAt: -1, chunkHook: at1(20, func(v uint32) uint32 { return v + 1 })},
		"numéro":      {failAt: -1, chunkHook: at1(24, func(v uint32) uint32 { return v + 1 })},
		"marque":      {failAt: -1, chunkHook: at1(8, func(uint32) uint32 { return 0xc9 })},
		// Mots repris par l'outil dans la requête suivante : valeurs
		// observées sur d'autres lectures de l'outil (0xc0, mot 7 nul).
		"code":  {failAt: -1, chunkHook: at1(16, func(uint32) uint32 { return 0xc0 })},
		"mot 7": {failAt: -1, chunkHook: at1(28, func(uint32) uint32 { return 0 })},
	} {
		t.Run(name, func(t *testing.T) {
			f.payload, f.xfer = make([]byte, 0x1000), 9
			f.ioctl(t)
			d, err := Open("/dev/null", devNull, list(t))
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			// Tout le transfert est demandé : sans l'écart, 10 morceaux.
			data, err := d.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return 0x1000 })
			if err == nil || data != nil || errors.Is(err, ErrStopped) {
				t.Fatalf("err=%v, données=%v", err, data != nil)
			}
			if exchange[name] {
				if got := f.subs(); !slices.Equal(got, []uint32{234, 83, 83}) {
					t.Fatalf("séquence %v, attendu [234 83 83] sans clôture", got)
				}
				if !errors.Is(err, aacraid.ErrExchange) {
					t.Fatalf("échec d'échange attendu : %v", err)
				}
				return
			}
			if got := f.subs(); !slices.Equal(got, []uint32{234, 83, 83, 84}) {
				t.Fatalf("séquence %v, attendu [234 83 83 84] avec clôture", got)
			}
			if stop := f.requests[len(f.requests)-1]; !bytes.Equal(stop, aacraid.StopRequest(0x1000, 9, 1)) {
				t.Fatalf("clôture %x, attendu celle de l'ouverture validée après le morceau 1", stop[:64])
			}
			if errors.Is(err, aacraid.ErrExchange) {
				t.Fatalf("rejet propre à l'exporter classé échec d'échange : %v", err)
			}
		})
	}
}

// Ouvertures des lignes 2, 4 et 6 du déroulement (code 0xe2, total nul,
// code atypique), et code 0xe2 sous la marque 0xc8 : clôture sans morceau,
// mots 2 à 7 repris de la réponse d'ouverture comme l'outil de référence,
// sur le couple validé comme ailleurs ; la lecture est en échec. Une
// ouverture dont la taille diffère du total n'est pas close (aucune
// commande).
func TestReadPagedStopsNonNominalOpen(t *testing.T) {
	for name, tc := range map[string]struct {
		f    func() *fakeFirmware
		stop bool
	}{
		"ligne 2, 0xe2": {func() *fakeFirmware { return &fakeFirmware{payload: make([]byte, 0x408), openCode: 0xe2} }, true},
		"0xe2 sous la marque 0xc8": {func() *fakeFirmware {
			return &fakeFirmware{payload: make([]byte, 0x1000), openCode: 0xe2, openMark: 0xc8}
		}, true},
		"ligne 4, total nul": {func() *fakeFirmware { return &fakeFirmware{payload: []byte{}} }, true},
		"ligne 6, code 0xdb": {func() *fakeFirmware { return &fakeFirmware{payload: make([]byte, 0x1000), openCode: 0xdb} }, true},
		"mot 7 différent du total": {func() *fakeFirmware {
			return &fakeFirmware{payload: make([]byte, 0x1000), openHook: func(d []byte) { binary.LittleEndian.PutUint32(d[28:], 0x800) }}
		}, false},
		"0xe2, mot 7 différent du total": {func() *fakeFirmware {
			return &fakeFirmware{payload: make([]byte, 0x1000), openCode: 0xe2, openHook: func(d []byte) { binary.LittleEndian.PutUint32(d[28:], 0x800) }}
		}, false},
	} {
		for _, validated := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, validé %v", name, validated), func(t *testing.T) {
				f := tc.f()
				f.xfer, f.failAt = 0x2061e, -1
				f.ioctl(t)
				d, err := Open("/dev/null", devNull, list(t))
				if err != nil {
					t.Fatal(err)
				}
				defer d.Close()
				d.SetValidatedCouple(validated)
				data, err := d.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return aacraid.ContainerInfoNeed })
				if err == nil || data != nil || errors.Is(err, allowlist.ErrDenied) {
					t.Fatalf("ouverture acceptée ou refusée trop tard : %v", err)
				}
				if !tc.stop {
					if got := f.subs(); !slices.Equal(got, []uint32{234}) {
						t.Fatalf("séquence %v, attendu l'ouverture seule", got)
					}
					return
				}
				if got := f.subs(); !slices.Equal(got, []uint32{234, 84}) {
					t.Fatalf("séquence %v, attendu l'ouverture puis la clôture", got)
				}
				// Clôture : en-tête de la requête d'ouverture, mots 0 et 1 =
				// 2, 0x54, mots 2 à 7 = marque, total, code, transfert,
				// morceau 0 et taille de la réponse d'ouverture servie.
				le := binary.LittleEndian
				mark, code, total := uint32(0xc9), uint32(0xda), uint32(len(f.payload))
				if f.openMark != 0 {
					mark = f.openMark
				}
				if f.openCode != 0 {
					code = f.openCode
				}
				want := make([]byte, aacraid.FIBSize)
				copy(want, f.requests[0][:aacraid.FIBHeaderSize])
				for k, v := range []uint32{2, 0x54, mark, total, code, 0x2061e, 0, total} {
					le.PutUint32(want[aacraid.FIBHeaderSize+4*k:], v)
				}
				if !bytes.Equal(f.requests[1], want) {
					t.Fatalf("clôture\n got %x\nwant %x", f.requests[1][:64], want[:64])
				}
			})
		}
	}
}

// Hors du couple validé (réglage par défaut), le transport suit l'outil de
// référence (CDC, section 6.11, colonne « autre couple ») : tous les
// morceaux puis la clôture même si les données suffisent (pas d'E1) ; à
// l'arrêt après l'ouverture, morceaux restants puis clôture, lecture en
// échec ; clôture 0xc5 : lecture rejetée, non refaite (pas d'E2).
func TestReadPagedOutsideValidatedPair(t *testing.T) {
	ten := append(append([]uint32{234}, slices.Repeat([]uint32{83}, 10)...), 84)
	t.Run("données suffisantes", func(t *testing.T) {
		f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1}
		f.ioctl(t)
		d, err := Open("/dev/null", devNull, list(t))
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		data, err := d.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return aacraid.ContainerInfoNeed })
		if err != nil || len(data) != 0x1000 {
			t.Fatalf("err=%v, %d octets", err, len(data))
		}
		if got := f.subs(); !slices.Equal(got, ten) {
			t.Fatalf("séquence %v, attendu les 10 morceaux puis la clôture", got)
		}
	})
	t.Run("arrêt après l'ouverture", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1, onOpen: cancel}
		f.ioctl(t)
		d, err := Open("/dev/null", devNull, list(t))
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		data, err := d.ReadPaged(ctx, aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return aacraid.ContainerInfoNeed })
		if !errors.Is(err, ErrStopped) || data != nil {
			t.Fatalf("err=%v, données=%v", err, data != nil)
		}
		if got := f.subs(); !slices.Equal(got, ten) {
			t.Fatalf("séquence %v, attendu les 10 morceaux puis la clôture", got)
		}
	})
	// Arrêt après le morceau 1 (déjà reçu) : la suite de l'outil va au
	// bout dans la borne stopGrace ; un morceau qui rend la main après la
	// borne arrête tout nouvel envoi, sans clôture.
	for name, slow := range map[string]time.Duration{"arrêt après un morceau": 0, "borne dépassée": 60 * time.Millisecond} {
		t.Run(name, func(t *testing.T) {
			old := stopGrace
			stopGrace = 20 * time.Millisecond
			defer func() { stopGrace = old }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			chunks := 0
			f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1, onChunk: func() {
				if chunks++; chunks == 2 {
					cancel()
					time.Sleep(slow)
				}
			}}
			f.ioctl(t)
			d, err := Open("/dev/null", devNull, list(t))
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			data, err := d.ReadPaged(ctx, aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return aacraid.ContainerInfoNeed })
			if err == nil || data != nil {
				t.Fatalf("err=%v, données=%v", err, data != nil)
			}
			want := ten
			if slow > 0 {
				want = []uint32{234, 83, 83}
			}
			if got := f.subs(); !slices.Equal(got, want) {
				t.Fatalf("séquence %v, attendu %v", got, want)
			}
		})
	}
	// Fenêtre d'arrêt (stopGrace) comptée depuis l'arrêt et vérifiée à
	// l'admission, sur le thread dédié : ouverture qui rend la main après la
	// fenêtre, arrêt par signal ou par échéance (aucun morceau) ; thread
	// dédié occupé au-delà de la fenêtre avant l'admission d'un morceau
	// (refusé, même si l'appelant l'a confié à temps) ; clôture après la
	// fenêtre (refusée). Chaque refus porte errGraceExceeded et abandonne
	// sans tentative de clôture.
	for name, tc := range map[string]struct {
		payload  int
		deadline bool
		setup    func(f *fakeFirmware, cancel func())
		want     []uint32
	}{
		"ouverture tardive, signal": {0x1000, false, func(f *fakeFirmware, cancel func()) {
			f.onOpen = func() { cancel(); time.Sleep(60 * time.Millisecond) }
		}, []uint32{234}},
		"ouverture tardive, échéance": {0x1000, true, func(f *fakeFirmware, _ func()) {
			f.onOpen = func() { time.Sleep(90 * time.Millisecond) }
		}, []uint32{234}},
		"admission tardive sur le thread": {0x1000, false, func(f *fakeFirmware, cancel func()) {
			chunks := 0
			f.onChunk = func() {
				if chunks++; chunks == 2 {
					cancel()
					armed := true
					admitting = func() {
						if armed {
							armed = false
							time.Sleep(60 * time.Millisecond)
						}
					}
				}
			}
		}, []uint32{234, 83, 83}},
		"clôture tardive": {100, false, func(f *fakeFirmware, cancel func()) {
			f.onChunk = func() { cancel(); time.Sleep(60 * time.Millisecond) }
		}, []uint32{234, 83}},
	} {
		t.Run(name, func(t *testing.T) {
			old := stopGrace
			stopGrace = 20 * time.Millisecond
			defer func() { stopGrace, admitting = old, nil }()
			ctx, cancel := context.WithCancel(context.Background())
			if tc.deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
			}
			defer cancel()
			f := &fakeFirmware{payload: make([]byte, tc.payload), xfer: 9, failAt: -1}
			tc.setup(f, cancel)
			f.ioctl(t)
			d, err := Open("/dev/null", devNull, list(t))
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			data, err := d.ReadPaged(ctx, aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return 100 })
			// Transfert ouvert, commande suivante refusée : fin non établie.
			if !errors.Is(err, errGraceExceeded) || !errors.Is(err, aacraid.ErrTransferUnsettled) || data != nil {
				t.Fatalf("err=%v, données=%v", err, data != nil)
			}
			if got := f.subs(); !slices.Equal(got, tc.want) {
				t.Fatalf("séquence %v, attendu %v", got, tc.want)
			}
		})
	}
	// Clôture admise dans la fenêtre, qui rend EIO après sa fin : la
	// clôture est partie, son erreur est rendue telle quelle.
	t.Run("clôture admise, EIO tardif", func(t *testing.T) {
		old := stopGrace
		stopGrace = 20 * time.Millisecond
		defer func() { stopGrace = old }()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f := &fakeFirmware{payload: make([]byte, 100), xfer: 9, failAt: -1, onChunk: cancel}
		f.onStop = func() { time.Sleep(60 * time.Millisecond) }
		f.ioctl(t)
		fakeStopErr(t, f)
		d, err := Open("/dev/null", devNull, list(t))
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		_, err = d.ReadPaged(ctx, aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return 100 })
		if !errors.Is(err, syscall.EIO) || errors.Is(err, errGraceExceeded) {
			t.Fatalf("err=%v, attendu EIO de la clôture", err)
		}
		if got := f.subs(); !slices.Equal(got, []uint32{234, 83, 84}) {
			t.Fatalf("séquence %v", got)
		}
	})
	t.Run("clôture 0xc5", func(t *testing.T) {
		f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1, stopMarks: []uint32{0xc5, 0xc6}}
		f.ioctl(t)
		d, err := Open("/dev/null", devNull, list(t))
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		data, err := d.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return aacraid.ContainerInfoNeed })
		if err == nil || data != nil || d.CloseUnconfirmed() != 1 {
			t.Fatalf("err=%v, données=%v, rejets %d", err, data != nil, d.CloseUnconfirmed())
		}
		if got := f.subs(); !slices.Equal(got, ten) {
			t.Fatalf("séquence %v, attendu une seule lecture", got)
		}
	})
}

// Ouverture nominale (0xc9, 0xda, total valide) portant l'identifiant
// sentinelle 0xffffffff : aucun transfert établi, aucune commande ensuite
// (ligne 1 du déroulement).
func TestReadPagedSentinelTransferSendsNothing(t *testing.T) {
	f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: aacraid.XferNone, failAt: -1}
	f.ioctl(t)
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// Refus par le transport dès l'ouverture, pas seulement par la liste
	// blanche au morceau suivant.
	_, err = d.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return aacraid.ContainerInfoNeed })
	if err == nil || errors.Is(err, allowlist.ErrDenied) {
		t.Fatalf("ouverture sur l'identifiant sentinelle : %v", err)
	}
	if got := f.subs(); !slices.Equal(got, []uint32{234}) {
		t.Fatalf("séquence %v, attendu l'ouverture seule", got)
	}
}

// Une clôture confirmée pour un autre transfert est une erreur.
func TestReadPagedStopMustMatchTransfer(t *testing.T) {
	f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1, stopXfer: 10}
	f.ioctl(t)
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return aacraid.ContainerInfoNeed }); err == nil {
		t.Fatal("clôture d'un autre transfert acceptée")
	}
}

// Clôture confirmée par 0xc5 : l'outil de référence rejette la lecture.
// Elle est rejetée et refaite une fois ; un second rejet est une erreur ;
// 0xc5 pour un autre transfert, ou une autre marque, est une erreur sans
// nouvelle tentative.
func TestReadPagedCloseOtherMark(t *testing.T) {
	need := func([]byte) int { return aacraid.ContainerInfoNeed }
	// Contenus distincts d'un transfert à l'autre : seules les données du
	// transfert confirmé par 0xc6 (0x42) peuvent être rendues.
	first, second := make([]byte, 0x1000), make([]byte, 0x1000)
	first[5], second[5] = 0x11, 0x42
	for _, c := range []struct {
		name     string
		marks    []uint32
		xfer     uint32
		ok       bool
		rejected int
		requests int
	}{
		{"0xc6", []uint32{0xc6}, 0, true, 0, 3},
		{"0xc5 puis 0xc6", []uint32{0xc5, 0xc6}, 0, true, 1, 6},
		{"0xc5 deux fois", []uint32{0xc5, 0xc5}, 0, false, 2, 6},
		{"0xc5, autre transfert", []uint32{0xc5}, 10, false, 0, 3},
		{"0xc7", []uint32{0xc7}, 0, false, 0, 3},
	} {
		f := &fakeFirmware{payloads: [][]byte{second}, xfer: 9, failAt: -1, stopMarks: c.marks, stopXfer: c.xfer}
		if len(c.marks) > 1 {
			f.payloads = [][]byte{first, second}
		}
		f.ioctl(t)
		d, err := Open("/dev/null", devNull, list(t))
		if err != nil {
			t.Fatal(err)
		}
		d.SetValidatedCouple(true) // relecture après 0xc5 (E2)
		data, err := d.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, need)
		if (err == nil) != c.ok || (c.ok && data[5] != 0x42) || (!c.ok && data != nil) {
			t.Fatalf("%s : err=%v, données=%v", c.name, err, data != nil)
		}
		if d.CloseUnconfirmed() != c.rejected || len(f.requests) != c.requests {
			t.Fatalf("%s : %d rejets, %d requêtes", c.name, d.CloseUnconfirmed(), len(f.requests))
		}
		d.Close()
	}
}

// Arrêt demandé pendant une lecture paginée close par 0xc5 : le transfert
// engagé est clos, mais aucune seconde ouverture n'est envoyée.
func TestReadPagedNoRetryAfterStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1, stopMarks: []uint32{0xc5, 0xc6}, onStop: cancel}
	f.ioctl(t)
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetValidatedCouple(true) // écarts E1 et E2 du couple validé
	data, err := d.ReadPaged(ctx, aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return aacraid.ContainerInfoNeed })
	if !errors.Is(err, ErrStopped) || data != nil {
		t.Fatalf("err=%v, données=%v", err, data != nil)
	}
	if got := f.subs(); len(got) != 3 || got[2] != 84 {
		t.Fatalf("requêtes après l'arrêt : %v", got)
	}
}

// Arrêt déjà demandé : aucune requête n'atteint le noyau, quelle que soit
// la lecture.
func TestStoppedContextSendsNothing(t *testing.T) {
	n := fakeIoctl(t, func(uint32, []byte) error { return nil })
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e1 := d.SupplementInfo(ctx)
	_, e2 := d.ConnectorMap(ctx)
	_, e3 := d.CacheProtection(ctx)
	_, e4 := d.ReadPaged(ctx, aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return aacraid.ContainerInfoNeed })
	_, e5 := d.AdapterInfo(ctx)
	_, e6 := d.Environment(ctx)
	_, e7 := d.Paused(ctx)
	_, e8 := d.Revision(ctx)
	for i, e := range []error{e1, e2, e3, e4, e5, e6, e7, e8} {
		if !errors.Is(e, ErrStopped) {
			t.Fatalf("lecture %d : ErrStopped attendu, obtenu %v", i+1, e)
		}
	}
	if *n != 0 {
		t.Fatalf("%d requête(s) envoyée(s) après l'arrêt", *n)
	}
}

// Arrêt demandé pendant la lecture de présence du module : la lecture
// d'état n'est pas envoyée.
func TestCacheProtectionStopsBetweenReads(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sent := 0
	fakeIoctl(t, func(req uint32, buf []byte) error {
		sent++
		cancel()
		le := binary.LittleEndian
		d := buf[aacraid.FIBHeaderSize:]
		le.PutUint32(d[0:], 0)
		le.PutUint16(buf[8:], aacraid.FIBHeaderSize+44)
		le.PutUint16(d[42:], 0x2000) // module décrit
		return nil
	})
	dev, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	rd, err := dev.CacheProtection(ctx)
	if !errors.Is(err, ErrStopped) || !rd.Flash || sent != 1 {
		t.Fatalf("flash=%v err=%v, %d requête(s)", rd.Flash, err, sent)
	}
}

// Arrêt demandé pendant un transfert de plusieurs morceaux : aucun autre
// morceau n'est demandé, le transfert est clos aussitôt.
func TestReadPagedStopsMidTransfer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1, onChunk: cancel}
	f.ioctl(t)
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetValidatedCouple(true) // écarts E1 et E2 du couple validé
	// Toute la charge est demandée : sans arrêt, 10 morceaux seraient lus.
	data, err := d.ReadPaged(ctx, aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return 0x1000 })
	if !errors.Is(err, ErrStopped) || data != nil {
		t.Fatalf("err=%v, données=%v", err, data != nil)
	}
	if got := f.subs(); len(got) != 3 || got[1] != 83 || got[2] != 84 {
		t.Fatalf("requêtes : %v (ouverture, un morceau, clôture attendus)", got)
	}
}

// Arrêt pendant l'ouverture d'une lecture qui tient dans un morceau : la
// séquence qualifiée la plus courte (morceau 0, clôture) part, mais la
// lecture n'est pas rendue comme réussie.
func TestReadPagedStopDuringOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1, onOpen: cancel}
	f.ioctl(t)
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetValidatedCouple(true) // écarts E1 et E2 du couple validé
	data, err := d.ReadPaged(ctx, aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return aacraid.ContainerInfoNeed })
	if !errors.Is(err, ErrStopped) || data != nil {
		t.Fatalf("err=%v, données=%v", err, data != nil)
	}
	if got := f.subs(); len(got) != 3 || got[1] != 83 || got[2] != 84 {
		t.Fatalf("requêtes : %v (ouverture, morceau 0, clôture attendus)", got)
	}
}

// queuedCall occupe le thread dédié, lance call, attend qu'elle soit mise en
// file (le contrôle de l'appelant est franchi), exécute between, puis libère
// le thread et rend l'erreur de call. Sur tous les chemins, échec compris,
// le thread est libéré et la fin de call attendue avant que le crochet ne
// soit rétabli et que le transport ne soit fermé par l'appelant.
func queuedCall(t *testing.T, d *Device, call func() error, between func()) error {
	t.Helper()
	busy, release := make(chan struct{}), make(chan struct{})
	go d.thread.run(func() { close(busy); <-release }) // commande précédente en cours
	<-busy
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	inQueue := make(chan struct{})
	queued = func() { close(inQueue) }
	errc := make(chan error, 1)
	go func() { errc <- call() }()
	finished := false
	defer func() {
		free()
		if !finished {
			select {
			case <-errc:
			case <-time.After(5 * time.Second):
			}
		}
		queued = nil
	}()
	select {
	case <-inQueue:
	case err := <-errc:
		finished = true
		t.Fatalf("commande refusée avant sa mise en file : %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("commande jamais mise en file")
	}
	between()
	free()
	err := <-errc
	finished = true
	return err
}

// Arrêt demandé alors que la commande attend son tour sur le thread dédié
// (après le contrôle fait par l'appelant) : le contrôle fait sur le thread,
// juste avant l'appel système, la refuse ; rien n'atteint le noyau.
func TestStopCheckedOnIoctlThread(t *testing.T) {
	calls := fakeIoctl(t, func(uint32, []byte) error { return nil })
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithCancel(context.Background())
	err = queuedCall(t, d, func() error { _, err := d.SupplementInfo(ctx); return err }, cancel)
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("ErrStopped attendu, obtenu %v", err)
	}
	if *calls != 0 {
		t.Fatalf("%d appel(s) système après l'arrêt", *calls)
	}
}

// clockOnly : contexte dont l'échéance est fixée mais dont le minuteur ne
// s'est pas encore exécuté (Err reste nil), comme juste après l'échéance.
type clockOnly struct {
	context.Context
	deadline time.Time
}

func (c clockOnly) Deadline() (time.Time, bool) { return c.deadline, true }
func (clockOnly) Err() error                    { return nil }

// L'échéance passe pendant que la commande attend son tour sur le thread
// dédié, après le contrôle de l'appelant : le contrôle fait sur le thread la
// compare à l'horloge et refuse la commande ; rien n'atteint le noyau.
func TestAdmitChecksDeadlineClock(t *testing.T) {
	calls := fakeIoctl(t, func(uint32, []byte) error { return nil })
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := clockOnly{context.Background(), time.Now().Add(500 * time.Millisecond)}
	err = queuedCall(t, d, func() error { _, err := d.SupplementInfo(ctx); return err }, func() {
		time.Sleep(time.Until(ctx.deadline) + 10*time.Millisecond) // échéance passée, Err toujours nil
	})
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("ErrStopped attendu, obtenu %v", err)
	}
	if *calls != 0 {
		t.Fatalf("%d appel(s) système après l'échéance", *calls)
	}
}

// Réponse d'ouverture ou de clôture plus courte que celle de la carte (champ
// Size) : ses champs pourraient venir de la requête. Ouverture tronquée :
// ni morceau ni clôture ; clôture tronquée : lecture en échec, aucune donnée.
func TestReadPagedTruncatedResponses(t *testing.T) {
	need := func([]byte) int { return aacraid.ContainerInfoNeed }
	// open : nœud ouvert sur le faux firmware, fermé par le nettoyage du
	// sous-test (avant le rétablissement de l'ioctl réel), échec compris.
	open := func(t *testing.T, f *fakeFirmware) *Device {
		f.ioctl(t)
		d, err := Open("/dev/null", devNull, list(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		return d
	}
	t.Run("ouverture", func(t *testing.T) {
		f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1, openSize: aacraid.FIBHeaderSize + 16}
		d := open(t, f)
		if _, err := d.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, need); err == nil {
			t.Fatal("ouverture tronquée acceptée")
		}
		if got := f.subs(); len(got) != 1 {
			t.Fatalf("requêtes après une ouverture tronquée : %v", got)
		}
	})
	t.Run("clôture", func(t *testing.T) {
		f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1, stopSize: aacraid.FIBHeaderSize + 16}
		d := open(t, f)
		if data, err := d.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, need); err == nil || data != nil {
			t.Fatalf("clôture tronquée acceptée (err=%v)", err)
		}
	})
}

// Morceau en échec : abandon sans clôture, donc ni réponse 0xc5 ni nouvel
// essai, même si la carte confirmerait une clôture par 0xc5. Seule une
// lecture dont les morceaux sont conformes est refaite après 0xc5 (E2).
func TestReadPagedNoRetryAfterChunkError(t *testing.T) {
	need := func([]byte) int { return 0x1000 }
	f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: 1, stopMarks: []uint32{0xc5, 0xc5}}
	f.ioctl(t)
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	_, err = d.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, need)
	if err == nil || !errors.Is(err, syscall.EIO) {
		t.Fatalf("erreur du morceau attendue : %v", err)
	}
	opens := 0
	for _, s := range f.subs() {
		if s != 83 && s != 84 {
			opens++
		}
	}
	if got := f.subs(); opens != 1 || !slices.Equal(got, []uint32{234, 83, 83}) || d.CloseUnconfirmed() != 0 {
		t.Fatalf("séquence %v, %d rejet(s) : une ouverture, deux morceaux, aucune clôture attendus", got, d.CloseUnconfirmed())
	}
}

// La liste des conteneurs n'est envoyée qu'à la taille déduite du comptage
// validé pour ce périphérique (ExpectContainerList) : sans comptage, ou à une
// autre taille, même alignée et acceptée par la liste blanche, rien
// n'atteint le noyau. Le réglage ne vaut que pour ce périphérique.
func TestContainerListSizeBoundToCount(t *testing.T) {
	list5 := make([]byte, 28)
	binary.LittleEndian.PutUint32(list5[4:], 5)
	f := &fakeFirmware{payload: list5, xfer: 3, failAt: -1}
	calls := 0
	f.ioctl(t)
	orig := ioctlFn
	ioctlFn = func(fd int, req uint32, buf []byte) error { calls++; return orig(fd, req, buf) }
	t.Cleanup(func() { ioctlFn = orig })
	open := func() *Device {
		d, err := Open("/dev/null", devNull, list(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		return d
	}
	need := aacraid.ContainerListNeed
	d := open()
	if _, err := d.ReadPaged(context.Background(), aacraid.ReadContainerList, 0, aacraid.ContainerListSize(256), need); err == nil || calls != 0 {
		t.Fatalf("liste envoyée sans comptage : %v, %d ioctl", err, calls)
	}
	d.ExpectContainerList(mustCount(t, 256))
	if _, err := d.ReadPaged(context.Background(), aacraid.ReadContainerList, 0, aacraid.ContainerListSize(255), need); err == nil || calls != 0 {
		t.Fatalf("liste de %d octets envoyée pour MaxContainers 256 : %v, %d ioctl", aacraid.ContainerListSize(255), err, calls)
	}
	if _, err := d.ReadPaged(context.Background(), aacraid.ReadContainerList, 0, 0x408, need); err != nil || calls != 3 {
		t.Fatalf("liste de 0x408 octets refusée : %v, %d ioctl", err, calls)
	}
	if got := binary.LittleEndian.Uint32(f.requests[0][aacraid.FIBHeaderSize+28:]); got != 0x408 {
		t.Fatalf("taille demandée %#x, 0x408 attendu", got)
	}
	// Un autre périphérique (autre carte, autre cycle) ne reprend pas ce
	// réglage.
	if _, err := open().ReadPaged(context.Background(), aacraid.ReadContainerList, 0, 0x408, need); err == nil || calls != 3 {
		t.Fatalf("réglage partagé entre périphériques : %v, %d ioctl", err, calls)
	}
}

// Le contrôle de la taille de la liste des conteneurs vaut aussi pour l'API
// brute Do : une ouverture de 12 octets, acceptée par la liste blanche,
// n'atteint pas le noyau sans le comptage correspondant.
func TestContainerListSizeCheckedOnRawPath(t *testing.T) {
	calls := fakeIoctl(t, func(req uint32, buf []byte) error { return nil })
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	open, err := aacraid.OpenRequest(aacraid.ReadContainerList, 0, aacraid.ContainerListSize(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Do(allowlist.Request{Ioctl: aacraid.IoctlSendFIB, Buf: open}); !errors.Is(err, allowlist.ErrDenied) || *calls != 0 {
		t.Fatalf("ouverture de 12 octets sans comptage : %v, %d ioctl", err, *calls)
	}
	d.ExpectContainerList(mustCount(t, 256))
	if err := d.Do(allowlist.Request{Ioctl: aacraid.IoctlSendFIB, Buf: open}); !errors.Is(err, allowlist.ErrDenied) || *calls != 0 {
		t.Fatalf("ouverture de 12 octets pour MaxContainers 256 : %v, %d ioctl", err, *calls)
	}
	d.ExpectContainerList(mustCount(t, 1))
	if err := d.Do(allowlist.Request{Ioctl: aacraid.IoctlSendFIB, Buf: open}); err != nil || *calls != 1 {
		t.Fatalf("ouverture de 12 octets pour MaxContainers 1 : %v, %d ioctl", err, *calls)
	}
}

// mustCount décode une réponse CT_GET_CONTAINER_COUNT construite pour n
// conteneurs : seule voie pour obtenir un comptage utilisable.
func mustCount(t *testing.T, n uint32) aacraid.ContainerCount {
	t.Helper()
	b := make([]byte, aacraid.FIBSize)
	le := binary.LittleEndian
	le.PutUint16(b[4:], 500)
	le.PutUint16(b[8:], aacraid.ChunkRspSize)
	le.PutUint32(b[aacraid.FIBHeaderSize+4:], 4)
	le.PutUint32(b[aacraid.FIBHeaderSize+8:], n)
	c, err := aacraid.DecodeContainerCount(b)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Sans comptage décodé (valeur nulle), aucune liste des conteneurs ne part,
// quelle que soit sa taille.
func TestContainerListNeedsDecodedCount(t *testing.T) {
	calls := fakeIoctl(t, func(req uint32, buf []byte) error { return nil })
	d, err := Open("/dev/null", devNull, list(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.ExpectContainerList(aacraid.ContainerCount{})
	for _, n := range []uint32{1, 256, aacraid.MaxContainersLimit} {
		open, err := aacraid.OpenRequest(aacraid.ReadContainerList, 0, aacraid.ContainerListSize(n))
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Do(allowlist.Request{Ioctl: aacraid.IoctlSendFIB, Buf: open}); !errors.Is(err, allowlist.ErrDenied) || *calls != 0 {
			t.Fatalf("liste de %d octets sans comptage : %v, %d ioctl", aacraid.ContainerListSize(n), err, *calls)
		}
	}
}

// fakeStopErr fait rendre EIO à la clôture du faux firmware f, après son
// rappel onStop.
func fakeStopErr(t *testing.T, f *fakeFirmware) {
	t.Helper()
	inner := ioctlFn
	ioctlFn = func(fd int, req uint32, buf []byte) error {
		err := inner(fd, req, buf)
		if binary.LittleEndian.Uint32(buf[aacraid.FIBHeaderSize+4:]) == 84 {
			return syscall.EIO
		}
		return err
	}
	t.Cleanup(func() { ioctlFn = inner })
}

// Fenêtre d'arrêt : rappel d'annulation retardé (instant enregistré après
// l'échéance) ; la fenêtre compte depuis l'échéance, pas depuis le rappel.
// Arrêt par signal : depuis son premier constat. Contexte actif : ouverte.
func TestStopWindowCountsFromStop(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-30*time.Millisecond))
	defer cancel()
	w := &stopWindow{stop: ctx, grace: 20 * time.Millisecond}
	w.mark() // rappel tardif : 30 ms après l'échéance
	if err := w.Err(); !errors.Is(err, errGraceExceeded) {
		t.Fatalf("échéance passée de 30 ms, fenêtre de 20 ms : %v", err)
	}
	live, stop := context.WithCancel(context.Background())
	w = &stopWindow{stop: live, grace: 20 * time.Millisecond}
	if err := w.Err(); err != nil {
		t.Fatalf("contexte actif : %v", err)
	}
	stop()
	if err := w.Err(); err != nil {
		t.Fatalf("arrêt à l'instant : %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if err := w.Err(); !errors.Is(err, errGraceExceeded) {
		t.Fatalf("arrêt depuis 30 ms : %v", err)
	}
}

// Clôture d'une ouverture non nominale (code 0xdb) : hors couple validé,
// soumise à la fenêtre d'arrêt (ouverture qui rend la main après la
// fenêtre : clôture refusée à l'admission, non envoyée) ; sur le couple
// validé, engagée (envoyée malgré l'arrêt) ; réponse de clôture d'un autre
// transfert : erreur de clôture rendue ; confirmation 0xc5 : ni comptée
// (la lecture n'est pas rejetée par sa clôture, elle est déjà en échec) ni
// suivie d'une relecture, même sur le couple validé.
func TestReadPagedStopAfterOpenWindowAndAnswers(t *testing.T) {
	read := func(t *testing.T, f *fakeFirmware, validated bool, ctx context.Context) (*Device, error) {
		t.Helper()
		f.ioctl(t)
		d, err := Open("/dev/null", devNull, list(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		d.SetValidatedCouple(validated)
		data, err := d.ReadPaged(ctx, aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return aacraid.ContainerInfoNeed })
		if data != nil || err == nil {
			t.Fatalf("données rendues ou lecture réussie : %v", err)
		}
		return d, err
	}
	for _, validated := range []bool{false, true} {
		t.Run(fmt.Sprintf("arrêt pendant l'ouverture, validé %v", validated), func(t *testing.T) {
			old := stopGrace
			stopGrace = 20 * time.Millisecond
			defer func() { stopGrace = old }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1, openCode: 0xdb,
				onOpen: func() { cancel(); time.Sleep(60 * time.Millisecond) }}
			_, err := read(t, f, validated, ctx)
			want := []uint32{234}
			if validated {
				// Clôture engagée et confirmée : fin établie.
				want = []uint32{234, 84}
				if errors.Is(err, aacraid.ErrTransferUnsettled) {
					t.Fatalf("err=%v : fin établie marquée non établie", err)
				}
			} else if !errors.Is(err, errGraceExceeded) || !errors.Is(err, aacraid.ErrTransferUnsettled) {
				t.Fatalf("err=%v, attendu errGraceExceeded et fin non établie", err)
			}
			if got := f.subs(); !slices.Equal(got, want) {
				t.Fatalf("séquence %v, attendu %v", got, want)
			}
		})
	}
	t.Run("clôture d'un autre transfert", func(t *testing.T) {
		f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1, openCode: 0xdb, stopXfer: 10}
		if _, err := read(t, f, false, context.Background()); !strings.Contains(err.Error(), "stop not confirmed") || !errors.Is(err, aacraid.ErrTransferUnsettled) {
			t.Fatalf("erreur de clôture non rendue : %v", err)
		}
	})
	for _, validated := range []bool{false, true} {
		t.Run(fmt.Sprintf("clôture 0xc5, validé %v", validated), func(t *testing.T) {
			f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1, openCode: 0xdb, stopMarks: []uint32{0xc5, 0xc6}}
			d, err := read(t, f, validated, context.Background())
			if d.CloseUnconfirmed() != 0 || errors.Is(err, aacraid.ErrTransferUnsettled) {
				t.Fatalf("%d rejet(s) compté(s), err=%v", d.CloseUnconfirmed(), err)
			}
			if got := f.subs(); !slices.Equal(got, []uint32{234, 84}) {
				t.Fatalf("séquence %v, attendu l'ouverture et la clôture seules", got)
			}
		})
	}
}

// inline prépare une ouverture du parcours 0xc8 : marque 0xc8, pas step au
// mot 6, premières données (step octets du contenu) dans la réponse.
func inline(f *fakeFirmware, step uint32) {
	f.openMark = 0xc8
	f.openHook = func(d []byte) {
		binary.LittleEndian.PutUint32(d[24:], step)
		copy(d[32:32+aacraid.ChunkData], f.payload[:min(int(step), len(f.payload))])
	}
}

// Parcours 0xc8 (ligne 7 du déroulement), comme l'outil de référence
// (variante V9 sur carte émulée ; 2.05 CT_SendReceiveFIB 0x8ddf56) : les
// données de l'ouverture sont reprises, puis les morceaux 1 à 9 d'un
// transfert de 4 096 octets en pas de 432, sans clôture, sur le couple
// validé comme ailleurs ; chaque demande porte la marque 0xc8, le total,
// CT_OK, le transfert, son numéro et la taille demandée | 0x80000000 (la
// taille de l'ouverture, bit 13 compris ou non). Données rendues : le
// contenu exact.
func TestReadPagedInlinePath(t *testing.T) {
	payload := make([]byte, 0x1000)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	for _, tc := range []struct {
		validated, highBit bool
	}{{false, false}, {true, false}, {false, true}} {
		t.Run(fmt.Sprintf("validé %v, bit 13 %v", tc.validated, tc.highBit), func(t *testing.T) {
			f := &fakeFirmware{payload: payload, xfer: 0x2061e, failAt: -1}
			inline(f, 432)
			f.ioctl(t)
			d, err := Open("/dev/null", devNull, list(t))
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			d.SetValidatedCouple(tc.validated)
			d.SetPagedHighBit(tc.highBit)
			data, err := d.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return aacraid.ContainerInfoNeed })
			if err != nil || !bytes.Equal(data, payload) {
				t.Fatalf("err=%v, %d octets, contenu identique %v", err, len(data), bytes.Equal(data, payload))
			}
			want := append([]uint32{234}, slices.Repeat([]uint32{83}, 9)...)
			if got := f.subs(); !slices.Equal(got, want) {
				t.Fatalf("séquence %v, attendu l'ouverture et les morceaux 1 à 9, sans clôture", got)
			}
			le := binary.LittleEndian
			if w7 := le.Uint32(f.requests[0][aacraid.FIBHeaderSize+28:]); w7 != 0x1000|map[bool]uint32{true: aacraid.SizeHighBit}[tc.highBit] {
				t.Fatalf("ouverture : mot 7 %#x", w7)
			}
			for i, r := range f.requests[1:] {
				want := make([]byte, aacraid.FIBSize)
				copy(want, f.requests[0][:aacraid.FIBHeaderSize])
				for k, v := range []uint32{2, 83, 0xc8, 0x1000, 0xda, 0x2061e, uint32(i + 1), 0x80001000} {
					le.PutUint32(want[aacraid.FIBHeaderSize+4*k:], v)
				}
				if !bytes.Equal(r, want) {
					t.Fatalf("morceau %d :\n got %x\nwant %x", i+1, r[:64], want[:64])
				}
			}
		})
	}
}

// Parcours 0xc8, cas limites : pas nul (l'outil lirait octet par octet) ou
// supérieur aux 432 octets écrits par la carte : aucune commande ; total
// plus court que le pas : données de l'ouverture seules, sans morceau ;
// réponse de morceau incohérente : abandon, ni morceau suivant ni clôture ;
// arrêt pendant le parcours : morceaux restants dans la fenêtre, lecture
// non rendue, sans clôture ; fenêtre dépassée : abandon.
func TestReadPagedInlineLimits(t *testing.T) {
	read := func(t *testing.T, f *fakeFirmware, ctx context.Context) ([]byte, error) {
		t.Helper()
		f.xfer, f.failAt = 9, -1
		f.ioctl(t)
		d, err := Open("/dev/null", devNull, list(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		return d.ReadPaged(ctx, aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return 0 })
	}
	for name, step := range map[string]uint32{"pas nul": 0, "pas de 433": 433} {
		f := &fakeFirmware{payload: make([]byte, 0x1000)}
		inline(f, step)
		if _, err := read(t, f, context.Background()); err == nil || !slices.Equal(f.subs(), []uint32{234}) {
			t.Fatalf("%s : err=%v, séquence %v", name, err, f.subs())
		}
	}
	short := &fakeFirmware{payload: []byte("donnees courtes")}
	inline(short, 432)
	if data, err := read(t, short, context.Background()); err != nil || string(data) != "donnees courtes" || !slices.Equal(short.subs(), []uint32{234}) {
		t.Fatalf("total court : %q, %v, séquence %v", data, err, short.subs())
	}
	for name, hook := range map[string]func(uint32, []byte){
		"transfert": func(n uint32, d []byte) {
			if n == 2 {
				binary.LittleEndian.PutUint32(d[20:], 10)
			}
		},
		"numéro": func(n uint32, d []byte) {
			if n == 2 {
				binary.LittleEndian.PutUint32(d[24:], 3)
			}
		},
		"code": func(n uint32, d []byte) {
			if n == 2 {
				binary.LittleEndian.PutUint32(d[16:], 0xdb)
			}
		},
		"total": func(n uint32, d []byte) {
			if n == 2 {
				binary.LittleEndian.PutUint32(d[12:], 0xfff)
			}
		},
		"marque": func(n uint32, d []byte) {
			if n == 2 {
				binary.LittleEndian.PutUint32(d[8:], 0xc9)
			}
		},
	} {
		f := &fakeFirmware{payload: make([]byte, 0x1000), chunkHook: hook}
		inline(f, 432)
		if _, err := read(t, f, context.Background()); err == nil || !slices.Equal(f.subs(), []uint32{234, 83, 83}) {
			t.Fatalf("%s incohérent : err=%v, séquence %v", name, err, f.subs())
		}
	}
	old := stopGrace
	stopGrace = 20 * time.Millisecond
	defer func() { stopGrace = old }()
	for name, slow := range map[string]time.Duration{"arrêt": 0, "fenêtre dépassée": 60 * time.Millisecond} {
		ctx, cancel := context.WithCancel(context.Background())
		chunks := 0
		f := &fakeFirmware{payload: make([]byte, 0x1000), onChunk: func() {
			if chunks++; chunks == 2 {
				cancel()
				time.Sleep(slow)
			}
		}}
		inline(f, 432)
		data, err := read(t, f, ctx)
		cancel()
		want := append([]uint32{234}, slices.Repeat([]uint32{83}, 9)...)
		if slow > 0 {
			want = []uint32{234, 83, 83}
		}
		if err == nil || data != nil || !slices.Equal(f.subs(), want) {
			t.Fatalf("%s : err=%v, données %v, séquence %v", name, err, data != nil, f.subs())
		}
	}
}

// Parcours 0xc8 : pas tiré aussi du SenderSize de la réponse, comme l'outil
// (SenderSize 256 : pas de 192, morceaux 1 à 21 pour 4 096 octets ; 64 :
// refusé sans commande) ; mot 7 des morceaux = taille demandée à
// l'ouverture (0x1000 pour la 234) même quand la carte annonce un autre
// total (0xfa0).
func TestReadPagedInlineSenderSizeAndRequested(t *testing.T) {
	read := func(t *testing.T, f *fakeFirmware) error {
		t.Helper()
		f.xfer, f.failAt = 9, -1
		f.ioctl(t)
		d, err := Open("/dev/null", devNull, list(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		_, err = d.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return 0 })
		return err
	}
	f := &fakeFirmware{payload: make([]byte, 0x1000), openSender: 256}
	inline(f, 432)
	if err := read(t, f); err != nil || !slices.Equal(f.subs(), append([]uint32{234}, slices.Repeat([]uint32{83}, 21)...)) {
		t.Fatalf("SenderSize 256 : err=%v, séquence %v", err, f.subs())
	}
	f = &fakeFirmware{payload: make([]byte, 0x1000), openSender: 64}
	inline(f, 432)
	if err := read(t, f); err == nil || !slices.Equal(f.subs(), []uint32{234}) {
		t.Fatalf("SenderSize 64 : err=%v, séquence %v", err, f.subs())
	}
	f = &fakeFirmware{payload: make([]byte, 0x1000), total: 0xfa0}
	inline(f, 432)
	if err := read(t, f); err != nil || len(f.requests) != 10 {
		t.Fatalf("total 0xfa0 : err=%v, %d requêtes", err, len(f.requests))
	}
	le := binary.LittleEndian
	for i, r := range f.requests[1:] {
		if total, w7 := le.Uint32(r[aacraid.FIBHeaderSize+12:]), le.Uint32(r[aacraid.FIBHeaderSize+28:]); total != 0xfa0 || w7 != 0x80001000 {
			t.Fatalf("morceau %d : total %#x, mot 7 %#x (attendu 0xfa0 et 0x80001000)", i+1, total, w7)
		}
	}
}

// Échec d'échange (aacraid.ErrExchange, sur lequel l'outil abandonne
// l'ouverture de la carte) : ioctl en échec à l'ouverture ou sur un
// morceau, statut non nul ou mauvais écho dans une réponse. Un refus n'en
// est pas un : code d'ouverture atypique, contrôle propre à l'exporter
// (marque d'un morceau), clôture d'un autre transfert.
func TestReadPagedExchangeFailures(t *testing.T) {
	le := binary.LittleEndian
	at := func(n uint32, off int, v uint32) func(uint32, []byte) {
		return func(m uint32, d []byte) {
			if m == n {
				le.PutUint32(d[off:], v)
			}
		}
	}
	for name, tc := range map[string]struct {
		f        *fakeFirmware
		exchange bool
	}{
		"ioctl de l'ouverture":         {&fakeFirmware{payload: make([]byte, 0x1000), failAt: -1, openErr: syscall.EIO}, true},
		"ioctl d'un morceau":           {&fakeFirmware{payload: make([]byte, 0x1000), failAt: 1}, true},
		"statut d'un morceau":          {&fakeFirmware{payload: make([]byte, 0x1000), failAt: -1, chunkHook: at(1, 0, 1)}, true},
		"écho d'un morceau":            {&fakeFirmware{payload: make([]byte, 0x1000), failAt: -1, chunkHook: at(1, 4, 82)}, true},
		"statut de l'ouverture":        {&fakeFirmware{payload: make([]byte, 0x1000), failAt: -1, openHook: func(d []byte) { le.PutUint32(d[0:], 1) }}, true},
		"écho de l'ouverture":          {&fakeFirmware{payload: make([]byte, 0x1000), failAt: -1, openHook: func(d []byte) { le.PutUint32(d[4:], 0xeb) }}, true},
		"statut de la clôture":         {&fakeFirmware{payload: make([]byte, 0x1000), failAt: -1, stopHook: func(d []byte) { le.PutUint32(d[0:], 1) }}, true},
		"écho de la clôture":           {&fakeFirmware{payload: make([]byte, 0x1000), failAt: -1, stopHook: func(d []byte) { le.PutUint32(d[4:], 83) }}, true},
		"marque d'un morceau":          {&fakeFirmware{payload: make([]byte, 0x1000), failAt: -1, chunkHook: at(1, 8, 0xc9)}, false},
		"code d'ouverture":             {&fakeFirmware{payload: make([]byte, 0x1000), failAt: -1, openCode: 0xdb}, false},
		"clôture d'un autre transfert": {&fakeFirmware{payload: make([]byte, 0x1000), failAt: -1, stopXfer: 10}, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := tc.f
			f.xfer = 9
			f.ioctl(t)
			d, err := Open("/dev/null", devNull, list(t))
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			_, err = d.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return 0x1000 })
			if err == nil || errors.Is(err, aacraid.ErrExchange) != tc.exchange {
				t.Fatalf("err=%v, échec d'échange attendu %v", err, tc.exchange)
			}
		})
	}
}

// Rejet propre à l'exporter sur le morceau 0, puis clôture : son échec
// d'échange (statut non nul) est un échec d'échange de la lecture, qui
// arrête la carte pour le cycle comme l'exception de l'outil ; un arrêt
// demandé pendant le morceau rejeté laisse partir la clôture sur le couple
// validé, et ailleurs tant que la fenêtre d'arrêt est ouverte, sinon
// abandon sans clôture.
func TestReadPagedRejectThenStop(t *testing.T) {
	need := func([]byte) int { return aacraid.ContainerInfoNeed }
	t.Run("échec d'échange de la clôture", func(t *testing.T) {
		f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1, chunkMark: 0xc9,
			stopHook: func(d []byte) { binary.LittleEndian.PutUint32(d[0:], 1) }}
		f.ioctl(t)
		d, err := Open("/dev/null", devNull, list(t))
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		_, err = d.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, need)
		if !errors.Is(err, aacraid.ErrExchange) || !errors.Is(err, errRejected) {
			t.Fatalf("err=%v, attendu rejet puis échec d'échange de la clôture", err)
		}
		if got := f.subs(); !slices.Equal(got, []uint32{234, 83, 84}) {
			t.Fatalf("séquence %v", got)
		}
	})
	for name, tc := range map[string]struct {
		validated bool
		slow      time.Duration
		want      []uint32
	}{
		"couple validé":                {true, 60 * time.Millisecond, []uint32{234, 83, 84}},
		"hors couple, fenêtre ouverte": {false, 0, []uint32{234, 83, 84}},
		"hors couple, fenêtre close":   {false, 60 * time.Millisecond, []uint32{234, 83}},
	} {
		t.Run("arrêt pendant le morceau rejeté, "+name, func(t *testing.T) {
			old := stopGrace
			stopGrace = 20 * time.Millisecond
			defer func() { stopGrace = old }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1, chunkMark: 0xc9,
				onChunk: func() { cancel(); time.Sleep(tc.slow) }}
			f.ioctl(t)
			d, err := Open("/dev/null", devNull, list(t))
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			d.SetValidatedCouple(tc.validated)
			data, err := d.ReadPaged(ctx, aacraid.ReadContainerInfo, 0, 0, need)
			if err == nil || data != nil || !errors.Is(err, errRejected) {
				t.Fatalf("err=%v, données=%v", err, data != nil)
			}
			if got := f.subs(); !slices.Equal(got, tc.want) {
				t.Fatalf("séquence %v, attendu %v", got, tc.want)
			}
			if closed := len(tc.want) == 3; closed == errors.Is(err, errGraceExceeded) {
				t.Fatalf("clôture %v, err=%v", closed, err)
			}
		})
	}
}

// Fin de transfert : aacraid.ErrTransferUnsettled quand rien n'établit que
// le transfert est clos (ouverture refusée avec un identifiant reçu,
// parcours 0xc8 interrompu, clôture non confirmée) ; jamais pour un
// transfert mené à terme, une clôture 0xc6 ou 0xc5 du bon transfert, une
// ouverture sans identifiant, ni un échec d'échange (qui a son propre
// motif). Dernière requête vérifiée : aucune commande après la dernière
// attendue.
func TestReadPagedTransferEnd(t *testing.T) {
	le := binary.LittleEndian
	for name, tc := range map[string]struct {
		f         func() *fakeFirmware
		need      int
		subs      []uint32
		unsettled bool
		ok        bool
		exchange  bool
	}{
		"nominal":           {func() *fakeFirmware { return &fakeFirmware{} }, 0x1000, []uint32{234, 83, 83, 83, 83, 83, 83, 83, 83, 83, 83, 84}, false, true, false},
		"total hors bornes": {func() *fakeFirmware { return &fakeFirmware{total: 0x2000} }, 0x1000, []uint32{234}, true, false, false},
		"mot 7 différent du total": {func() *fakeFirmware {
			return &fakeFirmware{openHook: func(d []byte) { le.PutUint32(d[28:], 0x800) }}
		}, 0x1000, []uint32{234}, true, false, false},
		"sans identifiant":              {func() *fakeFirmware { return &fakeFirmware{xfer: aacraid.XferNone} }, 0x1000, []uint32{234}, false, false, false},
		"en-tête d'ouverture (Size)":    {func() *fakeFirmware { return &fakeFirmware{openSize: 100} }, 0x1000, []uint32{234}, true, false, false},
		"clôture, marque inconnue":      {func() *fakeFirmware { return &fakeFirmware{stopMark: 0xc7} }, 0x1000, []uint32{234, 83, 83, 83, 83, 83, 83, 83, 83, 83, 83, 84}, true, false, false},
		"clôture, autre transfert":      {func() *fakeFirmware { return &fakeFirmware{stopXfer: 10} }, 0x1000, []uint32{234, 83, 83, 83, 83, 83, 83, 83, 83, 83, 83, 84}, true, false, false},
		"clôture, Size invalide":        {func() *fakeFirmware { return &fakeFirmware{stopSize: 100} }, 0x1000, []uint32{234, 83, 83, 83, 83, 83, 83, 83, 83, 83, 83, 84}, true, false, false},
		"clôture 0xc5":                  {func() *fakeFirmware { return &fakeFirmware{stopMark: aacraid.MarkClosedOther} }, 0x1000, []uint32{234, 83, 83, 83, 83, 83, 83, 83, 83, 83, 83, 84}, false, false, false},
		"0xe2, clôture marque inconnue": {func() *fakeFirmware { return &fakeFirmware{openCode: 0xe2, stopMark: 0xc7} }, 0x1000, []uint32{234, 84}, true, false, false},
		"rejet puis clôture 0xc6":       {func() *fakeFirmware { return &fakeFirmware{chunkMark: 0xc9} }, 0x1000, []uint32{234, 83, 84}, false, false, false},
		"échec d'échange":               {func() *fakeFirmware { return &fakeFirmware{failAt: 1} }, 0x1000, []uint32{234, 83, 83}, false, false, true},
		// Réponse d'ouverture à l'en-tête refusé : son mot 5 n'est pas fiable,
		// une sentinelle lue ne prouve pas l'absence de transfert.
		"sentinelle et en-tête refusé":              {func() *fakeFirmware { return &fakeFirmware{xfer: aacraid.XferNone, openSize: 100} }, 0x1000, []uint32{234}, true, false, false},
		"0xc8, en-tête de morceau refusé":           {func() *fakeFirmware { f := &fakeFirmware{chunkSize: 100}; inline(f, 432); return f }, 0, []uint32{234, 83}, true, false, false},
		"rejet d'un morceau, clôture non confirmée": {func() *fakeFirmware { return &fakeFirmware{chunkMark: 0xc9, stopMark: 0xc7} }, 0x1000, []uint32{234, 83, 84}, true, false, false},
		"données rejetées, clôture non confirmée":   {func() *fakeFirmware { return &fakeFirmware{stopMark: 0xc7} }, 0x2000, []uint32{234, 83, 84}, true, false, false},
		"0xc8 complet": {func() *fakeFirmware { f := &fakeFirmware{}; inline(f, 432); return f }, 0, []uint32{234, 83, 83, 83, 83, 83, 83, 83, 83, 83}, false, true, false},
		"0xc8 pas nul": {func() *fakeFirmware { f := &fakeFirmware{}; inline(f, 0); return f }, 0, []uint32{234}, true, false, false},
		"0xc8 morceau incohérent": {func() *fakeFirmware {
			f := &fakeFirmware{chunkHook: func(n uint32, d []byte) {
				if n == 2 {
					le.PutUint32(d[20:], 10)
				}
			}}
			inline(f, 432)
			return f
		}, 0, []uint32{234, 83, 83}, true, false, false},
		"marque hors parcours avec identifiant": {func() *fakeFirmware { return &fakeFirmware{openMark: 0xc7} }, 0x1000, []uint32{234}, true, false, false},
		"0xc8, dernier morceau incohérent": {func() *fakeFirmware {
			f := &fakeFirmware{chunkHook: func(n uint32, d []byte) {
				if n == 9 {
					le.PutUint32(d[20:], 10)
				}
			}}
			inline(f, 432)
			return f
		}, 0, []uint32{234, 83, 83, 83, 83, 83, 83, 83, 83, 83}, true, false, false},
		"0xe2, clôture confirmée":        {func() *fakeFirmware { return &fakeFirmware{openCode: 0xe2} }, 0x1000, []uint32{234, 84}, false, false, false},
		"0xc8 complet, données rejetées": {func() *fakeFirmware { f := &fakeFirmware{}; inline(f, 432); return f }, 0x2000, []uint32{234, 83, 83, 83, 83, 83, 83, 83, 83, 83}, false, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := tc.f()
			f.payload = make([]byte, 0x1000)
			if f.xfer == 0 {
				f.xfer = 9
			}
			if f.failAt == 0 {
				f.failAt = -1
			}
			f.ioctl(t)
			d, err := Open("/dev/null", devNull, list(t))
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			_, err = d.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return tc.need })
			if (err == nil) != tc.ok || errors.Is(err, aacraid.ErrTransferUnsettled) != tc.unsettled || errors.Is(err, aacraid.ErrExchange) != tc.exchange {
				t.Fatalf("err=%v, attendu ok=%v, fin non établie=%v, échec d'échange=%v", err, tc.ok, tc.unsettled, tc.exchange)
			}
			if got := f.subs(); !slices.Equal(got, tc.subs) {
				t.Fatalf("séquence %v, attendu %v", got, tc.subs)
			}
		})
	}
}

// Arrêt manuel pendant l'ouverture d'un parcours 0xc8 qui rend la main
// après la fenêtre d'arrêt : la fenêtre, installée avant l'ouverture, date
// l'arrêt de son premier constat (rappel d'annulation, ici pendant
// l'ouverture) et non de la fin de l'ouverture ; aucun morceau ne part, sur
// le couple validé comme ailleurs, et la fin du transfert n'est pas établie.
func TestReadPagedInlineCancelDuringOpen(t *testing.T) {
	for _, validated := range []bool{false, true} {
		t.Run(fmt.Sprintf("validé %v", validated), func(t *testing.T) {
			old := stopGrace
			stopGrace = 20 * time.Millisecond
			defer func() { stopGrace = old }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1}
			inline(f, 432)
			f.onOpen = func() { cancel(); time.Sleep(60 * time.Millisecond) }
			f.ioctl(t)
			d, err := Open("/dev/null", devNull, list(t))
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			d.SetValidatedCouple(validated)
			_, err = d.ReadPaged(ctx, aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return 0 })
			if !errors.Is(err, errGraceExceeded) || !errors.Is(err, aacraid.ErrTransferUnsettled) {
				t.Fatalf("err=%v", err)
			}
			if got := f.subs(); !slices.Equal(got, []uint32{234}) {
				t.Fatalf("séquence %v, attendu l'ouverture seule", got)
			}
		})
	}
}

// Commande refusée par la liste blanche dans un transfert ouvert (morceau ou
// clôture) : rien ne part, la fin du transfert n'est pas établie. Liste
// restreinte construite à partir de celle d'aacraid, sans la sous-commande
// visée.
func TestReadPagedDeniedInsideTransfer(t *testing.T) {
	for name, tc := range map[string]struct {
		sub  uint32
		subs []uint32
	}{
		"morceau refusé":  {83, []uint32{234}},
		"clôture refusée": {84, []uint32{234, 83, 83, 83, 83, 83, 83, 83, 83, 83, 83}},
	} {
		t.Run(name, func(t *testing.T) {
			var entries []allowlist.Entry
			for _, e := range aacraid.Entries {
				if e.Ioctl == aacraid.IoctlSendFIB {
					inner := e.Payload
					e.Payload = func(b []byte) error {
						if binary.LittleEndian.Uint16(b[4:]) == 500 && binary.LittleEndian.Uint32(b[aacraid.FIBHeaderSize+4:]) == tc.sub {
							return errors.New("sous-commande retirée pour le test")
						}
						return inner(b)
					}
				}
				entries = append(entries, e)
			}
			l, err := allowlist.New(entries...)
			if err != nil {
				t.Fatal(err)
			}
			f := &fakeFirmware{payload: make([]byte, 0x1000), xfer: 9, failAt: -1}
			f.ioctl(t)
			d, err := Open("/dev/null", devNull, l)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			_, err = d.ReadPaged(context.Background(), aacraid.ReadContainerInfo, 0, 0, func([]byte) int { return 0x1000 })
			if !errors.Is(err, allowlist.ErrDenied) || !errors.Is(err, aacraid.ErrTransferUnsettled) || errors.Is(err, aacraid.ErrExchange) {
				t.Fatalf("err=%v", err)
			}
			if got := f.subs(); !slices.Equal(got, tc.subs) {
				t.Fatalf("séquence %v, attendu %v", got, tc.subs)
			}
		})
	}
}
