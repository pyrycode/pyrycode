# Spec #656 — v2 `session_transition` wire type (SSOT)

**Ticket:** #656 — feat(protocol): v2 session_transition wire type — define the session-boundary event shape (SSOT)
**Size:** S (override-confirmed; ~2 production files, 1 new exported type, 1 new constant — see § Scope)
**Split from:** #655. Sibling **#657** (producer, `security-sensitive`) is blocked on this one.
**Security-sensitive:** no (label absent — wire vocabulary only, no inbound trust boundary, no producer; the producer carries the label).

---

## Files to read first

Read these before writing anything. Each addition mirrors an existing precedent in this exact package — copy the precedent, don't invent.

- `internal/protocol/codes.go:127-141` — **`TypeResync` const block.** The precedent for this ticket's constant: its own `const ( … )` block, a rationale comment, and the "MUST NOT be added to v1TypeSet" paragraph. Mirror its shape for `TypeSessionTransition`.
- `internal/protocol/messaging.go:26-34` — **`BackfillSincePayload`.** Carries *both* fields this payload needs: `time.Time` (`since_ts`, RFC3339Nano) and `*string` **without** `omitempty` (`conversation_id`, renders literal `null`). Copy the json-tag discipline verbatim — this is the model for `OccurredAt` and `WorkspaceCwd`.
- `internal/protocol/interactive.go:1-25` — header comment + `TurnStatePayload`. The "plain string, not a named enum" precedent for `Reason` (same as `MessagePayload.Role`, `TurnEndPayload.StopReason`), and the no-omitempty doc style.
- `internal/protocol/compat_test.go:39-53, 97-152` — the three test edit sites: `TestIsV1Compatible` rejection cases (39-53), the `v2OnlyTypes` map (97-108), and `TestTypeConstants_V1V2Partition`'s `all` slice + union-count check (110-152).
- `internal/protocol/messaging_test.go:82-118` — **`TestBackfillSincePayload_RoundTrip`.** The exact template for the new test: byte-equal envelope round-trip, `.Equal` for the `time.Time` field, `*string` nil assertion, and the regression-guard comment explaining why byte-equality catches an accidental `omitempty`.
- `internal/protocol/interactive_test.go:9-28` — `roundTripEnvelope(t, env, payload, raw)` helper. Same package, reusable for the new test's round-trip assertion.
- `internal/protocol/testdata/backfill_since.json` — fixture shape showing `"conversation_id":null` and an RFC3339 `since_ts`. Author the two new fixtures in **struct-field order** (see § Testing — `canonical()` compacts but does not sort keys).
- `internal/protocol/envelope.go:111-130` — `v1TypeSet`. **Do NOT add the new constant here.** The partition test enforces its absence; this is the one file you must not touch.
- `docs/protocol-mobile.md:402-435` — § Application message types table; add the row after the `resync` row (433).
- `docs/protocol-mobile.md:484-567` — § Interactive events intro + `#### resync` (557-565). Mirror the field-table + invariant prose; place the new `#### session_transition` subsection here.

---

## Context

The v2 wire carries message content but has **no session-lifecycle representation**. `MessagePayload` is `{conversation_id, message_id, role, text}` — no `session_id`, no timestamp, no transition reason. The v2 interactive event family (`turn_state` / `assistant_delta` / `tool_use` / `tool_result` / `turn_end` / `stall` / `resync`) has no session-transition type.

Mobile `pyrycode/pyrycode-mobile#336` needs, per its `ThreadItem.SessionBoundary` data model: `previousSessionId`, `newSessionId`, `reason ∈ {Clear, IdleEvict, WorkspaceChange}`, `occurredAt`, and `workspaceCwd` (non-null **iff** `WorkspaceChange`). None exist on the wire, so the marker can't be constructed mobile-side. Boundary markers were de-scoped from mobile #313 on 2026-06-01 for exactly this reason.

**This ticket adds the wire representation only:** the type constant, the payload struct, the round-trip test, and the docs SSOT. The producer that emits it on session transitions is sibling **#657** (blocked on this). See ADR 025 (`docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md`).

---

## Design

Three production-surface additions, all additive, all mirroring an existing precedent. Nothing is dispatched, wired, or fanned out — this is pure leaf-data wire vocabulary.

### 1. `internal/protocol/codes.go` — the type constant

Add a **new `const` block at the end of the file** (after the `TypeResync` block), following the `resync` precedent — its own block with a dedicated rationale comment, not appended to the six-event interactive group (whose "these six … turn-event model" comment must stay accurate; `session_transition` is a session-boundary marker, not a turn-stream event).

```go
const (
	TypeSessionTransition = "session_transition" // binary → phone, outbound v2 session-boundary marker
)
```

