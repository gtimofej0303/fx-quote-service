//go:build integration

package integration_test

import (
	"os"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gtimofej0303/fx-quote-service/internal/repository/postgres"
)

// TestOpenWithPostgresEnvironment checks that a database connection works with PG* settings.
func TestOpenWithPostgresEnvironment(t *testing.T) {
	_, _, ctx := openTestDatabase(t)
	cfg, err := pgx.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGHOST", cfg.Host)
	t.Setenv("PGPORT", strconv.Itoa(int(cfg.Port)))
	t.Setenv("PGUSER", cfg.User)
	t.Setenv("PGPASSWORD", cfg.Password)
	t.Setenv("PGDATABASE", cfg.Database)
	t.Setenv("PGSSLMODE", "disable")
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGSERVICEFILE", "")

	db, err := postgres.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect using literal PG* environment values: %v", err)
	}
	var databaseName string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	if databaseName != cfg.Database {
		t.Fatalf("connected to %q, expected %q", databaseName, cfg.Database)
	}
}
