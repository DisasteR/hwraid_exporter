// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

// Package buildinfo détermine la version et la révision publiées par
// hwraid_exporter_build_info.
package buildinfo

import "runtime/debug"

// Resolve renvoie la version et la révision du binaire. Par ordre de
// priorité :
//   - les valeurs injectées à la compilation (-ldflags -X, tag de la
//     publication et commit) ;
//   - la version du module, pour un binaire installé par go install
//     module@vX.Y.Z ;
//   - le commit que la chaîne Go intègre en compilant dans un dépôt git
//     (vcs.revision), suffixé de « -dirty » si l'arbre était modifié.
//
// À défaut, la version vaut « dev » et la révision est vide.
func Resolve(ldVersion, ldRevision string, bi *debug.BuildInfo) (version, revision string) {
	version, revision = ldVersion, ldRevision
	var vcsRev string
	var dirty bool
	if bi != nil {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				vcsRev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
	}
	if revision == "" && vcsRev != "" {
		revision = vcsRev
		if len(revision) > 12 {
			revision = revision[:12]
		}
		if dirty {
			revision += "-dirty"
		}
	}
	if version == "" && bi != nil && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = bi.Main.Version
	}
	if version == "" && revision != "" {
		version = "devel-" + revision
	}
	if version == "" {
		version = "dev"
	}
	return version, revision
}
