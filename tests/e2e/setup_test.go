//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	httptransport "github.com/gtimofej0303/fx-quote-service/internal/handler/http"
	"github.com/gtimofej0303/fx-quote-service/internal/provider/frankfurter"
	"github.com/gtimofej0303/fx-quote-service/internal/repository/postgres"
	quoteservice "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
	"github.com/gtimofej0303/fx-quote-service/internal/worker"
)

// This package has its own test setup.
// Run tests/e2e and tests/integration one at a time.
type testRig struct {
	ctx     context.Context
	db      *sql.DB
	repo    *postgres.Repository
	service *quoteservice.Service
	api     *httptest.Server
	client  *http.Client
	logger  *slog.Logger
}

// newTestRig creates the test database connection, service, and HTTP server.
func newTestRig(t *testing.T) *testRig {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run e2e tests")
	}

	db, err := postgres.Open(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("cannot connect to test PostgreSQL: %v", err)
	}

	var name string

	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}

	if !strings.HasSuffix(name, "_test") {
		t.Fatalf("refusing destructive e2e test on database %q", name)
	}

	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM quote_updates`).Scan(new(int)); err != nil {
		t.Fatalf("apply migrations to _test database before e2e: %v", err)
	}

	if _, err := db.ExecContext(ctx, `TRUNCATE quotes, quote_updates RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("reset dedicated test database: %v", err)
	}

	repo := postgres.New(db)

	service, err := quoteservice.New(repo)
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ready := &atomic.Bool{}
	ready.Store(true)

	api := httptest.NewServer(httptransport.NewRouter(db, ready, logger, service))
	t.Cleanup(api.Close)

	return &testRig{
		ctx: ctx, db: db, repo: repo, service: service, api: api,
		client: &http.Client{Timeout: 10 * time.Second}, logger: logger,
	}
}

type httpResult struct {
	status int
	header http.Header
	body   []byte
}

// keyPtr returns a pointer to the key string.
func keyPtr(value string) *string { return &value }

// request sends an HTTP request and reads the response.
func (r *testRig) request(ctx context.Context, method, path string, body []byte, key *string) (httpResult, error) {
	req, err := http.NewRequestWithContext(ctx, method, r.api.URL+path, bytes.NewReader(body))
	if err != nil {
		return httpResult{}, err
	}

	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}

	if key != nil {
		req.Header.Set("Idempotency-Key", *key)
	}

	res, err := r.client.Do(req)
	if err != nil {
		return httpResult{}, err
	}

	defer res.Body.Close()

	data, err := io.ReadAll(io.LimitReader(res.Body, 16*1024))
	if err != nil {
		return httpResult{}, err
	}

	return httpResult{status: res.StatusCode, header: res.Header.Clone(), body: data}, nil
}

// decodeResult reads the JSON response into the given value.
func decodeResult(t *testing.T, result httpResult, into any) {
	t.Helper()

	if err := json.Unmarshal(result.body, into); err != nil {
		t.Fatalf("decode HTTP %d response %q: %v", result.status, result.body, err)
	}
}

type accepted struct {
	ID int64 `json:"id"`
}

type wireFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type wireUpdate struct {
	ID          int64        `json:"id"`
	Pair        string       `json:"pair"`
	Status      string       `json:"status"`
	CreatedAt   string       `json:"created_at"`
	CompletedAt *string      `json:"completed_at"`
	Price       *string      `json:"price"`
	UpdatedAt   *string      `json:"updated_at"`
	SourceDate  *string      `json:"source_date"`
	Error       *wireFailure `json:"error"`
}

type wireLatest struct {
	UpdateID   int64  `json:"update_id"`
	Pair       string `json:"pair"`
	Price      string `json:"price"`
	UpdatedAt  string `json:"updated_at"`
	SourceDate string `json:"source_date"`
}

// post creates a job through HTTP and returns its ID.
func (r *testRig) post(t *testing.T, pair string, key *string) int64 {
	t.Helper()

	result, err := r.request(r.ctx, http.MethodPost, "/api/v1/quote-updates", []byte(fmt.Sprintf(`{"pair":%q}`, pair)), key)
	if err != nil {
		t.Fatal(err)
	}

	if result.status != http.StatusAccepted || result.header.Get("X-Request-ID") == "" {
		t.Fatalf("POST status=%d, headers=%v, body=%s", result.status, result.header, result.body)
	}

	var job accepted

	decodeResult(t, result, &job)

	if job.ID < 1 || result.header.Get("Location") != fmt.Sprintf("/api/v1/quote-updates/%d", job.ID) {
		t.Fatalf("incorrect POST response: %+v, headers=%v", job, result.header)
	}

	return job.ID
}

// getUpdate gets a job through HTTP and checks the response.
func (r *testRig) getUpdate(t *testing.T, id int64) wireUpdate {
	t.Helper()

	result, err := r.request(r.ctx, http.MethodGet, fmt.Sprintf("/api/v1/quote-updates/%d", id), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if result.status != http.StatusOK {
		t.Fatalf("GET update %d: HTTP %d: %s", id, result.status, result.body)
	}

	var update wireUpdate

	decodeResult(t, result, &update)

	return update
}

// waitStatus waits until the job reaches the expected status.
func (r *testRig) waitStatus(t *testing.T, id int64, expected string, duration time.Duration) wireUpdate {
	t.Helper()

	deadline := time.NewTimer(duration)
	defer deadline.Stop()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		got := r.getUpdate(t, id)

		if got.Status == expected {
			return got
		}

		if got.Status == "succeeded" || got.Status == "failed" {
			t.Fatalf("update %d reached unexpected terminal status: %+v", id, got)
		}

		select {
		case <-deadline.C:
			t.Fatalf("timeout waiting for status %q of update %d; last=%+v", expected, id, got)
		case <-ticker.C:
		}
	}
}

// newFakeFrankfurter creates a rate client connected to a test server.
func newFakeFrankfurter(t *testing.T, handler http.HandlerFunc) *frankfurter.Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := frankfurter.NewWithBaseURL(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	return client
}

// startWorkers starts test workers and returns a function that stops them.
func (r *testRig) startWorkers(t *testing.T, provider quoteservice.RateProvider, count int) func() {
	t.Helper()

	workers := make([]*worker.Worker, 0, count)

	for i := 0; i < count; i++ {
		w, err := worker.New(r.repo, r.service, provider, r.logger)
		if err != nil {
			t.Fatal(err)
		}

		workers = append(workers, w)
	}

	ctx, cancel := context.WithCancel(r.ctx)
	done := make(chan error, count)

	for _, w := range workers {
		go func(w *worker.Worker) { done <- w.Run(ctx) }(w)
	}

	var once sync.Once

	stop := func() {
		once.Do(func() {
			cancel()

			for range workers {
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("worker stopped with error: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Errorf("worker did not stop within 5 seconds")
				}
			}
		})
	}

	t.Cleanup(stop)

	return stop
}
