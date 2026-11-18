package msfp

import (
	"time"

	"github.com/overfin/server/msfp/wire"
)

// The records a Store keeps (host.md section 1). The engine fills every
// field; a store keeps them as they are, in JSON (the tags are for the
// shipped stores) or in columns of its own. Times are UTC; a zero time
// means "never" or "none".

// ServerRecord is this server's own: its keys, its address, its
// settings. There is one.
type ServerRecord struct {
	// Current is the current key's seed, sealed ("msfp current key").
	Current []byte `json:"current"`
	// Next is the next key's seed, sealed ("msfp next key"); nil once the
	// operator exported and deleted it. NextCommitment is SHA-256 of its
	// public key, and stays (MSFP core section 12.5).
	Next           []byte `json:"next,omitempty"`
	NextCommitment []byte `json:"nextCommitment"`
	// Old is the key before Current while peers move from it, sealed
	// ("msfp old key"), since OldSince; OldRevoked: it was revoked first
	// (MSFP core sections 12.4 and 12.7).
	Old        []byte    `json:"old,omitempty"`
	OldSince   time.Time `json:"oldSince,omitzero"`
	OldRevoked bool      `json:"oldRevoked,omitempty"`
	// Revoked is a key revoked with no next key, replaced by Current,
	// sealed ("msfp old key"), since RevokedAt: kept 30 days for the
	// answers to the hellos of the peers whose pairing ended then (MSFP
	// core section 12.7, case 2).
	Revoked   []byte    `json:"revoked,omitempty"`
	RevokedAt time.Time `json:"revokedAt,omitzero"`
	// IDsSecret is the secret of opaque IDs, sealed ("msfp ids secret").
	// It never changes.
	IDsSecret []byte `json:"idsSecret"`
	// Address is the address noted last; Former the one before it, and
	// MovedAt when it changed, while a move lasts (MSFP core section 13.1).
	Address string    `json:"address"`
	Former  string    `json:"former,omitempty"`
	MovedAt time.Time `json:"movedAt,omitzero"`
	// Uncallable is the operator's setting "this server can't be called",
	// and UncallableAddress the https://msfp-<26>.invalid address made
	// for it, kept (MSFP core section 7.5).
	Uncallable        bool   `json:"uncallable,omitempty"`
	UncallableAddress string `json:"uncallableAddress,omitempty"`
}

// PeerState is where a pairing stands (MSFP core section 7.3).
type PeerState string

// The states of a peer.
const (
	// Ours: the peer used our code; our operator hasn't accepted.
	Ours PeerState = "ours"
	// Theirs: we used the peer's code; its operator hasn't accepted.
	Theirs PeerState = "theirs"
	// Paired: both sides accepted.
	Paired PeerState = "paired"
	// Gone: the peer answered, signed, that it doesn't know us.
	Gone PeerState = "gone"
)

// Inbound is whether a peer can be called (MSFP core section 7.5).
type Inbound string

// The values of Inbound.
const (
	InboundYes        Inbound = "yes"
	InboundNo         Inbound = "no"
	InboundConfirming Inbound = "confirming"
)

