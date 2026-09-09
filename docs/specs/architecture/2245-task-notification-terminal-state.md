# #2245 — give a background task a terminal state on the wire by mapping claude's `task_notification`

## Files read

- `internal/streamsup/parser.go` → `emitSystemSubtype` — the one place the mapped subtype set is enumerated; this ticket adds an arm there and nowhere else.
- `internal/streamsup/parser.go` → `systemTaskStartedLine`, `systemTaskUpdatedLine` — the family's decode-target discipline: one target per subtype, kept off `streamLine`, field set exactly what the capture shows. `systemTaskUpdatedLine`'s doc is the model for the new target's docblock, including "a field that is never declared cannot leak".
- `internal/streamsup/parser.go` → `emitBackgroundTaskUpdated` — the whole-shape analogue: decode from the TOP-LEVEL bytes, `bound` closure, sequential `bound` calls so `TruncatedFields` order is visible, undecodable → content-free Debug + `true`.
- `internal/streamsup/parser.go` → `maxTaskFieldID`, `maxTaskDescription`, `maxTaskPatch` — the cap family. `maxTaskFieldID`'s two AMENDED entries establish that reusing it for a short vendor token on a second event is the family's move; `maxTaskPatch`'s closing paragraph establishes that a field with a different *reason* gets its own constant even at an equal value.
- `internal/streamsup/parser.go` → `ignoredLineTypes` — its "Still dropped in silence" paragraph names `task_notification` as measured absent on this surface. Goes stale the moment the arm lands; the #1404 `rate_limit_event` correction in the same comment is the editing pattern.
- `internal/streamsup/task_notification_capture_test.go` → `taskNotificationPinnedKeys`, `taskNotificationCapturePath`, `taskNotificationCaptureRecord` — the fourth reader and the pinned key set this mapping may declare from. Its docblock forbids by name growing any of the three older readers a path parameter, so the replay helper lands **here**, not in `capture_test.go`.
- `internal/streamsup/parser_test.go` → `TestParser_TaskUpdatedMapsFromCapture`, `TestParser_IgnoredLineTypesStaySilent`, `taskUpdatedLineFixture` — the test shapes to mirror, and the silence row that must be deleted.
- `internal/turnevent/event.go` → `BackgroundTaskUpdated` — the event to widen; its `Patch` doc is the contract AC 3 protects.
- `internal/turnevent/event.go` → `BackgroundTaskRoster` — its "No terminal, finish, or completion event exists in this family, deliberately" paragraph is a **fourth** stale statement the ticket body does not list. It must be amended here.
- `internal/turnbridge/outbound.go` → the `turnevent.BackgroundTaskUpdated` case in the outbound switch — pure adapter, re-caps nothing.
- `internal/protocol/interactive.go` → `BackgroundTaskUpdatedPayload` — the wire type and its SECURITY block.
- `internal/e2e/realclaude/testdata/task_notification_v2.1.259.json` → `frames[1]`, `observed_notification_keys`, `limitations` — the nine observed keys, the one observed `status` token (`completed`), and the statement that `failed`/`stopped` were never staged.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapExpectedSubtypes`, `TestDropcapClassification` — the tagged-suite pins `make check` cannot compile.
- `docs/protocol-mobile.md` § `background_task_updated` and § Changelog — the wire contract and the "No terminal/finish event exists in the family, deliberately" sentence this ticket falsifies.

## Context

A client that draws a background-task row has no way to close it. `background_task_started` opens the row; `background_task_updated` carries only claude's opaque `patch`, whose one observed key is `is_backgrounded`; `background_task_roster` is a snapshot the daemon refuses to diff on the client's behalf. Nothing the daemon sends says a task ended, so every row opened stays open for the session.

claude does say it, on `system/task_notification`, and since #2247 closed the bytes are committed. The subtype has no arm in `emitSystemSubtype`, so it reaches that switch's `default` and is dropped in silence.

The whole licence to declare is `taskNotificationPinnedKeys`: `type`, `subtype`, `task_id`, `tool_use_id`, `status`, `output_file`, `summary`, `uuid`, `session_id`. The Agent SDK additionally describes `ambient`, `skip_transcript` and `usage`; the record files all three under `keys_documented_not_observed` and this ticket declares none of them.

No ADR is warranted. This is one more arm on an enumeration that already exists, under rules three prior tickets in the family already wrote down.

## Design

### The frame

The terminal state widens the **existing** `background_task_updated` frame rather than adding a fourth. One frame therefore ends up with two producing subtypes, which is the one genuinely new structural fact and the thing the docs must state.

The obvious-looking alternative — report the terminal state as a synthesized `patch` carrying a `status` key — is **unavailable**. `Patch` on both `turnevent.BackgroundTaskUpdated` and `protocol.BackgroundTaskUpdatedPayload` is documented as claude's own patch object carried whole and unparsed, with no key enumerated, and the captured `task_notification` line carries no `patch` key at all. Synthesizing one falsifies both doc comments and destroys the guarantee a consumer reads them for. AC 3 is that guarantee restated as a criterion.

### Declared field set

Three of the nine observed keys:

| claude key | daemon field | why |
|---|---|---|
| `task_id` | `TaskID` | the join key AC 2 names |
| `status` | `Status` | the terminal state itself |
| `summary` | `Summary` | claude's account of what the task did; the payoff of a row that resolves |

Six are **not declared**, each for a reason stated at the declaration site:

- `output_file` — documented as a path on the operator's host. AC 4. Absence from the decode target is the stronger guarantee than a redaction pass, and it is the guarantee `systemTaskUpdatedLine`'s doc already invokes: a field that is never declared cannot leak. The captured value is the empty string, so the capture does not demonstrate the hazard; the documented field class is the reason, not the observation.
- `tool_use_id` — no criterion needs it. `background_task_started` already published the tool call under `tool_call_id`, and the join key is `task_id`. Declaring it on the **shared** update event would leave it empty on every event the `task_updated` arm produces, which a consumer cannot distinguish from claude omitting it. Stated on the event beside the `uuid` / `session_id` omissions.
- `uuid`, `session_id` — the family's standing omissions, unchanged (#1380).
- `type`, `subtype` — segmentation, `streamLine`'s business.

`Status` is a **plain string, not a closed token set.** One token has been observed (`completed`). The record's `limitations` says `failed` and `stopped` were never staged and that nothing in it says what they carry, so a closed set would declare two of three members from a docs page. `BackgroundTaskStarted.TaskType` settled this exact case already.

### Emptiness is the discriminator

The two producing subtypes populate disjoint field sets: the `task_updated` arm fills `Patch` and leaves `Status`/`Summary` empty; the `task_notification` arm fills `Status`/`Summary` and leaves `Patch` empty. A non-empty `Status` is therefore what says "a terminal state was reported on this frame", and that reading is stated on the fields, on the payload and in the protocol doc rather than left to be inferred. No daemon-authored discriminator field is added: it would be exactly the synthesized content AC 3 forbids in the neighbouring field.

### Parser

New decode target, peer of `systemTaskUpdatedLine`, kept off `streamLine` for its stated reason:

```go
type systemTaskNotificationLine struct {
    TaskID  string `json:"task_id"`
    Status  string `json:"status"`
    Summary string `json:"summary"`
}
```

All three are `string`, so a non-string value in any of them fails the whole decode and takes the undecodable path — the asymmetry `systemTaskUpdatedLine` documents does not arise here because no field carries an unconstrained shape.

New emit function `emitBackgroundTaskNotification(line []byte) bool`, named for the **line** rather than the event because it is the second producer of one event and a name matching the event would collide. Signature and behaviour are `emitBackgroundTaskUpdated`'s verbatim: decode from the top-level `line` bytes, never a nested field; `bound` closure appending cut names in call order; emit; return `true` either way.

New arm in `emitSystemSubtype`: `case "task_notification": return p.emitBackgroundTaskNotification(line)`.

### Caps

- `Status` → **`maxTaskFieldID`**, reused. A short vendor token from a set claude owns is the exact shape that constant's two AMENDED entries already reuse it for (`task_type`, `tool_name`, `decision_reason_type`, `refusal_category`). A fourth event, and the enumeration in its first sentence stays unrestated for the reason that comment gives.
- `Summary` → **new constant `maxTaskSummary = 4 << 10`.** Model-authored free text with no documented bound; neither existing constant fits by reason. Its own constant even at a value equal to `maxTaskDescription` and `maxTaskPatch`, on `maxTaskPatch`'s stated grounds: folding them would make a change to one budget silently move another. The docblock carries the observation (the capture's summary is the task's command text; `parent_tool_use_v2.1.259.json` holds a multi-line prose summary of ~120 bytes on the same subtype) and the envelope arithmetic in `maxUnrecognizedRaw`'s style — worst case one `task_notification`-produced event carries 256 + 256 + 4096 = 4608 bytes, deliberately in line with the family's existing 4352 / 4864.

`TruncatedFields` names `task_id`, `status`, `summary` — no translation, claude's keys and the daemon's fields agree.

**Envelope arithmetic**, in `maxUnrecognizedRaw`'s style, and it carries two numbers deliberately. The `task_notification` arm's worst case is 256 + 256 + 4096 = 4608 bytes of claude-derived text, 7.0% of the v2 application-envelope cap of 65519 bytes and deliberately in line with the family's existing 4352 and 4864. The **type's** ceiling is higher: the two arms populate disjoint fields today, but nothing structural stops a third producer filling all four, which would be 256 + 256 + 4096 + 4096 = 8704 bytes, 13.3%. Both numbers are written down so a future third arm is a decision somebody makes rather than a silent envelope regression.

### Event, bridge, wire

`turnevent.BackgroundTaskUpdated` gains `Status` and `Summary`; its docblock stops saying it maps one subtype and says which fields arrive from which. `protocol.BackgroundTaskUpdatedPayload` gains `status` and `summary`. The bridge arm copies both across verbatim — pure adapter, re-caps nothing, as it already does for `Patch`.

**`Status` is claude's report, not the daemon's observation.** The daemon does not verify that a task reporting `completed` has stopped running, and it has one observation of one token. That is stated on the field, on the payload and in the protocol doc, because it is the same honesty the roster's docblock enforces when it refuses to infer a finish from a disappearance: what the daemon carries is a claim claude made, and a consumer must not read it as a detection.

**The SECURITY block on `BackgroundTaskUpdatedPayload` is extended, not inherited.** It currently governs `patch` alone. `Summary` is model-authored free text a client will actually render in a task row — unlike `patch`, which the doc tells a client to treat as an opaque blob — and in the committed capture its value **is the task's literal command line**, the same hazard `BackgroundTaskStarted.Description` already carries. So the render-as-inert-text / never-execute / never-re-shell / never-feed-to-an-HTML-sink rule is restated at `Summary`'s own declaration site rather than delegated to a sibling, and `Status`, though a short token from a set claude owns, is claude-authored text under the same rule.

Both widenings are **additive on keyed struct literals**, so no consumer call site is forced to change: all eight `BackgroundTaskUpdated{` and five `BackgroundTaskUpdatedPayload{` literals in the repo are keyed.

### Stale committed statements

Four, three named by the ticket and one found while reading:

1. `ignoredLineTypes`' "Still dropped in silence" paragraph — drop the `task_notification` naming, add a dated CORRECTED entry in the same comment's own idiom (#1404's `rate_limit_event` correction is the pattern).
2. `parser_test.go`'s `TestParser_IgnoredLineTypesStaySilent` — delete the `system/task_notification` row and its comment. A red here is the expected first signal.
3. `dropcapExpectedSubtypes` / `TestDropcapClassification` in `internal/e2e/realclaude`, behind the `e2e_realclaude` tag. `dropcapExpectedSubtypes` is a **census-absence** list, not a mapped/dropped list, so its membership is unaffected; what needs checking is whether any prose there asserts this subtype is unmapped, and whether `TestDropcapClassification` needs a row now that the subtype maps. Verified under the tag with `go vet` and `go test -run`, not by reading a green `make check`.
4. **`turnevent.BackgroundTaskRoster`'s docblock** — "No terminal, finish, or completion event exists in this family, deliberately… the daemon does not report a finish it cannot detect." That is now false, and it is the statement a reader of the family is most likely to reach first. Amended with a dated entry; the measurement behind the original stays legible.

`docs/protocol-mobile.md` carries the same sentence in its 2026-08-09 changelog entry. That entry is a dated historical record and stays unedited; a new dated entry corrects it, which is how this file already handles superseded statements.

## Concurrency model

None. The parser is single-goroutine over its own buffer, the new arm adds no goroutine, no channel, no lock and no shutdown path. `json.RawMessage` aliasing does not arise: all three declared fields are `string`, which `encoding/json` allocates fresh.

## Error handling

One reject branch, inherited whole from `emitBackgroundTaskUpdated`:

| input | behaviour |
|---|---|
| line will not decode into the shape (numeric `task_id`, `status` an object, …) | content-free `Debug` naming the subtype keyword only, no event, returns `true` |
| a declared key absent | field lands empty; absence is claude's to choose and there is no captured negative case, so no validation rule is invented |
| a declared string over its cap | cut at construction, name appended to `TruncatedFields` |

No `Unrecognized` is produced on any path. `system` stays whole on `ignoredLineTypes`, which is what keeps "no system line reaches the unrecognized lane" structural — and it is what AC 1's "and no `unrecognized_message`" asserts.

## Testing strategy

RED first, in this order:

1. **Delete** the `system/task_notification` silence row. The suite goes red at the new replay test, not here.
2. `capturedTaskNotificationLine(t)` — a replay helper in `task_notification_capture_test.go`, that file's own fourth-reader discipline: its own constants, no path parameter, provenance checked (`is_capture`, `claude_version`, `payload_encoding`, exactly-one-frame). No older reader grows a parameter.
3. `TestParser_TaskNotificationMapsFromCapture` — mirrors `TestParser_TaskUpdatedMapsFromCapture`. `TaskID`, `Status` and `Summary` **derived from the capture's own payload**, not pinned, because the capture is redacted; plus one pinned canary (`status` = `completed`) proving the reader picked the right record. Asserts `Patch == ""` (AC 3) and `TruncatedFields == nil`.
4. **Reflection sweep** over every string field of the emitted event for the capture's `output_file`, `tool_use_id`, `uuid` and `session_id` values, with a non-vacuity floor as a literal. `output_file`'s captured value is the empty string, so a value-equality sweep would be vacuous for it — the `output_file` assertion is instead **structural**: `reflect` over `systemTaskNotificationLine`'s fields and its JSON tags, asserting no field maps to `output_file`. That is AC 4 asserted where AC 4 actually lives.
5. **Cap tests** — a synthesized line per bounded field at cap+1, asserting the cut and the `TruncatedFields` name and order. Cap values as **literals**, not the constants, per `taskStartedCapCheat`'s stated rule: a fixture built from the constant it validates asserts nothing about the number.
6. **Undecodable** — `task_id` numeric: no event, one content-free Debug carrying `subtype=task_notification` and nothing else.
7. `turnbridge` — one case for a `task_notification`-shaped event (status/summary set, patch empty) and one confirming the existing `task_updated`-shaped case still maps with the new fields empty.
8. `protocol` — a new `testdata/background_task_updated_terminal.json` fixture round-tripped; the existing `background_task_updated.json` stays untouched so the patch-only shape keeps its own pin.
9. **`cmd/pyry`'s log-leak sweep is extended, and this is a requirement rather than a nicety.** `TestInteractiveTurnEmitterV2_BackgroundTasksNoLogLeak` enumerates one marker string per claude-derived field **by hand**, so widening a struct does not widen the test: without an edit, the no-leak property is simply untested for `Status` and `Summary`, and `Summary` is the field with the most to leak. Both get their own marker.
10. Under the tag: `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` plus `go test -tags e2e_realclaude -run TestDropcap ./internal/e2e/realclaude/`, which needs no claude.
11. Check whether `turnbridge`'s envelope-cap test enumerates per-payload worst cases; if it does, the new arm's 4608 lands there.

Gate (§ B2): `go test -race` on `internal/streamsup`, `internal/turnevent`, `internal/turnbridge`, `internal/protocol` and `cmd/pyry`, plus `go vet ./...` and `go build ./cmd/pyry`.

## Open questions

1. **Does `TestDropcapClassification` need a `system/task_notification` row?** Its neighbours each carry one with a derived verdict. Resolve by reading the test under the tag in Phase B; record the answer in `## Revisions`.
2. **Does `turnbridge`'s envelope-cap test enumerate per-payload worst cases?** If it does, the new arm's 4608 belongs in it. Resolve by reading it in Phase B.

(The log-leak sweep was an open question in the first draft of this plan; the security review reclassified it as a requirement, and it now sits in the testing strategy where a verifier can check it landed.)

## Revisions

**2026-09-10 — both open questions resolved, and one departure from the plan.**

1. **`TestDropcapClassification` needed a row, and it got one.** Every neighbour in that table carries a row whose verdict is *derived* from the shipped parser rather than declared, and `task_notification` had none only because it had never been observed on that surface. The new row asserts the subtype now maps, and it pins something its three drop neighbours cannot: this arm **gates on nothing**, so a bare line carrying no fields still maps, where `init`, `status` and `compact_boundary` each read as drops because their arms have a gate a bare line does not satisfy. Adding a gate here turns the row red, which is what makes it a pin rather than a restatement. `dropcapExpectedSubtypes` is **unchanged**: it is a census-absence list naming what #1247 saw on the headless surface, not a mapped-versus-dropped list, so mapping the subtype does not move its membership.
2. **The per-payload envelope worst case lives in `internal/protocol/interactive_test.go`, not in `turnbridge`.** `turnbridge`'s `maxV2AppEnvelope` is a test-local constant used for a different purpose and its docblock is explicit that this package enforces no envelope bound. The protocol table was the right place and it was widened — to all four claude-derived fields at once, which is deliberately **more than any single frame can carry**, so it measures the event type's 8704-byte ceiling rather than the arm's 4608. That is the number a third producing subtype would have to fit inside.

**Departure: `internal/protocol/testdata/background_task_updated.json` had to change**, where the plan said it would stay untouched. `roundTripEnvelope` compares bytes exactly and neither new field is `omitempty` (the family's convention — `patch` is always present too), so the existing fixture no longer round-tripped. It gained `"status":""` and `"summary":""` and nothing else, which keeps it what it was: the pin on the patch-only shape. The new `background_task_updated_terminal.json` pins the other shape, and the pair is the assertion — a single fixture carrying both sets would be a frame the daemon cannot emit and would let a regression merging the two sets pass.

## Security review

**Verdict:** PASS (first pass returned FAIL on two MUST FIX findings; the plan above was revised and the checklist re-run from the top)

**Findings:**

- **[Trust boundaries] MUST FIX — addressed in this revision.** The boundary is claude's subprocess stdout crossing into the daemon's event stream and then to a remote client, and this frame's SECURITY block on `BackgroundTaskUpdatedPayload` currently governs `Patch` alone. `Summary` is a different class of hazard from `Patch`, not the same one inherited: the doc tells a client to treat `Patch` as an opaque blob it must not even assume parses, whereas `Summary` is prose a client will actually render in a task row — and in the committed capture its value is the task's **literal command line**, which is the hazard `BackgroundTaskStarted.Description` already carries and states at its own site. The first draft delegated it to the sibling. Revised: the render-as-inert-text / never-execute / never-re-shell / never-feed-to-an-HTML-sink rule is restated at `Summary`'s own declaration on the event, on the payload, and in `docs/protocol-mobile.md`, with `Status` under the same rule as claude-authored text.
- **[Trust boundaries] No further findings.** The boundary is a single named function, `emitBackgroundTaskNotification`, and it decodes from the **top-level** `line` bytes and never a nested field. That is the property `streamLine`'s doc states and it is load-bearing here, not stylistic: decoding this payload from nested content would let a tool result whose text is literally `{"type":"system","subtype":"task_notification",…}` forge a terminal state and close a client's row for a task that is still running.
- **[Error messages, logs, telemetry] MUST FIX — addressed in this revision.** `TestInteractiveTurnEmitterV2_BackgroundTasksNoLogLeak` is the test that enforces "no claude-derived text from this family reaches a log", and it enumerates one marker string per field **by hand**. Widening a struct therefore does not widen the test: the property would silently go untested for exactly the two new fields, one of which is unbounded model prose. The first draft filed this as an open question, which is the deferral this pass exists to catch. Revised into a stated testing requirement with a marker per new field. The parser's own undecodable-path `Debug` stays content-free and logs the subtype keyword only — notably not `Summary`, which is the field a handler is most tempted to explain itself with.
- **[Network & I/O] SHOULD FIX — addressed in this revision.** Input size is the only I/O dimension this change moves, and both new fields are bounded at construction (`maxTaskFieldID`, the new `maxTaskSummary`) so an oversized payload never enters the event stream, a queue, or a log. The concern was under-statement rather than absence: quoting only the arm's 4608-byte worst case hides that the **type's** ceiling is 8704 if a third producer ever fills all four fields. Both numbers are now in the plan and go in the constant's docblock.
- **[File operations] Not applicable by design, and this is the ticket's central security decision rather than an absence.** `output_file` is the one path-shaped value on the line and it is **never declared on the decode target**, so no code path in the daemon ever holds it — no concatenation, no canonicalisation question, no TOCTOU, no symlink question, because there is no path. A field that is never declared cannot leak, which is `systemTaskUpdatedLine`'s stated guarantee and strictly stronger than redacting one. #2247's own security review built a third redaction mechanism (`tncapUnredactedPathFields`) around this same field class, which is the measure of how seriously the class was taken.
- **[Subprocess / external command execution] No findings; one hazard named.** The change spawns nothing and passes no value to `exec.Command`. The hazard it adds is downstream and is covered by the first finding: `Summary` is the family's **second** field that can carry a command line, so the never-re-shell rule is stated at its site rather than delegated.
- **[Tokens, secrets, credentials] No findings.** Nothing is generated, stored, rotated, or compared. `session_id`, claude's session identity, is deliberately not declared — the family's standing omission, so a consumer or a later wire mapper cannot route on it.
- **[Cryptographic primitives] Not applicable.** No RNG, no key, no comparison against a secret; the change is field carriage and adds no security decision of its own.
- **[Concurrency] No findings.** No goroutine, channel, lock, or shared mutable state is added; the parser is single-goroutine over its own buffer. All three declared fields are `string`, which `encoding/json` allocates fresh, so nothing aliases `p.buf` and nothing outlives the buffer it came from — the property `emitBackgroundTaskUpdated` documents for `json.RawMessage` holds here trivially.
- **[Threat model alignment] No findings.** The frame is binary → phone only and `interactive`-gated, and `compat_test.go`'s `background_task_updated-rejected` row already asserts it is refused inbound; widening a payload does not change direction, so a hostile phone gains no injection surface. The residual — that claude could report `completed` for a task still running — is not a new surface, because claude's stdout is the trust source for the whole event stream. It is an honesty question rather than an exploit, and the revision answers it the way this family already answers it for the roster: `Status` is documented as claude's **report**, carried, never the daemon's own detection.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-10

## Size

Over the 800-line ceiling by roughly 100, stated rather than split, and this matches the refiner's estimate. Every other boundary holds: 4 production source files, 0 new exported types, 0 forced consumer call-site updates, 5 acceptance criteria, 1 reject branch. The parser-to-event half's only consumer is the wire half — a slice whose sole consumer is one sibling is part of that sibling, so cutting them apart would ship an event nothing emits and leave neither half verifiable on its own. The floor wins over the ceiling.
