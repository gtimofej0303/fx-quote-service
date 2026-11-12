package http

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
	quoteservice "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
)

type fakeQuoteService struct {
	requestFn func(context.Context, string, *string) (domain.UpdateJob, error)
	getFn     func(context.Context, int64) (quoteservice.Update, error)
	latestFn  func(context.Context, string) (domain.Quote, error)
}

// RequestUpdate calls the test function that requests a job.
func (f *fakeQuoteService) RequestUpdate(ctx context.Context, pair string, key *string) (domain.UpdateJob, error) {
	if f.requestFn == nil {
		panic("unexpected RequestUpdate")
	}

	return f.requestFn(ctx, pair, key)
}

// GetUpdate calls the test function that gets a job result.
func (f *fakeQuoteService) GetUpdate(ctx context.Context, id int64) (quoteservice.Update, error) {
	if f.getFn == nil {
		panic("unexpected GetUpdate")
	}

	return f.getFn(ctx, id)
}

// GetLatestQuote calls the test function that gets the latest quote.
func (f *fakeQuoteService) GetLatestQuote(ctx context.Context, pair string) (domain.Quote, error) {
	if f.latestFn == nil {
		panic("unexpected GetLatestQuote")
	}

	return f.latestFn(ctx, pair)
}

var _ QuoteService = (*fakeQuoteService)(nil)

var httpTestTime = time.Date(2026, 11, 6, 12, 0, 0, 0, time.FixedZone("EET", 2*60*60))

// newQuoteTestRouter creates a test router with a ready database and the given service.
func newQuoteTestRouter(svc QuoteService) http.Handler {
	ready := &atomic.Bool{}
	ready.Store(true)

	return NewRouter(fakePinger{}, ready, slog.New(slog.NewTextHandler(io.Discard, nil)), svc)
}

// doRequest sends a test request and checks the JSON response and request ID.
func doRequest(t *testing.T, handler http.Handler, method, path, body, contentType string, headers http.Header) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}

	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("wrong Content-Type %q, response: %s", rec.Header().Get("Content-Type"), rec.Body.String())
	}

	requestID := rec.Header().Get("X-Request-ID")
	if _, err := uuid.Parse(requestID); err != nil {
		t.Fatalf("invalid X-Request-ID: %q", requestID)
	}

	var result map[string]any

	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("invalid response JSON: %v / %s", err, rec.Body.String())
	}

	if _, hasRequestID := result["request_id"]; hasRequestID {
		if _, ok := result["error"].(map[string]any); !ok || result["request_id"] != requestID {
			t.Fatalf("error must include matching request_id, got %#v", result)
		}
	}

	return rec, result
}

// assertError checks the HTTP status and JSON error fields.
func assertError(t *testing.T, rec *httptest.ResponseRecorder, result map[string]any, status int, code string) {
	t.Helper()

	if rec.Code != status {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, status, rec.Body.String())
	}

	detail, ok := result["error"].(map[string]any)
	if !ok || detail["code"] != code || detail["message"] == "" {
		t.Fatalf("expected error code %q, got %#v", code, result)
	}

	if len(result) != 2 {
		t.Fatalf("API errors must contain only error and request_id: %#v", result)
	}
}

// httpPendingJob creates a valid waiting job for HTTP tests.
func httpPendingJob(t *testing.T) domain.UpdateJob {
	t.Helper()

	job, err := domain.NewPendingJob(42, domain.PairEUREGP, httpTestTime, httpTestTime, nil)
	if err != nil {
		t.Fatal(err)
	}

	return job
}

// httpSucceededJob creates a valid successful job for HTTP tests.
func httpSucceededJob(t *testing.T) domain.UpdateJob {
	t.Helper()

	job := httpPendingJob(t)

	job.Status = domain.JobSucceeded
	job.Attempts = 1
	completed := httpTestTime.Add(time.Second)
	job.CompletedAt = &completed

	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}

	return job
}

// httpFailedJob creates a valid failed job for HTTP tests.
func httpFailedJob(t *testing.T) domain.UpdateJob {
	t.Helper()

	job := httpSucceededJob(t)
	job.Status = domain.JobFailed
	job.Failure = &domain.UpdateFailure{Code: domain.FailureProviderRateNotFound, Message: "Rate not found"}

	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}

	return job
}

