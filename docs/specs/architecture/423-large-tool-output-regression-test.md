# Spec — `e2e/realclaude`: tool output > 64 KiB scanner buffer regression test (#423)

## Files to read first

- `internal/e2e/realclaude/long_session_test.go` — closest peer. Same shape: `WithWorktreeAuthenticated` → `RunPyryAgentRun` with Bash → `parseResultTrailer` → `ReadJSONL` → assert + tripwire on `"bufio.Scanner: token too long"` in stderr. Mirror its diagnostic style.
- `internal/e2e/realclaude/tool_loop_test.go:148-178` — `contentBlock` struct + `parseContentBlocks(raw)` helper. The new test reuses both verbatim (same package, no new types).
- `internal/e2e/realclaude/tool_loop_test.go:207-223` — `parseResultTrailer`. **Default 64 KiB scanner.** With an 80 KiB user line in stdout, this function silently fails before reaching the trailer (Scanner returns false on a too-long token). The spec resolves this; see § Design.
- `internal/e2e/realclaude/fixtures.go:80-188` — `RunOpts` / `RunPyryAgentRun` / `RunResult` contract. `Stdout`/`Stderr` are full subprocess captures; nothing scans them for you.
- `internal/e2e/realclaude/fixtures.go:56-78` — `ReadJSONL(t, workdir, sessionID) []JSONLEntry`. JSONL parser cap is 16 MiB (`internal/agentrun/jsonl/reader.go:30`), so on-disk reads are safe.
- `internal/e2e/realclaude/permission_protocol_spike_test.go:128-133` — the canonical defensive buffer extension pattern: `scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)`. Use this exact shape for the test's local scanners.
- `internal/e2e/realclaude/prompt_fidelity_test.go:75-89` — `jsonlPathFor(workdir, sessionID)` (package-internal helper for diagnostics).
- `internal/e2e/realclaude/per_agent_test.go:135-144` — `truncate([]byte) string` (1 KiB cap for failure-message embeds).
- `internal/agentrun/streamjson/emitter.go:115-156` — `Emit` writes `ev.Raw + '\n'` verbatim to pyry's stdout. Confirms the load-bearing invariant: a JSONL line byte-equals its pyry-stdout twin (no transformation, no length-limited copy). The cross-check assertion rides on this.
- `internal/agentrun/jsonl/reader.go:27-30` — `const maxLineBytes = 16 << 20`. The on-disk JSONL tail reader is not the regression risk.
- `docs/knowledge/codebase/421.md` (if present — long-session regression sensor context) — sibling test's design notes.

## Context

`internal/e2e/realclaude/permission_protocol_spike_test.go:133` is the only test that defensively extends `bufio.Scanner`'s buffer to 1 MiB. Four other scanners in the package rely on the stdlib's 64 KiB default. No existing test exercises a JSONL line that exceeds 64 KiB, so a regression that drops the buffer extension — or that introduces a similarly truncating scanner elsewhere in pyry's stream-json forwarding path — would surface only when a real tool produces a large block, with the symptom being silent truncation or `bufio.Scanner: token too long` in stderr.

#421's `TestRealClaude_LongSessionJSONLIntegrity` already tripwires the `"bufio.Scanner: token too long"` stderr string across a ≥10-turn session, but its prompt design produces short lines per turn and never forces a single line past 64 KiB. This ticket adds the orthogonal sensor: **one big line**. The two tests together cover both the "many small lines" and "one large line" failure modes.

Cost: ~$0.02 per `make e2e-realclaude` run.

## Design

One new test file `internal/e2e/realclaude/large_tool_output_test.go` carrying one test `TestRealClaude_LargeToolOutput_ExceedsDefaultScannerBuffer`.

### Shape

