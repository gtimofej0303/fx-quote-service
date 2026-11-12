package frankfurter

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
	quoteservice "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
)

// TestParseRetryAfter checks retry dates, seconds, and invalid values.
func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 123, time.FixedZone("UTC+3", 3*3600))
	future := now.UTC().Truncate(time.Second).Add(time.Minute)

	cases := []struct {
		raw  string
		want time.Time
	}{
		{"60", now.UTC().Add(time.Minute)},
		{"  60\t", now.UTC().Add(time.Minute)},
		{"0", now.UTC()},
		{"00060", now.UTC().Add(time.Minute)},
		{future.Format(http.TimeFormat), future},
		{future.Format(time.RFC850), future},
		{future.Format(time.ANSIC), future},
		{"4294967295", now.UTC().Add(time.Duration(4294967295) * time.Second)},
		{"", time.Time{}},
		{"-1", time.Time{}},
		{"+1", time.Time{}},
		{"1.5", time.Time{}},
		{"60, 120", time.Time{}},
		{"not a date", time.Time{}},
		{"4294967296", time.Time{}},
		{"9999999999999999999999999999", time.Time{}},
		{now.UTC().Add(-time.Minute).Format(http.TimeFormat), time.Time{}},
	}

	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got := parseRetryAfter(tc.raw, now)
			if !got.Equal(tc.want) || (!got.IsZero() && got.Location() != time.UTC) {
				t.Fatalf("parseRetryAfter(%q)=%v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestFetchRateRetryAfter checks the retry time returned for provider errors.
func TestFetchRateRetryAfter(t *testing.T) {
	for _, status := range []int{429, 503, 500, 404} {
		for _, header := range []string{"60", testRetrievedAt.UTC().Add(time.Minute).Format(http.TimeFormat), "invalid"} {
			t.Run(http.StatusText(status)+"/"+header, func(t *testing.T) {
				client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Retry-After", header)
					w.WriteHeader(status)
				})

				_, err := client.FetchRate(context.Background(), domain.PairUSDEUR)

				var pe *quoteservice.ProviderError

				if !errors.As(err, &pe) {
					t.Fatalf("expected provider error: %v", err)
				}

				want := time.Time{}

				if status == 429 || status == 503 {
					want = parseRetryAfter(header, testRetrievedAt)
				}

				if !pe.RetryNotBefore.Equal(want) {
					t.Fatalf("status %d retry time=%v, want %v", status, pe.RetryNotBefore, want)
				}
			})
		}
	}
}
