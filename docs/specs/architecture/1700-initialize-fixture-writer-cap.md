# #1700 — prove the `initialize` fixture writer bounds its stderr capture

Test-only. One added test function in one existing test file, plus three
doc-comment corrections in that same file. No production file changes, no new
helper, no new type, no ban-list change.

## Files to read first

| Path | Symbol | What to extract |
| --- | --- | --- |
| `internal/e2e/realclaude/inband_bypass_revoke_fixture_test.go` | `TestPoolRevokeFixture_WriterCapsChildOutputCapture` | **The template.** The realized form of this whole ticket for the #1643 family: table shape, the range bound, the prefix check, the vacuity control, the no-mutation check, the report-lengths-never-values discipline. Copy its structure. |
| `internal/e2e/realclaude/inband_bypass_revoke_fixture_test.go` | `capFixtureCapture` | The helper under proof. Read its doc comment — it already records the U+FFFD measurement this ticket's third mutant depends on. Do **not** call it from an assertion. |
| `internal/e2e/realclaude/initialize_control_writer_test.go` | `writeInitControlFixture` | The subject. Its `out := *rec` copy, its single `capFixtureCapture` call on `StderrCapture`, and its `t.Fatalf`-names-nothing-else discipline. |
| `internal/e2e/realclaude/initialize_control_writer_test.go` | `TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry` | The sibling test in the file you are editing: the tempdir/write/read-back/decode shape to mirror, and the value-printing pattern its own comment says this ticket must **not** inherit. |
| `internal/e2e/realclaude/initialize_control_record_test.go` | `initControlFullRecord` | The record to mutate. Note the doc comment's fresh-pointer-per-call clause — it names this ticket as the reason. |
| `internal/e2e/realclaude/initialize_control_record_test.go` | `initControlFixtureRecord` | The `StderrCapture` field and its `json:"stderr_capture"` tag; its doc states this type carries no bound of its own. |
| `internal/e2e/realclaude/permission_protocol_spike_test.go` | `stderrFixtureCap`, `truncateString` | The cap constant (8 KiB) and the byte-slicing helper. `truncateString` slices **bytes** — that is the whole reason the multi-byte row exists. |
| `internal/e2e/realclaude/offline_exec_ban_test.go` | `finOfflineExecBans`, `TestFinOfflineFilesReachNoExecHelper` | The twelve-name entry keyed `"initialize_control_writer_test.go"` and its doc comment. Read it to confirm what stays available; **add no name and change no entry.** |

## Context

`writeInitControlFixture` caps `StderrCapture` at `stderrFixtureCap` before
writing, because that field absorbs unbounded free text from a process this repo
does not control and the file it lands in gets committed. #1688 is the live run
that first fills the field with real claude stderr.

The cap call is currently unproven on this writer. The file's round trip runs
over `initControlFullRecord`, whose capture is a 53-byte literal against an 8 KiB
cap, so `capFixtureCapture` is a no-op there and the row compares equal on both
sides — the file's own header says so.

`capFixtureCapture` itself is already proven by #1662's cap test. What is proven
here is different and cannot be reached by calling the helper: that **this
writer** calls it, on **the right field**, and that the bound survives a write
and a read-back **from disk**.

No ADR is warranted — this is one test function following an existing realized
template.

## The mutant matrix (re-measured on `c0bc034`, 2026-08-22)

Each of the two rows is the sole red for a distinct mis-implementation. This is
the reason neither is dead weight, and it is what the design below must preserve.

| mis-implementation | on-disk `len` (ASCII) | ASCII exact-`cap` row | on-disk `len` (multi-byte) | multi-byte range+prefix row | no-mutation check |
| --- | --- | --- | --- | --- | --- |
| honest `capFixtureCapture` | 8192 | green | 8191 | green | green |
| the cap call is omitted, or applied to another string field | 9216 | RED | 10001 | RED | green |
| `truncateString(s, stderrFixtureCap)` called directly | 8192 | green | **8194** | **RED — sole** | green |
| the trim removes one byte too many | 8191 | **RED — sole** | 8191 | green | green |
| the writer caps the caller's record instead of a copy | 8192 | green | 8191 | green | **RED — sole** |

Row 3 is the measured reason the multi-byte case is a separate row rather than a
restatement. `truncateString` slices bytes; `encoding/json` does not error on
invalid UTF-8, it substitutes U+FFFD at three bytes per invalid byte. So the
direct-`truncateString` mutant reads back at **cap+2** for this literal — the
writer states a bound it does not hold — while the ASCII row, where a byte cut is
a rune cut, stays perfectly green.

