// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package module

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DisasteR/hwraid_exporter/internal/nodes"
	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
)

// fake est un module de simulation paramétrable.
type fake struct {
	name   string
	driver string
	caps   uint64
	nodes  func(Card) ([]NodeRequest, error)
}

func (f fake) Name() string                                    { return f.name }
func (f fake) Match(c Card) bool                               { return c.PCI.Driver == f.driver }
func (f fake) Capabilities() uint64                            { return f.caps }
func (fake) Collectors() []string                              { return nil }
func (f fake) Collect(context.Context, Card, []NodeRef) Result { return Result{NodeValid: true} }
func (f fake) Nodes(c Card) ([]NodeRequest, error)             { return f.nodes(c) }

func perCard(prefix, majorOf string) func(Card) ([]NodeRequest, error) {
	return func(c Card) ([]NodeRequest, error) {
		return []NodeRequest{{Name: prefix + c.PCI.Address[5:7], MajorOf: majorOf, Minor: uint32(len(c.Hosts))}}, nil
	}
}

func h(name string, num int, addr, driver, class string) sysfs.SCSIHost {
	return sysfs.SCSIHost{Name: name, Number: num, ProcName: driver, PCI: sysfs.PCIFunction{Address: addr, Driver: driver, Class: class}}
}

const raid, sata = "0x010400", "0x010601"

func TestBuildGroupsMatchesAndFilters(t *testing.T) {
	hosts := []sysfs.SCSIHost{
		h("host4", 4, "0000:01:00.0", "drvA", raid),
		h("host2", 2, "0000:01:00.0", "drvA", raid), // second hôte de la même carte
		h("host5", 5, "0000:02:00.0", "inconnu", raid),
		h("host6", 6, "0000:03:00.0", "ahci", sata),
	}
	mods := []Module{fake{name: "A", driver: "drvA", caps: 1 << 17, nodes: perCard("a", "majA")}}
	p := Build(hosts, mods)
	if len(p.Cards) != 2 {
		t.Fatalf("%d cartes, attendu 2 (la carte SATA est hors inventaire)", len(p.Cards))
	}
	a, u := p.Cards[0], p.Cards[1]
	if a.Module == nil || a.Module.Name() != "A" || len(a.Card.Hosts) != 2 || a.Card.Hosts[0].Number != 2 {
		t.Fatalf("carte A : %+v", a)
	}
	if u.Module != nil || u.Err != nil || u.Card.PCI.Address != "0000:02:00.0" {
		t.Fatalf("carte RAID sans module : %+v", u)
	}
	if p.Keep != 1<<17 {
		t.Fatalf("capacités %#x", p.Keep)
	}
}

// Un module sans carte n'ajoute aucune capacité.
func TestKeepOnlyForModulesWithCards(t *testing.T) {
	mods := []Module{
		fake{name: "A", driver: "drvA", caps: 1 << 17, nodes: perCard("a", "majA")},
		fake{name: "B", driver: "drvB", caps: 1 << 21, nodes: perCard("b", "majB")},
	}
	p := Build([]sysfs.SCSIHost{h("host0", 0, "0000:01:00.0", "drvA", raid)}, mods)
	if p.Keep != 1<<17 {
		t.Fatalf("capacités %#x : celles de B ne doivent pas être conservées", p.Keep)
	}
	if p = Build(nil, mods); p.Keep != 0 || len(p.Cards) != 0 {
		t.Fatalf("sans carte : %+v", p)
	}
}

func TestAmbiguousMatch(t *testing.T) {
	mods := []Module{
		fake{name: "A", driver: "drvA", caps: 1, nodes: perCard("a", "majA")},
		fake{name: "A2", driver: "drvA", caps: 2, nodes: perCard("b", "majA")},
	}
	p := Build([]sysfs.SCSIHost{h("host0", 0, "0000:01:00.0", "drvA", raid)}, mods)
	c := p.Cards[0]
	if c.Module != nil || !errors.Is(c.Err, ErrAmbiguous) || len(c.Requests) != 0 || p.Keep != 0 {
		t.Fatalf("ambiguïté : %+v keep=%#x", c, p.Keep)
	}
}

func TestNameConflictBetweenCards(t *testing.T) {
	same := func(Card) ([]NodeRequest, error) { return []NodeRequest{{Name: "n0", MajorOf: "majA"}}, nil }
	mods := []Module{fake{name: "A", driver: "drvA", nodes: same}}
	p := Build([]sysfs.SCSIHost{h("host0", 0, "0000:01:00.0", "drvA", raid), h("host1", 1, "0000:02:00.0", "drvA", raid)}, mods)
	for _, c := range p.Cards {
		if !errors.Is(c.Err, ErrNameConflict) || len(c.Requests) != 0 {
			t.Fatalf("conflit non signalé : %+v", c)
		}
	}
	if specs := p.Specs(map[string]uint32{"majA": 240}); len(specs) != 0 {
		t.Fatalf("nœuds produits malgré le conflit : %+v", specs)
	}
}

