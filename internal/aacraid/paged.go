// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"encoding/binary"
	"fmt"
)

// Transfert paginé VM_ContainerConfig (docs/aacraid-interface.md, section 4).
// Formes des requêtes identiques à celles de l'outil de référence : en-tête
// XferState = HostOwned | ApiFib, Size = 480, SenderSize = 512 ; données :
//
//	ouverture : [2, sous-commande, paramètre, 0, marque, 0xffffffff, 0, taille]
//	morceau   : [2, 83, 0xc9 (premier) | 0xc8, total, 0xda, transfert, n, total]
//	clôture   : [2, 84, 0xc8, total, 0xda, transfert, dernier n, total]
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
)

// PagedRead décrit une lecture paginée qualifiée.
type PagedRead uint32

// Sous-commandes de lecture qualifiées, avec la marque (mot 4) et la taille
// (mot 7) de leur ouverture.
const (
	ReadContainerList PagedRead = 229 // liste des conteneurs, paramètre 0
	ReadContainerInfo PagedRead = 234 // information de conteneur, paramètre : numéro
	ReadDiskList      PagedRead = 247 // liste des disques présents, paramètre 0
	ReadDiskInfo      PagedRead = 248 // information de disque, paramètre : numéro
	ReadMemberList    PagedRead = 249 // liste des membres, paramètre : UID
)

type pagedForm struct {
	mark  uint32
	size  uint32
	param func(uint32) bool
}

var pagedForms = map[PagedRead]pagedForm{
	ReadContainerList: {markDA, 0x408, func(p uint32) bool { return p == 0 }},
	ReadContainerInfo: {markDA, 0x1000, func(p uint32) bool { return p <= 0xffff }},
	ReadDiskList:      {0, 0x1004, func(p uint32) bool { return p == 0 }},
	ReadDiskInfo:      {0, 0x1000, func(p uint32) bool { return p <= 0xffff }},
	ReadMemberList:    {0, 0, func(p uint32) bool { return p != 0 && p != 0xffffffff }},
}

// MaxMembers borne le nombre de membres d'un conteneur accepté dans une
// requête de liste de membres.
const MaxMembers = 64

// MemberListSize est la taille demandée pour la liste de n membres, comme le
// fait l'outil de référence : en-tête de 8 octets et 32 octets par membre.
func MemberListSize(n int) uint32 { return uint32(8 + 32*n) }

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
// utilisé que pour la liste des membres (MemberListSize).
func OpenRequest(r PagedRead, param uint32, size uint32) ([]byte, error) {
	f, ok := pagedForms[r]
	if !ok || !f.param(param) {
		return nil, fmt.Errorf("read %d, parameter %#x: not qualified", r, param)
	}
	if f.size != 0 {
		size = f.size
	} else if size < MemberListSize(1) || size > MemberListSize(MaxMembers) || (size-8)%32 != 0 {
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
	case ctContinueData, ctStopData:
		total, n := w(3), w(6)
		if total == 0 || total > 0x1004 || w(7) != total || w(4) != markDA || n >= chunks(total) {
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
		if f.size != 0 {
			return w(7) == f.size
		}
		size := w(7)
		return size >= MemberListSize(1) && size <= MemberListSize(MaxMembers) && (size-8)%32 == 0
	}
}

// PagedResponse est l'en-tête de réponse d'une étape de transfert.
// CodeOK : mot 4 de la réponse d'ouverture d'un transfert (CT_OK, Linux
// aacraid.h).
const CodeOK = markDA

type PagedResponse struct {
	Mark, Total, Xfer, Chunk uint32
	// Code : mot 4 de la réponse ; 0xda (CT_OK) à l'ouverture.
	Code uint32
	Data []byte // données du morceau (CT_CONTINUE_DATA)
}

// DecodePagedResponse vérifie la réponse à une requête paginée req et
// renvoie son en-tête. L'écho de la sous-commande et le statut sont exigés.
func DecodePagedResponse(req, rsp []byte) (PagedResponse, error) {
	if len(rsp) != FIBSize {
		return PagedResponse{}, fmt.Errorf("paged response: %d bytes", len(rsp))
	}
	le := binary.LittleEndian
	if cmd := le.Uint16(rsp[fibCommand:]); cmd != cmdContainerCommand {
		return PagedResponse{}, fmt.Errorf("paged response: command %d", cmd)
	}
	d := rsp[FIBHeaderSize:]
	q := req[FIBHeaderSize:]
	if st := le.Uint32(d[0:]); st != 0 {
		return PagedResponse{}, fmt.Errorf("paged response: status %d", st)
	}
	if le.Uint32(d[4:]) != le.Uint32(q[4:]) {
		return PagedResponse{}, fmt.Errorf("paged response: subcommand %d, expected %d", le.Uint32(d[4:]), le.Uint32(q[4:]))
	}
	r := PagedResponse{Mark: le.Uint32(d[8:]), Total: le.Uint32(d[12:]), Code: le.Uint32(d[16:]), Xfer: le.Uint32(d[20:]), Chunk: le.Uint32(d[24:])}
	if le.Uint32(q[4:]) == ctContinueData {
		if sz := le.Uint16(rsp[fibSize:]); sz != ChunkRspSize {
			return PagedResponse{}, fmt.Errorf("paged response: chunk size field %d, expected %d", sz, ChunkRspSize)
		}
		// Données du morceau : jusqu'à la fin annoncée par Size, jamais
		// au-delà (octets non écrits par la carte).
		r.Data = d[32 : 32+ChunkData]
	}
	return r, nil
}
