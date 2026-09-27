# 038. Codex sessions run in a daemon-owned `CODEX_HOME` with a read-only posture and default-decline approvals

## Status

Accepted (#2620). Partially superseded — see [Superseded in part
(2026-09-25, #2586)](#superseded-in-part-2026-09-25-2586) and
[Superseded in part (2026-09-25, #2587)](#superseded-in-part-2026-09-25-2587)
and [Superseded in part (2026-09-27, #2671)](#superseded-in-part-2026-09-27-2671)
below.

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
`OnServerRequest` is left nil. ~~`SetPermissionMode` on the runner always
returns an error rather than changing anything live, `SetSpawnPermissionMode`
is a no-op, and neither `Restart` nor `SetSpawnArgs` touches the config
file. No runner call can loosen the posture; only rewriting `config.toml`
can, and only the daemon does that.~~ **Superseded — see [Superseded in
part (2026-09-25, #2586)](#superseded-in-part-2026-09-25-2586) below:**
`SetPermissionMode`/`SetSpawnPermissionMode` now store the posture and
every `turn/start` asserts it, so a runner call can loosen the posture —
on the next turn, not live on the running one.

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

- Before #2587, **no Codex session could accept any server request** —
  every approval, tool call and user-input request was declined, regardless
  of what the operator would have said. See [Superseded in part (2026-09-25,
  #2587)](#superseded-in-part-2026-09-25-2587) below for what changed and
  what didn't.
- System-wide or managed Codex configuration outside `CODEX_HOME` (for
  example under `/etc/codex`) is out of scope for this decision and could
  still apply beside it, loosening the effective posture in a way
  `prepareCodexHome` cannot see or prevent. Detecting that at construction
  is #2621's job, not this ticket's.
- The thread id and turn id Codex mints live only on the runner in memory
  (`cmd/pyry/codex_runner.go`'s `threadID`/`turnID`), not in this config or
  in the registry; persisting the thread id across a daemon restart is
  #2622's job.

## Superseded in part (2026-09-25, #2586)

Codex takes model, effort, approval policy, sandbox and approvals reviewer
as overrides on each `turn/start`, so a posture change needs no respawn.
`codexTurnOverrides` (`cmd/pyry/codex_settings.go`) maps the session's
stored `PermissionMode`/`YOLO` to those three fields on every turn, and
`SetPermissionMode`/`SetSpawnPermissionMode` now store the mode instead of
refusing or no-opping. The posture is no longer fixed read-only for a
session's lifetime — an operator's posture pick now actually applies, on
the session's next turn.

What this decision got right and what still stands unchanged:

- The daemon-owned `CODEX_HOME`, its `0700` permissions, and
  `prepareCodexHome`'s unconditional `config.toml` rewrite on every
  construction. That rewrite is now the posture's *baseline before the
  first turn* rather than its permanent value — every turn since #2586
  re-asserts the session's actual posture on top of it.
- The default decline on every server request codexsup itself doesn't route
  elsewhere (`OnServerRequest` left nil in this decision's scope). A looser
  sandbox or approval policy only changes what Codex is *permitted* to
  attempt without asking — see [Superseded in part (2026-09-25,
  #2587)](#superseded-in-part-2026-09-25-2587) for the two methods that now
  route to a real answer instead.
- `auth.json` is still never read, copied, linked or moved by the daemon.

See [codexsup-package.md § Production
wiring](../features/codexsup-package-production-wiring.md#production-wiring--the-cmdpyry-codex-runner-2620)
for the full posture table and the sticky-override reasoning (an omitted
field keeps the thread's previous, possibly looser, override — which is
why every turn asserts all three rather than only the ones that changed).

## Superseded in part (2026-09-25, #2587)

`OnServerRequest` is no longer unconditionally nil. `codex_runner.go`'s
`codexApprovals` wires it to park `item/commandExecution/requestApproval`
and `item/fileChange/requestApproval` on the daemon-wide `permbridge.Registry`
and surface them through the permission modal, the same path a Claude
session's stdio approvals take. What this decision got right and what still
stands:

- The daemon-owned `CODEX_HOME`, its `0700` permissions and the unconditional
  `config.toml` rewrite are unchanged — the sandbox and approval-policy
  *baseline* this decision sets still stands underneath #2586's per-turn
  overrides.
- The default decline is unchanged for the other eight server-request
  methods at 0.156.1, including the legacy `applyPatchApproval`/
  `execCommandApproval` and the experimental `item/tool/requestUserInput` —
  #2587 explicitly kept them out of scope. See the later #2671 supersession
  below for eligible user-input requests.
- `auth.json` is still never read, copied, linked or moved by the daemon.
- Nothing answers `accept` by default: a request is only ever accepted
  through an explicit operator decision resolving the registry entry, and
  every path the operator did not choose (window timeout, interrupt,
  teardown, app-server exit, a request Codex withdrew on its own) still
  answers `decline`, mirroring this decision's original always-decline
  guarantee for those paths.

See [codexsup-package.md § Approvals reach the permission modal
(#2587)](../features/codexsup-package-production-wiring.md#approvals-reach-the-permission-modal-2587)
for the adapter, the two security-review findings that shaped its final
shape (decline rather than partially display a scope-changing field;
mark a truncated display rather than cutting it silently), and the known
test-coverage gap on the file-change correlation path.

## Superseded in part (2026-09-27, #2671)

`codexHomeConfig` now enables `default_mode_request_user_input` in the
daemon-owned `[features]` table, while preserving the approval, sandbox and
reviewer posture. `Client.handshake` declares `experimentalApi`, required by
the granular approval policy in the live Codex path. That capability can
expose experimental API methods and fields; the pinned method registration
and the runner's parser remain the boundary for requests that can reach an
operator.

`codexApprovals` now routes eligible `item/tool/requestUserInput` batches to
the existing question bridge. The bridge accepts an operator answer and the
runner translates it back under Codex's original question IDs. Invalid or
ineligible question batches and unsupported server requests still take the
default-decline path. Refusal and other no-answer terminal paths return an
empty answers object while the request is writable; a request Codex has
already withdrawn is dismissed locally without a response. The daemon-owned
home and the deny-by-default rule for all other requests remain in force.

See [Codex production wiring](../features/codexsup-package-production-wiring.md#codex-questions-reach-the-shared-bridge-2671)
for the eligibility and terminal boundaries.

## Related

- [features/codexsup-package.md](../features/codexsup-package.md) §
  Production wiring, § Deny-by-default — the runner and the decline table
  this decision sits on top of.
- `docs/specs/architecture/2620-codex-pool-runner.md` — the full plan,
  including the Security review this decision matches.
- #2586 — applies the stored posture per turn instead of fixing it read-only; see "Superseded in part" above.
- #2587 — routes command and file-change approvals to the permission modal;
  see "Superseded in part (2026-09-25, #2587)" above.
- #2671 — routes eligible Codex user-input batches to the question bridge;
  see "Superseded in part (2026-09-27, #2671)" above.
- #2621 — construction-time sign-in/version checks, the natural place to
  also detect a looser effective posture from outside `CODEX_HOME`.
