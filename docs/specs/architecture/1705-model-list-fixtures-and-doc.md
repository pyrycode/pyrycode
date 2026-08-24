# #1705 — Pin the model-list encoding with fixtures, publish it in `docs/protocol-mobile.md`

Turns #1704's declaration into a contract a client author can consume: the committed bytes and the prose.
**No production source files.** The deliverable is three `testdata` fixtures, three round-trip tests in
`internal/protocol/interactive_test.go`, and the `docs/protocol-mobile.md` edit.

## Files to read first

| Read | Symbol / section | What to extract |
|---|---|---|
| `internal/protocol/interactive.go` | `ModelListPayload`, `ModelOption` | Field **declaration order** — it is the fixture's key order (see § Byte-equality rules). |
| `internal/protocol/interactive.go` | `ModelListPayload.MarshalJSON`, `ModelOption.MarshalJSON` | The three list encodings, and *why* `TruncatedFields` is exempt. Both sit between the value and the bytes. |
| `internal/protocol/interactive.go` | `ModelOption`'s doc comment | The SECURITY paragraph and the `Value` paragraph — the doc prose paraphrases these, it does not invent new claims. |
| `internal/protocol/envelope_test.go` | `canonical`, `readFixture` | `canonical` is `json.Compact` only: it neither sorts keys nor changes escaping. Both facts are load-bearing. |
| `internal/protocol/interactive_test.go` | `TestModelAnnouncedPayload_RoundTrip`, `TestModelAnnouncedPayload_ZeroValue_RoundTrip` | The shape all three new tests copy, **including** the comment that separates a measured value from a chosen one, and the zero fixture's byte-level guards. |
| `internal/protocol/interactive_test.go` | `TestBackgroundTaskRosterPayload_RoundTrip`, `TestBackgroundTaskRosterPayload_Empty_RoundTrip` | The two-entry populated/`null` `truncated_fields` pattern, and the empty-frame `bytes.Contains` guard. |
| `internal/protocol/interactive_test.go` | `TestModelListPayload_NilModelsNormalises`, `TestModelOption_NilSliceEncodings` | The three keys already covered. Do not duplicate their coverage, and **do not modify either test** — in particular `TestModelOption_NilSliceEncodings`'s trailing `p.Models[0].EffortLevels != nil` assertion, which code review's own mutant matrix measured as the *only* thing that catches an entry marshaller normalising in place (that mutant produces byte-identical JSON, so no round trip can see it). It reads like a tidy-up candidate and is not one. |
| `internal/protocol/testdata/background_task_roster.json`, `background_task_roster_empty.json` | — | AC 1's two named precedents, and the `<` / `&` escaping a fixture must reproduce. |
| `internal/protocol/testdata/model_announced_zero.json` | — | AC 1's third named precedent: a zero-**payload** frame inside a normal envelope. |
| `internal/protocol/testdata/rate_limited.json` | — | The `<unmeasured>` sentinel for a field whose value set no capture reports. This spec reuses it. |
| `docs/protocol-mobile.md` | § Application message types | The registry-row format; the `model_announced` and `session_transition` rows. |
| `docs/protocol-mobile.md` | § Interactive events (v2, capability-gated), opener paragraph | Count site #1 ("These **fifteen** envelope types…") and its defining clause. |
| `docs/protocol-mobile.md` | `#### model_announced` | The prose model to follow, and the paragraph that gains the back-link. |
| `docs/protocol-mobile.md` | `#### session_transition` | Count site #2, the not-one-of-the-fifteen phrasing to mirror, and the insertion point (immediately after it). |
| `docs/protocol-mobile.md` | `#### background_task_roster` | The nested "Each element of `tasks`:" two-table format the field tables copy. |
| `docs/knowledge/features/protocol-package.md` | § Model-list payload (#1704) | Records an **unfixed SHOULD FIX**: `TruncatedFields`'s doc enumerates 2 of the 3 cut-able text fields. Do not propagate it into the doc — see § Open questions. |
| `docs/knowledge/features/protocol-package.md` | § Session-transition payload (#656) | The house rule: struct field order == fixture `payload` key order == doc-table row order. |
| `internal/relay/v2session_settings.go` | `validModel`, `validEffort` | The exact inbound rule the doc's property 3 states. **Read only — neither is changed here or as a drive-by.** |

## Context

#1704 declared `TypeModelList`, `ModelListPayload` and `ModelOption` and deliberately shipped no fixtures and no
`docs/protocol-mobile.md` section. A client author (pyrycode-desktop#561, blocked since 2026-08-19; #682 for the
`supports_auto_mode` flag) currently has to read Go structs to write a decoder. This slice closes that: the bytes and the
prose land together, as #1405 (`772b822`) and #1616 (`c9d35e9`) each did in one feature commit.

The encoding gap is specific and measured. Of the nine wire keys, #1704 already reddens the three **list** keys
(`models`, `effort_levels`, `truncated_fields`) by constructing values directly. The six **scalar** keys are uncovered,
and four of them live on `ModelOption`, which a frame carrying no entries cannot reach at all. That is what forces three
fixtures rather than `model_announced`'s two.

No ADR is warranted. Every decision here is a restatement of a settled #1704 choice or a fixture-authoring convention the
package already has precedents for.

## Design

### 1. The three fixtures

All three are single-line JSON under `internal/protocol/testdata/`, envelope type `model_list`, envelope ids **714 /
715 / 716** (712 and 713 are the `model_announced` pair; 714–716 are free).

#### Byte-equality rules — read before authoring

`roundTripEnvelope` compares `canonical(out)` against `canonical(raw)`, and `canonical` is `json.Compact`. It removes
whitespace and does nothing else. Three consequences, each of which costs debugging turns if missed:

1. **Key order is significant.** The fixture's `payload` keys must appear in `ModelListPayload`'s field order
   (`conversation_id`, `models`, `dropped_models`), and each entry's keys in `ModelOption`'s field order
   (`resolved_model`, `value`, `display_name`, `effort_levels`, `supports_auto_mode`, `truncated_fields`). Envelope keys
   follow `Envelope`: `id`, `type`, `ts`, `payload`.
2. **Go's marshaller HTML-escapes `<`, `>` and `&` into their six-character `\u00XX` JSON escapes.** A fixture writing
   those three characters literally is never byte-equal to the re-marshalled bytes. Copy the escaped form verbatim from
   the two fixtures that already carry it: `rate_limited.json`'s `status` value (the `<unmeasured>` sentinel) and
   `background_task_roster.json`'s first task `description` (which contains both `<` and `&`). Open those two files and
   read the raw bytes — this spec deliberately does not reproduce the escape sequences, because a transcription error
   here is exactly the failure it is warning about.
3. **Both `MarshalJSON` methods sit between the value and the bytes.** A fixture writing `"models":null` or
   `"effort_levels":null` decodes to nil, re-marshals to `[]`, and reddens the round trip. `"truncated_fields":null` is
   correct and stays `null`. If a fixture and a marshaller disagree, **the fixture is the fix** — never the removal of a
   normaliser, and never an edit to `interactive.go`.

#### `model_list.json` (id 714) — populated

`conversation_id: "c1"`. Five entries in claude's own order, carrying the rows measured live against claude 2.1.220 on
2026-08-21:

| # | `display_name` | `value` | `supports_auto_mode` | `effort_levels` |
|---|---|---|---|---|
| 1 | `Default (recommended)` | `default` | `true` | `low, medium, high, xhigh, max` |
| 2 | `Opus (1M context)` | `opus[1m]` | `true` | `low, medium, high, xhigh, max` |
| 3 | `Fable` | `claude-fable-5[1m]` | `true` | `low, medium, high, xhigh, max` |
| 4 | `Sonnet` | `sonnet` | `true` | `low, medium, high, xhigh, max` |
| 5 | `Haiku` | `haiku` | `false` | `[]` |

Row 5 is the load-bearing one: claude's reply **omits** `supportsAutoMode` and `supportedEffortLevels` on Haiku's entry,
and this wire states one position for absent and empty. So the row carries `false` and `[]` — not an elided key. It is
the empty effort list a client meets on the first frame it ever decodes.

Three values in this fixture are **chosen, not captured**, and the test's doc comment must say so in
`model_announced.json`'s own register ("The model value is measured; the bool is chosen to discriminate"):

- **`resolved_model`.** The 2026-08-21 measurement recorded the four columns above and no `resolvedModel`-shaped key, so
  there is nothing to copy. Row 5 carries `claude-haiku-4-5-20251001` — the resolution of `haiku` measured on the *same*
  claude version in the committed capture `internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json`, which is a
  turn announcement rather than an `initialize` reply, and the comment says exactly that. Rows 1–4 carry the sentinel
  `<unmeasured>`, `rate_limited.json`'s own device for a field no capture reports — whose angle brackets
  double as the HTML-escaping pin. **Do not invent dated identifiers here.** A fixture is something a client author
  copies as if it were observed; the sibling `TestModelListType_IsNotClaudesVocabulary`'s test-local
  `"claude-opus-4-5-20251101"` appears in no capture and must not be promoted into committed bytes.
- **`truncated_fields`.** Populated on **row 3** as `["value"]`, `null` on the other four — `background_task_roster.json`'s
  two-entry pattern, and the sharpest pairing available: a cut `value` is doubly un-sendable. The comment notes that no
  measured value is anywhere near a producer cap, so no real frame carries this row with this report; the flag is chosen
  to discriminate a struct that drops or mis-wires the field.
- **`dropped_models`.** `2`. Non-zero so the populated fixture pins the value rather than the zero encoding, matching
  `background_task_roster.json`'s `dropped_tasks: 3`. Nothing counts it yet (#1690 / #1693), so the count is chosen.

#### `model_list_empty.json` (id 715) — empty menu

`{"conversation_id":"c1","models":[],"dropped_models":0}`. `background_task_roster_empty.json`'s shape: a frame carrying
no models at all, with `models` present as `[]` rather than elided.

#### `model_list_zero.json` (id 716) — zero-value payload

`conversation_id: ""`, `dropped_models: 0`, and `models` carrying **exactly one entry that is itself all-zero**:

`{"resolved_model":"","value":"","display_name":"","effort_levels":[],"supports_auto_mode":false,"truncated_fields":null}`

Note the asymmetry inside that entry — `effort_levels` is `[]` and `truncated_fields` is `null` — which is the two
marshallers' decided positions, not a typo. The envelope itself is normal (a real `id` and `ts`, as
`model_announced_zero.json` has); it is the *payload* that is zero-valued.

This is the fixture AC 2 turns on. Five of the six uncovered keys are reachable **only** here, and `models` carries one
entry precisely because an empty list cannot reach `ModelOption`'s keys at all.

### 2. The three round-trip tests

In `internal/protocol/interactive_test.go`, appended after `TestModelListType_IsNotClaudesVocabulary`. Each follows
`TestModelAnnouncedPayload_RoundTrip`'s shape exactly: `readFixture` → decode `Envelope` → assert `env.Type ==
TypeModelList` → decode the payload → assert fields → `roundTripEnvelope(t, env, payload, raw)`.

- `TestModelListPayload_RoundTrip` — over `model_list.json`. Assert `ConversationID`, `len(Models) == 5`, and the five
  rows **table-driven** (`t.Run` over a slice of expected rows, per CODING-STYLE § Testing) rather than 30 flat
  `if` blocks. `EffortLevels` compares via `strings.Join`, the idiom the roster test already uses. Assert row 5's
  `EffortLevels` is empty and `SupportsAutoMode` is false; assert row 3's `TruncatedFields` is `["value"]` and that at
  least one other row's is `nil`. Assert `DroppedModels == 2`.
- `TestModelListPayload_Empty_RoundTrip` — over `model_list_empty.json`. Opens with the byte-level guard
  `bytes.Contains(canonical(t, raw), []byte(`"models":[]`))`, the `Empty_RoundTrip` sibling's own device: without it an
  `omitempty` would elide the key from both sides and the round trip would go on passing.
- `TestModelListPayload_ZeroValue_RoundTrip` — over `model_list_zero.json`. Byte-level guards on **each of the six keys
  whose zero value an `omitempty` would elide**, before the decode: `"conversation_id":""`, `"dropped_models":0`,
  `"resolved_model":""`, `"value":""`, `"display_name":""`, `"supports_auto_mode":false`. These guards are what make
  "explicit zero, not elided" checkable at all, and they are the mechanism AC 2 measures. Then the ordinary decode +
  field assertions + `roundTripEnvelope`.

Doc comments on all three, in the register of their `model_announced` and roster siblings: what the fixture pins, why
the shape is what it is, and — for the zero fixture — that it is a frame no producer will ever emit, existing for the
encoding rather than the scenario.

**Citations in comments: name the symbol, never a line number.** `make cite-guard` fails a `//`-comment citation that
resolves to a declaration, and it has no depth or range exemption. Write ``the guard in `roundTripEnvelope` ``, not a
`file.go:NNN`.

### 3. The nine-mutant run (AC 2)

Measured by **running** all nine, not by inspection. `go test -overlay=<abs>/overlay.json ./internal/protocol/` runs
against a mutated copy of `interactive.go` with no write to the worktree:

```
# overlay.json — absolute paths, both sides
{"Replace":{"<worktree>/internal/protocol/interactive.go":"<scratch>/interactive.go"}}
```

Per mutant: copy `interactive.go` to scratch, add `,omitempty` to **one** JSON tag, run the command above, record which
tests fail, restore. Expect all nine red.

**Two of the nine tags are not unique in `interactive.go`.** `conversation_id` occurs 16 times and `truncated_fields` 5.
A file-wide substitution on either mutates unrelated payloads, and the red it produces proves nothing about *this*
payload. Scope the edit to the `type ModelListPayload struct { … }` / `type ModelOption struct { … }` block — e.g. an
`awk` range from the `type … struct` line to the closing `}`. The other seven tags are unique in the file.

**Mutate the scratch copy, never the worktree file.** The whole point of `-overlay` is that `interactive.go` is not
touched; an in-place `sed -i` on the worktree, restored by hand nine times, is one forgotten restore away from
committing a wire-contract change. The unscoped `conversation_id` mutant is the dangerous one — it would leave an
`omitempty` on fifteen other payloads' routing key, and not every one of those has a test carrying an empty
`conversation_id`, so `make check` is not guaranteed to catch it. Before committing, confirm
`git status --porcelain internal/protocol/` reports only the three new fixtures and the test file.

Expected redness, which doubles as the check that each fixture is pulling its weight:

| Key | On | Reddened by |
|---|---|---|
| `models` | payload | `TestModelListPayload_NilModelsNormalises` (#1704) — plus the empty fixture's guard |
| `effort_levels` | entry | `TestModelOption_NilSliceEncodings` (#1704) — plus rows 5 and the zero entry |
| `truncated_fields` | entry | `TestModelOption_NilSliceEncodings` (#1704) — plus every `null` row |
| `conversation_id` | payload | **`model_list_zero.json` only** — the other two carry `"c1"`, which `omitempty` does not elide |
| `dropped_models` | payload | the empty and zero fixtures (both `0`) |
| `resolved_model` | entry | the zero fixture's all-zero entry |
| `value` | entry | the zero fixture's all-zero entry |
| `display_name` | entry | the zero fixture's all-zero entry |
| `supports_auto_mode` | entry | the zero fixture's all-zero entry, and row 5's `false` |

If a mutant comes back green, the fixture is the fix. `interactive.go` is out of scope — this ticket writes no
production source file.

### 4. The `docs/protocol-mobile.md` edit

Three insertions and one one-sentence amendment. **Nothing else in the file moves.**

**(a) Registry row**, in § Application message types, immediately after the `session_transition` row (mirroring the new
subsection's placement; that table's order is not strictly the section order, so this is a placement choice rather than
a derivation). Follow the `model_announced` row's construction: bold type name, `binary → phone`, `no`, then
**New in v2** (interactive, capability-gated), one sentence on what it is, the **not** the per-turn announcement clause
linking `#model_announced`, the declared-#1704 / fixtures-#1705 / producer-#1693 status, and the
`See [Interactive events](#interactive-events-v2-capability-gated).` tail `session_transition`'s own row uses.

**(b) `#### \`model_list\`` subsection**, placed **after** `session_transition` and before
`#### Reconnect replay & resync (consumer, #647)`. Contents, in order:

1. **Direction + membership**, in `session_transition`'s manner: binary → phone, not in `v1TypeSet` (an old phone never
   receives it), and **distinct from the fifteen turn-stream events above** — it is not a `turnevent` variant and does
   not belong to the structured live-session stream. State that it arrives on claude's `control_response` (subtype
   `initialize`), not on the turn stream. **Do not claim it carries no `event_id`** the way `session_transition` does:
   whether it is pushed or answered on request is #1693's to settle, and this section states the payload shape only.
   `cmd/pyry/relay_guard_test.go` classifying it `"push"` decides nothing here — `TypeSessionTransition` is classified
   `"push"` there too and is the canonical uncounted frame.
2. **Two field tables**, in `background_task_roster`'s nested format: the payload's three keys, then
   `Each element of \`models\`:` and the entry's six. Row order == struct field order. `effort_levels` is
   `array of string` (always present, never `null`); `truncated_fields` is `array of string \| null`.
   **`dropped_models`' row must not imply an entry cap that exists.** `background_task_roster`'s wording ("beyond the
   daemon's entry cap") is true there and would be a lie here: nothing counts this field yet — #1690 owns making the
   decode record it, #1693 is where the field and a counter meet. Say what `ModelListPayload`'s own doc comment says —
   the field is declared ahead of any producer, and a permanent `0` reads as "nothing was dropped", which is why it is
   declared rather than omitted. A client must not take `len(models) + dropped_models` as the menu's true size today.
3. **Emission status**: shape declared by #1704, fixtures and this section #1705, **nothing emits it yet** — producer
   #1693. Same declare-then-emit sequencing as #1405 → #1410 and #1616 → #1638.
4. **The four properties (AC 5)**, each as its own bolded paragraph, in `model_announced`'s register:
   - **`value` is not a dated identifier.** It is what you pass: an alias (`sonnet`), a bracketed variant (`opus[1m]`),
     or `default`. A client cannot derive a family by splitting it on `-`. `resolved_model` is what it resolves to right
     now, published *before* the first turn so a client can show what a family currently means.
   - **A lookup against this list can miss, and that is ordinary.** claude announces an identifier at least as specific
     as the one it was given, so a `model_announced` value need not appear here. Link `#model_announced`; state that
     `display_name` is the intended join, not `resolved_model`.
   - **Two of the five measured `value`s are rejected by the daemon's own inbound validator.** The only inbound path
     that accepts a model is `set_session_settings`; its rule (`validModel`) accepts `""`, otherwise 1..64 bytes whose
     first byte is alphanumeric and whose every byte is in `[A-Za-z0-9._-]`. Against the five measured values that
     accepts `default`, `sonnet`, `haiku` and **rejects** `opus[1m]` and `claude-fable-5[1m]` — the bracket is outside
     the charset. **A menu row is therefore not necessarily sendable back.** Add `effort_levels`' identical direction
     hazard: `validEffort`'s enum is closed and accepts all five measured levels today, so a level claude adds later
     would be published here and refused inbound. State plainly that the charset is #845's argv-injection defense and is
     not widened to close the gap — that belongs to whichever slice first makes a client send one (#1693).
   - **The strings are claude-authored, bounded but not sanitized — a report, never a control input.** Restate
     `ModelOption`'s SECURITY paragraph **in full, in this subsection**: safe to render as inert text, never fed to an
     HTML sink, an attribute, or a URL; no control-character or terminal-escape stripping happens on this path, so the
     render boundary owing the sanitization is the client's. Name the four claude-authored surfaces —
     `resolved_model`, `value`, `display_name`, and every string in `effort_levels`. Carry the amendment the sibling
     frames do not need: `value` is the first field in this family a client is meant to send **back**, and publishing it
     does not make it trusted — the daemon re-validates it at `validModel` rather than trusting its own published list.
     **Do not delegate this to a link at `#model_announced`.** A "SECURITY: as `model_announced`" pointer is the exact
     defect already on record against `BackgroundTask`'s own comment (`docs/knowledge/features/protocol-package.md`
     § Model-list payload) — a warning that claims to repeat something it does not restate. The sentences have to be
     literally present, because a client author implements from this subsection alone.
   - Plus, as `model_announced` and the roster both do: **`truncated_fields` is load-bearing, not decoration.** A client
     ignoring it presents cut text — or a cut list — as complete.
5. **Cross-reference back (AC 3's other half)**: amend the existing `#### model_announced` subsection's
   "**A lookup miss is ordinary, not an error**" paragraph, which already says the value "need not appear in any
   published model list", so that the phrase links `#model_list`. One sentence/link, nothing else in that subsection
   moves. The two subsections then reach each other from either arrival point: one is the per-turn announcement, the
   other the menu.

**(c) Changelog entry** at the **head** of § Changelog, dated `2026-08-22`, in the #1616 / #1405 register. It must
record: what was added and for whom (desktop#561, #682); that the shape was declared by #1704 and nothing emits it yet
(#1693); the three fixtures and what each pins; the four client-facing properties; and — explicitly — that
**no live count moved**, because the frame is uncounted and landing after `session_transition` keeps both count
sentences checkable by counting headings.

**Counts: nothing moves (AC 4).** Both live sites still read **fifteen** — § Interactive events' opener and
`session_transition`'s back-reference. The opener's clause is "the wire representation of the daemon's neutral internal
turn-event model", and this frame is not a `turnevent` variant, so it is not counted. Placing the subsection after
`session_transition` is what preserves the property that both sentences are *literally countable*: fifteen `####`
headings sit between the opener and `session_transition`, and inserting an uncounted subsection into that run would
leave sixteen headings above a sentence saying fifteen.

**Two things must not be touched while editing nearby:**

- **The changelog's own numerals.** #1405's entry says the counts "both now read fourteen" and #1616's "both now read
  fifteen". Those are historical records of a change in the tense of their own moment. A sweep that "fixes" the
  stale-looking numeral corrupts the record.
- **§ `unrecognized_message`'s count of five.** It counts `system` subtypes the parser maps internally. This frame
  arrives on a `control_response`, not as a `system` subtype, so the count is unaffected.

**The three existing `model` rows are not re-pointed.** #1616 pointed `screen_snapshot`, `session_settings` and
`set_session_settings` at `#model_announced` because the field name was *literally identical*. It is not here — this
payload has no field named `model`, only `models`, `value` and `resolved_model`. The distinction that needs drawing is
between the two frames that publish model identity, and item 5 above is where it is drawn.

## Concurrency model

None. Fixtures, tests and prose only; `internal/protocol` remains a pure-DTO package with no goroutines.

## Error handling

No new failure modes in shipped code. The failure surface is the test's:

- A fixture/marshaller disagreement surfaces as a `roundTripEnvelope` byte diff. Fix the fixture (§ Byte-equality rules).
- A key-order or escaping mistake surfaces the same way and reads confusingly — check those two causes first.
- A green mutant means the fixture does not carry that key at its zero value. Fix the fixture, never `interactive.go`.

## Testing strategy

- `make check` is the gate. `internal/protocol` is not behind a build tag, so the standard gate runs all of it.
- The nine-mutant matrix (§ 3) is run once, by hand, via `-overlay`. It is the evidence for AC 2 and belongs in the PR
  description; record the tests each mutant reddened.
- No new e2e coverage. Nothing emits this frame, so there is no live path to exercise.
- No envelope-cap test. `TestBackgroundTaskPayloads_FitV2EnvelopeCap`'s pattern is not repeated: no producer sets an
  entry cap yet, so any arithmetic here would pin a bound nothing enforces. #1693 owns it.

## Open questions

1. **`resolved_model` has no measured source.** The 2026-08-21 capture recorded `displayName`, `value`,
   `supportsAutoMode` and `supportedEffortLevels` and no `resolvedModel`-shaped key. The fixture therefore marks four of
   five rows `<unmeasured>` and grounds the fifth in a same-version turn announcement. **#1693 must measure
   the `initialize` reply's own per-entry keys** and, if claude carries no resolution there, decide where the producer
   derives it — a decision this slice deliberately does not pre-empt. When #1693 measures it, the populated fixture's
   four sentinels become the thing to replace.
2. **The package overview's unfixed SHOULD FIX must not be propagated.** `ModelOption.TruncatedFields`'s doc comment
   enumerates the cut-field vocabulary as `("value", "display_name")` while the same type's SECURITY paragraph credits
   three claude-authored text fields plus `EffortLevels`. The doc's `truncated_fields` row should say it names **this
   row's** cut fields without re-committing to that 2-of-3 list. Fixing the code comment is out of scope here (no
   production source files) and belongs to #1693, which reads that line to pick the names its producer emits.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] MUST FIX — addressed inline.** The design publishes claude-authored strings that crossed the
  subprocess trust boundary, and the doc subsection is the *only* artifact a client author implements from. The first
  draft said "paraphrase `ModelOption`'s SECURITY paragraph", which a developer under budget pressure satisfies with a
  link to `#model_announced`. That is the defect class already on record against `BackgroundTask`'s own comment
  (`docs/knowledge/features/protocol-package.md` § Model-list payload): a warning that claims to repeat something it
  does not restate. § 4(b) item 4 now requires the sentences literally present, names all four claude-authored surfaces
  (`resolved_model`, `value`, `display_name`, every string in `effort_levels`), and forbids the delegating link.
- **[Trust boundaries] No further findings.** This ticket adds no code on the boundary — it documents it. The boundary
  itself is unchanged and explicit: claude's stdout → `internal/streamsup`'s parser → a `turnevent` variant → (future,
  #1693) `turnbridge` → this payload. The `<unmeasured>` sentinel's angle brackets are a deliberate HTML-escaping pin,
  `rate_limited.json`'s own device: a fixture whose escaped bytes go red the day a marshaller stops escaping.
- **[Subprocess / external execution] MUST FIX — addressed inline.** The nine-mutant run (AC 2) is the only command in
  this ticket that touches a production file. Applied in place rather than through `-overlay`, one forgotten restore
  commits a wire-contract change, and the **unscoped `conversation_id` mutant is the dangerous one** — it would leave
  `omitempty` on fifteen other payloads' routing key, and not every one of those has a test carrying an empty
  `conversation_id`, so `make check` is not guaranteed to catch it. § 3 now requires mutating a scratch copy and
  confirming `git status --porcelain internal/protocol/` before commit. No other subprocess execution exists; no
  `exec.Command`, no `sh -c`.
- **[Network & I/O] SHOULD FIX — addressed inline.** `background_task_roster`'s `dropped_tasks` wording ("beyond the
  daemon's entry cap") is true there and would be a lie here: nothing counts `dropped_models` yet. Copying that phrasing
  would tell a client that `len(models) + dropped_models` is the menu's true size and that an entry cap is enforced —
  neither holds today. § 4(b) item 2 now requires the field's honesty, matching `ModelListPayload`'s own doc comment. No
  envelope-cap test is prescribed, deliberately and with the reason stated: no producer cap exists, so any arithmetic
  here would pin a bound nothing enforces (#1693).
- **[Threat model alignment] No findings.** `docs/protocol-mobile.md` § Security model Threat #1 (prompt injection,
  severity high, mitigation partial) is the relevant one, and § 4(b) item 4 is its alignment — the render boundary owing
  the sanitization is the client's, restated in full. No new disclosure: a paired phone already receives
  `model_announced` over the same Noise_IK channel every turn, so publishing the menu widens nothing. Threats #2–#8 are
  untouched — this ticket adds no transport, no key material, and no inbound path. The one change that *would* have
  touched a threat is explicitly out of scope: `validModel`'s charset is #845's argv-injection defense, and admitting
  `[` / `]` to make `opus[1m]` sendable is a security decision about an untrusted phone-supplied string, not a typo fix.
  It belongs to **#1693**, the slice that first makes a client send one. The spec names it as out of scope in the
  reading list ("read only, neither is changed"), in § 4(b) item 4's property 3, and here.
- **[Concurrency] No findings, one invariant protected.** No goroutines, no locks, no shared state. The one
  concurrency-relevant property in the surrounding code is that both `MarshalJSON` methods take value receivers so
  normalisation lands on a copy — reaching through `p.Models[i]` would mutate a caller's backing array, a data race on a
  shared payload as well as a correctness bug. Code review's own mutant matrix measured that the in-place variant emits
  **byte-identical JSON**, so no round trip can see it and only `TestModelOption_NilSliceEncodings`'s trailing
  `p.Models[0].EffortLevels != nil` assertion catches it. The reading list now says that assertion must not be removed,
  because it reads like a tidy-up candidate.
- **[Tokens, secrets, credentials] Not applicable, structurally.** `ModelListPayload` and `ModelOption` declare no
  `session_id`, no `uuid` and no path field, and `TestModelListType_IsNotClaudesVocabulary` already pins the absence of
  claude's identity keys — so a fixture cannot carry one even by accident. The fixtures are hand-authored from a
  measured table, not captured output: the only strings are model identifiers, claude's display labels, effort-level
  names and the synthetic `c1`. Contrast `internal/e2e/realclaude`, whose fixture family needs `dropcapScanner` (token
  values **and** `/Users/` paths) precisely because it commits real captured output; no host path enters this package.
- **[File operations] Not applicable.** The only filesystem access is `readFixture`, which composes `"testdata/" + name`
  from a test literal. No caller-controlled path, no traversal surface, no check-then-use, no created files at runtime.
- **[Cryptographic primitives] Not applicable.** No randomness, no key material, no comparison against a secret.
- **[Error messages, logs, telemetry] Not applicable.** No logging. The only output is `roundTripEnvelope`'s byte diff
  on failure, over synthetic fixture bytes that carry nothing sensitive.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-22
