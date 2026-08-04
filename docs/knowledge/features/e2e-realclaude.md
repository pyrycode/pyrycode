# `internal/e2e/realclaude` — real-`claude`-binary integration suite

Sibling Go package to [`internal/e2e`](e2e-harness.md), gated by a distinct build tag so the real-`claude` trust-boundary suite is opt-in and never runs under `make test` / `make check`.

## Why a sibling, not part of `internal/e2e`

`internal/e2e` carries `//go:build e2e || e2e_install` and drives `pyry` against a fake-claude (`TestHelperProcess` or shell wrapper). That harness deliberately stops at the trust boundary with the real `claude` binary — useful for control-plane / supervisor coverage, but it can't catch the `/doctor` prompt-poisoning class of bug that broke Phase C on 2026-05-14.

`internal/e2e/realclaude` is the package where tests DO cross that boundary. Keeping it separate means:

- `make test` skips it via tag exclusion alone (no path filter).
- A future `make e2e` that picks up `e2e` / `e2e_install` won't accidentally pull real-claude tests in.
- Each suite's tag set documents its intent at the file header.

## Build tag

All files in the directory carry exactly:

```go
//go:build e2e_realclaude
```

Single tag, no alternation. The `e2e_install` precedent established the `e2e_<purpose>` naming.

## Operator authentication

