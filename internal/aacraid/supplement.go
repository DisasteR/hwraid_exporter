// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// IoctlSendFIB est FSACTL_SENDFIB = CTL_CODE(2050, METHOD_BUFFERED)
// (drivers/scsi/aacraid/aacraid.h).
const IoctlSendFIB uint32 = 4<<16 | 2050<<2

// FIBSize est la taille du tampon passé à FSACTL_SENDFIB. Le pilote copie
// max(Size + 32, SenderSize) octets à l'aller et au retour (ioctl_send_fib,
// commctrl.c) : avec SenderSize = 512 et Size = 4, exactement 512.
const FIBSize = 512

// FIBHeaderSize est sizeof(struct aac_fibhdr).
const FIBHeaderSize = 32

// CmdRequestSupplementAdapterInfo est la commande FIB 706
// (Linux aacraid.h, FreeBSD aacraid_reg.h).
const CmdRequestSupplementAdapterInfo = 706

// En-tête FIB (struct aac_fibhdr).
const (
	fibXferState  = 0
	fibCommand    = 4
	fibStructType = 6
	fibSize       = 8
	fibSenderSize = 10
)

// Valeurs de la requête, identiques à celles de l'outil de référence
// (docs/aacraid-interface.md, section 8) :
//   - XferState = HostOwned (1) : aac_fib_send refuse une FIB sans ce bit ;
//   - StructType = FIB_MAGIC (1) ;
//   - Size = 4 : un mot de données, nul ;
//   - SenderSize = 512.
//
// Les octets 12 à 31 (adresses, poignée, chaînage) sont à zéro : le pilote
// réécrit SenderFibAddress, Handle et, selon l'interface, le mot 16-19
// (aac_fib_send, aac_src_deliver_message) ; ses propres FIB ont ces champs à
// zéro (aac_fib_init). Dans les captures, ces octets contiennent des
// adresses propres à l'outil, différentes d'un appel à l'autre, pour une
// réponse identique.
const (
	reqXferState  = 1
	reqStructType = 1
	reqDataSize   = 4
)

// SupplementInfoRequest construit la requête RequestSupplementAdapterInfo.
func SupplementInfoRequest() []byte {
	b := make([]byte, FIBSize)
	le := binary.LittleEndian
	le.PutUint32(b[fibXferState:], reqXferState)
	le.PutUint16(b[fibCommand:], CmdRequestSupplementAdapterInfo)
	b[fibStructType] = reqStructType
	le.PutUint16(b[fibSize:], reqDataSize)
	le.PutUint16(b[fibSenderSize:], FIBSize)
	return b
}

// qualifiedRequests liste les seules FIB que l'exporter peut envoyer.
func qualifiedRequests() [][]byte {
	return [][]byte{
		SupplementInfoRequest(),
		AdapterInfoRequest(),
		EnvironmentRequest(),
		CacheProtectionPresenceRequest(),
		CacheProtectionStateRequest(),
	}
}

// ErrFIBNotAllowed : la FIB n'est pas une requête qualifiée.
var ErrFIBNotAllowed = errors.New("unqualified FIB")

// CheckFIBRequest n'accepte qu'une requête identique, octet pour octet, à
// une requête qualifiée. Toute autre commande, sous-commande ou valeur est
// refusée.
func CheckFIBRequest(b []byte) error {
	for _, ok := range qualifiedRequests() {
		if bytes.Equal(b, ok) {
			return nil
		}
	}
	if checkPagedRequest(b) {
		return nil
	}
	cmd := -1
	if len(b) >= FIBHeaderSize {
		cmd = int(binary.LittleEndian.Uint16(b[fibCommand:]))
	}
	return fmt.Errorf("%w (command %d, %d bytes)", ErrFIBNotAllowed, cmd, len(b))
}

// Champs publics de struct aac_supplement_adapter_info (Linux aacraid.h,
// FreeBSD aacraid_reg.h), relatifs au début des données de la FIB.
// Décalages vérifiés avec offsetof() sur la définition de aacraid.h.
const (
	supVersion          = 0x20
	supFeatureBits      = 0x24
	supSupportedOptions = 0x90
	supMinData          = supSupportedOptions + 4
)

// SupportedArcIo est le bit 0x08 de SupportedOptions2
// (AAC_SUPPORTED_ARCIO_PHYDEV, FreeBSD aacraid_reg.h).
const SupportedArcIo = 0x08

