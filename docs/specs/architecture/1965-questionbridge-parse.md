# #1965 — recognize claude's question tool and parse its batch into the outbound payload

## Files read

- `internal/protocol/questions.go` → `QuestionShownPayload`, `Question`, `QuestionOption` — the shape this parse fills; its doc block states the bounds (1-4 questions, 2-4 options), the **documented 12 / observed 14** header cap, the "#1965 MUST NOT enforce 12 fail-closed" instruction, and the "name its unit" obligation.
- `internal/permbridge/permbridge.go` → `Request` (`ToolName`, `Input json.RawMessage`) — the parse's input pair; also the model for a self-contained, **log-free** package (its package doc argues both properties explicitly).
- `cmd/pyry/modal_resolve_v2.go` → `streamApprovalBridge.Surface` — today's hard-coded `tuidriver.ModalClassPermission` path. Unchanged by this slice; #1927 is the call site that will consult the parse.
- `internal/modalbridge/modal.go` → `maxPromptBytes`, `boundPrompt` — the **posture** to borrow (a control frame is never dropped by the push queue, so an un-droppable frame must not be inflatable) and the **mechanism** to reject (`boundPrompt` truncates; this family has no `truncated_fields` to report a cut in).
- `internal/e2e/realclaude/testdata/ask_user_question_v2.1.239.json` → the committed capture: record wrapper `{claude_version_raw, claude_version_slug, tool_name, tool_input}`, one question, two options, explicit `"multiSelect": false`, 14-rune header.
- `docs/protocol-mobile.md` § Question (v2) → the client-facing contract, its bounds table (all four rows "Enforced by: Nothing"), and the deliberate absence of any published string-length number. **Not edited by this slice** — the row flip is #1927's.
- `docs/knowledge/features/protocol-package-question-batch-payload.md` → #1963's lessons; the "no `TruncatedFields` ⇒ #1965 must reject, not truncate" consequence.
- `docs/knowledge/features/e2e-realclaude-ask-user-question-shape-test-go.md` → why `ask_user_question_shape_test.go` types multi-select `json.RawMessage`; this plan diverges (see Design) and says why.
- `docs/knowledge/features/permbridge-package.md`, `docs/knowledge/features/modalbridge-package.md` → the two sibling packages `internal/questionbridge` is named after.

## Context

claude's clarifying-question tool rides the same approval bridge as a permission prompt, and nothing tells them apart: `Surface` hard-codes the permission modal class and uses the tool name as the prompt body, so a question reaches a client as a modal titled "Permission required". This slice lands the discriminant and the fail-closed parse in a new package with **no consumer**; #1927 wires it. The envelope type (#1962), the payload (#1963) and the wire doc plus its encoding goldens (#1964) have all merged.

**Sizing measurement.** Five of the six size-S boundaries hold with margin: 1 production source file, 0 new exported types, 0 consumer call sites, 4 acceptance criteria, 7 reject branches. The sixth — total written work — lands at roughly 520 lines (~160 production, ~230 test, ~130 this plan), above the 400 boundary. The parent chain is #1965 → #1926 → #1906, so the split-depth gate forbids a third cut; and the only cut available here (parse-without-bounds, then bounds) fails the floor rule, since a bounds-only child has no consumer outside this family and a parse without its bounds is not shippable as fail-closed. Recorded on the ticket with `needs-human:sizing` and built as it stands.

**No ADR.** Every decision below is local to one new package and already argued in `internal/protocol/questions.go` or `docs/protocol-mobile.md` § Question (v2); nothing here re-decides a cross-cutting convention.

## Design

New package `internal/questionbridge`, beside `internal/modalbridge` and `internal/permbridge` and named after them. One production file, `questionbridge.go`.

**Dependencies: `internal/protocol` and the standard library only.** Not `internal/permbridge` — the parse takes the *pair* rather than a `Request`, so it never holds a `ToolUseID` it has no business reading, and the package stays as self-contained as `permbridge` documents itself to be. #1927 has the `Request` in hand and passes the two fields.

