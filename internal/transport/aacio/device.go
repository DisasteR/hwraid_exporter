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
	"sync/atomic"
	"time"
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

// queued, si non nil, est appelé juste avant qu'une commande soit confiée au
// thread dédié ; réservé aux tests (synchronisation).
var queued func()

// admitting, si non nil, est appelé sur le thread dédié juste avant
// l'admission d'une commande (tests).
var admitting func()

// ErrStopped : la collecte est arrêtée (échéance du cycle ou arrêt du
// service) ; aucune nouvelle commande n'est envoyée à la carte.
var ErrStopped = errors.New("collection stopped: no new card command sent")

// admit autorise une nouvelle commande firmware tant que ctx n'est pas
// terminé. La clôture d'un transfert déjà ouvert ne passe pas par admit :
// un transfert engagé est toujours clos.
//
// L'échéance est aussi comparée à l'horloge : ctx.Err() n'est renseigné que
// lorsque le minuteur du contexte s'exécute, parfois un peu après l'échéance.
func admit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w (%w)", ErrStopped, err)
	}
	if dl, ok := ctx.Deadline(); ok && !time.Now().Before(dl) {
		return fmt.Errorf("%w (%w)", ErrStopped, context.DeadlineExceeded)
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
	// containerListSize : seule taille de liste des conteneurs admise pendant
	// le cycle (0 : aucune ; ExpectContainerList).
	containerListSize uint32
	// validated : la carte est un couple modèle et firmware validé (CDC,
	// section 6.11, écarts E1 et E2). Faux par défaut : suite de commandes
	// de l'outil de référence.
	validated bool
	// highBit : la carte annonce le bit 13 de FeatureBits ; ouvertures et
	// CT_GET_CONTAINER_COUNT portent SizeHighBit au mot 7, comme l'outil.
	highBit bool
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
func (d *Device) Do(req allowlist.Request) error { return d.do(nil, req) }

// do émet req comme Do. Si ctx n'est pas nil, l'admission (admit) a lieu
// sur le thread dédié, juste avant l'appel système : c'est le point
// d'engagement de la commande. Une commande refusée là n'atteint jamais le
// noyau. ctx nil : commande déjà engagée (premier morceau et clôture d'un
// transfert ouvert).
func (d *Device) do(ctx context.Context, req allowlist.Request) error {
	if d.fd < 0 {
		return ErrClosed
	}
	priv := allowlist.Request{Ioctl: req.Ioctl, Buf: append([]byte(nil), req.Buf...)}
	if _, err := d.list.Check(priv); err != nil {
		return err
	}
	// Liste des conteneurs : la liste blanche admet toute taille 8 + 4 × n ;
	// seule celle qui découle du comptage de cette carte part (CDC, section
	// 6.11, contrôle des formes), quel que soit le chemin d'appel.
	if size, ok := aacraid.ContainerListOpenSize(priv.Buf); ok && priv.Ioctl == aacraid.IoctlSendFIB && (d.containerListSize == 0 || size != d.containerListSize) {
		return fmt.Errorf("%w: container list of %d bytes, %d expected from the card's container count (0: none)", allowlist.ErrDenied, size, d.containerListSize)
	}
	var err, refused error
	if queued != nil {
		queued()
	}
	d.thread.run(func() {
		if admitting != nil {
			admitting()
		}
		if ctx != nil {
			if refused = admit(ctx); refused != nil {
				return
			}
		}
		err = ioctlFn(d.fd, priv.Ioctl, priv.Buf)
	})
	if refused != nil {
		return refused
	}
	// Ioctl parti et en échec : échec d'échange (aacraid.ErrExchange), que
	// l'outil de référence traite par une exception.
	if errors.Is(err, unix.ENOMEM) && priv.Ioctl != aacraid.IoctlGetHBAInfo {
		return fmt.Errorf("ioctl %#x on %s: %w: %w: %w", req.Ioctl, d.path, err, ErrDriverNoMemory, aacraid.ErrExchange)
	}
	if err != nil {
		return fmt.Errorf("ioctl %#x on %s: %w: %w", req.Ioctl, d.path, err, aacraid.ErrExchange)
	}
	copy(req.Buf, priv.Buf)
	return nil
}

// HBAInfo lit FSACTL_GET_HBA_INFO (réponse du pilote, sans commande
// firmware). Le pilote la sert sous le même verrou que les commandes de
// gestion : elle peut attendre derrière un autre outil. Elle est donc
// soumise à ctx, comme une commande firmware, pour qu'un arrêt n'en
// engage pas une nouvelle.
func (d *Device) HBAInfo(ctx context.Context) (aacraid.HBAInfo, error) {
	buf := make([]byte, aacraid.HBAInfoSize)
	if err := d.do(ctx, allowlist.Request{Ioctl: aacraid.IoctlGetHBAInfo, Buf: buf}); err != nil {
		return aacraid.HBAInfo{}, err
	}
	return aacraid.DecodeHBAInfo(buf)
}

// Paused envoie IsAdapterPaused (704) : vrai si la carte est en pause.
// Soumise à ctx.
func (d *Device) Paused(ctx context.Context) (bool, error) {
	if err := admit(ctx); err != nil {
		return false, err
	}
	buf := aacraid.PausedRequest()
	if err := d.do(ctx, allowlist.Request{Ioctl: aacraid.IoctlSendFIB, Buf: buf}); err != nil {
		return false, err
	}
	return aacraid.DecodePaused(buf)
}

// Revision envoie CheckRevision (701). Soumise à ctx.
func (d *Device) Revision(ctx context.Context) (aacraid.Revision, error) {
	if err := admit(ctx); err != nil {
		return aacraid.Revision{}, err
	}
	buf := aacraid.RevisionRequest()
	if err := d.do(ctx, allowlist.Request{Ioctl: aacraid.IoctlSendFIB, Buf: buf}); err != nil {
		return aacraid.Revision{}, err
	}
	return aacraid.DecodeRevision(buf)
}

// TaskList envoie AifReqJobsForCtr (101) pour un conteneur et rend ses
// tâches. Soumise à ctx.
func (d *Device) TaskList(ctx context.Context, container uint32) ([]aacraid.Task, error) {
	if err := admit(ctx); err != nil {
		return nil, err
	}
	buf := aacraid.TaskListRequest(container)
	if err := d.do(ctx, allowlist.Request{Ioctl: aacraid.IoctlSendFIB, Buf: buf}); err != nil {
		return nil, err
	}
	return aacraid.DecodeTaskList(buf)
}

// TaskReport envoie AifReqJobReport (103) pour une tâche et rend son
// rapport de progression. Soumise à ctx.
func (d *Device) TaskReport(ctx context.Context, job uint32) (aacraid.Task, error) {
	if err := admit(ctx); err != nil {
		return aacraid.Task{}, err
	}
	buf := aacraid.TaskReportRequest(job)
	if err := d.do(ctx, allowlist.Request{Ioctl: aacraid.IoctlSendFIB, Buf: buf}); err != nil {
		return aacraid.Task{}, err
	}
	return aacraid.DecodeTaskReport(buf, job)
}

// SupplementInfo envoie RequestSupplementAdapterInfo (706) au firmware et
// décode la réponse.
func (d *Device) SupplementInfo(ctx context.Context) (aacraid.SupplementInfo, error) {
	if err := admit(ctx); err != nil {
		return aacraid.SupplementInfo{}, err
	}
	buf := aacraid.SupplementInfoRequest()
	if err := d.do(ctx, allowlist.Request{Ioctl: aacraid.IoctlSendFIB, Buf: buf}); err != nil {
		return aacraid.SupplementInfo{}, err
	}
	return aacraid.DecodeSupplementInfo(buf)
}

// AdapterInfo lit RequestAdapterInfo (703), dont SupportedOptions annonce le
// capteur de température. Soumise à ctx.
func (d *Device) AdapterInfo(ctx context.Context) (aacraid.AdapterInfo, error) {
	if err := admit(ctx); err != nil {
		return aacraid.AdapterInfo{}, err
	}
	buf := aacraid.AdapterInfoRequest()
	if err := d.do(ctx, allowlist.Request{Ioctl: aacraid.IoctlSendFIB, Buf: buf}); err != nil {
		return aacraid.AdapterInfo{}, err
	}
	return aacraid.DecodeAdapterInfo(buf)
}

// Environment lit AdapterEnvironmentInfo (803) : température de la carte et
// seuils annoncés. Soumise à ctx.
func (d *Device) Environment(ctx context.Context) (aacraid.Environment, error) {
	if err := admit(ctx); err != nil {
		return aacraid.Environment{}, err
	}
	buf := aacraid.EnvironmentRequest()
	if err := d.do(ctx, allowlist.Request{Ioctl: aacraid.IoctlSendFIB, Buf: buf}); err != nil {
		return aacraid.Environment{}, err
	}
	return aacraid.DecodeEnvironment(buf)
}

// ContainerCount lit CT_GET_CONTAINER_COUNT et rend MaxContainers décodé,
// borné à 1..aacraid.MaxContainersLimit. Soumise à ctx.
func (d *Device) ContainerCount(ctx context.Context) (aacraid.ContainerCount, error) {
	if err := admit(ctx); err != nil {
		return aacraid.ContainerCount{}, err
	}
	buf := aacraid.ContainerCountRequest()
	if d.highBit {
		buf = aacraid.WithSizeHighBit(buf)
	}
	if err := d.do(ctx, allowlist.Request{Ioctl: aacraid.IoctlSendFIB, Buf: buf}); err != nil {
		return aacraid.ContainerCount{}, err
	}
	return aacraid.DecodeContainerCount(buf)
}

// ExpectContainerList fixe la taille de la liste des conteneurs (229) que ce
// périphérique accepte d'envoyer pendant le cycle : celle qui découle d'un
// comptage décodé d'une réponse de cette carte (lu par ContainerCount, ou
// gardé par le module sous l'identité de la carte). Sans comptage (valeur
// nulle), aucune liste des conteneurs n'est envoyée (CDC, section 6.11,
// contrôle des formes : la taille découle des capacités de la carte
// destinataire).
func (d *Device) ExpectContainerList(c aacraid.ContainerCount) {
	d.containerListSize = c.ListSize()
}

// ConnectorMap envoie RequestConnectorMap (708) par FSACTL_SEND_LARGE_FIB,
// comme l'outil de référence, et décode la réponse.
func (d *Device) ConnectorMap(ctx context.Context) (aacraid.ConnectorMap, error) {
	if err := admit(ctx); err != nil {
		return aacraid.ConnectorMap{}, err
	}
	buf := aacraid.ConnectorMapRequest()
	if err := d.do(ctx, allowlist.Request{Ioctl: aacraid.IoctlSendLargeFIB, Buf: buf}); err != nil {
		return aacraid.ConnectorMap{}, err
	}
	return aacraid.DecodeConnectorMap(buf)
}

// fib envoie une requête FIB qualifiée et renvoie la réponse.
// ctx est transmis à do (nil : commande déjà engagée).
func (d *Device) fib(ctx context.Context, req []byte) ([]byte, error) {
	if err := d.do(ctx, allowlist.Request{Ioctl: aacraid.IoctlSendFIB, Buf: req}); err != nil {
		return nil, err
	}
	return req, nil
}

// CacheProtection lit l'état du module de protection du cache : lecture de
// présence, puis lecture d'état si la carte y décrit son module. present
// vaut false si la carte ne décrit pas de module dans ce format. Chacune des
// deux lectures est soumise à ctx.
func (d *Device) CacheProtection(ctx context.Context) (rd aacraid.CacheProtectionReading, err error) {
	if err := admit(ctx); err != nil {
		return rd, err
	}
	rsp, err := d.fib(ctx, aacraid.CacheProtectionPresenceRequest())
	if err != nil {
		return rd, err
	}
	if rd.Flash, err = aacraid.DecodeCacheProtectionPresence(rsp); err != nil {
		return rd, err
	}
	if !rd.Flash {
		// Sans le bit 0x2000 : batterie classique, décodée de cette seule
		// réponse (0xa0000, que l'outil envoie pour un champ inutile à
		// l'état, n'est pas envoyée).
		rd.Battery, err = aacraid.DecodeBattery(rsp)
		return rd, err
	}
	if err := admit(ctx); err != nil {
		return rd, err
	}
	if rsp, err = d.fib(ctx, aacraid.CacheProtectionStateRequest()); err != nil {
		return rd, err
	}
	rd.ZMM, err = aacraid.DecodeCacheProtectionState(rsp)
	return rd, err
}

// stop clôture le transfert après le morceau n et vérifie la confirmation
// pour le même transfert. 0xc6 : lecture valide. 0xc5 (observé quand un
// autre outil interroge la carte en même temps) : le transfert est clos
// mais la lecture n'est pas valide (l'outil de référence la rejette) ;
// rejected le signale. Toute autre réponse est une erreur.
// window : nil sur le couple validé (clôture engagée, toujours envoyée) ;
// ailleurs, fenêtre d'arrêt de readOnce, admise sur le thread dédié.
func (d *Device) stop(window context.Context, r aacraid.PagedRead, head aacraid.PagedResponse, n uint32) (rejected bool, err error) {
	return d.stopWith(window, r, head, aacraid.StopRequest(head.Total, head.Xfer, n), true)
}

// stopWith envoie la clôture req du transfert head (stop). count : une
// confirmation 0xc5 rejette une lecture et est comptée (CloseUnconfirmed) ;
// faux pour la clôture d'une ouverture non nominale, dont la lecture est
// déjà en échec.
func (d *Device) stopWith(window context.Context, r aacraid.PagedRead, head aacraid.PagedResponse, req []byte, count bool) (rejected bool, err error) {
	rsp, err := d.fib(window, append([]byte(nil), req...))
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
		if count {
			d.closeOther++
		}
		return true, nil
	}
	return false, nil
}

