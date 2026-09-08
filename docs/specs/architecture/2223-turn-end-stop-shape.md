# #2223 — carry the result line's stop shape on `turn_end`

Ticket: streamsup: carry the result line's stop shape on turn_end — subtype, is_error, terminal_reason.

## Files read

- `internal/streamsup/parser.go` → `streamLine`, `resultLine`, `resultModelUsage`, `decodeModelWindows`, `resultTurnEndReason`, `truncateField`, `maxTaskFieldID`, `maxModelWindowID`, `emitModelAnnounced`, `emitConversationReset` — the `result` arm this ticket extends, the second-unmarshal pattern to copy, the cap doctrine, and the two precedents for how an overflowing claude string is answered (truncate-and-report vs drop).
- `internal/turnevent/event.go` → `TurnEnd`, `ModelWindow` — the variant that gains the fields, and the doc paragraph asserting that nothing on it reaches the wire, which this ticket makes false and must rewrite.
- `internal/protocol/interactive.go` → `TurnEndPayload`, `ToolResultPayload` — the wire shape, and the sibling whose doc states this file's no-`omitempty` rule.
- `internal/turnbridge/outbound.go` → `MapEvent` (`turnevent.TurnEnd` arm) — the field-by-field build that is the only path from the variant to the envelope.
- `internal/agentrun/streamjson/emitter.go` → `wireFields`, `ExitReason`, `trailer` — pyry's own spelling of this taxonomy (`success`/`completed`, `error_max_turns`/`max_turns`), reused rather than re-spelled.
- `internal/e2e/realclaude/budget_test.go` → `TestRealClaude_MaxTurnsHonored` — the live max-turns provocation; read to decide whether a new capture is needed (it is not, see Context).
- `internal/agentrun/streamrunner/args.go`, `runner.go` → `BuildClaudeArgs`, `Run` — the return-value contract stating claude's own trailer wins over the exit status, i.e. which bytes are claude's.
- `internal/streamsup/result_model_window_test.go` → `capturedResultLine`, `capturedModelUsage`, `parseOneLine`, `turnEndFrom`, `resultLineWith` — the nearest reader to copy, including its vacuity-guard shape.
- `internal/streamsup/initialize_capture_test.go` → `initCaptureDir` — the one capture directory; no second one is created.
- `internal/streamsup/tool_progress_capture_test.go` → `capturedToolProgressLines` — the "every failure is `t.Fatalf`, never a skip" rule for a committed capture, and the version-pin check.
- `internal/protocol/interactive_test.go` → `TestTurnEndPayload_RoundTrip`, `roundTripEnvelope` — re-marshals the payload and compares to the raw fixture, so `testdata/turn_end.json` must gain the new keys in declaration order.
- `internal/streamsup/parser_test.go` → `TestStreamLine_StaysSegmentationOnly` — the reflection sweep that forbids widening `streamLine`.
- `docs/protocol-mobile.md` § `turn_end`, § Application-envelope size cap, § Security model — the section AC 5 extends and the envelope figure the cap arithmetic is measured against.
- `internal/e2e/realclaude/testdata/permission_mode_switch_v2.1.239_plan.json` — see Context.

## Context

Every turn ends the same way on a client: `resultTurnEndReason` collapses claude's five result subtypes into two wire values, so a turn that hit `--max-turns`, exhausted a budget or overflowed its context reaches a phone as a clean `turn_end{stop_reason:"end_turn"}`. This ticket carries the stop shape claude actually sent — the subtype, the error flag, the terminal reason — beside the unchanged `stop_reason`.

**AC 4 needs no new live capture, and this is the single largest departure from the ticket body.** The body's Context 4 proposes extending `TestRealClaude_MaxTurnsHonored` to write one. Measured 2026-09-08 against every file under `internal/e2e/realclaude/testdata/`: one committed capture already holds a claude-authored `error_max_turns` result line —
`permission_mode_switch_v2.1.239_plan.json`, whose `argv` is `claude --input-format stream-json --output-format stream-json --verbose --model sonnet --max-turns 4` with `exit_code: 1`. Those are claude's own bytes by the body's own Context 5 test: the probe drives `claude` directly, so there is no `pyry agent-run`, no `internal/agentrun/streamjson` emitter and no synthesised trailer anywhere on that path. The line carries `subtype: "error_max_turns"`, `terminal_reason: "max_turns"`, `is_error: true` and `errors: ["Reached maximum number of turns (4)"]`. The same file's FIRST result line is the clean arm — `success` / `completed` / `is_error:false` — so one committed capture pins both ends of the taxonomy.

Three consequences, and they are why this plan is smaller than the estimate line:

- No capture-writing probe is built. Building one would be a defence for a failure mode that has not been observed here — the artifact AC 4 asks for exists and is committed.
- The ordering hazard disappears. A capture written by the dispatcher's live gate lands *after* the verifier's `make check`, so a reader that fatals on absence would redden the gate that runs first; #2089 answered that with a temporary `fs.ErrNotExist` skip deleted in a later leg. Reading an already-committed capture lets the reader fatal from the first commit, which is what AC 4 asks for.
- The ticket's `needs-real-claude` label stays earned but no longer load-bearing: the gate proves `TestRealClaude_MaxTurnsHonored` still provokes the same shape upstream. Nothing in this ticket's ACs waits on it.

The ticket's estimate line is ~1000 lines over 4 production files and declares a deliberate ~200-line overage of the 800-line ceiling, arguing the only seam (retain, then publish) is forbidden by the one-consumer floor. That floor reasoning is correct and this plan does not split. The estimate itself is high for this design: dropping the capture harness and bounding both strings with one constant puts the written total at roughly 580 lines of code plus this spec, inside the ceiling. No overage is claimed.

No ADR is warranted. This extends an existing wire frame under conventions already recorded at `maxTaskFieldID` and `maxModelWindowID`.

## Design

### Producer — `internal/streamsup/parser.go`

`streamLine` does not widen; `TestStreamLine_StaysSegmentationOnly` enforces that and the boundary it protects is exactly the one this ticket must not cross. The two new keys are read in a SECOND, independent unmarshal off the raw line bytes, copying `decodeModelWindows`' shape.

A new decode target, sibling to `resultLine` rather than a widening of it:

```go
type resultStopLine struct {
    IsError        bool   `json:"is_error"`
    TerminalReason string `json:"terminal_reason"`
}
```

**A separate target rather than two more fields on `resultLine`, and the reason is failure isolation.** `resultLine` exists to decode `modelUsage`, a map whose value shape claude controls. A hostile `modelUsage` — a number, an array, an object of numbers — fails that unmarshal, which is the property `decodeModelWindows`' doc rests on. Folding the stop shape into the same struct would make a hostile `modelUsage` ALSO suppress `terminal_reason`, i.e. let one field claude controls silently erase another. Two targets fail independently.

`IsError` is a plain `bool`, not `*bool`: absent, `null` and `false` are one reading, and nothing acts differently on the three. `systemThinkingTokensLine` makes the same argument for `int` over `*int`.

Two functions, both pure — no receiver, no parser state, nothing logged on any path (`decodeModelWindows`' rule, and for its sharpest reason: `encoding/json` quotes the offending input into its error text, so logging the decode error would route claude's bytes into the daemon log through a channel no per-attribute check sees):

- `decodeStopShape(line []byte) (isError bool, terminalReason string)` — unmarshals into `resultStopLine`, returns `(false, "")` on any decode failure, and returns the terminal reason already bounded.
- `boundStopField(s string) string` — returns `s`, or `""` when it exceeds `maxTurnEndStopField`.

**Overflow DROPS rather than truncates, so `truncateField` is deliberately not called here.** This is `maxModelWindowID`'s departure, applied for its stated reason rather than by resemblance: that cap drops because `#2102` JOINS on the id, and a cut key matches nothing while an absent entry is visible. `outcome` and `terminal_reason` are the same kind of value — open-set tokens a client MATCHES against a known list, never free text it displays. A 256-byte cut token matches no known token, so it is indistinguishable from a token the client has never heard of, which is a state the client must already handle because the set is open. Carrying `""` says the same thing and invents nothing.

The consequence is that **no truncation report is owed and none is added**. `ModelAnnounced.Truncated` exists because that field is DISPLAYED and a mangled value there is worth flagging; there is no `TruncatedFields` on this shape for `maxModelWindowID`'s reason, and no counter either — unlike `ModelWindows`, a dropped scalar is directly observable as the empty value the wire documents.

One new constant:

```go
const maxTurnEndStopField = 256
```

It bounds BOTH strings, on `maxTaskFieldID`'s precedent (one constant over `TaskID`, `ToolCallID` and `TaskType`, because they are one shape). Both are short claude-authored identifier tokens off one line: the longest documented spelling is `error_max_structured_output_retries` at 35 bytes and `structured_output_retry_exhausted` at 33, so 256 is roughly 7x the observed maximum — room for a token claude has not shipped, a hard cut on anything that has stopped being a token. Envelope arithmetic in `maxTaskDescription`'s style: worst case one `turn_end` carries `2 * 256 = 512` bytes of claude-derived text, 0.8% of the 65519-byte v2 application-envelope cap. Applied at CONSTRUCTION, so an oversized value never enters the event stream, the push queue, or any log.

