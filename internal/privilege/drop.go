// Package privilege abandonne de façon irréversible les privilèges du
// processus après la phase de préparation (exigence S6 du cahier des charges).
//
// Chaque modification s'applique à tous les threads du processus : les
// identités par les fonctions de syscall (qui le font depuis Go 1.16), les
// capacités et attributs prctl par syscall.AllThreadsSyscall, disponible
// parce que le binaire est compilé sans cgo.
package privilege

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/DisasteR/hwraid_exporter/internal/procstatus"
)

// ErrNotRoot : la phase de préparation exige de démarrer en root.
var ErrNotRoot = errors.New("the process must start as root for the preparation phase")

// ErrInvalidIdentity : l'identité cible est root ou la valeur spéciale -1.
var ErrInvalidIdentity = errors.New("invalid target identity: must be neither 0 nor 4294967295")

// Drop passe tous les threads à l'identité uid/gid, vide les groupes
// supplémentaires, ne conserve que les capacités de keep dans les ensembles
// effectif, permis et limite, vide les ensembles hérité et ambiant, puis
// active no_new_privs. L'opération est irréversible. En cas d'échec à mi-
// parcours, le processus doit s'arrêter : son état n'est plus garanti.
func Drop(uid, gid uint32, keep uint64) error {
	if uid == 0 || gid == 0 || uid == ^uint32(0) || gid == ^uint32(0) {
		return ErrInvalidIdentity
	}
	if os.Geteuid() != 0 {
		return ErrNotRoot
	}
	last, err := lastCap()
	if err != nil {
		return err
	}
	if keep>>uint(last+1) != 0 {
		return fmt.Errorf("kept mask %#x outside the known capabilities (last: %d)", keep, last)
	}
	// 1. Conserver l'ensemble permis lors du changement d'UID.
	if err := prctlAll(unix.PR_SET_KEEPCAPS, 1); err != nil {
		return fmt.Errorf("PR_SET_KEEPCAPS: %w", err)
	}
	// 2. Réduire l'ensemble limite tant que CAP_SETPCAP est encore effective.
	for c := 0; c <= last; c++ {
		if keep&(1<<uint(c)) != 0 {
			continue
		}
		if err := prctlAll(unix.PR_CAPBSET_DROP, uintptr(c)); err != nil {
			return fmt.Errorf("PR_CAPBSET_DROP(%d): %w", c, err)
		}
	}
	// 3. Vider l'ensemble ambiant.
	if err := prctlAll(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL); err != nil {
		return fmt.Errorf("PR_CAP_AMBIENT_CLEAR_ALL: %w", err)
	}
	// 4. Changer d'identité (tous les threads).
	if err := syscall.Setgroups(nil); err != nil {
		return fmt.Errorf("setgroups: %w", err)
	}
	if err := syscall.Setresgid(int(gid), int(gid), int(gid)); err != nil {
		return fmt.Errorf("setresgid: %w", err)
	}
	if err := syscall.Setresuid(int(uid), int(uid), int(uid)); err != nil {
		return fmt.Errorf("setresuid: %w", err)
	}
	// 5. Fixer les ensembles effectif, permis et hérité.
	if err := capsetAll(keep); err != nil {
		return fmt.Errorf("capset: %w", err)
	}
	// 6. Plus aucune acquisition de privilège possible, et keepcaps retiré.
	if err := prctlAll(unix.PR_SET_NO_NEW_PRIVS, 1); err != nil {
		return fmt.Errorf("PR_SET_NO_NEW_PRIVS: %w", err)
	}
	if err := prctlAll(unix.PR_SET_KEEPCAPS, 0); err != nil {
		return fmt.Errorf("PR_SET_KEEPCAPS(0): %w", err)
	}
	return nil
}

// VerifyAllThreads relit l'état de chaque thread dans /proc/self/task et
// renvoie une erreur au premier écart avec l'état attendu.
func VerifyAllThreads(uid, gid uint32, keep uint64) error {
	want := procstatus.AfterDrop(uid, gid, keep)
	tasks, err := filepath.Glob("/proc/self/task/*/status")
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return errors.New("no readable thread in /proc/self/task")
	}
	for _, p := range tasks {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		s, err := procstatus.Parse(f)
		f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if err := s.Verify(want); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

func prctlAll(option, arg uintptr) error {
	_, _, e := syscall.AllThreadsSyscall6(unix.SYS_PRCTL, option, arg, 0, 0, 0, 0)
	if e != 0 {
		return e
	}
	return nil
}

// capData convertit un masque 64 bits au format capset version 3.
func capData(keep uint64) [2]unix.CapUserData {
	lo, hi := uint32(keep), uint32(keep>>32)
	return [2]unix.CapUserData{
		{Effective: lo, Permitted: lo, Inheritable: 0},
		{Effective: hi, Permitted: hi, Inheritable: 0},
	}
}

func capsetAll(keep uint64) error {
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := capData(keep)
	_, _, e := syscall.AllThreadsSyscall(unix.SYS_CAPSET,
		uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data[0])), 0)
	runtime.KeepAlive(&hdr)
	runtime.KeepAlive(&data)
	if e != 0 {
		return e
	}
	return nil
}

func lastCap() (int, error) {
	b, err := os.ReadFile("/proc/sys/kernel/cap_last_cap")
	if err != nil {
		return 0, fmt.Errorf("reading cap_last_cap: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 0 || n > 63 {
		return 0, fmt.Errorf("invalid cap_last_cap: %q", b)
	}
	return n, nil
}
