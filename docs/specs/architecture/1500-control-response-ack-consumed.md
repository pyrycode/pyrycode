# Spec — #1500 streamsup: consume the interrupt `control_response` ack instead of surfacing it as `unrecognized_message`

**Size:** S (confirmed, not overridden). Two production files (`internal/streamsup/parser.go` — one `case` arm; `internal/e2e/internal/fakeclaude/main.go` — one writer plus a call site), zero new files, zero new exported symbols, zero consumer call sites to migrate, one new branch. Projected total written work ≈ 220 lines including tests, comments and the doc sweep. The nearest analogue, #1411, shipped 596 insertions across 5 files as one `size:s`; this is roughly a third of that, because its hermetic tier extends an existing e2e instead of adding a 347-line one.

**File-overlap check: clean.** `git fetch origin --prune` then a `git diff --name-only origin/main...origin/feature/<N>` sweep across every remote feature branch found zero in-flight branch touching `parser.go`, `parser_test.go`, the fake, `relay_v2_stream_interrupt_test.go`, either realclaude interrupt/liveness file, or `streamsup-package.md`. No `blockedBy` needed.

**`security-sensitive`** — the security review is at the end of this document, run before commit per `architect/security-review.md`.

---

## The ack's field nesting: the ticket's proposed shape is wrong, and a committed capture says so

AC1 asks for the shape `{"type":"control_response","request_id":"<id>","response":{"subtype":"success"}}`, and instructs confirming it against the `#1120` spec before pinning. That confirmation was run, and it fails — but not against `#1120`, whose prose is shorthand (`control_response{subtype:success,request_id:<id>}`) that commits to no nesting at all and therefore cannot confirm or refute anything. It fails against a **verbatim live capture of the real thing**, committed at `internal/e2e/realclaude/testdata/set_permission_mode_v2.1.220_revoke.json` and transcribed into `docs/knowledge/features/set-permission-mode-inband-probe.md` § "The `control_response` received, verbatim":

```
{"type":"control_response","response":{"subtype":"success","request_id":"set-permission-mode-revoke","response":{"mode":"default"}}}
{"type":"control_response","response":{"subtype":"error","request_id":"set-permission-mode-enable","error":"Cannot set permission mode to bypassPermissions because …"}}
```

**Both `subtype` and `request_id` live INSIDE `response`. Neither is top-level.** The only top-level key besides `type` is `response` itself. The asymmetry is easy to trip over and is the reason the AC's guess reads plausibly: on the **request** side `request_id` *is* top-level — `marshalInterruptEnvelope` writes it there, `interruptControlRequestLine` mirrors it there, and the same capture's `control_request` confirms it. The response inverts that. Pin the response as the capture shows it, not as the request's mirror image.

Two caveats stated rather than buried, because a fixture that overclaims its provenance is exactly what this ticket is cleaning up after:

- The capture is a `set_permission_mode` ack, **not an interrupt ack**. What it establishes is the `control_response` **envelope** — `type` plus a `response` object carrying `subtype` and `request_id` — which is the control channel's, not any one request's. That envelope is all this ticket's arm and fixtures depend on.
- The **inner `response` payload** (`{"mode":"default"}` in the capture) is request-specific and **unmeasured for `interrupt`**. Do not invent one. The fixture omits the inner payload entirely; an ack carrying one and an ack carrying none are handled identically here, because nothing below the top-level `type` is read at all.

### This finding also flips the shape argument the ticket set up

Technical Notes offers, as the fact distinguishing `control_response` from `rate_limit_event`, that "`control_response` carries a `subtype`, unlike `rate_limit_event`". That rests on the guessed top-level nesting and is **false against the capture**. `streamLine` declares `Subtype` as a *top-level* `json:"subtype"`; a real `control_response` has no top-level `subtype`, so `sl.Subtype` decodes to `""`.

Which is precisely the structural fact `#1404` used to move `rate_limit_event` off `ignoredLineTypes` and give it its own arm. The analogy is not weaker than the ticket assumed — it is exact.

