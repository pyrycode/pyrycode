# Spec — #1021: move Noise handshake/hello out of `v2session.go`

**Size:** S (confirmed — pure mechanical move; 2 production files, ~0 net new logic, 0 call-site changes).
**Security-sensitive:** No. Not labelled `security-sensitive`; a same-package carve-out with no behaviour change, no new trust boundary, and no new design to audit. Touching Noise code does not by itself earn the label — there is nothing to attack that did not already exist byte-for-byte on `main`.

## Context

`internal/relay/v2session.go` is 3744 lines / ~76 top-level decls — the largest readability liability flagged in the 2026-07-15 full-repo review. This is the **first** slice of the #964 split: carve out exactly one concern — **inbound Noise session establishment** (the initiator handshake + hello/capability negotiation) — into a new file in the *same* `package relay`.

It is a **pure move**. No exported identifier changes; no logic changes; because every declaration stays in `package relay`, **no call site anywhere in the tree changes** — Go resolves package-level identifiers across files regardless of which file they live in.

## Files to read first

- `internal/relay/v2session.go:53-57` — `maxNoisePayloadBytes` const + doc (**Move block A**).
- `internal/relay/v2session.go:1253-1585` — the contiguous handshake/hello block (**Move block B**): `decodeInnerFrameV2`, `InnerFrameV2Decoded`, `supportedV2Capabilities`, `negotiateCapabilities`, `handleNoiseInit`. `decodeInnerFrameV2`'s doc comment starts at line 1253; `handleNoiseInit`'s closing brace is line 1585.
- `internal/relay/v2session.go:1587-1706` — `handleRekeyInit`. **STAYS.** This sits *between* the two move blocks; the move is therefore not one contiguous range. Do not move it — it belongs to the later re-key slice.
- `internal/relay/v2session.go:1702-1782` — `handleNoiseMsg` (**Move block C**): doc comment starts at 1702, closing brace at 1782, immediately before `dispatchAppFrame`'s doc at ~1793.
- `internal/relay/v2session.go:1-22` — the import block; the new file's imports are a strict subset (see § Import hygiene).
- `internal/relay/v2session.go:305-420` — `V2Session` struct + field doc-comments referencing `handleNoiseInit`'s token-OK path (`device`, `interactive`, `peerStatic`). Read-only context: these fields STAY; the moved code mutates them by name (same package).
- `internal/relay/auth.go:23-27` — `MsgInvalidToken` const. STAYS. `handleNoiseInit` (line 1501) and `handleNoiseMsg` (line 1747) reference it by name; same package, no import, no edit.
- `internal/relay/v2bundlestream.go` — references `maxNoisePayloadBytes`. STAYS UNTOUCHED. See § "Do not touch v2bundlestream.go".
- `CODING-STYLE.md` § Git Conventions — "One concern per commit"; this is a single move commit.

## Design

### What moves, into what

Create `internal/relay/v2session_handshake.go` (`package relay`). Relocate, **verbatim** (including doc comments), exactly these declarations:

| Decl | Kind | Current location |
|---|---|---|
| `maxNoisePayloadBytes` | const | 53–57 (block A) |
| `decodeInnerFrameV2` | func | block B |
| `InnerFrameV2Decoded` | type | block B |
| `supportedV2Capabilities` | var | block B |
| `negotiateCapabilities` | func | block B |
| `handleNoiseInit` | method | block B (ends 1585) |
| `handleNoiseMsg` | method | block C (1702–1782) |

Suggested in-file order for the new file (groups the decode/type/negotiation helpers above the handlers that use them, matching the existing top-to-bottom reading order): `maxNoisePayloadBytes` → `decodeInnerFrameV2` → `InnerFrameV2Decoded` → `supportedV2Capabilities` → `negotiateCapabilities` → `handleNoiseInit` → `handleNoiseMsg`. Order is cosmetic (Go is order-independent within a package); keep it readable.

### What explicitly STAYS in `v2session.go`

- `handleRekeyInit` (1587–1706) — re-key concern, a later slice. It physically separates move blocks B and C, so the developer performs the move as **three cut operations** (block A in the preamble; block B = 1253–1585; block C = 1702–1782), leaving `handleRekeyInit` in place.
- `handleFrame` (1194) and `dispatchAppFrame` (1801) — the router; both call moved decls (`decodeInnerFrameV2`, dispatch to `handleNoiseInit`/`handleNoiseMsg`). Same-package calls, no edit.
- `sealError` (3154), `marshalInnerFrameV2` (3183), the close-code const block (27–52), all timer/rekey vars, and the manager core — untouched.

### Cross-file references are all in-package (why nothing else changes)

