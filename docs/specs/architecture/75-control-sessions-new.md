# #75 — control-plane verb `sessions.new` + `Sessioner` seam

Phase 1.1a-B1. Wire-only addition. Adds the first `sessions.<verb>` namespace
member to `internal/control` and a consumer-side `Sessioner` interface that
`*sessions.Pool` will satisfy structurally. No `cmd/pyry` work — the `pyry
sessions new` CLI router is a separate ticket.

## Files to read first

- `internal/control/protocol.go` (whole file, 116 lines) — Verb constants,
  Request/Response shape, AttachPayload/ResizePayload `omitempty` precedent.
  The new `VerbSessionsNew` constant and payload types slot into the same
  pattern (`sessionID,omitempty`, etc.).
- `internal/control/server.go:33-122` — existing `Session` /
  `SessionResolver` interface declarations and `NewServer` shape. The new
  `Sessioner` interface lives next to them; the new constructor parameter
  mirrors the optional `logs LogProvider` / `shutdown func()` plumbing.
- `internal/control/server.go:273-316` — the `handle` switch where
  `case VerbSessionsNew:` slots in alongside `VerbStatus` / `VerbStop` /
  `VerbAttach` / `VerbResize`.
- `internal/control/server.go:320-342` — `handleLogs` / `handleStop` are
  the single-purpose handler shapes the new `handleSessionsNew` mirrors
  (encode-error-or-OK, no streaming machinery).
- `internal/control/client.go` (whole file, 131 lines) — `Status` / `Logs` /
  `Stop` / `SendResize` are the model for the new `SessionsNew` client. All
  reuse `request()` (one-shot dial → encode → decode → close).
- `internal/control/server_test.go:20-99` — `fakeSession` /
  `fakeResolver` test doubles; the new `fakeSessioner` follows the same
  shape (mu-guarded recorded calls, configurable error).
- `internal/control/attach_resolve_test.go:26-37` —
  `TestAttach_WireBackCompat_EmptySessionID` is the byte-equality
  assertion pattern the new `omitempty` round-trip test reuses.
- `internal/sessions/pool.go:803-882` — `Pool.Create(ctx, label) (SessionID,
  error)` — the signature the `Sessioner` interface mirrors verbatim. Also
  confirms the empty-label semantics (`label == ""` → no-label session;
  Pool does not reject it).
- `internal/sessions/id.go:12-30` — `SessionID` is `type SessionID string`;
  `NewID()` returns `SessionID(fmt.Sprintf("%08x-%04x-...", ...))`. The wire
  carries a plain `string` (not `SessionID`) so external clients do not
  need to import the `sessions` package.
- `docs/knowledge/features/control-plane.md:23-59` — the resolver-seam
  section the new "Sessions: creation seam" subsection extends.
- `docs/lessons.md` § "Interface adapters for covariant returns" (lines
  22-24) — explains why most Pool methods need an adapter at the call
  site. `Pool.Create` returns `SessionID` (a primitive `string` newtype),
  not an interface — so it satisfies `Sessioner.Create` directly with no
  adapter, unlike `Pool.Lookup`.

## Context

The control plane currently exposes `status`, `stop`, `logs`, `attach`,
`resize`. Phase 1.1 adds five `sessions.*` verbs (`new`, `list`, `rename`,
`rm`, `attach`). This ticket lands the first one — `sessions.new` — plus
the conventions the other four reuse:

1. **Namespace.** Dot-delimited verb string `"sessions.new"`. The Verb
   type (`type Verb string`) imposes no syntactic constraint on the
   string; the dot is a documentation convention, not a parser rule. Locks
   the `sessions.<verb>` prefix as the Phase 1.1 namespace.
2. **Payload shape.** A new `Request.Sessions *SessionsPayload` field
   with `omitempty`, mirroring the `Attach` / `Resize` precedent. The
   payload is shared across the five `sessions.*` verbs (Phase 1.1b/c/d/e
   add fields like `Selector` for `list/rename/rm/attach` and `NewName`
   for `rename` to the same struct, all `omitempty`). One typed payload
   per namespace keeps the dispatch table flat.
3. **Response shape.** A new `Response.SessionsNew *SessionsNewResult`
   field with the minted UUID. Subsequent verbs add their own typed
   results (`Response.SessionsList`, etc.) — same pattern.
