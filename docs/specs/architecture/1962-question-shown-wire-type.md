# #1962 — Declare the question-batch wire type and its guard classification

**Size:** S (confirmed against PO's estimate; see § Sizing)
**Deliverable:** one exported constant, its doc block, three test registrations, one naming pin. Nothing constructs the frame; nothing emits it.

## Files to read first

Read these before writing anything. Every entry names a symbol, not a line — resolve with `codegraph_search` / `codegraph_node`.

| File | Symbol | What to extract |
|---|---|---|
| `internal/protocol/codes.go` | `TypeSlashCommandList` | **The form this whole ticket copies.** Its doc block is the paragraph structure AC #1 enumerates: grouping rationale → daemon's-name rule → discriminating-words → MUST-NOT-`inboundAppTypeSet` → no-request-verb → vocabulary-only forward reference. Copy the *structure*, write your own words. |
| `internal/protocol/codes.go` | `TypeAttachmentChunk` | The last block in the file. New blocks append after the current last one — that is the file's convention (every block since `TypeSessionError`). |
| `internal/protocol/interactive_test.go` | `TestSlashCommandListType_IsNotClaudesVocabulary` | The pin's form. Take the **names half only** — the four checks before the `json.Marshal`. The payload-bytes half after it is #1963's. |
| `internal/protocol/interactive_test.go` | `TestModelListType_IsNotClaudesVocabulary` | The four-check skeleton in its clearest form: equality against claude's word, `Contains` on its shortened root, `Contains` on its array key, exact pin last. |
| `internal/protocol/compat_test.go` | `v2OnlyTypes`, `TestTypeConstants_V1V2Partition`, `TestIsKnownAppType` | The **three** registration sites in this file — the allowlist entry, the `all` list entry, and the rejection row. All three are yours. |
| `internal/protocol/compat_test.go` | `TestInboundAppTypeSet_CoversAllExportedTypeConstants` | Read to confirm what you must **not** touch: its `all` list is v1 inbound only and its `23` literal stays `23`. Bumping it is how this ticket fails AC #4. |
| `cmd/pyry/relay_guard_test.go` | `excludedTypes` | The `"TypeSlashCommandList": "push"` entry and its comment are the exact form to mirror, with `#1927` as the pending producer. |
| `internal/protocol/envelope.go` | `inboundAppTypeSet` | The set the constant must **not** join, and the doc comment above it explaining why v2-only types stay out. |
| `internal/protocol/messaging.go` | `ModalShownPayload`, `ModalOption`, `ModalAnswerPayload` | The three facts the new-family argument rests on: `DefaultOptionID`'s documented MUST invariant, `ModalOption`'s flat `{id,label}`, `ModalAnswerPayload`'s single `OptionID`. |
| `internal/modalbridge/modal.go` | `denyByClass` | The fail-safe default-is-deny mapping. Name this symbol when the doc block carries the security argument. |
| `docs/protocol-mobile.md` | § Modal, the `default_option_id` row | The invariant in its normative form: "MUST equal one of `options[].id`", plus the #716 fail-safe convention. |
| `docs/knowledge/features/protocol-package.md` | § Constants, the **v2 slash-command-list vocabulary** entry; § Drift detectors | Two things: the declare-then-emit precedent, and **the trap-word misattribution warning** — see § Design, "One lesson the analogue carries that you must not copy". |
| `internal/e2e/realclaude/testdata/ask_user_question_v2.1.239.json` | — | claude's own key set, already committed. `tool_name` is `AskUserQuestion`; `tool_input` carries `questions`, each entry `question` / `header` / `multiSelect` / `options`, each option `label` / `description`. Read it rather than trusting this table. |

## Context

claude's clarifying-question tool rides the same approval bridge as a permission prompt: the call blocks on `pyry mcp-approve`, `handleApprove` parks it in `internal/permbridge` keyed by `tool_use_id`, and `streamApprovalBridge.Surface` raises it to clients. That surfacer hard-codes `tuidriver.ModalClassPermission` and uses the tool name as the prompt body, so a clarifying question today reaches a remote client as a modal titled "Permission required" whose body reads as `AskUserQuestion`.

Fixing that needs a wire vocabulary for the question batch. This slice supplies exactly the name, so pyrycode-desktop#849 can write a decode path against a stable string before anything emits the frame. The three slices that follow it consume this constant: #1963 declares the payload struct and its nested types, #1965 the parse that fills them, #1927 the producer that emits the frame.

**Why a new frame family and not a grown `ModalShownPayload`.** The deciding argument is security, and it is the argument the doc block has to carry. `denyByClass` in `internal/modalbridge/modal.go` makes `DefaultOptionID` the DENY option, so a careless confirm on a remote surface denies rather than allows, and `docs/protocol-mobile.md` § Modal states as a hard invariant that `default_option_id` MUST equal one of `options[].id`. That invariant is **total** on the permission surface today — every `modal_shown` frame satisfies it, and a client or a test can assert it unconditionally. A clarifying question has no deny option and no safe default, so a question riding `modal_shown` would demote a total invariant to a class-conditional one: every asserting site would have to learn a class exemption, and the exemption is precisely on the field whose whole purpose is fail-safe.

Two supporting reasons, both re-checked against the tree on 2026-09-01. `ModalAnswerPayload` carries exactly one `OptionID`, so a batch with per-question multi-select forks the inbound leg either way — the claimed reuse is false at the joint carrying the security contract. And `ModalOption` is flat `{id, label}` while the committed capture nests options under each question and gives each a `description`, so growing the modal shape would add a field that is always empty for `permission` and `trust` and would rewrite every committed `modal_shown` golden on both sides of the wire.

**No ADR is warranted.** The decision is one frame family in one package with the argument recorded in the constant's own doc block, which is where every sibling in this run put the same class of reasoning. If the documentation phase disagrees it owns that call; this spec does not create anything under `docs/knowledge/`.

**Overlap check.** `git fetch origin --prune` then a scan of every `origin/feature/<N>` branch against this ticket's four files found one hit: `origin/feature/449` touches `codes.go`. Issue #449 is CLOSED (2026-05-17) — a stale branch that will never merge, so no conflict is reachable and no `blockedBy` was set.

## Design

### The constant

One new `const` block appended at the end of `internal/protocol/codes.go`, after the `TypeAttachmentChunk` block:

```go
const (
	TypeQuestionShown = "question_shown" // binary → phone, outbound v2 clarifying-question batch
)
```

The single-type-own-block shape is the precedent set by `TypeResync`, `TypeSessionError`, `TypeThinkingProgress`, `TypeRateLimited`, `TypeModelList` and `TypeSlashCommandList`. Do not merge it into the modal block: sharing the approval bridge is not sharing a subject, and the whole point of § Context's argument is that this is a different family.

**The wire string is `question_shown` and this slice's job is to settle it.** Nothing downstream supplies the string — pyrycode-desktop#849 models the contract without naming it, and #1963/#1965/#1927 reference the constant without assuming a spelling. `question_shown` mirrors `modal_shown` because the frame is the same *kind* of thing (a prompt surfaced to a client, answered or dismissed on a terminal path) while being a different family, and — see § The naming pin — it is the spelling that leaves the discriminating checks satisfiable. `questions_shown` would not be: it makes the plural check red by construction.

### The doc block

The block above the constant carries these paragraphs, in this order. This is AC #1's enumeration; `TypeSlashCommandList`'s block is the model for each one's register and length.

1. **What the frame is, and the grouping rationale.** claude's clarifying-question batch as it reaches a client; its own block rather than a merge into any block above, with the single-type precedent named.
2. **Why a new family rather than a grown `modal_shown`.** § Context's total-invariant argument, naming `denyByClass` and `ModalShownPayload.DefaultOptionID` as symbols, plus the two supporting reasons (`ModalAnswerPayload`'s single `OptionID`; `ModalOption`'s flat shape versus the capture's nested, `description`-carrying options). This paragraph is the one thing this block has that `TypeSlashCommandList`'s does not.
3. **The NAME is the daemon's, not claude's** — the wire type names what the frame IS to a client, so a claude rename lands in one place instead of breaking every client at once.
4. **The discriminating words, and the subject-noun trap.** claude's words on this path are the tool name `AskUserQuestion` and the input keys `questions`, `question`, `header`, `options`, `multiSelect`. The singular `question` is a substring of `question_shown`, so it cannot be a check — it would be red against the correct name. The plural `questions` is not a substring (`question_shown` carries `question_`, not `questions`), so the plural and the tool name are the checkable words. Record that this is why the name is not `questions_shown`. Cross-reference `TestQuestionShownType_IsNotClaudesVocabulary`.
5. **MUST NOT be added to `inboundAppTypeSet` in `internal/protocol/envelope.go`.** An outbound binary → phone report an old phone never receives; a leak into that set would let a phone send a `question_shown` frame into `dispatch.Route`. Name both drift detectors and state that both are mandatory from the moment the constant exists, not from the moment something emits it.
6. **Why no inbound request verb.** `TestEveryInboundV2TypeHasHandler`'s Assertion #1 requires an inbound type to be wired into `cmd/pyry/relay.go`'s `Handlers` map or `internal/relay/v2session.go`'s `dispatchAppFrame` switch. This slice ships no handler, so a verb declared here would be red by construction, and filing it under `excludedTypes` to dodge that would be a lie to the guard. **Also state that no question-dismissal type is declared here** — whether dismissal is its own type or reuses `modal_dismissed` is #1927's design call, and declaring a type before its semantics are settled is worse than declaring it late.
7. **Vocabulary only.** Forward-reference #1963 (payload struct and nested types), #1965 (the parse that fills them) and #1927 (producer/emit), and name the declare-then-emit precedent (#1405→#1410, #1616→#1638, #1704→#1848, #1726).

