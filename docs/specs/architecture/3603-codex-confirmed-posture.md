# #3603 Codex accepted-turn permission reporting

## Forward repair and evidence

This plan precedes production work on `fix/3603-confirmed-mode` and preserves
the retrospective plan. Base `2379fb7261d9c9f257a54252fc70237b081a3051` failed native
`TestCodexConversationLive`: one executed, one failed, zero skipped; same-session
`permission_mode` stayed empty. The pool excludes future intent; Codex lacked its reader.

## Contract and implementation

Implement `codexRunner.ConfirmedPermissionMode` without changing the public
protocol, runner interface or `codexTurnOverrides` mappings. The pinned schema
applies `turn/start` posture overrides to this and subsequent turns. Confirmation
joins successful `codexsup.Client.StartTurn` with the matching `turn/started`;
it means accepted exact overrides, not a posture echo the RPC does not provide.

Keep state in a small helper under the runner's leaf mutex. Fence both response
orders by current client, child generation, unique child translator, monotonic
attempt epoch and matching nonempty turn ID. Raw notifications must also match
the snapshotted active root thread ID; the translator does not filter it.
Serialize `WriteUserTurn` snapshot through RPC completion with a context-selectable
single-token gate. Cancelled waiters cannot send later. Reader, lifecycle and
notification paths never take the gate. Independent trace confirms
`wrapUpCapture.Sink`, `sinkForProducer`, `forwardEvent` and publisher callbacks
only accumulate/offer to separate workers: no synchronous writer reentry.

`SetPermissionMode` and `SetSpawnPermissionMode` change future intent only;
previously accepted mode may remain until a new `StartTurn` attempt. Clear it
before that attempt's I/O, so an older plan cannot accompany a possible new
workspace-write turn. Failed, unknown, empty, incomplete or superseded attempts
stay unavailable, with no rollback/stale revival. Bind, unbind, restart, rotation
arm, fresh-session change and teardown invalidate immediately. Rotation abort
cannot resurrect authority; `Client.Done()` makes reads unavailable even before
the supervise loop observes exit. Only the newest attempt can confirm.

Keep the original plain live turn; after setting plan add one short tool-free
staging turn, then bounded exact-session plan wire readback. Preserve both real
decline/accept modal and file guards: no empty-mode bypass, model retry, skip,
validator relaxation, public event or notification logging.

## Verification and scope

First reproduce the missing optional reader with the real fake app-server.
Cover pending intent, both factor orders, mismatched/failed/incomplete attempts,
wrong-thread/stale-child/epoch events, lifecycle barriers and exact posted/wire
policy. Block model resolution to verify send order; a bounded marker-scoped
fake release-file gate in a temporary directory must also hold the RPC response,
proving a deadline waiter returns without posting now or after release.
Run focused native race tests and tagged compilation through shared admission,
independent review, then one exact pinned native live scenario with the managed
dedicated home and serialized live gate. Full native and named dispatcher gates
remain mandatory. Keep the whole PR within 800 written lines.
Documentation handoff: explain accepted-turn confirmation versus future intent
in the owning streams/session settings knowledge topic during documentation.

## Revisions

Before code: add root-thread fencing, serialized starts, cancellable gate and held-RPC proof.
After focused checks: condense repeated prose only; design and security unchanged.

Before prompt repair: current `92c507` initial LIVE attempt failed (7.14s), confirmed read-only with no tool call; reason unknown.
Scope no-tools to this turn; request real shell approval for the rig-owned touch, await and honor its decision.
Security: keep model/effort, modal/file/remote checks, credential isolation, policy and one attempt unchanged.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `notify` checks exact child translator and root thread;
  both RPC success and matching turn ID are required. Future intent and stale
  callbacks cannot become wire authority; approval/sandbox mappings stay intact.
- [Tokens, secrets, credentials] No credential access or creation. Native proof
  uses the managed home; do not log payloads, notifications, paths or tokens.
- [File operations] Production state is memory-only. Isolated live modal/file
  guards remain; only fake test controls add temporary arrival/release files.
- [Subprocesses] Existing `codexsup.Client` spawn/stop and structured RPC encoding
  remain; no shell invocation or environment inheritance change.
- [Cryptography] No crypto added. Turn IDs are equality witnesses for one local
  child, not credentials, nonces or authorization tokens.
- [Network and I/O] Existing bounded parsing and RPC contexts remain. No new
  listener/transport; one pending attempt retains identifiers admitted by parsing.
- [Errors, logs, telemetry] Preserve generic turn errors; no new logs/telemetry.
  Confirmation exposes only the existing finite permission labels.
- [Concurrency] Leaf mutex protects factors/generation/epoch and never crosses
  RPC, translator locking or blocking I/O. Clear before send; every barrier
  invalidates. The separate start gate orders writes; lifecycle never waits for
  it or an RPC. No goroutine added; late responses cannot revive old authority.
- [Threat model] Accurate current-child reporting only. Relay authorization and
  actual sandbox/approval enforcement remain unchanged; reporting grants nothing.

**Reviewer:** builder self-review, with parent contract review before code
**Date:** 2026-10-18
