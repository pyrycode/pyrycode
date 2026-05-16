# System Overview

Pyrycode is a process supervisor that keeps a Claude Code session alive across crashes and reboots. Phase 0 is a single-session supervisor; later phases add multi-session routing, Channels integration, and remote access.

## Module Structure

```
pyrycode/
├── cmd/pyry/                  Binary entry point
│   └── main.go                CLI parsing, signal setup, supervisor init
├── internal/supervisor/       Core process supervision
│   ├── supervisor.go          Supervisor type: PTY spawn, I/O bridge, restart loop
│   ├── backoff.go             Backoff timer: exponential delay with stability reset
│   └── winsize.go             SIGWINCH → PTY size sync
├── internal/sessions/         Session-addressable runtime (Phase 1.0+)
│   ├── id.go                  SessionID + UUIDv4 NewID() via crypto/rand + ValidID() canonical-shape validator
│   ├── session.go             Session: wraps one supervisor + optional bridge; lifecycle goroutine (active↔evicted state machine, idle timer); Activate / Run / Attach with attach bookkeeping
│   ├── pool.go                Pool: in-memory registry, Config (RegistryPath + ClaudeSessionsDir + IdleTimeout + ActiveCap), load-or-mint bootstrap on New, RotateID seam, saveLocked + persist, errgroup Run with supervise() fan-out seam, allocated-UUID skip set (registerAllocatedUUIDLocked variant), buildSession helper shared with GetOrCreate, Snapshot, Activate (cap-aware), capMu
│   ├── get_or_create.go       Pool.GetOrCreate take-or-create primitive (1.3b): caller-supplied UUIDv4, atomic register+persist+skip-set+g.Go(sess.Run) under p.mu; ErrInvalidSessionID
│   ├── registry.go            On-disk schema (registryFile, registryEntry); loadRegistry, saveRegistryLocked (atomic temp+rename), pickBootstrap, sortEntriesByCreatedAt
│   ├── reconcile.go           Startup JSONL scan: encodeWorkdir, mostRecentJSONL, reconcileBootstrapOnNew, DefaultClaudeSessionsDir
│   └── rotation/              Live /clear watcher (Phase 1.2b-B)
│       ├── watcher.go         fsnotify lifecycle, event loop, probe orchestration with bounded retry
│       ├── probe.go           Probe interface, parseProcFD, parseLsofOutput, noopProbe fallback
│       ├── probe_linux.go     //go:build linux  — /proc/<pid>/fd walk
│       └── probe_darwin.go    //go:build darwin — lsof -nP -p <pid> -F fn shell-out
├── internal/config/           User-configurable values (Phase 3 foundation)
│   ├── config.go              Config struct, DefaultConfig, Load (overlay-decode over defaults)
│   └── config_test.go         Same-package, table-driven
├── internal/identity/         Typed routing identifiers (Phase 3 foundation)
│   ├── server_id.go           ServerID newtype, NewServerID (crypto/rand + UUIDv4 version/variant), ParseServerID (canonical validation), ErrInvalidServerID sentinel
│   └── server_id_test.go      Same-package, table-driven; format/uniqueness/parse/round-trip
├── internal/keys/             Binary-side X25519 static keypair for Mobile Protocol v2 / Noise_IK (Phase 3 foundation, #438)
│   ├── static_key.go          StaticKey type (unexported priv/pub [32]byte) + by-value PrivateKey()/PublicKey() accessors, validDaemonName allowlist ([a-z0-9_-], len 1..64, no leading '-'), newStaticKey generator (crypto/ecdh.X25519().GenerateKey, panic-on-rng-fail), ErrInvalidDaemonName + ErrCorruptKeyFile sentinels
│   ├── static_key_test.go     Same-package (white-box); allowlist matrix + generator non-zero/uniqueness + accessor by-value contract
│   ├── store.go               LoadOrCreate(baseDir, daemonName) — package owns (baseDir, daemonName) → path mapping; on-disk JSON schema {version, algorithm, private_key base64.StdEncoding, public_key base64.StdEncoding, created_at RFC3339 UTC} locked to docs/protocol-mobile.md § Static keys — binary side; atomic-write recipe (MkdirAll 0o700 → CreateTemp → Chmod 0o600 BEFORE write → Sync → Close → Rename); seven-step load-side validation with public/private consistency check via crypto/subtle.ConstantTimeCompare; existing files never overwritten on load path; three-way ReadFile switch preserves I/O-vs-corruption sentinel distinction. **Filesystem hardening (parent-dir mode rejection, post-MkdirAll re-stat, existing-file mode rejection, O_NOFOLLOW) deferred to #439.**
│   └── store_test.go          Same-package; fresh-create + mode assertions + round-trip + corrupt-JSON sentinel matrix + no-mutation-on-load + no-private-key-leak in error string + I/O-error-is-not-corruption
├── internal/devices/          Paired-device type + token hashing (Phase 3 foundation)
│   ├── device.go              Device struct (TokenHash/Name/PairedAt/LastSeenAt), HashToken (SHA-256 hex), VerifyToken (crypto/subtle.ConstantTimeCompare)
│   ├── device_test.go         Same-package, table-driven; determinism + verify true/false/empty/malformed
│   ├── registry.go            Registry: mutex-guarded device list + Load / Save (atomic temp+rename + 0o600/0o700) / Add / Remove / List / FindByTokenHash
│   └── registry_test.go       Same-package, table-driven; round-trip + atomic-rename + concurrent-readwrite race probe
├── internal/conversations/    Conversation entity + on-disk registry (Phase 3 foundation)
│   ├── conversation.go        Conversation struct + ConversationID typedef
│   ├── conversation_test.go   Same-package, table-driven; round-trip + omitempty
│   ├── id.go                  NewID (crypto/rand + UUIDv4 version/variant), ValidID (canonical-shape predicate)
│   ├── id_test.go             Format / uniqueness / validity table
│   ├── registry.go            Registry: mutex-guarded conversation list + Load / Save (atomic temp+rename + 0o600/0o700, snapshot-then-write) / Create / Get / List(filter) / Update(id, fn); ListFilter (IsPromoted *bool)
│   └── registry_test.go       Same-package; round-trip + atomic-rename + ordering + concurrent-readwrite race probe + List filter + Update hit/miss/pointer-stability
├── internal/pair/             QR pairing payload encode/decode + render (Phase 3 foundation)
│   ├── payload.go             Payload struct, Encode (JSON → base64url no-pad), Decode (5-stage rejection), ErrInvalidPayload sentinel
│   ├── payload_test.go        Same-package, table-driven; round-trip + format + stable-field-order + decode rejection table
│   ├── render.go              Render(p, w): qrterminal.GenerateHalfBlock at level M + blank line + Encode(p) + instruction line; errTrackingWriter wraps the qrterminal no-error-return API
│   └── render_test.go         Same-package; format/field-order/determinism/writer-error/no-panic-on-broken-writer
├── internal/noise/            flynn/noise IK wrapper for handshake + AEAD transport (Phase 3 foundation, #433)
│   ├── noise.go               Cipher-suite pin (Noise_IK_25519_ChaChaPoly_BLAKE2s, single source-location var); KeyLen=32 const + ErrInvalidKeyLength sentinel; Responder + Initiator (state-machine wrappers around flynn's HandshakeState — no re-export of flynn types) with NewResponder(staticPriv)/ReadInit/WriteResp on the binary side and NewInitiator(staticPriv, peerStaticPub)/WriteInit/ReadResp on the phone side; CipherState wrapper with Encrypt(plaintext)/Decrypt(ciphertext) — **no AD parameter** (empty-AD invariant from docs/protocol-mobile.md:197 enforced at the type system); public-key derivation via crypto/ecdh (not flynn) for byte-for-byte compat with internal/keys.StaticKey.PrivateKey(); responder-side cs1/cs2 swap so both sides expose symmetric (send, recv) — pinned structurally by TestRoundTrip_BothDirections
│   └── noise_test.go          Same-package; 11 tests covering full handshake round-trip + both-directions transport + 32-frame loop + tampered/truncated message rejection + wrong-responder-static rejection + bad-key-length matrices + out-of-order/replayed frame rejection + error-message no-leak hygiene
├── internal/transport/        Long-lived WSS client to the relay (Phase 3, #247 + extensions in #248)
│   ├── wssclient.go           Client, Config (URL/Headers/WriteTimeout/Logger/FatalCloseCodes), New, Connect (blocking lifecycle, returns ErrFatalClose for FatalCloseCodes hit), Send, Receive (returns ErrDisconnected on conn drop), Connected (signal channel for relay handshake layer), DropConn (force-close via CloseNow), Close; per-conn three-pump fan-out (recvPump/sendPump/pingLoop), backoff with ±20% jitter, stability-reset
│   └── wssclient_test.go      Same-package; backoff/ping/pong/dial/fatal-close/connected/disconnected/dropconn behaviour against httptest-hosted coder/websocket relay
├── internal/relay/            Binary↔relay handshake + per-phone-conn auth layer (Phase 3, #248 + #249 + #307)
│   ├── connection.go          Connection, Config (ServerID/RelayURL/BinaryVersion/Logger), Connect (sync-validate / async-run), Frames, Send (#307; JSON-marshal RoutingEnvelope + transport.Client.Send), Wait, Close; ErrServerIDConflict (fatal 4409) / ErrInvalidConfig sentinels; runs one-shot hello/hello_ack handshake on every Connected() signal; FatalCloseCodes{4409} halts the dial loop; structured-slog field discipline
│   ├── connection_test.go     Same-package; happy-path / ack-timeout / unexpected-frame / 4409-fatal / transport-drop-during-handshake / re-handshake-post-reconnect / Frames-in-order / Close + ctx-cancel / config-validation table against httptest-hosted test relay; connectWithClient test seam bypasses wss:// check
│   ├── auth.go                AuthenticateFirstFrame(env, token, reg, serverID, logger) pure per-conn token-validation predicate; AuthOutcome{Response, CloseConn}; StatusUnauthorized=4401 / MsgInvalidToken const / ErrMalformedHelloFrame sentinel; composes devices.Registry.Validate (#210) with protocol.HelloAckPayload / ErrorPayload (#271); carrier-agnostic (relay-conn ticket picks wire mechanism); revoked-vs-invalid is one code in v1 (CodeAuthTokenRevoked unused pending tombstone primitive); no device name on reject (anti-enumeration)
│   └── auth_test.go           Same-package; ValidToken (hello_ack + LastSeenAt bump) / UnknownToken / RevokedTokenSameUX (Remove-then-Validate, spec line 535) / EmptyToken / MalformedHelloFrame (ErrMalformedHelloFrame + zero outcome) / StatusUnauthorized_Value
├── internal/dispatch/         Per-phone-conn demultiplexer + handler-table seam (Phase 3, #307, extended #308)
│   ├── dispatch.go            Dispatcher, Config (Frames/OutboundBuffer/Logger/FirstFrame), Handler, Conn (ConnID/NextID atomic.Uint64/Send/Reply), FirstFrameGate + FirstFrameOutcome (Response/CloseConn/Code/Err), New/Register/Run/Outbound; one demux goroutine + N per-conn goroutines (size-8 input, shared bounded outbound, default 32); handleOne maps malformed→protocol.malformed, encrypted→protocol.unsupported, unknown-type→protocol.unknown_type, no-handler→protocol.unsupported; Register-before-Run enforced via atomic.Bool; carrier-agnostic (imports internal/protocol only); #308 adds first-frame gate (runs once per new conn_id on the per-conn goroutine; accept advances NextID past gate's hello_ack id=1; reject publishes one envelope with Frame=<error>+CloseCode=4401 and exits the per-conn goroutine; gate-Err emits protocol.malformed and consumes gate-status); closed-conn drop in routeConn under d.mu so demux can't block on a send into a dead goroutine; CloseCode on inbound ignored (binary→relay-only wire field)
│   ├── dispatch_test.go       Same-package; stdlib only; -race-clean: empty-table-unsupported / unknown-type / encrypted-refusal / malformed-frame / id-counter-monotonic-per-conn / reply-in-reply-to / ctx-cancel-teardown / frames-close-teardown / two-conns-arrival-order / register-duplicate-panics / register-after-run-panics / new-nil-{frames,logger}-panics
│   └── gate_test.go           #308; FirstFrameGate_Accept (gate runs once; subsequent frames bypass) / Reject (one envelope with Frame+CloseCode=4401; further frames dropped) / RejectDoesNotAffectOtherConns (per-conn isolation) / Err (malformed-fall-through, gate consumed) / NilDisablesGate (pre-#308 byte-stable) / IgnoresInboundCloseCode (binary→relay-only invariant) / ConcurrentConns (10 conns under -race)
├── internal/protocol/         Mobile WS wire-format types (Phase 3 foundation, #255)
│   ├── envelope.go            Envelope, RoutingEnvelope (+ #308 optional omitempty fields: Token string phone→binary-first-frame-only / CloseCode uint16 binary→relay-only), ErrUnknownType / ErrUnsupported sentinels, IsV1Compatible (encrypted-wins-over-unknown check order), v1TypeSet map literal
│   ├── codes.go               12 Code* error-string constants + 16 Type* envelope-type-string constants (grouped by spec table order)
│   ├── envelope_test.go       Golden round-trip vs. testdata/envelope_full.json, envelope_minimal.json, routing_envelope.json (canonical json.Compact compare; time.Time.Equal for TS)
│   ├── compat_test.go         Truth-table for IsV1Compatible + drift detectors (v1TypeSet covers all Type* constants; Code* match spec dotted strings)
│   └── testdata/              envelope_full.json (every field), envelope_minimal.json (omitempty branches), routing_envelope.json (relay splice)
├── internal/control/          Control-plane server (Unix socket, JSON)
│   ├── server.go              Server, SessionResolver / Session interfaces, verb dispatch
│   ├── attach.go              Attach handoff to supervisor bridge
│   └── logs.go                Ring-buffer log streaming
├── internal/e2e/              End-to-end test harness (//go:build e2e || e2e_install)
│   ├── harness.go             Harness, Start(t), pyry build helper, readiness poll, teardown
│   ├── harness_test.go        Smoke + failure-injection (re-exec + processAlive)
│   └── install_linux_test.go  //go:build linux && e2e_install — systemd round-trip, PATH inheritance, cleanup-on-fatal (#80)
├── systemd/pyry.service       Linux systemd user unit
└── launchd/dev.pyrycode.pyry.plist   macOS launchd plist
```

