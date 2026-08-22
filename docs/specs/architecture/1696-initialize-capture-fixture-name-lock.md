# #1696 — Mint and lock the `initialize` capture's fixture name

Test-only. Two files, both `*_test.go`, both in `internal/e2e/realclaude`. No production
code in this slice.

## Files to read first

Everything below is in `internal/e2e/realclaude` unless stated otherwise. Resolve each
symbol with `codegraph_node` / `codegraph_search` and read the enclosing declaration —
no line numbers, they rot within a ticket's lifetime and `make cite-guard` rejects them
in comments.

| Where | Symbol | What to extract |
| --- | --- | --- |
| `inband_bypass_revoke_names_test.go` | `poolRevokeFixtureName` | **The model for this whole ticket.** #1661 did this job for #1643's family. Read its doc comment for the "literal prefix no input can reach" argument you are restating for one input instead of two. |
| `inband_bypass_revoke_names_test.go` | `poolRevokeNamePattern` | The row type you reuse verbatim — `glob` / `underTestdata` / `controls` / `hazard`. Do **not** declare a parallel type. |
| `inband_bypass_revoke_names_test.go` | `anchorFixtureName` | The single anchoring decision in the package. Both your negatives and your controls call it. |
| `inband_bypass_revoke_names_test.go` | `TestPoolRevokeFixtureName_AvoidsCommittedFamiliesAndStaysContained` | The three-subtest shape you mirror. Note how its redundant `setModeFixtureName` equality loop carries an explicit "cannot be the sole red, kept for the failure message" comment — you need the same treatment for the `.`/`..` half of AC 3. |
| `inband_bypass_revoke_names_test.go` | the file header (top-of-file comment block) | The prose idiom for these lock files: what is fenced off, why one-directional absence proves nothing, the exact `go test` invocation. Yours restates it for one input dimension. |
| `permission_protocol_spike_test.go` | `versionSlug`, `versionSlugSubst` | Lowercase → `[^a-z0-9._-]+` → `_`, then a 32-**byte** cap. Confirm for yourself that `.` and `-` survive it, so `..` slugs to `..`. |
| `permission_protocol_regression_test.go` | `fixtureGlob`, `TestRealClaude_PermissionProtocol_RegressionFixtures` | The glob's value **and** the fact that its owning test evaluates it via `filepath.Glob` against package-relative paths — that is why it anchors under `testdata/`. |
| `dropped_line_capture_test.go` | `dropcapFixtureGlob` (in the `dropcapTicket` const block) | Same: `testdata/`-prefixed. Also read the block's header note on why every file-local identifier takes a file-local prefix. |
| `inband_bypass_revoke_names_test.go` | `setModeFamilyGlob` | Already package-level, base-names-only, **no** `testdata/` prefix. Do not redeclare it. |
| `set_permission_mode_probe_test.go` | `setModeFixtureName`, `setModeFixturePath`, `writeSetModeFixture` | Only so you recognise them as things this file must not reach. You do not call any of them. |
| `offline_exec_ban_test.go` | `finOfflineExecBans`, `TestFinOfflineFilesReachNoExecHelper` | The map you add one entry to, and the AST check that enforces it. Read `inband_bypass_revoke_names_test.go`'s and `inband_bypass_revoke_fixture_test.go`'s entries — yours is the first plus `writeFixture`. |
| `permission_protocol_spike_test.go` | `writeFixture`, `packageDir` | The third `packageDir` wrapper. Verified today: `setModeFixturePath`, `writeSetModeFixture`, `writeFixture` is the complete wrapper set. |
| `docs/knowledge/features/permission-protocol-spike.md` | § fixture naming | Background on why this package commits version-stamped fixtures at all. Read-only; the documentation phase owns it. |

`CODING-STYLE.md` § "Comments — Citing Other Code" is binding on every comment you write
in this ticket. Name symbols. `make cite-guard` is diff-scoped and has no depth exemption
and no range exemption.

## Context

The daemon wants to publish claude's model list to clients. Measured by hand on
2026-08-21 against claude 2.1.220, **outside this repo**: a `control_request` with
subtype `initialize`, written on the child's already-held-open stdin, comes back with a
`models` array. #1688 is the live run that spends tokens to put that round trip under
test and commit the bytes. Nothing in the tree yet records the request line or the
response shape.

