// Package sysfs inventorie les hôtes SCSI et leur fonction PCI parente
// à partir de sysfs, sans aucun appel au matériel.
package sysfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// pciAddress reconnaît une adresse PCI complète, domaine compris (0000:01:00.0).
var pciAddress = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)

// PCIFunction décrit la fonction PCI parente d'un hôte SCSI.
type PCIFunction struct {
	Address         string // domaine:bus:slot.fonction
	Driver          string // nom du pilote lié, vide si aucun
	Class           string // classe PCI (0x010400 : contrôleur RAID)
	Vendor          string // identifiants tels que publiés par sysfs (0x9005…)
	Device          string
	SubsystemVendor string
	SubsystemDevice string
}

// SCSIHost décrit un hôte SCSI et les attributs publiés par son pilote.
type SCSIHost struct {
	Name     string // host0
	Number   int    // H de hostH
	ProcName string // nom du pilote (proc_name)
	PCI      PCIFunction

	// Attributs facultatifs : vides si le pilote ne les publie pas.
	UniqueID     string
	SerialNumber string
	Model        string
	Firmware     string // hba_kernel_version pour aacraid

	// Devices : périphériques SCSI de l'hôte, vide si leur lecture a échoué
	// (l'erreur est alors signalée par Discover, l'hôte reste retenu).
	Devices []SCSIDevice
}

// HostError signale un hôte qui n'a pas pu être rattaché ou lu.
// Les autres hôtes restent exploitables.
type HostError struct {
	Host string
	Err  error
}

func (e HostError) Error() string { return e.Host + ": " + e.Err.Error() }
func (e HostError) Unwrap() error { return e.Err }

// ErrNoPCIParent : aucun ancêtre de l'hôte ne porte une adresse PCI (hôte
// virtuel, VMBus, iSCSI). Preuve structurelle que l'hôte n'est pas une carte
// PCI : il est hors périmètre et ignoré par Discover.
var ErrNoPCIParent = errors.New("no parent PCI function")

// ErrPCIUnreadable : un ancêtre porte une adresse PCI mais ne publie pas de
// fichier vendor lisible. Anomalie de lecture : l'hôte reste en erreur, pour
// qu'une carte ne sorte pas de l'inventaire sur une lecture ratée.
var ErrPCIUnreadable = errors.New("unreadable parent PCI function")

// Discover parcourt <root>/class/scsi_host. root vaut "/sys" en production.
// Une erreur globale n'est renvoyée que si le répertoire de classe est illisible ;
// les problèmes propres à un hôte sont renvoyés dans la liste de HostError.
// Un hôte sans aucun ancêtre PCI n'est ni retenu ni signalé.
func Discover(root string) ([]SCSIHost, []HostError, error) {
	classDir := filepath.Join(root, "class", "scsi_host")
	entries, err := os.ReadDir(classDir)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", classDir, err)
	}
	var hosts []SCSIHost
	var herrs []HostError
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "host") {
			continue
		}
		h, devErr, err := readHost(root, classDir, name)
		if errors.Is(err, ErrNoPCIParent) {
			continue // hôte non PCI : hors périmètre
		}
		if err != nil {
			herrs = append(herrs, HostError{Host: name, Err: err})
			continue
		}
		if devErr != nil {
			// Liste des périphériques illisible : l'hôte reste retenu (la
			// carte ne sort pas de l'inventaire), l'erreur est signalée.
			herrs = append(herrs, HostError{Host: name, Err: devErr})
		}
		hosts = append(hosts, h)
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Number < hosts[j].Number })
	return hosts, herrs, nil
}

// readHost lit un hôte. devErr signale une liste de périphériques
// illisible, sans invalider l'hôte.
func readHost(root, classDir, name string) (h SCSIHost, devErr, err error) {
	h, real, err := readHostAttrs(root, classDir, name)
	if err != nil {
		return h, nil, err
	}
	// real = …/hostN/scsi_host/hostN : le répertoire de l'hôte est deux
	// niveaux plus haut.
	h.Devices, devErr = readDevices(filepath.Dir(filepath.Dir(real)), h.Number)
	return h, devErr, nil
}

func readHostAttrs(root, classDir, name string) (SCSIHost, string, error) {
	h := SCSIHost{Name: name}
	n, err := strconv.Atoi(strings.TrimPrefix(name, "host"))
	if err != nil {
		return h, "", fmt.Errorf("invalid host name: %w", err)
	}
	h.Number = n

	real, err := filepath.EvalSymlinks(filepath.Join(classDir, name))
	if err != nil {
		return h, "", fmt.Errorf("resolving the link: %w", err)
	}
	h.ProcName = readAttr(real, "proc_name")
	h.UniqueID = readAttr(real, "unique_id")
	h.SerialNumber = readAttr(real, "serial_number")
	h.Model = readAttr(real, "model")
	h.Firmware = readAttr(real, "hba_kernel_version")

	pciDir, err := pciParent(root, real)
	if err != nil {
		return h, "", err
	}
	h.PCI = PCIFunction{
		Address:         filepath.Base(pciDir),
		Class:           readAttr(pciDir, "class"),
		Vendor:          readAttr(pciDir, "vendor"),
		Device:          readAttr(pciDir, "device"),
		SubsystemVendor: readAttr(pciDir, "subsystem_vendor"),
		SubsystemDevice: readAttr(pciDir, "subsystem_device"),
	}
	if drv, err := filepath.EvalSymlinks(filepath.Join(pciDir, "driver")); err == nil {
		h.PCI.Driver = filepath.Base(drv)
	}
	return h, real, nil
}

// IsRAIDClass indique une classe PCI « contrôleur de stockage RAID » (0x0104xx).
func (p PCIFunction) IsRAIDClass() bool { return strings.HasPrefix(p.Class, "0x0104") }

// pciParent remonte depuis le répertoire réel de l'hôte jusqu'à la première
// fonction PCI : le premier ancêtre dont le nom est une adresse PCI, qui
// doit publier un fichier vendor. La remontée s'arrête à la racine sysfs.
func pciParent(root, start string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolving the sysfs root: %w", err)
	}
	for dir := filepath.Dir(start); ; dir = filepath.Dir(dir) {
		if dir == realRoot || dir == filepath.Dir(dir) || !strings.HasPrefix(dir, realRoot+string(filepath.Separator)) {
			return "", ErrNoPCIParent
		}
		if pciAddress.MatchString(filepath.Base(dir)) {
			if _, err := os.Stat(filepath.Join(dir, "vendor")); err != nil {
				return "", fmt.Errorf("%w: %s: %v", ErrPCIUnreadable, filepath.Base(dir), err)
			}
			return dir, nil
		}
	}
}

// readAttr lit un attribut sysfs et retire les blancs finaux.
// Un attribut absent ou illisible donne une chaîne vide.
func readAttr(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
