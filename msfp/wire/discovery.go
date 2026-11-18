package wire

import (
	"encoding/json"
	"errors"
	"strings"
)

// The discovery document (core section 11): an unsigned JSON document a
// server may serve at GET /.well-known/msfp, saying where its federation
// calls are (its base path) and, before any pairing, which versions it
// speaks. It proves nothing: the versions used are always those of the
// signed hello.

// DiscoveryPath is where a server serves its discovery document.
const DiscoveryPath = "/.well-known/msfp"

// DefaultBase is the base path of the federation calls when a server
// serves no document, or one without base.
const DefaultBase = "/api/v1/federation/"

// MaxDiscovery caps what is read of a discovery document: 16 KiB.
const MaxDiscovery = 16 << 10

// maxBase caps a base path, in bytes.
const maxBase = 256

// ErrBase is returned for a base path not of core section 11's form.
var ErrBase = errors.New("federation: not a base path in MSFP's form")

// CheckBase accepts a base path in its one form (core section 11): at
// most 256 bytes, an absolute path of segments of A-Z a-z 0-9 . _ ~ -,
// none empty, "." or "..", no percent-encoding at all, ending in
// /api/v1/federation/. So the path means the same before and after any
// normalization.
func CheckBase(base string) error {
	if len(base) > maxBase || !strings.HasPrefix(base, "/") || !strings.HasSuffix(base, DefaultBase) {
		return ErrBase
	}
	segments := strings.Split(strings.TrimSuffix(base[1:], "/"), "/")
	for _, s := range segments {
		if s == "" || s == "." || s == ".." {
			return ErrBase
		}
		for i := 0; i < len(s); i++ {
			c := s[i]
			ok := 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' ||
				c == '.' || c == '_' || c == '~' || c == '-'
			if !ok {
				return ErrBase
			}
		}
	}
	return nil
}

// Prefix is the part of a valid base before /api/v1/federation/: the
// server's prefix, "" by default (core section 11). Its stream root is
// <address><prefix>/api/v1/stream/.
func Prefix(base string) string {
	return strings.TrimSuffix(base, DefaultBase)
}

// Discovery is a discovery document, as read.
type Discovery struct {
	// Versions are those it lists, read as in a hello (section 10.2).
	Versions Versions
	// Base is its base path, DefaultBase when it gives none.
	Base string
}

// ReadDiscovery reads a discovery document's body. A body that isn't a
// JSON object, a core or parts member of the wrong type, and a base that
// isn't a string of CheckBase's form, make it no document (ErrJSON or
// ErrBase): its reader then uses DefaultBase.
func ReadDiscovery(body []byte) (Discovery, error) {
	var m struct {
		Core  json.RawMessage `json:"core"`
		Parts json.RawMessage `json:"parts"`
		Base  json.RawMessage `json:"base"`
	}
	if err := DecodeAnswer(body, &m); err != nil {
		return Discovery{}, err
	}
	v, err := readVersions(m.Core, m.Parts)
	if err != nil {
		return Discovery{}, err
	}
	d := Discovery{Versions: v, Base: DefaultBase}
	if m.Base != nil {
		if json.Unmarshal(m.Base, &d.Base) != nil {
			return Discovery{}, ErrJSON
		}
		if err := CheckBase(d.Base); err != nil {
			return Discovery{}, err
		}
	}
	return d, nil
}