This ticket mints the filename those bytes will land under, and proves — with no claude
binary, no credentials and no disk I/O in either direction — that the name can never
join a committed fixture family.

`testdata/` is swept by three globs owned by three different probes:

| constant | value | anchoring |
| --- | --- | --- |
| `fixtureGlob` | `testdata/permission_protocol_v*_*.json` | `testdata/`-prefixed |
| `dropcapFixtureGlob` | `testdata/dropped_lines_v*.json` | `testdata/`-prefixed |
| `setModeFamilyGlob` | `set_permission_mode_v*_*.json` | base names, no prefix |

A capture whose name landed in one of those families gets swept into a regression test
asserting findings about a **different argv**, or gets overwritten by the next run of the
probe that owns the family. Either failure is durable-evidence loss **with no red
anywhere**. That is why it gets a deterministic lock rather than a convention, and why
the lock lands before the live run exists that could trip it.

This is the third instance of the pattern (#1595's namer, #1661's, now this). It does not
warrant an ADR — the argument is already carried by #1661's file header and by the
per-namer doc comments. If the documentation phase wants a cross-cutting note, the
package overview is the place, not a new decision record.

## Design

### Deliverable 1 — `internal/e2e/realclaude/initialize_control_names_test.go` (new)

Build tag `//go:build e2e_realclaude`, package `realclaude`. Imports: `fmt`,
`path/filepath`, `strings`, `testing`. Nothing else — in particular no `os`.

**File-local identifier prefix: `initControl`.** Siblings add files to this package
concurrently and the branch-overlap check does *not* catch a same-package identifier
collision — it surfaces only once both branches are on `main`. Verified today: nothing in
the package starts with `initControl` (`initModels`, `initModes`, `initMsg`, `initPriv`,
`initRecv`, `initSend`, `initBody`, `initEvent`, `initial`, `initiator` all exist and none
collide). Only **two** new package-scope identifiers are introduced; everything else is
function-local.

#### The namer

```go
func initControlFixtureName(versionToken string) string
```

One input, not two. #1643 had three arms so `poolRevokeFixtureName` carries an arm
parameter; this capture has one probe, so the name is `initialize_control_v<slug>.json`
and the lock table is tokens × 1. Body is a single `fmt.Sprintf` with the
`initialize_control_v` prefix as a **literal inside the format string** and the sole
argument passed through `versionSlug`.

Two properties the doc comment must state, because nothing in the signature does:

- **The literal prefix is the entire mechanism.** `filepath.Match` anchors a pattern's
  literal head at position 0, so a name beginning `initialize_control_v` cannot match any
  of the three globs — their heads are `set_permission_mode_v`, `permission_protocol_v`,
  `dropped_lines_v`. Do not derive the prefix from the argument.
- **CONTRACT for #1697's writer:** the result is always a single clean path component —
  no separator, never `.` or `..`, for any input. That is what makes
  `filepath.Join(dir, name)` land inside `dir` at the call site. The containment subtest
  is what proves it.
- **The guarantee is lexical and it is about the name, not about the filesystem.** It says
  the minted string is one component, so `Join` cannot walk out of `dir`. It says nothing
  about `dir` itself: if the caller passes a directory that is or contains a symlink, the
  write still resolves wherever that symlink points. Choosing `dir` is the caller's
  responsibility and stays #1697's. State this in the doc comment so the contract is not
  read as a filesystem-level containment guarantee it does not provide.
- **Totality rests on `versionSlug`'s character class, not on the token table.** No `/`
  can survive `[^a-z0-9._-]+ → _`, which is why the property holds for *every* string and
  not merely the thirteen sampled. The table's job is to catch the coupling breaking —
  and it does: widening that class to admit a separator reddens the containment subtest on
  the three `/`-bearing tokens. Say both halves in the comment, so nobody reads the table
  as the guarantee.

#### The pattern table

Function-local `[]poolRevokeNamePattern` inside the test, three rows. The type is #1661's
and is generic over the family despite its name; say so in a one-line comment rather than
renaming it — a rename touches #1661's file, which is not this ticket's deliverable.

Unlike #1661 the table could be a package-level literal (all three controls are literals
here, none are generated). Keep it function-local anyway: it is used by exactly one test,
and it keeps the new package-scope surface at two identifiers.

