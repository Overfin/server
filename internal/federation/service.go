package federation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

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
	Key   Key
	// Address is this server's site address, as its codes give it:
	// http(s)://host, without a path.
	Address string
	// Name is what this server calls itself to its peers.
	Name   func(context.Context) string
	HTTP   *http.Client
	Logger *slog.Logger
	// Now is the clock, time.Now unless a test sets it.
	Now func() time.Time
	// Seen is the server's memory of nonces already accepted, shared
	// across rebuilds (Verifier.Seen).
	Seen *Nonces

	verifier *Verifier
	mu       sync.Mutex
	asked    map[string]time.Time // CheckSoon's, by peer ID
}

// NewHTTPClient is the client for talking to peers: short timeouts, and
// redirects never followed (a request is signed for one server only).
func NewHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
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
	p.S.verifier = &Verifier{Host: p.S.host(), Seen: p.S.Seen}
	if err := r.HandlePublic("POST /api/v1/federation/pair", http.HandlerFunc(p.S.pair)); err != nil {
		return err
	}
	if err := r.HandlePublic("POST /api/v1/federation/hello", http.HandlerFunc(p.S.hello)); err != nil {
		return err
	}
	if err := r.HandlePublic("POST /api/v1/federation/libraries", http.HandlerFunc(p.S.libraries)); err != nil {
		return err
	}
	return r.HandlePublic("POST /api/v1/federation/titles", http.HandlerFunc(p.S.titles))
}

type pairRequest struct {
	Address    string `json:"address"`
	Name       string `json:"name"`
	SecretHash string `json:"secretHash"`
	Proof      string `json:"proof"`
}

type pairAnswer struct {
	Name string `json:"name"`
}

type helloMessage struct {
	// State is the sender's view: "paired", "waiting", or "ended".
	State string `json:"state"`
}

// Hello states.
const (
	helloPaired  = "paired"
	helloWaiting = "waiting"
	helloEnded   = "ended"
)

// verified reads and checks a request; ok is false once it has answered.
func (s *Service) verified(w http.ResponseWriter, r *http.Request) (Verified, []byte, bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return Verified{}, nil, false
	}
	v, err := s.verifier.Check(r, body, s.now())
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return Verified{}, nil, false
	}
	return v, body, true
}

