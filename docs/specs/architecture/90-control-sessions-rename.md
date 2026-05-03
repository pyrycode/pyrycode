# #90 — control-plane verb `sessions.rename` + `Renamer` seam

Phase 1.1c-B1. Wire-only addition. Adds the third `sessions.<verb>`
namespace member to `internal/control` and a consumer-side `Renamer`
interface that `*sessions.Pool` satisfies structurally via its existing
`Pool.Rename` (delivered by #62). No `cmd/pyry` work — the `pyry sessions
rename` CLI router and prefix resolution are sibling ticket 63-B2.

This spec mirrors #98 (`sessions.rm`) closely. The infrastructure #98
introduced — `Response.ErrorCode`, the `ErrCodeSessionNotFound` token,
and the `Sessioner`-aggregates-named-sub-interfaces pattern — is reused
here verbatim. The novel surface this ticket adds is small:

1. One new verb constant (`VerbSessionsRename`).
2. One new `omitempty` field on `SessionsPayload` (`NewLabel`).
3. One new named interface (`Renamer`), embedded into `Sessioner`.
4. One new handler (`handleSessionsRename`) and one new client wrapper
   (`SessionsRename`).

`Pool.Rename`'s only typed sentinel is `ErrSessionNotFound`, which #98
already pinned to a wire token. **No new `ErrorCode` constant is
introduced.** This is the win from #98's wire-error infrastructure
landing first: subsequent verbs reuse the envelope at zero
incremental wire cost.

## Files to read first

- `internal/control/protocol.go` (whole file, 193 lines) — `Verb`
  constants, `SessionsPayload` (which already carries `Label`/`ID`/
  `JSONLPolicy` from prior phases), `ErrorCode` enum + sentinels,
  `Response.ErrorCode` field. The new `VerbSessionsRename` constant and
  the new `NewLabel` field on `SessionsPayload` slot in here. **No new
  enum members.**
- `internal/control/server.go:33-91` — `Session` / `SessionResolver` /
  `Remover` / `Sessioner` declarations. The new `Renamer` interface
  lives next to `Remover`; `Sessioner` gains `Renamer` as a second
  embedded interface (alongside the existing `Remover` embed).
  `NewServer` signature is **unchanged**.
- `internal/control/server.go:329-358` — the `handle` switch where
  `case VerbSessionsRename:` slots in alongside `VerbSessionsRm`.
- `internal/control/server.go:417-461` — `handleSessionsRm` is the
  handler `handleSessionsRename` mirrors. Note the differences called
  out below: no JSONL policy, no `context.WithTimeout` (Pool.Rename's
  signature does not take ctx).
- `internal/control/client.go:123-154` — `SessionsRm` is the model for
  the new `SessionsRename` client wrapper. Same `request()` lifecycle,
  same `Response.ErrorCode` → sentinel mapping (only one sentinel to
  map this time).
- `internal/control/sessions_new_test.go` (whole file, 356 lines) — the
  template for `sessions_rename_test.go`. The shared `fakeSessioner`
  (lines 21-60) gains a `Rename` method + `recordedRenames`; the
  `TestProtocol_SessionsRoundTripBackCompat` table (lines 98-140) gains
  one row asserting that the new `NewLabel` omitempty tag holds. The
  `startServerWithSessioner` harness is reused as-is.
- `internal/sessions/pool.go:31-32` — the `ErrSessionNotFound` sentinel
  definition. `Pool.Rename` returns this **bare** (no wrap), so
  `err.Error()` equals the sentinel's `Error()` — same shape as
  `Pool.Remove`, so the client wrapper returns the bare sentinel
  rather than `fmt.Errorf("%s: %w", resp.Error, sentinel)` (the
  same rationale documented in #98's "Why return the bare sentinel"
  subsection applies verbatim).
- `internal/sessions/pool.go:393-429` — `Pool.Rename(id, newLabel)`
  signature, contract, and concurrency notes. The `Renamer` interface
  mirrors this signature **exactly**: no ctx, two args, `error`
  return.
- `docs/specs/architecture/98-control-sessions-rm.md` (whole file, 827
  lines) — the precedent. Sections "Wire surface (protocol.go)",
  "Remover interface (server.go)", "Server constructor (server.go)",
  "Server dispatch (server.go)", "Client wrapper (client.go)",
  "Concurrency", and "Testing strategy" all have direct analogues
  here. The "Why embed Remover in Sessioner instead of adding a new
  constructor parameter" rationale applies identically and is **not**
  re-litigated below — the embedding pattern is now the established
  shape for `sessions.<verb>` seams.
- `docs/specs/architecture/75-control-sessions-new.md` § "Naming
  rationale" — the verb-family-payload pattern (one typed payload
  struct per namespace, omitempty fields per verb). `NewLabel` is the
  next field to land on the same struct, continuing the pattern.
- `docs/knowledge/features/control-plane.md:615` — the start of the
  "Sessions: removal seam (1.1d-B1)" subsection. A "Sessions: rename
  seam (1.1c-B1)" companion goes immediately after it (or before it
  if Phase order is preferred — same-day decision; see Documentation
  below).

## Context

The control plane currently exposes `status`, `stop`, `logs`, `attach`,
`resize`, `sessions.new`, `sessions.rm`. This ticket adds
`sessions.rename`. One operational shape the wire surface must support:

- **Carry `(id, newLabel)` on the request.** The CLI needs to ask the
  daemon to update a specific session's human-friendly label. Empty
  `newLabel` is a valid request meaning "clear the label" (per #62);
  the wire and seam both forward the empty string unchanged.
- **Propagate one typed error sentinel.** `Pool.Rename` returns
  `ErrSessionNotFound` for unknown IDs. `Pool.Rename`'s no-op shape
  (newLabel equals current label) returns `nil` and is observable on
  the wire as `OK: true` — no special signalling.

This ticket's scope is **wire surface plus seam plumbing only**. No
CLI, no `cmd/pyry/main.go` change (Pool already satisfies the extended
`Sessioner` interface — `Pool.Rename` shipped in #62), no prefix
resolution, no flag parsing.

## Design

### Wire surface (protocol.go)

Add a `VerbSessionsRename` constant. Extend `SessionsPayload` with a
`NewLabel` field (`omitempty`). **Reuse `Response.ErrorCode` and
`ErrCodeSessionNotFound` from #98 verbatim — no new constants.**

```go
const (
    // ... existing verbs through VerbSessionsRm ...

    // VerbSessionsRename updates an existing session's human-friendly
    // label. Request.Sessions carries the session ID and the new label
    // (empty newLabel clears the on-disk label, per Pool.Rename's
    // contract); Response.OK acknowledges success. The typed
    // ErrSessionNotFound from the pool propagates through
    // Response.ErrorCode == ErrCodeSessionNotFound so the CLI can match
    // it with errors.Is. No new ErrorCode constants are introduced —
    // ErrCodeSessionNotFound (1.1d-B1) is reused.
    VerbSessionsRename Verb = "sessions.rename"
)

// SessionsPayload gains one omitempty field. Existing verbs
// (sessions.new with Label-only; sessions.rm with ID + JSONLPolicy)
// serialise byte-identically — pinned by an extended row in
// TestProtocol_SessionsRoundTripBackCompat.
type SessionsPayload struct {
    Label       string      `json:"label,omitempty"`       // sessions.new
    ID          string      `json:"id,omitempty"`          // sessions.rm, sessions.rename
    JSONLPolicy JSONLPolicy `json:"jsonlPolicy,omitempty"` // sessions.rm
    NewLabel    string      `json:"newLabel,omitempty"`    // sessions.rename
}
```

**Why `NewLabel` and not reuse `Label`.** `Label` is documented as the
human-friendly name supplied at create time; reusing it for rename
would conflate the verb-distinguishing field of `sessions.new`
(present? → create with this label) with `sessions.rename`'s
"replacement value" semantic. Two reasons keeping them distinct
matters:

1. **Empty-string semantics differ.** On `sessions.new`, an empty
   `Label` means "no label, store the empty string and let
   Pool.List substitute the synthetic 'bootstrap' display name."
   On `sessions.rename`, an empty `NewLabel` means "clear the
   current on-disk label" — a deliberate write of empty-string
   over a previously non-empty value. Both serialise to "field
   omitted via omitempty," so server-side disambiguation must come
   from the verb, not the field name. Separate field names make
   the per-verb intent explicit at every reading site (handler,
   tests, fixtures, jq-piped wire dumps).
2. **The verb-family-payload pattern doesn't require deduplication.**
   Per #75 § "Naming rationale" and #98 § "Why extend
   `SessionsPayload`," the conscious trade-off of this pattern is
   that fields conceptually unused by a given verb are omitted via
   `omitempty` rather than excluded from the in-memory struct.
   Adding `NewLabel` continues that pattern at zero wire-bytes
   cost. Phase 1.1e (`sessions.attach`) will add `SessionID` (or
   reuse `ID`) on the same shape.

The `omitempty` cost on `NewLabel` is one small inelegance: a
caller who genuinely wants to **clear** a label by sending the
empty string cannot signal that distinctly from "field absent." The
ticket body explicitly accepts this — empty `newLabel` "clears the
label" in both interpretations of the wire and the call. The
server-side handler **forwards `payload.NewLabel` unchanged** to
`Pool.Rename`; if the field is absent the decoded zero value is `""`,
which is already the documented "clear" semantic. No special-casing
needed at any layer.

### Renamer interface (server.go)

Consumer-side, single method, mirrors `Pool.Rename` verbatim. Defined
next to the existing `Remover` declaration. **Embedded into
`Sessioner`** as a second sub-interface (alongside the existing
`Remover` embed) so `NewServer`'s signature stays unchanged.

```go
// Renamer is the per-pool view the control server depends on for
// session rename. *sessions.Pool satisfies it structurally via
// Pool.Rename. Defined here, where it is consumed; tests fake it
// directly.
//
// Rename updates the named session's label and persists the change to
// the registry. Empty newLabel is permitted and clears the on-disk
// label to "". Returns sessions.ErrSessionNotFound when id is not
// present in the pool. See Pool.Rename for the full contract,
// including the no-op shape (newLabel == current label) returning
// nil without persisting.
//
// Rename does not take a context — Pool.Rename's signature is
// (id, newLabel) error and the operation is bounded by a single
// Pool.mu critical section + saveLocked. Mirrors the Pool.Rename
// shape to keep the seam adapter-free; *sessions.Pool satisfies
// this directly.
type Renamer interface {
    Rename(id sessions.SessionID, newLabel string) error
}

// Sessioner aggregates the lifecycle methods the control server
// dispatches to. Phase 1.1a-B1 added Create; Phase 1.1d-B1 added
// Remove via the embedded Remover; Phase 1.1c-B1 adds Rename via
// the embedded Renamer. NewServer's signature stays stable across
// the namespace's growth — see #98's "Why embed Remover in
// Sessioner" rationale (applies identically here; not re-argued).
//
// *sessions.Pool satisfies Sessioner structurally — Pool.Create,
// Pool.Remove, and Pool.Rename match the embedded interfaces'
// signatures exactly, so no covariant-return adapter is needed.
type Sessioner interface {
    Create(ctx context.Context, label string) (sessions.SessionID, error)
    Remover
    Renamer
}
```

**No adapter needed.** `Pool.Rename` returns plain `error` and takes
`(sessions.SessionID, string)`. `Renamer.Rename` declares the same
signature. Concrete and interface match exactly, so `*sessions.Pool`
satisfies `Renamer` (and by transitivity, the extended `Sessioner`)
directly — same structural-satisfaction property as `Pool.Create` /
`Pool.Remove` did when their embeds landed.

**Why no ctx on the seam.** `Pool.Rename`'s signature does not take
`context.Context`. Three options were considered:

1. **Seam takes ctx, server passes `context.Background()`, ctx is
   discarded by the implementation.** Adds a noise parameter to
   every test fake and lies about cancellability. Rejected.
2. **Seam takes ctx, server passes a 30s WithTimeout, ctx is
   discarded by Pool.Rename.** Same problem; the timeout is a
   fiction. Rejected.
3. **Seam mirrors `Pool.Rename` (no ctx).** Adapter-free
   satisfaction, no fiction. Chosen.

If a future Pool.Rename needs cancellation (e.g. registry persist
becomes async), the seam grows ctx at that time and the handler
plumbs it through. Defer until needed.

### Server constructor (server.go)

**Unchanged.** No new parameters. The existing `sessioner Sessioner`
parameter now requires `Rename` (in addition to `Create` and `Remove`)
to satisfy the broader interface; `*sessions.Pool` already does.

The nil-handling diagnostic for `VerbSessionsRename` mirrors the
existing ones for `VerbSessionsNew`/`VerbSessionsRm`: when
`s.sessioner == nil`, `handleSessionsRename` returns
`Response{Error: "sessions.rename: no sessioner configured"}`.

### Server dispatch (server.go)

Add a `case VerbSessionsRename:` branch to `handle`'s switch and a
new `handleSessionsRename` method.

```go
// In handle(), inside the switch:
case VerbSessionsRename:
    s.handleSessionsRename(enc, req.Sessions)

// New method, modelled on handleSessionsRm but simpler — no JSONL
// policy translation, no context.WithTimeout (Pool.Rename does not
// take ctx; the operation is bounded by Pool.mu + saveLocked).
func (s *Server) handleSessionsRename(enc *json.Encoder, payload *SessionsPayload) {
    if s.sessioner == nil {
        _ = enc.Encode(Response{Error: "sessions.rename: no sessioner configured"})
        return
    }
    if payload == nil || payload.ID == "" {
        _ = enc.Encode(Response{Error: "sessions.rename: missing id"})
        return
    }
    // payload.NewLabel forwarded unchanged — empty string is a valid
    // request meaning "clear the label" per Pool.Rename's contract.
    err := s.sessioner.Rename(sessions.SessionID(payload.ID), payload.NewLabel)
    if err != nil {
        resp := Response{Error: err.Error()}
        if errors.Is(err, sessions.ErrSessionNotFound) {
            resp.ErrorCode = ErrCodeSessionNotFound
        }
        _ = enc.Encode(resp)
        return
    }
    _ = enc.Encode(Response{OK: true})
}
```

**Error wire shape.**

| Failure | `Response.Error` | `Response.ErrorCode` |
|---|---|---|
| `sessioner == nil` | `"sessions.rename: no sessioner configured"` | (empty) |
| Empty `ID` | `"sessions.rename: missing id"` | (empty) |
| `Pool.Rename` → `ErrSessionNotFound` | `"sessions: session not found"` (verbatim) | `"session_not_found"` |
| `Pool.Rename` → other (e.g. `saveLocked` failure) | `<err.Error()>` (verbatim) | (empty) |

The `"sessions.rename: "` prefix appears only on server-side
diagnostics (missing sessioner / missing id). Real `Pool.Rename`
errors flow through verbatim — same convention as
`handleSessionsNew` / `handleSessionsRm`.

**Why `errors.Is` on the server side (not just string match).**
Same rationale as #98: `Pool.Rename` returns the sentinel bare
today, but a future change could legitimately wrap it. The
`errors.Is` check survives wrapping; the typed `ErrorCode`
continues to fire on the wire even if the message string changes.

**Empty `ID` handling.** Rejected at the handler boundary with a
`Response.Error`. The alternative — passing `""` through to
`Pool.Rename` and letting it return `ErrSessionNotFound` — would
also work but produces the wrong `ErrorCode` (an empty ID is a
missing-input condition, not a not-found one). Mirrors the same
guard in `handleSessionsRm` for consistency.

**Empty `NewLabel` is NOT rejected.** Empty `NewLabel` is a valid
request (clear the label, per #62). The decoded zero value `""`
flows directly to `Pool.Rename`, which treats it as "set label to
empty string, persist, return nil." No guard fires. (Contrast with
the empty-`ID` guard above — different semantic.)

### Client wrapper (client.go)

```go
// SessionsRename asks the daemon to update the named session's
// human-friendly label. Empty newLabel is a valid argument meaning
// "clear the label" — Pool.Rename treats it as such (per #62) and
// the wire forwards it unchanged via SessionsPayload.NewLabel's
// omitempty tag (an empty string elides the field, and the server
// decodes the absent field as "").
//
// Typed errors propagate via Response.ErrorCode — a server response
// carrying ErrCodeSessionNotFound returns sessions.ErrSessionNotFound
// directly so callers can errors.Is against it. Other server errors
// (no sessioner configured, missing id, registry persist failures,
// ...) return as errors.New(resp.Error) verbatim.
func SessionsRename(ctx context.Context, socketPath, id, newLabel string) error {
    resp, err := request(ctx, socketPath, Request{
        Verb:     VerbSessionsRename,
        Sessions: &SessionsPayload{ID: id, NewLabel: newLabel},
    })
    if err != nil {
        return err
    }
    if resp.Error != "" {
        if resp.ErrorCode == ErrCodeSessionNotFound {
            return sessions.ErrSessionNotFound
        }
        return errors.New(resp.Error)
    }
    if !resp.OK {
        return errors.New("control: sessions.rename response missing ok flag")
    }
    return nil
}
```

**Why return the bare sentinel.** Same rationale as #98's `SessionsRm`
client wrapper: `Pool.Rename` returns the sentinel bare,
`resp.Error` already matches the sentinel's message verbatim, and
wrapping with `fmt.Errorf("%s: %w", resp.Error, sentinel)` would
produce `"sessions: session not found: sessions: session not found"`
(double prefix). Return the sentinel directly.

**Imports.** `internal/sessions` is already imported by `client.go`
(added in #98). No new import.

### Data flow

```
 CLI client (sibling 63-B2)                  Server                            Pool (#62)
 ──────────────────────────                  ──────                            ──────────
 SessionsRename(ctx, sock, "<uuid>", "alpha")
   │
   ▼
 dial unix sock
 encode {verb:"sessions.rename",
         sessions:{id:"<uuid>", newLabel:"alpha"}}
   ──────────────────────────────►
                                              decode → switch
                                                case VerbSessionsRename:
                                                  handleSessionsRename(enc, req.Sessions)
                                                    sessioner.Rename(id, "alpha")
                                                                                ──►
                                                                                  Pool.Rename
                                                                                    take p.mu (write),
                                                                                    update sess.label,
                                                                                    saveLocked,
                                                                                    release
                                                                                ◄──
                                                                                nil
                                                  encode {ok: true}
   ◄──────────────────────────────
 decode → return nil

 Error path (unknown id):
                                                    sessioner.Rename(unknownID, "x")
                                                                                ──►
                                                                                  → ErrSessionNotFound
                                                                                ◄──
                                                  errors.Is(err, ErrSessionNotFound) → true
                                                  encode {error:"sessions: session not found",
                                                          errorCode:"session_not_found"}
   ◄──────────────────────────────
 decode → ErrorCode == ErrCodeSessionNotFound →
                       return sessions.ErrSessionNotFound
 caller errors.Is(err, sessions.ErrSessionNotFound) → true

 Empty-newLabel path (clear the label):
 SessionsRename(ctx, sock, "<uuid>", "")
   │
   ▼
 encode {verb:"sessions.rename",
         sessions:{id:"<uuid>"}}     ← NewLabel elided via omitempty
   ──────────────────────────────►
                                              decode → payload.NewLabel == ""
                                                handleSessionsRename forwards "" unchanged
                                                  sessioner.Rename(id, "")
                                                                                ──►
                                                                                  Pool.Rename clears label,
                                                                                  saveLocked
                                                                                ◄──
                                                                                nil
                                                  encode {ok: true}
   ◄──────────────────────────────
```

### Concurrency

No new mutexes, channels, or goroutines.

- `VerbSessionsRename` is a one-shot verb. The existing per-conn
  handler goroutine in `Server.Serve` handles it identically to
  other one-shot verbs. The `streamingWG` pattern is **not** used.
- `Pool.Rename` is documented as safe under concurrent use against
  other pool writers (it takes `Pool.mu` write across the read-
  modify-write + persisted file write — see `pool.go:404-407`
  comment). The control server adds no coordination on top.
- One subtle interleave: a `sessions.rename` request for an in-flight
  `sessions.new`'s minted ID could in principle race the new
  session's bootstrap. `Pool.Create` finishes registering the entry
  under `Pool.mu` before returning — by the time the wire response
  carries the new ID, the registry has it. A subsequent
  `sessions.rename` is FIFO-after — no race observable to the
  client. The control plane doesn't need to add ordering on top.
- Concurrent `sessions.rename` against `sessions.rm` for the same
  ID: both go through `Pool.mu`. Whichever acquires first wins;
  the second observes the post-first state (Rename after Remove
  → `ErrSessionNotFound`; Remove after Rename → succeeds against
  the renamed session). Both outcomes are well-defined and
  surface cleanly to clients.

### Error handling

Beyond the wire-shape table above:

- **Decode-error propagation.** `request()` already wraps
  `json.Decoder.Decode` errors as `"read response: <err>"` — no
  change needed.

- **No 30s ctx timeout in the handler.** Unlike `handleSessionsNew`
  / `handleSessionsRm` which spawn a `context.WithTimeout`,
  `handleSessionsRename` is fully synchronous (Pool.Rename takes no
  ctx; the operation is bounded by a single Pool.mu critical
  section + saveLocked). The handler returns immediately after
  Pool.Rename returns. The conn's existing 5s `handshakeTimeout`
  on the request side is sufficient; a slow registry write that
  blocks Pool.mu for >5s would expose itself as a failed write
  to the conn, which is fine.

  This is a deliberate deviation from the #98 template. The
  handshake deadline on the conn is set in `handle()` at line 318
  (`SetDeadline(time.Now().Add(handshakeTimeout))`) — that
  deadline applies to the whole one-shot lifecycle including the
  encode-response write. If Pool.Rename's saveLocked is observed
  to take more than 5s in practice, the appropriate fix is
  raising `handshakeTimeout` for verbs that need it, not
  introducing an ignored seam-level ctx. Defer until observed.

## Testing strategy

New file: `internal/control/sessions_rename_test.go` (~220 LOC).
Stdlib `testing` only.

`fakeSessioner` (in `sessions_new_test.go`) gains a `Rename` method
**in place** — not a new fake type:

```go
// In sessions_new_test.go, on the existing fakeSessioner struct.

type renameCall struct {
    ID       sessions.SessionID
    NewLabel string
}

type fakeSessioner struct {
    mu          sync.Mutex
    createCalls []string
    removeCalls []removeCall
    renameCalls []renameCall   // NEW
    returnID    sessions.SessionID
    returnErr   error           // shared across Create / Remove / Rename
}

func (f *fakeSessioner) Rename(id sessions.SessionID, newLabel string) error {
    f.mu.Lock()
    f.renameCalls = append(f.renameCalls, renameCall{ID: id, NewLabel: newLabel})
    err := f.returnErr
    f.mu.Unlock()
    return err
}

func (f *fakeSessioner) recordedRenames() []renameCall {
    f.mu.Lock()
    defer f.mu.Unlock()
    return append([]renameCall(nil), f.renameCalls...)
}
```

**Decision on `returnErr` sharing.** Same convention as #98: one
`returnErr` is returned by Create / Remove / Rename; tests that need
to differentiate use a per-test fakeSessioner instance. None of this
ticket's AC items require Create-and-Rename divergence within one
fake.

Mandatory tests, mapping to AC items:

1. **Extend `TestProtocol_SessionsRoundTripBackCompat`** — AC#1
   byte-equality. Add one row to the existing table:
   - `Request{Verb: VerbSessionsRm, Sessions: &SessionsPayload{ID: "x", JSONLPolicy: JSONLPolicyArchive}}`
     marshals to `{"verb":"sessions.rm","sessions":{"id":"x","jsonlPolicy":"archive"}}`
     — the new `NewLabel` field's omitempty tag holds.

   The existing rows for `VerbStatus`, `VerbSessionsNew` with
   label-only, `Response{}`, and `Response{OK:true}` continue to
   pass unchanged — adding `NewLabel` to `SessionsPayload` does not
   perturb them (omitempty drops the empty string).

2. **`TestServer_SessionsRename_Success`** — AC#3 success path.
   `fakeSessioner` returns nil. Client calls `SessionsRename(ctx,
   sock, "<uuid>", "new-label")`; asserts:
   - Returned error is nil.
   - The fake recorded one Rename call with the canned ID and
     `newLabel == "new-label"`.
   - Server response decodes with `OK: true`, no `Error`,
     no `ErrorCode`.

3. **`TestServer_SessionsRename_EmptyNewLabel`** — AC#3 empty-label
   forwarding. Client passes `newLabel == ""`. Asserts:
   - Returned error is nil.
   - The fake recorded one Rename call with `newLabel == ""`
     (verifies the empty string is forwarded unchanged through the
     wire's omitempty + handler's no-guard path).
   - Server response decodes with `OK: true`.

4. **`TestServer_SessionsRename_ErrSessionNotFound`** — AC#3 typed-
   error propagation. `fakeSessioner.returnErr =
   sessions.ErrSessionNotFound`. Client call must:
   - return a non-nil error
   - `errors.Is(err, sessions.ErrSessionNotFound)` → true
   - `err.Error()` contains `"sessions: session not found"`

5. **`TestServer_SessionsRename_OtherPoolError`** — error path with
   a non-typed error (e.g. `errors.New("sessions: persist registry:
   ..."))`. Client must:
   - return a non-nil error whose `Error()` contains the verbatim
     inner message
   - `errors.Is(err, sessions.ErrSessionNotFound)` → **false**

6. **`TestServer_SessionsRename_NoSessionerConfigured`** — nil-
   Sessioner branch. Server constructed with `sessioner: nil`.
   Client call returns error `"sessions.rename: no sessioner
   configured"`.

7. **`TestServer_SessionsRename_MissingID`** — empty-ID guard.
   Client passes `id == ""`. Returns `"sessions.rename: missing
   id"`. Fake sessioner must record **zero** Rename calls (the
   guard fires before the seam call).

8. **`TestSessionsRename_PassesArgsOnWire`** — wire shape. Hand-
   rolled `net.Listen` server (mirrors
   `TestSessionsNew_PassesLabelOnWire`) captures the raw client-
   encoded line. Table:
   - `(id="11111111-...", newLabel="alpha")` → contains
     `"verb":"sessions.rename"` and
     `"sessions":{"id":"11111111-...","newLabel":"alpha"}`
   - `(id="22222222-...", newLabel="")` → contains
     `"sessions":{"id":"22222222-..."}` (empty newLabel drops via
     omitempty)

9. **`TestSessionsRename_DecodesEmptyResponseAsError`** — defensive
   client-shape check. Hand-rolled server returns `Response{}` (no
   Error, no OK). Client returns `"control: sessions.rename
   response missing ok flag"`.

`go test -race ./internal/control/...` must pass (AC#5-row-1).
`go vet ./...` must be clean (AC#5-row-2). No new staticcheck
violations.

### What's out of scope for tests

- **No integration test against `*sessions.Pool`.** The CLI ticket
  (63-B2) exercises end-to-end; this ticket's contract is
  wire+seam, and the fake Sessioner exercises the contract exactly.
- **No `var _ Sessioner = (*sessions.Pool)(nil)` in the test
  package.** Same reason as #75/#98 — would create an import
  cycle. `cmd/pyry/main.go` is the natural site for that compile-
  time check; the broader `Sessioner` interface satisfaction is
  already validated there by `pool` being passed to `NewServer`.
- **No prefix-resolution test.** Out of scope per the ticket
  body. `payload.ID` flows verbatim to `Pool.Rename`. The CLI
  sibling will either resolve client-side (preferred) or extend
  `handleSessionsRename` to call `ResolveID` first; either is a
  clean follow-on.
- **No `Pool.Rename` no-op behaviour test (newLabel ==
  current label → nil, no save).** That's a `Pool.Rename`
  contract assertion (lives in `internal/sessions` tests from
  #62), not a control-plane contract. The control-plane sees
  the same `nil` return either way and surfaces it as
  `OK: true`.

## Documentation

Update `docs/knowledge/features/control-plane.md`. Add a subsection
**immediately after** "Sessions: removal seam (1.1d-B1)" (around line
615+), titled **"Sessions: rename seam (1.1c-B1)"**:

```markdown
## Sessions: rename seam (1.1c-B1)

The third `sessions.*` verb is `sessions.rename`. The control server
consumes session-rename commands through the embedded `Renamer`
interface in `internal/control`:

    type Renamer interface {
        Rename(id sessions.SessionID, newLabel string) error
    }

    type Sessioner interface {
        Create(ctx context.Context, label string) (sessions.SessionID, error)
        Remover
        Renamer
    }

`*sessions.Pool` satisfies `Renamer` directly via `Pool.Rename`
(shipped in #62). `Renamer` does not take a context — Pool.Rename's
signature is `(id, newLabel) error` and the operation is bounded by a
single Pool.mu critical section + saveLocked, so the seam mirrors that
shape adapter-free. Adding `Renamer` to `Sessioner` keeps NewServer's
signature stable; the rationale documented under "Sessions: removal
seam" applies identically.

### Wire shape

`SessionsPayload` gains one omitempty field used by `sessions.rename`:

    type SessionsPayload struct {
        Label       string      `json:"label,omitempty"`       // sessions.new
        ID          string      `json:"id,omitempty"`          // sessions.rm, sessions.rename
        JSONLPolicy JSONLPolicy `json:"jsonlPolicy,omitempty"` // sessions.rm
        NewLabel    string      `json:"newLabel,omitempty"`    // sessions.rename
    }

`sessions.rename` uses the `OK`/`Error` envelope — no typed result.
Empty `NewLabel` on the wire is forwarded to `Pool.Rename` as the
empty string, which clears the on-disk label per #62's contract.

### Typed errors via Response.ErrorCode

One sentinel propagates from `Pool.Rename`:

    Response.ErrorCode == "session_not_found"  → sessions.ErrSessionNotFound

The client maps this to the corresponding sentinel error so callers
can match with `errors.Is`. Untyped errors (e.g. registry persist
failures) flow through `Response.Error` verbatim with no `ErrorCode`.

The `ErrorCode` envelope and the `ErrCodeSessionNotFound` token are
reused verbatim from #98 — no new wire constants. This is the
intended dividend of #98's wire-error infrastructure landing first:
subsequent verbs reuse the envelope at zero incremental wire cost.
```

The placement (after `## Sessions: removal seam (1.1d-B1)`) is
phase-out-of-order (1.1c lands after 1.1d in the file) but matches
**ticket-merge order**, which is the more useful chronology for
readers tracing the namespace's growth in git log. If a future
editor prefers strict phase order, swap the two subsections; the
content is independent.

## Open questions

1. **Should the server resolve a prefix before calling
   `Pool.Rename`?** Today, no — `payload.ID` flows verbatim. The CLI
   sibling decides where prefix resolution lives. Either lands
   cleanly on top of this ticket's surface; this spec leaves the
   door open in both directions. (Same answer as #98.)

2. **Should `Sessioner`'s `Create` be promoted to a named `Creator`
   interface for symmetry with `Remover` / `Renamer`?** Today, no —
   `Create` lives directly on `Sessioner`. Promotion is an
   additive, no-cost refactor that can happen any time the third
   per-verb consumer wants to mock just `Create` without the
   others. No anticipatory factoring. (Same answer as #98.)

3. **Should `handleSessionsRename` adopt a 30s ctx timeout for
   parity with `handleSessionsNew` / `handleSessionsRm`?** Today,
   no — see "Error handling" above. The handshake-deadline
   property of the conn covers the realistic blocking budget;
   adding an ignored seam ctx would lie about cancellability. If
   Pool.Rename grows ctx in a future change, the seam grows ctx
   then.

4. **Do we want a server-side log line on each successful
   sessions.rename?** Pool.Rename does not currently log (nor does
   Pool.Remove or Pool.Create at the success boundary, as of
   2026-05-03); an additional one in the control handler would
   start a new convention without precedent. Defer until operator
   feedback says otherwise — same answer as #75/#98.
