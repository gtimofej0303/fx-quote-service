//go:build integration

package integration_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
	"github.com/gtimofej0303/fx-quote-service/internal/repository/postgres"
)

// create creates a job in the test database.
func create(t *testing.T, ctx context.Context, repo *postgres.Repository, pair quote.CurrencyPair) quote.UpdateJob {
	t.Helper()

	job, err := repo.CreateJob(ctx, pair, nil)
	if err != nil {
		t.Fatal(err)
	}

	return job
}

// claim takes a ready job from the test database.
func claim(t *testing.T, ctx context.Context, repo *postgres.Repository, max int) quote.UpdateJob {
	t.Helper()

	job, err := repo.ClaimNext(ctx, 30*time.Second, max)
	if err != nil {
		t.Fatal(err)
	}

	if job.Status != quote.JobProcessing || job.Lease == nil {
		t.Fatalf("invalid claimed job: %+v", job)
	}

	return job
}

// makeQuote creates a valid quote for the test job.
func makeQuote(t *testing.T, job quote.UpdateJob, price, date string, fetched time.Time) quote.Quote {
	t.Helper()

	p, err := quote.ParsePrice(price)
	if err != nil {
		t.Fatal(err)
	}

	d, err := quote.ParseSourceDate(date)
	if err != nil {
		t.Fatal(err)
	}

	q, err := quote.NewQuote(job.ID, job.Pair, p, d, fetched)
	if err != nil {
		t.Fatal(err)
	}

	return q
}

// TestCreateJobIdempotencyConcurrent checks that requests with the same key create one job at the same time.
func TestCreateJobIdempotencyConcurrent(t *testing.T) {
	_, repo, ctx := openTestDatabase(t)
	key := "operation-42"

	const count = 24

	ids := make(chan int64, count)
	errorsSeen := make(chan error, count)
	var wg sync.WaitGroup

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			job, err := repo.CreateJob(ctx, quote.PairEUREGP, &key)
			if err != nil {
				errorsSeen <- err
				return
			}

			ids <- job.ID
		}()
	}

	wg.Wait()
	close(ids)
	close(errorsSeen)

	for err := range errorsSeen {
		t.Errorf("concurrent create: %v", err)
	}

	var first int64

	for id := range ids {
		if first == 0 {
			first = id
		}

		mustEqual(t, id, first, "idempotent ID")
	}

	if first <= 0 {
		t.Fatal("no job was created")
	}

	if _, err := repo.CreateJob(ctx, quote.PairUSDEUR, &key); !errors.Is(err, postgres.ErrIdempotencyConflict) {
		t.Fatalf("same key, different pair: %v", err)
	}

	a := create(t, ctx, repo, quote.PairEUREGP)
	b := create(t, ctx, repo, quote.PairEUREGP)

	if a.ID == b.ID || a.ID == first || b.ID == first {
		t.Fatal("unkeyed requests must create distinct jobs")
	}

	if _, err := repo.CreateJob(ctx, "EUR/MXN", nil); !errors.Is(err, quote.ErrUnsupportedPair) {
		t.Fatalf("unsupported pair: %v", err)
	}

	bad := "has whitespace"

	if _, err := repo.CreateJob(ctx, quote.PairEUREGP, &bad); !errors.Is(err, quote.ErrInvalidIdempotencyKey) {
		t.Fatalf("bad idempotency key: %v", err)
	}
}

