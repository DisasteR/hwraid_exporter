// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Transfert paginé VM_ContainerConfig (docs/aacraid-interface.md, section 4).
// Formes des requêtes identiques à celles de l'outil de référence : en-tête
// XferState = HostOwned | ApiFib, Size = 480, SenderSize = 512 ; données :
//
//	ouverture : [2, sous-commande, paramètre, 0, marque, 0xffffffff, 0, taille]
//	morceau   : [2, 83, 0xc9 (premier) | 0xc8, total, 0xda, transfert, n, total]
//	clôture   : [2, 84, 0xc8, total, 0xda, transfert, dernier n, total]
//	clôture après l'ouverture : [2, 84, mots 2 à 7 de la réponse d'ouverture]
//	morceau du parcours 0xc8 : [2, 83, 0xc8, total, 0xda, transfert, n ≥ 1,
//	                            taille demandée | 0x80000000]
//
// FeatureBits (706) bit 13 : mot 7 de l'ouverture (et du comptage des
// conteneurs et de CT_ALARM_GET_STATE) | 0x80000000 (SizeHighBit).
//
// reste à zéro. Les octets que l'outil laisse au-delà des huit mots sont des
// résidus de ses propres tampons : ils ne sont pas reproduits.
const (
	vmContainerConfig = 2
	ctContinueData    = 83
	ctStopData        = 84

	pagedDataSize  = 480
	pagedHeadWords = 8
	// ChunkData est le nombre d'octets de données par morceau : la réponse
	// annonce Size = 496 (32 d'en-tête, 32 de mots de contrôle, 432 de
	// données) et, sur les 325 réponses de morceau capturées, les octets
	// suivants ne sont jamais écrits par la carte.
	ChunkData = 432

	markFirst = 0xc9
	markNext  = 0xc8
	// MarkChunk est la marque de toute réponse de morceau, et ChunkRspSize
	// son champ Size (en-tête compris) : forme constante sur les 325
	// réponses de morceau capturées.
	MarkChunk    = 0xc8
	ChunkRspSize = 496
	markClosed   = 0xc6
	// MarkClosedOther : statut de clôture 197, observé quand un autre outil
	// (maxView) interroge la carte en même temps. Le transfert est clos, mais
	// l'outil de référence rejette alors la lecture (il n'accepte que le
	// statut CT_OK) : l'exporter la rejette aussi et la refait une fois.
	MarkClosedOther = 0xc5
	markDA          = 0xda
	// codeE2 : code d'ouverture (mot 4) sur lequel l'outil de référence
	// clôture le transfert sans lire de morceau ; sens non établi.
	codeE2 = 0xe2
	// XferNone : identifiant de transfert sentinelle (mot 5 d'une
	// ouverture) ; jamais un transfert établi.
	XferNone = 0xffffffff
)

// PagedRead décrit une lecture paginée qualifiée.
type PagedRead uint32

// Sous-commandes de lecture qualifiées, avec la marque (mot 4) et la taille
// (mot 7) de leur ouverture ; taille 0 : fixée par l'appelant et vérifiée
// par sizeOK (liste des conteneurs : ContainerListSize ; liste des membres :
// MemberListSize).
const (
	ReadContainerList PagedRead = 229 // liste des conteneurs, paramètre 0
	ReadContainerInfo PagedRead = 234 // information de conteneur, paramètre : numéro
	ReadDiskList      PagedRead = 247 // liste des disques présents, paramètre 0
	ReadDiskInfo      PagedRead = 248 // information de disque, paramètre : numéro
	ReadMemberList    PagedRead = 249 // liste des membres, paramètre : UID
)

type pagedForm struct {
	mark   uint32
	size   uint32
	param  func(uint32) bool
	sizeOK func(uint32) bool // taille fixée par l'appelant (size = 0)
}

