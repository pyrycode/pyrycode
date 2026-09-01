# #1939 — offline reader for the committed AskUserQuestion capture

Pin the committed `AskUserQuestion` capture with a deterministic, credential-free
reader: the repo, not a token-spending live run, is what proves the bytes
downstream code parses are present, still shaped as expected, and clean.

## Files to read first

Symbols, not lines — resolve each with `codegraph_search` / `codegraph_node`.

- `internal/e2e/realclaude/initialize_control_compare_test.go` → `initControlArmFixtureGlob`,
  `initControlDiscoverArms` — **the idiom this file follows**: relative glob constant,
  `filepath.Glob` error branch, zero-match `t.Fatalf`, `os.ReadFile`, decode through the
  family's own record type. Read the glob's doc comment for why a glob beats addressing
  the family by exact name.
- `internal/e2e/realclaude/permission_protocol_regression_test.go` → `fixtureGlob`,
  `TestRealClaude_PermissionProtocol_RegressionFixtures` — the original zero-match
  "deleted fixture set must be loud" phrasing.
- `internal/e2e/realclaude/ask_user_question_shape_test.go` → `requireAskQuestionShape`,
  `askQuestionShapeFindings`, `askQuestionInput` — the shape assertion this file calls
  and must not restate. Read `requireAskQuestionShape`'s doc for the never-`%+v`-the-record
  rule, and the file header for the presence-not-truth limit on multi-select.
- `internal/e2e/realclaude/ask_user_question_record_test.go` → `askQuestionFixtureRecord`
  — the decode target. Four fields, no `omitempty`.
- `internal/e2e/realclaude/ask_user_question_names_test.go` → `askQuestionFixtureName`
  — the namer whose literal `ask_user_question_v` prefix the glob's head is derived from,
  and the `patterns` table this file's glob must **not** be added to (see § Traps).
- `internal/e2e/realclaude/ask_user_question_writer_test.go` → `scanAskQuestionFixture`,
  `askQuestionPlantedPath`, `askQuestionPlantedKeyPrefix`,
  `TestAskQuestionFixture_ScanRefusesAPlantedValue` — the write-path scan (which this
  file must **not** call), the synthetic plants it reuses, and the inline
  `dropcapScanner{needles: dropcapFixedNeedles()}` construction it copies.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapFixedNeedles`,
  `dropcapScanner`, `newDropcapScanner`, `dropcapMinNeedle`, `dropcapDenyUsers`,
  `dropcapContains` — the deny-scan. `dropcapFixedNeedles`' doc states the property this
  slice depends on outright.
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans`,
  `TestFinOfflineFilesReachNoExecHelper` — the ban table and the AST check that enforces
  it. Read the `"initialize_control_compare_test.go"` and `"ask_user_question_writer_test.go"`
  entries; this file's entry is composed from both.
- `docs/knowledge/features/e2e-realclaude-initialize-control-compare-test-go.md`
  — the lessons from the only other offline reader in this directory, in particular the
  three shipped claims that adding a family glob made false.
- `docs/knowledge/features/e2e-realclaude-ask-user-question-writer-test-go.md`
  — the writer's deny-scan lessons.
- `CODING-STYLE.md` § "Comments — Citing Other Code" — symbol citations only; `make cite-guard`
  is a build gate with no depth and no range exemption.

## Context

#1938's live run produced `internal/e2e/realclaude/testdata/ask_user_question_v2.1.239.json`
and committed it. A live run cannot on its own prove an artifact reached the repository —
an agent run happens in a worktree discarded when the run ends, and on 2026-08-25 #1763's
gate ran green while landing zero of its three artifacts.

The second check has to be **different fabric**: the capture run is stochastic and needs
credentials; this reader is deterministic, needs neither, and reddens when the artifact is
absent, undecodable, reshaped by a claude release, or leaky.

Every input is already in the tree, verified against `main` at `fcfe1813`:

- The capture is tracked (`78944ac5`), matches `testdata/ask_user_question_v*.json`, and
  is clean under all five fixed needles (`sk-ant-`, `/Users/`, `/home/`,
  `/private/var/folders/`, `/var/folders/` — each occurring zero times).
- Its content is well-formed under today's checks: `tool_name` is `AskUserQuestion`, one
  question with a non-empty header and text, `"multiSelect": false` **present**, two
  options each carrying a label and a description.
