# Spec #860 — e2e harness: bind the daemon control socket under a short path (macOS sun_path 104-byte limit)

**Size:** S (near XS). One production file, one test file. No edit fan-out.
**Security review:** not required — ticket is not `security-sensitive`. Test-harness plumbing, no untrusted input, no new wire surface.

## Files to read first

- `internal/e2e/harness.go:464-515` — `spawnWith`, the shared spawn core. Line 478 (`socket := filepath.Join(home, "pyry.sock")`) is the *only* production change. Everything downstream (`args`, the returned `socket`, `SocketPath`) is unchanged.
- `internal/e2e/harness.go:185-221` — `Start`/`StartIn`. `Start(t)` → `StartIn(t, t.TempDir())`; `home` = long `t.TempDir()`. This is the latent path AC-1 fixes.
- `internal/e2e/harness.go:744-766` — `teardown`. Registered via `t.Cleanup` *after* the helper's dir-cleanup, so it runs *first* (LIFO): daemon dies before the socket dir is removed. Confirm the ordering claim in § Cleanup.
- `internal/e2e/harness.go:396-427` — `StartExpectingFailureIn`. Also routes through `spawn` → `spawnWith`, so it inherits the new derivation and its `t.Cleanup` dir-removal for free. Its own `os.Remove(socket)` stays (removes the file; dir removed by cleanup).
- `internal/control/server_test.go:126` — `shortTempDir`: the canonical `os.MkdirTemp("/tmp", …)` + `t.Cleanup(os.RemoveAll)` recipe. Copy this shape.
- `cmd/pyry/rekey_test.go:53` — `shortSockTempDir`: second instance of the same recipe. Confirms `/tmp` base is the established convention for a short socket dir under a longer HOME.
- `internal/e2e/restart_test.go:31-49` — `newRegistryHome`: the *home-based* workaround (`os.MkdirTemp("", "pyry-rs-*")`). Do **not** copy this shape — it shortens HOME, which AC-1 forbids. Read it only to see why the ticket keeps HOME long and decouples the socket instead.
- `docs/lessons.md:207-211` — the sun_path lesson (104 macOS / 108 Linux; `bind(2)` → `EINVAL` → "ready-deadline exceeded"). The failure mode this ticket removes.
- `internal/e2e/realclaude/fixtures.go:1` + `fixtures.go:323-` — the `//go:build e2e_realclaude` boundary and the "duplicates deliberately — disjoint build tags block reuse" convention. Governs the cross-harness sharing decision (§ Cross-harness sharing).

## Context

`spawnWith` derives the daemon control socket as `filepath.Join(home, "pyry.sock")` (`harness.go:478`), where `home` is `t.TempDir()` for the common `Start(t)` path. On macOS `t.TempDir()` resolves under `/var/folders/<hash>/T/<TestName>/NNN/` — a long path that embeds the sanitised test name. For a long-enough test name, `<home>/pyry.sock` exceeds macOS's 104-byte `sockaddr_un.sun_path` limit, `bind(2)` returns `EINVAL`, the daemon can't bind its control listener, and the harness reports "not ready within 5s".

Four tests already dodge this ad hoc (`restart_test.go`, `auto_attach.go`, `attach_pty.go`, `fakeclaude_test.go`) by allocating a short-named HOME via `os.MkdirTemp`. Plain `Start(t)` has no such protection and is latent for any new long-named real-daemon test. Surfaced live 2026-07-08 while building #854's interactive real-claude test. Fixing the derivation once inside `spawnWith` makes every `Start`/`StartIn`/`StartRotation*`/`StartExpectingFailureIn` spawn sun_path-safe without each test re-inventing the workaround. #854 is blocked by this ticket.

## Design

### The derivation

Decouple the socket path from HOME. HOME stays `t.TempDir()` (long); the socket moves to a short directory under `/tmp` whose length is bounded independent of the test name.

Add one package-internal helper to `harness.go`:

```go
// shortSocketPath returns a control-socket path short enough to stay under
// macOS's 104-byte sun_path limit regardless of the test name. HOME can be a
// long t.TempDir(); the socket is decoupled to a fresh /tmp dir (~13 bytes via
// /private/tmp) so the total path is ~40 bytes. Registers t.Cleanup to remove
// the containing dir (and the socket inside it).
func shortSocketPath(t *testing.T) string
```

Behavior contract:
- `dir, err := os.MkdirTemp("/tmp", "pyry-sock-*")`; `t.Fatalf` on error (matches `shortTempDir`/`shortSockTempDir`).
- `t.Cleanup(func() { _ = os.RemoveAll(dir) })` — removes the dir *and* the socket file inside it.
- Returns `filepath.Join(dir, "pyry.sock")`.

