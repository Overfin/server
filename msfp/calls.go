package msfp

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/overfin/server/msfp/wire"
)

// This server's calls to its peers (MSFP core sections 3, 6 and 8):
// signed POSTs, their answers checked against the key the peer is known
// by, no redirects, no content coding, no proxy, 15 seconds each.

// defaultBase is where the federation calls are, unless a peer's
// discovery document says otherwise (MSFP core section 11).
const defaultBase = wire.DefaultBase

// maxAnswer caps what is read of an answer (MSFP core section 3).
const maxAnswer = 4 << 20

// callTimeout is a call's time in all; mediaHeaderTime a media fetch's
// time to its answer's headers (MSFP media section 2.4).
const (
	callTimeout     = 15 * time.Second
	mediaHeaderTime = 60 * time.Second
)

// The calls of the core.
const (
	callPair  = "pair"
	callHello = "hello"
)

// errNotHeard is an unsigned refusal: the peer doesn't hear the key the
// request was signed with (wrapped in ErrUnreachable).
var errNotHeard = errors.New("an unsigned refusal")

// errNoConnection is a call that couldn't connect: no route, refused,
// timed out, a name that doesn't resolve, TLS failing; but a certificate
// refused, or a destination this server refuses (wrapped in
// ErrUnreachable).
var errNoConnection = errors.New("couldn't connect")

// reply is a peer's signed answer: its status, and for a refusal the
// problem type its body gives ("" for none) and its retryAfter.
type reply struct {
	status     int
	problem    string
	retryAfter int
}

// call sends p a signed call ("pair", "hello"…) with the body in, and
// reads its signed 200 answer into out. A refusal that counts (signed,
// or any refusal of pair) is a reply, not an error. background marks the
// calls this server makes by itself: after calls the peer didn't
// answer, they hold off (MSFP core section 8.3).
func (e *Engine) call(ctx context.Context, p *PeerRecord, call string, in, out any, background bool) (reply, error) {
	switch {
	case call != callPair && p.Inbound != "" && p.Inbound != InboundYes:
		// A peer that can't be called: no connection at all (MSFP core
		// section 7.5).
		return reply{}, ErrUncallable
	case !p.Suspended.IsZero() && !(call == callHello && background && e.saysUncallable()):
		// Nothing is asked of a revoked key (MSFP core section 12.6), but
		// the scheduled hello of a server that can't be called, so that a
		// rotation can still reach it.
		return reply{}, ErrSuspended
	case p.OurKey == KeyLost && call != callPair:
		// It never moved to our new key, and knows no key of ours now
		// (MSFP core section 12.4).
		return reply{}, ErrKeyLost
	}
	if core, _ := e.agreed(p); core == 0 && call != callHello && call != callPair {
		return reply{}, ErrNotSupported
	}
	key := wire.Encode64(p.Key)
	if call != callPair && !e.limits.mayCall(key, background, e.now()) {
		return reply{}, ErrBackingOff
	}
	if p.OurKey == KeyOld && call != callRotate && call != callPair {
		// Still on our old key: told of the new one first, at each contact
		// (MSFP core section 12.4).
		p = e.rotateAtContact(ctx, p, background)
	}
	if call != callHello && call != callPair && notMoved(p, e.ourHost()) {
		// Still on our former address: told of the new one, at each
		// contact (MSFP core section 13.1, step 2).
		e.announceSoon(p)
	}
	r, err := e.exchange(ctx, p, call, in, out, e.signerFor(p))
	if call != callPair {
		e.calledPeer(p, err)
	}
	if errors.Is(err, errNotHeard) && p.OurKey == KeyOld {
		// Our old key no longer heard there: it may have moved without our
		// seeing its answer. A hello under the new key tells; then the
		// call again, under it. A rotate isn't sent again: under the new
		// key its statement wouldn't verify, and moved is what it was for.
		if q, moved := e.helloUnderNew(ctx, p); moved && call != callRotate {
			p = q
			r, err = e.exchange(ctx, p, call, in, out, e.current())
		}
	}
	if call != callPair && ctx.Err() == nil {
		e.limits.heard(key, err == nil, r.status, r.retryAfter, e.now())
	}
	return r, err
}

