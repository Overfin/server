package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"
)

// Whatever an ID token holds, verify takes it only when it is signed by
// the provider's key and its claims hold, and never panics on a broken
// one.
func FuzzVerify(f *testing.F) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		f.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	m := &metadata{Issuer: "https://idp.example.org/"}
	b64 := base64.RawURLEncoding.EncodeToString
	sign := func(header, claims string) string {
		signed := b64([]byte(header)) + "." + b64([]byte(claims))
		sum := sha256.Sum256([]byte(signed))
		r, s, _ := ecdsa.Sign(rand.Reader, key, sum[:])
		sig := make([]byte, 64)
		r.FillBytes(sig[:32])
		s.FillBytes(sig[32:])
		return signed + "." + b64(sig)
	}
	good := `{"iss":"https://idp.example.org/","sub":"u1","aud":"overfin","exp":1800000300,"iat":1800000000,"auth_time":1800000000,"nonce":"n","groups":["a"]}`
	f.Add(`{"alg":"ES256","kid":"k1"}`, good, "", true)
	f.Add(`{"alg":"ES256","kid":"k1"}`, `{"iss":"https://idp.example.org/","sub":"u1","aud":["overfin","x"],"azp":"overfin","exp":9223372036854775807,"iat":-9223372036854775808}`, "", true)
	f.Add(`{"alg":"none","kid":"k1"}`, good, "a.b.", false)
	f.Add(`{"alg":"ES256","kid":"k2"}`, good, "x..y", false)
	f.Fuzz(func(t *testing.T, header, claims, junk string, signIt bool) {
		p := &Provider{ClientID: "overfin", GroupsClaim: "groups", now: func() time.Time { return now },
			keys: map[string]crypto.PublicKey{"k1": &key.PublicKey}, keysFetch: now}
		token := junk
		if signIt {
			token = sign(header, claims)
		}
		for _, nonce := range []string{"n", ""} {
			login, err := p.verify(context.Background(), m, token, nonce)
			if err != nil {
				continue
			}
			if !signIt || login.Subject == "" {
				t.Fatalf("%q accepted: %+v", token, login)
			}
			if nonce != "" && login.AuthTime.IsZero() {
				t.Fatalf("no login time: %+v", login)
			}
		}
	})
}
