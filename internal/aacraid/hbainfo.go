// Package aacraid décode les réponses de l'interface de gestion du pilote
// Linux aacraid. Il ne réalise aucun appel système.
package aacraid

import (
	"encoding/binary"
	"fmt"
)

// IoctlGetHBAInfo est FSACTL_GET_HBA_INFO = CTL_CODE(2150, METHOD_BUFFERED)
// (drivers/scsi/aacraid/aacraid.h). Le pilote remplit la réponse à partir de
// ses propres données (aac_get_hba_info, commctrl.c) : aucune commande
// n'est transmise au firmware.
const IoctlGetHBAInfo uint32 = 4<<16 | 2150<<2

// HBAInfoSize est sizeof(struct aac_hba_info) sur x86-64. Le noyau copie
// exactement ce nombre d'octets : le tampon fourni doit avoir cette taille.
const HBAInfoSize = 200

// Décalages vérifiés avec offsetof() sur la définition de aacraid.h.
const (
	offAdapterNumber = 50
	offBus           = 51
	offDevice        = 52
	offFunction      = 56
	offVendorID      = 60
	offDeviceID      = 64
	offSubVendorID   = 68
	offSubSystemID   = 72
)

// HBAInfo regroupe les champs de struct aac_hba_info utiles au rattachement
// d'un nœud de gestion à sa carte. Le domaine PCI n'y figure pas.
type HBAInfo struct {
	AdapterNumber uint8 // = aac->id, donc le minor du nœud
	Bus           uint8
	Device        uint8
	Function      uint32
	VendorID      uint32
	DeviceID      uint32
	SubVendorID   uint32
	SubSystemID   uint32
}

// DecodeHBAInfo décode une réponse de FSACTL_GET_HBA_INFO.
// Une réponse dont la taille diffère de HBAInfoSize est refusée.
func DecodeHBAInfo(b []byte) (HBAInfo, error) {
	if len(b) != HBAInfoSize {
		return HBAInfo{}, fmt.Errorf("aac_hba_info: %d bytes received, %d expected", len(b), HBAInfoSize)
	}
	le := binary.LittleEndian
	return HBAInfo{
		AdapterNumber: b[offAdapterNumber],
		Bus:           b[offBus],
		Device:        b[offDevice],
		Function:      le.Uint32(b[offFunction:]),
		VendorID:      le.Uint32(b[offVendorID:]),
		DeviceID:      le.Uint32(b[offDeviceID:]),
		SubVendorID:   le.Uint32(b[offSubVendorID:]),
		SubSystemID:   le.Uint32(b[offSubSystemID:]),
	}, nil
}

// MatchesPCI indique si la réponse correspond à l'adresse PCI attendue
// (bus, slot, fonction) et au numéro de carte attendu (unique_id).
func (h HBAInfo) MatchesPCI(uniqueID uint8, bus, slot, function uint8) bool {
	return h.AdapterNumber == uniqueID && h.Bus == bus && h.Device == slot && h.Function == uint32(function)
}
