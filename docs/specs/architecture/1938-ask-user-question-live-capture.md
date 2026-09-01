# #1938 — drive a real claude to call `AskUserQuestion` and commit the captured call

**Ticket:** [#1938](https://github.com/pyrycode/pyrycode/issues/1938) · `size:s` · `security-sensitive` · `needs-real-claude`
**Package:** `internal/e2e/realclaude` (behind the `e2e_realclaude` build tag)
**Production source files touched:** none — every Go file in this spec is a `_test.go`

---

## Files to read first

This is the turn-1 data load. Read these before writing anything; each entry names the symbol and what to extract from it.

| Path | Symbol | What to extract |
|---|---|---|
| `internal/e2e/realclaude/permission_protocol_spike_test.go` | `TestRealClaude_PermissionProtocol_Spike` | The live-spawn skeleton this test mirrors: pipes, the single reader goroutine, `cmd.Wait()` then `<-readerDone`, the deadline-tripped check. **Do not copy its argv** — see § "What the spike gets wrong for this ticket". |
| same | `captureClaudeVersion` | Returns `(raw, token)` — exactly the record's two version fields from one exec. Use it; do not re-derive. |
| same | `versionSlug`, `packageDir` | `versionSlug` produces `ClaudeVersionSlug` from the token; `packageDir` resolves the committed `testdata/` parent. |
| `internal/e2e/realclaude/ask_user_question_record_test.go` | `askQuestionFixtureRecord` | The four fields and their tags. `ToolInput` is `json.RawMessage` — **never** decode-and-re-marshal it. |
| `internal/e2e/realclaude/ask_user_question_writer_test.go` | `scanAskQuestionFixture`, `writeAskQuestionFixture` | The only sanctioned route to the artifact. Note the `t.Fatalf`-from-the-test-goroutine rule in both doc comments, and the "never `%v` the record" rule. |
| `internal/e2e/realclaude/ask_user_question_names_test.go` | `askQuestionFixtureName` | The writer mints the name from `rec.ClaudeVersionSlug`. You never call this yourself. |
| `internal/e2e/realclaude/ask_user_question_shape_test.go` | `requireAskQuestionShape`, `askQuestionShapeFindings`, `askQuestionInput` | The eight checks the captured bytes are measured against, and the decode target that must not be widened. |
| `internal/e2e/realclaude/dropped_line_capture_test.go` | `newDropcapScanner`, `dropcapScanner.scan`, `dropcapFixedNeedles` | The scanner constructor this run passes to the writer — the dynamic half, not `dropcapFixedNeedles()`. See § "The scanner". |
| `internal/e2e/realclaude/fixtures.go` | `WithWorktreeAuthenticated`, `ensurePyryBuilt`, `buildEnvWithRealHome` | Credential skip + the `pyry` binary the mcp-config points at. `ensurePyryBuilt` builds under the operator's real `HOME` on purpose. |
| `internal/e2e/realclaude/resilience_test.go` | `resolveClaudeBin` | The claude-binary skip, with the fork-bomb defence. |
| `internal/e2e/realclaude/harness_daemon_test.go` | `shortSocketPath` | Already solves the 104-byte `sun_path` cap. Reuse it — do not write a second one. |
| `cmd/pyry/mcp_config.go` | `renderMCPApproveConfig`, `permissionArgs`, `approveToolRef` | The exact config document and flag set to reproduce. Package `main`, so not importable — transcribe the shape. |
| `cmd/pyry/mcp_approve.go` | `mcpServerName`, `approveToolName`, `runMCPApprove`, `approveServer.toolsCall` | The server name/tool name the prompt-tool reference is built from, and the proof that diagnostics go to **stderr** so stdout stays a clean MCP frame stream. |
| `internal/control/protocol.go` | `ApprovePayload`, `ApproveResult`, `Request`, `Response`, `VerbMCPApprove` | What the stub socket decodes and answers. `ApprovePayload.Input` is `json.RawMessage` — this is why the bytes survive verbatim. |
| `internal/control/client.go` | `Approve`, `requestPatient` | One fresh connection per approval, no client read deadline. The stub must therefore **accept in a loop**, not once. |
| `internal/e2e/internal/fakeclaude/approve_test.go` | `TestRunStreamJSONApprove_ToolUsePrecedesTheDial` | The working stub-socket recipe: `json.NewDecoder(conn).Decode(&control.Request)` → `json.NewEncoder(conn).Encode(control.Response{...})`. Copy the shape, not the file (different package, different build tag). |
| `docs/knowledge/features/e2e-realclaude-ask-user-question-shape-test-go.md` | § "Lessons that outlive this ticket" | **Required.** Carries the batch-width limitation (only `in.Questions[0]` is checked) and the "#1938 should know this property is unpinned" note. |
| `docs/knowledge/features/e2e-realclaude-ask-user-question-writer-test-go.md` | § "Lessons that outlive this ticket" | **Required.** Carries the family's recurring defect: doc-comment claims that ship unmeasured. Applies directly to the header you are about to write. |

---

## Context

`AskUserQuestion` is claude's clarifying-question tool. #1906 wants that question batch carried out to interactive clients; #1925 is the slice that gets its wire bytes on disk so the parser downstream is written against a measurement rather than vendor prose.

Everything this run writes *through* is already merged. #1943 built `askQuestionFixtureRecord`, #1944 the namer, #1941 the deny-scanning writer, and #1951/#1952/#1950 the eight-check shape assertion. **Nothing offline lands here.** This slice produces the bytes, calls those four, and commits the result.

What the tree proves today: all eight committed `permission_protocol_*` captures list `AskUserQuestion` in their `system`/`init` `tools` array, unbroken from 2.1.143 through 2.1.199. What it does not prove: not one committed capture holds a `tool_use` block named `AskUserQuestion`. The name's every occurrence under `testdata/` is inside a `tools` array. So the *offer* is measured and the *payload* is not — and a fake built to the documented shape cannot contradict the documented shape, which is why the fake-daemon suite cannot substitute for this run.

**This design does not warrant an ADR.** It adds no decision that outlives the ticket; the design decisions it does make (approve-path-as-capture-point, deny-everything) are recorded in the file header and folded into the package overview by the documentation phase.

### Sizing — the tension, reported rather than resolved

Five of the six `size:s` boundaries are clear by a wide margin: **0** production source files, **0** new exported types, **0** consumer call sites needing simultaneous update, **5** acceptance criteria, **6** reject branches. The sixth — total written work — is not: the leanest honest construction of this file is ~460 lines (header ~60, stub server ~70, mcp-config ~35, spawn ~80, reader ~50, outcome switch ~65, fill+write+assert ~50, imports/tag ~25, misc ~25), plus ~60 lines of comment corrections across three siblings. That is ~520 against a 400-line boundary, and PO's ~750 is the same figure at this package's actual comment convention.

Three facts bear on it, and none of them is a rationalization about mechanical edits:

1. **The depth gate forecloses a split.** #1938 → #1925 → #1906 makes this a grandchild; the gate stops at two.
2. **There is no seam that brings a child lower.** The only candidate cut is (a) a live spawn that proves the tools array lists `AskUserQuestion` on 2.1.239, and (b) a live spawn that records the call. (a) produces no artifact, its deliverable is a premise rather than a product, and (b) re-pays the whole spawn — a cut that duplicates the expensive half while moving ~40 lines. The comment corrections cannot be their own ticket either: comment-only changes are observable to no test.
3. **The measured fixed per-file cost in this package is ~330–370 lines** (header, ban-table reasoning, decode targets, harness). A 400-line boundary is therefore very close to unsatisfiable for *any* new file here, and splitting cannot reduce a fixed cost — it duplicates it.

Corroborating, not deciding: all six merged siblings (#1943, #1944, #1941, #1951, #1952, #1950) landed at 934–995 insertions, every one `size:s`, and not one carries an `error:*` or salvage label. That answers *what this package has gotten away with*, not *what this ticket costs* — it is reported so the boundary can be recalibrated if it is genuinely wrong for this package, and it did not move the call. The depth gate and the absent seam did.

**Turns, not lines, are the real risk here, and the risk is the live spawn** — a prompt that fails to elicit the call burns a full spawn and produces nothing. § "Eliciting the call" is written to spend that budget once.

---

## Design

### Shape of the run

```
                    ┌──────────────────────────────────────────┐
                    │  test goroutine                          │
                    │  skips → spawn → select → fill → write   │
                    └───────┬──────────────────────┬───────────┘
                            │ starts               │ starts
                  ┌─────────▼─────────┐  ┌─────────▼──────────┐
                  │ stdout reader     │  │ stub approve server│
                  │ goroutine         │  │ goroutine          │
                  │ records init tools│  │ accept loop        │
                  └─────────▲─────────┘  └─────────▲──────────┘
                            │ stdout                │ unix socket
   ┌────────────────────────┴───────────┐           │
   │  claude  (real, --permission-       │  spawns  │
   │  prompt-tool mcp__pyry_approve__…)  ├──────────┤
   └─────────────────────────────────────┘  stdio   │
                                    ┌───────────────┴─────────┐
                                    │ pyry mcp-approve        │
                                    │ (real binary, one       │
                                    │  control.Approve conn   │
                                    │  per gated call)        │
                                    └─────────────────────────┘
```

The daemon is absent by design. `handleApprove` would park the payload keyed by `tool_use_id`; the stub socket receives the identical `control.Request{Verb: VerbMCPApprove, Approve: …}` and answers it directly. Because `ApprovePayload.Input` is `json.RawMessage` on both the marshal and the unmarshal side, the bytes reaching the stub are the bytes claude emitted — which is what makes any point on the approve path equivalent to any other, and makes `startStreamModalResolutionHarness`'s phone/noise/relay stack unnecessary.

### The one test

`TestRealClaude_AskUserQuestion_CapturesTheCall` in the new file `internal/e2e/realclaude/ask_user_question_capture_test.go`.

**Not `t.Parallel()`** — `WithWorktreeAuthenticated` reaches `t.Setenv`, which is incompatible with a parallel test. State that in the doc comment; the reason is not obvious from the call.

Sequence, all on the test goroutine unless noted:

1. `resolveClaudeBin(t)` — skips naming `PATH` / `PYRY_CLAUDE_BIN`.
2. `WithWorktreeAuthenticated(t)` — returns the dir that is **both** the pinned `HOME` and `cmd.Dir`. Skips naming `ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN` when neither is set. This is AC 3's credential-skip outcome, inherited rather than re-implemented.
3. `ensurePyryBuilt(t)` — the binary the mcp-config's `command` names.
4. `shortSocketPath(t)` → `net.Listen("unix", …)`, with `t.Cleanup` closing the listener.
5. Write the mcp-config document (§ "The spawn").
6. Start the stub approve server goroutine (§ "The stub approve server").
7. Start claude under a `context.WithTimeout`; start the stdout reader goroutine.
8. Write one user envelope to stdin, close stdin.
9. `select` on the wanted-call channel vs `ctx.Done()`.
10. Stop claude, join both goroutines, then take one of four outcomes (§ "Outcomes").
11. On the success outcome only: fill the record, write it, assert its shape.

### The spawn

The mcp-config document, written by the test because `renderMCPApproveConfig` lives in package `main`:

```json
{"mcpServers":{"pyry_approve":{"command":"<abs pyry bin>","args":["mcp-approve","-pyry-socket","<abs socket path>"]}}}
```

Write it at mode `0600` into a `t.TempDir()` (not the pinned `HOME`, which claude also reads for its own config). Reproduce `renderMCPApproveConfig`'s shape — same two keys, same argv — and say in a comment *why* it is transcribed rather than called.

argv, mirroring what `permissionArgs(false, cfg)` composes:

| Flag | Value | Why |
|---|---|---|
| `--input-format` / `--output-format` | `stream-json` | The daemon's interactive path; also what makes the `system`/`init` line readable. |
| `--verbose` | — | Required for stream-json output to carry the init line. |
| `--permission-prompt-tool` | `mcp__pyry_approve__approve` | The whole mechanism. Hard-code the string **and** assert it equals `fmt.Sprintf("mcp__%s__%s", …)`'s shape in a comment — the constants are in package `main` and unreachable. |
| `--mcp-config` | the written path | |
| `--strict-mcp-config` | — | Non-negotiable: without it a project/user `.mcp.json` can shadow `pyry_approve`. |
| `--permission-mode` | `default` | The only mode that consults the prompt tool. |
| `--max-turns` | `2` | One turn to ask, one of margin. |
| `--model` | see § "Eliciting the call" | |

**No `--allowed-tools` at all.** An allowlisted tool is never routed to the prompt tool, so anything listed there can never park — and `AskUserQuestion` in particular must not appear. Omitting the flag entirely also means every other tool claude reaches for is gated and denied, which is how AC 4's "no gated tool executes" is satisfied structurally rather than by a per-tool allowlist the developer has to reason about.

**No `--dangerously-skip-permissions`.** It disables the permission path outright.

`cmd.Dir` is the worktree; `cmd.Env` stays nil so the child inherits the process environment, which `t.Setenv` has already pinned (`HOME`, the credential variable). Do **not** use `buildEnvWithRealHome()` for the claude spawn — that helper exists for the `go build`, and handing claude the operator's real `HOME` would defeat the worktree isolation.

### What the spike gets wrong for this ticket

`permission_protocol_spike_test.go` is the right skeleton and the wrong argv. It passes `--permission-prompt-tool stdio`, and `stdio` is not a served MCP tool: in the committed `permission_protocol_v2.1.199.json` the gated `Bash` call runs to completion, returns a real directory listing as its `tool_result`, and the run ends `result success`. That file's own header says the test passes whether or not a permission event fires. Copied wholesale it would violate AC 4 — a gated tool would execute. Take the process plumbing; replace the flags.

### The stub approve server

A goroutine owning an accept loop, because `control.Approve` opens a **fresh connection per approval** (`requestPatient`) and claude may gate several tools before it asks its question.

Per connection: decode one `control.Request`, and

- if `req.Approve == nil` → answer `control.Response{Error: …}` and move on;
- if `req.Approve.ToolName` is the wanted name → publish a **copy** of `*req.Approve` on a buffered channel (capacity 1, non-blocking send so a second call cannot deadlock the server);
- **always** answer `control.Response{Approve: &control.ApproveResult{Behavior: permbridge.BehaviorDeny, Message: <fixed constant>}}`.

Deny is unconditional — including for the wanted call. AC 4 asks for no side effects, and denying the clarifying question costs nothing: the bytes are already captured by the time the verdict is composed.

`Message` is a fixed package-level constant, never derived from `req`. Use `permbridge.BehaviorDeny` rather than the literal `"deny"` so a rename in the bridge reaches this file.

The loop exits when `ln.Close()` makes `Accept` return an error. Close the listener from `t.Cleanup`, and join the goroutine before the test returns so `-race` sees no write outliving the test.

**Nothing in this goroutine calls `t.Fatalf`.** `scanAskQuestionFixture`, `writeAskQuestionFixture` and `requireAskQuestionShape` all fail fatally and all three doc comments name this test's goroutines as the place not to call them from. Accept/decode/encode failures here are `t.Logf` plus `return`; the outcome switch on the test goroutine is what turns silence into a verdict.

### The stdout reader

One goroutine, `bufio.Scanner` over `cmd.StdoutPipe()` with the spike's 1 MiB buffer.

It decodes each line into a minimal envelope — `{"type","subtype","tools"}` — and records the first `type=="system" && subtype=="init"` line's `tools` array. That array is the sole input to AC 3's "not offered" diagnosis, and asserting it is what keeps a model that simply declined to ask from being misreported as a claude release that stopped offering the tool.

It does **not** accumulate the stream. AC 5's "keep the artifact narrow" applies to memory as well as to disk: the record is the call, not the session, and there is no field for stdout to land in.

The reader's recorded state is read by the test goroutine **only after the reader is joined** (`<-readerDone`). No mutex, no race, and the join is what establishes the happens-before edge.

### Outcomes

Four, each with its own message. AC 3 names three failing outcomes; the fourth splits "no `system`/`init` line at all" out of "not offered", because a spawn that never initialised and a claude release that dropped the tool are different defects and a shared message would send the reader after the wrong one.

| Outcome | Trigger | Result |
|---|---|---|
| skip — no credentials | `WithWorktreeAuthenticated` | `t.Skipf`, inherited verbatim. Names both variables and the Keychain recipe. |
| fail — not offered | init line seen, its `tools` array lacks `AskUserQuestion` | `t.Fatalf` naming *that*: the spawn's tools array did not offer the tool. Include the claude version and the observed array. |
| fail — no init line | reader saw no `system`/`init` line | `t.Fatalf` naming *that*, with the exit code, the deadline flag and the (capped) stderr. |
| fail — not called | init offered it, deadline tripped, channel empty | `t.Fatalf` naming *that*: the tool was offered and no call arrived before the deadline. Include the deadline. |
| pass | a payload arrived on the channel | Fill → write → assert shape. |

**No empty outcome reports success.** There is no path that reaches the end of the test without either a skip, a `t.Fatalf`, or a written artifact — write the function so the success branch is the only one that falls through, and say so in the doc comment.

On the pass branch, stop claude as soon as the payload is in hand: cancel the context, `cmd.Wait()`, `<-readerDone`. That is AC 4's "stops once the wanted call is recorded". A killed claude on the success path is expected, so its `waitErr` is logged, never fatal.

### Fill, write, assert

```go
rec := &askQuestionFixtureRecord{
    ClaudeVersionRaw:  versionRaw,               // captureClaudeVersion's first return
    ClaudeVersionSlug: versionSlug(versionToken), // its second, slugged
    ToolName:          payload.ToolName,          // NOT a literal — see below
    ToolInput:         payload.Input,             // verbatim json.RawMessage
}
```

Three constraints, each load-bearing:

- **`ToolInput` is assigned, never round-tripped.** No `json.Unmarshal` into `any` and back, no `json.Compact`, no re-`Marshal`. `encoding/json` emits map keys in sorted order, so a generic decode silently rewrites the call's own key ordering and the artifact stops being a recording. `ApprovePayload.Input` is already `json.RawMessage`; assigning it is the whole job.
- **`ToolName` comes from the payload.** AC 1 requires the shape check to read the capture's own field rather than the file name. If claude spells it differently, `askQuestionShapeFindings` reports `tool_name` and the run reddens — which is the measurement working, not a bug to route around.
- **`ClaudeVersionSlug`, never `ClaudeVersionRaw`,** reaches the writer's name minting. The raw field is a whole `claude --version` line. On the installed 2.1.239 the artifact lands as `ask_user_question_v2.1.239.json`.

Then, **in this order**:

1. `path := writeAskQuestionFixture(t, artifactDir, scanner, rec)` where `artifactDir := filepath.Join(packageDir(t), "testdata")`.
2. `requireAskQuestionShape(t, rec)`.

The order matters and the comment must say why: `requireAskQuestionShape`'s own failure message tells the reader to *"read them from the committed artifact instead"*, which presupposes the artifact exists. Asserting first would fail the run with the divergence unreadable — the exact opposite of what a measurement ticket wants when the measurement disagrees with the documented shape.

3. `t.Logf` the path and the byte count. **Never `%v`, `%+v` or `%#v` the record** — that prints `tool_input` into a run log this pipeline salvages.

`writeAskQuestionFixture` is the only sanctioned route. A direct `json.Marshal` + `os.WriteFile` bypasses the deny-scan and nothing detects it: `finOfflineExecBans` is per-file and this file execs, so it carries no entry and cannot.

### The scanner

```go
scanner := newDropcapScanner(worktree /*tempHome*/, artifactDir, worktree /*workdir*/)
```

**Not `dropcapScanner{needles: dropcapFixedNeedles()}`.** #1941's two callers use the fixed-only form because their file bans `newDropcapScanner` — that constructor reads `os.Getenv` and `realHome`, which would make an offline table green or red depending on whose machine ran it. This run has no such constraint and every reason to scan against the machine's actual credentials and paths: it is the run that produces bytes a model wrote.

`tempHome` and `workdir` are the same directory here, because `WithWorktreeAuthenticated` returns one dir serving as both. Pass it twice rather than inventing a second; note the identity in a comment so a reader does not read it as a copy-paste slip.

**Consequence the developer must plan for:** the fixed class `/var/folders/` is armed unconditionally, and the worktree lives under it on macOS. If claude's question text quotes its working directory, the scan refuses, **nothing is written**, and the spawn's tokens are spent. That is the design working, but it makes the prompt's content a correctness concern, not a stylistic one — see below.

### Eliciting the call

The prompt is the single highest-risk decision in this ticket, because a spawn that does not elicit the call costs a full run and yields nothing.

**Shape it as an abstract product choice with no filesystem or repository context.** Two constraints:

- It must make claude *want* to clarify, and must ask for a form that produces the eight fields the shape check reads: at least one question, non-empty `question` and `header`, **two or more options**, a non-empty `label` and `description` on every option, and a present `multiSelect` key. Presence, not truth — `false`, `true` and explicit `null` all pass, so the prompt does not need multi-select *enabled*, only asked in a form where claude emits the key.
- It must not induce claude to quote a path. Give it no directory, no filenames, no "in this repo". A question about which of two named abstract approaches to take is enough. This is what keeps the deny-scan from refusing bytes that are otherwise perfect.

Naming the tool explicitly in the prompt is legitimate and is not "loosening a check": the ticket measures the tool's *payload shape*, not claude's spontaneous propensity to reach for it.

**Model:** start with `claude-sonnet-5`, not `claude-haiku-4-5`. The spike's haiku choice optimises for cost on a probe that passes either way; this run's failure mode is an unproduced artifact that blocks #1939, and tool-selection reliability is worth more than the token delta. If sonnet elicits it on the first spawn, leave it.

**A divergence is the measurement, not an obstacle.** `askQuestionInput` is written against the documented shape, and whether claude nests options under each question or flattens them across the batch is unmeasured — this capture settles it. If the real call flattens, or omits a per-option description, `requireAskQuestionShape` reddens. **Report that divergence in the PR.** Do not widen `askQuestionInput` to accept a second spelling and do not loosen a check to get green: a target accepting both cannot redden on either, and reddening is how the capture reports what it found.

**Known limit, so it is not mistaken for coverage:** the shape assertion reads `in.Questions[0]` only, and the batch-width property is unpinned (recorded in the shape package overview as *"#1938 should know this property is unpinned before assuming it's covered"*). If the captured call carries several questions, only the first is measured. Do not add a check here to close that — this file ships no shape logic.

### Committing the artifact

An agent run happens in a worktree that is discarded when the run ends, so **the run must `git add` what it wrote**. #1688's PR is the pattern: probe test and artifact in one commit.

```bash
make e2e-realclaude          # or: go test -tags e2e_realclaude -count=1 -run TestRealClaude_AskUserQuestion_CapturesTheCall ./internal/e2e/realclaude/
git add internal/e2e/realclaude/ask_user_question_capture_test.go \
        internal/e2e/realclaude/testdata/ask_user_question_v2.1.239.json
git status --short internal/e2e/realclaude/testdata/   # must show the file as ADDED, not untracked
```

No ignore rule matches `ask_user_question_v*.json` today, but the hazard is live rather than historical: `.gitignore` ignores `testdata/permission_protocol_v*.json` because a probe regenerating its capture on every run twice rode into unrelated commits. **Confirm the new file is tracked, not merely written** — `git check-ignore --no-index <path>` and `git ls-files <path>` both. On 2026-08-25 #1763's live gate ran green, spent real tokens, and landed none of the three artifacts its criteria asked for.

Read the count of tests executed, never the exit code. This package is behind `e2e_realclaude`; `make check` never compiles it and the suite exits 0 both on a build failure and on a full credential skip. `make preship` is the gate that proves the package builds.

---

## Comment corrections (AC 5)

Thirteen sites across three files. Every one is comment-only; no code changes.

`ask_user_question_writer_test.go` — seven sites, all `#1942 and #1938` / `#1942's and #1938's` → **#1938 alone**, with the surrounding number and pronoun agreement fixed (*"neither can ever carry an entry"* → *"it can never carry an entry"*; *"Addressed to those two"* → *"Addressed to it"*; *"They must not inherit"* → *"It must not inherit"*):

1. header, *"the fill site is #1942's and #1938's"*
2. the `notApplied` paragraph, *"would refuse EVERY live capture #1942 and #1938 attempt"*
3. `scanAskQuestionFixture`'s doc, *"including from #1942's and #1938's stdout readers"*
4. `writeAskQuestionFixture`'s doc, *"with #1942 and #1938 passing the committed directory later"*
5. the bypass paragraph, *"#1942's and #1938's live capture files exec, so neither can ever carry an entry"*
6. the round-trip `t.Errorf` comment, *"#1942 and #1938 fill this same record's tool_input FROM A LIVE CHILD"*
7. `TestAskQuestionFixture_ScanRefusesAPlantedValue`'s doc, *"#1942 and #1938 inherit no pattern worth copying"*

`ask_user_question_record_test.go` — three sites:

8. header § "What this file deliberately is not", *"No shape assertion over a real call: that is #1942."* → names **#1951, #1952 and #1950**. This is the shape-assertion sentence AC 5's second clause covers.
9. `askQuestionFixtureFields`' doc, *"#1941 and #1942 reach it the same way"* → **#1941 alone**. This run does not use the field listing; do not add #1938 to the sentence.
10. inside `TestAskQuestionFullRecord_PinsTheFourFieldsAndTheSlugShape`, *"#1941's fill site is where the two fields are minted from one call and where that coupling becomes checkable"* → **#1938's fill site**. Beyond AC 5's literal wording (it misattributes to #1941, not #1942), but in scope by explicit deferral: `ask_user_question_writer_test.go`'s header names this exact sentence STALE and says correcting it *"belongs with the slice that actually builds the thing it describes"*. That is this ticket, and after this run the coupling genuinely does become checkable here.

`ask_user_question_shape_test.go` — the § "This file is #1942's offline successor and execs nothing" paragraph, four mentions, rewritten as one block:

11. Its counts (*"Nine shipped comments in this package name #1942 and eight describe it as a live-capture slice"*) are the state AC 5 removes; do not re-count them, drop the sentence.
12. The paragraph's argument — that this file is offline, starts no child, reads no directory, and carries a `finOfflineExecBans` entry proving it — is still true and worth keeping. Re-anchor it on what the file *is* rather than on which withdrawn ticket it succeeded.
13. Its closing *"#1938 corrects them"* becomes a statement that they are corrected, not a forward reference.

**Verify with `grep -rn '#1942' internal/e2e/realclaude/ --include='*.go'` after the edits.** AC 5 is satisfied when no surviving occurrence attributes a live capture, a fill from a live child, a live stdout read, or a write into the committed testdata directory to #1942. A neutral historical mention is not required to disappear, but none of the thirteen above is neutral.

---

## Concurrency model

Three goroutines, all joined before the test returns.

| Goroutine | Started by | Communicates via | Exits when |
|---|---|---|---|
| test | `go test` | — | after the outcome switch |
| stub approve server | test, before the spawn | buffered chan (cap 1) of `control.ApprovePayload` | `ln.Close()` from `t.Cleanup` makes `Accept` error; joined on its own `done` channel |
| stdout reader | test, after `cmd.Start` | struct fields read only post-join | stdout EOF (claude exit or kill); joined via `readerDone` |

Ordering discipline:

- The listener is bound **before** the mcp-config is written and before claude starts, so the first approval cannot race a not-yet-listening socket.
- The reader's recorded init state is read only after `<-readerDone`; that join is the happens-before edge, replacing a mutex.
- The payload channel is buffered at 1 with a non-blocking send, so a second `AskUserQuestion` call (or a retry after the deny) cannot block the server goroutine after the test goroutine has moved on.
- Shutdown is: context cancel → `cmd.Wait()` → `<-readerDone` → `ln.Close()` (via cleanup) → `<-serverDone`. Kill-path `waitErr` is logged, not fatal.
- `t.Fatalf` is reachable from the test goroutine only. This is not style: the three helpers' doc comments name these goroutines specifically.

---

## Error handling

| Failure | Handling |
|---|---|
| no claude on `PATH` | `t.Skipf` via `resolveClaudeBin` |
| no credentials | `t.Skipf` via `WithWorktreeAuthenticated`, naming both variables |
| `go build pyry` fails | `t.Fatalf` via `ensurePyryBuilt` |
| socket bind fails | `t.Fatalf` naming the path — it is a test-owned temp path, safe to print |
| mcp-config write fails | `t.Fatalf` naming the path and the error |
| claude fails to start | `t.Fatalf` with the error and the capped stderr |
| stub server accept/decode/encode error | `t.Logf` and continue the loop; never fatal from that goroutine |
| tools array lacks the name / no init line / deadline with no call | the three distinct `t.Fatalf`s of § "Outcomes" |
| deny-scan hit | `writeAskQuestionFixture` → `scanAskQuestionFixture` `t.Fatalf`, class names only, nothing written |
| shape divergence | `requireAskQuestionShape` `t.Fatalf`, check names only — artifact already on disk, readable |

stderr is captured into a `bytes.Buffer` and **truncated before printing** (reuse the spike's `truncateString` with its 8 KiB cap). It is claude's own diagnostic channel plus `pyry mcp-approve`'s `slog` output; it is not model-authored content, but it is unbounded and it can carry paths, so it is printed only on the failure branches and only capped.

---

## Testing strategy

There is one test and it *is* the measurement; there is nothing to unit-test underneath it.

- **The gate:** `make e2e-realclaude`, then `make preship` before the PR — `make check` never compiles this package, and a package that fails to build exits 0 through a shell wrapper with zero tests run. Count `=== RUN` lines.
- **The pass condition:** the artifact exists at `internal/e2e/realclaude/testdata/ask_user_question_v2.1.239.json`, `git ls-files` reports it tracked, and it appears in the PR diff.
- **Verifying the failure outcomes without four live runs:** the "not offered", "no init line" and "not called" branches are reachable cheaply by construction rather than by spending spawns —
  - *not called*: drop `--max-turns` to a value that ends the turn before a tool call, or shorten the deadline to a few seconds. Costs one cheap spawn.
  - *no init line*: point `--mcp-config` at a nonexistent path so claude exits before initialising. No tokens.
  - *not offered*: reachable only by an argv without `--permission-prompt-tool`, which is what `initialize_control_v2.1.239.json` already records — cite that committed capture as the evidence rather than re-spawning.

  Report which of the three you exercised and which you argued from the committed capture. Do **not** add a mutation harness or a table to this file; it ships one live test.
- **Comment corrections:** verified by `grep -rn '#1942' internal/e2e/realclaude/ --include='*.go'` and by reading the surviving hits, not by a test. Comment-only changes are observable to no test — that is precisely why they ride with this ticket.
- **`make cite-guard`** (inside `make check`) must stay green. Cite symbols, never lines, in every comment you add.

**One standing lesson from this family, which applies directly to the header you are about to write:** doc-comment claims here keep shipping unmeasured, and code review keeps finding them rather than the file finding them itself — four times across #1943, #1944 and #1941, the last one despite that ticket's own spec naming the pattern. **Do not write a claim you have not measured.** If you want to say a mutant reddens, run it under `go test -overlay`; if you have not run it, write the instruction without the justification.

---

## Open questions

1. **Does `AskUserQuestion` actually route through `--permission-prompt-tool`?** This is the ticket's premise and the thing being measured. Anthropic's guidance describes one callback for both approvals and questions, distinguished by tool name, and the tool is offered under every prompt-tool spawn in the committed captures. If it turns out to bypass the prompt tool entirely, the run fails on the "not called" branch with the tools array asserted — which is the correct report. **Do not fall back to reading the `tool_use` block off stdout to get green.** AC 1 says "as the approve path receives it", and a stdout-sourced capture would answer a different question than the daemon needs answered. Report it and route back.
2. **Does 2.1.239 still offer the tool under a prompt-tool spawn?** Unmeasured — the newest committed capture, `initialize_control_v2.1.239.json`, was spawned *without* `--permission-prompt-tool` and does not list it. The tools-array assertion is what separates that from a model that simply did not ask.
3. **Nested or flattened options?** Unmeasured; this capture settles it. Divergence → red → report, never widen the decode target.
4. **How many spawns will the prompt need?** Budget one. If the first fails on "not called", change the prompt's directness (not the model, not the checks) and try once more; if a second fails, report the finding rather than iterating on a live gate.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The design has exactly one untrusted→trusted crossing: `control.ApprovePayload.Input`, model-authored bytes arriving over the stub Unix socket. The boundary is explicit and single: those bytes are assigned to `askQuestionFixtureRecord.ToolInput` as an opaque `json.RawMessage` and are never parsed, dispatched on, or interpolated by this file. Every downstream consumer that *does* parse them — `askQuestionShapeFindings` — is structurally prevented from carrying them back out, because its return type is a `[]string` of fixed package constants. `ToolName` is the second untrusted field and it too is stored rather than branched on; the one place it is compared, in `askQuestionShapeFindings`, compares and does not echo.
- **[Tokens, secrets, credentials]** No MUST FIX. This run generates no tokens. It *consumes* two — `ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN` — via `WithWorktreeAuthenticated`, which re-pins them into the child's environment and never writes them anywhere. The leak vector is the committed artifact, and it is closed by construction: `newDropcapScanner` arms both variables' *values* as dynamic needles, and `scanAskQuestionFixture` refuses the record before the first filesystem call if either appears in the marshalled bytes. This is exactly why § "The scanner" mandates `newDropcapScanner` over `dropcapFixedNeedles()` — the fixed-only form would leave both credential classes unarmed for the one run where a model could plausibly echo one back.
- **[File operations]** No MUST FIX. One artifact path, and no component of it is model-controlled: the directory comes from `packageDir`, and the filename is minted by `askQuestionFixtureName` from `versionSlug(token)`, whose `[^a-z0-9._-]+ → _` class cannot emit a separator. Path traversal via a hostile `claude --version` line is therefore closed at the namer, not at the call site. The write is `os.MkdirAll` → `.tmp` → `os.Rename`, so an interrupted run strands nothing under the target name. **SHOULD FIX:** the mcp-config document names an absolute socket path and the pyry binary; the spec specifies mode `0600` and a `t.TempDir()` for it. Neither is secret, but the file is an execution instruction claude obeys, and a world-writable one in a shared `/tmp` would be a local-privilege footgun in a suite that already writes there. Developer writes `0600`; code review checks.
- **[Subprocess execution]** No MUST FIX. Three processes: `claude` (path from `resolveClaudeBin`, argv entirely literal or test-derived), `go build` (via `ensurePyryBuilt`), and `pyry mcp-approve` (spawned by claude from the config the test wrote). **No model-controlled value reaches any argv** — the prompt is a literal, and the captured payload is only ever read after the last spawn. No `sh -c` anywhere. `--strict-mcp-config` is load-bearing security, not hygiene: without it a project or user `.mcp.json` could register a second `pyry_approve` server that shadows the real one and silently answers allow, defeating AC 4. Environment is inherited rather than scrubbed, deliberately — `HOME` is already pinned to the throwaway worktree, and the credential variable must reach the child. Subprocess teardown is context-cancel + `cmd.Wait()`; the `pyry mcp-approve` grandchild is claude's to reap, and `runMCPApprove`'s `signal.NotifyContext` handles its own SIGTERM.
- **[Cryptographic primitives]** Not applicable, and the reason is structural rather than an omission: this design generates no randomness, derives no keys, and compares nothing against a secret. The one comparison over attacker-influenced data — `rec.ToolName != "AskUserQuestion"` in `askQuestionShapeFindings` — is a diagnostic classification, not an authentication decision, so constant-time comparison would be noise.
- **[Network & I/O]** No MUST FIX. The listener is a Unix socket under a `0700`-by-default `os.MkdirTemp` dir, reachable only by the running user; no TCP, no TLS, no HTTP server. Input size is capped where it matters: the stdout scanner carries the spike's explicit 1 MiB `scanner.Buffer`, and stderr is truncated to 8 KiB before ever being printed. **SHOULD FIX:** the per-connection `json.Decoder` on the stub socket has no explicit size cap. The peer is `pyry mcp-approve` on the same machine as the same user, and the whole run is bounded by the spawn deadline, so this is not exploitable as designed — but a decoder reading an unbounded object from a socket is the shape worth naming. Developer may set a read deadline on the accepted conn; code review need not gate on it.
- **[Error messages, logs, telemetry]** No MUST FIX, and this is the category with the most design attention because the pipeline salvages run logs. The rules, each with a named enforcement point: `requireAskQuestionShape` prints check names and `claude_version_slug` only; `scanAskQuestionFixture` prints class names and counts, never the offending value nor its offset; `writeAskQuestionFixture` prints `claude_version_slug` and the error and nothing else. This spec adds the matching call-site rule — **never `%v`, `%+v` or `%#v` the record**, because `%+v` prints `ToolInput`. The one thing this design prints that it does not author is claude's stderr, and it is capped and confined to failure branches. `pyry mcp-approve`'s own `logVerdict` emits only `tool_use_id` and `behavior`, never the input, and it goes to stderr.
- **[Concurrency]** No MUST FIX. No locks are taken at all — the design deliberately replaces a mutex over the reader's state with a join (`<-readerDone`), so there is no lock order to get wrong and no check-then-mutate window. Every goroutine has a named exit condition and a join (§ "Concurrency model"); the payload channel is buffered with a non-blocking send so the server goroutine cannot wedge after the test goroutine proceeds. Shutdown mid-write is covered by the writer's temp-file-plus-rename. `go test -race` covers it.
- **[Threat model alignment]** No relay surface — this ticket stands up no phone, no noise session and no relay, so `docs/protocol-mobile.md` § Security model has no applicable threat. The CLI-side threat this ticket *does* touch is permission-bridge bypass, and it is addressed twice: `--strict-mcp-config` prevents server shadowing, and the omission of `--allowed-tools` means no tool is exempt from the prompt tool. **OUT OF SCOPE, named:** the artifact's *consumption* — decoding and pinning the committed capture — is #1939's, and no parse of the artifact lands here.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-09-01
