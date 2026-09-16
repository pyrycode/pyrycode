# `Pool.SettingsFor` (#1585) and `Pool.DormantSettingsFor` (#2449)

The read half `DefaultSettings` couldn't provide: it reads only the bootstrap,
while `UpdateSettings` can already change *any* session's settings by id. This
closes that read/write asymmetry.

```go
func (p *Pool) SettingsFor(id SessionID) (SessionSettings, error)
```

One `p.mu.RLock()` per call. A hit returns `(sess.settings, nil)` — including
the zero value for a session genuinely at defaults. A miss (unknown id,
malformed id, or `""`) returns `(SessionSettings{}, ErrSessionNotFound)`, the
bare sentinel, unwrapped — mirroring `UpdateSettings`'s lookup-and-refuse shape
rather than `DefaultSettings`'s existence-bool. The empty id is **not**
special-cased to the bootstrap: unlike `Lookup("")`, it misses `p.sessions`
like any other unknown id, so read and write agree on what `""` means.

Deliberately does not delegate to (or from) `DefaultSettings`: two exported
methods each taking `p.mu.RLock()` in the same call chain self-deadlock the
moment a writer queues between the acquisitions, since `RWMutex` is not
reentrant — the hazard `mintSettings`'s docstring already records. The two
stay independent four-line bodies with different contracts (id-keyed lookup
vs. no-argument bootstrap fallback) rather than sharing an exported entry
point. `SettingsFor(p.BootstrapID())` is not equivalent to `DefaultSettings()`
either — it's two acquisitions with a `RotateID` window between them, so it
can observe a session that stopped being the bootstrap between the two calls.

Shipped unwired at first (#1577 split). It is wired since #1609/#1610: `cmd/pyry`'s
`resolveBoundRunSettings` calls it as the live half of the conversation-keyed
[`request_session_settings`](v2-session-manager-state-machine-inbound-request-session-settings-the-rea.md)
read. The docstring carries a read-modify-write warning for that caller and
any future one: echoing the full snapshot back through `UpdateSettings`
re-asserts a `YOLO` posture an operator may have cleared in the interval —
send only the fields actually changing.
See [codebase/1585.md](../codebase/1585.md).

## `Pool.DormantSettingsFor` (#2449)

```go
func (p *Pool) DormantSettingsFor(id SessionID) (SessionSettings, error)
```

`SettingsFor`'s dormant half: the `SessionSettings` a `Revive` of `id` would
materialise, read from `p.dormant` instead of `p.sessions` — the entry's own
persisted `Model` and `Effort`, plus `ErrSessionNotFound` (bare, unwrapped)
when no dormant entry exists for `id`. Same discipline as `SettingsFor`: one
`p.mu.RLock()` per call, called with `p.mu` unheld, `""` not special-cased.

It exists because `Pool.New` materialises only the bootstrap session from
`sessions.json`, so after a daemon restart `SettingsFor` alone answers
"not addressable" for every other conversation the daemon has a persisted
record of — the all-zero `request_session_settings` reply that left a
restarted channel's model and effort menus blank and inert until its first
message revived the session (#2449). [Reviving a dropped session](sessions-package-key-types-reviving-a-dropped-session-pool-revive.md)
keeps those entries in `p.dormant` across a restart (#2448); this read is
what a settings request does with them.

Two decisions worth restating because both are easy to undo by accident:

- **Not `revivedSettings`, though it is the same map read.** `revivedSettings`
  collapses an unknown id into the zero `SessionSettings` — correct for a
  revive (an id with no record revives to claude's defaults) and wrong for a
  reader, which would then report an unknown bound id as an addressable
  session with empty settings. `DormantSettingsFor` has its own miss.
- **The posture is built, not cleared.** The literal names `Model` and
  `Effort` only and is then passed through `canonicalSettings` — the same
  function `buildSession` applies to what `Revive` hands it — so `YOLO` is
  false and `PermissionMode` is `"default"` structurally, never copied from
  the entry. A restart stays a revocation point for a phone-granted
  permission bypass (#1487): a persisted `yolo` or non-default
  `permission_mode` cannot reach this read however the entry was written,
  and the reported posture is guaranteed to equal the one `Revive` will
  actually materialise, because both go through the same `canonicalSettings`
  call rather than two places spelling the same constant.

Not folded into `SettingsFor` as a fallback: that would change what "not
found" means for every other caller of the live read, several of which use
exactly that answer to decide a session is not addressable. Consumed by
`cmd/pyry`'s `resolveBoundRunSettings` as the second of two reads, live
first — see [`request_session_settings`](v2-session-manager-state-machine-inbound-request-session-settings-the-rea.md).

Since #2463 this read has a write counterpart on the same partition:
[`Pool.UpdateDormantSettings`](sessions-package-key-types-pool-updatesettings.md#pool-updatedormantsettings-2463)
merges a `set_session_settings`' model/effort into the same `p.dormant` entry
this method reads, also used as `settingsUpdaterAdapter`'s existence probe for
a dormant id.
