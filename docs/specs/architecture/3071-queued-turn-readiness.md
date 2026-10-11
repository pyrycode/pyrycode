# Queued Claude readiness comparison (#3071)

## Files read

- `internal/streamsup/runner.go` → `spawnAndWait`: Start, arm, stdin publication and permission request ordering.
- `internal/streamsup/parser.go`, `parser_control.go` → `Write`, `noteControlAck`: complete-line processing and matching-success release.
- `internal/streamsup/posture_gate.go` → `arm`, `release`: current-child acknowledgment identity.
- `cmd/pyry/inbound_deliver.go` → `newInboundDeliver`: per-attempt activation, idle hold and queued write.
- `internal/e2e/realclaude/harness_modal_test.go` → `spawnPermissionDaemon`: stored default posture and operator-bypass provenance.
- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go` → `startObservedPermissionHarnessWithOperatorBypass`: authenticated isolated HOME, real daemon and encrypted relay.
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` → `createConversationViaPhone`: actual queue consumer setup.
- Checkpoint `d4bffd77` → corrected readiness capture, readers and tagged observations: retain bounded WebSocket reads; previous successful arms are non-reproduction.
- Pinned mobile `90d0407f06` → `e2e-emulator.sh`, `InteractiveStreamE2ETest`: 65 selected methods, 37 before ping by signed Java name hash, separate daemon ownership and asynchronous/background edges.
- Pinned mobile `ThreadViewModel`, `RemoteConversationRepository`: MCP, context, settings and newest-history requests at thread arrival/reconnect.
- Mobile comparison with `4a5e624f6f`: merged changes affect archive cleanup, rename and attention projection; source-derived order is not an execution witness.
- `docs/knowledge/features/e2e-realclaude-test-infrastructure.md`: timeout closes fakephone; count named terminal outcomes and commit artifacts.
- `docs/knowledge/features/streamsup-package-posture-gate-spawn-permission-mode-ack.md`: operator bypass cannot be inferred from composed argv, and residual arm must not survive replacement.
- `docs/knowledge/features/msgqueue-package.md`, `sessions-package.md`: isolated serial queue drains and deferred child creation.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: bounded evidence, comments and race checks.

## Context

Original host admission took 185.6 seconds; the acknowledgment and gate-release timestamps remain unknown. The authenticated checkpoint reached 34 children without reproducing it, but omitted mobile prefix effects, thread-opening controls and startup configuration. Diagnose before repairing. A decision record is premature without an established cause.

## Design

Recover corrected ticket-owned capture/readers and additive tagged observations onto current main. Ordinary builds keep observation no-ops. Correct `ack_stdout_line` to describe its actual site inside `Parser.Write`. Wrap the child's stdout writer in the tagged build to witness each forwarder write before parser work; complete-line availability there is not kernel arrival. Preserve permission-write, processing/release, activation/idle and write-attempt boundaries.

Build a serial, permission-aware sonnet comparison through `spawnPermissionDaemon`, with operator bypass false and both MCP booleans true. Both arms use the same test-owned startup fixture and thread-opening traffic; the fresh arm omits only the prefix. Source idle/cap zero and unknown original spacing/configuration remain assumptions. Store a replay table identifying method, daemon ownership, operations, retention/removal, completion/idle edges, background/permission work, reconnect and pacing. Reproduce main-daemon effects through encrypted relay/queue paths, without assigning answer/bypass/second-host turns to the main daemon. UI delays become explicit unknowns rather than invented original timings. Record every omitted effect as a fidelity limit.

At target arrival, issue MCP-status, context-usage, settings and newest-history asks without awaiting individual replies. Send immediately through `send_message`, without a readiness warmup. Record create/open/control/enqueue separately and bootstrap as ordinal zero. Resource samples include live children, subprocess aggregate CPU/RSS and host memory/pressure.

Test-owned MCP and startup-hook fixtures model documented configuration roles, with deterministic completion and private cleanup. Their contents and timing are hypotheses, not recovered operator files. Never read operator configuration or obtain credentials directly; use launcher admission and the existing isolated authenticated HOME.

Retain portable structured records including failures/non-reproduction, safe source/setup provenance, observation-site descriptions, exact-content and identity-match booleans, local ordinals, hashes and named executed/passed/failed/skipped counts. The 90-second bound starts at persisted enqueue; continued observation cannot convert late admission into success. Historical records remain linked at `d4bffd77` rather than restored.

Select a repair only if measured boundaries establish a host mechanism. Append a revision with its contract and focused failing-before/passing-after race scenario before implementation. Otherwise return measured uncertainty for refinement; do not claim this diagnostic completes the repair ticket.

