package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
)

func (r *Repository) GetQuoteByUpdateID(ctx context.Context, updateID int64) (quote.Quote, error) {
	if updateID <= 0 {
		return quote.Quote{}, fmt.Errorf("%w: update ID must be positive", ErrInvalidRepositoryArgument)
	}

	result, err := scanQuote(r.db.QueryRowContext(ctx, `
		SELECT q.update_id, q.pair, q.price::text, to_char(q.source_date, 'YYYY-MM-DD'), q.updated_at
		FROM quotes AS q
		JOIN quote_updates AS u ON u.id = q.update_id
		WHERE q.update_id = $1 AND u.status = 'succeeded'`, updateID))
	if errors.Is(err, sql.ErrNoRows) {
		return quote.Quote{}, ErrQuoteNotFound
	}

	if err != nil {
		return quote.Quote{}, fmt.Errorf("get quote for job %d: %w", updateID, err)
	}

	return result, nil
}

func (r *Repository) GetLatestQuote(ctx context.Context, pair quote.CurrencyPair) (quote.Quote, error) {
	if err := pair.Validate(); err != nil {
		return quote.Quote{}, fmt.Errorf("get latest quote: %w", err)
	}

	result, err := scanQuote(r.db.QueryRowContext(ctx, `
		SELECT q.update_id, q.pair, q.price::text, to_char(q.source_date, 'YYYY-MM-DD'), q.updated_at
		FROM quotes AS q
		JOIN quote_updates AS u ON u.id = q.update_id
		WHERE q.pair = $1 AND u.status = 'succeeded'
		ORDER BY q.source_date DESC, q.updated_at DESC, q.update_id DESC
		LIMIT 1`, pair.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return quote.Quote{}, ErrQuoteNotFound
	}

	if err != nil {
		return quote.Quote{}, fmt.Errorf("get latest quote for %s: %w", pair, err)
	}

	return result, nil
}

// SaveSuccess commits the price and succeeded status together, or neither.
// A lease must still be owned and unexpired both before INSERT and at UPDATE.
func (r *Repository) SaveSuccess(ctx context.Context, q quote.Quote, token string) (err error) {
	if err := q.Validate(); err != nil {
		return fmt.Errorf("save successful quote: %w", err)
	}

	if token == "" {
		return fmt.Errorf("%w: empty lease token", ErrInvalidRepositoryArgument)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin save-success transaction: %w", err)
	}
	defer tx.Rollback()

	var dbPair string

	err = tx.QueryRowContext(ctx, `
		SELECT pair FROM quote_updates
		WHERE id = $1 AND status = 'processing'
		  AND lock_token::text = $2 AND locked_until > statement_timestamp()
		FOR UPDATE`, q.UpdateID, token).Scan(&dbPair)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLeaseLost
	}

	if err != nil {
		return fmt.Errorf("lock job for success: %w", err)
	}

	if dbPair != q.Pair.String() {
		return ErrPairMismatch
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO quotes (update_id, pair, price, source_date, updated_at)
		VALUES ($1, $2, $3::numeric, $4::date, $5)`,
		q.UpdateID, q.Pair.String(), q.Price.String(), q.SourceDate.String(), q.UpdatedAt.UTC())
	if err != nil {
		return fmt.Errorf("insert successful quote: %w", err)
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE quote_updates
		SET status = 'succeeded', completed_at = statement_timestamp(),
		    lock_token = NULL, locked_until = NULL,
		    error_code = NULL, error_message = NULL
		WHERE id = $1 AND status = 'processing'
		  AND lock_token::text = $2 AND locked_until > statement_timestamp()`,
		q.UpdateID, token)
	if err != nil {
		return fmt.Errorf("mark job succeeded: %w", err)
	}

	count, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("read success update count: %w", err)
	}

	if count != 1 {
		return ErrLeaseLost // deferred Rollback also removes the inserted quote
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit successful quote: %w", err)
	}
	
	return nil
}