- `requireAskQuestionShape` / `askQuestionShapeFindings` ship with all nine reported names.
- `dropcapFixedNeedles` is the version-independent half of the deny-scan.

Nothing here is built a second time. This ticket wires four existing pieces together over
the committed bytes and adds one ban entry.

**No ADR is warranted.** This is one test file following an idiom the package already has
two instances of.

## Design

### One new file, one ban entry

| File | Change |
|---|---|
| `internal/e2e/realclaude/ask_user_question_reader_test.go` | new, behind `//go:build e2e_realclaude` |
| `internal/e2e/realclaude/offline_exec_ban_test.go` | one map entry added to `finOfflineExecBans` |

The name pairs with `ask_user_question_writer_test.go`: writer ↔ reader. It must not be
called `*_capture_*` — `ask_user_question_capture_test.go` is #1938's live run, and the two
must stay distinguishable at a glance.

### The glob

```go
// testdata/ask_user_question_v*.json
const askQuestionFixtureGlob = ...
```

- **Relative, with the `testdata/` prefix**, matching `fixtureGlob`, `dropcapFixtureGlob`
  and `initControlArmFixtureGlob` (and unlike `setModeFamilyGlob`, which names base names):
  `go test` runs in the package source directory, so the pattern resolves with no
  `packageDir` call. That is what keeps `packageDir` bannable — see § The ban entry.
- **The head is `askQuestionFixtureName`'s literal prefix.** That literal is the part of a
  minted name no input can reach, which is what makes this glob unable to sweep any of the
  four foreign families and makes every name that namer mints match it.
- **No second `_` in the pattern**, unlike `fixtureGlob` and `initControlArmFixtureGlob`:
  this family has no arm and no mode dimension, so `askQuestionFixtureName` mints exactly
  one component after the version slug.
- **No exact-name addressing.** It would hard-code a version token, and a re-capture at a
  new version would demand a code edit. `initControlArmFixtureGlob`'s doc argues this at
  length; the argument carries over unchanged.

### One test, one subtest per matched file

```go
func TestAskQuestionReader_CommittedCapturesAreWellShapedAndScanClean(t *testing.T)
```

Structure — parent, then `t.Run(filepath.Base(path), …)` per match:

1. **Glob.** `filepath.Glob` error → `t.Fatalf` naming the pattern ("a malformed pattern
   constant makes every claim in this file meaningless"). Zero matches → `t.Fatalf` naming
   the pattern, in `permission_protocol_regression_test.go`'s "a deleted fixture set must
   be loud" phrasing. **AC 1.**
2. **Build the scanner once, in the parent:** `dropcapScanner{needles: dropcapFixedNeedles()}`,
   written inline exactly as both offline callers in `ask_user_question_writer_test.go` do.
   Sharing one value across parallel subtests is safe for that file's stated reason — it is
   append-only during construction, read-only afterwards, and `scan` is a value receiver
   that allocates its own results.
3. **Per file: read.** `os.ReadFile`; error → `t.Fatalf` naming the base name and the error.
   A file the glob matched but the process cannot read must fail, never be skipped.
4. **Per file: scan the raw bytes — before the decode.** See § Scan the file, not a
   re-marshal. Two vacuity controls plus the verdict, in this order:
   - `notApplied` non-empty → `t.Fatalf`. Every fixed needle is exempt from
     `dropcapMinNeedle` by construction, so a reported class means a fixed needle was
     marked dynamic and the verdict below is green-and-vacuous.
   - The positive control (below) must hit.
   - `hits` non-empty → `t.Fatalf` naming the **count and the class names and nothing
     else**. **AC 3.**
5. **Per file: decode.** `json.Unmarshal` into `askQuestionFixtureRecord`; error →
   `t.Fatalf` on its own, naming the base name. **This is AC 2's explicit requirement**:
   an undecodable file must not reach the shape assertion as a zero-valued record, where
   `askQuestionShapeFindings` would report `tool_name` alone and "nothing else missing"
   would read as all-clear on garbage. Plain `json.Unmarshal`, **not**
   `DisallowUnknownFields`: a claude release *adding* a field is not a defect this reader
   owns, and its bytes are scanned anyway because step 4 reads the file, not the record.
6. **Per file: shape.** `requireAskQuestionShape(t, &rec)` — called, never restated.
   **AC 2.** It is a `t.Fatalf` wrapper and must be called directly from the subtest
   goroutine, which this structure does.

`t.Parallel()` on the parent and on each subtest, the package idiom. A `t.Fatalf` inside one
subtest fails that file alone; every other match still runs, so one run names every broken
fixture without an accumulator. That is why this file uses per-file subtests rather than
`initControlDiscoverArms`' accumulate-then-one-`Fatalf` shape: the shape assertion this
ticket is required to reuse is already a fatal wrapper, and an accumulator would force a
second call path around it.

### Scan the file, not a re-marshal

**Do not call `scanAskQuestionFixture`.** It is the obvious-looking reuse and it is wrong
here, for two independent reasons:

- It marshals the **decoded record** and scans that. Any byte in the committed file that
  `askQuestionFixtureRecord` does not carry — an unknown key, a key a claude release
  renamed, anything a future field cap drops — is silently absent from what it scans.
  That class is precisely what this reader exists to catch.
- It `t.Fatalf`s from inside itself, so it cannot be composed with the ordering above.

Scan `raw` — the bytes `os.ReadFile` returned — through `dropcapScanner.scan`. That is
reuse of the deny-scan (the needle set and the scan method); it is not a second copy of it.

**Never `newDropcapScanner`.** It reads `os.Getenv` twice and `realHome`, which makes the
verdict depend on whose machine ran the test and puts two live credentials in a struct.
Both names are banned in this file's entry for exactly that reason.

### The scan's positive control

The committed capture is clean, so "no hits" is the expected result whether the scan is
wired correctly or not scanning anything at all. Nothing else in the package closes that:
`TestAskQuestionFixture_ScanRefusesAPlantedValue` proves the *writer's* call site fires,
not this one.

The control is one comparison over the same `raw` and the same `scanner`: concatenate
`askQuestionPlantedPath` onto the file's bytes and require `dropcapDenyUsers` among the
hits, via `dropcapContains`.

```go
// must report dropcapDenyUsers; a fresh []byte, never append(raw, …)
scanner.scan([]byte(string(raw) + askQuestionPlantedPath))
```

**A fresh slice, never `append(raw, …)`.** `os.ReadFile` can return a slice with spare
capacity, and appending into it would mutate the very bytes the clean scan and the decode
read.

This pairs a required-negative with a required-positive over one buffer, so a subtest that
scanned the wrong variable cannot satisfy both. `askQuestionPlantedPath` is reused rather
than re-minted; it is already constrained to exactly one armed class, JSON-string-safe, and
free of `<`, `>`, `&`.

### The ban entry

Composed from `initialize_control_compare_test.go`'s entry — the shape that fits, because
this file legitimately reads `testdata/` — with three deliberate divergences. **Do not copy
a sibling's entry whole**: the other offline files in this family ban `filepath.Glob` and
`os.ReadFile`, and this reader must call both. A whole copy is red against correct code.

Nineteen names:

```
resolveClaudeBin, WithWorktreeAuthenticated, WithWorktree,
probeClaudeVersion, captureClaudeVersion,
os.Getenv, os.Environ, os.LookupEnv,
packageDir, setModeFixturePath, writeSetModeFixture, writeFixture,
writeAskQuestionFixture, scanAskQuestionFixture,
newDropcapScanner, realHome,
os.WriteFile, os.Create, os.ReadDir
```

- `filepath.Glob` and `os.ReadFile` are **absent** — this file's whole subject.
- **`packageDir` stays banned**, diverging from the compare entry which drops it. This
  reader resolves its fixtures through the relative glob constant and nothing else, so the
  ban costs nothing and closes the route by which a later edit could join a fixture-derived
  value onto an absolute path. Say so in the entry's comment; a reviewer diffing against
  the compare entry will otherwise read it as a copy error.
- **`newDropcapScanner` and `realHome` are added**, from the writer's entry. The pull is
  stronger here than there: the writer receives its scanner as a parameter, while this file
  constructs one, so the constructor is the single call a developer here reaches for — and
  it satisfies the `os.Getenv` ban to the letter while destroying the offline property.
- **`scanAskQuestionFixture` is added**, and it is the one name with no precedent in either
  source entry. It is the trap in § Scan the file, not a re-marshal, made deterministic:
  the AST check is what stops the weaker scan from arriving as a plausible-looking reuse.
- `writeAskQuestionFixture` mirrors the compare entry's `writeInitControlFixture`: this
  file reads and must never write.
- `os.ReadDir` stays banned: discovery goes through the glob constant, so a directory
  listing is a second, unpinned route to the same files.
- `t.TempDir` is absent, and `os.TempDir` is not added — matching both source entries.

State the ban's limit in the comment as every sibling entry does: the check is per-file
**syntax**, not a call graph, so a banned name stays reachable through a helper this file
calls while the entry stays green.

## Error handling

Every failure is a `t.Fatalf` scoped to the file it concerns; there is no recovery path and
no partial-success reporting.

| Failure | Branch | Message carries |
|---|---|---|
| malformed glob constant | parent, fatal | the pattern |
| zero matches | parent, fatal | the pattern; "a deleted fixture set must be loud" |
| unreadable file | subtest, fatal | base name, the `os` error |
| a class not applied | subtest, fatal | the count, the class names |
| the planted control does not hit | subtest, fatal | the expected class name |
| a deny class hit | subtest, fatal | the count, the class names, the base name |
| undecodable file | subtest, fatal | base name, the decode error |
| shape findings | `requireAskQuestionShape` | its own message, unchanged |

**Message discipline, and it is the load-bearing rule of this file.** No message prints
`raw`, an excerpt of it, a byte offset, a needle, the `dropcapScanner`, or the record. The
scan's whole reason for existing is that a committed capture might carry a credential; a
message quoting the offending bytes moves that value into a run log this pipeline salvages
and inverts the control. `requireAskQuestionShape` already holds this line (it prints
`ClaudeVersionSlug` and `ToolName` only, and its doc says why); the new call sites must
match it. Base names and the glob pattern are repo content and are safe.

A leaky **and** misshapen file reports the leak alone, because the scan fatals first. That
is correct: the file must be redacted and re-captured either way, and the leak is the
finding with the more urgent remedy.

## Concurrency model

No goroutines, no channels, no context. `t.Parallel()` on the parent and on each per-file
subtest; the shared `dropcapScanner` is read-only after construction and `scan` is a value
receiver. Every `t.Fatalf` runs on its own subtest's goroutine.

## Testing strategy

The file *is* the test. What proves it:

**The credential-free tagged run — AC 4.** Run from the worktree root:

```bash
env -u ANTHROPIC_API_KEY -u CLAUDE_CODE_OAUTH_TOKEN \
  go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestAskQuestionReader_|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/
```

**Paste the `=== RUN` count and the SKIP count into the PR**, not the exit code. This
package exits 0 both when it fails to build and when every test skips; an exit code cannot
tell either from a pass. Required: a non-zero executed count and **zero** `--- SKIP`.
`fixtures_test.go`'s `TestMain` does not gate on credentials — verified on this ticket
before the `needs-real-claude` label was removed — so nothing here can legitimately skip.

**`make preship`** is the standing gate that keeps the package compiling; `make check`
never compiles it.

**Mutants this file must redden on** — check each by hand rather than assuming:

- Delete or rename the committed capture → zero matches → the parent fatals.
- Truncate the capture's JSON → the decode branch fatals on its own; the shape assertion is
  never reached.
- Drop the `multiSelect` key from the capture → `requireAskQuestionShape` reports
  `multi_select_present` alone. (This is the check #1950 built and the reason it is a
  blocker; confirm it fires.)
- Rename `tool_name`'s value in the capture → `tool_name` reported alone.
- Splice `/Users/` into the capture → the scan fatals naming `users-path-prefix`.
- Replace the scan's subject with a re-marshal of the decoded record → an unknown key
  carrying `/Users/` is no longer seen. This one has no automated red; it is the reason
  `scanAskQuestionFixture` is banned, and the ban is what reddens.
- Drop the planted-append control → the scan verdict goes vacuous with nothing red. This is
  why the control ships in the same commit.

**Do not** add a test that mutates the committed capture on disk to demonstrate the above.
This file is fenced off from writing for that exact reason; verify by hand in a scratch copy
and revert.

## Scope — what this file deliberately does not do

Each of these was considered and declined. Building any of them is what turned #1764 into a
562-line reader, and none is asked for by an acceptance criterion.

- **No version agreement across matches.** The glob may match several captures one day;
  every match is validated independently. There is no "exactly one" assertion — that would
  redden the day a second version is committed, which is a healthy event.
- **No name↔record binding.** #1944's lock already pins `askQuestionFixtureName`, and no AC
  asks this reader to re-derive the name from `ClaudeVersionSlug`.
- **No cross-capture comparison, no arm dimension, no discovery struct.** This family has
  one capture shape.
- **No widening of the scan beyond this family.** AC 3 is explicit: five committed captures
  outside this family carry a `/Users/` occurrence today
  (`initialize_control_v2.1.239.json` and the four `set_permission_mode_v2.1.220_*`), so a
  widened scan reddens on files this ticket does not own.
- **No knowledge-base doc.** The documentation phase owns
  `docs/knowledge/features/e2e-realclaude-*.md`.

## Traps

1. **Do not add `askQuestionFixtureGlob` to `ask_user_question_names_test.go`'s `patterns`
   table.** That table holds globs **foreign** to this family, and every name
   `askQuestionFixtureName` mints matches this one *by design*. Adding it makes "no minted
   name joins a committed family" red against correct code. `initControlArmFixtureGlob`'s
   doc states the same rule for its own family; state it in this glob's doc too.
2. **The "Four committed families live there" claims in `ask_user_question_names_test.go`
   stay true and need no edit.** They enumerate globs foreign to this family, and the new
   glob is this family's own. Adding a family glob made three shipped claims false in
   #1764 — that is why this was checked rather than assumed. It was, on 2026-09-01; leave
   those comments alone.
3. **The three shipped `#1939` references are accurate.** `ask_user_question_shape_test.go`
   (header and the empty-check-name message) and `ask_user_question_capture_test.go` all
   describe this ticket correctly as the offline reader. No correction sweep is needed.
4. **`askQuestionCheckToolInputDecodes` is not this file's decode branch.** That constant
   belongs to `askQuestionShapeFindings`, which decodes `rec.ToolInput`. This file's decode
   is of the whole file into `askQuestionFixtureRecord`, one level up, and it fails before
   the shape assertion runs. Two different decodes; do not fold them.

## Budget

The reader is roughly **40 lines of Go**. Everything else in the file is doc comment, and
this family's headers have run 60–110 lines.

Measured actuals on `main` for the sibling slices: #1951 = 355 + 47 = **402**, #1944 = 445,
#1943 = 567, #1941 = 632. This slice writes strictly less code than any of them — the
record, the namer, the shape assertion and the needle set all ship. **Target ≤ 380 lines
total across the two files, and do not exceed #1951's 402.** If the header is heading past
90 lines, cut it: the arguments for the glob's shape, the presence-not-truth limit and the
plant constraints are all written down in files this spec's reading list already names, and
a second copy of them here is the thing to drop first.

## Open questions

None blocking. One judgment call left to implementation: whether the two vacuity controls
(`notApplied` empty, planted-append hits) run per matched file or once in the parent. Per
file is the recommendation — it costs nothing with one capture, it keeps every assertion
about the bytes it is scanning, and the planted control is only meaningful over the same
buffer the clean scan read.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. There is one boundary and it is explicit: the bytes
  `os.ReadFile` returns are untrusted-by-provenance (written by a live claude child in
  #1938's run) and become trusted only after `askQuestionShapeFindings` reports nothing.
  The design forces every match through it, and the ordering in § Design step 5 closes the
  one way that boundary could be bypassed — a failed decode reaching the assertion as a
  zero-valued record, where "nothing missing" would report all-clear on garbage. Downstream
  holds `askQuestionFixtureRecord`, a four-field type whose only untyped member is
  `ToolInput json.RawMessage`; nothing in this file dereferences it beyond handing it to
  the shape assertion.
- **[Tokens, secrets, credentials]** No MUST FIX, and this is the category the ticket is
  labelled for. This file mints, stores and reads no token. It *searches* for two classes
  of them. Three concrete decisions carry the property: (a) the scanner is built from
  `dropcapFixedNeedles` inline and **never** `newDropcapScanner`, whose two `os.Getenv`
  reads would place live `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` values into a
  struct in scope, one `%v` away from a salvaged run log — and both names are banned in the
  `finOfflineExecBans` entry, so the rule is enforced by AST match rather than by prose;
  (b) the scan runs over the **raw file bytes**, so a credential sitting in a key
  `askQuestionFixtureRecord` does not carry is still seen — calling `scanAskQuestionFixture`
  instead would scan a re-marshal of the decoded record and miss exactly that, which is why
  that name is banned too; (c) every failure message is restricted to class names, counts
  and base names, matching `scanAskQuestionFixture`'s and `requireAskQuestionShape`'s
  existing discipline. The residual risk is a needle-set regression, and it has two named
  controls rather than one: `notApplied` must be empty, and the planted-append must hit.
- **[File operations]** No findings. The file performs **no writes at all** — `os.WriteFile`,
  `os.Create` and `writeAskQuestionFixture` are banned in its entry, so the relative-path
  hazard the sibling entries close (a relative `testdata/…` write reaching the committed
  captures, naming no wrapper) cannot arise. Reads are confined to paths returned by
  `filepath.Glob` over a **constant** pattern: no value read out of a fixture is ever joined
  into a path, so there is no traversal surface. No `os.Stat`-then-open, so no TOCTOU.
  Symlink following is not addressed and does not need to be: the glob resolves under the
  package source directory of a git checkout, and an attacker who can plant a symlink there
  can already edit the test.
- **[Subprocess / external command execution]** Not applicable by construction, and the
  construction is enforced: `resolveClaudeBin`, `WithWorktree`, `WithWorktreeAuthenticated`,
  `probeClaudeVersion` and `captureClaudeVersion` are all banned in the file's
  `finOfflineExecBans` entry, and `TestFinOfflineFilesReachNoExecHelper` parses the file
  without `parser.ParseComments` so the check cannot satisfy itself out of the header that
  states it. The first three would additionally skip *inside* the test body, and a skip
  exits 0 — which AC 4 exists to make visible.
- **[Cryptographic primitives]** Not applicable — no randomness, no hashing, no comparison
  against a secret. `dropcapScanner.scan` is `bytes.Contains` over fixed literals; it is a
  leak detector over repo content, not an authentication check, so constant-time comparison
  buys nothing here.
- **[Network & I/O]** Not applicable — no socket, no HTTP, no listener. Input size: the
  whole matched file is read into memory with no cap. Accepted rather than overlooked; the
  input is a git-tracked artifact in this repository, its current size is under 1 KB, and a
  cap would be a bound with no threat behind it. If a future capture family grows large, the
  cap belongs at the writer, not here.
- **[Error messages, logs, telemetry]** No MUST FIX. Covered under § Error handling as a
  table of what each branch may carry. The rule — class names, counts, base names, the glob
  pattern; never bytes, offsets, needles, the scanner or the record — is the one an editor
  under budget pressure is most likely to weaken with a "just for diagnostics" excerpt, and
  it is stated at both new call sites for that reason. No telemetry, no metrics.
- **[Concurrency]** No findings. No locks, no shared mutable state, no goroutines beyond the
  ones `testing` creates for `t.Parallel()`. The one shared value is the `dropcapScanner`,
  read-only after construction with a value receiver on `scan`. The single aliasing hazard
  in the design is called out explicitly and closed: the planted control allocates a fresh
  `[]byte` rather than `append`-ing into the slice `os.ReadFile` returned, which could
  otherwise mutate the bytes the clean scan and the decode read.
- **[Threat model alignment]** The relevant threat is the one this ticket exists for, and it
  is `docs/knowledge/features/e2e-realclaude-ask-user-question-writer-test-go.md`'s: a
  committed capture is permanent, and #1688's whole-stream capture needed three follow-up
  tickets (#1729, #1732, #1733) to redact what it had already swallowed. This slice adds a
  re-scan that runs forever with no credentials, which is the mitigation. **Named as out of
  scope:** the scan covers only this family, so it does not address the five captures
  outside it that carry a `/Users/` occurrence today — widening it would redden on files
  this ticket does not own, and AC 3 says so. Also out of scope, and unchanged by this
  slice: `writeAskQuestionFixture` is bypassable by a caller that marshals and writes the
  record itself, which `ask_user_question_writer_test.go` names as the one failure mode its
  net cannot see. This reader narrows that gap — a bypassing writer's bytes are re-scanned
  here once committed — but does not close it, since it cannot see an artifact that was
  never committed.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-09-01
