# #459 — `internal/control`: `rekey` verb wire + `Rekeyer` interface

Slice A of #451's split. This slice lays the control-socket wire contract (verb const + payload + dispatcher) and the `Rekeyer` seam that slice B (#460) plugs into. **No operator-facing subcommand, no consumer, no `cmd/pyry` change** — the `control.Rekey` client helper is callable but unused in production until slice B lands.

## Files to read first

Production code:

- `internal/control/protocol.go:13-80` — existing `Verb*` constants in the `sessions.*` family; mirror this shape for `VerbRekey`.
- `internal/control/protocol.go:97-115` — `ErrorCode` + `ErrCodeSessionNotFound` taxonomy; the new `ErrCodeConnNotFound` is the analogue from AC1.
- `internal/control/protocol.go:117-189` — `Request` envelope + `SessionsPayload` / `ResizePayload` (single-purpose, omitempty); template for the new `RekeyPayload` and the new `Rekey *RekeyPayload` field on `Request`.
- `internal/control/server.go:33-149` — interface-at-consumer pattern (`Session`, `SessionResolver`, `Remover`, `Renamer`, `Lister`, `Sessioner`); the new `Rekeyer` is its own free-standing interface (NOT embedded into `Sessioner` — see § Design rationale).
- `internal/control/server.go:151-221` — `Server` struct + `NewServer` constructor; **`NewServer`'s signature is frozen** (AC2). Add the `rekeyer` field guarded by the existing `s.mu`.
- `internal/control/server.go:372-425` — `handle` dispatcher; add one `case VerbRekey:` branch in the same shape as the surrounding cases.
- `internal/control/server.go:499-528` — `handleSessionsRm`: the canonical pattern for typed-sentinel→`ErrorCode` mapping via `errors.Is`. Copy this shape verbatim.
- `internal/control/server.go:547-565` — `handleSessionsRename`: the shorter no-ctx-timeout variant of the same pattern (no `context.WithTimeout`, no `JSONLPolicy` decoding). `handleRekey` follows this variant — see § "Why no server-side timeout" below.
- `internal/control/client.go:122-185` — `SessionsRm` / `SessionsRename` client helpers; copy the typed-error reconstruction shape (`switch resp.ErrorCode { case … }` → sentinel) for `Rekey`.
- `internal/control/client.go:247-270` — `request` helper; nothing to add, just confirm `Rekey` flows through it like every other client verb.

Tests / fixtures:

- `internal/control/sessions_new_test.go:18-143` — `fakeSessioner` shape (mu-guarded recorded calls, `returnErr` field, copy-on-read accessors). The new `fakeRekeyer` follows the same shape but stands alone (not embedded into `fakeSessioner` — see § Design rationale).
- `internal/control/sessions_new_test.go:145-174` — `startServerWithSessioner`: pattern for the new `startServerWithRekeyer` helper.
- `internal/control/sessions_rm_test.go:20-216` — full handler-test suite (success / typed sentinel / untyped error / no-sessioner / missing id / bad arg); mirror for the rekey suite (success / `ErrConnNotFound` mapped to `ErrCodeConnNotFound` / no-rekeyer / missing-`connID` / ctx-timeout).
- `internal/control/sessions_rm_test.go:218-344` — `TestSessionsRm_PassesArgsOnWire` + `TestSessionsRm_DecodesEmptyResponseAsError`; mirror for `Rekey`.

Convention references:

- `docs/PROJECT-MEMORY.md:20` — "Refusal-to-wire-code mapping is the consumer's job, NOT the primitive's." The `Rekeyer` returns a Go sentinel; the dispatcher maps to the wire `ErrorCode`. This shape is exactly what `handleSessionsRm` does for `sessions.ErrSessionNotFound` → `ErrCodeSessionNotFound`.
- `docs/protocol-mobile.md` § Re-key (line 234) — names `payload.reason = "manual"` as *"operator-triggered via `pyry rekey <conn_id>`"*. Out of scope for this slice (slice B emits `rekey_request` envelopes; slice A only triggers); cited for context.

## Context

