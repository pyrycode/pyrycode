# Retain Claude agent/task attribution and reported outcomes (#3030)

## Files read

- `cmd/pyry/interactive_turn_v2.go` → `handleForSource`, `rememberLauncher`, `endTurn`, `releaseConversation`: captured source selection and child attribution retention.
- `cmd/pyry/runtime_history.go` → `recordRuntimeFact`, `closeRuntimeSource`: append/receipt seam and producer-incarnation teardown.
- `cmd/pyry/history_projection.go` → `historyEntryShown`, `legacyHistoryType`: explicit visibility and closed legacy vocabulary.
- `cmd/pyry/legacy_runtime_receipts.go` → `legacyRuntimeReceipt`, `legacyHistoryReader.LatestDisplayableEntryID`: validated receipts and #3029 unread classification.
- `cmd/pyry/conversation_history.go` → `appendConversationHistory`, `newHistoryPager`: best-effort appends and bounded page projection.
- `cmd/pyry/legacy_runtime_receipts_test.go` → `testLegacyCheckpoint`, `testLegacyLatest`, `TestLegacyRuntimeReceipts_LiveReplay`: independent legacy accounting fixtures.
- `internal/turnevent/event_task.go`, `event_turn.go`, `event_denial.go`: bounded report fields and incomplete-ID indicators.
- `docs/knowledge/features/history-package.md` and `history-package-producers.md` → Producers and legacy visibility: receipts count independently of shown, without granting presentation.
- `docs/knowledge/features/development-verification.md` → Prove that tests distinguish the change: test background reports from idle as well as during a main turn.
- `docs/knowledge/decisions/042-daemon-built-thread.md` → Sessions, agents, messages, read marks: launch results differ from background outcomes; Codex has no agents.
- `CODING-STYLE.md`: passive emitter ownership, content-free logs and contract comments.

## Context

Mapped reports already retain prose and status, but lack durable call/task attribution across runtime lifetimes. #3029 is merged and its receipt/unread contract is the baseline. Add facts in the existing history log; no fold, second durable log, roster disappearance inference or session-ending inference. ADR 042 already supplies the decision record. No overlapping feature branches were found for the touched existing files.

## Design

Add `agent_history.go` in `cmd/pyry`, with an unexported attribution payload and observer called from the captured-source emitter. It writes six raw types: `agent_call_observed`, `agent_call_result`, `agent_call_denied`, `background_task_observed`, `background_task_linked`, `background_task_outcome`.

Each fact carries conversation, occurrence time and a minted producer lifetime ID. Store metadata retains the captured Claude session source. The lifetime belongs to the conversation/source/incarnation state, survives `endTurn`, and is retired at runtime/session teardown. UUID minting makes different lifetimes distinguishable after reopening and across daemon restarts, even when incarnation counters restart. Missing source is not guessed; only an explicitly captured Claude producer creates agent/task facts. Codex creates none.

Agent/Task starts retain tool ID, name and observed parent. A small call-attribution map retains only these facts to identify subsequent results and denials, never inputs or prose. Completed/failed tool results retain their reported status. Their meaning is conditional: a foreground result establishes finished/failed; any complete task-to-call link in the same lifetime makes that result a launch result instead. A denial independently establishes refusal, so a later failed result cannot make the call unfinished. No eager fold or irreversible outcome classification is introduced.

Every usable background report records its task identity, even when first seen in update, progress or roster. Starts and enriched roster rows additionally record complete task-to-call links. A task update with nonempty reported status adds a task outcome, preserving open status vocabulary. Empty-status updates, patches, progress, rosters and main-turn end add no outcome. Late links join earlier launch results and task outcomes without replacing either report. Repeated observations are evidence, not new lifetimes.

Missing or truncated/dropped join fields are omitted independently. A usable task ID with an unavailable call ID still records task identity; an unusable task ID produces no joinable task fact. Description truncation is irrelevant to identity. Original mapped reports and routing remain unchanged.

Identity/link facts are explicitly hidden; result/denial/task-outcome facts explicitly shown. Extend the existing receipt validator for these known shapes, requiring their identities, timestamps and type-specific fields. Raw payloads never enter the legacy vocabulary. Successful appends publish the same nonvisual info-banner receipt with original durable ID/timestamp through existing recipient/ring gates. #3029 automatically counts valid receipts independently of raw shown; malformed/unknown facts stay barriers.

## Concurrency model

No new goroutines or locks. The existing drain exclusively selects and mutates emitter attribution state. Store/ring concurrency retains their existing synchronization; source is captured before fan-in.

## State transitions and identity reuse

