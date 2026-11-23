// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

// Commande ioplprobe : sonde de la CI qui indique si l'appel système iopl
// est permis (« allowed ») ou refusé (« denied ») dans l'environnement
// courant, pour vérifier le profil seccomp de deploy/docker.
package main

import (
	"fmt"
	"os"
	"syscall"
)

func main() {
	if err := syscall.Iopl(0); err != nil {
		fmt.Println("denied:", err)
		os.Exit(0)
	}
	fmt.Println("allowed")
}
