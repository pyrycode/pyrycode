# Background shell lifecycle on ordinary calls

## Files read
- `internal/thread/agent.go` → `agentWork`, `agentAddReport`, `applyAgent`: scoped joins, reference validation and first genuine final.
- `internal/thread/main.go` → `foldWork`, `applyTerminal`: ordinary creation and provisional turn closure.
- `internal/thread/child.go` → `childGroup`, `resolveChildren`: recorded group and intrinsic child state.
- `internal/thread/fold.go` → `Feed`: serialized updates and detached snapshots.
- `cmd/pyry/thread_agent_test.go` → `TestThreadAgentRecordedReplay`; `session_background_task_join_test.go` → `TestBackgroundTaskJoin_CaptureOrders`: offline parser/emitter/store seams.
- `docs/knowledge/features/thread-package.md` → Agents and background work: companion order, reference validation and retained child state.
- ADR 042 → Item model and Sessions, agents, messages, read marks; history runtime lifecycle → Unfinished agent/task session endings and Recover agent/task work and original identities: independent lifetimes and original evidence.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification.md` → inert content and capture provenance.

## Context
Complete one deliverable: enrich existing Claude/legacy Bash calls with linked task lifecycle. No producer or daemon changes. Existing agent lifecycle machinery supplies the contract; no new decision record. Feature branches #2873 and #2882 do not overlap the proposed files.

## Design
Register ordinary Bash/local_bash creations with their existing scoped call evidence, retaining the original item and launch payload. Apply shared lifecycle computation only with a usable task link; unlinked calls retain ordinary semantics. Linked results are launch evidence, denials remain finals, and main-turn closure is provisional and repairable. Promote earlier ordinary call/result evidence when the first validated durable fact establishes a lifetime, only from the same recorded source and uninterrupted legacy scope; never merge established successor lifetimes. Preserve original observation references and reject supplied invalid references without fallback. Reuse task/report validation and durable/mapped companions. No new exported API. Estimated written work: 700 lines, zero exports/migrations, four acceptance criteria, fewer than ten new rejection branches; rechecked before plan commit.

## Concurrency model
Fold remains caller-serialized, with no goroutines or external I/O.

## State transitions and identity reuse
| Event | Race-enabled proof |
| --- | --- |
| Creation/result/outcome/link in any order; early final floor | `TestShellLifecycleOrders` |
| Main end before link, late launch result, stopping/progress/omission | `TestShellLateLinkAndEnrichment` |
| First lifetime after call/result; reused IDs across lifetimes/sources/boundaries | `TestShellLifetimeScopes` |
| Saved session ending, reference rejection, conflicting finals and companions | `TestShellFinalsAndRecovery` |
| Detached content and child parenting | `TestShellIdentityAndValidation` |
| Recorded completion and omission before completion | `TestThreadShellRecordedReplay` |

## Error handling
Malformed/foreign facts and lost identities consume version only. An unusable link leaves a usable task pending. Saved endings require the existing source/lifetime/observation validation. No inferred roster disappearance or stop status.

## Testing strategy
Write failing offline behavior tests first, compare full replay with every split and incremental feeds, then run race tests for thread and cmd/pyry, `go vet ./...`, and build pyry outside the tree. Both committed captures feed verbatim frames through production parsing and raw history emission; omitted Bash context is explicitly synthetic. Missing captures fail. Full-module gate belongs to verifier.

## Open questions
None.

## Documentation handoff
Pending documentation stage: in `docs/knowledge/features/thread-package.md`, extend “Agents and background work” with “Background shells”: lifecycle fields on ordinary calls, late links and early reports, lifetime-scoped joins including calls predating task facts, task completion versus launch result, independent main-turn lifetimes and saved session-ending causes. Replace remaining shell/background exclusion or downstream-#3059 claims and qualify the ordinary-call turn-closure description for joined background work.

## Security review
**Verdict:** PASS
**Findings:**
- Trust boundaries: `Feed`, `mainDecode` and `agentWork` validate recorded JSON, conversation, identities and references before joining. SHOULD FIX: test that first-lifetime promotion cannot cross source or legacy boundary and that lost links reserve nothing.
- Tokens/secrets, cryptography: none created or interpreted; content remains copied inert JSON.
- Files/subprocesses/network and I/O: production fold has none; offline tests read fixed committed fixture paths and use isolated temporary history stores. Shell input is never executed.
- Errors/logs/telemetry: existing generic entry-order errors; no payload logging added.
- Concurrency: serialized fold contract unchanged; snapshots remain detached.
- Threat model: this ticket adds no relay/CLI endpoint or authentication behavior; wire transport remains outside the fold.
**Reviewer:** builder (self-review)
**Date:** 2026-10-09
