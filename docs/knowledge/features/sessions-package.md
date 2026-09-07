# `internal/sessions` Package

The session-addressable runtime layer that wraps `internal/supervisor` with identity (`SessionID`) and registry (`Pool`) semantics. One `Pool` holds the set of supervised claude instances managed by a single `pyry` process.

Today the pool holds exactly one entry — the **bootstrap session** — so external behaviour is unchanged from the pre-Phase-1 supervisor-only world. The package shape is the seam Phase 1.1+ extends additively (multi-session CLI, `pyry attach <id>`, idle eviction) without touching `internal/supervisor`.

## Package Layout

```
internal/sessions/
  id.go         SessionID, NewID()
  session.go    Session: wraps one supervisor + optional bridge
  pool.go       Pool: registry, lifecycle, Config, SessionConfig, RotateID
  registry.go   On-disk sessions.json (loadRegistry, saveRegistryLocked)
  reconcile.go  encodeWorkdir; newTranscriptResolver (AC5 no-lsof fallback
                source only — the startup adopt-by-mtime caller
                reconcileBootstrapOnNew was removed in #839) and
                newProbePreferredTranscriptResolver (#838) — both thin
                adapters over internal/transcript as of #1149
```

## Key Types

## Errors

| Condition | Surface |
|---|---|
| `NewID` rng failure | `sessions.New` returns wrapped error. Fatal. |
| `supervisor.New` failure | Wrapped: `sessions: bootstrap supervisor: %w`. Fatal. |
| `Pool.Lookup` unknown id | `ErrSessionNotFound` (sentinel). |
| `Pool.Rename` unknown id | `ErrSessionNotFound` (sentinel). On-disk + in-memory state byte-identical to before. |
| `Pool.Rename` save failure | Wrapped error from `saveLocked` propagated verbatim; in-memory label rolled back to prior value. |
| `Pool.Remove` unknown id | `ErrSessionNotFound` (sentinel). On-disk + in-memory state + JSONL byte-identical to before. |
| `Pool.Remove` bootstrap target | `ErrCannotRemoveBootstrap` (sentinel). On-disk + in-memory state + JSONL byte-identical to before. |
| `Pool.Remove` save failure | Error from `saveLocked` propagated verbatim; in-memory delete rolled back; child not terminated. |
| `Pool.Remove` ctx cancellation during Evict | `ctx.Err()` from `Session.Evict`. Registry entry already deleted and persisted; child terminates asynchronously under the pool's `runCtx`. |
| `Pool.Remove` archive destination exists | Wrapped error matching `errors.Is(err, fs.ErrExist)`. Registry entry already removed (disposition runs after persist); live JSONL and existing archive both untouched; child still terminated by Evict. |
| `Pool.Remove` archive when registry persistence disabled | `"sessions: archive requires a registry path"`. Registry entry already removed; child still terminated by Evict. |
| `Pool.ResolveID` no match | `ErrSessionNotFound` (sentinel). |
| `Pool.ResolveID` ≥2 prefix matches | `ErrAmbiguousSessionID` (sentinel) wrapped with `fmt.Errorf("%w:\n%s", …)`; message lists each `<uuid> (<label>)` pair on its own line, sorted by `SessionID` asc; bootstrap-empty-label substituted with `"bootstrap"`. |
| `Pool.GetOrCreate` invalid id (empty / non-canonical UUIDv4) | `ErrInvalidSessionID` (sentinel). On-disk + in-memory state unchanged. |
| `Pool.GetOrCreate` save failure | Wrapped error from `saveLocked` propagated verbatim; in-memory insert rolled back. |
| `Pool.GetOrCreate` Pool.Run not active | `ErrPoolNotRunning` (sentinel). In-memory insert rolled back; best-effort re-save of the rolled-back state. |
| `Pool.supervise` before/after `Run` | `ErrPoolNotRunning` (sentinel). |
| `Session.Attach` with nil bridge | `ErrAttachUnavailable` (sentinel). |
| `Session.Attach` while bridge busy | `supervisor.ErrBridgeBusy` propagated **verbatim** — no wrap. |
| `Session.Run` / `Pool.Run` ctx cancel | `context.Canceled` from the supervisor. |

