package msfp_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The engine, its stores, its toolkit and its examples are the module
// that will become github.com/msfp-io/msfp-go: no file of them, tests
// included, imports Overfin, nor any module but the standard library and
// filippo.io/edwards25519.
func TestImportsNoServer(t *testing.T) {
	fset := token.NewFileSet()
	n := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		n++
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			first, _, _ := strings.Cut(p, "/")
			ok := !strings.Contains(first, ".") || p == "filippo.io/edwards25519" ||
				p == "github.com/overfin/server/msfp" || strings.HasPrefix(p, "github.com/overfin/server/msfp/")
			if !ok {
				t.Errorf("%s imports %s", path, p)
			}
		}
		return nil
	})
	if err != nil || n < 10 {
		t.Fatal("files not read", n, err)
	}
}
