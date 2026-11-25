// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import "fmt"

// Niveau RAID d'un conteneur (docs/aacraid-interface.md, section 6 bis) :
// conversion de base de l'outil de référence, dans la numérotation choisie
// par le conteneur. Les traitements particuliers de l'outil (migration,
// types classiques 5, 6 et 7) donnent un niveau inconnu : l'exporter ne le
// devine pas.

// Redundancy est la famille de tolérance d'un niveau, qui décide du calcul
// de l'état.
type Redundancy int

const (
	RedundancyUnknown  Redundancy = iota // niveau inconnu : état non calculé
	RedundancyNone                       // RAID 0, volume simple ou étendu
	RedundancySingle                     // une panne tolérée
	RedundancyDouble                     // deux pannes tolérées
	RedundancyAssembly                   // assemblage de conteneurs
)

// Level est le niveau publié : terme normalisé (docs/metrics.md), libellé
// de l'outil de référence et famille de tolérance.
type Level struct {
	Raid, Vendor string
	Redundancy   Redundancy
}

var (
	levelRaid0  = Level{"raid0", "0", RedundancyNone}
	levelRaid1  = Level{"raid1", "1", RedundancySingle}
	levelRaid1E = Level{"raid1e", "1E", RedundancySingle}
	levelRaid5  = Level{"raid5", "5", RedundancySingle}
	level5EE    = Level{"raid5", "5EE", RedundancySingle}
	levelRaid10 = Level{"raid10", "10", RedundancySingle}
	levelRaid50 = Level{"raid50", "50", RedundancySingle}
	levelRaid6  = Level{"raid6", "6", RedundancyDouble}
	levelRaid60 = Level{"raid60", "60", RedundancyDouble}
	level6RS    = Level{"raid6", "6 Reed-Solomon", RedundancyDouble}
	level60RS   = Level{"raid60", "60 Reed-Solomon", RedundancyDouble}
	levelAsm    = Level{"concat", "RAID_volume", RedundancyAssembly}
)

// classicReedSolomon : bit 10 du mot des octets 88-91 (bit 0x4 de l'octet
// 89), qui fait d'un RAID 6 ou 60 de numérotation classique sa variante
// Reed-Solomon.
const classicReedSolomon = 1 << 10

// minMembers : nombre minimal de membres de chaque niveau, repris de la
// table de création de l'outil de référence (docs/aacraid-interface.md,
// section 6 bis). Un conteneur qui en annonce moins a une topologie
// incompatible avec son type : niveau non établi.
var minMembers = map[string]int{
	"0": 2, "1": 2, "1E": 3, "5": 3, "5EE": 4, "10": 4, "50": 6,
	"6": 4, "6 Reed-Solomon": 4, "60": 8, "60 Reed-Solomon": 8,
	"Simple_volume": 1, "Spanned_volume": 2, "RAID_volume": 2,
}

// ContainerLevel renvoie le niveau du conteneur c, si ses membres sont
// compatibles avec lui ; sinon unknown. Hors assemblage, un niveau exige des
// membres tous disques, au moins aussi nombreux que son minimum (l'outil de
// référence construit aussi des RAID 10 et 50 classiques sur des grappes :
// cette forme n'est pas observée, son niveau n'est pas publié). Un
// assemblage (reconnu par volumeLevel) exige au moins deux membres.
func ContainerLevel(c Container, byUID map[uint32]ContainerInfo) Level {
	l := baseLevel(c, byUID)
	switch {
	case l.Redundancy == RedundancyUnknown:
		return l
	case l.Redundancy == RedundancyAssembly && c.Info.Members < minMembers[l.Vendor]:
		return unknownLevel(c.Info)
	case l.Redundancy == RedundancyAssembly:
		return l
	}
	disks := 0
	for _, m := range c.Members {
		if m.Disk != NoUID {
			disks++
		}
	}
	if disks == len(c.Members) && disks >= minMembers[l.Vendor] {
		return l
	}
	return unknownLevel(c.Info)
}

