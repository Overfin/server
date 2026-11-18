package msfp

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// Key rotation and revocation (MSFP core section 12). A server changing
// its key from O to N signs a rotation statement with both keys, and
// sends it to its peers in a rotate call; a peer moves to N at once when
// N is the key the server committed to in advance (its next key
// commitment), or once its operator confirmed N's fingerprint when there
// is no commitment. A server whose key was stolen signs a revocation
// statement with it: its peers then hear nothing from that key but a
// rotation to the committed key.
//
// What a server keeps, and when it sends what, is the server's: this file
// is the statements, their signatures and the bodies that carry them.

// The statements' first lines (core sections 12.2 and 12.6).
const (
	rotationLabel   = "msfp-key-rotation-1"
	revocationLabel = "msfp-key-revocation-1"
)

// The states rotate and revoke answer (core sections 9.3 and 9.4).
const (
	// StateRotated: the new key is the sender's current key here (200).
	StateRotated = "rotated"
	// StatePending: the receiver's operator must confirm the new key
	// (202).
	StatePending = "pending"
	// StateSuspended: the key is revoked here (200).
	StateSuspended = "suspended"
)

// ErrStatement is returned for a rotate or revoke body that isn't valid:
// a bad member, a key MSFP refuses, a signature that doesn't verify, or a
// date more than MaxSkew from the receiver's clock. Its answer is 400
// invalid-request (core sections 9.3 and 9.4).
var ErrStatement = errors.New("federation: not a valid rotation or revocation")

// Commitment is the next key commitment to a key (core section 12.5): the
// SHA-256 of its 32 bytes, sent base64url. A server commits to its next
// key in advance, so that a thief holding its current key can't move its
// peers to a key of the thief's.
func Commitment(public ed25519.PublicKey) []byte {
	sum := sha256.Sum256(public)
	return sum[:]
}

// Rotation is a rotation statement (core section 12.2): a server moving
// from key Old to key New, committing to the key after New.
type Rotation struct {
	Old, New ed25519.PublicKey
	// Next is the commitment to the key after New: nil for none (the
	// statement then says "-").
	Next []byte
	// Date is when it was made, Unix seconds.
	Date int64
	// OldSignature and NewSignature are the statement's signatures with
	// Old and with New: the second proves the server holds New.
	OldSignature, NewSignature []byte
}

// rotationStatement is what both keys sign: five lines, no final line
// feed.
func rotationStatement(old, new ed25519.PublicKey, next []byte, date int64) []byte {
	committed := "-"
	if next != nil {
		committed = b64.EncodeToString(next)
	}
	return []byte(rotationLabel + "\n" + b64.EncodeToString(old) + "\n" + b64.EncodeToString(new) + "\n" +
		committed + "\n" + strconv.FormatInt(date, 10))
}

// SignRotation makes the statement of a server moving from old to new,
// committing to next (a Commitment, or nil for none), dated at.
func SignRotation(old, new Key, next []byte, at time.Time) Rotation {
	date := at.Unix()
	msg := rotationStatement(old.Public(), new.Public(), next, date)
	return Rotation{Old: old.Public(), New: new.Public(), Next: next, Date: date,
		OldSignature: old.sign(msg), NewSignature: new.sign(msg)}
}

// RotateBody is a rotate request's body (core section 9.3); a hello
// answer's rotation member carries the same members (section 12.4).
type RotateBody struct {
	NewKey         string `json:"newKey"`
	NextCommitment string `json:"nextCommitment,omitempty"`
	Date           int64  `json:"date"`
	OldSignature   string `json:"oldSignature"`
	NewSignature   string `json:"newSignature"`
}

// Body is the statement as rotate sends it. The old key isn't in it: the
// request is signed with it, and names it in MSFP-Key.
func (r Rotation) Body() RotateBody {
	b := RotateBody{NewKey: b64.EncodeToString(r.New), Date: r.Date,
		OldSignature: b64.EncodeToString(r.OldSignature), NewSignature: b64.EncodeToString(r.NewSignature)}
	if r.Next != nil {
		b.NextCommitment = b64.EncodeToString(r.Next)
	}
	return b
}

