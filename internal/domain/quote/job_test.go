package quote

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStatusTransitions(t *testing.T) {
	allowed := [][2]JobStatus{
		{JobPending, JobProcessing},
		{JobProcessing, JobPending},
		{JobProcessing, JobSucceeded},
		{JobProcessing, JobFailed},
	}

	for _, pair := range allowed {
		if err := ValidateStatusTransition(pair[0], pair[1]); err != nil {
			t.Errorf("expected %q -> %q to be allowed: %v", pair[0], pair[1], err)
		}
	}

	for _, pair := range [][2]JobStatus{
		{JobPending, JobPending},
		{JobPending, JobSucceeded},
		{JobProcessing, JobProcessing},
		{JobSucceeded, JobPending},
		{JobSucceeded, JobFailed},
		{JobFailed, JobProcessing},
	} {
		if err := ValidateStatusTransition(pair[0], pair[1]); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("expected %q -> %q to be rejected, got %v", pair[0], pair[1], err)
		}
	}

	if err := ValidateStatusTransition("unknown", JobPending); !errors.Is(err, ErrInvalidJobStatus) {
		t.Fatalf("invalid status should be rejected: %v", err)
	}

	if !JobSucceeded.IsTerminal() || !JobFailed.IsTerminal() || JobPending.IsTerminal() || JobProcessing.IsTerminal() {
		t.Fatal("incorrect terminal-state check")
	}
}

func TestIdempotencyKeyValidation(t *testing.T) {
	for _, key := range []string{"a", "Operation-123:EUR_USD.1", strings.Repeat("x", 128)} {
		if err := ValidateIdempotencyKey(key); err != nil {
			t.Errorf("valid key %q: %v", key, err)
		}
	}

	for _, key := range []string{"", " a", "a/b", "a b", "ключ", strings.Repeat("x", 129)} {
		if err := ValidateIdempotencyKey(key); !errors.Is(err, ErrInvalidIdempotencyKey) {
			t.Errorf("invalid key %q: %v", key, err)
		}
	}
}

func TestUpdateFailureCodes(t *testing.T) {
	for _, code := range []FailureCode{
		FailureProviderTimeout, FailureProviderUnavailable, FailureProviderRateLimited,
		FailureProviderRateNotFound, FailureProviderRejectedRequest,
		FailureProviderInvalidResponse, FailureProcessingTimeout,
	} {
		if err := (UpdateFailure{Code: code, Message: "reason"}).Validate(); err != nil {
			t.Errorf("code %s: %v", code, err)
		}
	}

	for _, bad := range []UpdateFailure{
		{Code: "unknown", Message: "reason"},
		{Code: FailureProviderTimeout, Message: " \t "},
	} {
		if err := bad.Validate(); !errors.Is(err, ErrInvalidFailure) {
			t.Errorf("bad failure %+v: %v", bad, err)
		}
	}
}

func TestUpdateJobStateInvariants(t *testing.T) {
	created := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	key := "operation-123"
	pending, err := NewPendingJob(1, PairEUREGP, created, created, &key)
	if err != nil || pending.Validate() != nil {
		t.Fatalf("pending = %+v, err = %v", pending, err)
	}

	key = "modified"
	if *pending.IdempotencyKey != "operation-123" {
		t.Fatal("constructor should copy idempotency key")
	}

	processing := pending
	processing.Status = JobProcessing
	processing.Attempts = 1
	processing.Lease = &Lease{Token: "uuid-token", Until: created.Add(time.Minute)}
	if err := processing.Validate(); err != nil {
		t.Fatalf("processing: %v", err)
	}

	// An expired lease remains a valid persisted processing state until recovery.
	processing.Lease.Until = created.Add(time.Second)
	if err := processing.Validate(); err != nil {
		t.Fatalf("expired but valid lease: %v", err)
	}

	completed := created.Add(5 * time.Second)
	succeeded := pending
	succeeded.Status = JobSucceeded
	succeeded.Attempts = 1
	succeeded.CompletedAt = &completed
	if err := succeeded.Validate(); err != nil {
		t.Fatalf("succeeded: %v", err)
	}

	failed := succeeded
	failed.Status = JobFailed
	failed.Failure = &UpdateFailure{Code: FailureProviderTimeout, Message: "provider timed out"}
	if err := failed.Validate(); err != nil {
		t.Fatalf("failed: %v", err)
	}

	tests := []struct {
		name string
		job  UpdateJob
	}{
		{"zero ID", func() UpdateJob { j := pending; j.ID = 0; return j }()},
		{"negative attempts", func() UpdateJob { j := pending; j.Attempts = -1; return j }()},
		{"zero created", func() UpdateJob { j := pending; j.CreatedAt = time.Time{}; return j }()},
		{"zero next attempt", func() UpdateJob { j := pending; j.NextAttemptAt = time.Time{}; return j }()},
		{"pending with completion", func() UpdateJob { j := pending; j.CompletedAt = &completed; return j }()},
		{"pending with lease", func() UpdateJob { j := pending; j.Lease = processing.Lease; return j }()},
		{"processing without lease", func() UpdateJob { j := processing; j.Lease = nil; return j }()},
		{"processing without attempts", func() UpdateJob { j := processing; j.Attempts = 0; return j }()},
		{"processing with completion", func() UpdateJob { j := processing; j.CompletedAt = &completed; return j }()},
		{"processing with empty token", func() UpdateJob { j := processing; j.Lease = &Lease{Until: created.Add(time.Minute)}; return j }()},
		{"succeeded without completion", func() UpdateJob { j := succeeded; j.CompletedAt = nil; return j }()},
		{"succeeded with failure", func() UpdateJob { j := succeeded; j.Failure = failed.Failure; return j }()},
		{"failed without failure", func() UpdateJob { j := failed; j.Failure = nil; return j }()},
		{"failed with invalid failure", func() UpdateJob { j := failed; j.Failure = &UpdateFailure{Code: "bogus", Message: "error"}; return j }()},
		{"terminal with lease", func() UpdateJob { j := succeeded; j.Lease = processing.Lease; return j }()},
		{"completion before creation", func() UpdateJob {
			j := failed
			earlier := created.Add(-time.Second)
			j.CompletedAt = &earlier
			return j
		}()},
		{"invalid key", func() UpdateJob { j := pending; bad := ""; j.IdempotencyKey = &bad; return j }()},
		{"invalid status", func() UpdateJob { j := pending; j.Status = "bogus"; return j }()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.job.Validate(); !errors.Is(err, ErrInvalidJob) {
				t.Errorf("Validate() = %v; want ErrInvalidJob", err)
			}
		})
	}
}

func TestNewPendingJobRejectsInvalidData(t *testing.T) {
	now := time.Now()

	if _, err := NewPendingJob(0, PairUSDEUR, now, now, nil); !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("invalid job ID: %v", err)
	}

	bad := ""

	if _, err := NewPendingJob(1, PairUSDEUR, now, now, &bad); !errors.Is(err, ErrInvalidIdempotencyKey) {
		t.Fatalf("invalid provided key: %v", err)
	}
}
