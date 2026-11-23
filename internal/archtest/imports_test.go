// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package archtest

import (
	"path/filepath"
	"sort"
	"testing"
)

// Le dépôt réel ne doit contenir aucune violation.
func TestRepositoryHasNoForbiddenImports(t *testing.T) {
	v, err := Check(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range v {
		t.Errorf("%s : %s", x.File, x.Detail)
	}
}

// Le contrôle doit détecter chaque forme de contournement plantée dans le
// piège, et laisser passer les mêmes imports dans un répertoire autorisé.
func TestCheckerDetectsPlantedViolations(t *testing.T) {
	v, err := Check(filepath.Join("testdata", "trap"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range v {
		got = append(got, x.File+" : "+x.Detail)
	}
	sort.Strings(got)
	want := []string{
		"internal/nodes/ioctl.go : ioctl hors de internal/transport",
		"internal/privilege/sysioctl.go : ioctl hors de internal/transport",
		"internal/sysfs/alias.go : import golang.org/x/sys/unix",
		"internal/sysfs/cgo.go : import C (cgo interdit)",
		"internal/sysfs/conn.go : usage de SyscallConn",
		"internal/sysfs/direct.go : import syscall",
		"internal/sysfs/direct.go : import unsafe",
	}
	if len(got) != len(want) {
		t.Fatalf("violations :\n%v\nattendues :\n%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("violations :\n%v\nattendues :\n%v", got, want)
		}
	}
}