**One lesson the analogue carries that you must not copy.** `TypeSlashCommandList`'s shipped block and its pin both say the trap word for `TypeModelList` is `models`. That is wrong and `docs/knowledge/features/protocol-package.md` records it as an uncorrected SHOULD FIX from code review on #1735: `models` is a *live check* in `TestModelListType_IsNotClaudesVocabulary`; the trap word is always the **singular** subject noun (`model` there, `command`/`slash_command`/`slash` for the slash block, `question` here). When paragraph 4 cross-references the siblings' trap, attribute it to the singular. **Do not fix the sibling's wording** — that is out of scope for this ticket; just do not propagate it.

**Citations.** `make check` runs `cite-guard`, which fails any `file.go:NNN` in a Go comment at any depth, with no range and no depth exemption. Name symbols throughout, including in the test comments.

### The naming pin

`TestQuestionShownType_IsNotClaudesVocabulary`, appended at the end of `internal/protocol/interactive_test.go` beside its four sibling pins. **Names-only in this slice** — no `json.Marshal`, no payload-bytes half. Every sibling pin ends with one, but `TestSlashCommandListType_IsNotClaudesVocabulary`'s own comment records that its half "arrived with the shape (#1727)"; here the shape is #1963's, and a payload half written now would not compile. `header`, `options` and `multiSelect` are per-entry keys and belong to that half.