var pagedForms = map[PagedRead]pagedForm{
	ReadContainerList: {markDA, 0, func(p uint32) bool { return p == 0 }, validContainerListSize},
	ReadContainerInfo: {markDA, 0x1000, func(p uint32) bool { return p <= 0xffff }, nil},
	ReadDiskList:      {0, 0x1004, func(p uint32) bool { return p == 0 }, nil},
	ReadDiskInfo:      {0, 0x1000, func(p uint32) bool { return p <= 0xffff }, nil},
	ReadMemberList:    {0, 0, func(p uint32) bool { return p != 0 && p != 0xffffffff }, validMemberListSize},
}

// MaxMembers borne le nombre de membres d'un conteneur accepté dans une
// requête de liste de membres.
const MaxMembers = 64

// MemberListSize est la taille demandée pour la liste de n membres, comme le
// fait l'outil de référence : en-tête de 8 octets et 32 octets par membre.
func MemberListSize(n int) uint32 { return uint32(8 + 32*n) }

func validMemberListSize(size uint32) bool {
	return size >= MemberListSize(1) && size <= MemberListSize(MaxMembers) && (size-8)%32 == 0
}

func pagedRequest(words ...uint32) []byte {
	b := make([]byte, FIBSize)
	le := binary.LittleEndian
	le.PutUint32(b[fibXferState:], xferHostOwnedAPIFib)
	le.PutUint16(b[fibCommand:], cmdContainerCommand)
	b[fibStructType] = reqStructType
	le.PutUint16(b[fibSize:], pagedDataSize)
	le.PutUint16(b[fibSenderSize:], FIBSize)
	for i, w := range words {
		le.PutUint32(b[FIBHeaderSize+4*i:], w)
	}
	return b
}

// OpenRequest construit l'ouverture d'une lecture paginée. size n'est
// utilisé que pour la liste des conteneurs (ContainerListSize) et la liste
// des membres (MemberListSize).
func OpenRequest(r PagedRead, param uint32, size uint32) ([]byte, error) {
	f, ok := pagedForms[r]
	if !ok || !f.param(param) {
		return nil, fmt.Errorf("read %d, parameter %#x: not qualified", r, param)
	}
	if f.size != 0 {
		size = f.size
	} else if !f.sizeOK(size) {
		return nil, fmt.Errorf("read %d: size %d not qualified", r, size)
	}
	return pagedRequest(vmContainerConfig, uint32(r), param, 0, f.mark, 0xffffffff, 0, size), nil
}

// ChunkRequest construit la lecture du morceau n.
func ChunkRequest(total, xfer, n uint32) []byte {
	mark := uint32(markNext)
	if n == 0 {
		mark = markFirst
	}
	return pagedRequest(vmContainerConfig, ctContinueData, mark, total, markDA, xfer, n, total)
}

// StopRequest construit la clôture d'un transfert dont le dernier morceau
// lu est n.
func StopRequest(total, xfer, n uint32) []byte {
	return pagedRequest(vmContainerConfig, ctStopData, markNext, total, markDA, xfer, n, total)
}

// StopAfterOpen indique une ouverture hors du parcours nominal que
// l'exporter clôture sans lire de morceau (docs/aacraid-interface.md,
// section 4) : marque 0xc9 ou 0xc8 avec le code 0xe2 (l'outil de référence
// clôture de même), marque 0xc9 avec un total nul (de même) ou un code
// autre que CT_OK (l'outil lit d'abord tous les morceaux : l'exporter n'en
// lit aucun). Seulement pour un transfert établi dont les mots repris dans
// la clôture restent dans les bornes de la liste blanche : morceau 0,
// taille égale au total, total d'au plus 0x1004. Ni la marque 0xc8 avec un
// autre code (parcours des données incluses, non implémenté) ni une
// ouverture CT_OK à total non nul (lecture nominale) n'en relèvent.
func (r PagedResponse) StopAfterOpen() bool {
	if r.Xfer == XferNone || r.Chunk != 0 || r.Size != r.Total || r.Total > 0x1004 {
		return false
	}
	switch r.Mark {
	case markFirst:
		return r.Code != markDA || r.Total == 0
	case markNext:
		return r.Code == codeE2
	}
	return false
}

