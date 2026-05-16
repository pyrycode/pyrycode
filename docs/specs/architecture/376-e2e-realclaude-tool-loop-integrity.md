# Spec — #376 e2e/realclaude: tool loop integrity test (multi-turn with Bash)

## Files to read first

- `internal/e2e/realclaude/fixtures.go` — `WithWorktree`, `RunPyryAgentRun`, `RunOpts`, `RunResult`, `ReadJSONL`, `JSONLEntry`. The full fixture surface this test composes against. Note the validation rules in `validateRunOpts` (`Workdir/Prompt/SystemPrompt/AllowedTools/MaxTurns/Effort/Model` all required and non-empty/positive).
- `internal/e2e/realclaude/allowed_tools_enforcement_test.go` — closest sibling. Lift its structure verbatim (file header comment, `WithWorktree`, `RunPyryAgentRun`, ExitCode/SessionID gates, `ReadJSONL`, inline-helper-on-`e.Raw` pattern). The inline `bashInvokedInRaw` helper at lines 76–94 is the precedent for the parser this spec adds.
- `internal/e2e/realclaude/prompt_fidelity_test.go` — second sibling. The `jsonlPathFor` helper at lines 79–89 already exists in the package; the new test reuses it for failure messages (no need to redefine).
- `internal/agentrun/jsonl/reader.go:38-83` — `Event` struct. `Event.Raw` carries the verbatim JSONL line bytes; `Event.Kind` whitelists `assistant`/`user`/`tool_use`/`tool_result`/`system`/`attachment`/`""`. **Crucial:** the on-disk JSONL emits `tool_use`/`tool_result` as content blocks **inside** `assistant.message.content[]` and `user.message.content[]` — NOT as top-level `Kind == "tool_use"` / `Kind == "tool_result"` events. This test parses `e.Raw` for those nested content blocks.
- `internal/agentrun/streamjson/emitter.go:251-275` (`trailer` struct) and `internal/agentrun/streamjson/testdata/captured_run.jsonl:7` (the synthesised `result` trailer fixture). Authoritative shape of the `type:"result"` line on pyry's stdout. **Note:** pyry's emitter does NOT include `permission_denials` (claude-only field) — see [Design § Result trailer source and `permission_denials`](#result-trailer-source-and-permission_denials) below.

## Context

#364 (prompt fidelity) and #365 (`--allowed-tools` enforcement) exercise single-turn behaviour. The interesting failure surface for stream-json mode is the multi-turn tool loop: claude emits `tool_use`, executes the tool itself, emits `tool_result`, continues to the next assistant turn, and eventually reaches `end_turn`. A regression test pins the protocol shape so a silent change in claude's stream-json contract surfaces in CI rather than at the next user-facing breakage.

This is the positive-path counterpart to #365 (which verifies Bash is blocked under `--allowed-tools=Read`); this verifies that under `--allowed-tools=Bash` the model successfully drives the full tool loop to `end_turn`.

## Design

### File layout

One new file:

```
internal/e2e/realclaude/tool_loop_test.go      //go:build e2e_realclaude
```

No other file is created or modified. No production code change.

### Test shape

`TestRealClaude_ToolLoopIntegrity` follows the structure of `TestRealClaude_AllowedToolsEnforcement` verbatim through the `RunPyryAgentRun` call, then diverges in the assertion block.

Top-to-bottom:

1. `workdir := WithWorktree(t)` — temp dir + `HOME` override.
2. **Seed the workdir** with two small text files before invocation:
   - `hello.txt` → `"hello\n"`
   - `world.txt` → `"world\n"`
   - Use `os.WriteFile(filepath.Join(workdir, name), payload, 0o600)`; `t.Fatalf` on any write error with `"seed %s: %v"` prefix.
   - These files exist so the Bash invocation has something deterministic to list and summarise. They are not asserted against — the test pins protocol shape, not response content.
3. `result := RunPyryAgentRun(t, RunOpts{...})` with:
   - `Workdir: workdir`
   - `Prompt:` a string instructing Bash use. **Recommended literal** (one line, no shell metacharacters that would surprise an LLM):  
     `"Use the Bash tool to run \"ls -1\" in the current directory, then tell me how many .txt files you see."`  
     The phrasing makes Bash the obvious tool and gives the model a concrete numeric question to answer in its final assistant turn — exercising both the tool_use→tool_result hop and the subsequent text turn.
   - `SystemPrompt:` minimal but non-empty (validator rejects empty). Recommended:  
     `"You are an e2e regression-guard test. When asked to inspect the filesystem, use the Bash tool."`
   - `AllowedTools: []string{"Bash"}`
   - `MaxTurns: 3` (assistant tool_use → tool_result → final assistant text; 3 gives one turn of slack against haiku occasionally running a verification command)
   - `Effort: "low"`
   - `Model: "claude-haiku-4-5"`
4. Gate assertions, copied verbatim from the sibling pattern (`allowed_tools_enforcement_test.go:40-51`):
   - `result.ExitCode == 0` → `t.Fatalf` with stderr on miss
   - `result.SessionID != ""` → `t.Fatalf` with first 1 KiB of stdout on miss
5. `events := ReadJSONL(t, workdir, result.SessionID)` — on-disk JSONL.
6. `jsonlPath := jsonlPathFor(workdir, result.SessionID)` — reuse the existing helper from `prompt_fidelity_test.go:79`. Used only in failure messages.
7. **Tool-loop traversal** (state machine, single pass over `events`):
   - `var bashToolUseID string`
   - `var sawToolResult bool`
   - `var sawFinalText bool`
   - For each `e` in `events`:
     - If `e.Kind == "assistant"`: parse `e.Raw` via `parseContentBlocks(e.Raw)` (inline helper, see below).
       - If `bashToolUseID == ""`: scan blocks for the first `{Type: "tool_use", Name: "Bash"}`; if found and `ID != ""`, set `bashToolUseID = block.ID`.
       - Else (Bash tool_use already captured AND `sawToolResult` is true): if any block has `Type == "text"` and non-empty `Text`, set `sawFinalText = true`.
     - If `e.Kind == "user"` AND `bashToolUseID != ""`: parse `e.Raw`; if any block has `Type == "tool_result" && ToolUseID == bashToolUseID`, set `sawToolResult = true`.
     - Parse-error fallback follows the `bashInvokedInRaw` precedent: silently skip the line (do not `t.Fatalf` on a single malformed JSONL line — mirrors `selfcheck.go:283`'s rate-limit-and-continue policy and the existing sibling).
   - After the loop, assert all three flags:
     - `bashToolUseID == ""` → `t.Fatalf("no Bash tool_use observed in assistant entries; path: %s", jsonlPath)`
     - `!sawToolResult` → `t.Fatalf("Bash tool_use id %s present but no matching tool_result; path: %s", bashToolUseID, jsonlPath)`
     - `!sawFinalText` → `t.Fatalf("no subsequent assistant text block after tool_result; path: %s", jsonlPath)`
8. **Result-trailer assertions** (parse `result.Stdout`, see [Design § Result trailer source](#result-trailer-source-and-permission_denials)):
   - Decode the `type:"result"` line via `parseResultTrailer(result.Stdout)` (inline helper).
   - `trailer.Subtype == "success"` else `t.Fatalf`
   - `trailer.StopReason == "end_turn"` else `t.Fatalf`
   - `trailer.NumTurns >= 2` else `t.Fatalf`
   - If `trailer.PermissionDenials != nil`: `len(*trailer.PermissionDenials) == 0` else `t.Fatalf`. (Today pyry's emitter never emits this field — see design note — so this branch is dormant; it is a forward-compat guard.)

### Inline helpers (test-file scope, unexported)

Two helpers, both keyed off the precedent at `allowed_tools_enforcement_test.go:76` (parse `Event.Raw` with a struct that names only the fields under assertion):

#### `parseContentBlocks(raw json.RawMessage) ([]contentBlock, error)`

Decodes the verbatim line into a struct shaped like:

```go
type contentBlock struct {
    Type      string // "tool_use", "tool_result", "text", "thinking", ...
    Name      string // tool_use only
    ID        string // tool_use only
    Text      string // text only
    ToolUseID string // tool_result only
}

type contentEnvelope struct {
    Message struct {
        Content []contentBlock `json:"content"`
    } `json:"message"`
}
```

with JSON tags `name,omitempty` / `id,omitempty` / `text,omitempty` / `tool_use_id,omitempty` to keep the struct one-shot decodable for both `assistant` and `user` kinds. (Real-claude on-disk content blocks confirmed against `testdata/clean.jsonl`: assistant tool_use blocks carry `id` + `name` + `input`; user tool_result blocks carry `tool_use_id` + `content`; assistant text blocks carry `text`.)

Returns `(blocks, nil)` on success or `(nil, err)` on malformed JSON. Callers treat parse errors as a skip (do not fail the test on a single malformed line).

#### `parseResultTrailer(stdout []byte) (*resultTrailer, error)`

Scans stdout line by line via `bufio.Scanner` over `bytes.NewReader(stdout)`. For each line, attempts `json.Unmarshal` into:

```go
type resultTrailer struct {
    Type              string              `json:"type"`
    Subtype           string              `json:"subtype"`
    StopReason        string              `json:"stop_reason"`
    NumTurns          int                 `json:"num_turns"`
    PermissionDenials *[]json.RawMessage  `json:"permission_denials,omitempty"`
}
```

Returns the first decoded line where `t.Type == "result"`. Pointer-typed `PermissionDenials` so callers distinguish "field absent" (nil) from "field present, empty slice" (non-nil, len 0).

If no result line is found, returns `(nil, errors.New("no type:result line in stdout"))`. Caller `t.Fatalf`s with the first 1 KiB of stdout (mirrors the SessionID-missing failure message pattern at `allowed_tools_enforcement_test.go:45-50`).

The `bufio.Scanner` default 64 KiB line limit is sufficient — the `result` trailer pyry emits is ~400 bytes (per `streamjson/emitter.go:251-275` field set) and other stream-json lines on stdout are bounded by the upstream JSONL line size. If a future change pushes a stream-json line past 64 KiB this helper will skip-and-miss-the-result; in that case extend the scanner buffer via `scanner.Buffer(make([]byte, 1<<20), 1<<20)`. Not needed today — call out in a one-line comment in the helper, no code yet.

### Result trailer source and `permission_denials`

The on-disk JSONL (read via `ReadJSONL`) emits `assistant`/`user`/`system` entries only. There is **no** `type:"result"` line on disk — that event is exclusive to pyry's stream-json **stdout** trailer (see `streamjson/emitter.go:176-220` `Close`).

The acceptance criteria mention the `result` event without disambiguating its source. The spec resolves this: the `result`-event assertions read `result.Stdout`, not `events` from `ReadJSONL`. This matches the only place such an event is produced in pyry's runtime.

Pyry's emitter (`streamjson/emitter.go:251-275`'s `trailer` struct) emits eleven fields: `type`, `subtype`, `is_error`, `duration_ms`, `num_turns`, `result`, `stop_reason`, `session_id`, `total_cost_usd`, `usage`, `terminal_reason`. **`permission_denials` is not in that set** — it's a claude-only field present in `claude -p` output but dropped by pyry's re-emitter (see `streamjson-package.md` § Trailer field set). Today the conditional check in step 8 never fires. The pointer-typed field is retained per the AC ("if present in the schema") as a forward-compat guard against either:

- pyry's emitter growing the field (would require a separate spec; would land alongside `permission_denials` propagation through `jsonl.Event`)
- a future test variant that compares against claude's native stdout (out of scope here)

### Concurrency model

None. The test is single-goroutine: invoke `RunPyryAgentRun` (which itself blocks until the child exits), then parse two byte buffers (`events`, `result.Stdout`) synchronously. No `context`, no channels, no `t.Cleanup` beyond what the fixture helpers register internally.

### Error handling

- Workdir seeding failure → `t.Fatalf("seed %s: %v", name, err)` before `RunPyryAgentRun`. Without seeded files, the Bash invocation behaviour is non-deterministic.
- `result.ExitCode != 0` or `result.SessionID == ""` → `t.Fatalf` with diagnostic context (stderr / truncated stdout). Sibling-pattern verbatim.
- `parseContentBlocks` returns an error on a single line → silent skip (mirrors `bashInvokedInRaw` precedent). A test should not turn a PASS into an inconclusive on one malformed line.
- `parseResultTrailer` returns an error (no `type:"result"` line in stdout) → `t.Fatalf` with stdout prefix. Unlike per-line skip, missing the entire trailer is a hard regression.
- All `t.Fatalf` failure messages include the path string from `jsonlPathFor` where the failure is about the on-disk JSONL. Stdout-based failures include a stdout prefix instead.

### Testing strategy

This **is** the test. There is no second-order test of the test (no fixture-driven unit test for `parseContentBlocks` — its correctness is bounded by what the production claude CLI emits and the assertion path within the test itself). The shape parallel to `bashInvokedInRaw` is intentional: small inline helpers, no cross-test reuse.

The test runs under `//go:build e2e_realclaude` only. Default `go test ./...` does not compile it. Nightly CI invokes it via the existing `e2e_realclaude` job; estimated cost ~$0.02 per run (haiku, low effort, ≤3 turns).

### Open questions

- **Prompt brittleness against haiku revisions.** Haiku may, on a future model update, decline to use Bash when reading two small files (since `Read` could accomplish the same goal). If the test starts flaking on "no Bash tool_use observed", the fix is to harden the prompt ("you MUST use Bash") and/or pin a snapshot via `claude-haiku-4-5-<date>` if Anthropic exposes versioned aliases. Not anticipating this today; documenting it so a future flake has a fast diagnosis path.
- **`num_turns >= 2` lower bound.** A tool-loop run necessarily has ≥2 assistant entries (the tool_use turn + the final text turn). Equality at 2 is the happy path; ≥2 absorbs any thinking-block resolution turns or post-tool clarification turns within `MaxTurns: 3`.
