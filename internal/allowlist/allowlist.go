// Package allowlist implémente la liste blanche positive et fermée des
// requêtes de gestion (exigence S2 du cahier des charges) : toute requête
// qui ne correspond pas exactement à une entrée déclarée est refusée
// avant l'appel système.
package allowlist

import (
	"errors"
	"fmt"
)

// Request est une requête prête à partir : code ioctl et octets finaux,
// tels qu'ils seront remis au noyau.
type Request struct {
	Ioctl uint32
	Buf   []byte
}

// Entry autorise un code ioctl pour une taille de tampon exacte.
// Payload, s'il est fourni, valide le contenu sérialisé (sélecteurs FIB).
// Une entrée sans Payload n'est admise que pour un ioctl sans commande
// transmise au firmware (le tampon est alors une simple zone de réponse).
type Entry struct {
	Name    string // nom lisible, repris dans les journaux
	Ioctl   uint32
	Size    int
	Payload func(buf []byte) error
	Proof   string // référence de qualification (exigence S3), obligatoire
}

// ErrDenied signale une requête hors liste.
var ErrDenied = errors.New("request not on the allowlist")

// List est immuable après construction.
type List struct {
	byIoctl map[uint32]Entry
}

// New construit une liste. Elle refuse les doublons, les tailles invalides
// et les entrées sans référence de qualification.
func New(entries ...Entry) (*List, error) {
	l := &List{byIoctl: make(map[uint32]Entry, len(entries))}
	for _, e := range entries {
		switch {
		case e.Name == "":
			return nil, fmt.Errorf("entry %#x has no name", e.Ioctl)
		case e.Size <= 0:
			return nil, fmt.Errorf("%s: invalid size %d", e.Name, e.Size)
		case e.Proof == "":
			return nil, fmt.Errorf("%s: missing qualification reference", e.Name)
		}
		if _, dup := l.byIoctl[e.Ioctl]; dup {
			return nil, fmt.Errorf("%s: duplicate ioctl code %#x", e.Name, e.Ioctl)
		}
		l.byIoctl[e.Ioctl] = e
	}
	return l, nil
}

// Check renvoie l'entrée correspondante, ou une erreur enveloppant ErrDenied.
func (l *List) Check(r Request) (Entry, error) {
	e, ok := l.byIoctl[r.Ioctl]
	if !ok {
		return Entry{}, fmt.Errorf("%w: ioctl %#x", ErrDenied, r.Ioctl)
	}
	if len(r.Buf) != e.Size {
		return Entry{}, fmt.Errorf("%w: %s expects %d bytes, %d given", ErrDenied, e.Name, e.Size, len(r.Buf))
	}
	if e.Payload != nil {
		if err := e.Payload(r.Buf); err != nil {
			return Entry{}, fmt.Errorf("%w: %s: %v", ErrDenied, e.Name, err)
		}
	}
	return e, nil
}

// Entries renvoie une copie des entrées, pour la comparaison avec la liste déclarée.
func (l *List) Entries() map[uint32]Entry {
	out := make(map[uint32]Entry, len(l.byIoctl))
	for k, v := range l.byIoctl {
		out[k] = v
	}
	return out
}
