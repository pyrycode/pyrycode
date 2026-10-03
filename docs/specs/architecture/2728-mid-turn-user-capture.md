# #2728 — capture what claude does with a user message written mid-turn

## Files read

- `internal/e2e/realclaude/subagent_prompt_capture_test.go` → `TestRealClaude_SubagentPromptCapture`, `spcWriteRecord`, `fixtureWorthy`: the lean probe shape to copy (fixture absence arms, force env re-captures, record to an artifact dir that outlives the worktree, promote in-repo only when worthy, loud `t.Errorf` when not).
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapRecorder` (verbatim line splitter, `snapshot`), `newDropcapRedactor` / `addValueClass` (substitution table), `newDropcapScanner` / `addDynamic` / `scan` (fail-closed deny-scan): reused unchanged.
- `internal/e2e/realclaude/context_usage_capture_test.go` → `runCucapChild`: the direct `exec.CommandContext(claudeBin, …)` spawn pattern with a stdin pipe.
- `internal/e2e/realclaude/roster_after_finish_capture_test.go` → `rafcapStageFixture`: best-effort `git add` of the promoted fixture.
- `internal/e2e/realclaude/fixtures.go` → `WithWorktreeAuthenticated` (temp `$HOME`, skip without credentials, seeds `.claude.json`), `realHome`.
- `internal/e2e/realclaude/background_trigger_probe_test.go` → `probeClaudeVersion`; `resilience_test.go` → `resolveClaudeBin`.
- `internal/streamsup/runner.go` → `buildArgs`: production's flag prefix (`--input-format stream-json --output-format stream-json --verbose --include-partial-messages --forward-subagent-text`), which the probe copies so the stream shape matches the daemon's child.
- `internal/streamsup/envelope.go` → `WriteTurn`: production's user-line encoder, used for every probe write.
- `docs/knowledge/features/development-verification.md` § "Captures and live evidence": arm on fixture absence, not on an operator-only switch; record enough to diagnose an empty capture.

## Context

Send now needs to know what claude does with a `user` line written while a turn runs. One manual run says it folds at the next tool boundary; this ticket commits a capture of four arms so the delivery and client-report tickets build on measured bytes. The probe drives `claude` directly; the daemon is not involved.

The live run is the dispatcher's (`needs-real-claude`). Per `handbacks.md` "Live records" the builder adds `needs-live-artifacts`, the gate writes the record to an artifact dir outside the worktree, and the return trip commits it.

## Design

One file, `internal/e2e/realclaude/mid_turn_user_capture_test.go`, build tag `e2e_realclaude`.

**Gate.** `TestRealClaude_MidTurnUserCapture` runs when no file matches `testdata/mid_turn_user_v*.json`, or when `PYRY_PROBE_MID_TURN_USER_CAPTURE=1` forces a re-capture. Plain `make e2e-realclaude` therefore arms it while the fixture is absent.

**Child.** Per arm, a fresh child: `claude -p` + production's `buildArgs` prefix + `--replay-user-messages --model haiku --dangerously-skip-permissions --session-id <arm's fixed uuid>`, cwd a fresh workdir under the temp `$HOME`, `BASH_DEFAULT_TIMEOUT_MS` raised so the 15 s sleep is not cut. Stdout goes to a `dropcapRecorder`; the test goroutine polls its `snapshot` to decide when to write.

**Arms.** Each arm is `{name, opening prompt, trigger, writes}`:

| Arm | Opening turn | Trigger for the write(s) |
|---|---|---|
| tool-boundary | run `sleep 15` once via Bash, then answer | 2 s after the first assistant line carrying a `tool_use` block |
| no-tool | no tools, write a long text-only answer | 1 s after the first `stream_event` (text streaming has begun) |
| after-last-tool | one quick Bash `echo`, then a long text-only answer | the first `user` line carrying a `tool_result` block |
| back-to-back | as tool-boundary | as tool-boundary, two writes back to back |

Every written message carries a unique marker (`MIDTURN-<ARM>-<n>`) and asks claude to include it in its final answer; the opening turn carries `OPENING-<ARM>`. A trigger that does not fire within its wait records the arm as `no-trigger` and nothing is written.

**Ending an arm.** After the writes, wait for a `result` line, then a quiet window (no new line for 20 s) so a second turn the write opened is caught; bounded by a per-arm budget. Then close stdin and wait for exit, killing on the context deadline.

