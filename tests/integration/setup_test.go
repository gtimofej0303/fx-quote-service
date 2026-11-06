//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gtimofej0303/fx-quote-service/internal/repository/postgres"
)

// Each test uses the dedicated database ending in _test. Tests are sequential.
func openTestDatabase(t *testing.T) (*sql.DB, *postgres.Repository, context.Context) {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}

	db, err := postgres.Open(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("test database unavailable: %v", err)
	}

	var databaseName string

	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}

	if !strings.HasSuffix(databaseName, "_test") {
		t.Fatalf("refuse to truncate database %q: name must end with _test", databaseName)
	}

	// A failed query here usually means migration 00002 has not been applied.
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM quote_updates`).Scan(new(int)); err != nil {
		t.Fatalf("run migrations against the TEST database first: %v", err)
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE quotes, quote_updates RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("reset isolated test database: %v", err)
	}

	return db, postgres.New(db), ctx
}

func mustEqual[T comparable](t *testing.T, got, want T, label string) {
	t.Helper()
	
	if got != want {
		t.Fatal(fmt.Sprintf("%s: got %v, want %v", label, got, want))
	}
}
