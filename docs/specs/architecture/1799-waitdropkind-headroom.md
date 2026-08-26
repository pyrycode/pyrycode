# #1799 — `waitDropKind`: give the give-up barrier real headroom

**Size:** XS (confirmed; PO's estimate stands). One test file, ~16 written lines,
zero production source files, zero call-site edits.

## Files to read first

Everything below is in package `main` under `cmd/pyry`. Resolve symbols with
`codegraph_search` / `codegraph_node`; do not go hunting by line.

- `cmd/pyry/stream_turn_drain_test.go` → `waitDropKind` — **the only symbol this
  ticket changes.** Note its exact signature: `(t *testing.T, kinds <-chan string,
  want string)`. AC4 requires that signature to survive untouched.
- `cmd/pyry/stream_turn_drain_test.go` → `dropWatcher` (and its `Handle` method) —
  the producer feeding the `kinds` channel. Extract one fact: the send is
  **non-blocking with a `default:` arm**, so a full channel silently discards a
  kind. That is why the new diagnostic must be worded as *kinds seen*, never *all
  kinds* (see § Error handling).
- `cmd/pyry/streamsup_runner_exit_test.go` → `waitRecord` — the in-package
  precedent the AC names as the 5s floor. Read it for the `t.Helper()` +
  `time.After` + `for { select { … } }` shape the changed helper must keep.
  **Cite it by this symbol name only** — see § The cite-guard trap.
- `cmd/pyry/streamsup_runner_exit_test.go` → `fakeExitingClaude`,
  `TestStreamRunnerFactory_ChildExitClearsTurnBusy` — the observed-failure call
  site. Extract two things: (a) the barrier sits behind a real spawned `/bin/sh`
  child, which is why it is contention-sensitive at all; (b) the same test already
  arms a **10-second** `context.WithTimeout` for its `WaitIdle` on the *same*
  fixture — the in-package precedent this spec's chosen value leans on.
- `cmd/pyry/main.go` → `inboundActivateTimeout` — the package's naming and
  declaration idiom for a named duration constant (`const <thing>Timeout = N *
  time.Second`, doc comment above). There is no time-valued package constant in
  `cmd/pyry`'s test files yet; this ticket adds the first, and it should look like
  the four in `main.go`.
- `docs/knowledge/features/streamsup-package.md` § "Exit lane on the turn-busy
  fan-in (#1209)" — why the barrier's *design* is already correct: the drop is
  logged after `observe` on the same goroutine, so seeing it is a deterministic
  happens-after signal. Read this so you do not redesign the barrier. Only the
  give-up value is wrong.
- `CODING-STYLE.md` § "Comments — Citing Other Code" — binding on the new doc
  comment.

## Context

`waitDropKind` arms a 2-second give-up deadline. Nine call sites across three test
files share it. Under full-suite contention that budget has no headroom, and one
call site — `TestStreamRunnerFactory_ChildExitClearsTurnBusy`, which sits behind a
real spawned child process — reddened `make check` on PR #1798 while that PR's diff
was confined to `internal/e2e/realclaude` behind the `e2e_realclaude` build tag,
which `make check` never compiles. The cost was misattribution: clearing #1798
required proving its diff *could not* be the cause.

The ticket carries the measurement (2026-08-25, `feature/1764` worktree, compiled
`-race` binary at `GOMAXPROCS=2` alongside 8 busy-loop CPU hogs, `-test.count=15`):
15/15 PASS, fastest 0.32s, **slowest 1.53s against a 2.00s budget** — 76% of the
budget spent at the tail, under a synthetic load the ticket itself notes is
*lighter* than a real `go test -race ./...` fan-out.

This is not a logic defect and there is no redesign here. The barrier is a
deterministic happens-after signal, not a poll. One constant is mis-sized, and the
failure it produces is under-informative.

**No ADR.** A test-helper timeout is not an architectural decision, and the
knowledge this ticket generates (the measurement, the chosen multiple) lives in the
constant's own doc comment, where the next person to touch the value will read it.

## Design

One file changes: `cmd/pyry/stream_turn_drain_test.go`.

### 1. A named package constant

Declare a file-scope constant in package `main` immediately above `waitDropKind`,
in that file's `--- collection helpers ---` section. Adjacency is the point: the
doc comment is the justification for the value, and it should be visible from the
`time.After` that consumes it.

```go
const dropKindWaitTimeout = 10 * time.Second
```

**Name.** `dropKindWaitTimeout`. Matches the package's four existing duration
constants in `main.go`, all `<thing>Timeout`. The ticket's prose says "budget";
the package's idiom says `Timeout`, and a give-up deadline *is* a timeout. Idiom
wins — this is a decision, not an open question; do not deliberate it further.

**Value: 10s.** Rationale, which the doc comment must record (AC1):

| | |
|---|---|
| measured contended tail (2026-08-25, 15 runs) | 1.53s |
| old budget / headroom multiple | 2.00s → **1.3×** |
| new budget / headroom multiple | 10.00s → **6.5×** |

Why 10s rather than the AC's 5s floor:

- 5s is only 3.3× a tail measured under a load the ticket describes as *lighter*
  than the real fan-out. A 3.3× margin on a known underestimate is thin, and
  re-reddening this barrier costs another misattribution cycle.
- 10s is not an invented number: `TestStreamRunnerFactory_ChildExitClearsTurnBusy`
  — the call site that actually failed — already arms a 10-second
  `context.WithTimeout` for its `WaitIdle` on the *same* `fakeExitingClaude`
  fixture. 10s is already this package's stated tolerance for how long the
  spawned-child lane may take under load.
- The larger value costs nothing on the green path: the helper returns the instant
  the wanted kind arrives. It is paid only on a genuine red, and only once in wall
  clock — all nine call-site tests are `t.Parallel()`, so their deadlines overlap
  rather than sum.
- 10s remains ~60× under Go's default 10-minute package timeout, so AC's
  "do not delete the deadline" property holds: a genuine hang still surfaces as a
  named failure at the right line, not a whole-package panic dump.

### 2. `waitDropKind` — unchanged signature, two new locals

The signature stays exactly as it is. The budget lives inside the helper; it is
**not** a new parameter and **not** a per-call-site edit (AC4). The `t.Helper()`
call, the `for { select { … } }` shape, and the `return` on match all stay.

Two locals are added:

- a `time.Time` captured before the loop, for elapsed wall-clock;
- a `[]string` appended on **every** kind received — in arrival order, duplicates
  kept. Duplicates and ordering are signal (`[text_chunk, text_chunk, …]` while
  waiting for `tool_use` is a different diagnosis from an empty slice).

`time.After(dropKindWaitTimeout)` replaces `time.After(2 * time.Second)`.

The timeout arm still calls `t.Fatalf`. It does not return, log-and-continue, or
degrade to a sleep (AC3). Its format string gains the elapsed duration (rounded to
milliseconds) and the accumulated slice alongside the existing `want`.

That is the whole change — roughly eight lines of helper body plus the constant and
its comment. Do not touch anything else in the file.

### 3. `waitRecord` is deliberately left alone

The ticket offers, as optional, pointing `waitRecord` at the same constant. **Do
not.** Three reasons:

1. `waitRecord`'s 5s has never been observed failing. Raising it is a defence
   against a failure mode that has not happened.
2. Sharing one constant forces a bad trade in one direction or the other: it either
   drags `waitRecord` from 5s to 10s (the unobserved-failure defence above) or
   drags `waitDropKind` down to 5s (the thin margin § 1 rejects).
3. Keeping them independent holds the diff to a single file.

If `waitRecord` ever reddens, it gets its own ticket with its own measurement —
same rule the ticket applies to the 22 other `2 * time.Second` barriers in
`cmd/pyry`'s tests, which this ticket explicitly does **not** sweep.

## Concurrency model

Unchanged, and worth stating so it is not accidentally altered.

- `waitDropKind` runs on the **test goroutine**. `t.Fatalf` is only legal there,
  and all nine call sites already call it from the test goroutine. Do not move the
  wait onto a goroutine.
- The `kinds` channel is fed from the drain goroutine via `dropWatcher.Handle`.
  Both new locals are owned exclusively by the test goroutine — read and written
  only inside `waitDropKind` — so no synchronisation is added and `-race` sees
  nothing new.
- The happens-after property the barrier exists for is untouched: the drain logs
  the not-active drop after `observe`, on the same goroutine. Seeing the drop still
  proves the tracker was already fed.

## Error handling

Exactly one failure branch, and its wording carries a correctness constraint.

**The accumulated slice is best-effort, not a census.** `dropWatcher.Handle` sends
into `kinds` with a `select` + `default:`, so on a full buffer (8 or 32 slots
depending on the call site) a kind is silently discarded and never reaches the
helper. The message must therefore say *kinds seen* — it must not claim to
enumerate every drop that occurred. A message that over-claims here would send the
next investigator hunting a phantom.

The failure line must carry three things:

1. **elapsed wall-clock.** Not redundant with the constant. An elapsed of ~10.0s
   says the budget was the binding constraint; an elapsed of 40s says the test
   goroutine was starved so badly the `select` did not run — a different diagnosis,
   and precisely the one full-suite contention produces. Do not "simplify" this
   away on the grounds that the timer fires at a known duration.
2. **`want`** — the kind that never arrived (already present).
3. **the kinds seen**, in arrival order. Empty ⇒ *never arrived*. Non-empty ⇒
   *arrived late, or the wrong kinds kept coming*. This is AC3's whole discriminant.

## Testing strategy

**No new test function is added, and none should be.** The nine existing
`waitDropKind` call sites are the coverage; AC4's requirement is that all nine stay
byte-identical and `make check` goes green.

Verification of AC3's diagnostic is the one-off run the ticket prescribes: force the
constant near zero, run one call site, quote the resulting failure line in the PR
body.

**Do it with `go test -overlay`, not by editing the worktree.** An overlay gives a
real compiled run with no file to remember to revert:

1. Copy `cmd/pyry/stream_turn_drain_test.go` into the scratch directory and change
   only the constant's value there — e.g. to `1 * time.Millisecond`.
2. Write an overlay JSON in scratch mapping the worktree's absolute path to the
   scratch copy: `{"Replace": {"<abs worktree>/cmd/pyry/stream_turn_drain_test.go":
   "<abs scratch>/stream_turn_drain_test.go"}}`.
3. Run, in one shell invocation, `cd <worktree> && go test -overlay=<abs json>
   -race -run 'TestStreamRunnerFactory_ChildExitClearsTurnBusy' ./cmd/pyry/` and
   capture the `timed out …` line verbatim.

Pick that call site specifically: its barrier waits on a real spawned child, so
nothing is buffered on `kinds` at the moment of the call and the deadline arm wins
deterministically. (At a call site where a kind is already buffered, Go's `select`
picks uniformly among ready cases and the run could pass instead — re-run if that
happens.) The captured line will show an empty kinds-seen slice, which is the honest
output for a near-zero budget and demonstrates the *never arrived* side of AC3's
discriminant. One quoted line satisfies the AC; a second capture is not required.

If the overlay route is unavailable for any reason, the fallback is edit → run →
**revert**, and the revert must be verified with `git diff` before committing.

**Why AC3's message content gets no automated pin.** The ticket chose one-off
verification, and that choice is right: an automated assertion on the failure text
would need either an injectable budget (a new parameter, which AC4 forbids) or a
`testing.TB` seam whose sole purpose is capturing a string — and without an
injectable budget such a test would itself burn the full 10 seconds to observe one
format string. Reviewers: this AC is verified by the quoted PR output by design,
not by omission.

Beyond that, `make check` is the gate. `cmd/pyry`'s tests are in the standard
hermetic run — no build tag, nothing opt-in.

## The cite-guard trap

`make check` runs `cite-guard`, and it is diff-scoped: it checks the lines this
branch adds. The new doc comment is exactly such a line.

**The ticket body cites `waitRecord` as `cmd/pyry/streamsup_runner_exit_test.go:80`.
Do not copy that form into the comment.** That line sits inside a declaration, the
guard resolves it, and the gate goes red. There is no depth exemption, no range
exemption, and a bare `:NNN` is worse still.

Write the symbol name: `` `waitRecord` ``. Same for any reference to the failing
test or to the drain — name `TestStreamRunnerFactory_ChildExitClearsTurnBusy`,
name `dropWatcher`, never a file-and-line.

## Open questions

None blocking. Two things the implementer should simply decide and move on:

- Exact phrasing of the doc comment. AC1 fixes its *content* — the 2026-08-25
  contended measurement (slowest of 15 runs = 1.53s against a 2.00s budget) and the
  headroom multiple 10s buys (6.5×) — not its wording. Write it in this file's
  existing voice, which explains *why* a value is what it is rather than restating
  the code.
- Exact `Fatalf` format string. § Error handling fixes the three required elements
  and the *kinds seen* wording constraint; the punctuation is yours.