---

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_search` / `codegraph_node`. Where an entry names a markdown section it is because the region genuinely is not a symbol.

| Where | Symbol / section | What to extract |
|---|---|---|
| `internal/streamsup/parser.go` | `consumeLine` | The switch this ticket adds one arm to. Read the `rate_limit_event` arm's comment in full — it is the recorded reasoning this spec's shape decision follows. |
| `internal/streamsup/parser.go` | `ignoredLineTypes` | The map, and the 2026-07-27 census that is its stated provenance. Note it says *top-level types only* and *measured, not guessed* — both bear on why `control_response` must not join it. |
| `internal/streamsup/parser.go` | `streamLine` | Three fields. `Subtype` is **top-level**; a `control_response` leaves it empty. |
| `internal/streamsup/parser.go` | `emitRateLimit`, `emitSystemSubtype` | The two silent-consumption idioms: a `Debug` record naming a reason, and the bool-returning "did I consume it?" dispatch. The new arm follows the first and deliberately not the second. |
| `internal/streamsup/parser.go` | `emitUnrecognized` | The lane this ticket makes unreachable for one type. |
| `internal/streamsup/parser_test.go` | `TestParser_IgnoredLineTypesIsTheMeasuredSet` | AC2's pin. Under the shape below it is **untouched and green** — verify that, do not edit it. |
| `internal/streamsup/parser_test.go` | `TestParser_IgnoredLineTypesStaySilent` | The table shape to mirror for the new silence rows, and its `collectEvents`-style harness. |
| `internal/e2e/internal/fakeclaude/main.go` | `runStreamJSON` | The rider dispatch. Read the `rateLimitStatus` rider's own comment on writing *before* the reply — AC3(a) asks for the same argument, and it is already written down here. |
| `internal/e2e/internal/fakeclaude/main.go` | `interruptControlRequest`, `inControlRequest`, `writeInterruptedResult`, `writeJSONLine` | The interrupt rider's whole surface. `inControlRequest` does **not** decode `request_id` yet. |
| `internal/e2e/internal/fakeclaude/main.go` | `writeRateLimitEvent` | The `#1411` writer this one mirrors — including its doc's discipline of naming the capture a fixture is transcribed from. |
| `internal/e2e/internal/fakeclaude/stream_detect_test.go` | `parseEmitted`, `interruptControlRequestLine` | `parseEmitted` runs the fake's stdout through the **real** `streamsup.Parser`. This is load-bearing for § "The free discriminator" below. |
| `internal/e2e/internal/fakeclaude/stream_detect_test.go` | `TestRunStreamJSON_InterruptMode_InterruptEndsTurnCancelled`, `TestRunStreamJSON_InterruptMode_InFlightThenInterrupt` | Both assert an **exact event count**. Both go red on the rider alone and green again on the parser arm, unmodified. |
| `internal/e2e/internal/fakeclaude/stream_detect_test.go` | `TestRunStreamJSON_RateLimitRider` | The emitted-line assertion shape AC3(b) needs. |
| `internal/e2e/relay_v2_stream_interrupt_test.go` | `TestRelayV2_StreamInterruptStopsRunningTurn`, and its `nextEnv` closure | AC3(a)'s host. `nextEnv` is per-test and serves all four drain loops. |
| `internal/e2e/realclaude/interactive_stream_interrupt_test.go` | `drainForCancelledTurnEnd` | AC4's host. Its non-target arm is the bare `continue` that swallows the frame today. |
| `internal/e2e/realclaude/interactive_stream_liveness_test.go` | `drainForCompletedTurn` | Its `TypeUnrecognizedMessage` arm is the alarm shape AC4 mirrors, and its `TypeRateLimited` arm is the model for naming a legitimate cause beside the bug reading. |
| `docs/knowledge/features/set-permission-mode-inband-probe.md` | § "The `control_response` received, verbatim" | The capture. The authority for every byte of the fixture. |
| `docs/knowledge/features/streamsup-package.md` | § "Two tiers, and the split is the whole design" | The `{"system": true}` claim AC5 checks. |
| `cmd/pyry/stream_turn_busy.go` | `turnMarkFor` | Its comment carries a `system` alone claim (AC5), and it classifies `Unrecognized` as `turnMarkNone` — see § "What a green does not prove". |

---

## Context

The daemon writes an interrupt `control_request` to its child's held-open stdin. claude acks ~40 ms later on **stdout** — the same stream the `Parser` consumes — and then ends the turn. The runner never reads the ack; that is documented and fine. But never-read is not never-parsed: the ack reaches `consumeLine`, matches no case, is not on `ignoredLineTypes`, and falls to `emitUnrecognized`, which `turnbridge` maps to an `unrecognized_message` frame on the phone.

