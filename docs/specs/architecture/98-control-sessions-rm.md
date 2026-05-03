# #98 — control-plane verb `sessions.rm` + `Remover` seam

Phase 1.1d-B1. Wire-only addition. Adds the second `sessions.<verb>`
namespace member to `internal/control` and a consumer-side `Remover`
interface that `*sessions.Pool` satisfies structurally via its existing
`Pool.Remove`. No `cmd/pyry` work — the `pyry sessions rm` CLI router and
prefix resolution are sibling tickets.

This spec mirrors the structure of #75 (`sessions.new`) and reuses every
convention #75 locked in. The two notable additions over #75 are:

1. A wire-level **error-code field** (`Response.ErrorCode`) so typed
   sentinels (`sessions.ErrSessionNotFound`,
   `sessions.ErrCannotRemoveBootstrap`) propagate through JSON in a way
   that survives `errors.Is`. #75 didn't need this — `Pool.Create` has no
   typed sentinels worth matching.
2. A wire-level **JSONL policy enum** (string values "leave" / "archive"
   / "purge") sitting in `internal/control` next to the existing
   primitives. The duplicate-vs-import choice is documented below.

## Files to read first

- `internal/control/protocol.go` (whole file, 144 lines) — Verb
  constants, Request/Response shape, `SessionsPayload`/`AttachPayload`/
  `ResizePayload` `omitempty` precedent. The new `VerbSessionsRm`
  constant, the new fields on `SessionsPayload` (`ID`, `JSONLPolicy`),
  the new `JSONLPolicy` wire enum, the new `ErrorCode` wire enum, and
  the new `Response.ErrorCode` field all slot into this file.
- `internal/control/server.go:33-142` — existing `Session` /
  `SessionResolver` / `Sessioner` interface declarations and `NewServer`
  shape. The new `Remover` interface lives next to them; `Sessioner`
  embeds it (rationale below). `NewServer` signature is **unchanged**.
- `internal/control/server.go:293-338` — the `handle` switch where
  `case VerbSessionsRm:` slots in alongside `VerbSessionsNew`.
- `internal/control/server.go:366-395` — `handleSessionsNew` is the
  single-purpose handler the new `handleSessionsRm` mirrors (encode-
  error-or-OK, fresh 30s background ctx, no streaming).
- `internal/control/client.go` (whole file, 158 lines) — `Status` /
  `Logs` / `Stop` / `SendResize` / `SessionsNew` are the model for the
  new `SessionsRm` client. All reuse `request()` (one-shot dial →
  encode → decode → close).
- `internal/control/sessions_new_test.go` (whole file, 304 lines) — the
  template for `sessions_rm_test.go`. The new file follows the same
  `fakeSessioner`/`startServerWithSessioner` shape; **the existing
  `fakeSessioner` gains a `Remove` method** (one method on one struct,
  not a new file) so `*fakeSessioner` continues to satisfy `Sessioner`.
- `internal/control/sessions_new_test.go:77-88` —
  `TestProtocol_SessionsRoundTripBackCompat` is the byte-equality
  precedent the new test extends to assert that the new `ID`/
  `JSONLPolicy`/`ErrorCode` fields don't perturb existing-verb output.
- `internal/sessions/pool.go:31-38` — the `ErrSessionNotFound` and
  `ErrCannotRemoveBootstrap` sentinel definitions. `Pool.Remove`
  returns these **bare** (no wrap), so `err.Error()` equals the
  sentinel's `Error()` — relevant to the client wire-error mapping
  decision (return the sentinel directly; no wrap dup).
- `internal/sessions/pool.go:431-521` — `JSONLPolicy` enum, `JSONLLeave`/
  `JSONLArchive`/`JSONLPurge` constants, `RemoveOptions` struct, and
  `Pool.Remove(ctx, id, opts) error` signature. The `Remover` interface
  mirrors this signature verbatim.
- `docs/specs/architecture/75-control-sessions-new.md` (whole file) —
  the precedent spec. Sections "Wire surface (protocol.go)",
  "Sessioner interface (server.go)", "Server constructor (server.go)",
  "Server dispatch (server.go)", "Client wrapper (client.go)" all have
  direct analogues here. The "Naming rationale" subsection's argument
  for verb-family request payloads + per-verb response payloads
  applies to `sessions.rm` identically (request reuses
  `SessionsPayload`; response uses `OK`/`Error`/`ErrorCode` envelope —
  no typed result needed).
- `docs/knowledge/features/control-plane.md` § "Sessions: creation
  seam (1.1a-B1)" — the subsection to extend with a "Sessions: removal
  seam (1.1d-B1)" companion. Same template, new method/payload/result.
- `docs/lessons.md` § "Interface adapters for covariant returns"
  (lines 22-24) — confirms `Pool.Remove` returns plain `error` (no
  covariant return), so `*sessions.Pool` satisfies `Remover` directly,
  no adapter (mirrors `Pool.Create` / `Sessioner`).

