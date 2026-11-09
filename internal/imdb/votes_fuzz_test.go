package imdb

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// A day's votes file that's damaged, or isn't one, gives an error or some
// votes, never a panic or a loop without end.
func FuzzReadVotes(f *testing.F) {
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	z.Write([]byte{1, 2, 3, 200, 1, 5})
	z.Close()
	f.Add(b.Bytes())
	f.Add([]byte{})
	f.Add([]byte{0x1f, 0x8b})
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		const day = "2026-11-01"
		if err := os.WriteFile(filepath.Join(dir, votesPrefix+day), data, 0o600); err != nil {
			t.Fatal(err)
		}
		n := 0
		_ = ReadVotes(dir, day, func(id string, votes int) {
			if n++; n > 10_000_000 {
				t.Fatal("too many votes from a small file")
			}
		})
	})
}
