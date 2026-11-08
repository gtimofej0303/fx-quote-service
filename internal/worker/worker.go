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

// Queue is the existing PostgreSQL job-queue contract, kept independent of SQL.
type Queue interface {
	ClaimNext(context.Context, time.Duration, int) (domain.UpdateJob, error)
	ScheduleRetry(context.Context, int64, string, time.Time) error
	SaveFailure(context.Context, int64, string, domain.UpdateFailure) error
	FailExhausted(context.Context, int, int) (int64, error)
}

// Processor is implemented by the existing quote service.
type Processor interface {
	ProcessJob(context.Context, domain.UpdateJob, quoteservice.RateProvider) (quoteservice.ProcessResult, error)
}

// Worker consumes database jobs; at most one provider request is in flight.
type Worker struct {
	queue        Queue
	processor    Processor
	provider     quoteservice.RateProvider
	logger       *slog.Logger
	pollInterval time.Duration
	now          func() time.Time
}

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

// Run polls the durable queue. Cancellation stops new claims and cancels active work.
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

// runOne finalizes expired/exhausted jobs and claims at most one due job.
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

func (w *Worker) processClaimed(ctx context.Context, job domain.UpdateJob) error {
	// Provider has an 8s timeout. The 20s worker budget also includes SaveSuccess.
	// A 30s database lease is deliberately longer than this entire attempt.
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)

	result, err := w.processor.ProcessJob(attemptCtx, job, w.provider)

	cancel()

	if ctx.Err() != nil {
		// On shutdown, do not write using an obsolete or canceled context.
		// The tokenized lease makes recovery safe after the lease expires.
		return nil
	}

	if err != nil {
		if errors.Is(err, quoteservice.ErrLeaseLost) {
			w.logger.Warn("quote update lease lost", "update_id", job.ID)

			return nil
		}

		// Do not disguise database/unknown errors as provider failures. If the
		// attempt did not commit, the leased row becomes claimable after expiry.
		return fmt.Errorf("process quote update %d: %w", job.ID, err)
	}

	if result.Failure == nil {
		if result.Retryable {
			return fmt.Errorf("invalid processing result for update %d: retry without failure", job.ID)
		}

		w.logger.Info("quote update succeeded", "update_id", job.ID, "attempt", job.Attempts)

		return nil
	}

	finishCtx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()

	if result.Retryable && job.Attempts < maxAttempts {
		delay := retryDelay(job.Attempts)
		next := w.now().UTC().Add(delay)

		err = w.queue.ScheduleRetry(finishCtx, job.ID, job.Lease.Token, next)
		if err == nil {
			w.logger.Warn("quote update scheduled for retry", "update_id", job.ID,
				"attempt", job.Attempts, "delay", delay, "code", result.Failure.Code)
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
