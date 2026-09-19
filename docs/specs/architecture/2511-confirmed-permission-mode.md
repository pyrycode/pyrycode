# Confirmed permission mode of the current stream child

## Files read

- `internal/streamsup/parser.go` → `Parser`, `beginMCPStatusChild`, `emitInitLine`, `emitSessionFacts`, `noteControlAck`, `controlAckLine`, and the `control_response` arm of `consumeLine` — owns child stdout parsing, per-child parser state, the existing bounded init mode, and acknowledgement dispatch.
- `internal/streamsup/runner.go` → `Runner`, `SetPermissionMode`, `spawnAndWait`, `setStdin`, `takeStdin`, and `PostureGate` — owns the current child, the permission-mode write, child boundaries, and the concurrency-safe runner API.
- `internal/streamsup/posture_gate_test.go` → `TestParser_NoteControlAck_ReleasesOnlyItsOwnAck` and `TestRunner_SetPermissionMode_FailedWriteDoesNotRetarget` — existing request-id correlation and failed-write coverage that the new informational hold must preserve rather than replace.
- `internal/streamsup/session_facts_test.go` → `TestParser_SessionFactsFieldsAreBounded` and `TestParser_SystemInitDecodeTargetDeclaresOnlySafeFields` — pins the existing 256-byte permission-mode bound and the intentionally narrow `systemInitLine` decode target.
- `internal/streamsup/mcp_actuation_test.go` → the `actuateMCP` correlation tests — nearest hermetic example of registration-before-write, an early reply waiting for the write verdict, and child-boundary retirement.
- `internal/streamsup/initialize_capture_test.go` → `capturedInitialize` — pattern for a hermetic reader that binds committed capture names to their recorded Claude version and content and fails rather than skips when evidence is absent.
- `internal/e2e/realclaude/testdata/set_permission_mode_v2.1.220_revoke.json` → `control_responses` — committed redacted evidence that Claude 2.1.220 echoes the accepted `default` mode beside the matching request id.
- `internal/e2e/realclaude/testdata/bypass_reescalation_v2.1.239_reescalate.json` → `control_responses` and `second_control_request` — committed redacted evidence that Claude 2.1.239 echoes both a downgrade and a later `bypassPermissions` re-escalation.
- `docs/knowledge/features/streamsup-package.md` → “Turn I/O — envelope write + stdout parser” and its correlation testing note — requires a blocked early response to lose to a failed write and keeps parser correlator locks outside `Runner.mu`.
- `docs/knowledge/features/streamsup-package-posture-gate-spawn-permission-mode-ack.md` → “A fail-closed gate needs a working exit” — distinguishes the turn-admission gate from Claude's informational echoed posture and records why in-band changes must not close an open gate.
- `docs/knowledge/features/development-verification.md` → “Prove that tests distinguish the change” and “Captures and live evidence” — requires direct committed-fixture provenance checks and non-vacuous branch coverage.
- `CODING-STYLE.md` → “Concurrency”, “Logging”, and “Comments — Citing Other Code” — requires leaf mutexes, race verification, content-safe structured logging, and symbol-based citations.

## Context

The runner currently knows what posture the daemon intended to launch or request, while only Claude can report what the live child says it is enforcing. `SessionFacts` already publishes the `system/init.permissionMode` claim, and `PostureGate` already correlates a request id for turn admission, but neither retains the last confirmed mode for an out-of-turn read. Downstream ticket #2510 needs that current-child fact without falling back to settings or argv and without turning the read into a control operation.

This change adds one informational runtime hold inside `internal/streamsup`. It does not change the protocol, stored settings, spawn posture, `PostureGate`, or any client-facing frame. The distinction deserves no ADR: it extends the package's established per-child correlation pattern without changing a cross-package architecture decision.

The written scope remains one deliverable and within the ticket boundary: two production files, one new exported method, no changed call sites, four acceptance criteria, and fewer than ten response rejection branches. Estimated total written work is about 650 lines including this plan and focused tests, below the 800-line ceiling.

