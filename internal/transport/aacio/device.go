// Package aacio est la couche de transport unique du module aacraid
// (exigence S1) : ouverture du nœud dédié, vérification du périphérique
// ouvert, puis émission des seules requêtes de la liste blanche (S2).
package aacio

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/DisasteR/hwraid_exporter/internal/aacraid"
	"github.com/DisasteR/hwraid_exporter/internal/allowlist"
)

// ErrNodeMismatch : le nœud ouvert n'est pas le périphérique attendu.
var ErrNodeMismatch = errors.New("le nœud ne correspond pas à la carte attendue")

// ioctlFn est l'appel système réel ; les tests le remplacent. Il reçoit le
// tampon entier, pour que la taille effectivement passée soit observable.
var ioctlFn = func(fd int, req uint32, buf []byte) error {
	_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(req), uintptr(unsafe.Pointer(&buf[0])))
	runtime.KeepAlive(buf)
	if e != 0 {
		return e
	}
	return nil
}

// ErrClosed : le nœud a déjà été refermé.
var ErrClosed = errors.New("nœud de gestion refermé")

// Device est un nœud de gestion ouvert pour un cycle de collecte.
// Il doit être refermé à la fin du cycle (pas de descripteur persistant).
type Device struct {
	fd   int
	path string
	list *allowlist.List
}

