# Local developer commands. Run from Git Bash on Windows or from a POSIX shell.
# PostgreSQL-backed suites share a database and must never run in parallel.
# Use Bash for commands.
SHELL := bash
# Stop on errors, missing variables.
.SHELLFLAGS := -euo pipefail -c
# Run targets one at a time because tests share a database.
.NOTPARALLEL:
# Show help by default.
.DEFAULT_GOAL := help

# Allow a custom Go command.
GO ?= go
# Allow a custom Compose command.
COMPOSE ?= docker compose
# Read the target system from Go.
GOOS := $(shell $(GO) env GOOS 2>/dev/null)
# Check for a Windows build.
ifeq ($(GOOS),windows)
# Set the program extension for this system.
BIN_EXT := .exe
# Use this setting on other systems.
else
# Set the program extension for this system.
BIN_EXT :=
endif

# Treat these names as commands, not files.
.PHONY: help fmt fmt-check test-unit vet build check \
        migrate-test test-integration test-e2e test-all \
        up down ps logs

# Print available commands.
help:
	@printf '%s\n' \
	  'make check             - gofmt check, unit tests, go vet and builds' \
	  'make fmt               - format tracked and untracked Go files' \
	  'make fmt-check         - fail if any Go file needs gofmt' \
	  'make test-unit         - unit tests (no PostgreSQL)' \
	  'make migrate-test      - migrate the dedicated TEST_DATABASE_URL' \
	  'make test-integration  - run real PostgreSQL integration tests' \
	  'make test-e2e          - run real HTTP/PostgreSQL/worker E2E tests' \
	  'make test-all          - check + integration + E2E, sequentially' \
	  'make up                - build and start Docker Compose in background' \
	  'make down              - stop Compose; keep PostgreSQL volume' \
	  'make ps                - list Compose services' \
	  'make logs              - follow Compose logs'

# Format tracked and new Go files.
fmt:
	@git ls-files -z --cached --others --exclude-standard -- '*.go' | xargs -0 -r gofmt -w

# Fail if Go files need formatting.
fmt-check:
# Find files that need formatting; print their names and fail if any are found.
	@unformatted=$$(git ls-files -z --cached --others --exclude-standard -- '*.go' | xargs -0 -r gofmt -l); \
	  if [ -n "$$unformatted" ]; then \
	    printf 'Go files require gofmt:\n%s\n' "$$unformatted"; \
	    exit 1; \
	  fi

# Run unit tests with fresh results.
test-unit:
	@$(GO) test -count=1 ./...

# Check Go code for common mistakes.
vet:
	@$(GO) vet ./...

build:
# Create the output folder if needed.
	@mkdir -p bin
# Build this program in the bin folder.
	@$(GO) build -o bin/api$(BIN_EXT) ./cmd/api
# Build this program in the bin folder.
	@$(GO) build -o bin/migrate$(BIN_EXT) ./cmd/migrate

# Run format checks, unit tests, code checks, and builds in order.
check: fmt-check test-unit vet build

# A migration changes schema. Never point TEST_DATABASE_URL at application data.
# Apply migrations to the separate test database.
migrate-test:
# Stop if TEST_DATABASE_URL is missing.
	@test -n "$${TEST_DATABASE_URL:-}" || { echo 'Set TEST_DATABASE_URL to a dedicated *_test database'; exit 1; }
# Run migrations against the separate test database.
	@DATABASE_URL="$$TEST_DATABASE_URL" $(GO) run ./cmd/migrate up

# Run tests with real PostgreSQL.
test-integration:
# Stop if TEST_DATABASE_URL is missing.
	@test -n "$${TEST_DATABASE_URL:-}" || { echo 'Set TEST_DATABASE_URL to a dedicated *_test database'; exit 1; }
	@$(GO) test -tags=integration -count=1 -v ./tests/integration

# Test HTTP, database work, and workers.
test-e2e:
# Stop if TEST_DATABASE_URL is missing.
	@test -n "$${TEST_DATABASE_URL:-}" || { echo 'Set TEST_DATABASE_URL to a dedicated *_test database'; exit 1; }
	@$(GO) test -tags=e2e -count=1 -v ./tests/e2e

# .NOTPARALLEL prevents accidental concurrent TRUNCATE by make -j.
# It cannot prevent a second, independent terminal from running another suite.
# Run all checks and test suites in order.
test-all: check test-integration test-e2e

# Build and start containers in the background.
up:
	@$(COMPOSE) up --build -d

# Stop containers and keep database files.
down:
	@$(COMPOSE) down

# Show all containers and their status.
ps:
	@$(COMPOSE) ps -a

# Show recent logs and follow new messages.
logs:
	@$(COMPOSE) logs --tail=100 -f postgres migrate app