// StopAfterOpenRequest construit la clôture d'une ouverture StopAfterOpen,
// comme l'outil de référence : mots 2 à 7 de la réponse d'ouverture repris
// (marque, total, code, transfert, morceau, taille).
func StopAfterOpenRequest(open PagedResponse) []byte {
	return pagedRequest(vmContainerConfig, ctStopData, open.Mark, open.Total, open.Code, open.Xfer, open.Chunk, open.Size)
}

// SizeHighBit est le bit de poids fort que l'outil de référence ajoute à la
// taille demandée (mot 7) : sur l'ouverture, sur CT_GET_CONTAINER_COUNT et
// sur CT_ALARM_GET_STATE quand FeatureBits (706) porte le bit 13 (sens non
// établi), et sur chaque morceau du parcours 0xc8, quel que soit ce bit.
const SizeHighBit = 0x80000000

// WithSizeHighBit rend une copie de la requête b (ouverture,
// CT_GET_CONTAINER_COUNT ou CT_ALARM_GET_STATE) dont le mot 7 porte
// SizeHighBit (forme de l'outil pour une carte qui annonce le bit 13 de
// FeatureBits).
func WithSizeHighBit(b []byte) []byte {
	out := append([]byte(nil), b...)
	le := binary.LittleEndian
	le.PutUint32(out[FIBHeaderSize+28:], le.Uint32(out[FIBHeaderSize+28:])|SizeHighBit)
	return out
}

// RequestedSize rend la taille demandée (mot 7, sans SizeHighBit) d'une
// requête d'ouverture.
func RequestedSize(open []byte) uint32 {
	return binary.LittleEndian.Uint32(open[FIBHeaderSize+28:]) &^ SizeHighBit
}

// InlineStep rend le pas du parcours 0xc8 (données portées par
// l'ouverture), calculé comme l'outil de référence (2.05 0x8ddf56..0x8ddf78,
// 3.07 0x8b03cb..0x8b03e7) : min(mot 6, SenderSize de la réponse − 64).
// ok est faux, et l'exporter refuse sans commande, pour un mot 6 nul
// (l'outil prend alors un pas d'un octet, jusqu'à 4 100 morceaux), un
// SenderSize d'au plus 64 (l'outil divise alors par zéro) ou un pas
// supérieur aux ChunkData octets que la carte écrit dans une réponse de
// 496.
func (r PagedResponse) InlineStep() (step uint32, ok bool) {
	if r.Chunk == 0 || r.SenderSize <= 64 {
		return 0, false
	}
	step = min(r.Chunk, r.SenderSize-64)
	return step, step <= ChunkData
}

// InlineChunkRequest construit la demande du morceau n (n ≥ 1) du parcours
// 0xc8, comme l'outil de référence : marque 0xc8, total, code CT_OK et
// transfert repris de la réponse précédente (que le transport exige
// identiques), taille demandée à l'ouverture | SizeHighBit au mot 7.
func InlineChunkRequest(total, xfer, n, requested uint32) []byte {
	return pagedRequest(vmContainerConfig, ctContinueData, markNext, total, markDA, xfer, n, requested|SizeHighBit)
}

// qualifiedOpenSize indique une taille demandée par l'une des ouvertures
// qualifiées (pagedForms) : taille fixe, ou taille calculée bornée.
func qualifiedOpenSize(size uint32) bool {
	for _, f := range pagedForms {
		if (f.size != 0 && size == f.size) || (f.size == 0 && f.sizeOK(size)) {
			return true
		}
	}
	return false
}

// chunks est le nombre de morceaux d'un transfert de total octets.
func chunks(total uint32) uint32 { return (total + ChunkData - 1) / ChunkData }

