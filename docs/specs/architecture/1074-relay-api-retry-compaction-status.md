# Spec: surface claude api-retry + compaction status over the v2 relay stream (#1074)

**Ticket:** #1074 · **Size:** S · **Label:** `security-sensitive`

## Files to read first

Read these before writing code. Each line names what to extract.

- `internal/turnevent/event.go:72-103` — the `Stall struct{}` template: type doc, `isTurnEvent()` marker, and the `var _ Event = Stall{}` compile-time assertion. Your two new types mirror this exactly (plus fields).
- `internal/turnbridge/mapper.go:20-38` — `mapEvent`'s switch. The `EventKindStallDetected` arm is your template; add four new arms. The `default: return nil, false` is why the new PTY kinds are dropped today.
- `internal/turnbridge/outbound.go:62-104` — `MapEvent`'s switch. The `turnevent.Stall` arm (lines 93-99) is your template — conversation-id only, `tc.TurnID`/`tc.Seq` ignored.
- `internal/protocol/codes.go:168-187` — the v2 interactive-events const cluster (`TypeTurnState`…`TypeStall`) and its "MUST NOT be added to `inboundAppTypeSet`" doc rationale. Add a peer const block.
- `internal/protocol/interactive.go:79-89` — `StallPayload`, and the file header's **no-`omitempty`** rule (lines 12-14). Your two payload structs follow it.
- `cmd/pyry/interactive_turn_v2.go:218-234` — the `turnevent.Stall` arm in `Handle` (flush delta, emit, NO lifecycle mutation) and `eventKind` (lines 388-407). Both get two new arms.
- `internal/protocol/compat_test.go:143-244` — the `v2OnlyTypes` map + `TestTypeConstants_V1V2Partition` + `TestIsKnownAppType`. These drift-detectors are the **only** consumers of the new `Type*` constants and MUST be updated (see § Testing).
- `internal/protocol/interactive_test.go:188-208` + `internal/protocol/testdata/stall.json` — the per-payload round-trip test + fixture pattern (`readFixture` → decode → assert fields → `roundTripEnvelope`). Two new fixtures + two new tests mirror this.
- `internal/turnbridge/mapper_test.go:66-189` — `TestMapEvent` table + `kindEvent` helper. Note: the api-retry cases need `tuidriver.Event{Kind:…, Retry: tuidriver.ApiRetryAttempt{Current:…, Total:…}}`, so a `kindEvent` variant (or inline literal) that sets `Retry`.
- `cmd/pyry/interactive_turn_v2_test.go:497-620` — the four `Stall*` emitter tests (fans-out-interactive-only, no-lifecycle-mutation, mid-turn, cursor-empty). Your emitter tests are a tightly-scoped subset of these (see § Testing — do not duplicate all four per event).
- tui-driver `pkg/tuidriver/events.go:72-130,209-217` + `apiretry.go:8-17` (read via `go doc` or the module cache) — the four `EventKindPtyApiRetry{Shown,Hidden}` / `EventKindPtyCompacting{Shown,Hidden}` kinds, the `Event.Retry ApiRetryAttempt` field, and `ApiRetryAttempt{Current, Total int}` with its `{0,0}` "count unavailable" sentinel.

## Context

Two claude sub-states now stop at pyry and never reach a remote head: claude's
API-error retry (`✻ API error · Retrying in Ns · attempt N/M`) and its
auto-compaction pass (`Compacting conversation` banner). API errors are common
and currently invisible remotely; compaction makes a remote head look frozen for
tens of seconds. tui-driver **v1.12.0** (already pinned in `go.mod`) delivers both
detectors on the same unified `Session.Events` stream the interactive turn
producer already drains — today `turnbridge/mapper.go`'s `default` arm drops them.

Both are **PTY-derived status signals** — peers of the existing `stall` event, not
turn-lifecycle events. This spec threads two new event kinds through the same
five-layer additive pipeline the `stall` event already occupies. No new
interface, package, constructor, or goroutine; no `producer.go` change (its
`drain` loop maps every event via `mapEvent`, so new `mapEvent` arms are picked up
for free — see `producer.go:157`).

