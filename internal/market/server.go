package market

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"agora/internal/authn"
	"agora/internal/httpx"
)

type Server struct {
	pool *pgxpool.Pool
	auth *authn.Verifier
	pay  *PayClient
	mux  *http.ServeMux
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func NewServer(pool *pgxpool.Pool, auth *authn.Verifier, pay *PayClient) *Server {
	s := &Server{pool: pool, auth: auth, pay: pay}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /categories", s.handleCategories)
	mux.HandleFunc("GET /listings", s.handleList)
	mux.HandleFunc("GET /listings/{id}", s.handleGet)
	mux.HandleFunc("POST /listings", s.auth.Require(s.handleCreate))
	mux.HandleFunc("PATCH /listings/{id}", s.auth.Require(s.handlePatch))
	mux.HandleFunc("GET /mine", s.auth.Require(s.handleMine))
	s.orderRoutes(mux)
	s.mux = mux
	return s
}

type Listing struct {
	ID          string    `json:"id"`
	SellerID    string    `json:"seller_id"`
	CategoryID  *int      `json:"category_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	PriceMinor  int64     `json:"price_minor"`
	Currency    string    `json:"currency"`
	Status      string    `json:"status"`
	ImageURL    string    `json:"image_url"`
	CreatedAt   time.Time `json:"created_at"`
}

const listingCols = `id, seller_id, category_id, title, description, price_minor, currency, status, image_url, created_at`

func scanListing(row pgx.Row) (Listing, error) {
	var l Listing
	err := row.Scan(&l.ID, &l.SellerID, &l.CategoryID, &l.Title, &l.Description,
		&l.PriceMinor, &l.Currency, &l.Status, &l.ImageURL, &l.CreatedAt)
	return l, err
}

func (s *Server) handleCategories(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(), `SELECT id, name, slug FROM market.categories ORDER BY id`)
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	defer rows.Close()
	type cat struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	out := []cat{}
	for rows.Next() {
		var c cat
		if err := rows.Scan(&c.ID, &c.Name, &c.Slug); err != nil {
			httpx.Error(w, 500, "db")
			return
		}
		out = append(out, c)
	}
	httpx.JSON(w, 200, out)
}

func encodeCursor(t time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.Format(time.RFC3339Nano) + "|" + id))
}

func decodeCursor(c string) (time.Time, string, error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, "", err
	}
	at, id, ok := strings.Cut(string(b), "|")
	if !ok {
		return time.Time{}, "", errors.New("bad cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	return t, id, err
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 24
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 100 {
		limit = n
	}
	sql := `SELECT ` + listingCols + ` FROM market.listings WHERE status = 'active'`
	args := []any{}
	if search := q.Get("q"); search != "" {
		args = append(args, search)
		sql += fmt.Sprintf(` AND search @@ plainto_tsquery('simple', $%d)`, len(args))
	}
	if cat := q.Get("category"); cat != "" {
		catID, err := strconv.Atoi(cat)
		if err != nil {
			httpx.Error(w, 400, "bad category")
			return
		}
		args = append(args, catID)
		sql += fmt.Sprintf(` AND category_id = $%d`, len(args))
	}
	if cur := q.Get("cursor"); cur != "" {
		at, lastID, err := decodeCursor(cur)
		if err != nil {
			httpx.Error(w, 400, "bad cursor")
			return
		}
		args = append(args, at, lastID)
		sql += fmt.Sprintf(` AND (created_at, id) < ($%d, $%d)`, len(args)-1, len(args))
	}
	args = append(args, limit+1)
	sql += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args))

	rows, err := s.pool.Query(r.Context(), sql, args...)
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	defer rows.Close()
	items := []Listing{}
	for rows.Next() {
		l, err := scanListing(rows)
		if err != nil {
			httpx.Error(w, 500, "db")
			return
		}
		items = append(items, l)
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	httpx.JSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	l, err := scanListing(s.pool.QueryRow(r.Context(),
		`SELECT `+listingCols+` FROM market.listings WHERE id = $1`, r.PathValue("id")))
	if err != nil {
		httpx.Error(w, 404, "not found")
		return
	}
	httpx.JSON(w, 200, l)
}

type listingInput struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	PriceMinor  int64  `json:"price_minor"`
	CategoryID  *int   `json:"category_id"`
	ImageURL    string `json:"image_url"`
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var in listingInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.Error(w, 400, "bad json")
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || len(in.Title) > 120 {
		httpx.Error(w, 400, "title must be 1-120 characters")
		return
	}
	if in.PriceMinor <= 0 {
		httpx.Error(w, 400, "price must be positive")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	defer tx.Rollback(r.Context())
	l, err := scanListing(tx.QueryRow(r.Context(),
		`INSERT INTO market.listings (seller_id, category_id, title, description, price_minor, image_url)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+listingCols,
		authn.UserID(r.Context()), in.CategoryID, in.Title, in.Description, in.PriceMinor, in.ImageURL))
	if err != nil {
		httpx.Error(w, 400, "invalid listing")
		return
	}
	if err := outboxTx(r.Context(), tx, "listing.created", map[string]any{
		"listing_id": l.ID, "seller_id": l.SellerID, "price_minor": l.PriceMinor}); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	httpx.JSON(w, 201, l)
}

func (s *Server) handlePatch(w http.ResponseWriter, r *http.Request) {
	var sellerID string
	err := s.pool.QueryRow(r.Context(),
		`SELECT seller_id FROM market.listings WHERE id = $1`, r.PathValue("id")).Scan(&sellerID)
	if err != nil {
		httpx.Error(w, 404, "not found")
		return
	}
	if sellerID != authn.UserID(r.Context()) {
		httpx.Error(w, 403, "not your listing")
		return
	}
	var in struct {
		Title       *string `json:"title"`
		Description *string `json:"description"`
		PriceMinor  *int64  `json:"price_minor"`
		CategoryID  *int    `json:"category_id"`
		ImageURL    *string `json:"image_url"`
		Status      *string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.Error(w, 400, "bad json")
		return
	}
	l, err := scanListing(s.pool.QueryRow(r.Context(),
		`UPDATE market.listings SET
		   title = COALESCE($2, title),
		   description = COALESCE($3, description),
		   price_minor = COALESCE($4, price_minor),
		   category_id = COALESCE($5, category_id),
		   image_url = COALESCE($6, image_url),
		   status = COALESCE($7, status),
		   updated_at = now()
		 WHERE id = $1 RETURNING `+listingCols,
		r.PathValue("id"), in.Title, in.Description, in.PriceMinor, in.CategoryID, in.ImageURL, in.Status))
	if err != nil {
		httpx.Error(w, 400, "invalid update")
		return
	}
	httpx.JSON(w, 200, l)
}

func (s *Server) handleMine(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(),
		`SELECT `+listingCols+` FROM market.listings WHERE seller_id = $1 ORDER BY created_at DESC`,
		authn.UserID(r.Context()))
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	defer rows.Close()
	items := []Listing{}
	for rows.Next() {
		l, err := scanListing(rows)
		if err != nil {
			httpx.Error(w, 500, "db")
			return
		}
		items = append(items, l)
	}
	httpx.JSON(w, 200, items)
}
