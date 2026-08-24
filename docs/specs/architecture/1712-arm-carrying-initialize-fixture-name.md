# #1712 — mint and lock an arm-carrying fixture name for the three-arm `initialize` capture

Test-only. One file modified: `internal/e2e/realclaude/initialize_control_names_test.go`. No
production change, no new file, no new `finOfflineExecBans` entry.

## Files to read first

Symbols, not lines — resolve each with `codegraph_search` / `codegraph_node`.

- `internal/e2e/realclaude/initialize_control_names_test.go` → the whole file. It is the edit
  site. Read its header end to end, then `initControlFixtureName` and
  `TestInitControlFixtureName_AvoidsCommittedFamiliesAndStaysContained`. Extract: the three
  subtest shapes, the `patterns` literal, the `targetDir` comment, and the § "One input, not
  two" paragraph this ticket has to correct.
- `internal/e2e/realclaude/inband_bypass_revoke_names_test.go` → `poolRevokeFixtureName`,
  `poolRevokeNamePattern`, `anchorFixtureName`, and the `hostileArms` literal inside
  `TestPoolRevokeFixtureName_AvoidsCommittedFamiliesAndStaysContained`. Extract: the two-input
  namer shape, the row type you must reuse, the single anchoring point you must reuse, and the
  arm-column hostile shapes.
- `internal/e2e/realclaude/permission_protocol_spike_test.go` → `versionSlug`. Extract: the
  lowercase + `[^a-z0-9._-]+` → `_` fold + 32-character truncation. All three bear on
  distinctness.
- `internal/e2e/realclaude/set_permission_mode_probe_test.go` → `setModeFixtureName`,
  `setModeArms`, and the trailing `seen` map inside
  `TestRealClaude_SetPermissionMode_FixtureNamesAvoidRegressionGlobs`. Extract: the pairwise
  collision check shape (the `seen` map), and the counter-example — `setModeFixtureName`
  interpolates its arm RAW and is the wrong sibling to copy.