## Design

### Wire shape decision

The AC leave the wire shape to the architect (one frame per state with an
active/cleared field, vs. paired shown/hidden frame types). **Decision: one frame
type per state, carrying an `active` boolean.** The `Shown` edge → `active:true`;
the `Hidden` edge → `active:false`. This:

- keeps the surface at **two** new frame types (not four), so the new-exported-type
  count stays at 4 (`ApiRetry`, `Compacting`, `ApiRetryPayload`, `CompactingPayload`)
  — within the S envelope;
- maps the api-retry counter re-fire naturally: tui-driver re-fires
  `EventKindPtyApiRetryShown` when the parsed count climbs (3/10 → 4/10), and each
  re-fire is just another `active:true` frame with an updated counter — no dedup,
  no per-tick flood (tui-driver only re-fires on an actual count change);
- gives a remote head a single deterministic "clear" signal (`active:false`) per
  state, satisfying AC #3 without a second frame type.

### The five layers

Each layer adds arms alongside the `stall` exemplar. **All edits are additive**;
no existing arm is modified (the `Stall` arm and its comment stay untouched).

**Layer 1 — `internal/turnevent/event.go` (neutral model).** Two new sealed
variants + markers + compile-time assertions:

```go
// ApiRetry is a PTY-derived status peer of Stall carrying claude's live
// API-error retry state. Active is the rising (true) / falling (false) edge;
// Current/Total are the parsed `attempt N/M` counter ({0,0} when unparsed).
// Carries no conversation identity — the bridge injects it (like Stall).
type ApiRetry struct {
	Active  bool
	Current int
	Total   int
}

// Compacting is a PTY-derived status peer of Stall: claude's auto-compaction
// banner. Banner-only (tui-driver streams no progress payload), so Active is
// the only field beyond the bridge-injected conversation id.
type Compacting struct{ Active bool }
```

Add `func (ApiRetry) isTurnEvent() {}`, `func (Compacting) isTurnEvent() {}`, and
the two `var _ Event = …{}` assertions. Extend the `Event` interface doc's variant
list.

**Layer 2 — `internal/turnbridge/mapper.go` (tui-driver → neutral).** Four arms in
`mapEvent`, before `default`:

```go
case tuidriver.EventKindPtyApiRetryShown:
	return turnevent.ApiRetry{Active: true, Current: ev.Retry.Current, Total: ev.Retry.Total}, true
case tuidriver.EventKindPtyApiRetryHidden:
	return turnevent.ApiRetry{Active: false, Current: ev.Retry.Current, Total: ev.Retry.Total}, true
case tuidriver.EventKindPtyCompactingShown:
	return turnevent.Compacting{Active: true}, true
case tuidriver.EventKindPtyCompactingHidden:
	return turnevent.Compacting{Active: false}, true
```

The counter is copied on both edges (tui-driver carries last-known on `Hidden`
"so the final render stays coherent"); the phone ignores it when `active:false`.
This keeps the arm a pure kind→(active, copy-counter) map with no branching.

**Layer 3 — `internal/turnbridge/outbound.go` (neutral → wire).** Two arms in
`MapEvent`, before `default`. Like the `Stall` arm, conversation-id only —
`tc.TurnID`/`tc.Seq` are ignored:

```go
case turnevent.ApiRetry:
	return protocol.TypeApiRetry, protocol.ApiRetryPayload{
		ConversationID: tc.ConversationID, Active: e.Active, Current: e.Current, Total: e.Total,
	}, true
case turnevent.Compacting:
	return protocol.TypeCompacting, protocol.CompactingPayload{
		ConversationID: tc.ConversationID, Active: e.Active,
	}, true
```

**Layer 4a — `internal/protocol/codes.go` (wire vocabulary).** A new const block
adjacent to the interactive-events cluster, with a doc comment matching the
cluster's "v2-only, MUST NOT be added to `inboundAppTypeSet`" pattern and noting
these are PTY-derived status peers of `stall`:

```go
const (
	TypeApiRetry   = "api_retry"  // binary → phone, outbound v2 api-retry status
	TypeCompacting = "compacting" // binary → phone, outbound v2 compaction status
)
```

**Layer 4b — `internal/protocol/interactive.go` (payload structs).** Two structs
following the file's no-`omitempty` rule (every field always on the wire so
boundary values like `active:false` / `current:0` never silently vanish):

```go
type ApiRetryPayload struct {
	ConversationID string `json:"conversation_id"`
	Active         bool   `json:"active"`
	Current        int    `json:"current"`
	Total          int    `json:"total"`
}
type CompactingPayload struct {
	ConversationID string `json:"conversation_id"`
	Active         bool   `json:"active"`
}
```

Doc comments mirror `StallPayload`: conversation-id-scoped, not turn-scoped (no
`turn_id`); `Active` is the show/clear edge; the bridge supplies `ConversationID`.

**Layer 5 — `cmd/pyry/interactive_turn_v2.go` (consumer / fan-out).** Two arms in
`Handle`, mirroring the `Stall` arm — flush any pending delta so buffered text
keeps its wire position, then emit, with **NO** lifecycle mutation
(`startTurnIfNeeded` / `transitionTo` / `endTurn` are not called; `inTurn`,
`turnID`, `currentState` untouched):

```go
case turnevent.ApiRetry:
	e.flushDelta(ctx) // status peer of turn_state — see the Stall arm
	e.emitMapped(ctx, convID, ev)
case turnevent.Compacting:
	e.flushDelta(ctx)
	e.emitMapped(ctx, convID, ev)
```

Add two arms to `eventKind` returning `"api_retry"` / `"compacting"` (content-free
discriminants for log fields only). These ride the existing `emit()` path
verbatim: capability gate (interactive conns only), per-conn monotonic env id,
ring append for reconnect-replay. **Do not** merge with the `Stall` arm — additive
arms keep the tested `Stall` case and its comment undisturbed.

### Data flow (unchanged skeleton, two new event kinds)

```
tui-driver Session.Events ──> producer.drain ──> mapEvent (Layer 2)
   EventKindPtyApiRetry{Shown,Hidden}  ─┐
   EventKindPtyCompacting{Shown,Hidden} ┘
                                         └─> turnevent.{ApiRetry,Compacting} (Layer 1)
                                                └─> emitter.Handle (Layer 5)
                                                      flushDelta → emitMapped → MapEvent (Layer 3)
                                                        └─> protocol.{ApiRetry,Compacting}Payload (Layer 4)
                                                              └─> emit() → interactive conns only → wire
```

## Concurrency model

**No change.** No new goroutines, channels, or locks. Both new event kinds are
delivered on the producer's single `Run` goroutine that already invokes
`Handle` serially (`turnbridge/producer.go`), exactly like `Stall`. The emitter's
lifecycle fields stay unguarded-but-race-free because the status peers touch none
of them. The `emit()` fan-out, `nextID` counter, and `ring.Append` paths are
reused unchanged; the ring's own mutex continues to cover the only cross-goroutine
read.

## Error handling

**Reuses existing paths — no new failure modes.**

- **Empty cursor** — `Handle`'s top guard (`convID == ""` → debug-log + drop)
  covers the new kinds for free, same as `Stall`.
- **No turn mint** — status peers never call `startTurnIfNeeded`, so the
  `crypto/rand` mint-failure path is not on this route.
- **Marshal failure** — `emit()`'s existing defensive `json.Marshal` guard
  applies; the payloads are closed string/bool/int structs that cannot fail to
  marshal in practice.
- **Unparsed counter** — tui-driver hands `Retry = {0,0}` when the `attempt N/M`
  counter didn't parse; the mapper copies it verbatim and the frame ships
  `current:0, total:0` (a legitimate "retrying, count unknown" state). No error.
- **Falling edge with no prior rising edge** — harmless: the emitter is stateless
  w.r.t. these peers, so a lone `active:false` just emits a clear frame the phone
  no-ops on.

## Testing strategy

