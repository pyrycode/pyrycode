# Spec — net/e2e: send_message roundtrip with fakeclaude observation (#323)

## Files to read first

- `internal/relay/handlers/send_message.go:1-77` — the handler under exercise. Already calls `TurnWriter.WriteUserTurn(p.ConversationID, []byte(p.Text))` and emits `ack` / `error{code=conversations.not_found}` / wrapped error. The e2e exercises the success path.
- `internal/relay/handlers/send_message_test.go:1-220` — unit-test patterns the e2e does NOT need to repeat (per-branch coverage already lives here). The e2e covers the wire path only.
- `internal/sessions/session.go:109-113` — `(*Session).WriteUserTurn` is the production `TurnWriter`; delegates to `Supervisor.WriteUserTurn`. PTY write happens after `ValidateConversation`; hence the test's seeded conversation_id MUST be present in `conversations.json`.
- `internal/sessions/pool.go:355-362` — `ValidateConversation` closure: returns `conversations.ErrConversationNotFound` on miss. With `convReg == nil` the validator is skipped; the prod daemon always passes a registry, so the e2e MUST pre-seed the registry file.
- `cmd/pyry/relay.go:129-137` — wiring: `dispatch.Register(protocol.TypeSendMessage, handlers.SendMessage(sess, logger))` where `sess = pool.Default()`. Confirms the e2e drives the production path verbatim — no fakes inserted between the handler and the supervisor.
- `cmd/pyry/main.go:410-477` — the conversations.Registry is loaded from `<home>/.pyry/<name>/conversations.json` via `resolveConversationsRegistryPath(*name)` and is passed to both `sessions.New` (for `ValidateConversation`) and ultimately to `pool.Default()` as the `TurnWriter` for the handler. Pre-seeding the file before daemon spawn is sufficient.
- `internal/e2e/harness.go:266-301` — `StartRotation` is the existing fakeclaude composer. Does NOT set the relay flag/env. The new helper composes its env+flags shape with `StartInWithEnv`'s relay shape.
- `internal/e2e/harness.go:380-424` — `spawnWith` is the shared core. The new helper assembles `spawnOpts` directly rather than wrapping `StartRotation` (which has positional args and no `extraFlags`).
- `internal/e2e/relay_auth_test.go:1-120` — closest sibling test (phone → relay → binary path). Reuse: `shortHome`, `readPersistedServerID`, `relayTestLogger`, `mustJSON`, the `LastBinaryHello` poll loop.
- `internal/e2e/register_push_token_test.go:1-154` — closest sibling test for the pair → hello → ack pattern. Reuse: `RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")`, `decodePairPayload`, the hello → hello_ack handshake.
- `internal/e2e/internal/fakeclaude/main.go:1-92` — current fakeclaude. Must be extended **additively**: when `PYRY_FAKE_CLAUDE_STDIN_LOG` is unset, behaviour is byte-identical to today (rotation tests must remain green).
- `internal/e2e/internal/fakeclaude/main_test.go:1-126` — pattern the additive edit's unit test should mirror: build the binary into a tmpdir, exec, drive the env, assert observable side-effects, signal SIGTERM.
- `internal/e2e/internal/fakephone/fakephone.go:1-176` — the phone client surface (`Dial`, `Send`, `Receive(timeout)`, `Close`). No changes needed.
- `internal/protocol/messaging.go:5-11` — `SendMessagePayload{ConversationID, MessageID, Text}`. `Text` is the byte-for-byte payload that traverses to the PTY.
- `internal/protocol/codes.go` — `TypeSendMessage`, `TypeAck`. The e2e asserts on these constants, not string literals.
- `internal/conversations/registry.go:15-62` — registry on-disk envelope is `{"conversations":[…]}`. Pre-seeding is a single `os.WriteFile` of a JSON object containing one `Conversation{ID, Cwd}` record.

## Context

#322 landed the production `send_message` handler + dispatcher registration. Unit tests cover all four refusal/success branches in `internal/relay/handlers/send_message_test.go` against a `stubTurnWriter`. What's still untested under `-race` is the **wire path**: phone WS frame → fakerelay → binary → dispatcher → handler → `Supervisor.WriteUserTurn` → PTY → fakeclaude stdin. This slice closes that gap with exactly one e2e test, plus the fakeclaude observability needed to make it deterministic.

