// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package module

import (
	"errors"
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

func (f fake) Name() string                        { return f.name }
func (f fake) Match(c Card) bool                   { return c.PCI.Driver == f.driver }
func (f fake) Capabilities() uint64                { return f.caps }
func (fake) Collectors() []string                  { return nil }
func (f fake) Collect(Card, []NodeRef) Result      { return Result{NodeValid: true} }
func (f fake) Nodes(c Card) ([]NodeRequest, error) { return f.nodes(c) }

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
