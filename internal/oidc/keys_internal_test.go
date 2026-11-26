package oidc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func rsaJWK(t *testing.T, kid, use string, bits int) map[string]string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"kty": "RSA", "kid": kid, "use": use, "n": b64(k.N.Bytes()), "e": b64(big.NewInt(int64(k.E)).Bytes())}
}

func ecJWK(t *testing.T, kid string, curve elliptic.Curve, crv string) map[string]string {
	t.Helper()
	k, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	size := (curve.Params().BitSize + 7) / 8
	x, y := make([]byte, size), make([]byte, size)
	k.X.FillBytes(x)
	k.Y.FillBytes(y)
	return map[string]string{"kty": "EC", "kid": kid, "crv": crv, "x": b64(x), "y": b64(y)}
}

// The provider's signing keys: RSA of 2048 bits or more and P-256 kept;
// a smaller RSA key, one for encryption, a point off the curve, another
// curve, another type, never; an unknown key fetches the set again, at
// most once a minute, and a key the provider rotated in is then found.
func TestSigningKeys(t *testing.T) {
	offCurve := ecJWK(t, "off", elliptic.P256(), "P-256")
	offCurve["y"] = b64(make([]byte, 32))
	var mu sync.Mutex
	fetches := 0
	set := []map[string]string{
		rsaJWK(t, "rsa", "sig", 2048), rsaJWK(t, "rsa-any", "", 2048), rsaJWK(t, "small", "sig", 1024), rsaJWK(t, "enc", "enc", 2048),
		ecJWK(t, "ec", elliptic.P256(), "P-256"), offCurve, ecJWK(t, "p384", elliptic.P384(), "P-384"),
		{"kty": "OKP", "kid": "ed", "crv": "Ed25519", "x": b64(make([]byte, 32))},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		fetches++
		json.NewEncoder(w).Encode(map[string]any{"keys": set})
	}))
	defer srv.Close()
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	p := &Provider{HTTP: srv.Client(), now: func() time.Time { return now }}
	m := &metadata{JWKSURI: srv.URL}
	ctx := context.Background()

	for _, kid := range []string{"rsa", "rsa-any", "ec"} {
		if k, err := p.key(ctx, m, kid); err != nil || k == nil {
			t.Errorf("%s: %v", kid, err)
		}
	}
	for _, kid := range []string{"small", "enc", "off", "p384", "ed"} {
		if _, err := p.key(ctx, m, kid); err == nil {
			t.Errorf("%s kept", kid)
		}
	}
	if fetches != 1 {
		t.Errorf("fetched %d times in a minute, want once", fetches)
	}
	// The provider rotates a key in: found once a minute has passed.
	mu.Lock()
	set = append(set, rsaJWK(t, "new", "sig", 2048))
	mu.Unlock()
	if _, err := p.key(ctx, m, "new"); err == nil {
		t.Error("a key found within the minute")
	}
	now = now.Add(61 * time.Second)
	if k, err := p.key(ctx, m, "new"); err != nil || k == nil || fetches != 2 {
		t.Errorf("the rotated key: %v, %d fetches", err, fetches)
	}
}
