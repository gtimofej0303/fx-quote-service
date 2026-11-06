package quote

import "errors"

var (
	ErrUpdateNotFound      = errors.New("update job not found")
	ErrQuoteNotFound       = errors.New("quote not found")
	ErrIdempotencyConflict = errors.New("idempotency key belongs to another pair")
	ErrNoClaimableJob      = errors.New("no claimable update job")
	ErrLeaseLost           = errors.New("update lease is missing or expired")

	ErrMissingRepository  = errors.New("quote service requires a repository")
	ErrInvalidUpdateID    = errors.New("update ID must be positive")
	ErrInconsistentResult = errors.New("repository returned inconsistent quote data")
)
