# A rate limiter that would have locked out real users

This pass hardened the parts of [Vault](../../README.md) that face an
anonymous attacker directly: per-IP rate limits on `id`'s login and MFA
routes, self-service account deactivation, TOTP secrets encrypted at rest
instead of stored in plaintext, a CSP on the Next.js app, closed ports on
`db`/`redis`/`redpanda` in `docker-compose.yml`, and a prompt-injection
defense plus daily spend caps on `assist`'s vision-model listing suggestions.
None of it is exotic. All of it is the kind of gap that looks fine in a demo
and stops looking fine the day the app has a real user base.

Running the full test suite after wiring it up is what caught the two bugs
worth writing about. Neither would have shown up in a read-through of the
diff.

## Bug 1: a dependency that was never added

`internal/ratelimit/ratelimit.go` wraps `golang.org/x/time/rate` in a small
per-IP token bucket. It's twelve lines of real logic:

```go
func (l *Limiter) allow(key string) bool {
	l.mu.Lock()
	v, ok := l.visitors[key]
	if !ok {
		v = rate.NewLimiter(l.rps, l.burst)
		l.visitors[key] = v
	}
	l.mu.Unlock()
	return v.Allow()
}
```

`go.mod` never got the matching `require` line. `go build ./...` failed on
the first attempt with `no required module provides package
golang.org/x/time/rate`. One `go get golang.org/x/time/rate` later, the build
was green. Not interesting on its own, but it's the reason to always run a
clean build before calling anything done: an editor with the module already
resolved in its cache will happily let this kind of gap sit invisible for a
long time.

## Bug 2: one bucket, seven routes

The rate limiter's own doc comment explains the intended budget:

> 2 req/s per IP with a burst of 10 covers a real user mistyping a code a
> few times in a row.

That budget makes sense for one endpoint. The first version of the wiring
gave it to seven:

```go
limiter := ratelimit.New(2, 10)
mux.HandleFunc("POST /register", limiter.Wrap(s.handleRegister))
mux.HandleFunc("POST /login", limiter.Wrap(s.handleLogin))
// ...
mux.HandleFunc("POST /password", limiter.Wrap(s.handleChangePassword))
s.mfaRoutes(mux, limiter)
```

`mfaRoutes` wrapped four more with that same `limiter` value: `/mfa/activate`,
`/mfa/disable`, `/login/totp`, `/login/recovery`. One token bucket, one
shared budget of 10 requests per IP, backing register, login, password
change, and the entire MFA flow at once.

Walk through what a single legitimate session costs against that budget.
Register (1), enroll TOTP, mistype the activation code once (2), activate
for real (3), log out, log back in (4), mistype the TOTP step once (5),
get it right (6). That's six requests before the user has done anything
wrong, on a flow explicitly designed to expect a mistyped code along the
way. Log out and log back in twice more to check a recovery code and then
confirm it's single-use, the exact scenario the security work was
supposed to prove, and the bucket is empty. The eleventh request gets a
`429`, not because anyone did anything suspicious, but because unrelated
routes had already spent a budget that was never really shared API
capacity. It was one IP's entire login allowance for the day.

The test suite caught this directly, because `TestMFAFlow` walks through
close to that exact sequence:

```
--- FAIL: TestMFAFlow (2.50s)
    mfa_test.go:90: reused recovery code: want 401, got 429
FAIL
```

The assertion at line 90 checks that a spent recovery code is rejected with
`401 invalid recovery code`. It got `429 too many requests` instead, because
the login call two lines earlier had already spent the last token in a
bucket that register, two logins, two TOTP submissions, and an MFA
activation had been drawing from the whole time.

The fix is smaller than the bug: give each route its own limiter instance
instead of sharing one.

```go
mux.HandleFunc("POST /register", ratelimit.New(2, 10).Wrap(s.handleRegister))
mux.HandleFunc("POST /login", ratelimit.New(2, 10).Wrap(s.handleLogin))
// ...
mux.HandleFunc("POST /password", ratelimit.New(2, 10).Wrap(s.handleChangePassword))
s.mfaRoutes(mux)
```

and inside `mfaRoutes`, the same change: `ratelimit.New(2, 10)` called once
per route instead of one shared value passed in. Each endpoint now defends
itself against a brute-force attempt on *that* endpoint, which is what
credential stuffing and TOTP guessing actually look like, an attacker
hammering one route, not spreading requests evenly across seven. A real
user mistyping a code twice on `/login/totp` no longer spends down the
budget they'll need for `/login/recovery` a minute later. `TestMFAFlow`
passes, and so does everything else:

```
ok  	agora/internal/id	1.699s
ok  	agora/internal/market	2.317s
ok  	agora/internal/pay	1.971s
ok  	agora/internal/sale	0.010s
```

## Why this is worth a write-up

A per-route rate limit is a one-line change per route once you've decided
to add it. The part that's easy to get wrong is the part with no compiler
warning and no type error: whether two `Wrap()` calls that look identical
in a diff are secretly sharing state. `limiter.Wrap(a)` next to
`limiter.Wrap(b)` reads exactly like `ratelimit.New(2, 10).Wrap(a)` next to
`ratelimit.New(2, 10).Wrap(b)` unless you already know to check whether
`limiter` is the same variable both times. Nothing about the code review
surface makes that obvious. The doc comment on `ratelimit.New` even states
the intended per-route budget correctly. It's the wiring, not the design,
that broke the promise.

`TestMFAFlow` is more end-to-end than each individual `id` handler test,
which is exactly why it's the one that noticed. A rate limiter is a
cross-cutting concern by nature: it sits in front of otherwise-unrelated
handlers, and a bug in how it's shared won't show up in any single
handler's own unit test, only in a test that plays out a real multi-step
session against all of them in sequence. That's the same argument the load
testing write-up made about running real traffic instead of reading code:
some classes of bug only exist in the interaction between parts that each
look correct on their own.
