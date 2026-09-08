# Load tests (AGORA_SPEC.md section 11)

Five k6 scenarios. Run with the official image, no local k6 install needed,
joined to the compose network so service names resolve. Mount the directory
rather than piping the script over stdin: 01, 02, 04, and 05 all `import`
from `./lib/`, which only resolves when k6 can see the file on disk.

```bash
docker run --rm --network vault_default -v "$(pwd)/load:/load" -w /load \
  -e SALE_URL=http://sale:8085 -e MARKET_URL=http://market:8082 \
  -e PAY_URL=http://pay:8083 -e ID_URL=http://id:8081 \
  grafana/k6 run 01-drop-spike.js
```

(`WEB_ORIGIN` stays its default, `http://localhost:3001`: it only needs to
match `id`'s registered `oauth_clients.redirect_uris` string exactly, the
scripts never actually follow that redirect.)

## Before each drop scenario (01, 02)

`setup()` opens the drop and prints its id, but sale has no bulk-admit
endpoint (spec 7.2's admission queue fills organically); load tests reach
into Redis directly instead of adding one just for this:

```bash
docker compose exec redis redis-cli SMEMBERS sale:<drop-id>:queue
# or, simpler: admit every user in the pool k6 just registered:
docker compose exec -T db psql -U vault -d vault -c \
  "SELECT id FROM id.users WHERE email LIKE 'loadtest-%'" -t -A | \
  xargs docker compose exec -T redis redis-cli SADD sale:<drop-id>:admitted
```

## After 04 and 05 (anything touching pay)

```bash
docker compose exec -T db psql -U vault -d vault < load/verify_invariants.sql
```

(`-f load/verify_invariants.sql` would look for that path inside the db
container, which doesn't have it; pipe the file from the host instead.)

All three queries must return 0 (or 0 for the sum). See
`internal/pay/ledger_test.go`'s `checkBooks` for the in-process version of
the same three checks.

## Scenario 4 needs switch running

`04-checkout-e2e.js` tests pay's real card-authorization path (ADR 0003), so
bring up `switch`'s own compose stack first and set `SWITCH_URL`/`SWITCH_API_KEY`
on `pay` (see `docker-compose.yml`'s comment on the `pay` service).
`05-ledger-write.js` is the deliberate opposite: run it with `SWITCH_URL`
unset, so it measures the ledger alone.

## Hardware and honesty

Numbers in `results/` were captured on the machine running this session
(see each result file's header for exact spec), not the single cloud
instance section 11 assumes. Docker Desktop's networking layer adds
overhead a bare-metal or cloud VM wouldn't have. Any target missed is
reported as a miss with the likely reason, not omitted; see
`docs/writeups/05-load-test-results.md`.

## Why 100-300 identities, not thousands

Each real identity costs a full register + login + consent + PKCE token
exchange (`load/lib/auth.js`) against `id`'s real OIDC flow, not a shortcut
around it. `setup()` runs these sequentially and once per scenario, so the
pool size is capped by how long a test run can reasonably wait on setup,
not by anything sale/pay enforce. What each scenario measures (the atomic
Redis claim's throughput; the ledger's write throughput) doesn't depend on
identity count, only on concurrent request volume, which the k6 executor
config controls independently of pool size.
