# #2268 — map claude's `model_refusal_no_fallback` line to a bounded daemon event

## Files read

- `internal/streamsup/parser.go` → `emitSystemSubtype`, `emitModelRefusalFallback`, `systemModelRefusalFallbackLine` — the dispatch seam and nearest shipped decode/bound/emit pattern.
- `internal/streamsup/parser.go` → `maxTaskFieldID`, `maxDenialProse`, `truncateField`, `streamLine` — the existing token/prose caps, UTF-8-safe cutter, and top-level reachability constraint.
- `internal/streamsup/parser.go` → `emitPermissionDenied`, `systemPermissionDeniedLine` — the earlier content-free malformed-line and structurally omitted-key precedent.
- `internal/turnevent/event.go` → `ModelRefusalFallback`, `ToolCallDenied`, `ModelAnnounced`, `Event` — the sibling variant, bounded-field reports, descriptive-only trust posture, and marker contract.
- `internal/streamsup/parser_model_refusal_fallback_test.go` → `oneRefusalFallback`, `refusalFallbackLineFixture`, `TestParser_RefusalFallbackBoundsEveryClaudeString` — the documentation-derived fixture and parser proof shape to reduce for four fields.
- `cmd/pyry/stream_turn_busy_test.go` → `TestTurnMarkFor_TotalOverEveryVariant` — the marker-derived exhaustive table that must classify the new event as `turnMarkNone`.
- `docs/knowledge/features/streamsup-package.md` § “Turn I/O” — package ownership and the parser's synchronous sink boundary.
- `docs/knowledge/features/streamsup-package-system-maps-per-subtype-since-2026-08-07.md` — the rule that `emitSystemSubtype` is the mapped-set enumeration and current family lessons.
- `docs/knowledge/features/development-verification.md` → “Establish the change surface”, “Prove that tests distinguish the change”, “Captures and live evidence” — defaulting consumers, subtype-consumption observability, and fixture provenance checks.
- `$AGENTS_REPO_PATH/builder/security-review.md` — the label-gated adversarial checklist applied below.

## Context

Claude can end a refused turn without retrying and announce the outcome on a `system/model_refusal_no_fallback` line. `emitSystemSubtype` currently has no arm for that subtype, so the known `system` lane deliberately consumes it without an event. This change gives daemon clients a distinct explanation rather than making the no-fallback case indistinguishable from silence.

The field set is documentation-derived from the Claude Code headless and Agent SDK documentation and `@anthropic-ai/claude-agent-sdk@0.3.263`; it is not capture-derived. No refusal fixture can be safely provoked, so the tests use explicitly labelled, hand-built hermetic lines and assert parser behaviour for that proposed shape, not observed bytes.

No ADR is warranted. This adds one member to the existing system-subtype/event family and deliberately reuses #2267's mechanism. Mobile publication remains #2266.

The refiner estimated about 825 written lines, slightly above the 800-line ceiling. The actual strict-subset sketch is approximately 650–750 lines across two production files, three test files including the totality row, and this plan. Even if it reaches the refiner estimate, splitting the event declaration from its only producer would create a one-consumer, non-observable slice, and GitHub confirms the depth cap `#2196 → #2264 → #2268`. The existing `needs-human:sizing` marker is therefore correct and implementation proceeds in place.

## Design

### Event contract

Add `turnevent.ModelRefusalNoFallback`, a value variant distinct from `ModelRefusalFallback`, with these fields:

| Daemon field | Claude key | Overflow answer | Existing cap |
|---|---|---|---|
| `OriginalModel` | `original_model` | drop | `maxTaskFieldID` |
| `RefusalCategory` | `api_refusal_category` | drop | `maxTaskFieldID` |
| `RefusalExplanation` | `api_refusal_explanation` | cut | `maxDenialProse` |
| `Banner` | `content` | cut | `maxDenialProse` |

`TruncatedFields` reports cut prose as `refusal_explanation` and `banner`; `DroppedFields` reports dropped tokens as `original_model` and `refusal_category`. Reports use daemon field names, stay nil when no bound fired, and preserve declaration order.

Every field is optional. An empty field named in neither report means claude omitted or sent it empty; a field named in a report means the daemon bounded it. The type documentation will say the event is claude's descriptive assertion, does not replace `ModelAnnounced`, opens or closes no turn, and must not drive daemon behaviour. Its prose is inert, unsanitized text that may echo refused user content.

### Parser arm

Add `model_refusal_no_fallback` to `emitSystemSubtype`, routed to `emitModelRefusalNoFallback(line []byte) bool`. Its separate `systemModelRefusalNoFallbackLine` declares exactly the four string keys carried into the event. It deliberately omits `request_id` and `refused_user_message_uuid`; undeclared fields cannot accidentally leak into a daemon surface.

The arm always consumes the recognized subtype. A successful decode emits exactly one event, even when all four fields are absent. A wrong JSON type on any declared string makes `encoding/json` reject the whole target; the arm then emits no event and writes one Debug record containing only the fixed subtype keyword.

The documented line has no `message`, so the ordinary `streamLine` route should reach the subtype switch. No speculative decode-failure recovery entry point is added. A future capture demonstrating a scalar `message` would be evidence for separate work.

