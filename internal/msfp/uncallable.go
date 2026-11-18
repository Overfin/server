package msfp

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base32"
	"errors"
	mathrand "math/rand/v2"
	"strings"
	"time"
)

// Servers that can't be called (MSFP core section 7.5): one that can
// connect out but can't be connected to (at home behind a router, say)
// says so with inbound false in its pair and hello messages. Its peers
// then never connect to it, and it asks them, on a schedule, for what it
// would otherwise be told: a hello every 20 seconds while a pairing
// waits for the other operator, every 5 minutes once paired, every hour
// once gone. Their rotations and revocations travel in their hello
// answers instead of rotate and revoke calls (core sections 12.4 and
// 12.6). It can watch what they share; it shares nothing.
//
// Its address is one that never resolves (core section 5.2),
// https://msfp-<26 characters>.invalid, made once and kept: a peer that
// ignores inbound connects nowhere.
//
// Which server can't be called, what each peer stores, and when the
// hellos go is the server's: this file is the address, the schedule's
// numbers, and the rules for the statements an answer carries.

// The address of a server that can't be called: this prefix, 16 random
// bytes in lowercase base32 without padding (26 characters), this suffix.
const (
	uncallablePrefix = "https://msfp-"
	uncallableSuffix = ".invalid"
	uncallableBytes  = 16
)

// lowerBase32 is RFC 4648's base32 (section 6) in lowercase, without
// padding.
var lowerBase32 = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// ErrUncallableAddress is returned for an address that isn't one of a
// server that can't be called, where one must be (core section 5.2).
var ErrUncallableAddress = errors.New("federation: a server that can't be called has an address https://msfp-<26 characters>.invalid")

// NewUncallableAddress makes the address of a server that can't be called,
// at random. A server makes it once, and keeps it.
func NewUncallableAddress() string {
	b := make([]byte, uncallableBytes)
	rand.Read(b)
	return uncallablePrefix + lowerBase32.EncodeToString(b) + uncallableSuffix
}

// CheckUncallable accepts the address of a server that can't be called,
// in its one form (core section 5.2): https://msfp-, 26 characters of
// a-z 2-7 that read back as exactly 16 bytes, .invalid, nothing else (no
// port, no other label). The 2 unused bits of the last character must be
// zero (section 18, point 30): the 16 bytes written again give the same
// text, so each address has one.
func CheckUncallable(address string) error {
	rest, ok := strings.CutPrefix(address, uncallablePrefix)
	if !ok {
		return ErrUncallableAddress
	}
	text, ok := strings.CutSuffix(rest, uncallableSuffix)
	if !ok || len(text) != 26 {
		return ErrUncallableAddress
	}
	b, err := lowerBase32.DecodeString(text)
	if err != nil || len(b) != uncallableBytes || lowerBase32.EncodeToString(b) != text {
		return ErrUncallableAddress
	}
	return nil
}

// Uncallable says whether address is one of a server that can't be
// called.
func Uncallable(address string) bool { return CheckUncallable(address) == nil }

// CheckInbound checks a message's inbound against the address it is
// read with (core sections 5.2 and 9.2): a sender saying it can't be
// called (uncallable) must have an address of that form. address is the
// message's own, or, in a hello without one, the one stored for its
// sender (section 18, point 31). A request that fails this is a bad body;
// an answer, a badly signed one.
func CheckInbound(uncallable bool, address string) error {
	if uncallable {
		return CheckUncallable(address)
	}
	return nil
}

// The hello schedule of a server that can't be called (core sections 7.5
// and 8.3), to each of its peers, suspended ones included: every
// HelloWaiting while the other operator hasn't accepted, for HelloWaitingFor
// after the pair, then every HelloPaired; every HelloPaired once paired;
// every HelloGone once the peer says it doesn't know this server. Each
// wait varies by a fifth either way (Vary).
const (
	HelloWaiting    = 20 * time.Second
	HelloWaitingFor = 24 * time.Hour
	HelloPaired     = 5 * time.Minute
	HelloGone       = time.Hour
)

// HelloWait is how long a server that can't be called waits before its
// next scheduled hello to a peer, before Vary: waiting says the peer's
// operator hasn't accepted yet (the state "theirs" of core section 7.3),
// gone that it said it doesn't know us, sincePair how long ago the pair
// was. A pairing waiting for this server's own operator ("ours") counts
// as paired: nothing on the other side waits for it.
func HelloWait(waiting, gone bool, sincePair time.Duration) time.Duration {
	switch {
	case gone:
		return HelloGone
	case waiting && sincePair < HelloWaitingFor:
		return HelloWaiting
	}
	return HelloPaired
}

// Vary is d give or take a fifth, by r, a number in [0, 1): 0 gives d
// less a fifth, toward 1 d and a fifth. Peers then don't call together.
func Vary(d time.Duration, r float64) time.Duration {
	return d - d/5 + time.Duration(r*float64(2*d/5))
}

// NextHello is HelloWait, varied at random.
func NextHello(waiting, gone bool, sincePair time.Duration) time.Duration {
	return Vary(HelloWait(waiting, gone, sincePair), mathrand.Float64())
}

// Pulled is what a hello answer to a server that can't be called brings
// of the answering server's keys (core sections 12.4 and 12.6), checked:
// a revocation of the key the answer verified under, and a rotation from
// it. Either is nil when the answer carries none, or one that doesn't
// check (a bad member, a signature that doesn't verify, a date more than
// MaxSkew from now): that one changes nothing (section 18, point 34).
//
// They are applied in this order: the revocation (the peer suspended),
// then the rotation (section 12.3, which a suspended peer passes only
// to the key it committed to).
type Pulled struct {
	Revocation *Revocation
	Rotation   *Rotation
}

// Pull reads the statements of a hello answer h, which verified under
// key, at now (Pulled).
func (h Hello) Pull(key ed25519.PublicKey, now time.Time) Pulled {
	var p Pulled
	if h.Revocation != nil {
		if r, err := ReadRevoke(h.Revocation, key, now); err == nil {
			p.Revocation = &r
		}
	}
	if h.Rotation != nil {
		if r, err := ReadRotate(h.Rotation, key, now); err == nil {
			p.Rotation = &r
		}
	}
	return p
}
