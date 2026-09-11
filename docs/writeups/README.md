# Engineering write-ups

Six posts on the parts of [Vault](../../README.md) that were worth building
from scratch. Each is concrete, with code pulled from the repo.

1. **[Building an OIDC provider from the RFCs (and what I got wrong first)](01-oidc-from-the-rfcs.md)**
   PKCE, single-use auth codes, RS256/JWKS, refresh rotation with reuse
   detection, TOTP, and the two things I got wrong on the first pass.

2. **[The transactional outbox pattern in practice](02-transactional-outbox-in-practice.md)**
   The dual-write problem, the relay, at-least-once + idempotent =
   exactly-once effects, and the chaos test. Doubles as documentation for
   [outboxkit](https://github.com/NichoHo/outboxkit).

3. **[What my tests prove: invariant testing for money code](03-what-my-tests-prove.md)**
   Double-entry conservation, concurrent double-spends, escrow zeroing out,
   and timer-vs-manual exactly-once release.

4. **[The answer that never comes: resolving a payment neither approved nor declined](04-the-answer-that-never-comes.md)**
   Switch's `AUTH_UNKNOWN` state, why a bounded synchronous wait beat a
   webhook here, and what changes if that stops being true.

5. **[Load testing Agora: three real bugs and one honest miss](05-load-test-results.md)**
   Five k6 scenarios against the real stack: an invalid test card, a
   funding path that only worked for wallets, a client that skipped both
   tracing and connection reuse, and a rate limit that turned out to be a
   feature, not a bug.

6. **[A rate limiter that would have locked out real users](06-a-rate-limiter-that-would-have-locked-out-real-users.md)**
   Hardening `id`'s login and MFA routes, a missing `go.mod` dependency the
   first build caught, and a shared rate-limit bucket across seven routes
   that only the full test suite caught.
