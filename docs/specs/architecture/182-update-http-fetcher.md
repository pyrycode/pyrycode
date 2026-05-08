# Spec: `internal/update` — HTTP fetcher for release JSON + asset bytes (#182)

## Files to read first

- `internal/update/version.go` — sibling file in the same package; mirror its file-header doc comment style and exported-error pattern (`ErrMalformedRelease`, `ErrInvalidVersion` declared at top, wrapped at return sites with `fmt.Errorf("…: %w", …)`).
- `internal/update/version_test.go` — establishes the package's table-driven test convention with `t.Parallel()` per subtest and `errors.Is` for sentinel assertions; reuse the same shape for the cases that don't need an `httptest.Server`.
- `internal/update/checksum.go:1-32` — confirms package doc comment is on `version.go` (not repeated elsewhere) and that exported errors live next to the function that returns them. Same convention applies to this ticket: the new sentinel (if any) goes at the top of `fetch.go`.
- `cmd/pyry/main.go:53-54` — `var Version = "dev"`. The wiring ticket will pass `"pyry/" + Version` as the `UserAgent` field; the doc comment on `Fetcher.UserAgent` should reference this so the developer knows what shape of value to expect.
- `CODING-STYLE.md` §§ Error Handling, Testing, Dependencies — stdlib-only `net/http`, table-driven tests, error wrapping with `%w`, no testify.
- `docs/lessons.md` § "PTY master backpressure stalls slave-side process exit" and § "fsnotify reports as-watched, kernel probes report canonicalised — match in one form" — **not directly relevant** to this ticket; listed here only because they're in the same lessons file. Skip.

(No prior `pyrycode-docs` decisions on HTTP fetching; this is greenfield.)

## Context

This is the network-I/O slice of `pyry update`, paired with the pure-function tickets:

- #179 (`ParseLatestRelease`, `CompareVersions`) — already merged; consumes the JSON bytes this fetcher returns.
- #180 (`AssetName`, `ParseChecksumsFile`, `VerifySHA256`) — already merged; consumes the asset bytes this fetcher returns (both the tarball and `checksums.txt`).
- #183 (tarball extraction, future) — consumes the tarball bytes.
- The wiring ticket (future) — composes all four pure functions plus this fetcher into the `pyry update` subcommand.

The fetcher is intentionally narrow: it knows how to do an authenticated-via-User-Agent GET, return the body, and surface non-2xx as an error. It does **not** know what the bytes mean, does **not** retry, does **not** impose a timeout (caller owns the `*http.Client`), and does **not** template URLs beyond the latest-release endpoint shape `<BaseURL>/repos/<repo>/releases/latest`.

The two asset fetches (tarball + `checksums.txt`) collapse to one method (`FetchAsset`) because both are GETs against URLs the caller has already extracted from the release JSON via the wiring ticket's logic. Only the latest-release fetch needs URL templating, which is why it gets its own method (`FetchLatestRelease`) that takes `repo string` rather than a fully-formed URL.

## Design

### Package

Same package `internal/update`, new files:

```
internal/update/
  version.go       (existing — #179)
  version_test.go  (existing — #179)
  checksum.go      (existing — #180)
  checksum_test.go (existing — #180)
  fetch.go         NEW — Fetcher struct + FetchLatestRelease + FetchAsset
  fetch_test.go    NEW — httptest-driven coverage
```

No new sub-packages. The package doc comment lives on `version.go` and already describes the package's full purpose ("release manifest parsing, version comparison, fetch, and replace") — `fetch.go` opens with a bare `package update` line, no doc comment, matching `checksum.go:1`.

### Types

```go
// Fetcher performs the network reads required by the `pyry update` flow:
// the GitHub Releases API JSON and the release-asset bytes (tarball,
// checksums.txt). It is a thin wrapper over net/http: no retries, no
// caller-imposed timeouts, no body parsing — those concerns live
// elsewhere in this package and in the wiring ticket.
//
// The zero value is usable and targets api.github.com with
// http.DefaultClient and a "pyry/dev" User-Agent. Callers in cmd/pyry
// override UserAgent to "pyry/<Version>" and typically install an
// HTTPClient with an explicit Timeout (the fetcher does not impose one).
type Fetcher struct {
    // BaseURL is the API root for the latest-release endpoint.
    // Empty value defaults to "https://api.github.com".
    // Tests set this to httptest.NewServer's URL.
    BaseURL string

    // HTTPClient is the underlying transport. Empty value defaults to
    // http.DefaultClient. Callers wanting a request budget construct a
    // &http.Client{Timeout: 60*time.Second} and assign it here.
    HTTPClient *http.Client

    // UserAgent is sent on every request. Empty value defaults to
    // "pyry/dev". Callers in cmd/pyry set this to "pyry/" + main.Version
    // so GitHub's API doesn't blanket-reject anonymous traffic.
    UserAgent string
}
```

