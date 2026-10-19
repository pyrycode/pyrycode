# Witness the stop scenario's hold operation

## Files read

- `internal/e2e/realclaude/stop_background_task_completion_test.go` → `stopHeldTaskSetup`, `stopHeldTaskID`: exact command matching and last-candidate selection currently weaken setup.
- `internal/e2e/realclaude/tool_progress_capture_test.go` → `tpcapHoldFIFO`: bounded, idempotent writer cleanup and arrival rendezvous remain shared.
- `internal/e2e/realclaude/composer_stop_test.go` → `TestRealClaudeComposerStopPreservesBackgroundTask`: shares setup with the existing FIFO-command contract; retain that caller's behavior.
- `internal/e2e/realclaude/roster_after_finish_capture_test.go` → `rafcapPrompt`: other scenarios keep their existing prompt.
- `docs/knowledge/features/e2e-realclaude-roster-after-finish-capture-test-go.md` § “Stop completion needs a held task and all terminal signals”: preserve latest-roster joins, truncation guards and either terminal signal while held.
- `docs/knowledge/features/e2e-realclaude.md`, `development-verification-captures-and-live-evidence.md`, `CODING-STYLE.md`: tagged offline checks, redacted diagnostics, native verification and subprocess cleanup.

## Context

This is one behavior slice: attribute the physically held stop target independently of incidental shell quoting. Offline execution controls and identity rejection cases prove that repair together. This source-audit risk is distinct from #2848's reproduced description-matching failure. No production behavior or decision record changes.

Scope estimate: approximately 500 written lines including tests and this plan, zero exports, two existing setup consumers, four acceptance criteria, fewer than ten identity rejection categories. No overlapping feature branch edits the stop file at planning time. Rechecked against the completed plan; all limits hold.

## Design

Add a scenario-owned `stopHoldOperation` test helper: create an owner-executable script that appends a one-byte execution witness in the private rig directory and then execs `cat` on the held FIFO. Require exactly one witnessed execution. Its dedicated prompt requests one background Bash invocation of this executable with no arguments.

Recognize the executable as the sole shell operation using a small allowlist: shell-safe bare path, single-quoted path, double-quoted path, optionally `exec`, whitespace and a final semicolon. Quote generated paths correctly; reject arguments, prose, command substitutions, compound operations and mere mentions. This is not a general shell normalizer. Quoted paths with spaces are supported.

Opt the stop scenario into witnessed setup; preserve the existing FIFO-command mode for Composer Stop. Track all observed matching operation/tool identities (including ineligible foreground or other-conversation calls) so their execution cannot be borrowed by a different eligible call. Require exactly one eligible tool, exactly one execution, and one unambiguous untruncated local_bash task in the latest bound roster. Retain all valid task-start joins rather than overwriting the last one. Conflicting start identities or contradictory roster tool IDs fail closed; duplicate observations of the same identity remain harmless.

Keep the live drain and terminal proof: FIFO arrival precedes stop, the FIFO stays held until stopped-update or subsequent roster omission in the bound conversation, and acceptance alone cannot pass. Poll setup within its existing deadline so witness/file and envelope arrival order cannot strand a complete join.

## Concurrency model

Setup belongs to the test goroutine. The witness is written by the script and read with a two-byte cap (zero, one or multiple executions); no in-memory state is shared. The existing FIFO goroutine exits on idempotent release, including no-reader teardown. Offline child processes use context deadlines and are killed/waited during cleanup.

## State transitions and identity reuse

| Event | Race-test coverage |
| --- | --- |
| Tool, roster, start and physical execution arrive in different orders | `TestStopHeldTaskSetup`, `TestStopHoldOperation` |
| Latest roster removes an earlier member | `TestStopHeldTaskSetup` |
| Repeated identical metadata, competing tools, repeated execution or conflicting starts/rows | `TestStopHeldTaskSetup`, `TestStopHeldTaskID`, `TestStopHoldOperation` |
| Release with a held reader, repeated release, teardown without execution | `TestStopHoldOperation`, existing `TestTpcapHoldFIFO` |

## Error handling

Malformed relevant frames return fixed decode errors; witness absence/read failure, missing/truncated joins and ambiguity yield no target. Rig creation failures fail the test with fixed messages. Setup diagnostics expose only counts/booleans. Existing setup and completion deadlines remain bounded.

## Testing strategy

Write offline regression controls first and observe failure against the old spelling/first-or-last selection. Execute the real script through Bash under bare, single and double quotes, including spaces; confirm arrival and blocked exit before release. Reject mentions, missing physical evidence, foreground/wrong-conversation tools, false joins, competing identities and truncated keys. Exercise all six tool/start/roster permutations and execution-before/after metadata. Retain focused tagged race JSON logs/counts on macOS, run tagged vet, normal vet and binary build. Full suites and live acceptance belong to dispatcher/verifier.

## Open questions

None. Supported shell forms are intentionally bounded; arbitrary shell equivalence is outside this attribution contract.

## Documentation handoff

Pending documentation stage: update `docs/knowledge/features/e2e-realclaude-roster-after-finish-capture-test-go.md`, under “Stop completion needs a held task and all terminal signals”, replacing the exact `cat`-string identification description with the witnessed rig-operation contract and supported invocation forms. Preserve the tool/task join, latest-roster membership, truncation safeguards and unreleased-FIFO terminal proof; distinguish this source-audit risk from #2848's reproduced failure.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `stopHoldOperation.matches` accepts only a direct invocation; setup also requires its private physical witness and unique eligible tool/task joins. Mere text and FIFO arrival carry no identity authority. Foreground and wrong-conversation matching invocations poison attribution rather than lending evidence.
- [Tokens and cryptography] No credentials or cryptographic material added. Operation identity comes from the private rig-owned path, not a secret token or invented cryptographic guarantee.
- [File operations] Script mode 0700; witness mode 0600 via script umask, under test-owned directories. No externally supplied path is accepted. Quote all embedded paths. Read at most two witness bytes; partial/missing writes cannot establish repeated-execution safety until exactly one byte exists.
- [Subprocesses] Shell interpretation is deliberate for testing the supported shell forms. Only rig-generated quoted paths enter the script; offline children have deadlines and cleanup waits. The script execs the FIFO reader, preserving the stopped process's hold.
- [Network and I/O] Reuse existing bounded harness reads; no new network endpoint or authorization behavior. Witness reads are capped.
- [Errors and logs] Fixed errors and count/boolean diagnostics avoid exposing commands, paths, descriptions or IDs.
- [Concurrency] No new goroutines outside existing FIFO infrastructure; bounded process cleanup and repeated release tests cover shutdown. Setup reads occur on its owning goroutine.
- [Threat model] Test attribution protects against incidental or ambiguous model output. A malicious process in the same user account can forge test files; defending that account boundary is outside this test and introduces no product trust claim.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-19

## Revisions

- 2026-10-19: Source review found that an unsupported compound invocation could otherwise lend its witness to a later direct-command claim. Any Bash command mentioning the operation through an unsupported form now vetoes attribution; substring occurrence is used only to reject, never to identify. Offline controls verify that a later eligible tool cannot borrow that evidence. Offline Bash controls own a process group so cancellation also stops forked FIFO readers.
