CREATE TABLE drops (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    seller_id      uuid NOT NULL,
    title          text NOT NULL,
    description    text NOT NULL DEFAULT '',
    image_url      text NOT NULL DEFAULT '',
    starts_at      timestamptz NOT NULL,
    ends_at        timestamptz,
    total_units    int NOT NULL CHECK (total_units > 0),
    price_minor    bigint NOT NULL CHECK (price_minor > 0),
    units_per_user int NOT NULL DEFAULT 1 CHECK (units_per_user > 0),
    shard_count    int NOT NULL DEFAULT 8 CHECK (shard_count > 0),
    status         text NOT NULL DEFAULT 'scheduled' CHECK (status IN ('scheduled','open','closed')),
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- One row per sellable unit of a drop, minted as real market.listings at drop-open
-- time (see ADR 0001). listing_id is what sale hands to market's own POST /orders.
CREATE TABLE drop_units (
    drop_id    uuid NOT NULL REFERENCES drops(id),
    shard      int NOT NULL,
    listing_id uuid NOT NULL,
    claimed_by uuid,
    PRIMARY KEY (drop_id, listing_id)
);
CREATE INDEX drop_units_shard_idx ON drop_units (drop_id, shard);

CREATE TABLE reservations (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    drop_id         uuid NOT NULL REFERENCES drops(id),
    user_id         uuid NOT NULL,
    listing_id      uuid,
    shard           int,
    units           int NOT NULL DEFAULT 1 CHECK (units > 0),
    state           text NOT NULL DEFAULT 'reserved' CHECK (state IN
                    ('reserved','payment_pending','payment_indeterminate','confirmed','expired','released')),
    idempotency_key text NOT NULL,
    reserved_at     timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    order_id        uuid,
    UNIQUE (drop_id, idempotency_key)
);
CREATE INDEX reservations_drop_state_idx ON reservations (drop_id, state);
CREATE INDEX reservations_user_idx ON reservations (drop_id, user_id);
CREATE INDEX reservations_sweep_idx ON reservations (state, expires_at) WHERE state = 'reserved';

CREATE TABLE stock_reconciliation (
    id                bigserial PRIMARY KEY,
    drop_id           uuid NOT NULL REFERENCES drops(id),
    checked_at        timestamptz NOT NULL DEFAULT now(),
    redis_remaining   int NOT NULL,
    postgres_reserved int NOT NULL,
    drift             int NOT NULL
);

-- ponytail: outbox written transactionally now; a relay consumer for these
-- topics (risk scoring) arrives in Phase 4.
CREATE TABLE outbox (
    id           bigserial PRIMARY KEY,
    at           timestamptz NOT NULL DEFAULT now(),
    topic        text NOT NULL,
    payload      jsonb NOT NULL,
    published_at timestamptz
);
