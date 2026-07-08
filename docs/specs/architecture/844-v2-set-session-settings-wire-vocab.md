# Spec #844 — v2 wire vocabulary for the set-session-settings verb + reply

**Size:** S (confirmed; PO sized S). Purely additive protocol vocabulary — 2 new
`Type*` constants, 1 new error code, 2 payload structs, golden round-trips, docs.
No handler, no `dispatchAppFrame` case, no persistence call (those land in the
handler sibling **#845**).

**Not `security-sensitive`.** This child defines message *shape* only — no nonce,
no token, no capability primitive. The `yolo` field is a shape, not a gate; the
fail-safe default (nil = leave unchanged, an omitted YOLO can never enable bypass)
lives in `sessions.SettingsUpdate` (#840) and is *enforced* by the handler. The
security label rides the handler #845 (which gates on capability, validates, and
persists). This follows the established wire-vocab → handler split precedent:
#701→#703, #720→#723, #812→#813, #656→#657.

---

## Files to read first

- `internal/protocol/codes.go:64-106` — the v2-only `Type*` const-block pattern
  (`TypeRekeyRequest`, the interactive events). Copy the doc-comment shape: the
  "MUST NOT be added to v1TypeSet … the drift detector in compat_test.go
  partitions …" boilerplate is load-bearing and every v2 cluster repeats it.
- `internal/protocol/codes.go:304-337` — `TypeNewSession` (inbound v2 control) and
  `TypeRequestDebugBundle`: the closest naming precedent for the request verb
  (imperative, phone → binary, intercepted pre-`dispatch.Route`).
- `internal/protocol/codes.go:1-31` — the `Code*` error-constant block; add a new
  **`// Session errors.`** group here in spec-table order.
- `internal/protocol/messaging.go:36-64` — `SessionTransitionPayload`: struct-shape
  precedent, and the **contrast** for the optional-field decision. It uses
  `*string` **without** `omitempty` to force a literal `null` on the wire. This
  ticket wants the opposite (key *absent*, not `null`), so it uses `omitempty` —
  see § Design.
- `internal/protocol/messaging.go:30-33` — `BackfillSincePayload`: the inline
  comment explaining the omitempty/no-omitempty wire-shape trade-off, and the
  byte-equal regression-guard idea its test encodes.
- `internal/protocol/compat_test.go:82-212` — the three partition tests you must
  extend: `TestIsV1Compatible` (rejection cases), `TestV1TypeSet_CoversAll…`
  (leave untouched — it counts v1 types only), `TestTypeConstants_V1V2Partition`
  (`all` list + `v2OnlyTypes` map), and `TestErrorCode_Constants_MatchSpec` (the
  drift assertion — add `session.not_found`, bump the count 12→13).
- `internal/protocol/messaging_test.go:82-118` — `TestBackfillSincePayload_RoundTrip`:
  the exact golden round-trip shape to mirror (unmarshal envelope → assert Type →
  unmarshal payload → assert pointer nil/non-nil → re-marshal → `bytes.Equal` on
  `canonical()`).
- `internal/protocol/envelope_test.go:11-30` — `canonical(t, b)` and
  `readFixture(t, name)` helpers the new test reuses (both are package-local).
- `internal/sessions/session.go:75-88` — `sessions.SettingsUpdate{Model, Effort
  *string; YOLO *bool}`: the decode target the handler sibling will `json.Unmarshal`
  the request payload into. Field names + pointer semantics must line up 1:1.
- `docs/protocol-mobile.md:405-450` — the Application-message-types table (append two
  rows after the `request_debug_bundle` row at line 448).
- `docs/protocol-mobile.md:708-718` — the `### New session (v2)` subsection: the
  prose template for the new `### Session settings (v2)` subsection.
- `docs/protocol-mobile.md:804-811` — the Error-codes *additions* table; append the
  `session.not_found` row here.

---

## Context

Desktop (pyrycode-desktop#156) and a forthcoming mobile client both need one shared
daemon message to change per-session settings (model, reasoning effort, YOLO). The
relay forwards v2 frames opaquely, so this is a v2-only protocol type pair. `#840`
already landed `sessions.Pool.UpdateSettings(id, SettingsUpdate)` — the persistence
seam. This ticket introduces the **wire vocabulary** the handler sibling #845 will
decode into that seam. Splitting vocab from handler is the repo's standard v2
precedent (see the header).

Why the split is worth it: the request carries a **presence contract** (per-field
"omitted vs set") that is subtle enough to deserve its own golden round-trip test,
independent of any handler logic. Getting the wire shape wrong (e.g. `omitempty` on
a value field instead of a pointer, so an unset YOLO reads as `false`) is a silent
security-relevant bug the handler can't fix. Nailing the shape here, with a
regression test, de-risks #845.

---

## Design

### New envelope types (`internal/protocol/codes.go`)

One new v2-only const block, mirroring the `TypeNewSession` block's doc-comment
shape (the "MUST NOT be added to v1TypeSet … drift detector partitions …"
boilerplate is required):

| Constant | Wire value | Direction | Nature |
|---|---|---|---|
| `TypeSetSessionSettings` | `set_session_settings` | phone → binary | inbound v2 control (intercepted pre-`dispatch.Route` by the handler in #845) |
| `TypeSessionSettingsUpdated` | `session_settings_updated` | binary → phone | outbound v2 reply confirming the change |

Both are **v2-only**: absent from `v1TypeSet`, present in `v2OnlyTypes`. The doc
comment must state that #844 is wire-vocabulary-only and the handler that
intercepts the request, validates, persists via `Pool.UpdateSettings`, and emits
the reply is sibling #845.

Naming rationale: `set_session_settings` follows the imperative inbound-control
convention (`new_session`, `dequeue_message`, `modal_answer`, `request_debug_bundle`);
`session_settings_updated` follows the past-tense/noun outbound convention
(`conversation_updated`, `modal_dismissed`, `session_transition`).

### New error code (`internal/protocol/codes.go`)

Add a **`// Session errors.`** group to the `Code*` block:

```go
// Session errors.
CodeSessionNotFound = "session.not_found"
```

Returned by the handler sibling when the request's `session_id` names no live
session. Defined here (dotted-string form, `Code<Category><Reason>` naming) so the
vocabulary is complete; no consumer in this ticket.

### Payload structs (new file `internal/protocol/settings.go`)

`codes.go` is constants-only, so the two payload structs get a new file. Contract
sketch (full field set; developer writes the doc comments in the package idiom):

```go
// SetSessionSettingsPayload — body of a TypeSetSessionSettings request.
// Pointer fields are the presence contract: nil = "leave unchanged",
// non-nil = "set to this value" (including "" / false). Mirrors
// sessions.SettingsUpdate so the #845 handler decodes 1:1.
type SetSessionSettingsPayload struct {
    SessionID string  `json:"session_id"`
    Model     *string `json:"model,omitempty"`
    Effort    *string `json:"effort,omitempty"`
    YOLO      *bool   `json:"yolo,omitempty"`
}

// SessionSettingsUpdatedPayload — body of a TypeSessionSettingsUpdated reply.
// Identifies the session the change was applied to (correlated with the
// request via Envelope.InReplyTo at the handler layer).
type SessionSettingsUpdatedPayload struct {
    SessionID string `json:"session_id"`
}
```

**The load-bearing decision — `omitempty` on the three pointer fields.** This is
the crux of AC #2 and AC #5, and it is subtle enough to be the reason this vocab
earned its own ticket. Go's `omitempty` treats a **non-nil pointer as non-empty
regardless of the pointee**. Therefore:

| Field state | Marshals to | Unmarshals back to |
|---|---|---|
| `nil` | key absent | `nil` — "omitted / leave unchanged" |
| non-nil `*""` (Model/Effort) | `"model":""` | non-nil `*""` — "set to empty, distinct from omitted" |
| non-nil `*false` (YOLO) | `"yolo":false` | non-nil `*false` — "explicitly set false, never reads as omitted" |

So an absent YOLO can never masquerade as a sent `false`, and an absent Model can
never masquerade as an instruction to clear the stored value — exactly AC #2.

**Contrast with the precedent, and why it differs.** `SessionTransitionPayload`
and `BackfillSincePayload` use `*string` **without** `omitempty` *on purpose* —
their spec wire shows a literal `null` with sentinel meaning ("all conversations").
This ticket wants the opposite: an unset field is *absent* (the minimal wire shape
a client that changes only one setting naturally produces), not `null`. Hence
`omitempty`. Call this out in the struct doc comment so a future contributor doesn't
"fix" it by copying the sibling payloads' no-omitempty style.

`session_id` (not `conversation_id`) is the addressing key: it matches
`Pool.UpdateSettings(id sessions.SessionID, …)` and is already carried to clients in
the `session_transition` marker's `new_session_id` field, so a client already knows
it. Plain `string`, no omitempty — always required.

### Data flow (informational — nothing in this ticket wires it)

```
client                     daemon (handler = #845, NOT this ticket)
  │  set_session_settings   │
  ├────────────────────────>│  dispatchAppFrame intercepts (v2 control)
  │  {session_id, model?,    │  → decode into sessions.SettingsUpdate
  │   effort?, yolo?}        │  → Pool.UpdateSettings(id, update)
  │                          │     ├─ unknown id → error{code: session.not_found}
  │  session_settings_updated│     └─ ok → reply
  │<────────────────────────┤  {session_id}, in_reply_to = request.id
```

This ticket delivers only the boxes' *vocabulary* — the two `Type*` constants, the
error code, and the two payload structs. The arrows are #845.

---

## Concurrency model

None. `internal/protocol` is pure data (no I/O, no goroutines, no context) per the
package doc. No concurrency surface is added.

---

## Error handling

- `CodeSessionNotFound` is *defined* here, *returned* by #845. Per the repo
  convention (PROJECT-MEMORY § "Refusal-to-wire-code mapping is the consumer's
  job"), the dotted-string wire code lives as a `Code*` constant; the mapping from
  a Go sentinel (`sessions.ErrSessionNotFound` or equivalent, in #845) to this wire
  code happens at the handler call site via `errors.Is`. Not this ticket.
- Malformed request payloads (bad JSON, wrong types) are a handler concern — the
  handler's `json.Unmarshal` into `SetSessionSettingsPayload` surfaces them, mapped
  to `protocol.malformed`. This ticket adds no validation.

---

## Testing strategy

All additive; no existing assertion changes value, only counts grow.

**Partition + drift (`internal/protocol/compat_test.go`):**

- `TestIsV1Compatible` — add two rejection cases to the `cases` table:
  `{"set_session_settings-rejected", TypeSetSessionSettings, false, ErrUnknownType}`
  and `{"session_settings_updated-rejected", TypeSessionSettingsUpdated, false,
  ErrUnknownType}`. Both must reject: the request is inbound v2 control (never a v1
  type), the reply is an outbound event an old phone must never receive.
- `TestTypeConstants_V1V2Partition` — add both constants to the `all` list (under a
  `// v2 session-settings vocabulary.` comment) **and** to the `v2OnlyTypes` map.
  The `len(v1TypeSet)+len(v2OnlyTypes) == len(all)` check keeps this honest: adding
  to only one place fails the test.
- `TestV1TypeSet_CoversAllExportedTypeConstants` — **do not touch.** It asserts
  `v1TypeSet` has exactly the 16 v1 types; these two are not v1 types.
- `TestErrorCode_Constants_MatchSpec` — add `"CodeSessionNotFound":
  CodeSessionNotFound` → `"session.not_found"` to both the `cases` and `want` maps.
  The `len(cases) != len(want)` guard and per-key comparison catch a
  constant/string drift. (Count 12 → 13.)

**Golden round-trip (new `internal/protocol/settings_test.go` + 3 fixtures under
`testdata/`):**

Mirror `TestBackfillSincePayload_RoundTrip` (unmarshal envelope → assert `Type` →
unmarshal payload → assert field/pointer states → re-marshal envelope → `bytes.Equal`
on `canonical()`). The byte-equal check is the regression guard for the `omitempty`
decision, exactly as BackfillSince's guards its no-omitempty decision.

- **Fixture `set_session_settings_full.json`** — all three fields present at their
  **zero values**: `{"session_id":"sess-a","model":"","effort":"","yolo":false}`.
  Assertions: after unmarshal, `Model`/`Effort`/`YOLO` are all **non-nil** and point
  to `""`/`""`/`false`; re-marshal is byte-equal (all three keys survive with zero
  values). This is the "explicit zero stays distinguishable" half of AC #5.
- **Fixture `set_session_settings_omitted.json`** — only the required key:
  `{"session_id":"sess-a"}`. Assertions: after unmarshal, all three pointers are
  **nil**; re-marshal is byte-equal (the three keys stay *absent* — `omitempty`
  drops them). This is the "omitted field" half of AC #5, and proves an omitted
  YOLO never appears as `false`.
- **Fixture `session_settings_updated.json`** — reply: `{"session_id":"sess-a"}`.
  Assertions: `Type == TypeSessionSettingsUpdated`, payload `SessionID == "sess-a"`,
  byte-equal round-trip.

Table-driven is fine for the two request fixtures (present-zero vs omitted share the
assertion structure over a `wantNil bool` per field); the reply gets its own small
test. Keep to stdlib `testing`, `t.Parallel()`.

**Verification:** `go test -race ./internal/protocol/...`, `go vet ./...`,
`gofmt`. No PTY / integration surface.

---

## Documentation (`docs/protocol-mobile.md`) — AC #6

1. **Application-message-types table** (after the `request_debug_bundle` row, ~L448)
   — two rows in the established `**\`type\`** | direction | encrypted | notes`
   shape:
   - `set_session_settings` | phone → binary | no | **New in v2.** Inbound control —
     client changes per-session model / effort / YOLO. Interactive-capability-gated
     (enforced by the handler #845). See [Session settings](#session-settings-v2).
   - `session_settings_updated` | binary → phone | no | **New in v2.** Reply
     confirming a `set_session_settings`, correlated by `in_reply_to`. See
     [Session settings](#session-settings-v2).
2. **New `### Session settings (v2)` subsection** (mirror the `### New session (v2)`
   prose). Must document: direction of each type; the pointer/optional presence
   contract (omitted key = leave unchanged, present key incl. zero value = set,
   `yolo` absent can never mean `false`); that `session_id` is the addressing key
   (naming it explicitly, matching `Pool.UpdateSettings`); and that the handler,
   capability gate, and persistence are #845. Include a small request + reply JSON
   example showing a partial change (e.g. only `effort` set).
3. **Error-codes additions table** (~L808) — one row:
   `session.not_found` | no | The `set_session_settings` target `session_id` names
   no live session. Returned by the handler (#845).

Note: `docs/protocol-mobile.md` is the developer's deliverable and is in-scope for
the worktree (it is the protocol's source of truth, edited alongside the code, not
a `docs/knowledge/` evergreen). The per-ticket `docs/knowledge/codebase/844.md` is
**not** an AC — the documentation phase writes it post-merge.

---

## Open questions

- **Wire key for the YOLO field.** Spec uses `"yolo"` (lowercased Go field name,
  consistent with `session_id`/`conversation_id`). It is the daemon-internal term
  and the ticket frames the feature as "YOLO" throughout; desktop#156 follows this
  vocabulary since #844 *defines* it. If the desktop team has already shipped a
  different key, reconcile before #845 — but the daemon is the source of truth here.
  **Recommendation: `"yolo"`.** No blocker.
- **Should the reply echo the applied settings?** No — AC #3 asks only that it
  identify the session. The client knows what it sent; `session_id` + `in_reply_to`
  is sufficient correlation. Keep the reply minimal (vocab-only discipline); #845
  can extend if a concrete need appears.
