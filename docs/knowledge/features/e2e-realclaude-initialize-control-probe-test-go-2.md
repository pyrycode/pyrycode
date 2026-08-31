# initialize_control_probe_test.go
- `initialize_control_probe_test.go` (#1747) — **builds a `dropcapScanner`
  beside the redactor at the capture site and wires it to the fifth field
  described above.** `newDropcapScanner(home, "", workdir)` — the middle
  parameter, `artifactDir`, is left empty deliberately: not by reusing
  `workdir` (which would arm `artifact_dir` under the wrong value and leave
  `workdir` itself unarmed) and not by minting a directory to fill the
  slot. The empty value **is** the fact the field exists to record.
  `CredentialScanApplied: initControlScanApplied(scanner)` is assigned in
  `runInitControlChild`'s record literal, beside the other populated
  fields — not inside `writeInitControlFixture`, whose `out := *rec` copy
  must go on receiving a record it only copies (`Redaction`'s precedent).

  **A scanner in scope is two live credentials, and the hazard is new to
  this file.** `newDropcapScanner` reads `CLAUDE_CODE_OAUTH_TOKEN` and
  `ANTHROPIC_API_KEY` into each `dropcapNeedle.value`, so `scanner` now
  sits beside a driver that logs on nearly every path.
  `initControlScrubbed` does not cover it — that guard reads the child's
  stderr, and a `%+v` on the scanner would be the harness's own output,
  not the child's. `runInitControlChild`'s doc now states the ban next to
  the redactor's own concurrency note, and names `applied()`'s map as the
  safe, useful thing to print instead: its keys are declared vocabulary
  (env-var names, `dropcapClass*` identifiers) and its values are bools,
  so no needle, path or credential can reach one. Any future parameter
  added to this driver that carries a `dropcapNeedle`-shaped value needs
  its own instruction written at its own call site — the ban does not
  propagate from one parameter to the next by association.

  Zero production files touched. See
  `docs/specs/architecture/1747-armed-credential-classes-on-the-initialize-capture.md`
  for the full design and security review.

- `initialize_control_writer_test.go` (#1747) — **one new test,
  `TestInitControlFixture_DistinguishesAnArmedNothingScanFromAnAbsentOne`,
  reusing #1731's `...DistinguishesAnEmptyCensusFromAnAbsentOne` shape**
  for the new field, with one change: the armed-nothing map under test is
  built from `dropcapScanner{}.applied()`, not a `map[string]bool{}`
  literal — `applied()`'s non-nil-empty return is the entire mechanism the
  `{}`-vs-`null` distinction rests on, and a literal would pin the test's
  own value rather than the production one.

  **A writer that filters armed-nothing entries out on the way to disk is
  invisible to the very test built to catch nil-normalisation.** Confirmed
  by mutation: a writer-side filter dropping every `false`-valued map
  entry leaves this new test green, because its `{}`-vs-`null` comparison
  never has an entry on either side for the filter to remove.
  What reddens instead is
  `TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry` — and only
  because `initControlFullRecord`'s fixture value carries one entry of
  each kind (`dropcapClassWorkdir: true`, `dropcapClassArtifactDir:
  false`), not just one entry of any kind. A record field whose *values*
  carry meaning, not only its presence, needs a fixture value of each kind
  it can hold, or a mutant that discriminates by value has no test whose
  sole purpose is to catch it.

  Zero production files touched. See
  `docs/specs/architecture/1747-armed-credential-classes-on-the-initialize-capture.md`
  for the full design and the mutant table.

- `initialize_control_writer_test.go` (#1748) — **the fail-closed net behind
  #1732/#1733's redaction table: a `dropcapScanner` deny-scan on the write
  path, refusing the record outright on a hit instead of rewriting it.**
  `scanInitControlFixture` copies the record, caps `StderrCapture` with
  `capFixtureCapture`, marshals with `json.MarshalIndent`, runs
  `scanner.scan` over the blob and returns exactly those bytes — making no
  filesystem call of any kind, so a hit's `t.Fatalf` strands nothing under
  the target directory, not even a `.tmp`. `writeInitControlFixture` takes
  the scanner as a parameter (never a `newDropcapScanner` call inside the
  file — `finOfflineExecBans` bans that name and `realHome` there for the
  same reason #1732's entry banned `os.Getenv`) and writes the returned
  slice byte for byte, with `os.MkdirAll` strictly after the step returns.
  A table only rewrites what it predicted; this is different fabric on
  purpose.

  **The cap has to sit inside the scanning step, not before or after it, and
  no bound assertion alone can tell the difference.** Length, prefix and
  UTF-8 checks all stay green whether the cap runs before the scan, inside
  it, or after it — moving the cap across the scan boundary changes only
  which bytes get scanned, and only a byte-identity comparison between the
  scanned bytes and the disk bytes can see that. That comparison is also
  vacuous everywhere `json.MarshalIndent` is a no-op transform (it's
  deterministic, so re-marshalling an under-cap record is byte-identical to
  writing the returned slice) — it only discriminates over a record the cap
  actually shortens, which is why the assertion rides inside
  `TestInitControlFixture_WriterCapsStderrCapture`, the one test with
  over-cap rows, rather than in a function of its own.

  **A refusal that fatals has no seam to assert the refusal from.**
  `testing.TB` can't be implemented outside `testing`, so "nothing was
  written" and "no excerpt is printed" are properties of construction — the
  scan sits inside the sole producer of the write's bytes, which touches the
  filesystem not at all — not properties a test observes directly. Proving
  the scan itself fires against a hit has to happen one level down, over the
  marshalled record, the way `dropped_line_capture_test.go`'s
  `dropcapWriteRecord`/`TestDropcapRedactionAndDenyScan` already do it for
  the sibling family — `TestInitControlFixture_ScanRefusesAPlantedCredential`
  follows that shape here, planting a `/Users/`-prefixed value and asserting
  `dropcapContains(hits, dropcapDenyUsers)`, with a clean-record control row
  first so the plant assertion can't pass vacuously.

  Zero production files touched. See
  `docs/specs/architecture/1748-deny-scan-the-initialize-fixture-before-writing.md`
  for the full design, the mutant table and the security review. #1749
  (below) swept the remaining ten armed classes; this slice shipped only the
  one planted row needed to prove the mechanism live.

- `initialize_control_writer_test.go` (#1749) — **the per-class sweep #1748
  deferred:** one row per class `newDropcapScanner` arms (all eleven), each
  planting a value of that class into `initControlFullRecord().StderrCapture`
  and asserting the class is among `scanner.scan`'s hits. `scan` reads the
  whole marshalled blob, so one plant site per row is enough — unlike
  `redactInitControlRecord`, which visits fields by name and needs per-field
  rows. `newInitControlOfflineScanner` mirrors `newDropcapScanner`'s five
  arming calls with its two `os.Getenv` reads and its `realHome` read
  replaced by declared synthetic constants, so the table runs — not skips —
  with no claude and no credentials; code review re-ran it with
  `CLAUDE_CODE_OAUTH_TOKEN`/`ANTHROPIC_API_KEY` deliberately *set* to fake
  values and got the same 12/12/0-skips result, confirming
  environment-independence rather than mere credential-freeness. The scanner
  is built once and shared read-only across eleven parallel subtests. Two
  rows legitimately hit more than one class — `workdir`'s plant nests inside
  both the fixed `/home/` literal and its own `temp_home` value, and
  `private-var-folders-prefix`'s plant contains `var-folders-prefix` as a
  literal tail — so the table asserts containment, never equality.

  **A per-row presence check plus `len(applied) != len(rows)` does not by
  itself prove the row-class set equals the armed-class set.** Code review
  found the gap by measurement: swapping one row's class for a duplicate of
  another's leaves the count at eleven and every row's presence check green,
  while the class that lost its row goes uncaught. The fix — dedup `rows` by
  class, or diff it against `applied`'s key set — was filed as a
  non-blocking follow-up rather than shipped in #1749. Anyone editing this
  table's row list should close that gap first; until then, the size check
  only proves the *builder-arms-a-class-with-no-row* direction, not its
  converse.

  Zero production files touched. See
  `docs/specs/architecture/1749-per-class-capture-scan-coverage.md` for the
  full design and the mutant table.
