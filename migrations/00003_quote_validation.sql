-- +goose Up

-- Existing invalid data must be corrected explicitly before this migration.
ALTER TABLE quote_updates
    ADD CONSTRAINT quote_updates_failed_code_required
        CHECK (status <> 'failed' OR error_code IS NOT NULL);

ALTER TABLE quotes
    ADD CONSTRAINT quotes_source_date_retrieval_check
        CHECK (source_date <= (updated_at AT TIME ZONE 'UTC')::date + 1);

-- PostgreSQL numeric NaN passes price > 0, so reject it explicitly.
ALTER TABLE quotes
    ADD CONSTRAINT quotes_price_not_nan CHECK (price <> 'NaN'::numeric);

-- +goose Down

ALTER TABLE quotes
    DROP CONSTRAINT quotes_price_not_nan;

ALTER TABLE quotes
    DROP CONSTRAINT quotes_source_date_retrieval_check;

ALTER TABLE quote_updates
    DROP CONSTRAINT quote_updates_failed_code_required;
