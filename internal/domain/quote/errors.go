package quote

import "errors"

var (
	ErrInvalidPair           = errors.New("invalid currency pair")
	ErrUnsupportedPair       = errors.New("unsupported currency pair")
	ErrInvalidPrice          = errors.New("invalid quote price")
	ErrInvalidSourceDate     = errors.New("invalid source date")
	ErrInvalidQuote          = errors.New("invalid quote")
	ErrInvalidJob            = errors.New("invalid update job")
	ErrInvalidJobStatus      = errors.New("invalid job status")
	ErrInvalidTransition     = errors.New("invalid job status transition")
	ErrInvalidFailure        = errors.New("invalid update failure")
	ErrInvalidIdempotencyKey = errors.New("invalid idempotency key")
)
