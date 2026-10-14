# Connect-gap fixture synchronization

## Files read
- `cmd/pyry/daemon_live_relay_rework_test.go` → `TestDaemonLiveRelayPublisherConnectGap`: held publisher and stale/source assertions.
- `cmd/pyry/agent_switch_frames_test.go` → `switchWire.open`, `switchWire.read`: handshake response precedes snapshot capture; reads have bounded waits.
- `cmd/pyry/daemon_live_relay_test.go` → `testDaemonRelayWire`: starts manager/publisher and joins both during cleanup.
- `cmd/pyry/daemon_live_relay.go` → `daemonRelaySnapshot`, `startDaemonLivePublisher`: finite detached snapshot and per-reading recipient enumeration.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit`: sends the response before opening and capturing supplied live state.
- `internal/protocol/interactive_session.go` → `ModelAnnouncedPayload`: exact model and conversation assertion surface.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md` § Relay delivery: a later connection needs the replacement after its overtaken snapshot.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: synchronization must observe the operation being ordered.
- `CODING-STYLE.md`: channel coordination, bounded test waits, cleanup.

## Context
The fixture assumes receiving the Noise response completes snapshot capture. It can therefore admit `old` before the first snapshot or resume held delivery before the second snapshot admits `fresh`, causing a false stale-reading failure. One test-only deliverable; no production change or decision record needed.

## Design
Signal the first capture and the second capture/admission from the existing `ThreadLiveState` wrapper. Await the first signal before admitting `old` and the second before releasing held delivery, using bounded waits. Preserve capture-before-`fresh` inside the second callback and the publisher hold. Decode the received model payload and require `fresh`, conversation `conv`, and session `a` for both named peers. No concurrent feature branch touches this test file at planning time.

## Concurrency model
The relay Run goroutine owns the snapshot callback counter and signals capture completion by closing channels. The test goroutine waits with deadlines. The existing publisher hold escapes on release or cancellation; its idempotent deferred release runs before fixture cleanup joins the publisher. Add no goroutines or production state.

## State transitions and identity reuse
| Event | Race test |
|---|---|
| First peer captures empty state before admission of `old` | `TestDaemonLiveRelayPublisherConnectGap` |
| Second peer captures `old`, admits `fresh`, then held delivery resumes | `TestDaemonLiveRelayPublisherConnectGap` |
| Both peers receive the replacement with the same conversation/session identity | `TestDaemonLiveRelayPublisherConnectGap` |

## Error handling
Missing capture/admission signals fail within three seconds. Missing replacement still fails the bounded wire read; stale or wrong-source/conversation readings fail immediately. Preserve cancellation and deferred release paths.

## Testing strategy
Run the existing race test before edits to observe the failure, then repeat it 100 times after synchronization. Run race tests for `cmd/pyry`, `go vet ./...`, and `go build ./cmd/pyry`. The verifier owns the required complete `make check` and `make build` gates; do not duplicate its full-module gate here. Expected written work is under 100 lines, zero exported types/interfaces, zero consumer updates, two acceptance criteria, and no production reject branches: all five sizing limits hold.

## Open questions
None.
