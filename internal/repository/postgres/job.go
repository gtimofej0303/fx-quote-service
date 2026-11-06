package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
)

// Repository uses the existing database/sql pool.
type Repository struct {
	db *sql.DB
}

func New(db *sql.DB) *Repository { return &Repository{db: db} }

// CreateJob is concurrency-safe for both keyed and unkeyed requests.
// ON CONFLICT DO NOTHING avoids mutating an existing job on a repeated POST.
func (r *Repository) CreateJob(ctx context.Context, pair quote.CurrencyPair, key *string) (quote.UpdateJob, error) {
	if err := pair.Validate(); err != nil {
		return quote.UpdateJob{}, fmt.Errorf("create job: %w", err)
	}

	if key != nil {
		if err := quote.ValidateIdempotencyKey(*key); err != nil {
			return quote.UpdateJob{}, fmt.Errorf("create job: %w", err)
		}
	}

	// database/sql sees either a concrete string or nil (SQL NULL).
	var sqlKey any

	if key != nil {
		sqlKey = *key
	}

	var id int64

	err := r.db.QueryRowContext(ctx, `
		INSERT INTO quote_updates (pair, idempotency_key)
		VALUES ($1, $2)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id`, pair.String(), sqlKey).Scan(&id)
	if err == nil {
		return r.GetJob(ctx, id)
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return quote.UpdateJob{}, fmt.Errorf("insert update job: %w", err)
	}

	if key == nil {
		return quote.UpdateJob{}, fmt.Errorf("insert update job: unexpected conflict with absent key")
	}

	// After the conflicting transaction commits, a new READ COMMITTED statement
	// sees its row. This also works for simultaneous requests with the same key.
	existing, err := scanJob(r.db.QueryRowContext(ctx, `
		SELECT id, pair, status, idempotency_key, attempts, next_attempt_at,
		       created_at, completed_at, locked_until, lock_token::text,
		       error_code, error_message
		FROM quote_updates WHERE idempotency_key = $1`, *key))

	if err != nil {
		return quote.UpdateJob{}, fmt.Errorf("read idempotent job: %w", err)
	}

	if existing.Pair != pair {
		return quote.UpdateJob{}, ErrIdempotencyConflict
	}

	return existing, nil
}

func (r *Repository) GetJob(ctx context.Context, id int64) (quote.UpdateJob, error) {
	if id <= 0 {
		return quote.UpdateJob{}, fmt.Errorf("%w: job ID must be positive", ErrInvalidRepositoryArgument)
	}

	job, err := scanJob(r.db.QueryRowContext(ctx, `
		SELECT id, pair, status, idempotency_key, attempts, next_attempt_at,
		       created_at, completed_at, locked_until, lock_token::text,
		       error_code, error_message
		FROM quote_updates WHERE id = $1`, id))

	if errors.Is(err, sql.ErrNoRows) {
		return quote.UpdateJob{}, ErrJobNotFound
	}

	if err != nil {
		return quote.UpdateJob{}, fmt.Errorf("get job %d: %w", id, err)
	}

	return job, nil
}
