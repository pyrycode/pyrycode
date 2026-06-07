# Spec — #619: bump tui-driver to v1.3.0 (`Events` gains a `Tracker` param)

Confirms PO's size: **XS** (architect may lower, never raise — here it holds at XS).
One production file modified (`internal/agentrun/ptyrunner/runner.go`, one line),
plus the `go.mod` / `go.sum` pin. No new files, no new exported types, one consumer
call site, three ACs, zero reject branches. No red lines tripped — proceed as one ticket.

Not `security-sensitive` (ticket is unlabelled, and correctly so): a behaviour-preserving
dependency bump. The `Events` stream parses the locally-supervised claude child's own JSONL
(trusted local input). No auth/token/crypto/header/dispatch design decision is introduced for
a reviewer to audit — the internet-exposed surface lives downstream in #615 (the producer) and
#618 (the inbound mobile handler, which carries the label). The security-review step is skipped.

## Files to read first

- `internal/agentrun/ptyrunner/runner.go:447-509` — the single edit site and its proof of
  correctness in one window: `tracker := tuidriver.NewTracker(cfg.WatchdogTrackerOpts)` (467),
  the watchdog goroutine that already reads it (488–495), and the `Events` call to amend (503).
  Confirms the watchdog `tracker` is in lexical scope at the call and already live.
- `internal/agentrun/ptyrunner/runner.go:512-537` — the `switch ev.Kind` event loop. Note it has
  **no `default` arm**: this is the proof that v1.3.0's additive `EventKindStallDetected` falls
  through silently — the "no behaviour change" AC rests on this exact structure. Do not add a
  `default` arm; adopting stall handling is explicitly out of scope (evidence-based — no agent-run
  stall has been observed).
- `go.mod:11` — current `github.com/pyrycode/tui-driver v1.2.0` pin to bump.
- `go.sum` — the two `github.com/pyrycode/tui-driver v1.2.0` lines (the `h1:` zip hash and the
  `/go.mod` hash); `go get` rewrites both to v1.3.0.
- `docs/knowledge/codebase/513.md` — established ptyrunner as the `Session.Events` consumer and
  enumerates the `EventKind*` constants it switches on. Context for why this is the only call site.

## Context

