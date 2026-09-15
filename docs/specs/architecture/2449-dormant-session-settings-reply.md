# #2449 — `session_settings` answers a dormant session with its id, model and effort

## Files read

- `internal/sessions/pool.go` → `Pool.SettingsFor` — the live read this one is modelled on, including its bare-error and single-acquisition contracts; `Pool.revivedSettings` — the same dormant lookup, and the reason it cannot be reused (it collapses a miss into the zero value); `Pool.mintSettings` — the field-by-field construction shape both new and revived settings keep; `Pool.dormant` — its docstring already names this read as the second reader of the map.
- `internal/sessions/session.go` → `canonicalSettings`, `canonicalPermissionMode`, `permissionModeDefault` — where `""` becomes `"default"`, and the function `buildSession` applies to every settings value it stores.
- `internal/sessions/revive.go` → `Pool.Revive` — passes `revivedSettings` into `materialise`, so what this read must report is `canonicalSettings(revivedSettings(id))`.
- `internal/sessions/get_or_create.go` → `Pool.materialise` — retires the dormant entry it takes over, which is what makes "live" and "dormant" a partition rather than two overlapping sets.
- `cmd/pyry/main.go` → `sessionSettingsReader`, `resolveBoundRunSettings`, `boundRunSettings` — the seam to widen, its stated call-counting reason, and the struct the live/dormant distinction has to ride on.
- `cmd/pyry/relay.go` → `runConfigFor` — composes settings with usage; the gate for AC 4 goes here.
- `cmd/pyry/session_transcript_dir.go` → `sessionTranscriptDir` — answers `""` for an id `Pool.Lookup` misses, which is exactly a dormant id.
- `internal/contextwindow/usage.go` → `Read` — `Read("", nil)` returns `{WindowTokens: defaultWindowTokens}`, i.e. `(0, 200000)`. Confirms AC 4 needs a gate: the existing chain does **not** already answer zero for a dormant id.
- `internal/relay/v2session_settings.go` → `handleRequestSessionSettings`; `internal/relay/v2session_seams.go` → `RunConfig` — the wire side is a pass-through of all seven fields, so every value this ticket changes is decided in `cmd/pyry` and `internal/sessions`.
- `docs/knowledge/features/sessions-package-key-types-reviving-a-dropped-session-pool-revive.md` — #2448's contract: model and effort restored, posture zeroed, and `materialise`'s retire (both rollback paths restore the entry).
- `docs/knowledge/features/sessions-package-key-types-pool-settingsfor.md` — `SettingsFor` is not a rewrite of `DefaultSettings`; the same non-composition rule applies to the new read.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-request-session-settings-the-rea.md` — the verb always answers one `session_settings` frame; there is no refusal frame to add.

## Context

`request_session_settings` answers a conversation whose bound session the pool does not hold with the all-zero reply. After a daemon restart that is every conversation but one, because `Pool.New` materialises only the bootstrap — so an existing channel's model and effort menus are blank and inert until the first message revives the session. #2448 made the dormant entries survive the restart in `Pool.dormant`; this ticket reads them.

The change is a second pool read and a fallback, not a new frame: the verb's "always exactly one reply" shape is untouched, and `protocol.SessionSettingsPayload` gains no field.

**One premise in the ticket body is wrong, and it changes nothing about the work.** The Context says "a settings write on a dormant session already goes through `sessionRouter.resolve`, which revives, so nothing else is needed for a change to land." It does not: `sessionRouter.resolve` is the message-delivery seam (it returns a `handlers.TurnWriter`), while `handleSetSessionSettings` calls `SettingsUpdater.UpdateSettings` with the id from its own payload, wired to `settingsUpdaterAdapter`, which calls `Pool.UpdateSettings` — and that reads `p.sessions` only, so a dormant id is `ErrSessionNotFound` → `relay.ErrSessionUnknown` → a `session.not_found` reply. So after this ticket a restarted channel's menus read correctly and label correctly, but a model or effort change made *before* the first message is still refused until something revives the session.

That is out of scope here — all five acceptance criteria are read-side, the Technical Notes prescribe read-side changes only, and revive-on-write is a separate design decision with its own security weight (a settings frame would become able to spawn a claude child). Filed separately; the number is in the PR body. This plan implements the read as specified.

No ADR is warranted — this narrows one reply's "unresolvable" case inside the boundary ADR 035 already describes, and that document was amended for #2448.

## Design

### `internal/sessions` — one new read beside `SettingsFor`

```go
func (p *Pool) DormantSettingsFor(id SessionID) (SessionSettings, error)
```

Contract: the `SessionSettings` a `Revive` of `id` would materialise — the entry's own persisted `Model` and `Effort`, no posture — or `ErrSessionNotFound` when no dormant entry exists for `id`. One `p.mu.RLock` acquisition, `SettingsFor`'s discipline; the empty id is not special-cased (it misses the map like any other unknown id); the error is returned bare so a hostile id cannot be reflected into a caller's log line or wire frame.

Three decisions worth stating because a later reader will be tempted by each:

- **A miss of its own, not `revivedSettings`.** `revivedSettings` returns the zero `SessionSettings` for an unknown id, which is correct for a revive (an id with no record revives to claude's defaults) and wrong here (it would answer an unknown bound id with that id and empty settings — worse than today, and it reddens AC 5). The two share a map read and nothing else.
- **Not folded into `SettingsFor`.** A fallback inside `SettingsFor` would change what "not found" means for every other caller of the live read. Two reads, each with one meaning.
- **The posture is built, not cleared.** The literal names `Model` and `Effort` only, then goes through `canonicalSettings` — the same function `buildSession` applies to what `Revive` hands it. So `YOLO` is false and `PermissionMode` is `"default"` structurally: no clearing statement exists for a later edit to delete, and a field added to `registryEntry` is not inherited until someone opts it in. This is `mintSettings`' and `revivedSettings`' recorded reason, and routing through `canonicalSettings` rather than spelling `permissionModeDefault` here is what keeps the reported posture equal to the materialised one by construction.

### `cmd/pyry/main.go` — the resolver tries live, then dormant

`sessionSettingsReader` gains `DormantSettingsFor`. The interface's "do not widen it past `SettingsFor`" instruction and the call-counting reason under it are rewritten rather than worked around: the claim the double exists to carry is now "an unbound conversation reaches *neither* read", and a two-method double can still count it.

`boundRunSettings` gains one unexported bool, `live` — true when the settings came from a session the pool holds, false when they came from a dormant entry. It exists for AC 4: `runConfigFor` cannot ask the pool itself, and the id alone cannot carry the distinction.

Polarity is deliberate. The zero `boundRunSettings` is not live, so the fail-closed direction (do not stat a transcript for a session nobody confirmed live) falls out of the zero value rather than out of a branch — and every existing `boundRunSettings` literal in the tests has to opt in, which is what makes the usage-gate change visible in the diff instead of silent.

`resolveBoundRunSettings` keeps its two guards ahead of both reads, so "no pool lookup happens for an unbound conversation" still holds verbatim. Then: `SettingsFor`; on `err == nil` return with `live: true`. Otherwise `DormantSettingsFor`; on `err == nil` return with `live: false`; otherwise `(zero, false)`.

The single-acquisition property is per answer, not across the pair. Each read answers "does this half hold the id" and "what are its settings" under one lock, so no reported field can describe a session another field does not. The window between the two reads is benign in both directions: a revive landing in it makes a dormant answer describe a session that is now live, whose model and effort are the same values `Revive` just materialised from the same entry; an idle eviction cannot move an id into `dormant` at all (the map only ever shrinks, per its docstring), so a live hit cannot be downgraded mid-read.

### `cmd/pyry/relay.go` — the usage read is gated on `live`

`runConfigFor` calls `usage` only when `usage != nil && b.live`. A dormant conversation reports `UsedTokens` and `WindowTokens` at zero.

Without the gate the chain answers `(0, 200000)`: `sessionTranscriptDir` returns `""` for an id `Pool.Lookup` misses, `snapshotUsageFor` passes `""` to `contextwindow.Read`, and that path returns the default window. Zero `window_tokens` is the reply's published "do not render a percentage" sentinel, so reporting the default window beside a zero used count would claim a genuine fresh session on a channel that may be near full.

Not in scope: a dormant session's real occupancy. `registryEntry` persists no spawn dir and the transcript folder comes from the live runner's `ClaudeSessionsDir`, so there is nothing on disk to derive it from until the session is revived.

## Concurrency model

No goroutines, no new locks, no new lock-order edge. `DormantSettingsFor` takes `p.mu.RLock` once and must be called with `p.mu` unheld (Go's `RWMutex` is not reentrant) — `SettingsFor`'s contract verbatim. The resolver takes the conversations-registry lock and then, sequentially and never nested, one or two pool RLocks; `runConfigFor` adds no acquisition at all, and the gate strictly removes acquisitions that used to happen.

## Error handling

| Case | Answer |
|---|---|
| Unknown / unbound conversation | `(zero, false)` before any pool read (unchanged) |
| Bound id the pool holds | Live settings, `live: true`, one read |
| Bound id with a dormant entry | Entry's model and effort, `YOLO` false, `PermissionMode` `"default"`, `live: false`, two reads |
| Bound id in neither | `(zero, false)` after two reads |

`ErrSessionNotFound` is reused rather than a new sentinel: the resolver only ever tests `err != nil`, and a second sentinel would be a value with no reader. Both pool errors stay bare — never wrapped with the caller's id.

## Testing strategy

`internal/sessions` (new `pool_dormant_settings_test.go`, on #2448's `helperPoolWarmStart` fixture):

- A dormant entry answers its own model and effort.
- An entry persisting `yolo: true` and a non-default `permission_mode` answers `YOLO` false and `"default"` — the restart-as-revocation-point assertion (AC 2). The distinct persisted values are what make it unforgeable: a copy-then-clear implementation passes a zero-valued fixture.
- The read materialises nothing: `Lookup` still misses afterwards and the live session set is unchanged (AC 3).
- An id the pool never held is `ErrSessionNotFound` (AC 5).
- After `Revive`, the same id misses this read — `materialise`'s retire is visible through it, so live and dormant partition.

`cmd/pyry` (`run_config_test.go`):

- `settingsReaderDouble` grows a dormant map and records both reads into **one ordered, tagged sequence**, so each row asserts which reads happened and in what order. A live hit must show one read; the dormant and neither cases two, live first.
- Rows: dormant-only binding resolves with `live: false`; a binding in neither half stays `(zero, false)`; the existing live and unresolvable rows keep their answers.
- `TestResolveBoundRunSettings_RealPool` gains a dormant arm over a hand-written warm-start registry (a `bootstrap: true` entry plus a second entry with a model and effort), pinning that the double models what `*sessions.Pool` implements — the double cannot prove `canonicalSettings` runs.
- A `runConfigFor` row where the resolution is dormant: both context figures zero and the usage recorder reached zero times (AC 4). Zeros alone cannot carry it — a fresh live session reports a zero used count too — so the recorded call count is the assertion, `TestRunConfigFor_UnresolvableReachesNoUsageRead`'s shape.

Gate: `go test -race ./internal/sessions/... ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`.

## Open questions

- Whether the dormant read should report the entry's `LastActiveAt` or label alongside the settings. Resolved in the design: no — the wire shape is unchanged and `SessionSettings` is what both the live read and `Revive` speak in.
- Whether `sessionTranscriptDir` should learn about dormancy instead of gating in `runConfigFor`. Resolved in the design: no — it already refuses correctly by returning `""`; the defect is that `contextwindow.Read("")` reports a window, and the composition seam is where the two halves meet.

## Documentation handoff

Owned by the documentation stage; pending. Not edited by this ticket.

- `docs/protocol-mobile.md`, under `request_session_settings`: the "Three answers" list says a conversation "bound to no live session" gets the all-zero reply. Split that case — a conversation bound to a session this daemon holds a persisted record of is answered with that session's id, model and effort; only a conversation it does not host, or one whose bound id it has no record of at all, still gets the all-zero reply.
- `docs/protocol-mobile.md`, under `session_settings`: record in the `used_tokens` / `window_tokens` rows that a reply naming a session the daemon is not currently running carries both context fields at zero, because there is no transcript to read until that session is revived.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No MUST FIX. The one untrusted input is `conversation_id`; it stays a key into the conversations registry and reaches no pool read, no path join and no log line — the id handed to both reads is the daemon's own `conv.CurrentSessionID`. The boundary this ticket *does* widen is disk → wire: a persisted `registryEntry`'s `Model` and `Effort` now reach a reply that previously carried nothing for that conversation. Bounded on the way in rather than on the way out, deliberately — `handleSetSessionSettings` gates every client-supplied value through `validModel` / `validEffort`, and `settingsUpdaterAdapter` additionally checks the model against the retained vocabulary, so nothing unvetted can be persisted by a client; a writer of `sessions.json` already owns the daemon's process files. Adding a second validation on the read side would put a drifting definition of "valid model" opposite the write path's.
- [Disclosure scope, #678] No MUST FIX, but the mechanism is worth naming because it is not obvious: `handleSetSessionSettings` takes its target session id **from the client payload**, so a session id is the write path's addressing capability, and this ticket discloses one the reply previously withheld. The scope is nevertheless unchanged — a caller naming conversation X learns X's own bound id, exactly what the live path already answers for the same conversation — and both guards stay ahead of both reads, so no request can reach a pool read for an unknown or unbound conversation and the `Pool.Lookup("") == bootstrap` fall-through has no expression on this path (neither read special-cases `""`).
- [Tokens, secrets, credentials] Not applicable — the reply carries no credential and this ticket creates, stores and compares none. The nearest thing to a capability is the session id, handled under Disclosure scope above.
- [Posture, #1487] The load-bearing finding, and the reason AC 2 exists. Reporting the entry's persisted `yolo` / `permission_mode` would show a bypass toggle ON for a session that will actually revive at the default posture, and would hand a read-modify-write client the exact snapshot needed to re-assert a bypass the restart revoked — the trap `Pool.SettingsFor`'s own docstring records. This pass verified the exclusion is **structural**, not a clearing statement: the literal names `Model` and `Effort` only and then goes through `canonicalSettings`, so the reported posture equals the one `buildSession` will materialise by construction, and a future `registryEntry` field is not inherited until someone opts it in. Pinned by a test whose fixture persists `yolo: true` and a non-default mode — a copy-then-clear implementation passes a zero-valued fixture, so the fixture's values are the assertion.
- [Resource exhaustion] AC 3 is a security property, not only a correctness one: this verb fires on every channel activation and is client-triggered, so a read that revived would turn N channel opens into N claude children. The design satisfies it structurally — a map lookup under `RLock`, with no write path into or out of it — and the test asserts the live session set is unchanged rather than that nothing visibly spawned.
- [File operations] No findings, and the change strictly narrows the surface: a dormant conversation no longer reaches `sessionTranscriptDir` or `transcript.StatByID` at all. No path is constructed, joined, opened or stat'd by anything this ticket adds.
- [Subprocess] No findings. The reported values feed no argv — `Revive` reads the same entry independently through `revivedSettings`, so this read cannot influence a later spawn.
- [Cryptographic primitives] Not applicable — no randomness, no key material, and no comparison against a secret; the only equality performed is a map lookup on a daemon-recorded id.
- [Network & I/O] No findings. No new frame, no new field, no change in reply size class, and at most one extra map read per request on the miss path under a lock the path already takes.
- [Errors, logs, telemetry] No findings, as a standing constraint rather than an absence: neither new function takes a logger and neither may grow one, `ErrSessionNotFound` is returned bare rather than wrapped with the caller's id, and no reject branch logs — the only field such a line could add is the caller's untrusted conversation id, which is the channel #833 closes.
- [Concurrency] No findings. Two sequential, never-nested `RLock`s, adding no edge to the daemon's lock order; "call with `p.mu` unheld" is `SettingsFor`'s contract verbatim (Go's `RWMutex` is not reentrant, so a call from inside a critical section self-deadlocks). The between-reads window is analysed in § Design and is benign in both directions, and live-first ordering plus `materialise`'s retire means no id can be answered from the dormant half while a live session holds it.
- [Threat model alignment] The two threats this verb sits under are #1487 (a restart is a revocation point for a phone-granted bypass) and #678 (no client-driven verb may fall through to the bootstrap); both are addressed above. OUT OF SCOPE and named: a dormant session's real context occupancy, which the ticket defers because `registryEntry` persists no spawn dir to resolve a transcript from; and the settings **write** on a dormant session, which is refused rather than revived — filed as its own ticket, see § Context.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-15
