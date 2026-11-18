// Package federation is the server side of federation between servers
// (ROADMAP.md milestone 6, SECURITY.md rule 94), in MSFP version 1:
// pairing and its states, the calls and their handlers, which parts of
// MSFP are on with each peer, limits, and connections to paired servers.
// The protocol itself (keys, pairing codes, signatures, strict JSON,
// pins, versions, rotation and revocation statements) is package msfp;
// changes of key, this server's and its peers', are rotation.go.
package federation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/overfin/server/internal/msfp"
	"github.com/overfin/server/internal/outbound"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/overfin/server/internal/tmdb"

	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/textclean"
)

// How pairing goes, between server A, whose operator made the code, and
// server B, whose operator pasted it:
//
//  1. B sends A a signed POST /api/v1/federation/pair with B's address and
//     name, the code's secret's hash (which finds the code) and a proof
//     that B knows the secret (which never travels). A uses the code up,
//     adds B as waiting for A's operator, and answers, signed, with its
//     name. B checks A's answer against the key in the code, and adds A as
//     waiting for A's operator too.
//  2. A's operator compares B's fingerprint with B's operator, then
//     accepts: A tells B, by a signed POST /api/v1/federation/hello. B can
//     also ask A, by the same request, whenever it likes.
//  3. Either side ends it at once: it forgets the other, then tells it.
//
// Every route answers a key it doesn't know with the same plain 404 as any
// missing route, and so does pair for a code that isn't live: nobody
// learns there's anything here without a code.

// Service is federation on this server.
type Service struct {
	Store *store.Store
	// Keys are this server's federation keys, shared across rebuilds.
	Keys *Keys
	// Address is this server's site address, as its codes give it:
	// https://host, without a path (plain http only with the test switch,
	// msfp.AllowPlainHTTPForTests).
	Address string
	// Name is what this server calls itself to its peers.
	Name   func(context.Context) string
	HTTP   *http.Client
	Logger *slog.Logger
	// Now is the clock, time.Now unless a test sets it.
	Now func() time.Time
	// Seen is the server's memory of nonces already accepted, shared
	// across rebuilds (Verifier.Seen).
	Seen *msfp.Nonces

	// Certificates is what this server names of its own certificates to
	// its peers (nil: none).
	Certificates *Certificates

	// Limits are the limits on calls here and the memory of peers that
	// asked to wait, shared across rebuilds; nil makes its own.
	Limits *Limits

	// PeerChanged, when set, is told a peer's ID once its pairing ended,
	// it is no longer paired, or what we offer it changed: its playbacks
	// here end at once (MSFP playback, section 6.3).
	PeerChanged func(peerID string)

	verifier *msfp.Verifier
	mu       sync.Mutex
	asked    map[string]time.Time // CheckSoon's, by peer ID
	// peerConns are the TLS settings and clients for each peer (conns).
	peerConns map[string]*peerConns
	// readings holds a lock per peer ID, for SyncTitles.
	readings sync.Map
	// refused are the parts each peer answered not-supported (MSFP core
	// section 10.4), by peer ID: off with it until its next hello.
	refused map[string]map[string]bool
	// rechecked is when each peer was last asked its versions again after
	// such an answer, by peer ID: at most once an hour.
	rechecked map[string]time.Time
	// told are the peers Maintain sent our commitment in a hello, by ID.
	told map[string]bool
	// former is this server's address before it moved, and movedAt when,
	// while the move lasts (NoteAddress); "" for none.
	former  string
	movedAt time.Time
	// announced is when each peer was last sent a hello for our new
	// address, at its contacts, by ID.
	announced map[string]time.Time
	// confirming are the peers whose new address is being checked, by ID.
	confirming map[string]bool
}

// NewHTTPClient is the client for talking to peers: short timeouts, and
// redirects never followed (a request is signed for one server only).
func NewHTTPClient() *http.Client {
	return &http.Client{
		Transport: outbound.Transport(),
		Timeout:   15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// maxAnswer caps what's read from a peer's answer: a page of titles is
// the largest.
const maxAnswer = 4 << 20

// maxName caps a peer's name.
const maxName = 80

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) host() string {
	u, _ := url.Parse(s.Address)
	return u.Host
}

// Plugin is the federation API, as a gateway plugin.
type Plugin struct{ S *Service }

// Name is the plugin's name.
func (p Plugin) Name() string { return "federation" }

// Register declares the routes other servers call. They're public: the
// caller is a server, proven by its signature, never a person.
func (p Plugin) Register(r *plugin.Registrar) error {
	p.S.verifier = &msfp.Verifier{Host: p.S.host(), Former: p.S.formerHost, Seen: p.S.Seen, Admit: p.S.admit, Forgotten: p.S.forgotten}
	if err := r.HandlePublic("POST /api/v1/federation/pair", SignRefusals(http.HandlerFunc(p.S.pair))); err != nil {
		return err
	}
	if err := r.HandlePublic("POST /api/v1/federation/hello", SignRefusals(http.HandlerFunc(p.S.hello))); err != nil {
		return err
	}
	if err := r.HandlePublic("POST "+rotatePath, SignRefusals(http.HandlerFunc(p.S.rotate))); err != nil {
		return err
	}
	if err := r.HandlePublic("POST "+revokePath, SignRefusals(http.HandlerFunc(p.S.revoke))); err != nil {
		return err
	}
	if err := r.HandlePublic("POST /api/v1/federation/libraries", SignRefusals(http.HandlerFunc(p.S.libraries))); err != nil {
		return err
	}
	return r.HandlePublic("POST /api/v1/federation/titles", SignRefusals(http.HandlerFunc(p.S.titles)))
}

// lim is s.Limits, made the first time when nil.
func (s *Service) lim() *Limits {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Limits == nil {
		s.Limits = &Limits{}
	}
	return s.Limits
}

// changed tells PeerChanged, when set.
func (s *Service) changed(peerID string) {
	if s.PeerChanged != nil {
		s.PeerChanged(peerID)
	}
}

// forgotten says whether a key is a server's whose pairing with this one
// ended.
func (s *Service) forgotten(r *http.Request, key ed25519.PublicKey) bool {
	f, err := s.Store.PeerForgotten(r.Context(), key)
	return err == nil && f
}

// refusalWriter holds a federation route's answer until its handler is
// done: a refusal to a server this one knows, or knew, is then signed
// (the signature covers the body). A stranger's stays the plain 404.
type refusalWriter struct {
	rec  *httptest.ResponseRecorder
	sign func(status int, contentType string, body []byte) string
	// key is what this server signs with toward the caller (signer).
	key *msfp.Key
	// free frees the request's slots (Limits), once its handler is done.
	free func()
}

func (rw *refusalWriter) Header() http.Header         { return rw.rec.Header() }
func (rw *refusalWriter) Write(b []byte) (int, error) { return rw.rec.Write(b) }
func (rw *refusalWriter) WriteHeader(status int)      { rw.rec.WriteHeader(status) }

// SignRefusals wraps a route other servers call, so that its refusals to
// a server this one knows are signed like its answers: nobody between the
// two can fake one ("unknown", "not playable"…). The route's handler
// reads the request with verified (or PeerRequest), which says whom.
func SignRefusals(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &refusalWriter{rec: httptest.NewRecorder()}
		func() {
			// Its slots are freed whatever happens, and a crash is a
			// signed 500 (the server's own recovery, outside, would
			// answer unsigned: no answer, to a paired server).
			defer func() {
				if rw.free != nil {
					rw.free()
				}
			}()
			defer func() {
				if v := recover(); v != nil {
					if v == http.ErrAbortHandler {
						panic(v)
					}
					slog.Error("federation: a route crashed", "path", r.URL.Path, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
					rw.rec = httptest.NewRecorder()
					problem.Send(rw.rec, problem.Internal, "")
				}
			}()
			h.ServeHTTP(rw, r)
		}()
		res := rw.rec
		for k, v := range res.Header() {
			w.Header()[k] = v
		}
		body := res.Body.Bytes()
		if res.Code != http.StatusOK && rw.sign != nil && w.Header().Get(msfp.HeaderSignature) == "" {
			w.Header().Set(msfp.HeaderSignature, rw.sign(res.Code, w.Header().Get("Content-Type"), body))
		}
		w.WriteHeader(res.Code)
		w.Write(body)
	})
}

