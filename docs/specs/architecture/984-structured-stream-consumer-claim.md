# Spec #984 — Structured-stream e2e capstone: consumer claim-before-remove kills the residual trigger-loss race

**Ticket:** #984 (bug, size:s, NOT security-sensitive)
**Test under repair:** `TestTwoPhoneStructured_InteractiveReceivesStream` (`internal/e2e/relay_two_phone_structured_test.go`)
**Evolves:** [#958 empty-read gate](./958-jsonl-trigger-empty-read-gate.md) — this is the RESIDUAL race #958 did not cover.

---

## Files to read first

Load these before touching anything — the fix lands in exactly one function, but the diagnosis
spans the producer (test), consumer (fakeclaude), and daemon tail (ruled out).

- `internal/e2e/internal/fakeclaude/main.go:435-459` — **`emitStructuredJSONLIfTriggered`, THE fix site.**
  Read-then-remove consumer: `os.ReadFile(path)` → `if len==0 return` (the #958 gate) → `f.Write` →
  `f.Sync()` → **`os.Remove(path)`**. The residual race is the read→remove window; extract the
  removal-of-a-changed-file bug here.
- `internal/e2e/internal/fakeclaude/main.go:394-400` + `:169-170` — the poll loop calls `emit` every
  `pollInterval` (50 ms) on the **single main goroutine**. There is exactly one consumer; no
  in-process concurrency inside fakeclaude. This is why a fixed sidecar claim-name is safe.
- `internal/e2e/internal/fakeclaude/jsonl_trigger_test.go` — the #958 **untagged** contract test
  (`package main`, no `//go:build e2e`, runs in the default `go test` gate). Its three subtests
  ("empty is skipped, not removed" / "non-empty consumed and removed" / "missing is a no-op") MUST
  stay green; the new destroy-newer subtest is added here.
- `internal/e2e/relay_two_phone_structured_test.go:279-310` — **the producer.** The 250 ms `kicker`
  goroutine re-drops `midTurnLine` (self-healing) and `dropFull` (a `sync.Once`) drops the 4-line
  `fixture` exactly once after stopping+joining the kicker (`<-kickerDone`). The fixture carries the
  only `tool_use`/`turn_end`; its single loss == the observed failure signature. READ ONLY — do not
  modify.
- `internal/e2e/relay_two_phone_structured_test.go:312-372` — A's decrypt-drain loop + the
  **vacuous-pass guard** (`failVacuous`, `required`, A-observes-full-set-before-B). AC4: this stays
  byte-identical. The fix does not touch this file.
- `cmd/pyry/interactive_turn_stream_v2.go:252-361` — `resolveOwnBootstrapJSONL`: the daemon tail
  settles (~500 ms, retry-driven) and tails **from EOF**. Read to confirm the tail is *demonstrably
  settled before `dropFull` fires* (A must receive its first envelope first) — this RULES OUT the
  tail as the permanent-loss cause. Not modified.
- `internal/e2e/relay_v2_interrupt_test.go:112-212` — **sibling** `TestRelayV2_InterruptStopsRunningTurn`:
  same trigger, but kicker-only; its `turn_end` comes from ESC→`appendTurnEnd` (a direct session
  append, main.go:562), NOT through the trigger. Re-verify after the fix (non-binding AC).
- `docs/specs/architecture/958-jsonl-trigger-empty-read-gate.md` — the empty-read gate this fix
  generalises. Same consumer, same single-seam philosophy.

---

## Context

`TestTwoPhoneStructured_InteractiveReceivesStream` is red-on-main under `make check`'s
`-tags e2e -race` leg — confirmed **pre-existing** (merge-base `a5c7986` fails identically), not
introduced by PR #983. Signature:

```
relay_two_phone_structured_test.go:320: ... (observed 2 structured envelope(s);
missing required types [tool_use turn_end]).
```

A receives `turn_state` + `assistant_delta` (the kicker's `midTurnLine`) but never the full fixture's
`tool_use`/`turn_end`. #958 already closed the empty-trigger consume-and-remove window (the
`O_TRUNC`→write gap, pinned by `TestEmitStructuredJSONLIfTriggered`). **A different race remains.**

---

## Design

### Diagnosis (design-time hypothesis — the developer confirms it under instrumentation, AC1)

The producer drops content to a **single-slot mailbox** (`jsonlTrigger`) via `os.WriteFile`
(`open(O_CREATE|O_TRUNC)` → `write` → `close`). The consumer `emitStructuredJSONLIfTriggered` does a
**non-atomic read-then-remove**: it `os.ReadFile`s the content, appends it to the live session JSONL,
`f.Sync()`s (a real fsync — load-bearing for cross-process tail visibility, and *slow* under
`-race` + full-suite I/O contention), then `os.Remove`s the trigger **by name**.

The residual defect is a classic TOCTOU: **the consumer removes whatever the trigger holds *now*, not
what it read.** If a producer rewrites the trigger between the consumer's read and its remove, the
remove unlinks the *newer* content:

```
1. kicker writes midTurnLine → trigger
2. consumer: os.ReadFile(trigger) → midTurnLine  (non-empty; passes #958 gate)
3. consumer: f.Write(midTurnLine); f.Sync()       ← wide window (fsync under contention)
4. A receives its first envelope → dropFull() fires:
      stopKicking(); <-kickerDone; os.WriteFile(trigger, FIXTURE)   ← trigger now = full fixture
5. consumer: os.Remove(trigger)  ← UNLINKS THE FIXTURE dropFull just wrote
6. dropFull is sync.Once → the fixture is gone forever
   → tool_use/turn_end never appended, never tailed, never reach A
   → A times out with 2 envelopes → exact observed signature
```

Why this and not the alternatives:

- **Not the #958 empty-read race** — that destroyed an *empty* mid-`O_TRUNC` read; here a *complete*
  fixture is destroyed by a remove, and the `len==0` gate never fires for it.
- **Not the daemon tail-settle window** — the test's kicker keeps re-dropping until A observes its
  FIRST structured envelope, and only *then* does `dropFull` fire. So the tail is demonstrably
  settled before the fixture is written. A settle/partial-line miss would self-heal (re-drop /
  re-read from the byte offset); the signature is a *permanent* loss of a `sync.Once` payload, which
  only the consumer-remove race produces. The "2 envelopes tailed, fixture's 4 lines never in the
  session file" shape is "consumed-then-lost at the consumer," not "nothing arrived."

### The fix — claim before you remove (consumer-only, one function)

Make the consume atomic against concurrent producer writes by **claiming the trigger with an atomic
`os.Rename` before reading it**, so the consumer's later removal targets only the inode it actually
read — never a value a producer wrote afterward.

New contract for `emitStructuredJSONLIfTriggered(f *os.File, path string)` — an ordered sequence, not
a body to transcribe:

1. `claimed := path + ".consuming"` (fixed sidecar in the same dir — single consumer, so no
   uniquification needed; same filesystem, so the rename is atomic).
2. `os.Rename(path, claimed)`. On error (`ENOENT` — no trigger, the steady state) return. **After this
   point the consumer operates only on `claimed`; the shared name `path` is free for producers.**
3. `os.ReadFile(claimed)`.
4. **Empty claim** (`len==0`): this is a producer mid-`O_TRUNC` write (the trigger existed but wasn't
   written yet when we claimed it). `os.Rename(claimed, path)` to hand the inode back under the shared
   name so the producer's pending write stays reachable, then return. The next 50 ms poll consumes the
   completed content. (This is the #958 "leave it for the next poll" behaviour, now expressed through
   the claim — which is why the existing "empty is skipped, not removed" subtest still passes.)
5. Cap at `assistantMaxBytes`, `f.Write(data)`, `f.Sync()` (unchanged).
6. `os.Remove(claimed)` — removes exactly the inode read in step 3.

Keep the read-error branch defensive (`os.Remove(claimed)` then return). The doc comment must be
rewritten to explain the claim (and note it subsumes the #958 empty gate).

**Why claim-then-remove is deterministic** (walk every interleaving of the fatal `dropFull` write vs.
the consumer poll — the kicker is already stopped and joined, so `dropFull`'s inode `X` is the sole
producer-touched inode):

- Consumer claims **before** `dropFull` opens `path`: `dropFull`'s `O_CREATE` makes a fresh inode
  under `path`; next poll claims and consumes it. No loss.
- Consumer claims **between** `dropFull`'s open and write (empty claim): step 4 renames the inode back
  under `path`; `dropFull`'s `write` lands in it; next poll consumes it. No loss. (No second producer
  can orphan it during the rename-back because the kicker is joined — this is why `dropFull`'s
  `<-kickerDone` is load-bearing.)
- Consumer claims **after** `dropFull`'s write: claims the complete fixture; consumes it. No loss.
- A prior cycle consuming a **kicker line** races `dropFull`: that cycle only ever `os.Remove`s
  `claimed` (the kicker inode), never `path`; `dropFull` writes a *different* inode under `path`,
  untouchable by the remove. No loss.

In all cases the consumer removes only `claimed`, so it can never unlink a fixture written to `path` —
the TOCTOU is closed by construction. Producers stay as plain `os.WriteFile`; **no test file is
modified** (the kicker/`dropFull`/vacuous-guard code is untouched).

### Rejected alternatives

- **Timeout bump** — forbidden by AC3; masks, does not fix.
- **Atomic-mailbox (producers write temp+rename AND consumer claims)** — also correct, but touches 3
  producer sites across 2 test files and breaks the "empty is skipped" subtest (atomic producers
  never present empty). Strictly more surface than the consumer-only claim, which already subsumes the
  empty case via rename-back. Rejected on [simplicity-first] + minimal-seam grounds — same reasoning
  #958 used to prefer the single consumer seam.
- **Idempotent re-drop of the full fixture** (kicker-style self-heal for the fixture) — fabricates
  multiple `turn_end`s / re-opened turns, muddies the single-turn model, and is retry-not-determinism.
  Rejected.
- **Newline-terminal "complete content" gate** — the #958-reserved escalation for *torn non-empty*
  reads; not needed (no partial read observed; the claim closes the real bug). Do not reach for it
  unless instrumentation surfaces a genuine partial read.

---

## Concurrency model

- **Producers** (test, unchanged): the `kicker` goroutine (`os.WriteFile` every 250 ms; self-heals on
  loss) and `dropFull` (`sync.Once`, writes once *after* `stopKicking()` + `<-kickerDone`, so it never
  overlaps the kicker). Both target the shared name `jsonlTrigger`.
- **Consumer** (fakeclaude, the fix): single main goroutine, one `emit` call per 50 ms poll, serial.
  Each call completes claim → read → (append/remove | rename-back) before the next, so `claimed` is
  always absent at a cycle's start (no stale-sidecar handling needed).
- **Daemon tail** (`resolveOwnBootstrapJSONL`): separate process, tails the session JSONL from EOF
  after ~500 ms settle. Out of the fix's path; the `f.Sync()` remains its cross-process visibility
  fence.

---

## Error handling

- `os.Rename(path, claimed)` `ENOENT` → no trigger present (steady state) → return, no-op.
- Empty claim → rename back, return (next poll retries). Deterministic; never drops the `sync.Once`
  fixture.
- The only residual loss possible is a **kicker `midTurnLine`** orphaned if two successive kicker
  writes straddle a rename-back — non-observable because the kicker re-drops every 250 ms and the loss
  never touches the fixture. Note this in the function comment; do not defend against it (no observed
  failure; [evidence-based fix selection]).
- All filesystem errors stay best-effort/silenced as today — the e2e asserts downstream (A receives
  the envelopes); the untagged contract test pins the in-process invariant.

---

## Testing strategy

**Reproduce + instrument (AC1 — an identified cause, not a hypothesis):**

- Reproduce red under the **full** `-tags e2e -race` suite as `make check` runs it (the repro is
  contention-sensitive — running the test in isolation may pass; run the full `internal/e2e` package,
  add `-count` if needed to hit it). First run builds fakeclaude.
- Instrument `emitStructuredJSONLIfTriggered` (stderr from the child) to log, per consume, the bytes
  read and — right before removal — a re-stat of the trigger size, flagging any cycle that is about to
  remove a trigger whose size grew under it (kicker-line read → fixture-sized at remove). That
  re-stat-grew log line is the smoking gun for the read→remove TOCTOU. Confirm the session JSONL never
  gains the fixture's 4 lines in a failing run. Remove the instrumentation before committing (or gate
  it behind an env flag the test never sets).

**Deterministic regression net (untagged contract test — different fabric from the stochastic e2e,
per [belt-and-suspenders]):** add ONE subtest to `jsonl_trigger_test.go` pinning the destroy-newer
invariant, deterministically (sequential, no timing). Scenario:

- Write content **A** to the trigger.
- Drive the consume up to the point *after* it has claimed (renamed) A but *before* it removes — then
  write content **B** to the trigger (the shared name) — then let the consume finish.
- Assert: the session file grew by **A** (A was consumed), and the trigger still holds **B** (B
  survived, was NOT destroyed), and a second `emit` consumes **B**.
- The developer chooses the seam that makes "between claim and remove" testable in-process (extract an
  unexported `claim(path) (claimedPath string, ok bool)` helper and drive it directly, or an
  `afterClaim func()` test hook defaulting nil). Prefer the extracted helper — it also makes the
  consumer read as claim → consume.
- Keep the three existing subtests green unchanged (the claim + rename-back preserves their
  semantics).

**Prove the fix (AC2, AC3):**

- `TestTwoPhoneStructured_InteractiveReceivesStream` green under the **full** `-tags e2e -race` suite
  (not isolation), and green under `-count=5` (or the suite's repeated-run form) — deterministic, not
  a lucky single green. Record the run counts + output in the PR body.

**Vacuous-pass guard (AC4):** the fix touches only fakeclaude; A's "observe the full live set
(`turn_state`/`assistant_delta`/`tool_use`/`turn_end`) before B's negative" and B's "no structured,
no coarse `message`" assertions in `relay_two_phone_structured_test.go` are byte-identical. State this
explicitly in the PR body.

**Sibling (non-binding):** re-run `TestRelayV2_InterruptStopsRunningTurn` after the fix. Its
`turn_end` arrives via ESC→`appendTurnEnd` (not the trigger), so if it stays red its root cause is
likely the daemon tail-settle window — **file a separate follow-up; do not expand this ticket's
scope.**

---

## AC1 documentation — deliberate redirect

AC1 asks for the diagnosis in `spec + docs/knowledge/codebase/984.md`. `docs/knowledge/codebase/984.md`
is **documentation-phase-owned** and is written post-merge from this spec + the merged diff; making it
a developer deliverable pushes a fixed-cost doc into the implementation turn budget (the #471/#478
`max_turns` trap), exactly as #958 handled it. So: **this spec** carries the design-time diagnosis, the
**developer records the reproduced-under-instrumentation confirmation + repeated-green evidence in the
PR body**, and documentation writes `codebase/984.md` after the PR merges. Do not add a `codebase/984.md`
AC to the developer's worklist.

---

## Security

Not security-sensitive (label absent; PO classified it so). This is a timing/determinism fix in a
test harness: no change to any outbound-delivery policy, inbound-content parse, capability gate, or
Noise nonce/key. The #632 capability-gated emitter and the vacuous-pass oracle are untouched — the
harness only makes the structured stream *reliably produce*; the security guarantee A/B prove is
unchanged. Per the label-gated rule, no `## Security review` pass is required.

---

## Open questions

- **Test seam for the destroy-newer subtest** — extracted `claim` helper vs. `afterClaim` hook. Left
  to the developer; the extracted helper is recommended (improves readability, no prod-facing test
  param). Not a blocker.
- **Sibling root cause** — whether `TestRelayV2_InterruptStopsRunningTurn` shares this race or is a
  daemon tail-settle issue. Resolve by re-running after the fix; file a follow-up if it diverges. Out
  of scope here.
