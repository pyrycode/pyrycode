# Spec — fail-on-additive-drift assertions in ptyrunner ↔ streamrunner byte-equivalence test (#506)

Extends `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go` (landed
under #482) with two explicit allowlist tables (`expectedStreamRunnerOnly`,
`expectedPtyRunnerOnly`) and a new assertion that fails when either runner
emits an event type or top-level result-trailer field outside the
cross-runner intersection plus the appropriate per-runner allowlist. The
mechanism catches **additive** drift on either side — the case the existing
`reflect.DeepEqual` shape comparison cannot detect because both shape sequences
remain equal so long as ptyrunner does not SHRINK relative to streamrunner.

The new assertion is symmetric: it independently checks
`streamSet − ptySet ⊆ expectedStreamRunnerOnly` and
`ptySet − streamSet ⊆ expectedPtyRunnerOnly`. Violations name the unknown
identifier AND the allowlist table to update, so a future contributor reading
the failure has a single edit point.

The sibling ticket #505 aligns the test's tolerance scope with the #503 audit
decisions; this ticket only ships the mechanism. Initial population of
`expectedStreamRunnerOnly` is the union of (a) #503's catalogue of
streamrunner-only event types and (b) the existing `envelopeShape`
doc-comment's enumeration of streamrunner-only result-trailer fields, with
two corrections noted under "Initial allowlist contents" below.

## Files to read first

- `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:84-119` —
  `envelopeShape` doc-comment + `extractShapes`. The new helpers live next to
  these; the doc-comment's 8-field enumeration is the source for the
  result-trailer field allowlist.
- `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:288-339` —
  `compareShapes`. The new assertion is called alongside this, after the
  `reflect.DeepEqual` check at line 328, from
  `TestPtyRunnerVsStreamRunner_StructuralEquivalence`.
- `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:416-447` —
  `byteEquivResultTrailer` + `decodeResultTrailer`. The new field-set helper
  reuses the "find the result line" loop shape but unmarshals to
  `map[string]json.RawMessage` instead of the two-field struct.
- `internal/agentrun/streamjson/emitter.go:297-333` — `initLine` + `trailer`
  + `trailerUsage` structs. The trailer's top-level fields are: `type`,
  `subtype`, `is_error`, `duration_ms`, `num_turns`, `result`, `stop_reason`,
  `session_id`, `total_cost_usd`, `usage`, `terminal_reason`. ptyrunner's
  intersection-side surface — use to verify which AC-cited "8 fields" are
  truly streamrunner-only at the top level versus actually emitted by
  ptyrunner.
- `internal/agentrun/jsonl/reader.go:160-169` — the jsonl `knownKinds`
  whitelist. ptyrunner re-emits `ev.Raw` verbatim regardless of `Kind`, so
  every line in claude's JSONL flows through to stdout; the event-type set
  on the wire is whatever claude wrote into the JSONL file for the run.
- `internal/agentrun/streamjson/emitter.go:171-202` — `Emit` verbatim
  re-emission contract. Confirms ptyrunner does not filter by event type;
  the event-type set is producer-determined.
