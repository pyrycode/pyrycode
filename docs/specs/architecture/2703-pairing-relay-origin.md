# #2703 — pairing codes carry the relay origin, not the daemon's /v1/server URL

## Files read

- `cmd/pyry/pairing_mint_v2.go` → `newPairingMinterV2`, `encodePairing`: where the daemon's relay URL enters every minted pairing. The only production file this ticket changes.
- `cmd/pyry/relay.go` → `resolveRelayURL` (returns the flag/env/config value verbatim) and the `pairingMinter := newPairingMinterV2(` construction, which passes `w.relayURL`. Unchanged: the dial leg keeps the full URL.
- `internal/relay/connection.go` → `resolveDialURL`: appends `/v1/server` to a bare origin and passes an explicit path through, so the daemon dials either form. Its table in `connection_test.go` ("base no path appends", "explicit v1 passthrough") already covers the second acceptance criterion.
- `internal/pair/payload.go` → `Payload.Relay`: not parsed by the pair package, only checked non-empty.
- `cmd/pyry/pairing_mint_v2_test.go` → `pmTestRelayURL` and the fixtures in `TestLocalPairingProvider_BindsEachControlSocketToItsDaemonState`: both use URLs with a path and assert the pairing carries them verbatim, so they change with the contract.

## Change

`newPairingMinterV2` stores `relayOrigin(relayURL)` instead of `relayURL`. `relayOrigin` parses the URL and returns only its scheme and host (`u.Host`, which keeps any port), dropping path, query, fragment and userinfo, because the phone reads `relay` as a `wss://host[:port]` origin and appends `/v1/client` itself. A value that does not parse or lacks a scheme or host is returned unchanged: the minter has nothing better to offer, and the dial leg would already have refused it. The daemon's own `w.relayURL` and `resolveDialURL` are untouched, so dialling still accepts both the bare origin and `.../v1/server`.

## Testing strategy

- New table test `TestRelayOrigin`: `wss://host/v1/server` → `wss://host`; `wss://host:8443/v1/server` → `wss://host:8443`; bare origin and trailing slash → origin; query and userinfo dropped; unparseable or host-less value passes through.
- `TestPairingMinterV2_PermittedMint` keeps its `/v2/phone`-pathed fixture URL and now asserts the decoded `relay` is the origin, so the minter's wiring is pinned end to end, not just the helper. The two-daemon local-provider test asserts the same against each fixture's expected origin.
- Dialling either form: existing `resolveDialURL` table in `internal/relay/connection_test.go`.
