# Operator authentication

`make e2e-realclaude` only exercises the trust-boundary tests if the spawned `claude` subprocess can reach Anthropic's API. The [authenticated fixtures](e2e-realclaude-smoke-test-go.md) accept two env vars; their credential gate skips with a named-variable diagnostic only when **both** are unset or empty.

### Two auth paths

- `ANTHROPIC_API_KEY` — Anthropic API-key path (the "console" path: API users with billing on `console.anthropic.com`).
- `CLAUDE_CODE_OAUTH_TOKEN` — Max-plan OAuth path (the "subscription" path: paid `claude.ai` Max plan, no API key issued by default).

Either is sufficient. The fixture's exact env-var lookup, `t.Setenv` re-pinning, and `HOME` isolation rules live in the [fixture reference](e2e-realclaude-smoke-test-go.md) — start here for the operator setup, drop into that paragraph if you need fixture internals.

### Max-plan operator setup (macOS)

On macOS, the `claude` binary stores Max-plan OAuth credentials in the system Keychain under service `Claude Code-credentials`, with the account set to the macOS user. The entry is a ~1025-byte JSON blob; the bearer token the suite needs is `claudeAiOauth.accessToken`.

Extract it with:

```bash
security find-generic-password -s 'Claude Code-credentials' -w | jq -r '.claudeAiOauth.accessToken'
```

The first invocation against this Keychain item triggers a macOS confirmation dialog ("`security` wants to use your confidential information stored in `Claude Code-credentials` in your keychain"). Click **Always Allow** to suppress the prompt on subsequent runs from the same binary, or **Allow** if you prefer to be prompted each time.

#### Stream-json authentication needs no onboarding file

Current stream-json tests accept either nonempty `ANTHROPIC_API_KEY` or
`CLAUDE_CODE_OAUTH_TOKEN`, including environments with both credentials.
`authenticatedHome` returns an isolated temporary HOME without requiring,
reading or copying the operator's `~/.claude.json`.
`WithWorktreeAuthenticated` also pins HOME and re-pins nonempty credentials;
`authenticatedHome` leaves the process environment unchanged for parallel
consumers, which pass HOME explicitly to their children. These fixtures skip
only when both supported credentials are unset or empty. Real Claude decides
credential validity; invalid credentials fail the live proof.

The former onboarding-file gate served the PTY theme picker. After the PTY
runner was removed in [#1348](https://github.com/pyrycode/pyrycode/issues/1348),
retaining that gate could skip authenticated stream-json tests merely because
an unrelated operator file was absent or unreadable, leaving the suite at exit
0. [#1479](https://github.com/pyrycode/pyrycode/issues/1479) removes that stale
prerequisite. Completing interactive onboarding is unnecessary for these tests.

`TestWithWorktreeAuthenticated_RealAssistant` proves the inherited-credential
path and OAuth-only paths with missing and read-failing `.claude.json` in
disposable operator HOMEs. The OAuth-only cases remove the API key and run in
the normal live gate whenever the OAuth token is available. A named non-skipped
pass requires a nonempty session ID and real assistant JSONL end-of-turn text;
test starts or a synthetic authentication envelope cannot prove a working
login. See [the evidence runbook](../../release-tooling.md#live-claude-suite--read-the-count-not-the-exit-code).

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
- Lifting the suite's `$HOME`-pinning so the subprocess inherits the operator's real `~/.claude/` is NOT a workaround. The pinning isolates per-test JSONL namespaces under `~/.claude/projects/<encoded-cwd>/<sid>.jsonl`; lifting it re-introduces a cross-test JSONL race. See the [HOME isolation rationale](e2e-realclaude-smoke-test-go.md). The env-var path keeps `$HOME` pinned.

### Out of scope for this section

- **In-suite token refresh.** The fixture does not re-read Keychain mid-run. A token extracted at process start that expires mid-run will surface as a subprocess auth failure. Deferred until observed.
- **CI Keychain access.** GitHub Actions runners have no macOS Keychain, so the Max-OAuth path is operator-machine only. The realclaude suite intentionally has no CI workflow (see "CI cadence" below). The related CI-secret-injection bug is tracked separately in [#406](https://github.com/pyrycode/pyrycode/issues/406).
- **Defaulting `pyry agent-run` to OAuth on the operator's Mac.** Not needed: the dispatcher runs in the operator's real shell where Keychain works normally, and the pre-existing `claude` installation handles auth without pyry's help. The env-var path documented here is specifically for the realclaude test suite.
