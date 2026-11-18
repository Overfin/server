package msfp

import (
	"errors"
	"net/http"
)

// Error is an error of the engine: one code of MSFP's shared vocabulary
// (Code), and, for the logs, what happened (Err). Match it with
// errors.Is against the Err* values below:
//
//	if errors.Is(err, msfp.ErrCodeExpired) { … }
//
// The text of Err may hold detail for an operator's logs, never a
// secret (MSFP core section 14.1); it is never sent to a peer.
type Error struct {
	Code ErrorCode
	Err  error
}

// ErrorCode names an error, the same in every MSFP library.
type ErrorCode string

func (e *Error) Error() string {
	if e.Err != nil {
		return "msfp: " + string(e.Code) + ": " + e.Err.Error()
	}
	return "msfp: " + string(e.Code)
}

// Unwrap gives the detail, for errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.Err }

// Is reports whether target is an *Error of the same code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// with is e with detail err, for the logs.
func (e *Error) with(err error) error {
	return &Error{Code: e.Code, Err: err}
}

// A peer's refusals, as the engine reports them for calls it makes (MSFP
// core section 8.2).
var (
	// ErrNotFound: no such thing here, or a peer's signed 404 not-found
	// (no such title, or not paired yet).
	ErrNotFound = &Error{Code: "NotFound"}
	// ErrUnknown: the peer answered a hello with a signed 404: it doesn't
	// know this server, and is now marked gone.
	ErrUnknown = &Error{Code: "Unknown"}
	// ErrNotSupported: the part isn't on with the peer.
	ErrNotSupported = &Error{Code: "NotSupported"}
	// ErrInvalidRequest: the peer answered 400: a bug on this side.
	ErrInvalidRequest = &Error{Code: "InvalidRequest"}
	// ErrAlreadyPeer: the peer already knows this server's key.
	ErrAlreadyPeer = &Error{Code: "AlreadyPeer"}
	// ErrRotationRefused: the peer refused a rotation.
	ErrRotationRefused = &Error{Code: "RotationRefused"}
	// ErrStreamLimitReached: the pairing's streams are all in use.
	ErrStreamLimitReached = &Error{Code: "StreamLimitReached"}
	// ErrNotPlayable: the title can't be played.
	ErrNotPlayable = &Error{Code: "NotPlayable"}
	// ErrPlaybackStopped: the playback is over.
	ErrPlaybackStopped = &Error{Code: "PlaybackStopped"}
	// ErrRateLimited: the peer asked this server to wait; calls hold
	// until then.
	ErrRateLimited = &Error{Code: "RateLimited"}
	// ErrServerBusy: the peer (or this server) is busy now.
	ErrServerBusy = &Error{Code: "ServerBusy"}
	// ErrPeerFailed: the peer answered 500.
	ErrPeerFailed = &Error{Code: "PeerFailed"}
	// ErrUnreachable: no answer, an unsigned or badly signed one, or a
	// refused certificate.
	ErrUnreachable = &Error{Code: "Unreachable"}
)

