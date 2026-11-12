package quote

import (
	"errors"
	"testing"
	"time"
)

// TestParseSourceDate checks valid and invalid source dates.
func TestParseSourceDate(t *testing.T) {
	for _, date := range []string{"2024-02-29", "2026-11-03", "0001-01-01"} {
		parsed, err := ParseSourceDate(date)
		if err != nil || parsed.String() != date || parsed.Validate() != nil {
			t.Errorf("date %q: %v", date, err)
		}
	}

	for _, date := range []string{"", "2026-02-29", "2026-13-01", "2026-11-31", "2026-1-01", "0000-01-01", "2026-11-03T00:00:00Z"} {
		if _, err := ParseSourceDate(date); !errors.Is(err, ErrInvalidSourceDate) {
			t.Errorf("date %q: err %v", date, err)
		}
	}
}

// TestQuoteValidationAndUTC checks quote fields and UTC time conversion.
func TestQuoteValidationAndUTC(t *testing.T) {
	price := mustPrice(t, "61.96")
	date := mustDate(t, "2026-11-03")
	clock := time.Date(2026, 11, 4, 12, 30, 0, 0, time.FixedZone("EET", 2*3600))

	q, err := NewQuote(7, PairEUREGP, price, date, clock)
	if err != nil || q.Validate() != nil || q.UpdatedAt.Format(time.RFC3339) != "2026-11-04T10:30:00Z" {
		t.Fatalf("quote = %+v, err = %v", q, err)
	}

	cases := []struct {
		name string
		edit func(*Quote)
	}{
		{"nonpositive ID", func(q *Quote) { q.UpdateID = 0 }},
		{"invalid pair", func(q *Quote) { q.Pair = "EUR/MXN" }},
		{"zero price", func(q *Quote) { q.Price = Price{} }},
		{"zero date", func(q *Quote) { q.SourceDate = "" }},
		{"missing retrieval time", func(q *Quote) { q.UpdatedAt = time.Time{} }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := q
			tc.edit(&bad)
			if err := bad.Validate(); !errors.Is(err, ErrInvalidQuote) {
				t.Fatalf("Validate() = %v", err)
			}
		})
	}
}

// TestQuoteRecencyOrdering checks which quote is newer when dates, times, or IDs differ.
func TestQuoteRecencyOrdering(t *testing.T) {
	price := mustPrice(t, "1.23")
	old := Quote{UpdateID: 1, Pair: PairUSDEUR, Price: price, SourceDate: mustDate(t, "2026-11-02"), UpdatedAt: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)}

	newDate := old
	newDate.SourceDate = mustDate(t, "2026-11-03")
	newDate.UpdatedAt = old.UpdatedAt.Add(-24 * time.Hour)
	if !newDate.NewerThan(old) || old.NewerThan(newDate) {
		t.Fatal("source_date must win over retrieval time")
	}

	newTime := old
	newTime.UpdatedAt = old.UpdatedAt.Add(time.Second)
	if !newTime.NewerThan(old) || old.NewerThan(newTime) {
		t.Fatal("later retrieval time should win for same source date")
	}

	otherPair := old
	otherPair.Pair = PairEUREGP
	if old.NewerThan(otherPair) || otherPair.NewerThan(old) {
		t.Fatal("different pairs must not be compared")
	}

	newID := old
	newID.UpdateID = 2
	if !newID.NewerThan(old) || old.NewerThan(newID) || old.NewerThan(old) {
		t.Fatal("update ID should break timestamp ties")
	}
}

// TestQuoteSourceDateRelativeToRetrieval checks the source date against the time the quote was received.
func TestQuoteSourceDateRelativeToRetrieval(t *testing.T) {
	cases := []struct {
		name, retrieved, source string
		valid                   bool
	}{
		{"historical", "2000-01-03T12:00:00Z", "1999-12-31", true},
		{"same day", "2026-11-08T00:00:00Z", "2026-11-08", true},
		{"one day ahead", "2026-11-08T00:00:00Z", "2026-11-09", true},
		{"two days ahead", "2026-11-08T23:59:59Z", "2026-11-10", false},
		{"far future", "2026-11-08T12:00:00Z", "2099-01-01", false},
		{"year boundary", "2026-12-31T23:59:59Z", "2027-01-01", true},
		{"beyond year boundary", "2026-12-31T23:59:59Z", "2027-01-02", false},
		{"leap day", "2024-02-28T00:00:00Z", "2024-02-29", true},
		{"beyond leap day", "2024-02-28T23:59:59Z", "2024-03-01", false},
		{"UTC boundary allowed", "2026-11-09T00:30:00+14:00", "2026-11-09", true},
		{"UTC boundary rejected", "2026-11-09T00:30:00+14:00", "2026-11-10", false},
		{"maximum source year", "9999-12-31T23:59:59Z", "9999-12-31", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			retrieved, err := time.Parse(time.RFC3339, tc.retrieved)
			if err != nil {
				t.Fatal(err)
			}

			q, err := NewQuote(1, PairUSDEUR, mustPrice(t, "0.9"), mustDate(t, tc.source), retrieved)
			if !tc.valid {
				if !errors.Is(err, ErrInvalidQuote) {
					t.Fatalf("expected invalid quote, got %+v / %v", q, err)
				}
				return
			}

			if err != nil || q.SourceDate.String() != tc.source || q.Validate() != nil {
				t.Fatalf("date must be preserved and remain valid: %+v / %v", q, err)
			}
		})
	}
}

// mustDate parses a source date and fails the test if it is invalid.
func mustDate(t *testing.T, s string) SourceDate {
	t.Helper()

	v, err := ParseSourceDate(s)
	if err != nil {
		t.Fatal(err)
	}

	return v
}

// mustPrice parses a price and fails the test if it is invalid.
func mustPrice(t *testing.T, s string) Price {
	t.Helper()

	v, err := ParsePrice(s)
	if err != nil {
		t.Fatal(err)
	}

	return v
}
