package postgres

import "errors"

var (
	ErrJobNotFound               = errors.New("update job not found")
	ErrQuoteNotFound             = errors.New("quote not found")
	ErrNoClaimableJob            = errors.New("no claimable update job")
	ErrIdempotencyConflict       = errors.New("idempotency key belongs to another pair")
	ErrLeaseLost                 = errors.New("update lease is missing or expired")
	ErrPairMismatch              = errors.New("quote pair differs from update pair")
	ErrInvalidRepositoryArgument = errors.New("invalid repository argument")
	ErrCorruptData               = errors.New("invalid persisted quote data")
)
