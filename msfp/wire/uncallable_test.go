package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"
)

// The addresses of servers that can't be called (vectors/v1/strict.json,
// uncallableAddresses): https://msfp-<26 characters>.invalid, those
// characters exact base32 of 16 bytes.
func TestUncallableAddressVectors(t *testing.T) {
	var v struct {
		UncallableAddresses []struct {
			Text   string
			Valid  bool
			Reason string
		}
	}
	readVectors(t, "strict.json", &v)
	if len(v.UncallableAddresses) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range v.UncallableAddresses {
		if err := CheckUncallable(c.Text); (err == nil) != c.Valid {
			t.Errorf("%q (%s): %v", c.Text, c.Reason, err)
		}
		// One that is valid is an address like any other (section 5.2).
		if c.Valid && CheckAddress(c.Text) != nil {
			t.Errorf("%q isn't an address", c.Text)
		}
	}
}

// Made at random, once: each one of the form, and never the same.
func TestNewUncallableAddress(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		a := NewUncallableAddress()
		if err := CheckUncallable(a); err != nil || CheckAddress(a) != nil {
			t.Fatalf("%q: %v", a, err)
		}
		if seen[a] {
			t.Fatalf("%q twice", a)
		}
		seen[a] = true
	}
}

// inbound false goes with that form of address only, whichever the
// address checked is (core section 18, point 31).
func TestCheckInbound(t *testing.T) {
	invalid := "https://msfp-4dq6fy7e4xtop2hj5lv6z3po54.invalid"
	for _, c := range []struct {
		uncallable bool
		address    string
		ok         bool
	}{
		{true, invalid, true},
		{true, "https://b.example.org", false},
		{true, "", false},
		{false, "https://b.example.org", true},
		{false, invalid, true},
	} {
		if err := CheckInbound(c.uncallable, c.address); (err == nil) != c.ok {
			t.Errorf("uncallable %v, %q: %v", c.uncallable, c.address, err)
		}
	}
}

// The schedule (core sections 7.5 and 8.3): 20 seconds while the other
// operator hasn't accepted, for a day after the pair; 5 minutes once
// paired, or after that day; an hour once gone; each give or take a fifth.
func TestHelloSchedule(t *testing.T) {
	for _, c := range []struct {
		waiting, gone bool
		since         time.Duration
		want          time.Duration
	}{
		{true, false, time.Minute, 20 * time.Second},
		{true, false, 23 * time.Hour, 20 * time.Second},
		{true, false, 25 * time.Hour, 5 * time.Minute},
		{false, false, time.Minute, 5 * time.Minute},
		{false, true, time.Minute, time.Hour},
		{true, true, time.Minute, time.Hour},
	} {
		if got := HelloWait(c.waiting, c.gone, c.since); got != c.want {
			t.Errorf("waiting %v, gone %v, %v after the pair: %v, want %v", c.waiting, c.gone, c.since, got, c.want)
		}
	}
	d := 5 * time.Minute
	if got := Vary(d, 0); got != 4*time.Minute {
		t.Errorf("Vary at 0: %v", got)
	}
	if got := Vary(d, 0.5); got != d {
		t.Errorf("Vary at 0.5: %v", got)
	}
	if got := Vary(d, 0.999999); got <= 5*time.Minute+59*time.Second || got > 6*time.Minute {
		t.Errorf("Vary toward 1: %v", got)
	}
	for range 1000 {
		if got := NextHello(false, false, 0); got < 4*time.Minute || got >= 6*time.Minute {
			t.Fatalf("NextHello %v, out of 5 minutes give or take a fifth", got)
		}
	}
}