// SetValidatedCouple indique si la carte est un couple modèle et firmware
// validé, seul à recevoir les écarts E1 (arrêt anticipé des morceaux, et
// séquence abrégée à l'arrêt) et E2 (relecture après 0xc5). Sinon, le
// transport envoie la suite de commandes de l'outil de référence : tous les
// morceaux, puis la clôture, sans relecture (CDC, section 6.11, règle de
// conduite). Par défaut : non validé.
func (d *Device) SetValidatedCouple(v bool) { d.validated = v }

// SetPagedHighBit indique que la carte annonce le bit 13 de FeatureBits
// (706) : le mot 7 des ouvertures de transfert et de CT_GET_CONTAINER_COUNT
// porte alors aacraid.SizeHighBit, comme chez l'outil de référence. Par
// défaut : absent.
func (d *Device) SetPagedHighBit(v bool) { d.highBit = v }

// pagedAttempts : sur un couple validé (E2), une lecture rejetée à la
// clôture (0xc5) est refaite une fois dans le même cycle ; un second rejet
// est une erreur.
const pagedAttempts = 2

// ReadPaged effectue une lecture paginée qualifiée (readOnce). Une lecture
// dont la clôture est confirmée par 0xc5 est rejetée, comme le fait l'outil
// de référence, puis refaite une seule fois. ctx est vérifié avant chaque
// ouverture de transfert, y compris celle de la seconde tentative, et avant
// chaque morceau.
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
		if !d.validated {
			// Comme l'outil de référence : la lecture rejetée n'est pas
			// refaite.
			return nil, fmt.Errorf("read %d: data rejected (close status %#x), not read again on a card outside the validated pair", r, aacraid.MarkClosedOther)
		}
		if attempt == pagedAttempts {
			return nil, fmt.Errorf("read %d: close status %#x on %d attempts, data rejected", r, aacraid.MarkClosedOther, attempt)
		}
	}
}

