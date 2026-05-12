# #261 — e2e: `pyry update` failure paths (fetch / verify / broken binary)

## Files to read first

- `cmd/pyry/update_e2e_test.go:36-44` — the `e2eUpd*` constants (`e2eUpdReadyDeadline`, `e2eUpdRunTimeout`, etc.). New tests reuse all of them; no new constants required.
- `cmd/pyry/update_e2e_test.go:46-50` — `runResult` struct. Reuse verbatim.
- `cmd/pyry/update_e2e_test.go:198-258` — `buildPyryBinE2E`, `copyFileE2E`, `inodeOfE2E`, `childEnvE2E`. The new `buildBrokenPyryBinE2E` mirrors `buildPyryBinE2E` exactly (one-line target swap + env-var rename).
- `cmd/pyry/update_e2e_test.go:260-337` — `spawnDaemonE2E`, `waitForSocketE2E`, `stopDaemonE2E`. The broken-binary test reuses `spawnDaemonE2E` for its kill-respawn closure; the wait-goroutine + doneCh pattern is what surfaces the broken child's early exit as `"daemon exited before ready"`.
- `cmd/pyry/update_e2e_test.go:57-196` — `TestUpdate_HappyPath_E2E`. The pre-update install + daemon-spawn + inode/PID capture block (lines 57–91) is the boilerplate the three new tests share via a small helper. The `runRestart` closure (lines 109–125) is the structural twin of the broken-binary test's closure.
- `cmd/pyry/update_test.go:23-85` — `buildTarGzForTest`, `fakeRelease`, `newFakeReleaseServer`. Reused as-is by the verify-failure test (pass a deliberately-wrong checksums body). Not used by the fetch-failure test — see § Fetch failure.
- `cmd/pyry/update_test.go:90-143` — `TestUpdate_Success`. The shape of the zero-`RestartProbe` + fatal-`runRestart` pair (lines 112–116) is what the fetch-failure and verify-failure tests copy verbatim.
- `cmd/pyry/update_test.go:438-460` — `TestUpdate_RestartFailure`. Pins the exact error-message substrings (`"binary replaced to v…"` + `"daemon restart failed"`) and the no-success-line assertion the broken-binary test mirrors end-to-end.
- `cmd/pyry/update.go:111-209` — `doUpdate`. Lines 161–179 are the fetch + verify path that returns before AtomicReplace. Lines 188–204 are the replace + restart path where the broken-binary case fails. Line 202's error format (`"update: binary replaced to %s, but daemon restart failed: %w"`) is what AC#4's substring assertion targets.
- `internal/update/replace.go:27-64` — `AtomicReplace`. The temp-file pattern (`.<base>.*.tmp` created in `filepath.Dir(targetPath)`) and the defer-remove-on-error contract — confirms the "no stragglers" assertion is structurally guaranteed when AtomicReplace is reached, and trivially true when fetch/verify fail before AtomicReplace runs.
- `internal/update/fetch.go` — `FetchAsset` returns an error on non-2xx HTTP responses. That error becomes `"update: download tarball: …"` per `update.go:163-165`, which is what the fetch-failure test asserts a non-nil return from `doUpdate` for. (Read once to confirm the error shape; the test doesn't assert the message text.)
- `internal/update/checksum.go` — `ParseChecksumsFile` + `VerifySHA256`. The verify-failure path returns `update.ErrChecksumMismatch` (wrapped as `"update: verify checksum: …"`). Test asserts non-nil error only — no substring pin.
- `docs/knowledge/features/pyry-update-command.md` — the no-rollback design is documented here. The broken-binary test's inline comment cites this doc as the recovery-path reference.
- `docs/specs/architecture/187-update-atomic-replace.md` — the same no-rollback principle from the AtomicReplace design side.
- `docs/lessons.md` § "Reject hidden env vars added 'just for the test'" — load-bearing for the brokenpyry helper's `PYRY_E2E_BROKEN_BIN` env-var: it short-circuits a `go build` in CI, never alters production behaviour.

## Context

#260 landed the happy-path e2e test plus the reusable scaffolding (`spawnDaemonE2E`, `waitForSocketE2E`, `stopDaemonE2E`, `runVerbE2E`, `buildPyryBinE2E`, `inodeOfE2E`, `copyFileE2E`, `childEnvE2E`, the `runResult` struct, and the `e2eUpd*` timing constants). With those helpers in place, the three failure-path tests in this ticket are mostly an exercise in wiring: each test reuses #260's spawn/dial/teardown machinery to set up a pre-update daemon, drives `doUpdate` against a server tailored to inject one failure, and asserts a small set of structural properties.

The broken-binary case is the most interesting of the three. By design `pyry update` has no rollback (see `docs/knowledge/features/pyry-update-command.md` and spec #187): once `AtomicReplace` swaps the new bytes into place, the old binary is gone. If the new binary is broken, the operator must intervene. The unit-shaped precedent for the error contract (`TestUpdate_RestartFailure` at `cmd/pyry/update_test.go:438-460`) pins the message shape (`"binary replaced to v…"` + `"daemon restart failed"`); this ticket extends that contract end-to-end against a real spawned-and-immediately-dead child process.

## Design

### Same file, same build tag, three test functions

```
cmd/pyry/update_e2e_test.go    (APPEND — new tests + helpers; existing file)
internal/brokenpyry/main.go    (NEW — broken-pyry helper, see § brokenpyry below)
```

Build tag stays `(darwin || linux) && e2e_update`. Run with the same command #260 wired:

```bash
go test -tags=e2e_update -count=1 ./cmd/pyry/...
```

The three tests are siblings of `TestUpdate_HappyPath_E2E` in the same file, in `package main`. No package split, no second build tag.

### Shared pre-update setup helper

The first ~30 lines of each failure-path test repeat #260's pre-update install + spawn + inode/PID capture block (`update_e2e_test.go:57-91`). Extract that block to a tiny helper:

```go
// preUpdateState bundles the pre-update daemon's identity. Each failure-path
// test asserts at least two of these fields didn't change.
type preUpdateState struct {
    targetPath  string
    home        string
    socket      string
    inodeBefore uint64
    pidBefore   int
    cmd1        *exec.Cmd
    done1       chan struct{}
    stdout1     *bytes.Buffer
    stderr1     *bytes.Buffer
}

// installPreUpdateDaemonE2E installs srcBin → <home>/bin/pyry, spawns it,
// waits for the socket, and captures the pre-update inode + PID. Failure
// modes fatal the test. Caller is responsible for stopping cmd1 — either
// directly via stopDaemonE2E (broken-binary test) or via t.Cleanup
// (fetch/verify failures, where daemon 1 outlives the doUpdate call).
func installPreUpdateDaemonE2E(t *testing.T) *preUpdateState {
    ...
}
```

The happy-path test does NOT switch to this helper — the spec stays append-only (#260 just landed; no regression risk by leaving it). The helper exists for the three new tests.

### Test outlines

All three follow the same skeleton:

```go
//go:build (darwin || linux) && e2e_update
// (already at file top — same file, same tag)

func TestUpdate_FetchFailure_E2E(t *testing.T) {
    s := installPreUpdateDaemonE2E(t)

    // One-off httptest.Server. fakeRelease/newFakeReleaseServer has no
    // failure-injection knob and growing one for a single caller is not
    // worth it (the AC body documents this trade explicitly).
    srv := newFetchFailReleaseServer(t, "v999.0.0")

    var out bytes.Buffer
    err := doUpdate(t.Context(), updateOptions{
        currentVersion: "0.0.1",
        goos:           runtime.GOOS,
        goarch:         runtime.GOARCH,
        repo:           "pyrycode/pyrycode",
        releaseBaseURL: srv.URL + "/releases/download",
        fetcher:        &update.Fetcher{BaseURL: srv.URL, UserAgent: "pyry/test"},
        executablePath: func() string { return s.targetPath },
        replace:        update.AtomicReplace,
        out:            &out,
        probeRestart:   func() update.RestartProbe { return update.RestartProbe{} },
        runRestart: func(context.Context, []string) error {
            t.Fatalf("runRestart must not fire on fetch-failure path")
            return nil
        },
    })
    if err == nil {
        t.Fatalf("doUpdate: expected error, got nil; output:\n%s", out.String())
    }

    assertBinaryUnchangedE2E(t, s)
    assertDaemonAliveE2E(t, s)
    assertNoStragglersE2E(t, s)
    assertNoSuccessLineE2E(t, &out, "v999.0.0")
}

func TestUpdate_VerifyFailure_E2E(t *testing.T) {
    s := installPreUpdateDaemonE2E(t)

    // fakeRelease produces a correctly-keyed checksums body. Swap the body
    // for one that lists a deliberately-wrong digest for the same asset
    // (any 64-hex value that isn't sha256(tgz) works). The server still
    // hands out the (genuine) tarball, so the failure is a verify error,
    // not a download error.
    newBytes := []byte("\x7fELF...does-not-matter...")
    asset, tgz, _ := fakeRelease(t, "v999.0.0", runtime.GOOS, runtime.GOARCH, newBytes)
    bogusSums := fmt.Sprintf("%64s  %s\n", "0", asset) // 64 zeros
    srv := newFakeReleaseServer(t, "v999.0.0", asset, tgz, []byte(bogusSums))

    var out bytes.Buffer
    err := doUpdate(t.Context(), updateOptions{
        // ... same wiring as fetch-failure ...
        probeRestart:   func() update.RestartProbe { return update.RestartProbe{} },
        runRestart: func(context.Context, []string) error {
            t.Fatalf("runRestart must not fire on verify-failure path")
            return nil
        },
    })
    if err == nil { t.Fatalf(...) }

    assertBinaryUnchangedE2E(t, s)
    assertDaemonAliveE2E(t, s)
    assertNoSuccessLineE2E(t, &out, "v999.0.0")
    // Stragglers check omitted here: verify-failure exits doUpdate *before*
    // AtomicReplace runs, so there is no plausible source of stragglers
    // beyond what fetch-failure already covers. One assertion is enough.
}

func TestUpdate_BrokenNewBinary_E2E(t *testing.T) {
    s := installPreUpdateDaemonE2E(t)

    brokenBin := buildBrokenPyryBinE2E(t)
    brokenBytes, err := os.ReadFile(brokenBin)
    if err != nil { t.Fatalf("read brokenBin: %v", err) }

    asset, tgz, sums := fakeRelease(t, "v999.0.0", runtime.GOOS, runtime.GOARCH, brokenBytes)
    srv := newFakeReleaseServer(t, "v999.0.0", asset, tgz, []byte(sums))

    var (
        cmd2    *exec.Cmd
        stdout2 *bytes.Buffer
        stderr2 *bytes.Buffer
        done2   chan struct{}
    )
    cmd1Stopped := false
    runRestart := func(_ context.Context, _ []string) error {
        if err := stopDaemonE2E(s.cmd1, s.done1, s.socket); err != nil {
            return fmt.Errorf("stop daemon 1: %w", err)
        }
        cmd1Stopped = true
        cmd2, stdout2, stderr2, done2 = spawnDaemonE2E(t, s.targetPath, s.home, s.socket)
        // The broken binary writes BROKEN_PYRY_TOKEN to stderr then
        // os.Exit(1) — waitForSocketE2E's doneCh short-circuit catches the
        // early exit and returns "daemon exited before ready". That error
        // propagates as the "daemon restart failed" half of the asserted
        // doUpdate error message.
        return waitForSocketE2E(s.socket, done2, e2eUpdReadyDeadline)
    }

    var out bytes.Buffer
    err = doUpdate(t.Context(), updateOptions{
        // ... same wiring as happy-path: managed-unit probe ...
        probeRestart: func() update.RestartProbe {
            return update.RestartProbe{
                LaunchdPlistExists: runtime.GOOS == "darwin",
                SystemdUnitExists:  runtime.GOOS == "linux",
                UID:                strconv.Itoa(os.Getuid()),
            }
        },
        runRestart: runRestart,
    })

    // Recovery-path note: pyry update has no rollback. Once AtomicReplace
    // swaps the new bytes into place, the old binary is gone — operator
    // intervention is the only recovery. See
    // docs/knowledge/features/pyry-update-command.md.
    if err == nil {
        t.Fatalf("doUpdate: expected error, got nil; output:\n%s", out.String())
    }
    msg := err.Error()
    if !strings.Contains(msg, "binary replaced to ") {
        t.Errorf("error must mention 'binary replaced to ': %v", err)
    }
    if !strings.Contains(msg, "daemon restart failed") {
        t.Errorf("error must mention 'daemon restart failed': %v", err)
    }

    // The cleanup for cmd1 mirrors happy-path: only stop if runRestart
    // didn't already do it. cmd2 cleanup mirrors happy-path verbatim.
    t.Cleanup(func() {
        if !cmd1Stopped {
            _ = stopDaemonE2E(s.cmd1, s.done1, s.socket)
        }
        if cmd2 != nil {
            _ = stopDaemonE2E(cmd2, done2, s.socket)
        }
    })

    // AC#4 — AtomicReplace happened: inode changed AND the on-disk bytes
    // are the broken bytes.
    inodeAfter := inodeOfE2E(t, s.targetPath)
    if inodeAfter == s.inodeBefore {
        t.Errorf("inode unchanged: AtomicReplace did not run; inode=%d", s.inodeBefore)
    }
    got, readErr := os.ReadFile(s.targetPath)
    if readErr != nil {
        t.Fatalf("read targetPath: %v", readErr)
    }
    if !bytes.Equal(got, brokenBytes) {
        t.Errorf("on-disk binary is not the broken bytes (size got=%d want=%d)", len(got), len(brokenBytes))
    }

    // Diagnostic guard: the broken helper's stderr must contain its token.
    // If a future change spawns something else, this assertion localizes
    // the failure cleanly instead of leaving the developer chasing a
    // generic "daemon exited before ready".
    if stderr2 == nil || !bytes.Contains(stderr2.Bytes(), []byte("BROKEN_PYRY_TOKEN")) {
        var got []byte
        if stderr2 != nil { got = stderr2.Bytes() }
        t.Errorf("broken pyry stderr missing BROKEN_PYRY_TOKEN; got: %q", got)
    }

    assertNoSuccessLineE2E(t, &out, "v999.0.0")
}
```

### Helper inventory (additions)

All `_test.go`-scoped, behind the `e2e_update` build tag, in the same file.

| Helper | Lines | Purpose |
|---|---|---|
| `preUpdateState` (struct) | ~10 | Bundle for `installPreUpdateDaemonE2E`'s return value. |
| `installPreUpdateDaemonE2E(t) *preUpdateState` | ~25 | Install + spawn + waitForSocket + capture inode/PID. Fatals on any failure. |
| `newFetchFailReleaseServer(t, version) *httptest.Server` | ~20 | Standalone fake server. `/repos/pyrycode/pyrycode/releases/latest` returns the version JSON (so the `latest` fetch succeeds and the AssetName/URL derivation runs); the asset download URL returns HTTP 500 (so `FetchAsset` returns a wrapped error). The checksums URL is wired but never hit because the asset download fails first. |
| `buildBrokenPyryBinE2E(t) string` | ~15 | `go build -o <tmp>/brokenpyry github.com/pyrycode/pyrycode/internal/brokenpyry`; honours `PYRY_E2E_BROKEN_BIN`. Same shape as `buildPyryBinE2E`. |
| `assertBinaryUnchangedE2E(t, s)` | ~5 | `inodeOfE2E(s.targetPath) == s.inodeBefore`. |
| `assertDaemonAliveE2E(t, s)` | ~10 | (a) `s.cmd1.Process` is still findable via `os.FindProcess` + `Signal(syscall.Signal(0))` — non-fatal probe — and (b) `pyry status` against `s.socket` returns exit 0. The status check is the structural assertion; the signal probe is the cheap pre-check that localizes "process died unrelated to update" cleanly. |
| `assertNoStragglersE2E(t, s)` | ~10 | `os.ReadDir(filepath.Dir(s.targetPath))` returns exactly one entry whose name is `"pyry"`. No `.pyry.*.tmp` files survived. |
| `assertNoSuccessLineE2E(t, out, version)` | ~5 | `!strings.Contains(out.String(), "==> Updated to "+version+".")`. |

Total new helper lines: ~100. Three test bodies: ~120 lines combined. Plus brokenpyry helper (~10 lines). Combined new source: **~230 lines**. Within the S threshold for a test-heavy ticket.

### `brokenpyry` location and shape

**Location:** `internal/brokenpyry/main.go`.

**Justification:** A `package main` directory cannot be imported by other Go code, so the visibility tightening that `cmd/pyry/internal/brokenpyry/` would provide over plain `internal/brokenpyry/` does not apply (the directory path is just a build target string for `go build`). Choosing the shorter, flatter path matches the existing repo shape (`internal/e2e/`, `internal/install/`, `internal/update/`) and keeps the import path the test passes to `go build` short and obvious.

**Contents** (entire file):

```go
// Package main is a deliberately-broken pyry stand-in for the
// TestUpdate_BrokenNewBinary_E2E test. Build target:
//   go build -o /tmp/brokenpyry github.com/pyrycode/pyrycode/internal/brokenpyry
//
// On every invocation, it writes a recognizable token to stderr and exits
// non-zero. The token lets the e2e test localize "broken helper ran" from
// "some other binary exited early" cleanly.
package main

import (
    "fmt"
    "os"
)

func main() {
    fmt.Fprintln(os.Stderr, "BROKEN_PYRY_TOKEN: broken pyry stand-in exiting non-zero")
    os.Exit(1)
}
```

That is the entire file. No flag handling, no signal handling, no `-pyry-socket=` parsing — the binary exits before any of that would run, which is exactly the failure mode the test exercises.

### Fetch failure: why a one-off `httptest.Server`, not `newFakeReleaseServer`

`newFakeReleaseServer` always serves the asset and the checksums file successfully. Adding a failure-injection knob (e.g. `assetStatus int`) for a single caller would grow its signature and contaminate the four happy-path tests in `update_test.go` that call it today. The principled alternative is a parallel constructor that wires the same `/repos/.../releases/latest` route but returns HTTP 500 on the asset URL. ~20 lines, scoped to one caller, no helper-API churn.

The fetch-failure test specifically wants the *asset download* to fail, not the *latest-release* fetch — the AC says "release-asset download" returns 500. So:

```
GET /repos/pyrycode/pyrycode/releases/latest          → 200, {"tag_name":"v999.0.0"}
GET /releases/download/v999.0.0/<asset>               → 500
GET /releases/download/v999.0.0/checksums.txt         → (never hit; doUpdate returns first)
```

This shape exercises the `o.fetcher.FetchAsset(ctx, tarballURL)` error path at `update.go:162-165`.

### Verify failure: bogus checksums body, real tarball

`newFakeReleaseServer` accepts the checksums body as a parameter — no new helper needed. Pass a string of the shape `"000…0  <asset>\n"` (64 zeros, two spaces, asset name, newline) and the tarball download succeeds but `update.VerifySHA256` returns `ErrChecksumMismatch`, wrapped as `"update: verify checksum: …"` at `update.go:177-181`.

### Broken-binary: AtomicReplace + restart-into-broken

The broken-binary case runs the full happy-path wiring up to and including `AtomicReplace`. The seam where it diverges is in `runRestart`'s `waitForSocketE2E` call: the broken pyry exits non-zero before opening the socket, so the wait-goroutine closes `done2`, `waitForSocketE2E` returns `"daemon exited before ready"`, the closure propagates that error, and `doUpdate` wraps it at `update.go:202`:

```go
return fmt.Errorf("update: binary replaced to %s, but daemon restart failed: %w", targetVer, err)
```

The test asserts both substrings (`"binary replaced to "` and `"daemon restart failed"`). The version is `v999.0.0`, but the AC pins only the literal prefix `"binary replaced to "` — the assertion is intentionally version-agnostic, matching how `TestUpdate_RestartFailure` (the unit-shaped precedent at `cmd/pyry/update_test.go:438-460`) phrases it.

## Concurrency model

Identical to #260's happy-path test:

- One test goroutine per `Test*` function.
- One child process for daemon 1 (always alive at `doUpdate` time).
- For fetch-failure / verify-failure: no daemon 2; daemon 1 lives until `t.Cleanup` stops it.
- For broken-binary: daemon 2 is the broken helper, which exits immediately on spawn. The `spawnDaemonE2E` wait-goroutine closes `done2` essentially synchronously (within the broken binary's startup cost — microseconds). `waitForSocketE2E`'s `<-doneCh` short-circuit picks that up.
- `pyry status` (inside `assertDaemonAliveE2E`) runs as a short-lived `exec.CommandContext` against `s.socket`; its own wait goroutine is scoped to `cmd.Run()`. No leaks.

No fan-out, no errgroup, no shared state beyond the closure-captured `cmd2 / stdout2 / stderr2 / done2` pointers in the broken-binary test.

## Error handling

The three tests fail fast (`t.Fatalf`) on any unexpected nil error from `doUpdate` (fetch/verify/broken-binary all MUST return an error) and on any spawn / stat / read failure during setup or assertions. Diagnostic output dumps stderr buffers verbatim — same pattern as the happy-path test.

The broken-binary test's `t.Cleanup` MUST handle both `cmd1Stopped == false` (the closure never fired — e.g. AtomicReplace itself failed for a different reason) AND `cmd2 == nil` (spawn never happened). Both are guarded explicitly in the outline above.

## Testing strategy

The three new tests ARE the tests. No unit tests for the new helpers — they exist only to make the test bodies readable. `installPreUpdateDaemonE2E` is the first candidate for promotion if a fourth e2e_update test materializes, but per the AC body this is the last failure-path ticket in the series.

Run locally:

```bash
go test -tags=e2e_update -count=1 ./cmd/pyry/...
```

Pre-built short-circuit for CI:

```bash
PYRY_E2E_BIN=$(pwd)/pyry \
PYRY_E2E_BROKEN_BIN=$(pwd)/brokenpyry \
  go test -tags=e2e_update ./cmd/pyry/...
```

Each test cleans up all artefacts under its own `<home>` (mkdtemp). Nothing escapes to `~/.pyry`, `~/Library/LaunchAgents`, `~/.config/systemd/user`, or any operator-owned path. The brokenpyry binary lives under the test's tempdir (`t.Cleanup` removes the dir).

## Open questions

- **Should `assertDaemonAliveE2E` also check that the PID is unchanged?** The AC says "PID unchanged" for fetch-failure and "daemon is untouched (PID unchanged)" for verify-failure. The structural assertion is already that `s.cmd1` is still the process answering the socket — `cmd1.Process.Pid` never mutates, and if a different process were now bound to the socket the dial would either fail (race with socket-file replacement) or talk to the wrong daemon. **Recommendation: assert it anyway**, as a one-liner — the AC explicitly mentions it, and the cost is one extra line per failure test. Wire it inside `assertDaemonAliveE2E` for free: `if s.cmd1.Process.Pid != s.pidBefore { ... }`. (The check is trivially true within the test's process model — `cmd1.Process.Pid` is captured at `Start()` and never changes — but the assertion is documentation that the test's invariant holds end-to-end.)
- **Should the fetch-failure test also assert the daemon is still answering on the socket, not just alive by signal?** Yes — `assertDaemonAliveE2E` is one helper that does both. Splitting them gains nothing.
- **Why not assert "no stragglers" for the broken-binary case too?** AtomicReplace ran successfully → its defer-remove path doesn't fire (it only removes the temp file on error before rename). Post-rename, the only file in `<home>/bin/` is `pyry` (the new bytes). The assertion would pass trivially — including it doesn't strengthen the test, and the inode + on-disk-bytes assertions already pin the post-replace state. **Recommendation: skip.**
- **Should `installPreUpdateDaemonE2E` register the `cmd1` cleanup itself?** Tempting, but the broken-binary test's `runRestart` closure stops `cmd1` mid-test — registering a cleanup inside the helper would force a `cmd1Stopped` flag visible to the closure, which couples the helper to the test's local control flow. **Recommendation: leave cleanup to the caller.** The fetch/verify tests register `stopDaemonE2E(s.cmd1, ...)` in `t.Cleanup` themselves (one line); the broken-binary test gates the cleanup on `cmd1Stopped` (already designed in the outline).

## Out of scope

Per the ticket body and lessons.md:

- A `--release-url` CLI flag or `PYRY_RELEASE_BASE_URL` env var (lessons.md "Reject hidden env vars added 'just for the test.'" — `doUpdate` is invoked from a same-package test instead).
- Rollback / `.bak` artefact behaviour. The broken-binary test asserts the *currently designed* contract (no rollback); a future ticket changing that contract owns the new assertions.
- Refactoring `TestUpdate_HappyPath_E2E` to use `installPreUpdateDaemonE2E`. Append-only.
- Cross-architecture or cross-OS scenarios. Test runs on darwin and linux only (build tag enforces).
- The post-restart smoke check (status / sessions list). That belongs to the happy path; the failure paths assert the failure shape, not the recovered state.
