# Bounded thread query replies

## Files read
- `internal/protocol/thread.go` → `ThreadItem`: complete, inert row vocabulary.
- `internal/protocol/thread_encoding.go` → `EncodeThreadUpdate`, `ThreadContinuation`, `threadStampBudget`, `threadFragmentLength`, `threadValidUTF8`: shared serialization and byte budgets.
- `internal/protocol/envelope.go` → `Envelope`, `inboundAppTypeSet`: correlation and variable routing/session metadata.
- `internal/protocol/handshake.go` → `ErrorPayload`: existing terminal failure vocabulary.
- `internal/protocol/thread_encoding_test.go` → `testThreadLogical`, `testThreadMaxEnvelope`: assembly and maximal stamp helpers.
- `internal/protocol/thread_assembly_test.go` → `testThreadReceiver.accept`: atomic assembly validation pattern.
- `internal/protocol/compat_test.go` → `v2OnlyTypes`, `TestTypeConstants_V1V2Partition`: vocabulary classification.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes`: declared requests remain outside handler dispatch.
- `docs/knowledge/features/protocol-package.md` and `development-verification.md` § Protocol boundaries: marshal decoded DTOs, assert required keys and distinct values.
- `docs/knowledge/features/thread-package-background-store.md` § Bounded queries: ordered windows, authoritative coverage and unbounded nonzero catch-up.
- `docs/knowledge/decisions/042-daemon-built-thread.md` § Update messages: query completion contract.
- `CODING-STYLE.md`: pure protocol logic, errors as values and focused race tests.

## Context
Declare the shared query contract ahead of handlers #3234/#3235 and wiring #3236.
An interrupted reply must never certify coverage. No additional decision record is needed.
One deliverable: correlated, bounded query replies. Estimated written work 700 lines,
four exported DTOs, zero migrated consumers, four criteria and fewer than ten reject branches.
The finished plan retains these limits. Remote feature branches share none of the planned files.

## Design
Add `ThreadCatchUpPayload`, `ThreadPagePayload`, `ThreadItemsPayload` and
`ThreadResetPayload` in `thread_query.go`, with four discriminators in `codes.go`.
Signed optional limits normalize through `NormalizeThreadQueryLimit(int) (int, error)`:
zero means 50, positive clamps to 256, negative rejects even for nonzero catch-up.
Nonzero catch-up uses the complete supplied result regardless of the normalized limit.
Window limits count ordered rows; ties, active rows and parents may exceed them.

`ThreadItemsPayload` uses optional pointers for conditional coverage fields. A
nonzero catch-up marker has only `from_version`; a window/page marker has all of
`lower_order`, `upper_order`, numeric `continuation`, `older_exists`, including zero/false.
Only final markers carry coverage; bounds are supplied backend facts, never inferred from items.
Marshal nil items as `[]`. `ThreadResetPayload` has identity/version only.

`EncodeThreadReply(Envelope, any) ([][]byte, error)` accepts a successful complete
`ThreadItemsPayload` with `Final: true` and all items, or reset/existing error DTOs.
Require request correlation. Split items into ordinary batches with `final: false`,
then append exactly one separate empty final marker. For one oversized item use
`ThreadUpdatePart` and `ThreadContinuation` under `thread_items`, assembling the
entire single-item batch. Object continuation is assembly; numeric continuation is coverage.
Extract shared payload encoding from `EncodeThreadUpdate` while preserving its API/wire shape.
Supply variable envelope conversation/session metadata before encoding; reserve relay stamps.
Generalize UTF-8 traversal for item slices and optional pointers, retaining raw JSON checks.
No `internal/thread` import or handler changes. Classification changes are additive tests.

## Concurrency model
Pure synchronous encoding, detached inputs, no goroutines or mutable global state.
Callers must not mutate inputs during encoding and must preserve supplied routing/correlation.

## State transitions and identity reuse
| Event | Race-enabled test |
| --- | --- |
| Multiple batches, empty success, oversized assembly then separate marker | `TestThreadQueryReplies` |
| Missing, reordered or mismatched assembly parts, correlation change, interrupted assembly then marker | `TestThreadQueryAssemblyRejects` |
| Reset/error terminates without advancing completed checkpoint | `TestThreadQueryTerminalFailure` |
| Repeat deterministic detached encoding | `TestThreadQueryReplies` |
No production lifecycle state is introduced; the receiver is test-only.

## Error handling
Invalid JSON/UTF-8, missing correlation, malformed completion metadata or insufficient
budget returns a content-free sentinel and nil output, including failure after earlier items.
Final markers, resets and errors must fit without fragmentation. No content is logged.

## Testing strategy
Write focused failing tests first. Round-trip decoded DTOs through envelopes, checking
distinct fields and required zero/false/empty keys. Reconstruct ordinary/oversized full items,
unknown kind/status/content, and byte-identical serialized assembly. Measure escaped Unicode
with maximal stamps and variable routing/session metadata. Retain live encoder regressions.
Run `go test -race ./internal/protocol/...` and `./cmd/pyry/...`, `go vet ./...`,
and `go build -o /tmp/builder-3233/pyry ./cmd/pyry`. Full-module tests belong to verifier.

## Open questions
None. Production client assembly/resource limits and transport ordering are downstream.

## Documentation handoff
Pending documentation stage: `docs/protocol-mobile.md` message table and a “Thread query
messages” subsection beside thread update sections: exact request/reply fields, defaults/bounds,
correlation, terminal outcomes, conditional ranges, oversized assembly. State: “Thread query
messages are declared; relay handlers and production query wiring remain pending in #3234–#3236.”
Distinguish numeric page continuation, fragment completion and request completion.
Pending: `docs/knowledge/features/protocol-package.md`: link query vocabulary and explain
that only the separate successful final marker certifies query coverage.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: `EncodeThreadReply` validates encoding/correlation, not authorization; DTO content stays inert. Handler admission is OUT OF SCOPE in #3234–#3236.
- Tokens/errors/logs: no credentials or logging; errors contain no supplied data.
- File operations/subprocesses/cryptography: no I/O, commands, keys or nonces introduced. SHA-256 identifies serialized assemblies, not authentication.
- Network/I/O: shared envelope budget includes escaping, maximal relay stamps and supplied variable metadata; terminal payloads reject oversize. Receiver memory limits and transport ordering are OUT OF SCOPE for downstream clients.
- Concurrency: no goroutines; encoding errors expose no usable prefix. Test receiver rejects interrupted assembly and correlation changes before checkpoint advancement.
- Threat model: unknown content never executes; query vocabulary grants no capability or access. Relay/auth boundaries remain in #3234–#3236.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-12
