package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
)

func TestRequestDeadlineReturnsJSONServiceUnavailable(t *testing.T) {
	svc := &fakeQuoteService{requestFn: func(ctx context.Context, _ string, _ *string) (job domain.UpdateJob, err error) {
		<-ctx.Done()

		return job, ctx.Err()
	}}

	router := withRequestDeadline(newQuoteTestRouter(svc), 20*time.Millisecond)

	rec, body := doRequest(t, router, http.MethodPost, updateCollectionPath,
		`{"pair":"EUR/EGP"}`, "application/json", nil)

	assertError(t, rec, body, http.StatusServiceUnavailable, "service_unavailable")
}

func TestRequestDeadlinePreservesEarlierDeadlineAndCancelsOnReturn(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	wantDeadline, _ := parent.Deadline()

	var handledContext context.Context

	handler := withRequestDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handledContext = r.Context()

		deadline, ok := handledContext.Deadline()

		if !ok || !deadline.Equal(wantDeadline) {
			t.Fatalf("earlier parent deadline changed: %v", deadline)
		}

		w.WriteHeader(http.StatusNoContent)
	}), time.Hour)

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil).WithContext(parent))
	if !errors.Is(handledContext.Err(), context.Canceled) {
		t.Fatal("request context must be canceled when the handler returns")
	}

	if parent.Err() != nil {
		t.Fatal("middleware must not cancel the parent context")
	}
}
