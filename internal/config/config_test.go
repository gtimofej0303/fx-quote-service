package config

import "testing"

func TestLoad(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/fxquotes?sslmode=disable")
	t.Setenv("HTTP_ADDR", ":8080")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("unexpected HTTPAddr: %q", cfg.HTTPAddr)
	}
}

func TestLoadPostgresEnvironment(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("PGHOST", "localhost")
	t.Setenv("PGUSER", "review")
	t.Setenv("PGDATABASE", "quotes")
	t.Setenv("PGPASSWORD", "literal@pass/with%40$#characters")
	t.Setenv("LOG_LEVEL", "info")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "" {
		t.Fatal("PG* configuration must not be converted to a URL")
	}
}

func TestLoadRejectsIncompletePostgresEnvironment(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	for _, missing := range []string{"PGHOST", "PGUSER", "PGDATABASE"} {
		t.Run(missing, func(t *testing.T) {
			t.Setenv("PGHOST", "localhost")
			t.Setenv("PGUSER", "review")
			t.Setenv("PGDATABASE", "quotes")
			t.Setenv(missing, "")
			if _, err := Load(); err == nil {
				t.Fatalf("expected error for missing %s", missing)
			}
		})
	}
}

func TestLoadRejectsUnknownLogLevel(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/db")
	t.Setenv("LOG_LEVEL", "trace")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for unknown LOG_LEVEL")
	}
}
