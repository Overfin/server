package federation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// MSFP's version 0 test vectors (https://github.com/C0PT3R/msfp-spec,
// vectors/v0.json), copied in testdata/vectors.json: recomputed here with
// the code, so Overfin keeps speaking the protocol they describe.

type vectorKey struct {
	Seed        string `json:"seed"` // hex
	Public      string `json:"public"`
	Fingerprint string `json:"fingerprint"`
}

type vectorExchange struct {
	Name     string `json:"name"`
	Signer   string `json:"signer"`   // the requester's key, by name
	Answerer string `json:"answerer"` // the answering server's key
	Method   string `json:"method"`
	Host     string `json:"host"`
	Target   string `json:"target"`
	Date     string `json:"date"`
	Nonce    string `json:"nonce"` // the 16 bytes, hex
	// What's computed from the above.
	NonceText string `json:"nonceText"`
	Body      string `json:"body"`
	Message   string `json:"message"`
	Signature string `json:"signature"`
	// The answer.
	Status          int    `json:"status"`
	AnswerBody      string `json:"answerBody"`
	AnswerMessage   string `json:"answerMessage"`
	AnswerSignature string `json:"answerSignature"`
}

type vectors struct {
	Keys map[string]vectorKey `json:"keys"`
	Code struct {
		Key        string `json:"key"` // by name
		Address    string `json:"address"`
		Secret     string `json:"secret"` // hex
		Expires    int64  `json:"expires"`
		Text       string `json:"text"`
		SecretHash string `json:"secretHash"`
	} `json:"code"`
	Proof struct {
		Redeemer string `json:"redeemer"` // by name
		Address  string `json:"address"`
		Value    string `json:"value"`
	} `json:"proof"`
	Exchanges []vectorExchange `json:"exchanges"`
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex %q: %v", s, err)
	}
	return b
}

func TestVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	same := func(what, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s: the code gives %q, vectors.json says %q", what, got, want)
		}
	}

	keys := map[string]Key{}
	for name, k := range v.Keys {
		key, err := KeyFromSeed(unhex(t, k.Seed))
		if err != nil {
			t.Fatal(err)
		}
		keys[name] = key
		same(name+" seed", k.Seed, k.Seed)
		same(name+" public key", b64.EncodeToString(key.Public()), k.Public)
		same(name+" fingerprint", Fingerprint(key.Public()), k.Fingerprint)
	}

	secret := unhex(t, v.Code.Secret)
	code := Code{Address: v.Code.Address, Key: keys[v.Code.Key].Public(), Secret: secret, Expires: time.Unix(v.Code.Expires, 0)}
	same("pairing code", code.String(), v.Code.Text)
	if c, err := ParseCode(v.Code.Text); err != nil || c.Address != code.Address || !SameKey(c.Key, code.Key) ||
		!bytes.Equal(c.Secret, secret) || !c.Expires.Equal(code.Expires) {
		t.Errorf("the pairing code reads back as %+v, %v", c, err)
	}
	same("secret hash", b64.EncodeToString(store.PairingSecretHash(secret)), v.Code.SecretHash)
	same("pairing proof", PairingProof(secret, keys[v.Proof.Redeemer].Public(), v.Proof.Address), v.Proof.Value)

	for _, x := range v.Exchanges {
		signer, answerer := keys[x.Signer], keys[x.Answerer]
		same(x.Name+" nonce", b64.EncodeToString(unhex(t, x.Nonce)), x.NonceText)
		msg := requestMessage(x.Method, x.Host, x.Target, x.Date, x.NonceText, []byte(x.Body))
		same(x.Name+" signed message", string(msg), x.Message)
		same(x.Name+" signature", b64.EncodeToString(signer.sign(msg)), x.Signature)
		same(x.Name+" body", x.Body, x.Body)

		// The request as it arrives, checked as a server checks it.
		req := httptest.NewRequest(x.Method, "http://"+x.Host+x.Target, strings.NewReader(x.Body))
		req.Header.Set(HeaderKey, v.Keys[x.Signer].Public)
		req.Header.Set(HeaderDate, x.Date)
		req.Header.Set(HeaderNonce, x.NonceText)
		req.Header.Set(HeaderSignature, x.Signature)
		date, _ := strconv.ParseInt(x.Date, 10, 64)
		at := time.Unix(date, 0)
		ver := &Verifier{Host: x.Host, Seen: NewNonces(at.Add(-time.Minute))}
		if got, err := ver.Check(req, []byte(x.Body), at); err != nil || !SameKey(got.Key, signer.Public()) {
			t.Errorf("%s: the request is refused: %v", x.Name, err)
		}

		answer := []byte(x.AnswerBody)
		same(x.Name+" answer's signed message", string(responseMessage(x.NonceText, x.Status, answer)), x.AnswerMessage)
		same(x.Name+" answer's signature", SignResponse(answerer, x.NonceText, x.Status, answer), x.AnswerSignature)
		if err := CheckResponse(answerer.Public(), x.NonceText, x.Status, answer, x.AnswerSignature); err != nil {
			t.Errorf("%s: the answer is refused: %v", x.Name, err)
		}
	}
}

