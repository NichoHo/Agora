# End-to-end tests

Playwright happy-path e2e for the whole stack: **register → MFA enroll + step-up
→ AI-assisted listing → escrow buy → ship → confirm → wallet reconciles**.

Unlike unit tests, this drives all six services, so it runs against a **live
compose stack** rather than a dev server:

```sh
make up                        # from the repo root
docker compose run --rm seed   # demo users + listings (or: make seed)
cd web
npx playwright install chromium   # first time only
npm run e2e
```

The seller is a fresh random account each run (`seller-<ts>@vault.test`); the
buyer is seeded `bob@vault.test` ($1,000, MFA off). `e2e/totp.ts` mirrors
`internal/id/totp.go` so the test computes the same TOTP code the IdP expects
for the MFA step.

> This is intentionally **not** part of the unit CI job, because it needs the full
> stack (Postgres + Redpanda + four services + web). Run it against a deployed
> or local compose environment. On failure, Playwright writes a trace to
> `test-results/`; open it with `npx playwright show-trace <trace.zip>`.
