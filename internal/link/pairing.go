package link

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// A pairing code is 20 random bytes (160 bits: out of reach of guessing),
// written in groups of four letters and digits, e.g. ABCD-EFGH-…
var codeEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewCode makes a pairing code.
func NewCode() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := codeEncoding.EncodeToString(b)
	var groups []string
	for len(s) > 0 {
		n := min(4, len(s))
		groups = append(groups, s[:n])
		s = s[n:]
	}
	return strings.Join(groups, "-"), nil
}

// codeBytes reads a code as the operator may have typed it: dashes,
// spaces and case don't matter.
func codeBytes(code string) ([]byte, error) {
	s := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
	b, err := codeEncoding.DecodeString(s)
	if err != nil || len(b) != 20 {
		return nil, errors.New("that isn't a pairing code")
	}
	return b, nil
}

// proof is one side's proof that it knows the code, bound to both keys:
// someone in the middle, without the code, can't make it for keys of its
// own.
func proof(code []byte, side string, gateway, backend ed25519.PublicKey) []byte {
	m := hmac.New(sha256.New, code)
	m.Write([]byte("overfin-link pairing " + side + "\n"))
	m.Write(gateway)
	m.Write(backend)
	return m.Sum(nil)
}

// The preamble, before HTTP/2 starts on the connection. The backend speaks
// first, one line:
//
//	OVFLINK1 link <version>
//	OVFLINK1 pair <version> <backend's proof, hex>
//
// The gateway answers one line:
//
//	OK                       (link: go on, HTTP/2 follows)
//	OK <gateway's proof>     (pair: paired; HTTP/2 follows)
//	NO <reason> [details]    (and closes)
const preambleTag = "OVFLINK1"

// maxLine bounds a preamble line.
const maxLine = 512

// readLine reads one preamble line a byte at a time: whatever follows it
// on the connection (HTTP/2) must stay unread.
func readLine(r io.Reader) ([]string, error) {
	var b []byte
	one := make([]byte, 1)
	for {
		if _, err := io.ReadFull(r, one); err != nil {
			return nil, err
		}
		c := one[0]
		if c == '\n' {
			break
		}
		if len(b) >= maxLine {
			return nil, errors.New("preamble line too long")
		}
		b = append(b, c)
	}
	return strings.Fields(string(b)), nil
}

func hexProof(s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != sha256.Size {
		return nil, fmt.Errorf("not a proof")
	}
	return b, nil
}
