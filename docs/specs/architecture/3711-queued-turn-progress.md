# Preserve queued same-generation turn progress

## Files read

- `cmd/pyry/daemon_live_source.go` → `acceptEventLocked`: captures thinking/responding and terminal idle under the producing generation.
- `cmd/pyry/daemon_live_state.go` → `capture`, `admitLocked`, `advanceLocked`, `remove`: immutable admissions, monotonic revisions and generation retirement.
- `cmd/pyry/daemon_live_relay.go` → `startDaemonLivePublisher`, `daemonLiveCurrent`, `installDaemonLiveRelay`: both delivery boundaries share the freshness predicate.
- `internal/relay/v2session_livestate.go` → `forwardLiveState`: recipient access gates, family clears and consumer revision watermarks remain authoritative.
- `cmd/pyry/daemon_live_publisher_test.go` → `TestDaemonLivePublicationRetirement`: bounded overflow and cancellation proof.
- `cmd/pyry/daemon_live_relay_test.go` → `testDaemonRelayWire`, `TestDaemonLiveRelayOrdering`: authenticated decrypted delivery and reused-ID retirement fixtures.
- `cmd/pyry/daemon_live_relay_rework_test.go` → `TestDaemonLiveRelayPublisherConnectGap`: publisher hold and snapshot synchronization pattern.
- `cmd/pyry/daemon_relay_wire_test.go` → `testDaemonRelayAdmission`: handshake response alone does not establish queue registration.
- `docs/knowledge/features/streamsup-package.md` and `streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md` § Relay delivery: detached captures must remain generation fenced; snapshots and borrowed inventories have distinct freshness contracts.
- `docs/knowledge/features/development-verification.md` and its prove-that-tests-distinguish-the-change topic: await producer enqueue completion before wire barriers.
- `CODING-STYLE.md`, `docs/knowledge/features/protocol-package.md`, `docs/protocol-mobile.md` § Security model: payload privacy, bounded transport and Noise single-writer invariants.

## Context

An admitted responding reading is dropped when idle replaces retention before publisher enumeration or relay delivery completes. Mobile clears session errors on recognized progress, so omitting this edge can leave an error visible. The controlled daemon failure does not establish the complete historical Mobile timeout sequence. This is one behavior with two interleavings, and its regression and retirement proof belong together. No decision record is needed.

## Design

Keep `daemonLiveCurrent(owner, reading) bool` as the shared delivery predicate. After checking current conversation and generation, permit a positive, already-admitted revision of a recognized thinking/responding `turn_state` at or below the retained turn-state revision. Decode the typed payload; unknown states and idle keep exact retained-revision equality. All other families, especially inventory/catalog replies, retain their existing predicates. Clears still require the current generation.

This exception preserves transient progress in the existing FIFO publication and relay queues. It does not replace retention: a fresh snapshot continues to contain only the latest reading. Relay consumer watermarks still discard duplicates and older revisions after newer delivery. No new exports, interfaces, queue, consumer migration or unbounded history is introduced.

Feature-branch overlap check after fetching origin found no other feature branch touching the intended production file or new regression file.

Sizing against both sketch and written plan: about 500 written lines (roughly 25 production, 400 tests/helpers, 75 plan/revisions), zero exported types, zero simultaneous consumer updates, two acceptance criteria, and fewer than ten distinct reject branches. Nearest analogue is #3611's initial 476 added lines including 400 test lines. The repair is within all five limits.

## Concurrency model

Reuse the publisher's single context-owned worker and the relay's single writer. The predicate reads owner state under its existing mutex, with no delivery or recipient enumeration under that lock. Test channel holds select cancellation and have idempotent release before fixture shutdown. All observations and decryptions happen on the test goroutine.

## State transitions and identity reuse

| Event | Race-enabled regression |
| --- | --- |
| Text or thought progress followed by idle immediately, during publisher enumeration, or during relay delivery | `TestDaemonLiveQueuedTurnProgress` checks ordered phases at callback and decrypted wire. |
| Retire held progress, reuse the same session ID, then publish successor state | `TestDaemonLiveQueuedTurnProgress` retirement cases check current-generation clears and idle without predecessor progress. |
| Remove/recreate the same conversation and session IDs while progress is held | `TestDaemonLiveQueuedTurnProgress` recreation cases check generation fencing at both boundaries. |
| Replay duplicate progress or older progress after idle was consumed | `TestDaemonLiveTurnProgressWatermark` checks no rollback through decrypted delivery. |
| New snapshot after queued progress ends | `TestDaemonLiveQueuedTurnProgress` verifies latest retained idle, without replaying the transient edge. |
| Publication overflow and shutdown/reinstallation | Existing `TestDaemonLivePublicationRetirement` and `TestDaemonLivePublicationShutdownInstallation`. |

## Error handling

Malformed/unknown turn phases do not receive the exception. Missing conversation, changed generation, zero/unadmitted future revision, or absent retained family reject progress. Existing push error handling, overflow retirement and cancellation remain intact. No payloads or private captures enter logs.

## Testing strategy

