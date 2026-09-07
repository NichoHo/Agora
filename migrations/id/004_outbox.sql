-- id publishes no events today; audit_events (003) is Postgres-only. This is
-- the minimum needed for AGORA_SPEC.md section 8.3's two id-sourced risk
-- signals (auth.login, auth.refresh_reuse_detected), added the same way
-- Phase 3 extended pay: a new table alongside the existing tested behavior,
-- not a change to it. See internal/id/server.go's emit() and its two call
-- sites in oauth.go.
CREATE TABLE outbox (
    id           bigserial PRIMARY KEY,
    at           timestamptz NOT NULL DEFAULT now(),
    topic        text NOT NULL,
    payload      jsonb NOT NULL,
    published_at timestamptz
);
