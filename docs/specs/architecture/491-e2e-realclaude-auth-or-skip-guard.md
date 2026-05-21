# #491 — Guard plain-`WithWorktree` realclaude tests with auth-or-skip

**Size:** XS — 5 files, ~5 LOC total (one call-site replacement per file). No new files, no new exported types, no test code added. The 5 tests being modified ARE the test surface; no separate test additions are needed because the sibling #490 already ships `TestWithWorktreeAuthenticated_SkipsAndNamesBothEnvVarsWhenNeitherSet`, which pins the named-variable skip-message contract that this ticket now flows through 5 more call sites.

## Files to read first

- `internal/e2e/realclaude/fixtures.go:39-77` — `WithWorktreeAuthenticated` as it stands post-#490. Reads both `ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN`, skips with a single named-variable message when neither is set, re-pins whichever is present, then composes `WithWorktree(t)`. The replacement target — its contract is identical to what the 5 call sites need.
- `internal/e2e/realclaude/prompt_fidelity_test.go:28-29` — `TestRealClaude_PromptFidelity`, line 29 is the `WithWorktree(t)` call to replace.
- `internal/e2e/realclaude/prompt_fidelity_unicode_test.go:30-31` — `TestRealClaude_PromptFidelity_Unicode`, line 31 is the call to replace.
- `internal/e2e/realclaude/tool_loop_test.go:27-28` — `TestRealClaude_ToolLoopIntegrity`, line 28 is the call to replace.
- `internal/e2e/realclaude/allowed_tools_enforcement_test.go:37-38` — `TestRealClaude_AllowedToolsEnforcement`, line 38 is the call to replace.
- `internal/e2e/realclaude/per_agent_test.go:83-95` — `runRoleSmokeTest` helper, line 85 is the call to replace. The 5 top-level `*_RoleLoop` tests (`PO`, `Architect`, `Developer`, `CodeReview`, `Documentation`) all delegate here — single-point edit covers all 5 subtests.
- `docs/specs/architecture/490-realclaude-auth-accepts-oauth-token.md` — the sibling spec. Confirms `WithWorktreeAuthenticated`'s skip-message substring contract is already test-pinned in `fixtures_test.go`; this ticket inherits that pin without duplicating it.

## Context

Five test files in `internal/e2e/realclaude/` use plain `WithWorktree(t)` rather than `WithWorktreeAuthenticated(t)` but still spawn `claude` end-to-end via `pyry agent-run`. Without auth in the outer environment, `claude` returns "Not logged in · Please run /login" and the test either times out at the 31-second ptyrunner deadline or fails with the 5-second streamrunner exit-1 — depending on the default runner. Reproduced 2026-05-20 on both runners; the failure mode is the same → env gap, not a runner regression.

Sibling #490 has already taught `WithWorktreeAuthenticated` to accept `CLAUDE_CODE_OAUTH_TOKEN` in addition to `ANTHROPIC_API_KEY`, with a named-variable skip message and a Keychain extraction recipe pointing operators at the macOS-specific path. This ticket is the consumer-side flip: 5 call sites need to start using that already-correct helper.

## Design

### Shape selection: Option 1 (direct call-site replacement)

The ticket body offers two shapes. **Pick Option 1 (convert the 5 files to call `WithWorktreeAuthenticated(t)` directly).** Reasoning:

- The audit below confirms all 5 files genuinely spawn `pyry agent-run` end-to-end — no test in scope is argv-shape-only. So the audit risk Option 1 carries is already discharged.
- Option 2 (introduce a separate `RequireRealClaudeAuth(t)` helper) would duplicate the dual-env-var skip logic that already lives in `WithWorktreeAuthenticated`. Two places to maintain the skip message; two places where a future env-var addition (e.g. `ANTHROPIC_AUTH_TOKEN`) would have to land.
- Option 1 makes the test's auth requirement structurally explicit by reading the fixture name (`WithWorktreeAuthenticated`), not a guard line above it. Fewer LOC, fewer concepts to remember.
- Option 1 does not touch `fixtures.go` at all — keeps the change contained to the 5 test files, no fan-out, no overlap surface with other in-flight fixture tickets.

