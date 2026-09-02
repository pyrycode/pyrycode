# Constructing a `streamRunner` — `newStreamRunnerFactory` (#1109, extended #1098, #1168)

`cmd/pyry/streamsup_runner.go` also holds `newStreamRunnerFactory(sink *streamTurnSink, mcpApprovePath
string) sessions.RunnerFactory` — returns exactly the `sessions.RunnerFactory` signature (above); #1109
delivered the constructor (as the bare `streamRunnerFactory` func, the first `streamsup.New` caller
tree-wide), #1098 turned it into this `sink`-capturing constructor so the Parser it installs has somewhere
to send turnevents, and #1168 added the `mcpApprovePath` param to inject the permission-approval flags.
Body of the returned closure: `scfg := mapStreamsupConfig(cfg)`; `scfg.Args =
withApprovalArgs(scfg.Args, mcpApprovePath)`; `parser, held :=
newSessionParser(sink.sinkFor(cfg.SessionID), cfg.Logger)` (#1840, below); `scfg.Stdout = parser`;
`streamsup.New(scfg)`; on error, `fmt.Errorf("cmd/pyry: stream runner: %w", err)` and a genuine nil
`sessions.Runner`; on success, `streamRunner{r: r, models: held}`.

**`withApprovalArgs(args []string, mcpApprovePath string) []string` (#1168, extended #2043)** is the
interactive-stream twin of `agent_run.go`'s non-yolo `permissionArgs` wiring (#1106) — the first live
consumer of `permissionArgs`/`writeMCPApproveConfig` on the interactive path. Reads
`--dangerously-skip-permissions` off `args` as the single deterministic per-spawn yolo signal (both the
bootstrap operator pass-through and `sessions.claudeSettingsArgs`'s per-session YOLO funnel through that
one flag): present → return `args` unchanged (byte-identical to pre-#1168, no duplicate flag); absent →
append the approval set, dropping its own `--permission-mode default` pair first when `args` already
names a mode (below). Runs inside the shared factory closure, so it covers **both** the bootstrap runner
and per-conversation runners — a per-conversation stream session cannot silently bypass the approval
gate. `mcpApprovePath` is the daemon-global `--mcp-config` file `runSupervisor` writes once at startup
via `writeMCPApproveConfig` (gated on `cfg.InteractiveRunner == "stream-json"`, fail-closed on write
error, removed at shutdown); on the `""`/`"pty"` path the factory is never built, so the PTY interactive
argv is untouched. See [pyry-mcp-approve-command.md](pyry-mcp-approve-command.md) and
[codebase/1168.md](../codebase/1168.md).

**Since #2043, `args` can already name a permission mode, and this function runs at runner
CONSTRUCTION on top of it — the path a daemon restart takes to rebuild a session from the registry.**
A session storing `plan` composes `--permission-mode plan` through
`sessions.claudeSettingsArgs`, and injecting the approval set unmodified would spawn it as
`--permission-mode plan … --permission-mode default`. `withApprovalArgs` now drops **only its own**
`--permission-mode default` pair in that case (`namesPermissionMode` checks both the two-token and the
joined `--permission-mode=` forms — the operator's bootstrap pass-through args can spell it either way,
mirroring `stripSessionIDFlags`'s two-form handling; `dropPermissionMode` scans for the pair rather than
slicing a known offset, so `permissionArgs`' own ordering is not load-bearing).

**Rejected shortcut — a second early return, mirroring the yolo arm above
(`if namesPermissionMode(args) { return args }`).** This looks like the natural sibling of the
yolo-present early return but is a privilege escalation: it would spawn every mode-carrying session
with **no** `--permission-prompt-tool`, `--mcp-config`, or `--strict-mcp-config` at all — the daemon's
approval gate entirely absent — reachable from a stored setting alone, no operator action beyond
setting a permission mode. The yolo early return is safe only because a bypass child has no approval
gate to lose in the first place; a mode-carrying child (`plan`, `acceptEdits`, `auto`, `dontAsk`) still
needs one. Caught in the #2043 spec's mandated security review before any code shipped; a test asserts
the three approval flags survive a mode-carrying spawn.

**No PTY fallback, structurally.** The function has no branch that calls `supervisor.New` — a
`streamsup.New` failure (missing binary, absent work dir; an empty `SessionID` is impossible at the pool
sites per #1108) always surfaces as an error rather than silently degrading the bootstrap (the session
`pyry attach` drives) to the PTY path.

**`mapStreamsupConfig(cfg supervisor.Config) streamsup.Config`** is the fully-inspectable mapper and
the primary tested surface — unchanged by #1098, extended by **#1631**. Field mapping: `ClaudeBin`/`WorkDir`/`SessionID`/
`Logger`/`BackoffInitial`/`BackoffMax`/`BackoffReset` copy verbatim; `ClaudeArgs → Args` (streamsup's argv
field has a different name) through `stripSessionIDFlags`; `ClaudeSessionsDir` is derived per runner from
`WorkDir` (below); `Stdout`/`Stderr`/`Env` stay nil **inside the
mapper** (`Stdout` is filled one layer up, in `newStreamRunnerFactory`'s closure — keeping the mapper's
`Stdout == nil` assertion untouched; `Stderr`/`Env` have no `supervisor.Config` analogue). The
seven PTY-only fields (`ResumeLast`, `ResolveSessionID`, `Bridge`, `ValidateConversation`,
`ResolveTranscript`, `RecordDir`, `helperEnv`) are deliberately not mapped — `ResolveSessionID` stays off
the list even now that a sessions *directory* crosses this seam: streamsup still owns its own id-flag
inversion (`useCreateForm`) and decides the flag from the directory itself; no resolver callback crosses,
and no directory scan happens on either side.

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

Neither #1109 nor #1098 wired the factory into production on their own — that was #1081's scope (below).
See [codebase/1109.md](../codebase/1109.md).
