//go:build e2e

package e2e_test

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestE2EFutureSourceDateDoesNotPoisonLatest(t *testing.T) {
	rig := newTestRig(t)

	var future atomic.Bool

	provider := newFakeFrankfurter(t, func(w http.ResponseWriter, r *http.Request) {
		date := time.Now().UTC().Format("2006-01-02")

		if future.Load() {
			date = "2099-01-01"
		}

		fmt.Fprintf(w, `{"date":%q,"base":"USD","quote":"EGP","rate":50.1}`, date)
	})

	rig.startWorkers(t, provider, 2)

	first := rig.post(t, "USD/EGP", nil)

	rig.waitStatus(t, first, "succeeded", 10*time.Second)

	future.Store(true)

	bad := rig.post(t, "USD/EGP", nil)

	failed := rig.waitStatus(t, bad, "failed", 10*time.Second)
	if failed.Error == nil || failed.Error.Code != "provider_invalid_response" || failed.Price != nil {
		t.Fatalf("unexpected future-date result: %+v", failed)
	}

	var count int

	if err := rig.db.QueryRowContext(rig.ctx, `SELECT count(*) FROM quotes WHERE update_id=$1`, bad).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid quote was stored: count=%d err=%v", count, err)
	}

	latest, err := rig.service.GetLatestQuote(rig.ctx, "USD/EGP")
	if err != nil || latest.UpdateID != first {
		t.Fatalf("future date replaced latest: %+v / %v", latest, err)
	}

	future.Store(false)
	recovered := rig.post(t, "USD/EGP", nil)

	rig.waitStatus(t, recovered, "succeeded", 10*time.Second)

	result, err := rig.request(rig.ctx, http.MethodGet, "/api/v1/quotes/latest?pair=USD%2FEGP", nil, nil)
	if err != nil || result.status != http.StatusOK {
		t.Fatalf("latest HTTP request: %+v / %v", result, err)
	}

	var body wireLatest

	decodeResult(t, result, &body)

	if body.UpdateID != recovered || body.Price != "50.1" {
		t.Fatalf("latest did not recover: %+v", body)
	}
}
