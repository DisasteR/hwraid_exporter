package sysfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// SCSIDevice est un périphérique SCSI d'un hôte, tel que le noyau le
// publie dans sysfs. Aucune donnée n'est demandée au matériel : le numéro
// de série vient de la page VPD 0x80 gardée par le noyau depuis la
// détection du périphérique.
type SCSIDevice struct {
	Address              string // H:C:T:L
	Channel, Target, LUN int
	Generic              string // nœud SCSI générique (sg8), vide s'il n'y en a pas
	Serial               string // page VPD 0x80, vide si non publiée
}

var scsiAddress = regexp.MustCompile(`^(\d+):(\d+):(\d+):(\d+)$`)

// readDevices liste les périphériques SCSI de l'hôte dont hostDir est le
// répertoire (…/hostN), dans l'arbre des périphériques :
// hostN/targetH:C:T/H:C:T:L. Un hôte sans cible n'a pas de périphérique.
func readDevices(hostDir string, host int) ([]SCSIDevice, error) {
	targets, err := os.ReadDir(hostDir)
	if err != nil {
		return nil, fmt.Errorf("devices of %s: %w", filepath.Base(hostDir), err)
	}
	var out []SCSIDevice
	for _, t := range targets {
		if !strings.HasPrefix(t.Name(), "target") {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(hostDir, t.Name()))
		if err != nil {
			return nil, fmt.Errorf("devices of %s: %w", t.Name(), err)
		}
		for _, e := range entries {
			m := scsiAddress.FindStringSubmatch(e.Name())
			if m == nil {
				continue
			}
			n := make([]int, 4)
			for i := range n {
				n[i], _ = strconv.Atoi(m[i+1])
			}
			if n[0] != host {
				continue
			}
			dir := filepath.Join(hostDir, t.Name(), e.Name())
			d := SCSIDevice{Address: e.Name(), Channel: n[1], Target: n[2], LUN: n[3]}
			if d.Generic, err = generic(dir); err != nil {
				return nil, err
			}
			if d.Serial, err = vpdSerial(dir); err != nil {
				return nil, err
			}
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out, nil
}

// generic renvoie le nom du nœud SCSI générique du périphérique (sgN), vide
// s'il n'y en a pas ou s'il n'est pas unique.
func generic(dir string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "scsi_generic"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%s: scsi_generic: %w", filepath.Base(dir), err)
	}
	if len(entries) != 1 {
		return "", nil
	}
	return entries[0].Name(), nil
}

// vpdSerial décode la page VPD 0x80 (numéro de série de l'unité, SPC) :
// octet 1 = code de page, octets 2-3 = longueur, puis le numéro en ASCII,
// complété par des espaces. Une page absente, vide ou mal formée donne une
// chaîne vide : valeur non observée, sans effet sur la découverte (elle
// concerne aussi les disques d'autres pilotes). Seule une erreur de lecture
// est signalée.
func vpdSerial(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "vpd_pg80"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%s: vpd_pg80: %w", filepath.Base(dir), err)
	}
	if len(b) < 4 || b[1] != 0x80 {
		return "", nil
	}
	n := int(b[2])<<8 | int(b[3])
	if 4+n > len(b) {
		return "", nil
	}
	s := strings.TrimSpace(strings.TrimRight(string(b[4:4+n]), "\x00"))
	for _, r := range s {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) {
			return "", nil
		}
	}
	return s, nil
}
