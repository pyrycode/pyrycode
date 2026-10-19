# Context usage across reset rotation

## Files read

- `cmd/pyry/relay_context_usage.go`: `get`, `prepare`, `fly`, `record` own shared flights, memory and source attribution.
- `cmd/pyry/daemon_control_live.go`: `liveResolveSource` and `daemonLiveOperation` serialize source capture and reject stale publications.
- `cmd/pyry/streamsup_runner.go`: `QueryContextUsage` is the concrete runner adapter.
- `internal/streamsup/runner_control.go`: `QueryContextUsage` correlates private responses and rechecks child generation.
- `internal/streamsup/runner.go`: `setStdin` releases rotation only for an entitled fresh-sequence binding.
- `internal/streamsup/runner_turns.go`: `BeginRotation` stamps repeated arms and aborts.
- `cmd/pyry/new_session_starter.go`: `resetThenRotate` completes before asynchronous successor spawn.
- Existing context usage query/resolver and daemon live relay tests provide correlation, collapse and wire helpers.
- `docs/knowledge/features/streamsup-package.md` and its rotation-delivery-gate topic: an ineligible bind must never release rotation; tests must distinguish a standing gate from missing stdin.
- `docs/knowledge/features/sessions-package.md`, `development-verification.md`, `CODING-STYLE.md`, and `docs/protocol-mobile.md` security model: package ownership, discriminating proof and inert child-authored display data.

## Context

Reset completion can precede successor binding. Immediate query refusal currently returns predecessor memory. This is one useful behavior slice: a request during rotation waits for the entitled successor and reports its actual source. No decision record is needed. Feature-branch scan found no overlapping edits to the selected production files.

## Design

- Add a concrete optional readiness capability beside `QueryContextUsage`, leaving `sessions.Runner` and its doubles unchanged. `WaitContextUsageReady(ctx)` returns the admitted child generation; it waits only while rotating, polls under the existing leaf mutex, and never changes the admission gates. A generation-bound query validates that generation at registration and completion.
- After the existing idle deferral, readiness and query share the existing 30-second deadline. Repeated rotation follows the latest admitted child without extending this budget. The resolver re-resolves canonical conversation and runner under the live transition boundary after readiness, captures the successor source, and reserves ordering for it before querying.
- Flights continue to collapse per source; source retargeting is serialized with flight preparation. An overtaken flight cannot overwrite a newer flight or record its result. Completion revalidates the current live source before accepting fresh data. Separate recorder memory mutation from persistence so source validation and memory update can share the transition boundary without holding it across disk I/O.
- Caller cancellation returns no successful result, including when memory exists; it never cancels a shared flight. Failed readiness/query retains the existing remembered fallback and original timestamp/producer evidence. Legacy replies retain the corresponding payload, and supplied replies retain correlation and source generation.

## Concurrency model

Existing daemon-owned flight goroutines remain bounded by daemon shutdown and the query deadline; caller waits use caller contexts. Readiness creates no goroutine. Runner mutex remains a leaf and is released before parser registration, callbacks and writes. Source capture and fresh settlement take `offerMu`, then resolver/recorder mutexes as needed; registry persistence runs after that boundary is released.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Rotation held then entitled successor binds, no additional message | `TestContextUsageRotationDelivery` |
| Repeated arm and ineligible intermediate binding | `TestContextUsageReadinessRotation` |
| Generation replaced between readiness/query or delayed predecessor reply | `TestContextUsageGenerationQuery`, `TestContextUsageRotationStaleFlight` |
| Caller cancellation with shared callers; daemon shutdown; deadline expiry | `TestContextUsageRotationCancellation`, `TestContextUsageReadinessCancellation`, `TestContextUsageRotationTimeout` |
| Other conversation remains independently answerable; collapse within source | `TestContextUsageRotationDelivery`, existing resolver collapse tests |

## Error handling

Unsupported detail and absent nonrotating children keep immediate refusal. Rotation readiness expires under the same query deadline; daemon cancellation ends the flight. Parser/write errors retain existing refusal and cleanup. Fallback is read-only and is never re-recorded. No new logs expose readings or subprocess-authored paths.

## Testing strategy

Write the held-binding wired regression first and run it against unchanged production to prove failure. Use a test helper subprocess for the real runner boundary, plus deterministic internal binding tests for repeated rotation and generation rejection. Run affected context/live resolver tests and streamsup tests under race admission on macOS, then `go vet ./...` and `go build ./cmd/pyry`. Complete gates belong to dispatcher/verifier; Linux is outside this ticket's OS verification scope.

## Open questions

None. Readiness polling observes existing admission state rather than adding notification mutations to rotation guards.

## Documentation handoff

Pending documentation stage: update `docs/protocol-mobile.md`, “Asking for a context usage reading on demand” and the `context_usage.unavailable` row under “Error codes”. Replace immediate rotation-fallback wording with the bounded successor-readiness contract: after turn-idle deferral, readiness and query share 30 seconds; repeated rotations do not renew it; a successful answer belongs to the entitled successor. State that timeout retains the remembered fallback with its original `as_of`, or unavailable when absent, and preserve supplied-path stale-source rejection.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `resolveBoundRunner` retains its unbound-session refusal; readiness is scoped to that runner and never selects bootstrap. Re-resolve canonical conversation and source before querying.
- Tokens/secrets and cryptography: no credential, nonce, key or crypto changes; existing sealed relay transport remains the boundary.
- Files: recorder keeps registry persistence and permissions unchanged; split memory update from save without accepting a new path.
- Subprocesses: no production launch/argument/environment changes. Readiness only inspects the admitted binding.
- Network/I/O: reuse bounded parser decoding and existing envelope limits; readiness and query share one deadline and per-conversation flights limit duplicate work.
- Errors/logs: keep child-authored readings and memory paths out of diagnostics; generic unavailable/fallback behavior remains.
- Concurrency: SHOULD FIX implemented by design: predecessor completion must be checked before both publication and memory mutation under the transition boundary; checking publication alone leaves stale durable memory.
- Threat model: existing protocol threats for hostile paired clients and child-authored display strings retain membership isolation, bounded work, encrypted delivery and client render sanitization. No new trust or authorization is inferred from generation numbers.

**Reviewer:** builder (self-review)
**Date:** 2026-10-19

Sizing recheck: one behavior, approximately 700 written lines including plan and tests, zero new exported types/interfaces, one production consumer, four acceptance criteria, fewer than ten new rejection branches. Within all admission ceilings.

## Revisions

- 2026-10-19: The held-binding shared-caller regression exposed a transition during readiness. Flights that have not started querying now join across source transitions only for the same stable runner identity, reserving successor ordering under the existing boundary. Once querying starts, source-generation separation remains mandatory. The adapter contains a function field and cannot itself be compared as an interface; identity is its underlying runner pointer.
- 2026-10-19: The subprocess fixture uses a launcher that places Claude's fixed argument prefix after Go's `--` separator. It holds the second launch before binding after a real predecessor has run. Both delivery variants fail against the original resolver, adapter and control implementation via a Go overlay, returning predecessor memory before release; the repair preserves their pending state and successor source. Verification runs on macOS under the current single-OS policy.
