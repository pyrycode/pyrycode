# `internal/e2e/liverelay` — real-`pyrycode-relay` round-trip suite

Sibling Go package to [`internal/e2e`](e2e-harness.md) and
[`internal/e2e/realclaude`](e2e-realclaude.md), gated by its own build tag,
that proves the daemon ↔ **real** `pyrycode-relay` pairing with one automated
round-trip. Closes the gap every other relay e2e test leaves open: they all
run against the in-process [`fakerelay`](fakerelay-harness.md); the deployed
relay server (`pyrycode/pyrycode-relay`, live at `pyrycode-relay.pyryco.de`)
had been exercised by hand only, never by an automated test.

## Why a sibling, not part of `internal/e2e`

Same rationale as `realclaude`: a distinct build tag keeps the suite out of
`go build ./...`, `go vet ./...`, `make test`, and `make e2e` by tag
exclusion alone — no path filter needed. Unlike `realclaude`, this suite
needs **no credentials and spends no real resources** (see § Design decision
below), so it did not need the auth-gated-skip machinery `realclaude` built.

## Build tag

```go
//go:build e2e_liverelay
```

Single tag, no alternation, one file
(`internal/e2e/liverelay/liverelay_test.go`, ~753 LOC). All helpers are
file-local — nothing is exported, nothing widens `internal/e2e/harness.go`'s
build tags. This is a **transcription**, not a shared-import: the
daemon-spawn, Noise-driving, sleep-child-stand-in, and pair-decoding helpers
are copy-adapted from `internal/e2e/realclaude/interactive_bootstrap_liveness_test.go`
(the same transcription pattern established by #854/#997). The one piece
that *is* imported directly is [`internal/e2e/internal/fakephone`](fakephone-harness.md)
— it speaks the real phone-side wire protocol (not a protocol fake), so it
drives the real relay's `/v1/client` route unchanged, and is importable from
`internal/e2e/liverelay` under Go's `internal/` visibility rule (both rooted
at `internal/e2e/`).

## Design decision — a locally-built relay binary, not the production URL

The ticket left an (a)/(b) choice to the architect: (a) spawn a locally built
`pyrycode-relay` from the sibling repo, or (b) target the production URL with
a pre-provisioned test device identity. **Chosen: (a).** The relay is
stateless and credential-free — it "routes by the `x-pyrycode-server` header
and never reads message payloads" and does not validate `x-pyrycode-token`
against any backing store (the Noise handshake between phone and daemon is
the real auth) — so a hermetic local relay needs no database, no
provisioning, no secrets. It runs behind `--insecure-listen` (plain HTTP, no
autocert, no domain), and its boot-time production guards
(`CheckInsecureListenInProduction`, `CheckRunningAsRoot`,
`CheckSingleInstance`) only fire on production env vars or Fly — a clean
local env starts cleanly. This keeps `make preship` fully offline-capable and
collapses the ticket's mandated security review to a near-no-op (see the
architecture spec's § Security review — verdict PASS, no findings beyond a
baked-in loopback-bind MUST-FIX). Option (b) — a persisted production device
identity — is explicitly deferred; a future ticket that adds it must audit
persisted-key custody and production reachability separately.

## Why the fakerelay round-trip body ports unchanged

The test ports `testV2DaemonListConversationsRoundTrip`
(`internal/e2e/relay_v2_daemon_test.go`) almost verbatim, swapping only the
relay (`fakerelay.New(...)` → a spawned `pyrycode-relay` binary). Three facts
make that swap safe:

1. **The daemon always speaks Noise v2** — `startRelay` (`cmd/pyry/relay.go`)
   unconditionally calls `startRelayV2`; there is no v1/mixed-mode path.
   `PYRY_MOBILE_V2` is dead (no production `.go` file reads it).
2. **The daemon connects at `/v1/server`.** `resolveDialURL`
   (`internal/relay/connection.go`) appends `/v1/server` to a path-less base
   URL. The real relay serves exactly `/v1/server` (binary) and `/v1/client`
   (phone) — so the daemon is pointed at a **bare base URL**
   (`ws://127.0.0.1:<port>`, no `/v2/server` suffix). The fakerelay test's
   `/v2/server` suffix only worked because fakerelay maps both paths to one
   handler; the real relay has no `/v2/server` route.
3. **`fakephone` speaks the real phone wire** — it is a device stand-in, not
   a protocol fake, so it drives the real relay's `/v1/client` route
   unchanged when imported directly.

The relay is a dumb, stateless router: the Noise IK handshake and the verb
round-trip ride end-to-end *inside* the routing frames it forwards without
inspecting.

## The round-trip

`TestLiveRelay_ListConversationsRoundTrip`, one test function, any stage
error or a reply that doesn't match the seeded conversation fails the test
(not a no-panic smoke check):

