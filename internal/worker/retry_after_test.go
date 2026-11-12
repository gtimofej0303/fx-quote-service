package worker

import (
	"context"
	"testing"
	"time"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
	quoteservice "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
)

// TestWorkerHonorsRetryNotBefore checks that the worker respects the provider's earliest retry time.
func TestWorkerHonorsRetryNotBefore(t *testing.T) {
	cases := []struct {
		name      string
		notBefore time.Time
		attempt   int
		retryable bool
		wantDelay time.Duration
	}{
		{"missing", time.Time{}, 1, true, 2 * time.Second},
		{"past", nowFixed.Add(-time.Minute), 1, true, 2 * time.Second},
		{"shorter than backoff", nowFixed.Add(time.Second), 2, true, 4 * time.Second},
		{"longer than backoff", nowFixed.Add(time.Minute), 1, true, time.Minute},
		{"last attempt", nowFixed.Add(time.Minute), 3, true, 0},
		{"permanent failure", nowFixed.Add(time.Minute), 1, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			queue := &fakeQueue{job: validClaim(tc.attempt)}

			processor := fakeProcessor(func(context.Context, domain.UpdateJob, quoteservice.RateProvider) (quoteservice.ProcessResult, error) {
				return quoteservice.ProcessResult{
					Failure:   &domain.UpdateFailure{Code: domain.FailureProviderUnavailable, Message: "Unavailable"},
					Retryable: tc.retryable, RetryNotBefore: tc.notBefore,
				}, nil
			})

			if err := testWorker(t, queue, processor).runOne(context.Background()); err != nil {
				t.Fatal(err)
			}

			if tc.wantDelay == 0 {
				if queue.retryCalls != 0 || queue.failureCalls != 1 {
					t.Fatalf("terminal failure must not retry: %+v", queue)
				}
			} else if queue.retryCalls != 1 || queue.failureCalls != 0 || !queue.retryAt.Equal(nowFixed.Add(tc.wantDelay)) {
				t.Fatalf("wrong retry schedule: %+v", queue)
			}
		})
	}
}