// exchange is one call, signed with key.
func (e *Engine) exchange(ctx context.Context, p *PeerRecord, call string, in, out any, key wire.Key) (reply, error) {
	body, err := marshal(in)
	if err != nil {
		return reply{}, ErrInternal.with(err)
	}
	if wire.Uncallable(p.Address) {
		// An address that never resolves (MSFP core section 5.2).
		return reply{}, ErrUncallable
	}
	base := cmp.Or(p.Base, defaultBase)
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Address+base+call, bytes.NewReader(body))
	if err != nil {
		return reply{}, ErrInternal.with(err)
	}
	req.Header.Set("Content-Type", "application/json")
	nonce, err := wire.SignRequestFrom(req, body, key, e.now(), e.cfg.Random)
	if err != nil {
		return reply{}, ErrInternal.with(err)
	}
	anyCert := call == callPair || call == callHello
	res, err := e.conns.client(p, anyCert).Do(req)
	if err != nil {
		switch {
		case errors.Is(err, ErrRefusedDestination):
			return reply{}, ErrRefusedDestination.with(errors.New(p.Address))
		case errors.Is(err, wire.ErrUntrustedCertificate):
			return reply{}, ErrCertificateRefused.with(errors.New(p.Address))
		}
		// The client's error quotes the URL: fine here (no ticket in a
		// federation call's), but kept short. It couldn't connect, as a
		// hello answer's callsFailed says (MSFP core section 7.5).
		return reply{}, ErrUnreachable.with(fmt.Errorf("%w: %w", errNoConnection, unwrapURL(err)))
	}
	defer res.Body.Close()
	// No content coding (MSFP core section 3): refused like a badly
	// signed answer.
	if ce := res.Header.Get("Content-Encoding"); res.Uncompressed || ce != "" && !strings.EqualFold(ce, "identity") {
		return reply{}, ErrUnreachable.with(errors.New("a coded answer"))
	}
	answer, err := io.ReadAll(io.LimitReader(res.Body, maxAnswer+1))
	if err != nil {
		return reply{}, ErrUnreachable.with(err)
	}
	if len(answer) > maxAnswer {
		return reply{}, ErrUnreachable.with(errors.New("an answer over 4 MiB"))
	}
	contentType, ctErr := wire.AnswerMediaType(res.Header)
	sigText, _ := wire.OneHeader(res.Header, wire.HeaderSignature)
	signed := ctErr == nil && wire.CheckResponse(p.Key, nonce, res.StatusCode, contentType, answer, sigText) == nil
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusAccepted {
		// A refusal counts only signed by the peer's key; but pair's,
		// whose maker doesn't know us yet (MSFP core section 6.6).
		if !signed && call != callPair {
			return reply{status: res.StatusCode}, ErrUnreachable.with(fmt.Errorf("%w: %d", errNotHeard, res.StatusCode))
		}
		r := reply{status: res.StatusCode}
		if signed {
			var pd struct {
				Type string `json:"type"`
			}
			if wire.DecodeAnswer(answer, &pd) == nil && len(pd.Type) <= 64 {
				r.problem = pd.Type
			}
			if r.status == http.StatusTooManyRequests || r.status == http.StatusServiceUnavailable {
				r.retryAfter = wire.RetryAfter(answer)
			}
		}
		return r, nil
	}
	if !signed {
		return reply{}, ErrUnreachable.with(errors.New("an answer not signed by its key"))
	}
	if contentType != "application/json" {
		return reply{}, ErrUnreachable.with(errors.New("an answer that isn't JSON"))
	}
	if err := wire.DecodeAnswer(answer, out); err != nil {
		return reply{}, ErrUnreachable.with(err)
	}
	return reply{status: res.StatusCode}, nil
}

// unwrapURL is a client's error without its URL.
func unwrapURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// peerClients are the HTTP clients for calls to peers: one that accepts
// any certificate (pair and hello, MSFP core section 3.2), and one per
// peer that accepts a publicly trusted certificate or one the peer named,
// made again when its address or pins change.
type peerClients struct {
	e *Engine

	mu      sync.Mutex
	anyCert *http.Client
	byPeer  map[string]pinnedClient
	// media are the clients of media fetches, by peer: the same
	// certificates, no time bound in all (a fetch lasts as long as its
	// playback), 60 seconds to the answer's headers.
	media map[string]pinnedClient
}

type pinnedClient struct {
	sig    string // its address and pins, as it was made for
	client *http.Client
}

// client gives the client for a call to p.
func (c *peerClients) client(p *PeerRecord, anyCert bool) *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if anyCert {
		if c.anyCert == nil {
			c.anyCert = c.newClient("", nil, true, false)
		}
		return c.anyCert
	}
	if c.byPeer == nil {
		c.byPeer = map[string]pinnedClient{}
	}
	return c.pinned(c.byPeer, p, false)
}

// mediaClient gives the client for media fetches from p: a trusted or
// named certificate only (MSFP media section 1).
func (c *peerClients) mediaClient(p *PeerRecord) *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.media == nil {
		c.media = map[string]pinnedClient{}
	}
	return c.pinned(c.media, p, true)
}

// pinned is p's client in clients, made again when its address or pins
// changed.
func (c *peerClients) pinned(clients map[string]pinnedClient, p *PeerRecord, media bool) *http.Client {
	sig := p.Address + " " + strings.Join(p.Pins, " ")
	if pc, ok := clients[p.ID]; ok && pc.sig == sig {
		return pc.client
	}
	if old, ok := clients[p.ID]; ok {
		old.client.CloseIdleConnections()
	}
	host := ""
	if u, err := url.Parse(p.Address); err == nil {
		host = u.Hostname()
	}
	cl := c.newClient(host, p.Pins, false, media)
	clients[p.ID] = pinnedClient{sig: sig, client: cl}
	return cl
}

// newClient is a client whose connections go through the Dialer, check
// the certificate as wire.VerifyPeer says, never follow redirects, never
// ask for compression and use no proxy.
func (c *peerClients) newClient(host string, pins []string, anyCert, media bool) *http.Client {
	cfg := &tls.Config{
		// Checked by VerifyConnection instead, which Go runs either way.
		InsecureSkipVerify: true,
		VerifyConnection:   wire.VerifyPeer(nil, c.e.now, host, pins, anyCert),
	}
	dial := c.e.cfg.Dialer
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dial.Dial(ctx, network, addr)
		},
		TLSClientConfig:     cfg,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
		DisableCompression:  true,
	}
	timeout := callTimeout
	if media {
		tr.ResponseHeaderTimeout, timeout = mediaHeaderTime, 0
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
