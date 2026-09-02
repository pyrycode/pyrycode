# #2006 — Connect-time slash-command-list reconcile

`security-sensitive`. The fifth Mode B instance of the reconcile-on-connect mechanism: a
freshly interactive-open conn is unicast the daemon's currently-retained
`slash_command_list` set on the handshake's success tail.

## Files read

- `internal/relay/v2session_modelreconcile.go` → `reconcileModelLists` — the direct twin
  (#1863). Same reply (`initialize`), sibling frame, identical guard/marshal/push shape.
  Its doc block is the template for mine, including the three-loss-point paragraph and
  the "EventID left nil is load-bearing twice" sentence.
- `internal/relay/v2session_questionreconcile.go` → `reconcileQuestions` — the most recent
  twin (#1979) and the file whose header records why each reconcile takes its own file.
- `internal/relay/v2session_seams.go` → `RetainedModelLists`, `OutstandingQuestions`,
  `V2SessionConfig` — where the new seam field goes and the doc conventions it must keep
  (enumerate-all reasoning, closure-not-concrete-import, "bound is the producer's",
  `nil ⇒ no reconcile`).
- `internal/relay/v2session_handshake.go` → `handleNoiseInit` — the success tail. Four
  reconcile calls already sit between `armIdleTimer` and the `replayMissed` hook; mine is
  the fifth and goes last.
- `internal/relay/v2session.go` → `Push` — the error contract this design leans on:
  `ctx.Err()` or `ErrConnNotFound`, and nothing else. The queue-overflow path returns
  `nil`, so a full queue is not a push failure.
- `internal/protocol/interactive.go` → `SlashCommandListPayload`, `SlashCommand`, both
  `MarshalJSON` methods — the payload's closed-type shape (which is what makes the marshal
  branch unredenable) and the SECURITY paragraph naming `Name` / `ArgumentHint` /
  `Description` / `Aliases` as workspace-authored untrusted text.
- `internal/protocol/codes.go` → `TypeSlashCommandList` — outbound-only, deliberately
  absent from `inboundAppTypeSet`, and already recorded in `cmd/pyry/relay_guard_test.go`'s
  `excludedTypes` as a push. So this slice adds no handler and trips no drift detector.
- `internal/relay/v2session_modelreconcile_test.go` → `sampleModelListPayload`,
  `reconciledModelLists`, the four `TestV2Session_ModelListReconcile_*` functions — the
  table-driven test layout to mirror, and the header note explaining the deliberate
  marshal-branch coverage gap.
- `internal/relay/v2session_questionreconcile_test.go` → `reconciledQuestions` — carries the
  nil-`EventID` assertion inside the decrypt helper, which the model twin's helper lacks.
  Mine copies the question shape, since AC3 names it explicitly.
- `docs/knowledge/features/v2-session-manager-state-machine-connect-time-model-list-reconcile-retain.md`
  — three lessons that change how this is built, not just described:
  1. **staticcheck U1000 forbids shipping the seam plus an unexported method without the
     call site.** An uncalled unexported method fails `make check`. This is why the
     handshake line is part of *this* slice and cannot be deferred to #2007.
  2. **The non-interactive test row needs a second, genuinely-interactive control conn.**
     Without one, "zero frames for conn A" is indistinguishable from an empty seam and the
     `!s.interactive` mutant survives green.
  3. **The `len(retained) == 0` early return has no sole-red mutant** — with a zero-length
     slice the loop body never runs, so deleting the check is an equivalent mutant. The
     "zero payloads" row pins the *contract* (a producer returning `nil` must not error),
     not a reachable branch. Worth stating so it does not read as coverage skipped.
- `cmd/pyry/session_slash_command_list.go` → `resolveBoundSlashCommandList` (#2005) — read
  to confirm it is the *wrong* shape here and to leave it alone. It is conversation-keyed;
  a `V2Session` has no conversation id. Bridging the two is #2007's job.

## Context

`SlashCommandListPayload` reaches a live conn on exactly one path today — the interactive
turn lane, via `internal/turnbridge/outbound.go`'s emitter — and three independent loss
points sit in front of it:

1. The emitter's empty-conversation early return, which is unconditional for the bootstrap
   child: the conversation cursor is only ever set by a successful route, while the
   `initialize` ask fires at child spawn.
2. The droppable classification under `droppableCap`, which sheds the frame under load.
3. `forwardEnvelope`'s `last_event_id` dedup — a *reconnect* mechanism with no fresh-connect
   backfill.

A client attaching later therefore has no path to the list at all, and its slash-command
menu stays empty until a turn that may never come.

`docs/protocol-mobile.md` § Reconnect / Backfill semantics splits reconciliation by **data
class**, not by how long the client was away, and puts control state in Mode B: a
current-state snapshot re-asserted on connect. A slash-command list is exactly that class —
decoded from one `initialize` reply, session configuration rather than a turn event, and
receiving one neither opens nor closes a turn. The re-send is idempotent by construction:
the frame is snapshot-shaped full state, not a delta.

The shipped Mode B instances are `reconcileModals` (#877), `reconcileQueues` (#878),
`reconcileModelLists` (#1863) and `reconcileQuestions` (#1979). This is the fifth.

**No ADR is warranted.** Mode B is already an established mechanism with four instances and
a protocol-doc section; this adds an instance, not a decision. The documentation phase folds
the lessons below into the package overview.

### Size — the overage is deliberate, and stated

Re-counted against this written plan (§ A4):

| Boundary | Limit | This ticket |
|---|---|---|
| Production source files created/modified | ≤ 5 | **3** |
| Total written work | ≤ 800 | **~880** ✗ |
| New exported types or interfaces | ≤ 5 | **0** (a seam *field*, not a type) |
| Consumer call sites needing simultaneous update | ≤ 10 | **1** |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches | ≤ 10 | **2** (marshal, push) |

One line trips, by ~10%. It ships as one ticket anyway, for two independent reasons and not
by rationalising the count down:

- **The floor rule bars every candidate split.** The only seams available are (a) the seam
  declaration alone and (b) the reconcile function without its call site. (a) is consumed by
  nothing outside the family; (b) is an uncalled unexported method that fails `make check`
  under staticcheck U1000, as the model-list overview records. Both are one-consumer slices,
  and when the floor and the ceiling disagree the floor wins.
- **The depth gate bars it regardless.** `parent 1720 grandparent 1683` — a further split is
  off the table.

Since there is no split to propose, no `needs-human:sizing` marker is added: the label marks
a split judgement deferred for human review, and here the judgement is settled by two
independent rules rather than left open. The overage is recorded here instead, which is the
floor rule's own prescribed remedy.

Nearest analogues, both merged clean as single tickets: #1863 (516 code + 417 spec = 933) and
#1979 (599 code + spec).

## Design

Three production files, no new package, no new exported type.

### 1. The seam — `V2SessionConfig.RetainedSlashCommandLists`

```go
RetainedSlashCommandLists func() []protocol.SlashCommandListPayload
```

Declared in `v2session_seams.go` immediately after `OutstandingQuestions`, in family order.
Contract, in its doc block:

- **Enumerate-all, not conversation-keyed.** A `V2Session` holds `connID`, `state`, `resp`,
  `send`, `recv`, `device`, `interactive` and `peerStatic` — no conversation id — so there is
  nothing to key on at connect time. Each payload self-identifies by its own
  `conversation_id`. `RetainedModelLists` states the same reasoning and is the precedent;
  #2005's `resolveBoundSlashCommandList` is the conversation-keyed variant and is
  deliberately *not* reached for here.
- **A closure returning `[]protocol.SlashCommandListPayload`**, not a `*sessions.Pool` or a
  `turnevent` value — `internal/relay` imports neither, and `internal/protocol` is already a
  direct import, so the payload crosses with no new import and no cycle.
- **Order is not part of the contract.** A caller correlates by `conversation_id`, never by
  position. (The #2007 producer will walk a registry whose order is unspecified.)
- **The bound is the producer's.** This path applies no cap of its own — not on payload count,
  not on any entry's text. The bound is decided upstream at construction
  (`SlashCommandListPayload.DroppedCommands` on the aggregate,
  `SlashCommand.TruncatedFields` per entry). A second cap here would be a second place the
  limit is decided and the two could disagree silently.
- **Optional: `nil` ⇒ no reconcile**, byte-identical to the pre-#2006 / foreground /
  existing-test posture. **This slice ships it unwired**, which is the nil-resolver posture
  the other optional control seams already share; #2007 wires the daemon-side enumeration.

### 2. The reconcile — `reconcileSlashCommandLists`, in its own file

New file `internal/relay/v2session_slashreconcile.go`. Its own file for the reason both twins
give for theirs: `reconcileSlashCommandLists` beside `reconcileQuestions` and one letter from
`reconcileModals`/`reconcileModelLists` is a readability hazard worth one file to avoid, and
the test files were already one-per-reconcile.

Signature and behaviour (contract, not body):

```go
func (m *V2SessionManager) reconcileSlashCommandLists(ctx context.Context, s *V2Session)
```

1. **Guard:** `!s.interactive || m.cfg.RetainedSlashCommandLists == nil` ⇒ return. Identical
   shape to the four twins.
2. **Snapshot once**; `len(retained) == 0` ⇒ return.
3. **One timestamp** shared by the batch (`time.Now().UTC()`), matching the twins.
4. **Per payload:** `json.Marshal`, then `m.Push(ctx, s.connID, env)` where `env` is
   `protocol.Envelope{ID: 1, Type: protocol.TypeSlashCommandList, TS: ts, Payload: payload}`
   with `EventID` left nil.

`ID: 1` is non-load-bearing — the client correlates on `conversation_id` — so callers and
tests must never index by position. `EventID` nil is load-bearing twice: it keeps the frame
out of the #647 turn-event replay ring (so this path adds no per-conversation ring memory),
and it makes `forwardEnvelope`'s `last_event_id` dedup inert for the frame, so loss point 3
above cannot re-drop what this path sends.

**No turn is opened.** Nothing here touches turn state: the frame goes straight onto the
conn's push queue via `Push`, never through `forwardEnvelope`, and `TypeSlashCommandList` is
not a turn-boundary type.

### 3. The call site — `handleNoiseInit`'s success tail

One line after `m.reconcileModelLists(ctx, s)`, still before the `replayMissed` hook.

Correctness does not depend on the position: the five reconciles carry distinct payload types
and none reads another's effect. The tail is ordered by time-sensitivity — permission prompt
first — and a command menu is the least time-sensitive of the five, so it goes last.

## Concurrency model

**No new goroutine and no new lock.** Run-goroutine only, called from `handleNoiseInit`'s
success tail, so `s.interactive` and `s.connID` are read lock-free under the package's
single-owner invariant — a guarantee owned by the caller, restated in the function's doc
comment per the twins' precedent rather than left implied.

`m.Push` takes `pushMu` internally and is the only lock on the path. No lock is held across
the `json.Marshal` or across the seam call. The seam is invoked once, on the Run goroutine,
and its result is a local slice; nothing on this path mutates what it read.

The reconcile reaches no `cmd/pyry` emitter state: the payloads come from a pure daemon-side
read and the envelope ID is fixed, so the send is entirely relay-side with no cross-goroutine
coupling.

## Error handling

Two branches, both inside the per-payload loop.

- **Marshal failure** ⇒ log at Warn with content-free discriminants only (`event`, `conn_id`,
  `conversation_id`) and `continue` to the next payload. **The error is deliberately not
  echoed**: `encoding/json` quotes the input bytes, so a command name would land in the
  record. This branch is defensive and cannot be reddened — `SlashCommandListPayload` is a
  closed struct of `string` / `[]SlashCommand` / `int`, `SlashCommand` is three strings and
  two `[]string`, and both custom `MarshalJSON`s delegate to `json.Marshal` over those closed
  types. No fixture exists that reaches it. Neither twin has a marshal test, for the same
  reason; the test file's header will say so, so the gap reads as a decision rather than an
  oversight.
- **Push failure** ⇒ log at Debug with `event`, `conn_id`, `err`; then `ctx.Err() != nil` ⇒
  `return` (teardown: the session is going away and the remaining payloads have nowhere to
  land), otherwise `continue`. Echoing `err` is safe here *only* because `Push` returns
  `ctx.Err()` or `ErrConnNotFound` and nothing else — an inherited contract, named in the
  comment so a later change to `Push`'s error values is visibly load-bearing.
  `ErrConnNotFound` is unreachable from this call site (the push queue is created a few
  statements earlier in the same `handleNoiseInit` on the same goroutine), which is why this
  is Debug rather than Warn.

**The success path logs nothing at all.** A connect-time reconcile fires on every handshake —
the routine-read cadence `handleRequestSessionSettings` cites when it logs `conn_id` and
nothing else.

## Testing strategy

New file `internal/relay/v2session_slashreconcile_test.go`, table-driven per the house idiom
this package already uses. Header note records the deliberate marshal-branch gap.

Helpers:

- `sampleSlashCommandListPayload(convID string, names ...string)` — one `SlashCommand` per
  name with every field distinct and non-zero (name, argument hint, description, one alias),
  so a mapping that dropped or crossed a field cannot survive `reflect.DeepEqual`.
  `SlashCommandListPayload` has no `time.Time` field, so `DeepEqual` is safe and #878's
  `equalQueued` has no counterpart.
- `reconciledSlashCommandLists(t, rec, connID, recv)` — decrypts every `noise_msg` for the
  conn in recorded (AEAD-nonce) order and returns each payload keyed by `conversation_id`.
  Fatals on any non-`slash_command_list` type (this is where "no turn opened" is asserted — a
  turn-boundary frame would have to surface here), errors on a repeated `conversation_id`,
  and **asserts `inner.EventID == nil`** — the question twin's shape, since AC3 names it.

Scenarios:

- **Delivery (AC1).** Table: one retained list; two conversations; and a bounded row with
  `DroppedCommands` non-zero and `TruncatedFields` set on one entry — the frame's only record
  that the producer cut something, so a mapping that silently zeroed them would pass every
  other row. Compared by `reflect.DeepEqual` against the seam's own values, keyed by
  `conversation_id`, so the match is order-independent.
- **Unicast only to the opening conn (AC1).** With A open, opening B delivers to B only; A
  receives no second frame. Envelope count plus the helper's per-conn duplicate check catches
  a fan-out.
- **No frame, no error (AC2).** Three rows — nil seam, zero-payload seam, capability not
  negotiated — each asserting zero `noise_msg` for the conn under test *and* that the session
  is still enumerable-open. The capability row opens a second interactive control conn
  afterwards; without it, A's zero is indistinguishable from an empty seam and the
  `!s.interactive` mutant survives.
- **Content-free logging (AC4).** Four distinct sentinels — a command name, an argument hint,
  a description and an alias — since the four are separate struct members and a branch echoing
  one of them must not pass. Captured at Debug (the lowest level) so any leak surfaces; the
  handshake-accept line proves the capture is live, so the never-log assertion is non-vacuous.
- **Context teardown stops the batch (AC5).** The twins ship this branch untested; AC5 names
  it, so it gets a test. Calling the reconcile directly with an already-cancelled context and
  a hand-built `V2Session` (a same-package unit test — the package already unit-tests
  unexported methods) makes `Push` return `ctx.Err()` on the first payload. Asserting *zero*
  envelopes is not sole-red — without the early return the loop still pushes nothing — so the
  assertion is on the **count of `push_err` records**: exactly one with the early return, one
  per payload without it. That distinguishes `return` from `continue`, which is the whole
  content of the clause.
  - The other half of AC5 — "any other push failure skips that one payload and continues" —
    has no reachable fixture: `Push`'s only non-ctx error is `ErrConnNotFound`, unreachable
    from this call site by construction. Recorded here rather than left as a silent gap.
- **The full-module race suite is the verifier's gate.** My own gate is
  `go test -race ./internal/relay/...`, `go vet ./...`, `go build ./cmd/pyry`.

## Open questions

1. **Seam field name** — `RetainedSlashCommandLists` vs `SlashCommandLists`. Resolved in
   favour of the former before writing: `Retained*` is the twin's prefix for exactly this
   data class (a list held from a past `initialize` reply), and it reads correctly against
   #2007's future `retainedSlashCommandLists` producer, matching #1867's `retainedModelLists`.
2. **File name** — `v2session_slashreconcile.go` vs `v2session_slashcommandreconcile.go`.
   Resolved to the former: the neighbours are `v2session_modelreconcile.go` /
   `v2session_questionreconcile.go`, and the short form keeps the family's shape.
3. **Does the AC5 teardown test need the manager's Run goroutine?** To resolve in Phase B by
   writing it. If a hand-built `V2Session` plus a cancelled context is enough (expected —
   `Push` checks `ctx.Err()` before touching any map), the test needs no handshake at all and
   stays ~40 lines. Any departure from what this section describes is recorded under
   `## Revisions`.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The boundary this slice owns is the seam. What crosses
  it is workspace-authored text that already crossed the subprocess boundary upstream at the
  `initialize` decode, and it is forwarded **unsanitised** — no control-character or
  terminal-escape stripping — which is `SlashCommand`'s own documented decision (the render
  boundary owing the sanitisation is the client's) rather than an omission here. Go's type
  system cannot mark a `string` untrusted, so the obligation is restated in the reconcile's
  and the seam's doc blocks, the twins' precedent. `Description` may contain newlines and
  0x0a is the only sub-0x20 byte across the committed capture's 51 entries — which is exactly
  why nothing on this path may put that text in a log record (see [Errors/logs]).
- **[Trust boundaries]** OUT OF SCOPE — **no per-device confinement.** The enumerate-all shape
  unicasts *every* retained list to *any* interactive conn, so a paired device sees the
  daemon's whole slash-command inventory, including conversations bound to other workspaces.
  This is not new and not specific to this frame: `OutstandingQueues`, `RetainedModelLists`
  and `OutstandingQuestions` have the identical shape, grounded in the daemon's pairing model
  (a paired interactive device is trusted with daemon state). Named here so it is a recorded
  posture rather than an unexamined default; if confinement is ever wanted it belongs to the
  Mode B umbrella (#829), not to one instance.
- **[Tokens/secrets]** No findings. Nothing on this path generates, stores, compares, rotates
  or revokes a credential, and `V2SessionConfig.StaticPriv` is untouched. The one identifier
  reaching a log record is `conversation_id`, and it is **server-minted** — `conversations.NewID()`
  in `create_conversation`'s handler, never a value the phone chooses — so it is neither a
  secret nor a log-injection vector, and that handler already logs it on two of its own
  branches.
- **[File operations]** Not applicable by construction: no path is built, opened, statted,
  written or removed. The path is a pure in-memory read → `json.Marshal` → `Push`, so there is
  no traversal, TOCTOU, file-mode, symlink or atomic-write question to answer.
- **[Subprocess]** No findings, and the adjacent risk is specifically closed rather than
  absent. `SlashCommand.Name` is text a client is *meant to send back* — sending the slash
  command is the feature — but `TypeSlashCommandList` is deliberately absent from
  `inboundAppTypeSet` and is recorded in `cmd/pyry/relay_guard_test.go`'s `excludedTypes` as a
  push, so a phone cannot send a `slash_command_list` frame into `dispatch.Route`. This slice
  declares no inbound verb and adds no handler, and no field on this path reaches a child as
  an argv element.
- **[Cryptographic primitives]** No findings. No RNG, no key derivation, no comparison against
  a secret, no primitive selected. The one real hazard in this family is **Noise send-nonce
  burn** (#874: a burned nonce gaps the phone's recv nonce and 4421-closes a live session),
  and the design is immune structurally because it never seals — it calls `Push`, which only
  enqueues, and `drainOnce` consults `Connected` before sealing. A variant that sealed
  directly on the handshake tail *would* be a MUST FIX; the `Push`-not-seal choice is
  load-bearing rather than incidental and the doc block says so.
- **[Network & I/O]** OUT OF SCOPE — **aggregate cardinality is unbounded on this path, by
  design, with a named backstop.** Measured rather than asserted:
  - Per payload, the producer bounds the serialised `commands` array via
    `maxSlashCommandListBytes`, keeping the marshalled envelope under the 65519 B v2
    application-envelope cap (its own tests measure the worst case at 63224 B, 96.5% of cap).
    This path adds no second cap deliberately — a second place the limit is decided could
    disagree with the first silently.
  - In aggregate, the backstop is `pushQueueByteCeiling` (32 MiB). The queue is **fresh** at
    this call site (created a few statements earlier in the same `handleNoiseInit`), so all
    five reconciles together would have to produce more than `pushQueueCap` (256) control
    envelopes *and* more than 32 MiB from a cold start — roughly 512 worst-case retained
    lists. Conversation count is daemon-minted, so a remote peer cannot inflate it at will,
    and a workspace author can only grow the per-payload dimension, which is already capped.
    The failure mode is the deterministic, tested `StatusQueueOverflow` teardown (#1505 AC#5),
    not unbounded growth.
  - A cardinality cap, if one is ever wanted, belongs to #2007's producer — the position
    `OutstandingQuestions`' seam doc already takes for its own aggregate.
  - No socket read, no header parse, no timeout and no TLS config are in play: this path is
    outbound-only onto an already-established, already-authenticated session.
- **[Network & I/O]** No findings on delivery completeness. `pushQueue.enqueue` marks an
  envelope droppable **only** when its type is `protocol.TypeAssistantDelta`, so
  `slash_command_list` is control-class and the nominal-cap drop policy can never shed it.
  That makes the plan's "position in the tail is immaterial to correctness" claim structural
  rather than merely intended — being fifth of five cannot cost this frame its delivery.
- **[Errors/logs/telemetry]** No MUST FIX; the never-log rule is AC4 and the design states it
  at both branches. The marshal branch must not echo `err` because `encoding/json` quotes the
  input bytes and a command name would land in the record; the push branch may echo `err`
  only because `Push` returns `ctx.Err()` or `ErrConnNotFound` and nothing else — verified
  against `Push` directly, and named in the comment so a later change to its error values is
  visibly load-bearing. The success path logs nothing at all.
- **[Errors/logs/telemetry]** SHOULD FIX — **the content-free-logging test cannot reach either
  error branch, and must say so.** Both branches are unreachable by fixture (marshal by the
  closed-type argument; `ErrConnNotFound` by the queue being created a few statements earlier
  on the same goroutine), so on a green run the test asserts the absence of a leak the
  reconcile never had the opportunity to emit. The guarantee for those two branches therefore
  rests on code reading, not on the test. Phase B records this in the test file's header, so
  a later reader does not mistake the passing test for branch coverage.
- **[Concurrency]** No MUST FIX. No goroutine is spawned, so none can leak; no lock is added
  and no lock ordering is introduced — `pushMu` is taken and released inside `Push` and is
  never held across `json.Marshal` or across the seam call. There is no cross-goroutine
  check-then-use gap: the guard's reads and the loop's actions all run on the Run goroutine
  under the package's single-owner invariant. On shutdown, `ctx.Err() != nil` returns, and a
  partially-sent batch is safe precisely because the frame is snapshot-shaped — the next
  connect re-sends the whole set. The returned slice is the producer's backing array and this
  path marshals without ever writing through it.
- **[Concurrency]** SHOULD FIX — **the seam runs on the Run goroutine and must return in
  bounded time**, or it stalls the manager and every conn it services. `ModalResolver`'s doc
  states this obligation explicitly; the four enumerate-seams leave it implicit. It is not
  hypothetical here: #2007's producer will walk a conversation registry under that registry's
  mutex, exactly the shape that can block. Phase B states the obligation in
  `RetainedSlashCommandLists`' doc block, which is the doc the #2007 implementer reads.
- **[Threat model alignment]** No findings — `docs/protocol-mobile.md` § Security model's two
  relevant gates are inherited structurally rather than re-implemented. **Authentication:** the
  call site is `handleNoiseInit`'s success tail, after the device token's `Validate` and after
  `s.state = V2StateOpen`, so an unauthenticated or unpaired peer never reaches it — placing
  the call earlier in `handleNoiseInit` would be a MUST FIX, and last-in-the-tail is the safest
  position available. **Capability:** `!s.interactive` reads the negotiated flag recorded from
  the same slice the ack echoed, so a spoofed or unsupported advertisement can never flag the
  session. **Replay:** `EventID` nil keeps the frame out of the #647 turn-event ring, so this
  path adds no replayable material and no per-conversation ring memory. Per-device
  authorization of *which* conversations a device may see is the OUT OF SCOPE item under
  [Trust boundaries].

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02
