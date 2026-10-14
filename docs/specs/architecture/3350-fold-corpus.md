# Long-history fold corpus and baseline digests

## Files read
- `internal/thread/fold.go` → `Feed`, `Items`, `Item`: complete creation-ordered output and consumed version.
- `internal/thread/main_test.go` → `testMain`: main text/turn fixture conventions.
- `internal/thread/agent_test.go` → `testDurable`: mapped/durable companion shapes.
- `internal/thread/child_test.go` → `TestChildNestedLinkRepair`: late parent enrichment.
- `internal/thread/sends_test.go` → `TestSendDelivery`: acceptance identity and delivery suppression.
- `cmd/pyry/thread_agent_test.go` → `TestThreadAgentRecordedReplay`: recorded parser/emitter route and provenance assertions.
- `cmd/pyry/thread_shell_test.go` → `TestThreadShellRecordedReplay`: synthetic shell context and recorded lifecycle route.
- `docs/knowledge/features/thread-package.md` and its main-thread/agents children: retained evidence must keep independent semantic/provenance checks; unfinished children are needed to prove reopening.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md`: emitter ownership.
- `docs/knowledge/features/development-verification.md`: missing fixtures must fail; snapshot equality alone is insufficient.
- ADR 042 § Item model: immutable kinds and complete item contract.

## Context
Retain an offline behavioral gate before further fold optimization. Baseline production
source is `a920b90cc6bfd2f1fb8461e9f85ce6f19934afa6`, containing #3345,
#3324 and #3342. No production changes or decision record are needed.
No other fetched feature branch currently touches the selected replay files or thread package.

## Design
Same-package thread test helpers generate exact count prefixes from fixed-size scenarios
with distinct turn/call/task identities and bounded payloads. Small semantic checkpoints
cover the first scenario; sparse checkpoints reach 30,000. Raw shadow history is read
unchanged. Existing cmd replay tests expose their history derivation as reusable helpers;
their independent semantic/provenance assertions remain effective.

Compact manifests pin source commit, generator identity/count, raw input SHA-256,
normalized history SHA-256 and explicit checkpoint versions. Every checkpoint includes
Version, item count, LastShownVersion, complete canonical-JSON item SHA-256 and a
separate exact-Content SHA-256 framed by big-endian uint64 item ID and byte length.
Canonical JSON decodes with UseNumber and encodes sorted object keys without dropping
fields. Recorded fields are never normalized. Only emitter-created turn/lifetime IDs
are mapped by first occurrence and emitter timestamps are replaced deterministically;
entry timestamps are fixed. Repeated derivation must produce identical histories.
Golden writing requires an explicit flag and named generation command; normal readers
fail on absent/incomplete manifests and changed inputs or checkpoint output.

## Concurrency model
Tests feed owner-local folds synchronously. Existing parser/emitter history writes use
their existing lifecycle. No new goroutines or shared mutable fold state.

## State transitions and identity reuse
| Event | Race-enabled coverage |
| --- | --- |
| Scenario repetition with new turns/calls/tasks | `TestFoldCorpus` |
| Nested child appearance, closure, late-link reopening and parent enrichment | `TestFoldCorpus` |
| Shell links and mapped/durable companions | `TestFoldCorpus` and recorded replay tests |
| Queued send delivery and delivery-row suppression | `TestFoldCorpus` |
| Fresh whole-prefix, deterministic chunks and single-entry replay | `TestFoldBaselines`, recorded replay tests |

## Error handling
Missing/invalid inputs, incomplete checkpoints and digest disagreement fail tests.
Mutation controls distinguish canonical content from exact Content bytes and reject
changed expectations. Ordinary tests do not write files or capture credentials.

## Testing strategy
Write failing corpus/golden tests before helper implementation. Test generator prefix
stability and bounded scenario payloads, semantic lifecycle checkpoints, lossless numeric
canonicalization and corruption detection. Run focused race tests for thread and cmd,
repository vet/build and leave the full make-check gate to the verifier per builder policy.
Benchmark full Feed once at 30,000 entries with generation/snapshots/digests outside the
timer; report elapsed time, allocated bytes, item count, machine, Go and source in PR.

## Open questions
None. Expected work approximately 760 added/changed lines including plan and manifests;
zero exported types/interfaces, two existing replay consumers, four acceptance criteria,
and no production state-machine reject branches.
