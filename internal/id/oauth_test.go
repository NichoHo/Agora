package id

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"agora/internal/pg"
	"agora/migrations"
)

const (
	testIssuer   = "http://issuer.test"
	testClient   = "vault-web"
	testRedirect = "http://web.test/auth/callback"
)

// testEnv boots the full id server against the TEST_DATABASE_URL database
// (schema recreated per run). Skips when no database is available.
func testEnv(t *testing.T) (*httptest.Server, *http.Client, *pgxpool.Pool) {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pg.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS id CASCADE`); err != nil {
		t.Fatal(err)
	}
	sub, _ := fs.Sub(migrations.FS, "id")
	if err := pg.Migrate(ctx, pool, "id", sub); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO id.oauth_clients (id, name, redirect_uris) VALUES ($1, 'Vault Web', $2)`,
		testClient, []string{testRedirect}); err != nil {
		t.Fatal(err)
	}
	signer, err := LoadOrCreateSigner(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(NewServer(pool, signer, testIssuer, "http://web.test"))
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return ts, client, pool
}

func postJSON(t *testing.T, c *http.Client, url string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := c.Post(url, "application/json", strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func register(t *testing.T, c *http.Client, ts *httptest.Server, email, handle string) {
	t.Helper()
	resp := postJSON(t, c, ts.URL+"/register",
		map[string]string{"email": email, "password": "password123!", "handle": handle})
	if resp.StatusCode != 201 {
		t.Fatalf("register: %d", resp.StatusCode)
	}
}

func pkce() (verifier, challenge string) {
	verifier = randToken(32)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, b64(sum[:])
}

func authorizeURL(ts *httptest.Server, challenge, state string) string {
	q := url.Values{
		"response_type": {"code"}, "client_id": {testClient}, "redirect_uri": {testRedirect},
		"scope": {"openid profile"}, "state": {state},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}
	return ts.URL + "/authorize?" + q.Encode()
}

// getCode drives login->consent->code for an already-registered session.
func getCode(t *testing.T, c *http.Client, ts *httptest.Server, challenge string) string {
	t.Helper()
	resp, err := c.Get(authorizeURL(ts, challenge, "st4te"))
	if err != nil {
		t.Fatal(err)
	}
	loc := resp.Header.Get("Location")
	if resp.StatusCode != 302 {
		t.Fatalf("authorize: %d", resp.StatusCode)
	}
	if strings.Contains(loc, "/auth/consent") {
		r2 := postJSON(t, c, ts.URL+"/consent", map[string]string{"clientId": testClient, "scope": "openid profile"})
		if r2.StatusCode != 204 {
			t.Fatalf("consent: %d", r2.StatusCode)
		}
		resp, err = c.Get(authorizeURL(ts, challenge, "st4te"))
		if err != nil {
			t.Fatal(err)
		}
		loc = resp.Header.Get("Location")
	}
	u, err := url.Parse(loc)
	if err != nil || !strings.HasPrefix(loc, testRedirect) {
		t.Fatalf("expected redirect to client, got %q", loc)
	}
	if u.Query().Get("state") != "st4te" {
		t.Fatalf("state not round-tripped: %q", loc)
	}
	code := u.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in %q", loc)
	}
	return code
}

func exchange(t *testing.T, c *http.Client, ts *httptest.Server, code, verifier string) *http.Response {
	t.Helper()
	resp, err := c.PostForm(ts.URL+"/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {testRedirect}, "client_id": {testClient}, "code_verifier": {verifier},
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestOIDCRoundTripAndMisuse(t *testing.T) {
	ts, c, pool := testEnv(t)
	register(t, c, ts, "alice@test.dev", "alice")

	// unauthenticated authorize bounces to login
	fresh := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	_, challenge := pkce()
	resp, _ := fresh.Get(authorizeURL(ts, challenge, "s"))
	if resp.StatusCode != 302 || !strings.Contains(resp.Header.Get("Location"), "/auth/login") {
		t.Fatalf("want login redirect, got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// happy path
	verifier, challenge := pkce()
	code := getCode(t, c, ts, challenge)
	resp = exchange(t, c, ts, code, verifier)
	if resp.StatusCode != 200 {
		t.Fatalf("token: %d", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
		TokenType   string `json:"token_type"`
	}
	json.NewDecoder(resp.Body).Decode(&tok)
	if tok.TokenType != "Bearer" || tok.AccessToken == "" || tok.IDToken == "" {
		t.Fatalf("bad token response: %+v", tok)
	}

	// access token verifies against the served JWKS
	jr, err := http.Get(ts.URL + "/.well-known/jwks.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := io.ReadAll(jr.Body)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := ParseJWKS(doc)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := VerifyJWT(tok.AccessToken, keys, testIssuer, "vault", time.Now())
	if err != nil {
		t.Fatalf("verify access token: %v", err)
	}
	if claims.Email != "alice@test.dev" {
		t.Fatalf("wrong claims: %+v", claims)
	}

	// replayed code fails
	if resp := exchange(t, c, ts, code, verifier); resp.StatusCode != 400 {
		t.Fatalf("replayed code: want 400, got %d", resp.StatusCode)
	}

	// wrong verifier fails
	_, challenge2 := pkce()
	code2 := getCode(t, c, ts, challenge2)
	if resp := exchange(t, c, ts, code2, "not-the-verifier"); resp.StatusCode != 400 {
		t.Fatalf("wrong verifier: want 400, got %d", resp.StatusCode)
	}

	// expired code fails
	verifier3, challenge3 := pkce()
	code3 := getCode(t, c, ts, challenge3)
	if _, err := pool.Exec(context.Background(),
		`UPDATE id.auth_codes SET expires_at = now() - interval '1 minute' WHERE code = $1`, code3); err != nil {
		t.Fatal(err)
	}
	if resp := exchange(t, c, ts, code3, verifier3); resp.StatusCode != 400 {
		t.Fatalf("expired code: want 400, got %d", resp.StatusCode)
	}

	// unregistered redirect_uri is rejected without redirecting
	q := url.Values{
		"response_type": {"code"}, "client_id": {testClient},
		"redirect_uri":   {"http://evil.test/steal"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}
	resp, _ = c.Get(ts.URL + "/authorize?" + q.Encode())
	if resp.StatusCode != 400 {
		t.Fatalf("evil redirect_uri: want 400, got %d", resp.StatusCode)
	}

	// audit trail exists
	var n int
	pool.QueryRow(context.Background(),
		`SELECT count(*) FROM id.audit_events WHERE action IN ('user.register','token.issue','token.fail')`).Scan(&n)
	if n < 3 {
		t.Fatalf("expected audit events, got %d", n)
	}
}

func TestLoginMisuse(t *testing.T) {
	ts, c, _ := testEnv(t)
	register(t, c, ts, "bob@test.dev", "bob")

	if resp := postJSON(t, c, ts.URL+"/login",
		map[string]string{"email": "bob@test.dev", "password": "wrong-password"}); resp.StatusCode != 401 {
		t.Fatalf("wrong password: want 401, got %d", resp.StatusCode)
	}
	if resp := postJSON(t, c, ts.URL+"/login",
		map[string]string{"email": "ghost@test.dev", "password": "whatever123"}); resp.StatusCode != 401 {
		t.Fatalf("unknown user: want 401, got %d", resp.StatusCode)
	}
	if resp := postJSON(t, c, ts.URL+"/register",
		map[string]string{"email": "bob@test.dev", "password": "password123!", "handle": "bob2"}); resp.StatusCode != 409 {
		t.Fatalf("duplicate email: want 409, got %d", resp.StatusCode)
	}
	if resp := postJSON(t, c, ts.URL+"/login",
		map[string]string{"email": "bob@test.dev", "password": "password123!"}); resp.StatusCode != 200 {
		t.Fatalf("correct login: want 200, got %d", resp.StatusCode)
	}
}
