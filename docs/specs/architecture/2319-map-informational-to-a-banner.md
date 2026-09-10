# #2319 — `streamsup`: map `system/informational` to a banner, proven against the committed capture

## Files read

- `internal/streamsup/parser.go` → `emitSystemSubtype` — the one enumeration of the mapped set; this arm is a new `case`.
- `internal/streamsup/parser.go` → `emitPermissionDenied`, `emitCompactionBoundary` — the two nearest mapping precedents, and the two answers to the gate question this arm has to pick between.
- `internal/streamsup/parser.go` → `consumePermissionDeniedLine` — the decode-failure recovery gate, and why this arm owes nothing of its shape.
- `internal/streamsup/parser.go` → `maxCompactTrigger`, `maxDenialProse`, `maxCompactField` — the cap-doc form this ticket's constants follow, and the drop-versus-cut split argued at each.
- `internal/streamsup/parser.go` → `systemTaskStartedLine`, `systemPermissionDeniedLine`, `systemCompactBoundaryLine` — the per-subtype decode-target discipline, including the standing rule for `session_id` and `uuid`.
- `internal/streamsup/parser.go` → `truncateField` — the cut helper, and its stated UTF-8 posture.
- `internal/streamsup/parser.go` → `ignoredLineTypes` and `consumeLine`'s default branch — why an unmapped `system` subtype falls to a silent debug drop and never reaches `emitUnrecognized`.
- `internal/streamsup/compaction_capture_test.go` → `compactionCapturePath`, `TestCompactionFixtureReplayPublishesTheBoundary`, `capturedStrings` — the capture-replay shape, its argument for reading bytes out of `internal/e2e/realclaude/testdata/`, and the leak-sweep pattern.
- `internal/streamsup/task_notification_capture_test.go` → `taskNotificationCapturePath`, `taskNotificationPinnedKeys` — the most recent reader, and the one whose fixture had already landed when it was written.
- `internal/turnevent/event.go` → `Banner` and its four fields — the contract this arm produces, including the bound and the drop-versus-cut split stated there as owed here.
- `internal/protocol/codes.go` → `TypeBanner` — one of the three stale "no producer" claims.
- `internal/protocol/interactive.go` → `BannerPayload` — the wire shape the bridge copies verbatim.
- `internal/turnbridge/outbound.go` → `MapEvent` — the arm that already carries `Banner`, and which re-caps nothing.
- `internal/e2e/realclaude/testdata/operator_system_lines_v2.1.259.json` — the committed capture: one `informational` frame, `events_emitted: 0`, keys `content` / `level` / `prevent_continuation` / `session_id` / `subtype` / `type` / `uuid`.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `TestDropcapClassification` — the subtype-to-drop pin, and its two `permission_denied` rows as the mapped-row shape.
- `docs/protocol-mobile.md` § `banner` and § Changelog — the third stale claim, and the dated-record convention for the repair.
- `docs/knowledge/features/streamsup-package-system-maps-per-subtype-since-2026-08-07.md` — the chronicle of this switch, and the reason each arm states its own gate rather than inheriting one.

## Context

`system/informational` is the line `claude` uses for non-error status text about the session: hook feedback, a `UserPromptSubmit` hook's block reason, text a slash command prints. `emitSystemSubtype` has no arm for it, so it falls to `consumeLine`'s silent debug drop. The operator-visible consequence is the worst kind: a prompt a hook refuses is never answered, and nothing at all tells the operator why — a blocked message is indistinguishable from an accepted one.

The wire frame, the `turnevent.Banner` variant, the `internal/turnbridge` arm and the v2 emitter arms all landed with #2256. This ticket is the producer, and the first one, so the 4 KiB bound `turnevent.Banner.Text`'s doc publishes as a contract is enforced here for the first time.

The capture is committed. `internal/e2e/realclaude/testdata/operator_system_lines_v2.1.259.json` holds exactly one `informational` frame, recorded at `events_emitted: 0` — the pre-mapping observation this ticket's replay changes.

No ADR is warranted. This is an eleventh arm on an established switch, and every design question it raises is answered by a precedent already documented at the symbol that holds it.

## Design

### The arm

`emitSystemSubtype` gains a `case "informational"` calling `emitInformationalBanner`. The name carries the subtype rather than the event, deliberately: #2258 will map `notification` onto the same `turnevent.Banner`, so an `emitBanner` would collide with the sibling that follows.

### The decode target

`systemInformationalLine`, its own per-subtype struct on `systemTaskStartedLine`'s standing rule. `streamLine` stays the line-level segmentation struct at Type/Subtype/Message, and `TestStreamLine_StaysSegmentationOnly` fails the build on a widening.

