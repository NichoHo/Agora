// Seed: demo OAuth client, users, categories, listings. Idempotent.
package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"

	"agora/internal/id"
	"agora/internal/pay"
	"agora/internal/pg"
	"agora/migrations"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type item struct {
	title, desc, cat, img string
	price                 int64
}

var items = []item{
	{"Nikon FM2 film camera", "Fully mechanical SLR, recently serviced. Light seals replaced.", "electronics", "/seed/nikon_fm2_camera_1785689479003.jpg", 42000},
	{"Uniqlo U crew neck tee (M, navy)", "Worn twice, no stains. Smoke-free home.", "fashion", "/seed/uniqlo_navy_tee_1785689493029.jpg", 900},
	{"The Design of Everyday Things", "Don Norman. Paperback, light shelf wear.", "books", "/seed/design_everyday_things_1785689503937.jpg", 1200},
	{"Balmuda kettle (white)", "Two years old, descaled monthly. Original box.", "home", "/seed/balmuda_kettle_1785689513116.jpg", 8500},
	{"Gundam RX-78-2 MG kit (unbuilt)", "Sealed box, bought duplicate by mistake.", "hobby", "/seed/gundam_rx782_1785689521246.jpg", 4300},
	{"Sony WH-1000XM4 headphones", "Earpads replaced with official parts last month.", "electronics", "/seed/sony_headphones_1785689531607.jpg", 19800},
	{"Levi's 501 (W32 L32)", "Classic straight fit, honest fade.", "fashion", "/seed/levis_501_1785689551471.jpg", 5600},
	{"Norwegian Wood by Murakami", "English paperback, good condition.", "books", "/seed/norwegian_wood_book_1785689562218.jpg", 800},
	{"Muji oak desk lamp", "Warm LED, dimmer works perfectly.", "home", "/seed/muji_desk_lamp_1785689572434.jpg", 3200},
	{"Shimano 105 rear derailleur", "Taken off an upgrade build, ~500km use.", "hobby", "/seed/shimano_derailleur_1785689607870.jpg", 6200},
	{"iPad (9th gen, 64GB, WiFi)", "Screen protector since day one. Battery 89%.", "electronics", "/seed/ipad_9th_gen_1785689618999.jpg", 28000},
	{"Vintage seiko 5 automatic", "Runs +10s/day. New strap.", "hobby", "/seed/seiko_5_watch_1785689647939.jpg", 15500},
}

func main() {
	ctx := context.Background()
	pool, err := pg.Connect(ctx, env("DATABASE_URL", "postgres://vault:vault@localhost:5432/vault"))
	if err != nil {
		slog.Error("connect", "err", err)
		os.Exit(1)
	}
	// migrations are idempotent; running them here makes seed order-independent
	for _, schema := range []string{"id", "market", "pay"} {
		sub, _ := fs.Sub(migrations.FS, schema)
		if err := pg.Migrate(ctx, pool, schema, sub); err != nil {
			slog.Error("migrate", "schema", schema, "err", err)
			os.Exit(1)
		}
	}

	must := func(err error) {
		if err != nil {
			slog.Error("seed", "err", err)
			os.Exit(1)
		}
	}

	_, err = pool.Exec(ctx,
		`INSERT INTO id.oauth_clients (id, name, redirect_uris) VALUES ('vault-web', 'Vault Storefront', $1)
		 ON CONFLICT (id) DO UPDATE SET redirect_uris = $1`,
		[]string{env("WEB_CALLBACK", "http://localhost:3001/auth/callback")})
	must(err)

	userIDs := map[string]string{}
	for _, u := range []struct{ email, handle string }{
		{"alice@vault.test", "alice"}, {"bob@vault.test", "bob"},
	} {
		hash, err := id.HashPassword("password123!")
		must(err)
		// select-then-insert instead of upsert: handle may clash with users
		// created by tests sharing this dev database
		var uid string
		if err := pool.QueryRow(ctx, `SELECT id FROM id.users WHERE email = $1`, u.email).Scan(&uid); err != nil {
			if err := pool.QueryRow(ctx,
				`INSERT INTO id.users (email, handle) VALUES ($1, $2) RETURNING id`,
				u.email, u.handle).Scan(&uid); err != nil {
				must(pool.QueryRow(ctx,
					`INSERT INTO id.users (email, handle) VALUES ($1, $2) RETURNING id`,
					u.email, u.handle+"_demo").Scan(&uid))
			}
		}
		_, err = pool.Exec(ctx,
			`INSERT INTO id.credentials (user_id, password_hash) VALUES ($1, $2)
			 ON CONFLICT (user_id) DO NOTHING`, uid, hash)
		must(err)
		userIDs[u.handle] = uid
	}

	cats := map[string]int{}
	for _, c := range []struct{ name, slug string }{
		{"Electronics", "electronics"}, {"Fashion", "fashion"}, {"Books", "books"},
		{"Home", "home"}, {"Hobby", "hobby"}, {"Other", "other"},
	} {
		var cid int
		must(pool.QueryRow(ctx,
			`INSERT INTO market.categories (name, slug) VALUES ($1, $2)
			 ON CONFLICT (slug) DO UPDATE SET name = excluded.name RETURNING id`,
			c.name, c.slug).Scan(&cid))
		cats[c.slug] = cid
	}

	// wallet head start so the escrow demo works immediately
	ledger := &pay.Ledger{Pool: pool}
	for handle, uid := range userIDs {
		_, err := ledger.Deposit(ctx, "seed:deposit:"+handle, uid, 100_000)
		must(err)
	}

	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM market.listings`).Scan(&n))
	if n > 0 {
		fmt.Println("listings already seeded, skipping")
		return
	}
	sellers := []string{userIDs["alice"], userIDs["bob"]}
	for i, it := range items {
		_, err := pool.Exec(ctx,
			`INSERT INTO market.listings (seller_id, category_id, title, description, price_minor, image_url)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			sellers[i%2], cats[it.cat], it.title, it.desc, it.price, it.img)
		must(err)
	}
	fmt.Printf("seeded %d listings, %d users\n", len(items), len(userIDs))
}
