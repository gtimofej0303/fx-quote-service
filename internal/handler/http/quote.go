package http

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
	quoteservice "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
)

const (
	maxUpdateBodyBytes   = 4096
	updateCollectionPath = "/api/v1/quote-updates"
	updateItemPrefix     = updateCollectionPath + "/"
)

// QuoteService is the application use cases needed by the HTTP adapter.
type QuoteService interface {
	RequestUpdate(context.Context, string, *string) (domain.UpdateJob, error)
	GetUpdate(context.Context, int64) (quoteservice.Update, error)
	GetLatestQuote(context.Context, string) (domain.Quote, error)
}

type quoteHandler struct {
	service QuoteService
	logger  *slog.Logger
}

// POST /api/v1/quote-updates persists a job; it never fetches a remote rate.
func (h quoteHandler) requestUpdate(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeAPIError(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")

		return
	}

	var key *string
	keys := r.Header.Values("Idempotency-Key")

	if len(keys) > 1 {
		writeAPIError(w, r, http.StatusBadRequest, "invalid_idempotency_key", "Provide Idempotency-Key only once")
		return
	}

	if len(keys) == 1 {
		value := keys[0]

		if err := domain.ValidateIdempotencyKey(value); err != nil {
			writeAPIError(w, r, http.StatusBadRequest, "invalid_idempotency_key", "Invalid Idempotency-Key")

			return
		}

		key = &value
	}

	if r.ContentLength > maxUpdateBodyBytes {
		writeAPIError(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "JSON body exceeds 4096 bytes")

		return
	}

	if r.Body == nil {
		writeAPIError(w, r, http.StatusBadRequest, "invalid_json", "JSON body is required")

		return
	}

	body := http.MaxBytesReader(w, r.Body, maxUpdateBodyBytes)
	defer body.Close()

	data, err := io.ReadAll(body)
	if err != nil {
		var tooLarge *http.MaxBytesError

		if errors.As(err, &tooLarge) {
			writeAPIError(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "JSON body exceeds 4096 bytes")
		} else {
			writeAPIError(w, r, http.StatusBadRequest, "invalid_json", "Cannot read JSON body")
		}

		return
	}

	pair, code := decodeUpdatePair(data)
	if code != "" {
		message := "Invalid JSON body"

		if code == "invalid_request" {
			message = "JSON body must contain only one string field named pair"
		}

		writeAPIError(w, r, http.StatusBadRequest, code, message)

		return
	}

	job, err := h.service.RequestUpdate(r.Context(), pair, key)
	if err != nil {
		h.writeServiceError(w, r, err)

		return
	}

	if job.ID <= 0 {
		h.writeServiceError(w, r, fmt.Errorf("%w: invalid created job ID", quoteservice.ErrInconsistentResult))

		return
	}

	w.Header().Set("Location", updateItemPrefix+strconv.FormatInt(job.ID, 10))

	writeJSON(w, http.StatusAccepted, updateAcceptedResponse{ID: job.ID})
}

// decodeUpdatePair rejects unknown/duplicate fields, missing/null pair and extra JSON values.
func decodeUpdatePair(data []byte) (pair, errorCode string) {
	dec := json.NewDecoder(bytes.NewReader(data))

	first, err := dec.Token()
	if err != nil {
		return "", "invalid_json"
	}

	if first != json.Delim('{') {
		return "", "invalid_request"
	}

	seenPair := false

	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return "", "invalid_json"
		}

		name, ok := token.(string)
		if !ok {
			return "", "invalid_json"
		}

		if name != "pair" || seenPair {
			return "", "invalid_request"
		}
		var raw json.RawMessage

		if err := dec.Decode(&raw); err != nil {
			return "", "invalid_json"
		}

		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return "", "invalid_request"
		}

		if err := json.Unmarshal(raw, &pair); err != nil {
			return "", "invalid_request"
		}

		seenPair = true
	}

	end, err := dec.Token()
	if err != nil || end != json.Delim('}') {
		return "", "invalid_json"
	}

	if !seenPair {
		return "", "invalid_request"
	}

	if _, err := dec.Token(); err != io.EOF {
		return "", "invalid_json"
	}

	return pair, ""
}

func isJSONContentType(contentType string) bool {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return false
	}

	for name, value := range params {
		if name != "charset" || !strings.EqualFold(value, "utf-8") {
			return false
		}
	}

	return true
}

