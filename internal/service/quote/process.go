package quote

import (
	"context"
	"errors"
	"fmt"
	"time"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
)

var (
	ErrMissingProvider      = errors.New("processing requires a rate provider")
	ErrInvalidClaimedJob    = errors.New("processing requires a claimed job")
	ErrInvalidProviderError = errors.New("provider returned an invalid failure classification")
)

// ProviderError holds an expected error from the rate provider.
type ProviderError struct {
	Code           domain.FailureCode
	Message        string
	Cause          error
	RetryNotBefore time.Time
}

// Error returns the error message.
func (e *ProviderError) Error() string {
	if e == nil {
		return "quote provider error"
	}

	if e.Message != "" {
		return e.Message
	}

	if e.Cause != nil {
		return e.Cause.Error()
	}

	return "quote provider error"
}

// Unwrap returns the original error.
func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Cause
}

// ProcessResult holds the result of one try.
type ProcessResult struct {
	Failure        *domain.UpdateFailure
	Retryable      bool
	RetryNotBefore time.Time
	Diagnostic     error `json:"-"` // Internal error; do not show it in the API response.
}

// ProcessJob runs a quote update job that a worker has taken.
func (s *Service) ProcessJob(ctx context.Context, job domain.UpdateJob, provider RateProvider) (ProcessResult, error) {
	if err := job.Validate(); err != nil {
		return ProcessResult{}, fmt.Errorf("%w: %w", ErrInvalidClaimedJob, err)
	}

	if job.Status != domain.JobProcessing || job.Lease == nil {
		return ProcessResult{}, ErrInvalidClaimedJob
	}

	if provider == nil {
		return ProcessResult{}, ErrMissingProvider
	}

	if err := ctx.Err(); err != nil {
		return ProcessResult{}, err
	}

	fetched, err := provider.FetchRate(ctx, job.Pair)

	// Handle cancellation when the worker stops or runs out of time.
	if ctx.Err() != nil {
		return ProcessResult{}, ctx.Err()
	}

	if err != nil {
		return classifyProviderError(err)
	}

	q, err := domain.NewQuote(job.ID, job.Pair, fetched.Price, fetched.SourceDate, fetched.UpdatedAt)
	if err != nil {
		failure := domain.UpdateFailure{
			Code:    domain.FailureProviderInvalidResponse,
			Message: "Provider returned an invalid quote",
		}

		return ProcessResult{Failure: &failure, Diagnostic: err}, nil
	}

	// SaveSuccess checks the lease and saves the quote and success status in one step.
	if err := s.repository.SaveSuccess(ctx, q, job.Lease.Token); err != nil {
		return ProcessResult{}, fmt.Errorf("save quote for update %d: %w", job.ID, err)
	}

	return ProcessResult{}, nil
}

// classifyProviderError turns a provider error into a job result.
func classifyProviderError(err error) (ProcessResult, error) {
	var providerErr *ProviderError

	if errors.As(err, &providerErr) {
		if providerErr == nil || providerErr.Code == domain.FailureProcessingTimeout {
			return ProcessResult{}, fmt.Errorf("%w: %v", ErrInvalidProviderError, err)
		}

		failure := domain.UpdateFailure{Code: providerErr.Code, Message: providerErr.Message}
		if validationErr := failure.Validate(); validationErr != nil {
			return ProcessResult{}, fmt.Errorf("%w: %v", ErrInvalidProviderError, validationErr)
		}

		diagnostic := providerErr.Cause
		if diagnostic == nil {
			diagnostic = err
		}

		return ProcessResult{
			Failure:        &failure,
			Retryable:      isRetryableProviderCode(failure.Code),
			RetryNotBefore: providerErr.RetryNotBefore,
			Diagnostic:     diagnostic,
		}, nil
	}

	// Treat unknown errors as temporary provider problems.
	code := domain.FailureProviderUnavailable
	message := "Quote provider is temporarily unavailable"

	if errors.Is(err, context.DeadlineExceeded) {
		code = domain.FailureProviderTimeout
		message = "Quote provider timed out"
	}

	failure := domain.UpdateFailure{Code: code, Message: message}

	return ProcessResult{Failure: &failure, Retryable: true, Diagnostic: err}, nil
}

// isRetryableProviderCode checks if we can try again after this provider error.
func isRetryableProviderCode(code domain.FailureCode) bool {
	switch code {
	case domain.FailureProviderTimeout, domain.FailureProviderUnavailable, domain.FailureProviderRateLimited:
		return true
	default:
		return false
	}
}
