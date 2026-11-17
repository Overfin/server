package federation

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"maps"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Every request between paired servers carries four headers:
//
//	Overfin-Key:       the sender's public key (URL-safe base64)
//	Overfin-Date:      when it was signed (Unix seconds)
//	Overfin-Nonce:     16 random bytes, never used twice
//	Overfin-Signature: the sender's Ed25519 signature
//
// The signature covers the method, the server it's meant for, the path and
// query, the date, the nonce and the body's SHA-256: changing any of them,
// sending it to another server, or sending it again, is caught. The answer
// carries an Overfin-Signature too, from the server answering, over the
// request's nonce, the status and the body: an answer can't be forged or
// swapped for another request's, even by someone who can read and change
// the traffic (a web certificate alone is never trusted, rule 94).
const (
	HeaderKey       = "Overfin-Key"
	HeaderDate      = "Overfin-Date"
	HeaderNonce     = "Overfin-Nonce"
	HeaderSignature = "Overfin-Signature"
)

// MaxSkew is how far a request's date may be from the receiver's clock.
// Servers' clocks are set by the network, usually within a second.
const MaxSkew = 5 * time.Minute

// What's signed starts with what it is, so a signature made for one use
// never passes for another.
const (
	requestLabel  = "overfin-federation-1 request\n"
	responseLabel = "overfin-federation-1 response\n"
	pairingLabel  = "overfin-federation-1 pairing\n"
)

var b64 = base64.RawURLEncoding

// ErrSignature is returned for any request or answer whose signature,
// date, nonce or key is missing or wrong. Why isn't told: the receiver
// answers every one the same way.
var ErrSignature = errors.New("federation: not a valid signature")

// decode64 decodes base64url in MSFP's one form (section 2.1): no
// padding, no white space, unused bits zero, n bytes (any, for n < 0);
// re-encoding gives the same text.
func decode64(s string, n int) ([]byte, bool) {
	b, err := b64.DecodeString(s)
	if err != nil || (n >= 0 && len(b) != n) || b64.EncodeToString(b) != s {
		return nil, false
	}
	return b, true
}

// maxInteger is the largest integer MSFP writes: 2^53 − 1.
const maxInteger = 1<<53 - 1

// dateRe is a positive decimal integer in MSFP's one form: no sign, no
// leading zero, nothing else.
var dateRe = regexp.MustCompile(`^[1-9][0-9]{0,15}$`)

// parseDate reads an Overfin-Date (section 2.1).
func parseDate(s string) (int64, bool) {
	if !dateRe.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n <= maxInteger
}

// oneHeader is a signature header sent exactly once, its value without a
// comma (section 2.1); false otherwise.
func oneHeader(h http.Header, name string) (string, bool) {
	v := h.Values(name)
	if len(v) != 1 || strings.Contains(v[0], ",") {
		return "", false
	}
	return v[0], true
}

func requestMessage(method, host, target, date, nonce string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte(requestLabel + method + "\n" + strings.ToLower(host) + "\n" + target + "\n" +
		date + "\n" + nonce + "\n" + hex.EncodeToString(sum[:]))
}

func responseMessage(nonce string, status int, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte(responseLabel + nonce + "\n" + strconv.Itoa(status) + "\n" + hex.EncodeToString(sum[:]))
}

// SignRequest signs req, whose body is body, with k. It returns the nonce,
// which the answer's signature covers (CheckResponse).
func SignRequest(req *http.Request, body []byte, k Key, now time.Time) string {
	n := make([]byte, 16)
	rand.Read(n)
	nonce := b64.EncodeToString(n)
	date := strconv.FormatInt(now.Unix(), 10)
	msg := requestMessage(req.Method, req.URL.Host, req.URL.RequestURI(), date, nonce, body)
	req.Header.Set(HeaderKey, b64.EncodeToString(k.Public()))
	req.Header.Set(HeaderDate, date)
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderSignature, b64.EncodeToString(k.sign(msg)))
	return nonce
}

// SignResponse is the signature to put in an answer's Overfin-Signature,
// for the request whose nonce it was.
func SignResponse(k Key, nonce string, status int, body []byte) string {
	return b64.EncodeToString(k.sign(responseMessage(nonce, status, body)))
}

// CheckResponse checks an answer's signature against the key the
// requester knows the other server by.
func CheckResponse(peer ed25519.PublicKey, nonce string, status int, body []byte, signature string) error {
	sig, ok := decode64(signature, ed25519.SignatureSize)
	if !ok || !verify(peer, responseMessage(nonce, status, body), sig) {
		return ErrSignature
	}
	return nil
}

