package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
	quoteservice "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
)

func TestWorkerLogsDiagnosticBeforeFinalization(t *testing.T) {
	databaseErr := errors.New("database write failed")
	cases := []struct {
		name     string
		attempt  int
		writeErr error
		success  bool
	}{
		{"retry", 1, nil, false},
		{"terminal", 3, nil, false},
		{"retry database error", 1, databaseErr, false},
		{"terminal database error", 3, databaseErr, false},
		{"lost lease", 1, quoteservice.ErrLeaseLost, false},
		{"success", 1, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cause := &net.DNSError{Err: "synthetic DNS failure", Name: "upstream.example.invalid"}
			queue := &fakeQueue{job: validClaim(tc.attempt), writeErr: tc.writeErr}
			processor := fakeProcessor(func(context.Context, domain.UpdateJob, quoteservice.RateProvider) (quoteservice.ProcessResult, error) {
				if tc.success {
					return quoteservice.ProcessResult{}, nil
				}
				return quoteservice.ProcessResult{
					Failure:   &domain.UpdateFailure{Code: domain.FailureProviderUnavailable, Message: "Safe public message"},
					Retryable: true, Diagnostic: cause,
				}, nil
			})
			var logs bytes.Buffer
			w := testWorker(t, queue, processor)
			w.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			err := w.runOne(context.Background())
			if errors.Is(tc.writeErr, databaseErr) && !errors.Is(err, databaseErr) {
				t.Fatalf("database error was lost: %v", err)
			}
			if !errors.Is(tc.writeErr, databaseErr) && err != nil {
				t.Fatal(err)
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
				if record["provider_error"] != cause.Error() || record["update_id"] != float64(41) ||
					record["pair"] != "USD/EGP" || record["attempt"] != float64(tc.attempt) ||
					record["code"] != "provider_unavailable" || record["retryable"] != true {
					t.Fatalf("incomplete diagnostic: %s", line)
				}
			}
			want := 1
			if tc.success {
				want = 0
			}
			if count != want {
				t.Fatalf("diagnostic count=%d, want %d; logs=%s", count, want, logs.String())
			}
			if queue.failure != nil && queue.failure.Message != "Safe public message" {
				t.Fatalf("diagnostic leaked to persisted failure: %+v", queue.failure)
			}
		})
	}
}