Row 4 is the mirror image, and is why the ASCII assertion must be **exact** while
the multi-byte one can only be a **range**: the rune-boundary trim legitimately
removes up to `utf8.UTFMax-1` bytes, so a range is the strongest claim the
multi-byte row can make, and an over-trim hides inside it.

**The valid-UTF-8 clause is 0-red and must be labelled as such.** Measured: the
read-back is valid UTF-8 under *every* row above, including row 3 — that same
U+FFFD substitution happens on the way to disk, so a split rune surfaces as a
length and prefix violation, never as an invalid one. Carry the clause (the
template does, and it names the failure mode the range check actually detects)
but comment it as a restatement, never as coverage for a row.

## Design

One test function added to `internal/e2e/realclaude/initialize_control_writer_test.go`,
under a new `// --- the cap ---` section after the round trip.

### Name

`TestInitControlFixture_WriterCapsStderrCapture` — mirroring
`TestPoolRevokeFixture_WriterCapsChildOutputCapture`.

The name is load-bearing beyond convention: the file header documents an offline
run command whose `-run` filter is prefix-scoped to `TestInitControlFixture_`, so
this name falls inside the documented filter and the header's command needs no
change. Verified: `go test -run` matches each `/`-separated element unanchored,
so `TestInitControlFixture_WriterCapsStderrCapture` matches
`TestInitControlFixture_`. **Any other name requires updating the header's
command in the same change** — a documented command that silently stops selecting
the file's own tests reads as a pass while proving nothing.

### Table shape

Two rows. There is deliberately no under-cap row: both `capFixtureCapture` and
`truncateString` carry a `len(s) <= max` early return, so an under-cap input
reddens nothing. #1662's header records this as measured.

```go
tests := []struct {
    name    string
    capture string
    // precheck asserts a property of this row's OWN literal that the row's
    // ability to discriminate depends on. nil where the row has none.
    precheck func(t *testing.T, capture string)
    check    func(t *testing.T, capture, back string)
}{ /* over_cap_ascii, over_cap_multibyte */ }
```

Row literals, both lifted from the template:

- `over_cap_ascii` — `strings.Repeat("A", stderrFixtureCap+1024)`.
- `over_cap_multibyte` — `"A" + strings.Repeat("é", 5000)`. One leading ASCII
  byte then two-byte runes, so rune starts land on odd byte indices and the byte
  at `stderrFixtureCap` is a **continuation** byte. Verified: `utf8.RuneStart` at
  that index reports `false` for this literal and `true` for the ASCII one.

### The vacuity control — a per-row `precheck`, not a name comparison

The multi-byte row must `t.Fatalf` — not skip, not `Errorf` — when
`utf8.RuneStart(capture[stderrFixtureCap])` is true. If a later edit to the
literal moves the boundary onto a rune start, `truncateString` alone no longer
splits a rune, matrix row 3 goes green, and the row proves nothing while still
passing. A row that cannot discriminate is a broken instrument, not a passing
test.

**Divergence from the template, deliberate.** The template guards this with
`if tc.name == "over_cap_multibyte"` in the loop body. Attach it to the row
instead, via the optional `precheck` field above. The reason is not style: a
name-string match stops firing silently if anyone renames the row, which is
exactly the class of silent degradation the control exists to prevent, and with
only two rows here a name-keyed branch reads worse than it does in the
template's three. The `precheck` runs before the write, inside the subtest.

### Per-subtest body

Order matters; each step is a distinct matrix row.

1. `t.Parallel()`.
2. Run `tc.precheck` if non-nil.
3. `rec := initControlFullRecord()` — **inside** the subtest. The fresh pointer
   per call is why `initControlFullRecord` is a function and not a package-level
   var; hoisting it to the parent makes these parallel subtests a `-race` data
   race. Its doc comment names this ticket as the reason.
4. `rec.StderrCapture = tc.capture`.
5. `path := writeInitControlFixture(t, t.TempDir(), rec)` — a fresh tempdir per
   row, so the shared minted filename cannot collide.
