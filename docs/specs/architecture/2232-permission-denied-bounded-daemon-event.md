# #2232 — keep claude's `permission_denied` line as a bounded daemon event

## Files read

- `internal/streamsup/parser.go` → `emitSystemSubtype` — the switch this ticket adds an arm to; its docblock states that the case arms are the ONE enumeration of the mapped set.
- `internal/streamsup/parser.go` → `emitBackgroundTaskStarted`, `systemTaskStartedLine` — the closest analogue by shape: flat line, several claude strings through one local `bound` helper, `session_id`/`uuid` absent from the decode target, `tool_use_id` renamed to the daemon's `tool_call_id`.
- `internal/streamsup/parser.go` → `emitCompactionBoundary`, `emitModelAnnounced` — the two precedents that GATE on a field, versus the one above that does not. The choice between them is this plan's load-bearing decision.
- `internal/streamsup/parser.go` → `maxTaskFieldID`, `maxTaskDescription`, `maxCompactTrigger`, `maxTurnEndStopField`, `truncateField` — the bounding scheme, and the two overflow answers (cut-and-report vs drop) with the reasoning that selects between them.
- `internal/turnevent/event.go` → `BackgroundTaskStarted`, `CompactionBoundary`, `Event` — the variant shape, the `TruncatedFields` convention, and the `isTurnEvent()` marker every variant declares.
- `internal/turnevent/permission.go` → `PermissionRequest`, `PermissionResponse` — the modal ask/answer pair this event must NOT be confused with; it drives the naming decision below.
- `internal/streamsup/initialize_capture_test.go` → `capturedInitialize`, `initCapturePath` — the multi-arm committed-capture reader: an ARM SELECTOR from a closed set, never a path parameter, with the provenance checks written out at the reader.
- `internal/streamsup/compaction_capture_test.go` → `compactionCapturePath` — the docblock the ticket points at for reader shape: own path and version constants, checks written out rather than borrowed.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `TestDropcapClassification`, `dropcapClassify` — "mapped" there means the shipped parser emits ≥ 1 event for the line, so AC 4's row constrains the gating decision.
- `docs/knowledge/features/streamsup-package-system-maps-per-subtype-since-2026-08-07.md` — the package's own account of why the subtype match sits INSIDE the `ignoredLineTypes` branch, which is what keeps `emitUnrecognized` unreachable from any `system` line. Not to be restructured.
- Fixtures `internal/e2e/realclaude/testdata/bypass_reescalation_v2.1.239_{control_default,enable,reescalate,control_bypass}.json` — re-counted at `f2a4fa1a`: 3 + 3 + 1 + 0 denial lines, every one carrying exactly `type, subtype, tool_name, tool_use_id, message, uuid, session_id`, `tool_name` always `Bash`, `message` 389–400 bytes, `tool_use_id` 30 bytes.

## Context

claude announces a per-call denial on a `system` line whose subtype is `permission_denied`, and the daemon drops it: `emitSystemSubtype` has no arm for the subtype, so it falls to the `ignoredLineTypes` branch's silent debug drop. A client therefore sees only a `tool_result` with `is_error:true`, indistinguishable from a tool that ran and failed. This slice maps the line to a daemon turn event. Nothing reaches the wire here — the protocol frame, the `turnbridge` mapping and the docs are #2233's.

No ADR is warranted: this is the eighth arm on an enumeration whose design ADR already exists.

## Design

### The event — `turnevent.ToolCallDenied` (in `event.go`, beside the other `system`-subtype variants)

**The name is the daemon's, not claude's, and it deliberately avoids `PermissionDenied`.** That spelling would sit beside `PermissionRequest` and `PermissionResponse` in this package, which are the MODAL ask/answer pair, and a reader would take it for that pair's negative answer. It is not: nothing asked. `ToolCallDenied` uses the vocabulary `ToolStart` and `ToolUpdate` already carry.