// MSFP's version 0 canonical forms (vectors/v0-strict.json, copied in
// testdata/v0-strict.json): addresses, codes, dates, base64url, keys and
// signatures, through the code that reads them.
func TestStrictVectors(t *testing.T) {
	b, err := os.ReadFile("testdata/v0-strict.json")
	if err != nil {
		t.Fatal(err)
	}
	type tc struct {
		Name, Text string
		Valid      bool
	}
	var v struct {
		Addresses, Codes, Dates, Base64url, Keys []tc
		Signatures                               []struct {
			Name, Public, Message, Signature string
			Valid                            bool
		}
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Addresses) == 0 || len(v.Codes) == 0 || len(v.Dates) == 0 || len(v.Base64url) == 0 ||
		len(v.Keys) == 0 || len(v.Signatures) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range v.Keys {
		key, _ := decode64(c.Text, 32)
		if ValidKey(key) != c.Valid {
			t.Errorf("key %s: valid %v", c.Name, !c.Valid)
		}
		// And as a request's Overfin-Key, and a code's key.
		req := httptest.NewRequest("POST", "https://a.example.org/api/v1/federation/pair", strings.NewReader("{}"))
		SignRequest(req, []byte("{}"), NewKey(), time.Now())
		req.Header.Set(HeaderKey, c.Text)
		if _, err := (&Verifier{Host: "a.example.org"}).Check(req, []byte("{}"), time.Now()); err == nil {
			t.Errorf("key %s: a request signed by another key passes", c.Name)
		}
		code := Code{Address: "https://a.example.org", Key: key, Secret: make([]byte, SecretSize), Expires: time.Unix(1800000000, 0)}
		if _, err := ParseCode(code.String()); (err == nil) != c.Valid {
			t.Errorf("key %s in a code: %v", c.Name, err)
		}
	}
	for _, c := range v.Signatures {
		public, _ := decode64(c.Public, 32)
		if got := CheckResponse(public, "", 0, nil, c.Signature) == nil; got {
			t.Errorf("signature %s: an answer's check passes a request's message", c.Name)
		}
		sig, _ := decode64(c.Signature, 64)
		if verify(public, []byte(c.Message), sig) != c.Valid {
			t.Errorf("signature %s: valid %v", c.Name, !c.Valid)
		}
		// Through the check of a request, as it arrives: the message is
		// that of v0.json's hello exchange.
		lines := strings.Split(c.Message, "\n")
		req := httptest.NewRequest(lines[1], "http://"+lines[2]+lines[3], strings.NewReader(""))
		req.Header.Set(HeaderKey, c.Public)
		req.Header.Set(HeaderDate, lines[4])
		req.Header.Set(HeaderNonce, lines[5])
		req.Header.Set(HeaderSignature, c.Signature)
		date, _ := strconv.ParseInt(lines[4], 10, 64)
		at := time.Unix(date, 0)
		body := []byte(`{"state":"paired"}`)
		if lines[6] != hexSum(body) {
			t.Fatalf("signature %s: the body isn't the one signed", c.Name)
		}
		_, err := (&Verifier{Host: lines[2], Seen: NewNonces(at.Add(-time.Minute))}).Check(req, body, at)
		if (err == nil) != c.Valid {
			t.Errorf("signature %s, in a request: %v", c.Name, err)
		}
	}
	for _, c := range v.Addresses {
		if err := checkAddress(c.Text); (err == nil) != c.Valid {
			t.Errorf("address %q: %v", c.Text, err)
		}
	}
	for _, c := range v.Codes {
		if _, err := ParseCode(c.Text); (err == nil) != c.Valid {
			t.Errorf("code %s: %v", c.Name, err)
		}
	}
	for _, c := range v.Dates {
		if _, ok := parseDate(c.Text); ok != c.Valid {
			t.Errorf("date %q: %v", c.Text, ok)
		}
	}
	for _, c := range v.Base64url {
		b, ok := decode64(c.Text, -1)
		if ok = ok && (len(b) == 32 || len(b) == 16); ok != c.Valid {
			t.Errorf("base64url %s: %v", c.Name, ok)
		}
	}
}

// Each signature header once, without a comma (MSFP section 2.1).
func TestSignatureHeadersOnce(t *testing.T) {
	k := NewKey()
	now := time.Now()
	ok := func(edit func(*http.Request)) bool {
		req := httptest.NewRequest("POST", "https://a.example.org/api/v1/federation/hello", strings.NewReader("{}"))
		SignRequest(req, []byte("{}"), k, now)
		edit(req)
		_, err := (&Verifier{Host: "a.example.org"}).Check(req, []byte("{}"), now)
		return err == nil
	}
	if !ok(func(*http.Request) {}) {
		t.Fatal("a good request")
	}
	if ok(func(r *http.Request) { r.Header.Add(HeaderDate, r.Header.Get(HeaderDate)) }) {
		t.Error("a date twice")
	}
	if ok(func(r *http.Request) { r.Header.Set(HeaderNonce, r.Header.Get(HeaderNonce)+",x") }) {
		t.Error("a comma")
	}
}

func hexSum(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