The doc comment above it must state: (a) what it is — an outbound binary→phone marker for mobile#336's `ThreadItem.SessionBoundary`; (b) the payload lives in `SessionTransitionPayload` (messaging.go); (c) the **MUST NOT be added to `v1TypeSet`** paragraph (verbatim shape from the `TypeResync` comment) — an old phone must never receive it; (d) the producer is sibling #657; this ticket is wire vocabulary only. Keep the comment in the established style; do not exceed the density of the `TypeResync` comment.

### 2. `internal/protocol/messaging.go` — the payload struct

Add `SessionTransitionPayload` (1 new exported type). **Placement: `messaging.go`, not `interactive.go`** — `interactive.go` is explicitly scoped to "the wire representation of … turn-event model" (a session boundary is not a turn event), and `messaging.go` already houses the two precedents this struct needs (`BackfillSincePayload`'s `time.Time` and `*string`-no-omitempty fields). This is the AC-pinned home and the architecturally correct one.

Contract (field set fixed by mobile#336; json tags are snake_case of the mobile field names):

| Go field | json tag | Type | Notes |
|---|---|---|---|
| `PreviousSessionID` | `previous_session_id` | `string` | The session id that ended. Always present (a transition has a prior session). |
| `NewSessionID` | `new_session_id` | `string` | The session id that began. |
| `Reason` | `reason` | `string` | Closed wire set `{clear, idle_evict, workspace_change}`. **Plain string, not a named enum** — matches `MessagePayload.Role` / `TurnEndPayload.StopReason`; `internal/protocol` is a stdlib-only leaf data package. |
| `OccurredAt` | `occurred_at` | `time.Time` | RFC3339Nano per the envelope timestamp rule. |
| `WorkspaceCwd` | `workspace_cwd` | `*string` | **`*string`, no `omitempty`** (mirror `BackfillSincePayload.ConversationID`): the new workspace dir for `workspace_change`; literal JSON `null` for `clear` / `idle_evict`. Encodes the *workspaceCwd-non-null-iff-workspace_change* invariant directly on the wire. |

The struct's doc comment states the closed `Reason` set, the RFC3339Nano rule, the `*string`-renders-`null` decision, and the invariant. `package protocol` already imports `time` (messaging.go:3) — no new import.

**Out of scope for the payload:** `event_id`. That is an `Envelope`-level field (added in #649, stamped by the producer); it is not a payload field. Do not add it to `SessionTransitionPayload`.

### 3. `docs/protocol-mobile.md` — the SSOT

Two edits, mirroring the `resync` (#647) entry:

- **§ Application message types table** (after the `resync` row, ~433): add
  `| **`session_transition`** | binary → phone | no | **New in v2** (interactive, capability-gated). Session-boundary marker for `pyrycode-mobile#336`. See [Interactive events](#interactive-events-v2-capability-gated). |`
- **§ Interactive events**: add a `#### session_transition` subsection (recommended placement: after `#### stall`, before `#### Reconnect replay & resync`). It must:
  - carry a field table (the five fields above, with `workspace_cwd` typed `string | null`);
  - state the **workspaceCwd-non-null-iff-`workspace_change`** invariant explicitly;
  - note it is a session-boundary marker, **distinct from the six turn-stream events** (so the section intro's "These six envelope types form the structured live-session stream" sentence stays correct — do not bump it to "seven");
  - note the producer is **#657**, and that until a server-side workspace-change source exists the producer will emit only `clear` and `idle_evict` — yet the type admits `workspace_change` so the mobile decoder is exhaustive and the invariant is expressible.

After editing docs, run `qmd update && qmd embed` (per CLAUDE.md) so the SSOT is searchable.

---

## Data flow

```
session transition (producer #657, future)
        │  builds SessionTransitionPayload{prev, new, reason, occurredAt, *cwd}
        ▼
Envelope{Type: TypeSessionTransition, Payload: <marshaled>}  ← this ticket defines this shape
        │  AEAD-sealed in a noise_msg, binary → phone, interactive-capability-gated
        ▼
mobile decode → ThreadItem.SessionBoundary marker (pyrycode-mobile#336)
```

This ticket owns only the boxed line — the wire shape. No emitter, no transport, no dispatch.

---

## Concurrency model

None. Pure data types and JSON (de)serialization; no goroutines, channels, or shared state.

## Error handling

None beyond JSON (un)marshal, which the `encoding/json` stdlib handles. `internal/protocol` is a leaf data package with no failure modes of its own. Forward-compat (unknown-field tolerance) is already the envelope-level contract; nothing new here.

---

## Testing strategy

One new test, `TestSessionTransitionPayload_RoundTrip` in `messaging_test.go`, templated on `TestBackfillSincePayload_RoundTrip`. Two `testdata` fixtures drive it (author both in **struct-field order** — `canonical()` compacts bytes but does not sort keys, so json key order must equal Go field order or the byte-equal check fails):

- `testdata/session_transition.json` — **cwd-unset case.** `reason: "idle_evict"`, `"workspace_cwd": null`. Proves cwd renders literal `null` when unset (the regression guard for the no-`omitempty` decision — identical role to `backfill_since.json`'s `conversation_id: null`).
- `testdata/session_transition_workspace.json` — **cwd-set case.** `reason: "workspace_change"`, `"workspace_cwd": "/home/user/project"`. Proves the key is present with a value when set.

Example fixture (unset case; developer may pick the literal values, but keep the field order):
`{"id":402,"type":"session_transition","ts":"2026-06-09T10:33:14.5Z","payload":{"previous_session_id":"sess-a","new_session_id":"sess-b","reason":"idle_evict","occurred_at":"2026-06-09T10:33:14.5Z","workspace_cwd":null}}`

Test scenarios (bullet form — write in the project's table/inline idiom; `roundTripEnvelope` from interactive_test.go is reusable):

- **unset fixture:** unmarshal envelope → `Type == TypeSessionTransition`; unmarshal payload → assert `PreviousSessionID`/`NewSessionID`/`Reason`; compare `OccurredAt` via **`.Equal`** (never `==` / `reflect.DeepEqual` — monotonic-clock + RFC3339Nano discipline); assert `WorkspaceCwd == nil`; then byte-equal round-trip (re-marshal → `canonical()` equal to raw). Byte-equality is what catches an accidental `omitempty` re-introduction — mirror the explanatory comment from `TestBackfillSincePayload_RoundTrip`.
- **set fixture:** same, but assert `WorkspaceCwd != nil && *WorkspaceCwd == "/home/user/project"` and `Reason == "workspace_change"`; byte-equal round-trip.

Compat tests — three edits in `compat_test.go`:

- Add `TypeSessionTransition: true,` to the `v2OnlyTypes` map.
- Add `TypeSessionTransition` to `TestTypeConstants_V1V2Partition`'s `all` slice (under a new `// v2 session-boundary marker.` comment). The union-count check `len(v1TypeSet)+len(v2OnlyTypes) == len(all)` self-balances (+1 to `v2OnlyTypes`, +1 to `all`).
- Add a rejection case to `TestIsV1Compatible`'s `cases`: `{"session_transition-rejected", TypeSessionTransition, false, ErrUnknownType}`.

`TestV1TypeSet_CoversAllExportedTypeConstants` (asserts `len(all)==16` over v1-only types) is **unaffected** — `session_transition` is not a v1 type; do not touch that test.

Run `go test -race ./internal/protocol/...`, `go vet ./...`, `gofmt`. (Heads-up: the repo may be gofmt-dirty at HEAD under a newer local Go than CI — check `git show HEAD:<f> | gofmt -l` before "fixing" any file you didn't change; reformatting untouched files sprays spurious diffs.)

---

## Acceptance criteria → design mapping

1. `codes.go` defines `TypeSessionTransition` in the v2 interactive (capability-gated) family; `compat_test.go`'s `v2OnlyTypes` includes it; `TestTypeConstants_V1V2Partition` passes; not in `v1TypeSet`. → § Design 1 + Testing.
2. `messaging.go` defines the payload with prev/new session id, reason, `occurred_at` (RFC3339Nano `time.Time`), and nullable `workspace_cwd` (`*string`, no `omitempty`). → § Design 2.
3. `reason` accepts `clear` / `idle_evict` / `workspace_change`, documented as a closed set. → § Design 2 (struct comment) + Design 3 (docs).
4. Unit test round-trips through JSON, compares `time.Time` via `.Equal`, asserts cwd absent/null when unset and present when set. → § Testing (two fixtures).
5. `docs/protocol-mobile.md` documents the type under § Interactive events + adds the § Application message types row, mirroring `resync`; states the workspaceCwd-non-null-iff-`workspace_change` invariant. → § Design 3.

---

## Scope (size self-check)

Production source files modified (excluding `*_test.go`, `*.md`, `testdata/*.json`, this spec): **`codes.go`, `messaging.go` = 2.** Below the ≥5-file gate. New exported types: **1** (`SessionTransitionPayload`). New constants: 1. No consumer call-site cascade — additive wire vocabulary, nothing dispatches or imports it yet (the producer is #657). Total written LOC (struct + const block + comments + one test fn + two ~1-line fixtures + 3 small compat edits + ~15 docs lines) ≈ 120. Solidly S. No red line tripped.

## Open questions

- **`previous_session_id` nullability.** Kept a plain non-null `string`: the producer emits on a *transition*, which by definition sits between two sessions, so a prior id always exists (matches mobile#336's non-null `previousSessionId`). If #657's producer design surfaces a genuine first-session edge with no prior id, revisit there — not here.
- **Reason wire-value casing.** Fixed as lowercase snake `clear` / `idle_evict` / `workspace_change` per the AC and mobile#336. The mobile enum names (`Clear` / `IdleEvict` / `WorkspaceChange`) map to these by the decoder; no Go-side enum constants are introduced (plain-string precedent).