So every interrupt fires the one frame whose entire value is that it means *claude started emitting something new*. Degrading that frame is the cost, and it is permanent: once it cries wolf on a routine user action, nobody reads it when it fires for real.

Both gates are green for structural reasons rather than luck — the fake never emits an ack, and the live interrupt gate's drain skips every non-`turn_end` envelope while the standing zero-unrecognized alarm lives in a *different* helper reached only on the health turn afterwards, by which point the frame is long gone. Closing both blind spots is why the two tiers are one concern.

---

## Design

### The shape decision: an own `case` arm, not an `ignoredLineTypes` member

Decided deliberately, on four independent grounds that all point the same way. The pin test is a consequence of the decision, not its cause.

1. **No top-level `subtype` to dispatch on.** Per the capture, a `control_response`'s `subtype` sits under `response`, so `sl.Subtype` is `""`. `emitSystemSubtype`'s dispatch — the only thing the map's drop branch does beyond logging — has nothing to match. This is verbatim `#1404`'s reason for `rate_limit_event`, whose census likewise measured it as carrying no subtype.

2. **The map's provenance would become a lie for one member.** `ignoredLineTypes` is documented, twice and emphatically, as *measured, not guessed* — the 2026-07-27 census, three turns on two models. That census never interrupted, so it never saw this line, and the string appears in no capture under `internal/e2e/realclaude/testdata/`. A member sourced from `#1120`'s spec rather than the census makes the map's own doc comment false at the point where its whole value lies. AC2 offers "moved deliberately with an in-test comment recording the divergent provenance" as an allowed branch; it is allowed, not preferable, and this avoids needing it.

3. **Unreachability by matching beats unreachability by membership** — `#1404`'s recorded phrasing, and it is the stronger guarantee for exactly the reason it says: a matched `case` cannot be weakened by an unrelated edit to a shared list.

4. **AC5 becomes verify-not-change.** Seven sites carry a live count or membership claim about the map — `ignoredLineTypes`' own comment (twice), the `sl.Type == "system"` guard's comment, `TestParser_IgnoredLineTypesIsTheMeasuredSet`, `TestParser_IgnoredLineTypesStaySilent`, `turnMarkFor`, `interactive_stream_unrecognized_test.go`'s header, `streamsup-package.md` (twice, including the doc index line), and `codebase/1404.md`. Under an own arm **every one stays literally true, untouched**. Under map membership every one is falsified and AC5 turns from a check into a nine-site rewrite — inside a ticket whose production change is one arm.

### The arm

In `consumeLine`'s main switch, beside `assistant` / `user` / `result` / `rate_limit_event`:

```go
case "control_response":
    // The daemon's own ack, consumed content-free. Signature + behaviour only —
    // one Debug record naming the type, then return. No event of any kind.
```

Behaviour, in full: log one `Debug` record naming the **type only** — never the line, never any nested field — and emit nothing. Two lines of code plus its comment.

**Why a `Debug` record rather than a bare `return`.** Every other silent consumption in this file leaves one (`emitRateLimit`'s three rungs, the ignored branch's drop). A solicited-ack consumption with no trace at all is the one shape where a future ack investigation has nowhere to start. It is not a new class of Debug producer — the ignored branch already emits one per `system` line on every turn, so the `#1318` stability guard's `level=DEBUG` grep is saturated on any exercised stream path either way, and this adds nothing to that.

**The comment must record**, briefly: that the daemon solicits this line itself (so consuming it is not tolerating a stranger); that the capture in `set-permission-mode-inband-probe.md` is the shape's authority and puts `subtype`/`request_id` under `response`; and that the arm keys on the **top-level `type` only**, so the ack is consumed whatever its `subtype` — including a NAK. Follow the file's house style of correcting in place rather than accreting; do **not** restate the ignored-list census here.

**The NAK, noted and not built** (Technical Notes asks for exactly this). A `subtype:"error"` ack — whose real shape the same capture records, carrying an `error` string — is consumed by this arm indistinguishably from a success, and the `Debug` record does not discriminate them. So the arm **does swallow a NAK silently**, in the sense that no observer can tell one from the other. Only `success` has ever been observed for an interrupt; no failure of this shape exists; per Evidence-Based Fix Selection no branch is built. Say so in the comment in one sentence so the next reader inherits the decision rather than the surprise. Making it visible would cost a decode target for the nested `response` object, which is building the branch — do not.