// answer sends a signed JSON answer to the request whose nonce it was.
func (s *Service) answer(w http.ResponseWriter, nonce string, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(HeaderSignature, SignResponse(s.Key, nonce, http.StatusOK, body))
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

func (s *Service) pair(w http.ResponseWriter, r *http.Request) {
	v, body, ok := s.verified(w, r)
	if !ok {
		return
	}
	var req pairRequest
	if json.Unmarshal(body, &req) != nil || checkAddress(req.Address) != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	hash, err := b64.DecodeString(req.SecretHash)
	if err != nil || len(hash) != 32 {
		problem.Send(w, problem.NotFound, "")
		return
	}
	name := cleanName(req.Name, req.Address)
	_, err = s.Store.RedeemPairingCode(r.Context(), hash, v.Key, req.Address, name, func(secret []byte) bool {
		return CheckPairingProof(secret, v.Key, req.Address, req.Proof)
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
	s.answer(w, v.Nonce, pairAnswer{Name: s.Name(r.Context())})
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
	var msg helloMessage
	if json.Unmarshal(body, &msg) != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	switch msg.State {
	case helloEnded:
		if err := s.Store.DeletePeer(ctx, p.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			problem.Send(w, problem.Internal, "")
			return
		}
		s.Logger.Info("federation: a peer ended the pairing", "name", p.Name)
		s.answer(w, v.Nonce, helloMessage{State: helloEnded})
		return
	case helloPaired:
		if err := s.theyPaired(ctx, p); err != nil {
			problem.Send(w, problem.Internal, "")
			return
		}
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
	s.answer(w, v.Nonce, helloMessage{State: viewOf(p)})
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

// cleanName keeps a peer's name short and printable, or its host when
// it gives none.
func cleanName(name, address string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, name)
	name = strings.Join(strings.Fields(name), " ")
	if r := []rune(name); len(r) > maxName {
		name = string(r[:maxName])
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

// call sends a signed POST to a peer and checks its signed answer.
func (s *Service) call(ctx context.Context, address string, peer ed25519.PublicKey, path string, in, out any) (int, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(address, "/")+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	nonce := SignRequest(req, body, s.Key, s.now())
	res, err := s.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer res.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(res.Body, maxAnswer))
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if res.StatusCode != http.StatusOK {
		// Refusals aren't signed: a 404 is the same for everyone.
		return res.StatusCode, nil
	}
	if CheckResponse(peer, nonce, res.StatusCode, answer, res.Header.Get(HeaderSignature)) != nil {
		return 0, fmt.Errorf("%w: its answer isn't signed by its key", ErrUnreachable)
	}
	if err := json.Unmarshal(answer, out); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	return http.StatusOK, nil
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
	c, err := ParseCode(text)
	if err != nil {
		return nil, err
	}
	if SameKey(c.Key, s.Key.Public()) {
		return nil, ErrOwnCode
	}
	if _, err := s.Store.PeerByKey(ctx, c.Key); err == nil {
		return nil, store.ErrPeerExists
	}
	req := pairRequest{Address: s.Address, Name: s.Name(ctx),
		SecretHash: b64.EncodeToString(store.PairingSecretHash(c.Secret)),
		Proof:      PairingProof(c.Secret, s.Key.Public(), s.Address)}
	var answer pairAnswer
	status, err := s.call(ctx, c.Address, c.Key, "/api/v1/federation/pair", req, &answer)
	switch {
	case err != nil:
		return nil, err
	case status == http.StatusConflict:
		return nil, store.ErrPeerExists
	case status != http.StatusOK:
		return nil, ErrCodeRefused
	}
	return s.Store.AddPeer(ctx, c.Key, c.Address, cleanName(answer.Name, c.Address))
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
	s.Logger.Info("federation: pairing ended", "name", p.Name)
	var answer helloMessage
	if _, err := s.call(ctx, p.Address, p.PublicKey, "/api/v1/federation/hello", helloMessage{State: helloEnded}, &answer); err != nil {
		s.Logger.Info("federation: the other server couldn't be told the pairing ended", "name", p.Name, "error", err)
	}
	return nil
}

// Check asks a peer where the pairing stands, telling it ours, and
// returns the peer as it is now. A peer that answers it doesn't know us is
// marked gone; one that can't be reached is left as it was (and the
// error says so).
func (s *Service) Check(ctx context.Context, p *store.Peer) (*store.Peer, error) {
	var answer helloMessage
	status, err := s.call(ctx, p.Address, p.PublicKey, "/api/v1/federation/hello", helloMessage{State: viewOf(p)}, &answer)
	switch {
	case err != nil:
		return p, err
	case status == http.StatusNotFound:
		if p.State != store.PeerGone {
			if _, err := s.Store.SetPeerState(ctx, p.ID, p.State, store.PeerGone); err != nil {
				return p, err
			}
			s.Logger.Info("federation: a peer no longer knows this server", "name", p.Name)
		}
	case status != http.StatusOK:
		return p, ErrUnreachable
	default:
		if err := s.Store.PeerSeen(ctx, p.ID); err != nil {
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
	c, err := NewCode(s.Address, s.Key.Public(), s.now())
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
	note = strings.Join(strings.Fields(note), " ")
	if r := []rune(note); len(r) > maxName {
		note = string(r[:maxName])
	}
	return note
}

// LoadKey reads this server's federation key, making it the first time.
func LoadKey(ctx context.Context, st *store.Store) (Key, error) {
	seed, ok, err := st.FederationSeed(ctx)
	if err != nil {
		return Key{}, err
	}
	if !ok {
		if seed, err = st.KeepFederationSeed(ctx, NewKey().Seed()); err != nil {
			return Key{}, err
		}
	}
	return KeyFromSeed(seed)
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
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			s.Check(ctx, p)
		}()
	}
}

// Sharing, library by library: each side offers its own libraries to each
// peer, and wants some of what the peer offers. Only what one offers and
// the other wants is ever shared; a server only offers what it owns, and
// never passes on what it receives (rule 65).

type librariesAnswer struct {
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
	v, _, ok := s.verified(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	p, err := s.Store.PeerByKey(ctx, v.Key)
	if err != nil || p.State != store.PeerPaired {
		problem.Send(w, problem.NotFound, "")
		return
	}
	offered, err := s.OfferedTo(ctx, p.ID)
	if err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	s.Store.PeerSeen(ctx, p.ID)
	s.answer(w, v.Nonce, librariesAnswer{Libraries: offered, Limits: &p.Limits})
}

// ErrNotPaired is returned for sharing with a server not paired yet.
var ErrNotPaired = errors.New("federation: not paired yet")

// TheirLibraries asks a paired peer which of its libraries it offers us.
func (s *Service) TheirLibraries(ctx context.Context, p *store.Peer) ([]store.PeerLibrary, error) {
	if p.State != store.PeerPaired {
		return nil, ErrNotPaired
	}
	var answer librariesAnswer
	status, err := s.call(ctx, p.Address, p.PublicKey, "/api/v1/federation/libraries", struct{}{}, &answer)
	switch {
	case err != nil:
		return nil, err
	case status == http.StatusNotFound:
		return nil, ErrUnknown
	case status != http.StatusOK:
		return nil, ErrUnreachable
	}
	s.Store.PeerSeen(ctx, p.ID)
	if l := answer.Limits; l != nil && ValidLimits(*l) == nil {
		if err := s.Store.SetTheirLimits(ctx, p.ID, *l); err != nil {
			return nil, err
		}
	}
	out := []store.PeerLibrary{}
	for _, l := range answer.Libraries {
		if len(out) == maxLibraries {
			break
		}
		if l.ID == "" || len(l.ID) > 64 {
			continue
		}
		name := textclean.Plain(l.Name, maxName)
		if name == "" {
			name = "?"
		}
		out = append(out, store.PeerLibrary{ID: l.ID, Name: name, Kind: cleanKind(l.Kind)})
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
	return s.Store.SetPeerOffers(ctx, p.ID, keep)
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

// titlesPage is how many titles a page holds at most.
const titlesPage = 5000

// maxPeerTitles caps how many titles we keep of one peer.
const maxPeerTitles = 500_000

type titlesRequest struct {
	Libraries []string `json:"libraries"`
	After     string   `json:"after"`
}

type titlesAnswer struct {
	Titles []store.PeerTitle `json:"titles"`
	// Next is where the next page starts; empty after the last.
	Next string `json:"next,omitempty"`
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
	if json.Unmarshal(body, &req) != nil || len(req.After) > 512 {
		problem.Send(w, problem.NotFound, "")
		return
	}
	offered, err := s.Store.PeerOffers(ctx, p.ID)
	if err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	// Only libraries we offer it: anything else asked is ignored.
	libs := []string{}
	for _, l := range req.Libraries {
		if slices.Contains(offered, l) && !slices.Contains(libs, l) {
			libs = append(libs, l)
		}
	}
	page, err := s.Store.TitlesInLibraries(ctx, libs, req.After, titlesPage)
	if err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	answer := titlesAnswer{Titles: page}
	if len(page) == titlesPage {
		answer.Next = page[len(page)-1].Key
	}
	s.Store.PeerSeen(ctx, p.ID)
	s.answer(w, v.Nonce, answer)
}

// SyncTitles reads again which titles a paired peer holds in the
// libraries we want of it, and keeps them, replacing what we knew. With
// nothing wanted, we know of nothing.
func (s *Service) SyncTitles(ctx context.Context, p *store.Peer) (int, error) {
	if p.State != store.PeerPaired {
		return 0, ErrNotPaired
	}
	wants, err := s.Store.PeerWants(ctx, p.ID)
	if err != nil {
		return 0, err
	}
	libs := []string{}
	for _, w := range wants {
		libs = append(libs, w.ID)
	}
	all := []store.PeerTitle{}
	after := ""
	for len(libs) > 0 {
		var answer titlesAnswer
		status, err := s.call(ctx, p.Address, p.PublicKey, "/api/v1/federation/titles", titlesRequest{Libraries: libs, After: after}, &answer)
		switch {
		case err != nil:
			return 0, err
		case status == http.StatusNotFound:
			return 0, ErrUnknown
		case status != http.StatusOK:
			return 0, ErrUnreachable
		}
		for _, t := range answer.Titles {
			// Only what we asked for, and only keys we read the same way.
			ref, ok := store.ParseTitleKey(t.Key)
			if !ok || ref.Kind != t.Kind || ref.Key() != t.Key || !slices.Contains(libs, t.Library) {
				continue
			}
			t.Audio, t.Subtitles = cleanTags(t.Audio), cleanTags(t.Subtitles)
			all = append(all, t)
		}
		if answer.Next == "" || answer.Next <= after || len(all) >= maxPeerTitles {
			break
		}
		after = answer.Next
	}
	if len(all) > maxPeerTitles {
		all = all[:maxPeerTitles]
	}
	if err := s.Store.SetPeerTitles(ctx, p.ID, all); err != nil {
		return 0, err
	}
	s.Store.PeerSeen(ctx, p.ID)
	return len(all), nil
}

// syncEvery is how often each peer's titles are read again.
const syncEvery = 30 * time.Minute

// SyncAll reads again every paired peer's titles; one that can't be
// reached keeps what we knew.
func (s *Service) SyncAll(ctx context.Context) {
	peers, err := s.Store.Peers(ctx)
	if err != nil {
		s.Logger.Error("federation: listing peers failed", "error", err)
		return
	}
	for _, p := range peers {
		if p.State != store.PeerPaired {
			continue
		}
		n, err := s.SyncTitles(ctx, p)
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
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
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
		if len(t) < 2 || len(t) > 12 || slices.Contains(out, t) || strings.IndexFunc(t, func(r rune) bool {
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
