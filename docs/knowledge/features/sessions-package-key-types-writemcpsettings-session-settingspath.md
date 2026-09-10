# `writeMCPSettings` + `Session.settingsPath` (#943, relocated #1518)

```go
func writeMCPSettings(registryPath string, id SessionID) (string, error)
```

Writes a per-session settings file containing exactly
`{"enableAllProjectMcpServers":true}` — no `permissions` key at all — and
returns its absolute path. Fixes the interactive session-pool spawn's version
of the modal claude 2.1.199 renders when the operator has any MCP server
configured (user-level `~/.mcp.json` or project `.mcp.json`): without
`enableAllProjectMcpServers:true`, claude shows "N new MCP servers found in
this project" at startup, the PTY readiness check reports `tuidriver:
unexpected dialog at startup`, and every live turn wedges. Agent-run already
carried this flag (`d10ce87`); the session-pool spawn — the path the phone and
desktop remote heads drive — never did.

**Two branches, selected by `registryPath` (#1518).** `registryPath == ""`
(persistence disabled — the mode `Pool.dataDir()` reports as `""`, and most of
this package's tests build a pool in) keeps #943's original behaviour
byte-for-byte: `os.TempDir()`, random `pyry-session-settings-*.json` name,
`0600`. `registryPath != ""` writes to
`<abs(dir(registryPath))>/session-settings/<id>.json` instead — a per-purpose
subdirectory under the daemon data dir, created on demand at `0700` (mirrors
`archived-sessions/`, see `disposeJSONLLocked`). The file "must outlive every
respawn" (a backoff restart and the #842 live settings-restart both re-exec
`spawnBase` with the same `--settings` path, and nothing re-reads or
re-creates it between spawns) is why it moved: `os.TempDir()` is subject to an
OS age-based reaper that a multi-day-uptime daemon can hit, deleting the file
out from under a live session and reopening the #943 modal wedge — or worse,
crash-looping the child into permanent backoff. `id` is gated on `ValidID` on
the data-dir branch only: a warm-start bootstrap id is decoded straight out of
the registry file with no shape check upstream, and after this change it names
a file, so an unvalidated id could traverse outside the data dir.

The write is atomic on both branches (`os.CreateTemp` in the target dir →
encode → `Sync` → `Close` → `Rename`), the same recipe `saveRegistryLocked`
uses for `sessions.json`. On the data-dir branch the scratch pattern is
`.settings-*.json.tmp` (dotted, so a SIGKILL-orphaned scratch file is never
mistaken for a real settings file, and a directory `*.json` glob counts
sessions exactly). Naming the file `<id>.json` rather than a random suffix
bounds the on-disk set by session count instead of daemon-restart count — a
warm-start restart against the same registry overwrites its own file instead
of accumulating a new one, so no startup sweeper is needed.

**Deliberately not a reuse of `internal/agentrun/settings`**
([agentrun-settings-subpackage.md](agentrun-settings-subpackage.md)). That
writer always stamps `permissions.defaultMode:"dontAsk"` (a headless
deny-default posture) and requires a non-empty `allowedTools` — both wrong for
an interactive operator session, which must keep today's normal tool-prompt
behaviour. `writeMCPSettings` is a duplicate of the tempfile +
cleanup-on-error discipline, not an extraction — consistent with
the project's "resist over-DRY on duplicated primitives" convention (see also
[agentrun-trust-subpackage.md](agentrun-trust-subpackage.md) for the same
pattern applied to workspace trust).

**Wired into `spawnBase`, not `claudeSettingsArgs`.** The file's content is
per-session immutable — it never varies with `Model`/`Effort`/`YOLO` — so it
belongs in `spawnBase`, the settings-free argv base both `Pool.New`
(bootstrap) and `Pool.buildSession` (minted) compose before appending
`claudeSettingsArgs(settings)`. Because `Session.spawnArgs` (the #842
live-restart recompose) and a backoff restart's re-exec both derive from
`spawnBase`, placing `--settings <path>` there means it survives every
respawn automatically — no additional wiring at either recompose site.

**Error handling.** A `writeMCPSettings` failure at construction (`Pool.New`
or `buildSession`) is a hard error, wrapped `sessions: write mcp settings:
%w` — a daemon that started anyway would silently wedge every turn on the
modal, so a loud startup failure is preferred. Once a session is confirmed
live, cleanup is best-effort (`_ = os.Remove(...)`) and runs only after the
child is confirmed dead so a backoff respawn can never race the removal:
`Pool.Remove` calls it after `sess.Evict(ctx)` returns (minted sessions); the
bootstrap is never `Remove`-d, so its file is removed by a `defer` in
`Pool.Run` that fires on ctx cancel / shutdown.

**Every error return between the write and construction's own success also
removes the file (#1518).** Under #943 a handful of tempfile leaks on those
paths were accepted as rare and harmless — `os.TempDir()`'s reaper collected
them eventually. #1518's data-dir relocation made that reaper absent, so the
same leaks became permanent, and both write sites now guard with a
`defer`-and-success-flag (`built := false; defer func(){ if !built {
os.Remove(settingsPath) } }()`, flipped just before the successful return):
`Pool.New` covers its `newRunner` failure and its `saveLocked` failure,
`buildSession` covers its `newRunner` failure. `CreateIn`'s `saveLocked`
rollback (one level up, discarding a freshly `NewID`-minted session) removes
the file too — safe because that id is never reused. `materialise`'s discard
branches (same-id race loser, `saveLocked` rollback, `ErrPoolNotRunning`
rollback) deliberately do **not**: with the id-derived filename, the
discarded session's `settingsPath` is byte-identical to the winner's live
one, so removing it there would delete a live session's settings file and
reopen the #943 modal wedge. See
[docs/specs/architecture/1518-session-settings-under-data-dir.md](../../specs/architecture/1518-session-settings-under-data-dir.md)
§ Error handling for the full enumeration, and
[codebase/1518.md](../codebase/1518.md) for why that asymmetry is a decision,
not an oversight.

**Blast radius beyond `internal/sessions`.** The ACP-embedded pool
(`cmd/pyry acp`, #761) spawns through the same `buildSession`, so three
argv-equality tests there (`TestACP_SessionNew_SpawnsOneInteractiveClaude`,
`TestACP_SessionLoad_ResumesExistingClaude`,
`TestACPConformance_FullSessionDrive`) broke and needed a `stripMCPSettingsPair`
test helper to strip the new `--settings <path>` pair before asserting the
`--session-id`-only interactive-path shape. Any future change to the shared
`spawnBase` composition should check `cmd/pyry` argv assertions too — the
blast radius is not scoped to one package just because the change is.

**A caller's own pass-through `--settings` cannot reach a daemon-spawned
child — LIVE-CONFIRMED 2026-09-10 (#2320).** `Pool.New` and `buildSession`
both compose the child argv as `[operator pass-through] … --settings
<this file's path> …` — the daemon's own flag always lands after whatever
the caller supplied, per this doc's own "Wired into `spawnBase`" paragraph
above. claude 2.1.259 registers `--settings` as a plain single-valued
option with no accumulating parser (unlike `--plugin-dir`, one entry along
in the same table, which advertises itself as repeatable), and a repeated
single-valued option is last-wins — so the daemon's file always wins the
collision, silently. #2320 needed a `UserPromptSubmit` hook to reach a
daemon-spawned child and first tried it as a pass-through `--settings
<rig-file>`; the hook never registered, because it always loses this
race. The route that works is planting the config as **user settings**
under the child's own `HOME` (`<home>/.claude/settings.json`) instead of
fighting this flag — a settings source of its own, not a competitor for
the one `spawnBase` already owns, and unaffected by `--setting-sources`
since pyry passes no such flag. Before betting a future ticket on a
pass-through flag reaching a daemon-spawned child, check whether claude
registers that flag as repeatable — most are not.

`spawnBase` gained a second daemon-written file this way in #2093 —
[`writeSystemPrompt` + `systemPromptText`](sessions-package-key-types-writesystemprompt-systemprompttext.md),
joining `--settings <path>` with `--append-system-prompt-file <path>`
immediately after it. Same composition-site discipline (both sites, survives
every recompose), same test-helper pattern (a second strip pass in
`waitArgv`/`installedArgv`, not a re-audit of the exact-argv assertions that
depend on them) — but a **different cleanup lifecycle**, since that file is
daemon-scoped rather than session-scoped and must not be removed by
`Pool.Remove`. See that document for the divergence.

See [codebase/943.md](../codebase/943.md) and
[docs/specs/architecture/943-interactive-spawn-mcp-settings.md](../../specs/architecture/943-interactive-spawn-mcp-settings.md)
for the original design, and [codebase/1518.md](../codebase/1518.md) and
[docs/specs/architecture/1518-session-settings-under-data-dir.md](../../specs/architecture/1518-session-settings-under-data-dir.md)
for the data-dir relocation and error-path cleanup.