4. **Sessioner seam.** Consumer-side interface in `internal/control` with
   one method (`Create`) today. The other Phase 1.1 verbs grow the
   interface (one method each). Defined where consumed; satisfied by
   `*sessions.Pool` when wired.

This ticket's scope is **wire surface plus seam plumbing only**. No CLI,
no integration with `*sessions.Pool` (cmd/pyry/main.go is untouched
beyond the mechanical `nil` parameter expansion), no end-to-end test.
Tests use a fake `Sessioner`.

## Design

### Wire surface (protocol.go)

Add a Verb constant, a request payload, and a response payload. All new
fields carry `omitempty` so existing verbs serialise byte-identically.

```go
// In the verb block.
const (
    // ... existing verbs ...

    // VerbSessionsNew creates a new session. Request.Sessions carries an
    // optional human-friendly label; Response.SessionsNew carries the
    // minted session UUID.
    VerbSessionsNew Verb = "sessions.new"
)

// Request gains a Sessions field; existing fields unchanged.
type Request struct {
    Verb     Verb              `json:"verb"`
    Attach   *AttachPayload    `json:"attach,omitempty"`
    Resize   *ResizePayload    `json:"resize,omitempty"`
    Sessions *SessionsPayload  `json:"sessions,omitempty"` // populated for VerbSessionsNew (Phase 1.1+)
}

// SessionsPayload carries arguments shared across the sessions.* verb
// family. Today only Label is used (sessions.new); Phase 1.1b/c/d/e add
// further omitempty fields (Selector, NewName, ...).
//
// Label is the human-friendly name supplied by the client. Empty maps to
// a no-label session — Pool.Create accepts it verbatim and the registry
// stores ""; not an error.
type SessionsPayload struct {
    Label string `json:"label,omitempty"`
}

// Response gains a SessionsNew field.
type Response struct {
    Status      *StatusPayload         `json:"status,omitempty"`
    Logs        *LogsPayload           `json:"logs,omitempty"`
    SessionsNew *SessionsNewResult     `json:"sessionsNew,omitempty"` // populated for VerbSessionsNew
    OK          bool                   `json:"ok,omitempty"`
    Error       string                 `json:"error,omitempty"`
}

// SessionsNewResult carries the result of a successful sessions.new
// request. SessionID is the minted UUID as a string (not the
// sessions.SessionID newtype) so external clients need not import the
// sessions package.
type SessionsNewResult struct {
    SessionID string `json:"sessionID"`
}
```

**Naming rationale.** `Request.Sessions` (the verb-family payload) and
`Response.SessionsNew` (the verb-specific result) are deliberately
asymmetric. The request side groups by family because subsequent verbs in
the family will reuse the same struct (with different fields populated).
The response side splits per verb because each verb produces a different
result type — `SessionsList` returns a slice, `SessionsRename` returns
nothing (uses `OK`), etc. Forcing a single `Response.Sessions` envelope
would require a type switch on the client and create a marshal trap
where two response variants could populate at once. Per-verb response
fields with `omitempty` are the same pattern `Status` / `Logs` already
follow.

The `omitempty` tag on every new field is **load-bearing** — pinned by a
new test (`TestProtocol_SessionsRoundTripBackCompat`) that round-trips an
existing-verb request and asserts byte-identical output, mirroring
`TestAttach_WireBackCompat_EmptySessionID`'s shape.

### Sessioner interface (server.go)

Consumer-side, single method, mirrors `Pool.Create` verbatim. Defined
next to the existing `Session` and `SessionResolver` declarations.

```go
// Sessioner is the per-pool view the control server depends on for
// session creation (and, in Phase 1.1b/c/d/e, list/rename/rm/attach
// orchestration). *sessions.Pool satisfies it structurally. Defined
// here, where it is consumed; tests fake it directly.
//
// Create mints a new supervised session with the given (possibly empty)
// label and returns the new SessionID. Errors are surfaced to the
// client verbatim through Response.Error.
type Sessioner interface {
    Create(ctx context.Context, label string) (sessions.SessionID, error)
}
```