### Segmentation is unaffected

The switch still keys on top-level `type` only. A tool result whose text contains `control_response` is never a top-level `type` and cannot reach this arm — the identical argument `#1120` made for `error_during_execution`.

### The fake's rider

`writeInterruptAck(w io.Writer, requestID string) error` — one `writeJSONLine` of the envelope the capture records, with `subtype:"success"` and the caller's `requestID`, both nested under `response`, and no inner `response` payload (unmeasured for `interrupt`; see the caveats above). Its doc names the capture file, mirroring `writeRateLimitEvent`'s discipline.

The interrupt branch in `runStreamJSON` writes the ack **first**, then `writeInterruptedResult`, on the same writer — matching real claude's order (ack ~40 ms, then the `result`) and, more importantly, giving AC3(a) its causality: the `turn_end` reaching the phone proves the ack already went through the parser. No sleep, no poll, no ordering race. This is the argument the `rateLimitStatus` rider already makes for itself in its own comment; point at it rather than restating it.

**Echo the daemon's real `request_id`.** `inControlRequest` gains `RequestID string \`json:"request_id"\`` (top-level — the request side, per `marshalInterruptEnvelope`), and `interruptControlRequest` widens from `bool` to `(string, bool)`, keeping its "fails to decode or is not an interrupt → skip" contract. It has exactly one caller and no direct test, so the widen ripples nowhere. Cost is ~5 lines; the benefit is a fixture that does not lie about a field the capture shows claude echoing.

**No default-mode change.** The ack writes only inside the already-gated `honorInterrupt && interruptControlRequest(...)` branch, so the send / `new_session` / queue riders stay byte-identical and `TestRunStreamJSON_BogusRiderOffIsByteIdentical` and its rate-limit sibling are unaffected.

---

## Concurrency model

Unchanged. No goroutine, no channel, no lock is added or touched.

The `Parser`'s single-writer invariant holds as stated at its type: `os/exec` drives a non-`*os.File` `Config.Stdout` through one internal `io.Copy` goroutine, so `consumeLine` runs serially from that one goroutine. The new arm reads only its own local `sl` and calls `p.log.Debug`; it touches neither `buf` nor `thinkingSinceEmit`, so it introduces no cross-line state and no new reader of parser state.

**It must not reset `thinkingSinceEmit`.** That accumulator has exactly one boundary — the `result` arm — and the value of having one boundary is that there is only one place to keep correct. An ack is not a turn boundary. Leave it alone.

The fake's rider writes two lines back-to-back from `runStreamJSON`'s single read loop goroutine, as it already does for the bogus and rate-limit riders. Ordering on the wire is the write order.

---

## Error handling

| Condition | Result |
|---|---|
| `control_response`, `response.subtype: "success"` | Zero events. One `Debug` record naming the type. — AC1(a) |
| `control_response`, `response.subtype: "error"` (NAK) | **Identical**: zero events, same record. Deliberate; see § The NAK. |
| `control_response` with no `response` object, or an unrecognised inner payload | Identical. Nothing below top-level `type` is read, so there is no decode to fail. |
| A top-level type that is neither `control_response` nor on `ignoredLineTypes` | Unchanged — exactly one `Unrecognized{Kind: <type>}`. — AC1(b) |
| A `control_response` **inside** an assistant/user content block | Unchanged — block-level mapping; this arm is top-level only. |
| Fake: marshal or write failure in `writeInterruptAck` | Returned to `runStreamJSON`, which returns — the existing per-writer contract, unchanged. |

There is no new failure mode. The arm cannot fail: it performs no decode beyond the whole-line one that already ran, allocates nothing, and calls one logger method.

---

## Testing strategy

Stdlib `testing`, table-driven, `-race`. Scenarios below, not test bodies — write them in each file's existing idiom.

### Parser tier (AC1, AC2)

