// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

// Package lifecycle isole les constantes de signal du système, pour que la
// commande n'importe pas syscall (exigence S1).
package lifecycle

import (
	"os"
	"os/signal"
	"syscall"
)

// TerminationSignals sont les signaux qui déclenchent un arrêt propre :
// SIGTERM (superviseur), SIGINT (Ctrl+C) et SIGHUP (fin de session, envoi
// manuel). Laissé à son comportement par défaut, SIGHUP ferait sortir le
// processus aussitôt, même pendant une commande de gestion, que le pilote
// aacraid perdrait. SIGQUIT garde son rôle Go : vidage des piles et sortie
// immédiate, à réserver au diagnostic, comme SIGKILL.
func TerminationSignals() []os.Signal {
	return []os.Signal{syscall.SIGTERM, os.Interrupt, syscall.SIGHUP}
}

// IgnoreBrokenPipe ignore SIGPIPE. Sans cela, Go fait sortir le processus à
// la première écriture sur une sortie standard ou d'erreur dont le lecteur a
// disparu (collecteur de journaux arrêté), y compris pendant une commande de
// gestion, que le pilote aacraid perdrait. L'écriture échoue alors avec
// EPIPE, sans autre effet.
func IgnoreBrokenPipe() { signal.Ignore(syscall.SIGPIPE) }
