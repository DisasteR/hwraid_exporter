// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacio

import (
	"fmt"
	"runtime"

	"golang.org/x/sys/unix"
)

// Un signal reçu pendant qu'une commande de gestion attend la carte
// interrompt l'attente dans le pilote aacraid. Si la réponse n'est pas
// encore arrivée (retour -ERESTARTSYS), le pilote ne rend jamais le bloc de
// commande (FIB) à sa petite réserve partagée (chemin aac_intr_normal, série
// 6) : chaque interruption en perd un jusqu'au redémarrage de la machine, et
// la réserve vide bloque toute gestion de la carte (ENOMEM). Si le
// gestionnaire du signal est installé avec SA_RESTART (cas de Go), le noyau
// relance de plus l'ioctl, qui envoie la commande une seconde fois.
//
// Le runtime Go envoie lui-même des signaux à ses threads (SIGURG, pour la
// préemption asynchrone) et le noyau peut livrer à n'importe quel thread un
// signal adressé au processus (SIGTERM). Les ioctl sont donc émis depuis un
// thread dédié qui bloque tous les signaux asynchrones : le noyau livre
// alors ceux adressés au processus à un autre thread, et ceux qui visent ce
// thread attendent la fin de l'appel. SIGKILL ne peut pas être bloqué.

// syncSignals restent débloqués : ils signalent une faute de ce thread
// même, que le runtime doit pouvoir traiter.
var syncSignals = []unix.Signal{unix.SIGSEGV, unix.SIGBUS, unix.SIGFPE, unix.SIGILL, unix.SIGTRAP, unix.SIGSYS}

// asyncSignalMask est l'ensemble de tous les signaux sauf syncSignals.
func asyncSignalMask() unix.Sigset_t {
	var s unix.Sigset_t
	for i := range s.Val {
		s.Val[i] = ^uint64(0)
	}
	for _, sig := range syncSignals {
		n := uint(sig) - 1
		s.Val[n/64] &^= 1 << (n % 64)
	}
	return s
}

// quietThread exécute des fonctions, l'une après l'autre, sur un thread
// système réservé dont les signaux asynchrones sont bloqués.
type quietThread struct {
	work chan func()
	done chan struct{} // fermé quand la goroutine du thread se termine
}

// startQuietThread démarre le thread. Une erreur de masquage est rendue
// telle quelle : aucune commande n'est alors émise (échec fermé).
func startQuietThread() (*quietThread, error) {
	q := &quietThread{work: make(chan func()), done: make(chan struct{})}
	ready := make(chan error, 1)
	go func() {
		defer close(q.done)
		// Jamais déverrouillé : à la sortie de la goroutine, le runtime
		// détruit le thread au lieu de le rendre, masque compris, à
		// d'autres goroutines.
		runtime.LockOSThread()
		mask := asyncSignalMask()
		if err := unix.PthreadSigmask(unix.SIG_BLOCK, &mask, nil); err != nil {
			ready <- fmt.Errorf("blocking signals on the ioctl thread: %w", err)
			return
		}
		ready <- nil
		for f := range q.work {
			f()
		}
	}()
	if err := <-ready; err != nil {
		return nil, err
	}
	return q, nil
}

// run exécute f sur le thread et attend sa fin. Si f met fin à la
// goroutine du thread (runtime.Goexit), l'appel n'est pas rendu comme
// réussi : run panique.
func (q *quietThread) run(f func()) {
	done := make(chan struct{})
	completed := false
	q.work <- func() {
		defer close(done)
		f()
		completed = true
	}
	<-done
	if !completed {
		panic("aacio: the ioctl thread ended during a call")
	}
}

// stop termine le thread. run ne doit plus être appelé ensuite.
func (q *quietThread) stop() { close(q.work) }
