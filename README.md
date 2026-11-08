# FX Quote Service

[![CI](https://github.com/gtimofej0303/fx-quote-service/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/gtimofej0303/fx-quote-service/actions/workflows/ci.yml)

A REST API for currency exchange rates. You can request a rate update, check its status, and get the latest saved rate. The update runs in the background, so the API does not wait for the external service.

**Stack:** Go 1.27, PostgreSQL 17, pgx, Goose migrations, Docker Compose, and the [Frankfurter API](https://frankfurter.dev/). GitHub Actions runs the CI checks.

**Currencies:** USD, EUR, and EGP. All six directions are supported (for example, `EUR/EGP` and `EGP/EUR`). `EUR/EGP` means how many EGP you get for 1 EUR.

The API has three main endpoints:

| Method | Endpoint | Purpose |
| --- | --- | --- |
| `POST` | `/api/v1/quote-updates` | Request a new rate update. |
| `GET` | `/api/v1/quote-updates/{id}` | Check an update and get its result. |
| `GET` | `/api/v1/quotes/latest?pair=EUR%2FEGP` | Get the latest saved rate for a pair. |

## Quick Start

You need **Git** and **Docker with Docker Compose**. You do not need to install Go or PostgreSQL. Run the commands below in Bash (Git Bash on Windows).

**1. Clone the project.**

```bash
git clone https://github.com/gtimofej0303/fx-quote-service.git
cd fx-quote-service
```

**2. Create your local settings.** The example file has values for local development.

```bash
cp .env.example .env
```

**3. Start the service.** Docker builds the app, starts PostgreSQL, and runs database migrations.

```bash
docker compose up --build -d
```

**4. Check that it works.**

```bash
docker compose ps
curl http://localhost:8080/health/ready
```

The health request should return `{"status":"ok"}`.

**5. Run unit tests and static checks.** These run inside Docker:

```bash
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$(pwd -W 2>/dev/null || pwd):/app" -w /app golang:1.27.3-alpine \
  sh -c 'go test ./... && go vet ./...'
```

The first run may take longer because Docker downloads the Go image and project packages. CI also runs integration and end-to-end tests.

**6. Optional: run integration and E2E tests with PostgreSQL.** Keep Docker Compose running. Create a separate test database once (skip this command if it already exists):

```bash
docker compose exec -T postgres sh -c 'createdb -U "$POSTGRES_USER" fxquote_test'
```

Run the migrations and both test suites inside Docker:

```bash
set -a; source <(sed 's/\r$//' .env); set +a
MSYS_NO_PATHCONV=1 docker run --rm --network "container:$(docker compose ps -q postgres)" \
  -v "$(pwd -W 2>/dev/null || pwd):/app" -w /app \
  -e PGUSER="$POSTGRES_USER" -e PGPASSWORD="$POSTGRES_PASSWORD" \
  -e TEST_DATABASE_URL='postgres://127.0.0.1:5432/fxquote_test?sslmode=disable' \
  golang:1.27.3-alpine \
  sh -c 'DATABASE_URL="$TEST_DATABASE_URL" go run ./cmd/migrate up &&
    go test -tags=integration -count=1 ./tests/integration &&
    go test -tags=e2e -count=1 ./tests/e2e'
```

These suites **reset data in `fxquote_test`** and must run sequentially. Never use the application database for tests. The password is passed separately from the URL so special characters remain literal.

**7. Stop the service when you are done.** Try the API examples below before stopping it.

```bash
docker compose down
```

This stops the containers but **keeps your PostgreSQL data**.

## API Examples

Run these commands while the service is running.

**1. Request an update.** Send a currency pair as JSON:

```bash
curl -i -X POST http://localhost:8080/api/v1/quote-updates \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: eur-egp-demo-1' \
  -d '{"pair":"EUR/EGP"}'
```

The API returns `202 Accepted` with a body like `{"id":1}`. It means the job was created, **not** that the rate is ready. The optional `Idempotency-Key` helps prevent duplicate jobs if you send the same request again.

If a POST returns `503` or the connection breaks before you receive the ID, retry
with the **same key and pair**. The first attempt may already have saved the job.
Use a new key when you intentionally request another update. Keys do not expire.

**2. Check the update.** Replace `1` with the ID from your response:

```bash
curl http://localhost:8080/api/v1/quote-updates/1
```

The status can be `pending`, `processing`, `succeeded`, or `failed`. When it succeeds, the response includes `price`, `updated_at`, and `source_date`. The price is a string to keep its decimal precision.

**3. Get the latest saved rate.**

```bash
curl 'http://localhost:8080/api/v1/quotes/latest?pair=EUR%2FEGP'
```

The response includes the latest price and update time for `EUR/EGP`, ordered by source date and then retrieval time. If no successful update exists for this pair yet, the API returns `404 Not Found`.

## Project Structure

```text
.
├── api/                  # OpenAPI specification
├── cmd/                  # API and migration entry points
├── internal/             # HTTP, business logic, workers, and database code
├── migrations/           # PostgreSQL schema changes
├── tests/                # Integration and end-to-end tests
├── docs/                 # Project and architecture notes
├── .github/workflows/     # CI pipeline
├── compose.yaml          # Docker services
├── Dockerfile            # Application image
├── Makefile              # Developer commands
└── .env.example          # Example local settings
```

## How It Works

1. The API checks the request and saves a new update job in PostgreSQL.
2. A background worker picks up the job and requests a rate from Frankfurter.
3. The worker saves the result and changes the job status to `succeeded` or `failed`.
4. You can check the job by its ID or read the latest saved rate for a pair.

**The API does not wait for the exchange rate.** It creates a job, and a background worker processes it. Workers use database locks to avoid processing the same job at the same time. Temporary provider errors can be retried, with up to three attempts in total; a valid `Retry-After` on provider HTTP 429/503 responses can postpone the next attempt.

## Common Issues

| Problem | What to do |
| --- | --- |
| Docker is not running. | Start Docker Desktop, then run `docker compose up --build -d` again. |
| Port `8080` or `5433` is in use. | Change `APP_PORT` or `POSTGRES_PORT` in `.env`, then restart the containers. |
| `/health/ready` does not return `ok`. | Check the containers with `docker compose ps` and logs with `docker compose logs`. |
| The latest quote returns `404`. | Request an update first, then check that its status is `succeeded`. |
