# Question-batch payload (#1963 shape, #1964 fixtures + docs, #1965 parse; producer #1927)

The v2 wire shape for claude's clarifying-question batch (`AskUserQuestion` tool
call), riding `TypeQuestionShown` (#1962 — see [Envelope types § v2 question-batch
vocabulary](protocol-package-constants-codes-go-envelope-types.md)). The
committed fixtures and the `docs/protocol-mobile.md` § Question (v2) write-up
landed in #1964; the fail-closed parse landed in #1965
([`internal/questionbridge`](questionbridge-package.md)), with no consumer yet
— the producer (#1927) is still ahead, the pyrycode-desktop#849, #1405→#1410
/ #1616→#1638 / #1704→#1848 / #1726→#1727 sequencing repeated a fifth time.
Nothing constructs these types on a live path yet; nothing emits them.

```go
type QuestionShownPayload struct {
    ConversationID  string     `json:"conversation_id"`
    QuestionBatchID string     `json:"question_batch_id"`
    Questions       []Question `json:"questions"`
}

type Question struct {
    Text        string           `json:"question"`
    Header      string           `json:"header"`
    Options     []QuestionOption `json:"options"`
    MultiSelect bool             `json:"multi_select"`
}

type QuestionOption struct {
    Label       string `json:"label"`
    Description string `json:"description"`
}
```

Two nesting levels: the batch holds ordered questions, each holding ordered
options. `internal/protocol/questions.go`'s doc comments carry the full
rationale for each choice below — cite them rather than re-deriving.

- **`QuestionBatchID`, not `QuestionID`.** Plays `ModalID`'s role (one-time,
  opaque, unguessable nonce minted per batch, resolved server-side) but the
  payload also declares a nested `Question` type with no id of its own, so
  `question_id` beside a `questions` array would misread as that type's key.
- **The two `nil → []` normalisations share one reason, stated once.** Both
  `questions:[]` and `options:[]` are out of contract (bounds are documented
  1-4 / 2-4), so a nil slice is only reachable from a zero value or a producer
  bug — `[]` keeps the frame decodable by a non-optional-array client where
  `null` would fail. This is the **client-decode** half of
  `BackgroundTaskRosterPayload.MarshalJSON`'s rationale, not its "empty is a
  positive statement" half, which is false here. Contrast `ModelOption`'s two
  normalisers, which share a family but genuinely differ — don't assume every
  sibling pair splits that way; check which one this pair is.
- **Header cap: documented 12, observed 14 — now published, not just recorded
  here.** The vendor contract says "max 12 characters"; the committed capture
  (`ask_user_question_v2.1.239.json`) has a 14-rune/14-byte header. `docs/protocol-mobile.md`
  § Question (v2) states it as **documented 12, observed 14** and tells a
  client to size for 14, naming runes (not bytes) as the unit and flagging
  that the observed header is pure ASCII, so nothing committed yet separates
  the two units — a coincidence, not a measurement of the byte case. **#1965
  landed with no header bound of its own** — the header is bounded only
  transitively, by `questionbridge`'s single pre-decode byte cap over the
  whole input. See [questionbridge-package.md](questionbridge-package.md).
- **No `TruncatedFields`, unlike `SlashCommand` and `ModelOption`.** Deliberate:
  a producer cutting an over-long question/header/label/description has
  nowhere to report it, so #1965's parse rejects an over-long input
  fail-closed rather than truncating silently.
- **`QuestionOption` has no id, unlike `ModalOption{id,label}`** — claude's
  answer protocol selects by label. Flagged for #1927: whatever inbound answer
  it designs returns a claude-authored string, and publishing that string does
  not make it trusted coming back. `docs/protocol-mobile.md` § Question (v2)
  now states this explicitly, so a client author reads it rather than
  discovering it while designing #1927's inbound frame.
- **SECURITY tier is `ModelOption`'s, not `SlashCommand`'s.** `Text`, `Header`,
  `Label`, `Description` are claude-authored (subprocess boundary), not
  workspace-authored — inert-text-only, client owns sanitization, no
  server-side stripping.
- **A wire doc's SECURITY paragraph is not where provenance belongs — the field
  tables are.** Security review on #1964 caught this as a MUST FIX: the first
  draft of § Question (v2) stated that the two ids are daemon-asserted and the
  four strings claude-authored only in prose below the tables, which asks a
  client author mirroring the tables field for field to reconstruct the trust
  boundary from a paragraph elsewhere — rendering `header` as trusted chrome
  is the concrete failure that invites. The shipped section carries a
  Provenance column on all three field tables instead. Worth doing by default
  for any future v2 payload doc section that mixes daemon-asserted and
  subprocess- or workspace-authored fields, rather than waiting for review to
  catch the omission again.