func TestNodesErrorKeepsCardListed(t *testing.T) {
	bad := func(Card) ([]NodeRequest, error) { return nil, errors.New("unique_id invalide") }
	p := Build([]sysfs.SCSIHost{h("host0", 0, "0000:01:00.0", "drvA", raid)}, []Module{fake{name: "A", driver: "drvA", nodes: bad}})
	if len(p.Cards) != 1 || p.Cards[0].Module == nil || p.Cards[0].Err == nil {
		t.Fatalf("carte en erreur : %+v", p.Cards)
	}
}

func TestSpecsResolveMajors(t *testing.T) {
	mods := []Module{
		fake{name: "A", driver: "drvA", nodes: perCard("a", "majA")},
		fake{name: "B", driver: "drvB", nodes: perCard("b", "majB")},
	}
	p := Build([]sysfs.SCSIHost{h("host0", 0, "0000:01:00.0", "drvA", raid), h("host1", 1, "0000:02:00.0", "drvB", raid)}, mods)
	specs := p.Specs(map[string]uint32{"majA": 240})
	if len(specs) != 1 || specs[0].Name != "a01" || specs[0].Major != 240 || specs[0].MajorOf != "majA" {
		t.Fatalf("specs : %+v", specs)
	}
	if p.Cards[1].Err == nil {
		t.Fatal("major introuvable non signalé pour la carte B")
	}
	refs := Refs("/dev/hwraid_exporter", p.Cards[0], specs)
	if len(refs) != 1 || refs[0].Path != "/dev/hwraid_exporter/a01" || refs[0].Dev != specs[0].Dev() {
		t.Fatalf("refs : %+v", refs)
	}
}

// Une carte n'est collectée que si chacun de ses nœuds est exactement un
// nœud préparé au démarrage : nom, major (relu) et minor.
func TestPrepared(t *testing.T) {
	mods := []Module{fake{name: "A", driver: "drvA", nodes: perCard("a", "majA")}}
	p := Build([]sysfs.SCSIHost{h("host0", 0, "0000:01:00.0", "drvA", raid)}, mods)
	majors := map[string]uint32{"majA": 240}
	prepared := p.Specs(majors)
	if len(prepared) != 1 || !Prepared(p.Cards[0], majors, prepared) {
		t.Fatalf("carte inchangée non collectée : %+v", prepared)
	}
	if Prepared(p.Cards[0], map[string]uint32{"majA": 241}, prepared) {
		t.Fatal("major changé accepté")
	}
	if Prepared(p.Cards[0], map[string]uint32{}, prepared) {
		t.Fatal("major disparu accepté")
	}
	other := append([]nodes.Spec(nil), prepared...)
	other[0].Minor++
	if Prepared(p.Cards[0], majors, other) {
		t.Fatal("minor différent accepté")
	}
	if Prepared(p.Cards[0], majors, nil) {
		t.Fatal("carte sans nœud préparé acceptée")
	}
	bad := p.Cards[0]
	bad.Err = errors.New("x")
	if Prepared(bad, majors, prepared) {
		t.Fatal("carte en erreur acceptée")
	}
}

// Capacités conservées : seulement pour un module dont une carte a ses
// nœuds préparés.
func TestKeepPrepared(t *testing.T) {
	mods := []Module{
		fake{name: "A", driver: "drvA", nodes: perCard("a", "majA"), caps: 1 << 17},
		fake{name: "B", driver: "drvB", nodes: perCard("b", "majB"), caps: 1 << 21},
	}
	p := Build([]sysfs.SCSIHost{h("host0", 0, "0000:01:00.0", "drvA", raid), h("host1", 1, "0000:02:00.0", "drvB", raid)}, mods)
	majors := map[string]uint32{"majA": 240, "majB": 241}
	specs := p.Specs(majors)
	if got := p.KeepPrepared(majors, specs); got != 1<<17|1<<21 {
		t.Fatalf("deux cartes préparées : %#x", got)
	}
	// Carte B non préparée (nœud absent) : sa capacité n'est pas gardée.
	var onlyA []nodes.Spec
	for _, s := range specs {
		if s.MajorOf == "majA" {
			onlyA = append(onlyA, s)
		}
	}
	if got := p.KeepPrepared(majors, onlyA); got != 1<<17 {
		t.Fatalf("carte B non préparée : %#x", got)
	}
	if got := p.KeepPrepared(majors, nil); got != 0 {
		t.Fatalf("aucune carte préparée : %#x", got)
	}
}

