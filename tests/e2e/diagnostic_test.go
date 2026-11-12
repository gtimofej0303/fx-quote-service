//go:build e2e

package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type diagnosticLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write adds data to the test log buffer safely.
func (b *diagnosticLogBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

// String reads the test log buffer safely.
func (b *diagnosticLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestE2EProviderDiagnosticIsLoggedButNotExposed checks that internal provider errors appear in logs but stay out of HTTP responses.
func TestE2EProviderDiagnosticIsLoggedButNotExposed(t *testing.T) {
	rig := newTestRig(t)
	var logs diagnosticLogBuffer
	rig.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	provider := newFakeFrankfurter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "private upstream response body")
	})
	id := rig.post(t, "USD/EGP", nil)
	rig.startWorkers(t, provider, 2)
	got := rig.waitStatus(t, id, "failed", 5*time.Second)
	if got.Error == nil || got.Error.Code != "provider_rate_not_found" || got.Error.Message != "Rate not found at provider" {
		t.Fatalf("unexpected public failure: %+v", got)
	}
	response, err := rig.request(rig.ctx, http.MethodGet, fmt.Sprintf("/api/v1/quote-updates/%d", id), nil, nil)
	if err != nil || response.status != http.StatusOK {
		t.Fatalf("HTTP failure response: %+v / %v", response, err)
	}
	for _, marker := range []string{"Frankfurter HTTP status 404", "private upstream response body", "Diagnostic", "provider_error"} {
		if strings.Contains(string(response.body), marker) {
			t.Fatalf("diagnostic leaked to HTTP response: %s", response.body)
		}
	}
	stored, err := rig.repo.GetJob(rig.ctx, id)
	if err != nil || stored.Failure == nil || stored.Failure.Message != "Rate not found at provider" {
		t.Fatalf("diagnostic leaked to persisted public message: %+v / %v", stored, err)
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] != "quote provider attempt failed" {
			continue
		}
		count++
		if record["provider_error"] != "Frankfurter HTTP status 404" || record["update_id"] != float64(id) ||
			record["pair"] != "USD/EGP" || record["attempt"] != float64(1) {
			t.Fatalf("diagnostic context was lost: %s", line)
		}
	}
	if count != 1 || strings.Contains(logs.String(), "private upstream response body") {
		t.Fatalf("missing diagnostic or exposed upstream body: %s", logs.String())
	}
}
