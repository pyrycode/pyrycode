# Operator authentication

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
