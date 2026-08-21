# #1661 — Lock the Pool-revocation fixture-name family against #1595's committed family

**Ticket:** [#1661](https://github.com/pyrycode/pyrycode/issues/1661) · `bug` · `size:s` · `security-sensitive` · `needs-real-claude`
**Package:** `internal/e2e/realclaude` (behind the `e2e_realclaude` build tag)
**Production files:** none. Two test files: one new, one one-entry edit.

---

## Files to read first

Read these before writing anything. Every entry names a symbol, not a line — resolve each with
`codegraph_search` / `codegraph_node`, or open the file and search for the name.

| File | Symbol | What to extract |
|---|---|---|
| `internal/e2e/realclaude/set_permission_mode_probe_test.go` | `TestRealClaude_SetPermissionMode_FixtureNamesAvoidRegressionGlobs` | **The precedent to copy.** Its adversarial token list, its glob loop, its containment assertion. Copy the shape; do **not** copy its use of `setModeFixturePath` — see § "The one thing not to copy from the precedent". |
| `internal/e2e/realclaude/set_permission_mode_probe_test.go` | `setModeFixtureName` | The namer this ticket must differ from. Pure (`versionSlug` + `fmt.Sprintf`), safe to call. Its output is AC 3's family control. |
| `internal/e2e/realclaude/set_permission_mode_probe_test.go` | `setModeArms` | #1595's four-row table. Ranged **read-only**, in exactly one place: AC 3's family control. Not the arm table this ticket iterates. |
| `internal/e2e/realclaude/set_permission_mode_probe_test.go` | `setModeFixturePath`, `writeSetModeFixture` | The two `packageDir` wrappers. Read them to see *why* they are banned here, then never call them. |
| `internal/e2e/realclaude/inband_bypass_revoke_arms_test.go` | `poolRevokeArms`, `poolRevokeArm` | The arm table this ticket ranges. **Read the header comment above `poolRevokeArms`** — it is marked read-only, ranged from `t.Parallel()` tests in several files, and it already names this file. Never append, never reassign. |
| `internal/e2e/realclaude/inband_bypass_revoke_arms_test.go` | the file header comment (top of file) | The house style for an offline file in this package: what the header must claim, and the `go test -tags e2e_realclaude -run …` invocation it prints. Mirror it. |
| `internal/e2e/realclaude/permission_protocol_spike_test.go` | `versionSlug`, `versionSlugSubst` | The normaliser this ticket's namer reuses. `[^a-z0-9._-]+` → `_`, lowercased, truncated at 32. Note what it leaves intact: `.` and `-`. |
| `internal/e2e/realclaude/permission_protocol_spike_test.go` | `packageDir` | Resolves the package's real source dir via `os.Getwd`. AC 5 bans it here. |
| `internal/e2e/realclaude/permission_protocol_regression_test.go` | `fixtureGlob` | One of the two testdata globs. Carries a `testdata/` prefix — that is the anchoring fact. |
| `internal/e2e/realclaude/dropped_line_capture_test.go` | `dropcapFixtureGlob` | The other testdata glob. Also `testdata/`-prefixed. |
| `internal/e2e/realclaude/offline_exec_ban_test.go` | `finOfflineExecBans`, `TestFinOfflineFilesReachNoExecHelper` | Where AC 5's entry goes, and the AST checker that reads it. Note it parses **without** `parser.ParseComments`, and that it flags both bare idents and dotted selectors. The `inband_bypass_revoke_arms_test.go` entry is the shape to copy. |
| `docs/knowledge/features/set-permission-mode-inband-probe.md` | § the fixture record | #1595's committed record — the artifact family this ticket fences off. Skim only; nothing here modifies it. |
| `CODING-STYLE.md` | § "Comments — Citing Other Code" | `make cite-guard` is a build gate. Cite symbols, never `file.go:NNN`, never a range, never a bare `:NNN`. This file will be comment-heavy; the gate is diff-scoped and will see every line you add. |

---

## Context

#1595 proved live that a `set_permission_mode` control request carrying `mode: "default"` drops a running
child's bypass posture. Its durable record is four committed fixtures under `internal/e2e/realclaude/testdata/`:
`set_permission_mode_v2.1.220_{revoke,control_default,control_bypass,enable}.json`.

#1643 is the live three-arm behavioural probe. Its three arm names — `revoke`, `control_default`,
`control_bypass` — are *the same strings* #1595 already uses, and `setModeFixtureName` is package-level and
right there. Reusing it on the same claude version would overwrite three of #1595's four committed fixtures
**while every test stays green**. That is a durable-evidence integrity failure with no red anywhere, which is
why the ticket carries `security-sensitive`.

The filename prefix is the only thing between a live run and that overwrite. This ticket ships the name half of
#1643's substrate — a distinct namer plus the deterministic proof that its output cannot land in any committed
family — and it settles entirely offline: no claude binary, no credentials, no subprocess, no filesystem read.
#1662 (the fixture record and the writer that mints its target path through this namer) is blocked on it.

**No ADR is warranted.** This is one namer and one test inside an existing package, following an existing
precedent; there is no decision here that outlives the ticket. The lesson worth folding into
`docs/knowledge/features/e2e-realclaude.md` is the anchoring rule in § "Anchoring is the whole design" — the
documentation phase owns that file and will decide.

---

## Design

Two files. One new, one one-entry edit.

```
internal/e2e/realclaude/inband_bypass_revoke_names_test.go   NEW   the namer + the lock test
internal/e2e/realclaude/offline_exec_ban_test.go             EDIT  one map entry in finOfflineExecBans
```

The new file's name is deliberately the sibling of `inband_bypass_revoke_arms_test.go`. That file's header
already refers to this one as "#1652's name test" — same file, from before the split.

### 1. The namer (AC 1)

Package-level, in the new file. A pure function of two strings: no directory parameter, no `*testing.T`, no
I/O, no globals read beyond `versionSlugSubst`.

```go
// poolRevokeFixtureName mints the fixture filename for one arm of #1643's
// Pool-issued bypass-revocation probe. Pure: no directory, no I/O.
//
// Returns: "pool_revoke_v" + versionSlug(versionToken) + "_" + versionSlug(arm) + ".json"
//
// CONTRACT for #1662's writer: the result is always a SINGLE clean path
// component — no separator, never "." or "..", for any pair of inputs. That is
// what makes filepath.Join(dir, name) safe at the call site, and it is proven by
// the containment subtest below rather than by the type.
func poolRevokeFixtureName(versionToken, arm string) string
```

The "single clean path component" clause is part of the contract, not commentary: #1662 joins this name under a
real directory, and the guarantee it relies on lives here. State it in the doc comment.

Three decisions, each load-bearing:

- **The `pool_revoke_` prefix.** It is the entire mechanism. `filepath.Match` anchors a pattern's literal head
  at position 0, so a name beginning `pool_revoke_v` can never match `set_permission_mode_v*_*.json`,
  `testdata/permission_protocol_v*_*.json` or `testdata/dropped_lines_v*.json`. Nothing else in the repo uses
  this prefix (verified: zero hits repo-wide for `pool_revoke`). **The prefix must be a literal in the format
  string (or a `const`), never derived from either input** — it is the one part of the name no input can reach,
  and that is precisely why the overwrite is impossible rather than merely unobserved.
- **Both inputs go through `versionSlug`, not just the version.** The precedent slugs only the version because
  its arms come from a controlled table. AC 4 requires containment across *hostile arm strings*, and an arm
  `a/b` interpolated raw mints `pool_revoke_v2.1.220_a/b.json`, which lands in a **subdirectory** of the target.
  Slugging the arm with the same function neutralises that. Reusing `versionSlug` rather than adding an
  `armSlug` is deliberate: it is a general `[^a-z0-9._-]+ → _` normaliser with a 32-char cap, and applying the
  *same* normaliser to both inputs is what makes "both dimensions get identical treatment" visible in one line.
  Say so in the function's doc comment — the name says "version" and the reader will otherwise wonder.
- **`.json` suffix, `_` separator between the two slugs.** Matches every other fixture family in the package,
  and keeps the name a single path component.

### 2. Anchoring is the whole design (AC 2 + AC 3)

The three patterns are **not matched against the same string**, and this is the one place the test can go
silently vacuous:

| Pattern | Source | Matched against |
|---|---|---|
| `fixtureGlob` | `permission_protocol_regression_test.go` | `filepath.Join("testdata", base)` |
| `dropcapFixtureGlob` | `dropped_line_capture_test.go` | `filepath.Join("testdata", base)` |
| `set_permission_mode_v*_*.json` | a **new local constant** in the new file | the bare `base` |

The first two carry a `testdata/` prefix because that is how the tests that own them evaluate them. #1595's
family pattern has no prefix — it names the committed family, not a glob anyone runs. Get either direction
wrong and the assertion passes unconditionally (both measured false in § Measured evidence).

**The structural defence: one anchoring function, used by both the negative assertions and the controls.**

```go
// poolRevokeNamePattern is one committed-fixture family this namer's output must
// stay out of, paired with a control proving the pattern can still match.
type poolRevokeNamePattern struct {
    glob          string
    underTestdata bool     // anchor against filepath.Join("testdata", base), else base
    controls      []string // BASE names of the shape this glob was written for
    hazard        string   // what a match would cost, for the failure message
}

// anchorFixtureName returns the string p's glob is evaluated against.
func anchorFixtureName(p poolRevokeNamePattern, base string) string
```

Both loops call `anchorFixtureName`. That is what makes AC 3 mechanical rather than a convention: flip a row's
`underTestdata` and its negative assertion goes vacuous **and** its control goes red in the same edit. Neither
half can rot alone.

Declare the table **inside** the test function — the family row's `controls` slice is computed from
`setModeFixtureName` over the token list × `setModeArms`, so it cannot be a package-level literal.

**Controls, per row:**

- **Family row** — AC 3 names it specifically: over the same version tokens and #1595's own `setModeArms`,
  every name `setModeFixtureName` mints **must** match. That is 44 subjects (11 tokens × 4 arms), all measured
  green. This is the one place the file ranges `setModeArms`, and it ranges it read-only.
- **`fixtureGlob`** — the synthetic literal `permission_protocol_v0.0.0_default.json` (a base name; the
  anchoring function adds the prefix).
- **`dropcapFixtureGlob`** — the synthetic literal `dropped_lines_v0.0.0.json`.

Use **synthetic** literals with a `v0.0.0` token, not committed filenames. A control written as
`dropped_lines_v2.1.220.json` — today the only committed dropcap fixture — goes spuriously red the day that
capture is retaken at a new version. A literal of the right *shape* proves the anchoring identically and does
not rot with the fixture set. It also keeps the control honest about doing no I/O.

**The inequality** (AC 2's last clause): for each token × arm,
`poolRevokeFixtureName(token, arm.name) != setModeFixtureName(token, arm.name)`.

Measured, the family glob strictly implies this inequality — 44 of 44 of `setModeFixtureName`'s outputs match
the family pattern, so a name that clears the glob has already cleared the inequality, and the inequality can
never be the sole red. Keep it anyway: it is what names the hazard concretely in a failure message, and it
keeps holding if #1595's family ever grows a name the glob does not describe. **Do not credit it as the thing
closing the overwrite hazard — the family glob closes it.**

### 3. Containment (AC 4)

The namer returns no directory, so the test joins one itself:

```
got := filepath.Join(targetDir, poolRevokeFixtureName(token, arm))
assert filepath.Dir(got) == targetDir
```

`filepath.Dir(filepath.Join(dir, name)) == dir` is exactly AC 4's clause: a subdirectory (`a/b`) gives
`dir/…/a`, an escape (`../..`) gives an ancestor, both ≠ `dir`.

Two hazards to get right:

- **`targetDir` must be a clean absolute literal** — e.g. `/pool-revoke/fixtures`. `filepath.Join` cleans its
  result, so a literal with a trailing slash makes `Dir` return the cleaned form and the comparison fails on a
  perfectly healthy name (measured). No `t.TempDir()`, no `packageDir` — the directory is arbitrary and needs
  to exist no more than the fixture does.
- **Both dimensions, in one nested loop:** the adversarial version tokens × (`poolRevokeArms`' three names +
  the hostile arm literals). The hostile arms are **this test's own literals** — do not add rows to
  `poolRevokeArms`.

**This assertion is not green-by-construction, and that is why AC 4 keeps the version dimension.** The
precedent's containment check is green by construction against its own shipped namer. This namer is being
written now, and AC 1 requires only purity — a namer that interpolates both inputs raw is equally
AC-1-conforming and, measured, escapes on **40** of the 110 token × arm pairs. Containment is the assertion
that forces both inputs through the slug.

**Input lists.**

- *Version tokens* — the precedent's nine (`2.1.220`, `2.1.220 (Claude Code)`, `2_1_220`, `2.1.220-beta.1`,
  `permission_protocol`, `..`, `../..`, `""`, sixty-four `9`s) **plus `set_permission_mode` and
  `dropped_lines`**. The precedent carries `permission_protocol` as the token shaped to smuggle a name into
  another family; this namer must stay clear of *three* families, so it carries one such token per family.
- *Hostile arm literals* — at minimum `a/b`, `..`, `../..`, `/abs`, `""`, and a 64-character run. Add
  `set_permission_mode` for symmetry with the token list if you like; it is one string.

### 4. The offline ban entry (AC 5)

Add one entry to `finOfflineExecBans`, keyed on the new filename, in the same commit as the new file. Copy the
comment shape of the `inband_bypass_revoke_arms_test.go` entry.

The list, and why each name is on it:

| Banned | Why |
|---|---|
| `resolveClaudeBin` | `t.Skip`s **inside the test body**, after `=== RUN` is printed. The gate cannot tell that skip from a pass. |
| `WithWorktreeAuthenticated`, `WithWorktree` | Same class: credential/worktree setup that skips. |
| `probeClaudeVersion` | Runs the binary. |
| `os.Getenv`, `os.Environ` | The process environment here carries `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY`. |
| `packageDir` | AC 5 names it. The namer takes no directory and containment is asserted against an arbitrary literal one; nothing here has business resolving the package's real `testdata/`. |
| `setModeFixturePath`, `writeSetModeFixture` | **The `packageDir` ban is defeated by one level of indirection, and the precedent takes exactly that indirection.** These are the only two wrappers that reach `packageDir` from the file this test copies its shape from (verified: they and `writeFixture` are the only `packageDir` callers in the package). Banning `packageDir` alone leaves the ban true and the property false. |
| `filepath.Glob` | AC 3's controls must not read the real `testdata/`. `filepath.Match` is a pure string operation; `filepath.Glob` is a directory read. This is the ban that keeps the controls synthetic. |
| `os.ReadFile`, `os.WriteFile`, `os.Create`, `os.ReadDir` | **A relative path bypasses every entry above.** `go test` runs each package's tests in the package source directory, so `os.WriteFile("testdata/…")` reaches — and overwrites — the real committed fixtures without touching `packageDir` or any of its wrappers. This is the exact hazard the ticket exists to prevent, so the ban has to cover the shortest route to it, not only the scenic one. Beyond the AC 5 minimum; keep it, and say why in the entry's comment. |

`t.TempDir` is deliberately **absent**, for a different reason than in #1651's entry: that file's seed writes a
`sessions.json` and a tempdir is where it must land. This file writes nothing and needs no directory at all —
listing `t.TempDir` would be a ban against a thing that is already structurally impossible.

Two mechanics of the checker to keep in mind:

- It flags bare `*ast.Ident` by name, so **do not name a local variable `packageDir`** (or any other banned
  bare name). A dotted entry is matched as a selector, so `filepath.Glob` bans only that selector and leaves
  `filepath.Match` / `filepath.Join` / `filepath.Dir` alone.
- It parses **without** `parser.ParseComments`. Your header may name every banned symbol in prose; the check
  cannot answer itself out of it.

### The one thing not to copy from the precedent

`TestRealClaude_SetPermissionMode_FixtureNamesAvoidRegressionGlobs` builds its subjects with
`setModeFixturePath(t, token, arm.name)` and its `wantDir` with `filepath.Join(packageDir(t), "testdata")`.
Both reach `packageDir`. Copy the *structure* of that test — the token list, the glob loop, the containment
comparison — and replace those two calls with `setModeFixtureName` and a literal directory. If you find
yourself importing `os`, you have gone wrong.

---

## Concurrency model

None beyond `testing`'s own. One test function, `t.Parallel()` at the top and in each `t.Run` subtest, matching
the package's house style. The two tables the test reads (`poolRevokeArms`, `setModeArms`) are package-level
and ranged read-only — this file adds a third reader to `poolRevokeArms`, which is precisely what its
read-only header anticipates. No writes, no shared mutable state, no goroutines, no `-race` exposure.

## Error handling

Test-only, so this is failure-reporting policy rather than error handling:

- `t.Errorf` for every assertion — report **all** offending token × arm pairs, not the first. A namer that
  fails on one dimension usually fails on many, and a reader deserves to see the shape before starting.
- `t.Fatalf` only for `filepath.Match` returning `ErrBadPattern`, which means a pattern constant is malformed
  and every subsequent comparison is meaningless.
- Every message states **the consequence**, in this package's established voice: not "want false, got true"
  but which committed fixture family the name would land in and what a live run would overwrite. The failure
  message is read once, by someone about to spend tokens on a three-arm live run.
- Print the token, the arm and the minted name in every message. Sixty-four `9`s truncated to 32 is not
  guessable from a bare boolean.

---

## Testing strategy

One test function in the new file, with three `t.Run` subtests. One entry point, three readable failure
surfaces.

**`TestPoolRevokeFixtureName_AvoidsCommittedFamiliesAndStaysContained`**

- **subtest "no minted name joins a committed family" (AC 2)** — over every version token × every arm
  (`poolRevokeArms`' rows plus the hostile literals): for each of the three patterns, `filepath.Match(p.glob,
  anchorFixtureName(p, base))` is false; and `poolRevokeFixtureName(token, arm) != setModeFixtureName(token,
  arm)` for the table arms.
- **subtest "each pattern's control can still match" (AC 3)** — for every row, every entry in `p.controls`
  matches `p.glob` under `anchorFixtureName(p, control)`. The family row's controls are the 44
  `setModeFixtureName` outputs; the other two rows carry one synthetic literal each. A row whose control fails
  is a row whose negative assertion proved nothing.
- **subtest "every minted name stays directly inside its target directory" (AC 4)** — over the same token ×
  arm cross-product, `filepath.Dir(filepath.Join(targetDir, base)) == targetDir`.

**`TestFinOfflineFilesReachNoExecHelper`** (existing, `offline_exec_ban_test.go`) picks up the new file
automatically once its `finOfflineExecBans` entry lands, and asserts the offline claim over the new file's AST.

**Deliberately out of scope — do not add:**

- *Intra-family uniqueness.* Two `poolRevokeArms` rows slugging to the same fixture name would be a
  self-overwrite, but the rows are pinned to three distinct plain names by
  `TestPoolRevokeArms_PinLaunchPostureAndUpdateByName`, and the ACs do not ask for it. Leave it to #1662, which
  owns the writer.
- *Any assertion about files that exist on disk.* AC 5 bans the directory read that would need.
- *Any change to `poolRevokeArms`, `setModeArms`, or #1595's committed fixtures.*

**How to run it while developing** — the package is behind the `e2e_realclaude` tag, so the standard gate never
compiles it:

```bash
go vet -tags e2e_realclaude ./internal/e2e/realclaude/
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestPoolRevokeFixtureName_|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/
```

Both must report **PASS** — not SKIP, not "no tests to run" — on a machine with no claude and no credentials.
**Read the count of tests that executed, never the exit code:** `make e2e-realclaude` exits 0 both on a build
failure and on a full credentials skip. Put that invocation and that warning in the file header, as
`inband_bypass_revoke_arms_test.go` does.

### Line budget

Total written work ≈ 275 lines, against the size-`s` boundary of 400:

| Piece | Lines |
|---|---|
| `poolRevokeFixtureName` + doc comment | ~15 |
| `poolRevokeNamePattern`, `anchorFixtureName` + docs | ~25 |
| Token / hostile-arm / pattern tables | ~50 |
| Three subtests | ~120 |
| File header comment | ~50 |
| `finOfflineExecBans` entry + its comment | ~15 |

The header is the piece most likely to overrun — this package's headers are long and the temptation is real.
Fifty lines is enough to state: what the file locks and why, that it is offline and what that buys, the run
invocation, the RUN-count warning, and the anchoring rule. Everything else belongs on the assertion it
explains.

---

## Measured evidence

Re-measured against the tree at `9eb5b55`, with a standalone reimplementation of `versionSlug`,
`setModeFixtureName` and the proposed namer over 11 tokens × 10 arms (3 table rows + 7 hostile literals):

| Claim | Measured |
|---|---|
| Minted names match none of the three patterns, and never equal `setModeFixtureName` | 0 bad / 110 pairs |
| Containment holds for the slugged namer | 0 escapes / 110 |
| Containment for the raw, unslugged, AC-1-conforming counterfactual | **40 escapes / 110** — the assertion is load-bearing |
| Family control: `setModeFixtureName` × `setModeArms` matches `set_permission_mode_v*_*.json` | 44 / 44 |
| Family pattern vs a `testdata/`-prefixed subject | `false` — silently vacuous |
| `"*" + family pattern` vs a `testdata/`-prefixed subject | `false` — `*` will not cross a separator |
| `fixtureGlob` / `dropcapFixtureGlob` vs a bare base name | `false` / `false` — silently vacuous |
| Same two vs correctly-anchored synthetic controls | `true` / `true` |
| `set_permission_mode_v2.1.220_a/b.json` | escapes containment **and** evades the family glob — AC 2 and AC 4 cover each other's blind spot |
| Trailing-slash `targetDir` literal | `Dir` returns the cleaned form → spurious red. Use a clean literal. |
| `pool_revoke` prefix elsewhere in the repo | zero hits |
| Testdata globs in the package | exactly two (`fixtureGlob`, `dropcapFixtureGlob`) — AC 2's "only two" holds |

Sample output: `pool_revoke_v2.1.220_revoke.json`; token `../..` → `pool_revoke_v.._.._revoke.json`; hostile arm
`a/b` → `pool_revoke_v2.1.220_a_b.json`.

---

## Security review

**Verdict:** PASS

**Asset and threat.** Committed evidence, not a runtime surface. #1595's four fixtures under
`internal/e2e/realclaude/testdata/` are the durable record of a live-measured protocol behaviour, read by
humans deciding whether pyry's in-band revocation works. The asset is their *integrity*; the threat is silent
overwrite by a later live run, with every test staying green. Three SHOULD FIX findings were folded into the
spec during this pass (marked below); no MUST FIX remains.

**Findings:**

- **[Trust boundaries]** No findings. One explicit boundary: `versionSlug`, applied to **both** of
  `poolRevokeFixtureName`'s inputs. `versionToken` is the untrusted one — ultimately `claude --version` output
  reaching this code through `probeClaudeVersion` in #1662/#1643 — and `arm` is a repo-controlled literal that
  gets the same treatment anyway, because `poolRevokeArms` is a human-edited growth point. The boundary is one
  function, not scattered. Downstream gets no type-system signal that the result is safe, which is why the
  namer's doc comment now carries the "single clean path component" clause as an explicit contract for #1662
  *(SHOULD FIX, folded into § Design 1)*.

- **[Tokens, secrets, credentials]** No findings. Nothing is generated, stored, rotated or revoked here. The
  relevant exposure is the ambient one: this process's environment carries `CLAUDE_CODE_OAUTH_TOKEN` and
  `ANTHROPIC_API_KEY`, and `os.Getenv` / `os.Environ` are banned for this file in `finOfflineExecBans`. Failure
  messages print version tokens and arm names — `claude --version` output and repo literals, neither secret,
  and the same values the precedent already prints.

- **[File operations]** SHOULD FIX, folded into § Design 4. Path traversal is the category that matters and it
  is answered: `..` survives `versionSlug` (dots are in the allowed class), so `../..` slugs to the inert
  component `.._..`, and containment is asserted over both input dimensions against an arbitrary target
  directory — proving the property of the namer rather than of one call site. The measured counterfactual
  matters here: an unslugged, equally AC-1-conforming namer escapes on 40 of 110 pairs, so AC 4 is load-bearing
  rather than green-by-construction. The finding was in the *ban list*: `packageDir` alone is defeated by one
  level of indirection (`setModeFixturePath`, `writeSetModeFixture` — and the precedent this ticket says to
  copy calls the first of them), and defeated entirely by a **relative** path, since `go test` runs in the
  package source directory and `os.WriteFile("testdata/…")` reaches the committed fixtures without naming
  `packageDir` at all. Both routes are now banned, along with `filepath.Glob` for the read direction. No TOCTOU
  (no check-then-use), no file modes (nothing is created), no symlink following, no atomic-write requirement —
  this file performs no I/O in either direction.

- **[Subprocess / external command execution]** No findings. No `exec`, no `sh -c`, no environment inheritance
  decision to make. `resolveClaudeBin`, `probeClaudeVersion`, `WithWorktree` and `WithWorktreeAuthenticated`
  are banned by name, which is the deterministic half of the offline claim; the first two of those `t.Skip`
  **inside** the test body, after `=== RUN` prints, so without the ban a skip is indistinguishable from a pass
  under `make e2e-realclaude`.

- **[Cryptographic primitives]** Not applicable, by design rather than by omission. No randomness, no hashing,
  no key material. The `==` and `filepath.Match` comparisons are over non-secret strings — filenames and glob
  patterns — so `crypto/subtle.ConstantTimeCompare` has nothing to protect here.

- **[Network & I/O]** Not applicable — no sockets, no HTTP, no reads to cap. The one length bound in the design
  is `versionSlug`'s 32-character truncation, applied per input, which caps the minted filename at roughly 78
  characters. That bound is inherited, not restated, and § Open questions 1 records why it is left alone.

- **[Error messages, logs, telemetry]** No findings. No `slog`, no telemetry, no aggregation. `t.Errorf` for
  every assertion so all offending pairs are reported rather than the first; `t.Fatalf` reserved for
  `filepath.Match` returning `ErrBadPattern`, which invalidates every subsequent comparison. Messages carry the
  token, the arm and the minted name — a 64-character token truncated to 32 is not guessable from a bare
  boolean — and no fixture contents, because the file reads none.

- **[Concurrency]** No findings for this ticket; one inherited hazard named. This file becomes the third
  parallel reader of `poolRevokeArms` and the first outside reader of `setModeArms`; both are ranged read-only,
  and the spec forbids append and reassignment, matching the read-only header `poolRevokeArms` already carries.
  The pattern table is function-local and shared by closure with `t.Parallel()` subtests — read-only, so no
  race. No goroutines are spawned, so no lifecycle or leak question. The inherited hazard is that nothing
  *mechanically* stops #1643's live driver from mutating `poolRevokeArms`; that is #1651's header contract and
  #1643's to honour, not this ticket's to enforce.

- **[Silent vacuity]** — not a checklist category, but the dominant risk in a test whose entire job is a
  negative claim, so it gets walked. Three routes to a green test that proves nothing, each answered
  structurally rather than by convention: (a) *mis-anchored pattern* — measured `false` in both directions and
  for the leading-`*` variant, answered by `anchorFixtureName` being the single anchoring used by both the
  negative assertions and AC 3's controls, so a flipped `underTestdata` reddens that row's control in the same
  edit; (b) *rotting control* — a control literal lifted from a committed filename goes red the day that
  capture is retaken and the cheap repair is to weaken it, answered by synthetic `v0.0.0` literals; (c)
  *emptied arm table* — the hostile-arm literals and the 44-subject family control still run, and
  `TestPoolRevokeArms_PinLaunchPostureAndUpdateByName` reddens on a missing row, so no extra guard is needed
  here. The one instruction the developer must not soften: **the controls loop calls `anchorFixtureName`; it
  must not inline its own anchoring**, or the property is lost silently. That is the single highest-value line
  for code review to check.

- **[Threat model alignment]** No findings. `docs/threat-model.md` does not exist in this repo, and
  `docs/protocol-mobile.md` § Security model covers the relay's network threats — neither speaks to
  repo-artifact integrity, which is this ticket's threat. Naming that gap is the alignment statement: the
  threat is stated in § Context and in this section, and nowhere else, deliberately.

- **[Out of scope]** Nothing here prevents #1662's writer from bypassing the namer and composing a path itself,
  or from calling `setModeFixtureName` directly. The name family is fenced; the *writer's use of it* is #1662's
  acceptance criterion and must be pinned there. This ticket closes the naming half of the hazard and no more,
  and saying so is part of the review rather than a caveat on it.

**Residual risk, stated rather than implied.** `finOfflineExecBans` is matched by identifier and selector name,
so it cannot express "this file does not import `os`". A novel `os` call — or any newly added helper that
reaches the real `testdata/` — is outside the ban until somebody adds its name. Code review should read the new
file's import block as well as its ban entry; a file that legitimately needs neither `os` nor `exec` should
import neither.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-20

---

## Open questions

1. **Should the namer's arm slug be capped shorter than 32?** `versionSlug`'s cap applies to each input
   independently, so a pathological pair yields a ~78-character filename — long, but well inside every
   filesystem limit and measured contained. Left as-is; raise it only if #1662 finds a real arm that truncates
   into a collision.
2. **Intra-family collision** (two `poolRevokeArms` rows slugging to one name) is out of scope here and belongs
   with #1662's writer, which is the thing that would actually overwrite. Noted so the developer does not add
   it speculatively.
3. **Whether the anchoring rule belongs in `docs/knowledge/features/e2e-realclaude.md`.** It generalises past
   this ticket — every future fixture family in this package faces the same one-directional-absence trap. The
   documentation phase owns that file and decides; this spec does not touch it.
