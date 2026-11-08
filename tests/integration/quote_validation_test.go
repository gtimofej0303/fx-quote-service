//go:build integration

package integration_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
)

func requireCheckConstraint(t *testing.T, err error, name string) {
	t.Helper()

	var pgErr *pgconn.PgError

	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != name {
		t.Fatalf("expected CHECK %s, got %v", name, err)
	}
}

func TestFailedJobRequiresErrorCode(t *testing.T) {
	db, repo, ctx := openTestDatabase(t)

	_, err := db.ExecContext(ctx, `
		INSERT INTO quote_updates(pair,status,attempts,completed_at,error_message)
		VALUES ('USD/EUR','failed',1,statement_timestamp(),'Failure without code')`)

	requireCheckConstraint(t, err, "quote_updates_failed_code_required")

	created := create(t, ctx, repo, quote.PairUSDEUR)
	claimed := claim(t, ctx, repo, 3)

	if err := repo.SaveFailure(ctx, created.ID, claimed.Lease.Token,
		quote.UpdateFailure{Code: quote.FailureProviderUnavailable, Message: "Unavailable"}); err != nil {
		t.Fatal(err)
	}

	_, err = db.ExecContext(ctx, `UPDATE quote_updates SET error_code=NULL WHERE id=$1`, created.ID)
	requireCheckConstraint(t, err, "quote_updates_failed_code_required")
	job, err := repo.GetJob(ctx, created.ID)
	if err != nil || job.Failure == nil || job.Failure.Code != quote.FailureProviderUnavailable {
		t.Fatalf("rejected UPDATE must preserve the valid failure: %+v / %v", job, err)
	}
}

func TestDatabaseSourceDateMatchesDomainTolerance(t *testing.T) {
	db, repo, ctx := openTestDatabase(t)
	cases := []struct {
		name, retrieved, source string
		valid                   bool
	}{
		{"historical", "2000-01-03T12:00:00Z", "1999-12-31", true},
		{"same day", "2026-11-08T00:00:00Z", "2026-11-08", true},
		{"next day", "2026-11-08T00:00:00Z", "2026-11-09", true},
		{"two days ahead", "2026-11-08T23:59:59Z", "2026-11-10", false},
		{"far future", "2026-11-08T00:00:00Z", "2099-01-01", false},
		{"year boundary", "2026-12-31T23:59:59Z", "2027-01-01", true},
		{"leap day", "2024-02-28T00:00:00Z", "2024-02-29", true},
		{"beyond leap day", "2024-02-28T23:59:59Z", "2024-03-01", false},
		{"UTC allowed", "2026-11-09T00:30:00+14:00", "2026-11-09", true},
		{"UTC rejected", "2026-11-09T00:30:00+14:00", "2026-11-10", false},
		{"maximum source year", "9999-12-31T23:59:59Z", "9999-12-31", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			job := create(t, ctx, repo, quote.PairUSDEUR)
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			// Session time zone must not affect the UTC date boundary.
			if _, err := tx.ExecContext(ctx, `SET LOCAL TIME ZONE 'Pacific/Honolulu'`); err != nil {
				t.Fatal(err)
			}
			_, err = tx.ExecContext(ctx, `
				INSERT INTO quotes(update_id,pair,price,source_date,updated_at)
				VALUES ($1,'USD/EUR',0.9,$2::date,$3::timestamptz)`, job.ID, tc.source, tc.retrieved)
			if !tc.valid {
				requireCheckConstraint(t, err, "quotes_source_date_retrieval_check")
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE quote_updates SET status='succeeded',attempts=1,completed_at=statement_timestamp()
				WHERE id=$1`, job.ID); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			q, err := repo.GetQuoteByUpdateID(ctx, job.ID)
			if err != nil || q.SourceDate.String() != tc.source {
				t.Fatalf("SQL and domain validation disagree: %+v / %v", q, err)
			}
		})
	}
}