6. **No-mutation check**, before the read-back: compare
   `len(rec.StderrCapture)` against `len(tc.capture)` and `t.Errorf` on
   mismatch. Scoped to the capped field **deliberately** — the writer's `out :=
   *rec` is a shallow copy sharing every slice header with the caller, which is
   sufficient today only because the sole mutation is to a string field. No row
   of the matrix violates the slice-valued fields; do not widen the check to
   them. The shallow-copy hazard is a note for whoever caps a slice field later.
7. `os.ReadFile(path)` → `json.Unmarshal` into a fresh `initControlFixtureRecord`
   → `tc.check(t, tc.capture, back.StderrCapture)`.

### The two `check` funcs

**`over_cap_ascii`** — one clause, exact:

- `len(back) != stderrFixtureCap` → `Errorf`. Exactness is what makes this the
  sole red for the over-trim mutant; a range here would swallow it.

**`over_cap_multibyte`** — three clauses, in this order:

- `utf8.ValidString(back)` — carry it, comment it as the **restatement** the
  matrix section describes. It names the failure mode the range check detects
  and is 0-red on its own.
- `len(back) > stderrFixtureCap || len(back) < stderrFixtureCap-(utf8.UTFMax-1)`
  → `Errorf`. This is the clause that reddens on row 3 (8194 > 8192).
- `!strings.HasPrefix(capture, back)` → `Errorf`. The second discriminating
  clause: it catches the writer rewriting content rather than trimming a split
  tail, and it also reddens on row 3 (the U+FFFD is not in the original).

### Assert on disk, never through the helper

Every assertion runs over the bytes read back out of the written file. A direct
call to `capFixtureCapture` inside an assertion collapses matrix rows 2 and 3 to
green, because it would not catch a writer that never applied the cap. Every
length assertion is on `len()`, never on rune count — the cap is a byte cap.

### Do not print the capture

Failure messages report lengths and at most a short prefix
(`back[:min(16, len(back))]`, as the template does). A `%q` of `want`/`got` here
dumps up to 8 KiB of child output into an unbounded run log, which defeats the
field's entire purpose. The round trip's own comment in this file already names
this ticket as the reason its value-printing pattern must not be inherited.

### Imports

`strings` and `unicode/utf8` join the file's existing list. Every other symbol
needed (`encoding/json`, `os`, `testing`) is already imported; `path/filepath`,
`reflect` and `sort` stay in use by the round trip.

### Ban list — no change

`finOfflineExecBans`'s twelve-name entry for this file already leaves
`os.ReadFile`, `os.WriteFile`, `os.Create`, `os.ReadDir` and `filepath.Glob`
available, precisely because a write, a read-back and a directory listing are
this file's subject. Nothing this test needs is on the list; `strings.Repeat`
and the `utf8` helpers are not banned names. **Add no entry and no name** —
`TestFinOfflineFilesReachNoExecHelper` must stay green with the map untouched.

Note especially that `captureClaudeVersion` is banned here. Do not reach for it
to build a record: `initControlFullRecord` already returns a fully-populated one.

## Doc-comment corrections in the same file

This change falsifies three claims the file currently makes about itself. All
three are one-clause edits, in the file the developer is already editing.

1. **The header's three-property sentence.** It currently states the file settles
   COMPLETE and ATOMIC. With this test the file settles all three, BOUNDED
   included.
2. **The header's "What this file deliberately is not" paragraph.** It currently
   says the file does not prove the bound and routes both BOUNDED and the
   no-mutation contract to #1700 as if elsewhere. Rewrite so the scope limit sits
   where it is still true — on `TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry`,
   whose row genuinely is a no-op against the 53-byte fixture capture — and so
   the file's own new test is named as what proves BOUNDED.
3. **The pointers that route the reader out of the file.** The round trip's
   "that row is #1700's whole subject" clause and `writeInitControlFixture`'s
   "both are #1700's" clause should name
   `TestInitControlFixture_WriterCapsStderrCapture` instead. Same file now; a
   ticket number reads as elsewhere.

Cite symbols, never line numbers — `make cite-guard` is diff-scoped and fails on
any `//`-comment citation resolving into a declaration. There is no depth
exemption and no range exemption.

## Concurrency model

No goroutines. The new test is `t.Parallel()` at the parent and at each subtest,
matching the template. The only shared-state hazard is `initControlFullRecord`,
resolved by calling it inside each subtest as § "Per-subtest body" step 3
requires. Everything else is per-subtest local: its own `t.TempDir()`, its own
record, its own read-back.

## Error handling

Three tiers, matching the template and the file's existing discipline:

- **`t.Fatalf`** — the vacuity control (a row that cannot discriminate is broken,
  not failing), and the I/O and decode steps (`os.ReadFile`, `json.Unmarshal`),
  where continuing produces meaningless comparisons. `writeInitControlFixture`
  already `t.Fatalf`s internally on mkdir/marshal/write/rename.
