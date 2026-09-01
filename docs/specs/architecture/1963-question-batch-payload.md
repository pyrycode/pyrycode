# #1963 — Declare the question-batch payload and its nested question and option types

Wire vocabulary only. Three exported types, two `MarshalJSON` normalisers, no producer, no parse, no fixtures.

## Files to read first

| Read | Symbol | What to extract |
|---|---|---|
| `internal/protocol/codes.go` | `TypeQuestionShown` and its doc block | The frame-family security argument, the nesting fact, the declare-ahead-of-producer precedents, and the naming trap. **Cite this block; do not restate it.** #1962 landed it and it is the single source for all four. |
| `internal/protocol/interactive.go` | `SlashCommandListPayload`, `SlashCommand`, and both their `MarshalJSON` methods | The closest structural analogue: a nested list payload declared ahead of its producer. Take the doc-block *shape*, the value-receiver + type-alias marshaller idiom, and the "cannot be folded into the payload's" paragraph. |
| `internal/protocol/interactive.go` | `SlashCommand`'s SECURITY paragraph | The exact shape this slice's SECURITY paragraph follows — inert text, never an HTML sink, bound decided by the producer, sanitization owed by the client. |
| `internal/protocol/messaging.go` | `ModalShownPayload` | The two paragraphs to cite for `ConversationID` (#1065 outbound scoping) and for the correlation nonce (`ModalID`). Also the "No field carries omitempty" discipline sentence above `ModalOption`. |
| `internal/protocol/interactive.go` | `ModelOption.MarshalJSON` | The counter-example that makes AC 3 checkable: two normalisers in one family with *different* reasons, each stating its own. Read it to see what a manufactured distinction would look like, then don't write one. |
| `internal/protocol/interactive_test.go` | `roundTripEnvelope`, `TestSlashCommandListPayload_NilCommandsNormalises`, `TestSlashCommand_NilSliceEncodings` | The test idiom to reuse verbatim. `roundTripEnvelope` takes `raw []byte`, so it works against an inline literal exactly as it works against a fixture. |
| `internal/protocol/envelope_test.go` | `canonical` | Whitespace-normalises both sides of the byte comparison, so the inline golden may be indented. |
| `internal/protocol/interactive_test.go` | `TestModelListPayload_ZeroValue_RoundTrip` | Why a zero-value payload carries one all-zero entry: it is the only route to the nested types' wire keys. Same reasoning applies at two levels here. |
| `internal/e2e/realclaude/testdata/ask_user_question_v2.1.239.json` | — | The committed capture. One question, two options, `"multiSelect": false`, header `"Write strategy"`. It is the *evidence*, not a test input — nothing in this slice reads it. |
| `internal/e2e/realclaude/ask_user_question_shape_test.go` | `TestAskQuestionShape_ReportsEachMissedCheckAndSkipsAfterAnUndecodableInput` | Where the capture's shape is already asserted. Read only if you doubt the nesting; it is behind `e2e_realclaude` and `make check` never compiles it. |
| `docs/knowledge/features/protocol-package-slash-command-list-payload.md` | — | The #1727 retrospective. Two lessons apply directly: a "which test reddens this mutant" claim must be *run*, not predicted; and an unscoped `sed` on `return json.Marshal(alias(p))` hits every marshaller sharing a file. |

## Context

`question_shown` is the frame that closes a live defect: claude's clarifying-question tool call reaches a remote client today as a modal titled "Permission required" whose body reads `AskUserQuestion`. #1962 landed the wire type and its guard classification. This slice lands the payload shape so pyrycode-desktop#849 can be written against it, ahead of the parse (#1965) and the producer (#1927) — the same sequencing as #1405→#1410, #1616→#1638, #1704→#1848 and #1726→#1727.

Nothing constructs these types. Nothing emits them. There are no fixtures — those and the `docs/protocol-mobile.md` section are #1964's.

No ADR is warranted: the one decision big enough for one (a separate frame family rather than a grown `modal_shown`) was made and recorded by #1962 in `TypeQuestionShown`'s doc block.

## Design

### File placement — a new file pair

Land `internal/protocol/questions.go` and `internal/protocol/questions_test.go`. Do **not** append to `interactive.go` or `messaging.go`.

Three reasons, in order of weight. #1962 grouped `TypeQuestionShown` in its own const block rather than merging it into the modal block, and the deciding argument there was security, not taste — this is a distinct family and the file layout should say so. `attachments.go` (#1752) is the standing precedent: a declare-ahead-of-producer v2 family gets its own file pair, and this slice is the same shape. And `interactive.go` is already the package's largest production file at 952 lines, against a package convention of one file per spec-section group.

There is a fourth, contingent reason: `feature/449` is in flight and touches `codes.go`. A new file pair makes this branch structurally conflict-free.

**Do not touch `codes.go`.** `TypeQuestionShown`'s doc block already forward-references this ticket correctly. `TestQuestionShownType_IsNotClaudesVocabulary` also needs no edit — unlike #1726's, it carries no closing paragraph that this slice makes false. Both were checked; don't go looking.

### Types

```go
type QuestionShownPayload struct {
	ConversationID string     `json:"conversation_id"`
	QuestionBatchID string    `json:"question_batch_id"`
	Questions      []Question `json:"questions"`
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

Four naming calls, each of which needs a sentence in the doc block:

- **`QuestionShownPayload`**, from the mechanical `<Type>Payload` convention (`TypeModalShown`→`ModalShownPayload`, `TypeSlashCommandList`→`SlashCommandListPayload`).
- **`QuestionBatchID`, not `QuestionID`.** It plays `ModalID`'s role exactly — one nonce per surfaced batch — but this payload also declares a nested `Question` type, and `question_id` sitting beside a `questions` array would read as that type's key. `Question` carries no id, so the misreading is not idle. The `_batch_` is what makes the field self-describing to pyrycode-desktop#849 and to #1927.
- **`Question.Text`, wire key `question`.** The wire keeps claude's own key, as `SlashCommand` keeps all four of claude's. The Go name diverges only because `Question.Question` stutters at every call site.
- **`QuestionOption`, not `Option`.** `ModalOption` already exists in the package; a bare `Option` beside it would read as the generic one.

`MultiSelect` is a plain `bool`, wire `multi_select` — claude's camelCase `multiSelect` snake-cased, exactly as `SlashCommand.ArgumentHint` snake-cases `argumentHint`. Under the no-`omitempty` discipline the wire always states a position, so there is no absent-versus-`false` tri-state to mint here; that question is real but it is #1965's, on the decode side.

**No field carries `omitempty`** (AC 4), for `docs/protocol-mobile.md` § Modal's stated reason: all fields always present, so an empty header or an unset `multi_select` is a real answer rather than a vanished one.

### The two normalisers

`QuestionShownPayload.MarshalJSON` normalises a nil `Questions` to `[]`. `Question.MarshalJSON` normalises a nil `Options` to `[]`. Both take a value receiver and the `type alias` indirection, for `SlashCommandListPayload.MarshalJSON`'s reasons.

**AC 3's reason is genuinely shared, and the doc must say so rather than manufacture a distinction.** Write it once, in the payload's method, and have the entry's method point at it:

> Both empty arrays are **out of contract**. The documented bounds are 1–4 questions and 2–4 options per question, so neither `"questions":[]` nor `"options":[]` is reachable in a well-formed batch. A nil slice is therefore only reachable from a constructed zero value or a producer bug, and `[]` is the encoding that leaves such a frame decodable by a client whose array type is non-optional, where `null` fails that decode outright.

That is one half of `BackgroundTaskRosterPayload.MarshalJSON`'s rationale — the client-decode half. State explicitly that the *other* half does **not** transfer: "an empty list is a positive statement" is true of an empty roster (the daemon has no background tasks) and false here (an empty question array states nothing legal at all). Distinguishing the halves is what keeps this from being a borrowed reason.

The entry's method still owns one reason of its own, and it is not shared: it **cannot be folded into the payload's**. A payload marshaller normalising entries in place would reach through `p.Questions[i]` into the caller's backing array — for a payload shared between an emitter goroutine and a per-connection fan-out that is a data race as well as a correctness bug — and it would not fire at all when a `Question` is marshalled on its own.

### Doc-block requirements

The doc blocks are this slice's cost, not the code. **Cite the sibling rationales; do not restate them** — restating is what would put this over the boundary. Each item below is one to three sentences, not a paragraph.

1. **Frame identity and sequencing** — cite `TypeQuestionShown`'s block for the family argument and the nesting fact. Name #1965 (parse), #1927 (producer), #1964 (fixtures + wire doc), pyrycode-desktop#849 (consumer). State that nothing constructs these types.
2. **`ConversationID`** — cite `ModalShownPayload`'s #1065 paragraph. Daemon-asserted, outbound scoping only; present and unfilled by this ticket, the producer supplies it at mapping time.
3. **`QuestionBatchID`** — cite `ModalShownPayload`'s `ModalID` paragraph. One-time, opaque, **unguessable** nonce minted per surfaced batch, resolved server-side against the daemon's own outstanding-batch state. Minting is #1927's, mirroring #703's; "unguessable" is what carries the `crypto/rand` requirement across to it. State that adding this outbound key loosens no inbound guarantee.
4. **The batch is modelled whole** — the desktop steps one question at a time with header tabs and a Previous button, so every question must be in hand at once; a sequence of single-question frames cannot serve it.
5. **The bounds are documented here, not enforced here** — 1–4 questions, 2–4 options. The fail-closed bounded parse is #1965's.
6. **The header cap: documented 12, observed 14.** The vendor page (https://code.claude.com/docs/en/agent-sdk/user-input) says "max 12 characters"; the committed capture's only header is `"Write strategy"`, 14 runes and 14 bytes. So the cap is a generation-side guideline claude does not itself hold to, not a wire invariant. **Say this explicitly enough that #1965 does not enforce 12 fail-closed** — its own AC pins it against that same capture, so a 12-rune reject branch would make those two criteria unsatisfiable together. **Name the unit** in whatever is said about the bound: a rune count and a byte count diverge on the first non-ASCII header, and the observed header is pure ASCII, so nothing in the tree distinguishes them yet. Note that the question and option *counts* are not contradicted — the capture's one question and two options sit inside 1–4 and 2–4.
7. **`preview` is absent by construction, and that is a stated gap.** The vendor contract gives each option an optional `preview` carrying an HTML fragment, emitted only when `toolConfig.askUserQuestion.previewFormat` is set. pyry never sets it — `previewFormat` and `toolConfig` appear nowhere under `cmd/` or `internal/` (verified 2026-09-01). So `label` and `description` are the complete per-option key set. State it the way `SlashCommandListPayload` records its measured key set, so a later reader finds a stated gap rather than a silent drop — and so that if pyry ever sets `previewFormat`, the first person to look finds the sentence that says why the field isn't here.
8. **SECURITY** — follow `SlashCommand`'s paragraph. `Question.Text`, `Question.Header`, `QuestionOption.Label` and `QuestionOption.Description` are claude-authored strings that crossed the subprocess trust boundary to a client render surface. Safe to render as inert text; never an HTML sink, an attribute, or a URL. Unlike `SlashCommand`'s, these are claude-authored rather than workspace-authored, so this is `ModelOption`'s trust level, not the lower one. The daemon does not sanitize them — no control-character or terminal-escape stripping on this path — and the render boundary owing sanitization is the client's. This struct re-decides no maximum and declares no charset check: the bound is the parse's (#1965) and the producer's (#1927), and a second cap here would be a second place the limit is decided.
9. **This shape carries no truncation report, deliberately** — unlike `SlashCommand.TruncatedFields` and `ModelOption.TruncatedFields`. State it, and state the consequence: a producer that cuts a long question or description has nowhere to report the cut, so #1965 must reject an over-long field fail-closed rather than silently truncate it, or come back and add the field. Without this sentence #1965 reads an unbounded type and assumes cutting is free.
10. **`QuestionOption` has no id, unlike `ModalOption{id,label}`** — claude's answer protocol selects by label. So whatever inbound answer #1927 designs will identify an option by a claude-authored string, and publishing that string does not make it trusted. One sentence, flagging it for #1927; this slice declares no inbound verb and grants nothing (`TypeQuestionShown`'s block has that reasoning).

## Concurrency model

None of its own — these are DTOs with no goroutines, no channels, no locks.

One concurrency constraint the design does carry, and it is load-bearing: **the marshallers must not mutate the caller's state.** Value receivers make the nil substitution land on a copy. A payload marshaller reaching through `p.Questions[i]` would mutate the caller's backing array, which for a payload shared between an emitter goroutine and a per-connection fan-out is a data race. Its JSON output is byte-identical to the correct implementation's, so only the test's trailing assertion can catch it — see Testing strategy.

## Error handling

No failure modes. `MarshalJSON` returns whatever `json.Marshal` returns on the aliased value; there is no branch that can construct an error of its own. Decoding is stdlib `encoding/json` with no custom `UnmarshalJSON` — a malformed frame is the caller's `json.Unmarshal` error, and #1965 owns what a decoder does with it.

Explicitly **not** here: bounds enforcement, header-length checks, charset validation, truncation. All named above with their owners.

## Testing strategy

Hermetic `internal/protocol` tests in `questions_test.go`. **No fixtures, no capture read** — fixtures are #1964's, and the capture is behind `e2e_realclaude`, which `make check` never compiles. Reuse `roundTripEnvelope` and `canonical` from the existing test files (same package, so they resolve).

Four tests, as bullet-pointed scenarios:

- **`TestQuestionShownPayload_RoundTrip`** — a populated batch: two questions, the first with two options and `multi_select` false, the second with three options and `multi_select` true. Golden bytes as an inline `raw := []byte(...)` literal in place of `readFixture`, then unmarshal → assert `env.Type == TypeQuestionShown` → assert every field at all three nesting levels → `roundTripEnvelope(t, env, payload, raw)`. The inline golden is what pins the wire keys, `multi_select` snake-casing included; it is a `_` where claude sends a capital `S`, and nothing else in the tree would catch that flipping.
- **`TestQuestionShownPayload_ZeroValue_RoundTrip`** — a zero-value payload carrying exactly one all-zero `Question` holding one all-zero `QuestionOption`. That single entry is the only route to the nested types' six wire keys, since a batch carrying no questions cannot reach them at all — `TestModelListPayload_ZeroValue_RoundTrip`'s reasoning, one level deeper. Guard on the *explicit* presence of `"header":""` and `"multi_select":false` in the canonical bytes before the round trip: without those two guards an `omitempty` on either field would elide the key from both sides and the round trip would go on passing, which is precisely AC 4's regression.
- **`TestQuestionShownPayload_NilQuestionsNormalises`** — subtests `value` and `pointer`; assert the bytes contain `"questions":[]` and not `"questions":null`; then assert the receiver's `Questions` is still nil.
- **`TestQuestion_NilOptionsNormalises`** — subtests `value`, `pointer` and `nested in payload`, sharing one `assertEncodings` closure (`TestSlashCommand_NilSliceEncodings`'s shape). The `nested in payload` subtest proves the entry marshaller fires *through* the payload's, and its trailing `p.Questions[0].Options != nil` assertion is **the only thing in the suite that would catch an in-place-normalising payload marshaller** — that mutant's JSON is byte-identical to the correct implementation's, so no byte check can separate them. Say so in the test's doc comment.

Two mutants worth running before believing any sole-redness claim written into a doc comment, both from #1727's retrospective:

- A pointer-receiver mutant must carry the deref (`alias(*p)`); flipping only the receiver type fails to compile and reddens the whole package instead of isolating a subtest.
- `return json.Marshal(alias(p))` will appear twice in `questions.go`. Anchor any substitution on the enclosing `func` line and `diff` the mutant before trusting its verdict. Run mutants over `go test -overlay` against a scratch copy — never edit the worktree.

Gate: `make check`.

## Line budget

The estimate sits close enough to the size-S ceiling that it is worth stating as a target rather than discovering it at turn 60. **Production ≤ 190 lines, tests ≤ 160, ≤ 350 total.** The analogue (#1727, `cee96d20`) was 375 insertions across 211 production and 164 test for two flat types with a heavier doc block — this slice adds a third type and a second nesting level but drops #1727's entire drop-reporting dimension (`DroppedCommands`, `TruncatedFields`, and the 51-entry measurement narrative behind both).

If the doc blocks are running long, the cause is restatement. Every item in § Doc-block requirements names the sibling that already argues it; cite and move on.

## Open questions

- **#1964's AC 4 says "header at most 12 characters" and this slice records the cap as documented-12-observed-14.** Those disagree, and #1964 publishes to `docs/protocol-mobile.md`, where a client author will read it as a contract. Resolve on #1964, not here — but resolve it, or the wire doc will state a bound the committed capture already violates. (#1964's own Technical Notes ask for the unit to be named, which is the same crack seen from the other side.)
- **Does #1964 keep this slice's inline goldens or replace them with `readFixture`?** Either is fine; the byte half becomes redundant once fixtures land, while the field-by-field assertions do not. #1964's call.
- **An unanswered question has no fail-safe, and nothing owns that yet.** The modal family degrades safely because `denyByClass` makes `DefaultOptionID` the deny option; #1962's argument for a separate family was precisely that a question has no deny option and no safe default. So #1927 must decide what a timed-out, dismissed, or never-answered batch does. Named here so it is a decision rather than an assumption. Out of scope for this slice.
- **Whether the #702 per-device answer gate extends to answering a question** is #1927's, alongside the inbound shape and whether dismissal reuses `modal_dismissed`. This slice declares no inbound verb and grants nothing.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** SHOULD FIX — addressed in-spec. Four claude-authored strings (`Question.Text`, `Question.Header`, `QuestionOption.Label`, `QuestionOption.Description`) cross the subprocess boundary to a client render surface, and this slice declares no bound, no charset check and no sanitization. That is correct — the bound belongs to #1965 and #1927, and a second cap here would be a second place the limit is decided — but it is only safe if it is *stated*, so doc-block requirement 8 is mandatory rather than decorative. The boundary is explicit and single: these types are the only shape the batch takes on the wire. `ConversationID` and `QuestionBatchID` are on the other side of it — daemon-asserted, never filled from claude's tool input — and requirement 2 says so, so #1927 cannot read the struct and assume otherwise.
- **[Trust boundaries]** SHOULD FIX — **added by this pass.** The shape carries no `TruncatedFields`, unlike both its siblings, so a producer cutting an over-long description has nowhere to report the cut and a client would present cut text as complete. The envelope cap is not the forcing function (a 4×4 batch is low single-digit KB against 65519), but an unbounded claude-authored string is. Requirement 9 was added to make #1965 reject fail-closed rather than truncate silently, or come back and add the field. Without that sentence the omission reads as an oversight and gets "fixed" by silent truncation.
- **[Trust boundaries]** SHOULD FIX — **added by this pass.** `QuestionOption` has no id, unlike `ModalOption{id,label}`, because claude's answer protocol selects by label. Any inbound answer #1927 designs will therefore carry a claude-authored string back into the daemon and onward into claude's tool result. Requirement 10 flags it. Adding an id here would exceed AC 1 and contradict the measured "label and description are the complete per-option key set", so this is a flag, not a shape change.
- **[Tokens, secrets, credentials]** No findings. `QuestionBatchID` is a correlation nonce whose *minting* is #1927's, mirroring #703's for `ModalID`. Requirement 3 carries the four properties across — one-time, opaque, unguessable, resolved server-side — and "unguessable" is what obliges #1927 to `crypto/rand` rather than a counter or `math/rand`. No secret is stored, logged, or compared by this slice; there is nothing here for `crypto/subtle` to protect.
- **[File operations]** Not applicable, by design decision rather than by luck: these are pure DTOs. No path is constructed, no file is opened, and the tests read no fixture — the goldens are inline literals precisely so no `testdata` path exists to traverse.
- **[Subprocess / external command execution]** No findings for this slice; one flagged downstream. No field here reaches a child as an argv element, and no `exec.Command` is involved. The family's report-only convention needs `ModelOption.Value`'s amendment for the reason under requirement 10: a client is meant to send an answer back, and publishing an option's label does not make that label trusted when it returns. `TypeQuestionShown`'s block already establishes that this frame declares no inbound verb.
- **[Cryptographic primitives]** Not applicable. No RNG, no hashing, no key material in this slice. The one crypto-relevant obligation is the nonce's, recorded above and owned by #1927.
- **[Network & I/O]** OUT OF SCOPE, owner #1927/#1965. This slice declares no maximum, so nothing here bounds the frame against the 65519-byte v2 application-envelope cap. The margin is large — the capture's single question is roughly 700 bytes and the documented ceiling is 4 questions of 4 options — but "large margin" is an argument about well-formed input, and the strings are unbounded in principle. A `TestQuestionBatchPayloads_FitV2EnvelopeCap` in the `TestBackgroundTaskPayloads_FitV2EnvelopeCap` shape is not written here because those tests are `<`-filled at *producer* caps and no producer cap exists yet; writing one now would pin a number invented for the test.
- **[Error messages, logs, telemetry]** No findings. Nothing in this slice logs, and no error path can carry a field value — `MarshalJSON` has no branch of its own. The four strings' logging discipline is the producer's call, and this slice deliberately does not mint a never-log rule that `SlashCommand` and `ModelOption` do not carry; `QueuedItem.Text`'s never-log discipline is not the analogue, since that text is phone-originated transit content and this is claude's own prose.
- **[Concurrency]** No findings — the hazard is real and the design forecloses it. An in-place-normalising payload marshaller would reach through `p.Questions[i]` into the caller's backing array; for a payload shared between an emitter goroutine and a per-connection fan-out that is a data race, and its output is byte-identical to the correct implementation's, so no byte assertion separates them. Value receivers plus two separate methods prevent it, and the `nested in payload` subtest's trailing assertion is the only thing that would catch it — both stated as requirements rather than left to taste. No goroutine is spawned, so there is nothing to leak.
- **[Threat model alignment]** OUT OF SCOPE, owner #1927, named rather than assumed. Two `docs/protocol-mobile.md` § Security model properties that hold for `modal_shown` do **not** transfer: the per-device answer gate (#702) has not been decided for questions, and the deny-by-default fail-safe has no analogue at all — `denyByClass` makes `DefaultOptionID` the deny option, and #1962's whole argument was that a question has neither a deny option nor a safe default. Both are in § Open questions so #1927 decides them rather than inheriting the modal family's answers by resemblance.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-09-01
