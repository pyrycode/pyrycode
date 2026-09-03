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

Since #2066, `permissionModeKnown` is also the routing membership predicate
`inBandDeliverable` reads for the posture field — every storable posture is
now delivered the same way, and `permissionModeInBand` staying at five is
what keeps `canonicalPermissionMode`, `claudeSettingsArgs` and
`permissionModeForDisk` reading exactly the set they read before: the routing
question was moved to a *different* predicate that already existed, not
answered by widening this one. See
[`Pool.UpdateSettings`](sessions-package-key-types-pool-updatesettings.md).

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
`--effort <x>`; then the posture.

**Since #2065 the escalation flag is unconditional, and the posture slot is no
longer mutually exclusive.** Every argv this function composes carries
`--dangerously-skip-permissions`, always, including the zero value — so
`claudeSettingsArgs` no longer returns `nil` for an unconfigured session, and
neither construction site's argv is byte-identical to pre-#833 behaviour any
more. What launches is bypass for every child; what the child actually *runs*
as is decided afterward, in-band, by the write `internal/streamsup`'s
`spawnAndWait` issues before any user turn can reach it (see [posture
gate](streamsup-package-posture-gate-spawn-permission-mode-ack.md)). The launch
argv stopped being the posture's authority — that is the whole ticket, because
claude refuses an in-band re-escalation at a child that did not launch with the
flag, so tightening a posture used to be free and loosening it needed a
respawn.

The property the old mutual exclusion existed for is preserved, restated
rather than dropped:

- **The escalation keeps exactly one spelling.** `YOLO == true` still emits the
  flag alone and nothing else; a non-escalated posture emits
  `--permission-mode <mode>` beside it, gated by `permissionModeInBand`, which
  does not include `bypassPermissions` — so no mode string can ever compose
  `--permission-mode bypassPermissions`.
- **`default` now names itself.** It used to append nothing, on the reasoning
  that `default` *is* claude's own default so silence was equivalent to an
  empty `Model`. Silence beside an unconditional bypass flag no longer reads as
  `default` — it reads as whatever the flag says — so `default` composes
  `--permission-mode default` like every other in-band member. The assembled
  argv shape is not new: every non-bypass stream spawn already ended in that
  pair, injected by `cmd/pyry`'s `permissionArgs`; `withApprovalArgs`' own
  mode-drop arm (#2043, below) simply becomes the common case instead of the
  exception.

A mode outside the in-band set (an unrecognised string, or `""` from a
hand-built literal that skipped `canonicalSettings`) still appends no pair —
the argv must never claim a posture nothing will go on to write in-band. A
`Pool`-held `Session` cannot reach that state; a hand-built one in a test can,
which is why `TestRunnerConfigPermissionModeIsAlwaysKnown` pins it across all
three construction paths (below).

**Provenance, not presence, is what the two former fail-safes read now.**
Before #2065, `internal/streamsup`'s spawn-time write and `cmd/pyry`'s
`withApprovalArgs` both used the flag's *absence* from the assembled argv as
their safety signal. Once the flag is unconditional that signal reads "yes"
for every session, and both readers invert into their permissive arm
silently — `withApprovalArgs` stops injecting the approval set for anyone, and
the spawn-time write stops downgrading anyone, i.e. every child stays in
bypass with no approval gate, the exact inverse of this ticket, shipped green.
Both were rewritten instead to read `operatorBypass(base)` — `base` being
`Session.spawnBase`, the **settings-free** argv (template args, including the
operator's `pyry install-service -- --dangerously-skip-permissions`
pass-through, plus the construction-time `--session-id`/`--settings` suffixes)
— because that is the one place a bypass this package composed from the stored
posture is still separable from one the operator handed the daemon directly.
`spawnBase` is immutable post-construction and every post-construction argv
install (`Pool.UpdateSettings` → `Runner.Restart` / `Runner.SetSpawnArgs`)
recomposes from that same base, so the bit cannot go stale. It crosses both
package boundaries as a single `bool` — `RunnerConfig.OperatorBypass`, set once
at each construction site from `operatorBypass(base)` and mapped straight
through to `streamsup.Config.OperatorBypass` — never as a second read of the
assembled argv. `PermissionModeBypass` is exported (an alias of
`permissionModeBypass`, not a second literal) so `cmd/pyry`'s
`withApprovalArgs` can pair the stored-posture half of that same distinction
without a second vocabulary. See [`withApprovalArgs`](streamsup-package-constructing-a-streamrunner-newstreamrunnerfacto.md)
and [the posture gate](streamsup-package-posture-gate-spawn-permission-mode-ack.md).

**Cosmetic edge case, not a fail-safe: an operator-bypass daemon composes the
flag twice.** `spawnArgs` is `spawnBase + claudeSettingsArgs(...)`; when the
operator's pass-through already put the flag in `spawnBase`, the unconditional
append puts a second copy on every spawn from that daemon. `claude
--dangerously-skip-permissions --dangerously-skip-permissions --help` parses
and exits 0, so nothing breaks — but nothing hermetic in this package's own
tests covers the shape, because a duplicate is unreachable by construction from
`claudeSettingsArgs`' own output alone; it only appears once `spawnBase` is
concatenated in. Reviewed and accepted at #2065's code review as a documented
cosmetic, not a correctness gap.

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