The Phase 2 structured-streaming producer (#615, EPIC #596 / ADR-025) maps tui-driver `JSONLEntry`
tool payloads into the neutral internal turn-event model using `ParseToolUse` (tui-driver#140) and
`ParseToolResult` (tui-driver#142). Those helpers were merged but first **released in tui-driver
v1.3.0**; pyrycode pins **v1.2.0**, which predates them, so #615 cannot compile until this bump
lands (it is why #615 was parked). This ticket does the minimum to unpark #615: the version pin
plus the one adaptation v1.3.0's lone breaking change forces. Follows the established
`chore(deps): bump tui-driver to vX` pattern (#601 → v1.2.0, #599 → v1.1.0, #564 → v1.0.1).

v1.3.0's breaking change (tui-driver#141): `(*Session).Events` gained a required trailing
`tr *Tracker` parameter for the `stall_detected` degrade marker —

```go
func (s *Session) Events(ctx context.Context, jsonlPath string, startOffset int64, tr *Tracker) (<-chan Event, error)
```

A `nil` tracker **panics** on first dereference inside tui-driver. v1.3.0 also adds an additive
`EventKindStallDetected` constant — handled by no one here (see the no-`default`-arm note above).

## Design

Two changes, both mechanical:

1. **Pin bump.** `go.mod` and `go.sum` move `github.com/pyrycode/tui-driver` from `v1.2.0` to
   `v1.3.0`. Apply via the module tool, not by hand-editing the hash lines:

   ```
   go get github.com/pyrycode/tui-driver@v1.3.0
   go mod tidy
   ```

   `go get` rewrites the `go.mod` `require` line and both `go.sum` lines; `go mod tidy` prunes any
   stale indirect entries. Do **not** introduce a `replace` directive or a pseudo-version — the pin
   is a clean released tag.

2. **`Events` call adaptation** (`runner.go:503`). Pass the watchdog `tracker` already constructed
   at line 467 — the same `*tuidriver.Tracker` value, not a fresh one and **not `nil`**:

   - before: `sess.Events(runCtx, jsonlPath, 0)`
   - after:  `sess.Events(runCtx, jsonlPath, 0, tracker)`

That is the whole production diff. The package doc comment at the top of `runner.go` already
describes the watchdog `Tracker` and the `Session.Events` stream; it needs no edit — the
parameter addition does not change the documented behaviour. (Optional, at the developer's
discretion: a half-line inline note at the call site that the tracker is shared with the watchdog.
Not required.)

## Concurrency model

No new concurrency design. The `tracker` is **already** shared across multiple goroutines in the
current (v1.2.0) code:

- the main `Run` goroutine — `RecordTransition("prompt-written")` at line 468;
- the budget `Terminate` hook — `RecordTransition("budget-hit")` at line 475, fired from the budget
  Counter's timer goroutine;
- the watchdog goroutine — `runWatchdog(runCtx, sess, tracker, …)` at line 492.

So `*tuidriver.Tracker` is already required to be safe for concurrent use, and the existing
`go test -race` suite already exercises that. Passing the same tracker to `Events` adds tui-driver's
internal Events goroutine as one more accessor of an already-concurrent-safe type — it does not
introduce a sharing pattern that didn't exist before. The race detector covers the new accessor for
free; there is nothing for pyrycode to guard here.

## Error handling

- **`nil` tracker → panic.** Avoided structurally: the only value in scope to pass is the live
  `tracker` from line 467, which `NewTracker` always returns non-nil. The AC forbids `nil`; the
  design has no path that could pass it.
- **`EventKindStallDetected` → ignored, by design.** The event loop's `switch ev.Kind` has no
  `default` arm (lines 514–536), so the new kind is silently dropped — identical to how any
  unhandled kind behaves today. This is the "no behaviour change" guarantee. Adopting stall
  handling is deferred until a real agent-run stall is observed (evidence-based-fix principle).
- **All existing `Events` error/ctx-cancel handling is unchanged** — the return-value contract
  (`ptyrunner: events: %w` on a non-ctx synchronous open/seek failure; ctx-collapse to `nil`) is
  untouched by adding an argument.

## Testing strategy

No new test. The bump is verified by the existing suite — `go build ./...`, `go vet ./...`, and
`go test -race ./...` must all pass **unchanged**. The race detector is the load-bearing check
here: it validates the now-four-way-shared `tracker` (see Concurrency model). The ptyrunner tests
drive `Run`, which constructs the real tracker and calls `Events` on the live path, so the new
signature and the non-nil tracker are both exercised end-to-end without any test edit.

No `_test.go` file references `Session.Events` directly (the only `.Events(` call site in the repo
is `runner.go:503`), so no test signature needs updating.

**If a test does break against v1.3.0**, do not reflexively retune a threshold or dismiss it as
"version drift." Ask first whether the failure reveals a genuine behaviour change in v1.3.0 that
this ticket's "no behaviour change" claim missed — and if so, stop and surface it rather than
patching the test to pass. (See the assistant-docs note "Dependency-Drift-Over-Test-Validity",
2026-06-06.) A clean pass is the expected outcome; an unexpected failure is signal, not noise.

## Open questions

- **Module availability.** The bump requires fetching `tui-driver@v1.3.0` through the module proxy.
  The ticket asserts v1.3.0 is released (#140/#141/#142 merged and tagged). If `go get` cannot
  resolve the tag (proxy unreachable, or the tag isn't actually published yet), that is a
  release/environment blocker — escalate it; do **not** work around it with a `replace` directive,
  a pseudo-version, or a local checkout. The pin must be the clean released tag or nothing.
