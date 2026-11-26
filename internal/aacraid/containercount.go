// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"encoding/binary"
	"fmt"
)

// CT_GET_CONTAINER_COUNT (Linux aacraid.h : CT_GET_CONTAINER_COUNT = 4,
// struct aac_get_container_count_resp) : nombre maximal de conteneurs de la
// carte, qui fixe la taille de la liste des conteneurs (229) demandée par
// l'outil de référence (CDC, section 6.11, forme 1).
//
// Forme de la requête : en-tête des requêtes paginées, données [2, 4, 0, 0,
// 0, 0xffffffff, 0, 0], reste à zéro. Sur les 21 requêtes de l'outil de
// référence capturées sur la carte de référence, aucune n'est identique
// octet pour octet à celle-ci : le mot 3 (0 ou 0x40), le mot 7 (0 ou 0x2e),
// l'octet 7 de l'en-tête (Unused pour Linux) et ses octets 12-19 et 24-31
// varient d'un envoi à l'autre sans changer les champs utiles de la réponse.
// Ce sont des résidus des tampons de l'outil (exigence S4), mis à zéro ici ;
// après mise à zéro de ces champs, sept requêtes capturées (celles aux mots
// 3 et 7 nuls) sont identiques à celle-ci.
const ctGetContainerCount = 4

// MaxContainersLimit borne MaxContainers avant le calcul de la taille de la
// liste : 1023 conteneurs, soit 4 100 octets, la plus grande taille de
// transfert acceptée (CDC, section 6.11, forme 1).
const MaxContainersLimit = 1023

// ContainerCount est MaxContainers tel que décodé d'une réponse de la carte
// à CT_GET_CONTAINER_COUNT : seule DecodeContainerCount en construit une
// valeur utilisable, et le transport n'accepte la taille de la liste des
// conteneurs que d'une telle valeur (provenance garantie à la compilation).
// La valeur nulle ne désigne aucun comptage.
type ContainerCount struct{ n uint32 }

// Max rend MaxContainers (0 pour la valeur nulle).
func (c ContainerCount) Max() uint32 { return c.n }

// ListSize rend la taille de la liste des conteneurs qui découle du
// comptage, 8 + 4 × MaxContainers, ou 0 pour la valeur nulle.
func (c ContainerCount) ListSize() uint32 {
	if c.n == 0 {
		return 0
	}
	return ContainerListSize(c.n)
}

// ContainerCountRequest construit la requête CT_GET_CONTAINER_COUNT.
func ContainerCountRequest() []byte {
	return pagedRequest(vmContainerConfig, ctGetContainerCount, 0, 0, 0, 0xffffffff, 0, 0)
}

// DecodeContainerCount vérifie la réponse à CT_GET_CONTAINER_COUNT et rend
// MaxContainers (mot 2). La réponse capturée a la forme des réponses
// paginées : Size = 496, statut 0, et 4 au mot 1 (dummy0 pour Linux : écho
// observé sur la carte de référence, exigé ici, sans garantie publique).
// MaxContainers hors de 1..MaxContainersLimit est refusé : l'outil n'émet
// aucune liste pour 0, et au-delà la taille dépasserait la borne de
// l'exporter.
func DecodeContainerCount(rsp []byte) (ContainerCount, error) {
	if len(rsp) != FIBSize {
		return ContainerCount{}, fmt.Errorf("container count: %d bytes received, %d expected", len(rsp), FIBSize)
	}
	le := binary.LittleEndian
	if cmd := le.Uint16(rsp[fibCommand:]); cmd != cmdContainerCommand {
		return ContainerCount{}, fmt.Errorf("container count: command %d in header", cmd)
	}
	if sz := le.Uint16(rsp[fibSize:]); sz != ChunkRspSize {
		return ContainerCount{}, fmt.Errorf("container count: size field %d, expected %d", sz, ChunkRspSize)
	}
	d := rsp[FIBHeaderSize:]
	if st := le.Uint32(d[0:]); st != 0 {
		return ContainerCount{}, fmt.Errorf("container count: status %d", st)
	}
	if echo := le.Uint32(d[4:]); echo != ctGetContainerCount {
		return ContainerCount{}, fmt.Errorf("container count: subcommand %d, expected %d", echo, ctGetContainerCount)
	}
	n := le.Uint32(d[8:])
	if n < 1 || n > MaxContainersLimit {
		return ContainerCount{}, fmt.Errorf("container count: MaxContainers %d outside [1, %d]", n, MaxContainersLimit)
	}
	return ContainerCount{n: n}, nil
}

// MaxContainersOpen est le plus grand MaxContainers avec lequel l'outil de
// référence ouvre la carte (mode ordinaire) : il compare MaxContainers − 1,
// non signé, à 0xff, et abandonne l'ouverture au-delà comme pour 0 (2.05
// FsaOpenAdapter2W 0x8f4d28..0x8f4d5a, 3.07 0x8c848f).
const MaxContainersOpen = 256

// ContainerListSize est la taille demandée pour la liste des conteneurs,
// comme le fait l'outil de référence : 8 + 4 × MaxContainers octets.
func ContainerListSize(maxContainers uint32) uint32 { return 8 + 4*maxContainers }

// ContainerListOpenSize rend la taille demandée (mot 7) si b est une
// ouverture de la liste des conteneurs (VM_ContainerConfig, sous-commande
// 229) ; ok est faux pour toute autre requête. Le transport s'en sert pour
// vérifier, au point d'envoi, que la taille découle du comptage de la carte.
func ContainerListOpenSize(b []byte) (size uint32, ok bool) {
	if len(b) < FIBHeaderSize+32 {
		return 0, false
	}
	le := binary.LittleEndian
	if le.Uint16(b[fibCommand:]) != cmdContainerCommand {
		return 0, false
	}
	d := b[FIBHeaderSize:]
	if le.Uint32(d[0:]) != vmContainerConfig || le.Uint32(d[4:]) != uint32(ReadContainerList) {
		return 0, false
	}
	return le.Uint32(d[28:]) &^ SizeHighBit, true
}

// validContainerListSize accepte les tailles 8 + 4 × n, n dans
// 1..MaxContainersLimit.
func validContainerListSize(size uint32) bool {
	return size >= ContainerListSize(1) && size <= ContainerListSize(MaxContainersLimit) && (size-8)%4 == 0
}
