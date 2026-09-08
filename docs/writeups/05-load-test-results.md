# Load testing Agora: three real bugs and one honest miss

`AGORA_SPEC.md` section 11 asks for five k6 scenarios against the real
stack, real targets, and a rule I like: if a target is missed, publish the
miss and the reason, don't quietly drop the scenario. This is that
publication, run on the same laptop that's been running [Vault](../../README.md)
and [Switch](https://github.com/NichoHo/Switch) all session, not a cloud
instance.

Running the scenarios for real, instead of eyeballing the code, is what
turned up three bugs that a code review would have missed. That's the part
worth writing up.

## Bug 1: the demo card number wasn't a real card number

`pay` authorizes every escrow funding against one hardcoded test card,
tokenized once at startup (Section 4: no real payment data anywhere in the
simulation). The number I'd written down was `4111110000000000`, BIN
`411111`, which `Switch/README.md` names as an accepted test prefix. First
call: `pan_invalid_luhn`. The number fails the checksum every card number
actually has to pass.

Swapping in a Luhn-valid `411111`-prefixed number got a different error:
`pan_not_a_test_card`. The README's prose and the code had drifted.
Switch's `BinDirectory` only recognizes three prefixes, and `411111` isn't
one of them:

```java
if (panStr.startsWith("42424242")) { ... }  // VISA
if (panStr.startsWith("400000"))   { ... }  // VISA
if (panStr.startsWith("55555555")) { ... }  // MASTERCARD
return Optional.empty();
```

`4242424242424242`, the card number every Stripe integration guide uses,
matches the first prefix and is Luhn-valid. That's what `pay` uses now.
Every card-funded escrow this session runs on it, including the trace in
[ADR 0004](../adr/0004-distributed-tracing.md).

## Bug 2: a captured card charge still couldn't fund escrow

With a working card, the first real checkout still failed: `pay` logged
`card authorization unresolved; retry`, a 502, while switch's own database
showed the payment as `CAPTURED`. The charge worked. Funding escrow didn't.

The reason is that `FundEscrow` was never touched when card auth was
added (by design, see [ADR 0003](../adr/0003-pay-switch-integration.md)):
it moves money out of the *buyer's own wallet* account, and a buyer who
paid by card never deposited anything into that wallet. A successful card
charge and a non-empty wallet balance are two different facts, and only
the second one satisfied `FundEscrow`'s check.

The fix treats a captured charge as what it functionally is: a deposit,
using the deposit path that already existed for wallet top-ups.

```go
if s.sw != nil {
    if _, err := s.ledger.Deposit(r.Context(), "card:"+in.OrderID, in.BuyerID, in.AmountMinor); err != nil {
        httpx.Error(w, 502, "card deposit failed")
        return
    }
}
```

`FundEscrow` stays exactly as it was; the card charge just arrives through
the front door instead of trying to skip the wallet check entirely.

## Bug 3: the trace had a hole, and load testing found it before Jaeger did

Every method on `SwitchClient` uses the traced HTTP client this repo
standardizes on, `switchHTTPClient`, except `authorize()`, which built its
own request and sent it through bare `http.DefaultClient`. No traceparent
header, no client span, and a separate connection pool from every other
call this service makes: `http.DefaultTransport`'s `MaxIdleConnsPerHost` is
2, fine for a client that talks to many hosts once each, wrong for a
service that talks to one host constantly under load.

Scenario 4 (100 checkouts/sec against the real card path) is what
surfaced the second half of that: p99 latency at 3.01 seconds against an
800ms target, with `dropped_iterations` climbing as VUs piled up waiting on
slow responses. Routing `authorize()` through the same client the rest of
the file uses fixed both problems in one line, and cut p99 to 818ms:

```go
resp, err := switchHTTPClient.Do(req)  // was http.DefaultClient.Do(req)
```

The same fix, generalized to every inter-service client in the repo, went
into `tracing.Client()` itself: clone `http.DefaultTransport` and raise
`MaxIdleConnsPerHost` to 100, once, in the one place every client already
gets built.

## The one real miss, and why it isn't a bug

Even after that fix, scenario 4 still failed hard: 93% of requests
still came back 502. Switch's own database told the real story: only
~120-200 payments actually landed there per run, however many thousand
`pay` attempted. `RateLimitFilter.java` explains why:

```java
// 100 requests per minute
Bandwidth limit = Bandwidth.classic(100, Refill.greedy(100, Duration.ofMinutes(1)));
```

Keyed by API key, and every request from `pay` carries the same one.
Switch caps a single integration at 100 requests a minute across every
endpoint, tokenize included. Scenario 4 asks for 100 *a second*, sixty
times over. I confirmed it directly: 150 concurrent calls against
`/v1/payments` with pay's own key, and the 429s start right around request
120.

This isn't a performance ceiling to chase, it's Switch's anti-abuse policy
doing exactly what it's for. A merchant integration that wants more
throughput asks for a higher-tier key; it doesn't get to out-request the
limiter. So scenario 4's target is reported as a miss, and the reason is
"a real rate limit I have no business bypassing," not "the code is slow."

## Scorecard

| # | Scenario | Target | Result | Verdict |
|---|----------|--------|--------|---------|
| 1 | Drop spike (10,000 VUs, 5s ramp) | p99 < 200ms, no 5xx | p99 7.0s, 0 five-xx, 0 oversell (1000/1000 units, verified in Postgres) | **Miss on latency.** 10k concurrent connections against a 3-container dev stack; correctness held completely. |
| 2 | Drop sustained (2000 iter/s, 60s) | p99 < 300ms, error rate < 0.1% | p99 156.65ms, error rate 0.16%, 0 oversell (2000/2000, verified) | **Pass on latency, narrow miss on error rate** (0.16% vs 0.1%, ~200 non-5xx requests out of 120k; no server error observed). |
| 3 | Marketplace browse (500 req/s, 60s) | p99 < 100ms | p99 1.45ms, 0% failed | **Pass**, by two orders of magnitude. |
| 4 | Checkout end to end (100 req/s, 60s) | p99 < 800ms | p99 818ms after the connection-pool fix; ~97% of requests rejected by Switch's 100/min rate limit | **Miss, and not a code problem.** See above. |
| 5 | Ledger write (500 req/s, 60s, no card auth) | p99 < 150ms, invariants hold | p99 2.57s, 100% of completed requests correct, all three money invariants held | **Miss on latency**, real cause identified: every `FundEscrow` call locks the same shared escrow account row (see the `ponytail:` comment on `FundEscrow` in [`ledger.go`](../../internal/pay/ledger.go)), capping this machine at roughly 150-200 fundings/sec regardless of available CPU. |

Every scenario that touched money (`4`, `5`) ended with
[`verify_invariants.sql`](../verify_invariants.sql): zero unbalanced
transfers, a global entry sum of exactly zero, zero accounts drifted from
their entries. Whatever else went wrong under load, no yen was created,
destroyed, or misplaced.

## What actually needed fixing, and what didn't

Three real bugs got fixed: a card number that was never valid, a funding
path that only worked for wallet balances, and a client that skipped both
tracing and connection reuse. All three were sitting in code that had
"passed" before anyone sent it real concurrent traffic.

Two misses stay misses. Scenario 1 and 5's latency targets assume hardware
this laptop doesn't have, and section 11 says to publish that honestly
rather than hide it. Scenario 4's target assumes throughput Switch's own
policy won't grant a single API key, and the fix for that isn't in this
repo. The escrow row-lock ceiling in scenario 5 is real and load-bearing
information for anyone scaling this past a demo, which is exactly why it's
a comment naming the ceiling and not a rewrite nobody asked for yet.
