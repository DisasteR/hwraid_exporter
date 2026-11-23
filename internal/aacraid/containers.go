// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode"
)

// Formats des lectures de conteneurs, de membres et de disques
// (docs/aacraid-interface.md, sections 5 et 6). Positions en octets dans
// les données reconstituées du transfert paginé.

// ContainerListNeed est le nombre d'octets nécessaires pour décoder la liste
// des conteneurs à partir de ce qui est déjà lu.
func ContainerListNeed(d []byte) int {
	if len(d) < 8 {
		return 8
	}
	return 8 + 4*int(binary.LittleEndian.Uint32(d[4:]))
}

// DecodeContainerList renvoie les numéros de conteneur (229, paramètre 0) :
// mot 1 = nombre, puis un numéro par mot à partir de l'octet 8.
func DecodeContainerList(d []byte) ([]uint32, error) {
	if len(d) < 8 {
		return nil, fmt.Errorf("container list: %d bytes", len(d))
	}
	le := binary.LittleEndian
	n := int(le.Uint32(d[4:]))
	if n > (len(d)-8)/4 {
		return nil, fmt.Errorf("container list: %d entries for %d bytes", n, len(d))
	}
	out := make([]uint32, n)
	seen := map[uint32]bool{}
	for i := range out {
		out[i] = le.Uint32(d[8+4*i:])
		if seen[out[i]] {
			return nil, fmt.Errorf("container list: duplicate container %d", out[i])
		}
		seen[out[i]] = true
	}
	return out, nil
}

// DiskListNeed est l'équivalent de ContainerListNeed pour la liste des
// disques.
func DiskListNeed(d []byte) int {
	if len(d) < 4 {
		return 4
	}
	return 4 + 4*int(binary.LittleEndian.Uint32(d[0:]))
}

// DecodeDiskList renvoie les numéros des disques présents (247) : mot 0 =
// nombre, puis un numéro par mot.
func DecodeDiskList(d []byte) ([]uint32, error) {
	if len(d) < 4 {
		return nil, fmt.Errorf("disk list: %d bytes", len(d))
	}
	le := binary.LittleEndian
	n := int(le.Uint32(d[0:]))
	if n > (len(d)-4)/4 {
		return nil, fmt.Errorf("disk list: %d entries for %d bytes", n, len(d))
	}
	out := make([]uint32, n)
	seen := map[uint32]bool{}
	for i := range out {
		out[i] = le.Uint32(d[4+4*i:])
		if seen[out[i]] {
			return nil, fmt.Errorf("disk list: duplicate disk %d", out[i])
		}
		seen[out[i]] = true
	}
	return out, nil
}

// NoUID marque l'absence d'UID (conteneur sans parent, membre disque).
const NoUID = 0xffffffff

// Types de conteneur observés (octet 48 de l'information de conteneur).
const (
	TypeAssembly = 1 // conteneur qui assemble d'autres conteneurs (« Array0 »)
	TypeMirror   = 3 // miroir de deux disques
)

// ContainerInfoNeed : octets nécessaires. Le nom est lu sur 16 octets au
// plus (longueur maximale non établie ; sur l'hôte de référence, les octets
// qui suivent le nom ne sont pas du texte).
const ContainerInfoNeed = containerName + 16

const (
	containerUID     = 16
	containerType    = 48
	containerParent  = 52
	containerMembers = 56
	containerBlocks  = 64
	containerName    = 296
)

// ContainerInfo regroupe les champs utilisés de l'information de conteneur
// (234).
type ContainerInfo struct {
	UID, Type, Parent uint32
	Members           int
	Blocks            uint64 // taille en blocs de 512 octets
	Name              string
}

// DecodeContainerInfo décode l'information de conteneur.
func DecodeContainerInfo(d []byte) (ContainerInfo, error) {
	if len(d) < ContainerInfoNeed {
		return ContainerInfo{}, fmt.Errorf("container info: %d bytes", len(d))
	}
	le := binary.LittleEndian
	name := d[containerName:ContainerInfoNeed]
	if i := bytes.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	c := ContainerInfo{
		UID:     le.Uint32(d[containerUID:]),
		Type:    le.Uint32(d[containerType:]),
		Parent:  le.Uint32(d[containerParent:]),
		Members: int(le.Uint32(d[containerMembers:])),
		Blocks:  le.Uint64(d[containerBlocks:]),
		Name:    printable(name),
	}
	if c.Members < 1 || c.Members > MaxMembers {
		return ContainerInfo{}, fmt.Errorf("container info %#x: %d members", c.UID, c.Members)
	}
	return c, nil
}

// Drapeaux d'état d'un membre (octets 12-15 d'une entrée).
const (
	MemberRebuilding = 0x2
	MemberDead       = 0x4
)

// Member est une entrée de la liste des membres (249).
type Member struct {
	Disk     uint32 // NoUID si le membre est un conteneur
	ChildUID uint32 // NoUID si le membre est un disque
	Rank     uint8
	Flags    uint32
	Start    uint64
	Blocks   uint64
}

// DecodeMemberList décode la liste des membres : mot 1 = nombre, puis une
// entrée de 32 octets par membre à partir de l'octet 8.
func DecodeMemberList(d []byte) ([]Member, error) {
	if len(d) < 8 {
		return nil, fmt.Errorf("member list: %d bytes", len(d))
	}
	le := binary.LittleEndian
	n := int(le.Uint32(d[4:]))
	if n < 1 || n > MaxMembers || n > (len(d)-8)/32 {
		return nil, fmt.Errorf("member list: %d entries for %d bytes", n, len(d))
	}
	out := make([]Member, n)
	for i := range out {
		e := d[8+32*i:]
		out[i] = Member{
			Disk:     le.Uint32(e[0:]),
			ChildUID: le.Uint32(e[4:]),
			Rank:     e[10],
			Flags:    le.Uint32(e[12:]),
			Start:    le.Uint64(e[16:]),
			Blocks:   le.Uint64(e[24:]),
		}
		if (out[i].Disk == NoUID) == (out[i].ChildUID == NoUID) {
			return nil, fmt.Errorf("member list: entry %d is neither a disk nor a container", i)
		}
	}
	return out, nil
}

// printable garde un nom affichable : UTF-8 valide, sans caractère de
// contrôle (un label Prometheus non UTF-8 ferait rejeter tout le scrape).
func printable(b []byte) string {
	s := strings.ToValidUTF8(string(b), "?")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}
