# sdk-core-go

> [!WARNING]
> **Work in progress, not production-ready.** APIs are unstable and
> may change without notice before the first stable release. External
> pull requests are not accepted yet while the foundations stabilise.
> Issues are welcome: open one before sending code if you spot a bug
> or want a feature.

Reusable Connect-Go interceptors for every Pinguteca service SDK: auth, retry, idempotency, and observability. Drop in once when constructing the generated `*ServiceClient` and the rest of the application code stays focused on business logic.

```go
import (
    "net/http"

    "connectrpc.com/connect"

    "github.com/Pinguteca/sdk-core-go/auth"
    "github.com/Pinguteca/sdk-core-go/idempotency"
    "github.com/Pinguteca/sdk-core-go/otel"
    "github.com/Pinguteca/sdk-core-go/retry"
)

func NewClient(baseURL string) (myservicev1.MyServiceClient, error) {
    src, err := auth.ClientCredentials(auth.ClientCredentialsConfig{
        TokenURL:     "https://idp.example/oauth2/token",
        ClientID:     "svc",
        ClientSecret: "shh",
        Scopes:       []string{"myservice.read", "myservice.write"},
    })
    if err != nil {
        return nil, err
    }

    authIc, err := auth.Interceptor(auth.Options{Source: src})
    if err != nil {
        return nil, err
    }

    return myservicev1.NewMyServiceClient(
        http.DefaultClient,
        baseURL,
        connect.WithInterceptors(
            otel.Interceptor(otel.Options{}),         // outermost: span covers everything
            retry.Interceptor(retry.DefaultConfig()), // before idempotency: retried attempts reuse the cached key
            idempotency.Interceptor(idempotency.Options{}),
            authIc, // innermost: token attached just before the network
        ),
    ), nil
}
```

## Packages

### `auth`

Token-source interceptor for outbound RPCs. Two implementations ship out of the box and `TokenSource` is a small interface so you can plug in any IdP.

- `StaticBearer(token)` — fixed token (CI fixtures, hand-issued service credentials).
- `ClientCredentials(cfg)` — OAuth2 `client_credentials` flow with thread-safe token caching and automatic refresh on expiry. Backed by `golang.org/x/oauth2/clientcredentials`. Works with Keycloak, Auth0, Entra ID, Okta, anything OAuth2-compliant.
- `TokenSourceFunc(fn)` — adapt any token-fetching function into a `TokenSource`.

Override `Options.FormatHeader` to send a raw token, a custom scheme, or a non-`Authorization` header.

### `retry`

Exponential backoff with jitter. Retries unary RPCs only — streams cannot be safely replayed.

- Honours server-provided [`google.rpc.RetryInfo`](https://pkg.go.dev/google.golang.org/genproto/googleapis/rpc/errdetails#RetryInfo). When present, the retry waits for exactly the suggested delay (capped at `Max`) before the next attempt.
- Default retryable codes: `Unavailable`, `ResourceExhausted`, `Aborted`, `DeadlineExceeded`. Override with `RetryableCodes` or supply `IsRetryable` for per-error logic (e.g. inspect `ErrorInfo.Reason`).
- Honours `context.Context` cancellation: a cancelled context aborts immediately even mid-backoff.

### `idempotency`

Attaches a stable `Idempotency-Key` header to mutating unary RPCs.

- Generates a UUIDv7 by default — time-ordered, 128-bit, ideal for server-side dedup tables that can range-scan keys.
- Caches the key on the call's `context.Context` so retries replay the same key (compose this interceptor *before* `retry`).
- Skips read-only methods by default (procedure-name heuristic: starts with `Get`, `List`, `Read`, `Watch`, `Search`, `Query`, `Lookup`). Override with `Options.IsSafe`.

### `otel`

OpenTelemetry span per RPC plus correlation-ID propagation.

- Emits one span per call with `rpc.system=connect_rpc`, `rpc.service`, `rpc.method`, and `rpc.connect.code` on error.
- Propagates W3C `traceparent` outbound (uses the global `TextMapPropagator` by default).
- Generates an `X-Request-ID` (UUIDv7) when the inbound call lacks one, forwards it through the call chain, and echoes it back to the caller via the response header for log correlation.

### `errors`

Helpers for inspecting Connect errors. Used internally by `retry` and exposed for application code:

- `AsConnect(err)` — typed unwrap.
- `Code(err)` — extract the Connect code, or `Unknown`.
- `RetryDelay(err)` — pull `RetryInfo.RetryDelay` if present.
- `FieldViolations(err)` — flatten every `BadRequest.FieldViolation` attached to the error.
- `Detail[T proto.Message](err)` — extract the first detail of type `T`.

## Composition order

Outermost first when calling `connect.WithInterceptors(...)`:

```
otel  ->  retry  ->  idempotency  ->  auth
```

Reasoning:

- **otel** wraps everything so spans capture retries and auth latency.
- **retry** replays failures of inner interceptors. Placed before idempotency so each retry reuses the same idempotency key (the key lives on the `context.Context`).
- **idempotency** runs before auth so the auth header is attached *after* the request is fully prepared (idempotency key needs no auth).
- **auth** is innermost: tokens are short-lived; we want the most recent one on the wire, not one captured by a span four interceptors ago.

## Status

Phase 1 of the Pinguteca SDK-Core. Adds: auth, retry, idempotency, OTel.

Planned (Phase 2):
- Circuit breaker
- Hedging (read-only operations)
- ETag / `Cache-Control` for Connect GET
- Pagination iterators
- Connection pool tuning helpers

## License

Apache 2.0. See [LICENSE](./LICENSE).
