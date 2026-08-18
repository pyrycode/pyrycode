# Spec — #1518: write the per-session `--settings` file under the daemon data dir

**Size:** S (PO-sized S; architect confirms). Two production files (`internal/sessions/settings.go`,
`internal/sessions/pool.go`), one function signature change with 5 call sites (2 production, 3 in
`settings_test.go`), no new exported types. Projected ~70 production LOC + ~300 test LOC.

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_node` / `codegraph_search`.

| File | Symbol | What to extract |
|---|---|---|
| `internal/sessions/settings.go` | `writeMCPSettings`, `mcpSettingsFile` | The whole file. This is the function being rewritten; its doc comment states both halves of the contradiction the ticket fixes. |
| `internal/sessions/pool.go` | `New` | Write site 1. Note `bootstrapID` is in hand before the write, `p` is late-bound (does **not** exist yet), and there are **two** error returns after the write — the `newRunner` failure *and* the `p.saveLocked()` failure. |
| `internal/sessions/pool.go` | `buildSession` | Write site 2. `id` is a parameter. One error return after the write (`p.newRunner`). |
| `internal/sessions/pool.go` | `dataDir` | The `filepath.Dir(registryPath)` derivation this change mirrors, plus the `""`-means-persistence-disabled convention. |
| `internal/sessions/pool.go` | `Remove` | The minted-session removal of `sess.settingsPath`, and the "leaked tempfile in os.TempDir is harmless" comment that this change falsifies. |
| `internal/sessions/pool.go` | `Run` | The bootstrap-teardown `defer`, carrying the same now-false comment. |
| `internal/sessions/pool.go` | `disposeJSONLLocked` | The `archived-sessions` precedent: `filepath.Join(dataDir, ...)` + `os.MkdirAll(dir, 0o700)` on demand. Match its shape. |
| `internal/sessions/pool.go` | `CreateIn` | The `saveLocked`-failure rollback that discards a freshly built session — a post-`buildSession` orphan site (see § Error handling). |
| `internal/sessions/get_or_create.go` | `materialise` | The same-id race path. Its discard branches must **not** remove the settings file — see § Error handling, "the trap". |
| `internal/sessions/registry.go` | `saveRegistryLocked` | The house atomic-write recipe (`MkdirAll` → `CreateTemp` in the same dir → encode → `Sync` → `Close` → `Rename`). Copy the shape. |
| `internal/sessions/registry.go` | `pickBootstrap`, `loadRegistry` | Proof that a warm-start bootstrap id is decoded from disk with **no** shape validation, and that `loadRegistry` treats a malformed registry as a hard error. Both facts drive the `ValidID` gate below. |
| `internal/sessions/id.go` | `ValidID`, `NewID` | The canonical UUIDv4 shape check. 36 chars, lowercase hex, dashes at 8/13/18/23 — contains no `/` and no `.`, which is what makes it usable as a filename. Note there is a second `ValidID` in `internal/conversations`; the package-local one is the one you want. |
| `internal/sessions/session.go` | `settingsPath` field | Its doc already promises an **absolute** path. Keep that promise (see § Design, step 3). |
| `internal/sessions/settings_test.go` | `TestWriteMCPSettings_ShapeAndContent`, `TestWriteMCPSettings_DistinctPaths` | The two tests that break on the signature change, and the shape assertions to preserve. |
| `internal/sessions/pool_mcp_settings_test.go` | `helperPoolArgvRecorder`, `settingsArgPath`, `assertMCPSettingsFile`, `TestPool_Remove_CleansUpMintedSettingsFile`, `TestPool_Run_CleansUpBootstrapSettingsFile` | Reusable helpers, and the two teardown tests that must stay green **unmodified**. |
| `internal/sessions/runner_test.go` | `fakeRunner`, `testRunnerFactory`, `TestRunnerFactory_InvokedAtEveryConstructionSite` | The no-`RegistryPath` pool construction (AC #2's protected path) and the factory-injection pattern AC #5's tests reuse. |
| `cmd/pyry/main.go` | `resolveRegistryPath` | Its documented `$HOME`-unresolvable fallback returns a **CWD-relative** path. This is why absolutisation is not hypothetical. |

## Context

`writeMCPSettings` writes the per-session `--settings` file into `os.TempDir()`, while its own doc
comment states the file "must outlive every respawn". The path is baked into `spawnBase`, the
immutable argv every backoff restart, idle-evict reactivation, and #842 settings-restart re-execs
with; nothing re-reads or re-creates the file between spawns. A daemon running for days can have the
file age-reaped underneath a live session, after which the next respawn passes `--settings` pointing
at a missing path — the #943 MCP-enablement modal wedge returns, or the child fails at boot into
permanent respawn backoff.

Moving the file to the daemon's own data directory removes the reaper from the picture. It also
removes the reaper as an accidental garbage collector, which is why the orphan-cleanup work
(AC #4, AC #5) ships in the same ticket rather than after it.

Three properties change ownership of the file, and all three are load-bearing together:

1. **Location** — under the data dir, so no OS reaper touches it.
2. **Name** — derived from the session id, so a restart against the same registry overwrites rather
   than accumulates. This is what makes AC #4 hold by construction instead of by a sweeper.
3. **Failure cleanup** — every error return between the write and the successful return removes the
   file, because in the data dir an orphan is permanent.

## Design

### 1. `writeMCPSettings` takes the registry path and the session id

```go
// internal/sessions/settings.go
func writeMCPSettings(registryPath string, id SessionID) (string, error)
```

Signature only — no new type, no new exported name, no second helper. Both call sites already hold
the registry path (`cfg.RegistryPath` in `New`, `p.registryPath` in `buildSession`), so nothing new
is plumbed. The `""`-means-persistence-disabled derivation is duplicated from `Pool.dataDir` rather
than shared with it, because `Pool.New` writes the bootstrap file *before* the `*Pool` literal
exists. Say so in the doc comment; that duplication is the reason this is a package function and not
a method.

Behaviour, in order:

1. **`registryPath == ""` → today's path, unchanged (AC #2).** The file goes to `os.TempDir()` with
   the existing `pyry-session-settings-*.json` pattern and the existing `0600` mode. No directory is
   created, no id is consulted. This is the branch the 17 `internal/sessions` test files that build a
   pool without a `RegistryPath` take, and it must stay observably identical.
2. **`registryPath != "" && !ValidID(string(id))` → error.** See § Error handling, "why the id is
   validated". Wrap with `sessions:` per house style and quote the offending id with `%q`.
3. **Otherwise:** the target is `<abs(dir(registryPath))>/session-settings/<id>.json`.
   - `session-settings/` mirrors `archived-sessions/` (see `disposeJSONLLocked`) — a per-purpose
     subdirectory, created on demand at `0700`, so `sessions.json`'s neighbours stay legible and a
     future sweeper has one directory to walk.
   - Absolutise with `filepath.Abs` before use. `resolveRegistryPath` documents a CWD-relative
     fallback when `$HOME` is unresolvable, and a relative `--settings` value would be resolved by
     claude against the *session's* spawn workdir, not pyry's cwd — pointing the child at a
     nonexistent file, which is exactly the #943 wedge this file exists to prevent. `os.CreateTemp("")`
     is absolute today, so this hazard is introduced by the relocation and must be closed by it.
     `Session.settingsPath`'s doc already promises an absolute path.
   - `os.MkdirAll(dir, 0o700)` (AC #3). Cold start needs it: the data dir is created today only by
     `saveRegistryLocked`, which has not necessarily run when `New` writes the bootstrap file.

**Write atomically**, matching `saveRegistryLocked` and PROJECT-MEMORY's named "atomic-write recipe
for on-disk registries": `os.CreateTemp` in the target directory with the pattern
`.settings-*.json.tmp` → encode the `mcpSettingsFile` payload → `Sync` → `Close` → `Rename` into
place, with a `defer os.Remove(tmp)` that is a harmless no-op once the rename has consumed the name.
The pattern mirrors `saveRegistryLocked`'s `.sessions-*.json.tmp` and is load-bearing twice: it keeps
a scratch file from ever being mistaken for a session's settings file, and it is what lets AC #4's
count assertion glob `*.json` unambiguously. Two reasons beyond convention, both introduced by this
change:

- Deterministic naming makes two concurrent writers of the *same* path possible (`buildSession` runs
  off `p.mu`, so two same-id `materialise` callers both write `<id>.json`). A truncate-in-place
  writer would give a live child a window to read a half-written file; a rename gives it either the
  old complete file or the new complete file.
- `os.Rename` replaces a symlink at the destination rather than writing through it. An
  open-truncate-write would follow it.

Structure the function so the encode happens once: resolve `dir` (empty string ⇒ `os.CreateTemp`'s
own `os.TempDir()` contract) and `final` (empty ⇒ keep the temp name) up front, then run one
create/encode/sync/close sequence, then rename only when `final != ""`. This keeps the whole function
under ~40 lines with a single payload site.

The payload (`mcpSettingsFile`, both fields `true`) does not change. Neither does the `0600` file
mode: `os.CreateTemp` creates at `0600` and `os.Rename` preserves it, so AC #3's
"not group- or world-readable" holds without an explicit `Chmod`. The mode matters for **integrity,
not confidentiality** — the content is two public booleans, but any local user who could write this
file could put `hooks` or `permissions` keys into a file pyry hands to claude as `--settings`, which
is arbitrary code execution as the operator.

That integrity property is also why the path becoming *predictable* (`<uuid>.json` instead of a
random temp name) is not a weakening. `os.CreateTemp`'s randomness was never the control — the
`0600` file inside a `0700` directory is, and this change strengthens the surrounding directory from
a world-writable `/tmp` to an operator-only one. A pre-planted file at the predictable path is
destroyed by the `Rename` immediately before every spawn, and planting it requires the write access
to `~/.pyry/<name>/` that the `0700` mode denies.

### 2. Why the id-derived name satisfies AC #4

The filename is a pure function of the session id, so the on-disk set is keyed by session, not by
process:

- **Bootstrap, warm start:** `pickBootstrap` returns the persisted id, so restart *N* writes the same
  `<id>.json` it wrote on restart *N-1*. One file, overwritten.
- **Bootstrap, cold start:** a fresh id, one file.
- **Revived sessions** (#1487/#1488) go through `materialise` with the persisted id — same filename.
- **A session that existed at shutdown and is never revived** leaves one file. That is one per
  session id, never one per restart.

No sweeper is needed, which is what makes the ticket's "startup sweep is out of scope" note hold.

### 3. Call-site changes

Both sites keep their existing comment block (the #943 rationale) and gain the ticket number. The
write becomes:

- `Pool.New` — `writeMCPSettings(cfg.RegistryPath, bootstrapID)`. The id is minted or loaded in the
  branch immediately above the write; no reordering is required.
- `buildSession` — `writeMCPSettings(p.registryPath, id)`. `id` is the parameter.

### 4. Comments that become false and must be updated

Not optional — a stale comment here actively misdirects the next reader about whether a leak matters.

- `Pool.Remove`'s removal block: "a leaked tempfile in os.TempDir is harmless" → the file is now in
  the data dir and a leak is permanent; the removal is the thing that keeps AC #4's bound honest.
- `Pool.Run`'s bootstrap-teardown `defer`: same sentence, same fix.
- `writeMCPSettings`' own doc comment: it currently justifies `os.TempDir` with "the data dir is
  empty in test mode". Rewrite it to state the two branches and why the persistence-disabled branch
  still exists.

`Session.settingsPath`'s doc needs no change — it already says "absolute path" and names both
teardown sites, and both statements stay true.

## Concurrency model

No new goroutines, no new locks, no change to any lock order.

- `buildSession` still runs off `p.mu` (both `CreateIn` and `materialise` deliberately build before
  taking the lock). Deterministic naming means two same-id builders now write the same path
  concurrently; the atomic rename above is what makes that safe, and it is the only concurrency
  consequence of this change.
- `Pool.New` writes before any goroutine exists.
- Nothing reads the file inside pyry. The only reader is the claude child at startup, which is
  spawned strictly after the write returns.

## Error handling

### Cleanup on every error return after the write

At **both** write sites, guard with a deferred removal keyed on a success flag set immediately before
the successful return:

- `Pool.New` — covers the `newRunner` failure named in AC #5 **and** the `p.saveLocked()` failure the
  filing did not name. Sweeping the function for `return nil, err` between the write and `return p, nil`
  finds exactly those two.
- `buildSession` — covers the `p.newRunner` failure. Only one error return exists there today; the
  deferred shape is used anyway so a future error return is covered by construction. This ticket
  exists because a hand-placed cleanup was forgotten at two sites; the defer is the fix that does not
  need to be remembered a third time.

Also remove the file in `CreateIn`'s `saveLocked`-failure rollback — the branch that deletes the
just-registered session from `p.sessions` and returns. This is **beyond AC #5** and deliberate: the
id there is freshly minted by `NewID`, so it is never reused, so in the data dir that orphan is
permanent and unbounded across retries. It is the same argument the ticket uses to pair AC #5 with
AC #1. One line, with a comment saying why this site is safe and the `materialise` ones are not.

### The trap: do not clean up in `materialise`'s discard branches

`materialise` builds a session, then takes `p.mu` and may discard it — the same-id race loser
(`existing, true, nil`), the `saveLocked` rollback, and the `ErrPoolNotRunning` rollback. It is
tempting to symmetrise these with `CreateIn`. **Do not.** With id-derived naming, the discarded
session's `settingsPath` is *byte-identical to the winner's*, and the winner's `spawnBase` references
it. Removing it there deletes a live session's settings file and re-opens #943. The file the loser
wrote is the file the winner needs — same path, same fixed content, so the redundant write is
harmless and the correct action is to leave it.

Add a comment at the race-loser branch recording this, so the asymmetry with `CreateIn` reads as a
decision rather than an oversight.

### Why the id is validated

`pickBootstrap` returns `entry.ID` straight out of the decoded registry file. No `ValidID` runs on
it anywhere on the warm-start path. Today that string never reaches a path join in `New`; after this
change it names a file. An id containing `../` would place the write outside the data dir.

The registry is `0600` operator-owned state, so this is not a privilege boundary an attacker crosses
without already having the operator's file access — it is corruption-robustness, not an exploit. But
the check is three lines, `ValidID` is the package's canonical shape validator (and PROJECT-MEMORY
names "caller-supplied id validation at the primitive boundary" as a project convention), and
`loadRegistry` already documents "a malformed file is a hard error (operator must fix or remove)".
Failing loudly on a malformed id is consistent with that posture; silently falling back to a temp
file would hide a corrupt registry.

The check is scoped to the `registryPath != ""` branch. The persistence-disabled branch performs no
path join, and gating it would change AC #2's protected behaviour for tests that pass hand-made ids.

### Error text

Wrap per house style — `fmt.Errorf("sessions: ...: %w", err)`. `New`'s existing
`"sessions: write mcp settings: %w"` wrapper stays, so a failure is still fatal at startup: a daemon
that started anyway would silently wedge every turn on the modal.

## Testing strategy

Scenarios, not code. Every assertion below is a `t.Run` row or a small standalone test in the
project's existing table-driven idiom.

### `internal/sessions/settings_test.go`

- **Update `TestWriteMCPSettings_ShapeAndContent`** — pass `("", id)` for the temp branch; keep every
  existing assertion (absolute path, `enableAllProjectMcpServers`, `skipDangerousModePermissionPrompt`,
  no permission-posture tokens). Add a second row that passes a real registry path and asserts the
  identical shape, proving the payload is branch-independent.
- **Update `TestWriteMCPSettings_DistinctPaths`** — the invariant it protects ("removing one file
  never orphans another") is per *session*, not per *call*. Two different ids under the same registry
  path must give distinct paths; two calls with `("", id)` must give distinct paths. Restate the doc
  comment accordingly.
- **New — data-dir path and modes (AC #1, AC #3).** Registry path under a `t.TempDir()` whose
  `session-settings/` does not pre-exist. Assert: returned path equals
  `<dataDir>/session-settings/<id>.json`; it is absolute; the directory's permission bits are exactly
  `0700`; the file's permission bits have no group or world bit set.
- **New — same id is a stable path (AC #4).** Two calls, same id, same registry path: identical
  returned path, exactly one entry in the settings directory, content still valid after the second
  write.
- **New — non-canonical ids are rejected.** Table with `"../escape"`, `"a/b"`, `""`, `"not-a-uuid"`:
  each returns an error and creates nothing anywhere under the data dir's parent. **Include a
  control row with a real `NewID()` value that succeeds** — a rejection table whose every row errors
  proves nothing if the function is broken outright.
- **New — a relative registry path still yields an absolute settings path.** Use `t.Chdir` (Go 1.26)
  into a `t.TempDir()` and pass a relative registry path.

### `internal/sessions/pool_mcp_settings_test.go`

`helperPoolArgvRecorder` already builds pools with a real registry path, so it exercises the data-dir
branch directly.

- **New — the bootstrap's `--settings` argv value lives under the data dir (AC #1).** Read the path
  out of the recorded argv with the existing `settingsArgPath`, assert `<dataDir>/session-settings`
  is its directory and `<bootstrapID>.json` its base, and reuse `assertMCPSettingsFile` on it.
- **New — the minted session's argv value likewise (AC #1)**, keyed on the minted id.
- **New — restarts are bounded (AC #4).** Call `New` three times against the same registry path
  without running the pool; assert the settings directory holds exactly one `*.json` entry (glob,
  not a raw dir count — the scratch pattern is `.settings-*.json.tmp` and must not be counted). This
  is the test that goes red if the name reverts to a random suffix.
- **New — `Pool.New` runner failure leaves nothing behind (AC #5).** A `RunnerFactory` that returns
  an error; assert `New` errors and the settings directory contains zero files (a still-absent
  directory counts as zero).
- **New — `buildSession` runner failure leaves nothing behind (AC #5).** Build a pool with a working
  factory, then swap the pool's runner-factory field to a failing one (same-package test), call
  `buildSession` with a fresh id, assert the error and that no `<id>.json` exists. Assert the
  *bootstrap's* file is still present in the same check, so the test also proves the cleanup is
  scoped to the failed session.

### Must stay green, unmodified

- `TestPool_Remove_CleansUpMintedSettingsFile` and `TestPool_Run_CleansUpBootstrapSettingsFile` —
  they read `settingsPath` off the session, so they are location-agnostic. If either needs an edit,
  the teardown contract has silently changed and that is a finding.
- Every `internal/sessions` test that constructs a pool with no `RegistryPath` (AC #2) — no edits.
  `TestRunnerFactory_InvokedAtEveryConstructionSite` is the canonical one and calls both write sites.
- `make check` is the gate. Nothing here needs the live-claude suite.

### Red-set check

Each AC has a test it is the sole cause of red for: AC #1 → the two argv-location tests; AC #2 →
`TestRunnerFactory_InvokedAtEveryConstructionSite` (the persistence-disabled branch is the only thing
keeping it from erroring); AC #3 → the path-and-modes test (drop the `MkdirAll`, or widen `0700`,
and it reddens); AC #4 → the stable-path and restart-bound tests; AC #5 → the two runner-failure
tests, one per site.

## Open questions

- **Post-`buildSession` orphans in `materialise`'s rollback branches** are knowingly left in place
  (see § Error handling, "the trap"). For a caller-supplied id the path is reused on the next
  `materialise` of the same id, so the growth is still bounded by session count; the leak is real
  only for an id that is materialised once, fails to persist, and is never retried. Removing it is
  unsafe against the concurrent same-id builder. If evidence ever shows this accumulating, the fix is
  the startup sweep the ticket already scoped out, not a removal at that branch.
- **An existing `session-settings/` directory with looser permissions is not tightened.** AC #3 says
  "created if absent, with mode `0700`", and `os.MkdirAll` is a no-op on an existing directory.
  Matching `disposeJSONLLocked`'s behaviour for `archived-sessions/`. A chmod-on-every-write would
  fight an operator who deliberately widened it; leaving it is the smaller surprise.
- **Files orphaned by a SIGKILLed previous process** are out of scope per the ticket. AC #4's
  id-keyed naming means such a file is overwritten the moment that session id is used again, so the
  bound holds without a sweeper for every id that comes back.
- **A SIGKILL inside the write window leaves a `.settings-*.json.tmp` scratch file**, and in a
  reaper-free directory that orphan is permanent. This is a new shape the relocation introduces, and
  it is accepted rather than fixed: the window is the few hundred microseconds between `CreateTemp`
  and `Rename` during session construction, it is the identical exposure `saveRegistryLocked` already
  carries for the registry's own scratch file, and the dotted `.tmp` pattern makes such files
  trivially identifiable by the startup sweep the ticket already scoped out. Naming it here so it is
  not rediscovered as an unexplained artefact.
- **The id's trust boundary is the registry load, not the write site.** This spec validates at the
  sink (`writeMCPSettings`) because that is what the ticket's scope allows, but `loadRegistry` and
  `pickBootstrap` still hand back `entry.ID` with no shape check, and `disposeJSONLLocked` is a
  second `filepath.Join` sink for the same value — safe today only because no unvalidated id ever
  reaches it (the bootstrap cannot be `Remove`d, and every other registered id came through
  `materialise`'s `ValidID` gate). Validating once at `loadRegistry` would close the class instead of
  the instance. That is a behaviour change to daemon startup for existing corrupt registries and
  belongs in its own ticket; file it if the pattern recurs.

## Security review

**Verdict:** PASS (first pass FAILed on two MUST FIX items; both fixed inline above, checklist
re-walked from the top against the revised spec).

**Findings:**

- **[Trust boundaries] SHOULD FIX — recorded, not gated.** One boundary crosses in this design:
  `sessions.json` → `pickBootstrap` → `bootstrapID` → a filesystem path. `loadRegistry` and
  `pickBootstrap` apply **no** shape validation to `entry.ID`; before this change that string never
  reached a path join in `Pool.New`, and after it names a file. The spec closes the instance with a
  `ValidID` gate at the sink (§ Error handling, "why the id is validated") but the boundary itself
  stays open, and `disposeJSONLLocked` is a second `filepath.Join` sink for the same value — safe
  today only by accident. Not gating: `sessions.json` is `0600` operator-owned state, so writing it
  already requires the operator's file access. Named as a follow-up in § Open questions.
- **[Tokens, secrets, credentials] No findings.** The payload is two public booleans; no token is
  generated, stored, transmitted, or logged. The file is nonetheless **integrity**-sensitive, because
  claude's `--settings` surface accepts `hooks`/`permissions`/`env` keys — anyone who can write it
  gets code execution as the operator. That is the justification the spec gives for the mode
  discipline rather than a hand-wave that "it holds no secrets".
- **[File operations] MUST FIX ×1 — fixed inline; remainder no findings.**
  - *Path traversal:* `filepath.Join(dir, string(id)+".json")` with a registry-supplied id is a real
    traversal primitive (`"../x"` cleans to a write outside the data dir). Closed by the `ValidID`
    gate: the canonical UUIDv4 shape admits only `[0-9a-f]` and `-`.
  - *Permissions:* stated explicitly — directory `0700` via `MkdirAll`, file `0600` via
    `os.CreateTemp` (preserved by `Rename`, so no `Chmod` and no world-readable window).
  - *Atomic writes:* prescribed, matching `saveRegistryLocked`. **MUST FIX (fixed):** the first draft
    said "`os.CreateTemp` in the target directory" without naming the pattern, leaving a scratch file
    indistinguishable from a real settings file and AC #4's count assertion ambiguous. Now pinned to
    `.settings-*.json.tmp` with the count assertion globbing `*.json`.
  - *Symlinks:* `os.Rename` replaces a symlink at the destination instead of writing through it — an
    open-truncate-write would not. A symlinked `session-settings/` directory would be followed by
    `MkdirAll`, but planting it requires write access to a `0700` operator-owned directory, i.e. the
    same trust level as the operator; not a boundary crossing.
  - *TOCTOU:* no stat-then-use anywhere on the new path. `session-settings` existing as a regular
    file makes `MkdirAll` error, which fails startup loudly through `New`'s existing wrapper.
- **[Subprocess execution] No findings.** The path is a discrete argv element (`--settings`, then the
  path) handed to the existing runner; no `sh -c`, no environment change, no new inheritance. After
  the `ValidID` gate the basename is `[0-9a-f-]{36}.json`. The path becoming predictable is addressed
  as a decision in § Design, not left silent: the control was always the `0600`-in-`0700` mode pair,
  never `CreateTemp`'s randomness, and the surrounding directory moves from world-writable `/tmp` to
  operator-only.
- **[Cryptographic primitives] No findings.** No crypto is introduced. `NewID` draws from
  `crypto/rand` (verified in `internal/sessions/id.go`), and the filename's unpredictability is
  explicitly *not* relied on as a security control.
- **[Network & I/O] Not applicable — stated, not skipped.** No socket, no listener, no reader of
  attacker-supplied length. The only write is a fixed ~80-byte payload with no caller-controlled
  size, so no cap is needed.
- **[Error messages, logs, telemetry] No findings.** The new rejection error quotes the offending id
  with `%q`, which escapes newlines and non-printables — a corrupt registry therefore cannot inject
  lines into the daemon's text log (`%s` would have allowed it, which is why the verb is specified
  rather than left to the developer). The path that reaches logs and argv contains only a UUID and
  the operator's own data dir. No new telemetry.
- **[Concurrency] MUST FIX ×1 — fixed inline; remainder no findings.** No new goroutine, no new lock,
  no lock-order change. Deterministic naming introduces exactly one new interaction — two same-id
  `materialise` callers writing the same path off `p.mu` — mitigated by the atomic rename, and its
  corollary (the race loser must **not** remove the file, because it is the winner's) is called out
  as a trap in § Error handling rather than left for a developer to symmetrise wrongly. **MUST FIX
  (fixed):** shutdown safety was unaddressed — a SIGKILL inside the write window leaves a permanent
  `.tmp` orphan in a now reaper-free directory, which is precisely the class of unnamed permanent
  leak this ticket exists to stop. Now named, bounded, and justified in § Open questions.
- **[Threat model alignment] No findings.** No `docs/threat-model.md` exists and this is not a relay
  ticket, so `docs/protocol-mobile.md` § Security model does not apply. The relevant domain is
  local-operator state: the file joins `sessions.json` and the control socket under `~/.pyry/<name>/`
  at the same `0700` trust level, which is a tightening relative to today's `os.TempDir()`.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-18