```
TestRealClaude_LargeToolOutput_ExceedsDefaultScannerBuffer
    │
    ├── workdir = WithWorktreeAuthenticated(t)                    (skips when ANTHROPIC_API_KEY unset)
    │
    ├── result = RunPyryAgentRun(t, RunOpts{
    │       AllowedTools: ["Bash"],
    │       MaxTurns:     2,
    │       Model:        "claude-haiku-4-5",
    │       Effort:       "low",
    │       Prompt:       <forces single Bash call producing ~80 KiB of A's on one line>,
    │       SystemPrompt: <anti-chain, one-tool-call steering>,
    │       Timeout:      5 * time.Minute,                         (matches RunPyryAgentRun default)
    │   })
    │
    ├── Assertions on result.ExitCode, result.SessionID            (boilerplate from long_session_test.go)
    │
    ├── trailer = scanResultTrailerExt(result.Stdout)              (LOCAL scanner, 1 MiB cap — see § Why a local scanner)
    │   ├── trailer.Subtype    == "success"
    │   └── trailer.StopReason == "end_turn"
    │
    ├── events = ReadJSONL(t, workdir, result.SessionID)
    │   ├── locate first `user` event whose `message.content[]` carries a `tool_result` block
    │   ├── extract that block's `content` bytes  →  jsonlPayloadLen, jsonlBlockIdx
    │   └── assert jsonlPayloadLen > 70_000       (AC headroom under ~80 KiB target)
    │
    ├── extract the matching `tool_result` content bytes from pyry's stdout
    │   ├── scan result.Stdout line-by-line with the SAME 1 MiB-capped Scanner
    │   ├── for each `user` line, decode envelope.message.content[] → contentBlock[]
    │   ├── find block with matching tool_use_id (or the same content-block index)
    │   └── extract content bytes → stdoutPayloadLen
    │
    ├── assert stdoutPayloadLen == jsonlPayloadLen                 (EXACT match — see § Tolerance)
    │
    └── assert !bytes.Contains(result.Stderr, []byte("bufio.Scanner: token too long"))
```

### Prompt design

Deterministic Bash command preferred over `/dev/urandom` (per AC) so any future fixture-snapshot work doesn't churn:

```
SystemPrompt: "You are an e2e regression-guard test. When asked to run a shell command, " +
              "use the Bash tool exactly once. Do not chain commands with && or ;. " +
              "Do not modify or paraphrase the command — run it exactly as given."

Prompt: "Use the Bash tool exactly once to run this exact command and report when it completes:\n\n" +
        "printf '%80000s' '' | tr ' ' 'A'\n\n" +
        "Do not invoke any other tool. After the command completes, reply with one short sentence."
```

