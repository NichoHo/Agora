// Package sale: limited-inventory drops. Redis is the hot-path reservation
// authority; Postgres is the durable record, reconciled asynchronously. See
// docs/adr/0001-redis-hot-path-authority.md.
package sale

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"

	"github.com/redis/go-redis/v9"
)

// claimScript atomically SPOPs one available unit id, starting at a random
// shard and walking the rest before declaring sold out (spec 7.3), then marks
// the claim with a TTL so Redis expiry (not app clock) governs the hold.
var claimScript = redis.NewScript(`
local n = #KEYS - 1
local start = tonumber(ARGV[2])
for i = 0, n - 1 do
	local idx = ((start + i - 1) % n) + 1
	local listing = redis.call('SPOP', KEYS[idx])
	if listing then
		redis.call('SET', KEYS[n+1], listing, 'EX', ARGV[1])
		return listing
	end
end
return false
`)

type Redis struct {
	rdb *redis.Client
}

func NewRedis(addr string) *Redis {
	return &Redis{rdb: redis.NewClient(&redis.Options{Addr: addr})}
}

func (r *Redis) Close() error                   { return r.rdb.Close() }
func (r *Redis) Ping(ctx context.Context) error { return r.rdb.Ping(ctx).Err() }

func shardKey(dropID string, shard int) string {
	return fmt.Sprintf("sale:%s:stock:%d", dropID, shard)
}

func reservationKey(dropID, reservationID string) string {
	return fmt.Sprintf("sale:%s:reservation:%s", dropID, reservationID)
}

func queueKey(dropID string) string    { return fmt.Sprintf("sale:%s:queue", dropID) }
func admittedKey(dropID string) string { return fmt.Sprintf("sale:%s:admitted", dropID) }

// Seed loads a drop's freshly minted unit ids into their home shards.
func (r *Redis) Seed(ctx context.Context, dropID string, unitsByShard map[int][]string) error {
	pipe := r.rdb.Pipeline()
	for shard, ids := range unitsByShard {
		key := shardKey(dropID, shard)
		for _, id := range ids {
			pipe.SAdd(ctx, key, id)
		}
	}
	_, err := pipe.Exec(ctx)
	return err
}

// Claim atomically pops one available unit for dropID. Returns ("", nil) when
// every shard is empty (sold out).
func (r *Redis) Claim(ctx context.Context, dropID, reservationID string, shardCount int, ttlSeconds int64) (string, error) {
	keys := make([]string, shardCount+1)
	for i := 0; i < shardCount; i++ {
		keys[i] = shardKey(dropID, i)
	}
	keys[shardCount] = reservationKey(dropID, reservationID)
	start, err := rand.Int(rand.Reader, big.NewInt(int64(shardCount)))
	if err != nil {
		return "", err
	}
	res, err := claimScript.Run(ctx, r.rdb, keys, ttlSeconds, start.Int64()+1).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if ok, isBool := res.(bool); isBool && !ok {
		return "", nil // sold out
	}
	listing, _ := res.(string)
	return listing, nil
}

// Release returns a unit to its home shard: TTL expiry or an explicit cancel.
func (r *Redis) Release(ctx context.Context, dropID string, shard int, listingID string) error {
	return r.rdb.SAdd(ctx, shardKey(dropID, shard), listingID).Err()
}

// Remaining sums units left across all shards. Approximate under concurrent
// claims; never treated as authoritative, only shown on the landing page.
func (r *Redis) Remaining(ctx context.Context, dropID string, shardCount int) (int, error) {
	pipe := r.rdb.Pipeline()
	cmds := make([]*redis.IntCmd, shardCount)
	for i := 0; i < shardCount; i++ {
		cmds[i] = pipe.SCard(ctx, shardKey(dropID, i))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	total := 0
	for _, c := range cmds {
		total += int(c.Val())
	}
	return total, nil
}
