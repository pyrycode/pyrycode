# `internal/questionbridge` — claude's clarifying-question discriminant + fail-closed parse

`internal/questionbridge` (#1965, split from #1926/#1906) recognises claude's
`AskUserQuestion` tool call on the shared mcp-approve bridge and parses its
tool input into `protocol.QuestionShownPayload`
([question-batch payload](protocol-package-question-batch-payload.md)). It
ships beside [`internal/permbridge`](permbridge-package.md) and
[`internal/modalbridge`](modalbridge-package.md), named after them, and
depends on `internal/protocol` and the standard library only — not
`permbridge`, since the parse takes the `(ToolName, Input)` pair rather than
a `permbridge.Request`, so it never holds a `ToolUseID` it has no business
reading.

**This slice has no consumer.** `streamApprovalBridge.Surface`
(`cmd/pyry/modal_resolve_v2.go`) still hard-codes the permission-modal class
for every approval, question or not; #1973 is the call site that consults
`questionbridge.ToolName`/`Parse` and wires the result onto the wire.

```go
const ToolName = "AskUserQuestion"

func Parse(toolName string, input json.RawMessage) (protocol.QuestionShownPayload, bool)
```

`Parse` reports whether `toolName` is the question tool and `input` is a
well-formed, in-bounds batch. The two negative outcomes — not the question
tool, and the question tool but rejected — are deliberately not
distinguished; a caller that needs to tell them apart compares against the
exported `ToolName` itself. `ConversationID`/`QuestionBatchID` stay
unfilled — both are daemon-asserted; `Registry.Record` (#1975, below) mints
and stamps them.

## Bounds

Seven reject branches, each yielding the zero payload rather than a partial
or truncated one — the result becomes an un-droppable control frame, and
this family has no `truncated_fields` to report a cut in (mechanism borrowed
from `modalbridge`'s `maxPromptBytes` **posture**, not `boundPrompt`'s
truncate-and-continue **mechanism**): a pre-decode byte cap over the raw
input (`maxInputBytes = 16384`, taken *before* `json.Unmarshal`), a decode
failure, 1-4 questions, 2-4 options per question, and a question missing
`multiSelect`.

**The header carries no length bound of its own.** The vendor documents a
12-character cap; the only committed capture has a 14-rune header, so a
12-rune reject would reject the one real sample anyone has. `Parse` lands no
header bound and no rune-count bound at all — the four claude-authored
strings are bounded transitively, only by `maxInputBytes`. See [the
payload doc's header-cap note](protocol-package-question-batch-payload.md)
for the full resolution.

## `Registry` — parking a surfaced batch (#1975)

```go
type Registry struct { /* sync.Mutex + map[string]protocol.QuestionShownPayload */ }
func New() *Registry
func (r *Registry) Record(p protocol.QuestionShownPayload, convID string) (protocol.QuestionShownPayload, error)
func (r *Registry) Lookup(batchID string) (protocol.QuestionShownPayload, bool)
func (r *Registry) Resolve(batchID string) (protocol.QuestionShownPayload, bool)
func (r *Registry) Snapshot() []protocol.QuestionShownPayload
```

`Record` is the single mint site: it draws a `crypto/rand` UUIDv4
(`newQuestionBatchID`, `newModalID`'s shape), stamps it and the caller's
`convID` onto the payload — overwriting whatever arrived on `p`
unconditionally, since `p` traces back to claude's tool call and an adopted
id would let the caller pick its own routing key — and parks the batch
before returning. A mint failure stores nothing and returns the zero payload
plus a wrapped error, mirroring `modalbridge.Registry.Record`'s untested RNG
branch. `Resolve`'s read-and-delete is one critical section, which is what
makes the one-shot consume — "exactly one broadcaster" between #1907's
answer path and #1973's retire backstop — structural rather than agreed
between tickets. `Lookup` and `Snapshot` are pure reads; nothing calls any of
the four yet, deliberately, as #1965 landed with no caller.

**No stored-entry type**, unlike `modalbridge.Outstanding`: the parked thing
*is* the stamped `protocol.QuestionShownPayload`, so a second type would only
buy a field-by-field copy in and out.

**Clone-on-read goes one nesting level deeper than `modalbridge`'s.**
`Question.Options` is itself a slice, so a plain `slices.Clone` over
`[]protocol.Question` still leaves every option slice aliased to the stored
backing array. `cloneQuestions` clones the outer slice and each element's
`Options`; applied on write in `Record` and on read in `Lookup`/`Snapshot`,
never in `Resolve` (the entry is deleted in the same critical section, so
there's no kept copy left to alias).

### Trap: a field-by-field composite literal for a same-typed map value silently drops a future field

`Record` parks the batch as a fresh `protocol.QuestionShownPayload{...}`
composite literal (`ConversationID`, `QuestionBatchID`, `Questions` named
individually) rather than cloning `p` itself and overwriting just those
three. Because the map's value type is the same `QuestionShownPayload` the
caller handed in, this costs nothing today — it's field-complete against the
current three-field struct — but nothing catches it if the struct grows a
fourth field: the composite literal silently zeroes the new field in every
parked copy while the broadcast frame (built from `p`) carries it, so
`Lookup`/`Snapshot` hand back a batch that has quietly diverged from what was
surfaced. Go gives no compiler warning for a keyed literal that omits a
field, and a test fixture built the same way (a struct literal that also
predates the new field) won't catch it either — the drift is only visible by
reading `Record` against the current field list of
[`protocol.QuestionShownPayload`](protocol-package-question-batch-payload.md).
The next ticket that adds a field to that type should change `Record` to
copy `p` and override only `Questions`, `ConversationID`, `QuestionBatchID`,
rather than re-listing fields. (Flagged in code review on PR #1977 as
non-blocking; shipped as-is.)

### No expiry — deliberate, and owned elsewhere

The registry has no TTL and nothing bounds how long a batch can stay
outstanding. This is deliberate, not an oversight: the terminal no-answer
paths (caller disconnect, daemon shutdown, timeout) belong to #1973's retire
backstop, which calls `Resolve`. A TTL added here would be a second retire
authority that could disagree with that one. Until something calls
`Resolve`, a batch stays in the map — the growth bound is entirely "however
many callers actually let go", enforced outside this package.

### Testing: a shallow-clone regression needs a ≥2-element nested fixture to catch

A `go test -overlay` mutant reducing `cloneQuestions` to a bare
`slices.Clone` (dropping the per-element `Options` clone) reddens only the
assertions that mutate a returned question's *nested* `Options` element — a
fixture with one option per question, or an isolation test that only mutates
a top-level `Question` field, ships the shallow clone green. The registry's
isolation test uses a two-question / two-option fixture and mutates at both
nesting levels across all three hand-out sites (`Record`'s return, `Lookup`,
`Snapshot`). Generalizes past this package: any clone-on-read isolation claim
over a nested slice needs ≥2 elements at the nested level in its fixture, or
the test can't tell a correct deep clone from a shallow one that happens to
pass because there's nothing to alias.

## Lessons

- **A fail-closed guard's "sole red" mutation result can lie when guards are
  ordered — a later guard can shadow an earlier one.** All seven reject
  branches here were run as `go test -overlay` mutants rather than reasoned
  about. Six were sole-red immediately; the decode-failure guard was not:
  swallowing the `json.Unmarshal` error still leaves a zero decode target,
  which the question-count bound also rejects, so the original
  malformed-JSON fixture was caught twice over and proved nothing about the
  decode guard specifically. The row that finally kills it exploits a fact
  worth carrying into any future JSON boundary parse: **`encoding/json`
  partially populates a struct on a type error** — a wrong-*typed* field
  (not malformed syntax) leaves every other field decoded and the bad one at
  its zero value, so a type-error input reaches later bounds checks as a
  *plausible* batch with one silently-empty field. Any ordered chain of
  fail-closed guards can have this shadowing shape; it's invisible from the
  row names, so it has to be measured, not predicted.
- **`*bool` vs. a shape-checker's `json.RawMessage` for an optional-but-
  required key is a real choice, not a style pick — decide it by what the
  caller does with a wrong-typed value.** Both keep an absent key
  distinguishable from a well-formed `false` for `multiSelect`. The pointer
  additionally folds a wrong-*typed* `multiSelect` into the existing
  decode-failure branch for free (an eighth reject branch avoided).
  `ask_user_question_shape_test.go`'s decode target uses `RawMessage`
  instead, deliberately: that helper must *survive* a bad value in order to
  *report* it as a finding, where this parse wants to reject it outright.
  Pick by which side of "report vs. reject" the code sits on, not by
  precedent.
- **A committed capture gated behind an `e2e_realclaude`-tagged package is
  still readable from the hermetic gate.** The build tag sits on the Go
  files in `internal/e2e/realclaude`, not on its `testdata/*.json`, so a
  relative-path read of the capture from an untagged package (here,
  `questionbridge_test.go` reading
  `../e2e/realclaude/testdata/ask_user_question_v2.1.239.json`) pins against
  the same bytes and runs under `make check`, where the capture's own family
  never compiles. Worth knowing before copying a capture's bytes inline to
  "escape" the tag — the escape is unnecessary.

## Related

- [Question-batch payload](protocol-package-question-batch-payload.md) — the
  wire shape this parse fills, and the header-cap / no-`TruncatedFields`
  reasoning this package's bounds implement.
- [permbridge-package.md](permbridge-package.md), [modalbridge-package.md](modalbridge-package.md) —
  the two sibling packages this one is named after and modeled on (self-contained,
  log-free registries/parsers at the same trust boundary).
- Specs: [`specs/architecture/1965-questionbridge-parse.md`](../../specs/architecture/1965-questionbridge-parse.md),
  [`specs/architecture/1975-questionbridge-batch-registry.md`](../../specs/architecture/1975-questionbridge-batch-registry.md).
- Open: nothing calls `Parse` or `Registry` yet, deliberately. #1973 is the
  surfacer: it wires the discriminant into `streamApprovalBridge.Surface`,
  calls `Registry.Record`, and broadcasts, plus the no-answer dismissal paths
  (#1974's `question_dismissed`,
  [question-batch payload](protocol-package-question-batch-payload.md)).
  #1907 is the answer path (`Registry.Resolve`, and must be the sole
  dismissal broadcaster for an answered batch). #1928 is the connect-time
  reconcile (`Registry.Snapshot`, mints/retires nothing).
