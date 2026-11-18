package msfp

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/overfin/server/msfp/wire"
)

// The routes peers call (MSFP core sections 6 and 8): every request is
// checked in the same order, before its call is handled. Limits by
// network address, then the signature and the nonce, then who the key
// is, then the limits per peer and in all; then the call. Refusals to
// strangers are the host's plain 404; to a server this one knows, or
// knew, they are signed.

// maxBody caps a request's body (MSFP core section 3).
const maxBody = 64 << 10

// response is what a call's handler answers, signed or not by serve.
type response struct {
	// plain: the unsigned 404 of a failed check, from the host's
	// not-found handler. Everything else is signed.
	plain       bool
	status      int
	contentType string // "" for none
	body        []byte
	retryAfter  int // also sent as a Retry-After header
}

// plain404 is the unsigned refusal.
var plain404 = response{plain: true}

// refuse is a signed problem document of type t.
func refuse(t problemType) response {
	return refuseAfter(t, 0)
}

// refuseAfter is a signed problem document with retryAfter (0: none).
func refuseAfter(t problemType, retryAfter int) response {
	p := problems[t]
	var b bytes.Buffer
	fmt.Fprintf(&b, `{"type":%q,"title":%q,"status":%d`, t, p.title, p.status)
	if retryAfter > 0 {
		fmt.Fprintf(&b, `,"retryAfter":%d`, retryAfter)
	}
	b.WriteString("}\n")
	return response{status: p.status, contentType: "application/problem+json", body: b.Bytes(), retryAfter: retryAfter}
}

// answer is a signed 200 with v as JSON, never with a null in it (MSFP
// core section 2.3).
func answer(v any) response {
	body, err := marshal(v)
	if err != nil {
		return refuse(probInternal)
	}
	return response{status: http.StatusOK, contentType: "application/json", body: body}
}

// plainNotFound is the default unsigned 404: MSFP's problem document
// (MSFP core section 3, "Unsigned refusals").
func plainNotFound(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusNotFound)
	io.WriteString(w, `{"type":"not-found","title":"Not found","status":404}`+"\n")
}

// request is a request being handled: what its check proved.
type request struct {
	r    *http.Request
	body []byte
	v    wire.Verified
	// peer is the peer whose key signed it (nil for a stranger heard on
	// pair while a code is live).
	peer *PeerRecord
	// signer is the key the answer is signed with; nil: nothing signed.
	signer *wire.Key
	// ours is the host of ours it was signed for: the current one, or the
	// former one during a move (play's URLs are built on it, MSFP core
	// section 13.1, step 5).
	ours string
}

// FederationHandler serves the calls peers make, under
// <Prefix>/api/v1/federation/: mount it there, on the raw path (never
// behind http.StripPrefix: the signature covers the path as sent), with
// no authentication in front and no compression of its answers.
func (e *Engine) FederationHandler() http.Handler {
	return http.HandlerFunc(e.serveFederation)
}

// The calls served, by their name under the base path, and whether each
// may come from a stranger (pair only) or a suspended key (rotate,
// revoke).
var calls = map[string]func(*Engine, *request) response{
	callPair:   (*Engine).pair,
	callHello:  (*Engine).hello,
	callRotate: (*Engine).rotate,
	callRevoke: (*Engine).revoke,

	callLibraries: (*Engine).libraries,
	callTitles:    (*Engine).titles,
	callPlay:      (*Engine).play,
	callProgress:  (*Engine).progress,
	callStop:      (*Engine).stop,
}

func (e *Engine) serveFederation(w http.ResponseWriter, r *http.Request) {
	base := e.cfg.Prefix + defaultBase
	call, ok := strings.CutPrefix(r.URL.Path, base)
	if !ok || r.Method != http.MethodPost || calls[call] == nil {
		e.cfg.NotFound.ServeHTTP(w, r)
		return
	}
	req := &request{r: r}
	res := e.handle(req, call)
	if res.plain {
		e.cfg.NotFound.ServeHTTP(w, r)
		return
	}
	if res.contentType != "" {
		w.Header().Set("Content-Type", res.contentType)
	}
	if res.retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(res.retryAfter))
	}
	// Every 200 and 202 is signed; a refusal only to a key this server
	// knows, or knew (MSFP core section 6.6).
	if req.signer == nil && (res.status == http.StatusOK || res.status == http.StatusAccepted) {
		k := e.current()
		req.signer = &k
	}
	if req.signer != nil {
		w.Header().Set(wire.HeaderSignature, wire.SignResponse(*req.signer, req.v.Nonce, res.status, res.contentType, res.body))
	}
	w.WriteHeader(res.status)
	w.Write(res.body)
}

