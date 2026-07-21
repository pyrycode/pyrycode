# Spec: Config toggle `interactive_runner` + rollback docs (#1081)

> **Ticket:** #1081 · **Size:** S (confirmed, not split — see § Sizing) · **Label:** `security-sensitive`
> **Blocked-by:** #1131 (modal-keystroker typed-nil guard) — **CLOSED / merged** (PR #1132). Unblocked.
> **Blocks:** #1082 (fake-daemon e2e for stream mode under the toggle).

This is the single "turn on stream-json interactive mode" wiring step. Every seam it wires is **merged**; the work is thin assembly plus one new config field, one startup selector, and one parallel relay-leg branch. No new logic, zero new exported types.

---

## Files to read first

Generated from the code surface; each line names what to extract.

- `internal/config/config.go:12-53` — `Config` struct + `Load`. Copy the **`DebugCapture` precedent** exactly: additive `json` field, **no** `DefaultConfig` entry (absent → zero value → PTY). `Load` stays parse-only — do NOT add enum validation here.
- `cmd/pyry/streamsup_runner.go:69-101` — `newStreamRunnerFactory(sink *streamTurnSink) sessions.RunnerFactory`. **Already built. Do not re-implement.** Its doc-comment names #1081 as the assigner. Construct one sink, call this, inject the result.
- `cmd/pyry/stream_turn_drain.go` (whole file) — `streamTurnSink`, `newStreamTurnSink(buf, logger)`, `sinkFor(sessionID)`, and `startStreamTurnDrainV2(ctx, sink, emitter, activeSession, logger) (cleanup func())`. **Already built; zero production callers today.** Note its doc explicitly scopes "production selection (`sessions.Config.RunnerFactory` wiring) and composing `activeSession`/`SetReplaySource` at the relay leg" to this ticket.
- `cmd/pyry/main.go:741-770` — `config.Load` → `sessions.New(sessions.Config{...})`. The composition root. The selector runs between these; `RunnerFactory` is threaded into the `sessions.Config` literal.
- `cmd/pyry/main.go:750-752` — the `DebugCapture` gate: the exact "resolve X only when the flag is on, else leave the byte-identical default" shape the selector mirrors.
- `cmd/pyry/main.go:923-965` — the `relayWiring{...}` literal `startRelay` is called with. One new field (`streamSink`) is set here.
- `cmd/pyry/relay.go:196-244` — `relayWiring` struct fields (`sup`, `bridge`, `claudeSessionsDir`, `active`, `convReg`, `boundHost`, `approvals`). The new `streamSink` field is declared here.
- `cmd/pyry/relay.go:477-499` — `snapshotUsage` build. **Landmine #1**: its `pidFn` reads `w.sup.State().ChildPID` — nil-deref on the typed-nil stream-mode `w.sup`. Gate it off in stream mode.
- `cmd/pyry/relay.go:656-673` — the shared PTY interactive-streams gate (`startInteractiveTurnStreamV2` + `startInteractiveModalStreamV2`). **Landmines #2/#3**: both take `w.sup` and a `pidFn` reading `w.sup.State()`. This is the block a stream-mode branch replaces.
- `cmd/pyry/relay.go:709-727` — the returned `cleanup` closure; the stream drain's cleanup joins here.
- `cmd/pyry/interactive_turn_stream_v2.go:96-114` — the **canonical emitter + replay wiring** the stream branch mirrors: `newInteractiveTurnEmitterV2(active, mgr, logger)` then `mgr.SetReplaySource(emitter.ring, active.CurrentConversation)`. Same emitter type `startStreamTurnDrainV2` already consumes.
- `cmd/pyry/interactive_turn_v2.go:117` — `newInteractiveTurnEmitterV2(sup cursorReader, bcast interactiveBroadcaster, logger)` signature. `*activeConversation` satisfies `cursorReader`; `*relay.V2SessionManager` satisfies `interactiveBroadcaster`.
- `cmd/pyry/main.go:1218-1285` — `resolveBoundRunner` / `resolveBoundSession`: the merged follow-active resolvers (`convID → CurrentSessionID → Pool.Lookup`). The stream `activeSession` closure mirrors the **`conv.CurrentSessionID` guard** these enforce.
- `internal/sessions/session.go:245-258` — `Supervisor()` returns typed-nil `*supervisor.Supervisor` for a stream runner; `Runner()` is total. **This is why `w.sup` is nil-deref-unsafe in stream mode.**
- `internal/sessions/transition.go:113-142` + `internal/sessions/pool.go:627-641` (`rekeyLocked`) — `RotateForNewSession` rebinds `conv.CurrentSessionID` to a fresh id on a stream `new_session`. **Read for § Open Questions (the frozen-tag gap).**
- `internal/sessions/pool.go:139-147` + `:355-362` — `Config.RunnerFactory` seam (nil ⇒ `supervisor.New`, the rollback guarantee) + `newRunner` normalization.

