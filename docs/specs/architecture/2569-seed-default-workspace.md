# #2569 — A new host starts with a Default workspace and a General channel

## Files read

- `cmd/pyry/main.go` → `runSupervisor` — `conversations.Load`, `normaliseLegacyCwds`, the `createChannel` value, `pool.Run` (blocking), the `<-qDone` join the new join sits beside.
- `cmd/pyry/channel.go` → `channelCreator` — resolves/confines/creates/trust-marks the folder via `resolveSpawnDir`, mints, records the realpath as `Cwd`, eager-saves without returning its own save error, returns static refusals.
- `cmd/pyry/conversation_cwd_normalise.go` → `normaliseLegacyCwds` — the startup-pass shape the seed mirrors (one function, static events, daemon keeps running).
- `internal/conversations/registry.go` → `registryFile`, `Registry`, `Load`, `Save`, `WorkspaceLabel`, `SetWorkspaceLabel` — where the marker lives and how `WorkspaceLabels` stays byte-compatible via omitempty.
- `internal/conversations/archive.go` → `ShouldArchive` — promoted rows are never swept, so General cannot vanish by sweep.
- `internal/sessions/pool.go` → `Pool.Mint`, `Pool.Ready` — Mint before Run persists then returns `ErrPoolNotRunning`; Ready closes once Mint will supervise.
- `internal/relay/v2session_handshake.go` → `workspaceRoot` — the one `pyry-workspace` literal, sent to clients as `workspace_root`.
- `cmd/pyry/channel_test.go` → `installIdentityTrustMark`, `newChannelTestRegistry` — test helpers the seed tests reuse.

In-flight overlap: none (no remote `feature/*` branch touches `registry.go`, `main.go` or `v2session_handshake.go`).

## Context

A fresh host has no conversations, so clients draw an empty host row. The host seeds one promoted channel `General` in `$HOME/pyry-workspace/default`, labels that workspace `Default workspace`, and records a marker so it never seeds again. General is an ordinary channel afterwards.

## Design

### Marker (`internal/conversations/registry.go`)

- `registryFile` gains `Seeded bool `json:"seeded,omitempty"``. Absent key decodes as `false` — no migration step; an unseeded registry stays byte-identical to today's file.
- `Registry` gains a `seeded bool` field guarded by `mu`. `Load` copies it in; `Save` snapshots it inside the same `mu` critical section as the conversations and labels, so the marker and the label land in one `Save`.
- `func (r *Registry) Seeded() bool` and `func (r *Registry) MarkSeeded()`. `MarkSeeded` does not Save (the package convention).

### Workspace root (`internal/relay/v2session_handshake.go`)

