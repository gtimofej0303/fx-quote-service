package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gtimofej0303/fx-quote-service/internal/bootstrap"
	"github.com/gtimofej0303/fx-quote-service/internal/config"
	"github.com/gtimofej0303/fx-quote-service/internal/observability"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration:", err)
		os.Exit(1)
	}

	logger := observability.NewLogger(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := bootstrap.Run(ctx, cfg, logger); err != nil {
		logger.Error("application stopped with error", "error", err)
		os.Exit(1)
	}
}