## Context

The control plane currently exposes `status`, `stop`, `logs`, `attach`,
`resize`, `sessions.new`. This ticket adds `sessions.rm`. Two
operational shapes the wire surface must support:

1. **Carry `(id, JSONLPolicy)` on the request.** The CLI needs to ask
   the daemon to terminate a specific session's child, drop its
   registry entry, and either leave / archive / purge the on-disk
   JSONL transcript.
2. **Propagate two typed error sentinels.** `Pool.Remove` returns
   `ErrSessionNotFound` for unknown IDs and `ErrCannotRemoveBootstrap`
   for the bootstrap entry. The CLI sibling will branch on these to
   produce different exit codes / hints. `errors.Is` is the canonical
   matcher; the wire shape must let the client reconstruct the
   sentinel after JSON round-trip.

This ticket's scope is **wire surface plus seam plumbing only**. No
CLI, no `cmd/pyry/main.go` change (Pool already satisfies the extended
interface — see "Why not a new constructor parameter" below), no
prefix resolution, no flag parsing.

## Design

### Wire surface (protocol.go)

Add a `VerbSessionsRm` constant. Extend `SessionsPayload` with `ID`
and `JSONLPolicy` fields (both `omitempty`). Add a `JSONLPolicy` wire
enum and an `ErrorCode` wire enum. Add a `Response.ErrorCode` field
(`omitempty`).

```go
const (
    // ... existing verbs ...

    // VerbSessionsRm removes an existing session. Request.Sessions
    // carries the session ID and JSONL disposition policy;
    // Response.OK acknowledges success. Typed errors from the pool
    // (ErrSessionNotFound, ErrCannotRemoveBootstrap) propagate
    // through Response.ErrorCode so the CLI can match them with
    // errors.Is.
    VerbSessionsRm Verb = "sessions.rm"
)

// JSONLPolicy is the wire-level enum selecting how the daemon
// disposes of a removed session's on-disk JSONL transcript file.
// Empty string is treated as JSONLPolicyLeave (backward-compat /
// zero-value ergonomics, same default as sessions.JSONLLeave).
type JSONLPolicy string

const (
    JSONLPolicyLeave   JSONLPolicy = "leave"
    JSONLPolicyArchive JSONLPolicy = "archive"
    JSONLPolicyPurge   JSONLPolicy = "purge"
)

// SessionsPayload gains two omitempty fields. Existing verbs
// (sessions.new with Label-only) serialise byte-identically — pinned
// by TestProtocol_SessionsRoundTripBackCompat (extended).
type SessionsPayload struct {
    Label        string      `json:"label,omitempty"`         // sessions.new
    ID           string      `json:"id,omitempty"`            // sessions.rm
    JSONLPolicy  JSONLPolicy `json:"jsonlPolicy,omitempty"`   // sessions.rm
}

// ErrorCode is a stable wire token identifying a typed server-side
// error. Empty when the response carries no typed sentinel; the
// server still populates Response.Error with the human-readable
// message in every error case.
type ErrorCode string

const (
    // ErrCodeSessionNotFound is set by the server when Pool.Remove
    // returns sessions.ErrSessionNotFound. The client maps this
    // back to the same sentinel so callers can errors.Is against it.
    ErrCodeSessionNotFound ErrorCode = "session_not_found"

    // ErrCodeCannotRemoveBootstrap is set by the server when
    // Pool.Remove returns sessions.ErrCannotRemoveBootstrap.
    ErrCodeCannotRemoveBootstrap ErrorCode = "cannot_remove_bootstrap"
)

// Response gains an ErrorCode field. Empty string is the zero value
// (omitempty), so non-error responses and untyped errors round-trip
// byte-identically vs. pre-1.1d-B1 servers/clients.
type Response struct {
    Status      *StatusPayload     `json:"status,omitempty"`
    Logs        *LogsPayload       `json:"logs,omitempty"`
    SessionsNew *SessionsNewResult `json:"sessionsNew,omitempty"`
    OK          bool               `json:"ok,omitempty"`
    Error       string             `json:"error,omitempty"`
    ErrorCode   ErrorCode          `json:"errorCode,omitempty"` // NEW (1.1d-B1)
}
```

