# 038. Codex sessions run in a daemon-owned `CODEX_HOME` with a read-only posture and default-decline approvals

## Status

Accepted (#2620)

## Context

Codex support's S0 spike (vault "Codex CLI Support - Research", § S0 spike
results) found that the operator's personal `~/.codex` loads Astra at the
highest effort, every personal MCP server, plugins and a notify hook — none
of it appropriate for a daemon-driven session, and none of it something a
pool runner should inherit by accident. `internal/codexsup.Client` is one
process connection with no posture of its own: it sends whatever
`Config.CodexHome` and `StartThread`'s params say, and its default
`OnServerRequest` (nil) already declines every server request
(`serverrequest.go`'s `declineFor`).

#2620 adds the pool-side runner (`cmd/pyry/codex_runner.go`) that actually
spawns Codex for a session. Approvals — routing a request to a real operator
prompt instead of a blanket decline — are explicitly #2587's job and were not
built here. The runner still had to decide, now, what posture a Codex
session runs under before that ticket lands, and how isolated its home
directory is from the operator's own.

## Decision

Every Codex session runs with `CODEX_HOME` set to `<instance
dir>/codex-home`, a directory the daemon owns outright. On every
`codexRunner` construction (`cmd/pyry/codex_home.go`'s `prepareCodexHome`,
called once per session — including a crash respawn, since the factory
builds one runner per session, not one per spawn) the daemon:

- `chmod`s the directory to `0700` (tightening, not just creating);
- rewrites `config.toml` through a temp file and rename, unconditionally,
  to exactly:
  ```
  approval_policy = "on-request"
  sandbox_mode = "read-only"
  approvals_reviewer = "user"
  ```

Nothing else in that directory is touched. The operator signs in once,
out-of-band, with `CODEX_HOME=<dir> codex login`; the resulting `auth.json`
is never read, copied, linked or moved by the daemon.

Every server request keeps `codexsup`'s existing default decline —
`OnServerRequest` is left nil. `SetPermissionMode` on the runner always
returns an error rather than changing anything live, `SetSpawnPermissionMode`
is a no-op, and neither `Restart` nor `SetSpawnArgs` touches the config file.
No runner call can loosen the posture; only rewriting `config.toml` can, and
only the daemon does that.

## Rationale

- **Isolation over configuration.** Reusing the operator's `~/.codex` would
  require stripping or overriding its default model, MCP servers, plugins
  and notify hook one setting at a time, with no guarantee the next Codex
  release doesn't add a fifth thing to strip. A daemon-owned home with a
  daemon-written config is a positive allowlist instead: the file the daemon
  writes is the entire posture, full stop.
- **Rewrite on every construction, not once at daemon start.** A config
  written once could drift — hand-edited, or overwritten by `codex login`
  writing more than `auth.json`. Rewriting it on every runner construction
  makes the read-only posture self-healing instead of a one-time default.
- **Read-only plus always-decline is the correct default while approvals
  don't exist.** #2587 has not shipped a real operator-approval path yet.
  Defaulting to "ask" with no one able to answer would either hang every
  gated action forever or require a fallback accept, which is the one thing
  the deny-by-default posture (already `codexsup`'s policy — see
  [codexsup-package.md § Deny-by-default](../features/codexsup-package.md))
  is built to prevent. `on-request` plus `read-only` plus a forced decline
  means a Codex session can read and reason today, and gains write access
  only once #2587 gives a human something to answer with.
- **No second refusal path.** The runner does not re-implement declines; it
  simply never wires `OnServerRequest` to anything else, so `codexsup`'s
  existing closed-switch decline table (`declineFor`) is the only place a
  server request is answered.

## Consequences

- Until #2587 lands, **no Codex session can accept any server request** —
  every approval, tool call and user-input request is declined, regardless
  of what the operator would have said. A ticket that needs Codex to take a
  write action needs #2587 first, not a change to this runner.
- System-wide or managed Codex configuration outside `CODEX_HOME` (for
  example under `/etc/codex`) is out of scope for this decision and could
  still apply beside it, loosening the effective posture in a way
  `prepareCodexHome` cannot see or prevent. Detecting that at construction
  is #2621's job, not this ticket's.
- The thread id and turn id Codex mints live only on the runner in memory
  (`cmd/pyry/codex_runner.go`'s `threadID`/`turnID`), not in this config or
  in the registry; persisting the thread id across a daemon restart is
  #2622's job.

## Related

- [features/codexsup-package.md](../features/codexsup-package.md) §
  Production wiring, § Deny-by-default — the runner and the decline table
  this decision sits on top of.
- `docs/specs/architecture/2620-codex-pool-runner.md` — the full plan,
  including the Security review this decision matches.
- #2587 — the approvals ticket this posture defers to.
- #2621 — construction-time sign-in/version checks, the natural place to
  also detect a looser effective posture from outside `CODEX_HOME`.