Constraint: **no production package outside `internal/e2e/` is modified**. Everything required for the test is either (a) test scaffolding under `internal/e2e/internal/fakeclaude/` or (b) a new helper in `internal/e2e/harness.go` plus a new `_test.go` file.

## Design

Three additive pieces. Order is independent — each can be reviewed on its own.

### 1. fakeclaude stdin logging (additive, opt-in)

`internal/e2e/internal/fakeclaude/main.go` grows one optional env var:

```
PYRY_FAKE_CLAUDE_STDIN_LOG  filesystem path; when set, every byte read from os.Stdin is appended (with fsync after each write); when unset, stdin is not read at all.
```

Behaviour contract:

- **Default off** — when the env var is unset or empty, the program does NOT read stdin at all and does NOT open the log file. Existing tests (`rotation_test.go`, `restart_test.go`, etc.) continue to behave identically; the supervisor's PTY-write side will buffer until the kernel limit (the rotation/restart tests never write a user turn, so this never matters in practice).
- **When set** — at startup, open the log path with `os.O_WRONLY|os.O_APPEND|os.O_CREATE`, mode `0o600`, in a fresh goroutine (not the main poll loop). Read stdin with a small bounded `[4096]byte` buffer in a `for` loop using `io.ReadFull`-like semantics: `n, err := os.Stdin.Read(buf)`; on `n>0`, `f.Write(buf[:n])` then `f.Sync()`; on EOF or read error, return from the goroutine (don't fatal — the supervisor may close the PTY at any time during teardown). The main loop is unchanged.
- The log path is the test's responsibility; the test caller picks a path under its `t.TempDir()` and reads it back via `os.ReadFile`.

Why a goroutine plus `Sync` per write: the e2e polls the file from the test process. Without `Sync`, page-cache-only writes might be invisible across processes for an indeterminate window (Linux generally fine, macOS APFS less so). The cost is one fsync per envelope, which is negligible for an e2e budget.

Why `os.Stdin.Read` rather than a `bufio.Scanner`: `Scanner` would buffer arbitrarily before the test sees bytes; raw `Read + Sync` keeps the visible-byte boundary tight.

A sibling unit test in `main_test.go` builds the binary, pipes a known string to its stdin via `cmd.Stdin = strings.NewReader(...)`, asserts the log file contains those bytes within a 2s deadline, then SIGTERMs. Mirrors the existing rotation test's process-launch shape.

### 2. e2e helper: `StartRotationWithRelay`

New function in `internal/e2e/harness.go`. Composes the fakeclaude wiring (`StartRotation`'s env + claude-bin shape) with the relay-flag wiring (`StartInWithEnv`'s extra flags + insecure-relay env). One signature, all paths supplied by the caller:

```
StartRotationWithRelay(t *testing.T, home, sessionsDir, initialUUID, trigger, stdinLog, relayURL string) *Harness
```

Behaviour contract (one-line summaries; exact assembly is mechanical from `spawnWith`):

- Idempotently builds pyry + fakeclaude (`ensurePyryBuilt`, `ensureFakeClaudeBuilt`).
- `os.MkdirAll(sessionsDir, 0o700)` (matches `StartRotation`).
- Calls `spawnWith(t, home, spawnOpts{...})` with:
  - `claudeBin = fakeclaude binary path`
  - `claudeArgs = []string{}`
  - `extraFlags = []string{"-pyry-workdir="+home, "-pyry-relay="+relayURL}`
  - `extraEnv = ["PYRY_ALLOW_INSECURE_RELAY=1", "PYRY_FAKE_CLAUDE_SESSIONS_DIR=…", "PYRY_FAKE_CLAUDE_INITIAL_UUID=…", "PYRY_FAKE_CLAUDE_TRIGGER=…", "PYRY_FAKE_CLAUDE_STDIN_LOG=…"]`
- Populates `Harness.ClaudeSessionsDir` (mirroring `StartRotation`).
- Registers `t.Cleanup(h.teardown)` and runs `waitForReady`. Same teardown contract as the rest of the harness.

Why a new helper rather than extending `StartRotation`: `StartRotation` has positional params and no `extraFlags`/`extraEnv`. Threading two new positional params through it would force every existing caller to opt out of the relay wiring; a focused new helper is lower-risk and self-documenting at the test call site.

### 3. The e2e test

New file: `internal/e2e/relay_send_message_test.go` (build tag `//go:build e2e`).

Single test: `TestRelay_SendMessage_AckAndPTYDelivery`. Linear narrative — no table-drive (one path under exercise; refusal branches stay in the unit suite). Scenario, in order:

1. **Pair a device** — `RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")`. Decode the QR payload via `decodePairPayload(t, r.Stdout)` (reused from `register_push_token_test.go`).
2. **Pre-seed `conversations.json`** — write `{"conversations":[{"id":"<knownConvID>","cwd":"<home>"}]}` directly to `<home>/.pyry/test/conversations.json` with mode `0o600` (parent dir already exists from `pyry pair`'s side-effect). `knownConvID` is a fixed UUIDv4-shaped string defined as a test const so the assertion path can grep for it.
3. **Allocate fakeclaude paths** — `sessionsDir := <home>/.claude/projects/<encoded>` (or any sub-tempdir; not load-bearing for this test), `initialUUID` = a fixed UUIDv4 stem, `trigger` = a tempdir path that is **never created** (no rotation in this test), `stdinLog` = `<t.TempDir()>/fakeclaude-stdin.log`.
4. **Start fakerelay** — `fr := fakerelay.New(relayTestLogger())`; cleanup.
5. **Spawn daemon** — `h := StartRotationWithRelay(t, home, sessionsDir, initialUUID, trigger, stdinLog, fr.URL()+"/v1/server")`; cleanup.
6. **Wait for handshake** — `serverID := readPersistedServerID(t, home)`; poll `fr.LastBinaryHello(serverID)` with a 5s deadline (same shape as sibling tests).
7. **Phone dial + hello → hello_ack** — `fakephone.Dial(ctx, fr.URL(), serverID, pairPayload.Token, "phone-a")`; send a `TypeHello` envelope (id=1) with `HelloClientPayload`; receive and assert `Type==TypeHelloAck`, `*InReplyTo==1`.
8. **Send `send_message`** — envelope with `ID=2`, `Type=TypeSendMessage`, `Payload=mustJSON(SendMessagePayload{ConversationID: knownConvID, MessageID: "m-1", Text: knownText})`. `knownText` is a fixed multi-byte literal containing a sentinel substring (e.g. `"e2e-323-marker:hello world\n"`).
9. **Receive ack** — `phone.Receive(3*time.Second)`; assert `Type==TypeAck`, `*InReplyTo==2`, `ID >= 2` (dispatcher monotonic).
10. **Poll fakeclaude stdin log for the marker** — `os.ReadFile(stdinLog)` in a 3s deadline loop; succeed when the file contents `bytes.Contains` the marker substring (i.e. `[]byte(knownText)`). Fail with the file's current contents in the error message.
11. **(Implicit conversation_id association)** — The handler's `WriteUserTurn(knownConvID, …)` only reaches the PTY write after `ValidateConversation(knownConvID)` returns nil; the validator returns `ErrConversationNotFound` for any other id (refusal-coded in #322's unit tests). Therefore: **observing both the `ack` envelope AND the marker bytes on stdin proves the conversation_id was accepted by the supervisor cursor before the bytes were written**. No extra cursor-readback assertion needed.

Failure-mode coverage NOT in this test (already covered by unit tests in `send_message_test.go` and live in `internal/sessions/pool*_test.go` for the validator):
- `conversations.not_found` refusal — unit test.
- Malformed payload → `protocol.malformed` — unit test.
- Wrapped error (non-sentinel from `WriteUserTurn`) → no wire reply — unit test.

The e2e is the success-path wire test; refusal coverage stays unit.

## Concurrency model

- **fakeclaude stdin logger goroutine** — exits on `os.Stdin.Read` returning EOF or a non-nil error. The supervisor closes the PTY on shutdown; the goroutine returns naturally. No leak.
- **Test goroutines** — none added by the test itself; all polling is sequential `time.Sleep` loops with deadlines, matching sibling e2e tests.
- **Race-cleanliness** — the test runs under `-race ./...` per AC#4. The single shared resource between the test process and fakeclaude is the stdin log file; the test only reads it via `os.ReadFile`, fakeclaude only writes via append+Sync. POSIX guarantees a single write/Sync is observed atomically by a single read; the `bytes.Contains` check is monotonic (only succeeds, never regresses).

## Error handling

- All test failures use `t.Fatalf` with the most-recent observable state in the message (current log file contents, full ack envelope, fakerelay's last seen hello). Same shape as sibling tests.
- fakeclaude's stdin-log open failure (e.g. unwritable directory) → `fatalf` (stderr line + `os.Exit(1)`). The supervisor sees the child exit and applies backoff; the test's readiness gate then fails. The error message identifies the failed path so the test author can debug. Matches the existing `openSession` failure shape.

## Testing strategy

Two test additions:

- `internal/e2e/internal/fakeclaude/main_test.go` (additive case): `TestFakeClaude_StdinLog_AppendsBytes` — build the binary, exec it with all four env vars (sessions dir, initial UUID, trigger, stdin-log path); pipe a known string via `cmd.Stdin = strings.NewReader(want)`; poll the log file for `bytes.Contains(content, []byte(want))` with a 2s deadline; SIGTERM and assert clean exit. Asserts the additive feature in isolation.

- `internal/e2e/relay_send_message_test.go`: the test described in §3 above.

Both run under `go test -race -tags=e2e ./internal/e2e/...`.

## Open questions

- **Is the test flake-sensitive to PTY buffering?** The marker payload is small (<100 bytes); a single PTY write fits in the kernel buffer regardless of whether stdin is read. The fakeclaude goroutine reads + Syncs continuously, so visibility-from-the-test-process is the bottleneck, not PTY drain. 3s deadline should be ample.
- **Does the seeded `Cwd` matter?** No — `ValidateConversation` only checks ID presence; `Cwd` is unused in the validator. Set it to `home` for human readability.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings — the test crosses three boundaries (phone WS → fakerelay → daemon WS, daemon → PTY → fakeclaude), all of which already exist in the production data path. The new `PYRY_FAKE_CLAUDE_STDIN_LOG` env var is parsed by fakeclaude itself (never by production code) and the path it accepts is supplied by the test, not by an untrusted source.
- **[Tokens, secrets, credentials]** No findings — the test uses a real paired token via `pyry pair`; `pairPayload.Token` is the same plaintext shape every other e2e drives. The token is NOT logged by the new helper or test code (mirrors existing `register_push_token_test.go`'s discipline). The fakeclaude stdin log captures the message **payload** (`SendMessagePayload.Text`) — by design — but `Text` here is a test-controlled marker string, not a real secret. Document this in the test header comment so a future reader doesn't paste a real token into `knownText`.
- **[File operations]** No findings on path traversal — every path the test or helper opens is computed from `t.TempDir()` or `shortHome(t)`. fakeclaude's stdin-log open uses the env-var path verbatim with mode `0o600` and `O_APPEND|O_CREATE` (no `O_TRUNC`); the test owns the path. No symlink hardening is added (and none is needed: the path is test-allocated, never under attacker control). The `0o600` mode keeps the log unreadable by other users on shared CI hosts.
- **[Subprocess / external command execution]** No findings — `exec.Command(binPath, args...)` with allowlisted argv (no `sh -c`). All env is built with `childEnv(home)` + appended literals; no env passthrough beyond what existing helpers already inherit.
- **[Cryptographic primitives]** Not applicable — this slice introduces no new crypto; it consumes `pyry pair`'s existing `crypto/rand`-minted token verbatim.
- **[Network & I/O]** No findings — the test reuses `transport`'s 1 MiB `SetReadLimit` via `fakephone` (already pinned). The marker payload is <100 bytes; no DoS surface.
- **[Error messages, logs, telemetry]** No findings on token leakage — neither the helper nor the test logs `pairPayload.Token`. The fakeclaude stderr path on log-open failure echoes the path the test allocated (not an external input). Test failure messages include the ack envelope and fakeclaude log contents, both of which contain only test-controlled bytes.
- **[Concurrency]** No findings — the fakeclaude stdin goroutine exits on Read error/EOF (PTY close on supervisor shutdown). No new shared state in the daemon; the test polls the log file via `os.ReadFile`, which is wait-free w.r.t. the appender.
- **[Threat model alignment]** Not applicable — this is a test-only slice. No new production trust boundary, no new production secrets surface, no new production network listener. The thing being tested (the `send_message` wire path) was already security-reviewed in #322.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-14
