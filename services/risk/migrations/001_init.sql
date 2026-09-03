CREATE TABLE scores (
    id           bigserial PRIMARY KEY,
    subject_type text NOT NULL,   -- 'reservation' | 'order' | 'transfer'
    subject_id   text NOT NULL,
    score        double precision NOT NULL,
    decision     text NOT NULL CHECK (decision IN ('allow','review','block')),
    reasons      jsonb NOT NULL DEFAULT '[]',
    model_version text NOT NULL,
    status       text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','approved','rejected')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    resolved_by  text,
    resolved_at  timestamptz,
    UNIQUE (subject_type, subject_id)
);
CREATE INDEX scores_queue_idx ON scores (status, created_at DESC);

CREATE TABLE consumed_events (
    source   text NOT NULL,
    event_id bigint NOT NULL,
    PRIMARY KEY (source, event_id)
);
