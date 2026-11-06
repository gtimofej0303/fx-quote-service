package quote

import (
	"fmt"
	"time"
)

// SourceDate is a timezone-free calendar day (YYYY-MM-DD), not a timestamp.
// It maps to PostgreSQL DATE and to the OpenAPI source_date field.
type SourceDate string

func ParseSourceDate(raw string) (SourceDate, error) {
	if len(raw) != len("2006-01-02") || raw[4] != '-' || raw[7] != '-' {
		return "", fmt.Errorf("%w: expected YYYY-MM-DD", ErrInvalidSourceDate)
	}

	for i := range raw {
		if i == 4 || i == 7 {
			continue
		}

		if raw[i] < '0' || raw[i] > '9' {
			return "", fmt.Errorf("%w: expected YYYY-MM-DD", ErrInvalidSourceDate)
		}
	}

	date, err := time.Parse("2006-01-02", raw)
	if err != nil || date.Year() < 1 || date.Format("2006-01-02") != raw {
		return "", fmt.Errorf("%w: impossible or unsupported calendar date", ErrInvalidSourceDate)
	}

	return SourceDate(raw), nil
}

func (d SourceDate) String() string { return string(d) }

func (d SourceDate) Validate() error {
	_, err := ParseSourceDate(string(d))
	return err
}

// Quote is a successfully retrieved value for a specific update job.
// UpdatedAt is retrieval time, not source rate date or job completion time.
type Quote struct {
	UpdateID   int64
	Pair       CurrencyPair
	Price      Price
	SourceDate SourceDate
	UpdatedAt  time.Time
}

func NewQuote(id int64, pair CurrencyPair, price Price, sourceDate SourceDate, updatedAt time.Time) (Quote, error) {
	q := Quote{
		UpdateID:   id,
		Pair:       pair,
		Price:      price,
		SourceDate: sourceDate,
		UpdatedAt:  updatedAt.UTC(),
	}
	if err := q.Validate(); err != nil {
		return Quote{}, err
	}

	return q, nil
}

func (q Quote) Validate() error {
	if q.UpdateID <= 0 {
		return fmt.Errorf("%w: update ID must be positive", ErrInvalidQuote)
	}

	if err := q.Pair.Validate(); err != nil {
		return fmt.Errorf("%w: pair: %w", ErrInvalidQuote, err)
	}

	if err := q.Price.Validate(); err != nil {
		return fmt.Errorf("%w: price: %w", ErrInvalidQuote, err)
	}

	if err := q.SourceDate.Validate(); err != nil {
		return fmt.Errorf("%w: source date: %w", ErrInvalidQuote, err)
	}

	if q.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: missing retrieval timestamp", ErrInvalidQuote)
	}

	return nil
}

// NewerThan matches the latest-query ordering: source_date DESC,
// updated_at DESC, update_id DESC. Call only on validated quotes.
// Quotes for different currency pairs are not comparable.
func (q Quote) NewerThan(other Quote) bool {
	if q.Pair != other.Pair {
		return false
	}

	if q.SourceDate != other.SourceDate {
		return q.SourceDate > other.SourceDate
	}

	if !q.UpdatedAt.Equal(other.UpdatedAt) {
		return q.UpdatedAt.After(other.UpdatedAt)
	}

	return q.UpdateID > other.UpdateID
}