| `glob` | `underTestdata` | `controls` (one synthetic literal each) |
| --- | --- | --- |
| `setModeFamilyGlob` | `false` | `set_permission_mode_v0.0.0_default.json` |
| `fixtureGlob` | `true` | `permission_protocol_v0.0.0_default.json` |
| `dropcapFixtureGlob` | `true` | `dropped_lines_v0.0.0.json` |

Each row's `hazard` names, in one sentence, what a match would cost — that string is read
once, by somebody about to spend tokens on a live run.

**Controls are synthetic literals, never a directory listing and never a cross-product.**
Two independent reasons, and the second is true *today*:

1. A control written as a committed filename reddens spuriously the day that capture is
   retaken at a new version, and the cheap repair for a spurious red is to weaken the
   control. Worse than rot.
2. `permission_protocol_v2.1.158.json` and `permission_protocol_v2.1.199.json` are both
   committed right now and **neither matches `fixtureGlob`** — no second `_`. A control
   lifted off the real directory is red on arrival.

Do **not** copy #1661's `familyControls` construction, which ranges `setModeArms` to build
a token × arm cross-product. That dimension does not exist here, and rebuilding it is most
of the size difference between this ticket and #1661.

#### The token table

Function-local `tokens []string`, built from #1661's list with the separator-bearing
shapes **added, not substituted**. Minimum contents and why each row is there:

| token | why |
| --- | --- |
| `2.1.220` | what `claude --version` emits today |
| `2.1.220 (Claude Code)` | the raw, unsplit `--version` line |
| `2_1_220` | already-underscored |
| `2.1.220-beta.1` | `-` and `.` survive `versionSlug` |
| `permission_protocol` | shaped to smuggle a name into `fixtureGlob`'s family |
| `set_permission_mode` | ditto, `setModeFamilyGlob` |
| `dropped_lines` | ditto, `dropcapFixtureGlob` |
| `..` | traversal, no separator — slugs to `..` unchanged |
| `../..` | **separator-bearing**, traversal shape |
| `a/b` | **separator-bearing**, interior separator |
| `/abs` | **separator-bearing**, leading separator |
| `""` | empty |
| `strings.Repeat("9", 64)` | exercises the 32-byte cap |

**The three `/`-bearing rows are load-bearing and must not be dropped.** See § "Why the
separator-bearing tokens are the only thing this file measures" below — without them the
entire file goes green under a namer that never calls `versionSlug`.

#### The three subtests

All `t.Parallel()`, at both the parent and the subtest level, matching `poolRevokeFixtureName`'s test.

1. **`no minted name joins a committed family`** — for every token, mint the base name,
   and for every pattern evaluate `filepath.Match(p.glob, anchorFixtureName(p, base))`.
   `t.Fatalf` on a non-nil `err` (a malformed pattern constant makes every comparison in
   the file meaningless); `t.Errorf` naming token, minted name, glob and `hazard` on a
   match. No equivalent of #1661's `setModeFixtureName` equality loop — that loop existed
   because #1661's arms collided by string with #1595's, which has no analogue here.

2. **`each pattern's control can still match`** — for every pattern, fail loudly if
   `len(p.controls) == 0`, then assert every control matches, **anchored through
   `anchorFixtureName`**. The failure message must say what a non-matching control means:
   the pattern is anchored against a string shape it can never match, so subtest 1 passed
   unconditionally and the file locks nothing.

3. **`every minted name stays directly inside its target directory`** — an arbitrary
   *clean* absolute literal `const targetDir = "/initialize-control/fixtures"` (no
   trailing slash: `filepath.Join` cleans its result, and a trailing slash makes
   `filepath.Dir` return the cleaned form and the comparison fail on a healthy name).
   Nothing resolves the package's real `testdata/` — the namer takes no directory, and the
   directory needs to exist no more than the fixture does.

   Two assertions per token:
   - `filepath.Dir(filepath.Join(targetDir, base)) != targetDir` → the separator half.
   - `base == "." || base == ".."` → the `.`/`..` half.

   **The second is strictly implied by the first** — verified: base `"."` joins to
   `targetDir` whose `Dir` is `/initialize-control`, and base `".."` joins to
   `/initialize-control` whose `Dir` is `/`; both are already red under comparison one. It
   is kept because AC 3 names it, because it is one comparison, and because it pins the
   literal prefix against a later edit that drops it. **Write that in a comment on the
   assertion**, in the same shape as #1661's note on its redundant equality loop, and say
   explicitly that it can never be the sole red and must not be credited as the thing
   catching a mis-slugged token. Without that comment it reads as dead weight to review.