- GitHub issue [#503](https://github.com/pyrycode/pyrycode/issues/503) — the
  byte-equivalence audit. The "Event types emitted" table and "Result envelope
  field divergence" section are the citation source for each allowlist
  entry. The audit report itself (path TBD when #503 closes) will be the
  durable reference; until then, link to the issue body.
- `docs/specs/architecture/482-ptyrunner-byte-equivalence-smoke.md:1-100` —
  the parent test's design rationale, including the "shape comparison asks the
  right question — *does the dispatcher see the same signal?*" framing. The
  new assertions extend this framing from "shape-equality" to "no-additive-
  drift on either side."
- `docs/knowledge/codebase/498.md` — #498's wiring of the leading `system/init`
  envelope. Explains why both runners now emit `(system, init)` as the first
  line; relevant because the event-type intersection's `system` membership is
  load-bearing for the AC's intersection-plus-allowlist semantics.

## Context

The existing `TestPtyRunnerVsStreamRunner_StructuralEquivalence`
(`ptyrunner_byte_equivalence_test.go:147-286`) asserts shape equivalence via
`reflect.DeepEqual(streamShapes, ptyShapes)` on `[]envelopeShape{Type,
Subtype}`. This catches:

- **ptyrunner SHRINKING relative to streamrunner** — e.g. ptyrunner stops
  emitting an event type that streamrunner still emits, breaking the
  dispatcher's parser expectations.
- **either runner DIVERGING on order** — a `(type, subtype)` pair appearing
  in different positions across the two streams.

It does NOT catch **additive drift**: ptyrunner (or streamrunner) emitting a
NEW event type or NEW result-trailer field that the other does not. Because
the shape sequence is compared as positions × `(type, subtype)` tuples, the
sequences could each grow a new element of the same `(type, subtype)` and
still compare equal. More importantly, the result-trailer field-set
divergence is invisible to shape comparison at all — both runners emit one
`(result, success|error_*)` envelope; the test does not currently inspect
the top-level keys.

The migration's [[Drop-In Contract]] is that ptyrunner's wire shape is a
strict subset of streamrunner's plus an explicit list of documented omissions.
The current generic justification ("strict subset by design", lines 84-97)
admits anything; the new assertions make the tolerance per-identifier and
fail-closed.

#506 ships the mechanism. #505 (sibling, blocked on #503) then tunes the
allowlist contents per the audit's per-field "must close" vs "document as
omitted" decisions. Both halves are independent — the mechanism is correct
regardless of how the audit lands.

## Design

### Allowlist tables

Two package-level vars in `ptyrunner_byte_equivalence_test.go`, alongside the
existing `ptyRunnerArgvFlags` table (lines 33-40). Use a small struct to
group the two facets (events, result-trailer fields) per runner so the AC's
table names (`expectedStreamRunnerOnly`, `expectedPtyRunnerOnly`) appear
literally in the source:

```go
type additiveDriftAllowlist struct {
    Events              map[string]struct{}
    ResultTrailerFields map[string]struct{}
}

var expectedStreamRunnerOnly = additiveDriftAllowlist{ ... }
var expectedPtyRunnerOnly    = additiveDriftAllowlist{ ... }
```

`map[string]struct{}` is the canonical Go set type. Each entry carries a
trailing line comment citing #503 (or the audit doc path once it lands).
Variable names match the AC literally so failure messages can reference them
by `grep`-able identifiers.

### Initial allowlist contents

Populate at design time per the architect's reading of #503's catalogue +
the trailer struct in `emitter.go:314-326`. Do NOT blindly transcribe the
existing `envelopeShape` doc-comment — it includes two entries that are not
actually streamrunner-only at the top-level. See "Note: corrections to the
existing doc-comment enumeration" below.

`expectedStreamRunnerOnly`:

- **Events** (per #503's "Event types emitted" table, column `streamrunner`,
  minus what's in column `ptyrunner`):
    - `"rate_limit_event"` — `// #503: API-level transient event, ptyrunner reads JSONL not API`
- **ResultTrailerFields** (top-level keys streamrunner emits that ptyrunner's
  `trailer` struct in `emitter.go:314-326` does not):
    - `"api_error_status"`   — `// #503`
    - `"duration_api_ms"`    — `// #503`
    - `"fast_mode_state"`    — `// #503`
    - `"modelUsage"`         — `// #503`
    - `"permission_denials"` — `// #503`
    - `"ttft_ms"`            — `// #503`
    - `"uuid"`               — `// #503: claude's own session UUID, distinct from session_id`

`expectedPtyRunnerOnly`:

- **Events**: empty map literal (`map[string]struct{}{}`) — the AC mandates
  this. Sibling #505 will populate per the audit decisions if any
  ptyrunner-emitted event types are decided to be tolerated rather than
  closed.
- **ResultTrailerFields**: empty map literal — same rationale.

Empty maps not nil maps: the assertion's subset check uses `_, ok := m[x]`
which works on a nil map but a `nil` literal would be a confusing static
signal. Empty-but-initialised is the explicit "intentionally empty" shape.

### Note: corrections to the existing doc-comment enumeration

The `envelopeShape` doc-comment at lines 84-97 lists 8 streamrunner-only
result-trailer fields, including `result` and `usage.server_tool_use`. Two
corrections:

1. **`result`** is also emitted by ptyrunner (as an empty string) — see
   `emitter.go:258` (`Result: ""` in the `trailer{}` literal) and `emitter.go:320`
   (`Result string \`json:"result"\``, no `omitempty`). It is in the
   intersection, NOT streamrunner-only. Omit from the allowlist; if a
   real-claude run surfaces a divergence on `result`, that's a real bug to
   investigate, not an allowlist gap.

2. **`usage.server_tool_use`** is a NESTED field inside the `usage` sub-object.
   The AC scopes to top-level field names ("top-level field name outside the
   cross-runner intersection"). Do not include in the allowlist; the
   top-level field `usage` is in the intersection (both runners emit it).
   Sub-object structural divergence is out of scope for this ticket; if
   #505's audit decides to assert on it, that's a separate helper.

Net count: 7 result-trailer entries, not the AC's stated "8" — and 1 event-type
entry, not the AC's stated "4". The AC numbers paraphrase #503's catalogue
slightly inaccurately; the truthful count per the cited sources is what's
above. Document this in the spec so the developer doesn't waste turns
trying to reconcile to "8" and "4" exactly.

### New helpers (test file, alongside `extractShapes`)

Three new helpers. Signatures + behavior summary only:

```go
// extractEventTypeSet returns the set of distinct top-level "type" values
// observed across all non-empty lines in stream. Empty type strings are
// included (signal that something emitted an envelope with no type field).
func extractEventTypeSet(stream []byte) (map[string]struct{}, error)
```

- One pass over `bytes.Split(stream, '\n')`.
- For each non-empty line, unmarshal to `struct{ Type string \`json:"type"\` }`.
- Surface line-number context in the wrapped error, mirroring `extractShapes`.

```go
// extractResultTrailerFields returns the set of top-level JSON keys on the
// FIRST type:"result" line in stream. Errors if no result line is found
// (mirrors decodeResultTrailer's contract but returns the error so the
// self-check sub-test can assert on it without a fake *testing.T).
func extractResultTrailerFields(stream []byte) (map[string]struct{}, error)
```

- One pass over `bytes.Split(stream, '\n')`.
- For each non-empty line: `json.Unmarshal(line, &struct{ Type string }{})` to
  filter for `Type == "result"`.
- On match: `json.Unmarshal(line, &map[string]json.RawMessage{})`; return the
  keys as the set.
- On loop exhaustion without a match: return an error wrapping a sentinel
  message; caller decides whether to `t.Errorf` or `t.Fatalf`.

```go
// additiveDriftViolations returns one violation message per drift between
// the two streams. Empty return = no drift. Messages are pre-formatted
// (caller passes each to t.Errorf as-is).
func additiveDriftViolations(streamRaw, ptyRaw []byte) []string
```

- Calls `extractEventTypeSet` + `extractResultTrailerFields` on both raw
  streams. Errors from those helpers become violation messages too (a
  malformed stream is itself a drift signal worth surfacing).
- Computes `streamEvents − ptyEvents`; for each element not in
  `expectedStreamRunnerOnly.Events`, appends a violation message.
- Computes `ptyEvents − streamEvents`; for each element not in
  `expectedPtyRunnerOnly.Events`, appends a violation message.
- Same for `ResultTrailerFields` against the two allowlists.
- Failure-message shape (variable substitution shown; exact wording is
  developer's call but MUST name the unknown identifier AND the allowlist
  table that needs updating):

  > `"streamrunner emitted event type %q which is not in the cross-runner intersection and not in expectedStreamRunnerOnly.Events; either add it to that table with a citation comment (#503 or audit doc), or treat the divergence as a real bug"`

  > `"ptyrunner result-trailer field %q is not in the cross-runner intersection and not in expectedPtyRunnerOnly.ResultTrailerFields; either add it to that table with a citation comment, or file a follow-up ticket to align ptyrunner's emitter"`

**Why return `[]string` instead of taking `*testing.T`.** The caller-decides
shape makes the helper unit-testable by the self-check sub-tests below —
they can feed hand-crafted byte fixtures and assert on the returned slice
without needing a fake testing.T. The real test (`TestPtyRunnerVsStream...`)
loops over the returned slice with `t.Error(v)` so all violations surface in
one test run.

### Wiring into the existing test

Insert one block at `ptyrunner_byte_equivalence_test.go:328` (right after
the existing `reflect.DeepEqual` block, before the field-level invariants at
line 271+):

```go
for _, v := range additiveDriftViolations(streamOut.Bytes(), ptyOut.Bytes()) {
    t.Error(v)
}
```

`t.Error` (not `t.Fatal`) so the existing field-level invariants 7-10 still
run on a drift failure; the test surfaces all signals in one run.

### Self-check sub-tests (regression-test-the-test)

New top-level test function `TestAdditiveDriftAssertion_SelfCheck` in the
same file. Runs under `e2e_realclaude` build tag (the whole package is gated
by that tag — `fixtures.go` has it too), but does NOT require the real claude
binary or any network: it feeds hand-crafted byte fixtures into
`additiveDriftViolations` and asserts on the returned slice.

Build a small fixture builder once: a Go string constant for the
"intersection baseline" — a minimal byte stream that both runners would
plausibly emit for the test's minimal prompt:

```
{"type":"system","subtype":"init",...}
{"type":"user",...}
{"type":"assistant",...}
{"type":"result","is_error":false,"num_turns":1,"duration_ms":100,"usage":{...},...}
```

Each sub-test mutates the fixture to inject one synthetic drift, then asserts:
- `additiveDriftViolations` returns exactly one violation.
- The violation message contains the synthetic identifier name.
- The violation message contains the name of the table that needs updating.

Sub-test scenarios (each as `t.Run("<name>", func(t *testing.T) {...})`):

- `streamrunner_extra_event_type` — Inject `{"type":"synthetic_extra_event",...}`
  into the streamrunner stream only. Expected violation: names
  `synthetic_extra_event` AND `expectedStreamRunnerOnly.Events`.
- `streamrunner_extra_result_field` — Inject `"synthetic_extra_field":"x"`
  into the streamrunner result line only. Expected violation: names
  `synthetic_extra_field` AND `expectedStreamRunnerOnly.ResultTrailerFields`.
- `ptyrunner_extra_event_type` — Mirror of #1 on the ptyrunner side.
  Expected: names `expectedPtyRunnerOnly.Events`.
- `ptyrunner_extra_result_field` — Mirror of #2 on the ptyrunner side.
  Expected: names `expectedPtyRunnerOnly.ResultTrailerFields`.
- `intersection_match_no_drift` — Both streams identical baseline. Expected:
  zero violations. (Sanity check: the assertion does not false-positive on
  agreement.)
- `allowlisted_streamrunner_field_does_not_fire` — streamrunner emits
  `"api_error_status"` (an allowlisted entry); ptyrunner does not. Expected:
  zero violations. (Sanity check: allowlist actually allowlists.)

Six sub-tests; together they cover the cross-product of (event-type, result-field) × (streamrunner-side, ptyrunner-side) × (drift, no-drift).

## Concurrency model

None. All new code is synchronous, single-goroutine, and runs after both
runner invocations have completed. The two raw stream buffers are immutable
by the time `additiveDriftViolations` reads them.

## Error handling

- `extractEventTypeSet` / `extractResultTrailerFields` return wrapped errors
  on JSON-unmarshal failure or missing-result-line. The wrapped error wording
  follows the existing `extractShapes` pattern (`fmt.Errorf("extractX: line
  %d: %w (raw: %s)", ...)`).
- `additiveDriftViolations` converts those errors into violation strings (one
  per error) — a malformed stream is itself a drift signal worth surfacing
  in the same channel as a missing-allowlist-entry.
- Empty input to either helper returns an empty set (event-type case) or an
  error (result-trailer case, since "no result line" is a contract violation).

## Testing strategy

- The self-check sub-tests above are the unit-level regression for the
  assertion logic itself. They deterministically pass/fail without any
  external dependency.
- The real-claude path is the existing `TestPtyRunnerVsStreamRunner_Structural
  Equivalence`. The new assertion runs there as a side-effect of being wired
  into the test body at line ~328. The test's wall-clock budget and skip-gate
  semantics are unchanged — the new helpers are pure-Go in-memory work.
- `staticcheck` + `go vet` + `gofmt`: same package conventions apply. The
  allowlist tables follow `ptyRunnerArgvFlags`'s style (package-level var,
  trailing line comments per entry).

## Open questions

1. **Should `expectedStreamRunnerOnly.Events` also include `"assistant"`?**
   #503's table shows ptyrunner's `assistant` events under the literal type
   name `"message"`. If the real-claude run for this test's minimal prompt
   (`--max-turns=1`, `"Reply OK"`) produces `type:"assistant"` on the
   streamrunner side and `type:"message"` on the ptyrunner side, then yes —
   both would need to be allowlisted (`"assistant"` on stream-only,
   `"message"` on pty-only). However, the existing `reflect.DeepEqual` shape
   comparison currently passes on main, which means both runners emit
   matching `(type, subtype)` sequences for this prompt. The two reads
   conflict; resolution is empirical.

   **Recommendation:** populate only `"rate_limit_event"` initially. Run the
   test; if the new assertion fires on `"assistant"` / `"message"`, add both
   entries with citations. Document the discrepancy between #503's catalogue
   and the minimal-prompt runtime in the entry comment.

   (Same shape as #498's spec-level open-question handling per
   `docs/knowledge/codebase/498.md` § Lessons learned: recommend a shape,
   flag the open question, let the implementation confirm.)

2. **Should the self-check fixtures use the package's existing fixture
   helpers, or hand-rolled string constants?**
   The realclaude package's fixture helpers (`RunPyryAgentRun`,
   `WithWorktreeAuthenticated`) all wrap real-claude invocations and are not
   reusable for byte-level fixture construction. Hand-rolled multi-line Go
   string constants (with `\n` joiners) are the simplest shape.

   **Recommendation:** hand-rolled. One ~20-line `const intersectionBaseline = ...`
   block plus per-test mutations via `strings.Replace` or template
   substitution. Keep the fixture readable as JSONL-style text.

3. **Should `additiveDriftViolations` deduplicate the same identifier
   appearing multiple times across both directions?**
   Unlikely in practice (an identifier is either stream-only or pty-only,
   not both). But defensively: a malformed stream could end up surfacing
   the same field name twice from two different code paths.

   **Recommendation:** no deduplication. Each violation message names a
   distinct concern (which table to update); duplicates are fine and the
   developer reading the failure can dedupe mentally. Adding dedup logic
   adds complexity for a hypothetical case.

## Constraints

- Test-only change. No production-code (`internal/agentrun/**`) edits.
- Build tag `e2e_realclaude` covers the entire test file; the self-check
  sub-tests inherit that gate. Acceptable per the spec for #482 (the parent
  test) — the file is already in the e2e-realclaude package.
- Allowlist tables MUST be named `expectedStreamRunnerOnly` and
  `expectedPtyRunnerOnly` (literal AC requirement, also load-bearing for the
  failure-message grep-ability promise).
- Each populated entry MUST carry a trailing line comment citing #503 (or
  the audit doc once it lands). Empty maps (`expectedPtyRunnerOnly`) are
  vacuous for this requirement.
- Failure messages MUST name (a) the unknown identifier and (b) the
  allowlist table that needs updating, so a contributor reading the failure
  has the single-edit-point hint.

## Acceptance Criteria mapping

- AC 1 (two allowlist tables, named, naming visible in failure messages) —
  § "Allowlist tables", § "additiveDriftViolations" failure-message shapes.
- AC 2 (test fails on unknown event type, message names type + table) —
  § "additiveDriftViolations".
- AC 3 (test fails on unknown result-trailer field, message names field +
  table) — § "additiveDriftViolations".
- AC 4 (self-check sub-test with hand-crafted fixtures, both directions) —
  § "Self-check sub-tests".
- AC 5 (each populated entry has trailing comment citing #503) —
  § "Initial allowlist contents" + § "Constraints".
