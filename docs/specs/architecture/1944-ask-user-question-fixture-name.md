# #1944 — mint and lock the AskUserQuestion capture's fixture name

**Ticket:** [#1944](https://github.com/pyrycode/pyrycode/issues/1944) — `test(realclaude): mint and lock the AskUserQuestion capture's fixture name`
**Size:** S (0 production source files; one new test file plus one map entry)
**Labels:** `enhancement`, `size:s`, `security-sensitive`, `needs-real-claude`

---

## Files to read first

Read these before writing anything. Every entry names the symbol to read, not a
line, so the reference stays true as the files move underneath it.

| Path | Symbol | What to extract |
|---|---|---|
| `internal/e2e/realclaude/initialize_control_names_test.go` | `initControlFixtureName`, `TestInitControlFixtureName_AvoidsCommittedFamiliesAndStaysContained` | **The whole shape of this ticket.** The one-input namer, its doc comment's CONTRACT / LEXICAL-guarantee / TOTALITY paragraphs, and the three subtests. Its arm half — `initControlArmFixtureName`, `initControlArm`, `initControlArms`, `TestInitControlArmFixtureName_AvoidsCommittedNamesStaysDistinctAndContained` — does **not** apply here and must not be copied. |
| `internal/e2e/realclaude/inband_bypass_revoke_names_test.go` | `poolRevokeNamePattern`, `anchorFixtureName`, `setModeFamilyGlob` | The row type and the single anchoring helper, both reused verbatim across the family boundary. Read `anchorFixtureName`'s doc comment for why `underTestdata` is the field that decides whether a row asserts anything at all. |
| `internal/e2e/realclaude/permission_protocol_spike_test.go` | `versionSlug`, `versionSlugSubst`, `captureClaudeVersion`, `writeFixture` | `versionSlug` is the slug helper to reuse — pure, banned nowhere. `captureClaudeVersion` and `writeFixture` are on this file's ban list; read them to see what is being kept out. |
| `internal/e2e/realclaude/ask_user_question_record_test.go` | `askQuestionFixtureRecord`, `askQuestionFullRecord` | The family's naming vocabulary (`askQuestion*`) and the two version fields the writer will eventually mint a name from. **Read `docs/knowledge/features/e2e-realclaude-ask-user-question-record-test-go.md` rather than this file's own comments before reusing its mutant table** — a false "the mutant compiles" claim shipped in the file's doc comment. |
| `internal/e2e/realclaude/offline_exec_ban_test.go` | `finOfflineExecBans`, `TestFinOfflineFilesReachNoExecHelper` | The map is keyed by **filename**; a new offline file with no entry gets no subtest and no enforcement. `"ask_user_question_record_test.go"` sits at the head of the map — that is the entry to copy, and the new one goes beside it. The check parses without `parser.ParseComments`, so it cannot answer itself out of a comment. |
| `internal/e2e/realclaude/permission_protocol_regression_test.go` | `fixtureGlob`, `TestRealClaude_PermissionProtocol_RegressionFixtures` | `"testdata/permission_protocol_v*_*.json"` and the sweep that reads the trailing token as an expected init permission mode. |
| `internal/e2e/realclaude/dropped_line_capture_test.go` | `dropcapFixtureGlob` | `"testdata/dropped_lines_v*.json"`. |
| `internal/e2e/realclaude/initialize_control_compare_test.go` | `initControlArmFixtureGlob` | `"testdata/initialize_control_v*_*.json"` — **the fourth family, and the one #1696's table does not carry.** #1764 added it and it is `filepath.Glob`-swept by that file's cross-arm comparison, which expects exactly three arms that agree. It is foreign to *this* family, so it is a required row here. |
| `docs/knowledge/features/e2e-realclaude-ask-user-question-record-test-go.md` | § *Lessons that outlive this ticket* | The overlay lesson (below), the comment-density floor, and the corrected mutant claim. |
| `docs/knowledge/features/e2e-realclaude-initialize-control-names-test-go.md` | whole file | #1696's own retro on the file being copied. |
| `CODING-STYLE.md` | § *Comments — Citing Other Code* | `make cite-guard` is diff-scoped and has no depth or range exemption. Cite symbols. |

---

## Context

The `AskUserQuestion` capture is being built offline before a live run spends
tokens on it: the record (#1943, merged), **the name (this ticket)**, the writer
(#1941), the live capture (#1938). Nothing in this repo has ever recorded a
*call* to that tool — all eight committed `permission_protocol_*` captures list
the name in their `system`/`init` `tools` array from claude 2.1.143 through
2.1.199 and not one holds a `tool_use` block for it. The name minted here is
where those bytes will land.

Two hazards make a name worth its own lock rather than an `fmt.Sprintf` at a
call site, and both are silent:

1. **`testdata/` is swept by foreign globs.** Seventeen captures are committed
   there today in four glob-swept families. A new capture whose name falls
   inside one of those globs is swept into a regression test asserting about a
   run it never made — or overwritten by the next run of the probe that owns the
   family — **with every test still green**.
2. **The name is joined under a directory the namer never sees.** The version
   token comes out of `claude --version`, so a token carrying a separator mints
   a name that writes somewhere its caller never chose. `versionSlug`'s
   character class is what closes that, and only a test over separator-bearing
   tokens pins the namer to it.

Both are closed by the same two mechanisms `initControlFixtureName` uses: a
literal prefix no input can reach, and `versionSlug` over the one input.

**No ADR.** This is the third instance of an established pattern (#1661, #1696),
not a decision. If the documentation phase wants a cross-family note, the place
for it is the package overview, not `docs/knowledge/decisions/`.

---

## Design

### Deliverables

| File | Change |
|---|---|
| `internal/e2e/realclaude/ask_user_question_names_test.go` | **New.** `//go:build e2e_realclaude`, package `realclaude`. One namer, one token table, one pattern table, one lock test with four subtests. |
| `internal/e2e/realclaude/offline_exec_ban_test.go` | **Modified.** One entry appended to `finOfflineExecBans`. |

Zero production source files. Nothing is created under `testdata/`, and nothing
in this ticket reads that directory.

### 1 — The namer

```go
func askQuestionFixtureName(versionToken string) string
```

Returns `"ask_user_question_v" + versionSlug(versionToken) + ".json"`. One
`fmt.Sprintf`, exactly as `initControlFixtureName` is. Pure: no directory
parameter, no `*testing.T`, no I/O in either direction.

- **`ask_user_question_v` is a literal in the format string and no input can
  reach it.** `filepath.Match` anchors a pattern's literal head at position 0,
  so a name beginning with a head no committed family shares cannot match any of
  the four family globs — their heads are `permission_protocol_v`,
  `dropped_lines_v`, `set_permission_mode_v` and `initialize_control_v`. Do not
  derive the prefix from the argument. This is what makes the collision
  impossible rather than merely unobserved.
- **`ask_user_question_` is already this family's file-level prefix** (see
  `ask_user_question_record_test.go`), and it is the tool's real name, which is
  what a human reading `testdata/` needs. Do not shorten it to `ask_question_`
  to match the Go identifier prefix; the Go identifiers stay `askQuestion*` to
  match `askQuestionFixtureRecord` and its siblings.
- **One parameter.** `askQuestionFullRecord`'s doc comment and #1943's spec both
  forward-reference "#1944's namer assertion … on the tool-name column", which
  reads as a namer over `(versionToken, toolName)`. That prediction was written
  before this ticket was refined and it is **stale**: AC 1 fixes the signature at
  one parameter, and the ticket states this capture has no arm dimension. The
  tool name is a constant for this whole family — interpolating it would put a
  fixed string in the name twice over and buy nothing. Do not build a
  two-input namer, and do not add a tool-name column to the table below. What
  #1943 was reaching for lands with #1941: the *writer* names its file exactly
  what this namer mints.
- **The contract the doc comment must state, because nothing in the signature
  does** — copy `initControlFixtureName`'s three paragraphs and adapt:
  - *CONTRACT for #1941's writer:* the result is always a **single clean path
    component** — it carries no separator and is never `"."` or `".."`, for any
    input. That is what makes `filepath.Join(dir, name)` land in `dir` at the
    call site.
  - *The guarantee is LEXICAL.* It is about the name, not the filesystem: it
    says the minted string is one component, so `Join` cannot walk out of `dir`.
    It says nothing about `dir` itself — pass a directory that is or contains a
    symlink and the write still resolves wherever that symlink points. Choosing
    `dir` stays the caller's responsibility.
  - *Its TOTALITY rests on `versionSlug`'s character class, not on the table.*
    No separator survives `[^a-z0-9._-]+` → `_`, which is why the property holds
    for every string rather than the fifteen sampled. The table is the tripwire;
    the character class is the guarantee.

### 2 — The token table, with a golden `want` column

**One table, four subtests read it.** AC 1 asks for the namer's output to be
*pinned*, which the three subtests copied from #1696 do not do — a namer that
stopped calling `versionSlug` would mint
`ask_user_question_v2.1.239 (Claude Code).json`, a perfectly legal single
component, and every one of those three subtests stays green. So the token table
grows a `want` column and the golden subtest reads it. Keeping it as a column
rather than a second table is deliberate: the load-bearing-row argument gets
written once, and a row added later cannot reach one subtest and miss another.

Row type — function-local, beside the table (a package-level type would widen
this file's package-scope surface, which is what keeps concurrent siblings from
colliding with it):

```go
type askQuestionNameRow struct {
	token string
	want  string
}
```

The table, computed against `versionSlug` on 2026-09-01 — **these `want` values
are the contract; do not re-derive them by running the namer and pasting what it
printed**:

| # | `token` | `want` | Why the row is here |
|---|---|---|---|
| 1 | `2.1.239` | `ask_user_question_v2.1.239.json` | The plausible current version. Already slug-clean, so it gives the golden subtest nothing — the baseline row. |
| 2 | `2.1.239 (Claude Code)` | `ask_user_question_v2.1.239_claude_code_.json` | **Load-bearing, golden subtest.** The raw `claude --version` LINE shape: space, parens, uppercase. With row 15 it is one of only two rows that redden a namer which stops slugging *and* that no other subtest can see — the separator rows redden containment too, so the golden subtest is not their sole red. |
| 3 | `2.1.239-fixture` | `ask_user_question_v2.1.239-fixture.json` | Slug-clean by construction, and the shape #1941 passes if it mints from the record's `ClaudeVersionSlug`. Documents the fixed point; 0-red on the golden subtest, and say so in the comment rather than letting a reader credit it. |
| 4 | `2_1_220` | `ask_user_question_v2_1_220.json` | Underscores survive the class. |
| 5 | `2.1.220-beta.1` | `ask_user_question_v2.1.220-beta.1.json` | Dots and hyphens survive the class. |
| 6 | `permission_protocol_v1_x` | `ask_user_question_vpermission_protocol_v1_x.json` | **Load-bearing, family subtest.** See below. |
| 7 | `set_permission_mode_v1_x` | `ask_user_question_vset_permission_mode_v1_x.json` | **Load-bearing, family subtest.** |
| 8 | `dropped_lines_v1` | `ask_user_question_vdropped_lines_v1.json` | **Load-bearing, family subtest.** |
| 9 | `initialize_control_v1_x` | `ask_user_question_vinitialize_control_v1_x.json` | **Load-bearing, family subtest.** |
| 10 | `..` | `ask_user_question_v...json` | The `"."` / `".."` clause. Note it is a perfectly clean component — do **not** credit this row with catching a mis-slugged token. |
| 11 | `../..` | `ask_user_question_v.._...json` | **Load-bearing, containment subtest.** |
| 12 | `a/b` | `ask_user_question_va_b.json` | **Load-bearing, containment subtest.** |
| 13 | `/abs` | `ask_user_question_v_abs.json` | **Load-bearing, containment subtest.** |
| 14 | `""` (empty) | `ask_user_question_v.json` | The empty input. |
| 15 | `strings.Repeat("9", 64)` | `ask_user_question_v` + thirty-two `9`s + `.json` | **Load-bearing, golden subtest** — the 32-byte truncation path, and the second of the two rows that redden a no-slug namer with no other subtest seeing it. Write the `want` as a `strings.Repeat("9", 32)` expression rather than a literal run of digits — a hand-typed run of 32 identical characters is unreviewable. |

**Rows 6–9 replace #1696's bare family heads, and the substitution is not
cosmetic.** #1712 measured, with `filepath.Match` semantics, that a bare head
like `set_permission_mode` mints a name matching **nothing** under the realistic
mutant, because `set_permission_mode_v*_*.json` needs a literal `v` after the
head and the bare head never supplies it. The realistic mutant for a one-input
namer is a prefix derived from the token — `fmt.Sprintf("%s.json",
versionSlug(token))` — and under it these four rows mint
`permission_protocol_v1_x.json`, `set_permission_mode_v1_x.json`,
`dropped_lines_v1.json` and `initialize_control_v1_x.json`. Measured against
`filepath.Match` on 2026-09-01, over all fifteen rows × all four patterns: under
that mutant each of these four rows reddens **exactly one** glob and no other row
reddens any, so the mapping row→family is 1:1 and every row is load-bearing.
The `*_*` globs need the second `_`, which is why three of the four carry a
trailing `_x` and `dropped_lines_v1` does not. **Do not "harmonise" these back
to bare heads** — that silently empties the family subtest of its only mutant.

Measured in the same run: under the real namer, zero of the fifteen rows match
any of the four globs, and zero escape containment.

### 3 — The pattern table: four rows, not three

Reuse `poolRevokeNamePattern` and `anchorFixtureName` rather than restating
them. A parallel row type would mean a second anchoring path, and a second
anchoring site is how a row goes silently vacuous. Function-local, as it is in
both sibling files.

| `glob` | `underTestdata` | `controls` (synthetic literals) | `hazard` — what a match would cost |
|---|---|---|---|
| `fixtureGlob` | `true` | `permission_protocol_v0.0.0_default.json` | `TestRealClaude_PermissionProtocol_RegressionFixtures` sweeps that glob and reads the trailing token as the expected init permission mode, so the capture would be swept in and asserted about a different argv |
| `dropcapFixtureGlob` | `true` | `dropped_lines_v0.0.0.json` | the dropped-line capture's fixture sweep would read an `AskUserQuestion` call as one of its own captures |
| `setModeFamilyGlob` | `false` | `set_permission_mode_v0.0.0_default.json` | that is #1595's committed record of the in-band revocation wire format, and a live #1938 run writing there overwrites it while every test stays green |
| `initControlArmFixtureGlob` | `true` | `initialize_control_v0.0.0_before_first_turn.json` | #1764's cross-arm comparison `filepath.Glob`s that pattern and expects exactly three arms that agree, so the capture would be read as a fourth arm of a measurement it took no part in |

**The fourth row is required by AC 3 and is the one thing this file does not
inherit from #1696.** `initialize_control` is a committed family — four captures
under `testdata/` — and its glob is foreign to *this* family, so AC 3's "the
glob of a capture family already committed under
`internal/e2e/realclaude/testdata/`" covers it. #1696 omits it only because
#1764 did not exist yet, and #1712's own table omits it for the opposite reason:
there it is the family's *own* glob, which every name it mints matches on
purpose. Neither reason applies here.

**`underTestdata` is per-row and is what decides whether a row asserts
anything.** Three of the four globs carry a `testdata/` prefix and
`setModeFamilyGlob` does not, because that is how their owning tests evaluate
them. Anchor either one against the other's shape and it is false for every
input, the negative assertion passes unconditionally, and the file locks
nothing. `anchorFixtureName` is the single place anchoring happens, and **both**
the negative assertions and the controls go through it: flip a row's
`underTestdata` and that row's negative assertion goes vacuous **and** its
control reddens, in the same edit. Neither half can rot alone. Controls must not
inline their own anchoring.

**Controls are synthetic literals — never a committed filename, never a
directory listing.** A control lifted off the real directory is red on arrival:
`permission_protocol_v2.1.158.json` is committed right now and does **not** match
`fixtureGlob`, having no second `_`. And a control written as a real capture
reddens spuriously the day that capture is retaken at a new version, where the
cheap repair for a spurious red is to weaken the control. `v0.0.0` is not a
version any committed capture carries.

### 4 — The lock test

One `t.Parallel()` test, `TestAskQuestionFixtureName_AvoidsCommittedFamiliesAndStaysContained`,
with four `t.Parallel()` subtests. The parent builds the two tables once; the
subtests only read them, so the shared slices are safe.

Scenarios, as behaviour rather than code:

- **`each token mints exactly the pinned name`** — for every row, assert
  `askQuestionFixtureName(row.token) == row.want`. The subtest that catches a
  namer which stops calling `versionSlug`; measured, it reddens on rows 2, 11,
  12, 13 and 15, and rows **2 and 15** are the two where it is the *sole* red.
  The failure message names the token, the got and the want, and says that
  #1941's writer and #1938's live run both derive their path from this function.
- **`no minted name joins a committed family`** — for every row × every pattern,
  `filepath.Match(p.glob, anchorFixtureName(p, name))` must be false. A
  `filepath.Match` error is `t.Fatalf`, not `t.Errorf`: a malformed pattern
  constant makes every comparison in the file meaningless. The failure message
  interpolates the row's hazard.
- **`each pattern's control can still match`** — for every pattern, fail loudly
  if `len(p.controls) == 0`, then for every control assert the same
  `filepath.Match` through the same `anchorFixtureName` is **true**. This is
  AC 3's non-vacuity half: a pattern anchored against a string it can never
  match is false for every input, so the subtest above would pass over it having
  proven nothing.
- **`every minted name stays directly inside its target directory`** — with
  `const targetDir = "/ask-user-question/fixtures"`, an arbitrary **clean**
  absolute literal (a trailing slash would make `filepath.Dir` return the
  cleaned form and redden a healthy name), assert
  `filepath.Dir(filepath.Join(targetDir, name)) == targetDir` for every row, and
  additionally that the name is never `"."` or `".."`. Nothing resolves the
  package's real `testdata/`; the namer takes no directory and the directory
  needs to exist no more than the fixture does.

  The second assertion is **strictly implied** by the first — base `"."` joins to
  `targetDir` whose `Dir` is `/ask-user-question`, and `".."` joins to
  `/ask-user-question` whose `Dir` is `/` — so it can never be the sole red.
  Keep it as a prefix pin (a name that *is* `"."` or `".."` has no
  `ask_user_question_v` prefix left) and say in the comment that it does **not**
  catch a mis-slugged token: measured, `".."` interpolated raw mints
  `ask_user_question_v...json`, a perfectly clean component, and the assertion
  stays green.

### 5 — The `finOfflineExecBans` entry

Add `"ask_user_question_names_test.go"` **beside** `"ask_user_question_record_test.go"`
at the head of `finOfflineExecBans`, carrying the identical seventeen names.
Append the entry rather than reflowing the map.

```
"resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
"probeClaudeVersion", "captureClaudeVersion",
"os.Getenv", "os.Environ", "os.LookupEnv",
"packageDir", "setModeFixturePath", "writeSetModeFixture", "writeFixture",
"filepath.Glob", "os.ReadFile", "os.WriteFile", "os.Create", "os.ReadDir",
```

- **Copy the set whole; do not hand-pick.** The check is an AST identifier
  match, so banning `packageDir` while leaving its wrappers `setModeFixturePath`,
  `writeSetModeFixture` and `writeFixture` unlisted leaves the ban true and the
  property false.
- The first five keep a SKIP out. `resolveClaudeBin` and
  `WithWorktreeAuthenticated` skip *inside* the test body, after `=== RUN` is
  printed, and a skip exits 0 — which reads as a pass under `make
  e2e-realclaude`.
- `captureClaudeVersion` is the one a developer here is most likely to reach
  for: it is the package's own `claude --version` exec and it returns exactly
  the token this namer takes. The token arrives as a *parameter*; the live run
  supplies it.
- The three environment readers are the credential guard — this process
  environment carries `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY`.
- The `packageDir` group plus `filepath.Glob` and the four `os` read/write names
  are what keep this file's controls synthetic literals rather than a directory
  listing, and they are not tidiness: `go test` runs in the package source
  directory, so a **relative** `os.WriteFile("testdata/…")` reaches the
  seventeen committed captures without naming any wrapper at all.
- `t.TempDir` is deliberately absent: this file writes nothing and needs no
  directory. `versionSlug`, `filepath.Match`, `filepath.Join`, `filepath.Dir`,
  `anchorFixtureName` and `strings.Repeat` are all pure and are banned nowhere;
  this file calls them.
- **The limit, stated so nobody over-reads the ban:** the check is per-file
  *syntax*, not a call graph. A banned read stays reachable through a helper the
  file calls while the ban stays green.

### 6 — The header comment

This family's comment density does not scale down with what a file holds
(#1943's measured lesson), and the header is where the correctness argument
lives. State each of these once and **cross-reference the sibling by symbol
name** rather than re-deriving it:

1. What is being fenced off — the two silent hazards from § Context.
2. The four foreign globs and why the literal prefix closes all four.
3. Why every pattern carries a control, and why `anchorFixtureName` is the
   single anchoring site.
4. Why the golden `want` column exists — the three inherited subtests cannot see
   a namer that stops slugging on a non-separator token.
5. The offline property, in full: this file reaches no live claude, no daemon,
   no subprocess, no credential and no directory, and
   `TestFinOfflineFilesReachNoExecHelper` enforces it over the file's AST rather
   than over the paragraph — it parses without `parser.ParseComments`, so the
   check cannot answer itself out of the header that states it.
6. The runnable command and how to read its result:

   ```
   go test -tags e2e_realclaude -race -count=1 -v \
     -run 'TestAskQuestionFixtureName_|TestFinOfflineFilesReachNoExecHelper' \
     ./internal/e2e/realclaude/
   ```

   Both must report PASS — not SKIP, not "no tests to run" — on a machine with
   no claude and no credentials. **Read the count of tests that executed, never
   the exit code:** this package is behind the `e2e_realclaude` tag, `make check`
   never compiles it, and the suite exits 0 both on a build failure and on a full
   credentials skip.

---

## Concurrency model

No goroutines, no channels, no context. The test and its four subtests all call
`t.Parallel()`; the parent computes both tables before the subtests start and
nothing writes to either afterwards, so `-race` has nothing to find.

The one live concurrency constraint is inherited: `initControlArms` and
`poolRevokeArms` are read-only tables ranged from `t.Parallel()` tests in
several files. This ticket ranges neither, and it declares no table of its own
that another file reads.

---

## Error handling

There is no error path in production code here — the namer cannot fail. What
matters is the *failure message*, which is read once, by somebody about to spend
tokens on a live run:

- `filepath.Match` returning an error is `t.Fatalf`, everywhere. A malformed
  pattern constant makes every comparison in the file meaningless, and a
  `t.Errorf` there would let the rest of the run report green-ish noise.
- Every other assertion is `t.Errorf`, so one bad row does not hide the others.
- A family-glob failure interpolates the row's `hazard` string. A containment
  failure names the token, the minted name, the target directory and where it
  actually landed, and says that #1941's writer joins this name under a real
  directory.
- No message interpolates anything read from the environment or from disk —
  there is nothing to read.

---

## Testing strategy

`make preship` is the gate. `make check` never compiles this package, so a
package that fails to build exits 0 through a shell wrapper with zero tests run;
read the count of `=== RUN` lines, not the exit code.

### Mutants each subtest must be the sole red for

Run each against the real worktree and record the result. `go test -overlay` is
fine for all four of these (they are ordinary package code); it is **not** fine
for the ban entry — see below.

| Mutant | Expected sole red |
|---|---|
| `askQuestionFixtureName` interpolates its token raw (drops `versionSlug`), staying pure and keeping the literal prefix | `each token mints exactly the pinned name` on rows 2, 11, 12, 13 and 15 — **sole** red on 2 and 15 — **and** `every minted name stays directly inside its target directory` on rows 11–13. Measured 2026-09-01: the family subtest stays green on all fifteen rows, because the prefix keeps the name out of every family whether or not the token was slugged. |
| The prefix is derived from the argument: `fmt.Sprintf("%s.json", versionSlug(token))` | `no minted name joins a committed family`, one row per glob — row 6→`fixtureGlob`, 7→`setModeFamilyGlob`, 8→`dropcapFixtureGlob`, 9→`initControlArmFixtureGlob` — plus the golden subtest on every row. |
| The prefix literal is changed to `dropped_lines_v` | `no minted name joins a committed family` on every row, plus the golden subtest. |
| One pattern row's `underTestdata` is flipped | `each pattern's control can still match` for that row — and this is the point of routing controls through `anchorFixtureName`: the negative assertion for that row goes vacuous in the same edit, and the control is what reports it. |
| A pattern row's `controls` emptied | `each pattern's control can still match`, via the explicit `len(p.controls) == 0` branch. |

### Proving the `finOfflineExecBans` entry non-vacuous

**Do not use `go test -overlay` for this, and do not report an overlay result as
evidence.** `TestFinOfflineFilesReachNoExecHelper` reads its subject off disk —
`parser.ParseFile` on a relative filename — which a build overlay does not
intercept. An overlay mutant of the new file exercises nothing at all and comes
back green. #1943 measured exactly this; the note in
`docs/knowledge/features/e2e-realclaude-ask-user-question-record-test-go.md`
records it.

The only way to prove the entry: write one banned call (e.g. a
`captureClaudeVersion(t)` line, or an `os.ReadDir("testdata")`) into the **real
worktree file**, run

```
go test -tags e2e_realclaude -count=1 -run 'TestFinOfflineFilesReachNoExecHelper' ./internal/e2e/realclaude/
```

observe the `ask_user_question_names_test.go` subtest red, then revert. Confirm
the subtest exists and runs at all — a missing map key produces no subtest and
no failure, which is indistinguishable from a pass in the summary line.

### What is deliberately not tested

- **Nothing reads `testdata/`.** The controls are synthetic literals precisely
  so that the file never needs to.
- **No live claude, no writer, no filesystem write, no record type.** Those are
  #1941 and #1938.
- **`versionSlug` itself is not re-tested here.** It is a shipped, pure helper;
  this file's tables pin the *coupling* to it, which is the thing that can break.

---

## Scope check (re-applied against this spec, per § 4)

| Boundary | Limit | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **0** — both files are `*_test.go` |
| Total written work | ≤ 400 lines | **~370** (see below) |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — nothing calls the namer yet; #1941 is its first consumer |
| Acceptance criteria | ≤ 5 | **4** |
| Distinct error/reject branches in a state machine | ≤ 10 | **0** |

**The line budget, sized against the nearest analogue by shape.** #1696
(`f12af96b`) shipped this exact scope — one-input namer, token table,
containment, non-collision, plus its bans entry — in **332 lines** (291 in the
new file, 41 in `offline_exec_ban_test.go`). This ticket adds two things #1696
does not have: the golden `want` column and its subtest (~35 lines), and the
fourth pattern row (~10 lines). That puts the file at **~340** and the bans
entry at **~25–35**, for **~370 total**.

Two guardrails on that number, because it has real variance above it:

- **Do not size this as a fraction of #1696 because the arm dimension is
  gone.** #1943 just measured that this family's comment density does not scale
  down with what a file holds; its 4-field record overran its spec's budget by
  42%. The budget above scales #1696 *up*, which is the correct direction.
- **The saving that is legitimate is not writing the same argument twice.** Both
  sibling name-lock files re-derive the anchoring argument and the
  `filepath.Match` head-anchoring argument from scratch. State each once here and
  cross-reference the sibling by symbol name. That is better prose, not thinner
  prose, and it is where the ~40-line gap between #1696's 291 and this file's
  ~340 target is meant to come from without the file growing past it.

The nearer analogue by *shape* is #1696, not #1943 (`1c16547d`, 567 lines) — that
one built a struct, a fixture literal, a field listing, a round trip and a
wire-order property, none of which land here. If the file does overrun past 400,
that is a data point for the documentation phase, not a mid-flight re-scope.

## File-overlap check

`git fetch origin --prune` on 2026-09-01, then every `origin/feature/<n>` branch
diffed against `origin/main` for `ask_user_question_names_test.go` and
`offline_exec_ban_test.go`. **No overlap.** No `blockedBy` needed.

---

## Open questions

1. **Which of the record's two version fields does #1941's writer pass?**
   `askQuestionFixtureRecord` carries both `ClaudeVersionRaw`
   (`"2.1.239-FIXTURE (Claude Code)"`) and `ClaudeVersionSlug`
   (`"2.1.239-fixture"`). The namer slugs whatever it is given, so **both are
   safe**: the slug field is a fixed point (row 3 pins it), the raw line is
   rewritten (row 2 pins it). #1943's spec flagged this as blunting one column of
   a "named exactly what the namer mints" assertion; that assertion belongs to
   #1941, and the resolution there is to assert over `ClaudeVersionRaw`, which is
   not slug-clean. Nothing in *this* ticket needs the question answered.
2. **This family gets no glob of its own here, deliberately.** #1764 added
   `initControlArmFixtureGlob` for the sibling family only once a sweep needed
   it. If #1938's capture grows past one file, the glob and its sweep belong
   with that ticket — and at that point the *other* families' name locks should
   grow a row for it, exactly as this spec grows one for `initialize_control`.
3. **Is `2.1.239` still the current claude version at capture time?** Irrelevant
   to this ticket — the token is a parameter and every row here is synthetic —
   but #1938 will mint against whatever `captureClaudeVersion` returns, and no
   assertion here constrains that.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** The one boundary in this design is
  `askQuestionFixtureName`'s parameter: a version token that will, at #1938's
  live run, originate from `captureClaudeVersion`'s exec of `claude --version` —
  i.e. from subprocess stdout, which is untrusted input crossing into a value
  used to build a filesystem path. The boundary is **explicit and single**:
  `versionSlug` is the only sanitiser and it is applied inside the namer, not at
  any call site, which is what AC 1's "nothing formats that name at a call site"
  buys. Downstream callers hold a value the namer's doc comment declares to be a
  single clean path component. **No finding**, but the boundary must be stated
  in the namer's doc comment as specified in § Design 1 — the type system says
  `string` on both sides and carries no signal.
- **[File operations — path traversal]** This is the category the ticket exists
  for, and it is the one real attack surface. A `claude --version` line
  containing `/`, `..`, or a leading `/` would, under a namer that interpolated
  it raw, produce a path escaping the directory `filepath.Join` puts it under.
  The design closes it in two independent places: `versionSlug`'s
  `[^a-z0-9._-]+` → `_` class (the guarantee, total over all strings) and the
  containment subtest over rows 11–13 (the tripwire). Measured 2026-09-01
  against a raw-interpolating namer: rows 11–13 are the *only* rows that redden
  containment, so dropping any one of them narrows the traversal tripwire and
  dropping all three removes it while every other subtest stays green. That is
  why the spec marks them load-bearing and forbids dropping them.
  **SHOULD FIX, and already written into the spec:** the guarantee is *lexical*.
  It bounds the name, not the directory — a `dir` that is or contains a symlink
  still resolves wherever that symlink points, and nothing here uses
  `O_NOFOLLOW` or an equivalent. That is correct scoping (the namer takes no
  directory), but it must be stated in the doc comment so #1941's writer does
  not inherit a guarantee it was never given. § Design 1 requires it verbatim.
- **[File operations — overwrite of committed evidence]** The second real
  hazard, and it is a *durable-evidence* attack rather than a memory-safety one:
  a name landing inside a foreign family's glob is swept into a test asserting
  about a different run, or overwritten by the probe that owns the family, with
  no red anywhere. Closed by the literal `ask_user_question_v` prefix, which no
  input can reach, plus the four-row pattern table. **The fourth row
  (`initControlArmFixtureGlob`) is this pass's one substantive addition to
  #1696's design** — omitting it would have left the `initialize_control`
  family's four committed captures unprotected by this lock while the file
  claimed to cover "committed families".
- **[File operations — TOCTOU, permissions, atomic writes]** Not applicable:
  this ticket performs **no filesystem operation at all**, in either direction.
  There is no `os.Stat`, no `os.Open`, no create, no rename, and no file mode to
  choose. Those questions belong to #1941's writer, whose AC 2 already requires
  refusal *before* the first filesystem call and whose model
  (`writeInitControlFixture`) does `MkdirAll` → `.tmp` → rename. Enforced here
  structurally rather than by prose: `os.Create`, `os.WriteFile`, `os.ReadFile`,
  `os.ReadDir`, `filepath.Glob`, `packageDir` and its three wrappers are all in
  this file's `finOfflineExecBans` entry, checked over the file's AST.
- **[Subprocess execution]** Not applicable by construction, and enforced:
  `resolveClaudeBin`, `probeClaudeVersion`, `captureClaudeVersion`,
  `WithWorktree` and `WithWorktreeAuthenticated` are all banned for this file.
  Nothing here is passed to `exec.Command`, and no `sh -c` exists anywhere in
  the design. The version token arrives as a parameter precisely so this slice
  never execs.
- **[Tokens, secrets, credentials]** No token is generated, stored, rotated or
  revoked here. The relevant risk is the *opposite* direction — a credential
  leaking **in** — and it is real: this process environment carries
  `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY`, and #1941's ticket measured
  that thirteen of the seventeen committed captures already carry a deny-class
  operator value. Closed structurally: `os.Getenv`, `os.Environ` and
  `os.LookupEnv` are banned for this file, so no table row and no failure message
  can be sourced from the environment. Every value in both tables is a synthetic
  literal. **No finding.**
- **[Error messages, logs, telemetry]** Failure messages interpolate only the
  table's own synthetic literals, the minted name, the glob constants and the
  hard-coded `targetDir`. Nothing reaches the environment, the real `testdata/`
  or a real version line, so no message can leak a credential or an
  operator-machine path. The one thing to watch: a developer "improving" a
  message by adding `packageDir(t)` for context would leak the operator's
  absolute path into test output *and* break the offline property — the ban
  entry reddens on it. **No finding.**
- **[Cryptographic primitives]** Not applicable. No randomness, no hashing, no
  comparison against a secret. `versionSlug` is a regexp substitution plus a
  byte truncation; the 32-byte cap is a filename-length concern, not a security
  one, and truncation collisions between two version tokens are a
  durable-evidence question already owned by the sibling families' distinctness
  subtests — which this capture, having one dimension, does not need.
- **[Network & I/O]** Not applicable. No socket, no HTTP server, no reader, no
  deadline, no size cap — the ticket has no I/O of any kind.
- **[Concurrency]** No goroutines, no locks, no shared mutable state. Both
  tables are built by the parent before any subtest starts and are read-only
  afterwards, so there is no check-then-mutate and no lock-ordering question.
  Nothing here ranges the read-only tables other files share (`initControlArms`,
  `poolRevokeArms`), so the -race hazard those carry is not inherited. **No
  finding.**
- **[Threat model alignment]** No relay or mobile-protocol surface, so
  `docs/protocol-mobile.md` § Security model does not apply. The threat this
  ticket *is* aligned against is the one #1941's body names and this family has
  measured: operator-machine values and credentials reaching a committed
  artifact. This slice's contribution is upstream of that — it fixes where the
  bytes land — and the scan that decides *whether* they are written is #1941's,
  explicitly out of scope here.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-09-01
