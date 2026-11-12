package quote

import (
	"context"
	"errors"
	"fmt"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
)

type Service struct {
	repository Repository
}

func New(repository Repository) (*Service, error) {
	if repository == nil {
		return nil, ErrMissingRepository
	}

	return &Service{repository: repository}, nil
}

// RequestUpdate checks input and creates a job or returns an existing one.
func (s *Service) RequestUpdate(ctx context.Context, rawPair string, key *string) (domain.UpdateJob, error) {
	pair, err := domain.ParseCurrencyPair(rawPair)
	if err != nil {
		return domain.UpdateJob{}, fmt.Errorf("request update: %w", err)
	}

	if key != nil {
		if err := domain.ValidateIdempotencyKey(*key); err != nil {
			return domain.UpdateJob{}, fmt.Errorf("request update: %w", err)
		}
	}

	job, err := s.repository.CreateJob(ctx, pair, key)
	if err != nil {
		return domain.UpdateJob{}, fmt.Errorf("request update: %w", err)
	}

	if err := job.Validate(); err != nil {
		return domain.UpdateJob{}, fmt.Errorf("%w: created job: %v", ErrInconsistentResult, err)
	}

	if job.Pair != pair {
		return domain.UpdateJob{}, fmt.Errorf("%w: created job has pair %s, expected %s", ErrInconsistentResult, job.Pair, pair)
	}

	return job, nil
}

// Update holds a job and its quote if it succeeded.
type Update struct {
	Job   domain.UpdateJob
	Quote *domain.Quote
}

// GetUpdate returns a job and its result, if ready.
func (s *Service) GetUpdate(ctx context.Context, id int64) (Update, error) {
	if id <= 0 {
		return Update{}, ErrInvalidUpdateID
	}

	job, err := s.repository.GetJob(ctx, id)
	if err != nil {
		return Update{}, fmt.Errorf("get update %d: %w", id, err)
	}

	if err := job.Validate(); err != nil {
		return Update{}, fmt.Errorf("%w: job %d: %v", ErrInconsistentResult, id, err)
	}

	if job.ID != id {
		return Update{}, fmt.Errorf("%w: requested job %d, got %d", ErrInconsistentResult, id, job.ID)
	}

	result := Update{Job: job}
	if job.Status != domain.JobSucceeded {
		return result, nil
	}

	q, err := s.repository.GetQuoteByUpdateID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrQuoteNotFound) {
			// A successful job must have a quote.
			// A missing quote is a data error. Do not return HTTP 404 for this job.
			return Update{}, fmt.Errorf("%w: succeeded update %d has no quote: %v", ErrInconsistentResult, id, err)
		}

		return Update{}, fmt.Errorf("get quote for succeeded update %d: %w", id, err)
	}

	if err := q.Validate(); err != nil {
		return Update{}, fmt.Errorf("%w: quote for update %d: %v", ErrInconsistentResult, id, err)
	}

	if q.UpdateID != id || q.Pair != job.Pair {
		return Update{}, fmt.Errorf("%w: quote does not match job %d", ErrInconsistentResult, id)
	}

	result.Quote = &q

	return result, nil
}

// GetLatestQuote returns the latest saved quote for a currency pair.
func (s *Service) GetLatestQuote(ctx context.Context, rawPair string) (domain.Quote, error) {
	pair, err := domain.ParseCurrencyPair(rawPair)
	if err != nil {
		return domain.Quote{}, fmt.Errorf("get latest quote: %w", err)
	}

	q, err := s.repository.GetLatestQuote(ctx, pair)
	if err != nil {
		return domain.Quote{}, fmt.Errorf("get latest quote for %s: %w", pair, err)
	}

	if err := q.Validate(); err != nil {
		return domain.Quote{}, fmt.Errorf("%w: latest quote: %v", ErrInconsistentResult, err)
	}

	if q.Pair != pair {
		return domain.Quote{}, fmt.Errorf("%w: latest quote pair %s, expected %s", ErrInconsistentResult, q.Pair, pair)
	}

	return q, nil
}
