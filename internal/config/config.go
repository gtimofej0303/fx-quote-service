package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
)

type Config struct {
	HTTPAddr    string
	DatabaseURL string
	LogLevel    slog.Level
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:    envOrDefault("HTTP_ADDR", ":8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL must be set")
	}
	if _, _, err := net.SplitHostPort(cfg.HTTPAddr); err != nil {
		return Config{}, fmt.Errorf("invalid HTTP_ADDR %q: %w", cfg.HTTPAddr, err)
	}

	switch strings.ToLower(envOrDefault("LOG_LEVEL", "info")) {
	case "debug":
		cfg.LogLevel = slog.LevelDebug
	case "info":
		cfg.LogLevel = slog.LevelInfo
	case "warn":
		cfg.LogLevel = slog.LevelWarn
	case "error":
		cfg.LogLevel = slog.LevelError
	default:
		return Config{}, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error")
	}
	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
