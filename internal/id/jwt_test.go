package id

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testSigner(t *testing.T) *Signer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &Signer{Kid: "test-kid", Key: key}
}

func claims(exp time.Time) Claims {
	return Claims{Iss: "http://issuer.test", Sub: "user-1", Aud: "agora",
		Exp: exp.Unix(), Iat: time.Now().Unix(), Email: "a@b.c"}
}

func TestJWTRoundTrip(t *testing.T) {
	s := testSigner(t)
	keys := map[string]*rsa.PublicKey{s.Kid: &s.Key.PublicKey}
	now := time.Now()

	tok, err := s.Sign(claims(now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyJWT(tok, keys, "http://issuer.test", "agora", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sub != "user-1" || got.Email != "a@b.c" {
		t.Fatalf("claims mangled: %+v", got)
	}

	// tampered payload
	parts := strings.Split(tok, ".")
	evil, _ := json.Marshal(map[string]any{"iss": "http://issuer.test", "sub": "user-666",
		"aud": "agora", "exp": now.Add(time.Hour).Unix()})
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString(evil) + "." + parts[2]
	if _, err := VerifyJWT(tampered, keys, "http://issuer.test", "agora", now); err == nil {
		t.Fatal("tampered token verified")
	}

	// expired
	tok, _ = s.Sign(claims(now.Add(-time.Minute)))
	if _, err := VerifyJWT(tok, keys, "http://issuer.test", "agora", now); err == nil {
		t.Fatal("expired token verified")
	}

	// wrong audience
	tok, _ = s.Sign(claims(now.Add(time.Hour)))
	if _, err := VerifyJWT(tok, keys, "http://issuer.test", "other", now); err == nil {
		t.Fatal("wrong-aud token verified")
	}

	// unknown kid
	if _, err := VerifyJWT(tok, map[string]*rsa.PublicKey{}, "http://issuer.test", "agora", now); err == nil {
		t.Fatal("unknown-kid token verified")
	}
}

func TestAlgNoneRejected(t *testing.T) {
	s := testSigner(t)
	keys := map[string]*rsa.PublicKey{s.Kid: &s.Key.PublicKey}
	header, _ := json.Marshal(map[string]string{"alg": "none", "kid": s.Kid})
	payload, _ := json.Marshal(claims(time.Now().Add(time.Hour)))
	tok := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + "."
	if _, err := VerifyJWT(tok, keys, "http://issuer.test", "agora", time.Now()); err == nil {
		t.Fatal("alg=none accepted")
	}
}

func TestJWKSRoundTrip(t *testing.T) {
	s := testSigner(t)
	doc, err := JWKS(map[string]*rsa.PublicKey{s.Kid: &s.Key.PublicKey})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := ParseJWKS(doc)
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := s.Sign(claims(time.Now().Add(time.Hour)))
	if _, err := VerifyJWT(tok, keys, "http://issuer.test", "agora", time.Now()); err != nil {
		t.Fatalf("verify with parsed JWKS: %v", err)
	}
}
