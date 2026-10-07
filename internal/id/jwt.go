// Hand-rolled RS256 JWT + JWKS. Educational by design (see README): the point
// of Agora's id service is building the OIDC provider from the RFCs.
package id

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Claims struct {
	Iss   string `json:"iss"`
	Sub   string `json:"sub"`
	Aud   string `json:"aud"`
	Exp   int64  `json:"exp"`
	Iat   int64  `json:"iat"`
	Email string `json:"email,omitempty"`
	Nonce string `json:"nonce,omitempty"`
}

type Signer struct {
	Kid string
	Key *rsa.PrivateKey
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (s *Signer) Sign(c Claims) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": s.Kid})
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	input := b64(header) + "." + b64(payload)
	h := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.Key, crypto.SHA256, h[:])
	if err != nil {
		return "", err
	}
	return input + "." + b64(sig), nil
}

// VerifyJWT checks structure, alg (RS256 only, so "none" and friends are rejected),
// signature against the kid's key, expiry, issuer, and audience.
func VerifyJWT(token string, keys map[string]*rsa.PublicKey, iss, aud string, now time.Time) (Claims, error) {
	var c Claims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return c, errors.New("jwt: malformed")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return c, errors.New("jwt: bad header encoding")
	}
	var h struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &h); err != nil {
		return c, errors.New("jwt: bad header")
	}
	if h.Alg != "RS256" {
		return c, fmt.Errorf("jwt: alg %q rejected", h.Alg)
	}
	pub, ok := keys[h.Kid]
	if !ok {
		return c, fmt.Errorf("jwt: unknown kid %q", h.Kid)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return c, errors.New("jwt: bad signature encoding")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		return c, errors.New("jwt: bad signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return c, errors.New("jwt: bad payload encoding")
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return c, errors.New("jwt: bad payload")
	}
	if now.Unix() >= c.Exp {
		return c, errors.New("jwt: expired")
	}
	if c.Iss != iss {
		return c, fmt.Errorf("jwt: issuer %q != %q", c.Iss, iss)
	}
	if c.Aud != aud {
		return c, fmt.Errorf("jwt: audience %q != %q", c.Aud, aud)
	}
	return c, nil
}

// JWKS renders public keys as an RFC 7517 key set.
func JWKS(keys map[string]*rsa.PublicKey) ([]byte, error) {
	type jwk struct {
		Kty string `json:"kty"`
		Alg string `json:"alg"`
		Use string `json:"use"`
		Kid string `json:"kid"`
		N   string `json:"n"`
		E   string `json:"e"`
	}
	out := struct {
		Keys []jwk `json:"keys"`
	}{Keys: []jwk{}}
	for kid, k := range keys {
		out.Keys = append(out.Keys, jwk{
			Kty: "RSA", Alg: "RS256", Use: "sig", Kid: kid,
			N: b64(k.N.Bytes()),
			E: b64(big.NewInt(int64(k.E)).Bytes()),
		})
	}
	return json.Marshal(out)
}

// ParseJWKS is the consumer side (used by market and tests).
func ParseJWKS(data []byte) (map[string]*rsa.PublicKey, error) {
	var in struct {
		Keys []struct {
			Kty, Kid, N, E string
		} `json:"keys"`
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return nil, err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range in.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, err
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, err
		}
		keys[k.Kid] = &rsa.PublicKey{
			N: new(big.Int).SetBytes(n),
			E: int(new(big.Int).SetBytes(e).Int64()),
		}
	}
	return keys, nil
}

// LoadOrCreateSigner returns the newest active signing key, generating and
// persisting one on first boot. The table supports multiple keys so JWKS can
// serve old + new during rotation (rotation job lands in Phase 3).
func LoadOrCreateSigner(ctx context.Context, pool *pgxpool.Pool) (*Signer, error) {
	var kid, pemStr string
	err := pool.QueryRow(ctx,
		`SELECT kid, private_pem FROM id.signing_keys WHERE active ORDER BY created_at DESC LIMIT 1`).
		Scan(&kid, &pemStr)
	switch {
	case err == nil:
		block, _ := pem.Decode([]byte(pemStr))
		if block == nil {
			return nil, errors.New("signing key: bad pem")
		}
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		return &Signer{Kid: kid, Key: key}, nil
	case errors.Is(err, pgx.ErrNoRows):
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, err
		}
		kidBytes := make([]byte, 8)
		rand.Read(kidBytes)
		kid = hex.EncodeToString(kidBytes)
		pemStr = string(pem.EncodeToMemory(&pem.Block{
			Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
		if _, err := pool.Exec(ctx,
			`INSERT INTO id.signing_keys (kid, private_pem) VALUES ($1, $2)`, kid, pemStr); err != nil {
			return nil, err
		}
		return &Signer{Kid: kid, Key: key}, nil
	default:
		return nil, err
	}
}

// PublicKeys lists every signing key (active and retired) for the JWKS endpoint.
func PublicKeys(ctx context.Context, pool *pgxpool.Pool) (map[string]*rsa.PublicKey, error) {
	rows, err := pool.Query(ctx, `SELECT kid, private_pem FROM id.signing_keys`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := map[string]*rsa.PublicKey{}
	for rows.Next() {
		var kid, pemStr string
		if err := rows.Scan(&kid, &pemStr); err != nil {
			return nil, err
		}
		block, _ := pem.Decode([]byte(pemStr))
		if block == nil {
			return nil, errors.New("signing key: bad pem")
		}
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		keys[kid] = &key.PublicKey
	}
	return keys, rows.Err()
}
