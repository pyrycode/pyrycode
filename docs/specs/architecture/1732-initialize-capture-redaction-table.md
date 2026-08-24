# #1732 — build the `initialize` capture's redaction table from its caller's values alone

**Ticket:** [#1732](https://github.com/pyrycode/pyrycode/issues/1732) · size `s` · `security-sensitive`
**Scope:** test-only, `internal/e2e/realclaude` (behind the `e2e_realclaude` build tag). No production file changes.

---

## Files to read first

Read these before writing anything. This list is the turn-1 data load; the design below assumes you have it.

- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapRedactor`, `dropcapRule`, `add`, `redact` — the mechanism reused **unchanged**. Extract two properties from `add`: the empty-value guard (`strings.ReplaceAll(s, "", x)` inserts `x` between every character), and that it dedups by *value* keeping the **first** rule that claimed it. Both are load-bearing below.
- same file → `dropcapPathSpellings`, `dropcapSlug`, `dropcapSlugSeparators` — the spelling enumeration (path, its `filepath.EvalSymlinks` form, the project-slug encoding of each) and the `/`, `_`, `.` → `-` map. Extract: a path with no resolved form contributes **two** spellings, not four.
- same file → `newDropcapRedactor` — the constructor this ticket must **not** reuse. Extract the two things that make it unusable here: it reads `realHome` and `os.TempDir()` on its own, and it formats a `nonce int64` with `strconv.FormatInt`, which never returns `""` so the empty-value guard cannot reach it.
- same file → the class constants `dropcapClassWorkdir`, `dropcapClassTempHome`, `dropcapClassOperatorHome`, `dropcapClassTempDir` — the four names reused verbatim. Note `dropcapClassNonce`, `dropcapClassSessionID` and `dropcapClassFIFOPath` exist and are **not** reused.
- same file → `substitutions` — inherited unchanged; not this slice's subject (see § Non-goals).
- `internal/e2e/realclaude/fixtures.go` → `realHome` — the package var the new construction must never reach, and one of the two names the ban entry adds.
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans` and `TestFinOfflineFilesReachNoExecHelper` — the map to append to and the AST check it drives. Extract: the check parses **one file's tree** and matches identifiers, so it is per-file syntax and not call-graph; and it produces a subtest only for files that have an entry, so a missing entry is silent.
- same file → the `initialize_control_record_test.go` and `initialize_control_writer_test.go` entries — this family's standing set, and the prose shape and length an entry is expected to carry.
- `internal/e2e/realclaude/initialize_control_names_test.go` → the file header — the family's header shape and the `initControl…` identifier-prefix convention this file follows.
- `internal/e2e/realclaude/permission_protocol_spike_test.go` → `captureClaudeVersion` — one of the banned names; confirms it resolves to a real declaration (AC5).
- `docs/knowledge/features/e2e-realclaude.md` § the `finding_staging_fill_test.go` (#1304) entry and the `finding_run_gather_test.go` (#1284) entry — the two recorded lessons this design inherits: *redaction and scanning are deliberately different fabric*, and *a redaction test can be vacuous in ways that pass*. Read them before deciding how much each row proves.

---

## Context

This family's committed artifact, `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json`, carries operator paths right now — re-measured at `df126f8`: `argv[0]` under the operator's home, and the `system`/`init` line's `cwd` and `memory_paths.auto` under `/private/var/folders/…` and `/var/folders/…`. Three of the five fixed classes this package's own deny-scan arms, in a file that got a clean bill from two independent human reads.

The fix splits in two. **This slice ships the table** — its rules, their spellings, their ordering — and proves it over raw bytes with no record involved. **#1733** is the pass that applies it to the capture record and wires it at the fill site; it consumes this construction and the divergent-directory helper below. Redaction rewrites values the harness *knows* it produced (this slice and #1733); the deny-scan (#1729) is the fail-closed net for the ones it did not predict. Different fabric, deliberately.

The mechanism already exists and fits unchanged. What does not fit is `newDropcapRedactor`, and that constructor is this ticket's whole subject.

**No ADR warranted.** The "different fabric" principle it would record is already stated in `docs/knowledge/features/e2e-realclaude.md`'s #1304 entry; this slice applies it rather than establishing it.

---

## Design

One new file, `internal/e2e/realclaude/initialize_control_redaction_test.go`, plus one appended entry in `finOfflineExecBans`. Nothing else.

### Why a new constructor rather than a wrapper

`newDropcapRedactor` fails this ticket's first criterion in two independent ways, and both are *shapes*, not bugs:

1. It reads the environment twice on its own — `realHome` arms `operator_home`, `os.TempDir()` arms `temp_dir` — so a redactor "constructed from synthetic values" still carries two machine-dependent rules.
2. `nonce int64` is formatted with `strconv.FormatInt`, which never returns `""`. The empty-value guard in `add` therefore cannot reach it, and **no `int64` avoids installing a rule**. Passing `0` installs a rule that rewrites every `0` byte it meets — and #1733's consumer record carries `TurnBoundaries: []int{0, 7}` and a trailer with `TotalCostUSD: 0`.

A construction that takes **every path value as a parameter** and takes no nonce, no session id and no FIFO path makes both failures *impossible* rather than guarded against. It also collapses two constructions into one: this slice's fully-test-determined table and #1733's "the construction the capture actually ships" become the same function called with different arguments, so #1733's own no-captured-bytes criterion runs against the shipped construction rather than a rule table hand-built beside it.

### The construction

```go
// newInitControlRedactor builds this family's substitution table from the four
// path values its caller hands it and from nothing else.
func newInitControlRedactor(operatorHome, tempHome, workdir, tempDir string) *dropcapRedactor
```

Contract:

- For each parameter: `strings.TrimSuffix(v, "/")`, then one rule per `dropcapPathSpellings` result, all sharing that class's name and replacement. An empty parameter arms nothing (`dropcapPathSpellings` returns nil for `""`, and `add`'s guard is the second line of defence).
- Class/replacement pairs, reused verbatim: `dropcapClassWorkdir`/`$WORKDIR`, `dropcapClassTempHome`/`$TEMP_HOME`, `dropcapClassOperatorHome`/`$HOME`, `dropcapClassTempDir`/`$TMPDIR`.
- **Add order is innermost-first**: workdir, temp home, operator home, temp dir. `add` dedups by value and keeps the first rule that claimed it, so when two classes are handed the same directory the *inner* class owns the placeholder. `newDropcapRedactor` orders the same way for the same reason; do not reorder alphabetically.
- Ends with the same `sort.SliceStable` by descending value length that `newDropcapRedactor` ends with. This is the property AC2 proves.
- Returns the shared `*dropcapRedactor`. No new type: #1733 needs `str`, `strs` and `substitutions` off the same value.

**The trailing-slash trim moves here, and that is deliberate.** `newDropcapRedactor` normalises exactly one of its two environment reads — `strings.TrimSuffix(os.TempDir(), "/")` — and it is not cosmetic. Re-measured 2026-08-24 on this platform: `os.TempDir()` returns `/var/folders/…/T/` **with** the slash. An untrimmed rule rewrites `…/T/TestRealClaude…` to `$TMPDIRTestRealClaude…`. Once the value arrives as a parameter the trim has to live somewhere, and the construction is the right somewhere — at #1733's call site nothing catches the miss. AC1's "exactly the spellings of the path values passed" names the trim as the one permitted normalisation: the point is that each rule be **decidable from the caller's argument**, not that the argument be copied verbatim.

### The divergent-directory helper

```go
// initControlDivergentDir returns a directory whose filepath.EvalSymlinks form
// genuinely differs from the spelling it hands back, on macOS and on Linux.
func initControlDivergentDir(t *testing.T) (handed, resolved string)
```

Contract: create a real directory under `t.TempDir()`, create a symlink beside it pointing at that directory, resolve the symlink, and `t.Fatalf` if the resolved form equals the handed one. Returns the symlink path and its resolved form.

**Why a symlink the test creates, rather than `t.TempDir()` alone.** Measured 2026-08-24: on macOS `t.TempDir()` already diverges (`/var/folders/…` → `/private/var/folders/…`), on Linux it typically does not — so a row resting on `t.TempDir()` alone is green here and **green-and-vacuous** there. The symlink makes the last path segment differ (`link` vs `real`), and that difference exists on every platform. Measured output of the enumeration over the symlink path: four spellings, the two resolved-form ones carrying both the `/private/var` mapping *and* the different basename.

**It is extracted rather than inlined because #1733 needs the same divergent value for its own fixture**, and a second hand-rolled copy there is a second thing to get wrong. The `t.Fatalf` lives in the helper so #1733 inherits the loud failure; the row asserts the divergence again on the returned pair, because AC3 requires the row itself to fail loudly before it asserts any substitution.

### Data flow

```
caller's four path strings
      │  TrimSuffix(v, "/")           ← the one permitted normalisation
      ▼
dropcapPathSpellings(v)               ← path, EvalSymlinks form, slug of each
      │                                 (2 spellings for a synthetic path,
      ▼                                  4 for one that resolves elsewhere)
dropcapRedactor.add(class, repl, s)   ← empty-value guard, first-claimer dedup
      ▼
sort.SliceStable, len(value) desc     ← AC2's subject
      ▼
dropcapRedactor.redact([]byte)        ← inherited unchanged
```

### Non-goals

- **No row asserts `substitutions()`.** The applied-class census is the record pass's subject (#1733, feeding #1731's on-record report); this slice proves the table over raw bytes. AC2 compares expected **bytes** precisely because the census is not available to it as evidence here.
- **No production caller.** Nothing in `make check` compiles this package; the file's own rows are the construction's consumers until #1733 lands.

