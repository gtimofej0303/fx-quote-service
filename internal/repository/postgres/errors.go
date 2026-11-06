package postgres

import (
	"errors"

	quoteservice "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
)

var (
	ErrJobNotFound         = quoteservice.ErrUpdateNotFound
	ErrQuoteNotFound       = quoteservice.ErrQuoteNotFound
	ErrNoClaimableJob      = quoteservice.ErrNoClaimableJob
	ErrIdempotencyConflict = quoteservice.ErrIdempotencyConflict
	ErrLeaseLost           = quoteservice.ErrLeaseLost

	ErrPairMismatch              = errors.New("quote pair differs from update pair")
	ErrInvalidRepositoryArgument = errors.New("invalid repository argument")
	ErrCorruptData               = errors.New("invalid persisted quote data")
)
