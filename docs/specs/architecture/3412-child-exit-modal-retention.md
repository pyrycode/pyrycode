# Child-exit modal observation retention (#3412)

## Files read

- `internal/e2e/relay_v2_stream_modal_test.go` → `TestRelayV2_StreamModalPermissionRoundTrip`: one decrypt stream, phase-local dismissal wait, replacement-child safety assertions.
- `internal/e2e/harness.go` → `StartStreamInteractiveWithRelayStdio`, `Harness.Stderr`: isolated fake daemon and synchronized diagnostic capture.
- `cmd/pyry/streamsup_runner.go` → `stdioPermissionHandler.handle`, `await`, `childExited`: exit resolves the pending request; the awaiting goroutine retires its surface.
- `cmd/pyry/modal_resolve_v2.go` → `streamApprovalBridge.Surface`, `retire`, `broadcast`: retirement audit precedes dismissal delivery; push failures have content-free events.
- `docs/knowledge/features/e2e-harness.md` → isolation and readiness: socket readiness does not order child events.
- `docs/knowledge/features/e2e-harness-stream-interactive-harness-pattern-startstreamin.md` → early-frame retention: every awaited milestone needs retention at the sole decrypt point; controlled ordering must happen after decryption.
- `docs/knowledge/features/development-verification.md` → mutation verification and artifact survival: retain actual outcomes, not temporary paths.
- `docs/specs/architecture/2612-stream-modal-ack-ordering-flake.md` → analogous early-surface loss, not proof of this historical failure's cause.
- `CODING-STYLE.md` → testing, concurrency, and contract comments.
- `docs/protocol-mobile.md` → Security model: preserve Noise nonce ordering and keep credentials/content out of diagnostics.

## Context

The unchanged #3411 head both failed and passed the full gate. Its historical
logs are not available here. A full race-enabled fake-daemon baseline is running
at `c468065350a74924ef11b9e5557ac803ebd21dc2` before implementation. The test
currently discards a dismissal consumed while awaiting the send acknowledgement.
This is a concrete ordering defect, but does not establish attribution for the
historical failure. No decision record is needed for a local test-reader repair.

## Design

Add a test-local `streamModalReader` around the existing envelope decoder. Its
`next(deadline)` consumes one envelope, retains dismissal IDs and records bounded
content-free summaries (receive phase, elapsed time, envelope type/id, reply id,
modal id, daemon timestamp). `waitDismissed(deadline, modalID)` first checks the
retained observation, then pumps that same reader under one absolute deadline.
The original surface, verdict, replacement-child synchronization and late-answer
assertions remain. Deadlines remain unchanged. No production behavior changes.

On failure, print the reader timeline plus an allowlisted projection of captured
daemon diagnostics, including retirement identity/outcome, delivery-error events
and child PID/timestamps. Log successful child-exit evidence too, so the PR can
retain ordering observations. Keep payload text, answer tokens and keys out.
Bound timeline storage; retain dismissal identity independently of trace eviction.

No other fetched feature branch currently touches the planned files. Estimated
written work: 350 lines including plan, helper, regression cases and integration;
0 exports, 1 consumer, 4 acceptance criteria, fewer than 10 reject branches.

## Concurrency model

All reader state belongs to the subtest goroutine. The existing decoder alone
receives/decrypts Noise frames. No new goroutine, mutex or shutdown path.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Dismissal consumed before ack / dedicated wait | `TestStreamModalReaderOrdering/early` |
| Dismissal arrives during dedicated wait | `TestStreamModalReaderOrdering/later` |
| Different modal dismissed; target absent | `TestStreamModalReaderOrdering/missing` |
| Repeated wait for same observed identity | `TestStreamModalReaderOrdering/early` |
| Deadline expires despite buffered unrelated frames | `TestStreamModalReaderDeadline` |
| Origin exits and replacement uses same session id | `TestRelayV2_StreamModalPermissionRoundTrip/stdio_child_exit_late_answer` |

Each case creates a fresh reader; no identity survives into another case.

## Error handling

Decode failures remain fatal. Missing matching dismissal fails within the
existing ten-second deadline. Diagnostics distinguish an observed early dismissal
from no observed dismissal; a correlated retirement audit without a receive
narrows failure to delivery/scheduling, while absent audit leaves retirement
unproven. Neither condition alone establishes the precise production cause.

## Testing strategy

Write controlled ordering tests first and show the early case fails with the
phase-local wait. Include missing, unrelated, repeated and expired-wait cases and
a distinctive secret sentinel for diagnostic exclusion. Run the existing modal
round trips with `-tags=e2e -race -count=1`, then two uncached complete fake-daemon
runs after repair. Run `go vet ./...` and build `./cmd/pyry` to scratch storage.
The complete uncached `make check` requested by the issue is pending the verifier:
builder role instructions explicitly reserve that full-module gate to it.
Retain baseline/candidate revisions, commands, executed counts, named child-exit
results and diagnostic examples in the PR, without depending on scratch paths.

## Open questions

- Does the full-suite baseline reproduce the historical failure? Record the
  outcome and diagnosis limits regardless.
- Does controlled early consumption explain a real test failure? Require red
  before / green after evidence; investigate production only if evidence demands it.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `next` observes only decoded test envelopes after the existing authenticated decrypt point. Modal ID matching remains mandatory.
- [Tokens, secrets, credentials] SHOULD FIX: allowlist diagnostic metadata and test exclusion of a distinctive payload/token sentinel; never print raw envelopes or raw stderr in the new diagnostics.
- [File operations] Existing isolated fixture operations remain; the helper adds no persistent file or caller-controlled path.
- [Subprocesses] Existing fake-daemon startup and process-group cleanup remain; no shell or child environment changes.
- [Cryptography] One receive cipher decrypts each frame exactly once in arrival order. Retention stores metadata, never ciphertext for replay.
- [Network and I/O] Preserve existing transport caps and absolute deadlines; cap the diagnostic timeline independently of retained dismissal identity.
- [Errors, logs, telemetry] Emit only timing/type/identity fields and selected structured daemon metadata. No prompts, response bodies, headers or secrets.
- [Concurrency] Reader and observations stay on the subtest goroutine; `safeBuffer.String` supplies a synchronized daemon-log snapshot.
- [Threat model] The loopback fake relay remains untrusted until Noise authentication. Production authorization, replay protection and credential storage are untouched; the retired-modal answer must still never actuate the replacement child.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-14

## Revisions

### 2026-10-14 — Baseline and controlled ordering proof

The unchanged complete fake-daemon baseline passed in 230.851s, including the
named child-exit case (1.20s); the historical failure did not recur. The controlled
`TestStreamModalReaderOrdering/early` failed with the original phase-local wait:
it observed shown → dismissed → ack in the ack phase, then expired after 20ms
waiting for the consumed dismissal. Retention makes this case pass, including a
repeated observation without another read. Missing and unrelated dismissals still
fail at the original absolute deadline. These resolve both open questions without
claiming the unavailable historical logs establish the same cause.

Diagnostics skip quoted slog values atomically before selecting metadata fields:
splitting on whitespace could mistake `modal_id=...` inside a quoted message for
a real field and disclose content. The sentinel test covers that case and trace
eviction independently of retained dismissal identity. Design and scope unchanged.
