# #2078 — Connect-time background-task-roster reconcile (`RetainedBackgroundTaskRosters` seam + `reconcileBackgroundTaskRosters`)

`security-sensitive`. The sixth Mode B instance of the reconcile-on-connect
mechanism, after `reconcileModals` (#877), `reconcileQueues` (#878),
`reconcileModelLists` (#1863), `reconcileQuestions` (#1979) and
`reconcileSlashCommandLists` (#2006). Ships the seam wired to nothing, exactly as
#2006 did before #2007; the daemon-side producer is #2079.

## Files read

- `internal/relay/v2session_slashreconcile.go` → `reconcileSlashCommandLists` — the
  direct twin: guard, snapshot-then-early-return, one shared batch timestamp, nil
  `EventID`, the two-branch error posture this file inherits verbatim.
- `internal/relay/v2session_slashreconcile_test.go` → `sampleSlashCommandListPayload`,
  `reconciledSlashCommandLists`, and the five test functions — the table shape, the
  control-conn trick that makes a capability-gate zero non-vacuous, and the
  push-record-count assertion for the teardown branch.
- `internal/relay/v2session_seams.go` → `RetainedSlashCommandLists`,
  `RetainedModelLists`, `OutstandingQuestions` — the seam declaration idiom: closure
  over a `protocol` payload slice, enumerate-all, BOUNDED TIME, `nil ⇒ no reconcile`.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit` — the success tail that
  already calls the five reconciles, and the `s.interactive` / `m.queues[s.connID]`
  statements a few lines above it that make the push queue's existence structural.
- `internal/protocol/interactive.go` → `BackgroundTaskRosterPayload`, its
  `MarshalJSON`, and `BackgroundTask` — the three-field aggregate, the nil→`[]`
  normalisation this ticket's AC3 leans on, and the four untrusted row fields.
- `internal/protocol/codes.go` → `TypeBackgroundTaskRoster` — already on main.
- `internal/turnbridge/outbound.go` → the `TypeBackgroundTaskRoster` mapping — proof
  the live turn lane already carries the frame, so nothing new is minted on the wire.
- `docs/knowledge/features/v2-session-manager-state-machine-connect-time-slash-command-list-reconcile-retain.md`
  — the twin's overview. Three lessons carried into this design: `pushQueue.enqueue`
  marks only `TypeAssistantDelta` droppable (so last-in-tail is provably safe, not
  luckily so); a zero-envelope assertion cannot pin the teardown branch, only the
  count of `push_err` records can; and the twin's *three loss points* are a
  slash-command-specific claim that must not be copied unexamined.
- `docs/knowledge/features/protocol-package-background-task-event-payloads.md` — the
  family's own overview.

## Context

`docs/protocol-mobile.md` § Reconnect / Backfill defines two recovery modes and the
background-task family is served by neither. Mode A (cursor replay) needs the client
to advertise `hello.last_event_id`, which `pyrycode-desktop` does not; the spec's
stated consequence is that such a client receives no replay at all. Mode B does not
cover this frame. So a client opening an interactive session sees an empty
background-task panel until claude next changes the roster — which on a quiet session
may never happen.

The frame is snapshot-shaped full state ("A SNAPSHOT, not a delta", the payload's own
doc), which is the data class § Reconnect / Backfill binds to a connect-time snapshot
rather than a cursor backfill. That split is keyed on the KIND OF DATA, not on how
long the client was away, so the re-send is idempotent by construction and needs no
special-casing of the away duration.

**No ADR.** This adds a sixth instance of an established mechanism and decides nothing
the umbrella (#829) has not already decided.

**Scope guard.** `docs/protocol-mobile.md` is not touched. Its § `background_task_roster`
still says the frame carries an envelope-level `event_id`, and § Reconnect / Backfill's
Mode B list still names five reconciles. Both go stale as this family lands and both
are #2080's acceptance criteria; correcting them here would duplicate a sibling's
deliverable and publish a capability that is not live until #2079 wires the seam — the
drift #2010 had to repair for the slash-command family.

### Size: 1.35× the guideline, stated rather than split

Five of the six size-S boundaries hold: 3 production files (`v2session_seams.go`,
`v2session_handshake.go`, one new reconcile file), 0 new exported types, 0 consumer
call sites needing simultaneous update, 5 acceptance criteria, 2 reject branches.
Total written work is the one overage — ~950 lines against the 800 guideline.

The only cuts available separate the seam declaration from its sole consumer, or the
reconcile from its 13-line call site. The sizing floor forbids both: either child
would have exactly one consumer inside its own family and could not be verified alone.
Merging with #2079 is barred by the cross-package always-split rule. Floor beats
ceiling, so this builds as one ticket with the overage stated. The refiner reached the
same conclusion in the ticket's `Estimate:` line; this is an independent re-derivation
of it against the written plan, not a deferral to it.

## Design

### The seam

A sixth optional field on `V2SessionConfig`, declared beside the five it matches:

```go
RetainedBackgroundTaskRosters func() []protocol.BackgroundTaskRosterPayload
```

- **Enumerate-all, not conversation-keyed.** A `V2Session` carries no conversation id
  — it holds `connID`, `state`, `resp`, `send`, `recv`, `device`, `interactive` and
  `peerStatic` — so there is nothing to key on at connect time. Each payload
  self-identifies by its own `conversation_id`. `RetainedModelLists`,
  `OutstandingQuestions` and `RetainedSlashCommandLists` state the same reasoning.
- **No new import.** A closure over `[]protocol.BackgroundTaskRosterPayload`, not a
  `*sessions.Pool` or a `turnevent` value: `internal/relay` imports neither
  `internal/sessions` nor `internal/turnevent`, and `protocol` is already imported.
  Define the dependency where it is consumed (the Technical Notes' requirement).
- **Optional.** `nil ⇒ no reconcile`, byte-identical to the pre-#2078 / foreground /
  existing-test posture. This slice ships it nil in production; #2079 wires it.
- **Order is not part of the contract** — a caller correlates by `conversation_id`,
  never by position, for the same reason the envelope id this path stamps is fixed.
- **BOUNDED TIME**, like every seam the manager calls on its Run goroutine: an
  implementation that blocks stalls Run and with it every conn the manager services.
- **Already-bounded payloads only.** The reconcile applies no bound of its own — not
  on how many payloads are returned, not on any row's text. The bound is the
  producer's, decided at construction (`DroppedTasks` on the aggregate,
  `BackgroundTask.TruncatedFields` per row, over `internal/streamsup/parser.go`'s
  `maxTaskRosterEntries` / `maxTaskRosterDescription`). A second cap here would be a
  second place the limit is decided and the two could disagree silently. Those
  per-payload bounds do not bound the *cardinality* of the returned slice; on this
  path `pushQueueByteCeiling` over a queue created a few statements earlier in the
  same `handleNoiseInit` is the backstop, and a cardinality cap, if ever wanted,
  belongs to the producer.

### `reconcileBackgroundTaskRosters(ctx, s)`

New file `internal/relay/v2session_rosterreconcile.go`, one-per-reconcile like the
other five, for the readability reason the twin's file comment gives.

```go
func (m *V2SessionManager) reconcileBackgroundTaskRosters(ctx context.Context, s *V2Session)
```

Behaviour, in order:

1. **Guard.** `!s.interactive || m.cfg.RetainedBackgroundTaskRosters == nil` ⇒ return.
   Capability gate plus the unwired/foreground opt-out.
2. **Snapshot.** Call the seam once; `len(retained) == 0` ⇒ return. Nothing retained
   ⇒ nothing sent.
3. **One shared batch timestamp**, matching the five twins.
4. **Per payload:** `json.Marshal`, then `m.Push(ctx, s.connID, env)` with an
   `protocol.Envelope{ID: 1, Type: protocol.TypeBackgroundTaskRoster, TS: ts, Payload: payload}`
   and `EventID` left nil.

`EventID` nil is load-bearing twice, as in the five twins: it keeps the frame out of
the #647 turn-event replay ring, and it makes `forwardEnvelope`'s `last_event_id`
dedup inert for the frame. No turn is opened either — the frame goes straight onto
the conn's push queue via `Push`, never through `forwardEnvelope`, and
`TypeBackgroundTaskRoster` is not a turn-boundary type. It enqueues rather than seals,
which is load-bearing rather than incidental: `Push` only buffers, leaving `drainOnce`
to consult `Connected` before sealing, so no Noise send-nonce is burned for a frame
that cannot reach the phone (#874).

**The one place the twin's answer is wrong here.** The twin family's producers filter
an empty aggregate away (`streamsup.emitSlashCommandList` early-returns on a
zero-length list). This family must not: an empty roster is a POSITIVE statement that
nothing is alive, which is exactly the #1240 signal, and the payload's own
`MarshalJSON` exists to guarantee it serialises as `"tasks":[]` and never as `null`.
So the loop body carries **no** `len(p.Tasks) == 0 { continue }` guard, and the
absence is deliberate rather than an omission — a comment says so at the loop, and a
test row pins it (§ Testing strategy).

Run-goroutine only (called from `handleNoiseInit`'s success tail), so `s.interactive`
and `s.connID` are read lock-free under the package's single-owner invariant — that
guarantee is the caller's, not this function's. `m.Push` takes `pushMu` internally and
is the only lock on the path; it is never held across the marshal or across the seam
call. The path mutates nothing it read: it marshals each payload and never writes
through the producer's backing array.

### Call site

Last in `handleNoiseInit`'s success tail, after `reconcileSlashCommandLists`, ~13
lines of comment plus call. Ordering carries no correctness weight — the six carry
distinct payload types, none reads another's effect, and none of the six frames is
droppable (`pushQueue.enqueue` marks only `TypeAssistantDelta` so, which is what makes
"position is immaterial" provable rather than assumed). The tail is ordered by
time-sensitivity, permission prompt first, and a roster snapshot is not time-sensitive
in that sense.

## Concurrency model

No goroutine is spawned and no lock is added. The function runs synchronously on the
manager's Run goroutine inside `handleNoiseInit`. The only lock touched is `pushMu`,
taken and released inside `Push`. Shutdown: the batch stops at the first `ctx`
teardown (below), and the enqueued frames are drained or discarded by the existing
`drainOnce` / `closeWith` machinery — this path adds no new lifecycle to end.

## Error handling

Two branches, both inheriting the twin's contract rather than making a fresh choice:

- **Marshal failure** — defensive and unreachable by fixture.
  `BackgroundTaskRosterPayload` is a closed struct of `string` / `[]BackgroundTask` /
  `int`, `BackgroundTask` is three strings and a `[]string`, and the custom
  `MarshalJSON` delegates to `json.Marshal` over that closed alias, so no value can
  fail. Log `Warn` with `event`, `conn_id`, `conversation_id` and **never** `err` —
  `encoding/json` quotes its input bytes, so a task description would land in the
  record. Skip that payload, keep sending the rest.
- **Push failure** — `ctx` teardown stops the batch (the session is going away and the
  remaining payloads have nowhere to land); any other failure skips that one payload
  and continues. Log `Debug` with `event`, `conn_id`, `err`. Echoing `err` is safe
  only because `Push` returns `ctx.Err()` or `ErrConnNotFound` and nothing else — an
  inherited contract, named in the doc block so a later change to `Push`'s error
  values is visibly load-bearing. `ErrConnNotFound` is unreachable from this call
  site: the push queue was created a few statements earlier in the same
  `handleNoiseInit` on the same goroutine, which is why this is `Debug` and not `Warn`.

The success path logs nothing at all — a connect-time reconcile fires on every
handshake, the routine-read cadence `handleRequestSessionSettings` cites.

Event names: `v2.backgroundtaskroster.reconcile.marshal_err`,
`v2.backgroundtaskroster.reconcile.push_err`.

## Testing strategy

One new file, `internal/relay/v2session_rosterreconcile_test.go`, following the twin's
shape. Two package-local helpers: `sampleBackgroundTaskRosterPayload(convID, ids...)`
(every field distinct and non-zero, so a crossed or dropped mapping cannot survive)
and `reconciledBackgroundTaskRosters(t, rec, connID, recv)`, which decrypts each
`noise_msg` for the conn, fatals on any Type other than `TypeBackgroundTaskRoster`
(where "no turn opened" is asserted — a turn-boundary frame would appear here as
another type), errors on a non-nil `EventID` (AC4) and on a repeated
`conversation_id`, and returns the decoded payloads *and* their raw bytes keyed by
conversation id.

**The round-trip asymmetry the helper must not paper over.** A seam payload with
`Tasks: nil` marshals to `"tasks":[]` and decodes back to an empty, non-nil slice, so
`reflect.DeepEqual` against the seam's own value is false for that case. The delivery
comparison normalises the *want* side (nil → `[]BackgroundTask{}`) rather than the got
side, so the empty case is compared honestly instead of being excused.

Scenarios:

- **Delivery (AC1, AC3 first half)** — table: one roster; two conversations; a bounded
  roster with `DroppedTasks` non-zero and `TruncatedFields` set on one row. Compared
  by `DeepEqual` keyed on `conversation_id`, so the match is order-independent and
  covers the dropped-count and per-row truncation reporters a mapping that silently
  zeroed them would otherwise pass.
- **Empty roster is sent, not filtered (AC3 second half)** — the seam returns one
  empty-`Tasks` payload and one populated one. Assert both frames arrive (a
  `len(p.Tasks) == 0 ⇒ continue` mutant drops exactly one and the count catches it)
  and that the empty one's raw payload bytes contain `"tasks":[]` — the wire form is
  the AC, so it is asserted directly rather than through the decoder.
- **Unicast only to the opening conn (AC1)** — A open, then B opens; B receives one
  frame, A receives no second one. Also pins the seam as a pure read: the same closure
  is called once per open and must yield the same set both times.
- **No frame and no log record (AC2)** — three rows: nil seam, seam returning nothing,
  capability not negotiated. Each asserts zero `noise_msg` for the conn under test,
  zero log records carrying the reconcile's event prefix, and that the session is
  still enumerable-open. The capability row opens a second interactive control conn
  that *does* receive a frame — without it, A's zero is indistinguishable from an
  empty seam and the `!s.interactive` mutant survives green. Log capture is proved
  live by the handshake line before the zero is trusted.
- **Content-free logging (AC5)** — four distinct sentinels, one per untrusted row
  field (`TaskID`, `TaskType`, `Description`, a `TruncatedFields` entry), captured at
  `Debug`; none may appear in any record. Four rather than one because they are
  separate struct members and a branch echoing just one must not pass.
- **Context teardown stops the batch** (Technical Notes' error posture) — call the
  method directly with an already-cancelled context and a hand-built `V2Session`, and
  assert the *count* of `push_err` records is exactly 1 for 3 payloads. The natural
  assertion (envelope count) is vacuous here: zero envelopes hold whether the branch
  returns or continues. The twin's overview records this as sole-red-by-mutant and
  worth carrying forward; this is that carry-forward.

Coverage the file states rather than claims: on a green run neither error branch is
reachable (marshal cannot fail; `ErrConnNotFound` is structurally impossible from this
call site), so the logging test proves the *success* path logs nothing and no more.
The never-log guarantee for the two error branches rests on reading them.

Gate: `go test -race ./internal/relay/...`, `go vet ./...`, `go build ./cmd/pyry`.

## Open questions

1. **Does the `"tasks":[]` assertion need the raw bytes, or would a decoded
   `len(Tasks) == 0` do?** — Resolved in the design above: decoded, `null` and `[]`
   both yield a zero-length slice, so only the raw bytes distinguish them, and the
   distinction is the AC. Assert the bytes.
2. **Should `dropped_tasks` be logged on the marshal branch alongside
   `conversation_id`?** — Resolved: no. It is content-free, but the twin logs only
   `event` / `conn_id` / `conversation_id` and a sixth instance is not the place to
   widen a family's log surface. AC3 is a wire-format obligation, not a logging one.
3. **Does the seam need a `len(p.Tasks) > 0` filter for parity with #2007's
   producer?** — Resolved: no, and this is the family's one genuine divergence. Noted
   in the design and pinned by a test row.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The boundary is explicit and single: everything
  the seam returns is claude-authored untrusted text that crossed the subprocess trust
  boundary at `internal/streamsup`'s parser, and it stays untrusted through this path
  — marshalled and forwarded verbatim, never parsed, never interpolated, never
  logged. The one value that *does* reach a log record, `conversation_id`, is
  server-minted (`conversations.NewID()`) and never client-chosen, which is what makes
  it safe; that distinction is stated in the doc block rather than left to a reader.
  Downstream, `protocol.BackgroundTask`'s own doc block carries the
  render-never-execute obligation for `Description` (a literal command line for the
  `local_bash` task type), so the client-side holder is told what it is holding.
- **[Error messages, logs, telemetry]** MUST-NOT-log: all four `BackgroundTask` fields.
  The marshal branch is the concrete exposure — `encoding/json` quotes its input bytes,
  so echoing `err` would land a task description in a record, and a *list* of command
  lines is the more tempting shape of this family (`BackgroundTask`'s own doc grades it
  so). The design therefore forbids the echo explicitly and a test pins four separate
  sentinels rather than one. The push branch's `err` echo is admitted only under the
  named `Push`-returns-`ctx.Err()`-or-`ErrConnNotFound` contract. The success path logs
  nothing, so a per-handshake cadence cannot turn into a per-connect record of who has
  what running.
- **[Network & I/O]** No findings on input caps — this path reads nothing from the
  network; it is outbound-only, on a conn already authenticated. Resource exhaustion is
  the live question and it is answered in two different packages, neither visible from
  the other: the producer bounds one payload (`maxTaskRosterEntries`,
  `maxTaskRosterDescription`) so the marshalled envelope stays under the 65519 B v2
  application-envelope cap, and `pushQueueByteCeiling` (32 MiB) over a queue that is
  *fresh* at this call site is the relay-side backstop, whose trip is the deterministic
  `StatusQueueOverflow` teardown rather than unbounded growth. The reconcile adds no
  third cap on purpose: two places deciding one limit can disagree silently.
- **[Concurrency]** No findings. No goroutine is spawned, so none can leak. One lock is
  touched (`pushMu`, inside `Push`), never held across the marshal or the seam call, so
  no ordering is introduced and none can invert. `s.interactive` / `s.connID` are read
  lock-free, valid only because the caller is the Run goroutine — stated as the
  caller's guarantee, not assumed. The check-then-mutate hazard does not arise: this
  path mutates nothing, including the producer's backing arrays. The BOUNDED TIME
  obligation on the seam is the one real concurrency risk and it is not hypothetical —
  #2079's producer will walk a registry under a mutex — so it is declared on the seam
  where the implementer will read it.
- **[Threat model alignment]** Two structural gates, both inherited and both stated:
  authentication (this tail runs post-handshake and post-token-validation) and
  capability (`!s.interactive`). `docs/protocol-mobile.md` § Security model's replay
  concern does not bite — the frame is idempotent snapshot state and carries no
  `event_id`, so re-sending it neither replays a turn nor mutates daemon state.
  OUT OF SCOPE, identically to the five enumerate-all seams before it: **no per-device
  confinement** — a paired interactive conn is sent every retained roster, including
  conversations bound to workspaces other than the one that device works in. That is a
  property of the enumerate-all shape shared by the whole family and belongs to the
  Mode B umbrella (#829) if it is ever wanted, not to a sixth instance that would then
  disagree with the other five.
- **[Tokens, secrets, credentials]** Not applicable by design: this path handles no
  token, no key and no credential. It reads two session fields (`interactive`,
  `connID`) and a payload slice, and `connID` is a server-minted routing id that
  already crosses the wire in both directions.
- **[File operations]** Not applicable by design: no filesystem access on this path,
  and the seam's contract is a pure in-memory read.
- **[Subprocess / external command execution]** Not applicable by design: nothing here
  execs. Worth naming rather than skipping, because `Description` *is* a literal
  command line for the `local_bash` task type — the point is that this path never
  treats it as one, and the never-execute obligation is carried to the client in
  `BackgroundTask`'s doc block.
- **[Cryptographic primitives]** No findings. No randomness and no comparison is
  performed here. Sealing is `Push`/`drainOnce`'s existing Noise machinery, unchanged;
  the enqueue-rather-than-seal choice preserves the #874 nonce invariant, which is the
  one crypto property this path could have broken and does not.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-05
