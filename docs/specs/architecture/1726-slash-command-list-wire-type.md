# #1726 — declare the slash-command-list wire type and its guard classification

**Size:** xs (verified against the six boundaries — see § Sizing)
**Scope:** wire vocabulary only. One constant, four guard-list edits, one naming pin. No payload struct, no producer, no emit, no handler, no relay wiring, no fixtures, no `docs/protocol-mobile.md`.

## Files to read first

Read these before writing anything. Every entry names the symbol to read and what to take from it.

| File | Symbol | What to extract |
|---|---|---|
| `internal/protocol/codes.go` | the `TypeModelList` const block | **The doc form to follow, paragraph for paragraph.** It is the same frame class one field-set away and already carries every paragraph this one needs. Read all seven paragraphs before writing any of yours. |
| `internal/protocol/codes.go` | the `TypeModelAnnounced` const block | The earlier instance of the subject-noun trap, in its own words. `TypeModelList`'s block cites it; read the source rather than the citation. |
| `internal/protocol/interactive_test.go` | `TestModelListType_IsNotClaudesVocabulary` | The naming-pin shape: negative equality, negative `strings.Contains`, positive exact-equality. **Read its code, not only its doc comment** — the comment describes two claude words and the code checks them in a specific order, and this ticket's word set is twice the size. |
| `internal/protocol/interactive_test.go` | `TestModelAnnouncedType_IsNotClaudesSubtype` | The same pin one generation earlier, and the sibling whose payload-bytes half this slice does not carry. Its excluded-key list already names `slash_commands`, which is this ticket's third claude word — read why it is excluded there. |
| `internal/protocol/compat_test.go` | `TestIsKnownAppType` | The `<type>-rejected` row shape and the two-sentence comment the recent siblings carry. AC 2's one edit that no gate forces. |
| `internal/protocol/compat_test.go` | `v2OnlyTypes` | The second list, and its one-line-comment-per-group style (each group is its own gofmt alignment island). |
| `internal/protocol/compat_test.go` | `TestTypeConstants_V1V2Partition` | The third list (`all`), plus the two branches that make omissions loud: "missing from both", and the union-size equality against `len(all)`. |
| `internal/protocol/compat_test.go` | `TestInboundAppTypeSet_CoversAllExportedTypeConstants` | Read it to confirm you must **not** touch it. Its `all` is the v1-only set, pinned by a literal `23`, and it also asserts `len(inboundAppTypeSet) == len(all)`. Adding a v2 constant there turns it red. |
| `internal/protocol/envelope.go` | `inboundAppTypeSet` | What the set actually is (the v1 application-type set), so the MUST-NOT paragraph is written from the source rather than from the name. |
| `cmd/pyry/relay_guard_test.go` | `excludedTypes` | Where the `"push"` entry goes and the comment style the last three siblings established. |
| `cmd/pyry/relay_guard_test.go` | `TestEveryInboundV2TypeHasHandler` | Assertion #1 (an inbound type needs a real dispatch surface) and Assertion #3 (totality — an unclassified constant is red on its own). Together these are the whole "why no request verb" argument. |
| `cmd/pyry/relay_guard_test.go` | `codesPath`, `appTypeConstNames` | Why `codes.go` is the guard's home convention rather than an incidental location: the guard AST-parses that one file. |
| `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` | — (data file; read with a JSON tool, not codegraph) | The authority for the four-word table. Confirm for yourself: `commands` under the control response, `slash_commands` and `terminal_slash_commands` on the `system`/`init` stdout line. |
| `docs/knowledge/features/protocol-package.md` | § "Model-list payload (#1704 shape, …)" | The recorded lesson on this exact pin: the negative checks alone leave every wrong name green, which is why the exact-equality half exists. **Do not source counts from this file** — its § "Envelope types" prose still says 16 and 19 v1 types where the pinned literal is 23. |

## Context

The desktop's Actions menu offers reset, compact and knowledge capture as slash commands in an ordinary message. Two of the three are built in; knowledge capture is workspace-specific, so a menu that always offers it is wrong in most repositories and sending it produces an "Unknown command" reply in the thread. claude already knows the answer and the daemon already runs the child that has it: a `control_request` with subtype `initialize` on the child's held-open stdin returns a `commands` array alongside the `models` array — one round trip, two payloads, no new credential and no new trust boundary.

