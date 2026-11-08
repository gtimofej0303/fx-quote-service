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

Additional top-level fields in the provider response are accepted and ignored.
The four required fields keep their existing type, direction and value checks;
duplicate top-level names, malformed JSON and oversized bodies are rejected.

The domain and PostgreSQL reject a source date more than one calendar day ahead
of the UTC retrieval date. This tolerance covers source time zones ahead of UTC;
the stored source date is never shifted. Older business-day dates remain valid.

### Operational boundaries

- The stage-7 worker calls `service/quote.ProcessJob` with this provider. HTTP POST never calls the provider.
- Request deadline: 8 seconds in the adapter. No retries or caching in the provider adapter; retry/backoff is handled by the stage-7 worker (at most three claimed attempts).
- HTTP 404 is `provider_rate_not_found`; 429 is `provider_rate_limited`; 408/504 and network timeouts are `provider_timeout`; 5xx are `provider_unavailable`; 400/422 and other 4xx are `provider_rejected_request`; malformed successful JSON is `provider_invalid_response`.
- On HTTP 429/503, `Retry-After` accepts an HTTP date or unsigned decimal seconds (up to 4294967295). Missing, invalid, out-of-range or past-date values use the normal backoff. A valid later deadline is persisted in `next_attempt_at`; it never shortens backoff or occupies a worker while waiting. The three-attempt limit remains in effect.
- The adapter limits a response body to 32 KiB, does not follow redirects, and never includes raw upstream response bodies in client-facing error messages.
- Failed attempts log the internal cause with the update ID, pair, attempt and public error code before database finalization. HTTP responses and persisted public failure messages retain only the safe message; raw upstream bodies are not logged.
- Unit tests run offline through `httptest`. The stage-7 guide includes a full HTTP-to-worker smoke test, while the stage-6 guide checks each direct Frankfurter endpoint.

For production-scale traffic, consider caching on the worker side or self-hosting.
The public API has abuse-prevention rate limiting, even though daily/monthly
quotas are absent. The application itself currently does not promise strict
per-second rate control. See the [official API FAQ](https://frankfurter.dev/).
