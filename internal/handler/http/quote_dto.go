package http

import (
	"fmt"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
	quoteservice "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
)

type updateAcceptedResponse struct {
	ID int64 `json:"id"`
}

type updateFailureResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type updateResponse struct {
	ID          int64                  `json:"id"`
	Pair        string                 `json:"pair"`
	Status      string                 `json:"status"`
	CreatedAt   string                 `json:"created_at"`
	CompletedAt *string                `json:"completed_at,omitempty"`
	Price       *string                `json:"price,omitempty"`
	UpdatedAt   *string                `json:"updated_at,omitempty"`
	SourceDate  *string                `json:"source_date,omitempty"`
	Error       *updateFailureResponse `json:"error,omitempty"`
}

type latestQuoteResponse struct {
	UpdateID   int64  `json:"update_id"`
	Pair       string `json:"pair"`
	Price      string `json:"price"`
	UpdatedAt  string `json:"updated_at"`
	SourceDate string `json:"source_date"`
}

func makeUpdateResponse(update quoteservice.Update) (updateResponse, error) {
	job := update.Job
	if err := job.Validate(); err != nil {
		return updateResponse{}, fmt.Errorf("%w: invalid job for HTTP response: %v", quoteservice.ErrInconsistentResult, err)
	}

	result := updateResponse{
		ID: job.ID, Pair: job.Pair.String(), Status: string(job.Status), CreatedAt: utcTime(job.CreatedAt),
	}

	switch job.Status {
	case domain.JobPending, domain.JobProcessing:
		if update.Quote != nil {
			return updateResponse{}, fmt.Errorf("%w: active job contains quote", quoteservice.ErrInconsistentResult)
		}
	case domain.JobSucceeded:
		if update.Quote == nil || job.CompletedAt == nil {
			return updateResponse{}, fmt.Errorf("%w: succeeded job missing quote/completion", quoteservice.ErrInconsistentResult)
		}

		q := update.Quote

		if err := q.Validate(); err != nil || q.UpdateID != job.ID || q.Pair != job.Pair {
			return updateResponse{}, fmt.Errorf("%w: succeeded job quote mismatch: %v", quoteservice.ErrInconsistentResult, err)
		}

		result.CompletedAt = stringPtr(utcTime(*job.CompletedAt))
		result.Price = stringPtr(q.Price.String())
		result.UpdatedAt = stringPtr(utcTime(q.UpdatedAt))
		result.SourceDate = stringPtr(q.SourceDate.String())
	case domain.JobFailed:
		if job.CompletedAt == nil || job.Failure == nil || update.Quote != nil {
			return updateResponse{}, fmt.Errorf("%w: failed job response mismatch", quoteservice.ErrInconsistentResult)
		}

		result.CompletedAt = stringPtr(utcTime(*job.CompletedAt))
		result.Error = &updateFailureResponse{Code: string(job.Failure.Code), Message: job.Failure.Message}
	default:
		return updateResponse{}, fmt.Errorf("%w: unsupported job status", quoteservice.ErrInconsistentResult)
	}

	return result, nil
}

func stringPtr(s string) *string { return &s }