---

## Testing strategy

Five rows and two helpers is the whole build. Every row is offline and deterministic: it runs on a machine with no claude binary and no credentials, and it must **PASS**, never SKIP.

### Row 1 + Row 2 — the table arms only what its caller hands it (AC1)

One table-driven test, two rows.

- **Row 1 — all four handed, one with a trailing slash.** Nested synthetic values plus an unrelated operator home: temp dir `/synthetic/tmp/` (trailing slash, deliberately), temp home `/synthetic/tmp/home`, workdir `/synthetic/tmp/home/work`, operator home `/synthetic/operator/home`.
- **Row 2 — operator home and system temp absent.** Both handed `""`; temp home and workdir as above.

Assertion for both: the set of `(class, replacement, value)` triples the construction holds equals the set assembled **from the row's own inputs** — for each non-empty input, `dropcapPathSpellings(strings.TrimSuffix(v, "/"))` paired with that class's two constants. Compare as sorted slices and report **both** directions; the *extra* direction is the one this row exists for.

Why each half is non-vacuous:

- Asserted over **values, not class names**. A construction that arms a synthetic operator home *and* also arms `realHome` behind the caller's back holds the same class *set*, so a class-set comparison is green against exactly the mutant this row targets.
- The expected set is assembled from the row's inputs and **never read back off the table under test** — reading it back asserts nothing.
- The trailing slash makes the trim decidable rather than assumed. Verified 2026-08-24: a construction that dropped the trim arms `/synthetic/tmp/` and `-synthetic-tmp-`; the trimmed construction arms `/synthetic/tmp` and `-synthetic-tmp`. Different value set, so the row reddens.
- Row 2 is where a `realHome` / `os.TempDir()` fallback surfaces: with those two classes handed nothing, a fallback shows up as extra triples the row's own inputs cannot explain.
- Every value is one the test controls, so both rows run with no claude and no credentials.