// signsFor marks w's refusals, and its answer, to be signed with key for
// the request whose nonce it was, when w is a route's (SignRefusals).
func (s *Service) signsFor(w http.ResponseWriter, nonce string, key msfp.Key) {
	if rw, ok := w.(*refusalWriter); ok {
		rw.key = &key
		rw.sign = func(status int, contentType string, body []byte) string {
			return msfp.SignResponse(key, nonce, status, contentType, body)
		}
	}
}

type pairRequest struct {
	Address    string `json:"address"`
	Name       string `json:"name"`
	SecretHash string `json:"secretHash"`
	Proof      string `json:"proof"`
	// NextCommitment is the caller's next key commitment (MSFP core
	// section 12.5). Inbound is read to check its form only: this server
	// calls every peer.
	NextCommitment *string `json:"nextCommitment,omitempty"`
	Inbound        *bool   `json:"inbound,omitempty"`
}

type pairAnswer struct {
	Name           string `json:"name"`
	NextCommitment string `json:"nextCommitment,omitempty"`
}

// helloMessage is the hello this server sends, and answers (MSFP core
// section 9.2); those it receives are read by msfp.ReadHello.
type helloMessage struct {
	// State is the sender's view: "paired", "waiting", or "ended".
	State string `json:"state"`
	// Core and Parts are the versions of MSFP it speaks (ourVersions).
	Core  []int            `json:"core,omitempty"`
	Parts map[string][]int `json:"parts,omitempty"`
	// Certificates are the pins of the certificates the sender's address
	// serves (Pin), for the other's connections to accept: none when it
	// couldn't tell.
	Certificates []string `json:"certificates,omitempty"`
	// Address is the sender's address.
	Address string `json:"address,omitempty"`
	// NextCommitment is the sender's next key commitment, while the peer
	// holds none from pair or a rotation (MSFP core section 12.5).
	NextCommitment string `json:"nextCommitment,omitempty"`
}

// ourVersions is what this server speaks of MSFP (core section 10): core
// 1, and the parts catalog, playback, media and video at version 1. It
// has no music.
var ourVersions = msfp.Versions{Core: []int{1}, Parts: map[string][]int{
	msfp.PartCatalog: {1}, msfp.PartPlayback: {1}, msfp.PartMedia: {1}, msfp.PartVideo: {1}}}

// OurVersions is what this server speaks of MSFP, as its hellos list it.
func OurVersions() msfp.Versions {
	parts := map[string][]int{}
	for name, list := range ourVersions.Parts {
		parts[name] = slices.Clone(list)
	}
	return msfp.Versions{Core: slices.Clone(ourVersions.Core), Parts: parts}
}

// ourHello is the hello this server sends p, or answers it, in state:
// with its versions, pins and address, and its next key commitment while
// p holds none from us, except for "ended", which says nothing else.
func (s *Service) ourHello(ctx context.Context, p *store.Peer, state string, answering bool) helloMessage {
	if state == helloEnded {
		return helloMessage{State: helloEnded}
	}
	h := helloMessage{State: state, Core: ourVersions.Core, Parts: ourVersions.Parts,
		Certificates: s.ownCertificates(ctx, answering), Address: s.Address}
	if !p.CommitmentTold {
		h.NextCommitment = msfp.Encode64(s.Keys.NextCommitment())
	}
	return h
}

// ownCertificates is what this server names of its certificates in a
// hello.
func (s *Service) ownCertificates(ctx context.Context, answering bool) []string {
	look := s.Certificates.For
	if answering {
		look = s.Certificates.Fresh // a peer asking may have just been refused the old one
	}
	pins, err := look(ctx, s.Address)
	if err != nil {
		s.Logger.Info("federation: this server's certificate couldn't be read at its address", "address", s.Address, "error", err)
	}
	return pins
}

// Hello states.
const (
	helloPaired  = "paired"
	helloWaiting = "waiting"
	helloEnded   = "ended"
)

// admit says whether a signed request's key may be heard: a server this
// one knows (whatever its state: each route checks that), or, on the
// pairing route, anyone while a pairing code is live.
func (s *Service) admit(r *http.Request, key ed25519.PublicKey) (heard, known bool) {
	if p, err := s.Store.PeerByKey(r.Context(), key); err == nil {
		// A key it rotated away from is heard for a while only (MSFP
		// core section 12.4), then gets the plain 404.
		return heardKey(p, key, s.now()), true
	}
	if r.URL.Path != "/api/v1/federation/pair" {
		return false, false
	}
	live, err := s.Store.LivePairingCodes(r.Context())
	return err == nil && live, false
}

// playPath is the call that can start work on the media, which has a
// limit of its own (Limits).
const playPath = "/api/v1/federation/play"

// verified reads and checks a request, within the limits on calls here
// (MSFP core section 8.3); ok is false once it has answered. Before the
// signature, a limit per network address, answered with the plain 404
// of a failed check; then, for a known peer, its own limits (429
// rate-limited) and all peers' (503 server-busy), signed, with
// retryAfter.
func (s *Service) verified(w http.ResponseWriter, r *http.Request) (msfp.Verified, []byte, bool) {
	lim := s.lim()
	if !lim.fromAddr(clientAddr(r), lim.now()) {
		problem.Send(w, problem.NotFound, "")
		return msfp.Verified{}, nil, false
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return msfp.Verified{}, nil, false
	}
	v, err := s.verifier.Check(r, body, s.now())
	if errors.Is(err, msfp.ErrForgotten) {
		// A server whose pairing ended: "unknown", signed.
		s.signsFor(w, v.Nonce, s.Keys.Current())
		problem.Send(w, problem.NotFound, "")
		return msfp.Verified{}, nil, false
	}
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return msfp.Verified{}, nil, false
	}
	key := ""
	if v.Known {
		p, err := s.Store.PeerByKey(r.Context(), v.Key)
		if err != nil {
			problem.Send(w, problem.NotFound, "")
			return msfp.Verified{}, nil, false
		}
		// Signed with the key it knows this server by (MSFP core
		// section 12.4).
		s.signsFor(w, v.Nonce, s.signer(p))
		// A revoked key is heard for rotate and revoke only: anything
		// else gets the signed 404 of a forgotten key (section 12.6).
		if p.Suspended != nil && r.URL.Path != rotatePath && r.URL.Path != revokePath {
			problem.Send(w, problem.NotFound, "")
			return msfp.Verified{}, nil, false
		}
		// A peer still on our old key: told of the new one again, at
		// each contact (section 12.4).
		if p.OurKey == store.OurKeyOld {
			s.rotateSoon(p)
		}
		// Which of our addresses it signed for: one still on our former
		// address told of the new one again, at each contact (section
		// 13.1).
		s.heardAt(r.Context(), p, v.Former)
		key = msfp.Encode64(v.Key)
		if no := lim.call(key, r.URL.Path == playPath, lim.now()); no != nil {
			sendRefusal(w, no)
			return msfp.Verified{}, nil, false
		}
	}
	free, no := lim.slots(r.Context(), key, v.Known)
	if no != nil {
		if v.Known {
			sendRefusal(w, no)
		} else {
			problem.Send(w, problem.NotFound, "")
		}
		return msfp.Verified{}, nil, false
	}
	if rw, ok := w.(*refusalWriter); ok {
		rw.free = free
	} else {
		free() // not a route's: nothing to hold the slots until it's done
	}
	return v, body, true
}

// answer sends a signed JSON answer to the request whose nonce it was:
// never with a null in it (MSFP core section 2.3).
func (s *Service) answer(w http.ResponseWriter, nonce string, v any) {
	body, err := msfp.Marshal(v)
	if err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	s.answerWith(w, nonce, http.StatusOK, body)
}

