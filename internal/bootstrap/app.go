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
	"github.com/gtimofej0303/fx-quote-service/internal/repository/postgres"
	quoteservice "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
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

	ready := &atomic.Bool{}
	handler := httptransport.NewRouter(db, ready, logger, quoteSvc)
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	ready.Store(true)

	go func() {
		serverErrors <- server.Serve(listener)
	}()
	logger.Info("HTTP server started", "address", listener.Addr().String())

	select {
	case err := <-serverErrors:
		ready.Store(false)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("HTTP server: %w", err)

	case <-ctx.Done():
		ready.Store(false)
		logger.Info("shutdown requested")

		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelShutdown()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}

		if err := <-serverErrors; !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP server stopped unexpectedly: %w", err)
		}
		logger.Info("HTTP server stopped")

		return nil
	}
}
