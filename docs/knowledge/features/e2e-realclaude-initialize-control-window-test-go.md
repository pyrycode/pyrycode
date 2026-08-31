# initialize_control_window_test.go
- `initialize_control_window_test.go` (#1762) — **the reader for the
  send-point window #1723 defined and left unpopulated: a pure, total
  function over `[]json.RawMessage` and one anchor, returning the window's
  `system`/`init` count and one `initControlResultTrailer` per `result`
  line, in arrival order.** `initControlReadWindow` decodes each line in
  `lines[anchor:]` into `map[string]json.RawMessage` through
  `initControlWindowField`, which looks a key up and, only if present, runs
  `json.Unmarshal` into the destination and reports presence independently
  of whether that decode succeeded — the mechanism #1723's security review
  asked this ticket to build: `TotalCostUSDPresent` reads the raw key,
  never `TotalCostUSD != 0`, so a `result` line carrying a string cost
  still reads present with a zero value instead of being indistinguishable
  from a line with no cost key at all. A line that fails to decode as a
  JSON object at all (the shape `setModeRecorder.add` stores for non-JSON
  child output) is skipped, not aborted — one bad line costs its own entry,
  never the lines after it. The `system`/`init` count is keyed on both
  `type` and `subtype`; the committed
  `testdata/initialize_control_v2.1.239.json` carries six `system` lines
  that are not `init`, so a `type`-only count reads 7 over a fixture where
  1 is right. The empty window returns a non-nil, empty
  `[]initControlResultTrailer{}` rather than `nil` — the only way a
  committed `[]` can be told apart from a field that was never filled.

  The live fill site (`runInitControlChild`, in
  `initialize_control_probe_test.go`) reads the anchor as
  `len(rec.snapshotLines())` immediately before the control line is
  written, then computes both window reads from that one anchor after the
  post-join snapshot — one anchor, so the two reads cannot disagree about
  which window they measured, and nothing claude wrote in response to the
  request can land before it. The rejected alternative — the `system`/`init`
  count as a difference of two `snapshotInitModes()` calls taken before and
  after the send point — is a real race: the two calls take the lock
  separately, so a line landing in the gap between them is attributed to
  the wrong side of the anchor and the window under-reports by one.
  Registered in `finOfflineExecBans` with the same seventeen names
  `initialize_control_record_test.go` carries — this file performs no I/O
  in either direction, and the ban entry is what makes that claim
  mechanical rather than reviewed.

  **Two lessons that outlive this ticket, both about what a mutation table
  actually proves in this family:**
  - **Two rows that look like they test the same edge can be non-redundant
    for a reason invisible without running the mutant.** The offline table
    has one row at `anchor == 0` and one at `anchor == len(lines)`, sharing
    one line list. An "ignore the anchor" mutant (`lines[0:]` regardless of
    `anchor`) reddens **only** the `len(lines)` row — at `anchor == 0` the
    correct and mutated readers produce the same answer, so that row alone
    cannot catch it. The `anchor == 0` row's job is the opposite one: it
    proves the `len(lines)` row's empty result came from the anchor and not
    from a line list that had nothing to find in it. Neither row is
    redundant, but the table has to state which mutant each one is *sole
    red* for, not just assert both — code review caught one such claim that
    was correct as a purpose statement but not literally sole (a `subtype`
    check widened to `true` reddens both the subtype row and the
    `anchor == 0` row, since that row's shared list carries a non-`init`
    `system` line by construction).
  - **`reflect.DeepEqual` treats `nil` and `[]T{}` as different, and that is
    the *only* instrument for a non-nil-empty contract — a `want` literal
    that omits the slice field defeats it silently.** A `want` written as
    `initControlWindow{systemInitCount: 1}` carries a **nil**
    `resultTrailers` and passes equally for a reader that returns `nil` on
    an empty window and one that returns `[]initControlResultTrailer{}`.
    Both empty-window rows have to spell the field out explicitly
    (`resultTrailers: []initControlResultTrailer{}`) for the comparison to
    mean anything; the shorter, more natural-looking literal is the one
    that silently inverts the AC.

  Zero production files touched. See
  `docs/specs/architecture/1762-send-point-window-reads.md` for the full
  design, per-mutant table and security review.

- `initialize_control_writer_test.go` (#1702, extended #1700 and #1722) —
  **the write half of the `initialize` fixture family: the
  directory-injectable, atomic writer for #1701's record, and the offline
  round trip proving no field is dropped on the way.** `writeInitControlFixture`
  mirrors #1662's `writePoolRevokeFixture` step for step — `dir` parameter,
  `os.MkdirAll`, name minted from `out.ClaudeVersion` and `out.Arm` (never
  `ClaudeVersionRaw`, and never self-formatted) through #1712's
  `initControlArmFixtureName` — both columns passed through unmodified, so
  the writer itself formats no filename and slugs nothing — `MarshalIndent`,
  `.tmp` write, `os.Rename` — and caps `stderr_capture` on a shallow copy
  (`out := *rec`), whose doc comment states the caveat explicitly: the copy
  shares every slice header with the caller, so it is safe only because the
  sole capped field is a `string`; a future writer that caps a slice-valued
  field would be mutating the caller's backing array through a copy that
  looks defensive. `compactInitControlRawRows` normalises the record's three
  raw-JSON-bearing rows (`control_request_sent`, `control_responses`,
  `stdout_events`) by **Go type**, not by name — a `json.RawMessage` case and
  a `[]json.RawMessage` case — so a fourth raw-JSON field added later to
  #1701's record is picked up automatically; the round trip pins that
  type-scoped selection separately, asserting the touched-name set equals
  exactly those three, so a normaliser silently widened or narrowed still
  reddens even though the type switch itself never needs editing. Registered
  in `finOfflineExecBans` with the same twelve names as #1696 and #1701 carry
  minus their five I/O names (`filepath.Glob`, `os.ReadFile`, `os.WriteFile`,
  `os.Create`, `os.ReadDir`) — this file, unlike its two siblings, performs
  real directory I/O (a write, a read-back, a directory listing), so those
  five stay available rather than banned; the relative-path hazard that
  leaves closed for the siblings is closed here instead by the exactly-one-
  entry assertion, which goes to zero entries if a write escapes to the real
  `testdata/`.

  **Two lessons that outlive this ticket:**
  - **The zero-entry arm of an exactly-one-entry assertion doesn't require
    writing into the committed `testdata/`.** The spec's prescribed mutant
    for "writer ignores `dir`" was to join a relative `testdata/` path,
    which lands a real file in the repo and needs a manual delete plus a
    `git status` check to verify cleanly afterward. Redirecting the writer
    to a *second* `t.TempDir()` instead hits the identical
    `len(names) != 1` branch and leaves the worktree untouched — worth
    reaching for whenever a mutant's only hazard is where its bytes land,
    not what they are.
  - **`%v` over a `json.RawMessage` row prints a decimal byte dump, not
    JSON.** `json.RawMessage` implements `MarshalJSON` but not `String`, so
    `%v` in the round trip's mismatch message renders a ~300-byte envelope
    as `[123 34 116 ...]`. Kept for consistency with #1662's sibling, and
    the row *name* still carries the diagnosis so the test isn't weakened —
    but a future file in this family that wants a readable raw-JSON diff has
    to type-switch at the print site; the row's `any` type can't use `%s`
    without mangling the int and bool rows alongside it.

  Code review flagged, non-blocking: the touched-set guard's failure message
  names two causes (normaliser widened, normaliser narrowed) but not the
  third the type-switch design itself predicts — #1701 grows a fourth
  raw-JSON field, the switch picks it up correctly and automatically, and
  the hardcoded three-name `wantTouched` reddens against an honest writer
  and an honest normaliser. The fix is one more clause in the message, not a
  design change, and was not applied in this ticket.

  #1722 re-pointed `TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry`'s
  `wantName` onto `initControlArmFixtureName(rec.ClaudeVersion, rec.Arm)` and
  added `TestInitControlFixture_RoundTripsAnUnansweredWaitBesideCapturedBytes`
  alongside it — the incoherent-pair test described in the record entry
  above (bytes captured, wait not satisfied), placed in this file because it
  is the only one in the family whose `finOfflineExecBans` entry permits
  `os.WriteFile`/`os.ReadFile`. Its vacuity control (`t.Fatalf` if the
  fixture's `ControlResponses` is empty) follows this file's own
  `TestInitControlFixture_WriterCapsStderrCapture` precedent below rather
  than a skip, on the same reasoning: a row that cannot discriminate is a
  broken instrument, not a passing test.

  Zero production files touched. See
  `docs/specs/architecture/1702-initialize-capture-fixture-writer-and-round-trip.md`
  for the full design and the twelve-mutant table, and
  `docs/specs/architecture/1722-arm-named-initialize-capture-record.md` for
  the arm-namer migration and the incoherent-pair test. This closed the
  `initialize` fixture family's complete/atomic half; bounded closed in #1700,
  below, which adds a fourth test to this same file and updates the file's own
  COMPLETE/ATOMIC/BOUNDED self-description and in-file pointers accordingly.

- **`initialize_control_writer_test.go` (#1700 — bounded, same file as
  above)** — `TestInitControlFixture_WriterCapsStderrCapture` proves the
  `capFixtureCapture` call on the bytes the writer actually leaves **on
  disk**: an over-cap ASCII row asserted exactly at `stderrFixtureCap`, and an
  over-cap multi-byte row (its byte at the cap is a UTF-8 continuation byte,
  enforced by a `t.Fatalf` vacuity control on the literal itself) asserted as
  a range plus a prefix check. Each row also confirms the caller's record
  came back unmutated. Mutation-verified via `go test -overlay` (no worktree
  writes): the cap-omitted, direct-`truncateString`, over-trim, and
  caps-the-caller's-record mutants each have exactly one sole-red instrument
  among the two rows and the no-mutation check.

  **Two lessons that outlive this ticket:**
  - **A "sole red" claim from the spec is still worth re-measuring, not
    trusting.** Re-running the mutant matrix surfaced that the multi-byte
    row's *prefix* clause reddens alongside its *range* clause on the
    direct-`truncateString` mutant — the substituted U+FFFD is not in the
    original capture, so both clauses fire together. The row carries two
    independent discriminators, not one; a future simplification to a single
    length check would silently drop one of them.
  - **A hand-rolled `perl -pe 'script' -0777 file` mutation one-liner can
    silently no-op.** Perl only consumes flags that appear *before* the
    script argument, so `-0777` placed after it is read as a filename
    instead, and the "mutated" file comes back byte-identical to its source
    — a false negative indistinguishable from a dead assertion once the test
    still passes. `diff -q` each generated mutant against its source and fail
    loudly on a match; that check is what caught it here.

  Zero production files touched. See
  `docs/specs/architecture/1700-initialize-fixture-writer-cap.md` for the
  full design and the five-row mutant matrix.
