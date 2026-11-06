package quote

import (
	"context"
	"time"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
)

// Repository is the small set of persistence operations used by the service.
type Repository interface {
	CreateJob(ctx context.Context, pair domain.CurrencyPair, key *string) (domain.UpdateJob, error)
	GetJob(ctx context.Context, id int64) (domain.UpdateJob, error)
	GetQuoteByUpdateID(ctx context.Context, updateID int64) (domain.Quote, error)
	GetLatestQuote(ctx context.Context, pair domain.CurrencyPair) (domain.Quote, error)
	SaveSuccess(ctx context.Context, q domain.Quote, token string) error
}

// FetchedRate contains provider data, but not a job ID or a lease token.
// UpdatedAt is the moment the quote was retrieved, not its source date.
type FetchedRate struct {
	Price      domain.Price
	SourceDate domain.SourceDate
	UpdatedAt  time.Time
}

type RateProvider interface {
	FetchRate(ctx context.Context, pair domain.CurrencyPair) (FetchedRate, error)
}
