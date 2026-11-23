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
	diskBlocks    = 40  // capacité brute, en blocs
	diskBlockSize = 64  // taille de bloc en octets
	diskVendor    = 88  // 8 octets
	diskModel     = 96  // 16 octets
	diskFirmware  = 112 // 4 octets
	diskSerial    = 116 // 20 octets au plus, terminé par un octet nul
	diskPhy       = 200 // phy de la carte auquel le disque est relié
	diskWWN       = 288 // 8 octets, ordre de l'identifiant
)

// DiskInfoNeed est le nombre d'octets nécessaires au décodage.
const DiskInfoNeed = diskWWN + 8

// DiskInfo regroupe les champs d'inventaire d'un disque.
type DiskInfo struct {
	Number                          uint32
	Vendor, Model, Firmware, Serial string
	WWN                             string // « 0x » et 16 chiffres hexadécimaux, vide si nul
	Blocks                          uint64
	BlockSize                       uint32
	Phy                             uint8 // phy de la carte (contrôle de la carte des connecteurs)
}

// DecodeDiskInfo décode l'information de disque.
func DecodeDiskInfo(d []byte) (DiskInfo, error) {
	if len(d) < DiskInfoNeed {
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
	}
	if w := d[diskWWN : diskWWN+8]; !bytes.Equal(w, make([]byte, 8)) {
		i.WWN = "0x" + hex.EncodeToString(w)
	}
	return i, nil
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
