# #157 — control-plane verb `sessions.has-id` (cheap UUID-existence query)

Phase 1.3c-1. Wire-only addition. Adds the fifth `sessions.<verb>`
namespace member (`new` / `rm` / `rename` / `list`, plus this verb) to
`internal/control`. No `cmd/pyry` changes. No new `Sessioner`
sub-interface — the registry-read primitive this handler needs is
already exposed via the existing `SessionResolver.Lookup` seam (see
"Why no new sub-interface" below).

The closest precedent is **#87 (`sessions.list`)**: a read-only,
no-typed-sentinel verb. The differences are:

1. The request **does** carry a payload (the UUID to query) — handler
   reuses the existing `SessionsPayload.ID` field, no new wire field.
2. The handler **does** validate input — empty/malformed UUIDs are
   rejected at the boundary as `Response.Error`.
3. The response is a single boolean wrapped in a typed result struct,
   not a slice.

Consumer (1.3c-2's auto-attach detection) is out of scope.

## Files to read first

- `internal/control/protocol.go` (whole file, 267 lines) — `Verb`
  constants block (lines 16-71), `Request` (lines 109-114) reusing
  `SessionsPayload`, `SessionsPayload` (lines 163-168) which already
  carries the `ID` field this verb consumes, `Response` (lines 191-199)
  where the new `SessionsHasID` field slots in next to `SessionsList`,
  the existing `SessionsNewResult` struct (lines 205-207) — closest
  precedent for a small typed result payload. The new
  `VerbSessionsHasID` and `SessionsHasIDResult` types slot in here. **No
  new `SessionsPayload` field; no new `ErrorCode` member.**
- `internal/control/server.go:33-149` — `Session` / `SessionResolver`
  / `Remover` / `Renamer` / `Lister` / `Sessioner` declarations. **No
  new sub-interface.** `Sessioner` is unchanged (`s.sessioner` is not
  consulted by this verb). The handler reaches for `s.sessions`
  (`SessionResolver`), which is non-nil by `NewServer` precondition.
- `internal/control/server.go:388-421` — the `handle` switch where
  `case VerbSessionsHasID:` slots in alongside `VerbSessionsList`.
- `internal/control/server.go:495-524` — `handleSessionsRm` is the
  closest precedent for the **payload boundary check** (`payload ==
  nil || payload.ID == ""` → `"sessions.rm: missing id"`); reuse the
  same shape with the new prefix.
- `internal/control/server.go:563-595` — `handleSessionsList` is the
  closest precedent for the **handler body** (no `context.WithTimeout`,
  no typed-sentinel mapping, single Encode call). The new handler
  mirrors its shape.
- `internal/control/client.go:188-217` — `SessionsList` is the closest
  precedent for the client wrapper (single result + error, no
  typed-sentinel switch). The new `SessionsHasID` wrapper mirrors it
  with one structural difference: the request carries a payload
  (`SessionsPayload{ID: id}`), like `SessionsRename` (lines 168-186).
- `internal/sessions/id.go:34-69` — `ValidID(s string) bool`. The
  handler calls this to reject malformed UUIDs at the boundary
  (matches AC: "malformed input returns an error"). Empty strings are
  already rejected by the missing-id boundary check, but `ValidID("")`
  also returns false — the validation is a strict superset.
- `internal/sessions/pool.go:596-607` — `Pool.Lookup`'s contract. The
  registry-read primitive this verb consumes through
  `SessionResolver.Lookup`. Returns `(*Session, ErrSessionNotFound)`
  for unknown IDs, `(*Session, nil)` for known ones; the empty-id →
  bootstrap branch is gated upstream by `ValidID`.
- `internal/control/sessions_new_test.go:18-174` — `fakeSessioner`
  shape. **Not extended by this ticket** (no new `Sessioner`
  sub-interface). The `TestProtocol_SessionsRoundTripBackCompat` table
  at lines 181-233 is the back-compat regression — extend with rows
  pinning the new `Response.SessionsHasID` omitempty (see Testing).
- `internal/control/sessions_list_test.go` (whole file) — closest test
  template for the new `sessions_has_id_test.go` file. Reuses
  `startServerWithSessioner` and `fakeResolver` verbatim.
- `docs/specs/architecture/87-control-sessions-list.md` — direct
  precedent for "no typed sentinel, read-only verb" pattern. The
  rationale for `time.Time` and `state` encoding from #87 does not
  apply here (no time / enum on the wire).
- `docs/specs/architecture/98-control-sessions-rm.md` — precedent for
  the handler-boundary missing-id validation pattern. This verb shares
  that shape (unlike list, which has no payload).
- `docs/lessons.md` § "Wire-level error code over message-string
  matching" — relevant context for why this verb introduces no new
  `ErrorCode` (no typed sentinels propagate from the registry-read
  path).

## Context

The control plane currently exposes `status`, `stop`, `logs`, `attach`,
`resize`, `sessions.new`, `sessions.rm`, `sessions.rename`,
`sessions.list`. This ticket adds `sessions.has-id`. Two operational
shapes the wire surface must support:

- **One-bit existence query.** The 1.3c-2 auto-attach detection asks,
  "does this UUID currently exist in the daemon's session registry?".
  `sessions.list` returns the entire registry; we want a single
  boolean for ~36-byte input + ~30-byte output instead of an O(N)
  table.
- **Strict yes/no semantics.** A well-formed UUID that is absent
  returns `false`, not an error — absence is the cheap signal the
  caller wants to act on. A malformed (non-UUID) input is a programmer
  error and surfaces through `Response.Error`.

Pure verb-pattern work. Additive on the wire; existing verbs'
byte-output is unchanged.

## Design

### Wire surface (protocol.go)

Add a `VerbSessionsHasID` constant. Add one new wire-level result
struct (`SessionsHasIDResult`) and one new field on `Response`
(`SessionsHasID`). **No new `SessionsPayload` field** — the existing
`ID` field already carries "the session this verb operates on" for
`sessions.rm` and `sessions.rename`, and is reused verbatim here. **No
new `ErrorCode`** — the typed-sentinel envelope #98 introduced is
unused.

```go
const (
    // ... existing verbs through VerbSessionsList ...

    // VerbSessionsHasID asks whether a session is currently registered
    // with the given UUID. Request.Sessions.ID carries the UUID;
    // Response.SessionsHasID carries the boolean answer. Pure
    // registry read — no claude spawn, no state transition. The
    // 1.3c-2 foreground auto-attach path consumes this as a cheap
    // alternative to sessions.list. Empty / malformed input returns
    // Response.Error; a well-formed but absent UUID returns
    // {Has: false}.
    VerbSessionsHasID Verb = "sessions.has-id"
)
```

```go
// SessionsHasIDResult carries the boolean answer to a sessions.has-id
// query. Has is emitted unconditionally (no omitempty) so the wire
// distinguishes "id absent" ({"has":false}) from a malformed empty
// response ({}). Defined here, in protocol.go, so external Go callers
// don't transitively import internal/sessions.
type SessionsHasIDResult struct {
    Has bool `json:"has"`
}
```

`Response` gains one omitempty field:

```go
type Response struct {
    Status        *StatusPayload         `json:"status,omitempty"`
    Logs          *LogsPayload           `json:"logs,omitempty"`
    SessionsNew   *SessionsNewResult     `json:"sessionsNew,omitempty"`
    SessionsList  *SessionsListPayload   `json:"sessionsList,omitempty"`
    SessionsHasID *SessionsHasIDResult   `json:"sessionsHasID,omitempty"` // NEW (1.3c-1)
    OK            bool                   `json:"ok,omitempty"`
    Error         string                 `json:"error,omitempty"`
    ErrorCode     ErrorCode              `json:"errorCode,omitempty"`
}
```

#### Why reuse `SessionsPayload.ID` instead of adding a new field

`SessionsPayload.ID` was introduced by #98 (sessions.rm) for "the
session this verb operates on" and reused verbatim by #90
(sessions.rename) for the same purpose. `sessions.has-id` carries the
same semantic — "the session this verb operates on" — and a redundant
parallel field (e.g. `QueryID`) would inflate the wire and split the
concept across two field names. The PO ACs phrase this as
`SessionsPayload` "extended by one omitempty field carrying the UUID
to query (same payload shape as other sessions verbs)"; **the same
field shape as other sessions verbs is the existing `ID` field**, so
no new field is added. The omitempty tag on `ID` is already
load-bearing for the v0.5.x rollover (see #98 spec) and is unaffected.

#### Why `Has bool` is **not** omitempty

`omitempty` elides the zero value. With `Has bool` + `omitempty`, the
wire shape for an absent session would be `{"sessionsHasID":{}}` —
indistinguishable from a malformed response that forgot to populate
the field. Emitting the boolean unconditionally costs ~10 bytes on the
wire and produces an unambiguous shape (`{"sessionsHasID":{"has":false}}`
or `{"sessionsHasID":{"has":true}}`). The wrapper-pointer's omitempty
on `Response.SessionsHasID` is sufficient to keep other verbs'
byte-output unchanged.

#### Why no new `ErrorCode`

The handler's only registry call goes through
`SessionResolver.Lookup`, whose only typed error is
`sessions.ErrSessionNotFound` — and that case is **not** propagated as
an error to the client; it's the absence-signal the verb returns as
`Has: false`. Boundary-validation errors (missing/invalid UUID) are
plain strings, not typed sentinels — no `errors.Is` matching the
caller could meaningfully do beyond reading the message.

### Server dispatch (server.go)

`handle`'s switch gains one new case:

```go
switch req.Verb {
// ... existing cases through VerbSessionsList ...
case VerbSessionsHasID:
    s.handleSessionsHasID(enc, req.Sessions)
default:
    _ = enc.Encode(Response{Error: fmt.Sprintf("unknown verb: %q", req.Verb)})
}
```

The handler:

```go
// handleSessionsHasID serves a VerbSessionsHasID request: report
// whether a session is currently registered under the given UUID.
// Pure registry read — no claude spawn, no state transition, no
// context.WithTimeout (s.sessions.Lookup is in-memory and bounded by
// Pool.mu RLock; the conn's handshake deadline is sufficient).
//
// Empty ID is rejected at the boundary (a missing-input condition,
// not an "absent" one) for symmetry with handleSessionsRm /
// handleSessionsRename. Malformed (non-UUIDv4) ID is also rejected at
// the boundary — Pool.Lookup would return ErrSessionNotFound for any
// non-canonical string regardless, but failing fast at the seam
// distinguishes "client typed garbage" from "well-formed UUID that
// happens to be absent". Per AC.
//
// Lookup returns (*Session, ErrSessionNotFound) for unknown ids,
// (*Session, nil) for known. The Session is discarded — the handler
// only cares whether the entry exists. Any non-ErrSessionNotFound
// error (theoretically unreachable today; defensive against future
// Pool.Lookup error growth) is surfaced verbatim.
func (s *Server) handleSessionsHasID(enc *json.Encoder, payload *SessionsPayload) {
    if payload == nil || payload.ID == "" {
        _ = enc.Encode(Response{Error: "sessions.has-id: missing id"})
        return
    }
    if !sessions.ValidID(payload.ID) {
        _ = enc.Encode(Response{Error: "sessions.has-id: invalid uuid"})
        return
    }
    _, err := s.sessions.Lookup(sessions.SessionID(payload.ID))
    if err != nil && !errors.Is(err, sessions.ErrSessionNotFound) {
        _ = enc.Encode(Response{Error: fmt.Sprintf("sessions.has-id: %v", err)})
        return
    }
    has := err == nil
    _ = enc.Encode(Response{SessionsHasID: &SessionsHasIDResult{Has: has}})
}
```

Notes:

- **No `context.WithTimeout`.** `Pool.Lookup` is an O(1) map read
  under `Pool.mu` RLock. The handshake deadline on the conn already
  bounds slow-write clients.
- **No `s.sessioner` involvement.** The verb does not need the
  Sessioner aggregate; the registry-read primitive is on
  `SessionResolver`. Daemons constructed with `sessioner = nil` (none
  exist today, but the construction surface allows it) still answer
  this verb correctly.
- **No prefix resolution.** Unlike attach (which calls `ResolveID` to
  accept loose-input prefixes), has-id is strict-UUID by AC: malformed
  input returns an error rather than a boolean. The point of the verb
  is "given the canonical UUID I expect, does it exist?" — prefix
  ambiguity is meaningless in that context.
- **`sessions.ValidID` is a strict superset of "non-empty"**, so the
  empty-id check could in principle be folded into the ValidID check.
  Kept separate because the error message is more useful for the
  empty case (`"missing id"` vs `"invalid uuid"`) — same split as
  `handleSessionsRm` / `handleSessionsRename`.

### Why no new sub-interface (`HasIDer`)

The pattern established by #87/#90/#98 is "one named sub-interface per
verb, embedded in `Sessioner`, satisfied structurally by
`*sessions.Pool`." That pattern fits when the seam needs a method
that doesn't already exist on the resolver — `Pool.Create`,
`Pool.Remove`, `Pool.Rename`, `Pool.List`. For has-id, the registry
read is **already** reachable through the existing
`SessionResolver.Lookup` seam (returning `ErrSessionNotFound` for
unknown ids), and `*sessions.Pool` already satisfies it via the
`poolResolver` adapter (`cmd/pyry/main.go:355-359`). Adding a
parallel `HasIDer` interface + a parallel `Pool.HasID` method would
double the surface for the same registry read.

This is a "use what's there, don't add what isn't needed" call (see
the project's "Simplicity First" principle and CLAUDE.md's "Stdlib
over dependencies, abstractions over speculation"). If a future verb
needs a registry read that genuinely doesn't fit `Lookup`'s
`(*Session, error)` shape — e.g. a bulk-existence-check or a
prefix-existence query — that's the moment to introduce a new seam.

### Client wrapper (client.go)

```go
// SessionsHasID asks the daemon whether a session is currently
// registered under the given UUID. Returns true for a known UUID,
// false for a well-formed UUID that is absent, and an error for
// empty / malformed input or transport failure.
//
// In-process Go callers (the future 1.3c-2 foreground auto-attach
// path) consume this directly. Same one-shot dial → encode → decode
// → close lifecycle as Status/Logs/Stop/SendResize/SessionsNew/
// SessionsRm/SessionsRename/SessionsList.
//
// No typed-sentinel mapping. Server-side validation errors flow
// through Response.Error verbatim.
func SessionsHasID(ctx context.Context, socketPath, id string) (bool, error) {
    resp, err := request(ctx, socketPath, Request{
        Verb:     VerbSessionsHasID,
        Sessions: &SessionsPayload{ID: id},
    })
    if err != nil {
        return false, err
    }
    if resp.Error != "" {
        return false, errors.New(resp.Error)
    }
    if resp.SessionsHasID == nil {
        return false, errors.New("control: empty sessions.has-id response")
    }
    return resp.SessionsHasID.Has, nil
}
```

### Server constructor (server.go)

`NewServer`'s signature is **unchanged**. The doc comment at lines
192-195 is updated to mention has-id alongside the existing
new/rm/rename/list mentions — but only as documentation; the verb
does not actually require `sessioner`:

```go
// sessioner is optional. When nil, VerbSessionsNew, VerbSessionsRm,
// VerbSessionsRename, and VerbSessionsList all return error
// responses — same precedent as logs/shutdown. VerbSessionsHasID is
// independent of sessioner (consults SessionResolver instead) and
// answers correctly even with sessioner == nil.
```

`cmd/pyry/main.go` is **not edited**. The handler reaches for
`s.sessions` (already wired, non-nil by `NewServer` precondition).

### Concurrency

- **`handleSessionsHasID` runs on the per-conn goroutine.** No new
  goroutines spawned. The handler returns after one Encode call and
  the conn closes via the existing `defer` in `handle`.
- **Lock order: `Pool.mu` (RLock) only.** `Pool.Lookup` takes
  `p.mu.RLock` and does an O(1) map lookup. No `Session.lcMu`
  involvement. No new lock-order edges.
- **Read consistency.** A session minted between the handler's read
  and the client's decode will not appear; one removed will still
  appear. Acceptable for a yes/no query — the caller (1.3c-2's
  startup detection) treats the answer as a decision input, not a
  durable contract. Any acted verb (rm/new) serializes against this
  read through `Pool.mu`.

### Error handling

| Failure mode | Wire shape |
|---|---|
| `payload == nil` or empty `payload.ID` | `Response{Error: "sessions.has-id: missing id"}` |
| `payload.ID` is not a canonical UUIDv4 | `Response{Error: "sessions.has-id: invalid uuid"}` |
| `Lookup` returns `ErrSessionNotFound` | `Response{SessionsHasID: &SessionsHasIDResult{Has: false}}` (success) |
| `Lookup` returns nil (id present) | `Response{SessionsHasID: &SessionsHasIDResult{Has: true}}` (success) |
| `Lookup` returns other error (unreachable today) | `Response{Error: "sessions.has-id: <err>"}` |
| Decode failure on request | `Response{Error: "decode request: ..."}` (existing path in `handle`) |

## Testing strategy

New file: `internal/control/sessions_has_id_test.go` (~200 LOC).
Pattern mirrors `internal/control/sessions_list_test.go`. Reuses
`startServerWithSessioner` and `fakeResolver` verbatim. **No
`fakeSessioner` extension** — this verb does not consult the
sessioner.

The existing `fakeResolver` (in `client_test.go` or wherever it
lives) has a `Lookup(SessionID) (Session, error)` method already; if
its current shape doesn't easily express "id X is present, id Y is
absent", extend it with a small `present map[sessions.SessionID]bool`
field plus the matching Lookup branching. Keep the existing single-id
construction (`&fakeResolver{sess: ...}`) working unchanged for the
other tests.

| Test | What it asserts |
|---|---|
| `TestServer_SessionsHasID_Present` | Resolver answers Lookup(known) with a session; client receives `Has: true`. Resolver records exactly one Lookup call with the known SessionID. |
| `TestServer_SessionsHasID_Absent` | Resolver answers Lookup(unknown) with `ErrSessionNotFound`; client receives `Has: false`, no error. Pins the AC's "well-formed but absent → false, not error". |
| `TestServer_SessionsHasID_MissingID` | Client sends `{"verb":"sessions.has-id"}` (no payload); server returns `Response{Error: "sessions.has-id: missing id"}`. Same for an explicit empty-ID payload. |
| `TestServer_SessionsHasID_InvalidUUID` | Client sends a non-UUID id ("not-a-uuid", "11111111-2222-3333-4444-XXXXXXXXXXXX"); server returns `Response{Error: "sessions.has-id: invalid uuid"}`. Lookup is **not** called (resolver records zero Lookup calls) — pins fast-fail at the boundary. |
| `TestServer_SessionsHasID_LookupError` | Resolver answers Lookup with a non-`ErrSessionNotFound` error (e.g. `errors.New("boom")`); client receives `errors.New("sessions.has-id: boom")`. Defensive — unreachable on `*sessions.Pool` today; pins the contract for future Pool.Lookup error growth. |
| `TestClient_SessionsHasID_Wire` | Hand-rolled `net.Listen` that captures the JSON line — assert request bytes are exactly `{"verb":"sessions.has-id","sessions":{"id":"..."}}\n`. Pins the omitempty behavior on `SessionsPayload.{Label,JSONLPolicy,NewLabel}` (none of those fields appear). |
| `TestClient_SessionsHasID_EmptyResponse` | Server returns `Response{}` (no `SessionsHasID`, no `Error`); client returns `errors.New("control: empty sessions.has-id response")` — the meaningful empty-response guard. |
| `TestProtocol_SessionsHasIDResult_HasNotOmitempty` | Marshal a `SessionsHasIDResult{Has: false}` and assert the JSON output is exactly `{"has":false}` (omitempty would have produced `{}`). Pins the load-bearing not-omitempty design choice. |
| `TestProtocol_SessionsRoundTripBackCompat` (extended in `sessions_new_test.go`) | Add three rows: (a) `Response{}` still marshals to `{}` (omitempty on SessionsHasID holds); (b) `Response{OK: true}` still marshals to `{"ok":true}`; (c) `Response{SessionsList: &SessionsListPayload{Sessions: []SessionInfo{}}}` still marshals to its #87-form. Pins the byte-equality regression for existing verbs after the new field is added. |

The back-compat regression on `TestProtocol_SessionsRoundTripBackCompat`
is the load-bearing assertion the AC names ("Existing verbs' wire
output is byte-identical"). It already includes rows for empty
Response and for `Response{SessionsNew: ...}`; the developer **adds**
a row for the SessionsList shape (so #87's invariant continues to
hold) and confirms (rather than re-adds) the omitempty pins. The
existing rows pass unmodified.

## Open questions

None. Field reuse, no-new-interface choice, error envelope (unused),
and validation shape are all settled by precedent (#87, #98) and the
AC. If the existing `fakeResolver` shape proves awkward for the
present/absent split, extend it inside the test file rather than
reshaping the production seam.

## Out of scope

- The 1.3c-2 foreground auto-attach detection that consumes this verb.
- A CLI verb (`pyry sessions has-id` or similar) — the AC scopes the
  caller to a Go in-process client wrapper.
- Prefix resolution / loose-input acceptance — has-id is
  strict-UUID by AC.
- Adding a `Pool.HasID` primitive or a `HasIDer` interface — the
  registry read goes through the existing `SessionResolver.Lookup`
  seam.

## Documentation

Update `docs/knowledge/features/control-plane.md`:

- Add a "Sessions: has-id seam (1.3c-1)" subsection mirroring the
  existing list/rename/rm subsections. Document the verb name, the
  payload reuse of `SessionsPayload.ID`, the `SessionsHasIDResult`
  wire type, the not-omitempty `Has` boolean rationale, the
  `SessionResolver.Lookup`-not-Sessioner choice, and the strict-UUID
  validation gate.

After editing, run `qmd update && qmd embed` (per CLAUDE.md).

Update `docs/PROJECT-MEMORY.md` after the developer ticket lands —
add a "Phase 1.3c-1 (#157)" entry under control-plane work, mirroring
the rename / rm / new / list entries' shape (verb name, seam,
file layout, test diff size).
