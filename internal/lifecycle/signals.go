// Package lifecycle isole les constantes de signal du système, pour que la
// commande n'importe pas syscall (exigence S1).
package lifecycle

import (
	"os"
	"syscall"
)

// TerminationSignals sont les signaux qui déclenchent un arrêt propre.
func TerminationSignals() []os.Signal { return []os.Signal{syscall.SIGTERM, os.Interrupt} }
