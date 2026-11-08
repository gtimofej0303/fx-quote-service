package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
	quoteservice "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
)

type fakeQueue struct {
	job          domain.UpdateJob
	claimErr     error
	exhausted    int64
	exhaustedErr error
	claimCalls   int
	maxSeen      int
	leaseSeen    time.Duration
	batchSeen    int
	retryAt      time.Time
	retryCalls   int
	failure      *domain.UpdateFailure
	failureCalls int
	writeErr     error
}

func (q *fakeQueue) ClaimNext(_ context.Context, lease time.Duration, max int) (domain.UpdateJob, error) {
	q.claimCalls++

	q.leaseSeen, q.maxSeen = lease, max

	if q.claimErr != nil {
		return domain.UpdateJob{}, q.claimErr
	}

	if q.job.ID == 0 {
		return domain.UpdateJob{}, quoteservice.ErrNoClaimableJob
	}

	job := q.job

	q.job = domain.UpdateJob{}

	return job, nil
}

func (q *fakeQueue) FailExhausted(_ context.Context, max, batch int) (int64, error) {
	q.maxSeen, q.batchSeen = max, batch

	return q.exhausted, q.exhaustedErr
}

func (q *fakeQueue) ScheduleRetry(_ context.Context, id int64, token string, at time.Time) error {
	if id <= 0 || token == "" {
		panic("invalid retry arguments")
	}

	q.retryCalls++

	q.retryAt = at

	return q.writeErr
}

func (q *fakeQueue) SaveFailure(_ context.Context, id int64, token string, failure domain.UpdateFailure) error {
	if id <= 0 || token == "" {
		panic("invalid failure arguments")
	}

	q.failureCalls++

	q.failure = &failure

	return q.writeErr
}

type fakeProcessor func(context.Context, domain.UpdateJob, quoteservice.RateProvider) (quoteservice.ProcessResult, error)

func (p fakeProcessor) ProcessJob(ctx context.Context, j domain.UpdateJob, r quoteservice.RateProvider) (quoteservice.ProcessResult, error) {
	return p(ctx, j, r)
}

type fakeProvider func(context.Context, domain.CurrencyPair) (quoteservice.FetchedRate, error)

func (p fakeProvider) FetchRate(ctx context.Context, pair domain.CurrencyPair) (quoteservice.FetchedRate, error) {
	return p(ctx, pair)
}

var nowFixed = time.Date(2026, 11, 7, 18, 0, 0, 0, time.UTC)

func validClaim(attempt int) domain.UpdateJob {
	return domain.UpdateJob{
		ID: 41, Pair: domain.PairUSDEGP, Status: domain.JobProcessing,
		Attempts: attempt, CreatedAt: nowFixed.Add(-time.Minute), NextAttemptAt: nowFixed.Add(-time.Second),
		Lease: &domain.Lease{Token: "test-lease", Until: nowFixed.Add(time.Minute)},
	}
}

