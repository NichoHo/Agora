# The answer that never comes: resolving a payment neither approved nor declined

Most payment failures are easy. The bank says no, you show the buyer a
decline message, the order stays unpaid. The interesting failure is the one
where the bank never says anything at all: the authorization request went
out, and then the connection died, or the acquirer's response got lost
somewhere on the way back. The card may have been charged. It may not have.
Nobody on your side of the wire knows, and guessing wrong in either
direction is a real mistake: confirm the order and you might ship an item
nobody paid for; cancel it and you might charge a buyer for a purchase you
told them failed.

[Switch](https://github.com/NichoHo/Switch), the payment gateway [Agora](https://github.com/NichoHo/Agora)
routes card authorization through, models this state explicitly as
`AUTH_UNKNOWN`. Building the integration between the two was the hardest
design problem in this migration, not because the code is long, but because
the obvious instinct (build a resolver) is wrong once you actually read what
switch already does.

## What switch already does about it

`AcquirerClient.authorize` classifies every failure by whether a retry is
safe:

```java
} else if (cause instanceof SocketTimeoutException ste) {
    if (ste.getMessage() != null && ste.getMessage().toLowerCase().contains("connect")) {
        return new AcquirerCallResult(RetrySafety.SAFE, null, "Connect timeout");
    }
    return new AcquirerCallResult(RetrySafety.UNSAFE, null, "Read timeout");
}
```

A connect timeout means the request never left; safe to retry, nothing
happened yet. A read timeout means the request *did* leave and the response
just never arrived, the genuinely ambiguous case, and `switch` marks the
payment `AUTH_UNKNOWN` rather than guessing. Then, separately, a scheduled
job resolves it without anyone asking:

```java
@Scheduled(fixedDelay = 30000)
public void probeAuthUnknownPayments() {
    List<PaymentEntity> unknownPayments = paymentRepository.findAuthUnknown(cutoff);
    for (PaymentEntity payment : unknownPayments) {
        if (payment.getCreatedAt().isAfter(tenSecondsAgo)) continue; // give the acquirer time to catch up
        AuthorizationStatusResponse response = acquirerClient.statusProbe(...);
        if (response != null && response.status() != null) {
            paymentService.resolveAuthUnknown(payment.getId(), "APPROVED".equals(response.status()));
        }
    }
}
```

Every 30 seconds, for every payment old enough to give the acquirer a
reasonable chance to catch up, `switch` asks the acquirer's own status-probe
endpoint what actually happened and settles the payment one way or the
other. This runs whether or not anything outside `switch` is watching.

The first design I sketched for `pay` had it polling `switch`'s payment
resource in a loop, mirroring `StatusProbeJob`, essentially rebuilding the
same resolver on the other side of the boundary. It would have worked. It
would also have been redundant: two processes independently deciding when
enough time has passed to check again, with no reason for the two intervals
to agree, polling the same underlying fact through two different paths.

## What pay actually does: wait, don't resolve

`pay` doesn't need its own resolver. It needs to read the answer once
`switch`'s resolver has written it:

```go
func (c *SwitchClient) AuthorizeAndResolve(ctx context.Context, orderID string,
    amountMinor int64, currency string, maxWait time.Duration) (state string, err error) {

    res, err := c.authorize(ctx, orderID, amountMinor, currency)
    if err != nil || res.State != "AUTH_UNKNOWN" {
        return res.State, err
    }
    deadline := time.Now().Add(maxWait)
    ticker := time.NewTicker(3 * time.Second)
    for time.Now().Before(deadline) {
        <-ticker.C
        if res, err = c.getPayment(ctx, res.ID); err == nil && res.State != "AUTH_UNKNOWN" {
            return res.State, nil
        }
    }
    return "AUTH_UNKNOWN", nil
}
```

`getPayment` is a plain `GET /v1/payments/{id}`, the same read anyone would
use to check on an order. It doesn't need to know a status-probe job exists.
It just needs to ask again in a few seconds, because `switch` said "we
don't know yet" is a *temporary* answer here, not a permanent one.

`maxWait` is set to 90 seconds against a job that runs every 30 and skips
anything under 10 seconds old, so worst case is roughly 40 seconds before
`switch` has an answer, and `pay` has more than double that margin. Not
tight by design; there was no load number to tune it against, so it's a
generous guess with a name, not a measured constant.

## The boundary this respects

`market`'s order flow was not allowed to change (`AGORA_SPEC.md` section
6). Its `PayClient.Fund` understands exactly three outcomes: 200 success,
402 insufficient funds, 409 already settled. Anything else falls into a
generic error branch that leaves the order in `pending_payment` and returns
502 to the buyer.

That generic branch turns out to be exactly the behavior an indeterminate
charge needs: the order sits unresolved, neither confirmed nor released,
until `pay`'s wait resolves one way or the other on a *later* attempt (the
buyer's retry, or the storefront's own retry logic). `market` never learns
that anything unusual happened. It just sees a slow, then eventually
successful (or cleanly declined) request, the same shape as any other
transient failure it already had to tolerate. Zero new states, zero new
code, on the one service in this migration most protected from getting
either.

## The gap this doesn't close

There's a second, quieter version of "no answer" in this migration, on
`sale`'s side rather than `pay`'s. A reservation's Redis hold expires on
`sale`'s own five-minute clock; the market listing behind it stays
`reserved` for up to fifteen more minutes, `market`'s own sweep interval,
untouched for the same reason as above. In that window a second buyer can
claim the same unit in Redis before `market` agrees it's free, and
`sale`'s checkout step has to treat `market`'s 409 as an expected retry
signal, not a failure:

```go
if errors.Is(err, ErrListingUnavailable) {
    fresh, cerr := s.rdb.Claim(ctx, res.DropID, res.ID+":retry", d.ShardCount, ...)
    if cerr == nil && fresh != "" {
        orderID, err = s.market.CreateOrder(ctx, bearer, fresh)
    }
}
```

Same shape as the `AUTH_UNKNOWN` wait: an answer that isn't ready yet
becomes a reason to try again, not a reason to fail. I wrote this one up as
a tradeoff in [`docs/adr/0002`](../adr/0002-drop-units-as-market-listings.md)
instead of fixing it, because fixing it for real needs `market` to accept a
caller acting on another user's behalf, exactly the kind of new surface
Section 6 rules out for a boundary this migration doesn't own.

## Why this is the strongest story here, not the flashiest one

The sharded Redis reservation script is more fun to read. The escrow ledger
has more invariant tests. But this is the one place in the migration where
the right answer was to build *less*, specifically, to notice that the
service on the other side of a boundary had already solved the hard part,
and that the only code `pay` needed was enough patience to ask it twice.
