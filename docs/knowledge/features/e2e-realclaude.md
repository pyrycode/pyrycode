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
  message's bare text as a string literal, never `msg="..."` —
  `runAgentRunStreamRunner` passes no `Logger`, so `streamrunner.Run` falls
  back to `slog.Default()`, not the `slog.NewTextHandler` the ticket body
  cited, and an anchor built against the wrong handler would silently read
  "no line" on the only path that matters. (#1557 re-attributed this and two
  sibling comments in the source from the `ptyrunner` path #1348 deleted to
  the surviving `streamrunner` path; the mechanism was unchanged, only its
  owner's name was wrong.)
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

- `trailer_key_names_test.go` (#1357) — **offline instrument, not a
  probe**; adds `trailKeyNames(line []byte) []string`, invoked from
  inside `trailScan`'s existing match return on `scanner.Bytes()` — the
  full line, before `reachCapCommand`'s 512-byte cap, and never
  `trailScanResult.Line` — and lands on a new `trailScanResult.KeyNames
  []string` field (`json:"trailer_keys,omitempty"`). Answers a question
  #1266's fixed eight-field `resultTrailer` decode structurally cannot:
  `TerminalReason` is `omitempty`, so an absent `terminal_reason` and one
  emitted as `""` both decode to `""`, and `terminal_reason` is a pyry
  invention absent entirely from claude's own `result` line on the
  headless stream path. The reader answers *which keys the line carried*
  instead. Ordering is the whole difficulty: a realistic trailer's
  `result` field pushes the line past the cap, so a reader fed the capped
  `Line` returns zero names where the full line returns eleven — proved
  by a red (`TestTrailKeyNamesReadsTheFullLine`'s padded row), not a
  comment. Containment is structural rather than disciplined: the
  signature is `[]byte` in, `[]string` out, no `error` — the decode's
  failure arm returns `nil` and renders no part of the error, because
  `json.SyntaxError`/`json.UnmarshalTypeError` both carry attacker-chosen
  bytes no fixture needle could catch — and the intermediate
  `map[string]json.RawMessage` is discarded inside the function, never
  returned or formatted. `resultTrailer` is unwidened; a reachability
  test bans `map[string]json.RawMessage` from `trailScanResult` (**the
  map type, never `json.RawMessage` itself** — that element type is
  already reachable via `Trailer.PermissionDenials *[]json.RawMessage`,
  so banning it would be red against correct shipped code; the map ban is
  green today and the walk's own control proves it still walks). A
  five-needle containment test is deliberately scoped to `KeyNames`
  alone rather than the neighbouring whole-record-marshal idiom
  (`TestTrailScan`'s padded sub-test) — that idiom would go red against a
  *correct* build here, since `Subtype`/`StopReason`/`TerminalReason` are
  carried through `Trailer` by design and published verbatim downstream;
  bounding that publication surface is #1362's. Key names themselves were
  not bounded at this tier — `trailScanResult` is published by nothing, so
  there was no rendering surface here to bound — and the per-name cap
  landed one tier up instead, at `finSighting`/`finTrailerRecord`
  (`finBoundKeyNames`, #1363). Purely additive, one new 435-line
  file, zero production files touched, two-line extension of #1266's
  file (the field plus the wiring); 0 SKIP on `-run '^TestTrail'`. First
  code-review pass FAILed on an incomplete inbound-citation sweep (bare
  and chained pointers into #1266's file, which this ticket's field
  addition shifted) rather than on the implementation; the implementation
  itself passed clean on both rounds. See
  [`codebase/1357.md`](../codebase/1357.md) and, for the field's carriage
  onto both publishing tiers and its bounds, [`codebase/1363.md`](../codebase/1363.md).

- `trailer_admissibility_test.go` (#1270) — **offline instrument, not a
  probe**; two pure predicates that decide whether #1266's trailer scan and
  #1253's reap-log attribution can support a claim, so #1271's downstream
  classifier never has to. `trailGate(trailGateInput) trailGateResult` maps
  onto a seven-value positive allowlist (`trailGateUsable`/`NoTrailer`/
  `ScanAborted`/`BudgetFired`/`AbsentOwesNone`/`PresentOwesNone`/`OutOfContract`)
  and certifies a non-empty terminal reason on the two arms that carry one.
  **#1373** widened the
  input from a bare `trailScanResult` to `trailGateInput{Scan, RunnerPath}`
  so the gate's input can carry which runner produced the trailer line
  (`terminal_reason` means different things on ptyrunner vs. streamrunner);
  `RunnerPath` is echoed onto a fourth `trailGateResult` field. Until #1420
  no arm read it; since #1420 exactly one does — the absence arm. What
  `TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt` proves is the
  per-row declaration (9 rows × 5 readings): a row that does not declare
  it has a byte-identical `Detail` across them, and the declaring row's
  variance is proved positively by the same test. See
  [`codebase/1373.md`](../codebase/1373.md). **#1419** split the
  out-of-contract arm that fires on an empty `terminal_reason` into two: the
  key absent from the line, and the key present with a blank value —
  discriminated by scan-produced `KeyNames` membership
  (`slices.Contains(keyNames, trailReasonKeyName)`), never by the decoded
  value or by cardinality, both of which collapse the two shapes. Both
  arms still answer `trailGateOutOfContract` with an empty `Reason`, so at
  #1419 the five-value gate allowlist and every downstream consumer were
  unchanged; only the published `Detail` — and which of the gate's ten
  return sites (#1420) a given input reaches — changed. See
  [`codebase/1419.md`](../codebase/1419.md). **#1417** then diverges exactly
  one of the three absence sub-cases — absent `terminal_reason` on a path
  the observed reading reduces to `streamrunner`, which owes none — to
  `trailGateAbsentOwesNone`, a sixth gate value that certifies no reason
  (`Reason` stays empty) but is no longer `trailGateOutOfContract`: a
  healthy headless `PYRY_USE_STREAMJSON=1` run's trailer is claude's own
  `result` line by construction (`streamrunner.Run` passthrough,
  `internal/agentrun/streamrunner/runner.go:177-179`; watchdog-only
  synthesis, `:250-253`), so filing it as an out-of-contract caller bug was
  the defect. The other two absence sub-cases (owes-one, path-unnamed) and
  present-and-empty keep `trailGateOutOfContract`, so at #1417 `trailGate`
  still had ten return sites, five of them `trailGateOutOfContract` (was
  six). `trailClassifyRun` (#1271, below) gains a matching step-1 arm rather
  than falling through: `trailOutcomeVoidPathOwesNoReason`, a twelfth run
  outcome and a void — not `trailOutcomeVoidNoTrailer` (a trailer *was*
  written on this path) and not `trailOutcomeOutOfContract` (this is a
  genuine reading, not a caller's bug). At #1417 this was unreachable from
  either shipped live gather — both filled `RunnerPath` with
  `trailRunnerUnread()` — so the value was reachable only from fixtures; no
  comment added by that ticket claimed the gate decides against the path a
  live run took. **#1452** closes that gap for the finding gather (below) —
  it no longer holds. See [`codebase/1417.md`](../codebase/1417.md).
  **#1433** then reads the runner
  path on the **presence** side — the complementary half of #1420's absence
  split, and the gate's second decision-path caller of
  `trailReasonAgainstPath`. A `terminal_reason` that IS on the line, present
  and non-empty, from a run whose observed path reduces to `streamrunner`
  (owes none), reaches a new return site answering the shipped
  `trailGateOutOfContract` and certifying nothing — placed *after* the
  budget arm (a `max_turns` trailer on such a path keeps reaching the budget
  arm; the budget void is structural and outranks every reap-side void) and
  *before* the present-and-empty branch (which stays path-invariant
  deliberately, since the reduction's one absorbing answer can't express the
  `NO LIVE REPRO EXISTS` distinction between a blank key and an absent one).
  `trailGate` now has **eleven** return sites, **six** of them
  `trailGateOutOfContract` (was five). The Detail cites the 32 B
  `trailReasonPresentOwesNone` constant rather than embedding the
  reduction's 395 B Detail (445 B measured, against a 470 B ceiling); the
  runner-path sweep's `pathVaries` exemption widens to cover the certified
  `Reason` too, since the usable row is now the first arm whose
  certification itself moves with the reading — repaid by a positive
  per-reading companion sub-test rather than left as withdrawn coverage. No
  closed set grows; promoting the shape to a gate value of its own,
  mirroring #1417's move for absence, is **#1434**. See
  [`codebase/1433.md`](../codebase/1433.md). **#1434** then makes that
  promotion: the presence arm answers `trailGatePresentOwesNone` — a
  seventh gate value — instead of `trailGateOutOfContract`, and still
  certifies nothing (`Reason` stays empty). `trailGateOutOfContract`'s
  enumerated sub-cases drop from six to five; `trailGate` still has eleven
  return sites, but only five answer `trailGateOutOfContract` (was six). The
  Detail keeps citing the case constant rather than the reduction's Detail
  (427 B measured, against the same 470 B ceiling — down from #1433's 445 B
  after the closing "a value of its own is #1434" clause came out).
  `trailClassifyRun` (#1271, below) gains a matching step-1 arm,
  `trailOutcomeVoidReasonNotOwedByPath` — a thirteenth run outcome and a
  void, distinct from its `trailOutcomeVoidPathOwesNoReason` sibling (same
  void-ness, opposite reading: absent-on-owes-none is that path's healthy
  shape, present-on-owes-none is not) and from `trailOutcomeOutOfContract`
  (a genuine reading, not a caller's bug). Both closed sets grew in one
  commit, since step 1's switch has no default arm and an unhandled value
  would fall through to steps 3-8 and award a scan-side answer about pyry
  from a record the gate says certifies nothing. Two code-review FAIL/PASS
  rounds on the family's repeat failure mode — bare `(:NNN)` cites resolved
  by last-named-file instead of by symbol, both correct on `main` and moved
  anyway — fixed by re-deriving bare cites from their named symbol first.
  See [`codebase/1434.md`](../codebase/1434.md).
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
  observations onto exactly one of sixteen outcomes (four answers, twelve named
  voids — `trailOutcomeVoidPathOwesNoReason` is #1417's,
  `trailOutcomeVoidReasonNotOwedByPath` — a `terminal_reason` present on a
  runner path that owes none, a genuine reading rather than a caller's bug —
  is #1434's, `trailOutcomeAliveAtSightingByOrdering` — the second
  answer, and the only one from an evidence class other than the reap log —
  is #1446's, `trailOutcomeVoidPinnedPidDidNotEstablish` — the sighting
  route having *measured* a pinned pid without establishing aliveness at the
  trailer's sighting, split off the blanket #1446 left on
  `trailOutcomeVoidPathOwesNoReason` — is #1447's, and
  `trailOutcomeVoidSightingRouteNotStaged` — the route never having been
  staged at all, split off that same blanket, which #1447 left shared between
  a never-staged run and a measured-premise-failure one — is #1448's) so a run
  that measured nothing is recorded as having measured nothing
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
  subtests, 0 SKIP on `-run '^TestTrail'`. **#1446** later wires the one arm
  from which no evidence route was reachable — `trailGateAbsentOwesNone` — to
  #1440's `trailEstablishSighting`, consulted from inside the arm rather than
  by falling through to the scan-side steps below it; `trailRunReadings`
  gains `Ordering`/`PinnedPid` (both taken whole, and deliberately
  unvalidated by the contract block — at the time, no shipped gather staged
  either field, so a tenth check would have filed every run that existed then
  as `trailOutcomeOutOfContract`. **#1458** later stages the pin half from the
  one live caller, `finExitRunProbe` — **since #1353, a second live caller
  stages the same pin half, see below**; **#1462** later gives `finGatherInputs`
  a field the ordering can arrive on but wires no live caller to fill it, so
  the ordering half stays unstaged through *that field* on every run that
  exists today — **since #1353, see below**; the field
  remains uncontracted even with a carrying field shipped, since its zero
  already has an argued home inside the route itself — see
  [`codebase/1458.md`](../codebase/1458.md) and
  [`codebase/1462.md`](../codebase/1462.md)), and `trailRunOutcome` gains `Route`
  (`evidence_route`, empty except on the two finding values) so a reader
  never infers which evidence class produced a verdict from the outcome
  value alone; the void arm's Detail exchanges its out-of-contract clause for
  the route's answer rather than growing past its 22-byte headroom. **#1447**
  then takes the route's *measured non-establishment* case off that same
  blanket void: where the route measured a pinned pid and did not establish
  aliveness (`sighting.Value == trailSightingUnestablished`), the arm now
  answers `trailOutcomeVoidPinnedPidDidNotEstablish` rather than
  `trailOutcomeVoidPathOwesNoReason`, and publishes `Route =
  trailRouteSighting` on it — the first time a *void* carries a route, so
  `Route`'s own doc and `trailRouteSighting`'s doc were both corrected from
  "set on the finding alone" to "set wherever the route's own measurement
  decided the value." **#1448** then separates the two cases #1447 still left
  sharing that same blanket: a guard on `readings.Ordering.Value == ""`,
  tested *before* `trailEstablishSighting` is called and deliberately
  single-sided (only the ordering side lacks a documented "unfilled maps
  here" clause in its reason constant's doc — the pid side already has one),
  routes the never-staged case to a value of its own,
  `trailOutcomeVoidSightingRouteNotStaged`, while the remaining fall-through —
  route staged, consulted, measured nothing — keeps
  `trailOutcomeVoidPathOwesNoReason` and now also publishes `Route`. A new
  field, `trailRunOutcome.RouteReason` (`evidence_route_reason`), carries
  `sighting.Reason` through whole on both sighting-void arms so a reader
  tells a measured premise failure apart from an unanswered pid read — the
  two verdicts the route itself returns identically — without parsing the
  Detail; the published invariant is a biconditional,
  `RouteReason != "" ⟺ Route == trailRouteSighting`, since the reap-log route
  has no reason space and can never publish one. No default arm added in
  either ticket: each new branch sits inside the arm's existing total
  coverage rather than growing it by a case. See
  [`codebase/1271.md`](../codebase/1271.md),
  [`codebase/1446.md`](../codebase/1446.md),
  [`codebase/1447.md`](../codebase/1447.md) and
  [`codebase/1448.md`](../codebase/1448.md).

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
  `stage-` sub-namespace apart from the fourteen's `run-`. The structural
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

- `finding_staging_fill_test.go` (#1304) — **fills the staging record from a
  run's own transcript.** `finOutcomeStagingGate` (#1284, above) decides all
  seven staging outcomes from synthetic inputs; this file fills exactly the
  three transcript-side fields (`BashIssued`, `IssuedCommand`, `TriggerFired`)
  a real caller would supply, through a `finTranscript*` composition reading a
  JSONL transcript the test writes at the session's own path. The scan's unit
  is a `finTranscriptBashCall{ToolUseID, Command}` pair rather than a bare
  command: the shipped `probeWaitForBashToolUse` returns the **first** Bash
  `tool_use` regardless of `input.command` (a #1223 code-review SHOULD FIX
  shipped unfixed), and #1230 guarded that value-side caller-side already
  without editing the shared rig — this file generalises the guard and closes
  a second, key-side route to the same defect: a composition that selects the
  staged call for its *command* but keeps the first call's *id* would still
  read the trigger off the decoy's `tool_result`, since the trigger reading is
  `probeWaitForToolResult(<id>)`. The content guard (`finTranscriptSelect`) is
  pure over its input — no `*testing.T` — so its removal (AC2's mutation) runs
  and grades without touching the worktree; the first-match id is bound inside
  an `if` statement in `finTranscriptSelectBash` and goes out of scope
  immediately after, making it unreferenceable rather than merely unused
  below. `TriggerFired` reads `timedOutAfterMs` presence alone, never
  conjoined with the handle and never corroborated by
  `tool_use.input.run_in_background` — that flag marks the model-set
  backgrounding path this probe must exclude (`docs/knowledge/codebase/
  1223.md:87-88`). Nothing on the path trims, unquotes, or canonicalises
  either command; both new types carry no json tags, mirroring
  `finOutcomeStaging`'s own rule (`finding_staging_gate_test.go:141-157`).
  Purely additive, one new file, 602 lines, zero production change, zero
  consumer call sites. See [`codebase/1304.md`](../codebase/1304.md) for the
  full implementation and both mutation-tested rows.

- `finding_trailer_evidence_test.go` (#1290, builder moved onto the sighting
  carrier #1320, published bound proven measured #1316) — **the trailer half
  of the probe's published record.**
  `finTrailerRecord` (ten scalars, no pointer, no embedded observation)
  carries one run's outcome value together with the trailer evidence behind
  it — scan `State`, the `BoundFrom` lateness discriminator with its
  `Bounded` boolean (`== trailBoundFromMiss` and nothing else, never
  `Staleness != 0`) and `Staleness` itself, and the four decoded trailer
  fields (`Subtype`, `IsError`, `TerminalReason`, `StopReason`).
  `finTrailerBuild(outcome string, sighting finSighting) finTrailerRecord`
  is the pure projection: since #1320 it takes the #1309 carrier rather than
  a `trailObservation`, so its input carries no `.Line` and no
  `*resultTrailer` — both the record it returns and the builder itself are
  now trap-free by construction, checked by
  `TestFinSightingReachesNoScanType` rather than asserted in prose. The four
  fields are read from `sighting`'s own scalars under a guard on
  `sighting.CarriesTrailer` (a bool the carrier precomputes — no pointer left
  to guard a dereference of; a no-trailer run returns its void instead of
  panicking, unreachably now rather than through a checked short-circuit);
  `Outcome` is copied from the caller's #1271/#1284 value as handed, never
  re-derived from `State`. On the false arm the four scalars are zeroed
  rather than copied through — under the carrier that is a decision the
  builder makes rather than a consequence of there being no pointer to read,
  pinned in both directions by
  `TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer` over one
  carrier with its one impossible bit flipped. `StopReason` is the one
  exception to trap-free: forwarded from the model's last message uncapped,
  by design, named explicitly so a sweep author doesn't plant a needle in a
  field the record must carry verbatim. No field carries `omitempty` — under
  it a seen trailer with an empty `terminal_reason` would render
  byte-identical to a no-trailer record, the exact collapse the nil-pointer
  design one tier down exists to prevent. `TestFinTrailerRecordCarriesNoCapturedBytes`
  no longer plants `trailNeedle` here — #1325 retired that plant along with the
  test's other `.Line`-dependent checks, since the carrier the builder now takes
  has no `.Line` for a needle to sit in. The in-cap plant (`trailPaddedTrailer(0)`,
  needle inside the 512-byte cap at offset 104–146, chosen over the family's
  habitual past-the-cap pad specifically so a record that kept the capped line
  would still be caught) lives one tier down instead, at
  `TestFinGatherReturnsNoCapturedBytes` (`finding_run_gather_test.go`), which
  sweeps the carrier itself. What remains in this file is two channel-independent
  construction rules on `finTrailerRecord`: the per-row `Detail` headroom
  assertion (#1284's fix, argued as a type-level rule that travels — the record
  embeds whole into `finRecordRun.Trailer` and from there into the artifact, so
  a Detail that ate its own budget would defeat the marshal sweep and the
  artifact's file byte sweep two tiers up) and the flat forbidden-key scan
  (`finTrailerRecord` is ten scalars, so a top-level key scan is exhaustive).
  The shell `TestFinTrailerRecordReadsTheDecodedTrailer` is gone; its one
  surviving row — the four scalars come from the full-line decode rather than
  the capped copy — is promoted to top-level as
  `TestFinTrailerSightingScalarsComeFromTheFullLineDecode`, re-stated onto
  `finTrailerSighting` (the builder reads neither `Trailer` nor `Line`) and
  named to mirror `TestFinGatherSightingScalarsComeFromTheFullLineDecode`, the
  two halves of one agreement obligation that a grep now returns together.
  Purely additive at #1290, one new file, 697 lines, zero production change, zero
  consumer call sites. **#1363 added a fifth trailer *field*, deliberately not a
  fifth decoded scalar** — `KeyNames []string` (`json:"trailer_keys"`, no
  `omitempty`), copied through `finTrailerBuild` from a different reader
  (`trailKeyNames` over the full line) than the four scalars above, bounded by
  `finBoundKeyNames` at the fill sites rather than in this builder (which copies
  and computes nothing). Every "ten scalars" / "the four decoded scalars"
  sentence in this file stays true as written; only the record's total field
  count moved. See [`codebase/1290.md`](../codebase/1290.md) for the
  original implementation, [`codebase/1320.md`](../codebase/1320.md) for the
  move onto the carrier, [`codebase/1325.md`](../codebase/1325.md) for the
  retirement, [`codebase/1316.md`](../codebase/1316.md) for the row that
  joins this file's `Bounded` derivation to a poll that genuinely measured it
  (`finding_run_gather_test.go`'s `TestFinGatherRecordPublishesTheMeasuredMissBound`),
  and [`codebase/1363.md`](../codebase/1363.md) for the key-names field.

- `finding_key_name_bounds_test.go` (#1364) — **offline instrument, not a
  probe**; pins all five clauses of `finBoundKeyNames`' doc comment (#1363),
  none of which shipped pinned. Two tests drive hostile fixtures through the
  shipped builders (`trailScan` → `finTrailerSighting` → `finTrailerBuild`)
  and assert both the unbounded reader output and the published, bounded
  names from shipped code alone; two call the helper directly for the two
  clauses no fixture can reach (nil-not-`[]string{}` on empty input; its own
  backing array on every path, including the under-both-bounds fast path a
  fixture can never exercise). The hostile fixtures are deliberately small —
  a few hundred bytes — because `trailScan`'s `bufio.Scanner` buffer aborts
  rather than truncates past its 64 KiB default, and on the aborted arm the
  names field renders `null`, making "the field is bounded" trivially true
  over a fixture that produced no names at all; the drive helper asserts
  `trailer-seen` as a fatal precondition specifically to catch a future
  fixture that grows into that ceiling. Marker-aware: a truncated name
  carries `reachTruncationMarker` on top of the kept bytes, so
  `len(name) <= finTrailerMaxKeyNameBytes` is red against a correct build.
  Purely additive, one new 382-line file, zero production files touched,
  zero existing test files touched — the AC that no inbound line-number cite
  in `internal/` moves holds by construction rather than by argument. Seven
  mutants of `finBoundKeyNames`, run via `go test -overlay`, all RED; 75 PASS
  / 0 SKIP on `-run '^TestFin|^TestTrail'` (71 before). Explicitly left for
  #1362: the Detail-interpolation prohibition (measured and cut — the
  hostile fixture's names-interpolating mutant reaches 444 of a 470-byte
  headroom budget and would ship green over the violation it claims to
  detect) and the artifact-wide containment sweep. See
  [`codebase/1364.md`](../codebase/1364.md) for the full implementation,
  the mutation matrix, and a stale comment in
  `finding_trailer_evidence_test.go:203` left for #1362 to correct.

- `trailer_terminal_reason_test.go` (#1366) — **offline instrument, not a
  probe**; consumes #1357's `KeyNames` reading and answers a question neither
  it nor the decoded scalar can answer alone: what a trailer's
  `terminal_reason` *means*, given the runner path the run was observed to
  take. `trailReasonAgainstPath(runnerReading string, keyNames []string,
  decodedReason string) trailReasonResult` maps onto a closed six-value
  `reason-`-prefixed set — the cross product of {absent, present-and-empty,
  present-and-non-empty} × {streamrunner, ptyrunner, indeterminate}, since
  absence means opposite things on the two runner paths (ptyrunner's trailer
  is pyry's own and owes a non-empty reason by construction; streamrunner
  passes claude's bytes through unchanged, `streamrunner/runner.go:177-179`,
  so a healthy run's trailer is claude's own and owes none at all). Presence
  is read from `keyNames` alone, never from `decodedReason != ""` — taking it
  from the scalar would merge "absent" and "present-and-empty" on the
  owes-one path, since both decode to `""`. The reading is reduced with the
  shipped `finRecordRunnerLabel` (`reachRunnerPathFromArgv` is deliberately
  not used — it keys on a flag both argv builders pass and has no
  `streamrunner` answer at all); the default arm
  (`trailReasonPathUnnamed`) is not the fall-through catch-all
  `trailGate`/`trailAdmitAttribution`/`trailClassifyRun` each refuse, because
  its meaning — "the path reading names no runner" — is true of every
  non-runner label without exception, so there is no out-of-contract case
  left to guard against. The present-on-owes-none value
  (`trailReasonPresentOwesNone`) carries a stated claim limit: the line is
  not that path's documented healthy shape, and *never* that pyry wrote it,
  since claude can produce the same reading through the passthrough. No
  input byte interpolates into any `Detail`, at all — stronger than the
  AC requires, made structural rather than disciplined. Purely additive, one
  new 643-line file, zero production files touched, zero callers on landing —
  **#1420 is its first decision-path caller**, from `trailGate`'s absence
  branch, and **#1433 its second**, from the presence branch; nine
  cross-product rows plus a three-variant
  indeterminate-outranks-a-qualifying-shape sub-test, 0 SKIP on
  `-run '^TestTrail'`. Code review PASS with one non-blocking SHOULD FIX left
  unfixed — the table's closure-reached-set loop keys off the *expected*
  value rather than `got.Value`, so its "catches an unhandled sixth value"
  doc comment overstates what it does (the per-row assertion already covers
  that mutation class). See [`codebase/1366.md`](../codebase/1366.md) for the
  full implementation, the AC-by-AC proof, and both code-review findings.

- `finding_key_name_containment_test.go` (#1362) — **offline instrument, not
  a probe**; proves end to end, over the **files the artifact writer actually
  wrote**, that no value from a trailer line reaches the artifact through
  #1363's published `trailer_keys` field — the gap left by #1364, which
  proved the field's bounds bite but stopped at the reader's own return.
  Four checks, no one subsuming another, driven through a shared
  `finContainRender` helper that scans a fixture line, substitutes it as
  `finWriteInputs().Trailer`, writes the artifact under `t.TempDir()`, and
  returns only the rendered files plus the reader's `[]string` key names —
  never the `trailScanResult` or `*resultTrailer` a first draft returned,
  fixed as a security-review MUST FIX so no failure message in the file can
  print `scan.Line`. AC1 sweeps every written file for a needle planted past
  the 512-byte cap inside `result` and as `session_id`'s whole value,
  excluding the four positions (`subtype`, `is_error`, `terminal_reason`,
  `stop_reason`) the record publishes verbatim by design — planting there
  would fail a correct build. AC2 sweeps the published names field alone
  against a needle in all five string-valued positions (reusing
  `trailKeyNamesNeedledTrailer` at pad 0, the deliberate inversion of the
  reader-tier check's pad 600), the only check that can catch a builder
  copying one of the four by-design positions into the names field. AC3 walks
  both carriers with `finRecordInputReaches` banning
  `map[string]json.RawMessage`, paired with a control reaching `[]string`.
  AC4 plants a needle as a top-level key name and asserts it reaches
  `trailer_keys` but no Detail anywhere in the artifact — discharging the
  names half of the Detail prohibition #1364 left open (the count half has no
  instrument and stays stated-unproven). Also repairs
  `finding_trailer_evidence_test.go:200-204`, false since #1364 cut the
  fixture it described; five comment lines for five, line-count-neutral.
  Purely additive, one new 650-line file, zero production files touched.
  Four mandated mutants plus two run beyond the mandate (a schema-edit
  overlay, since M4 has no one-line form) all RED in the direction the
  matrix predicts; PASS on review with two non-blocking SHOULD FIX (a
  precondition that preempts AC1's sweep on M1 rather than letting the sweep
  itself fire — same fix shape as AC2 already uses; a projected Detail-size
  figure the shipped fixture's own measurement superseded). See
  [`codebase/1362.md`](../codebase/1362.md) for the full implementation, the
  mutation table, and both lessons learned.

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
  covered that slice not at all); a `trailNeedle` sweep over
  every file `os.ReadDir` returns (planted only in inputs the pipeline
  reduces or drops — a matched row's argv, the claude argv, a reap
  outcome's stderr — never in the four fields the record carries whole),
  with a mandated pair of applied-and-reverted
  mutations (one inside the Detail format, one adding an undeclared third
  file) both observed RED before the sweep shipped; a recursive
  forbidden-key scan with two exact-key exemptions (`tool_stderr`, carried
  whole and permitted; `runner_from_argv`, a closed three-constant set with
  no input byte in reach); and a structural + behavioural pair proving
  `resultTrailer` has no `result` member and that the four decoded trailer
  scalars cross into the artifact verbatim while the needle beside them does
  not. The sweep shipped with a fourth channel, a trailer-scan-line plant
  landing **inside** `reachCapCommand`'s 512-byte cap (pad `0`, needle at
  byte 104–146) — `trailNeedle`'s own comment claims it is placed past the
  cap, which this ticket measured to be false against the fixture the
  family actually reuses; the comment was left uncorrected as a sibling
  file, out of scope here. **#1326 retired that fourth channel**: since
  #1320 `finTrailerBuild` takes the sighting carrier, and the needle in the
  scanned line is consumed at fixture-construction time by
  `finTrailerSighting` — which never reads `.Line` — so it never enters
  `finRecordInputs` and the writer performs no reduction there. The in-cap
  fixture (`finWriteTrailerPad = 0`) was kept, not deleted: it still backs
  a diagnosis-and-guard pair relocated onto the pre-build clean check for
  the embedded trailer sub-record (a prospective guard against a future
  builder that starts reading the line) and the four-scalar-vs-needle
  pairing in the verbatim-output test, which rests on the weaker claim that
  the *wire* line carries the needle at every pad regardless of the cap and
  so needs no cap guard of its own. The retired in-cap claim itself now
  holds one tier down, at `TestFinGatherReturnsNoCapturedBytes`
  (`finding_run_gather_test.go`), which sweeps the carrier. Purely
  additive, one new file, 966 lines then trimmed by #1326's prose-and-guard
  rewrite, zero production change, zero consumer call sites. **The fixed
  safety-claim constant (`finWriteSafetyClaim`) was repaired by #1363** when
  the trailer's key names — claude-authored strings, not "a string this rig
  authored" — became a published field: the sentence now names that field
  explicitly and states it is safe because it is bounded and value-free, never
  because it is rig-authored, while keeping the note's two forbidden
  review-caveat strings absent. See
  [`codebase/1286.md`](../codebase/1286.md) for the full implementation, the
  path-vs-name census MUST FIX, and the stale-comment lesson,
  [`codebase/1326.md`](../codebase/1326.md) for the channel retirement, and
  [`codebase/1363.md`](../codebase/1363.md) for the safety-claim repair.

- `finding_run_gather_test.go` (#1281, `PyryExited`/`ClaudeState` promoted
  #1302, trailer-sighting carrier added #1309, carrier's miss bound proven
  #1312, carrier's four decoded scalars proven to come from the full-line
  decode #1313, published record's bound proven to be the classified
  sighting's #1316) — **parameterises
  `trailRigGather` (#1268) on the two inputs it hardcoded.** That rig passes
  a `nil` literal as the reap-log stderr and keys attribution on the test
  process's own process group; under those two hardcodings,
  `trailAdmitProof` — and with it `trailOutcomeRunningAtTrailer`, the only
  outcome that is a finding — is structurally unreachable, so a probe built
  on it would report a clean negative forever with no symptom.
  `finGatherReadings(in finGatherInputs) (trailRunReadings,
  finAttributeRecord, finSighting)` takes `Stdout`, `Needles`, `Stderr` and
  `Pinned` as fields and, driven offline from synthetic stdout/stderr,
  reaches both the finding and a genuine negative
  (`trailOutcomeNoRowMatched`, never a `run-void-*`) through its own
  composition, both at `MatchCount == 0` under a certifying gate —
  demonstrating rather than describing that Step 2 outranks the match-count
  arms. `Pinned` is `[]int`, never `[]reachProc`, continuing #1280's
  credential-channel-closed-by-signature pattern; the `[]reachProc` →
  `[]int` conversion is left to #1282's call site by design. The trailer
  observation is a function-local and never returned, which is what keeps
  `trailScanResult.Trailer`/`.Line` structurally out of the caller's reach.
  A recursive forbidden-key walk (lowercased keys, two named exact-key
  exemptions) closes the flat-only-key-scan gap #1280 left open for nested
  records. `finGatherInputs.PyryExited`/`.ClaudeState` (#1302) are the same
  struct's remaining two fields — copied into the readings whole, no
  default, no repair — and are exercised by two more top-level tests: one
  varying `PyryExited` alone across an identical stdout/needle pair to prove
  the outcome moves (`trailOutcomeNoRowMatched` ↔
  `trailOutcomeVoidPyryDidNotExit`), one carrying a documented verdict, an
  undocumented one, and `""` through unchanged. The third return, `finSighting`
  (#1309), is what the classified poll *measured* — scan state, the bound and
  its discriminator, staleness, a carries-a-decoded-trailer discriminator and
  the four decoded scalars (`Subtype`/`IsError`/`TerminalReason`/
  `StopReason`) — filled from the same `trailWaitForTrailer` call that fills
  `BoundFrom`, so no second scan is needed to recover what the sighting saw.
  It reaches none of `trailObservation`, `trailScanResult` or `resultTrailer`
  (proven by walking types, reusing `finRecordInputReaches` rather than a
  second traversal), so `.Line` and the decoded `*resultTrailer` stay exactly
  as unreachable as before; #1320 moved `finTrailerBuild` onto this carrier,
  via a fixture-side helper (`finTrailerSighting`) that is a copy of this
  file's fill and inherits its agreement obligation.
  Purely additive, zero production change, zero consumer call sites; nine
  top-level tests, 0 SKIP on `-run '^TestFinGather'`. #1312 adds the row #1309
  shipped without: `TestFinGatherSightingReportsTheMissBound` leaves the
  buffer unseeded (this file's first row to do so, and its first to cost wall
  clock — ~600ms), appends the trailer past two poll ticks on a spawned
  goroutine's sibling, and proves `BoundFrom` reports `trailBoundFromMiss` —
  the discriminator that actually bounds something, as opposed to
  `trailBoundFromStart`, which every pre-seeded row reaches and whose own doc
  says it BOUNDS NOTHING. A second, direct `trailWaitForTrailer` call over the
  same buffer supplies the contrast (`trailBoundFromStart`), with only its
  discriminator ever bound to a variable — never the observation itself, which
  carries the two things the carrier exists to keep unreachable. #1313 is #1312's
  sibling half of the #1310 split: a standalone test on a 585-byte over-cap
  fixture (`trailPaddedTrailer(200)`) proves the same carrier's four decoded
  scalars come from `trailScanResult.Trailer` — the full-line decode — and
  never from a re-read of the capped `.Line`, which fails to decode wholesale
  on a syntax error rather than losing fields one at a time. The precondition
  pins the bare `"terminal_reason"` **key** (never the `"max_turns"` value,
  which survives every cap via `subtype`'s `error_max_turns`), asserted so a
  fixture edit that collapses the disagreement fails loudly instead of the row
  going quietly vacuous. #1316 adds this file's second and last row that costs
  wall clock, `TestFinGatherRecordPublishesTheMeasuredMissBound`, placed
  directly after #1312's row: it builds a `finTrailerRecord` from the
  composition's classified sighting over the same unseeded-buffer/delayed-append
  idiom, and puts it beside a record built over the same frozen bytes from a
  second, direct `trailWaitForTrailer` call — joining the record tier (which
  pinned `Bounded` with the discriminator handed in) to the carrier tier
  (#1312, which measured the discriminator but stopped short of publishing it),
  separated by measurement rather than by `finTrailerBuild`'s input type.
  **#1452** adds a seventh field, `RunnerPath string` — the runner-path
  reading, **already reduced by the caller** — so `finGatherReadings` no
  longer types `trailRunnerUnread()` into the gate's `RunnerPath` at its own
  call site. The reduction happens at the call site and never inside the
  gather (`Pinned []int`'s own doctrine, applied to a string):
  `finExitRunProbe` (#1337, below) now passes `RunnerPath:
  tdnRunnerFromArgv(h.Pin.ClaudeCommand)`, so verbatim argv never enters
  `finGatherInputs`. A new pure helper, `finGatherRunnerPath(reading string)
  string`, maps an unstaged `""` to `trailRunnerUnread()` before the gate
  sees it — total and information-preserving (`trailRunnerUnread()` **is**
  `tdnRunnerFromArgv("")`), and a **publication** fix rather than a
  classification repair: `finRecordRunnerLabel("")` already returns `""`, so
  `""` and `trailRunnerUnread()` reach the identical gate decision; what
  changes is whether `trailGateResult.RunnerPath`'s `omitempty` silently
  drops the field. The gather's needle set is untouched — `RunnerPath` is
  never appended to `in.Needles`, proven by a fourth, deliberately abusive
  row (`RunnerPath: os.Args[0]`, itself a matching needle) against a
  `t.TempDir()` baseline rather than a cross-row `os.Args[0]` comparison,
  which this package's 29 `t.Parallel()` calls and five re-exec sites would
  make flaky. A live run reaches `trailGateAbsentOwesNone` — #1417's value,
  unreachable from any shipped gather until now — for the first time; the
  ptyrunner-presence row keeps #1337's recorded finding classified the same
  way. Neither `trailGateCases()` nor `finGatherCases()` grew; the new
  coverage lives in its own table.
  **#1458** adds an eighth field, `PinnedPid pinStateOutcome` — the other half
  of #1440's pinned-pid sighting route, staged the same way `RunnerPath` is:
  reduced at the caller and carried through the gather whole. `finExitRunProbe`
  takes a second `pinReadState` call, over the first entry of `h.Pin.PGIDs`
  (never `h.ClaudePID`, which is claude's own pid and answers the wrong
  route) and after the exit wait rather than at gather time, guarded so an
  empty pinned set (a failed scan) takes no read at all rather than calling
  `pinReadState(0)` — which would answer `pinStateInstrumentFailed` and so
  claim an instrument ran. The reading lands on `PinnedPid` and never
  `Liveness`, proven by a dedicated test rather than left to the type
  checker, since `pinReadState`'s return type matches `Liveness`'s element
  type exactly and an `append` there would compile. Because the classifier's
  `Ordering.Value == ""` guard is single-sided and, at the time,
  `finGatherInputs` carried no field the ordering could arrive on at all, no
  outcome moves: a live run still answers `run-void-sighting-route-not-staged`,
  now from a genuinely half-staged pair rather than a hypothetical one —
  asserted by a second new test comparing whole `trailRunOutcome` values.
  Thirteen shipped comment sites whose truth or stated basis rested on no
  gather staging this input were swept in the same commit, two of them now
  naming **#1459** — the gather-tier captured-bytes sweep this route's opened
  channel still owes — as the ticket that updates them again once that sweep
  lands.
  **#1462** later adds the ninth field, `Ordering trailOrderResult` —
  appended after `PinnedPid`, never mid-struct, so the file's in-body bare
  `:NNN` citation tail (the tax #1452's insertion paid in full) takes no
  displacement — carrying the sighting route's *other* input, produced only
  by `trailCertifyOrdering` at the call site and taken **whole**, never
  narrowed to `.Value`: a hand-built `trailOrderResult{Value:
  trailOrderCertified}` would let everything downstream pass against a
  certification that certifies nothing. **No live caller fills it, and none
  is wired here by design** — the premise `trailCertifyOrdering` needs,
  `holdHeld`, is a fact about a FIFO the *caller* holds, and the one live
  sighting call (`finGatherReadings`'s own) reports its sighting only on its
  third return, after the point a certification would need it; recovering
  the premise by sighting the trailer a second time at the call site would
  degrade the gather's *own* sighting to `trailBoundFromStart`, a
  discriminator whose own doc says it BOUNDS NOTHING. So the half-staged
  pair #1458 shipped is still what every live run brings; driven offline
  through the shipped gather and classifier instead, a certified ordering
  (`trailCertifyOrdering(true, true, true)`, never a literal) beside a
  still-running pinned-pid read reaches `trailOutcomeAliveAtSightingByOrdering`
  for the first time from a gather rather than a hand-built classifier
  fixture, and beside a refuting read reaches
  `trailOutcomeVoidPinnedPidDidNotEstablish` under that refutation's own
  published reason — three new rows, each cross-checked against
  `trailEstablishSighting`'s own answer taken over the readings the gather
  produced, so a row can't agree with a classifier that hardcoded a reason
  the predicate no longer emits. Ten shipped comment sites across both files
  carrying variants of "no gather stages the ordering" were split rather
  than flipped: the carriage half goes false and is corrected, the
  no-live-caller half stays true and is kept, and all five stale **#1457**
  attributions (#1457 is CLOSED, the parent this ticket split from) are
  repaired to name the carriage as landed and the staging as still unowned —
  never repointed to "#1462 stages it," and never a placeholder ticket
  number for the wiring, since none exists yet. No C10 contract check added;
  the no-C10 note's basis is restated, not replaced. Split from #1457;
  security-sensitive (architect self-review PASS); one code-review round,
  PASS with two non-blocking NITs. Purely additive to this file, zero
  production files touched. See [`codebase/1281.md`](../codebase/1281.md),
  [`codebase/1302.md`](../codebase/1302.md),
  [`codebase/1309.md`](../codebase/1309.md),
  [`codebase/1312.md`](../codebase/1312.md),
  [`codebase/1313.md`](../codebase/1313.md),
  [`codebase/1316.md`](../codebase/1316.md),
  [`codebase/1452.md`](../codebase/1452.md),
  [`codebase/1458.md`](../codebase/1458.md) and
  [`codebase/1462.md`](../codebase/1462.md) for the full implementation and
  the mutation-tested lessons.

- `finding_stage_held_group_test.go` (#1282) — **fills `finGatherReadings`'
  (#1281) two parameters from a real held command, not hand-passed
  integers.** `finStageHeldGroup` stages `sh -c '"$1" "$2"; exit 0'` over a
  real `cat` held on a real FIFO, in a process group of its own
  (`SysProcAttr{Setpgid: true}` — copying the wrapper subject shape from
  `trail_run_rig_test.go`, not the flip test's bare `exec.Command`, which
  would inherit the test's own group), then pins that group off a real
  `pinScanArgv` (#1280) match set's `.PGID`s. AC1's distinctness guard
  compares the **scanned** pgid against `syscall.Getpgrp()`, never
  `cmd.Process.Pid` — the pid form is vacuous under the dropped-`Setpgid`
  mutation, confirmed green in code review, while the scanned form reddens
  in 0.06s. The teardown adds a third statement (a direct
  `cmd.Process.Kill()`) that the neighbouring rig's two-statement teardown
  doesn't need, because only this file's guard can redden on a path where
  the group kill finds no group to signal — without it, `t.Fatalf`'s
  `runtime.Goexit()` would deadlock the mutation against `holdProbeFIFO`'s
  `t.Cleanup`. Two tests, five arms: the finding and a genuine negative
  (`trailOutcomeMatchedUnattributed`, one step earlier than #1281's
  `trailOutcomeNoRowMatched` because a real command carries the needle),
  plus #1268's two hardcodings trapped at the **`Admit`** layer against a
  same-staging control, each varying exactly one dimension. First `fin*`
  file whose scan matches live rows, so `readings.Liveness` is non-empty
  for the first time — the neighbour's whole-struct-print licence
  (`finding_run_gather_test.go:105-113`) is deliberately not inherited,
  since its proof ran with `Liveness` empty on every row. Purely additive,
  one new file, 617 lines, zero production change; both new tests PASS,
  never SKIP. See [`codebase/1282.md`](../codebase/1282.md) for the full
  implementation and the grade-mutations-per-line lesson.

- `finding_live_pin_test.go` (#1338) — **offline reduction, not a probe**;
  the pure post-filter a later ticket's during-turn `pinScan` (held `cat` on
  a FIFO, pinned mid-turn) is reduced through before it ever reaches the
  staging record — no live scan, no `ps` exec, no `pyry` spawn, no caller.
  `finLivePinReduce(scan pinScan, fifoPath string) finLivePinReading` takes
  membership from `reachMatchedNeedle` over each row's recorded needle list
  (never a re-scan of `.Command`, which the byte cap may have truncated past
  `reachMaxCommandBytes`), returns every FIFO-matched row and its `.PGID`
  raw — unsorted, undeduped, since the consumer `finAttributeFanOut` (#1280)
  dedupes and sorts internally — and reads claude's own argv via
  `tdnClaudeCommand(scan)` over the whole scan, not the FIFO-filtered rows
  (claude's row carries only the claude needle, so filtering first always
  returns `""`). `finLivePinWantRows = 2` names the expected FIFO-row count,
  sourced from #1230's live measurement (the `zsh -c` wrapper plus the
  forked `cat`) and corroborated, not primarily sourced, from #1268's
  rig-staged mutation test; `trail_run_rig_test.go:563` is deliberately not
  cited, since it asserts only `MatchCount > 1`, never `== 2`. Both
  plausible-wrong fills — `scan.MatchCount` (3, since one scan carries both
  the FIFO and claude needles) and the distinct-pgid count of the FIFO rows
  (1, since claude isolates the Bash command into its own group) — are
  pinned as asserted values in `TestFinLivePinCountIsNeitherWrongCandidate`
  and checked pairwise-distinct from the correct count, so a fixture edit
  that collapses two candidates together fails loudly instead of silently
  disarming the trap. The offline trap drives everything over a synthetic
  four-column `ps` table built as **bytes** and turned into a `pinScan`
  through the real `pinMatchArgvExcluding` (a hand-built `pinScan` would skip
  the match-uncapped/store-capped asymmetry the truncation assertion rests
  on); the wrapper row's padding is derived from `reachMaxCommandBytes`
  itself, never a literal 512. Purely additive, one new file, 514 lines,
  zero production change, zero consumer call sites — the driver and record
  tickets that call `finLivePinReduce` for real land later. See
  [`codebase/1338.md`](../codebase/1338.md) for the full implementation, the
  mutation-tested lessons, and why `strings.Contains(s, "")` being `true`
  makes the empty-needle assertion a real second witness for the
  membership-re-scan defect rather than comment-only work.

- `finding_live_staging_test.go` (#1342) — **declarations, not a probe**;
  the run's FIFO name, hold prompt, staged command literal and env delta a
  later live turn stages from, plus one offline trap per declaration. Exists
  because `finOutcomeStagingGate`'s identity arm
  (`finding_staging_gate_test.go:299`) is byte equality between claude's
  verbatim `input.command` and whatever the rig says it staged — get either
  operand wrong and every *correctly*-staged run reports
  `stage-command-not-staged`, one live claude turn burned per attempt.
  `finLiveStageCommand(fifoPath)` splices `probeHeldCommandName` rather than
  re-typing `"cat"` (a rig staging one verb while #1340's liveness check
  looks for another would drift silently; the splice makes a rename a build
  break) and is deliberately bare, never `finOutcomeHoldCommand`'s
  `sh -c … ; exit 0` stand-in shape. `finLiveStagePrompt(fifoPath)` follows
  `probePrompt`'s backtick-delimited form with the *whole* command
  interpolated, not just the path, so the prompt and the staged literal
  derive from one `fmt.Sprintf` instead of being written twice; the offline
  trap recovers the command back out of the prompt by an independent
  delimiter scan (`finLiveStageCommandFromPrompt`) rather than comparing
  against a hand-copied second literal. `finLiveStageFIFOName =
  "fin-live-stage-hold"` is checked both-directions substring-disjoint
  against all eight shipped FIFO name/path constants, referenced **by
  identifier** so a rename breaks the build instead of rotting the taken-set
  list silently — re-derived at `26d83b7` via
  `rg -n 'FIFOName *=|FIFOPath *=' internal/e2e/realclaude/` (the
  `FIFOPath`-inclusive recipe; a `FIFOName`-only search misses #1338's
  `finLivePinFIFOPath`). `finLiveStageEnvDelta()` names
  `BASH_DEFAULT_TIMEOUT_MS=5000` (the settled #1223 trigger) and
  `PYRY_USE_STREAMJSON=0` explicitly — the latter because
  `reachRunnerPathFromEnv` reads the ambient `os.Getenv` first, so an empty
  delta would make the downstream runner reading a reading of the operator's
  shell; its offline trap sets a hostile ambient (`t.Setenv`) to prove the
  claim is non-vacuous rather than accidentally true whenever the variable
  happens to be unset. Purely additive, one new file, 489 lines, zero
  production change, zero live caller — #1340 is the driver that spends a
  real turn on these declarations. See [`codebase/1342.md`](../codebase/1342.md)
  for the full implementation, the mutation-tested lessons, and the
  reachable-red-vs-shadowed-by-Fatalf lesson code review surfaced on the
  extraction round-trip's pass-through guard.

  **#1349 adds a sibling, `finLiveStageStreamEnvDelta()`** — the same two
  keys with `PYRY_USE_STREAMJSON=1`, two independent literals never derived
  from `finLiveStageEnvDelta()` (a clone, append or wrap would defeat the
  property that an edit to either can't silently change the other). Its
  trap, `TestFinLiveStageStreamEnvDeltaNamesTheRunner`, is a sibling of
  `TestFinLiveStageEnvDeltaNamesTheRunner`, never a copy: the hostile
  ambient is `PYRY_USE_STREAMJSON=0` rather than `=1`, and its control's
  honesty is asymmetric because the truthiness rule is one-sided — only the
  exact string `"1"` is truthy, so a `0` ambient is indistinguishable from
  unset and the control excludes an *effective* ambient of `1` without
  establishing non-vacuity by construction the way the shipped trap's does.
  See [`codebase/1349.md`](../codebase/1349.md).

- `finding_live_assembly_test.go` (#1343) — **the join, not a probe**; the one
  function, `finLiveAssembleStaging`, that fills all eight
  `finOutcomeStaging` fields — three read from the run's transcript via
  `finTranscriptFill` (#1304), five supplied by the caller as
  `finLiveAssembleFacts`, `finTranscriptReading`'s mirror image — and returns
  `finOutcomeStagingGate`'s decision (#1284) as returned, never re-derived.
  Exists because nothing previously called both halves together: the only
  thing filling the five caller-side fields was `finTranscriptStagedCaller`,
  a #1304 test fixture whose hardcoded `PinMatchCount: 1, PinWantCount: 1` is
  wrong for the rig, whose real expectation is `finLivePinWantRows = 2`
  (#1338) — an assembly that inherited the `1` would send every
  correctly-staged live run to `finOutcomePinCountUnexpected`, burning a live
  claude turn per attempt. The composite literal is name-for-name with no
  literal on any right-hand side, which is the one rule that keeps both the
  fixture's `1` and the driver's `finLivePinWantRows` out of the assembly's
  body — the counts are forwarded unaltered, neither re-derived nor fixed
  internally. `facts.StagedCommand` is the single source of the staged
  string, closing structurally (rather than by care) the two-consumer drift
  between the gate's identity arm and the fill's own `call.Command == staged`
  guard. `finLiveAssembleContractWant = finLivePinWantRows + 1` backs a
  deliberate contract row over a want no live driver emits — the only row
  that catches an assembly forwarding the match count while fixing the want
  internally — derived rather than written as a literal so it can never
  coincide with the real constant. Test drives four rows over one
  correctly-staged synthetic transcript, written once in the parent, with
  every assertion reading the assembly's return value rather than
  `finOutcomeStagingGate` directly, so it proves the counts travel without
  re-asserting `finOutcomeGateCases`' (#1284) already-shipped count mapping.
  Two mis-assemblies survive every row by construction — a count swap inside
  the literal, and hardcoding the three transcript fields at their staged
  values — and are stated as accepted in the file's own header rather than
  chased with the duplicate rows this ticket's AC forbade reproducing.
  Purely additive, one new file, 428 lines, zero production change, zero live
  caller — #1340 (driver) and #1337 (record/classification) are the tickets
  that call `finLiveAssembleStaging` for real. See
  [`codebase/1343.md`](../codebase/1343.md) for the full implementation, the
  mutation matrix, and the code-review NIT on the assembly's two adjacent
  `time.Duration` parameters.

- `finding_live_run_test.go` (#1340, parameterised #1349) — **the live
  staging driver, not a probe of pyry itself**;
  `finLiveRunStage(t, envDelta) *finLiveRunHandle` spawns pyry on the
  runner path its caller's `envDelta` selects, holds the rendezvous FIFO, drives the turn to
  the instant a during-turn process pin is meaningful, takes that pin, and
  hands it plus the rig's own facts to #1343's `finLiveAssembleStaging`,
  returning a handle carrying the run's live facts and the staging tier's
  `finOutcomeResult` **as the gate returned it**. Nothing #1338/#1342/#1343
  already shipped is re-derived: the pin reduction and its expected row
  count, the staged command/prompt/FIFO-name/env-delta declarations, and the
  eight-field assembly all cross unchanged. `finLiveRunHandle` is returned as
  a **pointer** — the pyry-exit kill cleanup is registered before `PyryPID`
  exists, so its closure has to read a field written later — and carries
  **no JSON tags**, inheriting the "input only, never published" posture of
  the types it wraps (`Pin.Rows`/`Pin.ClaudeCommand` are verbatim argv off
  the ambient process table). The kill cleanup is registered **before**
  `holdProbeFIFO` so LIFO releases the FIFO first and the kill is
  defence-in-depth rather than the thing that produces the exit — inverting
  that order yields a run that looks identical (green, handle populated)
  while the rig itself produced the exit; copied verbatim, `PyryPID <= 0`
  guard and `// LOAD-BEARING` comment included, from the reach precedent
  (`background_reach_probe_test.go:355-374`), not the comment-less trigger
  copy. **The driver takes its own `tool_use`/`tool_result` wait before
  pinning**, even though the assembly waits internally too — the assembly's
  wait fires strictly after the pin (it takes pin counts as inputs), so
  skipping the driver's own wait pins before the held `cat` exists and fires
  the gate's count arm on a correctly staged run, one live claude turn spent
  finding out. The pin itself is one `ps -axww` scan carrying both needles
  (the FIFO path and `tdnClaudeNeedle`) with two exclusions, handed to
  `finLivePinReduce` unchanged, forwarding `finLivePinWantRows` as the want
  (never `scan.MatchCount`, never `len(Pin.PGIDs)`). No budget-fired run is
  staged — `--max-turns=6` gives the turn room to complete, since a
  budget-fired run's exit code can't discriminate outcomes and its
  `Terminate` hook reaps before the trailer is written. No `ps -E`/`-Eww`
  anywhere; matched rows and claude's argv cross the handle only as the
  already-capped `reachProc.Command`; the file formats no `Detail` and writes
  no artifact. **Ships no test of its own** — its only exercise is
  compilation and `go test`'s vet subset under `make e2e-realclaude`;
  `finLiveRunStage`'s one caller was #1337's live entry point
  (`finding_exit_path_probe_test.go:216`) until **#1353** added a second,
  `finStreamExitRunProbe` — see below. See
  [`codebase/1340.md`](../codebase/1340.md) for the original implementation
  and the code-review SHOULD FIX on a counted `t.Fatalf` claim the shipped
  file falsified.

  **#1349 parameterised the delta and re-derived the doc comment site by
  site.** `envDelta` reaches `spawnProbePyry` verbatim — no default, no
  nil-check, no package-level fallback, since a silent default would hide
  exactly the ambient-environment failure `reachRunnerPathFromEnv`'s own doc
  exists to make visible. The doc comment's argument was ptyrunner-only in
  five places and one was wrong on its own path: the `cmd.Wait` goroutine's
  "claude runs on a PTY" reason covered the one fd claude never shares
  (`cmd.Stderr` is `os.Stderr` on both runner paths, and `creack/pty` fills
  stdio only when nil), not the fd that can actually hold the wait open.
  The replacement separates the rig's own `Wait` (evidence transfers
  unchanged, from the 2026-08-06 live run) from a new stream-path-only
  hazard — pyry's `Wait` on claude's stdout pipe, bounded by
  `cmd.WaitDelay=killGrace` (5s) — whose symptom, if it fires, is a
  distorted `ExitStatus` rather than a hang, for #1353 to meet in the
  comment before it meets it in an exit reading. See
  [`codebase/1349.md`](../codebase/1349.md) for the full site-by-site
  classification, the corrected fd argument, and the stale-citation sweep
  code review caught across all three touched files.

- `dropped_line_capture_test.go` (#1260) — **evidence probe,
  security-sensitive**; opt-in behind `PYRY_PROBE_DROPPED_LINE_CAPTURE=1`.
  Answers what four downstream tickets (#1261–#1264) all needed and nobody
  had ever read: the verbatim payload of every stream-json line
  `internal/streamsup/parser.go` drops on the **interactive** surface (not
  headless — #1218 already proved the two surfaces don't share subtype
  rates). Drives `streamsup.Runner` **in process** and installs
  `dropcapRecorder` in the exact `Config.Stdout` slot production gives
  `streamsup.NewParser` (`cmd/pyry/streamsup_runner.go:117`), so "upstream of
  the parser" is structural; `spawn_shape` is observed from production's own
  `buildArgs` output through a discard-`slog.Handler` on the runner's log
  record rather than transcribed, so the recorded argv cannot drift from the
  shape it claims to measure. Reuses #1223's FIFO-hold lever and #1240's
  interactive staging idioms unedited (`holdProbeFIFO`, `bgIdlePrompt`), but
  is deliberately **not** built on #1240's `bgIdleRecordTurn` — that recorder
  reads decrypted phone frames downstream of the parser, so every line this
  ticket needs would be structurally absent from it. Classification asks the
  shipped parser (`parseOne`) rather than mirroring `ignoredLineTypes`, so a
  future parser change can't silently desync the census from what actually
  ships. Three outcomes (`fired`/`did-not-fire`/`instrument-broken`), only
  the first licensing an absence claim, gated by a pre- and post-rendezvous
  `fifoLiveRead` pair. Redaction is two mechanisms with different fabric: a
  declared substitution table applied to every string that enters the
  record (not just payloads — `fifoLiveOutcome.Path`/`.Detail` and every
  `t.Logf` leak a path with no payload involved), and a fail-closed deny-scan
  over the whole marshalled record as the deterministic net behind it,
  `t.Fatalf`-ing on a hit and writing no file. **Result, committed as
  `testdata/dropped_lines_v2.1.220.json`** (`outcome: fired`,
  `absence_claim_valid: true`): 49 lines captured, 39 dropped —
  `system/init` ×1, `system/thinking_tokens` ×33, `system/task_started` ×1,
  `system/task_updated` ×1, `system/background_tasks_changed` ×1,
  `rate_limit_event` ×1, and the suppressed `user`/`text` harness-nudge block
  ×1 (matched `harnessNoOutputNudge` byte-exactly — the second confirmed
  observation #1247's doc comment asks for before promoting that constant to
  a set, deferred as a follow-up). `task_notification` is named explicitly
  as absent, not silently zero. Code review FAILed once on 3 SHOULD FIX (all
  in the deny-scan's base64 arm and the redaction kept-list prose; no MUST
  FIX, nothing in the committed fixture unsafe), fixed before merge with no
  re-capture needed. Zero production files, zero modified files. See
  [`codebase/1260.md`](../codebase/1260.md) for the full implementation, the
  capture's field-level contents, and the code-review lessons (a deny class
  that carries its own needle; a path that leaks in a slug spelling no
  substitution rule enumerated).

- `finding_exit_path_probe_test.go` (#1337) — **the live entry point that
  reads, classifies and publishes**, on top of #1340's driver;
  `TestRealClaude_ExitPathWhileCommandRuns` stages a turn via
  `finLiveRunStage`, waits for pyry's own exit **in the subtest body, never
  in a cleanup** (`finExitPyryExitDeadline`, 120s, deliberately not
  `probePyryExitGrace` — a different wait on a different clock) with the
  rendezvous FIFO still held, and only then gathers, classifies and writes
  the artifact. `finLiveRunHandle` gains one field, `ExitStatus int` — the
  driver's `cmd.Wait` goroutine used to discard it and no consumer could
  recover a reaped child's status — written **before** `close(pyryExited)`
  and initialised to `pinExitStatusUnknown`; that close is the sole
  happens-before edge, so the field is read **inside** the channel receive
  arm and nowhere else, deliberately with no
  `finExitObservedCode(exited, status)`-style helper, because any shape that
  evaluates the field outside the arm races the goroutine. Staging is
  decided **before** the classifier is consulted:
  `finExitClassify(staging, readings) (string, trailRunOutcome, bool)`
  returns the staging value and the **zero** `trailRunOutcome` unconsulted
  on any non-pass-through staging value — an unstaged run's post-trailer
  argv scan still parses a healthy process table and matches nothing, which
  would otherwise reach the classifier's fall-through answer about a run in
  which no command ever existed. The primary evidence is pyry's own reap
  log (`ReapDescendantGroups` logs only the groups it actually killed, which
  on this path is strictly after the trailer write); the claude-still-alive
  read and the per-pid post-trailer reads are corroboration only, recorded
  as **known blind** (the reap runs between the trailer and claude's
  SIGTERM) and **known late** respectively, and neither overrides an
  attribution. The published artifact carries only the trailer sub-record's
  outcome string, so the classifier's full outcome and the staging result
  reach the operator through `t.Logf` alone — `json.Marshal` or field-by-
  field, never a `%v` on a struct or slice, the mechanism that would
  otherwise print verbatim argv from an ordinary-looking debug line. One
  offline table test, `TestFinExitClassifyConsultsTheClassifierOnlyOnThePassThrough`
  (8 rows, ranged from `finOutcomeValues()`), RED-proved by mutation via
  `go test -overlay` rather than worktree edits. In the dispatch environment
  the entry point skips (exit 0) for want of a Claude login, which is the
  expected outcome and not a finding about pyry. **The operator live run has
  since happened, 2026-08-06 on claude 2.1.220, and it produced a finding:
  pyry declared the turn finished while the command it launched was still
  running.** Classified `run-running-at-trailer` on `admit-proof` — pyry's
  own reap log named the held group under terminal reason `completed`, and
  `emitter.Close()` wrote the trailer before the reap defer reached that
  group, so the group was alive when the trailer was written. The trailer
  read `subtype=success is_error=false terminal_reason=completed
  stop_reason=end_turn`, and the exit code was 0 on a run that completed —
  which is the same 0 a budget-terminated run gives, so `terminal_reason` is
  the discriminator, not the exit status. The predicted systematic lateness
  showed up exactly as designed: 2 during-turn matched rows against 0
  post-trailer matches over 893 rows scanned. See
  [`codebase/1337.md`](../codebase/1337.md) for the full implementation, the
  code-review SHOULD FIX on four citations this PR staled in the same file
  it edited, and the finding.

- `finding_stream_exit_path_probe_test.go` (#1353) — **the headless
  `PYRY_USE_STREAMJSON=1` structural sibling of #1337, and only that path**:
  the two runners do not write the same trailer and do not present the same
  process tree at trailer time, so the sibling file's central argument is not
  merely different here, it is **inverted** — copying it across would ship a
  false claim. `TestRealClaude_StreamExitPathWhileCommandRuns` stages a turn
  via `finLiveRunStage` under #1349's `finLiveStageStreamEnvDelta`, waits for
  pyry's own exit **in the body, never a cleanup** with the FIFO hold still
  held (`finStreamExitPyryExitDeadline`, 120s, deliberately its own constant
  and not `finExitPyryExitDeadline`, for the same asymmetry argument at the
  same value plus one genuinely path-specific leg — pyry's own `cmd.Wait` on
  claude blocks on claude's stdout **pipe**, not just process exit, bounded by
  `streamrunner`'s `WaitDelay`), takes the claude-still-alive corroboration
  read and a **second**, distinct pinned-pid re-read, hands one
  `finGatherReadings` call the observed runner-path reading (reduced once via
  `tdnRunnerFromArgv`, reused at both the gate and the record so the two
  cannot disagree) and the pinned-pid read, then **certifies #1439's ordering
  after the gather and fills `trailRunReadings.Ordering`** — deliberately
  never `finGatherInputs.Ordering`, which #1462 shipped and left structurally
  unfillable by any live caller; this ticket routes *around* that circularity
  rather than resolving it, so the input field's zero and its "no live
  caller" doc both stay true. `finExitClassify` is **reused from #1337's
  probe, not copied**. The one new symbol, `finStreamCertifyOrdering`, passes
  a **structural `true`** (not a guess) as the ordering's `holdHeld`
  premise — `holdProbeFIFO` releases only in the `t.Cleanup` it registers
  itself, and this rig registers none — pinned by a six-row offline trap
  whose one load-bearing clause is that **no row may answer
  `trailOrderVoidUnheld`**; RED→GREEN proven by three `go test -overlay`
  mutants (`holdHeld`→`false`, `== trailSeen`→`!= ""`, transposed premises).
  Because `streamrunner.Run` reaps only inside `cmd.Cancel`, which its own
  comment says never fires on a clean exit, **a healthy run on this path
  writes no reap log at all** — the evidence leg that carried #1337's entire
  verdict is empty by construction here, not merely late, so what stands in
  its place is #1439's/#1440's pinned-pid sighting route: the strongest
  reachable claim is that the command was still running **when the trailer
  was sighted on pyry's stdout** (`run-alive-at-sighting-by-ordering`), which
  is strictly weaker than #1337's aliveness-at-declared-finish claim because
  no `terminal_reason` is certified here and so no declared-finished instant
  exists — the finding sentence states that gap explicitly rather than
  approximating the stronger claim in weaker words. Seven `t.Logf` sites
  publish what the artifact writer's two files don't (staging result,
  classifier outcome, certified ordering, exit timing, observed path vs. gate
  value, corroboration, the finding), every rendering `json.Marshal` or
  field-by-field, **never `%v` on a struct or slice**. One shipped-file edit,
  comment-only and per-hunk line-neutral, corrects `finding_live_run_test.go`'s
  "only caller" claim now that `finLiveRunStage` has two. Security-sensitive
  (architect self-review PASS); one code-review round, PASS with one
  non-blocking SHOULD FIX (a stale "no live run stages the ordering" doc
  claim at `trail_run_outcome_test.go:1243` the spec's file-clearance argument
  missed) and three NITs. **The live measurement itself has not run as of this
  writing** — unlike #1337, where the codebase note was written before the
  operator's live run and updated once it landed, this ticket's automated
  `needs-real-claude` gate removed the label on the strength of the tagged
  suite's generic 652/652 pass, without noticing that
  `TestRealClaude_StreamExitPathWhileCommandRuns` was itself among that run's
  15 *skipped* tests — a gap in the dispatcher's opt-in-probe handling, not in
  this diff. See [`codebase/1353.md`](../codebase/1353.md) for the full
  implementation, that gap recorded precisely, and the exact invocation still
  owed a live run.

- `interactive_change_workspace_test.go` (#1029) — first real-`claude`
  coverage of `change_workspace`, split from #963 (`change_workspace` had
  fake-tier coverage only, #980). Rescoped during refinement: the original
  ask — proving real claude picks up the changed cwd — is unbuildable, since
  no production path reads a stored `conv.Cwd` and spawns with it (a
  session's spawn workdir is fixed at runner construction, and
  `RestartFresh` never re-reads it on respawn). What ships instead is the
  narrower real-tier-only claim: the verb round-trips correctly against a
  **live** supervised claude child, and does not wedge or kill it — a
  scripted fake cannot regress a real child dying.
  `TestInteractiveChangeWorkspace` drives the #1028/#1031 sequential spine
  on one daemon / one seeded bound conversation: turn 1 (liveness, drained
  to `turn_state{idle}`) → `change_workspace` (target minted directly,
  request sent in **tilde form** `~/ws-<nonce>` so the "resolved realpath,
  not the raw request" assertion discriminates on every platform, not just
  where `$HOME` sits behind a symlink) → assert the `conversation_updated`
  reply's `Cwd` is the confined realpath and that it's persisted on disk →
  turn 2 (liveness, proves the verb left the live child undisturbed). The
  turn-2 delta is produced by the child still running at the conversation's
  **original** cwd — a liveness assertion, never evidence of cwd adoption;
  the test's header states this at length so a later reader doesn't "fix"
  it into a hang. Reuses the restored harness (`harness_daemon_test.go`,
  #1473, restoring what #1348 deleted) and the #997/#1028 control-frame
  helpers (`sealEnvelope`, `drainForReply`, `assertConversationUpdated`)
  verbatim; the only new code is `readConversationCwdOnDisk` (~25 lines,
  mirrors #1028's `readConversationIDsOnDisk`). Zero production files
  touched. The fake tier (`relay_v2_change_workspace_test.go` #980) owns
  the confine-reject-no-leak and not-found paths — not duplicated here.
  See [`codebase/1029.md`](../codebase/1029.md).

- `interactive_stream_inband_model_test.go` (#1582) — **live proof of #1581, not
  a new feature under test**: #1581 changed `Pool.UpdateSettings` to deliver a
  model/effort-only change by writing `/model <value>` as an in-band user turn
  instead of killing and respawning the child, and proved that hermetically —
  observing that no respawn happened and that the write was issued, never that
  claude itself changed model. This test supplies the second half. It drives an
  **in-process `sessions.Pool`** (not a daemon subprocess — the only test in this
  package that constructs one) through a turn, `Pool.UpdateSettings`, and a
  further turn, and asserts from claude's own per-turn `system`/`init`
  announcement — read via `inbandTapRecorder`, an `io.Writer` dropped into
  `streamsup.Config.Stdout` **upstream of the parser**, the same seam
  `dropcapRecorder` (#1260) taps — that the reported model changed to the
  requested one and that one process (`ChildPID` unchanged, one `"spawning
  claude"` log record) served every turn. The `init` line is asserted here from
  the tap upstream of the parser, independent of whatever the parser does with
  it downstream. CORRECTED 2026-08-19 (#1600): this used to say the line "must
  never reach a client or daemon event" because `emitSystemSubtype` had no arm
  for `init` — false now, `init` maps to `turnevent.ModelAnnounced` and reaches
  a daemon *event*. The reasoning was already wrong, not just the fact: the
  arm's absence was never entailed by the #833 posture, which is scoped to
  *logs*, and an event is not a log. What #833 still guarantees, unaffected by
  #1600, is that the value reaches no daemon *log* at any level — #1600's own
  arm logs the subtype keyword on its undecodable path and nothing else, ever.
  It still reaches no *client*: `turnbridge.MapEvent`'s `default` drops the
  variant. CORRECTED 2026-08-19 (#1616): this used to close "so no wire frame
  exists for it yet" — false now. `protocol.TypeModelAnnounced` /
  `protocol.ModelAnnouncedPayload` exist, declared by #1616 so a client can be
  written against the shape, the same declared-ahead-of-producer sequencing
  `rate_limited` used (#1405 ahead of #1410). What survives is `MapEvent` having
  no case for the variant: nothing emits the frame until #1617, so a client
  still receives nothing. CORRECTED 2026-08-20 (#1639): that surviving half is
  gone too, and so is the "still reaches no *client*" sentence above it. #1638
  added `MapEvent`'s `turnevent.ModelAnnounced` case and `cmd/pyry`'s matching
  `Handle` case, so the frame is emitted and an interactive v2 client does
  receive one — #1617 was the split parent and never shipped the mapping. The
  *log* half is the one that has never expired: the value still reaches no
  daemon log at any level. An event is not a log, and a wire frame is not a log
  either. No `--model` in the base argv
  (a respawn's
  recomposed argv would otherwise carry two); the starting model is read off
  turn 1 rather than assumed, so the two-alias target table (`haiku`/`sonnet`)
  always has a candidate that differs from whatever a given machine's default
  is. Two assertion pairs, each the sole red for a distinct mutant, both proven
  by measured evidence runs recorded in the file's header: A1/A2 (model changed)
  red under a `-overlay` mutant dropping the in-band `/model` send, green
  (vacuously) on the pre-#1581 tree since a respawn also changes the model;
  A3/A4 (no teardown) red on the pre-#1581 tree (`b047b9e^`, 2 spawns), green
  under the mutant. Zero production files touched. Code review: one round, PASS,
  one non-blocking SHOULD FIX (the post-`UpdateSettings` settle wait's target
  result count is hardcoded rather than captured relative to the count at that
  moment — a low-probability false-red path on a >45s first turn, left as a
  follow-up rather than fixed on this branch). See [`codebase/1582.md`](../codebase/1582.md).

- `set_permission_mode_probe_test.go` (#1595) — **does the bypass posture have
  an in-band form, the way #1581/#1582 proved the model does?** Four direct
  `exec.CommandContext("claude", …)` children (not a `sessions.Pool`, mirroring
  `permission_protocol_spike_test.go`'s shape, not #1582's), each driven through
  an identical two-turn Bash probe: `revoke` and `enable` write a
  `{"type":"control_request",…,"subtype":"set_permission_mode"}` line on the
  held-open stdin between the two turns (the same control-channel
  `(*Runner).Interrupt` already writes to, but whose `control_response` it never
  reads — this test does); `control_default` and `control_bypass` run the
  identical sequence with no control request, as the behavioural baseline each
  measurement arm is judged against at turn 2. Verdict is computed from turn-2
  behaviour, never the echoed `control_response` alone, per the ticket's AC:
  an echoed `success` that still behaves like the bypass control would be
  recorded as a FAILED revocation. Measured 2026-08-19 against claude 2.1.220:
  **revoke succeeds in-band (`bypassPermissions → default`, no respawn, all
  three reads — response, `init` echo, behaviour — agree); enable is refused**,
  with a third error string not previously read out of the binary (`Cannot set
  permission mode to bypassPermissions because the session was not launched
  with --dangerously-skip-permissions`). Four fixtures under `testdata/`
  (`set_permission_mode_v2.1.220_<arm>.json`), named to fall outside both
  `permission_protocol_regression_test.go`'s `fixtureGlob` and
  `dropped_line_capture_test.go`'s `dropcapFixtureGlob` — a live-free sibling
  test (`TestRealClaude_SetPermissionMode_FixtureNamesAvoidRegressionGlobs`)
  pins that deterministically, no subprocess or credentials required. Zero
  production files touched; no writer for the subtype is added — #1596 decides
  that. See [`set-permission-mode-inband-probe.md`](set-permission-mode-inband-probe.md)
  for the full measurement writeup and [`codebase/1595.md`](../codebase/1595.md).

- `interactive_stream_inband_bypass_revoke_test.go` (#1622) — **live proof of the
  composed path #1604 built, not of the wire format #1595 already proved.** #1595
  hand-wrote the `set_permission_mode` control line onto four
  `exec.CommandContext` children it owned directly; #1604 proved `Pool.UpdateSettings
  → inBandDeliverable → deliverSettingsInBand → Runner.RevokeBypass` only through a
  fake runner. Neither proved pyry's own `Pool`, holding a real `streamsup.Runner`
  over a real claude child, actually emits those bytes. This test drives that: an
  in-process `sessions.Pool` (#1582's shape, re-pointed) whose bootstrap child gets
  its bypass posture from a **seeded registry entry** (`yolo:true`) rather than the
  base argv — `revokeBaseArgs` is declared empty on purpose, because either shortcut
  (a stored posture already matching the update, or a bypass flag baked into the
  base argv) yields a run that measures a child nobody revoked. Two guards cover
  that seam: a pre-spawn check on `Pool.DefaultSettings()` and a post-turn-1 check
  that the first `init.permissionMode` is `bypassPermissions`, so a seed failure
  reads as itself rather than as a delivery failure. The stdout tap
  (`revokeTap`) bridges #1582's line-splitting `io.Writer` shape with #1595's
  `setModeRecorder` field capture (`control_response`, `permissionMode`, results)
  by embedding rather than re-deriving the classifier — the one type in the package
  that reads `control_response` off a `Pool`-spawned child's raw stdout. A sibling
  log recorder retains `deliverSettingsInBand`'s fire-and-forget "not delivered"
  `Info` record verbatim, since that record is otherwise swallowed and nothing else
  in the daemon's ordinary logs distinguishes a working revocation from a dropped
  one. Measured 2026-08-19 against claude 2.1.220, all three runs `-race`, mutants
  applied via `-overlay` (no mutated source ever written to the worktree): green run
  — one `control_response`, `init.permissionMode` `[bypassPermissions default]`, one
  spawn, pid unchanged, 5.85s; **M1** (drop the revoke-detection clause from
  `deliverSettingsInBand`) — no `control_response`, `init.permissionMode` stays
  `[bypassPermissions bypassPermissions]`, one spawn, pid unchanged, 50.03s — proves
  nothing was written and nothing was torn down; **M2** (`inBandDeliverable` returns
  false for a revoke, the pre-#1604 shape, falling through to `sup.Restart`) — no
  `control_response`, `init.permissionMode` still flips to `[bypassPermissions
  default]`, but two spawns and pid changes, 50.57s — the row that earns the
  four-assertion set, since the permission-mode echo alone cannot tell a delivered
  revocation from a respawn under a recomposed bypass-free argv. Both mutant runs
  take ~50s against the green run's 5.85s because no `control_response` ever
  arrives, so the tolerated wait burns its full budget — working as intended, not a
  hang. **Scope boundary, deliberate**: asserts the revocation reached the child and
  nothing was torn down, not that the posture is behaviourally enforced — an echoed
  permission mode is claude's own report, not proof of enforcement; that
  measurement is a sibling ticket that consumes this harness. One signature
  widening in `interactive_stream_inband_model_test.go`: `inbandSendTurn`'s
  parameter is now the `inbandResultCounter` interface (`resultCount() int`)
  instead of the concrete `*inbandTapRecorder`, so both this file's `revokeTap` and
  #1582's recorder satisfy it with zero call-site edits. Zero production files
  touched. See `docs/specs/architecture/1622-live-pool-bypass-revocation.md` for
  the full design and the assertion-to-mutant mapping.

- `inband_bypass_revoke_arms_test.go` (#1651) — **the deterministic, credential-free
  half of #1643's three-arm substrate: which stored posture each arm launches
  with.** #1622's `seedBypassRegistry` wrote `yolo:true` unconditionally; it now
  takes the posture as a parameter (`seedBypassRegistry(t, path, yolo)`), and
  #1622's own call site is unchanged (`true`). The trap this ticket exists to
  avoid: for the `false` posture, a **stored** false and a **cold start** (no
  registry file at all) both read back as `YOLO: false` through
  `Pool.DefaultSettings()` — that seam cannot tell a correctly-seeded
  `control_default` arm from a completely broken one. The only signal that
  separates them is the registry **entry's existence** (`bootstrap: true`, an
  `id` `sessions.ValidID` accepts), so `TestSeedBypassRegistry_StoresRequestedPostureUnderBothValues`
  reads the seeded file directly rather than the Pool, decoding through
  `map[string]json.RawMessage` — never `revokeSeedEntry` or `registryEntry` — so
  a `yolo` key misspelled in the seed can't round-trip through its own struct and
  hide from the test. `revokeSeedEntry.YOLO` keeps its `json:"yolo"` tag with no
  `omitempty` (unlike `registryEntry.YOLO`'s `json:"yolo,omitempty"`) precisely so
  a stored false is a **present** false key on disk, not an absent one; adding
  `omitempty` to "match" production would make the key vanish, and only the
  test's presence clause (checked separately from the value) reddens on it — a
  value-only check passes, since a struct decode of an absent key is
  indistinguishable from a decoded `false`. The second new test,
  `TestPoolRevokeArms_PinLaunchPostureAndUpdateByName`, pins the package-level
  `poolRevokeArms` table (`revoke`/`control_default`/`control_bypass`, each
  carrying its launch posture and whether it takes a mid-run settings update) by
  **name**, in both directions — deliberately not "the two controls differ",
  since swapping `control_default` and `control_bypass` still leaves them
  differing while making the arm names lie to every downstream consumer.
  `poolRevokeArms` is read-only by convention (ranged over from `t.Parallel()`
  tests in this file and by #1652); nothing appends to or reassigns it. Both
  tests report **PASS**, not SKIP, with no claude binary and no credentials — the
  file takes neither `WithWorktreeAuthenticated` nor `resolveClaudeBin`, enforced
  by a new `finOfflineExecBans` entry over the file's AST rather than its prose.
  **Two lessons surfaced while mutating the presence/value split**, reported
  candidly against the spec's own prediction rather than silently patched: a
  misspelled `yolo` tag reddens the presence clause on **both** rows, not the
  spec-predicted value clause on the `true` row — because the value check is
  nested inside the presence check's `else`, and a nested assertion is never the
  sole red for a mutant that trips its guard; the guard is. And the map-decode
  itself is not the "vacuous value-only check" the spec's hazard prose describes
  for a **struct** decode: `json.Unmarshal(nil, &b)` over an absent key's `nil`
  `json.RawMessage` errors rather than silently decoding to `false`, so a
  map-based value check alone would have caught the vanished key too — the
  separate presence clause earns its place on readability and naming the real
  cause, not on being the only thing that reddens. Zero production files
  touched. See
  `docs/specs/architecture/1651-bypass-seed-posture-and-arm-table.md` for the
  full design, and #1622's entry above for the seam this ticket parameterizes.

- `inband_bypass_revoke_names_test.go` (#1661) — **locks #1643's fixture-name
  family out of #1595's committed one before the live run that could collide
  exists.** #1643's three arm names (`revoke`/`control_default`/`control_bypass`)
  are the same strings #1595 already uses, and #1595's `setModeFixtureName` is
  package-level and reachable — reusing it on the same claude version would
  silently overwrite three of #1595's four committed fixtures while every test
  stayed green. `poolRevokeFixtureName` is a pure two-string namer whose
  `pool_revoke_` prefix is a literal neither input can reach, with **both**
  inputs (not just the version, unlike the #1595 precedent) run through
  `versionSlug` so a hostile arm like `a/b` can't escape containment either —
  an unslugged-but-equally-pure counterfactual namer escapes containment on 40
  of 110 measured pairs, so that property is load-bearing, not green by
  construction. **The property this file exists to prove is a negative claim,
  and a negative claim needs its control anchored the same way it's checked**:
  `fixtureGlob`/`dropcapFixtureGlob` are matched with a `testdata/` prefix,
  #1595's family glob is matched bare, and getting either direction wrong makes
  the "no minted name matches" loop pass unconditionally with nothing checked.
  `anchorFixtureName` is the single function both the negative loop and each
  row's control call, so the two can't drift apart — proven by mutation:
  flipping the family row to `underTestdata: true` produced **zero** reds from
  the negative loop and reddened only its control, i.e. the control was the
  sole detector for a mis-anchored pattern going silently vacuous. Code review
  flagged one residual, left for #1662 rather than fixed here (closed there —
  see below): the file's header claims it can't reach any `os` read or write,
  but its `finOfflineExecBans` entry enumerates four verbs
  (`os.ReadFile`/`WriteFile`/`Create`/`ReadDir`), so `os.OpenFile` and a
  `writeFixture`-shaped third `packageDir` wrapper sit outside the ban table's
  actual coverage — true of this file today (it imports no `os`) but a gap for
  whatever #1662 adds next to this package. Zero production files touched. See
  `docs/specs/architecture/1661-pool-revoke-fixture-name-family.md` for the
  full design and the anchoring table.

- `inband_bypass_revoke_fixture_test.go` (#1662) — **the write half of #1643's
  three-arm substrate: the fixture record one arm commits, and a writer whose
  target directory is a parameter.** `poolRevokeFixtureRecord` carries exactly
  the eighteen fields #1643 can fill — no `env` field, inherited from
  `setModeFixtureRecord`'s constraint, since the credential reaches the child
  through the environment while the argv carries none — and
  `writePoolRevokeFixture` mints its target filename by passing the record's
  slugged version token and arm **unmodified** into #1661's
  `poolRevokeFixtureName`, never formatting its own name. `capFixtureCapture`
  reuses #1595's `stderrFixtureCap`/`truncateString` and adds a rune-boundary
  trim: `encoding/json` substitutes U+FFFD per invalid byte rather than
  erroring on bad UTF-8, so a plain byte cap over a capture cut mid-rune reads
  back over the stated cap — measured, a five-byte cut string round-trips at
  seven bytes. Two lessons surfaced during mutation testing, both reported
  candidly against the design's own predictions rather than silently
  absorbed: **a reused helper's own guard can make the new wrapper's guard
  unpinnable** — `capFixtureCapture`'s `len(s) <= cap` early return is
  measurably dead for the value path, because `truncateString` already
  carries the identical guard and its trim loop breaks immediately on a valid
  tail, so dropping the wrapper's own early return reddens nothing; the
  function's doc comment says so rather than claiming coverage it doesn't
  have. And **`json.MarshalIndent` reflows an embedded `json.RawMessage`**, so
  a `control_response` envelope written and read back is not byte-equal until
  both sides are compacted first — which blinds only that whitespace and
  still catches a dropped field, a `json:"-"` tag, or a wrong-tag decode. The
  round-trip fixture's arm (`"revoke arm/2"`) is deliberately not
  slug-clean: every real arm name and slugged version token already passes
  `versionSlug` unchanged, so a writer that formats its own name instead of
  minting through `poolRevokeFixtureName` would produce the identical name
  and AC 2's name-equals-namer assertion would be vacuous — this is the one
  literal choice that keeps that assertion coupled to the write path.
  `finOfflineExecBans`' entry for the file carries a fourth wrapper beyond the
  `packageDir`/`setModeFixturePath`/`writeSetModeFixture` trio —
  `writeFixture`, the spike's own third `packageDir` wrapper — closing the
  residual #1661 flagged and left open (above). Code review also flagged,
  non-blocking, that "a field decoded from the wrong tag" — carried verbatim
  from the design into the file's header as something non-zero, distinct
  values catch — overstates it for a symmetric struct round trip: only a
  **colliding** tag is caught (`encoding/json` drops both); a unique wrong tag
  round-trips green. Worth remembering for any future file in this family
  that reuses that phrasing. Zero production files touched. See
  `docs/specs/architecture/1662-pool-revoke-fixture-record-and-capped-writer.md`
  for the full design and the mutation-to-assertion table.

- `interactive_stream_model_announced_test.go` (#1634) — the live proof that a
  real claude's announced model reaches the daemon's **own emitted frame**,
  not just claude's stdout. #1582's `interactive_stream_inband_model_test.go`
  taps `streamsup.Config.Stdout` upstream of the parser, so it stayed green
  through the whole period `turnbridge.MapEvent` had no
  `turnevent.ModelAnnounced` case and would stay green if #1638's two arms
  were reverted; `TestInteractiveStreamModelAnnouncedFrame` instead drains the
  sealed `protocol.TypeModelAnnounced` frame at a connected fakephone and reds
  against a tree missing either arm. Reuses #1582's `inbandModelTargets` table
  as the equality comparand (never a fresh literal) paired with an inequality
  against the spawn alias — the discriminator that tells a future stale-row
  failure (inequality green) apart from claude regressing to announcing the
  bare alias (inequality red). Carries `spawnBootstrapDaemonVerbose`, a
  `spawnBootstrapDaemonWithIdle`-style near-copy of the shared spawner whose
  sole delta is `-pyry-verbose`: both plausible leak sites for the value
  (`streamsup`'s undecodable-line drop, `Handle`'s unknown-event default) log
  at Debug, so at the shared spawner's default `LevelInfo` the no-leak
  assertion would pass vacuously. Drain returns at the turn's
  `turn_state{idle}` close rather than at the frame's arrival — under either
  mutant no frame is ever emitted, so a frame-first drain would burn its full
  budget on every red run; closing on turn-end keeps a mutation proof in this
  package cheap (~5s red vs. the 120s timeout).

  **Two lessons that outlive this ticket:**
  - **`go test -overlay` cannot mutate this package's daemon-side targets.**
    AC-3's mutants live in `turnbridge.MapEvent` and `cmd/pyry`'s
    `interactiveTurnEmitterV2.Handle`, but this test asserts against a
    *separately built* daemon binary — `ensurePyryBuilt` shells out to a
    plain `go build` with no overlay forwarding, and the `cmd/pyry` mutant is
    never compiled into the test binary at all. An `-overlay` passed to
    `go test` reaches neither mutant and both mutant runs come back green — a
    false pass, not a weak one. The route that works: `go build
    -overlay=<abs path json> -o <tmp bin> ./cmd/pyry`, then
    `PYRY_E2E_BIN=<tmp bin>`, which `ensurePyryBuilt` returns unbuilt without
    rebuilding. Any future mutation proof of daemon-side (as opposed to
    test-process-side) code in this package needs this route, not the house
    `-overlay`-into-`go test` shortcut used elsewhere in the suite.
  - **A no-leak haystack can contain the value it's guarding against for an
    unrelated reason.** The daemon's `spawning claude` record logs the argv
    verbatim, `--model haiku` included — an absence assertion searched
    against the spawn alias rather than the drained frame's resolved `Model`
    value fails on every healthy daemon. Search for the value that actually
    crossed the wire, never the value that was asked for.

  Zero production files touched.

- `session_transcript_probe_test.go` (#1655) — measures whether a `claude`
  launched under `--session-id <id>` that runs no turn leaves an `<id>.jsonl`
  on disk, the premise a suspected `streamsup` crash-loop (2026-08-18) rests
  on and [ADR 032](../decisions/032-bootstrap-resume-per-spawn-existence-probe.md)
  needs before #1630 can carry its by-id-existence rule into `streamsup`.
  `TestRealClaude_TurnlessSessionIDTranscript` runs a control arm (one turn;
  the transcript's appearance pins the sessions directory empirically and is
  compared against `sessions.DefaultClaudeSessionsDir`) and a turnless arm
  read twice — while alive, and again after a `SIGTERM`→grace→`SIGKILL`
  termination — through `classifyTurnlessTranscript`, which reads the
  termination mode so a force-killed absence can never be recorded as the
  fact holding. **Measured HOLDS** (claude 2.1.220): full record and
  reproduce steps in
  [`session-transcript-and-resume-probe.md`](session-transcript-and-resume-probe.md).
  Credential-free companion `TestTurnlessTranscriptVerdict` pins the
  classifier's five outcome rows offline.

  **Two lessons that outlive this ticket:**
  - **A `*bytes.Buffer` behind a live `exec.Cmd` cannot be read while the
    child is still running.** The liveness `t.Fatalf` path reads the
    turnless arm's stderr with the child still alive, racing `os/exec`'s own
    copy goroutine under `-race`. A mutex-guarded `boundedBuffer` is needed
    regardless of the separate ingest-cap requirement — a plain capped
    buffer still races on this read.
  - **`agentrun.ResolveWorkdir` returns `(string, error)`, not a bare
    string.** It wraps `fs.ErrNotExist`; a caller that drops the error can
    set `cmd.Dir` on a workdir that no longer exists and silently invalidate
    any directory comparison built on it.

  Zero production files touched.

- `resume_absent_transcript_probe_test.go` (#1656) — measures the other half of
  #1655's premise: how claude answers `--resume <id>` when `<id>.jsonl` is
  absent, the half the suspected `streamsup` crash-loop actually turns on.
  `TestRealClaude_ResumeAbsentTranscript` establishes a real transcript
  `<A>` through one turn, pre- and post-reads a reserved absent id `<C>`
  through the same by-id instrument, then runs both a `--resume <C>` arm and
  a `--resume <A>` control arm — same builder (`resumeProbeArgs`), same
  workdir, same 45 s deadline — through `classifyResumeAbsent`, which reads
  the control **first**: a control that itself rejects a resume of an
  *existing* transcript short-circuits to INCONCLUSIVE regardless of what
  the absent arm did. **Measured HOLDS** (claude 2.1.220): the absent arm
  exited 1 (`No conversation found with session ID: …`, carried on both
  stdout and stderr) while the control sat on stdin past its deadline. Full
  record and reproduce steps in
  [`session-transcript-and-resume-probe.md`](session-transcript-and-resume-probe.md).
  Credential-free companions `TestResumeAbsentVerdict` (all nine
  `{exit 0, exit non-zero, did-not-exit}²` cells) and
  `TestResumeProbeArgsIsRespawnShape` (the respawn argv differs from the
  first-spawn argv only in the trailing id-flag pair) run with no claude at
  all.

  **Two lessons that outlive this ticket, both about classifying a
  terminated child's exit code:**
  - **A did-not-exit outcome needs a liveness guard, not just a code
    comparison.** `snapshotExit` (from #1655) returns `-1` for a child that
    has not exited, and `-1 != 0` — a "was it rejected?" predicate written
    as `ExitCode != 0` alone reads a child still sitting on stdin as a
    rejection. The predicate here is `Exited && ExitCode != 0`, and the
    offline table's did-not-exit rows deliberately use `ExitCode: -1`
    (mirroring `snapshotExit`'s real sentinel) rather than a conveniently
    zeroed field, so a dropped guard shows up as four reds, not zero.
  - **Terminating an arm before snapshotting it manufactures the verdict.**
    `SIGTERM` leaves exit 143 behind, indistinguishable from a rejection at
    read time. The fix is ordering, not a special case: snapshot the
    pre-termination exit code first, call `endTurnlessChild` only on the
    did-not-exit path, and route the post-signal code into a cleanup field
    the classifier never reads. The same hazard applies to any future probe
    in this package that classifies a child's exit code after it may have
    been signalled.

  Zero production files touched.

- `initialize_control_names_test.go` (#1696) — **the fourth fixture-name lock in
  this package, and the first for a single-input namer.** #1688 will spend live
  tokens capturing claude's `initialize` `control_request`/`models` round trip;
  this ticket mints the filename those bytes land under —
  `initialize_control_v<slug>.json` via `initControlFixtureName`, reusing
  #1661's `poolRevokeNamePattern` row type and `anchorFixtureName` — and proves,
  with no claude binary and no disk I/O, that no minted name can join
  `fixtureGlob`, `dropcapFixtureGlob`, or #1595's `setModeFamilyGlob`.
  `poolRevokeFixtureName` (#1661) carries an arm parameter because #1643 had
  three arms; this capture has one, so the namer takes one input and the
  lock table collapses to tokens × 1. Registered in `finOfflineExecBans` with
  #1661's list plus `writeFixture`, `captureClaudeVersion`, and
  `os.LookupEnv` — the middle one because it is the package's own
  `claude --version` exec and returns exactly this namer's input, making it
  the exec a developer touching version tokens is likeliest to reach for.

  **Two lessons that outlive this ticket:**
  - **Collapsing an input dimension can silently empty the hazard shape a
    lock measures.** #1661 covered path-separator escape through its
    `hostileArms` list, not its token list; #1643's arm names were the
    separator-bearing inputs, not its version tokens. Dropping the arm
    parameter here was the ticket's size win, and it also removed nearly
    every `/`-bearing input from the table — a token list of plausible
    `claude --version` strings plus `..` and `""` leaves the whole file green
    against a namer that never calls `versionSlug` (confirmed by mutation:
    the raw-interpolation mutant reddens only on tokens carrying `/`, and
    `..` is not among them — unslugged, it mints a clean
    `initialize_control_v...json`). When a split drops a dimension a
    predecessor used to cover a property, re-derive which inputs the
    surviving assertions still redden on; don't inherit the predecessor's
    table and assume the coverage came with it.
  - **`-overlay` cannot verify a check that parses source at run time**, and
    this is a different reason than #1634's daemon-side overlay gap above.
    `TestFinOfflineFilesReachNoExecHelper` calls `parser.ParseFile` with a
    `nil` source, so it reads the registered file off disk at test run time;
    `-overlay` is a build-time mapping consumed by the `go` command and
    never interposes on the test binary's own reads. A banned call injected
    via overlay compiles cleanly while the check parses the unmodified file
    and stays green — a misleading pass reading as "the ban does not bite."
    Verifying a ban entry in this file needs a real edit and a real revert,
    confirmed byte-identical against the pristine file before committing;
    `-overlay` stays correct for the three ordinary compiled-code namer
    mutants above it. Any future AST-parses-off-disk check in this package
    inherits the same caveat.

  Zero production files touched. See
  `docs/specs/architecture/1696-initialize-capture-fixture-name-lock.md` for
  the full design and the per-mutant table. The write half is #1702 (#1697,
  named here at the time, was superseded and closed before it was built).

- `initialize_control_record_test.go` (#1701) — **the record half of the
  `initialize` fixture family: fixes the JSON contract #1688's live capture,
  #1690's decoder and #1692's fake all read, and pins the fixture standing in
  for it against the two ways it could degenerate silently.**
  `initControlFixtureRecord` carries the 22 fields the capture needs (no
  `env` field; the 18 shared with `setModeFixtureRecord` carry that record's
  JSON tags and Go types unchanged), and `initControlFullRecord` returns a
  **fresh pointer per call** rather than a package-level `var`, so #1700's
  parallel subtests mutating the record are not a `-race` data race. The
  hand-written `initControlFixtureFields` listing is what #1702 zips against
  both sides of its round trip instead of restating the field set; its
  length is asserted against the struct's `reflect` field count, so a field
  added later with no row reddens instead of going silently unchecked.
  Registered in `finOfflineExecBans` with #1696's seventeen names copied
  whole — this file performs no I/O in either direction, unlike #1702's
  writer, which is why the two files need separate, differently-scoped
  entries rather than one shared one.

  **One lesson that outlives this ticket:**
  - **A duplicate-name row in a hand-written listing reddens more than the
    property it was written to test, so a mutant table has to be checked by
    failure *message*, not by which subtest went red.** Listing `argv` twice
    while dropping `prompts` was predicted to redden only the
    listing-covers-every-field subtest's uniqueness pass. It also puts two
    identical `[]string` rows into the same-typed-fields-distinct
    comparison, so that subtest fires as collateral — while the length check
    inside the first subtest stays green the whole time, still counting 22
    rows. Two subtests firing where one was predicted is invisible if you
    only read red/green; it shows up only in what each failure message says
    caused it. Any future mutant table in this family that predicts "exactly
    one subtest reds" needs its message read, not just its count.

  Zero production files touched. See
  `docs/specs/architecture/1701-initialize-capture-fixture-record.md` for
  the full design and the per-field distinctness-group table. The write half —
  the writer and the round trip that reuses this file's record and listing —
  is `initialize_control_writer_test.go` (#1702), described next.

- `initialize_control_writer_test.go` (#1702) — **the write half of the
  `initialize` fixture family: the directory-injectable, atomic writer for
  #1701's record, and the offline round trip proving no field is dropped on
  the way.** `writeInitControlFixture` mirrors #1662's
  `writePoolRevokeFixture` step for step — `dir` parameter, `os.MkdirAll`,
  name minted from `out.ClaudeVersion` (never `ClaudeVersionRaw`, and never
  self-formatted) through #1696's `initControlFixtureName`, `MarshalIndent`,
  `.tmp` write, `os.Rename` — and caps `stderr_capture` on a shallow copy
  (`out := *rec`), whose doc comment states the caveat explicitly: the copy
  shares every slice header with the caller, so it is safe only because the
  sole capped field is a `string`; a future writer that caps a slice-valued
  field would be mutating the caller's backing array through a copy that
  looks defensive. `compactInitControlRawRows` normalises the record's three
  raw-JSON-bearing rows (`control_request_sent`, `control_responses`,
  `stdout_events`) by **Go type**, not by name — a `json.RawMessage` case and
  a `[]json.RawMessage` case — so a fourth raw-JSON field added later to
  #1701's record is picked up automatically; the round trip pins that
  type-scoped selection separately, asserting the touched-name set equals
  exactly those three, so a normaliser silently widened or narrowed still
  reddens even though the type switch itself never needs editing. Registered
  in `finOfflineExecBans` with the same twelve names as #1696 and #1701 carry
  minus their five I/O names (`filepath.Glob`, `os.ReadFile`, `os.WriteFile`,
  `os.Create`, `os.ReadDir`) — this file, unlike its two siblings, performs
  real directory I/O (a write, a read-back, a directory listing), so those
  five stay available rather than banned; the relative-path hazard that
  leaves closed for the siblings is closed here instead by the exactly-one-
  entry assertion, which goes to zero entries if a write escapes to the real
  `testdata/`.

  **Two lessons that outlive this ticket:**
  - **The zero-entry arm of an exactly-one-entry assertion doesn't require
    writing into the committed `testdata/`.** The spec's prescribed mutant
    for "writer ignores `dir`" was to join a relative `testdata/` path,
    which lands a real file in the repo and needs a manual delete plus a
    `git status` check to verify cleanly afterward. Redirecting the writer
    to a *second* `t.TempDir()` instead hits the identical
    `len(names) != 1` branch and leaves the worktree untouched — worth
    reaching for whenever a mutant's only hazard is where its bytes land,
    not what they are.
  - **`%v` over a `json.RawMessage` row prints a decimal byte dump, not
    JSON.** `json.RawMessage` implements `MarshalJSON` but not `String`, so
    `%v` in the round trip's mismatch message renders a ~300-byte envelope
    as `[123 34 116 ...]`. Kept for consistency with #1662's sibling, and
    the row *name* still carries the diagnosis so the test isn't weakened —
    but a future file in this family that wants a readable raw-JSON diff has
    to type-switch at the print site; the row's `any` type can't use `%s`
    without mangling the int and bool rows alongside it.

  Code review flagged, non-blocking: the touched-set guard's failure message
  names two causes (normaliser widened, normaliser narrowed) but not the
  third the type-switch design itself predicts — #1701 grows a fourth
  raw-JSON field, the switch picks it up correctly and automatically, and
  the hardcoded three-name `wantTouched` reddens against an honest writer
  and an honest normaliser. The fix is one more clause in the message, not a
  design change, and was not applied in this ticket.

  Zero production files touched. See
  `docs/specs/architecture/1702-initialize-capture-fixture-writer-and-round-trip.md`
  for the full design and the twelve-mutant table. This closed the
  `initialize` fixture family's complete/atomic half; bounded closed in #1700,
  below, which adds a fourth test to this same file and updates the file's own
  COMPLETE/ATOMIC/BOUNDED self-description and in-file pointers accordingly.

- **`initialize_control_writer_test.go` (#1700 — bounded, same file as
  above)** — `TestInitControlFixture_WriterCapsStderrCapture` proves the
  `capFixtureCapture` call on the bytes the writer actually leaves **on
  disk**: an over-cap ASCII row asserted exactly at `stderrFixtureCap`, and an
  over-cap multi-byte row (its byte at the cap is a UTF-8 continuation byte,
  enforced by a `t.Fatalf` vacuity control on the literal itself) asserted as
  a range plus a prefix check. Each row also confirms the caller's record
  came back unmutated. Mutation-verified via `go test -overlay` (no worktree
  writes): the cap-omitted, direct-`truncateString`, over-trim, and
  caps-the-caller's-record mutants each have exactly one sole-red instrument
  among the two rows and the no-mutation check.

  **Two lessons that outlive this ticket:**
  - **A "sole red" claim from the spec is still worth re-measuring, not
    trusting.** Re-running the mutant matrix surfaced that the multi-byte
    row's *prefix* clause reddens alongside its *range* clause on the
    direct-`truncateString` mutant — the substituted U+FFFD is not in the
    original capture, so both clauses fire together. The row carries two
    independent discriminators, not one; a future simplification to a single
    length check would silently drop one of them.
  - **A hand-rolled `perl -pe 'script' -0777 file` mutation one-liner can
    silently no-op.** Perl only consumes flags that appear *before* the
    script argument, so `-0777` placed after it is read as a filename
    instead, and the "mutated" file comes back byte-identical to its source
    — a false negative indistinguishable from a dead assertion once the test
    still passes. `diff -q` each generated mutant against its source and fail
    loudly on a match; that check is what caught it here.

  Zero production files touched. See
  `docs/specs/architecture/1700-initialize-fixture-writer-cap.md` for the
  full design and the five-row mutant matrix.

- `initialize_control_probe_test.go` (#1688) — **the live run that closes the
  `initialize` fixture family: one real child, one tool-free probe turn, one
  `control_request` with subtype `initialize` on the held-open stdin, the
  reply written through #1702's writer into
  `testdata/initialize_control_v2.1.239.json` (committed).** Reuses
  `setModeRecorder`, `setModeWaitFor`, `setModeTurnLine` and
  `setModeResponseIDMatches` from `set_permission_mode_probe_test.go`
  wholesale; ports none of that file's arm/verdict machinery (`probeOutcome`,
  `setModeFieldMatches`, `setModeDirections`) since this run has one arm and
  no control. Measured against claude 2.1.239: `subtype:"success"`, 6
  `models` entries, also carrying `commands` (51, for #1683) and `agents` (6).

  **The `control_response` payload nests one level deeper than
  `streamsup/parser.go`'s documented shape accounts for.** That shape records
  `subtype`/`request_id` inverted under `response` relative to the request —
  true, and `initControlSummarize` reads it there — but the actual payload
  (`models`, `commands`, `agents`, `account`, `pid`, …) is nested a further
  level, under `response.response`. `internal/streamsup` never parses past
  `subtype`, so its own documented shape was never wrong; it was just not the
  whole shape a payload-reading caller needs. Any future code that decodes
  this control-reply's payload — #1690's decoder, #1693's model-list
  producer — reads `response.response`, not `response`. The verbatim capture
  is `initControlFixtureRecord.ControlResponses[0]` in the committed fixture.

  **Two lessons that outlive this ticket:**
  - **A "check both placements" instruction, derived correctly from one
    known fact, can still be one level short — and the computed field built
    on top of it will report the wrong answer while looking internally
    consistent.** The spec derived two placements (top level, under
    `response`) from `streamsup`'s documented `subtype`/`request_id` shape.
    The first live run of this file read only those two, and recorded
    `models_present:false` against a reply that carried six models — a
    `false` that had nothing pointing back at it, because the run had
    otherwise passed cleanly (a `control_response` arrived, stdout was
    non-empty). What caught it was reading the produced fixture's raw
    `control_responses` bytes rather than trusting the summary field they
    were supposed to justify. For any field a summariser computes by walking
    a shape nobody has fully decoded yet, diff the summary against the raw
    bytes it summarises before trusting a green run — a green run only
    proves the gates it checks, not the fields it computes.
  - **A credential guard scoped to the surface named in the design is not
    the same as a credential guard scoped to the surface the ticket
    commits.** The spec's security review enumerated argv, env and stderr as
    the credential-bearing surfaces and closed the first two by construction;
    `initControlScrubbed` guards the third. But every byte this family
    commits is claude's **stdout**, and no deterministic check runs over it —
    the PR's clean bill came from two independent human reads of the
    committed JSON, not from code. `dropped_line_capture_test.go`'s
    `dropcapScanner` already exists in this package for exactly this (scans
    arbitrary bytes for credential values and operator-path classes, and
    records which classes it checked so "no hits" stays distinguishable from
    "never ran") — a live-capture test that writes stdout-derived bytes to a
    committed fixture should run it over the marshalled record before the
    write, not rely on a human `grep`. Deferred to #1694, which inherits this
    driver and is the family's next live run.

  Code review also flagged, non-blocking: the `!= "null"` guard in
  `initControlSummarize` — the one thing distinguishing `"models":null` from
  `"models":[]`, which the function's own doc comment says is exactly what
  #1690 needs — has no test row pinning it (confirmed by mutation: dropping
  the guard leaves the summariser's test green). Worth a row before #1690
  starts decoding against this shape.

  Zero production files touched. See
  `docs/specs/architecture/1688-initialize-control-round-trip-capture.md` for
  the full design and security review. This closes the `initialize` fixture
  family opened by #1695's split (#1696/#1701/#1702/#1700); the trigger-design
  questions (send point, session perturbation) are carved out to #1694.

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
- Ticket [#1337](https://github.com/pyrycode/pyrycode/issues/1337) — live entry point measuring whether `pyry agent-run`'s ptyrunner default reaches its normal exit path while a claude-auto-backgrounded command is still running; composes #1338/#1340/#1342/#1343 without re-deriving any of them, adds `ExitStatus` to #1340's `finLiveRunHandle`; split from #1305, blocked by #1340; codebase note at [`codebase/1337.md`](../codebase/1337.md).
- Ticket [#1349](https://github.com/pyrycode/pyrycode/issues/1349) — parameterises #1340's `finLiveRunStage` with the staging delta (previously hardcoded) and ships `finLiveStageStreamEnvDelta()`, the `PYRY_USE_STREAMJSON=1` sibling of #1342's delta, so #1353 can stage a probe on the headless stream path through the existing rig; re-derives the driver's doc comment site by site for a caller passing either delta, correcting the `cmd.Wait` goroutine's fd argument in the process; no live turn staged, no production code touched; split from #1237; codebase note at [`codebase/1349.md`](../codebase/1349.md).
- Ticket [#1415](https://github.com/pyrycode/pyrycode/issues/1415) — offline pin of the composition `trailGate` → `trailAdmitAttribution` → `trailClassifyRun` under a ptyrunner reading, closing the gap between #1373's `RunnerPath` field and the two shipped tests that come close but don't claim this (`TestTrailRunComposesWithGateCases` runs every row on the indeterminate reading; `TestTrailGateIgnoresTheRunnerPathExceptAtTheAbsenceArm` drives all five readings but only over `trailGate`, comparing each row against itself); nine assertions, nine `go test -overlay` mutations, 1:1; zero production files; split from #1368 ← #1351 ← #1237; codebase note at [`codebase/1415.md`](../codebase/1415.md).
- Ticket [#1419](https://github.com/pyrycode/pyrycode/issues/1419) — splits `trailGate`'s empty-`terminal_reason` arm into an absent-key arm and a present-and-empty arm, so the published `Detail`'s scoped `NO LIVE REPRO EXISTS` claim stops being false of the shape every healthy `PYRY_USE_STREAMJSON=1` run produces (claude's own `result` line, no `terminal_reason` key at all); presence is read from scan-produced `KeyNames` membership, never the decoded value or cardinality — both mandated mutations verified sole-red under `go test -overlay`; repairs the two shipped empty-reason fixtures (were hand-built with `KeyNames` unset, i.e. absence-shaped under the new reading) to scan-produced records and adds a ninth `trailGateCases()` row so `TestTrailGateIgnoresTheRunnerPathExceptAtTheAbsenceArm`'s carriage totality is restored by coverage; adds a key-name leak rung to the no-captured-bytes sweep (the security review's MUST FIX) — SHOULD FIX left open, sole-red truncation blind spot when an echo lands in the last ~32 bytes of the absence Detail's 512-byte cap; no closed set grows, no consumer gains an arm; split from #1416; codebase note at [`codebase/1419.md`](../codebase/1419.md).
- Ticket [#1420](https://github.com/pyrycode/pyrycode/issues/1420) — splits `trailGate`'s absence arm three ways (absent on a path that owes one, absent on a path that owes none, absent on a path naming no runner) by calling the shipped `trailReasonAgainstPath` on the branch the gate's own key-name read has already decided is absent — its first decision-path caller; all three still answer `trailGateOutOfContract` certifying nothing, so `trailGate` moves from eight return sites (four out-of-contract) to ten (six out-of-contract) with no closed set growing; the gate's own prose is rewritten rather than appended to, since 480 B (shipped) + 264–286 B (embedded) overflows the 512-byte cap and truncates 1–5 bytes past each case's value marker — the three rewritten Details land at 461/460/468 B, each checked for both length and absence of the truncation marker on the output; `TestTrailGateIgnoresTheRunnerPathExceptAtTheAbsenceArm` (renamed) exempts only the one absence-shaped row's `Detail` byte comparison via a declared `pathVaries` field, keeps clause B and the value/`Reason` comparison running unconditionally, and a companion sub-test proves the exempted arm positively varies across all five readings; four mutants of `trailReasonAgainstPath`'s label switch each sole-red under `go test -overlay`; nine doc blocks (plus a tenth in a sibling file) asserting "no arm reads the runner path" corrected, and six `#1374` attributions repointed to "unowned" now that #1374 is closed NOT_PLANNED; split from #1416, blocked by #1419; codebase note at [`codebase/1420.md`](../codebase/1420.md).
- Ticket [#1417](https://github.com/pyrycode/pyrycode/issues/1417) — admits the one absence sub-case (#1420's split) that is a healthy reading rather than a caller's bug: absent `terminal_reason` on a path the observed reading reduces to `streamrunner` now reaches `trailGateAbsentOwesNone`, a sixth gate value certifying no reason; the owes-one and path-unnamed absence sub-cases and present-and-empty are unchanged and still `trailGateOutOfContract`; `trailClassifyRun` gains a matching step-1 arm, `trailOutcomeVoidPathOwesNoReason` (a twelfth outcome, a void — distinct from `trailOutcomeVoidNoTrailer` because a trailer *was* written, and from `trailOutcomeOutOfContract` because this is a reading, not a defect); both closed sets (union map, membership predicates, every switch over the gate's values including the non-certifying-value fatal in `TestTrailGateThenAdmit`) grow in the same commit, since an unhandled gate value would fall through step 1's default-less switch to steps 3–8 and award a scan-side answer about pyry from a record the gate says certifies nothing; `TestTrailGateNamesWhichAbsenceCaseFired`'s R2 row is amended from a shared precondition into the discriminator, gaining mutants M5–M9 (each a sole-red row, verified under `go test -overlay`); the new gate arm's prose is rewritten within a 470-byte ceiling (512 − the 42-byte leak needle) rather than appended to; unreachable from either shipped live gather today (both fill `RunnerPath` with `trailRunnerUnread()`), and no comment added claims otherwise; two code-review FAIL rounds on an abandoned then partial cross-file cite-renumbering sweep (79 then 2 stale cites, unrelated to the ticket's substance) before a clean PASS; split from #1368 ← #1351 ← #1237, blocked by #1419 and #1420; security-sensitive (architect self-review PASS, two SHOULD FIX both discharged); codebase note at [`codebase/1417.md`](../codebase/1417.md).
- Ticket [#1433](https://github.com/pyrycode/pyrycode/issues/1433) — the #1420 step for the **presence** side: a `terminal_reason` present and non-empty on a path the observed reading reduces to `streamrunner` (owes none) now reaches an eleventh return site answering the shipped `trailGateOutOfContract` and certifying nothing, instead of falling through to `trailGateUsable` under a Detail that cited ptyrunner's `emitter.Close()`/reap defer on a path where neither exists; `trailGate` moves from ten return sites (five out-of-contract) to eleven (six); called via `trailReasonAgainstPath`'s `trailReasonPresentOwesNone` answer (the reduction's second decision-path caller), placed after the budget arm (structural priority: a `max_turns` trailer keeps reaching it) and before the present-and-empty branch (kept path-invariant deliberately — the reduction's one absorbing value can't express the blank-vs-absent `NO LIVE REPRO EXISTS` distinction); Detail cites the 32 B case constant rather than embedding the reduction's 395 B Detail (445 B measured against a 470 B ceiling); `TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt`'s `pathVaries` exemption widens to cover the certified `Reason` — the usable row is the first whose certification itself moves with the reading — repaid by a positive per-reading companion; no closed set grows, no amendment to `trailClassifyRun`; one code-review FAIL/rework round on a cite regression the sweep itself introduced (fixed by naming the file explicitly rather than leaving a bare line-number cite); split from #1427 ← #1369 (closed NOT_PLANNED); security-sensitive (architect self-review PASS); follow-up **#1434** promotes the shape to a gate value of its own; codebase note at [`codebase/1433.md`](../codebase/1433.md).
- Ticket [#1434](https://github.com/pyrycode/pyrycode/issues/1434) — the #1417 step for the **presence** side: promotes #1433's arm from certifying nothing under the shipped `trailGateOutOfContract` to a gate value of its own, `trailGatePresentOwesNone` (a seventh gate value, still certifying no reason), and gives `trailClassifyRun` a matching step-1 arm and run outcome, `trailOutcomeVoidReasonNotOwedByPath` (a thirteenth outcome, a tenth void) — because the presence-side reading, like the absence-side one #1417 promoted, is genuinely what `streamrunner.Run`'s tee-parse passthrough produced (`internal/agentrun/streamrunner/runner.go:177-179`) and not a caller's bug; `trailGateOutOfContract`'s enumerated sub-cases drop six → five and `trailGate`'s eleven return sites now split five/six instead of six/five; both closed sets (union map 37 → 39, membership predicates, every switch over the gate's values) grow in the same commit, since step 1's default-less switch would otherwise fall an unhandled value through to steps 3-8 and award a scan-side answer about pyry from a record the gate says certifies nothing; the new value's doc carries the same claim limit #1433 already wrote — **never** that pyry wrote the line, cited to the passthrough as the reason claude's own output can produce the same reading, and never a claim about whether a process was alive; the composition test's vacuity guard is a controlled experiment (two tails that provably disagree under a usable gate both collapse to the new outcome once the gate answer is swapped), demonstrated rather than asserted, and the "arm deleted" mutant is shown under `go test -overlay` reaching `run-matched-not-attributed` — a scan-side answer about pyry from a record certifying nothing; Detail re-measured at 427 B against the same 470 B ceiling (down from #1433's 445 B once its "a value of its own is #1434" closing clause came out); two code-review rounds (FAIL → PASS) on the family's repeat failure mode, bare `(:NNN)` cites resolved by last-named-file instead of by named symbol — both misses were correct on `main` and moved anyway; no production files touched; split from #1427 ← #1369 (closed NOT_PLANNED), blocks #1428 (the reason × reading matrix); security-sensitive (architect self-review PASS); codebase note at [`codebase/1434.md`](../codebase/1434.md).
- Ticket [#1439](https://github.com/pyrycode/pyrycode/issues/1439) — ships `trailCertifyOrdering(trailerSighted, pyryExited, holdHeld bool) trailOrderResult`, a pure offline predicate that certifies a staged probe run's three instants (trailer sighted, pyry exited, pinned pid re-read) were ordered by construction, or refuses with the name of the one missing premise, as a fourth `order-`-prefixed value space (`trailOrderCertified` plus one void per premise, deliberately no out-of-contract value since three bools admit eight readings by construction); precedence `hold → sighting → exit` (the hold's failure removes the *subject*, not merely an endpoint — pid reissue risk) is chosen on the same structural-outranks-situational rule `trailAdmitAttribution` uses and checked by all eight fixture rows, proven load-bearing under a `go test -overlay` guard-reorder that reddens exactly the four multi-failure rows; joins the union map as a fifth space (39 → 43) rather than a new closure test, since the realistic mistake is cross-space (`order-void-pyry-did-not-exit` sits one word from #1271's `run-void-pyry-did-not-exit`); makes no claim about any command's liveness — that composition is #1440's, the sibling consumer ticket, and `trailRunReadings`'s hold field is #1437's, both deliberately out of scope here; no production files touched; two code-review rounds (FAIL → PASS) — round 1 killed a false "structural" claim about a shared Detail-format constant with a mutant (nothing asserted its presence) and caught four collateral cite breaks the +18-line union-map insertion caused in files this PR never opened, one a bare chained ref no filename-anchored grep could see; round 2 caught the rework's own hand-off note misattributing several pre-existing `docs/knowledge/codebase/` cite staleness as this PR's damage — left untouched by this documentation pass accordingly; security-sensitive (architect self-review PASS); split from #1436 (closed); codebase note at [`codebase/1439.md`](../codebase/1439.md).
- Ticket [#1440](https://github.com/pyrycode/pyrycode/issues/1440) — the consumer half of #1436's split: ships `trailEstablishSighting(ordering trailOrderResult, pin pinStateOutcome) trailSightingResult`, a pure offline predicate that decides whether a command was ALIVE WHEN ITS RUN'S TRAILER WAS SIGHTED (never "when pyry declared the turn finished" — no such instant exists on this path) from #1439's certified ordering plus a pid re-read after pyry's exit, filling the evidence gap left by `streamrunner` writing no reap log at all on a clean exit; both parameters are the shipped records **whole** rather than bare value/verdict strings — the opposite enforcement direction from #1439's bare-boolean input, needed here because `pinStateOutcome.ToolStderr` is a live raw-`ps`-stderr capture route and a bare-string parameter would make the no-captured-bytes sweep unbuildable; three outcomes (`trailSightingEstablished` = `sighting-alive-by-ordering`, deliberately far from the shipped `run-running-at-trailer` collision it states the same claim as, from a different evidence class) and five reasons, one precedence rule (an uncertified ordering outranks a failed pid read — a premise failure is never read as evidence about the command) and one totality rule (`pinStateInstrumentFailed` and any off-space verdict fold into one reachable `default:` arm, both meaning "nothing was measured", never a clean negative); no `PID` field on the record, the pid reaches a reader only through `Detail` as `%d`; joins the union closure map as a sixth space (43 → 51) rather than a new closure test; delivered 1017 lines against the spec's ~750 and the ticket's ~697 estimate on the same authoring pattern as #1439's 618; one code-review comment miscounted six PR-caused stale-cite sites (its own +6/+38 shift of `trailer_admissibility_test.go`, no rework needed) as six findings and tripped FAIL, corrected in a follow-up same-PR comment to PASS/one-SHOULD-FIX once the repo's N-sites-of-one-omission calibration was applied — every technical finding stood; **discovered mid-build:** the no-captured-bytes sweep's marshalled-key walk cannot see a `command`-shaped field added via `omitempty` before something populates it, closed by a second half walking the record type's own json tags; no production files touched; security-sensitive (architect self-review PASS); split from #1436 (closed), sibling #1439; codebase note at [`codebase/1440.md`](../codebase/1440.md).
- Ticket [#1446](https://github.com/pyrycode/pyrycode/issues/1446) — reaches #1440's shipped-but-unwired sighting route from the one arm it exists to serve: `trailClassifyRun`'s `trailGateAbsentOwesNone` case previously answered `trailOutcomeVoidPathOwesNoReason` unconditionally, before any evidence was examined, because #1440's route was wired to nothing and every step past step 1 is structurally unreachable for a gate value that certifies nothing; the arm now calls `trailEstablishSighting(readings.Ordering, readings.PinnedPid)` from inside itself — never by falling through to the scan-side steps below — and where it establishes aliveness reports `trailOutcomeAliveAtSightingByOrdering` (`run-alive-at-sighting-by-ordering`, the second answer and the only one from an evidence class other than the reap log), never `trailOutcomeRunningAtTrailer` (`trail_ptyrunner_composition_test.go` passes unamended); `trailRunReadings` gains `Ordering`/`PinnedPid` as whole records (narrowing either would leave no route for the no-captured-bytes needle to travel) and `trailRunOutcome` gains `Route` (`evidence_route`, `trailRouteReapLog` / `trailRouteSighting`, empty elsewhere) so a reader never infers the evidence class from the outcome value — the two answers state nearly the same English sentence from different evidence, which #1440's own header calls its central risk; deliberately **no** C10 contract check over the two new fields, since no shipped gather stages either and validating them the way C7 validates `Liveness` would file every run that exists today as `trailOutcomeOutOfContract` — the argument is a comment beside the contract block, not a check; the void arm's Detail *exchanges* its out-of-contract clause for the route's answer (22 B headroom, not enough for an addition) rather than growing; joins the four-answer set as `trailOutcomeAliveAtSightingByOrdering`'s carrier of `trailDeclaredFinishInstantClause` (`trailRunCertifiesNothingArms()`, four now) since it is the one arm publishing a positive finding from a gate that certified nothing; value space fourteen outcomes (four answers, ten voids), union map 51 → 54; **two rework rounds**, the first three stale line cites of the bare/symbol-anchored class the spec flagged as trap #1, the second a leak-sweep hole — the arm has two reachable outcomes (finding, void) and the no-captured-bytes sweep's first draft planted its needle on the finding side only, so `PinnedPid.ToolStderr` (the field the architect's own security review names as the trust boundary) survived every test on the void side; fixed with a third fixture and measured, not asserted, with a substitution-form mutant that avoids a truncation-marker false-kill; security-sensitive (architect self-review PASS); split from #1444 (itself split into #1446/#1447/#1448, wired natively so neither later ticket reaches an architect until this one closes); codebase note at [`codebase/1446.md`](../codebase/1446.md).
- Ticket [#1447](https://github.com/pyrycode/pyrycode/issues/1447) — splits the sighting route's *measured non-establishment* off #1446's blanket void: where `trailEstablishSighting` measured a pinned pid and answered `trailSightingUnestablished` (the pid read did not establish aliveness at the trailer's sighting), the `trailGateAbsentOwesNone` arm now answers a value of its own, `trailOutcomeVoidPinnedPidDidNotEstablish` (`run-void-pinned-pid-did-not-establish`, a fifteenth outcome, an eleventh void), rather than the same `trailOutcomeVoidPathOwesNoReason` a route having measured *nothing* or never having been staged still shares — those two stay #1448's; inherits `trailSightingUnestablished`'s claim limit verbatim (a statement about this evidence route, never that the command had exited before the sighting — the re-read is late by construction) and joins `trailRunCertifiesNothingArms()` as its fifth carrier of `trailDeclaredFinishInstantClause`; publishes `Route = trailRouteSighting` on the new value — the first void to carry a route — which falsified and corrected four prose claims that a route was set "on the finding alone" (`Route`'s own doc, `trailRouteSighting`'s doc, the route space's header, `wantRoute`'s doc), rewritten as "published exactly where the route's own measurement decided the value"; no default arm added, the new branch sits between the established case and the `trailSightingVoid` fall-through so the switch stays total by construction; sibling void's Detail deliberately does **not** grow a "kept apart from" clause naming the new value (461 B rendered / 51 B headroom, a 37-byte value needs 60-80 B to name honestly) — the separation is discharged in the two values' own docs and in the new arm's Detail instead, which is budgeted against the *longer* of two reasons that reach it (`sighting-reason-pid-reaped-pending`, 10 B longer than `sighting-reason-pid-gone`) and measured at 492 B / 20 B headroom on the shipped format string, not a draft (an earlier draft measured 536 B and silently truncated its closing claim-limit clause — the row that makes that worst case fail the build rather than truncate quietly, `trailSightingPin(pinStateExitedNotReaped)`, is the mutation-proven sole killer of a build keying the branch on the raw pin verdict instead of `sighting.Value`); registered in `trailIsRunOutcome`, `trailRunOutcomeValues`, the union closure map, `trailRunCertifiesNothingArms`, and a six-file count-literal sweep (three files outside the ticket's own three: `finding_staging_gate_test.go`, `finding_exit_path_probe_test.go`, `finding_attribution_fanout_test.go`); no production files touched, purely additive; security-sensitive (architect self-review PASS); one code-review FAIL round on the cite-renumbering pass — two classes of remap bug (a bare `:NNN` ref resolved through the wrong file's line map by last-named-file inheritance rather than by the symbol it named, and a four-hunk file's cumulative shift captured from only one of its hunks, undercounting by 3-5 lines across eight sites) — both classes were staleness this PR created at exact base-commit cites, not inherited debt, fixed by a machine-checked two-pass verification (pair each cite to its base-tree counterpart on normalised prose, then re-run an arithmetic git-diff line map) that took 11 mismatches to 0, then PASS with two unrelated single-literal fixture drifts (`duration_ms`, an array-element `pgid`) flagged as landing in the implementation commit rather than the auto-commit — both behaviour-neutral, left as named nits below the rework threshold; split from #1444, blocks #1448; codebase note at [`codebase/1447.md`](../codebase/1447.md).
- Ticket [#1448](https://github.com/pyrycode/pyrycode/issues/1448) — separates the two cases #1447 still left sharing `trailOutcomeVoidPathOwesNoReason`: the route was never staged (every run that exists today) versus the route was staged, consulted, and measured nothing (an ordering premise failure or an unanswered pid read). A guard on `readings.Ordering.Value == ""`, tested *before* `trailEstablishSighting` is called, is deliberately single-sided — read off the two reason constants' own docs, since `trailSightingReasonPidReadFailed` already names the unfilled pin's zero `""` as a correct member of its space while `trailSightingReasonOrderingUncertified` names only #1439's three `trailOrderVoid*` values, so only the ordering side needed a guard at all; an `&&` guard would have been worse than redundant, publishing "premise failed" on a half-staged pair. The never-staged case gets a value of its own, `trailOutcomeVoidSightingRouteNotStaged` (`run-void-sighting-route-not-staged`, a sixteenth outcome, a twelfth void), with `Route`/`RouteReason` both `""` — argued, not defaulted: no evidence class produced the verdict, its absence did. The remaining fall-through keeps `trailOutcomeVoidPathOwesNoReason` and now also publishes `Route`; a new field, `trailRunOutcome.RouteReason` (`evidence_route_reason`, `omitempty`), carries `sighting.Reason` through whole on both sighting-void arms so a reader separates a measured premise failure from an unanswered pid read — the two verdicts the route itself returns identically — from the published bytes rather than the Detail's prose; published invariant is a biconditional, `RouteReason != "" ⟺ Route == trailRouteSighting`, checked on every row (`trailAdmitResult`, the reap-log route, has no reason space and can never publish one). `trailRunCase` gains a `wantReason` column, since two rows reach the same value under two different reasons and a value-keyed map cannot express that. Detail budgets rendered, not read (`go test -overlay`): the 461 B / 51 B pre-split arm becomes 453 B / 459 B post-split, both *shorter* than the arm they replace, and neither interpolates `PinnedPid.Verdict` or `Ordering.Value` — the former prohibited explicitly, since `PinnedPid` is the one input this classifier has no contract check over. Two new fixtures drive the real producers (`trailCertifyOrdering`, `trailSightingPin`) rather than hand-built literals, and joins `trailRunCertifiesNothingArms()` as its sixth carrier — a *second* set the same commit grew that the architect's own count-cascade table did not enumerate; security-sensitive (architect self-review PASS); one code-review FAIL round on the tail of that second-set sweep (three files still said "five" after the headline fifteen→sixteen cascade was fixed), then a rework NIT extending the `PinnedPid.Verdict` needle plant to both remaining sighting-void arms (mutation-proven: both die on the leak assertion, not the truncation marker), then PASS with one unrelated bare-cite staleness the rework's own insertion introduced, named but below the rework threshold; split from #1444, closes the #1446→#1447→#1448 chain; codebase note at [`codebase/1448.md`](../codebase/1448.md).
- Ticket [#1452](https://github.com/pyrycode/pyrycode/issues/1452) — routes the runner-path reading `finExitRunProbe` already derives (`tdnRunnerFromArgv(h.Pin.ClaudeCommand)`, reduced at the caller's own call site) into `finGatherReadings`' gate call, replacing the constant `trailRunnerUnread()` that pinned every live run to the gate's path-unnamed out-of-contract case; `finGatherInputs` gains a seventh field, `RunnerPath string`, reduced by the caller and never inside the gather (`Pinned []int`'s conversion-site doctrine, applied to a string, so verbatim argv — and an operator's `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` — never enters the struct); a new pure helper, `finGatherRunnerPath`, maps an unstaged `""` to `trailRunnerUnread()` as a publication fix (`omitempty` would otherwise silently drop `runner_path`), not a classification repair, since `finRecordRunnerLabel("")` already routes `""` and `trailRunnerUnread()` to the same gate decision; a live stream-path run reaches `trailGateAbsentOwesNone` — #1417's value, the sole live entry to the #1439→#1440→#1446→#1447→#1448 evidence route, unreachable from any shipped gather before this ticket — for the first time, while the ptyrunner-presence row keeps #1337's recorded finding classified the same way; the gather's needle set and both `trailGateCases()`/`finGatherCases()` stay exactly as sized, proven by a new four-row table asserted against the shipped gate's own output over each row's own reading rather than a typed-in `trailGateResult`, a needle-non-reachability test using a deterministic `t.TempDir()` baseline plus an abusive `os.Args[0]` row (a cross-row `os.Args[0]` comparison was rejected during the architect's own security self-review as flaky in a package with 29 `t.Parallel()` calls and five re-exec sites), and a captured-bytes sweep run for the first time under a reading derived from real argv rather than the empty-argv answer; `finStageRun` and `trailRigGather` are both unchanged by design (the former supplies nothing and still degrades honestly, the latter runs no claude process and so has no producer for any reading); 11 shipped comments across 5 files asserting "both gathers supply the not-read reading" or "a live run always reaches the path-unnamed case" corrected, plus the `finGatherInputs` field-count claim in 3 more; security-sensitive (architect self-review PASS, one MUST FIX on a flaky test design fixed pre-ship); two code-review rounds (FAIL → PASS), both entirely on the cite-renumbering tail (2 cites re-pointed the wrong way, 4 half-bumped lists) rather than substance, fixed in a comment-only 6-file follow-up commit and independently re-verified bidirectionally on re-review; two pre-existing, unrelated cite mis-attributions found and filed as follow-up #1455 rather than fixed here; codebase note at [`codebase/1452.md`](../codebase/1452.md).

- Ticket [#1458](https://github.com/pyrycode/pyrycode/issues/1458) — stages the pid-read half of #1440's pinned-pid sighting route: `finGatherInputs` gains an eighth field, `PinnedPid pinStateOutcome`, appended at the end after `RunnerPath`; its doc names the one admissible producer (`pinReadState`, over a pid from the caller's own pinned set, read after pyry exited — never `trailSightingPin`, a fixture constructor, and never the gather's own per-matched-pid loop, whose operand and instant both differ), states the selection rule on the field itself (first entry of `h.Pin.PGIDs` when non-empty, no read at all when empty — measured at two entries naming one detached group on a healthy run via `finLivePinReduce`/`TestFinLivePinReduce`, not the stale `len == 1` parenthetical elsewhere in the tree), and argues the zero `pinStateOutcome` is the honest report for the empty case (`Verdict ""`, named among the shapes `trailSightingReasonPidReadFailed` answers for — so no C10 contract check is owed even once the field is staged). `finGatherReadings` carries it through **whole** — `readings.PinnedPid = in.PinnedPid`, no normalisation — beside the existing `PyryExited`/`ClaudeState` tail; the gather's own per-matched-pid `pinReadState` loop is untouched and no second call was added inside it. `finExitRunProbe`, the one live caller, takes a second `pinReadState` call over `h.Pin.PGIDs[0]` (never `h.ClaudePID`, which names claude itself) immediately after the exit wait, guarded so an empty pinned set takes no read rather than calling `pinReadState(0)` — which would answer `pinStateInstrumentFailed` and falsely claim an instrument ran. Because the classifier's `Ordering.Value == ""` guard is single-sided and, at the time, no shipped gather could stage `Ordering` at all, **no outcome moves**: a live run still answers `run-void-sighting-route-not-staged`, now from a genuinely half-staged pair rather than a hypothetical one, proven by `TestFinGatherHalfStagedRouteMovesNoOutcome`'s whole-`trailRunOutcome` comparison across two gathers differing only in `PinnedPid`; a sibling test, `TestFinGatherPinnedPidDoesNotReachTheLiveness`, proves the reading never lands on `Liveness` instead — type-correct and silently wrong, since `pinReadState`'s return type matches `Liveness`'s element type exactly. Both tests proven RED→GREEN via `go test -overlay` mutants (append-to-`Liveness`, narrow-to-`.Verdict`, drop-the-pass-through, `&&`-guard defeat) rather than trusted on inspection. Thirteen shipped comment sites corrected across three groups — seven gone outright false, six needing interim wording (two naming **#1459**, the gather-tier captured-bytes sweep, as the ticket that updates them again once that sweep lands — **since landed and re-pointed, see below**), three verified as correctly unedited (about `Ordering`, the ordering half — **#1462 later added the carrying field; no live caller stages it still, see below**); security-sensitive (architect self-review PASS); **five** code-review rounds (FAIL ×4 → PASS), all on the comment-citation tail rather than the implementation — the spec's own "cite tail is empty" measurement was scoped to the wrong file (it measured the file gaining the *field*, not `trail_run_outcome_test.go`, the file the sweep actually *grows*, the package's most-cited file at +51 lines / 185 displaced endpoints across 18 files), and the residual rounds turned on citation forms invisible to a filename grep (bare `(:NNN)` inheriting a preceding **symbol**, not only a filename) and on distinguishing a genuine displacement fix from "repairing" a citation that was already stale on `main` before the branch existed — round 5 reversed a FAIL over two such pre-existing-stale cites and recommended one cleanup ticket for the seven-member family rather than fixing them piecemeal; also reverted an inherited, out-of-scope 79-file citation renumbering (`12aee0d`) that had corrupted fixture data resembling line numbers (`"duration_ms"`, `"pgid"`) along with genuine cites; codebase note at [`codebase/1458.md`](../codebase/1458.md).
- Ticket [#1459](https://github.com/pyrycode/pyrycode/issues/1459) — seals #1458's route with a gather-tier sibling test, `TestFinGatherPinnedPidCarriesNoCapturedBytes`, placed immediately after `TestFinGatherPinnedPidDoesNotReachTheLiveness` rather than as a third plant on `TestFinGatherReturnsNoCapturedBytes` — that test marshals `readings` whole, and `readings.PinnedPid = in.PinnedPid` carries the pin by design (the same whole-value equality the sibling test asserts on purpose), so a plant there is red against a correct build. A closure plant (`plantedPin`) fills all three string-bearing members of `pinStateOutcome` — `Detail`, `StateColumn`, `ToolStderr` — with `trailNeedle`, a shape no single `pinClassifyState` arm actually emits (its instrument-failed branch leaves `StateColumn` empty), deliberately maximising surface because the rule enforced is "no `Detail` quotes any input's captured string," not "don't copy the one field named." Six channels swept individually for absence — the marshalled `trailRunOutcome`, `outcome.Detail`, `readings.Gate.Detail`, `readings.Admit.Detail`, the marshalled `finAttributeRecord`, the marshalled `finSighting` — four of which pass structurally and are kept as forward guards, the same standing `TestFinGatherReturnsNoCapturedBytes` gives its own sighting row; the marshalled `trailRunReadings` is deliberately **not** a row, since those bytes ride that value by design (AC2), stated in the test's own doc so a later reader doesn't "fix" the omission against correct code. The control (AC3) renders what a republishing arm would have produced through the shipped `trailDetail` off the shipped `outcome.Detail` (never a re-typed format string) and asserts the needle survives under `reachMaxCommandBytes` with no `reachTruncationMarker` — the discrimination between a leak kill and a **budget kill wearing a leak kill's clothes**, the exact failure mode this test family was fooled by once before. Six shipped comments naming #1459 as owing the sweep are re-pointed to name the shipped test directly (`grep -rn "1459" internal/` returns nothing post-change). Verified in code review via `go test -overlay` (step-8 `decide` interpolating `readings.PinnedPid.Detail`, no worktree writes): the marshalled-outcome and `outcome.Detail` rows redden on the needle, the control's three assertions stay green, no premise fires; shipped `outcome.Detail` measured at 380 bytes, control at 440/512, 72 bytes headroom. Citation tail Δ=0 on both edited files, verified per-hunk. Security-sensitive (architect self-review PASS); one code-review round, PASS with one non-blocking SHOULD FIX — `plantedPin`'s budget comment claims the control "pays … twice" and references a "mutation run below" that isn't in the shipped file; true only under the mutation proof (which lives in the PR body, not the test), false of the shipped tree's 72-byte headroom — plus two wording NITs, none addressed in a follow-up commit as of this writing; developer's own lessons: a per-file citation-tail measurement is only valid below its own anchor (site 3 sat ~1000 lines above the spec's scanned anchor, on an already-stale-on-`main` bare cite, left alone), and a compose-on-top control must be sized against the *mutated* tree, since the mutation proof doubles the plant's cost and tightens the real budget to `408 + 2×len(Detail) < 512` (~50 bytes, not the shipped tree's ~132); no production files touched; split from #1456, unblocked when #1458 merged; codebase note at [`codebase/1459.md`](../codebase/1459.md).
- Ticket [#1462](https://github.com/pyrycode/pyrycode/issues/1462) — ships the ordering half of #1440's pinned-pid sighting route that #1458 left open: `finGatherInputs` gains a ninth field, `Ordering trailOrderResult`, appended after `PinnedPid`; its doc names the one admissible producer (`trailCertifyOrdering`'s own output, never a `trailOrderResult{Value: trailOrderCertified}` literal, which would let everything downstream pass against a certification that certifies nothing), states it travels **whole** (never narrowed to `.Value`, for the same reason `trailEstablishSighting` re-decides nothing from a bare string), and argues the zero `trailOrderResult` degrades to the named `trailOutcomeVoidSightingRouteNotStaged` reading rather than to a claim (so no C10 is owed). `finGatherReadings` carries it through with a bare `readings.Ordering = in.Ordering`, beside a new paragraph explaining why the gather does **not** certify the ordering itself even though two of the three premises are in reach — `holdHeld` is a fact about a FIFO the caller holds, and a certification from two premises and a guess is worse than none. **No live caller is wired, and none can be added here**: the trailer's sighting is reported *after* the call a certification would need it for, in both shipped gathers, and recovering it earlier would degrade the gather's own observation to `trailBoundFromStart` — a discriminator whose own doc says it BOUNDS NOTHING; `finExitRunProbe` is untouched by design, and the resolving ticket does not exist yet. `TestFinGatherStagedOrderingReachesTheSightingOutcomes`, a new three-row table reusing #1458's arm-reaching recipe, drives a certified ordering (`trailCertifyOrdering(true, true, true)`, never a literal) beside the real shapes `pinClassifyState` fills on each pin arm through the shipped gather and classifier, reaching `trailOutcomeAliveAtSightingByOrdering` and (on two distinct refuting reads, cross-checked by published reason) `trailOutcomeVoidPinnedPidDidNotEstablish` — both reachable before only from hand-built classifier fixtures; `TestFinGatherHalfStagedRouteMovesNoOutcome`'s rows are unchanged, its doc revised to say its premise is now a behavioural check rather than a restated compile-time fact. Ten shipped comment sites across both files carrying a welded "no gather stages the ordering / every live run lands on not-staged" claim were split rather than flipped — the carriage half corrected, the no-live-caller half kept — and all five stale **#1457** attributions (closed, the parent this ticket split from) repaired to name the carriage as landed and the staging as unowned, never repointed to "#1462 stages it," which is the one correction the ticket forbids. Six-edit numeral-and-ordinal doc-block sweep on the field's own nine-field/five-staged count, including a site (`"over a fourth field"`) that stays correctly unedited since it is `PinnedPid`'s own argued-home claim, not a rolling count. All three spec mutants (`.Value`-narrowed carriage, `&&`-widened guard, swapped refutation reasons) reproduced independently in code review via `go test -overlay`, each sole-red on its intended assertion. Security-sensitive (architect self-review PASS); one code-review round, PASS with two non-blocking NITs; split from #1457 (closed); follow-up owed: **#1463**, the gather-tier captured-bytes sweep over this route, deferred on a measured gap (`trailCertifyOrdering`'s three-`bool` signature admits no captured byte to the field through its producer, `trailEstablishSighting` already can't quote `ordering.Detail`, and `trailClassifyRun` never renders `Ordering`) — **since shipped, see below**; codebase note at [`codebase/1462.md`](../codebase/1462.md).
- Ticket [#1463](https://github.com/pyrycode/pyrycode/issues/1463) — seals #1462's route with a gather-tier sibling test, `TestFinGatherOrderingCarriesNoCapturedBytes`, placed immediately after `TestFinGatherStagedOrderingReachesTheSightingOutcomes` rather than as a third plant on `TestFinGatherReturnsNoCapturedBytes` — that test marshals `readings` whole, and `readings.Ordering = in.Ordering` carries the ordering by design, so a plant there is red against a correct build; #1452's "a new route gets a sibling test" precedent points the same way. **Weaker than its two siblings by construction, and the test's doc says so rather than overclaiming**: `trailCertifyOrdering` takes three `bool`s, so no captured byte can reach a `trailOrderResult` through its one producer, and `trailClassifyRun` reads `readings.Ordering` at exactly two sites (the `Ordering.Value == ""` guard and the whole-value hand-off to `trailEstablishSighting`), neither of which renders `Ordering.Detail` into any published string — so the sweep states "all six channels pass structurally, and none is a live leak," a *stronger* absence than the pid sibling's four-of-six, not the weaker one dressed up. The plant (`plantedOrdering`, a function, not a package-level value) fills both of `trailOrderResult`'s members — `Value: trailOrderCertified`, `Detail: "ordering: " + trailNeedle` (52 B) — with no third-field surface to maximise, unlike the pid sibling's deliberate three-field fill; fixture is `finGatherNegativeInputs` with only `Ordering` replaced, and **staging the value does not move the arm**, measured rather than assumed (an earlier draft claimed the opposite): the whole sighting route sits behind step 1's gate-absent branch, so on this usable-gate fixture the classifier never consults `Ordering`, and the outcome stays on the cheap `run-scan-matched-no-row` (step 8) arm rather than the far-tighter gate-absent arms (453–492 of the 512 cap). Five premises turn a vacuous sweep into a named failure (plant carries the needle, `readings.Ordering == in.Ordering` whole-value, `Gate.Detail`/`Admit.Detail` non-empty, arm is the pinned one, no pre-existing truncation marker); six channels swept (marshalled run outcome/attribution/sighting, plus the outcome's/gate's/attribution's `Detail` named individually), with the marshalled `trailRunReadings` deliberately excluded and the exclusion stated in the test's own doc; the control composes through the shipped `trailDetail` off the shipped `outcome.Detail` (never a re-typed format string), measured 443 clean / 506 under the RED-proof mutant against the 512 cap, both silent on the truncation marker. Re-points the **one** shipped comment naming this ticket (measured, not the six an earlier draft predicted by #1459's analogy) to the new test's name, in the resolved pid-sibling form; the two count claims in `TestFinGatherReturnsNoCapturedBytes`' doc are verified unchanged (they count inputs that *can* carry captured bytes, and the ordering isn't one) with a two-sentence extension added to the paragraph below them naming the ordering as a second whole-carried value; the file header's own legitimate-publication list is untouched. Verified in code review via `go test -overlay` (step-8 `decide` appending `readings.Ordering.Detail`, no worktree writes): the marshalled-outcome and `outcome.Detail` rows redden on the needle, the other four channels and the control's three assertions stay green; the mutation-only "~54-byte ceiling" budget claim was independently re-measured exact on both sides (54 green on the needle alone, 55 trips the control's own budget `Fatalf` at 512). Security-sensitive (architect self-review PASS); one code-review round, PASS with one non-blocking NIT (a "three hundred bytes" overrun estimate measures 282, direction-conservative, argument unaffected); split from #1457 (closed); no further follow-up owed at this tier — the live gather-tier route remains #1458's pid read, swept by `TestFinGatherPinnedPidCarriesNoCapturedBytes`; codebase note at [`codebase/1463.md`](../codebase/1463.md).

- Ticket [#1353](https://github.com/pyrycode/pyrycode/issues/1353) — live entry point measuring whether `pyry agent-run` reaches its normal exit path on the headless `PYRY_USE_STREAMJSON=1` path while a claude-auto-backgrounded command is still running, the structural sibling of #1337's ptyrunner reading; composes #1338/#1340/#1342/#1343/#1349/#1439/#1440/#1446/#1447/#1448/#1452/#1458/#1459/#1462/#1463 without re-deriving any of them, `finExitClassify` reused from #1337's probe rather than copied; the one design decision — certifying #1439's ordering *after* the one `finGatherReadings` call and filling `trailRunReadings.Ordering` rather than the structurally unfillable `finGatherInputs.Ordering` #1462 shipped — routes around the circularity #1462's spec named instead of resolving it, leaving that field's zero and its "no live caller" doc both true and unedited; adds one pure symbol, `finStreamCertifyOrdering`, whose `holdHeld` premise is a structural `true` (`holdProbeFIFO` releases only in its own `t.Cleanup`, and this rig registers none) rather than a guess, pinned by a six-row offline trap whose one load-bearing clause is that no row may answer `trailOrderVoidUnheld`; comment-only, per-hunk line-neutral edit to `finding_live_run_test.go` correcting its "only caller" claim now that `finLiveRunStage` has two; no production files touched; security-sensitive (architect self-review PASS); one code-review round, PASS with one non-blocking SHOULD FIX (a stale "no live run stages the ordering" doc claim the #1462 spec's file-clearance argument missed, saved to agent memory as a general lesson — a spec's "these files need no edit" list clears sites, not files) and three NITs; split from #1237. **The live measurement has not run as of this writing**: the dispatcher's automated real-claude gate removed `needs-real-claude` on the strength of the tagged suite's generic 652/652 pass without noticing `TestRealClaude_StreamExitPathWhileCommandRuns` was itself among that run's 15 skipped tests — a dispatcher-level gap in opt-in-probe handling across this whole family (#1337, #1353, and any future #1231/#1227/#1234 live probes), not a defect in this diff; the exact invocation remains owed to an operator; codebase note at [`codebase/1353.md`](../codebase/1353.md).

- Ticket [#1029](https://github.com/pyrycode/pyrycode/issues/1029) — first real-claude coverage of `change_workspace`, split from #963 (fake-tier only, #980). Rescoped during refinement off an unbuildable premise (no production path reads a stored `conv.Cwd` and spawns with it) to the narrower real-tier-only claim the verb round-trips against a live supervised claude child without wedging it; `TestInteractiveChangeWorkspace` reuses the restored harness (#1473, restoring what #1348 deleted) and the #997/#1028 control-frame helpers verbatim, adding only `readConversationCwdOnDisk`; the tilde-form request (`~/ws-<nonce>`) is what makes the "resolved realpath, not raw request" assertion discriminate on every platform rather than only where `$HOME` sits behind a symlink; zero production files touched; codebase note at [`codebase/1029.md`](../codebase/1029.md).

- Ticket [#1428](https://github.com/pyrycode/pyrycode/issues/1428) — corrects four false claims in `TestTrailGateNamesThePresenceCaseOnAPathThatOwesNone`'s header and its two neighbours, comments only (zero production files, zero assertions/fixtures/`func`s touched). #1434's "Deferred to #1428" paragraph named three mutants as an open gap the blocker had in fact already covered: two are dead against the shipped tree (the reason column the deferral asked for already ships — `trailFixtureTrailer`'s `terminal_reason` is `"completed"`, and the streamrunner arm never reads `decodedReason`), the third is unkillable on the presence side by construction (`trailReasonNamedOwesOne` and `trailReasonPathUnnamed` fall through to the same usable return in `trailGate`) and is caught instead at the reduction's own driver and on the absence side — replaced with where each is actually caught rather than deleted outright. Three live comments named `TestTrailComposesUnderAPtyrunnerReading`, deleted by #1348 along with the ptyrunner runner it pinned: the ptyrunner-award paragraph's other two named detectors survive (both inside `TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt`) and are now the only two claimed; `trailRunnerUnread`'s dangling pointer is deleted outright (the paragraph was already self-contained); and the 68-byte headroom claim at `trailClassifyRun`'s proof arm gets the one honest answer measurement decides — spending the full 68 spare bytes reddens nothing (444 + 68 = 512 = `reachMaxCommandBytes` exactly, returned unchanged by `reachCapCommand`), 69 reddens `TestTrailClassifyRun` in four sub-tests, so the comment now states the margin is **unenforced today** rather than naming the deleted test, no replacement detector added (out of scope for this slice). Every corrected claim re-measured on this branch under `go test -overlay` (six mutants plus a mandatory control, `-run '^TestTrail' -v`, offline, no worktree writes); the matrix table's own five rows are #1434's measurement and explicitly not re-run, with the corrected heading now stating which part of the doc carries whose measurement. Code review independently re-derived all eight runs (PASS, zero disagreements) and surfaced one non-blocking finding one paragraph above this ticket's four sites: #1434's "reddens four tests" now measures five, since #1443 (`git log -S`-dated) added a fifth detector after #1434 measured — true when written, stale one ticket later, the same class this ticket exists to fix; not fixed here (out of the AC's four named sites), recorded in `codebase/1428.md` for #1424's checker discussion; codebase note at [`codebase/1428.md`](../codebase/1428.md).
