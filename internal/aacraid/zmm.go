// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"encoding/binary"
	"fmt"
)

// Lecture de l'état du module de protection du cache (ZMM, kit AFM-600 sur
// les Series 6). Deux requêtes ContainerCommand portant une struct
// aac_vmioctl (FreeBSD aacraid_reg.h) : Command = VM_Ioctl (3),
// ObjType = FT_DRIVE (9), MethId = 3, ObjId = 0, puis IoctlCmd.
// Formes identiques, octet pour octet dans leur partie significative, aux
// requêtes de l'outil de référence (docs/aacraid-interface.md, section 6).
// La même enveloppe porte aussi des actions (ResetBus = 0x16) : seules ces
// deux valeurs d'IoctlCmd sont qualifiées.
const (
	cmdContainerCommand = 500
	vmIoctl             = 3
	ftDrive             = 9
	vmIoctlMethID       = 3

	// IoctlCmd de la lecture de présence et de la lecture d'état.
	ioctlCacheProtectionPresence = 0x30000
	ioctlCacheProtectionState    = 0x700000

	// Tailles des données de requête (champ Size, hors en-tête).
	presenceDataSize = 44
	stateDataSize    = 76

	// XferState des requêtes de l'outil de référence : HostOwned (bit 0)
	// et ApiFib (bit 20, « It's an API Fib », Linux aacraid.h).
	xferHostOwnedAPIFib = 1 | 1<<20
)

func vmIoctlRequest(dataSize int, ioctlCmd uint32) []byte {
	b := make([]byte, FIBSize)
	le := binary.LittleEndian
	le.PutUint32(b[fibXferState:], xferHostOwnedAPIFib)
	le.PutUint16(b[fibCommand:], cmdContainerCommand)
	b[fibStructType] = reqStructType
	le.PutUint16(b[fibSize:], uint16(dataSize))
	le.PutUint16(b[fibSenderSize:], FIBSize)
	d := b[FIBHeaderSize:]
	le.PutUint32(d[0:], vmIoctl)
	le.PutUint32(d[4:], ftDrive)
	le.PutUint32(d[8:], vmIoctlMethID)
	le.PutUint32(d[16:], ioctlCmd)
	return b
}

// CacheProtectionPresenceRequest construit la lecture de présence.
func CacheProtectionPresenceRequest() []byte {
	return vmIoctlRequest(presenceDataSize, ioctlCacheProtectionPresence)
}

// CacheProtectionStateRequest construit la lecture d'état.
func CacheProtectionStateRequest() []byte {
	return vmIoctlRequest(stateDataSize, ioctlCacheProtectionState)
}

// vmIoctlResponse vérifie l'en-tête et l'écho d'une réponse VM_Ioctl et
// renvoie ses données. La réponse porte dans Size la taille totale, en-tête
// compris ; elle doit couvrir minData octets de données.
func vmIoctlResponse(b []byte, ioctlCmd uint32, minData int) ([]byte, error) {
	if len(b) != FIBSize {
		return nil, fmt.Errorf("VM_Ioctl response: %d bytes received, %d expected", len(b), FIBSize)
	}
	le := binary.LittleEndian
	if cmd := le.Uint16(b[fibCommand:]); cmd != cmdContainerCommand {
		return nil, fmt.Errorf("VM_Ioctl response: command %d in header", cmd)
	}
	size := int(le.Uint16(b[fibSize:]))
	if size < FIBHeaderSize+minData || size > FIBSize {
		return nil, fmt.Errorf("VM_Ioctl response: size %d outside [%d, %d]", size, FIBHeaderSize+minData, FIBSize)
	}
	d := b[FIBHeaderSize:size]
	if st := le.Uint32(d[0:]); st != 0 {
		return nil, fmt.Errorf("VM_Ioctl response: status %d", st)
	}
	if le.Uint32(d[4:]) != ftDrive || le.Uint32(d[8:]) != vmIoctlMethID || le.Uint32(d[16:]) != ioctlCmd {
		return nil, fmt.Errorf("VM_Ioctl response: unexpected echo (%#x, %#x, %#x)", le.Uint32(d[4:]), le.Uint32(d[8:]), le.Uint32(d[16:]))
	}
	return d, nil
}

// Lecture de présence : mot de 16 bits à l'octet 42 des données.
const (
	presenceFlagsOff   = 42
	presenceStateBlock = 0x2000
)

// DecodeCacheProtectionPresence indique si la carte décrit son module dans
// la lecture d'état (bit 0x2000).
func DecodeCacheProtectionPresence(b []byte) (bool, error) {
	d, err := vmIoctlResponse(b, ioctlCacheProtectionPresence, presenceFlagsOff+2)
	if err != nil {
		return false, err
	}
	return binary.LittleEndian.Uint16(d[presenceFlagsOff:])&presenceStateBlock != 0, nil
}

// CacheProtectionState est l'état normalisé du module.
type CacheProtectionState string

const (
	CacheProtectionOptimal      CacheProtectionState = "optimal"
	CacheProtectionCharging     CacheProtectionState = "charging"
	CacheProtectionFailed       CacheProtectionState = "failed"
	CacheProtectionNotInstalled CacheProtectionState = "not_installed"
	CacheProtectionUnknown      CacheProtectionState = "unknown"
	// CacheProtectionOtherType : type de module autre que ZMM, non décodé.
	CacheProtectionOtherType CacheProtectionState = "other_type"
)

