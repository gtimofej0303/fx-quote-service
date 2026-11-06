package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/gtimofej0303/fx-quote-service/internal/config"
	"github.com/gtimofej0303/fx-quote-service/internal/observability"
	"github.com/gtimofej0303/fx-quote-service/internal/repository/postgres"
	"github.com/gtimofej0303/fx-quote-service/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migration failed:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := observability.NewLogger(cfg.LogLevel)

	db, err := postgres.Open(cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}
	
	var errMigration error

	switch {
	case len(os.Args) == 1 || (len(os.Args) == 2 && os.Args[1] == "up"):
		errMigration = goose.UpContext(ctx, db, ".")

	case len(os.Args) == 2 && os.Args[1] == "down":
		errMigration = goose.DownContext(ctx, db, ".")

	default:
		return fmt.Errorf("usage: migrate [up|down]")
	}

	if errMigration != nil {
		return fmt.Errorf("run migration: %w", errMigration)
	}

	logger.Info("database migrations applied")
	return nil
}
