# #2249 — carry claude's `utilization` reading on the `rate_limited` frame

Split from #2194. Blocks #2250 (forwarding the benign `allowed` reading so a client can
clear a warning), which is why this slice leaves `emitRateLimit`'s three-rung gate exactly
as it stands.

## Files read

- `internal/streamsup/parser.go` → `rateLimitInfo`, `rateLimitEventLine`, `emitRateLimit`,
  `maxRateLimitField`, `benignRateLimitStatus` — the decode target, the gate, and four of the
  doc blocks this capture falsifies.
- `internal/streamsup/parser.go` → `compactMetadata`, `systemCompactBoundaryLine` — #2237's
  optional-numeric precedent, pointer-on-the-decode-target, in the same file.
- `internal/turnevent/event.go` → `RateLimited` and its four field docs — the event shape, and
  `ResetsAt`'s stated unvalidated-in-both-directions rule, which AC 4 extends to this field.
- `internal/turnbridge/outbound.go` → `MapEvent`'s `turnevent.RateLimited` arm, and its
  `turnevent.CompactionBoundary` arm — the latter states the pointers-cross-as-pointers and
  no-defensive-copy rules this arm now needs too.
- `internal/protocol/interactive.go` → `RateLimitedPayload` — the wire shape and its SECURITY
  paragraph, plus `CompactionBoundaryPayload` for the `*int` precedent.
- `internal/protocol/interactive_test.go` → `TestRateLimitedPayload_RoundTrip`,
  `TestRateLimitedPayload_ZeroValue_RoundTrip`, `TestRateLimitedType_IsNotClaudesVocabulary`,
  `TestCompactionBoundaryPayload_AbsentCountIsNullNotZero`, `countOrNull` — the two golden
  round trips to extend, the claude-key enumeration that must NOT grow, and #2237's
  wire-half shape for the absent-versus-zero claim.
- `internal/streamsup/initialize_capture_test.go` → `initCaptureRecord`, `capturedInitialize`,
  `initCaptureArms`, `initCapturePath` — the existing reader for this capture family. Its
  record doc names `stdout_events` as deliberately omitted and gives the reason to weigh;
  § Design records how that was weighed.
- `internal/streamsup/permission_denial_capture_test.go` → `capturedDenialLines`,
  `replayDenialCapture` — #2234's replay shape, including the `json.Compact` argument for an
  indent-encoded record and the one-parser-per-replay correction.
- `internal/streamsup/compaction_capture_test.go` → the `compactionCapturePath` docblock — the
  one-reader-per-capture-family convention, and `compactionPinnedShapes`' CORRECTED note, which
  is the precedent for not putting a re-rotting count in a comment.
- `internal/streamsup/capture_test.go` → `capturedLines`, `capturePath` — the reader that
  already serves `rate_limit_event` from the *dropped-lines* family (benign only), which is why
  the warning capture genuinely has no reader.
- `internal/streamsup/parser_test.go` → `rateLimitLineFixture`, `rateLimitEvent`,
  `TestParser_RateLimitEmitsForNonBenignStatus`, `TestParser_RateLimitSilentRungsLogTheirReason`,
  `TestParser_RateLimitFieldCaps` — where the hermetic rows go and the fixture builder they use.
- `internal/turnbridge/outbound_test.go` → `TestMapEventOutbound`'s `RateLimited` rows and
  `TestMapEventRateLimitedTruncatedFieldsOnTheWire` — the two halves of the arm's proof. The
  table compares with `reflect.DeepEqual` over separately-allocated values, so a pointer field
  compares by pointee and no existing row breaks.
- `internal/e2e/relay_v2_stream_rate_limit_test.go` → `driveRateLimitTurn`, and
  `internal/e2e/realclaude/interactive_stream_liveness_test.go`'s single
  `protocol.RateLimitedPayload` decode — the two places outside the four production files that
  hold this payload. Both assert field-by-field, neither compares a whole struct literal, so a
  new field is transparent to both. This is the check the ticket's "no live-claude run" claim
  rests on, re-derived rather than taken on the ticket's word.
- `docs/protocol-mobile.md` § `rate_limited` — already current on the `allowed_warning`
  observation and on both observed `limit_type` values; the model for every corrected sentence.
