package quote_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
	service "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
)

type fakeRateProvider struct {
	fetchFn func(context.Context, domain.CurrencyPair) (service.FetchedRate, error)
}

// FetchRate returns a rate or error from the test provider.
func (f *fakeRateProvider) FetchRate(ctx context.Context, pair domain.CurrencyPair) (service.FetchedRate, error) {
	if f.fetchFn == nil {
		panic("unexpected FetchRate")
	}

	return f.fetchFn(ctx, pair)
}

var _ service.RateProvider = (*fakeRateProvider)(nil)

// rate creates a valid provider rate for tests.
func rate(t *testing.T) service.FetchedRate {
	t.Helper()

	q := savedQuote(t)

	return service.FetchedRate{Price: q.Price, SourceDate: q.SourceDate, UpdatedAt: q.UpdatedAt}
}

// Check that a successful job saves exactly one quote.
func TestProcessJobWritesExactlyOneSuccessfulQuote(t *testing.T) {
	ctx := context.Background()
	job := processingJob(t)
	fetched := rate(t)
	callsFetch, callsSave := 0, 0

	repo := &fakeRepository{successFn: func(gotCtx context.Context, q domain.Quote, token string) error {
		callsSave++

		if gotCtx != ctx || token != job.Lease.Token || q.UpdateID != job.ID || q.Pair != job.Pair || q.Price.String() != "61.960000000000000001" || q.SourceDate != fetched.SourceDate || !q.UpdatedAt.Equal(fetched.UpdatedAt) {
			t.Fatalf("bad saved quote: %+v, token=%q", q, token)
		}

		return nil
	}}

	provider := &fakeRateProvider{fetchFn: func(gotCtx context.Context, pair domain.CurrencyPair) (service.FetchedRate, error) {
		callsFetch++

		if gotCtx != ctx || pair != job.Pair {
			t.Fatalf("wrong provider args: %q", pair)
		}

		return fetched, nil
	}}

	result, err := newService(t, repo).ProcessJob(ctx, job, provider)
	if err != nil || result.Failure != nil || result.Retryable || callsFetch != 1 || callsSave != 1 {
		t.Fatalf("success: result=%+v err=%v fetches=%d writes=%d", result, err, callsFetch, callsSave)
	}
}

// Check jobs without a lease and services without a provider.
func TestProcessJobRejectsUnclaimedAndMissingProvider(t *testing.T) {
	s := newService(t, &fakeRepository{})
	provider := &fakeRateProvider{}

	for _, job := range []domain.UpdateJob{pendingJob(t), {}, func() domain.UpdateJob {
		j := processingJob(t)
		j.Lease = nil
		return j
	}()} {
		if _, err := s.ProcessJob(context.Background(), job, provider); !errors.Is(err, service.ErrInvalidClaimedJob) {
			t.Fatalf("unclaimed job: %+v / %v", job, err)
		}
	}

	if _, err := s.ProcessJob(context.Background(), processingJob(t), nil); !errors.Is(err, service.ErrMissingProvider) {
		t.Fatalf("missing provider: %v", err)
	}
}

// Check known provider error types.
func TestProcessJobClassifiesTypedProviderFailures(t *testing.T) {
	cases := []struct {
		code      domain.FailureCode
		retryable bool
	}{
		{domain.FailureProviderTimeout, true},
		{domain.FailureProviderUnavailable, true},
		{domain.FailureProviderRateLimited, true},
		{domain.FailureProviderRateNotFound, false},
		{domain.FailureProviderRejectedRequest, false},
		{domain.FailureProviderInvalidResponse, false},
	}

	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			cause := errors.New("upstream internal information")
			notBefore := time.Date(2026, 11, 8, 12, 1, 0, 0, time.UTC)
			providerErr := &service.ProviderError{
				Code: tc.code, Message: "Safe user-facing message", Cause: cause,
				RetryNotBefore: notBefore,
			}

			provider := &fakeRateProvider{fetchFn: func(context.Context, domain.CurrencyPair) (service.FetchedRate, error) {
				return service.FetchedRate{}, fmt.Errorf("wrapped: %w", providerErr)
			}}

			result, err := newService(t, &fakeRepository{}).ProcessJob(context.Background(), processingJob(t), provider)
			if err != nil || result.Failure == nil || result.Failure.Code != tc.code || result.Failure.Message != providerErr.Message || result.Retryable != tc.retryable {
				t.Fatalf("classification: %+v / %v", result, err)
			}
			if !result.RetryNotBefore.Equal(notBefore) {
				t.Fatalf("provider retry time was lost: %v", result.RetryNotBefore)
			}
			if !errors.Is(result.Diagnostic, cause) {
				t.Fatalf("provider cause was lost: %v", result.Diagnostic)
			}

			if !errors.Is(providerErr, cause) {
				t.Fatal("diagnostic cause must be unwrappable")
			}
		})
	}
}

