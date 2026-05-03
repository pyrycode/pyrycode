# #87 — control-plane verb `sessions.list` + `Lister` seam

Phase 1.1b-B1. Wire-only addition. Adds the fourth `sessions.<verb>`
namespace member (`status`, `attach`, `resize`, plus `sessions.new` /
`sessions.rm` / `sessions.rename`) to `internal/control` and a
consumer-side `Lister` interface that `*sessions.Pool` satisfies
structurally via its existing `Pool.List` (delivered by #60). No
`cmd/pyry` work — the `pyry sessions list` CLI verb (`case "list":` in
the router, table renderer, `--json` flag, "no pyry running"
diagnostic) is sibling ticket 61-B.

This spec mirrors #90 (`sessions.rename`) closely. The infrastructure
established by #75/#98/#90 — the `Sessioner`-aggregates-named-
sub-interfaces pattern — is reused here verbatim. The novel surface
this ticket adds is small:

1. One new verb constant (`VerbSessionsList`).
2. One new wire-level payload struct (`SessionsListPayload`) and one
   new wire-level entry struct (`SessionInfo` in `protocol.go`).
3. One new `omitempty` field on `Response` (`SessionsList`).
4. One new named interface (`Lister`), embedded into `Sessioner`.
5. One new handler (`handleSessionsList`) and one new client wrapper
   (`SessionsList`).

`Pool.List` does not return errors. The only seam-side failure path is
the existing nil-sessioner branch ("sessions.list: no sessioner
configured"), so **no new `ErrorCode` constant is introduced** and the
typed-sentinel envelope #98 introduced is not exercised here.

## Files to read first

- `internal/control/protocol.go` (whole file, 211 lines) — `Verb`
  constants, `Request` (note: `sessions.list` carries **no** payload;
  `Request.Sessions` stays nil for this verb), `Response` (the new
  `SessionsList` field slots in next to `SessionsNew`), the existing
  `SessionsNewResult` struct (the closest precedent for a verb-specific
  result payload). The new `VerbSessionsList`, `SessionsListPayload`,
  and wire-level `SessionInfo` types slot in here. **No new `ErrorCode`
  member.**
- `internal/control/server.go:33-112` — `Session` / `SessionResolver` /
  `Remover` / `Renamer` / `Sessioner` declarations. The new `Lister`
  interface lives next to `Renamer`; `Sessioner` gains `Lister` as a
  third embedded interface (alongside the existing `Remover` and
  `Renamer` embeds). `NewServer` signature is **unchanged**.
- `internal/control/server.go:350-381` — the `handle` switch where
  `case VerbSessionsList:` slots in alongside `VerbSessionsRename`.
- `internal/control/server.go:486-521` — `handleSessionsRename` is the
  handler `handleSessionsList` mirrors most closely (same nil-sessioner
  shape, same lack of `context.WithTimeout`). Note the differences
  called out below: no payload validation, no typed-sentinel mapping,
  no missing-id guard.
- `internal/control/client.go:155-186` — `SessionsRename` is the model
  for the new `SessionsList` client wrapper, with two structural
  differences: (a) returns a slice + error instead of bare error,
  (b) no `Response.ErrorCode` switch (no typed sentinels to map).
  `SessionsNew` (lines 96-121) is a closer template for the
  "non-empty payload required on success" assertion.
- `internal/control/sessions_new_test.go` (whole file, 356 lines) — the
  template for `sessions_list_test.go`. The shared `fakeSessioner`
  (lines 21-60) gains a `List` method + `recordedListCalls` counter
  (or equivalent — see Testing); the
  `TestProtocol_SessionsRoundTripBackCompat` table (lines 98-140)
  gains one row asserting that the new `Response.SessionsList`
  omitempty tag holds for non-`sessions.list` responses. The
  `startServerWithSessioner` harness is reused as-is.
- `internal/sessions/pool.go:177-242` — `SessionInfo` (internal),
  `Pool.List() []SessionInfo`, and the bootstrap-label substitution
  rule. The `Lister` interface mirrors `Pool.List` exactly: no args,
  no ctx, returns `[]sessions.SessionInfo`.
- `internal/sessions/session.go:24-51` — `lifecycleState` enum +
  `String()` (returns `"active"` / `"evicted"`) + `parseLifecycleState`.
  The handler converts the internal enum to a wire-level string via
  `e.LifecycleState.String()` — same encoding as the on-disk registry.
- `docs/specs/architecture/90-control-sessions-rename.md` (whole file)
  — the precedent. Sections "Wire surface (protocol.go)", "Renamer
  interface (server.go)", "Server constructor (server.go)", "Server
  dispatch (server.go)", "Client wrapper (client.go)", "Concurrency",
  and "Testing strategy" all have direct analogues here. The "Why
  embed Renamer in Sessioner instead of adding a new constructor
  parameter" rationale applies identically and is **not** re-litigated
  below — the embedding pattern is now the established shape for
  `sessions.<verb>` seams.
- `docs/specs/architecture/75-control-sessions-new.md` § "Naming
  rationale" — the verb-family-payload pattern (one typed payload
  struct per namespace, omitempty fields per verb). `sessions.list`
  diverges minimally from that pattern: it carries **no** request
  payload, so `SessionsPayload` gains no new field. The verb name
  alone is the dispatch token; the Sessions field on Request stays
  nil for this verb.
- `docs/knowledge/features/control-plane.md` — the doc that gains a
  "Sessions: list seam (1.1b-B1)" subsection mirroring the existing
  rename / rm seam subsections.
- `docs/lessons.md` § "Wire-level error code over message-string
  matching" / § "Wire enums: prefer self-documenting strings" — the
  applicable conventions for any cross-boundary enum on the wire.
  `SessionInfo.State` is encoded as a self-documenting string
  (`"active"` / `"evicted"`), reusing `lifecycleState.String()`'s
  output verbatim — the same encoding the on-disk registry uses, so
  nothing new on the translation surface.

## Context

The control plane currently exposes `status`, `stop`, `logs`, `attach`,
`resize`, `sessions.new`, `sessions.rm`, `sessions.rename`. This ticket
adds `sessions.list`. Three operational shapes the wire surface must
support:

- **Snapshot every session in the pool.** The CLI needs a single
  read of all sessions (bootstrap and minted alike) with enough
  metadata to render a table or `--json` blob. `Pool.List` is the
  one and only data source; the seam does not re-read
  `sessions.json`.
- **Carry `(id, label, state, last_active, bootstrap)` per entry.**
  These five fields are what 61-B's table renderer needs (id, label,
  state, last-active relative format) plus what `--json` consumers
  need (raw last-active timestamp, bootstrap discriminator). The
  bootstrap-label-substitution-on-empty rule has already happened
  inside `Pool.List` (#60); this layer renders verbatim.
- **No typed sentinels to propagate.** `Pool.List` does not return
  errors. The seam's only failure path is the nil-sessioner
  configuration error; the typed-error envelope #98 introduced is
  unused here.

This ticket's scope is **wire surface plus seam plumbing only**. No
CLI, no `cmd/pyry/main.go` change (Pool already satisfies the extended
`Sessioner` interface — `Pool.List` shipped in #60), no prefix
resolution, no flag parsing, no rendering.

## Design

### Wire surface (protocol.go)

Add a `VerbSessionsList` constant. Add two new wire types
(`SessionsListPayload`, `SessionInfo`) and one new field on `Response`
(`SessionsList`). **No new `SessionsPayload` field** — the verb
carries no request arguments.

```go
const (
    // ... existing verbs through VerbSessionsRename ...

    // VerbSessionsList returns a snapshot of every session in the
    // pool. Request carries no payload. Response.SessionsList carries
    // the snapshot. First read-side member of the sessions.<verb>
    // namespace; Pool.List is the only data source the server-side
    // handler calls (see #60 for the underlying primitive's bootstrap
    // label substitution and sort-order guarantees).
    VerbSessionsList Verb = "sessions.list"
)
```

```go
// SessionsListPayload carries the result of a successful sessions.list
// request: a snapshot of every session in the pool, in the order
// returned by Pool.List (LastActiveAt descending, SessionID ascending
// tiebreak). The slice is never nil on a successful response (an empty
// pool would only ever contain the bootstrap entry, but the seam does
// not enforce non-emptiness — it renders what it receives). Final
// user-facing ordering is the responsibility of the CLI renderer
// (61-B); this layer does not re-sort.
type SessionsListPayload struct {
    Sessions []SessionInfo `json:"sessions"`
}

// SessionInfo is one session's operator-visible metadata as carried on
// the wire. Mirrors sessions.SessionInfo (#60) field-for-field with
// wire-appropriate types: SessionID is encoded as a plain string (not
// the sessions.SessionID newtype), LifecycleState as a self-documenting
// string ("active" / "evicted") matching the on-disk registry encoding,
// and LastActiveAt as a time.Time (encoding/json marshals to RFC3339Nano).
//
// Bootstrap carries omitempty so the field elides for non-bootstrap
// entries — the discriminator is only meaningful for the one entry
// where it's true. ID, Label, State, and LastActive are always present
// (they describe every entry).
//
// Defined here, in protocol.go, rather than reusing sessions.SessionInfo
// directly so external Go callers / future hand-written clients of the
// wire don't transitively import internal/sessions for the lifecycleState
// uint8 enum (kept package-private for the same reason — see lessons.md).
type SessionInfo struct {
    ID         string    `json:"id"`
    Label      string    `json:"label"`
    State      string    `json:"state"`        // "active" | "evicted"
    LastActive time.Time `json:"last_active"`  // RFC3339Nano on the wire
    Bootstrap  bool      `json:"bootstrap,omitempty"`
}
```

`Response` gains one omitempty field:

```go
type Response struct {
    Status       *StatusPayload       `json:"status,omitempty"`
    Logs         *LogsPayload         `json:"logs,omitempty"`
    SessionsNew  *SessionsNewResult   `json:"sessionsNew,omitempty"`
    SessionsList *SessionsListPayload `json:"sessionsList,omitempty"` // NEW (1.1b-B1)
    OK           bool                 `json:"ok,omitempty"`
    Error        string               `json:"error,omitempty"`
    ErrorCode    ErrorCode            `json:"errorCode,omitempty"`
}
```

**Byte-equality regression test.** A `Response{}`, a `Response{OK: true}`,
and the `Response{SessionsNew: ...}` shape from #75 must round-trip
to bytes that are identical to their pre-#87 form (the new
`SessionsList` field has `omitempty` and is nil in those cases). Pin
this in `TestProtocol_SessionsRoundTripBackCompat` by extending the
table with a `Response{}` and a `Response{OK: true}` row asserting
they marshal to `{}` and `{"ok":true}` respectively, plus a
`Response{SessionsNew: &SessionsNewResult{SessionID: "..."}}` row
asserting bytes are identical to a hand-rolled string literal. (The
existing table covers `Request` shapes; extend it to cover `Response`
shapes too — this is the load-bearing assertion the AC names.)

#### Why `time.Time` on the wire instead of an RFC3339 string

The closest precedent (`StatusPayload`) uses pre-formatted strings
("310ms", "1.5s") for fields that are already display-formatted
durations — those values are not data downstream consumers do math on.
`LastActive` is different: 61-B's table renderer needs to format
relative time ("3m ago"), which requires a `time.Time` to subtract
from `time.Now()`. Going string here would force every consumer to
re-parse RFC3339Nano back into a `time.Time`. Marshalling
`time.Time` directly via `encoding/json` produces RFC3339Nano on
the wire (jq-debuggable, byte-stable); decoded back into `time.Time`
on the client side without per-consumer parsing.

The protocol.go import set gains `time` (a stdlib import, no transitive
dependency on `internal/sessions`). The "import-free by design"
convention from the wire-enums lesson is specifically about not
dragging in `internal/sessions` (which would leak supervisor-package
internals into the wire contract); `time` is fine.

JSON roundtrip strips the monotonic-clock component of `time.Time`
(per `lessons.md` § "JSON roundtrip strips monotonic-clock state from
`time.Time`"). Tests that compare a pre-encode `time.Time` with the
post-decode value must use `time.Equal`, not `==` or
`reflect.DeepEqual`. Document this on `SessionInfo.LastActive`'s
field comment.

#### Why `state` as a string instead of an int

Self-documenting wire enum, same rule as `JSONLPolicy` (see
`lessons.md` § "Wire enums: prefer self-documenting strings"). The
two values are `"active"` and `"evicted"` — exactly the encoding
`lifecycleState.String()` produces and exactly the encoding the
on-disk registry uses (no parallel translation table needed; reuse
`String()` directly).

`parseLifecycleState` (the inverse) is **not** exported on the wire.
The CLI ticket (61-B) consumes the string verbatim for display; if a
future client needs to compare against a typed enum, it can compare
against the literal string `"active"` / `"evicted"`. The wire
contract is the string set; the enum is internal.

### `Lister` interface (server.go)

Add a `Lister` interface alongside `Renamer`:

```go
// Lister is the per-pool view the control server depends on for the
// sessions.list verb. *sessions.Pool satisfies it structurally via
// Pool.List. Defined here, where it is consumed; tests fake it
// directly.
//
// List returns a snapshot of every session in the pool — bootstrap
// and minted alike — sorted by LastActiveAt descending with SessionID
// ascending tiebreak. The bootstrap entry's empty on-disk label is
// substituted with "bootstrap" by Pool.List itself; this seam renders
// verbatim. Read-only: does not bump LastActiveAt or transition
// state. See Pool.List for the full contract.
//
// List does not take a context — Pool.List's signature is `() []SessionInfo`
// and the operation is bounded by Pool.mu (RLock) + each Session.lcMu
// (briefly). The seam mirrors Pool.List's shape so *sessions.Pool
// satisfies it adapter-free.
type Lister interface {
    List() []sessions.SessionInfo
}
```

Embed `Lister` into `Sessioner`:

```go
type Sessioner interface {
    Create(ctx context.Context, label string) (sessions.SessionID, error)
    Remover
    Renamer
    Lister
}
```

This mirrors #98's `Remover` embed and #90's `Renamer` embed: zero
call-site fan-out at `NewServer`, the only test-side cascade is one
new method on the existing `fakeSessioner` struct.

`*sessions.Pool` already satisfies the embedded shape via
`Pool.List() []sessions.SessionInfo` (#60). No adapter, no
`cmd/pyry/main.go` change.

### Server dispatch (server.go)

`handle`'s switch gains one new case:

```go
switch req.Verb {
// ... existing cases through VerbSessionsRename ...
case VerbSessionsList:
    s.handleSessionsList(enc)
default:
    _ = enc.Encode(Response{Error: fmt.Sprintf("unknown verb: %q", req.Verb)})
}
```

The handler:

```go
// handleSessionsList serves a VerbSessionsList request: snapshot every
// session in the pool through Pool.List and write the result back to
// the client. Mirrors handleSessionsRename's nil-sessioner shape
// (verbatim error message, no context.WithTimeout — Pool.List's
// signature does not take ctx and the operation is in-memory).
//
// Pool.List does not return errors — the only failure path here is
// the nil-sessioner branch. Sort order is whatever Pool.List returns
// (LastActiveAt desc, SessionID asc tiebreak); this layer does not
// re-sort.
//
// LifecycleState is encoded as a string via lifecycleState.String()
// — the same encoding the on-disk registry uses, so the wire and the
// registry agree on token spelling. LastActiveAt is passed through
// as time.Time; encoding/json marshals to RFC3339Nano.
func (s *Server) handleSessionsList(enc *json.Encoder) {
    if s.sessioner == nil {
        _ = enc.Encode(Response{Error: "sessions.list: no sessioner configured"})
        return
    }
    snapshot := s.sessioner.List()
    out := make([]SessionInfo, 0, len(snapshot))
    for _, e := range snapshot {
        out = append(out, SessionInfo{
            ID:         string(e.ID),
            Label:      e.Label,
            State:      e.LifecycleState.String(),
            LastActive: e.LastActiveAt,
            Bootstrap:  e.Bootstrap,
        })
    }
    _ = enc.Encode(Response{SessionsList: &SessionsListPayload{Sessions: out}})
}
```

Notes:

- **No `context.WithTimeout`.** `Pool.List` is in-memory and bounded
  by `Pool.mu` + `Session.lcMu` — handshake's existing 5s deadline
  on the conn covers any pathological slow-write client.
- **No empty-id / payload validation.** The verb carries no payload;
  `req.Sessions` is ignored verbatim. A client that sends one is not
  a protocol violation, just a wasted field on the wire.
- **No typed-sentinel mapping.** `Pool.List` cannot fail; the
  envelope's `ErrorCode` field stays unused on this verb.
- **Translation site is at the seam, not at the renderer.** The
  internal `sessions.SessionInfo` (with the package-private
  `lifecycleState` enum) is converted to the wire-level
  `control.SessionInfo` (with the public `string` state) here, in the
  one handler. The renderer in 61-B receives wire-level types and
  doesn't import `internal/sessions`.

### Client wrapper (client.go)

```go
// SessionsList asks the daemon for a snapshot of every session in the
// pool and returns the result. In-process Go callers (the future
// cmd/pyry sessions list) consume this directly. Same one-shot dial →
// encode → decode → close lifecycle as Status/Logs/Stop/SendResize/
// SessionsNew/SessionsRm/SessionsRename.
//
// Snapshot ordering is whatever the server returned (Pool.List's
// LastActiveAt desc, SessionID asc tiebreak); callers needing a
// different order are responsible for re-sorting.
//
// Empty pool would still contain the bootstrap entry. An empty
// returned slice is treated as a malformed response (the daemon
// always has at least the bootstrap session); SessionsList returns
// "control: empty sessions.list response" in that case for symmetry
// with SessionsNew's empty-result branch.
//
// No typed-sentinel mapping (Pool.List does not return errors). All
// server errors flow through Response.Error verbatim.
func SessionsList(ctx context.Context, socketPath string) ([]SessionInfo, error) {
    resp, err := request(ctx, socketPath, Request{Verb: VerbSessionsList})
    if err != nil {
        return nil, err
    }
    if resp.Error != "" {
        return nil, errors.New(resp.Error)
    }
    if resp.SessionsList == nil {
        return nil, errors.New("control: empty sessions.list response")
    }
    return resp.SessionsList.Sessions, nil
}
```

The `resp.SessionsList == nil` check is the meaningful empty-response
guard; an explicit zero-length slice (`{"sessions":[]}`) decodes to a
non-nil `SessionsList` with `len(Sessions) == 0` — the caller sees the
zero-length slice and decides what to do. The handler never produces
that shape (the bootstrap entry is always present), but the client
treats it as "well-formed but empty" rather than as an error: the
daemon's contract is "snapshot reflects pool state"; if the pool is
genuinely empty the client should observe that, not see an error.

### Server constructor (server.go)

`NewServer`'s signature is **unchanged**. The doc comment at lines
141-164 is updated to mention list alongside the existing
new/rm/rename mentions:

```go
// sessioner is optional. When nil, VerbSessionsNew, VerbSessionsRm,
// VerbSessionsRename, and VerbSessionsList all return error
// responses — same precedent as logs/shutdown. The CLI ticket wires
// *sessions.Pool here.
```

`cmd/pyry/main.go` is **not edited**. `*sessions.Pool` already
satisfies the extended `Sessioner` (Pool.List shipped in #60) — the
runtime wiring is implicit.

### Concurrency

- **`handleSessionsList` runs on the per-conn goroutine.** No new
  goroutines spawned. The handler returns after one Encode call and
  the conn closes via the existing `defer` in `handle`.
- **Lock order: `Pool.mu` (RLock) → `Session.lcMu` (Lock).** Same
  order as `Pool.saveLocked`, `Pool.pickLRUVictim`, and `Pool.List`'s
  own internal traversal. No new lock-order edges introduced — the
  seam delegates to `Pool.List` and does no locking of its own.
- **Snapshot is consistent at acquisition time but not across the
  network round-trip.** A session minted between snapshot and client
  decode will not appear; one removed will still appear. Acceptable
  for a snapshot read — the operator loop is "list → decide → act",
  and any acted verb (rename/rm) will hit a `Pool.mu` write lock that
  serialises against future reads.
- **No interaction with the streaming attach lifecycle.** The
  per-attach `streamingWG` tracks attach goroutines; this handler
  is one-shot and lives on `handleWG` like other one-shot verbs.

### Error handling

| Failure mode | Wire shape | `errors.Is` matchable? |
|---|---|---|
| `sessioner` nil | `Response{Error: "sessions.list: no sessioner configured"}` | No (config error) |
| Pool.List returns empty (impossible: bootstrap always present, but defensible) | `Response{SessionsList: &SessionsListPayload{Sessions: []}}` | N/A (success) |
| Decode failure on request | `Response{Error: "decode request: ..."}` (existing path in `handle`) | No |

`Pool.List` does not return errors; nothing else can fail. The
typed-sentinel envelope #98 introduced is unused here.

### Why `time.Time` and not separate `Sec` + `Nsec` ints

Two-field encoding optimises for languages that lack a native
RFC3339 parser (dart, some embedded JSON consumers). pyry's clients
today are Go (in-process), tomorrow Discord (Go SDK), Channels (Go).
RFC3339Nano is a one-line parse in any of them; the typed-int
optimisation is premature and carries the cost of a custom marshal/
unmarshal pair on every client.

## Testing strategy

New file: `internal/control/sessions_list_test.go` (~250 LOC). Pattern
mirrors `internal/control/sessions_rename_test.go`:

| Test | What it asserts |
|---|---|
| `TestServer_SessionsList_Success` | `SessionsList` end-to-end: client receives the snapshot in the order the fake `Lister.List` returned it, with all five fields populated for each entry, bootstrap discriminator preserved, and `time.Equal` on `LastActive`. |
| `TestServer_SessionsList_PreservesPoolOrder` | Fake `List` returns a deliberately out-of-order slice (e.g. ascending LastActiveAt instead of descending); the wire response preserves the fake's order verbatim — the seam does not re-sort. Pins the AC's "renders what it receives" contract. |
| `TestServer_SessionsList_NilSessioner` | A server constructed with `sessioner = nil` returns `Response{Error: "sessions.list: no sessioner configured"}`. Mirrors the analogous test in `sessions_rename_test.go`. |
| `TestServer_SessionsList_EmptyState` | Fake `List` returns `[]sessions.SessionInfo{}` (zero entries — defensive even though Pool always has bootstrap); wire response is `{"sessionsList":{"sessions":[]}}`, client receives a non-nil empty slice (no error). |
| `TestServer_SessionsList_StateEncoding` | A snapshot containing one `stateActive` and one `stateEvicted` entry produces wire `state` values `"active"` and `"evicted"` respectively — pins the encoding agreement with the on-disk registry. |
| `TestProtocol_SessionInfo_BootstrapOmitempty` | Marshal a `SessionInfo{Bootstrap: false}` and assert the JSON output contains no `"bootstrap"` key (omitempty pinning); marshal one with `Bootstrap: true` and assert it contains `"bootstrap":true`. |
| `TestProtocol_SessionsRoundTripBackCompat` (extended) | Add a row asserting `Response{}` marshals to `{}`, `Response{OK: true}` marshals to `{"ok":true}`, `Response{SessionsNew: &SessionsNewResult{SessionID: "abc"}}` marshals to the byte-identical pre-#87 form. The new `SessionsList` field's omitempty tag is the load-bearing invariant under test. |
| `TestClient_SessionsList_Wire` | Hand-rolled `net.Listen` that captures the JSON line — assert the request bytes are exactly `{"verb":"sessions.list"}\n` (no `Sessions` payload, omitempty for nil; no other fields). |
| `TestClient_SessionsList_EmptyResponse` | Server returns `Response{}` (no `SessionsList`, no `Error`); client returns `errors.New("control: empty sessions.list response")` — the meaningful empty-response guard. |
| `TestClient_SessionsList_ServerError` | Server returns `Response{Error: "boom"}`; client returns `errors.New("boom")` verbatim (no wrap, no prefix). |

`fakeSessioner` (in `sessions_new_test.go`) gains:

```go
listSnapshots [][]sessions.SessionInfo // FIFO of canned responses
listCalls     int                      // increments per List() call
```

…plus a `List` method returning the next canned snapshot (or the last
one if the FIFO is shorter than the call count, for tests that don't
care about repeated reads). No new fake type — same struct, one new
method, one new helper. Same shape as the `Rename` extension #90 added.

**Time comparison discipline.** Tests that round-trip `LastActive`
through JSON must use `time.Equal` (not `==` or `reflect.DeepEqual`)
to compare pre-encode / post-decode values, per the existing lesson
on monotonic-clock stripping. Capture the canonicalised "want" value
the same way `TestE2E_Restart_LastActiveAtSurvives` does (#107):
encode → decode → compare against the round-trip artefact.

**Backward-compat: `sessions.new` / `sessions.rm` / `sessions.rename`
unchanged.** Run those tests untouched; the new `SessionsList`
omitempty field on `Response` must not change their wire bytes by a
single byte. Pinned by the extended `TestProtocol_SessionsRoundTripBackCompat`
table.

## Open questions

None. Sort order, time encoding, state encoding, error envelope
(unused), and seam shape are all settled by precedent (#60, #75,
#90, #98) and the AC. If any test ergonomics require a smaller
`fakeSessioner.List` shape than the FIFO approach above, prefer
shrinking inside the test file rather than reshaping the seam.

## Out of scope

- The CLI verb (`case "list":` in `runSessions`, table renderer,
  `--json` flag, "no pyry running" diagnostic) — that's 61-B.
- `Pool.List` itself — delivered by #60.
- Any "live" / "streaming" snapshot — single response only.
- E2E coverage at the binary boundary — package-level coverage is
  sufficient for a wire-only seam, in line with #75/#90/#98.

## Documentation

Update `docs/knowledge/features/control-plane.md`:

- Add a "Sessions: list seam (1.1b-B1)" subsection mirroring the
  existing rename seam subsection. Document the verb name, the
  no-payload request shape, the `SessionsListPayload` /
  `SessionInfo` wire types, the `Lister` embed, the `time.Time`
  encoding rationale, the seam-not-renderer translation rule for
  the `state` enum, and the no-typed-sentinel-needed note.
- Note that the wire-level `SessionInfo.LastActive` is `time.Time`
  (RFC3339Nano on the wire); jq will render it as a string. CLI
  consumers in 61-B operate on the typed value.

After editing, run `qmd update && qmd embed` (per CLAUDE.md).

Update `docs/PROJECT-MEMORY.md` after the developer ticket lands —
add a "Phase 1.1b-B1 (#87)" entry under control-plane Phase 1.1
work, mirroring the rename / rm / new entries' shape (verb name,
seam, file layout, test diff size). The `Sessioner`-aggregates note
already calls out `Lister` as the next member; verify that line is
still current.