- `docs/knowledge/features/protocol-package-rate-limited-event-payload.md` — the payload's
  package overview. Two lessons it carries shape this slice: the zero-value fixture is the
  *enforcement mechanism* for the no-`MarshalJSON` decision (so a field whose absence must be
  `null` belongs in that fixture, not only in a new one), and there is deliberately no
  envelope-cap test because the arithmetic has two orders of magnitude of headroom — a ~20-byte
  number does not change that.

## Context

`emitRateLimit` decodes claude's `rate_limit_event` into `rateLimitInfo`, which declares three
keys. `utilization` is not one, so it is discarded at the decode target and exists nowhere in
the daemon. A client receiving the frame knows a limit exists but not how close it is, which is
the one thing a user could act on.

The evidence is committed, not documented-only:
`internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` carries the single
`allowed_warning` reading on record, with `"utilization": 0.94` beside
`"rateLimitType": "seven_day"`.

**The census, re-derived at `c2626d5b` rather than taken from the ticket.** Every committed
`rate_limit_event` record across four claude versions (2.1.158, 2.1.199, 2.1.220, 2.1.239):

| Records | `status` | `rateLimitType` | `utilization` |
|---|---|---|---|
| 24 | `allowed` | `five_hour` | key absent |
| 1 | `allowed_warning` | `seven_day` | `0.94` |

Absence is therefore the measured majority case by a wide margin, and a wire form that could
not tell "claude reported nothing" from "claude reported 0" would render a fresh window as an
exhausted one. That settles the pointer.

It also shows why the doc sweep is larger than a count fix: the prose in three production files
says "three captures", and the corpus is 25 records. § Doc sweep states the shape of each
correction.

No ADR is warranted. The absent-versus-zero answer is #2237's, three weeks old and in the same
file; this is that precedent applied, not a new decision.

## Design

One optional number carried verbatim across the existing four layers. No new type, no new
exported symbol, no signature change, no gate change.

### The field, at each layer

| Layer | Symbol | Declaration |
|---|---|---|
| decode target | `rateLimitInfo` | `Utilization *float64` \| `json:"utilization"` |
| event | `turnevent.RateLimited` | `Utilization *float64` |
| wire | `protocol.RateLimitedPayload` | `Utilization *float64` \| `json:"utilization"` |

**`*float64`, and each half of that is a separate decision.**

*Pointer*, because absence and an explicit `0` are different facts a client renders differently
— #2237's `compactMetadata.PreTokens`/`PostTokens` reasoning, and the inverse of the
plain-`int64` choice `ResetsAt` makes. `ResetsAt` can collapse the two because `0` there is
already defined as "not reported"; a utilization of `0` is a *meaningful* reading (a fresh
window) and cannot absorb absence.

*float64*, because the observed value is fractional. Not a bounded-fraction type and not a
percentage-int: claude's number is carried as claude sent it, and inventing a 0–1 domain would
be a validation rule with no captured negative case behind it.

**Wire name `utilization` coincides with claude's spelling**, the rule `RateLimitedPayload`'s doc
already states for `status`: it is the daemon's chosen name for the field, and a generic English
word rather than a vocabulary import. `TestRateLimitedType_IsNotClaudesVocabulary` enumerates the
claude keys that must never appear on the wire; `utilization` is not one of them and is **not**
added to that list.

**Not a `TruncatedFields` member, and no `bound()` call.** The report names fields the producer
*cut*, and a float cannot be cut — `ResetsAt`'s stated reason, unchanged. `maxRateLimitField`
gains no second duty.

**`surpassedThreshold`, `isUsingOverage` and the overage keys stay out of the decode target.**
Nothing asks for them, and `rateLimitEventLine`'s doc gives the standing reason: absent from the
decode target is stronger than a scrub, because a field never declared cannot leak.

**No cap, and the reason is the type rather than a measurement.** `maxRateLimitField` bounds the
two strings because a string claude chooses can be arbitrarily long. A `float64` cannot: its
shortest round-trip encoding is bounded by the type, worst case 39 bytes including the key
(measured on `1.7976931348623157e+308`), against the 65519-byte v2 envelope cap. So the
amplification clause in `maxRateLimitField`'s doc gains this field as a second no-term
contributor beside `ResetsAt`, and nobody should later add a cap for it.

### Data flow

`emitRateLimit` copies the pointer to the event verbatim, beside `ResetsAt`, under the same
"unbounded and unvalidated in both directions" comment. `MapEvent`'s arm copies it to the payload
verbatim. Neither clamps, range-checks, rounds nor reformats.