Slice A of the split of #451. The original ticket sketched the verb + plumbing + V2 manager wiring in one piece; the architect's edit-fan-out review found that the end-to-end design touched 6 production files and would have required changing `control.NewServer`'s signature across all of its call sites (1 production + 10 test files). Splitting the wire-contract from the verb-and-manager work keeps both slices well under the file-count and refactor-fan-out red lines and lets each slice ship and test independently.

This slice ships:

1. The `rekey` verb wire (constant, payload, error code).
2. A `Rekeyer` interface installed via `Server.SetRekeyer(r)` — **the installation path that satisfies AC2's no-`NewServer`-signature-change constraint**.
3. The server-side dispatcher that calls the installed `Rekeyer` and maps its typed result to the wire envelope.
4. A `control.Rekey(ctx, socketPath, connID)` client helper.
5. Coverage against a stub `Rekeyer`.

No production caller is wired in this slice — the operator subcommand and the `V2SessionManager.TriggerRekey` implementation that plugs into `Rekeyer` are slice B (#460). Until #460 lands, every production-path `VerbRekey` request returns `rekey: no rekeyer configured`. This is the documented production state.

## Design

### Wire protocol (`internal/control/protocol.go`)

Three additions, mirroring the established `sessions.rm` shape:

1. **Verb constant** — `VerbRekey Verb = "rekey"`. Single-word, no namespace — there is only one rekey operation. Sentinel doc-comment references slice B (#460) as the eventual operator-side consumer and notes that slice A ships no caller.
2. **Payload struct** — `RekeyPayload struct { ConnID string \`json:"connID"\` }`. **Field tag is camelCase `connID`** to match the existing `SessionsPayload.ID` / `AttachPayload.SessionID` / `ResizePayload.SessionID` convention on the control socket (control-socket wire is camelCase; `RoutingEnvelope.ConnID`'s snake-case `conn_id` is the mobile-WS wire and is unrelated). No `omitempty` — empty `ConnID` is invalid input, and an absent field decodes to `""` which the handler explicitly rejects.
3. **Error code** — `ErrCodeConnNotFound ErrorCode = "conn_not_found"`. The analogue of `ErrCodeSessionNotFound` named in AC1; snake_case to match the existing wire-token convention.

`Request` grows one field: `Rekey *RekeyPayload \`json:"rekey,omitempty"\``. The omitempty + pointer keeps every other verb's wire bytes byte-identical (same back-compat property `SessionsPayload`'s addition preserves).

`Response` is unchanged — `OK` carries the success ack; `Error` + `ErrorCode` carry the refusal. No new response payload type is needed because the rekey trigger is fire-and-acknowledge with no return data.

### Server-side seam (`internal/control/server.go`)

#### `Rekeyer` interface

```go
// Rekeyer is the per-conn rekey-trigger view the control server depends on
// for VerbRekey. Slice B's *relay.V2SessionManager satisfies it via
// TriggerRekey.
//
// Rekey triggers an immediate Noise re-key on the named conn (the
// operator-driven "manual" rekey path in docs/protocol-mobile.md § Re-key).
// Returns ErrConnNotFound when no conn with the given id is currently open
// on the v2 session manager — the dispatcher maps this to
// ErrCodeConnNotFound on the wire. Any other non-nil error is surfaced
// verbatim through Response.Error with no ErrorCode.
//
// Plumbing channel: the operator-facing verb routes through the control
// socket rather than a direct in-process call because the `pyry rekey`
// subcommand runs in a separate process; an in-process channel is not a
// workable alternative when the trigger originates outside the daemon
// process. The trade-off is one socket round-trip on a verb the operator
// invokes interactively — immaterial in practice.
type Rekeyer interface {
    Rekey(ctx context.Context, connID string) error
}
```

Free-standing — **not embedded into `Sessioner`** — because slice B's eventual implementer (`*relay.V2SessionManager`) is a different type from `*sessions.Pool`. Embedding it into `Sessioner` would either force `Pool` to grow a stub method or force a covariant adapter; both are noise. Decoupled interfaces match the "defined at the consumer, satisfied at the producer" pattern this package already uses for `Remover` / `Renamer` / `Lister` (each is independently addressable; `Sessioner` aggregates the ones `Pool` happens to satisfy).

#### `ErrConnNotFound` sentinel

```go
// ErrConnNotFound is returned by Rekeyer.Rekey when the named conn is not
// open on the v2 session manager. The dispatcher maps this to
// ErrCodeConnNotFound on the wire; the client helper reconstructs this
// sentinel from the wire token so callers can errors.Is against it.
var ErrConnNotFound = errors.New("rekey: conn not found")
```

Defined in `internal/control` because the `Rekeyer` contract is defined here. Per the PROJECT-MEMORY convention ("Refusal-to-wire-code mapping is the consumer's job"), the sentinel is owned where the interface lives; slice B's `V2SessionManager.TriggerRekey` wraps its internal not-found condition with `fmt.Errorf("...: %w", control.ErrConnNotFound)` (or returns it directly). The dispatcher uses `errors.Is(err, ErrConnNotFound)` — survives future wrapping. Mirrors how `internal/control` consumes `sessions.ErrSessionNotFound`; the import-direction is flipped because slice B's package owns the producer.

#### `Server.rekeyer` field + `SetRekeyer`

`Server` gains one new field:

```go
type Server struct {
    // ... existing fields ...
    // rekeyer is set via SetRekeyer between NewServer and Serve. Read
    // under s.mu by handleRekey. Zero-value (nil) is the production state
    // until slice B (#460) lands a V2SessionManager — handleRekey replies
    // "rekey: no rekeyer configured".
    rekeyer Rekeyer
}
```

And one new method:

```go
// SetRekeyer installs the Rekeyer used to service VerbRekey requests.
// Safe to call from any goroutine; canonically called once between
// NewServer and Serve as part of daemon startup. Passing nil clears the
// previously-installed Rekeyer (used by tests; production startup
// installs once and never clears).
func (s *Server) SetRekeyer(r Rekeyer) {
    s.mu.Lock()
    s.rekeyer = r
    s.mu.Unlock()
}
```

**Concurrency rationale.** `s.mu` already guards `listener`/`closed`/`closedCh`; adding `rekeyer` to its scope is the smallest delta. `handleRekey` takes the lock for a one-pointer-load, releases, then calls — no nested lock with the underlying Rekeyer's work. The reviewer-noted "goroutine-safe-by-construction" alternative (rely on the Listen→SetRekeyer→Serve happens-before chain) is genuine but more fragile than a 5-line mutex pair; locking is the simpler shape and matches the rest of `Server`'s state-management.

**Why `NewServer`'s signature stays frozen (AC2).** Threading `rekeyer` through `NewServer` would cascade across 1 production call site (`cmd/pyry/daemon.go` or equivalent — slice B will identify) plus ≥10 test call sites (every `NewServer(...nil...)` in `_test.go`). That cascade is the reason #451 was split. `SetRekeyer` keeps the wire contract independent of the constructor.

#### `handleRekey` dispatcher

Pattern lifted from `handleSessionsRename` (the short no-ctx-timeout variant): read the rekeyer under `s.mu`, validate the payload, call, map typed sentinel to `ErrorCode`, ack.

Behaviour table (one row per branch):

| Precondition                | Server reply                                                                          |
| --------------------------- | ------------------------------------------------------------------------------------- |
| `s.rekeyer == nil`          | `Response{Error: "rekey: no rekeyer configured"}` (no `ErrorCode`)                    |
| `payload == nil \|\| payload.ConnID == ""` | `Response{Error: "rekey: missing connID"}` (no `ErrorCode`) — guard fires BEFORE the rekeyer call so a misconfigured client doesn't trigger work |
| `r.Rekey(...)` returns `ErrConnNotFound` (via `errors.Is`) | `Response{Error: err.Error(), ErrorCode: ErrCodeConnNotFound}` |
| `r.Rekey(...)` returns any other error | `Response{Error: err.Error()}` (no `ErrorCode`)                              |
| `r.Rekey(...)` returns `nil` | `Response{OK: true}`                                                                 |

The dispatcher `case VerbRekey:` lives next to `case VerbSessionsHasID:` in `handle`, calling `s.handleRekey(enc, req.Rekey)`.

#### Why no server-side `context.WithTimeout`

`handleSessionsNew` / `handleSessionsRm` set a fresh 30s background context because `Pool.Create` / `Pool.Remove` actually do work (spawn claude, kill processes). The rekey-trigger contract is the opposite shape: `V2SessionManager.TriggerRekey` (slice B) enqueues a message on the manager's single-owner goroutine and returns once the manager has accepted it (or returned a typed sentinel). The bounded operation is "deliver the request to the manager loop", not "complete the rekey handshake" — the rekey runs asynchronously on the conn's own state machine. A handler-level 30s timeout would lie about cancellability the way a `Rename`-shaped no-ctx-timeout pattern does NOT — `handleRekey` follows `handleSessionsRename`'s lead and trusts the conn's handshake deadline to bound slow-write pathologies, plus slice B's Rekeyer implementation to bound its own enqueue.

The signature still accepts `ctx context.Context` so slice B can propagate cancellation into its enqueue/dispatch logic if it wants to.

### Client-side helper (`internal/control/client.go`)

Pattern lifted from `SessionsRm`:

```go
// Rekey asks the daemon to trigger an immediate Noise re-key on the named
// conn. Returns nil on a successful enqueue (the underlying handshake runs
// asynchronously on the conn's state machine — the helper does not wait
// for it).
//
// ErrConnNotFound is reconstructed from Response.ErrorCode so callers can
// errors.Is against it. Other server errors (no rekeyer configured,
// missing connID, manager-internal failures) return as
// errors.New(resp.Error) verbatim.
//
// No production caller exists until slice B (#460) lands. Wired now so
// slice B is a one-file change in cmd/pyry/.
func Rekey(ctx context.Context, socketPath, connID string) error {
    // → request(VerbRekey, &RekeyPayload{ConnID: connID})
    // → if resp.Error != "" { ErrorCode → ErrConnNotFound, else verbatim }
    // → if !resp.OK { "control: rekey response missing ok flag" }
}
```

Same single-arg shape as `SessionsRm` minus the policy enum. Lives next to `SessionsHasID` in the file.

## Concurrency model

No goroutines added. The existing per-conn `handle` goroutine model in `Serve` covers every `VerbRekey` request. The `s.rekeyer` field is read under `s.mu` exactly once per `handleRekey` invocation; the lock is released BEFORE the (potentially blocking) call into the installed Rekeyer to avoid a long critical section.

Slice B's `V2SessionManager.TriggerRekey` is responsible for its own concurrency story — the `Rekeyer.Rekey` contract is "blocks until the manager has accepted or rejected the request", which from the control server's perspective is just another synchronous call.

## Error handling

| Error shape returned by `Rekeyer.Rekey` | Wire `ErrorCode`     | Client returns          |
| --------------------------------------- | -------------------- | ----------------------- |
| `nil`                                   | none                 | `nil`                   |
| `ErrConnNotFound` (or wrapped)          | `ErrCodeConnNotFound`| `ErrConnNotFound`       |
| anything else                           | none                 | `errors.New(resp.Error)`|

Server-side guards that fire BEFORE calling `Rekeyer.Rekey`:

- `s.rekeyer == nil` → `"rekey: no rekeyer configured"` (no wire `ErrorCode`).
- `payload == nil || payload.ConnID == ""` → `"rekey: missing connID"` (no wire `ErrorCode`).

Neither of these are typed-sentinel-mapped because they are not user-actionable distinctions for the client — they indicate either a config drift on the daemon side or a malformed client.

## Testing strategy

New file: `internal/control/rekey_test.go`. Same-package, stdlib-only, table-driven where useful. No `*V2SessionManager` involved (slice B's manager is out of scope here); every test drives a stub `fakeRekeyer`.

**Test fixtures:**

- `fakeRekeyer` — mu-guarded; records each `Rekey(ctx, connID)` call; configurable `returnErr`. Standalone struct (NOT embedded into `fakeSessioner`) for symmetry with the standalone `Rekeyer` interface. Field shape mirrors `fakeSessioner.removeCalls`/`returnErr`.
- `startServerWithRekeyer(t, resolver, rekeyer)` — variant of `startServerWithSessioner` that calls `srv.SetRekeyer(rekeyer)` between `NewServer` and `Listen`. Lives in `rekey_test.go`. No change to `startServerWithSessioner`.

**Test cases** (each is one `t.Run` or one top-level `Test*`):

1. **Happy path** — `fakeRekeyer{returnErr: nil}` → `Rekey(ctx, sock, "<some-conn-id>")` returns `nil`; the fake records exactly one call with the canned `connID`.
2. **Unknown `connID` maps to `ErrConnNotFound`** — `fakeRekeyer{returnErr: ErrConnNotFound}` → `Rekey` returns an error that satisfies `errors.Is(err, ErrConnNotFound)`. Also assert the error string contains `"rekey: conn not found"`.
3. **Unknown `connID` via wrapped sentinel** — `fakeRekeyer{returnErr: fmt.Errorf("manager: %w", ErrConnNotFound)}` → `Rekey` returns an error that still satisfies `errors.Is`. Pins that the dispatcher uses `errors.Is`, not `==`. (Equivalent to `TestServer_SessionsRm_ErrSessionNotFound` plus the wrap.)
4. **No-rekeyer-configured reject** — server constructed with `SetRekeyer` never called → `Rekey` returns `errors.New("rekey: no rekeyer configured")` verbatim. `errors.Is(err, ErrConnNotFound)` MUST be false. The fake records zero calls (guard fired before Rekeyer reference).
5. **Missing `connID` payload** — `Rekey(ctx, sock, "")` → returns `errors.New("rekey: missing connID")` verbatim. The fake records zero calls (guard fired before Rekeyer reference).
6. **Untyped error pass-through** — `fakeRekeyer{returnErr: errors.New("manager: simulated")}` → `Rekey` returns an error whose `.Error()` is exactly `"manager: simulated"`. `errors.Is(err, ErrConnNotFound)` MUST be false. (Equivalent to `TestServer_SessionsRm_OtherPoolError`.)
7. **Client ctx-timeout** — fake rekeyer blocks until a `<-time.After(2*time.Second)` fires; client passes a 100ms-deadline ctx. `Rekey` returns within ~100ms with a deadline-exceeded / i/o-timeout-shaped error. (Pins that the client respects ctx via `request()`'s `conn.SetDeadline`.)
8. **Wire-shape pin** — `TestRekey_PassesConnIDOnWire` — dial a hand-rolled `net.Listen("unix", ...)` server, read raw bytes, assert the request line contains `"verb":"rekey"` and `"rekey":{"connID":"<id>"}`. Mirrors `TestSessionsRm_PassesArgsOnWire`.
9. **Empty-response defensive shape** — `TestRekey_DecodesEmptyResponseAsError` — server returns `Response{}` (no Error, no OK) → client returns an error containing `"missing ok flag"`. Mirrors `TestSessionsRm_DecodesEmptyResponseAsError`.

**No `NewServer`-signature cascade.** Existing tests are untouched. `startServerWithSessioner` and `startServer` keep their current shapes; the new `startServerWithRekeyer` is additive.

## Open questions

- **Slice B's sentinel ownership.** Slice B's V2SessionManager can either (a) return `control.ErrConnNotFound` directly (importing this package) or (b) define its own `relay.ErrConnNotFound` and have the wiring layer wrap-translate. (a) is the simpler shape and matches how `internal/control` consumes `sessions.ErrSessionNotFound` (just with the import direction flipped). Slice B's architect picks; slice A is byte-stable either way because the dispatcher uses `errors.Is`. Noted for slice B's review, not gating for this slice.
- **JSON tag spelling.** This spec uses `"connID"` (camelCase) to match the control-socket convention. The mobile-WS `RoutingEnvelope` uses `"conn_id"` (snake_case) but is a separate wire. If slice B finds the snake/camel asymmetry painful for operator-facing CLI output, the JSON tag can be revisited then — for this slice the camelCase match to `sessionID`/`jsonlPolicy` is the right local optimum.

## Out of scope

- `pyry rekey <conn_id>` operator subcommand (slice B — #460).
- `cmd/pyry/main.go` verb dispatch + `ctrl.SetRekeyer(v2mgr)` wire-up (slice B — #460).
- `V2SessionManager.TriggerRekey` implementation, the manual-rekey channel inside the manager `Run` loop, and the `emitRekeyRequest(reason)` refactor (slice B — #460).
- The 1-hour scheduled timer + emit path (shipped in #450).
- The responder-side handshake re-run + `CipherState` swap (separate slice).

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No new external trust boundary. The control socket is `0600`-perms-locked, owner-only (`internal/control/server.go:277-281`); only the local user can issue `VerbRekey`. `RekeyPayload.ConnID` is a string forwarded verbatim to slice B's `Rekeyer` — slice B is responsible for validating the conn-id against its `sessions` map. No path traversal, no privilege boundary crossed.
- **[Tokens, secrets, credentials]** Not in scope. No tokens are minted, stored, transmitted, or logged by this slice. The rekey trigger itself is a control signal; the actual Noise re-handshake (slice B) sealed traffic stays inside slice B's machinery and does not cross this wire.
- **[File operations]** Not applicable. No filesystem operations introduced.
- **[Subprocess / external command execution]** Not applicable. No subprocess spawned.
- **[Cryptographic primitives]** Not applicable in this slice. The crypto lives in slice B's `V2SessionManager.TriggerRekey` (and ultimately in `internal/noise`'s already-audited handshake) — slice A is wire plumbing.
- **[Network & I/O]** Wire payload growth is bounded: `RekeyPayload{ConnID string}` is one short string field, sub-100 bytes in practice. The existing `handshakeTimeout = 5*time.Second` in `Server.handle` (`server.go:359`) caps request-read time; the conn deadline applies to the new verb identically to existing verbs. **No new input size limit beyond what `json.Decoder` enforces on the existing `Request` shape** — every existing verb on this socket shares the same envelope and the same deadline, so the rekey verb does not change the DoS surface. No header validation needed (control socket has no headers). One observation noted in passing: the existing control-socket protocol does not enforce an explicit max-bytes cap (it relies on `json.Decoder`'s implicit single-Decode call + the handshake timeout). That is a pre-existing property of every verb on this socket and is not something this slice introduces, regresses, or is the right place to fix. Filed mentally as "investigate when the socket is ever exposed beyond the owning user" — already noted at `server.go:367-371`.
- **[Error messages, logs, telemetry]** Error strings are clean: `"rekey: no rekeyer configured"`, `"rekey: missing connID"`, `"rekey: conn not found"`. No payload bytes, no full conn state, no internal pointer values emitted. The `ConnID` echoed in `ErrConnNotFound`'s wrap (if slice B chooses to wrap with `%w` and includes the conn-id in the message) is operator-supplied, not server-generated — same trust class as the input. No new `slog` calls introduced in `handleRekey`; the dispatcher's existing decode-error log already covers malformed requests.
- **[Concurrency]** Single new field `s.rekeyer` guarded by the pre-existing `s.mu`. No new lock; no lock-ordering question (the mutex is leaf-only — released before calling into the Rekeyer). Goroutine lifecycle unchanged: every request lives on the existing per-conn `handle` goroutine spawned by `Serve` and exits when the response is written. Shutdown safety unchanged: `Server.Close` already drains in-flight handlers via `handleWG.Wait` (`server.go:317`); the new branch behaves identically.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Re-key threats apply to the v2 wire (initiator emits → responder verifies → cipher-state swap), which is slice B's responsibility. The control-socket trigger (this slice) is operator-authenticated by filesystem perms (`0600`) — same authentication boundary as `pyry stop` and `pyry sessions rm`, which are similarly destructive. An attacker who can `Rekey` can also `Stop`; the rekey verb does not lower the bar.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-05-17