Write controlled tests first and retain their failing JSON run against unchanged production at `4f4c7fa1b5b23435d173a9bd345d7176cb124e38`. Use channel barriers, collect through terminal idle, and compare the whole phase sequence; immediate cases wait for observed progress before admitting idle. Publisher holds capture actual push callback deliveries; relay holds wrap the installed `LiveStateCurrent` before its owner check and observe authenticated decrypted frames. Retirement cases admit successor idle and require generation clears before it. Verify latest snapshots, monotonic replay rejection and the narrow freshness exception. Run the affected `cmd/pyry` race tests under managed admission, `go vet ./...`, and build `./cmd/pyry` to scratch. Record leaf counts for before/after proof in the PR. Full-module and downstream Mobile acceptance remain dispatcher-owned. Linux is the verification OS; macOS is outside this ticket's scope.

## Open questions

None. Mobile's clearing contract, both fixture arms and original 20-second deadline remain owned by Mobile #2338. Its native correlation and #2327's complete scripted/UI acceptance remain pending downstream.

## Documentation handoff

- Pending documentation stage: in `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md`, section “Relay delivery”, explain that admitted queued same-generation turn progress survives a later idle while generation retirement and consumer revision watermarks still reject stale delivery; distinguish transient delivery from latest-state snapshots and inventory freshness. Record only the demonstrated daemon cause, leaving Mobile native correlation/acceptance pending.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `acceptEventLocked` and `admitLocked` still own source/generation admission. The exception is restricted to typed recognized turn phases with a positive revision bounded by retained state; inventory and prompt eligibility gain no exception.
- Tokens/secrets: no credential handling changes. Test output and PR evidence contain static enums and counts only.
- File operations: no production filesystem operations added; new files are the plan and focused regression.
- Subprocesses: no production process invocation or environment changes.
- Cryptography: authenticated fixtures reuse the existing Noise path; no keys, nonce discipline or encryption changes.
- Network/I/O: existing frame validation, envelope size cap, bounded publisher/relay queues and overflow retirement remain in place. No new buffering or remote input path.
- Errors/logs: no new payload logging; existing push errors use static metadata.
- Concurrency: generation and retained revision are read under one owner mutex; relay consumer watermarks remain on the single writer. Every test hold has cancellation and release.
- Threat model: the relevant risk is stale state after identity retirement or unauthorized recipient disclosure. Controlled generation-reuse/recreation tests and existing access/capability checks cover those gates. No change to pairing, remote commands, token revocation or the protocol's existing deferred protections.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-20

## Verification evidence

Linux, 2026-10-20. The plan was committed in `7223a036` before the regression commit `a1601307c457c18eb82beb6e8559179eb808428a` and production repair `dc3018b7f761041b074641ac1be7f3521de4442e`.

The failing baseline used `a1601307` with unchanged production from `4f4c7fa1b5b23435d173a9bd345d7176cb124e38`. The relevant production files (`daemon_live_relay.go`, `daemon_live_source.go`, `daemon_live_state.go`, `v2session_livestate.go`) are identical between that base and the originally demonstrated `f90990dd142f206422bfd4a24a8feedcc94f8274`.

Baseline command: `python3 scripts/test-admission.py -- go test -timeout=5m -race -json -run '^(TestDaemonLiveQueuedTurnProgress|TestDaemonLiveTurnProgressWatermark|TestDaemonLiveTurnProgressFreshness)$' ./cmd/pyry`.

| Proof | Leaf executed | Passed | Failed | Skipped |
| --- | ---: | ---: | ---: | ---: |
| Unchanged production, named regression | 22 | 16 | 6 | 0 |
| Final repair `dc3018b7`, same regression within affected-package run | 22 | 22 | 0 | 0 |

Both publisher-held and relay-held baseline cases delivered only `[idle]`, missing `[responding idle]` or `[thinking idle]`; the two recognized-progress freshness cases also failed. Immediate, retirement/recreation and consumer-watermark controls passed before and after repair. No retries, sleeps or deadline extensions established the controlled result.

Final command: `python3 scripts/test-admission.py -- go test -timeout=30m -race -json ./cmd/pyry/...`. It exited successfully with 15,702 leaf cases passed, zero failed and four skipped. Skips were existing opt-ins in `TestRunAgentRun_RealClaude`, `TestDaemonThreadMetadataReadiness` and `TestPushWakeFeedbackExpiryIntegration`, plus `TestMemoryPathsCaseInsensitive` on the case-sensitive filesystem. `go vet ./...` passed on the final code tree; `go build -o /tmp/builder-3711/pyry ./cmd/pyry` passed on the identical production source. Full-module, documentation and downstream Mobile gates remain pending with their owning stages.

Private raw evidence is retained under `/work/Projects/pyrycode-agents/logs/evidence/3711/builder-20261020-dc3018b7/`; only enum/static outcomes and hashes are published here:

- `before-regression.json`: SHA-256 `115e6a3bb3227861e524701b63a34c54e64f01189f579584dde521b248a36bbe`.
- `package-race.json`: SHA-256 `efd243552178f7fbbf57444966dedb8fc34f4e23589f9f9d3038a42a36974f00`.
- `SHA256SUMS` (including the earlier fixture compile failure and intermediate focused pass): SHA-256 `f7c4c04e5d3a2fdd660ff5aca6e1f9fd8743f39910d0023f2b3f66c40f466c9a`.