### Row 3 — the measured composite shape, under longest-first ordering (AC2)

- Values **must nest**: temp dir ⊃ temp home ⊃ workdir. `/synthetic/tmp` ⊃ `/synthetic/tmp/home` ⊃ `/synthetic/tmp/home/work`.
- Input assembled from the row's own values: temp home, then `/.claude/projects/`, then `dropcapSlug(workdir)`, then `/memory/`. **Never from the measured bytes**, which name the operator's machine.
- Expect exactly `$TEMP_HOME/.claude/projects/$WORKDIR/memory/`.

Why the nesting is load-bearing and why the comparison is against bytes:

- The nesting is what makes each class's slug spelling a substring of the next. **Over unrelated values both orderings agree and the row decides nothing.** Re-verified 2026-08-24 over these exact synthetic values: longest-first yields the expected bytes; shortest-first yields `$TMPDIR/home/.claude/projects/$TMPDIR-home-work/memory/`.
- **Absence decides nothing here.** Both orderings were also run against the measured values and neither leaves a denied value behind — the machine-identifying token sits inside `$TMPDIR` either way, so a deny-scan and an absence assertion both pass on the mangled string. What shortest-first destroys is the *placeholder assignment*: every path credited to `temp_dir`, and a value no reader can map back to a shape. That is why this row compares expected bytes.

### Row 4 — a path is substituted under its resolved spelling too (AC3)

