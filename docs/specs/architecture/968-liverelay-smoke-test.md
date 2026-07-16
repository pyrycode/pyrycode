# 968 — Live-relay smoke test (`e2e_liverelay`)

Prove the **daemon ↔ real pyrycode-relay** pairing with one automated round-trip,
closing the gap that only hand-runs have covered. Structural twin of
`e2e-realclaude`: an opt-in build tag, its own package, wired into `preship`,
**not** `check`.

## Context

Every relay e2e test today runs against the in-process `fakerelay`
(`internal/e2e/internal/fakerelay/`). The deployed relay
(`github.com/pyrycode/pyrycode-relay`, live at `pyrycode-relay.pyryco.de`) is
exercised by **no** automated test — the daemon↔real-relay pairing has only ever
been proven by hand. The daemon-side client (`internal/relay`,
`internal/transport`) is well covered; the real server is the untested half.
This ticket adds one opt-in test that drives a full round-trip through a **real**
relay binary.

The design reuses the existing, proven fakerelay round-trip almost verbatim —
`testV2DaemonListConversationsRoundTrip` (`internal/e2e/relay_v2_daemon_test.go`)
— and swaps only the relay: `fakerelay.New(...)` → a spawned local
`pyrycode-relay` binary. Everything else (pair, Noise IK handshake, envelope
round-trip) is unchanged, because the relay is a **dumb, stateless router**: the
Noise handshake and the verb round-trip ride end-to-end *inside* the routing
frames, which the relay forwards without inspecting.

### Design decision — option (a), locally-built relay binary

The ticket left the (a)/(b) call to the architect. **Decision: (a)** — spawn a
locally built `pyrycode-relay` from the sibling repo. Rationale, all verified
against the relay source:

- The relay is **stateless and credential-free**: "the binary owns canonical
  state; the relay holds zero per-user state" (relay `README.md`); it "routes by
  the `x-pyrycode-server` header and never reads message payloads." It does
  **not** validate `x-pyrycode-token` against any backing store — the Noise
  handshake between phone and daemon is the actual auth. So a hermetic local
  relay needs **no database, no provisioning, no secrets**.
- It runs hermetically behind `--insecure-listen` (plain HTTP, no autocert, no
  domain, no TLS). Boot-time guards (`CheckInsecureListenInProduction`,
  `CheckRunningAsRoot`, `CheckSingleInstance`) only fire when production env vars
  are set or on Fly (`FLY_APP_NAME`); with a clean env it starts cleanly.
- Option (a) keeps `preship` **offline-capable** — no network, no production
  dependency, no credential lifecycle. This collapses the `security-sensitive`
  review to a near-no-op (see § Security review), exactly as the ticket
  predicted: "if (a) with an ephemeral in-test identity, the review is expected
  to be a fast no-op."

Option (b) (production URL + pre-provisioned device identity) is **explicitly
not chosen**: it would introduce persisted key material, make the gate depend on
live infrastructure, and force a real credential-custody audit — all avoidable.

### Why the round-trip works unchanged against the real relay

Three facts, each verified, make the fakerelay body port cleanly:

1. **The daemon always speaks Noise v2**, regardless of relay endpoint path. ADR
   024 is a hard cutover — `startRelay` (`cmd/pyry/relay.go`) unconditionally
   calls `startRelayV2`; there is no v1 or mixed-mode path and no path-based
   protocol selection. `testV2DaemonDefaultEngagesV2` already proves v2 engages
   by default (switch unset). `PYRY_MOBILE_V2` is no longer read by any
   production `.go` file.
2. **The daemon connects at `/v1/server`.** `resolveDialURL`
   (`internal/relay/connection.go`) appends `/v1/server` when the relay URL
   carries no path. The real relay serves exactly `/v1/server` (binary) and
   `/v1/client` (phone) (relay `main.go` mux). So passing a **bare base URL**
   (`ws://127.0.0.1:<port>`, no `/v2/server` suffix) makes the daemon connect at
   the endpoint the real relay serves. (The fakerelay test's explicit
   `/v2/server` suffix worked only because fakerelay maps both paths to one
   handler; the real relay has no `/v2/server` route — do **not** copy that
   suffix.)