## Design

### Shared per-child hold

`Parser` will own a `confirmedPermissionModes` hold whose mutex protects:

- whether a child boundary is active;
- a monotonically changing child generation;
- the bounded last confirmed mode plus availability;
- pending `set_permission_mode` entries keyed by request id, each carrying the requested mode, its child generation, and a one-shot write verdict.

`Runner.ConfirmedPermissionMode() (string, bool)` will read that hold through the concrete parser already bound from `Config.Stdout`. A runner without that parser, a runner before its child reports, and a runner after child exit all return `"", false`. The method performs no write, request, gate transition, setting lookup, or argv inspection.

`beginMCPStatusChild` will also begin a permission-mode child boundary before `cmd.Start`, clearing predecessor state before any new stdout can arrive. Spawn setup failures will end that boundary explicitly. `takeStdin` will end it after `cmd.Wait` and outside `Runner.mu`, before `spawnAndWait` returns to the backoff loop. Both transitions increment the hold generation, clear the confirmed value, and discard every pending id.

### Confirmation sources

`emitSessionFacts` will keep emitting the unchanged `turnevent.SessionFacts`. When its decoded `PermissionMode` is non-empty, the same `truncateField` result already used for the event and `maxPermissionModeField` will also update the active child's hold. An empty field never clears or synthesizes a value.

`SetPermissionMode` will register its locally minted id and requested mode before calling `WritePermissionMode`. It then publishes the write verdict to the pending entry and removes a failed registration. Its existing `PostureGate.retarget` call remains after a successful write and otherwise unchanged.

The parser's `control_response` arm will add a content-free confirmed-mode acknowledgement reader beside `noteControlAck`. It first decodes only the request id and retires an exact pending entry. It updates the hold only when all of these remain true:

- the write verdict is successful;
- the pending entry's generation is still the active child generation;
- the response subtype is `success`;
- the nested echoed mode is present and exactly equals the requested mode.

Unknown ids, mismatched ids, a response with a missing or differently typed id, malformed payloads, absent or differently typed modes, unequal modes, non-success responses, failed writes, and replies retired by a child boundary leave the confirmed value unchanged. A matched but unusable response is terminal for that id, so a later duplicate cannot repair it into acceptance.

The acknowledgement decoder is separate from `controlAckLine`. Widening the posture-gate target would let an invalid echoed mode disturb its existing id/subtype decode even though turn admission deliberately does not depend on the echo. The confirmed-mode decoder has no logger and retains the daemon's short requested value after equality, never arbitrary response content.

### Data flow

```text
system/init.permissionMode ── bound to 256 bytes ───────┐
                                                        ├─> per-child hold ─> Runner.ConfirmedPermissionMode
SetPermissionMode(id, mode) ─> pending(id, mode, gen) ──┤
                   successful write + exact success echo┘

child begin / child exit ─> generation change + clear confirmed and pending
```

## Concurrency model

No goroutine is added. The runner caller can write or read from arbitrary goroutines, while `Parser.Write` consumes stdout on the existing forwarder goroutine.

The permission-mode hold has one leaf mutex and is never acquired with `Runner.mu`, `restartMu`, `PostureGate.mu`, a logger, or an I/O call. Registration and mode reads are short mutex sections. The potentially blocking stdin write happens after registration and outside every hold lock.

Each pending entry has a one-shot write-verdict channel. A response that arrives after the operating-system write but before `WritePermissionMode` returns waits for that verdict; a later write error therefore wins and cannot update the hold. The entry's captured generation is rechecked under the hold mutex before committing, so an acknowledgement already removed from the map cannot land after child exit or into a successor generation.

## Error handling

