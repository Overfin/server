// Package problem writes error responses as RFC 9457 problem details.
//
// Every error type Overfin can return is listed here, with its HTTP status
// and title, so the API's errors stay consistent and documented in one place.
// Clients branch on Type; Detail is for humans only.
package problem

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/lang"
)

// Type is a stable, machine-readable error name.
type Type string

const (
	NotFound Type = wire.ProblemNotFound
	// NotSupported: a federation call of an MSFP part this server doesn't
	// speak with the caller (MSFP core section 10.4).
	NotSupported     Type = wire.ProblemNotSupported
	NotAuthenticated Type = "not-authenticated"
	OwnerOnly        Type = "owner-only"
	// AccountNotAllowed: the identity may not use this instance (not in
	// the required group, or its account is disabled).
	AccountNotAllowed Type = "account-not-allowed"
	// AccountSetupRequired: no account yet; the site offers to start fresh
	// or to claim an existing media server account.
	AccountSetupRequired Type = "account-setup-required"
	// InvitationRequired: accounts are created by invitation only here, and
	// the visitor has no valid one.
	InvitationRequired Type = "invitation-required"
	// AccountExpired: the operator set an end to this account's access.
	AccountExpired Type = "account-expired"
	AccountExists  Type = "account-exists"
	// ClaimFailed: wrong username or password, deliberately not saying
	// which. ClaimNotAllowed: the credentials were right, but that account
	// can't be adopted (an administrator, or already adopted).
	ClaimFailed Type = "claim-failed"
	// SigninFailed: a wrong password or code, or an unusable link, never
	// saying whether the email has an account.
	SigninFailed     Type = "signin-failed"
	ClaimNotAllowed  Type = "claim-not-allowed"
	CrossSiteRequest Type = "cross-site-request"
	NotPlayable      Type = wire.ProblemNotPlayable
	// DeliveryRefused: the version can't be served the way asked (MSFP
	// playback section 2.5): the player asks for another.
	DeliveryRefused Type = wire.ProblemDeliveryRefused
	// TargetRefused: a target the request names isn't made (MSFP Media
	// playing 2.5: the media type, a codec, the range, a subtitle to
	// burn): the player asks for other targets, another delivery, or
	// none.
	TargetRefused Type = wire.ProblemTargetRefused
	// Gone: a title or a copy held for the caller, and no longer (MSFP
	// Media base 4): the player drops it and reads the title again.
	Gone Type = wire.ProblemGone
	// Unreachable: the media can't be reached now (MSFP Media playing
	// 2.2, step 8): busy now, temporary.
	Unreachable Type = wire.ProblemUnreachable
	// TicketRefused: a ticket refused (MSFP Media base 4).
	TicketRefused       Type = wire.ProblemTicketRefused
	StreamLimitReached  Type = wire.ProblemStreamLimit
	NameTaken           Type = "name-taken"
	ProfileLimitReached Type = "profile-limit-reached"
	ServerBusy          Type = wire.ProblemServerBusy
	// A playback that no longer exists, and why.
	PlaybackTakenOver    Type = "playback-taken-over"
	PlaybackExpired      Type = "playback-expired"
	PlaybackStopped      Type = wire.ProblemStopped
	PlaybackEndedByAdmin Type = "playback-ended-by-admin"
	UnsupportedMediaType Type = "unsupported-media-type"
	InvalidRequest       Type = wire.ProblemInvalid
	// RotationRefused: a federation peer's change of key refused (MSFP
	// core section 9.3).
	RotationRefused Type = wire.ProblemRotationRefused
	InvalidCursor   Type = "invalid-cursor"
	RateLimited     Type = wire.ProblemRateLimited
	// AlreadyRequested: a wish for what a library has whole already.
	AlreadyRequested Type = "already-requested"
	// Wishes: the profile's owner switched its wishing off; its wishlist
	// is full.
	WishesOff    Type = "wishes-off"
	WishlistFull Type = "wishlist-full"
	// PINRequired: the profile has a PIN and none was given. PINIncorrect:
	// a wrong PIN. PINLocked: too many wrong PINs; retryAfter says when
	// another attempt is possible.
	PINRequired  Type = "pin-required"
	PINIncorrect Type = "pin-incorrect"
	PINLocked    Type = "pin-locked"
	// ProfileRequired: this session has no profile chosen yet; the site
	// shows the profile list ("Who's watching?").
	ProfileRequired Type = "profile-required"
	// StepUpRequired: the action needs a fresh login with the identity
	// provider.
	StepUpRequired Type = "step-up-required"
	Internal       Type = wire.ProblemInternal
)

type info struct {
	status int
	title  string
}