The `result` arm gains two statements and three fields on the emit. `resultTurnEndReason(sl.Subtype)` is called on the UNBOUNDED subtype and is untouched, which is what keeps AC 3 structural: the bound applies to the published `Outcome` only and cannot move `stop_reason` for any input.

### Model — `internal/turnevent/event.go`

`TurnEnd` gains `Outcome string`, `IsError bool`, `TerminalReason string`.

The variant's doc currently asserts "NONE OF IT REACHES THE WIRE, by construction rather than by omission", scoped to the `#2101` window fields. That paragraph is rewritten to say what is now true: the window fields still do not reach the wire, and these three do — so their caps DO owe an envelope percentage, which `maxTurnEndStopField` carries.

Each field documents that the value is claude-authored, VERBATIM (no lowercasing, no mapping, no lookup against a published token list), bounded but NOT sanitized, and that the empty value means absent-or-over-cap. `Outcome` names its relationship to `Reason`: both derive from the same subtype, `Reason` is the daemon's two-value classification and `Outcome` is claude's token, and the two are never reconciled.

### Wire — `internal/protocol/interactive.go`

```go
type TurnEndPayload struct {
    ConversationID string `json:"conversation_id"`
    TurnID         string `json:"turn_id"`
    StopReason     string `json:"stop_reason"`
    Outcome        string `json:"outcome"`
    IsError        bool   `json:"is_error"`
    TerminalReason string `json:"terminal_reason"`
}
```

No `omitempty`, per this file's rule as stated at `ToolResultPayload`: absence and the empty value mean the same thing, and always emitting the key keeps the fixture pinning the full shape. That is also exactly AC 5's "an absent field decodes to the empty value". The doc warns that `stop_reason` and `outcome` are two different fields and that neither is claude's own `stop_reason` key, which this daemon does not forward.

Appended rather than interleaved: `roundTripEnvelope` re-marshals and compares to the raw fixture, so declaration order is the wire's key order, and `testdata/turn_end.json` gains the three keys in that order.

### Bridge — `internal/turnbridge/outbound.go`

`MapEvent`'s `turnevent.TurnEnd` arm passes the three fields straight through. No cap here: `maxTurnEndStopField` is applied at construction in the producer, and a second bound would be a number to keep in step — `ToolUpdate`'s `ResultDetail` arm makes this argument in place and is the precedent.

### Data flow

`claude result line` → `streamLine` (segmentation; subtype only) → `decodeStopShape` (second unmarshal; is_error + terminal_reason) → `boundStopField` ×2 → `turnevent.TurnEnd` → `MapEvent` → `protocol.TurnEndPayload` → envelope → phone.

## Concurrency model

None introduced. Both new functions are pure functions of the line bytes with no receiver and no shared state; the arm that calls them already runs on the parser's single `Write` goroutine, and no goroutine, channel or lock is added. Shutdown is unaffected.

## Error handling

- Undecodable `result` line at the top level — unchanged: `emitUnrecognized` on `UnrecognizedUndecodable`, and neither new function is reached.
- `is_error` or `terminal_reason` of a wrong JSON type — the second unmarshal fails; both come back as their zero values and the turn boundary is untouched. Nothing is logged.
- Either string past `maxTurnEndStopField` — carried as `""`. Not an error, not reported, documented on the wire as the absent reading.
- A subtype claude has not shipped — carried verbatim (bounded). Open set by construction; no allowlist, no rejection.
- Nothing on this path returns an error or panics; every failure is a value.

## Testing strategy

`internal/streamsup/result_stop_shape_test.go` (new), reusing `parseOneLine` and `turnEndFrom` from `result_model_window_test.go`:

- **AC 1** — table over five synthetic lines: `error_max_turns`, `error_max_budget_usd`, `error_max_structured_output_retries`, `success` with `is_error:true` + `terminal_reason:"prompt_too_long"`, and a clean `success`. Each asserts all three carried fields plus `Reason`, so all five are mutually distinguishable.
- **AC 2** — an over-long `terminal_reason` and an over-long subtype, each driven through the decode, asserting the field comes back empty; plus an exactly-at-cap row proving the boundary is not off by one, and an assertion that the over-long subtype did not move `Reason`.
- **AC 3** — a table over every subtype in the ticket body plus the unknown and empty cases, asserting `resultTurnEndReason`'s output is unchanged; and a `turnbridge` row asserting `StopReason` is still `string(e.Reason)`.
- **AC 4** — capture pin against `permission_mode_switch_v2.1.239_plan.json`, read through a reader local to this file (no path parameter, `t.Fatalf` on absence, on a decode failure, and on finding no `error_max_turns` result line — the last is the vacuity guard: without it a capture that lost the arm would pass by iterating nothing). Also pins the clean arm off the same file's first result line.
- **Non-vacuity** — the AC 1 arms use DISTINCT `terminal_reason` values per row, so a mapping that returned a constant, or crossed two fields, reddens.