| Field | Key | Type |
|---|---|---|
| `Content` | `content` | `string` |
| `Level` | `level` | `string` |
| `PreventContinuation` | `prevent_continuation` | `bool` |

Exactly what the capture shows and nothing else. The two keys the captured line also carries stay undeclared: `session_id`, because `claude`'s session identity is not the daemon's conversation identity, and `uuid`, because nothing in the daemon reads it. Absent from the decode target is stronger than a scrub — a field never declared cannot leak, and the capture-replay leak sweep proves it against the real values rather than against a hand-typed placeholder.

`PreventContinuation` is a plain `bool` and the two strings are plain strings, so a value of the wrong JSON type fails the whole decode and takes the undecodable path. Fail-closed, as `emitPermissionDenied` and `emitModelRefusalFallback` both are.

### No decode-failure gate is owed

`consumePermissionDeniedLine` exists because a real denial spells `message` as a string where `streamLine` declares `*streamMessage`, so the line fails the whole-line decode before its type is read. The captured `informational` line decodes into `streamLine` cleanly with `message` absent, which the record states as `decodes_into_stream_line: true` / `message_json_type: "absent"`. Nothing of that gate's shape transfers, and adding one on speculation would widen the narrow match that gate's safety rests on.

### The gate: an empty `content` emits nothing

This is the decision the ticket names as owed, taken against the two precedent docs rather than by default.

`emitPermissionDenied` gates on nothing because **the subtype is the payload**: nothing else on that surface separates a denied call from one that ran and failed, so a field-less line is still news. `emitCompactionBoundary` gates on `compact_metadata` because **a frame carrying no trigger and no count would be a claim with no content**.

The second one fits. `turnevent.Banner` reports *operator-facing text `claude` printed*, and `Text` is the field the variant exists to carry. With `content` empty there is no operator-facing text: `Level` is a rendering attribute of nothing, and `StopsTurn` is a report nothing in the daemon acts on. A client renders this frame as a first-class notice, so an empty banner is visible chrome saying nothing — strictly worse for the operator than the silence it replaced. Unlike the denial case, the subtype itself carries no fact a client could act on; "`claude` had something to say" is not news when the thing it said is empty.

The empty gate is pinned by a unit test inside `make check`, so removing it reddens the standard gate rather than waiting for the live one.

### The two bounds

Both new, both unexported, both landing beside `maxCompactTrigger` and `maxDenialProse`, and both applied at construction so an oversized value never enters the event stream.

`maxBannerText` = `4 << 10`. The 4 KiB `turnevent.Banner.Text`'s doc publishes as the contract this ticket owes. It **cuts** via `truncateField` and the cut **is reported** through `turnevent.Banner.Truncated`, which is that field's whole purpose: prose survives a cut as what it is.

`maxDenialProse` is deliberately **not** reused, and the reason is stated at the new constant because the mistake reads as intentional. It is 2 KiB — half the published contract — and it already caps a `claude`-authored prose field the daemon spells `banner`, `turnevent.ModelRefusalFallback.Banner`. The field names match while the events do not, so a reuse would silently halve a published bound at the one site nobody would re-read.

`maxBannerLevel` = 256, the family's token value. It **drops** rather than cuts and the drop is **unreported**, on `maxCompactTrigger`'s and `turnevent.Banner.Level`'s shared reasoning: a client matches this token against an open set, so a cut token matches nothing while still looking like a value, and an emptied scalar is directly observable where an absence would have to be inferred. It cannot share `maxBannerText` under the one-constant-over-several-fields form that `maxTaskFieldID` and `maxDenialProse` use — that form covers fields of the same kind taking the same answer, and these two take opposite answers for opposite reasons.

No envelope-fit measurement is taken. #2256 declined one and left it to ride this constant; every `FitV2EnvelopeCap` test in the repo guards an aggregate whose per-field caps compose badly, and one 4 KiB string against a 65519-byte envelope is not that shape. The arithmetic is stated in the constant's doc instead: worst case 4096 + 256 on a frame whose only other content is a conversation id and a bool.

### Data flow

```
claude stdout line
  → consumeLine  (streamLine decode; type "system" is on ignoredLineTypes)
  → emitSystemSubtype("informational")
  → emitInformationalBanner(line)          // TOP-LEVEL bytes, never a nested field
      ├─ decode fails            → Debug naming the subtype only; consumed; no event
      ├─ content == ""           → consumed; no event; nothing logged
      └─ otherwise               → exactly one turnevent.Banner; nothing logged
```