**Why a separate interface from `SessionResolver`.** Phase 0/1.0 split
"per-session view" (`Session`) from "lookup" (`SessionResolver`); the
same axis applies to "lifecycle commands" (`Sessioner`). Conflating them
would (a) force every existing fake resolver to grow Create stubs (4+
test doubles in 4 files), (b) mix read-side and write-side concerns on
one interface, and (c) preempt the Phase 1.1 follow-ups' freedom to
group their methods naturally. Per the package's existing convention
(small interfaces, defined at the consumer; cf. lessons.md "Interface
adapters for covariant returns"), Sessioner stands alone.

**No adapter needed.** `Pool.Create` returns `sessions.SessionID` (a
primitive `string` newtype). `Sessioner.Create` declares the same
return type. Concrete and interface signatures match exactly, so
`*sessions.Pool` satisfies `Sessioner` directly — unlike `Pool.Lookup`,
which returns `*sessions.Session` and needs the `poolResolver` adapter
in `cmd/pyry/main.go` because `SessionResolver.Lookup` returns the
`control.Session` interface (no covariant returns in Go). The CLI
ticket wires the pool with one line: `srv := control.NewServer(sock,
poolResolver{pool}, ..., pool)`.

### Server constructor (server.go)

Add `sessioner Sessioner` as the **last** parameter, mirroring the
existing optional-dependency convention (`logs LogProvider`, `shutdown
func()` are both optional with nil-handler returning errors). When
nil, `handleSessionsNew` returns `Response{Error: "sessions.new: no
sessioner configured"}` — the Phase 1.0 precedent for "feature wired in
later ticket" (logs, shutdown).

```go
func NewServer(
    socketPath string,
    sessions   SessionResolver,
    logs       LogProvider,
    shutdown   func(),
    log        *slog.Logger,
    sessioner  Sessioner, // nil-OK; VerbSessionsNew returns error response if nil
) *Server {
    // ... existing nil-check on sessions resolver, log defaulting ...
    return &Server{
        // ... existing fields ...
        sessioner: sessioner,
    }
}
```

**Why a constructor parameter instead of a setter.** The existing
optional dependencies (`logs`, `shutdown`) are constructor params; a
setter would break the convention and introduce a window where the
server is observable but partially configured. The 26 existing call
sites for `NewServer` get a mechanical `, nil` appended; per-file
`replace_all` collapses each file's update to a single Edit
(unmechanical paths: cmd/pyry/main.go, where the `nil` is conscious;
plus 4 test files where multiple sites share `, nil, nil, nil)`
suffixes that `replace_all` rewrites uniformly to `, nil, nil, nil,
nil)`).

### Server dispatch (server.go)

Add a `case VerbSessionsNew:` branch to `handle`'s switch and a new
`handleSessionsNew` method.

```go
// In handle(), inside the switch:
case VerbSessionsNew:
    s.handleSessionsNew(enc, req.Sessions)

// New method, modelled on handleStop/handleLogs.
func (s *Server) handleSessionsNew(enc *json.Encoder, payload *SessionsPayload) {
    if s.sessioner == nil {
        _ = enc.Encode(Response{Error: "sessions.new: no sessioner configured"})
        return
    }
    var label string
    if payload != nil {
        label = payload.Label
    }
    // Independent ctx with a generous deadline. Pool.Create's documented
    // critical-path latency is dominated by claude spawn (2-15s typical).
    // 30s leaves margin without leaving a hung handler if the pool blocks.
    // The handshake deadline on conn was already armed in handle(); it
    // expires after handshakeTimeout (5s) but Pool.Create runs after the
    // request decode, so the deadline fires only on a stuck server-side
    // encode of the response. Acceptable.
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    id, err := s.sessioner.Create(ctx, label)
    if err != nil {
        _ = enc.Encode(Response{Error: err.Error()})
        return
    }
    _ = enc.Encode(Response{SessionsNew: &SessionsNewResult{SessionID: string(id)}})
}
```

**Error wire shape.** The handler surfaces the error's `%v` string
verbatim — no `"sessions.new: "` prefix wrap. This matches `handleLogs`
("logs: no log provider configured") and `handleStop` precedents where
the prefix appears only when the server itself diagnoses the missing
dependency. Real `Pool.Create` errors (e.g.
`"sessions: create supervisor: ..."`) already carry their own
package-prefixed context; double-prefixing them would be noise.

The "underlying error's message preserved" AC is satisfied because
`err.Error()` is verbatim — no `fmt.Errorf("sessions.new: %w", err)`
unwrap-and-rewrap that could lose the original.

**Why a fresh background context, not the conn's.** The conn's deadline
was set at handle entry (`handshakeTimeout = 5s`) for request decode;
extending it across a 2-15s claude spawn would either (a) require
clearing the deadline mid-handler — uncomfortable surface area — or (b)
race the handshake timer. A fresh `context.WithTimeout(30s)` for the
Pool.Create call is the simpler shape; the encode-back-on-conn write
that happens after still completes well inside the 5s handshake window
(it's microseconds). If a future operator complains about the 30s
ceiling, raise it in this method without touching the conn lifecycle.

### Client wrapper (client.go)

```go
// SessionsNew asks the daemon to mint a new session with the given
// (possibly empty) label. Returns the new session's UUID.
//
// In-process Go callers (the future cmd/pyry sessions new) consume this
// directly. Same one-shot dial → encode → decode → close lifecycle as
// Status/Logs/Stop/SendResize.
func SessionsNew(ctx context.Context, socketPath, label string) (string, error) {
    resp, err := request(ctx, socketPath, Request{
        Verb:     VerbSessionsNew,
        Sessions: &SessionsPayload{Label: label},
    })
    if err != nil {
        return "", err
    }
    if resp.Error != "" {
        return "", errors.New(resp.Error)
    }
    if resp.SessionsNew == nil || resp.SessionsNew.SessionID == "" {
        return "", errors.New("control: empty sessions.new response")
    }
    return resp.SessionsNew.SessionID, nil
}
```

**Empty-label note.** `SessionsNew(ctx, sock, "")` still sends
`{"verb":"sessions.new","sessions":{}}`. The `Sessions` field is
non-nil so the server's payload-nil branch is not hit; `Label`
omitempty inside the payload means the inner JSON is `{}`. Both server
paths (`payload == nil` → `label = ""`; `payload.Label == ""`) produce
identical behaviour, so the client doesn't need to micro-optimise the
nil-vs-empty-payload distinction.

### Data flow

```
 Future CLI client                            Server                       Pool (#73)
 ─────────────────                            ──────                       ──────────
 SessionsNew(ctx, sock, "feature-x")
   │
   ▼
 dial unix sock
 encode {verb:"sessions.new", sessions:{label:"feature-x"}}
   ──────────────────────────────►
                                              decode → switch
                                                case VerbSessionsNew:
                                                 handleSessionsNew(enc, req.Sessions)
                                                  s.sessioner.Create(ctx, "feature-x")
                                                                              ──►
                                                                                Pool.Create →
                                                                                  NewID, supervise, Activate
                                                                              ◄──
                                                                              (id, nil)
                                                  encode {sessionsNew:{sessionID:"<uuid>"}}
   ◄──────────────────────────────
 decode → return id, nil
```

Error path is symmetric: any non-nil error from `sessioner.Create`
becomes `{error:"<err.Error()>"}`; client returns
`errors.New(resp.Error)`.

### Concurrency

No new mutexes, channels, or goroutines.

- One additional handler-goroutine variant from `Server.Serve`'s accept
  loop (the existing per-conn goroutine handles `VerbSessionsNew` exactly
  the same way it handles other one-shot verbs).
- `Pool.Create` is documented as safe for concurrent use (lessons.md
  "Buffered-signal lifecycle and caller-ctx cancellation"). Two
  simultaneous `sessions.new` requests serialise inside `Pool.mu`'s
  registration window, then run their respective Activate paths off-lock.
  The control server adds no coordination on top.
- The `streamingWG` pattern is **not** used — `VerbSessionsNew` is a
  one-shot verb, not a streaming verb. The handler returns before the
  per-conn goroutine completes; the conn closes via the existing `defer
  conn.Close()` path in `handle`.

### Error handling

| Failure | Wire `Response.Error` | Source |
|---|---|---|
| `sessioner == nil` (this ticket: always nil at server boot) | `"sessions.new: no sessioner configured"` | `handleSessionsNew` early return |
| `Pool.Create` returns `sessions: create id: <err>` | `"sessions: create id: <err>"` (verbatim) | Pool's own wrap |
| `Pool.Create` returns `sessions: create supervisor: <err>` | `"sessions: create supervisor: <err>"` | Pool's own wrap |
| `Pool.Create` Activate failure (returns `(id, err)`) | `"<activate err>"` | Pool's own message |
| 30s handler-ctx timeout | `"context deadline exceeded"` | ctx |
| Client decode failure mid-flight | propagated from `request()` as `"read response: <err>"` | client.go |

The "underlying error's message preserved" AC is asserted via a fake
`Sessioner` that returns `errors.New("create id: read random: io: read failed")`;
the wire response's Error must contain that exact substring.

**Important non-error case:** `Pool.Create` may return `(id, ctx.Err())`
when the caller's ctx cancels mid-Activate (lessons.md "Buffered-signal
lifecycle and caller-ctx cancellation"). Per Pool's contract, `id` is
valid (the registry has the entry) and the lifecycle goroutine
respects the pool's run-context, not the caller's. `handleSessionsNew`
treats this as an error (the Sessioner.Create signature does not
distinguish "id valid despite error"); the wire response carries the
error string, the client sees a failure. The session still exists in
the registry — operationally, a subsequent `pyry sessions list` (Phase
1.1b) will surface it. This is acceptable for Phase 1.1a-B1 since the
30s ctx timeout is well past Pool's Activate latency; the error path is
reachable only by external ctx cancellation, which the wire client (one
fresh background context per request) does not initiate.

## Testing strategy

New file: `internal/control/sessions_new_test.go` (~250 LOC). Stdlib
`testing` only.

Mandatory tests, mapping to AC items:

1. **`TestProtocol_SessionsRoundTripBackCompat`** — AC#2. Marshals a
   `Request{Verb: VerbStatus}` and asserts the byte output is
   `{"verb":"status"}` (no `sessions` field leaks). Mirrors
   `TestAttach_WireBackCompat_EmptySessionID`.

2. **`TestServer_SessionsNew_Success`** — AC#4 success path. Fake
   `Sessioner` returns `(sessions.SessionID("11111111-2222-3333-4444-555555555555"), nil)`
   regardless of input. Client calls `SessionsNew(ctx, sock, "my-label")`;
   asserts:
   - returned UUID matches the canned value
   - the fake recorded one Create call with `label == "my-label"`
   - `resp.Error` is empty
   - `resp.SessionsNew.SessionID` decodes to the canned value

3. **`TestServer_SessionsNew_EmptyLabel`** — AC#1 empty-label semantics.
   Same as above but `SessionsNew(ctx, sock, "")`. Fake records
   `label == ""`.

4. **`TestServer_SessionsNew_PoolError`** — AC#4 error path. Fake
   `Sessioner` returns `("", errors.New("sessions: create supervisor: pty start: ..."))`.
   Client call returns an error whose `Error()` contains the verbatim
   inner string.

5. **`TestServer_SessionsNew_NoSessionerConfigured`** — nil-Sessioner
   branch. Server constructed with `sessioner: nil`. Client call returns
   error `"sessions.new: no sessioner configured"`.

6. **`TestSessionsNew_PassesLabelOnWire`** — AC#1 wire shape. Hand-rolled
   `net.Listen` server (mirrors `TestAttach_EmptySessionIDOmittedOnWire`)
   captures the raw bytes the client sends. For
   `SessionsNew(ctx, sock, "alpha")`, the captured bytes contain
   `"sessions":{"label":"alpha"}` and `"verb":"sessions.new"`. For
   `SessionsNew(ctx, sock, "")`, the captured bytes contain
   `"sessions":{}` (label omitted via `omitempty`).

7. **`TestSessionsNew_DecodesEmptyResponseAsError`** — defensive client
   shape check. Hand-rolled server returns `Response{}` (no Error, no
   SessionsNew). Client returns `"control: empty sessions.new response"`.

Test doubles:

```go
// fakeSessioner satisfies control.Sessioner. Safe under concurrent use.
type fakeSessioner struct {
    mu          sync.Mutex
    createCalls []string         // recorded labels in order
    returnID    sessions.SessionID
    returnErr   error
}

func (f *fakeSessioner) Create(ctx context.Context, label string) (sessions.SessionID, error) {
    f.mu.Lock()
    f.createCalls = append(f.createCalls, label)
    id, err := f.returnID, f.returnErr
    f.mu.Unlock()
    return id, err
}

func (f *fakeSessioner) recordedCalls() []string {
    f.mu.Lock()
    defer f.mu.Unlock()
    return append([]string(nil), f.createCalls...)
}
```

`startServer` (existing helper in `server_test.go`) gains a sibling
`startServerWithSessioner(t, resolver, sessioner)` — copy the existing
helper, change the `NewServer` call to pass `sessioner` instead of `nil`
at the end. Two helpers, no flag-to-existing-helper, since the existing
`startServer` is called from many tests in many files; broadening its
signature is the high-fan-out edit the architect-side red-line warns
against.

`go test -race ./internal/control/...` must pass (AC#7). `go vet ./...`
must be clean (AC#8). No new staticcheck violations.

### What's out of scope for tests

- No integration test against a real `*sessions.Pool`. The CLI ticket
  exercises end-to-end; this ticket's contract is the wire+seam, and
  the fake Sessioner exercises the contract exactly.
- No test that `*sessions.Pool` satisfies `Sessioner`. Compile-time:
  `var _ Sessioner = (*sessions.Pool)(nil)` would create an import
  cycle. The CLI ticket is the only place where both packages are
  visible; that's where the satisfaction check naturally lives.
  (Optionally, the dev may add this check at the top of cmd/pyry/main.go
  alongside the existing `var _ control.Session = (*sessions.Session)(nil)`
  if any — but defer to the CLI ticket.)

## Documentation

Update `docs/knowledge/features/control-plane.md`. New subsection between
"Resolver Seam" and "Attach: ResolveID-then-Lookup":

```markdown
## Sessions: creation seam (1.1a-B1)

The `sessions.*` verb namespace begins with `sessions.new`. The server
consumes session-lifecycle commands through one consumer-side interface,
defined in `internal/control` next to `Session` / `SessionResolver`:

    type Sessioner interface {
        Create(ctx context.Context, label string) (sessions.SessionID, error)
    }

`*sessions.Pool` satisfies `Sessioner` directly — `Pool.Create` returns
`sessions.SessionID` (a primitive `string` newtype), no covariant-return
adapter needed (contrast with `poolResolver`'s `Lookup` adapter).

Wired as the last (optional) parameter to `NewServer`:

    NewServer(socketPath, resolver, logs, shutdown, log, sessioner)

When nil (Phase 1.1a-B1: always nil at server boot), `VerbSessionsNew`
returns `Response.Error == "sessions.new: no sessioner configured"` —
matching the existing precedent for `logs LogProvider` /
`shutdown func()`.

### Wire shape

`Request.Sessions *SessionsPayload` is the verb-family payload — Phase
1.1b/c/d/e add fields to the same struct (Selector, NewName, ...) under
omitempty. `Response.SessionsNew *SessionsNewResult` is the per-verb
result; Phase 1.1b/c/d/e add their own `SessionsList`, etc. (the
`OK`/`Error` envelope handles verbs without a typed result, like
sessions.rename).

    type SessionsPayload struct {
        Label string `json:"label,omitempty"`
    }

    type SessionsNewResult struct {
        SessionID string `json:"sessionID"`
    }

Empty label maps to a no-label session (Pool.Create accepts ""
verbatim). Empty-payload omitempty on the request envelope keeps
existing-verb wire output byte-identical — pinned by
`TestProtocol_SessionsRoundTripBackCompat`.

### Error wire shape

Errors from `Pool.Create` flow to `Response.Error` verbatim through
`err.Error()` — no `"sessions.new: "` prefix wrap. Pool's own messages
already carry package context (`sessions: create supervisor: ...`).
The only `"sessions.new: "`-prefixed error is the diagnostic for
"sessioner not wired" (mirrors `"logs: no log provider configured"`).

### Why a separate Sessioner interface

`SessionResolver` is the read seam; `Sessioner` is the write seam.
Conflating them would force every existing fake resolver (4+ test
doubles in 4 files) to grow Create stubs, mix read/write concerns on
one interface, and preempt the Phase 1.1 follow-ups' grouping
freedom. Per the package's existing convention (small interfaces,
defined at the consumer; cf. lessons.md "Interface adapters for
covariant returns"), Sessioner stands alone.
```

This subsection is the template the four follow-on Phase 1.1 tickets
extend (each adding one paragraph with the new verb's payload field
and result type, plus one method on Sessioner).

## Open questions

1. **Should `handleSessionsNew` rate-limit?** Today, no — control socket
   is `0600`-restricted to the owning user, and Pool's own resource
   bounds (active cap, idle eviction) limit damage. Phase 2.x's remote
   access surface will need this question revisited.

2. **Does the 30s handler ctx need to be configurable?** Today, no —
   the documented Pool.Create latency is well below it, and a stuck
   Pool is a real bug we should surface, not paper over with a longer
   timeout. If Phase 1.1's CLI ticket wants per-call control, propose
   threading ctx through the SessionsNew client signature in that
   ticket, not preemptively here.

3. **Do we want a server-side log line on each successful sessions.new?**
   Pool.Create already emits structured logs at the lifecycle
   boundaries; an additional one in the control handler would be
   noise. Defer until operator feedback says otherwise.
