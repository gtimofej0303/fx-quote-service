package quote

import (
	"fmt"
	"strings"
	"time"
)

type JobStatus string

const (
	JobPending    JobStatus = "pending"
	JobProcessing JobStatus = "processing"
	JobSucceeded  JobStatus = "succeeded"
	JobFailed     JobStatus = "failed"
)

func (s JobStatus) Valid() bool {
	switch s {
	case JobPending, JobProcessing, JobSucceeded, JobFailed:
		return true
	default:
		return false
	}
}

func (s JobStatus) IsTerminal() bool { return s == JobSucceeded || s == JobFailed }

// ValidateStatusTransition describes business state changes, not database
// concurrency. An expired lease can be reassigned while status stays processing.
func ValidateStatusTransition(from, to JobStatus) error {
	if !from.Valid() || !to.Valid() {
		return fmt.Errorf("%w: %q -> %q", ErrInvalidJobStatus, from, to)
	}

	if (from == JobPending && to == JobProcessing) ||
		(from == JobProcessing && (to == JobPending || to == JobSucceeded || to == JobFailed)) {
		return nil
	}

	return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
}

type FailureCode string

const (
	FailureProviderTimeout         FailureCode = "provider_timeout"
	FailureProviderUnavailable     FailureCode = "provider_unavailable"
	FailureProviderRateLimited     FailureCode = "provider_rate_limited"
	FailureProviderRateNotFound    FailureCode = "provider_rate_not_found"
	FailureProviderRejectedRequest FailureCode = "provider_rejected_request"
	FailureProviderInvalidResponse FailureCode = "provider_invalid_response"
	FailureProcessingTimeout       FailureCode = "processing_timeout"
)

func (c FailureCode) Valid() bool {
	switch c {
	case FailureProviderTimeout, FailureProviderUnavailable, FailureProviderRateLimited,
		FailureProviderRateNotFound, FailureProviderRejectedRequest,
		FailureProviderInvalidResponse, FailureProcessingTimeout:
		return true
	default:
		return false
	}
}

type UpdateFailure struct {
	Code    FailureCode
	Message string
}

func (f UpdateFailure) Validate() error {
	if !f.Code.Valid() || strings.TrimSpace(f.Message) == "" {
		return fmt.Errorf("%w: allowed code and nonempty message required", ErrInvalidFailure)
	}

	return nil
}

// ValidateIdempotencyKey validates a PROVIDED key. Omission is represented
// by nil, not by an empty string. Keys are case-sensitive and never trimmed.
func ValidateIdempotencyKey(key string) error {
	if len(key) == 0 || len(key) > 128 {
		return fmt.Errorf("%w: expected length 1..128", ErrInvalidIdempotencyKey)
	}

	for i := 0; i < len(key); i++ {
		c := key[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '.' || c == '_' || c == ':' || c == '-' {
			continue
		}

		return fmt.Errorf("%w: unexpected character", ErrInvalidIdempotencyKey)
	}

	return nil
}

// Lease represents an exclusive, renewable claim on a processing job.
// The repository is responsible for checking token ownership atomically.
type Lease struct {
	Token string // UUID string from the database claim operation
	Until time.Time
}

// UpdateJob mirrors the persisted state needed by later repository/service
// stages. Public fields make hydration straightforward. SQL operations enforce atomic transitions.
type UpdateJob struct {
	ID             int64
	Pair           CurrencyPair
	Status         JobStatus
	IdempotencyKey *string
	Attempts       int
	NextAttemptAt  time.Time
	CreatedAt      time.Time
	CompletedAt    *time.Time
	Lease          *Lease
	Failure        *UpdateFailure
}

func NewPendingJob(id int64, pair CurrencyPair, createdAt, nextAttemptAt time.Time, key *string) (UpdateJob, error) {
	j := UpdateJob{
		ID:            id,
		Pair:          pair,
		Status:        JobPending,
		CreatedAt:     createdAt.UTC(),
		NextAttemptAt: nextAttemptAt.UTC(),
	}

	if key != nil {
		copy := *key
		j.IdempotencyKey = &copy
	}

	if err := j.Validate(); err != nil {
		return UpdateJob{}, err
	}

	return j, nil
}

func (j UpdateJob) Validate() error {
	if j.ID <= 0 {
		return fmt.Errorf("%w: ID must be positive", ErrInvalidJob)
	}

	if err := j.Pair.Validate(); err != nil {
		return fmt.Errorf("%w: pair: %w", ErrInvalidJob, err)
	}

	if !j.Status.Valid() {
		return fmt.Errorf("%w: %w", ErrInvalidJob, ErrInvalidJobStatus)
	}

	if j.IdempotencyKey != nil {
		if err := ValidateIdempotencyKey(*j.IdempotencyKey); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidJob, err)
		}
	}

	if j.Attempts < 0 || j.CreatedAt.IsZero() || j.NextAttemptAt.IsZero() {
		return fmt.Errorf("%w: invalid attempts or missing timestamps", ErrInvalidJob)
	}

	if j.CompletedAt != nil && (j.CompletedAt.IsZero() || j.CompletedAt.Before(j.CreatedAt)) {
		return fmt.Errorf("%w: invalid completion timestamp", ErrInvalidJob)
	}

	if j.Lease != nil && (j.Lease.Token == "" || j.Lease.Until.IsZero()) {
		return fmt.Errorf("%w: invalid lease", ErrInvalidJob)
	}

	if j.Failure != nil {
		if err := j.Failure.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidJob, err)
		}
	}

	switch j.Status {
	case JobPending:
		if j.CompletedAt != nil || j.Lease != nil || j.Failure != nil {
			return fmt.Errorf("%w: pending job must not have completion, lease or failure", ErrInvalidJob)
		}
	case JobProcessing:
		if j.Attempts < 1 || j.CompletedAt != nil || j.Lease == nil || j.Failure != nil {
			return fmt.Errorf("%w: processing job requires attempts and lease only", ErrInvalidJob)
		}
	case JobSucceeded:
		if j.CompletedAt == nil || j.Lease != nil || j.Failure != nil {
			return fmt.Errorf("%w: succeeded job requires completion without lease or failure", ErrInvalidJob)
		}
	case JobFailed:
		if j.CompletedAt == nil || j.Lease != nil || j.Failure == nil {
			return fmt.Errorf("%w: failed job requires completion and failure without lease", ErrInvalidJob)
		}
	}

	return nil
}