**The pointer crosses as a pointer and is not deep-copied** — the `CompactionBoundary` arm's
stated terms, which apply here for the same three reasons: `encoding/json` allocates a fresh
`float64` per line, the parser retains nothing after `emit`, and nothing downstream mutates a
payload. A defensive copy would only obscure that.

### The replay reader — and the alternative rejected

The capture's `stdout_events` has no reader. `initialize_capture_test.go` owns this capture
family for its control replies and its `initCaptureRecord` lists `stdout_events` among the keys
deliberately not restated.

**Decision: extend `initCaptureRecord` with `StdoutEvents []json.RawMessage` and put the replay
in a new file, `internal/streamsup/rate_limit_capture_test.go`, riding `capturedInitialize`.**

The rejected alternative was a fifth self-contained reader in #2234's exact shape — own
constants, own path minting, own provenance checks. Two things rule it out:

1. The convention's own property is that **no reader can decode another's record shape**, so
   reaching for the wrong one yields zero values rather than a failure. A second reader over the
   *same* record shape breaks that property rather than extending it.
2. `initCaptureRecord`'s doc names the risk it already carries — a parallel struct of
   `initControlFixtureRecord`, where a field rename lands as a silent zero value — and accepts it
   with the surface minimised. A second parallel struct over the same record doubles exactly
   that risk for nothing.

What the convention actually forbids is growing a reader a **path parameter**, because the
provenance assertions are what stop a hand-built file being swapped in behind them. This adds no
path parameter: `capturedInitialize` keeps its closed arm selector and mints its own path, and
the new helper inherits every check rather than copying a weaker set. The reader's own file doc
says it exists so the provenance discipline is written once while several decode slices ride it;
this is the fifth such rider.

`stdout_events` is present in all four committed arms (verified), so adding it does not break the
"every field is in the subset all four records share" property that doc rests on.

### Replay shape

`capturedInitializeStdoutLines(t, arm) []json.RawMessage` in the new file: calls
`capturedInitialize(t, arm)`, `json.Compact`s each entry, fatals on an empty `stdout_events`.

- `json.Compact` for #2234's reason: the record is written by an indenting encoder, the parser is
  line-oriented, and an un-compacted entry would arrive as a dozen undecodable lines. Compaction
  removes only the *recorder's* insignificant whitespace — key order and every value byte
  survive, which keeps this a replay of claude's line.
- **One parser per arm**, not one per line — #2234's correction. Production builds one parser per
  session, and a per-line parser cannot express cross-line state.
- Every failure is `t.Fatalf`, never a skip: the captures are committed, so absence is a broken
  premise and a skip would report a deleted fixture as a green run.

## Concurrency model

No goroutines, no locks, no shared state. `emitRateLimit` runs on the parser's existing
line-consuming goroutine and `MapEvent` is pure. The one concurrency-adjacent property is the
no-defensive-copy rule above, and it is safe for the reasons § Data flow states rather than by
luck: unlike `turnevent.ModelList` — whose slice headers are retained across two goroutines by
`sessionModelHold` — nothing retains a `RateLimited` past the emit.

## Error handling

No new failure mode and no new drop reason — AC 4.

| Input | Outcome |
|---|---|
| key absent | `Utilization == nil`; event still fires on the gate's verdict |
| `"utilization": null` | `nil` too — claude stating nothing and claude stating `null` are one reading, and no consumer answers them differently |
| `"utilization": 0` | non-nil pointer to `0`; crosses to the wire as `0` |
| `"utilization": -3.5` or `17` | crosses **verbatim**; not clamped, not rejected |
| `"utilization": "0.94"` (or any non-numeric) | whole-line decode fails → `emitRateLimit`'s existing undecodable drop, `reason` unchanged, no event |
| `"utilization": 1e400` (outside `float64`) | the same undecodable drop — `encoding/json` refuses the conversion rather than saturating |

The last two rows are the mechanism `rateLimitInfo`'s doc already states for a non-numeric
`resetsAt`, for a `resetsAt` too large for `int64`, and `systemTaskUpdatedLine.TaskID` for a
numeric task id; both were verified against `encoding/json` rather than assumed. Notably there is
no saturating path: an out-of-range number cannot arrive as `+Inf`, and JSON has no `NaN` or
`Inf` literal at all, so no non-finite reading is representable on the wire and no guard for one
is owed.