// httpQuote creates a valid quote for HTTP tests.
func httpQuote(t *testing.T) domain.Quote {
	t.Helper()

	price, err := domain.ParsePrice("61.960000000000000001")
	if err != nil {
		t.Fatal(err)
	}

	date, err := domain.ParseSourceDate("2026-11-05")
	if err != nil {
		t.Fatal(err)
	}

	q, err := domain.NewQuote(42, domain.PairEUREGP, price, date, httpTestTime)
	if err != nil {
		t.Fatal(err)
	}

	return q
}

// TestPOSTCreatesPendingUpdateAndSetsLocation checks that POST creates a waiting job and returns its URL.
func TestPOSTCreatesPendingUpdateAndSetsLocation(t *testing.T) {
	calls := 0

	key := "Op-42"

	svc := &fakeQuoteService{requestFn: func(ctx context.Context, pair string, gotKey *string) (domain.UpdateJob, error) {
		calls++

		if ctx == nil || pair != "  eur/egp  " || gotKey == nil || *gotKey != key {
			t.Fatalf("wrong service arguments: %q / %v", pair, gotKey)
		}

		return httpPendingJob(t), nil
	}}

	router := newQuoteTestRouter(svc)
	headers := http.Header{"Idempotency-Key": {key}}

	rec, data := doRequest(t, router, http.MethodPost, updateCollectionPath, `{"pair":"  eur/egp  "}`, "application/json; charset=utf-8", headers)
	if rec.Code != http.StatusAccepted || rec.Header().Get("Location") != updateItemPrefix+"42" || data["id"] != float64(42) || len(data) != 1 || calls != 1 {
		t.Fatalf("POST: status=%d location=%q JSON=%#v calls=%d", rec.Code, rec.Header().Get("Location"), data, calls)
	}
}

// TestPOSTWithoutKeyAndReplayStayInService checks that the service handles requests without keys and repeated requests.
func TestPOSTWithoutKeyAndReplayStayInService(t *testing.T) {
	calls := 0

	svc := &fakeQuoteService{requestFn: func(_ context.Context, _ string, key *string) (domain.UpdateJob, error) {
		calls++

		job := httpPendingJob(t)

		if key == nil {
			job.ID = int64(100 + calls)
		} else if *key != "unique-1" {
			t.Fatalf("key unexpectedly normalized: %q", *key)
		}

		return job, nil
	}}

	router := newQuoteTestRouter(svc)

	first, a := doRequest(t, router, "POST", updateCollectionPath, `{"pair":"EUR/EGP"}`, "application/json", http.Header{"Idempotency-Key": {"unique-1"}})
	second, b := doRequest(t, router, "POST", updateCollectionPath, `{"pair":"EUR/EGP"}`, "application/json", http.Header{"Idempotency-Key": {"unique-1"}})
	third, c := doRequest(t, router, "POST", updateCollectionPath, `{"pair":"EUR/EGP"}`, "application/json", nil)
	if first.Code != 202 || second.Code != 202 || third.Code != 202 || a["id"] != b["id"] || c["id"] == a["id"] || calls != 3 {
		t.Fatalf("replay and unkeyed POSTs: %#v / %#v / %#v", a, b, c)
	}
}

// TestPOSTValidationRejectsBeforeService checks that invalid requests are rejected before calling the service.
func TestPOSTValidationRejectsBeforeService(t *testing.T) {
	svc := &fakeQuoteService{}
	router := newQuoteTestRouter(svc)

	cases := []struct {
		name, body, contentType string
		headers                 http.Header
		status                  int
		code                    string
	}{
		{"missing content type", `{"pair":"EUR/EGP"}`, "", nil, 415, "unsupported_media_type"},
		{"wrong media type", `{"pair":"EUR/EGP"}`, "text/plain", nil, 415, "unsupported_media_type"},
		{"wrong charset", `{"pair":"EUR/EGP"}`, "application/json; charset=iso-8859-1", nil, 415, "unsupported_media_type"},
		{"unexpected media parameter", `{"pair":"EUR/EGP"}`, "application/json; boundary=123", nil, 415, "unsupported_media_type"},
		{"empty body", "", "application/json", nil, 400, "invalid_json"},
		{"broken JSON", `{"pair":`, "application/json", nil, 400, "invalid_json"},
		{"trailing JSON", `{"pair":"EUR/EGP"} {}`, "application/json", nil, 400, "invalid_json"},
		{"array root", `[{"pair":"EUR/EGP"}]`, "application/json", nil, 400, "invalid_request"},
		{"null root", `null`, "application/json", nil, 400, "invalid_request"},
		{"empty object", `{}`, "application/json", nil, 400, "invalid_request"},
		{"extra field", `{"pair":"EUR/EGP","other":1}`, "application/json", nil, 400, "invalid_request"},
		{"duplicate pair", `{"pair":"EUR/EGP","pair":"USD/EUR"}`, "application/json", nil, 400, "invalid_request"},
		{"null pair", `{"pair":null}`, "application/json", nil, 400, "invalid_request"},
		{"numeric pair", `{"pair":42}`, "application/json", nil, 400, "invalid_request"},
		{"array pair", `{"pair":["EUR/EGP"]}`, "application/json", nil, 400, "invalid_request"},
		{"duplicate key header", `{"pair":"EUR/EGP"}`, "application/json", http.Header{"Idempotency-Key": {"a", "a"}}, 400, "invalid_idempotency_key"},
		{"empty key header", `{"pair":"EUR/EGP"}`, "application/json", http.Header{"Idempotency-Key": {""}}, 400, "invalid_idempotency_key"},
		{"space key header", `{"pair":"EUR/EGP"}`, "application/json", http.Header{"Idempotency-Key": {" bad "}}, 400, "invalid_idempotency_key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, result := doRequest(t, router, "POST", updateCollectionPath, tc.body, tc.contentType, tc.headers)

			assertError(t, rec, result, tc.status, tc.code)

			if rec.Header().Get("Location") != "" {
				t.Fatal("error must not set Location")
			}
		})
	}
}

