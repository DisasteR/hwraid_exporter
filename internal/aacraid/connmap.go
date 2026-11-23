package aacraid

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// IoctlSendLargeFIB est FSACTL_SEND_LARGE_FIB = CTL_CODE(2138,
// METHOD_BUFFERED) (drivers/scsi/aacraid/aacraid.h). Le pilote le traite
// comme FSACTL_SENDFIB (ioctl_send_fib, commctrl.c) : il copie
// max(Size + 32, SenderSize) octets à l'aller et au retour.
const IoctlSendLargeFIB uint32 = 4<<16 | 2138<<2

// LargeFIBSize est la taille du tampon passé à FSACTL_SEND_LARGE_FIB, égale
// au SenderSize de la requête de l'outil de référence.
const LargeFIBSize = 2048

// CmdRequestConnectorMap est la commande FIB 708 (carte des connecteurs,
// docs/aacraid-interface.md, section 5).
const CmdRequestConnectorMap = 708

// ConnectorMapRequest construit la requête, identique à celle de l'outil de
// référence (octets 0 à 11 de l'en-tête, données) ; octets 12 à 31 nuls
// comme pour les autres requêtes (supplement.go).
func ConnectorMapRequest() []byte {
	b := make([]byte, LargeFIBSize)
	le := binary.LittleEndian
	le.PutUint32(b[fibXferState:], xferHostOwnedAPIFib)
	le.PutUint16(b[fibCommand:], CmdRequestConnectorMap)
	b[fibStructType] = reqStructType
	le.PutUint16(b[fibSize:], reqDataSize)
	le.PutUint16(b[fibSenderSize:], LargeFIBSize)
	return b
}

// CheckLargeFIBRequest n'accepte que la requête de carte des connecteurs,
// octet pour octet.
func CheckLargeFIBRequest(b []byte) error {
	if bytes.Equal(b, ConnectorMapRequest()) {
		return nil
	}
	cmd := -1
	if len(b) >= FIBHeaderSize {
		cmd = int(binary.LittleEndian.Uint16(b[fibCommand:]))
	}
	return fmt.Errorf("%w (commande %d, %d octets, tampon large)", ErrFIBNotAllowed, cmd, len(b))
}

// Format de la réponse (données, après l'en-tête). Seule la forme observée
// est acceptée : deux connecteurs décrits sur 12 octets, puis les entrées de
// disque sur 16 octets à partir de l'octet 32.
const (
	cmapEntries        = 4
	cmapConnectors     = 5
	cmapConnectorBase  = 8
	cmapConnectorSize  = 12
	cmapEntryBase      = 32
	cmapEntrySize      = 16
	cmapMaxEntries     = 64
	cmapQualifiedConns = 2
)

// ConnectorEntry relie un disque, désigné comme dans la liste des disques
// (canal << 24 | cible), à sa position : connecteur, position dans le
// connecteur et phy de la carte. Valeurs fournies par le firmware, telles
// que l'outil de référence les affiche.
type ConnectorEntry struct {
	Disk      uint32
	Connector uint8
	Device    uint8
	Phy       uint8
}

// ConnectorMap est la carte des connecteurs d'une carte.
type ConnectorMap struct {
	Names   map[uint8]string // numéro de connecteur → nom donné par la carte
	Entries []ConnectorEntry
}

// Locate renvoie l'entrée du disque n, s'il est relié directement à la
// carte. Un disque derrière un expandeur ou un fond de panier n'a pas
// d'entrée : sa position n'est pas donnée par cette carte.
func (m ConnectorMap) Locate(n uint32) (ConnectorEntry, bool) {
	for _, e := range m.Entries {
		if e.Disk == n {
			return e, true
		}
	}
	return ConnectorEntry{}, false
}

// DecodeConnectorMap décode la réponse à RequestConnectorMap.
func DecodeConnectorMap(b []byte) (ConnectorMap, error) {
	if len(b) != LargeFIBSize {
		return ConnectorMap{}, fmt.Errorf("réponse 708 : %d octets reçus, %d attendus", len(b), LargeFIBSize)
	}
	le := binary.LittleEndian
	if cmd := le.Uint16(b[fibCommand:]); cmd != CmdRequestConnectorMap {
		return ConnectorMap{}, fmt.Errorf("réponse 708 : commande %d dans l'en-tête", cmd)
	}
	size := int(le.Uint16(b[fibSize:]))
	d := b[FIBHeaderSize:]
	if size < FIBHeaderSize+cmapEntryBase || size > LargeFIBSize {
		return ConnectorMap{}, fmt.Errorf("réponse 708 : taille %d", size)
	}
	if v := le.Uint32(d[0:]); v != 1 {
		return ConnectorMap{}, fmt.Errorf("réponse 708 : premier mot %d, forme non qualifiée", v)
	}
	n, nc := int(d[cmapEntries]), int(d[cmapConnectors])
	if nc != cmapQualifiedConns {
		return ConnectorMap{}, fmt.Errorf("réponse 708 : %d connecteurs, forme non qualifiée", nc)
	}
	if n > cmapMaxEntries || FIBHeaderSize+cmapEntryBase+n*cmapEntrySize > size {
		return ConnectorMap{}, fmt.Errorf("réponse 708 : %d entrées pour %d octets", n, size)
	}
	m := ConnectorMap{Names: map[uint8]string{}}
	for c := 0; c < nc; c++ {
		e := d[cmapConnectorBase+c*cmapConnectorSize:]
		if _, dup := m.Names[e[0]]; dup {
			return ConnectorMap{}, fmt.Errorf("réponse 708 : connecteur %d en double", e[0])
		}
		m.Names[e[0]] = field(e[4:12])
	}
	seen := map[uint32]bool{}
	for i := 0; i < n; i++ {
		e := d[cmapEntryBase+i*cmapEntrySize:]
		ce := ConnectorEntry{Disk: le.Uint32(e[0:]), Connector: e[4], Device: e[5], Phy: e[6]}
		if _, ok := m.Names[ce.Connector]; !ok {
			return ConnectorMap{}, fmt.Errorf("réponse 708 : entrée %d sur un connecteur inconnu (%d)", i, ce.Connector)
		}
		if seen[ce.Disk] {
			return ConnectorMap{}, fmt.Errorf("réponse 708 : disque %#x en double", ce.Disk)
		}
		seen[ce.Disk] = true
		m.Entries = append(m.Entries, ce)
	}
	return m, nil
}
