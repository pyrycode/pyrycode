# #1758 — Drop slog's timestamp from the ThinkingProgress kind-log capture

**Ticket:** [#1758](https://github.com/pyrycode/pyrycode/issues/1758) · **Size:** XS · **Labels:** `bug`, `security-sensitive`

## Files to read first

| Where | Symbol / section | What to extract |
|---|---|---|
| `cmd/pyry/interactive_turn_v2_test.go` | `TestInteractiveTurnEmitterV2_ThinkingProgressEventKindNamesTheVariant` | The only test that changes. Note its capture setup (a local `bytes.Buffer` behind its own `slog.NewTextHandler`, **no** `ReplaceAttr`) and its readings loop over `{"184", "37"}`. |
| `cmd/pyry/interactive_turn_v2_test.go` | `TestInteractiveTurnEmitterV2_RateLimitedEventKindNamesTheVariant` | The `ReplaceAttr` closure to copy **verbatim**, and the measurement comment above it — the repo's only record of the collision. One clause of that comment is corrected (see § Design). |
| `cmd/pyry/interactive_turn_v2_test.go` | `TestInteractiveTurnEmitterV2_ModelAnnouncedEventKindNamesTheVariant` | The second existing copy of the same closure — confirms the shape is settled idiom, not a one-off. **Not modified by this ticket.** |
| `cmd/pyry/interactive_turn_v2.go` | `interactiveTurnEmitterV2.Handle` | The empty-cursor Debug drop that produces the captured record: `msg` + `event=interactive_turn.no_cursor` + `kind=eventKind(ev)`. Confirm for yourself that none of those three carries a digit — that fact is what makes the fix complete. |
| `cmd/pyry/interactive_turn_v2.go` | `eventKind` (the `turnevent.ThinkingProgress` arm) | The mutation target for AC 2 and AC 3. Its comment records why the arm is content-free ("both fields are ints, and neither is returned") and why it exists for the ACP call sites rather than this lane's default. |
| `docs/knowledge/features/turnbridge-package.md` | § bullet *"A 'value never reaches a log' test needs a positive control that the value traversed the path at all"* | Why the `kind=thinking_progress` positive assertion must stay. Without it, an arm that silently dropped the event would pass the negatives for the wrong reason. |
| `CODING-STYLE.md` | § "Comments — Citing Other Code" | You are writing new comment lines. `make cite-guard` is diff-scoped, so **your** new lines are checked: name symbols, never `file.go:NNN`, never a bare `:NNN`, no range exemption. |

## Context

`TestInteractiveTurnEmitterV2_ThinkingProgressEventKindNamesTheVariant` goes red on roughly 5% of runs, and on 100% of runs whose log instant lands in any minute `:37` or any second `:37`. The cause is deterministic and is **not** the order-dependence originally hypothesised on PR #1757:

- The test's negative is a `strings.Contains` over the **whole** captured record, and `slog.TextHandler` writes its own `time=` attribute first.
- The needle `37` matches the timestamp's digits — in the captured failure, the fractional `.378` of `2026-08-25T00:12:31.378+03:00`. The daemon leaked nothing.
- The drop log itself has no digits: `Handle`'s no-cursor branch logs exactly `msg`, `event=interactive_turn.no_cursor` and `kind=eventKind(ev)`. Remove the timestamp and the only remaining way either needle can match is a genuine leak.

The remedy already exists two hundred lines down the same file — both sibling kind-log tests pass a `ReplaceAttr` that drops `slog.TimeKey`, and the comment above the rate-limited one cites *this* test as the measured collision and settles the posture: *"Removing the attr deletes the false-positive source outright rather than choosing needles around it."* It was written down and never applied to the one test carrying the hazard. This ticket applies it, and adds the assertion that makes the guard's own disarming deterministically red.

The needles cannot simply be dropped or loosened. The arm is content-free by design and the numeric negative is this test's discriminating half — #1404's notes record an equivalent `"rate_limited:" + Status` mutant surviving a whole-suite green where such a negative was absent. A "fix" that deletes or relaxes the readings check would stop the flake and gut the guard.

**No ADR is warranted.** The posture ("delete the false-positive source rather than choose needles around it") is already recorded in code and is being applied, not decided. The documentation phase may want the corrected arithmetic in `docs/knowledge/codebase/1758.md`; that is its call, not a developer deliverable.

## Design

Test-only, one file: `cmd/pyry/interactive_turn_v2_test.go`. **No production code moves.** Three changes, in order of importance.

### 1. Drop the timestamp from this test's capture handler

In `TestInteractiveTurnEmitterV2_ThinkingProgressEventKindNamesTheVariant`, add a `ReplaceAttr` field to the existing `slog.HandlerOptions`. **Copy the closure verbatim** from `TestInteractiveTurnEmitterV2_RateLimitedEventKindNamesTheVariant` — do not re-derive it.

The `len(groups) == 0` half of that predicate is load-bearing and must survive the copy. A closure that dropped any attribute keyed `time` at any depth would silently delete a genuine leaked attribute that happened to carry that key inside a group, turning the leak guard blind in exactly the direction this test exists to prevent. The predicate must stay narrowed to slog's own top-level `TimeKey`.

The closure captures nothing and reads only its arguments, so it is race-free under `t.Parallel()` and under a handler shared across goroutines.

### 2. Assert the timestamp is gone (the deterministic half)

Configuration alone gives AC 1 no red: strip the `ReplaceAttr` back off and the test still passes on ~95% of runs. Add an explicit negative so the guard's disarming is caught on **every** run:

```go
// slog's own time= is the digit source that made this test's numeric needles
// flaky; the readings check below is only sound while it is absent.
if strings.Contains(logs, "time=") {
    t.Fatalf("capture carries slog's timestamp; the readings check below can match the clock:\n%s", logs)
}
```

**Placement matters:** after the `kind=unknown` check and **before** the readings loop. A failure then reports the actual cause — the guard is disarmed — instead of a confusing "reading leaked" message pointing at the clock.

`time=` cannot appear as a substring of anything else in this record: neither the message text, nor `event=interactive_turn.no_cursor`, nor `kind=thinking_progress`, nor `level=DEBUG` contains it. `AddSource` is not set, so there is no `source=` attribute either. After the fix the whole record is `level=DEBUG msg=… event=… kind=thinking_progress` — no digits at all.

This is the belt-and-suspenders split the pipeline asks for, in different fabric: the `ReplaceAttr` is the fix, the assertion is the deterministic gate on the fix staying applied.

### 3. Correct the rate figure in the sibling's measurement comment

The comment above `TestInteractiveTurnEmitterV2_RateLimitedEventKindNamesTheVariant`'s handler options says the `37` needle hits the timestamp in *"~2% of runs on this host."* That figure undercounts and this ticket proves it wrong, so it must not survive next to a fix that explains itself. Against the format `2026-08-25T00:12:31.378+03:00`, `37` matches when the minute is `37` (1/60), when the second is `37` (1/60), or when the millisecond field contains it (≈1.9%) — **≈5% per run overall**, and 100% of runs landing in any minute `:37` or second `:37`. (`184` adds ≈0.1% via the millisecond field.) A measurement taken inside a single `-count=N` burst shares one minute and usually one second, so it structurally cannot observe the two 1/60 terms — which is exactly how the recorded ~2% arose.

Correct **only that rate clause**, and say that this is a scheduled flake rather than a rare one. Everything else in that comment stays verbatim — in particular the `-1`-shaped-needle / UTC-offset clause and the settled-posture sentence. No assertion, fixture, or handler option in that test changes.

Keep the canonical record where it already is. `TestInteractiveTurnEmitterV2_ModelAnnouncedEventKindNamesTheVariant` already points at it in one hop and is **not** touched; relocating the record would turn that pointer into a two-hop chain. The new comment in change 1 therefore points at the rate-limited test **by symbol name** and adds only the one fact that record cannot carry: that with the attr dropped this particular record has no digits left, so the `184`/`37` needles can now match only a genuine leak.

### What deliberately does not change

- **The readings loop.** Both `184` and `37` stay, and stay as bare numeric needles. They are the discriminating half; AC 2's mutants are the proof they still bite.
- **The positive assertions.** `kind=thinking_progress` present / `kind=unknown` absent are the positive control that the event traversed the path at all — see the `turnbridge-package.md` bullet in the reading list. Removing either would let a silently-dropping arm pass the negatives for the wrong reason.
- **`t.Parallel()`.** The filed shared-state hypothesis is refuted: the buffer is local to the test and nothing else writes into it. The stray `flag provided but not defined` / `Usage of pyry sessions …` lines in the captured failure were other subtests' **stdout** interleaved in the console, not content of the scanned buffer.
- **The two sibling tests' handler options and assertions.** Their needles are alphabetic sentinels plus a 10-digit instant that no timestamp string contains, so they were never exposed. See § Open questions for why they do not get the `time=` assertion.
- **No helper is hoisted.** See below.

### Rejected: hoisting the closure into a shared capture-logger helper

The ticket flags that this would become the file's third identical copy and that hoisting is defensible. It is being declined, and the reasoning is recorded here so it is not relitigated in review:

- The file has **six** capture-logger construction sites at Debug level, only three of which need (or would be safe with) a dropped timestamp. A whole-logger helper serving three of six leaves the file in a worse state than either extreme, and migrating all six is scope well past this ticket's ACs — the other three assert on content this ticket has no mandate to touch.
- Hoisting only the closure into a package-level symbol avoids that objection but still edits two currently-green tests for a benefit no observed failure demands. Three copies of a seven-line stateless closure is not a maintenance failure; the observed failure is one missing copy.
- Pipeline principle, verbatim: *"Skip [the elegance pass] for simple, obvious fixes — don't over-engineer routine work."* Adding the missing copy is that fix.
- A helper would in any case be advisory-strength, not a gate: the next author who writes a bare `slog.NewTextHandler(&buf, …)` inline gets the hazard whether the helper exists or not.

The ticket's real constraint — the measurement must not evaporate — is honoured by leaving the record in place and correcting it, which is strictly safer than moving it into a helper's doc comment.

## Concurrency model

Unchanged. No goroutine is created, and `Handle` is synchronous on the calling goroutine. `t.Parallel()` stays; the `ReplaceAttr` closure is stateless and captures nothing, so it introduces no shared mutable state. `-race` on the existing gate covers it.

## Error handling

Unchanged. No production error path is touched. The only new failure path is a test assertion, whose `t.Fatalf` prints the captured log exactly as the surrounding assertions already do — after the fix that log contains strictly less host information than it does today, since the timestamp is gone.

## Testing strategy

The ACs are pinned by mutants, not by a green run. Run each against the changed tree; my memory of this repo's tooling says a mutation driver that edits the worktree can poison its own baseline, so prefer `go test -overlay=<abs-path json>` — it gives a mandated RED with no worktree writes.

| # | Mutant | Expected | Pins |
|---|---|---|---|
| 1 | Remove the `ReplaceAttr` field from **this test's** `slog.HandlerOptions` | **RED**, on every run, at the new `time=` assertion | AC 1 |
| 2 | `eventKind`'s thinking-progress arm → `return "thinking_progress:" + strconv.Itoa(v.EstimatedTokens)` (needs `switch v := ev.(type)`) | **RED** on the `184` needle | AC 2 |
| 3 | Same arm → append `strconv.Itoa(v.EstimatedTokensDelta)` instead | **RED** on the `37` needle | AC 2 |
| 4 | Same arm → `return "unknown"` | **RED** on both the missing-`kind=thinking_progress` and the present-`kind=unknown` assertions | AC 3 |
| 5 | Unmodified tree | **GREEN** | baseline |

Mutants 2 and 3 must each be run separately: each needle needs to be the sole red for its own field, or the loop covers one reading and free-rides on the other.

**Do not try to demonstrate the flake is gone with a `-count=N` burst.** A burst shares one minute and usually one second, so it cannot observe the two 1/60 terms that dominate the true rate — it is exactly the measurement that produced the wrong ~2% figure being corrected in change 3. Mutant 1 is the demonstration: it is deterministic where the burst is not.

Commands:

```bash
go test ./cmd/pyry/ -race -count=1 -v \
  -run 'TestInteractiveTurnEmitterV2_(ThinkingProgress|RateLimited|ModelAnnounced)EventKindNamesTheVariant'
make check
```

`make cite-guard` runs inside `make check` and is diff-scoped, so it will inspect every comment line you add or modify. Name symbols; no `file.go:NNN`, no ranges, no bare `:NNN`.

## Open questions

1. **Should the two sibling tests get the same `time=` assertion?** Today, removing *their* `ReplaceAttr` reddens nothing — their needles (`qq-status-sentinel`, `zz-limittype-sentinel`, `ZZMODELSENTINELZZ`, `4102444800`) cannot collide with a timestamp, so their guard is unpinned but also unexposed. Adding two more assertions would pin configuration that protects against nothing observed. Left out deliberately as dead weight; a reviewer who disagrees can say so, but it is not an AC and should not be added silently.
2. **Future digit-bearing fields on the drop log.** The `time=` assertion guards slog's own timestamp, not a hypothetical new attribute on the no-cursor Debug that carried digits. Considered and rejected: asserting the whole record equals an expected string would pin it completely but is brittle against slog format changes. The residual risk is acceptable because the drop log's fields are `event` and `kind`, both content-free constants — adding a digit-bearing field there would itself be the posture violation this guard exists to catch.

## Size check

Re-applied to this written spec, per the size-S boundary table:

| Limit | Boundary | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **0** (test-only) |
| Total written work | ≤ 400 lines | **≈35** |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** |
| Acceptance criteria | ≤ 5 | **3** |
| Distinct error/reject branches | ≤ 10 | **0** |

Not refactor-shaped: no signature, type, or interface changes, so there is no edit fan-out to count. `XS` confirmed; no split.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The boundary in this design is claude-authored event content (`turnevent.ThinkingProgress`'s `EstimatedTokens` / `EstimatedTokensDelta`, derived from claude's stream-json output) crossing into log output. It is explicit and single-sited: `eventKind` returns a variant name only and never event content, and the test under change is the guard on that boundary. The change adds a precondition assertion and removes no coverage — both integer fields keep their needles, pinned by mutants 2 and 3 in § Testing strategy.
- **[Trust boundaries]** No finding, but a **load-bearing detail** the spec pins explicitly: the `ReplaceAttr` predicate must keep its `len(groups) == 0` half. A closure that dropped any attribute keyed `time` at any depth would silently delete a genuinely leaked attribute nested inside a group, blinding the guard in precisely the direction it exists to protect. § Design change 1 requires a verbatim copy from `TestInteractiveTurnEmitterV2_RateLimitedEventKindNamesTheVariant` for this reason, not for brevity.
- **[Tokens, secrets, credentials]** Not applicable, by design and not by omission: nothing on this path is a credential. The guarded values are claude-authored telemetry integers. The repo's #833 posture — restated in `internal/relay`'s `v2session_settings.go` and `internal/sessions`' `pool.go` as "model / effort / YOLO values are NEVER logged at any level" — is the neighbouring rule; `eventKind`'s arms extend the same treatment to all event content. No token is created, stored, compared, or rotated here.
- **[File operations]** Not applicable. The change creates no file, builds no path, and writes nothing to disk. The capture target is an in-memory `bytes.Buffer` local to the test.
- **[Subprocess / external command execution]** Not applicable. No `exec.Command`, no environment mutation, no signal handling.
- **[Cryptographic primitives]** Not applicable. No randomness is introduced, and the `strings.Contains` checks compare against fixture values, not secrets — constant-time comparison is not a relevant property here.
- **[Network & I/O]** Not applicable. No socket, no reader, no size cap to set. The captured record is one fixed-shape line produced by a single synchronous `Handle` call.
- **[Error messages, logs, telemetry]** The load-bearing category, and the reason for the label. Three points. (a) **No weakening.** The readings loop and both positive assertions are preserved verbatim; § Design names the "delete or loosen the needles" fix as forbidden and mutants 2–4 are the deterministic proof. (b) **Failure-message content.** The new assertion prints the captured log in its `t.Fatalf`, matching the surrounding assertions; after the change that log carries strictly *less* host-identifying information than today, since the timestamp is gone from CI output. (c) **Residual gap, accepted.** The `time=` assertion guards slog's own timestamp, not a future digit-bearing attribute added to the no-cursor Debug in `Handle`. Asserting whole-record equality would close it but is brittle against slog format changes; the risk is accepted because that log's only fields are `event` and `kind`, both content-free constants, so adding a digit-bearing field there would itself be the violation this guard catches. Recorded in § Open questions rather than hidden.
- **[Concurrency]** No findings. The `ReplaceAttr` closure captures nothing and reads only its arguments, so it is safe even though `slog.HandlerOptions.ReplaceAttr` may be invoked concurrently by a shared handler. `t.Parallel()` is retained; the buffer is local to the test and written only from the test goroutine via a synchronous `Handle`. The filed shared-state hypothesis is refuted in § Design — the interleaved `flag provided but not defined` lines were other subtests' stdout in the console, never content of the scanned buffer. No lock is taken, no goroutine is spawned.
- **[Threat model alignment]** No findings. The relevant posture is #833's never-log rule as extended by `eventKind`'s content-free arms; this ticket preserves it and converts its test from a ~5%-flaky check into a deterministic one. `EstimatedTokens` / `EstimatedTokensDelta` are not in #833's named triple, but the arm's own comment and #1404's notes treat all event content identically — this spec keeps that consistent rather than carving out an exception for integers.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-25