// checkPagedRequest accepte une requête paginée qualifiée : chaque mot est
// vérifié, seuls le paramètre, le transfert, le numéro de morceau et la
// taille sont libres dans leurs bornes ; tout le reste du tampon est nul.
func checkPagedRequest(b []byte) bool {
	if len(b) != FIBSize {
		return false
	}
	le := binary.LittleEndian
	hdr := pagedRequest()
	if string(b[:12]) != string(hdr[:12]) {
		return false
	}
	for _, x := range b[12:FIBHeaderSize] {
		if x != 0 {
			return false
		}
	}
	for _, x := range b[FIBHeaderSize+4*pagedHeadWords:] {
		if x != 0 {
			return false
		}
	}
	w := func(i int) uint32 { return le.Uint32(b[FIBHeaderSize+4*i:]) }
	if w(0) != vmContainerConfig {
		return false
	}
	switch sub := w(1); sub {
	case ctGetContainerCount:
		return w(2) == 0 && w(3) == 0 && w(4) == 0 && w(5) == 0xffffffff && w(6) == 0 && (w(7) == 0 || w(7) == SizeHighBit)
	case ctAlarmGetState:
		return w(2) == alarmParam && w(3) == 0 && w(4) == 0 && w(5) == 0xffffffff && w(6) == 0 && (w(7) == alarmSize || w(7) == alarmSize|SizeHighBit)
	case ctContinueData, ctStopData:
		if sub == ctStopData && (PagedResponse{Mark: w(2), Total: w(3), Code: w(4), Xfer: w(5), Chunk: w(6), Size: w(7)}).StopAfterOpen() {
			return true
		}
		total, n := w(3), w(6)
		if sub == ctContinueData && w(7)&SizeHighBit != 0 {
			// Morceau du parcours 0xc8 : n ≥ 1, marque 0xc8, taille demandée
			// égale à celle d'une ouverture qualifiée ; n < total (pas d'au
			// moins un octet).
			return w(2) == markNext && total != 0 && total <= 0x1004 && w(4) == markDA && w(5) != XferNone &&
				n >= 1 && n < total && qualifiedOpenSize(w(7)&^SizeHighBit)
		}
		if total == 0 || total > 0x1004 || w(7) != total || w(4) != markDA || w(5) == XferNone || n >= chunks(total) {
			return false
		}
		mark := w(2)
		if sub == ctStopData {
			return mark == markNext
		}
		return (n == 0 && mark == markFirst) || (n > 0 && mark == markNext)
	default:
		r := PagedRead(sub)
		f, ok := pagedForms[r]
		if !ok || !f.param(w(2)) || w(3) != 0 || w(4) != f.mark || w(5) != 0xffffffff || w(6) != 0 {
			return false
		}
		size := w(7) &^ SizeHighBit // bit 13 de FeatureBits : SizeHighBit admis
		if f.size != 0 {
			return size == f.size
		}
		return f.sizeOK(size)
	}
}

// ErrExchange marque l'échec d'un échange avec la carte que l'outil de
// référence traite par une exception (2.05 CT_SendReceiveFIB 0x8dda1d,
// 3.07 0x8afeea) : ioctl en échec, statut non nul ou écho incorrect, sur
// une ouverture, un morceau ou une clôture. Pendant les lectures de
// configuration, l'outil abandonne alors l'ouverture de la carte : rien
// d'autre ne part ; l'exporter arrête de même la carte pour le cycle. Un
// code refusé ou un contrôle propre à l'exporter n'en est pas un.
var ErrExchange = errors.New("card exchange failed (ioctl, status or echo)")

// ErrTransferUnsettled marque une lecture paginée après laquelle la fin du
// transfert n'est pas établie : ouverture refusée par un contrôle propre à
// l'exporter alors qu'un transfert a pu être ouvert, parcours 0xc8 interrompu
// avant son dernier morceau, ou clôture dont la réponse ne confirme pas la
// fermeture (marque autre que 0xc6 et 0xc5, autre identifiant, en-tête
// invalide), ou commande refusée avant son envoi dans un transfert ouvert.
// L'outil de référence ne fait pas ces contrôles et va au bout du
// transfert ; rien n'établit qu'une commande suivante soit sans effet sur un
// transfert resté ouvert. Comme un échec d'échange, il arrête la
// carte pour le cycle : aucune commande ne suit (CDC, section 6.11, règle 2).
var ErrTransferUnsettled = errors.New("paged transfer end not established")

