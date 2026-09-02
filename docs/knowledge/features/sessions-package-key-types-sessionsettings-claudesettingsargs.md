# `SessionSettings` + `claudeSettingsArgs` (#833, `PermissionMode` #2043)

The per-session model / reasoning-effort / permission-posture set — the
storage + spawn **primitive** the wire verb (#841) builds on. No wire message
ships with this primitive.

```go
type SessionSettings struct {
    Model          string
    Effort         string
    YOLO           bool
    PermissionMode string
}
```

Zero value inherits the daemon template for `Model`/`Effort` and enforces
permissions (`YOLO` off, mode `default`) — the fail-safe default. Stored on
`Session.settings`, set initially in `Pool.New` (bootstrap) or
`Pool.buildSession` (minted) and mutated post-construction by
`Pool.UpdateSettings` (#840, below) under `Pool.mu` (write); read under
`Pool.mu` (same discipline as `label`).

**`YOLO` and `PermissionMode` express one posture, not two settings.** A
`Pool`-held `Session` always satisfies
`YOLO == (PermissionMode == "bypassPermissions")`. `YOLO` stays the
authoritative half for the escalation — `claudeSettingsArgs` derives the
bypass flag from it alone, never from the mode string — so the fail-safe stays
enforced in exactly one place even if a hand-built `SessionSettings` literal
in a test breaks the invariant. Three writers keep it true: `Pool.UpdateSettings`
rejects an unrecognised mode (`ErrUnsupportedPermissionMode`) or a
self-contradictory frame (`ErrPermissionModeConflict`) before mutating
anything (see below); the two construction sites and the registry read
(`settingsFromEntry`, [sessions-registry.md](sessions-registry.md)) normalise
through `canonicalPermissionMode(mode, yolo)` — `yolo:true` → `bypassPermissions`;
otherwise an in-band member (`default`, `acceptEdits`, `plan`, `auto`,
`dontAsk`) → itself; otherwise (`""`, unknown, or `bypassPermissions` paired
with `yolo:false`) → `default`. The last arm is a normalisation of *trusted*
input (construction sites, registry read) — operator input is rejected
loudly by `Pool.UpdateSettings`, never silently downgraded. `yolo` is not
being phased out: the mobile client speaks it, and #1687 settles what the two
mean together on the wire.

`permissionModeInBand(mode)` — the five claude accepts on a held-open stream,
mirroring `internal/streamsup`'s writer-side allow-list character for
character since `internal/sessions` may not import that package — and
`permissionModeKnown(mode)` — those five plus `bypassPermissions`, the
storable set — are both `switch` statements, never a package-level slice or
map: a `var` holding a security vocabulary is mutable state anything in the
package, a test included, could `append` the escalation onto; control flow
cannot be appended to. Both are **vocabulary** gates ("will claude parse
this?"), not authorisation gates — three in-band members (`acceptEdits`,
`auto`, `dontAsk`) genuinely loosen a child launched behind the daemon's
approval flags, and who may request that is #1687's decision, not this
primitive's.

`Pool.mintSettings` keeps its two-field literal (`Model`/`Effort` only) and
gains no `PermissionMode` line — a posture is not inherited by a freshly-minted
session, the same structural fail-safe already applied to `YOLO`;
`canonicalSettings` (called from `Pool.buildSession`) then gives the minted
session `default` because the field is absent, not because a downgrade rescues
it.

```go
func claudeSettingsArgs(s SessionSettings) []string
```

Pure helper, unexported. `Model != ""` → `--model <x>`; `Effort != ""` →
`--effort <x>`; then a **mutually exclusive posture slot**, in that
deterministic order:

- `YOLO == true` → `--dangerously-skip-permissions` and nothing else — never
  `--permission-mode bypassPermissions`, so the escalation keeps exactly one
  spelling and the flag can never be composed from a mode string.
- else a non-`default` in-band mode → `--permission-mode <mode>`.
- else (`default` or `""`) → nothing. `default` **is** claude's own default,
  so emitting no flag is the equivalent of an empty `Model`, and it is what
  keeps every argv this function composed before #2043 byte-identical.

Zero value → `nil`, so both call sites append nothing and the argv is
byte-identical to pre-#833 behaviour.

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
`Bootstrap`/`LifecycleState` field precedent exactly; `PermissionMode string`
(#2043) joins them with the same tag but a closed on-disk vocabulary — see
[sessions-registry.md](sessions-registry.md) for `permissionModeForDisk` /
`settingsFromEntry`. `saveLocked` copies `s.settings` into the outgoing entry
under the held `Pool.mu`.

A test helper that pre-writes a `registryEntry` literal by hand (rather than
going through `saveLocked`) has to build the `PermissionMode` field the same
way `permissionModeForDisk` would, or a stored non-default mode silently reads
back as `default` in that fixture. #2043 caught this in two of the package's
own pool-construction test helpers, which pre-date the field and built the
entry by hand.

**Only the bootstrap round-trips settings across a live daemon restart** —
`Pool.New` only re-materialises the bootstrap entry from disk; a minted
session's settings round-trip is exercised at the registry-serialization layer
only (this is the same pre-existing "only bootstrap reloads" limitation
[ADR 016](../decisions/016-bootstrap-ignores-persisted-lifecycle-state.md)
already documents, not something #833 introduces).