// PeerRecord is a server this one is paired, or pairing, with.
type PeerRecord struct {
	// Identity.
	//
	// ID is the engine's own, random and stable; peers never see it. Key
	// is the peer's current public key; PreviousKey its key before its
	// latest rotation, still heard until PreviousUntil.
	ID            string    `json:"id"`
	Key           []byte    `json:"key"`
	PreviousKey   []byte    `json:"previousKey,omitempty"`
	PreviousUntil time.Time `json:"previousUntil,omitzero"`
	// Name is the peer's name, cleaned.
	Name    string    `json:"name"`
	Created time.Time `json:"created"`

	// State.
	//
	// WasPaired: it was paired once, so that a gone peer's paired hello
	// brings it back. Paired is when it became paired, Seen when it was
	// last heard from, Suspended when it revoked its key.
	State     PeerState `json:"state"`
	WasPaired bool      `json:"wasPaired,omitempty"`
	Paired    time.Time `json:"paired,omitzero"`
	Seen      time.Time `json:"seen,omitzero"`
	Suspended time.Time `json:"suspended,omitzero"`

	// Address.
	//
	// Address is where to call it, Base its base path ("" for the
	// default, /api/v1/federation/). Move is an address it announced, not
	// confirmed yet. OurAddress is the host of ours its requests were
	// last signed for. Inbound says whether it can be called;
	// CallsFailed, that its answer said our calls to it couldn't connect.
	Address     string    `json:"address"`
	Base        string    `json:"base,omitempty"`
	Move        *PeerMove `json:"move,omitempty"`
	OurAddress  string    `json:"ourAddress,omitempty"`
	Inbound     Inbound   `json:"inbound"`
	CallsFailed bool      `json:"callsFailed,omitempty"`

	// Versions is what its latest signed hello listed (nil before the
	// first); Pins its certificates' pins, from the same.
	Versions *wire.Versions `json:"versions,omitempty"`
	Pins     []string       `json:"pins,omitempty"`

	// Its keys.
	//
	// Commitment is its next key commitment, and CommitmentFrom where it
	// came from ("pair", "rotation"; "" for none). Pending is a rotation
	// to a key with no commitment, waiting for our operator; Rotations the
	// statements kept; Refused a rotation refused, for the operator.
	Commitment     []byte          `json:"commitment,omitempty"`
	CommitmentFrom string          `json:"commitmentFrom,omitempty"`
	Pending        *wire.Rotation  `json:"pending,omitempty"`
	Rotations      []wire.Rotation `json:"rotations,omitempty"`
	Refused        *RefusedKey     `json:"refused,omitempty"`

	// Our keys toward it.
	//
	// OurKey is what it knows us by: "" (our current key), "old" (it
	// hasn't moved to our new one yet) or "lost" (it never did).
	// RotationAnswer is its last answer to our rotate ("pending",
	// "refused" or ""); RotateSent when we last sent one; CommitmentTold,
	// that it holds our next key's commitment.
	OurKey         string    `json:"ourKey,omitempty"`
	RotationAnswer string    `json:"rotationAnswer,omitempty"`
	RotateSent     time.Time `json:"rotateSent,omitzero"`
	CommitmentTold bool      `json:"commitmentTold,omitempty"`

	// Sharing.
	//
	// Offers are the host libraries offered to it, OffersSeq the change
	// number they were set at; Wants are its libraries we want; Limits
	// ours to it, TheirLimits its to us (nil until it said); CatalogRead
	// where our reading of its titles stands.
	Offers      []string        `json:"offers,omitempty"`
	OffersSeq   int64           `json:"offersSeq,omitempty"`
	Wants       []WantedLibrary `json:"wants,omitempty"`
	Limits      Limits          `json:"limits"`
	TheirLimits *Limits         `json:"theirLimits,omitempty"`
	CatalogRead CatalogRead     `json:"catalogRead,omitzero"`
}

// PeerMove is an address a peer announced, being checked (MSFP core
// section 13.2).
type PeerMove struct {
	Address      string    `json:"address"`
	Since        time.Time `json:"since"`
	Tried        time.Time `json:"tried,omitzero"`
	FailingSince time.Time `json:"failingSince,omitzero"`
}

// RefusedKey is a key a peer tried to rotate to, refused.
type RefusedKey struct {
	Key []byte    `json:"key"`
	At  time.Time `json:"at"`
}

// CatalogRead is where the reading of a peer's titles stands: the change
// number read up to, and the wants it was read with.
type CatalogRead struct {
	Seq   int64    `json:"seq,omitempty"`
	Wants []string `json:"wants,omitempty"`
}

// CodeRecord is a pairing code this server made.
type CodeRecord struct {
	ID         string    `json:"id"`
	Secret     []byte    `json:"secret"` // sealed ("msfp code secret")
	SecretHash []byte    `json:"secretHash"`
	Note       string    `json:"note,omitempty"`
	Created    time.Time `json:"created"`
	Expires    time.Time `json:"expires"`
}

// ForgottenKey is a key whose pairing ended (MSFP core section 7.4), by
// its SHA-256: its requests get a signed 404. Revoked: the pairing ended
// when this server's key was revoked with no next key (section 12.7,
// case 2), so its hellos get the revocation.
type ForgottenKey struct {
	KeyHash []byte    `json:"keyHash"`
	At      time.Time `json:"at"`
	Revoked bool      `json:"revoked,omitempty"`
}

// Leftover is a playback this server started on a peer (the peer's
// playback ID), to stop after a restart.
type Leftover struct {
	Peer       string `json:"peer"`
	PlaybackID string `json:"playbackId"`
}
