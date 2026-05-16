# Spec: e2e/realclaude permission-prompt-tool stdio spike (#383)

## Files to read first

- `internal/e2e/realclaude/fixtures.go` — patterns for `WithWorktree(t)` ($HOME pinning), `ensurePyryBuilt` (NOT used here — we exec `claude` directly), and the build-tag convention `//go:build e2e_realclaude`.
- `internal/e2e/realclaude/allowed_tools_enforcement_test.go` — reference for the allow-list contract being probed; the spike sits adjacent and runs in the same package.
- `internal/e2e/realclaude/smoke_test.go:12-24` — `claude --version` invocation pattern with `exec.CommandContext`. The spike reuses this shape to capture the version string for the fixture filename.
- `internal/e2e/realclaude/per_agent_test.go:104-114` and `internal/e2e/realclaude/tool_loop_test.go:190-203` — `parseResultTrailer` shape and `resultTrailer.PermissionDenials`. Useful as a fallback signal: even if no inline permission event reaches stdout, denials may surface in the final `type:"result"` envelope.
- `cmd/pyry/agent_run.go:254-266` — `buildClaudeArgs`. Shows the canonical pyry argv (notably `--dangerously-skip-permissions` and `--input-format stream-json`/`--output-format stream-json`/`--verbose`). The spike intentionally diverges: it must NOT pass `--dangerously-skip-permissions` (that flag is what suppresses permission gates), and it adds `--permission-prompt-tool stdio` plus `--permission-mode <mode>`.
- `cmd/pyry/agent_run_test.go:402-...` — `TestBuildClaudeArgs_Shape`. Confirms the canonical flag set so the spike's divergent flag set can be reasoned about against a known baseline.
- `internal/agentrun/jsonl/` (via `jsonl.NewReader`) — only relevant if we later choose to parse the captured stream; the spike captures raw bytes and does not parse them in-test.
- `docs/PROJECT-MEMORY.md` — confirms `docs/knowledge/INDEX.md` is documentation-phase-only. The spec below routes the INDEX update there rather than to the developer.

## Context

`claude --permission-prompt-tool stdio` is undocumented in `--help` but is the mechanism VS Code's Claude Code extension uses to surface permission prompts to its UI. The pyrycode-mobile design depends on relaying these prompts phone ↔ pyry ↔ claude. Before that design starts, we need a captured trace of the actual event/response shapes — not guesses.

This is a spike, not a behavior test. Its outputs are:

1. A test under `e2e_realclaude` that drives one cache-cold run of `claude` with the relevant flags and captures every stream-json event verbatim.
2. A fixture under `testdata/` containing the captured trace plus the observed `claude --version`.
3. A short feature-knowledge doc summarizing the findings.

The test PASSES regardless of whether a permission event fires — the absence of an event is itself a finding.

## Design

### Package and file

- New file: `internal/e2e/realclaude/permission_protocol_spike_test.go`
- Build tag: `//go:build e2e_realclaude` (same as siblings; never runs under `go test ./...`)
- Package: `realclaude`
- Test name: `TestRealClaude_PermissionProtocol_Spike`

No new exported types. No new helpers in `fixtures.go` — the spike's invocation shape is one-off and divergent enough from `RunPyryAgentRun` that lifting it would only obscure both. (`RunPyryAgentRun` shells out to `pyry agent-run`, which always passes `--dangerously-skip-permissions`. The spike must invoke `claude` directly.)

### Argv (the divergence from pyry's canonical agent-run)

```
claude
  --input-format stream-json
  --output-format stream-json
  --verbose
  --allowed-tools Read
  --permission-prompt-tool stdio
  --permission-mode default
  --max-turns 2
  --model claude-haiku-4-5
```

Notable absences vs `buildClaudeArgs`:
- No `--dangerously-skip-permissions` — that flag short-circuits permission gates entirely; passing it would defeat the spike.
- No `--append-system-prompt-file` / `--effort` — keeps the input surface minimal so the response is reproducible across reruns.

The only `--permission-mode` value the spike runs in code is `default`. Other modes (`acceptEdits`, `plan`, `bypassPermissions`) are documented in the resulting feature doc as a manual-rerun matrix the spike-runner can sweep by editing one constant — see *Reproducing the rest of the matrix* below.

### Process I/O contract

`exec.CommandContext` with explicit pipes:

- `cmd.Stdin` ← an `io.PipeReader` the test writes a single user envelope to, then closes.
- `cmd.Stdout` ← read line-by-line via `bufio.Scanner` on a pipe; each line is appended verbatim (raw bytes, as a `json.RawMessage`) to a slice.
- `cmd.Stderr` ← captured into a `bytes.Buffer` for diagnostic embedding only.
- `context.WithTimeout(ctx, 90*time.Second)` — a hard cap. The spike does not respond to permission events (see below), so claude may block waiting forever; the timeout is the bound that lets the test finish.

