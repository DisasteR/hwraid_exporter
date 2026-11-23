// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

// Package procstatus lit l'identité et les capacités d'un processus dans
// /proc/<pid>/status et vérifie qu'elles correspondent exactement à
// l'état attendu après l'abandon des privilèges (exigence S6).
package procstatus

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Numéros de capacités Linux (include/uapi/linux/capability.h).
const (
	CapChown    = 0
	CapSetGID   = 6
	CapSetUID   = 7
	CapSetPCap  = 8
	CapSysRawIO = 17
	CapSysAdmin = 21
	CapMknod    = 27
)

// Mask construit un masque de capacités.
func Mask(caps ...uint) uint64 {
	var m uint64
	for _, c := range caps {
		m |= 1 << c
	}
	return m
}

// Status regroupe les champs utiles de /proc/<pid>/status.
type Status struct {
	UID, GID               [4]uint32 // réel, effectif, sauvegardé, système de fichiers
	Groups                 []uint32  // groupes supplémentaires
	CapInh, CapPrm, CapEff uint64
	CapBnd, CapAmb         uint64
	NoNewPrivs             bool
}

// Parse lit le format texte de /proc/<pid>/status. Un champ requis absent
// est une erreur : un contrôle qui n'a pas pu lire ne doit pas passer.
func Parse(r io.Reader) (Status, error) {
	var s Status
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		var err error
		switch key {
		case "Uid":
			s.UID, err = parseIDs(val)
		case "Gid":
			s.GID, err = parseIDs(val)
		case "Groups":
			s.Groups, err = parseGroups(val)
		case "CapInh":
			s.CapInh, err = strconv.ParseUint(val, 16, 64)
		case "CapPrm":
			s.CapPrm, err = strconv.ParseUint(val, 16, 64)
		case "CapEff":
			s.CapEff, err = strconv.ParseUint(val, 16, 64)
		case "CapBnd":
			s.CapBnd, err = strconv.ParseUint(val, 16, 64)
		case "CapAmb":
			s.CapAmb, err = strconv.ParseUint(val, 16, 64)
		case "NoNewPrivs":
			s.NoNewPrivs = val == "1"
		default:
			continue
		}
		if err != nil {
			return Status{}, fmt.Errorf("unreadable field %s: %w", key, err)
		}
		seen[key] = true
	}
	if err := sc.Err(); err != nil {
		return Status{}, err
	}
	for _, k := range []string{"Uid", "Gid", "Groups", "CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb", "NoNewPrivs"} {
		if !seen[k] {
			return Status{}, fmt.Errorf("missing field %s", k)
		}
	}
	return s, nil
}

func parseGroups(v string) ([]uint32, error) {
	var out []uint32
	for _, x := range strings.Fields(v) {
		n, err := strconv.ParseUint(x, 10, 32)
		if err != nil {
			return nil, err
		}
		out = append(out, uint32(n))
	}
	return out, nil
}

func parseIDs(v string) ([4]uint32, error) {
	var ids [4]uint32
	f := strings.Fields(v)
	if len(f) != 4 {
		return ids, fmt.Errorf("4 values expected, %d read", len(f))
	}
	for i, x := range f {
		n, err := strconv.ParseUint(x, 10, 32)
		if err != nil {
			return ids, err
		}
		ids[i] = uint32(n)
	}
	return ids, nil
}

// Expected décrit l'état exigé après l'abandon des privilèges, ensemble par
// ensemble. Après l'abandon, l'exporter ne garde CAP_SYS_RAWIO que dans les
// ensembles effectif, permis et limite ; hérité et ambiant sont vides, car
// l'exporter ne lance aucun programme.
type Expected struct {
	UID, GID                uint32
	Eff, Prm, Bnd, Inh, Amb uint64
}

// AfterDrop renvoie l'état attendu pour une identité et un masque conservé.
func AfterDrop(uid, gid uint32, keep uint64) Expected {
	return Expected{UID: uid, GID: gid, Eff: keep, Prm: keep, Bnd: keep}
}

// Verify renvoie une erreur décrivant chaque écart. Chaque ensemble de
// capacités doit être exactement égal à l'attendu : une capacité en trop,
// même seulement dans l'ensemble limite, est un écart.
func (s Status) Verify(e Expected) error {
	var errs []string
	for i, v := range s.UID {
		if v != e.UID {
			errs = append(errs, fmt.Sprintf("Uid[%d]=%d (attendu %d)", i, v, e.UID))
		}
	}
	for i, v := range s.GID {
		if v != e.GID {
			errs = append(errs, fmt.Sprintf("Gid[%d]=%d (attendu %d)", i, v, e.GID))
		}
	}
	sets := []struct {
		name      string
		got, want uint64
	}{{"CapInh", s.CapInh, e.Inh}, {"CapPrm", s.CapPrm, e.Prm}, {"CapEff", s.CapEff, e.Eff}, {"CapBnd", s.CapBnd, e.Bnd}, {"CapAmb", s.CapAmb, e.Amb}}
	for _, set := range sets {
		if set.got != set.want {
			errs = append(errs, fmt.Sprintf("%s=%016x (attendu %016x)", set.name, set.got, set.want))
		}
	}
	for i := range s.UID {
		if s.UID[i] == 0 || s.GID[i] == 0 {
			errs = append(errs, "residual root identity")
			break
		}
	}
	if len(s.Groups) != 0 {
		errs = append(errs, fmt.Sprintf("non-empty supplementary groups: %v", s.Groups))
	}
	if !s.NoNewPrivs {
		errs = append(errs, "NoNewPrivs=0")
	}
	if len(errs) > 0 {
		return fmt.Errorf("non-compliant privileges: %s", strings.Join(errs, ", "))
	}
	return nil
}