`/tmp` base (not `os.MkdirTemp("", …)`): on macOS the default temp root is `/var/folders/…` (~50 chars) which the ticket flags as possibly-not-short-enough for a decoupled-but-still-nested path; `/tmp` → `/private/tmp` (~13 chars) gives maximum headroom and matches the two existing socket-dir helpers (`shortTempDir`, `shortSockTempDir`). Established, CI-proven pattern.

### The one-line wiring

In `spawnWith`, replace `harness.go:478`:

```go
socket := filepath.Join(home, "pyry.sock")   // before
socket := shortSocketPath(t)                  // after
```

Nothing else in `spawnWith` changes. `socket` still flows into `-pyry-socket=<socket>` (`harness.go:484`) and is still returned as the first value; `Harness.SocketPath` still carries it verbatim. The daemon binds exactly where told, so this is a pure change to *what path the harness hands it* — HOME (used for `-pyry-workdir=home` and `childEnv(home)`'s `HOME=home`) is untouched, satisfying AC-1's "HOME stays `t.TempDir()`; only the socket path is decoupled."

Because `spawn`, `StartExpectingFailureIn`, `StartIn`, `StartInWithEnv`, `StartRotation`, and `StartRotationWithRelay` all funnel through `spawnWith`, all six become sun_path-safe from this single edit. No call site changes.

### Data flow (unchanged shape, new socket origin)

```
Start(t) ─► StartIn(t, t.TempDir())        HOME = long /var/folders/.../TestName/NNN/
                    │
                    ▼
              spawnWith(t, home)
                    │
   socket ◄── shortSocketPath(t)            socket = /private/tmp/pyry-sock-XXXX/pyry.sock  (~40 bytes)
                    │
   -pyry-socket=<socket> ─► pyry binds control listener  ── bind(2) succeeds (was EINVAL)
                    │
   return socket ─► Harness.SocketPath ─► tests dial h.SocketPath (unchanged field)
```

## Cross-harness sharing (AC-4)

AC-4 requires the derivation be "factored so it can be reused, not inlined at each spawn site," with the fake-daemon harness as its first consumer. The `shortSocketPath` helper satisfies this: `spawnWith` calls it rather than inlining `filepath.Join(home, "pyry.sock")`, and every other spawn site in package `e2e` funnels through `spawnWith`.

**#854's real-claude harness (`internal/e2e/realclaude/`) is out of scope for this ticket** — AC-4 states it explicitly ("This ticket does not add a daemon spawn to `internal/e2e/realclaude/`"). That harness runs `pyry agent-run` (one-shot, no control socket) today.

**Sharing mechanism: documented recipe, duplicated across the tag boundary — not a shared package.** `internal/e2e/harness.go` is `//go:build e2e || e2e_install`; `internal/e2e/realclaude/fixtures.go` is `//go:build e2e_realclaude`, a *separate package* that already duplicates `ensurePyryBuilt` deliberately (`fixtures.go:323` comment: "disjoint build tags block reuse"). A single `.go` file cannot belong to two packages, so genuine reuse would require a third tag-neutral package for a ~10-line helper — over-engineering that contradicts the repo's established "duplicate deliberately across the tag boundary" convention. When #854 introduces its own control socket in the realclaude package, it duplicates the `shortSocketPath` recipe there, consistent with how `ensurePyryBuilt` is already duplicated. This ticket does not create that duplicate. Keeping it out holds this ticket at one production file.

## Cleanup (AC-3)

No leaks under the OS temp root:
- `shortSocketPath` registers `t.Cleanup(os.RemoveAll(dir))` — removes the socket dir and the socket file inside it.
- **Cleanup ordering is correct by LIFO.** In `StartIn`, `shortSocketPath`'s cleanup registers *first* (inside `spawn`→`spawnWith`, before `StartIn` registers `h.teardown`). `t.Cleanup` runs LIFO, so `teardown` fires first — SIGTERM/SIGKILL the daemon and wait for `doneCh` (process exit) — *then* `os.RemoveAll(dir)` runs against a dead daemon. The socket dir is never removed out from under a live listener.
- `StartExpectingFailureIn` inherits the same cleanup (it calls `spawn`). Its existing `os.Remove(socket)` removes only the file; the dir is removed by the registered cleanup. No leak on the failure path either.
- `teardown`'s existing best-effort `os.Remove(h.SocketPath)` (`harness.go:764`) stays — harmless and still the SIGKILL-path fallback for the socket file; the dir removal is now the guaranteed cleanup.

## Error handling

- `os.MkdirTemp("/tmp", …)` failure → `t.Fatalf` (a test-infra failure, not a daemon failure). Matches `shortTempDir`/`shortSockTempDir`.
- No new daemon-side failure modes: the daemon still binds wherever `-pyry-socket=` points; the only change is that the pointer is now short.

## Testing strategy

**Regression guard (AC-2)** — a new test in a new file `internal/e2e/socket_path_test.go` (package `e2e`; keeps the guard self-documenting and isolated. Appending to `harness_test.go` is equally acceptable — developer's call):

- **Test name is deliberately long** — long enough that `<t.TempDir()>/pyry.sock` overflows macOS's 104-byte `sun_path` when derived the old way. Document the target in a comment (the pre-fix `t.TempDir()` path on macOS is `/var/folders/<hash>/T/<TestName>/NNN/pyry.sock`; a ~60+ char `TestE2E_…` name pushes it over 104). The long name *is* the fixture.
- **Body:** `h := Start(t)`. `Start` blocks on `waitForReady`, which only returns once the control socket is bound and dialable — so reaching the next line means the daemon bound the socket.
- **Assertions:**
  - `len(h.SocketPath) <= 104` — the deterministic, cross-platform invariant. This is what "fails against the current derivation" means concretely: pre-fix on macOS, `h.SocketPath` is the long `t.TempDir()`-nested path (> 104); post-fix it's the short `/tmp` path (≤ 104).
  - `net.Dial("unix", h.SocketPath)` succeeds, then close — AC-2's "binds and is dialable" made explicit (redundant with `Start`'s readiness gate, but states the contract).
  - Optionally `filepath.Dir(h.SocketPath) != h.HomeDir` — pins the decoupling (socket no longer lives under HOME).
- **Verifying "fails against the current derivation" (AC-2):** the developer confirms by temporarily reverting `harness.go:478` to `filepath.Join(home, "pyry.sock")` and observing the guard fail *on macOS* (readiness timeout → `Start`'s `t.Fatalf`, or the length assertion if run past it). The behavioral overflow is macOS-only (Linux allows 108 and `/tmp`-rooted paths are shorter); the `len(...) <= 104` assertion is the deterministic invariant that holds post-fix on all platforms and is the durable guard against re-introduction.

**Existing suite** — run `go test -tags=e2e ./internal/e2e/...` (and `-tags=e2e_install` if touched). Every existing test funnels through `spawnWith`, so the whole e2e suite exercises the new derivation. No existing test computes `<home>/pyry.sock` itself (verified: the only `filepath.Join(home,"pyry.sock")` sites are the harness production files; tests dial `h.SocketPath`), so none breaks on the decoupling. The `install_*_test.go` references to `<home>/.pyry/<name>.sock` are the *installed-service* default socket path (a different daemon-owned location), not the `-pyry-socket=` override, and are untouched.

## Open questions

- **Migrate the four existing ad-hoc workarounds to `shortSocketPath`?** No — out of scope. `restart_test.go`/`auto_attach.go`/`attach_pty.go`/`fakeclaude_test.go` shorten HOME (not just the socket) and already work; migrating them churns extra files for no behavioral gain and would touch `auto_attach.go`/`attach_pty.go`'s own `filepath.Join(home,"pyry.sock")` sites. Simplicity-first: fix the latent `spawnWith` path only. A follow-up could unify them onto `shortSocketPath` later if desired, but there's no observed failure demanding it.

## Acceptance criteria

- [ ] `spawnWith` (`internal/e2e/harness.go`) derives the daemon control socket via a factored `shortSocketPath(t)` helper that binds it at a `/tmp`-rooted short path, bounded under macOS's 104-byte `sun_path` limit regardless of test name. HOME stays `t.TempDir()`; only the socket path is decoupled. `Start(t)` is sun_path-safe automatically.
- [ ] Regression guard: a new test with a deliberately long name brings the daemon up via `Start(t)` and asserts `len(h.SocketPath) <= 104` and that the socket is dialable. The guard fails against the current `filepath.Join(home, "pyry.sock")` derivation (verified by temporary revert on macOS) and passes after the fix.
- [ ] The short-socket directory is cleaned up on test end via `t.Cleanup(os.RemoveAll)`; no sockets or socket directories leak under `/tmp`. Cleanup ordering (teardown-then-dir-removal) is preserved by LIFO registration.
- [ ] The socket-path derivation is factored into `shortSocketPath`, reused by `spawnWith` (its first consumer) rather than inlined. `internal/e2e/realclaude/` is not modified — #854 duplicates the recipe when it introduces its own control socket, consistent with the package's existing deliberate-duplication convention.
- [ ] `go test -tags=e2e ./internal/e2e/...` passes; `go vet ./...` clean.
