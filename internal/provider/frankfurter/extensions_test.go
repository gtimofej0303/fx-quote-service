package frankfurter

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
)

// TestFetchRateAcceptsAdditionalFields checks that extra JSON fields are allowed.
func TestFetchRateAcceptsAdditionalFields(t *testing.T) {
	for _, extra := range []string{`null`, `true`, `123`, `1e400`, `"text"`, `[]`, `[1,{"x":true}]`, `{"provider":"test","nested":{"x":1}}`} {
		t.Run(extra, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"metadata":%s,"rate":0.123456789012345678,"quote":"EUR","date":"2026-11-06","base":"USD"}`, extra)
			})
			got, err := client.FetchRate(context.Background(), domain.PairUSDEUR)
			if err != nil || got.Price.String() != "0.123456789012345678" || got.SourceDate.String() != "2026-11-06" {
				t.Fatalf("additional field changed the quote: %+v / %v", got, err)
			}
		})
	}
}

// TestAdditionalFieldsDoNotHideInvalidRequiredFields checks that extra fields do not hide errors in required fields.
func TestAdditionalFieldsDoNotHideInvalidRequiredFields(t *testing.T) {
	cases := []string{
		`{"metadata":{},"base":"USD","quote":"EUR","rate":1}`,
		`{"metadata":{},"date":"2026-11-06","quote":"EUR","rate":1}`,
		`{"metadata":{},"date":"2026-11-06","base":"USD","rate":1}`,
		`{"metadata":{},"date":"2026-11-06","base":"USD","quote":"EUR"}`,
		`{"metadata":{},"date":"2026-11-06","base":"USD","quote":"EUR","rate":null}`,
		`{"metadata":{},"date":"2026-11-06","base":"USD","quote":"EUR","rate":1,"\u0072ate":2}`,
		`{"metadata":{},"date":"2026-11-06","base":"USD","quote":"EUR","rate":1,"metadata":null}`,
		`{"metadata":{"broken":},"date":"2026-11-06","base":"USD","quote":"EUR","rate":1}`,
		`{"metadata":{},"date":"2026-11-06","base":"EUR","quote":"USD","rate":1}`,
		`{"metadata":{},"date":"2026-02-30","base":"USD","quote":"EUR","rate":1}`,
		`{"metadata":{},"date":"2026-11-06","base":"USD","quote":"EUR","rate":0}`,
		`{"metadata":{},"date":"2026-11-06","base":"USD","quote":"EUR","rate":1} {}`,
	}
	for _, body := range cases {
		if _, _, err := parseRateResponse([]byte(body), "USD", "EUR"); err == nil {
			t.Fatalf("invalid required field or JSON was accepted: %s", body)
		}
	}
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"date":"2026-11-06","base":"USD","quote":"EUR","rate":1,"metadata":%q}`, strings.Repeat("x", maxResponseBytes))
	})
	_, err := client.FetchRate(context.Background(), domain.PairUSDEUR)
	requireProviderCode(t, err, domain.FailureProviderInvalidResponse)
}