3. **`fakephone` speaks the real phone wire.** It dials `/v1/client` with the
   three real headers and sends raw `protocol.Envelope` /
   `InnerFrameV2` JSON (its package doc: "speaks the phone half of the
   mobile↔relay protocol"). It is a *device* stand-in, not a *protocol* fake, so
   it drives the real relay's `/v1/client` route unchanged. Reused by import (see
   § Package structure).

## Files to read first

- `internal/e2e/relay_v2_daemon_test.go:86-186` — `testV2DaemonListConversationsRoundTrip`.
  **The primary template.** Port this body 1:1, swapping the relay (§ Design).
  Note its `fr.WaitBinary(serverID)` call — the real relay has no such hook;
  replace per § Phone-connect readiness.
- `internal/e2e/realclaude/interactive_bootstrap_liveness_test.go:247-475` —
  the **transcription source** for the standalone helpers: `driveHandshakeInteractive`
  (Noise IK to open state), `sendNoiseInit`/`sendNoiseMsg`/`readInnerFrame`,
  `spawnBootstrapDaemon`/`waitForReady`/`stop`, `shortSocketPath`, `runPyry`,
  `decodePairPayload`. These are already a standalone transcription of the
  `internal/e2e` harness under a sibling build tag — copy-adapt them, dropping
  the real-claude/auth bits and swapping fakerelay → the spawned relay's base URL.
- `internal/e2e/internal/fakephone/fakephone.go` — the phone client. **Import
  and use directly** (`Dial`, `SendBytes`, `ReceiveBytes`, `Close`); it dials
  `/v1/client`. Importable from `internal/e2e/liverelay` under Go's `internal/`
  rule (both rooted at `internal/e2e/`).
- `internal/relay/connection.go:147-165` — `resolveDialURL`: proof that a
  path-less base URL gets `/v1/server` appended. Drives the "pass a bare base
  URL" decision.
- `internal/e2e/harness.go:462-485` — `sleepClaudeScript` + `writeSleepClaude`:
  the supervised-child stand-in to pass via `-pyry-claude` (a bare `/bin/sleep`
  crash-loops because the daemon appends `--session-id`). Transcribe (~12 lines).
- `internal/e2e/harness.go:853-878` — `shortHome`, `readPersistedServerID`,
  `relayTestLogger`: small unexported helpers to transcribe (~30 lines total).
- `internal/e2e/realclaude/fixtures.go` — the `ensurePyryBuilt` pattern (build
  `pyry` once via `go build`, honor `PYRY_E2E_BIN`, `sync.Once`). Mirror it, and
  mirror it again for the relay binary (§ Relay binary resolution).
- `../pyrycode-relay/cmd/pyrycode-relay/main.go:49-101,216-281` — relay flags and
  mux: `--insecure-listen`, `--metrics-listen` (empty disables), `/healthz`,
  `/v1/server`, `/v1/client`. Confirms no env/creds needed to start insecure.
- `../pyrycode-relay/Makefile:10-12` + `README.md` § Build/Run — `go build -o
  bin/pyrycode-relay ./cmd/pyrycode-relay`; `--insecure-listen :8080`
  behind-proxy mode.
- `docs/knowledge/features/e2e-realclaude.md` § Build tag, § CI cadence — the
  opt-in-tag + wired-into-preship-not-check precedent this ticket mirrors
  (including the **skip-loud-on-missing-prerequisite** posture).
