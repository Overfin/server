package msfp

import (
	"crypto/ed25519"
	"encoding/json"
)

// Hello is a hello message, request or answer (core section 9.2), as it
// is read: what its sender says of the pairing, the versions it speaks,
// the certificates its address serves and its address; whether it can be
// called; and the members that go one way only.
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
	// Uncallable: it says inbound false, it can't be called (section
	// 7.5). Absent or true reads as false: it can.
	Uncallable bool

	// In a request from a server that can't be called only (read from no
	// other, whatever their value):
	//
	// KnownKey is the key it checks the receiver's answers against now
	// (section 12.4); nil when it gives none, or a string that isn't a
	// key (then it is neither of the receiver's keys, and ignored).
	KnownKey ed25519.PublicKey
	// RotationState is "pending" or "refused": it hasn't moved to the
	// receiver's new key. Any other string reads as "".
	RotationState string

	// In an answer only:
	//
	// Rotation and Revocation are the answering server's statements, as
	// they came (objects only: anything else reads as none), for a
	// server that can't be called to check (Pull).
	Rotation, Revocation json.RawMessage
	// CallsFailed: "my last calls to you couldn't connect" (true only;
	// any other value reads as false). For the panel to show, nothing
	// else.
	CallsFailed bool
}

// helloMembers are a hello's members, raw, for ReadHello to check each.
type helloMembers struct {
	State          json.RawMessage `json:"state"`
	Core           json.RawMessage `json:"core"`
	Parts          json.RawMessage `json:"parts"`
	Certificates   json.RawMessage `json:"certificates"`
	Address        json.RawMessage `json:"address"`
	NextCommitment json.RawMessage `json:"nextCommitment"`
	Inbound        json.RawMessage `json:"inbound"`
	// One way only.
	KnownKey    json.RawMessage `json:"knownKey"`
	Rotation    json.RawMessage `json:"rotation"`
	Revocation  json.RawMessage `json:"revocation"`
	CallsFailed json.RawMessage `json:"callsFailed"`
}

// versionMembers are a hello's members whose reader, readVersions, judges
// what they hold: a null entry of a list is ignored, as an unknown part
// is whatever its value (core sections 2.3 and 10.2).
var versionMembers = []string{"core", "parts"}

// ReadHello reads a hello's body: a request, or with answer, an answer.
// A request without state, or with a member of the wrong type, is a bad
// body (ErrJSON), as is an answer with one, but for certificates that
// aren't a list, which in an answer only count as none (section 3.2).
// A nextCommitment that isn't 32 bytes of base64url is a bad body too.
//
// The members that go one way only (section 9.2): in a request,
// knownKey and rotation are read, and type-checked (strings), only from
// a server that says it can't be called; from any other they are
// ignored, whatever their value, null included. revocation and
// callsFailed are ignored in a request, whatever their value. In an answer, knownKey is
// ignored; a rotation or a revocation that isn't an object, and a
// callsFailed that isn't true, count as absent.
//
// Whether an inbound false goes with an address of the right form needs
// the address stored for the sender: CheckInbound.
func ReadHello(body []byte, answer bool) (Hello, error) {
	var m helloMembers
	var err error
	if answer {
		err = DecodeAnswer(body, &m)
	} else {
		err = decodeRequest(body, &m, []string{"knownKey", "rotation", "revocation", "callsFailed"}, versionMembers)
	}
	if err != nil {
		return Hello{}, err
	}
	var h Hello
	if m.State == nil && !answer || m.State != nil && json.Unmarshal(m.State, &h.State) != nil {
		return Hello{}, ErrJSON
	}
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
	inbound := true
	if m.Address != nil && json.Unmarshal(m.Address, &h.Address) != nil ||
		m.Inbound != nil && json.Unmarshal(m.Inbound, &inbound) != nil {
		return Hello{}, ErrJSON
	}
	h.Uncallable = !inbound
	if m.NextCommitment != nil {
		var ok bool
		if h.NextCommitment, ok = readBytes(m.NextCommitment, 32); !ok {
			return Hello{}, ErrJSON
		}
	}
	if answer {
		h.Rotation, h.Revocation = onlyObject(m.Rotation), onlyObject(m.Revocation)
		h.CallsFailed = string(m.CallsFailed) == "true"
		return h, nil
	}
	if h.Uncallable {
		// From a server that can't be called: read again, its knownKey
		// and rotation kept this time, each a string or a bad body.
		if err := decodeRequest(body, &m, []string{"revocation", "callsFailed"}, versionMembers); err != nil {
			return Hello{}, err
		}
		var known, rotation string
		if m.KnownKey != nil && json.Unmarshal(m.KnownKey, &known) != nil ||
			m.Rotation != nil && json.Unmarshal(m.Rotation, &rotation) != nil {
			return Hello{}, ErrJSON
		}
		if key, ok := Decode64(known, ed25519.PublicKeySize); ok {
			h.KnownKey = key
		}
		if rotation == StatePending || rotation == RotationRefused {
			h.RotationState = rotation
		}
	}
	return h, nil
}

// RotationRefused is what a hello request from a server that can't be
// called says of a rotation it refused (core section 12.4, step 3); a
// pending one says StatePending.
const RotationRefused = "refused"

// onlyObject is raw when it's a JSON object, nil otherwise.
func onlyObject(raw json.RawMessage) json.RawMessage {
	if len(raw) > 0 && raw[0] == '{' {
		return raw
	}
	return nil
}
