package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// The rotation of core section 17.2 (vectors/v1/rotation.json): A moves
// from "a" to "a2", committing to "a3". The statement and both signatures
// are rebuilt, the rotate body reads back and checks, and a2 matches the
// commitment A gave in its pair answer.
func TestVectorRotation(t *testing.T) {
	keys := vectorKeys(t)
	var v struct {
		Rotation struct {
			From, To, Next                        string
			OldKey, NewKey, NextCommitment        string
			Date                                  int64
			Statement, OldSignature, NewSignature string
		}
		Exchange vectorExchange
	}
	readVectors(t, "rotation.json", &v)
	r := v.Rotation
	old, new, next := keys[r.From], keys[r.To], keys[r.Next]
	if got := Encode64(Commitment(next.Public())); got != r.NextCommitment {
		t.Fatalf("the commitment to %s is %q, rotation.json says %q", r.Next, got, r.NextCommitment)
	}
	if got := string(rotationStatement(old.Public(), new.Public(), Commitment(next.Public()), r.Date)); got != r.Statement {
		t.Errorf("statement %q, rotation.json says %q", got, r.Statement)
	}
	s := SignRotation(old, new, Commitment(next.Public()), time.Unix(r.Date, 0))
	if Encode64(s.OldSignature) != r.OldSignature || Encode64(s.NewSignature) != r.NewSignature {
		t.Errorf("signatures %s, %s; rotation.json says %s, %s", Encode64(s.OldSignature), Encode64(s.NewSignature), r.OldSignature, r.NewSignature)
	}
	// The same members (JSON leaves their order free).
	var sent RotateBody
	if err := json.Unmarshal([]byte(v.Exchange.Body), &sent); err != nil || sent != s.Body() {
		t.Errorf("rotate body %+v, rotation.json says %s (%v)", s.Body(), v.Exchange.Body, err)
	}
	at := time.Unix(r.Date, 0)
	got, err := ReadRotate([]byte(v.Exchange.Body), old.Public(), at)
	if err != nil || !SameKey(got.New, new.Public()) || !bytes.Equal(got.Next, Commitment(next.Public())) || got.Date != r.Date {
		t.Fatalf("the rotate body reads as %+v, %v", got, err)
	}
	// B holds the commitment to a2, from A's pair answer (keys.json).
	var k struct {
		Keys map[string]struct{ Commitment string }
	}
	readVectors(t, "keys.json", &k)
	if Encode64(Commitment(got.New)) != k.Keys[r.To].Commitment {
		t.Error("a2 doesn't match its commitment")
	}
	// Its answer, rotated, as the exchange gives it.
	var answer struct{ State string }
	if json.Unmarshal([]byte(v.Exchange.AnswerBody), &answer); answer.State != StateRotated {
		t.Errorf("answer %s", v.Exchange.AnswerBody)
	}
	// The request's own signature and answer are the exchanges' checks.
	checkExchange(t, keys, v.Exchange)
}

// The revocation of core section 17.3 (vectors/v1/revocation.json).
func TestVectorRevocation(t *testing.T) {
	keys := vectorKeys(t)
	var v struct {
		Revocation struct {
			Key, Public          string
			Date                 int64
			Statement, Signature string
		}
		Exchange vectorExchange
	}
	readVectors(t, "revocation.json", &v)
	r := v.Revocation
	k := keys[r.Key]
	if got := string(revocationStatement(k.Public(), r.Date)); got != r.Statement {
		t.Errorf("statement %q, revocation.json says %q", got, r.Statement)
	}
	s := SignRevocation(k, time.Unix(r.Date, 0))
	if Encode64(s.Signature) != r.Signature {
		t.Errorf("signature %s, revocation.json says %s", Encode64(s.Signature), r.Signature)
	}
	var sent RevokeBody
	if err := json.Unmarshal([]byte(v.Exchange.Body), &sent); err != nil || sent != s.Body() {
		t.Errorf("revoke body %+v, revocation.json says %s (%v)", s.Body(), v.Exchange.Body, err)
	}
	if got, err := ReadRevoke([]byte(v.Exchange.Body), k.Public(), time.Unix(r.Date, 0)); err != nil || got.Date != r.Date {
		t.Fatalf("the revoke body reads as %+v, %v", got, err)
	}
	// Under another key, it doesn't verify.
	if _, err := ReadRevoke([]byte(v.Exchange.Body), keys["b"].Public(), time.Unix(r.Date, 0)); !errors.Is(err, ErrStatement) {
		t.Errorf("under b: %v", err)
	}
	checkExchange(t, keys, v.Exchange)
}

// The rotation member of the pulled exchange (vectors/v1/exchanges.json,
// A revokes a and rotates to a2 in one answer) checks as a rotate body.
func TestVectorPulledRotation(t *testing.T) {
	keys := vectorKeys(t)
	var v struct {
		Exchanges []vectorExchange `json:"exchanges"`
	}
	readVectors(t, "exchanges.json", &v)
	i := slices.IndexFunc(v.Exchanges, func(x vectorExchange) bool { return strings.Contains(x.Name, "rotation and a revocation") })
	if i < 0 {
		t.Fatal("no such exchange")
	}
	x := v.Exchanges[i]
	var answer struct {
		Rotation, Revocation json.RawMessage
	}
	if err := json.Unmarshal([]byte(x.AnswerBody), &answer); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1800300000, 0)
	if _, err := ReadRevoke(answer.Revocation, keys["a"].Public(), at); err != nil {
		t.Errorf("revocation: %v", err)
	}
	r, err := ReadRotate(answer.Rotation, keys["a"].Public(), at)
	if err != nil || !SameKey(r.New, keys["a2"].Public()) {
		t.Errorf("rotation: %+v, %v", r, err)
	}
}

