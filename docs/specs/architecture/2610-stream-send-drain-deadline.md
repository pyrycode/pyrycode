# #2610 — StreamSendMessageDrainsTurn: load-tolerant waits and honest timeout diagnostics

## Files read

- `internal/e2e/relay_v2_stream_send_test.go` → `TestRelayV2_StreamSendMessageDrainsTurn` — the only file this ticket changes: its 15 s ack deadline, 20 s drain deadline, and the M1 message that blames a UUID mismatch it never checked for.
- `cmd/pyry/stream_turn_drain.go` → the drain gate in `startStreamTurnDrainV2` — logs `stream_turn.not_active` (Debug, with `session_id`) for every event it drops because its tag is not `activeSession()`. That record is the observable symptom of a seed UUID mismatch.
- `internal/e2e/harness.go` → `StartStreamInteractiveWithRelay` — runs the daemon with `-pyry-verbose`, so Debug records (including the gate drop) land in `h.Stderr`.
- `internal/e2e/per_conversation_eviction_test.go` → `stderrTail` — existing helper that the M1 failure attaches the daemon log tail with; its own M1 message already names `stream_turn.not_active` as the discriminant.
- `internal/e2e/relay_v2_stream_new_session_test.go` → `testStart` / `elapsed` stamps (#1273) — the elapsed-stamp pattern this ticket mirrors; its turn-two deadline comment records why a longer deadline cannot fix a genuine stall.
- `internal/e2e/internal/fakerelay/fakerelay.go` → the "binary referenced unknown conn_id" Debug line — checked in the repro log as the ticket's technical note asks.

## Change

**Approach: bump the send test's waits** (the first of the ticket's two options), not a shared value. A shared value would move `relay_v2_stream_new_session_test.go`'s turn-drain waits too, and that file carries a deliberate "this deadline stays where it is" decision (#1273) that a shared constant would silently override. Only the send test has an observed failure.

In `TestRelayV2_StreamSendMessageDrainsTurn`:

1. One named local `streamWait` (60 s) replaces both the 15 s ack deadline and the 20 s drain deadline. The v0.26.0 failure burned a full 20 s drain with the package at 224 s under `-race`; 60 s is three times the old drain budget. A turn that is never delivered still fails, at 60 s instead of 20 s. The 3 s dial context is untouched (no observed failure there).
2. `testStart` / `elapsed()` stamps as in the new_session spec; every milestone `t.Logf` and every timeout `t.Fatalf` carries the elapsed time, and each timeout names what it waited for and for how long (`streamWait`).
3. The M1 timeout no longer asserts a UUID mismatch blindly. It checks `h.Stderr` for a `stream_turn.not_active` record: if present, the gate dropped this turn's events, and the message names the seed-UUID mismatch and quotes the drop line; if absent, the message says the turn was never delivered/drained within the wait and says the gate dropped nothing. Both attach `stderrTail`.

Nothing else moves: no production code, no shared helper, no sibling spec.

## Root cause (for the PR)

Hypothesis the fix acts on: the turn drains too slowly under full-suite `-race` load, it does not fail to drain. A UUID mismatch cannot come and go between runs of the same code, and the IDs are constants seeded by the test. The repro attempt runs `go test -tags e2e -race -count=1 ./internal/e2e/...` (the failing run's load) and inspects the log for `stream_turn.not_active` and fakerelay "unknown conn_id" lines. The PR records whether it reproduced.

## Testing strategy

The change is to a test's waits and messages; no new logic under test. Proof: the send test passes alone and within the full `./internal/e2e/...` `-race` run. The non-delivery path keeps its existing guard (M1/M2 fatals) — only the deadline length and message change.

## Documentation handoff

None. The ticket has no documentation requirements.

## Revisions

### 2026-09-25 — root cause is a discarded frame, not slowness

Found during Phase B, driven by the #2612 lesson ("ack-await frame-drop trap"). The
send test awaited the ack in its own loop that skipped every non-ack envelope, then
drained M1/M2 in a second loop. The ack is not ordered before the turn's frames
(the handler enqueues, then acks). Measured with a temporary diagnostic over 45
runs of the test alone (15 at default, 30 with `GOMAXPROCS=1`, all `-race`): the
`assistant_delta` trailed the ack by 2–5 ms every time. If it overtakes the ack,
the ack loop discards it and the drain waits out its whole deadline for a frame
already consumed. The v0.26.0 failure fits that shape: 20.73 s total against a
20 s drain deadline puts the pre-drain prefix at about 0.73 s, where a lone run
takes about 0.13 s. So setup was about five times slower under load, and the
drain would have had to be thousands of times slower to miss 20 s.

**New contract:** one loop awaits the ack, M1 and M2 and records each milestone
whenever it arrives. It ends when the ack and the terminal idle have both been
seen, with the idle only counted after the delta. `streamWait` (60 s) now bounds
that whole send→idle exchange. The two separate waits are gone. The timeout names
whichever milestone is still pending: ack, M1 or M2. The ack timeout also lists
the envelope types seen before it.

**Also observed:** re-seeding either `seedBootstrapRegistry` or
`seedBoundConversation` with a different UUID still drains green. The header
comment's "a mismatched UUID hangs the drain" claim is stale, so it now says the
M1 failure checks the gate-drop record instead of assuming a mismatch.

**Not reproduced:** a full `go test -tags e2e -race -count=1 ./internal/e2e/...`
run on the unchanged test passed. The package took 142 s there, against 224 s in
the failing run.