// Check provider errors without a known type.
func TestProcessJobClassifiesUnspecifiedProviderFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want domain.FailureCode
	}{
		{"network_error", errors.New("network problem"), domain.FailureProviderUnavailable},
		{"provider_deadline", fmt.Errorf("fetch: %w", context.DeadlineExceeded), domain.FailureProviderTimeout},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeRateProvider{fetchFn: func(context.Context, domain.CurrencyPair) (service.FetchedRate, error) {
				return service.FetchedRate{}, tc.err
			}}

			result, err := newService(t, &fakeRepository{}).ProcessJob(context.Background(), processingJob(t), provider)
			if err != nil || result.Failure == nil || result.Failure.Code != tc.want || !result.Retryable {
				t.Fatalf("fallback: %+v / %v", result, err)
			}
			if !errors.Is(result.Diagnostic, tc.err) {
				t.Fatalf("untyped cause was lost: %v", result.Diagnostic)
			}
		})
	}
}

// Check provider errors with invalid codes.
func TestProcessJobRejectsInvalidProviderError(t *testing.T) {
	for _, tc := range []service.ProviderError{
		{Code: "invalid", Message: "bad"},
		{Code: domain.FailureProviderTimeout, Message: "  "},
		{Code: domain.FailureProcessingTimeout, Message: "not a provider code"},
	} {
		provider := &fakeRateProvider{fetchFn: func(context.Context, domain.CurrencyPair) (service.FetchedRate, error) {
			return service.FetchedRate{}, &tc
		}}

		result, err := newService(t, &fakeRepository{}).ProcessJob(context.Background(), processingJob(t), provider)
		if !errors.Is(err, service.ErrInvalidProviderError) || result.Failure != nil {
			t.Fatalf("invalid provider classification %+v: %+v / %v", tc, result, err)
		}
	}
}

// Check that invalid rates are not saved.
func TestProcessJobRejectsInvalidFetchedRateWithoutStorageWrite(t *testing.T) {
	for _, tc := range []struct {
		name string
		rate service.FetchedRate
	}{
		{"zero_price", func() service.FetchedRate { r := rate(t); r.Price = domain.Price{}; return r }()},
		{"no_date", func() service.FetchedRate { r := rate(t); r.SourceDate = ""; return r }()},
		{"no_retrieval_time", func() service.FetchedRate { r := rate(t); r.UpdatedAt = time.Time{}; return r }()},
		{"future_source_date", func() service.FetchedRate { r := rate(t); r.SourceDate = "2099-01-01"; return r }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := tc.rate

			provider := &fakeRateProvider{fetchFn: func(context.Context, domain.CurrencyPair) (service.FetchedRate, error) {
				return bad, nil
			}}

			result, err := newService(t, &fakeRepository{}).ProcessJob(context.Background(), processingJob(t), provider)
			if err != nil || result.Failure == nil || result.Failure.Code != domain.FailureProviderInvalidResponse || result.Retryable {
				t.Fatalf("invalid rate: %+v / %v", result, err)
			}
			if !errors.Is(result.Diagnostic, domain.ErrInvalidQuote) {
				t.Fatalf("domain validation cause was lost: %v", result.Diagnostic)
			}
		})
	}
}

// Check database and lease errors.
func TestProcessJobPropagatesPersistenceAndLeaseErrors(t *testing.T) {
	stale := service.ErrLeaseLost

	repo := &fakeRepository{successFn: func(context.Context, domain.Quote, string) error {
		return fmt.Errorf("repository: %w", stale)
	}}
	provider := &fakeRateProvider{fetchFn: func(context.Context, domain.CurrencyPair) (service.FetchedRate, error) {
		return rate(t), nil
	}}

	result, err := newService(t, repo).ProcessJob(context.Background(), processingJob(t), provider)
	if !errors.Is(err, stale) || result.Failure != nil {
		t.Fatalf("failed SaveSuccess should not be treated as a provider failure: %+v / %v", result, err)
	}
}

// Check that canceling stops the job before saving.
func TestProcessJobDoesNotFinalizeAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := newService(t, &fakeRepository{}).ProcessJob(ctx, processingJob(t), &fakeRateProvider{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel: %v", err)
	}

	ctx, cancel = context.WithCancel(context.Background())

	provider := &fakeRateProvider{fetchFn: func(context.Context, domain.CurrencyPair) (service.FetchedRate, error) {
		cancel() // stop while the provider request is running
		return rate(t), nil
	}}

	result, err := newService(t, &fakeRepository{}).ProcessJob(ctx, processingJob(t), provider)
	if !errors.Is(err, context.Canceled) || result.Failure != nil {
		t.Fatalf("shutdown must not persist a result: %+v / %v", result, err)
	}
}
