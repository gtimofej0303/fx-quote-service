package http

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"
)

type healthResponse struct {
	Status string `json:"status"`
}

func liveHandler(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

func readyHandler(db DBPinger, ready *atomic.Bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireGET(w, r) {
			return
		}
		if !ready.Load() {
			writeAPIError(w, r, http.StatusServiceUnavailable, "service_unavailable", "Service is not ready")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			writeAPIError(w, r, http.StatusServiceUnavailable, "service_unavailable", "Database is unavailable")
			return
		}
		writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
	}
}

func requireGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	w.Header().Set("Allow", http.MethodGet)
	writeAPIError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	return false
}