The gate's three rungs are untouched, and `emitRateLimit`'s content-free logging rule is untouched:
the reading never reaches a log field. A number is the value a drop site is *most* tempted to
explain itself with, so that rule is asserted rather than assumed — see § Testing strategy.

## Doc sweep (bounded)

Only sentences this capture **falsifies**, re-grepped at `c2626d5b` for `three captures`,
`five_hour` and `UNMEASURED` across the three named files. Nine sentences plus the two new field
docs. `docs/protocol-mobile.md` § `rate_limited` is already correct and is the wording model;
`internal/turnbridge/outbound.go` has no hit.

- `internal/streamsup/parser.go`: `maxRateLimitField`'s observed-value list and its
  "value set is UNMEASURED" clause; `benignRateLimitStatus`'s "in all three captures";
  `rateLimitEventLine`'s version-variable count; `rateLimitInfo`'s "all three present in all
  three captures"; `emitRateLimit`'s rung-2 "status read allowed in all three captures" and
  rung-3 "a condition that has never fired once in three captures".
- `internal/turnevent/event.go`: `RateLimited`'s own "all three captures" clause, `Status`'s
  UNMEASURED claim, `LimitType`'s `"five_hour"` claim.
- `internal/protocol/interactive.go`: `RateLimitedPayload`'s UNMEASURED claim and its
  `"five_hour"` claim.

**Two doc edits the sweep does not cover, added by the security review and load-bearing.**

1. `RateLimitedPayload`'s **SECURITY paragraph** names `ResetsAt` as *the* claude-authored number
   that is unvalidated in both directions. Leaving it that way while adding a second such number
   tells a client that `Utilization` **is** validated — a bounded 0–1 fraction it can scale a
   progress bar by. It is not. The paragraph must name both, and the new field's own doc must say
   the same in the place a reader meets it. The same addition goes to
   `turnevent.RateLimited`'s type doc, whose "every field is claude-authored text or a
   claude-authored integer" enumeration no longer covers the field set.
2. `turnevent.RateLimited`'s doc already warns that a slice wanting the daemon to **act** on a
   rate limit is re-opening its trust analysis, not extending it. A *number* makes that invitation
   materially stronger than a status string did — a threshold is the obvious thing to branch on —
   so the report-never-a-control-input constraint is restated at the new field rather than left to
   the type doc one screen above.

**Corrections carry no new tally.** `compactionPinnedShapes`' CORRECTED note is the precedent: it
removed a count that was stale within a ticket of being written and added no replacement. So
"in all three captures" becomes a statement of *shape* — the benign value in every committed
record but one, the exception being the warning band — which the new replay test pins, rather than
a number that rots on the next capture. `Status`'s and the payload's UNMEASURED claims become
§ `rate_limited`'s own wording: almost entirely unmeasured, exactly one non-benign value on
record, **no capture of a limit actually in force exists** — that last clause is still true and
must survive the edit, because the rung-3 rationale rests on it.

The one test comment corrected is `TestRateLimitedPayload_RoundTrip`'s justification for the
`<unmeasured>` sentinel, which asserts every capture reads `allowed`. The fixture's *choice*
stands — `<unmeasured>` is still a value no capture carries and the angle brackets are a
load-bearing escaping pin — only its reason needs restating.

`docs/protocol-mobile.md` § `rate_limited` gains the `utilization` field row (claude's own number,
unvalidated, not a bounded fraction, `null` when claude reported none), one short paragraph for
the absent-versus-`0` rule, and a changelog entry. No other prose is re-audited.

## Testing strategy

RED before GREEN at each layer. Scenarios, not bodies.

**`internal/streamsup/rate_limit_capture_test.go`** (new) — AC 1, over all four committed arms:

- The base arm's replay yields exactly one `turnevent.RateLimited` carrying `Utilization` 0.94,
  with `Status` `allowed_warning` and `LimitType` `seven_day`. All three wanted values are
  **re-derived from the captured line's own bytes**, not from a literal, so the assertion measures
  claude's record rather than agreeing with a pin someone edited.
