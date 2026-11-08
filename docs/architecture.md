# fx-quote-service Architecture

The service exposes an asynchronous HTTP/JSON API for currency quotes between
USD, EUR and EGP. The HTTP server and one background worker run in the same Go
process; PostgreSQL stores both quotes and the durable update queue.

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

Handlers and the worker use the service. The service depends on interfaces
implemented by PostgreSQL and the provider; the domain depends on neither.

## Asynchronous request flow

1. `POST /api/v1/quote-updates` persists a `pending` job and immediately returns
   HTTP 202 with the update ID; it **does not** contact Frankfurter.
2. The worker polls PostgreSQL every second. `ClaimNext` atomically selects a due
   `pending` job or an expired `processing` lease using `FOR UPDATE SKIP LOCKED`.
   It issues a new UUID lease token, moves the job to `processing` and increases
   its attempt counter.
3. `service/quote.ProcessJob` calls `provider/frankfurter.FetchRate`. On success,
   it saves the exact price and marks the job `succeeded` in one transaction.
   The repository checks the lease token and deadline before committing.
4. On temporary provider failures (timeout, unavailable, 429), the worker
   schedules a retry in `pending` with backoff, up to three claimed attempts.
   Permanent errors or the last failed attempt become `failed` with a safe code
   and message. `FailExhausted` finalizes expired last attempts.
5. Clients poll `GET /api/v1/quote-updates/{id}` or read
   `GET /api/v1/quotes/latest?pair=USD%2FEGP` after success.

The worker executes one job at a time. Polling is once per second, with a
30-second lease and a 20-second total processing budget (the provider itself
has an 8-second request timeout). Transient retry delays are 2s, then 4s.
An expired lease can be reclaimed after a crash; token checks prevent a stale
worker from persisting a second result. `processing_timeout` is used for jobs
that exhausted attempts without finalizing.

## Runtime and shutdown

Docker Compose starts PostgreSQL, runs Goose migrations, then starts the
application. `bootstrap.Run` starts both the HTTP server and worker after the
database has been checked. `/health/ready` checks readiness and database
connectivity; it is not a check of external provider freshness.

On SIGINT/SIGTERM, readiness is disabled, the worker context is canceled, the
HTTP server stops accepting connections and waits up to 15 seconds for active
HTTP requests. The application waits for the worker to stop before closing
its database pool. An interrupted `processing` job remains durable and becomes
eligible after its lease expires; already succeeded jobs are not reclaimed.

## Project layout

| Directory | Contents |
| --- | --- |
| `cmd/` | API and migration entry points. |
| `internal/` | All domain, HTTP, service, repository, provider and worker packages. |
| `migrations/` | Goose PostgreSQL schema. |
| `api/openapi.yaml` | Public HTTP contract. |
| `tests/integration/` | Database-backed tests (requires separate `_test` DB). |
| `docs/` | Requirements, architecture and provider description. |

No Redis, RabbitMQ, extra Go modules or new SQL migrations are needed for stage 7.
