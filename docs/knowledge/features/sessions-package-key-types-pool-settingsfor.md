# `Pool.SettingsFor` (#1585)

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

Shipped unwired on purpose (#1577 split): no consumer reads it yet, and the
method stays visible to staticcheck's `unused` check only because it's
exported on an exported type. The docstring carries a read-modify-write
warning for whichever consumer wires it next: echoing the full snapshot back
through `UpdateSettings` re-asserts a `YOLO` posture an operator may have
cleared in the interval — send only the fields actually changing.
See [codebase/1585.md](../codebase/1585.md).