// The members that go one way only (core section 9.2, section 18 point
// 34): read from a server that can't be called, type-checked there, and
// ignored where the table doesn't put them.
func TestHelloOneWay(t *testing.T) {
	key := NewKey().Public()
	known := strconv.Quote(Encode64(key))
	invalid := `"address":"https://msfp-4dq6fy7e4xtop2hj5lv6z3po54.invalid"`
	req := func(body string) (Hello, error) { return ReadHello([]byte(body), false) }

	h, err := req(`{"state":"paired",` + invalid + `,"inbound":false,"knownKey":` + known + `,"rotation":"pending"}`)
	if err != nil || !h.Uncallable || !SameKey(h.KnownKey, key) || h.RotationState != StatePending {
		t.Fatalf("from a server that can't be called: %+v, %v", h, err)
	}
	if h, err := req(`{"state":"paired",` + invalid + `,"inbound":false,"rotation":"refused"}`); err != nil || h.RotationState != RotationRefused {
		t.Errorf("refused: %+v, %v", h, err)
	}
	// Another string is ignored, and so is a knownKey that isn't a key.
	if h, err := req(`{"state":"paired",` + invalid + `,"inbound":false,"knownKey":"x","rotation":"rotated"}`); err != nil || h.KnownKey != nil || h.RotationState != "" {
		t.Errorf("other strings: %+v, %v", h, err)
	}
	// Of the wrong type there: a bad body.
	for _, bad := range []string{`"knownKey":5`, `"rotation":{"newKey":"x"}`, `"knownKey":null`, `"inbound":"no"`} {
		if _, err := req(`{"state":"paired",` + invalid + `,"inbound":false,` + bad + `}`); !errors.Is(err, ErrJSON) {
			t.Errorf("%s from a server that can't be called: %v", bad, err)
		}
	}
	// From a server that can be called, whatever their value: ignored.
	for _, inbound := range []string{``, `"inbound":true,`} {
		h, err := req(`{"state":"paired",` + inbound + `"knownKey":5,"rotation":null}`)
		if err != nil || h.Uncallable || h.KnownKey != nil || h.RotationState != "" {
			t.Errorf("from a server that can be called (%s): %+v, %v", inbound, h, err)
		}
	}
	// revocation and callsFailed in a request: ignored, null included.
	if h, err := req(`{"state":"paired",` + invalid + `,"inbound":false,"revocation":null,"callsFailed":"x"}`); err != nil || h.CallsFailed || h.Revocation != nil {
		t.Errorf("answer members in a request: %+v, %v", h, err)
	}

	ans := func(body string) Hello {
		t.Helper()
		h, err := ReadHello([]byte(body), true)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		return h
	}
	// In an answer: rotation and revocation objects, callsFailed true;
	// anything else counts as absent, and knownKey is ignored.
	if h := ans(`{"state":"paired","rotation":{"a":1},"revocation":{"b":2},"callsFailed":true,"knownKey":5}`); string(h.Rotation) != `{"a":1}` ||
		string(h.Revocation) != `{"b":2}` || !h.CallsFailed || h.KnownKey != nil {
		t.Errorf("an answer's members: %+v", h)
	}
	for _, other := range []string{`"rotation":"pending","revocation":[1],"callsFailed":1`, `"rotation":5,"revocation":"x","callsFailed":"true"`, `"callsFailed":false`} {
		if h := ans(`{"state":"paired",` + other + `}`); h.Rotation != nil || h.Revocation != nil || h.CallsFailed {
			t.Errorf("%s: %+v", other, h)
		}
	}
}

