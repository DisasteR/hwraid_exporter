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

// PCIIdentity est l'identité attendue d'une carte, relevée dans sysfs.
type PCIIdentity struct {
	Bus, Slot, Function                  uint8
	Vendor, Device, SubVendor, SubDevice uint32
}

// Matches indique si la réponse désigne la carte attendue : numéro de carte
// (unique_id, minor du nœud), adresse PCI hors domaine (absent de la
// réponse) et les quatre identifiants PCI.
func (h HBAInfo) Matches(uniqueID uint8, id PCIIdentity) bool {
	return h.AdapterNumber == uniqueID && h.Bus == id.Bus && h.Device == id.Slot && h.Function == uint32(id.Function) &&
		h.VendorID == id.Vendor && h.DeviceID == id.Device && h.SubVendorID == id.SubVendor && h.SubSystemID == id.SubDevice
}