// Identity : chaque champ illisible rend l'identité incomplète ; un attribut
// d'hôte vient du premier hôte qui le renseigne.
func TestCardIdentity(t *testing.T) {
	full := func() Card {
		return Card{
			PCI: sysfs.PCIFunction{Vendor: "0x9005", Device: "0x028b", SubsystemVendor: "0x9005", SubsystemDevice: "0x0301"},
			Hosts: []sysfs.SCSIHost{{SerialNumber: "", Model: "6805"},
				{SerialNumber: "S1", Firmware: "5.3-0[19204]"}},
		}
	}
	f, ok := full().Identity()
	if !ok || strings.Join(f, "|") != "S1|0x9005|0x028b|0x9005|0x0301|6805|5.3-0[19204]" {
		t.Fatalf("identité : %v, complète %v", f, ok)
	}
	for name, blank := range map[string]func(*Card){
		"série":                     func(c *Card) { c.Hosts[1].SerialNumber = "" },
		"fabricant":                 func(c *Card) { c.PCI.Vendor = "" },
		"périphérique":              func(c *Card) { c.PCI.Device = "" },
		"fabricant du sous-système": func(c *Card) { c.PCI.SubsystemVendor = "" },
		"sous-système":              func(c *Card) { c.PCI.SubsystemDevice = "" },
		"modèle":                    func(c *Card) { c.Hosts[0].Model = "" },
		"firmware":                  func(c *Card) { c.Hosts[1].Firmware = "" },
	} {
		c := full()
		blank(&c)
		if _, ok := c.Identity(); ok {
			t.Errorf("%s illisible : identité déclarée complète", name)
		}
	}
}

// ResolveSerials : seule une carte dont le numéro lu est non vide et
// n'apparaît sur aucune autre carte de l'inventaire est marquée, que cette
// autre carte ait un module ou non (le numéro est lu sur le premier hôte qui
// le renseigne) ; inventaire incomplet : aucune carte marquée.
func TestResolveSerials(t *testing.T) {
	withSerial := func(name string, num int, addr, driver, serial string) sysfs.SCSIHost {
		x := h(name, num, addr, driver, raid)
		x.SerialNumber = serial
		return x
	}
	hosts := []sysfs.SCSIHost{
		withSerial("host1", 1, "0000:01:00.0", "drvA", "UNIQ"),
		withSerial("host2", 2, "0000:02:00.0", "drvA", "DUP"),
		withSerial("host3", 3, "0000:03:00.0", "inconnu", "DUP"),
		withSerial("host4", 4, "0000:04:00.0", "drvA", ""),
		withSerial("host5", 5, "0000:05:00.0", "drvA", ""),
		withSerial("host6", 6, "0000:05:00.0", "drvA", "SECOND"),
	}
	mods := []Module{fake{name: "A", driver: "drvA", nodes: perCard("a", "majA")}}
	p := Build(hosts, mods)
	p.ResolveSerials(true)
	want := map[string]bool{"0000:01:00.0": true, "0000:02:00.0": false, "0000:03:00.0": false, "0000:04:00.0": false, "0000:05:00.0": true}
	if len(p.Cards) != len(want) {
		t.Fatalf("%d cartes", len(p.Cards))
	}
	for _, pc := range p.Cards {
		if pc.Card.UniqueSerial != want[pc.Card.PCI.Address] {
			t.Errorf("%s : numéro unique %v", pc.Card.PCI.Address, pc.Card.UniqueSerial)
		}
	}
	p.ResolveSerials(false)
	for _, pc := range p.Cards {
		if pc.Card.UniqueSerial {
			t.Errorf("%s marquée malgré un inventaire incomplet", pc.Card.PCI.Address)
		}
	}
	// Carte 05 dont les hôtes annoncent T puis UNIQ : elle n'est pas
	// cohérente (non marquée), et UNIQ, annoncé par son second hôte, n'est
	// plus unique (carte 01 non marquée).
	hosts[4].SerialNumber, hosts[5].SerialNumber = "T", "UNIQ"
	p = Build(hosts, mods)
	p.ResolveSerials(true)
	for _, pc := range p.Cards {
		if pc.Card.UniqueSerial {
			t.Errorf("%s marquée malgré deux numéros sur la carte 05", pc.Card.PCI.Address)
		}
	}
	hosts[4].SerialNumber, hosts[5].SerialNumber = "", "SECOND"
	// Numéro illisible sur le second hôte d'une carte (le premier est lisible
	// mais vide) : il pourrait masquer un doublon, aucune carte n'est marquée.
	hosts[5].SerialNumber, hosts[5].SerialUnreadable = "", true
	p = Build(hosts, mods)
	p.ResolveSerials(true)
	for _, pc := range p.Cards {
		if pc.Card.UniqueSerial {
			t.Errorf("%s marquée malgré un numéro illisible sur un second hôte", pc.Card.PCI.Address)
		}
	}
	hosts[5].SerialNumber, hosts[5].SerialUnreadable = "SECOND", false
	// Numéro illisible sur un hôte, ici celui d'une carte sans module : il
	// pourrait masquer un doublon de UNIQ, aucune carte n'est marquée.
	hosts[2].SerialNumber, hosts[2].SerialUnreadable = "", true
	p = Build(hosts, mods)
	p.ResolveSerials(true)
	for _, pc := range p.Cards {
		if pc.Card.UniqueSerial {
			t.Errorf("%s marquée malgré un numéro illisible", pc.Card.PCI.Address)
		}
	}
}