Four assertions, in the siblings' order — three negatives, then the exact pin:

- **Equality against claude's tool name.** `TypeQuestionShown == "AskUserQuestion"` fails. This is the named statement of the one wrong name a reader reaches for first, in the same register as the siblings' `initialize` equality check.
- **`strings.Contains(TypeQuestionShown, "ask")` fails.** This is the discriminating check on the tool-name axis, and it is **not** redundant with the equality above: `strings.Contains` is case-sensitive, so the equality check is green against `ask_user_question_shown` — the most plausible wrong name — while this one is red. `ask` is the root every snake-cased derivation carries (`ask_user_question`, `askuserquestion`, `ask_user_question_shown`). Record the one subtlety in the comment: `ask` is a substring of `task`, so this check is safe only because the correct name carries no task word, and a future rename must re-check that.
- **`strings.Contains(TypeQuestionShown, "questions")` fails** — claude's input array key. This is the check `questions_shown` would have made red by construction, which is why the name is not that.
- **`TypeQuestionShown != "question_shown"` fails** — the exact pin. The negatives alone leave every other wrong name green, and naming is this slice's whole deliverable, so the exact pin is the load-bearing half.

The singular `question` is **not** a check and the comment must say why: it is a substring of the correct name, so it would be red by construction — the trap #1726 records three words wide for `command` / `slash_command` / `slash`.