// ReadRotate reads a rotate body sent signed with old, at now, and checks
// it as core section 12.3, step 1 says: the statement rebuilt with old,
// both signatures (the new key valid under section 2.1, and not old), the
// date within MaxSkew. Anything wrong is ErrStatement. Whether the new
// key may be accepted (a commitment, another peer's key) is the
// receiver's to decide.
func ReadRotate(body []byte, old ed25519.PublicKey, now time.Time) (Rotation, error) {
	var m struct {
		NewKey         json.RawMessage `json:"newKey"`
		NextCommitment json.RawMessage `json:"nextCommitment"`
		Date           json.RawMessage `json:"date"`
		OldSignature   json.RawMessage `json:"oldSignature"`
		NewSignature   json.RawMessage `json:"newSignature"`
	}
	if DecodeJSON(body, &m) != nil {
		return Rotation{}, ErrStatement
	}
	r := Rotation{Old: old}
	var ok bool
	if r.New, ok = readBytes(m.NewKey, ed25519.PublicKeySize); !ok {
		return Rotation{}, ErrStatement
	}
	if m.NextCommitment != nil {
		if r.Next, ok = readBytes(m.NextCommitment, sha256.Size); !ok {
			return Rotation{}, ErrStatement
		}
	}
	if r.Date, ok = readDate(m.Date, now); !ok {
		return Rotation{}, ErrStatement
	}
	if r.OldSignature, ok = readBytes(m.OldSignature, ed25519.SignatureSize); !ok {
		return Rotation{}, ErrStatement
	}
	if r.NewSignature, ok = readBytes(m.NewSignature, ed25519.SignatureSize); !ok {
		return Rotation{}, ErrStatement
	}
	if !ValidKey(r.New) || SameKey(r.New, old) {
		return Rotation{}, ErrStatement
	}
	msg := rotationStatement(old, r.New, r.Next, r.Date)
	if !verify(old, msg, r.OldSignature) || !verify(r.New, msg, r.NewSignature) {
		return Rotation{}, ErrStatement
	}
	return r, nil
}

// Revocation is a revocation statement (core section 12.6): a server
// saying its key Key is compromised.
type Revocation struct {
	Key       ed25519.PublicKey
	Date      int64
	Signature []byte
}

func revocationStatement(key ed25519.PublicKey, date int64) []byte {
	return []byte(revocationLabel + "\n" + b64.EncodeToString(key) + "\n" + strconv.FormatInt(date, 10))
}

// SignRevocation makes the statement revoking k, dated at.
func SignRevocation(k Key, at time.Time) Revocation {
	date := at.Unix()
	return Revocation{Key: k.Public(), Date: date, Signature: k.sign(revocationStatement(k.Public(), date))}
}

// RevokeBody is a revoke request's body (core section 9.4).
type RevokeBody struct {
	Date      int64  `json:"date"`
	Signature string `json:"signature"`
}

// Body is the statement as revoke sends it: the key is the request's own.
func (r Revocation) Body() RevokeBody {
	return RevokeBody{Date: r.Date, Signature: b64.EncodeToString(r.Signature)}
}

// ReadRevoke reads a revoke body sent signed with key, at now, and checks
// its signature and its date (within MaxSkew). Anything wrong is
// ErrStatement.
func ReadRevoke(body []byte, key ed25519.PublicKey, now time.Time) (Revocation, error) {
	var m struct {
		Date      json.RawMessage `json:"date"`
		Signature json.RawMessage `json:"signature"`
	}
	if DecodeJSON(body, &m) != nil {
		return Revocation{}, ErrStatement
	}
	r := Revocation{Key: key}
	var ok bool
	if r.Date, ok = readDate(m.Date, now); !ok {
		return Revocation{}, ErrStatement
	}
	if r.Signature, ok = readBytes(m.Signature, ed25519.SignatureSize); !ok {
		return Revocation{}, ErrStatement
	}
	if !verify(key, revocationStatement(key, r.Date), r.Signature) {
		return Revocation{}, ErrStatement
	}
	return r, nil
}

// readBytes reads a member that must be a string of n bytes, base64url in
// its one form; absent is false.
func readBytes(raw json.RawMessage, n int) ([]byte, bool) {
	var s string
	if raw == nil || json.Unmarshal(raw, &s) != nil {
		return nil, false
	}
	return Decode64(s, n)
}

// readDate reads a statement's date: an integer, Unix seconds, within
// MaxSkew of now.
func readDate(raw json.RawMessage, now time.Time) (int64, bool) {
	var date int64
	if raw == nil || json.Unmarshal(raw, &date) != nil {
		return 0, false
	}
	if d := now.Sub(time.Unix(date, 0)); d > MaxSkew || d < -MaxSkew {
		return 0, false
	}
	return date, true
}
