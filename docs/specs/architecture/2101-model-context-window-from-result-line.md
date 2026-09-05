# #2101 — Decode claude's per-model `contextWindow` into a bounded daemon event

## Files read

- `internal/streamsup/parser.go` → `streamLine` — the segmentation struct this ticket must NOT widen; its doc states the rule and `TestStreamLine_StaysSegmentationOnly` enforces it.
- `internal/streamsup/parser.go` → `consumeLine`'s `case "result"` arm — the edit site. It already holds the raw `line` in scope and hands it to `emitRateLimit` on the neighbouring arm; the same is available here.
- `internal/streamsup/parser.go` → `resultTurnEndReason` — the subtype→reason table AC 4 pins. Untouched by this ticket.
- `internal/streamsup/parser.go` → `emitModelList` — the nearest structural analogue: a cardinality cap, a per-entry text cap, a `Dropped*` count on the event, and no payload byte in any log. Its `boundEach` closure is the model for "one report per field, not per cut element".
- `internal/streamsup/parser.go` → `maxModelField`, `maxModelResolved`, `maxModelListEntries`, `maxUnrecognizedRaw`, `truncateField` — the cap family. Their doc blocks state the arithmetic each cap is derived from; this package expects a new cap to state its own.
- `internal/streamsup/parser.go` → `systemTaskStartedLine`, `systemBackgroundTasksLine` — the per-subtype decode-target pattern (a struct taken off the raw line bytes, kept out of `streamLine`).
- `internal/turnevent/event.go` → `TurnEnd` — the variant being widened; its doc currently reads "carrying the reason only" and must be rewritten.
- `internal/turnevent/event.go` → `ModelList`, `ModelOption` — the field-doc conventions this event copies: `DroppedModels`' "true size is len + dropped", and `EffortLevels`' nil-spelling for an empty list (#1828).
- `internal/turnbridge/outbound.go` → `MapEvent`'s `case turnevent.TurnEnd` — builds `protocol.TurnEndPayload` field by field, so a widened `TurnEnd` cannot reach the wire by construction.
- `internal/contextwindow/usage.go` → `Usage.WindowTokens`, `defaultWindowTokens` — the consumer #2102 will join against. `WindowTokens` is an `int` and 0 already means "no trustworthy window"; this event's field type and its "no window" spelling match it.
- `internal/streamsup/parser_test.go` → `logRecorder`, `TestParser_ModelAnnouncedIsLoggedContentFree` — the log-sweep idiom AC 5 is proven with.
- `docs/knowledge/features/streamsup-package-two-further-lessons-from-tightening-that-sam.md` — three lessons that change how this is built: (a) `if len(x) > cap` is an **equivalent mutant** against `>=` when the guarded block computes only a count and a slice, so the boundary row must be pinned by overlaying a cap that fires one entry early; (b) a per-entry accumulator's isolation needs a cut-then-clean fixture, and a running-total accumulator needs three entries with a clean one in the middle; (c) `maxUnrecognizedRaw`'s 16 KiB is the retained-bytes ceiling, with the fraction **re-derived per shape** rather than inherited (ADR 036).
- `docs/knowledge/features/streamsup-package-the-per-entry-byte-budget-s-third-dimension.md` — the "absent / null / published-empty are one reading" collapse and why its test needs a fixture-level guard, not just a decoded-value assertion.
- `internal/e2e/realclaude/testdata/*.json` — the measurement base. Re-counted 2026-09-05: **55 `result` objects across 30 capture files, all 55 carrying `modelUsage`** (the ticket says 56/31; the difference is immaterial to every number below). Every map has exactly **2** entries; the longest model id is **25** bytes (`claude-haiku-4-5-20251001`); windows are 200000 and 1000000; `canonicalModel` is present in the v2.1.220/v2.1.239 arms and absent in v2.1.143/158/199.

## Context

The daemon's only context-window number is the `defaultWindowTokens = 200_000` guess in `internal/contextwindow`. Since #2100 a session whose used count exceeds that guess reports no window at all rather than a clamped lie, so a 1M-context session past 200K shows nothing. Claude reports the real per-model window on every `result` line under `modelUsage`, and the parser already receives and drops it.

This ticket carries that number as far as a daemon event. Reporting it on the gauge is #2102.

No ADR is warranted: this adds two caps to an established family and one field-set to an existing variant, and ADR 036 already covers the cap-derivation doctrine both caps follow.

## Design

### 1. The route: widen `TurnEnd`, no new variant

The window is learned at exactly the moment `TurnEnd` is emitted, so a second variant would carry a second copy of the same turn boundary. The four production case sites (`stream_turn_busy.go`, `interactive_turn_v2.go` twice, `turnbridge/outbound.go`) type-switch and read `Reason` only; all 69 `turnevent.TurnEnd{…}` literals in the tree are keyed, so no call site changes.

**The widened variant cannot reach the wire.** `MapEvent`'s `TurnEnd` arm constructs `protocol.TurnEndPayload` field by field (`ConversationID`, `TurnID`, `StopReason`) rather than embedding the event, so the new fields are unreachable from the v2 envelope by construction, not by omission. That is deliberate — no wire consumer wants this yet, and #2102 owns whatever publication it needs. It also means **the v2 application-envelope arithmetic the cap family usually states does not apply here**; the ceiling this ticket derives against is the package-internal retained-bytes one.

`TurnEnd` gains a slice field, so it stops being comparable with `==`. Nothing compares it: the only `TurnEnd{}` in the tree is the `var _ Event = TurnEnd{}` assertion, and `ModelList` already puts a slice-carrying variant through every emitter path this one travels.

### 2. The decode target

A new `resultLine` struct, unmarshalled from the raw `line` bytes inside the `case "result"` arm — `systemTaskStartedLine`'s pattern, and `emitRateLimit`'s call shape. `streamLine` is untouched.

```go
type resultLine struct {
    ModelUsage map[string]resultModelUsage `json:"modelUsage"`
}
type resultModelUsage struct {
    ContextWindow int `json:"contextWindow"`
}
```

`maxOutputTokens` and `canonicalModel` stay **undeclared**. Absence from the decode target is a stronger guarantee than a test sweep, and `canonicalModel` is version-dependent (fact 3) — declaring it would invite a consumer to depend on a key three of five observed claude versions do not send.

Declaring the map means a `modelUsage` that arrives as a number, a string, or an object whose values are not objects fails **this** unmarshal — never the line. AC 4 is satisfied structurally: the `TurnEnd` reason comes from `resultTurnEndReason(sl.Subtype)` off the already-decoded `streamLine`, and the window decode is a second, independent unmarshal whose only failure mode is "no windows".

`ContextWindow` is `int` and signed on purpose: a negative reading must be *observable* so it can be rejected, which an unsigned type would silently wrap.

### 3. The event shape — a sorted slice, not a map

```go
type ModelWindow struct {
    ModelID      string
    WindowTokens int
}
// on TurnEnd:
ModelWindows        []ModelWindow
DroppedModelWindows int
```

**Why a slice and not the map claude sent.** Go map iteration is randomised, so a cardinality cap over a map would make *which* entries survive differ between two runs on identical bytes — the event would be nondeterministic and untestable at the boundary. Sorting by `ModelID` is the only deterministic order available (a JSON object has no order to preserve, so the list caps' "claude's order, from the tail" rule has nothing to apply to here) and it matches `ModelList.Models`' slice shape. Both entries of the two-model capture reach the event under their own ids; nothing is collapsed, maxed, or picked.

`WindowTokens` is an `int` to match `contextwindow.Usage.WindowTokens`, the field #2102 joins to.

### 4. The two caps

Both are new constants in `parser.go`, applied at construction like every cap in the family, each carrying its own derivation doc block.

**`maxModelWindowID = 256`** — the model-id length. Observed maximum 25 bytes, so 256 is ~10.2x, the multiple `maxModelField` and `maxModelResolved` take over **the same identifier shape** and for their stated reason: room for a naming scheme claude has not shipped, and a hard cut on anything that has stopped being an identifier. A separate constant even though it equals both, per the family's standing rule.

**Overflow is a DROP, not a truncation** — the one place this cap departs from its two siblings, and the departure is AC 3's. `truncateField` is deliberately not used: #2102 joins on the id, so a cut id names no model and is strictly worse than no entry. There is therefore no `TruncatedFields` on this shape; the report is the count below.

**`maxModelWindowEntries = 16`** — how many entries the event carries. Derived:

- *Multiplicand:* 256 bytes per entry (`maxModelWindowID`; the `int` beside it is daemon-decoded and carries none of claude's bytes, exactly as `ModelOption.SupportsAutoMode` is excluded from that entry's budget).
- *Ceiling:* `maxUnrecognizedRaw`'s whole-line 16 KiB, at **1/4** of it — the package's ordering rule that a whole KNOWN event must not approach the cap on an entire UNKNOWN line, measured retained-against-retained. The fraction is re-derived for this shape rather than inherited from the roster's 1/2 or the model list's 5/8, which is ADR 036's rule. `16 * 256 = 4096 = 16384/4` exactly.
- *Floor:* the observed map is 2 entries, and entries are not models — fact 2's alias pairing means one model can contribute two entries, so 16 entries is ~8 models in a single turn, against an observation of at most two.
- *A power of two*, matching every constant in this family except `maxModelListEntries`.
- *NOT 4:* twice an observation of 2 is not room, and under the alias doubling 4 entries is only 2 models — a turn that spawns a subagent on a third model is claude's ordinary output and would be cut. `maxModelListEntries`' NOT 8 failure, one scale down.
- *NOT 8:* 4 models. A turn using the session model, a haiku helper and two subagent models, each aliased, is exactly 8 — a cap that fires *at* the boundary of plausible ordinary output.
- *NOT 32:* 8192 bytes is half of `maxUnrecognizedRaw`, `maxTaskRosterEntries`' fraction, and there is no evidence asking for it. Taking half the unknown-line budget for a field observed at 2 inverts the ordering rule it is derived from.

Both caps are applied **after** `json.Unmarshal`, so a hostile map is materialised transiently before it is bounded — `maxTaskRosterEntries`' accepted trade, with `defaultMaxParseBuf`'s 4 MiB whole-line cap bounding the spike. What is RETAINED is at most `16 * 256` bytes of claude-derived text plus 16 ints.

### 5. Filter order, and why it is this order

In `decodeModelWindows(line []byte) ([]turnevent.ModelWindow, int)`:

1. Unmarshal `resultLine`. On error → `(nil, 0)`.
2. Per entry, reject and count: `ContextWindow <= 0` (this collapses AC 2's *absent*, *zero* and *negative* into one rejection — an absent key decodes to 0), then `len(id) > maxModelWindowID`.
3. Sort the survivors by `ModelID`.
4. Apply `maxModelWindowEntries` from the tail of the sorted order; add the cut to the count.
5. Return nil rather than an empty slice when nothing survives.

**Content filters run before the cardinality cap, and that ordering is a security property.** Claude's output is untrusted input to this parser. If the count cap ran first, a map padded with unusable (zero-window) entries could evict the real readings before they were ever examined; filtering first makes that padding inert. Padding with *plausible* entries can still evict, which is inherent to any cardinality bound — the mitigations are the cap's 8x headroom and the fact that the eviction is reported rather than silent.

**The `>` boundary is an equivalent mutant against `>=`** at `len == cap`: the guarded block computes only a count and a reslice, with no report flag inside it, so no fixture can separate them. This is the streamsup lesson applied rather than re-learned; the boundary row therefore pins a cap firing *one entry early*, verified by overlay.

### 6. One drop counter, not two

`DroppedModelWindows` counts **every entry claude sent that the event does not carry**, whatever the reason: unusable window, over-long id, or over-cap. The invariant is `len(ModelWindows) + DroppedModelWindows == len(modelUsage)`, which makes the true map size recoverable exactly as `ModelList`'s `len + DroppedModels` does.

The rejected alternative is a counter per cause. It buys a consumer nothing — nothing renders this list, and #2102 joins on ids it either has or does not — while costing two fields to keep in step, two doc blocks, and a second dimension in every test table. AC 3 asks that the event record *that* a cut happened; one honest counter does that.

### 7. Logging

**Nothing on this path logs at all** — no new log call, no new keyword. AC 5 then holds by construction rather than by a per-attribute check, which is the stronger guarantee: a value that reaches no `slog` call cannot leak from one. This departs from `emitModelList`, which logs a record of daemon-authored counts; that record exists because `logControlResponse` was, before #1848, the cap's only observable. Here the counts ride the event itself.

## Concurrency model

None added. `decodeModelWindows` is a pure function of the line bytes with no receiver state, called from `consumeLine` on the parser's single reader goroutine. No new goroutine, channel, lock, or shutdown path. The parser's one accumulator (`thinkingSinceEmit`) and its reset on this arm are untouched.

## Error handling

| Input | Behaviour |
|---|---|
| No `modelUsage` key / `null` | `ModelWindows` nil, dropped 0, `TurnEnd` emits normally |
| `modelUsage: {}` | as above |
| `modelUsage` a number, string, array, or an object with non-object values | unmarshal fails → nil, dropped 0, `TurnEnd` emits normally |
| entry with absent / 0 / negative `contextWindow` | entry dropped, counted |
| entry id longer than `maxModelWindowID` | entry dropped, counted — never reported under a cut id |
| more than `maxModelWindowEntries` survivors | tail of sorted order dropped, counted |

No error is returned, logged, or wrapped anywhere on this path: there is no failure a caller could act on, and `encoding/json` quotes offending input bytes into its error text — the reason `emitModelList` deliberately does not log its own decode error.

## Testing strategy

In `internal/streamsup/parser_test.go` (table-driven, stdlib only):

- **Capture pin.** Feed the real `result` line from `permission_mode_switch_v2.1.239_dontAsk.json`; assert both entries reach `TurnEnd.ModelWindows` under their own ids with 200000 and 1000000, dropped 0. Guard first that the fixture actually carries both keys, so the row cannot go vacuous — the fixture-guard lesson from the #1828 collapse.
- **Alias-pair capture.** One of the 27 same-window arms, proving two entries with the identical window are both carried rather than deduplicated.
- **Decode table:** absent key, `null`, `{}`, a number, a string, an array, an object of numbers, an entry whose `contextWindow` is a string. Each asserts nil windows, dropped 0 where the map never decoded, and — in every row — that the `TurnEnd` reason is unchanged.
- **Rejection table:** absent / 0 / negative `contextWindow`; an id at the cap, one byte over, and far over. Assert the survivor set, the count, and that no cut id appears in any entry.
- **Cardinality:** under, exactly at, and over `maxModelWindowEntries`; the over row asserts sorted-order survival and the drop count. The exactly-at row's comment states the `>`/`>=` equivalence rather than claiming redness it cannot have.
- **Mixed drop causes** in one map (unusable + over-long id + over-cap), pinning the single-counter invariant `len + dropped == len(modelUsage)`.
- **Determinism:** the same over-cap line fed to two parsers yields identical slices, which is the assertion the map-vs-slice decision exists for.
- **Segmentation (AC 4):** for each of `success`, `error_during_execution` and `error_max_turns`, a line carrying a hostile `modelUsage` still ends the turn with today's reason, and `thinkingSinceEmit` still resets.
- **Log sweep (AC 5):** `logRecorder` over the emit path, both drop paths and the undecodable path with sentinel ids and window values; assert **zero** records and no sentinel in any message or attribute.
- **`TestStreamLine_StaysSegmentationOnly`** must still pass untouched — it is the guard that this decode did not leak into the segmentation struct.

In `internal/turnbridge/outbound_test.go`: a `TurnEnd` carrying windows maps to a `protocol.TurnEndPayload` byte-identical to one from a `TurnEnd` without them — the wire-unreachability claim above, asserted rather than inferred.

Gate: `go test -race ./internal/streamsup/... ./internal/turnevent/... ./internal/turnbridge/...`, `go vet ./...`, `go build ./cmd/pyry`. No live-claude run: every fixture is a committed capture or synthetic, and the fake-claude harness is left alone (its `result` lines carry no `modelUsage`, which is this ticket's absent case).

## Open questions

1. **Does `int` overflow on a hostile `contextWindow`?** On 64-bit it does not; a value past `int64` fails the unmarshal and is caught by the "entry whose `contextWindow` is a string"-shaped rows. To confirm in Phase B with an explicit `99999999999999999999` row and, if the whole map then fails to decode, to record that as the observed behaviour rather than assume the per-entry one.
2. **Does any `internal/e2e` fake-daemon test assert an exact `turnevent.TurnEnd` value with `reflect.DeepEqual`?** All literals are keyed so nothing breaks at compile time, but a `DeepEqual` against a zero-valued `TurnEnd` would still pass with nil windows. To sweep in Phase B; if one exists and reddens, the fix is the fixture, not the design.

Each is resolved in Phase B and recorded under `## Revisions` if it changes anything.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** SHOULD FIX. The boundary is explicit and singular — claude's stdout crosses into daemon state at exactly one function (`decodeModelWindows`) through exactly one decode target (`resultLine`), and everything downstream holds a `turnevent.ModelWindow` whose id is length-bounded and whose window is provably positive. What the plan does **not** say is who owes sanitization of `ModelWindow.ModelID`: it is claude-authored text, and the daemon bounds it without stripping control characters or terminal escapes. Not exploitable as designed — `MapEvent`'s field-by-field `TurnEndPayload` construction means nothing renders it today — but #2102 may publish it, and the obligation has to travel with the field. Phase B writes `ModelOption.DisplayName`'s SECURITY paragraph onto this field's doc: bounded, not sanitized, render boundary is the client's.
- **[Tokens, secrets, credentials]** No findings, and not vacuously — the category's live concern on this path is the package's standing posture that model values never reach a log at any level (`internal/relay`'s `v2session_settings.go`, `internal/sessions`' `pool.go`). § 7 satisfies it structurally by adding no log call at all, which is stronger than a per-attribute check. No credential, key, or token material exists on this path to store, rotate, or revoke.
- **[File operations]** Not applicable, by design decision rather than omission: this path constructs no filesystem path, opens nothing, and writes nothing. Its input is bytes the line reader has already accumulated in memory. No traversal, TOCTOU, mode, symlink, or atomic-write question arises.
- **[Subprocess / external command execution]** Not applicable, same shape: nothing here composes argv, sets an environment, or spawns or signals a process. It reads the stdout of a child some other component already started.
- **[Cryptographic primitives]** No primitive is introduced and no RNG is consulted. The category's real content here is the opposite one — the design **removes** a nondeterminism source. Go randomises map iteration, so a cardinality cap applied over the decoded map directly would let the same bytes yield different events on two runs; § 3's sort by `ModelID` is what makes the cut reproducible.
- **[Network & I/O — resource exhaustion]** SHOULD FIX. Both retained dimensions are bounded (`maxModelWindowEntries` × `maxModelWindowID` = 4096 bytes of claude-derived text plus 16 ints), but the **transient** exposure is not yet written down, and this shape has one step its siblings do not: a sort. `defaultMaxParseBuf` caps the whole line at 4 MiB before the decoder sees it; the densest window-carrying entry is roughly `"aaaa":{"contextWindow":1},` at ~26 bytes, so a pathological line is order 10⁵ entries — an order-10⁵-element survivor slice, sorted at O(n log n), on the parser's reader goroutine. That is a constant-factor multiple on top of the `json.Unmarshal` spike `maxTaskRosterEntries` and `maxModelResolved` already accept as this family's trade, and `defaultMaxParseBuf` remains the whole of what bounds it. No new mitigation is added: the failure is unobserved, and the evidence-based rule says what this buys is the arithmetic in the doc, not code. Phase B writes that paragraph into `maxModelWindowEntries`' doc block, naming the sort as the one super-linear step. Separately, § 5's filter-before-cap ordering **is** the mitigation for the one exhaustion-adjacent attack that is cheap to run — padding the map with unusable entries to evict the real readings — and it makes that padding inert.
- **[Error messages, logs, telemetry]** No findings, and this was checked rather than assumed, because it is the category where the plan could most easily have been wrong. Two halves. Locally: the unmarshal error is discarded without being wrapped or logged, because `encoding/json` quotes the offending input bytes into its error text — routing claude's own strings into the daemon log through a channel no per-attribute check can see, which is `emitModelList`'s stated rule. Downstream: all four production `TurnEnd` sites were read. `turnMarkFor` type-switches and returns a mark; `interactive_turn_v2.go`'s `TurnEnd` arm logs one daemon-authored keyword and no event value; the same file's event-name function returns the variant name only and documents that rule explicitly; `MapEvent` builds `protocol.TurnEndPayload` field by field. No model id and no window value can reach a log line at any level, which is AC 5 end-to-end rather than at the producer only.
- **[Concurrency]** No findings. `decodeModelWindows` is a pure function of the line bytes with no receiver state, called on the parser's single reader goroutine; no goroutine, channel, lock, or shutdown path is added, so there is no lock order to keep, no check-then-mutate on shared state, and nothing to leak. The returned slice is freshly allocated per line and never retained by the parser, so no consumer aliases parser-owned mutable state — the property that made `ModelList`'s two retaining holders worth naming, and that this event has no equivalent of.
- **[Threat model alignment]** The wire threats in `docs/protocol-mobile.md` § Security model do not reach this ticket: `MapEvent`'s `TurnEnd` arm constructs its payload field by field, so the new fields are unreachable from the v2 envelope by construction, and the testing strategy asserts that rather than leaving it inferred. That is also why no application-envelope percentage appears in either cap's derivation — quoting one would measure a bound against a wire this data never touches. **OUT OF SCOPE:** publishing these fields to a client, and whatever wire-side cap and envelope arithmetic that then owes, is #2102's.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-05

## Revisions

### 2026-09-05 — Phase B

**1. § 1's comparability claim was wrong, and the correction is the ticket's main lesson.** The plan said "Nothing compares it: the only `TurnEnd{}` in the tree is the `var _ Event = TurnEnd{}` assertion." Measured false at the first cross-package run: `cmd/pyry`'s `TestSessionModelHold_OtherVariantsChangeNothing` compares two `turnevent.Event` **interface** values with `!=`, which dispatches to the dynamic type's comparison and panics once that type carries a slice — taking the parallel subtests around it down with it. The Phase-A sweep looked for source shaped like `== turnevent.TurnEnd`, and an interface-typed comparison (`seen[0] != tt.ev`) matches no such pattern, so a grep for the *type name* could never have found it. `ModelList` had carried a slice since #1812 and escaped only by not being in that table. Fixed in the test with `reflect.DeepEqual`, with a comment stating that a table of variants cannot use `==` at all. Test-only, and caused by this change rather than pre-existing.

**2. The cardinality table was rewritten with literals, because as planned it pinned nothing.** § Testing said the exactly-at-cap row pins a cap firing one entry early. The first draft wrote every row as `maxModelWindowEntries - 1`, the constant, `+1`, `*4`, deriving the expectation from the same constant — so an overlay moving the cap to 15 moved the fixture and the expectation in lockstep and **the mutant survived a full run**. Rewritten with literals (15/16/17/64); the same overlay now reddens three rows. A cap's own test cannot be written in terms of the cap.

**3. The cut clones rather than reslices — not in the plan, and load-bearing.** `emitModelList` reslices its cut because the result is iterated and discarded inside the call. This one *returns* the slice onto the event, so a bare `windows[:cap]` would keep the decoder's whole backing array — and every model id string in it — reachable for the event's life, falsifying `maxModelWindowEntries`' own claim that what is retained is only the capped result. `slices.Clone` on the over-cap path only; the ordinary two-entry map still allocates exactly once.

**4. Tests live in `internal/streamsup/result_model_window_test.go`**, not appended to the 8744-line `parser_test.go` as § Testing said. `initialize_capture_test.go` is the package's precedent for a per-feature test file, and `logRecorder` and `initCaptureDir` are same-package either way. The capture fixture needs one transformation the plan did not anticipate: the capture files store their `stdout_events` indented, and the parser splits on newlines, so `json.Compact` is applied — whitespace between tokens only, key order and every literal preserved.

**5. Open question 1 resolved.** A `contextWindow` past int64 fails the **whole map's** unmarshal rather than the single entry, so the line reports `(nil, 0)`. That is the declared-map behaviour `commandEntryLine` documents, and it is now pinned as its own row rather than assumed.

**6. Open question 2 resolved, and it is the one revision 1 came out of.** The existing keyed `turnevent.TurnEnd{Reason: …}` fixtures compared with `reflect.DeepEqual` in `parser_test.go` all still pass — nil windows equal the zero value, as predicted. What that sweep did not cover was the interface-value `==` in revision 1, which is a different shape entirely.

**Mutation testing run** (via `go test -overlay`, no worktree writes): cap 16→15 reddens three rows of the cardinality table (after revision 2; it survived before). Dropping the `slices.SortFunc` call reddens the determinism test, the cardinality table and the alias-pair capture pin. The comment on the determinism test was corrected from an overclaim — an under-cap line already catches order nondeterminism; what only an over-cap line catches is *membership* nondeterminism.