The `printf '%80000s' '' | tr ' ' 'A'` emits exactly 80,000 literal `A` characters on a single line (no trailing newline). At ~80 KiB it clears the 64 KiB stdlib cap by ~16 KiB and stays comfortably under the 1 MiB extended cap (and well under pyry's 16 MiB on-disk reader cap).

`MaxTurns: 2` because the loop is `assistant tool_use → user tool_result → assistant text → end_turn`. Two assistant turns suffice.

### Why a local scanner

`parseResultTrailer` (`tool_loop_test.go:211`) uses the stdlib's 64 KiB default. With an 80 KiB `user`/`tool_result` line in `result.Stdout` **before** the trailer line, the default scanner returns `Scan() == false` on the long line, exits the loop, and the function returns `"no type:result line in stdout"` — a false negative that masks the very regression this test exists to catch.

The new test defines a single local helper `scanLargeStdoutLines(stdout []byte) ([]json.RawMessage, error)` that:
- Wraps `bytes.NewReader(stdout)` in a `bufio.Scanner`
- Applies `scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)` (the canonical pattern from `permission_protocol_spike_test.go:133`)
- Returns a slice of `json.RawMessage` (one per line, copied so the backing array can be reused)
- Propagates `scanner.Err()` so a future regression beyond 1 MiB **fails loudly** rather than silently truncating

The test then walks that slice to find both the result trailer and the user/tool_result line, decoding each with `json.Unmarshal` + the existing `parseContentBlocks` helper from `tool_loop_test.go`. No edit to `parseResultTrailer` itself — keep the shared helper unchanged; the regression scope is one ticket.

### Locating the tool_result block

Same approach as `TestRealClaude_ToolLoopIntegrity` (`tool_loop_test.go:72-110`):

1. Walk JSONL events, find the assistant `tool_use` block whose `name == "Bash"`; capture `tool_use_id`.
2. Walk forward in events, find the first `user` event with a `tool_result` block whose `tool_use_id` matches. Capture its `Content` (a `json.RawMessage` — may be a JSON string or an array of nested content blocks per the comment at `tool_loop_test.go:151-153`).
3. On stdout side, walk the scanned lines, decode each `user` envelope with `parseContentBlocks`, find the matching `tool_result` block by the same `tool_use_id`, extract its `Content`.
4. Compare `len(jsonl.Content)` against `len(stdout.Content)`.

If the JSONL `tool_result` block's `Content` is a string (the common case for raw Bash stdout), the raw JSON-encoded length includes the surrounding `"..."` quotes — this is fine for the comparison since both sides are byte-equal. If the model wraps the output in a nested `[{type:text,text:...}]` array, the two sides are still byte-equal (the array is byte-for-byte verbatim through `Emit`). The test does NOT decode `Content` further — it compares raw `json.RawMessage` byte lengths directly.

### Tolerance: exact match

Pyry's emitter writes `ev.Raw + '\n'` verbatim (`internal/agentrun/streamjson/emitter.go:149-150`). The on-disk JSONL line and the pyry-stdout twin are byte-equal modulo the trailing newline. There is **no transformation step** between disk-read and stdout-write, so the comparison is exact:

```
len(jsonl.Content) == len(stdout.Content)
```

Document this in a code comment: any non-zero delta means a scanner / truncator was wired into the forwarding path — the regression has fired. Exact match is the strongest possible assertion; do not soften to a tolerance band.

### Test stop_reason caveat

If the model occasionally emits a transitional thinking block after `tool_result` (per the `tool_loop_test.go:91-94` precedent), the final assistant block still carries `stop_reason == "end_turn"`. The trailer's `StopReason` field reflects the run-level terminal reason; `success`/`end_turn` is the expected pair when the run completes naturally with MaxTurns=2.

If real-claude runs prove unreliable at producing a clean `end_turn` after one large Bash call at MaxTurns=2 (collapsing turns, refusing to run the literal command, etc.), bump `MaxTurns` to 3 — do **not** weaken the trailer assertions. Mirrors the steering note in `long_session_test.go:43-46`.

## Concurrency model

None. The test is a synchronous wrapper around one `RunPyryAgentRun` invocation. No goroutines, no channels, no timers beyond the `RunOpts.Timeout` default (5m).

## Error handling

Three failure shapes, each with a focused diagnostic message per the `budget_test.go` / `tool_loop_test.go` pattern (capture JSONL path, block index, and a `truncate()`-capped stdout/stderr):

1. **Run failed / no SessionID / trailer missing or non-success** — boilerplate `t.Fatalf` with stderr embedded.
2. **No `tool_result` block found, or `len(jsonl.Content) <= 70_000`** — emit JSONL path, total assistant + user event counts, and `truncate(result.Stdout)`. The "model didn't run the command" failure mode is structurally indistinguishable from "model collapsed turns"; the message asks the reader to inspect the JSONL.
3. **Mismatch `len(stdout.Content) != len(jsonl.Content)`** — emit JSONL path, both byte lengths, the content-block index, and a deliberate hint: *"this is the regression sensor for #423; a scanner in pyry's stdout forwarding path has truncated the tool_result content block"*.
4. **`scanner.Err() != nil` from the local 1 MiB-capped scanner** — fail with the error verbatim. Means the line exceeded 1 MiB, which is unexpected at 80 KiB target; surfaces a future buffer-extension regression in the test itself.
5. **`bytes.Contains(stderr, "bufio.Scanner: token too long")`** — already-canonical assertion from `long_session_test.go:135-139`; reproduce verbatim with the same diagnostic text.

## Testing strategy

This IS a test. There is no "testing the test" layer beyond:

- `make e2e-realclaude` locally with `ANTHROPIC_API_KEY` set in the outer env → confirms the prompt elicits a single Bash call with ~80 KiB stdout, the trailer asserts pass, and the byte-length equality holds.
- The test gates itself on `ANTHROPIC_API_KEY` via `WithWorktreeAuthenticated` (which calls `t.Skipf`); contributor machines without API keys see a skip, not a failure.

## Open questions

- **Will haiku-4-5 reliably emit exactly the literal `printf '%80000s' '' | tr ' ' 'A'` command?** Recent revisions occasionally paraphrase. If runs flake on this, the mitigation order is: (a) sharpen the system prompt's "exactly as given" wording, (b) bump `MaxTurns` to 3 to give a recovery turn, (c) fall back to a model-determined alternative that still produces ≥80 KiB on one line (`yes A | head -c 80000`, `head -c 80000 /dev/zero | tr '\0' A`). Do NOT weaken the byte-length thresholds or the exact-match assertion.
- **What if claude's `tool_result` wraps the body as `[{type:"text",text:"AAAA..."}]` instead of a bare string?** Both shapes carry the same byte-length contract through `Emit` (verbatim re-emission), so the assertion still fires correctly. The new test does not need to special-case either shape — comparing `len(json.RawMessage)` between disk and stdout is invariant to the inner structure.
- **Should this also assert `len(events) == 2` (one assistant + one user before the final assistant) to pin "exactly one tool call"?** No — that's the developer's call during implementation. The byte-length assertions are load-bearing; the turn-shape assertions are not.
