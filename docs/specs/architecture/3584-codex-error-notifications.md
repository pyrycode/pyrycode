# Codex error notifications (#3584)

## Files read

- `internal/codexsup/translate.go` → `Translate`, `turnCompleted`, `errorCategory`, `warningBanner`, `truncate`: notification lanes, existing failure reports and bounded prose.
- `internal/codexsup/translate_test.go` → `TestHandBuiltFramesMatchSchema`, `replay`: schema validation and fixture replay.
- `internal/codexsup/translate_warning_test.go` → warning mapping and malformed-input tests: pointer decoding distinguishes missing/null from empty prose.
- `internal/codexsup/codex_app_server_protocol.schemas.json` → `ErrorNotification`, `TurnError`, `CodexErrorInfo`: required identities, boolean, nested message and nullable optional details.
- `internal/streamsup/parser_system.go` → `emitAPIRetry`, `clearAPIRetry`: every retry notification emits an active update; resumed content or completion clears once before output.
- `docs/knowledge/features/codexsup-package.md` → translator partition and usage: one serial translator per app-server spawn, with per-turn usage and reroute state preserved.
- `docs/knowledge/features/turnevent-package.md` and `docs/protocol-mobile.md` § `banner` / Security model: existing status events and inert provider-prose boundary.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification.md`: focused race checks and schema-backed hand-built proof.

## Context

Codex error notifications currently become parser-gap diagnostics. Users need retry status for transient failures and a visible explanation for other failed turns. One deliverable: translate this notification and clear the retry status at resumed output or completion. No decision record is needed.

## Design

- Move `error` exclusively into `mappedMethods`. Decode through an `errorNotification` method with pointers for required `error`, `message`, `willRetry`, `threadId` and `turnId`; invalid JSON, non-object values, missing/null required fields and wrong decoded types use `undecodable` without changing retry state. Empty required strings remain schema-valid.
- For `willRetry: true`, set one translator-local retry latch and emit exactly one `ApiRetry{Active: true}` per notification. Counters remain zero because the pinned schema supplies none.
- For `willRetry: false`, emit one error `Banner`, with `StopsTurn: false`, combining `message` and nonempty `additionalDetails` with a newline, then using `truncate` at `maxBannerText` including its `Truncated` result.
- Suppress the permanent-error banner for `usageLimitExceeded` and `contextWindowExceeded`, using `errorCategory`. `turnCompleted` already emits `RateLimited` for the former and a max-tokens `TurnEnd` for the latter. All other permanent errors get a banner; completion still supplies the existing error category and single `TurnEnd`.
- Keep `turnCompleted` unchanged. Wrap the current notification dispatch in `Translate` so an open latch prepends one inactive retry event when translated text, thought, tool start/update or turn end resumes. Warnings, banners, usage, ignored methods and undecodable notifications do not clear it. Clearing depends on emitted events, so future mapped tools inherit the rule.
- Preserve model, usage and reroute state; no new exported types, dependencies or consumer changes. Overlap with #3583 is additive: it maps web-search items in `item`, while this ticket wraps dispatch and maps errors.

## Concurrency model

No goroutines, channels or locks are added. `Translate` runs serially on the notification caller's goroutine, as before. The retry latch belongs to one translator/app-server spawn on one Codex thread and retains no peer-chosen identifiers.

## State transitions and identity reuse

| Event | Behavior and race test |
| --- | --- |
| Repeated retry notification | One active update each; `TestTranslateErrorRetryLifecycle`. |
| Text, reasoning or tool resumes | One inactive update before content, no repeated falling edge; `TestTranslateErrorRetryLifecycle`. |
| Completed, failed or interrupted turn | One inactive update before existing terminal events; `TestTranslateErrorRetryLifecycle`. |
| Retry then permanent error or malformed/unrelated notification | Banner/fallback does not consume latch; `TestTranslateErrorRetryLifecycle`, `TestTranslateMalformedErrors`. |
| Another retry episode, including reuse of turn ID | Reopens after clear; `TestTranslateErrorRetryLifecycle`. |
| Respawn / new translator | Starts inactive; `TestTranslateErrorRetryLifecycle`. |

## Error handling

The bounded `UnrecognizedUndecodable` fallback is the only new decode rejection path. Optional details accept null or absence, but reject other non-string types. Error prose and metadata are never logged or acted upon; the outer transport keeps its existing frame-size cap.

## Testing strategy

Write tests first and observe the missing mapping fail. Add hand-built error frames under `testdata/handbuilt` so the existing schema test validates them. Table tests prove retry values, event ordering and rearming, permanent-error suppression plus unchanged completion, message/details composition, ASCII and multibyte boundary cuts, malformed required fields and bounded fallback. Compare completed-turn events and retained usage/reroute state with a control translator. Run the affected `internal/codexsup` package under race through test admission, then `go vet ./...` and `go build ./cmd/pyry` on Linux. The verifier owns full-module acceptance; no live error capture is claimed.

## Open questions

None. The suppression rule follows the existing `turnCompleted` variants; retry counters are unavailable in the pinned schema.

## Documentation handoff

- Pending documentation stage: `docs/knowledge/features/codexsup-package.md`, section “The translator's three-list partition”: describe `error` mapping to active/inactive `ApiRetry` and bounded non-stopping error banners, including the two suppressed completion-owned categories.
- Pending documentation stage, same path and section: describe zero retry counters, optional details, the 4096-byte UTF-8-safe cap and `Truncated`, malformed-input fallback and unchanged terminal reporting. State that the new notification examples are schema-derived, not live captures.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries / threat model] `errorNotification` decodes untrusted subprocess data into existing display events. Prose stays inert and provider-attributed under `docs/protocol-mobile.md` § `banner`; identities have no routing or authorization effect.
- [Tokens / errors / logs] No credential generation, storage or logging is added. Message, details, misalignment and identity text never enter logs. Invalid payloads retain only the existing bounded diagnostic display fallback.
- [File operations / subprocesses / cryptography] The mapping performs no file access, command execution or cryptographic operation; peer text cannot reach any such sink through this change.
- [Network and I/O] Reuse the transport frame cap; cap joined banner text at 4096 bytes and fallback at 16384 bytes with UTF-8-safe cuts. No new socket or endpoint is added.
- [Concurrency] One serial boolean latch is bounded independent of peer identities. Failed decoding leaves it intact; clearing happens only before an emitted content/terminal event. No shutdown path changes.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-17

## Revisions

- 2026-10-17: The current issue body spells out the documentation handoff in more detail than the dispatch prompt. Carried those exact pending topics above; the implementation contract is unchanged. Final sizing is about 355 written lines, zero new exported types and zero consumer changes, five acceptance criteria and fewer than ten distinct new decision/reject branches, within all limits.