**Record, per arm** (`mtuArm`):
- `sequence`: every stdout line in order as `{type, subtype?, blocks?}` with consecutive identical entries run-length collapsed (`count`), and each probe write inserted as `{write: marker}` at the line count observed when it was written.
- `echoes`: every `user` line whose text holds a probe marker, as `{index, markers, line}` with `line` the redacted verbatim line.
- `result_count`, and per `result` line the markers its `result` text holds.
- `final_text_markers`: marker → whether the last `result` text holds it.
- `verdict`: `folded` (one result, its text holds every mid-turn marker), `second-turn` (more than one result), `not-read` (one result, no mid-turn marker), `partial` (one result, some markers), `no-result`, `no-trigger`.
- `terminated_on`, `seconds`, `exit` (the wait error, redacted).

Top level: ticket, claude version, captured_at, model, argv (redacted), redaction substitutions, scan-applied map, limitations.

**Redaction.** One `dropcapRedactor` over the temp home, artifact dir and workdir, with every arm's session id added via `addValueClass`, plus the account identifiers (`accountUuid`, `emailAddress`, `organizationUuid` under `oauthAccount`) read from the temp home's `.claude.json` added to both redactor and scanner. Only echoed `user` lines are kept verbatim; everything else is reduced to types and marker booleans.

**Promotion.** The record is always written to an `os.MkdirTemp` artifact dir. `fixtureWorthy` promotes it to `testdata/mid_turn_user_v<version>.json` when the version parses as `N.N.N`, all four arms are present and none is `no-trigger` or instrument-broken. A `no-result` arm is still worthy (the ticket records timeouts as such). A refused promotion is a `t.Errorf` naming the reason. A promoted fixture is staged with a best-effort `git add`.

## Concurrency model

Per arm: `os/exec`'s stdout copier goroutine writes into the mutex-guarded `dropcapRecorder`; the test goroutine polls `snapshot` and writes stdin. `cmd.Wait` joins the copier. The child runs under a per-arm `context.WithTimeout`, so a hung child is killed and `Wait` returns. No goroutine of the probe's own.

## Error handling

Instrument failures (stdin pipe, start, write, deny-scan hit, marshal) fail the test; a deny-scan hit writes nothing. Model behaviour (no result, timeout, ignored message) is recorded in the arm's verdict, never a failure. A trigger that never fires makes the capture unusable and fails promotion loudly.

## Testing strategy

Offline tests in the same file (they compile only under the tag, as the siblings do):
- `TestMtuSequence`: run-length collapse, block extraction, write insertion at its count.
- `TestMtuVerdict`: one row per verdict.
- `TestMtuFixtureWorthy`: a good record promotes; each refusal arm (bad version, missing arm, `no-trigger`) refuses with a reason.

Compile check: `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` and the offline tests via `go test -tags e2e_realclaude -run 'TestMtu' ./internal/e2e/realclaude/`. The live run is the dispatcher's gate.

## Open questions

- Whether `--replay-user-messages` echoes a mid-turn line at all: the record answers it; the probe classifies echoes by marker, not by an assumed position.
- Whether the no-tool arm's 1 s delay after the first `stream_event` lands while text is still streaming for haiku: the sequence records it either way, since the write's position is shown against the line types.

## Documentation handoff

None from the ticket. The documentation stage may fold the four verdicts into the streamsup / Send now topic once the capture is committed.

## Revisions

- **2026-10-03, during the build.** Three departures from the Design above.
  1. The verdict for one result with no mid-turn marker in its text is `not-quoted`, not `not-read`. The marker's absence from the final text does not show claude never read the message; the name says only what was measured, and the record's limitations say so.
  2. The four arms run concurrently, one goroutine and one child each, joined by a `sync.WaitGroup` before the record is built. Each arm also runs `cmd.Wait` in a goroutine so the polling loop can see an early exit; the arm's deferred join receives it. Redaction moved to the test goroutine after the join, because `dropcapRedactor`'s counters are unlocked. This replaces "No goroutine of the probe's own" in the Concurrency model, and cuts the wall clock from the sum of the arms to the slowest one.
  3. Added `TestMtuSummariseFindsEchoesAndResults` to the offline tests: the echo census (a marker-bearing `user` line is an echo, a `tool_result` is not) and the final text being the last result's.
