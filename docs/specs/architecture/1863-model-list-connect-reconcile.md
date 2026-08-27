# #1863 — Reconcile retained model lists to a conn on connect

Relay-side half only: an optional `V2SessionConfig` seam plus the connect-time send that
drains it. With the seam left nil this ticket changes no observable behaviour, which is what
makes it independently shippable ahead of #1864 (the daemon-side producer + wiring + e2e proof).

## Files to read first

Everything below is named by symbol. Resolve each with `codegraph_search` / `codegraph_node`
and read on demand — do not go looking for line numbers, and do not write any into comments
(`make cite-guard` fails a `//`-comment citation that lands on or inside a declaration, at any
depth, with no range exemption).

**The two things to read first, in this order — the rest of the list is support:**

- `internal/relay/v2session_modal.go` → `reconcileQueues` — **the template.** Gate shape,
  batch timestamp, per-payload marshal→envelope→Push loop, both error branches, the
  `ctx.Err()` early return, and the doc-comment structure (run-goroutine note + `SECURITY:`
  paragraph). `reconcileModelLists` is this function with the payload type swapped.
- `internal/relay/v2session_queuereconcile_test.go` — **the test template.** In particular
  `reconciledQueues` (decrypt-and-key-by-id helper), `sampleQueuePayload`, and
  `TestV2Session_QueueReconcile_ContentFreeLogging`. Read the whole file once; you are
  re-deriving its arms in a table-driven shape, not cloning its file layout.

Support:

- `internal/relay/v2session_modal.go` → `reconcileModals` — the older twin. Read only its
  doc comment, for the "why fixed envelope ID" and "pure read" phrasing.
- `internal/relay/v2session_seams.go` → `OutstandingModals`, `OutstandingQueues` — the seam
  doc-comment shape, including the verbatim "a closure returning `protocol.X`, not a
  `*pkg.Y`, because `internal/relay` does not import `internal/pkg`" boundary argument. Copy
  that reasoning; it is the reason the new seam adds no import.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit` — the success tail. The two
  existing `m.reconcileModals(ctx, s)` / `m.reconcileQueues(ctx, s)` calls sit consecutively
  between `armIdleTimer` and the `replayMissed` block; the new call goes third in that group.
- `internal/protocol/interactive.go` → `ModelListPayload`, `ModelOption` — the frame's shape
  and both `MarshalJSON` methods. Extract: the payload is closed types only, and it is
  `reflect.DeepEqual`-able (no `time.Time` field) — that is why this ticket needs no
  `equalQueued` counterpart.
- `internal/protocol/codes.go` → `TypeModelList` — the wire type constant.
- `internal/relay/v2session.go` → `Push` — the error contract the push branch's `err` field
  depends on: it returns `ctx.Err()` or `ErrConnNotFound` and nothing else, so it can never
  carry a payload byte into a log record.
- `internal/relay/v2session_test.go` → `startManager`, `genV2Keypair`, `v2PairedRegistry`,
  `silentLogger`, `waitForEnvelopes`, `waitConnOpen`, `noiseMsgsForConn`, `decryptAppFrame`,
  `lockedBuffer` — the shared harness. All already exist; write no new harness.
- `internal/relay/v2session_modal_test.go` → `openModalConn` — drives one conn from
  `noise_init` to `V2StateOpen` with a chosen capability list. Every test in this ticket
  opens conns through it.
- `docs/protocol-mobile.md` § "Reconcile on connect" (inside § Reconnect / Backfill
  semantics) — the Mode B contract this ticket is a third instance of. Read it; **do not edit
  it** (see Context).
- `docs/knowledge/features/relay-package.md` — package overview, for the V2 session-manager
  single-owner goroutine invariant.

## Context

`ModelListPayload` reaches a live interactive conn today only on the turn lane, and three
independent loss points sit in front of that lane — the `cmd/pyry` emitter's empty-conversation
early return (which is unconditional on the bootstrap child, because the conversation cursor is
only ever set by a successful route while the `initialize` ask fires at child spawn), the
droppable classification under `droppableCap`, and `forwardEnvelope`'s replay dedup, which is a
reconnect mechanism with no fresh-connect backfill. A client attaching later therefore has no
path to the list at all, and on the bootstrap session no client ever gets it.

The transport is not an open question. `docs/protocol-mobile.md` § Reconnect / Backfill
semantics binds by **data class**: bulk transcript content takes a cursor backfill, control
state always takes a cheap current-state snapshot on connect. `ModelListPayload`'s own doc
calls the frame "a SNAPSHOT of what claude will accept, not a delta, and conversation-scoped
rather than turn-scoped — receiving one neither opens nor closes a turn", which is Mode B's
data class exactly. This ticket therefore adds a third Mode B instance alongside `#877`'s
modal reconcile and `#878`'s queue reconcile, in their shape.