// Lecture d'état : bloc à l'octet 20 (bit 0 = bloc valide), mot d'état R à
// l'octet 48 des données.
const (
	stateBlockOff = 20
	stateWordOff  = 48
	stateTypeZMM  = 0x40000000
	stateTypeMask = 0xF0000000
	stateInstall  = 0x1
	stateCharging = 0x4
	stateReady    = 0x8
	// stateObserved0x80 : bit présent sur le module en panne de l'hôte de
	// référence (R = 0x40000081), sens non établi.
	stateObserved0x80 = 0x80
	stateKnownBits    = stateInstall | stateCharging | stateReady | stateObserved0x80
)

// ZMMStatus est le résultat décodé de la lecture d'état.
type ZMMStatus struct {
	Word  uint32 // mot d'état brut (journalisation et tests)
	State CacheProtectionState
}

// InstallKnown indique si le mot d'état établit la présence du module : bit
// d'installation positionné, ou mot sans aucun bit d'état (module absent).
func (s ZMMStatus) InstallKnown() bool {
	low := s.Word &^ stateTypeMask
	return low == 0 || low&stateInstall != 0
}

// DecodeCacheProtectionState décode la lecture d'état. Un bloc marqué non
// valide est une erreur : l'état n'est pas observé.
func DecodeCacheProtectionState(b []byte) (ZMMStatus, error) {
	d, err := vmIoctlResponse(b, ioctlCacheProtectionState, stateWordOff+4)
	if err != nil {
		return ZMMStatus{}, err
	}
	if d[stateBlockOff]&1 == 0 {
		return ZMMStatus{}, fmt.Errorf("state response: invalid block (%#x)", d[stateBlockOff])
	}
	r := binary.LittleEndian.Uint32(d[stateWordOff:])
	return ZMMStatus{Word: r, State: zmmState(r)}, nil
}

// zmmState applique la règle de docs/aacraid-interface.md (section 6).
//
// La règle n'annonce jamais à tort un module sain ou absent : optimal et
// charging exigent installé et prêt sans aucun bit non reconnu (0x80, non
// qualifié sur un module sain, compris) ; not_installed exige qu'aucun bit
// d'état ne soit positionné. Un module installé mais pas prêt est en panne,
// quels que soient les autres bits. Toute autre combinaison est unknown.
func zmmState(r uint32) CacheProtectionState {
	low := r &^ stateTypeMask
	switch {
	case r&stateTypeMask != stateTypeZMM:
		return CacheProtectionOtherType
	case low == 0:
		return CacheProtectionNotInstalled
	case low&stateInstall == 0:
		return CacheProtectionUnknown
	case low&stateReady == 0:
		return CacheProtectionFailed
	case low&^(stateInstall|stateCharging|stateReady) != 0:
		return CacheProtectionUnknown
	case low&stateCharging != 0:
		return CacheProtectionCharging
	default:
		return CacheProtectionOptimal
	}
}

// CacheProtectionMaintenance : batterie en cycle de maintenance
// (reconditionnement), terme de l'outil de référence ; pas un état sain.
const CacheProtectionMaintenance CacheProtectionState = "maintenance"

// Batterie classique (CDC, section 6.11, « Protection du cache par
// batterie ») : réponse à la lecture de présence sans le bit 0x2000. Le mot
// d'indicateurs (octets 42-43) dit quels octets d'état sont présents ;
// l'outil de référence n'en lit aucun sans son indicateur.
const (
	batteryPresentOff  = 20 // présence, indicateur 0x1
	batteryUsableOff   = 21 // batterie utilisable, indicateur 0x2
	batteryMaintOff    = 22 // mode maintenance, indicateur 0x4
	batteryFlagPresent = 0x1
	batteryFlagUsable  = 0x2
	batteryFlagMaint   = 0x4
)

// Battery est l'état décodé d'une batterie classique.
type Battery struct {
	// PresenceKnown : octet de présence lu et valant 0 ou 1 ; Present : 1.
	PresenceKnown, Present bool
	// State : vide si la batterie est absente (présence établie) ;
	// optimal, failed, maintenance, ou unknown (indicateur requis absent ou
	// valeur autre que 0 et 1).
	State CacheProtectionState
}

// batteryByte lit un octet d'état seulement si son indicateur est présent.
func batteryByte(d []byte, flags uint16, flag uint16, off int) (v byte, ok bool) {
	if flags&flag == 0 {
		return 0, false
	}
	return d[off], true
}

// DecodeBattery décode la batterie de la réponse à la lecture de présence
// (b), selon la table du CDC.
func DecodeBattery(b []byte) (Battery, error) {
	d, err := vmIoctlResponse(b, ioctlCacheProtectionPresence, presenceFlagsOff+2)
	if err != nil {
		return Battery{}, err
	}
	flags := binary.LittleEndian.Uint16(d[presenceFlagsOff:])
	var bat Battery
	if v, ok := batteryByte(d, flags, batteryFlagPresent, batteryPresentOff); ok && v <= 1 {
		bat.PresenceKnown, bat.Present = true, v == 1
		if v == 0 {
			return bat, nil
		}
	}
	bat.State = CacheProtectionUnknown
	if !bat.Present {
		return bat, nil
	}
	usable, ok := batteryByte(d, flags, batteryFlagUsable, batteryUsableOff)
	switch {
	case !ok || usable > 1:
	case usable == 0:
		bat.State = CacheProtectionFailed
	default:
		if m, ok := batteryByte(d, flags, batteryFlagMaint, batteryMaintOff); ok && m <= 1 {
			bat.State = CacheProtectionOptimal
			if m == 1 {
				bat.State = CacheProtectionMaintenance
			}
		}
	}
	return bat, nil
}

// CacheProtectionReading est le résultat des lectures de la protection du
// cache : module décrit par la lecture d'état (bit 0x2000, ZMM), ou
// batterie décodée de la seule lecture de présence.
type CacheProtectionReading struct {
	Flash   bool
	ZMM     ZMMStatus
	Battery Battery
}
