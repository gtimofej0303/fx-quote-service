package quote

import (
	"errors"
	"testing"
	"time"
)

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

func mustDate(t *testing.T, s string) SourceDate {
	t.Helper()

	v, err := ParseSourceDate(s)
	if err != nil {
		t.Fatal(err)
	}

	return v
}

func mustPrice(t *testing.T, s string) Price {
	t.Helper()

	v, err := ParsePrice(s)

	if err != nil {
		t.Fatal(err)
	}

	return v
}