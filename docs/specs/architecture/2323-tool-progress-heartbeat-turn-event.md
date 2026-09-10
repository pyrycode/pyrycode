# 2323 — Decode tool-progress heartbeats into turn events

## Files read

- `internal/streamsup/parser.go` → `consumeToolProgress`, `toolProgressMarkers`, `parentToolUseID`, `systemTaskProgressUsage`, `emitBackgroundTaskProgress` — owns the marker match, second-decode conventions, identifier cap, and signed counter precedent.
- `internal/streamsup/tool_progress_test.go` → `TestParser_ToolProgressMarkerTolerance`, `TestParser_ToolProgressCapturedFramesAreSilent` — pins lane selection and replays the committed heartbeat evidence.
- `internal/streamsup/tool_progress_capture_test.go` → `capturedToolProgressLines` — fail-closed reader for the committed Claude 2.1.259 capture.
- `internal/e2e/realclaude/tool_progress_capture_test.go` → `tpcapCollect`, `TestRealClaude_ToolProgressCapture` — records the parser verdict that must describe the post-change heartbeat emission contract.
- `internal/turnevent/event.go` → `Event`, `ToolStart`, `ToolUpdate`, `BackgroundTaskProgress`, `ToolCallDenied` — defines the daemon-owned event vocabulary and the existing `ToolCallID` join-key rules.
- `cmd/pyry/interactive_turn_v2.go` → `eventKind`, `interactiveTurnEmitterV2.Handle` — classifies known variants for content-free logs while leaving wire handling to the follow-on ticket.
- `cmd/pyry/interactive_turn_v2_test.go` → the event-kind no-cursor tests — establishes the reachable proof that a known internal-only variant is named without logging its fields.
- `cmd/pyry/stream_turn_busy.go` → `turnMarkFor` — decides lifecycle and fan-in reservation semantics from event type alone.
- `cmd/pyry/stream_turn_busy_test.go` → `TestTurnMarkFor_TotalOverEveryVariant`, `turnEventVariants` — marker-derived exhaustive table for the sealed event set.
- `docs/knowledge/features/streamsup-package-tool-progress-consumed-by-matching.md` → “Wire facts pinned by a live capture” and “parent_tool_use_id means something different here” — records the observed cadence, markers, and key semantics.
- `docs/knowledge/features/turnevent-package-outbound-event-variants.md` → outbound variant rules — requires producer-side bounds for Claude-authored identifiers and content-free classification.
- `docs/knowledge/features/development-verification.md` → capture evidence guidance — requires explicit captured-field assertions and compilation of the tagged suite even when the live probe skips.

## Context

`consumeToolProgress` currently recognizes three `tool_progress` varieties and consumes all three without an event. The committed Claude 2.1.259 capture proves that the heartbeat variety carries a foreground Bash call's real id in `parent_tool_use_id`, a synthetic per-tick id in `tool_use_id`, and elapsed readings from 30 through 360 seconds. This ticket publishes that observed heartbeat as a daemon-internal event; ticket #2324 owns its wire shape.

The change does not broaden the matcher. A matched heartbeat remains consumed even when its fields cannot produce a joinable event, the other two known markers remain silently consumed, and the unobserved marker-less variety still reaches `Unrecognized`.

## Design

Add `turnevent.ToolProgress` with `ToolCallID string` and `ElapsedSeconds int`. `ToolCallID` uses the house name shared by the tool lifecycle variants, while the type remains distinct from terminal `ToolUpdate`. The event deliberately excludes `tool_use_id`, `tool_name`, `session_id`, and `uuid`: the existing tool row already owns the name, and the other captured identifiers do not identify the row being updated.

`consumeToolProgress` keeps `toolProgressMarkers` as the only marker discriminator. After a strict top-level `heartbeat: true` match, it performs a second decode into a heartbeat-only target containing `parent_tool_use_id` as `json.RawMessage` and `elapsed_time_seconds` as signed `int`.

The raw parent id is passed through `parentToolUseID`. Reuse is intentional but narrow: the helper is the shared JSON-string and `maxTaskFieldID` validator for the same wire key, not a claim that the key has the same relationship meaning on every line type. Its documentation will distinguish the two meanings. On assistant/user lines the result populates `ParentToolCallID`; on `tool_progress` it populates the new event's `ToolCallID`. This preserves the one implementation of the drop-not-truncate join-key rule and keeps `TestParser_ParentToolUseID_IsNotFedByToolProgress` true because no heartbeat value enters `ToolStart.ParentToolCallID` or `ToolUpdate.ParentToolCallID`.

An absent, empty, non-string, or over-cap parent id yields no event. A malformed or out-of-range elapsed value makes the heartbeat-only second decode fail and likewise emits nothing. Both cases still return handled from `consumeToolProgress`. An absent elapsed key decodes to `0`; zero and negative signed readings are emitted verbatim rather than turned into validation policy. This takes `systemTaskProgressUsage`'s decode-side contract: signed values keep anomalous upstream readings observable, while only the join key is required to make the event useful.