// Open ouvre path en lecture seule, sans suivre de lien, et vérifie sur le
// descripteur ouvert qu'il s'agit du nœud caractère wantDev.
func Open(path string, wantDev uint64, list *allowlist.List) (*Device, error) {
	if list == nil {
		return nil, errors.New("liste blanche absente")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("ouverture de %s : %w", path, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("fstat de %s : %w", path, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFCHR || st.Rdev != wantDev {
		unix.Close(fd)
		return nil, fmt.Errorf("%w : %s est %d:%d, attendu %d:%d", ErrNodeMismatch, path,
			unix.Major(st.Rdev), unix.Minor(st.Rdev), unix.Major(wantDev), unix.Minor(wantDev))
	}
	return &Device{fd: fd, path: path, list: list}, nil
}

// Close referme le nœud. Un second appel est sans effet : le descripteur,
// une fois libéré, peut être réattribué à un autre fichier.
func (d *Device) Close() error {
	if d.fd < 0 {
		return nil
	}
	err := unix.Close(d.fd)
	d.fd = -1
	return err
}

// Do émet req si la liste blanche l'autorise. Une interruption (EINTR) est
// un échec de la lecture en cours, sans nouvel essai (exigence S8).
//
// La vérification et l'appel portent sur une copie privée du tampon : un
// appelant ne peut pas modifier la requête entre la vérification de la
// liste blanche et l'appel système. La réponse est recopiée ensuite.
func (d *Device) Do(req allowlist.Request) error {
	if d.fd < 0 {
		return ErrClosed
	}
	priv := allowlist.Request{Ioctl: req.Ioctl, Buf: append([]byte(nil), req.Buf...)}
	if _, err := d.list.Check(priv); err != nil {
		return err
	}
	if err := ioctlFn(d.fd, priv.Ioctl, priv.Buf); err != nil {
		return fmt.Errorf("ioctl %#x sur %s : %w", req.Ioctl, d.path, err)
	}
	copy(req.Buf, priv.Buf)
	return nil
}

// HBAInfo lit FSACTL_GET_HBA_INFO (réponse du pilote, sans commande firmware).
func (d *Device) HBAInfo() (aacraid.HBAInfo, error) {
	buf := make([]byte, aacraid.HBAInfoSize)
	if err := d.Do(allowlist.Request{Ioctl: aacraid.IoctlGetHBAInfo, Buf: buf}); err != nil {
		return aacraid.HBAInfo{}, err
	}
	return aacraid.DecodeHBAInfo(buf)
}

// SupplementInfo envoie RequestSupplementAdapterInfo (706) au firmware et
// décode la réponse.
func (d *Device) SupplementInfo() (aacraid.SupplementInfo, error) {
	buf := aacraid.SupplementInfoRequest()
	if err := d.Do(allowlist.Request{Ioctl: aacraid.IoctlSendFIB, Buf: buf}); err != nil {
		return aacraid.SupplementInfo{}, err
	}
	return aacraid.DecodeSupplementInfo(buf)
}

// fib envoie une requête FIB qualifiée et renvoie la réponse.
func (d *Device) fib(req []byte) ([]byte, error) {
	if err := d.Do(allowlist.Request{Ioctl: aacraid.IoctlSendFIB, Buf: req}); err != nil {
		return nil, err
	}
	return req, nil
}

// CacheProtection lit l'état du module de protection du cache : lecture de
// présence, puis lecture d'état si la carte y décrit son module. present
// vaut false si la carte ne décrit pas de module dans ce format.
func (d *Device) CacheProtection() (present bool, st aacraid.ZMMStatus, err error) {
	rsp, err := d.fib(aacraid.CacheProtectionPresenceRequest())
	if err != nil {
		return false, st, err
	}
	if present, err = aacraid.DecodeCacheProtectionPresence(rsp); err != nil || !present {
		return false, st, err
	}
	if rsp, err = d.fib(aacraid.CacheProtectionStateRequest()); err != nil {
		return true, st, err
	}
	st, err = aacraid.DecodeCacheProtectionState(rsp)
	return true, st, err
}

// maxPagedTotal borne la taille d'un transfert accepté (plus grande taille
// qualifiée : liste des disques, 4 100 octets).
const maxPagedTotal = 0x1004

// ReadPaged effectue une lecture paginée qualifiée et renvoie au moins
// need(données lues) octets, ou tout le transfert. Le transfert est clôturé
// dès que les données suffisent, et dans tous les cas après son ouverture,
// même en cas d'erreur : le firmware ne garde pas de transfert ouvert.
func (d *Device) ReadPaged(r aacraid.PagedRead, param, size uint32, need func([]byte) int) ([]byte, error) {
	open, err := aacraid.OpenRequest(r, param, size)
	if err != nil {
		return nil, err
	}
	rsp, err := d.fib(append([]byte(nil), open...))
	if err != nil {
		return nil, err
	}
	head, err := aacraid.DecodePagedResponse(open, rsp)
	if err != nil {
		return nil, err
	}
	if head.Mark != 0xc9 || head.Total == 0 || head.Total > maxPagedTotal {
		return nil, fmt.Errorf("lecture %d : ouverture refusée (marque %#x, taille %d)", r, head.Mark, head.Total)
	}
	var data []byte
	last := uint32(0)
	readErr := func() error {
		for n := uint32(0); n < (head.Total+aacraid.ChunkData-1)/aacraid.ChunkData; n++ {
			req := aacraid.ChunkRequest(head.Total, head.Xfer, n)
			rsp, err := d.fib(append([]byte(nil), req...))
			if err != nil {
				return err
			}
			c, err := aacraid.DecodePagedResponse(req, rsp)
			if err != nil {
				return err
			}
			if c.Xfer != head.Xfer || c.Chunk != n || c.Total != head.Total {
				return fmt.Errorf("lecture %d : morceau %d incohérent (transfert %#x, morceau %d)", r, n, c.Xfer, c.Chunk)
			}
			last = n
			data = append(data, c.Data...)
			if len(data) >= int(head.Total) {
				data = data[:head.Total]
				return nil
			}
			if want := need(data); want <= len(data) {
				return nil
			} else if want > int(head.Total) {
				return fmt.Errorf("lecture %d : %d octets demandés pour un transfert de %d", r, want, head.Total)
			}
		}
		return nil
	}()
	stop := aacraid.StopRequest(head.Total, head.Xfer, last)
	stopRsp, stopErr := d.fib(append([]byte(nil), stop...))
	if stopErr == nil {
		var s aacraid.PagedResponse
		if s, stopErr = aacraid.DecodePagedResponse(stop, stopRsp); stopErr == nil && s.Mark != 0xc6 {
			stopErr = fmt.Errorf("lecture %d : clôture non confirmée (marque %#x)", r, s.Mark)
		}
	}
	if readErr != nil {
		return nil, readErr
	}
	if stopErr != nil {
		return nil, stopErr
	}
	return data, nil
}
