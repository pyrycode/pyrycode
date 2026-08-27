# #1702 — the `initialize` capture's directory-injectable writer and offline round trip

**Ticket:** [#1702](https://github.com/pyrycode/pyrycode/issues/1702) · `size:s` · `security-sensitive`
**Scope:** test-only. Two files, both `*_test.go`. Zero production files.

---

## Files to read first

Everything below is behind the `e2e_realclaude` build tag; `make check` never
compiles it. Symbols resolve with `codegraph_search` / `codegraph_node`.

| File | Symbol | What to extract |
| --- | --- | --- |
| `internal/e2e/realclaude/inband_bypass_revoke_fixture_test.go` | `writePoolRevokeFixture` | **The writer shape to mirror exactly**: caller-supplied `dir`, cap applied to a local copy, `MarshalIndent`, `.tmp` write, `os.Rename`, and failure messages naming one identifying field and nothing else. |
| ″ | `capFixtureCapture` | Call it. Already generic over the string, already bounded at `stderrFixtureCap`, already handles the mid-rune split. Do not rewrite. |
| ″ | `compactRawMessages` | Call it. Note its `t.Fatalf` text — it names `control_responses` explicitly, which matters for the mutation table below. |
| ″ | `TestPoolRevokeFixture_RoundTripsEveryFieldIntoOneNamedEntry` | The round-trip and exactly-one-entry shape. Its rows are **restated inline**; that is the one thing this ticket does differently (it zips #1701's listing instead). |
| ″ | `TestPoolRevokeFixture_WriterCapsChildOutputCapture` | Read only to see where the cap and no-mutation assertions live in the sibling — they are **not** this ticket's, they are #1700's. |
| `internal/e2e/realclaude/initialize_control_record_test.go` | `initControlFixtureRecord`, `initControlFullRecord`, `initControlFixtureField`, `initControlFixtureFields` | The record written, the fresh-pointer-per-call fixture, and the single listing this ticket applies to **both** sides. |
| ″ | `TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken` | Its final subtest ("the version token does not survive slugging") is what carries AC 2's redness. Read its failure message before touching any literal. |
| `internal/e2e/realclaude/initialize_control_names_test.go` | `initControlFixtureName` | The namer, its single-clean-path-component contract, and the explicit statement that the guarantee is **lexical** and says nothing about `dir`. |
| `internal/e2e/realclaude/permission_protocol_spike_test.go` | `versionSlug`, `stderrFixtureCap`, `truncateString` | The slug rewrite rule (`[^a-z0-9._-]+` → `_`, lowercased first, clamped at 32) and the cap constant. |
| ″ | `packageDir`, `writeFixture`, `captureClaudeVersion` | The three banned helpers whose names go in the ban entry — read what each actually does, since the entry's comment has to say why. |
| `internal/e2e/realclaude/offline_exec_ban_test.go` | `finOfflineExecBans`, `TestFinOfflineFilesReachNoExecHelper` | The map to extend and the AST check. Read the `inband_bypass_revoke_fixture_test.go` entry's comment (the "deliberately absent" half this entry copies) and the `initialize_control_record_test.go` entry's (the two additions it copies). |
| `docs/knowledge/features/e2e-realclaude.md` | § the `inband_bypass_revoke_fixture_test.go` (#1662) entry | The `MarshalIndent`-reflow lesson and the "a reused helper's own guard can make the new wrapper's guard unpinnable" lesson. |
| ″ | § the `initialize_control_record_test.go` (#1701) entry | **The lesson that binds this ticket's mutation testing:** check a mutant table by failure *message*, not by which subtest went red. Two subtests firing where one was predicted is invisible if you only read red/green. |

---

## Context

#1688 will spend live tokens capturing claude's `initialize` control response —
the model list the daemon wants to publish to clients. That artifact is worth
committing only if it is COMPLETE, BOUNDED and ATOMIC, and none of the three
needs a claude binary. #1701 settled the record and its fully-populated fixture.
This slice settles **complete** and **atomic**: the writer that puts those bytes
on disk, and the offline round trip proving no field is dropped on the way.
**Bounded** is #1700, which reads this writer.

No ADR is warranted — this adds no cross-cutting decision, it extends a
four-file fixture-family pattern (#1595 → #1661/#1662 → #1696 → #1701) that the
package overview already documents.

### What already exists and is not re-litigated

- The record, the fully-populated fixture and the field listing are #1701's.
  **This slice adds no field and mints no second fixture.**
- The name is #1696's `initControlFixtureName`. Its lock proves the minted name
  joins no committed family and is always a single clean path component — which
  is exactly what makes `filepath.Join(dir, name)` safe here.

---

## Design

### Package structure

One new file, `internal/e2e/realclaude/initialize_control_writer_test.go`, plus
one entry appended to `finOfflineExecBans` in `offline_exec_ban_test.go`.

The file name follows the family (`initialize_control_names_test.go`,
`initialize_control_record_test.go`). It is a **separate file from #1701's, and
that is structural, not stylistic**: `finOfflineExecBans` is keyed by file name
with exactly one entry per file, and #1701's entry bans the five I/O names this
ticket's entire subject requires. Folding both slices into one file means
shipping the intersection and losing #1701's defining property.

### Three new package-scope identifiers, and no more

This package's siblings deliberately hold their package-scope surface small so
concurrent tickets don't collide. Three is the whole budget here:

| Identifier | Kind | Contract |
| --- | --- | --- |
| `writeInitControlFixture` | `func(t *testing.T, dir string, rec *initControlFixtureRecord) string` | Writes `rec` into `dir` under the name `initControlFixtureName` mints from `rec.ClaudeVersion`, atomically. Returns the written path. Never mutates `rec`. |
| `compactInitControlRawRows` | `func(t *testing.T, rows []initControlFixtureField) ([]initControlFixtureField, []string)` | Returns `rows` with every raw-JSON-bearing row normalised through `compactRawMessages`, plus the names of the rows it touched. |
| `TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry` | test | AC 2 + AC 3. |

The test-name prefix `TestInitControlFixture_` does not collide with #1696's
`TestInitControlFixtureName_` under an unanchored `-run` regexp (the character
after `TestInitControlFixture` is `Name`, not `_`).

### `writeInitControlFixture` — the writer

Mirror `writePoolRevokeFixture` step for step. Signature above; body is
`os.MkdirAll` → `filepath.Join(dir, initControlFixtureName(out.ClaudeVersion))`
→ `json.MarshalIndent` → write `path + ".tmp"` → `os.Rename`. Four error
branches, each `t.Fatalf`.

Four contracts the body must hold, each of which a mutant can break:

1. **The name is minted, never formatted.** Pass `out.ClaudeVersion` into
   `initControlFixtureName` unmodified. A writer that interpolates its own
   `"initialize_control_v%s.json"` puts #1688's committed artifact back inside
   the overwrite hazard #1696's lock exists to close — with #1696's own test
   still green.
2. **The namer's input is `claude_version`, not `claude_version_raw`.** The two
   are distinct strings by #1701's distinctness property, and the raw form
   (`"2.1.220-FIXTURE (Claude Code)"`) carries a space and parens that slug to
   something else entirely.
3. **`os.MkdirAll` before the join.** A no-op against `t.TempDir()`, and load-bearing
   for #1688 passing the real `testdata/`. Kept for the same reason the sibling
   keeps it.
4. **The cap lands on a local copy.** `out := *rec`, then
   `out.StderrCapture = capFixtureCapture(out.StderrCapture)`.

On (4), one design note the sibling does not have to make: **the shallow copy is
sufficient only because the sole mutation is to a `string` field.** `out := *rec`
shares every slice header with the caller's record. That is correct today; a
future writer that capped a slice-valued field would be mutating the caller's
backing array through a copy that looks defensive. Say so in the doc comment so
the next person to add a cap does not inherit a false sense of isolation.

**Failure messages name `claude_version` and nothing else.** No `%+v` of the
record: that would move up to `stderrFixtureCap` bytes of child output out of the
bounded file and into an unbounded run log, which is the exact thing the cap
exists to prevent. `ClaudeVersion` is safe to print — it is already in the
filename.

### `compactInitControlRawRows` — selection by Go type, verified by name

`json.MarshalIndent` reflows the whitespace *inside* an embedded raw message, so
the value read back is not byte-equal to the value written and a **correct**
writer reddens. The record carries three fields this hits, not one.

Measured on this toolchain 2026-08-22 against all three shapes:

| Field | Go type | byte-equal after round trip | equal after compaction |
| --- | --- | --- | --- |
| `control_request_sent` | `json.RawMessage` | **false** | true |
| `control_responses` | `[]json.RawMessage` | **false** | true |
| `stdout_events` | `[]json.RawMessage` | **false** | true |
| (any plain `string` field) | `string` | true | — unaffected |

Normalising only `control_responses` — #1662's shape, which had one such field —
is **red on arrival** against a perfectly correct writer, not a latent risk.

**Selection is by the row's Go type, not by the three names.** #1701's length
assertion guarantees a fourth raw-JSON field added later arrives with a listing
row; a name-scoped normaliser would then redden against an honest writer. A type
switch over `row.value` covers it automatically:

- `case json.RawMessage:` → wrap in a one-element slice, compact, unwrap. The
  helper takes `[]json.RawMessage`, so the single-valued field wraps at the call
  site rather than growing a second helper. Unwrapping keeps the row's dynamic
  type identical to the record's, so a failure message reads as the field's own
  shape.
- `case []json.RawMessage:` → pass straight through `compactRawMessages`.
- `default:` → returned unchanged.

**And the type-scoped selection is itself pinned by name.** The three names are
the AC's checkable list, so the round trip asserts that the set of rows the
normaliser touched is exactly `{control_request_sent, control_responses,
stdout_events}`. This is what makes "selection is the architect's call" safe: it
catches a normaliser widened to blind rows it should not, and it turns a
normaliser that silently stopped matching into one clear failure instead of three
confusing row mismatches.

### `TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry`

Parent takes `t.Parallel()` and owns the tempdir and the one written artifact;
subtests are **not** parallel, following the sibling.

Flow: `dir := t.TempDir()` → `rec := initControlFullRecord()` →
`path := writeInitControlFixture(t, dir, rec)` → `os.ReadFile(path)` →
`json.Unmarshal` into a local `initControlFixtureRecord` → build both row sets
through `initControlFixtureFields` and `compactInitControlRawRows` → zip.

Both row slices come from the same function over the same struct type, so a
length divergence is impossible. **Do not add a length guard before the zip** —
it could never redden, and an assertion that cannot fail is the defect this
family spends most of its comment budget avoiding.

**Do not restate #1701's properties.** Non-zero and same-typed-distinctness ship
in `TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken` and are
preconditions this ticket consumes, not assertions it repeats.

**Do not add a cap row.** The round-trip record's `stderr_capture` is 53 bytes
against an 8 KiB cap, so `capFixtureCapture` is a no-op here and the row compares
equal on both sides. That row is #1700's whole subject.

### The literal that carries AC 2, and what would empty it

`versionSlug` lowercases and rewrites every run outside `[a-z0-9._-]` to `_`. A
realistic token like `2.1.220` is *already* slug-clean, so
`initControlFixtureName("2.1.220")` is byte-identical to what a plain
`fmt.Sprintf("initialize_control_v%s.json", token)` produces — and AC 2's "named
exactly what the namer mints" goes **0-red** against a self-formatting writer.

#1701 therefore shipped `claude_version` as `"2.1.220-FIXTURE"`, which slugs to
`2.1.220-fixture`, so the namer mints `initialize_control_v2.1.220-fixture.json`
while a self-formatting writer produces `…-FIXTURE.json` and AC 2 reddens.

- **Do not "clean up" that literal**, and do not substitute a slug-clean token of
  your own. AC 2's redness is carried entirely by that one uppercase run.
- **Do not source a token from `captureClaudeVersion`.** A real token is
  slug-clean, which empties AC 2 — and it would destroy this ticket's defining
  property. It is in the ban list for exactly this reason.

### The exactly-one-entry assertion — one assertion, four hazards

`os.ReadDir(dir)`, collect names, compare against
`[]string{initControlFixtureName(rec.ClaudeVersion)}`. It catches:

1. a `.tmp` stranded beside the target by a copy-instead-of-rename;
2. any stray file;
3. a write that escaped to the package's real `testdata/` — which leaves the
   temp directory at **zero** entries, and is the relative-path hazard no AST ban
   can close;
4. a writer that minted its target name some other way instead of calling the
   namer.

Hazard 3 is why this file may legitimately keep `os.WriteFile`, `os.ReadFile`,
`os.ReadDir` and friends while its siblings ban them.

### The `finOfflineExecBans` entry — twelve names

```
"resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
"probeClaudeVersion", "captureClaudeVersion",
"os.Getenv", "os.Environ", "os.LookupEnv",
"packageDir", "setModeFixturePath", "writeSetModeFixture", "writeFixture",
```

The check is a file-wide AST identifier match, so getting this wrong is red
against your own shipped code. The entry's doc comment must state, per group:

- **The first four keep a SKIP out.** `resolveClaudeBin` and
  `WithWorktreeAuthenticated` skip *inside* the test body, after `=== RUN` is
  printed, and a skip exits 0, which reads as a pass under `make e2e-realclaude`.
- **`captureClaudeVersion`** is the package's own direct `claude --version` exec.
  It returns `(raw, token)` — both of the record's version fields **and** the
  namer's input — so it is the exec a developer minting a name here is most
  likely to reach for. It `t.Fatalf`s rather than skipping, so it would not fake
  a pass; what it would destroy is this ticket's defining property, with
  `TestFinOfflineFilesReachNoExecHelper` green the whole time. #1696's and
  #1701's entries carry it, #1662's omits it — **follow the two newer siblings
  and do not harmonise it away against the older one.**
- **`os.Getenv` / `os.Environ` / `os.LookupEnv`** are the credential guard: the
  process environment here carries `CLAUDE_CODE_OAUTH_TOKEN` and
  `ANTHROPIC_API_KEY`. `os.LookupEnv` is the two-value form of `os.Getenv`;
  #1662 omits it, #1696 added it as a deliberate superset. Follow #1696.
- **`packageDir` plus every wrapper that reaches it** — today
  `setModeFixturePath`, `writeSetModeFixture`, `writeFixture`. The check is an
  identifier match, so a file calling a wrapper reaches `packageDir`
  transitively while never naming it; a `packageDir`-only entry leaves the ban
  true and the property false.
- **`exec.Command` / `exec.CommandContext` are declined**, for #1696's reason:
  this file imports no `os/exec`, and the package's own exec helpers are already
  covered. Do not add them speculatively.
- **Deliberately absent — copy this half from #1662, not from #1696/#1701:**
  `t.TempDir`, `os.WriteFile`, `os.Create`, `os.ReadFile`, `os.ReadDir` and
  `filepath.Glob` all stay available. This file's entire subject is a write, a
  read-back and a directory listing, so banning them would be red against
  shipped code. The relative-path hazard is closed instead by the
  exactly-one-entry assertion.

### The header

The file header follows the family's shape (what this file is, what it
deliberately is not, the offline paragraph, the `-run` invocation). Four things
it must and must not say:

- **Do not claim the cap bound is proven.** The writer calls `capFixtureCapture`,
  but nothing here exercises it. That is #1700.
- **Do not claim the no-mutation contract is proven either.** It is structurally
  the same claim as the cap: observing it needs an over-cap capture, so #1700's
  rows are what carry it, exactly as #1662's cap test carries its no-mutation
  assertion rather than its round trip.
- **Do not paraphrase #1662's "a misspelling is cosmetic" sentence.** #1662's key
  names are read by humans alone; **these are not.** #1690's decoder and #1692's
  fake read this contract, so a misspelled tag here is a real defect that this
  test is simply not the instrument for. The instrument is a reviewer diffing
  #1701's hand-written listing against the struct's tags — which is why that
  listing must never be regenerated by reflection.
- **State the offline property over this file's AST, not over the prose.**
  `TestFinOfflineFilesReachNoExecHelper` parses without `parser.ParseComments`
  precisely so the check cannot answer itself out of the header that states it.

---

## Concurrency model

No goroutines, no channels, no context. The only concurrency is `go test`'s:

- Parent test `t.Parallel()`; subtests sequential (the parent owns the tempdir
  and the single written artifact, and the subtests only read it).
- `initControlFullRecord` returns a **fresh pointer per call** by design. Do not
  hoist it to a package-level var — #1700's cap test mutates the returned record
  across parallel subtests, and a shared var would be a `-race` data race
  discovered two tickets away from the line that caused it.
- Every write in this ticket lands in a fresh `t.TempDir()`. No shared filesystem
  state between tests, so `-race` and parallel siblings are unaffected.

---

## Error handling

| Site | Failure | Handling |
| --- | --- | --- |
| `writeInitControlFixture` | `os.MkdirAll`, `json.MarshalIndent`, `os.WriteFile`, `os.Rename` | `t.Fatalf` naming `claude_version` and the error. Never `%+v` the record. |
| round trip | `os.ReadFile`, `json.Unmarshal`, `os.ReadDir` | `t.Fatalf` — an instrument that cannot read its own artifact has proven nothing. |
| `compactRawMessages` | malformed / nil raw message | Its own `t.Fatalf` (#1662's). See the diagnosis note below. |
| row comparison | any field mismatch | `t.Errorf` — report **every** offending row, not the first. See the printing rule below. |
| directory listing | wrong count or wrong name | `t.Errorf` naming all four hazards. |

### What a failure message may print

The writer's messages name `claude_version` **and nothing else** — that discipline
is the reason the cap exists. The round trip's row-mismatch message is the one
place that discipline could be silently broken, because #1662's equivalent prints
`wrote %v, read back %v` and one of these rows is `stderr_capture`.

**Print both values anyway, and carry the caveat.** Every value here is a #1701
synthetic literal, so printing is safe and the diagnostic is worth far more than
protecting a 53-byte fixture string. But say so in a comment, in #1701's own
words: *safe here and only here — #1688 fills this same record from a live child
and must not inherit the pattern.* Without that line the next file in the family
copies a message that dumps up to `stderrFixtureCap` bytes of real child output
into a run log.

This is also why the round trip must not grow a cap row: #1700's cap assertions
report **lengths and at most a short prefix**, never values, and they live in
#1700's own test for exactly that reason.

### One diagnosis trap, sharper than the ticket states it

A dropped or `json:"-"`-tagged **`control_request_sent`** leaves the read-back
side `nil`, and `json.Compact` over `nil` returns `unexpected end of JSON input`
(verified on this toolchain). So that mutant surfaces as `compactRawMessages`'s
`t.Fatalf` rather than as a row mismatch — and that message does not merely fail
to name the right row, it **names `control_responses` explicitly**, which is the
wrong row. The verdict is correct and the mutant is red; the diagnosis points at
the wrong file *and* the wrong field.

Expect it when mutating, do not read it as a broken test, and **say so in
`compactInitControlRawRows`'s doc comment** so the next reader is not misled. A
second helper is not the fix — the wrapping belongs at the call site.

The two slice-valued fields degrade cleanly (`nil` → empty slice, loop body never
runs) and report as ordinary row mismatches.

---

## Testing strategy

### The invocation

`make check` never compiles this package. `go vet` does not execute tests. These
tests therefore run **nowhere** unless invoked explicitly:

```
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestInitControlFixture_|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/
```

Both must report **PASS** — not SKIP, not "no tests to run" — on a machine with
no claude and no credentials. **Read the count of `=== RUN` lines, never the exit
code:** the suite exits 0 both on a build failure and on a full credentials skip.

Also confirm the whole tagged package still builds, since a sibling can break
while the standard gate stays green:

```
go vet -tags e2e_realclaude ./internal/e2e/realclaude/
```

### Scenarios (bullet points, not pre-written test bodies)

**AC 2 — one entry, named exactly what the namer mints**
- Write `initControlFullRecord()` into a fresh `t.TempDir()`.
- The directory afterwards holds exactly one entry.
- That entry's name equals `initControlFixtureName(rec.ClaudeVersion)`.
- Failure message names all four hazards it covers.

**AC 3 — every field reads back unchanged**
- Build the want rows from the record and the have rows from the decode, both
  through `initControlFixtureFields` then `compactInitControlRawRows`.
- Zip by index; compare with `reflect.DeepEqual`; label with the row's name.
- Report every mismatching row.

**AC 3 (selection guard) — the normaliser touches exactly three rows**
- The touched-name set equals `{control_request_sent, control_responses,
  stdout_events}`.
- Failure message says a widened normaliser blinds more than whitespace and a
  narrowed one reddens against an honest writer.

**AC 4 — the ban entry**
- `TestFinOfflineFilesReachNoExecHelper/initialize_control_writer_test.go`
  passes against the shipped file.

### Mutation table

Verify by **failure message**, not by which subtest went red — #1701 shipped the
lesson that a single mutant can fire two subtests where one was predicted, and
that is invisible in red/green.

| Mutant | Expected result |
| --- | --- |
| Writer formats `fmt.Sprintf("initialize_control_v%s.json", …)` instead of calling the namer | **RED** at the entry-name assertion (`…-FIXTURE.json` vs `…-fixture.json`). Non-vacuous *only* because of #1701's uppercase run. |
| Writer passes `ClaudeVersionRaw` to the namer | **RED**, same assertion (space and parens slug differently). |
| Writer copies instead of renaming (leaves the `.tmp`) | **RED** at the entry-count assertion — two entries. |
| Writer omits `os.Rename` entirely | **RED**, but earlier: `os.ReadFile(path)` fails first. Correct verdict, different message than the entry assertion. |
| Writer ignores `dir` and joins a relative `testdata/` | **RED** at the entry count — temp directory holds **zero**. ⚠️ This mutant writes a real file into the committed `testdata/`; delete it and confirm `git status` is clean before committing. |
| Drop the `capFixtureCapture` call | **GREEN — 0-red, and expected.** The fixture's 53-byte capture is far under the 8 KiB cap. Do not chase this; it is #1700's subject. |
| `json:"-"` on `ControlRequestSent` | **RED** via `compactRawMessages`' `t.Fatalf` — misattributed to `control_responses`. See § Error handling. |
| `json:"-"` on `ControlResponses` or `StdoutEvents` | **RED** as an ordinary row mismatch (empty slice vs populated). |
| Two fields given a **colliding** tag | **RED** — `encoding/json` drops both; both rows mismatch. |
| One field given a **unique wrong** tag | **GREEN.** The read-back decodes through the struct that wrote the file, so it is symmetric. This is the documented limit, not a gap to fix here. |
| Normaliser narrowed to `control_responses` only (#1662's shape) | **RED** on the other two rows **and** on the touched-set guard. |
| Normaliser widened to compact every row | **RED** on the touched-set guard. |
| Normaliser removed entirely | **RED** on all three raw rows. |

### Verifying the ban entry — `-overlay` does not work here

`TestFinOfflineFilesReachNoExecHelper` calls `parser.ParseFile` with a `nil`
source, so it reads the registered file **off disk at test run time**. `-overlay`
is a build-time mapping consumed by the `go` command and never interposes on the
test binary's own reads: a banned call injected via overlay compiles cleanly
while the check parses the unmodified file and stays green — a misleading pass
that reads as "the ban does not bite".

Verify this entry with a **real edit and a real revert**: add e.g.
`_ = packageDir(t)` to the file, run the check, confirm it reds naming
`packageDir` and the position, revert, and confirm the file is byte-identical to
the pristine version before committing.

Note also that removing a name from the ban list is *not* a detectable mutant —
it only weakens the check. The entry is verified by adding a banned call, never
by deleting a name.

`-overlay` remains correct for every ordinary compiled-code mutant in the table
above (the writer and the round trip).

---

## Open questions

None blocking. Two notes for implementation:

1. **The committed comment in `initialize_control_names_test.go` calls this
   "#1697's writer".** #1697 was superseded and closed; the writer is this
   ticket. Nothing needs editing there, and do not go looking for #1697.
2. **`#1700`'s body says its cap proof lands in "the file the record and writer
   already live in".** That phrasing predates the #1701/#1702 split. The cap
   proof lands in **this** file, and needs no new ban name.

---

## Scope

- **Test-only.** No production file changes.
- **Two files:** `internal/e2e/realclaude/initialize_control_writer_test.go`
  (new) and `internal/e2e/realclaude/offline_exec_ban_test.go` (one map entry
  plus its doc comment).
- **Not in scope:** the cap proof (#1700), the live capture (#1688), the decoder
  (#1690), the fake (#1692), any change to #1701's record, fixture or listing,
  and any knowledge-base doc (the documentation phase owns those).

### Size check (re-applied against this spec)

| Limit | Boundary | This spec |
| --- | --- | --- |
| Production source files created or modified | ≤ 3 | **0** (both files are `*_test.go`) |
| Total written work | ≤ 400 | **~280–330** (see below) |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** (additive file + one map entry) |
| Acceptance criteria | ≤ 5 | **4** |
| Distinct error/reject branches | ≤ 10 | **4** (mkdir, marshal, write tmp, rename) |

Sized against the two realized siblings in this exact family rather than
bottom-up, because this package's comment density runs ~1:1 with code. #1662
(`ddb25f3`) shipped 547 insertions for record + writer + round trip + cap test +
three helpers; #1701 (`daeec24`) shipped 437 for record + fixture + listing + one
four-subtest test. This slice rebuilds the writer half only: header and imports
~70, writer and doc ~45, round trip ~95 (#1662's 115 less the ~42 lines of
non-zero/distinctness subtests that shipped in #1701, plus the three-row
normalisation, the listing zip and the touched-set guard), normaliser ~25, ban
entry ~37 (matching #1701's actual). It reuses `capFixtureCapture`,
`compactRawMessages`, `truncateString`, `fixtureFieldNonZero` and
`initControlFixtureFields` rather than rebuilding any of them.

---

## Security review

**Verdict:** PASS

Walked against `$AGENTS_REPO_PATH/architect/security-review.md`. No MUST FIX. One
SHOULD FIX was found and fixed inline before this section was written (§ "What a
failure message may print"); two items are named OUT OF SCOPE with owners.

**Findings:**

- **[Trust boundaries]** No findings *for this ticket*. The boundary in this
  design is `child subprocess output → record fields → committed JSON file`, and
  it is crossed in **#1688**, not here: every value this slice writes is a #1701
  synthetic literal. The boundary is explicit and single — `writeInitControlFixture`
  is the only thing that puts a record on disk. Two transformations happen at it
  and both are documented rather than assumed: `json.MarshalIndent` **validates**
  every embedded `json.RawMessage` and returns an error on malformed bytes, so a
  non-JSON child line reaches #1688 as a loud `t.Fatalf` instead of a committed
  garbage file (which is why the record carries `NonJSONLineCount` and
  `ScannerError` rather than stuffing raw lines into `StdoutEvents`); and
  `encoding/json` HTML-escapes `<`, `>` and `&` inside a raw message, an escape
  that survives compaction. #1701's literals are clean of all three by deliberate
  choice, so nothing reddens here — but #1688's real assistant-text events will
  carry them, and the committed bytes will therefore not be byte-identical to the
  wire bytes. Not exploitable; named because it is the one lossy step at the
  boundary.

- **[Tokens, secrets, credentials]** No MUST FIX. Nothing in this design
  generates, stores, rotates or revokes a token, so those four lifecycle
  questions do not apply. What does apply:
  - `stderr_capture` is **the** credential-bearing field — an auth failure can
    dump an unbounded, credential-bearing child message into a file that is then
    *committed*. AC 1 puts `capFixtureCapture` inside the writer rather than at
    each call site precisely so #1688 cannot forget it.
  - **This ticket does not prove the bound holds** (the fixture's capture is 53
    bytes against an 8 KiB cap, so the call is a no-op here). #1700 proves it and
    is blocked by this ticket. The header must not claim otherwise — that is an
    explicit design constraint above, not an oversight.
  - The process environment carries `CLAUDE_CODE_OAUTH_TOKEN` and
    `ANTHROPIC_API_KEY`. No code here reads it, and the ban entry enforces that
    over the file's **AST** via `os.Getenv`, `os.Environ` and `os.LookupEnv`. The
    AST check is deterministic code standing behind advisory header prose —
    different fabric, and it parses without `parser.ParseComments` so it cannot
    satisfy itself out of the paragraph that states it.
  - The record has no `env` field and must never grow one: the credential reaches
    the child through the environment while the argv carries none, so recording
    argv is safe and recording env would not be. This slice adds no field, so the
    constraint holds by construction.

- **[File operations]** No MUST FIX; one OUT OF SCOPE below.
  - *Path traversal* — closed, and the closure is verified rather than asserted.
    `rec.ClaudeVersion` is the only record field that reaches a filesystem path,
    and in #1688 it is **subprocess output**, i.e. genuinely untrusted. The single
    sanitiser is `versionSlug` (`[^a-z0-9._-]+` → `_`, lowercased first, clamped
    at 32 bytes); totality rests on that character class rather than on any sample
    table, and `initControlFixtureName`'s literal prefix means no input can
    produce `.` or `..`. #1696's containment subtest is the tripwire that reddens
    if the class is ever widened to admit a separator. **Nothing in this ticket
    may bypass the namer** — which is also why `captureClaudeVersion` is banned.
  - *Symlinks* — `os.WriteFile` follows them, and the namer's guarantee is
    **lexical**: it says `filepath.Join(dir, name)` cannot walk out of `dir`, and
    says nothing about `dir` itself. `O_NOFOLLOW` is declined. Every `dir` in this
    ticket is a fresh `t.TempDir()`; in #1688 it is the repo's own `testdata/`.
    An actor who can plant a symlink in either already holds write access to the
    source tree, so the check would be a defense for a failure mode this pipeline
    has not observed. `initControlFixtureName`'s doc comment already assigns
    choosing `dir` to the caller.
  - *TOCTOU* — none. The writer never stats-then-opens; it writes a temp file and
    renames. `os.Rename` is atomic within a filesystem.
  - *Permissions* — `0o755` dir / `0o644` file, matching all four sibling
    writers. **The mode is deliberately not the control here:** the file's
    destination is git, which records nothing but the exec bit, so a `0o600`
    fixture would be security theatre. The control that keeps a credential out of
    the artifact is the cap, and its proof is #1700.
  - *Atomic writes* — temp-file-plus-rename is present, and it delivers exactly
    the stated threat: the **target** name is never observed half-written. Note
    that `f.Sync()` is omitted, diverging from PROJECT-MEMORY's registry
    atomic-write recipe. Deliberate: that recipe defends durability against a
    machine crash, whereas the threat here is process interruption, and the file
    is read back in-process and committed rather than reloaded after a reboot.
    Both sibling writers omit it too.
  - **OUT OF SCOPE — owned by #1688: the `.tmp` residue.** Rename guarantees the
    target is never half-written; it does **not** guarantee no residue. A SIGKILL
    between `os.WriteFile` and `os.Rename` leaves a possibly-truncated
    `initialize_control_v<slug>.json.tmp` holding up to `stderrFixtureCap` bytes
    of child stderr. Verified against this repo: `.gitignore` carries **no**
    `*.tmp` rule (only a narrow `permission_protocol_v*.json` allow/deny pair), so
    inside the tracked `testdata/` a `git add -A` would pick that residue up.
    **This ticket has zero exposure** — every write here lands in a `t.TempDir()`
    that is never tracked — which is why the fix belongs to the slice that writes
    to `testdata/`. Cheapest mitigations for #1688: a `*.tmp` ignore rule scoped
    to that directory, or a `t.Cleanup` removing the temp path. Declined here: a
    `defer os.Remove(tmp)` would cover only the error branches, not the signal
    that is the actual threat, and would diverge from the sibling writer for no
    gain.

- **[Subprocess / external command execution]** No findings. This design executes
  nothing: no `exec.Command`, no `sh -c`, no signals, no environment scrubbing to
  get wrong, and the file imports no `os/exec`. `exec.Command` and
  `exec.CommandContext` are declined from the ban list rather than added
  speculatively, per #1696's reasoning — the package's own exec helpers are the
  ones reachable without a new import, and all of them are banned by name. **One
  honest limit, stated rather than glossed:** the ban is an identifier match, so
  it is an *accident-catcher, not a sandbox* — a function value, a method value,
  or reflection would all pass it. The modelled threat is a confused developer
  reaching for `captureClaudeVersion` because it returns exactly this record's two
  version fields, which is the observed failure mode this family designed against.
  OUT OF SCOPE, owned by `finOfflineExecBans` as a whole: a *newly added* package
  exec helper has to be appended to every entry by hand, and nothing detects that.

- **[Cryptographic primitives]** Not applicable, with the reason: this design
  contains no randomness (no `math/rand`, no `crypto/rand`), no keys, no nonces
  and no hashing. `reflect.DeepEqual` compares fixture field values, not secrets,
  so `crypto/subtle.ConstantTimeCompare` is irrelevant — there is no
  attacker-controlled comparison against a secret anywhere in the flow.

- **[Network & I/O]** No sockets, no HTTP server, no TLS, no timeouts to set. The
  checklist's input-size-limit question does map onto the record, and the answer
  is split: `stderr_capture` is capped at `stderrFixtureCap`, while
  `control_request_sent`, `control_responses` and `stdout_events` are
  **deliberately uncapped**, because capping structured evidence destroys the
  artifact (#1701's and #1662's inherited decision). OUT OF SCOPE, owned by
  **#1688**: there is neither a per-item nor an *aggregate* bound on
  `stdout_events`, so a long live run produces an arbitrarily large committed
  fixture. This slice adds no field and changes no bound, so it neither creates
  nor worsens that gap — but a per-field cap is not an aggregate cap, and the
  distinction is worth carrying forward to the run that fills the field.

- **[Error messages, logs, telemetry]** One SHOULD FIX, **fixed inline**. No
  telemetry or metrics exist in this design.
  - The writer's `t.Fatalf`s name `claude_version` and the error and nothing
    else. A `%+v` of the record would move up to `stderrFixtureCap` bytes of child
    output out of the bounded file and into an unbounded run log — defeating the
    cap entirely, since run logs are retained where fixtures are reviewed.
  - `compactRawMessages`' `t.Fatalf` prints `json.Compact`'s error, which is a
    `*json.SyntaxError` carrying an offset and at most the single offending byte —
    not content. Safe.
  - **SHOULD FIX (fixed):** the spec as first drafted did not say what the
    row-mismatch message may print, and the sibling this ticket tells the
    developer to mirror prints `wrote %v, read back %v` — over a row set that
    includes `stderr_capture`. Copying it verbatim would seed the pattern #1688
    must not inherit. § "What a failure message may print" now settles it: print
    both values (safe here, and worth far more diagnostically than protecting a
    53-byte synthetic string) **and carry #1701's caveat naming why it is safe
    here and only here**.

- **[Concurrency]** No findings. No goroutines are spawned, so none can leak; no
  locks are taken, so there is no ordering to document; no shared mutable state
  exists, so there is no check-then-mutate gap. The one real hazard is inherited
  and already closed: `initControlFullRecord` returns a **fresh pointer per call**
  rather than a package-level var, because #1700's parallel subtests mutate the
  returned record and a shared var would be a `-race` data race surfacing two
  tickets away from its cause. The parent test takes `t.Parallel()` and owns the
  tempdir; subtests are sequential. Interruption mid-write is the atomicity item
  under [File operations].

- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model is
  relay-scoped (Noise_IK, device pairing, relay trust) and none of its threats
  reach a test-only fixture writer; there is no `docs/threat-model.md` in this
  repo. The governing constraint for this family is the package's own and is
  stated rather than assumed: **no credential may reach a committed fixture.**
  This design addresses it on both routes — the environment route is closed here
  and enforced by the AST ban, and the child-output route is bounded by the cap
  this writer applies, whose proof is #1700 and is named as such rather than
  claimed here.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-22
