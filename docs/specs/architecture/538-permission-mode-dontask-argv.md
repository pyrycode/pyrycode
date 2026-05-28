# #538 — `agent-run`: `--permission-mode dontAsk` in `buildArgs`

**Size:** XS (1 production literal + 1 test fixture + 3 stale doc/comment fixups)

**Ticket:** https://github.com/pyrycode/pyrycode/issues/538

## Files to read first

- `internal/agentrun/ptyrunner/runner.go:440-456` — `buildArgs`, the single production site of the bug (literal `"default"` on line 451).
- `internal/agentrun/ptyrunner/runner_test.go:446-482` — `TestBuildArgs`, the fixture that pins the argv shape (literal `"default"` on line 458).
- `internal/agentrun/settings/settings.go:1-75` — confirm the settings-file half of the deny-default already writes `defaultMode: "dontAsk"` (#487 landed this); the argv fix completes the belt-and-suspenders pair.
- `internal/agentrun/selfcheck/selfcheck.go:1-15` — package doc comment names the argv pair (`--settings <path> --permission-mode default`); update to `dontAsk` so the comment matches reality.
- `cmd/pyry/agent_run_selfcheck.go:85-108` — operator-facing FAIL diagnostic prints the argv pair; update to `dontAsk`.
- `cmd/pyry/agent_run_test.go:407-413` — comment on `TestBuildStreamRunnerClaudeArgs_Shape` describes the PTY argv contract by name (`--permission-mode default` MUST appear); update to `dontAsk` for accuracy.
- Claude CLI docs (referenced from the ticket): https://code.claude.com/docs/en/cli-reference — *"The `--permission-mode` flag … overrides any `defaultMode` settings found in configuration files."* This is why the argv string shadows the settings file's `defaultMode` field today and why the literal must change.

## Context

The ptyrunner production path (#470) passes `--permission-mode default` on every claude spawn. Per the CLI reference, that argv overrides any `defaultMode` in the settings file. Argv enum `default` is "prompt the user before each tool" — not a sentinel for "consult settings file." So every interactive-TUI spawn has been silently overriding the deny-default `dontAsk` mode that `internal/agentrun/settings/settings.go` writes (#487).

In production this collapses to a no-op because `claude -p` runs headless — out-of-allow-list tools land in prompt-mode with no operator to answer, which silently drops. A future model that auto-confirms, or a behavioural shift, turns that silent drop into a real sandbox escape. The dispatcher's claim that the per-agent allow list is enforced is currently advisory.

Per parent #537's empirical probe, `--permission-mode dontAsk` is field-specific: it overrides settings `defaultMode` only, **not** `permissions.allow`. So the fix preserves the per-agent allow lists; only the deny-default mode is double-asserted.

This is the production-impact half of the #537 split. The selfcheck-credibility half is tracked separately and depends on this landing first — its real-claude PASS only proves enforcement once the argv pair actually deny-defaults.

## Design

### Production change

`internal/agentrun/ptyrunner/runner.go` `buildArgs` — change the argv literal `"default"` to `"dontAsk"` on the `--permission-mode` value position.

The rest of the argv shape — `--session-id`, `--settings`, `--append-system-prompt-file`, `--model`, `--effort` — is unchanged. The forbidden flags (`--input-format`, `--output-format`, `--verbose`, `--dangerously-skip-permissions`, `--max-turns`, `--allowed-tools`) remain forbidden. No new arguments, no removals, no ordering changes.

### Belt-and-suspenders, different fabric

CLAUDE.md's pipeline principle "Belt-and-Suspenders Means Different Fabric" applies cleanly:

- **Suspender (production today, since #487):** settings file JSON field `permissions.defaultMode: "dontAsk"` — written deterministically by `internal/agentrun/settings/settings.go:WriteSettings`.
- **Belt (this ticket):** argv `--permission-mode dontAsk` — written deterministically by `buildArgs`.

The two substrates are distinct: a JSON parse failure in claude (a documented `-p` failure mode) leaves the argv flag still enforcing; an argv-parser anomaly leaves the settings JSON still enforcing. Both layers say the same thing; either alone deny-defaults. Neither layer is stochastic, so this satisfies "different fabric."

### No-touch list

The fix is field-specific. The following must NOT change:

- `permissions.allow` handling — argv `dontAsk` does not overwrite the settings allow list (verified empirically per #537).
- The streamrunner argv path (`buildStreamRunnerClaudeArgs` in `cmd/pyry/agent_run.go`). That path uses `--dangerously-skip-permissions` + `--allowed-tools` and explicitly bans `--permission-mode`. It is a separate enforcement model and is out of scope.
- The selfcheck collaborator surface (`internal/agentrun/selfcheck/selfcheck.go` function variables) and the FAIL/PASS rendering shape.
- The `internal/e2e/realclaude/permission_protocol_spike_test.go` knob (`permissionMode` var) — that file is a manually-driven empirical probe; its value is meant to be edited at probe time, not pinned.

### Stale-comment fixups (mechanical, single-line each)

The string `"--permission-mode default"` appears in three comments / user-facing strings that describe what `buildArgs` writes. After the production change the descriptions become wrong. Update each to `"--permission-mode dontAsk"`:

1. `internal/agentrun/selfcheck/selfcheck.go:8` — package doc: `... \`--settings <path> --permission-mode default\` argv pair ...` → `dontAsk`.
2. `cmd/pyry/agent_run_selfcheck.go:93` — operator-facing FAIL message: `passed via --settings <path> --permission-mode default` → `dontAsk`.
3. `cmd/pyry/agent_run_test.go:410` — explanatory comment on `TestBuildStreamRunnerClaudeArgs_Shape`: `The security invariants the old PTY/settings argv pinned (\`--permission-mode default\` MUST appear ...)` → `dontAsk`.

These are doc/string-literal hygiene only. They don't change behaviour or assertions. The streamrunner test itself still bans `--permission-mode` outright and is unaffected.

## Concurrency model

N/A — `buildArgs` is a pure function that constructs a `[]string`. No goroutines, no shared state, no channels.

## Error handling

N/A — `buildArgs` has no failure modes; it always returns a constant-shape slice with fields substituted from `Config`. The downstream `exec.Command` path is unchanged.

## Testing strategy

### Update the existing fixture (required by AC)

`internal/agentrun/ptyrunner/runner_test.go` `TestBuildArgs` — change the literal `"default"` on the `--permission-mode` value position in the `want` slice to `"dontAsk"`. No other change. The forbidden-flag loop, the length check, and the per-index comparison remain.

The test stays structurally identical: same `Config` fixture, same expected argv length, same forbidden-flag set. Only the single string value flips.

### No new tests

This is a literal-string change. The existing `TestBuildArgs` already pins the argv shape position-by-position; flipping the expected value is the test. Adding a separate "dontAsk-specifically" test would duplicate the pinning that `TestBuildArgs` already does.

The empirical end-to-end behaviour (does claude actually deny-default Bash under `dontAsk`?) is the selfcheck's job, not the unit test's. The selfcheck-credibility ticket (sibling of this one, deferred per ticket body) will exercise the live-claude side once this lands.

### Verification commands

Per the AC:

```bash
go vet ./...
go test -race ./...
go build ./...
```

All three must be green. Expected affected tests: only `TestBuildArgs` in `internal/agentrun/ptyrunner`. Nothing in `cmd/pyry/agent_run_test.go` asserts on the literal; the comment fixup there is doc-only.

## Open questions

None. The ticket body resolves the only judgment call (`dontAsk` vs leaving the argv off entirely): keep the argv, set it to `dontAsk`, deliberately as a belt-and-suspenders pair with the settings-file field of the same name. Field-specificity (argv `dontAsk` does NOT overwrite `permissions.allow`) is verified empirically per parent #537's probe table.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings** — the change does not introduce a new
  trust boundary or modify the existing pyry-claude argv boundary
  (`internal/agentrun/ptyrunner/runner.go` `cmd.Args`). The argv string
  `dontAsk` is a hardcoded constant in `buildArgs`; no user input flows
  into the flag value. The settings JSON boundary (separate substrate)
  was hardened in #487 and is unchanged here.

- **[Authorisation model] STRENGTHEN — this is the whole point** — before
  this change, argv `--permission-mode default` shadowed the settings
  file's `defaultMode: "dontAsk"` per the claude CLI reference's documented
  precedence ("argv overrides settings"). The dispatcher's claim of a
  per-agent deny-default sandbox was advisory: out-of-allow-list tools
  hit prompt-mode, which silently drops under headless `claude -p` today
  but is a real escape under any future model that auto-confirms or
  changes prompt-mode behaviour. Post-fix, the argv and the settings
  field BOTH assert `dontAsk` — two distinct deterministic substrates
  expressing the same deny-default. Field-specificity verified
  empirically in parent #537: argv `dontAsk` overrides `defaultMode`
  only, never `permissions.allow`, so the per-agent allow lists are
  preserved.

- **[Tokens / secrets] No findings** — no secrets introduced, read, or
  logged. The argv flag value is a public configuration knob, not
  sensitive material.

- **[Subprocess / exec] No findings** — argv shape is structurally
  identical (same length, same flag positions, same forbidden-flag set).
  No shell invocation, no `sh -c`, no metacharacter risk; the literal
  `"dontAsk"` is a constant. The `exec.Command` call site in ptyrunner
  is unchanged.

- **[File operations] No findings** — no filesystem touch in `buildArgs`.
  The settings file written by `internal/agentrun/settings/settings.go`
  is unchanged.

- **[Cryptographic primitives] No findings** — no crypto.

- **[Network & I/O] No findings** — no network surface added or removed.

- **[Error messages / logs] No findings** — the change does not alter any
  log statement, error wrapper, or operator-facing diagnostic except the
  three stale comment/string-literal fixups, all of which simply replace
  the now-incorrect token `default` with the now-correct token `dontAsk`
  in descriptions of what `buildArgs` produces. No new fields, no
  PromptBytes content, no JSONL content leaked.

- **[Concurrency] No findings** — `buildArgs` is a pure function with no
  shared state; concurrency semantics of the caller (`runner.Run`) are
  untouched.

- **[Threat model alignment] No findings** — the change addresses the
  exact gap the ticket body and parent #537 frame: post-#470 production
  ptyrunner spawns have been silently shadowing the settings-file
  deny-default with argv `default`. The fix completes the
  belt-and-suspenders pair on different fabric (argv string + settings
  JSON field) per CLAUDE.md. The selfcheck-credibility half of #537 is
  explicitly deferred to a sibling ticket; this spec does not preempt
  it.

- **[Adversarial probe — could `dontAsk` itself be MORE permissive than
  `default`?] No** — `default` argv = "prompt the user before each tool"
  (which silently drops under headless `-p`). `dontAsk` argv = "deny
  anything not on the allow list, do not prompt". `dontAsk` is strictly
  more restrictive than `default` for non-allow-listed tools under
  headless execution: today's silent drop becomes an explicit deny.
  Allow-listed tools were already allowed; that is unchanged
  (field-specificity of argv `dontAsk` per parent #537 probe). There is
  no configuration of `default` that `dontAsk` makes more permissive.

- **[Adversarial probe — could a stale-comment fixup leak something?]
  No** — the three fixup sites are: (1) a package doc comment, (2) an
  operator-facing FAIL diagnostic that already prints the argv pair,
  (3) a test-only explanatory comment. None of them prints PromptBytes,
  session IDs, settings paths, or any other sensitive material. They
  describe the production argv shape and were already public; correcting
  the token does not change the surface.

**Reviewer:** architect (self-review per architect prompt §3 security
review pass; the dispatched `security-review.md` file referenced by the
agent prompt is not shipped to the worktree, so the pass was applied
from the categorical checklist directly, modelled on the format used in
prior security-sensitive specs e.g. `473-agent-run-self-check-ptyrunner.md`).
**Date:** 2026-05-28
