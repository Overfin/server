package msfp

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

// Every request between paired servers carries four headers (MSFP core
// section 6.1):
//
//	MSFP-Key:       the sender's public key (base64url)
//	MSFP-Date:      when it was signed (Unix seconds)
//	MSFP-Nonce:     16 random bytes, never used twice
//	MSFP-Signature: the sender's Ed25519 signature
//
// The signature covers the method, the server it's meant for, the path,
// the date, the nonce and the body's SHA-256: changing any of them,
// sending it to another server, or sending it again, is caught. The answer
// carries an MSFP-Signature too, from the server answering, over the
// request's nonce, the status, the answer's media type and its body: an
// answer can't be forged or swapped for another request's, even by
// someone who can read and change the traffic (a web certificate alone is
// never trusted).
const (
	HeaderKey       = "MSFP-Key"
	HeaderDate      = "MSFP-Date"
	HeaderNonce     = "MSFP-Nonce"
	HeaderSignature = "MSFP-Signature"
)

// oldHeaders are the signature headers of MSFP's first test version: a
// request that carries one of them is refused (core section 6.1).
var oldHeaders = []string{"Overfin-Key", "Overfin-Date", "Overfin-Nonce", "Overfin-Signature"}

// MaxSkew is how far a request's date may be from the receiver's clock.
// Servers' clocks are set by the network, usually within a second.
const MaxSkew = 5 * time.Minute

// What's signed starts with what it is, so a signature made for one use
// never passes for another (core sections 6.2, 6.3 and 9.1).
const (
	requestLabel  = "msfp-1 request\n"
	responseLabel = "msfp-1 response\n"
	pairingLabel  = "msfp-1 pairing\n"
)

var b64 = base64.RawURLEncoding

// ErrSignature is returned for any request or answer whose signature,
// date, nonce or key is missing or wrong. Why isn't told: the receiver
// answers every one the same way.
var ErrSignature = errors.New("federation: not a valid signature")

// Encode64 writes b as base64url in MSFP's one form (section 2.2): no
// padding.
func Encode64(b []byte) string { return b64.EncodeToString(b) }

// Decode64 decodes base64url in MSFP's one form (section 2.2): no
// padding, no white space, unused bits zero, n bytes (any, for n < 0);
// re-encoding gives the same text.
func Decode64(s string, n int) ([]byte, bool) {
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

// parseDate reads an MSFP-Date (section 2.2).
func parseDate(s string) (int64, bool) {
	if !dateRe.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n <= maxInteger
}

// OneHeader is a signature header sent exactly once, its value without a
// comma (section 2.4); false otherwise.
func OneHeader(h http.Header, name string) (string, bool) {
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

// responseMessage is what an answer's signature covers (core section
// 6.3): the request's nonce, the status, the answer's media type (from its
// Content-Type header, MediaType) and the body's SHA-256.
func responseMessage(nonce string, status int, contentType string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte(responseLabel + nonce + "\n" + strconv.Itoa(status) + "\n" + MediaType(contentType) + "\n" +
		hex.EncodeToString(sum[:]))
}

// MediaType is a Content-Type header as an answer's signature names it
// (core section 6.3): its type and subtype in lower case, without
// parameters or white space; "-" when there is none.
func MediaType(contentType string) string {
	mt, _, _ := strings.Cut(contentType, ";")
	mt = strings.ToLower(strings.TrimSpace(mt))
	if mt == "" {
		return "-"
	}
	return mt
}

// AnswerMediaType reads an answer's Content-Type, as its signature names
// it (core section 6.3): one header or none. None, or an empty one, is
// "-". One sent more than once, or whose value doesn't read as
// type/subtype (each a token of RFC 9110, parameters after a ";" not
// read), is ErrSignature: the answer fails like a badly signed one, so two
// implementations never read different media types from one answer.
func AnswerMediaType(h http.Header) (string, error) {
	values := h.Values("Content-Type")
	switch {
	case len(values) == 0:
		return "-", nil
	case len(values) > 1:
		return "", ErrSignature
	}
	value := strings.Trim(values[0], " \t")
	if value == "" {
		return "-", nil
	}
	mt, _, _ := strings.Cut(value, ";")
	typ, sub, ok := strings.Cut(strings.TrimRight(mt, " \t"), "/")
	if !ok || !isToken(typ) || !isToken(sub) {
		return "", ErrSignature
	}
	return strings.ToLower(typ + "/" + sub), nil
}

// isToken says whether s is a token of RFC 9110 (section 5.6.2): one or
// more of its tchar.
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
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

// SignResponse is the signature to put in an answer's MSFP-Signature, for
// the request whose nonce it was: contentType is the answer's
// Content-Type header ("" for none).
func SignResponse(k Key, nonce string, status int, contentType string, body []byte) string {
	return b64.EncodeToString(k.sign(responseMessage(nonce, status, contentType, body)))
}

// CheckResponse checks an answer's signature against the key the
// requester knows the other server by: contentType is the Content-Type
// header the answer came with ("" for none).
func CheckResponse(peer ed25519.PublicKey, nonce string, status int, contentType string, body []byte, signature string) error {
	sig, ok := Decode64(signature, ed25519.SignatureSize)
	if !ok || !verify(peer, responseMessage(nonce, status, contentType, body), sig) {
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
	// No query (MSFP core section 6.4, step 1): a call with one is refused
	// like a bad signature, even one signed over it.
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		return Verified{}, ErrSignature
	}
	// Never beside the first test version's headers (section 6.1).
	for _, h := range oldHeaders {
		if _, ok := r.Header[http.CanonicalHeaderKey(h)]; ok {
			return Verified{}, ErrSignature
		}
	}
	// Each header once, each value in its one form (MSFP section 2.4).
	keyText, ok1 := OneHeader(r.Header, HeaderKey)
	date, ok2 := OneHeader(r.Header, HeaderDate)
	nonce, ok3 := OneHeader(r.Header, HeaderNonce)
	sigText, ok4 := OneHeader(r.Header, HeaderSignature)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return Verified{}, ErrSignature
	}
	// A key MSFP accepts (section 2.1), wherever it's learnt: here for
	// pair, and checked again (from memory) on every other request.
	key, ok := Decode64(keyText, ed25519.PublicKeySize)
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
	if _, ok := Decode64(nonce, 16); !ok {
		return Verified{}, ErrSignature
	}
	sig, ok := Decode64(sigText, ed25519.SignatureSize)
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
	// seen before a restart, replayed after it). There's no
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
	got, ok := Decode64(proof, sha256.Size)
	if !ok {
		return false
	}
	want, _ := b64.DecodeString(PairingProof(secret, redeemer, address))
	return hmac.Equal(got, want)
}

// SecretHash is a pairing code's secret as the redeeming server names it
// (pair's secretHash, section 9.1): SHA-256 of the secret.
func SecretHash(secret []byte) []byte {
	sum := sha256.Sum256(secret)
	return sum[:]
}

// SameKey reports whether two public keys are the same.
func SameKey(a, b ed25519.PublicKey) bool { return bytes.Equal(a, b) }
