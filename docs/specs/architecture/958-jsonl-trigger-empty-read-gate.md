# Spec #958 — Close the fakeclaude JSONL-trigger empty-read race (structured-stream e2e flake)

**Ticket:** [#958](https://github.com/pyrycode/pyrycode/issues/958) · **Size:** XS (architect override S→XS — the diagnosed fix is a single consumer-side gate) · **Security-sensitive:** No (label absent; harness-timing fix, no new trust surface — see § Security posture)

## Files to read first

Turn-1 reading list. Read these before touching anything.

- `internal/e2e/internal/fakeclaude/main.go:423-448` — `emitStructuredJSONLIfTriggered`, **the consumer to fix**. Note the fatal sequence: `os.ReadFile` → `f.Write(data)` → `f.Sync()` → **`os.Remove(path)`**. A zero-byte read still reaches the `os.Remove`, destroying the fixture.
- `internal/e2e/internal/fakeclaude/main.go:394-400` — the single poll-loop call site (~50 ms `pollInterval`). Context only; unchanged.
- `internal/e2e/relay_two_phone_structured_test.go:279-310` — the producer side: a 250 ms **kicker** goroutine and a once-only **`dropFull`**, both `os.WriteFile(jsonlTrigger, …)`. `dropFull` is the load-bearing write whose loss is the observed flake.
- `internal/e2e/relay_two_phone_structured_test.go:319-371` — `failVacuous` (the **exact observed error**), the `required`/`structuredTypes` sets, and the "A observed before B's negative" ordering. **All must stay byte-identical** (AC: security oracle preserved).
- `internal/e2e/relay_v2_interrupt_test.go:205-219` — the interrupt test's kicker: **same** `os.WriteFile` producer, **same** consumer. Confirms one consumer gate fixes both tests.
- `internal/e2e/internal/fakeclaude/esc_detect_test.go` — the **untagged** unit-test idiom (`package main`, no `//go:build`), runs in the default `go test ./...` gate. The new gate test mirrors this.
- `internal/e2e/internal/fakeclaude/main_test.go:1` — note it is `//go:build e2e`-tagged (it builds+runs the binary). The new test is **not** in this file; it calls the function in-process, so it stays untagged.
- `docs/knowledge/codebase/642.md` § "Code-review NITs" (lines ~219-227) — the deferred NIT that predicted this exact flake ("`os.Rename`-from-temp … if CI ever shows it"). CI has now shown it.
- `docs/knowledge/codebase/956.md` — the direct precedent in the same race family: the `waitForRotatedJSONL` consumer non-empty (`Size()>0`) poll gate over a write-then-rename producer. Mirror its shape.

## Context

`TestTwoPhoneStructured_InteractiveReceivesStream` and `TestRelayV2_InterruptStopsRunningTurn` intermittently fail under the full-suite `-tags e2e -race` leg of `make check` (observed on QA of PR #957, 2026-07-15). Both drive `fakeclaude` through the **same** JSONL-trigger consumer, so they are two witnesses of one root cause. Isolated re-runs pass; only full-suite `-race` contention surfaces it. It rots undetected between full-suite runs because neither CI's untagged gates nor QA's default gates run `-tags e2e`.

This closes a NIT the #642 architect explicitly deferred, and is the same create-before-content race family that #956/#957 just closed in `waitForRotatedJSONL`.

## Diagnosis (design-time; developer must reproduce before fixing — AC "diagnose first")

**Mechanism: the `O_TRUNC` empty-read-then-remove race in the consumer.** Confirmed by static reading of both sides:

1. Every producer drop is `os.WriteFile(jsonlTrigger, payload, 0o600)` = `open(O_CREATE|O_TRUNC|O_WRONLY)` **then** a single `write(2)`. Between the open (which truncates to 0 bytes) and the write, the file exists but is **empty**.
2. The consumer polls every ~50 ms. If its `os.ReadFile` lands in that window, `err == nil` and `data == []` (the existing `if err != nil` guard does **not** catch it). It then runs `f.Write([])` (appends nothing), `f.Sync()`, and **`os.Remove(path)`** — unlinking the trigger before the producer's content was ever visible. The producer's subsequent `write` goes to the now-unlinked inode and is lost.
3. In the structured test the kicker is self-healing (re-drops every 250 ms), but **`dropFull` is `sync.Once`** — losing it means A never receives `tool_use`/`turn_end`. That is exactly the reported signature: `observed 2 structured envelope(s); missing required types [tool_use turn_end]`. Under `-race` + full-suite contention the open→write window stretches, widening the race.

Why the two secondary hypotheses from the ticket are **not** the fire here (do not fix them):
- **Cold-start producer-subscribe re-race** — already handled by the pre-created `<initialUUID>.jsonl` + the re-drop kicker (`relay_two_phone_structured_test.go:143-153, 267-300`). It would zero out A's set entirely, not leave a `turn_state`-only partial.
- **Deadline starvation under `-race`** — the observed message reports a *partial* set (2 envelopes seen), i.e. the fixture was consumed-then-lost, not "nothing arrived in time." A widened deadline is explicitly banned by AC and would not close a lost fixture.

**Developer obligation:** establish the pre-fix reproduction under contention (repeated `go test -tags e2e -race -run 'TestTwoPhoneStructured|TestRelayV2_InterruptStopsRunningTurn' ./internal/e2e/`, or the full e2e package) and confirm the failure signature is the partial/lost-fixture mode above **before** applying the fix. If — against expectation — reproduction shows a *non-empty partial* read (a torn line), escalate to the § Fix "complete-content" fallback; do not silently switch defenses.

## Design

### The fix — one consumer-side gate (primary)

In `emitStructuredJSONLIfTriggered` (`main.go:435`), after the successful `os.ReadFile`, **skip a zero-byte read without removing the trigger**. Contract:

- `os.ReadFile` error → `return` (unchanged; trigger absent is the steady state).
- **`len(data) == 0` → `return` without `os.Remove`.** Leave the trigger in place; the producer's `write` completes microseconds later and the next ~50 ms poll consumes the full content. This is the entire fix — one `if` plus an explaining comment (~4 LOC).
- Otherwise → cap, `f.Write`, `f.Sync`, `os.Remove` (unchanged).

Why this is correct and sufficient:
- An empty trigger is **never** a legitimate payload (fakeclaude appending zero bytes is a no-op regardless), so skip-and-retry can never drop real data or hang.
- It covers **both** tests — one edit at the shared consumer — including the load-bearing `dropFull`: an empty read now leaves the fixture for the next poll instead of destroying it.
- Zero coupling to payload format; deterministic; no widened deadline, no `time.Sleep`.

**Rejected — producer-side atomic write (write-temp + `os.Rename`).** The AC allows it, but it must be applied at **every** drop site (structured kicker + `dropFull`, interrupt kicker = 3 edits across 2 files) and re-derived for any future site. The consumer gate is one edit that structurally covers all present and future producers. Prefer the single seam (`[[Simplicity First]]`).

**Rejected — newline-terminal "complete-content" gate.** A `data[len(data)-1] != '\n'` check would also skip a torn partial line, but it couples the consumer to a "every payload ends with `\n`" invariant and would **silently hang** on any future drop site that violates it. Torn non-zero reads from a single sub-page `write(2)` to a regular file are not practically observable and have **not** been observed (`[[Evidence-Based Fix Selection]]`). Reserve this shape **only** as the escalation path if reproduction surprisingly shows a non-empty partial read.

### The deterministic regression net — one untagged unit test (belt-and-suspenders, different fabric)

The ≥20 contention e2e runs (AC) are probabilistic proof over the real cross-process path. Pair them with a **deterministic** in-process unit test of the gate, in a **new untagged** file `internal/e2e/internal/fakeclaude/jsonl_trigger_test.go` (`package main`, **no** `//go:build` tag — mirrors `esc_detect_test.go`). It runs in the default `go test ./...` gate, closing the CI-coverage gap #642 flagged (the e2e tests never compile upstream of code-review). It calls `emitStructuredJSONLIfTriggered` directly — no binary build, no harness.

Different fabric: the e2e runs exercise the live path under contention; the unit test pins the exact function contract deterministically where the e2e can't reach.

## Concurrency model

Unchanged. `emitStructuredJSONLIfTriggered` still runs only on the main poll goroutine (sole writer of `f`); the fix adds no goroutine, channel, or shared state. The producer-side kicker/`dropFull` goroutines in the tests are untouched.

## Error handling

The gate is the error-handling change: it reclassifies "read succeeded but returned empty" from "consume + remove" (destructive) to "not ready yet, retry next poll" (non-destructive). All other error paths (read error, write error) keep their existing silent-return posture — the e2e asserts downstream.

## Testing strategy

**Deterministic unit test** (new untagged file; scenarios, not full bodies):

- **Empty trigger is skipped, not removed.** Create a temp session `f` (`os.OpenFile … O_APPEND|O_CREATE`) and a temp trigger path written with **zero bytes** (`os.WriteFile(path, nil, 0o600)`). Call `emitStructuredJSONLIfTriggered(f, path)`. Assert: (a) the trigger **still exists** (`os.Stat` succeeds), and (b) `f` did **not** grow (size 0 or unchanged from its `openSession`-style seed). This is the exact bug — pre-fix it fails (trigger removed).
- **Non-empty trigger is consumed and removed.** Write real claude-format JSONL bytes to the trigger, call the function, assert: (a) the trigger is **removed** (`os.IsNotExist`), and (b) `f`'s on-disk content now contains the bytes.
- **Missing trigger is a no-op.** Point at a non-existent path, call, assert no panic and the trigger stays absent (steady state).

**Contention verification (AC — the real proof).** The developer establishes the pre-fix reproduction, then demonstrates **≥20 consecutive** full-e2e-package contention runs with **zero** failures of either test, e.g.:

```
for i in $(seq 1 20); do go test -tags e2e -race -count=1 \
  -run 'TestTwoPhoneStructured_InteractiveReceivesStream|TestRelayV2_InterruptStopsRunningTurn' \
  ./internal/e2e/ || { echo "FAIL run $i"; break; }; done
```

Run the whole e2e package (not `-run`-isolated only) at least once too — the flake surfaces under package contention, which isolation does not reproduce.

**Standard gates:** `go build ./cmd/pyry`, `go vet ./...`, `staticcheck ./...`, `go test -race ./...` (the new untagged unit test runs here), `go vet -tags e2e ./internal/e2e/...` (compiles the e2e files).

## What must NOT change (security-oracle preservation — AC)

The fix touches only the harness's *production* of the structured stream. Leave every assertion intact:
- `structuredTypes` / `required` sets and the four-required-types gate (`turn_state`/`assistant_delta`/`tool_use`/`turn_end`).
- The vacuous-pass guard (`failVacuous`) and the "A observed **before** B's negative" ordering.
- B receives **zero** structured envelopes; the interrupt test's two ordered `t.Fatal` guards and AC2 stdin-log oracle.

No `pyry` runtime change: `cmd/pyry` and `internal/*` non-test code are untouched. Only `internal/e2e/internal/fakeclaude/main.go` (a test-only stand-in) and one new test file change.

## Security posture

Not security-sensitive; the `security-sensitive` label is absent and this run does **not** run the security-review pass. Rationale (`[[security-sensitive-label-tracks-design-not-lineage]]`): the change is a harness-timing fix in a test-only stand-in. It introduces no nonce/token/capability primitive, no inbound-content parsing, no outbound policy decision, and no new trust boundary. It makes the harness reliably *produce* the structured stream; it does not touch the #607/#632 capability gate the tests assert on — that oracle is explicitly preserved unchanged (above). That a security-property *test* flakes does not make its harness fix security-sensitive.

## Scope self-check (XS confirmed)

- Production-source files modified: **1** (`internal/e2e/internal/fakeclaude/main.go`, ~4 LOC). New files: **1** (untagged unit test). New exported types: **0**. Consumer call-sites updated: **0** (single-site behavior change, no signature change → no fan-out). `pyry` runtime change: **0**.
- No edit cascade (the two e2e test files are **not** modified — the shared consumer gate covers both). No branch overlap (checked at architect time; no `feature/<N>` sibling touches these files). Under every red line — no split.

## Diagnosis-record placement (deliberate deviation from AC1's literal wording)

AC1 says record the diagnosis in `docs/knowledge/codebase/958.md`. Per architect policy, `docs/knowledge/codebase/<N>.md` is **owned by the documentation phase**, which writes it post-merge from this spec + the merged diff; making it a developer deliverable pushes a fixed-cost housekeeping task into the implementation turn budget (worked examples #471/#478 hit `max_turns` with the knowledge doc half-written). Therefore:

- The **developer records the diagnosis + pre-fix reproduction evidence + the ≥20-run result in the PR body** (satisfying AC1's intent: diagnose first, record the mechanism, don't fix an unobserved mechanism).
- This spec's § Diagnosis is the design-time hypothesis the developer confirms.
- `docs/knowledge/codebase/958.md` is written later by documentation — **not** a developer AC here.

## Open questions

- **Residual `dropFull` δ-window (unobserved, not fixed).** A theoretical narrow race remains where the consumer reads a *stale full* kicker line, then between its read and its `os.Remove` the (already-stopped-kicker-free) `dropFull` truncates+writes the fixture, so the remove drops the fixture. This is orders of magnitude narrower than the open→truncate→empty window the fix closes, is not the observed mode, and `dropFull` runs only after `stopKicking()` + `<-kickerDone`. Per `[[Evidence-Based Fix Selection]]`, do not build for it. If the ≥20-run verification still flakes on this signature (fixture written but immediately removed), escalate to producer-side atomic `os.Rename` for `dropFull` only.