**Why extend `SessionsPayload`, not add a separate `SessionsRmPayload`.**
The ticket body's example (`Request.SessionsRm *SessionsRmPayload`)
explicitly defers the choice to the architect ("Architect picks
whether..."). #75 locked in the verb-family-payload pattern: one
typed payload struct per namespace, omitempty fields per verb. This
ticket continues that pattern — Phase 1.1b/c/e (`list`, `rename`,
`attach`) will add their own omitempty fields (`Selector`, `NewName`,
`SessionID` etc.) to the same struct. A separate `SessionsRm` field
on `Request` would force three more (`SessionsList`,
`SessionsRename`, `SessionsAttach`) and start drifting toward the
"one envelope field per verb" shape #75 deliberately rejected.

The trade-off — `Label` is conceptually present (always omitted via
`omitempty`) on `sessions.rm` requests and `ID`/`JSONLPolicy` are
conceptually present on `sessions.new` — is a known cost of the
verb-family pattern. The wire bytes stay clean (omitempty drops
unused fields); only the in-memory struct shape is broader than any
single verb consumes. Acceptable.

**Why duplicate `JSONLPolicy` as a wire-level type rather than import
`sessions.JSONLPolicy`.** Three reasons:

1. `protocol.go` currently has zero imports — wire types are
   primitives, intentionally decoupled from the supervisor packages so
   external Go callers don't need to drag in the `sessions` package
   transitively.
2. `sessions.JSONLPolicy` is `uint8`. Marshalling `uint8` as JSON
   produces an integer (`0`, `1`, `2`), opaque to anyone reading the
   wire with `jq`. A string enum (`"leave"`, `"archive"`, `"purge"`)
   is self-documenting and durable across protocol versions if the
   underlying enum order ever changes.
3. The translation lives in one place (`handleSessionsRm`) and is a
   single switch — cheaper than the alternative coupling.

The cost is one `toSessionsPolicy` helper in `server.go`. Acceptable.

**Why a new `Response.ErrorCode` field rather than message-string
matching.** The CLI sibling will need `errors.Is(err,
sessions.ErrSessionNotFound)`. After a JSON round-trip the only
mechanical signal the client has is the bytes the server wrote; the
options are:

- **Match on `Response.Error` text.** Fragile — couples the wire
  contract to sentinel error message strings. Renaming a sentinel
  message becomes a wire-protocol break.
- **Add `Response.ErrorCode`.** Stable token, decoupled from message
  text. Server checks `errors.Is` once; client maps token → sentinel
  once. This is the same shape gRPC / HTTP-status conventions use.

The new field is `omitempty` so non-`sessions.rm` responses round-
trip byte-identically (asserted by the extended back-compat test).

The error code design is **introduced now, scoped to two values** —
one for each sentinel `Pool.Remove` exposes. Phase 1.1c (`rename`)
and 1.1e (`attach`) may extend it (e.g.
`ErrCodeAmbiguousSessionID`, `ErrCodeSessionNotFound` reused) on the
same envelope. No anticipatory expansion in this ticket.

### Remover interface (server.go)

Consumer-side, single method, mirrors `Pool.Remove` verbatim. Defined
next to the existing `Sessioner` declaration. **Embedded into
`Sessioner`** so NewServer's signature stays unchanged.

```go
// Remover is the per-pool view the control server depends on for
// session removal. *sessions.Pool satisfies it structurally via
// Pool.Remove. Defined here, where it is consumed; tests fake it
// directly.
//
// Remove terminates the named session's child, drops its registry
// entry, and applies opts.JSONL to the on-disk transcript file.
// Returns sessions.ErrSessionNotFound for an unknown id,
// sessions.ErrCannotRemoveBootstrap for the bootstrap entry, or
// ctx.Err() if termination is cancelled. See Pool.Remove for the
// full contract.
type Remover interface {
    Remove(ctx context.Context, id sessions.SessionID, opts sessions.RemoveOptions) error
}

// Sessioner aggregates the lifecycle methods the control server
// dispatches to. Phase 1.1a-B1 added Create; Phase 1.1d-B1 adds
// Remove via the embedded Remover. Phase 1.1b/c/e (list, rename,
// attach orchestration) will continue this pattern — one method
// per verb, embedded onto Sessioner so NewServer's signature stays
// stable across the namespace's growth.
type Sessioner interface {
    Create(ctx context.Context, label string) (sessions.SessionID, error)
    Remover
}
```

**Why embed `Remover` in `Sessioner` instead of adding a new
constructor parameter.** This is the ticket's most consequential
choice and it directly addresses the failure mode #75 hit. Three
options were considered:

1. **Add a 7th `NewServer` parameter (`remover Remover`).** Mirrors
   the `sessioner Sessioner` parameter #75 added. **Rejected.**
   `grep -rn 'NewServer(' internal/ cmd/` returns **27 call sites**
   today. Each new optional dependency that takes the parameter
   route fans out to all 27 sites with a mechanical `, nil` append.
   The architect prompt's edit-fan-out red line trips at >10 call
   sites. #75 sized a 26-call-site cascade as S, hit max_turns at
   61 turns, was salvaged only by the pipeline's safer-salvage
   layer. Repeating that decision here is not "the same trade-off"
   — it is the failure mode the post-#75 architect prompt was
   rewritten to prevent.

2. **Switch `NewServer` to a `Config` struct.** Eliminates the
   cascade going forward and is the "right" long-term shape.
   **Rejected for THIS ticket.** Refactoring `NewServer`'s signature
   is a 27-call-site edit on its own — that *is* the cascade we're
   trying to avoid. It would also expand scope beyond "wire +
   seam" into a constructor refactor. A Config-struct migration
   belongs in a dedicated ticket.

3. **Embed `Remover` in `Sessioner`.** **Chosen.** The single
   `sessioner` parameter already exists; making `Sessioner`
   require both `Create` and `Remove` adds the new seam without
   touching `NewServer`'s signature. `*sessions.Pool` already has
   both methods (`Pool.Create` shipped in #73; `Pool.Remove`
   shipped in #94/#95) so it continues to satisfy `Sessioner` with
   no `cmd/pyry/main.go` change. The only consumer cascade is the
   single `fakeSessioner` in `sessions_new_test.go`, which gains
   one `Remove` method on its existing struct (one in-place edit,
   not a new fake type). Total cascade: **1 file, 1 method**, well
   under the red line.

The shape this commits us to: each subsequent Phase 1.1 verb that
needs a seam grows `Sessioner` by one method (or by embedding a
named sub-interface). This is the intended growth path —
small composable interfaces (`Remover`), aggregated at the consumer
boundary (`Sessioner`), with `*sessions.Pool` satisfying the union
through its existing public surface. `Sessioner` becomes the
"session lifecycle facade" the control server depends on; named
sub-interfaces (`Remover`, future `Renamer` / `Lister` / etc.) stay
addressable for tests that want to fake one method without
implementing the others.

`Remover` exists as a named interface (per the AC: "a `Remover`
interface in `internal/control` whose method shape matches
`Remove(ctx, id, opts) error`"). The fact that it is embedded in
`Sessioner` rather than directly consumed by `Server.handle` does
not contradict the AC — `Remover`'s contract is the seam; how the
Server depends on it (via the `Sessioner` aggregate) is an
implementation detail of the server's interface composition.

**No adapter needed.** `Pool.Remove` returns plain `error` and
takes `(context.Context, sessions.SessionID, sessions.RemoveOptions)`.
`Remover.Remove` declares the same signature. Concrete and
interface match exactly, so `*sessions.Pool` satisfies `Remover`
(and by transitivity, `Sessioner`) directly — no covariant-return
adapter (contrast with `poolResolver` for `Lookup`).

### Server constructor (server.go)

**Unchanged.** No new parameters. The existing
`sessioner Sessioner` parameter now requires `Remove` to satisfy
the broader interface; `*sessions.Pool` already does.

The nil-handling diagnostic for `VerbSessionsRm` mirrors the existing
one for `VerbSessionsNew`: when `s.sessioner == nil`,
`handleSessionsRm` returns
`Response{Error: "sessions.rm: no sessioner configured"}`.

### Server dispatch (server.go)

Add a `case VerbSessionsRm:` branch to `handle`'s switch and a new
`handleSessionsRm` method.

```go
// In handle(), inside the switch:
case VerbSessionsRm:
    s.handleSessionsRm(enc, req.Sessions)

// New method, modelled on handleSessionsNew.
func (s *Server) handleSessionsRm(enc *json.Encoder, payload *SessionsPayload) {
    if s.sessioner == nil {
        _ = enc.Encode(Response{Error: "sessions.rm: no sessioner configured"})
        return
    }
    if payload == nil || payload.ID == "" {
        _ = enc.Encode(Response{Error: "sessions.rm: missing id"})
        return
    }
    policy, err := toSessionsPolicy(payload.JSONLPolicy)
    if err != nil {
        _ = enc.Encode(Response{Error: fmt.Sprintf("sessions.rm: %v", err)})
        return
    }
    // Independent ctx — same rationale as handleSessionsNew. Pool.Remove
    // blocks until the child exits (modulo ctx cancellation); 30s leaves
    // ample headroom over the supervisor's SIGTERM→SIGKILL ladder
    // (~5s in pool.go's evict path).
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    err = s.sessioner.Remove(ctx, sessions.SessionID(payload.ID), sessions.RemoveOptions{JSONL: policy})
    if err != nil {
        resp := Response{Error: err.Error()}
        switch {
        case errors.Is(err, sessions.ErrSessionNotFound):
            resp.ErrorCode = ErrCodeSessionNotFound
        case errors.Is(err, sessions.ErrCannotRemoveBootstrap):
            resp.ErrorCode = ErrCodeCannotRemoveBootstrap
        }
        _ = enc.Encode(resp)
        return
    }
    _ = enc.Encode(Response{OK: true})
}

// toSessionsPolicy maps the wire-level JSONLPolicy enum to the
// internal sessions.JSONLPolicy enum. Empty string maps to JSONLLeave
// — same as sessions.JSONLPolicy's zero value, so a client that
// omits the field gets the documented default. Unknown values
// surface as "unknown jsonl policy %q" in Response.Error.
func toSessionsPolicy(p JSONLPolicy) (sessions.JSONLPolicy, error) {
    switch p {
    case "", JSONLPolicyLeave:
        return sessions.JSONLLeave, nil
    case JSONLPolicyArchive:
        return sessions.JSONLArchive, nil
    case JSONLPolicyPurge:
        return sessions.JSONLPurge, nil
    default:
        return 0, fmt.Errorf("unknown jsonl policy %q", string(p))
    }
}
```

**Error wire shape.** Same convention as `handleSessionsNew`:

| Failure | `Response.Error` | `Response.ErrorCode` |
|---|---|---|
| `sessioner == nil` | `"sessions.rm: no sessioner configured"` | (empty) |
| Empty `ID` | `"sessions.rm: missing id"` | (empty) |
| Unknown JSONL policy string | `"sessions.rm: unknown jsonl policy "..."` | (empty) |
| `Pool.Remove` → `ErrSessionNotFound` | `"sessions: session not found"` (verbatim) | `"session_not_found"` |
| `Pool.Remove` → `ErrCannotRemoveBootstrap` | `"sessions: cannot remove bootstrap session"` (verbatim) | `"cannot_remove_bootstrap"` |
| `Pool.Remove` → other (e.g. `evictErr` wrapped) | `<err.Error()>` (verbatim) | (empty) |
| 30s handler-ctx timeout | `"context deadline exceeded"` | (empty; `errors.Is` against the typed sentinels won't match) |

The `"sessions.rm: "` prefix appears only on server-side diagnostics
(missing sessioner / missing id / bad policy). Real `Pool.Remove`
errors flow through verbatim — same convention as `handleSessionsNew`.

**Why `errors.Is` on the server side (not just string match).**
`Pool.Remove` returns the sentinels bare today, but a future change
could legitimately wrap them (e.g. `fmt.Errorf("removing %s: %w", id,
ErrSessionNotFound)`). The `errors.Is` check survives wrapping; the
typed `ErrorCode` continues to fire on the wire even if the message
string changes. This is the durability the wire-level error code
buys.

**Empty `ID` handling.** Rejected at the handler boundary with a
`Response.Error`. The alternative — passing `""` through to
`Pool.Remove` and letting it return `ErrSessionNotFound` — would
also work but produces the wrong ErrorCode (the empty ID isn't a
"not found" condition; it's a missing-input condition). Server-side
guard is clearer and avoids round-tripping through the pool's
locking critical section for what is unambiguously a malformed
request. (Compare with `handleResize` which guards `payload == nil`
the same way.)

### Client wrapper (client.go)

```go
// SessionsRm asks the daemon to remove a session by id and apply
// the JSONL disposition policy. Empty policy is treated as
// JSONLPolicyLeave by the server (matches sessions.JSONLLeave's
// zero-value default).
//
// Typed errors propagate via Response.ErrorCode — a server response
// carrying ErrCodeSessionNotFound returns sessions.ErrSessionNotFound
// directly so callers can errors.Is against it; same for
// ErrCodeCannotRemoveBootstrap. Other server errors (no sessioner
// configured, missing id, evict failures, ...) return as
// errors.New(resp.Error).
func SessionsRm(ctx context.Context, socketPath, id string, policy JSONLPolicy) error {
    resp, err := request(ctx, socketPath, Request{
        Verb:     VerbSessionsRm,
        Sessions: &SessionsPayload{ID: id, JSONLPolicy: policy},
    })
    if err != nil {
        return err
    }
    if resp.Error != "" {
        switch resp.ErrorCode {
        case ErrCodeSessionNotFound:
            return sessions.ErrSessionNotFound
        case ErrCodeCannotRemoveBootstrap:
            return sessions.ErrCannotRemoveBootstrap
        }
        return errors.New(resp.Error)
    }
    if !resp.OK {
        return errors.New("control: sessions.rm response missing ok flag")
    }
    return nil
}
```

**Why return the bare sentinel, not `fmt.Errorf("%s: %w", resp.Error,
sentinel)`.** `Pool.Remove` returns the sentinels bare today —
`err.Error()` equals `sentinel.Error()`, so `resp.Error` already
matches the sentinel's message verbatim. Wrapping with
`fmt.Errorf("%s: %w", resp.Error, sentinel)` would produce
`"sessions: session not found: sessions: session not found"` (double
prefix) on the client side. The simpler shape (return the sentinel
directly) preserves the message and supports `errors.Is`. If a
future change wraps the sentinel server-side with extra context,
that context is on the wire as `resp.Error` — and the right
response then is to introduce a small unexported `wireError` type
that carries both the message and the sentinel. Defer until needed.

**Imports.** This adds `internal/sessions` to `internal/control/
client.go`'s import list (today the client.go file imports only
stdlib). The dependency is intrinsic to the typed-error contract;
external callers that don't want the import have the option of
matching on `errors.Is(err, sessions.ErrSessionNotFound)` from their
own consumption site — the dependency arrow is unchanged.

### Data flow

```
 CLI client (sibling ticket)                 Server                            Pool (#94/#95)
 ──────────────────────────                  ──────                            ──────────────
 SessionsRm(ctx, sock, "<uuid>", "purge")
   │
   ▼
 dial unix sock
 encode {verb:"sessions.rm",
         sessions:{id:"<uuid>", jsonlPolicy:"purge"}}
   ──────────────────────────────►
                                              decode → switch
                                                case VerbSessionsRm:
                                                  handleSessionsRm(enc, req.Sessions)
                                                    toSessionsPolicy("purge") → JSONLPurge
                                                    sessioner.Remove(ctx, id, {JSONL: JSONLPurge})
                                                                                ──►
                                                                                  Pool.Remove
                                                                                    SIGTERM→SIGKILL child,
                                                                                    delete entry,
                                                                                    persist registry,
                                                                                    rm jsonl
                                                                                ◄──
                                                                                nil
                                                  encode {ok: true}
   ◄──────────────────────────────
 decode → return nil

 Error path (bootstrap):
                                                    sessioner.Remove(ctx, bootstrapID, ...)
                                                                                ──►
                                                                                  → ErrCannotRemoveBootstrap
                                                                                ◄──
                                                  errors.Is(err, ErrCannotRemoveBootstrap) → true
                                                  encode {error:"sessions: cannot remove bootstrap session",
                                                          errorCode:"cannot_remove_bootstrap"}
   ◄──────────────────────────────
 decode → ErrorCode == ErrCodeCannotRemoveBootstrap →
                       return sessions.ErrCannotRemoveBootstrap
 caller errors.Is(err, sessions.ErrCannotRemoveBootstrap) → true
```

### Concurrency

No new mutexes, channels, or goroutines.

- `VerbSessionsRm` is a one-shot verb. The existing per-conn handler
  goroutine in `Server.Serve` handles it identically to other one-
  shot verbs. The `streamingWG` pattern is **not** used.
- `Pool.Remove` is documented as safe under concurrent use against
  other pool writers (it serialises behind `Pool.mu` for the
  delete + persist + JSONL disposition critical section, then
  releases the lock before calling `sess.Evict` — see
  `pool.go:477-495`). The control server adds no coordination on
  top.
- One subtle interleave: a `sessions.rm` request for an in-flight
  `sessions.new`'s minted ID could in principle race the new
  session's bootstrap. `Pool.Create` finishes registering the entry
  under `Pool.mu` before returning — by the time the wire response
  carries the new ID, the registry has it. A subsequent
  `sessions.rm` is FIFO-after — no race observable to the client.
  The control plane doesn't need to add ordering on top.

### Error handling

Beyond the wire-shape table above:

- **Unknown JSONL policy.** Server-side guard. The wire enum is
  open at the type level (it's a `string` newtype, no compile-time
  exhaustiveness) so a forward-incompatible client (e.g. a future
  `"compress"` policy not yet implemented server-side) gets a
  clear `"unknown jsonl policy "compress""` error rather than
  silent fallback to `JSONLLeave`. Default to "fail loudly" —
  same convention as the rest of the protocol.

- **Decode-error propagation.** `request()` already wraps
  `json.Decoder.Decode` errors as `"read response: <err>"` — no
  change needed. A server response with both `Error` and `OK:true`
  is malformed; the client treats `Error != ""` as the dominant
  signal (matches existing helpers).

- **30s ctx timeout.** Bound by `Pool.Remove`'s SIGTERM→SIGKILL
  ladder (~5s) plus registry persist. 30s is generous; if a real-
  world Pool.Remove is observed to need more, raise this constant
  in this method without touching the conn lifecycle. (Same
  precedent as `handleSessionsNew`.)

## Testing strategy

New file: `internal/control/sessions_rm_test.go` (~280 LOC). Stdlib
`testing` only.

`fakeSessioner` (in `sessions_new_test.go`) gains a `Remove` method
**in place** — not a new fake type:

```go
// In sessions_new_test.go, on the existing fakeSessioner struct.

type removeCall struct {
    ID   sessions.SessionID
    Opts sessions.RemoveOptions
}

type fakeSessioner struct {
    mu          sync.Mutex
    createCalls []string
    removeCalls []removeCall   // NEW
    returnID    sessions.SessionID
    returnErr   error           // shared between Create and Remove
    // OR, if a test needs to differentiate: returnRemoveErr error
}

func (f *fakeSessioner) Remove(_ context.Context, id sessions.SessionID, opts sessions.RemoveOptions) error {
    f.mu.Lock()
    f.removeCalls = append(f.removeCalls, removeCall{ID: id, Opts: opts})
    err := f.returnErr
    f.mu.Unlock()
    return err
}

func (f *fakeSessioner) recordedRemoves() []removeCall {
    f.mu.Lock()
    defer f.mu.Unlock()
    return append([]removeCall(nil), f.removeCalls...)
}
```

**Decision on `returnErr` sharing.** The simplest path: one
`returnErr` is returned by both `Create` and `Remove`. Tests that
need to differentiate can set the field per-test (each test has its
own `fakeSessioner` instance). If a test ever needs Create to
succeed while Remove fails (or vice versa) **inside the same
fakeSessioner**, add a separate `returnRemoveErr` field then; YAGNI
otherwise. None of the AC items require it.

Mandatory tests, mapping to AC items:

1. **`TestProtocol_RmRoundTripBackCompat`** — AC#2 byte-equality.
   Asserts:
   - `Request{Verb: VerbStatus}` marshals to `{"verb":"status"}`
     (re-asserts the existing pin; new fields don't perturb it).
   - `Request{Verb: VerbSessionsNew, Sessions: &SessionsPayload{Label: "x"}}`
     marshals to `{"verb":"sessions.new","sessions":{"label":"x"}}`
     — the new `ID`/`JSONLPolicy` fields' omitempty tags hold.
   - `Response{}` marshals to `{}` — `ErrorCode`'s omitempty holds.
   - `Response{OK: true}` marshals to `{"ok":true}` — same.
   These can live as one test with three table rows.

2. **`TestServer_SessionsRm_Success`** — AC#1, AC#3 success path.
   `fakeSessioner` returns nil. Client calls `SessionsRm(ctx, sock,
   "<uuid>", JSONLPolicyArchive)`; asserts:
   - Returned error is nil.
   - The fake recorded one Remove call with the canned ID and
     `RemoveOptions{JSONL: sessions.JSONLArchive}`.
   - Server response decodes with `OK: true`, no `Error`,
     no `ErrorCode`.

3. **`TestServer_SessionsRm_PolicyEachValue`** — AC#7 coverage of
   each `JSONLPolicy` value. Table-driven over
   `{ "", "leave", "archive", "purge" }`; asserts the fake
   received the matching `sessions.JSONLPolicy` (`""` and
   `"leave"` both → `sessions.JSONLLeave`).

4. **`TestServer_SessionsRm_ErrSessionNotFound`** — AC#3 typed-
   error propagation. `fakeSessioner.returnErr =
   sessions.ErrSessionNotFound`. Client call must:
   - return a non-nil error
   - `errors.Is(err, sessions.ErrSessionNotFound)` → true
   - `err.Error()` contains `"sessions: session not found"`

5. **`TestServer_SessionsRm_ErrCannotRemoveBootstrap`** — AC#3
   typed-error propagation, second sentinel. Same structure as #4
   with `sessions.ErrCannotRemoveBootstrap`.

6. **`TestServer_SessionsRm_OtherPoolError`** — error path with a
   non-typed error (e.g. `errors.New("sessions: persist registry:
   ...")`). Client must:
   - return a non-nil error whose `Error()` contains the verbatim
     inner message
   - `errors.Is(err, sessions.ErrSessionNotFound)` → **false**
   - `errors.Is(err, sessions.ErrCannotRemoveBootstrap)` → **false**

7. **`TestServer_SessionsRm_NoSessionerConfigured`** — nil-Sessioner
   branch. Server constructed with `sessioner: nil`. Client call
   returns error `"sessions.rm: no sessioner configured"`.

8. **`TestServer_SessionsRm_MissingID`** — empty-ID guard. Client
   passes `id == ""`. Returns `"sessions.rm: missing id"`. Fake
   sessioner must record **zero** Remove calls (the guard fires
   before the seam call).

9. **`TestServer_SessionsRm_BadPolicy`** — unknown-policy guard.
   Client built with a hand-rolled `Request` containing
   `JSONLPolicy: "bogus"` (the `request()` helper takes a
   `Request`, not a typed `policy` arg, so this is reachable
   without extending the wrapper). Returns `"sessions.rm: unknown
   jsonl policy \"bogus\""`. Fake sessioner must record zero
   Remove calls.

10. **`TestSessionsRm_PassesArgsOnWire`** — wire shape. Hand-rolled
    `net.Listen` server (mirrors
    `TestSessionsNew_PassesLabelOnWire`) captures the raw client-
    encoded line. Table:
    - `(id="11111111-...", policy=JSONLPolicyArchive)` → contains
      `"verb":"sessions.rm"` and
      `"sessions":{"id":"11111111-...","jsonlPolicy":"archive"}`
    - `(id="22222222-...", policy=JSONLPolicyLeave)` → contains
      `"sessions":{"id":"22222222-...","jsonlPolicy":"leave"}`
    - `(id="33333333-...", policy="")` → contains
      `"sessions":{"id":"33333333-..."}` (empty policy drops via
      omitempty)

11. **`TestSessionsRm_DecodesEmptyResponseAsError`** — defensive
    client-shape check. Hand-rolled server returns `Response{}`
    (no Error, no OK). Client returns
    `"control: sessions.rm response missing ok flag"`.

`go test -race ./internal/control/...` must pass (AC#7-row-1).
`go vet ./...` must be clean (AC#7-row-2). No new staticcheck
violations.

### What's out of scope for tests

- **No integration test against `*sessions.Pool`.** The CLI ticket
  exercises end-to-end; this ticket's contract is wire+seam, and
  the fake Sessioner exercises the contract exactly.
- **No `var _ Sessioner = (*sessions.Pool)(nil)` in the test
  package.** Same reason as #75 — would create an import cycle.
  `cmd/pyry/main.go` is the natural site for that compile-time
  check; it already exists for the broader `Sessioner` interface
  (`pool` is passed verbatim to `NewServer`'s sessioner argument).
  The dev may add a comment on that line noting it now also
  satisfies `Remover` via `Pool.Remove`; not mandatory.
- **No prefix-resolution test.** Out of scope per the ticket
  body. `payload.ID` flows verbatim to `Pool.Remove`. The CLI
  sibling will either resolve client-side (preferred) or extend
  `handleSessionsRm` to call `ResolveID` first; either is a clean
  follow-on.

## Documentation

Update `docs/knowledge/features/control-plane.md`. Add a subsection
**after** "Sessions: creation seam (1.1a-B1)":

```markdown
## Sessions: removal seam (1.1d-B1)

The second `sessions.*` verb is `sessions.rm`. The control server
consumes session-removal commands through the embedded `Remover`
interface in `internal/control`:

    type Remover interface {
        Remove(ctx context.Context, id sessions.SessionID, opts sessions.RemoveOptions) error
    }

    type Sessioner interface {
        Create(ctx context.Context, label string) (sessions.SessionID, error)
        Remover
    }

`*sessions.Pool` satisfies `Remover` directly via `Pool.Remove`
(shipped in #94/#95). Aggregating `Remover` (and future per-verb
sub-interfaces) into `Sessioner` keeps `NewServer`'s signature
stable as the namespace grows — see ADR-NNN (or this spec's
"Why embed Remover in Sessioner" rationale) for the alternative
considered (constructor parameter per seam).

### Wire shape

`SessionsPayload` gains two omitempty fields used by `sessions.rm`:

    type SessionsPayload struct {
        Label       string      `json:"label,omitempty"`        // sessions.new
        ID          string      `json:"id,omitempty"`            // sessions.rm
        JSONLPolicy JSONLPolicy `json:"jsonlPolicy,omitempty"`   // sessions.rm
    }

    type JSONLPolicy string  // "leave" | "archive" | "purge" (empty = leave)

`sessions.rm` uses the `OK`/`Error` envelope — no typed result.

### Typed errors via Response.ErrorCode

Two sentinels propagate from `Pool.Remove`:

    Response.ErrorCode == "session_not_found"        → sessions.ErrSessionNotFound
    Response.ErrorCode == "cannot_remove_bootstrap"  → sessions.ErrCannotRemoveBootstrap

The client maps these to the corresponding sentinel error so callers
can match with `errors.Is`. Untyped errors (e.g. evict failures)
flow through `Response.Error` verbatim with no `ErrorCode`.

### JSONL policy translation

The wire enum (string) and the internal enum (`sessions.JSONLPolicy`,
uint8) are deliberately distinct types. `protocol.go` stays import-
free; the translation (`toSessionsPolicy`) lives in `server.go` next
to the handler. Strings are jq-debuggable and durable across protocol
versions if the underlying enum order ever changes. Unknown wire
values surface as `"unknown jsonl policy %q"` in `Response.Error`.
```

## Open questions

1. **Should the server resolve a prefix before calling `Pool.Remove`?**
   Today, no — `payload.ID` flows verbatim. The CLI sibling decides
   where prefix resolution lives (client-side via a separate
   `ResolveID`-like wire call, or server-side by extending
   `handleSessionsRm` to invoke `s.sessions.ResolveID(payload.ID)`
   before calling `Remove`). Either lands cleanly on top of this
   ticket's surface; this spec leaves the door open in both
   directions.

2. **Should `Sessioner` be split into `Creator` + `Remover` (and
   future `Renamer`, etc.) named sub-interfaces?** Today, only
   `Remover` is named (because the AC names it). `Create` lives
   directly on `Sessioner`. If Phase 1.1c (`rename`) needs a
   `Renamer` for symmetry, it can be promoted at that time. No
   anticipatory factoring.

3. **Should `Response.ErrorCode` be reused for `sessions.new` errors?**
   Today, no — `Pool.Create`'s errors are not currently typed
   sentinels the CLI needs to match. If that changes (e.g. an
   `ErrLabelInUse` is introduced), the same envelope handles it
   without a new wire field. The infrastructure is in place.

4. **Do we want a server-side log line on each successful
   sessions.rm?** Pool.Remove already emits structured logs at the
   lifecycle boundaries (evict, JSONL disposition); an additional
   one in the control handler would be noise. Defer until operator
   feedback says otherwise — same answer as #75.
