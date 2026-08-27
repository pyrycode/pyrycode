# `internal/sessions` Package

The session-addressable runtime layer that wraps `internal/supervisor` with identity (`SessionID`) and registry (`Pool`) semantics. One `Pool` holds the set of supervised claude instances managed by a single `pyry` process.

Today the pool holds exactly one entry — the **bootstrap session** — so external behaviour is unchanged from the pre-Phase-1 supervisor-only world. The package shape is the seam Phase 1.1+ extends additively (multi-session CLI, `pyry attach <id>`, idle eviction) without touching `internal/supervisor`.

## Status

- **Phase 1.0a (#28):** package introduced, fully tested, no production consumers.
- **Phase 1.0b (#29):** consumers wired. `cmd/pyry/main.go` constructs the `Pool`; `internal/control` resolves session state via a `SessionResolver` interface defined in the control package. Wire protocol unchanged; `pyry status`/`stop`/`logs`/`attach` are byte-identical to Phase 0.
- **Phase 1.2a (#34):** `Config.RegistryPath`, on-disk `sessions.json`, cold/warm-start in `Pool.New`. See [sessions-registry.md](sessions-registry.md).
- **Phase 1.2b-A (#38):** `Config.ClaudeSessionsDir`, startup reconciliation pass in `Pool.New`, `Pool.RotateID` seam. See [jsonl-reconciliation.md](jsonl-reconciliation.md).
- **Phase 1.2b-B (#39):** errgroup wrap in `Pool.Run` (bootstrap + rotation watcher); `RegisterAllocatedUUID` skip set on `Pool`. See [rotation-watcher.md](rotation-watcher.md).
- **Phase 1.2c-A (#40):** per-session `active ↔ evicted` lifecycle goroutine, `Config.IdleTimeout` / `SessionConfig.IdleTimeout`, `Session.Activate` / `Pool.Activate`, registry `lifecycle_state`. `Session.Run` rewritten as the lifecycle loop; `Session.Attach` gains attach bookkeeping. See [idle-eviction.md](idle-eviction.md).
- **Phase 1.2c-B (#41):** `Config.ActiveCap` + LRU eviction at `Pool.Activate`; `Session.Evict` public primitive; `Pool.capMu` outermost lock. See [idle-eviction.md](idle-eviction.md) and [ADR 006](../decisions/006-concurrent-active-cap-lru.md).
- **Phase 1.1a-A1 (#72):** `Pool.runGroup` / `Pool.runCtx` supervisor handle, unexported `Pool.supervise(sess)` helper, `ErrPoolNotRunning` sentinel. Bootstrap fan-out refactored onto the helper; the watcher fan-out is unchanged (not a `*Session`). The seam Phase 1.1a-A2's `Pool.Create` consumes.
- **Phase 1.1a-A2 (#73):** `Pool.Create(ctx, label) (SessionID, error)` — mint, persist, activate primitive. New unexported fields `Pool.sessionTpl` (per-session template snapshotted from `cfg.Bootstrap`) and `Pool.idleTimeoutDefault` (mirror of `Config.IdleTimeout`). `claude --session-id <uuid>` is now baked on the spawn path for non-bootstrap sessions. Bootstrap behaviour unchanged.
- **Phase 1.1b-A (#60):** `Pool.List() []SessionInfo` read primitive + new `SessionInfo` value type. Operator-visible snapshot (id, label, lifecycle state, last-active timestamp, bootstrap flag) for every session in the pool, sorted by `LastActiveAt` desc with `SessionID` asc tiebreak. Synthetic `"bootstrap"` label substitution for empty-on-disk bootstrap labels lives at this layer (consumers get the same UX guarantee). Read-only; no on-disk mutation. The internal seam Phase 1.1b-B's `pyry sessions list` CLI verb (46-B) consumes.
- **Phase 1.1c-A (#62):** `Pool.Rename(id, newLabel) error` write primitive — typed mutator for the `label` field that flows through `saveLocked`. Empty `newLabel` clears the on-disk value to `""` (synthetic `"bootstrap"` substitution from #60 then resumes for the bootstrap entry); non-empty labels persist verbatim and are reflected by `Pool.List`. Unknown id returns `ErrSessionNotFound` with on-disk and in-memory state byte-identical to before. The internal seam Phase 1.1c-B's `pyry sessions rename` CLI verb (47-B) consumes.
- **Phase 1.1d-A1 (#94):** `Pool.Remove(ctx, id) error` write primitive + `ErrCannotRemoveBootstrap` sentinel — terminates the named session's claude process and drops its registry entry. JSONL on disk is **not** touched (disposition lands in 64-A2 / #95). Bootstrap is rejected via the sentinel; unknown ids reuse `ErrSessionNotFound`. Delete-then-evict ordering: `Pool.mu` covers the in-memory delete + persist, then is released before `Session.Evict` so the lifecycle goroutine's `transitionTo → Pool.persist` can reacquire the mutex without deadlock. Save-failure rolls the in-memory delete back. The internal seam the future `pyry sessions rm` CLI verb (#65) consumes.
- **Phase 1.1d-A2 (#95):** `Pool.Remove` signature evolves to `(ctx, id, opts RemoveOptions) error` + `JSONLPolicy` enum (`JSONLLeave` / `JSONLArchive` / `JSONLPurge`). Zero-value `RemoveOptions{}` preserves 1.1d-A1 behaviour byte-for-byte. Archive moves the live JSONL into `<pyry-data-dir>/archived-sessions/<uuid>.jsonl` (data-dir = parent of `registryPath`; subdir auto-created; errors wrapping `fs.ErrExist` if destination exists; source-absent is a no-op). Purge deletes the JSONL (source-absent is a no-op). Disposition runs under `Pool.mu` after `saveLocked` so the registry+JSONL transition is observably atomic; on disposition failure the registry is already committed and `Session.Evict` still runs.
- **Phase 1.3b (#155):** `Pool.GetOrCreate(ctx, id, label) (SessionID, error)` take-or-create primitive + new `ErrInvalidSessionID` sentinel + new `ValidID(s string) bool` UUIDv4 validator (next to `NewID`). Either returns the existing session for an already-registered UUID or atomically registers a new one under the caller-supplied id. Builds on `Pool.Create`'s helper extraction (`buildSession`, `registerAllocatedUUIDLocked`); the load-bearing change is that the register + persist + skip-set + lifecycle-goroutine schedule all run under one `p.mu` critical section, closing the race where a same-id observer could see the entry before its lifecycle goroutine parks. The internal seam Phase 1.3b's `pyry attach --create-if-missing` consumes via the new `control.GetOrCreator` interface (embedded in `Sessioner`). See [ADR 014](../decisions/014-get-or-create-take-or-create.md).
- **Phase 1.1e-A (#66):** `Pool.ResolveID(arg string) (SessionID, error)` prefix resolver + new `ErrAmbiguousSessionID` sentinel. Maps a user-supplied UUID-or-prefix string to a canonical `SessionID` under `Pool.mu` (RLock). Empty arg → bootstrap (same seam as `Pool.Lookup("")`); exact full-UUID match short-circuits via single map lookup; otherwise scan for unique prefix match. Zero matches → `ErrSessionNotFound` (reused); ≥2 → `ErrAmbiguousSessionID` wrapped via `fmt.Errorf("%w:\n%s", …)` so the message lists `<uuid> (<label>)` pairs on their own lines, sorted by `SessionID` ascending. The internal seam Phase 1.1e-B's `pyry attach <id>` wire + CLI consumes; 47-B / 48-B may opportunistically refactor onto it when next touched.
- **Phase 1.1+:** `Request.SessionID` on the wire (#71 — `sessions.new` verb consumes `Pool.Create`), per-session log lines, channel-driven auto-mint.
- **Phase 3 (#243):** `Config.ConversationsRegistry *conversations.Registry` + `Config.ConversationsRegistryPath string` plumbing fields; matching unexported `Pool.convReg` / `Pool.convRegistryPath`. When `convReg` is non-nil, `Pool.Run` registers a sibling goroutine to the rotation watcher inside its errgroup: `g.Go(func() error { return conversations.RunSweepLoop(gctx, p.convReg, p.convRegistryPath, p.convSweepInterval, p.log) })`. nil-registry disables the goroutine — preserves test default. See [conversations-auto-archive.md § Daemon wiring (#243)](conversations-auto-archive.md).
- **Phase 3 (#262):** `Config.SweepInterval time.Duration` (zero = use `conversations.SweepInterval` of one hour) + matching unexported `Pool.convSweepInterval` resolved in `New` with the `<= 0` fallback baked in. Replaces the prior package-level `var convSweepInterval` test seam — one seam, not two. Surfaced via the `cmd/pyry` `-pyry-conv-sweep-interval` flag (visible, annotated `(testing; 0 = production default of 1h)`) so out-of-process e2e tests downstream of #251 can drive the sweep loop at ~100ms instead of waiting one hour. Production callers leave the field zero; in-package tests set `pool.convSweepInterval = …` directly after construction (mirrors the existing `pool.convReg` / `pool.convRegistryPath` pattern). See [conversations-auto-archive.md § Single seam: `Config.SweepInterval` (#262)](conversations-auto-archive.md).
- **Phase 2 mobile (#659):** `Pool.SetTransitionObserver(TransitionObserver)` — an injectable, in-process signal fired on `/clear` rotations and evictions (idle + cap). New types `TransitionReason` / `SessionTransition` / `TransitionObserver` in `transition.go`; new unexported field `Pool.transitionObserver`. The `cmd/pyry` consumer (#657) maps it onto the v2 `session_transition` wire event without `internal/sessions` importing `internal/protocol` / `internal/relay`. See *Transition observer* below and [codebase/659.md](../codebase/659.md).
- **EPIC #672 (#684):** `Pool.CreateIn(ctx, label, spawnDir)` / `Pool.GetOrCreateIn(ctx, id, label, spawnDir)` — sibling methods carrying an explicit per-session spawn working directory through the shared `buildSession` seam into `supervisor.Config.WorkDir`. Empty `spawnDir` falls back to `tpl.WorkDir`, so `Create` / `GetOrCreate` become thin delegators (`=> CreateIn(ctx, label, "")` / `=> GetOrCreateIn(ctx, id, label, "")`) with byte-identical behaviour for every existing caller. The pool treats the path as **opaque** (no validation / canonicalisation / trust) — that is the consumer slice #685's job. See *Per-session spawn workdir* below and [codebase/684.md](../codebase/684.md).
- **EPIC #600 (#761):** `Config.BootstrapEvicted bool` + `Pool.Ready() <-chan struct{}` — two purely-additive primitives for **embedded pool hosts** (`pyry acp`) that must run exactly one interactive claude per caller session. `BootstrapEvicted` parks the bootstrap in `stateEvicted` so it spawns no eager claude (a single `Pool.Create` is then the only interactive claude — ACP divergence 6); `Ready()` is a `sync.Once`-closed channel gating the unrecoverable first-`Create → ErrPoolNotRunning` startup race. Both zero-value-safe, single-consumer, no call-site fan-out. See *`Config.BootstrapEvicted` + `Pool.Ready()`* below, [codebase/761.md](../codebase/761.md), and [ADR 026](../decisions/026-embedded-acp-pool-exact-one-claude.md).
- **#838:** `newProbePreferredTranscriptResolver(dir, probe, pidFn)` in `reconcile.go` — replaces the `supervisor.Config.ResolveTranscript` wiring at `pool.go`'s bootstrap block with a probe-preferred resolver (mirrors #827's turn-stream fix, applied to the *other* live-child bootstrap-transcript consumer #827 deferred). Tails the daemon's own bootstrap child's open `<uuid>.jsonl` via `rotation.Probe.OpenJSONL` + a live child PID, instead of `newTranscriptResolver`'s newest-by-mtime scan, so a second claude in the same shared sessions dir can't redirect the delivery-confirm baseline onto its own newer transcript. `pool.go`'s `New` reads the live PID through a late-bound `bootstrapSup *supervisor.Supervisor` holder (assigned right after `supervisor.New` returns) because `supCfg` is copied by value into the supervisor before it exists. No-lsof, `pid <= 0`, empty/erroring probe, and the AC4 confidentiality guard (dir-equality + UUID stem) all collapse to `("", 0, nil)` — a nil error, never a non-nil one (which would divert `confirmViaTranscriptGrowth` to the #668 Committed-chip fallback) and never an mtime fallback. `newTranscriptResolver` / `mostRecentJSONL` are unchanged and still back the no-lsof delegation path and `reconcileBootstrapOnNew` (unrewired here — `ChildPID == 0` at that call site, sibling ticket). See *`newProbePreferredTranscriptResolver`* below and [codebase/838.md](../codebase/838.md).
- **#1149:** `newProbePreferredTranscriptResolver` and `newTranscriptResolver` are rewritten as thin adapters over `internal/transcript` (#1148) — the local `jsonlExt` const, `uuidStemPattern` regexp, and `mostRecentJSONL` scan are deleted, replaced by `transcript.Ext` / `transcript.ValidStem` / `transcript.Newest` / `transcript.CanonicalDir` / `transcript.StatByID` / `transcript.Probed`. Pure adapter swap: the `("", 0, nil)` no-baseline convention and the AC4 confidentiality guard are preserved verbatim (the adapter swallows `transcript.Probed`'s one surfaced error to keep the convention), the no-lsof AC5 delegation to `newTranscriptResolver` is unchanged, and `probeUsable`/`availabilityReporter` stay local (probe-capability detection, not shared core). No signature change, no consumer cascade. See the rewritten *`newProbePreferredTranscriptResolver`* below and [codebase/1149.md](../codebase/1149.md).
- **#833:** `SessionSettings{Model, Effort string; YOLO bool}` — a per-session model/reasoning-effort/bypass-permissions triple, persisted on `registryEntry` (`Model`/`Effort`/`YOLO`, all `omitempty`) and applied to the `claude` spawn argv via the pure `claudeSettingsArgs` helper at both spawn sites (`New`'s bootstrap warm-start, `buildSession`'s minted path). Zero value appends no flags — byte-identical argv for every session that doesn't opt in. `security-sensitive`: `YOLO`'s zero value is the fail-safe "permissions enforced" state; no custom decoder needed (see [ADR 030](../decisions/030-plain-bool-failsafe-persisted-flag.md)). Storage + spawn primitive only — no wire verb yet (setter: #826b, reader: #826c). See *`SessionSettings` + `claudeSettingsArgs`* below and [codebase/833.md](../codebase/833.md).
- **#943:** `writeMCPSettings() (string, error)` (new `settings.go`) + `Session.settingsPath` — a per-session `--settings <path>` file carrying `{"enableAllProjectMcpServers":true}`, joined into `spawnBase` (not `claudeSettingsArgs`) at both construction sites so it survives every recompose (backoff restart, #842 live settings-restart) automatically. Fixes claude 2.1.199's "N new MCP servers found" startup modal wedging every interactive spawn's PTY readiness check; mirrors the un-gated agent-run compat fix (`d10ce87`) without importing its deny-default posture. Not `security-sensitive`. See *`writeMCPSettings` + `Session.settingsPath`* below and [codebase/943.md](../codebase/943.md).
- **#839:** `Pool.BootstrapID() SessionID` — new RLock-and-resolve-fresh accessor mirroring `Default()`/`DefaultSettings()`, deliberately reading `p.bootstrap` rather than `Default().ID()`/`sess.id` so it introduces no new reader of the latter (`RotateID` mutates `sess.id` without `Session.lcMu` under a documented no-concurrent-reader invariant). Wired as `supervisor.Config.ResolveSessionID` on the bootstrap `supCfg` (`ResumeLast: false` alongside it) via a late-bound `var p *Pool` closure, so the daemon's own persisted id — never a foreign `<uuid>.jsonl` from the shared sessions dir — is what `--session-id` resolves to at every spawn. The startup `reconcileBootstrapOnNew` call is deleted; `/clear` reconciliation now falls out of `ResolveSessionID` re-reading `p.bootstrap` fresh at each spawn, since the watcher's existing `RotateID` call already persists the rotated id before the next restart. `security-sensitive`: closes a confused-deputy restart-resume gap. See *`Pool.BootstrapID`* below, [codebase/839.md](../codebase/839.md), and [jsonl-reconciliation.md](jsonl-reconciliation.md) (now marked retired).
- **#1164:** `ResolveSessionID` widened `func() string` → `func() (id string, resume bool)`. claude 2.1.199 refuses `--session-id <uuid>` when `<uuid>.jsonl` already exists (a hard daemon restart survives the pinned id's transcript), so the closure now probes `transcript.StatByID(cfg.ClaudeSessionsDir, id)` fresh every spawn — exists → `(id, true)` → `buildClaudeArgs` emits `--resume <id>` (reattach, preserves the idle conversation); absent, empty id, or `ClaudeSessionsDir == ""` → `(id, false)` → `--session-id <id>` (byte-identical #839 create path). Decided per-spawn (not `firstRun`-gated like `streamsup`, not a one-shot decision at `New()`) so cold start, in-process respawn, and daemon restart are all handled by one rule with no bookkeeping. `resume` never rotates the id — that stays #1165's independent, different-fabric safety net. Not `security-sensitive` (by-id probe, no dir scan — narrows the surface). See *`Pool.BootstrapID`* below, [codebase/1164.md](../codebase/1164.md), and [ADR 032](../decisions/032-bootstrap-resume-per-spawn-existence-probe.md).
- **#1518:** `writeMCPSettings` signature widens to `func writeMCPSettings(registryPath string, id SessionID) (string, error)`. With a registry path configured, the file moves off `os.TempDir()` to `<dataDir>/session-settings/<id>.json` (created on demand at `0700`, mirroring `archived-sessions/`), written atomically (scratch file, `fsync`, rename), with `id` gated on `ValidID` since it now names a file and a warm-start id comes off disk unchecked. With no registry path (persistence disabled), behaviour is unchanged from #943. The id-derived name bounds the on-disk set by session count instead of daemon-restart count (AC #4), and because the data dir has no OS reaper, both write sites (`Pool.New`, `buildSession`) plus `CreateIn`'s `saveLocked` rollback now remove the file on every error return after the write via a `defer`-and-success-flag — the OS temp reaper had been acting as an accidental leak-bounder for #943's best-effort-only cleanup, and relocating without also closing those error paths would have shipped an unbounded leak. `materialise`'s discard branches deliberately do **not** remove the file — the race loser's path is byte-identical to the winner's live one. `security-sensitive` (id-to-path traversal gate). See the rewritten *`writeMCPSettings` + `Session.settingsPath`* below, [codebase/1518.md](../codebase/1518.md), and `docs/specs/architecture/1518-session-settings-under-data-dir.md`.
- **#1805:** `Session.Activate` gains an entry guard — `if err := ctx.Err(); err != nil { return err }` — as the first statement, before `lcMu` is taken. Fixes a coin-flip: once a session is active, `activeCh` is closed, so the pre-existing `select` between `<-activeCh` and `<-ctx.Done()` had two ready arms and Go picked between them uniformly at random, leaking `nil` for an already-cancelled `ctx` on roughly half of production calls (`streamsup.Runner.WaitForPTY` never itself checks the context). The guard also stops a cancelled caller from still driving a full re-activation: it sits above the `activateCh` send, so a call that fails fast triggers no lifecycle-goroutine wake and spawns no child. Scoped narrowly: a `ctx` that cancels *during* the wait (after the guard) is unaffected — see the *ctx cancellation race* paragraph below. `Evict` has the identical `select` shape and the same latent coin flip; left alone, no failure observed against it.

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

### `SessionID`

```go
type SessionID string

func NewID() (SessionID, error)
func ValidID(s string) bool
```

A 36-char canonical UUIDv4 (`8-4-4-4-12` hex with dashes), drawn from `crypto/rand`. No external UUID library — stdlib only, ~15 lines. The version (`b[6] = b[6]&0x0f | 0x40`) and variant (`b[8] = b[8]&0x3f | 0x80`) bits are set explicitly.

The empty `SessionID` (`""`) is the **unset sentinel**, never a valid generated ID. `Pool.Lookup("")` resolves to the default entry — the mechanism that lets future handlers call `Lookup(req.SessionID)` against an empty wire field without a special case.

`ValidID` (1.3b) reports whether `s` matches the canonical shape `NewID` produces: 36 chars, lowercase hex, dashes at positions 8/13/18/23, version-4 nibble (`'4'`) at position 14, RFC 4122 variant (`'8'`/`'9'`/`'a'`/`'b'`) at position 19. Empty input returns false. Lives next to `NewID` so producer + validator share one file. Used by `Pool.GetOrCreate` to reject caller-supplied ids that aren't canonical UUIDv4s. The version + variant checks are belt-and-suspenders: SDK-produced UUIDs are uuidv4 by construction, so the cost is nil and a future contributor passing a v3/v5 id gets a clean error. See [ADR 014](../decisions/014-get-or-create-take-or-create.md).

### `Session`

```go
type Session struct { /* id, sup, bridge, log, lifecycle fields */ }

func (s *Session) ID() SessionID
func (s *Session) State() supervisor.State
func (s *Session) WriteUserTurn(conversationID string, payload []byte) error
func (s *Session) Supervisor() *supervisor.Supervisor // #311; type-asserted off Runner since #1077
func (s *Session) Bridge() *supervisor.Bridge         // #311; nil in foreground
func (s *Session) LifecycleState() lifecycleState
func (s *Session) Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error)
func (s *Session) Activate(ctx context.Context) error
func (s *Session) Run(ctx context.Context) error
```

One supervised claude instance plus the bridge that mediates its I/O in service mode. As of 1.2c-A each Session owns a lifecycle goroutine driving an `active ↔ evicted` state machine (see [idle-eviction.md](idle-eviction.md)).

- `State()` returns the supervisor's snapshot — same safe-from-any-goroutine contract. In `evicted`, the supervisor reports `PhaseStopped` (faithful — it really isn't running).
- `WriteUserTurn(conversationID, payload)` (#322) is a one-line passthrough to `(*supervisor.Supervisor).WriteUserTurn`. Consumed by the `send_message` handler via the `handlers.TurnWriter` interface — the interface is declared in `internal/relay/handlers` (not here) so the handler sub-package stays free of `internal/sessions` / `internal/supervisor` imports; `*Session` satisfies it structurally. No tests at this level (contract is exercised by `internal/supervisor`'s tests; a broken delegation fails to compile).
- `Supervisor()` / `Bridge()` (#311) return the underlying supervisor handle and I/O bridge. Consumed by the assistant-turn bridge wiring in `cmd/pyry` to read `CurrentConversation()` at broadcast time and register an output observer on `Bridge.Write`. `Bridge()` returns `nil` in foreground mode; callers must gate on it. Returned pointers are owned by the session — callers must not retain them past the session's lifetime.
- `LifecycleState()` returns the current lifecycle state under `lcMu`. Used by tests and (eventually) richer status payloads.
- `Attach` returns `ErrAttachUnavailable` when `bridge == nil` (foreground mode); otherwise delegates to `(*supervisor.Bridge).Attach`. `supervisor.ErrBridgeBusy` is propagated **verbatim** so callers' `errors.Is` checks keep working. Bumps `attached` under `lcMu`; the wrapper goroutine spawned here decrements on bridge `done`. **Contract:** callers must `Activate` first — `bridge.Attach` on an evicted session would block on the pipe forever.
- `Activate(ctx)` moves an evicted session to `active`, blocking until the supervisor has started (or `ctx` cancels). It has no early-return for "already active" — an already-active call still falls into the same wait, which is why an already-cancelled `ctx` needed its own entry guard (#1805, below) rather than being caught by a no-op short-circuit. Idempotent under concurrent calls. An already-cancelled or expired `ctx` returns `ctx.Err()` immediately, before any re-activation is requested — no `activateCh` signal, no supervisor start, regardless of the session's current state.
- `Run(ctx)` blocks until ctx cancellation, driving the lifecycle loop (`runActive` ↔ `runEvicted`). The supervisor is started on an inner ctx during active periods and drained when the ctx cancels.

The `log` field is written by the constructor but not read in 1.0 (no per-session log lines yet — see [parent ADR](../decisions/003-session-addressable-runtime.md)). Kept on the struct so 1.1 can attach without reshaping.

### `Pool`

```go
type Pool struct { /* mu RWMutex, sessions map, bootstrap, log */ }

func New(cfg Config) (*Pool, error)
func (p *Pool) Lookup(id SessionID) (*Session, error)
func (p *Pool) Default() *Session
func (p *Pool) Run(ctx context.Context) error
func (p *Pool) RotateID(oldID, newID SessionID) error
func (p *Pool) RotateForNewSession(oldID SessionID) (SessionID, error) // #1125
func (p *Pool) RotateBootstrapForSelfHeal() (SessionID, error) // #1165
func (p *Pool) Activate(ctx context.Context, id SessionID) error
func (p *Pool) Create(ctx context.Context, label string) (SessionID, error)
func (p *Pool) CreateIn(ctx context.Context, label, spawnDir string) (SessionID, error) // #684
func (p *Pool) GetOrCreate(ctx context.Context, id SessionID, label string) (SessionID, error)
func (p *Pool) GetOrCreateIn(ctx context.Context, id SessionID, label, spawnDir string) (SessionID, error) // #684
func (p *Pool) Revive(id SessionID, label, spawnDir string) (*Session, error) // #1487; no ctx — never spawns
func (p *Pool) List() []SessionInfo
func (p *Pool) Rename(id SessionID, newLabel string) error
func (p *Pool) ResolveID(arg string) (SessionID, error)
func (p *Pool) SetTransitionObserver(obs TransitionObserver) // #659; call before Run
```

`New` generates a `SessionID`, constructs the underlying `*supervisor.Supervisor` from `cfg.Bootstrap`, and installs the result as the single bootstrap entry. Both `NewID` failure and `supervisor.New` failure are wrapped (`sessions: generate bootstrap id: %w`, `sessions: bootstrap supervisor: %w`) and treated as fatal-at-startup.

`Lookup`:

- empty id → bootstrap entry, no error
- known id → that entry
- non-empty unknown id → `ErrSessionNotFound` (sentinel, matchable via `errors.Is`)

`Default()` is a separate accessor with the same body minus the empty-string branch — startup paths that need the bootstrap don't carry an `error` return they know is impossible.

`Run` wraps the bootstrap session and (when `ClaudeSessionsDir` is set) the rotation watcher under `errgroup.WithContext`. The 1.2b-B errgroup wrap is the extension point Phase 1.1's N-session fan-out reuses by adding one `g.Go(sess.Run)` per pool entry. As of 1.1a-A1 (#72), `Run` exposes the live group on `*Pool` (see *Supervisor handle* below) so post-`Run` code paths can join the same supervised set; the bootstrap itself is now scheduled through that seam.

`Activate(ctx, id)` (1.2c-A) is a thin wrapper that resolves `id` and calls `Session.Activate`. Symmetry with the rest of the surface; future routers get a single entry point. Returns `ErrSessionNotFound` for unknown ids.

`Create(ctx, label)` (1.1a-A2) is the user-facing seam for minting a new session. See *Pool.Create* below for the full sequence and failure modes.

`RotateID` (1.2b-A) atomically swaps the in-memory entry keyed by `oldID` with one keyed by `newID`, updates the bootstrap pointer if `oldID` was the bootstrap, bumps `last_active_at`, and persists. `p.mu` (write) is held across the entire operation, and `sess.id = newID` is written inside the same brief `sess.lcMu` section as `lastActiveAt` (#866) — `sess.id` is a two-lock field, written under both `Pool.mu` and `lcMu`, read race-clean while holding either. `RotateID(x, x)` is a no-op; unknown `oldID` returns `ErrSessionNotFound`. This is the load-bearing seam shared between startup reconciliation and the live-detection (`/clear` while claude is running) work — see [jsonl-reconciliation.md](jsonl-reconciliation.md) and [rotation-watcher.md](rotation-watcher.md). See *Concurrency* below and [codebase/866.md](../codebase/866.md) for the full two-lock rationale.

`RotateForNewSession(oldID)` (#1125, `security-sensitive`) is the **daemon-driven** analog of the rotation watcher's `onRotate`: where `onRotate` observes claude having *already* self-rotated (a `/clear` keystroke landed and produced a new `<uuid>.jsonl`), `RotateForNewSession` **drives** the rotation itself for the `new_session` control verb's direct stream-json path (#1125 routing, consuming `(*streamsup.Runner).RestartFresh`, #1124). It mints a fresh id via `NewID()` (returning the error verbatim, no mutation, on a `crypto/rand` failure), then under `Pool.mu` (write): `ErrSessionNotFound` if `oldID` is absent (a TOCTOU guard — the binding may vanish between the caller's resolve and this call); the re-key itself is a call to `rekeyLocked(oldID, newID)`, an unexported helper extracted **byte-identical** to `RotateID`'s prior inline body (stamp `sess.id`/`lastActiveAt` under `lcMu`, move the map entry, flip `p.bootstrap` if `oldID` was it) — `RotateID`'s exported signature and its `onRotate` caller are untouched, this is a pure internal factoring shared by both callers; then `registerAllocatedUUIDLocked(newID)` — **the one asymmetry vs. `onRotate`**. In the watcher's case the rotated-to id is deliberately *not* in the skip-set (that absence is how a self-rotation is detected in the first place); here the daemon is *about* to spawn `claude --session-id <newID>` itself, so the id **must** already be registered or the watcher's own CREATE handler double-rotates it. `saveLocked()` follows, with a failure Warn-logged and swallowed (in-memory state is already authoritative — same best-effort-durability posture as `RotateID` and `rebindConversation`). Off-lock, it fires `notifyTransition(SessionTransition{PreviousID: oldID, NewID: newID, Reason: ReasonClear})` — reusing the existing `ReasonClear` wire value rather than adding a new one, since from the client's view a direct `new_session` and a `/clear` are the same observable event (a fresh session started); this also drives the `RebindSession` conversation rebind, exactly as `onRotate`'s fire does. **Ordering is load-bearing**: the caller (`cmd/pyry`'s `startFreshRunner`) must call this *before* `RestartFresh`, so the skip-set registration is published under `Pool.mu` before the fresh spawn's `<newID>.jsonl` CREATE can fire — reversing the order reopens the double-rotation race. See [codebase/1125.md](../codebase/1125.md) and [rotation-watcher.md](rotation-watcher.md) for the skip-set mechanism this reuses.

`RotateBootstrapForSelfHeal()` (#1165) is the `internal/supervisor` crash-loop
self-heal seam: `supervisor.Run` calls it (via the `Config.SelfHeal func()
error` closure, wired only on the bootstrap `supCfg`) after `FastCrashThreshold`
consecutive fast non-zero exits, to get the daemon off a wedged pinned id.
Mints via `NewID()`, then under a single `Pool.mu` (write) hold reads the
*current* `p.bootstrap`, guards it's still present (`ErrSessionNotFound` on a
TOCTOU), re-keys via the same shared `rekeyLocked` helper `RotateID` and
`RotateForNewSession` use, and `saveLocked()`s (failure Warn-logged and
swallowed — in-memory rotation is authoritative for the running daemon,
matching `RotateForNewSession`'s best-effort-durability posture). **Unlike
`RotateForNewSession`, it does neither of that method's two extras**: no
`registerAllocatedUUIDLocked` skip-set entry, and no `notifyTransition`
(`ReasonClear`) client signal. Both omissions are deliberate — see
[ADR 033](../decisions/033-supervisor-self-heal-dedicated-rotation-no-skip-set.md)
for the full rationale, but in short: the rekey commits `p.bootstrap → newID`
*before* the next spawn creates `<newID>.jsonl`, so the rotation watcher's
`ref.ID == stem` guard already no-ops on that CREATE with no skip-set entry
needed (structurally identical to cold start); and a self-heal is a
supervisor-internal crash-recovery action, not a user `/clear`, so firing
`ReasonClear` would misrepresent it to clients. The rotated id needs no push
into the spawn path — the pre-existing `ResolveSessionID` (#839) pull
resolves it on the very next spawn automatically. See [codebase/1165.md](../codebase/1165.md).

### `Config` / `SessionConfig`

```go
type Config struct {
    Bootstrap         SessionConfig
    Logger            *slog.Logger
    RegistryPath      string        // sessions.json path; "" disables persistence (test-only)
    ClaudeSessionsDir string        // claude's <uuid>.jsonl dir; "" disables the rotation
                                    // watcher + #838 growth-confirm probe resolver (#839:
                                    // no longer gates a startup reconcile — that's removed)
    IdleTimeout       time.Duration // default per-session eviction window; 0 disables
    BootstrapEvicted  bool          // true → bootstrap parks evicted, spawns no claude (#761)
    RunnerFactory     RunnerFactory // MANDATORY since #1348 deleted internal/supervisor; nil is a
                                    // construction error ("sessions: Config.RunnerFactory is required")
}

type SessionConfig struct {
    ClaudeBin  string
    WorkDir    string
    ResumeLast bool
    ClaudeArgs []string
    Bridge     *supervisor.Bridge // nil = foreground

    BackoffInitial time.Duration
    BackoffMax     time.Duration
    BackoffReset   time.Duration

    IdleTimeout    time.Duration  // 0 inherits Config.IdleTimeout
}
```

`SessionConfig` mirrors the relevant fields of `supervisor.Config`; `New` translates one to the other. Defaults (claude bin lookup, backoff timings) are applied by `supervisor.New` — `sessions.New` does **not** duplicate them.

`ResumeLast` maps to `--continue` on restart, as today. The locked-design `claude --session-id <uuid>` invocation is deliberately **not** plumbed in 1.0 — Phase 1.1+ adds it.

### `Runner` interface + `RunnerFactory` (#1077, corrected #1580 for #1348 fallout)

`Session.sup` is typed `Runner` (`internal/sessions/runner.go`):

```go
type Runner interface {
    State() State
    WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error
    WaitForPTY(ctx context.Context) error
    Run(ctx context.Context) error
    Restart(args []string)
    SetSpawnArgs(args []string) // #1580 — installs the NEXT spawn's argv, no kill
    RevokeBypass() error        // #1604 — drops bypass on the LIVE child, no kill
}

type RunnerFactory func(cfg RunnerConfig) (Runner, error)
```

**#1348 deleted `internal/supervisor`.** There is now exactly **one** production implementation:
`cmd/pyry`'s `streamRunner` adapter, wrapping `*streamsup.Runner` — the adapter exists because Go has no
covariant return on interface satisfaction and the concrete runner's `State` returns `streamsup.State`,
not `sessions.State`. Its compile-time proof, `var _ sessions.Runner = streamRunner{}`, lives with it in
`cmd/pyry/streamsup_runner.go` — **not** in this file, since the type it asserts about is not in this
package. The other five implementations are all test doubles: `fakeRunner`/`lifecycleRunner`
(`internal/sessions/runner_test.go`), `raceRunner` (`internal/sessions/session_evict_race_test.go`),
`stubRunner` (`cmd/pyry/session_router_test.go`), `baseRunner`
(`cmd/pyry/inbound_deliver_rotation_test.go`), `modelListRunner`
(`cmd/pyry/session_model_list_test.go` — `stubRunner` plus the one method
`resolveBoundModelList` asserts for, so `stubRunner` itself stays the
ready-made not-implemented fixture for that resolver's refusal case, #1857).

`Config.RunnerFactory` has **no default**: it is mandatory, and a nil factory is a construction error out
of `sessions.New` (`"sessions: Config.RunnerFactory is required"`) — there is no implicit PTY
implementation to fall back to. Production supplies `cmd/pyry`'s `newStreamRunnerFactory`; tests supply
their own doubles. The "nil selects a wrapper over `supervisor.New`" rollback story this section used to
tell was true for the #1077 Strangler-Fig slice and stopped being true when #1348 removed the thing being
rolled back to.

There is no `Session.Supervisor()` accessor. Consumers that need methods off the concrete runner
(`cmd/pyry`'s interrupt / new_session wiring) reach `*streamsup.Runner` via `Session.Runner()` — which
keeps returning the `Runner` interface — and a **capability type-assertion** on an anonymous method-set
interface, e.g. `interface{ Interrupt() error }` (`interruptRunner`),
`interface{ RestartFresh(string) }` (`startFreshRunner`), `interface{ BeginRotation() func() }`
(`beginRotationOrNoop`) — all in `cmd/pyry/main.go` — and, since #1857,
`interface{ ModelList() (turnevent.ModelList, bool) }` (`resolveBoundModelList`), which lives in its own
`cmd/pyry/session_model_list.go` rather than `main.go`: a fourth twin in the conversation-keyed resolver
family (alongside `resolveBoundRunner` / `resolveBoundSession` / `resolveBoundRunSettings`), placed in a
topic file the way `outstandingQueues` is, so adding it manufactures no merge conflict in the
high-churn wiring file. None of these assert to the concrete `*streamsup.Runner` type; `Session.Runner()`
holds the value-typed `streamRunner` adapter in production, so a literal `.(*streamsup.Runner)` assertion
would be `ok == false` always and fall through silently to an inert default — the same class of hazard AC
5's #1580 review round caught in this file's own first-draft correction. `Interrupt`/`RestartFresh`/
`BeginRotation`/`ModelList` stay off `sessions.Runner` deliberately (adding any would be speculative
surface, or in `ModelList`'s case would drag every test double in both packages into the diff for no
compile-time guarantee since its consumer sits in `cmd/pyry`, not `internal/sessions`); `SetSpawnArgs` and
`RevokeBypass` are on it instead, because — per their own docs — widening is compile-checked across the
whole one-production/five-double set, whereas a type assertion at a future call site would fail silently
(open, for `RevokeBypass`) at runtime and either fall back to `Restart` or leave a posture un-revoked while
the caller reports success — the exact outcomes these two swap/revoke-only methods exist to avoid. Both
share the same placement rule: their consumer, `Pool.UpdateSettings`, is inside `internal/sessions`, so
there is no `cmd/pyry` dispatch site to type-assert at. See [codebase/1580.md](../codebase/1580.md) for the
swap-only installer and [codebase/1604.md](../codebase/1604.md) for the in-band revocation.

A runner double armed for a **bootstrap-session** capability test cannot be armed at construction time:
`sessions.New` invokes `RunnerFactory` while building the bootstrap entry, so the test does not know the
bootstrap's session id until after the runner already exists. `modelListRunner` (#1857) solves this by
reading a shared, mutex-guarded plan **keyed by session id at call time** rather than storing an answer on
the runner value itself — which also gives "nothing reported" for free as the plan's zero state, and lets
a refusal test arm the bootstrap with a distinguishable sentinel list so a broken isolation guard produces
a visibly wrong (leaked) answer instead of an empty one that could pass unnoticed.

**Capability type-assertions evade `staticcheck`'s unused check from the other direction too
(#1550).** This package's one prior instance of the pattern — `probeUsable`'s
`probe.(interface{ Available() bool })` assertion over `rotation.Probe` — orphaned
`noopProbe.Available` invisibly for as long as it existed: `staticcheck` never flagged the
method, because a method reachable only from its own test still counts as "used", and the
assertion (not an interface implementation) is the only thing that made it reachable at all.
Deleting the resolver that held the assertion (#1550) is what stranded it, and finding that
required a by-hand repo-wide grep, not a gate failure. The five capability interfaces above
share the same structural blind spot: removing `interruptRunner`, `startFreshRunner`,
`beginRotationOrNoop`, or `resolveBoundModelList` would not, by itself, surface any strandable
method on `*streamsup.Runner` via a build or vet failure — that has to be checked by hand at
deletion time, the same way #1550's spec did. `resolveBoundModelList` (#1857) is the sharper
case of the same coin: it ships with **no production caller at all** yet (its consumer, #1858,
lands separately) and stays reported as "used" purely because a `_test.go` reference counts —
verified empirically against the `staticcheck` version `make check` installs before relying on
it, rather than assumed.

**Typed-nil-in-interface trap for downstream consumers (#1101).** A call site that assigns `w.sup` (the `*supervisor.Supervisor` returned by `Supervisor()`) straight into a consumer-declared interface field inherits a footgun on the stream-json path: a nil `*supervisor.Supervisor` wrapped in an interface value is a **non-nil interface holding a nil pointer**, so the consumer's `== nil` guard silently fails and any method call on it panics on the nil receiver. `cmd/pyry/relay.go`'s `Snapshotter: w.sup` wiring hit exactly this and was fixed by a `screenSnapshotterOrNil` helper that returns a genuine nil when `sup == nil` — see [codebase/1101.md](../codebase/1101.md). Two sibling wiring sites carry the same unfixed trap as of #1101: `SessionStarter: w.sup` and the modal resolver's `w.sup` argument (both `cmd/pyry/relay.go`) — flagged out of scope there, not yet guarded.

See [codebase/1077.md](../codebase/1077.md) and spec [`1077-sessions-runner-seam.md`](../../specs/architecture/1077-sessions-runner-seam.md).

### `supervisor.Config.SessionID` — construction-safe id seam for a stream-json factory (#1108)

`RunnerFactory` (above) hands a `supervisor.Config` to the factory before any runner exists. Until #1108, the only id a factory could read from that `Config` was `ResolveSessionID`, a spawn-time closure — useless to a `streamsup.New` caller, which requires a non-empty `SessionID` **eagerly at construction** (see [streamsup-package.md](streamsup-package.md) § Public API) and has no lazy-resolve path.

`Config.SessionID string` is the fix: an additive, eager field set unconditionally at both pool construction sites — `Pool.New`'s bootstrap `supCfg` (`SessionID: string(bootstrapID)`, the same local `ResolveSessionID`'s closure reads via `p.BootstrapID()`) and `Pool.buildSession` (`SessionID: string(id)`, alongside the pre-existing `--session-id <id>` in `ClaudeArgs`). The PTY supervisor **never reads it** — `buildClaudeArgs` keeps its 4-parameter signature, so the field is structurally unable to reach argv, and `ResolveSessionID` remains the only spawn-time id source on that path. That inertness is the rollback guarantee: setting the field is a no-op until a non-nil `RunnerFactory` reads it.

**Construction-fixed, not rotation-safe.** `ResolveSessionID` re-reads `p.bootstrap` every spawn, so a `/clear` rotation (`Pool.RotateID`) is picked up on the PTY path with no extra wiring (#839, above). `SessionID` is a snapshot taken once at construction — a future stream-json runner built from it will **not** see a later rotation. This is accepted, not deferred-as-a-gap: `streamsup.New` is itself structurally fixed-at-construction (`buildArgs` `--resume`s the same id forever, no on-disk-mtime adoption, no rotation mechanism by design), so a lazy resolver on the seam side couldn't grant rotation-safety without also rewriting `streamsup.New` — out of scope for a package with no production consumer yet (the `RunnerFactory` that reads `cfg.SessionID`, `streamRunnerFactory`, landed in #1109 but is not yet wired into production — that's #1081).

See [codebase/1108.md](../codebase/1108.md) and spec [`1108-streamsup-construction-safe-session-id-seam.md`](../../specs/architecture/1108-streamsup-construction-safe-session-id-seam.md).

### Supervisor handle (1.1a-A1)

Two unexported fields on `*Pool` hold the live errgroup while `Run` is in progress:

```go
runGroup *errgroup.Group   // set in Pool.Run, cleared on return
runCtx   context.Context   // the gctx returned by errgroup.WithContext
```

Both are guarded by `Pool.mu` and read together so a caller never sees a half-initialised handle. `Pool.Run` writes them under `Pool.mu` (write) right after `errgroup.WithContext`, and clears them in a `defer` so a panicking goroutine still resets the handle.

```go
func (p *Pool) supervise(sess *Session) error
```

`supervise` schedules `sess.Run(gctx)` on the live group. RLock-snapshots `runGroup` + `runCtx`, releases the lock, then calls `g.Go` off-lock. Returns `ErrPoolNotRunning` (`var ErrPoolNotRunning = errors.New("sessions: pool not running")`) when the handle is `nil` — i.e. before `Run` has wired it or after `Run` has cleared it. Matchable with `errors.Is`.

The helper is unexported. Phase 1.1a-A2's `Pool.Create(ctx, label)` is the consumer: build a `*Session`, then `p.supervise(sess)` to fan it onto the same supervised set as the bootstrap. The bootstrap fan-out inside `Pool.Run` is itself rewritten to call `supervise` (the helper is exercised in production from day one rather than living dormant).

The watcher fan-out (`g.Go(func() error { return w.Run(gctx) })`) does **not** go through `supervise` — the watcher is not a `*Session`.

**Lock discipline.** `supervise` takes only `Pool.mu` (RLock) briefly; it does not call into `Session.lcMu`. The documented orders (`Pool.mu → Session.lcMu`, `Pool.capMu → Pool.mu → Session.lcMu`) are unchanged. Concurrent `supervise` callers contend only with `Run`'s one-shot setup and one-shot teardown, never with each other.

**Race windows.** A `supervise` call racing teardown either acquires RLock first (sees the handle, schedules onto a group whose ctx is about to be cancelled — `Session.Run` handles `ctx.Done` cleanly) or after (sees `nil`, returns the sentinel). The "scheduled onto a soon-cancelled group" case is safe: `errgroup.Group.Go` is documented as concurrency-safe and the scheduled func observes the cancelled ctx immediately, exiting via the existing shutdown path.

### `Config.BootstrapEvicted` + `Pool.Ready()` (#761)

Two purely-additive primitives for **embedded pool hosts** that map each caller
session onto its own `Pool.Create`'d claude and must run no eager, unaddressed
bootstrap claude. The sole consumer is `pyry acp`'s composition root (epic #600);
both preserve today's behaviour at their zero value with no call-site fan-out.
Design rationale: [ADR 026](../decisions/026-embedded-acp-pool-exact-one-claude.md).

**`Config.BootstrapEvicted bool`** — when true, `New` forces the bootstrap to
`stateEvicted` *after* the warm/cold-start `lcState` choice, so `Pool.Run →
supervise(bootstrap) → runEvicted` parks it and it **spawns no claude**. The
default (both warm-start and fresh-mint) forces `stateActive` — the daemon-mode
startup contract "claude is available" ([ADR 016](016-bootstrap-ignores-persisted-lifecycle-state.md))
— which eager-spawns a bootstrap claude the moment `Pool.Run` starts. With
`BootstrapEvicted`, a single `Pool.Create` is the only interactive claude
(ACP's divergence 6 / hard-cost invariant).

```go
if cfg.BootstrapEvicted {
    lcState = stateEvicted        // → activeCh open, evictedCh closed (existing branch)
}
```

**Soundness constraint (documented on the field).** Sound only when the
bootstrap is **never Activated and never persisted evicted**: the pool keeps it
as a dormant `Default()`/`Lookup("")` placeholder until `Run`'s ctx cancels. Do
**not** pair it with a `RegistryPath` that would persist "evicted" for the
bootstrap and later warm-start it with no attach client to drive `Activate` —
that is the [#202 hang the warm-start branch guards against](016-bootstrap-ignores-persisted-lifecycle-state.md).
Embedded, non-persistent hosts (`RegistryPath == ""`) satisfy this by
construction.

**`Pool.Ready() <-chan struct{}`** — a channel (internal `readyCh`, created in
`New`) closed **once** by `Run` under `sync.Once` (`readyOnce`), right after
`runGroup`/`runCtx` are wired and `supervise(bootstrap)` returns nil — i.e. once
`Pool.Create`'s `supervise` can no longer return `ErrPoolNotRunning`. Before
`Run` is ever called the channel is open (never ready). Read lock-free (set once
in `New`, never reassigned; safe to `select` on repeatedly and from multiple
goroutines).

```go
func (p *Pool) Ready() <-chan struct{}   // closed once Create's supervise is safe
```

Why this is a **correctness gate, not a nicety**: `Create → supervise` returns
`ErrPoolNotRunning` when `runGroup` is nil, and on that path `Create` returns a
**non-empty id** for a session that is minted (would-be persisted) but **never
supervised** — there is no public re-supervise, and `Activate` on it blocks
forever. The session is permanently stuck; the failure is unrecoverable. A
microsecond window between backgrounding `pool.Run` and the first `Create` is
enough to strand the first call. So an embedded host must background `Run`, then
`select` on `Ready()` before issuing any `Create`. Same channel-backed
readiness shape as [ADR 023](023-activate-waits-pty-readiness.md)'s
`Supervisor.WaitForPTY`.

### `SessionSettings` + `claudeSettingsArgs` (#833)

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

### `Pool.UpdateSettings` (#840)

The persistence seam the v2 settings verb (#841, split into wire vocabulary
#844 + handler #845) calls to change an existing session's `Model` / `Effort`
/ `YOLO` after creation — `SessionSettings` above was immutable
post-construction until this ticket.

```go
type SettingsUpdate struct {
    Model  *string
    Effort *string
    YOLO   *bool
}

func (p *Pool) UpdateSettings(id SessionID, update SettingsUpdate) error
```

`SettingsUpdate` is the presence contract: a `nil` field leaves the stored
value untouched; a non-nil field overwrites it, including `""` for
`Model`/`Effort` and `false` for `YOLO` — both distinguishable from omitted.
`YOLO`'s `*bool` is the security-relevant choice: an absent (`nil`) `YOLO` can
never enable bypass, only an explicit non-nil `*true` can (fail-safe-OFF by
construction, not convention).

Same shape as `Pool.Rename`: takes `Pool.mu` (write), looks up the session
(miss → `ErrSessionNotFound`, no entry created), overlays present fields onto
a copy of `sess.settings`, no-op short-circuits if nothing changed
(`SessionSettings` is comparable), else swaps in the merged value and calls
`saveLocked`, rolling the field back to its previous value if the save fails.
Never takes `Session.lcMu` — `settings` is a `Pool.mu`-guarded field, same as
`label`, so no lock-order hazard with `saveLocked`'s internal `lcMu`
re-acquire (`docs/lessons.md` § "Lock order with callback into the host").

Validating untrusted model/effort values is explicitly **not** this method's
job — it operates on operator-trusted input; the wire handler (#845, a
charset/length shape check for `Model`, a closed enum for `Effort`) owns the
untrusted → trusted crossing. See [codebase/840.md](../codebase/840.md).

**Live-apply on a real change (#842, #1581).** After a successful persist of a
real change (not a no-op, not a failed save), `UpdateSettings` recomposes the
session's full spawn argv and live-applies the change — so a single client
message both persists **and** takes effect on the currently-running child,
without waiting for the session's next spawn.
`Session.spawnBase []string` holds the settings-free argv (template/bootstrap
args + any construction-time resume suffix), set alongside the full
`ClaudeArgs` at both construction sites (`Pool.New`, `Pool.buildSession`).
`Session.spawnArgs(settings SessionSettings) []string` —
`append(slices.Clone(s.spawnBase), claudeSettingsArgs(settings)...)` — is the
**single** argv-recompose path outside construction, reusing `claudeSettingsArgs`
verbatim so the YOLO fail-safe has exactly one origin. `UpdateSettings` captures
`newArgs := sess.spawnArgs(merged)` and `sup := sess.sup` under `Pool.mu`, then
releases the lock before either live-apply branch runs — **outside** `Pool.mu`,
never touching `Session.lcMu`.

**Which branch, and why (#1581, redrawn by #1604).** `inBandDeliverable(update)`
partitions on what the update carried — which fields, and for `YOLO` its
*value* too — never on merged-vs-previous per field. `SetSessionSettingsPayload`'s
three `omitempty` pointers are a presence contract, so a client changing one
setting sends one field, and a present `YOLO` is read for its direction rather
than diffed against stored state:

- **A change claude accepts on the already-open stream** — a non-empty
  `Model`/`Effort`, and/or a `YOLO` **revoke** (`true → false`) — →
  `sup.SetSpawnArgs(newArgs)` then `deliverSettingsInBand`, which writes
  `/model <v>` and `/effort <v>` as ordinary user turns via
  `sup.WriteUserTurn(context.Background(), "", …)`, and a bypass revoke as a
  `set_permission_mode` control request via `sup.RevokeBypass()` (#1604) —
  model, then effort, then bypass, one send per **present** field. Claude
  accepts all three on the stream the daemon already holds open and applies
  them to the running session, so **nothing is killed and the transcript
  survives**. The `SetSpawnArgs` call is not optional: it is `Restart`'s swap
  half (#1580), and skipping it would let the operator's change silently
  revert on the next crash-respawn or evict → `Activate` — this is what makes
  a revocation survive those too, with no new mechanism. Swap **before**
  write — the install is the durable half. Delivery is fire-and-forget: every
  write error is logged at `Info`
  (`"sessions: in-band settings command not delivered"`, fields `session` / a
  fixed `setting` literal / `err` — **never** the value, the payload bytes, or
  the conversation id) and swallowed, so the client sees success. They cannot
  be classified anyway: `internal/sessions` must not import
  `internal/streamsup`, and the reachable set (`ErrNoLiveChild`,
  `turncommit.ErrDropped`, a wrapped pipe failure) all warrants the same
  response, with the dominant case — an evicted session — not a degradation.
- **Everything else** → `sup.Restart(newArgs)`, unchanged. That is a `YOLO`
  **enable** (`false → true`) — claude gates the escalation on the launch argv
  and refuses the control request in words (#1595 measured this live against
  claude 2.1.220), so only a respawn under the recomposed argv can grant it —
  and clearing model or effort to `""` ("run at claude's own default", which
  `claudeSettingsArgs` expresses by *omitting* the flag, and for which no
  `/model` invocation means "revert"). A `YOLO` revoke takes this branch too
  when a present-but-empty `Model`/`Effort` is mixed into the same frame — the
  empty-value reject wins, but costs nothing: the restart recomposes argv from
  the **merged** settings, so the respawn still carries the revocation. No
  frame can lose a revocation by mixing. Clean partition — never both
  mechanisms for one change, no case left unserved.

The mechanism swap was a bug fix, not an optimisation. The respawn re-execs
with `--resume` on a session that has never run a turn; claude answers
`No conversation found with session ID` and exits 1, and the daemon retries
forever on a widening backoff (observed 2026-08-18). The in-band path
**avoids** that rather than fixing it — no resume, no lost transcript, no
crash-loop. Live-applying a `YOLO` revoke is #1604 — the enable direction has
no in-band form; claude refuses it. #1574 may **not** delete `Restart`: the
enable direction keeps a live production caller. **#1605 was split, not
landed as such**: the live-claude proof that this composed path (`Pool` →
`inBandDeliverable` → `deliverSettingsInBand` → `Runner.RevokeBypass`)
reaches a real child without tearing it down is #1622, measured against
claude 2.1.220 — see
[`e2e-realclaude.md`](e2e-realclaude.md#interactive_stream_inband_bypass_revoke_test-go-1622).
The question #1605 also implied but #1622 deliberately leaves open — whether
the revoked posture is *behaviourally enforced*, not just echoed back — is a
sibling ticket that consumes #1622's harness, not yet landed. See
[codebase/1581.md](../codebase/1581.md) and
[codebase/1604.md](../codebase/1604.md).

`Supervisor.Restart(args []string)` (`internal/supervisor`) swaps the live
spawn args under a leaf `restartMu` and, if a child is currently running,
forces it to exit (SIGKILL via a per-iteration derived ctx) so the
supervisor's existing forever-retry loop relaunches it with the new argv,
resuming the conversation. When no child is running, the swap alone applies
on the session's next spawn (e.g. its next `Activate`) — which incidentally
closes a latent gap: a *reused* supervisor (evict → activate within one
process) previously kept stale baked args across that boundary. `Restart` is
non-blocking and fire-and-forget by design — see [ADR
031](../decisions/031-settings-restart-fire-and-forget.md) for why the AC
"a failed live-apply must not surface as a false success" is satisfied by a
deterministic kill + the supervisor's retry guarantee rather than a
synchronous wait. Not-found, no-op, and persist-failure paths in
`UpdateSettings` all return before `Restart` is ever called — a still-correct
running child is never disturbed. See [codebase/842.md](../codebase/842.md)
and `docs/specs/architecture/842-live-restart-on-settings-change.md` (§
Security review, verdict PASS) for the full design and the argument that the
YOLO fail-safe survives the restart.

**Security (`security-sensitive` ticket).** `YOLO`'s fail-safe posture — a
missing or corrupt on-disk value can never enable bypass — rests entirely on
`YOLO` being a plain `bool` (Go zero value = `false`) plus the pre-existing
`loadRegistry` whole-parse strictness (a malformed value fails the entire load,
not just that field). No new decoder was needed. See [ADR
030](../decisions/030-plain-bool-failsafe-persisted-flag.md) for the full
argument. Model/effort values on this path are operator-trusted (local 0600
registry, delivered to `exec.CommandContext` as discrete argv tokens — no
shell, no injection surface); validating *wire-supplied* model/effort is
explicitly out of scope here and deferred to #826b.

**Reference, not a shared code path:** `internal/agentrun/ptyrunner`
([ptyrunner-package.md](ptyrunner-package.md)) already models `--model` /
`--effort` / bypass as first-class knobs for `pyry agent-run`, and its
`buildArgs` supplied the exact flag spellings here — but that path forbids
`--dangerously-skip-permissions` outright (#538) and uses
`--permission-mode dontAsk` instead. The two packages do not share code.

See [codebase/833.md](../codebase/833.md) for the full implementation writeup.

### `Pool.DefaultSettings` (#847)

The read counterpart to `Pool.UpdateSettings` above — a locked accessor that
surfaces the bootstrap session's currently-persisted `SessionSettings` across
the package boundary. `SessionInfo` (from `Pool.List()`) does not carry
`settings`, and `settings` is a private field readable only under `Pool.mu`,
so a consumer outside `internal/sessions` cannot reach it without this.

```go
func (p *Pool) DefaultSettings() (SessionSettings, bool)
```

Mirrors `Default()`'s lock discipline exactly: `p.mu.RLock()`, resolve
`p.sessions[p.bootstrap]` **fresh** on every call (not cached) so the result
stays correct across a `RotateID` (which flips `p.bootstrap` under the write
lock). Returns `(SessionSettings{}, false)` when there is no bootstrap to read
from (the embedded evicted-bootstrap host, or a zero-value `&Pool{}` map-miss)
so a consumer falls back to daemon defaults — no error path.
`SessionSettings` is a value type, so the return is a snapshot copy with no
aliasing of the pool's live field.

Shipped unwired in #847; wired by #848, which populates the `screen_snapshot`
reply's `model`/`effort`/`yolo` fields via a closure over this accessor built
in `cmd/pyry/main.go` and threaded through `V2SessionConfig.SnapshotSettings`
(see [v2-session-manager.md § Inbound screen-snapshot handler](v2-session-manager.md)
and [protocol-package.md § Screen-snapshot payloads](protocol-package.md)).
Deliberately has no conversation-keyed variant: the snapshot source is always
the bootstrap session, so settings-source == snapshot-source by construction.
See [codebase/847.md](../codebase/847.md) and [codebase/848.md](../codebase/848.md).

### `Pool.SettingsFor` (#1585)

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

### `newProbePreferredTranscriptResolver` (#838, migrated onto `internal/transcript` in #1149)

```go
func newProbePreferredTranscriptResolver(
    dir string, probe rotation.Probe, pidFn func() int, pinnedID func() string,
) func(ctx context.Context) (string, int64, error)
```

The probe-preferred replacement for the `supervisor.Config.ResolveTranscript`
closure wired at `pool.go`'s bootstrap block (`newTranscriptResolver` stays as
the AC5 no-lsof fallback — its other caller, `reconcileBootstrapOnNew`, was
removed in #839; see [jsonl-reconciliation.md](jsonl-reconciliation.md), now
marked retired). Follow-up to #827's turn-stream fix, applied to the *other*
live-child bootstrap consumer of the shared sessions dir: the delivery-confirm
growth baseline. As of #1149 both this resolver and `newTranscriptResolver`
are thin adapters over the `internal/transcript` leaf (#1148) — the dir
canonicalisation, confidentiality guard, by-id/probe selection, and UUID-stem
matcher all live there now; this function composes those primitives in
Family A's own dispatch order and maps every no-result to Family A's
`("", 0, nil)` convention. See [transcript-package.md](transcript-package.md).

- **`!probeUsable(probe)`** (the no-lsof `noopProbe`, detected via the local
  `availabilityReporter{ Available() bool }` interface — a probe that omits
  the method is treated as usable) → delegates to `newTranscriptResolver(dir)`
  wholesale (which itself now calls `transcript.Newest(dir)`). This is the
  *only* returned closure that may emit a non-nil error, because it *is*
  today's newest-by-mtime behaviour (AC5).
- Otherwise, `canonicalDir := transcript.CanonicalDir(dir)` is computed once
  at construction. Per resolve:
  - **Pinned-id path** (#989): `pinnedID()` non-empty and
    `transcript.ValidStem`-valid → `transcript.StatByID(dir, id)`; its error
    maps to `("", 0, nil)` **without falling through to the probe** — the
    `ValidStem` pre-check is the branch selector (a miss on a *valid* stem is
    no-baseline-no-probe; an *empty/invalid* stem is what falls through).
  - **Probe path** (pinned id nil/empty/invalid): `pid := pidFn()`;
    `res, _ := transcript.Probed(dir, canonicalDir, probe, pid)` — the
    confidentiality guard (AC4: probed path must canonicalise to a
    `<uuid>.jsonl` directly inside `canonicalDir`) and every benign no-result
    (`pid <= 0`, empty open, guard reject, vanished-before-stat) all collapse
    inside `transcript.Probed` to `(Result{}, nil)`. The adapter additionally
    **swallows** the one error `Probed` *can* surface (a failing
    `probe.OpenJSONL`) via `res, _ :=` — never propagated, never an mtime
    fallback.
- **The nil-error no-baseline convention is load-bearing** and is the inverse
  of the sibling resolvers in `cmd/pyry` (Family B, #1150 — which *wrap* that
  same `Probed` error because their turn-stream subscriber retries on error):
  a non-nil error here would divert `confirmViaTranscriptGrowth` to the #668
  stochastic Committed-chip fallback — the very heuristic the growth-confirm
  exists to replace. `("", 0, nil)` instead keeps the caller on the growth
  path (deliver, then poll for the daemon's own child's file to appear/grow,
  else loud `ErrTurnNotCommitted`). The neutral `transcript` core surfaces one
  error signal; each adapter maps it to its own convention.
- Drops the sibling's `resolvedOnce`/`sawEmpty` cold/warm tail-offset state —
  this consumer needs the true current byte size every call as a `grew()`
  baseline, never a rewound stream offset.

**Wiring (`pool.go`, `New`):** the resolver needs the bootstrap child's *live*
PID, but `supCfg` is copied by value into `supervisor.New` before the
`Supervisor` (the PID source) exists. `New` declares `var bootstrapSup
*supervisor.Supervisor` above the `ClaudeSessionsDir != ""` block; `pidFn`
closes over it (`bootstrapSup == nil` → `0`, a guard never observed in
practice); `bootstrapSup = sup` is assigned immediately after
`supervisor.New` returns. The only caller of the resolver (`WriteUserTurn`) is
reached from a goroutine created long after `New` returns, so the read
strictly follows the assignment — race-free by goroutine-creation
happens-before, no mutex needed. `probe := newProbe(cfg.Logger)` reuses the
existing factory (the rotation watcher builds its own separate instance;
the probe is a stateless lsof/`/proc` wrapper). Unchanged by #1149 — the
4-arg signature and this wiring are untouched by the adapter migration.

See [codebase/838.md](../codebase/838.md) for the original implementation
writeup, [codebase/1149.md](../codebase/1149.md) for the `internal/transcript`
migration, and [rotation-watcher.md](rotation-watcher.md) for the
`rotation.Probe` interface this resolver shares with the watcher and #827.

---
### `Pool.BootstrapID` + `supervisor.Config.ResolveSessionID` (#839, resume branch #1164)

```go
func (p *Pool) BootstrapID() SessionID
```

RLock, resolve `p.bootstrap` fresh, RUnlock — the same shape as
`Default()`/`DefaultSettings()`. Deliberately reads `p.bootstrap`, never
`Default().ID()`/`sess.id`: `p.bootstrap` is the `Pool.mu`-guarded value, and
reading it keeps the spawn path off `Session.lcMu` entirely (`sess.id` is
guarded by both `Pool.mu` and `lcMu` as of #866 — see *Concurrency* below —
but there is no reason for this call to take `lcMu` when `Pool.mu` already
gives it a race-clean answer).

**Wiring (`pool.go`, `New`):** the bootstrap `supCfg` sets `ResumeLast: false`
and (as of #1164) a named `resolveID := func() string { return string(p.BootstrapID()) }`
closure feeding `ResolveSessionID: func() (string, bool) { ... }` — see below.
Same late-bind problem as the `bootstrapSup` pattern above — the closure
needs to reference `p`, but `supCfg` is built (and copied by value into
`supervisor.New`) before the `&Pool{...}` literal exists. `New` forward-declares
`var p *Pool` before `supCfg`, then reassigns (`p = &Pool{...}`, not
`p := &Pool{...}`) at the existing construction site. `ResolveSessionID` is
only *called* inside `supervisor.Run`'s spawn loop, on a goroutine started
long after `New` returns, so the read is race-free by the same
goroutine-creation happens-before argument as `bootstrapSup`.

`supervisor.Run` calls the resolver at the start of every spawn (first run
and every restart) and passes both return values into `buildClaudeArgs`.
Since #1164, `ResolveSessionID` returns `(id string, resume bool)`, resolved
in one call so the resume decision always targets the id about to be
spawned: non-empty id with `resume=false` → `--session-id <id>` appended,
`--continue` suppressed (#839's original create path, byte-identical when no
transcript exists yet); non-empty id with `resume=true` → `--resume <id>`
instead (claude 2.1.199 refuses `--session-id` for a transcript that already
exists — a hard daemon restart, or the in-process respawn after the first
spawn created `<id>.jsonl` — so this reattaches instead of crash-looping);
empty id (defensive only, never hit on the bootstrap path) → falls through
to the existing `ResumeLast`/`--continue` logic. `pool.go`'s closure decides
`resume` by probing `transcript.StatByID(cfg.ClaudeSessionsDir, id)` fresh on
every call — exists → `resume=true`; absent, empty id, or
`ClaudeSessionsDir == ""` → `resume=false`. Because the resolver re-reads
`p.bootstrap` on every call, a `/clear` rotation the watcher already
persisted via `RotateID` is picked up automatically on the *next* spawn — no
push notification, no supervisor argv-swap method, no `onRotate` wiring.
This also retires the startup `reconcileBootstrapOnNew` call (deleted from
`New` by #839): with the session flag deterministic from the first spawn,
there is nothing left to adopt-by-mtime at startup. See
[jsonl-reconciliation.md](jsonl-reconciliation.md) (marked retired),
[codebase/839.md](../codebase/839.md), [codebase/1164.md](../codebase/1164.md),
and [ADR 032](../decisions/032-bootstrap-resume-per-spawn-existence-probe.md).

**Growth-confirm resolver re-sourced (#1164).** `newProbePreferredTranscriptResolver`'s
4th argument (a `func() string` — the pinned id) used to be fed
`supCfg.ResolveSessionID` directly; now that the field returns a 2-tuple, the
growth-confirm resolver is re-sourced to the named `resolveID` closure
instead (id-only, unchanged behaviour for that consumer).

### `writeMCPSettings` + `Session.settingsPath` (#943, relocated #1518)

```go
func writeMCPSettings(registryPath string, id SessionID) (string, error)
```

Writes a per-session settings file containing exactly
`{"enableAllProjectMcpServers":true}` — no `permissions` key at all — and
returns its absolute path. Fixes the interactive session-pool spawn's version
of the modal claude 2.1.199 renders when the operator has any MCP server
configured (user-level `~/.mcp.json` or project `.mcp.json`): without
`enableAllProjectMcpServers:true`, claude shows "N new MCP servers found in
this project" at startup, the PTY readiness check reports `tuidriver:
unexpected dialog at startup`, and every live turn wedges. Agent-run already
carried this flag (`d10ce87`); the session-pool spawn — the path the phone and
desktop remote heads drive — never did.

**Two branches, selected by `registryPath` (#1518).** `registryPath == ""`
(persistence disabled — the mode `Pool.dataDir()` reports as `""`, and most of
this package's tests build a pool in) keeps #943's original behaviour
byte-for-byte: `os.TempDir()`, random `pyry-session-settings-*.json` name,
`0600`. `registryPath != ""` writes to
`<abs(dir(registryPath))>/session-settings/<id>.json` instead — a per-purpose
subdirectory under the daemon data dir, created on demand at `0700` (mirrors
`archived-sessions/`, see `disposeJSONLLocked`). The file "must outlive every
respawn" (a backoff restart and the #842 live settings-restart both re-exec
`spawnBase` with the same `--settings` path, and nothing re-reads or
re-creates it between spawns) is why it moved: `os.TempDir()` is subject to an
OS age-based reaper that a multi-day-uptime daemon can hit, deleting the file
out from under a live session and reopening the #943 modal wedge — or worse,
crash-looping the child into permanent backoff. `id` is gated on `ValidID` on
the data-dir branch only: a warm-start bootstrap id is decoded straight out of
the registry file with no shape check upstream, and after this change it names
a file, so an unvalidated id could traverse outside the data dir.

The write is atomic on both branches (`os.CreateTemp` in the target dir →
encode → `Sync` → `Close` → `Rename`), the same recipe `saveRegistryLocked`
uses for `sessions.json`. On the data-dir branch the scratch pattern is
`.settings-*.json.tmp` (dotted, so a SIGKILL-orphaned scratch file is never
mistaken for a real settings file, and a directory `*.json` glob counts
sessions exactly). Naming the file `<id>.json` rather than a random suffix
bounds the on-disk set by session count instead of daemon-restart count — a
warm-start restart against the same registry overwrites its own file instead
of accumulating a new one, so no startup sweeper is needed.

**Deliberately not a reuse of `internal/agentrun/settings`**
([agentrun-settings-subpackage.md](agentrun-settings-subpackage.md)). That
writer always stamps `permissions.defaultMode:"dontAsk"` (a headless
deny-default posture) and requires a non-empty `allowedTools` — both wrong for
an interactive operator session, which must keep today's normal tool-prompt
behaviour. `writeMCPSettings` is a duplicate of the tempfile +
cleanup-on-error discipline, not an extraction — consistent with
the project's "resist over-DRY on duplicated primitives" convention (see also
[agentrun-trust-subpackage.md](agentrun-trust-subpackage.md) for the same
pattern applied to workspace trust).

**Wired into `spawnBase`, not `claudeSettingsArgs`.** The file's content is
per-session immutable — it never varies with `Model`/`Effort`/`YOLO` — so it
belongs in `spawnBase`, the settings-free argv base both `Pool.New`
(bootstrap) and `Pool.buildSession` (minted) compose before appending
`claudeSettingsArgs(settings)`. Because `Session.spawnArgs` (the #842
live-restart recompose) and a backoff restart's re-exec both derive from
`spawnBase`, placing `--settings <path>` there means it survives every
respawn automatically — no additional wiring at either recompose site.

**Error handling.** A `writeMCPSettings` failure at construction (`Pool.New`
or `buildSession`) is a hard error, wrapped `sessions: write mcp settings:
%w` — a daemon that started anyway would silently wedge every turn on the
modal, so a loud startup failure is preferred. Once a session is confirmed
live, cleanup is best-effort (`_ = os.Remove(...)`) and runs only after the
child is confirmed dead so a backoff respawn can never race the removal:
`Pool.Remove` calls it after `sess.Evict(ctx)` returns (minted sessions); the
bootstrap is never `Remove`-d, so its file is removed by a `defer` in
`Pool.Run` that fires on ctx cancel / shutdown.

**Every error return between the write and construction's own success also
removes the file (#1518).** Under #943 a handful of tempfile leaks on those
paths were accepted as rare and harmless — `os.TempDir()`'s reaper collected
them eventually. #1518's data-dir relocation made that reaper absent, so the
same leaks became permanent, and both write sites now guard with a
`defer`-and-success-flag (`built := false; defer func(){ if !built {
os.Remove(settingsPath) } }()`, flipped just before the successful return):
`Pool.New` covers its `newRunner` failure and its `saveLocked` failure,
`buildSession` covers its `newRunner` failure. `CreateIn`'s `saveLocked`
rollback (one level up, discarding a freshly `NewID`-minted session) removes
the file too — safe because that id is never reused. `materialise`'s discard
branches (same-id race loser, `saveLocked` rollback, `ErrPoolNotRunning`
rollback) deliberately do **not**: with the id-derived filename, the
discarded session's `settingsPath` is byte-identical to the winner's live
one, so removing it there would delete a live session's settings file and
reopen the #943 modal wedge. See
[docs/specs/architecture/1518-session-settings-under-data-dir.md](../../specs/architecture/1518-session-settings-under-data-dir.md)
§ Error handling for the full enumeration, and
[codebase/1518.md](../codebase/1518.md) for why that asymmetry is a decision,
not an oversight.

**Blast radius beyond `internal/sessions`.** The ACP-embedded pool
(`cmd/pyry acp`, #761) spawns through the same `buildSession`, so three
argv-equality tests there (`TestACP_SessionNew_SpawnsOneInteractiveClaude`,
`TestACP_SessionLoad_ResumesExistingClaude`,
`TestACPConformance_FullSessionDrive`) broke and needed a `stripMCPSettingsPair`
test helper to strip the new `--settings <path>` pair before asserting the
`--session-id`-only interactive-path shape. Any future change to the shared
`spawnBase` composition should check `cmd/pyry` argv assertions too — the
blast radius is not scoped to one package just because the change is.

See [codebase/943.md](../codebase/943.md) and
[docs/specs/architecture/943-interactive-spawn-mcp-settings.md](../../specs/architecture/943-interactive-spawn-mcp-settings.md)
for the original design, and [codebase/1518.md](../codebase/1518.md) and
[docs/specs/architecture/1518-session-settings-under-data-dir.md](../../specs/architecture/1518-session-settings-under-data-dir.md)
for the data-dir relocation and error-path cleanup.

### Pool.Create (1.1a-A2)

The user-facing primitive that ties together every existing seam — `NewID`, `saveLocked`, `RegisterAllocatedUUID`, `supervise`, `Activate` — to mint a fresh non-bootstrap session. One well-tested entry point so downstream callers (Phase 1.1a-B's `sessions.new` verb, future channel-driven auto-mint) don't re-derive the sequence.

```go
func (p *Pool) Create(ctx context.Context, label string) (SessionID, error)
```

**Two unexported fields support it,** captured at `New()` time and read-only after:

- `sessionTpl SessionConfig` — shallow copy of `cfg.Bootstrap`. `Create` clones `ClaudeArgs`, appends `--session-id <uuid>`, sets `ResumeLast = false`, and (if `tpl.Bridge != nil`) mints a fresh `*supervisor.Bridge`. The bridge presence is the service-mode signal — sharing one bridge across N sessions would multiplex their I/O into a single client view.
- `idleTimeoutDefault time.Duration` — mirrors `Config.IdleTimeout`. `Create` applies the same fallback `New()` applies to the bootstrap (per-session zero → pool default).

**Sequence (in order — each step depends on the previous):**

1. `NewID()` — fresh UUIDv4
2. Build per-session `SessionConfig`: clone `ClaudeArgs`, append `--session-id <uuid>`, `ResumeLast=false`, fresh `Bridge` in service mode
3. `supervisor.New(supCfg)` — wrap on `sessions: create supervisor: %w` failure (no state mutated yet)
4. Build `*Session` in `stateEvicted` (label verbatim — empty preserved as empty; `bootstrap=false`; `createdAt=lastActiveAt=now`)
5. **Persist phase under `p.mu` (write):** insert into `p.sessions[id]`, call `saveLocked()`. On save failure, `delete(p.sessions, id)` rollback under the same lock and return `("", err)`. Lock released.
6. `RegisterAllocatedUUID(id)` — primes the rotation watcher's skip-set. Must fire before claude opens the JSONL or the CREATE looks like a `/clear` rotation. The 30s TTL is well clear of the sub-second spawn path.
7. `supervise(sess)` — schedules `sess.Run(gctx)` on the live errgroup. On `ErrPoolNotRunning`, return `(id, err)` — entry is on disk, no lifecycle goroutine.
8. `Activate(ctx, id)` — cap-aware. The new session is in `stateEvicted` so it doesn't count toward `active` for the cap pre-flight; `pickLRUVictim` excludes the target. On Activate failure, return `(id, err)`.

**Why persist *before* activate.** A save failure with claude already running leaves an unsupervised orphan: claude has opened its JSONL, started a conversation, and pyry has no on-disk record. The next pyry start won't reconcile it; the JSONL becomes a ghost. A registry-only entry that didn't activate is benign — same shape as a session that ran, idled out, and is now reattachable. A subsequent attach goes through `Pool.Activate` (the same primitive used here) and brings it up. See [lessons.md § Lock-order pitfalls when a callee persists](../../lessons.md#lock-order-pitfalls-when-a-callee-persists) for the lock-order discipline.

**Why register-allocated *after* persist, *before* activate.** `RegisterAllocatedUUID` has a 30s TTL window. It must fire before claude opens the JSONL (so the watcher's skip-set has the UUID when the CREATE fsnotify event lands). Doing it after persist (rather than before) makes the order robust to a slow registry write — TTL countdown starts from a known-recent moment.

**Why supervise *before* Activate.** `Session.Activate` sends on `activateCh` (buffered 1) then waits on `activeCh` until the lifecycle goroutine flips to active. If `sess.Run` isn't running yet, the buffered signal is held and `Activate` blocks until ctx cancels. So `supervise` (which schedules `sess.Run`) must precede `Activate`. If `supervise` returns `ErrPoolNotRunning`, bail before calling `Activate` — no goroutine to wake.

**Cap pre-flight via `Pool.Activate` directly (choice (a)).** Two viable shapes were considered: (a) call `Pool.Activate(ctx, id)` and let its existing cap path evict, or (b) run a cap pre-flight before registering. Choice (a): the new session IS in the pool and IS in `stateEvicted` — same shape as any other evicted session being reactivated. No duplicated cap logic, no new code path through `pickLRUVictim`, no transient inconsistent view from `Snapshot`/`Lookup` (which (b) would create).

**Failure-mode discriminator: id-or-empty.** The returned `SessionID` is the caller's signal:

| Failure point | Caller sees | On-disk state | In-memory state |
|---|---|---|---|
| `NewID` (rng) | wrapped err, `""` | unchanged | unchanged |
| `supervisor.New` | wrapped err, `""` | unchanged | unchanged |
| `saveLocked` | err verbatim, `""` | unchanged | rolled back |
| `supervise` | `ErrPoolNotRunning`, valid id | entry persisted | entry in map; no lifecycle goroutine |
| `Pool.Activate` | err verbatim (often `ctx.Err`), valid id | entry persisted | entry in map; lifecycle goroutine running; lcState may race to active |

Empty id ⇒ "nothing persisted, nothing to clean up." Non-empty id ⇒ "entry on disk, decide what to do (retry Activate, accept the eventual lifecycle, leave it for next pyry start)." Use `errors.Is(err, ErrPoolNotRunning)` to distinguish the not-running case from an Activate failure.

**ctx cancellation race — mid-flight only (narrowed, #1805).** `Session.Activate` now fails fast on an already-cancelled `ctx`: an entry guard reads `ctx.Err()` before taking `lcMu`, so a `ctx` that is cancelled or expired *at the call* returns immediately with no `activateCh` signal sent — for that case "Activate error → claude not running" **is** a hard invariant. The race described below still holds for a cancellation that lands *after* the call is already past the guard: if the caller cancels `ctx` after `supervise` succeeded but before `Activate` returns, `sess.Activate` may have already sent the buffered signal on `activateCh`. The lifecycle goroutine respects the *pool's* run-context (the errgroup's `gctx`), not the caller's, so the session may still spin up to active even though `Create` returns `(id, ctx.Err)`. Tests should not depend on the invariant for a `ctx` that was live when `Activate` was called and cancelled during the wait — only for one already dead at the call.

**Lock order — unchanged.** `Create` introduces no new ordering edges:

| Step | Locks | Order |
|---|---|---|
| Register + persist | `Pool.mu` (write) → `Session.lcMu` (briefly inside `saveLocked`) | `Pool.mu → Session.lcMu` ✓ |
| RegisterAllocatedUUID | `Pool.mu` (write) | trivially ✓ |
| supervise | `Pool.mu` (RLock) | trivially ✓ |
| Activate (cap path) | `capMu → Pool.mu` (RLock in pickLRU) → `Session.lcMu` | `capMu → Pool.mu → Session.lcMu` ✓ |

Critically: `Create` does NOT hold `p.mu` across `supervise`, `Activate`, or `RegisterAllocatedUUID`. The lock is taken only for the register+persist couple, then released. Concurrent `Create` calls each mint their own UUID via `crypto/rand` and serialise on `Pool.mu` for the persist couple; cap-path serialisation continues through `capMu` if `activeCap > 0`.

**Bridge: fresh per session in service mode.** `tpl.Bridge != nil` ⇒ `supervisor.NewBridge(p.log)` for the new session; `tpl.Bridge == nil` (foreground mode) ⇒ `nil`. Foreground-mode `Create` is operationally odd (the new session's output goes to its JSONL but has no live client) — not gated against, since the control verb path will only call `Create` in service mode.

**No new public types or sentinels.** `Pool.Create` is the only new exported name. `ErrPoolNotRunning` (from #72) is the only sentinel `Create` propagates.

### Pool.List (1.1b-A)

The typed read primitive Phase 1.1b-B's `pyry sessions list` CLI verb (#46-B) calls instead of poking at `sessions.json` directly. One method, one new value type, no error path.

```go
type SessionInfo struct {
    ID             SessionID
    Label          string         // synthetic "bootstrap" substituted for the
                                  // bootstrap entry when its on-disk label is
                                  // empty; on-disk value is unchanged.
    LifecycleState lifecycleState
    LastActiveAt   time.Time
    Bootstrap      bool           // true for the bootstrap entry; lets
                                  // consumers disambiguate without re-checking IDs.
}

func (p *Pool) List() []SessionInfo
```

**Deep-copy by construction.** Every field is a value type (`SessionID`/`string`/`time.Time` are values; `lifecycleState` is a `uint8` enum). Mutating a `SessionInfo` cannot affect pool state or registry contents — no defensive cloning, no documentation contract that callers can violate.

**Sort: `LastActiveAt` desc, `SessionID` asc tiebreak.** Most-recent-first matches operator intuition (the session you just used is at the top). The id tiebreak makes ordering deterministic across calls — important for unit tests where time freezes; degenerate at runtime. Uses `sort.Slice` to match `registry.go`'s style.

**Bootstrap label substitution lives here, not in 46-B's renderer.** The wire payload is self-explanatory (every consumer gets `"bootstrap"` instead of the empty string for the unlabelled bootstrap entry) and the on-disk registry entry is **not** mutated. An operator-set bootstrap label passes through verbatim — `Bootstrap` (the bool) is the discriminator, not the label string.

**Why a new type, not extending `SnapshotEntry`.** `SnapshotEntry{ID, PID}` exists for the rotation watcher's closure-over-primitives boundary (`internal/sessions/rotation` cannot import `internal/sessions`). Adding `lifecycleState` to it would either bloat every rotation snapshot or push the enum into `rotation`'s import set. Two distinct types, one per consumer, is cleaner than a shared shape.

**Lock order — unchanged.** `Pool.mu` (RLock) → `Session.lcMu` (Lock). Identical to `Pool.saveLocked` and `Pool.pickLRUVictim`. The bootstrap flag, label, and id are immutable post-`New` / post-`RotateID` from any reader holding `Pool.mu`, so they're read off-`lcMu`; `lcState` and `lastActiveAt` MUST be read under `lcMu` (the lifecycle goroutine writes them under `lcMu` in `transitionTo` and `touchLastActive`). Each session's pair is read under one `lcMu` acquire — no torn reads.

**Read-only.** No `lastActiveAt` bump, no state transition, no `persist()` call. The AC's "registry fields unchanged on disk after the call" is a direct consequence of not calling `saveLocked`.

**Concurrent List:** standard RWMutex semantics — concurrent `List` callers don't contend with each other; concurrent `List + Create` (or `List + RotateID`) sees one ordering or the other, neither corrupts the result. Race-clean under `-race`.

### Pool.Rename (1.1c-A)

The typed write primitive Phase 1.1c-B's `pyry sessions rename` CLI verb (#47-B) calls instead of poking at `sessions.json` directly. One method, no new types, no new sentinel errors.

```go
func (p *Pool) Rename(id SessionID, newLabel string) error
```

**Sequence under `Pool.mu` (write):**

1. Resolve `id` in `p.sessions`. Unknown ⇒ `ErrSessionNotFound`; in-memory and on-disk state byte-identical to before (no `saveLocked` call).
2. If `sess.label == newLabel`, no-op return — skips `saveLocked` so the registry mtime stays stable for an idempotent rename. Same precedent as `RotateID(x, x)`.
3. Otherwise: snapshot `prev := sess.label`, set `sess.label = newLabel`, call `saveLocked()`. On save failure, restore `sess.label = prev` and return the error verbatim.

**Why `Pool.mu`, not `Session.lcMu`.** `Session.label` is read under `Pool.mu` by `List` (RLock) and `saveRegistryLocked` (Lock — caller holds write). The lifecycle goroutine in `Session.Run` does not read `label`. So `Pool.mu` is the correct guard; taking `lcMu` would add a lock-order edge for no benefit. The doc-comment on `Session.label` (`session.go:68-72`) was updated to reflect that `label` is mutable via `Pool.Rename` under `Pool.mu` (write).

**Why hold `Pool.mu` across the disk write.** Releasing it between the in-memory mutation and the disk write would let a concurrent `Lookup` observe the new label while the disk still has the old one — exactly the in-memory-vs-on-disk drift the existing locking discipline rules out. Matches `RotateID` and every `Pool.persist`-from-`Session.transitionTo` callback.

**Save-failure rollback is belt-and-suspenders.** `saveRegistryLocked`'s temp+rename discipline makes the rename the commit point — partial writes are unreachable, so disk state never disagrees with the rolled-back in-memory state. The rollback exists so a subsequent retry has consistent inputs and a `Lookup` after a failed `Rename` doesn't return a label that isn't on disk. Error returned verbatim (no `Rename:`-specific wrap) — `saveLocked`/`saveRegistryLocked` already produce well-prefixed `registry: …` errors and double-wrapping breaks `errors.Is` symmetry with other persist sites (`RotateID`, `Create`, `Session.transitionTo`).

**Bootstrap-label semantics.** `Rename` writes the verbatim string through to disk and does **not** branch on `sess.bootstrap`. Two cases fall out for free:

- `Rename(bootstrapID, "")` — clears on-disk label; `Pool.List` then re-applies the synthetic `"bootstrap"` substitution introduced in #60.
- `Rename(bootstrapID, "primary")` — persists `"primary"`; `Pool.List` reflects it verbatim (no synthetic substitution, since the on-disk value is non-empty). The disk record is still bootstrap-flagged (`Bootstrap: true`).

The substitution rule lives in `List`, not in `Rename` — keeping the writer simple and the substitution uniform across consumers.

**No validation.** Empty strings are explicitly permitted by the AC; nothing else is mentioned. Length caps, character-class restrictions, and uniqueness checks belong at the CLI layer (47-B) where operator-input policy lives. Same posture as `Pool.Create`'s unvalidated `label` parameter.

**Strict full-`SessionID` only.** UUID-prefix resolution is a CLI/UX concern; the pool primitive matches on the full id to keep the API surface and the test matrix small. Phase 1.1c-B can resolve a prefix via `Pool.List` and then call `Rename` with the full id.

### Pool.Remove (1.1d-A1)

The typed delete primitive the future `pyry sessions rm` CLI verb (#65) calls instead of touching processes or `sessions.json` directly. One method, one new exported sentinel (`ErrCannotRemoveBootstrap`), no other type additions.

```go
var ErrCannotRemoveBootstrap = errors.New("sessions: cannot remove bootstrap session")
func (p *Pool) Remove(ctx context.Context, id SessionID) error
```

**Sequence — delete-then-evict.**

1. Take `Pool.mu` (write).
2. Resolve `id` in `p.sessions`. Unknown ⇒ release `Pool.mu`, return `ErrSessionNotFound` (in-memory + on-disk state byte-identical, no `saveLocked` call).
3. If `sess.bootstrap` ⇒ release `Pool.mu`, return `ErrCannotRemoveBootstrap` (bytes-identical, same as above).
4. `delete(p.sessions, id)`, then `saveLocked()`. On save failure: restore `p.sessions[id] = sess`, release the lock, return the error verbatim. (Mirrors `Pool.Rename`'s rollback discipline.)
5. Release `Pool.mu`.
6. Call `sess.Evict(ctx)`. Returns only after the child has exited (or `ctx` cancels). The on-disk JSONL is **not** touched — disposition (archive / purge) is 64-A2 / #95.

**Why delete-then-evict (not evict-then-delete).** Holding `Pool.mu` across `Session.Evict` deadlocks: the lifecycle goroutine's `transitionTo` calls `Pool.persist`, which reacquires `Pool.mu` (write). The cap-policy path (#41) hits the same constraint and uses `capMu` as the outer mutex; here the simpler resolution is to release `Pool.mu` after the in-memory delete commits — concurrent `Lookup` / `Activate` / `Rename` / `List` callers see the session as gone from that moment on, so there's no half-removed state for any observer to witness, and no risk of a re-spawn race against a session that's about to die.

**Why `ctx` (not the AC's bare `id` shape).** `Session.Evict` already accepts a context; passing one through keeps the (potentially long-lived) termination interruptible and matches `Pool.Activate(ctx, id)` / `Pool.Create(ctx, label)`.

**Bootstrap rejection is structural, not policy.** The bootstrap is the per-process invariant `Pool.Lookup("")` resolves to. Removing it would leave the pool in a state no caller can satisfy without an explicit re-bootstrap pass. The sentinel surface lets the CLI distinguish "operator targeted bootstrap" from "id not found" without string-matching error text.

**Termination reuse.** `Pool.Remove` does not re-implement SIGTERM/SIGKILL/grace logic. `Session.Evict` already drives the supervisor's child via `exec.CommandContext` cancel ⇒ SIGKILL ⇒ `cmd.Wait` returns. The supervisor today does not have a SIGTERM grace window — earlier docs that referenced one describe an aspiration, not the current behaviour. SIGKILL is uncatchable, so no fallback path is needed.

**Already-evicted sessions are a fast path.** If `sess` is already in `stateEvicted` (prior idle eviction, prior cap-policy eviction, or warm-started in evicted), `Session.Evict` is an immediate no-op — no second persist runs, the registry write in step 4 is the only mutation.

**Lifecycle goroutine after Remove (#775).** `Pool.Remove` closes the session's write-once `removedCh` — after the registry-remove commits (past the `saveLocked` rollback branch) and off `Pool.mu`, next to the already-off-lock `Evict` call. `Session.Evict` then drives an active session `active → evicted`; once `sess.Run`'s loop reaches `runEvicted` it observes the closed `removedCh` and returns **`nil`** — a clean exit, never `context.Canceled`, so the pool's **shared** errgroup (`gctx`) does not cancel and tear down any sibling session or the relay leg. The goroutine and everything it captures (`*Session`, `*supervisor.Supervisor`, `*supervisor.Bridge`, logger) are released at `Remove` time rather than surviving until pool shutdown — the #94 "bounded resource cost / one orphan per remove" note **no longer applies**. `removedCh` is watched **only** in `runEvicted`, never `runActive`: a select race there could return before `transitionTo(stateEvicted)` closes `evictedCh` and hang `Remove`'s `Evict` call. `Run`'s post-`runEvicted` `isRemoved()` re-check makes removal win over a racing `Activate`, so a removed session can never resurrect. The non-removable bootstrap allocates a `removedCh` that is never closed (`ErrCannotRemoveBootstrap`). See [codebase/775.md](../codebase/775.md).

**No `Session.lcMu` taken.** `Pool.Remove` does not read `lcState` / `lastActiveAt` / `attached`; the lifecycle goroutine continues to take `lcMu` inside `transitionTo` exactly as before. Lock-order graph (`Pool.capMu → Pool.mu → Session.lcMu`) is unchanged.

**Strict full-`SessionID` only.** Same posture as `Pool.Rename`. Empty-id is *not* the bootstrap shorthand it is in `Pool.Lookup` — empty falls through to the "not in map" branch and returns `ErrSessionNotFound`. Destructive operations require an explicit id.

### Pool.Remove JSONL disposition (1.1d-A2 / #95)

Phase 1.1d-A2 adds a `RemoveOptions` parameter so callers pick the on-disk disposition. The signature became `Pool.Remove(ctx, id, opts RemoveOptions) error`; `RemoveOptions{}` (zero value) is byte-identical to the 1.1d-A1 behaviour above (JSONL untouched).

```go
type JSONLPolicy uint8

const (
    JSONLLeave   JSONLPolicy = iota // do not touch the JSONL (default)
    JSONLArchive                    // mv to <pyry-data-dir>/archived-sessions/<uuid>.jsonl
    JSONLPurge                      // delete the JSONL
)

type RemoveOptions struct {
    JSONL JSONLPolicy
}

func (p *Pool) Remove(ctx context.Context, id SessionID, opts RemoveOptions) error
```

**Why an enum, not two booleans.** A `bool Archive`/`bool Purge` shape makes (true, true) a representable-but-illegal state. The enum makes the "exactly one disposition" property type-level, the zero value is well-defined (Leave), and the dispatch switch is exhaustive.

**Why a struct, not a positional `JSONLPolicy` parameter.** Future options (`Force`, `Reason`, …) stay additive at zero call-site churn. Same precedent as stdlib `os.RemoveAll` would have been if it took options.

**Pyry data-dir resolution.** No new config knob — the per-instance data-dir is the **parent of `Pool.registryPath`** (`~/.pyry/<sanitized-name>/sessions.json` ⇒ `~/.pyry/<sanitized-name>/`). `claudeSessionsDir` is claude's directory (the JSONL *source*), not the pyry-owned destination root. When `registryPath == ""` (test/disabled mode), `JSONLArchive` errors with `"sessions: archive requires a registry path"`; `JSONLPurge` and `JSONLLeave` are no-ops.

**Disposition runs under `Pool.mu` after `saveLocked`.** Single critical section, single observable transition: a concurrent `Pool.List` either sees the session present (registry + JSONL both untouched) or absent (registry + JSONL both at their final state). The held-lock window grows by one stat + one rename or unlink — single-syscall granularity, well inside the existing `saveLocked` envelope. POSIX inode semantics keep the operation safe even though claude may still hold the JSONL fd open (rename preserves the fd→inode binding; unlink lets pending writes drain into the soon-to-be-orphaned inode).

**Source-absent semantics.** Both `JSONLArchive` and `JSONLPurge` are success no-ops when the live JSONL is missing — symmetric "ensure the file is at its target state" intent.

**Destination-exists semantics.** Archive errors (wrapping `fs.ErrExist`) when `<archiveDir>/<uuid>.jsonl` already exists. Re-archiving the same UUID is almost always a bug; silent overwrite would lose transcript history. The `errors.Is(err, fs.ErrExist)` shape leaves room for a future CLI `--force`.

**`os.Rename`, not copy + unlink.** Source and destination both live under `$HOME` in normal deployments — same filesystem. EXDEV would surface as a clear error rather than silent corruption. No copy-then-delete fallback today; defer until observed.

**Failure ordering — registry committed before disposition.** On `saveLocked` failure: in-memory delete rolls back, JSONL untouched, child not terminated. On `disposeJSONLLocked` failure: registry already removed (in-memory + on-disk), `Session.Evict` is *still* called (the registry says the session is gone, the child must follow), the disposition error is returned. If disposition and Evict both fail, disposition wins (the new failure mode this signature introduces, and the more actionable one).

**Bootstrap and unknown-id rejection still run before disposition.** `Remove(bootstrapID, {JSONL: JSONLPurge})` does *not* touch the bootstrap's JSONL — structural invariants take precedence over destructive opts.

### Pool.ResolveID (1.1e-A)

The typed prefix resolver Phase 1.1e-B's `pyry attach <id>` wire + CLI surface (and any future verb taking a session selector) consumes instead of inlining the same `strings.HasPrefix` walk over `Pool.List`. The natural pairing with `Pool.Lookup`:

| Caller-supplied input | API |
|---|---|
| canonical `SessionID` (or `""`) | `Pool.Lookup(id)` |
| user input string (UUID, prefix, or `""`) | `Pool.ResolveID(arg)` |

```go
var ErrAmbiguousSessionID = errors.New("sessions: ambiguous session id")

func (p *Pool) ResolveID(arg string) (SessionID, error)
```

**Resolution order, all under `Pool.mu` (RLock):**

1. `arg == ""` ⇒ bootstrap id, no error. Same seam as `Pool.Lookup("")`.
2. `arg` is an exact key in the in-memory `p.sessions` map ⇒ that id, no error. Single map lookup; the short-circuit never falls through to the prefix scan, so a full-UUID match always wins over any coincidental prefix overlap with no extra scan cost.
3. Scan `p.sessions`, collect every `*Session` whose `SessionID` has `arg` as a prefix (`strings.HasPrefix`). Exactly one match ⇒ that id. Zero ⇒ `ErrSessionNotFound` (reused). ≥2 ⇒ `ambiguousError(matches)`.

**Sentinel + `fmt.Errorf("%w: …")` over a struct error type.** The AC required "the simpler shape that keeps `errors.Is` matching cheap" and "no new exported types beyond the new typed error." A struct error with `Matches []SessionRef` would expose a second exported type and force every consumer to either type-assert or duplicate the formatting. The chosen shape — `var ErrAmbiguousSessionID = errors.New(...)` plus `fmt.Errorf("%w:\n%s", ErrAmbiguousSessionID, lines)` — gives `errors.Is` one pointer compare via the wrapped chain, lets the CLI consumer `fmt.Fprintln(os.Stderr, err)` and get a human-readable list verbatim, and stays within the AC's exported-surface budget.

**Match list formatting.** Sorted by `SessionID` ascending (same tiebreak as `Pool.List`) so the error message is deterministic and tests can pin the exact substring. Each line is `<uuid> (<label>)`. The synthetic `"bootstrap"` substitution from `Pool.List` is mirrored one-for-one — when the bootstrap entry's on-disk label is empty, the formatter writes `<uuid> (bootstrap)` rather than `<uuid> ()`. Otherwise operators would see one name in `pyry sessions ls` and another in the disambiguation prompt.

**No `Session.lcMu` taken.** `ResolveID` reads only `id` (mutated by `RotateID` under `Pool.mu` write — invariant documented at that site) and `label` (mutated by `Pool.Rename` under `Pool.mu` write). Both sit under `Pool.mu`'s reader set; no new lock-order edges. `lcState` and `lastActiveAt` are not consulted — `ResolveID` does **not** filter by lifecycle state. An evicted session is still a registry entry, and `pyry sessions rename <prefix>` / `pyry sessions rm <prefix>` must still resolve it. Filtering, if any, is a verb-layer policy (e.g. a future `pyry attach` may bounce active-vs-idle differently).

**No minimum prefix length.** A one-character prefix is accepted as long as it is unique. Refusing short prefixes (1- or 2-char) for safety is a CLI-layer guard, not a pool invariant — the same posture as `Pool.Rename` declining to validate `newLabel` and `Pool.Create` declining to validate `label`.

**No whitespace trimming.** The pool primitive accepts whatever string the caller hands it. Trimming is the CLI's responsibility (`flag` already handles positional args; an explicit `strings.TrimSpace` at the CLI layer is one line).

**Returns `SessionID`, not `*Session` / `SessionInfo`.** Smallest possible surface, symmetric with the rest of the wire/CLI flow: 1.1e-B unmarshals an id from the request, calls `ResolveID`, then routes to `Lookup` / `Activate` / `Remove` with the resolved id. Returning `*Session` would tempt callers to short-circuit the second lookup — but the second lookup is the lock-clean way to guard against a session being removed between resolve and use, and saving the second hashmap probe is not worth the sharp edge.

**Concurrency.** `Pool.mu` (RLock) for the whole call. Concurrent `ResolveID + List` share the read lock and run truly concurrently. Concurrent `ResolveID + writer` (`Rename` / `Create` / `Remove` / `RotateID`) blocks briefly behind the writer and observes either pre- or post-write state — the same race-clean shape callers were already prepared for. Concurrent `ResolveID + ResolveID` share the read lock with no contention. The lock-order graph (`Pool.capMu → Pool.mu → Session.lcMu`) is unchanged.

**A session removed mid-resolve** either appears in the scan (caller then races on the second `Lookup` and sees `ErrSessionNotFound`) or doesn't — both outcomes are valid races the consumer was already prepared for. No error wrapping prefix is added (the wrapped sentinel already begins with `sessions: …`; double-wrapping would just produce `sessions: resolve: sessions: …`).

**Out of scope** (handed to 1.1e-B): wire protocol field carrying the resolved/unresolved id, control verb routing, CLI argument parsing, refactoring 47-B / 48-B's inlined prefix resolvers (opportunistic when those callers are next touched).

### Pool.GetOrCreate (1.3b)

The take-or-create primitive Phase 1.3b's `pyry attach --create-if-missing <uuid>` consumes. SDK consumers (Claudian / `@anthropic-ai/claude-agent-sdk`) mint a UUIDv4 per chat upstream and pass it through; pyry must accept the SDK's id even when no session under that UUID is registered yet. Pairs `Pool.Lookup` and `Pool.Create` into one atomic call:

| Caller | API |
|---|---|
| Server-minted id (CLI `sessions new`) | `Pool.Create(ctx, label)` |
| Caller-supplied id (SDK `attach --create-if-missing`) | `Pool.GetOrCreate(ctx, id, label)` |

```go
var ErrInvalidSessionID = errors.New("sessions: invalid session id")

func (p *Pool) GetOrCreate(ctx context.Context, id SessionID, label string) (SessionID, error)
```

**Take-or-create over insert-or-error.** `GetOrCreate` returns the canonical `SessionID` whether the session was already registered or this call created it — same return contract for both paths. The handler treats both branches identically from the call site onward (`Lookup → Activate → Attach`). The insert-or-error alternative (`CreateWithID` returning `ErrIDInUse`) would force the handler to follow up with a `Lookup`, re-introducing a TOCTOU window that two SDK chats opening simultaneously could race through. See [ADR 014](../decisions/014-get-or-create-take-or-create.md).

**ValidID gate at the Pool boundary.** Empty / non-canonical-UUIDv4 ids return `ErrInvalidSessionID` before any Pool state is touched. The validator runs before the lock is taken, so concurrent calls with different ids contend only briefly through `p.mu`.

**Atomic registration — the load-bearing change.** Unlike `Pool.Create` (which releases `p.mu` between persist and supervise), `GetOrCreate` holds `p.mu` across **all five** of (since #1487 this whole sequence lives in the shared `materialise` core — see [§ `Pool.Revive`](#reviving-a-dropped-session-poolrevive-1487)):

1. Duplicate-id short-circuit (`if existing, ok := p.sessions[id]`).
2. Registry-map insert.
3. `saveLocked()` (registry persist; rolled back on failure via `delete(p.sessions, id)`).
4. `registerAllocatedUUIDLocked(id)` — primes the rotation watcher's skip-set so a concurrent watcher snapshot sees register + skip-set atomically.
5. `g.Go(func() error { return sess.Run(gctx) })` — schedules the lifecycle goroutine.

`g.Go` is non-blocking: the goroutine it spawns parks on `activateCh` / `runCtx.Done()` before doing any pool work. Holding `p.mu` across `g.Go` is therefore safe (no lock-order violations, no deadlock risk). `Activate(ctx)` happens **after** `p.mu` is released — `Activate` has its own (`capMu`, `lcMu`) discipline that would deadlock if held under `p.mu`.

**Why `g.Go` must run inside the critical section.** Without holding `p.mu` across the schedule, a concurrent `GetOrCreate(sameID)` caller could:

1. Acquire `p.mu`, see the registered entry under `if existing, ok := p.sessions[id]`, release `p.mu`, return id.
2. Call `sess.Activate(ctx)` — which sends on `activateCh` (buffered 1) and waits on `activeCh`.
3. Block 30s until ctx times out, because the winner's lifecycle goroutine has not been scheduled yet (and will never close `activeCh`).

The race detector cannot catch it; the failure mode is "long hangs at attach time on the loser." Holding `p.mu` across `g.Go` makes the schedule observable as part of the same critical section that registers the entry, so any same-id observer sees both atomically.

**Two concurrent same-id callers.** One wins the lock, registers, persists, schedules `sess.Run`, releases the lock, proceeds to `Activate`, returns id. The other acquires the lock, observes the now-registered session via the duplicate-id short-circuit, releases the lock, returns id (no error). Both then `Lookup → Activate → Attach`; `Activate` is idempotent (already active → LRU touch, no-op). Net result: exactly one registry entry, exactly one supervised child, exactly one rotation skip-set entry.

**Helper extraction shared with `Pool.Create`.** Two private helpers, both new in this ticket:

- `buildSession(id, label) (*Session, error)` — constructs the per-session supervisor + Session. Touches no Pool state. `Pool.Create` and `Pool.GetOrCreate` call it identically; the supervisor.Config + Session field shape lives in one place.
- `registerAllocatedUUIDLocked(id)` — the lock-held variant of `RegisterAllocatedUUID`. Caller MUST hold `p.mu` (write). The exported `RegisterAllocatedUUID` keeps its current "takes the lock" contract for `Pool.Create`'s caller.

`Pool.Create`'s body shrinks; behaviour is unchanged.

**Failure modes.**

| Failure point | Caller sees | On-disk state | In-memory state |
|---|---|---|---|
| `ValidID` rejects (empty / malformed) | `ErrInvalidSessionID`, `""` | unchanged | unchanged |
| `buildSession` (supervisor.New) | wrapped err, `""` | unchanged | unchanged |
| Take path (id already registered) | id, no error | unchanged | unchanged; caller's `label` silently dropped |
| `saveLocked` | err verbatim, `""` | unchanged | rolled back |
| `runGroup == nil` (Pool.Run not active) | `ErrPoolNotRunning`, `""` | rolled back (best-effort re-save) | rolled back |
| `Pool.Activate` | err verbatim, valid id | entry persisted | entry in map; lifecycle goroutine running |

The take-path's silent label drop is documented in the docstring. Today's only caller (`handleAttach`) passes `""`; if a future caller wants take-or-create-with-label-update, that's a separate primitive (`Rename` after `GetOrCreate`).

**Lock order — unchanged.** `Pool.mu (write) → Session.lcMu` (briefly inside `saveLocked`) → release → `Pool.Activate` (`capMu → Pool.mu (R) → Session.lcMu`). The `g.Go`-under-`p.mu` edge introduces no new ordering: `errgroup.Group.Go` takes its own internal mutex and the spawned goroutine's parking on `activateCh` does not touch `p.mu`.

**Tests** (in `internal/sessions/pool_get_or_create_test.go`): `TestValidID` (table — empty/short/long/wrong-dash/non-hex/v3/non-RFC-4122-variant + canonical-NewID-output); `TestPool_GetOrCreate_Take_ReturnsExisting` (existing label preserved on take); `TestPool_GetOrCreate_Create_Persists` (caller's id written verbatim, claude spawned); `TestPool_GetOrCreate_PersistsPostDetach` (AC #1 — registry survives evict; this test surfaced ticket #169's persist-ordering race against `-race`); `TestPool_GetOrCreate_InvalidID` (`errors.Is(err, ErrInvalidSessionID)` for empty/malformed/v3); `TestPool_GetOrCreate_PoolNotRunning` (`ErrPoolNotRunning`, registry rolled back); `TestPool_GetOrCreate_ConcurrentSameID` (AC #4 — N=8 goroutines racing on one id, exactly one registry entry, label is one of the inputs, `-race`-clean); `TestPool_GetOrCreate_HonorsCap` (cap=1; bootstrap evicted via `Pool.Activate`'s cap-aware path).

### Per-session spawn workdir: `CreateIn` / `GetOrCreateIn` (#684)

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

### Reviving a dropped session: `Pool.Revive` (#1487)

`Pool.New` materialises exactly one `*Session` from `sessions.json` — the bootstrap (`pickBootstrap` is the only reader of `reg.Sessions`). Every minted entry is parsed, discarded, and then erased by the first `saveLocked`. A conversation whose `CurrentSessionID` points at one of them is left with a healthy binding onto a session the pool does not have.

```go
func (p *Pool) Revive(id SessionID, label, spawnDir string) (*Session, error)
```

**No `context.Context` — that absence is the API contract.** `Revive` registers the session and returns; it never spawns claude and never blocks. The returned `*Session` is at `stateEvicted` with an open `activeCh` and a closed `evictedCh`, which is byte-for-byte the shape an idle-evicted session has, so it respawns on the next `Pool.Activate` through the **existing** lazy-respawn path. Reviving introduces no new lifecycle path; the caller owns the `Activate`.

**Shared core with `GetOrCreateIn`.** Both route through the unexported `materialise(id, label, spawnDir string, settings SessionSettings) (*Session, took bool, error)`, which holds the whole `ValidID` → build → register → `saveLocked` → skip-set → `runGroup` guard → `g.Go` sequence described under [§ Pool.GetOrCreate](#poolgetorcreate-13b) — including every rollback. Exactly **two** things differ between the two callers. The first is what happens after: `GetOrCreateIn` calls `Activate` on the register path (and, per its long-standing contract, *not* on the take path), `Revive` calls it never. That take/register split became the `took` flag rather than an inline early return, so `TestPool_GetOrCreate_Take_DoesNotActivate` pins it — a `Revive`d session is the only fixture that can hold a registered-but-never-activated session to assert against. The second is the `settings` argument, below.

**Zero settings — fail-closed by construction.** `materialise` takes the settings as a parameter and forwards them verbatim to `buildSession`, making no policy decision of its own; `Revive` is the caller that passes `SessionSettings{}`, so a revived session's argv carries no `--dangerously-skip-permissions` even if the dropped entry persisted `yolo: true`. Deliberate, and free: a phone-set permission bypass does not survive a daemon restart. Restoring it would require reading the discarded `registryEntry`, i.e. the rehydrate-at-`New` design this ticket rejected.

That parameter is also what keeps `Revive` off the mint path's inheritance (#1575): `GetOrCreateIn` passes `Pool.mintSettings`, `Revive` the zero value, so each caller states its choice where the contract justifying it already lives. Reading `mintSettings` *inside* `materialise` would be a one-line change that silently hands a revived session the bootstrap's model and effort — a third behaviour with its own security reasoning to redo. `TestPool_Revive_DoesNotInheritOperatorSettings` reddens under exactly that mutation (verified via `go test -overlay`), which is the regression nothing previously caught: the revive contract had no test pinning model, effort, or bypass at all.

**`spawnDir` is opaque here too.** Same contract as `CreateIn` / `GetOrCreateIn`: used verbatim, never validated, canonicalised, or trust-checked pool-side; empty falls back to `tpl.WorkDir`. The caller supplies a pre-resolved `$HOME`-confined realpath. SECURITY: it is a phone-influenced workspace path, so `Revive` must not log it. The consumer — `sessionRouter.revive` in `cmd/pyry` — re-runs `resolveSpawnDir` at revive time rather than trusting the recorded value, because a path valid at mint time can become an escape before the restart; see [conversation-session-binding.md § Restart scope](conversation-session-binding.md#edge-cases--limitations).

**Tests** (`pool_revive_test.go`): registers-without-spawning (Lookup pointer identity, `ChildPID == 0`, on-disk `lifecycle_state: "evicted"`), activates-normally (the child does come up), take-path-returns-existing, `spawnDir` threading both legs, `ErrPoolNotRunning` with a **byte-level** registry-unchanged assertion (the rollback re-persists), and `ErrInvalidSessionID`.

### Transition observer (#659)

The injectable, in-process signal a `cmd/pyry`-side consumer (#657) wires to map
session boundaries onto the v2 `session_transition` wire event — **without**
`internal/sessions` importing `internal/protocol` / `internal/relay` (the import
cycle that forces the producer to the `cmd/pyry` boundary). All the machinery
lives in `transition.go`.

```go
type TransitionReason string

const (
    ReasonClear    TransitionReason = "clear"    // /clear rotation: id changed in place
    ReasonEviction TransitionReason = "eviction" // idle OR cap; no successor id
)

type SessionTransition struct {
    PreviousID SessionID
    NewID      SessionID // empty for eviction (no successor)
    Reason     TransitionReason
    OccurredAt time.Time // stamped by internal/sessions at fire
}

type TransitionObserver func(SessionTransition)

func (p *Pool) SetTransitionObserver(obs TransitionObserver)
```

**Package-local reason vocabulary, not the wire's.** `TransitionReason` is a
`string` type owned by this package; #657 maps it onto the wire
`{clear, idle_evict, workspace_change}`. Mirrors the standing "refusal-to-wire-code
mapping is the consumer's job, not the primitive's" convention — the cycle-free
boundary stays at `internal/sessions`.

**Func type, not interface.** Matches the package's closure-injection precedent
(`rotation.Config.OnRotate`, `supervisor.Config.ValidateConversation`).

**Post-construction setter, set-once-before-`Run`.** The pool is built via
`sessions.New` at `cmd/pyry/main.go:460`; the consumer/emitter (#657) comes up
later (`startRelay`), so the observer cannot be a `Config` field. `SetTransitionObserver`
writes `Pool.transitionObserver`; the field is then **read-only**, read lock-free
by the lifecycle + watcher goroutines (both spawned by `Run`) via `Run`'s
goroutine-start happens-before edge — the same "read-only after New" convention as
`convReg` / `activeCap`. A set-after-`Run` call is a programming error the race
detector flags. `nil` (the zero value) disables signalling.

**Two fire sites, both off-lock and post-persist** (the `#41`/`#155`/`#169`
lock-order + pre-persist-exposure lessons):

- **Clear** — `Pool.onRotate(old, new)` (the `Pool.Run` `OnRotate` closure routes
  through it instead of calling `RotateID` directly): on `RotateID` success, fire
  `ReasonClear` with old/new ids; the `RotateID` error is returned verbatim and a
  failed/no-op rotation fires nothing. Fires after `Pool.mu` is released.
  Startup reconciliation (`reconcile.go`) calls `RotateID` **directly**, so no
  spurious clear fires at boot before an observer is wired — the only production
  clear paths are the live fsnotify watcher and, since #1125, `RotateForNewSession`
  (the `new_session` control verb's daemon-driven direct rotation) firing the same
  `ReasonClear` off-`Pool.mu`, no new `TransitionReason`.
- **Eviction** — `Session.runActive` now returns `(TransitionReason, error)`;
  `Session.Run` fires `ReasonEviction` (empty `NewID`) **after** `transitionTo(stateEvicted)`
  returns (post-persist, no `lcMu` held), behind a `reason != "" && s.pool != nil`
  guard. The idle (`<-timerCh`, `attached==0`) and cap (`<-s.evictCh`) paths both
  return `ReasonEviction`; the defensive spontaneous-exit (`<-runErr`) and
  shutdown (`<-ctx.Done()`) paths return `""` / `ctx.Err()` and fire nothing — the
  wire has no "crashed"/shutdown reason.

`Pool.notifyTransition` (unexported) is the nil-guarded leaf callback both sites
call; it takes no lock and the observer runs with no `Pool.mu`/`Session.lcMu`/`capMu`
held. **Idle and cap collapse to one `ReasonEviction`** (evidence-based — #656's
wire has no separate cap reason); the `string` type leaves room for a future
`ReasonCapEviction` with zero signature churn.

**Synchronous, no new goroutine.** Fires run on the goroutine that already owns
the transition (lifecycle for eviction, rotation-watcher for clear) — a per-fire
goroutine would add goroutines to paths that deliberately have none and could
reorder signals. The non-blocking burden is therefore the observer's: the
`TransitionObserver` contract documents "MUST NOT block — hand off to a buffered
channel"; #657 owns the non-blocking impl. See [codebase/659.md](../codebase/659.md).

## Concurrency

`sync.RWMutex` on `Pool.sessions`:

- `Lookup` and `Default` take the read lock.
- `Run` takes the read lock once briefly to grab the bootstrap pointer and `claudeSessionsDir`.
- Writers: `RotateID` (1.2b-A), `RotateForNewSession` (#1125; re-key via the shared `rekeyLocked` + skip-set register + save, one `Pool.mu` critical section), `RotateBootstrapForSelfHeal` (#1165; same shared `rekeyLocked` + save, but deliberately NO skip-set register — see [ADR 033](../decisions/033-supervisor-self-heal-dedicated-rotation-no-skip-set.md)), `RegisterAllocatedUUID` / `IsAllocated` mutations (1.2b-B), `persist` (1.2c-A; called from `Session.transitionTo`). Phase 1.1's `Pool.Add(SessionConfig)` plugs in the same way.

`sync.Mutex` on each `Session.lcMu` (1.2c-A): protects `lcState`, `attached`, `activeCh`, `lastActiveAt`, and (as of #866) `id`. **Lock order: `Pool.mu → Session.lcMu`**. `Session.transitionTo` releases `lcMu` *before* calling `Pool.persist` so `saveLocked`'s per-session re-acquire can't deadlock.

**`id` is a two-lock field (#866).** `RotateID` writes `sess.id = newID` under **both** `Pool.mu` (W, via its function-level `defer`) and `lcMu` — the write sits inside the same brief `lcMu` section `RotateID` already opens for `lastActiveAt`, so no new lock and no lock-order change. A read is race-clean while holding *either* lock: `Pool.mu`-holders (`List`, `ResolveID`, `Snapshot`, `saveLocked`) read `sess.id` directly; lifecycle-goroutine readers, which hold neither `Pool.mu` nor (before the read) `lcMu`, go through the unexported `(*Session).currentID()` helper (`sess.lcMu.Lock/Unlock`, return `id`). `Session.ID()` also routes through `currentID()` for the same reason, though it has zero production callers today. This replaced a stale invariant ("`RotateID` mutates `id` without `lcMu`; today's only callers run before any lifecycle goroutine begins observing it") that broke once #839 wired `RotateID` into the live fsnotify `/clear` watcher — that watcher goroutine runs concurrently with the per-session lifecycle goroutines and fires on every `/clear`, so the two lifecycle-goroutine reads of `sess.id` (the eviction-transition notify and the idle-eviction warn log) were a live, if latent, data race. See [codebase/866.md](../codebase/866.md).

**Known residual gap (non-blocking, #866 code review).** `Pool.Activate`'s LRU-eviction path reads `sess.id`/`victim.id` as call arguments to `pickLRUVictim` while holding only `capMu` — `Pool.mu` is acquired *inside* `pickLRUVictim`, after Go has already evaluated those arguments, so this specific read is technically unguarded by either lock. It is pre-existing (unchanged by #866), and nil-impact today (the torn value is only used to exclude an already-known-inactive target from victim candidates), but it means "every `Pool.mu`-holder reads `id` race-clean" is not quite exception-free. Tracked as a follow-up, not yet filed as its own ticket.

Goroutines introduced in this layer (1.2c-A):

1. **Per-Session lifecycle goroutine** — body of `Session.Run`, owns the `active ↔ evicted` state machine and idle timer.
2. **Per-active-period supervisor goroutine** — wraps `s.sup.Run(subCtx)` and pipes the result to `runErr`.
3. **Per-attach detach-watcher** — decrements `attached` when the bridge's done channel fires.

The PTY spawn / wait / backoff loop, the I/O bridge goroutines, and the SIGWINCH watcher all remain in their existing packages.

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

## Testing

Three test files mirror the production layout. Stdlib `testing` only.

- **`id_test.go`** — format regex match, 1000-iteration uniqueness smoke test for `crypto/rand` wiring.
- **`pool_test.go`** — bootstrap installation, `Lookup("")` ↔ `Default()` identity, lookup by ID, unknown-ID sentinel match. Uses `/bin/sleep` as the "claude" binary; tests never call `Run`, so it's never spawned.
- **`pool_create_test.go`** (1.1a-A2) — `HappyPath` (UUID + entry shape after `Run` is live); `BootstrapUnchanged` (`Default()` returns the same `*Session` pointer pre/post `Create`); `LabelRoundTrip` (empty + non-empty labels round-trip via JSON unmarshal, not string match); `CapPassthrough_EvictsLRU` (`ActiveCap=1` evicts the bootstrap when `Create` activates); `SuperviseFails_EntryOnDisk` (no `Run` ⇒ `ErrPoolNotRunning`, valid id, entry on disk, ChildPID=0); `PersistFails_NoEntry_NoSpawn` (`registryPath` set to a non-directory path ⇒ empty id, no entry, only bootstrap in `Snapshot`).
- **`pool_list_test.go`** (1.1b-A) — `BootstrapOnly` (single entry, `Bootstrap=true`, `Label="bootstrap"`, on-disk `label` re-read from `sessions.json` is still empty); `OrderingByLastActive` (mutate three sessions' `lastActiveAt` under `lcMu` directly to `t0` / `t0+1m` / `t0+2m`, assert desc order; add a fourth equal-time entry, assert id-asc tiebreak is stable across two `List` calls); `BootstrapLabelPassthrough` (warm-start from a `sessions.json` whose bootstrap entry has `label: "main"` — synthetic substitution does NOT clobber); `RaceClean` (N goroutines × 100 `List` calls plus a mutator goroutine, `-race`-clean).
- **`pool_rename_test.go`** (1.1c-A) — `RoundTrip` (rename bootstrap to `"main"`; assert in-memory via `List`, on-disk by re-reading `sessions.json`); `EmptyClears` (rename to `"foo"` then to `""`; assert on-disk label is empty AND `List[0].Label == "bootstrap"` synthetic substitution resumes); `UnknownID` (zero-UUID returns `ErrSessionNotFound`, `bytes.Equal(before, after)` for the on-disk file, `List` snapshot deep-equal); `RaceWithList` (concurrent `Rename` + `List` goroutines under `-race`); `BootstrapPersistsAndShows` (rename bootstrap to `"primary"`; assert on-disk `Bootstrap=true` AND `Label="primary"`, `List[0].Label == "primary"` — no synthetic substitution).
- **`pool_resolve_id_test.go`** (1.1e-A) — `EmptyReturnsBootstrap`; `FullUUID`; `UniquePrefix` (1/4/8/16/35-char prefixes against a single-bootstrap pool); `FullUUIDBeatsPrefix` (synthetic two-session pool built via in-package `pool.sessions[id] = &Session{…}` writes; passes a full id whose prefix would also match a sibling and asserts the exact match wins); `AmbiguousPrefix` (synthetic two-session pool sharing a prefix; asserts `errors.Is(err, ErrAmbiguousSessionID)` plus the exact sorted match-list substring including the `"bootstrap"` substitution); `NoMatch` (zero-UUID + clearly-non-prefix `"zzzz"` both return `ErrSessionNotFound`); `RaceWithList` (concurrent `ResolveID` + `List` goroutines under `-race`).
- **`pool_remove_test.go`** (1.1d-A1) — `HappyPath` (Create + Remove a non-bootstrap session: assert child PID gone, `Lookup` returns `ErrSessionNotFound`, registry on disk has bootstrap only, stub JSONL byte-identical); `Bootstrap_Rejected` (Remove bootstrap returns `ErrCannotRemoveBootstrap`; registry bytes/mtime + `List` snapshot + JSONL byte-identical); `UnknownID` (zero-UUID returns `ErrSessionNotFound`; same byte-identity assertions); `RaceWithList` (concurrent Create+Remove writers and `List` readers under `-race`); `TerminatesUncooperativeChild` (`/bin/sh -c 'trap "" TERM INT HUP; exec sleep 86400'` as the fake claude — SIGKILL via `exec.CommandContext` cancel terminates it inside a 10s budget, no real-time `time.Sleep` in the assertion).
- **`session_test.go`** — `State` delegation, `Attach` with no bridge, `Attach` busy via `io.Pipe` (first attach blocks on input, second races and gets `supervisor.ErrBridgeBusy`), `Run` ctx-cancel via a real `/bin/sleep 3600` child.

### Why no `TestHelperProcess` re-exec helper

The parent spec considered duplicating `internal/supervisor`'s `TestHelperProcess` re-exec pattern into the sessions package (~20 lines) per the project's "duplicate, don't export test surface" convention. The blocker: `supervisor.Config.helperEnv` is unexported and is the only way to pass test-only env to the spawned child without polluting the parent test process's `os.Environ()`. External packages cannot set it.

The chosen workaround is to use a real benign binary (`/bin/sleep`) as the fake claude. No re-exec, no env injection, no helper duplication. The supervisor spawns it, ctx cancellation kills it, `supervisor.Run` returns `ctx.Err()` — which is the only contract the test asserts.

`/bin/sleep` exists on both Linux and macOS; CI runs both. If a future CI environment lacks it, `t.Skipf` on `exec.LookPath` failure rather than silently passing.

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
