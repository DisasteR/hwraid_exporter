// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacio

import "testing"

// ReplaceIoctl remplace l'appel système pour les tests de bout en bout du
// paquet aacio_test (module, ordonnanceur et transport réels). Ce fichier
// n'est compilé que pour les tests de ce répertoire.
func ReplaceIoctl(t *testing.T, fn func(req uint32, buf []byte) error) {
	t.Helper()
	orig := ioctlFn
	ioctlFn = func(_ int, req uint32, buf []byte) error { return fn(req, buf) }
	t.Cleanup(func() { ioctlFn = orig })
}