- The three sibling arms yield **zero** events: each carries a benign `allowed` reading and no
  `utilization` key at all. This is both the absence-is-the-measured-case evidence and the
  non-vacuity control in `denialCapturePinnedCounts`' `control_bypass` shape — without it, a
  mapping that emitted nothing would pass the base-arm count only if that pin were also zero.
- A sweep asserting no arm's replay produces a `turnevent.Unrecognized`, so the type stays off
  the surfaced lane.

**`internal/streamsup/parser_test.go`** — AC 4, hermetic rows beside the existing rate-limit
tables, the absent and explicit-zero rows **adjacent** for
`TestParser_CompactBoundaryPublishesTriggerAndCounts`' reason: a plain `float64` must not be able
to pass one while failing the other.

- no `utilization` key → `nil`.
- `"utilization":0` → non-nil, `0`.
- `"utilization":null` → `nil`, the same reading as an absent key.
- `-3.5` and `17.25` → verbatim, proving no clamp and no rejection.
- `"utilization":"0.94"` and `"utilization":1e400` → no event, and the existing undecodable
  `reason` unchanged. The second row is the out-of-`float64` case, which must not saturate.
- `utilization` never appears in any logged field (extends the content-free assertion). Asserted
  on a value chosen so a substring search is discriminating, not on `0.94`, whose digits could
  occur in an unrelated field.

**`internal/turnbridge/outbound_test.go`** — AC 2's mapping half. New rows on both existing
tables: a populated pointer crossing verbatim, and a byte-level row pinning `"utilization":null`
for nil and `"utilization":0` for an explicit zero, with each as the other's `notWant`. Needles
are full `"key":value` pairs, that table's standing rule.

**`internal/protocol/interactive_test.go`** + fixtures — AC 2's wire half and AC 3:

- `rate_limited.json` gains `"utilization":0.94` — the captured value, so the fixture a client
  author copies is measured; `TestRateLimitedPayload_RoundTrip` asserts it.
- `rate_limited_zero.json` gains `"utilization":null`, with a byte-level fixture guard beside the
  existing `"resets_at":0` and `"truncated_fields":null` ones. This is the no-`omitempty` claim:
  an `omitempty` added later reddens here.
- `TestRateLimitedPayload_AbsentUtilizationIsNullNotZero` — #2237's wire-half shape. Asserts the
  nil pointer encodes to `null`, that a pointer to `0` encodes to `0`, and that the two byte
  strings **differ**, which is AC 3 made executable. A `utilOrNull` helper keeps nil and zero
  visibly apart in failure messages, `countOrNull`'s reason.

Both existing golden round trips are byte-compared after `json.Compact`, so the fixture edits must
place the new key where the struct declares it.

**Not run and not touched:** `internal/e2e/realclaude`, `fakeclaude`'s `writeRateLimitEvent`
rider, and `internal/e2e/relay_v2_stream_rate_limit_test.go`. The proof is committed bytes plus
hermetic paths. The live drain's single `RateLimitedPayload` decode asserts field-by-field and
compares no struct literal, so the new field is transparent to it — verified rather than assumed.

Gate: `go test -race` on the four touched packages, `go vet ./...`, `go build ./cmd/pyry`, and
`make cite-guard`. The full-module race suite is the verifier's gate.

## Open questions

1. **Does the populated golden fixture switch to the captured `allowed_warning`/`seven_day`
   values?** Resolved as no, during planning: `<unmeasured>`'s angle brackets are an HTML-escaping
   pin the package overview calls load-bearing, and the captured values are already pinned where
   they were measured. Only the *stated reason* for the sentinel is corrected.
2. **Does AC 3 need a third golden fixture carrying an explicit `0`?** Resolved as no: AC 2 asks
   the fixtures to pin the populated and zero-value round trips, which the two existing files now
   do, and the absent-versus-`0` *distinguishability* is a marshalling property one test asserts
   directly. Revisit only if the encoding turns out to need fixture bytes to pin.
3. Whether `utilization` should appear in `docs/protocol-mobile.md`'s frame-count sentences —
   expected no, since no `####` heading is added. To confirm against the section while editing.

## Sizing

Measured against the one-ticket boundary: **4 production source files** (≤5), **0 new exported
types** (≤5), **0 call sites needing simultaneous update** — the field is additive, every
construction site across the repo is a keyed literal, and `reflect.DeepEqual` in the turnbridge
table compares pointers by pointee — **4 acceptance criteria** (≤5), **3 reject branches**,
unchanged.