- **AC1(a) — the ack is silent.** Feed a `control_response` line in the captured shape (`response.subtype: "success"`, `response.request_id` set) and assert **zero** events of any kind. Add a row for a NAK (`response.subtype: "error"` with an `error` string), one for an ack with no `response` object at all, and one carrying an inner `response` payload — all zero. Mirror `TestParser_IgnoredLineTypesStaySilent`'s harness but keep the rows in their own test: this type is not on that list and a row there would assert a membership it does not have. (This is `#1404`'s recorded reason for moving its row out of that table; follow it.)
- **AC1(b) — the alarm is not widened, in the same run.** In the same table, a line of a type that is neither `control_response` nor known-ignored must still produce exactly one `Unrecognized` whose `Kind` is that type. Reverting the arm reddens (a) alone; dropping unknown types generally reddens (b) alone. Keeping both in one table is what makes that asymmetry legible.
- **Silence-with-a-reason.** Assert the `Debug` record names the type and that **no** record carries the line's bytes or the `request_id` — the content-free discipline `TestParser_IgnoredLineTypesStaySilent` and the harness-nudge drop test already assert for their sites.
- **AC2 — the pin.** `TestParser_IgnoredLineTypesIsTheMeasuredSet` stays **untouched and green**; `want` remains `map[string]bool{"system": true}`. Verify by running it, not by assuming. If it needs an edit, the shape decision above was not implemented.

### Hermetic tier (AC3)

**The free discriminator — read this before writing anything.** `parseEmitted` feeds the fake's stdout through the **real** `streamsup.Parser`, and two existing tests assert an exact event count: `TestRunStreamJSON_InterruptMode_InterruptEndsTurnCancelled` (`want 1`) and `TestRunStreamJSON_InterruptMode_InFlightThenInterrupt` (`want 2`). Adding the rider makes both count one event higher **on `main`** — the ack's `Unrecognized` — so both go **red**. The parser arm alone returns them to their existing expected counts, **green with no test-code edit**. Do not "fix" them by bumping the counts; that would delete the discriminator. Their combination is a stronger statement than a frame-absence check, because it pins the *total* event count rather than the absence of one type. If either needs its count changed to pass, something is wrong with the arm — stop and diagnose.

- **AC3(a) — the e2e zero, non-vacuously.** In `TestRelayV2_StreamInterruptStopsRunningTurn`, fail on any `unrecognized_message` envelope observed up to and including the `turn_end{cancelled}` the test already waits for. Put the check in the per-test `nextEnv` closure: it is one site, it covers all four drain loops (the bogus rider is off in this test, so the earlier windows must be clean too), and it strictly contains AC3(a)'s required window. The message must decode `UnrecognizedMessagePayload` and print `Site` / `MessageType` / `Raw`, as `drainForCompletedTurn`'s arm does, so a red run names the type rather than merely reporting a count. **The zero is non-vacuous because the ack is written before the `result`:** the `turn_end` this test already blocks on cannot arrive unless the ack has already been through the parser.
- **AC3(b) — the arrival control, on the emitted line.** A new test in `stream_detect_test.go`, modelled on `TestRunStreamJSON_RateLimitRider`: drive interrupt mode with `interruptControlRequestLine("<fixed-id>")`, then assert **on the emitted bytes** (not through `parseEmitted`, and not on any downstream absence) that exactly two lines were written; that line 0 decodes to `type == "control_response"` with `response.subtype == "success"` and `response.request_id == "<fixed-id>"` (the echo); and that line 1 is the `result{error_during_execution}`. The line **order** is part of the assertion — AC3(a)'s causality argument depends on it, so it must be pinned where it is made, not assumed. Deleting the rider's write reddens this while AC3(a) stays green; that asymmetry is why both rows exist.

### Live tier (AC4)

- Add a `TypeUnrecognizedMessage` arm to `drainForCancelledTurnEnd`, before the existing non-`turn_end` `continue`, in the shape `drainForCompletedTurn`'s alarm already uses: decode the payload, `t.Fatalf` naming `Site` / `MessageType` / `Raw`. No conversation filter — matching both existing alarms, and correct here because a parser gap is a property of the daemon, not of one conversation.
- The message must name **both** readings, as `drainForCompletedTurn`'s `TypeRateLimited` arm does for its own: (1) the daemon bug — an ack the daemon solicited is reaching the phone, i.e. the `control_response` arm regressed or never landed; (2) the legitimate cause — a claude release emitting a genuinely new message type mid-interrupt, which is the alarm working, and whose answer is to read the payload and decide on a mapping, not to widen the drain.
- **This row is red on `main` before the fix**, and that is the only place a *real* claude's ack is observed. The ordering that makes it reachable is claude's own: the ack precedes the `result`, and the drain returns on the first matching `turn_end`, so the frame arrives inside the window. Verify the red under `make preship` (or a targeted `-run` against the live suite) — **never `make check`, which does not compile this package**, and read the count of `=== RUN` lines rather than the exit code.