// answerWith sends a signed JSON answer of status, its body ready.
func (s *Service) answerWith(w http.ResponseWriter, nonce string, status int, body []byte) {
	key := s.Keys.Current()
	if rw, ok := w.(*refusalWriter); ok && rw.key != nil {
		key = *rw.key
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(msfp.HeaderSignature, msfp.SignResponse(key, nonce, status, "application/json", body))
	w.WriteHeader(status)
	w.Write(body)
}

func (s *Service) pair(w http.ResponseWriter, r *http.Request) {
	v, body, ok := s.verified(w, r)
	if !ok {
		return
	}
	var req pairRequest
	if msfp.DecodeJSON(body, &req) != nil || msfp.CheckAddress(req.Address) != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	hash, ok := msfp.Decode64(req.SecretHash, 32)
	var commitment []byte
	if req.NextCommitment != nil {
		var good bool
		commitment, good = msfp.Decode64(*req.NextCommitment, 32)
		ok = ok && good
	}
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	name := cleanName(req.Name, req.Address)
	_, err := s.Store.RedeemPairingCode(r.Context(), hash, v.Key, req.Address, name, commitment, func(secret []byte) bool {
		return msfp.CheckPairingProof(secret, v.Key, req.Address, req.Proof)
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		problem.Send(w, problem.NotFound, "")
		return
	case errors.Is(err, store.ErrPeerExists):
		// Already known: told plainly, since only a server holding a live
		// code and the key it names gets this far.
		w.WriteHeader(http.StatusConflict)
		return
	case err != nil:
		s.Logger.Error("federation: a pairing code couldn't be used", "error", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	s.Logger.Info("federation: a server used a pairing code, waiting for the operator", "name", name)
	s.answer(w, v.Nonce, pairAnswer{Name: s.Name(r.Context()), NextCommitment: msfp.Encode64(s.Keys.NextCommitment())})
}

func (s *Service) hello(w http.ResponseWriter, r *http.Request) {
	v, body, ok := s.verified(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	p, err := s.Store.PeerByKey(ctx, v.Key)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	// state is required, and a string, and every member of its type (MSFP
	// core section 9.2): otherwise nothing is kept.
	msg, err := msfp.ReadHello(body, false)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	switch msg.State {
	case helloEnded:
		// Forgotten without its key remembered: only the side that ends
		// remembers (MSFP core section 7.4, step 3).
		if err := s.Store.PeerEnded(ctx, p.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			problem.Send(w, problem.Internal, "")
			return
		}
		s.changed(p.ID)
		s.Logger.Info("federation: a peer ended the pairing", "name", p.Name)
		s.answer(w, v.Nonce, s.ourHello(ctx, p, helloEnded, true))
		return
	case helloPaired:
		if err := s.theyPaired(ctx, p); err != nil {
			problem.Send(w, problem.Internal, "")
			return
		}
	}
	if err := s.keepHello(ctx, p, msg); err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	if err := s.Store.PeerSeen(ctx, p.ID); err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	p, err = s.Store.PeerByID(ctx, p.ID)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	s.answer(w, v.Nonce, s.ourHello(ctx, p, viewOf(p), true))
}

// keepHello keeps what a peer's signed hello, request or answer, says of
// it: its versions (MSFP core section 10.3), its pins (section 3.2), its
// next key commitment if it never had one (section 12.5), and, for
// another address than the one stored, the check of that address
// (section 13.2).
func (s *Service) keepHello(ctx context.Context, p *store.Peer, h msfp.Hello) error {
	if err := s.keepCertificates(ctx, p, h.Certificates); err != nil {
		return err
	}
	if err := s.announcedAddress(ctx, p, h.Address); err != nil {
		return err
	}
	if h.NextCommitment != nil && p.CommitmentFrom == "" {
		// Trust on first use, signed by its current key: weaker than
		// one from pair, and the panel says so.
		set, err := s.Store.SetPeerCommitment(ctx, p.ID, h.NextCommitment)
		if err != nil {
			return err
		}
		if set {
			p.NextCommitment, p.CommitmentFrom = h.NextCommitment, store.CommitmentHello
			s.Logger.Info("federation: a peer's next key commitment, from its hello", "name", p.Name)
		}
	}
	versions, err := json.Marshal(h.Versions)
	if err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.refused, p.ID) // a new hello: what it speaks is read again
	s.mu.Unlock()
	if string(versions) == p.Versions {
		return nil
	}
	if err := s.Store.SetPeerVersions(ctx, p.ID, string(versions)); err != nil {
		return err
	}
	p.Versions = string(versions)
	if _, _, ok := msfp.Agreed(ourVersions, h.Versions); !ok {
		s.Logger.Info("federation: a peer speaks no version of MSFP's core this server speaks", "name", p.Name)
	}
	return nil
}

// Speaks says whether part is on between this server and p (MSFP core
// section 10.3), by what p's latest signed hello listed: never before its
// first, nor once p answered a call of it not-supported, until its next
// hello.
func (s *Service) Speaks(p *store.Peer, part string) bool {
	var theirs msfp.Versions
	if p.Versions == "" || json.Unmarshal([]byte(p.Versions), &theirs) != nil {
		return false
	}
	_, parts, ok := msfp.Agreed(ourVersions, theirs)
	if _, on := parts[part]; !ok || !on {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.refused[p.ID][part]
}

// SpeaksCore says whether p and this server have a version of MSFP's core
// in common, by p's latest signed hello; true before its first (nothing
// is known yet).
func SpeaksCore(p *store.Peer) bool {
	var theirs msfp.Versions
	if p.Versions == "" || json.Unmarshal([]byte(p.Versions), &theirs) != nil {
		return true
	}
	_, _, ok := msfp.Agreed(ourVersions, theirs)
	return ok
}

// partOf is the part of MSFP each call belongs to (core section 3): pair
// and hello are the core's.
var partOf = map[string]string{
	librariesPath:                 msfp.PartCatalog,
	titlesPath:                    msfp.PartCatalog,
	playPath:                      msfp.PartPlayback,
	"/api/v1/federation/progress": msfp.PartPlayback,
	"/api/v1/federation/stop":     msfp.PartPlayback,
}

// Paths of the catalog's calls.
const (
	librariesPath = "/api/v1/federation/libraries"
	titlesPath    = "/api/v1/federation/titles"
)

// ErrNotSupported is returned for a call of a part of MSFP that isn't on
// with the peer (core section 10.4): it isn't made.
var ErrNotSupported = errors.New("federation: the other server doesn't speak that part of MSFP with this one")

// notSupported answers a call of a part that is off with the caller: a
// signed 404 not-supported (MSFP core section 10.4).
func notSupported(w http.ResponseWriter) {
	problem.Send(w, problem.NotSupported, "")
}

// recheckEvery is how often, at most, a peer that answered not-supported
// is asked its versions again (MSFP core section 10.4).
const recheckEvery = time.Hour

// refusedPart is a peer answering a call of part not-supported: no more
// calls of it until its next hello, which is asked for, at most once an
// hour.
func (s *Service) refusedPart(p *store.Peer, part string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refused == nil {
		s.refused, s.rechecked = map[string]map[string]bool{}, map[string]time.Time{}
	}
	if s.refused[p.ID] == nil {
		s.refused[p.ID] = map[string]bool{}
	}
	s.refused[p.ID][part] = true
	now := s.now()
	if last, ok := s.rechecked[p.ID]; ok && now.Sub(last) < recheckEvery {
		return
	}
	s.rechecked[p.ID] = now
	go func() {
		ctx, cancel := context.WithTimeout(Background(context.Background()), 20*time.Second)
		defer cancel()
		s.Check(ctx, p)
	}()
}

// theyPaired is a peer saying it has accepted: if we were waiting for it,
// or had lost it after a pairing both had accepted, it's paired again.
func (s *Service) theyPaired(ctx context.Context, p *store.Peer) error {
	switch {
	case p.State == store.PeerTheirs:
		_, err := s.Store.SetPeerState(ctx, p.ID, store.PeerTheirs, store.PeerPaired)
		if err == nil {
			s.Logger.Info("federation: paired", "name", p.Name)
		}
		return err
	case p.State == store.PeerGone && p.Paired != nil:
		_, err := s.Store.SetPeerState(ctx, p.ID, store.PeerGone, store.PeerPaired)
		return err
	}
	return nil
}

// viewOf is how we describe a pairing to the peer: paired, or waiting for
// one of the two operators.
func viewOf(p *store.Peer) string {
	if p.State == store.PeerPaired {
		return helloPaired
	}
	return helloWaiting
}

// libraryIDRe is a library ID's form (MSFP catalog, section 2).
var libraryIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// cleanName keeps a peer's name short and printable, or its host when
// it gives none.
// In MSFP core section 9.1's order: each run of white space one space,
// then other control and format characters removed, both ends trimmed,
// cut to maxName code points and trimmed again.
func cleanName(name, address string) string {
	name = strings.Join(strings.FieldsFunc(name, func(r rune) bool { return unicode.Is(unicode.White_Space, r) }), " ")
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if r := []rune(name); len(r) > maxName {
		name = strings.TrimSpace(string(r[:maxName]))
	}
	if name == "" {
		u, _ := url.Parse(address)
		name = u.Host
	}
	return name
}

// ErrUnreachable is returned when a peer couldn't be reached, or answered
// something that isn't a valid, signed answer.
var ErrUnreachable = errors.New("federation: the other server couldn't be reached")

// ErrUnknown is returned when a peer answers that it doesn't know this
// server (a plain 404).
var ErrUnknown = errors.New("federation: the other server doesn't know this one")

// Reply is a peer's signed answer to a call: its status, and for a
// refusal, the problem type its body gives ("" for none).
type Reply struct {
	Status  int
	Problem string
	// RetryAfter is a 429's or 503's retryAfter, seconds (0 for none).
	RetryAfter int
}

// Paths of the calls that accept any certificate (tls.go).
const (
	pairPath  = "/api/v1/federation/pair"
	helloPath = "/api/v1/federation/hello"
)

// call sends a signed POST to a peer and checks its signed answer. pair
// and hello accept any certificate; every other call wants one publicly
// trusted or named by the peer, and asks the peer's hello once for what it
// names now when it gets another.
//
// A peer that asked to wait (a signed 429 or 503 with retryAfter) isn't
// called again before that time; after calls it didn't answer, calls
// made in the background (Background) hold off, longer each time (MSFP
// core section 8.3): ErrBackingOff. A code's server isn't known yet:
// pair always goes.
func (s *Service) call(ctx context.Context, p *store.Peer, path string, in, out any) (Reply, error) {
	// A revoked key: no call at all (MSFP core section 12.6). A peer that
	// didn't follow our change of key in time: none either, it no longer
	// knows any key of ours (section 12.4).
	switch {
	case p.Suspended != nil:
		return Reply{}, ErrSuspended
	case p.OurKey == store.OurKeyLost:
		return Reply{}, ErrKeyLost
	case p.OurKey == store.OurKeyOld && path != rotatePath:
		// Still on our old key: told of the new one first, at each
		// contact (section 12.4).
		p = s.rotateAtContact(ctx, p)
	}
	// A part that is off with it: not called (MSFP core section 10.4).
	part := partOf[path]
	if part != "" && !s.Speaks(p, part) {
		return Reply{}, ErrNotSupported
	}
	key := msfp.Encode64(p.PublicKey)
	lim := s.lim()
	if path != pairPath && !lim.mayCall(key, background(ctx), lim.now()) {
		return Reply{}, ErrBackingOff
	}
	signer := s.signer(p)
	r, err := s.callOnce(ctx, p, path, in, out, signer)
	if errors.Is(err, errNotHeard) && p.OurKey == store.OurKeyOld {
		// Our old key no longer heard there: it may have moved without
		// our seeing its answer. A hello under the new key tells
		// (section 12.4); then the call again, under it.
		// A rotate isn't sent again: under the new key, its statement
		// wouldn't verify, and moved is what it was for.
		if q, moved := s.helloUnderNew(ctx, p); moved && path != rotatePath {
			p = q
			r, err = s.callOnce(ctx, p, path, in, out, s.Keys.Current())
		}
	}
	if path != pairPath && ctx.Err() == nil {
		status := r.Status
		if path == playPath && err == nil && r.RetryAfter == 0 {
			// play's own refusal (its transcodes all taken: a signed
			// 503 without retryAfter) is an answer about that title,
			// not a server overloaded: the viewer is told "busy now",
			// and the other calls (other playbacks' progress among them)
			// don't hold off for it.
			status = http.StatusOK
		}
		lim.heard(key, err == nil, status, r.RetryAfter, lim.now())
	}
	if err == nil && part != "" && r.Status == http.StatusNotFound && r.Problem == string(problem.NotSupported) {
		s.refusedPart(p, part)
		return r, ErrNotSupported
	}
	return r, err
}

// callOnce is one call, signed with key, and once more if the peer's
// certificate was refused and it names another now.
func (s *Service) callOnce(ctx context.Context, p *store.Peer, path string, in, out any, key msfp.Key) (Reply, error) {
	anyCert := path == pairPath || path == helloPath
	r, err := s.exchange(ctx, s.conns(p, anyCert).client, p, path, in, out, key)
	if anyCert || err == nil {
		return r, err
	}
	if q, changed := s.Repin(ctx, p, err); changed {
		return s.exchange(ctx, s.conns(q, false).client, q, path, in, out, key)
	}
	return r, err
}

// errNotHeard is an unsigned 404: the peer doesn't hear the key the call
// was signed with.
var errNotHeard = errors.New("federation: an unsigned 404")

// exchange is one call, by client, signed with key.
func (s *Service) exchange(ctx context.Context, client *http.Client, p *store.Peer, path string, in, out any, key msfp.Key) (Reply, error) {
	body, err := msfp.Marshal(in)
	if err != nil {
		return Reply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(p.Address, "/")+path, bytes.NewReader(body))
	if err != nil {
		return Reply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	nonce := msfp.SignRequest(req, body, key, s.now())
	res, err := client.Do(req)
	if err != nil {
		return Reply{}, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	defer res.Body.Close()
	// No content coding (MSFP core section 3): an answer sent coded, or
	// unpacked on the way (a client that asked for gzip), is refused
	// like a badly signed one.
	if ce := res.Header.Get("Content-Encoding"); res.Uncompressed || ce != "" && !strings.EqualFold(ce, "identity") {
		return Reply{}, fmt.Errorf("%w: a coded answer", ErrUnreachable)
	}
	answer, err := io.ReadAll(io.LimitReader(res.Body, maxAnswer))
	if err != nil {
		return Reply{}, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	// The answer's media type is signed (MSFP core section 6.3): a
	// Content-Type sent twice, or that doesn't read as one, fails the
	// answer like a bad signature.
	contentType, ctErr := msfp.AnswerMediaType(res.Header)
	if res.StatusCode != http.StatusOK {
		// A refusal counts only signed by its key: a server we're paired
		// with, or were, signs its own; anything else may come from
		// someone between the two. A code's server doesn't know us yet:
		// its refusal is a stranger's, plain, and costs only that pairing.
		sigText, _ := msfp.OneHeader(res.Header, msfp.HeaderSignature)
		signed := ctErr == nil && msfp.CheckResponse(p.PublicKey, nonce, res.StatusCode, contentType, answer, sigText) == nil
		if !signed && path != pairPath {
			if res.StatusCode == http.StatusNotFound {
				return Reply{}, fmt.Errorf("%w: %w", ErrUnreachable, errNotHeard)
			}
			return Reply{}, fmt.Errorf("%w: a refusal not signed by its key (%d)", ErrUnreachable, res.StatusCode)
		}
		r := Reply{Status: res.StatusCode}
		if signed {
			// Its problem's type, when it's one: what tells two refusals
			// of the same status apart (play's 409).
			var pd struct {
				Type string `json:"type"`
			}
			if msfp.DecodeAnswer(answer, &pd) == nil && len(pd.Type) <= 64 {
				r.Problem = pd.Type
			}
			if r.Status == http.StatusTooManyRequests || r.Status == http.StatusServiceUnavailable {
				r.RetryAfter = msfp.RetryAfter(answer)
			}
		}
		return r, nil
	}
	if sigText, _ := msfp.OneHeader(res.Header, msfp.HeaderSignature); ctErr != nil ||
		msfp.CheckResponse(p.PublicKey, nonce, res.StatusCode, contentType, answer, sigText) != nil {
		return Reply{}, fmt.Errorf("%w: its answer isn't signed by its key", ErrUnreachable)
	}
	if contentType != "application/json" {
		return Reply{}, fmt.Errorf("%w: an answer that isn't JSON", ErrUnreachable)
	}
	if err := msfp.DecodeAnswer(answer, out); err != nil {
		return Reply{}, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	return Reply{Status: http.StatusOK}, nil
}

// Errors from using a code, for the panel to explain.
var (
	// ErrCodeRefused: the other server doesn't take the code: used,
	// expired, withdrawn, or not one of its own.
	ErrCodeRefused = errors.New("federation: the other server refused the code")
	// ErrOwnCode: the code is this server's own.
	ErrOwnCode = errors.New("federation: that's this server's own code")
)

// UseCode is our operator pasting another server's pairing code: we
// introduce ourselves, and add it as waiting for its operator.
func (s *Service) UseCode(ctx context.Context, text string) (*store.Peer, error) {
	c, err := msfp.ParseCode(text)
	if err != nil {
		return nil, err
	}
	if msfp.SameKey(c.Key, s.Keys.Current().Public()) {
		return nil, ErrOwnCode
	}
	if _, err := s.Store.PeerByKey(ctx, c.Key); err == nil {
		return nil, store.ErrPeerExists
	}
	req := pairRequest{Address: s.Address, Name: s.Name(ctx),
		SecretHash:     msfp.Encode64(store.PairingSecretHash(c.Secret)),
		Proof:          msfp.PairingProof(c.Secret, s.Keys.Current().Public(), s.Address),
		NextCommitment: ptr(msfp.Encode64(s.Keys.NextCommitment()))}
	var answer pairAnswer
	r, err := s.call(ctx, &store.Peer{Address: c.Address, PublicKey: c.Key}, pairPath, req, &answer)
	switch {
	case err != nil:
		return nil, err
	case r.Status == http.StatusConflict:
		return nil, store.ErrPeerExists
	case r.Status != http.StatusOK:
		return nil, ErrCodeRefused
	}
	// Its commitment, when it gave one in its form (MSFP core section
	// 9.1); one that isn't is left out.
	commitment, _ := msfp.Decode64(answer.NextCommitment, 32)
	return s.Store.AddPeer(ctx, c.Key, c.Address, cleanName(answer.Name, c.Address), commitment)
}

// Accept is our operator accepting a server that used our code, having
// compared fingerprints: it's paired, and it's told so (if it can't be
// reached now, it learns when it next asks).
func (s *Service) Accept(ctx context.Context, id string) (*store.Peer, error) {
	ok, err := s.Store.SetPeerState(ctx, id, store.PeerOurs, store.PeerPaired)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, store.ErrNotFound
	}
	p, err := s.Store.PeerByID(ctx, id)
	if err != nil {
		return nil, err
	}
	s.Logger.Info("federation: paired", "name", p.Name)
	return s.Check(ctx, p)
}

// End is our operator ending a pairing, or refusing one: the peer is
// forgotten at once, then told (if it can't be reached, its next request
// gets a 404).
func (s *Service) End(ctx context.Context, id string) error {
	p, err := s.Store.PeerByID(ctx, id)
	if err != nil {
		return err
	}
	if err := s.Store.DeletePeer(ctx, id); err != nil {
		return err
	}
	s.changed(id)
	s.Logger.Info("federation: pairing ended", "name", p.Name)
	var answer json.RawMessage
	if _, err := s.call(ctx, p, helloPath, s.ourHello(ctx, p, helloEnded, false), &answer); err != nil {
		s.Logger.Info("federation: the other server couldn't be told the pairing ended", "name", p.Name, "error", err)
	}
	return nil
}

// Check asks a peer where the pairing stands, telling it ours, and
// returns the peer as it is now. A peer that answers it doesn't know us is
// marked gone; one that can't be reached is left as it was (and the
// error says so).
func (s *Service) Check(ctx context.Context, p *store.Peer) (*store.Peer, error) {
	var raw json.RawMessage
	r, err := s.call(ctx, p, helloPath, s.ourHello(ctx, p, viewOf(p), false), &raw)
	var answer msfp.Hello
	if err == nil && r.Status == http.StatusOK {
		// An answer with a member of the wrong type changes nothing.
		if answer, err = msfp.ReadHello(raw, true); err != nil {
			err = fmt.Errorf("%w: %v", ErrUnreachable, err)
		}
	}
	switch {
	case err != nil:
		return p, err
	case r.Status == http.StatusNotFound:
		if p.State != store.PeerGone {
			if _, err := s.Store.SetPeerState(ctx, p.ID, p.State, store.PeerGone); err != nil {
				return p, err
			}
			s.changed(p.ID)
			s.Logger.Info("federation: a peer no longer knows this server", "name", p.Name)
		}
	case r.Status != http.StatusOK:
		return p, ErrUnreachable
	default:
		if err := s.Store.PeerSeen(ctx, p.ID); err != nil {
			return p, err
		}
		if err := s.keepHello(ctx, p, answer); err != nil {
			return p, err
		}
		if answer.State == helloPaired {
			if err := s.theyPaired(ctx, p); err != nil {
				return p, err
			}
		}
	}
	return s.Store.PeerByID(ctx, p.ID)
}

// NewCode makes a pairing code for our operator to send, noted as note.
func (s *Service) NewCode(ctx context.Context, note string) (string, *store.PairingCode, error) {
	c, err := msfp.NewCode(s.Address, s.Keys.Current().Public(), s.now())
	if err != nil {
		return "", nil, err
	}
	pc, err := s.Store.CreatePairingCode(ctx, c.Secret, cleanNote(note), c.Expires)
	if err != nil {
		return "", nil, err
	}
	return c.String(), pc, nil
}

func cleanNote(note string) string {
	return cleanName(note, "")
}

// checkEvery is how often, at most, a peer is asked where things stand
// while our panel is open on it.
const checkEvery = 20 * time.Second

// CheckSoon asks, in the background, every server whose operator we're
// waiting for: so the panel shows a pairing once they've accepted, without
// anyone pressing anything. Each is asked at most every checkEvery.
func (s *Service) CheckSoon(peers []*store.Peer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.asked == nil {
		s.asked = map[string]time.Time{}
	}
	now := s.now()
	for _, p := range peers {
		if p.State != store.PeerTheirs || now.Sub(s.asked[p.ID]) < checkEvery {
			continue
		}
		s.asked[p.ID] = now
		go func() {
			ctx, cancel := context.WithTimeout(Background(context.Background()), 20*time.Second)
			defer cancel()
			s.Check(ctx, p)
		}()
	}
}

// Sharing, library by library: each side offers its own libraries to each
// peer, and wants some of what the peer offers. Only what one offers and
// the other wants is ever shared; a server only offers what it owns, and
// never passes on what it receives (rule 98).

type librariesAnswer struct {
	// Name is what this server calls itself now (its branding's name),
	// for the caller's site to show.
	Name      string              `json:"name,omitempty"`
	Libraries []store.PeerLibrary `json:"libraries"`
	// Limits is what the caller may use of this server at once, so it never
	// offers what this server would refuse.
	Limits *store.PeerLimits `json:"limits,omitempty"`
}

// maxLibraries caps the libraries a peer's answer may list.
const maxLibraries = 200

// OurLibraries lists this server's libraries, as the index knows them.
func (s *Service) OurLibraries(ctx context.Context) ([]store.PeerLibrary, error) {
	libs, err := s.Store.Libraries(ctx)
	if err != nil {
		return nil, err
	}
	out := []store.PeerLibrary{}
	for _, l := range libs {
		var item struct{ Name, CollectionType string }
		json.Unmarshal(l.Item, &item)
		out = append(out, store.PeerLibrary{ID: l.ID, Name: textclean.Plain(item.Name, maxName), Kind: cleanKind(item.CollectionType)})
	}
	return out, nil
}

// OfferedTo lists our libraries offered to a peer: those we picked that
// still exist.
func (s *Service) OfferedTo(ctx context.Context, peerID string) ([]store.PeerLibrary, error) {
	ids, err := s.Store.PeerOffers(ctx, peerID)
	if err != nil {
		return nil, err
	}
	ours, err := s.OurLibraries(ctx)
	if err != nil {
		return nil, err
	}
	out := []store.PeerLibrary{}
	for _, l := range ours {
		if slices.Contains(ids, l.ID) {
			out = append(out, l)
		}
	}
	return out, nil
}

// libraries answers a paired peer with what we offer it, and nothing
// else: any other caller gets the plain 404.
func (s *Service) libraries(w http.ResponseWriter, r *http.Request) {
	v, body, ok := s.verified(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	p, err := s.Store.PeerByKey(ctx, v.Key)
	if err != nil || p.State != store.PeerPaired || msfp.DecodeJSON(body, &struct{}{}) != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if !s.Speaks(p, msfp.PartCatalog) {
		notSupported(w)
		return
	}
	offered, err := s.OfferedTo(ctx, p.ID)
	if err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	// By the IDs peers know them by, never Jellyfin's (MSFP catalog, section 2).
	ids := make([]string, len(offered))
	for i, l := range offered {
		ids[i] = l.ID
	}
	public, err := s.Store.PublicLibraryIDs(ctx, ids)
	if err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	for i := range offered {
		offered[i].ID = public[offered[i].ID]
	}
	s.Store.PeerSeen(ctx, p.ID)
	answer := librariesAnswer{Name: s.Name(r.Context()), Libraries: offered}
	// The limits are playback's, sent only when it is on (MSFP catalog,
	// section 2).
	if s.Speaks(p, msfp.PartPlayback) {
		answer.Limits = &p.Limits
	}
	s.answer(w, v.Nonce, answer)
}

// ErrNotPaired is returned for sharing with a server not paired yet.
var ErrNotPaired = errors.New("federation: not paired yet")

// TheirLibraries asks a paired peer which of its libraries it offers us.
func (s *Service) TheirLibraries(ctx context.Context, p *store.Peer) ([]store.PeerLibrary, error) {
	if !p.Usable() {
		return nil, ErrNotPaired
	}
	// Each library read on its own: one that isn't one is skipped alone
	// (MSFP catalog, section 2.2).
	var answer struct {
		Name      string            `json:"name"`
		Libraries []json.RawMessage `json:"libraries"`
		Limits    json.RawMessage   `json:"limits"` // ignored when it isn't limits
	}
	r, err := s.call(ctx, p, librariesPath, struct{}{}, &answer)
	switch {
	case err != nil:
		return nil, err
	case r.Status == http.StatusNotFound:
		s.forgotUs(ctx, p)
		return nil, ErrUnknown
	case r.Status != http.StatusOK:
		return nil, ErrUnreachable
	}
	s.Store.PeerSeen(ctx, p.ID)
	var l *store.PeerLimits
	if answer.Limits != nil && msfp.DecodeEntry(answer.Limits, &l) == nil && l != nil && ValidLimits(*l) == nil {
		if err := s.Store.SetTheirLimits(ctx, p.ID, *l); err != nil {
			return nil, err
		}
	}
	out := []store.PeerLibrary{}
	for _, raw := range answer.Libraries {
		if len(out) == maxLibraries {
			break
		}
		var l struct {
			ID   string          `json:"id"`
			Name json.RawMessage `json:"name"`
			Kind json.RawMessage `json:"kind"`
		}
		// Its ID in the form MSFP gives, each once (the first kept).
		if msfp.DecodeEntry(raw, &l) != nil || !libraryIDRe.MatchString(l.ID) ||
			slices.ContainsFunc(out, func(o store.PeerLibrary) bool { return o.ID == l.ID }) {
			continue
		}
		var name, kind string
		_ = json.Unmarshal(l.Name, &name) // not a string: none
		_ = json.Unmarshal(l.Kind, &kind)
		// Cleaned as a server's name is (section 9.1), then as text.
		name = textclean.Plain(cleanName(name, ""), maxName)
		if name == "" {
			name = "?"
		}
		out = append(out, store.PeerLibrary{ID: l.ID, Name: name, Kind: cleanKind(kind)})
	}
	// What it calls itself and its libraries now, for our site's menu.
	if err := s.Store.RenamePeerWants(ctx, p.ID, cleanName(answer.Name, ""), out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetOffers sets which of our libraries we offer a paired peer; IDs that
// aren't our libraries are left out.
func (s *Service) SetOffers(ctx context.Context, p *store.Peer, ids []string) error {
	if p.State != store.PeerPaired {
		return ErrNotPaired
	}
	ours, err := s.OurLibraries(ctx)
	if err != nil {
		return err
	}
	keep := []string{}
	for _, l := range ours {
		if slices.Contains(ids, l.ID) {
			keep = append(keep, l.ID)
		}
	}
	// Unchanged (the panel saves the offers with every edit of a
	// pairing): nothing to do. A change has the peer read the whole list
	// again, and a reading under way would fail.
	had, err := s.Store.PeerOffers(ctx, p.ID)
	if err != nil {
		return err
	}
	same := slices.Clone(keep)
	slices.Sort(same)
	same = slices.Compact(same)
	slices.Sort(had)
	if slices.Equal(same, slices.Compact(had)) {
		return nil
	}
	if err := s.Store.SetPeerOffers(ctx, p.ID, keep); err != nil {
		return err
	}
	// Each gets its public ID when first offered.
	if _, err := s.Store.PublicLibraryIDs(ctx, keep); err != nil {
		return err
	}
	s.changed(p.ID)
	return nil
}

// SetWants sets which of a paired peer's libraries we want, out of those
// it offers us now (so it must be reachable); IDs it doesn't offer are left
// out.
func (s *Service) SetWants(ctx context.Context, p *store.Peer, ids []string) error {
	theirs, err := s.TheirLibraries(ctx, p)
	if err != nil {
		return err
	}
	keep := []store.PeerLibrary{}
	for _, l := range theirs {
		if slices.Contains(ids, l.ID) {
			keep = append(keep, l)
		}
	}
	return s.Store.SetPeerWants(ctx, p.ID, keep)
}

// cleanKind keeps Jellyfin's kinds of library, and "" for anything else.
func cleanKind(kind string) string {
	switch kind {
	case "movies", "tvshows", "music", "musicvideos", "homevideos", "boxsets", "books", "photos", "mixed":
		return kind
	}
	return ""
}

// Titles: a paired server learns which titles the libraries it wants of
// ours hold, by title key, a page at a time; never a file, a path or
// Jellyfin's IDs. Its own server then shows them as one more source.

// maxPeerTitles bounds the titles one reading of a paired server keeps:
// far above any real library's films, series, seasons and episodes, and a
// bound on what a paired server can put in this server's database.
var maxPeerTitles = 1_000_000

// titlesPage is how many titles a page holds at most.
const titlesPage = 5000

// pageWait is the longest a reading of a peer's titles waits when the
// peer asks it to (a 429 or 503 with retryAfter) before reading on.
const pageWait = time.Minute

type titlesRequest struct {
	Libraries []string `json:"libraries"`
	After     string   `json:"after"`
	// Since asks only for what changed since that change of our catalog
	// (the seq a former answer gave); 0, or one we can't answer from,
	// gets the whole list.
	Since int64 `json:"since,omitempty"`
}

// titlesReading is a peer's titlesAnswer as read: each title, and each
// key gone, on its own, so that one that isn't one is skipped alone.
type titlesReading struct {
	Titles  []json.RawMessage `json:"titles"`
	Next    string            `json:"next"`
	Changes bool              `json:"changes"`
	Gone    []json.RawMessage `json:"gone"`
	Seq     int64             `json:"seq"`
}

// readPeerTitle reads one of a peer's titles: false when it isn't an
// object with a key, a kind and a library, all strings; a language tag
// that isn't a string is skipped alone, as a list that isn't one is
// (cleanTags checks the rest).
func readPeerTitle(raw json.RawMessage) (store.PeerTitle, bool) {
	var t struct {
		Key       string          `json:"key"`
		Kind      string          `json:"kind"`
		Library   string          `json:"library"`
		Audio     json.RawMessage `json:"audio"`
		Subtitles json.RawMessage `json:"subtitles"`
	}
	if msfp.DecodeEntry(raw, &t) != nil {
		return store.PeerTitle{}, false
	}
	tags := func(raw json.RawMessage) []string {
		var list []any
		if json.Unmarshal(raw, &list) != nil {
			return nil
		}
		var out []string
		for _, v := range list {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return store.PeerTitle{Key: t.Key, Kind: t.Kind, Library: t.Library, Audio: tags(t.Audio), Subtitles: tags(t.Subtitles)}, true
}

type titlesAnswer struct {
	Titles []store.PeerTitle `json:"titles"`
	// Next is where the next page starts; empty after the last.
	Next string `json:"next,omitempty"`
	// Changes says the answer holds only what changed since Since: Titles
	// are those to keep (new or changed), Gone those no longer held.
	// Without it, Titles is the whole list, a page at a time.
	Changes bool     `json:"changes,omitempty"`
	Gone    []string `json:"gone,omitempty"`
	// Seq is the change our catalog was at: what to ask since next time.
	Seq int64 `json:"seq,omitempty"`
}

// titles answers a paired peer with a page of the titles in the libraries
// it asks for, among those we offer it; anyone else gets the plain 404.
func (s *Service) titles(w http.ResponseWriter, r *http.Request) {
	v, body, ok := s.verified(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	p, err := s.Store.PeerByKey(ctx, v.Key)
	if err != nil || p.State != store.PeerPaired {
		problem.Send(w, problem.NotFound, "")
		return
	}
	var req titlesRequest
	var required struct {
		Libraries *[]string `json:"libraries"` // required (MSFP catalog, section 3)
	}
	if msfp.DecodeJSON(body, &req) != nil || msfp.DecodeJSON(body, &required) != nil || required.Libraries == nil || len(req.After) > 512 {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if !s.Speaks(p, msfp.PartCatalog) {
		notSupported(w)
		return
	}
	// Every title here is a film's or a series': none without video
	// (MSFP catalog, section 5), and no seq, so that a reading once video
	// is on is a whole one.
	if !s.Speaks(p, msfp.PartVideo) {
		s.answer(w, v.Nonce, titlesAnswer{Titles: []store.PeerTitle{}})
		return
	}
	offered, err := s.Store.PeerOffers(ctx, p.ID)
	if err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	public, err := s.Store.PublicLibraryIDs(ctx, offered)
	if err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	ours := map[string]string{}
	for id, pub := range public {
		ours[pub] = id
	}
	// Only libraries we offer it, by the IDs it knows them by: anything
	// else asked is ignored (Jellyfin's IDs included).
	libs := []string{}
	for _, l := range req.Libraries {
		if id, ok := ours[l]; ok && !slices.Contains(libs, id) {
			libs = append(libs, id)
		}
	}
	head, floor, err := s.Store.TitleChanges(ctx)
	if err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	offersSeq, err := s.Store.PeerOffersSeq(ctx, p.ID)
	if err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	// titles is a list, empty or not: never null (MSFP core section 2.3).
	answer := titlesAnswer{Seq: head, Titles: []store.PeerTitle{}}
	// Only what changed, when we still know what changed since then, and
	// what we offer it hasn't changed since.
	// Read since the offers last changed (that change has a number of its
	// own: a reading before it is below it), else a whole list.
	if req.Since > 0 && req.Since >= floor && req.Since <= head && req.Since >= offersSeq {
		present, gone, last, more, err := s.Store.ChangedTitles(ctx, p.ID, libs, req.Since, req.After, titlesPage)
		if err != nil {
			problem.Send(w, problem.Internal, "")
			return
		}
		answer.Changes, answer.Gone = true, gone
		if present != nil {
			answer.Titles = present
		}
		if err := s.Store.PeerTold(ctx, p.ID, present, gone); err != nil {
			problem.Send(w, problem.Internal, "")
			return
		}
		// Paged on the keys looked at, told or not.
		if more {
			answer.Next = last
		}
	} else {
		page, err := s.Store.TitlesInLibraries(ctx, libs, req.After, titlesPage)
		if err != nil {
			problem.Send(w, problem.Internal, "")
			return
		}
		if page != nil {
			answer.Titles = page
		}
		if err := s.Store.PeerTold(ctx, p.ID, page, nil); err != nil {
			problem.Send(w, problem.Internal, "")
			return
		}
		if len(page) == titlesPage {
			answer.Next = page[len(page)-1].Key
		}
	}
	s.Store.PeerSeen(ctx, p.ID)
	s.answer(w, v.Nonce, answer)
}

// SyncTitles reads again which titles a paired peer holds in the
// libraries we want of it, and keeps them, replacing what we knew. With
// nothing wanted, we know of nothing.
// Readings of one peer go one after the other, so one started later (its
// wants just changed) never mixes with an earlier one still paging.
func (s *Service) SyncTitles(ctx context.Context, p *store.Peer) (int, error) {
	if !p.Usable() {
		return 0, ErrNotPaired
	}
	l, _ := s.readings.LoadOrStore(p.ID, &sync.Mutex{})
	l.(*sync.Mutex).Lock()
	defer l.(*sync.Mutex).Unlock()
	wants, err := s.Store.PeerWants(ctx, p.ID)
	if err != nil {
		return 0, err
	}
	libs := []string{}
	// Its titles are films and series: none while video is off with it
	// (MSFP catalog, section 5), and the reading drops what we had.
	if len(wants) > 0 && s.Speaks(p, msfp.PartVideo) {
		// Its names and ours of it, as it gives them now.
		theirs, err := s.TheirLibraries(ctx, p)
		if err != nil {
			return 0, err
		}
		// Only those it still offers: one it no longer does (or whose ID
		// it changed) waits for our operator to choose again, and its
		// titles go with this reading.
		for _, w := range wants {
			if slices.ContainsFunc(theirs, func(l store.PeerLibrary) bool { return l.ID == w.ID }) {
				libs = append(libs, w.ID)
			}
		}
	}
	// Only what changed since we last read, when the peer can tell: the
	// whole list after a first pairing, a change of what we want, or when
	// the peer no longer knows that far back. A reading of fewer than we
	// want is a whole one, and isn't one to ask since next time.
	since, wantsGen, err := s.Store.PeerTitlesRead(ctx, p.ID)
	if err != nil {
		return 0, err
	}
	partial := len(libs) != len(wants)
	if partial {
		since = 0
	}
	// Each page is kept as it comes (a peer may hold any number); with
	// the whole list, those the peer no longer has go once the last page
	// is read.
	reading := store.PeerReading()
	count := 0
	after := ""
	changes, first := false, true
	var seq int64
	for len(libs) > 0 {
		var answer titlesReading
		r, err := s.call(ctx, p, titlesPath, titlesRequest{Libraries: libs, After: after, Since: since}, &answer)
		// Asked to wait a moment: the same page again, once it's time
		// (never sooner); asked longer, the next reading.
		if err == nil && r.Status != http.StatusOK && s.waitTurn(ctx, r, pageWait) {
			continue
		}
		switch {
		case err != nil:
			return 0, err
		case r.Status == http.StatusNotFound:
			s.forgotUs(ctx, p)
			return 0, ErrUnknown
		case r.Status != http.StatusOK:
			return 0, ErrUnreachable
		}
		if len(answer.Titles)+len(answer.Gone) > titlesPage {
			// Past what a page holds (MSFP catalog, section 3.1): a peer
			// filling this server's database, not one paging.
			return 0, fmt.Errorf("%w: a page of more than %d titles", ErrUnreachable, titlesPage)
		}
		if first {
			// The first page says which kind of answer this reading is, and
			// up to which change: later pages are read at the same one.
			changes, seq, first = answer.Changes, answer.Seq, false
		} else if answer.Changes != changes {
			return 0, ErrUnreachable
		}
		page := make([]store.PeerTitle, 0, len(answer.Titles))
		for _, raw := range answer.Titles {
			t, ok := readPeerTitle(raw)
			if !ok {
				continue // not a title: skipped, not the whole page (MSFP catalog, section 3.3)
			}
			// Only what we asked for, and only keys we read the same way:
			// TMDB's, as TMDB numbers them (a "movie/tmdb/0550" would be
			// one more title here, a duplicate nobody can play, and one
			// more page asked of TMDB).
			ref, ok := store.ParseTitleKey(t.Key)
			if !ok || ref.Kind != t.Kind || ref.Key() != t.Key || !slices.Contains(libs, t.Library) || !canonicalTMDB(ref.TMDB()) {
				continue
			}
			t.Audio, t.Subtitles = cleanTags(t.Audio), cleanTags(t.Subtitles)
			page = append(page, t)
		}
		if err := s.Store.PutPeerTitles(ctx, p.ID, reading, page); err != nil {
			return 0, err
		}
		if changes {
			var gone []string
			for _, raw := range answer.Gone {
				var k string
				if json.Unmarshal(raw, &k) == nil && len(k) <= 128 {
					gone = append(gone, k)
				}
			}
			if err := s.Store.DropPeerTitles(ctx, p.ID, gone); err != nil {
				return 0, err
			}
		}
		count += len(page)
		if count > maxPeerTitles {
			return 0, fmt.Errorf("%w: more than %d titles", ErrUnreachable, maxPeerTitles)
		}
		if answer.Next == "" || answer.Next <= after {
			break
		}
		after = answer.Next
	}
	if !changes {
		if err := s.Store.EndPeerTitles(ctx, p.ID, reading); err != nil {
			return 0, err
		}
	}
	// Next time, what changed since; a peer that gives no seq (an older
	// Overfin) gets asked for the whole list each time.
	if partial {
		seq = 0
	}
	if err := s.Store.SetPeerTitlesRead(ctx, p.ID, seq, wantsGen); err != nil {
		return 0, err
	}
	s.Store.PeerSeen(ctx, p.ID)
	return count, nil
}

// syncEvery is how often each peer's titles are read again.
const syncEvery = 30 * time.Minute

// SyncAll reads again every paired peer's titles; one that can't be
// reached keeps what we knew.
// syncTimeout is how long reading one peer's titles may take.
const syncTimeout = 5 * time.Minute

// changesKept is how long our catalog's changes are kept for paired
// servers to ask what changed: one that last read before gets the whole
// list.
const changesKept = 30 * 24 * time.Hour

func (s *Service) SyncAll(ctx context.Context) {
	if err := s.Store.TrimTitleChanges(ctx, s.now().Add(-changesKept)); err != nil {
		s.Logger.Error("federation: trimming the catalog's changes failed", "error", err)
	}
	peers, err := s.Store.Peers(ctx)
	if err != nil {
		s.Logger.Error("federation: listing peers failed", "error", err)
		return
	}
	for _, p := range peers {
		if !p.Usable() {
			continue
		}
		// Each peer in its own time: a slow one doesn't hold the others up.
		peerCtx, cancel := context.WithTimeout(Background(ctx), syncTimeout)
		n, err := s.SyncTitles(peerCtx, p)
		cancel()
		if err != nil {
			s.Logger.Info("federation: a peer's titles couldn't be read", "name", p.Name, "error", err)
			continue
		}
		s.Logger.Info("federation: a peer's titles read", "name", p.Name, "titles", n)
	}
}

// SyncSoon reads a peer's titles again in the background (after what we
// want of it changed).
func (s *Service) SyncSoon(p *store.Peer) {
	go func() {
		ctx, cancel := context.WithTimeout(Background(context.Background()), 10*time.Minute)
		defer cancel()
		if _, err := s.SyncTitles(ctx, p); err != nil {
			s.Logger.Info("federation: a peer's titles couldn't be read", "name", p.Name, "error", err)
		}
	}()
}

// SyncEvery is how often the gateway should call SyncAll.
func SyncEvery() time.Duration { return syncEvery }

// cleanTags keeps a peer's language tags that look like tags ("fr",
// "fr-CA", "es-419"), at most 12, each once.
func cleanTags(tags []string) []string {
	var out []string
	for _, t := range tags {
		if len(out) == 12 {
			break
		}
		if len(t) < 2 || len(t) > 12 || slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, t) }) || strings.IndexFunc(t, func(r rune) bool {
			return !(r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
		}) >= 0 {
			continue
		}
		out = append(out, t)
	}
	return out
}

// ErrLimits is returned for limits out of range.
var ErrLimits = errors.New("federation: streams and transcodes go from 0 to 50, transcodes no more than streams, and the bitrate cap from 0 to 1,000,000 kbps")

// ValidLimits checks a pairing's limits.
func ValidLimits(l store.PeerLimits) error {
	if l.Streams < 0 || l.Streams > 50 || l.Transcodes < 0 || l.Transcodes > l.Streams || l.BitrateKbps < 0 || l.BitrateKbps > 1_000_000 {
		return ErrLimits
	}
	return nil
}

// PeerRequest checks a request from a paired server, for routes other
// packages serve (playback): its signature, and that its key is a paired
// server's. Anyone else gets the plain 404 and ok is false.
func (s *Service) PeerRequest(w http.ResponseWriter, r *http.Request) (p *store.Peer, body []byte, nonce string, ok bool) {
	v, body, ok := s.verified(w, r)
	if !ok {
		return nil, nil, "", false
	}
	p, err := s.Store.PeerByKey(r.Context(), v.Key)
	if err != nil || p.State != store.PeerPaired {
		problem.Send(w, problem.NotFound, "")
		return nil, nil, "", false
	}
	if part := partOf[r.URL.Path]; part != "" && !s.Speaks(p, part) {
		notSupported(w)
		return nil, nil, "", false
	}
	s.Store.PeerSeen(r.Context(), p.ID)
	return p, body, v.Nonce, true
}

// Answer sends a paired server a signed JSON answer to its request.
func (s *Service) Answer(w http.ResponseWriter, nonce string, v any) { s.answer(w, nonce, v) }

// Call sends a paired server a signed request and checks its signed answer:
// the status, and out filled when it's 200.
func (s *Service) Call(ctx context.Context, p *store.Peer, path string, in, out any) (int, error) {
	r, err := s.call(ctx, p, path, in, out)
	return r.Status, err
}

// CallReply is Call, giving a refusal's problem type too.
func (s *Service) CallReply(ctx context.Context, p *store.Peer, path string, in, out any) (Reply, error) {
	return s.call(ctx, p, path, in, out)
}

// canonicalTMDB reports whether id is a TMDB ID as TMDB writes it.
func canonicalTMDB(id string) bool {
	n, err := strconv.Atoi(id)
	return err == nil && strconv.Itoa(n) == id && tmdb.ValidID(n)
}

// forgotUs is a peer refusing us, signed, where a pairing should answer:
// it may have ended the pairing while we couldn't hear it, or not have
// heard yet that we accepted. It's asked where things stand (Check), which
// marks it gone only when it doesn't know us at all, and tells it we're
// paired otherwise; rather than wait for a Check by hand.
func (s *Service) forgotUs(ctx context.Context, p *store.Peer) {
	if p.State == store.PeerGone {
		return
	}
	if _, err := s.Check(ctx, p); err != nil {
		s.Logger.Info("federation: asking a peer that refused us where things stand", "name", p.Name, "err", err)
	}
}
