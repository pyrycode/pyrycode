# Source-bound control and requested readings

## Files read
- `cmd/pyry/daemon_live_state.go` → `admit`, `retain`, `retireLocked`, `transition`: existing generation, revision and detached cursor contract.
- `cmd/pyry/daemon_live_source.go` → `acceptEventLocked`: stream families converge on the same owner.
- `cmd/pyry/stream_turn_fanin.go` → `streamTurnSink`: offer mutex serializes capture with transitions.
- `cmd/pyry/modal_resolve_v2.go` → `Surface`, question answer/refusal, `retire`: outstanding answer identities and legacy delivery.
- `cmd/pyry/prompt_answer_history.go` → `promptOwner`: immutable answer attribution.
- `cmd/pyry/resetting_v2.go`, `new_session_starter.go` → `emit`, `resetThenRotate`: asynchronous reset lifetime.
- `cmd/pyry/session_error_v2.go` → notification closures: capture before buffered handoff.
- `cmd/pyry/reply_suggestion.go` → `publishLocked`, `startFallbackLocked`: retention must precede publication and inference must carry the original source.
- `cmd/pyry/relay_context_usage.go` → `Get`, `fresh`, `fly`, `remembered`: shared flights, cancellation, idle deferral and stored fallback.
- `cmd/pyry/session_router.go`, `session_model_selection.go`, `session_model_list.go`, `session_slash_command_list.go`, `pool_adapters.go`: provider refusal, dormant settings and model filtering contracts.
- `cmd/pyry/main.go`, `relay.go`: relay and history-only composition attachments.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md`: leaf suggestion lock and asynchronous provenance lessons.
- `docs/knowledge/features/development-verification.md`: raw JSON omission/null and actual producer size bounds.
- `docs/knowledge/decisions/042-daemon-built-thread.md`: live state stays outside folded history.

## Context
Prompts and on-demand readings must survive reconnect with the producing session,
without allowing an old asynchronous operation to restore successor state. #3077
owns relay adaptation and activation. No additional decision record is needed.
Recorded parent links are #2962 → #3076 → #3089; `needs-human:sizing` is present.
Forecast: approximately 1100–1400 written lines, zero exported interfaces/types,
no mandatory consumer migrations, five criteria. This exceeds 800 lines under
the existing grandchild exception. Potential seams would be prompt/control
retention and provider convergence. No overlapping remote feature branches found.

## Design
Extend the existing owner with canonical families for shown/dismissed prompts
and settings reply/update. Prompt identity is its original answer ID; singletons
use empty identity. Dismissals remove only their identity and preserve revision
ordering. All families participate in transition clears.
An optional daemon-local attachment captures conversation, generation and source
under `offerMu`. Provider operations reserve a family revision before asynchronous
work and expose detached source-bearing completion envelopes, preserving supplied
correlation fields. Older completions cannot replace a newer stream/update reading.
Existing provider signatures and nil seams remain usable; attachments retain their
results without changing relay handlers. Settings update completions occur only
on success. Cached results retain source evidence; stored readings without such
evidence use unresolved provenance. Context flights cannot be shared across
source generations. Suggestions retain before the publisher enumerates recipients;
fallback workers carry their captured source rather than reading it at completion.

## Concurrency model
Lock order is `offerMu` then owner mutex. Capture never holds either across a
query, worker, delivery or history commit. Producer leaf mutexes may acquire the
owner mutex, but never acquire `offerMu` while held. Existing worker cancellation
and joins remain in place; no new delivery goroutine is introduced.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Concurrent prompts; answer/timeout/dismiss | `TestControlLivePrompts` |
| Reset completion after clear; reused session ID | `TestControlLiveOperationOrdering` |
| Queued error and delayed suggestion after transition | `TestControlLiveDelayedProducers` |
| Context flight/collapse after rotation | `TestControlLiveContextFlights` |
| Settings reply/update and stream/request overtaking | `TestControlLiveOperationOrdering` |
| Independent conversations and detached snapshots | `TestControlLiveSources` |

## Error handling
Unresolved or absent producer evidence is omitted, positive no-session is null,
and known source is a nonempty string. Invalid/oversized envelopes are refused by
`validDaemonLiveEnvelope`; failed delivery never rolls retention back. Provider
refusals and failed settings writes retain nothing. Stale operation completion
still preserves legacy request behavior but cannot mutate successor retention.

## Testing strategy
Write hermetic producer/provider tests before implementation and observe failures.
Verify raw provenance, correlated equal-state completion, identity-specific prompt
retirement, transition and revision rejection, detached cursors, stored fallback,
stream convergence and actual bounded payload sizes. Run `go test -race ./cmd/pyry/...`,
`go vet ./...`, and `go build -o /tmp/builder-3089/pyry ./cmd/pyry`.
The verifier owns the full-module suite; no live Claude turn is required.

## Open questions
None. Source-bearing daemon seams are optional; relay wire integration remains #3077.

## Documentation handoff
Pending documentation stage: `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md`,
“Daemon-retained live state”: add “Control and on-demand readings” covering
outstanding answer IDs/answerability, shared logical families, captured query/fallback
provenance, unresolved stored readings and transition retirement. In “Native reply
suggestions after the result”, explain retention before delayed publication.
State relay provider installation, production activation and final producer-to-relay
proof remain pending #3077.

## Security review
**Verdict:** PASS
- Trust boundaries: SHOULD FIX: admission must validate detached envelope size including source/correlation metadata; caller correlation must never become reading identity.
- Tokens/credentials: no new credential handling or logging; prompt and suggestion content stays solely in bounded retained envelopes.
- Files: no new persistence or path operations; remembered bytes cannot establish session provenance.
- Subprocesses: existing bounded/cancellable queries and fallback workers are reused; no shell or new argv path.
- Cryptography: unchanged; sealing and negotiation are OUT OF SCOPE, owned by #3077.
- Network/I/O: owner performs no I/O; actual supported bounded payloads and clears must fit `MaxThreadEnvelopeBytes`.
- Errors/logs: no payload logging is added; delivery failures retain owner state.
- Concurrency: SHOULD FIX: capture before async work, generation-partition flights, and reserve revisions before queries; avoid suggestion-mutex-to-offerMu inversion.
- Threat model: authenticated relay handlers and capability gates remain unchanged; generation/revision checks prevent stale local state resurrection.
**Reviewer:** builder (self-review)
**Date:** 2026-10-10

## Revisions
- 2026-10-10: Preserve the legacy two-field `settingsUpdaterAdapter` through an
  embedding daemon-local `liveSettingsUpdater`; `UpdateLive` exposes the correlated
  result. `GetLive`, `resolveBoundMCPStatusReading`, `liveInventoryReading`, and
  `runSettingsReading` expose detached provider results for #3077. Prompt readings
  include detached answerability metadata, outside their unchanged payload.
- 2026-10-10: Reset operations reserve and conditionally advance family revisions
  across phases, so an overtaken same-generation reset cannot restore its falling
  edge. Bound resets share the parent emitter's delivery counter and broadcaster.
  Native suggestions use the stream capture for their payload session as well as
  retained metadata. No-relay wiring does not start absent prompts or queries.
- 2026-10-10: The actual supported question-boundary test exposed 16384 raw bytes
  expanding to a 98081-byte payload. Filed #3109 in Backlog with priority:low;
  `TestControlLiveQuestionEnvelopeBound` remains skipped pending the producer fix.
  The existing MCP envelope budget assertion remains skipped on #3091. Both are
  pre-existing producer-budget gaps; this owner rejects oversize envelopes.
- 2026-10-10: Permission answerability follows `RemoteAnswerable`: an
  interaction-required permission remains fail-closed for remote answers, so its
  retained eligibility is `!RequiresUserInteraction`. Question batches retain
  their separate, existing remotely answerable contract.
- 2026-10-10: Files-read map correction: `streamTurnSink` and `offerMu` live in
  `cmd/pyry/stream_turn_drain.go`. Also read and bound the reset lifetime in
  `cmd/pyry/conversation_agent_switch.go` → `conversationAgentSwitcher.Switch`.
  Additional race scenarios are `TestControlLiveFallbackSource`,
  `TestControlLiveResetRevision`, `TestControlLiveStreamRequestOrdering`,
  `TestControlLivePromptProducers`, and `TestControlLiveStoredAndConvergedReadings`.
  Final written work is approximately 1300 lines, under the recorded sizing exception.
