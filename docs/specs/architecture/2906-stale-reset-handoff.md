# Failed resets retain a visibly stale handoff

## Files read

- `cmd/pyry/session_reset.go` → `wrapUp`, `wrapUpTextContext`, `storeNote`: bounded coordinator, failure logs and note admission.
- `cmd/pyry/wrapup_capture.go` → `wrapUpReply.waitOutcome`: terminal failure is independent of partial text.
- `cmd/pyry/main.go` → `activeSessionStarter.resetThenRotate`: completion and rotation proceed after skipped handoff.
- `cmd/pyry/conversation_agent_switch.go` → switch handover: retain the reply/failure contract for this separate consumer.
- `cmd/pyry/conversation_handover.go` → handover storage: continue using `storeNote` unchanged.
- `cmd/pyry/session_reset_test.go` → `newResetFixture`: real capture/tracker with injected coordinator seams.
- `cmd/pyry/dormant_reset_test.go` → `TestActiveSessionStarter_RetainedDormantPoolWritesFreshNote`: retained dormant rotation and actual prompt proof.
- `internal/sessions/handoff.go` → note storage: preserve bounded note bytes, canonical paths and atomic writes.
- `internal/sessions/systemprompt.go` → `handoffNoteFor`, `writeComposedPrompt`: safe note reads and both refresh funnels.
- `internal/sessions/pool.go` → `Pool`: add a dedicated freshness lock separate from lifecycle locks.
- `internal/sessions/systemprompt_handoff_test.go` → activation/rotation proofs: assert actual appended prompt files.
- `docs/knowledge/features/sessions-package.md`: owning package map.
- `docs/knowledge/features/sessions-package-key-types-handoffnote-store.md` § The wrap-up's write side: failed older-note reads must not invalidate successful new wrap-ups.
- `docs/knowledge/features/sessions-package-key-types-writesystemprompt-systemprompttext.md` § Carrying the conversation's handoff note: read on every refresh, never freeze the note on a session.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md` § The wrap-up turn, and the reply's tense: asynchronous rotation and single daemon deadline.
- `docs/knowledge/features/development-verification.md`: prove partial-error rejection and asynchronous completion independently.
- `CODING-STYLE.md`: structured content-free logs, race checks and atomic persistence.

## Context

A failed client reset currently carries an older note into its successor without identifying its age. Worse, a terminally failed wrap-up can replace that note with partial assistant text. Preserve recoverable context while making its uncertain freshness explicit. No decision record is needed.

Sizing: one observable failure fallback, four acceptance criteria, about 650–750 written lines including tests/plan, no new exported types, one new pool method with one production consumer, and at most ten coordinator rejection stages. No feature branch currently overlaps the proposed production files. The #2486 analogue supplies the existing bounded coordinator fixture; this ticket reuses it instead of adding a daemon harness.

## Design

- Add `Pool.MarkHandoffNoteStale(id) error`, exposed to the reset through an optional narrow capability so existing handover/test-store interfaces need no migration.
- Mark freshness stale before the ordinary reset attempts wrap-up. Keep the old `.txt` file unchanged. Use separate per-conversation freshness metadata, with a positive certificate containing the digest of the bounded stored note. Missing, malformed, unsafe or unreadable metadata means stale.
- `WriteHandoffNote` invalidates freshness before replacing bytes, then publishes a matching fresh certificate after successful note persistence. A failed replacement cannot reuse the old fresh certificate. Serialize freshness updates using a dedicated mutex; retain an in-memory stale override when metadata invalidation fails.
- Ordinary reset rejects `failed` even when `ended` and partial text are present. Agent-switch `wrapUpText` still returns partial text and the terminal-failure flag for its existing fallback policy.
- The pool's refresh composition inserts fixed daemon-owned stale text before the handoff fence only when the existing admission predicate renders a usable note. Preserve pure composer signatures and safe note reads.
- Success reports `written`; every failed/disabled write reports `skipped` as before. Warn classifications identify activation, readiness, idle, delivery, completion, terminal failure, admission, persistence or metadata failure without logging raw errors.

## Concurrency model

No new goroutines. Reset uses its existing per-conversation claim and one daemon-cancelled, clamped deadline. A dedicated pool freshness mutex serializes invalidation/write/certificate publication and reads; do not acquire pool lifecycle locks while holding it. Prompt I/O remains outside `Pool.mu`. Separate conversations keep independent metadata and stale overrides.

## Error handling

Reset never aborts rotation because wrap-up failed. All freshness uncertainty renders stale, while absent/unusable notes render no section. Metadata invalidation failure warns and keeps the in-process stale override; a new-note write may not claim freshness without its certificate. Existing local filesystem operations retain the store's atomic-write semantics and permissions. Failed old-note reads only remove pruning context and do not prevent a successful replacement.

## Testing strategy

First change the existing partial-terminal-error assertion and run it red. Add table-driven coordinator-to-pool rotation proofs with older bytes for activation/readiness/idle/delivery/completion/terminal/admission/write failures; check Warn event classification, skipped outcome, unchanged bytes and the actual prompt file. Reuse the retained dormant fixture for successful recovery. Pool proofs cover delayed activation, recreation before activation, unknown/unsafe metadata, no usable note, isolation and successful recovery. Run race tests on `cmd/pyry` and `internal/sessions`, then `go vet ./...` and `go build ./cmd/pyry` (output outside the worktree). Full-module and live gates belong to the verifier/dispatcher.

## Open questions

None. Freshness is storage metadata, not a wire-protocol change.

## Documentation handoff

Pending for the documentation stage:
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md`, “The wrap-up turn, and the reply's tense”: failed reset preserves older note bytes, labels any injected older note stale and potentially incomplete after delayed activation/restart, and success clears the warning.
- `docs/knowledge/features/sessions-package-key-types-writesystemprompt-systemprompttext.md`, “Carrying the conversation's handoff note”: the same preserved-bytes, stale/incomplete, delayed activation/restart and successful recovery contract.
- `docs/knowledge/features/sessions-package-key-types-handoffnote-store.md`, “The wrap-up's write side”: update the matching failure fallback description.
- `docs/protocol-mobile.md`, “New session (v2)”: describe this failure fallback while retaining `written`/`skipped` semantics.

## Security review

**Verdict:** PASS

**Findings:**
- [Trust boundaries] Note text remains untrusted at `FencedHandoffNote`; stale framing is a fixed daemon constant outside the fence. Partial error text never reaches ordinary reset storage.
- [Tokens/secrets] No credentials or tokens added. Certificate contains a SHA-256 digest, never note text; neither is logged.
- [File operations] Canonical conversation IDs gate metadata paths. Reuse atomic tempfile/sync/rename writes, 0700 directory and 0600 files. Lstat regular-file checks and bounded reads refuse symlinks/FIFOs; same-uid swaps have the existing private-directory threat boundary.
- [Subprocesses] Existing runner lifecycle and daemon deadline unchanged; no shell or argv changes.
- [Cryptography] Standard SHA-256 only for byte equality, not authentication or secret comparison.
- [Network/I/O] No network reads added. Metadata reads are bounded; existing note-size/admission limits remain.
- [Errors/logs] Fixed event classifications only; metadata/store errors can contain host paths and must never be logged raw.
- [Concurrency] Dedicated freshness mutex; no new goroutines or lifecycle lock inversion. Crash between invalidation and successful certificate publication leaves stale metadata.
- [Threat model] Existing paired-client reset authority is unchanged. No new externally supplied framing or storage path.

**Reviewer:** builder (self-review)
**Date:** 2026-10-06
