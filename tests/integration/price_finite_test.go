//go:build integration

package integration_test

import (
	"testing"
	"time"

	"github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
)

// TestDatabaseRejectsNaNPrice checks that the database rejects a NaN price.
func TestDatabaseRejectsNaNPrice(t *testing.T) {
	db, repo, ctx := openTestDatabase(t)
	job := create(t, ctx, repo, quote.PairUSDEUR)
	_, err := db.ExecContext(ctx, `
		INSERT INTO quotes(update_id,pair,price,source_date,updated_at)
		VALUES ($1,'USD/EUR','NaN'::numeric,CURRENT_DATE,statement_timestamp())`, job.ID)
	requireCheckConstraint(t, err, "quotes_price_not_nan")

	claimed := claim(t, ctx, repo, 3)
	now := time.Now().UTC()
	q := makeQuote(t, job, "0.123456789012345678", now.Format("2006-01-02"), now)
	if err := repo.SaveSuccess(ctx, q, claimed.Lease.Token); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ price, constraint string }{
		{"NaN", "quotes_price_not_nan"},
		{"0", "quotes_price_positive_check"},
		{"-0.1", "quotes_price_positive_check"},
	} {
		_, err := db.ExecContext(ctx, `UPDATE quotes SET price=$2::numeric WHERE update_id=$1`, job.ID, tc.price)
		requireCheckConstraint(t, err, tc.constraint)
	}
	stored, err := repo.GetQuoteByUpdateID(ctx, job.ID)
	if err != nil || stored.Price.String() != q.Price.String() {
		t.Fatalf("valid exact price changed after rejected updates: %+v / %v", stored, err)
	}
	latest, err := repo.GetLatestQuote(ctx, job.Pair)
	if err != nil || latest.UpdateID != job.ID || latest.Price.String() != q.Price.String() {
		t.Fatalf("latest stopped working: %+v / %v", latest, err)
	}
}