// TestPOSTBodyLimitAtBoundaryAndUnknownLength checks the body size limit, including requests without a known length.
func TestPOSTBodyLimitAtBoundaryAndUnknownLength(t *testing.T) {
	base := `{"pair":"EUR/EGP"}`
	calls := 0

	svc := &fakeQuoteService{requestFn: func(_ context.Context, pair string, _ *string) (domain.UpdateJob, error) {
		calls++

		if pair != "EUR/EGP" {
			t.Fatalf("wrong pair %q", pair)
		}

		return httpPendingJob(t), nil
	}}

	router := newQuoteTestRouter(svc)

	valid := base + strings.Repeat(" ", maxUpdateBodyBytes-len(base))
	if len(valid) != maxUpdateBodyBytes {
		t.Fatal("test input not exactly 4096 bytes")
	}

	for _, tc := range []struct {
		body   string
		length int64
		status int
	}{
		{valid, int64(len(valid)), 202},
		{valid + " ", int64(len(valid) + 1), 413},
		{valid + " ", -1, 413},
	} {
		req := httptest.NewRequest("POST", updateCollectionPath, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = tc.length
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("length=%d contentlength=%d got %d, want %d: %s", len(tc.body), tc.length, rec.Code, tc.status, rec.Body.String())
		}
	}

	if calls != 1 {
		t.Fatalf("oversized requests called service %d times", calls)
	}
}

type sqlStateFailure string

// Error returns the test database error message.
func (e sqlStateFailure) Error() string { return "database error" }

// SQLState returns the test database error code.
func (e sqlStateFailure) SQLState() string { return string(e) }

// TestPOSTMapsServiceErrors checks the HTTP status and error code for each service error.
func TestPOSTMapsServiceErrors(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("wrapped: %w", domain.ErrInvalidPair), 400, "invalid_pair"},
		{domain.ErrUnsupportedPair, 422, "unsupported_pair"},
		{domain.ErrInvalidIdempotencyKey, 400, "invalid_idempotency_key"},
		{fmt.Errorf("storage: %w", quoteservice.ErrIdempotencyConflict), 409, "idempotency_conflict"},
		{fmt.Errorf("connection: %w", driver.ErrBadConn), 503, "service_unavailable"},
		{fmt.Errorf("deadline: %w", context.DeadlineExceeded), 503, "service_unavailable"},
		{fmt.Errorf("pg: %w", sqlStateFailure("08006")), 503, "service_unavailable"},
		{fmt.Errorf("pg: %w", sqlStateFailure("57P03")), 503, "service_unavailable"},
		{fmt.Errorf("pg: %w", sqlStateFailure("23505")), 500, "internal_error"},
		{errors.New("password=confidential"), 500, "internal_error"},
		{quoteservice.ErrInconsistentResult, 500, "internal_error"},
	}
	for _, tc := range cases {
		t.Run(tc.code+"/"+strconv.Itoa(tc.status), func(t *testing.T) {
			svc := &fakeQuoteService{requestFn: func(context.Context, string, *string) (domain.UpdateJob, error) {
				return domain.UpdateJob{}, tc.err
			}}

			rec, body := doRequest(t, newQuoteTestRouter(svc), "POST", updateCollectionPath, `{"pair":"EUR/EGP"}`, "application/json", nil)

			assertError(t, rec, body, tc.status, tc.code)

			if strings.Contains(rec.Body.String(), "confidential") || strings.Contains(rec.Body.String(), "storage") {
				t.Fatal("internal error leaked into HTTP")
			}
		})
	}
}

