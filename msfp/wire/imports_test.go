package wire

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The package stays the protocol alone: no file of it, tests included,
// imports the server that uses it, or any module but the standard
// library and filippo.io/edwards25519.
func TestImportsNoServer(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatal("no files", err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if !allowedImport(path) {
				t.Errorf("%s imports %s", name, path)
			}
		}
	}
}

// allowedImport reports whether the toolkit may import path: the
// standard library (no dot in its first element) and edwards25519 only.
func allowedImport(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".") || path == "filippo.io/edwards25519"
}