// The last three exchanges of vectors/v1/exchanges.json: B can't be
// called; its hello carries inbound false, its address of that form and
// knownKey a; A, having revoked a and rotated to a2 (committing to a3) in
// one action, answers signed with a, with both statements; B makes a2
// current, and its next hello carries knownKey a2.
func TestVectorUncallableExchanges(t *testing.T) {
	keys := vectorKeys(t)
	var v struct {
		Exchanges []vectorExchange `json:"exchanges"`
	}
	readVectors(t, "exchanges.json", &v)
	find := func(name string) vectorExchange {
		t.Helper()
		i := slices.IndexFunc(v.Exchanges, func(x vectorExchange) bool { return x.Name == name })
		if i < 0 {
			t.Fatalf("no exchange %q", name)
		}
		return v.Exchanges[i]
	}
	a, a2, a3 := keys["a"].Public(), keys["a2"].Public(), keys["a3"].Public()

	first := find("hello, from a server that can't be called")
	h, err := ReadHello([]byte(first.Body), false)
	if err != nil || !h.Uncallable || !SameKey(h.KnownKey, a) || h.Address != "https://msfp-4dq6fy7e4xtop2hj5lv6z3po54.invalid" ||
		len(h.Certificates) != 0 || CheckInbound(h.Uncallable, h.Address) != nil {
		t.Fatalf("B's hello reads as %+v, %v", h, err)
	}
	// Its answer names A's address, which isn't B's form: inbound isn't
	// in it, and A can be called.
	if ah, err := ReadHello([]byte(first.AnswerBody), true); err != nil || ah.Uncallable || ah.Rotation != nil {
		t.Fatalf("A's answer reads as %+v, %v", ah, err)
	}

	pulled := find("hello, answered with a rotation and a revocation")
	if h, err := ReadHello([]byte(pulled.Body), false); err != nil || !SameKey(h.KnownKey, a) {
		t.Fatalf("B's second hello reads as %+v, %v", h, err)
	}
	ah, err := ReadHello([]byte(pulled.AnswerBody), true)
	if err != nil || ah.Rotation == nil || ah.Revocation == nil {
		t.Fatalf("A's answer reads as %+v, %v", ah, err)
	}
	date, _ := strconv.ParseInt(pulled.Date, 10, 64)
	at := time.Unix(date, 0)
	p := ah.Pull(a, at)
	if p.Revocation == nil || !SameKey(p.Revocation.Key, a) {
		t.Errorf("the revocation: %+v", p.Revocation)
	}
	if p.Rotation == nil || !SameKey(p.Rotation.Old, a) || !SameKey(p.Rotation.New, a2) || !bytes.Equal(p.Rotation.Next, Commitment(a3)) {
		t.Fatalf("the rotation: %+v", p.Rotation)
	}
	// The statements as A makes them, dated as it answers: the same.
	rot := SignRotation(keys["a"], keys["a2"], Commitment(a3), at)
	rev := SignRevocation(keys["a"], at)
	var sent struct {
		Rotation   RotateBody `json:"rotation"`
		Revocation RevokeBody `json:"revocation"`
	}
	if err := json.Unmarshal([]byte(pulled.AnswerBody), &sent); err != nil || sent.Rotation != rot.Body() || sent.Revocation != rev.Body() {
		t.Errorf("A's statements %+v %+v; the exchange has %+v (%v)", rot.Body(), rev.Body(), sent, err)
	}
	// Checked against another key than the one the answer verified under,
	// or too late: neither counts.
	if p := ah.Pull(a2, at); p.Revocation != nil || p.Rotation != nil {
		t.Errorf("under a2: %+v", p)
	}
	if p := ah.Pull(a, at.Add(MaxSkew+time.Second)); p.Revocation != nil || p.Rotation != nil {
		t.Errorf("6 minutes later: %+v", p)
	}

	last := find("hello, with the new key known")
	if h, err := ReadHello([]byte(last.Body), false); err != nil || !SameKey(h.KnownKey, a2) {
		t.Fatalf("B's hello after it moved reads as %+v, %v", h, err)
	}
	if ah, err := ReadHello([]byte(last.AnswerBody), true); err != nil || ah.Rotation != nil || ah.Revocation != nil {
		t.Errorf("A's answer under a2 reads as %+v, %v", ah, err)
	}
}

// Whatever a hello holds, it reads without a crash, and the one-way
// members come only where they go: knownKey and rotation from a server
// that can't be called, statements only as objects, in answers.
func FuzzReadHello(f *testing.F) {
	f.Add([]byte(`{"state":"paired","address":"https://msfp-4dq6fy7e4xtop2hj5lv6z3po54.invalid","inbound":false,"knownKey":"A6EHv_POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg","rotation":"pending"}`), false)
	f.Add([]byte(`{"state":"paired","rotation":{"newKey":"x"},"revocation":{"date":1},"callsFailed":true}`), true)
	f.Add([]byte(`{"state":"waiting","knownKey":null,"rotation":5}`), false)
	f.Fuzz(func(t *testing.T, body []byte, answer bool) {
		h, err := ReadHello(body, answer)
		if err != nil {
			return
		}
		if !answer && !h.Uncallable && (h.KnownKey != nil || h.RotationState != "") {
			t.Errorf("knownKey or rotation read from a server that can be called: %+v", h)
		}
		if h.RotationState != "" && h.RotationState != StatePending && h.RotationState != RotationRefused {
			t.Errorf("rotation %q", h.RotationState)
		}
		if !answer && (h.Rotation != nil || h.Revocation != nil || h.CallsFailed) {
			t.Errorf("an answer's members read in a request: %+v", h)
		}
		for _, raw := range [][]byte{h.Rotation, h.Revocation} {
			if raw != nil && raw[0] != '{' {
				t.Errorf("a statement that isn't an object: %q", raw)
			}
		}
	})
}

// An address of a server that can't be called is an address, and its
// text is its bytes' one text.
func FuzzCheckUncallable(f *testing.F) {
	f.Add("https://msfp-4dq6fy7e4xtop2hj5lv6z3po54.invalid")
	f.Add("https://msfp-4dq6fy7e4xtop2hj5lv6z3po5b.invalid")
	f.Fuzz(func(t *testing.T, address string) {
		if CheckUncallable(address) != nil {
			return
		}
		if err := CheckAddress(address); err != nil {
			t.Errorf("%q isn't an address: %v", address, err)
		}
		text := address[len(uncallablePrefix) : len(address)-len(uncallableSuffix)]
		b, err := lowerBase32.DecodeString(text)
		if err != nil || lowerBase32.EncodeToString(b) != text {
			t.Errorf("%q: not its bytes' one text", address)
		}
	})
}