// TestGetUpdateAllStatesAndPublicSchemas checks the JSON response for each job status.
func TestGetUpdateAllStatesAndPublicSchemas(t *testing.T) {
	pending := httpPendingJob(t)

	processing := httpPendingJob(t)
	processing.Status = domain.JobProcessing
	processing.Attempts = 2
	processing.Lease = &domain.Lease{Token: "private-token", Until: httpTestTime.Add(time.Minute)}
	if err := processing.Validate(); err != nil {
		t.Fatal(err)
	}

	succeeded := httpSucceededJob(t)
	failed := httpFailedJob(t)

	quote := httpQuote(t)

	cases := []struct {
		name   string
		job    domain.UpdateJob
		quote  *domain.Quote
		fields []string
	}{
		{"pending", pending, nil, []string{"id", "pair", "status", "created_at"}},
		{"processing", processing, nil, []string{"id", "pair", "status", "created_at"}},
		{"succeeded", succeeded, &quote, []string{"id", "pair", "status", "created_at", "completed_at", "price", "updated_at", "source_date"}},
		{"failed", failed, nil, []string{"id", "pair", "status", "created_at", "completed_at", "error"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0

			svc := &fakeQuoteService{getFn: func(_ context.Context, id int64) (quoteservice.Update, error) {
				calls++

				if id != 42 {
					t.Fatalf("unexpected ID: %d", id)
				}

				return quoteservice.Update{Job: tc.job, Quote: tc.quote}, nil
			}}
			rec, body := doRequest(t, newQuoteTestRouter(svc), "GET", updateItemPrefix+"42", "", "", nil)
			if rec.Code != 200 || calls != 1 || len(body) != len(tc.fields) {
				t.Fatalf("bad status/fields/calls: %d %#v %d", rec.Code, body, calls)
			}

			for _, field := range tc.fields {
				if _, exists := body[field]; !exists {
					t.Errorf("missing %s", field)
				}
			}

			for _, secret := range []string{"Attempts", "attempts", "lease", "Lease", "idempotency_key", "next_attempt_at", "request_id"} {
				if _, exists := body[secret]; exists {
					t.Errorf("leaked field %s", secret)
				}
			}

			if body["status"] != tc.name || body["created_at"] != "2026-11-06T10:00:00Z" {
				t.Errorf("status or timestamp: %#v", body)
			}

			if tc.name == "succeeded" {
				if body["price"] != "61.960000000000000001" || body["source_date"] != "2026-11-05" || body["updated_at"] != "2026-11-06T10:00:00Z" {
					t.Errorf("price or date wrong: %#v", body)
				}
			}

			if tc.name == "failed" {
				failure, ok := body["error"].(map[string]any)
				if !ok || len(failure) != 2 || failure["code"] != "provider_rate_not_found" || failure["message"] != "Rate not found" {
					t.Errorf("failed error as data missing: %#v", body)
				}
			}
		})
	}
}

// TestGetUpdateInvalidIDAndRoutes checks invalid job IDs and request paths.
func TestGetUpdateInvalidIDAndRoutes(t *testing.T) {
	router := newQuoteTestRouter(&fakeQuoteService{})

	for _, path := range []string{
		updateItemPrefix, updateItemPrefix + "0", updateItemPrefix + "00", updateItemPrefix + "01",
		updateItemPrefix + "-1", updateItemPrefix + "+1", updateItemPrefix + "1.0",
		updateItemPrefix + "abc", updateItemPrefix + "9223372036854775808",
		updateItemPrefix + "%31",
	} {
		t.Run(path, func(t *testing.T) {
			rec, body := doRequest(t, router, "GET", path, "", "", nil)

			assertError(t, rec, body, 400, "invalid_id")
		})
	}

	rec, result := doRequest(t, router, "GET", updateItemPrefix+"42/extra", "", "", nil)
	assertError(t, rec, result, 404, "route_not_found")

	rec, result = doRequest(t, router, "GET", "/api/v1/not-exists", "", "", nil)
	assertError(t, rec, result, 404, "route_not_found")
}

