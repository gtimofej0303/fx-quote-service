//go:build e2e

package e2e_test

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gtimofej0303/fx-quote-service/internal/repository/postgres"
	quoteservice "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
)

// TestE2ERetryAfterPersistsAcrossWorkerRestart checks that the saved retry time survives a worker restart.
func TestE2ERetryAfterPersistsAcrossWorkerRestart(t *testing.T) {
	for _, status := range []int{429, 503} {
		for _, asDate := range []bool{false, true} {
			t.Run(fmt.Sprintf("status_%d/date_%t", status, asDate), func(t *testing.T) {
				rig := newTestRig(t)

				var calls atomic.Int32

				expected := make(chan time.Time, 1)

				provider := newFakeFrankfurter(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/v2/rate/usd/egp" && calls.Add(1) == 1 {
						notBefore := time.Now().UTC().Add(time.Minute)
						header := "60"

						if asDate {
							notBefore = notBefore.Truncate(time.Second)
							header = notBefore.Format(http.TimeFormat)
						}

						w.Header().Set("Retry-After", header)
						w.WriteHeader(status)

						expected <- notBefore

						return
					}
					parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v2/rate/"), "/")

					fmt.Fprintf(w, `{"date":%q,"base":%q,"quote":%q,"rate":50.1}`,
						time.Now().UTC().Format("2006-01-02"), strings.ToUpper(parts[0]), strings.ToUpper(parts[1]))
				})
				id := rig.post(t, "USD/EGP", nil)
				stop := rig.startWorkers(t, provider, 2)

				var notBefore time.Time

				select {
				case notBefore = <-expected:
				case <-time.After(5 * time.Second):
					t.Fatal("provider was not called")
				}

				deadline := time.NewTimer(5 * time.Second)
				defer deadline.Stop()

				ticker := time.NewTicker(20 * time.Millisecond)
				defer ticker.Stop()

				for {
					job, err := rig.repo.GetJob(rig.ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					if job.Status == "pending" && job.Attempts == 1 {
						if job.NextAttemptAt.Before(notBefore.Add(-time.Microsecond)) || job.Lease != nil {
							t.Fatalf("provider deadline was not persisted: %+v, expected >= %v", job, notBefore)
						}
						break
					}
					select {
					case <-deadline.C:
						t.Fatal("retry was not scheduled")
					case <-ticker.C:
					}
				}

				stop()

				// A new pool must see the saved retry time after the worker stops.
				db, err := postgres.Open(os.Getenv("TEST_DATABASE_URL"))
				if err != nil {
					t.Fatal(err)
				}

				defer db.Close()
				if _, err := postgres.New(db).ClaimNext(rig.ctx, 30*time.Second, 3); !errors.Is(err, quoteservice.ErrNoClaimableJob) {
					t.Fatalf("claimed job before Retry-After: %v", err)
				}

				stop = rig.startWorkers(t, provider, 2)
				other := rig.post(t, "EUR/USD", nil)
				rig.waitStatus(t, other, "succeeded", 5*time.Second)

				stop()

				if calls.Load() != 1 {
					t.Fatalf("provider called too early: %d", calls.Load())
				}

				// Change this test job's retry time to avoid a minute of waiting.
				if _, err := rig.db.ExecContext(rig.ctx, `UPDATE quote_updates SET next_attempt_at=statement_timestamp()-interval '1 second' WHERE id=$1`, id); err != nil {
					t.Fatal(err)
				}

				rig.startWorkers(t, provider, 2)

				got := rig.waitStatus(t, id, "succeeded", 5*time.Second)

				job, err := rig.repo.GetJob(rig.ctx, id)
				if err != nil || job.Attempts != 2 || calls.Load() != 2 || got.Price == nil || *got.Price != "50.1" {
					t.Fatalf("retry did not recover: job=%+v calls=%d result=%+v err=%v", job, calls.Load(), got, err)
				}
			})
		}
	}
}