Two desktop consumers wait on the frame: [pyrycode-desktop#681](https://github.com/pyrycode/pyrycode-desktop/issues/681) (Actions-menu grey-out) and [pyrycode-desktop#694](https://github.com/pyrycode/pyrycode-desktop/issues/694) (slash-command type-ahead). This slice freezes the type name and its guard classification so their decode paths can be written before the payload (#1727) and the producer (#1720) land — the declare-then-emit sequencing this repo has used for #1405→#1410, #1616→#1638 and #1704→#1693.

**No ADR is warranted.** Every decision here applies an existing precedent: the const-block form is `TypeModelList`'s, the guard classification is the four-list convention three siblings already follow, and the naming pin is `TestModelListType_IsNotClaudesVocabulary`'s shape with a longer word list. The one genuinely new thing — a claude vocabulary whose plurals subsume each other while its singulars are unusable as checks — is recorded in the doc block and the test comment where a reader of either will find it, not in a separate record.

## Design

### 1. The wire constant — `internal/protocol/codes.go`

Append a new const block immediately after the `TypeModelList` block, continuing the run of claude-sourced report types (`rate_limited` → `model_announced` → `model_list`):

```go
const (
	TypeSlashCommandList = "slash_command_list" // binary → phone, outbound v2 slash-command-list report
)
```

That is the entire production change. The rest of this section is the doc block above it, which AC 1 requires to follow `TypeModelList`'s form paragraph for paragraph:

- **What the frame is, and why now.** The set of slash commands the running child accepts for this conversation, sourced from the `initialize` control reply. The consumers and the defect are the § Context paragraph above, compressed: an Actions menu that offers a workspace-specific command everywhere offers it wrongly almost everywhere.
- **Why it is grouped alone.** Not a turn sub-state, not turn-independent work, not a periodic reading, not a condition report, not an identity report. It is a **capability inventory of verbs** — what the operator may ASK the session to do — where `model_list` is a capability inventory of identities and `model_announced` reports the one identity in force. Say explicitly why it is not merged into the `TypeModelList` block despite sharing the `initialize` round trip: sharing a source is not sharing a subject, the two blocks' naming paragraphs have to say different things (two claude words there, four here), and every block in this run groups alone.
- **The NAME is the daemon's, not claude's.** The wire type names what the frame IS to a client, so a claude rename lands in one place instead of breaking every client at once — the reason the blocks above give.
- **The discriminating-words paragraph — four of claude's words, not two.** This is the paragraph that differs most from the analogue; § 2 below is its content.
- **MUST NOT be added to `inboundAppTypeSet`.** Follow the sibling's reasoning: an outbound binary → phone report an old phone never receives, and a leak into that set would let a phone send a `slash_command_list` frame into `dispatch.Route`. Name both drift detectors and say they are mandatory from the moment the constant exists rather than from the moment something emits it — the partition in `internal/protocol/compat_test.go` (this lives in `v2OnlyTypes`) and `cmd/pyry/relay_guard_test.go`'s `excludedTypes` (this is a push).
- **Why no inbound request verb is declared here, and that it is not an omission.** `TestEveryInboundV2TypeHasHandler`'s Assertion #1 requires an inbound type to be wired into `cmd/pyry/relay.go`'s `Handlers` map or `internal/relay/v2session.go`'s `dispatchAppFrame` switch; this ticket ships no handler, so a verb declared here would be red by construction, and filing it under `excludedTypes` to dodge that would be a lie to the guard. If #1720 picks request/reply it declares the verb together with its handler and moves this entry from `push` to `reply`; a client's decode path is the same frame either way, which is what declaring the type now exists to freeze.
- **The closing vocabulary-only paragraph.** #1726 is wire vocabulary only: #1727 declares the payload and its entry type, #1720 produces and emits the frame, #1718 adds the encoding fixtures and the `docs/protocol-mobile.md` § `slash_command_list` section. Same declare-then-emit sequencing as #1405→#1410, #1616→#1638 and #1704→#1693. The forward references are to work that does not exist yet, which is the established form — `TypeModelList`'s block forward-references #1693 and #1705 today — and no false claim ships in the gap because each sentence is about who owns the work, not about what the tree contains.

### 2. The four claude words, and which of them can be a check

The authority is the committed capture `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` (claude 2.1.239, landed by #1688). Re-measured for this spec:

| claude's word | what it is | in the capture |
|---|---|---|
| `initialize` | the `control_request` subtype | the request the daemon writes on the child's stdin |
| `commands` | the array key in the control reply | 51 entries, each an object with `name`, `description`, `argumentHint`, and `aliases` on 9 of them |
| `slash_commands` | a key on the `system`/`init` stdout line | the same 51 names, in the same order, as bare strings |
| `terminal_slash_commands` | a different array on that same init line | 2 entries: `doctor`, `color` |

The names-only twin is why the source is measured rather than assumed. It carries **none** of the 11 alias strings the control reply publishes across those 9 entries — so a daemon forwarding it would ship the grey-out consumer a list in which `reset` does not appear, and `reset` is the desktop Actions menu's own entry (an alias of `clear`, not a command name). Verified against the capture: `reset` is absent from `slash_commands` and absent from the 51 `name` values, present only under `clear`'s `aliases`. The control reply is the source not because it is the only candidate but because it is the only one carrying what the consumers need.

**The containment lattice decides the checks, and it runs the opposite way from intuition.** Against the correct name `slash_command_list`:

| candidate check | truth against the correct name | verdict |
|---|---|---|
| `== "initialize"` | false | usable |
| `Contains("init")` | false | usable, and subsumes the equality check above |
| `Contains("commands")` | false | usable, and subsumes both plurals below |
| `Contains("slash_commands")` | false | usable but redundant — every name containing it also contains `commands` |
| `Contains("terminal_slash_commands")` | false | usable but redundant, same reason |
| `Contains("command")` | **TRUE** | **red against the correct name** |
| `Contains("slash_command")` | **TRUE** | **red against the correct name** |
| `Contains("slash")` | **TRUE** | **red against the correct name** |

Two facts fall out, and the doc block must state both:

1. **The discriminating checks are the plurals.** claude's keys differ from this frame's subject noun by a trailing `s`, and the subject noun is unusable: `command`, `slash_command` and `slash` are each a substring of the correct name, so a `strings.Contains` on any of the three would be RED against it. This is the trap `TypeModelAnnounced`'s and `TypeModelList`'s blocks each record for their own names, and it cuts harder here — there the trap covered one word, here it covers three, and it is why `slash_command_list` was chosen: it contains none of the four claude words and no `init`, which is what makes the negative pins satisfiable at all.
2. **`commands` is the shortest of the three plurals, so checking it covers the other two.** `commands` is a substring of `slash_commands`, which is a substring of `terminal_slash_commands`; a name derived from either longer key necessarily contains the shorter one. A separate check per plural is documentation rather than added coverage — no such check can be sole-red for anything. State the subsumption in the comment and write one check.

### 3. Guard classification

Four edits, plus one file to leave alone. The new constant is a **push**.

1. `internal/protocol/compat_test.go`, `TestIsKnownAppType`'s cases — add `{"slash_command_list-rejected", TypeSlashCommandList, false, ErrUnknownType}` after the `model_list-rejected` row, with the siblings' two-sentence comment: an outbound binary → phone report an old phone never receives, and rejection is also what keeps the type off the inbound path.
2. `internal/protocol/compat_test.go`, the `v2OnlyTypes` literal — `TypeSlashCommandList: true,` under its own `// v2 slash-command-list report.` comment line.
3. `internal/protocol/compat_test.go`, `TestTypeConstants_V1V2Partition`'s `all` — add `TypeSlashCommandList` under the same comment. No numeric edit: that test computes `len(all)` rather than hardcoding it.
4. `cmd/pyry/relay_guard_test.go`, `excludedTypes` — `"TypeSlashCommandList": "push",` after the `TypeModelList` entry, under its own comment following the shape the last three siblings established (outbound-only, mandatory from the moment the constant exists rather than from the moment something emits it, and a push rather than a reply because this slice declares no inbound verb).
5. **`TestInboundAppTypeSet_CoversAllExportedTypeConstants` — do not touch.** Its `all` is the v1 application set, pinned by a literal `23`, and it also asserts `len(inboundAppTypeSet) == len(all)`; adding a v2 constant there turns it red twice. Inbound-ness is not what selects that set — `TypeRequestSnapshot`, `TypeRequestSessionSettings` and `TypeRequestDebugBundle` are all inbound and all live in `v2OnlyTypes`.

**Three of the four are structurally forced; one is not.** Omitting #2 reds `TestTypeConstants_V1V2Partition`'s "missing from both" branch; adding #2 without #3 reds that same test's union-size equality (`len(inboundAppTypeSet) + len(v2OnlyTypes)` must equal `len(all)`); omitting #4 reds Assertion #3. **#1 — the `TestIsKnownAppType` row — is the one nothing forces**: it asserts a property rather than being counted, so a missing row is silently green. Write it first, before the three the gate would have caught for you.

Each of #2 and #4 goes under a fresh comment line, which starts a new gofmt alignment island — so neither edit realigns the entries above it, and the diff stays at one added line plus its comment.

**Keep the constant in `codes.go`.** `cmd/pyry/relay_guard_test.go` AST-parses that one file (`codesPath`) for the constants it classifies, and Assertion #3's inverse branch reds a classified name that matches no constant there. A constant declared in a neighbouring file fails AC 2 loudly rather than quietly — but it fails, and the convention is what keeps it inside the totality tie at all.

## Concurrency model

None. `internal/protocol` is a pure-data package: no goroutines, no locks, no shared mutable state, nothing initialised at package init. This ticket adds one untyped string constant, which is immutable by construction.

## Error handling

No failure modes are introduced. There is no constructor, no `Validate()`, no decode path and no error value in this slice. Rejecting a frame a phone should not have sent is `IsKnownAppType`'s and the v2 session manager's job, and both already do it for every type they do not know — which is precisely what AC 2's first edit pins for this one.

## Testing strategy

One new test in `internal/protocol/interactive_test.go`, appended at the end of the file. It stands alone rather than joining the `model_list` group: this slice ships no payload, so there is nothing to group it with, and appending avoids splitting an existing group. `strings` is already imported for the sibling pins; this test needs no new import (`bytes` and `encoding/json` are for the payload-bytes half this slice does not carry).

**`TestSlashCommandListType_IsNotClaudesVocabulary`** — `TestModelListType_IsNotClaudesVocabulary`'s shape with this frame's word list. Four checks, each a scenario:

- `TypeSlashCommandList != "initialize"` — the constant must not be claude's `control_request` subtype verbatim. Error text names it as claude's subtype, as the siblings do.
- `!strings.Contains(TypeSlashCommandList, "init")` — must not be derived from that subtype's stem. Live and discriminating against the correct name.
- `!strings.Contains(TypeSlashCommandList, "commands")` — must not be derived from claude's array key. Live and discriminating: `slash_command_list` contains `command` but not `commands`.
- `TypeSlashCommandList == "slash_command_list"` — the positive exact pin. **This is the half that fails a wrong name** rather than merely a claude-derived one; the negative checks alone leave every non-claude-derived wrong name green. Naming is this ticket's whole deliverable and nothing downstream supplies the string if this slice gets it wrong, so the pin is load-bearing rather than decorative.

The doc comment carries what the checks cannot, and this is where the § 2 analysis lands in test form:

- **Why the singular subject nouns are absent.** `command`, `slash_command` and `slash` are each a substring of the correct name, so a `strings.Contains` on any of them would be RED against it — the trap `TestModelAnnouncedType_IsNotClaudesSubtype` records for `model` and `TestModelListType_IsNotClaudesVocabulary` for `models`, three words wide here instead of one.
- **Why there is one plural check and not three.** `commands` is a substring of `slash_commands`, which is a substring of `terminal_slash_commands`, so the shortest check subsumes both longer ones; neither longer check could be sole-red for anything, and adding them would be documentation rather than coverage. Name all four claude words in the comment even though only two of them appear in code — the comment is where the coverage argument lives.
- **Why the `initialize` equality check is kept even though `Contains("init")` subsumes it.** Any name equal to `initialize` also contains `init`, so the equality check is not sole-red for anything either. It is kept because all three sibling pins carry it and because it is the named statement of the one wrong name a reader would most plausibly reach for; say so rather than leaving a reviewer to rediscover the redundancy and read it as an oversight.
- **Why there is no payload-bytes half.** Every sibling pin ends with a regression check over its payload's bytes; this frame has no payload until #1727, so that half arrives there rather than being faked with an inline struct here.

**Gate:** `make check` covers all of it — `internal/protocol` and `cmd/pyry` are both hermetic unit-test packages. No live-claude suite, no relay suite, no fixture. `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` is read by a human (and by this spec) as evidence; no test this ticket writes opens it.

## Sizing

Re-counted against this written spec, not the sketch:

| Limit | Boundary | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **1** — `internal/protocol/codes.go` |
| Total written work | ≤ 400 | **~115** — `codes.go` ~58, `interactive_test.go` ~35, `compat_test.go` ~9, `relay_guard_test.go` ~11 |
| New exported types or interfaces | ≤ 5 | **0 types** — one constant, which is not a type |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — purely additive; nothing reads the constant yet |
| Acceptance criteria | ≤ 5 | **3** |
| Distinct error/reject branches | ≤ 10 | **0** — no validation, no state machine |

Calibrated against the same-shape half of the nearest analogue rather than by eye. #1704's implementation commit `e527c4f` is 442 insertions and no deletions across five files (`git show --stat` summary: `5 files changed, 442 insertions(+)`), of which the three files this slice shares are **`codes.go` 52, `compat_test.go` 9, `relay_guard_test.go` 11** — re-run for this spec, not inherited. The fourth shared file, `interactive_test.go`, took 176 lines there, of which this slice carries only the naming pin: 30 lines as it sits inside a longer function, ~35 standalone with the doc comment this one needs. The two files it does not share at all are `interactive.go` (194 lines — the payload, which is #1727's) and the rest of `interactive_test.go` (the nil-encoding and round-trip tests, also #1727's). `codes.go` runs a few lines longer than the analogue's 52 because four of claude's words have to be cleared where the analogue cleared two.

### Out of scope — do not touch

- `internal/protocol/interactive.go` — no payload struct, no entry type. #1727's.
- `internal/protocol/testdata/` — no fixture. #1718's. Verified: no test enumerates that directory, so an absent fixture breaks nothing.
- `docs/protocol-mobile.md` — #1718's. Nothing in the tree gates on a section existing for a new type.
- `internal/streamsup`, `internal/turnbridge`, `cmd/pyry/relay.go`, `internal/relay/v2session.go` — the decode (#1719), the producer and the emit path (#1720). No handler, no dispatch case, no `Handlers` entry.
- `internal/e2e/realclaude/` — the capture is evidence, not a deliverable. No test added, no fixture regenerated.
- `docs/knowledge/features/protocol-package.md` and everything else under `docs/knowledge/` — the documentation phase owns them.

## Open questions

1. **Does #1720 keep this as a push, or turn it into a request/reply?** Deliberately undecided, and cheap either way: a client's decode path is the same frame, so only this constant's `excludedTypes` classification moves from `push` to `reply`, and the verb would land together with its handler. Freezing the type now is what lets desktop#681 and #694 start.
2. **Does the published list carry aliases per entry, a flat name+alias list, or names only?** #1727's call, and the wire name is deliberately neutral to it — `slash_command_list` says nothing about entry shape. The measurement that constrains the answer is in § 2 and on the ticket: the names-only twin carries none of the 11 aliases, and `reset` reaches a client only through them.
3. **Does the daemon publish `terminal_slash_commands` at all?** Not decided here. The two entries it carried in the capture (`doctor`, `color`) are terminal-only, so a mobile or desktop client plausibly should not see them — but that is a payload-and-producer question for #1727 and #1720. Named in the four-word table either way, because a reader of the doc block has to know the array exists to know why it was not the source.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX, and the boundary is named rather than assumed. The data behind this frame is claude-authored text that crossed the subprocess trust boundary at the daemon's read of the `initialize` control reply — 51 command names, their descriptions and their aliases, none of it validated by this repo. **This slice moves that boundary nowhere**: it declares a string constant and classifies it in two guard lists. No value crosses, no value is parsed, no value is stored. The place the boundary becomes real is #1719 (decode into a bounded daemon value) and #1727 (the payload's own SECURITY paragraph, which is where the "claude-authored, unsanitized, a REPORT never a control input" statement belongs and where a reviewer must check it is restated rather than delegated — `docs/knowledge/features/protocol-package.md` records `BackgroundTask`'s unfixed instance of exactly that delegation failing).
- **[Trust boundaries — direction]** SHOULD FIX for #1727, addressed here by exclusion. `model_list`'s review recorded the family's first field a client is meant to send **back** (`Value`, re-validated by `internal/relay/v2session_settings.go`'s `validModel`). A slash-command list has the same hazard shape — a client sends a chosen command back inside an ordinary message body — and the same answer: publication does not make a value trusted. This slice declares no field, so there is nothing to state yet; #1727 must not pattern-match the older siblings' "it is a REPORT, never a control input" sentence onto a field whose direction is new. Named here so the finding is on record before the payload slice reads only its own ticket.
- **[Subprocess / external command execution]** No findings, and one explicit non-change. The `initialize` request rides the child's already-held-open stdin as a `control_request` — no new `exec.Command`, no new argv, no `sh -c`, no environment change. Nothing in this ticket reaches a process boundary. The adjacent temptation — that a published command name might later be spliced into an argv — is #1719's and #1720's to answer, and neither the constant nor the guard entries make it easier.
- **[Network & I/O — resource exhaustion]** No MUST FIX. The measured list is 51 entries with per-entry description and argument-hint strings, which is larger than any prior member of this family, and the v2 application envelope caps at 65519 bytes. **No cap is declared here, deliberately**: caps belong to the producer, decided at construction, and a second cap in the protocol package would be a second place the limit is decided with the two free to disagree silently — the posture `BackgroundTaskRosterPayload`, `ModelAnnouncedPayload` and `ModelListPayload` all take. The residual risk is that #1720 ships an uncapped producer; #1719's own body is the bounded-value slice, so the bound has an owner. **SHOULD FIX for #1719/#1727, not for this ticket** — a cap declared in a vocabulary-only slice would be dead code today and in the wrong place tomorrow.
- **[Error messages, logs, telemetry]** No findings. This ticket adds no error path, no log call, no metric, and no error string. The one new string is the wire constant itself, which is a literal.
- **[Tokens, secrets, credentials]** Not applicable, and not by omission. This path carries no credential: the command list comes from claude over the control channel the daemon already writes to — not from an HTTP endpoint and not from an API key. `ANTHROPIC_API_KEY` is a different credential on a different path and is not read here. The frame publishes command *names*, not their contents; a workspace command's body never crosses.
- **[File operations]** Not applicable. No path is constructed, no file is opened, no fixture is written. `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` is read as evidence while writing the spec; no code or test this ticket adds opens it, and `internal/protocol/testdata/` is untouched.
- **[Cryptographic primitives]** Not applicable. No randomness, no key material, no comparison against a secret. The frame rides the existing Noise_IK v2 session like every other v2 application envelope; nothing here changes that transport.
- **[Concurrency]** No findings. `internal/protocol` is a pure-data package and this ticket adds one immutable constant — no goroutine, no lock, no shared mutable state, no package-init work, so no lock ordering, no TOCTOU, no shutdown path and no goroutine lifecycle to reason about.
- **[Threat model alignment]** Addressed, and it is the substance of AC 2. `docs/protocol-mobile.md` § Security model's relevant threat is a hostile or compromised phone sending a frame it should not be able to send. Two independent guards enforce that for this constant and this spec requires both: `IsKnownAppType` must reject `slash_command_list` (so no v1 phone can route one into `dispatch.Route`), and `TestEveryInboundV2TypeHasHandler`'s Assertion #3 forces the constant to be classified at all. The spec also forbids the one shortcut that would defeat the second guard — declaring an inbound request verb and filing it under `excludedTypes` to dodge the missing-handler failure, which is the #949 failure class the guard exists to catch. The `<type>-rejected` row is called out in § 3 as the one edit no gate forces, because a guard nobody wrote is indistinguishable from a guard that passes.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-24
