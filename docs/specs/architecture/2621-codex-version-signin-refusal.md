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

## Security review

**Verdict:** PASS

Walked against the implementation committed with this plan, since the section was added in rework (see Revisions).

**Findings:**

- [Trust boundaries] No findings. Two boundaries, each crossed in one function. The `account/read` result enters in `Client.SignedIn`, which decodes `account` only as `json.RawMessage` and returns a single bool, so no caller ever holds account data. The version enters in `Client.handshake` through `parseVersion` and leaves `codexsup` only as the string `Client.Version` returns, which `checkCodexVersion` then parses as untrusted input: anything but three integer components with an optional non-empty suffix is refused as unparseable, never allowed.
- [Tokens / secrets] No findings. `SignedIn` sends `{}` with no `refreshToken`, so the probe never asks Codex to refresh a credential. The account object (email, plan type) is never decoded into typed fields, logged or returned. It sits in memory as raw bytes only until `SignedIn` returns. The transport never logs a frame payload: every log line in `internal/acp` carries a method, an id or an error, never a result. The probe passes no `Stderr` in its `codexsup.Config`, so os/exec discards the probe's stderr and nothing Codex prints about the account reaches the daemon log. The fake's signed-in account uses the reserved `example.invalid` domain.
- [File operations] No findings. The probe creates and opens no file. The home it points `CODEX_HOME` at is the daemon-owned path from `codexHomePath`, already prepared by `prepareCodexHome` before the probe runs. No request input enters that path.
- [Subprocess] No findings. The binary is the operator's `-pyry-codex` flag, defaulting to `codex` on the daemon's PATH, the same binary the runner from #2620 spawns. It runs as `exec.CommandContext(bin, "app-server")`, with no shell and no request input in argv. Its environment is the daemon's own plus `CODEX_HOME`. os/exec keeps the last value of a duplicated key, so an inherited `CODEX_HOME` cannot redirect the probe. The runner already passes that same environment to Codex, so the probe exposes nothing new. The process never outlives `probeCodex`. A handshake failure or a timeout under `codexStartTimeout` makes `codexsup.Start` kill and reap the process before it returns. Every later return passes through the deferred `stopCodexClient`, which closes stdin, waits up to `codexCallTimeout`, then sends SIGTERM and SIGKILL after `killGrace` (`cmd.Cancel` plus `cmd.WaitDelay`), and waits on `Done`. The probe opens no thread and starts no turn, so it creates no tool subprocess that could escape through a double fork.
- [Cryptography] Not applicable: the change adds no randomness, key, hash or comparison against a secret.
- [Network & I/O] No findings. Frames from the probe are read through `internal/acp`, capped at `maxLineBytes`, and every call is bounded by `codexStartTimeout`. Resource use per attempt went down. A phone can trigger a create only through a paired, authenticated relay connection that revives a dormant Codex conversation. Each attempt costs at most one short-lived probe, killed on return. Before this change the same attempt returned a runner whose `Run` restarted a failing app-server with backoff for as long as the session lived.
- [Errors & logs] No findings. The version is formatted with `%q` in both refusal messages, so control characters and an empty value come out escaped. Its length is bounded only by the transport's line cap. It is not truncated further, because it comes from the binary the operator chose, which already runs with the daemon's full privileges and is inside the trust boundary. The signed-out refusal names the daemon's Codex home path. That path is not a secret: it sits under the operator's own instance directory, and the operator needs it in order to sign in. It does not reach a remote peer. A relay-driven create that fails surfaces to the phone only as the fixed `CodeServerBinaryOffline` message from the `send_message` handler, or as a generic revive failure in `new_session`. The full error goes to the local daemon log and to the local control-socket caller, which is the same user. An `account/read` RPC failure is wrapped with the server's error text. That text comes from the same trusted binary and carries no account fields that pyry decodes.
- [Concurrency] No findings. The probe runs synchronously inside the factory, which the pool calls outside its lock. Concurrent creates for the same id each run their own probe, and each probe is independent and reaped before it returns. The probe starts no goroutine of its own. The goroutines `codexsup.Start` starts end when the process exits, and the probe waits on `Done` before returning.
- [Test-only knobs] No findings. `FAKECODEX_VERSION` and `FAKECODEX_SIGNED_OUT` are read only in the `main` of `internal/e2e/internal/fakecodex`, a test-only binary under an `internal/e2e/internal` path that no production package can import. No production code reads either variable. Outside the fake, only `_test.go` files mention them, through `t.Setenv`.
- [Threat model] No relay threat is changed. The probe adds no wire message and no new peer input. The one new operator-facing surface is the refusal text, covered above.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25

## Revisions

- 2026-09-25, rework after verifier FAIL on PR #2624: the ticket carries `security-sensitive`, and the plan committed before the implementation had no `## Security review` section. That section is added above, after the implementation, and it audits the committed code as well as the design. It found no MUST FIX or SHOULD FIX items, so the design and the code are unchanged.
