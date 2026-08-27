# #1819 — decode each model's auto-permission-mode support onto the model list

`feat(streamsup)` · size **s** · `security-sensitive`

## Files to read first

Production:

- `internal/streamsup/parser.go` → `modelOptionLine` — the decode target this slice adds a field to,
  and the doc that promises a WHOLE-LINE failure for a mistyped value. Read the doc in full; two of
  its sentences are rewritten here.
- `internal/streamsup/parser.go` → `emitModelList` — the four rungs and the per-entry loop. The new
  field is assigned in the composite literal and does **not** go through the `bound` closure.
- `internal/streamsup/parser.go` → `maxModelResolved` — read the "THE ENVELOPE ARITHMETIC IS COMPLETE"
  and amplification paragraphs **only to confirm they do not move**. See § Caps below.
- `internal/streamsup/parser.go` → `maxModelListEntries` — same: read to confirm, not to edit.
- `internal/streamsup/parser.go` → `logControlResponse` — the four-attribute record. Nothing is added
  to it; AC 3's "no log line carries decoded content" is satisfied by leaving it alone.
- `internal/turnevent/event.go` → `ModelOption` — the daemon's type. Its omissions paragraph and the
  sentence naming the now-closed #1809 are both rewritten here.
- `internal/turnevent/event.go` → `ModelList` — the `Models` field doc's "three strings are bounded"
  clause needs one amendment (§ Doc edits).
