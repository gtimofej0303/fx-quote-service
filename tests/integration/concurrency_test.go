//go:build integration

package integration_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
	"github.com/gtimofej0303/fx-quote-service/internal/repository/postgres"
	quoteservice "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
	"github.com/gtimofej0303/fx-quote-service/internal/worker"
)

// slowProvider makes concurrent claims overlap, without using the internet.
type slowProvider struct {
	calls  atomic.Int32
	active atomic.Int32
	peak   atomic.Int32
}

func (p *slowProvider) FetchRate(ctx context.Context, _ domain.CurrencyPair) (quoteservice.FetchedRate, error) {
	p.calls.Add(1)
	active := p.active.Add(1)
	defer p.active.Add(-1)

	for {
		peak := p.peak.Load()

		if active <= peak || p.peak.CompareAndSwap(peak, active) {
			break
		}
	}

	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	
	select {
	case <-ctx.Done():
		return quoteservice.FetchedRate{}, ctx.Err()
	case <-timer.C:
	}

	price, err := domain.ParsePrice("50.123456789012345678")
	if err != nil {
		return quoteservice.FetchedRate{}, err
	}
	date, err := domain.ParseSourceDate("2026-11-06")
	if err != nil {
		return quoteservice.FetchedRate{}, err
	}

	return quoteservice.FetchedRate{Price: price, SourceDate: date, UpdatedAt: time.Now().UTC()}, nil
}

// Two independent pools approximate two application instances sharing one queue.
// In each pool two workers compete; no update may be processed twice.
func TestConcurrentWorkersAcrossTwoDatabasePools(t *testing.T) {
	dbA, repoA, parent := openTestDatabase(t)

	dbB, err := postgres.Open(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = dbB.Close() })
	
	repoB := postgres.New(dbB)

	serviceA, err := quoteservice.New(repoA)
	if err != nil {
		t.Fatal(err)
	}

	serviceB, err := quoteservice.New(repoB)
	if err != nil {
		t.Fatal(err)
	}

	const jobs = 20

	ids := make([]int64, 0, jobs)

	for i := 0; i < jobs; i++ {
		job, err := serviceA.RequestUpdate(parent, "USD/EGP", nil)
		
		if err != nil {
			t.Fatal(err)
		}

		ids = append(ids, job.ID)
	}

	provider := &slowProvider{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(parent)
	done := make(chan error, 4)

	for i := 0; i < 4; i++ {
		repo, svc := repoA, serviceA
		if i >= 2 {
			repo, svc = repoB, serviceB
		}

		w, err := worker.New(repo, svc, provider, logger)
		if err != nil {
			cancel()

			t.Fatal(err)
		}

		go func(w *worker.Worker) { done <- w.Run(ctx) }(w)
	}

	// Must run before either database pool's t.Cleanup.
	t.Cleanup(func() {
		cancel()

		for i := 0; i < 4; i++ {
			if err := <-done; err != nil {
				t.Errorf("worker stopped with error: %v", err)
			}
		}

	})

	deadline := time.NewTimer(25 * time.Second)
	defer deadline.Stop()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		var succeeded int

		err := dbA.QueryRowContext(parent, `SELECT count(*) FROM quote_updates WHERE status='succeeded'`).Scan(&succeeded)
		if err != nil {
			t.Fatal(err)
		}

		if succeeded == jobs {
			break
		}

		select {
		case <-deadline.C:
			t.Fatalf("only %d/%d jobs succeeded", succeeded, jobs)
		case <-ticker.C:
		}
	}

	// All successful writes have committed before we check the counters.
	if got := provider.calls.Load(); got != jobs {
		t.Fatalf("provider calls = %d; want %d (no duplicate processing)", got, jobs)
	}

	if peak := provider.peak.Load(); peak < 1 || peak > 4 {
		t.Fatalf("unexpected simultaneous provider calls: %d", peak)
	}

	var quotesCount, singleAttemptCount int

	if err := dbA.QueryRowContext(parent, `SELECT count(*) FROM quotes`).Scan(&quotesCount); err != nil {
		t.Fatal(err)
	}

	if err := dbA.QueryRowContext(parent, `SELECT count(*) FROM quote_updates WHERE attempts=1 AND status='succeeded'`).Scan(&singleAttemptCount); err != nil {
		t.Fatal(err)
	}

	if quotesCount != jobs || singleAttemptCount != jobs {
		t.Fatalf("persisted quotes=%d, one-attempt successes=%d, want %d", quotesCount, singleAttemptCount, jobs)
	}

	for _, id := range ids {
		got, err := serviceB.GetUpdate(parent, id)
		if err != nil {
			t.Fatal(err)
		}
		
		if got.Quote == nil || got.Quote.Price.String() != "50.123456789012345678" || got.Job.Attempts != 1 {
			t.Fatalf("incorrect saved result for %d: %+v", id, got)
		}
	}
}
