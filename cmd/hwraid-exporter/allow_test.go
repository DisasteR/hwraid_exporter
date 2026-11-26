package main

import (
	"context"
	"testing"

	"github.com/DisasteR/hwraid_exporter/internal/module"
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
}

func (qualMod) Qualified(module.Card) bool { return true }
func (q qualMod) AllowUnqualified()        { *q.allowed++ }

// -allow-unqualified : seul un module du registre qui qualifie ses cartes
// est accepté ; la forme serial: n'est pas encore disponible ; une valeur
// répétée n'autorise qu'une fois ; sans option, rien n'est autorisé.
func TestAllowUnqualified(t *testing.T) {
	n := 0
	registry := []module.Module{qualMod{plainMod{"aacraid"}, &n}, plainMod{"autre"}}
	for name, v := range map[string]string{
		"inconnu":     "megaraid",
		"sans qualif": "autre",
		"série":       "serial:ABC",
		"vide":        "",
		"toutes":      "all",
		"casse":       "AACRAID",
	} {
		if _, err := allowUnqualified([]string{v}, registry); err == nil {
			t.Fatalf("%s : %q accepté", name, v)
		}
	}
	if n != 0 {
		t.Fatalf("autorisation appliquée malgré un refus (%d)", n)
	}
	if got, err := allowUnqualified(nil, registry); err != nil || len(got) != 0 || n != 0 {
		t.Fatalf("sans option : %v, %v, %d", got, err, n)
	}
	got, err := allowUnqualified([]string{"aacraid", "aacraid"}, registry)
	if err != nil || len(got) != 1 || got[0] != "aacraid" || n != 1 {
		t.Fatalf("aacraid : %v, %v, %d autorisation(s)", got, err, n)
	}
}