- `internal/protocol/interactive.go` → `ModelOption` — read `SupportsAutoMode`'s shipped doc. It is
  the reading downstream already chose (#1704) and the field this one mirrors. **Not edited here.**

Tests:

- `internal/streamsup/parser_test.go` → `TestParser_InitializeControlResponseDecodesTheCapturedModels`
  — AC 1's pin. Extended, not duplicated. `capturedModelString` is the helper the bool accessor mirrors.
- `internal/streamsup/parser_test.go` → `modelListLineFixture`, `modelEntryFixture`,
  `modelEntriesFixture` — the fixture builders. `modelEntryFixture`'s three-parameter signature is
  **not** widened (§ Testing strategy).
- `internal/streamsup/parser_test.go` → `TestParser_InitializeControlResponseRejectBranches` — where
  AC 3's rows go. Its `wantAttrs` `reflect.DeepEqual` is the exact-attribute comparison that already
  makes a new log attribute red.
- `internal/streamsup/parser_test.go` → `TestParser_ModelListFieldsAreCapped` — the table idiom to
  copy (per-row `check` closure, per-entry expectations indexed against the fixture).
- `internal/streamsup/initialize_capture_test.go` → `capturedModelEntries` — read its doc: it decodes
  with **literal key strings** rather than through the production target, and says why. That idiom is
  load-bearing for AC 2's fixture guard.
- `internal/streamsup/initialize_capture_test.go` → `capturedInitializePayload` — the reader; it mints
  its own path from package constants and selects by arm from a closed set.

Read-only, for posture (do **not** edit — the documentation phase owns them):

- `docs/knowledge/features/streamsup-package.md` § "Decoding the initialize ack into
  `turnevent.ModelList` (#1811)" — the absence-is-the-zero-value posture, and the two test-writing
  lessons on this exact code (per-entry accumulator ordering; the equivalent-mutant cardinality
  boundary). Its "keeps exactly three of claude's payload keys" sentence goes stale with this slice;
  the documentation phase folds that in, not the developer.
- `internal/e2e/internal/fakeclaude/main.go` → `initializeModels` — the canned two-arm list. Its "a
  map per entry, not a struct with `omitempty`" paragraph is the clearest statement in the tree of
  why absence is a missing key. **Not edited here**: its deferral to the closed #1690 is #1822's.

## Context

`emitModelList` decodes claude's `initialize` reply into `turnevent.ModelList`. #1811 shipped the
three verbatim strings, #1812 bounded the entry count. This slice adds the first of the two
capability fields: `supportsAutoMode`, which says whether claude will accept auto permission mode for
that model, so a client's permission-mode menu can grey the option out where claude refuses it.

It is the cheap half of the capability pair. A bool carries none of claude's bytes, so no cap
constant is added and no envelope arithmetic moves; the effort-level list is the half that moves
those numbers, and it is #1820/#1821.

**The whole design question is what an absent key means, and it is answered by the field's shape.**
Measured on the committed capture (`initialize_control_v2.1.239.json`, claude 2.1.239) and
re-verified for this spec, entry by entry:

| `value` | keys | `supportsAutoMode` |
| --- | --- | --- |
| `default`, `sonnet`, `claude-fable-5[1m]` | 8 | `true` |
| `opus` | 9 | `true` |
| `haiku`, `claude-haiku-4-5` | 4 | **absent** |

Four `true`, two absent, **zero `false`** — and the same three key sets hold identically in all three
of #1763's arms. So claude omits the key rather than sending `false`, and a `bool` collapses the two
shapes while a `*bool` keeps them apart. Neither is fallen into; the choice is made below.

**No ADR is warranted.** The decision is field-specific and its reasoning belongs at the type where
#1820 will read it, not in a numbered record. The documentation phase should fold the collapse rule
and its safe-direction argument into `docs/knowledge/features/streamsup-package.md`'s model-list
section, replacing that section's now-stale "exactly three of claude's payload keys".

## Design

### The decision: a plain `bool`, collapsing absent, `null` and present-`false` into one reading

`turnevent.ModelOption` gains `SupportsAutoMode bool`. Absent, JSON `null`, and an explicit `false`
all read as `false`. This is a deliberate collapse, and four things decide it:

1. **The safe direction is asymmetric, and it points at `false`.** This field describes a permission
   *grant*. "claude refused auto for this model" and "claude said nothing about auto for this model"
   drive the **same** client behaviour: grey the option out. The two readings have no decision hanging
   between them. The collapse that would be unsafe is the other one — granting on silence — and
   nothing here does that.
2. **Downstream already collapsed, at the type, in shipped code.** `protocol.ModelOption`'s
   `SupportsAutoMode` is a plain `bool` whose doc states that absent "decodes to false, which is the
   correct reading" (#1704). A `*bool` here would be flattened at the mapping boundary in #1693
   anyway, so the pointer would preserve a distinction only long enough to discard it.
3. **The distinction has never been observed.** claude has never sent `false` — not in the base
   capture, not in either of #1763's other two arms. A pointer would defend a shape that exists
   nowhere in the bytes, and the evidence-based rule says defer that until it is seen.
4. **`turnevent` has no pointer field of this kind today** (verified: `event.go` declares none). This
   slice is not where the event vocabulary grows its first three-state field, for a field with no
   consumer asking for the third state.

**The choice is field-specific and #1820 must re-derive rather than inherit it.** Point 1 is what
carries it, and it is an argument about a *withheld grant*. An absent `supportedEffortLevels` is not
obviously a safe default in the same way — an empty effort menu is a different failure than a greyed
checkbox — so the type doc must say the reasoning, not just the answer, and must say that #1820
checks it rather than copies it.

### Production changes

**`internal/turnevent/event.go` — `ModelOption`.** One field, placed after `DisplayName` and before
`TruncatedFields`:

```go
SupportsAutoMode bool
```

The slot matters: `protocol.ModelOption`'s order is `ResolvedModel`, `Value`, `DisplayName`,
`EffortLevels`, `SupportsAutoMode`, `TruncatedFields`. Placing it there keeps the type's standing
claim — that it mirrors the wire row's declaration order — true, and leaves #1820 an obvious slot for
`EffortLevels` between `DisplayName` and this field.

Its doc paragraph is where AC 2's choice is *stated*, and it must carry, in this order: what the field
is (claude's answer, verbatim, per #1600); that absent, `null` and `false` are one reading and that
this is deliberate; the safe-direction argument (point 1) as the reason; the three supporting facts
(points 2–4) compressed; the note that it is never named in `TruncatedFields` because a bool is never
cut; and the sentence directed at #1820.

**One security sentence belongs in that paragraph too** (see § Security review, Trust boundaries):
this field is claude's *claim about itself* and is a report to a client's menu, never an
authorization input. Nothing in the daemon may branch on it to decide what to send claude — claude
already decides that when asked. A future slice reading it as permission would be letting the
subprocess authorize itself.

**`internal/streamsup/parser.go` — `modelOptionLine`.** One field:

```go
SupportsAutoMode bool `json:"supportsAutoMode"`
```

**`internal/streamsup/parser.go` — `emitModelList`.** One assignment in the existing composite
literal, after `DisplayName`, carrying a one-line comment: it does not go through `bound` because a
bool has no length to cut, carries none of claude's bytes, and is therefore never named in
`TruncatedFields`. The `bound` closure and the three `bound` calls above it are untouched.

There is **no daemon code implementing the collapse.** `encoding/json` performs it: an absent key and
a `null` both leave the field at its zero value. That is the point — in Go the choice *is* the field's
shape — and it is exactly why the doc has to carry it and why AC 2's test has to prove the two wire
shapes really are two (§ Testing strategy).

### Caps and envelope arithmetic: nothing moves, and nothing rots

`maxModelResolved`, `maxModelValue`, `maxModelDisplayName` and `maxModelListEntries` are **not
touched**, and no new constant is added. Confirm, don't edit:

- The 768-byte multiplicand "counts claude-derived **text** only". A bool retains no text. Still true.
- The `10 * 768 = 7680` product and its envelope percentages are unchanged.
- The amplification paragraph's transient figure is stated as *order* 100 MB. `modelOptionLine` grows
  from three string headers to three plus a bool — 48 to 56 bytes with padding — so a pathological
  line's transient cost rises by about a sixth and stays the same order of magnitude. The claim holds
  as written; do not restate it with a new number.
- Nothing crosses the wire either way: `turnbridge.MapEvent`'s `default` still drops `ModelList` until
  #1693, and `protocol.ModelOption.SupportsAutoMode` already exists and already serialises.

**If you find yourself editing a cap constant or an arithmetic paragraph, stop — that is #1820/#1821's
work and something has gone wrong here.**

### Doc edits (each sentence is being rewritten anyway; none is a drive-by)

1. **`modelOptionLine`'s doc — "reduced to the three keys the mapping reads … and the capability keys
   are #1809's."** Becomes four keys. The undecoded set is now `description`, `supportsEffort`,
   `supportsAdaptiveThinking` and `supportsFastMode` — name all four rather than the two the sentence
   names today, since the rewrite is happening and leaving two of them unaccounted for reads as an
   oversight. Point the remaining capability key at **#1820**, never at the closed #1809.
2. **`modelOptionLine`'s doc — "All three are plain strings … A non-string value for any of them …
   fails the WHOLE-LINE decode."** Extend to the bool: a `supportsAutoMode` that is a string, a number,
   an object or an array fails the whole-line decode identically. State the one carve-out — JSON
   `null` is documented by `encoding/json` as a no-op producing no error, so it lands as the zero
   value, which for this field is the same reading absence gets. The carve-out has always applied to
   the three strings too; saying it here is the first time it is written down.

   ⚠️ **The sentence "All three are plain strings, which is why truncateField's json.RawMessage
   exception does not reach this shape" appears in a second, unrelated doc comment — on
   `systemBackgroundTaskEntry`, with a near-identical wording. Do not `replace_all` on it.** Edit only
   the copy inside `modelOptionLine`'s doc.
3. **`emitModelList`'s per-entry paragraph** ("A per-entry field is never validated beyond its cap …
   absence is claude's to choose"): one clause noting that for the bool, what absence *reads as* is
   decided at `turnevent.ModelOption`'s type rather than here, because the shape is the decision.
4. **`ModelOption`'s doc — the omissions paragraph.** Today it names `description` and
   `supportsFastMode` as the two deliberate absences and does not mention `supportsEffort` or
   `supportsAdaptiveThinking` at all. Rewrite it to name all four, keeping the existing argument
   verbatim (no named consumer; `protocol.ModelOption` carries none of them; a field never declared
   cannot leak what a later sweep forgets to check).
5. **`ModelOption`'s doc — "The two capability keys the richer entries do carry
   (supportedEffortLevels, supportsAutoMode) are #1809's slice, not an omission."** #1809 is closed
   and split. Rewrite: `supportsAutoMode` is decoded here (#1819); `supportedEffortLevels` is
   **#1820's** slice, not an omission. Neither half may keep naming #1809.
6. **`ModelList`'s `Models` field doc — "Each entry's three strings are bounded by the producer AT
   CONSTRUCTION".** At most one clause: the bool is not bounded and needs no cap, because it carries
   none of claude's bytes. Keep the rest of that paragraph as it stands.

**Explicitly not edited:** the four shipped comments that still defer this question to the closed
#1690 (including `initializeModels`' in `fakeclaude`) — that is #1822, which answers for both
capability fields at once. `logControlResponse`'s doc may optionally gain the bool to its "no value,
no resolvedModel, no displayName" list; it is not required, because the exact-`wantAttrs`
`reflect.DeepEqual` comparisons in the tests are the deterministic net there.

## Concurrency model

Unchanged. `emitModelList` runs on `Parser.Write`'s single caller goroutine, holds no lock, spawns
nothing, and the new field is a value copied into a struct the parser does not retain. No goroutine,
no channel, no shutdown sequencing is introduced or affected.

## Error handling

The four rungs of `emitModelList` are unchanged in count, order and classification:

1. Line will not decode into the shape → `undecodable`, no event. **This rung now also catches a
   `supportsAutoMode` that is a string, number, object or array**, by exactly the mechanism that
   already catches a non-string `value`: `json.Unmarshal` returns an `*json.UnmarshalTypeError`, the
   existing `err != nil` branch fires, and the whole line is dropped. No partial list is ever emitted.
2. `subtype` is not `success` → `nak`, no event.
3. `models` absent, null, empty, or decoded empty → `ack`, no event.
4. Success and a non-empty array → one `ModelList`.

`null` is the one shape that looks like it belongs on rung 1 and does not: `encoding/json` documents
unmarshalling a JSON `null` into a non-pointer Go value as having no effect and producing no error, so
`{"supportsAutoMode": null}` decodes cleanly and the entry reads `false`. That is not a gap — it is the
collapse reaching a third spelling of the same thing — but it must be written at the type and pinned
by a test row, or a later reader reconciling AC 3's "not a bool" against the code will read it as one.

**The error text is still never logged.** `emitModelList`'s undecodable arm deliberately drops the
`err` because `encoding/json` quotes the offending input bytes into its message. A `supportsAutoMode`
type error quotes claude's bytes exactly as a `value` type error does, so that rule is load-bearing
here and must not be softened to "help debugging".

## Testing strategy

Three pins, one per AC. All run inside `make check` — `internal/streamsup` carries no build tag on any
file, and the `e2e_realclaude` tag governs that package's Go files, not its testdata.

### AC 1 — the capture pin (extend, do not add a test)

Extend `TestParser_InitializeControlResponseDecodesTheCapturedModels`, which already replays each
responding arm's real `control_response` line and compares field-for-field against the capture's own
bytes.

- A new accessor beside `capturedModelString`, mirroring its shape and its reason:
  `capturedModelBool(t, entry, key) (value, present bool)` — fatal when the key is present but not a
  bool, since a capture that changed that is a capture this decode was never proven against; `(false,
  false)` when absent.
- **A coverage guard before the comparison loop**, in the spirit of the existing key-count guard: the
  captured entries must include at least one carrying `supportsAutoMode: true` and at least one
  carrying no capability key at all. Name `sonnet` and `haiku` as the entries supplying them today —
  AC 1 names `sonnet` explicitly. Without this guard, a re-capture in which every entry carried the
  key would silently narrow what the loop proves, and the loop would stay green.
- In the existing per-entry loop, one comparison: the decoded `SupportsAutoMode` equals what
  `capturedModelBool` read off `want[i]`. Derived from the capture's bytes, never transcribed.

### AC 2 — the two shapes, proven to be two

A new table test. Each row declares **what claude's bytes carry** and **what the daemon reads**:

| row | wire shape | reads |
| --- | --- | --- |
| present and `true` | `"supportsAutoMode":true` | `true` |
| present and `false` (**hand-built** — claude never sends it) | `"supportsAutoMode":false` | `false` |
| absent | key not in the entry | `false` |
| present and `null` | `"supportsAutoMode":null` | `false` |

**The non-vacuity comes from asserting the fixture's own wire shape before asserting the decode.** With
a plain `bool`, absent and present-`false` are indistinguishable *by design* — that is the claim. A
table that only checked the decoded value would be satisfied by a fixture builder that quietly dropped
the `false` key, proving one shape twice and calling it two. So each row re-decodes the built line
with **literal key strings** — `capturedModelEntries`' idiom, and its doc says why not the production
target — and asserts the entry either carries the key with the declared raw JSON value, or does not
carry it at all. Suggested shape, one helper:

```go
// fixtureAutoMode reports what entry i of a built line ACTUALLY carries under the
// literal key, read back out of the line's own bytes.
func fixtureAutoMode(t *testing.T, line string, i int) (raw json.RawMessage, present bool)
```

Row declares a `wantWire string` (`"true"`, `"false"`, `"null"`, and `""` meaning absent); the helper's
result is compared against it before the event is inspected.

Fixture construction: **do not widen `modelEntryFixture`'s three-parameter signature** — it has many
call sites and none of them wants a fourth argument. Add a small wrapper that returns a *copy* of an
entry map carrying the key, taking `any` so the AC 3 rows can reuse it for a non-bool value.

Each row then asserts one emitted `turnevent.ModelList`, one entry, and the expected
`SupportsAutoMode`. The `true` row is the one with sole redness under a JSON-tag typo or an
always-`false` mutant; the other three pin the collapse, and their wire-shape guards are what make the
pair non-vacuous.

**Do not attempt a mutant that separates absent from present-`false`.** None exists under this design,
and that is the design's content, not a coverage gap — say so in the test's doc comment so a later
reviewer does not go looking.

### AC 3 — the undecodable rung

Rows added to the existing `TestParser_InitializeControlResponseRejectBranches` table, which already
asserts zero events, exactly one debug record, and an exact four-attribute map by `reflect.DeepEqual`:

- `supportsAutoMode` is a string (`"true"`) → `undecodable`
- `supportsAutoMode` is a number (`1`) → `undecodable`
- `supportsAutoMode` is an object or an array → `undecodable`

Two of the three are enough if the third reads as padding; the string row is the load-bearing one,
since `"true"` is the shape a hand-written client or a future claude is most likely to send.

The "no log line carries decoded content" half needs no new assertion: the table's `wantAttrs`
comparison is an exact map equality, so any attribute added to `logControlResponse` — including the
bool — turns every row red. Note in the test's doc that this is what covers it, so the coverage is
visible rather than assumed.

### What is deliberately not tested

- **A cap or truncation row for this field.** A bool has no length; `truncateField` never sees it and
  `TruncatedFields` never names it. A row asserting `TruncatedFields == nil` on an entry carrying the
  bool would be satisfied by construction and would prove nothing.
- **A wire/protocol round-trip.** Nothing publishes `ModelList` until #1693; `turnbridge.MapEvent`'s
  `default` drops the variant. `protocol.ModelOption.SupportsAutoMode` has its own shipped tests.

## Open questions

None blocking. One thing for the reviewer to weigh rather than for the developer to decide: whether
`logControlResponse`'s doc should name the bool in its "no value, no resolvedModel, no displayName"
list. The spec leaves it optional because the exact-attribute test comparisons are the deterministic
guarantee and the doc would be a second, weaker copy of it.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] MUST FIX — addressed in this spec, see § Design.** The boundary is
  `emitModelList`'s `json.Unmarshal` of the child's stdout line into `controlResponseLine` /
  `modelOptionLine`: one explicit function, one decode, claude-controlled on the input side. The new
  value is claude's own claim about itself, and it is the first field on this path whose *name* reads
  like a permission decision. The hazard is not this slice — nothing publishes it, nothing branches on
  it — but a later slice reading `SupportsAutoMode` to decide whether the daemon may *send* auto
  permission mode, which would let the supervised subprocess authorize itself. Mitigation is required
  in this spec's own deliverable: `turnevent.ModelOption.SupportsAutoMode`'s doc must state that it is
  a REPORT to a client's menu and never an authorization input. Post-decode the value is a bool over a
  closed two-element domain — no length, no encoding, no injection surface — so the boundary itself is
  sound; the risk is entirely in how a future reader reads the name. With the doc sentence required as
  part of AC 2's deliverable, this finding is closed rather than deferred.
- **[Trust boundaries] No findings — the collapse direction is the safe one.** Absent → `false`
  withholds a grant on silence. The inverse (absent → `true`) would grant one, and would be reachable
  by a claude that simply stopped sending the key. `protocol.ModelOption`'s shipped doc chose the same
  direction, so the two layers cannot disagree.
- **[Tokens, secrets, credentials] No findings.** No token, key or credential is read, minted, stored
  or compared. The decode target's *field set* is the standing guarantee here — the initialize
  payload's `account` key is never declared on `modelOptionLine` or `controlResponseLine` and this
  slice declares exactly one bool and nothing else, so `description`, `supportsEffort`,
  `supportsAdaptiveThinking`, `supportsFastMode` and `account` all remain undeclared and therefore
  unreachable by any later sweep that forgets to check them.
- **[File operations] No findings.** No production path touches the filesystem. The test reads the
  committed capture through `capturedInitialize`, which mints its path from package constants and
  selects by an arm identifier from a closed set, rejecting any other string — no caller-supplied path
  reaches it, so no traversal, TOCTOU or symlink question arises. No file is created; no mode is set.
- **[Subprocess / external command execution] No findings.** This slice writes nothing to the child.
  The request half of this exchange is `WriteInitialize`'s and is untouched; no `exec.Command`
  argument, no environment variable, no signal path is involved. Notably the decoded value never flows
  back to the child as an argument — `internal/relay`'s `validModel` remains the inbound gate for the
  one field of this family a client sends back, and this one is not sent back at all.
- **[Cryptographic primitives] Not applicable.** No randomness, no hashing, no key material, no
  comparison of an attacker-controlled value against a secret. The bool comparison in the tests is
  against a test expectation, not a credential, so constant-time comparison is not in question.
- **[Network & I/O] No findings, and the size question was checked rather than assumed.** Input size
  is bounded three ways before and after this change, none of which moves: `defaultMaxParseBuf` caps
  the line at 4 MiB before the decoder sees it; `maxModelResolved`/`maxModelValue`/`maxModelDisplayName`
  bound the retained text per entry; `maxModelListEntries` bounds the count. A bool retains no
  claude-derived text, so the 768-byte multiplicand and the `10 * 768` product are unchanged. The one
  quantity that does move is the pre-cap transient: `modelOptionLine` grows 48 → 56 bytes, so a
  pathological 4 MiB line's transient materialisation rises by about a sixth, which stays inside the
  "order 100 MB" the existing amplification paragraph states and inside the same three facts that
  bound it. No timeout, deadline, TLS or connection-count surface is touched.
- **[Error messages, logs, telemetry] No findings.** `logControlResponse` is unchanged at four
  attributes, none of which carries claude's bytes. The new field is never logged on any rung. The
  `undecodable` arm's deliberate discarding of the `json.Unmarshal` error stays — that error quotes
  the offending input, so a `supportsAutoMode` type error would route claude's own bytes into the
  daemon log through a channel no per-attribute check can see, and the spec says so explicitly so a
  developer does not "improve" it. The exact-map `wantAttrs` comparisons make any new attribute red
  deterministically, which is the code-level net behind the prose rule.
- **[Concurrency] No findings.** No lock is taken, no shared state is read or mutated, no goroutine is
  spawned. `emitModelList` runs on the single `Parser.Write` caller goroutine and the new field is a
  value copied into a struct the parser does not retain, so there is no partial state to recover after
  a mid-write signal.
- **[Threat model alignment] No findings for this slice; one named boundary.** The relevant threat is
  the subprocess-output trust boundary, covered above. Client-side rendering of this list is out of
  scope and stays where `protocol.ModelOption`'s SECURITY paragraph puts it: the three strings are
  untrusted, model-influenced text the daemon bounds but does not sanitize, and the render boundary
  owing the sanitization is the client's. This slice adds no string, so it widens that surface by
  nothing. Publication to a client is #1693's, and the provenance question `emitModelList`'s doc
  defers (shape discriminant rather than a correlated `request_id`) is explicitly deferred there too —
  this field does not make it more urgent, because a forged inventory can already misstate every
  string in the list.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-27