- `internal/e2e/realclaude/inband_bypass_revoke_arms_test.go` → `poolRevokeArm`,
  `poolRevokeArms`. Extract: the read-only-shared-table doc discipline the new arm declaration
  copies.
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans`, its
  `initialize_control_names_test.go` entry, `TestFinOfflineFilesReachNoExecHelper`. Extract:
  confirmation that the existing seventeen-name entry already covers everything this ticket
  adds, so **no map edit is needed**. Read it; do not change it.
- `internal/e2e/realclaude/permission_protocol_regression_test.go` → `fixtureGlob`.
  `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapFixtureGlob`.
  `internal/e2e/realclaude/inband_bypass_revoke_names_test.go` → `setModeFamilyGlob`. Extract:
  the exact pattern strings and which of them carries a `testdata/` prefix.
- `internal/e2e/realclaude/initialize_control_writer_test.go` → `writeInitControlFixture`.
  Extract: it mints through `initControlFixtureName` today and **stays that way in this
  slice**; #1713 is what migrates it.
- `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` — the committed capture
  this lock protects. Its `claude_version` is `2.1.239`; that string must be a row in the token
  table.
- `docs/knowledge/features/e2e-realclaude.md` § the fixture-name-lock entries for #1661, #1696,
  #1701, #1702 — the accumulated lessons for this exact family.

## Context

`initControlFixtureName` takes one input. #1696 collapsed the arm dimension deliberately and
recorded why in its header § "One input, not two": *"this capture is one probe, so the name is
`initialize_control_v<slug>.json` and the lock table is tokens × 1."*

That reason expires with #1715, which drives three arms. Through today's namer all three mint
the same path, the last arm wins, and `writeInitControlFixture` — which mints its own path
internally, on purpose — writes over `testdata/initialize_control_v2.1.239.json`. Nothing
compares a written path against a committed one, so the loss is silent. This slice mints the
arm-carrying name and locks it before the run that could trip it exists.

The lock is not one property but four, and the ticket's mutant table is what keeps them from
being four restatements of one thing. Reproduce that table (below, § Testing strategy) — it is
the acceptance shape, not decoration.

No ADR warranted: this is the fourth instance of an established in-package pattern (#1595 →
#1661 → #1696 → here), and `docs/knowledge/features/e2e-realclaude.md` already carries the
family's lessons for the documentation phase to extend.

**For the documentation phase, not for the developer:** that overview's
`initialize_control_names_test.go` (#1696) entry states *"this capture has one, so the namer
takes one input and the lock table collapses to tokens × 1"* and calls the file *"the first
[lock] for a single-input namer"*. Both go stale with this ticket, in the same way AC 3 flags the
in-code paragraph. The developer must not edit that file — the documentation phase owns it.

## Design

### Where it lands, and why not a new file

**Extend `initialize_control_names_test.go`.** The ticket sanctions either placement; this one
is smaller and lands the mandated correction where the falsehood is.

- The file's `finOfflineExecBans` entry (seventeen names) already covers everything added here:
  the new code performs no I/O in either direction, exactly like the code already in the file.
  A new file would need its own entry — `finOfflineExecBans` is keyed by file name and each
  file gets exactly one entry, as `initialize_control_record_test.go`'s entry comment states.
- AC 3 requires correcting #1696's recorded reason for collapsing the arm dimension. That
  reason lives in this file's header, directly above the one-input namer. Extending the file
  puts the correction and the two-input namer in the same place; a new file would leave a false
  paragraph standing in a file this ticket otherwise never opens.
- The two namers' outputs are compared to each other by AC 3. Both operands in one file is the
  legible arrangement.

Growing the file from 291 to roughly 560 lines is normal for this package
(`set_permission_mode_probe_test.go` is 1036, `initialize_control_probe_test.go` 575).

### The arm declaration

```go
var initControlArms = []string{ /* three identifiers */ }
```

Package scope, read-only, in this file. Identifiers, in this order:

| identifier | #1715's arm |
|---|---|
| `before_first_turn` | control request written before the first user turn |
| `after_completed_turn` | control request written after a completed turn |
| `control_no_request` | no control request sent |

Chosen so the identifier names the send point, because the send point is the only dimension the
arms vary on. All three are already slug-clean, all are under `versionSlug`'s 32-character cap,
and all three survive the slug unchanged and pairwise distinct — which is the property AC 4
locks and which a fourth arm typed by hand can break.

`control_` prefixes the control arm, matching `poolRevokeArms` and `setModeArms`.

A `[]string` and not a row struct: this slice needs identifiers and nothing else, and a
one-field struct is a shape #1713 would have to change the moment it knows its fields. The doc
comment must carry two instructions:

- **Read-only.** Never append, never reassign. It will be ranged from `t.Parallel()` tests in
  at least three files; `poolRevokeArms`' doc comment is the wording to follow.
- **Grow this declaration rather than shadowing it.** When #1713 or #1715 needs per-arm
  behaviour (send-point semantics, prompt, drive sequence), the fields go here, turning the
  slice into a table of rows. A second table keyed by these names is the duplication AC 4
  exists to prevent.

Hostile arms are the lock's **own** literals and must NOT be appended here, for the reason
#1661 gives about `poolRevokeArms`: this declaration is what other files range.

### The namer

```go
func initControlArmFixtureName(versionToken, arm string) string
```

Returns `initialize_control_v<versionSlug(versionToken)>_<versionSlug(arm)>.json`. Pure function
of exactly those two inputs: no directory parameter, no `*testing.T`, no I/O in either
direction. Lands next to `initControlFixtureName`, which keeps its one input and all its current
callers in this slice — `writeInitControlFixture` is unchanged here and is #1713's to migrate.

Three properties the doc comment must state, each with the reason:

- **`initialize_control_v` is a literal and must never be derived from an argument.**
  `filepath.Match` anchors a pattern's literal head at position 0, so a name beginning with a
  head no pattern shares cannot match any of the three family globs. This is what makes the
  collision impossible rather than merely unobserved.
- **BOTH inputs go through `versionSlug`, not only the version.** `poolRevokeFixtureName` is the
  sibling to copy and `setModeFixtureName` is not — the latter interpolates its arm raw. The
  arm table is the human-edited growth point, and an arm typed as `a/b` interpolated raw mints a
  name landing one directory down from the target.
- **The `_` before the arm is load-bearing.** It is the single character keeping the two-input
  name off the one-input name for an empty arm. Say so; it is the whole content of AC 3's
  mutant.

Contract for #1713's writer, in the doc comment as `initControlFixtureName`'s already carries
it: the result is always a SINGLE CLEAN PATH COMPONENT — no separator, never `.` or `..`, for
any pair of inputs. The guarantee is lexical and rests on `versionSlug`'s character class, not
on the sampled table; it says nothing about the directory the caller picks.

### Reuse, not parallel construction

Reuse `poolRevokeNamePattern` and `anchorFixtureName` exactly as
`TestInitControlFixtureName_AvoidsCommittedFamiliesAndStaysContained` does. Do not build a
second row type and do not build a second anchoring path — a second anchoring site is how a row
goes silently vacuous, which #1696's header records at length.

The three pattern rows are a function-local literal inside the new test, mirroring #1696's and
#1661's. Both of those files already carry their own copy; a shared helper is not this package's
pattern and would add a package-scope identifier for ~25 lines. The `hazard` strings are
rewritten to name the three-arm run and what it would destroy.

### Data flow

```
initControlArms ──┐
hostileArms   ────┴─► arms  ─┐
tokens ──────────────────────┴─► initControlArmFixtureName ─► base
                                                              ├─► anchorFixtureName ─► filepath.Match(glob)   (S1)
                                                              ├─► == initControlFixtureName(token)?           (S3)
                                                              ├─► seen[name] (initControlArms only, per token) (S4)
                                                              └─► filepath.Dir(filepath.Join(targetDir, base)) (S5)

