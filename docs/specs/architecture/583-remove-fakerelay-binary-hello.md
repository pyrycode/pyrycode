# Spec #583 — Mobile v2: remove the now-dead fakerelay binary-hello path

**Size:** XS (deletion-only). One production file (`fakerelay.go`) + one test file. ~90 lines removed from production, ~49 from tests, 0 new exported types, 0 new behaviour. No consumer cascade — every reference is inside the two files being edited.

## Files to read first

- `internal/e2e/internal/fakerelay/fakerelay.go:27-39` — package-doc deviation bullets; the binary-direct-hello bullet (the last one) is dead doc to remove.
- `internal/e2e/internal/fakerelay/fakerelay.go:82-88` — `lastBinaryHello` field + its doc comment (remove).
- `internal/e2e/internal/fakerelay/fakerelay.go:136-141` — `New` constructor; remove the `lastBinaryHello: make(...)` init line.
- `internal/e2e/internal/fakerelay/fakerelay.go:391-448` — `binaryRecvPump`; the `if env.ConnID == ""` dispatch branch (409-421) calls `handleBinaryDirect` and is the binary-direct entry point to remove. Note the existing unknown-conn_id Debug-drop (425-428) — it becomes the catch-all for any stray no-conn_id frame.
- `internal/e2e/internal/fakerelay/fakerelay.go:450-505` — `handleBinaryDirect` (remove entirely).
- `internal/e2e/internal/fakerelay/fakerelay.go:602-611` — `LastBinaryHello` accessor (remove).
- `internal/e2e/internal/fakerelay/fakerelay.go:613-642` — `WaitBinary`; the header-based readiness sync that **stays** and keeps `time` imported.
- `internal/e2e/internal/fakerelay/fakerelay_test.go:393-441` — `TestBinaryHello_GetsHelloAck`, the only test exercising the hello-ack synthesis + `LastBinaryHello` (remove the whole function).
- `internal/e2e/internal/fakerelay/fakerelay_test.go:201-300` — `TestPhoneToBinary_FrameWrappedWithConnID`, `TestBinaryToPhone_FrameUnwrapped`, `TestConnIDIncrementsPerPhone`; these route frames whose *content* is `{"id":1,"type":"hello"}` **with a conn_id** through the phone-leg path — they are NOT binary-direct hellos and MUST stay. They are the AC-3 "binary↔relay leg still reaches frame-forwarding" coverage. They also keep `protocol.RoutingEnvelope` (and thus the `protocol` import) live in the test file.
- `internal/relay/connection.go:1-16` — package doc confirming the dead-code premise: after #582 "there is no relay-originated hello/hello_ack handshake on this leg"; server-id is registered from the `x-pyrycode-server` header on WS upgrade.

## Context

Ticket #582 retired the binary↔relay `hello`/`hello_ack` handshake: the binary's outbound relay connection is now content-blind on that leg — the relay claims the server-id slot from the `x-pyrycode-server` upgrade header, no hello is exchanged. The fakerelay e2e harness still carries the *receiving* half of that retired handshake:

- `handleBinaryDirect`'s `hello`→`hello_ack` synthesis branch never fires (no binary sends a relay-leg hello).
- The `lastBinaryHello` map is never written.
- `LastBinaryHello` has no remaining readers — e2e readiness now syncs on the header-based `WaitBinary`.

This is dead code. Removing it keeps the harness honest: it should not carry machinery for a handshake the binary no longer performs. Split from #569.

## Design

Pure deletion in `internal/e2e/internal/fakerelay/fakerelay.go`, plus removal of the one test that covered it. No new types, no behaviour change, no signature changes. Concretely:

1. **Package doc (27-39):** delete the deviation bullet describing binary-direct `hello`→`hello_ack` synthesis. The other deviation bullets (HTTP-status rejections, no grace period, no TLS) stay.
2. **`Server` struct (82-88):** delete the `lastBinaryHello map[string]protocol.Envelope` field and its doc comment.
3. **`New` (140):** delete the `lastBinaryHello: make(map[string]protocol.Envelope)` initializer line. The `binaries` and `phones` maps stay.
4. **`binaryRecvPump` (409-421):** delete the entire `if env.ConnID == "" { … handleBinaryDirect … continue }` block **and its explanatory comment (409-415)**. This is the AC's "binary-recv-pump dispatch that calls it has no remaining purpose" case — after #582 the binary sends no no-conn_id frames on this leg, so the special-case is dead. A stray empty/unknown conn_id now falls through to the existing unknown-conn_id Debug-drop (425-428: log + `continue`), which already embodies the harness's "drop the offending frame, keep serving" philosophy (see the pump's own doc comment, 391-396). No frame ever reaches a nil phone — `s.phones[""]` is always a miss.
5. **`handleBinaryDirect` (450-505):** delete the entire method.
6. **`LastBinaryHello` (602-611):** delete the accessor and its doc comment.

In `internal/e2e/internal/fakerelay/fakerelay_test.go`:

7. **`TestBinaryHello_GetsHelloAck` (393-441):** delete the entire test function. It is the sole exerciser of both the hello-ack synthesis and `LastBinaryHello`.

### What stays (do not touch)

- **Header-based server-id registration** — `handleBinary` reads `X-Pyrycode-Server` and inserts into `s.binaries` (196, 266). Untouched; this is what makes `WaitBinary` work.
- **`WaitBinary`, `ForceCloseBinary`, `RejectNextBinaryWith4409`** — all server-side e2e hooks unrelated to hello.
- **Phone-leg handling** — `handlePhone`, `phoneRecvPump`, `phoneSendPump`, the `phoneSend`/`token`/`firstFrameSent` machinery. The phone↔binary hello travels as Noise early-data and never went through the binary-direct path (per ticket Technical Notes).
- **The three phone-leg frame-forwarding tests** (201-300) — they happen to carry `type:"hello"` *content* inside a conn_id-wrapped routing frame; that is ordinary phone traffic, not a binary-direct hello.

### Imports after removal

- **`fakerelay.go`:** no import changes. `protocol.RoutingEnvelope` is still used in `binaryRecvPump`/`phoneRecvPump`; `time` is still used in `WaitBinary`; `fmt`/`json` still used in the phone pumps. (`protocol.Envelope`/`TypeHello`/`HelloAckPayload`/`TypeHelloAck` references all lived inside the deleted code, but `RoutingEnvelope` keeps the package import alive.)
- **`fakerelay_test.go`:** no import changes. `protocol.RoutingEnvelope` survives at lines 222/253/257/292, so the `protocol` import stays. (`protocol.Envelope`/`TypeHello`/`TypeHelloAck` were only referenced inside the deleted test.)

The developer should still let `goimports`/`gofmt` + the compiler confirm — but the analysis above predicts zero import edits.

## Concurrency model

Unchanged. `serveBinary` still runs `binaryRecvPump` + `binarySendPump` under a per-conn errgroup-style fan-out; removing the binary-direct branch removes a code path *within* `binaryRecvPump`, not a goroutine. The `s.mu`-guarded maps lose one entry (`lastBinaryHello`) that was only ever touched under the existing lock — no lock-ordering or new-shared-state implications.

## Error handling

No new failure modes. The deleted `handleBinaryDirect` returned wrapped marshal errors and `ctx.Err()` on a blocked send; none of those paths can be reached anymore (no hello arrives). Stray no-conn_id frames are now dropped by the existing resilient unknown-conn_id path (Debug log + continue), matching how the harness already treats malformed wrappers and unknown conn_ids — a consuming test fails on a *missing expected receive* rather than on a harness-side teardown that would mask the real cause.

## Testing strategy

- Remove `TestBinaryHello_GetsHelloAck`; remove nothing else from the test file.
- Verify the surviving suite still covers the AC-3 invariants:
  - **`WaitBinary` coverage stays** — `TestWaitBinary` (happy + timeout) and `TestForceCloseBinary` (which calls `WaitBinary`).
  - **Binary↔relay leg reaches frame-forwarding** — `TestPhoneToBinary_FrameWrappedWithConnID` and `TestBinaryToPhone_FrameUnwrapped` exercise the full wrap/unwrap routing path end-to-end.
- Gates (AC-4):
  - `go test -race ./...` — passes (the fakerelay package is `//go:build e2e`; the default suite simply must not regress).
  - `go test -tags e2e ./internal/e2e/...` — the fakerelay unit tests and the relay round-trip e2e tests (`relay_roundtrip_test.go`, `relay_test.go`, `relay_v2_*`, etc.) pass, confirming real pyry binaries still complete their header-based relay registration without the synthesized hello_ack.
  - `go vet ./...` + `staticcheck ./...` — clean (no unused imports, no dead symbols left behind).

## Open questions

- **Dispatch-branch removal vs. explicit drop.** This spec removes the `if env.ConnID == ""` branch entirely (Design step 4), letting the existing unknown-conn_id Debug-drop absorb any stray no-conn_id frame. The minor cosmetic cost: such a frame logs "binary referenced unknown conn_id" with an empty `conn_id` rather than a binary-direct-specific message. After #582 this case should never occur, and the catch-all is the harness's established philosophy, so the simpler removal is preferred. If the developer finds the log message misleading enough to warrant it, a 3-line explicit `if env.ConnID == "" { s.log.Debug("dropping unexpected binary-direct frame", …); continue }` is an acceptable equivalent — but do not re-introduce any hello/ack handling or a `handleBinaryDirect` helper.
