//go:build e2e

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
)

func TestE2EConcurrentIdempotencyOverHTTP(t *testing.T) {
	rig := newTestRig(t)

	const requests = 32

	type answer struct {
		id     int64
		status int
		err    error
	}

	results := make(chan answer, requests)
	
	var wg sync.WaitGroup

	for i := 0; i < requests; i++ {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()

			pair := `{"pair":"EUR/EGP"}`

			if i%2 == 1 {
				pair = `{"pair":" eur/egp "}`
			}

			result, err := rig.request(rig.ctx, http.MethodPost, "/api/v1/quote-updates", []byte(pair), keyPtr("same-concurrent-key"))
			
			if err != nil {
				results <- answer{err: err}

				return
			}

			var a accepted

			if err := json.Unmarshal(result.body, &a); err != nil {
				results <- answer{err: err}
				
				return
			}

			if result.header.Get("Location") != fmt.Sprintf("/api/v1/quote-updates/%d", a.ID) ||
				result.header.Get("X-Request-ID") == "" {
				results <- answer{err: fmt.Errorf("bad headers: %v", result.header)}
				
				return
			}

			results <- answer{id: a.ID, status: result.status}
		}(i)
	}

	wg.Wait()

	close(results)

	var first int64

	for a := range results {
		if a.err != nil || a.status != http.StatusAccepted || a.id < 1 {
			t.Fatalf("concurrent POST: %+v", a)
		}

		if first == 0 {
			first = a.id
		}

		if a.id != first {
			t.Fatalf("same key produced %d and %d", first, a.id)
		}
	}

	var stored int

	if err := rig.db.QueryRowContext(rig.ctx, `SELECT count(*) FROM quote_updates WHERE idempotency_key='same-concurrent-key'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}

	if stored != 1 {
		t.Fatalf("number of jobs for one key = %d, want 1", stored)
	}

	if update := rig.getUpdate(t, first); update.Status != "pending" || update.Price != nil || update.Error != nil {
		t.Fatalf("POST must not synchronously fetch: %+v", update)
	}

	conflict, err := rig.request(rig.ctx, http.MethodPost, "/api/v1/quote-updates", []byte(`{"pair":"USD/EUR"}`), keyPtr("same-concurrent-key"))
	if err != nil {
		t.Fatal(err)
	}

	var errorBody struct {
		Error wireFailure `json:"error"`
	}

	decodeResult(t, conflict, &errorBody)

	if conflict.status != http.StatusConflict || errorBody.Error.Code != "idempotency_conflict" {
		t.Fatalf("different pair on same key: HTTP %d: %s", conflict.status, conflict.body)
	}

	emptyKey, err := rig.request(rig.ctx, http.MethodPost, "/api/v1/quote-updates", []byte(`{"pair":"EUR/EGP"}`), keyPtr(""))
	if err != nil {
		t.Fatal(err)
	}

	var invalidKeyBody struct {
		Error wireFailure `json:"error"`
	}

	decodeResult(t, emptyKey, &invalidKeyBody)

	if emptyKey.status != http.StatusBadRequest || invalidKeyBody.Error.Code != "invalid_idempotency_key" {
		t.Fatalf("empty Idempotency-Key: HTTP %d: %s", emptyKey.status, emptyKey.body)
	}

	// No header at all: each POST creates an independent job.
	unkeyedA := rig.post(t, "EUR/EGP", nil)
	unkeyedB := rig.post(t, "EUR/EGP", nil)

	if unkeyedA == unkeyedB || unkeyedA == first || unkeyedB == first {
		t.Fatalf("unkeyed POSTs must create fresh IDs: %d %d %d", first, unkeyedA, unkeyedB)
	}
}

func TestE2EHTTPWorkerFrankfurterAndLatest(t *testing.T) {
	rig := newTestRig(t)

	var calls atomic.Int32

	provider := newFakeFrankfurter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/rate/eur/egp" {
			http.NotFound(w, r)

			return
		}

		calls.Add(1)

		w.Header().Set("Content-Type", "application/json")

		_, _ = w.Write([]byte(`{"date":"2026-11-06","base":"EUR","quote":"EGP","rate":61.123456789012345678}`))
	})

	id := rig.post(t, "EUR/EGP", keyPtr("e2e-success"))

	rig.startWorkers(t, provider, 2)

	got := rig.waitStatus(t, id, "succeeded", 10*time.Second)
	if got.Pair != "EUR/EGP" || got.Price == nil || *got.Price != "61.123456789012345678" ||
		got.SourceDate == nil || *got.SourceDate != "2026-11-06" ||
		got.UpdatedAt == nil || got.CompletedAt == nil || got.Error != nil {
		t.Fatalf("invalid succeeded HTTP payload: %+v", got)
	}

	if _, err := time.Parse(time.RFC3339Nano, *got.UpdatedAt); err != nil {
		t.Fatalf("updated_at is not RFC3339: %v", err)
	}

	if calls.Load() != 1 {
		t.Fatalf("Frankfurter requests=%d, want 1", calls.Load())
	}

	result, err := rig.request(rig.ctx, http.MethodGet, "/api/v1/quotes/latest?pair=EUR%2FEGP", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if result.status != http.StatusOK {
		t.Fatalf("GET latest: HTTP %d: %s", result.status, result.body)
	}

	var latest wireLatest
	decodeResult(t, result, &latest)

	if latest.UpdateID != id || latest.Pair != "EUR/EGP" || latest.Price != *got.Price ||
		latest.SourceDate != *got.SourceDate || latest.UpdatedAt != *got.UpdatedAt {
		t.Fatalf("latest result disagrees with job: %+v vs %+v", latest, got)
	}

	missing, err := rig.request(rig.ctx, http.MethodGet, "/api/v1/quotes/latest?pair=USD%2FEUR", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if missing.status != http.StatusNotFound {
		t.Fatalf("uninitialized pair latest should be 404: HTTP %d, %s", missing.status, missing.body)
	}
}

func TestE2EProviderRetriesAndPermanentFailures(t *testing.T) {
	tests := []struct {
		name         string
		responseCode int
		responseBody string
		firstCode    int
		wantStatus   string
		wantError    string
		wantAttempts int
	}{
		{"429_then_success", http.StatusOK, `{"date":"2026-11-06","base":"USD","quote":"EGP","rate":50.123456789012345678}`, http.StatusTooManyRequests, "succeeded", "", 2},
		{"503_exhausts", http.StatusServiceUnavailable, "unavailable", 0, "failed", "provider_unavailable", 3},
		{"404_permanent", http.StatusNotFound, "missing", 0, "failed", "provider_rate_not_found", 1},
		{"invalid_success_JSON", http.StatusOK, `{"date":"2026-11-06","base":"EGP","quote":"USD","rate":2}`, 0, "failed", "provider_invalid_response", 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTestRig(t)

			var calls atomic.Int32

			provider := newFakeFrankfurter(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v2/rate/usd/egp" {
					http.NotFound(w, r)

					return
				}

				n := calls.Add(1)

				if n == 1 && tc.firstCode != 0 {
					w.WriteHeader(tc.firstCode)

					return
				}

				w.WriteHeader(tc.responseCode)

				_, _ = w.Write([]byte(tc.responseBody))
			})

			id := rig.post(t, "USD/EGP", nil)

			rig.startWorkers(t, provider, 2)

			got := rig.waitStatus(t, id, tc.wantStatus, 20*time.Second)
			
			persisted, err := rig.repo.GetJob(rig.ctx, id)
			if err != nil {
				t.Fatal(err)
			}

			if persisted.Attempts != tc.wantAttempts || int(calls.Load()) != tc.wantAttempts {
				t.Fatalf("attempts=%d, provider calls=%d, want=%d", persisted.Attempts, calls.Load(), tc.wantAttempts)
			}

			if tc.wantStatus == "failed" {
				if got.Error == nil || got.Error.Code != tc.wantError || got.Error.Message == "" ||
					got.Price != nil || got.CompletedAt == nil {
					t.Fatalf("wrong failed payload: %+v", got)
				}

				var count int

				if err := rig.db.QueryRowContext(rig.ctx, `SELECT count(*) FROM quotes WHERE update_id=$1`, id).Scan(&count); err != nil {
					t.Fatal(err)
				}

				if count != 0 {
					t.Fatalf("failed update %d has a quote", id)
				}
			} else if got.Price == nil || *got.Price != "50.123456789012345678" || got.Error != nil {
				t.Fatalf("wrong success after retry: %+v", got)
			}
		})
	}
}

func TestE2EWorkerRestartReclaimsInterruptedJob(t *testing.T) {
	rig := newTestRig(t)
	entered := make(chan struct{}, 1)

	blockingProvider := newFakeFrankfurter(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}

		<-r.Context().Done()
	})

	id := rig.post(t, "USD/EGP", keyPtr("restart-scenario"))

	stop := rig.startWorkers(t, blockingProvider, 1)
	
	select {
	case <-entered:
	case <-time.After(7 * time.Second):
		t.Fatal("worker never reached the provider")
	}

	stop()

	abandoned, err := rig.repo.GetJob(rig.ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if abandoned.Status != domain.JobProcessing || abandoned.Attempts != 1 || abandoned.Lease == nil {
		t.Fatalf("canceled worker must leave an owned processing row: %+v", abandoned)
	}

	if _, err := rig.db.ExecContext(rig.ctx, `UPDATE quote_updates SET locked_until=statement_timestamp() - interval '1 second' WHERE id=$1 AND status='processing'`, id); err != nil {
		t.Fatal(err)
	}

	var recoveredCalls atomic.Int32

	recoveryProvider := newFakeFrankfurter(t, func(w http.ResponseWriter, r *http.Request) {
		recoveredCalls.Add(1)

		_, _ = w.Write([]byte(`{"date":"2026-11-06","base":"USD","quote":"EGP","rate":51.876543210987654321}`))
	})

	rig.startWorkers(t, recoveryProvider, 1)

	got := rig.waitStatus(t, id, "succeeded", 10*time.Second)

	persisted, err := rig.repo.GetJob(rig.ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if persisted.Attempts != 2 || recoveredCalls.Load() != 1 || got.Price == nil || *got.Price != "51.876543210987654321" {
		t.Fatalf("failed to resume interrupted work: job=%+v HTTP=%+v calls=%d", persisted, got, recoveredCalls.Load())
	}

	var quotes int

	if err := rig.db.QueryRowContext(rig.ctx, `SELECT count(*) FROM quotes WHERE update_id=$1`, id).Scan(&quotes); err != nil {
		t.Fatal(err)
	}
	
	if quotes != 1 {
		t.Fatalf("recovered update has %d quotes, want 1", quotes)
	}
}