Bulleted scenarios; the developer writes the code in the project's table-driven
idiom. **Keep the emitter tests tightly scoped — do not duplicate all four
`Stall*` tests per event.**

**Layer 1 — `internal/turnevent/event_test.go`**
- Add `ApiRetry{}` and `Compacting{}` to the sum-type coverage list + the marker
  `switch` (mirrors the `Stall{}` entries at lines 57, 79).

**Layer 2 — `internal/turnbridge/mapper_test.go`** (extend `TestMapEvent` table)
- `EventKindPtyApiRetryShown` with `Retry:{Current:3,Total:10}` → `ApiRetry{Active:true,Current:3,Total:10}`.
- `EventKindPtyApiRetryHidden` with `Retry:{Current:4,Total:10}` → `ApiRetry{Active:false,Current:4,Total:10}` (last-known counter forwarded).
- `EventKindPtyApiRetryShown` with zero `Retry` → `ApiRetry{Active:true,Current:0,Total:0}` (unparsed-counter passthrough).
- `EventKindPtyCompactingShown` → `Compacting{Active:true}`; `…Hidden` → `Compacting{Active:false}`.
- The existing "drop pty …" rows already prove the `default` arm; no removal needed.

**Layer 3 — `internal/turnbridge/outbound_test.go`** (extend the `MapEvent` table)
- `ApiRetry{Active:true,Current:3,Total:10}` + a `tc` with non-empty `TurnID`/`Seq`
  → `TypeApiRetry` + `ApiRetryPayload{ConversationID:…, Active:true, Current:3, Total:10}`; assert **no `turn_id` leaks** (the payload has no such field — mirror the `Stall` arm's `turn_id`-can't-leak assertion at lines 143-150).
- `Compacting{Active:false}` → `TypeCompacting` + `CompactingPayload{ConversationID:…, Active:false}`.

**Layer 4 — `internal/protocol/interactive_test.go` + `testdata/`**
- New fixtures `testdata/api_retry.json` and `testdata/compacting.json` shaped like
  `stall.json` (`{"id":…,"type":"api_retry","ts":"…","payload":{…}}`). Pin the
  informative shown frame: `api_retry` → `{conversation_id, active:true, current:3, total:10}`;
  `compacting` → `{conversation_id, active:true}`.
- `TestApiRetryPayload_RoundTrip` / `TestCompactingPayload_RoundTrip` mirror
  `TestStallPayload_RoundTrip`: decode, assert each field, then `roundTripEnvelope`
  (the byte-equal re-marshal is what catches a missing/renamed json tag).

**Layer 4 drift-detectors — `internal/protocol/compat_test.go` (REQUIRED)**
- `v2OnlyTypes` map: add `TypeApiRetry: true`, `TypeCompacting: true`.
- `TestTypeConstants_V1V2Partition`'s `all` slice: add `TypeApiRetry, TypeCompacting`
  (the union-count assertion fails otherwise — `v2OnlyTypes` grew by 2).
- `TestIsKnownAppType`'s `cases`: add `{"api_retry-rejected", TypeApiRetry, false, ErrUnknownType}`
  and the `compacting` peer (pins AC #5 at the protocol layer — an old phone's
  `IsKnownAppType` rejects the new types, so they are never dispatched inbound).
- **Do NOT touch** `TestInboundAppTypeSet_CoversAllExportedTypeConstants` — its
  `all` list and hard-coded `want := 23` are v1-app-types only; the new types are
  v2-only and never enter `inboundAppTypeSet`.

