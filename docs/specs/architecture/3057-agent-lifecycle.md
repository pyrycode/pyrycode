# Agent call lifecycle (#3057)

## Files read
- `internal/thread/fold.go` → `Feed`, `addItem`, `decode`: ownership, detachment and fixed creation attribution.
- `internal/thread/main.go` → `mainWork`, `mainDecode`: launcher text splitting and ordinary Codex calls remain intact.
- `internal/thread/main_test.go` → `testMainReplay`: compare every feed partition with full replay.
- `cmd/pyry/agent_history.go` → `observeAgentHistory`, `validAgentHistoryFact`: durable observations precede mapped reports; results and task endings are separate.
- `cmd/pyry/startup_agent_history.go` → `readStartupAgentWork`: original observation references, lifetime and legacy scopes.
- `cmd/pyry/agent_history_test.go` → `TestAgentHistory_ReportedOrdering`: existing emitter/store seams.
- `docs/knowledge/features/thread-package.md` § Main-thread folding: preserve stable internal indexes and source attribution.
- `docs/knowledge/features/history-package-producers-runtime-lifecycle.md` § Agent/task attribution and reported endings, Recover agent/task work and original identities: independent task identities and saved scope.
- `docs/knowledge/decisions/042-daemon-built-thread.md` § Item model, Sessions, agents, messages, read marks: permanent agent kind, launch fields, explicit endings.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: inert recorded content and real capture proof.

## Context
Call-owned agents are the eighth ADR 042 kind. Child work parenting, shell lifecycle, cache and daemon wiring remain downstream. No new decision record is needed. No overlapping feature branches touch these files.

## Design
Add private agent state beside main-work state. The mapped Claude Agent/Task launch alone creates an agent item; durable observations establish lifetime and reference identity, never item identity. Preserve launch input, parent evidence, turn, visibility and attribution. Add `Item.EndedOrder` for the winning final, zero before it.
Groups use recorded source plus durable lifetime, or recorded legacy boundary scope when no lifetime exists. Retain independent call/task evidence, pending results, links and endings across feeds. Reconcile durable and mapped reports with matching identities/status as one outcome, retaining mapped content and the earliest evidence ID. Complete links reclassify all call results as launch fields; choose the earliest remaining genuine final. Denial remains final. Content enrichment changes revision only when observable data changes, floored at creation.
Recovery references resolve only earlier original observations with matching identity/source/lifetime. Any supplied invalid reference rejects the fact without append-scope fallback. Invalid call links leave usable task evidence intact. No child work is folded.

## Concurrency model
The existing serialized fold owner applies all state synchronously. No locks, goroutines, I/O or shutdown paths are added.

## State transitions and identity reuse
| Event | Race-enabled test |
| --- | --- |
| Reports before creation and feed companion splits | `TestAgentLifecyclePermutations`, `TestAgentDurableCompanions` |
| Late link removes provisional result; conflicting finals | `TestAgentLifecyclePermutations`, `TestAgentFirstFinal` |
| Stopping, stopped and summary-only enrichment | `TestAgentTaskEnrichment` |
| Source/lifetime reuse, legacy boundaries and restart | `TestAgentIdentityScopes` |
| Recovery appended after scope change and malformed references | `TestAgentRecoveryReferences` |
| Detached input/snapshots and malformed facts | `TestAgentInvalidFacts` |
| Recorded parser/emitter replay | `TestThreadAgentRecordedReplay` |

## Error handling
Malformed/foreign facts consume version only. Explicit non-Claude sources never create or join agents. Empty/lost identities cannot join. Missing capture fails the offline proof. Raw payloads remain inert JSON; no absence, progress or turn end infers completion.

## Testing strategy
Write offline tests first, observe missing agent rows, then implement. Use full/incremental comparisons and observable status/order/content assertions. Replay the committed #2191 Claude v2.1.259 capture through production parsing and raw interactive history, separately naming synthetic supplements if needed. Run race tests on thread and cmd/pyry, then module vet and pyry build. Full-module gate belongs to verifier; no live run.

## Open questions
None. `stopping` is active; all other nonempty task terminal words are inactive, with completed mapped to finished.

## Documentation handoff
Pending documentation stage: `docs/knowledge/features/thread-package.md`, “Agents and background work” / “Agent lifecycle”: cover call-owned creation, durable/mapped companion reconciliation, lifetime-scoped joins and legacy observation references, early reports, late launch reclassification, first-final behavior, stopping versus stopped, ended order and saved session-ending causes. Replace lifecycle-exclusion claims; child and shell work remain pending #3058/#3059.

## Security review
**Verdict:** PASS. **Reviewer:** builder self-review, 2026-10-09.
- Trust boundaries: `Feed` checks conversation ownership; agent decoding validates identities, source and original references before mutation. Payloads remain inert, and attribution cannot be overwritten by reports.
- Tokens/cryptography: no credentials, randomness or cryptographic operations are introduced.
- Files/subprocess/network: fold does no I/O. Offline proof reads a fixed committed capture through existing parser/emitter seams; no command or path comes from payloads.
- Errors/logs/telemetry: no payload logging; existing generic entry-order error remains the only returned fold error.
- Concurrency: serialized owner, copied inputs/snapshots, no added goroutines.
- Threat model: cross-source/lifetime mutation is prevented by scoped identity and reference validation; consumer wiring and transport remain downstream.

## Sizing
Approximately 950 written lines, no exported types, one integration point, four criteria. Above the line ceiling; actual grandchild #3057 → #3047 → #2961 requires building and `needs-human:sizing` is applied. Candidate independent seams: foreground calls, linked task lifecycle, recovery references.