// registry holds the status and title of every type of Overfin's own;
// MSFP's (wire.Problem: not-found, invalid-request, the playback
// refusals…) are MSFP's, for this server's players as for its peers. The
// title is the same for every occurrence of a type.
var registry = map[Type]info{
	NotAuthenticated:     {http.StatusUnauthorized, "Not logged in"},
	OwnerOnly:            {http.StatusForbidden, "Only the account owner can do this"},
	AccountNotAllowed:    {http.StatusForbidden, "This account may not use this service"},
	AccountSetupRequired: {http.StatusNotFound, "No account yet"},
	AccountExists:        {http.StatusConflict, "This account already exists"},
	AccountExpired:       {http.StatusForbidden, "Access to this account has ended"},
	InvitationRequired:   {http.StatusForbidden, "An invitation is needed to join"},
	ClaimFailed:          {http.StatusForbidden, "Wrong username or password"},
	SigninFailed:         {http.StatusUnauthorized, "Sign-in failed"},
	ClaimNotAllowed:      {http.StatusForbidden, "This account can't be brought over"},
	CrossSiteRequest:     {http.StatusForbidden, "Cross-site requests are not allowed"},
	NameTaken:            {http.StatusConflict, "This name is already used in this account"},
	ProfileLimitReached:  {http.StatusConflict, "This account has as many profiles as allowed"},
	PlaybackTakenOver:    {http.StatusGone, "Playback continued on another device"},
	PlaybackExpired:      {http.StatusGone, "Playback ended after a period of inactivity"},
	PlaybackEndedByAdmin: {http.StatusGone, "Playback was stopped by an administrator"},
	UnsupportedMediaType: {http.StatusUnsupportedMediaType, "Requests must be JSON"},
	InvalidCursor:        {http.StatusBadRequest, "Invalid cursor"},
	AlreadyRequested:     {http.StatusConflict, "This is here already"},
	WishesOff:            {http.StatusForbidden, "Wishing is switched off for this profile"},
	WishlistFull:         {http.StatusConflict, "The wishlist is full"},
	PINRequired:          {http.StatusForbidden, "This profile is protected by a PIN"},
	PINIncorrect:         {http.StatusForbidden, "Wrong PIN"},
	PINLocked:            {http.StatusForbidden, "Too many wrong PINs; try again later"},
	StepUpRequired:       {http.StatusForbidden, "Please log in again to do this"},
	ProfileRequired:      {http.StatusForbidden, "Choose a profile first"},
}

// Problem is one error response, with the extra fields some types carry.
type Problem struct {
	Type   Type   `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
	// TakenOverBy names the device that took over a playback.
	TakenOverBy string `json:"takenOverBy,omitempty"`
	// RetryAfter is how many seconds to wait before trying again; it is
	// also sent as the Retry-After header.
	RetryAfter int `json:"retryAfter,omitempty"`
	// StepUp and StepUpURL, for step-up-required: what confirms the action
	// ("login"), and where to send the user for it. StepUpURL is empty when
	// the instance offers no step-up.
	StepUp    string `json:"stepUp,omitempty"`
	StepUpURL string `json:"stepUpUrl,omitempty"`
	// Languages, for account-setup-required, are those the new account
	// may choose among (worth asking only when there are several), and
	// Language the one it gets unless it chooses.
	Languages []lang.Choice `json:"languages,omitempty"`
	Language  string        `json:"language,omitempty"`
	// Offers, for account-setup-required, are the other ways modules offer
	// to set an account up, each a member of its own: "claim": "copy", say.
	Offers map[string]string `json:"-"`
}

// Members are the names a problem's own members use, which offers can't.
var Members = []string{"type", "title", "status", "detail", "takenOverBy", "retryAfter", "stepUp", "stepUpUrl", "languages", "language"}

// New returns the problem for t, with an optional human-readable detail.
// The detail must never contain upstream error text or internal details
// (SECURITY.md rule 53).
func New(t Type, detail string) Problem {
	i, ok := lookup(t)
	if !ok {
		// An unregistered type is a programming error; never leak it.
		t, detail = Internal, ""
		i, _ = lookup(Internal)
	}
	return Problem{Type: t, Title: i.title, Status: i.status, Detail: detail}
}

// lookup is a type's status and title: MSFP's, else Overfin's own.
func lookup(t Type) (info, bool) {
	if status, title, ok := wire.Problem(string(t)); ok {
		return info{status, title}, true
	}
	i, ok := registry[t]
	return i, ok
}

// Write sends p as the response.
func Write(w http.ResponseWriter, p Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	if p.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfter))
	}
	w.WriteHeader(p.Status)
	if len(p.Offers) == 0 {
		_ = json.NewEncoder(w).Encode(p)
		return
	}
	b, _ := json.Marshal(p)
	var members map[string]any
	_ = json.Unmarshal(b, &members)
	for name, value := range p.Offers {
		if _, taken := members[name]; !taken {
			members[name] = value
		}
	}
	_ = json.NewEncoder(w).Encode(members)
}

// Send is New followed by Write.
func Send(w http.ResponseWriter, t Type, detail string) {
	Write(w, New(t, detail))
}