1. **Resolve the relay binary** (`ensureRelayBuilt`, `sync.Once`-cached) —
   `PYRY_LIVERELAY_BIN` (prebuilt path) → `PYRY_RELAY_REPO` (sibling checkout
   to `go build` from) → default `../pyrycode-relay` next to this repo. None
   found → `t.Skipf` with a named diagnostic (both env vars, the expected
   path, `git clone` command) so `make preship` stays green on a machine
   without the sibling.
2. **Start the relay** (`startRelay`) on a free **loopback-bound**
   (`127.0.0.1`, never `0.0.0.0`/bare `:port`) listener with
   `--metrics-listen=` (disables the default metrics bind, avoiding a port
   conflict), polling `/healthz` for HTTP 200 before proceeding. Loopback
   binding is load-bearing for the security review — the ephemeral plaintext
   relay must never be reachable off-box.
3. **Mint an ephemeral device identity** under a fresh temp `HOME` via
   `pyry pair`, decoded into `pair.Payload` (bearer token + the relay's
   static pubkey). Destroyed by `t.Cleanup(os.RemoveAll)` — nothing persists
   past the test.
4. **Seed one conversation** in `conversations.json`, mirroring
   `testV2DaemonListConversationsRoundTrip` verbatim.
5. **Spawn the daemon** with the sleep-claude stand-in and the bare base
   relay URL; wait for its control socket.
6. **Connect the phone** via `fakephone.Dial`, gated on `waitBinaryRegistered`
   (see § Readiness divergence below).
7. **Drive the Noise IK handshake** to open state, then round-trip a
   `list_conversations` envelope and assert the reply's type, `InReplyTo`,
   and payload match the seeded conversation.

## Readiness divergence from `fakerelay`: `/healthz`, not dial-retry

`fakerelay` exposes `WaitBinary(serverID)` to close the
daemon-registers-then-phone-dials race. The architecture spec proposed a
dial-retry loop as the real-relay analog, but the shipped implementation uses
something more precise, found during development: the real relay's
`ClientHandler` **accepts** the `/v1/client` WebSocket upgrade first and only
*then* closes with WS code 4404 if no binary is bound to that server-id — so
a bare `fakephone.Dial` retry loop cannot distinguish "not ready yet" from
"the upgrade succeeded, awaiting the 4404". `waitBinaryRegistered` instead
polls `/healthz`'s `connected_binaries` field (unauthenticated, and — unlike
`/v1/*` — **not** rate-limited) until it reaches 1, which is the relay's own
authoritative registration signal and doesn't risk exhausting the per-IP rate
limiter the dial-retry approach would have hit. This is the one operational
difference from the fakerelay body, and the only deviation from the
architecture spec's design — see [`codebase/968.md`](../codebase/968.md).

## What it requires — no credentials

Unlike `e2e-realclaude`, this suite needs **no** secrets and spends **no**
real resources: hermetic local relay, ephemeral in-test device identity,
loopback-only network. The only prerequisite is the sibling
`pyrycode-relay` checkout (or a prebuilt binary/repo path via the two env
vars above).

## Make target

```make
.PHONY: e2e-liverelay
e2e-liverelay:
	$(GO) test -tags e2e_liverelay ./internal/e2e/liverelay/...
```

Wired into **`preship`** (`check → e2e-realclaude → e2e-liverelay`) — offline
and credential-free, so it doesn't compromise `preship`'s ability to run
without network access when the sibling checkout is present. **Not** part of
`check` or the `e2e` target; the `e2e_liverelay` tag keeps it invisible to
`go build`/`go vet`/`make test`/`make e2e`.

## Related

- [features/e2e-harness.md](e2e-harness.md) — the fake-daemon suite this
  borrows `relay_v2_daemon_test.go`'s round-trip body from.
- [features/e2e-realclaude.md](e2e-realclaude.md) — the structural precedent
  (opt-in tag, sibling package, wired into `preship` not `check`) and the
  source of the transcribed daemon-spawn/Noise-driving helpers.
- [features/fakerelay-harness.md](fakerelay-harness.md) — the in-process
  relay every other relay e2e test uses; `WaitBinary` is the hook this
  suite's `waitBinaryRegistered` replaces with a real-relay-native signal.
- [features/fakephone-harness.md](fakephone-harness.md) — the phone client
  imported directly (not transcribed).
- [features/mobile-live-e2e-runbook.md](mobile-live-e2e-runbook.md) — prior
  art for an operator-run live-infra variant (the option-(b) shape this
  ticket deliberately did not choose).
- `docs/release-tooling.md` § Live-relay smoke test — operator-facing run
  instructions.
- Ticket [#968](https://github.com/pyrycode/pyrycode/issues/968) — spec at
  [`specs/architecture/968-liverelay-smoke-test.md`](../../specs/architecture/968-liverelay-smoke-test.md);
  codebase note at [`codebase/968.md`](../codebase/968.md); PR
  [#1052](https://github.com/pyrycode/pyrycode/pull/1052).