// baseLevel renvoie le niveau du conteneur c. byUID contient les
// informations de tous les conteneurs lus dans le même cycle : un membre
// conteneur n'en fait un assemblage que s'il y figure, et la migration se
// reconnaît aussi par un autre conteneur.
func baseLevel(c Container, byUID map[uint32]ContainerInfo) Level {
	i := c.Info
	unknown := unknownLevel(i)
	if migrating(i, byUID) {
		return unknown
	}
	if i.ClassicNumbering() {
		switch i.Type {
		case 1, 8:
			return volumeLevel(c, byUID)
		case 2:
			return levelRaid1
		case 3, 11:
			return levelRaid0
		case 4:
			return levelRaid5
		case 10:
			return levelRaid10
		case 12:
			// Volume de miroirs : assemblage, s'il a un membre conteneur
			// lu dans le cycle (autre que lui-même).
			if hasChildContainer(c, byUID) {
				return levelAsm
			}
			return unknown
		case 14:
			return levelRaid50
		case 15, 16:
			return level5EE
		case 17:
			return levelRaid1E
		case 18:
			if i.Visible&classicReedSolomon != 0 {
				return level6RS
			}
			return levelRaid6
		case 19:
			if i.Visible&classicReedSolomon != 0 {
				return level60RS
			}
			return levelRaid60
		}
		return unknown
	}
	switch i.Type {
	case 0, 1:
		return volumeLevel(c, byUID)
	case 2:
		return levelRaid0
	case 3:
		return levelRaid1
	case 4:
		return levelRaid10
	case 5:
		return levelRaid1E
	case 7:
		return levelRaid5
	case 8:
		return levelRaid50
	case 9, 10:
		return level5EE
	case 11:
		return levelRaid6
	case 12:
		return levelRaid60
	case 13:
		return level6RS
	case 14:
		return level60RS
	}
	return unknown
}

// hasChildContainer : un membre est un autre conteneur lu dans le cycle.
func hasChildContainer(c Container, byUID map[uint32]ContainerInfo) bool {
	for _, m := range c.Members {
		if _, ok := byUID[m.ChildUID]; ok && m.Disk == NoUID && m.ChildUID != c.Info.UID {
			return true
		}
	}
	return false
}

// unknownLevel : niveau non établi ; le type brut reste dans le libellé.
func unknownLevel(i ContainerInfo) Level {
	if i.ClassicNumbering() {
		return Level{"unknown", fmt.Sprintf("classic type %d", i.Type), RedundancyUnknown}
	}
	return Level{"unknown", fmt.Sprintf("type %d", i.Type), RedundancyUnknown}
}

// volumeLevel : volume simple (moins de deux membres), étendu, ou
// assemblage si l'un de ses membres est un conteneur lu dans le cycle
// (numérotation ARCIO seulement ; la numérotation classique a son propre
// type d'assemblage).
func volumeLevel(c Container, byUID map[uint32]ContainerInfo) Level {
	if !c.Info.ClassicNumbering() && hasChildContainer(c, byUID) {
		return levelAsm
	}
	// Un membre conteneur hors assemblage (introuvable, ou en numérotation
	// classique) : le volume ne repose pas seulement sur des disques, son
	// niveau n'est pas établi.
	for _, m := range c.Members {
		if m.Disk == NoUID {
			return unknownLevel(c.Info)
		}
	}
	if c.Info.Members < 2 {
		return Level{"single", "Simple_volume", RedundancyNone}
	}
	return Level{"concat", "Spanned_volume", RedundancyNone}
}

// migrating reprend les deux façons dont l'outil de référence reconnaît un
// conteneur en migration : son champ de tâche vaut 3 ; ou, en numérotation
// ARCIO, il n'a pas de parent et un autre conteneur en migration le désigne
// aux octets 224-231. (Le type classique 7, migration aussi pour l'outil,
// n'a pas de niveau dans la table : il est inconnu sans passer par ici.)
func migrating(i ContainerInfo, byUID map[uint32]ContainerInfo) bool {
	if i.Migrating() {
		return true
	}
	if i.ClassicNumbering() || i.Parent != NoUID {
		return false
	}
	for uid, o := range byUID {
		if uid != i.UID && o.Migrating() && (o.MigrationA == i.UID || o.MigrationB == i.UID) {
			return true
		}
	}
	return false
}
