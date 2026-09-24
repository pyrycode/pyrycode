# #2621 — refuse a Codex session on an old Codex or a signed-out Codex home

## Files read

- `cmd/pyry/codex_runner.go` → `newCodexRunnerFactory`, `codexRunner.Run`, `runOnce`, `stopCodexClient` — the factory is where the refusal must come out; `Run` loops with backoff on any start error, so a check there would retry forever.
- `cmd/pyry/main.go` → `harnessRunnerFactory` — a factory error leaves the session dormant and the daemon running.
- `internal/sessions/pool.go` → `buildSessionAs` — the create path calls the factory synchronously and wraps its error as `sessions: create runner: …`. The bootstrap (`New`) is always Claude, so the Codex probe never runs at daemon startup.
- `internal/codexsup/client.go` → `Start`, `handshake`, `parseVersion`, `Client.Version`, `call` — the handshake already parses the version; `call` is the request helper a new method reuses.
- `internal/codexsup/methods.go` → `clientRequests` — `TestMethodNamesInSchema` checks every entry against the schema's `ClientRequest`.
- `internal/codexsup/codex_app_server_protocol.schemas.json` → `v2.GetAccountParams` (`{refreshToken?: bool}`), `v2.GetAccountResponse` (`{account: Account|null, requiresOpenaiAuth: bool}`, only `requiresOpenaiAuth` required), `v2.Account` (`chatgpt` arm carries `email`, `planType`).
- `internal/e2e/internal/fakecodex/main.go` → `requestHandlers`, `initialize`, `main` — env-only configuration; `TestMethodNamesInSchema` in its test file ranges over `requestHandlers`.
- `cmd/pyry/codex_runner_test.go` → `fakeCodexBin`, `TestCodexRunnerFactory_DaemonHome` — the existing signed-in, current success case the factory tests extend.

No in-flight `feature/*` branch touches these files.

## Context

A Codex session on a Codex below the pinned 0.156.1, or on a daemon Codex home nobody signed in to, fails later and unclearly. The factory must talk to Codex once, before returning a runner, and refuse with an error saying which setup is wrong.

## Design

### `internal/codexsup`

- `methods.go`: `methodAccountRead = "account/read"`, appended to `clientRequests`.
- `client.go`: `func (c *Client) SignedIn(ctx context.Context) (bool, error)`. Sends `account/read` with `{}` params (no `refreshToken`). Decodes only `account` as `json.RawMessage` and `requiresOpenaiAuth` — never the account's fields, so an email address is never held in a typed value, logged or returned. Returns false only when `account` is absent/null **and** `requiresOpenaiAuth` is true; true otherwise.

### `cmd/pyry/codex_runner.go`

- `const codexMinVersion = "0.156.1"`.
- `checkCodexVersion(v string) error`: parses `MAJOR.MINOR.PATCH` with an optional `-prerelease` / `+build` suffix; three numeric components required. A prerelease of the same core is below it (semver: `0.156.1-alpha.1` < `0.156.1`). Below or unparseable → error naming the version found (`%q`, so an empty one is visible) and `codexMinVersion`.
- `probeCodex(bin, home, dir string) error`: `codexsup.Start` against the daemon home with a `codexStartTimeout` context, `checkCodexVersion(client.Version())`, then `client.SignedIn`, then `stopCodexClient` on every path. Signed out → error: the daemon's Codex home is not signed in; sign in with `CODEX_HOME=<home> codex login`. It never calls `StartThread`/`ResumeThread`, so no thread is started on any path.
- `newCodexRunnerFactory`: calls `probeCodex` after `prepareCodexHome` and workdir resolution, before `newCodexRunner`; an error returns `nil, fmt.Errorf("cmd/pyry: codex runner: %w", err)`.

Version check runs before `account/read`, so a too-old Codex that may not implement `account/read` is reported as too old, not as a request failure.

### fake Codex

- New env: `FAKECODEX_VERSION` (overrides the version in `userAgent`, and the threads' `cliVersion`; default `0.156.1`) and `FAKECODEX_SIGNED_OUT` (non-empty → signed out). Defaults unchanged, so existing tests are unaffected.
- New handler `account/read`: signed in → `{account: {type: "chatgpt", email: "fakecodex@example.invalid", planType: "plus"}, requiresOpenaiAuth: true}`; signed out → `{account: null, requiresOpenaiAuth: true}`.
- Package doc lists the new method and the two env vars.

## Concurrency model

The probe is one synchronous, short-lived app-server process inside the factory call; it is stopped (bounded by `codexCallTimeout`, then SIGKILL via codexsup) before the factory returns. No goroutine outlives it. The pool calls the factory outside its lock, so a slow probe (≤ `codexStartTimeout`) delays only that create.

## Error handling

- Start/handshake failure → wrapped as `probe codex: …`, runner not returned.
- Version below/unparseable → refusal naming found and required.
- `account/read` failure → wrapped, runner not returned (not treated as signed out).
- Signed out → refusal naming `CODEX_HOME` and the home path.
- Account details never enter an error or a log line.

## Testing strategy

- `cmd/pyry`: `TestCheckCodexVersion` — table: `0.156.1`, `0.156.2`, `0.157.0`, `1.0.0` pass; `0.155.0`, `0.155.0-alpha.3`, `0.156.1-alpha.1`, `0.156.0` fail naming both versions; `""`, `garbage`, `0.156` fail as unparseable.
- `cmd/pyry`: `TestCodexRunnerFactory_Refusals` — table driving the factory against the fake with `t.Setenv`: old version, unparseable version, signed out. Each: error contains the expected text (version found + `0.156.1`, or `CODEX_HOME=` + home path), runner is nil.
- `cmd/pyry`: `TestCodexRunnerFactory_DaemonHome` (existing) stays green — it is the signed-in, current case: runner returned, runs, binds.
- `internal/codexsup`: `TestSignedIn` against the fake: default → true; `FAKECODEX_SIGNED_OUT=1` → false. `TestMethodNamesInSchema` covers `account/read` in both packages automatically.

## Open questions

None.

## Documentation handoff

None required by the ticket.
