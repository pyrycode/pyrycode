# Spec: persisted `debug_capture` gate for interactive-session recording (#802)

**Ticket:** #802 · **Size:** S · **Labels:** `size:s`, `security-sensitive`
**Chosen mechanism:** a persisted default-OFF `bool` in `~/.pyry/config.json` that, when ON, threads a fixed local recordings directory into the bootstrap `supervisor.Config`; the supervisor attaches tui-driver's existing `SpawnOpts.RecordTo` cast recorder to the one interactive-session spawn. No change to the agent-run (`PYRY_RECORD_DIR`) path.

## Files to read first

- `internal/config/config.go:12-46` — `Config` struct, `DefaultConfig()` overlay, `Load` decode. The new `bool` field lands here; Go's zero value gives default-OFF for free (no `DefaultConfig` change).
- `internal/config/config_test.go:19-81` — the `RelayURL` table-driven load test. Clone the "missing file → default", "partial `{}` → default", "full file overrides" cases for `debug_capture`. This is where AC1 (unset → OFF) and AC2 (persist across reload) are exercised.
- `internal/supervisor/supervisor.go:80-144` — `supervisor.Config`. Add the `RecordDir string` field here, next to the other spawn inputs.
- `internal/supervisor/supervisor.go:656-675` — `runOnce`; the single `tuidriver.Spawn(cmd, tuidriver.SpawnOpts{MirrorOutput: true})` call at line 671 is the **only** spawn seam. This is where `RecordTo` is populated. Both foreground (line 727+) and service/Bridge mode (line 680+) flow through this one call, so recording covers both with one edit.
- `internal/agentrun/ptyrunner/runner.go:316-362, 607-676` — the existing recorder posture: the `SECURITY:` comment (0600, non-synced dir, not-a-log), `recordingPath`, and the prune/finalize lifecycle. **Read for the security contract and the reasons; do NOT modify (out of scope). Deliberately reuse only the minimum** — dir-create + unique path — not prune/rename.
- `internal/sessions/pool.go:148-165` — `SessionConfig` (per-session invocation shape). Add `RecordDir string` here.
- `internal/sessions/pool.go:383-410` — the **bootstrap** `supervisor.Config` construction (the daemon's interactive session). Map `SessionConfig.RecordDir` → `supCfg.RecordDir` here.
- `internal/sessions/pool.go:1041-1051` — `buildSession`, the per-caller (`pyry sessions new` / ACP) `supervisor.Config`. **Leave untouched** — these are not "the daemon's interactive session"; `RecordDir` stays its zero value `""` here.
- `cmd/pyry/main.go:710-730` — the daemon `config.Load` → `sessions.New(sessions.Config{Bootstrap: SessionConfig{...}})` wiring. Resolve the recordings dir here and set `Bootstrap.RecordDir` from `cfg.DebugCapture`.
- `cmd/pyry/main.go:120-137` — `resolveClaudeSessionsDir`; mirror its `os.UserHomeDir()` + `filepath.Join` shape for the new `resolveRecordingsDir` helper.
- tui-driver `pkg/tuidriver/session.go:38-53` (`SpawnOpts.RecordTo` doc) — confirms tui-driver owns the file end to end: opens `0600 O_EXCL`, writes the asciinema-v2 header on `Spawn`, closes on `Session.Close`. The consumer owns only path lifecycle (dir creation, pruning, rename).
- `.gitignore:8-12` — `*.cast` is already ignored repo-wide (belt-and-suspenders; the default dir lives outside the repo anyway).

## Context

When the client app misbehaves, a full terminal recording of the interactive session makes root-causing far faster. The recording captures every PTY byte — the prompt, claude's output, and all tool output, which can include file contents and secrets — so it must be strictly opt-in and off by default.

The recording *mechanism* already exists and is battle-tested on the agent-run path: `tuidriver.SpawnOpts.RecordTo`, attached behind the `PYRY_RECORD_DIR` env flag in `ptyrunner` (#552). This ticket adds the *persisted operator gate* the ACs ask for and attaches the same recorder to the daemon's **interactive-session** spawn (the bootstrap supervisor). Serving/downloading the recording is the next ticket.

The gate is what keeps the feature removable: everything hangs off one `bool`; set it OFF (or delete the field) and the spawn is byte-identical to today.

## Design

### 1. Persisted setting (`internal/config`)

Add one field to `Config`:

```go
// DebugCapture, when true, records the daemon's interactive session to a
// .cast file (see #802). Default OFF: the JSON zero value for an absent
// field is false, so a config that omits "debug_capture" behaves as OFF
// with no DefaultConfig entry. SECURITY: a recording holds every PTY byte
// — prompt, output, tool output — so this is strictly opt-in.
DebugCapture bool `json:"debug_capture"`
```

- **No `DefaultConfig()` change.** `false` is both the Go zero value and the desired default; adding it to the overlay would be redundant and would wrongly imply the default is meaningful to override.
- Persistence + default-OFF are pure `encoding/json` behaviour already proven by `RelayURL`; the config test is the whole verification for AC1/AC2.

### 2. Supervisor recorder attachment (`internal/supervisor`)

Add one field to `supervisor.Config`:

```go
// RecordDir, when non-empty, records each interactive-session spawn to a
// unique 0600 .cast file under this directory via tui-driver's
// SpawnOpts.RecordTo (#802). Empty (the default) attaches no recorder and
// leaves the spawn byte-identical to the pre-recording behaviour. The
// directory is created 0700 on demand; tui-driver owns the file (0600
// O_EXCL, header, close-on-Close). Only the daemon's bootstrap session
// sets this; per-caller sessions leave it empty.
RecordDir string
```

Introduce one **pure helper** (the AC4 test seam) plus one small path builder:

- `func spawnOpts(recordDir string, now time.Time) tuidriver.SpawnOpts` — returns `{MirrorOutput: true}` when `recordDir == ""` (RecordTo is the zero value `""` — byte-identical to today), and `{MirrorOutput: true, RecordTo: recordingPath(recordDir, now)}` otherwise. Pure and fully unit-testable — this is what AC4's "assert the unset case explicitly" pins.
- `func recordingPath(dir string, now time.Time) string` — `filepath.Join(dir, now.UTC().Format("20060102T150405.000000000Z")+".cast")`. Nanosecond stamp; see § Error handling for the uniqueness argument.

`runOnce` change (at line 671) — replace the literal `SpawnOpts{MirrorOutput: true}` with:

```
recordDir := s.cfg.RecordDir
if recordDir != "" {
    if err := os.MkdirAll(recordDir, 0o700); err != nil {
        s.log.Warn("debug_capture: recordings dir unavailable; continuing without capture", "dir", recordDir, "err", err)
        recordDir = ""            // degrade to no-capture; never fail the spawn
    }
}
sess, err := tuidriver.Spawn(cmd, spawnOpts(recordDir, time.Now()))
```

The `MkdirAll` side effect + fail-open-to-no-capture stays in `runOnce`; `spawnOpts` stays pure. Degrading to no-capture on a dir error is the **security-preferred** direction (no recording is the safe state) and keeps the daemon alive.

**Deliberately minimal — NOT reused from ptyrunner:** no startup prune, no `-ok`/`-err` finalize rename. The AC requires only that a `.cast` be produced; the supervisor is a long-lived restart loop with no per-run outcome to tag and no session UUID, so the ptyrunner finalize/prune machinery does not transfer cleanly. Unbounded growth is acceptable for an opt-in debug flag and is called out in § Open questions. This also honours "do NOT touch the agent-run path" — nothing in `ptyrunner` is exported or moved.

### 3. Wiring (`internal/sessions` + `cmd/pyry`)

- `SessionConfig` gets `RecordDir string` (per-session invocation shape).
- Bootstrap construction (`pool.go:383`) maps `RecordDir: cfg.Bootstrap.RecordDir` into `supCfg`. `buildSession` (`pool.go:1041`) is untouched — per-caller sessions record nothing.
- `cmd/pyry/main.go`: add `resolveRecordingsDir()` mirroring `resolveClaudeSessionsDir` — returns `filepath.Join(home, ".local", "share", "pyry-recordings")`, or `""` if `os.UserHomeDir()` fails (in which case capture silently no-ops, consistent with the other resolvers). In the `sessions.Config{Bootstrap: SessionConfig{...}}` literal, add:

  ```
  RecordDir: recordDirIf(cfg.DebugCapture),   // dir when ON, "" when OFF
  ```

  where the dir is `resolveRecordingsDir()` only when `cfg.DebugCapture` is true, else `""`. The gate lives at the cmd layer; the supervisor only ever sees a non-empty dir when the operator opted in.

**Default directory choice:** `~/.local/share/pyry-recordings/` is exactly the non-synced, non-backed-up location the ptyrunner `SECURITY:` comment already designates as the intended sibling of `~/.local/share/pyry-artifacts/`. Sharing that dir with the agent-run path is fine — filenames are timestamp-unique across both producers. The directory is **not** operator-configurable in this ticket (one new config field only); see § Open questions.

### Data flow

```
~/.pyry/config.json {"debug_capture": true}
        │ config.Load
        ▼
config.Config.DebugCapture ─── cmd/pyry serve ──► resolveRecordingsDir()
        │                                                │
        └──────────── (true) ────────────────────────────┘
                              │  Bootstrap.RecordDir = "~/.local/share/pyry-recordings"
                              ▼
        sessions.Config.Bootstrap (SessionConfig.RecordDir)
                              │  pool.go:383 bootstrap supCfg
                              ▼
        supervisor.Config.RecordDir
                              │  runOnce: MkdirAll 0700 → spawnOpts(dir, now)
                              ▼
        tuidriver.Spawn(cmd, SpawnOpts{MirrorOutput:true, RecordTo:<dir>/<stamp>.cast})
                              │  tui-driver: open 0600 O_EXCL, header, record, close-on-Close
                              ▼
        <dir>/20060102T150405.000000000Z.cast   (owner-only)
```

## Concurrency model

No new goroutines, channels, or locks. The recorder attachment is a synchronous decision inside the existing single-threaded `runOnce` body, before `tuidriver.Spawn`. tui-driver owns the recording file's I/O on its own PTY-reader goroutine (unchanged, internal to the library). Each `runOnce` iteration (one claude lifetime) opens exactly one `.cast`; the backoff restart loop produces a sequence of distinct files, one per spawn.

## Error handling

- **Recordings dir unresolvable (`os.UserHomeDir` fails):** `resolveRecordingsDir()` returns `""`; with the gate ON this yields `RecordDir == ""` → no capture, no error. Mirrors `resolveClaudeSessionsDir`'s degrade-to-empty contract.
- **`MkdirAll` fails (permissions, path is a file, etc.):** `runOnce` logs a **path+err-only** Warn and degrades to no-capture for that spawn; the spawn proceeds unchanged. Fail-open-to-no-recording — the daemon must not die because a debug sink is unwritable.
- **`RecordTo` open fails inside tui-driver (e.g. O_EXCL collision):** surfaces as a `tuidriver.Spawn` error → `runOnce` returns `fmt.Errorf("spawn: %w", err)` → backoff → the next spawn gets a fresh nanosecond stamp (time has advanced) and succeeds. Self-healing; the collision cannot wedge the loop.
- **Filename uniqueness:** spawns in the backoff loop are separated by at least `BackoffInitial` (≥500 ms), and any crash-respawn cycle costs real fork/exec/PTY-alloc wall-clock. A nanosecond-precision UTC stamp is collision-free in practice; O_EXCL is the deterministic backstop (above). Per **Evidence-Based Fix Selection**, no extra sequence counter is added to defend an unobserved sub-nanosecond collision.
- **Malformed `config.json`:** unchanged — `config.Load` already returns a wrapped parse error and the daemon refuses to start. The new field rides that contract.

## Testing strategy

**`internal/config/config_test.go`** (AC1, AC2):
- Extend `TestDefaultConfig` expectation to include `DebugCapture: false`.
- Add table cases to `TestLoad`: missing file → `DebugCapture:false`; `{}` → `false` (**explicit unset-is-OFF**, AC1); `{"debug_capture": true}` → `true` (persist/reload, AC2); `{"relay_url":"…","debug_capture":true}` → both set (coexistence).

**`internal/supervisor/supervisor_test.go`** (AC3, AC4, AC5):
- **Pure unit test on `spawnOpts`** (AC4, the explicit assertion the AC demands): `spawnOpts("", now)` returns `SpawnOpts{MirrorOutput:true}` with `RecordTo == ""` (byte-identical); `spawnOpts("/some/dir", now)` returns `MirrorOutput:true` and a non-empty `RecordTo` that is under `/some/dir` and ends in `.cast`. Table-driven.
- **Integration test via the fake-claude helper** (AC3): build a `Config` with `RecordDir: t.TempDir()` using the existing `helperConfig("emit_marker", …)` / `TestHelperProcess` seam, run one `runOnce` iteration to child exit, then assert exactly one `*.cast` exists in the dir and `os.Stat(...).Mode().Perm() == 0o600`. (tui-driver writes the asciinema header at spawn, so the file exists even for a trivial fake child.)
- **Byte-identical / no-file when OFF** (AC4): same harness with `RecordDir: ""` → assert no `*.cast` is created in a sibling temp dir and the run behaves as today (reuse an existing exit/restart assertion).
- **No content in logs** (AC5): capture the supervisor logger into a buffer (the `syncBuffer` helper already in the test file) during the ON integration run; assert the buffer contains no recorded bytes — the only recording-related log lines are the path/status Warn (dir + err), never session content. Assert the `.cast`'s own byte content does not appear in the log buffer.

**Non-vacuity note for the developer:** the ON integration test must confirm a real file with real perms, not merely that no error was returned.

## Security review (label-gated: `security-sensitive`)

Ran the security-review pass on this spec. The recording is the highest-value secret surface in the system (every PTY byte, unencrypted by design), so each category is walked against "could this arm, widen, or leak the recorder without an explicit local operator opt-in?"

- **Default-OFF / opt-in integrity (AC1, AC4).** The gate is a single `bool` whose absence decodes to `false` (Go zero value; no `DefaultConfig` entry can accidentally flip it). `RecordDir` reaches the supervisor as non-empty **only** when `cfg.DebugCapture == true`, resolved at the cmd layer. With the field unset or false, `spawnOpts` returns `RecordTo == ""` and the spawn is byte-identical — proven by the explicit unset unit test. No code path attaches the recorder without the persisted flag. **PASS.**
- **Trust boundary — who can arm it.** Arming requires writing `debug_capture: true` to `~/.pyry/config.json`, a local operator-owned file read at daemon start. The phone/relay/ACP surfaces never write config and never set `RecordDir` (per-caller `buildSession` leaves it `""`). No untrusted (remote) input can enable recording or influence the path. The recordings directory is a **compile-time-fixed** location under `$HOME`; the config toggles a bool, not a path, so there is **no path-traversal surface** from untrusted input. **PASS.**
- **File confidentiality (AC3).** tui-driver opens the `.cast` `0600 O_EXCL` and owns it end to end (verified in `session.go:40`); the O_EXCL open also defeats a pre-create symlink/swap. The containing directory is created `0700` (owner-only). Both match the inherited ptyrunner posture. **PASS.**
- **Location — no sync/backup exfil (inherited).** Default dir is `~/.local/share/pyry-recordings/` — the non-synced, non-backed-up location the ptyrunner `SECURITY:` comment already designates; `*.cast` is gitignored repo-wide as belt-and-suspenders. Never inside the repo, `~/.claude`, or an Obsidian-Sync/Time-Machine path. **PASS.**
- **Secret non-leakage in logs (AC5).** The two possible new log emissions (dir-create Warn; the inherited spawn-error wrap) carry only `dir`/`err` (a filesystem path + OS error) — never session bytes, never `.cast` content. The supervisor's existing logging discipline (the `SECURITY:` comments on `ScreenSnapshot`/`Session` forbidding screen literals) is preserved: no recording content is named, stored, or logged in this package. Enforced structurally (there is no code path from PTY bytes to a log call) and asserted by the AC5 test. **PASS.**
- **Fail-safe direction.** Every error path (home unresolved, `MkdirAll` fail, O_EXCL collision) degrades toward **less** recording (no capture) or a clean retry, never toward capturing-without-a-file or logging-the-bytes. **PASS.**
- **Removability.** The whole feature is one `bool` and one `RecordDir` field; setting the flag OFF, or deleting the config field and the field wiring, fully removes the behaviour with the spawn returning to byte-identical. **PASS.**

**Verdict: PASS.** No FAIL findings; no MUST-FIX constraints beyond the ACs (the path+err-only log discipline is already an AC and is pinned in § Error handling).

## Open questions

- **Unbounded growth.** The supervisor path intentionally omits prune (unlike ptyrunner's 7-day `recordingMaxAge`). For an opt-in debug flag that is OFF by default this is acceptable; if operators leave it on long-term, a follow-up can add the same age-based prune. Deferred — no observed failure, and reusing ptyrunner's prune would either duplicate code or touch the out-of-scope agent-run package.
- **Operator-configurable directory.** This ticket ships a fixed default dir to keep it to a single config field. If a deployment needs a different location (e.g. an encrypted volume), a future `debug_capture_dir` string field slots in beside the bool with no structural change.
- **Outcome tagging / serving.** No `-ok`/`-err` rename and no serving/download — the latter is the next ticket, which may want to revisit whether the daemon should tag recordings by outcome the way ptyrunner does.
