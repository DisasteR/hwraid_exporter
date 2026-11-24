// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

// Package aacio est la couche de transport unique du module aacraid
// (exigence S1) : ouverture du nœud dédié, vérification du périphérique
// ouvert, puis émission des seules requêtes de la liste blanche (S2).
package aacio

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/DisasteR/hwraid_exporter/internal/aacraid"
	"github.com/DisasteR/hwraid_exporter/internal/allowlist"
)

// ErrNodeMismatch : le nœud ouvert n'est pas le périphérique attendu.
var ErrNodeMismatch = errors.New("the node does not match the expected card")

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

// ErrDriverNoMemory : le pilote a refusé une commande de gestion faute de
// mémoire. ioctl_send_fib (commctrl.c) rend ENOMEM dans deux cas : sa
// réserve de blocs de commande de gestion est vide, ce qui ne se répare que
// par un redémarrage (voir thread.go), ou, pour une commande plus grande que
// le bloc, le tampon DMA n'a pas pu être alloué, selon la mémoire du noyau à
// cet instant, sans rien consommer de la réserve. Des
// ENOMEM répétés sur toutes les lectures désignent la réserve vide.
var ErrDriverNoMemory = errors.New("the driver refused a management command for lack of memory; two possible causes: its management command pool is exhausted (aacraid loses one block per interrupted command, only a reboot restores it), or a large command could not get a DMA buffer (depends on free kernel memory, does not consume the pool); the same error on every read points to the first, see docs/aacraid-defaut-pilote.md")

// ErrStopped : la collecte est arrêtée (échéance du cycle ou arrêt du
// service) ; aucune nouvelle commande n'est envoyée à la carte.
var ErrStopped = errors.New("collection stopped: no new card command sent")

// admit autorise une nouvelle commande firmware tant que ctx n'est pas
// terminé. La clôture d'un transfert déjà ouvert ne passe pas par admit :
// un transfert engagé est toujours clos.
func admit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w (%w)", ErrStopped, err)
	}
	return nil
}

// ErrClosed : le nœud a déjà été refermé.
var ErrClosed = errors.New("management node closed")

// Device est un nœud de gestion ouvert pour un cycle de collecte.
// Il doit être refermé à la fin du cycle (pas de descripteur persistant).
type Device struct {
	fd     int
	path   string
	list   *allowlist.List
	thread *quietThread // émet les ioctl, signaux bloqués
	// closeOther compte les lectures rejetées parce que leur clôture a été
	// confirmée par 0xc5.
	closeOther int
}

// CloseUnconfirmed renvoie le nombre de lectures paginées rejetées depuis
// l'ouverture du nœud parce que leur clôture a été confirmée par 0xc5 au
// lieu de 0xc6.
func (d *Device) CloseUnconfirmed() int { return d.closeOther }