### Edit per file

In each of the 5 files, change exactly one line:

```
-    workdir := WithWorktree(t)
+    workdir := WithWorktreeAuthenticated(t)
```

Locations:

- `prompt_fidelity_test.go:29` (inside `TestRealClaude_PromptFidelity`)
- `prompt_fidelity_unicode_test.go:31` (inside `TestRealClaude_PromptFidelity_Unicode`)
- `tool_loop_test.go:28` (inside `TestRealClaude_ToolLoopIntegrity`)
- `allowed_tools_enforcement_test.go:38` (inside `TestRealClaude_AllowedToolsEnforcement`)
- `per_agent_test.go:85` (inside `runRoleSmokeTest` — the shared helper; all 5 `*_RoleLoop` subtests inherit the guard transparently)

Nothing else in any of these files needs to change. No imports added (both helpers live in the same package), no new fields, no signature shifts.

### Audit — all 5 files genuinely need auth

Per ticket AC: confirm each affected test actually spawns claude end-to-end, not argv-shape-only.

| File | Test(s) | Calls `RunPyryAgentRun` → real `pyry agent-run` → claude? | Needs auth? |
|---|---|---|---|
| `prompt_fidelity_test.go` | `TestRealClaude_PromptFidelity` | Yes (line 31, `RunPyryAgentRun(t, RunOpts{...})`, default `UseTestBinaryAsFakePyry=false`, no `--dry-run`-style flag) | Yes — asserts on `ExitCode != 0` and on the real JSONL `user` entry surviving |
| `prompt_fidelity_unicode_test.go` | `TestRealClaude_PromptFidelity_Unicode` | Yes (line 33, same shape as above) | Yes — UTF-8 byte-preservation through the live claude JSONL roundtrip |
| `tool_loop_test.go` | `TestRealClaude_ToolLoopIntegrity` | Yes (line 42, same shape; asserts on tool_use → tool_result correlation) | Yes — exercises the multi-turn tool loop end-to-end |
| `allowed_tools_enforcement_test.go` | `TestRealClaude_AllowedToolsEnforcement` | Yes (line 40, asserts denial signal surfaces from claude) | Yes — guards the claude-side `--allowed-tools` deny-by-default contract |
| `per_agent_test.go` (5 subtests via `runRoleSmokeTest`) | `TestRealClaude_PO_RoleLoop`, `_Architect_RoleLoop`, `_Developer_RoleLoop`, `_CodeReview_RoleLoop`, `_Documentation_RoleLoop` | Yes (helper line 87, all 5 delegate identically) | Yes — asserts trailer's `num_turns >= 1` and a tail-side assistant event with `EndOfTurn=true` |

No file in scope is argv-shape-only. No audit-driven exclusion needed.

### Out-of-scope finding (deferred, NOT this ticket's work)

Codegraph reports a 16th `WithWorktree` caller not in the ticket's list: `TestRealClaude_MalformedStreamJSON` at `internal/e2e/realclaude/resilience_test.go:176`. That test spawns claude **directly** via `runClaudeDirect` (not via `pyry agent-run`) with malformed stdin and asserts a parse-shaped diagnostic. The expectation is that claude's stream-json parser rejects the garbage before the auth check matters — under that model the test does not strictly need real auth.

The hazard worth flagging: the test's stderr-keyword predicate is loose (`json | parse | input | format | envelope`), so without auth the test could pass or fail spuriously depending on whether `claude`'s "not logged in" diagnostic happens to contain `input` or similar. If observed in practice, file a follow-up. **Do NOT expand this ticket to add a 6th call-site change.** The PO sized this XS at 5 files; expanding scope here is exactly the kind of "while I'm in here" creep that blows the size budget.

### Why no new exported helper

The natural temptation is to add a shorter alias like `RequireAuth(t)` or `WithAuthedWorktree(t)`. Resist:

- `WithWorktreeAuthenticated` is already the established name (sibling #490 pinned it). A second name fragments the search surface (`grep -rn WithWorktreeAuth` would miss `RequireAuth`).
- The verb-shaped name (`WithWorktreeAuthenticated`) parallels `WithWorktree` exactly — readers immediately recognise the relationship.
- An alias adds a hop in the test reader's mental model (`RequireAuth` → "look up its body" → "ah, it just calls `WithWorktreeAuthenticated` and returns its workdir"). The direct call is shorter to read.

## Testing strategy

No new test code. The behaviour change is exercised by the 9 existing tests under three operator scenarios:

1. **Auth set in outer env (`ANTHROPIC_API_KEY` or `CLAUDE_CODE_OAUTH_TOKEN`):** All 9 tests run unchanged from the pre-ticket `ANTHROPIC_API_KEY`-only baseline. Verify with `ANTHROPIC_API_KEY=... go test -tags e2e_realclaude -run 'TestRealClaude_(PromptFidelity|PromptFidelity_Unicode|ToolLoopIntegrity|AllowedToolsEnforcement|.*_RoleLoop)' ./internal/e2e/realclaude/...`. Pass criterion: exits 0, no skips.
2. **Neither env var set:** All 9 tests skip with the named-variable message from `WithWorktreeAuthenticated`. Verify with `unset ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN; go test -v -tags e2e_realclaude -run ...`. Pass criterion: 9 `--- SKIP:` lines, total wall-clock under a few seconds (specifically: zero 31s ptyrunner timeouts, zero 5s streamrunner exit-1 fails).
3. **`CLAUDE_CODE_OAUTH_TOKEN` set, `ANTHROPIC_API_KEY` unset (Max-only Mac shape):** Same as scenario 1. The sibling #490's re-pin contract guarantees the token survives into the subprocess. No additional structural assertion needed in this ticket because `fixtures_test.go`'s OAuth-only test (#490) already pins it at the fixture layer.

The skip-message substring contract (names both env vars + Keychain recipe) is already pinned by `TestWithWorktreeAuthenticated_SkipsAndNamesBothEnvVarsWhenNeitherSet` in `fixtures_test.go` — landed with #490. This ticket inherits that pin; no duplicate assertion at the call-site layer.

### What NOT to add

- Do NOT add per-file skip-message assertions. The contract lives at the fixture, not the call site. Five duplicate assertions would couple every test to the message string and turn a single message tweak into a 6-file edit.
- Do NOT introduce a `t.Run` wrapper around the existing top-level tests just to "group the skip nicely". The existing structure already produces one skip-line per test, which is what the ticket's "single named-variable skip messages" AC asks for.
- Do NOT update any `*_test.go` other than the 5 listed. In particular, `resilience_test.go:176` is explicitly out of scope (see § "Out-of-scope finding" above).

## Error handling and failure modes

The helper has no error return surface; behaviour is fully governed by the sibling `WithWorktreeAuthenticated` contract. The two outcomes per test are:

- **Skip** (neither cred set) — `t.Skipf` fires inside `WithWorktreeAuthenticated`; the test never reaches `RunPyryAgentRun`. Operator-facing message is the sibling's named-variable diagnostic.
- **Proceed** (≥1 cred set) — `WithWorktree` allocates the tmp HOME and pins it, the re-pin survives, `RunPyryAgentRun` inherits the auth env via `cmd.Env = append(os.Environ(), opts.ExtraEnv...)` at `fixtures.go:192`. Existing assertions in each test fire unchanged.

No new failure modes introduced. No new error wrapping. No new logging.

## Open questions

None. The change is mechanical against an already-established contract.

## Out of scope (mirrors ticket body)

- No token-refresh handling.
- No CI keychain integration.
- No defaulting of `pyry agent-run` to OAuth.
- No expansion to a 6th caller (`TestRealClaude_MalformedStreamJSON`) even though codegraph flagged it — see § "Out-of-scope finding".
- No new helper, no name aliasing, no fixtures.go edits.