### Exported surface

```go
// The discriminant: claude's clarifying-question tool name.
const ToolName = "AskUserQuestion"

// Parse reports whether toolName is the question tool AND input is a
// well-formed, in-bounds batch, returning the payload's Questions in claude's
// own order. Every rejection yields the zero payload and false.
func Parse(toolName string, input json.RawMessage) (protocol.QuestionShownPayload, bool)
```

`ConversationID` and `QuestionBatchID` are left **unfilled** — both are daemon-asserted per `docs/protocol-mobile.md` § `question_shown`, and minting the nonce from `crypto/rand` is #1927's obligation. A parse that filled either would be asserting daemon state from claude's tool input.

The two negative outcomes — "not the question tool" and "the question tool, rejected" — are deliberately **not** distinguished in the return. A caller needing the distinction compares against `ToolName` itself, which is exported for exactly that. Minting a sentinel or an error type now would be API nothing consumes; #1927 adds one if it turns out to need it.

The bounds constants stay **unexported**: the wire doc deliberately publishes no number, a client learns the bound by being rejected, and a second exported copy of a limit is a second place it can be decided.

### Bounds

| Constant | Value | Unit | Why |
|---|---|---|---|
| `maxInputBytes` | 16384 | **bytes**, over the raw `input` before decode | The payload becomes an un-droppable control frame (`modalbridge`'s `maxPromptBytes` posture). A worst-case in-contract batch — 4 questions × (verbose text + 4 options × a prose description) — arithmetics to ~8 KB, so 16 KiB clears legitimate output by 2× while capping a pathological one. The committed capture's `tool_input` is well under 1 KB. |
| `minQuestions` / `maxQuestions` | 1 / 4 | questions | claude's contract; not contradicted by the capture (1 question). |
| `minOptions` / `maxOptions` | 2 / 4 | options | claude's contract; not contradicted by the capture (2 options). |

**The unit is stated in the constant's doc comment**, per `questions.go`'s instruction. This slice lands **no rune-count bound at all** and **no header-length bound at all**: the observed 14-rune header is pure ASCII, so nothing in the tree separates runes from bytes for this family, and the vendor's 12 is the one figure already falsified. The four claude-authored strings are bounded transitively by `maxInputBytes` and by nothing else.

### Decode target

An unexported mirror of claude's own keys — plain `json.Unmarshal`, **not** `DisallowUnknownFields`, so a claude release adding a field (the docs describe an optional per-option `preview` under `previewFormat`, which pyry never sets) does not turn every question into a dropped frame. Fail-closed means bounds, not unknown keys; `ask_user_question_reader_test.go` argues the same trade.

`multiSelect` is typed **`*bool`**, not the `json.RawMessage` that `askQuestionFixtureRecord`'s decode target uses. Both keep an absent key distinguishable from a well-formed `false`, and the divergence is deliberate: the shape helper must survive a wrong-typed value in order to *report* it as a finding, whereas here a non-bool `multiSelect` should reject anyway — `*bool` folds that case into the malformed-JSON branch for free instead of paying for a second unmarshal and an eighth branch. JSON `null` leaves the pointer nil and so rejects, which is the correct reading: null states no position.

### Order of operations — the size cap is first

1. `toolName != ToolName` → no batch (the discriminant; not a reject branch).
2. `len(input) > maxInputBytes` → reject. **Before decode**, so an oversized input is never parsed.
3. `json.Unmarshal` error → reject.
4. Question count outside `[minQuestions, maxQuestions]` → reject.
5. Per question, in order: option count outside `[minOptions, maxOptions]` → reject; `MultiSelect == nil` → reject.
6. Build `protocol.QuestionShownPayload{Questions: …}` preserving claude's array order at both nesting levels.

Nothing is built until every check passes, so no partial and no truncated payload is reachable — the property AC 2 asks for, and the reason the shape carries no `truncated_fields` to report a cut in.

## Concurrency model

None. `Parse` is a pure function over its arguments with no package-level mutable state, no goroutine and no lock, so it is safe to call concurrently from #1927's surfacer goroutine and from anywhere else. It holds no reference to `input` after returning: `json.Unmarshal` copies every string it decodes, and the returned payload shares no backing array with the caller's bytes.

## Error handling

Fail-closed by construction: every failure path returns `(protocol.QuestionShownPayload{}, false)` and the caller treats a false as "no batch". No error value, no sentinel, no wrapped cause — see the exported-surface note above.

**The parse emits no log line on any branch, at any level.** The package does not import `log/slog`. This is `permbridge`'s property and it is load-bearing for the same reason: the four claude-authored strings are untrusted subprocess-authored text, and a rejected input is exactly the input most tempting to log. Content-free decision logging, if #1927 wants any, belongs at that call site where the conversation id is in hand.

## Testing strategy

One table-driven test file, `questionbridge_test.go`, same package.

- **The capture pin (AC 3).** Reads `../e2e/realclaude/testdata/ask_user_question_v2.1.239.json` by relative path — not a copy of its bytes — decodes the record wrapper into a local struct, and calls `Parse(rec.ToolName, rec.ToolInput)`. The Go files in `internal/e2e/realclaude` are behind the `e2e_realclaude` tag; **the JSON file is not**, so this pin runs under `make check` where the capture's own family does not. Asserts every field: one question, its 14-rune header, its text, both options' labels and descriptions in order, and `MultiSelect == false`.
- **Positive arms the capture cannot reach**, hand-written as literals beside it: a two-question batch pinning batch **order** (assert `Questions[0]`/`Questions[1]` are not interchangeable), a `multiSelect: true` question, a 3-option question and a 4-option question.
- **Seven reject rows, one clause each**, each tripping its branch and no other: malformed JSON (a tiny literal, well under the cap, since the cap is taken first); an over-cap input that is otherwise entirely well-formed (a valid single question whose description is padded past `maxInputBytes`) so only the cap trips; zero questions; five questions; a one-option question; a five-option question; a question whose object omits `multiSelect`. Each asserts `ok == false` **and** a zero payload, so a partial result cannot pass.
- **Discriminant rows:** a different tool name over the capture's own well-formed input yields no batch; the empty tool name likewise.
- **The no-log pin (AC 4).** Installs `slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))` as the default logger — explicitly at `Debug`, because at the default level the assertion passes without ever admitting the lines it claims are clean — runs every branch above, restores the previous default, and asserts the buffer is empty. `slog.SetDefault` also redirects the standard `log` package, so both routes are covered by the one pin. This test mutates a global and therefore does not call `t.Parallel()`.

Everything settles offline from committed bytes: no `needs-real-claude`, no credentials, no network.

## Open questions

- **Is 16384 the right cap?** Resolved by arithmetic rather than by measurement — one capture is the whole population, and a defence sized off a single sample is not a measurement either. If #1927 or a later capture shows a legitimate batch near the cap, the constant moves and its doc comment records the new evidence.
- **Does #1927 need to tell "not a question" from "rejected"?** Deferred by design (see Design). Adding the distinction later is additive; removing an unused error type is not.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The boundary is explicit and singular: `Parse` is the one place claude's subprocess-authored tool input becomes a typed `protocol.QuestionShownPayload`, and it is a pure function with no second entry point. Downstream trust is signalled by the type plus `questions.go`'s SECURITY paragraph, which already binds all four strings as claude-authored and names the client as the render boundary owing sanitization. **The parse deliberately does not sanitize** — no control-character or terminal-escape stripping — which matches what `docs/protocol-mobile.md` § Question (v2) publishes to clients today; stripping here would silently falsify that published contract and would be a second place the rule is decided. One concrete risk named and accepted: `ConversationID`/`QuestionBatchID` stay zero-valued, so a #1927 that forgets to fill them would emit a frame routable to every conversation — mitigated in-slice by leaving them provably unfilled (a test asserts the zero value) rather than by inventing a placeholder that would look filled.
- **[Tokens, secrets, credentials]** Not applicable, by a design decision rather than by absence: `QuestionBatchID` **is** an unguessable nonce, and this slice deliberately does not mint it. Minting from `crypto/rand` is #1927's obligation, recorded in `questions.go` and § `question_shown`. Had the parse minted one, the nonce would be generated in a package with no `crypto/rand` import and no test for entropy.
- **[File operations]** No findings in production code — `Parse` performs no file I/O. The only path in the slice is the test's fixed relative path to the committed capture, a build-time constant with no caller-supplied segment, so no traversal, TOCTOU or symlink question arises.
- **[Subprocess / external command execution]** Not applicable — no `exec`, no environment read, no argument construction. The subprocess is *upstream*: this package consumes what claude already emitted.
- **[Cryptographic primitives]** Not applicable — no randomness, no comparison against a secret. The one string comparison, `toolName != ToolName`, is against a public constant, so constant-time comparison is not owed.
- **[Network & I/O]** The category's central requirement is met: `maxInputBytes` is an explicit pre-decode size cap over the raw bytes, stated with its unit, and taken **before** `json.Unmarshal` so an oversized input is never decoded. This is the concrete DoS answer for an un-droppable control frame. Nested-depth exhaustion is bounded transitively — the decode target is a fixed three-level struct, so a deeply nested input either fails to fit `maxInputBytes` or decodes into ignored fields.
- **[Error messages, logs, telemetry]** SHOULD FIX, and taken in this slice: the failure mode is a rejected input being logged for diagnosis, which would put untrusted claude-authored text into the daemon's logs. The design's answer is structural rather than disciplinary — the package does not import `log/slog` at all — and AC 4's pin is configured at `Debug` precisely because a pin at the default level would pass while observing nothing. No error value is returned either, so no reject reason can carry input bytes into a caller's log line.
- **[Concurrency]** No findings. Pure function, no package state, no lock, no goroutine; nothing to order and nothing to leak. The returned payload shares no backing array with the caller's input bytes, so a caller retaining the payload cannot observe a later mutation of those bytes.
- **[Threat model alignment]** § Security model's **threat 1** (prompt injection, `severity: high`, `mitigation: partial`) is the live one: claude's own words become text a remote client draws. This slice's share of the mitigation is bounding, which it does; sanitization is the client's, as published. **Out of scope and named:** the per-device answer gate and the unanswered/dismissed-batch fail-safe (#1927), and the inbound answer frame, whose option identity is a claude-authored `label` that must be re-resolved server-side against the recorded batch (#1927, per § `question_shown`).

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-01

## Revisions

**2026-09-01 — an eighth reject row, for the same seventh branch.** The seven guards were run as `go test -overlay` mutants rather than reasoned about, per the repo's standing lesson that a sole-redness claim written from the prediction column ships wrong. Six were sole-red on their own row. The **decode guard survived**: swallow the `json.Unmarshal` error and the zero `toolInput` still fails the question-count bound, so the syntactically-malformed fixture cannot show that guard is load-bearing. Added one row — a wrong-*typed* question text — which exploits the fact that `encoding/json` **partially populates on a type error**: without the guard, a structurally valid batch reaches the wire with a silently empty question text. It kills the mutant and it is the same reject branch, so the branch count stays at seven. Testing strategy is otherwise as planned.

**Open questions, resolved.** The 16 KiB cap stands as designed, with its arithmetic and its unit in the constant's doc comment and no new measurement to move it. The "not a question" / "rejected" distinction stays undistinguished, with `ToolName` exported so #1927 can draw it if it turns out to need one.
