# fx-quote-service Architecture

The service exposes an HTTP API and updates currency quotes in the background.
Supported currencies are USD, EUR and EGP.

## Layers

| Package | Responsibility |
| --- | --- |
| `domain/quote` | Currency pairs, quotes, update jobs and business rules. |
| `repository/postgres` | Store and retrieve jobs and quotes. |
| `service/quote` | Create updates, read results and process jobs. |
| `handler/http` | HTTP requests, JSON responses and validation. |
| `provider/frankfurter` | Fetch a directly quoted rate for the requested directed pair via Frankfurter v2. |
| `worker` | Run background jobs and retry temporary failures. |
| `config`, `bootstrap`, `observability` | Configuration, startup and logging. |

Handlers and workers call services. Services use interfaces implemented by the
repository and provider. Domain logic is independent of HTTP and PostgreSQL.
The provider returns a `FetchedRate` and performs **no reciprocal/cross-rate
calculation** in the application; the existing domain controls price precision.

## Request Flow

1. A client requests an update and receives a job ID.
2. A worker (implemented in stage 7) requests the specific pair via [Frankfurter API v2](provider.md) and saves the result.
3. The client reads the job result or the latest successful quote for a pair.

One application process runs the HTTP server and workers. PostgreSQL stores jobs
and quotes. Pending work survives application restarts; completed results remain
available by their job IDs. During stage 6, the worker has not been connected,
so newly created jobs remain `pending`.

## Project Layout

| Directory | Contents |
| --- | --- |
| `cmd/` | Application and migration entry points. |
| `internal/` | Layers listed above. |
| `migrations/` | Goose SQL migrations. |
| `api/` | [OpenAPI specification](../api/openapi.yaml). |
| `tests/` | Integration and end-to-end tests; unit tests live next to the code. |
| `docs/` | Requirements, architecture and provider description. |

## Runtime

Docker Compose runs the application and PostgreSQL, applying Goose migrations
before startup. Configuration uses environment variables. Logging uses standard
`log/slog`; errors use standard `errors` and `fmt`. Health endpoints report liveness
and readiness. The application shuts down gracefully.

The Frankfurter adapter uses the standard `net/http` client with an 8-second
request deadline. The worker will own job retry/backoff, not the provider.