The decode's input is the top-level bytes, which is `emitPermissionDenied`'s forgery argument held for a further entry point: `streamLine`'s doc states that control shapes are read from the top level only and nested content is never re-scanned, so a tool result whose text is literally an `informational` line cannot mint a banner.

### Contract

```go
// caps, beside maxCompactTrigger and maxDenialProse
const maxBannerText  = 4 << 10 // cut, reported via Banner.Truncated
const maxBannerLevel = 256     // dropped, unreported

type systemInformationalLine struct { /* content, level, prevent_continuation */ }

// always consumes the line; emits at most one turnevent.Banner
func (p *Parser) emitInformationalBanner(line []byte) bool
```

## Concurrency model

None added. `emitInformationalBanner` is called from `consumeLine` on the parser's own read goroutine, reads and writes no `Parser` field, and spawns nothing. It is stateless in the sense `emitCompactionBoundary` states for itself: no turn boundary has anything of this arm's to reset, and a banner arriving inside, between or outside a turn maps identically.

## Error handling

- **Undecodable line** → one `Debug` naming the subtype keyword and nothing else, then consume. `emitPermissionDenied`'s and `emitCompactionBoundary`'s shared arm, on its stated ground: the subtype is a message-name keyword rather than payload, and no `claude`-authored field was decoded on this path. Not surfaced as `Unrecognized` — keeping `system` whole on `ignoredLineTypes` is what makes "no system line reaches the unrecognized lane" structural.
- **Empty `content`** → consume, no event, nothing logged. The gate above.
- **Over-cap `Text`** → cut at `maxBannerText`, `Truncated` true.
- **Over-cap `Level`** → emptied, no report.
- **Nothing is logged on the emitting path.** This is load-bearing rather than tidy, and more so here than at any prior arm: the observed `content` names a host filesystem path and echoes the operator's own prompt back verbatim. It is the field a drop site would be most tempted to explain itself with, and it must never reach a log. `emitThinkingProgress`'s posture otherwise applies — everything decoded reaches the event, so a second sink would be a record to keep in step for no diagnostic gain.

## Testing strategy

**Unit, `internal/streamsup/parser_informational_banner_test.go`** — table-driven against a real `Parser`:

- the observed shape maps to exactly one `Banner` carrying `level`, `text` and `stops_turn`;
- an empty `content` emits nothing (pins the gate inside `make check`);
- a non-bool `prevent_continuation` emits nothing and is still consumed;
- `text` one byte over `maxBannerText` is cut to the cap and `Truncated` is true;
- `text` at exactly the cap is not cut and `Truncated` is false;
- `level` one byte over `maxBannerLevel` is emptied while `Truncated` stays false — the non-report;
- a level at the cap survives whole.

**Capture replay, `internal/streamsup/informational_capture_test.go`** — a further reader on `compaction_capture_test.go`'s terms, not a generalisation of any before it: its own path and version constants, no path parameter, every provenance check written out. It fatals rather than skips on a missing fixture, because the bytes are committed — the sequencing gate its two nearest siblings carry exists for a fixture that had not landed yet, and this one has. It asserts:

- provenance: `is_capture` true, `claude_version`'s leading token equal to the pinned release;
- the record holds at least one `informational` frame (non-vacuity: zero fatals);
- replaying that frame's own bytes through a real `Parser` yields exactly one `Banner`;
- its three values equal the ones re-derived from the captured line's own bytes, never from a literal this file also feeds the parser;
- non-vacuity for those three: a line whose content and level were empty and whose flag was false would satisfy all three while proving nothing;
- a leak sweep in `capturedStrings`' shape — every string value on the line other than the two that should cross must be absent from the rendered event, which is what proves `session_id` and `uuid` cannot be read into daemon state.

**Build-tagged, `internal/e2e/realclaude/dropped_line_capture_test.go`** — one `informational` row in `TestDropcapClassification`, carrying `content` so it pins the subtype as **mapped**. The `why` names the gate, so a reader knows a content-less line of this subtype still reads as a drop. That row compiles only under `e2e_realclaude`, which is why this ticket carries `needs-real-claude`.

**Not added:** a zero-`unrecognized_message` assertion on this path. `system` sits on `ignoredLineTypes`, so a system line that decodes cannot reach `emitUnrecognized` at all and an unmapped subtype falls to the silent debug drop instead. The count reads zero whether or not this ticket ships, which makes the assertion vacuous.

## Doc repairs

