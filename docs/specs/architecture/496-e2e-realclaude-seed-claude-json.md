# Architecture spec — #496 `WithWorktreeAuthenticated` seeds `.claude.json` so ptyrunner tests skip the onboarding TUI

**Status:** spec
**Ticket:** [#496](https://github.com/pyrycode/pyrycode/issues/496)
**Predecessors:** [#487](https://github.com/pyrycode/pyrycode/issues/487) (layer 1 — `defaultMode:"deny"` / `/doctor` poisoning), [#489](https://github.com/pyrycode/pyrycode/issues/489) → [#490](https://github.com/pyrycode/pyrycode/issues/490) / [#491](https://github.com/pyrycode/pyrycode/issues/491) / [#492](https://github.com/pyrycode/pyrycode/issues/492) (layer 2 — dual-credential fixture, consumer flip, operator docs)
**Estimated production LOC:** ~50 (one fixture function extended). Tests + doc bring total written work to ~150 LOC; well within the S budget.

## Files to read first

- [`internal/e2e/realclaude/fixtures.go:39-77`](../../../internal/e2e/realclaude/fixtures.go) — the existing `WithWorktreeAuthenticated`. Function body, dual-credential skip, `WithWorktree(t)` HOME-pin, per-variable `t.Setenv` re-pin. This is the single function being extended.
- [`internal/e2e/realclaude/fixtures.go:32-37`](../../../internal/e2e/realclaude/fixtures.go) — `WithWorktree(t)`: `t.TempDir()` + `t.Setenv("HOME", dir)`. The fixture you delegate to; understand that `t.Setenv("HOME", …)` clobbers the operator's real HOME after the WithWorktree call returns. Capture order matters.
- [`internal/e2e/realclaude/fixtures_test.go:104-176`](../../../internal/e2e/realclaude/fixtures_test.go) — two existing contract tests for `WithWorktreeAuthenticated`. `TestWithWorktreeAuthenticated_SkipsAndNamesBothEnvVarsWhenNeitherSet` uses the outer/inner subprocess re-exec pattern under sentinel `PYRY_REALCLAUDE_AUTH_SKIP_INNER=1` to capture the skip-message text; mirror this shape for the new "no `.claude.json`" skip test. `TestWithWorktreeAuthenticated_OAuthTokenOnly_RepinsAndPreservesAbsentApiKey` is in-process with a synthetic token literal — this test must be EXTENDED, not replaced, to pre-seed a fake operator `$HOME` with a fake `.claude.json` (new skip path would otherwise fire on a CI runner without a real operator `.claude.json`).
- [`internal/e2e/realclaude/fixtures_test.go:274-280`](../../../internal/e2e/realclaude/fixtures_test.go) — `TestMain` recursion-guard pattern. The new outer/inner subprocess skip test uses a NEW sentinel env-var name (e.g. `PYRY_REALCLAUDE_NOJSON_INNER`), distinct from `GO_TEST_HELPER_PROCESS=1`, so `TestMain` falls through to `m.Run()` on the inner branch. Do not reuse `PYRY_REALCLAUDE_AUTH_SKIP_INNER` — that sentinel clears BOTH env vars; the new test needs `CLAUDE_CODE_OAUTH_TOKEN` SET but `.claude.json` absent.
- [`docs/knowledge/features/e2e-realclaude.md:36-46`](../../knowledge/features/e2e-realclaude.md) — `### Max-plan operator setup (macOS)` section landed by #492. The new doc paragraph (per AC #5) extends this section with the `.claude.json` prerequisite. Read top-to-bottom of the `## Operator authentication` H2 (~L25–L89) so the new paragraph slots into the existing H3/H4 rhythm without disrupting it.
- [`docs/knowledge/features/e2e-realclaude.md:96-98`](../../knowledge/features/e2e-realclaude.md) — the `WithWorktreeAuthenticated` bullet inside `## What's there today`. Currently describes the dual-credential skip and `$HOME`-pinning rationale; needs a one-sentence extension naming the `.claude.json` seed.
- [`docs/knowledge/codebase/491.md`](../../knowledge/codebase/491.md) — same-suite predecessor pattern: small fixture-side change, documented "patterns established" block, careful audit of consumer impact.
- [`docs/knowledge/codebase/492.md`](../../knowledge/codebase/492.md) — operator-doc predecessor; templates the H2/H3 structure and the single-source-of-truth recipe convention. Useful when extending the doc paragraph.

Codegraph note: `codegraph_context "WithWorktreeAuthenticated seed claude.json onboarding TUI"` returns the same `internal/e2e/realclaude/fixtures.go` + `fixtures_test.go` set this list already names; no additional reading surface surfaced. The 15 consumer test files (per `codegraph_callers WithWorktreeAuthenticated`) do NOT need to be read — this change preserves the function signature and `oauthToken == ""` fall-through, so consumer behaviour is unchanged for the `ANTHROPIC_API_KEY` and "neither set" paths.

## Context

After [#489](https://github.com/pyrycode/pyrycode/issues/489) / [#490](https://github.com/pyrycode/pyrycode/issues/490) / [#491](https://github.com/pyrycode/pyrycode/issues/491) / [#492](https://github.com/pyrycode/pyrycode/issues/492) merged (main `b7e8a74`, 2026-05-21), `make e2e-realclaude` on the operator's Max-only Mac with `CLAUDE_CODE_OAUTH_TOKEN` exported from Keychain produced **19/19 FAIL in 593 s**. Every failing test exits with `duration_ms:31001, is_error:true, subtype:"error_during_execution", num_turns:0` — ptyrunner's 31 s deadline fires on a `claude` that never emits an assistant event.

Root cause (verified by reporter on operator Mac): the `CLAUDE_CODE_OAUTH_TOKEN` env-var path authenticates `claude -p` (subprocess / streamrunner) mode but **not** interactive `claude` (PTY mode = what ptyrunner spawns) on a fresh `$HOME`. Interactive `claude` always renders the onboarding theme picker first on a fresh `$HOME` regardless of auth env vars. ptyrunner reads the picker's prompt glyph as "ready", delivers the bracketed-paste prompt into the picker's input field, and `claude` never proceeds past the picker — the 31 s deadline fires.

Fix recipe empirically verified by reporter (`/tmp/onboarding-test/out.jsonl`, exit 0 / 3.4 s / `subtype:"success"`): seed `<tempHome>/.claude.json` from the operator's real `~/.claude.json` BEFORE invoking `claude`. The operator's real file carries `hasCompletedOnboarding=true` after any successful `claude` invocation; with both the OAuth token AND that JSON in place, interactive `claude` skips the picker and processes the prompt normally.

This is layer 3 of the validation-gate-on-Max-only-Mac peel — the final layer needed to make `make e2e-realclaude` actually exercise the real-claude trust-boundary suite on a Max-only Mac. The 19/19 fail surface is the only thing standing between the operator and a green-or-named-skip suite outcome.

## Design

### Behaviour contract

`WithWorktreeAuthenticated(t)` gains a third path on top of the existing two:

1. **Both env vars unset** → `t.Skipf` with the existing dual-variable + Keychain-extraction message. **Unchanged from #490.**
2. **OAuth token set, operator's `~/.claude.json` unreadable** → NEW: `t.Skipf` with a message naming BOTH prerequisites (token AND `.claude.json`) and the operator-recoverable workflow ("run `claude` once directly to complete onboarding").
3. **Either credential set AND (oauth path → `.claude.json` readable)** → proceed: `WithWorktree(t)` pins HOME to a tempdir, re-pin whichever env var(s) were non-empty, and (oauth path only) write the captured `.claude.json` bytes into `<tempHome>/.claude.json` at mode `0o600`.

`ANTHROPIC_API_KEY`-only callers are NOT affected by the new logic — the `.claude.json` read + seed are gated on `oauthToken != ""`. Rationale: the API-key path uses `claude -p` (headless), which authenticates via env var and skips the onboarding TUI; no JSON seed needed. AC #2's "no regression on `ANTHROPIC_API_KEY` scenario" preserved.

### Implementation option — Option A (verbatim copy)

The ticket framed two options. Architect picks Option A (reporter's read confirmed): **copy the operator's `~/.claude.json` verbatim** into `<tempHome>/.claude.json`.

Rationale:

- **Empirical robustness over theoretical isolation.** Option B (synthesise a minimal `{"hasCompletedOnboarding":true,"installMethod":"npm-global"}`) is brittle to future `claude` releases adding required onboarding flags — drift would resurface as another "31 s timeout mystery" identical in shape to the bug this ticket fixes. Option A carries every onboarding-related flag claude might care about now or later, which is exactly the property the fix needs.
- **Cross-test contamination risk is theoretical and revisitable.** The temp `$HOME` is per-test (`t.TempDir()` semantics: process-private, auto-cleaned), so per-test isolation already bounds the blast radius. The copied JSON's "unrelated state" (project history, plugin caches, mcpOAuth references) is read by `claude` but not exfiltrated — no test in the suite spawns `claude` against an external host beyond Anthropic's API. If a future test surfaces contamination as a real failure mode, the follow-up is to flip to Option B; until then, the verbatim copy is the right default.
- **Single source of truth.** The operator already maintains `~/.claude.json` as the contract between their interactive `claude` setup and the file's contents. Re-deriving a minimal JSON in the fixture would create a second source of truth that drifts.

### Capture order

```
fixture entry
  ↓
read ANTHROPIC_API_KEY + CLAUDE_CODE_OAUTH_TOKEN (existing)
  ↓
if both empty → t.Skipf (existing)
  ↓
[NEW] capture operatorHome = os.Getenv("HOME") — BEFORE WithWorktree pins it
  ↓
[NEW] if oauthToken != "":
        read <operatorHome>/.claude.json
        if read fails (any reason — ENOENT, EACCES, …) → t.Skipf naming both prerequisites
  ↓
WithWorktree(t) — pins HOME to t.TempDir() (existing)
  ↓
if apiKey   != "" → t.Setenv ANTHROPIC_API_KEY   (existing)
if oauthToken != "" → t.Setenv CLAUDE_CODE_OAUTH_TOKEN  (existing)
[NEW]    + os.WriteFile(<tempHome>/.claude.json, capturedBytes, 0o600)
  ↓
return tempHome
```

**Why capture HOME with `os.Getenv` and not `os/user.Current().HomeDir`:** the ticket's "Technical Notes" mentioned `os/user.Current` "or equivalent", and `os.Getenv("HOME")` IS the equivalent at the moment of fixture entry — `t.Setenv("HOME", ...)` has not yet been called by `WithWorktree`, so `$HOME` still reflects the operator's real value. Using `os.Getenv("HOME")` keeps the fixture testable without mocking `os/user` — tests can pre-pin `$HOME` to a fake operator home via `t.Setenv("HOME", fakeHome)` before invoking the fixture, and the fake home will be the value the fixture captures. `os/user.Current()` reads from `/etc/passwd` (Unix) and is not test-overrideable via env vars; choosing it would force a package-private indirection variable just to make the test work, with no production benefit.

**Why skip on read failure, not silently seed nothing:** AC #4 is explicit — tests must NOT time out at 31 s when the prerequisite is missing. A silent "skip the write if read fails" path would let downstream tests run with no `.claude.json` seeded, which is exactly the 31 s-timeout failure mode the ticket exists to eliminate. The skip must be named, deterministic, and fire before any subprocess spawns.

### File contents and permissions

- **Source:** `<operatorHome>/.claude.json`, read in full via `os.ReadFile`. Size cap: none. The operator's file is ~117 KB in the reporter's environment; a future bound is unwarranted (the file is operator-owned, not adversary-controlled, and the test process already holds it in memory as part of the OS file cache).
- **Destination:** `<tempHome>/.claude.json` (where `tempHome` is what `WithWorktree(t)` returns).
- **Mode:** `0o600` — matches the source file's protection on the operator's machine, matches the `prompt.txt` / `system.txt` writes elsewhere in this fixture (`fixtures.go:168, 172`), and matches the `~/.claude.json` mode convention. Pin the mode literal in the spec so code-review catches drift.
- **Verbatim copy.** No JSON parse, no field filter, no normalisation. Treat the bytes as opaque.

### Skip-message text

The new skip message must (per AC #4) name BOTH prerequisites AND give the operator a concrete recovery action:

> realclaude.WithWorktreeAuthenticated: CLAUDE_CODE_OAUTH_TOKEN is set but `<operatorHome>/.claude.json` could not be read (<original error>). Interactive `claude` (PTY mode) shows the onboarding theme picker on a fresh $HOME regardless of auth env vars, so ptyrunner tests would time out at 31 s without this file. Run `claude` once directly under your user account to complete the onboarding flow (which writes ~/.claude.json with `hasCompletedOnboarding=true`), then re-export CLAUDE_CODE_OAUTH_TOKEN and re-run `make e2e-realclaude`.

The exact prose is the developer's call; the spec pins the load-bearing substrings the test asserts on:

1. `CLAUDE_CODE_OAUTH_TOKEN` (names the token prerequisite verbatim)
2. `.claude.json` (names the JSON prerequisite verbatim)
3. `onboarding` (signals the failure class)
4. `claude` (the recovery action's verb)
5. `hasCompletedOnboarding=true` (the contract the recovery action satisfies)

### Doc extension (AC #5)

Extend [`docs/knowledge/features/e2e-realclaude.md`](../../knowledge/features/e2e-realclaude.md)'s `### Max-plan operator setup (macOS)` section (~L36–L46) with one paragraph documenting the new `.claude.json` prerequisite. The paragraph should:

- Name the prerequisite explicitly (file path + the `hasCompletedOnboarding=true` flag it carries).
- Explain WHY it's needed (interactive PTY mode shows the onboarding picker even with valid auth env vars; ptyrunner reads picker as "ready" and times out at 31 s).
- Name the recovery action (`claude` invoked once directly triggers onboarding; the resulting `~/.claude.json` carries the flag).
- Cross-reference [#496](https://github.com/pyrycode/pyrycode/issues/496) and [#487](https://github.com/pyrycode/pyrycode/issues/487) (layer 1 of the same peel) inline, mirroring #492's "ticket numbers stay inline" convention.

Also extend the `WithWorktreeAuthenticated` bullet inside `## What's there today` (~L98) with one sentence: "Also seeds `<tempHome>/.claude.json` from the operator's real `~/.claude.json` when the OAuth path is active so interactive (PTY) `claude` skips the onboarding TUI (#496)."

Do NOT add an entry to `docs/knowledge/INDEX.md` — this is an additive paragraph in an existing doc, same convention as #492.

### What does NOT change

- The `WithWorktreeAuthenticated` signature — same `(t *testing.T) string`. Zero call-site edits.
- `WithWorktree(t)` — untouched. It remains the no-auth, no-JSON-seed default.
- The `apiKey != ""`-only path — no `.claude.json` read attempt, no seed. CI scenario preserved.
- The "both env vars unset" skip path and its existing message — unchanged. Existing contract test (`TestWithWorktreeAuthenticated_SkipsAndNamesBothEnvVarsWhenNeitherSet`) continues to pass without modification.
- The `internal/e2e/realclaude/testdata/` directory — no fixture files added; the in-process tests synthesise their own `.claude.json` content inline.
- All 15 downstream consumers (`prompt_fidelity`, `tool_loop`, `allowed_tools_enforcement`, `per_agent`, `budget`, `large_tool_output`, `long_session`, `sigterm_mid_tool_use`, `resilience` ×3, `permission_protocol_spike` ×1, `ptyrunner_byte_equivalence`, `doctor_poisoning_regression`, the existing `fixtures_test.go` consumers) — no changes; they inherit the new seed transparently.

## Testing strategy

Three changes to [`internal/e2e/realclaude/fixtures_test.go`](../../../internal/e2e/realclaude/fixtures_test.go); zero new test files.

### 1. NEW — `TestWithWorktreeAuthenticated_SkipsWhenOAuthSetButNoClaudeJSON`

Outer/inner subprocess re-exec pattern (mirrors `TestWithWorktreeAuthenticated_SkipsAndNamesBothEnvVarsWhenNeitherSet`):

- **Sentinel env var:** `PYRY_REALCLAUDE_NOJSON_INNER=1` — distinct from `GO_TEST_HELPER_PROCESS=1` (so `TestMain` falls through to `m.Run()`) and distinct from `PYRY_REALCLAUDE_AUTH_SKIP_INNER=1` (so the inner branch's intent is unambiguous).
- **Inner branch:**
  1. Create a tempdir `opHome` via `t.TempDir()`. Do NOT create `.claude.json` in it.
  2. `t.Setenv("HOME", opHome)` — pin operator HOME to the empty tempdir.
  3. `t.Setenv("ANTHROPIC_API_KEY", "")` + `os.Unsetenv("ANTHROPIC_API_KEY")` (the two-step from the existing OAuth-token-only test).
  4. `t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-oauth-token-not-real")` — synthetic literal; no real claude invocation.
  5. Call `WithWorktreeAuthenticated(t)`. Expected: `t.Skip` ends the goroutine before the next line.
  6. `t.Fatalf("WithWorktreeAuthenticated returned without skipping; want t.Skip when .claude.json missing")` — defensive guard.
- **Outer branch:** re-exec self with `-test.run=^TestWithWorktreeAuthenticated_SkipsWhenOAuthSetButNoClaudeJSON$ -test.v` and `PYRY_REALCLAUDE_NOJSON_INNER=1` in env.
- **Outer assertions:**
  - Inner exited zero (skip is a success status; `cmd.CombinedOutput()` returns nil for `err`).
  - Output contains `--- SKIP: TestWithWorktreeAuthenticated_SkipsWhenOAuthSetButNoClaudeJSON`.
  - Output contains each of the load-bearing substrings: `CLAUDE_CODE_OAUTH_TOKEN`, `.claude.json`, `onboarding`, `hasCompletedOnboarding=true`, and the `claude` recovery verb.

The shape of the assertion list (substring set, not exact-prose match) mirrors the existing `wants` slice at `fixtures_test.go:129-134`.

### 2. EXTEND — `TestWithWorktreeAuthenticated_OAuthTokenOnly_RepinsAndPreservesAbsentApiKey`

The existing test currently uses a synthetic token literal and verifies env-var re-pinning. Under the new logic it would fire the new skip path (synthetic OAuth token + no `.claude.json` in operator HOME). Pre-seed the inputs and add post-call assertions:

- **Pre-call setup additions (insert BEFORE the existing `t.Setenv("ANTHROPIC_API_KEY", "")` line at L151):**
  1. Create `opHome := t.TempDir()`.
  2. Define a recognisable synthetic JSON content (e.g. `[]byte(\`{"hasCompletedOnboarding":true,"installMethod":"npm-global","_marker":"#496-test"}\` + "\n")`). The `_marker` field makes the byte-equality assertion below diagnostic.
  3. Write the bytes to `filepath.Join(opHome, ".claude.json")` at mode `0o600`. `t.Fatalf` on write error.
  4. `t.Setenv("HOME", opHome)` — pin operator HOME so the fixture captures `opHome`.
- **Post-call assertion additions (after the existing `if got := os.Getenv("HOME"); got != dir { ... }` block):**
  1. Read `filepath.Join(dir, ".claude.json")` via `os.ReadFile`. `t.Fatalf` on read error.
  2. Assert `bytes.Equal(read, written)` — verbatim copy contract.
  3. `os.Stat` the destination and assert `info.Mode().Perm() == 0o600` — mode contract.

This rolls the seed verification into the existing in-process test (zero new tests for the happy path), matching #491's "single test covers multiple contracts that share one execution context" pattern.

### 3. UNCHANGED — `TestWithWorktreeAuthenticated_SkipsAndNamesBothEnvVarsWhenNeitherSet`

Both env vars unset → fixture skips on path 1 before the `.claude.json` check runs. The existing test passes unchanged. Do NOT extend it; coupling it to the new skip-message substrings would falsely couple two independent contracts.

### 4. UNCHANGED — `TestWithWorktreeAuthenticated_RealAssistant`

Operator runs this with real `CLAUDE_CODE_OAUTH_TOKEN` from Keychain AND real `~/.claude.json` present (the production case). Both prerequisites satisfied → fixture proceeds, seeds the temp HOME with the operator's real JSON, the real `pyry agent-run` invocation succeeds, the existing JSONL assertions pass. No assertion changes needed.

### Out-of-test coverage

Per AC #1, the FULL real-claude suite (the 19 tests cited in the fail transcript) should run end-to-end on the operator's Mac after this lands. The architect's spec cannot pin that as a CI assertion (the suite has no CI workflow per the `## CI cadence` section in `e2e-realclaude.md`), but code-review's existing realclaude run on every dispatched PR will exercise it — the same path that will verify this ticket itself.

## Error handling

- `os.ReadFile(<operatorHome>/.claude.json)` fails for any reason → `t.Skipf` with the named-prerequisites message above. Do NOT `t.Fatalf` — a missing file is an operator-environment shape, not a test or fixture bug.
- `os.WriteFile(<tempHome>/.claude.json, ...)` fails (proceed-path) → `t.Fatalf` with the resolved path and the original error. A write failure on a tempdir we just created via `WithWorktree`'s `t.TempDir()` is a fixture-internal bug worth surfacing loudly.
- `os.Getenv("HOME") == ""` at fixture entry → leave the read attempt to fail naturally (`os.ReadFile("/.claude.json")` returns ENOENT or EACCES depending on FS), which fires the named-prerequisites skip. Adding an explicit empty-string check would add a third skip branch for a state that almost never occurs in practice (operator tests inherit a real shell environment). Keep the fast path uncluttered.
- No retries, no fallbacks. If `~/.claude.json` exists but is corrupt (invalid JSON, truncated, …) the fixture copies it anyway and `claude` surfaces the failure downstream — same trust envelope as the existing `CLAUDE_CODE_OAUTH_TOKEN` forwarding (no shape validation, claude is the authority).

## Concurrency model

None. Both fixtures (`WithWorktree`, `WithWorktreeAuthenticated`) are synchronous per-`t` calls; the suite already serialises every realclaude test (no `t.Parallel()` per the convention named in multiple codebase notes). The `os.ReadFile` / `os.WriteFile` pair runs on the test goroutine; no shared state mutation beyond the tempdir each test already owns. The `t.Setenv` discipline (already established by #490) handles env-var lifecycle.

## Open questions

- **Should the fixture validate that the copied JSON contains `hasCompletedOnboarding=true`?** Spec answer: NO. Validation adds a brittle JSON-parse surface for no real benefit — if the operator's file is missing the flag (or contains a different flag a future `claude` revision uses), the failure mode is exactly the 31 s ptyrunner timeout the ticket already documents, and an in-fixture parse would either reject a valid-but-different file or pass through a corrupted one. The verbatim copy is the right invariant.
- **Should `ANTHROPIC_API_KEY`-only callers also get the JSON seed?** Spec answer: NO. Per AC #2's "no regression" + the empirical fact that `claude -p` (headless) does NOT show the onboarding TUI under env-var auth. If a hypothetical future interactive test surfaces the same 31 s timeout under the API-key path, extend then; evidence-based fix selection per project principle.
- **Should this ticket also flip `permission_protocol_spike_test.go`'s ANTHROPIC_API_KEY-only inline skip to use `WithWorktreeAuthenticated`?** Spec answer: NO. That's the same follow-up #491 deferred ("`mcp_smoke_test.go`'s inline skip / `permission_protocol_spike_test.go`'s inline skip"). Keep this ticket S; the inline-skip refresh is XS in its own right.

## Files

- [`internal/e2e/realclaude/fixtures.go`](../../../internal/e2e/realclaude/fixtures.go) — modify `WithWorktreeAuthenticated`. ~50 LOC delta (additive). One new helper-free code block; no new exported types, no new exported functions, no import additions beyond what's already there (`os`, `path/filepath` both already imported).
- [`internal/e2e/realclaude/fixtures_test.go`](../../../internal/e2e/realclaude/fixtures_test.go) — modify. Add new outer/inner subprocess test (~50 LOC) and extend the existing OAuth-token-only test with pre-seed + post-call assertions (~20 LOC delta). No import additions needed (`bytes`, `os`, `os/exec`, `path/filepath`, `testing` all already imported).
- [`docs/knowledge/features/e2e-realclaude.md`](../../knowledge/features/e2e-realclaude.md) — modify. Append one paragraph (~12 lines) under `### Max-plan operator setup (macOS)` and extend the `WithWorktreeAuthenticated` bullet under `## What's there today` with one sentence (~2 lines).
- `docs/specs/architecture/496-e2e-realclaude-seed-claude-json.md` — this file.

Out of scope (developer must NOT touch these):

- `docs/PROJECT-MEMORY.md` — frozen for agent edits.
- `docs/lessons.md` — frozen 2026-05-11.
- `docs/knowledge/INDEX.md` — documentation-phase ownership.
- `docs/knowledge/codebase/496.md` — documentation-phase ownership; developer's spec ends with the test/code/doc changes above.
- `internal/e2e/realclaude/testdata/` — no fixture file added.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The trust boundary is the operator's machine: `$HOME` is operator-owned. The fixture's read (`os.ReadFile(<operatorHome>/.claude.json)`) consumes operator-trusted data; the write (`os.WriteFile(<tempHome>/.claude.json, …, 0o600)`) lands in a process-private `t.TempDir()` whose only consumer is the same test's `claude` subprocess (also operator-trusted). No untrusted input ever flows through this path.
- **[Tokens, secrets, credentials]** SHOULD FIX *(developer must verify, not blocking the spec)*. `~/.claude.json` contains OAuth account metadata (`oauthAccount` block: email, plan tier, etc.) — same shape as the existing `CLAUDE_CODE_OAUTH_TOKEN` env-var inheritance the fixture already does. Copying with mode `0o600` matches the source file's protection and the existing prompt/system.txt write mode in this fixture. The temp dir is process-private (`t.TempDir()` semantics: `0o700` directory mode, auto-cleaned on test exit) — same trust envelope as the existing token forwarding. **Action item for developer:** verify the destination file mode is `0o600` at write time (`os.WriteFile(..., 0o600)` — pinned in spec); code review must catch any drift.
- **[File operations]** No findings. (a) Path traversal: source path is `filepath.Join(<operatorHome>, ".claude.json")` with a hardcoded basename; destination path is `filepath.Join(<tempHome>, ".claude.json")` ditto. Neither concatenates user-controlled input. (b) TOCTOU: there is no check-then-use — the fixture does a direct `os.ReadFile` (single syscall sequence), not an `os.Stat` + `os.Open`. The source file is operator-owned on the operator's own machine; no adversary is positioned to swap it. (c) Symlinks: `os.ReadFile` follows symlinks; the source is the operator's own file on their own filesystem; no attacker-controlled symlink scenario applies. (d) Atomic writes: the destination is freshly created in a per-test tempdir, never read back as part of a registry; atomic-rename is not required.
- **[Subprocess / external command execution]** No findings — this change adds zero subprocess invocations. The downstream `pyry agent-run` subprocess already inherits the env via `os.Environ()` plus `ExtraEnv` and is out-of-scope for this ticket; the `.claude.json` seed lands on disk before the subprocess is spawned.
- **[Cryptographic primitives]** N/A — no cryptographic operations introduced.
- **[Network & I/O]** N/A — no network operations introduced. File I/O is bounded by operator's file size (~117 KB observed); no streaming, no size cap because the operator owns the source.
- **[Error messages, logs, telemetry]** No findings — the new skip message names the file path (`<operatorHome>/.claude.json`) and the original error (`<err>`), both already inferable from the operator's own environment. No tokens or `.claude.json` contents are surfaced in error messages or logs (the bytes go from `os.ReadFile` directly to `os.WriteFile`, never through `fmt.Errorf` or `t.Logf`). Naming the path explicitly is operator-recoverable diagnostic, not leakage.
- **[Concurrency]** N/A — synchronous fixture, no goroutines, no shared mutable state.
- **[Threat model alignment]** N/A — this is a test fixture on the operator's own machine, not a network-facing component. The pyrycode threat model (`docs/protocol.md`, `docs/protocol-mobile.md`) addresses network paths and CLI-controlled inputs; this fixture mediates between two operator-owned files.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-21