patterns ─► anchorFixtureName(control) ─► filepath.Match(glob) must be TRUE                   (S2)
```

## Concurrency model

No goroutines, no channels, no context. `t.Parallel()` on the new top-level test and on every
subtest, as every sibling in this file does. The declared arm table is read-only shared state
ranged from parallel subtests here and, later, from parallel tests in other files — that is what
the read-only doc instruction protects, and a mutation would race in a way `-race` catches only
when the runs happen to overlap.

## Error handling

- `filepath.Match` returning a non-nil error is `t.Fatalf`, not `t.Errorf`: a malformed pattern
  constant makes every comparison in the file meaningless, so continuing reports noise. Copy the
  existing message.
- Every property violation is `t.Errorf`, so one run reports every offending pair rather than
  the first.
- Each failure message names the input(s), the minted name, and the concrete loss — a reader of
  these messages is somebody about to spend real tokens on a three-arm live run. The `hazard`
  field on `poolRevokeNamePattern` exists for exactly that and must be filled in per row.

## Testing strategy

One new test, `TestInitControlArmFixtureName_AvoidsCommittedNamesStaysDistinctAndContained`,
with five subtests. No new production code, so there is nothing else to test.

### Input tables (function-local to the new test)

**Version tokens.** Adversarial, and every row earns its place:

- `2.1.239` — **required by AC 3.** It is the `claude_version` inside the committed
  `testdata/initialize_control_v2.1.239.json`, which makes S3 cover the file on disk today and
  not only the general property.
- `2.1.220`, `2.1.220 (Claude Code)`, `2_1_220`, `2.1.220-beta.1` — plausible `claude --version`
  output, including the raw parenthesised form.
- `permission_protocol_v1`, `set_permission_mode_v1`, `dropped_lines_v1` — the per-family
  smuggle tokens AC 2 requires, one per pattern.
- `..`, `../..`, `a/b`, `/abs` — separator- and dot-bearing, load-bearing in S5.
- `""`, `strings.Repeat("9", 64)` — the empty and over-length edges.

**The smuggle tokens carry the family's `v`, and the bare head does not work.** Measured with
`filepath.Match` semantics on 2026-08-24: under the mutant that derives the fixed prefix from an
input, token `set_permission_mode` mints `set_permission_mode_<arm>.json`, which matches
**nothing** — `set_permission_mode_v*_*.json` needs a literal `v` after the head and the head
alone never supplies it. Same for the other two. `..._v1` hits all three instantly. Carry the
`_v1` forms and say in the comment why the bare heads are absent, so nobody "harmonises" them
back in from #1696's table.

**Hostile arms**, this test's own literals, never appended to `initControlArms` (#1661's
`hostileArms` is the precedent and carries the same shapes):

`a/b`, `..`, `../..`, `/abs`, `""`, `set_permission_mode_v1`, `strings.Repeat("z", 64)`.

The `""` row is not filler: it is the only row that makes S3's mutant redden.

**The rows that carry a literal `/` are the ones S5 measures, and `..` is not one of them.**
#1696 confirmed this by mutation and the package overview records it: the raw-interpolation
mutant reddens only on inputs carrying `/`, because `..` interpolated raw still mints a perfectly
clean component. So `a/b`, `../..` and `/abs` are load-bearing in the arm column and must not be
dropped; `..` is kept for the `.`/`..` clause, not for containment.

**Arm domains differ per subtest, deliberately:**

| subtest | arms it ranges |
|---|---|
| S1 family globs, S3 equality, S5 containment | `initControlArms` ++ `hostileArms` |
| S4 distinctness | `initControlArms` **only** |

S4 must not range the hostile arms. Two hostile shapes that slug to one string are a property of
the hostile literals, not a defect in the namer, and folding them in would make the subtest red
against honest code. AC 4 is about the declared identifiers — the human-edited growth point.

### Subtests

- **S1 — "no minted name joins a committed family."** For every token × arm × pattern: anchor
  the minted base through `anchorFixtureName`, `filepath.Match` it, expect no match, report
  `p.hazard` on a hit.
- **S2 — "each pattern's control can still match."** For every pattern: at least one control, and
  every control anchored through the same `anchorFixtureName` must match. A pattern with no
  control is its own `t.Errorf`. Controls stay synthetic literals —
  `set_permission_mode_v0.0.0_default.json`, `permission_protocol_v0.0.0_default.json`,
  `dropped_lines_v0.0.0.json` — never a committed filename and never a directory listing, for
  the reasons #1696's control subtest states verbatim.
- **S3 — "no minted name collides with #1688's committed one-arm capture."** For every token ×
  arm: `initControlArmFixtureName(token, arm) != initControlFixtureName(token)`. String equality
  and not a pattern check: `testdata/initialize_control_v*` is matched by no glob in this
  package, so S1 cannot stand in for this. The failure message must name
  `testdata/initialize_control_v2.1.239.json` and say the overwrite is silent.
- **S4 — "distinct arms mint distinct names."** For every token, a fresh `seen map[string]string`
  over `initControlArms`; on a duplicate, `t.Errorf` naming both arms and the shared name. The
  `seen`-map shape is the one in
  `TestRealClaude_SetPermissionMode_FixtureNamesAvoidRegressionGlobs`, whose comment gives the
  reason in one line. Add this file's version of it: two arms reducing to one name overwrite
  each other, and the run then reports more arms measured than there are fixtures on disk.
  `versionSlug` is what makes this reachable — arms differing only in case, only in a folded
  character, or only past character 32 mint one name.
- **S5 — "every minted name stays directly inside its target directory."** For every token × arm,
  against a clean absolute literal `targetDir` (no trailing slash — `filepath.Join` cleans, and a
  trailing slash makes this red against a healthy name; copy #1696's comment):
  `filepath.Dir(filepath.Join(targetDir, base)) == targetDir`, plus `base != "."` and
  `base != ".."`. Nothing resolves the real `testdata/`: the namer takes no directory and the
  directory need not exist. Carry #1696's note that the `.`/`..` clause is strictly implied and
  is kept as a prefix pin, not credited as the mis-slug catcher.

### The mutant matrix — run it, do not assume it

Each criterion is the sole red for a distinct mutant. Verify all four before committing:

| Mutant | Sole red | Rows that carry it |
|---|---|---|
| arm interpolated raw, no `versionSlug` | S5 containment | hostile arms `a/b`, `/abs`, `../..` |
| fixed prefix derived from an input (e.g. `"%s_%s.json"`) | S1 family globs | tokens `permission_protocol_v1`, `set_permission_mode_v1`, `dropped_lines_v1` — **not** the bare heads |
| arm folded to a constant | S4 distinctness | any token |
| arm appended with no separator (`"initialize_control_v%s%s.json"`) | S3 equality | the empty arm row |

Two things to check per mutant, not one: the mapped subtest goes RED, **and** the other three
stay GREEN. A mutant that reddens two subtests means a row is doing double duty and the matrix's
claim is wrong; a mutant that reddens none means the table row is decoration.

Run the mutants with `go test -overlay=<abs-path>.json` so the worktree is never written:

```bash
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestInitControlFixtureName_|TestInitControlArmFixtureName_|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/
```

All three must report **PASS — not SKIP, not "no tests to run"** — on a machine with no claude
and no credentials. **Read the count of `=== RUN` lines, never the exit code:** the package is
behind the `e2e_realclaude` build tag, `make check` never compiles it, and the suite exits 0
both on a build failure and on a full credentials skip.

`TestFinOfflineFilesReachNoExecHelper` is in the filter because the new code lands in a file it
already guards. Confirm the existing entry stays green rather than editing the map.

**`-overlay` is correct for the four namer mutants and wrong for that guard.**
`TestFinOfflineFilesReachNoExecHelper` calls `parser.ParseFile` with a `nil` source, so it reads
the registered file off disk at run time; `-overlay` is a build-time mapping the test binary's
own reads never see, and an overlay-injected banned call compiles cleanly while the check parses
the pristine file and stays green. #1696 recorded this. It costs nothing here — this ticket adds
no ban entry and has nothing to prove about that guard beyond it staying green — but do not
reach for overlay if you find yourself wanting to check that the ban bites.

## Header corrections in `initialize_control_names_test.go`

Required, and part of the change rather than tidy-up:

1. **§ "One input, not two" is false where it stands** (AC 3). Replace it with a section saying
   the file now carries two namers of one family: `initControlFixtureName`, one input, #1688's
   single capture, callers unchanged; and `initControlArmFixtureName`, two inputs, #1715's three
   arms. Record that #1696's collapse of the arm dimension was correct for a one-probe family
   and expired when the family grew arms — the reason, not just the fact, so the next reader
   does not re-collapse it.
2. The same section's claim that collapsing the dimension *"removes most of the
   separator-bearing inputs, since #1661's hostileArms carried them"* is stale: hostile arms are
   back, in the arm column, and both columns now carry the separator-bearing shapes.
3. The `-run` filter in the header's runbook block must name the new test.
4. The `patterns` comment inside the existing test claims keeping the literal function-local
   *"holds this file's package-scope surface at two identifiers"*. After this change the surface
   is `initControlFixtureName`, `initControlArmFixtureName`, `initControlArms` and two tests.
   Correct the count; the reasoning (function-local because one test uses it) still holds.

Do not otherwise rewrite #1696's header, and do not touch
`TestInitControlFixtureName_AvoidsCommittedFamiliesAndStaysContained`'s body. It is green and
this ticket does not own it.

## Scope fence

- No production file changes. Nothing under `cmd/` or `internal/` outside
  `internal/e2e/realclaude/initialize_control_names_test.go`.
- `initControlFixtureName` keeps its one input and every current caller —
  `writeInitControlFixture`, `TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry`,
  `TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken`. #1713 migrates the
  writer; this slice does not.
- No `finOfflineExecBans` edit.
- Nothing consumes `initControlArmFixtureName` until #1713.
- No knowledge-base doc. The documentation phase folds this ticket's lessons into
  `docs/knowledge/features/e2e-realclaude.md` after code review.

## Open questions

- **Arm identifier wording.** `before_first_turn` / `after_completed_turn` / `control_no_request`
  are this spec's choice, picked to name #1715's send points literally. #1713 records a
  send-point field separately; if it wants an enum distinct from the arm name, that is its call —
  the identifiers here are the arm names and stay stable regardless.
- **Whether `initControlArms` becomes a row struct.** Left as `[]string` here because this slice
  needs nothing more. #1713 or #1715 grows it in place; the doc comment says so.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings, and the ticket's subject IS a boundary. The design's only
  boundary is the one between an arbitrary caller-supplied string and a filesystem path
  component, and it is explicit and single: `versionSlug`, applied to both inputs inside
  `initControlArmFixtureName`. Nothing downstream re-derives the name — `writeInitControlFixture`
  mints its path internally and #1713 keeps that shape, so no caller can hand a path in past the
  namer. The trusted side is guaranteed lexically (one clean component) and S5 is what proves
  it.
- **[File operations]** SHOULD FIX, already specified: path traversal in **both** input positions
  is the live hazard, not a hypothetical — an arm typed as `a/b` or `../..` interpolated raw
  mints a name that lands outside the caller's directory, and `setModeFixtureName` in this very
  package interpolates its arm raw. The design closes it by putting both inputs through
  `versionSlug` and proving it in S5 over hostile rows in both columns; the raw-arm mutant is
  the check that S5 is not vacuous. No TOCTOU, no `os.Stat`-then-open, no permissions decision
  and no symlink decision: the namer performs no I/O and the containment guarantee is stated as
  lexical and explicitly disclaimed for a caller-chosen directory that is or contains a symlink.
- **[File operations — the committed artifacts]** No findings. The file is banned from
  `os.WriteFile`, `os.Create`, `os.ReadFile`, `os.ReadDir` and `filepath.Glob` by its existing
  `finOfflineExecBans` entry, enforced over the AST by
  `TestFinOfflineFilesReachNoExecHelper`. That is what keeps a relative
  `os.WriteFile("testdata/…")` — `go test` runs in the package source directory — from reaching
  the very fixtures this lock protects, and what keeps the controls synthetic literals rather
  than a directory listing.
- **[Tokens, secrets, credentials]** No findings, and the guard is structural rather than
  advisory. The process environment here carries `CLAUDE_CODE_OAUTH_TOKEN` and
  `ANTHROPIC_API_KEY`; the file's existing ban entry covers `os.Getenv`, `os.Environ` and
  `os.LookupEnv`, and additionally `captureClaudeVersion` — the package's own direct
  `claude --version` exec, which returns precisely this namer's first input and is therefore the
  call a developer is most likely to reach for thinking "use the real token". Every version
  token in the new table is a literal. No token is generated, stored, logged or compared here.
- **[Subprocess / external command execution]** No findings by construction. No `exec`, no
  `sh -c`, no environment inheritance decision. The ban entry additionally covers
  `resolveClaudeBin`, `WithWorktree`, `WithWorktreeAuthenticated` and `probeClaudeVersion`,
  which exist as bans to keep a SKIP out as much as an exec: two of them skip *inside* the test
  body after `=== RUN` is printed, and a skip exits 0, which reads as a pass under
  `make e2e-realclaude`.
- **[Cryptographic primitives]** Not applicable — no randomness, no hashing, no comparison
  against a secret. `versionSlug` is a deterministic normaliser and the test's `seen`-map
  comparison is over test-local literals, not attacker-controlled values against secrets.
- **[Network & I/O]** Not applicable — no socket, no HTTP server, no read from any stream. The
  only size bound in the design is `versionSlug`'s 32-character truncation, which is a naming
  concern rather than a resource one; the spec names it under S4 because truncation is a
  *collision* vector, which is the security-relevant reading of it.
- **[Error messages, logs, telemetry]** No findings. Every failure message is built from the
  test's own literal inputs and the minted name. Nothing prints an environment value, a real
  version token, a fixture body, or child output — contrast `writeInitControlFixture`, whose
  doc comment explains why its `t.Fatalf` prints `claude_version` and nothing else. The new
  messages have no such payload to leak.
- **[Concurrency]** No findings. No goroutines and no locks, therefore no lock ordering and no
  shutdown window. The one piece of shared state is `initControlArms`, ranged from `t.Parallel()`
  subtests and later from parallel tests in other files; the design makes it read-only by
  documented contract, copying `poolRevokeArms`' wording, and hands the slice to nobody, so no
  defensive copy is needed. The hostile arms are the lock's own literals precisely so the shared
  table is never appended to at test time.
- **[Threat model alignment]** Not applicable to `docs/protocol-mobile.md` — nothing here
  touches the relay, the wire format or a device. The threat this slice does address is the
  repository's own: silent destruction of committed measurement evidence, which is what the
  whole lock exists for and which S3 covers for the file on disk today.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-24