Sentinels (`ErrSessionNotFound`, `ErrAttachUnavailable`, `ErrPoolNotRunning`, `ErrCannotRemoveBootstrap`, `ErrAmbiguousSessionID`, `ErrInvalidSessionID`) live in `internal/sessions`. `supervisor.ErrBridgeBusy` stays in `internal/supervisor`.

## Dependency Direction

```
internal/sessions  →  internal/supervisor
```

`internal/sessions` imports `internal/supervisor`. The reverse is forbidden — verifiable with `go list -deps ./internal/supervisor/...`. `internal/sessions` does **not** import `internal/control`; control will (after Phase 1.0b) import sessions for `SessionID` and the resolver interface, never the other way around.

## Production Consumers (Phase 1.0b)

After #29, `cmd/pyry/main.go` constructs `*sessions.Pool` and `internal/control` consumes a `SessionResolver` (defined inside `internal/control` — see [control-plane.md](control-plane.md)). External behaviour is unchanged:

- `Request`/`Response` JSON shapes unchanged. No `session_id` field yet.
- No new log lines; the bootstrap session ID is **not** logged.
- Startup log line preserved verbatim (`pyrycode starting` with the same fields).
- `pyry status`/`stop`/`logs`/`attach` are byte-identical to Phase 0.
- Foreground vs service mode keys off `term.IsTerminal(os.Stdin.Fd())` in `cmd/pyry/main.go`, unchanged.
- Restart still uses `--continue`. `claude --session-id <uuid>` is **not** plumbed in 1.0.

