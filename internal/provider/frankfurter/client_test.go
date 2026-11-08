package frankfurter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
	quoteservice "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
)

var testRetrievedAt = time.Date(2026, 11, 7, 18, 0, 0, 123, time.FixedZone("UTC+3", 3*3600))

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	c, err := NewWithBaseURL(server.URL, server.Client())
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	c.now = func() time.Time { return testRetrievedAt }

	return c
}

func requireProviderCode(t *testing.T, err error, want domain.FailureCode) {
	t.Helper()

	var pe *quoteservice.ProviderError

	if !errors.As(err, &pe) || pe == nil || pe.Code != want || pe.Message == "" {
		t.Fatalf("error=%v; expected ProviderError code %s", err, want)
	}
}

func TestFetchRateAllDirectedPairsOneRequestEach(t *testing.T) {
	cases := []struct {
		pair             domain.CurrencyPair
		path, rate, want string
	}{
		{domain.PairUSDEUR, "/v2/rate/usd/eur", "0.8759800", "0.87598"},
		{domain.PairEURUSD, "/v2/rate/eur/usd", "1.1416", "1.1416"},
		{domain.PairUSDEGP, "/v2/rate/usd/egp", "50.123456789012345678", "50.123456789012345678"},
		{domain.PairEGPUSD, "/v2/rate/egp/usd", "0.019951", "0.019951"},
		{domain.PairEUREGP, "/v2/rate/eur/egp", "61.96", "61.96"},
		{domain.PairEGPEUR, "/v2/rate/egp/eur", "0.016139", "0.016139"},
	}

	for _, tc := range cases {
		t.Run(tc.pair.String(), func(t *testing.T) {
			var calls atomic.Int32

			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)

				if r.Method != http.MethodGet || r.URL.RequestURI() != tc.path ||
					r.Header.Get("Accept") != "application/json" || r.Header.Get("User-Agent") == "" {
					t.Errorf("unexpected request: %s %s %#v", r.Method, r.URL.RequestURI(), r.Header)
				}

				if r.ContentLength > 0 {
					t.Error("unexpected request body")
				}

				parts := strings.Split(strings.TrimPrefix(tc.path, "/v2/rate/"), "/")

				_, _ = fmt.Fprintf(w, `{"date":"2026-11-06","base":%q,"quote":%q,"rate":%s}`, strings.ToUpper(parts[0]), strings.ToUpper(parts[1]), tc.rate)
			})

			result, err := c.FetchRate(context.Background(), tc.pair)
			if err != nil {
				t.Fatal(err)
			}

			if result.Price.String() != tc.want || result.SourceDate.String() != "2026-11-06" ||
				!result.UpdatedAt.Equal(testRetrievedAt) || result.UpdatedAt.Location() != time.UTC {
				t.Fatalf("wrong rate/date/time: %+v", result)
			}

			if calls.Load() != 1 {
				t.Fatalf("got %d HTTP requests instead of one", calls.Load())
			}
		})
	}
}

func TestFetchRateInvalidPairNeverCallsHTTP(t *testing.T) {
	var calls atomic.Int32

	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })

	for _, pair := range []domain.CurrencyPair{"", "usd/eur", "USD/MXN", "USD/USD", "USD-EGP"} {
		_, err := c.FetchRate(context.Background(), pair)
		if err == nil {
			t.Fatalf("pair %q was accepted", pair)
		}
	}

	if calls.Load() != 0 {
		t.Fatalf("called HTTP %d times", calls.Load())
	}
}