Dependency direction: `cmd/pyry → internal/sessions → internal/supervisor`, with `internal/control` importing `internal/sessions` for the `SessionID` type referenced by its `SessionResolver` interface. `internal/sessions/rotation` is downstream of `internal/sessions` (no back-edge — the contract is closures over primitive types so the rotation package never imports its host). `internal/supervisor` has no upward imports — verifiable with `go list -deps ./internal/supervisor/...`.

## Data Flow

### Interactive Session

```
User terminal
    │
    ├── stdin ──────> pyry ──> PTY master fd ──> claude (child process)
    │
    └── stdout <───── pyry <── PTY master fd <── claude (child process)
```

The supervisor puts the controlling terminal into raw mode so keystrokes pass through unmodified. SIGWINCH signals are forwarded to the PTY so terminal resizes propagate to the child.

### Restart Cycle

```
supervisor.Run()
    │
    ├── runOnce() ──> spawn claude in PTY, bridge I/O
    │                 wait for child exit
    │
    ├── child exited? ──> apply backoff delay
    │                     if uptime > resetAfter: reset backoff to initial
    │                     respawn with --continue (after first run, when ResumeLast is true)
    │
    └── ctx cancelled? ──> graceful shutdown
```

### Backoff Strategy

- Initial delay: 500ms
- Doubles on each restart: 500ms → 1s → 2s → 4s → ... → 30s (max)
- Resets to initial if the child stayed up longer than 60s (stability indicator)
- Context cancellation (SIGINT/SIGTERM) breaks out of the backoff wait

