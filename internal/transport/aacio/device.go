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

// ioctlFn est l'appel système réel ; les tests le remplacent.
var ioctlFn = func(fd int, req uint32, arg unsafe.Pointer) error {
	_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(req), uintptr(arg))
	if e != 0 {
		return e
	}
	return nil
}

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

// Close referme le nœud.
func (d *Device) Close() error { return unix.Close(d.fd) }

// Do émet req si la liste blanche l'autorise. Une interruption (EINTR) est
// un échec de la lecture en cours, sans nouvel essai (exigence S8).
func (d *Device) Do(req allowlist.Request) error {
	if _, err := d.list.Check(req); err != nil {
		return err
	}
	err := ioctlFn(d.fd, req.Ioctl, unsafe.Pointer(&req.Buf[0]))
	runtime.KeepAlive(req.Buf)
	if err != nil {
		return fmt.Errorf("ioctl %#x sur %s : %w", req.Ioctl, d.path, err)
	}
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
