# Reconnect retirement: integrated acceptance

## Files read

- `internal/relay/v2session_recovery_test.go` → `TestV2Session_LastEventIDIgnored`, `TestThreadRecoveryAfterReconnect`: authenticated cursor shapes, connection reuse and explicit scoped recovery.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit`: token/static-key admission and selection of supported reconciliation.
- `internal/protocol/handshake.go` → `HelloClientPayload`: obsolete cursor is outside the decoded contract.
- `internal/relay/v2session_push.go` → `forwardEnvelopeSource`, `mergedForConn`: delivery gates and explicit model projection provenance.
- `internal/relay/v2session_modelreconcile.go` → `reconcileModelLists`: projected retained lists and the inherited nonblocking stale-comment finding.
- `cmd/pyry/interactive_live_publication.go` → `publishLiveState`: supported fanout preserves durable history IDs without ring IDs.
- `cmd/pyry/relay.go` → `cleanupOnConversationDelete`, `startRelay`, `startRelayV2`: composed surviving cleanup in both relay postures.
- `docs/specs/architecture/3738-retire-event-replay.md`, `3739-remove-event-ring.md`: child contracts, security reviews, revisions and proof selections.
- `docs/knowledge/features/v2-session-manager.md`, its reconnect and model-reconcile topics: explicit recovery differs from current-state reconciliation; projection cannot depend on removed event IDs.
- `docs/knowledge/features/thread-package.md`, `protocol-package.md`, `relay-package.md`, `eventring-package.md`: retained thread/history/schema boundaries and package retirement.
- `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`: current source controls applicability; compilation and skips prove no live acceptance.
- `docs/knowledge/decisions/042-daemon-built-thread.md` → **Update messages**, and `docs/protocol-mobile.md` → **Security model**: scoped epoch/version recovery and existing trust boundaries.
- Issue #3719's archived contract and authoritative accepted stage records/bundles for #3718, #3738 and #3739: obligations, exact revisions, historical failures and pending gates.

## Context

One deliverable records integrated acceptance after publication retirement
(#3718 / PR #3838), replay retirement (#3738 / PR #3839) and storage retirement
(#3739 / PR #3840). A closed split parent alone establishes no delivery.
The inspected integrated base is `1ac43b7e8d9346f77e7dbfac004c3e743a27b1a7`.
The archived original body hashes to
`9db44af7cb86ea951e6bfceb020216a9d7f582a36333a680cc6747c212d55fb5`.
ADR 042 already owns the design; no new decision record is needed.

Scope: approximately 150 written plan/evidence lines, zero production files,
exported types, consumer migrations or new reject branches; three acceptance
criteria. No implementation behaviour is bundled into this evidence review.
Fetched feature branches have no overlap with this plan's path. Adjacent
mandatory-thread migration remains with #3740/#3741/#3743.

## Design

Compare accepted child revisions with this base and confirm that later changes
cannot invalidate reused checks. Read authoritative stage acceptance and
hash-checked bundles rather than treating comments as verdicts. Map every
archived obligation and current criterion to named retained tests and source.
Record OS, exact candidate, selections and executed/passed/failed/skipped counts;
executed excludes skipped events and includes parent/subtest events.

The #3739 candidate already includes both removals. Prefer its integrated
evidence; use #3738 to explain replay-specific proof and #3718 only as ancestor
publication/live evidence. Retain generic optional `Envelope.EventID` and durable
`HistoryEntryID`; neither restores event-ring recovery. Add focused proof only
if the combined behaviour lacks an existing meaningful oracle. Record the
stale `reconcileModelLists` comment explicitly as inherited nonblocking work,
as permitted by the issue, without claiming its correction.

## Concurrency model

No goroutines, locks, queues or cipher ownership change. Review the surviving
Run-owned delivery and composed cancellation/join evidence without adding work.

## State transitions and identity reuse

None: this change records evidence and introduces no lifecycle or identity state.
The evidence map must still name coverage for reconnect/reused connection IDs,
transport-down/rekey and deletion/sweep/recreation of surviving owners.

## Error handling

Missing required proof stays pending with its current owner; a focused pass,
compilation, skipped test or historical failure is never relabelled acceptance.
Preserve approval/operator holds and original failed results. Necessary denied
operations stop the run under the shared working practice.

## Testing strategy

Reuse accepted Linux race, fake-integration, build/static and tagged-compilation
evidence when its production/test inputs remain unchanged. No package is edited,
so no new regression test or duplicate package/full/live run is planned. Validate
the final documentation-only diff with `git diff --check`. The dispatcher/verifier
retains its configured acceptance, including slow/release/live checks; this
review does not declare a current live pass or order a new paired capture.

## Open questions

None about the product contract. Final evidence mapping must document any
applicability gap and disposition before handoff.

## Documentation handoff

Pending for the documentation stage; reconcile the already-merged child handoffs:

- `docs/protocol-mobile.md` → **hello (v2-specific note)**, **Reconnect replay & resync**, **Reconnect / Backfill semantics**, **Compatibility**, hello fields and reconnect/error recovery: ignore obsolete cursors; explicitly catch up each open conversation by completed epoch/version, fetch older pages and recover the newest window after `thread_reset`; preserve prompt/live-state reconciliation. Remove remaining production ring claims in **Application message types**' `mcp_status` row, **mcp_status**, and envelope metadata wording.
- `docs/knowledge/features/eventring-package.md` → **Where the durable id is assigned**, **Ownership & wiring**, **Files**: retain retirement wording, durable source-history/`HistoryEntryID` distinctions and no ring-derived IDs. `docs/knowledge/features/conversations-registry-crud.md` → **SetOnDelete**: composed thread, stream/live, suggestion and pending-inference cleanup in both relay postures, without ring Drop.
- Reconcile affected links/wording in `docs/knowledge/features/thread-package.md` → **Shadow lifecycle and evidence**, its linked lifecycle/evidence topics, `docs/knowledge/features/protocol-package-types-envelope.md`, `docs/knowledge/features/v2-session-manager.md` and `v2-session-manager-state-machine-reconnect-replay-hello-last-event-id-rin.md` → **Content recovery**/**Current-state reconciliation and delivery**. Preserve generic envelope metadata and source-history distinctions.
- Reuse `docs/knowledge/features/v2-session-manager-state-machine-connect-time-turn-phase-reconcile-running.md` and `v2-session-manager-state-machine-inline-reply-seals-gated-behind-transportdow.md` handoffs: remove retired watermark/pump ordering claims while preserving publish-before-fanout and transport-down nonce safety. Preserve model projection provenance in `v2-session-manager-state-machine-connect-time-model-list-reconcile-retain.md` → **Projection provenance**. Record final evidence/dispositions without claiming pending gates passed.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] Treat issue text and downloaded logs as untrusted evidence. Authoritative accepted stage records plus candidate ancestry and bundle hashes determine reuse; `handleNoiseInit` still validates token/static-key binding before app admission.
- [Tokens/secrets] No credential access or changes. Record only revision, test and verdict metadata; never copy environment, tokens or decrypted production payloads into the plan/PR.
- [File operations] Only this architecture plan is written in the worktree; evidence downloads stay in `/tmp/builder-3719`. No history migration, credential storage, permissions or path validation changes.
- [Subprocesses] No application subprocess or environment changes; existing child test results are reviewed without launching live Claude.
- [Cryptography] Reused nonce/rekey tests must apply to unchanged delivery inputs. `Run` remains sole cipher owner; ignored cursor data cannot authorize replay or suppress thread output.
- [Network/I/O] No endpoint/frame-cap changes. Retain evidence for requested conversation/host scope, bounded ordered queues, `livePending` and hosted admission; absence of replay alone does not prove those properties.
- [Errors/logs] Record failures separately from accepted reruns; no new runtime errors or logs. Snapshot refusals remain generic and source-history failures still omit `HistoryEntryID`.
- [Concurrency] No shared state changes. Retain direct-delete/sweep/recreation, joined thread removal and pending-inference cancellation evidence in both relay postures.
- [Threat model] Existing Noise confidentiality/authentication, nonce safety and host isolation remain binding. Mandatory-thread admission is OUT OF SCOPE under #3740/#3741/#3743; deployment, live-data migration and releasing existing holds are outside this ticket.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-23
