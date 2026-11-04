# fx-quote-service Requirements

## Assignment Requirements and Acceptance Criteria

| ID | Requirement | Acceptance criterion |
| --- | --- | --- |
| 1 | Go, HTTP and JSON | The service builds; the API accepts and returns documented JSON data. |
| 2 | Request a quote update and receive an ID | POST saves a job and returns its ID without requesting an external rate. |
| 3 | Background updates | A worker fetches and saves an external rate independently of the client's HTTP request. |
| 4 | Quote by update ID | After success, GET returns the price and update time for that job. |
| 5 | Latest quote for a pair | GET returns the latest successful result. |
| 6 | Database | PostgreSQL stores jobs, states and results. |
| 7 | External source | The provider fetches real rates over HTTPS. |
| 8 | Limited currency set | Six directed pairs between USD, EUR and EGP are supported. |
| 9 | GitHub, build and launch | The finished project is published, builds and completes the end-to-end scenario from a clean clone. |
| 10 | README | Includes the overview, configuration, limitations, build, launch and test commands. |

## Additional Assignment Requirements

| ID | Requirement | Acceptance criterion |
| --- | --- | --- |
| 1 | Unit tests | Cover domain rules, service scenarios, the HTTP contract and provider errors. |
| 2 | Docker | The application and PostgreSQL run through Compose; migrations complete before the application starts. |
| 3 | Idempotency | Requests with the same key and pair return one ID, including concurrent requests. Without a key, each POST creates a new job. |
| 4 | Swagger / OpenAPI | The [specification](../api/openapi.yaml) describes all operations, schemas, headers, states and errors. |
