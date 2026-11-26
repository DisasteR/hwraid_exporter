// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"encoding/binary"
	"fmt"
)

// Commandes FIB publiques de l'ouverture d'une carte par l'outil de
// référence (Linux aacraid.h, FreeBSD aacraid_reg.h), envoyées avant la 703
// et la 706 (docs/aacraid-interface.md, section 8) : IsAdapterPaused, puis
// CheckRevision.
const (
	CmdCheckRevision   = 701
	CmdIsAdapterPaused = 704
)

// Les deux requêtes de l'outil portent le bit 0x100000 de XferState (ApiFib),
// comme AdapterEnvironmentInfo.
const openXferState = 0x100001

// PausedRequest construit la requête IsAdapterPaused : un mot de données nul
// (requête identique aux captures de l'outil, octets d'adresse mis à part).
func PausedRequest() []byte {
	b := make([]byte, FIBSize)
	le := binary.LittleEndian
	le.PutUint32(b[fibXferState:], openXferState)
	le.PutUint16(b[fibCommand:], CmdIsAdapterPaused)
	b[fibStructType] = reqStructType
	le.PutUint16(b[fibSize:], reqDataSize)
	le.PutUint16(b[fibSenderSize:], FIBSize)
	return b
}

// Requête CheckRevision de l'outil de référence (version 2.05) : trois mots
// de données, Size = 12 ; le premier vaut 4, les deux suivants portent la
// révision et le numéro de build de l'outil. L'outil n'initialise pas les
// octets au-delà de Size (contenu de sa pile, sans signification pour la
// carte) : ils sont à zéro.
const (
	revSize      = 12
	revWord0     = 4
	revVersion   = 0x07040400
	revToolBuild = 0x510a
)

// RevisionRequest construit la requête CheckRevision.
func RevisionRequest() []byte {
	b := make([]byte, FIBSize)
	le := binary.LittleEndian
	le.PutUint32(b[fibXferState:], openXferState)
	le.PutUint16(b[fibCommand:], CmdCheckRevision)
	b[fibStructType] = reqStructType
	le.PutUint16(b[fibSize:], revSize)
	le.PutUint16(b[fibSenderSize:], FIBSize)
	le.PutUint32(b[FIBHeaderSize:], revWord0)
	le.PutUint32(b[FIBHeaderSize+4:], revVersion)
	le.PutUint32(b[FIBHeaderSize+8:], revToolBuild)
	return b
}

// DecodePaused décode la réponse à IsAdapterPaused : la carte est en pause si
// le premier octet des données est non nul. L'outil de référence n'ouvre pas
// une carte en pause.
func DecodePaused(b []byte) (paused bool, err error) {
	d, err := fibResponse(b, CmdIsAdapterPaused, 1)
	if err != nil {
		return false, err
	}
	return d[0] != 0, nil
}

// MinFirmwareBuild est la plus petite révision de firmware que l'outil de
// référence accepte d'ouvrir pour ces cartes (versions 2.05 et 3.07).
const MinFirmwareBuild = 2120

// Revision est la réponse à CheckRevision : la carte accepte l'outil
// (premier mot non nul) et annonce le build de son firmware (troisième mot).
type Revision struct {
	Compatible    bool
	FirmwareBuild uint32
}

// Accepted indique si l'outil de référence ouvrirait la carte : réponse
// compatible et build du firmware au moins égal à MinFirmwareBuild.
func (r Revision) Accepted() bool { return r.Compatible && r.FirmwareBuild >= MinFirmwareBuild }

// DecodeRevision décode la réponse à CheckRevision.
func DecodeRevision(b []byte) (Revision, error) {
	d, err := fibResponse(b, CmdCheckRevision, revSize)
	if err != nil {
		return Revision{}, err
	}
	le := binary.LittleEndian
	return Revision{Compatible: le.Uint32(d) != 0, FirmwareBuild: le.Uint32(d[8:])}, nil
}

// ErrAdapterPaused et ErrRevisionRefused : l'outil de référence n'ouvrirait
// pas la carte ; aucune autre commande n'est envoyée.
var (
	ErrAdapterPaused   = fmt.Errorf("adapter paused (IsAdapterPaused)")
	ErrRevisionRefused = fmt.Errorf("revision refused (CheckRevision)")
)