- `docs/release-tooling.md` — the doc to extend (AC #2).

## Package structure & build tag

New package, one file:

```
internal/e2e/liverelay/liverelay_test.go   //go:build e2e_liverelay
```

Single tag, no alternation — the `e2e_realclaude` precedent. All helpers are
file-local (the realclaude single-file convention). Because the tag is distinct,
the file is invisible to `go build ./...`, `go vet ./...`, `make test`, and
`make e2e` (`-tags e2e`) — satisfying AC #3 by tag exclusion alone, no path
filter.

**Imports (reuse, do not transcribe):** `internal/e2e/internal/fakephone`,
`internal/protocol`, `internal/noise`, `internal/pair` (for `pair.Payload`).
**Transcribe (small, file-local):** the daemon-spawn, Noise-driving, sleep-child,
`shortHome`, `readPersistedServerID`, `decodePairPayload`, `ensureXxxBuilt`
helpers named in § Files to read first.

Do **not** widen the build tags of `internal/e2e/harness.go` or any existing file
to import its unexported helpers — that drags unrelated `_test.go` suites into
`make e2e-liverelay` and departs from the realclaude precedent. Transcription is
the blessed pattern (see #854/#997, already transcribed into `realclaude`).

## Design — the round-trip

The single test (`TestLiveRelay_ListConversationsRoundTrip`) runs these stages;
**any stage error, or a reply that does not match what was sent, fails the test**
(AC #1 — this is not a no-panic smoke check).

**Stage 0 — relay binary resolution (or skip).** Resolve a `pyrycode-relay`
binary, in order: `PYRY_LIVERELAY_BIN` (prebuilt path) → build from the sibling
repo (located via `PYRY_RELAY_REPO`, else a default path derived from the test
file location, `../pyrycode-relay` relative to the pyrycode repo root) via `go
build -o <tmp> ./cmd/pyrycode-relay` with `Dir` = sibling repo, cached with
`sync.Once`. If neither the binary nor a buildable sibling repo is present,
`t.Skipf` with a **named** diagnostic: the two env vars, the expected sibling
path, and `git clone github.com/pyrycode/pyrycode-relay`. Skip-loud (not fatal,
not fail) keeps `make preship` green on a machine without the sibling — the
`e2e-realclaude` auth-gated-skip posture. Document the prerequisite (AC #2).

**Stage 1 — start the relay (loopback).** Pick a free port by binding
`127.0.0.1:0`, reading the port, and closing the listener (localhost TOCTOU
window is acceptable — test-only). Spawn the relay:
`pyrycode-relay --insecure-listen 127.0.0.1:<port> --metrics-listen=`.

- **Bind loopback explicitly (`127.0.0.1`, never `:<port>` / `0.0.0.0`)** so the
  ephemeral plaintext relay is never reachable off-box. Load-bearing for the
  security review — keep it in the code, with a comment.
- `--metrics-listen=` disables the default `127.0.0.1:9090` metrics bind, which
  would otherwise risk a port conflict / `CheckListenerPorts` refusal.
- Readiness: poll `http://127.0.0.1:<port>/healthz` for HTTP 200 under a bounded
  deadline (~5 s) before proceeding. Tear down via SIGTERM→grace→SIGKILL in
  `t.Cleanup`.

**Stage 2 — device identity (ephemeral).** Under a fresh temp HOME (`shortHome`),
run `pyry pair -pyry-name=test --name=phone-a` via the offline `runPyry` helper;
decode the pair stdout (`decodePairPayload`) into `pair.Payload` — the bearer
`Token` and the base64 `ServerStaticPubkey` the phone pins. The daemon loads the
same static key on startup. The keypair + token are ephemeral, live only under
the temp HOME, and are destroyed by `t.Cleanup` (`os.RemoveAll`).

**Stage 3 — seed one conversation.** Write `conversations.json` under
`<home>/.pyry/test/` at `0600` with one known conversation row (fixed UUID, `cwd`
= home, past `last_used_at`) — the row the handler reads back. Mirror
`testV2DaemonListConversationsRoundTrip` verbatim.

**Stage 4 — start the daemon.** Spawn `pyry` (built via `ensurePyryBuilt`) with
the sleep-claude stand-in and a **bare base relay URL**:

```
-pyry-name=test  -pyry-claude=<sleep-claude.sh>  -pyry-idle-timeout=0
-pyry-socket=<shortSocketPath>  -pyry-relay=ws://127.0.0.1:<port>   [no path]
env: PYRY_ALLOW_INSECURE_RELAY=1  (+ PYRY_MOBILE_V2=1, harmless no-op, matches
     the proven fakerelay config)
```

`resolveDialURL` appends `/v1/server`. Wait for the daemon's control socket
(`waitForReady`).

**Stage 5 — connect the phone (retry until the binary is registered).** Read the
persisted server-id (`readPersistedServerID`). Then dial `fakephone.Dial(ctx,
"ws://127.0.0.1:<port>", serverID, payload.Token, "phone-a")`. See § Phone-connect
readiness for the retry contract.

**Stage 6 — Noise IK handshake.** Drive the initiator to open state
(`driveHandshakeInteractive`, transcribed): generate an ephemeral X25519 key
(`crypto/rand`), `noise.NewInitiator(priv, serverStaticPub)`, `WriteInit(hello)` →
`sendNoiseInit`, read the `noise_resp` inner frame, `ReadResp` → the two
`CipherState`s (send encrypts phone→binary, recv decrypts binary→phone).

**Stage 7 — verb round-trip + assert.** Marshal a `protocol.Envelope{Type:
TypeListConversations, ID: reqID, Payload: "{}"}`, `initSend.Encrypt`, send via
`sendNoiseMsg`; read the reply inner frame, `initRecv.Decrypt`, unmarshal.
Assert:
- reply `Type == protocol.TypeConversations`,
- `InReplyTo` points to `reqID`,
- the decoded `ConversationsPayload` contains exactly the one seeded
  conversation, with the seeded ID.

This is the "verb response matches what was sent" check AC #1 requires.

### Phone-connect readiness (the one real-relay difference)

`fakerelay` exposes `WaitBinary(serverID)` to close the
daemon-registers-then-phone-dials race; the **real relay has no test hook**. The
real relay rejects a `/v1/client` upgrade (HTTP 503 / WS close) while no binary is
bound to the server-id. Replace `WaitBinary` with a **dial-retry loop**: attempt
`fakephone.Dial` on a short backoff (~50–100 ms) until it succeeds or a bounded
deadline (~5 s) elapses; treat dial/upgrade failure as retryable. Once the
daemon's relay client registers as the binary (automatic, shortly after
`waitForReady`), the dial succeeds. A deadline miss fails the test with the last
dial error. This is the only operational divergence from the fakerelay body and
must be a named, commented helper.

## Concurrency model

Sequential test; two spawned OS processes (relay, daemon) each with one
`cmd.Wait` goroutine closing a `doneCh` (the realclaude `bootstrapDaemon`
pattern). No shared mutable state beyond the `sync.Once`-guarded binary builds.
Teardown is `t.Cleanup` LIFO: relay is started first so its cleanup runs last —
the daemon (started later) is torn down first, then the relay, then temp dirs.
Every goroutine exits when its process exits or is killed; no leak.

## Error handling / failure modes

- **Sibling repo / relay binary absent** → `t.Skipf` (loud, named) — not a
  failure. Keeps `preship` green without the sibling.
- **Relay fails to start / `/healthz` never 200** → `t.Fatalf` with captured
  relay stderr.
- **Daemon never ready** → `t.Fatalf` with captured daemon stderr.
- **Phone never connects within the deadline** → `t.Fatalf` with the last dial
  error (binary-registration race exhausted — real bug or environment issue).
- **Handshake / decrypt / unmarshal error, or reply mismatch** → `t.Fatalf`.
  These are the AC #1 "fails if any stage errors" surface.

## Testing strategy

The test **is** the deliverable. Verify by running:

```
make e2e-liverelay          # with ../pyrycode-relay present → full round-trip, green
```

Manual verification the developer should record on the PR:
- With the sibling present: the test builds the relay, runs the round-trip,
  passes; the seeded conversation ID round-trips.
- With the sibling absent (temporarily rename the path / unset the env): the test
  **skips** with the named diagnostic — it does **not** fail.
- `make check` runs the same targets as before (AC #3): confirm `-tags e2e`
  compiles nothing new and `make check`'s target list is unchanged. `git grep -n
  e2e_liverelay -- Makefile` shows the tag only on the new standalone target.

No table-driven matrix — this is one end-to-end path. Keep it a single test
function with file-local helpers.

## Makefile + docs

**Makefile** — add a target mirroring `e2e-realclaude`, and wire it into
`preship` (option (a) is offline-capable):

```make
.PHONY: e2e-liverelay
e2e-liverelay:
	$(GO) test -tags e2e_liverelay ./internal/e2e/liverelay/...

# preship gains the live-relay gate alongside e2e-realclaude:
preship: check e2e-realclaude e2e-liverelay
```

Leave `check` and `e2e` untouched (AC #3). Update the `make` header comment block
to describe `e2e-liverelay` (prerequisite: sibling `pyrycode-relay` checkout;
skips loud without it).

**Docs (AC #2)** — extend `docs/release-tooling.md` with a short section: how to
run (`make e2e-liverelay`), what it requires (the sibling `pyrycode-relay`
checkout at `../pyrycode-relay`, or `PYRY_LIVERELAY_BIN`; **no** credentials —
contrast realclaude), that it spends **no** real resources (hermetic local
relay), and where it sits in the release flow (wired into `preship`, skips loud
without the sibling). ~30–40 lines.

The evergreen feature doc (`docs/knowledge/features/e2e-liverelay.md`) is the
**documentation phase's** job, not the developer's — do not add it as an AC.

## Open questions

- **Free-port TOCTOU** — bind-`:0`-then-close leaves a sub-millisecond window
  before the relay rebinds the port. Acceptable for a localhost test; if it ever
  flakes, switch to spawning the relay on `--insecure-listen 127.0.0.1:0` and
  parsing the bound port from a relay startup log line (requires the relay to log
  the resolved port — a sibling-repo change, out of scope here).
- **`PYRY_MOBILE_V2=1`** — set for belt-and-suspenders parity with the proven
  fakerelay test, but no production `.go` reads it (v2 is the hard-cutover
  default). Developer may drop it if the round-trip is green without it; harmless
  either way.

## Security review

**Verdict:** PASS

Option (a) — a hermetic, loopback-bound, credential-free local relay with an
ephemeral in-test identity — collapses this to the fast no-op the ticket
predicted. The test adds **no** production trust surface; it exercises the
existing protocol (Noise IK + token auth) against a real relay binary.

**Findings:**

- **[Trust boundaries]** No new production boundary. The test drives the real
  relay's existing `/v1/server` + `/v1/client` boundaries; all inputs (port,
  socket path, relay URL, conversation seed) are test-controlled constants, none
  attacker-influenced.
- **[Tokens, secrets, credentials]** No finding. The bearer token + X25519
  static keypair are minted by `pyry pair` (`crypto/rand`) under a temp HOME and
  destroyed by `t.Cleanup(os.RemoveAll)`. Nothing is persisted beyond the test,
  no production key, no `PYRY_LIVERELAY_BIN` *secret* (it's a binary path). The
  ephemeral token appearing in pair stdout / the `x-pyrycode-token` header is
  local and test-scoped. This is precisely the ephemeral-identity path the ticket
  flagged as a no-op.
- **[File operations]** No finding. `conversations.json` at `0600`, sleep-claude
  script at `0755`, all under a test-owned temp HOME; no user input in any path;
  no symlink/TOCTOU surface beyond the localhost port bind noted below.
- **[Subprocess execution]** MUST-FIX baked into the design, not deferred: the
  relay binds **loopback only** (`127.0.0.1`, never `0.0.0.0`/`:port`) so the
  ephemeral plaintext relay is unreachable off-box (§ Stage 1). `exec.Command`
  args are constants; no `sh -c`; relay + daemon both torn down SIGTERM→SIGKILL.
  The sibling `pyrycode-relay` is built and run from the operator's own checkout
  — the deliberate cross-repo dependency this ticket sanctions, on the operator's
  machine, not a supply-chain finding.
- **[Cryptographic primitives]** No finding. Noise IK via the existing
  `internal/noise`; ephemeral key via `crypto/rand`; no hand-rolled crypto; the
  test consumes these, it does not re-implement them.
- **[Network & I/O]** No finding. Plaintext `ws://` is gated behind
  `PYRY_ALLOW_INSECURE_RELAY=1` and a loopback bind — test-only, never a
  production path. Frame-size caps and `http.Server` timeouts are inherited from
  the relay binary and `fakephone`. Dial/read use `context.WithTimeout`.
- **[Error messages, logs, telemetry]** No finding. The only sensitive value in
  logs is the ephemeral pair token — local, test-scoped, no production log
  surface. Metrics listener is disabled (`--metrics-listen=`).
- **[Concurrency]** No finding. Sequential test; per-process `cmd.Wait`
  goroutines exit on process exit; `t.Cleanup` LIFO tears daemon down before
  relay; no shared mutable state beyond `sync.Once` builds.
- **[Threat model alignment]** This test improves coverage of
  `docs/protocol-mobile.md` § Security model (proves Noise IK + token auth end to
  end against the real relay); it adds no attack surface. **OUT OF SCOPE:** option
  (b) — targeting the production URL with a *persisted* pre-provisioned device
  identity — is not chosen here; a future ticket that adds (b) must audit
  persisted-key custody, scoping, and production-reachability. Named as deferred.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-16