## Key Types

### `supervisor.Config`

All supervisor configuration in a single struct. Passed to `supervisor.New()`.

Fields: ClaudeBin (path to claude), WorkDir (child's cwd), ResumeLast (use --continue after first run), ClaudeArgs (pass-through args), Bridge (optional service-mode I/O mediator), Logger (*slog.Logger), backoff params (Initial, Max, Reset durations), ValidateConversation (optional `func(id string) error` consulted by `WriteUserTurn` before mutation; production wiring in `internal/sessions/pool.go` synthesises a closure over `conversations.Registry.Get` that returns `conversations.ErrConversationNotFound` on miss — `internal/supervisor` never imports `internal/conversations`, the sentinel travels through the closure verbatim; nil skips validation, #312).

### `supervisor.Bridge`

Service-mode I/O mediator. A single `Bridge` instance persists across child restarts; the supervisor brackets each `runOnce` iteration with `Bridge.BeginIteration()` / `Bridge.EndIteration()` so the input pump goroutine terminates cleanly per iteration instead of leaking and racing the next one for queued attach-client bytes. Input path: `chan []byte` + per-iteration cancel (`Bridge.Read` selects between an incoming chunk and EOF on iteration end — Go's `select` non-determinism preserves any in-flight chunk for the next iteration). Output path: forward to attached writer or discard; `Write` never returns an error so the PTY-drain goroutine cannot wedge mid-disconnect. Resize seam: `SetPTY(*os.File)` registers the per-iteration PTY master under a leaf-only `ptyMu`; `Resize(rows, cols uint16)` calls `pty.Setsize` (silently no-ops between iterations). `runOnce` clears the registration with `SetPTY(nil)` **before** `EndIteration` so a racing `Resize` sees nil rather than a closed fd. See [ADR 007](../decisions/007-bridge-iteration-boundaries.md), [ADR 008](../decisions/008-bridge-resize-seam.md).

### `supervisor.Supervisor`

Owns the child process lifecycle. Methods:
- `New(cfg Config) (*Supervisor, error)` — validates config, applies defaults
- `Run(ctx context.Context) error` — the main loop: spawn, wait, backoff, repeat
- `WriteUserTurn(id string, payload []byte) error` (#312) — delivers a user-turn payload to the supervised child along a caller-tagged `conversation_id`; runs `Config.ValidateConversation` first (a non-nil result propagates verbatim, typically `conversations.ErrConversationNotFound`); on accept updates the cursor under `convMu` **before** writing to the PTY so concurrent `CurrentConversation()` readers never see a stale cursor; cursor is NOT mutated on validation refusal; no active child (between iterations / pre-spawn) drops the bytes silently and returns `nil` (matches `Bridge.Write`'s discard-on-unattached behaviour); PTY write failures wrap with stable `"supervisor: write user turn:"` prefix.
- `CurrentConversation() string` (#312) — snapshot read of the cursor under `convMu`; `""` when no `WriteUserTurn` has been accepted yet; persists across child restarts (in-memory supervisor state, not per-iteration). Consumed by #311's assistant-turn → `message` envelope bridge to stamp outbound envelopes.

Internal state added by #312: `convMu` + `currentConvID` (the cursor) and `ptmxMu` + `ptmx *os.File` (the per-iteration PTY master fd). Both leaf-only; never nested with each other or with the pre-existing `mu`/`state` pair. `runOnce` brackets each iteration with private `setPTY(ptmx)` / `setPTY(nil)`, mirroring `Bridge.SetPTY`'s register/clear pair — `setPTY(nil)` runs **before** the actual `ptmx.Close()` so a racing `WriteUserTurn` sees `nil` and drops rather than writing to a closed fd.

### `supervisor.backoffTimer`

Extracted backoff logic. Computes the next delay based on how long the previous child ran:
- `next(uptime time.Duration) time.Duration` — returns the delay and advances internal state
- `reset()` — returns to initial delay

## Platform Support

- **Linux:** Primary target. systemd user unit for daemon management.
- **macOS:** Supported. launchd plist provided. Cross-compile verified for darwin/amd64 and darwin/arm64.
- **Windows:** Out of scope. Would need ConPTY instead of Unix PTY, different signal handling, and a service wrapper.

## Dependencies

| Module | Purpose | Why not stdlib |
|--------|---------|----------------|
| `creack/pty` | PTY allocation and size management | No stdlib PTY support |
| `fsnotify/fsnotify` | Live `/clear` rotation detection on the claude sessions dir (Phase 1.2b-B) | Cross-platform inotify+kqueue without owning two stacks. See [ADR 004](../decisions/004-fsnotify-for-rotation-detection.md). |
| `golang.org/x/term` | Terminal raw mode, state save/restore | Extended terminal ops not in stdlib |
| `golang.org/x/sync` | `errgroup` for `Pool.Run`'s bootstrap+watcher fan-out (Phase 1.1+ extends to N sessions) | Semi-official extension; clearer than ad-hoc 2-goroutine coordination |
| `golang.org/x/sys` | System calls (indirect, via x/term and fsnotify) | — |

### Session Registry (Phase 1.2a)

```
~/.pyry/<sanitized-name>/sessions.json    (file 0600, dir 0700)
~/.pyry/<sanitized-name>.sock             (sibling — single-writer per name)
```

`Pool.New` reads the registry on startup. Missing or empty file → cold start (mint UUID, write file). Valid file → warm start (reuse persisted UUID, no rewrite). Malformed JSON → fatal at startup.

`saveLocked` writes via `os.CreateTemp` → fsync → `os.Rename` in the same directory. Rename is the commit point; partial JSON is unreachable in the target file. Called under `Pool.mu` (write) by mutating ops; in 1.2a only `Pool.New`'s cold-start path invokes it.

Forward-compat: `version` is a future hook; unknown top-level and per-session fields are silently ignored on read.

### Startup JSONL Reconciliation (Phase 1.2b-A)

```
~/.claude/projects/<encoded-cwd>/<uuid>.jsonl    (claude's own files)
```

`Pool.New` scans the per-workdir claude session dir, finds the most-recently-modified `<uuid>.jsonl`, and rotates the registry's bootstrap entry to that UUID if it disagrees. Self-heals across `/clear` (claude rotates UUIDs on `/clear`; without reconciliation, post-`pyry stop` the registry would still point at the pre-`/clear` UUID).

`encodeWorkdir` maps cwd → claude's path component by replacing both `/` and `.` with `-`. The pre-rotation JSONL is never modified — only the registry pointer moves. Missing/unreadable claude dir is logged and ignored (startup proceeds with the existing bootstrap). The mutation goes through `Pool.RotateID`, the load-bearing seam reused by Phase 1.2b-B's live-detection watcher.

### Live `/clear` Rotation Watcher (Phase 1.2b-B)

```
Pool.Run (errgroup)
    ├── bootstrap.Run(gctx)
    └── rotation.Watcher.Run(gctx)
              │
              ▼
        fsnotify CREATE on ~/.claude/projects/<encoded-cwd>/<new>.jsonl
              │
              ▼
        IsAllocated(<new>)? → consume + skip (Phase 1.1's --session-id mints)
        Snapshot()         → [{id: <old>, pid}, ...]
        probeWithRetry(pid) → /proc/<pid>/fd walk (Linux) or `lsof -F fn` (Darwin)
              │
              ▼
        match → OnRotate(<old>, <new>) → Pool.RotateID
```

`internal/sessions/rotation` is its own package, dependency-direction-respecting (no import of `internal/sessions`). The contract is `rotation.Config` closures over primitive types, wired in `Pool.Run`. Watcher disabled (and pyry startup proceeds) when `ClaudeSessionsDir` is empty, `fsnotify` init fails, or — on darwin — `lsof` is missing from PATH (`noopProbe` fallback). See [features/rotation-watcher.md](../features/rotation-watcher.md) and [ADR 004](../decisions/004-fsnotify-for-rotation-detection.md).

### Idle Eviction + Lazy Respawn (Phase 1.2c-A)

```
Session.Run (per-session lifecycle goroutine)
    │
    ├── runActive   → supervisor up, idle timer armed
    │     │
    │     ├── attached>0 on fire → re-arm (poll-with-grace)
    │     ├── attached==0 on fire → cancel inner ctx → drain runErr → evict
    │     └── outer ctx done    → cancel inner ctx → drain runErr → return
    │
    └── runEvicted  → no supervisor; wait on activateCh or ctx
              │
              ▼
    transitionTo(state) → Pool.persist → registry write
```

Each `*Session` owns a per-session lifecycle goroutine that drives an `active ↔ evicted` two-state machine. Activity = "at least one client attached" (`attached > 0`). On the idle timeout with no attaches, the supervisor's inner ctx is cancelled and claude exits cleanly — the JSONL on disk is preserved untouched. `Session.Activate(ctx)` (called by `handleAttach` before `Attach`) wakes the session and respawns the supervisor pointing at the same JSONL.

Registry gains `lifecycle_state` (`omitempty`, defaults to `"active"`). Bootstrap warm-starts in whatever state the registry says. Lock order: `Pool.mu → Session.lcMu`. CLI: `-pyry-idle-timeout` (default `0` / disabled; opt in with e.g. `15m`). See [features/idle-eviction.md](../features/idle-eviction.md) and [ADR 005](../decisions/005-idle-eviction-state-machine.md).

### E2E Harness (Phase test-infra, ticket #68)

```
internal/e2e (//go:build e2e)
    │
    ├── ensurePyryBuilt(t) ──> sync.Once go build  (or $PYRY_E2E_BIN)
    │
    └── Start(t) *Harness
          ├── HOME=t.TempDir(), -pyry-socket=<tmp>/pyry.sock,
          │   -pyry-claude=/bin/sleep -- infinity, -pyry-idle-timeout=0
          ├── cmd.Start
          ├── go { cmd.Wait; close(doneCh) }
          ├── waitForReady: os.Stat + net.Dial loop, 5s deadline,
          │                 short-circuit on doneCh
          └── t.Cleanup(SIGTERM → 3s → SIGKILL → 1s → os.Remove(socket))
```

Build-tag-isolated package; default `go test ./...` does not compile it. Invoke
with `go test -tags=e2e ./internal/e2e/...`. The supervised "claude" is
`/bin/sleep infinity` (exists on Linux + macOS, survives until SIGTERM); idle
eviction disabled so the smoke path isn't racing the timer. Failure-injection
verification re-execs the test binary (`-test.run=^TestInnerFatalChild$` +
`PYRY_E2E_INNER_FATAL_OUT` env var) so an inner `t.Fatal` runs in a fresh
process; the parent reads the state file and asserts the pid is gone (POSIX
`Signal(0)` probe) and the socket is `fs.ErrNotExist`. See
[features/e2e-harness.md](../features/e2e-harness.md).

CLI-driver wrappers (`Harness.Status()`, `Stop()`, generic `Run(args...)`),
`Option`s, the first feature-flavoured e2e, and CI wiring are deferred to the
#51 follow-up.

### Install-Service E2E (Phase test-infra, ticket #80)

```
internal/e2e/install_linux_test.go (//go:build linux && e2e_install)
    │
    ├── TestE2EInstall_RoundTrip_Linux
    │     install.Install → daemon-reload → start → waitForActive
    │     → pyry status -pyry-name=<name> → stop → waitForInactive
    │     → t.Cleanup(stop/disable/remove/daemon-reload)
    │
    ├── TestE2EInstall_PathInheritance_Linux        (no systemd needed)
    │     install.Install with EnvPath = $PATH, HomeDir = t.TempDir()
    │     → assert every entry of $PATH appears in Environment="PATH=..."
    │       with $HOME/ → %h/ substitution (bug-#19 regression guard)
    │
    └── TestE2EInstall_CleanupOnFatal_Linux         (re-exec)
          exec.Command(os.Args[0], -test.run=^TestInstallFatalChild$)
          ↓ child installs + starts + t.Fatal
          → parent: stat(unitPath) is ErrNotExist
                    is-active <name> != "active"
```

Build tag `e2e_install` is **separate** from `e2e` so default e2e CI doesn't
require a running systemd `--user` session. `harness.go`'s tag was widened to
`e2e || e2e_install` so the install tests reuse `ensurePyryBuilt` /
`childEnv`. Tests skip cleanly when `systemctl --user is-system-running`
reports `offline` / `unknown` / missing (CI runners, containers without D-Bus).
`install.Install` is called directly rather than via the CLI binary to avoid a
test-only override on `Options.Binary`. See
[features/install-e2e.md](../features/install-e2e.md).

## Future Architecture (not yet implemented)

- **Phase 1.1a-A1 (#72) — landed:** `Pool.supervise(sess)` seam + `runGroup`/`runCtx` handle on `*Pool`. Bootstrap fan-out in `Pool.Run` flows through the helper; the watcher fan-out stays inline (not a `*Session`). `ErrPoolNotRunning` sentinel for before/after-`Run` calls.
- **Phase 1.1+:** `Pool.Create(ctx, label)` (sibling A2 — consumer of the supervise seam, landed), `AttachPayload.SessionID` on the wire (1.1e-C, landed — server routes via `Pool.ResolveID`; CLI surface in 1.1e-D landed), `pyry sessions new [--name LABEL]` CLI verb + `sessions` sub-router (1.1a-B2 #76, landed — peels global pyry flags via `parseClientFlags` then dispatches on the first positional; each future verb is one switch case + one helper), `pyry sessions rm` (1.1d-B2 #99, landed — client-side prefix resolution via `control.SessionsList`), `pyry sessions rename` (1.1c-B2a #92, landed — full-UUID only), `pyry sessions list [--json]` (1.1b-B2 #88, landed — first text-table sink in `cmd/pyry`, `text/tabwriter` four-column table + `{"sessions":[...]}` JSON envelope; renderer choices template the rest of Phase 1.1's tabular output), per-session log lines. Live-resize loop landed end-to-end across #136 (`Bridge.Resize` seam) + #137 (`VerbResize` wire + `handleResize` server applier) + #133 (`startWinsizeWatcher` client-side SIGWINCH emitter in `pyry attach`).
- **Phase 1.3 (SDK consumer-shaped attach):** `pyry attach --stdio` (1.3a #154, landed — no-PTY byte forwarding); `pyry attach --create-if-missing <uuid>` (1.3b #155, landed — take-or-create attach via new `Pool.GetOrCreate` primitive + `ValidID` UUIDv4 validator; orthogonal to `--stdio`, the SDK's primary shape is `pyry attach --stdio --create-if-missing <uuid>`); foreground-binary auto-attach (1.3c #158).
- **Phase 2:** Channels — inbound event routing from Discord/Telegram
- **Phase 3 foundation (#205, landed):** `internal/config` — typed `Config` schema + `DefaultConfig` + `Load` overlay-decode loader for `~/.pyry/config.json`. First field is `RelayURL` (default `wss://relay.pyrycode.dev`, placeholder), consumed by `pyry pair` and daemon startup in their own follow-up tickets. See [features/config-package.md](../features/config-package.md), [ADR 018](../decisions/018-config-overlay-decode.md).
- **Phase 3 foundation (#206, landed):** `internal/identity` — typed `ServerID` (UUIDv4-shaped string newtype) + `NewServerID` (crypto/rand-driven generation, panic-on-rng-fail) + `ParseServerID` (canonical UUIDv4 validation, `ErrInvalidServerID` sentinel). Pure types, no I/O; persistence sibling will load/write the raw string from disk and feed it through `ParseServerID`. Server-id is the public routing identifier for one pyrycode-binary instance — surfaced in QR pairing payloads and the relay handshake's `x-pyrycode-server` upgrade header. See [features/identity-package.md](../features/identity-package.md).
- **Phase 3 foundation (#438, landed):** `internal/keys` — binary-side X25519 static keypair for Mobile Protocol v2 (Noise_IK). `StaticKey` (unexported `priv`/`pub [32]byte`) + by-value `PrivateKey() [32]byte` / `PublicKey() [32]byte` accessors; `LoadOrCreate(baseDir, daemonName string) (*StaticKey, error)` as the single entry point — package owns the full `(baseDir, daemonName) → filepath.Join(baseDir, daemonName, "static_key.json")` mapping so a caller-precomputed path cannot bypass the allowlist; `validDaemonName` (unexported) — length 1..64, every byte in `[a-z0-9_-]`, no leading `-`, rejects `.`/`..`/`/`/`\`/uppercase/whitespace/NUL/multi-byte UTF-8/oversize; reject path performs ZERO filesystem operations (pinned by `TestLoadOrCreate_InvalidDaemonName`). Generation via stdlib `crypto/ecdh.X25519().GenerateKey(rand.Reader)` (panic-on-rng-fail mirroring `identity.NewServerID`); raw 32-byte material wire-compatible bit-for-bit with `flynn/noise.DHKey`. On-disk JSON schema `{version, algorithm "Noise_25519", private_key+public_key base64.StdEncoding (padded — opposite of `internal/pair`'s `RawURLEncoding` for QR/wire), created_at RFC3339 UTC}` locked to `docs/protocol-mobile.md` § Static keys — binary side. Atomic-write recipe `MkdirAll(0o700)` → `CreateTemp` → **`Chmod(0o600)` BEFORE write** (umask defence, same as `internal/identity/store.go:77`) → `Sync` → `Close` → `Rename`. Seven-step load-side validation (all fail-fast `ErrCorruptKeyFile`): JSON syntax → version → algorithm → base64+length 32 (priv) → base64+length 32 (pub) → `!created_at.IsZero()` → public/private consistency via `crypto/subtle.ConstantTimeCompare`. **Existing files are never overwritten on the load path even on validation failure** — paired phones bind to a specific keypair, silent regeneration would invalidate every pairing; corruption is operator-escalated (mirrors `internal/identity.LoadOrCreate` invariant). Three-way `os.ReadFile` switch preserves I/O-vs-corruption sentinel distinction. Sentinels `ErrInvalidDaemonName` + `ErrCorruptKeyFile`. Error messages include path but NEVER include file contents / base64 fields / decoded bytes (pinned by `TestLoadOrCreate_CorruptJSONErrorDoesNotLeakPrivateKey`). SECURITY contract in package + `PrivateKey()` doc-comments: returned `[32]byte` MUST NOT be logged / wrapped into error / emitted on a wire; no `slog` calls in the package. Daemon-name validator deliberately NOT consolidated with `cmd/pyry/main.go:sanitizeName` (which is a transformer permitting `.`/uppercase — sharing would defeat the path-traversal defence). Fifth instance of the per-daemon JSON-file pattern + atomic-write recipe; recipe stays duplicated (architect re-evaluation at this boundary — the umask-defence variant matters too much to share). **Filesystem hardening (parent-dir mode `0700` rejection, post-`MkdirAll` re-stat, existing-file mode `0600` rejection, `O_NOFOLLOW` on read) intentionally NOT in this slice** — filed as #439, hard prerequisite for any production consumer (#432 QR payload extension, #433 Noise wrapper); production exposure window is zero because no consumer reads `static_key.json` until #439 lands. See [features/keys-package.md](../features/keys-package.md), [codebase/438.md](../codebase/438.md), [ADR 024](../decisions/024-noise-ik-mobile-e2e.md).
- **Phase 3 foundation (#433, landed):** `internal/noise` — narrow Go wrapper around `github.com/flynn/noise v1.1.0` for the `Noise_IK_25519_ChaChaPoly_BLAKE2s` cipher suite that Mobile Protocol v2 mandates. Owns the cipher-suite pin in exactly one source location (`var cipherSuite = flynnNoise.NewCipherSuite(DH25519, CipherChaChaPoly, HashBLAKE2s)`) — a future suite migration is a one-line edit, not a grep across `internal/relay`. Eleven exports: `KeyLen = 32` const + `ErrInvalidKeyLength` sentinel + `Responder` / `Initiator` / `CipherState` types + `NewResponder(staticPriv) (*Responder, error)` / `ReadInit(initMsg) (earlyData, err)` / `WriteResp(earlyData) (respMsg, send, recv, err)` (binary side) + `NewInitiator(staticPriv, peerStaticPub) (*Initiator, error)` / `WriteInit(earlyData) (initMsg, err)` / `ReadResp(respMsg) (earlyData, send, recv, err)` (phone side — production initiator is mobile-team's Kotlin/Swift impl, the Go side exists for full round-trip tests without a phone) + `CipherState.Encrypt(plaintext) (ciphertext, err)` / `Decrypt(ciphertext) (plaintext, err)`. **No `HandshakeState` / `Config` / `HandshakePattern` / `CipherSuite` / `DHKey` re-export** — flynn types do not leak; a future migration off `flynn/noise` touches only `noise.go`. **Empty associated-data enforced at the type system** — `Encrypt`/`Decrypt` have no AD parameter, internally call flynn's `Encrypt(nil, nil, plaintext)` / `Decrypt(nil, nil, ciphertext)`; the v2 spec mandate (`docs/protocol-mobile.md:197` — "Implementations MUST NOT pass a non-empty AD without a corresponding spec amendment") is structurally impossible to violate without editing this package. Constructors length-check before any flynn call (`len(staticPriv) != KeyLen` → wrapped `ErrInvalidKeyLength`), derive the matching public via stdlib `crypto/ecdh.X25519().NewPrivateKey(staticPriv).PublicKey().Bytes()` (not flynn — byte-for-byte compatible with `internal/keys.StaticKey.PrivateKey()` which uses the same `crypto/ecdh` source), and **defensively `append([]byte(nil), key...)` copy every key argument** into flynn's `DHKey{Private, Public}` struct so a caller-side mutation after construction (or hypothetical future `StaticKey` zeroisation) cannot corrupt the live handshake state. `Random: rand.Reader` explicit in both `Config` literals — explicit-over-default removes one "did the default change?" question and pins entropy source visibly. **Responder swaps cs1/cs2; initiator does not** — flynn returns `(cs1, cs2)` where cs1 carries initiator→responder traffic and cs2 carries responder→initiator traffic (asymmetric per role); the wrapper collapses to symmetric `(send, recv)` shape on both sides. The swap is pinned structurally by `TestRoundTrip_BothDirections` — if `WriteResp` returned `(cs1, cs2)` instead of `(cs2, cs1)`, the very first cross-side `Decrypt` fails with a MAC error. Single sentinel `ErrInvalidKeyLength`; everything else (MAC failure, malformed message, out-of-order handshake call, AEAD failure, counter exhaustion, oversize message) is bare wrapped `fmt.Errorf` — caller (#434) closes the WS with one close code per surface (4426 handshake, 4421 transport) regardless of the underlying flynn reason; finer-grained sentinels deferred until a caller needs to branch. **Zero `slog` calls**; every error has shape `"noise: <op>: <flynn message>"` and never echoes plaintext/ciphertext/key bytes/early-data (pinned by `TestErrorMessages_DoNotLeakPlaintextOrKey`). **Not safe for concurrent use** — each `Responder` / `Initiator` / `CipherState` is owned by one goroutine (flynn's `CipherState` carries a mutable 64-bit nonce counter); the wrapper adds no locks because adding them would mask programming errors at the caller layer (#434 maintains a single dispatch loop per `conn_id` satisfying this structurally). Single production file `internal/noise/noise.go` (~215 LOC); package location `internal/noise/` chosen over `internal/encryption/noise/` (every internal-package occupant is one level deep, the intermediate breaks the layout). `flynn/noise v1.1.0` is the only new direct dependency (latest stable, Feb 2024, pure Go, no CGo, used by Tailscale's control protocol with the same cipher suite — production precedent); `golang.org/x/crypto v0.51.0` promoted from indirect to direct by `go mod tidy` (flynn imports `chacha20poly1305` + `blake2s`). Same-package tests with `t.Parallel()` everywhere, stdlib `testing` only, no mocks — the test "phone" is a real `Initiator` built in the same test; 11 tests cover full round-trip + both-directions transport + 32-frame loop + tampered/truncated message rejection + wrong-responder-static rejection (test doc-comment cites the spec reconciliation: AC said "initiator's `ReadResp` errors" but the natural failure surface in IK is responder's `ReadInit` because DH outputs disagree at the encrypted-`s` decryption on message 1 — the responder never produces a `respMsg`) + bad-key-length matrices + out-of-order/replayed transport frame rejection + error-message no-leak hygiene. No production consumer in this PR — `internal/relay`'s per-`conn_id` handshake routing (#434) and the re-key state machine (#435) are the planned consumers. See [features/noise-package.md](../features/noise-package.md), [codebase/433.md](../codebase/433.md), [ADR 024](../decisions/024-noise-ik-mobile-e2e.md).
- **Phase 3 foundation (#208, landed):** `internal/devices` — `Device` struct (`TokenHash`, `Name`, `PairedAt`, `LastSeenAt`; snake_case JSON tags mirroring `registryEntry`) + `HashToken(plain) string` (lowercase SHA-256 hex) + `VerifyToken(plain, hash) bool` (`crypto/subtle.ConstantTimeCompare`, length-mismatch falls out via the same path — no early-return guard on empty/malformed `hash`). Pure functions, stdlib only; SECURITY contract documented in package doc-comment ("never log plain, never wrap plain into error context"). Sibling tickets cover token minting, registry CRUD, and WS-handshake auth wiring. No bcrypt / no per-token salt: the token is 256 bits of `crypto/rand`, brute force is infeasible regardless of hash speed; the hash exists only to prevent plaintext-at-rest. See [features/devices-package.md](../features/devices-package.md).
- **Phase 3 foundation (#211, landed):** `internal/pair` — `Payload{Server, Relay, Token}` + `Encode(p) string` (JSON → base64url no-pad) + `Decode(s)` round-trip with `ErrInvalidPayload` sentinel; rejects non-base64, non-JSON-object, trailing bytes, missing/empty fields, invalid server-id; error strings never echo input or decoded fields. Pure functions, stdlib + `internal/identity`. See [features/pair-package.md](../features/pair-package.md).
- **Phase 3 foundation (#212, landed):** `internal/pair.Render(p, w io.Writer) error` — display surface that draws a UTF-8 half-block QR symbol of `Encode(p)` (`qrterminal.GenerateHalfBlock` at level `M`), a blank line, the encoded payload string, and a fixed one-line instruction. An unexported `errTrackingWriter` adapter sits between `Render` and `w` so writer errors propagate through `qrterminal/v3`'s no-error-return API and short-circuit subsequent writes (belt-and-suspenders proven by `TestRender_DoesNotPanicOnBrokenWriter`). New module dep: `github.com/mdp/qrterminal/v3` (pure-Go, MIT, built on `rsc.io/qr`). Consumed by `pyry pair` (#213). See [features/pair-package.md](../features/pair-package.md).
- **Phase 3 foundation (#216, landed):** `internal/conversations` — `ConversationID` (string newtype) + `Conversation` struct (ID, Name `*string`, Cwd, CurrentSessionID, SessionHistory `[]string` oldest-first, IsPromoted, LastUsedAt) with snake_case JSON tags + selective `omitempty`. Pure type, no I/O. See [features/conversations-package.md](../features/conversations-package.md).
- **Phase 3 foundation (#217, landed):** `internal/conversations` ID generator + on-disk registry — `NewID() (ConversationID, error)` (`crypto/rand` + UUIDv4 version/variant nibbles), `ValidID(s) bool` (canonical lowercase-hex predicate; both clone `internal/sessions/id.go`); `Registry` (mutex-guarded slice) + `Load(path)` / `(*Registry).Save(path)` (atomic temp-rename + `0o600` / `0o700`, snapshot-then-write so readers stay unblocked) / `Create(c)` / `Get(id)` / `List(filter ...ListFilter)` (variadic ergonomics, only `filter[0]` consulted; `IsPromoted *bool` to distinguish "filter to promoted" / "filter to unpromoted" / "no filter") / `Update(id, fn func(*Conversation)) bool` (callback runs under registry lock, no callback-back, no pointer retention — see [ADR 022](../decisions/022-conversations-update-callback-under-lock.md)). Sort by `LastUsedAt` then `ID` for byte-deterministic output. Atomic-write recipe duplicated from `internal/devices/registry.go` rather than extracted into a shared helper (per issue tech note: divergence will surface as Phase 3 grows). No consumers wired in this slice — daemon-startup `Load` and post-mutation `Save` are a sibling ticket. See [features/conversations-registry.md](../features/conversations-registry.md).
- **Phase 3 transport (#247, landed; extended #248):** `internal/transport` — long-lived WSS client for the binary's outbound conn to the relay. `Client` (`Connect(ctx)` blocking lifecycle, `Send([]byte)` / `Receive(ctx)` channel-proxy methods, idempotent `Close`) + `Config{URL, Headers, WriteTimeout, Logger, FatalCloseCodes}`. Native ping/pong heartbeat (30s idle ping, 30s pong timeout, `1011` close on reconnect); exponential backoff with ±20% jitter (1s/2s/4s/8s/16s/30s cap, reset to attempt 1 after a serve loop lasting ≥60s); three pump goroutines per conn (`recvPump`/`sendPump`/`pingLoop`) under a child ctx, installed BEFORE `setConn` makes the conn observable to concurrent `Send` callers (per `docs/lessons.md:290`); `conn.SetReadLimit(1 MiB)` as the inbound DoS cap; generic over frame payload (no `protocol.Envelope` parsing, no handshake — those live in `internal/relay`). #248 added: `Config.FatalCloseCodes` + `ErrFatalClose` (relay passes `{4409}` to halt the reconnect loop on server-id conflict), `Connected() <-chan struct{}` buffer-1-drop-on-full signal for the handshake layer, `ErrDisconnected` from `Receive`/`Send` on conn drop (per-conn `connDone` channel; pre-closed at construction so pre-Connect calls return the sentinel rather than blocking), `DropConn()` force-close via `conn.CloseNow` for application-layer recycle; post-`serve` close-status preference walks the three pump-return errors and picks recvPump's `CloseError` over sendPump/pingLoop's generic `use of closed network connection` (without preferring, 4409 classification flakes under `-race`). New dep: `github.com/coder/websocket v1.8.13` (MIT, ~2k LOC, context-first; first network-protocol dep in the project). See [features/transport-package.md](../features/transport-package.md), [codebase/247.md](../codebase/247.md), [codebase/248.md](../codebase/248.md).
- **Phase 3 wiring (#248, landed):** `internal/relay` — binary side of the binary↔relay wire protocol. `Connection` (`Connect(ctx, Config{ServerID identity.ServerID, RelayURL, BinaryVersion, Logger})` returns after sync validation; runs in a `run` goroutine), `Frames() <-chan protocol.RoutingEnvelope` (closes on lifecycle exit), `Wait()` (terminal classification: `ErrServerIDConflict` for fatal 4409 / `ctx.Err()` / wrapped transport error), `Close()` (idempotent via `sync.Once`); sentinels `ErrServerIDConflict` + `ErrInvalidConfig`. Wraps `internal/transport.Client` with `FatalCloseCodes: []websocket.StatusCode{4409}`; builds `x-pyrycode-server` / `x-pyrycode-version` / `user-agent: pyry/<v>` upgrade headers; on every `Connected()` signal sends `Envelope{ID:1, Type:"hello", Payload: HelloServerPayload{Role:"server", ServerID, BinaryVersion, ProtocolVersions:["v1"]}}` and awaits `hello_ack` within 5s (wire-spec deadline). Two-pass JSON decode at the trust boundary: relay-to-binary frames are ALWAYS wrapped in `RoutingEnvelope` (handshake response has `conn_id: "-"` per `docs/protocol-mobile.md` § Worked example), so the handshake decoder unmarshals `RoutingEnvelope` first then unmarshals `routing.Frame` to `Envelope` then asserts `Type == TypeHelloAck`. Handshake failure (timeout / wrong type / malformed JSON) logs WARN + `client.DropConn()` and lets the transport's backoff recycle. `wss://`-only scheme validation at `Connect` time as defense-in-depth (server-id is a public routing key but cleartext disclosure is cheap to prevent). Structured-slog field discipline forbids `token`/`payload`/raw `frame` bytes/full `Headers` map (`server_id` is the operator-actionable subset). Caller resolves `ServerID` via `internal/identity.LoadOrCreate` before `Connect` — the relay package never touches the on-disk store. Supervisor wiring, per-envelope dispatch, outbound `Send`, and token validation of phone connections are deferred to consuming tickets. See [features/relay-package.md](../features/relay-package.md), [codebase/248.md](../codebase/248.md).
- **Phase 3 wiring (#249, landed):** `internal/relay.AuthenticateFirstFrame(env, token, reg, serverID, logger) (AuthOutcome, error)` — the binary's per-phone-conn token-validation predicate composing A5 (`devices.Registry.Validate`, #210) with the v1 handshake/control payload structs (`protocol.HelloAckPayload` / `protocol.ErrorPayload`, #271). Pure call-and-return on top of `reg.Validate`; no goroutines, no channels owned; safe for arbitrary concurrent invocations across distinct phone conns. **Carrier-agnostic** — `token` is an explicit `string` argument; the function never parses WS headers / `env.Frame` payload / hello payload, only the outer envelope's `id` to echo into `in_reply_to`. The (future) relay-conn ticket picks the wire mechanism (extended routing envelope / synthesized `connection_opened` / amended hello payload) without touching this signature. Two-state semantics: `reg.Validate` returns `(Device, bool)` and the handler folds empty / never-paired / removed-after-pair all into the same `auth.invalid_token` reject (spec § Error codes line 535 same-UX equivalence locked by `TestAuthenticateFirstFrame_RevokedTokenSameUX`; `CodeAuthTokenRevoked` constant remains defined but is unused pending a future tombstone primitive). `AuthOutcome{Response protocol.RoutingEnvelope, CloseConn bool}` carries protocol-level intent; the relay-conn caller closes the phone WS with the exported `StatusUnauthorized websocket.StatusCode = 4401`. Outer envelope `ID` fixed at 1 (binary's first outbound frame on the phone's conn; relay-conn allocates 2..N). `MsgInvalidToken` const pins the spec sentence (`"device token not recognised; re-pair via pyry pair on the binary"`). `ErrMalformedHelloFrame` sentinel for the JSON-undecodable-frame path only — missing/empty/unknown/revoked tokens are valid outcomes with `nil` error. SECURITY: `token` never logged / never wrapped into error / never echoed; `device_name` IS logged on accept (operator-actionable; attacker holds a valid token) but NOT on reject (anti-enumeration of paired-device names from binary logs). LastSeenAt bump happens inside `reg.Validate` under `reg.mu` — handler calls no further mutator; persistence (`Save`) is the supervisor's responsibility. See [features/relay-package.md](../features/relay-package.md) § "Auth: per-conn first-frame validation", [codebase/249.md](../codebase/249.md).
- **Phase 3 wiring (#307, landed):** `internal/dispatch` — per-phone-conn demultiplexer + handler-table seam between `internal/relay.Connection.Frames()` and the per-envelope-type processors. Pure (imports `internal/protocol` only; no I/O, no transport, no `internal/relay`); carrier-agnostic via generic `<-chan protocol.RoutingEnvelope` input + `Outbound() <-chan protocol.RoutingEnvelope` output. `Handler func(ctx, *Conn, protocol.Envelope) error`; `Conn` exposes `ConnID()` / `NextID() uint64` (atomic.Uint64, starts at 1) / `Send(ctx, env)` / `Reply(ctx, req, respType, payload)` (sets `ID=NextID()` + `InReplyTo=&req.ID` + `TS=time.Now().UTC()` — the AC-load-bearing helper). One demux goroutine + N per-conn goroutines (one per `conn_id`, size-8 buffered input, serial `handleOne` preserving arrival order within a conn); shared bounded outbound (default 32) — slow consumer pauses producers as intended flow control. `getOrCreateConn` lookup-insert-and-`go runConn` is a single critical section under `d.mu` (prevents two-goroutine-per-conn race). `handleOne` maps four refusal shapes: malformed-JSON → `protocol.malformed` (no `in_reply_to`); `payload_encrypted=true` → `protocol.unsupported`; unknown/empty `Type` → `protocol.unknown_type`; v1 type without registered handler → `protocol.unsupported`. Sentinel-to-`Code*` mapping at the dispatcher per the `docs/PROJECT-MEMORY.md` convention; encrypted-wins-over-unknown check order inherited from `protocol.IsV1Compatible`. Register-before-Run is enforced (not advisory) via `atomic.Bool` flipped at top of `Run`; late `Register` panics — turns the handlers-map lock-free read into a defensible invariant. Daemon wiring in `cmd/pyry/relay.go` replaces #301's drain-and-discard with three goroutines (dispatcher / outbound forwarder draining `d.Outbound()` into `Connection.Send` / `Wait` classifier) chained via channel-close lifecycle. New `(*Connection).Send(env protocol.RoutingEnvelope) error` in `internal/relay` (JSON-marshal + `transport.Client.Send` wrapper; returns `transport.ErrDisconnected`/`ErrNotConnected`/`ErrClosed` verbatim, frames sent while disconnected are lost per v1 reconnect semantics). Handler table empty in this slice — every inbound phone frame falls through to `protocol.unsupported` until #303/#304/#305 register routes. Auth gate / per-conn close intent on handler error / per-conn close signal from relay deferred to #308. See [features/dispatch-package.md](../features/dispatch-package.md), [codebase/307.md](../codebase/307.md).
- **Phase 3 wiring (#308, landed):** auth-gate first frame + WS 4401 close on reject. `protocol.RoutingEnvelope` grows two optional `omitempty` fields: `Token string` (phone→binary, first frame per `ConnID` only — populated by the relay from the phone's `x-pyrycode-token` HTTP header at WS upgrade; doc-comment forbids logging at any layer; consumed only by `relay.AuthenticateFirstFrame`) and `CloseCode uint16` (binary→relay only — when non-zero asks the relay to forward `Frame` if non-empty and then close the phone WS with this code; dispatcher ignores `CloseCode` on inbound, test-pinned). `dispatch.Config` grows `FirstFrame FirstFrameGate`; `FirstFrameOutcome{Response, CloseConn, Code, Err}`; gate runs once per new `conn_id` on the per-conn goroutine — accept publishes `Response` and advances the per-conn id counter past gate's `hello_ack` (id=1) so the next handler reply gets id=2; reject sets `Response.CloseCode = Code` and publishes one envelope carrying both intents atomically (no race between two `conn.Send` calls), marks `connState.closed = true` under `d.mu`, exits the per-conn goroutine; the demux's `routeConn` is updated to drop further frames for closed conns under the same `d.mu` (MUST-FIX from architect security review). New `(*relay.Connection).CloseConn(connID, code uint16) error` (`connection.go:203+`) — close-only routing envelope, fire-and-forget at this layer; reserved for direct callers that want close-without-payload (the dispatcher's auth-reject path does NOT use it). Daemon wiring (`cmd/pyry/relay.go`): `devices.Load(resolveDevicesPath(instanceName))` runs once at startup between `relay.Connect` and `dispatch.New` (ENOENT → empty registry → every phone rejects until `pyry pair` runs; malformed JSON fails fast); `authGate(registry, serverID, logger)` closure bridges `dispatch.FirstFrameGate` → `relay.AuthenticateFirstFrame(env, env.Token, …)` mapping `outcome.CloseConn` → `Code: uint16(relay.StatusUnauthorized)` (4401). Settles `AuthenticateFirstFrame`'s deferred wire-mechanism choice as option (a): token rides `RoutingEnvelope.Token`. fakerelay grows token-injection on the first phone→binary frame per `conn_id` (`phoneConn.tokMu` + `firstFrameSent`) and `CloseCode` honor via a `phoneSend{frame, closeCode}` tuple on `pc.sendCh` (write-then-close serialised through the same send pump so the phone observes the error envelope before the WS close). fakephone grows `LastCloseStatus() (websocket.StatusCode, bool)` capturing `websocket.CloseStatus(err)` on the Read error path. e2e `internal/e2e/relay_auth_test.go` `TestRelay_AuthReject_4401` drives the full path end-to-end (empty registry → phone hello with unpaired token → `error{code=auth.invalid_token, in_reply_to=1}` → WS close `4401`). `docs/protocol-mobile.md` § Routing envelope documents both new fields. See [features/dispatch-package.md](../features/dispatch-package.md), [features/relay-package.md](../features/relay-package.md), [features/protocol-package.md](../features/protocol-package.md), [codebase/308.md](../codebase/308.md).
- **Phase 3 foundation (#255, landed):** `internal/protocol` — wire-format leaf package for mobile WS protocol v1. `Envelope` (id/type/ts/payload + omitempty in_reply_to/payload_encrypted; `Payload json.RawMessage` for deferred decode; `TS time.Time` for typed clock-skew checks at the dispatcher), `RoutingEnvelope` (binary↔relay-only `{conn_id, frame}` wrapper, `Frame json.RawMessage` for byte-for-byte splice), `IsV1Compatible(env) error` predicate (sentinel `ErrUnsupported` on `payload_encrypted=true`, `ErrUnknownType` on empty/unknown `Type`; encrypted-wins check order pinned), 12 `Code*` error-code string constants + 16 `Type*` envelope-type string constants. Refusal-to-wire-code mapping stays at the dispatcher per the convention pinned by `docs/PROJECT-MEMORY.md`. Pure data — no I/O, no goroutines. Per-type payload structs (#256), WS close codes (#247), and dispatch wiring (#248–#250) are out of scope. See [features/protocol-package.md](../features/protocol-package.md).
- **Phase 3 wiring (#213, landed):** `pyry pair` — one-shot CLI verb composing the Phase 3 foundation primitives. Mints a 256-bit `crypto/rand` token, persists `Device{TokenHash, Name, PairedAt}` to `~/.pyry/<sanitized-name>/devices.json`, and renders the QR + paste-fallback `Payload{Server, Relay, Token}` to stdout. Flags: `-pyry-name=<instance>` / `--name <label>` / `--relay <url>`. Relay precedence: `--relay` → `Config.RelayURL` → `DefaultConfig().RelayURL`. Auto-name `device-<hash[:8]>` when `--name` omitted. Exit codes: `0` success / `1` I/O or render error / `2` parse error or empty relay. **No daemon involved**, no socket dial, no goroutines, no `context.Context`. Order of operations is structural: all loads (config + relay + devices.json + server-id) before `crypto/rand`, `Save` before `Render`, so the plaintext token never escapes the process if any I/O fails (see [ADR 021](../decisions/021-pair-cli-order-of-operations.md)). New e2e helper `RunBareIn(t, home, args...)` mirrors `RunBare` with `cmd.Env = childEnv(home)` for HOME-isolated daemon-free e2e. See [features/pyry-pair-command.md](../features/pyry-pair-command.md).
- **Phase 3:** Cross-cutting services — knowledge capture, memsearch, cron runner in-process
- **Phase 4:** Remote access — relay server, E2E encryption (Noise Protocol), QR pairing
- **Phase 5:** Voice — WebRTC via pion/webrtc, STT/TTS pipeline