// TestClaimRetryLeaseRotationAndFailure checks job leases, retry times, and saved failures.
func TestClaimRetryLeaseRotationAndFailure(t *testing.T) {
	db, repo, ctx := openTestDatabase(t)
	one := create(t, ctx, repo, quote.PairUSDEGP)
	two := create(t, ctx, repo, quote.PairEURUSD)

	var wg sync.WaitGroup
	results := make(chan quote.UpdateJob, 2)
	errCh := make(chan error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			job, err := repo.ClaimNext(ctx, 30*time.Second, 3)
			if err != nil {
				errCh <- err
				return
			}

			results <- job
		}()
	}

	wg.Wait()
	close(results)
	close(errCh)

	for err := range errCh {
		t.Fatal(err)
	}

	claimed := map[int64]quote.UpdateJob{}

	for j := range results {
		claimed[j.ID] = j
	}

	if len(claimed) != 2 || claimed[one.ID].Lease == nil || claimed[two.ID].Lease == nil {
		t.Fatalf("expected two distinct concurrent claims: %+v", claimed)
	}

	if _, err := repo.ClaimNext(ctx, 30*time.Second, 3); !errors.Is(err, postgres.ErrNoClaimableJob) {
		t.Fatalf("already claimed jobs should be unavailable: %v", err)
	}

	old := claimed[one.ID]
	next := time.Now().UTC().Add(time.Minute)

	if err := repo.ScheduleRetry(ctx, one.ID, old.Lease.Token, next); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.ClaimNext(ctx, 30*time.Second, 3); !errors.Is(err, postgres.ErrNoClaimableJob) {
		t.Fatalf("future retry must not be claimed: %v", err)
	}

	// Change the job time in the test database to avoid a minute of waiting.
	if _, err := db.ExecContext(ctx, `UPDATE quote_updates SET next_attempt_at = statement_timestamp() - interval '1 second' WHERE id=$1`, one.ID); err != nil {
		t.Fatal(err)
	}

	again := claim(t, ctx, repo, 3)
	mustEqual(t, again.ID, one.ID, "retried job ID")
	mustEqual(t, again.Attempts, 2, "claim increments attempts")

	if again.Lease.Token == old.Lease.Token {
		t.Fatal("reclaim must rotate UUID token")
	}

	if _, err := repo.RenewLease(ctx, again.ID, old.Lease.Token, 30*time.Second); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("old token must not renew lease: %v", err)
	}

	failure := quote.UpdateFailure{Code: quote.FailureProviderTimeout, Message: "provider timed out"}

	if err := repo.SaveFailure(ctx, again.ID, old.Lease.Token, failure); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("old token must not finalize job: %v", err)
	}

	until, err := repo.RenewLease(ctx, again.ID, again.Lease.Token, 30*time.Second)
	if err != nil || !until.After(time.Now().Add(10*time.Second)) {
		t.Fatalf("renew lease: %v / %v", until, err)
	}

	if err := repo.SaveFailure(ctx, again.ID, again.Lease.Token, failure); err != nil {
		t.Fatal(err)
	}

	failed, err := repo.GetJob(ctx, one.ID)
	if err != nil {
		t.Fatal(err)
	}

	if failed.Status != quote.JobFailed || failed.CompletedAt == nil || failed.Lease != nil || failed.Failure == nil || failed.Failure.Code != failure.Code {
		t.Fatalf("incorrect terminal failure: %+v", failed)
	}

	if _, err := repo.GetQuoteByUpdateID(ctx, one.ID); !errors.Is(err, postgres.ErrQuoteNotFound) {
		t.Fatalf("failed jobs have no quote: %v", err)
	}
}

