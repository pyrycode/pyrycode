# #2263 — map `system/api_retry` to the `ApiRetry` edge pair

## Files read

- `internal/streamsup/parser.go` → `Parser`, `consumeLine`, `emitSystemSubtype`, and `emitCompactingStatus`: the single-writer parser state, subtype dispatch, boundary ordering, and nearest edge-pair implementation.
- `internal/streamsup/parser_compacting_test.go` → `TestParser_CompactingEdges` and `compactingRun`: the nearest unit-test pattern for a stateful status edge ordered before content events.
- `internal/streamsup/compaction_capture_test.go` → `compactionCapture`, `TestRealClaudeCompactionCaptureShapesArePinned`, and `TestCompactionFixtureReplayReachesBothEdges`: the provenance checks and full-turn replay pattern the new committed-fixture reader follows.
- `internal/e2e/realclaude/testdata/api_retry_v2.1.259.json` → `frames`: the committed Claude 2.1.259 evidence containing ten retry updates followed by an assistant line and terminal result.
- `internal/turnevent/event.go` → `ApiRetry`: the existing event contract, including `{0,0}` for counters that do not parse.
- `internal/turnbridge/outbound.go` → the `turnevent.ApiRetry` arm: confirms the downstream wire mapping already exists and is outside this ticket.
- `docs/knowledge/features/streamsup-package.md` → `Turn I/O — envelope write + stdout parser`: the parser ownership and test conventions.
- `docs/knowledge/features/streamsup-package-system-maps-per-subtype-since-2026-08-07.md` → system subtype mapping: the current measured-subtype policy and silent treatment of unknown system subtypes.
- `docs/knowledge/features/development-verification.md` → `Captures and live evidence`: committed readers must fail on missing or unpinned evidence and must derive claims from payload bytes.
- `docs/knowledge/features/e2e-realclaude-api-retry-capture-test-go.md` → `api_retry_capture_test.go`: provenance and observed termination shape for the source capture.
- `CODING-STYLE.md` → testing, concurrency, and citation conventions: table-driven stdlib tests, parser single-writer state, and symbol-based references.

## Context

Claude 2.1.259 emits `system/api_retry` once for each live retry attempt. The existing parser silently drops that subtype even though `turnevent.ApiRetry` and its bridge mapping already exist. Mobile clients therefore receive no indication that a turn is still retrying.

This ticket adds only the missing streamsup producer and its hermetic proof. Retry delay, error category/status, protocol changes, bridge changes, and end-to-end phone delivery remain outside scope.

## Design

### Parser state and event flow

`Parser` gains one boolean latch recording whether at least one retry update has opened the status. The parser remains the sole owner of the latch under its existing single-writer invariant.

```text
system/api_retry
    -> emitSystemSubtype
    -> decode attempt and max_retries
    -> emit ApiRetry{Active:true}

next assistant | user | result
    -> clear open retry latch once
    -> emit ApiRetry{Active:false} first
    -> continue the line's existing mapping
```

Every `system/api_retry` line emits an active update, including subsequent lines while the latch is already open. The latch suppresses only duplicate falling edges; it never suppresses attempt updates.

`consumeLine` clears retry state at the start of each successfully decoded `assistant`, `user`, and `result` arm. A small parser method owns the conditional clear so all three arms share the same state transition and ordering. The `result` arm then performs its existing compaction/reset work and emits `TurnEnd` unchanged.

If a child exits while retry state is open, the long-lived parser retains the latch across respawn. The next successfully decoded assistant, user, or result line clears it before publishing that line's events. No child-exit reset is introduced because the client also retains the active state and requires an observable falling edge.

### Retry line decoding

`streamLine` remains the segmentation shape. A subtype-specific decode target reads only `attempt` and `max_retries` as Go integers. A valid line publishes those values directly. Missing keys decode to zero. If either counter has a non-integer JSON shape, subtype decoding fails as a unit and the event publishes `{Current: 0, Total: 0}` while still consuming the known subtype. No other retry fields are retained, logged, or published.

The subtype handler always returns consumed after dispatch reaches `api_retry`, so neither valid nor malformed counters reach `turnevent.Unrecognized` or the unknown-system drop.

### Committed capture replay

`internal/streamsup/api_retry_capture_test.go` adds a reader dedicated to the `api_retry_v2.1.259.json` record. It does not generalize the compaction reader because the record schemas and flagged-frame semantics differ.