// Verifier checks the requests a server receives.
type Verifier struct {
	// Host is this server's own host, as its address names it: a request
	// signed for another server is refused, even if sent here.
	Host string
	// Seen remembers the nonces already accepted. Shared by the Verifiers
	// a server makes over its life (each settings change rebuilds them),
	// so a request can't be replayed across one; nil for one of its own.
	Seen *Nonces
	// Admit, when set, says whether a key may be heard at all (heard: a
	// known server's, or anyone's while a pairing code is live) and whether
	// it's a known server's: asked once the signature is good, before the
	// nonce is remembered, so a stranger signing with keys of their own
	// fills no memory, and strangers heard while a code is live fill only a
	// small one of their own, never the paired servers'.
	Admit func(r *http.Request, key ed25519.PublicKey) (heard, known bool)
	// Forgotten, when set, says whether a key Admit doesn't hear is a
	// server's whose pairing ended: its request, signed and new, gets
	// ErrForgotten (and a signed "unknown"), not ErrSignature.
	Forgotten func(r *http.Request, key ed25519.PublicKey) bool
}

// ErrForgotten: a valid request from a server whose pairing with this one
// ended. Its Verified says who, for the answer to be signed.
var ErrForgotten = errors.New("federation: a server this one no longer knows")

// Verified is what a valid request proves.
type Verified struct {
	// Key is the sender's public key, checked: whether it belongs to a
	// paired server is the caller's to look up.
	Key   ed25519.PublicKey
	Nonce string
	// Known: a server this one knows (paired or not yet), not a stranger
	// heard while a code is live.
	Known bool
}

// Check verifies r, whose body is body, at now. It checks only that the
// request was signed by the key it names, for this server, recently, and
// never seen before: who that key belongs to is the caller's question.
func (v *Verifier) Check(r *http.Request, body []byte, now time.Time) (Verified, error) {
	// Each header once, each value in its one form (MSFP section 2.1).
	keyText, ok1 := oneHeader(r.Header, HeaderKey)
	date, ok2 := oneHeader(r.Header, HeaderDate)
	nonce, ok3 := oneHeader(r.Header, HeaderNonce)
	sigText, ok4 := oneHeader(r.Header, HeaderSignature)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return Verified{}, ErrSignature
	}
	// A key MSFP accepts (section 2.1), wherever it's learnt: here for
	// pair, and checked again (from memory) on every other request.
	key, ok := decode64(keyText, ed25519.PublicKeySize)
	if !ok || !ValidKey(key) {
		return Verified{}, ErrSignature
	}
	when, ok := parseDate(date)
	if !ok {
		return Verified{}, ErrSignature
	}
	if d := now.Sub(time.Unix(when, 0)); d > MaxSkew || d < -MaxSkew {
		return Verified{}, ErrSignature
	}
	if v.Seen != nil && v.Seen.before(when) {
		return Verified{}, ErrSignature
	}
	if _, ok := decode64(nonce, 16); !ok {
		return Verified{}, ErrSignature
	}
	sig, ok := decode64(sigText, ed25519.SignatureSize)
	if !ok {
		return Verified{}, ErrSignature
	}
	msg := requestMessage(r.Method, v.Host, r.URL.RequestURI(), date, nonce, body)
	if !verify(key, msg, sig) {
		return Verified{}, ErrSignature
	}
	// Only once the signature is good, and the key one this server hears,
	// is the nonce remembered: nobody can fill the memory with nonces, or
	// keys, they made up.
	known := true
	if v.Admit != nil {
		var heard bool
		if heard, known = v.Admit(r, ed25519.PublicKey(key)); !heard {
			if v.Forgotten == nil || !v.Forgotten(r, ed25519.PublicKey(key)) {
				return Verified{}, ErrSignature
			}
			// Remembered as a stranger's: a replay gets the plain 404.
			if v.Seen == nil {
				v.Seen = &Nonces{}
			}
			if !v.Seen.add(string(key), nonce, now, false) {
				return Verified{}, ErrSignature
			}
			return Verified{Key: ed25519.PublicKey(key), Nonce: nonce}, ErrForgotten
		}
	}
	if v.Seen == nil {
		v.Seen = &Nonces{}
	}
	if !v.Seen.add(string(key), nonce, now, known) {
		return Verified{}, ErrSignature
	}
	return Verified{Key: ed25519.PublicKey(key), Nonce: nonce, Known: known}, nil
}