### What a green does not prove

`turnMarkFor` classifies `Unrecognized` as `turnMarkNone`, i.e. droppable at the fan-in. So a green live run means *no ack frame arrived*, which under capacity pressure is weaker than *no ack frame was produced*. This is fine for a fail-loud sentinel and is not worth engineering around — but it is why AC3(b) asserts on the emitted line rather than on a downstream absence, and nobody should read a green live run as independent proof the fake or the parser did anything. Note it in one sentence at the AC4 arm.

---

## Doc and comment sweep (AC5)

**Expected outcome: nothing needs changing.** Under the own-arm shape every claim below stays true. Verify each by reading it; do not assume, and do not "improve" one that is already correct.

| Site | Claim | Expected |
|---|---|---|
| `internal/streamsup/parser.go` → `ignoredLineTypes` | "down to ONE member", "stays top-level types only" | True, untouched |
| `internal/streamsup/parser.go` → `consumeLine`'s `sl.Type == "system"` guard comment | "while the list has one member" | True, untouched |
| `internal/streamsup/parser_test.go` → `TestParser_IgnoredLineTypesIsTheMeasuredSet` | "down to one member"; `want` = `{"system": true}` | True, untouched — AC2 |
| `internal/streamsup/parser_test.go` → `TestParser_IgnoredLineTypesStaySilent` | "now what its name says: `system` alone" | True, untouched |
| `cmd/pyry/stream_turn_busy.go` → `turnMarkFor` | "is down to `system` alone" | True, untouched |
| `internal/e2e/realclaude/interactive_stream_unrecognized_test.go` header | "`ignoredLineTypes`, which is down to `system` alone" | True, untouched |
| `internal/turnevent/event.go` → `Unrecognized` | "unreachable for the type by matching rather than by list membership" | True — and now describes two types |
| `docs/knowledge/features/streamsup-package.md` § two tiers | "the map is down to `{"system": true}`" | True, untouched |
| `docs/knowledge/features/streamsup-package.md` doc-index line for `codebase/1404.md` | same claim | True, untouched |

**Sweep method, not just these sites.** Grep for the numeral *and* word forms — `one` / `1` / `ONE` / `single` / `sole` / `only member` — near `ignoredLineTypes`, and for `{"system": true}` and `system` alone as literals. Two traps this repo has hit before: `git grep -E` silently drops `\b` (POSIX ERE has no word boundaries) and reads as clean absence — use `-F`, `-P`, or the Grep tool; and a word-form sweep for `one` misses `ONE`, so pass `-i`.

**One line does need adding, and it is the only doc change this ticket owes.** `docs/knowledge/features/streamsup-package.md`'s mapping table gains a `control_response` row — *consumed content-free, top-level type only, ack the daemon solicited*, pointing at the capture for the shape. It belongs beside the `rate_limit_event` row it is the sibling of.

**Do not touch** `docs/knowledge/codebase/1500.md` — the documentation phase owns it and writes it from this spec plus the merged diff. It is not a developer deliverable and no AC here asks for it.

---

## Open questions