### Deliverable 2 — one entry in `finOfflineExecBans`

Key `"initialize_control_names_test.go"`. Value is `inband_bypass_revoke_names_test.go`'s
list **plus `writeFixture`, `captureClaudeVersion` and `os.LookupEnv`** — seventeen names
in four groups:

1. `resolveClaudeBin`, `WithWorktreeAuthenticated`, `WithWorktree`, `probeClaudeVersion` —
   keep a SKIP out. `resolveClaudeBin` and `WithWorktreeAuthenticated` skip *inside* the
   test body, after `=== RUN` is printed, and a skip exits 0, which reads as a pass under
   `make e2e-realclaude`.

   **Plus `captureClaudeVersion`, which no sibling entry carries.** It is the package's own
   direct `claude --version` exec, and it returns *precisely this namer's input* — so of
   every exec in the package it is the one a developer writing a file about version tokens
   is most likely to reach for, thinking "use the real token." Its failure mode is a loud
   `t.Fatalf` rather than a skip, so it would not fake a pass; what it would destroy is
   this ticket's *defining* property, that the file settles with no claude binary at all.
   The sibling entries lack it because their subject is not a version token. Say that in
   the comment, so the next reader does not "harmonise" it away.
2. `os.Getenv`, `os.Environ` — the credential guard. The process environment here carries
   `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY`. (Follow #1662's wording for the 4+2
   split, not #1661's header, which called all six a skip guard.)

   **Plus `os.LookupEnv`**, which is the two-value form of `os.Getenv` and reads the same
   environment, so it is a hole in the guard as the sibling entries have it. Adding it is a
   deliberate superset of the Technical Notes' enumeration, not a contradiction of it — PO
   describes this group by its purpose, and `os.LookupEnv` serves that purpose. It is true
   against this file's shipped code, which imports no `os` at all. Note the deviation in
   the comment so review does not have to reconstruct the reasoning.

   `exec.Command` / `exec.CommandContext` were considered and **declined**: no sibling
   entry carries them, the file imports no `os/exec`, and banning a stdlib package the file
   never imports is a defense for a failure mode nobody has observed. The package's own
   exec helpers, which *are* reachable without a new import, are covered by group 1.
3. `packageDir`, `setModeFixturePath`, `writeSetModeFixture`, `writeFixture` — `packageDir`
   plus **every** wrapper that reaches it. Verified today that those three are the
   complete wrapper set. The check is a bare AST identifier match, so a file calling a
   wrapper reaches `packageDir` transitively while never naming it; a `packageDir`-only
   entry leaves the ban true and the property false.
4. `filepath.Glob`, `os.ReadFile`, `os.WriteFile`, `os.Create`, `os.ReadDir` — the
   relative-path hazard. `go test` runs in the package source directory, so a relative
   `os.WriteFile("testdata/…")` reaches the committed fixtures without naming any wrapper.
   `filepath.Glob` is the read direction, and it is what keeps this file's controls
   synthetic literals rather than a directory listing.

`t.TempDir` is deliberately **absent**, matching #1661's entry. Note the reason in the
comment so review does not have to wonder: this file writes nothing and needs no directory
at all, so the ban would be true but would also be a name PO did not enumerate.

The new file uses `filepath.Match`, `filepath.Join` and `filepath.Dir`. None is banned —
the dotted entries are matched as selectors, so `filepath.Glob` bans only `filepath.Glob`.
`strings.Repeat` and `fmt.Sprintf` are likewise unaffected.

Registering the file is what enrols it: `TestFinOfflineFilesReachNoExecHelper` parses each
registered filename and `t.Fatalf`s outright if it cannot, so there is no path where the
entry is present and the check silently skips it. The parse deliberately omits
`parser.ParseComments`, so the check cannot answer itself out of the header prose that
states the same list.

## Why the separator-bearing tokens are the only thing this file measures

This is the one place the developer can conform to every line of the ticket and still ship
a file that asserts nothing.

Re-measured today (2026-08-22) against the real `versionSlug`, with the namer mutated to
interpolate the token raw — `fmt.Sprintf("initialize_control_v%s.json", versionToken)`,
which is equally pure, still carries the literal prefix, and conforms to every other line
of this spec:

| assertion | reds under that mutant |
| --- | --- |
| subtest 1, all three globs | **0** — the prefix keeps the name out of every family whether or not the token was slugged |
| subtest 3, the `.`/`..` half | **0** — the literal prefix already makes the name neither |
| subtest 3, the separator half | **4** — `../..`, `a/b`, `/abs`, and a multi-byte `é/é` |

So **containment is the sole red for the mutant that drops `versionSlug`, and it reddens
only on tokens carrying `/`.** Note `..` is *not* among the four: raw, it mints
`initialize_control_v...json`, a perfectly clean component.

A token table of plausible `claude --version` strings plus `..` and `""` — a fair reading
of "adversarial" — leaves this entire file green under a namer that never calls
`versionSlug`. The risk is specific to this ticket and not inherited from #1661: #1661 was
covered by its *arm* dimension, whose `hostileArms` carries `a/b`, `/abs` and `../..`.
Collapsing that dimension is this ticket's size win, and it removes most of the
separator-bearing inputs from the file. Put them back in the token table.

Also confirmed today, for the anchoring half: flipping `underTestdata` on any one row
makes that row's control stop matching (`testdata/set_permission_mode_v0.0.0_default.json`
→ false; bare `permission_protocol_v0.0.0_default.json` → false; bare
`dropped_lines_v0.0.0.json` → false). So subtest 2 genuinely guards the anchoring, in both
directions, provided both halves go through `anchorFixtureName`.

