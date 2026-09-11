# What my tests prove: invariant testing for money code

Most tests check examples: *given this input, expect that output.* That's fine
for a formatting function. It's not enough for money. With money, the bugs that
matter aren't "wrong output for input X." They're "under concurrency, value was
created or destroyed," and you will not find those by asserting on examples,
because the example that breaks is the interleaving you didn't think to write.

So [Vault](https://github.com/NichoHo/Agora)'s payment service is tested by
**invariants**: properties that must hold across *every* state, which the tests
then try to violate. This is the testing style I care most about, and money is
where it earns its keep.

## The ledger, in one sentence

Vault's `pay` service is a double-entry ledger. Every movement of money is a
`transfer` that writes two signed `entries`, one debit and one credit, that sum
to zero. Accounts are users, an escrow pool, a platform-revenue account, and an
`external` source for demo deposits. Balances are `int64` minor units (US cents); there
is no floating point anywhere near money.

That design exists to *make invariants checkable*. If every transfer's entries
sum to zero, then value is conserved by construction, and a test can assert it.

## Invariant 1: the books always balance

Three properties, checked after any sequence of operations:

```go
func checkBooks(t *testing.T, l *Ledger) {
    // (a) every transfer's entries sum to zero
    //     SELECT ... FROM entries GROUP BY transfer_id HAVING sum(amount_minor) <> 0
    // (b) global sum of all entries is zero, so value is conserved
    //     SELECT sum(amount_minor) FROM entries    → must be 0
    // (c) every account.balance equals the sum of its own entries, with no drift
    //     count accounts WHERE balance <> (SELECT sum(...) FROM entries ...) → must be 0
}
```

`(a)` is per-transfer integrity. `(b)` is the big one: money is neither created
nor destroyed anywhere in the system. `(c)` catches the cached `balance`
column drifting from the source-of-truth entries. Every money test ends with
`checkBooks`. A bug that leaks a cent fails `(b)`; a bug that updates a balance
without a matching entry fails `(c)`.

The database backs the invariant with a hard constraint, so it can't be violated
even by a bug in my Go:

```sql
CONSTRAINT balance_nonneg CHECK (owner_type = 'external' OR balance >= 0)
```

A user balance can never go negative. That single line is the last line of
defense for the next invariant.

## Invariant 2: concurrent double-spends fail

This is the one examples can't catch. Give a buyer $100 and fire twenty
goroutines each trying to spend $10 into escrow on *different* orders. Exactly
ten must succeed; the balance must land at exactly zero and never go negative:

```go
const n = 20
var wg sync.WaitGroup
results := make([]error, n)
for i := 0; i < n; i++ {
    wg.Add(1)
    go func(i int) {
        defer wg.Done()
        _, results[i] = l.FundEscrow(ctx, fmt.Sprintf("order-%d", i), buyer, 1_000)
    }(i)
}
wg.Wait()

succeeded := countNil(results)
if succeeded != 10 { t.Fatalf("want exactly 10 successful funds, got %d", succeeded) }
if balance(t, l, "user", &buyer) != 0 { t.Fatal("buyer should end at exactly 0") }
checkBooks(t, l)
```

The correctness comes from how a transfer moves money: inside one transaction it
locks both account rows `FOR UPDATE` **in a fixed order** (by account id, so
concurrent transfers touching the same accounts can't deadlock),
re-reads the balance, and refuses if it's insufficient. Ten goroutines serialize
on the buyer's row lock; the eleventh through twentieth each re-read a balance too
low and get `ErrInsufficientFunds`. And if my application check ever had a hole,
the `balance >= 0` constraint would abort the transaction anyway. Two independent
mechanisms, one invariant. Runs under `-race`.

## Invariant 3: escrow zeroes out

Escrow is a holding account. Money flows buyer → escrow → (seller + platform), and
the invariant is that **escrow nets to zero**, so nothing is ever stranded there.
Release splits the payment 90/10 (seller/platform fee); refund returns it whole.
Either way, escrow ends empty:

```go
// fund 30,000 → release
if balance(t, l, "escrow", nil) != 0 { t.Fatal("escrow should zero out") }
if balance(t, l, "user", &seller) != 27_000 { t.Fatal("seller should get 90%") }
if balance(t, l, "platform", nil) != 3_000 { t.Fatal("platform should get 10%") }
```

There's a matching refund test (escrow → buyer, buyer made whole, escrow zero).
Both end in `checkBooks`. If a rounding bug left one cent in escrow, the balance
assertion catches it directly and `checkBooks (b)` catches it globally.

## Invariant 4: timer-vs-manual releases exactly once

This is the subtlest, and it spans two services. An order's escrow can be released
two ways: the buyer clicks "confirm receipt," or a 72-hour auto-release timer
fires. In production both can happen *at the same instant*, with the buyer
confirming just as the sweeper wakes. The invariant: escrow is released **exactly once**, no
matter how they race.

The test forces the race and asserts on the ledger, not the UI:

```go
// shipped order, backdated so the sweeper considers it due
var wg sync.WaitGroup
wg.Add(2)
go func() { defer wg.Done(); e.srv.SweepAutoRelease(ctx, 72*time.Hour) }()
go func() { defer wg.Done(); e.do(t, "POST", "/orders/"+o.ID+"/confirm", buyerTok, nil) }()
wg.Wait()

var releases int
e.ledger.Pool.QueryRow(ctx,
    `SELECT count(*) FROM pay.transfers WHERE kind = 'escrow_release' AND reference = $1`,
    "order:"+o.ID).Scan(&releases)
if releases != 1 { t.Fatalf("want exactly 1 release transfer, got %d", releases) }
```

Exactly-once holds because of two independent guards. At the ledger, release is
keyed by an idempotency key (`release:<order>`) with a unique constraint, so the
second attempt returns the *existing* transfer instead of making a new one. At the
order state machine, the status flip is a guarded `UPDATE ... WHERE status =
'shipped'`, so only one path transitions the order. Belt and suspenders, and the
test proves the seller is paid exactly once (`7_200`, not `14_400`).

## Why this is the loudest signal

Almost no student project has tests like these, and the teams I most want to
work with name testing explicitly. That's not a coincidence. Invariant tests are
harder to write than example tests, because you have to *state the property* first
("value is conserved," "escrow nets to zero," "released exactly once") and then
build the machinery to attack it. But once written, they're worth ten example
tests each: they don't check that one path works, they check that *no* path can
break the property.

The recurring shape across all four: **make the invariant checkable by design**
(double-entry so value-conservation is a SQL query), **back it with a database
constraint** (`balance >= 0`, unique idempotency keys) so a bug in the application
can't violate it, and then **write a test that actively tries to break it**, with
real concurrency, under `-race`. That's what it takes to trust code that moves
money.

*Vault's ledger is [`internal/pay`](../../internal/pay/). The invariant suite is
in [`ledger_test.go`](../../internal/pay/ledger_test.go) and the cross-service
race test in [`orders_test.go`](../../internal/market/orders_test.go).*