- Take `handed, resolved` from `initControlDivergentDir`. **Assert `handed != resolved` first**, failing loudly — a row that discovers no divergence must report that rather than pass.
- Construct with the divergent directory as the **only** armed class (workdir); the other three handed `""`. No cross-class shadowing can then explain the result, and the "a class handed no value arms nothing" property rides along.
- Payload embeds all four spellings — handed, `dropcapSlug(handed)`, resolved, `dropcapSlug(resolved)`. Expect all four rewritten to `$WORKDIR`.
- The four cannot shadow one another: the last segment differs between handed and resolved, so neither slug contains the other as a substring (verified 2026-08-24 on this platform).
- The measured `cwd` leak is precisely this forgotten form: the harness built the workdir under the pinned `$HOME` in its `/var/folders/…` spelling and handed that to `cmd.Dir`; macOS resolved it on the way through the child, and a table carrying only the spellings it was handed leaves `cwd` untouched.
- The directory has to be **real**. Re-measured 2026-08-24: `filepath.EvalSymlinks("/synthetic/tmp")` returns `lstat /synthetic: no such file or directory`, so a construction handed only invented paths enumerates two spellings per class and a construction that forgot the resolved form is green on every such row.

### Row 5 — bytes carrying none of the handed values come back identical (AC4)

- Construct with Row 3's four nested values, so the table is at its widest.
- Input carries **none** of the handed values and the shape #1733's record pass will meet: `0` and `7` as `turn_boundaries` entries, a `total_cost_usd` of `0`, and `<`, `>`, `&` riding along in a text field.
- Assert byte-identical.
- **The discriminating byte is `0`.** This row reddens on a construction that inherits the sibling's `nonce int64` parameter: `strconv.FormatInt` never returns `""`, so `add`'s empty-value guard cannot stop such a rule, and passing `0` installs one that rewrites every `0` byte it meets. A byte that changed changed because a rule the table should not hold fired — no prompt-nonce rule, no session-id rule, no FIFO-path rule.

### Row 0 — the ban entry (AC5)

No new test. Append one entry to `finOfflineExecBans`, keyed `"initialize_control_redaction_test.go"`, carrying ten names:

`resolveClaudeBin`, `WithWorktreeAuthenticated`, `WithWorktree`, `probeClaudeVersion`, `captureClaudeVersion`, `os.Getenv`, `os.Environ`, `os.LookupEnv`, `realHome`, `os.TempDir`.

- The first four keep a **SKIP** out — `resolveClaudeBin` and `WithWorktreeAuthenticated` skip *inside* the test body, after `=== RUN` is printed, and a skip exits 0, which reads as a pass under `make e2e-realclaude`.
- `captureClaudeVersion` follows this family's three nearest entries; do not harmonise it away against the older siblings that omit it.
- The three environment readers are the credential guard — this process environment carries `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY`.
- `realHome` and `os.TempDir` are the two names this ticket adds and are precisely the two values AC1 says must arrive as parameters. The check matches a bare `*ast.Ident` as well as a dotted selector, so the plain `realHome` reference is caught.
- Every name was verified 2026-08-24 to resolve to a real declaration: `resolveClaudeBin` in `resilience_test.go`, `WithWorktree` and `WithWorktreeAuthenticated` and `realHome` in `fixtures.go`, `probeClaudeVersion` in `background_trigger_probe_test.go`, `captureClaudeVersion` in `permission_protocol_spike_test.go`, and the four `os` functions in the standard library. **A misspelled ban name is the one defect the check itself cannot report**, so re-verify rather than copy from memory.

Deliberate absences, and each entry's prose should say which:

- **The `packageDir` / `os.WriteFile` group the three nearest sibling entries carry is absent on purpose.** Those entries fence a file off from the committed `testdata/` because their subject *is* a fixture, and `go test` runs in the package source directory so a relative `os.WriteFile("testdata/…")` reaches the real artifacts. This file has no writer, no reader and no fixture: it builds a table and substitutes into byte slices. Adding the group would ban names the file has no route to anyway.
- **`t.TempDir` is absent** — Row 4's directory has to be created somewhere, and that is where. #1651's entry is the precedent for the same carve-out.
- `filepath.EvalSymlinks`, `os.Symlink` and `os.MkdirAll` stay available: they are the divergent-directory helper's own mechanism.

**The entry is a review obligation, not a self-check.** `TestFinOfflineFilesReachNoExecHelper` drives its subtests from `for f := range finOfflineExecBans`, so a file with **no** entry produces no subtest and no failure. Nothing detects a missing entry. The check is also per-file **syntax**, not call-graph — it parses one file's tree and matches identifiers — so `realHome` and `os.TempDir()` stay reachable through a helper the file calls while the ban stays green. The ban catches a direct reference; AC1's armed-values assertion catches a reference through a helper. Neither substitutes for the other; say so in the entry's prose.

