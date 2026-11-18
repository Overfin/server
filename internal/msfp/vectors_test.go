package msfp

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// MSFP's version 1 test vectors (https://github.com/msfp-io/msfp-spec,
// vectors/v1/), copied as they are in testdata/v1/: recomputed here with
// the code, so the code keeps speaking the protocol they describe. The
// exchanges of a server that can't be called are read in
// uncallable_test.go.

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
	Status            int    `json:"status"`
	AnswerContentType string `json:"answerContentType"`
	AnswerBody        string `json:"answerBody"`
	AnswerMessage     string `json:"answerMessage"`
	AnswerSignature   string `json:"answerSignature"`
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex %q: %v", s, err)
	}
	return b
}

// readVectors reads testdata/v1/name into v.
func readVectors(t *testing.T, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile("testdata/v1/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// vectorKeys are the keys of keys.json, rebuilt from their seeds and
// checked against their public keys and fingerprints.
func vectorKeys(t *testing.T) map[string]Key {
	t.Helper()
	var v struct {
		Keys map[string]vectorKey `json:"keys"`
	}
	readVectors(t, "keys.json", &v)
	if len(v.Keys) == 0 {
		t.Fatal("no keys")
	}
	keys := map[string]Key{}
	for name, k := range v.Keys {
		key, err := KeyFromSeed(unhex(t, k.Seed))
		if err != nil {
			t.Fatal(err)
		}
		keys[name] = key
		if got := b64.EncodeToString(key.Public()); got != k.Public {
			t.Errorf("%s: public key %q, keys.json says %q", name, got, k.Public)
		}
		if got := Fingerprint(key.Public()); got != k.Fingerprint {
			t.Errorf("%s: fingerprint %q, keys.json says %q", name, got, k.Fingerprint)
		}
	}
	return keys
}

func TestVectorCode(t *testing.T) {
	keys := vectorKeys(t)
	var v struct {
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
	}
	readVectors(t, "code.json", &v)
	secret := unhex(t, v.Code.Secret)
	code := Code{Address: v.Code.Address, Key: keys[v.Code.Key].Public(), Secret: secret, Expires: time.Unix(v.Code.Expires, 0)}
	if code.String() != v.Code.Text {
		t.Errorf("pairing code %q, code.json says %q", code.String(), v.Code.Text)
	}
	if c, err := ParseCode(v.Code.Text); err != nil || c.Address != code.Address || !SameKey(c.Key, code.Key) ||
		!bytes.Equal(c.Secret, secret) || !c.Expires.Equal(code.Expires) {
		t.Errorf("the pairing code reads back as %+v, %v", c, err)
	}
	if got := b64.EncodeToString(SecretHash(secret)); got != v.Code.SecretHash {
		t.Errorf("secret hash %q, code.json says %q", got, v.Code.SecretHash)
	}
	if got := PairingProof(secret, keys[v.Proof.Redeemer].Public(), v.Proof.Address); got != v.Proof.Value {
		t.Errorf("pairing proof %q, code.json says %q", got, v.Proof.Value)
	}
	if !CheckPairingProof(secret, keys[v.Proof.Redeemer].Public(), v.Proof.Address, v.Proof.Value) {
		t.Error("the pairing proof is refused")
	}
}

func TestVectorExchanges(t *testing.T) {
	keys := vectorKeys(t)
	var v struct {
		Exchanges []vectorExchange `json:"exchanges"`
	}
	readVectors(t, "exchanges.json", &v)
	if len(v.Exchanges) == 0 {
		t.Fatal("no exchanges")
	}
	same := func(what, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s: the code gives %q, exchanges.json says %q", what, got, want)
		}
	}
	for _, x := range v.Exchanges {
		signer, answerer := keys[x.Signer], keys[x.Answerer]
		same(x.Name+" nonce", b64.EncodeToString(unhex(t, x.Nonce)), x.NonceText)
		msg := requestMessage(x.Method, x.Host, x.Target, x.Date, x.NonceText, []byte(x.Body))
		same(x.Name+" signed message", string(msg), x.Message)
		same(x.Name+" signature", b64.EncodeToString(signer.sign(msg)), x.Signature)

		// The request as it arrives, checked as a server checks it.
		req := httptest.NewRequest(x.Method, "https://"+x.Host+x.Target, strings.NewReader(x.Body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(HeaderKey, b64.EncodeToString(signer.Public()))
		req.Header.Set(HeaderDate, x.Date)
		req.Header.Set(HeaderNonce, x.NonceText)
		req.Header.Set(HeaderSignature, x.Signature)
		date, _ := strconv.ParseInt(x.Date, 10, 64)
		at := time.Unix(date, 0)
		ver := &Verifier{Host: x.Host, Seen: NewNonces(at.Add(-time.Minute))}
		if got, err := ver.Check(req, []byte(x.Body), at); err != nil || !SameKey(got.Key, signer.Public()) {
			t.Errorf("%s: the request is refused: %v", x.Name, err)
		}
		// With a header of the first test version beside: refused.
		old := req.Clone(req.Context())
		old.Body = http.NoBody
		old.Header.Set("Overfin-Nonce", x.NonceText)
		if _, err := (&Verifier{Host: x.Host}).Check(old, []byte(x.Body), at); err == nil {
			t.Errorf("%s: passes with an Overfin-Nonce header beside", x.Name)
		}

		answer := []byte(x.AnswerBody)
		same(x.Name+" answer's signed message", string(responseMessage(x.NonceText, x.Status, x.AnswerContentType, answer)), x.AnswerMessage)
		same(x.Name+" answer's signature", SignResponse(answerer, x.NonceText, x.Status, x.AnswerContentType, answer), x.AnswerSignature)
		if err := CheckResponse(answerer.Public(), x.NonceText, x.Status, x.AnswerContentType, answer, x.AnswerSignature); err != nil {
			t.Errorf("%s: the answer is refused: %v", x.Name, err)
		}
		// Its Content-Type changed on the way, its parameters aside: refused.
		if CheckResponse(answerer.Public(), x.NonceText, x.Status, "text/plain", answer, x.AnswerSignature) == nil {
			t.Errorf("%s: the answer passes with another media type", x.Name)
		}
		if CheckResponse(answerer.Public(), x.NonceText, x.Status, x.AnswerContentType+"; charset=utf-8", answer, x.AnswerSignature) != nil {
			t.Errorf("%s: the answer is refused with a charset parameter", x.Name)
		}
	}
}

// The hello of core section 17.1, A to B, and B's answer: the versions
// each lists, and what is on between them (catalog only).
func TestVectorHello(t *testing.T) {
	var v struct {
		Exchanges []vectorExchange `json:"exchanges"`
	}
	readVectors(t, "exchanges.json", &v)
	i := slices.IndexFunc(v.Exchanges, func(x vectorExchange) bool { return x.Name == "hello" })
	if i < 0 {
		t.Fatal("no hello")
	}
	x := v.Exchanges[i]
	a, err := ReadHello([]byte(x.Body), false)
	if err != nil || a.State != "paired" || a.Address != "https://a.example.org" ||
		!slices.Equal(a.Versions.Core, []int{1}) || len(a.Versions.Parts) != 4 || len(a.Certificates) != 1 {
		t.Fatalf("A's hello reads as %+v, %v", a, err)
	}
	b, err := ReadHello([]byte(x.AnswerBody), true)
	if err != nil || b.State != "paired" || !slices.Equal(b.Versions.Core, []int{1}) || len(b.Certificates) != 1 {
		t.Fatalf("B's answer reads as %+v, %v", b, err)
	}
	core, parts, ok := Agreed(a.Versions, b.Versions)
	if !ok || core != 1 || !maps.Equal(parts, map[string]int{PartCatalog: 1}) {
		t.Errorf("agreed core %d, parts %v, %v; want core 1, catalog 1 only", core, parts, ok)
	}
}

// Addresses and codes under core 1 (vectors/v1/strict.json): https only,
// the prefix msfp-pair-1.; and the names of a stream's files.
func TestStrictVectors(t *testing.T) {
	type tc struct {
		Name, Text string
		Valid      bool
	}
	var v struct {
		Addresses, Codes, MediaNames []tc
	}
	readVectors(t, "strict.json", &v)
	if len(v.Addresses) == 0 || len(v.Codes) == 0 || len(v.MediaNames) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range v.Addresses {
		if err := CheckAddress(c.Text); (err == nil) != c.Valid {
			t.Errorf("address %q: %v", c.Text, err)
		}
	}
	for _, c := range v.Codes {
		if _, err := ParseCode(c.Text); (err == nil) != c.Valid {
			t.Errorf("code %s: %v", c.Name, err)
		}
	}
	for _, c := range v.MediaNames {
		if FileName(c.Text) != c.Valid {
			t.Errorf("media name %q: valid %v", c.Text, !c.Valid)
		}
	}
	// With the test switch on, plain http is an address, and nothing else
	// changes.
	AllowPlainHTTPForTests(true)
	defer AllowPlainHTTPForTests(false)
	for _, c := range v.Addresses {
		valid := c.Valid || c.Text == "http://192.0.2.10:8096"
		if err := CheckAddress(c.Text); (err == nil) != valid {
			t.Errorf("address %q, plain http allowed: %v", c.Text, err)
		}
	}
}

// The canonical forms of vectors/v0-strict.json that hold for core 1 too
// (keys, signatures, base64url, dates; its json and integers are in
// json_test.go). Its addresses and codes are version 0's: strict.json's
// replace them.
func TestStrictFormVectors(t *testing.T) {
	b, err := os.ReadFile("testdata/v0-strict.json")
	if err != nil {
		t.Fatal(err)
	}
	type tc struct {
		Name, Text string
		Valid      bool
	}
	var v struct {
		Dates, Base64url, Keys []tc
		Signatures             []struct {
			Name, Public, Message, Signature string
			Valid                            bool
		}
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Dates) == 0 || len(v.Base64url) == 0 || len(v.Keys) == 0 || len(v.Signatures) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range v.Keys {
		key, _ := Decode64(c.Text, 32)
		if ValidKey(key) != c.Valid {
			t.Errorf("key %s: valid %v", c.Name, !c.Valid)
		}
		// And as a request's MSFP-Key, and a code's key.
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
		public, _ := Decode64(c.Public, 32)
		sig, _ := Decode64(c.Signature, 64)
		if verify(public, []byte(c.Message), sig) != c.Valid {
			t.Errorf("signature %s: valid %v", c.Name, !c.Valid)
		}
	}
	for _, c := range v.Dates {
		if _, ok := parseDate(c.Text); ok != c.Valid {
			t.Errorf("date %q: %v", c.Text, ok)
		}
	}
	for _, c := range v.Base64url {
		b, ok := Decode64(c.Text, -1)
		if ok = ok && (len(b) == 32 || len(b) == 16); ok != c.Valid {
			t.Errorf("base64url %s: %v", c.Name, ok)
		}
	}
}

// Each signature header once, without a comma (MSFP core section 2.4).
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

// What a receiver keeps of a hello's versions (vectors/v1/strict.json,
// helloVersions): a number that isn't an integer ignored as an entry, an
// unknown member or part ignored whatever its value, null included; the
// lists of the wrong type a bad body (core sections 2.3 and 10.2).
func TestHelloVersionVectors(t *testing.T) {
	var v struct {
		HelloVersions []struct {
			Name, Text, Output string
			Valid              bool
		} `json:"helloVersions"`
	}
	readVectors(t, "strict.json", &v)
	if len(v.HelloVersions) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range v.HelloVersions {
		h, err := ReadHello([]byte(c.Text), false)
		if (err == nil) != c.Valid {
			t.Errorf("%s: %v, want valid %v", c.Name, err, c.Valid)
			continue
		}
		if !c.Valid {
			continue
		}
		sorted := func(list []int) []int {
			out := append([]int{}, list...)
			slices.Sort(out)
			return out
		}
		kept := Versions{Core: sorted(h.Versions.Core), Parts: map[string][]int{}}
		for name, list := range h.Versions.Parts {
			kept.Parts[name] = sorted(list)
		}
		got, _ := json.Marshal(kept)
		if string(got) != c.Output {
			t.Errorf("%s: kept %s, want %s", c.Name, got, c.Output)
		}
	}
}
