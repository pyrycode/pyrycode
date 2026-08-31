# `SessionSettings` + `claudeSettingsArgs` (#833)

The per-session model / reasoning-effort / YOLO (bypass-permissions) triple —
the storage + spawn **primitive** the wire verb (#841) builds on. No wire
message ships with this primitive.

```go
type SessionSettings struct {
    Model  string
    Effort string
    YOLO   bool
}
```

Zero value inherits the daemon template for `Model`/`Effort` and enforces
permissions (`YOLO` off) — the fail-safe default. Stored on `Session.settings`,
set initially in `Pool.New` (bootstrap) or `Pool.buildSession` (minted) and
mutated post-construction by `Pool.UpdateSettings` (#840, below) under
`Pool.mu` (write); read under `Pool.mu` (same discipline as `label`).

```go
func claudeSettingsArgs(s SessionSettings) []string
```

Pure helper, unexported. `Model != ""` → `--model <x>`; `Effort != ""` →
`--effort <x>`; `YOLO == true` → `--dangerously-skip-permissions`, in that
deterministic order. `YOLO == false` appends nothing — absence of the flag is
what enforces permissions, so the function can never emit a
permission-*disabling* flag. Zero value → `nil`, so both call sites append
nothing and the argv is byte-identical to pre-#833 behaviour.

**Two spawn sites, both appending to a cloned slice:**

- `Pool.New` (bootstrap): reads `entry.Model/Effort/YOLO` in the warm-start
  branch (cold start → zero value), then
  `ClaudeArgs: append(slices.Clone(cfg.Bootstrap.ClaudeArgs), claudeSettingsArgs(settings)...)`.
  The clone is required because the pre-#833 code aliased
  `cfg.Bootstrap.ClaudeArgs` directly; appending to an alias would mutate the
  caller's slice.
- `Pool.buildSession` (minted): gains a `settings SessionSettings` parameter,
  appends after `--session-id`. Since #1575 the two public mint entry points —
  `CreateIn` and `GetOrCreateIn` — both pass the unexported `Pool.mintSettings`
  rather than `SessionSettings{}`, so a newly-minted session starts at the
  operator's configured model and effort instead of claude's own defaults.
  `Pool.Revive` is now the only caller that passes the zero value.

  **`Pool.mintSettings` — inherit two fields, structurally.** It reads
  `DefaultSettings` (the bootstrap's persisted triple) and rebuilds a
  `SessionSettings` **field by field** from `Model` and `Effort` only. The
  returned literal never mentions `YOLO`, so a phone-granted
  `--dangerously-skip-permissions` cannot be inherited by construction rather
  than by a clearing statement a later edit could drop — and a field added to
  `SessionSettings` in future is likewise not inherited until someone opts it
  in. The existence bool is discarded: `DefaultSettings` already returns the
  zero value when there is no bootstrap, so the no-configuration argv falls out
  of the zero value rather than out of a second return site. It **must** be
  called off `Pool.mu` (`DefaultSettings` takes `RLock`, and Go's `RWMutex` is
  not reentrant); both call sites already build the session before taking the
  write lock, so the snapshot can be one concurrent `UpdateSettings` stale —
  accepted, matching how `label` and `spawnDir` already behave on this path.

  **Ripple:** `saveLocked` copies `s.settings` into the outgoing entry, so on a
  daemon whose bootstrap carries a model or effort a minted session's on-disk
  entry now carries them too, where `omitempty` previously dropped them. That
  is wanted — `Pool.UpdateSettings`'s live restart recomposes argv from the
  stored value, so a session that inherits at spawn must store what it
  inherited. `yolo` stays absent (`false` + `omitempty`).

  Setting the model/effort of an *already-minted* session is a different
  concern, covered by `Pool.UpdateSettings` below.

**Persistence.** `registryEntry` (`registry.go`) gains `Model string`,
`Effort string`, `YOLO bool`, all `json:"...,omitempty"`, following the
`Bootstrap`/`LifecycleState` field precedent exactly. `saveLocked` copies
`s.settings` into the outgoing entry under the held `Pool.mu`.

**Only the bootstrap round-trips settings across a live daemon restart** —
`Pool.New` only re-materialises the bootstrap entry from disk; a minted
session's settings round-trip is exercised at the registry-serialization layer
only (this is the same pre-existing "only bootstrap reloads" limitation
[ADR 016](../decisions/016-bootstrap-ignores-persisted-lifecycle-state.md)
already documents, not something #833 introduces).