`make e2e-realclaude` only exercises the trust-boundary tests if the spawned `claude` subprocess can reach Anthropic's API. The `WithWorktreeAuthenticated` fixture (documented under [What's there today](#whats-there-today)) accepts two env vars; tests skip with a named-variable diagnostic only when **both** are unset.

### Two auth paths

- `ANTHROPIC_API_KEY` — Anthropic API-key path (the "console" path: API users with billing on `console.anthropic.com`).
- `CLAUDE_CODE_OAUTH_TOKEN` — Max-plan OAuth path (the "subscription" path: paid `claude.ai` Max plan, no API key issued by default).

Either is sufficient. The fixture's exact env-var lookup, `t.Setenv` re-pinning, and `HOME` isolation rules live in the `WithWorktreeAuthenticated` paragraph below — start here for the operator setup, drop into that paragraph if you need fixture internals.

### Max-plan operator setup (macOS)

On macOS, the `claude` binary stores Max-plan OAuth credentials in the system Keychain under service `Claude Code-credentials`, with the account set to the macOS user. The entry is a ~1025-byte JSON blob; the bearer token the suite needs is `claudeAiOauth.accessToken`.

Extract it with:

```bash
security find-generic-password -s 'Claude Code-credentials' -w | jq -r '.claudeAiOauth.accessToken'
```

The first invocation against this Keychain item triggers a macOS confirmation dialog ("`security` wants to use your confidential information stored in `Claude Code-credentials` in your keychain"). Click **Always Allow** to suppress the prompt on subsequent runs from the same binary, or **Allow** if you prefer to be prompted each time.

#### Second prerequisite: `~/.claude.json` with `hasCompletedOnboarding=true` ([#496](https://github.com/pyrycode/pyrycode/issues/496))

The OAuth token alone is insufficient for the interactive (PTY) tests in the suite — `ptyrunner_byte_equivalence_test.go` and any future ptyrunner consumer. The env-var route authenticates `claude -p` (headless / `claude -p` subprocess used by streamrunner) but NOT interactive `claude`: on a fresh `$HOME`, interactive `claude` always renders the onboarding theme picker before processing any prompt, regardless of which auth env vars are set. ptyrunner reads the picker's prompt glyph as "ready", delivers the bracketed-paste prompt into the picker's input field, `claude` never proceeds, and the 31 s ptyrunner deadline fires — the failure shape that produced 19/19 FAIL on 2026-05-21 before this layer of the validation-gate-on-Max-only-Mac peel was closed (layer 1 was [#487](https://github.com/pyrycode/pyrycode/issues/487)'s `defaultMode:"deny"` rejection).

The fixture closes this on the OAuth path by copying the operator's real `~/.claude.json` into the per-test `$HOME` tempdir verbatim at mode `0o600`. The file carries `hasCompletedOnboarding=true` after any successful `claude` invocation under the operator's user account; with both the token AND that flag in place, interactive `claude` skips the picker and accepts the prompt normally. If `~/.claude.json` is unreadable, every real-claude test skips with a named-prerequisites diagnostic — tests do NOT time out at 31 s.

If `~/.claude.json` is missing on the operator's machine (fresh install, never invoked `claude` directly), run `claude` once directly under your user account, accept the onboarding defaults (theme picker, terms acknowledgement), exit, and re-run `make e2e-realclaude`. The first invocation writes `~/.claude.json` with `hasCompletedOnboarding=true`; the suite picks it up automatically on the next run.

### Token rotation and two sourcing patterns

`claude` refreshes its access token automatically during interactive use via the stored `refreshToken`, so an `accessToken` captured once and bound to a shell env var will eventually become stale (typical TTL: hours, not days; the exact value is Anthropic-owned and not pinned here). Operators have two patterns; pick by trade-off.

**Pattern A — source once at shell startup.** Add to `~/.zshenv` (or `~/.zprofile`, `~/.bashrc`):

```bash
export CLAUDE_CODE_OAUTH_TOKEN="$(security find-generic-password -s 'Claude Code-credentials' -w \
  | jq -r '.claudeAiOauth.accessToken')"
```

The token is captured when the shell starts; new shells get the current value. Long-lived shells go stale — refresh by running `exec zsh -l` or opening a fresh terminal.

**Pattern B — just-in-time shell function.** Add to `~/.zshrc`:

```bash
claude-token() {
  security find-generic-password -s 'Claude Code-credentials' -w \
    | jq -r '.claudeAiOauth.accessToken'
}

# Invoke per-command:
CLAUDE_CODE_OAUTH_TOKEN="$(claude-token)" make e2e-realclaude
```

Every invocation reads current Keychain state, so refresh is always picked up. The cost: every call pays the Keychain access (~tens of ms) and re-triggers the confirmation prompt if the operator clicked **Allow** rather than **Always Allow**.

Both patterns export to `CLAUDE_CODE_OAUTH_TOKEN` — the name the fixture and the `claude` binary both recognise. Do not alias to a shorter name and re-export; that just doubles the surface area to keep in sync.

### Negative controls — what does NOT work

These were eliminated during the 2026-05-20 recon for [#489](https://github.com/pyrycode/pyrycode/issues/489); recording them here so the next operator doesn't re-run the experiments.

- Copying `~/.claude.json` into a fresh `$HOME` does NOT authenticate. The subprocess reports `apiKeySource:"none"` and emits "Not logged in".
- Copying the entire `~/.claude/` directory (often >1 GB) into a fresh `$HOME` ALSO does NOT authenticate. The directory holds no OAuth credential; Keychain is the only source on macOS.
- Lifting the suite's `$HOME`-pinning so the subprocess inherits the operator's real `~/.claude/` is NOT a workaround. The pinning isolates per-test JSONL namespaces under `~/.claude/projects/<encoded-cwd>/<sid>.jsonl`; lifting it re-introduces a cross-test JSONL race. See the "Why `HOME` stays pinned" rationale in the `WithWorktreeAuthenticated` paragraph below. The env-var path is the only fix that keeps `$HOME` pinned.

### Out of scope for this section

- **In-suite token refresh.** The fixture does not re-read Keychain mid-run. A token extracted at process start that expires mid-run will surface as a subprocess auth failure. Deferred until observed.
- **CI Keychain access.** GitHub Actions runners have no macOS Keychain, so the Max-OAuth path is operator-machine only. The realclaude suite intentionally has no CI workflow (see "CI cadence" below). The related CI-secret-injection bug is tracked separately in [#406](https://github.com/pyrycode/pyrycode/issues/406).
- **Defaulting `pyry agent-run` to OAuth on the operator's Mac.** Not needed: the dispatcher runs in the operator's real shell where Keychain works normally, and the pre-existing `claude` installation handles auth without pyry's help. The env-var path documented here is specifically for the realclaude test suite.

## What's there today

- `smoke_test.go` (#361) — one test, `TestClaudeBinaryAvailable`, that:
  - Asserts `exec.LookPath("claude")` succeeds. **Fatal, not skip** — the suite is opted-into by typing `make e2e-realclaude`, so a missing binary is misconfiguration, not absence.
  - Runs `exec.CommandContext(ctx, "claude", "--version")` under a 10 s timeout and asserts a zero exit. `CombinedOutput()` is reported on failure for debuggability. The version string is NOT parsed — "real claude is on PATH and executes" is the entire assertion.
- `fixtures.go` — shared primitives for every downstream test. All symbols live in the same file under the same `//go:build e2e_realclaude` tag.
  - `WithWorktree(t) string` (#372) — `t.TempDir()` + `t.Setenv("HOME", dir)`, returns the path. Pins HOME for both the in-test process and any subprocess so `os.UserHomeDir()` resolves to the same root on both sides. Does NOT create `.claude/…`; the runtime owns that.
  - `WithWorktreeAuthenticated(t) string` (#409, dual-credential contract extended in [#490](../codebase/490.md)) — opt-in sibling for tests that need a **real Anthropic API response** (not just argv-shape probes). Reads both `ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN` from the outer test-runner environment and proceeds if **either** is non-empty; if both are unset/empty it calls `t.Skipf(...)` with a named-variable message that names BOTH variables and embeds the macOS Keychain extraction recipe verbatim (``security find-generic-password -s 'Claude Code-credentials' -w | jq -r '.claudeAiOauth.accessToken'`` — on a Max-only Mac the credential lives in Keychain rather than as `ANTHROPIC_API_KEY` and `claude` accepts the extracted access token via `CLAUDE_CODE_OAUTH_TOKEN`). Skip precedes `t.TempDir()` so the framework never allocates an unused workdir on the skip path. When proceeding, delegates to `WithWorktree(t)` for `$HOME` isolation and re-pins only the env var(s) that were non-empty via `t.Setenv` so `RunPyryAgentRun`'s `os.Environ()` inheritance is deterministic; an absent variable is NOT re-pinned to `""` (preserves the original outer-env shape — `os.LookupEnv` keeps distinguishing unset from set-empty for any downstream tooling that switches on it). No credential-shape validation — claude is the authority on key validity and surfaces bad values as a non-zero subprocess exit. Closes the gap that #383 surfaced: a `$HOME`-pinned subprocess can't find `~/.claude/credentials` and returns a synthetic `"Not logged in"` envelope with `error:"authentication_failed"`, exit 1, before any model/tool/permission code path runs. The plain `WithWorktree(t)` is unchanged and remains the fastest, most isolated default. **Why `HOME` stays pinned even on the OAuth-token path**: per-test JSONL namespace isolation under `~/.claude/projects/<encoded-cwd>/<sid>.jsonl` prevents cross-test JSONL collision when several tests run in parallel against the same workdir-encoding scheme; lifting `HOME` would re-introduce that race. The env-var path is the only correct fix that keeps `HOME` pinned (copying `~/.claude/` does not authenticate on macOS — Keychain is not file-backed in any portable sense, and copying `~/.claude.json` alone yields `apiKeySource:"none"`). **OAuth-path-only `<tempHome>/.claude.json` seed ([#496](https://github.com/pyrycode/pyrycode/issues/496))**: on the OAuth path the helper additionally copies the operator's real `~/.claude.json` verbatim into `<tempHome>/.claude.json` at mode `0o600` so interactive (PTY) `claude` skips the onboarding theme picker — the env-var alone authenticates `claude -p` (headless) but does NOT skip the picker on a fresh `$HOME`, and ptyrunner reading the picker as "ready" was the 31 s deadline shape that produced 19/19 FAIL on 2026-05-21. If `~/.claude.json` is unreadable on the operator's machine, the helper t.Skips naming both prerequisites (token AND `.claude.json`) with the recovery action ("run `claude` once directly to complete onboarding"); silently seeding nothing would re-introduce the 31 s timeout. The `ANTHROPIC_API_KEY` path is unchanged — no `.claude.json` read or seed (the API-key path uses headless `claude -p` which doesn't show the picker). **Post-#490 contract test coverage** (`fixtures_test.go:104-177`): `TestWithWorktreeAuthenticated_SkipsAndNamesBothEnvVarsWhenNeitherSet` (outer/inner subprocess re-exec under sentinel `PYRY_REALCLAUDE_AUTH_SKIP_INNER=1`, distinct from `GO_TEST_HELPER_PROCESS=1` so `TestMain` falls through to `m.Run()`; asserts the skip-message contains both env-var names + the `security find-generic-password` recipe + the `jq` extraction) and `TestWithWorktreeAuthenticated_OAuthTokenOnly_RepinsAndPreservesAbsentApiKey` (in-process; synthetic OAuth token literal; uses the `t.Setenv("ANTHROPIC_API_KEY", "") + os.Unsetenv("ANTHROPIC_API_KEY")` two-step to register the cleanup hook AND actually unset the variable, then asserts `os.LookupEnv("ANTHROPIC_API_KEY").present == false` to validate the "preserve outer-env shape" rule). Net effect: the 7 opt-in tests (`budget`, `large_tool_output`, `long_session`, `mcp_smoke`, `sigterm_mid_tool_use`, `resilience`, and `ptyrunner_byte_equivalence`) run on a Max-only Mac for the first time — closing the silent-skip masquerade named in #487's post-mortem. **Post-[#491](../codebase/491.md) call-site flip**: 5 more test files (`prompt_fidelity_test.go` #364, `prompt_fidelity_unicode_test.go` #419, `tool_loop_test.go` #376, `allowed_tools_enforcement_test.go` #365+#420, and `per_agent_test.go` #381's shared `runRoleSmokeTest` helper covering all 5 `*_RoleLoop` subtests) now also call `WithWorktreeAuthenticated(t)` instead of plain `WithWorktree(t)`. These tests all spawn `pyry agent-run` end-to-end against real claude but historically used the no-auth fixture, so without auth they hit either a 31 s ptyrunner deadline timeout or a 5 s streamrunner exit-1 fail; they now skip in milliseconds with the same named-variable diagnostic. Only `resilience_test.go:177`'s `TestRealClaude_MalformedStreamJSON` and `mcp_smoke_test.go` deliberately still use plain `WithWorktree(t)` (the former spawns claude directly with malformed stdin and asserts rejection before auth matters; the latter cannot pin `$HOME` without disabling MCP discovery and runs an inline `ANTHROPIC_API_KEY`-only skip — `CLAUDE_CODE_OAUTH_TOKEN` parity there is tracked as a follow-up).
  - `ReadJSONL(t, workdir, sessionID) []JSONLEntry` (#372; path composition delegated to `tuidriver.SessionJSONLPath` in [#508](../codebase/508.md)) — opens `tuidriver.SessionJSONLPath(<HOME>, workdir, sessionID)` and runs it through `jsonl.NewReader(...).Next()`. Empty file → empty slice; open/parse failures call `t.Fatalf` with the resolved path embedded. A private `resolveAndOpenJSONL` split exists so the missing-file path is testable as a returned error.
  - `JSONLEntry = jsonl.Event` (#372) — type **alias**, not a wrapper struct. Keeps downstream tests from importing `internal/agentrun/jsonl` directly while preserving full field access. See [`codebase/372.md`](../codebase/372.md) for the design rationale.
  - `RunPyryAgentRun(t, opts) RunResult` (#373) — synchronous subprocess invoker. Builds `pyry` once per test process via `sync.Once` (honours `PYRY_E2E_BIN` short-circuit), writes `<workdir>/prompt.txt` + `system.txt` from `opts.Prompt`/`opts.SystemPrompt`, invokes `pyry agent-run` with the eight required flags in `--flag=value` form (`--prompt-file`, `--system-prompt-file`, `--allowed-tools`, `--max-turns`, `--effort`, `--model`, `--workdir`, `--output-format=stream-json`), captures stdout/stderr/exit code, and returns the `session_id` parsed from the first stream-json `{"type":"system","subtype":"init",…}` line. **Non-zero subprocess exit is NOT fatal** — callers assert on `RunResult.ExitCode` themselves so the real CLI's error paths can be tested. Only structural failures (validation, build, exec start, timeout) call `t.Fatalf`. `RunOpts` mirrors `cmd/pyry/agent_run.go`'s flag surface 1:1 — no `SessionID` input, no `Mode` enum. See [`codebase/373.md`](../codebase/373.md) for the design rationale.

The composition pattern downstream tests use: `WithWorktree` → `RunPyryAgentRun` → `ReadJSONL`. Subsequent tickets (#364–#368, the actual prompt-poisoning / trust-boundary tests) build on this three-step shape.

- `prompt_fidelity_test.go` (#364) — first consumer of the trio. `TestRealClaude_PromptFidelity` runs `pyry agent-run` against the real `claude` CLI with a distinctive ASCII-only prompt literal (`INTEGRATION_TEST_PROMPT_PROMPTFIDELITY_2N7Q4R8W`), then asserts the literal survives byte-for-byte into the first `user`-kinded entry of the resulting JSONL session via `bytes.Contains(event.Raw, []byte(literal))`. Pins the streamrunner envelope's text round-trip end-to-end — would catch any future preprocessing, escaping drift, or wiring change that bypasses streamrunner. Run-level asserts (`ExitCode == 0`, `SessionID != ""`) come BEFORE the JSONL assert so a setup/auth/network failure surfaces as itself rather than as "no user entry". Uses `--max-turns=1`, `--effort=low`, `--model=claude-haiku-4-5` to minimise per-run cost (~$0.01). See [`codebase/364.md`](../codebase/364.md) for the design rationale.
- `prompt_fidelity_unicode_test.go` (#419) — UTF-8 sibling of #364. `TestRealClaude_PromptFidelity_Unicode` clones the #364 body 1:1 except for the prompt literal and test name. Literal is a single Go string with three byte-width classes (Finnish `ä`/`ö`/`å` 2-byte, em-dash `—` 3-byte, `🐍` 4-byte outside the BMP — the byte-slicing canary) wrapped in an ASCII frame `INTEGRATION_TEST_PROMPT_UNICODEFIDELITY_K3M9P2X7 … INTEGRATION_TEST_PROMPT_UNICODEFIDELITY_END_R5T8V1Z4` so substring matching stays deterministic and the frame-vs-payload diagnostic distinguishes "prompt missing" from "prompt corrupted on a UTF-8 boundary". Characters are literal in source (not `\uXXXX`) and sent without NFC/NFD normalization — the assertion is byte-identical preservation, not decode-equivalence. Reuses `jsonlPathFor` directly (same package, same build tag); no fixture-helper changes, no Makefile changes. Closes the JSON-escape / re-encoding / buffer-slicing-on-multi-byte-boundary gap that #364's ASCII-only literal deliberately left open for Finnish-speaking users. Uses the same `RunOpts` as #364 (~$0.01 per run). See [`codebase/419.md`](../codebase/419.md) for the design rationale.
- `allowed_tools_enforcement_test.go` (#365 + #420 extension) — second consumer of the trio. `TestRealClaude_AllowedToolsEnforcement` runs `pyry agent-run --allowed-tools Read` with a Bash-attractive prompt (`"List the files in the current working directory. Use the Bash tool to run \`ls -la\`."`) and asserts two co-located contracts against the same single haiku call: (1, #365) **gate held** — no assistant entry in the resulting JSONL emits a `tool_use` content block with `name == "Bash"`; (2, #420) **operator-visible signal** — when the gate held, at least one of (assistant-text refusal keyword in the fixed set `["cannot","can't","unable","not allowed","permission"]`, stream-json `result` envelope on stdout with non-empty `permission_denials` OR `is_error=true`) is present. Pins the **runtime behavior** of the agent-run argv shape that `cmd/pyry/agent_run_test.go:484` already pins statically: with `--dangerously-skip-permissions --allowed-tools Read`, the real claude binary refuses Bash AND surfaces the refusal somewhere an operator can see. A file-local 12-LoC detector `bashInvokedInRaw` mirrors `internal/agentrun/selfcheck/selfcheck.go:284-302` exactly — duplicated rather than imported to keep the dependency direction one-way (e2e tests don't depend on the daemon's audit package); the lockstep coupling is named in a comment so a future `tool_use` → `tool_invocation` rename moves both fixtures together. Two more file-local detectors land alongside in #420: `assistantTextRefusalHit(events) (bool, int)` walks `e.Kind == "assistant"` entries decoding `message.content[].{type,text}` and returns true on the first lowercased-substring match against `denialKeywords`; `structuredDenialHit(stdout) (bool, int)` scans the captured stream-json stdout with `bufio.Scanner` (default 64 KiB buffer — `result` lines are well under that, confirmed against `internal/e2e/realclaude/testdata/permission_protocol_v2.1.143_*.json` + `internal/agentrun/streamjson/testdata/captured_run.jsonl`) and returns true on the first `type=="result"` envelope where `len(permission_denials) > 0 || is_error`. Both return the scanned-entry count alongside the hit so the disjunctive-miss failure message (`if !textHit && !structHit { t.Fatalf(...) }`) names `denialKeywords`, the assistant-entry count, the stdout-line count, and the JSONL path — never raw bytes, matching `selfcheck.go:217-219` discipline. **The disjunctive shape is load-bearing**: the contract is "operator gets *some* interpretable signal", not "operator gets *this specific* signal" — a future claude that swaps text ↔ structured channels still satisfies the test; only a silent gate-no-op fails. The keyword set is intentionally narrow (broader markers like "available"/"access" false-positive in non-refusal contexts); the `is_error` branch in the structured detector is the forward-compat tolerance for a future claude that routes denial through `is_error=true` + a subtype change without touching `permission_denials`. Detector deliberately stops there — accepting `subtype != "success"` would conflate denial with unrelated failure modes (max-turns hit, timeout, API rejection). Architect chose **extension to the existing test, not a sibling test** because the new assertion needs the exact same argv/model/prompt/RunResult — a sibling would re-run the same haiku call (~$0.01) with no behavioural difference, and the two assertions guard one end-to-end contract. The pre-existing four-phase shape stays unchanged byte-for-byte; phases 5–6 append the disjunctive assertion. Complements `internal/agentrun/selfcheck` (#336), which probes the boot-time `--settings`/`defaultMode=deny` path; this probes the spawned agent-run `--allowed-tools` + `--dangerously-skip-permissions` path. The ticket was originally framed as a `defaultMode ∈ {deny, default, dontAsk}` matrix; the matrix collapsed to one row post-#391 (per-spawn settings file gone, `--allowed-tools` the sole enforcement configuration). Companion to #418 (captured-trace null-findings fixture-walk on the direct-`claude` spike argv) — no overlap; different argv. Uses `--max-turns=2`, `--effort=low`, `--model=claude-haiku-4-5` (~$0.01; #420 adds zero per-run cost — two synchronous in-memory walks over the same `events` slice and `result.Stdout` bytes). See [`codebase/365.md`](../codebase/365.md) for the gate-held design rationale and [`codebase/420.md`](../codebase/420.md) for the operator-signal extension.
- `tool_loop_test.go` (#376) — third consumer of the trio. **Positive-path counterpart to #365.** `TestRealClaude_ToolLoopIntegrity` seeds the worktree with two text files (`hello.txt`, `world.txt`), runs `pyry agent-run --allowed-tools Bash` with a prompt that drives the model to enumerate them (`"Use the Bash tool to run \"ls -1\" in the current directory, then tell me how many .txt files you see."`), and asserts the full internal tool loop runs end-to-end: assistant `tool_use` (Bash, with id captured) → user `tool_result` (matching `tool_use_id`) → subsequent assistant `text` block → `end_turn`. Two assertion phases: a single-pass state machine over the on-disk JSONL events (three flags: `bashToolUseID`, `sawToolResult`, `sawFinalText`) and a stdout-line scanner that decodes pyry's `type:"result"` trailer for `subtype=="success"`, `stop_reason=="end_turn"`, `num_turns>=2`. **Critical insight pinned by this test:** `tool_use`/`tool_result` are content blocks under `message.content[]` on `assistant`/`user`-kinded entries on disk, NOT top-level events — `Event.Kind`'s whitelist permits those strings but that path is unreachable for production claude. The `result` trailer is on pyry's stdout, NOT on disk. Two file-local helpers (`parseContentBlocks`, `parseResultTrailer`) parse `Event.Raw` and `result.Stdout` respectively, with `omitempty`-tagged single-shape structs that handle both `tool_use`, `tool_result`, and `text` content blocks. `resultTrailer.PermissionDenials *[]json.RawMessage` is pointer-typed so callers distinguish "field absent" (nil) from "field present, empty" (non-nil, len 0) — today pyry's emitter never emits this claude-only field (dropped by `streamjson/emitter.go`'s re-emitter), so the conditional is a dormant forward-compat guard per the AC. Uses `--max-turns=3` (slack against haiku occasionally running a verification command), `--effort=low`, `--model=claude-haiku-4-5` (~$0.02). See [`codebase/376.md`](../codebase/376.md) for the design rationale.
- `mcp_smoke_test.go` (#384) — sixth consumer of the suite. **MCP server smoke suite.** Four named top-level tests, one per MCP server the dispatcher agents depend on: `TestRealClaude_MCP_QMD` → `mcp__qmd__query`, `TestRealClaude_MCP_Context7` → `mcp__plugin_context7_context7__resolve-library-id`, `TestRealClaude_MCP_CodeGraph` → `mcp__codegraph__codegraph_search`, `TestRealClaude_MCP_Figma` → `mcp__plugin_figma_figma__get_metadata`. Each drives a haiku turn through `pyry agent-run` and asserts the named MCP tool fires with a non-empty `tool_result` — protocol drift on any of the four servers (renamed tool, changed parameter shape, broken stdio handshake) surfaces as a structured failure here rather than silently wedging a dispatched agent mid-run. **HOME is deliberately NOT pinned** (no `WithWorktree` / `WithWorktreeAuthenticated`): both pin `$HOME` to a tempdir, which disables claude's MCP server discovery (`$HOME/.claude.json` + `$HOME/.claude/plugins/...`). Tests allocate a workdir via `t.TempDir()` and leave HOME pointing at the outer operator's home so claude resolves the same MCP server set the operator has configured; per-test JSONL files land under `~/.claude/projects/<encoded-tempdir>/` and are bounded by tempdir uniqueness. Inline `ANTHROPIC_API_KEY`-unset skip per test (mirrors `WithWorktreeAuthenticated`'s message shape, inlined because the helper pins HOME). File-local pre-flight probe `mcpServerHealthy(t, displayName) (bool, string)` runs `claude mcp list` under a 10 s `context.WithTimeout`, parses each line for `<displayName>:` and the `✓ Connected` sentinel; returns `(true, reason)` for absent / `! Needs authentication` / `✗ Failed to connect` / probe-timeout — caller `t.Skipf`s with the reason so the test board surfaces operator-actionable context. `PYRY_CLAUDE_BIN` is intentionally NOT honored by the probe (reserved for stubbed pyry tests; fork-bomb defenses live in `resilience_test.go:resolveClaudeBin`). Plugin-prefixed names (`plugin_context7_context7`, `plugin_figma_figma`) are pinned because that's what claude actually emits in JSONL `tool_use.name`; mismatches with the unprefixed `mcp__context7__*` form in `dispatcherBaseTools` (#381) are invisible there because role smoke prompts don't compel a context7 call — a follow-up to align the dispatcher list is scoped out of #384. Codegraph test additionally seeds a one-symbol Go source file + runs `codegraph index .` (30 s timeout) in the workdir before driving the call — an MCP server with no index returns empty results regardless of protocol health, so the seed closes the false-negative window; `exec.LookPath("codegraph")` miss skips the test. Figma test pins a public Figma Community file URL (`figmaTestFileURL = "https://www.figma.com/community/file/1035203688168086460/material-3-design-kit"`) as a file-level const with a top-of-file swap-license comment ("if this URL stops resolving, replace with another stable public file; the test asserts the protocol round-trip, not any one file's content"). Shared `assertMCPToolUsed(t, workdir, result, toolName)` helper consolidates the six identical assertions (`ExitCode == 0`, `SessionID != ""`, assistant `tool_use` with `name == toolName` and non-empty `id`, matching user `tool_result` with `tool_use_id` equal and `len(content) > 0`, trailer `PermissionDenials` nil-or-empty); non-emptiness is sufficient because content text is server- and version-dependent (qmd snippets, figma file names, codegraph match shapes all vary). **Fourth consumer of `parseContentBlocks` + `contentBlock`** (#376 originated; #382 extended with `Content json.RawMessage` + `IsError` — both consumed here, the `Content` field is what makes the `tool_result` non-emptiness check correct per #382's lesson). **Fourth consumer of `parseResultTrailer`**. **Seventh consumer of `jsonlPathFor`**. **Third consumer of `truncate`**. Tight shared system prompt (`"You are an e2e regression-guard test. When asked to use an MCP tool, call exactly that tool once with the requested parameters, then briefly report the result."`) keeps the model on a single-call rail. `RunOpts`: `MaxTurns=3, Effort="low", Model="claude-haiku-4-5"`. Four real haiku calls per run (~$0.08 with cache warm, matching the AC estimate). `t.Parallel()` is NOT called — keeps cost predictable, avoids two concurrent `claude mcp list` invocations contending on the same MCP server stdio sockets. ~290 LoC, zero edits to `fixtures.go`, zero production-source changes. See [`codebase/384.md`](../codebase/384.md) for the design rationale.
- `resilience_test.go` (#382) — fifth consumer of the trio. **Protocol-resilience suite.** Four named top-level tests — one per production failure mode that pyry's supervisor must interpret as a structured signal: `TestRealClaude_BashTool_NonZeroExit` (Bash exits non-zero → tool_result with `is_error=true` + trailer `subtype="success"`), `TestRealClaude_PrematureStdinClose` (write user-turn envelope, close stdin, assert claude exits 0 within 60 s — the exact streamrunner production pattern), `TestRealClaude_MalformedStreamJSON` (garbage stdin → non-zero exit + parse-shaped stderr keyword), `TestRealClaude_LargePromptNearContextWindow` (~52 KB prompt → success-without-overflow-shaped-stop-reason OR non-success trailer with `ExitCode != 0`; "neither" is the failure mode). Each test pins a **structured exit signal** (exit code, JSONL event, or stderr substring) under a bounded timeout — a hang IS the failure mode being guarded against, so deadline expiry is itself an assertion target. **Two tests bypass pyry** (PrematureStdinClose, MalformedStreamJSON) and exercise `claude` directly via `exec.CommandContext` so the test can control raw stdin bytes and observe claude's untransformed exit shape; file-local helpers `resolveClaudeBin` (with the 2026-05-16 fork-bomb defense: refuse `PYRY_CLAUDE_BIN == os.Args[0]`), `directClaudeArgs`, `runClaudeDirect`, and `buildUserTurnEnvelope` own that surface — promoted to `fixtures.go` only if a third raw-exec test materialises. **Stderr predicate is OR-of-keywords** (`{"json","parse","input","format","envelope"}` case-insensitive), not a hard-coded phrase — pinning specific prose would couple the test to claude's diagnostic copy. Test 4's disjunction is structured as a single `if !branchA && !branchB { t.Fatalf(...) }` so the failure message lists both branches' requirements on "neither". Two-field extension to the `contentBlock` struct in `tool_loop_test.go` adds `Content json.RawMessage` (tool_result body lives in `content`, not `text`; can be string OR array of nested blocks per stream-json contract — `RawMessage` holds either shape) and `IsError bool` (`is_error` field on `tool_result` blocks, verified against 19 occurrences in `internal/agentrun/jsonl/testdata/*.jsonl`). Three real haiku calls per run (Test 3 rejects before reaching the model and incurs zero API cost), ~$0.08 total with cache warm. **`t.Parallel()` is NOT called** — matches the existing realclaude convention. See [`codebase/382.md`](../codebase/382.md) for the design rationale.
- `per_agent_test.go` (#381) — fourth consumer of the trio. **Per-dispatcher-role smoke suite.** Five named top-level tests (`TestRealClaude_PO_RoleLoop`, `TestRealClaude_Architect_RoleLoop`, `TestRealClaude_Developer_RoleLoop`, `TestRealClaude_CodeReview_RoleLoop`, `TestRealClaude_Documentation_RoleLoop`) — one per dispatcher role — exercising each role's `(allowedTools, system-prompt shape)` combination end-to-end. Motivated by the failure mode the prior tests don't cover: per-role drift (the 2026-05-14 `/doctor` bug failed all five roles together; the next failure of this class will likely hit one role only). Five separate top-level functions give five independent pass/fail signals on the nightly board; do NOT factor into a table loop. File-private `dispatcherBaseTools []string` mirrors `agents/dispatcher/src/dispatch.ts:1183` from the sibling `agent-dispatcher` repo (21 entries: Bash/Read/Write/Edit/Glob/Grep/TodoWrite + 4 qmd + 2 context7 + 7 codegraph; **no figma entries** — the dispatcher source-of-truth literal has none, despite the ticket-body parenthetical, see #381's lessons-learned). `dispatcherAllowedToolsForRole(role)` returns `dispatcherBaseTools` for po/developer/documentation and `dispatcherBaseTools + "Agent"` for architect/code-review, mirroring `dispatch.ts:1184-1186`'s `needsAgent` membership check; cite comments above both are the renumber-detection breadcrumbs. **Defensive `make+copy` on both branches** so a caller's slice mutation cannot corrupt the package-level constant. Unknown role panics (programmer error at the 5 call sites, not a runtime condition). System prompts are deliberate 1-2 line stand-ins (e.g. PO: "You are a Pyrycode product-owner agent. You refine issue bodies. Use Read/Edit/qmd/codegraph for research."), NOT verbatim copies of `agents/<role>/CLAUDE.md` — the goal is to exercise pyry's agent-run wiring with dispatcher-shaped inputs, not to validate operator prompts (which are large, frequently revised, and would make the suite both expensive and falsely sensitive to prompt-edit churn). User prompts are role-appropriate one-shot tasks ("Rewrite this one-line ticket title…", "Implement a Go function `Add(a, b int) int`…", "Summarize this one-line commit message…") that resolve in ≤4 turns with haiku/low and produce a single end-of-turn assistant text block (no tool_use); each explicitly constrains reply shape so haiku doesn't drift into exploratory tool-use. Shared `runRoleSmokeTest(t, role, systemPrompt, userPrompt)` body consolidates the six identical assertions: `ExitCode == 0`, `SessionID != ""`, `parseResultTrailer(result.Stdout)` succeeds, `PermissionDenials` empty (nil-or-zero-length, pointer-vs-empty distinction from #376 preserved), `NumTurns >= 1` (single-shot no-tool minimum; #376's `>= 2` is tool-loop specific), and the LAST `assistant`-kinded JSONL event has `EndOfTurn == true && TextChars > 0`. Shared helper is NOT a table loop — each `Test…_RoleLoop` is its own top-level function, so per-role failure attribution surfaces on the nightly board. Reuses the package-private `parseResultTrailer` + `resultTrailer` from #376 directly (second consumer; no fixture-export widening) and `jsonlPathFor` from #364 (fourth consumer). File-local `truncate([]byte) string` (1 KiB cap, suffixed `... (truncated)`) used in failure messages — matches the inline pattern from #376/#365 but is extracted file-locally because the five assertion blocks invoke it 2-3 times each. `RunOpts: MaxTurns=4, Effort="low", Model="claude-haiku-4-5"`, no `Timeout` override (5-minute default is the runaway guard, not the SLO). Production dispatch uses opus/high; this deliberate haiku/low downgrade is the cost/coverage trade — a future nightly opus suite is a separate file, not an additive flag. `t.Parallel()` is NOT called — matches the existing realclaude convention, keeps cost predictable, avoids API rate-limit interactions. ~$0.10 total per run (5 calls, cache warm); ~30 s per test, ~150 s aggregate wall time. See [`codebase/381.md`](../codebase/381.md) for the design rationale.
- `large_tool_output_test.go` (#423) — tenth consumer of the trio. **Large tool-output regression sensor (>64 KiB on one line).** One named test (`TestRealClaude_LargeToolOutput_ExceedsDefaultScannerBuffer`) drives a real claude session through a single Bash invocation producing ~80 KiB of stdout in one `tool_result` content block and pins four contracts: trailer reports `subtype="success"` + `stop_reason="end_turn"`, the on-disk JSONL `tool_result` content block exceeds 70 KiB (headroom under the ~80 KiB target), pyry's stdout-forwarded `tool_result` content block has **byte-equal length** to the on-disk twin (the regression sensor — the emitter is contracted to re-emit `ev.Raw` verbatim per `internal/agentrun/streamjson/emitter.go:149-150`, so any non-zero delta means a scanner truncated the forwarding path), and stderr does NOT contain `bufio.Scanner: token too long`. **Orthogonal to #421's long-session test**: that test fires when many short lines accumulate; this one fires when a single line on pyry's stream-json stdout exceeds the 64 KiB stdlib `bufio.Scanner` cap. Today only `permission_protocol_spike_test.go:133` extends a Scanner past the default; if that buffer extension is ever dropped — or if a similarly truncating scanner is wired into pyry's stream-json forwarding path — large tool output gets silently corrupted; this test fails. **Deterministic prompt** (`printf '%80000s' '' | tr ' ' 'A'` — exactly 80,000 literal `A` characters on a single line) preferred over `/dev/urandom` per the AC so any future fixture-snapshot work doesn't churn; the `"exactly as given"` wording in the system prompt is load-bearing because paraphrasing risks fewer bytes. `RunOpts: MaxTurns=2, Effort="low", Model="claude-haiku-4-5", AllowedTools=["Bash"]`. **Why a local scanner — not a call to `parseResultTrailer`**: the shared `parseResultTrailer` (`tool_loop_test.go:211`) uses the stdlib's 64 KiB default; with an 80 KiB `user`/`tool_result` line on stdout **before** the trailer line, the default scanner returns `Scan() == false` (no error) on the long line, exits the loop, and returns `"no type:result line in stdout"` — a false negative that would mask the very regression this test exists to catch. The new file's `findResultTrailer` walks pre-scanned 1 MiB-capped lines from `scanLargeStdoutLines` instead (canonical extension pattern: `scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)`, mirrors `permission_protocol_spike_test.go:133`). `scanner.Err()` is propagated so a future buffer-exhaustion regression (a line above 1 MiB) fails loudly rather than silently truncating. **Exact byte-length match, not a tolerance band** — the emitter writes `ev.Raw + '\n'` verbatim, so the disk-side and stdout-side bytes are byte-identical; comparing `len(stdoutContent) == len(jsonlContent)` is the strongest possible assertion and any non-zero delta IS the regression. **Invariant to `Content` shape**: claude may emit `tool_result.content` as either a bare JSON string OR a nested `[{type:"text",text:"..."}]` array; both shapes are byte-equal through `Emit`, so comparing raw `json.RawMessage` byte lengths between disk and stdout is shape-invariant — the test does NOT decode `Content` further. Four file-local helpers (`scanLargeStdoutLines`, `findResultTrailer`, `findBashToolResultBlock`, `findToolResultContentByID`, ~70 LoC combined) and two file-scope prompt constants (`largeToolOutputSystemPrompt`, `largeToolOutputUserPrompt`); helpers stay file-local until a second test needs the same shape, mirroring `resilience_test.go`'s precedent and #422's `spawnPyryAgentRun`/`processesInProcessGroup`. **Reuses unchanged** (no new fixture-package surface, no struct extensions): `WithWorktreeAuthenticated` (tenth consumer), `RunPyryAgentRun`/`RunOpts`/`RunResult`, `ReadJSONL`+`JSONLEntry`, `parseContentBlocks`+`contentBlock` (sixth consumer — `Content json.RawMessage` extended in #382 is the load-bearing field that makes the byte-length comparison work), `resultTrailer` (the local `findResultTrailer` decodes into it directly; the `parseResultTrailer` function itself is bypassed for the 64 KiB-cap reason above), `jsonlPathFor` (tenth consumer), `truncate` (sixth consumer). Failure-message discipline distinguishes "test setup wrong" (model paraphrased / refused — sharpen the system prompt, bump MaxTurns to 3) from "production broken" (the regression sensor named explicitly, with the emitter line cited so a future PR has to update both the assertion and the contract together). Forward-defensive stderr substring tripwire (`!bytes.Contains(result.Stderr, []byte("bufio.Scanner: token too long"))`) mirrors `long_session_test.go:135-139` — pins the literal stdlib error text verbatim (paraphrasing would silently disable it). One real haiku call per run, ~$0.02 (large `tool_result` inflates token count above peer haiku-low tests but is bounded). `t.Parallel()` is NOT called — matches the existing realclaude convention. ~252 LoC including the package-level comment block, two prompt constants, and inline rationale, zero edits to `fixtures.go` or any peer test file, zero production-source changes. See [`codebase/423.md`](../codebase/423.md) for the design rationale.
- `sigterm_mid_tool_use_test.go` (#422, re-specified twice by #1219) — ninth consumer of the trio. **SIGTERM-mid-tool_use cleanup regression guard.** One named test (`TestRealClaude_SigtermMidToolUse`) pins four production invariants when pyry receives SIGTERM with a Bash subprocess in flight: (1) full-subtree cleanup — no orphan claude or Bash descendant survives pyry's exit; (2) the on-disk session JSONL ends at a complete envelope boundary; (3) pyry exits within 5 s of SIGTERM (`internal/agentrun/streamrunner/runner.go:40`'s `killGrace` — the production contract, not a test-tuning budget: deadline expiry IS the regression); (4) the signal genuinely landed mid-tool_use — the precondition that makes 1–3 mean anything. **#1219 replaced the fixture command and invariant 4's discriminator twice** after claude's Bash-tool policy changed out from under both; see [`codebase/1219.md`](../codebase/1219.md) for the fragility history (`sleep 30` → refused by claude 2.1.158 → `tail -f /dev/null` → backgrounded-on-timeout by claude 2.1.220 → the current FIFO-owned shape) and the file's own header, which is the authoritative running record for any future defeat. **Current mechanics**: `holdFIFO` inverts ownership of the open window — the test `mkfifo`s a FIFO and blocks a goroutine on `open(O_WRONLY)`; claude's Bash call is `cat <fifo>`, which blocks in `read()` the instant the rendezvous with the test's open completes, and can only unblock via the test's own `t.Cleanup` release (the `*os.File` is never exposed to the test body). An event-driven wait (FIFO rendezvous → subprocess appears as a pyry descendant → Bash `tool_use` flushed to the on-disk JSONL) times the SIGTERM instead of a fixed sleep, so invariant 4's precondition holds regardless of claude version speed. Invariant 1's orphan check now walks claude's descendant process groups (claude runs Bash two levels below pyry, so `pgrep -g <pyry-pgid>` alone can't see it); invariant 4b's terminal-shape check is `classifyBashToolResult`, a four-way classifier (`toolResultAbsent` / `toolResultInterrupted` / `toolResultUnbounded` / `toolResultBounded`) that **rejects only on positive evidence claude's own bound ended the call**, read from two surfaces — `toolUseResult.backgroundTaskId`/`timedOutAfterMs` (claude's client-default timeout branch, #1223) and `input.timeout`/`run_in_background` (the model-chosen-bound branch, 2026-07-27) — because neither surface alone covers both of claude's bounding mechanisms. Every other shape accepts, including claude's teardown-interruption marker (`interruptedByShutdown: true`) and an ordinary non-zero-exit result claude writes when pyry's own reaper wins a teardown race against claude's signal handling (the 2026-07-29 defeat that flipped the polarity a second time — keying acceptance on the *absence* of a claude-internal flag accused pyry of doing its job correctly). A pre-SIGTERM JSONL snapshot backs the post-exit check with different fabric — a pure timing fact, no claude field — since a bounding defeat's `tool_result` is always written before the signal. `TestClassifyBashToolResult_ProbeEnvelopes` (credential-free, 11 fixture rows including verbatim probe captures from both the 2026-07-27 and 2026-07-29 live defeats) is the classifier's oracle and the AC2 gate. **Reuses unchanged**: `WithWorktreeAuthenticated` (ninth consumer), `ReadJSONL`, `ensurePyryBuilt`, `parseInitSessionID`, `parseContentBlocks`+`contentBlock`, `jsonlPathFor`, `truncate`. Same family as the 2026-05-16 fork-bomb incident (`ca8b688`, different shape, same cleanup-paths-leaking class). One real haiku call per run, ~$0.005 with cache warm. `t.Parallel()` is NOT called — matches the existing realclaude convention. Grew ~280 LoC → ~1500 LoC across the two reworks; zero production-source changes in any of the three. See [`codebase/422.md`](../codebase/422.md) for the original design and [`codebase/1219.md`](../codebase/1219.md) for the rework.
- `long_session_test.go` (#421) — eighth consumer of the trio. **Long-running session JSONL append integrity (≥10 turns).** Closes the long-turn-count gap left by the prior suite (peer tests cap at `MaxTurns ∈ {1,2,3,4}`): a regression in the multi-turn append path — Scanner buffer downgrade, off-by-one trailer write, trailing-newline drift, scanner reset across turn boundaries, buffer flush gap swallowing the last event — would not have been caught by any test that runs today. Single `TestRealClaude_LongSessionJSONLIntegrity` seeds the worktree with a 10-line `numbers.txt` (`"1\n2\n…\n10\n"`, `0o600`) and drives a real `claude` session through ten distinct single-command Bash operations against it (`wc -l`, `head -n 3`, `tail -n 3`, `sort`, `uniq`, `cat`, `grep 5`, `wc -c`, `awk '{s+=$1} END {print s}'`, `ls -l`) with an anti-chain steering paragraph reused from `budget_test.go`'s `maxTurnsSystemPrompt` shape (count bumped from 5 to 10, `error_max_turns` assertions removed). `RunOpts: MaxTurns=12, Effort="low", Model="claude-haiku-4-5", AllowedTools=["Bash"], Timeout=10 * time.Minute` — `MaxTurns=12` is deliberate 2-turn headroom above the ≥10 floor, `Timeout` bumped from the 5-minute `RunPyryAgentRun` default to absorb cold-network/queue tail latency without inflating the success-path budget on short tests. Six sequential assertions: `ExitCode == 0`, `SessionID != ""`, `parseResultTrailer` succeeds, `trailer.NumTurns >= 10` (failure message names the prompt-design recourse — "expand the prompt or strengthen anti-chain steering — do NOT lower the threshold" — at the point of failure so the next maintainer doesn't paper over the regression by lowering the constant), single-pass JSONL walk asserting `endOfTurnCount >= 10` on `e.Kind == "assistant"` entries AND that the last `assistant` entry satisfies `EndOfTurn && TextChars > 0`, and the **forward-defensive negative tripwire** `!bytes.Contains(result.Stderr, []byte("bufio.Scanner: token too long"))`. The Scanner tripwire pins the literal stdlib error text verbatim (paraphrasing would silently disable it) and is expected to pass trivially today — its job is to fire the day someone wires a stdlib `bufio.Scanner` into pyry's production stdout/stderr path WITHOUT bumping its buffer, hits a long claude line in this multi-turn run, and the regression would otherwise land silently; the failure message points the maintainer at the fix (`tool_loop_test.go:210` precedent: bump to 1 MiB). `ReadJSONL`'s underlying `jsonl.NewReader` has a 16 MiB cap (`internal/agentrun/jsonl/reader.go:27-30`), so the read path is structurally safe — the tripwire targets the OTHER stdout/stderr Scanner surface, not the read path. The user prompt ends with an explicit "After all ten results, summarize what you saw in one short sentence" — the summary tail nudges the model to emit one more `assistant`-kinded `end_turn` text block, giving the last-event-EndOfTurn assertion something to land on without depending on the model spontaneously producing a closing turn (without it, the run still completes successfully but the LAST assistant entry can be a `tool_use`-only message with `TextChars == 0`, failing the assertion on a non-regression). Reuses `WithWorktreeAuthenticated` + `RunPyryAgentRun` + `ReadJSONL` + `parseResultTrailer` + `jsonlPathFor` + `truncate` unchanged — zero new helpers, zero exported types, zero edits to `fixtures.go` or any peer test file. **Eighth consumer of `WithWorktreeAuthenticated`/fixture trio**, **sixth consumer of `parseResultTrailer`**, **eighth consumer of `jsonlPathFor`**, **fourth consumer of `truncate`**. One real haiku call per run, ~$0.05–$0.10 (higher per-test than peers because of the turn count, but bounded — same order of magnitude as `budget_test.go`'s cache-hit + max-turns pair). `t.Parallel()` is NOT called — matches the existing realclaude convention. The seeded-Bash variant was chosen over the text-only fallback ("list 10 facts about Helsinki, one per turn") because ten distinct shell commands give the model ten concrete, separable tasks (`wc -l` ≠ `head -n 3` ≠ `tail -n 3`) that resist collapsing into a single combined turn even under brevity pressure; the fallback stays documented as recourse if a future haiku revision collapses the Bash prompt. ~140 LoC including the two prompt constants and inline comments, zero production-source changes. See [`codebase/421.md`](../codebase/421.md) for the design rationale.
- `doctor_poisoning_regression_test.go` (#487) — eleventh consumer of the trio. **`/doctor` prompt-injection regression sensor.** One named test (`TestRealClaude_DoctorPoisoningRegression`) guards the contract that the per-spawn settings JSON pyry writes is one claude accepts at startup. A regression means claude rejected the JSON, prepopulated its `/doctor` repair template into the user input buffer, and processed THAT instead of pyry's prompt — the #487 failure mode that was alive across the [`ptyrunner`](ptyrunner-package.md) cutover (#470) because [`settings.WriteSettings`](agentrun-settings-subpackage.md) was emitting the invalid `permissions.defaultMode:"deny"` literal. Detector: walk JSONL events for the first `Kind == "user"` entry, decode its `Raw` into a content string (coercing both observed claude shapes via the file-local `decodeUserContent` helper: string literal AND `[{type:"text", text:"..."}]` array), assert the content does NOT contain the verbatim `/doctor` template opening substring `"Help me fix the issues reported by /doctor below."` (observed in the ticket's reproduction `out.jsonl`). Defence-in-depth: requires at least one `assistant` event in the JSONL (empty assistant set under a non-poisoned session indicates a different upstream failure that masks the regression-guard's signal — `t.Fatalf` with diagnostic context, not silent pass). **Malformed-line policy mirrors `bashInvokedInRaw`** at `allowed_tools_enforcement_test.go:74-76` — a JSONL line that fails to parse into the minimal `{Message: {Content: ...}}` shape is skipped silently; one malformed line must not turn a PASS into an inconclusive. `RunOpts: AllowedTools=["Read"], MaxTurns=1, Effort="low", Model="claude-haiku-4-5"`. Failure diagnostic includes the verbatim user-entry content (truncated to ~512 bytes with `"... (truncated)"` suffix), the JSONL path, and the operator-visible direction `"claude is rejecting the per-spawn settings JSON at startup — see #487"` — content is operator-supplied test data under a tempdir-pinned HOME so the dump is safe to print. Does NOT assert on `stop_reason: end_turn` (that AC item is satisfied by the manual reproduction in the ticket Context, not by the automated test — pinning it would couple the test to upstream model-behaviour detail beyond the contract being guarded). One real haiku call per run (~$0.01). **Reuses unchanged** (zero fixture-package surface changes): `WithWorktreeAuthenticated` (eleventh consumer), `RunPyryAgentRun`/`RunOpts`/`RunResult`, `ReadJSONL`+`JSONLEntry`, `jsonlPathFor` (eleventh consumer). Imports `encoding/json`, `strings`, `testing` — no internal package imports beyond the realclaude fixture surface. `t.Parallel()` is NOT called — matches the existing realclaude convention. **Post-mortem note**: `ptyrunner_byte_equivalence_test.go` (#482) should have caught the invalid literal but didn't — its `WithWorktreeAuthenticated` gate silently skipped on Max-only environments (no `ANTHROPIC_API_KEY`); the fixture-wide auth-skip cleanup to recognise Max-plan credentials is the architectural follow-up. ~149 LoC including the package-level comment block, zero edits to `fixtures.go` or any peer test file, zero production-source changes (the production fix is the single-literal flip in `internal/agentrun/settings/settings.go:72`). See [`codebase/487.md`](../codebase/487.md) for the design rationale.
- `budget_test.go` (#385) — seventh consumer of the trio. **Budget guardrails.** Two top-level tests pinning cost-relevant guarantees the suite did not previously exercise end-to-end. `TestRealClaude_CacheHitWarmsAcrossRuns` runs the same `RunOpts` skeleton twice through `RunPyryAgentRun` against `WithWorktreeAuthenticated(t)` and asserts the second invocation's trailer reports `Usage.CacheReadInputTokens > 0` (primary, `t.Fatalf`) — pinning Anthropic prompt-cache alignment within the 1-hour TTL when (system-prompt, allowed-tools, model, effort) are identical. A regression that breaks cache-key alignment (dynamic content in the system prompt, per-invocation tool-list churn) will show `== 0` here. A diagnostic-only check on the first run's `CacheCreationInputTokens > 0` uses `t.Errorf` (not `t.Fatalf`) so a sub-threshold system prompt surfaces as a soft signal rather than masking the primary on a passing run; the interpretation matrix (0+pass vs. 0+0) is documented in a comment above the check. `cacheHitSystemPrompt` is a deterministic 5-sentence string concatenation (~300 tokens) sized to clear Haiku 4.5's ~2048-token implicit-cache minimum by margin; the doc comment names the "no dynamic content" constraint (date, run id) explicitly because that is the regression class the test catches. `TestRealClaude_MaxTurnsHonored` runs `pyry agent-run --max-turns=2` against a prompt that natural-completion would require ≥5 turns (numbered 5-line Bash sequence with explicit "do NOT combine" guidance) and asserts five fields on the trailer: `Subtype == "error_max_turns"`, `TerminalReason == "max_turns"`, `NumTurns == 2` (exact — off-by-one fires here), `StopReason != "end_turn"`, `IsError == true`. **`ExitCode == 0` is correct** — `pyry agent-run` exits 0 on a successfully-emitted result trailer regardless of trailer `is_error`; the budget-exhaustion signal lives in the trailer fields, not the subprocess exit code, and a code comment pins this so a future maintainer doesn't "fix" the assertion to `!= 0`. Tool-call-collapse risk pinned in a comment above `maxTurnsPrompt`: if a future haiku revision is smart enough to fire all five `echo`s in one tool_use block (or otherwise complete in ≤2 turns naturally), the assertions fail loudly and the right fix is to bump the prompt to force more turns (e.g. 8 sequential `read X.txt` calls), not to weaken the assertion. Three-field extension to the `resultTrailer` struct in `tool_loop_test.go` adds `IsError bool`, `TerminalReason string`, and `Usage resultTrailerUsage` (plus the new `resultTrailerUsage` sub-struct emitting all four token-count fields); all `omitempty`-tagged so pre-#385 consumers (#376/#381/#382/#384) decode unchanged. **Fifth consumer of `parseResultTrailer`**. File-local `truncateStdout([]byte) string` mirrors the inline pattern from `tool_loop_test.go:127` (the existing file-local `truncate` from #381 stays untouched because the spec forbids touching `fixtures.go` and the existing helper is its own file's private). `RunOpts`: cache-hit uses `MaxTurns=1, AllowedTools=["Read"]`; max-turns uses `MaxTurns=2, AllowedTools=["Bash"]`; both `Effort="low", Model="claude-haiku-4-5"`. Three real haiku calls per run (2+1), ~$0.04 total with cache warm, matching the AC estimate. `t.Parallel()` is NOT called — matches the existing realclaude convention; also load-bearing on the cache-hit test where concurrent runs would muddy the "cache warmed by run 1 specifically" diagnostic. ~199 LoC, zero edits to `fixtures.go`, zero production-source changes. See [`codebase/385.md`](../codebase/385.md) for the design rationale.

- `interactive_bootstrap_liveness_test.go` (#854) — **not a `RunPyryAgentRun` trio
  consumer** (the only file in the suite that isn't): it drives the **daemon's
  interactive relay path**, not `pyry agent-run`. `TestInteractiveBootstrapLiveness`
  spawns a fresh real `pyry` daemon (real `claude --model haiku`) in an isolated
  authenticated HOME, seeds a deterministic bootstrap pool id + bound conversation
  (`seedBootstrapRegistry`/`seedBoundConversation`, transcribed from
  `internal/e2e/harness.go` per #861), pairs a headless phone over the encrypted
  Noise_IK v2 wire, and drives **two** turns — Turn 1 proves the bootstrap child
  creates its transcript and the reply bridge binds, Turn 2 proves the bridge
  survives past the first turn (the rotation/offset path). Each turn asserts only
  liveness (a non-empty streamed `assistant_delta` within a generous timeout;
  never claude's words). This is the RED/GREEN oracle for the #854 fix: on
  pre-fix `main` the bootstrap-bound reply resolves via a by-id resolver keyed on
  a pool id that never has a matching on-disk transcript, so Turn 1 times out.
  The daemon spawn routes its control socket through a transcribed
  `shortSocketPath` (the #860 `sun_path`-limit fix) — load-bearing for RED, since
  a `<home>/pyry.sock` under the long authenticated `t.TempDir()` HOME would
  overflow macOS's 104-byte limit and the daemon would never reach readiness,
  masking the deadlock. Known non-blocking gap (code review SHOULD FIX, not yet
  addressed): the drain correlates a turn by conversation id + non-empty text,
  not by `AssistantDeltaPayload.TurnID` — see [`codebase/854.md`](../codebase/854.md)
  for the failure scenario and the fix shape. Zero edits to `fixtures.go`. See
  [`codebase/854.md`](../codebase/854.md) for the production-fix half (which
  lives in `cmd/pyry`, not this package).

- `interactive_per_conversation_liveness_test.go` (#997) — sibling of #854,
  same interactive-relay shape but drives `create_conversation` over the wire
  first (`startPerConversationHarness`, `createConversationViaPhone`) rather
  than seeding a bound conversation, then proves liveness on the freshly
  created conversation. Exports the harness (`startPerConversationHarness`,
  `createConversationViaPhone`, `sealEnvelope`, `drainForReply`) that #1028
  below reuses for its verb-drive spine.

- `interactive_conversation_lifecycle_test.go` (#1028) — first real-`claude`
  coverage of the **conversation-management verbs**, not just liveness.
  `TestInteractiveConversationLifecycle` drives create → rename → archive →
  unarchive → delete on ONE conversation over the same encrypted channel
  against a freshly-spawned daemon on real `claude --model haiku`, with a
  `send_message` liveness turn inserted **between unarchive and delete**
  (deliberate: the metadata verbs then run on a quiescent wire, and proving
  liveness after the archive round-trip is the stronger claim that the live
  session survived it). Each verb's effect is asserted from an
  operator-observable signal — the `conversation_updated` reply's
  `Name`/`IsArchived` fields, and for delete both the `conversation_deleted`
  reply id and the on-disk registry no longer holding the row (new
  `readConversationIDsOnDisk` helper, ~25 lines). Test-only: reuses the #997
  harness (`startPerConversationHarness`, `createConversationViaPhone`,
  `sealEnvelope`, `drainForReply`) and the #854 turn drive/drain
  (`sealSendMessage`, `drainForAssistantReply`) verbatim; zero production
  files touched. The fake tier (`relay_v2_rename_test.go` #974,
  `relay_v2_delete_test.go` #975, `relay_v2_archive_test.go` #976) owns the
  verbs' detailed shape — this test is liveness/observable-state shaped only,
  proving the real interactive stack executes the verbs at all. See
  [`codebase/1028.md`](../codebase/1028.md).

- `interactive_modal_resolution_test.go` (#1030) — first real-`claude`
  coverage of the **modal-resolution verbs**, and the hardest of the #963
  families: the trigger is not a test env-var but real claude choosing to
  call a gated tool under default permission mode. `TestInteractiveModalResolution`
  drives one daemon spawned via the new `spawnPermissionDaemon` (byte-identical
  to `spawnBootstrapDaemon` #854 minus `--dangerously-skip-permissions` — that
  one omission is the entire trigger) through two sequential real-permission-modal
  cycles over one encrypted channel: **Phase A (answer)** sends a Bash-triggering
  prompt, drains to `modal_shown{Class:"permission"}` (asserted before
  resolving — non-vacuity), sends `modal_answer{allow_once}`, then proves the
  session proceeded via a subsequent non-empty `assistant_delta`; **Phase B
  (cancel)** raises a second modal and sends `modal_cancel`, observing the
  `modal_dismissed{cancelled,remote}` **broadcast** (`modal_cancel` is
  fire-and-broadcast — no reply is correlated to the cancel request). The new
  `drainForControlEvent` helper is a Type-only broadcast-drain sibling of
  #1028's `InReplyTo`-correlated `drainForReply`. The phone pairs WITH
  `--allow-remote-permissions` (the answer-side device gate). Reuses the
  #854/#997/#1028 harness (`bootstrapDaemon` plumbing, `driveHandshakeInteractive`,
  `sealSendMessage`, `sealEnvelope`, `drainForAssistantReply`) unchanged; zero
  production files touched. The fake tier (`relay_v2_modal_answer_test.go`
  #791, `relay_v2_modal_cancel_test.go` #1003, trust-class #993) owns the
  verbs' detailed shape and keystroke fidelity — this test is
  liveness/observable-state shaped only, proving the real interactive stack
  raises and resolves an actual permission modal at all. The `#798` daemon
  modal surfacer (see [modalbridge-package.md § Live daemon wiring (#798)](modalbridge-package.md#live-daemon-wiring-798))
  needed zero production changes. See [`codebase/1030.md`](../codebase/1030.md).

- `interactive_session_control_liveness_test.go` (#1031) — first real-`claude`
  coverage of the **session-control respawn verbs**, and the last of the #963
  families: `new_session` (rotate via `/clear`) and `set_session_settings`
  (respawn via a live restart) both tear down and re-establish the live
  claude child, so the operator-facing risk is the reply bridge failing to
  rebind past the respawn — the same failure class #854 guards on a cold
  bootstrap session. `TestInteractiveSessionControlLiveness` drives a
  sequential spine (the #1028 shape) on one daemon / one seeded bound
  conversation over one encrypted channel: turn 1 (binds the reply bridge +
  gives claude a transcript) → `new_session` rotate → turn 2 (proves the
  rebind past rotation) → `set_session_settings{Model:"haiku"}` respawn →
  turn 3 (proves the rebind past the restart). Each control verb pairs its
  liveness send with a deterministic on-disk `sessions.json` bootstrap-row
  anchor so the assertion is non-vacuous — a silently no-op'd respawn would
  answer from the un-rotated/un-restarted session and pass otherwise: the
  `new_session` rotate is a bounded ~1 s-cadence re-send loop (mirrors
  #1004) asserted against a pre-frame baseline id, and
  `set_session_settings` is asserted against the persisted `Model` field
  (mirrors #1005, matched by `Bootstrap==true` so it's robust to the
  restart itself rotating the id). `Model: "haiku"` is the credential-safe
  settings value — `claudeSettingsArgs` appends `--model haiku` after the
  daemon's own base `--model haiku` (last-wins), so the respawn never risks
  a different real model's credentials/rate limits in a pre-ship gate, while
  `"" → "haiku"` is still a genuine on-disk change that triggers a real
  restart. Real claude rotates its transcript on **every** `/clear` (unlike
  fakeclaude's one-shot `PYRY_FAKE_CLAUDE_CLEAR_ROTATES`), so the new
  `waitBootstrapIDSettled` helper waits for the bootstrap id to stop
  changing before each post-control `send_message` — otherwise a straggler
  rotation from a re-sent frame could tear down that turn's in-flight
  claude mid-stream. Reuses the #854/#1028 harness
  (`spawnBootstrapDaemon`, `driveHandshakeInteractive`, `sealSendMessage`,
  `drainForAssistantReply`) and the #997 generalised control-frame helpers
  (`sealEnvelope`, `drainForReply`) unchanged; zero production files
  touched. The only new code is the `bootstrapRow`/`readBootstrapRow`/
  `waitBootstrapID`/`waitBootstrapIDSettled`/`waitBootstrapModel` on-disk
  `sessions.json` reader family (mirrors #1028's
  `readConversationIDsOnDisk` and #1005's `settingsRow`/
  `readBootstrapSettings`). The fake tier (`relay_v2_new_session_test.go`
  #1004, `relay_v2_settings_test.go` #1005) owns the verbs' detailed shape
  and the reject-invalid path (out of scope here) — this test is
  liveness/observable-state shaped only, proving the real interactive stack
  survives both respawns. Last child of #963. See
  [`codebase/1031.md`](../codebase/1031.md).

- `interactive_stream_liveness_test.go` (#1153) — first real-`claude` coverage
  of the **stream-json interactive runner** (`interactive_runner:
  "stream-json"`, #1081); every prior interactive-relay real-claude test
  (#854/#997/#1028/#1030/#1031) runs under the default PTY runner.
  `TestInteractiveStreamLiveness` transcribes #854's daemon body (pair → seed
  bootstrap registry + bound conversation → spawn → handshake) with three
  deltas: the new `writeStreamInteractiveConfig(t, home)` helper flips the
  production config toggle (writes `<home>/.pyry/config.json =
  {"interactive_runner":"stream-json"}` before spawn — `resolveConfigPath`
  reads it once at startup) before `spawnBootstrapDaemon`; it drives **one**
  turn (AC parity with #1141) instead of #854's two; and it drains to
  completion via the new `drainForCompletedTurn` helper — the two-milestone
  drain #1141's fake-side spec requires (non-empty `assistant_delta` **then**
  terminal `turn_state{idle}`), where #854's `drainForAssistantReply` stops
  at the first delta. No content/echo assertion on M1 (real claude's words
  are non-deterministic, unlike #1141's fakeclaude echo). The config-writer
  is deliberately a standalone helper, not folded into a spawn wrapper, so
  the permission-flow rider #1154 (blocked-by this ticket) can compose it
  with `spawnPermissionDaemon` instead. Fresh package-private seed constants
  (`streamBootstrapUUID`/`streamConvID`) — #854's `liveBootstrapUUID`/
  `liveConvID` are file-private and would redeclare in the same package/tag
  namespace. Zero production files touched. See
  [`codebase/1153.md`](../codebase/1153.md).

- `interactive_stream_modal_resolution_test.go` (#1154) — the stream-json
  **sibling** of #1030's `TestInteractiveModalResolution`, not a replacement:
  #1030 proves the same answer round-trip under the default PTY runner; this
  is the desktop#483 scenario on the real stream stack (PTY-red vs.
  stream-green). `TestInteractiveStreamModalResolution` is #1030 Phase A
  (answer-only — Phase B cancel is out of scope) composed with #1153's
  stream-json seams: `startStreamModalResolutionHarness` is an inline copy of
  #1030's `startModalResolutionHarness` with exactly one inserted line,
  `writeStreamInteractiveConfig(t, home)` before `spawnPermissionDaemon` (the
  stream-vs-PTY differentiator), plus fresh distinct seed constants
  (`streamModalBootstrapUUID`/`streamModalConvID`, valid UUIDv4 shape, no
  redeclaration vs. #1030's or #1153's names). `raiseRealPermissionModal`
  (#1030, reused verbatim) still owns the AC1/AC2 non-vacuity gate —
  `modal_shown{Class:"permission"}` + non-empty `ModalID` asserted before the
  answer is sealed — and the turn-completion assertion upgrades from #1030's
  `drainForAssistantReply` (M1 only) to #1153's `drainForCompletedTurn` (M1
  non-empty `assistant_delta` **then** M2 terminal `turn_state{idle}`), so
  "modal answered but the turn never resumed" fails loud rather than greening
  vacuously. Zero production files touched; inlining over parameterizing
  #1030's shipped harness follows the #1153 precedent (avoids a file-overlap
  edit to another ticket's test file). Split from #1083, blocked-by #1153.
  See [`codebase/1154.md`](../codebase/1154.md).

- `interactive_stream_running_turn_test.go` (#1172) — reusable trigger infra
  that holds a live claude turn in `turn_state{responding}` for a bounded
  window, plus the smoke (`TestInteractiveStreamRunningTurn`) that proves it.
  Ports the desktop `e1fe219` fix (2026-07-17): a silent `sleep` gets
  backgrounded by claude (turn ends early), a chatty per-iteration loop
  floods the frame stream (delays `turn_state` delivery) — the working
  approach drives the turn with a bounded, silent, foreground shell loop
  inside one Bash-tool call. Load-bearing mechanism: the turn emitter
  (`cmd/pyry/interactive_turn_v2.go`) fires `responding` at `ToolStart`
  (before the command runs) and `idle` only once at `TurnEnd`, so a turn
  running an `L`-second loop holds `responding` for all of `L` — observing
  `responding` then verifying no `idle` for `hold < L` is the running-turn
  proof, and it fails loud on an early `idle` rather than passing silently.
  `startStreamRunningTurnHarness` transcribes #1153's setup verbatim (pairs
  **without** `--allow-remote-permissions`, spawns via `spawnBootstrapDaemon`
  so no permission modal blocks the Bash call — opposite posture from the
  modal specs #1030/#1154). No content/echo assertion — real claude's output
  is non-deterministic; only `turn_state` transitions and elapsed time are
  asserted. `startStreamRunningTurnHarness` + `driveRunningTurn` +
  `drainForResponding` are the reusable seam #1176 (interrupt, already
  natively `blocked-by` this ticket) composes. Zero production files
  touched. See [`codebase/1172.md`](../codebase/1172.md).

- `interactive_stream_multiturn_continuity_test.go` (#1173) — the first
  real-`claude` coverage of **multi-turn continuity** on the stream-json
  runner; every prior stream spec (#1153/#1154/#1172) drives exactly one
  turn, so none proves the runner's core purpose — holding one live
  `claude` child's stdin open across many turns with context intact.
  `TestInteractiveStreamMultiTurnContinuity` transcribes #1153's setup
  verbatim, then drives a **3-entry turn plan** strictly sequentially over
  one held-open session instead of a single send: (1) **plant** — claude is
  told to remember a per-run-unique `PYRY<hex nonce>` token; (2)
  **filler** — an intervening turn with no bearing on the token, load-bearing
  because a bare 2-turn plant→recall wouldn't prove a turn ran *between* them
  without respawn; (3) **recall** — asks for the token back. Continuity is
  asserted by content — `strings.Contains(strings.ToUpper(recallText),
  token)` — rather than pid inspection (the harness exposes no child pid, and
  a memory-less respawned child cannot produce the token, so content memory
  is the stronger "no respawn" observable). New helper
  `drainForCompletedTurnText` is a text-capturing superset of #1153's
  `drainForCompletedTurn`: byte-identical M1/M2 milestone semantics, plus
  accumulating every matching `assistant_delta.Text` into the return value
  instead of stopping at the first delta — kept as a separate helper rather
  than parameterizing the shared drain, since editing the shared one would
  touch the two already-merged sibling call sites (#1153, #1154). Fresh seed
  constants (`streamMultiTurnBootstrapUUID`/`streamMultiTurnConvID`). Zero
  production files touched. Split from #1083; siblings #1174 (new-session
  rotation) and #1175 (permission DENY) are out of scope here. See
  [`codebase/1173.md`](../codebase/1173.md).

- `interactive_stream_interrupt_test.go` (#1176) — the real-`claude`
  counterpart of the fakeclaude interrupt proof
  (`TestRelayV2_StreamInterruptStopsRunningTurn`, #1136), closing the
  fake-green/real-red gap (#949) on the interrupt path.
  `TestInteractiveStreamInterruptStopsRunningTurn` composes #1172's seam
  verbatim (`startStreamRunningTurnHarness` + `driveRunningTurn` +
  `drainForResponding`) to put a genuinely-running live turn in flight, sends
  a payload-less `TypeInterrupt` envelope (routes via the active cursor →
  `resolveBoundRunner` → the running turn's bound runner), and asserts the
  turn stops **cancelled** via a new drain, `drainForCancelledTurnEnd` — its
  vacuous-pass guard is the reason it exists as its own helper rather than a
  generic type-targeted drain: the first `turn_end` for the conversation must
  carry `StopReason == "cancelled"`, since the 40s running-turn loop *will*
  complete naturally (`"end_turn"`) if the interrupt no-ops. A trivial fourth
  turn drained via #1153's `drainForCompletedTurn` proves the session stays
  usable afterwards. Deliberately bootstrap-bound rather than minting a
  second conversation — AC only requires the interrupt reach the *running
  turn's* bound runner (proven here), not cross-conversation isolation
  (unit-owned deterministically by #1121). No new package-level constants,
  zero production files touched. Split from #1083. See
  [`codebase/1176.md`](../codebase/1176.md).

- `interactive_stream_resume_after_eviction_test.go` (#1177) — the
  real-`claude` proof that an idle-evicted **stream** session resumes via
  `--resume` with prior context intact, closing the last uncovered rung of
  the streamrunner plan's "restart after eviction" risk. Idle-evict +
  respawn was covered only against fakeclaude and only on the PTY/bootstrap
  runner (`TestE2E_IdleEviction_RespawnsOnSendMessage`, #396); the stream
  path's `TestE2E_PerConversation_IdleEvictsAndReactivates` (#680)
  explicitly deferred content-recall to "realclaude's domain" — this is that
  deferred work (see [idle-eviction.md § Testing](idle-eviction.md#testing)).
  `TestInteractiveStreamResumeAfterEviction` transcribes #1153's setup, then:
  plants a per-run-unique token in a turn drained via #1153's
  `drainForCompletedTurn` (the sync point guaranteeing the token committed
  before eviction); polls the daemon's stderr for the
  `session.idle_eviction` WARN via the new `waitForIdleEvictionWARN` — the
  non-vacuity gate proving eviction happened *before* the resume turn is
  sent; drives a second turn via the new `drainForResumedTurnText` (a fork
  of `drainForCompletedTurn` that accumulates delta text instead of stopping
  at the first); and asserts the reply recalls the token
  (`strings.Contains(strings.ToUpper(...))`) — a forked fresh spawn has no
  memory of it, so this is the discriminator. First stream spec to *enable*
  the idle timer (`-pyry-idle-timeout=30s` via the new
  `spawnBootstrapDaemonWithIdle`, a self-contained near-copy of
  `spawnBootstrapDaemon` keeping zero shared-file merge surface with
  siblings #1173–#1176); every prior spec disables it. Standing coupling
  constraint documented in-file: the idle timer arms once at activation and
  never resets per-turn, so the 30s window must exceed plant-turn
  completion or the plant drain REDs loudly. `waitForIdleEvictionWARN` pins
  the WARN's `session_id` **value** (stronger than #396's key-only pin),
  sound because the stream path's `--session-id`-first `buildArgs` never
  forks, so the pool id equals the on-disk transcript stem across
  `--resume`. Ticket-encoded fixed UUIDs (single-char-repeat stems
  exhausted by prior siblings). Zero production files touched. Split from
  #1083. See [`codebase/1177.md`](../codebase/1177.md).
- `interactive_stream_permission_deny_test.go` (#1175) — the security-relevant
  **deny** half of the remote permission round-trip on the stream-json runner
  (security-sensitive; architect security-review verdict PASS). #1154 proved
  allow on this stack; a fail-open regression (denied tool executes anyway)
  or a hang on the denied modal is exactly the real-claude-specific failure
  the fake tier (#1139) cannot surface.
  `TestInteractiveStreamPermissionDeny` reuses #1154's
  `startStreamModalResolutionHarness` verbatim (no new harness, no new seeded
  UUIDs) and `raiseRealPermissionModal`/`writeFileTrigger` (#1030), swaps the
  answer to `reject_once`, and adds two checks: a `modal_dismissed` drain
  asserting `Source == "remote"` + `Outcome == "reject_once"` (attribution —
  closed vocabulary rules out a timeout-deny or dropped answer masquerading
  as the explicit reject) and a workdir walk,
  `requireTriggerFileAbsent`, proving the gated `Write`'s target file never
  materialised. The retry-answering helper `denyModalsUntilIdle` (rework
  after an operator live-gate FAIL surfaced that real haiku retries a denied
  tool at least once) rejects every permission modal the turn raises until
  terminal `turn_state{idle}`, bounded by a retry-count cap
  (`maxRetryDenies`) and `perTurnReplyBudget` wall-clock, each with a distinct
  diagnostic. No content/echo assertion. Zero production files touched. Split
  from #1083. See [`codebase/1175.md`](../codebase/1175.md).
- `interactive_stream_new_session_test.go` (#1174) — real-claude cross of
  the fakeclaude sibling #1137: on the stream-json runner, `new_session`
  rotates the bootstrap session id AND `streamsup.Runner.RestartFresh`
  spawns a genuinely fresh live `claude` child under the rotated id, not a
  `--resume`. Five milestones: M1 turn-1 liveness (#1153's
  `drainForCompletedTurn`), M2 on-disk id rotation (#1031's actuation
  loop), M3 client-observed `session_transition{clear}` (#1154's
  `drainForControlEvent`), M4 turn-2 accepted (**ack only** —  a
  phone-side delta would hang, the drain gate's sink tag is fixed at
  runner construction and drops post-rotation deltas, #1081 out of
  scope), M5 a fresh `<idAfter>.jsonl` transcript appears alongside the
  untouched `<idBefore>.jsonl` (the word-independent, fake-unregressable
  fresh-spawn proof; transcript dir located empirically to sidestep the
  #989 `canonicalCase` hazard). Zero production files touched. Split from
  #1083; sibling leaves #1173 (multi-turn continuity), #1175 (permission
  DENY). See [`codebase/1174.md`](../codebase/1174.md).
- `background_trigger_probe_test.go` (#1223) — **evidence probe, not a
  regression gate**; opt-in behind `PYRY_PROBE_BACKGROUND_TRIGGER=1` on top of
  the package's normal auth skip (an ungated probe would burn ~9 live claude
  turns on every `make preship`). Settles which environment lever
  deterministically makes claude return a background handle under `pyry
  agent-run`, so #1224–#1227 can be specified against a known trigger instead
  of the model's discretion. Mechanism: the test `mkfifo`s a FIFO and holds
  the write end open in a goroutine (`holdProbeFIFO`, release only in
  `t.Cleanup`, never exposing the `*os.File`); claude's `cat <fifo>` Bash call
  blocks on the read end and cannot complete on its own, so a matching
  `tool_result` observed while the write end is held is a **structural**
  signal that claude ended the call itself — not a match against claude's
  result prose (the treadmill #563 and #1219 each paid for once). The same
  property removes the timing race from the `ps -axo pid=,ppid=,pgid=`
  snapshot: it is taken synchronously at the observation point, with liveness
  self-evidenced via a `cmd.Wait` channel rather than `Signal(0)` (which
  reports an unreaped zombie as alive). Row-table design over
  `BASH_DEFAULT_TIMEOUT_MS` / `BASH_MAX_TIMEOUT_MS` /
  `CLAUDE_CODE_DISABLE_BACKGROUND_TASKS` / model-set `run_in_background`, each
  a `t.Run` subtest with its own `t.TempDir()`/`t.Setenv`/FIFO. **Result:**
  `BASH_DEFAULT_TIMEOUT_MS` set low is the deterministic trigger (7/7 firing
  reps across all rows that carry it); `BASH_MAX_TIMEOUT_MS` alone does not
  fire (it caps what the model may set, and the model set no `timeout` in any
  rep); `toolUseResult.timedOutAfterMs` is the only surface that discriminates
  the timeout-expiry path from the model-set `run_in_background` path — the
  process tree is byte-identical between them. Four credential-free
  self-checks (FIFO hold/release, `ps`-parse, `input.timeout` projection) run
  ungated. Zero production files touched. Kept (not deleted) because
  #1224–#1227 all have to stage this same scenario. See
  [`codebase/1223.md`](../codebase/1223.md) for the full lever table, the
  live-run evidence, and two known gaps flagged by code review (SHOULD FIX,
  not blocking) in the Bash-call selection and the env-arrival control's
  absent/unread collapse.
- `fifo_reader_liveness_test.go` (#1239) — **offline instrument, not a probe**;
  no auth skip, no env gate, no live claude. Answers "is some process
  currently holding this FIFO's read end?" with no pid and no `ps`, closing a
  gap `holdProbeFIFO` alone leaves open: holding the write end proves a
  command could not have *finished*, not that it is still *alive* — a killed
  command leaves the same held write end. `fifoLiveRead(path)` returns a
  three-valued `fifoLiveOutcome` (`reader-present` / `no-reader` /
  `instrument-failed`, never a bare boolean) via `Lstat` → positive
  `os.ModeNamedPipe` allowlist gate → `open(path, O_WRONLY|O_NONBLOCK)`
  (success = reader present, `ENXIO` = no reader, everything else =
  instrument failure). The allowlist gate is what closes the inverting
  failure on the success arm: a bare open on `/dev/null` or a regular file
  succeeds with no reader anywhere, which a regular-file blacklist would
  misread as "reader present." Four offline self-checks prove: the read
  flips on one FIFO across one reader's lifetime (`Kill()` alone does not
  flip it — `Wait()`/reap does), the mode gate rejects every non-FIFO path
  including `/dev/null`, every open errno except `ENXIO` yields
  instrument-failed, and repeated reads don't perturb a blocked reader.
  Zero production files touched. #1240 is natively blocked on this file and
  calls `fifoLiveRead` at the instant it records `turn_state{idle}`. See
  [`codebase/1239.md`](../codebase/1239.md) for the implementation detail and
  two known gaps flagged by code review (SHOULD FIX, not blocking) in the
  `Lstat`-arm errno assertion coverage and a stuttering `Detail` string.
- `interactive_background_idle_probe_test.go` (#1240) — **evidence probe,
  security-sensitive**; opt-in behind `PYRY_PROBE_INTERACTIVE_BG_IDLE=1` on
  top of the package's normal auth skip. Stages one turn on the production
  stream-json interactive daemon around a Bash command claude backgrounds on
  timeout expiry (#1223's `BASH_DEFAULT_TIMEOUT_MS` trigger, reused unedited),
  records every frame the phone receives in receive order via the new
  `bgIdleRecordTurn` (the genuinely new code — both existing drains in this
  file discard frames, so neither was reusable as a recorder), and takes
  `fifoLiveRead` (#1239) twice on the same FIFO path across one command's
  lifetime — pre-rendezvous (must read `no-reader`) and at the instant `idle`
  is recorded — to re-prove the liveness flip in this rig rather than
  inheriting #1239's self-check. Attribution of the held FIFO to the
  backgrounded `tool_use` is closed by counting recorded frames
  (`bgIdleCountFIFONaming`, 201-rune truncation tell), not a third pid
  matcher. One extraction allowlist (`bgIdleFrameFromEnvelope`, five named
  arms + a default that can hold nothing but `conversation_id`) keeps
  claude's verbatim `unrecognized_message.Raw` and `assistant_delta.text` out
  of the published artefact by construction. **Result, run live 2026-07-30
  (3 reps, claude 2.1.220): yes — `turn_state{idle}` is emitted while the
  backgrounded command is still alive**, and `turn_end.stop_reason` is
  byte-identical (`"end_turn"`) between that case and a genuine finish, so a
  client has no field to key on. Side finding: an `unrecognized_message`
  frame appeared in all 3 reps specifically on the backgrounding path, filed
  separately. Zero production files touched. Split from #1227; blocks #1241
  (client-distinguishability baseline diff), which inherits this recorder.
  See [`codebase/1240.md`](../codebase/1240.md) for the full finding, two
  known gaps flagged by code review (SHOULD FIX, not blocking) in claude
  version attribution and anomaly-flag verdict gating, and the live-run
  timeline.
- `background_reach_probe_test.go` (#1230) — **evidence probe, not a regression
  gate**; opt-in behind `PYRY_PROBE_BACKGROUND_REACH=1`, reusing #1223's staging
  rig verbatim (`background_trigger_probe_test.go` not edited; every new symbol
  `reach`-prefixed against the concurrent `feature/1219` branch and sibling
  #1231). Answers the predictive half of "does a backgrounded Bash command
  outlive pyry": is it still a transitive child of claude's pid inside
  `agentrun.ReapDescendantGroups`'s descendant-BFS reach, and would its process
  group survive the reaper's three exclusions (`reap.go:52`) — one during-turn
  snapshot, no teardown. **Content-first identification, not subtree-first**:
  one full-table `ps -axww -o pid=,ppid=,pgid=,command=` matched in Go against
  the run's FIFO path and session UUID across the whole process table — the
  read #1223's subtree-first, base-name-only `probeAnnotateCommands` cannot
  perform, and the one that could actually catch a re-parented survivor. Root
  pinned content-first via `--session-id <uuid>` in claude's argv (the ptyrunner
  path only), checked for agreement against the rig's positional
  `probeWaitForDirectChild` guess rather than trusted on its own. Two
  reachability reads off one integer snapshot — `reachChainUp` walking ppid
  links up, `probeDescendantsFromPS` (#1223's, unedited) BFS-ing down —
  disagreement recorded as an instrument fault, never a finding. Three-valued
  match outcome (`matched` / `trigger-never-fired` / `fired-no-row-matched`),
  established before any reachability claim is made. **Result (live run,
  2026-07-30, claude 2.1.220):** the backgrounded `cat`/`zsh -c` pair IS
  reachable from claude's pid, two hops down, and the zsh wrapper's process
  group survives all three exclusions — the reaper *would* target it; whether
  it actually dies is #1231's question. Redaction is structural
  (`security-sensitive`, earned by this ticket): the raw argv table never
  leaves one stack frame, no `-E`/`-e`-with-environment/`eww` anywhere,
  commands capped at 512 bytes after matching, only the integer-column
  snapshot is persisted verbatim. Three credential-free self-checks
  (`TestReachMatchArgvRows`, `TestReachChainUp`, `TestReachBackgroundHandle`)
  run ungated. Zero production files touched. See
  [`codebase/1230.md`](../codebase/1230.md) for the full arithmetic, the
  live-run evidence, and lessons from two rounds of code review (a MUST FIX
  gating the reachability verdict on the integer snapshot's own read error,
  plus a still-open SHOULD FIX on two record fields' finding-semantics).
- `process_pin_liveness_test.go` (#1235) — **offline instrument, not a probe**;
  no auth skip, no env gate, no live claude, no verdict about pyry — it is
  depended on as code, not as evidence, by the live probes #1236 → #1237. Two
  parts, both additive over #1230's `reach*` surface. **Exclusion-aware argv
  scan (`pin*` prefix)**: `pinPartition` is a pure post-filter over
  `reachMatchArgvRows`' own `(matches, total)`, splitting by a caller-supplied
  `exclude map[int]string` so an instrument-owned pid is withheld with its
  reason recorded rather than relying on a needle that happens not to collide
  with it; every matched row is retained (`MatchCount` visible as `> 1` rather
  than resolved to the first), and `reachMatchArgvRows`/`TestReachMatchArgvRows`
  are untouched. **Four-valued per-pid liveness read**: `pinReadState(pid)`
  execs a narrow `ps -p <pid> -o pid=,ppid=,stat=` (no descendant requirement —
  a target re-parented to pid 1 reads like any other) and classifies into
  `running` / `exited-but-not-yet-reaped` / `no-such-process` /
  `instrument-failed`, never collapsing two of them. Branch order is the
  contract: stderr, a `CommandContext` timeout's non-`ExitError` type, and
  stdout arriving alongside an error are all checked before the
  `no-such-process` default is reachable — closing the measured trap where a
  bad `ps` column prints a keyword list on stdout next to a non-zero exit, and
  the measured trap where a timeout-killed `ps` is byte-identical to a dead pid
  on every field but the sign of its exit status. Zombie detection is
  first-rune (`state[0] == 'Z'`), not equality — darwin emits `ZN`/`Z`, Linux
  `Z+`, and an equality miss falls through to `running` silently, the one
  direction this instrument must never fail in. Five credential-free
  self-checks, including a one-subject one-lifetime flip
  (`running` → kill-without-wait → `exited-but-not-yet-reaped` → wait →
  `no-such-process`) that proves the exec wiring rather than only the
  classifier. `security-sensitive`, earned by the column set's environment-read
  prohibition (`pid=,ppid=,stat=`, no `-E`/`-e`-env/`eww`) backed by a
  deterministic tripwire test, not just a doc comment. Zero production files
  touched; blocked by, and reuses rather than rebuilds, #1230's argv scan. See
  [`codebase/1235.md`](../codebase/1235.md) for the branch-order table, the
  patterns this ticket's measured traps establish, and a code-review SHOULD FIX
  (not blocking, deferred to #1236) on a self-check whose comment overclaims
  what its assertion pins.
- `teardown_liveness_test.go` (#1250) — **offline instrument, not a probe**; no
  auth skip, no env gate, no live claude, no verdict about pyry — depended on
  as code by the live rig #1251. Two additive parts over #1235's `pin*` and
  #1239's `fifoLive*` surfaces, both unedited. **Reaper-log classifier
  (`tdn*` prefix)**: `tdnClassifyReapLog(stderr, heldPGID)` is pure over bytes
  and answers `held-pgid-in-reap-line` / `reap-line-without-held-pgid` /
  `no-reap-line` (ambiguous by construction — `reap.go:64` guards the emit on
  `len(reaped) > 0`, so silence means "reaped nothing" or "never fired," and
  the `Detail` names both) / `instrument-failed`. Anchored on the reap
  message's bare text as a string literal, never `msg="..."` — `runAgentRunPty`
  passes no `Logger`, so `ptyrunner` falls back to `slog.Default()`, not the
  `slog.NewTextHandler` the ticket body cited, and an anchor built against the
  wrong handler would silently read "no line" on the only path that matters.
  Membership decided over parsed integers via a key-boundary attribute match
  (`tdnAttrIndex`), never a substring — closes both a false negative (`slog`
  quotes `pgids=` the moment a second pgid appears) and its dual false
  positive (held `77` inside the text of `pgids=[7788]`). **Real-`ps`
  fail-safe premise (AC2)**: four mis-invocation arms assert
  `len(exitErr.Stderr) > 0` read from `.Output()`'s own `*exec.ExitError`
  (the exact channel `pinReadState` consumes) before requiring
  `pinClassifyState` to return `instrument-failed` — proving, against real
  bytes rather than #1235's hand-built errors, that branch 1 keeps every
  broken invocation off the `no-such-process` verdict. The bad-column arm
  uses four requested columns (not three) because `ps` silently drops the
  unknown one and prints the rest, producing a row `pinStateRow` parses
  *successfully* as a live pid; the out-of-range arm escalates a candidate
  ladder until `ps` actually rejects one, rather than assuming a hard-coded
  constant is out of range (macOS caps at 99999, Linux's default `pid_max` is
  4194304). **Record + writer (AC3)**: `tdnRecord` composes
  `pinStateOutcome`/`fifoLiveOutcome`/`tdnReapOutcome` with no new liveness
  type and no verdict synthesized across them; `writeTdnArtifacts` emits
  exactly one file (`teardown.json`, `0o600`) — "exactly one file" is itself
  the redaction assertion, since the sibling writer's second file (a verbatim
  `ps` snapshot) has no analog here. 22 credential-free self-checks, zero
  SKIP. Zero production files touched. See [`codebase/1250.md`](../codebase/1250.md)
  for the full implementation, the subprocess-boundary citation-swap pattern,
  and a code-review SHOULD FIX (not blocking, deferred to #1251) on a
  first-match self-check row that doesn't discriminate its own claimed
  mutation.
- `teardown_reap_capture_test.go` (#1253) — **offline instrument, not a
  probe**; no auth skip, no env gate, no live claude. Drives #1250's
  `tdnClassifyReapLog` with bytes captured from a *real*
  `agentrun.ReapDescendantGroups` call, replacing that classifier's
  hand-written string-constant fixtures with a live capture so a future
  `slog` rendering change fails a test instead of silently making every
  liveness answer read `no-reap-line`. Builds real two-level process trees
  (test → re-exec'd parent → leaves, the parent required because
  `setpgid` on a child rules out a shell) and captures whatever
  `slog.Default()` emits during the reap via `log.SetOutput` — no `t.Parallel`
  in the file, since that redirect is process-global and not reentrant. Proves
  the capture *flips* within one harness: a killed group classifies
  `held-pgid-in-reap-line`, a childless walk root emits no line at all and
  classifies `no-reap-line`. A same-group sibling spared by `reap.go:52` is
  asserted *still alive* at the instant its pgid reads absent from the line —
  the unearned negative the instrument exists to refuse. Both renderings
  (`pgids=[N]` unquoted, `pgids="[N M]"` quoted) come from real reaps and are
  asserted to differ; the substring hazard is closed in the previously-untested
  suffix direction (`88` vs `[7788]`). One new file rather than an edit to
  `teardown_liveness_test.go`, both because that file's header declares itself
  "pure over bytes" (this harness spawns real trees and issues real SIGKILLs)
  and because #1251 had an in-flight +259/−40 diff to it at filing time. Every
  pid a real reap produces is treated as a trust boundary: `tdnKillTree`
  refuses `pid <= 1`/the test's own pid/pgid before any `syscall.Kill`, and the
  multi-line report-file parse is all-or-nothing rather than treating a short
  read as "not ready yet." 32 credential-free subtests, zero SKIP. Zero
  production files touched; calls `tdnClassifyReapLog` and does not edit it.
  See [`codebase/1253.md`](../codebase/1253.md) for the full implementation and
  two non-blocking code-review NITs (a misleadingly-named loop variable, one
  reasoned-not-measured comment).
- `teardown_liveness_probe_test.go` (#1251) — **live rig, security-sensitive**;
  opt-in behind `PYRY_PROBE_TEARDOWN_LIVENESS=1` on top of the package's normal
  auth skip. The standing check that keeps #1231's hand-verified answer true:
  stages one claude turn around #1223's `BASH_DEFAULT_TIMEOUT_MS` trigger, tears
  pyry down through its real SIGTERM-to-pid path, and records whether the
  backgrounded Bash command survived and by whose hand it died — calling
  #1250's classifier/record, #1235's liveness read, and #1239's FIFO read as
  code, not evidence. **The core idea is `tdnBeforeFault`**: the same
  after-teardown liveness classifier is run at the before-snapshot too, and a
  run whose before reading isn't uniformly `pinStateRunning` voids rather than
  passes — an instrument hard-wired to `dead` cannot pass a live run for free.
  Two structural discriminators separate the three readings that all look like
  "dead after exit": the still-held FIFO (released only in `t.Cleanup`, after
  the SIGTERM/wait/after-snapshot run inside the test body — the
  `background_reach_probe_test.go:355-356` cleanup-ordering trap inverted on
  purpose) rules out the command finishing on its own, and a pgid in the reap
  line rules out dying alongside claude (`reap.go:56-62` skips `ESRCH` before
  the append). `tdnRecord` widened rather than duplicated: `HeldPID` → `HeldPIDs`
  (a slice — #1230's live run matched two rows on one needle), single
  `ArgvScan`/`Liveness`/`FIFO` → paired `Before`/`After *tdnSnapshot`, plus
  `ClaudeVersion`/`TeardownPath`/`RunnerFromEnv`/`RunnerFromArgv` provenance.
  Disposition is a positive allowlist with one red arm
  (`tdnDispositionLeaked`); a content re-match is dispositive only when the
  after-liveness verdict is `pinStateRunning` — otherwise a zombie's
  kernel-blanked argv would misfile as pid reuse. A ninth reject branch beyond
  the spec guards teardown provenance itself: pyry exiting on its own before
  the rig's SIGTERM would still read `dead-by-reaper` correctly but attribute it
  to a `TeardownPath` that never ran. Repairs #1250's inherited SHOULD FIX (a
  reap-classifier fixture row that didn't discriminate its own claimed
  mutation) by swapping a concatenation order, verified red-then-reverted by
  deliberate mutation. Deliberately does **not** reuse
  `reachRunnerPathFromArgv` for the runner label — it keys on
  `--append-system-prompt-file`, which the streamrunner path also emits, so
  reuse would have silently mislabelled every stream-path record; the fresh
  `tdnRunnerFromArgv` discriminates on `--session-id` vs. `--input-format`
  instead. Live test named `TestRealClaude_TeardownLiveness` (not `TestTdn…`)
  so it doesn't join #1250's zero-SKIP offline suite; three offline
  self-checks (`TestTdnRunnerFromArgv`, `TestTdnDecideAfter`, `TestTdnPinHeld`)
  do. 48 subtests, zero SKIP on `^TestTdn`; full package 216 PASS / 44 SKIP.
  Zero production files touched. **The live half has not been run** — no
  claude login in the dispatch environment; ticket carries `needs-real-claude`.
  See [`codebase/1251.md`](../codebase/1251.md) for the full implementation and
  two non-blocking code-review findings (a SHOULD FIX and a NIT, both deferred
  to a future touch on this file).

- `result_trailer_observation_test.go` (#1266) — **offline instrument, not a
  probe**; ships the observation only, no verdict. Answers "when did pyry's
  `{"type":"result",...}` trailer first become visible on stdout, and how late
  might that observation be?" for #1267's downstream liveness classifier.
  Two closed value spaces, neither collapsible into the other's zero value:
  `trailSeen`/`trailAbsent`/`trailAborted` (what a pure scan, `trailScan`,
  found) and `trailBoundFromMiss`/`trailBoundFromStart`/`trailBoundNone` (what
  the staleness bound was measured from — `trailBoundFromStart` names the trap
  where a first-poll match yields a duration that bounds nothing, because the
  write may precede the poll loop entirely). Fixes a gap in the existing
  `parseResultTrailer` (`tool_loop_test.go:216`, untouched, all nine call
  sites keep today's behaviour) without touching it: that function discards
  `scanner.Err()`, so a stdout line past `bufio.Scanner`'s 64 KiB default
  (reachable — the trailer's `result` field is the last assistant message
  verbatim) reads identically to a genuine absence; `trailScan` reads the
  scanner error and reports the new `trailAborted` state instead. The cap
  (`reachCapCommand`, `reachMaxCommandBytes = 512`) is applied only to the
  retained verbatim `Line` copy, never to the bytes decoded into
  `resultTrailer` — `result` sits sixth on the pinned wire order
  (`emitter.go:456-468`) and `terminal_reason` last, so capping the raw line
  would truncate inside `result` and destroy the field #1267 branches on;
  `resultTrailer` has no `result` member, so the decoded value structurally
  cannot leak the assistant payload regardless. `trailWaitForTrailer` polls
  `probeSyncBuffer` on the existing `probePollInterval` (200 ms) and stamps
  `now` **before** reading the buffer each iteration, which is what makes a
  miss-derived bound an over-estimate of the true lateness rather than a
  possible under-estimate wearing a bound's label. Purely additive, one new
  file, zero production files touched; 11 subtests, 0 SKIP on
  `-run '^TestTrail'`. See [`codebase/1266.md`](../codebase/1266.md).

- `trailer_admissibility_test.go` (#1270) — **offline instrument, not a
  probe**; two pure predicates that decide whether #1266's trailer scan and
  #1253's reap-log attribution can support a claim, so #1271's downstream
  classifier never has to. `trailGate(trailScanResult) trailGateResult` maps
  onto a five-value positive allowlist (`trailGateUsable`/`NoTrailer`/
  `ScanAborted`/`BudgetFired`/`OutOfContract`) and certifies a non-empty
  terminal reason on the two arms that carry one.
  `trailAdmitAttribution(tdnReapOutcome, certified string) trailAdmitResult`
  maps the reap attribution onto a seven-value allowlist — one admissible
  value (`trailAdmitProof`, requiring verdict `tdnReapHeldPGIDKilled`,
  exactly one reap line, and a non-`max_turns` reason) plus five named voids
  plus an out-of-contract value. Both open with a contract block ahead of
  every real arm, so out-of-contract is a guard at the top, never a
  fall-through default. The reap-side voids are outranked by the
  budget-fired void (structural: on that path the reap ran before the
  trailer, so the reap record's contents are irrelevant), which is itself
  outranked by the contract block (a caller's bug must surface regardless of
  path). `trailGateResult` is trap-free by construction — no `*resultTrailer`
  reachable from it, directly or through an embedded field — even though the
  gate cannot be the pointer trap's last consumer (`trailObservation` embeds
  `trailScanResult`, so `.Trailer` is still reachable by promotion elsewhere).
  `trailBudgetTerminalReason = "max_turns"` is a string literal with no
  executable pin to `emitter.go`'s unexported `wireFields`; a production
  rename would silently turn a budget-fired void into a false proof — named
  as a known limit, not fixed, since fixing it needs either a production
  change or a live budget-fired fixture, both out of scope for this
  probe-family ticket. Purely additive, one new file, zero production files
  touched; 50 subtests, 0 SKIP on `-run '^TestTrail'`. One code-review
  SHOULD FIX (an uncontracted `certified` parameter that lets `""` read as
  `trailAdmitProof`) shipped as a named, un-fixed gap — see
  [`codebase/1270.md`](../codebase/1270.md) for the full implementation, the
  ordering arguments, and the deferred findings.

- `trail_run_outcome_test.go` (#1271) — the **run-level classifier**:
  `trailClassifyRun(trailRunReadings) trailRunOutcome` maps one probe run's raw
  observations onto exactly one of eleven outcomes (three answers, eight named
  voids) so a run that measured nothing is recorded as having measured nothing
  rather than falling through to a finding. Consumes #1270's two admissibility
  results; a nine-check contract block (C1–C9) guards the top, calling
  #1270's/#1235's shipped membership predicates rather than re-deriving them,
  so the out-of-contract value is a guard, never a switch default. An
  admissible attribution is consulted *before* any point-in-time reading
  (proof outranks pyry-not-exiting outranks every instrument void), because
  the point-in-time reads are expected to be late and must never be what a
  verdict rests on — the systematic-false-negative case this ticket exists to
  prevent is a regression row in `TestTrailClassifyRun`. Input and outcome
  records carry discriminators and counts only — `BoundFrom` rather than the
  `trailObservation` that embeds `trailScanResult`, `MatchCount`/`RowsScanned`
  rather than `pinScan.Matches`' verbatim argv, no command string anywhere —
  enforced by a marshal-and-search test with a needle in four inputs. Folds in
  #1270's parked SHOULD FIX (an uncontracted `certified` parameter) at both the
  layer it was found and as a composition pair (C4/C5) one layer up. Purely
  additive, one new file plus a ~70-line extension of #1270's own closure test
  (eighteen constants → twenty-nine); 14 `TestTrail`-prefixed functions, 82
  subtests, 0 SKIP on `-run '^TestTrail'`. See
  [`codebase/1271.md`](../codebase/1271.md).

- `trail_run_rig_test.go` (#1268) — **proof-of-wiring rig, not a new
  instrument.** #1266/#1270/#1271 each prove their piece against fixtures and
  synthetic buffers; `trailClassifyRun` is pure, so a fixture proof never
  shows which code path fed it — a rig wired to the wrong path emits the same
  positive as one wired to the right one. This file gathers the classifier's
  inputs through the live producer chain (`trailWaitForTrailer` → `trailGate`,
  `pinScanArgv`, `pinReadState`, `tdnClassifyReapLog` → `trailAdmitAttribution`)
  against a real FIFO and a real `cat`, funnelled through one seam
  (`trailRigGather`) so "no field is hand-assigned and no reap line is
  synthesised" is a property of the file rather than a promise about its call
  sites — `tdnClassifyReapLog` is called over a literal `nil` inside that
  function, never a parameter. Three tests: a pre-subject reading
  (`trailOutcomeNoRowMatched`) flipping to a during-subject reading
  (`trailOutcomeMatchedUnattributed`) across one subject's life, with both
  post-death per-pid states (`pinStateExitedNotReaped` then
  `pinStateNoSuchProcess`) taken deterministically because the subject is a
  direct child; a staleness-bound margin pinned tight enough that a
  start-derived (rather than miss-derived) bound fails it; and a
  shell-wrapped subject staged so more than one row matches, without
  resolving "the" pid. `trailOutcomeRunningAtTrailer` — the finding itself —
  stays deliberately unreachable, twice-stated in the header: it requires a
  reap line this rig must not grow. Purely additive, one new file, 594 lines,
  zero existing call sites changed. See [`codebase/1268.md`](../codebase/1268.md).

- `finding_attribution_fanout_test.go` (#1280) — **the many-to-one reduction**:
  `trailAdmitAttribution` (#1270) takes one held process group; the probe's
  argv scan returns a set, because `pinScanArgv` deliberately refuses to
  resolve "the" pid. `finAttributeFanOut(stderr []byte, pgids []int, certified
  string) finAttributeRecord` reduces that set to the single `trailAdmitResult`
  `trailRunReadings.Admit` (#1271) accepts, under a total order
  (`finAttributeOrder`, proof first, argued in the code) so no group's void
  suppresses another group's proof and no composition of voids manufactures
  one. Two record-level conditions, never selectable values:
  `finAttributeGroupUnreportable` (a `pgid <= 1` group `reap.go:52` skips
  before it ever kills anything — surfaced rather than handed to
  `tdnClassifyReapLog`, which would misattribute the staging fault to the
  instrument) and `finAttributeNoGroups` (no reportable group remained — a
  staging fault, `Selected` left zero rather than filled with either of the
  two publishable falsehoods AC4 prices). The credential channel is closed by
  the **signature** — `pgids []int`, never `[]reachProc` — not a check;
  `certified` crosses verbatim by design (already-shipped, publishable
  behaviour) and the fan-out multiplies its copy count by the distinct-group
  count, each capped at 512 bytes. `finAttributeEntry` carries only `PGID`
  and `Admit` — no `tdnReapOutcome.Line`, no `reachProc.Command`. Purely
  additive, one new file, 767 lines, zero production change, zero consumer
  call sites; four top-level tests, 0 SKIP on `-run '^TestFinAttribute'`. One
  code-review SHOULD FIX, not blocking, deferred to #1281: the no-captured-
  bytes structural check is top-level-key-only over what is now a *nested*
  record, so a future `Command` field added to `finAttributeEntry` would pass
  it unnoticed. See [`codebase/1280.md`](../codebase/1280.md) for the full
  implementation, the selection-order argument, and the mutation-tested
  lessons.

- `finding_staging_gate_test.go` (#1284) — **the tier below the classifier**:
  `trailClassifyRun` (#1271) assumes a run staged — a Bash call issued, the
  rig's hold command, a completed rendezvous — and on an unstaged run its
  argv scan still runs over a healthy process table and matches nothing,
  landing on `trailOutcomeNoRowMatched`: a real answer, published as a false
  negative about a run where no command ever existed. `finOutcomeStagingGate(
  finOutcomeStaging) finOutcomeResult` decides, from synthetic staging
  conditions alone, one of six failure outcomes or the pass-through
  (`finOutcomeReadyToClassify`, deliberately not the zero value — an unfilled
  result must never read as "staged, go classify"), all seven in their own
  `stage-` sub-namespace apart from the eleven's `run-`. The structural
  closure is the signature itself: neither type mentions `trailRunReadings`,
  so a failure arm holds nothing a classifier call could be made from — the
  forbidden call is unwritable, not discouraged. Two guard conditions close
  reachable pass-through holes (both commands left empty; an unfilled
  match-count want agreeing with an unfilled count at zero). No Detail
  interpolates either command — both the issued command (verbatim model
  output) and the staged one (embeds a `t.TempDir()` path and an
  `exec.LookPath` result) are captured strings on the same footing — and the
  no-captured-bytes test plants `trailNeedle` in both, with a per-row
  headroom assertion against `trailDetail`'s 512-byte cap: house-style Detail
  prose alone was found to eat enough of that cap in the first draft to
  truncate a leaked command's needle away before it could be caught, a
  vacuity distinct from (and the mirror image of) #1278's cap hazard. Purely
  additive, one new file, 790 lines, zero production change, zero consumer
  call sites. See [`codebase/1284.md`](../codebase/1284.md) for the full
  implementation and the mutation-tested lesson on redaction-test vacuity.

- `finding_trailer_evidence_test.go` (#1290) — **the trailer half of the
  probe's published record.** `finTrailerRecord` (ten scalars, no pointer, no
  embedded observation) carries one run's outcome value together with the
  trailer evidence behind it — scan `State`, the `BoundFrom` lateness
  discriminator with its `Bounded` boolean (`== trailBoundFromMiss` and
  nothing else, never `Staleness != 0`) and `Staleness` itself, and the four
  decoded trailer fields (`Subtype`, `IsError`, `TerminalReason`,
  `StopReason`). `finTrailerBuild(outcome string, obs trailObservation)
  finTrailerRecord` is the pure projection: the four fields are read from
  `obs.Trailer` under a guard whose *first* operand is `State == trailSeen`
  (so a no-trailer run returns its void instead of panicking through the
  #1266 discriminated optional), never from the capped `Line`; `Outcome` is
  copied from the caller's #1271/#1284 value as handed, never re-derived from
  `State`. No field carries `omitempty` — under it a seen trailer with an
  empty `terminal_reason` would render byte-identical to a no-trailer record,
  the exact collapse the nil-pointer design one tier down exists to prevent.
  The no-captured-bytes proof plants `trailNeedle` via `trailPaddedTrailer(0)`
  (385 bytes, needle inside the 512-byte cap at offset 104–146) rather than
  the family's habitual past-the-cap pad, which would pass vacuously against
  a record that kept the capped line. Purely additive, one new file, 697
  lines, zero production change, zero consumer call sites. See
  [`codebase/1290.md`](../codebase/1290.md) for the full implementation, the
  guard-ordering disclosure code review confirmed by mutation, and the
  in-cap-plant lesson.

- `finding_run_record_test.go` (#1291) — **the assembled run record.**
  `finRecordRun` is the record one probe run publishes: pyry's exit code,
  every matched row reduced to `finRecordProc` (`PID`/`PPID`/`PGID` — three
  `int` fields, reflection-asserted, nothing else), the per-pid liveness
  verdicts (`[]pinStateOutcome`, carried whole), the reap-log attribution
  (`finAttributeRecord`, #1280) and the trailer sub-record
  (`finTrailerRecord`, #1290), both embedded whole rather than re-derived,
  and the runner path. The runner path is recorded **as observed**: the
  env reading (`reachRunnerPathFromEnv`) is carried as documentation, not
  corroboration, alongside an independent argv reading
  (`tdnRunnerFromArgv`), reduced to a three-valued `RunnerAgreement` —
  `agree` / `disagree` / `indeterminate` — decided on the **label** each
  reading's leading token, never the whole string, because the two
  producers append their own free-text reasons and the full strings are
  therefore never equal even when both name the same runner.
  `finRecordInputs` uses named fields rather than positional parameters
  specifically because two adjacent same-typed strings
  (`RunnerFromEnv`/`ClaudeCommand`) sit on opposite sides of the argv
  prohibition, and it carries neither a `trailObservation` nor a
  `trailScanResult` field, which is what keeps the discriminated-optional
  trailer pointer out of reach. Purely additive, one new file, 1061 lines,
  zero production change, zero consumer call sites; five top-level tests,
  all offline. One code-review SHOULD FIX left non-blocking: the
  attribution sub-record's "carried whole" claim is pinned by a single
  nested scalar rather than `reflect.DeepEqual` (the trailer half's
  pattern), so a future partial-carriage regression there would pass
  unnoticed — deferred to #1286. See [`codebase/1291.md`](../codebase/1291.md)
  for the full implementation and the mutation-tested lessons.

- `finding_artifact_write_test.go` (#1286) — **rendering the run record into
  a pasteable artifact, and proving the directory it lands in leaks no
  captured bytes.** `finWriteArtifacts(t, dir, rec finRecordRun)` takes the
  built record and nothing else — no raw process-table bytes, no second
  `[]byte` parameter — and writes exactly two files: `run.json`
  (`json.MarshalIndent`) and `run.md` (a fixed safety-claim constant, the
  same bytes fenced, one summary line built from derived scalars only). The
  signature *is* the design: `writeReachArtifacts` (`background_reach_probe_
  test.go:823`) is the cautionary precedent it deliberately does not
  reuse — that writer's unexported `rawPS` field produces a second file,
  `reach.ps.txt`, carrying the verbatim process table beside a clean
  `reach.json`; `finRecordRun` has no unexported field, so there is nothing
  raw in this writer's reach to write. Four tests measure what was
  **written**, not what was built: a set-equality census of every JSON
  *path* the record declares against every path the artifact renders
  (path-based rather than name-based after a code-review MUST FIX — four of
  the family's key names are shared across types, and `matched_rows[]`'s
  three keys are shared with `pinStateOutcome`'s, so a name-based census
  covered that slice not at all); a four-channel `trailNeedle` sweep over
  every file `os.ReadDir` returns (planted only in inputs the pipeline
  reduces or drops — a matched row's argv, the claude argv, an in-cap
  trailer scan line, a reap outcome's stderr — never in the four fields the
  record carries whole), with a mandated pair of applied-and-reverted
  mutations (one inside the Detail format, one adding an undeclared third
  file) both observed RED before the sweep shipped; a recursive
  forbidden-key scan with two exact-key exemptions (`tool_stderr`, carried
  whole and permitted; `runner_from_argv`, a closed three-constant set with
  no input byte in reach); and a structural + behavioural pair proving
  `resultTrailer` has no `result` member and that the four decoded trailer
  scalars cross into the artifact verbatim while the needle beside them does
  not. The trailer plant lands **inside** `reachCapCommand`'s 512-byte cap
  (pad `0`, needle at byte 104–146) — `trailNeedle`'s own comment claims it
  is placed past the cap, which this ticket measured to be false against the
  fixture the family actually reuses; the comment was left uncorrected as a
  sibling file, out of scope here. Purely additive, one new file, 966 lines,
  zero production change, zero consumer call sites. See
  [`codebase/1286.md`](../codebase/1286.md) for the full implementation, the
  path-vs-name census MUST FIX, and the stale-comment lesson.

## Test infrastructure

`fixtures_test.go` re-execs the test binary as a fake `pyry` when `GO_TEST_HELPER_PROCESS=1` is set (via a `TestMain` branch), and pins `PYRY_E2E_BIN=os.Args[0]` for every other test so `ensurePyryBuilt` short-circuits to the fake. The fake selects behaviour from `PYRY_E2E_FAKE_MODE` (`happy`, `fail`, `sleep`, `argv`). This lets the helper's contract be validated entirely from within the package — no real `claude` and no real `pyry` build are required for the helper's own tests. (The smoke test `TestClaudeBinaryAvailable` from #361 remains the only test in the suite that depends on real `claude` being on PATH.)

## Make target

```make
.PHONY: e2e-realclaude
e2e-realclaude:
	$(GO) test -tags e2e_realclaude ./internal/e2e/realclaude/...
```

No `-race`. These are I/O-bound trust-boundary checks, not goroutine-stress tests; flip on `-race` per-test when a future test in the directory does spin goroutines.

`make check` is unchanged. CI's per-PR `make check` does not run this suite — it stays opt-in for that path.

## CI cadence: code-review phase, no nightly workflow

The real-`claude` suite is NOT wired into GitHub Actions. It runs **locally
during the code-review phase** of every dispatched ticket via the pipeline
— see the code-review agent's `CLAUDE.md` for the invocation contract.

The earlier nightly workflow (`.github/workflows/e2e-realclaude-nightly.yml`,
#362) was removed in #379 the same day it landed. CI-side rationale for the
removal:

- GitHub Actions would need an `ANTHROPIC_API_KEY` repo secret; Max-plan
  tokens used locally are free.
- Per-run cost ($0.10–$0.50, scaling with test count) buys nothing local
  runs don't already cover once code-review runs the suite on every PR.
- Failure surface synchronised to dispatch cadence beats unpredictable
  04:00 UTC failures.
- One fewer CI file to keep in lockstep with `self-check-daily.yml`.

The make target is unchanged — `make e2e-realclaude` is still the entry
point, just no longer invoked by CI.

## Verifying tag exclusion

After landing, `make test 2>&1 | grep realclaude` should be empty (or only an `ok ... [no test files]` line) — files with an unsatisfied build tag are dropped at the build stage, so the package compiles to an empty test binary.

## Related

- [features/e2e-harness.md](e2e-harness.md) — the fake-claude sibling suite.
- [features/install-e2e.md](install-e2e.md) — the `e2e_install`-tagged install round-trip suite (same naming pattern).
- [features/agentrun-selfcheck-package.md](agentrun-selfcheck-package.md) — `self-check-daily.yml`, the sibling badge-only nightly self-check workflow.
- Ticket [#361](https://github.com/pyrycode/pyrycode/issues/361) — scaffolding ticket; codebase note at [`codebase/361.md`](../codebase/361.md).
- Ticket [#362](https://github.com/pyrycode/pyrycode/issues/362) — the now-removed nightly workflow; codebase note at [`codebase/362.md`](../codebase/362.md). See also [#379](https://github.com/pyrycode/pyrycode/issues/379) for the removal.
- Ticket [#372](https://github.com/pyrycode/pyrycode/issues/372) — `WithWorktree` + `ReadJSONL` fixture helpers; codebase note at [`codebase/372.md`](../codebase/372.md).
- Ticket [#373](https://github.com/pyrycode/pyrycode/issues/373) — `RunPyryAgentRun` subprocess fixture helper; codebase note at [`codebase/373.md`](../codebase/373.md).
- Ticket [#364](https://github.com/pyrycode/pyrycode/issues/364) — prompt-fidelity regression guard, first consumer of the fixture trio; codebase note at [`codebase/364.md`](../codebase/364.md).
- Ticket [#365](https://github.com/pyrycode/pyrycode/issues/365) — `--allowed-tools` enforcement regression guard, second consumer of the fixture trio; codebase note at [`codebase/365.md`](../codebase/365.md).
- Ticket [#376](https://github.com/pyrycode/pyrycode/issues/376) — tool-loop integrity regression guard, third consumer of the fixture trio (positive-path counterpart to #365); codebase note at [`codebase/376.md`](../codebase/376.md).
- Ticket [#381](https://github.com/pyrycode/pyrycode/issues/381) — per-dispatcher-role smoke suite, fourth consumer of the fixture trio (five named tests, one per role: po/architect/developer/code-review/documentation); codebase note at [`codebase/381.md`](../codebase/381.md).
- Ticket [#409](https://github.com/pyrycode/pyrycode/issues/409) — `WithWorktreeAuthenticated` opt-in real-API fixture variant (unblocks #383's permission-protocol spike and any future realclaude test that needs more than an argv-shape probe); codebase note at [`codebase/409.md`](../codebase/409.md).
- Ticket [#382](https://github.com/pyrycode/pyrycode/issues/382) — protocol-resilience suite, fifth consumer of the fixture trio (four named tests: Bash-non-zero-exit, premature-stdin-close, malformed-stream-json, large-prompt-near-context-window); codebase note at [`codebase/382.md`](../codebase/382.md).
- Ticket [#384](https://github.com/pyrycode/pyrycode/issues/384) — MCP server smoke suite, sixth consumer (four named tests, one per MCP server: qmd, plugin:context7:context7, codegraph, plugin:figma:figma); codebase note at [`codebase/384.md`](../codebase/384.md).
- Ticket [#385](https://github.com/pyrycode/pyrycode/issues/385) — budget guardrails, seventh consumer (cache-hit verification across two same-prompt runs + `--max-turns` boundary enforcement); codebase note at [`codebase/385.md`](../codebase/385.md).
- Ticket [#420](https://github.com/pyrycode/pyrycode/issues/420) — operator-visible denial signal assertion co-located in #365's test (disjunctive: assistant-text refusal keyword OR structured `result` envelope with `permission_denials` / `is_error=true`); codebase note at [`codebase/420.md`](../codebase/420.md).
- Ticket [#421](https://github.com/pyrycode/pyrycode/issues/421) — long-running session JSONL append-integrity regression guard, eighth consumer of the fixture trio (first ≥10-turn test in the suite: trailer `num_turns >= 10`, ≥10 assistant entries with `EndOfTurn=true`, last assistant entry is itself a well-formed end-of-turn, forward-defensive negative `bufio.Scanner: token too long` stderr tripwire); codebase note at [`codebase/421.md`](../codebase/421.md).
- Ticket [#422](https://github.com/pyrycode/pyrycode/issues/422) — SIGTERM-mid-tool_use cleanup regression guard, ninth consumer of the fixture trio (one named test pins three production invariants together: no orphan subprocess in pyry's process group via `pgrep -g`, no half-written trailing JSONL line via explicit byte-tail check, pyry exits within 5 s of SIGTERM matching `streamrunner.killGrace`); codebase note at [`codebase/422.md`](../codebase/422.md).
- Ticket [#423](https://github.com/pyrycode/pyrycode/issues/423) — large tool-output regression sensor, tenth consumer of the fixture trio (one named test drives a single Bash invocation producing ~80 KiB of stdout in one `tool_result` content block and pins four contracts: trailer `subtype="success"`+`stop_reason="end_turn"`, on-disk JSONL `tool_result` content >70 KiB, **byte-equal length** between disk and pyry-stdout twin, no `bufio.Scanner: token too long` in stderr); orthogonal to #421's long-session test — that test fires on many short lines, this one fires on a single line >64 KiB; codebase note at [`codebase/423.md`](../codebase/423.md).
- Ticket [#487](https://github.com/pyrycode/pyrycode/issues/487) — `/doctor` prompt-injection regression sensor, eleventh consumer of the fixture trio (one named test guards that the per-spawn settings JSON is accepted by claude at startup — first `user` JSONL entry's content is the operator's prompt, not the `/doctor` repair template; defence-in-depth assertion on `assistant` event presence; reuses unchanged fixtures with zero new helper surface); codebase note at [`codebase/487.md`](../codebase/487.md).
- Ticket [#491](https://github.com/pyrycode/pyrycode/issues/491) — consumer-side flip that pairs with #490's helper widening: 5 test files (`prompt_fidelity_test.go`, `prompt_fidelity_unicode_test.go`, `tool_loop_test.go`, `allowed_tools_enforcement_test.go`, `per_agent_test.go`'s shared `runRoleSmokeTest` helper) move from plain `WithWorktree(t)` to `WithWorktreeAuthenticated(t)`. With neither `ANTHROPIC_API_KEY` nor `CLAUDE_CODE_OAUTH_TOKEN` set the 9 affected tests (4 file-level + 5 `*_RoleLoop` subtests) skip in milliseconds with the named-variable diagnostic from #490, replacing prior 31 s ptyrunner timeouts / 5 s streamrunner exit-1 fails. `resilience_test.go:177` excluded (spawns claude directly with malformed stdin, asserts rejection before auth matters); codebase note at [`codebase/491.md`](../codebase/491.md).
- Ticket [#854](https://github.com/pyrycode/pyrycode/issues/854) — interactive daemon-relay liveness harness (bootstrap-bound conversation); source of `sealSendMessage`/`drainForAssistantReply` reused by #997 and #1028; codebase note at [`codebase/854.md`](../codebase/854.md).
- Ticket [#997](https://github.com/pyrycode/pyrycode/issues/997) — per-conversation liveness sibling of #854, drives `create_conversation` over the wire first; source of `startPerConversationHarness`/`createConversationViaPhone`/`sealEnvelope`/`drainForReply` reused by #1028.
- Ticket [#1028](https://github.com/pyrycode/pyrycode/issues/1028) — conversation-lifecycle real-claude liveness gate (create → rename → archive → unarchive → delete, liveness turn between unarchive and delete); first real-claude coverage of the conversation-management verbs, split from #963; codebase note at [`codebase/1028.md`](../codebase/1028.md).
- Ticket [#854](https://github.com/pyrycode/pyrycode/issues/854) — real-claude interactive two-turn liveness test, the RED/GREEN oracle for the fresh-daemon bootstrap-reply deadlock fix (`cmd/pyry/interactive_turn_stream_v2.go`'s `resolveTarget`); the only test in the suite that drives the daemon's interactive relay path directly rather than `pyry agent-run` — see [`codebase/854.md`](../codebase/854.md) and [`turnbridge-package.md`](turnbridge-package.md#which-jsonl-and-surviving-clear-rotation).
- Ticket [#1153](https://github.com/pyrycode/pyrycode/issues/1153) — real-claude counterpart of #1141's stream-json liveness proof; first real-claude test to flip `interactive_runner: "stream-json"`; introduces the reusable `writeStreamInteractiveConfig` config-toggle helper (composed with `spawnPermissionDaemon` by the blocked-by rider #1154) and the two-milestone `drainForCompletedTurn` drain; codebase note at [`codebase/1153.md`](../codebase/1153.md).
- Ticket [#1154](https://github.com/pyrycode/pyrycode/issues/1154) — stream-json sibling of #1030's real permission round-trip (desktop#483 scenario, real stream stack); composes #1030's harness/trigger scaffold with #1153's `writeStreamInteractiveConfig`/`drainForCompletedTurn` seams, zero production files; answer-only (approve), PTY-path cancel stays owned by #1030 Phase B; split from #1083; codebase note at [`codebase/1154.md`](../codebase/1154.md).
- Ticket [#1172](https://github.com/pyrycode/pyrycode/issues/1172) — reusable running-turn trigger infra (ports desktop `e1fe219`'s bounded foreground Bash-loop fix), holds a live claude turn in `turn_state{responding}` for a bounded window and proves it via `drainForResponding`/`assertNoIdleWithin`; transcribes #1153's setup, zero production files; split from #1083, consumed by #1176 (interrupt); codebase note at [`codebase/1172.md`](../codebase/1172.md).
- Ticket [#1176](https://github.com/pyrycode/pyrycode/issues/1176) — real-claude interrupt-stops-a-running-turn gate, composing #1172's running-turn trigger with the shipped interrupt primitives (#1120/#1121); new `drainForCancelledTurnEnd` drain guards against the spontaneous-`end_turn` vacuous pass; closes the fake-green/real-red gap (#949) on the interrupt path; split from #1083, zero production files; codebase note at [`codebase/1176.md`](../codebase/1176.md).
- Ticket [#1175](https://github.com/pyrycode/pyrycode/issues/1175) — real-claude permission **deny** round-trip on the stream-json runner (security-sensitive, architect security review PASS); reuses #1154's harness/trigger scaffold, swaps the answer to `reject_once`, and adds a `Source == "remote"`/`Outcome == "reject_once"` attribution assertion so a timeout-deny can't masquerade as the explicit reject, plus a workdir walk proving the gated `Write` never executed; rework `denyModalsUntilIdle` answers every retry modal (real haiku retries a denied tool at least once) bounded by a retry-count cap and wall-clock budget; zero production files; codebase note at [`codebase/1175.md`](../codebase/1175.md).
- Ticket [#1174](https://github.com/pyrycode/pyrycode/issues/1174) — real-claude cross of fakeclaude sibling #1137: on the stream-json runner, `new_session` rotates the bootstrap session id and `RestartFresh` spawns a genuinely fresh live claude child under the rotated id (not `--resume`), proven by a fresh `<idAfter>.jsonl` transcript appearing on disk; transcribes #1031's spine + #1153's/#1154's drain helpers, zero production files; split from #1083, sibling of #1173/#1175; codebase note at [`codebase/1174.md`](../codebase/1174.md).