The reader:

- builds the fixture path from one pinned Claude version constant;
- fails when the fixture is missing or malformed;
- requires `is_capture: true` and the matching leading Claude version token;
- requires at least one frame flagged `api_retry`;
- requires replayed payloads to be JSON strings;
- re-decodes each flagged frame's payload and verifies its own `type` and `subtype`, rather than trusting recorded labels; and
- feeds all captured frames through one `Parser` in original order so cross-line state and the assistant clear are exercised.

The replay asserts ten active `ApiRetry` values with currents 1 through 10 and total 10, followed by exactly one inactive value before the assistant event, with no `Unrecognized` event anywhere in the captured turn.

## Concurrency model

No goroutine or synchronization changes. `Parser.Write`, subtype dispatch, latch mutation, and sink calls remain serialized by the existing child-stdout forwarding goroutine. The same parser instance can outlive a child, so retry state deliberately survives a resultless child exit until a later content line emits the clear.

## Error handling

- Missing, unreadable, unpinned, or contradictory capture evidence fails the hermetic test; there is no pre-capture skip state.
- A retry line with absent counters emits zero values through normal JSON zero-value decoding.
- A retry line with a non-integer counter is consumed and emits an active zero-valued counter instead of becoming unrecognized.
- A line that fails the outer `streamLine` decode keeps the parser's existing undecodable-line behavior; this ticket does not broaden recovery for structurally invalid envelopes.
- Retry metadata beyond the two integer counters is ignored by construction and never included in logs or errors.

## Testing strategy

- Add a table-driven parser test covering every closer (`assistant`, `user`, `result`), exact ordering before each closer's existing mapped event, duplicate-clear suppression, and no clear when retry was never opened.
- Add retry-counter cases for successive active updates, absent fields, and non-integer fields, asserting the subtype is consumed and yields the existing zero-counter contract.
- Replay the entire committed turn and assert the exact ten active updates, the single assistant-triggered clear, and zero unrecognized events.
- Prove RED by running the new streamsup tests before production changes.
- Run `go test -race ./internal/streamsup/...`, `go vet ./...`, and `go build ./cmd/pyry` after implementation. The dispatcher runs the full-module gate.

## Open questions

None. The committed capture resolves the line shape, attempt cardinality, and first closing line; the existing `ApiRetry` type resolves the malformed-counter contract.

## Documentation handoff

Pending for the documentation stage: update `docs/knowledge/features/streamsup-package.md`, in the turn I/O/parser-state coverage, to record the `system/api_retry` producer, its per-attempt active updates, its assistant/user/result falling edge, and the residual that survives a resultless child exit until the next such line.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No MUST FIX — untrusted subprocess stdout crosses the existing bounded `Parser.Write` boundary. `emitAPIRetry` decodes only two integers into a subtype-specific target, and `streamLine` remains the segmentation authority.
- [Tokens, secrets, credentials] No findings — the event carries only a boolean and two integers. Session ids, UUIDs, upstream URLs, errors, and fixture payloads are neither retained nor logged by the new mapping.
- [File operations] No MUST FIX — production adds no file operation. The test opens one repository-fixed fixture path composed from a constant version, with no caller-controlled path, write, permission, symlink, or atomicity decision.
- [Subprocess execution] No findings — the change parses existing child output and neither starts a process nor changes arguments, environment inheritance, or signals.
- [Cryptographic primitives] Not applicable — no randomness, keys, nonces, hashes, encryption, or secret comparison enters the design.
- [Network & I/O] No MUST FIX — `Parser.Write` retains its 4 MiB partial-line bound. Each accepted retry line produces one fixed-size event; the observed stream has ten updates and the acceptance contract deliberately requires every attempt. Existing sink backpressure remains the resource bound, and no unobserved rate limiter is added.
- [Error messages, logs, telemetry] No findings — malformed counters degrade to numeric zero without logging payload content. The new code adds no telemetry and exposes no Claude-authored string.
- [Concurrency] No findings — no new goroutine, lock, or shared reader is introduced. Retry state is mutated and read on the existing single writer, including across child respawn after the prior stdout forwarder exits.
- [Threat model alignment] No MUST FIX — this is an internal display event producer, not a new control input. Retry state cannot alter daemon behavior, authorization, routing, process lifecycle, or protocol framing; downstream delivery continues through the existing `ApiRetry` bridge arm.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-10
