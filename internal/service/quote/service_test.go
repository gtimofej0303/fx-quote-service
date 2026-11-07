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

var testTime = time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)

// fakeRepository is a test implementation of the Repository interface.
type fakeRepository struct {
	createFn  func(context.Context, domain.CurrencyPair, *string) (domain.UpdateJob, error)
	jobFn     func(context.Context, int64) (domain.UpdateJob, error)
	byIDFn    func(context.Context, int64) (domain.Quote, error)
	latestFn  func(context.Context, domain.CurrencyPair) (domain.Quote, error)
	successFn func(context.Context, domain.Quote, string) error
}

func (f *fakeRepository) CreateJob(ctx context.Context, pair domain.CurrencyPair, key *string) (domain.UpdateJob, error) {
	if f.createFn == nil {
		panic("unexpected CreateJob")
	}

	return f.createFn(ctx, pair, key)
}

func (f *fakeRepository) GetJob(ctx context.Context, id int64) (domain.UpdateJob, error) {
	if f.jobFn == nil {
		panic("unexpected GetJob")
	}

	return f.jobFn(ctx, id)
}

func (f *fakeRepository) GetQuoteByUpdateID(ctx context.Context, id int64) (domain.Quote, error) {
	if f.byIDFn == nil {
		panic("unexpected GetQuoteByUpdateID")
	}

	return f.byIDFn(ctx, id)
}

func (f *fakeRepository) GetLatestQuote(ctx context.Context, pair domain.CurrencyPair) (domain.Quote, error) {
	if f.latestFn == nil {
		panic("unexpected GetLatestQuote")
	}

	return f.latestFn(ctx, pair)
}

func (f *fakeRepository) SaveSuccess(ctx context.Context, q domain.Quote, token string) error {
	if f.successFn == nil {
		panic("unexpected SaveSuccess")
	}

	return f.successFn(ctx, q, token)
}

var _ service.Repository = (*fakeRepository)(nil)

// pendingJob creates a valid pending job for tests.
func pendingJob(t *testing.T) domain.UpdateJob {
	t.Helper()

	job, err := domain.NewPendingJob(42, domain.PairEUREGP, testTime, testTime, nil)
	if err != nil {
		t.Fatal(err)
	}

	return job
}

// processingJob создаёт задание со статусом processing и активной арендой.
func processingJob(t *testing.T) domain.UpdateJob {
	t.Helper()

	job := pendingJob(t)
	job.Status = domain.JobProcessing
	job.Attempts = 1
	job.Lease = &domain.Lease{Token: "lease-42", Until: testTime.Add(time.Hour)}

	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}

	return job
}

// savedQuote creates a valid quote with a high-precision price.
func savedQuote(t *testing.T) domain.Quote {
	t.Helper()

	price, err := domain.ParsePrice("61.960000000000000001")
	if err != nil {
		t.Fatal(err)
	}

	date, err := domain.ParseSourceDate("2026-11-05")
	if err != nil {
		t.Fatal(err)
	}

	q, err := domain.NewQuote(42, domain.PairEUREGP, price, date, testTime)
	if err != nil {
		t.Fatal(err)
	}

	return q
}

