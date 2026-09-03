package sale

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"agora/internal/authn"
	"agora/internal/httpx"
)

type Drop struct {
	ID           string    `json:"id"`
	SellerID     string    `json:"seller_id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	ImageURL     string    `json:"image_url"`
	StartsAt     time.Time `json:"starts_at"`
	TotalUnits   int       `json:"total_units"`
	PriceMinor   int64     `json:"price_minor"`
	UnitsPerUser int       `json:"units_per_user"`
	ShardCount   int       `json:"shard_count"`
	Status       string    `json:"status"`
}

const dropCols = `id, seller_id, title, description, image_url, starts_at, total_units, price_minor, units_per_user, shard_count, status`

func scanDrop(row pgx.Row) (Drop, error) {
	var d Drop
	err := row.Scan(&d.ID, &d.SellerID, &d.Title, &d.Description, &d.ImageURL, &d.StartsAt,
		&d.TotalUnits, &d.PriceMinor, &d.UnitsPerUser, &d.ShardCount, &d.Status)
	return d, err
}

func (s *Server) getDrop(ctx context.Context, id string) (Drop, error) {
	return scanDrop(s.pool.QueryRow(ctx, `SELECT `+dropCols+` FROM sale.drops WHERE id = $1`, id))
}

func (s *Server) handleCreateDrop(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title        string    `json:"title"`
		Description  string    `json:"description"`
		ImageURL     string    `json:"image_url"`
		StartsAt     time.Time `json:"starts_at"`
		TotalUnits   int       `json:"total_units"`
		PriceMinor   int64     `json:"price_minor"`
		UnitsPerUser int       `json:"units_per_user"`
		ShardCount   int       `json:"shard_count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.Error(w, 400, "bad json")
		return
	}
	if strings.TrimSpace(in.Title) == "" || in.TotalUnits <= 0 || in.PriceMinor <= 0 {
		httpx.Error(w, 400, "title, total_units, and price_minor are required")
		return
	}
	if in.UnitsPerUser <= 0 {
		in.UnitsPerUser = 1
	}
	if in.ShardCount <= 0 {
		in.ShardCount = 8
	}
	d, err := scanDrop(s.pool.QueryRow(r.Context(),
		`INSERT INTO sale.drops (seller_id, title, description, image_url, starts_at, total_units, price_minor, units_per_user, shard_count)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+dropCols,
		authn.UserID(r.Context()), in.Title, in.Description, in.ImageURL, in.StartsAt,
		in.TotalUnits, in.PriceMinor, in.UnitsPerUser, in.ShardCount))
	if err != nil {
		httpx.Error(w, 400, "invalid drop")
		return
	}
	httpx.JSON(w, 201, d)
}

// handleOpenDrop mints total_units real market listings (one per sellable
// unit, see the ADR), seeds their ids into Redis shards, and flips the drop
// live. Synchronous and seller-token-authenticated; run once, well before
// starts_at, not on any hot path.
func (s *Server) handleOpenDrop(w http.ResponseWriter, r *http.Request) {
	d, err := s.getDrop(r.Context(), r.PathValue("id"))
	if err != nil {
		httpx.Error(w, 404, "not found")
		return
	}
	if d.SellerID != authn.UserID(r.Context()) {
		httpx.Error(w, 403, "only the seller can open this drop")
		return
	}
	if d.Status != "scheduled" {
		httpx.Error(w, 409, "drop is not scheduled")
		return
	}
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	unitsByShard := make(map[int][]string, d.ShardCount)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	defer tx.Rollback(r.Context())
	for i := 0; i < d.TotalUnits; i++ {
		listingID, err := s.market.CreateListing(r.Context(), bearer, d.Title, d.Description, d.ImageURL, d.PriceMinor)
		if err != nil {
			httpx.Error(w, 502, "market unavailable while minting units")
			return
		}
		shard := i % d.ShardCount
		if _, err := tx.Exec(r.Context(),
			`INSERT INTO sale.drop_units (drop_id, shard, listing_id) VALUES ($1,$2,$3)`,
			d.ID, shard, listingID); err != nil {
			httpx.Error(w, 500, "db")
			return
		}
		unitsByShard[shard] = append(unitsByShard[shard], listingID)
	}
	if _, err := tx.Exec(r.Context(), `UPDATE sale.drops SET status = 'open' WHERE id = $1`, d.ID); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if err := s.rdb.Seed(r.Context(), d.ID, unitsByShard); err != nil {
		httpx.Error(w, 500, "redis seed")
		return
	}
	d.Status = "open"
	httpx.JSON(w, 200, d)
}

type dropView struct {
	Drop
	Remaining int `json:"remaining"`
}

// handleGetDrop is the drop landing read: single-flight guarded so a cold
// cache under a stampede issues one Postgres+Redis read, not N (spec 7.7).
func (s *Server) handleGetDrop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err, _ := s.dropSF.Do(id, func() (any, error) {
		d, err := s.getDrop(r.Context(), id)
		if err != nil {
			return nil, err
		}
		remaining, err := s.rdb.Remaining(r.Context(), id, d.ShardCount)
		if err != nil {
			return nil, err
		}
		return dropView{Drop: d, Remaining: remaining}, nil
	})
	if err != nil {
		httpx.Error(w, 404, "not found")
		return
	}
	httpx.JSON(w, 200, v)
}