// PagedResponse est l'en-tête de réponse d'une étape de transfert.
// CodeOK : mot 4 de la réponse d'ouverture d'un transfert (CT_OK, Linux
// aacraid.h).
const CodeOK = markDA

type PagedResponse struct {
	Mark, Total, Xfer, Chunk uint32
	// Code : mot 4 de la réponse ; 0xda (CT_OK) à l'ouverture.
	Code uint32
	// Size : mot 7 de la réponse, égal au total sur les réponses
	// d'ouverture et de morceau capturées pour les lectures de l'exporter.
	// L'outil de référence reprend les mots 2, 3, 4 et 7 de la réponse
	// précédente dans la requête suivante : le transport exige ici les
	// valeurs que ses requêtes portent.
	Size uint32
	// SenderSize : champ SenderSize de l'en-tête de la réponse ; l'outil
	// en tire le pas du parcours 0xc8 (InlineStep).
	SenderSize uint32
	Data       []byte // données du morceau, ou portées par l'ouverture (parcours 0xc8)
}

// DecodePagedResponse vérifie la réponse à une requête paginée req et
// renvoie son en-tête. L'écho de la sous-commande et le statut sont exigés.
func DecodePagedResponse(req, rsp []byte) (PagedResponse, error) {
	if len(rsp) != FIBSize {
		return PagedResponse{}, fmt.Errorf("paged response: %d bytes", len(rsp))
	}
	le := binary.LittleEndian
	d := rsp[FIBHeaderSize:]
	q := req[FIBHeaderSize:]
	// Statut et écho d'abord, comme l'outil : leur échec est un échec
	// d'échange, qu'un autre champ de l'en-tête ne doit pas masquer.
	if st := le.Uint32(d[0:]); st != 0 {
		return PagedResponse{}, fmt.Errorf("%w: paged response: status %d", ErrExchange, st)
	}
	if le.Uint32(d[4:]) != le.Uint32(q[4:]) {
		return PagedResponse{}, fmt.Errorf("%w: paged response: subcommand %d, expected %d", ErrExchange, le.Uint32(d[4:]), le.Uint32(q[4:]))
	}
	if cmd := le.Uint16(rsp[fibCommand:]); cmd != cmdContainerCommand {
		return PagedResponse{}, fmt.Errorf("paged response: command %d", cmd)
	}
	// Taille annoncée par la carte : 496 octets, en-tête compris, pour toute
	// réponse paginée (ouverture, morceau, clôture ; 732 ouvertures et
	// clôtures et 325 morceaux capturés). Une réponse plus courte laisserait
	// dans le tampon des octets de la requête (identifiant du transfert)
	// pris pour une réponse de la carte.
	if sz := le.Uint16(rsp[fibSize:]); sz != ChunkRspSize {
		return PagedResponse{}, fmt.Errorf("paged response: size field %d, expected %d", sz, ChunkRspSize)
	}
	r := PagedResponse{Mark: le.Uint32(d[8:]), Total: le.Uint32(d[12:]), Code: le.Uint32(d[16:]), Xfer: le.Uint32(d[20:]), Chunk: le.Uint32(d[24:]), Size: le.Uint32(d[28:]),
		SenderSize: uint32(le.Uint16(rsp[fibSenderSize:]))}
	if le.Uint32(q[4:]) != ctStopData {
		// Données du morceau, ou portées par l'ouverture (parcours 0xc8) :
		// jusqu'à la fin annoncée par Size, jamais au-delà (octets non écrits
		// par la carte).
		r.Data = d[32 : 32+ChunkData]
	}
	return r, nil
}
