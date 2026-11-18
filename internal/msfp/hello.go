package msfp

import "encoding/json"

// Hello is a hello message, request or answer (core section 9.2), as it
// is read: what its sender says of the pairing, the versions it speaks,
// the certificates its address serves and its address.
type Hello struct {
	// State is the sender's view: "paired", "waiting", "ended", or any
	// other string, which counts as "waiting".
	State string
	// Versions are those it lists, absent ones read as section 10.2 says.
	Versions Versions
	// Certificates are its pins, cleaned (CleanPins): none when it named
	// none.
	Certificates []string
	// Address is its address now, "" when it gives none.
	Address string
	// NextCommitment is its next key commitment (section 12.5), 32
	// bytes; nil when it gives none.
	NextCommitment []byte
}

// ReadHello reads a hello's body: a request, or with answer, an answer.
// A request without state, or with a member of the wrong type, is a bad
// body (ErrJSON), as is an answer with one, but for certificates that
// aren't a list, which in an answer only count as none (section 3.2).
// A nextCommitment that isn't 32 bytes of base64url is a bad body too.
// The members that go one way only (knownKey, rotation, revocation,
// callsFailed) are not read here.
func ReadHello(body []byte, answer bool) (Hello, error) {
	var m struct {
		State          json.RawMessage `json:"state"`
		Core           json.RawMessage `json:"core"`
		Parts          json.RawMessage `json:"parts"`
		Certificates   json.RawMessage `json:"certificates"`
		Address        json.RawMessage `json:"address"`
		NextCommitment json.RawMessage `json:"nextCommitment"`
		Inbound        json.RawMessage `json:"inbound"`
	}
	decode := DecodeJSON
	if answer {
		decode = DecodeAnswer
	}
	if err := decode(body, &m); err != nil {
		return Hello{}, err
	}
	var h Hello
	if m.State == nil && !answer || m.State != nil && json.Unmarshal(m.State, &h.State) != nil {
		return Hello{}, ErrJSON
	}
	var err error
	if h.Versions, err = readVersions(m.Core, m.Parts); err != nil {
		return Hello{}, err
	}
	if m.Certificates != nil {
		var list []json.RawMessage
		if json.Unmarshal(m.Certificates, &list) != nil {
			if !answer {
				return Hello{}, ErrJSON
			}
			list = nil
		}
		// Entries that aren't pins, strings or not, are skipped.
		var pins []string
		for _, e := range list {
			var pin string
			if json.Unmarshal(e, &pin) == nil {
				pins = append(pins, pin)
			}
		}
		h.Certificates = CleanPins(pins)
	}
	var b bool
	if m.Address != nil && json.Unmarshal(m.Address, &h.Address) != nil ||
		m.Inbound != nil && json.Unmarshal(m.Inbound, &b) != nil {
		return Hello{}, ErrJSON
	}
	if m.NextCommitment != nil {
		var ok bool
		if h.NextCommitment, ok = readBytes(m.NextCommitment, 32); !ok {
			return Hello{}, ErrJSON
		}
	}
	return h, nil
}