| Event | Contract | Race-enabled test |
| --- | --- | --- |
| Result before/after task link, outcome before link | Preserve all reports; link selects launch meaning | `TestAgentHistory_ReportedOrdering` |
| Denial plus failed result | Both retained; refusal remains terminal | `TestAgentHistory_ReportedOrdering` |
| Main turn ends, then task reports/progress | Attribution survives; background reports do not change phase or open turn | `TestAgentHistory_ReportedOrdering` |
| Same IDs across conversations/sources/incarnations | Distinct durable lifetime identities | `TestAgentHistory_Isolation` |
| Runtime closure then reactivation with same session | New lifetime, including after emitter restart/store reopen | `TestAgentHistory_Isolation` |
| Repeated observation, incomplete link, description-only truncation | Stable lifetime; only complete join fields retained | `TestAgentHistory_IncompleteIdentities` |
| Codex/unknown producer | No agent/task facts | `TestAgentHistory_Isolation` |

## Error handling

Mint/marshal failures log only fixed discriminants. Nil/failed history writes remain best effort and produce no receipts; mapped traffic continues. Existing append error classification remains content-free. No new retry or lifecycle inference.

## Testing strategy

Write tests before production code and observe missing facts fail. Drive `HandleFor`/`handleForSource` into a real history store, checking raw payloads and explicit metadata warm and reopened. Use table-driven status/order/incomplete-ID cases and source/lifetime isolation. Compare unchanged mapped reports with `turnbridge.MapEvent`. Extend receipt coverage with independent checkpoint/latest fixtures, page/live/replay identity equality, recipient gates and no presentation from receipt-only traffic. Verify malformed facts and unknown types remain holes; nil/failed storage and history-only operation remain best effort.

Run `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build -o /tmp/builder-3030/pyry ./cmd/pyry`. The dispatcher owns `make check` after the PR opens.

Sizing after this plan: one attribution deliverable, four acceptance criteria, zero exported types/interfaces, one consumer observer call, fewer than ten rejecting/error branches; estimated 700–780 total written lines including plan and tests, within the 800-line limit.

## Open questions

None. Result facts deliberately preserve reports rather than pre-folding status; a link's presence determines launch meaning regardless of ordering.

## Documentation handoff

Pending for documentation stage: update `docs/knowledge/features/history-package-producers.md`, “Producers (#2114, #2115)”, to describe retained identities/links and reported endings, launch-result versus task-outcome meaning, source/child-lifetime isolation, and explicit visibility/legacy receipt compatibility under #3029. Distinguish raw visibility, legacy unread targets and foreground presentation.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `observeAgentHistory` consumes bounded `turnevent` fields; reject unusable join fields independently and require explicit Claude provenance. Receipt validation must require type-specific identities, not merely recognize the type.
- [Tokens/secrets] No credentials or prose retained in new facts or attribution memory. Lifetime IDs use existing `conversations.NewID` crypto randomness and are correlation identifiers, not authorization.
- [File operations] Reuse `appendConversationHistory` and the store's existing containment/permissions; no identifier becomes a path in the new observer.
- [Subprocesses] No subprocess creation or parsing changes.
- [Cryptography] No new cryptographic protocol or secret comparison.
- [Network/I/O] New raw payloads remain excluded from all legacy transports. Existing bounded five-field banner receipts and gates are reused.
- [Errors/logs] SHOULD FIX: mint and append failures must never log supplied call/task IDs, status, parent, input, description or storage paths; cover with sentinel-bearing failed-store tests.
- [Concurrency] Drain-only attribution state; teardown releases it, with no new goroutines or lock ordering.
- [Threat model] Reported status is unverified subprocess evidence, never an actuator. Roster disappearance and session-ending inference are OUT OF SCOPE for #3031 and #3032.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-09

## Revisions

- 2026-10-09: Reuse `rememberLauncher`'s existing `launcherTurns` to recognize results and denials, rather than maintaining another call inventory. Parent/name observations live in the raw start fact; results also retain any reported parent, and joins recover attribution within the lifetime.
- 2026-10-09: Sealed producers retain their lifetime and launcher attribution so a captured late report still joins the predecessor. New producer incarnations mint different durable lifetimes. Unsealed exit and conversation teardown release attribution. `TestAgentHistory_SealedReports` covers background-only sealing, delayed outcome and same-session reactivation.
- 2026-10-09: `recordAgentFact` flushes preceding buffered text before appending evidence, preserving accepted output order without opening or changing a main turn. `TestAgentHistory_ReceiptValidation` checks every fact's required receipt fields and both raw visibility values through warm/reopened legacy readers and read-mark fixtures.
- 2026-10-09: Verifier finding 1: sealed starts now retain launcher recognition before the sealed-path return, even when no originating main turn exists. Result/denial recognition uses map membership rather than a nonempty turn address. `TestAgentHistory_SealedReports` covers first starts after sealing, Agent/Task completed/failed results and denials, with/without preceding main work, against a successor reusing the call ID in warm/reopened stores. No main turn or child lane is opened; successor phase/lifetime, the existing security boundary and content-free logs remain unchanged.