Rationale for the struct-with-zero-value-defaults shape (over a `New(opts) *Fetcher` constructor):

- All three fields are independently optional, all three have obvious defaults, and the wiring layer will set at most one (`UserAgent`) in production. A constructor would be five lines of boilerplate per call site for a struct that's already zero-value-correct. Same shape as `http.Client` itself (`Timeout`, `Transport`, `Jar`, `CheckRedirect` all optional).
- Tests need to override `BaseURL` and `HTTPClient` — direct field assignment is the most readable form.
- No invariants between fields require a constructor to enforce.

The defaults are resolved per-call inside the methods, not at zero-value time, so a `Fetcher{}` literal continues to work even after a caller mutates the package-level `http.DefaultClient` (unlikely but supported). Pattern:

```go
func (f *Fetcher) baseURL() string {
    if f.BaseURL == "" {
        return "https://api.github.com"
    }
    return f.BaseURL
}

func (f *Fetcher) httpClient() *http.Client {
    if f.HTTPClient == nil {
        return http.DefaultClient
    }
    return f.HTTPClient
}

func (f *Fetcher) userAgent() string {
    if f.UserAgent == "" {
        return "pyry/dev"
    }
    return f.UserAgent
}
```

These three are unexported methods, not exported. They exist purely so the two public methods don't repeat the nil-check pattern.

### Function signatures

```go
// FetchLatestRelease GETs <BaseURL>/repos/<repo>/releases/latest and returns
// the response body verbatim. repo is in "owner/name" form (e.g.
// "pyrycode/pyrycode"). The body is the raw GitHub Releases API JSON,
// suitable for passing to ParseLatestRelease.
//
// Returns an error if the request cannot be constructed, the transport
// fails, the body cannot be read, or the response status is not 2xx.
// Non-2xx errors include the status code and the requested URL.
// Context cancellation propagates: an error wrapping context.Canceled
// or context.DeadlineExceeded is returned if ctx is done before the
// response body is fully received.
func (f *Fetcher) FetchLatestRelease(ctx context.Context, repo string) ([]byte, error)

// FetchAsset GETs the given URL and returns the response body verbatim.
// The URL is typically extracted from the release JSON returned by
// FetchLatestRelease (assets[].browser_download_url for the tarball,
// or the URL of the checksums.txt asset). The User-Agent header is sent
// on this request, same as FetchLatestRelease.
//
// Same error semantics as FetchLatestRelease.
func (f *Fetcher) FetchAsset(ctx context.Context, url string) ([]byte, error)
```

Rationale for two methods over one:

- The latest-release endpoint needs URL templating from a stable shape (`<BaseURL>/repos/<repo>/releases/latest`); the caller would otherwise need to know the API URL convention. Encapsulating it here keeps `cmd/pyry` free of GitHub-API-shape knowledge.
- Asset fetches receive a fully-formed URL from the parsed release JSON (the wiring ticket extracts `browser_download_url` per asset). The fetcher cannot template it — it doesn't know the GoReleaser asset name conventions and shouldn't.

The two methods share their implementation core (build request, set headers, do, check status, read body) — extract that into an unexported helper. See "Implementation sketch" below.

### Error contract

No new exported sentinel. The AC requires "wrapped error containing the status code and the URL"; plain `fmt.Errorf` is sufficient because:

- The wiring ticket cannot retry (AC explicitly forbids it) and therefore cannot branch on "transient 5xx" vs "permanent 4xx" — a typed sentinel would be unused.
- Context cancellation already produces a `*url.Error` wrapping `context.Canceled` / `context.DeadlineExceeded`, which `errors.Is` traverses correctly with no help from this layer.
- Body-read failures are mid-stream transport errors; surfacing them with `fmt.Errorf("reading response body from %s: %w", url, err)` gives the operator everything they need.

If a future caller needs typed branching, add `ErrUnexpectedStatus` in a follow-up — same shape as #180's `ErrChecksumMismatch`. Defer until observed (Pipeline Principle: Evidence-Based Fix Selection).