**Layer 5 — `cmd/pyry/interactive_turn_v2_test.go` (scoped subset of the `Stall*` tests)**
- `TestInteractiveTurnEmitterV2_ApiRetryFansOutToInteractiveOnly`: one
  `ApiRetry{Active:true,Current:3,Total:10}` → exactly one push, to the interactive
  conn only; decode `ApiRetryPayload` and assert `conversation_id`, `active:true`,
  `current:3`, `total:10`; non-interactive conn receives nothing (AC #4).
- `TestInteractiveTurnEmitterV2_CompactingFansOutToInteractiveOnly`: same shape for
  `Compacting{Active:true}` → `CompactingPayload{conversation_id, active:true}`.
- `TestInteractiveTurnEmitterV2_StatusPeersNoLifecycleMutation` (one test, both
  kinds): a bare `ApiRetry`/`Compacting` before any turn emits only its own frame
  (no `turn_state`/delta), and a subsequent `TextChunk` still opens a fresh turn —
  mirrors `StallNoLifecycleMutation`. Proves AC #4 "emitting does not open/close/alter a turn".
- `TestInteractiveTurnEmitterV2_ApiRetryClearAndRefire`: sequence
  `ApiRetry{true,3,10}` → `ApiRetry{true,4,10}` → `ApiRetry{false,4,10}` produces
  three `api_retry` frames carrying `current` 3, 4, 4 and `active` true, true,
  false — proving the count re-fire (AC #1) and the clear edge (AC #3) both reach
  the wire.
- The cursor-empty drop is already proven for all peers by `StallDroppedWhenCursorEmpty`
  (shared `Handle` top guard); no new test needed.

**CI gate:** `go build ./... && go vet ./... && go test -race ./internal/turnevent/... ./internal/turnbridge/... ./internal/protocol/... ./cmd/pyry/...`.

## Security

`security-sensitive` — these frames forward screen-derived data across the
tui-driver substrate seal to remote heads. See § "Security review" below for the
adversarial pass. Design-level guarantees:

- **Bounded fields only.** `ApiRetryPayload` carries `conversation_id` + `active`
  (bool) + `current`/`total` (two ints). `CompactingPayload` carries
  `conversation_id` + `active`. No field carries raw banner text, screen text, or
  any free-form string. The mapper reads only `ev.Retry.Current`/`.Total` (two
  ints tui-driver already parsed from the screen via a `\d+/\d+` regex inside the
  seal) — it never touches `ev`'s screen bytes, and there is no screen-text field
  to touch (the `Event` struct exposes none for these kinds).
- **No new logging of content.** The two `Handle` arms and `eventKind` follow the
  file's SECURITY discipline (`interactive_turn_v2.go:70-75`): logs carry only the
  content-free discriminants `event`, `kind` (`"api_retry"`/`"compacting"`),
  `conversation_id`, `turn_id`, `env_id`, `conn_id`. No counter value, no banner
  text, is ever logged.
- **Same capability gate as the whole interactive stream.** `emit()` fans out only
  to conns with `c.Interactive` true (AC #4); non-interactive conns never receive
  the frames. No new gate, no new trust decision.
- **Forward-compatible (AC #5).** The new types are v2-only (`v2OnlyTypes`), never
  in `inboundAppTypeSet`; a phone that doesn't recognize `api_retry`/`compacting`
  treats them as unknown envelope types and ignores them — pinned by the new
  `IsKnownAppType` rejection cases.

## Scope note (why ONE S, not a split)

This design modifies **6 production `.go` files** (0 new), which trips the
architect commit-time file-count heuristic (≥5 modified production files). It is
**not** split, deliberately and with authority:

- **The PO already adjudicated this during #1074's rework** (invalid `size:m` →
  `size:s`), documented on the ticket: the file count is intrinsic to threading a
  new event kind through an already-built 5-layer pipeline, not a proxy for real
  coordination. Neither split axis helps — **split-by-event** re-threads all five
  layers twice and touches the `protocol` schema (codes.go / interactive.go /
  compat_test.go) in two children (the merge-conflict the split is meant to
  *avoid*); **split-by-layer** leaves a `turnevent` type with no producer or
  consumer (untestable dead code).
- **The heuristic's target failure — undercounting that hides fan-out — does not
  apply.** All 6 files are counted openly; `codegraph_impact`/the drift-test read
  confirm **zero consumer cascade** (the only dependents are the three
  `compat_test.go` drift-detectors, enumerated in § Testing). Every arm is a small
  additive template-follow with an in-tree `Stall` exemplar.
- **Honest budget: ~300 total LOC / ~22 edits** — well inside the developer's
  ~50-turn / ~600-LOC envelope. New exported types: **4** (≤5). New production
  files: **0** (≤3). Consumer call sites: **0** (≤10). State-machine reject
  branches: **0**. Every § 1 quantitative red line is clean; only the modified-file
  count is high, and it is structurally forced by the pipeline's package layout
  (splitting files across the turnevent/turnbridge/protocol boundaries would
  violate the codebase's own conventions).

Proceeding is the evidence-based call and traces to the PO's prior decision; a
split would re-litigate a settled question and produce a strictly worse outcome.

## Open questions

- **`docs/protocol-mobile.md`** needs `§ api_retry` / `§ compacting` sections
  documenting the two new frames (mirroring `§ stall`). This is a **documentation-phase**
  task after merge, **NOT** a developer AC — the developer's worktree mutates only
  code, tests, and this spec. Flagged here so documentation picks it up.
- **Client renderers** (pyrycode-mobile #582/#583, pyrycode-desktop #488/#489)
  consume these frames; they are unblocked by this ticket. The wire field names
  (`active`, `current`, `total`) are the contract — coordinate any rename with the
  client tickets before merge.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST-FIX. The tui-driver → wire boundary is
  type-enforced: the `Event` struct exposes no string field for these kinds, only
  `Retry ApiRetryAttempt{Current, Total int}`, so raw banner/screen text
  *structurally cannot* leak — `mapEvent` names two ints, never screen bytes
  (substrate-guard stays green). The ints are pre-sanitized inside the seal
  (`apiretry.go`'s `\d+/\d+` regex + `strconv.Atoi`, overflow → `{0,0}`), so a
  hostile prompt inflating the on-screen counter yields at worst a cosmetically
  wrong "retrying N of M" on the head — no injection, no overflow on the wire.
- **[Tokens/secrets/credentials]** N/A — no token generation, storage, or
  comparison. Frames carry `conversation_id` (already wire-public on every
  interactive frame), a bool, and two ints; no credential surface.
- **[File operations]** N/A — no runtime filesystem access; the only new files are
  authoring-time test fixtures (`testdata/*.json`).
- **[Subprocess execution]** N/A — no `exec.Command`; tui-driver owns the claude
  process and this design only maps events off its existing stream.
- **[Cryptographic primitives]** N/A for added code — status peers skip
  `startTurnIfNeeded`, so no `crypto/rand` turn-id mint is on this route; the
  downstream Noise transport seal (`emit()` → `Push` → v2 manager) is unchanged.
- **[Network & I/O]** No MUST-FIX. Every payload field is bounded (bool + two
  `strconv.Atoi`-bounded ints + a daemon-minted UUID); no unbounded string. No
  amplification: tui-driver re-fires api-retry only on an actual count change and
  compaction only on show/hide edges (not per progress-tick), and the design adds
  no path outside the existing interactive stream's backpressure (ADR 025).
- **[Error messages / logs / telemetry]** No MUST-FIX. The new `Handle` arms and
  `eventKind` reuse `emit()`, which logs only content-free discriminants (`event`,
  `conversation_id`, `turn_id`, `env_id`, `conn_id`, transport-sentinel `err`).
  The counter value and any banner text are never logged; the design never holds
  banner text. SHOULD (developer + code-review): do not add any log line carrying
  `current`/`total` — the § Security guardrail is explicit.
- **[Concurrency]** No findings — zero new goroutines, locks, or shared state; the
  peers ride the existing single `Handle` goroutine, and `emit()` already returns
  on `ctx.Err()` at teardown.
- **[Threat model alignment]** Aligned with `docs/protocol-mobile.md` § Security
  model: old-phone / non-interactive exposure is blocked by the `c.Interactive`
  gate + `v2OnlyTypes` partition + `IsKnownAppType` rejection (AC #4/#5, pinned by
  the new drift-test cases); screen-content exfiltration is blocked by the
  int-only, no-string-field payload shape; turn-state tampering is blocked by the
  no-lifecycle-mutation rule (AC #4). Forwarding the attempt counter is the
  AC-sanctioned data flow, not a seal violation.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-21
