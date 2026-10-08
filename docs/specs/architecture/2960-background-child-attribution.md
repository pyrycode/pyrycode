# Background child events outlive the main turn

## Files read

- `cmd/pyry/interactive_turn_v2.go` → `HandleFor`, `ensureDeltaLane`, `emitMappedAt`, `endTurn`, `releaseConversation`, `flushAll`, `closeForConversation`: lifecycle, coalescing and cleanup must agree.
- `cmd/pyry/interactive_turn_v2_parent_lane_test.go` → parent lane tests: preserve independent IDs, sequences and arrival order; replace the old turn-end reset expectation.
- `cmd/pyry/interactive_turn_v2_parent_thinking_test.go` → `TestInteractiveTurnEmitterV2_ParentThinkingPreservesLifecycle`: child thinking remains unpublished.
- `cmd/pyry/interactive_turn_v2_history_test.go` → `historyEntries`, `assertLogMatchesRing`: reuse complete ordered history comparisons.
- `cmd/pyry/stream_turn_drain.go` → `startStreamTurnDrainV2`: existing exit and teardown paths call `closeForConversation` on the sole writer goroutine.
- `internal/turnevent/event_turn.go`, `event_denial.go` → child parent keys and parentless progress/denial join keys.
- `internal/turnbridge/outbound.go` → `MapEvent`: preserve existing payloads while choosing the attribution context.
- `docs/knowledge/INDEX.md`, `features/streamsup-package.md`, `features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md`: conversation state and the shared timer must preserve isolated ordered publication.
- `docs/knowledge/features/development-verification.md`: prove tests fail before the fix; compare live payloads against ring and history.
- `docs/knowledge/decisions/042-daemon-built-thread.md` § Sessions, agents, messages, read marks: sub-agent events never open or close a main turn. This implements that existing rule for event-stream clients.
- `CODING-STYLE.md`, `docs/protocol-mobile.md` § Security model: conventions and the existing relay threat boundary.

## Context

Background children currently open or change the main turn, lose text identities at main closure, and can be reattributed to later turns. Keep child attribution independently until the producing session exits or is torn down. No new decision record is needed.

One deliverable, four acceptance criteria. Forecast: about 500–650 written lines including tests and this plan; zero exported types, zero consumer signature updates, fewer than ten rejection branches. The #2330 analogue added 311 lines and removed 21; this fix adds tool attribution and teardown coverage to the same lane machinery. No fetched feature branch overlaps the planned files. Recount before plan commit remains within all five limits.

## Design

Keep per-conversation launcher origins for observed Agent/Task starts, and child tool origins keyed by tool ID. Child tool starts/results resolve their origin from their parent launcher; unknown launchers use the parent's lazily minted child lane identity. Preserve the first observed attribution for each child call. Observed nested launchers inherit their child's tool origin.

Parent-bearing text and tool frames bypass the main opener and state transition. Known child progress and denial use the saved child tool origin without adding a parent wire field. Empty-parent main text/tools keep their lifecycle behavior; unknown progress/denial keep their existing behavior. Child text continues using its own parent-keyed delta ID and sequence, separate from the originating main ID.

`endTurn` clears only main lifecycle fields. `releaseConversation` retains state while child lanes or origin maps exist. `closeForConversation` flushes pending content, closes an open main turn as before, and clears all child attribution even when already idle. Idle cleanup emits no idle transition or turn end. Continue using one coalescing buffer and the common `emit` history/ring/fan-out path.

## Concurrency model

No new goroutines or locks. All attribution and timer delivery remain on the existing single stream-drain writer. The existing phase snapshot mutex continues to protect only published main phases.

## Error handling

Reuse `ensureDeltaLane` crypto-random identity failure behavior: drop the affected child content without opening a main turn, partial child tool state or logging child identifiers. Existing mapping, history and transport error handling remains intact.

## Testing strategy

Write failing regressions first. Exercise observed Agent launches with child calls before/after main closure, progress/denial/result identity agreement, repeated child text flushes and later main thinking/responding turns. Exercise unknown launchers and result-first child attribution while idle and thinking. Compare exact live/ring/history payloads and order. Assert phases never change from child input, per-conversation isolation with identical join keys, and idle/open teardown flushing and attribution release without synthetic results. Update prior child-only tests to flush explicitly and retain lane identities across main ends. Existing emitter lifecycle and coalescing tests cover empty-parent main behavior.

Run `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build -o /tmp/builder-2960/pyry ./cmd/pyry`. The dispatcher owns the full-module gate.

## Open questions

None. Unknown launcher attribution is fixed at first child observation and is not borrowed from whatever main turn is currently active.

## Documentation handoff

- Pending documentation stage: `docs/protocol-mobile.md`, “Interactive events (v2, capability-gated)” / `assistant_delta`: replace the claim that main `turn_end` closes all child lanes; background lanes retain their own IDs/sequences without a main turn.
- Pending documentation stage: the same section under `tool_use`, `tool_result`, `tool_progress`, `tool_denied`: document retained originating main IDs and the nonempty parent-keyed fallback for unknown origins.
- Pending documentation stage: `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md`, paragraph beginning “The emitter's turn state is per conversation”: describe child attribution surviving main closure and cleanup at teardown.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] Child keys are subprocess-authored display attribution, never authority. `HandleFor` selects the producing conversation before key lookups; tests reuse keys across conversations to detect leakage.
- [Tokens and cryptography] No credentials or cryptographic protocol changes. Child identities reuse `conversations.NewID` and its crypto-random generation; they are display IDs, not capabilities.
- [File operations] No new file operations. Content continues through `emit` and the existing history store using the daemon-resolved conversation ID, never a child key as a path.
- [Subprocesses] No new execution or environment changes; child identifiers are never commands.
- [Network and I/O] Existing `emit` interactive capability filter, encrypted transport, text splitting and upstream field bounds remain shared. No new socket reads or frame shapes.
- [Errors, logs, telemetry] IDs and child prose never enter new diagnostics. Mint failure uses the existing content-free warning in `ensureDeltaLane`.
- [Concurrency] Maps and cleanup are drain-only; no new goroutines, locks or shutdown paths. Teardown releases attribution even with no open main turn.
- [Threat model] This changes grouping of untrusted display content only; client inert rendering and relay authentication/encryption remain the existing protocol obligations. It grants no permission or execution capability. Attribution retention lasts only through the existing session lifetime.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-08

## Revisions

- 2026-10-08: The package race check exposed `TestStreamTurnDrainV2_AttributedTextExcludesThinkingAndSignature` in `cmd/pyry/stream_turn_drain_test.go`, whose child-only fixture expected a synthetic main turn. Read that test and `startStreamTurnDrainV2`'s timer/shutdown loop; update the fixture to receive only a timer-delivered child delta and join the drain before asserting no main lifecycle state or extra frames. Production design is unchanged. The additional test edit keeps total written work below 800 lines.
