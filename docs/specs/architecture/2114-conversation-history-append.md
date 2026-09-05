# #2114 — Append the interactive stream and session transitions to the conversation log

`internal/history` (#2112) has shipped with no production caller. This ticket is its
first: every envelope the interactive chokepoint fans out, and every session transition,
also lands in the conversation's durable log.

## Files read

- `internal/history/log.go` → `New`, `Store.Append`, `Store.Page`, `Store.resolveDir`,
  `Store.writeSegment`, `ErrInvalidID`, `ErrInvalidPayload` — the contract, the
  authorisation precondition this ticket must preserve, the two refusals, and the modes
  it writes at (`0600` files under `0700` dirs, `O_NOFOLLOW`, containment-checked).
- `internal/history/segment.go` → `encodeEntry`, `decodeSegment` — both state that no
  error names a line's content. That is what makes a failure log auditable; it is also
  why the errors that remain carry absolute *paths*, which the call site must not log.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2`, `emit`,
  `newInteractiveTurnEmitterV2` — the chokepoint. `emit` already hoists one `ts` and
  marshals once before the per-conn loop and hands both to `ring.Append`, so the log
  append sits beside that call and needs no new mapping. The type's SECURITY comment is
  the never-log-content posture the new line must keep.
- `cmd/pyry/session_transition_v2.go` → `sessionTransitionEmitterV2`, `broadcast`,
  `startSessionTransitionStreamV2`, `toWirePayload` — the producer the ring never sees.
  `broadcast` mints its timestamp *inside* the per-conn loop today; AC-3 forces the
  hoist. `startSessionTransitionStreamV2`'s `busy *turnBusyTracker` is the
  concrete-pointer, nil-tolerant precedent this ticket copies for the store.
- `cmd/pyry/relay.go` → `startRelayV2` — the consumer leg. `attachments.NewIntake`'s
  wiring there is the "exactly one per daemon over `resolveInstanceDirPath`" analogue;
  `mgr.SetReplaySource(emitter.ring, …)` on the line after `newInteractiveTurnEmitterV2`
  is the precedent for reaching an emitter field post-construction.
- `cmd/pyry/main.go` → `runSupervisor`'s `relayWiring` literal, `resolveInstanceDirPath`,
  `newInboundDeliver` — the true composition root. The `approvals := permbridge.New()`
  comment states the rule this ticket follows: mint the daemon-wide singleton at the root
  and thread it as a `relayWiring` field *once it has a reader*.
- `internal/eventring/ring.go` → `Ring.Append`, `Ring.After` — AC-1's comparison target.
  `After(conv, 0)` returns every retained event in ascending id order.
- `docs/knowledge/features/history-package.md` § Concurrency — hands AC-5 forward by
  name: `Append` holds a store-wide mutex across ~7 syscalls of directory resolution plus
  the open/write/close, and asks whichever of #2114/#2115 lands first to measure the hold
  at the chokepoint rather than discover it later.
- `docs/knowledge/features/eventring-package.md` — why ring ids restart at 1 per daemon
  start, i.e. why the ring is catch-up and not history.
- `CODING-STYLE.md`, `docs/knowledge/architecture/system-overview.md`,
  `docs/PROJECT-MEMORY.md`.

## Context

A client opening an existing conversation sees nothing that happened before it
connected. `internal/eventring` is bounded at 1024 entries per conversation, covers only
the conversation the daemon resolves as current, and is empty after a restart — it is
reconnect catch-up, not history. #2112 built the durable store; this ticket gives it its
producers.

The write point is the *envelope* chokepoint, not `internal/turnevent`: that variant set
carries no operator message, no conversation id, no session transition and no question
batch, so a log written from it would hold assistant text and tool rows and none of what
the operator typed. `emit` already holds the four values `Append` wants.

No ADR is warranted — this adds a caller to a decision #2112 already recorded.

## Design

### One seam, two producers — `cmd/pyry/conversation_history.go` (new)

Both producers need the identical five steps: nil-guard the store, convert the id,
`Append`, classify a failure, log it content-free. Duplicating that duplicates the log
discipline, which is the thing that must not drift. So one package-level function and one
classifier:

```go
func appendConversationHistory(store *history.Store, logger *slog.Logger,
    event, convID, typ string, payload json.RawMessage, ts time.Time)

func historyAppendFailure(err error) string
```

- `store == nil` returns immediately and logs nothing, so a daemon with no log — and
  every existing emitter test that builds an emitter without one — emits exactly as
  today. The store is a **concrete `*history.Store`, never an interface**: the trap
  `startSessionTransitionStreamV2` documents for `busy` is that a typed-nil inside an
  interface is non-nil at the interface level and routes straight past the guard.
  `Store` is not nil-receiver-safe (`Append` locks immediately), so the guard is an
  explicit check at this one call site rather than a nil-receiver method.
- `convID` crosses as a `string` and is converted here — a conversion, not a lookup.
- The return value is discarded: the durable id surfaces as `Entry.ID` in a served page
  (#2116), and the turn payloads already carry `turn_id` and `seq`, which storing the
  payload bytes verbatim retains.
- `historyAppendFailure` maps an error to one of three content-free discriminants —
  `invalid_id`, `invalid_payload` (both by `errors.Is` against the exported sentinels)
  and `write` for everything else. See **Error handling** for why the error's own text is
  never logged.

### Producer 1 — `emit` in `cmd/pyry/interactive_turn_v2.go`

One field on `interactiveTurnEmitterV2` (`hist *history.Store`, nil ⇒ no durable log) and
one call in `emit`, immediately after `eventID := e.ring.Append(…)` and before the
`ActiveConns` loop — same `convID`, same `typ`, same `payloadJSON`, same hoisted `ts` the
ring and every conn's envelope carry. Appending before the fan-out satisfies AC-2: a
conversation with zero interactive conns still accumulates history, for the same reason
the ring already does.

`newInteractiveTurnEmitterV2` is **not** widened. It has 86 call sites across 8 test
files; a positional parameter is inseparable from its call sites in Go and 86 is over
eight times the size table's ceiling. The field is assigned post-construction at the one
production site — the shape `mgr.SetReplaySource(emitter.ring, …)` already uses one line
later.

### Producer 2 — `broadcast` in `cmd/pyry/session_transition_v2.go`

The same `hist *history.Store` field, plus:

1. **Hoist the timestamp.** `broadcast` mints `time.Now()` inside the per-conn loop, so N
   conns produce N timestamps for one logical transition. Hoist to a single
   `ts := time.Now().UTC()` above the loop, shared by the log entry and every envelope —
   AC-3's substance. `.UTC()` comes with the hoist so both producers stamp the log
   identically: `emit` already normalises, and a log whose entries mix zones is not
   orderable by the field it stores. `time.Time.Equal` makes this invisible to the
   existing wire assertions.
2. **Append once**, after the marshal and after `payload.ConversationID` is resolved,
   before the loop. Both existing drops (unknown reason, unresolvable conversation)
   return *before* this point, so the log records what was fanned out and never what was
   refused.

The wire type is `protocol.TypeSessionTransition`, the constant the envelope carries, so
a served page replays the transition under the type the client already parses.

### Wiring — `cmd/pyry/main.go` and `cmd/pyry/relay.go`

`history.New` takes the instance directory as an argument, mirroring
`attachments.EnsureDir`, so the store is built at the composition root and injected.

- **`main.go`**: mint `conversationHistory := history.New(resolveInstanceDirPath(*name))`
  beside `approvals` and thread it as a new `relayWiring` field `hist`. The literal has
  exactly one construction site.
- **`relay.go`**: `startRelayV2` reads `w.hist` **outside** the `w.streamSink != nil`
  branch — the interactive emitter is built inside that branch while the
  session-transition stream starts unconditionally, and both producers need it. It is
  assigned to `emitter.hist` inside the branch and passed to
  `startSessionTransitionStreamV2` unconditionally.

The ticket names `startRelayV2` as the place to call `history.New`; this plan mints it one
frame higher, for the reason the ticket's *other* instruction gives — **exactly one
`Store` per daemon**, reachable by #2115's producer. That producer is `newInboundDeliver`,
built in `main.go` *before* `startRelay` is called, so a store minted inside
`startRelayV2` is unreachable from it and #2115 would have to move it or mint a second one
over the same instance directory. Two stores is the documented hazard: `Store` caches each
conversation's next id after recovering it from disk once, so two mint duplicate ids, and
each undoes a failed write by truncating to a size it stat'd itself, which can drop an
entry the other just appended. Minting at the root is also the codebase's own rule for a
daemon-wide singleton, stated verbatim in the `approvals := permbridge.New()` comment,
which withholds the `relayWiring` field only until a reader exists — this ticket is that
reader. Everything else the ticket prescribes is unchanged: outside the stream branch,
injected, nil-tolerant, concrete.

`startSessionTransitionStreamV2` takes the store as a new parameter rather than a
functional option: 4 call sites (1 production, 3 test), an order of magnitude under the
ceiling of 10, and a plain parameter reads better than an option type introduced for a
single option. The ticket's prohibition is on the two *emitter constructors* —
`newInteractiveTurnEmitterV2` (86 sites) and `newSessionTransitionEmitterV2` (10) —
neither of which is touched.

## Concurrency model

No goroutine is added or changed. Both call sites are already serial: `emit` runs only on
the emitter's single `Handle`/`flush` goroutine, `broadcast` only on its `Run` goroutine.
`Store` is self-synchronised behind one store-wide mutex exactly as `Ring` is behind its
own, so the emitters keep their unguarded single-goroutine fields and the store becomes
the second field (after `ring`) a future cross-goroutine reader may touch. The new lock is
a leaf: `Append` takes `s.mu` and beneath it only filesystem calls — no channel op, no
second lock, no callback — and neither emitter holds a lock when it calls in, so no
ordering can be established and none needs documenting.

The real consequence is **serialisation between the two producers**, and from #2115 onward
between them and the delivery path: one shared mutex now sits in front of a fan-out that
took none, so `broadcast` can block behind an `emit` already inside `Append`. Transitions
are rare and human-paced, and both goroutines are private to their own producer — neither
is the pool's lifecycle goroutine, which `Enqueue`'s non-blocking buffered send still
shields, so #659's MUST-NOT-BLOCK contract is unaffected and queue-full stays the only
backpressure the pool sees. AC-5 measures what the hold costs.

Shutdown is unchanged: the store spawns nothing, holds no descriptor between calls and has
no `Close`. A daemon killed mid-`Append` leaves at most a torn trailing line, which
`decodeSegment` drops by design.

## Error handling

`Append` refuses with `ErrInvalidID` (id not of canonical shape) or `ErrInvalidPayload`
(payload not valid JSON, or an encoded line over the 1 MiB `MaxSegmentBytes` bound), and
otherwise returns a filesystem error from directory resolution or the segment write. All
three are handled identically and none is assumed unreachable:

- **The wire emit and the ring append are never suppressed.** The append is a statement
  with no branch after it: `emit` still marshals, still appends to the ring, still pushes
  to every interactive conn; `broadcast` still fans out. A daemon that cannot write
  history still works.
- **Logged at `Warn`,** not `Debug`. The neighbouring `Debug` drops (`push_err`,
  `marshal_err`) lose one frame to one conn; this loses an event from the durable record
  permanently — the class `session_transition.queue_full` already logs at `Warn`. None of
  the three reasons is reachable in a healthy daemon: ids come from the registry and are
  canonical, payloads are closed structs, and the write path is a local file under the
  instance directory.
- **The fields are `event`, `conversation_id` and `reason` — and not `err`.** A
  deliberate departure from the sibling `push_err` line. `history`'s errors are
  content-free by construction (`encodeEntry` and `decodeSegment` both say so) but they
  *do* format absolute filesystem paths — `open segment %q`, `resolve log directory %q`.
  An `errors.Is`-derived discriminant carries the identity without the path and stays
  immune if a future `history` change puts something new in a message.

## Testing strategy

New tests in `cmd/pyry`, all against a real `history.New(t.TempDir())` — the store does no
I/O until `Append`, so a temp dir is the whole fixture.

**`conversation_history_test.go`**

- Nil store: `appendConversationHistory(nil, …)` returns without panicking and logs
  nothing (captured `slog` buffer).
- `historyAppendFailure` table: the two sentinels wrapped in `fmt.Errorf` map to
  `invalid_id` / `invalid_payload`; `os.ErrPermission` and a bare `errors.New` map to
  `write`.
- The failure log carries no payload bytes: drive a failing append with a payload holding
  a recognisable literal; assert the captured log contains neither it nor the temp
  directory path.

**`interactive_turn_v2_history_test.go`**

- **AC-1** — drive a scripted event sequence through `Handle` under `testConvID`
  (canonical; a non-canonical id fails `Append` with `ErrInvalidID` and reads back empty,
  indistinguishable from a producer never wired), then compare `ring.After(testConvID, 0)`
  against `store.Page(testConvID, "", n)` pairwise: same length, same `Type`,
  byte-identical `Payload`, `TS.Equal`. `Page` is newest-first and `After` oldest-first,
  so one is reversed. Traffic stays well inside `eventring.MaxEventsPerConversation` so
  the ring is a complete reference.
- **AC-2** — the same drive with `fakeInteractiveBcast` returning zero conns: no pushes
  recorded, log non-empty and equal to the ring.
- **AC-4** — cursor set to a non-canonical id (`conv-x`) with a store wired: every
  expected `Push` still lands, `ring.After` still holds the events, the log for that id is
  unreadable, and the captured log carries `reason=invalid_id` with the conversation id
  and no payload bytes.

**`session_transition_v2_history_test.go`**

- **AC-3** — one transition, three interactive conns, resolver answering `testConvID`: the
  log holds exactly one entry, of type `protocol.TypeSessionTransition`, whose `TS` equals
  the `TS` on all three envelopes and whose payload is byte-identical to theirs. This is
  the assertion the hoist exists for — without it the three envelopes carry three
  timestamps and the shared-`TS` clause fails. Also assert the payload decodes with
  `ConversationID == testConvID`.
- Dropped transitions write nothing: an unknown reason and an unresolvable conversation
  each leave the log empty.

**AC-5 measurement** — `BenchmarkInteractiveEmitHistoryAppend` with sub-benchmarks `store`
and `no_store`, both calling `emit` directly with zero conns so the number is the append's
own cost at the chokepoint, not the fan-out's. The delta is recorded under
**Measurement**. The decision rule, fixed here before the number is known so it cannot be
fitted to the result: assistant text deltas are already batched behind `coalesceWindow`
(250 ms), so the sustained rate is bounded by the *unbatched* variants — turn_state, tool
start/update, turn_end — which are human- and tool-paced, tens per turn. Synchronous stays
justified while the added cost is small against that window; a delta approaching
single-digit milliseconds means the shared mutex is a real stall on the emit goroutine and
the append must move off it. Anything between is recorded for #2115 to re-measure once a
second producer shares the lock.

**Existing tests** stay green untouched — the additive seam's whole point: every emitter
they construct has a nil `hist`. The three `startSessionTransitionStreamV2` call sites gain
a literal `nil`.

**Gate** — `go test -race ./cmd/pyry/... ./internal/history/...`, `go vet ./...`,
`go build ./cmd/pyry`.

## Open questions

1. **Does the AC-5 number justify keeping the append synchronous?** Resolved by
   measurement in Phase B against the rule fixed above; recorded under **Measurement**.
2. **Does hoisting `broadcast`'s timestamp to UTC disturb an existing assertion?** Expected
   not — the wire assertions compare with `time.Time.Equal`, which is location-independent
   — but the transition suite is the check. Resolved by running it.
3. **Should the durable `Entry.ID` reach the wire?** No, deliberately: the client's dedup
   key is named in #2116's spec and the turn payloads already carry `turn_id` and `seq`.
   Recorded so a later reader does not mistake the discarded return value for an oversight.

## Measurement (AC-5)

`BenchmarkInteractiveEmitHistoryAppend`, `-benchtime 3000x -count 3`, darwin/arm64
(Apple M4, APFS on local SSD), medians of three:

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| `no_store` (nil store, today's behaviour) | 1 688 | 482 | 3 |
| `store` (wired) | 51 377 | 16 256 | 144 |
| **delta the append adds** | **≈ 49.7 µs** | ≈ 15.8 KB | +141 |

**The append stays synchronous.** Against the rule fixed above — small against the
250 ms `coalesceWindow`, with single-digit milliseconds as the line — 49.7 µs is two
orders of magnitude below it, about 0.02% of the window. The chokepoint's sustained rate
is set by the *unbatched* variants (turn_state, tool start/update, turn_end), tens per
turn, so a turn pays single-digit milliseconds of added serialised time in total. Moving
the append off the emit goroutine would buy that back at the cost of an unbounded queue,
a second goroutine to drain it and a shutdown path for both — not a trade this number
justifies.

The cost is dominated by what `history-package.md` § Concurrency named in advance: the
~7 syscalls of directory re-resolution (`Abs`, `MkdirAll`, `EvalSymlinks` twice, the
`Lstat` walk) that `Append` performs under the store-wide mutex, which is also what the
144 allocations are. Two caveats for whoever re-measures: this is a local SSD, and a
slower or networked filesystem scales the whole delta; and this is one producer holding
the lock alone. #2115 adds a second on the delivery path, so the number to watch there
is contention, not the single-call cost — re-run this benchmark with both wired.

## Revisions

**2026-09-05 — Open questions resolved.** No design change; recorded so the questions are
answered rather than dropped.

1. **AC-5 / synchronous append** — measured, recorded above, decision is *keep it
   synchronous* against the rule fixed before the number was known.
2. **UTC hoist in `broadcast`** — no existing assertion disturbed. The full `cmd/pyry`
   suite passes under `-race` with the timestamp hoisted and normalised to UTC, as the
   plan predicted (`time.Time.Equal` is location-independent).
3. **Durable `Entry.ID` on the wire** — unchanged: deliberately not published here.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The boundary that matters is `Append`'s stated
  precondition: `convID` MUST be the conversation the authenticated session is already on,
  never one a client asserted — `conversations.ValidID` is a *shape* predicate, and a
  client-supplied id of canonical shape resolves genuinely and defeats every check beneath
  it. Neither producer is changed to take an id off the wire. `emit` reads
  `e.sup.CurrentConversation()`, the daemon's follow-active cursor — the *same* cursor
  `attachments.NewIntake`'s resolver adapts under the identical precondition on
  `attachments.ResolvePath`, so this ticket inherits an established boundary rather than
  opening one. `broadcast` resolves the id from the registry session binding via
  `resolveConv` and drops the whole event when it cannot, rather than emitting a guessed
  or empty key. Adversarially: the strongest attack is a client steering the
  active-conversation cursor to a conversation it should not reach — but that would
  mis-file attachments first, and it is a defect in that cursor's own authorisation, not
  one this ticket introduces or widens.
- **[Trust boundaries — payload provenance]** No finding. The bytes appended are the
  daemon's own `json.Marshal` of a closed protocol struct, taken *after* the marshal, so
  nothing client-authored is written verbatim except values already validated onto those
  structs. The log stores what the wire carried — the property AC-1 asserts.
- **[Tokens, secrets, credentials]** Not applicable, named rather than skipped: this ticket
  mints, stores, compares and logs no credential. The one identifier it handles, the
  conversation id, is a routing key the neighbouring producers already log.
- **[File operations]** No MUST FIX. Every filesystem operation belongs to
  `internal/history` and was audited under #2112: `resolveDir` canonicalises with `Abs` +
  `EvalSymlinks` and refuses a directory resolving outside its destination
  (`ErrNotContained`), splitting the path at the longest existing ancestor so the check
  runs *before* anything is created beneath an offending symlink; `writeSegment` opens with
  `syscall.O_NOFOLLOW` at `0600` under directories created `0700`, and undoes a partial
  write through the descriptor it already holds rather than by re-resolving the name. This
  ticket constructs no path of its own. **The one new fact it introduces is that
  conversation content now reaches disk at all** — `0600` under `~/.pyry/<instance>/` is
  the posture the attachment bytes beside it already have, so at-rest exposure is unchanged
  in kind. TOCTOU: the `Lstat` walk in `resolveDir` is a check-then-use mitigated by the
  `O_NOFOLLOW` open of the leaf; #2112's decision, not weakened here.
- **[Subprocess execution]** Not applicable — no `exec.Command`, no shell, no environment
  change anywhere in this diff.
- **[Cryptographic primitives]** Not applicable — no randomness, no comparison against a
  secret, no key material. The durable id is a per-conversation counter recovered from
  disk, not a token, and does not reach the wire here.
- **[Network & I/O]** No MUST FIX. Nothing is read from a socket. The one size question is
  the *write* bound and it is already enforced: `Append` refuses an encoded line over
  `MaxSegmentBytes` (1 MiB) with `ErrInvalidPayload`, checking the payload's own length
  before the filesystem is touched. The unbounded quantity is the log's *total* size — an
  attacker-driven conversation grows it without limit, since #2112 left retention open by
  design (`listSegments` tolerates gaps precisely so a future policy can delete).
  **OUT OF SCOPE** here and not silently: disk-usage retention is #2112's documented
  deferral, not this ticket's to invent, and this ticket is what makes it reachable.
  Flagged for whoever owns retention.
- **[Error messages, logs, telemetry]** No MUST FIX — the category that changed the design.
  MUST-NOT-log for both producers is payload bytes; both already hold that line and the new
  lines keep it. Beyond that, `history`'s errors format absolute filesystem paths (`open
  segment %q`, `resolve log directory %q`), so the failure log deliberately does **not**
  carry `err`: it carries an `errors.Is`-derived discriminant (`invalid_id` /
  `invalid_payload` / `write`) alongside `event` and `conversation_id`. Stricter than the
  sibling `push_err` line, and what keeps a future `history` message change from leaking
  through a call site nobody re-reads. Asserted by a test, not merely intended. No
  telemetry is emitted.
- **[Concurrency]** No MUST FIX. One lock enters each call site's dynamic extent and it is
  a leaf: `Append` takes the store mutex and beneath it only filesystem calls — no channel
  op, no second lock, no callback into caller code — so no lock *ordering* exists to
  violate, and neither caller holds a lock when it calls in. No goroutine is spawned, so no
  leak is possible. The risk is availability, not safety: the shared mutex serialises the
  two producers (and #2115's delivery path later), so a slow filesystem can stall the emit
  goroutine and, behind it, the transition fan-out. It cannot reach the pool's lifecycle
  goroutine, which `Enqueue`'s drop-on-full send isolates. AC-5 exists to put a number on
  exactly this, and its decision rule is fixed in **Testing strategy** before the number is
  known.
- **[Threat model alignment]** No MUST FIX. `docs/protocol-mobile.md` § Security model is
  unengaged: nothing crosses the relay here — no new wire type, no new field, no new
  handler, and the durable id deliberately stays off the wire. The relevant threat is
  local-at-rest disclosure of conversation content, addressed by the `0600`/`0700` posture
  above and unchanged in kind from the attachment bytes under the same root.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-05