func (h quoteHandler) getUpdate(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	escaped := strings.TrimPrefix(r.URL.EscapedPath(), updateItemPrefix)

	if strings.Contains(escaped, "/") {
		writeAPIError(w, r, http.StatusNotFound, "route_not_found", "Route not found")

		return
	}

	id, err := parseUpdateID(escaped)
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "invalid_id", "Update ID must be a positive decimal integer without leading zeros")

		return
	}

	result, err := h.service.GetUpdate(r.Context(), id)
	if err != nil {
		h.writeServiceError(w, r, err)

		return
	}

	response, err := makeUpdateResponse(result)
	if err != nil {
		h.writeServiceError(w, r, err)

		return
	}

	writeJSON(w, http.StatusOK, response)
}

func parseUpdateID(raw string) (int64, error) {
	if len(raw) == 0 || raw[0] < '1' || raw[0] > '9' {
		return 0, quoteservice.ErrInvalidUpdateID
	}

	for i := 1; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, quoteservice.ErrInvalidUpdateID
		}
	}

	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, quoteservice.ErrInvalidUpdateID
	}

	return id, nil
}

func (h quoteHandler) getLatestQuote(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(params) != 1 || len(params["pair"]) != 1 {
		writeAPIError(w, r, http.StatusBadRequest, "invalid_request", "Provide exactly one pair query parameter")

		return
	}

	q, err := h.service.GetLatestQuote(r.Context(), params.Get("pair"))
	if err != nil {
		h.writeServiceError(w, r, err)

		return
	}

	if err := q.Validate(); err != nil {
		h.writeServiceError(w, r, fmt.Errorf("%w: latest response quote: %v", quoteservice.ErrInconsistentResult, err))

		return
	}

	writeJSON(w, http.StatusOK, latestQuoteResponse{
		UpdateID:   q.UpdateID,
		Pair:       q.Pair.String(),
		Price:      q.Price.String(),
		UpdatedAt:  utcTime(q.UpdatedAt),
		SourceDate: q.SourceDate.String(),
	})
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}

	w.Header().Set("Allow", method)
	writeAPIError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")

	return false
}

func utcTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func (h quoteHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := classifyServiceError(err)

	if status >= http.StatusInternalServerError && h.logger != nil {
		h.logger.Error("HTTP use case failed", "request_id", requestIDFrom(r.Context()),
			"method", r.Method, "path", r.URL.Path, "error", err)
	}

	writeAPIError(w, r, status, code, message)
}

func classifyServiceError(err error) (status int, code, message string) {
	switch {
	case errors.Is(err, domain.ErrInvalidPair):
		return http.StatusBadRequest, "invalid_pair", "Invalid currency pair format"
	case errors.Is(err, domain.ErrUnsupportedPair):
		return http.StatusUnprocessableEntity, "unsupported_pair", "Currency pair is not supported"
	case errors.Is(err, domain.ErrInvalidIdempotencyKey):
		return http.StatusBadRequest, "invalid_idempotency_key", "Invalid Idempotency-Key"
	case errors.Is(err, quoteservice.ErrInvalidUpdateID):
		return http.StatusBadRequest, "invalid_id", "Invalid update ID"
	case errors.Is(err, quoteservice.ErrIdempotencyConflict):
		return http.StatusConflict, "idempotency_conflict", "Idempotency-Key is already used for another currency pair"
	case errors.Is(err, quoteservice.ErrUpdateNotFound):
		return http.StatusNotFound, "update_not_found", "Update not found"
	case errors.Is(err, quoteservice.ErrQuoteNotFound):
		return http.StatusNotFound, "quote_not_found", "Quote not found"
	case isUnavailableError(err):
		return http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable"
	default:
		return http.StatusInternalServerError, "internal_error", "Internal server error"
	}
}

func isUnavailableError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return true
	}

	var networkError net.Error

	if errors.As(err, &networkError) {
		return true
	}

	var stateError interface{ SQLState() string }

	if errors.As(err, &stateError) {
		state := stateError.SQLState()

		return strings.HasPrefix(state, "08") || state == "57P01" || state == "57P02" ||
			state == "57P03" || state == "53300" || state == "53400"
	}

	return false
}
