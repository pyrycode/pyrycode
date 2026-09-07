# Rotation Primitive (`StartRotation`, `fakeclaude_test.go`, #123)

`StartRotation(t, home, sessionsDir, initialUUID, trigger) *Harness` is the
constructor that swaps `/bin/sleep 99999` for the [fake-claude
binary](fakeclaude-binary.md) (#122) so e2e tests can exercise pyry's
rotation watcher against a child that produces realistic JSONL behaviour.

Used today only by `TestE2E_StartRotation_PrimitiveWiresFakeClaude` (a
binary-boundary smoke test that does *not* touch
`internal/sessions/rotation`). No later ticket ever drove pyry's watcher
against this primitive — [#2137](https://github.com/pyrycode/pyrycode/issues/2137)
retired the watcher first. `StartRotation` and `StartRotationWithRelay`
stayed regardless: this test observes files on disk only, so it never
depended on the watcher it was originally staged for.

### What it wires

```
StartRotation(t, home, sessionsDir, initialUUID, trigger)
  │
  ├─ os.MkdirAll(sessionsDir, 0o700)         # auto-create
  ├─ ensureFakeClaudeBuilt(t)                # build (or reuse) fakeclaude
  └─ spawnWith(t, home, spawnOpts{
        claudeBin:  fakeBin,
        claudeArgs: []string{},
        extraFlags: []string{"-pyry-workdir=" + home},
        extraEnv: []string{
          "PYRY_FAKE_CLAUDE_SESSIONS_DIR=" + sessionsDir,
          "PYRY_FAKE_CLAUDE_INITIAL_UUID=" + initialUUID,
          "PYRY_FAKE_CLAUDE_TRIGGER="      + trigger,
        },
     })
```

`Harness.ClaudeSessionsDir` is populated to `sessionsDir`; left empty for
`Start` / `StartIn`.

### Why env vars on pyry, not flags

`supervisor.runOnce` does `cmd.Env = append(os.Environ(), s.cfg.helperEnv...)`
(`internal/supervisor/supervisor.go:234`). Setting the three
`PYRY_FAKE_CLAUDE_*` vars on pyry's `cmd.Env` flows them through to
fake-claude unchanged — no `helperEnv` knob, no supervisor changes. The
fake-claude binary's input surface is env-only by design (see
[fakeclaude-binary.md § Configuration](fakeclaude-binary.md)).

### `ensureFakeClaudeBuilt` — sibling of `ensurePyryBuilt`

```go
func ensureFakeClaudeBuilt(t *testing.T) string  // sync.Once + cached bin path
```

Mirrors `ensurePyryBuilt`: `sync.Once`-guarded `go build` into
`os.MkdirTemp("", "pyry-e2e-fakeclaude-*")`. `PYRY_E2E_FAKE_CLAUDE_BIN`
short-circuits to a pre-built binary on disk for CI prebuild.

### `spawnOpts` — shared spawn core

`spawn(t, home, extraFlags...)` and `StartRotation` both forward to a new
`spawnWith(t, home, spawnOpts) (socket, *exec.Cmd, *safeBuffer,
*safeBuffer, doneCh)` core (stdout/stderr buffers became `*safeBuffer` in #398 so tests can poll while `os/exec`'s pipe-copy goroutine still writes). `spawnOpts` zero-value yields the historical
`/bin/sleep 99999` behaviour, so `spawn` is now a one-liner over
`spawnWith`. Existing call sites (`StartIn`, `StartExpectingFailureIn`)
unchanged.

```go
type spawnOpts struct {
    claudeBin   string   // default "/bin/sleep"
    claudeArgs  []string // nil → {"99999"}
    extraEnv    []string // appended to childEnv(home)
    extraFlags  []string // appended after standard set, before `--`
}
```

`spawnOpts` is unexported — generalises cleanly when a third caller
appears, without committing to a public-surface shape today.

### `-pyry-workdir=<home>` is needed for the fake-claude path

Without `-pyry-workdir`, pyry's supervisor inherits the test process's
cwd; the supervised child's relative paths (and any production code that
encodes cwd into a path) drift away from the test's HOME. Pinning it to
`home` makes the test's view match pyry's view of "where the supervised
child is rooted." The flag exists today (`cmd/pyry/main.go:174-180`); no
production change.

### Test scope: primitive only

`TestE2E_StartRotation_PrimitiveWiresFakeClaude` (in
`internal/e2e/fakeclaude_test.go`, build tag `e2e`) verifies the wiring,
not pyry's rotation watcher:

1. `StartRotation` returns successfully (pyry came up; fake-claude opened
   its initial fd; readiness gate tripped).
2. `h.ClaudeSessionsDir == sessionsDir`.
3. Poll (5s deadline, 50ms gap) until `<sessionsDir>/<initialUUID>.jsonl`
   appears.
4. `os.WriteFile(trigger, nil, 0o600)`.
5. Poll until a *different* `<uuid>.jsonl` (matching `uuidV4Re`) appears
   in `sessionsDir` **and** is non-empty (`waitForRotatedJSONL`'s
   `Size() > 0` gate, #956 — closes a create-before-content race: fake-claude's
   `openSession` makes the rotated name visible via `os.OpenFile` before it
   writes the `{}\n` payload, and the file is opened `O_APPEND`/never
   truncated, so "size > 0" is a one-way latch once true).
6. Assert `os.Stat(rotated).Size() > 0` — now deterministically true given
   step 5's gate (previously racy: see [codebase/956.md](../codebase/956.md)).
   Combined with #122's strict close-OLD-before-open-NEW order, this implies
   the initial fd is no longer being written.

Deliberately does **not** assert on pyry's session registry, run
`/proc/<pid>/fd` probes, or drive `internal/sessions/rotation` — that
package no longer exists ([retired #2137](rotation-watcher.md)). The
on-disk-only scope this test chose turned out to be exactly right in
hindsight: it never needed updating when the watcher it was staged
ahead of was deleted instead of consumed.

### Why short-prefix `os.MkdirTemp` for HOME

Same `sun_path` budget rationale as the restart tests:
`TestE2E_StartRotation_PrimitiveWiresFakeClaude` is a long test name and
`t.TempDir()` would push `<home>/pyry.sock` past macOS's 104-byte limit.
`os.MkdirTemp("", "pyry-fc-*")` + `t.Cleanup(os.RemoveAll)` keeps the
prefix tiny.

### Production diff is zero

`-pyry-workdir`, the supervisor env-propagation, and fake-claude's env
contract all already shipped (#122 for the binary; pyry's flag/supervisor
are pre-Phase-1.0). #123 is harness + test diff: ~130 LOC across
`harness.go` and `fakeclaude_test.go`.
