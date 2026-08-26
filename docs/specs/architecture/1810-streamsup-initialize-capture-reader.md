# #1810 — Read the initialize-control captures from `internal/streamsup`

Test-only. No production file changes.

## Files to read first

- `internal/streamsup/capture_test.go` → `capturePath`, `capturedLines` — **the idiom to copy and the
  two sentences that go stale.** The cross-package read (`os.ReadFile` on a
  `../e2e/realclaude/testdata/…` path) is confirmed to resolve from this package today; reuse that.
  Do **not** reuse `droppedLineCapture`, `capturedLine`, `capturedSystemLine(s)` — those decode the
  line-record shape, whose `payload` is a JSON *string*. Read `capturedLines`' doc in full: the
  provenance-at-the-reader argument is the discipline this ticket restates, and two of its claims
  stop being true when this lands (see § Doc corrections).
- `internal/e2e/realclaude/initialize_control_record_test.go` → `initControlFixtureRecord` — the
  **authoritative** field set and JSON tags for this record. It sits behind `e2e_realclaude`, so it
  cannot be imported. Read it to copy tags exactly; do **not** restate its twenty-nine fields (see
  § The struct is deliberately narrow).
- `internal/e2e/realclaude/initialize_control_compare_test.go` → `initControlDiscoverArms` — two
  idioms this spec adopts: binding a file's NAME to its CONTENT by re-minting the name from the
  record's own fields, and accumulating every problem into ONE `t.Fatalf` rather than failing at the
  first.
- `internal/e2e/realclaude/initialize_control_names_test.go` → `initControlFixtureName`,
  `initControlArmFixtureName`, `initControlArms` — the two name shapes (`initialize_control_v<v>.json`
  for the unarmed base, `initialize_control_v<v>_<arm>.json` for the arms) and the arm identifiers.
- `internal/e2e/internal/fakeclaude/initialize_control_test.go` → `captureModelKeySets` — the
  existing walk down `control_responses[…].response.response.models` from a package **outside** the
  `e2e_realclaude` tag. Proof that the nesting and the cross-package read both work; note it walks
  `map[string]any` and skips silently, which is right for its aggregate sweep and wrong here.