- **`t.Errorf`** — every claim about the bound and the no-mutation contract, so
  one row's failure does not hide the other's.
- **Message content** — lengths, the cap constant, the row's diagnosis, and at
  most a 16-byte prefix. Never the capture, never a `%+v` of the record.

## Testing strategy

This is the test. Verification is running it, plus the baseline it must not
disturb.

Measured on `c0bc034` before any change: the file header's own documented
command runs **13** tests, all PASS, zero skips. The wider offline set the ticket
body names (`TestInitControlFixture_|TestFinOfflineFilesReachNoExecHelper|TestPoolRevokeFixture_WriterCaps`)
runs **17**, all PASS, zero skips.

After this change, both counts rise by exactly 3 — one parent plus two
subtests — to **16** and **20**:

```
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestInitControlFixture_|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/
```

**Read the count of `=== RUN` lines, never the exit code.** This package is
behind the `e2e_realclaude` build tag, `make check` never compiles it, and the
suite exits 0 both on a build failure and on a full credentials skip. All tests
must report PASS — not SKIP, not "no tests to run" — on a machine with no claude
binary and no credentials. This ticket settles entirely offline: every write
lands in a fresh `t.TempDir()` and nothing touches the committed `testdata/`.

Also run `make check` (it will not compile this package, but it gates
`cmd/cite-guard` over the branch diff and `gofmt`).

**Optional confidence step, no worktree write required.** Every row of the mutant
matrix above was verified by running the four implementations through
`json.MarshalIndent` and back. To re-confirm against the real writer, use
`go test -overlay=<abs-path-to-json>` with a patched copy of
`initialize_control_writer_test.go`; that mutates nothing on disk in the
worktree.

## Open questions

None blocking. Two decisions recorded rather than deferred:

- The `precheck` field is a deliberate divergence from the template's name-keyed
  guard, for the silent-rename reason given above. If code review prefers byte
  parity with the sibling, the name-keyed form is a mechanical revert of one
  field and one call site.
- The no-mutation check compares lengths rather than full strings, exactly as the
  template does. A full string comparison is strictly stronger and equally safe
  (the message still prints only lengths), but no row of the matrix distinguishes
  the two, so the template's form ships.

## Security review

**Verdict:** PASS

The subject of this ticket *is* a security control — bounding untrusted,
potentially credential-bearing child stderr before it reaches a committed file —
so the categories below are walked against that control, not against the test as
an inert artifact.

**Findings:**

