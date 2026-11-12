package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
	quoteservice "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
)

const (
	pollInterval       = time.Second
	leaseDuration      = 30 * time.Second
	attemptTimeout     = 20 * time.Second
	databaseTimeout    = 3 * time.Second
	maxAttempts        = 3
	exhaustedBatchSize = 100
)

// Queue lists the job operations a worker needs.
type Queue interface {
	ClaimNext(context.Context, time.Duration, int) (domain.UpdateJob, error)
	ScheduleRetry(context.Context, int64, string, time.Time) error
	SaveFailure(context.Context, int64, string, domain.UpdateFailure) error
	FailExhausted(context.Context, int, int) (int64, error)
}

// Processor runs jobs through the quote service.
type Processor interface {
	// ProcessJob processes an update job using the given rate provider.
	ProcessJob(context.Context, domain.UpdateJob, quoteservice.RateProvider) (quoteservice.ProcessResult, error)
}

// Worker runs jobs with one provider request at a time.
type Worker struct {
	queue        Queue
	processor    Processor
	provider     quoteservice.RateProvider
	logger       *slog.Logger
	pollInterval time.Duration
	now          func() time.Time
}

// New checks the required dependencies and creates a worker.
func New(queue Queue, processor Processor, provider quoteservice.RateProvider, logger *slog.Logger) (*Worker, error) {
	if queue == nil || processor == nil || provider == nil || logger == nil {
		return nil, errors.New("worker requires queue, processor, provider and logger")
	}

	return &Worker{
		queue:        queue,
		processor:    processor,
		provider:     provider,
		logger:       logger,
		pollInterval: pollInterval,
		now:          time.Now,
	}, nil
}

// Run checks the saved job queue. On cancel, it stops taking jobs and cancels work.
func (w *Worker) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return nil
	}

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	w.logger.Info("quote worker started", "poll_interval", w.pollInterval, "lease", leaseDuration, "max_attempts", maxAttempts)
	defer w.logger.Info("quote worker stopped")

	for {
		if ctx.Err() != nil {
			return nil
		}

		if err := w.runOne(ctx); err != nil && ctx.Err() == nil {
			w.logger.Error("quote worker cycle failed", "error", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// runOne closes expired jobs with no tries left and takes at most one ready job.
func (w *Worker) runOne(ctx context.Context) error {
	maintenanceCtx, cancel := context.WithTimeout(ctx, databaseTimeout)

	count, err := w.queue.FailExhausted(maintenanceCtx, maxAttempts, exhaustedBatchSize)

	cancel()

	if err != nil {
		return fmt.Errorf("finalize exhausted jobs: %w", err)
	}

	if count > 0 {
		w.logger.Warn("exhausted quote jobs finalized", "count", count)
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	claimCtx, cancel := context.WithTimeout(ctx, databaseTimeout)

	job, err := w.queue.ClaimNext(claimCtx, leaseDuration, maxAttempts)

	cancel()

	if errors.Is(err, quoteservice.ErrNoClaimableJob) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("claim quote update: %w", err)
	}

	if job.Lease == nil || job.Status != domain.JobProcessing || job.Attempts < 1 || job.Attempts > maxAttempts {
		return fmt.Errorf("invalid claimed job %d", job.ID)
	}

	w.logger.Info("quote update claimed", "update_id", job.ID, "pair", job.Pair, "attempt", job.Attempts)

	return w.processClaimed(ctx, job)
}

// processClaimed processes a job that the worker has claimed.
func (w *Worker) processClaimed(ctx context.Context, job domain.UpdateJob) error {
	// The provider has 8 seconds. The worker has 20 seconds, including SaveSuccess.
	// The database lease lasts 30 seconds, longer than one try.
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)

	result, err := w.processor.ProcessJob(attemptCtx, job, w.provider)

	cancel()

	if ctx.Err() != nil {
		// When stopping, do not save changes with an old or canceled context.
		// The lease token lets another worker safely take over after the lease ends.
		return nil
	}

	if err != nil {
		if errors.Is(err, quoteservice.ErrLeaseLost) {
			w.logger.Warn("quote update lease lost", "update_id", job.ID)

			return nil
		}

		// Keep database and unknown errors separate from provider errors.
		// If changes were not saved, another worker can take the job when the lease ends.
		return fmt.Errorf("process quote update %d: %w", job.ID, err)
	}

	// If there is no failure, the job was processed successfully.
	if result.Failure == nil {
		// A retry needs a failure to explain why it is needed.
		if result.Retryable {
			return fmt.Errorf("invalid processing result for update %d: retry without failure", job.ID)
		}

		w.logger.Info("quote update succeeded", "update_id", job.ID, "attempt", job.Attempts)

		return nil
	}

	// Log the provider error before saving the final status.
	if result.Diagnostic != nil {
		w.logger.Warn("quote provider attempt failed", "update_id", job.ID,
			"pair", job.Pair, "attempt", job.Attempts, "code", result.Failure.Code,
			"retryable", result.Retryable, "provider_error", result.Diagnostic)
	}

	finishCtx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()

	if result.Retryable && job.Attempts < maxAttempts {
		now := w.now().UTC()
		next := now.Add(retryDelay(job.Attempts))
		if result.RetryNotBefore.After(next) {
			next = result.RetryNotBefore.UTC()
		}

		err = w.queue.ScheduleRetry(finishCtx, job.ID, job.Lease.Token, next)
		if err == nil {
			w.logger.Warn("quote update scheduled for retry", "update_id", job.ID,
				"attempt", job.Attempts, "delay", next.Sub(now), "next_attempt_at", next,
				"code", result.Failure.Code)
		}
	} else {
		err = w.queue.SaveFailure(finishCtx, job.ID, job.Lease.Token, *result.Failure)
		if err == nil {
			w.logger.Warn("quote update failed", "update_id", job.ID,
				"attempt", job.Attempts, "code", result.Failure.Code)
		}
	}

	if errors.Is(err, quoteservice.ErrLeaseLost) {
		w.logger.Warn("quote update lease lost while finalizing", "update_id", job.ID)

		return nil
	}

	if err != nil {
		return fmt.Errorf("finalize quote update %d: %w", job.ID, err)
	}

	return nil
}