// stopGrace borne, hors du couple validé, la durée pendant laquelle de
// nouvelles commandes d'un transfert engagé partent encore après l'arrêt
// demandé (suite de l'outil : morceaux restants, puis clôture). Comptée
// depuis l'arrêt lui-même ; au-delà, le transfert est abandonné sans
// clôture (règle 2). Une commande déjà partie n'est jamais interrompue.
// Dix morceaux et une clôture prennent bien moins d'une seconde en
// fonctionnement normal.
var stopGrace = 2 * time.Second

// errGraceExceeded : cause d'un refus d'admission par la fenêtre d'arrêt
// (stopGrace écoulé depuis l'arrêt) pendant un transfert hors du couple
// validé ; abandon sans clôture.
var errGraceExceeded = errors.New("stop grace exceeded during the transfer: abandoned without stop")

// stopWindow est la fenêtre d'arrêt d'un transfert hors du couple validé :
// un contexte d'admission dont Err devient errGraceExceeded quand stopGrace
// s'est écoulé depuis l'arrêt de stop. L'instant de l'arrêt est l'échéance
// de stop si elle est passée (exacte), sinon le premier constat de son
// annulation (rappel d'annulation, ou première admission qui la voit).
// Aucun minuteur : la comparaison a lieu à l'admission, sur le thread
// dédié, en temps monotone.
type stopWindow struct {
	stop  context.Context
	grace time.Duration
	at    atomic.Pointer[time.Time]
}