Sizing: forecast approximately 1,200 written lines, one new exported observation function, no consumer migration, three acceptance criteria, no new production rejection state machine. The handbacks floor takes precedence over the 800-line ceiling: capture/observers have exactly one consuming cause-backed repair and no standalone shippable contract. Reassess after diagnosis. Overlap audit fetched feature/2873, /2882, /3139, /3163 and /3178; their daemon composition/live publication work does not supply a readiness prerequisite. Keep shared edits additive and local.

## Concurrency model

Existing child stdout-forwarding and per-conversation queue goroutines remain authoritative. Observation writes occur off existing locks, serialize private appends and never affect admission. The sampler is canceled/joined before export. Phone cipher reads/writes stay ordered; reconnect installs new cipher states. Test-owned fixture processes stop with daemon/test teardown.

## State transitions and identity reuse

| Event | Race coverage |
| --- | --- |
| Fresh target while confirmation pending | `TestInteractiveStreamQueuedTurnReadiness` prefix/fresh arms require release before exactly-once delivery |
| Multiple retained conversation identities | same test replays distinct main-daemon conversations and excludes dedicated-daemon work |
| Reconnect/re-read | same test re-handshakes and repeats opening controls without resending accepted turns |
| Queue removal, reset and background overlap | same test records replay effects and their observed terminal edges; omitted edges remain fidelity limits |
| Child replacement / stale or mismatched ack | existing `TestPostureGate` and spawn-permission race family; observation matching test |

## Error handling

Launcher authentication failure is an environment blocker. Missing required observations or broken capture ordering fail the diagnostic and retain partial safe evidence. Observer I/O is best effort; capture validates its existence. Do not serialize daemon log buffers, credentials, raw arguments or correlation tokens.

## Testing strategy

Recover observation privacy/matching tests and history-reader tests first. Add assertions for replay ordering, ownership exclusions and target-control order; verify them before live admission. Run the named authenticated comparison from this worktree. Diagnose measured boundary before writing a causal regression. Run scoped race checks, tagged offline checks, `go vet ./...` and `go build ./cmd/pyry`. Dispatcher owns the full host gate; mobile #2010 owns its full mobile gate and unchanged UI assertions.

## Open questions

- Does the candidate prefix/startup comparison expose the original slow boundary? Non-reproduction must preserve remaining fidelity limits.
- Which original runtime values and merged-tree effects cannot be recovered? Record unknowns rather than inventing evidence.
- Does any diagnosed repair fit the remaining budget and scope? Reassess before expanding.

## Documentation handoff

Pending documentation stage: `docs/knowledge/features/e2e-realclaude-test-infrastructure.md` § Test infrastructure records the established trigger, permission-aware daemon/queue prefix and fresh-daemon comparison, setup provenance and counted before/after evidence links. Distinguish process start, stdout observation site, acknowledgment processing and gate release. State that #2010 owns the unchanged full mobile gate. Describe the mechanism only after diagnosis establishes it.

## Security review

**Verdict:** PASS. **Reviewer:** builder self-review. **Date:** 2026-10-11.

- [Trust boundaries] `noteControlAck` and `PostureGate.release` remain the sole matching-success permission boundary. Observation booleans authorize nothing; encrypted test turns use the real paired-device route.
- [Tokens/secrets] SHOULD FIX: private joins must never appear in portable records; export ordinals and exact-match booleans only. Launcher owns credential access.
- [File operations] Fixtures/joins live in isolated temporary directories with 0700 directories and 0600 records; atomically publish bounded structured evidence. No operator configuration is copied.
- [Subprocesses] Use fixed test-owned fixture programs and existing descendant teardown; numeric `ps` columns exclude commands/argv. No runtime-authored shell command strings.
- [Cryptography] Reuse existing Noise transport; reconnect replaces states rather than reusing nonces. Artifact hashes use standard SHA-256.
- [Network/I/O] Bounded fakephone reads retain the corrected deadline; no short polling that closes the socket. Local fixture listeners have explicit lifecycle cleanup.
- [Errors/logs] SHOULD FIX: persist only static failure categories and allowlisted measurements. Private daemon output stays in memory.
- [Concurrency] Observe outside existing locks; join sampler before reading resources; preserve serialized cipher-state use.
- [Threat model] Existing relay, token binding, exactly-once queue and conversation isolation protections remain unchanged; no gate bypass or timeout increase.

## Revisions

- 2026-10-11: first reconstructed batch at `b1376c5a` executed 2 arms (1 passed, 1 failed, 0 skipped). Prefix stopped at method 17 because its new driver incorrectly awaited `turn_end` after `/compact`; compaction emits its own `compaction_boundary`. Fresh admission opened within 4.478 seconds. Retain this as incomplete diagnostic/capture failure, never failing-before host evidence. Correct the terminal edge, include required promote cwd and await source mutation replies so the replay does not count invalid writes as completed effects. The unchanged enqueue deadline and permission gate remain intact.