// Open ouvre path en lecture seule, sans suivre de lien, et vérifie sur le
// descripteur ouvert qu'il s'agit du nœud caractère wantDev.
func Open(path string, wantDev uint64, list *allowlist.List) (*Device, error) {
	if list == nil {
		return nil, errors.New("missing allowlist")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("fstat %s: %w", path, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFCHR || st.Rdev != wantDev {
		unix.Close(fd)
		return nil, fmt.Errorf("%w: %s is %d:%d, expected %d:%d", ErrNodeMismatch, path,
			unix.Major(st.Rdev), unix.Minor(st.Rdev), unix.Major(wantDev), unix.Minor(wantDev))
	}
	thread, err := startQuietThread()
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	return &Device{fd: fd, path: path, list: list, thread: thread}, nil
}

// Close referme le nœud. Un second appel est sans effet : le descripteur,
// une fois libéré, peut être réattribué à un autre fichier.
func (d *Device) Close() error {
	if d.fd < 0 {
		return nil
	}
	d.thread.stop()
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
	var err error
	d.thread.run(func() { err = ioctlFn(d.fd, priv.Ioctl, priv.Buf) })
	if errors.Is(err, unix.ENOMEM) && priv.Ioctl != aacraid.IoctlGetHBAInfo {
		return fmt.Errorf("ioctl %#x on %s: %w: %w", req.Ioctl, d.path, err, ErrDriverNoMemory)
	}
	if err != nil {
		return fmt.Errorf("ioctl %#x on %s: %w", req.Ioctl, d.path, err)
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
func (d *Device) SupplementInfo(ctx context.Context) (aacraid.SupplementInfo, error) {
	if err := admit(ctx); err != nil {
		return aacraid.SupplementInfo{}, err
	}
	buf := aacraid.SupplementInfoRequest()
	if err := d.Do(allowlist.Request{Ioctl: aacraid.IoctlSendFIB, Buf: buf}); err != nil {
		return aacraid.SupplementInfo{}, err
	}
	return aacraid.DecodeSupplementInfo(buf)
}

// ConnectorMap envoie RequestConnectorMap (708) par FSACTL_SEND_LARGE_FIB,
// comme l'outil de référence, et décode la réponse.
func (d *Device) ConnectorMap(ctx context.Context) (aacraid.ConnectorMap, error) {
	if err := admit(ctx); err != nil {
		return aacraid.ConnectorMap{}, err
	}
	buf := aacraid.ConnectorMapRequest()
	if err := d.Do(allowlist.Request{Ioctl: aacraid.IoctlSendLargeFIB, Buf: buf}); err != nil {
		return aacraid.ConnectorMap{}, err
	}
	return aacraid.DecodeConnectorMap(buf)
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
// vaut false si la carte ne décrit pas de module dans ce format. Chacune des
// deux lectures est soumise à ctx.
func (d *Device) CacheProtection(ctx context.Context) (present bool, st aacraid.ZMMStatus, err error) {
	if err := admit(ctx); err != nil {
		return false, st, err
	}
	rsp, err := d.fib(aacraid.CacheProtectionPresenceRequest())
	if err != nil {
		return false, st, err
	}
	if present, err = aacraid.DecodeCacheProtectionPresence(rsp); err != nil || !present {
		return false, st, err
	}
	if err := admit(ctx); err != nil {
		return true, st, err
	}
	if rsp, err = d.fib(aacraid.CacheProtectionStateRequest()); err != nil {
		return true, st, err
	}
	st, err = aacraid.DecodeCacheProtectionState(rsp)
	return true, st, err
}

// stop clôture le transfert après le morceau n et vérifie la confirmation
// pour le même transfert. 0xc6 : lecture valide. 0xc5 (observé quand un
// autre outil interroge la carte en même temps) : le transfert est clos
// mais la lecture n'est pas valide (l'outil de référence la rejette) ;
// rejected le signale. Toute autre réponse est une erreur.
func (d *Device) stop(r aacraid.PagedRead, head aacraid.PagedResponse, n uint32) (rejected bool, err error) {
	req := aacraid.StopRequest(head.Total, head.Xfer, n)
	rsp, err := d.fib(append([]byte(nil), req...))
	if err != nil {
		return false, fmt.Errorf("read %d: stop: %w", r, err)
	}
	s, err := aacraid.DecodePagedResponse(req, rsp)
	if err != nil {
		return false, fmt.Errorf("read %d: stop: %w", r, err)
	}
	switch {
	case s.Xfer != head.Xfer || (s.Mark != 0xc6 && s.Mark != aacraid.MarkClosedOther):
		return false, fmt.Errorf("read %d: stop not confirmed (mark %#x, transfer %#x)", r, s.Mark, s.Xfer)
	case s.Mark == aacraid.MarkClosedOther:
		d.closeOther++
		return true, nil
	}
	return false, nil
}

// pagedAttempts : une lecture rejetée à la clôture (0xc5) est refaite une
// fois dans le même cycle ; un second rejet est une erreur.
const pagedAttempts = 2

// ReadPaged effectue une lecture paginée qualifiée (readOnce). Une lecture
// dont la clôture est confirmée par 0xc5 est rejetée, comme le fait l'outil
// de référence, puis refaite une seule fois. ctx est vérifié avant chaque
// ouverture de transfert, y compris celle de la seconde tentative, et avant
// chaque morceau ; un transfert ouvert est toujours clos.
func (d *Device) ReadPaged(ctx context.Context, r aacraid.PagedRead, param, size uint32, need func([]byte) int) ([]byte, error) {
	for attempt := 1; ; attempt++ {
		if err := admit(ctx); err != nil {
			if attempt > 1 {
				return nil, fmt.Errorf("read %d: data rejected (close status %#x), not read again: %w", r, aacraid.MarkClosedOther, err)
			}
			return nil, err
		}
		data, rejected, err := d.readOnce(ctx, r, param, size, need)
		if err != nil || !rejected {
			return data, err
		}
		if attempt == pagedAttempts {
			return nil, fmt.Errorf("read %d: close status %#x on %d attempts, data rejected", r, aacraid.MarkClosedOther, attempt)
		}
	}
}

// maxPagedTotal borne la taille d'un transfert accepté (plus grande taille
// qualifiée : liste des disques, 4 100 octets).
const maxPagedTotal = 0x1004

// ReadPaged effectue une lecture paginée qualifiée et renvoie au moins
// need(données lues) octets, ou tout le transfert. Le transfert est clôturé
// dès que les données suffisent, et dans tous les cas après son ouverture,
// même en cas d'erreur : le firmware ne garde pas de transfert ouvert. Si
// ctx se termine pendant le transfert, la séquence qualifiée la plus courte
// est menée à son terme (morceau 0, puis clôture), aucun autre morceau n'est
// demandé, et la lecture est en échec.
func (d *Device) readOnce(ctx context.Context, r aacraid.PagedRead, param, size uint32, need func([]byte) int) ([]byte, bool, error) {
	open, err := aacraid.OpenRequest(r, param, size)
	if err != nil {
		return nil, false, err
	}
	rsp, err := d.fib(append([]byte(nil), open...))
	if err != nil {
		return nil, false, err
	}
	head, err := aacraid.DecodePagedResponse(open, rsp)
	if err != nil {
		// Statut d'échec ou écho incorrect : aucun transfert établi.
		return nil, false, err
	}
	if head.Mark != 0xc9 || head.Total == 0 || head.Total > maxPagedTotal {
		// Ouverture non conforme : rien ne prouve qu'un transfert existe
		// sous cet identifiant. Aucune clôture n'est envoyée, plutôt que de
		// clore un transfert peut-être étranger ; le firmware garde au plus
		// un transfert ouvert, que l'erreur signale.
		return nil, false, fmt.Errorf("read %d: open refused (mark %#x, size %d); transfer %#x not stopped", r, head.Mark, head.Total, head.Xfer)
	}
	var data []byte
	last := uint32(0)
	readErr := func() error {
		for n := uint32(0); n < (head.Total+aacraid.ChunkData-1)/aacraid.ChunkData; n++ {
			// Le morceau 0 est toujours lu : la clôture n'a été observée,
			// chez l'outil de référence, qu'après au moins un morceau.
			if n > 0 {
				if err := admit(ctx); err != nil {
					return fmt.Errorf("read %d: %w", r, err)
				}
			}
			req := aacraid.ChunkRequest(head.Total, head.Xfer, n)
			rsp, err := d.fib(append([]byte(nil), req...))
			if err != nil {
				return err
			}
			c, err := aacraid.DecodePagedResponse(req, rsp)
			if err != nil {
				return err
			}
			if c.Mark != aacraid.MarkChunk || c.Xfer != head.Xfer || c.Chunk != n || c.Total != head.Total {
				return fmt.Errorf("read %d: inconsistent chunk %d (mark %#x, transfer %#x, chunk %d)", r, n, c.Mark, c.Xfer, c.Chunk)
			}
			last = n
			data = append(data, c.Data...)
			if len(data) >= int(head.Total) {
				data = data[:head.Total]
				if want := need(data); want > len(data) {
					return fmt.Errorf("read %d: %d bytes needed, transfer of %d", r, want, len(data))
				}
				return nil
			}
			if want := need(data); want <= len(data) {
				return nil
			} else if want > int(head.Total) {
				return fmt.Errorf("read %d: %d bytes requested for a transfer of %d", r, want, head.Total)
			}
		}
		return nil
	}()
	rejected, stopErr := d.stop(r, head, last)
	if err := errors.Join(readErr, stopErr); err != nil {
		return nil, false, err
	}
	// Lecture rejetée (0xc5) : aucune donnée n'est rendue ; c'est ReadPaged
	// qui décide d'une seconde tentative, sous son propre contrôle d'arrêt.
	if rejected {
		return nil, true, nil
	}
	// Arrêt pendant le transfert : la séquence engagée (morceau 0 puis
	// clôture) est allée au bout, mais la lecture n'est pas rendue.
	if err := admit(ctx); err != nil {
		return nil, false, fmt.Errorf("read %d: %w", r, err)
	}
	return data, false, nil
}
