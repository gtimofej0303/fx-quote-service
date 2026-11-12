package worker

import (
	"context"
	"testing"
	"time"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
	quoteservice "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
)

type serviceRepositoryStub struct {
	savedQuote domain.Quote
	savedToken string
	saveCalls  int
}

// CreateJob panics if this unused test method is called.
func (*serviceRepositoryStub) CreateJob(context.Context, domain.CurrencyPair, *string) (domain.UpdateJob, error) {
	panic("not used by ProcessJob")
}

// GetJob panics if this unused test method is called.
func (*serviceRepositoryStub) GetJob(context.Context, int64) (domain.UpdateJob, error) {
	panic("not used by ProcessJob")
}

// GetQuoteByUpdateID panics if this unused test method is called.
func (*serviceRepositoryStub) GetQuoteByUpdateID(context.Context, int64) (domain.Quote, error) {
	panic("not used by ProcessJob")
}

// GetLatestQuote panics if this unused test method is called.
func (*serviceRepositoryStub) GetLatestQuote(context.Context, domain.CurrencyPair) (domain.Quote, error) {
	panic("not used by ProcessJob")
}

// SaveSuccess records the quote, lease token, and number of saves.
func (r *serviceRepositoryStub) SaveSuccess(_ context.Context, q domain.Quote, token string) error {
	r.savedQuote, r.savedToken = q, token

	r.saveCalls++

	return nil
}

// TestRunOneCallsRealServiceAndProvider checks that the worker uses the real service to get and save a rate.
func TestRunOneCallsRealServiceAndProvider(t *testing.T) {
	repo := &serviceRepositoryStub{}

	service, err := quoteservice.New(repo)
	if err != nil {
		t.Fatal(err)
	}

	q := &fakeQueue{job: validClaim(1)}

	var providerCalls int

	provider := fakeProvider(func(_ context.Context, pair domain.CurrencyPair) (quoteservice.FetchedRate, error) {
		providerCalls++

		if pair != domain.PairUSDEGP {
			t.Fatalf("wrong pair: %s", pair)
		}

		price, err := domain.ParsePrice("50.123456789012345678")
		if err != nil {
			t.Fatal(err)
		}

		date, err := domain.ParseSourceDate("2026-11-06")
		if err != nil {
			t.Fatal(err)
		}

		return quoteservice.FetchedRate{Price: price, SourceDate: date, UpdatedAt: nowFixed.Add(time.Second)}, nil
	})

	w := testWorker(t, q, fakeProcessor(func(context.Context, domain.UpdateJob, quoteservice.RateProvider) (quoteservice.ProcessResult, error) {
		t.Fatal("dummy processor must be replaced")

		return quoteservice.ProcessResult{}, nil
	}))

	w.processor, w.provider = service, provider

	if err := w.runOne(context.Background()); err != nil {
		t.Fatal(err)
	}

	if providerCalls != 1 || repo.saveCalls != 1 || repo.savedToken != "test-lease" ||
		repo.savedQuote.UpdateID != 41 || repo.savedQuote.Pair != domain.PairUSDEGP ||
		repo.savedQuote.Price.String() != "50.123456789012345678" ||
		repo.savedQuote.SourceDate.String() != "2026-11-06" {
		t.Fatalf("wrong integration: provider=%d repo=%+v", providerCalls, repo)
	}

	if q.retryCalls != 0 || q.failureCalls != 0 {
		t.Fatalf("unexpected failure writes: %+v", q)
	}
}