Three live places tell a reader that nothing emits a `banner`. Each is repaired to name the producer that now exists and to say what is still outstanding — #2258 and the two subtypes the capture recorded unobserved — and no more than that. The existing "each proven against a committed capture" wording is deliberately not carried into any of them: the committed capture records `local_command_output` and `notification` as unobserved, with `notification` explicitly supporting no absence claim, and #2258 owns deciding what that leaves mappable.

- `docs/protocol-mobile.md` § `banner`, the paragraph beginning "The frame ships ahead of its producers".
- `internal/protocol/codes.go` → `TypeBanner`, the "IT SHIPS WITH NO PRODUCER" paragraph.
- `internal/turnevent/event.go` → `Banner`, the same claim in the same words.

Plus, in `docs/protocol-mobile.md` § Changelog, a new dated entry carrying what changed. The #2256 entry stays unedited: the changelog is a set of dated records, not a running description.

One further attribution, in the same block as the third repair: `turnevent.Banner.Text`'s doc names #2257 as the ticket whose constant enforces the 4 KiB bound. The constant lands here, so the reference is corrected to this ticket. It is one parenthetical and no other #2257 reference is touched.

## Open questions

1. **Should the arm gate on an empty `level` as well as an empty `content`?** Resolved in the Design above: no. `Level` is an open-set token a consumer must already handle as absent, and `turnevent.Banner.Level` states that carrying the empty value says "no level I can offer you". Only the text's absence empties the frame.
2. **Does `TestDropcapClassification` also want a bare drop row to pin the gate?** Resolved: no. The precedent pairs (`task_progress`) use a bare row where the gate has no cheaper proof, and here the gate is pinned by a unit test inside `make check`. AC 3 asks for a mapped row, and a second row would be padding on a ticket the estimate puts near the ceiling.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** The boundary is `claude`'s stdout → daemon state, and this ticket crosses it in exactly one place: `emitInformationalBanner`'s decode into `systemInformationalLine`. Two properties close the category. The decode's input is the **top-level bytes**, never a nested field — `streamLine`'s doc states that control shapes are read from the top level only and nested content is never re-scanned, which is what stops a tool result whose text is literally an `informational` line from minting a banner; decoding from anywhere else would let `claude`'s own tool output forge one. And downstream holds the data as untrusted knowingly: `turnevent.Banner`'s SECURITY paragraph, `protocol.BannerPayload`'s doc and `docs/protocol-mobile.md` § `banner` each say the daemon bounds and does not sanitize, and no daemon code reads any field on the variant.
- **[Tokens, secrets, credentials]** No token is generated, stored, rotated or compared. The category still applies in one direction: `content` is a hook's stderr, which is an operator-authored script's arbitrary output and can echo an environment variable, a credential among them. Two things contain it. It reaches only paired interactive clients, which is no new data class — `tool_use`'s verbatim input fields already carry host paths and prompt text to the same grant, stated in § `banner`. And it reaches **no log at all**: nothing is logged on the emitting path, which is why that rule is load-bearing here rather than tidy.
- **[Error messages, logs, telemetry]** SHOULD FIX, two concrete items the verifier can check in the diff. First, the decode error must be **discarded, never logged** — `encoding/json` quotes the offending input into its error text, so a `"err", err` attribute would route the operator's echoed prompt and a host filesystem path into the daemon log through a channel no per-attribute check can see. `emitRecoveredDenials` and `decodeModelWindows` both discard for this reason. Second, the undecodable Debug must name the subtype as a **daemon-authored literal**, matching `emitPermissionDenied`'s `"subtype", "permission_denied"`, rather than echoing a value read off the line.
- **[Network & I/O]** Size is bounded twice: the line arrives already capped by `defaultMaxParseBuf`, and within it `maxBannerText` cuts `content` at 4 KiB while `maxBannerLevel` empties an over-long `level`, both at construction so an oversized value never enters the event stream. Rate needed checking rather than inheriting, and it holds: one event per line and an O(1) length test each, and a stream looping the line produces frames `turnMarkFor` answers `turnMarkNone` for — its opener set is a whitelist with a `turnMarkNone` default and `Banner` is not in it, pinned by `TestTurnMarkFor_TotalOverEveryVariant` — so every one is droppable at the fan-in, with retention bounded by `eventring.MaxEventsPerConversation`. **No per-turn count bound is owed**, unlike `maxTurnDenials`: that constant exists because the denial arm retains ids in `Parser.deniedThisTurn`, and this arm retains nothing, so there is no parser state a flood could grow.
- **[File operations]** Not applicable, and the reason is that the only file this ticket touches is the committed fixture, read in a test through `os.ReadFile` at a path composed from two package constants. No caller-supplied value reaches a path, nothing is written, no mode or symlink question arises.
- **[Subprocess / external command execution]** Nothing is spawned and no decoded value reaches `exec.Command`, a shell or an environment variable. Worth stating rather than skipping, because `content` **is** subprocess output and the tempting wrong move on a hook-feedback path is to feed the reason back somewhere.
- **[Cryptographic primitives]** Not applicable: no randomness, no key material, and no comparison against a secret. The `strings.Contains` in the capture reader's leak sweep compares committed fixture bytes inside a test, so constant-time comparison is not owed.
- **[Concurrency]** No lock is taken, no goroutine is spawned, and the arm reads and writes no `Parser` field — the same statelessness `emitCompactionBoundary` argues for itself. There is no lock order to document, no check-then-mutate on shared state, and no partial state to recover if the process is signalled mid-line.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model threat 1 (`claude`-authored content) lands here in the outward direction. The daemon's share is the bound, which this ticket enforces; control-character and terminal-escape stripping at the render boundary is explicitly the **client's**, already stated in § `banner`, and this ticket does not change that partition. `StopsTurn` staying a report and never an actuator is the constraint most specific to this frame, and no code here reads it — a daemon that keyed a teardown or a retry suppression on it would hand `claude` a self-service turn abort.
- **[Threat model alignment — the gate]** The empty-`content` gate was re-examined for whether it can silently restore the defect this ticket fixes, since a gate that drops a refusal is exactly #2232's stated hazard. It cannot: `claude` composes the wrapper prose itself (`"UserPromptSubmit operation blocked by hook:"`, the hook's path, then the original prompt), so a hook that refuses with an empty reason still yields non-empty `content`. The gate's reachable case is a line with no text at all, which carries no refusal to lose.
- **[Out of scope]** Client-side rendering hardening — escape stripping, inert-text treatment, attribution chrome — belongs to the client and is named as the client's in § `banner`. Mapping `system/notification` onto the same variant is #2258.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-10