**No ADR.** Mode B is already a written, argued contract with two instances; a third instance
that follows it decides nothing new.

**One doc-truth note for the documentation phase / #1864, not for this developer.**
`docs/protocol-mobile.md` § "Reconcile on connect" enumerates its instances closed — "the
still-outstanding modal (#877) and the current queued backlog (#878)" — and closes with
"Mechanism internals live in #877 (modal reconcile) and #878 (queue reconcile)". A third
instance makes that enumeration stale. It does **not** go stale on this ticket's merge: with
the seam nil, no `model_list` is ever reconciled, so the paragraph stays true. It goes stale
the moment #1864 wires the producer, which is where the amendment belongs. `docs/` outside
`docs/specs/architecture/` is outside this developer's mutable surface, so it is deliberately
not an acceptance criterion here.

## Design

Three production files, one new symbol each.

### 1. The seam — `internal/relay/v2session_seams.go`

Add one optional field to `V2SessionConfig`, immediately after `OutstandingQueues`:

```go
// RetainedModelLists enumerates the daemon's currently-retained model lists as
// marshal-ready model_list payloads (one per session holding a list) for
// connect-time reconcile (#1863) — the third Mode B instance after
// OutstandingModals and OutstandingQueues.
//
// Optional: nil ⇒ no reconcile, byte-identical to the pre-#1863 posture.
RetainedModelLists func() []protocol.ModelListPayload
```

The shipped doc comment is longer than the sketch above and must carry, in the twins' own
words and order:

- **Where it is called from** — the Run goroutine, `handleNoiseInit`'s interactive-open tail;
  the returned payloads are unicast to the just-opened conn only.
- **Why a closure over `protocol.ModelListPayload` rather than a typed dependency** —
  `internal/relay` imports neither `internal/turnevent` nor `internal/sessions` directly
  (`internal/sessions` appears only transitively via `internal/control`, so a `go list -deps`
  reading looks like a contradiction and is not one). `internal/protocol` *is* a direct
  import, so the payload crosses the boundary with no new import and no cycle. This is the
  same argument `OutstandingModals` and `OutstandingQueues` state verbatim.
- **Enumerate-all, not conversation-keyed.** A `V2Session` carries no conversation id — the
  struct holds `connID`, `state`, `resp`, `send`, `recv`, `device`, `interactive`,
  `peerStatic` and nothing else — so there is no "this conn's conversation" to key on at
  connect time. `OutstandingQueues`' one-per-conversation enumerate-all shape is the
  precedent; `RunConfigFor` is the conversation-keyed variant and is the wrong shape here.
- **A pure read** — it mints nothing, retires nothing, and changes no daemon state, so
  re-connecting re-sends the same snapshot idempotently.
- **The bound is the producer's.** The payloads arriving through this seam are already
  bounded at construction time (`DroppedModels` on the aggregate, `TruncatedFields` per
  entry). This path applies no bound of its own and must say so, so the obligation lands on
  #1864's producer rather than being silently assumed. See § Security review.

### 2. The reconcile — `internal/relay/v2session_modelreconcile.go` (new file)

```go
func (m *V2SessionManager) reconcileModelLists(ctx context.Context, s *V2Session)
```

Unicasts the current retained-model-list set to a freshly interactive-open conn. Structure,
in order, identical to `reconcileQueues`:

1. `if !s.interactive || m.cfg.RetainedModelLists == nil { return }` — capability gate plus
   the unwired/foreground opt-out.
2. `retained := m.cfg.RetainedModelLists()`; `if len(retained) == 0 { return }`.
3. One `ts := time.Now().UTC()` shared by the whole batch.
4. Per payload: `json.Marshal` → on error log and `continue`; else build the envelope and
   `m.Push(ctx, s.connID, env)` → on error log, and `return` if `ctx.Err() != nil`.

Envelope fields: `ID: 1` (non-load-bearing — a client correlates on `conversation_id`, the
same justification the twins give for their fixed ID), `Type: protocol.TypeModelList`,
`TS: ts`, `Payload: payload`, and `EventID` left nil.

`EventID: nil` is load-bearing twice and the doc comment must say both: it keeps the frame out
of the turn-event replay ring, and it makes `forwardEnvelope`'s `last_event_id` dedup inert for
this frame — the third loss point named in Context cannot re-drop what this path sends.

**No turn is opened** because nothing on this path touches turn state: the frame goes straight
onto the conn's push queue via `Push`, never through `forwardEnvelope`, and `TypeModelList` is
not a turn-boundary type ("receiving one neither opens nor closes a turn", frozen by #1704 /
#1705). Operationally this is pinned by the tests asserting the exact envelope count and that
every `noise_msg` reaching the conn decodes as `TypeModelList`.

**New file, not appended to `v2session_modal.go`.** The twins share that file only because
#877 landed before the monolithic `v2session.go` was split; the test files are already
one-per-reconcile. `modal` and `model` differ by one letter, and a model-list reconcile living
in a modal-named file is a readability hazard worth one file to avoid.

**Keep the defensive marshal branch and do not try to redden it — there is no test that can.**
`ModelListPayload` is `string` + `[]ModelOption` + `int`, `ModelOption` is three strings, two
`[]string` and a bool, and both custom `MarshalJSON`s delegate to `json.Marshal` over those
closed types. No value of the payload can fail to marshal. Neither analogue test file contains
a marshal test, for exactly this reason. Write the branch for the defensive reason the twins
state; leave it unpinned. Do not spend turns hunting a mutant for it.

### 3. The call site — `internal/relay/v2session_handshake.go`

One `m.reconcileModelLists(ctx, s)` in `handleNoiseInit`'s success tail, immediately after
`m.reconcileQueues(ctx, s)` and before the `replayMissed` block, with a comment in the twins'
register. Ordering relative to the other two is immaterial (distinct payload types); third
keeps the time-sensitive permission prompt first, matching the reason `reconcileQueues` is
already second. Ordering relative to `replayMissed` is immaterial for the twins' stated reason
— the reconcile lands in `m.queues` while `replayMissed` enqueues into the separate
`replayQueue`.

**Do not propose splitting the call site off.** `make check` runs `staticcheck` with defaults,
so U1000 fails an uncalled unexported method: seam + `reconcileModelLists` without the call
site cannot pass the gate.

### Data flow

```
handleNoiseInit success tail (Run goroutine, one conn)
  └─ reconcileModelLists(ctx, s)
       ├─ !s.interactive || cfg.RetainedModelLists == nil ─→ return          (AC2)
       ├─ retained := cfg.RetainedModelLists()   [#1864 wires the producer]
       ├─ len(retained) == 0 ─→ return                                       (AC2)
       └─ for each payload:  json.Marshal ─→ Envelope{TypeModelList, EventID:nil}
                             └─ m.Push(ctx, s.connID, env)   [unicast]       (AC1)
```

## Concurrency model

No new goroutine, no new lock, no change to any existing lock's scope.

`reconcileModelLists` runs **only** on the manager's Run goroutine, called synchronously from
`handleNoiseInit`. That is what lets it read `s.interactive` and `s.connID` lock-free under
the package's single-owner invariant — `reconcileModals`' doc states the rule and the new doc
comment must restate it, because the guarantee is the caller's, not the function's.

`m.Push` takes `pushMu` internally; that is the only lock on the path and it is already this
function's callee in both twins. The push queue for `s.connID` was created a few statements
earlier in the same `handleNoiseInit` on this same goroutine, so `ErrConnNotFound` is
unreachable here — the twins say so and the new comment should too, since it explains why the
push branch is `Debug` rather than `Warn`.

Shutdown: `ctx` is the manager's `runCtx`. A `Push` failure with `ctx.Err() != nil` returns
immediately — the session is going away and the remaining payloads have nowhere to land. Any
other `Push` error skips that payload and continues.

## Error handling

Exactly two branches, both content-free by construction.

| Branch | Level | Fields | Recovery |
|---|---|---|---|
| `json.Marshal` fails | `Warn` | `event: "v2.modellist.reconcile.marshal_err"`, `conn_id`, `conversation_id` | skip this payload, keep sending the rest |
| `m.Push` fails | `Debug` | `event: "v2.modellist.reconcile.push_err"`, `conn_id`, `err` | return if `ctx.Err() != nil`, else skip and continue |

Two rules the developer must not relax:

- **The marshal branch must not log `err`.** `encoding/json`'s error strings quote the input
  bytes, so echoing it would put model values — the exact fields #833 exists to keep out of
  logs — into a log record. `conversation_id` is the only discriminant, and it is a routing
  key that already crosses the wire in both directions.
- **The push branch may log `err`** only because `Push` returns `ctx.Err()` or
  `ErrConnNotFound` and nothing else. That is an inherited contract, not a local property;
  name `Push` in the comment so a future change to its error values is visibly load-bearing.

The success path logs nothing at all. A connect-time reconcile fires on every handshake, the
same routine-read cadence `handleRequestSessionSettings` cites when it logs `conn_id` and
nothing else.

## Testing strategy

One new file, `internal/relay/v2session_modelreconcile_test.go`. Reuse the harness entirely —
`startManager`, `openModalConn`, `waitForEnvelopes`, `waitConnOpen`, `noiseMsgsForConn`,
`decryptAppFrame`, `lockedBuffer`, `silentLogger`, `genV2Keypair`, `v2PairedRegistry`. Write
no new harness and no comparator helper: `ModelListPayload` has no `time.Time` field, so
`reflect.DeepEqual` is safe and `#878`'s `equalQueued` / `sampleQueueTS` have no counterpart
here.

**Two helpers only:**

- `sampleModelListPayload(convID string, values ...string) protocol.ModelListPayload` —
  builds a fully-populated payload with one `ModelOption` per value (distinct
  `ResolvedModel` / `Value` / `DisplayName`, a non-empty `EffortLevels`, and
  `SupportsAutoMode` set on one entry so the bool is carried, not defaulted).
- `reconciledModelLists(t, rec, connID, recv) map[string]protocol.ModelListPayload` — the
  `reconciledQueues` shape: decrypt every `noise_msg` for `connID` in recorded order, fatal
  if any decodes to a `Type` other than `TypeModelList` (this is where "no turn opened" is
  actually asserted), error on a repeated `conversation_id` (fan-out / dup), key by
  `conversation_id`. Decrypt each conn's frames exactly once per test — `Decrypt` advances
  the recv nonce.

**Write the arms table-driven where the setup differs only by config field** (`CODING-STYLE`
§ table-driven; `v2session_dequeue_test.go`, `v2session_interrupt_test.go` and
`v2session_modal_test.go` already do this in this package). Do **not** clone `#878`'s
one-function-per-arm file layout — same coverage, materially fewer lines, and it is the
house idiom.

Four test functions:

1. **`TestV2Session_ModelListReconcile_Delivery`** — table over payload count, one interactive
   conn per row, `reflect.DeepEqual` against the seam's own payloads (AC1):
   - one retained list ⇒ `waitForEnvelopes` 2 (`noise_resp` + one `model_list`), exactly one
     payload back, deep-equal including `Models`, `DroppedModels` and each entry's
     `EffortLevels` / `SupportsAutoMode` / `TruncatedFields`.
   - two retained lists for distinct conversations ⇒ 3 envelopes, both payloads back, matched
     by `conversation_id`, order-independent.
   - a payload with `DroppedModels` non-zero and an entry with a non-empty `TruncatedFields`
     ⇒ both survive the round trip (they are the frame's bound reporters; a mapping that
     silently zeroed them would otherwise pass every other row).

2. **`TestV2Session_ModelListReconcile_UnicastOnlyOpeningConn`** — A open, then B opens; each
   receives exactly one `model_list`, and A receives no second one. Four envelopes total in
   the correct case; a broadcast on B's open would push a fifth, which the count plus the
   helper's per-conn dup check catches (AC1's "unicast to that conn only").

3. **`TestV2Session_ModelListReconcile_NoFrame`** — table over the three AC2 arms, each
   asserting zero `noise_msg` for the conn and a still-open session (`waitConnOpen`), so
   "no frame **and no error**" is both halves:
   - seam left nil.
   - seam returns `nil` (zero payloads).
   - conn handshakes without `protocol.CapabilityInteractive`. This row needs a second,
     interactive conn opened *after* it, proving the seam *was* enumerating a list — otherwise
     the zero is indistinguishable from an empty seam. Open the non-interactive conn first and
     let it settle, as `#878`'s non-interactive test does.

4. **`TestV2Session_ModelListReconcile_ContentFreeLogging`** — AC3, copying
   `TestV2Session_QueueReconcile_ContentFreeLogging`'s structure. Capture at `slog.LevelDebug`
   (the lowest level, so any leak surfaces); assert the capture is live by requiring the
   handshake line before asserting absence, then assert **all three** distinctive sentinels
   are absent: a `Value`, a `ResolvedModel` and a `DisplayName`, each a distinct
   unmistakable literal. Three sentinels, not one — the fields are separate struct members and
   a branch could echo one without the others.

**Not tested, deliberately:** the marshal branch (unsatisfiable — see § Design). Say so in the
test file's header comment so a later reader does not read the gap as an oversight.

Gate: `make check` (this package is not behind a build tag; `go test -race ./internal/relay/`
is the inner loop).

## Open questions

- **Seam name.** `RetainedModelLists` is chosen over an `Outstanding*` spelling because a
  model list is retained state, not an outstanding item awaiting resolution, and "retained" is
  the vocabulary #1840's `sessionModelHold` and #1857's `resolveBoundModelList` already use.
  If #1864's producer lands on a different noun, renaming one unexported-adjacent config field
  is cheap; do not block on it.
- **Whether the enumeration key should be session id rather than conversation id.** Out of
  scope here — the seam returns fully-formed `ModelListPayload`s and the payload's own key is
  `conversation_id`. #1864 owns resolving a session's retained list to a conversation id, and
  it is the ticket that will discover whether a `V2Session`-carried session id would have been
  the better join. Nothing in this ticket forecloses it.

## Sizing note (recorded, not deferred)

Re-checked against the six `size:s` boundaries after writing this spec. Production source
files 3 (at the limit), new exported types/interfaces 1 (a func-typed field on an existing
struct, not a new type), consumer call sites 1, acceptance criteria 3, error branches 2 — all
clear.

**Total written work is the one line that does not clear: ~420 lines against a ≤400 ceiling,
~5% over.** Derivation, in insertions (both analogue commits are pure additions):
`#878` `878dbd88` filtered to this ticket's scope is 97 production + 386 test = 483; subtract
`equalQueued` and `sampleQueueTS` (~25, unneeded because `ModelListPayload` is
`reflect.DeepEqual`-able) for a shape-matched floor of ~458 at the twin's file layout. The
table-driven collapse specified above removes three test functions and their doc comments,
landing at ~105 production + ~310 test + spec-doc ticks ≈ 420. `#877` `4a70fa29` in scope is
105 + 417 = 522 and is the weaker anchor — two of its tests are modal-registry-specific.

Not routed back, because **no seam exists** and a split cannot reduce the total. Three cuts
were checked against the source, not against the ticket's prose, and each is independently
forbidden: seam+method ‖ call site fails `staticcheck` U1000 on an uncalled unexported method;
behaviour ‖ capability gate ships a merge window in which the reconcile reaches a conn that
never negotiated `interactive`; behaviour ‖ content-free logging ships a merge window of #833
violations. Splitting would add a second pipeline run's overhead on top of the same ~420
lines. This is #1434's shape — a ticket already split once (from #1858, with the daemon half
carved out as #1864) with no remaining seam — and its remedy is applied here: pay the
discovery at architect time, which is what the reading list and the named-helper reuse above
are for. PO's own route-back triggers (a fourth production file, or a test file heading past
~350 lines) are both clear; if either trips during implementation, route back rather than
absorb.