## What NOT to do

- **Do not import #1697's multi-byte concern.** `versionSlug`'s 32-character cap is a byte
  slice, but its output is pure ASCII: `[^a-z0-9._-]+` matches every UTF-8 continuation
  byte, so a multi-byte token collapses to `_` *before* the cap applies. Re-measured:
  `2.1.220éé` → `2.1.220_`; `日本語` + 40×`x` → `_` + 31×`x`, exactly 32 bytes. The cap
  cannot split a rune. The mid-rune-split concern #1695 carried belongs to
  `capFixtureCapture` and travels with #1697's writer, not this slice.
- **Do not redeclare** `fixtureGlob`, `dropcapFixtureGlob` or `setModeFamilyGlob`. All
  three are already package-level.
- **Do not declare a parallel row type or a second anchoring helper.** The single
  `anchorFixtureName` is the property: flip a row's `underTestdata` and that row's negative
  assertion goes vacuous *and* its control reddens, in the same edit. Neither half can rot
  alone. A second anchoring path breaks that coupling.
- **Do not touch `testdata/`** in either direction, and do not touch any file other than
  the two named above.

## Concurrency model

None beyond `testing`'s. Parent and all three subtests call `t.Parallel()`. The namer is a
pure function of one string: no `*testing.T`, no directory parameter, no I/O, no shared
state. The pattern and token tables are function-local and read-only. Nothing this file
touches is written by another test, and it ranges no package-level table — in particular
it does **not** range `setModeArms`, which is what forced #1661's table to be
function-scoped.

## Error handling

- `filepath.Match` returning a non-nil `err` → `t.Fatalf`. A malformed pattern constant
  makes every comparison in the file meaningless, so continuing would report a green that
  means nothing. Both subtests that call `Match` need this.
- A match against a family glob, or a name escaping `targetDir` → `t.Errorf`, so one run
  reports every offending token rather than the first.
- A pattern with no controls → `t.Errorf` + `continue`.
- Nothing here returns an error to a caller; there is no production error path in this
  slice.

## Testing strategy

The deliverable *is* the test. Verification is running it, plus proving it is not vacuous.

**Run it, and read the RUN count — never the exit code.** This package is behind the
`e2e_realclaude` build tag, so `make check` never compiles it; the package can fail to
build while the standard gate passes honestly, and the suite exits 0 both on a build
failure and on a full credentials skip.