// maxNoncesPerKey caps the nonces remembered for one known server's key:
// past it (after clearing the old ones), its requests are refused until
// room comes back, rather than the memory growing. Each key has its own,
// so one server, however fast it asks, never crowds out another (keys are
// known only once an operator paired them, so they are few). Its limits
// (limits.go) let a server through at most peerBurst + peerRate × 2 ×
// MaxSkew requests in the time a nonce is kept: 12,200; requests its
// limits refuse are remembered too, so a server asking much faster than
// allowed is the one refused.
const maxNoncesPerKey = 20_000

// maxStrangerNonces caps those of strangers' requests (servers pairing).
const maxStrangerNonces = 1_000

// Nonces remembers the nonces of the requests accepted within the last
// 2×MaxSkew, each with the key that signed it: any older request is
// refused by its date anyway.
type Nonces struct {
	// Since is when the memory began (NewNonces): a request signed before
	// it is refused, since its nonce may have been seen before (a request
	// read on an http pairing, replayed after a restart). There's no
	// allowance for the other server's clock: one running behind is
	// refused for that much after a start, as each second allowed would
	// let a request from that second before the start be replayed. Zero:
	// no such check.
	Since time.Time

	mu        sync.Mutex
	seen      map[string]map[string]time.Time // known servers', by key, then nonce
	strangers map[string]time.Time            // others' (key + nonce), heard while a code is live
	last      time.Time                       // when the old ones were last cleared
}

// NewNonces is a memory of nonces that begins at now: give it the time
// the process started, before any request is heard.
func NewNonces(now time.Time) *Nonces { return &Nonces{Since: now} }

// before says whether a request signed at when (Unix seconds) may predate
// the memory. A date is a whole second: one in the second the memory began
// counts as before it, unless that second began exactly then.
func (n *Nonces) before(when int64) bool {
	return time.Unix(when, 0).Before(n.Since)
}

// add remembers a request's nonce, signed by key: false when it was seen
// already, or there's no room for it.
func (n *Nonces) add(key, nonce string, now time.Time, known bool) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.seen == nil {
		n.seen, n.strangers = map[string]map[string]time.Time{}, map[string]time.Time{}
	}
	full := len(n.strangers) >= maxStrangerNonces
	if known {
		full = len(n.seen[key]) >= maxNoncesPerKey
	}
	// Full, the old ones are cleared once a second at most: a server
	// asking past its room doesn't make each request go through them all.
	if now.Sub(n.last) > time.Minute || full && now.Sub(n.last) >= time.Second {
		n.forget(now)
	}
	// A nonce is refused if either memory has it.
	_, dup := n.seen[key][nonce]
	_, dupStranger := n.strangers[key+nonce]
	switch {
	case dup || dupStranger:
		return false
	case !known:
		if len(n.strangers) >= maxStrangerNonces {
			return false
		}
		n.strangers[key+nonce] = now
		return true
	}
	m := n.seen[key]
	if m == nil {
		m = map[string]time.Time{}
		n.seen[key] = m
	}
	if len(m) >= maxNoncesPerKey {
		return false
	}
	m[nonce] = now
	return true
}

// forget clears the nonces old enough to be refused by their date; n.mu
// is held.
func (n *Nonces) forget(now time.Time) {
	old := func(t time.Time) bool { return now.Sub(t) > 2*MaxSkew }
	for key, m := range n.seen {
		maps.DeleteFunc(m, func(_ string, t time.Time) bool { return old(t) })
		if len(m) == 0 {
			delete(n.seen, key)
		}
	}
	maps.DeleteFunc(n.strangers, func(_ string, t time.Time) bool { return old(t) })
	n.last = now
}

// PairingProof proves, without sending it, that the server redeeming a
// pairing code knows the code's secret. It's bound to the redeeming
// server's own key and address: someone who sees the proof go by can't use
// it for a key of their own, and the secret never travels.
func PairingProof(secret []byte, redeemer ed25519.PublicKey, address string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(pairingLabel))
	mac.Write(redeemer)
	mac.Write([]byte("\n" + address))
	return b64.EncodeToString(mac.Sum(nil))
}

// CheckPairingProof checks a PairingProof in constant time.
func CheckPairingProof(secret []byte, redeemer ed25519.PublicKey, address, proof string) bool {
	got, ok := decode64(proof, sha256.Size)
	if !ok {
		return false
	}
	want, _ := b64.DecodeString(PairingProof(secret, redeemer, address))
	return hmac.Equal(got, want)
}

// SameKey reports whether two public keys are the same.
func SameKey(a, b ed25519.PublicKey) bool { return bytes.Equal(a, b) }
