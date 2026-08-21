# #1680 — Give the tool result summary its own cap at 10000 runes

## Files to read first

| File | Symbols | What to extract |
|---|---|---|
| `internal/turnbridge/outbound.go` | `resultSummary`, `truncate`, `maxSummaryLen` | The three arms this ticket edits; `truncate`'s rune-safe cut and its `…`; `maxSummaryLen`'s doc comment, whose "input/result précis" wording becomes wrong here and must be corrected to input-only. |
| `internal/turnbridge/outbound.go` | the `maxInputValueRunes` … `maxInputTotalRunes` const block (#1678) | **The template for the new constant's doc comment.** It already carries the six-bytes-per-rune arithmetic for a RUNE cut, the never-raise rule, the enforcing-test pointer, and the eventring memory paragraph. The new comment is that shape with this ticket's numbers — do not re-derive the escaping argument from scratch. |
| `cmd/pyry/interactive_turn_v2.go` | `maxDeltaTextBytes` | The other free-text field on a v2 envelope, already at 10000, with the standing rule *"if that ever fails, LOWER this constant — never raise it."* This ticket adopts the same number deliberately. |
| `internal/protocol/interactive_test.go` | `TestToolUsePayload_FitV2EnvelopeCap`, `TestBackgroundTaskPayloads_FitV2EnvelopeCap`, `maxV2AppEnvelope` | The measurement technique AC 4 names: `'<'` fill, worst-case `Envelope` (max-uint64 `ID`, populated `EventID`), `t.Logf` the percentage, then assert `< maxV2AppEnvelope`. Copy the technique; see § Where the envelope test lives for why the new test does **not** live in this file. |
| `internal/protocol/interactive.go` | `ToolResultPayload` | The five fields the envelope carries. Its doc comment gains no `SECURITY:` paragraph here — see § What this ticket does not touch. |
| `internal/turnbridge/outbound_test.go` | `TestResultSummary`, `TestMapEvent`'s `ToolUpdate` rows, `TestInputSummary` | The `"text truncated"` row that pins the old bound and must move; the `MapEvent` table where AC 3's `is_error` row belongs; `TestInputSummary`, which AC 2 requires to pass **unmodified**. |
| `internal/streamsup/parser.go` | `toolResultContent`, `toolResultText`, and the `ToolUpdate` emit site in `emitUser` | Proof that the live producer applies **no** cap to result text and passes `block.ToolUseID` through verbatim. `resultSummary` is therefore the only bound on this field — which is why the new constant is a wire constraint, not a display bound. |
| `docs/knowledge/features/turnbridge-package.md` | § "Per-field input extraction (`inputFields` / `inputValue`, #1678)" | The recorded lesson that decides § Where the envelope test lives: `maxInputFields` "shipped correct but with no test standing on it" because the cap test lives in `protocol`, hand-builds its payload, and cannot call the unexported producer at all. |
| `internal/eventring/ring.go` | package doc, `MaxEventsPerConversation`, `Event` | `tool_result` is control-class and preferentially **retained**, 1024 per conversation, storing the marshalled payload bytes. This makes the new cap a memory knob as well as a wire knob. |
| `docs/protocol-mobile.md` | § `tool_use` (the **Bounds.** paragraph), § `tool_result` | The prose shape AC 5's new paragraph mirrors, and the section it lands in. |

## Context

`resultSummary` caps a tool result at `maxSummaryLen` — 200 runes, the same constant that bounds the tool *input* précis. Measured across 11379 tool results from local claude transcripts (2026-08-21): mean 2959 characters, median 721, **only 22% survive a 200-rune cap whole**. The desktop client is about to render the result in an expanded tool row, so the text is going on screen for the first time and will show 200-character stubs until this lands.

The producer applies no cap of its own (`toolResultContent` returns claude's text verbatim), so `resultSummary` is the *only* bound between an arbitrarily large tool result and a 65519-byte envelope. That inverts `maxSummaryLen`'s framing: the new constant is a **wire constraint**, and the "phone-display bound, not a wire constraint" wording stays with the input path where it is still true. This is the same inversion #1678 wrote for the input-field constants.

**No ADR.** This is one constant and one prose paragraph, decided inside conventions ADR 025 and #1678 already set. The interesting content belongs in the constant's doc comment and § `tool_result`, both of which this ticket writes.

### The arithmetic, verified

Every number the ticket body states was re-measured against a real `protocol.ToolResultPayload` inside a real `protocol.Envelope` (max-uint64 `ID`, populated `EventID`, UUID `conversation_id`/`turn_id`, a `toolu_`-shaped `tool_use_id` — 309 B of overhead outside `result_summary`) and all of them hold:

| `result_summary` runes | `'<'` fill, UUID ids | `'a'` fill, UUID ids | `'<'` fill, hostile 64-rune ids |
|---|---|---|---|
| 200 (today) | 1509 B | 509 B | 2559 B |
| **10000 (this ticket)** | 60309 B | 10309 B | 61359 B |
| 16000 | 96309 B — **47% over** | 16309 B | 97359 B — over |
| 64525 (largest measured result) | 387459 B — over | 64834 B | 388509 B — over |

Three further measurements this spec adds, because they are what the developer's test and doc paragraph must state:

- **The shipping worst case is 61362 B — 93.7% of the cap, 4157 B of headroom.** That is `truncate(64525×'<', 10000)` — 10001 runes, the 10000-rune bound plus `truncate`'s one `…` — marshalled with hostile 64-rune ids. With realistic UUID ids it is 60312 B (92.0%).
- **6 bytes per rune is the true ceiling for a rune cut, not merely for a byte cut.** Measured: 10000 × `'<'`, 10000 × NUL, and 10000 × U+2028 all marshal to exactly 60309 B. 10000 × a 4-byte emoji marshals to 40309 B — multi-byte runes are *not* the worst case, because Go emits them raw.
- **The `…` costs 3 bytes, not 6.** U+2026 is emitted raw. The bound is on **pre-ellipsis** content, exactly as #1678's constants are; say so in the comment rather than pretending the bound is exact.

**Do not re-propose 16000.** It is 47% over the cap, and exceeding the cap does not truncate the frame — it *loses* it: nothing on `V2SessionManager.Push`'s path bounds the plaintext, so an oversized frame fails at the AEAD or is rejected by the phone's own decode cap. `tool_result` is never-droppable control class, so the operator would see an empty row rather than a truncated one — strictly worse than today.

### Why the same number as `maxDeltaTextBytes`

`maxDeltaTextBytes` solved this exact problem for the one other free-text field on a v2 envelope and landed on 10000. Using a different number for the same envelope would invite the two to drift. At 10000 the measured table puts **at least 90%** of results through whole, against 22% today.

The ceiling with hostile ids is 10693 runes and with UUID ids 10868, so 10000 is a ~6.5% rune margin. That margin is the whole reason the never-raise rule attaches to this constant.

## Design

One production file. One new constant, three one-token substitutions, one stale comment corrected.

### 1. The new constant

Add to `internal/turnbridge/outbound.go`, immediately after the #1678 const block, so the file reads `maxSummaryLen` → input-field caps → result cap:

```go
const maxResultSummaryRunes = 10000
```

The doc comment is the deliverable, not the literal. It must state, in `maxDeltaTextBytes`'s form:

- **What it bounds and why it is separate from `maxSummaryLen`** — the input précis keeps its meaning, its value and its own cap; results are an order of magnitude bigger and this is the only bound on them, because the producer applies none.
- **The six-bytes-per-rune arithmetic for a RUNE cut.** `SetEscapeHTML` is on by default, so `'<'`, `'>'`, `'&'` and every control byte without a short escape cost six bytes; U+2028/U+2029 escape to 6 from 3 input bytes; an invalid byte becomes U+FFFD (6 wire bytes) while `utf8.RuneCountInString` counts it as one rune; a multi-byte rune is emitted **raw** at 4 bytes or fewer. 10000 × 6 = 60000 B is the content ceiling.
- **That the bound is on pre-ellipsis content**, with one `…` (3 raw bytes) riding on top.
- **The measured worst case: 61362 B, 93.7% of the 65519-byte cap, 4157 B of headroom**, at hostile 64-rune identity fields.
- **The identity-field assumption, out loud.** `conversation_id` and `turn_id` are daemon-supplied; `tool_use_id` is claude's `block.ToolUseID` passed through **verbatim with no cap**. The 64-rune hostile fill is roughly 11× the longest observed (`tool_use_id` at 29 bytes) and is an assumption, not an enforced cap — the same statement `TestToolUsePayload_FitV2EnvelopeCap` makes for the tool_use frame.
- **The never-raise rule, with both of its reasons.** If the cap test fails, LOWER this constant. Reason one is the wire. Reason two is memory: `tool_result` is control-class in `internal/eventring`, preferentially retained up to `MaxEventsPerConversation` (1024) per conversation with the marshalled payload bytes held, so this constant multiplies the ring's worst-case per-conversation footprint — see § Security review.
- **That 10000 is `maxDeltaTextBytes`'s number on purpose**, and that 16000 was measured at 96309 B and rejected.
- **That `tool_use` and `tool_result` are separate envelopes**, so #1678's budget and this one do not sum (the #1678 block already says this from its side; say it from this side too).

### 2. `resultSummary`

Every arm switches to the new constant. Signature, call site, arm count, and exhaustiveness over the sealed `ToolContent` all unchanged:

- `TextContent` → `truncate(v.Text, maxResultSummaryRunes)`
- `DiffContent` → `truncate(v.Path, maxResultSummaryRunes)`
- `TerminalContent` → `truncate("terminal "+v.TerminalID, maxResultSummaryRunes)`

**One constant for all three arms, not a per-arm exception.** The cap is inert for two of them — a `DiffContent` carries a path and a `TerminalContent` carries `"terminal " + id`, neither of which approaches 10000 — and the behavioural change is scoped to `TextContent`, the only arm the live producer emits. One rule is cheaper to read and to test than three, and the unreachable arms stay deliberately minimal per `resultSummary`'s existing doc comment.

**`resultSummary` still does not see `is_error`, and must not learn to.** The flag is derived at the `MapEvent` call site (`e.Status == turnevent.ToolStatusFailed`) and never reaches the helper. Capping errors identically is what keeps the signature and the arm count unchanged — that is most of why this ticket is small. See § Error handling for why the ticket dropped the error exception.

### 3. `maxSummaryLen`'s doc comment

Its first sentence says it "bounds the input/result précis". After this ticket that is false. Correct it to the input précis, keep the "phone-display bound, not a wire constraint" framing (still true for `inputSummary`), and point at `maxResultSummaryRunes` for the result side.

`maxSummaryLen`'s **value** does not change and `inputSummary` is not touched (AC 2). Its other references are prose and stay as they are: the #1678 const block's comparison, the `ModelAnnounced` arm's "not the applicable bound" warning, and `protocol`'s mirrored `capSummaryLen`, which is `InputSummary`'s cap in the tool_use test.

### Where the envelope test lives

**`internal/turnbridge/outbound_test.go`, not `internal/protocol/interactive_test.go`** — a deliberate departure from where AC 4's two named precedents sit. Two reasons, and the first is a lesson this repo already paid for:

1. **A hand-built payload cannot stand on the producer's cap.** `docs/knowledge/features/turnbridge-package.md` records that `maxInputFields` "shipped correct but with no test standing on it": deleting its guard leaves both `turnbridge` and `protocol` fully green, because `TestToolUsePayload_FitV2EnvelopeCap` builds its map by hand and cannot call the unexported `inputFields` at all — it measures the *envelope*, not the *cap*. Repeating that placement here would produce a test that stays green when `maxResultSummaryRunes` is raised to 16000. A test in `turnbridge` drives the real `resultSummary`, so the constant is what it stands on.
2. **`protocol` cannot import `turnbridge`** — that is an import cycle — so a `protocol`-side test can only mirror the constant, and a mirrored constant is exactly the drift this ticket should not add. `turnbridge` already imports `protocol`, so the new test uses the real `ToolResultPayload` and the real `Envelope` with no mirroring at all.

Do **not** add a `capResultSummaryRunes` entry to `protocol`'s mirrored-constant block. Nothing in that package would use it, and an unused mirror is a stale measurement waiting to happen.

## Concurrency model

None. `internal/turnbridge` is a pure value-to-value adapter — no goroutines, no state, no I/O, no clock read — and this ticket adds none. `resultSummary` stays a pure function of its argument.

## Error handling

No new error paths, no new reject branches, no new return values. `resultSummary` has no error return and gains none.

**The error exception is deliberately dropped, and the spec states why** rather than leaving it as an untested consequence of a shared code path:

- **Uncapped is unavailable.** An error result is unbounded by construction — a failing build or test run dumps as much as it likes. The 234-error corpus (2% of results, mean 408 characters, 97% under 2000) is evidence about what *has* been observed, not a bound on what can arrive. An unbounded field on a frame with a hard 65519-byte cap is a lost control frame waiting to happen.
- **"Envelope headroom" is not a second tier.** The headroom for *any* tier is 10868 runes with UUID ids and 10693 with hostile ones. Giving errors 10693 and successes 10000 is a distinction with no behavioural difference on any content that exists.
- **The operator's actual goal is met by the raise itself.** Every error in the measured corpus arrives whole at 10000; the 200-rune cap was what destroyed them.

Raising the constant later is one literal, but it cannot go above ~10693 without a byte-level clamp that measures the serialised frame — separate ticket's worth of machinery, named in § Open questions.

## Testing strategy

All in `internal/turnbridge/outbound_test.go`. Scenarios, not code:

**`TestResultSummary` — move the one row that pins the old bound (AC 1).**

- The `"text truncated"` row's fixture is 300 `'b'`, which is now *under* the cap and would assert pass-through. Rebuild it above the new cap (`strings.Repeat("b", maxResultSummaryRunes+N)`) with `strings.Repeat("b", maxResultSummaryRunes) + "…"` as the want, so the row still exercises the cut. Express both against the constant, never against a bare literal.
- Add one row proving the cut is **rune-safe at the new bound**, in `TestTruncate`'s idiom: a multibyte fixture over the cap, asserting the result is valid UTF-8 and ends in `…`. `truncate` is unmodified, so this is a cheap guard on the composition rather than a re-test of `truncate`.
- The `"text verbatim"`, `"diff -> path"` and `"terminal -> reference"` rows stay green unmodified — they are the pass-through evidence that the raise did not change short results.

**`TestMapEvent` — `is_error` does not change the bound (AC 3).**

`resultSummary` cannot see `is_error`, so this row belongs at the `MapEvent` level where the flag is derived, beside the existing `ToolUpdate` rows. One row: `ToolUpdate{Status: turnevent.ToolStatusFailed, Content: TextContent{over-cap text}}` asserting the payload carries `IsError: true` **and** a `ResultSummary` cut at exactly the same bound the success row is cut at. Asserting only `IsError` would leave AC 3 vacuous; the point is the *bound*, not the flag.

**`TestToolResultPayload_FitV2EnvelopeCap` — the measured envelope guarantee (AC 4).**

New test, `TestToolUsePayload_FitV2EnvelopeCap`'s shape:

- Drive the real `resultSummary` with `turnevent.TextContent{Text: strings.Repeat("<", 64525)}` — the largest measured result, `'<'`-filled. **The fill is `'<'`, not `'a'`**: an `'a'` fill measures 10309 B against 60309 B and would prove nothing. Say that in the test's doc comment, in the form the two precedent tests say it, and add the point those tests do not make: `'<'` is the realistic case here, because a tool result is raw command output or file contents and reading a TSX or HTML file is an ordinary `'<'`-dense result.
- Fill all three identity fields hostilely at 64 runes of `'<'`, and say why in the comment: nothing bounds them, `tool_use_id` is claude's value verbatim, and 64 is ~11× the longest observed. This is the assumption made explicit, not an enforced cap.
- Marshal into the worst-case `Envelope` (max-uint64 `ID`, `TypeToolResult`, a fixed `TS`, populated `EventID`), `t.Logf` the byte count and its percentage of the cap, then assert `< 65519`. Declare the cap as a test-local constant with the same "nothing in this package enforces it" note `maxV2AppEnvelope` carries; `turnbridge` does not enforce it either.
- Expected: **61362 B, 93.7%**. Do not hard-code that as an assertion — the log line is the artefact a future reader compares against, and the assertion is the inequality.

The test earns its place by sole-redness against two mutants the developer should actually run:

| Mutant | Measured result |
|---|---|
| `maxResultSummaryRunes` raised to 16000 | 97362 B — red |
| `TextContent` arm returns `v.Text` uncapped | 388509 B — red |

Both are red *only* because the test drives `resultSummary`; a hand-built payload in `protocol` stays green under both.

**Unchanged and must stay green (AC 2 / AC 5):**

- `TestInputSummary` — **unmodified**, byte for byte. AC 2's evidence that the input path is untouched.
- `TestInputFields`, `TestToolUsePayload_FitV2EnvelopeCap` — unaffected; `tool_use` is a separate envelope.
- `cmd/pyry`'s `TestInteractiveTurnEmitterV2_NoAppOutputLogLeak` — **already covers AC 5's no-log half.** It drives a `ToolUpdate` carrying `secretToolReslt` through the emitter with a DEBUG-level handler and the push failing (the most log-heavy path), and asserts the value never reaches the log. Verify it stays green; do **not** write a second one. `internal/turnbridge` imports no logger and this ticket adds none, which is the structural half of the same claim.

## Documentation deliverable

`docs/protocol-mobile.md` § `tool_result` gains a **Bounds.** paragraph after the field table, mirroring the shape § `tool_use` uses. It must state:

- The cap: **10000 runes** (runes, not bytes), and that a result the daemon shortened ends in `…`.
- That a result legitimately ending in `…` is indistinguishable from a cut one — the accepted cost of the marker, stated the way § `tool_use` states it.
- That **`is_error` does not change the bound**: an error result is truncated exactly as a success result is.
- The six-bytes-per-rune arithmetic that fixes the number, and the measured worst case (61362 B against the 65519-byte cap). **Do not reuse `unrecognized.raw`'s "escaping is mild in practice" argument** — that argument rests on the payload already being JSON text with pre-escaped control characters, and it does not transfer to raw command output or file contents.
- That `result_summary` is model-authored text the daemon neither resolved nor validated, and clients must render it as inert text — never feed it to an HTML sink, an attribute, or a URL. § `tool_use`'s "display strings, not capabilities" paragraph is the precedent; the hazard is the same one and a `'<'`-dense result is the ordinary case, not the contrived one.

This is the ticket's last deliverable. Per the pipeline's phase split, the developer writes **no** knowledge-base doc — `docs/knowledge/features/turnbridge-package.md` and `protocol-package.md` are the documentation phase's to fold this into.

## What this ticket does not touch

Named so nobody widens the diff:

- **`maxSummaryLen`'s value, and `inputSummary`.** Only the stale half-sentence in the comment changes.
- **`ToolResultPayload`.** No new field, no `MarshalJSON`, no `SECURITY:` paragraph. The struct re-decides no maximum — the bound belongs to the producer of the value, which is the posture `BackgroundTaskStartedPayload`'s doc comment states for the same reason. `docs/protocol-mobile.md` is where the wire contract is written.
- **`internal/streamsup`.** The producer stays cap-free for result text. Adding a second cap upstream would be a second place the limit is decided and the two could disagree silently — the hazard the `RateLimited` and `ModelAnnounced` arms both name.
- **`internal/eventring`.** No retention change; see § Security review for why the memory finding is recorded rather than fixed here.
- **`internal/protocol/interactive_test.go`.** No new mirrored constant, no new test.
- **`internal/e2e/realclaude`'s background-idle probe.** Its truncation tell keys on `InputSummary` at 201 runes, on `TypeToolUse` — the input path, untouched.

## Open questions

1. **A byte-level envelope clamp.** The only way past ~10693 runes is a clamp that measures the serialised frame and shortens `result_summary` to fit, rather than a rune cap chosen against a worst case. It would also close the unbounded-`tool_use_id` gap below. Out of scope; a separate ticket if a human wants results larger than 10000.
2. **Capping `tool_use_id` / `name` upstream.** Already named as an open question by #1678 for the `tool_use` frame; this ticket inherits the same gap on the `tool_result` frame. See § Security review, finding 2.
3. **Whether 10000 is the right operator answer.** 16000 would carry 97% of results whole against 10000's ~90%, and the 7-point difference is what the wire costs. The constant is one literal; if a human disagrees, it moves down, not up.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings; the boundary is explicit and unchanged.** The untrusted → trusted crossing is claude's stdout JSONL → `internal/streamsup`'s parser → `turnevent` → `resultSummary` → wire. `resultSummary` is a pure single-function boundary that treats its input as opaque model-authored text: no parsing, no interpretation, no re-encoding, no path resolution. This ticket changes one number in it and adds no branch, so the boundary neither moves nor widens. Downstream awareness is carried by prose, not by the type system (both sides are `string`), which is why AC 5's doc paragraph must state "render as inert text" — that paragraph *is* the boundary signal for the client, and it is the one part of this ticket a client actually depends on.

- **[Network & I/O — input size limits] MUST FIX, and fixed in this spec.** This is the whole ticket, so it gets the scrutiny rather than a wave-through. The producer applies **no** cap (`toolResultContent` returns claude's text verbatim), so `resultSummary` is the sole bound between an unbounded model-adjacent string and a hard 65519-byte frame. A cap chosen by pre-escape arithmetic would be the third repetition of an error this repo has already made twice. The spec therefore (a) fixes the number by measurement rather than argument — 61362 B worst case, 93.7%, 4157 B headroom; (b) mandates a `'<'` fill, since `'a'` under-reports 6× and would pass a broken cap; (c) mandates the test drive the real `resultSummary`, so the constant is what the measurement stands on, which the `protocol`-side placement demonstrably does not achieve; and (d) attaches a never-raise rule naming the enforcing test. Without (c) the category would still be open: a green hand-built test next to a raised constant is exactly the shape that ships an oversized frame.

- **[Network & I/O — resource exhaustion, memory] SHOULD FIX, discharged by documenting it on the constant.** `tool_result` is control-class in `internal/eventring` and therefore *preferentially retained*: up to `MaxEventsPerConversation` (1024) per conversation, holding the marshalled payload bytes. Raising the cap 50× in runes raises the ring's worst-case per-conversation footprint from **~1.4 MiB to ~59 MiB** — measured: a marshalled `tool_result` payload with realistic UUID ids goes from 1393 B at the old cap to 60193 B at the new one, and the ring holds 1024 of them. The daemon is long-lived and multi-conversation, so that multiplies across the pool. The realistic figure is far lower — median result 721 characters, ~10% of results exceed 8000 — but the worst case is model-driven and an agent repeatedly reading large HTML files reaches it without malice. Not fixed here: adding a ring byte-ceiling is separate machinery, and #1678 set the precedent of recording this on the constant as the *second* reason for the never-raise rule. The spec requires exactly that paragraph. A reviewer who wants a hard bound should file against `internal/eventring`, not against this cap.

- **[Network & I/O — resource exhaustion, push queue] No finding; the analysis is structurally unchanged.** `pushQueueByteCeiling` (32 MiB) is derived against `pushQueueCap` (256) × the 65519-byte envelope cap = 16 MiB, so it *cannot* trip on a queue within the count cap regardless of how large individual payloads are. Raising this cap moves real bytes held during a transport outage but changes neither the tear-down condition nor the `4413` path. Worth stating because the naive reading — "300× the bytes per tool_result, so overflow gets likelier" — is wrong, and a reviewer arriving at it could ask for machinery that is not needed.

- **[Network & I/O — the unenforced assumption] SHOULD FIX, discharged by making it explicit; the residual is pre-existing and named.** The 65519-byte guarantee holds only if the identity fields stay small, and one of them is **not bounded by anything**: `tool_use_id` is claude's `block.ToolUseID` passed through verbatim at the `ToolUpdate` emit site, as `name` and `block.ID` are on the `tool_use` side. A pathological id blows the frame at *any* result cap, so the hazard predates this ticket — but this ticket shrinks the headroom that silently absorbs it from ~64 KB to ~4.2 KB, so a mid-sized id that is harmless today becomes fatal after. Two mitigations, both required by the spec: the cap test fills all three ids hostilely at 64 runes (~11× the longest observed), and the constant's comment states the assumption out loud as an assumption rather than an enforced cap. The real fix is upstream capping — already #1678's open question, carried forward here as § Open questions 2 — or the byte-level clamp in § Open questions 1.

- **[Error messages, logs, telemetry] No finding, and the existing coverage is load-bearing.** Tool results are user content and MUST NOT reach any log. `internal/turnbridge` imports no logger and this ticket adds none — the structural half. The behavioural half is already pinned by `cmd/pyry`'s `TestInteractiveTurnEmitterV2_NoAppOutputLogLeak`, which drives a `ToolUpdate` carrying a result sentinel at DEBUG level with the push failing, so the log-heaviest path runs. The spec requires verifying it stays green and explicitly forbids writing a second one. Note the trap the turnbridge overview records against that test's shape: a log-absence assertion alone passes for the wrong reason if an arm silently drops the event, which is why the existing test pairs it with a decoded-payload presence assertion. Raising the cap does not disturb either half — the sentinel is short and crosses verbatim.

- **[File operations] Not applicable.** No path is constructed, opened, stat'd or written. `resultSummary` is pure and `turnbridge`'s package doc commits to no I/O.

- **[Subprocess execution] Not applicable.** Nothing here executes. Worth stating rather than skipping, because the *value* is frequently a shell command's output and a `Bash` result is command output verbatim — the client-side hazard is rendering, which AC 5's doc paragraph addresses, not execution.

- **[Cryptographic primitives] Not applicable.** No randomness, no keys, no comparison against a secret. The frame is sealed downstream by `CipherState.Encrypt` on a path this ticket does not touch; the only crypto-adjacent property is that an oversized plaintext fails at the AEAD, which is the failure mode the cap exists to prevent and is covered above.

- **[Concurrency] Not applicable.** No goroutine, no lock, no shared state, no shutdown path. `resultSummary` is a pure function of its argument, called on the emitter's single `Run` goroutine.

- **[Threat model alignment] No findings.** The relevant threat is `docs/protocol-mobile.md` § Application-envelope size cap — a frame that exceeds it is lost rather than truncated, and `tool_result` is in the never-droppable control class (§ Error codes, `4413`), so a lost frame shows the operator an empty row. The spec addresses it by measurement plus an enforcing test. The two threats this ticket does not close — unbounded identity fields and unbounded ring retention — are named above with their owners.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-21
