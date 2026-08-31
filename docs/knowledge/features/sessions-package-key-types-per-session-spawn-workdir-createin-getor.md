# Per-session spawn workdir: `CreateIn` / `GetOrCreateIn` (#684)

The pool-level primitive (EPIC #672, split from #681) that lets a session spawn its supervised claude in a directory **other than** the shared `tpl.WorkDir`. Until #684 `buildSession` hard-wired `supervisor.Config{ WorkDir: tpl.WorkDir }` for every session; a forthcoming consumer (#685) needs each per-conversation session to spawn in its conversation's own directory.

```go
func (p *Pool) CreateIn(ctx context.Context, label, spawnDir string) (SessionID, error)
func (p *Pool) GetOrCreateIn(ctx context.Context, id SessionID, label, spawnDir string) (SessionID, error)
```

**`XxxIn` siblings, not functional options.** `Create` / `GetOrCreate` keep byte-identical signatures and shrink to one-line delegators (`=> CreateIn(ctx, label, "")` / `=> GetOrCreateIn(ctx, id, label, "")`); the create/persist/supervise body moves into the `In` variant unchanged. The mechanism mirrors the existing `StartIn` idiom (`internal/e2e/harness.go:201`) — the codebase has zero functional-options precedent, so a framework for one optional string was rejected. Every existing caller (`sessionMinter` `cmd/pyry/main.go:667`, the `sessions.new` verb, `GetOrCreate` in the control server, the `create_conversation` interface caller, all tests) compiles and behaves unchanged with zero churn.

**Spawn-seam conditional.** `buildSession(id, label, spawnDir)` (the seam shared by both public entry points) resolves `workDir := tpl.WorkDir; if spawnDir != "" { workDir = spawnDir }` and sets `supervisor.Config.WorkDir = workDir`. Empty `spawnDir` is byte-identical to today's behaviour (the AC-2 default-fallback). Exposing the option on the shared seam makes it available to whichever public entry point #685 ends up using.

**Survives respawn with no new state.** The workdir lives only in `supervisor.Config`, which the supervisor reads as `cmd.Dir` on **every** (re)spawn (`supervisor.go:638-639`, `spawn.go:40-41`), so a custom spawn dir survives child crash-respawns automatically — no new `Session` field, no registry-schema change. It is **not** persisted to `sessions.json` (a spawn-time input only); surviving a daemon *process* restart would be a separate slice if ever needed.

That "separate slice" arrived as [#1487](../codebase/1487.md) and resolved it *without* a registry-schema change: `spawnDir` is still not persisted on the sessions side, so `Pool.Revive`'s caller sources the directory from the **conversations** registry (`Conversation.Cwd`, the one place it is durable) — see [§ `Pool.Revive`](#reviving-a-dropped-session-poolrevive-1487).

**Opaque path — deliberately not `security-sensitive`.** The pool does **not** `os.Stat`, validate, canonicalise, or trust-check `spawnDir`; it is passed verbatim. An inaccessible directory surfaces at spawn time via the supervisor's existing chdir-failure → backoff path, not here. No untrusted input reaches this slice and its only caller after it still passes the default, so the trust / canonicalisation / `$HOME`-containment work (and the `security-sensitive` label) lives in the consumer #685.

**Take-path drops `spawnDir`.** `GetOrCreateIn` applies the workdir only on the *create* path; on the take path (session already registered) `spawnDir` is ignored — the existing session keeps its own workdir, mirroring the existing take-path label-drop.

**Tests** (`pool_spawndir_test.go`): a cwd-recorder fake claude (`/bin/sh -c 'pwd > "cwd-$2.txt"; exec sleep 3600' --`) writes a per-uuid marker into its own cwd, so the marker's *location* proves the spawn directory with no production accessor added. Existence-check (not content-compare) sidesteps the macOS `/tmp`→`/private/tmp` symlink rewrite. Covers `CreateIn` explicit-dir, plain `Create` template-workdir default-leg, the `GetOrCreateIn` create path, and the take-path-ignores-spawnDir negative case. See [codebase/684.md](../codebase/684.md).