// mark enregistre l'instant de l'arrêt s'il ne l'est pas déjà.
func (w *stopWindow) mark() {
	now := time.Now()
	w.at.CompareAndSwap(nil, &now)
}

func (w *stopWindow) Deadline() (time.Time, bool) { return time.Time{}, false }
func (w *stopWindow) Done() <-chan struct{}       { return nil }
func (w *stopWindow) Value(any) any               { return nil }

// Err : nil tant que la fenêtre est ouverte, errGraceExceeded ensuite.
func (w *stopWindow) Err() error {
	if w.stop.Err() == nil {
		if dl, ok := w.stop.Deadline(); !ok || time.Now().Before(dl) {
			return nil
		}
	}
	at := time.Now()
	if dl, ok := w.stop.Deadline(); ok && !at.Before(dl) {
		at = dl
	}
	w.at.CompareAndSwap(nil, &at)
	start := *w.at.Load()
	if dl, ok := w.stop.Deadline(); ok && dl.Before(start) {
		start = dl
	}
	if time.Since(start) > w.grace {
		return errGraceExceeded
	}
	return nil
}

// maxPagedTotal borne la taille d'un transfert accepté (plus grande taille
// qualifiée : liste des disques, 4 100 octets).
const maxPagedTotal = 0x1004

// readOnce effectue une lecture paginée qualifiée et renvoie au moins
// need(données lues) octets, ou tout le transfert (CDC, section 6.11,
// déroulement d'un transfert). Sur un couple validé (E1), le transfert est
// clôturé dès que les données suffisent ; si ctx se termine pendant le
// transfert, la séquence qualifiée la plus courte est menée à son terme
// (morceau 0, puis clôture), aucun autre morceau n'est demandé, et la
// lecture est en échec. Sur une autre carte, tous les morceaux sont lus
// puis le transfert est clôturé, comme l'outil de référence, y compris si
// ctx se termine après l'ouverture (au plus 10 morceaux, borne de taille) ;
// la lecture est alors en échec. Toute autre
// erreur après l'ouverture (ioctl en échec, réponse de morceau invalide ou
// incohérente, données rejetées par need) abandonne le transfert sans
// clôture, comme l'outil de référence sur une erreur de morceau : aucune
// commande de ce transfert ne suit (règle 2).
func (d *Device) readOnce(ctx context.Context, r aacraid.PagedRead, param, size uint32, need func([]byte) int) ([]byte, bool, error) {
	open, err := aacraid.OpenRequest(r, param, size)
	if err != nil {
		return nil, false, err
	}
	if d.highBit {
		open = aacraid.WithSizeHighBit(open)
	}
	// Hors couple validé : fenêtre d'arrêt, close stopGrace après la fin de
	// ctx (comptée depuis l'arrêt lui-même : installée avant l'ouverture).
	// Chaque morceau et la clôture y sont admis sur le thread dédié, juste
	// avant l'ioctl ; une fenêtre close abandonne le transfert sans clôture.
	var window context.Context
	if !d.validated {
		w := &stopWindow{stop: ctx, grace: stopGrace}
		release := context.AfterFunc(ctx, w.mark)
		defer release()
		window = w
	}
	rsp, err := d.fib(ctx, append([]byte(nil), open...))
	if err != nil {
		return nil, false, err
	}
	head, err := aacraid.DecodePagedResponse(open, rsp)
	if err != nil {
		// Statut d'échec ou écho incorrect : aucun transfert établi.
		return nil, false, err
	}
	if head.StopAfterOpen() {
		// Code 0xe2, total nul ou code autre que CT_OK : clôture sans
		// morceau, mots repris de la réponse d'ouverture comme l'outil de
		// référence ; la lecture est en échec, aucune donnée n'est rendue.
		_, stopErr := d.stopWith(window, r, head, aacraid.StopAfterOpenRequest(head), false)
		if stopErr != nil {
			return nil, false, fmt.Errorf("read %d: open not nominal (mark %#x, code %#x, size %d); %w", r, head.Mark, head.Code, head.Total, stopErr)
		}
		return nil, false, fmt.Errorf("read %d: open not nominal (mark %#x, code %#x, size %d); transfer %#x stopped", r, head.Mark, head.Code, head.Total, head.Xfer)
	}
	if head.Mark == aacraid.MarkChunk && head.Code == aacraid.CodeOK && head.Xfer != aacraid.XferNone && head.Total != 0 && head.Total <= maxPagedTotal {
		return d.readInline(ctx, r, open, head, need)
	}
	if head.Xfer == aacraid.XferNone || head.Mark != 0xc9 || head.Code != aacraid.CodeOK || head.Total == 0 || head.Total > maxPagedTotal || head.Size != head.Total {
		// Autre ouverture hors du parcours nominal (sans transfert, marque
		// autre, parcours 0xc8 des données incluses, total hors bornes,
		// taille différente du total) : aucune commande n'est envoyée pour
		// ce transfert. L'effet sur la carte d'un transfert laissé ouvert
		// n'est pas établi ; l'erreur le signale.
		return nil, false, fmt.Errorf("read %d: open refused (mark %#x, code %#x, size %d); transfer %#x not stopped", r, head.Mark, head.Code, head.Total, head.Xfer)
	}
	var data []byte
	last := uint32(0)
	readErr := func() error {
		for n := uint32(0); n < (head.Total+aacraid.ChunkData-1)/aacraid.ChunkData; n++ {
			// Le morceau 0 est toujours lu : la clôture n'a été observée,
			// chez l'outil de référence, qu'après au moins un morceau. Sur
			// un couple validé, les suivants sont soumis à ctx ; ailleurs,
			// la suite de l'outil est menée à son terme.
			abridged := n > 0 && d.validated
			if abridged {
				if err := admit(ctx); err != nil {
					return fmt.Errorf("read %d: %w", r, err)
				}
			}
			req := aacraid.ChunkRequest(head.Total, head.Xfer, n)
			// Couple validé : morceau 0 engagé (nil), suivants soumis à
			// ctx ; ailleurs, tous soumis à la fenêtre d'arrêt.
			engaged := window
			if d.validated && abridged {
				engaged = ctx
			} else if d.validated {
				engaged = nil
			}
			rsp, err := d.fib(engaged, append([]byte(nil), req...))
			if err != nil {
				return err
			}
			c, err := aacraid.DecodePagedResponse(req, rsp)
			if err != nil {
				return err
			}
			// Mots que l'outil de référence reprend dans la requête suivante
			// (2, 3, 4, 7) : seules les valeurs que porte la requête de
			// l'exporter sont admises, pour qu'elle reste celle de l'outil.
			if c.Mark != aacraid.MarkChunk || c.Xfer != head.Xfer || c.Chunk != n || c.Total != head.Total || c.Code != aacraid.CodeOK || c.Size != head.Total {
				return fmt.Errorf("read %d: inconsistent chunk %d (mark %#x, transfer %#x, chunk %d, code %#x, size %d)", r, n, c.Mark, c.Xfer, c.Chunk, c.Code, c.Size)
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
			if want := need(data); want <= len(data) && d.validated {
				return nil
			} else if want > int(head.Total) {
				return fmt.Errorf("read %d: %d bytes requested for a transfer of %d", r, want, head.Total)
			}
		}
		return nil
	}()
	if readErr != nil && (!errors.Is(readErr, ErrStopped) || errors.Is(readErr, errGraceExceeded)) {
		// Erreur ou rejet en cours de transfert : abandon sans clôture.
		return nil, false, fmt.Errorf("%w; transfer %#x abandoned without stop", readErr, head.Xfer)
	}
	// Données suffisantes, ou arrêt demandé après un morceau conforme
	// (ErrStopped : le morceau suivant n'est pas parti) : clôture. Hors
	// couple validé, elle passe par la fenêtre d'arrêt : refusée sur le
	// thread dédié si la fenêtre est close (abandon sans clôture).
	rejected, stopErr := d.stop(window, r, head, last)
	if errors.Is(stopErr, errGraceExceeded) {
		// Clôture refusée à l'admission (non partie) : abandon sans clôture.
		return nil, false, fmt.Errorf("%w; transfer %#x abandoned without stop", stopErr, head.Xfer)
	}
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

// readInline mène le parcours 0xc8 (ligne 7 du déroulement, CDC section
// 6.11) : l'ouverture porte déjà les premières données ; les morceaux 1 à
// n suivent, puis rien (l'outil ne clôture pas ce parcours). Comme l'outil
// (2.05 CT_SendReceiveFIB 0x8ddf56) : pas = mot 6 de la réponse
// d'ouverture, tous les morceaux sont lus, mot 7 des demandes = taille
// demandée | SizeHighBit. Plus strict que l'outil : pas nul ou supérieur
// aux octets écrits par la carte refusé sans commande ; chaque réponse de
// morceau doit porter la marque 0xc8, le transfert, son numéro, le total
// et CT_OK (les mots que la demande suivante reprend), sinon abandon. Les
// morceaux restants passent par la fenêtre d'arrêt, sur toute carte : sans
// clôture, arrêter en route laisserait le transfert à mi-chemin.
func (d *Device) readInline(ctx context.Context, r aacraid.PagedRead, open []byte, head aacraid.PagedResponse, need func([]byte) int) ([]byte, bool, error) {
	step, ok := head.InlineStep()
	if !ok {
		return nil, false, fmt.Errorf("read %d: open with inline data refused (step %d, at most %d); transfer %#x not continued", r, step, aacraid.ChunkData, head.Xfer)
	}
	w := &stopWindow{stop: ctx, grace: stopGrace}
	release := context.AfterFunc(ctx, w.mark)
	defer release()
	requested := aacraid.RequestedSize(open)
	take := func(b []byte, left uint32) []byte { return b[:min(step, left)] }
	data := append([]byte(nil), take(head.Data, head.Total)...)
	count := (head.Total + step - 1) / step
	for n := uint32(1); n < count; n++ {
		req := aacraid.InlineChunkRequest(head.Total, head.Xfer, n, requested)
		rsp, err := d.fib(w, append([]byte(nil), req...))
		if err != nil {
			return nil, false, fmt.Errorf("read %d: %w; transfer %#x abandoned", r, err, head.Xfer)
		}
		c, err := aacraid.DecodePagedResponse(req, rsp)
		if err != nil {
			return nil, false, fmt.Errorf("read %d: %w; transfer %#x abandoned", r, err, head.Xfer)
		}
		if c.Mark != aacraid.MarkChunk || c.Xfer != head.Xfer || c.Chunk != n || c.Total != head.Total || c.Code != aacraid.CodeOK {
			return nil, false, fmt.Errorf("read %d: inconsistent inline chunk %d (mark %#x, transfer %#x, chunk %d, total %d, code %#x); transfer %#x abandoned", r, n, c.Mark, c.Xfer, c.Chunk, c.Total, c.Code, head.Xfer)
		}
		data = append(data, take(c.Data, head.Total-uint32(len(data)))...)
	}
	// Arrêt pendant le parcours : la suite est allée au bout, mais la
	// lecture n'est pas rendue.
	if err := admit(ctx); err != nil {
		return nil, false, fmt.Errorf("read %d: %w", r, err)
	}
	if want := need(data); want > len(data) {
		return nil, false, fmt.Errorf("read %d: %d bytes needed, transfer of %d", r, want, len(data))
	}
	return data, false, nil
}
