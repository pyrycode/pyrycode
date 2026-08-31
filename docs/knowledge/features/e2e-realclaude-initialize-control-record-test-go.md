# initialize_control_record_test.go
- `initialize_control_record_test.go` (#1701, extended #1722, #1723, #1731) —
  **the record half of the `initialize` fixture family: fixes the JSON
  contract #1688's live capture, #1690's decoder and #1692's fake all read,
  and pins the fixture standing in for it against the two ways it could
  degenerate silently.** `initControlFixtureRecord` carries 29 fields (no
  `env` field; nineteen of them carry `setModeFixtureRecord`'s
  JSON tags and Go types unchanged). #1722 added the two fields past
  eighteen: `Arm` — the send point the run *intended*, not a claim that the
  turn completed; `turn_boundaries` is what tells a reader whether the turn
  actually did — copied from `setModeFixtureRecord.Arm` at the same tag and
  the same relative position, and `ControlResponseWithinWait`, a fact that
  is measured and not derivable. Unlike `ControlResponseRequestIDMatched`,
  which is a function of two verbatim fields recorded beside it, this one is
  a function of a bounded wait that has already expired by the time the
  record is built, and no other field — not even the verbatim response
  bytes — can reconstruct it, because a `control_response` carries no
  arrival time relative to the budget the harness chose. #1723 added a third
  group past those two: `SendPointIndex`, one positional anchor into
  `stdout_events` (same index units `turn_boundaries` uses — `0` and
  `len(stdout_events)` are both legitimate readings, not sentinels, and
  carry no presence flag), plus two reads scoped to the window
  `stdout_events[anchor:]` it defines — a `system`/`init` line count, and
  `AfterSendPointResultTrailers`, one `initControlResultTrailer` per
  `result` line in that window pairing its turn count with its cost and a
  `TotalCostUSDPresent` flag populated from the trailer's raw bytes, never
  derived from the decoded value. The nested type is invisible to
  `reflect.TypeOf(initControlFixtureRecord{}).NumField()` and to the
  record's same-typed-distinctness property, so it carries its own
  hand-written listing (`initControlTrailerFields`) and its own three
  subtests rather than riding the record's existing ones. Nothing computes
  these fields directly: #1762 fills all three at the fill site
  (`runInitControlChild`) from `initControlReadWindow`, a pure reader over
  the recorded lines described in `initialize_control_window_test.go`
  below. The committed `initialize_control_v2.1.239.json` predates the
  fields entirely and carries none of the three keys, which is why an
  artifact from before that ticket is still not readable as a
  `before_first_turn` capture — it is simply missing the fields, not
  zero-valued in them. #1731 added a fourth, one-field group:
  `Redaction []dropcapSubstitution` (tag `redaction`, no `omitempty`) — the
  census of which redaction classes fired and how often, populated at the
  fill site (`runInitControlChild`) from what #1733's
  `redactInitControlRecord` returns, never inside the pass and never inside
  `writeInitControlFixture`. `[]` versus `null` is deliberate and is the
  entire distinction: a non-nil empty census means the redactor ran and
  found nothing, and `null` means it never ran — a reader of a committed
  fixture could otherwise not tell those apart. That census is an audit
  trail over the redactor, not a clean bill of health over the artifact: an
  empty or wrongly-armed class in `newInitControlRedactor`'s table would
  match nothing and still report `[]` while the file kept the real path
  under an unarmed class. #1729's deny-scan is the fail-closed net that
  makes the clean-artifact claim; this field does not. The record's standing
  instruction — every string-bearing field added here must be visited by
  `redactInitControlRecord` — now names `Redaction` as its one exception,
  since its strings are the redactor's own vocabulary (the four
  `dropcapClass*` identifiers and the `$`-prefixed placeholders
  `newInitControlRedactor` mints), never child output or an environment
  value, and since the field is assigned *from* the pass's return, so
  visiting it would be circular. #1747 added a fifth, one-field group past
  those four: `CredentialScanApplied map[string]bool` (tag
  `credential_scan_applied`, no `omitempty`) — `dropcapRecord`'s own field
  name and Go type reused rather than re-derived, per the shared-field rule
  above. It is the other half of `Redaction`'s story: not which classes the
  redaction table rewrote, but which classes a `dropcapScanner`'s needles
  were **armed** for, populated at the fill site from the scanner the
  capture built — never computed here. **`dropcapScanner`'s two arming
  paths disagree about "absent."** `addDynamic` (the two credential
  classes) appends a needle unconditionally, so an unset
  `ANTHROPIC_API_KEY` still lands as a `false` key; `addDynamicPath` (the
  four path classes) calls `dropcapPathSpellings`, which returns `nil` for
  an empty path, so no needle is appended and `applied()` — which builds
  its map by iterating `s.needles` — carries **no key at all** for that
  class. The capture hands `newDropcapScanner` no artifact directory (and
  `operator_home`'s `realHome` can be empty too, whenever HOME is unset at
  launch), so `scanner.applied()` alone would have **omitted** those
  classes rather than recording them as armed-nothing — indistinguishable
  from a class that scanned and found nothing. `initControlScanApplied`
  closes the gap at the fill site by adding `false` for any class in its
  own hand-written `initControlScanPathClasses` list that `applied()`
  doesn't already carry, touching no existing key; `dropcapScanner` itself
  is unchanged, since it is shared with the `dropcapRecord` family whose
  shape is already committed. The field takes `Redaction`'s exception to
  the visit-every-string-field rule for the same reason: its keys are the
  scanner's own declared vocabulary (`dropcapClass*` identifiers, the deny-
  class names, the two credential env-var names), never a matched value —
  `applied` keys by `n.class`, never `n.value`. **Until #1748 lands, the
  classes are armed and nothing scans the written bytes with them** — same
  caveat `Redaction`'s doc already carries, for the same reason: a reader
  must not mistake a populated field for evidence the artifact was
  checked.
  `initControlFullRecord` returns a
  **fresh pointer per call** rather than a package-level `var`, so #1700's
  parallel subtests mutating the record are not a `-race` data race. The
  hand-written `initControlFixtureFields` listing is what #1702 zips against
  both sides of its round trip instead of restating the field set; its
  length is asserted against the struct's `reflect` field count, so a field
  added later with no row reddens instead of going silently unchecked.
  Registered in `finOfflineExecBans` with #1696's seventeen names copied
  whole — this file performs no I/O in either direction, unlike #1702's
  writer, which is why the two files need separate, differently-scoped
  entries rather than one shared one.

  **Lessons that outlive this ticket:**
  - **A duplicate-name row in a hand-written listing reddens more than the
    property it was written to test, so a mutant table has to be checked by
    failure *message*, not by which subtest went red.** Listing `argv` twice
    while dropping `prompts` was predicted to redden only the
    listing-covers-every-field subtest's uniqueness pass. It also puts two
    identical `[]string` rows into the same-typed-fields-distinct
    comparison, so that subtest fires as collateral — while the length check
    inside the first subtest stays green the whole time, still counting the
    field total. Two subtests firing where one was predicted is invisible if
    you only read red/green; it shows up only in what each failure message
    says caused it. Any future mutant table in this family that predicts
    "exactly one subtest reds" needs its message read, not just its count.
  - **A fixture literal that is deliberately not a real value needs its own
    slugging property pinned, or two unrelated writer mutants both go dead
    without the round trip noticing.** #1722's `initControlFullRecord` needed
    a synthetic `after_completed_turn-FIXTURE` arm for the same reason
    `claude_version` is `2.1.220-FIXTURE`: every declared arm in
    `initControlArms` is already inside `versionSlug`'s clean character
    class, so a "tidied" literal reading a real arm would let the round
    trip's name assertion pass against *both* a writer that interpolates the
    arm raw and one that hardcodes an arm entirely. Measured by running
    those two mutants against a clean literal: both went 0-red, and only a
    widened does-not-survive-slugging subtest caught the difference. A
    single-mutant matrix would have credited the round trip with coverage it
    did not have; the pairwise run is what showed one subtest was carrying
    the weight of two.
  - **A bool field's non-zero property can forbid the very pair the field
    exists to express, and the fix is a second record instance, not a
    weaker property.** `ControlResponseWithinWait` exists to state "bytes
    captured, wait not satisfied" — but non-zero for a bool means `true`, so
    the fully-populated fixture, which must carry every field non-zero,
    necessarily carries the *coherent* pair (`true` beside captured bytes)
    and cannot demonstrate the discriminating one. #1722 put that pair in a
    second record instance inside its own round-trip test instead, guarded
    by a vacuity check that the fixture's response bytes are non-empty
    before the write. The mutant this catches — the field derived from
    `len(ControlResponses) > 0` instead of the wait's own result — is
    invisible to the main round trip, which only ever sees the coherent
    pair.
  - **The write-half's touched-name canary selects by the row's Go type, not
    by field, so it cannot see raw JSON hidden inside a struct-slice row.**
    #1723's spec predicted that wrapping `TotalCostUSD` in a
    `json.RawMessage` "to be safe" would trip
    `TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry`'s
    `wantTouched` assertion — the same canary that catches it on the record's
    three top-level raw-JSON fields. Measured via `go test -overlay`: it
    doesn't. `compactInitControlRawRows` matches on the *row's* declared Go
    type (`json.RawMessage` / `[]json.RawMessage`), and the row here is
    `[]initControlResultTrailer`, so the touched set never changes; the round
    trip holds too, because `MarshalIndent` has no interior to indent inside
    a scalar. The canary only guards a fourth raw-JSON field added directly
    to the record — a raw-JSON field nested inside a struct-slice element is
    unenforced by any test in the family and has to stay a doc-comment-and-
    review rule (`initControlResultTrailer`'s own comment states this
    outright rather than citing a test that never runs over the value).
  - **A listing subtest that indexes a fixture's slice panics on an emptied
    list instead of reddening, which is the wrong failure mode when a sibling
    subtest's vacuity check exists to report exactly that mutant.** The
    "trailer listing covers every field" property is a claim about the
    *type*, not about any one entry, so it has to run over the zero value of
    `initControlResultTrailer{}` rather than over `entries[0]` — indexing the
    fixture's `AfterSendPointResultTrailers` would crash the whole test
    binary against an emptied list, stepping on the non-zero subtest's
    `t.Fatalf` vacuity control, which is where that mutant is meant to surface
    cleanly. Any future per-field-listing subtest added to this family should
    default to the zero value unless it specifically needs a populated entry.

  Zero production files touched. See
  `docs/specs/architecture/1701-initialize-capture-fixture-record.md` for
  the full design and the per-field distinctness-group table,
  `docs/specs/architecture/1722-arm-named-initialize-capture-record.md` for
  the arm and wait-result fields, and
  `docs/specs/architecture/1723-send-point-anchor-and-window-observables.md`
  for the anchor and window-observable fields. The write half —
  the writer and the round trip that reuses this file's record and listing —
  is `initialize_control_writer_test.go` (#1702), described next.
