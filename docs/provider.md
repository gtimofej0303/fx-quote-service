# Quote Provider

## Frankfurter API v2

| Item | Value |
| --- | --- |
| Documentation | [Frankfurter API](https://frankfurter.dev/) |
| Endpoint | `GET https://api.frankfurter.dev/v2/rate/{base}/{quote}` |
| Authentication | No API key or registration for the public API. |
| Cost and limits | Free public API. No daily/monthly quota; abuse-prevention rate limiting applies. The provider does not publish a guaranteed unlimited requests-per-second rate. |
| Supported pairs in this service | `USD/EUR`, `USD/EGP`, `EUR/USD`, `EUR/EGP`, `EGP/USD`, `EGP/EUR`. |
| Response fields | JSON object: `date` (`YYYY-MM-DD`), `base` (ISO 4217), `quote` (ISO 4217), `rate` (positive JSON number). |
| Price meaning | `base/quote` is the number of units of `quote` per unit of `base`. |
| Quote type | Reference/blended mid-market rate, not a live executable bid/ask quote. |
| Freshness | Typically daily; `date` may be an earlier business day on weekends/holidays. |

### Directed-pair contract

For each valid `CurrencyPair`, `internal/provider/frankfurter` makes exactly **one**
request to the corresponding `/v2/rate/{base}/{quote}` endpoint. For example,
`USD/EGP` uses `/v2/rate/usd/egp`; `EGP/USD` uses `/v2/rate/egp/usd`.
The application **does not** compute inverses or cross-rates from other pairs.
Frankfurter may blend and normalize upstream values internally; the service
stores the already-computed rate returned by this endpoint.

Example of the response shape (illustrative price/date, not a live rate):

```json
{"date":"2026-11-06","base":"USD","quote":"EUR","rate":0.87598}
```

The adapter validates the returned direction (`base` and `quote`), parses `rate`
without `float64`, rejects unrepresentable values rather than rounding, and
maps `date` to `SourceDate`. `UpdatedAt` is the UTC time at which the valid
response was received/processed by this application, not the provider's date.
The current domain stores prices with up to 18 fractional decimal digits.

### Operational boundaries

- Provider adapter is called by `service/quote.ProcessJob` from the worker **starting in stage 7**. Stage 6 does not run the worker.
- Request deadline: 8 seconds in the adapter. No automatic retries or caching in stage 6; retry/backoff belongs to the worker in stage 7.
- HTTP 404 is `provider_rate_not_found`; 429 is `provider_rate_limited`; 408/504 and network timeouts are `provider_timeout`; 5xx are `provider_unavailable`; 400/422 and other 4xx are `provider_rejected_request`; malformed successful JSON is `provider_invalid_response`.
- The adapter limits a response body to 32 KiB, does not follow redirects, and never includes raw upstream response bodies in client-facing error messages.
- Manual smoke test for all six pairs: run the `curl` loop in the stage-6 guide. Unit tests run offline through `httptest`.

For production-scale traffic, consider caching on the worker side or self-hosting.
The public API has abuse-prevention rate limiting, even though daily/monthly
quotas are absent. The application itself currently does not promise strict
per-second rate control. See the [official API FAQ](https://frankfurter.dev/).
