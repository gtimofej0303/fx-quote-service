//go:build e2e

package e2e_test

import (
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestE2EDatabaseLockReturnsJSONTimeoutAndAllowsSafeRetry(t *testing.T) {
	rig := newTestRig(t)
	id := rig.post(t, "EUR/EGP", nil)

	lock, err := rig.db.BeginTx(rig.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err := lock.ExecContext(rig.ctx, `LOCK TABLE quote_updates IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		result  httpResult
		err     error
		elapsed time.Duration
	}
	cases := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/quote-updates"},
		{http.MethodGet, "/api/v1/quote-updates/" + strconv.FormatInt(id, 10)},
		{http.MethodGet, "/api/v1/quotes/latest?pair=EUR%2FEGP"},
	}
	results := make([]outcome, len(cases))
	var wg sync.WaitGroup
	for i, tc := range cases {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			result, err := rig.request(rig.ctx, tc.method, tc.path,
				[]byte(`{"pair":"EUR/EGP"}`), keyPtr("timeout-safe-retry"))
			results[i] = outcome{result: result, err: err, elapsed: time.Since(start)}
		}()
	}
	wg.Wait()

	for i, got := range results {
		if got.err != nil {
			t.Fatalf("%s: expected JSON response, got transport error: %v", cases[i].path, got.err)
		}
		var body struct {
			Error     wireFailure `json:"error"`
			RequestID string      `json:"request_id"`
		}
		decodeResult(t, got.result, &body)
		if got.result.status != http.StatusServiceUnavailable ||
			got.result.header.Get("Content-Type") != "application/json" ||
			body.Error.Code != "service_unavailable" || body.RequestID == "" ||
			body.RequestID != got.result.header.Get("X-Request-ID") {
			t.Fatalf("%s: invalid timeout response: %+v body=%s", cases[i].path, got.result, got.result.body)
		}
		if got.elapsed < 4*time.Second || got.elapsed >= 9*time.Second {
			t.Fatalf("%s: unexpected request duration %s", cases[i].path, got.elapsed)
		}
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := rig.db.QueryRowContext(rig.ctx,
		`SELECT count(*) FROM quote_updates WHERE idempotency_key='timeout-safe-retry'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("timed-out blocked INSERT must not create a job after the lock is released")
	}
	first := rig.post(t, "EUR/EGP", keyPtr("timeout-safe-retry"))
	second := rig.post(t, "EUR/EGP", keyPtr("timeout-safe-retry"))
	if first != second {
		t.Fatalf("retry created duplicate jobs: %d and %d", first, second)
	}
	if got := rig.getUpdate(t, first); got.Status != "pending" {
		t.Fatalf("retry did not recover after the database lock: %+v", got)
	}
}
