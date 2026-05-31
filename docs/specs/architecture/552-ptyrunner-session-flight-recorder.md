# Spec #552 — agent-run: TUI session flight-recorder behind `PYRY_RECORD_DIR` (ptyrunner)

**Ticket:** [#552](https://github.com/pyrycode/pyrycode/issues/552) · **Size:** S · **Labels:** `security-sensitive`

Opt-in flight recorder for the interactive-TUI claude session driven by
`internal/agentrun/ptyrunner`. When the operator sets `PYRY_RECORD_DIR`, mirror
every PTY byte to an asciinema v2 `.cast` file so a failed run can be replayed
(`asciinema play`) or diffed offline. OFF by default — byte-identical to today
when the env var is unset/empty. This is the pyrycode-side consumer wiring of
the `tuidriver.NewCastRecorder` API published by tui-driver#125; **library-first,
no edit-and-cp.**

---

## Files to read first

| Path | What to extract |
| --- | --- |
| `internal/agentrun/ptyrunner/runner.go:232-298` | `Run` entry: validation block, `cmd` build, `EnsureClaudeEnv` (line 280), `tuidriver.Spawn(cmd, SpawnOpts{})` (line 282), `defer sess.Close()` (line 290). **The recorder block + its defer go between line 280 and line 282.** |
| `internal/agentrun/ptyrunner/runner.go:399-447` | The rest of the documented defer-LIFO chain (`cancel`/`emitter.Close`/`counter.Stop`/`wg.Wait`). Confirms run-order so the new recorder defer lands **last**. |
| `internal/agentrun/ptyrunner/runner.go:180-231` | `Run` doc-comment, esp. the **Cleanup ordering** block (219-231) — must be updated to append `finalizeRecording()` as the new last LIFO step. |
| `internal/agentrun/ptyrunner/runner.go:1-57` | Package doc (no-content-logging discipline para, lines 19-27) + import block (add `path/filepath`; `time`, `os`, `io`, `fmt` already imported). Add the "recorder is a separate opt-in artifact, not a log" carve-out. |
| tui-driver `pkg/tuidriver/castrecorder.go` (whole, ~117 lines) | `NewCastRecorder(w io.Writer, cols, rows int) *CastRecorder` → `WriteHeader() error` once → pass as `SpawnOpts.Mirror`. **The recorder owns no lifecycle — the consumer owns and closes the underlying `*os.File`.** `*CastRecorder` satisfies `io.Writer` (compile-time assert line 61). |
| tui-driver `pkg/tuidriver/session.go:37-49, 84-128, 243-265` | `SpawnOpts.Mirror` is read at spawn time; the PTY reader goroutine is the **only** writer of `Mirror`; `Close()` does `s.PTY.Close()` then `<-s.readerDone` (line 262-263) — the happens-before that makes a post-`Close` file close/rename race-free. |
| tui-driver `pkg/tuidriver/pty.go:10-16, 120-140` | `DefaultPtyRows uint16 = 40`, `DefaultPtyCols uint16 = 120`; `StartPTY` sets the PTY to exactly those. **This is the cols/rows source** (resolves the ticket's open design point). |
| `internal/agentrun/ptyrunner/runner_test.go:38-128` | `helperRunCfg(t, mode, stdout, stderr, jsonlBody)`, `testSessionID`, `happyPathBody`, `parseTrailer` — reuse verbatim for the recording tests. |
| `internal/agentrun/ptyrunner/helper_test.go:69-167` | Fake-claude modes. `jsonl` → clean (`nil`) → `-ok`; `trust`/`network_failure` → sentinel error → `-err`. All modes write `❯ ` to the PTY (so the mirror always captures ≥1 event). |
| `cmd/pyry/agent_run.go:288-323` | `runAgentRunPty`. **Not modified by this ticket** (see Design § "Where the env var is read"). Listed so the developer confirms no cmd-side change. |
| `.gitignore` (whole, ~40 lines) | Add `*.cast`. |
| `go.mod:11` | `github.com/pyrycode/tui-driver v0.0.0-20260523181457-c2dcd1e49992` → bump to `v0.0.0-20260531143940-6bec180ad34c` (publishes `NewCastRecorder`). |

---

## Context

ptyrunner drives claude as an interactive TUI under a PTY. The **control channel**
— the rendered PTY screen — is never persisted: it lives only in tui-driver's
~4 KB rolling buffer and is gone the moment the process exits. Nearly every
ptyrunner failure (hang, wrong state detection, modal mis-handling, paste
corruption) lives in that un-persisted channel. (The content channel — claude's
per-session JSONL — is already persisted by claude itself.) A flight recorder
closes the gap: mirror every PTY byte to a `.cast` file so a bad session can be
replayed or parsed offline.

The seam already exists. tui-driver's `Spawn` reads `SpawnOpts.Mirror io.Writer`
and the reader goroutine copies every PTY chunk into it; production passes
`Mirror: nil` today. tui-driver#125 published `NewCastRecorder`, an `io.Writer`
that frames each chunk as an asciinema "o" event. This ticket wires the two
together behind one env var.

**Operator decisions (2026-05-30, do not re-litigate):** debug flag OFF by
default; save both successes and failures when enabled; prune recordings older
than 7 days; temp filename is session-identifiable from creation so a crash
before rename still leaves a tagged file; clean close only *appends* an outcome
suffix.

---

## Design

### Where the env var is read (decision: inside `ptyrunner.Run`)

The whole feature — env read, dir create, prune, file create, header, Mirror
wiring, close+rename defer — lives in `internal/agentrun/ptyrunner/runner.go`.
`Run` reads `os.Getenv("PYRY_RECORD_DIR")` directly.

Rationale (this is the "is there a more elegant way?" pause point — recorded so
it isn't re-litigated downstream):

- The ticket names the `TUIDRIVER_*` env-driven pattern as the model: an env
  knob read **next to the seam it gates**. The `SpawnOpts.Mirror` seam lives in
  `Run`; the env read belongs there too — one cohesive block.
- Smallest blast radius: one production file, one test file. The recorder
  lifecycle (create-before-`Spawn`, defer-after-`Close`, prune) **must** live in
  `Run` regardless of where the env is read; co-locating the read keeps the
  feature in one place.
- No new public `Config` field. The recorder is an internal behaviour of `Run`,
  not a caller-supplied input. The only production caller (`runAgentRunPty`)
  derives nothing here, so `cmd/pyry/agent_run.go` is untouched.
- Testable: `t.Setenv("PYRY_RECORD_DIR", t.TempDir())` in the (non-parallel)
  recording tests, exactly as the AC describes.

Trade-off accepted: this is the first `os.Getenv` inside `ptyrunner`. Acceptable
— ptyrunner is an application-internal package, not a reusable library, and the
knob is a debug aid cohesive with the Mirror seam. (PYRY_CLAUDE_BIN /
PYRY_USE_STREAMJSON are read in `cmd` because they substitute a flag value and
select the runner path, respectively — neither shape applies here.)

**There is no baked-in default directory.** The env var unifies switch + location:
set → ON and write there; unset/empty → OFF. `~/.local/share/pyry-recordings/`
is *documented guidance* for the operator to point the var at — never a fallback
the code falls back to.

### Control flow inside `Run`

Insert one block **after** `tuidriver.EnsureClaudeEnv(cmd)` (line 280) and
**before** `tuidriver.Spawn(...)` (line 282). Mirror defaults to nil:

```
var mirror io.Writer
if dir := os.Getenv("PYRY_RECORD_DIR"); dir != "" {
    // 1. ensure dir (fail-fast)         os.MkdirAll(dir, 0o700)
    // 2. prune *.cast older than 7d     pruneOldRecordings(dir, logger)   [best-effort]
    // 3. create session-tagged temp     f, tmpPath, err := createRecordingFile(dir, cfg.SessionID)  (fail-fast)
    // 4. register finalize defer NOW     defer func() { finalizeRecording(f, tmpPath, err, logger) }()
    // 5. recorder + header               rec := tuidriver.NewCastRecorder(f, cols, rows); rec.WriteHeader()  (fail-fast)
    // 6. arm the mirror                  mirror = rec
}
sess, err := tuidriver.Spawn(cmd, tuidriver.SpawnOpts{Mirror: mirror})
```

When `dir == ""`, `mirror` stays nil and `SpawnOpts{Mirror: nil}` is byte-for-byte
equivalent to today's `SpawnOpts{}` — **AC #1 (OFF = unchanged) holds by
construction.**

### cols / rows (resolves the ticket's open design point)

tui-driver's `StartPTY` sets every PTY it spawns to `DefaultPtyRows` × `DefaultPtyCols`
(40 × 120), and ptyrunner spawns with empty opts, so the live PTY is exactly
120×40. Record with the **same exported constants** so the cast's dimensions
match the bytes' origin and track any upstream change:

```
rec := tuidriver.NewCastRecorder(f, int(tuidriver.DefaultPtyCols), int(tuidriver.DefaultPtyRows))
```

No magic numbers, no guess.

### Filenames + outcome tag

- Temp (at create): `<timestamp>-<sessionid>.cast`, mode `0600`.
  - `<timestamp>` = `time.Now().UTC().Format("20060102T150405Z")` (sortable, e.g.
    `20260530T182431Z`).
  - `<sessionid>` = `cfg.SessionID`.
  - Open with `os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600` — `O_EXCL` is cheap
    hygiene against a pre-created path; UUID + second-stamp makes collision
    otherwise impossible.
- Final (on close): insert the outcome **before** the `.cast` extension —
  `<timestamp>-<sessionid>-ok.cast` / `-err.cast`. Computed as
  `strings.TrimSuffix(tmpPath, ".cast") + "-" + outcome + ".cast"`.

  The suffix goes inside the stem (not `.cast-ok`) so the file **stays
  `*.cast`** — required for both prune (AC #4 globs `*.cast`) and replay. This
  is the precise meaning of the AC's "only adds the suffix": annotate the stem,
  keep the extension.

`outcome` = `"ok"` when the run's final error is nil, `"err"` otherwise. This
maps the fake-claude modes exactly: `jsonl` → nil → `-ok`;
`trust`/`network_failure` → sentinel → `-err` (**AC #3**). The operator-shutdown
collapse to nil (ctx-cancel) tags `-ok`, consistent with the AC's "nil error →
-ok". Panic: the defer still runs and renames (to `-ok`, since the named return
is nil), satisfying "leaves either the renamed file or the session-tagged temp"
— no panic-specific branch (no observed panic failure mode to defend against).

### Defer placement — the load-bearing invariant

The recorder file must be closed/renamed **only after** tui-driver's PTY reader
goroutine (the sole `Mirror` writer) has stopped. `sess.Close()` guarantees that:
it closes the PTY then blocks on `<-s.readerDone` (`session.go:262-263`). So the
recorder finalize must run **after** `sess.Close()`.

Defers run LIFO. Registering the recorder finalize **before** `Run`'s existing
`defer sess.Close()` (line 290) — which it is, since the recorder block sits
before `Spawn` — makes it the **last** step to run:

```
cancel() → wg.Wait() → counter.Stop() → emitter.Close() → sess.Close() → finalizeRecording()
```

This appends one step to the documented chain without reordering any existing
defer. Update the `Run` doc-comment's Cleanup-ordering block (runner.go:219-231)
to show `finalizeRecording()` as the new tail and one sentence on why
(post-`readerDone`, no Mirror write can race the close+rename).

**The finalize defer MUST be a wrapping closure** — `defer func() {
finalizeRecording(f, tmpPath, err, logger) }()` — so `err` (the named return) is
read when the defer *fires* (final value), not at registration. A bare
`defer finalizeRecording(f, tmpPath, err, logger)` would capture the nil `err`
at registration time. Classic Go gotcha; call it out so the developer doesn't
trip it.

### Named return

Change `func Run(ctx context.Context, cfg Config) error` →
`func Run(ctx context.Context, cfg Config) (err error)`. Non-invasive: every
existing `return X` still assigns and returns; all top-level `x, err := ...`
already bind the function-scope `err`. No existing defer assigns to `err`, so the
finalize defer reads the true return value. (Verified: no top-level shadowing of
`err`; inner blocks use `werr`/`herr`/`cerr`.)

### New unexported helpers (in `runner.go`)

```
func pruneOldRecordings(dir string, logger *slog.Logger)
    // filepath.Glob(filepath.Join(dir, "*.cast")) → for each, os.Stat;
    // os.Remove when ModTime().Before(now - recordingMaxAge). Best-effort:
    // Warn-log glob/remove errors, never returns an error, never aborts the run.

func createRecordingFile(dir, sessionID string) (f *os.File, tmpPath string, err error)
    // tmpPath = filepath.Join(dir, time-stamp + "-" + sessionID + ".cast");
    // os.OpenFile(tmpPath, O_CREATE|O_EXCL|O_WRONLY, 0o600).

func finalizeRecording(f *os.File, tmpPath string, runErr error, logger *slog.Logger)
    // _ = f.Close(); outcome from runErr; os.Rename(tmpPath, finalPath);
    // Warn-log a failed rename (temp remains, session-tagged — AC-acceptable).

const recordingMaxAge = 7 * 24 * time.Hour
```

`MkdirAll` + `WriteHeader` are called inline in the `Run` block (each fail-fast),
not wrapped in a helper — they read clearly in place.

### Prune is scoped, deterministic, and strictly inside `dir`

`filepath.Glob(filepath.Join(dir, "*.cast"))` lists only top-level `.cast` files
in `dir` (`*` never matches `/`, so no recursion, no escape). `os.Remove` runs
only on those glob hits whose mtime is older than 7 days. It can never touch a
non-`.cast` file, never a subdirectory, never a path outside `dir`. **AC #4**:
fresher `.cast` files (incl. the run's own freshly-created temp) are kept; the
new file's mtime is `now`, far inside the window. Prune runs before file create.

### Fail-fast vs best-effort

- **Setup failures fail-fast** (`MkdirAll`, `createRecordingFile`, `WriteHeader`)
  → return a wrapped `ptyrunner: <op>: %w` error before `Spawn`. The operator
  opted in explicitly; silently continuing without a recording would betray the
  opt-in, and nothing is lost (no session has started yet — fix the env var,
  re-run). If `WriteHeader` fails, the finalize defer (already registered at
  step 4) closes + renames the partial file to `-err`; no leaked temp.
- **Prune is best-effort** — housekeeping unrelated to *this* run's recording; a
  stale-cleanup hiccup must not block recording a fresh session.

The recording feature must never silently corrupt the primary job (driving
claude); the only new error paths are at the very start, before `Spawn`.

---

## Concurrency model

No new goroutines, channels, or timers. The recorder is driven entirely by
tui-driver's existing PTY reader goroutine via the `Mirror` seam.

- **Only writer of the recorder:** tui-driver's reader goroutine (`session.go:118`),
  serialised by `CastRecorder`'s internal mutex (single writer anyway).
- **`*os.File` cross-goroutine access:** reader-goroutine writes during the run;
  `finalizeRecording` closes after the run. Ordered by `<-s.readerDone` inside
  `sess.Close()` (happens-before). Race-free; verifiable under `go test -race`.
- **Lifecycle:** the recorder defer is the strict tail of the existing LIFO
  chain; it cannot leak (it runs on every return path past `Spawn`, and on the
  pre-`Spawn` early returns it simply closes a header-only file).

---

## Error handling

| Failure | Handling | Return / effect |
| --- | --- | --- |
| `PYRY_RECORD_DIR` unset/empty | No recorder block runs | Byte-identical to today |
| `os.MkdirAll(dir,0700)` fails | Fail-fast (before `Spawn`) | `fmt.Errorf("ptyrunner: recording dir: %w", err)` |
| `createRecordingFile` fails | Fail-fast | `fmt.Errorf("ptyrunner: create recording: %w", err)` |
| `WriteHeader` fails | Fail-fast; finalize defer renames partial → `-err` | `fmt.Errorf("ptyrunner: recording header: %w", err)` |
| Mid-stream mirror write fails | Swallowed by tui-driver (`_, _ = Mirror.Write`) | Recording truncates; run unaffected (not observable here) |
| Prune glob/stat/remove fails | Best-effort | Warn-log; run proceeds |
| `os.Rename` (finalize) fails | Best-effort | Warn-log; session-tagged temp remains (AC-acceptable) |
| Run returns non-nil / ctx-cancel-to-nil | finalize tags `-err` / `-ok` from named `err` | File sealed either way |

**No-content-logging discipline holds.** The recorder is a separate opt-in
*artifact*, not a log. No new `slog` line carries prompt bytes, buffer
substrings, or `.cast` contents — error/Warn lines carry only the op name + the
wrapped I/O error + the path. Add this carve-out to the package doc-comment.

---

## Security review

See the dedicated section at the end of this spec (ticket is `security-sensitive`).
Headlines folded into the design above: prune is glob-scoped and cannot escape
`dir`; files are `0600`; the `.cast` may contain full session content (incl. tool
output / possible secrets), documented at the recorder block; recordings dir is
gitignored (`*.cast`).

---

## Testing strategy

Reuse `helperRunCfg` + the existing fake-claude modes in `runner_test.go`
(the one edited test file). All recording tests use `t.Setenv` so they **must
not** call `t.Parallel()`. The `.cast` the mirror produces is always non-empty:
every helper mode writes `❯ ` to the PTY → ≥1 mirrored "o" event after the v2
header.

Scenarios (bullets, not full bodies — developer writes in the project idiom):

1. **OFF by default (AC #1).** Do not set `PYRY_RECORD_DIR` (or set it `""`). Run
   the `jsonl` happy path. Assert: `Run` returns nil; the normal trailer line is
   present (run unaffected); `filepath.Glob("*.cast")` under the home + workdir
   temp dirs returns zero — no stray recording.

2. **Created + tagged `-ok` + valid v2 (AC #2 + AC #3-ok).** `t.Setenv` record dir
   = `t.TempDir()`. Run `jsonl` happy path. Assert: nil return; exactly one
   `*.cast` in the dir; name matches `^\d{8}T\d{6}Z-<sid>-ok\.cast$`; mode is
   `0600` (`os.Stat().Mode().Perm()`); file non-empty; **parses as asciinema v2**
   — first line unmarshals to a struct with `version == 2` (and width 120 /
   height 40), and ≥1 subsequent line unmarshals to a 3-element
   `[]any{float64, "o", string}`.

3. **Tagged `-err` (AC #3-err).** `t.Setenv` record dir. Run mode `network_failure`
   (and/or `trust`). Assert: `errors.Is(err, ErrNetworkFailure)` (resp.
   `ErrTrustModalDetected`); exactly one `*.cast`, name ends `-err.cast`.

4. **7-day prune, scoped (AC #4).** `t.Setenv` record dir. Pre-create `old.cast`
   and `os.Chtimes` it to `now - 8*24h`; pre-create `keep.cast` at `now`;
   optionally a non-`.cast` `decoy.log` aged 8 days. Run `jsonl` happy path.
   Assert: `old.cast` gone; `keep.cast` present; `decoy.log` untouched; the run's
   own `*-ok.cast` present.

Required green check (paste the actual `ok` line, don't claim green blind):

```
go build ./... && go vet ./internal/agentrun/ptyrunner/ && go test ./internal/agentrun/ptyrunner/
```

Run the recording tests under `-race` at least once (they exercise the
reader-goroutine ↔ finalize handoff).

---

## Non-test deliverables (AC #5)

- **`.gitignore`** — add `*.cast` (belt-and-suspenders: if an operator points
  `PYRY_RECORD_DIR` inside the repo while debugging, recordings never get
  committed). The production default location is outside the repo entirely.
- **`go.mod` / `go.sum`** — `go get github.com/pyrycode/tui-driver@v0.0.0-20260531143940-6bec180ad34c`
  then `go mod tidy`. Confirm `NewCastRecorder` resolves
  (`go build ./internal/agentrun/ptyrunner/`).
- **Docs at the flag site** — block comment at the recorder block warning that
  enabling writes the **full session content (prompt + claude output + ALL tool
  output, which can include file contents and secrets)** to disk; name the
  suggested non-synced location `~/.local/share/pyry-recordings/` (sibling of
  `~/.local/share/pyry-artifacts/`, outside Obsidian Sync / Time Machine reach);
  state OFF-by-default. Mirror a one-line carve-out into the package doc-comment.

---

## Out of scope / deferred

- **Second `Mirror` consumer.** If a future ticket adds another `Mirror` use,
  wrap with `io.MultiWriter` (the ticket calls this out). Not needed now — the
  recorder is the sole consumer; wiring a `MultiWriter` for one writer would be
  speculative complexity.
- **Install / rebuild.** Hand to the operator (inode-cache gotcha + billing
  deadline). Rollback is trivial: unset `PYRY_RECORD_DIR`.
- **Knowledge-base doc** (`docs/knowledge/codebase/552.md`) — owned by the
  documentation phase, not this ticket.

---

## Open questions

None blocking. The three real design forks are decided above and recorded so they
aren't reopened: (1) env read inside `Run`, not a `Config` field; (2) cols/rows
from `tuidriver.DefaultPtyCols/Rows`; (3) fail-fast on setup, best-effort on prune
+ rename.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No new boundary. `cfg.SessionID` becomes a filename
  component, but the existing code already trusts it as a path component
  (`tuidriver.SessionJSONLPath(home, workdir, cfg.SessionID)`, runner.go:335) and
  as `--session-id` argv. Production mints it via `sessions.NewID` (`crypto/rand`
  UUIDv4). `PYRY_RECORD_DIR` is operator-set on a single-user CLI; no network or
  remote-attacker input reaches this code. The recorder reuses pre-existing
  trust, it does not widen it.
- **[Tokens / secrets]** The core property: a `.cast` may contain full session
  content (prompt + claude output + ALL tool output, incl. file contents and
  possible secrets). Mitigated in-design: mode `0600` (owner-only), `*.cast`
  gitignored, **OFF by default** (only on explicit `PYRY_RECORD_DIR`), flag-site
  warning, and a default location guided outside synced/backed-up paths. No
  encryption/hashing — by design (the artifact must stay `asciinema play`-able);
  the threat model is a local opt-in debug aid on the operator's own machine.
  No tokens generated; SessionID (a UUID, already in argv/logs) is not a secret.
- **[File operations]** No MUST-FIX. Create uses `O_CREATE|O_EXCL|O_WRONLY,0o600`
  (O_EXCL defeats a create-time symlink/pre-create swap); dir is `MkdirAll 0o700`;
  close+rename is a same-dir atomic rename. Prune's `os.Remove` does not follow a
  symlink on the final path component, so a swapped `*.cast → /target` symlink
  deletes the link, not the target. The stat-then-remove TOCTOU is confined to an
  operator-owned `0700` dir — an actor who can swap files there already holds the
  operator's privileges (accepted by the single-user CLI threat model).
- **[Subprocess execution]** N/A — no new `exec`; claude's argv (`buildArgs`) is
  unchanged; `dir`/`PYRY_RECORD_DIR` never reaches a command line; no `sh -c`.
- **[Cryptographic primitives]** N/A — no crypto introduced. The filename
  timestamp (`time.Now().UTC()`) is a sortable label, not a nonce/secret; no RNG
  is used for a security purpose in this change.
- **[Network & I/O]** N/A — local file writes only, no sockets. Per-`.cast` size
  is bounded by claude's own session length (not attacker-controllable);
  cross-run accumulation is bounded by the 7-day prune; total is bounded by the
  operator's disk.
- **[Error messages / logs]** No findings. Wrapped errors carry op name + path +
  I/O errno (no content); Warn lines (prune/rename failures) carry `err` + path
  only. The package's no-content-logging discipline is preserved by an explicit
  carve-out — no prompt bytes, buffer substrings, or `.cast` contents enter any
  `slog` line. No telemetry.
- **[Concurrency]** No findings. No new locks or goroutines. The recorder is
  driven by tui-driver's existing single reader goroutine; the `*os.File`
  close/rename in `finalizeRecording` is ordered strictly after that goroutine
  exits via `<-s.readerDone` inside `sess.Close()` (happens-before) — the
  defer-LIFO placement guarantees finalize runs last. Verified under `go test
  -race`. On SIGKILL no defer runs and a session-tagged temp remains (valid cast
  prefix, recoverable) — AC-intended.
- **[Threat model alignment]** In-scope threat = sensitive session content
  written to disk; mitigated as above. No relay/mobile wire surface is touched
  (`docs/protocol-mobile.md` § Security model is not engaged — recordings are
  local-only, never transmitted). OUT OF SCOPE: hardening `cfg.SessionID` against
  a path-bearing value (a pre-existing assumption shared with the JSONL-path code,
  not introduced here) — would be a cross-cutting `ValidID`-gate ticket if
  ptyrunner ever accepts externally-supplied SessionIDs.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-05-31
</content>
</invoke>