Flagged so an operator can recalibrate the boundary if it is genuinely wrong, and so the next
ticket in this family inherits the measurement rather than the estimate.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No finding, with a stated decision. This path is outbound-only: it
  reads a daemon-owned snapshot through `RetainedModelLists` and writes to an already-
  authenticated, already-AEAD-sealed conn. It parses nothing and accepts no network input, so
  it introduces no new boundary. The payload content is *claude*-originated text, which
  `ModelOption`'s own doc is explicit is not trusted merely because the daemon published it —
  and the inbound direction re-validates at `validModel` in `v2session_settings.go`. That
  inbound re-validation is untouched here; publishing a value on this path does not widen what
  the daemon will later accept back.
- **[Trust boundaries / resource bounds]** SHOULD FIX, discharged by documentation. This path
  applies **no bound of its own** — not on the number of payloads the seam returns, not on any
  entry's text. It relies entirely on the construction-time bound upstream (`DroppedModels`
  on the aggregate, `TruncatedFields` per entry, frozen by #1704/#1705 and enforced at decode
  by #1808/#1809). That is the correct place for it, but an undocumented assumption here
  becomes #1864's silent obligation. **The seam's doc comment must state that it accepts
  already-bounded payloads only**; § Design makes that a required element of the comment.
- **[Error messages, logs, telemetry]** No finding, by construction, and it is the
  category this ticket is labelled for. The success path logs nothing. The marshal branch
  carries `event` + `conn_id` + `conversation_id` and is explicitly forbidden from echoing
  `err`, because `encoding/json` quotes input bytes and a model value would land in the log.
  The push branch's `err` is safe only because `Push` returns `ctx.Err()` or `ErrConnNotFound`
  and nothing else — an inherited contract, named in the comment so a later change to `Push`'s
  error values is visibly load-bearing. `conn_id` and `conversation_id` are routing keys that
  already cross the wire, not secrets. AC3 is pinned by three distinct sentinels
  (`Value`, `ResolvedModel`, `DisplayName`) rather than one, so a branch that echoed a single
  field could not pass.
