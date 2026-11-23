// Commande hwraid-exporter. Prototype 0, incrément 1 : seule la sous-commande
// « discover » est disponible. Elle lit sysfs, sans aucun accès au matériel,
// et affiche l'inventaire des hôtes SCSI avec leur fonction PCI parente.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "discover" {
		fmt.Fprintln(os.Stderr, "usage : hwraid-exporter discover [--sysfs /sys]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("discover", flag.ExitOnError)
	root := fs.String("sysfs", "/sys", "racine de sysfs")
	_ = fs.Parse(os.Args[2:])

	hosts, herrs, err := sysfs.Discover(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "découverte impossible :", err)
		os.Exit(1)
	}
	out := struct {
		Hosts  []sysfs.SCSIHost `json:"hosts"`
		Errors []string         `json:"errors,omitempty"`
	}{Hosts: append([]sysfs.SCSIHost{}, hosts...)}
	for _, e := range herrs {
		out.Errors = append(out.Errors, e.Error())
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
