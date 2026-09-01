# #1974 — declare the question-dismissal wire type and its payload

Vocabulary only. One constant, one flat three-key payload, one fixture, both drift
detectors, one naming pin, and the § Question publication. Nothing emits it; the
producer is #1973.

## Files read

- `internal/protocol/codes.go` → `TypeQuestionShown`, `TypeModalDismissed` — the two
  frames this one sits between. `TypeQuestionShown`'s doc block holds the family
  argument, the drift-detector obligations, and the deferral sentence this slice
  discharges; `TypeModalDismissed` is the shape being mirrored.
- `internal/protocol/questions.go` → `QuestionShownPayload`, `Question`,
  `QuestionOption` — the nonce's role, the no-`omitempty` discipline, and the
  SECURITY paragraph whose scope this payload deliberately falls outside.
- `internal/protocol/messaging.go` → `ModalDismissedPayload` — the field-for-field
  template: `{modal_id, outcome, source}`, no `conversation_id`, no `omitempty`.
- `internal/protocol/compat_test.go` → `v2OnlyTypes`, `TestTypeConstants_V1V2Partition`,
  `TestIsKnownAppType` — the three sites a new constant must reach. The partition-size
  assertion compares against `len(all)`, so it carries no literal to bump.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes` — the second, out-of-package
  detector, and the `TypeQuestionShown` entry this one is filed beside.
- `internal/protocol/interactive_test.go` → `TestQuestionShownType_IsNotClaudesVocabulary`
  — the pin being mirrored, including its containment lattice.
- `internal/protocol/envelope_test.go` → `readFixture` — the fixture helper.
- `internal/audit/audit.go` → `SourceRemote`, `SourceLocal`, `SourceTimeout` — what the
  modal frame's three `source` values actually mean, read at the declaration rather
  than inferred from the doc.
- `docs/protocol-mobile.md` § Modal → `#### modal_dismissed`; § Question → `#### question_shown`
  — the row and subsection shapes to mirror, and the four live-prose `#1927` cites.
