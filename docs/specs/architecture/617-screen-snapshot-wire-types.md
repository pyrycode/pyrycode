# Spec — #617: Screen-snapshot wire types (`request_snapshot` / `screen_snapshot`) + v2 partition

**Size:** XS (confirmed; PO sized XS). Two payload structs, two `Type*` constants in the v2-only
partition, two fixtures, doc amendment. One package (`internal/protocol/`). No consumer cascade — all
new symbols are additive.

**Scope, held to the line:** wire **vocabulary only.** Pure protocol structs + their
(de)serialization, two new `Type*` constants placed in the v2-only partition, two `testdata/`
fixtures, and the `docs/protocol-mobile.md` amendment. **No interception, no render, no push, no
dispatch, no validation.** The daemon handler that intercepts `request_snapshot` at the v2 dispatch
boundary, renders the screen via tui-driver, and pushes `screen_snapshot` back is the **consumer
ticket** (the screen-snapshot handler child), which carries `security-sensitive`. This is the same
wire-types/consumer split #607 (interactive event wire types) drew against #608 (the bridge
consumer).

This ticket is **not** `security-sensitive`: no inbound frame is accepted, no trust decision is made,
nothing is dispatched here — only structs and constants are declared. The trust boundary lives in the
consumer. (Same reasoning #607 used versus #608.)

---

## Files to read first

Read these before writing anything; this is the turn-1 data load.

- `internal/protocol/interactive.go` (whole, 78 lines) — **the per-type struct-file template.** Copy
  its shape exactly: file-level doc comment stating "wire vocabulary only," per-struct doc comment
  pointing at the `docs/protocol-mobile.md §`, **no `omitempty` on any field**, pure data (no methods).
- `internal/protocol/interactive_test.go:9-53` — the `roundTripEnvelope(t, env, payload, raw)` helper
  and `TestTurnStatePayload_RoundTrip`. **REUSE `roundTripEnvelope` — it is in this package already;
  do NOT redefine it** (redefinition is a compile error). Mirror the per-type test body.
- `internal/protocol/envelope_test.go:11-27` — `canonical(t, b)` and `readFixture(t, name)` helpers
  (same package, reuse directly). `:43-49` — the **`env.TS.Equal(wantTS)`** comparison pattern; the
  `ts` field on `screen_snapshot` follows it verbatim.
- `internal/protocol/codes.go:64-105` — the two existing v2-only `Type*` const blocks (control block
  `TypeRekeyRequest`; interactive block `TypeTurnState…`). The new block mirrors these, including the
  block-level comment stating the constants MUST NOT be added to `v1TypeSet`.
- `internal/protocol/envelope.go:80-125` — `IsV1Compatible` + `v1TypeSet`. **The load-bearing
  absence: the two new constants MUST NOT be added to `v1TypeSet`.** `IsV1Compatible` returns
  `ErrUnknownType` for any type not in that set — which is exactly what AC #2 requires for both.
- `internal/protocol/compat_test.go:27-46` (the `IsV1Compatible` rejection-case table), `:80-97`
  (the test-local `v2OnlyTypes` map), `:99-137` (`TestTypeConstants_V1V2Partition`). These are the
  three edit sites for the partition. **Do NOT touch `TestV1TypeSet_CoversAllExportedTypeConstants`
  (`:57-78`) or its hardcoded `16`** — the new types are not v1 types, so that count is unchanged.
- `internal/protocol/testdata/turn_state.json` — fixture format: a single-line JSON envelope
  `{"id","type","ts","payload":{…}}`. Both new fixtures follow this exactly.
- `docs/protocol-mobile.md:396-423` — Application message types table (add two rows).
  `:464-512` — Interactive events section (style template for the new "Screen snapshot" section).
  `:219-236` — `rekey_request` shape (style template for documenting an inbound control type).
- `docs/PROJECT-MEMORY.md` § Project-level conventions — the **`time.Time` round-trip discipline**
  (monotonic reading strips on marshal; compare with `time.Time.Equal`, never `==`/`reflect.DeepEqual`).
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md` § Safe degradation and
  § Wire-protocol extension — the source pinning both shapes and the no-raw-bytes invariant on
  `screen_snapshot.text` (parser-independent floor; survives any parser break; backs the stall
  fallback).

---

## Context

ADR 025 § Safe degradation makes an always-available, parser-independent **screen snapshot** the floor
of the degrade strategy: the phone can ask for a one-shot text picture of the current claude screen at
any time, and because it depends on no screen parser it survives any parser break and backs the stall
fallback. The request/response pair:

- `request_snapshot` — `{conversation_id}`, **phone → binary** (inbound v2 control).
- `screen_snapshot` — `{conversation_id, text, ts}`, **binary → phone** (outbound v2 event). A one-shot
  **plain-text** picture of the current screen; never raw control codes.

This ticket lands the wire vocabulary for that pair and nothing else.

---

## Design

### Package structure

| File | Change | What |
|---|---|---|
| `internal/protocol/snapshot.go` | **new** | `RequestSnapshotPayload`, `ScreenSnapshotPayload` structs (mirrors `interactive.go`). |
| `internal/protocol/codes.go` | modify | One new const block: `TypeRequestSnapshot`, `TypeScreenSnapshot`. |
| `internal/protocol/snapshot_test.go` | **new** | Round-trip tests (mirrors `interactive_test.go`). |
| `internal/protocol/compat_test.go` | modify | Add both to `v2OnlyTypes` + partition list + `IsV1Compatible` rejection cases. |
| `internal/protocol/testdata/request_snapshot.json` | **new** | Fixture. |
| `internal/protocol/testdata/screen_snapshot.json` | **new** | Fixture (multi-line `text`). |
| `docs/protocol-mobile.md` | modify | Two table rows + new "Screen snapshot (v2)" section. |

Production source files: **2** (`snapshot.go`, `codes.go`). Well under the size-S boundary.

### Types — `internal/protocol/snapshot.go`

Contract sketch (the developer writes the full doc comments in the `interactive.go` idiom):

```go
// RequestSnapshotPayload — Envelope.Type == TypeRequestSnapshot. phone → binary.
type RequestSnapshotPayload struct {
    ConversationID string `json:"conversation_id"`
}

// ScreenSnapshotPayload — Envelope.Type == TypeScreenSnapshot. binary → phone.
type ScreenSnapshotPayload struct {
    ConversationID string    `json:"conversation_id"`
    Text           string    `json:"text"` // plain rendered text only; never raw control codes
    TS             time.Time `json:"ts"`
}
```

- **No `omitempty` on any field** — matches the `interactive.go` convention so the fixtures pin the
  full shape (an empty `conversation_id` or zero `ts` stays on the wire).
- `snapshot.go` imports `"time"` (the only import; `interactive.go` has none).
- Field name `TS` with json tag `ts` mirrors `Envelope.TS` for consistency.
- **Doc-comment invariants (required by the ticket's technical notes):**
  - `RequestSnapshotPayload` / `TypeRequestSnapshot`: this is an **inbound v2 control envelope**,
    structurally like `TypeRekeyRequest` — the v2 session manager intercepts it at the dispatch
    boundary before `dispatch.Route`. There is **no `dispatch.Route` handler** for it; the
    interception + render + push is the consumer ticket's job. Say so, so the next reader does not
    look for a handler that isn't there.
  - `ScreenSnapshotPayload.Text` / `TypeScreenSnapshot`: `Text` is **plain rendered text only, never
    raw terminal control codes** — this preserves ADR 025's no-raw-bytes invariant and the substrate
    seal. The struct's doc comment must state this.

### Constants — `internal/protocol/codes.go`

Add **one new const block** at the end of the file (after the interactive block, `:99-105`),
grouping the snapshot pair so a reader greps "snapshot" and finds both adjacent with their rationale.
The two existing v2 blocks already group by theme; this is a third themed block. (Splitting the pair
across the existing control/interactive blocks is also defensible, but the cohesive block reads
better — default to it.)

Block-level comment, mirroring `codes.go:64-98`, must state: both are **v2-only**; both **MUST NOT be
added to `v1TypeSet`** (`envelope.go`); the drift detector in `compat_test.go` partitions all `Type*`
constants between `v1TypeSet` and `v2OnlyTypes` and these two live in the latter. Per-constant
comments carry the inbound-control / outbound-event-plain-text nature described above.

```go
const (
    TypeRequestSnapshot = "request_snapshot" // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
    TypeScreenSnapshot  = "screen_snapshot"  // binary → phone, outbound v2 event (plain text only)
)
```

### Partition wiring — `internal/protocol/compat_test.go`

Three edits; no production-code change to `envelope.go` (the v1 absence is the whole point):

1. `v2OnlyTypes` (`:90-97`) — add `TypeRequestSnapshot: true, TypeScreenSnapshot: true`.
2. `TestTypeConstants_V1V2Partition`'s `all` list (`:107-121`) — add both under a new
   `// v2 screen-snapshot types` comment. The trailing assertion
   `len(v1TypeSet)+len(v2OnlyTypes) == len(all)` then balances automatically (16 + 8 == 24).
3. `TestIsV1Compatible`'s `cases` table (`:27-46`) — add two rows, both `ErrUnknownType`:
   `{"request_snapshot-rejected", TypeRequestSnapshot, false, ErrUnknownType}` and
   `{"screen_snapshot-rejected", TypeScreenSnapshot, false, ErrUnknownType}`.

**Leave `TestV1TypeSet_CoversAllExportedTypeConstants` and its `16` untouched** — the new types are
not v1 types.

---

## Data flow

Pure data; no runtime flow in this ticket. Marshal/unmarshal only:

```
phone  --(request_snapshot {conversation_id})-->  [consumer ticket intercepts pre-dispatch.Route]
binary --(screen_snapshot {conversation_id, text, ts})-->  phone   [consumer ticket renders + pushes]
```

Both ride inside a `noise_msg` (post-handshake AEAD-sealed payload), per the v2 envelope model — but
that framing is the consumer's concern. Here: `payload` is the deferred-decode `json.RawMessage` on
`Envelope`, decoded into the new struct via a second-pass `json.Unmarshal`, exactly as every other
per-type payload.

---

## Concurrency model

None. `internal/protocol` is a pure, stdlib-only leaf data package: no goroutines, no I/O, no context.

---

## Error handling

None added. `IsV1Compatible` already returns `ErrUnknownType` for any type absent from `v1TypeSet`;
the two new types inherit that behaviour for free by being absent (AC #2). No new sentinels, no new
reject branches.

---

## Testing strategy

New tests in `internal/protocol/snapshot_test.go` (same package; reuse `canonical`, `readFixture`,
`roundTripEnvelope` — do not redefine them). Per-type round-trip in the `interactive_test.go` idiom:

- **`TestRequestSnapshotPayload_RoundTrip`** — unmarshal `testdata/request_snapshot.json` → assert
  `env.Type == TypeRequestSnapshot`; unmarshal payload → assert `ConversationID`; then
  `roundTripEnvelope(t, env, payload, raw)` (canonical byte-equal).
- **`TestScreenSnapshotPayload_RoundTrip`** — unmarshal `testdata/screen_snapshot.json` → assert
  `env.Type == TypeScreenSnapshot`; unmarshal payload → assert `ConversationID`, `Text`, and
  **`payload.TS.Equal(wantTS)`** (parse `wantTS` with `time.Parse(time.RFC3339Nano, …)`; **never**
  `==` or `reflect.DeepEqual` — PROJECT-MEMORY `time.Time` discipline, AC #3). Use a fixture `text`
  that contains **newlines / multi-line content** (a rendered screen is multi-line) so the canonical
  round-trip pins the escaped multi-line shape (AC #4).
- **Empty-`conversation_id` boundary** (AC #4) — a programmatic sub-test (no fixture): construct
  `RequestSnapshotPayload{ConversationID: ""}`, marshal → assert the bytes contain
  `"conversation_id":""` (no `omitempty` drop) → unmarshal → assert the empty string round-trips.
  Mirrors the existing `Seq==0` / `IsError==false` boundary-pinning style.

Partition coverage (AC #2) is asserted by the `compat_test.go` edits above — `IsV1Compatible` returns
`ErrUnknownType` for both, and `TestTypeConstants_V1V2Partition` proves both are classified v2-only
and absent from `v1TypeSet`.

### Fixtures

Single-line JSON envelopes, `turn_state.json` format:

- `testdata/request_snapshot.json` — `{"id":…,"type":"request_snapshot","ts":"…","payload":{"conversation_id":"c1"}}`
- `testdata/screen_snapshot.json` — `{…,"type":"screen_snapshot","payload":{"conversation_id":"c1","text":"line one\nline two\n…","ts":"…"}}`

**`ts` byte-stability gotcha (read carefully).** `roundTripEnvelope` re-marshals the payload struct and
does a canonical byte-compare against the fixture. `time.Time.MarshalJSON` emits RFC3339Nano with
**trailing fractional zeros trimmed**. So the `payload.ts` string in the fixture MUST be exactly what
Go re-emits, or the byte-compare fails. Safe: a whole-second UTC value (`"2026-05-08T10:33:14Z"`) or a
fraction with no trailing zeros (`"2026-05-08T10:33:14.5Z"`, matching `turn_state.json`'s envelope
`ts`). Avoid `".120"` (Go re-emits `".12"`) and non-`Z` offsets. The same constraint already holds for
the envelope's own `ts`.

### Doc amendment — `docs/protocol-mobile.md` (AC #5)

1. **Application message types table (`:400-423`)** — add two rows after the interactive rows:
   - `request_snapshot` | phone → binary | no | **New in v2.** On-demand screen snapshot request. See [Screen snapshot].
   - `screen_snapshot` | binary → phone | no | **New in v2.** See [Screen snapshot].
2. **New section `### Screen snapshot (v2)`** after the Interactive events section (after `:512`,
   before `## Backfill semantics`), in the per-type style of `:464-512` / `:219-236`:
   - 1-2 sentence intro tying to ADR 025 § Safe degradation (parser-independent floor; phone may ask
     for a one-shot text picture at any time; survives any parser break; backs the stall fallback).
   - `#### request_snapshot` — direction phone → binary (inbound control). Field table:
     `conversation_id` (string). Note: intercepted by the v2 session manager before `dispatch.Route`
     (consumer ticket); not a `dispatch.Route` handler.
   - `#### screen_snapshot` — direction binary → phone. Field table: `conversation_id` (string),
     `text` (string — **plain rendered text only; never raw terminal control codes**), `ts` (RFC3339
     — when the snapshot was rendered). Note: all fields always present (no omitempty).
3. **Run `qmd update && qmd embed`** after the doc edit (AC #5; `embed` alone won't index the change
   in-place but the edit is to an existing file — run both to be safe, per CLAUDE.md).

---

## Open questions

- **Const block grouping** — spec defaults to one cohesive snapshot block in `codes.go`. If the
  developer finds splitting `TypeRequestSnapshot` into the control block and `TypeScreenSnapshot` into
  the interactive block reads better, that is acceptable; the `compat_test.go` partition enforces
  correctness regardless of grouping. Default to the cohesive block.
- **None blocking.** All shapes are pinned by ADR 025 and the ticket body; no design decision is left
  to implementation.
