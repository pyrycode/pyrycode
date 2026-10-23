# Isolation Strategy

Pyry resolves `~/.pyry/<name>.sock`, `~/.pyry/<name>/sessions.json`, and
`~/.claude/projects/<encoded-cwd>/<uuid>.jsonl` via `os.UserHomeDir()`, which
honors `$HOME` on Unix. The harness redirects `HOME` to `t.TempDir()` so every
path the daemon would touch under a real home is contained, with one env var.

Belt-and-suspenders: `-pyry-socket=<HomeDir>/pyry.sock` is also passed
explicitly. The registry still lands at `<HomeDir>/.pyry/test/` via HOME
redirection — no new `-pyry-registry` flag was needed.

`PYRY_NAME` is stripped from the child's env so the operator's shell alias can't
leak into a test daemon. Helpers that accept `extraEnv` append it after this
scrub, allowing a test whose subject is `PYRY_NAME` to opt in deliberately.

Spawn args:

```
-pyry-socket=<HomeDir>/pyry.sock
-pyry-name=test
-pyry-claude=<home>/sleep-claude.sh
-pyry-idle-timeout=0
<extraFlags...>          # variadic, last-wins via Go's flag package
-- 99999
```

`-pyry-claude` defaults (zero-value `spawnOpts.claudeBin`) to the
sleep-claude wrapper — `writeSleepClaude(t, home)` writes `#!/bin/sh` +
`exec sleep 99999` to `<home>/sleep-claude.sh` (0o755) and returns its path
(#918; was a bare `/bin/sleep` before). The wrapper ignores all argv, so it
tolerates both the bootstrap invocation's trailing `99999` and any
daemon-appended spawn-time flag — see
[Default Stand-In Argv Tolerance](e2e-harness-default-stand-in-argv-tolerance.md)
for why a bare `/bin/sleep` stopped working. `exec sleep 99999`
survives ~27 hours (longer than any test runs), and the readiness gate
doesn't depend on the child being a real claude. `99999` (a plain integer in
seconds) is the only argv form portable across BSD and GNU `sleep`:
`infinity` is GNU coreutils only and macOS BSD sleep rejects it (see
`lessons.md § Test helpers across packages`). #115 changed the harness from
`infinity` to `99999` because the lazy-respawn test waits for `Phase:
running` after a respawn — under `infinity`, macOS BSD sleep exits
immediately, the supervisor enters perpetual backoff, and `Phase: running`
is never observed. `IdleTimeout=0` defeats the eviction timer by default;
tests that need eviction pass `-pyry-idle-timeout=<dur>` via the variadic on
`StartIn`.

For service-folder tests, set `exec.Cmd.Dir` explicitly as well as the isolated
HOME. `spawnWith` sets `--pyry-workdir` but inherits the test process cwd; that
flag controls Claude's spawn directory, while the daemon's startup workspace
base comes from its own process cwd. With the usual isolated HOME, leaving
`Dir` unset exercises the legacy fallback instead of the intended service folder.
Use a distinct spawn override
and assert both the decrypted raw `hello_ack.workspace_root` and the persisted
General cwd, label and seed marker so wiring either consumer to the wrong base
fails the test. See `TestWorkspaceBase_DaemonAdvertisesAndSeedsProcessCwd` and
the [startup-base contract](../../protocol-mobile.md#hello_ack-v2-specific-note).

## Pre-daemon paired-device setup

Tests that need an authenticated phone before daemon startup use
`paireddevice.Setup` from `internal/e2e/internal/paireddevice`. Invoking the
operator-facing `pyry pair` command is the wrong fixture boundary: that command
is a live-daemon client, while setup must finish before the daemon under test
exists. Keeping the helper under `internal/e2e/internal` makes the offline path
available to the hermetic, live-Claude and live-relay suites without exposing it
to production packages or external callers.

`Setup` takes an absolute isolated home, instance name, relay destination,
optional label and remote-permission choice. It reuses the instance identity and
static key, appends a fresh device, and returns the matching `pair.Payload` only
after `devices.Registry.Save` succeeds. The plaintext token is therefore an
egress value, never persisted state; failures return a zero payload. Sequential
repeats are supported, but concurrent setup against one instance is outside the
contract because the underlying identity and key stores are not concurrent
initializers. See [Pair command](pyry-pair-command.md) for the shared credential
shape and [Device registry](devices-registry.md) for persistence semantics.

A save-failure test must inject a known token and fail at the save seam. That is
what lets it prove both halves of the security contract: the prior registry is
byte-for-byte unchanged, and neither the known plaintext nor its encoded pairing
appears in the error or any persisted file. Filesystem permission failures are a
poor substitute because they are platform-dependent and often fail before a
secret exists.
