# #1983 — declare the inbound question-answer and question-refusal wire types

**Ticket:** [#1983](https://github.com/pyrycode/pyrycode/issues/1983) — `feat(protocol): declare the inbound question-answer and question-refusal wire types`
**Size:** `size:s`, `security-sensitive`, `needs-human:sizing`
**Branch:** `feature/1983`

## Files read

- `internal/protocol/codes.go` → `TypeQuestionShown` / `TypeQuestionDismissed` doc block — the family's const-block shape, the four-registry classification prose, and the sentence *"the inbound answer verb is #1907's, to be declared with the handler that serves it"* that this slice makes false. `TypeAttachmentChunk`'s block is the one honest `excludedTypes` precedent for a handler-less inbound leg. `TypeModalAnswer` / `TypeModalCancel` are the inbound-control pair this family mirrors.
- `internal/protocol/questions.go` → `QuestionShownPayload`, `Question`, `QuestionOption`, `QuestionDismissedPayload` and the two `MarshalJSON` normalisers — the doc discipline, the no-`omitempty` rule, the nil→`[]` rationale (stated once on the payload's normaliser and referred to from the entry's), and the value-receiver + type-alias idiom. Also the four live `#1907` cites this slice re-points.
- `internal/protocol/messaging.go` → `ModalAnswerPayload`, `ModalCancelPayload` — the inbound pair whose shape and `answer_token` semantics carry over; `ModalAnswerPayload` is where "idempotency key, not authorization" is written down.
- `internal/protocol/questions_test.go` → `TestQuestionDismissedPayload_RoundTrip`, `TestQuestionDismissedPayload_ZeroValue_KeysPresent`, `TestQuestionShownPayload_MarshalJSON_DoesNotMutateCaller` — the three test shapes this slice copies, and the recorded reason a populated fixture is blind to every `omitempty`.
- `internal/protocol/envelope_test.go` → `readFixture`; `internal/protocol/interactive_test.go` → `roundTripEnvelope` — the two helpers every payload test in this package uses. `roundTripEnvelope` compares canonical bytes, which is why fixture values must be pairwise distinct.
- `internal/protocol/compat_test.go` → `TestIsKnownAppType`, `v2OnlyTypes`, `TestTypeConstants_V1V2Partition` — two of the four registries a new constant must enter.
- `cmd/pyry/relay_guard_test.go` → `inboundTypes`, `excludedTypes`, `TestEveryInboundV2TypeHasHandler` — the third registry, and the assertion that makes `inboundTypes` red for a handler-less type.
- `internal/protocol/envelope.go` → `inboundAppTypeSet` — the registry these constants must **not** enter (v2 control types are intercepted before `dispatch.Route`).
- `docs/protocol-mobile.md` § Question (v2), § Modal (v2), § Application message types → the section this slice extends and the seven live-prose correction sites.
- `docs/knowledge/features/` — no package overview exists for `internal/protocol`; the family's accumulated lessons live in § Question (v2)'s changelog entries, which is where the fixture/mutant findings above came from.

## Context

`question_shown` carries claude's `AskUserQuestion` batch out to interactive clients and `question_dismissed` retires it. The family has no inbound leg at all: § Question (v2) says so in its own words, and `internal/protocol/codes.go` records the omission as deliberate — a verb declared without its handler is red by construction under `TestEveryInboundV2TypeHasHandler`.

This slice closes the vocabulary gap only. Two inbound v2 control types and their payloads are declared, published in `docs/protocol-mobile.md`, and filed in every drift detector. **Nothing intercepts, decodes or acts on either frame here.** #1984 owns the `dispatchAppFrame` cases (and the `excludedTypes` → `inboundTypes` move that Assertion #2 then makes mandatory); #1985 owns resolution against the daemon's parked batch. The external consumer is [pyrycode-desktop#853](https://github.com/pyrycode/pyrycode-desktop/issues/853).

**No ADR is warranted.** Every design call here is a restatement of one the family already made and recorded: distinct types over a nullable flag (`question_dismissed`, #1974), `answer_token` as idempotency-not-authorization (`modal_answer`, #703), and `internal/protocol` enforcing no bounds (#1963/#1965). The documentation phase should fold the index-selection and values-are-opaque findings into § Question (v2)'s changelog rather than mint a decision record.

### Sizing — re-derived, and why this builds rather than splits

The ticket trips the `size:s` total-written-work boundary and carries `needs-human:sizing`. Re-checked independently rather than deferred to:

| Boundary | This ticket | Verdict |
|---|---|---|
| Production source files | 2 (`codes.go`, `questions.go`) | within ≤ 3 |
| New exported types | 3 (`QuestionAnswerPayload`, `QuestionAnswerEntry`, `QuestionRefusedPayload`) | within ≤ 5 |
| Consumer call sites | 0 — nothing constructs or decodes these | within ≤ 10 |
| Acceptance criteria | 4 | within ≤ 5 |
| Reject branches | 0 — this package enforces no bounds | within ≤ 10 |
| **Total written work** | **~800–900** | **exceeds 400** |

Analogue counts re-derived from the tree rather than copied (builder-written = spec + implementation, excluding the documentation phase's fold): **#1963 = 638** (163 + 475), **#1974 = 596** (300 + 296), **#1975 = 745** (211 + 534). All three shipped `size:s`; none parked.

**Split depth does not fire, and that is recorded as its own reason.** `#1983 → #1907 → none`. The `none` is honest rather than a missing sub-issue link: siblings #1962/#1974/#1980 all read `grandparent=1906`, so the family root's children are linked, and #1906's own sub-issue list is `{#1925, #1926, #1927, #1928}` — #1907 is not among them. #1907 is a peer epic of #1906 (outbound vs answer half), not its child. So the depth gate is not what keeps this whole.

**What keeps it whole is that no cut brings a child under 400.** Two seams were priced:

- *By frame* (`question_refused` / `question_answer`). The fixed cost — the `codes.go` const-block doc, the § Question (v2) scaffolding, the AC-4 correction sweep and the spec doc — is ~385 lines and cannot be shed by either child, because both children edit the same const block, the same doc section and the same test file. Priced: ~580 / ~505. Total rises from ~850 to ~1085 and **neither child clears the line**.
- *By layer* (constants+registries / payloads+fixtures / docs+sweep), the seam `slash_command_list` cut at #1726 = 153 and #1727 = 378. Here: ~250 / ~500 / ~300. The payload child is three types and two normalisers — #1963's exact shape, which measured 638 — so it **still does not clear the line**, and a fourth cut would make a ticket out of one struct.

That is the measurement, not the rationalization: ~385 of fixed cost against ~465 of behaviour, with the behaviour indivisible below ~500 at this package's doc density. Reported as a tension with the boundary rather than as a licence: five consecutive siblings in `internal/protocol` landed at 596–745 without parking, which says what this package has gotten away with, not what a boundary should be. `needs-human:sizing` stays on the ticket as the marker.

### File-overlap check

`git fetch origin --prune` then a scan of every `origin/feature/<n>` branch against the six files this design touches returned one hit: `origin/feature/449` touches `internal/protocol/codes.go`. **Not a live overlap** — #449 is CLOSED (2026-05-17), its branch head is from 2026-05-17 with no PR ever opened, and it is a stale leftover rather than in-flight work. No blocker set, no rework label.

## Design

### Wire types (`internal/protocol/codes.go`)

One new const block beside the `question_shown` / `question_dismissed` one:

```go
const (
	TypeQuestionAnswer  = "question_answer"  // phone → binary, inbound v2 control (interception is #1984's)
	TypeQuestionRefused = "question_refused" // phone → binary, inbound v2 control (interception is #1984's)
)
```

The block's doc carries five things the family's blocks establish and this one must state for itself:

1. **Two types, not one nullable flag.** A refusal carries no answers; the family's precedent is `question_dismissed` being its own type rather than a reused `modal_dismissed`. The inbound pair mirrors `modal_answer` / `modal_cancel`.
2. **The guard classification, and why it differs from the neighbours'.** `modal_answer` / `modal_cancel` sit in `inboundTypes` as `"switch-intercepted"` because `dispatchAppFrame` has cases for them. This slice ships no handler, so `inboundTypes` fails Assertion #1 by construction, and filing under `excludedTypes` as a `"push"` — the reason the seven neighbours give — would be false for a genuinely inbound frame. `TypeAttachmentChunk: "pending handler (#1744)"` is the one honest precedent; these follow it as `"pending handler (#1984)"`.
3. **Not in `inboundAppTypeSet`** (`envelope.go`): v2 control types are intercepted before `dispatch.Route`, and a leak would route an inbound control envelope into the v1 handler chain.
4. **The naming.** `question_answer` / `question_refused` are the daemon's names, not claude's, for the reason `TypeQuestionShown`'s block gives. The subject-noun trap cuts here too: the singular `question` is a substring of `AskUserQuestion`, so neither name may be probed with a `strings.Contains` on it.
5. **What is not declared.** The vendor `response` field (claude's optional top-level freeform reply that replaces `answers` entirely) is deliberately not carried — decided 2026-08-31 in both repos, available later with no wire change.

The sentence in `TypeQuestionShown`'s block asserting that the inbound answer verb is #1907's and must be declared with its handler becomes false here and is rewritten in the same commit, naming what actually happened: the verb is declared vocabulary-only under the `AttachmentChunk` precedent, and #1984 supplies the handler.

### Payloads (`internal/protocol/questions.go`)

Three types and two normalisers, appended after `QuestionDismissedPayload`. Contracts only:

```go
type QuestionAnswerPayload struct {
	QuestionBatchID string               `json:"question_batch_id"`
	AnswerToken     string               `json:"answer_token"`
	Answers         []QuestionAnswerEntry `json:"answers"`
}
func (p QuestionAnswerPayload) MarshalJSON() ([]byte, error) // nil Answers → []

type QuestionAnswerEntry struct {
	QuestionIndex int      `json:"question_index"`
	Values        []string `json:"values"`
}
func (e QuestionAnswerEntry) MarshalJSON() ([]byte, error) // nil Values → []

type QuestionRefusedPayload struct {
	QuestionBatchID string `json:"question_batch_id"`
	AnswerToken     string `json:"answer_token"`
}
```

Decisions the doc blocks must carry:

- **No `conversation_id` on either payload.** `QuestionDismissedPayload`'s reason transfers whole: the batch id is the sole correlation key, and a shape carrying both admits a disagreeing pair someone has to adjudicate. This is also the security-relevant half — the daemon never trusts a phone-asserted conversation, exactly as `modal_answer` carries none.
- **Selection is positional.** `QuestionIndex` is the entry's index into the batch's `questions` array — the canonical order a client already renders — rather than the question text echoed back. `QuestionOption` carries no id and claude selects by label, so the alternative would carry a claude-authored string across the boundary in both directions for no gain. The daemon reads the question text from its own parked copy.
- **The index is carried, never range-checked here.** `internal/protocol` enforces no bounds by design, exactly as `questionbridge.Parse` rather than this package enforces the outbound batch bounds. The consequence is loud and belongs to #1985: a negative or over-large index used to subscript the parked batch panics, so the resolver owes an explicit range check before indexing. The doc block states that as an obligation on the decoder, not as an implied guarantee of the type.
- **`int`, not an unsigned type.** A `uint` would reject `-1` at decode and accept `1<<62`, half-closing the door while turning a range problem into a decode-error surprise, and it would smuggle a bounds decision into this package against the rule above. Plain `int`, with the obligation named.
- **`Values` are opaque, client-authored strings, never checked against the batch's labels.** claude's contract permits free text anywhere and requires no value to be one of the offered labels. A validator that rejected an unlisted value would reject a legal answer.
- **`Answers` array order is not the correlation.** The array is ordered and a client should emit entries in batch order, but `question_index` is what selects — so a decoder must not infer the question from an entry's array position. Duplicate and missing indices are the decoder's problem (#1985), not this type's.
- **`AnswerToken` is `ModalAnswerPayload`'s field verbatim**, including its reasons: a client-minted idempotency key whose uniqueness and stability matter and whose secrecy does not, and which is **not** the authorization. The daemon's actual dedup is the one-shot consume of `question_batch_id`, as `modal_answer`'s is of `modal_id`.
- **No `omitempty` on any field**, § Modal's and `QuestionShownPayload`'s discipline unchanged.
- **`QuestionAnswerEntry`, not `QuestionAnswer`.** The nested types under `question_shown` are `Question` and `QuestionOption`, neither of which collides with its payload's name. `QuestionAnswer` beside `QuestionAnswerPayload` differs only by the family's frame-body suffix, so the pair reads as *"the payload of a QuestionAnswer"* rather than *"the body of a `question_answer` frame"*. The suffix buys the disambiguation the sibling nested types did not need.
- **The entry's normaliser cannot be folded into the payload's** — `Question.MarshalJSON`'s argument transfers verbatim: a payload marshaller normalising entries in place reaches through `p.Answers[i]` into the caller's backing array (a data race as well as a correctness bug for a payload shared across goroutines), and would not fire when an entry is marshalled alone. The shared nil→`[]` rationale is stated once on the payload's method and referred to from the entry's, as `QuestionShownPayload` / `Question` do.
- **`QuestionRefusedPayload` gets no `MarshalJSON`** — no slice field, so no normalisation to perform; `QuestionDismissedPayload`'s explicit "do not add one by analogy to the sibling" note applies.

### Registries — all four, moving independently

| Registry | Location | Entry |
|---|---|---|
| `inboundAppTypeSet` | `internal/protocol/envelope.go` | **absent** — v2 control types are intercepted pre-`dispatch.Route` |
| `v1TypeSet` | `internal/protocol/compat.go` | **absent** — plus an `IsKnownAppType` → `ErrUnknownType` row each in `TestIsKnownAppType` |
| `v2OnlyTypes` + `TestTypeConstants_V1V2Partition`'s `all` | `internal/protocol/compat_test.go` | **present**, both lists |
| `excludedTypes` | `cmd/pyry/relay_guard_test.go` | **present** as `"pending handler (#1984)"` — **not** `inboundTypes` |

### Documentation (`docs/protocol-mobile.md`)

- A `#### question_answer` and a `#### question_refused` subsection under § Question (v2), placed after `#### question_dismissed`, each with a per-field provenance table in the shape § `question_shown` uses. The prose states: the index selects into the daemon's parked batch; values are never checked against the offered labels; the vendor `response` field is deliberately not carried; `answer_token` is idempotency, not authorization; and nothing decodes either frame yet.
- One row each in § Application message types, styled on `modal_answer` / `modal_cancel` and placed with the question family so that section stays contiguous.
- A dated § Changelog entry **appended** — the existing dated entries are historical records and are not rewritten, including the `2026-09-01` one that misnames the answer frame as #1927's.

### AC 4 — the correction sweep, enumerated

Every live-prose claim that no inbound answer verb exists, and every live `#1907` cite, in the four files this slice already edits. Verified against `2a2847d5` by `grep -n 1907` plus a wording sweep for the numberless claims:

| File | Site (by symbol / section) | Correction |
|---|---|---|
| `docs/protocol-mobile.md` | § Application message types, `question_shown` row | *"no inbound verb at all"* → the pair now exists; the contrast with `modal_shown` becomes nesting depth, not verb absence |
| `docs/protocol-mobile.md` | § Application message types, `question_dismissed` row | *"the answered one is still #1907's"* → #1985 |
| `docs/protocol-mobile.md` | § Question (v2) intro | *"there is still no inbound answer verb"* → declared here, undecoded until #1984/#1985 |
| `docs/protocol-mobile.md` | § Question (v2), **No inbound verb is declared** paragraph | rewritten around the pair that now exists; keeps the untrusted-on-return rule, which the positional index strengthens rather than retires |
| `docs/protocol-mobile.md` | § `question_dismissed` intro line | *"the answered one is #1907's"* → #1985 |
| `docs/protocol-mobile.md` | § `question_dismissed`, `outcome` row | *"vocabulary owned by #1973/#1907"* → #1973/#1985 |
| `docs/protocol-mobile.md` | § `question_dismissed`, carry-over table `remote` / `local` rows | #1907 → #1985 |
| `internal/protocol/codes.go` | `TypeQuestionShown` doc block | *"the inbound answer verb is #1907's, to be declared with the handler that serves it"* → false as of this commit; rewritten per § Wire types |
| `internal/protocol/questions.go` | package doc block; `QuestionOption` doc; `QuestionDismissedPayload` doc; `QuestionDismissedPayload.Outcome` doc | four #1907 cites → #1983 (vocabulary) / #1984 (routing) / #1985 (resolution) |

Out of scope, deliberately: the `#1907` cites under `cmd/pyry/`, `internal/questionbridge/`, `docs/specs/` and `docs/knowledge/` — those belong to the slices that own those files. #1974's own commit did the identical bounded re-point for #1927.

## Concurrency model

None introduced. This slice adds pure data types and their serialization; no goroutines, no channels, no shared mutable state.

The one concurrency-relevant decision is negative and is inherited: both `MarshalJSON` methods take a **value receiver** and substitute into the copy, so a payload shared between an emitter goroutine and a per-connection fan-out is never mutated through its backing array. `QuestionShownPayload.MarshalJSON`'s rationale, carried across and pinned by a test rather than asserted.

## Error handling

`internal/protocol` returns no errors on this path by design — the only error either method can produce is `json.Marshal`'s on the aliased struct, which for a struct of strings, an `int` and string slices is unreachable in practice and is returned rather than swallowed.

The failure modes this shape **hands to its decoder**, stated in the doc blocks and in § Question (v2) so #1984/#1985 inherit them explicitly rather than by omission:

| Failure mode | Owner | Consequence if unhandled |
|---|---|---|
| `question_index` negative or ≥ `len(questions)` | #1985 | panic on subscript of the parked batch |
| duplicate or missing `question_index` across entries | #1985 | a silently partial or last-write-wins answer |
| `answers` array longer than the parked batch | #1985 | unbounded work per inbound frame |
| `values` containing a string no option offers | nobody — legal by claude's contract | a validator here would reject a legal answer |
| unrecognised or already-consumed `question_batch_id` | #1985 | resolves nothing (the one-shot consume is the dedup) |
| decode failure on the payload itself | #1984 | must be a rejected frame, never an empty-but-successful answer |

## Testing strategy

Two committed fixtures under `internal/protocol/testdata/` (ids and values are placeholders; neither length nor shape is a contract), plus marshalled-zero pins for what a populated fixture cannot reach:

- `question_answer.json` — **populated**: two entries, one single-valued and one multi-valued, every value pairwise distinct. `roundTripEnvelope` compares canonical bytes, so distinct values are what makes a field-reordering mutant detectable at all (#1974's finding).
- `question_refused.json` — both keys, distinct values.

Test scenarios:

- `TestQuestionAnswerPayload_RoundTrip` — envelope type, the three payload keys, both nested entry keys at both nesting levels, canonical-byte round trip against the fixture.
- `TestQuestionAnswerPayload_ZeroValue_KeysPresent` — the `omitempty` pin the round trip is structurally blind to (the fixture's values are all non-empty), and the pin that `"answers":[]` is emitted rather than `null`.
- `TestQuestionAnswerEntry_ZeroValue_KeysPresent` — the same one level down. **This is the one a populated fixture cannot buy**: an empty `answers` array reaches none of the entry's keys, and `"values":[]` bytes are produced only by marshalling a constructed nil, never by decoding the fixture (#1964's finding, one level down).
- `TestQuestionAnswerPayload_MarshalJSON_DoesNotMutateCaller` — both receivers, including the backing-array reach-through through `p.Answers[i]`.
- `TestQuestionAnswerPayload_EmptyArraysDecodeNonNil` — decoding `[]` at both levels yields a non-nil empty slice that re-encodes as `[]`, which is the `[]`-never-`null` invariant in the inbound direction.
- `TestQuestionRefusedPayload_RoundTrip` and `TestQuestionRefusedPayload_ZeroValue_KeysPresent` — `QuestionDismissedPayload`'s pair, flat payload, same reasoning.
- Registry coverage is assertion-based and comes free: `TestIsKnownAppType` rows, `TestTypeConstants_V1V2Partition`, and `TestEveryInboundV2TypeHasHandler`'s Assertion #3 (an unclassified constant is reported the moment it exists).

**Mutants to run, not predict** (via `go test -overlay` over a scratch copy, no worktree writes): `omitempty` on each of the seven keys; each of the seven key renames; a field reordering in each of the three structs; and deleting each normaliser. The claim to check is that each mutant reddens at least one test, and which test — #1974's carried result is that a round trip is blind to every `omitempty` while a marshalled-zero check is blind to reordering, so the two classes must stay disjoint and both present.

**Gate:** `go test -race ./internal/protocol/... ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`. The full-module race suite and `make check` are the verifier's.

## Open questions

1. **Wire key for the index — `question_index` or `index`?** Resolved at design time in favour of `question_index`: an entry logged or quoted on its own is self-describing, and `index` beside an `answers` array invites the reading *"index of this answer"* rather than *"index of the question this answers"*. Recorded here rather than left open.
2. **Does § Application message types want the new rows beside the question family or beside `modal_answer` / `modal_cancel`?** The AC says *"alongside `modal_answer` / `modal_cancel`"*, which is satisfiable as placement or as styling. Resolved as styling: the question rows are contiguous in that table today and splitting the family across it to sit beside the modal rows would make the section harder to read, not easier. If implementation shows the modal rows are adjacent anyway, no revision is needed.
3. **Does the `answers`-longer-than-the-batch case want a documented cap here?** Expected answer: no — it is the same bounds question `internal/protocol` already declines for the outbound batch, and #1965 put the batch's cap in `questionbridge.Parse`. To be confirmed against the § Contract bounds table while writing the doc section, and recorded under `## Revisions` if it changes anything.

## Security review

**Verdict:** PASS

The category that bounds most of the others was checked against the tree rather than assumed: **declaring these constants changes no runtime path.** `dispatchAppFrame`'s v2 control switch has no `default` arm, so an unmatched type falls through to `dispatch.Route`, which has no handler for either name and emits its unknown-type reply — and it does so identically whether or not Go has a constant for the string. A Go constant is not a registry. The frames a client can send today, and the daemon's answer to them, are exactly what they were before this commit.

**Findings:**

- **[Trust boundaries]** No finding *in this slice*, one **SHOULD FIX** for what it hands on. These are the family's first inbound frames: they carry bytes from a paired-but-not-trusted remote party into the daemon. The boundary itself is not here — it is #1984's decode and #1985's resolution — and the risk is that "vocabulary only" ships a shape whose safe use is recorded only in this spec, which #1985's builder will not read. **SHOULD FIX:** the range-check obligation on `QuestionIndex`, the "values are opaque and never checked against labels" rule, and the "a decode failure is a rejected frame, never an empty-but-successful answer" rule must land in the Go doc blocks on `QuestionAnswerEntry` / `QuestionAnswerPayload` **and** in § Question (v2), not only in the § Error handling table above. The failure they prevent is concrete: subscripting the parked batch with an unchecked `question_index` panics on any negative or over-large value a hostile client sends.
- **[Tokens, secrets, credentials]** No exploitable finding; one **SHOULD FIX**. `answer_token` is client-minted, its uniqueness matters and its secrecy does not, and it is **not** the authorization — `ModalAnswerPayload`'s contract verbatim, and the daemon's real dedup is the one-shot consume of `question_batch_id`. No token is generated, stored, rotated or revoked by this slice, so lifecycle is not applicable here; `question_batch_id`'s `crypto/rand` obligation is #1975's and is already discharged. **SHOULD FIX:** say in the doc that neither field is a secret and both are safe to log, so #1984's handler neither invents a redaction rule nor assumes one exists. Echoing a live batch id back inbound is the nonce's purpose, and it is echoed only within the capability-gated audience that received the batch, so it discloses nothing new.
- **[File operations]** Not applicable, and by construction rather than by omission: no code path in this slice builds a filesystem path. The only files are the two committed fixtures, read by `readFixture` under compile-time constant names.
- **[Subprocess / external command execution]** No finding, and the adjacent path is named rather than skipped. Nothing here execs. The `values` this frame carries are destined for claude's tool result via #1985 — remote-authored text flowing *toward* the model, the inverse of the family's threat 1. That grants nothing new: a paired phone can already put arbitrary text into the conversation with `send_message`, so an answer sits at exactly that trust tier and not a lower one. Worth one sentence in the doc because the shape invites the opposite reading — that an answer is more constrained than a message.
- **[Cryptographic primitives]** No finding, no new primitive, no key or nonce reuse — the only nonce is `question_batch_id`, minted elsewhere and echoed here for its designed purpose. No constant-time comparison is owed: #1985 resolves the batch id by map lookup against its own store, which is `modal_id`'s inherited posture (#703/#706) and not a re-decision, and the value is unguessable rather than a shared secret.
- **[Network & I/O]** The sharpest category, and it yields a **SHOULD FIX**. This shape is an unbounded `answers` array of unbounded `values` arrays of unbounded strings, and this package caps none of them by design. The only operative bound today is the transport's AEAD frame cap, which bounds total bytes but not entry count within a frame — so a single well-formed frame can carry many thousands of entries against a parked batch of at most four questions. That is unbounded work per frame at the *resolution* step, already assigned to #1985 in the § Error handling table. **SHOULD FIX:** the new doc subsections must carry a bounds note in the shape § `question_shown` uses — naming which bounds exist (none in this package), what actually limits the frame (the transport cap), and who owes the rest — because § Attachments' rule (#1752) is that a client author reads an undocumented bound as *no bound exists*. **No number is published**, since publishing a figure the code does not enforce is the failure that rule exists to prevent.
- **[Error messages, logs, telemetry]** One **SHOULD FIX**, forward-looking. Nothing in this slice logs or formats an error containing payload bytes. The rule the next slice needs is that a decode-failure error must **not** embed the raw payload: it is remote-authored and may carry terminal escape sequences, and the family already ships no sanitization on either direction of this path. One sentence in the doc block discharges it.
- **[Concurrency]** No finding; the one real trap is pinned by a test rather than asserted. Both `MarshalJSON` methods take a **value receiver** and substitute into the copy, so neither mutates a caller's slice; the entry's normaliser is deliberately not folded into the payload's, because a payload marshaller normalising in place would reach through `p.Answers[i]` into a backing array shared between an emitter goroutine and a per-connection fan-out. `TestQuestionAnswerPayload_MarshalJSON_DoesNotMutateCaller` covers both receivers. No goroutines, no locks, no shutdown path.
- **[Threat model alignment]** One **SHOULD FIX**, and it is the finding with the most downstream consequence. § Security model's threat 1 (prompt injection reaching a remote render surface) does **not** land on either frame — neither carries a claude-authored byte. But `modal_answer` is gated by a **per-device answer gate (#702, default OFF)** on top of the `interactive` capability, and **this slice does not decide whether that gate applies to a question answer.** Left unsaid, a client author builds a sender assuming it does not and #1984's builder never learns the question was open. It is not exploitable as designed — nothing decodes these frames — so it is not a MUST FIX. **SHOULD FIX:** § Question (v2) must say explicitly that declaring the vocabulary grants no inbound capability, that both the `interactive` gate and the #702 per-device answer gate remain the handler's to apply, and that the default is deny — replacing, in the same fail-closed voice, the *"Nothing in this section grants an inbound capability"* sentence the correction sweep is rewriting anyway. Whether #702 itself extends to questions is **OUT OF SCOPE**, named for #1984/#1985.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02

## Revisions

### 2026-09-02 — the mutant sweep refined one coverage claim

Seven mutants were run over a `go test -overlay` scratch copy rather than predicted (`omitempty` on `question_index` / `values` / `answers` / `answer_token`, a `question_index` key rename, and each normaliser's substitution deleted). **All seven redden at least one test**, so the design's coverage claim holds. One result differs from what § Testing strategy predicted and is recorded rather than quietly absorbed:

**`#1974`'s "a round trip is blind to every `omitempty`" does not hold here, and the reason is worth carrying.** That finding came from a payload whose every fixture value was non-empty. This fixture's first entry carries `question_index: 0` — a *legal and common* index rather than a placeholder — so `omitempty` on that key elides it from the fixture bytes too, and `TestQuestionAnswerPayload_RoundTrip` reddens alongside `TestQuestionAnswerEntry_ZeroValue_KeysPresent`. The two classes are therefore **overlapping on this one key and disjoint on the rest**, which is strictly more coverage than planned, not less.

It changes nothing about the tests that shipped: the marshalled-zero pins are still the only route to the entry's keys when `answers` is empty, and they are still the only thing that catches `omitempty` on `answers`, `values` and `answer_token`, none of which the round trip touches. The generalisable version of the lesson is that a fixture's coverage against `omitempty` is decided per key by whether that key's fixture value happens to be its zero value — which is a property to *measure* with a mutant, never to infer from the sibling ticket's summary.

**Open questions resolved.** (1) `question_index` over `index` — decided at design time, implemented as written. (2) The § Application message types placement — the `modal_answer` / `modal_cancel` rows turned out to be adjacent to the question rows anyway, so the two readings coincide and the new rows sit next to both. (3) No cap on `answers` is published: the § Contract bounds table names it as a gap owned by #1985, with the transport frame cap identified as the only operative limit, per § Attachments' rule.
