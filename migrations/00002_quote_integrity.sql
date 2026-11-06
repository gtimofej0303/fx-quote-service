-- +goose Up

-- Existing data must satisfy these constraints.
ALTER TABLE quote_updates
    ADD CONSTRAINT quote_updates_pair_check
        CHECK (pair IN ('USD/EUR', 'USD/EGP', 'EUR/USD', 'EUR/EGP', 'EGP/USD', 'EGP/EUR')),

    ADD CONSTRAINT quote_updates_status_check
        CHECK (status IN ('pending', 'processing', 'succeeded', 'failed')),
        
    ADD CONSTRAINT quote_updates_attempts_check
        CHECK (attempts >= 0),

    ADD CONSTRAINT quote_updates_key_check
        CHECK (idempotency_key IS NULL OR
               idempotency_key COLLATE "C" ~ '^[A-Za-z0-9._:-]{1,128}$'),

    ADD CONSTRAINT quote_updates_completion_time_check
        CHECK (completed_at IS NULL OR completed_at >= created_at),

    ADD CONSTRAINT quote_updates_state_check
        CHECK (
            (status = 'pending' AND completed_at IS NULL
                AND lock_token IS NULL AND locked_until IS NULL
                AND error_code IS NULL AND error_message IS NULL)
            OR
            (status = 'processing' AND attempts >= 1 AND completed_at IS NULL
                AND lock_token IS NOT NULL AND locked_until IS NOT NULL
                AND error_code IS NULL AND error_message IS NULL)
            OR
            (status = 'succeeded' AND attempts >= 1 AND completed_at IS NOT NULL
                AND lock_token IS NULL AND locked_until IS NULL
                AND error_code IS NULL AND error_message IS NULL)
            OR
            (status = 'failed' AND attempts >= 1 AND completed_at IS NOT NULL
                AND lock_token IS NULL AND locked_until IS NULL
                AND error_code IN (
                    'provider_timeout', 'provider_unavailable', 'provider_rate_limited',
                    'provider_rate_not_found', 'provider_rejected_request',
                    'provider_invalid_response', 'processing_timeout'
                )
                AND error_message IS NOT NULL AND btrim(error_message) <> '')
        ),

    ADD CONSTRAINT quote_updates_id_pair_unique UNIQUE (id, pair);

ALTER TABLE quotes
    ADD CONSTRAINT quotes_pair_check
        CHECK (pair IN ('USD/EUR', 'USD/EGP', 'EUR/USD', 'EUR/EGP', 'EGP/USD', 'EGP/EUR')),
    
    ADD CONSTRAINT quotes_price_positive_check CHECK (price > 0),
    
    ADD CONSTRAINT quotes_source_date_check
        CHECK (source_date BETWEEN DATE '0001-01-01' AND DATE '9999-12-31'),
    
    ADD CONSTRAINT quotes_job_pair_fk
        FOREIGN KEY (update_id, pair) REFERENCES quote_updates (id, pair);

CREATE INDEX quote_updates_pending_idx
    ON quote_updates (next_attempt_at, id) WHERE status = 'pending';

CREATE INDEX quote_updates_expired_idx
    ON quote_updates (locked_until, id) WHERE status = 'processing';

-- A deferred trigger checks the cross-table invariant at COMMIT.
-- The repository can insert the quote and mark the job succeeded in one transaction.
-- +goose StatementBegin
CREATE FUNCTION enforce_succeeded_job_has_quote() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM quotes
        WHERE update_id = NEW.id AND pair = NEW.pair
    ) THEN
        RAISE EXCEPTION 'succeeded update % has no quote', NEW.id
            USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER succeeded_job_requires_quote
    AFTER INSERT OR UPDATE ON quote_updates
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (NEW.status = 'succeeded')
    EXECUTE FUNCTION enforce_succeeded_job_has_quote();

-- Also protect a succeeded job if someone deletes or moves its quote manually.
-- +goose StatementBegin
CREATE FUNCTION enforce_quote_removal_preserves_success() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM quote_updates
        WHERE id = OLD.update_id AND status = 'succeeded'
    ) AND NOT EXISTS (
        SELECT 1 FROM quotes WHERE update_id = OLD.update_id
    ) THEN
        RAISE EXCEPTION 'cannot remove quote of succeeded update %', OLD.update_id
            USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER succeeded_job_preserves_quote
    AFTER DELETE OR UPDATE ON quotes
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW
    EXECUTE FUNCTION enforce_quote_removal_preserves_success();

-- +goose Down

DROP TRIGGER succeeded_job_preserves_quote ON quotes;
DROP FUNCTION enforce_quote_removal_preserves_success();
DROP TRIGGER succeeded_job_requires_quote ON quote_updates;
DROP FUNCTION enforce_succeeded_job_has_quote();
DROP INDEX quote_updates_expired_idx;
DROP INDEX quote_updates_pending_idx;

ALTER TABLE quotes
    DROP CONSTRAINT quotes_job_pair_fk,
    DROP CONSTRAINT quotes_source_date_check,
    DROP CONSTRAINT quotes_price_positive_check,
    DROP CONSTRAINT quotes_pair_check;

ALTER TABLE quote_updates
    DROP CONSTRAINT quote_updates_id_pair_unique,
    DROP CONSTRAINT quote_updates_state_check,
    DROP CONSTRAINT quote_updates_completion_time_check,
    DROP CONSTRAINT quote_updates_key_check,
    DROP CONSTRAINT quote_updates_attempts_check,
    DROP CONSTRAINT quote_updates_status_check,
    DROP CONSTRAINT quote_updates_pair_check;