| Field | Claude's key | Overflow | Bound |
|---|---|---|---|
| `ToolName` | `tool_name` | **drop** | `maxTaskFieldID` |
| `ToolCallID` | `tool_use_id` | **drop** | `maxTaskFieldID` |
| `Message` | `message` | **cut** | `maxDenialProse` |
| `DecisionReasonType` | `decision_reason_type` | **drop** | `maxTaskFieldID` |
| `DecisionReason` | `decision_reason` | **cut** | `maxDenialProse` |
| `TruncatedFields []string` | — | — | daemon names of the fields CUT, declaration order, nil when none |
| `DroppedFields []string` | — | — | daemon names of the fields DROPPED, declaration order, nil when none |

Neither `session_id` nor `uuid` is a field, and the exclusion is structural: they are not declared on the decode target, so they cannot leak (`systemTaskStartedLine`'s rule).

**Cut-or-drop is decided per field, and both precedents are live.** `Message` and `DecisionReason` are prose, where a cut sentence still reads as what it is — `maxCompactField`'s answer. The other three are tokens a consumer MATCHES or JOINS on, where a cut token matches nothing while still looking like one — `maxTurnEndStopField`'s and `maxModelWindowID`'s answer. `ToolCallID` is the sharpest case and the one the ticket flags: it is joined against a `ToolStart` the client already saw, so a cut id is strictly worse than an absent one. This DIVERGES from `BackgroundTaskStarted.ToolCallID`, which cuts and reports; that field predates the join-key rule, and the divergence is stated at both ends rather than left for a reader to find.

**`DroppedFields` is new to the family and is what makes AC 2's second clause true.** With drop-only bounding, an empty `ToolName` is ambiguous between "claude omitted it" and "the daemon dropped it" — exactly the absence-versus-value confusion AC 3 exists to prevent for the decision fields. Two report slices resolve it: a field named in `DroppedFields` was the daemon's doing, a field in neither is claude's. `BackgroundTaskRoster.DroppedTasks` already gives the family the "Dropped" vocabulary for overflow-by-dropping.

### The parser arm — `emitPermissionDenied`

Signature `(line []byte) bool`, reporting that it consumed the line either way, and reached from a new `case "permission_denied"` in `emitSystemSubtype`. The decode's input is the TOP-LEVEL bytes only, per `streamLine`'s stated property: a tool result whose text is literally a `permission_denied` line must not be able to forge a denial.

Decode target `systemPermissionDeniedLine`, five string fields, nothing else declared.

**It does NOT gate on any field, and that is the decision AC 4's row depends on.** Three consuming paths collapse to two:

- undecodable → `Debug` naming the subtype keyword only, no event. `emitBackgroundTaskStarted`'s arm verbatim, on its stated ground.
- decodable → exactly one event, whatever the fields hold, including all-empty.

The contrast with the two gating precedents is the argument. `emitCompactionBoundary` gates because the metadata IS its whole payload and a `Compacting` edge pair has already told the client a compaction happened, so a metadata-less frame would duplicate with nothing added. Here the SUBTYPE is the payload: nothing else on this surface tells a client the call was denied rather than run-and-failed, which is the entire #2232 problem. A gate would also re-drop the line silently in precisely the case where claude renamed a field — the failure this ticket exists to end. `emitBackgroundTaskStarted`'s rule therefore applies: absence is claude's to choose, the field lands empty, the event still fires.

Nothing is logged on the emitting path (`emitCompactionBoundary`'s posture): everything decoded reaches the event, so a second sink would be a record to keep in step for no diagnostic gain — and `message` is the field a drop site would be most tempted to explain itself with.

### The bound — one new constant

`maxDenialProse = 2 << 10` covers both prose fields, as `maxTaskFieldID` serves three and `maxCompactField` serves two. Observed `message` is 389–400 bytes, so 2048 is ~5x the observation. The envelope arithmetic lands on 3×256 + 2×2048 = 4864 bytes worst case — 7.4% of the 65519-byte v2 application-envelope cap, deliberately the SAME worst case `BackgroundTaskStarted` carries, so the event family keeps one number a reader can hold.

The three token fields reuse `maxTaskFieldID` rather than minting a scheme, per the ticket. Its docblock names the three `BackgroundTaskStarted` fields it caps and goes stale on that reuse, so it gains an `AMENDED` paragraph naming this event too.

## Concurrency model

None. The arm reads and writes no `Parser` field — not `p.compacting`, not the accumulator — so it is `emitCompactionBoundary`'s stateless shape and no turn-boundary reset has anything of its to reset. No goroutine, no channel, no lock. `p.emit` is the existing single-threaded emit path.

## Error handling

One failure mode: the line will not decode into the shape (a numeric `tool_name`, say). It consumes the line with a content-free `Debug` and no event, never an `Unrecognized` — keeping `system` whole on `ignoredLineTypes` is what makes "no system line reaches the unrecognized lane" structural, and that guarantee outranks surfacing a malformed line of a subtype already known. The subtype string logged is a message-name keyword owned by this package, not payload.

## Testing strategy

**Fixture replay** — new `internal/streamsup/permission_denial_capture_test.go`, its own reader:

- Own `denialCaptureVersion = "2.1.239"` and `denialCaptureDir` constants; a closed `denialCaptureArms` set of the four arm names; paths minted inside the file. The reader takes an ARM SELECTOR from that closed set, never a path — `capturedInitialize`'s precedent, which is how a multi-arm reader keeps `compactionCapturePath`'s "no path parameter" guarantee.
- Provenance written out at the reader, not borrowed: `claude_version`'s leading token equals the pinned version, the record's own `arm` equals the arm requested, `stdout_events` is non-empty. Every failure is `t.Fatalf` — the fixtures are committed, so absence is a broken premise and a skip would report a deleted fixture as green.
- `stdout_events` decodes to `[]json.RawMessage`, so each line replays as claude's own bytes.
- Scenarios: per-arm denial-event counts pinned at 3 / 3 / 1 / 0 with the zero arm as the control that stops the others passing vacuously (AC 1); each event's three carried strings re-derived from the line's OWN bytes and compared, never from the record's labels; a leak sweep taking each line's own `session_id` and `uuid` values and asserting neither appears in any string field of the event (AC 2, second half); all seven yielding empty `DecisionReasonType` and `DecisionReason`, so absence is pinned as an observation (AC 3).

**Hermetic parser tests** — new `internal/streamsup/parser_permission_denied_test.go`:

- Table-driven over-cap rows, one per claude-derived string, asserting the drop fields land empty and named in `DroppedFields`, the cut fields land at the cap and named in `TruncatedFields`, and an at-cap value trips neither (AC 2).
- A line carrying both decision fields, proving they are carried when claude sends them (AC 3, the other half).
- The no-gate decision: a line with no fields at all still emits, and an undecodable line emits nothing while consuming.

**Live-suite classification** — `TestDropcapClassification` gains a `system/permission_denied` row with `wantDrop: false`, its line carrying no fields at all so the row pins the ABSENCE of a gate rather than a field (AC 4). Proven by running that test under `-tags e2e_realclaude`, which is hermetic and needs no credentials; the package's compilation is proven by `go vet -tags e2e_realclaude`.

**Stale-count corrections** — the switch's arms are the one enumeration, so three comments carrying a count of them are corrected in place and no fourth is added: `compactionPinnedShapes`' docblock in `internal/streamsup/compaction_capture_test.go` and the "What is unknown" docblock in `internal/e2e/realclaude/compaction_capture_test.go` (both also claim an unmatched subtype reaches `emitUnrecognized` — it reaches the silent drop), and the `#2227` correction paragraph in `internal/e2e/realclaude/interactive_stream_unrecognized_test.go`. The `system/compact_boundary` row's `why` in `TestDropcapClassification` is corrected in the same pass: it still calls that subtype the one measured-and-dropped one left standing, which #2237 made false, and it sits directly beside the row this ticket adds.

## Open questions

1. Whether a classifier or deny-rule denial at a later claude carries `decision_reason_type` is unanswered and deliberately not chased: the decode is tolerant either way, so a fresh capture would change documentation and no code.
2. Whether `DroppedFields` should be lifted onto the sibling variants that drop silently today (`CompactionBoundary.Trigger`) — not in scope here; this event is the first with more than one drop-bounded field, which is what makes the report worth carrying.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No finding, and the property is structural rather than careful. The boundary is claude's stdout → daemon event, at `emitPermissionDenied`, which decodes the TOP-LEVEL line bytes only. `consumeLine` gates on the top-level `sl.Type == "system"` and hands the arm the same top-level `line`, so a tool result whose text is literally a `permission_denied` line cannot forge a denial — nested content is never re-scanned. Downstream holds a typed `turnevent.ToolCallDenied` whose every string is claude-authored; the signal is the type's doc, which is the family's existing convention and no stronger here.
- [Trust boundaries, second] MUST STATE (addressed in Phase B by a doc obligation, not a code change). A fabricated or simply mistaken line can name the `tool_use_id` of a call that ACTUALLY RAN AND SUCCEEDED, and a client would then render a successful call as blocked. The daemon does not verify the id against a call it saw, and deliberately will not: the cross-check needs parser state this arm refuses to hold, and it would silently drop a denial whose call preceded a session rotation — reintroducing this ticket's own defect. What bounds the damage is that NOTHING IN THE DAEMON ACTS ON ANY FIELD of this event: no retry, no backoff, no teardown and no routing is keyed on them, so a fabricated value is a misleading label rather than an actuator. `CompactionBoundary`'s doc states the same thing for the same reason. Phase B: carry that sentence onto the event, and say the join is the CLIENT's — an unmatched id renders as unattributed, never as a match.
- [Tokens, secrets, credentials] Finding, bounded by three existing decisions. No secret is generated, stored or compared here, but `message` is claude-authored prose that names absolute host paths in all seven captured lines and, for a rule denial, may quote the denied command line — which can carry a secret an operator typed. It is capped at construction (`maxDenialProse`), it is NEVER LOGGED (the arm logs nothing on the emitting path), and nothing reaches the wire in this slice. Phase B: no `slog` call in this arm may take a decoded field as a value.
- [File operations] No finding, by the reader's design. No production file operation at all. The test reader's paths are minted inside its own file from package constants and selected by an ARM SELECTOR from a closed set — never a caller-supplied path — so there is no traversal surface to canonicalise and no check-then-open gap. This is `capturedInitialize`'s design, adopted for exactly this property.
- [Subprocess / external command execution] Finding, and it is the consumer's to honour. Nothing here executes anything, but `Message` and `DecisionReason` can quote a denied command line, so a consumer that re-shells them executes a command claude was refused. `BackgroundTaskStarted.Description` carries the identical hazard and states the rule at the field. Phase B: carry the same sentence — safe to RENDER as text, never to execute or re-shell.
- [Cryptographic primitives] Not applicable, by a design decision rather than by absence: no value on this path is a secret, no comparison is made against one, and no identifier is minted — every string is claude's, carried or emptied. There is no randomness in the arm.
- [Network & I/O] No finding. The line is capped at 4 MiB by `defaultMaxParseBuf` before the decoder sees it, and every claude-derived string is capped again at construction, for a 4864-byte worst-case event. No RATE bound and none is owed in this slice: a model looping on denied calls yields one small synchronous `emit` per line, and `emit` is a direct sink call with no queue in this package. The fan-out consequence of a chatty denial stream belongs to #2233, which puts the event on the wire — named here so that ticket inherits the question rather than rediscovering it.
- [Error messages, logs, telemetry] No finding. MUST-NOT-log is every decoded field; MUST-log is nothing. The one log is the undecodable-path `Debug`, carrying the literal subtype keyword this package owns — the same class as the `sl.Type` the drop branch already logs — so no byte of claude's line is recorded anywhere. No telemetry.
- [Concurrency] No finding, structurally. The arm reads and writes no `Parser` field, takes no lock, spawns no goroutine, and reaches the existing synchronous sink. There is nothing to order, nothing to leak and no check-then-mutate.
- [Threat model alignment] OUT OF SCOPE — `docs/protocol-mobile.md` § Security model does not bind this slice, because nothing here reaches the wire. #2233 owns the frame, the fan-out and the envelope arithmetic against the live cap, and inherits the two consumer-facing obligations above.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-08
