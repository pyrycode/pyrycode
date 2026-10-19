# #3619 — witnessed background reply hold

## Files read
- `internal/e2e/realclaude/background_reply_completion_test.go`: `backgroundReplyBoundary`, release and capture publication own the scenario.
- `internal/e2e/realclaude/background_reply_replay_test.go`: persistent parser comparison and allowlist sanitization.
- `internal/e2e/realclaude/background_reply_controls_test.go`: release partial-line and replay controls.
- `cmd/pyry/interactive_turn_v2_completion_test.go`: retained evidence reader and live/catch-up/history/checkpoint assertions.
- `internal/turnevent/event_turn.go`: `ToolStart.RawInput` retains structured tool input.
- `internal/e2e/realclaude/interactive_stream_hook_blocked_banner_test.go`: isolated user settings installation pattern.
- `docs/knowledge/features/e2e-realclaude-test-infrastructure.md`: private capture, source ordering and release-token requirements.
- `docs/knowledge/features/e2e-realclaude.md` and `development-verification-captures-and-live-evidence.md`: tagged controls, capture survival and truthful provenance.
- `CODING-STYLE.md`: subprocess tests, cancellation and contract comments.
- Claude Code hooks reference, `PreToolUse` decision control: hook input includes tool identity; updatedInput preserves other tool fields.

## Context
Exact command equality rejects equivalent shell quoting. This is an audited test-rig risk, not a reproduced live failure. One behaviour slice joins actual held execution to its child tool; reply wording remains for #3620. No production change or decision record is needed.
Scope sketch and plan recount: approximately 700 written lines including tests, plan and returned capture; zero exported types, zero production consumers, four acceptance criteria, at most ten reject categories. The witness helper serves only this scenario and stays with its consumer. No overlapping feature branches touch the existing target files.

## Design
A private rig creates a hold executable and a PreToolUse hook using the TestHelperProcess pattern. The hook prepends an exported tool identity to Bash execution, preserving all other inputs and never granting permissions. The hold executable records its rig operation identity, inherited tool identity and PID atomically, then waits for the release file with a bounded deadline. Its independently read witness must be unique, match the operation and name a live held process before release.
`backgroundReplyBoundary` joins that witness to exactly one Bash declaration under exactly one background Agent, rejecting missing, wrong or competing identities without inspecting command spelling. Release needs both witnessed setup and main success, in either arrival order. Matching successful release-token output and attributed final prose retain their existing source-order contract.
Sanitization replaces every command with an explicitly synthetic value while retaining parser trace equivalence at every source index. The record carries aliased operation/Agent/Bash correlation and OS. New records require that correlation; the existing old fixture remains explicitly historical until dispatcher artifacts return and must never be relabelled.

## Concurrency model
The existing runner goroutine exits through context cancellation and bounded wait. The helper subprocess waits with a deadline; rig cleanup releases remaining holds. Witness publication uses exclusive creation plus atomic rename. Recorder release keeps its existing lock and partial-line exclusion. No new parent goroutine is needed.

## State transitions and identity reuse
| Event | Race control |
| --- | --- |
| Shell quoting variants execute the same hold | `TestBackgroundReplyOperation` |
| Main success before/after witness | `TestBackgroundReplyWitness` |
| Missing, wrong or competing identity; foreground Agent | `TestBackgroundReplyWitness` |
| Repeated execution or exited witness | `TestBackgroundReplyOperation` |
| Release with a partial recorder line, pre-release final, failed/wrong result | `TestBackgroundReplyReleaseBoundary`, `TestBackgroundReplyWitness` |
| Replay source order and correlation mutations | `TestBackgroundReplyWitness`, `TestBackgroundCompletionCorrelation` |

## Error handling
Missing setup waits until the scenario deadline; malformed/ambiguous evidence fails closed. Errors never include commands, paths or tokens. Capture loss remains fatal. Hook failures cannot mint execution evidence. Hold timeout exits unsuccessfully; cleanup releases before runner cancellation.

## Testing strategy
Write physical subprocess and correlation controls first and observe failure, then implement. Run affected tagged race controls, the recorded completion regression and correlation mutations, tagged compilation, vet and binary build on Linux. Complete suites and live acceptance belong to the dispatcher. The live gate must execute the scenario without skips and retain revision/OS/counts and deny-scanned replay. On return commit exact artifact bytes and rerun the recorded regression.

## Open questions
None. Live compatibility of hook propagation is checked by the dispatcher-owned live gate; no fabricated capture substitutes for it.

## Documentation handoff
Pending documentation stage: in `docs/knowledge/features/e2e-realclaude-test-infrastructure.md`, “Test infrastructure”, state that setup joins a physical execution witness to the child Bash operation independently of generated shell spelling, and release waits for both that hold and launching-main-turn success; retain the release-token, source-order and private-capture requirements.

## Security review
**Verdict:** PASS
- Trust boundaries: the hook supplies tool identity, but only execution of the hold helper publishes a witness. Boundary joining rejects ambiguity; a command mention cannot create evidence.
- Tokens: `crypto/rand` operation and release identities stay in private files and raw capture. Publication aliases operation/tool identities and replaces release content; no token is logged.
- Files: private directories 0700, evidence 0600 and executable wrappers 0700. Rig-generated paths are shell quoted; untrusted IDs never become paths. Witness publication is atomic and repeated executions remain distinguishable.
- Subprocesses: only test-owned wrappers are launched. Hook input is bounded and JSON-decoded; shell interpolation of identity uses proper quoting. Helpers have deadlines and cleanup release; runner cancellation retains descendant cleanup.
- Cryptography: randomness identifies operations; no encryption or authentication protocol is added. Equality checks correlate test evidence rather than authenticate remote callers.
- Network/I/O: no new network access; hook stdin is capped, recorder caps remain authoritative.
- Errors/logs: generic errors and scalar boundaries only; deny scanning remains mandatory before publication.
- Concurrency: recorder lock still spans physical release. Atomic witness publication prevents a partial file from becoming setup; no new lock ordering.
- Threat model: authenticated isolated live test rig, not a sandbox against a malicious model with arbitrary file access. Production relay/mobile security is unchanged.
**Reviewer:** builder self-review. **Date:** 2026-10-19.

## Revisions
- 2026-10-19: Retained correlation uses an explicitly synthetic operation command on the witnessed tool, plus aliased witness metadata. This lets the reader reject a mismatched operation as well as a mismatched tool. The exact old capture revision remains historical until artifact return. Direct Runner settings carry the hook without modifying HOME. Hold output waits for a complete newline-terminated release file to avoid observing its creation before token bytes are written.
