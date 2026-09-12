# Ticket #2394: Live-relay paired-device fixture

## Files read

- `internal/e2e/liverelay/liverelay_test.go` → `TestLiveRelay_ListConversationsRoundTrip`, `runPyryPair`, `decodePairPayload` — contains the one pre-daemon CLI setup site and the authenticated relay round trip that must remain intact.
- `internal/e2e/internal/paireddevice/paireddevice.go` → `Config`, `Setup` — defines the test-only fixture contract and returns the relay, token, server identity, and static public key needed by the phone.
- `docs/knowledge/features/e2e-liverelay.md` → “The round-trip” and “What it requires” — identifies the documentation-stage wording that must change from CLI pairing to fixture seeding.
- `docs/knowledge/features/development-verification.md` → “Establish the change surface” and “Test execution and artifact survival” — requires a source check for the removed setup dependency and compilation of the tagged package.
- `docs/protocol-mobile.md` → “Pairing”, “Handshake”, and “Security model” — defines the credential, Noise trust anchor, and relay threat boundaries preserved by the migration.

## Change

In `TestLiveRelay_ListConversationsRoundTrip`, call `paireddevice.Setup` after the real relay starts, using the isolated home, instance `test`, device label `phone-a`, and `relay.baseWS`. Pass the returned payload's relay to `spawnDaemon` and `fakephone.Dial`, and retain its token and decoded static public key for the existing Noise handshake. Remove `runPyryPair`, `decodePairPayload`, and their now-unused `pair` import. No daemon, relay, protocol, or credential behavior changes; only the test's pre-daemon setup mechanism moves behind the test-only fixture.

## Testing strategy

- RED: add a tagged source assertion that rejects the live-relay test while it still declares or calls `runPyryPair` or `decodePairPayload`, and requires a `paireddevice.Setup` call.
- GREEN: run the tagged live-relay package test. With no relay binary it must execute the source assertion and skip the round trip with its existing diagnostic; with the prerequisite available, `make e2e-liverelay` exercises the authenticated exchange.
- Run the builder-owned gates: `go test -race ./internal/e2e/liverelay/...`, `go vet ./...`, and `go build ./cmd/pyry`.

## Documentation handoff

Pending for the documentation stage: update `docs/knowledge/features/e2e-liverelay.md` under “The round-trip” and “What it requires” to describe `paireddevice.Setup` seeding instead of CLI pairing.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `paireddevice.Setup` is an internal test-only boundary receiving test-owned values; the network boundary and authenticated dispatch remain in `TestLiveRelay_ListConversationsRoundTrip`.
- [Tokens, secrets, credentials] No findings — `Setup` generates the token with `crypto/rand`, persists only its hash through `devices.Registry.Save`, and returns plaintext only in memory for the existing phone handshake; the test does not log it.
- [File operations] No findings — the isolated absolute home comes from `t.TempDir`; `Setup` delegates key, identity, and registry persistence to the existing 0700-directory, 0600-file, atomic-write stores.
- [Subprocess execution] No findings — the change removes the `pyry pair` subprocess and introduces no command execution or environment inheritance.
- [Cryptographic primitives] No findings — `Setup` uses the established X25519 key store and `crypto/rand`; the existing Noise IK handshake continues to pin the returned static public key.
- [Network & I/O] No findings — the same loopback-only real relay remains in use, and one `relay.baseWS` value is shared by fixture, daemon, and phone without widening listeners or changing frame reads.
- [Errors, logs, telemetry] No findings — setup errors fail the test without printing the returned credential, and the existing relay/daemon diagnostics remain unchanged.
- [Concurrency] No findings — the fixture is synchronous and adds no goroutine, lock, shutdown, or shared-state behavior.
- [Threat model alignment] No findings — the migration preserves the protocol security model's Noise authentication, hashed device-token registry, static-key trust anchor, and known transient relay exposure of the opaque token header; no production threat boundary changes.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-13