- **[Trust boundaries] SHOULD FIX — residual named for #1688, not fixable here.**
  The boundary is explicit and single: claude's stderr crosses into a committed
  file at exactly one point, `writeInitControlFixture`'s `capFixtureCapture`
  call, which is what this ticket proves. But the cap is **field-scoped, not
  record-scoped**, and the spec must not be read as proving "the committed
  artifact is bounded". `StdoutEvents`, `StdinWriteErrors` and
  `ModelsEntryFields` are equally child-derived and deliberately uncapped.
  `poolRevokeFixtureRecord`'s doc carries the routing constraint that makes the
  sibling family's cap meaningful — the free-text sink is the designated field
  and a failure stream *must not be routed into* the structured-evidence one.
  `initControlFixtureRecord`'s doc cites that paragraph but quotes only its
  capping half, so nothing in the tree currently tells #1688 not to route child
  stderr into `StdoutEvents`. If it does, this ticket's proof stays green and the
  artifact is unbounded anyway. The doc fix belongs to the record (#1701) or to
  #1688; widening this test would contradict the ticket's explicit instruction
  not to widen the no-mutation check to the slice-valued fields.

- **[Tokens, secrets, credentials] SHOULD FIX — state what BOUNDED does and does
  not buy.** `stderrFixtureCap` is 8 KiB. An OAuth token is orders of magnitude
  smaller, so a credential appearing in the first 8 KiB of an auth-failure dump
  still reaches the committed fixture. The cap bounds **volume, not secrecy** —
  it stops a megabyte of child spew from being committed; it is not redaction and
  must not be described as one. #1688 commits the artifact and remains
  responsible for reading the captured bytes before committing them. Not
  actionable in this slice: the ticket is test-only and adding redaction would be
  a production behaviour change. No findings on this test's own credential
  surface — it builds synthetic literals and cannot read the process
  environment (see Subprocess below).

- **[File operations] No findings for this ticket's surface; two items owned
  elsewhere.** Path traversal through the version token is `versionSlug`'s
  character class plus `initControlFixtureName`, already pinned adversarially by
  `TestInitControlFixtureName_AvoidsCommittedFamiliesAndStaysContained` (#1696),
  whose containment subtest runs traversal-shaped tokens through the namer; this
  ticket's two rows reuse one synthetic `ClaudeVersion` and add no surface. File
  modes (`0o644` file, `0o755` dir) are inherited from `writeInitControlFixture`
  unchanged and are *not* the operative control here — the artifact is committed
  to a public repo by design, so the commit is the exposure, not the mode. The
  relative-path escape to the real `testdata/` cannot be closed by a ban in this
  file (it legitimately needs `os.WriteFile`); it is pinned by
  `TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry`'s
  exactly-one-entry assertion, and the new rows degrade **loudly** if it recurs —
  an escaping writer returns a path that does not exist and the read-back
  `t.Fatalf`s. TOCTOU and symlink handling are not applicable: temp-file-plus-
  rename inside a per-row `t.TempDir()`, with no second party able to reach it.

- **[Subprocess / external command execution] No findings — enforcement is
  structural, and the spec leaves it intact.** This test spawns nothing.
  `finOfflineExecBans`' entry for this file bans `resolveClaudeBin`,
  `probeClaudeVersion`, `captureClaudeVersion`, `WithWorktree`,
  `WithWorktreeAuthenticated`, `os.Getenv`, `os.Environ` and `os.LookupEnv`, and
  `TestFinOfflineFilesReachNoExecHelper` enforces them over the file's **AST**,
  parsed *without* `parser.ParseComments`, so the check cannot satisfy itself out
  of the header prose that names the same symbols. The spec adds no name and
  changes no entry, so the enforcement is unchanged, and it explicitly forbids
  reaching for `captureClaudeVersion` to build a record.

- **[Cryptographic primitives] Not applicable — by a load-bearing design
  decision, not by absence.** Both row literals are fixed (`strings.Repeat`) and
  must never be randomised. A randomised multi-byte capture makes the byte at
  `stderrFixtureCap` nondeterministic, so the vacuity control would fire
  intermittently and the row's discrimination would become a coin flip.
  Determinism here is a property of the instrument.

- **[Network & I/O] One item named out of scope.** No network surface. The
  input-size-limit question is this ticket's whole subject — but #1688's pipeline
  carries **two** bounds and this ticket proves only the second: `ScannerError`
  exists on the record because #1688 caps its reader **per line** at read time,
  while `capFixtureCapture` caps the total at write time. A per-line cap that
  silently truncated instead of surfacing through `ScannerError` would leave both
  this test and the record's contract green. That belongs to #1688.

- **[Error messages, logs, telemetry] No findings — the discipline is specified,
  and the exposure channel is named.** The operative channel is the `-v` run log,
  which is unbounded and routinely pasted into PR comments; that is why "report
  lengths" is a security requirement and not tidiness. MUST-NOT-print: the
  capture, a `%+v` of the record, `want`/`got` capture values. MUST-print:
  lengths, the cap constant, the row's diagnosis, and at most a 16-byte prefix.
  The 16-byte prefix is safe **only because this test's literal is synthetic** —
  the spec says so, and says #1688 must not inherit even that much.

- **[Concurrency] No findings; one ordering obligation made explicit rather than
  assumed.** `initControlFullRecord` must be called **inside** each parallel
  subtest (§ "Per-subtest body" step 3). Hoisting it to the parent makes step 4's
  `rec.StderrCapture = tc.capture` a `-race` data race across two goroutines —
  precisely the race `initControlFullRecord`'s fresh-pointer-per-call doc
  predicts and names this ticket for. The per-row `t.TempDir()` is likewise
  mandatory and not stylistic: both rows mint the **same** filename from the same
  `ClaudeVersion`, so a shared directory would have them overwrite each other's
  artifact under a racing read-back. Table rows hold immutable strings after
  construction; no goroutines are spawned; nothing outlives the subtest.

- **[Threat model alignment] No findings.** `docs/protocol-mobile.md` § Security
  model does not apply — no relay, no wire surface, no network. The applicable
  model is the committed-artifact one both fixture records state: **no `env`
  field, ever** — the credential reaches the child through the environment while
  the argv carries none, which is what makes recording argv safe and recording
  env not. This spec adds no field to `initControlFixtureRecord` and the ban
  entry blocks the environment readers, so that invariant is untouched.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-22
