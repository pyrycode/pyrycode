# #2673 — carry context across an agent switch

## Files read

- `cmd/pyry/conversation_agent_switch.go` → `Switch`: validation, exclusion, mint/rebind and cleanup order.
- `cmd/pyry/session_reset.go` → `wrapUp`, `storeNote`, `previousNote`: bounded daemon-owned capture and shared admission.
- `cmd/pyry/session_reset_test.go` → `newResetFixture`: real capture, busy tracker, backlog and content-free failure regression fixtures.
- `cmd/pyry/resetting_v2.go` → `wrappingUp`, `restarting`, `done`: synchronous ordered status edges.
- `cmd/pyry/main.go` → `resolveBoundSession`, `resetThenRotate`: lookup without activation and falling edge before exclusion release.
- `internal/history/log.go` → `Page`, `resolveDir`: newest-first cursors, `AtStart`, per-call exact containment checks.
- `internal/protocol/interactive.go`, `messaging.go` → assistant delta, message and turn-end payloads.
- `internal/sessions/systemprompt.go`, `handoff.go` → `FencedHandoffNote`, `writeComposedPrompt`, `MaxHandoffNoteBytes`: one admission rule and bounded raw storage.
- `docs/knowledge/features/conversation-session-binding.md` § Switching to the other agent: primitive only; production caller belongs to #2674.
- `docs/knowledge/features/history-package.md` § Shape, Directory resolution, Producers: opaque storage and daemon-authored history; re-resolve paths on each call.
- `docs/knowledge/features/sessions-package.md` and `sessions-package-key-types-handoffnote-store.md`: atomic 0600 note writes; content-bearing errors must be suppressed at the consumer.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`, `docs/protocol-mobile.md` § Security model: testing and trust boundaries.

## Context

The rebind primitive loses conversational context when changing harnesses. Carry a best-effort note, recent completed exchanges and a daemon-owned log pointer through the existing stored-note composition path. No new decision record is needed. QMD search confirmed the reset/note precedents; codegraph tools did not respond, so source searches supplied symbol context. No other fetched feature branch changes the existing production files this design touches.

## Design

- Preserve `wrapUp(convID) bool` as capture followed by storing the reply alone. Extract `wrapUpText(convID) (string, bool)` for capture without a write; keep its prompt, deadline, backlog, interrupt, idle and capture ordering.
- Supply `history *history.Store` and `resetting *resettingEmitterV2` on the switch primitive. After all validations, emit wrapping-up and defer done before exclusion release. Look up the old session without reviving or activating; only a live child runs capture. Compose/store handover, emit restarting with its write outcome, then perform the existing mint/rebind.
- Use an admitted fresh completed reply as summary, otherwise an admitted previous note. Decode recent rows only in `cmd/pyry`. Walk `Page` backwards, skipping the incomplete newest tail; each `turn_end` closes an exchange. Select up to three exchanges containing user messages and main-agent assistant text, reverse exchanges and rows to chronological order, preserve all user messages and concatenate adjacent assistant deltas verbatim. Tool rows, subagent deltas and assistant-only turns contribute nothing. Any page error discards all selected exchanges.
- Add `Store.LogDir(convID) (string, error)`: validate ID, lock, reuse read-only `resolveDir`, return the absolute contained existing directory; create nothing and cache no path.
- Compose summary, labelled recent exchanges and a final pointer explaining JSON Lines segment files (schema header then one JSON event per line). Drop whole exchanges oldest first until the raw combination fits `MaxHandoffNoteBytes`. Shorten only summary at a UTF-8 boundary if summary plus pointer exceeds the cap. Omit a pointer that cannot itself fit. Admit the bounded result with `FencedHandoffNote`; on refusal try the bounded summary alone, then write nothing. Use `storeNote` for the single admitted write; write failure/disabled store leaves the previous note untouched.

## Concurrency model

Switch remains synchronous; #2674 must run it away from relay dispatch. No new goroutines. `begin` remains held through capture, composition, rebind and the falling edge. Existing capture and busy tracker own turn synchronization. History methods retain their leaf mutex. History scanning checks a bounded daemon context between pages and retains only bounded exchange text.

## Error handling

Handover failures never abort rebind. Read failures omit exchanges or pointer; inadmissible content falls back to summary. No note, turn text, path or content-bearing handover error is logged. Ordinary reset retains its existing failures and reply-only write contract. Existing switch errors and rollback behavior remain intact.

## Testing strategy

Write failing tests first. Reuse reset capture/backlog fixtures and resetting broadcaster. Fake-runner integration covers both directions, live, evicted and dormant/childless sessions, failed/timed-out/inadmissible wrap-up, first-spawn prompt fencing, validation silence, exclusion and falling edges on post-rise failures. History fixtures distinguish pagination chronology, multiple user messages, ignored rows/incomplete tails, oldest-whole-exchange dropping, UTF-8 summary shortening and hostile recent text falling back. Test path helper absence, invalid IDs, symlink root resolution and sibling/outside containment. Run race tests for `cmd/pyry` and `internal/history`, `go vet ./...`, and build `./cmd/pyry` to scratch; the verifier owns the full-module gate.

## Open questions

None. The production relay caller and wiring remain #2674's deliverable.

## Documentation handoff

- Pending documentation stage: `docs/knowledge/features/conversation-session-binding.md`, **Switching to the other agent (#2672)**: replace the statement that switching does not run a wrap-up or replay history with the live-child wrap-up, childless fallback, inline recent exchanges and log pointer. State that handover is best-effort and that #2674 owns the production wire caller.
- Pending documentation stage: `docs/knowledge/features/history-package.md`, **Shape**: document `Store.LogDir` and that switch handover reads this daemon-owned log. Explain that segment files are JSON Lines and reading them from an agent may require permission outside the workspace.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `FencedHandoffNote` admits both fresh summary and the combination; historical fence/control text triggers summary fallback. Registry validation precedes all handover effects.
- [Tokens] No credential handling added. Notes/history remain private conversation content, inside the existing handoff fence.
- [File operations] `LogDir` reuses canonical ID and exact symlink containment checks without creation. `storeNote` retains atomic 0600 files in a 0700 directory; no duplicate layout logic.
- [Subprocesses] Fixed existing wrap-up prompt and capture only; no shell or new spawn arguments. Childless sessions never activate for handover.
- [Cryptography] No cryptographic operations or key lifecycle changes.
- [Network and I/O] No new network surface. Page reads are bounded by history's existing limits; retained exchange memory and final raw note are bounded, with context checks between pages.
- [Errors/logs] SHOULD FIX: `wrapUp` currently logs `Interrupt` errors verbatim. Remove that error attribute when extracting capture, since a runner error can carry content. New history errors are omitted rather than rendered in logs.
- [Concurrency] Exclusion release is deferred before the falling-edge defer, preserving ordering on every post-rise exit. No new goroutines or nested mutexes.
- [Threat model] Relay content injection is contained by the existing handoff admission/fence. Production membership/wire checks are OUT OF SCOPE here and owned by #2674; ordinary switch validation remains in force.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-06

Sizing after plan: one deliverable, approximately 740 written lines, zero new exported types/interfaces, no changed exported signatures or consumer updates, five acceptance criteria, fewer than ten new rejection branches.
