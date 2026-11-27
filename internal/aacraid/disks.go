// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

// Information de disque (sous-commande 248, paramètre : numéro de disque ;
// docs/aacraid-interface.md, section 5). Les champs texte ont une largeur
// fixe, complétés par des espaces, sans séparateur. Chaque champ a été
// vérifié sur les huit disques de l'hôte de référence contre l'outil témoin.
const (
	diskNumber    = 8
	diskLinkSpeed = 32  // code de débit du lien (ARCCONF fsaNegotiatedSpeedInMbps)
	diskBlocks    = 40  // capacité brute, en blocs
	diskBlockSize = 64  // taille de bloc en octets
	diskVendor    = 88  // 8 octets
	diskModel     = 96  // 16 octets
	diskFirmware  = 112 // 4 octets
	diskSerial    = 116 // 20 octets au plus, terminé par un octet nul
	diskPhy       = 200 // phy de la carte auquel le disque est relié
	diskWWN       = 288 // 8 octets, ordre de l'identifiant
	// Octets d'état lus par l'outil de référence (docs/aacraid-interface.md,
	// section 6 bis) : seul leur effet sur l'état affiché est établi.
	diskFlags0    = 0
	diskStatus68  = 68
	diskStatus69  = 69
	diskStatus70  = 70
	diskQualifier = 80
	diskStatus81  = 81
	diskStatus332 = 332
)

// DiskInventoryNeed est le nombre d'octets exigés (inventaire) ; il couvre
// les octets d'état 0 à 81. Les octets 332-335 sont décodés quand la
// réponse les contient (HasStatus332), ce qui est le cas dès le premier
// morceau de 432 octets.
const (
	DiskInventoryNeed = diskWWN + 8
	DiskStatusNeed    = diskStatus332 + 4
)

// DiskInfo regroupe les champs d'inventaire d'un disque.
type DiskInfo struct {
	Number                          uint32
	Vendor, Model, Firmware, Serial string
	WWN                             string // « 0x » et 16 chiffres hexadécimaux, vide si nul
	Blocks                          uint64
	BlockSize                       uint32
	Phy                             uint8 // phy de la carte (contrôle de la carte des connecteurs)
	// LinkSpeedCode : code de débit du lien, uint32 aux octets 32-35, que
	// l'outil de référence publie tel quel (fsaNegotiatedSpeedInMbps) et
	// classe en 1,5 / 3 / 6 / 12 Gb/s (docs/aacraid-interface.md, section 5).
	LinkSpeedCode uint32
	// Octets bruts de la section 6 bis de docs/aacraid-interface.md ;
	// Status332 seulement si la réponse le contient (HasStatus332).
	Flags0, Status68, Status69, Status70, Qualifier, Status81 uint8
	HasStatus332                                              bool
	Status332                                                 uint32
}

// DecodeDiskInfo décode l'information de disque.
func DecodeDiskInfo(d []byte) (DiskInfo, error) {
	if len(d) < DiskInventoryNeed {
		return DiskInfo{}, fmt.Errorf("disk info: %d bytes", len(d))
	}
	le := binary.LittleEndian
	i := DiskInfo{
		Number:    le.Uint32(d[diskNumber:]),
		Vendor:    field(d[diskVendor : diskVendor+8]),
		Model:     field(d[diskModel : diskModel+16]),
		Firmware:  field(d[diskFirmware : diskFirmware+4]),
		Serial:    field(d[diskSerial : diskSerial+20]),
		Blocks:    le.Uint64(d[diskBlocks:]),
		BlockSize: le.Uint32(d[diskBlockSize:]),
		Phy:       d[diskPhy],

		LinkSpeedCode: le.Uint32(d[diskLinkSpeed:]),

		Flags0:    d[diskFlags0],
		Status68:  d[diskStatus68],
		Status69:  d[diskStatus69],
		Status70:  d[diskStatus70],
		Qualifier: d[diskQualifier],
		Status81:  d[diskStatus81],
	}
	if len(d) >= DiskStatusNeed {
		i.HasStatus332, i.Status332 = true, le.Uint32(d[diskStatus332:])
	}
	if w := d[diskWWN : diskWWN+8]; !bytes.Equal(w, make([]byte, 8)) {
		i.WWN = "0x" + hex.EncodeToString(w)
	}
	return i, nil
}

// Failed indique un disque que son information déclare en panne (bit 0x1
// de l'octet 68, docs/aacraid-interface.md, section 6 bis), présent dans
// toute réponse décodée.
func (i DiskInfo) Failed() bool { return i.Status68&0x1 != 0 }

// HardDrive indique un périphérique que l'outil de référence traite comme un
// disque : type 0 (5 bits bas de l'octet 80) et bit 0x80 de l'octet 81 nul
// (docs/aacraid-interface.md, section 6 bis) ; autrement, autre
// périphérique, sans état de disque.
func (i DiskInfo) HardDrive() bool { return i.Qualifier&0x1f == 0 && i.Status81&0x80 == 0 }

// LinkSpeedBps renvoie le débit du lien en bits par seconde pour les seuls
// codes nominaux établis (150, 300, 600 et 1200 : 1,5, 3, 6 et 12 Gb/s,
// que l'outil de référence affiche « Transfer Speed ») ; 0 pour un code nul
// ou autre, que l'outil classerait dans une tranche sans qu'il désigne un
// débit établi. Le caractère négocié (et non nominal) de la valeur n'est pas
// observé.
func (i DiskInfo) LinkSpeedBps() uint64 {
	switch i.LinkSpeedCode {
	case 150, 300, 600, 1200:
		return uint64(i.LinkSpeedCode) * 10_000_000
	}
	return 0
}

// CapacityBytes renvoie la capacité brute en octets, 0 si la taille de bloc
// n'est pas une valeur connue (512 ou 4 096 octets).
func (i DiskInfo) CapacityBytes() uint64 {
	if i.BlockSize != 512 && i.BlockSize != 4096 {
		return 0
	}
	return i.Blocks * uint64(i.BlockSize)
}

// field extrait un champ texte : jusqu'au premier octet nul, sans les
// espaces de remplissage, rendu affichable.
func field(b []byte) string {
	if n := bytes.IndexByte(b, 0); n >= 0 {
		b = b[:n]
	}
	return strings.TrimSpace(printable(b))
}