- `internal/streamsup/envelope.go` → `marshalInitializeEnvelope`, `WriteInitialize` — the request
  half of the exchange these captures record, written by this same package (#1689).
- `docs/knowledge/features/streamsup-package.md` § "Initialize send primitive (#1689)" — states that
  nothing in this package reads the ack yet. This ticket does not change that; it makes the ack's
  committed bytes *readable*, and #1811 / #1812 / #1719 / #1809 are the decodes that ride it.
- `docs/knowledge/features/e2e-realclaude.md` § `initialize_control_record_test.go` — why the base
  capture "is simply missing the fields, not zero-valued in them". That is the reason this reader
  depends on the common subset only.

## Context

Four captures of claude 2.1.239's reply to the `initialize` control request are committed under
`internal/e2e/realclaude/testdata/`. They are only **bytes** — the `e2e_realclaude` build tag belongs
to that package's Go *files*, not to its testdata — but every Go file that reads them today is behind
that tag, so the proof they carry runs only in `make e2e-realclaude`, which exits 0 with zero tests
executed when there is no claude login. Four downstream tickets (#1811, #1812, #1719, #1809) want to
pin a decode against what claude actually sent, inside `make check`. This slice is the reader they
share, so the provenance discipline is written once instead of four times.

Measured on the committed files 2026-08-26, three facts shape the design:

- **There is no `is_capture` key.** `capturedLines` anchors provenance on it; none of these four
  records carries it. Provenance has to be built from what these records *do* carry.
- **The four do not share one key set.** The base capture (#1688) predates the arm harness (#1763):
  no `arm`, no `send_point_index`, no `after_send_point_*`, no `redaction`. Twenty-two top-level keys
  are common to all four and include everything this reader needs.
- **The outer wrapper's key set is not stable either.** `pending_permission_requests` and
  `pending_user_dialog_requests` are on the base capture and the after-turn arm and **absent** on the
  before-first-turn arm. Only `subtype`, `request_id` and `response` are on all three.

No ADR is warranted. This is a test-support reader inside one package.

## Design

One new file: `internal/streamsup/initialize_capture_test.go`. Plus two doc-comment corrections in
`internal/streamsup/capture_test.go` (§ Doc corrections). Nothing in this package's non-test surface
moves.

A separate file rather than an append to `capture_test.go`: the two record shapes share nothing but
the directory they live in, and keeping them apart is what stops a later reader reaching for
`droppedLineCapture`'s helpers on a control record. The cost is that `capture_test.go`'s uniqueness
claims go stale, which § Doc corrections pays.

### Addressing: an arm selector, never a path

```
initCaptureDir     = "../e2e/realclaude/testdata"
initCaptureVersion = "2.1.239"

initCaptureArmBase             = ""                       // #1688's unarmed capture
initCaptureArmBeforeFirstTurn  = "before_first_turn"
initCaptureArmAfterCompletedTurn = "after_completed_turn"
initCaptureArmNoRequest        = "control_no_request"     // the arm that sent no request

initCaptureArms = []string{ …the four above, base first… }
```

`initCaptureName(version, arm string) string` mints `initialize_control_v<version>.json` when `arm`
is empty and `initialize_control_v<version>_<arm>.json` otherwise — a plain `fmt.Sprintf` pair.
`initCapturePath(arm string) string` joins it under `initCaptureDir`.

**No `versionSlug` port.** The producer slugs both tokens before formatting; every token committed
today is a fixed point of that slug, so a plain format is byte-identical. If a future capture carries
a token that is not slug-clean, the name↔content binding below goes red with a message naming both
strings, and teaching this namer is the fix. That is a loud, explainable failure, which is why the
slug is not copied here — a second copy of it is a second thing to drift.

**No glob discovery in the reader**, unlike `initControlDiscoverArms`. That function globs because it
must not hard-code a version it cannot learn from a live claude and because it needs something to
reject an undeclared arm with. Neither applies here: this package has no live claude, and the arm set
is a closed literal. The version *is* hard-coded, deliberately — a re-capture at a new claude version
must break this package loudly, because the decodes #1811 / #1812 / #1809 build on it were proven
against 2.1.239 and nothing else. The directory-coverage assertion in § Testing strategy is what makes
that break happen at the right moment.

### The struct is deliberately narrow

`initCaptureRecord` restates **only** the fields this reader and its caller use, with tags copied
verbatim from `initControlFixtureRecord`:

`claude_version` (string), `arm` (string), `control_request_id` (string), `control_responses`
(`[]json.RawMessage`), `control_response_subtype` (string),
`control_response_request_id_matched` (bool), `models_present` (bool), `models_count` (int).

`initControlFixtureRecord`'s own doc warns that a parallel shape "is how a field rename lands as a
silent zero value". That risk is real and is accepted with the surface minimised: eight fields, all in
the twenty-two-key common subset, none of them the ones the base capture is missing.

Explicitly **not** restated: `control_request_sent` (a `json.RawMessage` that decodes to the four
bytes `null` on the no-request arm, not to a zero length — a footgun with no upside here, since
`control_request_id` says the same thing as a plain string), `models_entry_fields`, `stdout_events`,
`turn_boundaries`, `redaction`, `credential_scan_applied`, and every `after_send_point_*` /
`send_point_index` field the base capture does not carry.

A second, smaller struct decodes one entry of `control_responses`: top-level `type`, and a nested
`response` object carrying `subtype`, `request_id` and `response` (`json.RawMessage`). Do **not**
restate `pending_permission_requests` / `pending_user_dialog_requests` — they are absent on one of the
three responding captures, so naming them commits this package to a key set claude does not always
send.

### The reader

```go
// capturedInitialize reads the committed initialize-control capture for one arm,
// asserts its provenance, and returns the record beside the initialize payload
// object claude sent — nil when the record says no response arrived.
func capturedInitialize(t *testing.T, arm string) (*initCaptureRecord, json.RawMessage)

// capturedInitializePayload is capturedInitialize with the absent case made fatal:
// the reader for callers that need a payload to decode.
func capturedInitializePayload(t *testing.T, arm string) json.RawMessage
```

Two levels, mirroring `capturedLines` / `capturedLine`: the checks live at the wide reader so the
narrow one inherits rather than copies them. #1811, #1812, #1719 and #1809 call the wrapper.

Payload is `control_responses[0].response.response` — the inner object, not the outer wrapper that
carries `subtype` and `request_id`, and not the whole record. `json.RawMessage` rather than a decoded
type, because each downstream ticket decodes a different slice of it; this is `capturedLines`
returning `[][]byte` for the same reason.

**Arrival is defined as `len(control_responses) > 0`**, and the summary fields are checked *against*
that rather than trusted. `arm == initCaptureArmNoRequest` is not consulted: the record says what it
recorded, and a file whose name and arm say "no request" while its body carries a response must fail
here rather than be believed on its name.

**`models_count` / `models_present` are carried on the record and read by nothing inside the reader.**
This is load-bearing, not an omission. If the reader asserted `len(payload.models) == models_count`,
the § Testing strategy test that asserts exactly that could never fail, and AC 4 would be satisfied by
construction with no test actually pinning it.

### Reject branches (8)

Every one is `t.Fatalf`, never `t.Skip` — the captures are committed, so a missing or unreadable
record is a broken premise, not an unavailable resource.

1. `arm` is not one of `initCaptureArms`. This is what replaces `capturedLines`' "takes no path
   parameter" guarantee: the parameter selects from a closed set and the reader mints the path itself,
   so no caller can put an unchecked file behind these assertions.
2. `os.ReadFile` failed.
3. The record did not decode.
4. **Name↔content binding.** Re-mint the base name from the record's *own* `claude_version` and `arm`
   and compare it to the base name actually opened. Mismatch is fatal, naming both strings — the
   `initControlDiscoverArms` message is the model: *"the file's name and its content disagree, so
   neither can be trusted."* Catches a swapped file, a re-captured version, and one arm's bytes
   committed under another arm's name. For the base capture the arm half of the binding is
   `arm == ""`, which is itself the check that distinguishes #1688's unarmed record from an arm record
   renamed onto it.
5. **Self-consistency**, accumulated into one `t.Fatalf` listing every mismatch (the
   `initControlDiscoverArms` accumulate-then-one-fatal idiom). Rows, with `arrived` =
   `len(control_responses) > 0`:
   - `arrived` ⟺ `control_response_subtype != ""`
   - `arrived` ⟺ `control_request_id != ""`
   - `arrived` ⟺ `control_response_request_id_matched`
   - `arrived` ⟹ `len(control_responses) == 1` — two would make "the initialize payload" ambiguous
     and silently taking the first is the kind of choice that should be deliberate (`capturedLine`'s
     argument)
   - when `arrived`: the entry's top-level `type == "control_response"`, its `response.subtype`
     equals the record's `control_response_subtype`, and its `response.request_id` equals the record's
     `control_request_id`
6. `control_responses[0]` did not decode into the wrapper struct.
7. The inner `response` is not a JSON object, or is an empty one. An empty payload would let every
   downstream decode pass over nothing.
8. (wrapper only) `capturedInitializePayload` was called for a capture with no response.

Absent-payload is **not** a reject: `capturedInitialize` returns the record with a nil payload, which
is AC 3.

## Concurrency model

None. Pure file reads inside `testing` helpers. The test is `t.Parallel()`; the reader holds no state
and returns a fresh record per call, so parallel subtests reading different arms cannot race.

## Error handling

Every failure path is `t.Fatalf` from a `t.Helper()`-marked function, so failures report the caller's
line. No error is returned to a caller and none is recovered. Messages name the base filename (not the
full relative path) and quote both sides of every comparison, so one run says what is wrong with which
file rather than that something is wrong.

## Testing strategy

One test, table-driven over `initCaptureArms`, each row a `t.Parallel()` subtest. Scenarios:

- **Every responding arm** (base, before-first-turn, after-completed-turn): the payload comes back
  non-nil; decoding `models` out of it yields exactly `rec.ModelsCount` entries; `rec.ModelsCount` is
  `> 0` and `rec.ModelsPresent` is true. The `> 0` row is the vacuity guard — without it a reader that
  returned an empty payload passes `0 == 0`.
- **The no-request arm**: the payload is nil, `rec.ModelsCount` is 0, `rec.ModelsPresent` is false.
  Asserted as its own case rather than folded into the count comparison, for the same reason: `0 == 0`
  against a nil payload proves nothing.
- **Directory coverage**: glob `initCaptureDir + "/initialize_control_v*.json"` and assert the matched
  base names are exactly the set `initCaptureArms` mints. A fifth committed capture, or a re-capture
  at a new version, fails here with both sets printed rather than being silently skipped by a table
  that names four files. This is the assertion that makes the hard-coded `initCaptureVersion` safe.

Do not add a name-lock test in the shape of
`TestInitControlFixtureName_AvoidsCommittedFamiliesAndStaysContained`. That namer mints paths a live
writer creates; this one only reads a closed set, and the coverage assertion above already binds it to
the directory.

`make check` runs this package with `-race`, which is the whole point of the ticket: the proof lands
in the standard gate rather than behind `e2e_realclaude`.

## Doc corrections

Two claims in `capture_test.go` stop being true and must be corrected in place, not deleted:

- `capturedLines`' doc says *"This is the package's one capture reader."* It is not, once this lands.
  State what it is instead — the reader for the dropped-line record shape — and point at the new one.
- The same doc's paragraph arguing that the reader takes **no path parameter** because the path is the
  `capturePath` package constant is the guarantee this ticket most needs restated rather than dropped.
  The new reader does take a parameter, so its own doc must say what holds the guarantee there: the
  parameter is an **arm selector from a closed set**, not a path, and the reader mints the path from
  package constants. Reject branch 1 is that sentence made executable.

Both are comment edits inside the diff, so `make cite-guard` applies: cite symbols, never lines.

## Open questions

- **The version constant is a deliberate tripwire, not a limitation.** A re-capture at claude 2.1.240
  reddens the directory-coverage assertion and the name↔content binding, and the fix is a one-token
  edit here plus whatever #1811 / #1812 / #1809 pinned against the old models list. If a later ticket
  wants several versions readable side by side, that is the moment to port discovery — not now.
- **Does the no-request arm earn a wrapper of its own?** `capturedInitializePayload` fatals on it, so
  a caller that wants the absent case calls the wide reader and checks for nil. Left as is until a
  second such caller exists; #1811, #1812, #1719 and #1809 all want a payload.
- **Budget note for the implementer.** The measured analogue —
  `internal/e2e/internal/fakeclaude/initialize_control_test.go`, the other cross-package reader over
  these same captures — landed at 374 lines including its fake-driving half, which this ticket does
  not have. The design above is roughly 145 lines of code. The doc density this repo runs at is what
  puts the total near the ceiling, so the levers are already spent in the design: no models logic in
  the reader, one accumulated fatal instead of five branches, no slug port, no discovery sweep, no
  name-lock test. Do not reintroduce any of them.
