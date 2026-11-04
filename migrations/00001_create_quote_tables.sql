
-- +goose Up

CREATE TABLE quote_updates (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    pair TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    idempotency_key VARCHAR(128) UNIQUE,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_until TIMESTAMPTZ,
    lock_token UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    error_code TEXT,
    error_message TEXT
);

CREATE TABLE quotes (
    update_id BIGINT PRIMARY KEY REFERENCES quote_updates(id),
    pair TEXT NOT NULL,
    price NUMERIC(38, 18) NOT NULL,
    source_date DATE NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX quotes_latest_idx
    ON quotes (pair, source_date DESC, updated_at DESC, update_id DESC);

-- +goose Down

DROP TABLE quotes;
DROP TABLE quote_updates;
