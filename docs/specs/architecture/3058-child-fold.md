# Parented agent work

## Files read
- `internal/thread/fold.go` → `Feed`, `Items`: dispatch, stable indexes and detached snapshots.
- `internal/thread/main.go` → `mainDecode`, `mainWork`, `closeText`: validation and ordinary first-terminal folding.
- `internal/thread/agent.go` → `agentWork`, `applyAgent`: saved scopes, parent evidence and reversible launch-result classification.
- `internal/thread/agent_validation_test.go` → `TestAgentDurableParentText`: saved parent evidence must precede main classification.
- `cmd/pyry/thread_agent_test.go` → `TestThreadAgentRecordedReplay`: production parser/emitter offline capture proof.
- `docs/knowledge/features/thread-package.md` → Main-thread folding and Agents and background work: immutable creation provenance and reversible finals.
- `docs/knowledge/features/history-package-producers-runtime-lifecycle.md` → Join within a durable producer lifetime: reused routing IDs are not durable identities.
- `docs/knowledge/decisions/042-daemon-built-thread.md` → Item model and Sessions, agents, messages, read marks: parents are older creating IDs.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: deterministic tests and capture provenance.

## Context
Child output is currently excluded, while nested agents retain only call-string evidence. Fold it under older agent items without disturbing main turns. No additional decision record is needed. Background shell work remains #3059.

## Design
Reuse validated ordinary-work folding for private child lanes keyed by recorded agent source/lifetime (or original legacy scope), parent call and turn. Retain early call reports and parent evidence in that scope. Child text/tools never enter main-turn state; nested Claude/legacy launchers still use agent lifecycle folding. Supported reports can enrich parent evidence, repairing an existing item without changing creation identity, kind, order, provenance or visibility.

Maintain parent evidence beside stable internal item indexes. Resolve only to an older agent creation in the exact recorded group. Hide unresolved children from snapshots, retaining their original identity and order internally. Resolve ancestors before descendants. Ordinary calls preserve the first terminal report and original input. Child turn endings are row-free and close only their lane.

Keep intrinsic child state separate from ancestor closure. Derive inactivity from genuine agent finals on each feed, allowing a late background link to remove a provisional closure. Ancestor closure does not manufacture a successful tool result or nested-agent final. Already independently terminal children retain their outcome.

## Concurrency model
Existing owner serialization remains required; no goroutines, locks or I/O are added.

## State transitions and identity reuse
| Event | Race-test coverage |
| --- | --- |
| Interleaved parent/source/turn lanes, launches and delivered text closure | `TestChildLanes` |
| Early result, unresolved parent and later saved parent repair | `TestChildRepair` |
| Nested agent parent enrichment and ancestor ending | `TestChildNested` |
| Provisional foreground result followed by late background link | `TestChildReclassification` |
| Reused calls across producer lifetimes/legacy scopes, invalid parent joins | `TestChildScopes` |
| Recorded child calls through parser and raw history emitter | `TestThreadAgentRecordedReplay` |

## Error handling
Malformed, foreign and identity-loss facts consume version only. Unsupported task envelope fields cannot establish parent evidence. Missing, self, newer, non-agent or mismatched-scope parents stay unresolved; no current binding or timestamp fallback is allowed.

## Testing strategy
Write synthetic behavior tests first and observe failures, then compare full replay with every feed partition. Assert main independence, ordinary terminal precedence, detached snapshots, immutable provenance and creating IDs. Extend the existing offline recording proof with both Read calls, saved inputs and marker results. Run race tests on thread and cmd/pyry, vet and binary build; the verifier owns the full-module gate.

## Open questions
None. Sizing is about 900 written lines, no exports or consumer migrations, four criteria and fewer than ten reject categories. This exceeds the 800-line ceiling; #3058 is a grandchild (#3047/#2961), so the required sizing label/comment records the judgment and building continues. No overlapping feature branches touch these files.

## Documentation handoff
Pending documentation stage: `docs/knowledge/features/thread-package.md`, “Agents and background work”, add “Parent repair” covering child lanes and nested agents, main-turn independence, closure and late-link reclassification, unresolved-evidence retention, stable identity/order repair and older-parent/source/lifetime invariants. Replace parent-attributed exclusion/deferred-parenting descriptions; keep background-shell work downstream in #3059.

## Security review
**Verdict:** PASS
- Trust boundaries: SHOULD FIX: validate supported parent identities before changing pending joins; `Feed` rejects foreign conversation data and `mainDecode`/`agentWork` reject malformed or lost identities. Exact saved scope plus strictly older agent IDs prevents cross-source joins and cycles.
- Tokens/credentials and cryptography: no generation, comparison or credential access; payloads remain inert copied JSON.
- Files/subprocesses/network: production fold is supplied-entry, in-memory logic; no paths, execution, sockets or new I/O.
- Errors/logs: existing generic feed errors remain; no payload logging is introduced.
- Concurrency: serialized owner contract remains, with detached snapshots and no new goroutines.
- Threat model: this stage folds saved evidence only; transport authentication, wire size limits and cache integration remain downstream ADR 042 migration work.
**Reviewer:** builder self-review. **Date:** 2026-10-09.

## Revisions
2026-10-09: Scoped child adoption also records the original group on ordinary pending calls; otherwise a reused turn/call could import an early predecessor result. `TestChildLifetimeReuse` caught that failure. A parent repair removes the old lane association, and saved agent enrichment takes precedence over stale lane evidence (`TestChildLateRepairAndClosure`, `TestChildNestedLinkRepair`). Child turn endings retain evidence on text as well as calls; `TestChildTurnEndBeforeCreation` covers pending closure. Canonical intrinsic/derived snapshots keep malformed feeds version-only (`TestChildMalformedNeutrality`). `TestChildMainIndependenceAndSend` covers idle main state and delivered text closure. These refine the planned scope/closure contracts without new exports, consumers or lifecycle branches.
Tagged main turns use scope zero, while child evidence without a lifetime retains legacy scope. Adoption locates the main turn with its key convention and separately validates the original group (`TestChildEarlyResultAfterBoundary`).

2026-10-09 (verifier finding 1): Retain first ordinary result/denial evidence independently of main-turn calls, keyed by the original source/lifetime-or-legacy-scope, turn and call. Pending child adoption uses that evidence rather than a replaceable main-turn slot, comparing it only with the child lane's own ending. This preserves early outcomes across explicit main openings and prevents reused producer lifetimes from losing their own report or importing a predecessor terminal. `TestChildEarlyReportInPreviousMainTurn` and `TestChildNewLifetimeEarlyReport` cover replay partitions, first-terminal precedence and both legacy and recorded Claude sources; the former also covers copied pending payloads. No concurrency, exported contract or security boundary changes; existing validation precedes retention.
