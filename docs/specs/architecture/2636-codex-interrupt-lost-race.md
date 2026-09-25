# #2636 — Codex interrupt that loses the race to the turn's own completion

## Files read

- `cmd/pyry/codex_runner.go` → `codexRunner.Interrupt` (the change), `codexRunner.notify` (clears `turnID` on `turn/completed`, on the client's read loop).
- `internal/codexsup/client.go` → `Client.call` (wraps the transport error with `%w`; a dead client yields `exitError`, not an RPC error), the `OnNotification` registration in `NewClient` (notifications call back inline).
- `internal/acp/acp.go` → `Transport.handleLine` / `dispatchNotification` / `routeResponse`: one read loop, notifications dispatched inline before the next line is scanned, so a notification that precedes a response on the wire has run its callback before the response reaches `Call`. `internal/acp/jsonrpc.go` → `*acp.Error`, what an error response surfaces as.
- `internal/e2e/internal/fakecodex/main.go` → `server.turnInterrupt`, `turn.run`, `turn.complete`, `server.send` (serialised by `writeMu`, never takes `s.mu`).
- `cmd/pyry/codex_approval_test.go` → `TestCodexApproval_InterruptDeclines` (the flake); `cmd/pyry/codex_runner_test.go` → `TestCodexRunner_InterruptEndsRunningTurn` (pattern for the new test).

No in-flight branch touches these files.

## Change

**Fake.** In `turn.run`'s completed path, hold `s.mu` across deleting the turn from `s.turns` *and* sending `turn/completed` (`send` only takes `writeMu`, so no lock-order issue). `turnInterrupt` looks the turn up under `s.mu`, so it now sees either the turn still present (interrupt succeeds; the turn then completes normally) or the turn gone with `turn/completed` already written — its `-32602` response is written after the notification.

**Runner.** When `client.Interrupt(ctx, turnID)` fails, `Interrupt` returns nil if Codex answered with an RPC error (`errors.As(err, *acp.Error)`) and the tracked `r.turnID` is no longer `turnID` (cleared or replaced). Because notifications run inline on the read loop, a `turn/completed` that precedes the error response has already cleared `turnID` when `Call` returns. Any other failure — the turn still tracked, a timeout, a dead client (`exitError`, not an `*acp.Error`) — is returned. The RPC-error gate is on the *kind* (Codex answered), not on the code or message; transport failures are excluded because the wire-order argument does not hold for them. The decline-before-interrupt order is unchanged.

## Testing strategy

- `TestCodexApproval_InterruptDeclines` at `-count=300` is the proof for the race: base measured 3/300 failures on this branch's base; after, 300/300.
- New `TestCodexRunner_InterruptRefusedReturnsError` in `codex_runner_test.go` beside `TestCodexRunner_InterruptEndsRunningTurn`: with a client bound and no turn running, set `r.turnID` to an id the fake never minted; `Interrupt` returns the fake's refusal because the tracked id did not move. RED against a fix that swallows every error.

## Documentation handoff

None required by the ticket. Pending for the documentation stage, optional: `docs/knowledge/features/codexsup-package.md` (or the cmd/pyry Codex runner section) could note that `Interrupt` returns nil when the turn completes before Codex handles `turn/interrupt`.