**#1673 and #1674 also append to this map.** Entries are keyed per file and semantically independent, so the only exposure is a textual rebase conflict in one map literal. **Append; do not reformat neighbouring entries.**

### Running it

`make check` never compiles this package — it is behind the `e2e_realclaude` build tag — and the live suite exits 0 both on a build failure and on a full credentials skip. **Read the count of tests that executed, never the exit code.**

```bash
go vet -tags e2e_realclaude ./internal/e2e/realclaude/
go test -tags e2e_realclaude -race -count=1 \
  -run 'TestInitControlRedactor|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/ -v
```

Count the `=== RUN` lines. Every one of this file's rows must read PASS. Zero `=== RUN` means the package did not build.

### Mutants — each must redden exactly its row

Run these with `go test -overlay=<abs-path json>` so nothing is written into the worktree, and run the **whole file** each time so you can see which rows are the sole reds.

| Mutant | Expected sole red |
|---|---|
| Re-add `addPath(dropcapClassOperatorHome, "$HOME", realHome)` to the construction | Rows 1 and 2 |
| Drop the `strings.TrimSuffix(v, "/")` | Row 1 |
| Flip the final sort to ascending value length | Row 3 |
| Enumerate `[]string{v, dropcapSlug(v)}` instead of calling `dropcapPathSpellings` | Row 4 |
| Re-add a `nonce int64` parameter and its `strconv.FormatInt` rule, passing `0` | Row 5 |

A mutant that reddens no row means that row is vacuous; a mutant that reddens every row means the rows are not isolating what they claim.

---

## Concurrency model

No goroutines, no channels, no shutdown sequence. Every row calls `t.Parallel()`, and each builds its **own** redactor — `dropcapRedactor`'s counters are unsynchronised on purpose (its own doc comment states that all redaction runs on the test goroutine), so a shared redactor across parallel rows would be a data race under `-race`. Do not hoist one to package scope.

## Error handling

- `initControlDivergentDir` uses `t.Fatalf` for `MkdirAll` / `Symlink` / `EvalSymlinks` failure and for a non-divergent result — the row cannot continue meaningfully past any of them, and a silent pass is the exact failure AC3 forbids.
- The value-set comparison in Rows 1–2 uses `t.Errorf` and reports **both** missing and extra triples, so a file that acquired two problems is not one edit away from clean and the reader learns that before starting.
- Rows 3–5 use `t.Errorf` with got/want bytes quoted with `%q`.

## Open questions

- **Does #1733 want `initControlDivergentDir` to take a name suffix** (so it can create two distinct divergent directories)? Ship it parameterless; widening it is a one-line change #1733 can make when it knows.
- **The ban entry's prose length.** The three nearest sibling entries run 37–47 inserted lines each, mostly prose. Follow the shape, but the ticket's sizing pointer is explicit that comment density is what carried #1701 past 400 lines — write the decisions a reader cannot recover from the code, and stop.

---

## Size check

Re-applied to this written spec, 2026-08-24.

| Limit | Boundary | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **0** — both files are `_test.go` |
| Total written work | ≤ 400 lines | **~355** (see below) |
| New exported types or interfaces | ≤ 5 | **0** — all identifiers unexported, no new type |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — new construction, no production caller; #1733 is the caller and is a separate ticket |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches in a state machine | ≤ 10 | **0** — no state machine |

**Line estimate against measured analogues.** All three are this family's own new-file-plus-ban-entry shape, and all three are insertions-only; counts re-derived from `git show --stat`, reading the summary line: #1696 `f12af96` = 332, #1702 `6609eab` = 345, #1701 `daeec24` = 437. This build is two helpers, five rows and one map entry — the #1696/#1702 shape. Budget: header ~70, construction ~30, divergent-dir helper ~25, Rows 1–2 with their comparison helper ~90, Row 3 ~40, Row 4 ~35, Row 5 ~30, ban entry ~35. **≈ 355.** If the file passes ~400, the overrun is prose and the answer is to cut prose, not to keep going.