**Total written work is over the 800-line ceiling, deliberately, and the overage is stated rather
than argued away.** The refiner estimated ~950; this plan's sketch lands nearer 850. The only cut
available is declaring the payload field in one ticket and producing it in the next, which leaves
a child whose single deliverable is consumed by its one sibling — the floor the sizing guide puts
*above* the ceiling. Per that rule the merged ticket is built and the overage recorded here.
#2249 already has a parent (#2194), so a split would also be weighed against the depth cap.

## Security review

**Verdict:** PASS (first pass FAILED on one MUST FIX, revised inline before commit, re-walked)

**Findings:**

- [Trust boundaries] **No findings, and the reason is structural rather than careful coding.** The
  boundary is one function: `emitRateLimit` unmarshals from `line`, the **top-level** bytes.
  `streamLine`'s doc states the property that preserves — control shapes are read from the top
  level only and nested content is never re-scanned — which is what stops a tool result whose text
  is literally `{"type":"rate_limit_event",...}` from forging a usage-limit report out of claude's
  own tool output. This field inherits it by decoding from the same parameter. Worth naming rather
  than assuming: a forged **number** is a better attack than a forged status string, because
  `utilization: 0.99` is directly alarming to a user and a status string is an opaque label. A
  later slice that decoded this key from anywhere but `line` would make quota panic forgeable.
- [Trust boundaries] **MUST FIX — FIXED IN REVISION.** `RateLimitedPayload`'s SECURITY paragraph
  names `ResetsAt` as *the* claude-authored number that is unvalidated in both directions, and
  `turnevent.RateLimited`'s type doc enumerates its fields as "claude-authored text or a
  claude-authored integer". Adding a second unvalidated number while both still read that way
  tells a client the new one **is** validated — a bounded 0–1 fraction safe to scale a progress bar
  by — which is the single most likely consumer bug this slice could ship. § Doc sweep now
  prescribes both edits plus the statement at the field itself. Without them the field is
  defensible code behind misleading documentation, which for a wire contract is the same defect.
- [Threat model alignment] **SHOULD FIX — folded into § Doc sweep.** `turnevent.RateLimited`'s doc
  warns that a slice wanting the daemon to *act* on a rate limit re-opens its trust analysis. A
  number strengthens that invitation materially: a threshold is the obvious thing to branch on, and
  a hostile `utilization` would then drive daemon behaviour rather than one misleading row. The
  report-never-a-control-input constraint is restated at the new field. The neighbouring gate
  change is **#2250** and is explicitly out of scope here.
- [Network & I/O] **No findings.** No new cap is owed and the reason is the type, not a
  measurement: a `float64`'s shortest round-trip encoding is bounded (worst case 39 bytes including
  the key, measured), against a 65519-byte envelope cap with two orders of magnitude of headroom.
  `maxRateLimitField` correctly gains no second duty. Verified against `encoding/json` rather than
  reasoned: a non-numeric value and an out-of-`float64` number both fail the whole-line decode and
  take the existing undecodable drop, and neither saturates to `±Inf`; JSON has no `NaN`/`Inf`
  literal, so no non-finite reading is representable and no guard for one is owed. Amplification
  stays near zero — the decode target holds scalars and no array.
- [Error messages, logs, telemetry] **No findings.** `emitRateLimit`'s rule is that nothing from the
  payload is logged on any path, the `reason` coming from a closed keyword set. A number is the
  value a drop site is most tempted to explain itself with, so § Testing strategy asserts the
  absence rather than trusting the convention, on a discriminating value rather than on `0.94`.
- [Concurrency] **No findings, verified rather than argued.** The pointer crosses without a
  defensive copy, which is safe only if nothing retains the event: `encoding/json` allocates a fresh
  `float64` per line, `cmd/pyry`'s `turnevent.RateLimited` handler arm calls `flushDelta` then
  `emitMapped` and stashes nothing, and the one retaining consumer in this family
  (`sessionModelHold`, which holds `ModelList` slice headers across two goroutines) does not touch
  this variant. No goroutine is added; no lock is taken.
- [Tokens, secrets, credentials] **Not applicable, and the decision is about the recipient rather
  than the value.** No token, key or credential is read, written or compared. The reading *is*
  account usage data, so the question is disclosure: it reaches only a phone whose `interactive`
  capability was echoed in `hello_ack`, over the same Noise-encrypted relay leg that already
  carries this frame's `status` and `resets_at` for the same account. No new recipient and no new
  channel, so no new consent question.