// newService creates a service using a fake repository.
func newService(t *testing.T, repo *fakeRepository) *service.Service {
	t.Helper()

	s, err := service.New(repo)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

// TestNewRequiresRepository verifies that a repository is required.
func TestNewRequiresRepository(t *testing.T) {
	if _, err := service.New(nil); !errors.Is(err, service.ErrMissingRepository) {
		t.Fatalf("nil repository: %v", err)
	}
}

// TestRequestUpdateValidatesAndNormalizes checks input validation and normalization.
func TestRequestUpdateValidatesAndNormalizes(t *testing.T) {
	ctx := context.Background()
	key := "Op-42:EUR_EGP"
	calls := 0

	repo := &fakeRepository{createFn: func(gotCtx context.Context, pair domain.CurrencyPair, gotKey *string) (domain.UpdateJob, error) {
		calls++

		if gotCtx != ctx || pair != domain.PairEUREGP || gotKey == nil || *gotKey != key {
			t.Fatalf("wrong persistence args: ctx=%v pair=%q key=%v", gotCtx, pair, gotKey)
		}

		job := pendingJob(t)

		copied := *gotKey
		job.IdempotencyKey = &copied

		return job, nil
	}}

	s := newService(t, repo)

	job, err := s.RequestUpdate(ctx, "  eur/egp \n", &key)
	if err != nil || job.ID != 42 || calls != 1 {
		t.Fatalf("request update: job=%+v, err=%v, calls=%d", job, err, calls)
	}

	invalid := []struct {
		pair string
		key  *string
		want error
	}{
		{pair: "EUR-MXN", want: domain.ErrInvalidPair},
		{pair: "EUR/MXN", want: domain.ErrUnsupportedPair},
		{pair: "eur/egp", key: ptr(" leading-space"), want: domain.ErrInvalidIdempotencyKey},
		{pair: "eur/egp", key: ptr(""), want: domain.ErrInvalidIdempotencyKey},
	}

	for _, tc := range invalid {
		_, err := s.RequestUpdate(ctx, tc.pair, tc.key)
		if !errors.Is(err, tc.want) {
			t.Errorf("pair=%q key=%v got=%v, want %v", tc.pair, tc.key, err, tc.want)
		}
	}

	if calls != 1 {
		t.Fatalf("invalid requests must not reach repository: got %d calls", calls)
	}
}

// ptr returns a pointer to the provided string.
func ptr(s string) *string { return &s }

// TestRequestUpdateIdempotencyAndErrors checks idempotency and repository errors.
func TestRequestUpdateIdempotencyAndErrors(t *testing.T) {
	ctx := context.Background()
	persisted := pendingJob(t)
	key := "operation-42"
	persisted.IdempotencyKey = &key
	calls := 0

	repo := &fakeRepository{createFn: func(_ context.Context, pair domain.CurrencyPair, gotKey *string) (domain.UpdateJob, error) {
		calls++

		if gotKey != nil {
			if *gotKey != key {
				t.Fatalf("key changed: %q", *gotKey)
			}

			return persisted, nil
		}

		job := pendingJob(t)
		job.ID = int64(100 + calls)

		return job, nil
	}}

	s := newService(t, repo)

	first, err := s.RequestUpdate(ctx, "EUR/EGP", &key)
	if err != nil {
		t.Fatal(err)
	}

	second, err := s.RequestUpdate(ctx, "EUR/EGP", &key)
	if err != nil || first.ID != second.ID {
		t.Fatalf("replay changed job ID: %d, %d, %v", first.ID, second.ID, err)
	}

	third, err := s.RequestUpdate(ctx, "EUR/EGP", nil)
	if err != nil || third.ID == first.ID {
		t.Fatalf("absent key must allow new job: %+v / %v", third, err)
	}

	fourth, err := s.RequestUpdate(ctx, "EUR/EGP", nil)
	if err != nil || fourth.ID == third.ID {
		t.Fatalf("unkeyed requests must differ: %+v / %v", fourth, err)
	}

	conflict := service.ErrIdempotencyConflict

	repo.createFn = func(context.Context, domain.CurrencyPair, *string) (domain.UpdateJob, error) {
		return domain.UpdateJob{}, fmt.Errorf("db conflict: %w", conflict)
	}

	if _, err := s.RequestUpdate(ctx, "EUR/EGP", &key); !errors.Is(err, conflict) {
		t.Fatalf("repository conflict must remain identifiable: %v", err)
	}

	repo.createFn = func(context.Context, domain.CurrencyPair, *string) (domain.UpdateJob, error) {
		return pendingJob(t), nil
	}
	if _, err := s.RequestUpdate(ctx, "USD/EUR", nil); !errors.Is(err, service.ErrInconsistentResult) {
		t.Fatalf("unexpected pair must be rejected: %v", err)
	}
}

// TestGetUpdateByStatus checks the result returned for each job status.
func TestGetUpdateByStatus(t *testing.T) {
	base := pendingJob(t)
	saved := savedQuote(t)

	cases := []struct {
		name    string
		job     domain.UpdateJob
		wantOne bool
	}{
		{name: "pending", job: base},
		{name: "processing", job: processingJob(t)},
		{name: "succeeded", job: func() domain.UpdateJob {
			job := processingJob(t)
			job.Status = domain.JobSucceeded
			job.Lease = nil
			completed := testTime.Add(time.Minute)
			job.CompletedAt = &completed
			return job
		}(), wantOne: true},
		{name: "failed", job: func() domain.UpdateJob {
			job := processingJob(t)
			job.Status = domain.JobFailed
			job.Lease = nil
			completed := testTime.Add(time.Minute)
			job.CompletedAt = &completed
			job.Failure = &domain.UpdateFailure{Code: domain.FailureProviderRateNotFound, Message: "not found"}
			return job
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			quoteReads := 0

			repo := &fakeRepository{
				jobFn: func(_ context.Context, id int64) (domain.UpdateJob, error) {
					if id != 42 {
						t.Fatalf("wrong ID %d", id)
					}
					return tc.job, nil
				},
				byIDFn: func(_ context.Context, id int64) (domain.Quote, error) {
					quoteReads++
					return saved, nil
				},
			}

			result, err := newService(t, repo).GetUpdate(context.Background(), 42)
			if err != nil {
				t.Fatal(err)
			}

			if result.Job.Status != tc.job.Status || (result.Quote != nil) != tc.wantOne {
				t.Fatalf("wrong result: %+v", result)
			}

			if tc.wantOne && result.Quote.Price.String() != "61.960000000000000001" {
				t.Fatalf("price lost precision: %q", result.Quote.Price.String())
			}

			if got := quoteReads; (got == 1) != tc.wantOne {
				t.Fatalf("unexpected quote lookups: %d", got)
			}

			if tc.job.Status == domain.JobFailed && (result.Job.Failure == nil || result.Job.Failure.Code != domain.FailureProviderRateNotFound) {
				t.Fatalf("failed must return error as data: %+v", result.Job)
			}
		})
	}
}

// TestGetUpdateRejectsBadIDAndPropagatesStorageErrors checks invalid IDs and storage failures.
func TestGetUpdateRejectsBadIDAndPropagatesStorageErrors(t *testing.T) {
	missing := service.ErrUpdateNotFound

	repo := &fakeRepository{jobFn: func(context.Context, int64) (domain.UpdateJob, error) {
		return domain.UpdateJob{}, missing
	}}

	s := newService(t, repo)

	for _, bad := range []int64{0, -1} {
		if _, err := s.GetUpdate(context.Background(), bad); !errors.Is(err, service.ErrInvalidUpdateID) {
			t.Errorf("invalid ID %d: %v", bad, err)
		}
	}

	if _, err := s.GetUpdate(context.Background(), 42); !errors.Is(err, missing) {
		t.Fatalf("not-found identity lost: %v", err)
	}

	repo.jobFn = func(context.Context, int64) (domain.UpdateJob, error) {
		job := pendingJob(t)
		job.ID = 123
		return job, nil
	}

	if _, err := s.GetUpdate(context.Background(), 42); !errors.Is(err, service.ErrInconsistentResult) {
		t.Fatalf("mismatched job ID: %v", err)
	}

	repo.jobFn = func(context.Context, int64) (domain.UpdateJob, error) {
		job := pendingJob(t)
		job.Status = domain.JobSucceeded
		completed := testTime.Add(time.Minute)
		job.CompletedAt = &completed
		return job, nil
	}

	quoteMissing := service.ErrQuoteNotFound

	repo.byIDFn = func(context.Context, int64) (domain.Quote, error) {
		return domain.Quote{}, fmt.Errorf("storage: %w", quoteMissing)
	}

	if _, err := s.GetUpdate(context.Background(), 42); !errors.Is(err, service.ErrInconsistentResult) || errors.Is(err, service.ErrQuoteNotFound) {
		t.Fatalf("succeeded job with missing quote must be inconsistent, not 404: %v", err)
	}

	storageFailure := errors.New("database unavailable")
	repo.byIDFn = func(context.Context, int64) (domain.Quote, error) {
		return domain.Quote{}, fmt.Errorf("read failed: %w", storageFailure)
	}

	if _, err := s.GetUpdate(context.Background(), 42); !errors.Is(err, storageFailure) {
		t.Fatalf("unexpected storage failure must preserve its cause: %v", err)
	}

	repo.byIDFn = func(context.Context, int64) (domain.Quote, error) {
		q := savedQuote(t)
		q.Pair = domain.PairUSDEUR
		return q, nil
	}

	if _, err := s.GetUpdate(context.Background(), 42); !errors.Is(err, service.ErrInconsistentResult) {
		t.Fatalf("mismatched quote: %v", err)
	}
}

// TestGetLatestQuoteValidationAndRepositoryContract checks latest-quote validation.
func TestGetLatestQuoteValidationAndRepositoryContract(t *testing.T) {
	calls := 0

	q := savedQuote(t)

	repo := &fakeRepository{latestFn: func(_ context.Context, pair domain.CurrencyPair) (domain.Quote, error) {
		calls++

		if pair != domain.PairEUREGP {
			t.Fatalf("pair not normalized: %q", pair)
		}
		return q, nil
	}}

	s := newService(t, repo)

	got, err := s.GetLatestQuote(context.Background(), " eur/egp ")
	if err != nil || got.Price.String() != q.Price.String() || calls != 1 {
		t.Fatalf("latest result: %+v / %v / %d", got, err, calls)
	}

	for _, pair := range []string{"eur-egp", "EUR/MXN"} {
		if _, err := s.GetLatestQuote(context.Background(), pair); err == nil {
			t.Fatalf("should reject pair %q", pair)
		}
	}

	if calls != 1 {
		t.Fatalf("invalid pair must not reach database: %d", calls)
	}

	missing := service.ErrQuoteNotFound

	repo.latestFn = func(context.Context, domain.CurrencyPair) (domain.Quote, error) {
		return domain.Quote{}, missing
	}

	if _, err := s.GetLatestQuote(context.Background(), "EUR/EGP"); !errors.Is(err, missing) {
		t.Fatalf("repository error lost: %v", err)
	}

	repo.latestFn = func(context.Context, domain.CurrencyPair) (domain.Quote, error) {
		q.Pair = domain.PairEURUSD
		return q, nil
	}

	if _, err := s.GetLatestQuote(context.Background(), "EUR/EGP"); !errors.Is(err, service.ErrInconsistentResult) {
		t.Fatalf("wrong pair returned by storage: %v", err)
	}
}
