# `Pool`

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

`Run` wraps the bootstrap session under `errgroup.WithContext`, plus the conversations auto-archive sweep loop when `Config.ConversationsRegistry` is set. Until #2137 it also ran an fsnotify rotation watcher over `ClaudeSessionsDir`, which guessed which session had rotated by asking the OS which transcript each tracked pid held open; claude announces the fact directly now (`conversation_reset`, #2134-2136), so that goroutine and its guess are gone — `AdoptAnnouncedID` carries the rotation instead (see *Concurrency* below). The 1.2b-B errgroup wrap is the extension point Phase 1.1's N-session fan-out reuses by adding one `g.Go(sess.Run)` per pool entry. As of 1.1a-A1 (#72), `Run` exposes the live group on `*Pool` (see *Supervisor handle* below) so post-`Run` code paths can join the same supervised set; the bootstrap itself is now scheduled through that seam.

`Activate(ctx, id)` (1.2c-A) is a thin wrapper that resolves `id` and calls `Session.Activate`. Symmetry with the rest of the surface; future routers get a single entry point. Returns `ErrSessionNotFound` for unknown ids.

`Create(ctx, label)` (1.1a-A2) is the user-facing seam for minting a new session. See *Pool.Create* below for the full sequence and failure modes.

`RotateID` (1.2b-A) atomically swaps the in-memory entry keyed by `oldID` with one keyed by `newID`, updates the bootstrap pointer if `oldID` was the bootstrap, bumps `last_active_at`, and persists. `p.mu` (write) is held across the entire operation, and `sess.id = newID` is written inside the same brief `sess.lcMu` section as `lastActiveAt` (#866) — `sess.id` is a two-lock field, written under both `Pool.mu` and `lcMu`, read race-clean while holding either. `RotateID(x, x)` is a no-op; unknown `oldID` returns `ErrSessionNotFound`. It has had **no production caller since #2137** retired the rotation watcher, its last one — kept because it's exported and ~40 test references across five files drive it; removing it is a separate deliberate call. See [jsonl-reconciliation.md](jsonl-reconciliation.md) and [rotation-watcher.md](rotation-watcher.md) (retired) for the history, *Concurrency* below, and [codebase/866.md](../codebase/866.md) for the full two-lock rationale.

`RotateForNewSession(oldID)` (#1125, `security-sensitive`) is the **daemon-driven** analog of [`AdoptAnnouncedID`](sessions-package-key-types-adoptannouncedid.md): it re-keys the pool entry, rebinds the owning conversation, and fires a `ReasonClear` transition so the client sees the fresh-session break, but it **mints** the id and drives the rotation itself for the `new_session` control verb's direct stream-json path (#1125 routing, consuming `(*streamsup.Runner).RestartFresh`, #1124), rather than following a reset claude announces. It mints a fresh id via `NewID()` (returning the error verbatim, no mutation, on a `crypto/rand` failure), then under `Pool.mu` (write): `ErrSessionNotFound` if `oldID` is absent (a TOCTOU guard — the binding may vanish between the caller's resolve and this call); the re-key itself is a call to `rekeyLocked(oldID, newID)`, the unexported helper shared by all four re-key paths (stamp `sess.id`/`lastActiveAt` under `lcMu`, move the map entry, flip `p.bootstrap` if `oldID` was it). `saveLocked()` follows, with a failure Warn-logged and swallowed (in-memory state is already authoritative — same best-effort-durability posture as `RotateID` and `rebindConversation`). Off-lock, it fires `notifyTransition(SessionTransition{PreviousID: oldID, NewID: newID, Reason: ReasonClear})` — reusing the existing `ReasonClear` wire value rather than adding a new one, since from the client's view a direct `new_session` and a `/clear` are the same observable event (a fresh session started). **It used to differ on a second axis too**: it primed a freshly-allocated skip-set before its caller (`cmd/pyry`'s `startFreshRunner`) spawned `claude --session-id <newID>`, so the (then-live) rotation watcher's CREATE handler would recognize the daemon's own spawn rather than double-rotating it as a self-rotation. #2137 retired the watcher and deleted the skip-set entirely, so the mint-and-drive above is the whole of the difference from `AdoptAnnouncedID` now. See [codebase/1125.md](../codebase/1125.md).

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
`RotateForNewSession`, it fires no `notifyTransition` (`ReasonClear`) client
signal** — a self-heal is a supervisor-internal crash-recovery action, not a
user `/clear`, so firing `ReasonClear` would misrepresent it to clients. See
[ADR 033](../decisions/033-supervisor-self-heal-dedicated-rotation-no-skip-set.md)
for the original rationale; its other omission (no freshly-allocated-skip-set
entry) no longer distinguishes it from `RotateForNewSession`, since #2137
deleted the skip-set from both. The rotated id needs no push into the spawn
path — the pre-existing `ResolveSessionID` (#839) pull resolves it on the
very next spawn automatically. See [codebase/1165.md](../codebase/1165.md).

### `Config` / `SessionConfig`

```go
type Config struct {
    Bootstrap         SessionConfig
    Logger            *slog.Logger
    RegistryPath      string        // sessions.json path; "" disables persistence (test-only)
    ClaudeSessionsDir string        // claude's <uuid>.jsonl dir; "" disables startup
                                    // reconciliation (test default / unresolvable $HOME
                                    // fallback). Until #2137 also disabled the now-retired
                                    // rotation watcher; Pool.removeJSONL's archive/purge
                                    // path is why the field survives regardless.
    IdleTimeout       time.Duration // default per-session eviction window; 0 disables
    TurnBusy          func(SessionID) bool // #1486; nil = no deferral signal, idle timer evicts on fire
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
