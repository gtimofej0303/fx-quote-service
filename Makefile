# Local developer commands. Run from Git Bash on Windows or from a POSIX shell.
# PostgreSQL-backed suites share a database and must never run in parallel.
SHELL := bash
.SHELLFLAGS := -euo pipefail -c
.NOTPARALLEL:
.DEFAULT_GOAL := help

GO ?= go
COMPOSE ?= docker compose
GOOS := $(shell $(GO) env GOOS 2>/dev/null)
ifeq ($(GOOS),windows)
BIN_EXT := .exe
else
BIN_EXT :=
endif

.PHONY: help fmt fmt-check test-unit vet build check \
        migrate-test test-integration test-e2e test-all \
        up down ps logs

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

fmt:
	@git ls-files -z --cached --others --exclude-standard -- '*.go' | xargs -0 -r gofmt -w

fmt-check:
	@unformatted=$$(git ls-files -z --cached --others --exclude-standard -- '*.go' | xargs -0 -r gofmt -l); \
	  if [ -n "$$unformatted" ]; then \
	    printf 'Go files require gofmt:\n%s\n' "$$unformatted"; \
	    exit 1; \
	  fi

test-unit:
	@$(GO) test -count=1 ./...

vet:
	@$(GO) vet ./...

build:
	@mkdir -p bin
	@$(GO) build -o bin/api$(BIN_EXT) ./cmd/api
	@$(GO) build -o bin/migrate$(BIN_EXT) ./cmd/migrate

check: fmt-check test-unit vet build

# A migration changes schema. Never point TEST_DATABASE_URL at application data.
migrate-test:
	@test -n "$${TEST_DATABASE_URL:-}" || { echo 'Set TEST_DATABASE_URL to a dedicated *_test database'; exit 1; }
	@DATABASE_URL="$$TEST_DATABASE_URL" $(GO) run ./cmd/migrate up

test-integration:
	@test -n "$${TEST_DATABASE_URL:-}" || { echo 'Set TEST_DATABASE_URL to a dedicated *_test database'; exit 1; }
	@$(GO) test -tags=integration -count=1 -v ./tests/integration

test-e2e:
	@test -n "$${TEST_DATABASE_URL:-}" || { echo 'Set TEST_DATABASE_URL to a dedicated *_test database'; exit 1; }
	@$(GO) test -tags=e2e -count=1 -v ./tests/e2e

# .NOTPARALLEL prevents accidental concurrent TRUNCATE by make -j.
# It cannot prevent a second, independent terminal from running another suite.
test-all: check test-integration test-e2e

up:
	@$(COMPOSE) up --build -d

down:
	@$(COMPOSE) down

ps:
	@$(COMPOSE) ps -a

logs:
	@$(COMPOSE) logs --tail=100 -f postgres migrate app
