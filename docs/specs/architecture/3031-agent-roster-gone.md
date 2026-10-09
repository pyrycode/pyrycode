# #3031 — Infer gone agents from complete refreshed task rosters

## Files read

- `cmd/pyry/agent_history.go` → `observeAgentHistory`, `observeTaskIdentity`, `recordAgentFact`, `validAgentHistoryFact`: durable attribution and validated receipt seam.
- `cmd/pyry/interactive_turn_v2.go` → `convTurnState`, `selectConversation`, `releaseConversation`, `handleForSource`: single-drain source/incarnation isolation and retained state after main completion.
- `cmd/pyry/session_background_task_hold.go` → `retainStart`, `enrichedRoster`, `Sink`, `childExited`: bounded joins, truncation propagation and unlocked forwarding.
- `cmd/pyry/history_projection.go` → `historyEntryShown`: explicit raw visibility independent of transport eligibility.
- `cmd/pyry/legacy_runtime_receipts.go` → `legacyRuntimeReceipt`, `legacyHistoryReader`: validated nonvisual projection and independent legacy unread target.
- `cmd/pyry/agent_history_test.go` → `testAgentFacts`, `TestAgentHistory_LegacyCompatibility`, `TestAgentHistory_ReceiptValidation`, `TestAgentHistory_StorageFailure`: real-store and legacy fixtures to extend.
- `docs/knowledge/features/history-package-producers-runtime-lifecycle.md` § Agent/task attribution and reported endings: launch results do not end linked tasks; preserve durable lifetimes across main completion.
- `docs/knowledge/features/history-package-producers-legacy-compatibility.md` § Legacy eligibility and explicit visibility: raw shown metadata never grants transport eligibility or receipt-only sight.
- `docs/knowledge/features/history-package.md`, `history-package-producers.md`, `docs/knowledge/INDEX.md`, `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: storage boundaries and behavioral test conventions.
- `docs/knowledge/decisions/042-daemon-built-thread.md` → decision 3: gone explicitly means unknown outcome; no refresh supplies no ending.
- QMD search in `pyrycode-docs` for gone/complete roster: existing #2525 observations do not justify synthetic refreshes.

## Context

The durable observer records links and reported endings but cannot retire a linked
agent when Claude reports its absence. Add that inference under ADR 042; session
closure remains #3032. No additional decision record is needed.

## Design

Retain compact observed call facts (including known parent), complete task links,
task terminal evidence and terminal call markers in `convTurnState`. Keep these
within the existing conversation/source/incarnation lifetime. Supplied nonempty
task status and usable denial identity suppress inference even before a link;
tool results remain launch evidence after linking. Repeated starts/links and late
reports cannot clear terminal markers.

On an incoming roster, infer against previously known links before observing its
rows. A complete roster has zero dropped rows and a nonempty, untruncated task ID
on every row. Compare task IDs, never call IDs. For each omitted task with an
observed Agent/Task call and no terminal evidence, append `background_task_gone`
with the complete task/call IDs, known parent, tool name and `status: gone`.
The type denotes an inferred unknown outcome. The normal append seam supplies
captured source, minted durable lifetime, timestamp and explicit shown visibility.
Mark both task and call terminal to prevent duplicate inferred endings.

`retainStart` must reject missing/truncated originating task IDs before enrichment;
existing call truncation markers remain intact. Description truncation is harmless.
Reads, pruning, join eviction and child-reset housekeeping do not call inference.
Register the new fact in validation and visibility so successful appends use the
existing receipt path; raw payloads never become legacy transport types.

No overlapping feature branch touches the planned production files at planning.
Sizing: one deliverable, four acceptance criteria, zero exported types/interfaces,
zero consumer signature updates, fewer than ten inference reject branches, about
600 total written lines including this plan and tests (ceiling 800).

## Concurrency model

The existing drain exclusively owns emitter state; no goroutines or locks are
added. The hold retains its leaf mutex and releases it before forwarding. Existing
concurrent retained reads/reset cannot trigger emitter observation or inference.

## State transitions and identity reuse

| Event | Race-enabled test |
| --- | --- |
| Empty/complete refresh omits a linked agent; repeat refresh and late start/report | `TestAgentHistory_GoneRosters` |
| Refresh precedes link/call; terminal report or denial precedes link | `TestAgentHistory_GoneRosters` |
| Main-turn completion and launch result retain eligibility | `TestAgentHistory_GoneRosters` |
| Incomplete roster/start or description-only truncation | `TestAgentHistory_GoneRosters` |
| Conversation, source, incarnation and restarted-emitter reuse of IDs | `TestAgentHistory_GoneIsolation` |
| Retained reads, eviction, pruning and child reset without qualifying refresh | `TestAgentHistory_GoneHousekeeping` |

## Error handling

Use existing best-effort append/mint behavior and content-free logs. Nil/failed
storage confers no receipt; relay-disabled operation still stores facts. Unknown
or malformed facts remain read barriers. No retry, extra durable log or live
refresh mechanism is introduced.

## Testing strategy

Write scenario tests through `sessionBackgroundTaskHold.Sink` and the production
emitter, observe their failure before implementing, and inspect warm/reopened raw
stores. Extend existing legacy/storage fixtures for gone facts, receipt validation,
durable IDs/timestamps, gates, unread targets and absence of receipt-only read
advancement. Run focused tests, `go test -race ./cmd/pyry/...`, `go vet ./...` and
`go build -o /tmp/builder-3031/pyry ./cmd/pyry`. The verifier owns `make check` and
the full-module suite.

## Open questions

None. Any nonempty reported task status is terminal under the existing contract.

## Documentation handoff

Pending documentation stage: update `docs/knowledge/features/history-package-producers.md`,
“Producers (#2114, #2115)”, to state that gone means unknown outcome and requires
a refreshed complete roster received after a complete join is known. No refresh
or join pruning alone implies no ending. Describe source/lifetime isolation,
shown visibility and legacy receipt/unread accounting under #3026/#3029,
distinguishing raw visibility, legacy unread targets and foreground presentation.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `observeTaskIdentity` and roster completeness checks reject missing/truncated identities. MUST FIX addressed in design: `retainStart` must not enrich a complete row from a truncated originating task ID.
- Tokens and cryptography: reuse `recordAgentFact`'s minted random lifetime; no credentials or new cryptographic operation.
- File operations: use `appendConversationHistory` and the existing history store; opaque task IDs never become paths.
- Subprocesses: observe typed bounded events only; no execution or refresh requests added.
- Network/I/O: reuse bounded roster events and `publishLegacyHistory` recipient gates; raw facts remain excluded and malformed facts receive no receipts.
- Errors/logs: no IDs, status, prose, payloads or filesystem error text enter new logs; existing content-free append discriminants remain.
- Concurrency: drain-only inference and unlocked hold forwarding avoid shared-state races and lock-order changes; storage failure remains best effort.
- Threat model: authenticated legacy delivery retains existing gates and does not grant presentation/read advancement. Session closure is deliberately out of scope, owned by #3032.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-09

## Revisions

- 2026-10-09: Ordering tests exposed two details needed to retain all known evidence.
  `releaseConversation` must keep a usable denial received before a sealed source's
  first observed launcher, even before a lifetime is minted. A result that first
  supplies the parent call identity must enrich the retained call for later gone
  facts. Neither change alters existing mapped reports or opens sealed main work.

- 2026-10-09: Verifier finding 1 showed that unsealed child respawns reuse the
  source/incarnation while `closeRuntimeSource` and `closeForConversation` cleared
  only the lifetime ID, retaining stale inference maps. Both now call
  `retireAgentHistory` to clear call facts, task links/outcomes and terminal call
  markers together with the lifetime. Sealed runtime closure retains predecessor
  evidence. `runtime_history.go` → `closeRuntimeSource` was read for this repair.
  `TestAgentHistory_GoneChildLifetimeReset` covers both reset paths, reused IDs
  after inferred/reported/denied endings, and old links with unrelated denials;
  `TestAgentHistory_GoneSealedLifetime` covers retained eligibility and terminal
  evidence across sealing. Both inspect warm/reopened stores under `go test -race`.
  Security re-review: PASS; drain ownership, storage/receipt boundaries and
  content-free logging remain unchanged, with no new input or I/O path.
