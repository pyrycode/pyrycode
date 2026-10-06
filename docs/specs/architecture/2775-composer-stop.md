# Composer Stop preserves background work

## Files read

- `internal/streamsup/envelope.go` → `controlRequestInner`, `marshalInitializeEnvelope`, `WriteInitialize`: shared structured writer and subtype-specific omission.
- `internal/streamsup/envelope_test.go` → `TestMarshalInitializeEnvelope`, `decodedControlRequest`: exact bytes and hostile correlation IDs.
- `internal/streamsup/interface_test.go` → `initializeAsks`, spawn/replacement tests: every child receives its own initialize declaration.
- `internal/streamsup/runner.go` → `spawnAndWait`, `RequestInitialize`: the existing per-spawn path covers replacements.
- `internal/e2e/realclaude/stop_background_task_completion_test.go` → `stopHeldTaskSetup`: joins the actual rig tool to its background task; descriptions alone are insufficient.
- `internal/e2e/realclaude/interactive_stream_interrupt_test.go` → `drainForCancelledTurnEnd`: cancellation must precede natural completion.
- `internal/e2e/realclaude/interactive_stream_running_turn_test.go` → `startStreamRunningTurnHarness`: production daemon/phone interrupt wiring.
- `internal/e2e/realclaude/tool_progress_capture_test.go` → `tpcapHoldFIFO`: release and bounded cleanup even without a reader.
- `internal/e2e/realclaude/roster_after_finish_capture_test.go` → `rafcapPrompt`: explicitly background the rig command; a task start alone does not prove backgrounding.
- `internal/e2e/realclaude/fifo_reader_liveness_test.go` → `fifoLiveRead`: distinguishes a held reader from death without process-table races.
- `internal/e2e/realclaude/mid_turn_user_capture_test.go` → `mtuArgv`, `mtuRunArm`: direct stream-json process shape and bounded cleanup.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapRecorder`: synchronized raw line observation without publishing captures.
- `docs/knowledge/features/streamsup-package.md`: shared initialize writer, held-open stdin and teardown boundary.
- `docs/knowledge/features/e2e-realclaude.md`, `development-verification.md` § Captures and live evidence: standing tests, rig-owned witnesses and actual execution counts.
- `docs/protocol-mobile.md` § Security model: authenticated remote interrupt stays within the existing paired-device boundary.

## Context

Composer Stop currently cancels background work along with the active turn. All rollout prerequisites are merged. Declare Claude's process-wide `perTaskStopAffordance` unconditionally, including for older clients, which retain turn-only Stop and need an upgrade for a per-task button. Historical initialize response captures remain evidence of their original bare request and are preserved.

One deliverable, two acceptance criteria. Estimated written work: 450 lines including tests and plan; zero new exported types/interfaces, zero consumer migrations, and fewer than ten test failure branches per scenario. No concurrent feature branch touches the planned files. No decision record is required.

## Design

Add an initialize-only boolean with `omitempty` to `controlRequestInner`; `marshalInitializeEnvelope` sets it to true. All other control subtypes retain exact existing bytes. Keep field order, structured encoding and single-line termination. The existing spawn initialize path needs no new wiring or version gate.

Extend exact-byte, live-child and initial/replacement spawn assertions to require the flag. Correct adjacent comments that still describe the writer as subtype-only, without rewriting historical response fixtures or the independent historical probe.

Add standing tests under the existing `e2e_realclaude` build tag:

- Interactive: stage and identify a FIFO-held background Bash task through the production phone harness, finish its setup turn, then start a second FIFO-held foreground call. Observe its exact tool and FIFO rendezvous before sending `protocol.TypeInterrupt`. Require a cancelled turn, verify the background FIFO still has its reader, release it, and require that same task's completed terminal update. A retained roster or interrupt ack cannot pass.
- Closed input: start a direct Claude stream-json child with the production initialize writer. Request the background FIFO call followed by a foreground FIFO call. Verify both calls' actual input and rendezvous, close stdin while the foreground result is held, and verify the background reader is still alive. Release only the foreground result, await normal child completion and require the background FIFO reader to disappear while its writer remains held. This distinguishes Claude's closed-input cleanup from parent cancellation or background EOF.

Log the Claude version in both standing tests. The dispatcher-owned live gate reports their pass/fail, executed counts and skip reasons on #2775; no capture artifacts are required.

## Concurrency model

The writer adds no state, locks or goroutines. Live tests keep one phone reader and sequential Noise receive state. Reuse FIFO holds with idempotent releases and bounded goroutine joins. The direct child has a context deadline, one `Wait` goroutine, and cleanup that cancels and joins it; release foreground/background holds on every return. Successful closed-input assertions run before cancellation.

## Error handling

Preserve existing no-child, marshal and write errors. Live tests fail on missing/extraneous rig calls, request refusal, early terminal events, wrong cancellation/completion status, instrument failure, or deadline expiration. Authentication absence uses the existing harness skip; it is not a live pass. Diagnostics report counts/statuses without dumping child output or credentials.

## Testing strategy

First update wire assertions and observe the old writer fail. Implement the boolean and run initialize, spawn/replacement and other exact-byte control tests. Run `go test -race ./internal/streamsup/...`, offline tagged helper tests for the reused setup/FIFO observers, `go vet ./...`, `go vet -tags e2e_realclaude ./internal/e2e/realclaude/...`, and `go build -o /tmp/builder-2775/pyry ./cmd/pyry`. The verifier owns the full-module gate. The dispatcher owns live execution under normal `make e2e-realclaude`.

## Open questions

None. The live gate must establish the behavior on the installed Claude version; binary-string evidence cannot satisfy it.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/streamsup-package.md`, add “Composer Stop”: describe turn-only Stop, older clients needing an upgrade for per-task stop, and the closed-input exception.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The new value is a fixed true literal at `marshalInitializeEnvelope`, never client-controlled. Existing authenticated interrupt and task-stop routing remain the boundary; initialization cannot depend on client connection order.
- [Tokens, secrets, credentials] No credential handling changes. Test auth uses `WithWorktreeAuthenticated`; logs retain only version, counts and status, never stdout, tool input or environment.
- [File operations] Production performs no new file operations. Test FIFOs live under isolated workdirs, use the existing `tpcapHoldFIFO` mode 0600, and have rig-generated paths.
- [Subprocesses] Production argv and teardown are unchanged. Test shell commands are rig-authored `cat` calls in isolated paths; direct-child cleanup cancels and joins, while successful cleanup assertions happen before parent cancellation.
- [Cryptography] No key, nonce or primitive changes. Existing sequential Noise state is reused by the single phone reader.
- [Network and I/O] No new endpoint or read surface. `omitempty` confines the new constant to initialize; exact-byte sibling tests guard against changing other requests. The direct recorder uses existing capture caps.
- [Errors, logs, telemetry] Writer error contracts remain unchanged. Test failures report structural observations, avoiding raw child stderr/stdout and task descriptions.
- [Concurrency] No new production state. Test release paths must execute even when setup never reaches a FIFO reader; the existing hold helper covers this. Direct child waits are bounded by context and cleanup joins.
- [Threat model] Turn-only Stop changes cancellation scope for already authorized callers; explicit per-task stop remains shipped separately. Existing protocol threats and mitigations are unchanged. Closed-input behavior is independently tested so parent teardown cannot masquerade as Claude task cancellation.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-06

## Revisions

- 2026-10-06 — Live gate repair: `TestRealClaudeComposerStopClosedInputKillsHeldTask` reached held-call setup on Claude 2.1.280 but timed out after releasing the foreground FIFO. The owning topic, `docs/knowledge/features/e2e-realclaude-roster-after-finish-capture-test-go.md` § “A foreground-held FIFO never returns its tool result, even after the command exits”, documents this rig trap. Replace only the closed-input foreground FIFO with `composerForegroundGate`: a shell command writes an arrival marker and waits for a release file in the isolated workdir. Match its exact Bash input, observe arrival, close stdin, verify the background FIFO reader still exists, then create the foreground release file. Normal child exit and background-reader disappearance remain mandatory before parent cancellation; the background writer remains held. `TestComposerForegroundGate` proves held execution, normal release, and repeated-release cleanup offline. Production initialization and the interactive interrupt scenario are unchanged.
- Security review of the repair: PASS. The file gate uses rig-generated paths in the private workdir, shell-quotes paths (including single quotes), creates the release marker with mode 0600, and exposes no payload or credentials in diagnostics. The existing child deadline, reaper on failure, and joined `Wait` goroutine still bound cleanup. No new production trust boundary, credential, network, or cryptographic behavior is introduced.
