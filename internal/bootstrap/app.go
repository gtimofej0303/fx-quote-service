package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gtimofej0303/fx-quote-service/internal/config"
	httptransport "github.com/gtimofej0303/fx-quote-service/internal/handler/http"
	"github.com/gtimofej0303/fx-quote-service/internal/provider/frankfurter"
	"github.com/gtimofej0303/fx-quote-service/internal/repository/postgres"
	quoteservice "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
	"github.com/gtimofej0303/fx-quote-service/internal/worker"
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	const workerCount = 2 // Number of concurrent worker loops in this application process.

	db, err := postgres.Open(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	startupCtx, cancelStartup := context.WithTimeout(ctx, 10*time.Second)
	pingErr := db.PingContext(startupCtx)
	cancelStartup()
	if pingErr != nil {
		return fmt.Errorf("connect to postgres: %w", pingErr)
	}

	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}
	defer listener.Close()

	repo := postgres.New(db)

	quoteSvc, err := quoteservice.New(repo)
	if err != nil {
		return fmt.Errorf("initialize quote service: %w", err)
	}

	// Initialize all workers before starting goroutines, so startup is all-or-nothing.
	workers := make([]*worker.Worker, 0, workerCount)

	for i := 0; i < workerCount; i++ {
		backgroundWorker, err := worker.New(repo, quoteSvc, frankfurter.New(), logger)

		if err != nil {
			return fmt.Errorf("initialize quote worker %d: %w", i+1, err)
		}

		workers = append(workers, backgroundWorker)
	}

	ready := &atomic.Bool{}
	handler := httptransport.NewRouter(db, ready, logger, quoteSvc)
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()

	type workerExit struct {
		id  int
		err error
	}

	serverErrors := make(chan error, 1)
	workerErrors := make(chan workerExit, workerCount)

	go func() { serverErrors <- server.Serve(listener) }()

	for i, backgroundWorker := range workers {
		go func(id int, w *worker.Worker) {
			workerErrors <- workerExit{id: id, err: w.Run(workerCtx)}
		}(i+1, backgroundWorker)
	}

	ready.Store(true)
	logger.Info("HTTP server and quote workers started", "address", listener.Addr().String(), "worker_count", workerCount)

	var runErr error

	serverStopped := false
	workersStopped := 0

	select {
	case err := <-serverErrors:
		serverStopped = true

		if errors.Is(err, http.ErrServerClosed) {
			runErr = errors.New("HTTP server closed unexpectedly")
		} else {
			runErr = fmt.Errorf("HTTP server: %w", err)
		}
	case result := <-workerErrors:
		workersStopped++

		if result.err != nil {
			runErr = fmt.Errorf("quote worker %d: %w", result.id, result.err)
		} else if ctx.Err() == nil {
			runErr = fmt.Errorf("quote worker %d stopped unexpectedly", result.id)
		}
	case <-ctx.Done():
		logger.Info("shutdown requested")
	}

	ready.Store(false)
	stopWorker() // Stop all claims and cancel all in-flight work.

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	shutdownErr := server.Shutdown(shutdownCtx)
	cancelShutdown()

	if shutdownErr != nil {
		_ = server.Close()

		runErr = errors.Join(runErr, fmt.Errorf("shutdown HTTP server: %w", shutdownErr))
	}

	if !serverStopped {
		if err := <-serverErrors; !errors.Is(err, http.ErrServerClosed) {
			runErr = errors.Join(runErr, fmt.Errorf("HTTP server stopped: %w", err))
		}
	}

	// One completion signal per worker; none can be forgotten on shutdown.
	for workersStopped < workerCount {
		result := <-workerErrors
		workersStopped++

		if result.err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("quote worker %d stopped: %w", result.id, result.err))
		}
	}

	logger.Info("HTTP server and all quote workers stopped", "worker_count", workerCount)

	return runErr
}