`*sessions.Pool` does not satisfy `control.SessionResolver` directly: `Pool.Lookup` returns the concrete `*sessions.Session`, while the resolver interface returns `control.Session`. Go does not do covariant return types on interface satisfaction, so `cmd/pyry` defines a 5-line `poolResolver` adapter to bridge the two. See [lessons.md](../../lessons.md#interface-adapters-for-covariant-returns) and [control-plane.md](control-plane.md).

## References

- Spec: [`docs/specs/architecture/28-sessions-package.md`](../../specs/architecture/28-sessions-package.md)
- Parent design: [`docs/specs/architecture/27-session-addressable-runtime.md`](../../specs/architecture/27-session-addressable-runtime.md)
- ADR: [`003-session-addressable-runtime.md`](../decisions/003-session-addressable-runtime.md)
- Locked phase design: [`docs/multi-session.md`](../../multi-session.md), [`docs/plan.md`](../../plan.md)


## Sections

This overview is split across the documents below. Each is kept small so
search can reach it.

- [Status](sessions-package-status.md) — see the document
- [`SessionID`](sessions-package-key-types-sessionid.md) — type SessionID string
- [`Session`](sessions-package-key-types-session.md) — type Session struct { /* id, sup, bridge, log, lifecycle fields */ }
- [`Pool`](sessions-package-key-types-pool.md) — type Pool struct { /* mu RWMutex, sessions map, bootstrap, log */ }
- [`Runner` interface + `RunnerFactory` (#1077, corrected #1580 for #1348 fallout)](sessions-package-key-types-runner-interface-runnerfactory.md) — `Session.sup` is typed `Runner` (`internal/sessions/runner.go`):
- [`supervisor.Config.SessionID` — construction-safe id seam for a stream-json factory (#1108)](sessions-package-key-types-supervisor-config-sessionid-construction.md) — `RunnerFactory` (above) hands a `supervisor.Config` to the factory before any runner exists. 
- [Supervisor handle (1.1a-A1)](sessions-package-key-types-supervisor-handle-1-1a-a1.md) — Two unexported fields on `*Pool` hold the live errgroup while `Run` is in progress:
- [`Config.BootstrapEvicted` + `Pool.Ready()` (#761)](sessions-package-key-types-config-bootstrapevicted-pool-ready.md) — Two purely-additive primitives for **embedded pool hosts** that map each caller session onto its own `Pool.Create`'d claude and must run no…
- [`SessionSettings` + `claudeSettingsArgs` (#833)](sessions-package-key-types-sessionsettings-claudesettingsargs.md) — The per-session model / reasoning-effort / YOLO (bypass-permissions) triple — the storage + spawn **primitive** the wire verb (#841) builds…
- [`Pool.UpdateSettings` (#840)](sessions-package-key-types-pool-updatesettings.md) — The persistence seam the v2 settings verb (#841, split into wire vocabulary #844 + handler #845) calls to change an existing session's…
- [`Pool.SettingsFor` (#1585)](sessions-package-key-types-pool-settingsfor.md) — The read half `DefaultSettings` couldn't provide: it reads only the bootstrap, while `UpdateSettings` can already change *any* session's…
- [`newProbePreferredTranscriptResolver` (#838, migrated onto `internal/transcript` in #1149)](sessions-package-key-types-newprobepreferredtranscriptresolver.md) — func newProbePreferredTranscriptResolver( dir string, probe rotation.Probe, pidFn func() int, pinnedID func() string, ) func(ctx…
- [`Pool.BootstrapID` + `supervisor.Config.ResolveSessionID` (#839, resume branch #1164)](sessions-package-key-types-pool-bootstrapid-supervisor-config-resol.md) — func (p *Pool) BootstrapID() SessionID
- [`writeMCPSettings` + `Session.settingsPath` (#943, relocated #1518)](sessions-package-key-types-writemcpsettings-session-settingspath.md) — func writeMCPSettings(registryPath string, id SessionID) (string, error)
- [Pool.Create (1.1a-A2)](sessions-package-key-types-pool-create-1-1a-a2.md) — The user-facing primitive that ties together every existing seam — `NewID`, `saveLocked`, `RegisterAllocatedUUID`, `supervise`, `Activate`…
- [Pool.List (1.1b-A)](sessions-package-key-types-pool-list-1-1b.md) — The typed read primitive Phase 1.1b-B's `pyry sessions list` CLI verb (#46-B) calls instead of poking at `sessions.json` directly. 
- [Pool.Rename (1.1c-A)](sessions-package-key-types-pool-rename-1-1c.md) — The typed write primitive Phase 1.1c-B's `pyry sessions rename` CLI verb (#47-B) calls instead of poking at `sessions.json` directly. 
- [Pool.Remove (1.1d-A1)](sessions-package-key-types-pool-remove-1-1d-a1.md) — The typed delete primitive the future `pyry sessions rm` CLI verb (#65) calls instead of touching processes or `sessions.json` directly. 
- [Pool.Remove JSONL disposition (1.1d-A2 / #95)](sessions-package-key-types-pool-remove-jsonl-disposition-1-1d-a2.md) — Phase 1.1d-A2 adds a `RemoveOptions` parameter so callers pick the on-disk disposition. 
- [Pool.ResolveID (1.1e-A)](sessions-package-key-types-pool-resolveid-1-1e.md) — The typed prefix resolver Phase 1.1e-B's `pyry attach <id>` wire + CLI surface (and any future verb taking a session selector) consumes…
- [Pool.GetOrCreate (1.3b)](sessions-package-key-types-pool-getorcreate-1-3b.md) — The take-or-create primitive Phase 1.3b's `pyry attach --create-if-missing <uuid>` consumes. 
- [Per-session spawn workdir: `CreateIn` / `GetOrCreateIn` (#684)](sessions-package-key-types-per-session-spawn-workdir-createin-getor.md) — The pool-level primitive (EPIC #672, split from #681) that lets a session spawn its supervised claude in a directory **other than** the…
- [Reviving a dropped session: `Pool.Revive` (#1487)](sessions-package-key-types-reviving-a-dropped-session-pool-revive.md) — `Pool.New` materialises exactly one `*Session` from `sessions.json` — the bootstrap (`pickBootstrap` is the only reader of `reg.Sessions`). 
- [Transition observer (#659)](sessions-package-key-types-transition-observer.md) — The injectable, in-process signal a `cmd/pyry`-side consumer (#657) wires to map session boundaries onto the v2 `session_transition` wire…
- [`Pool.AdoptAnnouncedID` (#2135, sole announced-reset writer since #2137)](sessions-package-key-types-adoptannouncedid.md) — Adopts a session id claude announced on its own stdout, refusing a collision inside the same lock hold — and why a pool-side refusal alone isn't enough.
- [Concurrency](sessions-package-concurrency.md) — `sync.RWMutex` on `Pool.sessions`:
- [Testing](sessions-package-testing.md) — Three test files mirror the production layout. 
