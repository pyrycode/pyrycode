# Public API

Fourteen exported names — `Harness`, `Start`, `StartIn`, `StartInWithEnv`,
`StartRotation`, `StartRotationWithRelay`, `StartStreamInteractiveWithRelay`,
`StartExpectingFailureIn`, `(*Harness).Stop`, `RunResult`, `(*Harness).Run`,
`RunBare`, `RunBareIn`, `RunBareInWithEnv`, plus the struct fields:

```go
type Harness struct {
    SocketPath        string         // dial-able after Start returns
    HomeDir           string         // child's $HOME (registry, claude dir live underneath)
    ClaudeSessionsDir string         // populated by StartRotation; empty otherwise
    PID               int            // captured at spawn for leak verification
    Stdout            *safeBuffer    // mutex-guarded — safe to poll while daemon runs
    Stderr            *safeBuffer    // (#398: was *bytes.Buffer; raced os/exec pipe-copy)
}

func Start(t *testing.T) *Harness  // fail-fast: t.Fatalf on any error

// StartIn behaves like Start but uses the caller-supplied home directory
// instead of allocating a fresh t.TempDir(). Pre-populate it (e.g.
// <home>/.pyry/test/sessions.json) before calling to drive a daemon
// against a chosen on-disk state. Caller owns the directory's lifecycle.
//
// Optional extraFlags are appended to the standard test flag set before
// the `--` claude-arg sentinel. Go's flag package is last-wins, so
// `StartIn(t, home, "-pyry-idle-timeout=1s")` overrides the harness
// default of `=0` to enable idle eviction in-test.
func StartIn(t *testing.T, home string, extraFlags ...string) *Harness

// StartInWithEnv behaves like StartIn but also appends extraEnv (each
// "K=V") to the child's environment. extraFlags semantics are unchanged.
// Sibling helper for tests that need to inject env vars (e.g.
// PYRY_ALLOW_INSECURE_RELAY=1 for the #301 relay e2e tests) without
// disturbing every existing StartIn call site. Internally just calls
// spawnWith(t, home, spawnOpts{extraEnv, extraFlags}).
func StartInWithEnv(t *testing.T, home string, extraEnv []string, extraFlags ...string) *Harness

// Stop gracefully terminates the daemon (SIGTERM, grace, escalate to
// SIGKILL — same path as t.Cleanup teardown), waits for exit, and
// removes the socket. HomeDir is left intact. Idempotent with t.Cleanup
// teardown via sync.Once.
func (h *Harness) Stop(t *testing.T)

// StartExpectingFailureIn spawns pyry against the given home, expects it
// to exit before the readiness deadline elapses, and returns the captured
// exit code, stdout, and stderr. Fails the test if pyry instead becomes
// ready (control socket dialable) or if it neither exits nor becomes
// ready within the readiness deadline. No Harness is returned: there is
// no live daemon to drive, no socket to clean up.
func StartExpectingFailureIn(t *testing.T, home string) RunResult

// StartRotation spawns pyry with the fake-claude test binary
// (internal/e2e/internal/fakeclaude) as the supervised child, propagating
// the three PYRY_FAKE_CLAUDE_* env vars via cmd.Env so the supervisor
// inherits them through os.Environ() and forwards them to the PTY child.
// sessionsDir is auto-created with 0o700 if missing and recorded on
// h.ClaudeSessionsDir. initialUUID is the stem for fake-claude's first
// jsonl; trigger is the filesystem path the test creates to signal
// rotation. Idle eviction is left at the spawn default (-pyry-idle-
// timeout=0). Used by rotation-watcher e2e tests; this primitive ships
// independent of any consumer (#123).
func StartRotation(t *testing.T, home, sessionsDir, initialUUID, trigger string) *Harness

// StartRotationWithRelay extends StartRotation with relay wiring so a test
// can drive phone → relay → binary → fakeclaude end-to-end. relayURL is the
// /v1/server endpoint (e.g. fakerelay.URL()+"/v1/server"); stdinLog is the
// path fakeclaude appends its stdin bytes to (additive
// PYRY_FAKE_CLAUDE_STDIN_LOG observability — see #323).
// PYRY_ALLOW_INSECURE_RELAY=1 is set automatically so the daemon accepts
// the ws:// URL. trigger need not refer to an existing file: tests that do
// not exercise rotation can point it at a never-created path. Composes
// StartRotation's fakeclaude env shape with StartInWithEnv's relay-flag
// shape via a single spawnWith call.
func StartRotationWithRelay(t *testing.T, home, sessionsDir, initialUUID, trigger, stdinLog, relayURL string) *Harness

// StartStreamInteractiveWithRelay starts a daemon under interactive_runner:
// "stream-json" (the production toggle selectInteractiveRunner, driven via
// <home>/.pyry/config.json) with the stream-json fakeclaude (#1140) as the
// supervised child plus relay wiring, so a spec can drive phone → relay →
// daemon → stream-runner → fakeclaude and observe the turn drain. Unlike
// StartRotationWithRelay this sets NONE of the SESSIONS_DIR / INITIAL_UUID /
// TRIGGER / STDIN_LOG child envs — stream-mode fakeclaude short-circuits
// above its mustEnv calls and binds no sessions dir. initialUUID pins the
// bootstrap pool id; see § Stream Interactive Harness Pattern below for the
// id-alignment invariant the drain gate depends on. relayURL is the
// /v2/server endpoint; extraEnv is appended verbatim for rider specs.
func StartStreamInteractiveWithRelay(t *testing.T, home, initialUUID, relayURL string, extraEnv ...string) *Harness

type RunResult struct {
    ExitCode int
    Stdout   []byte
    Stderr   []byte
}

func (h *Harness) Run(t *testing.T, verb string, args ...string) RunResult

// RunBare invokes the cached pyry binary with args verbatim — no daemon
// spawn, no auto-injected -pyry-socket, no HOME redirection. For verbs
// that don't touch the control socket (e.g. `version`) or for negative
// tests that want to drive a verb against a deliberately-bogus socket
// path. Reuses the same binary cache and exit-code/timeout/capture
// machinery as Harness.Run.
func RunBare(t *testing.T, args ...string) RunResult

// RunBareIn behaves like RunBare but pins HOME to the supplied directory.
// It does not auto-inject -pyry-socket or itself spawn a daemon. It delegates
// to RunBareInWithEnv with no explicit environment overrides.
func RunBareIn(t *testing.T, home string, args ...string) RunResult

// RunBareInWithEnv behaves like RunBareIn and appends extraEnv after
// childEnv(home) has replaced HOME and stripped PYRY_NAME. Use it when the
// environment variable is itself under test; ordinary callers should use
// RunBareIn so shell aliases cannot leak into the child.
func RunBareInWithEnv(t *testing.T, home string, extraEnv []string, args ...string) RunResult
```

`Start(t) *Harness` is now a one-line `return StartIn(t, t.TempDir())` —
existing call sites unchanged. `StartIn` is the workhorse; `Start` is the
common-case sugar. `Stop` is a public wrapper around the internal `teardown`
(name kept private to make the public/private split obvious to readers).

No `Option`s in this iteration. Per-verb typed wrappers (`Status()`,
`Attach()`) intentionally not added — `Harness.Run` + `RunBare` cover every
shipped non-interactive verb. Wrappers land if a consumer materially benefits.