The parser is a pure per-line mapping: it adds no timers, cadence checks, maps, or cross-line state. Each valid heartbeat emits exactly one event. The observed 30-second cadence is evidence for the capture assertion, not a daemon rate limiter.

`eventKind` gains the content-free name `tool_progress`. `interactiveTurnEmitterV2.Handle` deliberately gains no mapping arm: until #2324 adds the wire contract, the internal event follows `ConversationReset`'s producer-first precedent and reaches the existing unknown-event drop on that lane. The classifier prevents that intentional drop from being mislabeled `unknown` in logs and returns neither id nor elapsed value.

`turnMarkFor` remains unchanged and its exhaustive test gains an explicit `ToolProgress` row with `turnMarkNone`. A heartbeat is evidence about a tool row that `ToolStart` already opened, not a turn boundary. Treating repeated heartbeats as openers would make them reserved under fan-in saturation without improving lifecycle correctness; dropping one only makes a counter skip a reading, while `ToolStart`, `ToolUpdate`, and `TurnEnd` retain their existing protected semantics.

The live capture probe keeps its existing frame and marker census. Its parser-verdict check changes from “every matched frame emits zero events” to the post-change contract: a heartbeat emits one event and the other marked varieties emit none. The committed fixture keeps the live probe skipped by design; only tagged-package compilation is required here.

## Concurrency model

No goroutine, timer, shared field, lock, or shutdown path is added. Parsing, event construction, kind classification, and turn-mark classification remain synchronous on their existing callers. Each heartbeat is independent of prior and later frames.

## Error handling

- Marker decoding remains type-strict and cannot divert a matched heartbeat to the unrecognized lane.
- A heartbeat-only decode failure is consumed and silent; no raw payload or derived field is logged.
- An unusable join key drops the whole event rather than emitting an unjoinable or truncated handle.
- Elapsed readings are not clamped, wrapped, inferred from cadence, or accumulated by the daemon.
- The marker-less shape remains visible as `Unrecognized`; this ticket does not speculate about an unobserved wire form.

## Testing strategy

- Rewrite the capture replay so all 12 committed heartbeat frames produce `ToolProgress` with the exact Bash parent id and elapsed sequence 30, 60, …, 360; independently decode `heartbeat` in the test so a misspelled production marker cannot pass vacuously, and reject any synthetic `-heartbeat-` id.
- Extend the marker table to prove heartbeat emission while `subagent_type` and `repl_call` remain silent and the marker-less line remains `Unrecognized`.
- Add a table for absent, empty, non-string, and over-cap `parent_tool_use_id`; every row must emit nothing and must remain consumed. Pin the exact-cap success edge and signed elapsed behavior beside it.
- Exercise the reachable no-cursor logging path to prove `eventKind` returns `tool_progress` and leaks neither the call id nor elapsed reading.
- Add the explicit `turnMarkNone` row to the exhaustive turn-event table.
- Retarget the live probe's parser verdict and compile `internal/e2e/realclaude` with `-tags e2e_realclaude` without running a live test.
- Run `go test -race ./internal/streamsup/... ./internal/turnevent/... ./cmd/pyry/...`, `go vet ./...`, and `go build ./cmd/pyry`.

## Open questions

None. The committed capture settles the join key and cadence; the lifecycle decision is `turnMarkNone`; marker-less progress and the wire mapping remain explicitly assigned to separate tickets.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — untrusted subprocess stdout crosses the existing bounded parser boundary at `Parser.Write`; `consumeToolProgress` performs one marker decode and one heartbeat-only decode, and `parentToolUseID` is the single validator before the Claude-authored id enters `turnevent.ToolProgress`.
- [Tokens, secrets, credentials] No findings — the design creates, stores, rotates, compares, or publishes no credential. The opaque tool-call id is a routing handle, is capped, and is never logged.
- [File operations] No findings — production code adds no file access. `capturedToolProgressLines` continues to read the fixed committed fixture path with no caller-controlled path or write.
- [Subprocess execution] No findings — the change only decodes output from the already-running child and adds no command, argument, environment, or signal behavior.
- [Cryptographic primitives] No findings — no randomness, key material, hashing, encryption, or secret comparison is introduced.
- [Network and I/O] No findings — no network surface or new read is added. The existing `Parser.Write` maximum line buffer bounds the input before the second decode; the published identifier has the tighter `maxTaskFieldID` bound and the integer has Go's fixed-width bound.
- [Errors, logs, telemetry] No findings — every invalid heartbeat path is silent and consumed, while `eventKind` returns only a package-owned variant literal. Neither raw payload, call id, elapsed value, nor other captured field enters a log or error.
- [Concurrency] No findings — the mapping is stateless and synchronous, adds no goroutine or shared state, and therefore adds no lock ordering, check-then-mutate, or shutdown behavior.
- [Threat model alignment] No findings — this daemon-internal event is non-actuating and has no relay or CLI mapping in this ticket. A forged matching id can only mislabel a future client's existing row; the daemon performs no action from it, and #2324 owns the wire-boundary review.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-10

## Revisions

None.
