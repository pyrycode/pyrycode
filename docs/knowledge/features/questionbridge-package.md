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
for every approval, question or not; #1927 is the call site that consults
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
unfilled — both are daemon-asserted, and #1927 mints/fills them.

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
- Spec: [`specs/architecture/1965-questionbridge-parse.md`](../../specs/architecture/1965-questionbridge-parse.md).
- Open: the always-split consumer half — wiring the discriminant into
  `streamApprovalBridge.Surface`, minting `QuestionBatchID`, and the
  per-device answer gate / dismissed-batch fail-safe — is #1927.
