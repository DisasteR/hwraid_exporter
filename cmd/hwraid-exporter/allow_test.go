// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/DisasteR/hwraid_exporter/internal/module"
	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
)

// plainMod est un module qui ne qualifie pas ses cartes.
type plainMod struct{ name string }

func (p plainMod) Name() string                                  { return p.name }
func (plainMod) Match(module.Card) bool                          { return false }
func (plainMod) Nodes(module.Card) ([]module.NodeRequest, error) { return nil, nil }
func (plainMod) Capabilities() uint64                            { return 0 }
func (plainMod) Collectors() []string                            { return nil }
func (plainMod) Collect(context.Context, module.Card, []module.NodeRef) module.Result {
	return module.Result{}
}

// qualMod qualifie ses cartes et compte les autorisations reçues.
type qualMod struct {
	plainMod
	allowed *int
	serials *[]string
}

func (qualMod) Qualified(module.Card) bool        { return true }
func (q qualMod) AllowUnqualified()               { *q.allowed++ }
func (q qualMod) AllowUnqualifiedSerial(s string) { *q.serials = append(*q.serials, s) }

// -allow-unqualified : un module du registre qui qualifie ses cartes, ou
// serial:<numéro> (transmis à chacun de ces modules, jamais aux autres) ;
// une valeur répétée n'autorise qu'une fois ; une valeur invalide fait
// échouer sans rien autoriser, même avec des valeurs valides ; sans option,
// rien n'est autorisé.
func TestAllowUnqualified(t *testing.T) {
	n, m := 0, 0
	var sa, sb []string
	registry := []module.Module{qualMod{plainMod{"aacraid"}, &n, &sa}, plainMod{"autre"}, qualMod{plainMod{"second"}, &m, &sb}}
	for name, v := range map[string]string{
		"inconnu":          "megaraid",
		"sans qualif":      "autre",
		"série vide":       "serial:",
		"série espacée":    "serial: ABC",
		"série espace fin": "serial:ABC ",
		"vide":             "",
		"toutes":           "all",
		"casse":            "AACRAID",
		"préfixe en casse": "SERIAL:ABC",
		"label pci":        "serial:pci:0000:01:00.0",
		"label en double":  "serial:serial:ABC",
	} {
		if _, _, err := allowUnqualified([]string{"aacraid", "serial:OK1", v}, registry); err == nil {
			t.Fatalf("%s : %q accepté", name, v)
		}
	}
	if n != 0 || m != 0 || len(sa)+len(sb) != 0 {
		t.Fatalf("autorisation appliquée malgré un refus (%d, %d, %v, %v)", n, m, sa, sb)
	}
	if mods, sers, err := allowUnqualified(nil, registry); err != nil || len(mods)+len(sers) != 0 || n != 0 {
		t.Fatalf("sans option : %v, %v, %v, %d", mods, sers, err, n)
	}
	mods, sers, err := allowUnqualified([]string{"aacraid", "serial:ABC", "aacraid", "serial:ABC", "serial:00X1"}, registry)
	if err != nil || !slices.Equal(mods, []string{"aacraid"}) || n != 1 || m != 0 {
		t.Fatalf("aacraid : %v, %v, %d et %d autorisation(s)", mods, err, n, m)
	}
	want := []string{"ABC", "00X1"}
	if !slices.Equal(sers, want) || !slices.Equal(sa, want) || !slices.Equal(sb, want) {
		t.Fatalf("séries : rendues %v, transmises %v et %v, attendu %v à chaque module qui qualifie", sers, sa, sb, want)
	}
	if _, _, err := allowUnqualified([]string{"serial:ABC"}, []module.Module{plainMod{"autre"}}); err == nil {
		t.Fatal("série acceptée sans module qui qualifie ses cartes")
	}
	// Messages d'erreur sans le numéro (exigence S9).
	for _, v := range []string{"serial:SECRET1 ", "serial:pci:SECRET1", "SERIAL:SECRET1"} {
		if _, _, err := allowUnqualified([]string{v}, registry); err == nil || strings.Contains(err.Error(), "SECRET1") {
			t.Fatalf("%q : erreur %v", v, err)
		}
	}
}

// buildPlan : l'unicité du numéro (sélecteur serial:) n'est établie que si
// la découverte n'a eu ni erreur globale ni hôte illisible.
func TestBuildPlanResolvesSerials(t *testing.T) {
	host := sysfs.SCSIHost{Name: "host1", Number: 1, ProcName: "aacraid", SerialNumber: "S1",
		PCI: sysfs.PCIFunction{Address: "0000:01:00.0", Driver: "aacraid", Class: "0x010400"}}
	for name, tc := range map[string]struct {
		herrs []sysfs.HostError
		err   error
		want  bool
	}{
		"complète":       {nil, nil, true},
		"hôte illisible": {[]sysfs.HostError{{Host: "host9", Err: errors.New("EIO")}}, nil, false},
		"erreur globale": {nil, errors.New("EIO"), false},
	} {
		p := buildPlan([]sysfs.SCSIHost{host}, tc.herrs, tc.err, nil)
		if len(p.Cards) != 1 || p.Cards[0].Card.UniqueSerial != tc.want {
			t.Fatalf("%s : %+v", name, p.Cards)
		}
	}
}

// Un argument positionnel arrête la lecture des options : serve refuse de
// démarrer (code 2) plutôt que d'ignorer une option placée après, avant
// toute préparation.
func TestServeRejectsPositionalArgument(t *testing.T) {
	if got := serve([]string{"-allow-unqualified=aacraid", "inattendu", "-allow-unqualified=serial:"}); got != 2 {
		t.Fatalf("code %d, attendu 2", got)
	}
}
