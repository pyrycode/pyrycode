# #2568 — record the resolved working directory, not the raw requested string

## Files read

- `internal/relay/handlers/create_conversation.go` → `CreateConversation`, `SessionCreator`, `ErrSpawnDirRejected` — records `*p.Cwd` verbatim today; the seam returns only a session id.
- `internal/relay/handlers/create_conversation_test.go` → `stubSessionCreator` — the one test double of `SessionCreator`; every test builds it by struct literal, so a signature change touches only its method.
- `internal/relay/handlers/workspace_label_frames_test.go` — also constructs `&stubSessionCreator{}` (literal only, unaffected).
- `cmd/pyry/main.go` → `sessionMinter.Create`, `resolveSpawnDir`, `expandTilde`, `confineWorkdirToHome`, `confineWorkdirToHomeCreating`, `resolveDefaultCwd`, `runSupervisor` (the `conversations.Load` → logger window) — the one production `SessionCreator`, and the resolver whose return value is the spawn dir.
- `cmd/pyry/relay.go` → `resolveWorkspaceDir` — strict confine, no mkdir, no `trustMark`; the resolver the startup rewrite reuses. Also the `handlers.CreateConversation` wiring (unchanged).
- `cmd/pyry/channel.go` → `channelCreator` — already records `resolveSpawnDir`'s output; the behaviour create is aligned to.
- `cmd/pyry/conversation_spawndir_test.go` → `installRecordingTrustMark` — the seam override the new cmd tests reuse.
- `internal/conversations/registry.go` → `Registry`, `registryFile.WorkspaceLabels`, `SetWorkspaceLabel`, `WorkspaceLabel`, `Save` — label keys must byte-equal a row's `Cwd`; the map is guarded by `mu` and never handed out.
- `internal/protocol/codes.go` → `CodeWorkspaceNotFound` doc — the byte-exact key invariant the rekey preserves.
- `docs/knowledge/features/conversation-session-binding.md` § create path — `sessionMinter` is the sole validator; keep it that way.

In-flight overlap check: no other `feature/*` branch touches these files.

## Context

The create path records the phone's raw `cwd` string while the session spawns in `resolveSpawnDir`'s realpath of it, so `default`, `~/pyry-workspace/default` and `/home/pyry/pyry-workspace/default` become three sidebar workspaces for one folder. `pyry channel new` and `change_workspace` already store the realpath. This ticket makes create agree with them and migrates the rows already stored in the drifted form. No ADR needed.

## Design

### 1. Seam returns the resolved dir (handler + minter)

`SessionCreator.Create(ctx, label, spawnDir string) (sessionID, dir string, err error)`.

- `dir` is the directory the session will spawn in: `resolveSpawnDir(spawnDir)`'s return, i.e. `""` for an empty `spawnDir` and the trust-marked realpath otherwise.
- `sessionMinter.Create` returns the `resolved` it already computes. One resolver on the create path, so recorded and spawn dir cannot disagree.
- `CreateConversation`: `cwd` stays `defaultCwd` for a null `p.Cwd`; for a non-null `p.Cwd` it becomes the returned `dir`. The row, the `conversation_created` reply and its `workspace_label` lookup all use that value, so `list_conversations` carries it too.
- Rejection path unchanged: `ErrSpawnDirRejected` → non-retryable `protocol.malformed`, no row.
- `stubSessionCreator` gains a `dir` field; with it empty the stub echoes `spawnDir` (so existing tests that assert the recorded cwd equals the requested one stay meaningful), and a set `dir` lets a test prove the handler records the creator's answer, not the request.

Fan-out: one interface, one production implementation, one test double, one caller.

### 2. Registry rekey primitive (`internal/conversations`)

`func (r *Registry) RekeyCwds(rekey map[string]string) int` — under `mu`, in one critical section:

- every conversation whose `Cwd` is a key of `rekey` gets the mapped value; returns the number of rows rewritten;
- for each old key (iterated in sorted order, for determinism) that has a workspace label: if the new key has no label yet, the label moves to it; otherwise the existing label under the new key wins. The old key is deleted either way.
- Entries with `old == new` are ignored. No Save (caller's concern, matching the package convention). No logging.

"Absolute key wins" falls out: a label already stored under the absolute key is present before any move. Between two legacy keys collapsing into one unlabelled absolute key, the lexicographically first old key's label wins.

### 3. Startup normalisation (`cmd/pyry`)

New file `cmd/pyry/conversation_cwd_normalise.go`:

`normaliseLegacyCwds(reg *conversations.Registry, registryPath string, resolve func(string) (string, error), logger *slog.Logger)`

- Collect distinct `Cwd` values from `reg.List()` that are non-empty and not `filepath.IsAbs` (covers relative and `~`-prefixed). Absolute rows are never touched. Empty `Cwd` is skipped (not a path anyone requested; resolving it would invent one).
- For each, `resolve(cwd)`. Production passes `resolveWorkspaceDir`: `expandTilde` + strict `confineWorkdirToHome` → realpath, no mkdir, no `trustMark`. Relative paths resolve against the daemon's process cwd, the same base create uses.
- On error: leave the rows unchanged; `logger.Warn` with static event `conversations.legacy_cwd_unresolved` and a `rows` count. Never the path, never the error (the confine error names the path).
- Build `rekey` from the successes; `reg.RekeyCwds(rekey)`; if it rewrote anything, `reg.Save(registryPath)`. Save failure → `logger.Error` event `conversations.legacy_cwd_save_failed` with the error (names only the registry file path); the daemon continues with the in-memory rewrite, which the next Save persists. Success → `logger.Info` event `conversations.legacy_cwd_normalised` with `rows`.
- Never returns an error; the daemon always starts.

Called in `runSupervisor` immediately after the logger is built (which is right after `conversations.Load`), before anything else reads the registry.

## Concurrency model

No new goroutines. The startup call runs single-threaded before the relay, control server and sweep loop exist. `RekeyCwds` holds `mu` for its whole mutation; resolution (filesystem syscalls) happens outside the registry lock.

## Error handling

- Create: unchanged mapping (rejected → malformed non-retryable; other → retryable).
- Startup: per-cwd resolution failure isolates to that cwd; Save failure logged; nothing aborts startup.

## Testing strategy

- `internal/relay/handlers/create_conversation_test.go`: a stub whose `dir` differs from the requested cwd → the row, the reply's `cwd` and a subsequent `ListConversations` reply all carry `dir`; null cwd still records `defaultCwd`; rejected cwd test unchanged (no row, malformed, non-retryable).
- `internal/conversations/registry_test.go` (`RekeyCwds`): rows rewritten and counted; absolute rows untouched; label moved to new key; collision where the new key already has a label keeps it and drops the old; two legacy keys into one unlabelled key → sorted-first wins; round-trip through Save/Load.
- `cmd/pyry` new test file:
  - AC2: `$HOME` = temp dir, `t.Chdir($HOME/pyry-workspace)`, `trustMark` stubbed to identity; `sessionMinter`'s resolver (`resolveSpawnDir`) on `default`, `~/pyry-workspace/default` and the absolute form returns byte-equal dirs, and driving the handler with a creator that returns `resolveSpawnDir`'s answer yields three rows with byte-equal `Cwd`.
  - Normalise: relative and `~` rows rewritten to the realpath; label key rekeyed; absolute-key label wins on collision; absolute rows untouched; registry file on disk rewritten; a missing folder and an escaping path left unchanged, logged with the static event, and the captured log output contains neither path.

## Open questions

- Whether a test can drive the real `sessionMinter` (needs a `*sessions.Pool`). If standing a pool up is heavy, the AC2 test uses a creator that calls `resolveSpawnDir` directly, since `sessionMinter.Create` returns exactly that value.

## Client impact (for the PR)

Desktop's workspace-row plus sends the group's cwd back unchanged, so after the rewrite it sends absolute paths and they round-trip. Mobile keeps sending `~/.pyrycode/scratch` and gets an absolute row back; its Settings default-workspace row and desktop's picker "default" pill match by exact string and will no longer match rows created from `~`/relative forms. Client fixes are follow-ups.

## Documentation handoff (pending — documentation stage)

- `docs/protocol-mobile.md` § message-type table, `create_conversation` / `conversation_created` rows: the recorded and replied `cwd` for a non-null request is the daemon's resolved realpath (tilde expanded, absolute, symlink-resolved), not the string sent; a null cwd still records the daemon default.
- `docs/knowledge/features/conversation-session-binding.md` § create path: `SessionCreator.Create` now returns the spawn dir; one-time startup normalisation of legacy relative/`~` cwds and label keys.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — the untrusted wire `cwd` still reaches the filesystem only through `resolveSpawnDir` inside `sessionMinter.Create`; the handler does no path handling and now records the validator's output rather than the raw input, which narrows what is stored. The startup rewrite treats the persisted `Cwd` (a mutable file) as untrusted and runs it through `resolveWorkspaceDir`'s `$HOME` confinement before storing anything; an unresolvable or escaping value is left exactly as it was, which is no worse than today — every spawn from a stored cwd re-validates via `activeSessionStarter.resolveSpawnDir` anyway.
- [Tokens] No findings — no secrets involved; conversation and session ids stay server-minted.
- [File operations] No findings — the startup path uses the strict confiner: no `MkdirAll`, no `trustMark`, so a hostile stored path cannot cause a folder to be created or auto-trusted. `EvalSymlinks` + `withinDir` on both sides handles symlinked ancestors. The residual TOCTOU between rewrite and a later spawn is covered by the spawn-site re-validation. Registry persistence reuses `Registry.Save`'s temp+fsync+rename.
- [Subprocess] No findings — nothing new reaches argv; the stored cwd is used as a chdir target only after re-validation.
- [Crypto] N/A — no randomness or crypto added.
- [Network & I/O] No findings — the reply now carries an absolute `$HOME`-rooted path instead of the client's spelling. Paired clients already receive absolute cwds (`defaultCwd`, channel rows, `change_workspace` replies), so this discloses nothing new to an authenticated device; nothing leaves the AEAD envelope.
- [Logs] SHOULD FIX (addressed in design) — the confine errors embed the path, so the startup Warn must log only a static event and a row count, never `err`; the test asserts the captured log contains neither the raw nor the resolved path. The create path's existing logging is unchanged.
- [Concurrency] No findings — `RekeyCwds` mutates rows and label keys in one `mu` critical section, preserving the "Save copies the map under mu" invariant; the startup call runs before any concurrent reader exists; resolution syscalls run outside the lock.
- [Threat model] No findings — `docs/protocol-mobile.md` § Security model threats are unaffected: no new verb, no new field, no new privilege. Label data loss on collision (legacy label dropped when the absolute key already has one) is the specified behaviour, not a security issue.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-24