Error strings follow the siblings verbatim in shape: `t.Errorf("wire type %q is derived from claude's ... (contains %q)", TypeQuestionShown, "<word>")` and `t.Errorf("wire type: got %q, want %q", ...)`.

### Registrations — four sites, all in this commit

Both drift detectors are mandatory from the moment the constant exists. `docs/knowledge/features/protocol-package-drift-detectors.md` records #1393, #1386, #1405, #1616, #1704, #1726 and #1752 each registering in both in the same commit as the constant, and #1074 discovering the second detector at build time instead.

1. **`internal/protocol/compat_test.go` → `v2OnlyTypes`** — `TypeQuestionShown: true`, after the attachment entry, with a `// v2 clarifying-question batch.` comment in the file's form.
2. **`internal/protocol/compat_test.go` → `TestTypeConstants_V1V2Partition`'s `all` list** — the same entry and comment. This is the list the union assertion counts against; missing it here while adding to `v2OnlyTypes` makes `len(inboundAppTypeSet)+len(v2OnlyTypes) == len(all)` fail.
3. **`internal/protocol/compat_test.go` → `TestIsKnownAppType`'s table** — a `{"question_shown-rejected", TypeQuestionShown, false, ErrUnknownType}` row beside its siblings. This row is the deterministic proof of AC #4: it fails the moment the constant reaches `inboundAppTypeSet`.
4. **`cmd/pyry/relay_guard_test.go` → `excludedTypes`** — `"TypeQuestionShown": "push"`, after the attachment entry, with a comment naming **#1927** as the pending producer and giving the push reason in `"TypeSlashCommandList"`'s form.

`"push"` is the right value and not a borrowed one: it classifies direction, and this frame is outbound-only. All seven declared-before-producer siblings use it. The tree's one custom reason, `TypeAttachmentChunk`'s `"pending handler (#1744)"`, is custom because that frame is bidirectional so `"push"` would have been false — which is not the case here.

**No count literal moves.** `TestTypeConstants_V1V2Partition`'s size assertion is fully derived from the three collections, so registering in both places keeps it balanced with nothing to edit. The hard-coded `23` belongs to `TestInboundAppTypeSet_CoversAllExportedTypeConstants`, whose `all` list is v1 inbound types only; a v2-only type never joins it. **Bumping that `23` is how this ticket fails AC #4** — if you find yourself editing it, you have added the constant to `inboundAppTypeSet`.

### Concurrency model

None. This slice declares a string constant and three test registrations. No goroutines, no channels, no shared state, no shutdown sequence. Stated explicitly rather than omitted, because the absence is the design: a vocabulary-only slice has no runtime surface at all.

### Error handling

No production error paths — the deliverable is a constant. The failure modes that matter are build-gate failures, and each has a named detector:

| Mistake | What goes red |
|---|---|
| Constant added, `v2OnlyTypes` not amended | `TestTypeConstants_V1V2Partition` — "missing from both" |
| `v2OnlyTypes` amended, `all` list not | `TestTypeConstants_V1V2Partition` — union size mismatch |
| Constant added to `inboundAppTypeSet` | `TestTypeConstants_V1V2Partition` ("in BOTH"), `TestIsKnownAppType` (the rejection row), `TestInboundAppTypeSet_CoversAllExportedTypeConstants` (size) |
| `excludedTypes` not amended | `TestEveryInboundV2TypeHasHandler` Assertion #3 — unclassified constant |
| A `file.go:NNN` citation in any new comment | `make cite-guard`, inside `make check` |

## Testing strategy

Scenarios, not test code. The developer writes them in the package's idiom.

- **Naming pin** — `TestQuestionShownType_IsNotClaudesVocabulary` as specified in § The naming pin: four assertions, names-only. Verify by inspection that swapping the constant's value to `questions_shown` reddens the plural check, to `ask_user_question_shown` reddens the `ask` check, to `AskUserQuestion` reddens the equality check, and to any other string reddens the exact pin. Each wrong name is caught by at least one check, and the exact pin is the backstop for all of them.
- **Partition** — `TestTypeConstants_V1V2Partition` passes with the constant classified in `v2OnlyTypes` and listed in `all`, and no literal edited.
- **v1 rejection** — the new `TestIsKnownAppType` row asserts `IsKnownAppType` returns `ErrUnknownType` for a `question_shown` envelope, which is AC #4's deterministic half.
- **v1 set size** — `TestInboundAppTypeSet_CoversAllExportedTypeConstants` still passes with `23` unchanged.
- **Relay guard** — `TestEveryInboundV2TypeHasHandler` passes with the `excludedTypes` entry.
- **Gate** — `make check` green. Nothing here needs claude, credentials, or a network; the ticket is correctly not `needs-real-claude`.

