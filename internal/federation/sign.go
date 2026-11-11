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
	"net/http"
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
// the traffic (a web certificate alone is never trusted, rule 65).
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
	sig, err := b64.DecodeString(signature)
	if err != nil || len(peer) != ed25519.PublicKeySize ||
		!ed25519.Verify(peer, responseMessage(nonce, status, body), sig) {
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
}

// Verified is what a valid request proves.
type Verified struct {
	// Key is the sender's public key, checked: whether it belongs to a
	// paired server is the caller's to look up.
	Key   ed25519.PublicKey
	Nonce string
}

// Check verifies r, whose body is body, at now. It checks only that the
// request was signed by the key it names, for this server, recently, and
// never seen before: who that key belongs to is the caller's question.
func (v *Verifier) Check(r *http.Request, body []byte, now time.Time) (Verified, error) {
	key, err := b64.DecodeString(r.Header.Get(HeaderKey))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return Verified{}, ErrSignature
	}
	date := r.Header.Get(HeaderDate)
	when, err := strconv.ParseInt(date, 10, 64)
	if err != nil {
		return Verified{}, ErrSignature
	}
	if d := now.Sub(time.Unix(when, 0)); d > MaxSkew || d < -MaxSkew {
		return Verified{}, ErrSignature
	}
	nonce := r.Header.Get(HeaderNonce)
	if n, err := b64.DecodeString(nonce); err != nil || len(n) != 16 {
		return Verified{}, ErrSignature
	}
	sig, err := b64.DecodeString(r.Header.Get(HeaderSignature))
	if err != nil {
		return Verified{}, ErrSignature
	}
	msg := requestMessage(r.Method, v.Host, r.URL.RequestURI(), date, nonce, body)
	if !ed25519.Verify(key, msg, sig) {
		return Verified{}, ErrSignature
	}
	// Only once the signature is good, and the key one this server hears,
	// is the nonce remembered: nobody can fill the memory with nonces, or
	// keys, they made up.
	known := true
	if v.Admit != nil {
		var heard bool
		if heard, known = v.Admit(r, ed25519.PublicKey(key)); !heard {
			return Verified{}, ErrSignature
		}
	}
	if v.Seen == nil {
		v.Seen = &Nonces{}
	}
	if !v.Seen.add(string(key)+nonce, now, known) {
		return Verified{}, ErrSignature
	}
	return Verified{Key: ed25519.PublicKey(key), Nonce: nonce}, nil
}

// maxNonces caps the nonces remembered: past it (after clearing the old
// ones), requests are refused until room comes back, rather than the
// memory growing.
const maxNonces = 100_000

// maxStrangerNonces caps those of strangers' requests (servers pairing).
const maxStrangerNonces = 1_000

// Nonces remembers the nonces of the requests accepted within the last
// 2×MaxSkew, each with the key that signed it: any older request is
// refused by its date anyway.
type Nonces struct {
	mu        sync.Mutex
	seen      map[string]time.Time // known servers'
	strangers map[string]time.Time // others', heard while a code is live
	last      time.Time            // when the old ones were last cleared
}

func (n *Nonces) add(nonce string, now time.Time, known bool) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.seen == nil {
		n.seen, n.strangers = map[string]time.Time{}, map[string]time.Time{}
	}
	m, max := n.seen, maxNonces
	if !known {
		m, max = n.strangers, maxStrangerNonces
	}
	if now.Sub(n.last) > time.Minute || len(m) >= max {
		for _, each := range []map[string]time.Time{n.seen, n.strangers} {
			for k, t := range each {
				if now.Sub(t) > 2*MaxSkew {
					delete(each, k)
				}
			}
		}
		n.last = now
	}
	// A nonce is refused if either memory has it.
	_, dup := n.seen[nonce]
	_, dupStranger := n.strangers[nonce]
	if dup || dupStranger || len(m) >= max {
		return false
	}
	m[nonce] = now
	return true
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
	got, err := b64.DecodeString(proof)
	if err != nil {
		return false
	}
	want, _ := b64.DecodeString(PairingProof(secret, redeemer, address))
	return hmac.Equal(got, want)
}

// SameKey reports whether two public keys are the same.
func SameKey(a, b ed25519.PublicKey) bool { return bytes.Equal(a, b) }