**File-overlap check** (§ 1.5, run 2026-08-24 against all 78 remote `origin/feature/<N>` branches, control-verified that the loop sees real branches and real file lists): **no overlap** on `internal/e2e/realclaude/offline_exec_ban_test.go`, and the new file exists on no branch. `origin/feature/1673` exists but its diff against `origin/main` is empty. No `blockedBy` needed.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. This slice's boundary is `newInitControlRedactor`'s parameter list, and making it the *only* boundary is the ticket's whole point: the construction reads nothing ambient, so a rule exists if and only if a caller handed a value for it. The prior boundary was scattered — `newDropcapRedactor` crossed from ambient to table in two places its signature did not mention (`realHome`, `os.TempDir()`), which is exactly how a table described as "constructed from synthetic values" carried the operator's machine. Note that this slice moves the boundary rather than closing the leak: the committed artifact stays unsafe until #1733 applies the table at the fill site, and until #1729 lands there is no fail-closed net behind it. Both are named, sequenced and out of this slice's scope.

- **[Tokens, secrets, credentials]** No findings — no token is generated, stored, compared or logged here. The credential exposure this file *could* have is indirect and is closed twice over: the process environment carries `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY`, so `os.Getenv`, `os.Environ` and `os.LookupEnv` are banned for this file, and every value the rows use is a literal the test controls. The credential-shaped deny class `sk-ant-` is #1729's subject, not this slice's.

- **[File operations]** SHOULD FIX, and the spec addresses it. `initControlDivergentDir` deliberately **creates and follows a symlink**, which is the one category-3 hazard this design takes on knowingly: the symlink is created by the test, inside `t.TempDir()`, pointing at a directory the same test created, and is never derived from an argument or from the environment — so there is no attacker-controlled leg and no check-then-use gap. It is also why `filepath.EvalSymlinks` is not banned for this file. Two adjacent hazards are closed structurally rather than by rule: the file has **no** writer and **no** reader, so the relative-path route to the committed `testdata/` that the sibling entries fence off (`go test` runs in the package source directory) does not exist here, and Row 4's directory is created under `t.TempDir()` so nothing outside the test's own tree is touched. No file modes are set because no file is created — only directories under `t.TempDir()`, at Go's default.

- **[Subprocess / external command execution]** No findings — this file execs nothing, and the ban entry makes that structural rather than conventional for the five package-local helpers that could reach an exec. Noted as a real limit, not a reassurance: the check is per-file AST identifier matching, so it catches a direct reference and **not** a reference through a helper. The complementary guard is AC1's armed-values assertion, which reddens on a rule armed from `realHome` or `os.TempDir()` no matter how many hops away the read happened. Neither substitutes for the other, and the spec says so at the entry.

- **[Cryptographic primitives]** Not applicable — no randomness, no hashing, no key material, no comparison against a secret. `strings.ReplaceAll` over declared literals is the whole mechanism. Recorded rather than skipped because "redaction" invites a constant-time-comparison reflex that does not apply: nothing here compares an attacker-controlled value to a secret, and the substitution is a rewrite, not a decision.

- **[Network & I/O]** Not applicable — no socket, no reader, no size cap to set. The one input-size question that *does* exist is downstream and named: #1733's record pass feeds this table arbitrary captured bytes, and the cap on those is `dropcapMaxCaptureBytes` in the existing recorder, which this slice neither changes nor relies on.

- **[Error messages, logs, telemetry]** No MUST FIX, with one instruction the developer must not soften. The failure messages this file emits are the one place it can leak: Rows 1–2 report **extra** rule values, and on the mutant this row targets — a construction that falls back to `realHome` — the extra value *is* the operator's home path, printed into the test log. That is correct and must stay: the row exists to name the value that should not be there, and a message that redacted its own finding would report a set-size mismatch and nothing actionable. It is safe because a `go test` log is not a committed artifact; the committed artifact is #1733's, and this file writes none. The developer must not "fix" this by masking the reported values.

- **[Concurrency]** No findings — no locks, no shared state, no goroutines. The one real hazard is named in § Concurrency model: `dropcapRedactor`'s counters are unsynchronised by design, so hoisting one redactor to package scope across `t.Parallel()` rows is a data race. Each row constructs its own, and `-race` in the run command is the deterministic backstop.

- **[Threat model alignment]** The threat here is a repository-disclosure one rather than a protocol one, so `docs/protocol-mobile.md` § Security model does not apply. The relevant model is this package's own, recorded in `docs/knowledge/features/e2e-realclaude.md`: a live capture committed to a public repository must carry no operator-identifying path or credential, defended by declared redaction plus a fail-closed deny-scan of different fabric. This slice ships one half of the redaction leg. The scan leg (#1729) and the application of the table to the record (#1733) are explicitly out of scope for this ticket and both are already filed.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-24