The single user envelope written to stdin is the stream-json `user` shape claude consumes:

```json
{"type":"user","message":{"role":"user","content":"Use the Bash tool to run `ls -la` and report the result."}}
```

After writing one envelope and a trailing newline, the test closes stdin. Closing stdin is the EOF signal claude treats as end-of-input on the stream-json input channel. The test then drains stdout until the scanner returns `io.EOF` (process exit) OR the context deadline trips.

### Why the spike does NOT respond to permission events

The AC asks for both the request shape AND the expected response shape. The test captures the request directly. The response shape is inferred by the human reading the captured request — claude's elsewhere-control protocol uses `request_id`-mirrored response envelopes (`{"type":"control_response","request_id":"...","response":{...}}`-shaped), and the captured request will reveal whether this protocol matches. Attempting to send a heuristic response from inside the spike would either (a) succeed and leave the developer unsure which guess was correct, or (b) fail silently and contaminate the captured trace.

The follow-up issue (filed only if a real event fires) is where the response-shape probe lives. That ticket is assertion-based and CAN safely round-trip a response because the request shape is then known.

### Capture and fixture

After the process exits or the context deadlines:

1. Run `exec.Command("claude", "--version")` to capture the version string. Trim whitespace; if the output looks like `0.5.12 (Claude Code)`, extract the leading `0.5.12` token (split on whitespace, take field 0). If parsing fails, fall back to the literal trimmed output — the spike must not fail on version-parse weirdness.
2. Sanitize the version into a filename-safe slug: lowercase, replace any character outside `[a-z0-9._-]` with `_`. Cap at 32 chars. Examples: `0.5.12` → `0.5.12`; `0.5.12-rc1+build` → `0.5.12-rc1_build`.
3. Build the fixture path: `internal/e2e/realclaude/testdata/permission_protocol_v<slug>.json`. Ensure the `testdata` directory exists (`os.MkdirAll`) — it currently doesn't.
4. Marshal the fixture as:

   ```json
   {
     "claude_version_raw": "<full untrimmed --version output>",
     "claude_version": "<extracted version token>",
     "argv": ["claude", "--input-format", "stream-json", ...],
     "permission_mode": "default",
     "allowed_tools": ["Read"],
     "stdin_envelope_sent": {<the user envelope>},
     "stdout_events": [<one entry per stdout line, each a json.RawMessage so original bytes are preserved>],
     "stderr_capture": "<string, truncated to 8 KiB>",
     "exit_code": <int>,
     "context_deadline_tripped": <bool>,
     "duration_ms": <int>
   }
   ```

   `json.MarshalIndent` with two-space indent so the fixture is human-readable in a diff.

5. Write atomically: `os.WriteFile(path+".tmp", data, 0o644)` then `os.Rename(path+".tmp", path)`. (Same recipe pyrycode uses for on-disk registries — see PROJECT-MEMORY.md "Atomic-write recipe".)

6. The test logs the fixture path with `t.Logf` so the spike-runner can find it without grepping.

### Concurrency model

A single goroutine reads stdout via `bufio.Scanner` and appends each line to a slice protected by no lock — the goroutine is the sole writer, and the test goroutine reads the slice only after `cmd.Wait()` (or context cancellation followed by `cmd.Wait()`) returns. The pattern:

- Main goroutine: starts cmd, writes one envelope to stdin, closes stdin.
- Reader goroutine: scans stdout, appends lines, exits on `io.EOF` (which arrives when claude closes stdout, typically at process exit).
- Main goroutine: `cmd.Wait()`, then waits on a `done` channel the reader closes on exit.
- On context deadline: `cmd.Process.Kill()`, then both waits.

`bufio.Scanner` has a default 64 KiB line-size cap. Stream-json envelopes can include large model output. Set `scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)` — 1 MiB cap. Large lines beyond that are unlikely from a `--max-turns 2` run but the bigger buffer is essentially free.

### Error handling

The spike test's only assertion is "the run produced SOMETHING worth capturing." Specifically:

- `claude` not on PATH → `t.Skipf` (mirror smoke_test.go's diagnostic). Not a fatal — the suite already has a baseline `TestClaudeBinaryAvailable` that fails first if the binary is missing.
- `cmd.Start` fails → `t.Fatalf`. Structural.
- Context deadline trips with zero stdout lines captured → `t.Fatalf`. Means claude crashed before emitting anything; the spike has no signal.
- Context deadline trips with at least one stdout line → record `context_deadline_tripped: true` in the fixture and PASS. This is the expected path if a permission event fires and claude blocks waiting for our (un-sent) response.
- Process exits non-zero with at least one stdout line → record `exit_code` in the fixture and PASS. Claude may legitimately exit non-zero when stdin closes mid-permission-prompt.
- Fixture write fails → `t.Fatalf`. The whole point of the run is the artifact.

The shape: structural failures (process won't start, fixture won't write) fail the test; behavioral signals (claude crashed, hung, exited non-zero) get recorded into the fixture and pass.

### Testing strategy

This IS the test. There is no additional unit test — the spike is single-purpose and its own correctness is verified by the human reading the resulting fixture. Two cheap sanity checks the developer should run after writing the test:

- `go vet -tags e2e_realclaude ./internal/e2e/realclaude/...` passes.
- `go test -tags e2e_realclaude -run TestRealClaude_PermissionProtocol_Spike ./internal/e2e/realclaude/...` produces a fixture file under `testdata/` and exits 0.

### Reproducing the rest of the matrix

The AC says the spike must discover which `--permission-mode` triggers gates. Rather than encoding a four-mode loop in the test (which would multiply the cost), the test runs ONE mode (`default`). The feature doc instructs the spike-runner to repeat the run with `acceptEdits`, `plan`, and `bypassPermissions` by editing the `permissionMode` constant at the top of the test file and rerunning. Each rerun produces a fresh fixture (the filename embeds the version, not the mode — to disambiguate, the spike-runner copies/renames each result, e.g. `permission_protocol_v0.5.12_default.json`, `..._acceptEdits.json`, before overwriting on the next run).

This is a deliberate trade: the spike's test surface stays one cache-cold run (~$0.05); the matrix sweep is a manual loop the spike-runner does once and writes up.

## Outputs the developer produces

1. `internal/e2e/realclaude/permission_protocol_spike_test.go` — new file as designed above.
2. `internal/e2e/realclaude/testdata/permission_protocol_v<version>.json` — generated by running the test once. **This file is checked in.** It is a captured artifact, not a build output. The developer commits whatever the first run produces against the current real `claude` binary; subsequent reruns either match (no change) or update the fixture (which then needs a fresh review).
3. `docs/knowledge/features/permission-protocol-spike.md` — written by the developer after the spike run completes. Contents per AC:
   - Event shape on permission gate (cite the captured fixture).
   - Inferred expected response shape on stdin (with reasoning — what claude's elsewhere-control protocol convention suggests).
   - Which `--permission-mode` triggered gates (from the matrix the developer sweeps).
   - Interaction order between `--allowed-tools` and `--permission-prompt-tool stdio` (does the gate fire before or after `--allowed-tools` denial? Inferred from whether the captured trace contains a permission request for a non-allowlisted tool, or a direct denial).
   - Observed `claude --version`.
   - If no event fired in any mode, this doc states that finding clearly and the follow-up issue is NOT filed (per AC).
4. `docs/knowledge/codebase/383.md` — per-ticket implementation summary per `docs/PROJECT-MEMORY.md` convention. Documents that the spike test exists, where the fixture lives, and the divergence from `RunPyryAgentRun` (no `--dangerously-skip-permissions`).
5. **NOT** `docs/knowledge/INDEX.md` — that file is the documentation-phase agent's exclusive write surface (per `docs/PROJECT-MEMORY.md` and the architect/developer/po CLAUDE.md guards). The AC's wording asks for it; the project-level rule overrides. The developer leaves a note in the codebase summary (`docs/knowledge/codebase/383.md`) that documentation phase should append the new feature doc to INDEX.md on the docs pass.
6. **Conditional follow-up issue** — only if a real permission event fires in at least one mode. Title: `e2e/realclaude: TestRealClaude_PermissionDenialEvent — assertion-based permission protocol test`. Body: one paragraph linking to the captured fixture and proposing an assertion-based test that (a) sends a stream-json response in the inferred shape, (b) asserts claude proceeds to either invoke the tool (allow path) or emit a denial event (deny path).

## Open questions

- **Stream-json input envelope shape on the user line.** The shape `{"type":"user","message":{"role":"user","content":"..."}}` is inferred from claude's stream-json output convention, where `user` lines have exactly that shape. If claude rejects this on input (visible as a startup error in stderr), the developer should try the simpler `{"type":"user","content":"..."}` shape and capture which works in the feature doc. The spike captures stderr, so the wrong-shape failure is observable post-mortem.
- **Whether `--max-turns 2` is enough to provoke a Bash tool_use attempt.** With `claude-haiku-4-5` and a direct prompt, one turn is usually enough — but the model might prefer Read over Bash if it can satisfy the prompt that way. The prompt deliberately names Bash. If the captured trace shows zero tool_use attempts at all, the developer should bump `--max-turns` to 4 and rerun before concluding "no event fired."
- **Filename collision on rerun.** If the spike-runner reruns the same `claude` version with a different mode without renaming the prior fixture, the new write overwrites. The test logs the fixture path; the runner is responsible for the rename-then-rerun discipline. A more defensive design (timestamp suffix) would make diffing across reruns harder, so we prefer the simpler convention.