```
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestInitControlFixtureName_|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/
```

Expect **12** `=== RUN` lines, all PASS, on a machine with no claude and no credentials:
1 parent + 3 subtests for the new test, and 1 parent + 7 per-file subtests for
`TestFinOfflineFilesReachNoExecHelper` (six existing entries plus yours). Fewer than 12
means something did not run. Any SKIP is a failure of this ticket's defining property.

Also run `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` — `go vet` does not
execute tests, so it catches build breakage the `-run` filter would hide in an untouched
sibling file. `gofmt -l` on both changed files. `make cite-guard` on the branch.

**Prove the file is not vacuous** before calling it done. Three mutations, each expected
to redden a specific thing; use `go test -overlay=<abs-path>.json` so the worktree is
never written:

| mutation | expected |
| --- | --- |
| namer interpolates `versionToken` raw (drop `versionSlug`) | **subtest 3's separator half only**, reddening on exactly `../..`, `a/b`, `/abs`. Subtests 1 and 2 stay green, and so does the `.`/`..` half — that is the measured result above and confirms the token table carries the shapes that matter. If this mutant is green, the `/`-bearing tokens are missing. |
| flip `underTestdata` on any one row | that row's **control** goes red in subtest 2. Confirms the anchoring is guarded in both directions. |
| namer drops the `initialize_control_v` prefix, e.g. mints `set_permission_mode_v<slug>_x.json` | **subtest 1** goes red on the `setModeFamilyGlob` row. Confirms the negative assertion can fail at all. |

**Confirming the ban entry bites needs a different technique — `-overlay` cannot do it.**
`TestFinOfflineFilesReachNoExecHelper` calls `parser.ParseFile` with a `nil` source, so it
reads the file **off disk at run time**, in the package source directory. `-overlay` is a
build-time mapping consumed by the `go` command; it does not interpose on the test
binary's own `os`-level reads. An overlay-injected banned call would therefore compile
while the check parsed the unmodified on-disk file and stayed green — a misleading pass
that would read as "the ban does not bite."

So verify this one with a real edit and a real revert: add a `packageDir(t)` call to the
new file, run
`go test -tags e2e_realclaude -count=1 -run 'TestFinOfflineFilesReachNoExecHelper' ./internal/e2e/realclaude/`,
confirm the `initialize_control_names_test.go` subtest fails naming `packageDir`, then
remove the call and confirm `git status` reports the worktree clean before committing. Do
not leave the mutation in a commit.

The same caveat applies to any future check in this package that parses source at run
time. It does **not** apply to the three namer mutations above, which are ordinary
compiled-code changes and are correctly done through `-overlay` with no worktree write.

`make check` stays green but proves nothing about this package — say so when reporting,
rather than citing it as evidence.

## Open questions

- **`poolRevokeNamePattern`'s name outlives its ticket.** Three files will now share a row
  type named for #1643's family. A rename to something family-neutral is correct and is
  deliberately *not* in this ticket — it would edit `inband_bypass_revoke_names_test.go`,
  which is not a deliverable here, and would collide with any concurrent work on that
  file. Worth a follow-up ticket once #1697 lands and the third consumer makes the case
  concrete. Resolve by leaving it alone in this slice.
- **The `initialize_control_v` prefix is now the fourth family prefix in `testdata/`,** and
  nothing checks the four are pairwise disjoint — each namer only checks itself against the
  others that existed when it was written. A single package-level table of (prefix, glob)
  pairs with an all-pairs disjointness assertion would replace three hand-maintained
  files. Out of scope here; note it for whoever adds the fifth.
- **The exact version token #1688 will pass** is unknown until that live run happens — it
  may be the bare `2.1.220` field or the full `2.1.220 (Claude Code)` line. Both are in the
  token table and both mint clean names, so this ticket does not need the answer.

## Security review

**Verdict:** PASS (first pass FAILed on one MUST FIX, since addressed; this is the re-run)

**Findings:**