## Revisions

**2026-09-10 — the capture reader also pins the line's key set.** The Testing strategy above listed the reader's assertions and did not include one. `informationalPinnedKeys` and `TestRealClaudeInformationalCaptureKeysArePinned` were added because without them the decode target's "exactly what the capture shows and nothing invented" claim — `systemTaskStartedLine`'s standing rule, restated at `systemInformationalLine` — is a sentence no test can falsify. A claude release that adds a key now reddens one level below the replay, so the field set is re-decided by a person instead of the daemon quietly reading three fields out of a line that changed shape. It follows `taskNotificationPinnedKeys`' shape, with the key set re-derived from the line's own bytes rather than read off the record's `keys` label.

**2026-09-10 — the ticket overran the 800-line ceiling, and the overage is stated rather than resolved.** The pre-commit count in § A4 estimated 773 lines of total written work. The actual is **1021**: 183 for this spec and 838 insertions across the implementation, against the refiner's estimate of ~790 and the nearest analogue's 834. The five other boundaries held with room — 4 production files, no new exported types, no consumer call sites, 4 acceptance criteria, 4 reject branches — so the overage is entirely line count, and it is concentrated where this repo's style puts it: `internal/streamsup/parser.go` grows 217 lines of which the arm's and the two constants' docs are the great majority, and the two test files carry their rationale at the same density as the readers they follow.

**It was not split, and both reasons are recorded.** Split depth is exhausted — this ticket's parent is #2257 and its grandparent #2197 — so the ceiling's remedy was barred before the count was taken. Independently, every candidate seam fails the floor rule. The obvious one cuts the mapping from its proof and its doc repairs, and the resulting second slice has no deliverable anything outside the family consumes: the capture reader exists to falsify the first slice's literals, and the repairs are consequences of it landing. The floor outranks the ceiling exactly here — a ticket that cannot be verified on its own is a cost no resume recovers, where an overrun costs at most a continuation leg, and this run used neither. `needs-human:sizing` carries the judgement to the board. The Doc repairs section named one further attribution, `turnevent.Banner.Text`'s reference to the ticket whose constant enforces the 4 KiB bound. Its wire-side twin, `protocol.BannerPayload.Text`, carries the same statement in the same words, so repairing one and leaving the other would have put a deliberately paired doc out of step — the worse outcome of the two. Both now name this ticket and `maxBannerText`. No other `#2257` reference is touched: the remaining ones say the producer owns the cap, which stays true, and re-attributing them would widen the diff for nothing.
