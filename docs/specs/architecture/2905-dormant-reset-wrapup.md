# #2905 — resume dormant conversations for reset wrap-up

## Files read

- `cmd/pyry/main.go` → `activeSessionStarter.start`, `reviveDormantBound`, `resetThenRotate`, `startFreshRunner`: used-session discrimination, confinement, exclusion and asynchronous completion.
- `cmd/pyry/session_reset.go` → `newConversationReset`, `wrapUpText`, `wrapUp`: capture, deadline and note admission.
- `cmd/pyry/conversation_handover.go` → `conversationAgentSwitcher`: its reply-only handover must retain its existing contract.
- `internal/sessions/pool.go`, `session.go` → `Pool.Activate`, `Session.Activate`: resume the bound identity through the pool lifecycle.
- `internal/streamsup/runner.go` → `WaitForPTY`, `turnTarget`, `WriteUserTurn`: activation is not stream readiness; `ErrNoLiveChild` guarantees no bytes written, including while permission posture is pending.
- `cmd/pyry/session_reset_test.go`, `new_session_reset_test.go`: capture, note and asynchronous starter fixtures.
- `internal/e2e/relay_v2_stream_new_session_dormant_test.go`: restart-first reset and conversation isolation.
- `internal/e2e/realclaude/interactive_stream_announced_reset_test.go`, `harness_daemon_test.go`: authenticated reset window, per-session prompt and daemon startup helpers.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md`: the falling edge must precede exclusion release; a bootstrap binding cannot prove successor note composition.
- `docs/knowledge/features/sessions-package.md`, `streamsup-package.md`, `e2e-harness.md`, `e2e-realclaude.md`, `development-verification.md`, `conversation-session-binding.md`, `CODING-STYLE.md`: lifecycle, verification and workspace contracts.

## Context

Previously used childless conversations rotate without asking their predecessor for a fresh handoff. A restart materialises their persisted binding without starting its child, so an older note silently reaches the successor. This ticket restores that wrap-up; #2906 owns failure-note freshness. No decision record is needed.

One deliverable: reset used dormant conversations with a fresh predecessor handoff. Estimated total written work 600–700 lines including plan and tests, zero exported types, no consumer signature changes, four acceptance criteria and fewer than ten added reject branches. No other fetched feature branch touches the proposed files.

## Design

`activeSessionStarter.start` sends live or previously used conversations to the existing asynchronous reset tail. The never-used named guard stays ahead of that tail. Persisted sessions still pass through the existing validated revive path; neither named reset nor resume changes the active cursor.

The reset target gains an optional activation closure wired to `Pool.Activate` for its resolved identity. Reset's `wrapUp` creates one daemon-owned bounded context, resolves the target and activates it when childless. Activation, delivery readiness, idle wait and reply capture consume that same context with the existing shorter override and 90-second ceiling.

Readiness is proven by delivering the actual fixed wrap-up prompt: only a resumed reset retries `ErrNoLiveChild`, which promises zero bytes written. A cancellable short polling wait handles the spawn and permission-posture windows. Capture remains armed before any delivery and a successful delivery is never repeated. Other write errors stop wrap-up. The existing note store admits the completed reply before rotation recomposes the successor prompt.

Keep `wrapUpText`'s signature and live handover behavior for agent switching. An internal context-taking helper shares capture logic; only ordinary reset enables dormant activation/readiness retries. Log dormant resume at Info with event and validated conversation ID only.

## Concurrency model

Use the existing reset tail goroutine and per-conversation claim. Resume does not block relay dispatch. All new waits select on the single reset context; daemon cancellation and the deadline release them. Preserve wrapping_up, restarting and inactive emission, late completion, and inactive-before-claim-release ordering. No additional goroutine or lock ordering is introduced.

## Error handling

Activation errors, cancellation, readiness expiry and unusable replies report skipped wrap-up and continue existing rotation/completion. Log stage identifiers without raw errors or content. Never retry a possibly partial write: only the zero-byte `ErrNoLiveChild` refusal is retryable. Prior-note behavior on failure stays with #2906.

## Testing strategy

Write failing tests before production changes. Reuse reset fixtures to prove dormant activation before delivery, readiness retries, a shared deadline, daemon cancellation, nonretryable failures and unchanged agent handover. Starter tests cover named and cursor used-childless dispatch and exclusion without a wake-up message; existing tests retain never-used and validation/confinement guards.

Extend the fake restart proof: seed an older admissible note, reset first, require old-ID resume and wrap-up before rotation, read a fresh note and successor's actual appended prompt, and reject old-ID spawns after the rotation boundary. Add a normal authenticated real-Claude restart test planting a unique fact only before shutdown, seeding an older note lacking it, resetting first after restart, and requiring written edges, fresh note and successor prompt containing the fact. Missing evidence after authentication fails.

Run race tests on `cmd/pyry`, focused tagged fake e2e tests, `go vet ./...`, `go build` with output outside the worktree, and offline tagged live-package compilation. The verifier owns full-module gates and the dispatcher owns the authenticated live gate and executed counts.

## Open questions

None. Readiness uses the existing zero-byte delivery refusal rather than `WaitForPTY`, which is a no-op on streams.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md`, “The wrap-up turn, and the reply's tense”, and `docs/protocol-mobile.md`, “New session (v2)”, state that a previously used dormant conversation is resumed for wrap-up before rotation, including after daemon restart, under the same 90-second ceiling; never-used named conversations remain inert.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] Preserve canonical ID validation and bound-session resolution; `reviveDormantBound` still refuses unknown/unbound and never-used bindings. Reply admission and fencing remain in `storeNote` and pool prompt composition.
- [Tokens, secrets, credentials] No credential changes; the authenticated test uses the existing isolated live harness.
- [File operations] Reuse validated workspace revival and the existing atomic note store and prompt writer; no client-derived new paths. Fixture files use private modes.
- [Subprocesses] Resume through `Pool.Activate`, without shell execution, raw client arguments or a user wake-up turn; existing lifecycle owns child shutdown.
- [Cryptography] No changes to encryption, keys or nonces.
- [Network and I/O] Existing encrypted frame limits remain; resumed reset consumes one daemon-cancelled deadline and stays off dispatch.
- [Errors, logs, telemetry] SHOULD FIX: resume and readiness failures must name only event and validated conversation ID, never raw errors, paths, prompt or note. Test log exclusions.
- [Concurrency] Preserve per-conversation exclusion, capture-before-write and falling-edge-before-release. Retry only zero-byte refusals under the shared context.
- [Threat model] Paired clients already authorize reset of validated conversations; no additional lookup oracle or cursor mutation. OUT OF SCOPE: stale-note handling on failed wrap-up belongs to #2906.

**Reviewer:** builder (self-review)
**Date:** 2026-10-06

## Revisions

- 2026-10-06: The fake stream child echoes its prompt, so seeding an older fenced note there would yield an inadmissible nested-fence reply rather than usable wrap-up prose. Keep that restart case's note initially absent and prove predecessor-owned stdin, stored wrap-up reply and successor composition. The retained-pool tests and authenticated restart case cover replacing a seeded older note. Observe predecessor delivery through its identity-specific stdin log rather than requiring its queued assistant delta to beat the independently queued transition on the phone wire.