Rename `workspaceRoot` → exported `WorkspaceRoot` (one production caller in `handleHello`'s accept path). The seed builds its folder as `filepath.Join(relay.WorkspaceRoot(), "default")`, so there is still one `pyry-workspace` literal.

### Seed (`cmd/pyry/workspace_seed.go`, new)

```go
func seedDefaultWorkspace(reg *conversations.Registry, registryPath, root string,
    create func(cwd, name string) (string, error), logger *slog.Logger)
```

1. `reg.Seeded()` → return (nothing to do, nothing logged).
2. `len(reg.List()) > 0` → `MarkSeeded`, `Save`, log `conversations.seed_skipped_existing`. Existing hosts (pyrybox) get the marker and no channel.
3. `root == ""` (no absolute `$HOME`) → log `conversations.seed_failed` with `stage=root`, return without the marker.
4. `create(filepath.Join(root, "default"), "General")` → on error log `stage=create`, return. `channelCreator` has already logged its own detail.
5. `reg.Get(id)` → read the stored row back; key the label on `got.Cwd` (never a rebuilt path — `workspace_labels` keys must byte-equal a stored cwd). A missing row (deleted in between) → log `stage=readback`, return.
6. `SetWorkspaceLabel(got.Cwd, "Default workspace")`, `MarkSeeded()`, `Save` → on failure log `stage=save`, return. Success logs `conversations.seeded` with the conversation id.

Every log line is a static event plus a `stage` string or conversation id. No path and no `err` value: `Save`'s errors name the registry path and `resolveSpawnDir`'s name the workspace. The daemon keeps running on every branch.

On a Save failure the marker is set in memory, alongside the channel and label. A later lazy Save persists all three together; a restart before that sees an empty registry file with no marker and retries — the AC's "next start retries" — or, if the channel's own eager save landed, sees conversations without a marker and just marks. Neither path produces a second General.

### Timing (`cmd/pyry/main.go`)

```go
func seedWhenReady(ctx context.Context, ready <-chan struct{}, seed func()) <-chan struct{}
```

Starts one goroutine that waits for `ready` or `ctx.Done()`; on `ready` it runs `seed`. The returned channel closes when the goroutine exits. `runSupervisor` calls it right after `createChannel` is built, with `pool.Ready()` and the daemon ctx, and joins the returned channel after `<-qDone`. Before Ready, Mint persists a session and returns `ErrPoolNotRunning` — an orphan per start — so seeding strictly waits.

`seedWhenReady` lives in `workspace_seed.go` beside the seed.

## Concurrency model

One extra goroutine, exit on ready-then-seed or ctx cancellation; joined after `pool.Run` returns and ctx is cancelled. If Ready never closes (Run failed early), `cancelCause(nil)` after `pool.Run` releases it. A client creating a conversation concurrently with the seed can race the emptiness check: the outcome is General beside that conversation, both valid — acceptable, and the marker still lands.

## Error handling

| Failure | Result |
|---|---|
| `root == ""` | log `seed_failed stage=root`, no marker |
| folder / confine / trust / mint (`create` error) | log `seed_failed stage=create`, no marker |
| row gone before read-back | log `seed_failed stage=readback`, no marker |
| `Save` | log `seed_failed stage=save`; nothing persisted by this call |
| existing-host `Save` | log `seed_mark_failed`; retried next start (idempotent) |

## Testing strategy

Registry (`internal/conversations/registry_test.go`):
- Seeded round-trip: `MarkSeeded` → `Save` → `Load` reports `Seeded()`; file carries `"seeded": true`.
- Absent key loads as unseeded; an unseeded Save omits the key (byte-compat).

Seed (`cmd/pyry/workspace_seed_test.go`), real `channelCreator` with a fake mint, temp `$HOME`, `installIdentityTrustMark`:
- Empty registry: one promoted conversation named General; cwd == realpath of `$HOME/pyry-workspace/default`; folder exists; label `Default workspace` under that exact cwd; marker set and persisted (reload from disk).
- Marker already set (General renamed / archived / deleted): nothing created, change left in place, mint not called.
- Conversations present, no marker: nothing created, mint not called, marker persisted.
- Create fails (mint returns error): no conversation, no marker on disk or in memory; a second run with a working mint seeds.
- Save fails (registry path under a non-directory): daemon-side no panic; marker not on disk.
- Log lines of failure branches contain no `$HOME` path.

`seedWhenReady`: seed not run before `ready` closes, runs once after; ctx cancelled first → seed never runs and the done channel closes.

## Open questions

- None blocking. `root` is passed in rather than read inside the seed so tests set it without depending on `os.UserHomeDir`'s behaviour.

## Documentation handoff

None required by the ticket. Pending for the documentation stage: the `seeded` registry key and the startup seed may deserve a line in the conversations package overview (`docs/knowledge/features/`).

## Revisions

### 2026-09-24 — fake-daemon e2e harness pre-marks the registry

The plan's testing strategy missed that every fake-daemon e2e test starts a fresh host. With the seed live, six tests that count sessions or conversations on a fresh home failed (`TestChannelNew_E2E_RefusesDirOutsideHome`, three `TestE2E_Restart_*`, `TestSessionsList_E2E_BootstrapOnly`, `TestSessionsRename_E2E_UnknownUUID`): each saw General's row and its bound session.

Fix, in test code only: `spawnWith` in `internal/e2e/harness.go` calls a new `premarkWorkspaceSeeded`, which writes `{"conversations":[],"seeded":true}` for the `test` instance unless a `conversations.json` already exists. A test that wants the seed writes a zero-byte file first; the daemon loads it as empty and unseeded.

Added `TestWorkspaceSeed_E2E_FreshHostGetsGeneral` (`internal/e2e/workspace_seed_test.go`). It is the only test that can prove the timing half of AC1 against a real pool: exactly two sessions (the bootstrap and General's bound one), and General's session not active. Mutation-checked: seeding before `pool.Run` fails it with three sessions, one of them orphaned.
