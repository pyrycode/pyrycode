# Question-batch payload (#1963 shape; parse #1965; producer #1927)

The v2 wire shape for claude's clarifying-question batch (`AskUserQuestion` tool
call), riding `TypeQuestionShown` (#1962 — see [Envelope types § v2 question-batch
vocabulary](protocol-package-constants-codes-go-envelope-types.md)). Declared
ahead of the parse (#1965), the producer (#1927), the fixtures + `docs/protocol-mobile.md`
write-up (#1964), and pyrycode-desktop#849 — the #1405→#1410 / #1616→#1638 /
#1704→#1848 / #1726→#1727 sequencing repeated a fifth time. Nothing constructs
these types; nothing emits them.

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
- **Header cap: documented 12, observed 14.** The vendor contract says "max 12
  characters"; the committed capture (`ask_user_question_v2.1.239.json`) has a
  14-rune/14-byte header. Recorded here as a generation-side guideline claude
  doesn't hold to, not a wire invariant — **#1965 must not enforce 12
  fail-closed**, since its own acceptance criteria pin it against the same
  capture. Whatever bound #1965 does land on must name its unit (rune vs.
  byte); the observed header is pure ASCII, so nothing in the tree
  distinguishes them yet.
- **No `TruncatedFields`, unlike `SlashCommand` and `ModelOption`.** Deliberate:
  a producer cutting an over-long question/header/label/description has
  nowhere to report it, so **#1965 must reject an over-long field fail-closed
  rather than truncate silently**, or a later ticket adds the field back.
- **`QuestionOption` has no id, unlike `ModalOption{id,label}`** — claude's
  answer protocol selects by label. Flagged for #1927: whatever inbound answer
  it designs returns a claude-authored string, and publishing that string does
  not make it trusted coming back.
- **SECURITY tier is `ModelOption`'s, not `SlashCommand`'s.** `Text`, `Header`,
  `Label`, `Description` are claude-authored (subprocess boundary), not
  workspace-authored — inert-text-only, client owns sanitization, no
  server-side stripping.

## Testing

`questions_test.go`: a two-question/multi-option round trip against an inline
golden (no fixture — fixtures are #1964's, and the committed capture is behind
the `e2e_realclaude` tag `make check` never compiles), a zero-value round trip
that guards the seven keys an `omitempty` would elide before checking the
round trip itself, and nil→`[]` normalisation tests at both nesting levels.

Two mutants were run over `go test -overlay` rather than predicted, confirming
the design's stated concurrency hazard is real: an in-place-normalising
`QuestionShownPayload.MarshalJSON` (reaching through `p.Questions[i]` instead
of calling `Question.MarshalJSON`) produces JSON byte-identical to the correct
output and is caught **only** by `TestQuestion_NilOptionsNormalises/nested_in_payload`'s
trailing backing-array assertion — no byte check can separate the two. A
pointer-receiver mutant that doesn't carry the `alias(*p)` deref fails to
compile and reddens the whole package rather than isolating a subtest.

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
- Open, deliberately out of this slice's scope: the documented-12/observed-14 disagreement `docs/protocol-mobile.md` must resolve (#1964); the per-device answer gate (#702) extension and the unanswered/dismissed-batch fail-safe (#1927)
