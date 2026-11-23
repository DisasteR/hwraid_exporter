// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestResolve(t *testing.T) {
	vcs := func(rev, modified string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: rev}, {Key: "vcs.modified", Value: modified}}}
	}
	const rev = "2bc8c58f0e1d2c3b4a59687766554433221100ff"
	cases := []struct {
		name, ldv, ldr string
		bi             *debug.BuildInfo
		v, r           string
	}{
		{"publication : valeurs injectées", "v0.1.0", "abc", vcs(rev, "false"), "v0.1.0", "abc"},
		{"version injectée, révision du dépôt", "v0.1.0", "", vcs(rev, "false"), "v0.1.0", "2bc8c58f0e1d"},
		{"dépôt propre", "", "", vcs(rev, "false"), "devel-2bc8c58f0e1d", "2bc8c58f0e1d"},
		{"dépôt modifié", "", "", vcs(rev, "true"), "devel-2bc8c58f0e1d-dirty", "2bc8c58f0e1d-dirty"},
		{"go install", "", "", &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, "v0.1.0", ""},
		{"aucune information", "", "", nil, "dev", ""},
		{"hors dépôt", "", "", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "dev", ""},
	}
	for _, c := range cases {
		if v, r := Resolve(c.ldv, c.ldr, c.bi); v != c.v || r != c.r {
			t.Fatalf("%s : %q %q, attendu %q %q", c.name, v, r, c.v, c.r)
		}
	}
}
