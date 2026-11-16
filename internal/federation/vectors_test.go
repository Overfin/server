package federation

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
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