// handle checks a request and handles its call. A crash is a signed 500
// to a known peer (the server's own recovery, outside, would answer
// unsigned: no answer, to a peer).
func (e *Engine) handle(req *request, call string) (res response) {
	defer func() {
		if v := recover(); v != nil {
			if v == http.ErrAbortHandler {
				panic(v)
			}
			e.log.Error("msfp: a call crashed", "call", call, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			res = refuse(probInternal)
			if req.signer == nil {
				res = plain404
			}
		}
	}()
	free, res, ok := e.check(req, call)
	if !ok {
		return res
	}
	defer free()
	h := calls[call]
	if h == nil {
		return refuse(probNotSupported)
	}
	return h(e, req)
}

// check reads and checks a request, within the limits on calls here
// (MSFP core sections 6.4 and 8.3); ok is false when res is the answer.
// It gives what frees the request's slots once its call is handled.
func (e *Engine) check(req *request, call string) (free func(), res response, ok bool) {
	r := req.r
	if !e.limits.fromAddr(e.clientAddr(r), e.now()) {
		return nil, plain404, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return nil, plain404, false
	}
	// Content-Type application/json, and no browser's cross-site marker
	// (MSFP core section 3).
	if wire.MediaType(r.Header.Get("Content-Type")) != "application/json" {
		return nil, plain404, false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return nil, plain404, false
	}
	req.body = body
	v, err := e.verifier.Load().Check(r, body, e.now())
	req.v = v
	req.ours = e.ourHost()
	if v.Former {
		req.ours = e.formerHost()
	}
	if errors.Is(err, wire.ErrForgotten) {
		// A server whose pairing ended when our key was stolen: its hello
		// gets the revocation, for 30 days (MSFP core section 12.7).
		res, replaced := e.revocationAnswer(req, call)
		if res != nil {
			return nil, *res, false
		}
		// One whose pairing ended in a replacement, after those 30 days:
		// it can't know our new key, so "unknown" unsigned (section 12.7).
		if replaced {
			return nil, plain404, false
		}
		// A server whose pairing ended: "unknown", signed.
		k := e.current()
		req.signer = &k
		return nil, refuse(probNotFound), false
	}
	if err != nil {
		return nil, plain404, false
	}
	key := ""
	if v.Known {
		p, err := e.peerHeard(r, v.Key)
		if err != nil || p == nil {
			return nil, plain404, false
		}
		req.peer = p
		k := e.signerFor(p)
		req.signer = &k
		// Which of our addresses it calls: one still on our former is told
		// of the new one (MSFP core section 13.1).
		e.heardAt(r.Context(), p, v.Former)
		// A revoked key is heard for rotate and revoke only (MSFP core
		// section 12.6).
		if !p.Suspended.IsZero() && call != callRotate && call != callRevoke {
			return nil, refuse(probNotFound), false
		}
		// A peer still on our old key: told of the new one again, at each
		// contact (section 12.4); one that can't be called, in our answer
		// to its hello.
		if p.OurKey == KeyOld && p.Inbound == InboundYes && p.Suspended.IsZero() {
			e.rotateSoon(p)
		}
		// No core in common: every call but hello is not-supported
		// (sections 10.3, 10.4); pair from a known key gets its own 409.
		if core, _ := e.agreed(p); core == 0 && call != callHello && call != callPair {
			return nil, refuse(probNotSupported), false
		}
		key = wire.Encode64(v.Key)
		if no := e.limits.call(key, call == "play", e.now()); no != nil {
			return nil, refuseAfter(no.problem, no.retryAfter), false
		}
	}
	free, no := e.limits.slots(r.Context(), key, v.Known)
	if no != nil {
		if !v.Known {
			return nil, plain404, false
		}
		return nil, refuseAfter(no.problem, no.retryAfter), false
	}
	return free, response{}, true
}

// admit says whether a key may be heard (MSFP core section 6.5): a
// peer's current key, in any state; its previous one for 10 minutes
// after it rotated; on pair, anyone while a code of ours is live.
func (e *Engine) admit(r *http.Request, key ed25519.PublicKey) (heard, known bool) {
	if p, err := e.peerHeard(r, key); err == nil && p != nil {
		return true, true
	}
	if !strings.HasSuffix(r.URL.Path, "/"+callPair) {
		return false, false
	}
	live := false
	err := e.cfg.Store.Read(r.Context(), func(tx Tx) error {
		codes, err := tx.Codes()
		for _, c := range codes {
			if c.Expires.After(e.now()) {
				live = true
			}
		}
		return err
	})
	return err == nil && live, false
}

// forgotten says whether a key is a server's whose pairing with this
// one ended, and that isn't a peer again; or this server's own current
// key, which it hears only from itself, checking it is reached at its
// new address (answersAt): it gets the signed 404 of a forgotten key.
func (e *Engine) forgotten(r *http.Request, key ed25519.PublicKey) bool {
	if wire.SameKey(key, e.current().Public()) {
		return true
	}
	hash := sha256.Sum256(key)
	found := false
	err := e.cfg.Store.Read(r.Context(), func(tx Tx) error {
		f, err := tx.Forgotten(hash[:])
		found = f != nil
		return err
	})
	return err == nil && found
}

// peerHeard is the peer whose key this is: its current key, or its
// previous one while still heard (MSFP core section 12.4); nil for none.
func (e *Engine) peerHeard(r *http.Request, key []byte) (*PeerRecord, error) {
	var found *PeerRecord
	err := e.cfg.Store.Read(r.Context(), func(tx Tx) error {
		p, err := tx.PeerByKey(key)
		if err != nil || p != nil {
			found = p
			return err
		}
		peers, err := tx.Peers()
		for _, q := range peers {
			if bytes.Equal(q.PreviousKey, key) && e.now().Before(q.PreviousUntil) {
				found = &q
				break
			}
		}
		return err
	})
	return found, err
}