// TestGetUpdateNotFoundAndInconsistentState checks missing jobs and successful jobs without quotes.
func TestGetUpdateNotFoundAndInconsistentState(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("wrapped: %w", quoteservice.ErrUpdateNotFound), 404, "update_not_found"},
		{quoteservice.ErrInconsistentResult, 500, "internal_error"},
		{errors.New("database internal details"), 500, "internal_error"},
	} {
		svc := &fakeQuoteService{getFn: func(context.Context, int64) (quoteservice.Update, error) {
			return quoteservice.Update{}, tc.err
		}}

		rec, result := doRequest(t, newQuoteTestRouter(svc), "GET", updateItemPrefix+"42", "", "", nil)
		assertError(t, rec, result, tc.status, tc.code)
	}

	svc := &fakeQuoteService{getFn: func(context.Context, int64) (quoteservice.Update, error) {
		return quoteservice.Update{Job: httpSucceededJob(t)}, nil
	}}

	rec, result := doRequest(t, newQuoteTestRouter(svc), "GET", updateItemPrefix+"42", "", "", nil)
	assertError(t, rec, result, 500, "internal_error")
}

// TestLatestQuoteSuccessValidationAndNoRate checks the latest quote, invalid input, and missing rates.
func TestLatestQuoteSuccessValidationAndNoRate(t *testing.T) {
	q := httpQuote(t)
	calls := 0

	svc := &fakeQuoteService{latestFn: func(_ context.Context, rawPair string) (domain.Quote, error) {
		calls++

		if rawPair != " eur/egp " {
			t.Fatalf("unexpected pair argument %q", rawPair)
		}

		return q, nil
	}}

	router := newQuoteTestRouter(svc)

	rec, data := doRequest(t, router, "GET", "/api/v1/quotes/latest?pair=%20eur%2Fegp%20", "", "", nil)
	if rec.Code != 200 || calls != 1 || len(data) != 5 || data["update_id"] != float64(42) ||
		data["pair"] != "EUR/EGP" || data["price"] != "61.960000000000000001" ||
		data["source_date"] != "2026-11-05" || data["updated_at"] != "2026-11-06T10:00:00Z" {
		t.Fatalf("unexpected latest: %d %#v calls=%d", rec.Code, data, calls)
	}

	for _, path := range []string{
		"/api/v1/quotes/latest", "/api/v1/quotes/latest?pair=EUR%2FEGP&pair=USD%2FEUR",
		"/api/v1/quotes/latest?pair=EUR%2FEGP&other=1", "/api/v1/quotes/latest?pair=%xx",
	} {
		t.Run(path, func(t *testing.T) {
			rec, data := doRequest(t, router, "GET", path, "", "", nil)
			assertError(t, rec, data, 400, "invalid_request")
		})
	}

	if calls != 1 {
		t.Errorf("bad queries reached service: %d", calls)
	}
}

// TestLatestQuoteMapsErrors checks HTTP responses for errors when getting the latest quote.
func TestLatestQuoteMapsErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrInvalidPair, 400, "invalid_pair"},
		{domain.ErrUnsupportedPair, 422, "unsupported_pair"},
		{quoteservice.ErrQuoteNotFound, 404, "quote_not_found"},
		{quoteservice.ErrInconsistentResult, 500, "internal_error"},
		{driver.ErrBadConn, 503, "service_unavailable"},
	} {
		svc := &fakeQuoteService{latestFn: func(context.Context, string) (domain.Quote, error) {
			return domain.Quote{}, tc.err
		}}

		rec, result := doRequest(t, newQuoteTestRouter(svc), "GET", "/api/v1/quotes/latest?pair=EUR%2FEGP", "", "", nil)
		assertError(t, rec, result, tc.status, tc.code)
	}
}

// TestKnownRoutesReturnJSON405AndAllow checks that wrong HTTP methods return JSON 405 and list allowed methods.
func TestKnownRoutesReturnJSON405AndAllow(t *testing.T) {
	router := newQuoteTestRouter(&fakeQuoteService{})

	for _, tc := range []struct{ method, path, allowed string }{
		{"GET", updateCollectionPath, "POST"},
		{"PUT", updateCollectionPath, "POST"},
		{"POST", updateItemPrefix + "42", "GET"},
		{"HEAD", updateItemPrefix + "42", "GET"},
		{"POST", "/api/v1/quotes/latest", "GET"},
	} {
		rec, data := doRequest(t, router, tc.method, tc.path, "", "", nil)
		assertError(t, rec, data, 405, "method_not_allowed")

		if rec.Header().Get("Allow") != tc.allowed {
			t.Errorf("%s %s: Allow=%q want %q", tc.method, tc.path, rec.Header().Get("Allow"), tc.allowed)
		}
	}
}