func testWorker(t *testing.T, q *fakeQueue, p fakeProcessor) *Worker {
	t.Helper()

	provider := fakeProvider(func(context.Context, domain.CurrencyPair) (quoteservice.FetchedRate, error) {
		t.Fatal("unexpected provider invocation")

		return quoteservice.FetchedRate{}, nil
	})

	worker, err := New(q, p, provider, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	worker.now = func() time.Time { return nowFixed }

	return worker
}

func TestNewValidatesDependencies(t *testing.T) {
	q := &fakeQueue{}

	p := fakeProcessor(func(context.Context, domain.UpdateJob, quoteservice.RateProvider) (quoteservice.ProcessResult, error) {
		return quoteservice.ProcessResult{}, nil
	})

	r := fakeProvider(func(context.Context, domain.CurrencyPair) (quoteservice.FetchedRate, error) {
		return quoteservice.FetchedRate{}, nil
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	for _, deps := range []struct {
		q Queue
		p Processor
		r quoteservice.RateProvider
		l *slog.Logger
	}{
		{nil, p, r, logger}, {q, nil, r, logger}, {q, p, nil, logger}, {q, p, r, nil},
	} {
		if _, err := New(deps.q, deps.p, deps.r, deps.l); err == nil {
			t.Fatal("constructor accepted missing dependency")
		}
	}
}

func TestRunOneNoJobAndMaintenance(t *testing.T) {
	q := &fakeQueue{exhausted: 2}

	p := fakeProcessor(func(context.Context, domain.UpdateJob, quoteservice.RateProvider) (quoteservice.ProcessResult, error) {
		t.Fatal("processor must not run without a claim")

		return quoteservice.ProcessResult{}, nil
	})

	if err := testWorker(t, q, p).runOne(context.Background()); err != nil {
		t.Fatal(err)
	}

	if q.claimCalls != 1 || q.maxSeen != maxAttempts || q.leaseSeen != leaseDuration || q.batchSeen != exhaustedBatchSize {
		t.Fatalf("incorrect repository arguments: %+v", q)
	}
}

func TestRunOneSuccess(t *testing.T) {
	q := &fakeQueue{job: validClaim(1)}

	var calls atomic.Int32

	p := fakeProcessor(func(ctx context.Context, job domain.UpdateJob, provider quoteservice.RateProvider) (quoteservice.ProcessResult, error) {
		calls.Add(1)

		if job.ID != 41 || job.Pair != domain.PairUSDEGP || ctx.Err() != nil || provider == nil {
			t.Fatalf("bad processing arguments: %+v", job)
		}

		return quoteservice.ProcessResult{}, nil
	})

	if err := testWorker(t, q, p).runOne(context.Background()); err != nil {
		t.Fatal(err)
	}

	if calls.Load() != 1 || q.retryCalls != 0 || q.failureCalls != 0 {
		t.Fatalf("success must not schedule/fail: %+v", q)
	}
}

func TestRunOneRetryOrFailure(t *testing.T) {
	temporary := domain.UpdateFailure{Code: domain.FailureProviderRateLimited, Message: "rate limit"}
	permanent := domain.UpdateFailure{Code: domain.FailureProviderInvalidResponse, Message: "invalid response"}

	cases := []struct {
		name     string
		attempt  int
		failure  domain.UpdateFailure
		retry    bool
		wantWait time.Duration
		wantFail bool
	}{
		{"first temporary", 1, temporary, true, 2 * time.Second, false},
		{"second temporary", 2, temporary, true, 4 * time.Second, false},
		{"last temporary", 3, temporary, true, 0, true},
		{"permanent", 1, permanent, false, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeQueue{job: validClaim(tc.attempt)}

			p := fakeProcessor(func(context.Context, domain.UpdateJob, quoteservice.RateProvider) (quoteservice.ProcessResult, error) {
				failure := tc.failure

				return quoteservice.ProcessResult{Failure: &failure, Retryable: tc.retry}, nil
			})

			if err := testWorker(t, q, p).runOne(context.Background()); err != nil {
				t.Fatal(err)
			}

			if tc.wantFail {
				if q.failureCalls != 1 || q.failure == nil || *q.failure != tc.failure || q.retryCalls != 0 {
					t.Fatalf("wrong terminal failure: %+v", q)
				}
			} else if q.retryCalls != 1 || q.failureCalls != 0 || !q.retryAt.Equal(nowFixed.Add(tc.wantWait)) {
				t.Fatalf("wrong backoff: %+v, wanted %s", q, tc.wantWait)
			}
		})
	}
}

func TestRunOneErrorsAndLostLease(t *testing.T) {
	failure := domain.UpdateFailure{Code: domain.FailureProviderUnavailable, Message: "unavailable"}

	p := fakeProcessor(func(context.Context, domain.UpdateJob, quoteservice.RateProvider) (quoteservice.ProcessResult, error) {
		return quoteservice.ProcessResult{Failure: &failure, Retryable: true}, nil
	})

	q := &fakeQueue{exhaustedErr: errors.New("db down")}
	if err := testWorker(t, q, p).runOne(context.Background()); err == nil || q.claimCalls != 0 {
		t.Fatalf("maintenance failure must prevent claiming: %v", err)
	}

	q = &fakeQueue{job: validClaim(1), writeErr: quoteservice.ErrLeaseLost}
	if err := testWorker(t, q, p).runOne(context.Background()); err != nil {
		t.Fatalf("expected benign lost lease: %v", err)
	}

	q = &fakeQueue{job: validClaim(1), writeErr: errors.New("connection lost")}
	if err := testWorker(t, q, p).runOne(context.Background()); err == nil {
		t.Fatal("write error must not be silently swallowed")
	}

	q = &fakeQueue{job: validClaim(1)}
	p = fakeProcessor(func(context.Context, domain.UpdateJob, quoteservice.RateProvider) (quoteservice.ProcessResult, error) {
		return quoteservice.ProcessResult{}, errors.New("unknown persistence outcome")
	})
	if err := testWorker(t, q, p).runOne(context.Background()); err == nil || q.retryCalls != 0 || q.failureCalls != 0 {
		t.Fatalf("unknown processing errors must wait for lease recovery: %v", err)
	}
}

func TestRunCancellationNeverFinalizesCanceledAttempt(t *testing.T) {
	q := &fakeQueue{job: validClaim(1)}
	started := make(chan struct{})

	p := fakeProcessor(func(ctx context.Context, _ domain.UpdateJob, _ quoteservice.RateProvider) (quoteservice.ProcessResult, error) {
		close(started)
		<-ctx.Done()

		return quoteservice.ProcessResult{}, ctx.Err()
	})

	w := testWorker(t, q, p)
	w.pollInterval = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- w.Run(ctx) }()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not process claimed job")
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker ignored cancellation")
	}

	if q.retryCalls != 0 || q.failureCalls != 0 || q.claimCalls != 1 {
		t.Fatalf("canceled attempt must be recovered by lease expiry: %+v", q)
	}
}

func TestRunStartsImmediatelyAndDoesNotReclaim(t *testing.T) {
	q := &fakeQueue{job: validClaim(1)}

	processed := make(chan struct{})

	p := fakeProcessor(func(context.Context, domain.UpdateJob, quoteservice.RateProvider) (quoteservice.ProcessResult, error) {
		close(processed)

		return quoteservice.ProcessResult{}, nil
	})

	w := testWorker(t, q, p)

	w.pollInterval = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- w.Run(ctx) }()

	select {
	case <-processed:
	case <-time.After(time.Second):
		t.Fatal("work did not start on initial cycle")
	}

	cancel()

	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
