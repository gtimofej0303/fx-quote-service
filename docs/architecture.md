# fx-quote-service Architecture

The service exposes an asynchronous HTTP/JSON API for currency quotes between
USD, EUR and EGP. A single application process runs an HTTP server and **two
background workers**. PostgreSQL stores both quotes and a durable job queue.

## Layers

| Package | Responsibility |
| --- | --- |
| `internal/domain/quote` | Directed currency pairs, exact decimal prices, quotes, update jobs and validation. |
| `internal/repository/postgres` | Transactional persistence, atomic job claims and lease-protected state changes. |
| `internal/service/quote` | Request/read operations and processing of an already claimed job. |
| `internal/handler/http` | HTTP API, JSON validation and public error mapping. |
| `internal/provider/frankfurter` | One direct Frankfurter v2 GET per directed pair; no conversion in Go. |
| `internal/worker` | Poll the database queue, invoke `ProcessJob`, schedule retry and finalize failures. |
| `internal/config`, `internal/bootstrap`, `internal/observability` | Configuration, lifecycle and structured logs. |

Handlers and workers use the service. The service depends on interfaces
implemented by PostgreSQL and the provider; the domain depends on neither.

## Asynchronous request flow

1. `POST /api/v1/quote-updates` persists a `pending` job and immediately returns
   HTTP 202 with the update ID. It does not contact the external provider.
2. Each worker polls every second. `ClaimNext` atomically selects a due `pending`
   job or an expired `processing` lease using `FOR UPDATE SKIP LOCKED`, assigns
   a fresh lease token and increments the attempt counter.
3. `service/quote.ProcessJob` calls `provider/frankfurter.FetchRate`. On success,
   `SaveSuccess` commits the exact price and `succeeded` status in one transaction.
   A missing, expired or rotated lease token prevents stale writes.
4. Temporary provider failures (timeout, unavailable, rate limiting) are retried
   after a database-scheduled backoff of 2s and then 4s. Other provider failures
   immediately become `failed`. No job is claimed more than three times.
   `FailExhausted` finalizes a last attempt abandoned after a crash.
5. Clients poll `GET /api/v1/quote-updates/{id}` or read
   `GET /api/v1/quotes/latest?pair=USD%2FEGP` after success. Latest is ordered
   by `source_date`, then `updated_at`, then `update_id` (all descending).

Each worker processes at most one job at once (two concurrent jobs per app
process). The lease is 30 seconds, one processing attempt has a 20-second
budget, and a provider request has an 8-second timeout. A crash can cause the
external provider request to be repeated; processing is **at least once**,
while lease fencing and transactional uniqueness protect persisted results.
There is no global rate limiter across application processes.

## Runtime and shutdown

Docker Compose starts PostgreSQL, runs Goose migrations, then starts the
application. `/health/ready` checks application readiness and PostgreSQL,
not the freshness or availability of Frankfurter.

On SIGINT/SIGTERM, readiness is disabled, the workers are canceled and the HTTP
server stops accepting new connections, allowing up to 15 seconds for active
HTTP requests. The application waits for both workers to stop before closing
its database pool. An interrupted `processing` job remains durable and becomes
eligible when its lease expires; a completed job is never reclaimed.

## Testing and safety

- Unit tests next to implementation packages run without PostgreSQL or internet.
- `tests/integration/` uses real PostgreSQL to test repository behavior,
  concurrency across independent connection pools and worker recovery.
- `tests/e2e/` uses real HTTP handlers, PostgreSQL, workers and a local
  `httptest` Frankfurter v2 server. It checks concurrent idempotency, exact
  prices, failures, retry/backoff and a controlled interrupted-worker restart.
- Both tagged suites intentionally truncate `quotes` and `quote_updates` in
  a dedicated database whose name ends in `_test`. Run the two test packages
  **sequentially**; never aim `TEST_DATABASE_URL` at application data.
- A separate Docker Compose smoke test uses the real provider and checks
  deployment and persistence across a container restart. External availability
  is not deterministic and is not used for the automated offline E2E tests.

## Project layout

| Directory | Contents |
| --- | --- |
| `cmd/` | API and migration entry points. |
| `internal/` | Domain, HTTP, service, repository, provider and worker packages. |
| `migrations/` | Goose PostgreSQL schema. |
| `api/openapi.yaml` | Public HTTP contract. |
| `tests/integration/` | Tagged PostgreSQL-backed tests. |
| `tests/e2e/` | Tagged HTTP-to-PostgreSQL/worker/provider tests. |
| `docs/` | Requirements, architecture and provider description. |

Stage 8 adds tests and corrects documentation only: no Redis, broker, new SQL
migrations, dependencies or public API changes are required.
