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

// NewRouter registers the health and quote routes and wraps them with request middleware.
func NewRouter(db DBPinger, ready *atomic.Bool, logger *slog.Logger, quoteService QuoteService) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health/live", liveHandler)
	mux.HandleFunc("/health/ready", readyHandler(db, ready))

	quotes := quoteHandler{service: quoteService, logger: logger}
	// Register routes without HTTP method patterns so handlers can return JSON 405 errors.
	mux.HandleFunc(updateCollectionPath, quotes.requestUpdate)
	mux.HandleFunc(updateItemPrefix, quotes.getUpdate)
	mux.HandleFunc("/api/v1/quotes/latest", quotes.getLatestQuote)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, r, http.StatusNotFound, "route_not_found", "Route not found")
	})

	return requestMiddleware(withRequestDeadline(mux, requestTimeout), logger)
}
