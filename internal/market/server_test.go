package market

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"agora/internal/authn"
	"agora/internal/id"
	"agora/internal/pg"
	"agora/migrations"
)

const testIssuer = "http://issuer.test"

type env struct {
	ts      *httptest.Server
	signer  *id.Signer
	srv     *Server
	jwksURL string
}

func (e *env) token(t *testing.T, sub string) string {
	t.Helper()
	now := time.Now()
	tok, err := e.signer.Sign(id.Claims{Iss: testIssuer, Sub: sub, Aud: "vault",
		Exp: now.Add(time.Hour).Unix(), Iat: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (e *env) do(t *testing.T, method, path, token string, body any) *http.Response {
	t.Helper()
	var rd *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func testEnv(t *testing.T) *env {
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
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS market CASCADE`); err != nil {
		t.Fatal(err)
	}
	sub, _ := fs.Sub(migrations.FS, "market")
	if err := pg.Migrate(ctx, pool, "market", sub); err != nil {
		t.Fatal(err)
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer := &id.Signer{Kid: "market-test", Key: key}
	jwksDoc, err := id.JWKS(map[string]*rsa.PublicKey{signer.Kid: &key.PublicKey})
	if err != nil {
		t.Fatal(err)
	}
	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(jwksDoc)
	}))
	t.Cleanup(jwksSrv.Close)

	srv := NewServer(pool, authn.New(jwksSrv.URL, testIssuer), nil)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return &env{ts: ts, signer: signer, srv: srv, jwksURL: jwksSrv.URL}
}

const alice = "11111111-1111-1111-1111-111111111111"
const bob = "22222222-2222-2222-2222-222222222222"

func TestListingsCRUD(t *testing.T) {
	e := testEnv(t)
	at := e.token(t, alice)

	// no token → 401
	if resp := e.do(t, "POST", "/listings", "", map[string]any{"title": "x", "price_minor": 100}); resp.StatusCode != 401 {
		t.Fatalf("unauthenticated create: want 401, got %d", resp.StatusCode)
	}
	// garbage token → 401
	if resp := e.do(t, "POST", "/listings", "garbage.token.here",
		map[string]any{"title": "x", "price_minor": 100}); resp.StatusCode != 401 {
		t.Fatalf("garbage token: want 401, got %d", resp.StatusCode)
	}

	// create
	resp := e.do(t, "POST", "/listings", at, map[string]any{
		"title": "Vintage film SLR camera", "description": "classic, works great", "price_minor": 42000})
	if resp.StatusCode != 201 {
		t.Fatalf("create: want 201, got %d", resp.StatusCode)
	}
	var l Listing
	json.NewDecoder(resp.Body).Decode(&l)
	if l.SellerID != alice || l.Status != "active" || l.Currency != "USD" {
		t.Fatalf("bad listing: %+v", l)
	}

	// get
	if resp := e.do(t, "GET", "/listings/"+l.ID, "", nil); resp.StatusCode != 200 {
		t.Fatalf("get: want 200, got %d", resp.StatusCode)
	}

	// owner patch
	resp = e.do(t, "PATCH", "/listings/"+l.ID, at, map[string]any{"price_minor": 39000})
	if resp.StatusCode != 200 {
		t.Fatalf("patch: want 200, got %d", resp.StatusCode)
	}
	json.NewDecoder(resp.Body).Decode(&l)
	if l.PriceMinor != 39000 {
		t.Fatalf("price not updated: %+v", l)
	}

	// non-owner patch → 403
	if resp := e.do(t, "PATCH", "/listings/"+l.ID, e.token(t, bob),
		map[string]any{"price_minor": 1}); resp.StatusCode != 403 {
		t.Fatalf("non-owner patch: want 403, got %d", resp.StatusCode)
	}

	// invalid status rejected
	if resp := e.do(t, "PATCH", "/listings/"+l.ID, at, map[string]any{"status": "bogus"}); resp.StatusCode != 400 {
		t.Fatalf("bogus status: want 400, got %d", resp.StatusCode)
	}

	// reserved/sold are the order flow's to set, not a direct seller edit
	// (a listing stuck at "reserved" with no real order would let this
	// endpoint reopen it for a second buyer mid-payment)
	if resp := e.do(t, "PATCH", "/listings/"+l.ID, at, map[string]any{"status": "reserved"}); resp.StatusCode != 400 {
		t.Fatalf("direct reserved: want 400, got %d", resp.StatusCode)
	}
	if resp := e.do(t, "PATCH", "/listings/"+l.ID, at, map[string]any{"status": "sold"}); resp.StatusCode != 400 {
		t.Fatalf("direct sold: want 400, got %d", resp.StatusCode)
	}

	// FTS: word in title found, absent word not
	search := func(q string) int {
		resp := e.do(t, "GET", "/listings?q="+q, "", nil)
		var out struct {
			Items []Listing `json:"items"`
		}
		json.NewDecoder(resp.Body).Decode(&out)
		return len(out.Items)
	}
	if n := search("slr"); n != 1 {
		t.Fatalf("search slr: want 1, got %d", n)
	}
	if n := search("zeppelin"); n != 0 {
		t.Fatalf("search zeppelin: want 0, got %d", n)
	}

	// withdrawn listings leave the feed
	e.do(t, "PATCH", "/listings/"+l.ID, at, map[string]any{"status": "withdrawn"})
	if n := search("slr"); n != 0 {
		t.Fatalf("withdrawn still in feed: got %d", n)
	}

	// /mine still shows it
	resp = e.do(t, "GET", "/mine", at, nil)
	var mine []Listing
	json.NewDecoder(resp.Body).Decode(&mine)
	if len(mine) != 1 {
		t.Fatalf("mine: want 1, got %d", len(mine))
	}
}