- **The inner `response` payload of an interrupt ack is unmeasured.** The fixture omits it and the parser reads nothing below `type`, so nothing here depends on it. If a future ticket needs it, the measurement is a live interrupt with stdout captured — the `#1595` probe is the template. Not this ticket.
- **The `Debug` record's wording.** Prescribed as type-only and content-free; the exact message string is the developer's call within the file's idiom. Whether it deserves a named constant like `rateLimitDropMsg` — no: those exist because `emitRateLimit` has three rungs to discriminate between, and this arm has one outcome.
- **Whether `#1120`'s spec line should be corrected.** Its shorthand is not *wrong*, only silent on nesting, and it is a build artefact of a closed ticket rather than an evergreen doc — so it is left alone deliberately. The evergreen home for the shape is `set-permission-mode-inband-probe.md`, which already has it verbatim, and the new `streamsup-package.md` row points there.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX, and the boundary moves in the safe direction. `consumeLine` is *the* subprocess-stdout → daemon-state boundary, and this change makes one top-level type stop crossing it: the arm emits no event, so no bytes from a `control_response` reach `turnevent`, `turnbridge`, the relay, or a phone. Matching is on the top-level `type` only, so no nested field — attacker-influenced or otherwise — steers the decision, and a `control_response` appearing inside an assistant/user content block is unaffected (block-level mapping, different site). The trust decision stays where it already was, in one function, and the new arm is enumerated beside the others rather than scattered.
- **[Trust boundaries — the widening question]** Considered and rejected as a finding, but named because it is the one real risk in the design: the arm consumes **every** `control_response`, not only the ack for an interrupt the daemon actually sent. So a hostile or buggy child could suppress its own lines from the timeline by labelling them `control_response`. Not exploitable to any gain: the alternative for an unmapped type is an `unrecognized_message` diagnostic frame, so the "attack" is a child choosing not to raise a diagnostic about itself — which it can already achieve by emitting nothing at all. A child that can write arbitrary stdout is already inside the trust boundary and can simply not send content. Correlating against sent `request_id`s would close it, and Technical Notes puts that explicitly out of scope with no observed failure; the same call `#1120` made. **OUT OF SCOPE**, no future ticket named — file one if an ack-correlation failure is ever observed.
- **[Error messages, logs, telemetry]** No findings. The `Debug` record names the type only — never the line, never `request_id`, never the inner payload — matching the content-free discipline the ignored branch and `emitRateLimit` already hold and which the parser's existing tests assert. AC1's log assertions pin it. The fake's ack echoes a `request_id` that is a per-runner counter, not a secret. The two new test failure messages print `Unrecognized.Raw`, which is already truncated at construction to `maxUnrecognizedRaw` and already printed by `drainForCompletedTurn`'s existing arm — no new disclosure, and these are test binaries.
- **[Tokens, secrets, credentials]** Not applicable — no token, key, or credential is read, minted, stored, compared, or logged. The `request_id` is a per-`Runner` `atomic.Uint64`, documented as write-only and carrying no entropy claim; nothing in this ticket makes it security-relevant.
- **[Cryptographic primitives]** Not applicable — no RNG, no comparison against a secret, no key or nonce. The relay's Noise layer is untouched: this changes what is put *into* a frame stream, never how frames are sealed. The e2e assertions run inside the existing in-order decrypt discipline and add no `Decrypt` call, so no nonce sequencing changes.
- **[Concurrency]** No findings. No goroutine, channel, or lock is added. `consumeLine`'s single-writer invariant is preserved — the arm reads only its local `sl` — and it deliberately does not touch `thinkingSinceEmit`, so the accumulator keeps its single reset boundary at the `result` arm and no cross-turn leak is introduced. The fake writes both lines from its existing single read-loop goroutine.
- **[Network & I/O]** No findings. No socket, listener, timeout, or size cap is introduced or changed. Line size is already bounded upstream by the parser's existing buffering, and the arm reads no field, so it allocates nothing proportional to input. Emitting *fewer* frames strictly reduces relay egress.
- **[File operations]** Not applicable — no path is constructed, opened, created, or removed. `set_permission_mode_v2.1.220_revoke.json` is read by a human during this spec's authorship, not by any shipped code.
- **[Subprocess / external command execution]** Not applicable — no `exec.Command`, no argv, no environment, no signal handling is touched. The child's spawn and teardown paths are unchanged; only the interpretation of one stdout line changes.
- **[Threat model alignment]** The relevant threat is the one the ticket exists to fix: a degraded `unrecognized_message` frame is a **detection** failure — an operator who has learned to ignore the alarm will ignore it when a claude release genuinely starts emitting something new over the relay. This change restores the signal, and AC1(b) plus the two exact-count fake tests are what keep the fix from being a blunter "drop unknown types" that would destroy the alarm instead. The residual — a NAK is consumed indistinguishably from a success — is named in the arm's comment and accepted per Evidence-Based Fix Selection; it degrades diagnosis of a never-observed condition, not any security property.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