- `docs/knowledge/features/protocol-package-question-batch-payload.md` — carries two
  lessons that change how this is built: **provenance belongs in the field tables, not
  in a SECURITY paragraph** (#1964 security review, MUST FIX), and **an enforcement or
  carry-over table that lists only the covered cases implies the rest are covered**.
- `docs/knowledge/features/protocol-package-drift-detectors.md` — that the guard's label
  column is free text, and that a borrowed reason can ship a false statement past a
  green `make check`.
- `docs/knowledge/features/protocol-package-types-modal-v2-wire-payloads.md` — why the
  modal dismissal has no `conversation_id`, and the `Class`/`Outcome`-are-plain-strings
  posture this slice extends to `source`.

## Context

`docs/protocol-mobile.md` § Question records that there is no dismissal frame, and that
whether one is its own type or reuses `modal_dismissed` was #1927's call. #1927 is closed
and split; the call landed on this child. The answer is **its own type** —
`ModalDismissedPayload` identifies what it clears by `modal_id`, so a `question_batch_id`
arriving in that field clears the wrong panel or none.

No ADR is warranted: this is one more instance of a settled family pattern
(#1405→#1410, #1616→#1638, #1704→#1848, #1726, #1962→#1965), not a new decision class.

**Sizing.** Measured at ~435 lines of total written work — over the size-S boundary. Split
depth is capped (`parent 1927 grandparent 1906`), so per the builder's depth rule this
ships as one ticket with `needs-human:sizing` and the split-that-would-have-been recorded
on the issue.

**Overlap check.** `origin/feature/449` touches `internal/protocol/codes.go`, but #449
closed 2026-05-17 and its branch is a stale leftover that is not an ancestor of `main`
and will never merge. Not in-flight; not a blocker.

## Design

### The wire string

`TypeQuestionDismissed = "question_dismissed"`, mechanically `modal_shown`→`modal_dismissed`
applied to `question_shown`. It joins `TypeQuestionShown` in that constant's **existing
block** rather than opening a new one — the modal family keeps its four constants in one
block under one doc comment, and the deferral sentence this slice rewrites lives in that
same comment.

The `#1927` sentence in `TypeQuestionShown`'s doc block is **rewritten**, not renumbered:
it records an open question this slice closes.

### The payload

Declared in `questions.go`, the question family's file:

```go
type QuestionDismissedPayload struct {
	QuestionBatchID string `json:"question_batch_id"`
	Outcome         string `json:"outcome"`
	Source          string `json:"source"`
}
```

Field for field with `ModalDismissedPayload`, including the absences:

- **No `conversation_id`**, though `question_shown` carries one. `modal_dismissed` omits it
  for a reason that transfers whole: `question_batch_id` is the sole correlation key, and a
  shape carrying both would admit a disagreeing pair. A client holding the batch already
  knows its conversation.
- **No `omitempty`** on any field, § Modal's and § Question's discipline unchanged.
- **No `MarshalJSON`.** There is no slice field, so no `nil`→`[]` normalisation and none of
  `QuestionShownPayload.MarshalJSON`'s backing-array reasoning. A future reader must not add
  one by analogy.

### `outcome` — a producer-defined sentinel, and never a claude-authored label

`outcome` is **daemon-authored**: a sentinel the producer owns, `modal_dismissed`'s
`Outcome` posture exactly. The doc block and the published row state the rule positively:
**it must never carry a claude-authored option label.**

This is load-bearing rather than decorative, and the reason is that the natural #1907
implementation violates it. Claude's answer protocol selects an option by its **`label`**
(`QuestionOption` carries no id), so an answer path that reports "which option was chosen"
reaches for the label first — and that string crossed the subprocess trust boundary. A
client told the field is daemon-asserted would render it as trusted chrome. Holding the
sentinel rule is what keeps this frame carrying **no claude-authored byte at all**, which
in turn is what keeps it out of § Security model's threat 1, out of #1973's
no-parked-input-in-logs obligation, and length-bounded by construction. A client needing
the label reads it from the `question_shown` batch it already holds, keyed on
`question_batch_id`.

### `source` — a plain string here, because the modal set is provably incomplete

`modal_dismissed` pins `source` to the closed set `{remote, local, timeout}`. That set does
**not** carry over intact, and the published section says so per field rather than leaving a
client to assume it does:

| `modal_dismissed` value | Carries over? |
|---|---|
| `timeout` | **Yes** — #1973's elapsing approval window, bounded at `permbridge.Register` by #1912. The one member a slice in flight will emit. |
| `remote` | **Structurally yes, but nothing emits it.** A remote answer is #1907's, the answer half; #1973 broadcasts the no-answer half only. |
| `local` | **Structurally yes, same footing as `remote`** — a question parks in the same `permbridge` registry a permission does, so a desktop-TTY resolution is the same path (`audit.SourceLocal`). Also an answered outcome, so also not #1973's. |

And two of #1973's three terminal paths — **the caller disconnecting** and **the daemon
shutting down** — have **no member in that set at all**. Neither is a timeout and neither is
an answer.

So `source` is declared as a **plain string whose vocabulary the producer owns**,
documented not enforced — `modal_dismissed`'s `Outcome`/`Class` posture, not its `source`
posture — and the section names the gap instead of publishing a set it knows to be short.
This is § Attachments' rule (#1752): a closed set published ahead of the code that closes
it is worse than a named gap.

An open vocabulary obliges the reader, so the section states the reading rule too: a client
must treat an **unrecognised `source` as "resolved, cause unknown"** and never as an answer.
Getting that backwards renders a daemon safe-deny as the operator's own choice, and the two
values #1973 has yet to name are exactly the ones a client written today will not recognise.

### The nonce after retirement

`question_batch_id` is echoed back to the same audience that received it — both frames ride
the interactive capability gate, so this widens no disclosure. What the published row adds
is the other half: **receipt of `question_dismissed` is not a capability, and the nonce is
dead once it lands.** A retired batch resolves nothing server-side, the way a stale
`modal_id` resolves nothing under first-answer-wins (#703/#706). The `crypto/rand` minting
obligation is unchanged and stays #1975's — the renumbering pass re-points that sentence and
must not drop it.

## Concurrency model

None. Two declarations and a struct with three string fields; no goroutines, no shared
state, no marshaller. Named explicitly because the sibling payload's `MarshalJSON` carries
a data-race argument that does not apply here and must not be imported by analogy.

## Error handling

No failure modes: the package declares vocabulary and validates nothing, by design. The
fail-closed decode is the client's (pyrycode-desktop#849) and the bounded parse is
`internal/questionbridge`'s. Nothing here can return an error.

## Testing strategy

- `TestQuestionDismissedType_IsNotClaudesVocabulary` (`interactive_test.go`) — the naming
  pin, mirroring its `question_shown` sibling's lattice: equality against `AskUserQuestion`,
  containment on `ask` (safe only because the name carries no `task` word) and on the plural
  `questions`, **equality** against `TypeModalDismissed`, and the exact pin. The
  `modal_dismissed` check is an equality and not a containment because `dismissed` is a
  substring of both names, so a containment check on the shared word would be red against
  the correct name — the same containment trap the singular `question` poses one step over.
  It is subsumed by the exact pin as a predicate but not as a standing bar: the exact pin's
  literal is edited by whoever performs a rename, while a cross-constant check is not.
- `TestQuestionDismissedPayload_RoundTrip` (`questions_test.go`) — decode
  `testdata/question_dismissed.json`, assert all three fields, re-encode and compare bytes
  via `roundTripEnvelope`.
- A zero-value key-presence assertion in the same test — marshal a zero
  `QuestionDismissedPayload` and require all three keys present, which is what catches
  `omitempty`. #1963's zero-value presence loop is the idiom.
- Drift detectors, all in the same commit as the constant: the `question_dismissed-rejected`
  case in `TestIsKnownAppType`, the `v2OnlyTypes` entry and the `all` slice entry in
  `TestTypeConstants_V1V2Partition`, and `excludedTypes["TypeQuestionDismissed"] = "push"` in
  `cmd/pyry/relay_guard_test.go`. Filed a **push** and not a verb for `TypeQuestionShown`'s
  recorded reason.

**Mutants, run over a `go test -overlay` scratch copy rather than predicted** (#1718's
lesson, re-confirmed by #1964): `omitempty` on each of the three keys, each of the three key
renames, and one field reordering — seven in all, per key rather than assumed. The fixture's
three values must be **pairwise distinct** or the reordering mutant is undetectable.

## Open questions

1. Does the existing question const block's doc comment stay one comment covering two
   constants, or does the dismissal need its own? — resolve when editing `codes.go`.
2. Is `local` reachable for a question at all, or only for a permission? Published as
   *structurally applicable, nothing emits it*, which is true either way; if the code says
   otherwise, tighten the row.
3. Does the reordering mutant actually redden? It depends on the fixture's values being
   distinct — verify by running it, not by reading the fixture.

## Renumbering — the `#1927` forward references

`grep -rn '#1927' --include='*.go' .` returns 11 sites in 5 files. Nine are renumbered here;
`internal/questionbridge`'s two are #1975's and are **left alone**.

| Site | Becomes |
|---|---|
| `codes.go`, the dismissal-deferral sentence | **rewritten** — names `TypeQuestionDismissed` |
| `codes.go`, the declaring-ticket sentence's producer | #1973 |
| `questions.go`, the header block's producer / nonce mint | #1973 / #1975 |
| `questions.go`, `QuestionShownPayload.QuestionBatchID`'s `crypto/rand` obligation | #1975 |
| `questions.go`, the self-describing-to-the-producer sentence | #1973 |
| `questions.go`, `Question`'s bound owner | #1973 |
| `questions.go`, `QuestionOption`'s inbound answer | **#1907** |
| `relay_guard_test.go`, both `TypeQuestionShown` entry cites | #1973 |

Four live-prose cites in `docs/protocol-mobile.md` § Question move in the same pass: the
`question_shown` message-types row (→ #1973), the `question_batch_id` field row's mint
obligation (→ #1975), the *"Nothing emits this frame yet"* paragraph (producer → #1973, mint
→ #1975), and the paragraph the publication rewrites (inbound answer → #1907). **The
changelog entries below them are historical and stay untouched.**

## Doc publication

- One **application-message-types** row for `question_dismissed`, mirroring `question_shown`'s.
- One **`#### question_dismissed`** subsection under § Question, in `#### question_shown`'s
  shape: a direction line and a field table **carrying a Provenance column** (#1964's
  security-review MUST FIX — provenance belongs in the table, not in a paragraph below it).
- The *"There is no answer frame and no dismissal frame today"* paragraph loses its
  dismissal half and keeps its answer half, which is still true and is #1907's.
- No live heading count moves: § Interactive events' **fifteen** is counted from `turn_state`
  through `model_announced`, and this `#### ` heading sits under § Question, well past it.

## Security review

**Verdict:** PASS (after one MUST FIX revised into § Design before this commit)

**Findings:**

- **[Trust boundaries] MUST FIX — addressed in § Design, "`outcome` — a producer-defined
  sentinel".** The first draft copied `ModalDismissedPayload` field for field and marked all
  three fields daemon-asserted. That is true of the frame as designed and **false of the
  frame as #1907 will naturally implement it**: `QuestionOption` carries no id and claude's
  answer protocol selects by **`label`**, so an answer path reporting which option was chosen
  reaches for a claude-authored string first, and drops it into `outcome`. A client that read
  "daemon-asserted" in the field table then renders a subprocess-authored string as trusted
  chrome — precisely the failure #1964's own security review caught in prose form. The fix is
  a positive rule published in the doc block and the field row rather than a warning:
  `outcome` is a producer-defined sentinel and **must never carry a claude-authored option
  label**; a client needing the label reads it from the `question_shown` batch it already
  holds, keyed on `question_batch_id`. Foreclosed structurally, the way the modal frame's
  single-correlation-key shape forecloses cross-conversation confusion.
- **[Trust boundaries] No further findings.** There is no parse and no boundary crossing in
  this package: the frame is constructed daemon-side by #1973 and decoded client-side by
  pyrycode-desktop#849. Provenance is carried **per field in the table**, not in a paragraph
  below it — #1964's shipped correction, applied here by default rather than after review.
- **[Tokens, secrets, credentials] SHOULD FIX — folded into § Design, "The nonce after
  retirement".** `question_batch_id` is echoed to the same capability-gated audience that
  received `question_shown`, so disclosure does not widen. Two properties were unstated and
  now are: receipt is **not a capability**, and the nonce is **dead after this frame** — a
  retired batch resolves nothing server-side (#703/#706 first-answer-wins). Lifecycle:
  creation and rotation are #1975's `crypto/rand` mint, revocation *is* this frame, expiry is
  #1912's bound at `permbridge.Register`. A hazard specific to this slice: the renumbering
  pass rewrites the sentence carrying the `crypto/rand` obligation, and a careless re-point
  deletes the obligation along with the ticket number.
- **[Network & I/O] No findings, contingent on the sentinel rule.** Three short strings, no
  array, no nesting — nothing approaching `maxV2AppEnvelope`, and no attacker-influenced
  length, because all three values are daemon-minted or daemon-chosen. That bound is a
  *consequence* of the `outcome` rule, not independent of it: an `outcome` carrying a
  claude-authored label would make this frame's length subprocess-controlled, in a family
  that deliberately ships **no `truncated_fields`** and so could not report a cut.
- **[Error messages, logs, telemetry] No findings.** Nothing in this package logs. #1973's
  AC 5 forbids any byte of the parked input on that path, and under the sentinel rule this
  frame is structurally incapable of carrying one — there is no field a question, header,
  label or description can reach.
- **[Concurrency] No findings, and the sibling's argument does not transfer.** No goroutine,
  no shared state, no `MarshalJSON`. `QuestionShownPayload.MarshalJSON`'s backing-array data
  race exists because a normaliser could reach through `p.Questions[i]`; this payload has no
  slice field, so adding a normaliser here by analogy would import a hazard it does not have.
  Stated in § Design so a future reader does not.
- **[File operations] Not applicable by design.** No path is constructed from any value. The
  only file the package touches on this path is a testdata fixture opened by `readFixture`
  under a constant name, in tests.
- **[Subprocess / external command execution] Not applicable by design.** No `exec`. The
  subprocess boundary sits upstream in `internal/questionbridge`, whose fail-closed parse
  (#1965) has already run before anything could produce this frame.
- **[Cryptographic primitives] Not applicable by design** — no primitive is used or chosen
  here. The one cryptographic obligation in scope is the nonce's `crypto/rand` mint, which is
  #1975's and is restated rather than dropped; see the Tokens finding.
- **[Threat model alignment] No findings, and the asymmetry is the useful result.**
  § Security model's threat 1 (prompt injection, `severity: high`, `mitigation: partial`)
  lands on a remote render surface for `question_shown`, whose four strings are
  claude-authored. It does **not** land for `question_dismissed`, which carries no
  claude-authored byte — entirely because of the `outcome` rule. That is worth publishing
  next to the frame rather than leaving a client to re-derive: the two frames of one family
  sit at different trust tiers.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-01
