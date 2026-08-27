# #1840 — Retain the decoded model list for the session

## Files to read first

Turn-1 data load. Resolve every symbol with `codegraph_search` / `codegraph_node`; nothing here is cited by line.

| File | Symbol | What to extract |
|---|---|---|
| `cmd/pyry/streamsup_runner.go` | `newStreamRunnerFactory` | **The one production `streamsup.NewParser` call site tree-wide.** This is the only line that changes in the event path. Note the ordering: parser first, then `streamsup.New`, then the adapter. |
| `cmd/pyry/streamsup_runner.go` | `streamRunner`, `Interrupt`, `RestartFresh`, `BeginRotation` | The value-typed adapter, and the three docs that state the **type-assertion vs interface-widening rule** this spec follows. Read all three doc comments — they carry the argument you must not re-litigate. |
| `cmd/pyry/stream_turn_drain.go` | `sinkFor`, `streamTurnSink`, `droppableCap` | The droppable-class send this design must sit **upstream** of. Read the whole `sinkFor` doc: it says which class drops and why, and it is why retention cannot live downstream. |
| `cmd/pyry/stream_turn_drain.go` | `turnMarkFor`, `startStreamTurnDrainV2`, `clearForSession` | `turnMarkFor`'s default arm answers `turnMarkNone` for `ModelList` (droppable). `clearForSession` is the repo's **nil-receiver no-op** precedent this spec reuses. |
| `internal/turnevent/event.go` | `ModelList`, `ModelOption` | Exact field set and, on `Models`, the "Never empty" producer contract that AC 3 turns on. `ModelOption` carries two `[]string` fields — both matter to the clone. |
| `internal/streamsup/parser.go` | `emitModelList`, `NewParser` | Where the event is minted (fresh `[]ModelOption` per emit, never retained by the parser) and the `NewParser(sink, logger) *Parser` signature the new helper returns. |
| `internal/streamsup/runner.go` | `Config.RequestInitializeOnSpawn`, `spawnAndWait` | #1839's per-spawn ask — the cardinality that makes "a second report" (AC 2) a **respawn**, not a per-turn event. |
| `internal/sessions/session.go` | `Session.Runner` | `sup` is assigned once at `Session` construction and never reassigned. That single fact is what makes runner-held retention session-lifetime retention. |
| `cmd/pyry/stream_turn_drain_test.go` | `streamsup.NewParser(sink.sinkFor(...), …)` in the drain test | The existing in-repo pattern for driving a real stream-json line through a real parser into a real `streamTurnSink`. Copy this shape for the wiring test; do not invent a new harness. |
| `cmd/pyry/streamsup_runner_test.go` | `TestStreamRunnerFactory_Construct` | The factory test to extend. It already asserts on the returned `streamRunner`'s fields. |
| `internal/streamsup/parser_test.go` | `modelListLineFixture` | **Shape authority** for the `control_response` initialize line. Unexported and in another package, so copy the JSON shape, not the helper. |
| `docs/knowledge/features/streamsup-package.md` | § "Decoding the `initialize` ack into `turnevent.ModelList` (#1811)" and § "Firing the ask at spawn time (#1839)" | The three bounded dimensions, and the paragraph stating `ModelList` is an Event **rather than parser-held session state**. Read it before you are tempted to put the retention in `Parser`. |
| `docs/knowledge/features/sessions-package.md` | § session lifecycle | Confirms idle eviction stops the child, not the `Session`. Relevant to "does the retained value survive an eviction". |

---

## Context

#1839 made every spawned child answer one `initialize` control request. `emitModelList` already decodes that reply's `models` array into a `turnevent.ModelList` — the menu **this subscription can actually run** — bounded in all three dimensions. Nothing keeps it: `turnbridge.MapEvent`'s `default` drops the variant, so the list exists for a microsecond and is gone.

This slice gives it a home. It is daemon-internal: no wire frame, no client-visible change. #1837 publishes the retained value.

Two constraints decide the whole design, and both come from outside this ticket:

1. **Downstream (#1837):** a client connecting after the list was obtained must get it without waiting for a turn. So the home must be readable outside a turn, by something the session's owner can reach.
2. **Upstream (`sinkFor`):** `turnMarkFor`'s default arm classifies `ModelList` as droppable, so under load the fan-in channel refuses it at `droppableCap` and it is gone for the life of the child — one `initialize` exchange per child produces exactly one of these and no later event replaces it. **Retention must sit upstream of that send.**

No ADR is warranted. The placement rule this design applies is already stated in three existing doc comments (`Interrupt`, `RestartFresh`, `BeginRotation`) and one memory-level lesson; this ticket is a fourth application of it, not a new decision.

---

## Design

### Where the retained copy lives

**On the per-session `streamRunner` adapter in `cmd/pyry`, written by a decorator wrapped around the parser's sink.**

The event path becomes:

```
claude stdout
  → streamsup.Parser.Write → consumeLine → emitModelList
    → sessionModelHold.Sink            ← RETAIN HERE (in-process, no channel)
      → streamTurnSink.sinkFor(id)     ← today's droppable send, unchanged
        → channel → drain → MapEvent → dropped (unchanged)
```

Reading:

```
#1837 publisher
  → conversation → Pool.Lookup → Session.Runner()  (sessions.Runner)
    → type assertion for `ModelList() (turnevent.ModelList, bool)`
      → sessionModelHold.ModelList()
```

Four properties, each load-bearing:

- **Upstream of the drop.** `Sink` stores before it calls the downstream sink. No channel, no capacity, no watermark between the parser and the retained copy. The `droppableCap` refusal can still discard the *event*; it cannot discard the *retention*. This is the design's answer to the ticket's "which side of the send" call, and it is stated as an ordering rule inside `Sink` rather than left to be inferred.
- **Session-lifetime, with no registry and no leak.** `Session.sup` is assigned once at construction and never reassigned, so the hold's lifetime is exactly the session's. There is no daemon-global session-keyed map to grow, and no removal hook to remember to call — the ticket's cadence note (one report per child, weeks apart) makes any eviction policy dead weight. Compare `streamTurnSink`, whose own doc says "no session-keyed registry"; this design does not add one to it.
- **Reachable outside a turn.** `Session.Runner()` is reachable from `cmd/pyry` today (`resolveBoundRunner` already does exactly this walk for interrupts), on any goroutine, with no turn in flight and no turn context.
- **`sessions.Runner` stays un-widened.** `ModelList()` is a concrete method on `streamRunner`, reached by type assertion — the fourth method on that list after `Interrupt`, `RestartFresh` and `BeginRotation`. The placement rule those three state is: the interface carries a method when its consumer sits **inside** `internal/sessions`, where a structural assertion would fail open. #1837's consumer sits in `cmd/pyry`, so it does not. Widening the interface would drag every fake runner under `internal/sessions` and `cmd/pyry` into the diff, which is most of a `size:s` on its own.

### Rejected alternatives

- **In `streamsup.Parser`.** `turnevent.ModelList`'s own doc, and `streamsup-package.md`'s #1811 section, argue the variant is an Event rather than parser-held session state. That argument formally settles emission, not retention — but the practical objections stand anyway: `Parser` documents a single-writer, no-lock invariant (see the `thinkingSinceEmit` field doc, which names itself as the first field where that invariant does real work), so a concurrent reader would be the first thing to break it; and the `Parser` is not reachable from the session's owner, so a reader would need a new registry to find it. Both costs land on the wrong side of the ledger.
- **In `streamTurnSink`, keyed by session id.** One production file instead of two, but it contradicts that type's stated "fan-IN to a single point, not shared fan-out — no session-keyed registry" design, and the map would outlive every session it keyed, growing for the daemon's whole life with nothing to prune it.
- **Downstream of the channel, in the drain.** Ruled out by constraint 2 above.
- **A new `streamsup.Config` callback (`OnModelList`).** Widens `streamsup` and routes a claude-authored value through a new cross-package seam to reach a value that never leaves `cmd/pyry`. The decorator achieves the same with no package boundary crossed.

### New code — `cmd/pyry/session_model_hold.go` (new file)

All unexported; `cmd/pyry` is `package main`, so this adds **no exported API**.

```go
// sessionModelHold is one session's retained model list plus the sink decorator
// that fills it.
type sessionModelHold struct {
    mu   sync.Mutex
    list turnevent.ModelList
    have bool
    next func(turnevent.Event)
}
```

| Symbol | Signature | Contract |
|---|---|---|
| `newSessionModelHold` | `(next func(turnevent.Event)) *sessionModelHold` | Mints a hold that forwards to `next`. `next` is the per-session sink from `streamTurnSink.sinkFor`; a nil `next` is tolerated and forwards nothing (test convenience only — production always supplies one). |
| `(*sessionModelHold).Sink` | `(ev turnevent.Event)` | **The decorator.** If `ev` is a `turnevent.ModelList`, store it (replacing any prior value) and set `have`. Then forward `ev` to `next` **unchanged, always, for every variant**. Used as a method value: `hold.Sink` is a `func(turnevent.Event)`. |
| `(*sessionModelHold).ModelList` | `() (turnevent.ModelList, bool)` | Returns a deep copy of the retained list and `true`, or the zero value and `false` when nothing has been reported. Nil-receiver-safe: returns `(turnevent.ModelList{}, false)`. |
| `cloneModelList` | `(turnevent.ModelList) turnevent.ModelList` | Deep copy: clone `Models`, and within each `ModelOption` clone `EffortLevels` and `TruncatedFields`. `DroppedModels` and the scalar strings copy by assignment. |
| `newSessionParser` | `(next func(turnevent.Event), logger *slog.Logger) (*streamsup.Parser, *sessionModelHold)` | Mints the hold and the parser bound to it, **in one call**, so the two halves cannot be wired to different holds. This is the only place `streamsup.NewParser` is called in production after this ticket. |

`newSessionParser` exists for a specific reason and not as gratuitous indirection: it is what makes the wiring testable end-to-end. A test can call it, write a real `control_response` line into the returned `*streamsup.Parser`, and read the returned hold — proving the exact production composition through the real decoder. Leaving the two lines inline in `newStreamRunnerFactory` would leave that join provable only by inspection.

### Changed code — `cmd/pyry/streamsup_runner.go`

Three edits, all local:

1. `streamRunner` gains one field, `models *sessionModelHold`. The adapter stays a value type (the field is a pointer), so `var _ sessions.Runner = streamRunner{}` is unaffected.
2. A new method:
   ```go
   func (a streamRunner) ModelList() (turnevent.ModelList, bool) { return a.models.ModelList() }
   ```
   Its doc must state: OFF the `sessions.Runner` interface, for the reason `Interrupt`'s doc gives, and that #1837 is the consumer that reaches it by assertion.
3. `newStreamRunnerFactory`'s body:
   ```go
   parser, held := newSessionParser(sink.sinkFor(cfg.SessionID), cfg.Logger)
   scfg.Stdout = parser
   ...
   return streamRunner{r: r, models: held}, nil
   ```
   `sink.exitFor(cfg.SessionID)` and every other line stay exactly as they are.

### What deliberately does NOT change

- **The `ModelList` event is still forwarded to `sinkFor` and still travels the channel.** It could be swallowed at the decorator — `MapEvent` provably discards it and `turnMarkFor` gives it no tracker effect — but swallowing it would change what the fan-in and the drain observe, which is a behaviour change this ticket has no reason to make. AC 4 is satisfied by the event path being byte-identical to today's.
- `turnbridge.MapEvent`'s `default`, `eventKind`'s `ModelList` arm, `turnMarkFor`, `internal/streamsup`, `internal/turnevent`, `internal/sessions` and every `*.md` under `docs/knowledge/` are untouched.
- **Do not correct the stale "until #1693" comments** in `internal/turnevent/event.go` or `cmd/pyry/interactive_turn_v2.go`. #1837 rewrites those lines; touching them here buys nothing and widens the diff.

---

## Concurrency model

No new goroutines. No goroutine lifecycle to manage. No `context.Context` enters this design.

- **Writer:** the `Sink` method value runs on `os/exec`'s stdout forwarder goroutine for the live child — the same goroutine `streamsup.NewParser`'s doc names ("called serially, in stream order"). Across a respawn a *new* forwarder goroutine writes to the *same* hold, and the two can briefly overlap during teardown. That alone requires the mutex; it is not defensive.
- **Reader:** #1837's publisher, on a relay-leg goroutine. Today: nobody, plus tests.
- **Lock discipline:** `sessionModelHold.mu` is a **leaf lock**. It is taken and released entirely within `Sink`'s store step and within `ModelList`; it is **never held across the call to `next`**. Stated as a rule in `Sink`'s doc, because holding it across a channel send would put a new edge into the daemon's lock order and could not be justified by anything this design needs. It participates in no ordering with `Pool.mu`, `Session.lcMu` or `capMu`.
- **`sync.Mutex`, not `RWMutex`.** Writes happen once per child spawn (weeks apart per the ticket's cadence note); reads happen on client connect. There is no contention to optimise away, and `RWMutex` would imply a concurrency profile this value does not have.
- **Aliasing.** `emitModelList` allocates `Models` fresh per emit and the parser retains no reference, so the hold takes sole ownership of what it is handed and stores it without copying. `ModelList()` returns a **deep copy**, so a reader may mutate freely without corrupting the retained value or another reader's copy — the same contract `Pool.List` states for its snapshot, and the reason it is worth the ten lines here is that this value lives for the session's whole life and will be read repeatedly by different consumers.
- **Shutdown.** The hold holds no resource, registers nothing and starts nothing, so there is nothing to drain or close. A process killed mid-write leaves no partial state anywhere: the value is in memory only and is re-obtained from the next child's `initialize` reply.

---

## Error handling

There is no error to return anywhere in this design and no new failure mode. That is the point of the shape, and the reasoning is worth stating rather than leaving as an absence:

| Situation | Behaviour |
|---|---|
| Child never answers `initialize` (the ask was best-effort per #1839, or the child died first) | `have` stays false; `ModelList()` returns `(zero, false)`. This is AC 3's unreported state, and it is the state every session is in until its first child answers. |
| The reply is a NAK, undecodable, or carries no `models` | `emitModelList` returns without emitting (its rungs 1–3), so `Sink` never sees a `ModelList`. Unchanged from today; the hold is not involved. |
| The event is refused by `sinkFor` at `droppableCap` | The retention already happened. The event is lost; the list is not. |
| A second child reports | The retained value is replaced wholesale (AC 2). No accumulation, no merge, no per-report list. |
| The session is idle-evicted and reactivated | The `Session` and its `sup` survive eviction, so the retained value survives it too. The reactivated child's `initialize` reply replaces it. |
| `ModelList()` called on a `streamRunner` whose `models` is nil | Nil-receiver no-op returning `(zero, false)`, mirroring `clearForSession`'s nil-receiver precedent in `stream_turn_drain.go`. |

**Nothing on this path logs, at any level.** `sessionModelHold` takes no `*slog.Logger` and has no field for one, so there is no path by which `Value`, `ResolvedModel`, `DisplayName` or an effort level can reach a log record. That is #833's posture — restated in `internal/relay`'s `v2session_settings.go` and `internal/sessions`' `pool.go` as "model / effort / YOLO values are NEVER logged at any level" — enforced by construction rather than by care. Do not add a "retained model list for session X" Debug line, not even a content-free one: the hold has no logger to write it with, and giving it one to write a diagnostic nobody asked for would reopen the exact channel the posture closes.

---

## Testing strategy

`go test -race ./cmd/pyry/...` under `make check`. Table-driven where the shape allows, `t.Parallel()`, stdlib only, same-package tests. Scenarios, not pre-written test bodies:

**`cmd/pyry/session_model_hold_test.go` (new)**

- *Retains and forwards.* Drive a constructed `turnevent.ModelList` through `Sink` with a recording `next`. Assert the hold returns it with `ok == true` **and** that `next` received the same event unchanged. Both halves in one test — forwarding is AC 4 and must not be provable only by its absence.
- *Unreported is its own state.* A fresh hold's `ModelList()` returns `ok == false`. Assert on the bool, never on `len(Models) == 0` — the producer never emits an empty list, so an empty-list assertion would pin a state production cannot reach. **Do not construct an empty-list report anywhere in this suite.**
- *Second report replaces.* Two reports with distinguishable contents; assert the second's contents and that the count is the second's, not the sum.
- *Non-`ModelList` events change nothing.* Table over a handful of other `turnevent.Event` variants (`TextChunk`, `TurnEnd`, `ModelAnnounced`): each is forwarded and none flips `have`. This is the arm a `switch`-happy implementation gets wrong.
- *Read returns a deep copy.* Read twice; mutate the first result's `Models[0].EffortLevels`, `Models[0].TruncatedFields` and `Models` slice; assert the second read is unaffected. Three mutations, because a clone that misses one inner slice passes a one-mutation test.
- *Retention survives a saturated downstream sink.* Build `newStreamTurnSink(1, discardLogger())`, fill its channel, wrap it with `newSessionModelHold(s.sinkFor("sess"))`, drive one `ModelList`. Assert the hold has the list and that `len(s.ch)` did not grow. **This is the test that pins the ordering the ticket calls the design's central call** — it fails if a developer ever moves retention below the send.
- *Nil-receiver read.* `(*sessionModelHold)(nil).ModelList()` returns `(zero, false)` and does not panic.
- *Race.* One goroutine calling `Sink` in a loop against one calling `ModelList`, under `-race`.

**`cmd/pyry/streamsup_runner_test.go` (extend)**

- *The production composition decodes and retains.* Call `newSessionParser` with a recording `next`, write a real `control_response` initialize line (shape per `modelListLineFixture`) plus `"\n"` into the returned `*streamsup.Parser`, and assert the hold now holds the decoded list with the entries the line named, and that `next` also saw it. This is the only test in the suite that touches JSON, and it is what proves the wiring rather than the hold.
- *The factory carries the hold.* Extend `TestStreamRunnerFactory_Construct` to assert `sr.models != nil`, and that `sr.ModelList()` reports `ok == false` on a freshly constructed runner (no child, nothing reported).
- *Compile-time placement.* Assert `streamRunner` satisfies `interface{ ModelList() (turnevent.ModelList, bool) }` — the shape #1837 will assert for — and do **not** add `ModelList` to `var _ sessions.Runner = streamRunner{}`'s obligations.

**Known limit, stated rather than papered over.** No test proves that `newStreamRunnerFactory` assigns the parser and the hold from the *same* `newSessionParser` call — the runner does not expose its `Stdout`. The mitigation is structural: `newSessionParser` returns both halves from one call, on two adjacent lines, and the factory has nowhere else to obtain a hold. Do not add a production accessor to close this; the residual is a code-review-visible adjacent-line mistake, not a runtime failure mode.

---

## Open questions

1. **Does #1837 read through `Session.Runner()` or want a `Pool`-level accessor?** This spec commits to the runner-held value and the `cmd/pyry` type assertion, which is what `resolveBoundRunner` already does for interrupts. If #1837 finds it needs a session-id-keyed lookup instead, the hold is unchanged and only the walk differs — no rework of this slice.
2. **Should `ModelList()` eventually report *when* the list was obtained?** A timestamp would let a client distinguish "reported by the child that is running now" from "reported by a child that has since been replaced". Not needed for #1837's acceptance and deliberately out of scope; the field is additive if a later ticket wants it.

---

## Scope check (§ 4 re-count, against this written spec)

| Limit | Boundary | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **2** — `cmd/pyry/session_model_hold.go` (new), `cmd/pyry/streamsup_runner.go` |
| Total written work | ≤ 400 | **~330** — ~130 new production + ~25 changed production + ~150 new test + ~25 changed test |
| New exported types or interfaces | ≤ 5 | **0** — `cmd/pyry` is `package main` |
| Consumer call sites needing simultaneous update | ≤ 10 | **1** — `newStreamRunnerFactory` is the only production `streamsup.NewParser` caller tree-wide (verified: one non-test hit) |
| Acceptance criteria | ≤ 5 | **4** |
| Distinct error/reject branches in a state machine | ≤ 10 | **0** — no state machine, no reject branches, no error return |

Nearest analogue: #1839 landed 303 insertions over 4 files (2 production, 2 test). This design is the same shape and the same size. Ships as `size:s`.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings.** The untrusted → trusted crossing is upstream of this ticket and unchanged: claude's stdout bytes become bounded Go values at `streamsup`'s `emitModelList`, which caps all three dimensions at construction. `sessionModelHold` receives an already-bounded `turnevent.ModelList` and never parses, re-decodes, concatenates or re-derives anything from it — it stores and returns the value it was handed. The boundary stays a single named function in a single package. The data does **not** become trusted by being retained, and the spec says so where it matters: `ModelOption.DisplayName`'s own doc records that the daemon bounds but does not sanitize these strings and that the render boundary owing sanitization is the client's. Retention changes neither half of that. A retained value is exactly as untrusted as the event was, and #1837 inherits the same obligation `MapEvent` would have had.
- **[Resource exhaustion / memory] No findings — and this is the category that most deserved the walk.** The retained footprint per session is bounded by the producer's own caps, not by anything this design promises: at most `maxModelListEntries` (10) entries, each with three strings capped at 256 bytes and at most `maxModelEffortLevelCount` (8) levels of `maxModelEffortLevel` (32) bytes, so worst case is on the order of 10 KB per session. The number of holds is bounded by the number of live `Session` objects, because a hold is a field of a `streamRunner` and dies with it — there is **no daemon-global map keyed by session id**, which is precisely the alternative this design rejected. Cardinality is one write per child spawn, so a hostile or looping child cannot inflate the count: each spawn *replaces*, never appends. This is the memory-knob question that #1821's per-frame-cap-is-also-an-eventring-multiplier lesson exists to force, and it is answered by construction rather than by a cap this ticket would have to invent.
- **[Error messages, logs, telemetry] No findings, enforced structurally.** `sessionModelHold` has no `*slog.Logger` field and its constructor takes none, so there is no path by which `Value`, `ResolvedModel`, `DisplayName` or an effort level can reach a log record — the #833 posture restated in `v2session_settings.go` and `pool.go`. This design adds no error value, so there is no error string that could embed claude's bytes either. The spec carries an explicit instruction not to add a diagnostic line here. The one log record adjacent to this path, `sinkFor`'s close-drop `Warn`, is unchanged and stays content-free (it names `eventKind`, which returns the variant name alone).
- **[Concurrency] No findings.** One mutex, a leaf, never held across the downstream call — so no new edge enters the daemon's lock order and no path can wedge `os/exec`'s stdout forwarder goroutine (which, if blocked, backs up claude's stdout pipe). The check-then-mutate hazard is absent because `Sink` does not check anything before storing: it stores unconditionally under the lock, so there is no TOCTOU window between "is this a ModelList" and "store it". No goroutine is spawned, so none can leak. The read path returns a deep copy, so a reader cannot race a later writer through a shared backing array — which is a data-race argument, not only a hygiene one, and is why the clone is specified as MUST rather than SHOULD.
- **[Subprocess / external command execution] Not applicable, by design decision rather than by luck.** Nothing retained here reaches `exec.Command`. `Value` is the argument you pass to select a model and therefore *could* reach a spawn argv — but it does not on this path: the spawn argv is built by `internal/sessions`' `claudeSettingsArgs` from the session's persisted settings, which this design neither reads nor writes. `internal/relay`'s `validModel` remains the gate on inbound model values, untouched. **A future ticket that feeds a retained `Value` into a spawn argv is introducing a new sink and owes its own review** — this one deliberately does not.
- **[File operations] Not applicable.** The retention is in-memory only. Nothing is persisted, no path is constructed, no file is created, and the daemon's session registry is not extended. A restart re-obtains the list from the next child's `initialize` reply, which is why persistence buys nothing and would add an on-disk copy of claude-authored text to defend.
- **[Tokens, secrets, credentials] Not applicable.** No credential, token or key is read, derived, stored or compared. The retained value is a menu of model names.
- **[Cryptographic primitives] Not applicable.** No randomness, no hashing, no comparison against a secret; nothing on this path is security-relevant in a way an RNG or a constant-time compare would bear on.
- **[Network & I/O] Not applicable to this slice, and named as such.** The retention adds, removes and reshapes no frame (AC 4), reads no socket, and changes no `http.Server` or TLS configuration. Every size cap that governs what is retained is upstream in `emitModelList`. **The moment this value is published, the wire-side questions become live** — envelope budget, per-connection cost of sending a menu on connect, and whether a client can trigger repeated sends. Those belong to **#1837** and its own review, not here.
- **[Threat model alignment] No new exposure.** `docs/protocol-mobile.md` § Security model governs what crosses to a paired client; this slice crosses nothing, so no threat in it is newly engaged. The one threat the design *touches* is "claude-authored text reaching a daemon log", and it is closed by the no-logger construction above.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-27