// Bits de capacité qui changent l'état affiché d'un disque
// (docs/aacraid-interface.md, section 6 bis) : bit 16 de SupportedOptions2
// et AAC_FEATURE_JBOD (bit 27 de FeatureBits, Linux aacraid.h).
const (
	SupportedDirectDisks = 0x10000
	FeatureJBOD          = 0x08000000
)

// Bits de FeatureBits qui conditionnent les lectures (CDC, section 6.11) :
// bit 8, carte des connecteurs (708) ; bit 9, listes et informations des
// conteneurs et listes des membres (229, 234, 249) ; bit 13, forme
// d'ouverture à bit de poids fort (forme 2), non encore implémentée. Carte
// de référence : bits 8 et 9 présents, bit 13 absent (42 réponses 706
// capturées).
const (
	FeatureConnectorMap   = 0x100
	FeatureContainerReads = 0x200
	FeaturePagedHighBit   = 0x2000
)

// SupplementInfo regroupe les champs utilisés de la réponse à
// RequestSupplementAdapterInfo.
type SupplementInfo struct {
	Version           uint32
	SupportedOptions2 uint32
	FeatureBits       uint32
}

// capability : comme SupportedOptions2, FeatureBits n'est valide que si
// Version est supérieur à 1.
func (s SupplementInfo) capability(field, bit uint32) bool { return s.Version > 1 && field&bit != 0 }

// DirectDisks indique le bit 16 de SupportedOptions2.
func (s SupplementInfo) DirectDisks() bool {
	return s.capability(s.SupportedOptions2, SupportedDirectDisks)
}

// JBOD indique AAC_FEATURE_JBOD.
func (s SupplementInfo) JBOD() bool { return s.capability(s.FeatureBits, FeatureJBOD) }

// ContainerReads indique le bit 9 de FeatureBits : sans lui, l'outil de
// référence ne lit ni les conteneurs ni leurs membres.
func (s SupplementInfo) ContainerReads() bool {
	return s.capability(s.FeatureBits, FeatureContainerReads)
}

// ConnectorMapRead indique le bit 8 de FeatureBits : sans lui, l'outil de
// référence ne lit pas la carte des connecteurs.
func (s SupplementInfo) ConnectorMapRead() bool {
	return s.capability(s.FeatureBits, FeatureConnectorMap)
}

// PagedHighBit indique le bit 13 de FeatureBits (forme 2 des transferts).
func (s SupplementInfo) PagedHighBit() bool { return s.capability(s.FeatureBits, FeaturePagedHighBit) }

// ArcIoReads indique si la carte annonce l'interface des lectures de volume,
// de membres et de disque décrites dans docs/aacraid-interface.md :
// SupportedOptions2 n'est valide que si Version est supérieur à 1.
func (s SupplementInfo) ArcIoReads() bool {
	return s.Version > 1 && s.SupportedOptions2&SupportedArcIo != 0
}

// DecodeSupplementInfo décode la réponse à RequestSupplementAdapterInfo.
// Le champ Size de la réponse inclut l'en-tête (commentaire de
// ioctl_send_fib) ; il doit couvrir les champs lus et tenir dans le tampon.
func DecodeSupplementInfo(b []byte) (SupplementInfo, error) {
	if len(b) != FIBSize {
		return SupplementInfo{}, fmt.Errorf("706 response: %d bytes received, %d expected", len(b), FIBSize)
	}
	le := binary.LittleEndian
	if cmd := le.Uint16(b[fibCommand:]); cmd != CmdRequestSupplementAdapterInfo {
		return SupplementInfo{}, fmt.Errorf("706 response: command %d in header", cmd)
	}
	size := int(le.Uint16(b[fibSize:]))
	if size < FIBHeaderSize+supMinData || size > FIBSize {
		return SupplementInfo{}, fmt.Errorf("706 response: size %d outside [%d, %d]", size, FIBHeaderSize+supMinData, FIBSize)
	}
	d := b[FIBHeaderSize:]
	return SupplementInfo{
		Version:           le.Uint32(d[supVersion:]),
		SupportedOptions2: le.Uint32(d[supSupportedOptions:]),
		FeatureBits:       le.Uint32(d[supFeatureBits:]),
	}, nil
}
