package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
)

func leaseSeconds(duration time.Duration) (int, error) {
	if duration < time.Second || duration > 24*time.Hour || duration%time.Second != 0 {
		return 0, fmt.Errorf("%w: lease must be a whole number of seconds in [1, 86400]", ErrInvalidRepositoryArgument)
	}

	return int(duration / time.Second), nil
}

// ClaimNext atomically reserves one due pending or expired processing job.
// SKIP LOCKED lets concurrent workers claim different rows without waiting.
func (r *Repository) ClaimNext(ctx context.Context, duration time.Duration, maxAttempts int) (quote.UpdateJob, error) {
	seconds, err := leaseSeconds(duration)
	if err != nil {
		return quote.UpdateJob{}, err
	}

	if maxAttempts <= 0 {
		return quote.UpdateJob{}, fmt.Errorf("%w: maxAttempts must be positive", ErrInvalidRepositoryArgument)
	}

	job, err := scanJob(r.db.QueryRowContext(ctx, `
		WITH picked AS (
		    SELECT id FROM quote_updates
		    WHERE attempts < $1 AND (
		        (status = 'pending' AND next_attempt_at <= statement_timestamp()) OR
		        (status = 'processing' AND locked_until <= statement_timestamp())
		    )
		    ORDER BY CASE WHEN status = 'pending' THEN next_attempt_at ELSE locked_until END, id
		    LIMIT 1 FOR UPDATE SKIP LOCKED
		)
		UPDATE quote_updates AS u
		SET status = 'processing', attempts = u.attempts + 1,
		    lock_token = gen_random_uuid(),
		    locked_until = statement_timestamp() + make_interval(secs => $2::int)
		FROM picked 
		WHERE u.id = picked.id
		RETURNING u.id, u.pair, u.status, u.idempotency_key, u.attempts,
		          u.next_attempt_at, u.created_at, u.completed_at,
		          u.locked_until, u.lock_token::text, u.error_code, u.error_message`,
		maxAttempts, seconds))
	if errors.Is(err, sql.ErrNoRows) {
		return quote.UpdateJob{}, ErrNoClaimableJob
	}
	
	if err != nil {
		return quote.UpdateJob{}, fmt.Errorf("claim next job: %w", err)
	}

	return job, nil
}

// RenewLease is optional for a short provider request, but supports heartbeats.
func (r *Repository) RenewLease(ctx context.Context, id int64, token string, duration time.Duration) (time.Time, error) {
	seconds, err := leaseSeconds(duration)
	if err != nil {
		return time.Time{}, err
	}

	if id <= 0 || token == "" {
		return time.Time{}, fmt.Errorf("%w: ID and token are required", ErrInvalidRepositoryArgument)
	}

	var until time.Time
	
	err = r.db.QueryRowContext(ctx, `
		UPDATE quote_updates
		SET locked_until = statement_timestamp() + make_interval(secs => $3::int)
		WHERE id = $1 AND status = 'processing'
		  AND lock_token::text = $2 AND locked_until > statement_timestamp()
		RETURNING locked_until`, id, token, seconds).Scan(&until)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrLeaseLost
	}

	if err != nil {
		return time.Time{}, fmt.Errorf("renew lease: %w", err)
	}

	return until.UTC(), nil
}

// ScheduleRetry releases the lease and makes the job eligible no earlier than nextAttemptAt.
func (r *Repository) ScheduleRetry(ctx context.Context, id int64, token string, nextAttemptAt time.Time) error {
	if id <= 0 || token == "" || nextAttemptAt.IsZero() {
		return fmt.Errorf("%w: ID, token and retry time are required", ErrInvalidRepositoryArgument)
	}

	res, err := r.db.ExecContext(ctx, `
		UPDATE quote_updates
		SET status = 'pending', next_attempt_at = $3,
		    lock_token = NULL, locked_until = NULL,
		    error_code = NULL, error_message = NULL
		WHERE id = $1 AND status = 'processing'
		  AND lock_token::text = $2 AND locked_until > statement_timestamp()`,
		id, token, nextAttemptAt.UTC())

	return checkLeaseUpdate(res, err, "schedule retry")
}

func (r *Repository) SaveFailure(ctx context.Context, id int64, token string, failure quote.UpdateFailure) error {
	if id <= 0 || token == "" {
		return fmt.Errorf("%w: ID and token are required", ErrInvalidRepositoryArgument)
	}

	if err := failure.Validate(); err != nil {
		return fmt.Errorf("save job failure: %w", err)
	}

	res, err := r.db.ExecContext(ctx, `
		UPDATE quote_updates
		SET status = 'failed', completed_at = statement_timestamp(),
		    lock_token = NULL, locked_until = NULL,
		    error_code = $3, error_message = $4
		WHERE id = $1 AND status = 'processing'
		  AND lock_token::text = $2 AND locked_until > statement_timestamp()`,
		id, token, string(failure.Code), failure.Message)

	return checkLeaseUpdate(res, err, "save job failure")
}

// FailExhausted finalizes due jobs that have no attempts left.
// It also resolves processing jobs whose owners crashed on the last attempt.
func (r *Repository) FailExhausted(ctx context.Context, maxAttempts, batchSize int) (int64, error) {
	if maxAttempts <= 0 || batchSize <= 0 || batchSize > 1000 {
		return 0, fmt.Errorf("%w: maxAttempts > 0 and batchSize in [1,1000] required", ErrInvalidRepositoryArgument)
	}

	res, err := r.db.ExecContext(ctx, `
		WITH picked AS (
		    SELECT id FROM quote_updates
		    WHERE attempts >= $1 AND (
		        (status = 'pending' AND next_attempt_at <= statement_timestamp()) OR
		        (status = 'processing' AND locked_until <= statement_timestamp())
		    )
		    ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		UPDATE quote_updates AS u
		SET status = 'failed', completed_at = statement_timestamp(),
		    lock_token = NULL, locked_until = NULL,
		    error_code = 'processing_timeout',
		    error_message = 'Maximum attempts exceeded or processing lease expired'
		FROM picked WHERE u.id = picked.id`, maxAttempts, batchSize)
	if err != nil {
		return 0, fmt.Errorf("fail exhausted jobs: %w", err)
	}

	return res.RowsAffected()
}

func checkLeaseUpdate(res sql.Result, err error, operation string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}

	count, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s row count: %w", operation, err)
	}

	if count != 1 {
		return ErrLeaseLost
	}
	
	return nil
}