No new fixtures, no encoding goldens, no `docs/protocol-mobile.md` section — those arrive with the shape (#1963) and the producer (#1927), matching how #1705 and #1718 followed their own declaring tickets.

## Sizing

| Boundary | Limit | This spec |
|---|---|---|
| Production source files created/modified | ≤ 3 | **1** (`internal/protocol/codes.go`) |
| Total written work | ≤ 400 | **~160** (codes.go ~85, interactive_test.go ~45, compat_test.go ~14, relay_guard_test.go ~11) |
| New exported types or interfaces | ≤ 5 | **0 types, 1 constant** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — additive constant, no existing caller |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches | ≤ 10 | **0** |

Re-derived from the analogue rather than from PO's estimate alone: #1726's implementation commit is 153 insertions / 0 deletions across 4 files (`codes.go` 78, `interactive_test.go` 55, `relay_guard_test.go` 11, `compat_test.go` 9). This slice's `codes.go` block runs longer (the new-family argument is an extra paragraph) and its pin runs shorter (names-only, no payload half), so the total lands within a few lines of the analogue. No boundary is approached.

## Open questions

- **Where #1963's payload struct lives** (`interactive.go` beside the slash-command and model-list payloads, or a new file) is that ticket's call. It does not affect this slice: the pin's home is `interactive_test.go` because that is where all four sibling pins live, and #1963 extends this same test with its payload-bytes half rather than writing a second one.
- **The dismissal frame** is deliberately undeclared here, per § The doc block paragraph 6. #1927 decides whether it is its own type or a reuse of `modal_dismissed`.
- **Whether #1927 ships this as a push or a request/reply** is open. If it picks request/reply it declares the verb together with its handler and moves this constant's `excludedTypes` entry from `"push"` to `"reply"`; a client's decode path is the same frame either way, which is what declaring the type now exists to freeze.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings, and the reason is structural rather than incidental. This slice adds no boundary and moves no data: it declares a string constant. The one boundary the constant *could* have reached is `inboundAppTypeSet` in `internal/protocol/envelope.go`, which `IsKnownAppType` consults to decide whether a decrypted phone→binary frame may enter `dispatch.Route`. The design keeps it out, and AC #4 is enforced deterministically by three independent assertions, not by the doc comment: `TestIsKnownAppType`'s `question_shown-rejected` row, `TestTypeConstants_V1V2Partition`'s "in BOTH" branch, and `TestInboundAppTypeSet_CoversAllExportedTypeConstants`'s size check. The advisory MUST-NOT paragraph in the doc block is the stochastic rail; those three tests are the deterministic fabric behind it.
- **[Trust boundaries — the invariant this ticket exists to protect]** No findings, and this is the finding worth stating positively. Growing `ModalShownPayload` instead would have converted `default_option_id`'s **total** invariant into a class-conditional one on the surface where `denyByClass` makes the default the DENY option. Every site asserting that invariant — the producer, the fixtures, and both client implementations — would have needed a class exemption on exactly the field whose purpose is fail-safe against a careless confirm on a remote surface. A new family leaves the permission surface's invariant total and untouched. This is why the doc block is required to carry the argument by symbol name.
- **[Tokens, secrets, credentials]** Not applicable, by design: this slice mints nothing, stores nothing, and compares nothing. Worth naming for the family, though, because the deferral is real — the question frame will need a correlation key when #1927 produces it, and `ModalShownPayload.ModalID`'s contract (a one-time, opaque, unguessable nonce that the daemon resolves server-side, never trusting a phone-asserted conversation) is the model that ticket must follow. **OUT OF SCOPE → #1927.** Nothing in this slice constrains that choice.
- **[File operations]** Not applicable. No path is constructed, opened, or written. The one file this ticket reads, `internal/e2e/realclaude/testdata/ask_user_question_v2.1.239.json`, is read by a human at design time and is already committed.
- **[Subprocess / external command execution]** Not applicable. No `exec.Command`, no environment handling. The approval bridge that will eventually carry this frame (`handleApprove` → `internal/permbridge`) already exists and is unmodified by this slice.
- **[Cryptographic primitives]** Not applicable. No randomness, no comparison of attacker-controlled values against secrets. The frame will ride the existing Noise_IK v2 session transport unchanged; this slice adds no key, nonce, or comparison.
- **[Network & I/O]** No findings. The constant introduces no read path, so no size cap, timeout, or deadline is in play. The relevant property is that it introduces no *inbound* read path either — see Trust boundaries. Note for the family: whatever payload #1963 declares will be decoded from attacker-reachable bytes on the client side and must get its own size discipline there; **OUT OF SCOPE → #1963.**
- **[Error messages, logs, telemetry]** No findings. The only strings this slice adds are test failure messages, which print the constant's own value — a compile-time literal, not user data. Specifically checked: no assertion prints an envelope, a payload, or anything derived from the committed capture, so there is no path by which a captured question body could reach a log.
- **[Concurrency]** Not applicable, explicitly rather than by omission: no goroutine, no lock, no shared mutable state, no shutdown sequence. `v2OnlyTypes` is a package-level map in a test file, read-only after initialisation and never written at test time, so `-race` has nothing to find. The constant itself is immutable.
- **[Threat model alignment]** No findings. `docs/protocol-mobile.md` § Security model's relevant threat here is a hostile phone sending a frame the daemon will dispatch; this slice's whole guard posture (§ Trust boundaries) addresses it. The mirror threat — a hostile relay injecting a `question_shown` frame toward a phone — is not widened by declaring the type: app frames ride the Noise_IK session's AEAD, so a relay that cannot produce a valid tag cannot mint a frame of *any* declared type, and adding one more name to the vocabulary changes nothing about that. What a client does with a well-formed frame's *contents* is a decode-side concern that arrives with the shape (**OUT OF SCOPE → #1963**). The threats the *question feature* raises — a remote answer being forged, a stale question being answered after resolution, per-device gating of who may answer — are all inbound-leg threats that arrive with #1927's producer and its answer path. **OUT OF SCOPE → #1927**, which must resolve them against `ModalID`/#702/#706's existing model rather than inventing a parallel one.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-09-01
