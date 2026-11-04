package http

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
)

type DBPinger interface {
	PingContext(context.Context) error
}

func NewRouter(db DBPinger, ready *atomic.Bool, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health/live", liveHandler)
	mux.HandleFunc("/health/ready", readyHandler(db, ready))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, r, http.StatusNotFound, "route_not_found", "Route not found")
	})

	return requestMiddleware(mux, logger)
}
