
package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

type fakePinger struct {
	err error
}

func (p fakePinger) PingContext(ctx context.Context) error {
	return p.err
}

func TestHealthEndpoints(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string

		setup struct {
			ready bool
			dbErr error
		}

		assert struct {
			status    int
			errorCode string
		}
	}{
		{
			name:   "liveness ok",
			method: http.MethodGet,
			path:   "/health/live",
			assert: struct {
				status    int
				errorCode string
			}{status: 200},
		},
		{
			name:   "readiness ok",
			method: http.MethodGet,
			path:   "/health/ready",
			setup: struct {
				ready bool
				dbErr error
			}{ready: true},
			assert: struct {
				status    int
				errorCode string
			}{status: 200},
		},
		{
			name:   "readiness disabled",
			method: http.MethodGet,
			path:   "/health/ready",
			assert: struct {
				status    int
				errorCode string
			}{status: 503, errorCode: "service_unavailable"},
		},
		{
			name:   "database unavailable",
			method: http.MethodGet,
			path:   "/health/ready",
			setup: struct {
				ready bool
				dbErr error
			}{ready: true, dbErr: errors.New("database down")},
			assert: struct {
				status    int
				errorCode string
			}{status: 503, errorCode: "service_unavailable"},
		},
		{
			name:   "method not allowed",
			method: http.MethodPost,
			path:   "/health/live",
			assert: struct {
				status    int
				errorCode string
			}{status: 405, errorCode: "method_not_allowed"},
		},
		{
			name:   "route not found",
			method: http.MethodGet,
			path:   "/unknown",
			assert: struct {
				status    int
				errorCode string
			}{status: 404, errorCode: "route_not_found"},
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ready := &atomic.Bool{}
			ready.Store(tt.setup.ready)

			db := fakePinger{err: tt.setup.dbErr}
			router := NewRouter(db, ready, logger)

			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != tt.assert.status {
				t.Fatalf("status = %d, want %d",
					rec.Code, tt.assert.status)
			}

			var response struct {
				Status string `json:"status"`
				Error  struct {
					Code string `json:"code"`
				} `json:"error"`
				RequestID string `json:"request_id"`
			}

			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}

			if tt.assert.errorCode == "" {
				if response.Status != "ok" {
					t.Errorf("status = %q, want ok", response.Status)
				}
			} else {
				if response.Error.Code != tt.assert.errorCode {
					t.Errorf("error = %q, want %q",
						response.Error.Code, tt.assert.errorCode)
				}
			}

			requestID := rec.Header().Get("X-Request-ID")
			if _, err := uuid.Parse(requestID); err != nil {
				t.Errorf("invalid X-Request-ID: %q", requestID)
			}

			if rec.Header().Get("Content-Type") != "application/json" {
				t.Error("expected application/json")
			}

			if tt.assert.errorCode != "" && response.RequestID != requestID {
				t.Error("request IDs do not match")
			}
		})
	}
}
