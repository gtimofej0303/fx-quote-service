//go:build integration

package integration_test

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
	quoteservice "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
	"github.com/gtimofej0303/fx-quote-service/internal/worker"
)

type staticRateProvider struct {
	calls atomic.Int32
}

func (p *staticRateProvider) FetchRate(_ context.Context, pair domain.CurrencyPair) (quoteservice.FetchedRate, error) {
	p.calls.Add(1)

	price, err := domain.ParsePrice("50.123456789012345678")
	if err != nil {
		return quoteservice.FetchedRate{}, err
	}

	date, err := domain.ParseSourceDate("2026-11-06")
	if err != nil {
		return quoteservice.FetchedRate{}, err
	}

	return quoteservice.FetchedRate{
		Price: price, SourceDate: date, UpdatedAt: time.Now().UTC(),
	}, nil
}

func TestWorkerPersistsQuoteAndRecoversExpiredClaim(t *testing.T) {
	db, repo, ctx := openTestDatabase(t)

	service, err := quoteservice.New(repo)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a prior worker that crashed after its first claim.
	job, err := service.RequestUpdate(ctx, "USD/EGP", nil)
	if err != nil {
		t.Fatal(err)
	}

	oldClaim, err := repo.ClaimNext(ctx, 30*time.Second, 3)
	if err != nil || oldClaim.ID != job.ID {
		t.Fatalf("initial claim: %+v, %v", oldClaim, err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE quote_updates SET locked_until=statement_timestamp() - interval '1 second' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}

	provider := &staticRateProvider{}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	bg, err := worker.New(repo, service, provider, logger)
	if err != nil {
		t.Fatal(err)
	}

	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)

	go func() { done <- bg.Run(workerCtx) }()

	defer func() {
		cancel()

		if err := <-done; err != nil {
			t.Errorf("stop worker: %v", err)
		}
	}()

	deadline := time.NewTimer(6 * time.Second)
	defer deadline.Stop()

	poll := time.NewTicker(25 * time.Millisecond)
	defer poll.Stop()

	for {
		update, err := service.GetUpdate(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}

		if update.Job.Status == domain.JobSucceeded {
			if update.Quote == nil || update.Quote.Price.String() != "50.123456789012345678" ||
				update.Quote.SourceDate.String() != "2026-11-06" || update.Job.Attempts != 2 || provider.calls.Load() != 1 {
				t.Fatalf("invalid persisted result: %+v calls=%d", update, provider.calls.Load())
			}

			latest, err := service.GetLatestQuote(ctx, "USD/EGP")
			if err != nil || latest.UpdateID != job.ID {
				t.Fatalf("latest quote: %+v, %v", latest, err)
			}

			return
		}

		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatalf("worker did not complete job: %+v", update.Job)
		}
	}
}
