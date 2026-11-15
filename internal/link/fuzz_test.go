package link

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A pairing code reads back to its 20 bytes however it's typed (case,
// dashes, spaces), and anything else isn't a code, without a panic.
func FuzzCode(f *testing.F) {
	code, _ := NewCode()
	f.Add(code)
	f.Add(strings.ToLower(strings.ReplaceAll(code, "-", " ")))
	f.Add("")
	f.Add("ABCD-EFGH")
	f.Add("ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567=")
	f.Add("0000-1111-8888-9999-0000-1111-8888-9999")
	f.Fuzz(func(t *testing.T, s string) {
		b, err := codeBytes(s)
		if err != nil {
			return
		}
		if len(b) != 20 {
			t.Fatalf("%q read as %d bytes", s, len(b))
		}
		for _, again := range []string{writeCode(b), strings.ToLower(writeCode(b)), strings.ReplaceAll(writeCode(b), "-", "")} {
			if b2, err := codeBytes(again); err != nil || !bytes.Equal(b, b2) {
				t.Fatalf("%q read as %x, written %q read as %x (%v)", s, b, again, b2, err)
			}
		}
	})
}

// The gateway's side of the preamble: whatever the line, it never reads
// past it (HTTP/2 follows), it answers OK only to "link" from the paired
// backend at its own version, and a line nothing pairs changes nothing
// kept (no code spent, no key replaced).
func FuzzPreamble(f *testing.F) {
	dir := f.TempDir()
	paired, _, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := os.WriteFile(filepath.Join(dir, "backend.pub"), []byte(hex.EncodeToString(paired)+"\n"), 0o600); err != nil {
		f.Fatal(err)
	}
	code, _ := NewCode()
	g, err := NewGateway(dir, "1.0", func() string { return code }, quiet)
	if err != nil {
		f.Fatal(err)
	}
	f.Add([]byte("OVFLINK1 link 1.0\nPRI * HTTP/2.0"), true)
	f.Add([]byte("OVFLINK1 link 1.0\n"), false)
	f.Add([]byte("OVFLINK1 link 2.0\n"), true)
	f.Add([]byte("OVFLINK1 pair 1.0 "+strings.Repeat("ab", 32)+"\n"), false)
	f.Add([]byte("OVFLINK1 pair 1.0 zz\n"), false)
	f.Add([]byte("OVFLINK1 pair 1.0\n"), false)
	f.Add([]byte("OVFLINK1\n"), true)
	f.Add([]byte(strings.Repeat("A", 600)+"\n"), true)
	f.Add([]byte("OVFLINK1 link 1.0"), true)
	f.Add([]byte("\x00\xff OVFLINK1  link\t1.0 \r\n"), true)
	f.Fuzz(func(t *testing.T, line []byte, fromPaired bool) {
		pub := other
		if fromPaired {
			pub = paired
		}
		r := bytes.NewReader(line)
		answer, ok := g.preamble(r, pub)
		read := len(line) - r.Len()
		if nl := bytes.IndexByte(line, '\n'); nl >= 0 && read > nl+1 {
			t.Fatalf("read %d bytes, past the line's end at %d", read, nl+1)
		}
		if ok != strings.HasPrefix(answer, "OK") || (!ok && !strings.HasPrefix(answer, "NO ")) {
			t.Fatalf("answered %q, %v", answer, ok)
		}
		if ok {
			f := strings.Fields(string(line[:bytes.IndexByte(line, '\n')]))
			if !fromPaired || len(f) < 3 || f[0] != preambleTag || f[1] != "link" || f[2] != "1.0" {
				t.Fatalf("%q from paired=%v: answered %q", line, fromPaired, answer)
			}
		}
		if !bytes.Equal(g.BackendKey(), paired) {
			t.Fatalf("%q replaced the paired key", line)
		}
		if _, err := os.Stat(filepath.Join(dir, "used-code")); err == nil {
			t.Fatalf("%q spent the code", line)
		}
	})
}