- **An enforcement table that lists only the bounds with an enforcer implies
  the rest are covered.** #1964's first draft listed three bounds (question
  count, option count, header cap) and left the per-field string-length bound
  out entirely, which would have told a reader the header cap was the only
  gap rather than one of four. The shipped table has all four rows, and the
  length bound is published **without a number** — the § Attachments (#1752)
  reason: a figure ahead of the code that enforces it is worse than a named
  gap. Applies to any future bounds table in this doc: enumerate every bound
  the contract states, not only the ones something already enforces.

## Testing

`questions_test.go`'s two round trips now read committed fixtures via
`readFixture` (`question_shown.json`, `question_shown_zero.json`) instead of
carrying the bytes inline — every field-by-field assertion carries over
unchanged, including the zero-value test's seven-key presence loop, which
still runs before the round trip so an `omitempty` on `header` or
`multi_select` is caught before the bytes are even compared. A third fixture,
`question_shown_empty.json`, backs new coverage: `TestQuestionShownPayload_Empty_RoundTrip`
pins the **decode** side of `questions:[]` on a batch carrying no questions,
where the earlier constructed-value test only ever proved the encode side.
nil→`[]` normalisation tests remain at both nesting levels.

**No committed fixture carries `"options":[]`, and that's a property of this
shape rather than an omission.** The two-level analogues (`model_list_zero.json`,
`slash_command_list_zero.json`) reach their empty-array key from the same
all-zero entry that reaches the entry's other keys. One nesting level deeper,
that stops working: the all-zero `Question` needed to reach `label` and
`description` must carry one `QuestionOption`, so the only route to
`"options":[]` is a constructed nil — `TestQuestion_NilOptionsNormalises` is
its sole pin. Don't assume a two-level fixture trio's reachability arithmetic
survives a third nesting level; check which keys each fixture actually
reaches before copying the sibling's shape.

Thirteen mutants (`omitempty` on all nine wire keys, three key renames, one
field reordering) were **run** over a `go test -overlay` scratch copy rather
than predicted, per #1718's lesson that a sole-redness claim written from the
prediction column ships wrong. All thirteen turn at least one test red. The
zero fixture is **sole**-red for six of the nine keys (`conversation_id`,
`question_batch_id`, `question`, `header`, `label`, `description`);
`multi_select` reddens both the populated and zero fixtures; `options` is
caught only by `TestQuestion_NilOptionsNormalises`, confirming the
reachability finding above; and `questions` reddens both the new empty
fixture and the constructed-value normalisation test, so the empty fixture is
**not** sole-red for it — its value is the decode-side pin, not a mutant
nothing else catches. The earlier in-place-normalising `MarshalJSON` mutant
(reaching through `p.Questions[i]` instead of calling `Question.MarshalJSON`,
byte-identical output, caught only by
`TestQuestion_NilOptionsNormalises/nested_in_payload`'s backing-array
assertion) still holds, and a pointer-receiver mutant without the `alias(*p)`
deref still fails to compile and reddens the whole package rather than
isolating a subtest.

## Lesson: a declare-only slice's test file scales with nesting depth, not type count

Planned ≤190 production / ≤160 test / ≤350 total lines (sized against #1727's
211/164/375 for a flat two-type payload). Actual: 208 / 250 / 458. Production
landed close to plan — the overage is almost entirely the ten-requirement doc
block, already priced as the slice's real cost. Tests overran hardest, for two
reasons the plan under-counted: field-by-field assertions cost roughly three
lines each across **three** nesting levels rather than a flat payload's two,
and choosing inline goldens over `readFixture` (a deliberate call, since
fixtures don't exist yet) adds ~20 lines a one-line fixture call would have
hidden. Worth carrying into any future slice that adds a nesting level to an
existing declare-ahead-of-producer shape — size the test budget from the
nesting depth, not from the sibling with the closest type count.

## Related

- [Envelope types § v2 question-batch vocabulary](protocol-package-constants-codes-go-envelope-types.md) — `TypeQuestionShown`'s frame-family security argument, nesting fact, and naming-trap record (#1962)
- [Slash-command-list payload](protocol-package-slash-command-list-payload.md) — the closest structural analogue: nested list payload declared ahead of its producer, same value-receiver + type-alias marshaller idiom
- [Model-list payload](protocol-package-model-list-payload.md) — the two-normalisers-with-different-reasons counter-example this payload's shared-reason case contrasts with
- [questionbridge-package.md](questionbridge-package.md) — the fail-closed bounded parse (#1965), landed with no consumer
- Open, deliberately out of scope for both landed slices: the per-device answer gate (#702) extension and the unanswered/dismissed-batch fail-safe (#1927)
