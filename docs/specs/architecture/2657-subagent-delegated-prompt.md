# #2657 — a subagent's delegated prompt reaches clients as an unrecognized_message row

## Files read

- `internal/streamsup/parser.go` → `Parser.emitUser` — the harness-text guard the new trigger joins, and the `UnrecognizedUserBlock` fall-through it currently reaches.
- `internal/streamsup/parser.go` → `userLine` (`ParentToolUseID` as `json.RawMessage`, `IsSynthetic`) and `parentToolUseID` — the decode already carries the key; a non-string or over-cap value reads as `""`, which is the fail-closed direction for this rule.
- `internal/streamsup/parser.go` → `harnessNoOutputNudge` docblock — the harness-text census; the delegated prompt is NOT harness prose, which the guard comment must say.
- `internal/streamsup/runner.go` → the spawn argv carrying `--forward-subagent-text` (#2192). `streamsup.New` adds it itself, so a rig built on `streamsup.New` captures with the daemon's own flags without naming them.
- `internal/streamsup/parent_tool_use_capture_test.go` → `readParentCapture`, `parentReaderGate`, `TestRealClaudeParentToolUseCaptureJoinsSubagentWorkToItsAgentCall` — the reader shape to mirror. Its 2.1.259 fixture was taken without the flag and stays untouched.
- `internal/e2e/realclaude/parent_tool_use_capture_test.go` → `ptucRecord`, `ptucCollect`, `ptucAwaitTurn`, `ptucReadLine`, `ptucFiles`, `ptucIsAgentTool`, `ptucMinAttributed`, `ptucWriteRecord` — the rig to copy; most helpers are reused directly (same package).
- `internal/e2e/realclaude/testdata/roster_after_finish_v2.1.280.json` — shows `spawn_shape` recording `--forward-subagent-text` at 2.1.280; the local claude reads `2.1.280 (Claude Code)`.
- `internal/streamsup/parser_test.go` → `TestParser_SyntheticDropIsLoggedContentFree`, `logRecorder`, `harnessNudgeDropMsg`, `textBlock`, `toolResultBlock` — the content-free log assertion to mirror and the helpers to reuse.
- `docs/knowledge/features/development-verification.md` — capture evidence and artifact survival (the fixture must be `git add`ed by the run that lands it).

In-flight overlap: none (no remote feature branch touches these files).

## Context

Since #2192 the daemon spawns claude with `--forward-subagent-text`. With that flag, claude 2.1.280 writes the subagent's delegated prompt as a `user` line with one `text` block, stamped with the spawning Agent call's `parent_tool_use_id` and no harness flag. `emitUser` surfaces every such block as `Unrecognized{Site: UnrecognizedUserBlock, Kind: "text"}`, so every subagent spawn puts one noise row on every interactive client — and puts the prompt text on the wire a second time as `Raw`. The same prompt already reaches clients as the Agent `tool_use`'s `prompt` input, so dropping is correct; no new event.

No ADR warranted — this is a fourth trigger on an existing suppression.

## Design

### Parser (production, one file)

In `emitUser`, the harness-text guard gains a fourth OR'd trigger:

- `block.Type == "text" && parentToolUseID(ul.ParentToolUseID) != ""`

Same branch, same `continue`, same Debug log (`"streamsup: dropping known harness user block"`, attrs `site` + `type` only). Scoped to the block, so a `tool_result` on the same line still maps with its parent id. Unknown block types still surface on every line. The guard's comment is extended: the fourth trigger is not harness prose but a DUPLICATE of the Agent call's `prompt` input; it keys on the line's parent id because the capture shows no flag (to be confirmed by the capture — see Open questions). A user `text` block on a main-conversation line (parent null/absent/non-string/over-cap → `""`) still surfaces unless one of the three existing triggers takes it.

The fall-through comment ("WHAT REACHES HERE") is updated to say main-conversation lines only.

### Capture rig (`internal/e2e/realclaude/subagent_prompt_capture_test.go`, build tag `e2e_realclaude`, prefix `spc`)

- Gate: the fixture's ABSENCE at `testdata/subagent_prompt_v2.1.280.json`, with `PYRY_PROBE_SUBAGENT_PROMPT_CAPTURE=1` forcing a re-capture — the ptuc gate, for its reason (plain `make e2e-realclaude` must arm it).
- One turn on one `streamsup.New` child (daemon's own argv, which includes `--forward-subagent-text`), model sonnet, `--dangerously-skip-permissions`, no `--allowed-tools`. Fresh non-git workdir holding `ptucFiles`. Prompt asks for exactly one foreground `general-purpose` subagent via the Agent tool that reads both files.
- Record: `spcRecord` embeds `ptucRecord` (filled by `ptucCollect`) and adds `DelegatedPrompts []spcPromptLine` — one per user line whose parent id is non-empty and which carries a `text` block: frame index, raw parent id (redacted through the same table), the line's TOP-LEVEL KEY NAMES sorted, and its block types. Key names and types only, never values, so the census is content-free; the payload itself is in the frame.
- `fixtureWorthy` (own method, shadowing the embedded one): outcome fired, exactly one Agent call, `ptucMinAttributed` attributed frames, claude version == `2.1.280`, spawn shape contains `--forward-subagent-text`, exactly one delegated-prompt line and it names the Agent call, every frame `json-string`.
- Write path: deny-scan the marshalled record plus decoded base64 payloads (`dropcapScanner`), write the record to an out-of-worktree artifact dir, promote to the fixture path when worthy. The test FATALS when the record is not fixture-worthy, naming the reason — no usable capture is loud.
- Offline tests (run under the tag without creds): `fixtureWorthy` refusal matrix (one row per arm), and the delegated-prompt census over synthesized lines (a parent-stamped text line counts; a main-thread text line, a parent-stamped tool_result-only line and an undecodable line do not; keys reported as names only).

### Reader (`internal/streamsup/subagent_prompt_capture_test.go`, no tag — runs in `make check`)

- Constants `subagentPromptCaptureVersion = "2.1.280"`, path under `../e2e/realclaude/testdata/`, `subagentPromptPinnedAgentID = ""` until the live gate lands the fixture; a (fixture, pin) gate with exactly one legal skip, as `parentReaderGate`, with its four quadrants proven by a pure test.
- Provenance: `is_capture`, version, `agent_tool_use_id == pin`, non-empty frames, `spawn_shape` contains `--forward-subagent-text`.
- Replays every frame through ONE parser, bucketing events per frame. Re-derives the delegated-prompt line from each frame's own payload (type `user`, parent == pin, a `text` block): exactly one; its blocks are all `text`; it carries none of `isSynthetic`/`isReplay`/`isMeta`/`isCompactSummary` truthy (the premise: no existing trigger takes it). That frame emits no `Unrecognized`. Across the turn, at least `2` `ToolStart` and `2` `ToolUpdate` carry `ParentToolCallID == pin`.

### Hermetic parser tests (same reader file)

Matrix through `NewParser`:
- parent set + text → no events (the ticket)
- parent null / absent / `""` / `7` (non-string) + text → `Unrecognized{UserBlock, "text"}`
- parent set + unknown `image` block → `Unrecognized{UserBlock, "image"}`
- parent set + text + tool_result → only the `ToolUpdate`, with `ParentToolCallID`
- drop logged content-free: exactly one `harnessNudgeDropMsg` record with attrs `{site, type}` and no record carrying the prompt text.

## Concurrency model

No new goroutines in production. The rig runs `streamsup.Run` in one goroutine cancelled in `t.Cleanup` with a bounded wait (ptuc's shape).

## Error handling

- Parser: a line whose sidecar decode fails leaves `ul` zero → parent `""` → the block surfaces (safe direction). No new error paths.
- Rig: every failure before classification writes an `instrument-broken` record via the pre-registered cleanup; deny-scan hits fatal without writing anything; non-worthy records are written as evidence, not promoted, and fatal.

## Testing strategy

RED first: the matrix's "parent set + text → no events" row and the content-free log test fail on current `emitUser`. Then GREEN. Offline rig tests compile/run under `-tags e2e_realclaude` (vet + `go test -run 'Spc'`). The live capture is dispatcher-owned: the ticket keeps `needs-real-claude` and gains `needs-live-artifacts`; on return the builder commits the fixture plus `subagentPromptPinnedAgentID` in one commit.

## Documentation handoff

The ticket has no Documentation handoff section. Pending for the documentation stage: the harness-text census in `docs/knowledge/features/` for streamsup should gain the delegated-prompt row (not harness-authored; dropped as a duplicate of the Agent call's input; keyed on `parent_tool_use_id`).

## Open questions

1. Does the 2.1.280 line carry a flag? Resolved only by the capture. The reader asserts no truthy harness flag and records every top-level key name; if a flag appears, the return leg re-keys on it and records a Revision.
2. Does the delegated prompt arrive as exactly one line? Assumed yes; `fixtureWorthy` refuses anything else so a surprise is loud rather than pinned.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The boundary is claude's stdout → `Parser.emitUser`. The new trigger lets a value claude controls (`parent_tool_use_id`) suppress a `text` block. A hostile or confused child that stamps a parent id on a main-thread text block would hide that block — but it would also be claude choosing not to show text, which claude can already do by not emitting it; no daemon-held data is exposed and nothing is elevated. The trigger cannot reach tool output: a `tool_result`'s payload decodes into `Content`, not `Text`, and the guard requires `block.Type == "text"`. Unknown block types still surface on every line (pinned by the `image` row).
- [Trust boundaries] `parentToolUseID` reads a non-string or over-cap value as `""`, so malformed parent ids fail toward surfacing, not suppression (pinned by the `7` row).
- [Tokens / secrets] The rig captures real stdout. Mitigation inherited whole: per-test temp `$HOME`, a fresh non-git workdir holding two rig-authored marker files, rig-authored prompt, `dropcapRedactor` substitution over every string, `dropcapScanner` deny-scan over the marshalled record AND decoded base64 payloads, nothing written on a hit. SHOULD FIX (Phase B): the new `DelegatedPrompts` census records key NAMES and block TYPES only and its parent id passes through `red.str` — no free-text field is added to the record outside the already-scanned frames.
- [File operations] Record written `0o600` into an `os.MkdirTemp` dir; fixture written `0o600` to a constant repo-relative path; no path is built from claude output.
- [Subprocess] The child is spawned by `streamsup.New` with rig-constant args; no claude-derived value reaches argv. `--dangerously-skip-permissions` is confined to the empty temp workdir, as in ptuc.
- [Crypto] N/A — no randomness beyond a non-secret nonce (`time.Now().UnixNano()`), redacted as `prompt_nonce`.
- [Network & I/O] Recorder line caps inherited from `dropcapRecorder`; turn bounded by `ptucAwaitTurn`'s budget.
- [Logs] The drop log is the existing message with `site` + `type` only; the content-free test pins that no record carries the prompt text. Removing the `Unrecognized` also REMOVES a disclosure: its `Raw` put the whole delegated prompt on the wire a second time. Rig failure messages print counts, indices and key names, never payloads.
- [Concurrency] One rig goroutine with a cancel + bounded wait in cleanup; no production concurrency change.
- [Threat model] Relay clients receive strictly less (one fewer frame per spawn). OUT OF SCOPE: whether the Agent `tool_use` `prompt` input should itself be bounded on the wire — unchanged by this ticket.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25