Wrapping shape at each return site:

```go
// Request construction (rare — invalid URL only):
return nil, fmt.Errorf("building GET %s: %w", url, err)

// Transport failure (includes ctx-cancel via *url.Error):
return nil, fmt.Errorf("GET %s: %w", url, err)

// Non-2xx status:
return nil, fmt.Errorf("GET %s: unexpected status %d", url, resp.StatusCode)

// Body read failure:
return nil, fmt.Errorf("reading response body from %s: %w", url, err)
```

The non-2xx case uses bare `fmt.Errorf` (no `%w`) because there is no inner error to wrap — the message is fully self-contained. The status-code-and-URL requirement of the AC is satisfied by this single line.

### Implementation sketch

```go
package update

import (
    "context"
    "fmt"
    "io"
    "net/http"
)

type Fetcher struct {
    BaseURL    string
    HTTPClient *http.Client
    UserAgent  string
}

func (f *Fetcher) FetchLatestRelease(ctx context.Context, repo string) ([]byte, error) {
    url := fmt.Sprintf("%s/repos/%s/releases/latest", f.baseURL(), repo)
    return f.get(ctx, url)
}

func (f *Fetcher) FetchAsset(ctx context.Context, url string) ([]byte, error) {
    return f.get(ctx, url)
}

func (f *Fetcher) get(ctx context.Context, url string) ([]byte, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
    if err != nil {
        return nil, fmt.Errorf("building GET %s: %w", url, err)
    }
    req.Header.Set("User-Agent", f.userAgent())

    resp, err := f.httpClient().Do(req)
    if err != nil {
        return nil, fmt.Errorf("GET %s: %w", url, err)
    }
    defer resp.Body.Close()

    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        // Drain a bounded prefix of the body so the connection can be
        // reused; ignore errors — we already have a status to report.
        _, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
        return nil, fmt.Errorf("GET %s: unexpected status %d", url, resp.StatusCode)
    }

    body, err := io.ReadAll(resp.Body)
    if err != nil {
        return nil, fmt.Errorf("reading response body from %s: %w", url, err)
    }
    return body, nil
}

// baseURL / httpClient / userAgent helpers as shown above.
```

Notes:

- `http.NewRequestWithContext` is the right constructor (not `http.NewRequest`) — it propagates ctx to the transport so cancellation interrupts an in-flight read, not just a queued send. This is the mechanism the AC's "context cancellation mid-request returns `errors.Is(err, context.Canceled)`" relies on.
- `defer resp.Body.Close()` is unconditional after a successful `Do`. The `LimitReader(…, 1<<10)` drain on the non-2xx path lets the underlying TCP connection back into the pool; a small (1KiB) ceiling avoids reading megabytes of error HTML for a misconfigured server. This is a transport-hygiene optimisation, not a correctness requirement — drop it if it complicates the spec, but the line cost is two statements and `net/http` documentation explicitly recommends draining.
- `io.ReadAll` is fine here despite reading potentially-large tarballs (~10–30 MiB for pyry). The wiring ticket holds the bytes in memory anyway to compute SHA-256 (#180's `VerifySHA256(data []byte, …)`) and to extract the binary (#183's tarball extractor consumes bytes). A streaming variant (`io.Reader` return) would be premature optimisation — defer until profiling shows memory pressure.
- The `Fetcher` is a value type; `*Fetcher` receivers are used so a shared `Fetcher{}` (e.g. one per `pyry update` invocation) doesn't get accidentally copied with all-zero fields.

### Data flow

```
cmd/pyry update wiring (sister ticket)
    │
    │ f := update.Fetcher{UserAgent: "pyry/" + Version,
    │                     HTTPClient: &http.Client{Timeout: 60*time.Second}}
    │
    ▼
f.FetchLatestRelease(ctx, "pyrycode/pyrycode")
    │
    │ GET https://api.github.com/repos/pyrycode/pyrycode/releases/latest
    │ User-Agent: pyry/v0.9.1
    │
    ▼
[]byte (release JSON) ──► update.ParseLatestRelease (#179)
                                 │
                                 ▼
                           tagName, assetURL (parsed by wiring)
                                 │
                                 ▼
f.FetchAsset(ctx, checksumsURL)  ──► []byte ──► update.ParseChecksumsFile (#180)
                                                       │
                                                       ▼
                                                 expectedHex
f.FetchAsset(ctx, tarballURL)    ──► []byte ──► update.VerifySHA256(data, expectedHex)
                                                       │
                                                       ▼
                                                 #183: extract; wiring: replace + restart
```

The fetcher is stateless across calls: each `Fetch*` constructs a fresh `http.Request`, the `*http.Client` handles connection pooling internally. Concurrent calls on the same `*Fetcher` are safe (no mutable state).

## Concurrency model

No goroutines. No mutexes. No internal channels.

`*Fetcher` is safe for concurrent use because:

- All fields are read-only after construction (`BaseURL`, `UserAgent` strings; `HTTPClient` pointer). The wiring ticket sets them once and never mutates.
- `*http.Client.Do` is documented as safe for concurrent use.
- Each `Fetch*` call constructs a fresh `*http.Request` (no shared mutable state).

Context cancellation is the only "concurrency primitive" in play, and it flows through `http.NewRequestWithContext` → transport's poller → mid-read EAGAIN-equivalent on the socket. The `*url.Error` returned by `client.Do` wraps `context.Canceled` or `context.DeadlineExceeded`; `errors.Is` traverses the wrap chain transparently.

## Error handling

Failure modes and responses:

| Trigger | Returned error |
|---------|----------------|
| Invalid URL passed to `FetchAsset` (e.g. unparseable scheme) | `building GET <url>: <inner>` (rare; `http.NewRequestWithContext` only errors on truly malformed URLs) |
| DNS / connection failure | `GET <url>: <*url.Error>` |
| Server sends non-2xx response (404, 500, etc.) | `GET <url>: unexpected status <code>` (no `%w` — no inner error) |
| Body read interrupted mid-stream | `reading response body from <url>: <inner>` |
| Context cancelled before response | `GET <url>: <*url.Error wrapping context.Canceled>` |
| Context cancelled during body read | `reading response body from <url>: <wrapped context.Canceled>` |

The two ctx-cancel rows both satisfy `errors.Is(err, context.Canceled)` because `*url.Error.Unwrap` and `fmt.Errorf("…: %w", …)` both participate in the unwrap chain. Tests assert the `errors.Is` predicate, not the exact message — message wording is not load-bearing.

## Testing strategy

Single new test file `fetch_test.go`, table-driven where shape allows, `httptest.NewServer` for the live-loop cases, stdlib `testing` only.

### Test cases

Each subtest runs with `t.Parallel()` (no shared state; `httptest.NewServer` gives each its own port).

1. **`TestFetcher_FetchLatestRelease_OK`** — `httptest.NewServer` whose handler:
   - Asserts `r.URL.Path == "/repos/pyrycode/pyrycode/releases/latest"`.
   - Asserts `r.Header.Get("User-Agent") == "pyry/v0.9.1"`.
   - Writes a canned JSON body `{"tag_name":"v0.9.1"}`.

   Construct `f := Fetcher{BaseURL: ts.URL, UserAgent: "pyry/v0.9.1"}`; assert `body == canned` byte-for-byte.

2. **`TestFetcher_FetchAsset_OK`** — handler returns 16 bytes of binary fixture (`[]byte{0x00, 0x01, …, 0x0f}`). Caller passes `ts.URL + "/blob"` directly. Asserts `bytes.Equal(got, want)`.

3. **`TestFetcher_DefaultUserAgent`** — leaves `UserAgent` zero; asserts the request reaches the test server with `User-Agent: pyry/dev`. (Covers the zero-value default.)

4. **`TestFetcher_NonOKStatus`** — table-driven over `{404, 500, 503}`. Handler writes the status; assert the returned error contains the status code as a decimal substring AND the test server's URL. No `errors.Is` here — there's no sentinel; this is the one case where substring assertion is the right shape (the AC literally requires "an error that includes the HTTP status code and the URL").

5. **`TestFetcher_ContextCancelled`** — handler blocks on a channel that never closes (until `t.Cleanup` releases it post-test); test cancels `ctx` after a short delay; assert `errors.Is(err, context.Canceled)`. This is the load-bearing test for the AC's ctx-cancel propagation requirement.

   Implementation note: use `context.WithCancel`, fire `time.AfterFunc(50*time.Millisecond, cancel)` before the call, ensure handler-side block is released in `t.Cleanup` so the test server can shut down cleanly. (Test passes well under the 50ms because client cancels the in-flight request before the body arrives; the deadline only matters as a "don't hang the test forever" upper bound.)

6. **`TestFetcher_FetchLatestRelease_RepoSlugInPath`** — handler echoes back `r.URL.Path`; caller passes `repo = "owner/different-name"`; assert the path is `/repos/owner/different-name/releases/latest`. (Confirms the URL-templating shape.)

### Test conventions

- One `httptest.NewServer` per subtest (no shared server). Each test's handler is a closure capturing its own assertions.
- The handler asserts inbound headers via `t.Errorf` (not `t.Fatalf`) — a header mismatch shouldn't kill the response; the response body is what the outer test checks first, and a missing-UA failure should still report the body shape for diagnosis. **Caveat:** `t.Errorf` from inside the handler goroutine is safe under stdlib `testing` but the failure is associated with whichever subtest the handler-goroutine call frame walks back to. For the parallel-safe case, prefer `t.Errorf` — same pattern as `net/http/httptest`'s own examples.
- No `defer ts.Close()` — use `t.Cleanup(ts.Close)` so panicked tests still release the listener.
- `errors.Is(err, context.Canceled)` for the cancellation case; substring-match on `strconv.Itoa(code)` and `ts.URL` for the non-2xx case; `bytes.Equal` for body bytes.
- Run with `go test -race ./internal/update/...` as the verification command.

### What's deliberately NOT tested

- Real network calls to api.github.com. Out of scope; would make the test suite flaky and slow.
- Retry behaviour. There is none.
- Timeout enforcement. The fetcher doesn't impose one; the test would be testing `*http.Client.Timeout`, not `Fetcher`.
- Streaming download of large bodies. `io.ReadAll` is the contract.
- TLS verification details. `httptest.NewServer` is HTTP-only by design; TLS is `http.DefaultTransport`'s job.

## Open questions

1. **Should non-2xx body content be exposed in the error?** GitHub's API returns useful diagnostic JSON in 4xx bodies (e.g. `{"message":"Not Found","documentation_url":"…"}`). Today's spec discards it (drain-and-ignore). Operators debugging "update failed" would benefit from seeing the message. **Defer**: AC requires status code + URL only; adding body-prefix-in-error is a follow-up if real failures show "404 unhelpful" complaints. The wiring ticket sees the URL — operators can `curl` it themselves.
2. **`User-Agent` shape — `pyry/v0.9.1` vs `pyry/0.9.1`.** GitHub accepts either; pyry's `--version` output is "pyry v0.9.1" (per `cmd/pyry/main.go:151` — it prints the bare token, which after #179 may include the `v`). Matching the binary's self-report keeps the User-Agent grep-friendly when correlating GitHub access logs against pyry telemetry (none today). Not a load-bearing decision; document the shape (`"pyry/" + Version`) in the doc comment so the wiring ticket can't drift.
3. **`FetchAsset` URL validation.** Today: anything that `http.NewRequestWithContext` accepts. A future caller could pass a `file://` URL and read local files unintentionally. **Defer**: the wiring ticket extracts URLs from a trusted source (GitHub's release JSON, parsed by `ParseLatestRelease`), so the threat model doesn't apply today. If the wiring ticket evolves to accept user-supplied URLs (e.g. `pyry update --from-url`), revisit.
4. **Should `Fetcher` get a constructor `New(opts) *Fetcher` for symmetry with future caller patterns?** No — not until a second consumer arrives. Direct field assignment is the current convention for both `update.Fetcher` and stdlib `http.Client`, and a constructor adds boilerplate without enforcing any invariant. Defer until observed.

## Out of scope

- Parsing the JSON body (#179 — already merged: `ParseLatestRelease`).
- Parsing `checksums.txt` (#180 — already merged: `ParseChecksumsFile`).
- Verifying SHA-256 (#180 — already merged: `VerifySHA256`).
- Extracting the tarball (#183 — future).
- The `pyry update` CLI subcommand wiring (future ticket).
- Retry / exponential backoff (deliberately omitted; AC forbids retries).
- Caller-side timeouts (caller's responsibility via `*http.Client`).
- `If-None-Match` / ETag-based caching (would help against rate limits but not in scope today; GitHub's anonymous limit is 60/hr, fine for an interactive `pyry update`).
- A `Mockable` or `Fetcherer` interface. Tests use `httptest.NewServer` directly, which exercises the real `Fetcher` end-to-end. The wiring ticket can introduce a 2-method consumer-defined interface there if it needs one — defining it preemptively here violates `CODING-STYLE.md` §Interface Design.