// checkExchange checks one exchange's request and answer signatures, as
// TestVectorExchanges does for exchanges.json.
func checkExchange(t *testing.T, keys map[string]Key, x vectorExchange) {
	t.Helper()
	signer, answerer := keys[x.Signer], keys[x.Answerer]
	msg := requestMessage(x.Method, x.Host, x.Target, x.Date, x.NonceText, []byte(x.Body))
	if string(msg) != x.Message || Encode64(signer.sign(msg)) != x.Signature {
		t.Errorf("%s: the request's message or signature differ", x.Name)
	}
	if string(responseMessage(x.NonceText, x.Status, x.AnswerContentType, []byte(x.AnswerBody))) != x.AnswerMessage ||
		SignResponse(answerer, x.NonceText, x.Status, x.AnswerContentType, []byte(x.AnswerBody)) != x.AnswerSignature {
		t.Errorf("%s: the answer's message or signature differ", x.Name)
	}
}

// What ReadRotate refuses: each is a 400 invalid-request.
func TestReadRotateRefuses(t *testing.T) {
	old, new, next := NewKey(), NewKey(), NewKey()
	now := time.Unix(1800000000, 0)
	good := SignRotation(old, new, Commitment(next.Public()), now)
	body := func(edit func(*RotateBody)) []byte {
		b := good.Body()
		edit(&b)
		out, _ := Marshal(b)
		return out
	}
	if _, err := ReadRotate(body(func(*RotateBody) {}), old.Public(), now); err != nil {
		t.Fatalf("a good rotation: %v", err)
	}
	// Without a commitment, the statement says "-": signed so, it passes.
	none := SignRotation(old, new, nil, now)
	nb, _ := Marshal(none.Body())
	if r, err := ReadRotate(nb, old.Public(), now); err != nil || r.Next != nil {
		t.Fatalf("no commitment: %+v, %v", r, err)
	}
	bad := map[string][]byte{
		"not JSON":              []byte("nope"),
		"no newKey":             []byte(`{"date":1800000000,"oldSignature":"x","newSignature":"y"}`),
		"a date as a string":    bytes.Replace(body(func(*RotateBody) {}), []byte(`"date":1800000000`), []byte(`"date":"1800000000"`), 1),
		"a short commitment":    body(func(b *RotateBody) { b.NextCommitment = "AAAA" }),
		"another commitment":    body(func(b *RotateBody) { b.NextCommitment = Encode64(Commitment(old.Public())) }),
		"the commitment gone":   body(func(b *RotateBody) { b.NextCommitment = "" }),
		"another new key":       body(func(b *RotateBody) { b.NewKey = Encode64(next.Public()) }),
		"the old key as new":    body(func(b *RotateBody) { b.NewKey = Encode64(old.Public()) }),
		"signatures swapped":    body(func(b *RotateBody) { b.OldSignature, b.NewSignature = b.NewSignature, b.OldSignature }),
		"another date":          body(func(b *RotateBody) { b.Date++ }),
		"a small-order new key": body(func(b *RotateBody) { b.NewKey = Encode64(make([]byte, 32)) }),
	}
	for name, b := range bad {
		if _, err := ReadRotate(b, old.Public(), now); !errors.Is(err, ErrStatement) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Under another old key, and 6 minutes off.
	if _, err := ReadRotate(body(func(*RotateBody) {}), next.Public(), now); err == nil {
		t.Error("under another old key")
	}
	if _, err := ReadRotate(body(func(*RotateBody) {}), old.Public(), now.Add(6*time.Minute)); err == nil {
		t.Error("6 minutes later")
	}
	if _, err := ReadRotate(body(func(*RotateBody) {}), old.Public(), now.Add(-6*time.Minute)); err == nil {
		t.Error("6 minutes before")
	}
}

// A hello's nextCommitment: 32 bytes, or a bad body.
func TestHelloNextCommitment(t *testing.T) {
	c := Encode64(Commitment(NewKey().Public()))
	h, err := ReadHello([]byte(`{"state":"paired","nextCommitment":"`+c+`"}`), false)
	if err != nil || Encode64(h.NextCommitment) != c {
		t.Fatalf("%+v, %v", h, err)
	}
	if h, err := ReadHello([]byte(`{"state":"paired"}`), false); err != nil || h.NextCommitment != nil {
		t.Fatalf("none: %+v, %v", h, err)
	}
	for _, bad := range []string{`"AAAA"`, `1`, `true`} {
		if _, err := ReadHello([]byte(`{"state":"paired","nextCommitment":`+bad+`}`), false); err == nil {
			t.Errorf("nextCommitment %s passes", bad)
		}
	}
}