- **[Concurrency]** No finding. No goroutine is spawned and no lock is added. `s.interactive`
  and `s.connID` are read lock-free under the Run-goroutine single-owner invariant, which is
  the caller's guarantee — hence the requirement that the doc comment restate it rather than
  imply it. The only lock on the path is `pushMu`, taken inside `Push`, unchanged. No
  check-then-mutate: the function mutates nothing.
- **[Network & I/O]** No finding on amplification, with the reasoning stated. A connect
  produces N marshals and N pushes, so a peer that reconnects repeatedly repeats that work.
  The multiplier is bounded above by the retained-list count and each payload is bounded by
  the upstream caps; the connect itself is gated by a paired device, a completed Noise_IK
  handshake and a validated token, and session churn is bounded by the idle sweep armed in the
  same `handleNoiseInit` tail. Both twins have the identical property and it has not been
  observed to bite. No new cap is warranted on an unobserved failure mode.
- **[Network & I/O / delivery]** No finding. Backpressure is `Push`'s existing bounded queue
  with its overflow accounting; this path adds frames to it and handles the failure by
  skipping, never by blocking or retrying. `EventID: nil` keeps the frame out of the
  turn-event replay ring, so it adds no per-conversation ring memory.
- **[Tokens, secrets, credentials]** Not applicable — this path handles no token, key or
  credential. The frame rides the already-established session keys; nothing here touches
  `StaticPriv`, the device registry, or the rekey timer.
- **[File operations]** Not applicable — no filesystem access on this path.
- **[Subprocess execution]** Not applicable — no `exec` on this path.
- **[Cryptographic primitives]** Not applicable — the reconcile hands a plaintext envelope to
  `Push`; sealing is the existing transport's, with no new key, nonce or comparison
  introduced. Notably it does **not** mint an id: envelope `ID: 1` is a fixed non-load-bearing
  constant, so no RNG is involved and none is needed.
- **[Threat model alignment]** Addressed. `docs/protocol-mobile.md` § Reconnect / Backfill
  semantics governs the transport choice and this design follows Mode B rather than arguing
  around it; § Security model's capability discipline is honoured by gating on `s.interactive`
  — the same negotiated flag the twins and `broadcastModalDismissed` gate on — so a conn that
  never advertised the interactive capability receives nothing. Out of scope and named: the
  daemon-side producer's own bound and its conversation-id resolution are #1864's.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-27
