# #1133 — Rotate the stream-json turn-event sink tag on `RestartFresh`

## Files read

- `internal/streamsup/runner.go` → `Config` (the `OnChildExit` field's doc block is the contract this ticket's new field mirrors), `RestartFresh` (the sole writer of `r.sessionID`; `restartMu` is a documented leaf), `beginSpawn` (states the leaf rule) — the rotation site and the constraint on where a callback may fire.
- `internal/streamsup/parser.go` → `NewParser`, `Parser.PostureGate`, `Parser.postureGate` — the construction-ordering precedent (#2064): a live handle minted ahead of both halves rather than threaded through a widened signature.
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory` — where both lanes bind, and where the tag must be minted (before `newSessionParser`, which is before `streamsup.New`).
- `cmd/pyry/stream_turn_drain.go` → `streamTurnSink.sinkFor`, `streamTurnSink.exitFor` (the two frozen-tag closures), `streamTurnEnvelope`, `startStreamTurnDrainV2` (the gate AC 2 pins unchanged) — the class-aware drop policy must survive the refactor byte-for-byte.
- `cmd/pyry/relay.go` → `boundSessionIDForActive` — the live handle the gate compares against; returns `ok == false` for an empty `CurrentSessionID`, which is why an empty tag can never match.
- `cmd/pyry/stream_turn_busy.go` → `turnBusyTracker` doc — carries the unreachable-rotation-edge paragraph whose premise this ticket retires.
- `internal/e2e/relay_v2_stream_new_session_test.go` → `TestRelayV2_StreamNewSessionRotatesAndRestartsFresh` — the header's divergence paragraph, M4's stdin milestone, and the AC-1 Debug-level instrument guard whose determinism argument rests on the drop this ticket removes.
- `cmd/pyry/session_model_hold.go` → `newSessionParser` — the sink argument this ticket re-points; the four retention decorators sit between it and the parser and are untouched.
- `docs/knowledge/features/streamsup-package.md` § "Known gap (tracked, fail-closed): frozen sink tag vs. `new_session` rotation" — records the gap, names #1133 as the follow-up, and records that #1137's e2e confirmed it live. The documentation phase retires that section; this ticket does not edit it.

## Context

On the stream-json interactive path the turn-event sink tags every envelope with the runner's **construction-time** `cfg.SessionID`. A stream-mode `new_session` (`startFreshRunner` → `Pool.RotateForNewSession` → `RestartFresh(newID)`) rebinds the conversation to `newID` but leaves the tag frozen, so `startStreamTurnDrainV2`'s active-session gate drops every subsequent event for that conversation until the daemon restarts. Fail-closed — nothing is misdelivered — but the turn stream goes dark.

The fix is a **live tag** the runner rotates, not a widened signature: `.sinkFor(` has 17 occurrences and `.exitFor(` 10, so changing either signature is ~25 simultaneous consumer updates against a call-site ceiling of 10.

No ADR is warranted: this closes a gap the existing design already names, and introduces no new architectural seam beyond one optional `Config` callback that follows `OnChildExit`'s established shape.

**Size, stated rather than elided.** Total written work lands ~800–850 lines including this spec, ~50 over the size-S line ceiling. The only seam — the `streamsup` rotation notification split from its `cmd/pyry` consumer — has exactly one consumer and is barred by the one-consumer floor, and splitting the e2e from the fix would break the fails-on-main / passes-after proof. Floor beats ceiling; built as one ticket. Every other boundary holds: 4 production files, 0 new exported types, 0 consumer call sites needing simultaneous update, 4 acceptance criteria.

## Design

Three parts, in construction order.

### 1. `streamSessionTag` — the live handle (`cmd/pyry/stream_turn_drain.go`)

```go
type streamSessionTag struct{ id atomic.Pointer[string] }

func newStreamSessionTag(sessionID string) *streamSessionTag
func (t *streamSessionTag) ID() string           // the current tag; never ""
func (t *streamSessionTag) Rotate(newID string)  // no-op for ""
```

Backed by `atomic.Pointer[string]`, **not** a mutex. That is load-bearing twice: the per-event read is a single atomic load on claude's stdout forwarder goroutine, and `Rotate` is reachable from the runner, so a mutex here would introduce a lock the runner's teardown path could stall on. With an atomic there is no lock-order edge to reason about at all.

`Rotate("")` is refused **at the tag**, which is where the invariant lives ("the tag can never become empty"), even though `RestartFresh`'s own empty-id refusal means production never reaches it. Both checks are deterministic code, and the type owning the invariant should enforce it.

It lives beside the sink rather than in a file of its own: it exists only to be read by `sinkForTag`/`exitForTag`, and the two are read as a pair.

### 2. `sinkForTag` / `exitForTag` — additive handles (`cmd/pyry/stream_turn_drain.go`)

```go
func (s *streamTurnSink) sinkForTag(tag func() string) func(turnevent.Event)
func (s *streamTurnSink) exitForTag(tag func() string) func()
```

These carry the existing bodies verbatim — the class-aware watermark, the reserve, the Warn/Debug split, the field sets — with `sessionID` replaced by `tag()`, read **once per event** at the top of the closure so a rotation mid-closure cannot tag the envelope with one id and the drop record with another.

`sinkFor(sessionID string)` and `exitFor(sessionID string)` keep their signatures and become one-line delegates over a constant closure. All 27 existing call sites are untouched; there is exactly one implementation of the drop policy.

They take `func() string`, not `*streamSessionTag`: the sink then knows nothing about the tag type, and `sinkFor`'s delegation is expressible without a fake tag object.

### 3. `Config.OnSessionRotate` — the rotation notification (`internal/streamsup/runner.go`)

```go
// OnSessionRotate is called when RestartFresh accepts a rotation …
OnSessionRotate func(newID string)
```

Optional, nil-checked at the fire site, mirroring `OnChildExit`. Contract, stated on the field:

- Fires **once per accepted rotation**, never for the id `RestartFresh` refuses — the empty-id early return sits above it.
- Runs **synchronously on the caller's goroutine** (the dispatch goroutine running `startFreshRunner`) with **no Runner lock held**.
- Must not block: it sits ahead of the teardown hint and the iteration cancel, so a slow callback delays the respawn.
- Must not panic: no `recover`, matching `OnChildExit` and `onSpawn`.

**Fire site: immediately after the `restartMu` section, before the `restartCh` hint and `iterCancel`.** Both halves of that placement matter.

*Below the unlock* because `restartMu` is a leaf — nothing under it may take another lock or do synchronous I/O — and the callback is an arbitrary consumer-supplied function. The atomic-backed tag would not violate the letter of the rule, but firing a `Config` callback under a leaf mutex makes the rule depend on what the consumer happens to install.

*Above the hint and cancel* because the alternative is the bug this ticket fixes, reintroduced. Cancelling first lets the Run loop tear the child down and spawn the successor concurrently; the successor's first events would then be tagged with the **stale** id and dropped. Rotating first makes "the fresh child's events are never tagged stale" structural rather than a race the respawn latency happens to win.

The cost of that ordering is Window B in § Security review, and it is accepted there.

### 4. Wiring (`cmd/pyry/streamsup_runner.go`)

Inside `newStreamRunnerFactory`'s per-session closure, the tag is minted first — ahead of `newSessionParser`, which is ahead of `streamsup.New`:

```go
tag := newStreamSessionTag(cfg.SessionID)
parser, held := newSessionParser(sink.sinkForTag(tag.ID), cfg.Logger)
scfg.Stdout = parser
scfg.OnChildExit = sink.exitForTag(tag.ID)
scfg.OnSessionRotate = tag.Rotate
```

One tag, three bindings, adjacent lines — the same argument the existing doc makes for `sinkFor`/`exitFor` sitting adjacent, now with a third participant: identical tags on both lanes are what let the drain's exit arm clear exactly the conversation whose events it is ordered behind, and a rotation that moved one lane and not the other would break that silently.

`sessions.Runner` is **not** widened, `RestartFresh`'s signature is unchanged, and `startStreamTurnDrainV2` is not edited at all (AC 2).

## Concurrency model

No new goroutines.

| Actor | Goroutine | Operation |
|---|---|---|
| `sinkForTag` / `exitForTag` closure | claude's stdout forwarder (`os/exec`), one per live runner | atomic **load** per event |
| `tag.Rotate` via `OnSessionRotate` | the dispatch goroutine running `startFreshRunner` | atomic **store**, once per rotation |

Single atomic word, no lock, so no lock-ordering question and no leaf-mutex violation. `restartMu` is released before the store.

The tag is read **once** per event and the read value is used for both the envelope and any drop record, so a concurrent rotation cannot split one event across two ids.

Ordering against the drain is unchanged: envelopes stay FIFO on the one fan-in channel with a single reader, so events tagged with the old id that were already queued are still processed before any tagged with the new one.

## Error handling

| Condition | Behaviour |
|---|---|
| `RestartFresh("")` | Existing Warn + early return. `OnSessionRotate` never fires; the tag keeps its value (AC 3). |
| `Rotate("")` reached directly | No-op. The tag keeps its value; the type's invariant holds independently of its caller. |
| `OnSessionRotate == nil` | Nil-checked at the fire site. Every construction path other than `newStreamRunnerFactory` leaves it nil and is byte-identical to today. |
| Event arrives tagged with the pre-rotation id | Dropped at the unchanged gate with the unchanged content-free Debug record. Fail-closed (AC 2). |
| Tag rotated to an id no conversation is bound to | Every event dropped, fail-closed, until a later rotation. Unreachable in production: `RestartFresh`'s sole production caller passes the id `RotateForNewSession` just minted and bound. |

Nothing here returns an error — the tag store cannot fail — so no new error path enters the runner's backoff ladder.

## Testing strategy

RED first at every tier.

**`internal/streamsup` (`runner_test.go`)**

- `RestartFresh(newID)` invokes `OnSessionRotate` exactly once with `newID`. Fires without a live child, so the test needs no spawn.
- `RestartFresh("")` invokes it **zero** times (AC 3).
- A runner with `OnSessionRotate == nil` survives `RestartFresh` (no panic) — the nil-check.
- The callback observes the rotation **before** the runner's next spawn id is consumed: assert ordering by recording the callback's argument against the id the following `beginSpawn` reports.

**`cmd/pyry` (`stream_turn_drain_test.go`)**

- Table over the tag: envelopes carry `tag.ID()` at send time, not at bind time — pre-rotation events tagged old, post-rotation tagged new, on **both** lanes (sink and exit) (AC 1).
- `Rotate("")` leaves the tag; envelopes stay tagged with the previous id (AC 3).
- The class-aware drop policy is unchanged under `sinkForTag`: re-run the existing full/reserve/watermark cases through the new handle.
- **Gate fail-closed after rotation** (AC 2): with the drain running and the active conversation bound to the new id, an envelope tagged with the **old** id is still dropped before `emitter.Handle`.
- A `-race` case: concurrent `Rotate` and per-event reads.

**`cmd/pyry` (`streamsup_runner_test.go`)**

- The factory binds all three: assert `scfg.OnSessionRotate != nil` and that invoking it retags **both** lanes' envelopes (AC 1's "both lanes" clause, at the wiring tier where the two are bound together).

**`internal/e2e` (`relay_v2_stream_new_session_test.go`) — the end-to-end proof (AC 4)**

- New milestone **M6**, after M4: drain phone envelopes until an `assistant_delta` for `knownConvID` whose `Text` carries `echoNeedleTwo` — the turn sent **after** the rotation reaches the fake phone.
- M4 stays exactly as it is. It is what pins WHICH child served the turn; M6 pins that the event reaches the client. Neither subsumes the other.
- Header rewrite: the "THE POST-ROTATION DRAIN DIVERGENCE … asserting a delta would HANG" paragraph is replaced by the rotation it now describes, and the milestone list gains M6.
- The AC-1 Debug-level instrument guard's "WHY A DEBUG RECORD IS DETERMINISTIC HERE" paragraph and its failure-message reading list both rest on the post-rotation drop and must be re-anchored — see § Open questions.
- **Non-vacuity:** M6 must fail on `main`. Run it once against the pre-fix tree and confirm it times out waiting for the delta; that is the fails-before / passes-after evidence, and it is why the fix and the e2e cannot be split.

**Comment corrections (AC 4's second clause)**

- `cmd/pyry/stream_turn_busy.go` — the `turnBusyTracker` doc's unreachable-rotation-edge paragraph. Its *conclusion* survives: the edge needs two distinct producer tags resolving to one conversation **at the same time**, and one runner still has exactly one tag at any instant — it now moves rather than being frozen. The *premise* ("fixed at runner construction") is retired. The neighbouring paragraph's "the runner's construction-time session tag" phrasing goes with it.
- `internal/e2e/realclaude/interactive_stream_new_session_test.go` carries the same stale rationale twice and is **deliberately left alone** (the ticket's out-of-scope list): it sits behind the `e2e_realclaude` tag, so `make check` never compiles it, and touching it would pull this ticket onto the live gate for a comment edit.

**Verification (§ B2 scope):** `go test -race ./internal/streamsup/... ./cmd/pyry/...`, `go test -tags e2e -race -run TestRelayV2_StreamNewSessionRotatesAndRestartsFresh ./internal/e2e/`, `go vet ./...`, `go build ./cmd/pyry`.

## Open questions

1. **What deterministically emits a `level=DEBUG` record in the e2e once the post-rotation drop is gone?** The AC-1 guard asserts only "some Debug record anywhere in the capture", so it should still pass — but its stated determinism argument dies with the drop, and replacing a false argument with an unverified one is no better. Resolve empirically: read the daemon's captured stderr from the post-fix run and name a record that actually appears, then rewrite the paragraph around it. Candidate: the drain's own `stream_turn.not_active` still fires **before** M1's `send_message` stamps the active-conversation cursor (`boundSessionIDForActive` returns `ok == false` on an empty cursor), for any event the bootstrap child produces at spawn — but that is a hypothesis to confirm against the capture, not to assert. Record the resolution under `## Revisions`.
2. **Does Window B (§ Security review) ever actually deliver an outgoing child's tail under the fresh tag in practice?** Not measured. Stated as a bound, not as an observation; not defended with a guard, per evidence-based fix selection.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings, one boundary named.** The tag's value crosses no trust boundary: `newID` originates in `Pool.RotateForNewSession`'s minted UUID, daemon-side, and is never phone-writable. The handle does accept any non-empty string, and its safety rests on `RestartFresh`'s sole production caller (`startFreshRunner`) passing a freshly minted, freshly bound id — but that is an **existing** boundary this ticket consumes rather than a new one: the same argument already re-execs claude with `--resume <that id>`, which is the larger consequence. No guard is added, because the runner has no knowledge of the valid-id set and a second, weaker vocabulary would be a liability. `boundSessionIDForActive` remains the only producer of the value it is compared against.
- **[Concurrency] No findings — MUST-FIX avoided by the fire-site choice.** Firing `OnSessionRotate` under `restartMu` would put a consumer-supplied callback under a documented leaf mutex, which `beginSpawn`'s doc forbids and which would make the leaf property depend on what the consumer installs; the design fires below the unlock. The tag is `atomic.Pointer[string]`, so no lock is introduced on either the per-event path or the rotation path and there is no lock-order edge to document. The per-event read is taken **once** and reused for both the envelope and the drop record, so a concurrent rotation cannot split one event across two ids. No goroutine is added, so no lifecycle or leak question arises.
- **[Window A: rotate → tag rotation] No finding — fail-closed, and unchanged from today.** `startFreshRunner` runs `beginRotationOrNoop` → `rotate(oldID)` → `RestartFresh(newID)`, so the conversation rebinds *before* the tag moves. In that window the outgoing child's tail is tagged with the old id and dropped at the gate. That is correct: the client has already received `session_transition{clear}`, and it is exactly the behaviour that ships today.
- **[Window B: tag rotation → fresh child bound] SHOULD FIX-class, accepted as a fidelity bound, not a disclosure one — and stated rather than left implicit.** The Parser is the runner's `Config.Stdout` for **every** spawn, so residual stdout from the **outgoing** child parsed after the tag rotates is forwarded under the fresh session's tag. The exposure is bounded to: the same runner, the same conversation, the same following client, and the same turn that client itself sent and was already receiving deltas for before it pressed new_session. No cross-conversation and no cross-client path is opened — the gate's exact-match comparison is untouched, and the tag can only ever hold an id this runner was constructed with or was rotated onto by the daemon's own rotation. The alternative ordering (rotate after the cancel) trades this fidelity window for reintroducing the availability bug the ticket exists to fix, on the successor child. Accepted, documented at the fire site, not guarded.
- **[Error messages, logs, telemetry] No findings.** The drop records are unchanged in level, message and field set; they carry the event discriminant (`eventKind`, which never returns child-authored text) and the session id only. The refactor moves the id's *source* from a captured parameter to an atomic load and changes nothing about what is rendered. No new log call is added.
- **[Availability / resource exhaustion] No findings.** The class-aware reserve, the `droppableCap` watermark and the non-blocking sends are carried verbatim into `sinkForTag`/`exitForTag`; the fan-in's absolute memory bound is unchanged. The tag adds one pointer per runner. `OnSessionRotate` fires at most once per `new_session`, an operator-driven action already rate-limited by the rotation gate.
- **[Existence oracle] No findings.** `Rotate` returns nothing — no bool, no error — so it cannot report whether the tag changed, what it held, or whether a conversation exists. `ID()` is package-private to `cmd/pyry` and reachable only from the two sink closures.
- **[Tokens / crypto / file operations / subprocess / network] N/A, by construction.** The change adds no filesystem, exec, crypto or network operation, and no wire-format field. `RestartFresh`'s spawn argv is untouched — the tag is read only by the two fan-in closures and never reaches `buildArgs`, an argv token, or a path.
- **[Threat model alignment] No findings.** `docs/protocol-mobile.md` § Security model's relevant threat here is cross-conversation event leakage to a following client. The gate that prevents it (`startStreamTurnDrainV2`'s `env.sessionID != active` exact match) is unmodified, un-widened, and gains no fallback; this ticket only corrects the input it compares, so the daemon moves from "drops everything" to "drops exactly what does not match" without relaxing the comparison.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-06
