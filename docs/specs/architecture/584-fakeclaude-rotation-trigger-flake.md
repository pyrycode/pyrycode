# Spec #584 — Fix flaky `TestFakeClaude_OpensInitialAndRotatesOnTrigger` (trigger-removal races rotated-JSONL creation)

**Size:** XS · **Security-sensitive:** no (label absent) · **Chosen fix:** Option 2 (test-only poll-loop tightening)

## Files to read first

- `internal/e2e/internal/fakeclaude/main_test.go:62-93` — **the only edit site.** The poll loop (62-86) breaks the instant the rotated `<uuid>.jsonl` appears; the single-shot `os.Stat(triggerPath)` assertion (91-93) then races. This is what changes.
- `internal/e2e/internal/fakeclaude/main_test.go:182-199` — `waitForFile` / `signaledBy` helpers already in this file. No new helper is needed; the fix is inline in the existing loop.
- `internal/e2e/internal/fakeclaude/main.go:147-156` — the rotation block: `f.Close()` → `openSession(dir, newU)` (observable) → `os.Remove(trig)`. **Read-only.** This confirms the window the test must tolerate: the rotated JSONL becomes observable *before* the trigger is removed. Production code is **not** modified under the chosen option.
- `internal/e2e/internal/fakeclaude/main.go:1-7` — package doc-comment asserting the "close-OLD-fd-before-open-NEW-fd" invariant (AC-3). Confirm it stays intact; since production is untouched, it does. No prose edit.

## Context

`TestFakeClaude_OpensInitialAndRotatesOnTrigger` passes in isolation but flakes under the full parallel `-tags e2e` load with:

```
main_test.go:92: trigger file still present after rotation: err=<nil>
```

fakeclaude's rotation performs two observable filesystem operations in order — open the rotated `<uuid>.jsonl`, *then* remove the trigger (`main.go:152-153`). The test polls `os.ReadDir(sessionsDir)`, breaks the moment the rotated JSONL appears, then immediately asserts once that the trigger is gone. Between `openSession` and `os.Remove(trig)` there is a window where the rotated JSONL exists but the trigger has not yet been removed. The test's implicit assumption — *"rotated JSONL observed ⇒ trigger already consumed"* — is false, so the single-shot assertion races.

## Design decision: Option 2 (test-only), not Option 1 (reorder fakeclaude)

The ticket offers two mutually exclusive fixes and leaves the choice to the architect. **Chosen: Option 2** — fold the trigger-gone check into the existing poll loop's break condition. Rationale:

1. **The bug is in the test, so fix the test.** The root cause is the test's false assumption about operation ordering, not a defect in fakeclaude. The double never contracted that `os.Remove(trig)` happens-before the rotated JSONL is observable — only that the OLD fd closes before the NEW fd opens (the doc-comment invariant). Option 2 removes the false assumption at its source.

2. **Robust to *any* ordering, not just one.** A black-box, cross-process test double should be observed by its *post-conditions*, not by the internal ordering of its steps. Option 2 polls for the actual end state (rotated JSONL present **and** trigger absent) regardless of the order fakeclaude does the two operations. Option 1 instead imposes a new incidental contract ("remove-before-open") that a future refactor of the poll loop in `main.go` could silently break — re-introducing this exact flake. Option 2 is immune to that.

3. **Strictly smaller surface (Simplicity First).** Zero production change, no cross-process behaviour change, no doc-comment prose to keep in sync. One test function, one loop. The ticket's own blast-radius note confirms Option 2 is "trivially local" and has "the strictly smaller surface."

4. **AC-3 preserved for free.** The close-OLD-before-open-NEW invariant lives entirely in untouched production code.

## The change

Restructure the existing poll loop (`main_test.go:62-89`) so it breaks **only when both** post-conditions hold, keeping the two post-loop assertions (lines 87-93) verbatim as the timeout diagnostics.

Contract of the new loop (≤ ~15 lines, replaces lines 65-86):

- Guard the directory scan with `if rotatedUUID == ""` so it runs only until the rotated stem is found (the file persists; no need to re-scan).
- The scan itself is unchanged: skip non-`.jsonl`, skip `initialUUID`, require the `uuidStem` regexp match, set `rotatedUUID`.
- Break **only** when `rotatedUUID != ""` **and** `os.Stat(triggerPath)` returns `os.IsNotExist` — i.e. the rotated JSONL exists *and* the trigger has been removed.
- `time.Sleep(50 * time.Millisecond)` between iterations (unchanged cadence).

The two post-loop assertions stay exactly as they are today:

- `if rotatedUUID == "" { t.Fatalf("no rotated JSONL appeared ...") }` — still fires when rotation never happens.
- `if _, err := os.Stat(triggerPath); !os.IsNotExist(err) { t.Fatalf("trigger file still present after rotation: err=%v", err) }` — now correct: it only fires on **timeout** with the rotated JSONL present but the trigger never removed, which is a genuine fakeclaude regression (AC-1's real-regression signal is preserved), not the pre-existing race.

Note the shared 3-second `deadline` already bounds both the "rotation happened" and "trigger removed" waits, so no new timeout constant is introduced.

## Concurrency model

None new. Single test goroutine polling the filesystem of a separate fakeclaude process. The fix narrows *when* the loop is allowed to exit; it adds no goroutines, channels, or shared state.

## Error handling / failure modes after the fix

- **Rotation never happens** (fakeclaude broken / never sees trigger) → loop times out with `rotatedUUID == ""` → existing "no rotated JSONL appeared" fatal. Unchanged.
- **Rotated JSONL appears, trigger never removed** (genuine regression) → loop times out with `rotatedUUID != ""` and trigger present → existing "trigger file still present after rotation" fatal. This is now a *real* signal, not a race artifact.
- **Normal path** (rotation completes) → loop observes both conditions within the deadline and breaks cleanly; both post-loop assertions pass; the test proceeds to the SIGTERM/exit checks (lines 95-107, unchanged).

## Testing strategy

- The changed test *is* the verification. Confirm the race is closed by running the full parallel suite repeatedly:
  - `go test -tags e2e -race -count=20 -run TestFakeClaude ./internal/e2e/internal/fakeclaude/` — no "trigger file still present after rotation" failures (AC-1).
  - `go test -tags e2e ./internal/e2e/...` green under the full parallel load.
- No new test is added; the two sibling rotation-exercising tests (`rotation_test.go`, the package's `fakeclaude_test.go`) are untouched and do not assert on trigger lifetime, so there is nothing else to update (blast-radius verified at refinement).
- `go vet ./...` and `gofmt` clean. (Heads-up: the repo can read gofmt-dirty at HEAD under a newer local Go than CI's pinned toolchain — only reformat lines this change actually touches; do not sweep unrelated files.)

## Acceptance criteria mapping

- **AC-1** (deterministic pass under full parallel suite) → the poll loop no longer exits on a partial post-condition; the `-count=20` run above is the proof.
- **AC-2** (happens-before: rotated JSONL observable ⇒ trigger already removed) → satisfied on the *test's* side by requiring trigger-absent in the break condition; the test now waits for the guarantee instead of assuming it.
- **AC-3** (close-OLD-before-open-NEW invariant intact) → production `main.go` is not modified; invariant and its doc-comment are untouched.

## Open questions

None. Single-function, test-only change with all three ACs mechanically satisfied.
