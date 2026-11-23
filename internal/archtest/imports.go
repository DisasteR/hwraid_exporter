// Package archtest vérifie l'isolement des appels système (exigence S1) :
// seuls les paquets de transport, de création de nœuds et de gestion des
// privilèges peuvent importer les paquets d'appel système ou unsafe.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Forbidden : imports réservés aux paquets autorisés.
var Forbidden = []string{"syscall", "golang.org/x/sys/unix", "unsafe"}

// TransportDir : seul répertoire autorisé à émettre un ioctl.
const TransportDir = "internal/transport"

// AllowedDirs : répertoires (relatifs à la racine du module) qui peuvent
// les importer.
var AllowedDirs = []string{"internal/transport", "internal/nodes", "internal/privilege", "internal/lifecycle"}

// skipDirs : répertoires jamais analysés (le piège de test vit dans testdata
// et n'est analysé que lorsqu'on le désigne explicitement comme racine).
var skipDirs = map[string]bool{".git": true, "testdata": true, "vendor": true, "preuves-internes": true}

// Violation décrit un import ou un usage interdit.
type Violation struct {
	File   string
	Detail string
}

// Check analyse tous les fichiers .go sous root.
func Check(root string) ([]Violation, error) {
	var out []Violation
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		// Valable partout : pas de cgo, et pas d'ioctl hors du transport.
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == "C" {
				out = append(out, Violation{File: rel, Detail: "import C (cgo interdit)"})
			}
		}
		if !strings.HasPrefix(rel, TransportDir+"/") {
			// Noms locaux (alias compris) des paquets d'appel système.
			sys := map[string]bool{}
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if p != "syscall" && p != "golang.org/x/sys/unix" {
					continue
				}
				name := filepath.Base(p)
				if imp.Name != nil {
					name = imp.Name.Name
				}
				sys[name] = true
			}
			ioctl := false
			ast.Inspect(f, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && sys[id.Name] &&
						(strings.HasPrefix(sel.Sel.Name, "Ioctl") || sel.Sel.Name == "SYS_IOCTL") {
						ioctl = true
					}
				}
				return !ioctl
			})
			if ioctl {
				out = append(out, Violation{File: rel, Detail: "ioctl hors de " + TransportDir})
			}
		}
		if allowed(rel) {
			return nil
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			for _, bad := range Forbidden {
				if p == bad || strings.HasPrefix(p, bad+"/") {
					out = append(out, Violation{File: rel, Detail: "import " + p})
				}
			}
		}
		// Analyse syntaxique : un appel x.SyscallConn() est détecté, une
		// chaîne ou un commentaire contenant ce nom ne l'est pas.
		found := false
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "SyscallConn" {
				found = true
			}
			return !found
		})
		if found {
			out = append(out, Violation{File: rel, Detail: "usage de SyscallConn"})
		}
		return nil
	})
	return out, err
}

func allowed(rel string) bool {
	for _, d := range AllowedDirs {
		if strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	return false
}