func TestFetchRateHTTPStatuses(t *testing.T) {
	cases := []struct {
		status int
		code   domain.FailureCode
	}{
		{400, domain.FailureProviderRejectedRequest},
		{401, domain.FailureProviderRejectedRequest},
		{403, domain.FailureProviderRejectedRequest},
		{404, domain.FailureProviderRateNotFound},
		{408, domain.FailureProviderTimeout},
		{409, domain.FailureProviderRejectedRequest},
		{422, domain.FailureProviderRejectedRequest},
		{429, domain.FailureProviderRateLimited},
		{500, domain.FailureProviderUnavailable},
		{502, domain.FailureProviderUnavailable},
		{503, domain.FailureProviderUnavailable},
		{504, domain.FailureProviderTimeout},
		{206, domain.FailureProviderInvalidResponse},
		{302, domain.FailureProviderInvalidResponse},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			calls := 0

			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++

				if tc.status == 302 {
					w.Header().Set("Location", "/somewhere")
				}

				w.WriteHeader(tc.status)

				_, _ = io.WriteString(w, "private upstream body, never display")
			})
			_, err := c.FetchRate(context.Background(), domain.PairUSDEGP)

			requireProviderCode(t, err, tc.code)

			if strings.Contains(err.Error(), "private upstream") || calls != 1 {
				t.Fatalf("leaked body or retried: err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestFetchRateRejectsInvalidJSONAndBigBody(t *testing.T) {
	valid := `{"date":"2026-11-06","base":"USD","quote":"EUR","rate":0.87598}`

	cases := []struct{ name, body string }{
		{"empty", ""},
		{"html", "<html>nope</html>"},
		{"array", `[1,2,3]`},
		{"wrong direction", `{"date":"2026-11-06","base":"EUR","quote":"USD","rate":0.87598}`},
		{"wrong quote", `{"date":"2026-11-06","base":"USD","quote":"EGP","rate":0.87598}`},
		{"null date", `{"date":null,"base":"USD","quote":"EUR","rate":0.87598}`},
		{"invalid date", `{"date":"2026-02-30","base":"USD","quote":"EUR","rate":0.87598}`},
		{"missing rate", `{"date":"2026-11-06","base":"USD","quote":"EUR"}`},
		{"null rate", `{"date":"2026-11-06","base":"USD","quote":"EUR","rate":null}`},
		{"zero rate", `{"date":"2026-11-06","base":"USD","quote":"EUR","rate":0}`},
		{"negative rate", `{"date":"2026-11-06","base":"USD","quote":"EUR","rate":-1}`},
		{"string rate", `{"date":"2026-11-06","base":"USD","quote":"EUR","rate":"0.8"}`},
		{"boolean rate", `{"date":"2026-11-06","base":"USD","quote":"EUR","rate":true}`},
		{"too precise", `{"date":"2026-11-06","base":"USD","quote":"EUR","rate":0.1234567890123456789}`},
		{"duplicate rate", `{"date":"2026-11-06","base":"USD","quote":"EUR","rate":1,"rate":2}`},
		{"duplicate extra field", `{"date":"2026-11-06","base":"USD","quote":"EUR","rate":1,"extra":2,"extra":3}`},
		{"malformed extra field", `{"date":"2026-11-06","base":"USD","quote":"EUR","rate":1,"extra":{"value":}}`},
		{"extra JSON", valid + ` {}`},
		{"too big", valid + strings.Repeat(" ", maxResponseBytes)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, tc.body) })

			_, err := c.FetchRate(context.Background(), domain.PairUSDEUR)

			requireProviderCode(t, err, domain.FailureProviderInvalidResponse)
		})
	}
}

func TestFetchRateCancelAndTimeout(t *testing.T) {
	var calls atomic.Int32

	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)

		<-r.Context().Done()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.FetchRate(ctx, domain.PairUSDEGP)

	if !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatalf("pre-canceled context: %v, calls %d", err, calls.Load())
	}

	// Client's own short timeout behaves as a retryable provider timeout.
	slow := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)

		_, _ = w.Write([]byte(`{"date":"2026-11-06","base":"USD","quote":"EGP","rate":50}`))
	})

	slow.http.Timeout = 15 * time.Millisecond

	_, err = slow.FetchRate(context.Background(), domain.PairUSDEGP)

	requireProviderCode(t, err, domain.FailureProviderTimeout)

	// A caller-supplied deadline is an unwrapped ctx error for ProcessJob.
	ctx, cancel = context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()

	_, err = c.FetchRate(ctx, domain.PairUSDEGP)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected caller deadline, got %v", err)
	}
}

type failingRoundTripper struct{ err error }

func (r failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, r.err }

func TestFetchRateNetworkFailures(t *testing.T) {
	cases := []struct {
		err  error
		code domain.FailureCode
	}{
		{errors.New("connection refused"), domain.FailureProviderUnavailable},
		{&net.DNSError{IsTimeout: true}, domain.FailureProviderTimeout},
		{context.DeadlineExceeded, domain.FailureProviderTimeout},
	}

	for _, tc := range cases {
		transport := &http.Client{Transport: failingRoundTripper{err: tc.err}}

		c, err := NewWithBaseURL(BaseURL, transport)
		if err != nil {
			t.Fatal(err)
		}

		_, err = c.FetchRate(context.Background(), domain.PairUSDEUR)

		requireProviderCode(t, err, tc.code)
	}
}

func TestNewWithBaseURLValidation(t *testing.T) {
	for _, invalid := range []string{"", "api.frankfurter.dev", "ftp://x", "http://", "https://user:pass@example.com", "https://host/path", "https://host?x=1", "https://host/#hash"} {
		if _, err := NewWithBaseURL(invalid, nil); err == nil {
			t.Errorf("base URL accepted: %q", invalid)
		}
	}

	if New() == nil {
		t.Error("production constructor returned nil")
	}
}