Every consumer of a moved symbol is either in `package relay` or imports `relay.<Exported>`:

- `handleFrame` → `decodeInnerFrameV2`; `handleRekeyInit` → `InnerFrameV2Decoded` (param type) — same package, resolve fine.
- `v2bundlestream.go`, `v2bundlestream_test.go` → `maxNoisePayloadBytes` — same package.
- `v2session_test.go`, `v2session_queuereconcile_test.go`, `v2session_modalreconcile_test.go` → `supportedV2Capabilities`, `negotiateCapabilities`, `handleNoiseInit` — same package.
- `internal/e2e/*_test.go` → `handleNoiseInit` — imports `relay.…`; the exported/unexported status and the package are unchanged, so the import still resolves. (`handleNoiseInit` is unexported; the e2e refs are via same-package test wrappers or exported seams — not this ticket's concern, and unaffected by a same-package move.)

None of these files may be edited — AC#3.

## Import hygiene (the one green-build gotcha)

**No import goes stale in `v2session.go`.** The two imports the moved code uses that could plausibly become unused both retain other users in the staying code:

- `slices` — still used at line 223 (`pushQueue.enqueue` → `slices.Delete`). Stays.
- `internal/noise` — still used by the `V2Session` struct fields (308–310), `NewV2SessionManager` (959–961), and `handleRekeyInit`. Stays.

So the developer must **not** remove any import from `v2session.go`.

**The new file needs its own import block** — a subset of the parent's. Expected set (let `goimports -w internal/relay/v2session_handshake.go` or the compiler be the source of truth):

```
context
encoding/json
fmt
slices
github.com/pyrycode/pyrycode/internal/noise
github.com/pyrycode/pyrycode/internal/protocol
```

`websocket` and `devices` are **not** expected: the moved code references `StatusProtocolMismatch`/`StatusHandshakeFailure` (package-level consts in `v2session.go`) and the `*devices.Device` value returned by `Devices.Validate` (type-inferred) by name, not via those packages directly.

## Do not touch `v2bundlestream.go`

`maxNoisePayloadBytes` moves to the new file but stays in `package relay`, so `v2bundlestream.go`'s reference resolves unchanged. AC#3 forbids editing it. The developer should resist the reflex to "follow" the const with an import fix there — none is needed.

## Concurrency model

Unchanged. The single-owner-goroutine invariant (all of `s.send`/`s.recv`/`s.state`/`s.device`/`s.interactive` mutated only on the manager's Run dispatch goroutine) is a property of *when* these functions are called from `handleFrame`, not of *which file* they live in. The move preserves it verbatim.

## Error handling

Unchanged. Every close-code path (4421 `StatusProtocolMismatch`, 4426 `StatusHandshakeFailure`), the `auth.invalid_token` gating branch in `handleNoiseMsg`, and the `MsgInvalidToken` reply in `handleNoiseInit` move byte-for-byte. No new failure mode is introduced or removed.

## Testing strategy

No new tests; no test file changes (AC#3). Verification is the existing suite proving behavioural identity:

- Incremental, fast: `go build ./internal/relay/` after each cut to catch a mis-scoped boundary early (e.g. accidentally including `handleRekeyInit`, or leaving a decl behind).
- `go vet ./internal/relay/` and `staticcheck ./internal/relay/` — catch any accidentally-unused import.
- Full gate once at the end: `make check` (vet, `go test -race`, staticcheck, substrate-guard, e2e) must be green (AC#4). The existing `TestNegotiateCapabilities` (`v2session_test.go:4143`) and the handshake/reconnect e2e tests exercise the moved code unchanged.
- Confirm the diff touches exactly two files: `git diff --name-only` must list only `internal/relay/v2session.go` and `internal/relay/v2session_handshake.go` (AC#3).
- `gofmt`/aligned-comment check: the moved doc comments contain aligned `slog` field lists; preserve alignment on paste (gofmt 1.26 reflow is not CI-gated, so keep the blocks as-is).

## Open questions

None. Boundaries, ordering constraint (`handleRekeyInit` stays between blocks), and import set are all resolved above. The developer's only judgement call is the exact first/last line of each cut, which `go build ./internal/relay/` verifies in seconds.

## Note for the record — branch-overlap false positive

The §1.5 branch-overlap scan flagged `origin/feature/449` as also touching `v2session.go`. Verified **false positive**: issue #449 is CLOSED (2026-05-17), has no open PR, and `feature/449` is 1134 commits behind `main` (4 stale commits, last 2026-05-17). Its work — the re-key responder `handleRekeyInit` — is already on current `main` as part of the **staying** code this move leaves untouched. Not a live merge target; no block set. No region overlap even conceptually, since this ticket does not move `handleRekeyInit`.