- **[Trust boundaries]** SHOULD FIX — *addressed in this spec.* The real boundary is
  subprocess stdout → filesystem path component: at #1697's call site the token originates
  in `captureClaudeVersion`, which reads `claude --version`. The boundary is explicit and
  single (`versionSlug` inside `initControlFixtureName`), but the return type is a bare
  `string` carrying no signal that it is now sanitised. Mitigated by requiring the doc
  comment to state the contract, that its totality rests on `versionSlug`'s character class
  rather than on the sampled table, and that the guarantee is **lexical** — it constrains
  the name, not the directory. Without that last clause #1697 could read "lands inside
  `dir`" as filesystem-level containment, which it is not: a symlinked `dir` still resolves
  elsewhere, and choosing `dir` stays the caller's responsibility.
- **[Tokens, secrets, credentials]** SHOULD FIX — *addressed.* The ban group whose stated
  purpose is guarding `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` covers `os.Getenv`
  and `os.Environ` but **not `os.LookupEnv`**, the two-value form reading the same
  environment. That is a hole in every sibling entry, inherited rather than introduced
  here. Added to this entry as a deliberate superset of the Technical Notes' enumeration,
  with the deviation documented in the comment. Retrofitting the sibling entries is out of
  scope for this slice.
- **[File operations]** No findings for this slice — path traversal *is* the subject, and
  the containment comparison is sound against every escape shape checked: interior
  separator, leading separator, `..` traversal, and an absolute-looking base (`filepath.Join`
  treats it as relative, so `Dir` still differs from `targetDir`). TOCTOU, file mode and
  atomic temp-plus-rename all belong to the writer and are **OUT OF SCOPE**, picked up by
  #1697 — this file creates, reads and writes nothing.
- **[Subprocess execution]** SHOULD FIX — *addressed.* `captureClaudeVersion` is the
  package's own direct `claude --version` exec and returns *precisely this namer's input*,
  making it the most reachable exec from a file whose subject is version tokens, and it is
  in no sibling ban entry. Added to group 1. `exec.Command` / `exec.CommandContext`
  considered and **declined** — the file imports no `os/exec`, and banning a package it
  never imports is a defense for an unobserved failure mode.
- **[Cryptographic primitives]** N/A by design, not by omission: this slice generates no
  randomness, hashes nothing, and compares nothing against a secret. The namer is
  deliberately deterministic — a random component would defeat #1697's ability to locate
  the file it wrote. The only comparisons are `filepath.Match` against three public glob
  constants.
- **[Network & I/O]** N/A — no socket, no HTTP, no listener, no read of any kind. The one
  input bound in play is `versionSlug`'s 32-byte cap, which is inherited rather than
  introduced and is exercised by the table's 64-character token.
- **[Error messages, logs, telemetry]** No findings — no `slog`, no telemetry. Every value
  reaching a `t.Errorf` is either a hardcoded token literal from this file's own table or a
  public glob constant naming committed fixtures. Nothing derived from the operator's
  environment can reach a message, because nothing in the file can read it.
- **[Concurrency]** No findings — the namer is pure, both tables are function-local and
  read-only, and the file ranges **no** package-level table. That last point is the
  concrete decision: #1661 must range `setModeArms` to generate its controls, and this
  file's three literal controls are what remove even that read-only coupling. The only
  package state touched is three immutable `const`s.
- **[Threat model alignment]** The relevant threat is durable-evidence integrity — a live
  run overwriting committed fixtures owned by a different probe, **with no red anywhere** —
  and it is this ticket's entire subject. `docs/protocol-mobile.md` § Security model does
  not apply: nothing in this slice crosses the wire. **OUT OF SCOPE:** the `models` payload
  that #1688 captures will eventually cross the relay to connected clients; whether any
  field of claude's `initialize` response is sensitive to disclose is a question for the
  publish path, not for a filename.
- **[MUST FIX — resolved]** The first pass's own testing strategy was wrong and would have
  produced a misleading green. It told the developer to verify the ban entry by injecting a
  banned call through `go test -overlay`. But `TestFinOfflineFilesReachNoExecHelper` calls
  `parser.ParseFile` with a `nil` source, reading the file **off disk at run time**, while
  `-overlay` is a build-time mapping consumed by the `go` command and does not interpose on
  the test binary's own reads. The injected call would have compiled while the check parsed
  the unmodified file and passed — reading as "the ban does not bite." Replaced with a real
  edit-and-revert plus a `git status` clean check, and the caveat scoped so it does not
  wrongly discourage `-overlay` for the three namer mutations, where it is correct.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-22
