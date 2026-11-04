package http

import (
	"encoding/json"
	"net/http"
)

type apiErrorResponse struct {
	Error     apiErrorDetail `json:"error"`
	RequestID string         `json:"request_id"`
}

type apiErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeAPIError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, apiErrorResponse{
		Error:     apiErrorDetail{Code: code, Message: message},
		RequestID: requestIDFrom(r.Context()),
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "failed to encode JSON response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}