- `SetPermissionMode` returns the same vocabulary and write errors as today; correlation adds no new caller-visible error.
- A failed write resolves its pending verdict false and removes the entry before returning; no confirmed value changes.
- Parser decode failures and rejected shapes are silent and content-free, matching the existing control-response contract.
- Spawn setup errors explicitly end the preinstalled child boundary before returning their existing wrapped error.
- Child exit clears state even when stdin is already nil; cleanup is state retirement, not contingent on closing a handle.

## Testing strategy

- RED first: add focused tests that show `Runner.ConfirmedPermissionMode` is unavailable before a current-child report, accepts a bounded non-empty init value, preserves the existing `SessionFacts` event, and never derives a value from runner configuration or launch state.
- Replay the exact success responses from both committed, version-pinned permission-mode captures through a fixed-path reader that validates filename/version, request metadata, response cardinality, and echoed requested mode before registering their ids. Missing or altered captures fail; nothing skips.
- Table-test correlation rejection for unknown/mismatched ids, missing/mismatched modes, malformed mode payloads, non-success responses, and duplicates, always asserting that the prior confirmed mode is unchanged.
- Use a blocking error writer to make the parser receive an exact matching success response before the write returns, then release the write with an error and prove the mode did not change. This distinguishes the write-verdict ordering from simpler registration/removal tests.
- Exercise `takeStdin` and the next child boundary to prove both confirmed and pending state clear, then feed the predecessor response and prove it cannot populate the successor.
- Assert the read and rejected-response paths issue no extra control write, do not change `PostureGate`, preserve `SessionFacts`, and record no distinctive Claude-authored mode or response sentinel.
- Run `go test -race ./internal/streamsup/...`, `go vet ./...`, and `go build ./cmd/pyry`. No live-Claude run or new capture is required; the verifier owns the full-module race gate.

## Open questions

None. The ticket and current seams determine both confirmation sources, the unavailable representation, the existing 256-byte init bound, and the child-boundary reset point.

## Documentation handoff

Pending for the documentation stage: update `docs/knowledge/features/streamsup-package.md` in “Turn I/O — envelope write + stdout parser” to document `Runner.ConfirmedPermissionMode`, its two confirmation sources, and its clearing on child exit. Keep stored spawn posture, the open or closed `PostureGate`, and the last Claude-confirmed running posture explicitly distinct.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — Claude stdout crosses one explicit boundary in `emitSessionFacts` or the confirmed-mode acknowledgement decoder. The former applies `maxPermissionModeField`; the latter requires an exact daemon-minted id and exact echo of the daemon-requested mode. The returned string remains documented as Claude's informational claim, not an enforcement capability.
- [Tokens, secrets, credentials] No findings — `nextControlID` request ids are correlation tokens already disclosed to the child, not secrets. The design never persists or logs ids, modes, payloads, credentials, or decoder errors.
- [File operations] No findings — production adds no file access. Tests read only two package-constant committed fixture paths and never accept a caller-supplied path.
- [Subprocess execution] No findings — `spawnAndWait` command construction, arguments, environment, signalling, and teardown are unchanged; only its pre-start and post-exit in-memory lifecycle hooks are extended.
- [Cryptographic primitives] No findings — the change creates no secrets, randomness, hashes, keys, nonces, or comparisons of secret data.
- [Network & I/O] No findings — the existing parser buffer remains the outer input cap, init retention reuses `maxPermissionModeField`, and a response mode can be retained only after equality with the already allow-listed requested value.
- [Error messages, logs, telemetry] No findings — the new parser paths log nothing, no returned error embeds a mode or response, and focused tests scan structured records for distinctive Claude-authored sentinels.
- [Concurrency] No findings — one leaf mutex owns confirmed and pending state; blocking I/O occurs outside it; one-shot write verdicts order early replies; and child generations prevent a taken predecessor entry from committing after reset. `takeStdin` clears the hold outside `Runner.mu`, preserving its leaf-lock rule.
- [Threat model alignment] No findings — this package-local informational read adds no relay, client frame, grant, revoke, or posture decision. Client publication and its unavailable-state semantics remain explicitly owned by downstream #2510.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-19

## Revisions

None.
