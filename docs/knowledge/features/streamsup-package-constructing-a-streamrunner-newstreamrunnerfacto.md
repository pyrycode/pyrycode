# Constructing a `streamRunner` — `newStreamRunnerFactory` (#1109, extended #1098, #1168)

`cmd/pyry/streamsup_runner.go` also holds `newStreamRunnerFactory(sink *streamTurnSink, mcpServersPath
string) sessions.RunnerFactory` — returns exactly the `sessions.RunnerFactory` signature (above); #1109
delivered the constructor (as the bare `streamRunnerFactory` func, the first `streamsup.New` caller
tree-wide), #1098 turned it into this `sink`-capturing constructor so the Parser it installs has somewhere
to send turnevents, #1168 added the param (then `mcpApprovePath`) to inject the permission-approval flags,
and #2169 renamed it `mcpServersPath` once the document it names carries a second server (`pyry_files`,
[pyry-mcp-files-command.md](pyry-mcp-files-command.md)) alongside `pyry_approve`.
Body of the returned closure: `scfg := mapStreamsupConfig(cfg)`; `scfg.Args =
withApprovalArgs(scfg.Args, mcpServersPath, cfg.PermissionMode, cfg.OperatorBypass)`; `tag :=
newStreamSessionTag(cfg.SessionID)` (#1133 — the live handle both fan-in lanes read); `parser, held :=
newSessionParser(sink.sinkForTag(tag.ID), cfg.Logger)` (#1840, below); `scfg.Stdout = parser`;
`scfg.OnChildExit = sink.exitForTag(tag.ID)`; `scfg.OnSessionRotate = tag.Rotate` — see [Session rotation
notification](streamsup-package-session-rotation-notification-onsessionrotate.md) for why the tag is
minted here, ahead of both halves, rather than threaded through either signature; `streamsup.New(scfg)`;
on error, `fmt.Errorf("cmd/pyry: stream runner: %w", err)` and a genuine nil `sessions.Runner`; on success,
`streamRunner{r: r, models: held}`.

**`withApprovalArgs(args []string, mcpServersPath, storedMode string, operatorBypass bool) []string`
(#1168, extended #2043, #2065)** is the interactive-stream twin of `agent_run.go`'s non-yolo
`permissionArgs` wiring (#1106) — the first live consumer of `permissionArgs`/`writeMCPServersConfig`
on the interactive path. Through #2043 it read `--dangerously-skip-permissions` off `args` as the
single deterministic per-spawn yolo signal, because that flag's presence or absence *was* the
posture. **#2065 makes the flag unconditional** (`sessions.claudeSettingsArgs` appends it to every
argv, since the posture is now decided in-band rather than by what launches), so a predicate reading
`args` for it answers "yes" for every session — the approval set would stop being injected for
anyone and the daemon's approval gate would quietly disappear fleet-wide. The question this function
answers moved from "is the flag on the argv" to "will this child **keep** the bypass it launches
with", which is true in exactly two cases, both decided before this function runs and passed in
rather than read off `args`: `storedMode == sessions.PermissionModeBypass` (internal/streamsup refuses
to write that mode by non-membership, so nothing walks the child back), or `operatorBypass` — the
escalation came from the operator's pass-through claude args, which never touch `SessionSettings`, so
the daemon may not revoke a grant it did not make. Either → return `args` unchanged (byte-identical to
pre-#2065 on both rows, and avoids re-emitting a flag already present). Otherwise → append the
approval set, dropping its own `--permission-mode default` pair first when `args` already names a
mode (below); this is now a child the daemon downgrades in-band before its first turn, and it needs
the approval gate exactly as it did before #2065.

**"A bypass child has no approval gate to lose" stops holding at #2065.** That sentence used to
justify the yolo-present early return: the flag's presence *meant* the child was staying in bypass, so
there was nothing to inject. After #2065 every child launches with the flag, so the sentence is only
still true of the two rows above — the ones nothing downgrades. For every other child (any stored
in-band mode, no operator grant) the approval gate is very much there to lose, and the row that used
to read "no flag → inject" now reads "flag present, `storedMode` not bypass → inject anyway". That
is the row that reddens on any tree that keeps keying on the bare presence of the flag instead of on
`storedMode`/`operatorBypass` — #2065's code review measured a `go test -overlay` mutant restoring
the old presence check specifically to confirm this row catches it.

**The sessions boundary supplies one escalation flag (#2506).** When `operatorBypass` is true,
the settings-free base may already carry one or repeated pass-through copies while
`sessions.claudeSettingsArgs` contributes its unconditional copy. Sessions' final
`composeSpawnArgs` boundary preserves the first exact token and removes the rest before this
function runs. `withApprovalArgs` still returns that argv unchanged; it does not own deduplication,
and its `namesPermissionMode` / `dropPermissionMode` helpers continue to touch only
`--permission-mode`.

Runs inside the shared factory closure, so it covers **both** the bootstrap runner
and per-conversation runners — a per-conversation stream session cannot silently bypass the approval
gate. `mcpServersPath` is the daemon-global `--mcp-config` file `runSupervisor` writes once at startup
via `writeMCPServersConfig` (gated on `selectsStreamRunner(cfg)`, i.e. every accepted value except
`"pty"` — `""` included — fail-closed on write error, removed at shutdown). Only `"pty"` skips the
write, and that value fails startup in `selectInteractiveRunner` immediately after, so no factory is
ever built over an empty `mcpServersPath`; since #1348 there is no PTY interactive argv left to leave
untouched. See [pyry-mcp-approve-command.md](pyry-mcp-approve-command.md),
[codebase/1168.md](../codebase/1168.md) and
[`SessionSettings` / `claudeSettingsArgs`](sessions-package-key-types-sessionsettings-claudesettingsargs.md)
for the `OperatorBypass` provenance bit this function now consumes.

**Since #2043, `args` can already name a permission mode, and this function runs at runner
CONSTRUCTION on top of it — the path a daemon restart takes to rebuild a session from the registry.**
A session storing `plan` composes `--permission-mode plan` through
`sessions.claudeSettingsArgs`, and injecting the approval set unmodified would spawn it as
`--permission-mode plan … --permission-mode default`. `withApprovalArgs` now drops **only its own**
`--permission-mode default` pair in that case (`namesPermissionMode` checks both the two-token and the
joined `--permission-mode=` forms — the operator's bootstrap pass-through args can spell it either way,
mirroring `stripSessionIDFlags`'s two-form handling; `dropPermissionMode` scans for the pair rather than
slicing a known offset, so `permissionArgs`' own ordering is not load-bearing).

**Rejected shortcut — a second early return, mirroring the stay-in-bypass arm above
(`if namesPermissionMode(args) { return args }`).** This looks like the natural sibling of that
early return but is a privilege escalation: it would spawn every mode-carrying session
with **no** `--permission-prompt-tool`, `--mcp-config`, or `--strict-mcp-config` at all — the daemon's
approval gate entirely absent — reachable from a stored setting alone, no operator action beyond
setting a permission mode. The stay-in-bypass early return was safe at #2043 because the flag's
presence meant the child had no approval gate to lose in the first place; since #2065 that early
return is keyed on `storedMode`/`operatorBypass` rather than the flag (above) for exactly this
reason — a mode-carrying child (`plan`, `acceptEdits`, `auto`, `dontAsk`) still needs the gate, and
the flag alone can no longer tell the two apart. Caught in the #2043 spec's mandated security review before any code shipped; a test asserts
the three approval flags survive a mode-carrying spawn.

**No PTY fallback, structurally.** The function has no branch that calls `supervisor.New` — a
`streamsup.New` failure (missing binary, absent work dir; an empty `SessionID` is impossible at the pool
sites per #1108) always surfaces as an error rather than silently degrading the bootstrap (the session
`pyry attach` drives) to the PTY path.

**`mapStreamsupConfig(cfg supervisor.Config) streamsup.Config`** is the fully-inspectable mapper and
the primary tested surface — unchanged by #1098, extended by **#1631** and **#2169**. Field mapping: `ClaudeBin`/`WorkDir`/`SessionID`/
`Logger`/`BackoffInitial`/`BackoffMax`/`BackoffReset` copy verbatim; `ClaudeArgs → Args` (streamsup's argv
field has a different name) through `stripSessionIDFlags`; `ClaudeSessionsDir` is derived per runner from
`WorkDir` (below); `SessionIDEnvVar` is set to the constant `envSessionID` (`pyry_files`'s
`PYRY_SESSION_ID`, [pyry-mcp-files-command.md](pyry-mcp-files-command.md)); `Stdout`/`Stderr`/`Env` stay nil **inside the
mapper** (`Stdout` is filled one layer up, in `newStreamRunnerFactory`'s closure — keeping the mapper's
`Stdout == nil` assertion untouched; `Stderr`/`Env` have no `supervisor.Config` analogue). The
six PTY-only fields (`ResumeLast`, `ResolveSessionID`, `Bridge`, `ValidateConversation`,
`ResolveTranscript`, `helperEnv`) are deliberately not mapped — streamsup has no PTY bridge, no
transcript binding and no `.cast` recorder (`RecordDir` was in this list until #1514 deleted the
field itself: nothing read it after #1348 removed the terminal recorder). `ResolveSessionID` stays off
the list even now that a sessions *directory* crosses this seam: streamsup still owns its own id-flag
inversion (`useCreateForm`) and decides the flag from the directory itself; no resolver callback crosses,
and no directory scan happens on either side.

**Why `SessionIDEnvVar` (a name) and not `Env` (a value) — #2169's MUST FIX.** The mapper runs once per
runner *construction*, and `cfg.SessionID` is that construction-time seed — it does not move when
`RestartFresh` rotates the runner's live id. A first cut composed `Env: []string{envSessionID + "=" +
cfg.SessionID}` here directly; every child spawned after a `new_session` rotation then carried the
*retired* id on its environment while its argv (built fresh each spawn from the live id) carried the
current one, and `attachment.file` refused for the rest of that session's life. The fix keeps the mapper
construction-time-only by design and pushes the per-spawn composition down to where a live id actually
exists — see [`buildArgs`/`spawnEnv`](streamsup-package-buildargs-the-id-flag-inversion-that-keeps.md) and
[pyry-mcp-files-command.md](pyry-mcp-files-command.md) § "Session id" for the full shape and for the one
gap the environment can never close (a `/clear` rotation without a respawn).

**`streamClaudeSessionsDir(workdir string) string` (#1631) arms `useCreateForm` on the production path.**
Three arms: `workdir == ""` → `""` (unreachable at both pool sites, both carry a confined realpath, but
named explicitly rather than left to fall through); `agentrun.ResolveWorkdir(workdir)` erroring → `""`
(introduces no new failure — `streamsup.New` calls the same function on the same value and would have
failed to construct a runner at all); otherwise → `sessions.DefaultClaudeSessionsDir(resolved)` verbatim.
`""` on any arm means "no probe" — `useCreateForm`'s empty-directory mode above, i.e. pre-#1631 argv.

**Derived per runner, not once per pool — this is the load-bearing decision, not an implementation
detail.** A per-conversation runner's `WorkDir` is the phone's confined spawn dir when one was requested
(`Pool.buildSession`), and legitimately differs from the bootstrap workdir. A pool-global directory
computed once from the bootstrap workdir would be *wrong* for exactly the phone-created sessions — and
because a directory that's set makes `useCreateForm` decide **outright** (the latch is never consulted),
a wrong directory doesn't waste one spawn and self-heal the way the latch does; it reads "absent" for a
session whose transcript exists, spawns `--session-id` against a live transcript, claude refuses it (ADR
032), and the daemon loops permanently in the mirror direction — the same defect class this ticket exists
to close, caused by the fix. **Rejected alternative:** a new field on `sessions.RunnerConfig`, populated
at both pool construction sites. Costs three production files instead of one and adds a second source of
truth for a value that's a pure function of `WorkDir` — already on that struct — populated at two sites
instead of derived at the one place (`mapStreamsupConfig`) that already builds the streamsup config. A
future third construction site could forget the second field and silently ship an inert probe; the mapper
derivation can't drift that way because there's only one derivation site.

**Why `agentrun.ResolveWorkdir`, not `resolveClaudeSessionsDir` or a bare `sessions.DefaultClaudeSessionsDir`.**
`streamsup.New` sets the child's `cmd.Dir` to `agentrun.ResolveWorkdir(WorkDir)`, and claude encodes its
own resolved cwd into the projects folder name — so the probe must key on the *same* resolution as the
child's actual cwd, not a same-looking sibling. [#1655](session-transcript-and-resume-probe.md) measured
the empirical transcript directory against exactly this composition and found them equal. The delta
between the candidates is `canonicalCase`, which `ResolveWorkdir` applies and a bare
`DefaultClaudeSessionsDir` does not; neither `confineWorkdirToHome` (bootstrap) nor `resolveSpawnDir`
(phone) canonicalises case upstream, so on a case-insensitive filesystem a wrong-cased workdir could
survive to the pool intact while the child still lands in the canonical-case cwd, and a probe built
without `canonicalCase` would read a directory claude never writes — the permanent-loop mirror failure
above.

**Test-coverage caveat, from code review (2026-08-20): the shipped discriminator test pins the
per-runner-vs-pool-global property, not the `canonicalCase` half.** A per-runner derivation built on the
*wrong* transform — `sessions.DefaultClaudeSessionsDir(cfg.WorkDir)` directly, skipping
`ResolveWorkdir` — still passes `TestMapStreamsupConfig_ClaudeSessionsDir`, because on an ordinary tmpdir
path (no case difference) `DefaultClaudeSessionsDir`'s own `EvalSymlinks` already matches
`ResolveWorkdir`'s output; only a case-difference fixture would separate them, and no such fixture exists
here (§ Open questions in the #1631 spec declines to gate the build on it — no real-world divergence has
been observed). So: the "each runner gets its own directory" property is genuinely pinned; "that directory
is computed via `ResolveWorkdir`, not a cheaper sibling" is not. A future edit that quietly drops
`canonicalCase` from this composition will not be caught by this test.

**`stripSessionIDFlags(args []string) []string`** returns a fresh slice — never mutating the input, which
is aliased into the pool's `spawnBase` — with every `--session-id`/`--resume` occurrence removed (two-token
form, joined `--flag=value` form, and a dangling flag with no following token all handled). `buildArgs`
(above) re-injects `--session-id <id>` on first spawn / `--resume <id>` on respawn from `Config.SessionID`
itself, so an un-stripped id flag in `Args` would double-inject. Required at the per-session site
(`Pool.buildSession` bakes `--session-id <id>` into `ClaudeArgs`); a harmless no-op at the bootstrap site
(`Pool.New`'s `ClaudeArgs` carry no id flag).

**Neither shaping stayed construction-only past #2446.** `Pool.UpdateSettings` recomposes a session's
spawn argv from `Session.spawnBase` plus `claudeSettingsArgs`
([`Pool.UpdateSettings`](sessions-package-key-types-pool-updatesettings.md)) and installs it through
`streamRunner.SetSpawnArgs`/`.Restart` — a *second* call site for both `stripSessionIDFlags` and
`withApprovalArgs`, distinct from the one this section describes. Before #2446 that install forwarded the
pool's composition to `streamsup.Runner` verbatim: the baked `--session-id` survived past construction, so
a respawn taking the resume form spawned `--session-id X … --resume X`, which claude refuses outright
(exit 1); and the approval set was simply absent from a session the daemon downgrades in band, masked
until the id defect was fixed made it reachable. The fix is a `*settingsInstaller` field on `streamRunner`
(`cmd/pyry/streamsup_runner.go`), built in this factory from the *same* four values already bound here —
`mcpServersPath`, `approval.stdio`, `cfg.OperatorBypass`, `cfg.PermissionMode` — so the construction path
and the settings-update install path can never be handed different shaping inputs. `SetSpawnPermissionMode`
now also records the session's current stored posture on the installer (behind a leaf mutex), because that
is the one shaping input that moves after construction; `Pool.UpdateSettings` calls it unconditionally
immediately before either install branch, so the posture the installer shapes with is always the one that
call just persisted.

**Reading `operatorBypass` back off the argv being installed would reintroduce the exact bug the strip
fixes.** `claudeSettingsArgs` appends `--dangerously-skip-permissions` to every composition unconditionally
(#2065, above), so a predicate keyed on the installed argv would answer "this child keeps its bypass" for
every session and `withApprovalArgs` would return every argv unchanged — the approval gate silently absent
from every install, not just the operator-bypass ones. `operatorBypass` and the mcp/stdio values travel in
fields settled once at construction and are never re-derived from the argv on the install path; only the
posture cell moves, and it moves through `SetSpawnPermissionMode`, never by inspection of `args`.

Neither #1109 nor #1098 wired the factory into production on their own — that was #1081's scope (below).
See [codebase/1109.md](../codebase/1109.md).