// The engine's own refusals, before or instead of a call.
var (
	ErrBackingOff  = &Error{Code: "BackingOff"}  // the peer asked to wait, or didn't answer lately
	ErrUncallable  = &Error{Code: "Uncallable"}  // the peer can't be called
	ErrSuspended   = &Error{Code: "Suspended"}   // the peer's key was revoked
	ErrKeyLost     = &Error{Code: "KeyLost"}     // the peer never moved to this server's new key
	ErrNotPaired   = &Error{Code: "NotPaired"}   // the peer isn't paired
	ErrNotWaiting  = &Error{Code: "NotWaiting"}  // the peer isn't waiting for this server's operator
	ErrCodeInvalid = &Error{Code: "CodeInvalid"} // not a pairing code
	ErrCodeExpired = &Error{Code: "CodeExpired"} // the code's 24 hours are over
	ErrOwnCode     = &Error{Code: "OwnCode"}     // the code is this server's own
	ErrCodeRefused = &Error{Code: "CodeRefused"} // the code's server refused it: used, withdrawn, or not its own
	ErrNoCodes     = &Error{Code: "NoCodes"}     // this server can't be called, so it makes no codes

	ErrNoNextKey        = &Error{Code: "NoNextKey"}        // the next key's seed isn't on the server
	ErrNotNextKey       = &Error{Code: "NotNextKey"}       // the text given isn't the committed next key
	ErrNoPending        = &Error{Code: "NoPending"}        // no rotation waits for the operator
	ErrKeyTaken         = &Error{Code: "KeyTaken"}         // the key is another peer's
	ErrRotationUnderWay = &Error{Code: "RotationUnderWay"} // a rotation isn't over yet

	ErrInvalidAddress     = &Error{Code: "InvalidAddress"}     // not an address in MSFP's form
	ErrPlainHTTP          = &Error{Code: "PlainHTTP"}          // an http address: MSFP is HTTPS only
	ErrRefusedDestination = &Error{Code: "RefusedDestination"} // an address the engine never connects to
	ErrCertificateRefused = &Error{Code: "CertificateRefused"} // neither trusted nor named
	ErrMoveFailed         = &Error{Code: "MoveFailed"}         // a new address doesn't answer

	ErrInvalidLimits       = &Error{Code: "InvalidLimits"}       // limits out of range
	ErrRangeNotSatisfiable = &Error{Code: "RangeNotSatisfiable"} // a malformed Range, or out of the file
	ErrSealed              = &Error{Code: "Sealed"}              // a sealed value didn't open
	ErrInternal            = &Error{Code: "Internal"}            // anything else; logged, never shown to a peer
)

// problemType is a problem document's type, as MSFP core section 8.2
// lists them.
type problemType string

const (
	probNotFound     problemType = "not-found"
	probNotSupported problemType = "not-supported"
	probInvalid      problemType = "invalid-request"
	probRotation     problemType = "rotation-refused"
	probStreamLimit  problemType = "stream-limit-reached"
	probNotPlayable  problemType = "not-playable"
	probStopped      problemType = "playback-stopped"
	probRateLimited  problemType = "rate-limited"
	probInternal     problemType = "internal-error"
	probServerBusy   problemType = "server-busy"
)

// problems gives each type its status and title (the titles are
// examples, as section 8.2 says; these are its own).
var problems = map[problemType]struct {
	status int
	title  string
}{
	probNotFound:     {http.StatusNotFound, "Not found"},
	probNotSupported: {http.StatusNotFound, "Not supported"},
	probInvalid:      {http.StatusBadRequest, "Invalid request"},
	probRotation:     {http.StatusConflict, "Rotation refused"},
	probStreamLimit:  {http.StatusConflict, "Too many streams on this account"},
	probNotPlayable:  {http.StatusConflict, "This item can't be played"},
	probStopped:      {http.StatusGone, "Playback was stopped"},
	probRateLimited:  {http.StatusTooManyRequests, "Too many requests"},
	probInternal:     {http.StatusInternalServerError, "Internal error"},
	probServerBusy:   {http.StatusServiceUnavailable, "The server is busy; try again shortly"},
}

// refusalError is what a peer's signed refusal means, as an engine
// error (engine.md section 6). hello's signed 404 is ErrUnknown, and
// is handled by its caller.
func refusalError(status int, typ string) error {
	switch {
	case status == http.StatusNotFound && typ == string(probNotSupported):
		return ErrNotSupported
	case status == http.StatusNotFound:
		return ErrNotFound
	case status == http.StatusBadRequest:
		return ErrInvalidRequest
	case status == http.StatusConflict && typ == string(probRotation):
		return ErrRotationRefused
	case status == http.StatusConflict && typ == string(probNotPlayable):
		return ErrNotPlayable
	case status == http.StatusConflict:
		return ErrStreamLimitReached
	case status == http.StatusGone:
		return ErrPlaybackStopped
	case status == http.StatusTooManyRequests:
		return ErrRateLimited
	case status == http.StatusServiceUnavailable:
		return ErrServerBusy
	case status == http.StatusInternalServerError:
		return ErrPeerFailed
	}
	return ErrUnreachable.with(errors.New("an unexpected status"))
}