`internal/turnbridge/outbound_test.go` — one row proving all three reach the payload, and one proving an empty-valued `TurnEnd` still produces today's payload for the three original keys.

`internal/protocol/interactive_test.go` + `testdata/turn_end.json` — the fixture gains the keys; the round trip re-pins the full shape.

`internal/streamsup/parser_test.go` — existing rows that compare a whole `TurnEnd` literal now see a populated `Outcome`; each gains the field. Expected churn, not a design change.

AC 3's second half (the fake-daemon e2e suite stays green) is covered by the dispatcher's `make check`; the fake harness emits no `terminal_reason`, so every new field defaults to empty and no fixture there moves.

## Open questions

1. Does any `cmd/pyry` hold/snapshot path reconstruct a `TurnEnd` from stored state, which would then carry empty stop fields on replay? Reading of `interactive_turn_v2.go` and `session_model_window_hold.go` says the holds store windows, not whole events, and the two `TurnEnd` arms switch on type only — to be confirmed by grep in Phase B before the producer edit.
2. Does `permission_mode_switch_v2.1.239_plan.json`'s max-turns line carry a `result` key? The census shows 18 keys with `errors` present and no `result`/`stop_reason:"end_turn"`; if the shape differs from the assumption the capture test asserts on, the test asserts what the file holds, not what this plan predicted.

Each is resolved in Phase B; anything that moves the design is recorded under `## Revisions`.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] MUST FIX (addressed in the plan above, before commit).** The boundary is claude's stdout → daemon state, and this ticket moves one field ACROSS it that was previously inside. `outcome` is sourced from `streamLine.Subtype` — the SEGMENTATION struct — not from the isolated second decode. Verified 2026-09-08 that `sl.Subtype` has exactly two readers today, `resultTurnEndReason` and `emitSystemSubtype`, and both only compare it against literals in a `switch`; it reaches no event, no log and no wire field. So publishing it creates the only escape path there is, and the bound must sit at the publish site. The design applies `boundStopField` at the emit and nowhere else, and the plan's Design section states that `resultTurnEndReason` is deliberately called on the UNBOUNDED value so the bound cannot move `stop_reason`. Were the bound instead applied by mutating `sl.Subtype`, the classification would silently change for an over-long input — that is the version this finding rejected.
- **[Trust boundaries] No further findings.** The stop shape's own boundary is a single named function, `decodeStopShape`, over a target declaring exactly two keys. Absence from the DECODE TARGET is the strong half: claude's `result` line carries 18–22 keys and this reads two, so `result` (claude's free-text answer), `session_id`, `uuid`, `errors` and `stop_reason` cannot leak through this path even by a later careless widening of a shared struct — which is the concrete reason the plan refuses to fold these keys into `resultLine`. Downstream holders know the data is untrusted because each field's doc says so, matching `ModelWindow.ModelID`'s SECURITY paragraph.
- **[Threat model alignment] SHOULD FIX.** These are the `turn_end` frame's FIRST claude-authored strings, so `docs/protocol-mobile.md` § Security model threat 1 lands in the *outward* direction — the framing `attachment_offered`'s `filename` already uses for the same situation. The daemon owes the BOUND; the render boundary owing sanitization is the CLIENT's, and the daemon does no control-character or terminal-escape stripping on this path. Phase B must say this outright in § `turn_end`'s prose and in both field docs, not merely imply it by listing the fields; AC 5 as written does not require it and would pass without it. Verified there is no daemon-side render surface to worry about: no `cmd/pyry`, `internal/relay` or `internal/control` non-test path reads `StopReason` or would print these fields to a TTY.
- **[Error messages, logs, telemetry] No findings — by construction rather than by care.** Neither new function logs on any path, and the decode error in particular is DISCARDED rather than wrapped, for `decodeModelWindows`' stated reason: `encoding/json` quotes the offending input bytes into its error text, so `"err", err` would route claude's own token into the daemon log through a channel no per-attribute check can see. The house idiom (CLAUDE.md: wrap errors with context) points the other way, which is why the plan states it rather than assuming it. The `result` arm logs nothing today and gains no log call.
- **[Network & I/O] No findings.** Three bounds compose. The line is capped before it is ever decoded by the parser's own `maxBuf`; each published string is capped at `maxTurnEndStopField` at CONSTRUCTION, so an oversized value never enters the event stream, the push queue or a log; and the resulting worst case is 512 bytes of claude-derived text, 0.8% of the 65519-byte v2 application-envelope cap. No rate bound is owed and none is added: the `result` line IS the turn boundary, so this fires once per turn — `maxModelWindowID`'s situation verbatim.
- **[Concurrency] No findings.** Nothing is added that has state. Both functions are pure functions of the line bytes, called from the parser's existing single `Write` goroutine; no goroutine, channel, lock or shared accumulator is introduced, so there is no lock order to document, no check-then-mutate, and no shutdown path to change.
- **[File operations] No findings on any production path** — this ticket opens no file. Test-side, the capture reader takes NO path parameter and resolves a package constant, which is `capturedToolProgressLines`' rule and exists so no caller can point a provenance-checked reader at a hand-written payload file.
- **[Tokens, secrets, credentials] Not applicable.** No credential is created, stored, compared or transported. Worth naming rather than skipping, because the field this decode does NOT declare is the one that would matter: claude's `result` key carries the model's free-text answer for the turn, and a widened target would put it on a frame that until now carried none of claude's text at all.
- **[Subprocess / external command execution] Not applicable** — no `exec.Command`, no argv construction, no environment handling. The subprocess whose output this parses is spawned elsewhere and unchanged.
- **[Cryptographic primitives] Not applicable** — no randomness, no hashing, no comparison against a secret. The frame is sealed by the existing v2 AEAD envelope, which this change does not touch.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-08

## Revisions

### 2026-09-08 — both Open Questions resolved; the design did not move

1. **No path reconstructs a `TurnEnd`.** `git grep 'turnevent.TurnEnd{'` over non-test Go returns exactly one construction site, streamsup's `result` arm; the four `internal/e2e/internal/fakeclaude` hits are prose in comments. `cmd/pyry`'s two `turnevent.TurnEnd` arms switch on the type and read `Reason` only, and `session_model_window_hold` stores windows rather than whole events. So no hold or snapshot can replay a `TurnEnd` carrying empty stop fields, and the producer needed no defence against one.
2. **The captured budget-stopped line's shape is as the census read it** — 18 keys, `errors: ["Reached maximum number of turns (4)"]` present, no `result` key, and `stop_reason: "tool_use"` (claude's own key, which this daemon does not forward). The clean line beside it carries 22 keys including `result`. `TestParser_ResultStopShape_CapturePin` asserts only the three fields this ticket reads, so neither shape difference reaches an assertion.