---

## Context

The daemon has a merged stream-json interactive runner (`internal/streamsup`) selectable behind `sessions.Config.RunnerFactory` (nil ⇒ today's PTY `supervisor.New`, byte-identical — the rollback guarantee). Three merged seams sit unwired:

1. **The factory** — `newStreamRunnerFactory(sink)` (#1109), the only `streamsup.New` caller.
2. **The drain** — `streamTurnSink` + `startStreamTurnDrainV2` (#1098), a late-bound singleton fan-in with **zero production callers**.
3. **The selection seam** — `sessions.Config.RunnerFactory` (#1077).

This ticket adds the operator switch (`interactive_runner` in `~/.pyry/config.json`), picks the factory from it at the composition root, and lights the factory's turn stream so a relay client following the active conversation actually receives assistant output. It changes neither runner's behaviour — it is the wire between them.

Per-conversation routing consumers that ride the typed-nil bootstrap `w.sup` are **all merged and guarded**: interrupt (`activeInterrupter`, #1121), new_session (`activeSessionStarter`, #1125), snapshot-offline (`screenSnapshotterOrNil`, #1101), and the modal-resolution keystroker (`modalKeystrokerOrNoop`, #1131). What remains for this ticket is the **turn stream** and the **three remaining raw `w.sup.State()` readers** on the relay leg (§ Design 3).

---

## Design

### 1. Config field (`internal/config/config.go`)

Add one additive field to `Config`, following `DebugCapture` exactly:

```go
// InteractiveRunner selects which interactive runner the daemon builds:
// "" or "pty" → the terminal-driven PTY supervisor (byte-identical to today,
// the T3 rollback guarantee); "stream-json" → the streamsup-backed runner.
// Absent/empty is the default via the JSON zero value — NO DefaultConfig entry,
// so a config that omits the field keeps the PTY path. Any other value aborts
// startup (validated at the composition root, not here). Rollback: set back to
// "pty" (or remove the field) and restart the daemon.
InteractiveRunner string `json:"interactive_runner"`
```

- **No `DefaultConfig` entry** — absent must stay `""`, not become a non-empty value (T3 rollback: absent ⇒ nil factory ⇒ `supervisor.New`).
- `config.Load` stays **parse-only**; it does not validate the enum. (Matches `DebugCapture`, which added no `Load` validation. See config-package.md "Load semantics".)

### 2. Startup selector (`cmd/pyry/main.go`)

Extract a pure, unit-testable helper — the "startup validator/selector" (AC4's loud-error path):

```go
// selectInteractiveRunner maps cfg.InteractiveRunner to the runner factory and
// its turn-event sink. "" / "pty" → (nil, nil, nil): nil RunnerFactory keeps the
// PTY path byte-identical. "stream-json" → (factory, sink, nil) built over ONE
// newStreamTurnSink, so the factory (sinkFor per runner) and the single drain
// share that one instance (#1098 late-bound-singleton). Any other value →
// (nil, nil, error) naming the offending value and the accepted set. No silent
// PTY fallback (AC4).
func selectInteractiveRunner(cfg config.Config, logger *slog.Logger) (sessions.RunnerFactory, *streamTurnSink, error)
```

- One `newStreamTurnSink(0, logger)` call; `newStreamRunnerFactory(sink)` closes over it; **both returned** so caller threads the same instance two ways.
- Error message shape (AC4): `interactive_runner %q not recognized (accepted: "pty", "stream-json")`.

In `runSupervisor`, after `config.Load` and **before** `sessions.New` (AC: fail fast before the pool is built):

- Call `selectInteractiveRunner`; on error `return fmt.Errorf("...: %w", err)` (aborts startup, no fallback).
- Thread `factory` into the `sessions.Config{...}` literal as `RunnerFactory: factory`.
- Thread `sink` into the `relayWiring{...}` literal as `streamSink: sink`.

Both are nil in PTY/absent mode — the `sessions.Config` and `relayWiring` literals are unchanged in behaviour on the rollback path.

### 3. Relay-leg stream branch (`cmd/pyry/relay.go`) — the bulk of the work

**New `relayWiring` field** (declared in `relay.go`, set in `main.go`):

```go
// streamSink is the stream-json runner's turn-event fan-in. Non-nil selects
// stream mode (main.go sets it iff interactive_runner == "stream-json"); nil
// keeps the PTY interactive path. Its presence IS the relay-leg's stream-mode
// discriminant — see the three w.sup gates below.
streamSink *streamTurnSink
```

`w.streamSink != nil` is the mode discriminant. (It is exactly equivalent to `w.sup == nil`, because `bootstrap.Supervisor()` returns a genuine nil `*supervisor.Supervisor` for a stream runner — but keying off the sink is self-documenting and is the object the branch needs anyway.)

**Three `w.sup` readers must be neutralized in stream mode** — each calls `w.sup.State()`, which locks `s.mu` and nil-derefs on the typed-nil stream-mode `w.sup` (`internal/supervisor/supervisor.go:267`). These are the "#1081 gating audit" seams #1131 deferred:

| # | Site | Today's gate | Change |
|---|------|--------------|--------|
| 1 | `snapshotUsage` build (`relay.go:478`) | `if w.claudeSessionsDir != ""` | `if w.streamSink == nil && w.claudeSessionsDir != ""` — stream mode leaves `snapshotUsage` nil ⇒ the `screen_snapshot` handler reports zero usage, coherent with `screenSnapshotterOrNil(w.sup)` already returning nil (no live PTY screen). |
| 2 | `startInteractiveTurnStreamV2` (`relay.go:668`) | inside `if w.bridge != nil && w.claudeSessionsDir != ""` | replaced by the stream turn drain (below). |
| 3 | `startInteractiveModalStreamV2` (`relay.go:669`) | same gate | **not replaced** — the PTY modal stream's stream-mode analogue (the #1080 approval bridge) is already wired unconditionally at `relay.go:631`. Just gated off. |

**Restructure the `relay.go:660` gate** into a three-way branch (prefer a clean parallel branch over refactoring the PTY path):

```
if w.streamSink != nil {
    // STREAM MODE: gate BOTH PTY streams off; wire the stream turn drain.
    emitter := newInteractiveTurnEmitterV2(w.active, mgr, logger)
    mgr.SetReplaySource(emitter.ring, w.active.CurrentConversation)   // session-scoped, mirrors the PTY path
    activeSession := <the follow-active resolver, below>
    streamDrainCleanup = startStreamTurnDrainV2(ctx, w.streamSink, emitter, activeSession, logger)
} else if w.bridge != nil && w.claudeSessionsDir != "" {
    // PTY MODE: unchanged — startInteractiveTurnStreamV2 + startInteractiveModalStreamV2.
} else if w.bridge != nil {
    // unchanged no-sessions-dir Info log.
}
```

- The stream branch does **not** gate on `w.bridge` or `w.claudeSessionsDir`: the drain consumes parsed `turnevent.Event`s from the sink, not a PTY bridge or on-disk transcript. It runs whenever stream mode is selected and the relay leg is up.
- The emitter + `SetReplaySource` are **byte-identical** to the PTY path's construction (`interactive_turn_stream_v2.go:96-109`); only the *feeder* differs (`startStreamTurnDrainV2` vs `startInteractiveTurnStreamV2`). The branches are mutually exclusive, so `SetReplaySource` runs at most once.

**The follow-active `activeSession` closure** — extract as a named helper for a focused unit test (parallels the merged `resolveBoundSession` guard):

```go
// boundSessionIDForActive resolves the pool session id bound to the ACTIVE
// conversation, for the stream turn drain's AC2 scoping gate. Mirrors the
// follow-active cursor the PTY emitter reads (active.CurrentConversation) and
// the conv.CurrentSessionID == "" isolation guard resolveBoundRunner/
// resolveBoundSession enforce. Returns ("", false) when there is no active
// conversation or it is unbound — the drain then drops every event (fail-closed),
// exactly as the emitter drops on an empty cursor. Deliberately skips pool.Lookup
// (unlike boundHost): a stale id matches no live producer's tag, so forwarding
// nothing is the fail-closed outcome — no cross-session leak, no pool dependency
// dragged into relay.go.
func boundSessionIDForActive(active *activeConversation, convReg *conversations.Registry) (sessionID string, ok bool)
```

Body: `active.CurrentConversation()` → `""` ⇒ `("", false)`; else `convReg.Get(...)` → miss or `CurrentSessionID == ""` ⇒ `("", false)`; else `(conv.CurrentSessionID, true)`. Uses only existing `relayWiring` fields (`w.active`, `w.convReg`) — no new plumbing beyond `streamSink`.

**Cleanup**: add `streamDrainCleanup func()` to the `var (...)` block at `relay.go:656`, and in the returned `cleanup` (`relay.go:709`) call it (guarded `if streamDrainCleanup != nil`) alongside `streamCleanup`/`modalStreamCleanup`, before `<-mgrDone`.

### 4. Rollback docs (AC5)

- **Developer deliverable (in-worktree):** the `InteractiveRunner` field's Go doc-comment (§ 1) documents the rollback (set to `"pty"` / remove the field → restart).
- **Evergreen operator note in `docs/knowledge/features/config-package.md`** is a **documentation-phase** deliverable, authored after merge — NOT a developer AC. (`config-package.md` lives outside `src/`/`test/`/the spec; per the pipeline's doc-ownership rule the developer's worktree does not mutate it. The precedent: `DebugCapture` (#802) is likewise absent from `config-package.md` — documentation phase owns those updates.)

---

## Concurrency model

Unchanged from the merged parts — this ticket adds no new goroutines beyond `startStreamTurnDrainV2`'s single drain goroutine (already built, already tested #1098):

- **Fan-in:** N stream-json Parsers (one per session, each on claude's `os/exec` stdout-forwarder goroutine) push `{sessionID, ev}` via a **non-blocking** send onto one buffered channel (cap 256); full ⇒ drop-newest (never wedge claude).
- **Single reader:** the drain goroutine is the sole caller of `emitter.Handle` / `emitter.flushDelta`, preserving the emitter's single-Run-goroutine invariant (the same assumption the PTY producer relies on).
- **Shutdown:** the sink channel is never closed (a Parser may outlive the drain); the drain stops on `ctx`. `streamDrainCleanup()` blocks until the goroutine exits, mirroring `startInteractiveTurnStreamV2`'s cleanup, and is joined before `<-mgrDone`.
- **Ordering:** `SetReplaySource` runs before `mgr.Run`'s goroutine can serve a reconnect-replay (same as the PTY path).

---

## Error handling / failure modes

- **Invalid `interactive_runner` value** → `selectInteractiveRunner` returns an error naming the value + accepted set; `runSupervisor` aborts startup. **No silent PTY fallback** (AC4).
- **`streamsup.New` construction failure** (empty SessionID — impossible at pool sites per #1108; missing binary; absent workdir) → `newStreamRunnerFactory` already wraps and returns a nil runner with a non-nil error; it surfaces through the pool's existing `sessions: … supervisor: %w` wraps at both construction sites. No PTY substitution (already handled by #1109).
- **Sink full** → drop-newest, content-free debug log (already handled by #1098).
- **Typed-nil `w.sup` deref in stream mode** → structurally prevented by the three gates (§ Design 3). A relay-leg panic would be an availability defect, not a leak.
- **PTY rollback path** (`""`/`"pty"`) → nil factory, nil sink; `sessions.Config`, `relayWiring`, and every relay-leg gate evaluate exactly as today (byte-identical).

---

## Testing strategy

Bullet scenarios (developer writes them in the project idiom; stdlib `testing`, table-driven):

- **`config_test.go`** — `Load` decodes `interactive_runner`: absent ⇒ `""`; `"pty"`; `"stream-json"`; an arbitrary string decodes verbatim (Load does not validate — validation is the selector's job).
- **`selectInteractiveRunner` (main-package test)** —
  - `""` and `"pty"` ⇒ nil factory **and** nil sink, nil error (rollback path).
  - `"stream-json"` ⇒ non-nil factory **and** non-nil sink, nil error; assert the returned sink is the same instance the factory feeds (one `newStreamTurnSink`).
  - `"garbage"` ⇒ nil factory, nil sink, error whose message contains the offending value **and** both accepted tokens (AC4).
- **`boundSessionIDForActive` (relay-package test)** — over a real `conversations.Registry`:
  - no active conversation (`CurrentConversation() == ""`) ⇒ `("", false)`.
  - active but conversation absent / `CurrentSessionID == ""` ⇒ `("", false)` (isolation guard — never falls through to a bootstrap default).
  - active + bound ⇒ `(conv.CurrentSessionID, true)`.
- **Drain scoping** is already covered by #1098's `startStreamTurnDrainV2` `-race` test (event from a non-active session dropped before `Handle`). No re-test needed; the new surface is the `activeSession` closure, covered above.
- **Relay branch selection** — if an existing `startRelayV2`/`v2session_test.go` harness makes it cheap: assert that with a non-nil `streamSink` the PTY streams are not built and the drain is; otherwise the unit tests above plus the #1082 fake-daemon e2e cover it. Do **not** stand up a new heavy harness for this.

---

## Sizing (verdict: S, not split)

Red-line tally against the raw counts:

- **Production files:** 3 — `config.go`, `main.go`, `relay.go`. (`streamsup_runner.go`, `stream_turn_drain.go`, `interactive_turn_stream_v2.go` are read-only references — already built.) ≤ 5 ✓
- **Total written (production + tests + helpers):** config field ~8 LOC; `selectInteractiveRunner` ~15; main.go wiring ~6; relay.go field + 3 gates + branch + `boundSessionIDForActive` + cleanup ~40; tests ~95. ≈ **165 LOC**. ≤ 600 ✓
- **New exported types/interfaces:** 0 ✓
- **Consumer call-site fan-out:** additive only. `RunnerFactory` is a merged pool field (one assignment); `relayWiring` gains one field (one set site). No interface signature changes ⇒ no cascade. Not refactor-shaped. ✓ (`codegraph_impact` unnecessary — nothing is renamed or re-typed.)
- **Error/reject branches:** 1 new (invalid value). ≤ 10 ✓
- **ACs:** 5, each thin assembly of a merged seam. ✓

The factory selection and its drain share one sink — two ends of one wire; selecting stream-json without wiring the drain ships a runner whose turn stream is dead (a non-functional intermediate). Not split. The modal-keystroker guard that would have been a 6th concern already landed as #1131.

**File-overlap check:** ran `git fetch --prune` then diffed every `origin/feature/<n>` branch against `origin/main` for overlap on `config.go` / `main.go` / `relay.go` / `config-package.md` — **no overlap**. No `blockedBy` needed.

---

## Open questions

1. **[Known gap — recommend follow-up ticket] Frozen sink tag vs. `new_session` rotation.** The sink tags each event with the runner's **construction-time** `cfg.SessionID` (`sinkFor(cfg.SessionID)` is called once, at factory time; `RestartFresh` does not rebuild the Parser). A stream-mode `new_session` (`activeSessionStarter` → `Pool.RotateForNewSession` → `RestartFresh(newID)`, #1125) **rebinds `conv.CurrentSessionID` to `newID`** (`transition.go:113`) but leaves the tag at the original id. After a `new_session` on the followed conversation, `boundSessionIDForActive` returns `newID` while events stay tagged with the old id ⇒ the drain gate drops them ⇒ **the turn stream goes dark for that conversation** until the daemon restarts. This is **fail-closed** (no cross-session disclosure — an unmatched tag forwards nothing) and is a property of the merged #1098 sink / #1124 `RestartFresh` design, **not** of this ticket's wiring; there is no cheap fix inside #1081 (the tag is the only frozen handle, and the conversation binding is the only live handle — they diverge on rotation). The PTY path does not have this gap because it re-resolves the transcript file by the current bound id each subscription. **Recommendation:** file a follow-up to rotate the Parser's tag when `RestartFresh` rotates the runner id (a #1124/#1098-side change), and note the limitation on #1082's e2e. This ticket ships the happy path (a fresh followed session, no rotation), which satisfies AC3 as stated.

2. **Validation locus.** This spec puts enum validation in `selectInteractiveRunner` (composition root), keeping `config.Load` parse-only (matching `DebugCapture`). If a reviewer prefers config-domain validation, an alternative is a `config`-package predicate; the composition-root choice is preferred because the factory mapping (which defines the accepted set) cannot live in the leaf `config` package (no `supervisor`/`streamsup` import).

---

## Security review

**Verdict:** PASS

Adversarial self-audit of the **wiring** this ticket authors (config field, selector, the three `w.sup` gates, `boundSessionIDForActive`, `SetReplaySource`, the shared sink). The already-reviewed gate code is out of remit per the ticket: the #1098 drain gate and the #1088 stream I/O boundary are not re-reviewed here.

**Findings:**

- **[Trust boundaries]** No finding in the wiring. The single explicit scoping boundary is `boundSessionIDForActive`: it reads the **live** `active.CurrentConversation()` cursor (written by `sessionRouter.Route`) and the **live** `convReg` binding — not a global, first-session, or stale default. It is fail-closed on both an empty cursor and an unbound conversation (`conv.CurrentSessionID == ""`), mirroring the #678 isolation guard `resolveBoundRunner`/`resolveBoundSession` enforce. An unmatched or empty id forwards nothing (and no runner is ever tagged `""` — #1108 pins non-empty `SessionID`).
- **[Trust boundaries — sink keying]** No finding. `selectInteractiveRunner` constructs exactly one `newStreamTurnSink`; `newStreamRunnerFactory(sink)` (which keys `sinkFor(cfg.SessionID)` per runner) and `startStreamTurnDrainV2(…, sink, …)` receive that same instance. The factory and the single drain share one sink, correctly keyed per session (#1098 late-bound-singleton).
- **[Trust boundaries — replay]** No finding. The stream branch's `mgr.SetReplaySource(emitter.ring, w.active.CurrentConversation)` is **byte-identical** to the PTY path (`interactive_turn_stream_v2.go:109`): same session-scoped cursor, same emitter ring (which only holds events that already passed the drain's active-session gate). A mid-turn reconnect replays only the active conversation's buffered events — no cross-session surfacing beyond what #647 already reviewed.
- **[Concurrency / Trust boundaries] OUT OF SCOPE** — inside the merged `startStreamTurnDrainV2`, the sequence is `active, ok := activeSession()` then `emitter.Handle(ctx, env.ev)`, and `Handle` re-reads `active.CurrentConversation()` to stamp. A cursor flip in the window between the gate check and the stamp could mis-attribute an event to the newly-active conversation. This lives in the #1098 drain gate (explicitly out of this audit's remit) and is structurally shared with the PTY emitter's cursor design (#632/#687). The fail-closed direction (drop) dominates; **not introduced by #1081's wiring.** Recommend #1082's e2e exercise a route-flip-during-turn to confirm, and any fix land on the #1098 side.
- **[Availability — typed-nil `w.sup`]** No finding. The three `w.sup.State()` readers (`relay.go:479` snapshotUsage, `:668`/`:669` PTY streams) are gated off in stream mode (§ Design 3), so none dereferences the typed-nil bootstrap supervisor. A relay-leg panic would be an availability defect, not a disclosure. The already-guarded seams (`modalKeystrokerOrNoop` #1131, `screenSnapshotterOrNil` #1101, `activeInterrupter` #1121, `activeSessionStarter` #1125) are untouched.
- **[No silent fallback]** No finding. An unrecognised `interactive_runner` value aborts startup via `selectInteractiveRunner` (AC4) — no PTY fallback masks a mis-selected or failed-to-construct stream runner; a `streamsup.New` failure surfaces through #1109's existing non-nil-error path.
- **[Error messages / logs]** No finding. The AC4 error names the operator's own (non-secret) config value in the daemon's own startup error — never sent to a remote client. The drain's drop diagnostics are content-free (#1098); this ticket adds no content-bearing log.
- **[Frozen-tag rotation gap — Open Question 1]** SHOULD FIX (follow-up), **security-benign**. The gap is fail-**closed**: after a stream `new_session`, events with the stale tag match no active id and are **dropped**, never disclosed to another conversation's follower. It is an availability/functional gap, not a confidentiality one. Recommend the follow-up ticket named in Open Questions.
- **[Tokens/secrets · File operations · Subprocess · Cryptographic primitives · Network & I/O]** N/A. The toggle is a local read of the operator-owned, trusted `~/.pyry/config.json` and a Go string-enum → factory selection. It wires no new secret, filesystem path, subprocess argument, crypto primitive, or socket read. claude-subprocess spawning and the stream I/O boundary were reviewed under #1088/#1109.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-21
