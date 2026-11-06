package postgres

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
)

// rowScanner allows the same conversion for QueryRowContext and tx.QueryRowContext.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanJob converts nullable SQL columns to the already-defined domain model.
func scanJob(row rowScanner) (quote.UpdateJob, error) {
	var j quote.UpdateJob
	var pair, status string
	var key, token, code, message sql.NullString
	var completedAt, lockedUntil sql.NullTime

	err := row.Scan(
		&j.ID, &pair, &status, &key, &j.Attempts,
		&j.NextAttemptAt, &j.CreatedAt, &completedAt,
		&lockedUntil, &token, &code, &message,
	)
	if err != nil {
		return quote.UpdateJob{}, err
	}

	j.Pair = quote.CurrencyPair(pair)
	j.Status = quote.JobStatus(status)

	j.CreatedAt = j.CreatedAt.UTC()
	j.NextAttemptAt = j.NextAttemptAt.UTC()

	if key.Valid {
		value := key.String
		j.IdempotencyKey = &value
	}

	if completedAt.Valid {
		value := completedAt.Time.UTC()
		j.CompletedAt = &value
	}

	if token.Valid != lockedUntil.Valid || code.Valid != message.Valid {
		return quote.UpdateJob{}, fmt.Errorf("%w: inconsistent nullable job fields", ErrCorruptData)
	}

	if token.Valid {
		j.Lease = &quote.Lease{Token: token.String, Until: lockedUntil.Time.UTC()}
	}

	if code.Valid {
		j.Failure = &quote.UpdateFailure{Code: quote.FailureCode(code.String), Message: message.String}
	}

	if err := j.Validate(); err != nil {
		return quote.UpdateJob{}, fmt.Errorf("%w: %w", ErrCorruptData, err)
	}

	return j, nil
}

// Scan numeric/date as PostgreSQL text.
func scanQuote(row rowScanner) (quote.Quote, error) {
	var id int64
	var pair, priceText, sourceDateText string
	var updatedAt time.Time

	if err := row.Scan(&id, &pair, &priceText, &sourceDateText, &updatedAt); err != nil {
		return quote.Quote{}, err
	}

	price, err := quote.ParsePrice(priceText)
	if err != nil {
		return quote.Quote{}, fmt.Errorf("%w: price: %w", ErrCorruptData, err)
	}

	date, err := quote.ParseSourceDate(sourceDateText)
	if err != nil {
		return quote.Quote{}, fmt.Errorf("%w: source date: %w", ErrCorruptData, err)
	}

	result, err := quote.NewQuote(id, quote.CurrencyPair(pair), price, date, updatedAt)
	if err != nil {
		return quote.Quote{}, fmt.Errorf("%w: quote: %w", ErrCorruptData, err)
	}

	return result, nil
}
