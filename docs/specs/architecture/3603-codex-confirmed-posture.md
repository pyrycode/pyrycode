# #3603 Codex accepted-turn permission reporting

## Forward repair and evidence

This plan precedes the new production repair on `fix/3603-confirmed-mode`, based
on `2379fb7261d9c9f257a54252fc70237b081a3051`. It does not rewrite the earlier
retrospective test-staging plan. The exact native `TestCodexConversationLive`
run at that base executed one test, failed one, skipped zero: the same-session
`session_settings.permission_mode` remained empty before the approval turns.
`runSettingsPool.ConfirmedPermissionModeFor` deliberately excludes stored future
intent; only Claude currently implements its optional reader.

## Contract and implementation

Implement the existing optional `ConfirmedPermissionMode` reader on
`codexRunner`, without changing the public protocol, runner interface or
`codexTurnOverrides` permission mappings. The pinned app-server schema says
`turn/start` posture overrides apply to this and subsequent turns. A successful
`codexsup.Client.StartTurn` returns a nonempty turn ID, but does not echo posture.
Confirmation therefore means both that RPC succeeded with the exact snapshotted
posture and that its matching `turn/started` notification came from this child.
It is not a claim that Codex echoed a permission mode it does not expose.

Keep this state under the runner's leaf mutex in a small helper file. Fence it
by current client, child generation, unique child translator and monotonically
increasing attempt epoch. Support either response-before-notification or
notification-before-response; only the same nonempty turn ID joins the two.
The raw notification must also name the snapshotted active root thread ID; a
subagent or other thread in the same child cannot confirm it. The translator
does not provide this filter, so the confirmation boundary checks it directly.
Serialize `WriteUserTurn` snapshot through RPC completion with a separate context-selectable
single-token turn-start gate: concurrent callers cannot send an older attempt after a newer one
has confirmed. The reader, lifecycle barriers and notification callbacks never
take that gate. A waiting caller can return on context cancellation, with no
new goroutine. Independent source trace confirms `wrapUpCapture.Sink`,
`sinkForProducer` and `forwardEvent` only accumulate/offer to separate drain and
publisher workers; none synchronously reenter `WriteUserTurn` from the read loop;
cover the ordering with blocked model resolution and actual posted policies.
Add a narrowly marker-scoped fake app-server release-file control to hold a
turn/start response itself, proving a deadline-bound competing writer returns
without posting and cannot send later; controls stay under a temporary test dir.
Do not add public events or log notification contents. Empty and unknown modes
remain unconfirmed even though the existing encoder conservatively maps them.

`SetPermissionMode` and `SetSpawnPermissionMode` only change future intent. A
previously accepted mode can remain visible while that intent is pending. At
any new `StartTurn` attempt, clear public confirmation before sending: the old
plan must not be reported during a possible new workspace-write turn. A failed,
unknown, incomplete or superseded attempt remains unavailable; no rollback or
stale revival. Only the newest attempt can confirm. Bind, unbind, restart,
rotation arm, fresh-session change and teardown invalidate immediately. Aborting
rotation does not resurrect the old confirmation. A stopped child cannot report
confirmation while the supervise loop is still catching up.

Keep the original plain live turn. After setting plan, add a short tool-free
staging turn that applies that per-turn posture, then require exact-session wire
readback of plan before the existing real decline/accept modal and file checks.
No empty guard bypass, retry loop, skipped live scenario or relaxed validator.

## Verification and scope

First add a real fake-app-server regression that fails because the optional
reader is missing. Then verify stored intent versus accepted mode, both factor
orders, failed/incomplete/mismatched attempts, superseded epochs, stale children,
restart/rotation/teardown invalidation, wrong-thread notifications from the same
child, exact posted policy and bound wire mode.
Run focused native race tests through shared test admission, tagged package
compilation, independent review, then one exact pinned native live scenario via
the dedicated managed Codex home and serialized live gate. Existing full native
and named live dispatcher gates remain mandatory. Forecast under 800 changed
lines: runner hooks/helper, meaningful tests, live staging and this plan.

Documentation handoff: explain Codex accepted-turn confirmation versus stored
future intent in the owning streams/session settings knowledge topic. This
builder change does not edit the documentation-stage knowledge files.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `codexRunner.notify` accepts only the current child's unique
  translator for confirmation; `StartTurn` success and matching notification ID
  are both required. Stored future intent, old responses and old child events
  cannot become wire authority. Existing approval and sandbox mappings stay intact.
- [Tokens, secrets, credentials] The repair creates no credentials and reads no
  login material. Native proof uses the existing managed dedicated home; helper
  state and tests must not log payloads, notifications, paths or tokens.
- [File operations] Production state is memory-only. The live test retains its
  existing isolated workspace and exact modal/file guards; no new file access or
  permission change is introduced.
- [Subprocesses] Existing `codexsup.Client` spawn/stop and structured RPC encoding
  remain the only subprocess boundary. No new shell invocation or inherited
  environment handling is introduced.
- [Cryptography] No keys, nonces or cryptographic operations are added; turn IDs
  are equality witnesses for one authenticated local child, not secret tokens.
- [Network and I/O] Existing bounded app-server parsing and RPC contexts remain;
  the repair adds no listener, transport or unbounded buffer. One pending attempt
  stores constant-size identifiers already admitted by that parser.
- [Errors, logs, telemetry] Preserve generic turn errors and add no logging or
  telemetry. Confirmation exposes only the existing finite permission labels.
- [Concurrency] The leaf mutex protects both factors, generations and epoch. It
  is never held across `StartTurn`, translator locking or other blocking calls.
  Every attempt clears authority before I/O, all lifecycle barriers invalidate,
  and no goroutine is added. The separate turn-start gate orders writes only;
  lifecycle invalidation never waits for an RPC or acquires that gate. Failed/late responses cannot revive prior authority.
- [Threat model] Scope is accurate permission reporting for a current local
  Codex child. Existing relay authorization and actual sandbox/approval enforcement
  remain required and unchanged; reporting never authorizes an operation.

**Reviewer:** builder self-review, with parent contract review before code
**Date:** 2026-10-18