- [File operations] **Not applicable by construction.** The only filesystem access is the test
  reader's read of a committed fixture. The path is minted by `initCapturePath` from package
  constants; the parameter is a closed arm selector, never a path, so no caller-supplied string
  reaches a filesystem call and traversal is unreachable. No writes, no modes, no symlink
  handling, no TOCTOU — nothing is created.
- [Subprocess / external command execution] **Not applicable.** No `exec.Command` change, no new
  argument, no new environment variable, no signal-handling change. The slice reads bytes claude
  already wrote.
- [Cryptographic primitives] **Not applicable.** No randomness, no key material, no hashing. The
  only comparison on this path is `benignRateLimitStatus`'s byte-exact equality, untouched, and it
  compares claude's status against a public constant rather than a secret — so
  `subtle.ConstantTimeCompare` has nothing to protect here.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-09

## Revisions

### 2026-09-09 — implementation

**Open question 3 resolved: no frame-count sentence moves.** Those sentences count `####`
headings from § `turn_state` through § `model_announced`, and this slice adds a field to an
existing section rather than a heading. Checked against the section while editing; the four
counting sentences #2233 corrected to "seventeen" are untouched.

**Open questions 1 and 2 were resolved during planning and the implementation held to both.**
The populated fixture keeps the `<unmeasured>` status sentinel with its stated reason corrected,
and no third golden fixture was added.

**One departure from the plan's stated shape, no design change.** The plan's testing strategy
implied a generic pointer helper for the parser table; the implementation uses addressable locals
instead, which is `TestParser_CompactBoundaryPublishesTriggerAndCounts`' idiom in that same
package for that same absent-versus-present table. The turnbridge rows use fresh-allocation
closures rather than shared variables, because that table's standing rule is that `ev` and
`wantPayload` hold separately-allocated values — with a pointer field, sharing one variable would
make `reflect.DeepEqual` pass on the fact that both sides name it rather than on the pointee.

**Three mutants were run against the finished tests via `go test -overlay`, and all three were
killed.** This is the evidence that the absent-versus-zero pair is load-bearing rather than
decorative, and it is recorded because the pointer is the whole ticket:

| Mutant | What it did | Killed by |
|---|---|---|
| A | dropped the producer's pass-through (`Utilization: nil`) | the four value rows of the parser table, and the capture replay |
| B | collapsed absence into zero, the plain-`float64` equivalent | the absent and explicit-null rows only — the two a value type passes |
| C | added `omitempty` to the wire tag | the zero-value round trip and the absent-versus-zero encoding test |

Mutant B is the important one: it is the shape a reviewer would propose as a simplification, and
it passes every assertion except the pair this ticket exists for.

**Beyond the sweep as planned**, two paragraphs were corrected that the grep for `three captures`
/ `five_hour` / `UNMEASURED` did not surface but this field falsifies: `maxRateLimitField`'s
amplification arithmetic (now naming two no-term contributors and the float's bounded encoding),
and `rateLimitEventLine`'s omission list (`surpassedThreshold`, which the warning capture carries
beside the reading this target now reads, is named as deliberately left out).

**Verified rather than assumed, since the ticket forbids touching it:** `internal/e2e/realclaude`
still compiles under its build tag, and the hermetic `internal/e2e` rate-limit round trip through
a real daemon to a fake phone passes with the new field present. Neither was edited.

**The size estimate was wrong and the actual measurement is recorded here rather than left for
someone to re-derive.** § Sizing predicted ~850 lines against the refiner's ~950. The measured
total is **1431 added lines**: 430 in the `spec` commit plus 1001 in the `feat` commit. The gap is
almost entirely test lines, and the three largest items say where it went — the capture replay at
331, the parser's two new tables at 221, and the wire tests at 113. **Every one of the six
structural boundaries held** (4 production files, 0 new exported types, 0 simultaneous call-site
updates, 4 criteria, 3 reject branches), which is the useful calibration finding: the line ceiling
was the only line exceeded, and it was exceeded by the proof rather than by the change. A sizing
pass that wants to predict this shape should count the replay reader as a deliverable of its own,
since it is 23% of the diff and is a test-only artifact no production boundary counts.