// TestSaveSuccessAtomicAndLatest checks that the quote and success status save together and the latest quote is correct.
func TestSaveSuccessAtomicAndLatest(t *testing.T) {
	db, repo, ctx := openTestDatabase(t)

	if _, err := repo.GetLatestQuote(ctx, quote.PairEUREGP); !errors.Is(err, postgres.ErrQuoteNotFound) {
		t.Fatalf("no initial latest quote: %v", err)
	}

	base := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

	data := []struct {
		pair  quote.CurrencyPair
		price string
		date  string
		at    time.Time
	}{
		{quote.PairEUREGP, "61.960000000000000000", "2026-11-02", base.Add(3 * time.Hour)},
		{quote.PairEUREGP, "60.000000000000000001", "2026-11-03", base},
		{quote.PairEUREGP, "62.20", "2026-11-03", base.Add(time.Hour)},
		{quote.PairEUREGP, "63.30", "2026-11-03", base.Add(time.Hour)},
		{quote.PairUSDEGP, "51.1", "2026-11-03", base.Add(2 * time.Hour)},
	}

	var winning int64

	for idx, item := range data {
		job := create(t, ctx, repo, item.pair)

		processing := claim(t, ctx, repo, 5)
		mustEqual(t, processing.ID, job.ID, "claimed in creation order")

		q := makeQuote(t, job, item.price, item.date, item.at)

		if err := repo.SaveSuccess(ctx, q, processing.Lease.Token); err != nil {
			t.Fatalf("save success %d: %v", idx, err)
		}

		persisted, err := repo.GetQuoteByUpdateID(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}

		mustEqual(t, persisted.Price.String(), q.Price.String(), "exact decimal roundtrip")

		if idx == 3 {
			winning = job.ID
		}
	}

	latest, err := repo.GetLatestQuote(ctx, quote.PairEUREGP)
	if err != nil {
		t.Fatal(err)
	}

	mustEqual(t, latest.UpdateID, winning, "source_date / updated_at / ID ordering")
	mustEqual(t, latest.Price.String(), "63.3", "canonical decimal")

	usdLatest, err := repo.GetLatestQuote(ctx, quote.PairUSDEGP)
	if err != nil {
		t.Fatal(err)
	}

	mustEqual(t, usdLatest.Price.String(), "51.1", "pair isolation")

	// DELETE must not leave a successful job without a quote.
	if _, err := db.ExecContext(ctx, `DELETE FROM quotes WHERE update_id=$1`, winning); err == nil {
		t.Fatal("deferred integrity trigger should reject deleting successful quote")
	}

	if _, err := repo.GetQuoteByUpdateID(ctx, winning); err != nil {
		t.Fatalf("quote must still exist after rejected DELETE: %v", err)
	}
}

// TestStaleAndWrongPairNeverPersistQuote checks that old lease tokens and wrong currency pairs cannot save quotes.
func TestStaleAndWrongPairNeverPersistQuote(t *testing.T) {
	db, repo, ctx := openTestDatabase(t)

	job := create(t, ctx, repo, quote.PairEUREGP)
	processing := claim(t, ctx, repo, 2)

	q := makeQuote(t, job, "61.96", "2026-11-03", time.Now().UTC())

	if err := repo.SaveSuccess(ctx, q, "not-the-token"); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("wrong token: %v", err)
	}

	wrong := q
	wrong.Pair = quote.PairUSDEGP

	if err := repo.SaveSuccess(ctx, wrong, processing.Lease.Token); !errors.Is(err, postgres.ErrPairMismatch) {
		t.Fatalf("pair mismatch: %v", err)
	}

	if _, err := repo.GetQuoteByUpdateID(ctx, job.ID); !errors.Is(err, postgres.ErrQuoteNotFound) {
		t.Fatalf("rollback should prevent quote insert: %v", err)
	}

	// SQL must also reject a successful job without a quote.
	if _, err := db.ExecContext(ctx, `
		UPDATE quote_updates SET status='succeeded', attempts=1,
		    completed_at=statement_timestamp(), lock_token=NULL, locked_until=NULL
		WHERE id=$1`, job.ID); err == nil {
		t.Fatal("deferred guard should reject succeeded job without quote")
	}

	if err := repo.SaveSuccess(ctx, q, processing.Lease.Token); err != nil {
		t.Fatal(err)
	}

	if err := repo.SaveSuccess(ctx, q, processing.Lease.Token); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("success must be final: %v", err)
	}
}

