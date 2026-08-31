# initialize_control_redaction_test.go
- `initialize_control_redaction_test.go` (#1732) — **the redaction table for
  the `initialize` fixture family, proved over raw bytes with no record
  involved.** `newInitControlRedactor(operatorHome, tempHome, workdir,
  tempDir string) *dropcapRedactor` reuses `dropped_line_capture_test.go`'s
  `dropcapRedactor` mechanism (`add`, `redact`, `dropcapPathSpellings`,
  `dropcapSlug`, the four path class constants) unchanged and replaces only
  its constructor. `newDropcapRedactor` was not reusable as-is: it reads
  `realHome` and `os.TempDir()` on its own, so a redactor "constructed from
  synthetic values" still carried two machine-dependent rules, and it
  formats a `nonce int64` with `strconv.FormatInt`, which never returns
  `""` — the empty-value guard in `add` can't stop it, so every `int64`
  including `0` installs a rule that rewrites every `0` byte. The new
  constructor takes every path value as a parameter and takes no nonce, no
  session id, no FIFO path, which makes both failures impossible rather
  than guarded against, and reads nothing ambient — a rule exists if and
  only if a caller handed a value for it. `strings.TrimSuffix(v, "/")` is
  the one normalisation applied to each parameter (`os.TempDir()` returns a
  trailing `/` on macOS); the trim lives in the constructor rather than at
  the caller so #1733's call site can't forget it. Also ships
  `initControlDivergentDir(t) (handed, resolved string)`, a real directory
  under `t.TempDir()` reached through a symlink the test also creates —
  needed because a path that does not exist has no `filepath.EvalSymlinks`
  form (`lstat: no such file or directory`), so a row built only from
  invented paths can't exercise the resolved-spelling rule at all, and
  `t.TempDir()` alone diverges from its resolved form on macOS but not on
  Linux, so a row resting on that alone is green-and-vacuous on Linux.
  Ordering is longest-value-first (inherited from `dropcapRedactor`'s own
  sort): re-verified 2026-08-24 that this is load-bearing only when the
  handed values nest (workdir under temp home under system temp), because
  that's what makes one class's slug spelling a substring of the next —
  over unrelated values both orderings agree and the row proves nothing.
  This ticket ships the table only; #1733 (below) applies it to the capture
  record at the fill site, and #1729 (open) is the fail-closed deny-scan
  behind it — redaction and scanning are deliberately different fabric (see
  the #1260 entry above).

  **Two lessons from review:**
  - **A test helper that reimplements the construction's own logic to
    compute its expected values looks like duplication, but the duplication
    is what makes the row non-vacuous.** `initControlExpectedRules` calls
    the same `dropcapPathSpellings` and writes the same `TrimSuffix` as
    `newInitControlRedactor`. Collapsing that into one shared helper both
    functions call would make the trailing-slash-trim mutant (drop the trim
    in the constructor) green, because the expectation and the table under
    test would then always agree on whether the trim happened. The
    independence — two call sites, not one — is the thing actually under
    test.
  - **A mutant that reddens more rows than the design predicted isn't
    automatically a problem; check whether the rows still isolate different
    defects.** The nonce-parameter mutant was expected to redden only the
    unrelated-bytes row; it also reddens both armed-values rows, because an
    extra `prompt_nonce` triple is exactly "a rule the caller's values can't
    explain" from that row's own perspective. Code review separately found
    a sixth mutant the written spec's table hadn't listed (fall back to
    `realHome`/`os.TempDir()` only when the parameter is absent, rather than
    unconditionally) — the spec's own prose already named it, the mutant
    table just hadn't been built from that sentence. Each of the file's five
    rows still has at least one mutant for which it's the sole red.

  Registered in `finOfflineExecBans` with this family's standing eight names
  plus `realHome` and `os.TempDir` — the two ambient reads the parameters
  replace, and precisely the two the AST-identifier ban can catch that a
  values-only assertion also catches through any helper indirection; neither
  check substitutes for the other. Zero production files touched. See
  `docs/specs/architecture/1732-initialize-capture-redaction-table.md` for
  the full design, the mutant table and the security review.

- `initialize_control_redaction_test.go` (#1733) — **applies #1732's table to
  every field of `initControlFixtureRecord`, at the fill site.**
  `redactInitControlRecord(red *dropcapRedactor, rec *initControlFixtureRecord)
  []dropcapSubstitution` visits every string-bearing field of the record by
  name — no reflection, no whole-record marshal/substitute/unmarshal (that
  round trip strips insignificant whitespace and HTML-escapes `<`/`>`/`&`
  inside every `json.RawMessage`, which a byte-identity row over path-free
  payloads catches) — and returns the classes that fired instead of storing
  them; `initControlFixtureRecord` gained no field from this ticket. (#1731,
  below, later adds one — `Redaction`, the census itself — assigned at the
  fill site from this pass's return value, and deliberately the one field
  this pass does not visit, since visiting it would mean reading a value
  that does not exist until the pass returns.) `runInitControlChild` now takes
  a `*dropcapRedactor` instead of three more path strings, built once at the
  live call site (`newInitControlRedactor(realHome, home, workdir,
  os.TempDir())`); `writeInitControlFixture` is unchanged. `control_request_sent`
  is a bare `json.RawMessage`, a different Go type from the `[]json.RawMessage`
  pair beside it, and is visited separately — the split
  `compactInitControlRawRows`'s doc comment already called "red on arrival".

  **Two lessons from review:**
  - **A "the pass runs before the log site" placement claim is really a
    claim about which *variable* the log site reads.** The first cut placed
    the pass correctly — ahead of both post-write `t.Logf` sites — but the
    `control_response` log loop ranged the pre-pass local `responses` rather
    than `record.ControlResponses`. `initControlRedactRaws` allocates a fresh
    slice and `redact` returns `bytes.ReplaceAll`'s result; neither writes
    through the input, so a pre-pass alias is untouched no matter where the
    pass sits. Correct placement plus a stale alias leaks exactly as a late
    pass does, and a comment asserting the mitigation reads identically in
    both worlds. When a log site is the mitigation's subject, name the field
    it reads, not just the pass's position.
  - **A field an AC names explicitly can still ship with no test that
    reddens if the code stops visiting it.** AC1 names `control_responses`
    by hand as one of the payloads the pass must reach, but the fixture
    gives it exactly one entry and that entry is path-free — deleting
    `rec.ControlResponses = initControlRedactRaws(...)` from the pass and
    running the whole offline suite stayed green (confirmed by mutation,
    not assumed). The pass itself is correct; the row proving it for this
    field is not. Shipped anyway as a known gap (two SHOULD-FIX findings,
    under the fail threshold) — worth closing before #1729 arms the
    fail-closed scan on top of this, since that ticket will be reasoning
    about which fields the redaction already guarantees.

  One more standing gap from the same review, for whoever next edits this
  file: `initControlRedactRaws`'s doc comment says the nil→`[]` consequence
  applies "only for `models_entry_fields` and `stdin_write_errors`" — both
  `[]string` fields that never go through this helper. `control_responses`
  *does* go through it and is nilable on a live run
  (`snapshotControlResponses` returns `append([]json.RawMessage(nil),
  ...)`), so a no-response capture now commits `"control_responses": []`
  where it used to commit `null`. The behaviour is accepted (nothing decodes
  the committed bytes, and `[]` reads better in an artifact a human opens);
  the comment's field list is simply wrong and still says so as of this
  writing.

  Zero production files touched. See
  `docs/specs/architecture/1733-initialize-capture-record-redaction-pass.md`
  for the full design, the nine-mutant table and the security review.

- `initialize_control_writer_test.go` (#1731) — **the new field lands on the
  record (described above), but its one new test lives here, not on the
  record file or the redaction file, because it is the only one of the
  three that writes and reads back.** `TestInitControlFixture_DistinguishesAnEmptyCensusFromAnAbsentOne`
  writes two otherwise-identical records — one with `Redaction` set to the
  post-pass census, one with it forced back to `nil` — through
  `writeInitControlFixture` into separate `t.TempDir()`s (both records mint
  the same filename via `initControlArmFixtureName`, so one directory would
  make the second write clobber the first) and asserts the two files'
  bytes differ. Going through the writer rather than a bare `json.Marshal`
  is deliberate: it is the one instrument that catches both an `omitempty`
  tag on the new field *and* a nil-normalising helper inside
  `writeInitControlFixture`, since both would produce identical files this
  test can compare, and a bare marshal-and-compare only catches the first.
  The fill site (`runInitControlChild` in `initialize_control_probe_test.go`)
  now assigns `record.Redaction = redactInitControlRecord(red, record)`
  rather than discarding the pass's return — the sibling family's
  `dropcapWriteRecord` assigns its identically-shaped field *inside the
  writer*, and is the counter-example here, not the model:
  `writeInitControlFixture` takes an `out := *rec` copy and must go on
  receiving a record it only copies.

  **Lessons from review:**
  - **A vacuity precheck can need two separate failure messages, not one,
    because the two ways it can fail have different causes.** A `nil`
    census after the pass means `dropcapRedactor.substitutions` stopped
    returning a non-nil empty slice (the whole distinction this test exists
    to protect is gone, everywhere); a *non-empty* census means
    `initControlFullRecord` picked up a real path value, so the row is
    silently comparing a populated census against `nil` and proving nothing
    about the empty-census case it's named for. One combined message
    diagnoses neither failure.
  - **An inherited "that is why" clause needs to be re-derived against the
    fixture's current value, not carried forward verbatim.** Code review
    caught two: a literal-choice note claiming a realistic path in
    `Redaction.Replacement` would redden
    `TestInitControlRedactRecord_LeavesAPathFreeRecordByteIdentical` (false —
    that row never visits `Redaction`, so the path survives untouched and the
    bytes still match; the actual guard is the doc-comment prose plus #1729's
    future deny-scan), and `redactInitControlRecord`'s own doc describing
    "before: zero value, after: populated" when `initControlFullRecord` now
    ships a non-zero census, inverting which marshal is empty. Both
    directives were right; only the justification attached to each had gone
    stale the moment the fixture value it described was no longer what it
    used to be.

  Zero production files touched. See
  `docs/specs/architecture/1731-record-redaction-census.md` for the full
  design and the security review.