Three additions beyond the plan, none of them a design change:

- A subtest asserting no over-long claude byte reaches a log line, covering the clause of AC 2 the plan had argued in prose only.
- A `turnbridge` row asserting the window pair still does **not** reach the payload. The plan's Design says publication is a per-field decision; now that the variant has publishable fields, that row is what keeps the unpublished half honest.
- A hostile-shape table (`is_error` as a string, `terminal_reason` as a number, object, array, and null) proving each leaves the turn boundary and the outcome standing. This is the failure-isolation claim the two-decode-target choice rests on, measured rather than asserted.

One pre-existing condition left alone: `internal/streamsup/helper_test.go` is unformatted on `main` and is not in this diff.

### 2026-09-08 — the size estimate above was low; correcting it against the measured diff

The Context section closes with "the written total at roughly 580 lines of code plus this spec … No overage is claimed." **That was wrong and the measurement is 815 insertions on the feature commit plus a 166-line spec, about 980 lines** — over the 800-line ceiling by ~180 and within 2% of the refiner's own ~1000 estimate, which this plan had judged high.

The error was not scope: the four production files, five acceptance criteria, zero new exported types and zero forced call-site updates are all exactly as planned, and dropping the capture harness did save what it was expected to save. It was **doc-comment density**. This package argues each cap, each decode target and each dropped-vs-truncated choice in prose at the point of the decision, and that convention turns a 6-line constant into a 40-line one; `parser.go` grew 159 lines for roughly 20 of statements, and the test file's 423 lines carry their non-vacuity arguments the same way. Estimating the statements and not the prose is what put the figure at 580.

Nothing about the outcome changes. The ceiling and the one-consumer floor disagreed before a line was written — the retain half's only consumer is the publish half — and § A1's rule is that the floor wins, the overage is stated, and the ticket is built. The refiner reached the same conclusion independently. This entry exists so the number in the plan is not left contradicting the diff beside it; **the lesson for the next estimate on this package is to size the prose, not the statements.**