// TestExpiredProcessingJobGetsNewToken checks that taking a job with an expired lease gives it a new token.
func TestExpiredProcessingJobGetsNewToken(t *testing.T) {
	db, repo, ctx := openTestDatabase(t)

	job := create(t, ctx, repo, quote.PairEGPUSD)
	first := claim(t, ctx, repo, 3)

	if _, err := db.ExecContext(ctx, `
		UPDATE quote_updates SET locked_until=statement_timestamp() - interval '1 second'
		WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}

	second := claim(t, ctx, repo, 3)
	mustEqual(t, second.ID, job.ID, "reclaimed ID")
	mustEqual(t, second.Attempts, 2, "increment attempts after crash")

	if first.Lease.Token == second.Lease.Token {
		t.Fatal("expired job claim must receive fresh fencing token")
	}

	q := makeQuote(t, job, "0.02", "2026-11-03", time.Now().UTC())

	if err := repo.SaveSuccess(ctx, q, first.Lease.Token); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("stale worker write should fail: %v", err)
	}

	if err := repo.SaveSuccess(ctx, q, second.Lease.Token); err != nil {
		t.Fatal(err)
	}
}

// TestExpiredLeaseMaxAttemptsAndPersistence checks that an expired job with no tries left is saved as failed.
func TestExpiredLeaseMaxAttemptsAndPersistence(t *testing.T) {
	db, repo, ctx := openTestDatabase(t)

	job := create(t, ctx, repo, quote.PairEGPEUR)
	processing := claim(t, ctx, repo, 1)

	// Save an expired lease to test a worker crash on its last try.
	if _, err := db.ExecContext(ctx, `UPDATE quote_updates SET locked_until=statement_timestamp() - interval '1 second' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.ClaimNext(ctx, 30*time.Second, 1); !errors.Is(err, postgres.ErrNoClaimableJob) {
		t.Fatalf("max attempts must forbid another claim: %v", err)
	}

	q := makeQuote(t, job, "0.02", "2026-11-03", time.Now())
	if err := repo.SaveSuccess(ctx, q, processing.Lease.Token); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("expired token must never save a quote: %v", err)
	}

	count, err := repo.FailExhausted(ctx, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, count, int64(1), "recovered failed count")

	count, err = repo.FailExhausted(ctx, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, count, int64(0), "idempotent recovery")

	// Another connection must see the saved final status.
	secondDB, err := postgres.Open(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer secondDB.Close()

	persisted, err := postgres.New(secondDB).GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}

	if persisted.Status != quote.JobFailed || persisted.Failure == nil || persisted.Failure.Code != quote.FailureProcessingTimeout {
		t.Fatalf("recovered job did not persist: %+v", persisted)
	}

	if _, err := repo.GetJob(ctx, 987654321); !errors.Is(err, postgres.ErrJobNotFound) {
		t.Fatalf("unknown job: %v", err)
	}
}

// TestDatabaseIntegrityChecks checks that database rules reject invalid job and quote data.
func TestDatabaseIntegrityChecks(t *testing.T) {
	db, repo, ctx := openTestDatabase(t)

	for _, sqlText := range []string{
		`INSERT INTO quote_updates(pair) VALUES ('EUR/MXN')`,
		`INSERT INTO quote_updates(pair, status) VALUES ('EUR/EGP', 'waiting')`,
		`INSERT INTO quote_updates(pair, idempotency_key) VALUES ('EUR/EGP', 'bad key')`,
		`INSERT INTO quote_updates(pair, attempts) VALUES ('EUR/EGP', -1)`,
	} {
		if _, err := db.ExecContext(ctx, sqlText); err == nil {
			t.Errorf("constraint should reject: %s", sqlText)
		}
	}

	job := create(t, ctx, repo, quote.PairEUREGP)

	for _, tc := range []struct{ pair, price string }{
		{"EUR/EGP", "0"},
		{"USD/EGP", "61.96"},
	} {
		_, err := db.ExecContext(ctx, `
			INSERT INTO quotes(update_id,pair,price,source_date,updated_at)
			VALUES ($1, $2, $3::numeric, DATE '2026-11-03', statement_timestamp())`,
			job.ID, tc.pair, tc.price)
		if err == nil {
			t.Errorf("quote constraint should reject pair=%s price=%s", tc.pair, tc.price)
		}
	}

	if err := repo.ScheduleRetry(ctx, job.ID, "missing", time.Now()); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("pending job cannot retry: %v", err)
	}

	if _, err := repo.ClaimNext(ctx, 1500*time.Millisecond, 2); !errors.Is(err, postgres.ErrInvalidRepositoryArgument) {
		t.Fatalf("fractional lease is rejected: %v", err)
	}

	if n, err := repo.FailExhausted(ctx, 0, 20); !errors.Is(err, postgres.ErrInvalidRepositoryArgument) || n != 0 {
		t.Fatalf("invalid max attempts: %d, %v", n, err)
	}
}