Bounding remains local to the arm, mirroring `emitModelRefusalFallback`: token values over `maxTaskFieldID` become empty and append to `DroppedFields`; prose passes through `truncateField` at `maxDenialProse` and appends to `TruncatedFields`. No new limit or shared abstraction is introduced.

### Consumers

Register the value receiver in the `Event` marker block. Add a `turnMarkNone` row to `TestTurnMarkFor_TotalOverEveryVariant`; `turnMarkFor` itself stays unchanged because the event is neither a turn opener nor closer. `turnbridge.MapEvent` and the interactive emitter intentionally keep their unknown-variant defaults until #2266 publishes the event.

## Concurrency model

None. The parser arm reads and writes no retained parser state, creates no goroutine, channel, lock, or queue, and calls the existing synchronous sink once on success.

## Error handling

Malformed declared fields fail closed as one decode unit: consume the known subtype, log only `subtype=model_refusal_no_fallback`, and emit no event. Unknown fields are ignored by `encoding/json`; the two excluded identifiers therefore never enter the decoded struct. Missing fields are not errors and do not gate emission.

## Testing strategy

- Add a new parser test file whose header and fixture helper explicitly identify every line as documentation-derived and hand-built.
- Prove a complete line emits exactly one `ModelRefusalNoFallback` with four distinct sentinel values, while an adjacent `ModelRefusalFallback` remains its own concrete variant.
- Prove `request_id` and `refused_user_message_uuid` do not appear in any event field or report.
- Table-test omission of each declared key and all four keys together; each line still emits, the absent field is empty, and both report slices remain nil.
- Table-test wrong JSON types across scalar and composite shapes; assert zero events and exactly one subtype-only Debug record with a closed attribute set.
- Table-test each field over its cap, all fields over together, and all fields exactly at cap; assert drop versus cut, daemon report names, report order, UTF-8-safe prose cutting, and no report at the boundary.
- Add the new event to the marker-derived `TestTurnMarkFor_TotalOverEveryVariant` table as `turnMarkNone`.
- RED: add the new tests and run the focused streamsup test before production implementation; the missing type/arm must fail for the intended reason. GREEN: run the focused tests, then the required touched-scope race tests, repository vet, and `cmd/pyry` build.

No live proof is possible or requested. No capture, fixture promotion, or real-Claude run belongs to this ticket.

## Open questions

1. Could a future real line contain a scalar `message` and fail before subtype dispatch? Yes; the documentation-derived shape cannot answer it. Resolution: record the reachability prediction on the arm and defer recovery until observed evidence exists.
2. Should the no-fallback arm reuse `systemModelRefusalFallbackLine`? No. Separate targets make the accepted keys structural and keep `scope`/`fallback_model` out of the no-fallback contract, following the task-line family separation rule.
3. Should the bounding closures be extracted now? No. A local four-field application is simpler and avoids modifying shipped arms for no behavioural gain.

## Documentation handoff

Pending for the documentation stage:

- Update `docs/knowledge/features/streamsup-package-system-maps-per-subtype-since-2026-08-07.md` to add the `model_refusal_no_fallback` arm, its documentation-derived provenance, all-optional decode contract, excluded identifiers, and malformed-line behaviour.
- Update `docs/knowledge/features/turnevent-package-outbound-event-variants.md` in the refusal/model-event section to document `ModelRefusalNoFallback`, its four fields, drop/cut reports, descriptive-only trust posture, and distinction from `ModelRefusalFallback`.
- Do not update `docs/protocol-mobile.md` here; #2266 owns mobile publication and its protocol reference.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** Finding addressed by design — untrusted claude stdout becomes a typed daemon event only in `emitModelRefusalNoFallback`, after top-level `streamLine` dispatch. The event is claude's assertion, never a daemon finding, and nothing in the daemon acts on its model or category. The type must state that `ModelAnnounced` remains authoritative for the running model.
- **[Tokens, secrets, credentials]** Finding addressed by design — no credential is created or carried, but refusal prose can echo user input. Both prose fields are capped at construction and no decoded field may be logged. `request_id` and `refused_user_message_uuid` stay absent from the decode target.
- **[File operations]** No findings — the change opens, creates, stats, or names no file and uses no capture fixture.
- **[Subprocess / external command execution]** Finding addressed by design — the prose may contain code or commands claude refused. Event documentation must require inert text rendering and forbid executing or re-shelling it.
- **[Cryptographic primitives]** Not applicable — no randomness, secret comparison, encryption, hashing, or identifier generation is introduced.
- **[Network & I/O]** No findings in this slice — `Parser` already bounds an input line, every carried string is bounded again before the synchronous sink, and no network publication is added. #2266 owns wire fan-out and envelope mapping.
- **[Error messages, logs, telemetry]** Finding addressed by design — the undecodable path logs only the fixed subtype keyword. Every decoded value, especially explanation and banner, is MUST-NOT-log. No telemetry is added.
- **[Concurrency]** No findings — the arm retains no state, takes no locks, spawns no goroutines, and emits synchronously once.
- **[Threat model alignment]** OUT OF SCOPE — the mobile protocol threat model does not bind a parser-only event. #2266 owns client publication and render-boundary sanitization; this event documents that its strings are bounded and UTF-8-valid but not sanitized.

**Reviewer:** builder (self-review per the security-review checklist)  
**Date:** 2026-09-10
