# #1941 — the AskUserQuestion capture's directory-injectable writer that refuses before it writes

**Ticket:** [#1941](https://github.com/pyrycode/pyrycode/issues/1941) · `size:s` · `security-sensitive`
**Blocked by:** #1943 (record, merged) and #1944 (namer, merged) — both on `main`, nothing to rebase.
**Deliverables:** two files, both `*_test.go`, zero production source files.

---

## Files to read first

This is the turn-1 data load. Read these before writing anything; the design below assumes all of them.

| Path | Symbols | What to extract |
|---|---|---|
| `internal/e2e/realclaude/initialize_control_writer_test.go` | `scanInitControlFixture`, `writeInitControlFixture` | **The shape to copy.** Scan-step returns the exact bytes, makes no filesystem call, fatals on a hit naming count + class names only; writer calls it FIRST, then `MkdirAll` → `.tmp` → `Rename`. Copy the ordering and the comment that states it. |
| `internal/e2e/realclaude/initialize_control_writer_test.go` | `TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry` | The exactly-one-entry assertion and the four hazards it covers in one check. |
| `internal/e2e/realclaude/initialize_control_writer_test.go` | `TestInitControlFixture_ScanRefusesAPlantedCredential`, `initControlPlantedPath` | Why the refusal is asserted over the **marshalled record**, never over the writer's fatal; the two `t.Fatalf` vacuity controls; the class-names-only failure messages. |
| `internal/e2e/realclaude/ask_user_question_record_test.go` | `askQuestionFixtureRecord`, `askQuestionFullRecord`, `askQuestionFixtureFields`, `askQuestionFixtureInput` | The four fields, the fresh-pointer fixture, the single hand-written listing this spec's round trip applies to both sides, and the input literal's two constraints (unsorted keys; no `<`, `>`, `&`). |
| `internal/e2e/realclaude/ask_user_question_record_test.go` | `TestAskQuestionRecord_RoundTripsEveryFieldAndPreservesTheInputBytes` | The **single-site** `compactRawMessages` idiom for the one raw-JSON field — reuse it verbatim; do not clone `compactInitControlRawRows`. |
| `internal/e2e/realclaude/ask_user_question_names_test.go` | `askQuestionFixtureName`, `askQuestionNameRow` | The namer's one-parameter contract (`versionToken` → `ask_user_question_v<slug>.json`), and the package-scope identifier already taken (see § Package-scope surface). |
| `internal/e2e/realclaude/dropped_line_capture_test.go` | `dropcapScanner`, `dropcapFixedNeedles`, `dropcapScanner.scan`, `dropcapScanner.applied`, `dropcapMinNeedle`, `newDropcapScanner`, `dropcapContains`, `dropcapDenySkAnt`, `dropcapDenyUsers` | The offline scanner idiom `dropcapScanner{needles: dropcapFixedNeedles()}`; the five fixed classes; the `(hits, notApplied []string)` contract returning **class names, not values**; and exactly what `newDropcapScanner` reads that makes it banned here. |
| `internal/e2e/realclaude/inband_bypass_revoke_fixture_test.go` | `compactRawMessages`, `fixtureFieldNonZero` | The two pure helpers this file may call; `compactRawMessages` takes `[]json.RawMessage` and is the reason the single field is wrapped and unwrapped at the call site. |
| `internal/e2e/realclaude/offline_exec_ban_test.go` | `finOfflineExecBans` — the `"initialize_control_writer_test.go"` entry — and `TestFinOfflineFilesReachNoExecHelper` | The entry to copy **whole**, and the AST matcher: bare `*ast.Ident` and dotted `*ast.SelectorExpr`, parsed **without** `parser.ParseComments`, so no ban can be satisfied out of a comment. |
| `docs/knowledge/features/e2e-realclaude-ask-user-question-record-test-go.md` | — | Three lessons this ticket inherits: a `-overlay` mutant **cannot** exercise `TestFinOfflineFilesReachNoExecHelper` (it `parser.ParseFile`s a relative name off disk); the family's comment floor does not scale with field count; and #1943 shipped a false "the mutant compiles" doc claim. |
| `docs/knowledge/features/e2e-realclaude-ask-user-question-names-test-go.md` | — | `askQuestionNameRow` is declared at **package scope** (its own doc comment says otherwise); two doc-comment claims in that file overclaim. Distrust that file's self-description; measure before repeating any of its claims here. |
| `CODING-STYLE.md` § "Comments — Citing Other Code" | — | Symbol cites only. `make cite-guard` is diff-scoped, has no depth exemption and no range exemption; a bare `:NNN` is the worst form. |

---

## Context

The next slices of this family (#1942, #1938) drive a real claude to call `AskUserQuestion` and commit the call's input under `internal/e2e/realclaude/testdata/`. This slice builds the writer those runs will use and settles it **offline** — no claude, no credentials, no subprocess, no read of any committed artifact.

The refusal is the load-bearing half, and it is evidence-based rather than hypothetical. Re-measured against `main` on 2026-09-01, **twelve of the seventeen** committed captures under `testdata/` carry at least one deny-class occurrence: `/Users/` in `initialize_control_v2.1.239.json` and in each of the four `set_permission_mode_v2.1.220_*` captures; `/var/folders/` in seven of the eight `permission_protocol_*` captures and in all four `set_permission_mode_*` ones. Once committed, those bytes are permanent. #1688's whole-stream capture needed three follow-up tickets (#1729, #1732, #1733) to redact what it had already swallowed.

**The refusal is the writer's precondition, not an add-on.** The sibling family shipped its writer in #1702 and retrofitted the scan in #1748, which cost that ticket a restructure of shipped code and left one merge window with an unguarded writer. Both land together here.

**No ADR is warranted.** This slice introduces no decision the family has not already recorded: the fail-closed-scan-before-write discipline is `scanInitControlFixture`'s, and the directory-injectable-writer discipline is `writeInitControlFixture`'s. The documentation phase should fold this file's lessons into `docs/knowledge/features/` as a sibling of the two AskUserQuestion child docs.

### One stale forward reference, deliberately not fixed here

`askQuestionFullRecord`'s doc comment says *"#1941's fill site is where the two fields are minted from one call and where that coupling becomes checkable."* This slice has **no fill site** — it is offline and mints nothing from a live call. The same sentence appears in `docs/knowledge/features/e2e-realclaude-ask-user-question-record-test-go.md`'s closing paragraph, which additionally describes #1941 as "the writer that fills `ToolInput` from a live child".

Do not build a fill site to satisfy either. Do not edit either file: the code comment is #1943's and the knowledge doc is the documentation phase's, the ACs ask for neither, and correcting them belongs with #1942/#1938, which is where the fill site actually lands. Flagged here so the documentation phase can correct the overview when it folds this ticket in.

---

## Size check

**Ships as one `size:s` ticket.** Five of the six boundaries hold with margin; one is exceeded and is reported openly rather than argued away.

| Boundary | Limit | This ticket |
|---|---|---|
| Production source files created or modified | ≤ 3 | **0** (both deliverables are `*_test.go`) |
| Total written work | ≤ 400 | **~460** — exceeded |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** (nothing on `main` calls either new symbol) |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches in a state machine | ≤ 10 | **0** — no state machine; five `t.Fatalf` sites, four of them ordinary I/O errors |

**The overage, measured in two directions.**

*Shape-matched analogue floor.* #1702 (`6609eabd`) shipped this exact scope minus the scan — directory-injectable writer, offline round trip, one `finOfflineExecBans` entry, two files — at **345 insertions**. This record is strictly smaller on every axis that drove that number: four fields against roughly ten, one raw-JSON field against three (so a single-site `compactRawMessages` call rather than `compactInitControlRawRows`' type switch and touched-name canary), no capture cap, no redaction census, no arming census. The scan half adds a step, two planted rows and two vacuity controls; #1748 shipped that plus a per-class table plus a restructure of shipped code at 344/-31. The floor lands at ~450–500.

*Family measurement.* The knowledge doc records three data points for this file family's comment density: #1696 at 332, #1943 at 567, #1944 at 445 — and states that the density is a floor independent of field count. Two directions agree at ~460.

**Why it still ships whole: the seam check, run against the source rather than against the ticket's prose.** Two cuts exist and neither survives.

- *Writer first, scan retrofitted second* is #1702 → #1748 replayed. It ships the exact unguarded artifact this ticket exists to prevent, for one merge window, and pays the retrofit cost the sibling already measured. A construction-time security bound does not split from the payload it bounds.
- *Scan step first, writer second* inverts that ordering and does dodge the retrofit — but the step is `json.MarshalIndent` + `scanner.scan` + a fatal, roughly twelve lines of code whose only production consumer is the writer. Shipped alone it is a helper with no caller for a merge window, and its planted rows prove nothing a row calling `scanner.scan` over `askQuestionFullRecord()` could not already prove with #1943 and the dropcap scanner both on `main`. Child A would ship no new capability.

**The binding constraint the boundary protects is developer wall clock, and it is measured for this family, this week.** #1943 (567 insertions, 2 files) ran architect-complete 04:31 → developer-complete 04:37. #1944 (445 insertions, 2 files) ran 05:17 → 05:22. Six and five minutes against a 25-minute cap. The 400-line boundary is calibrated against cascade-shaped failures — #432 at 14 files, #445 at 596 production lines across many, #446 at 6 files — and this ticket is one new self-contained file plus one map entry, with zero call sites to cascade through.

**The tension is real and stated rather than suppressed:** this ticket is the third consecutive `size:s` in this package to exceed the total-line boundary while every other line holds comfortably. If the boundary is meant to bite on single-file, comment-dense test slices, it is biting here and the operator should recalibrate it deliberately rather than through a fourth architect judgment call.

---

## Design

Two files. Nothing else is touched.

### 1. `internal/e2e/realclaude/ask_user_question_writer_test.go` (new)

`//go:build e2e_realclaude`, package `realclaude`, matching every file in the package.

#### `scanAskQuestionFixture(t *testing.T, scanner dropcapScanner, rec *askQuestionFixtureRecord) []byte`

Returns the exact bytes `writeAskQuestionFixture` will put on disk, refusing the record outright when the deny-scan hits.

Behaviour: `t.Helper()`; `json.MarshalIndent(rec, "", "  ")`; on a marshal error `t.Fatalf`; `hits, _ := scanner.scan(data)`; on `len(hits) > 0` `t.Fatalf` naming the count and the class names and nothing else; return `data`.

Four properties are load-bearing and each needs a paragraph in the doc comment:

- **It makes no filesystem call of any kind, and its signature carries no directory.** That absence is the mechanism behind AC 2, not a coincidence of layout: a function with no path parameter cannot create an entry under one even in principle. It is also the sole producer of the write's bytes, so the writer cannot hold a blob the scan has not passed, and the first filesystem call anywhere on the path is the writer's `os.MkdirAll`, strictly after this returns.
- **It does not copy the record.** Unlike `scanInitControlFixture`, there is no cap to apply — this record carries no capture field — so `out := *rec` would be a defensive-looking shallow copy that mutates nothing and shares `ToolInput`'s backing array. Marshal the caller's pointer directly, and say in the comment that a future field-cap must add the copy **and** reckon with the slice field rather than inheriting a copy that already looks safe.
- **The second return is discarded, and the reason is measured rather than stylistic.** Do **not** extend the refusal to `notApplied`. `newDropcapScanner` arms `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` through `os.Getenv`; on a subscription-login machine both are unset, `scan` classes an empty dynamic needle as not-applied, and a writer refusing on `notApplied` would refuse **every live capture** #1942 and #1938 attempt. The vacuity control in the refusal test is where `notApplied` is consumed, and that is the right place: offline, over a fixed-only scanner, where a non-empty list means a fixed needle was wrongly marked dynamic.
- **Never format the scanner.** No `%v`, `%+v`, `%#v` or `%q` on a `dropcapScanner`, on a needle or on the needle slice, here or in the writer or in any row added later. A scanner built by `newDropcapScanner` holds two live credentials as needle values, and this pipeline salvages run logs.

#### `writeAskQuestionFixture(t *testing.T, dir string, scanner dropcapScanner, rec *askQuestionFixtureRecord) string`

Writes `rec` into `dir` and returns the written path. Mirrors `writeInitControlFixture` step for step:

1. `t.Helper()`.
2. `data := scanAskQuestionFixture(t, scanner, rec)` — **first, and before any filesystem call**, under that exact comment. A refused record must strand nothing under `dir`, not even a `.tmp`.
3. `os.MkdirAll(dir, 0o755)` — kept for the sibling's reason: #1942/#1938 may pass a directory that does not exist; it is a no-op against a `t.TempDir()`.
4. `path := filepath.Join(dir, askQuestionFixtureName(rec.ClaudeVersionSlug))`.
5. `os.WriteFile(path+".tmp", data, 0o644)` then `os.Rename(tmp, path)` — the tmp-and-rename discipline every writer in this package uses, so an interrupted run cannot strand a half-written fixture under the target name for a later commit.
6. Return `path`.

Rules for the doc comment:

- **`dir` is a parameter, deliberately** — unlike `writeSetModeFixture` and `writeFixture`, which resolve the real `testdata/` through `packageDir`. That is what lets this writer settle against a `t.TempDir()` here, with #1942/#1938 passing the committed directory later.
- **The name is minted, never formatted.** `askQuestionFixtureName` owns both the `ask_user_question_v` prefix and the slug column. A writer interpolating its own `"ask_user_question_v%s.json"` reopens the overwrite and containment hazards #1944's lock exists to close, with #1944's own tests still green.
- **Feed it `ClaudeVersionSlug`, never `ClaudeVersionRaw`.** The record carries no bare version token: the raw field is a whole `claude --version` line, which slugs to something else entirely. #1944's lock already pins the row this satisfies — token `2.1.239-fixture` mints `ask_user_question_v2.1.239-fixture.json`, slug-clean by construction so re-slugging is a fixed point.
- **The scanner is a parameter, never a `newDropcapScanner` call in this file.** That constructor reads `os.Getenv` twice and `realHome`; a table built through it is green or red depending on whose machine ran it. The ban entry lists both names for exactly that reason, and because the check is an AST identifier match, calling the constructor would satisfy the `os.Getenv` ban to the letter while destroying the property it protects. Both offline callers pass `dropcapScanner{needles: dropcapFixedNeedles()}`, written inline at the call site rather than behind a local helper — the sibling's spelling, and it keeps "this reads no environment" visible where the ban protects it.
- **The bytes are written byte for byte**, with nothing appended — not even a trailing newline, which would break AC 4 by one byte.
- **This writer is the only sanctioned route to a committed AskUserQuestion artifact.** The scan is bypassable by construction: a caller that marshals the record and calls `os.WriteFile` itself never reaches the step, and nothing in this slice can detect that — `finOfflineExecBans` is per-file, and the live capture file execs, so it can never carry an entry. Say so in the doc comment, addressed to #1942 and #1938: a direct marshal-and-write of this record is the defect to look for at review, and it is the one failure mode the net cannot see.
- **Name containment is inherited from the namer, and that is the security-relevant reason not to format the name here.** `versionSlug` maps every byte outside `[a-z0-9._-]` to `_`, so no path separator survives its input, and the literal `ask_user_question_v` prefix — which no input can reach — means the minted name can never be `.` or `..`. `filepath.Join` therefore cannot escape `dir` however odd the version token is. A writer interpolating its own format string discards that guarantee while #1944's own tests stay green.
- **The `.tmp` name is derived from the target name**, so two writers minting the same name into one directory race on it. The offline callers here cannot collide — one writes once, the other writes nothing — but #1942/#1938 must give each write its own directory if it ever writes twice, which is the discipline the sibling family's census rows already follow.

#### The two tests

- `TestAskQuestionFixture_RoundTripsEveryFieldIntoOneNamedEntry` — AC 1, AC 4, and AC 2's success-path half.
- `TestAskQuestionFixture_ScanRefusesAPlantedValue` — AC 2's refusal half and AC 3.

Scenarios are enumerated under § Testing strategy.

#### Planted values

Two package-level string constants and one small helper, all synthetic:

- `askQuestionPlantedPath` — an operator path of the `/Users/` class, e.g. a synthetic-operator log path under `Library/Logs`. It must contain **no other armed class**: not `/home/`, not `/var/folders/`, not `/private/var/folders/`, not `sk-ant-`.
- `askQuestionPlantedKeyPrefix` — a value starting with the literal `sk-ant-` and continuing with obviously synthetic text. It must contain no path of an armed class, and its tail must read as unmistakably not-a-key to a human and to a secret scanner; the sibling's `sk-ant-synthetic-not-a-real-key` is the spelling to follow, and its presence on `main` is what establishes that this repo's push protection does not block that shape.
- `askQuestionPlantedInput(plant string) json.RawMessage` — returns a valid AskUserQuestion-shaped payload carrying `plant` inside one JSON string value.

Constraints on both constants, each of which changes what the rows prove:

- **One class each.** A plant carrying two armed classes makes its row pass for the wrong reason and stops being the sole red for its own class.
- **JSON-string-safe**: no `"` and no `\`, because the helper splices `plant` into a string literal rather than marshalling it. A quote there produces invalid JSON, `json.MarshalIndent` fails, and the row reports a marshal error instead of a scan verdict.
- **No `<`, `>` or `&`**, matching `askQuestionFixtureInput`'s own constraint. `scan` checks both the raw and the JSON-escaped spelling of every needle, so such a character would not defeat the row — but it would introduce a second, unrelated difference between the two spellings and muddy what the row is about.
- **The fixed five needles are exempt from `dropcapMinNeedle` by construction**, so unlike the sibling's dynamic-class sweep neither plant has a minimum length to satisfy. Say so, so nobody imports that constraint from `newInitControlOfflineScanner`'s constant block, which does have it.

`ToolInput` is the carrier for both plants, and the reason is structural: it is the **only** field whose bytes come from the child. `ClaudeVersionRaw` and `ClaudeVersionSlug` are minted from `claude --version` and `ToolName` is a constant, so a leak arriving through any of the three is not a scenario this net exists for. That also puts the rows in the one field the record exists to carry.

### 2. `internal/e2e/realclaude/offline_exec_ban_test.go` (one map entry)

Add `"ask_user_question_writer_test.go"` to `finOfflineExecBans`, copying the `"initialize_control_writer_test.go"` entry **whole** — the table's only other writer entry:

```
"resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
"probeClaudeVersion", "captureClaudeVersion",
"os.Getenv", "os.Environ", "os.LookupEnv",
"packageDir", "setModeFixturePath", "writeSetModeFixture", "writeFixture",
"newDropcapScanner", "realHome",
```

What the entry's doc comment must say, and two traps in it:

- **Fourteen names, not seventeen.** The `os` read/write group (`os.ReadFile`, `os.WriteFile`, `os.Create`, `os.ReadDir`), `filepath.Glob` and `t.TempDir` are deliberately absent, because unlike #1943's and #1944's entries this file legitimately writes, reads back and lists a directory. Banning them would be red against shipped code.
- **Do not copy the source entry's count.** `initialize_control_writer_test.go`'s comment says "twelve names rather than seventeen" and then lists fourteen — #1748 added `newDropcapScanner` and `realHome` without updating the prose. Write **fourteen** in the new comment. Do not fix the old one: it is outside this ticket's ACs.
- **Why `packageDir` and its three wrappers all appear** even though a `packageDir`-only ban reads sufficient: the check is a per-file AST identifier match, not a call graph, so a file calling a wrapper reaches `packageDir` transitively while never naming it.
- **The relative-path hazard the `os` group closes for the two I/O-free entries is closed here differently**, and the comment should say by what: `go test` runs in the package source directory, so a relative `os.WriteFile("testdata/…")` reaches the committed captures without naming `packageDir` at all — and a writer that did that leaves the `t.TempDir()` holding **zero** entries, which the exactly-one-entry assertion reddens.
- **`newDropcapScanner` and `realHome` are the deterministic fabric behind the scanner parameter**, and the ban is what makes the parameter mean something.
- **`os.TempDir` is deliberately not added**, matching the sibling writer's entry: `newDropcapScanner` takes its three path arguments from callers and reads only `realHome` and the environment on its own, so `os.TempDir` would be a ban name with no hazard behind it.

### Package-scope surface

`askQuestionNameRow` is already declared at package scope by #1944 (its own doc comment claims otherwise — the knowledge doc records the discrepancy). Do not collide with it, and do not add a competing row type: **declare the planted-value table as an anonymous struct slice inside the test function**, which is what the sibling's sweep does. This file's package-scope additions are then two functions, two tests, two constants and one helper — no new type.

---

## Concurrency model

No goroutines, no channels, no shared mutable state. Three rules the design depends on:

- **`t.Fatalf` requires the test goroutine.** Both `scanAskQuestionFixture` and `writeAskQuestionFixture` fatal, and both are called directly from a test goroutine. `t.Helper()` plus a direct call is the whole discipline. Never call either from a goroutine, here or from #1942/#1938.
- **One `dropcapScanner` value is safe to share across parallel subtests.** It is append-only during construction and read-only afterwards; `scan` is a value receiver that allocates its own result slices.
- **`askQuestionFullRecord` returns a fresh pointer per call**, which is why planted rows may mutate their own record and run in parallel. Do not hoist it into a package-level `var` — that turns a parallel row's mutation into a `-race` data race discovered two tickets away.

The round-trip test's subtests share one written artifact and one `t.TempDir()`; keep them non-parallel with the parent owning the directory, as the sibling does. The refusal test's rows are independent and may be parallel — each mints its own record.

---

## Error handling

Five failure sites, each a `t.Fatalf`, each naming only values that are safe to print.

| Site | Message names | Never names |
|---|---|---|
| `json.MarshalIndent` fails | `claude_version_slug`, `tool_name`, the error | the record, `tool_input` |
| deny-scan hits | the **count** and the **class names**, plus the "nothing was written" statement | the matched value, its offset, the blob, the record |
| `os.MkdirAll` fails | `claude_version_slug`, the error | as above |
| `os.WriteFile` fails | `claude_version_slug`, the error | as above |
| `os.Rename` fails | `claude_version_slug`, the error | as above |

**Why those two fields are the safe set.** `ClaudeVersionSlug` is already half the filename and is minted from `claude --version`; `ToolName` is a package constant. `ToolInput` is claude-supplied, is the leak surface the whole scan exists for, and must never reach a message — a `%+v` of the record puts it there in one character.

The marshal error is the one message carrying text this file did not author. `encoding/json` reports an invalid `json.RawMessage` by naming the offending character, not by echoing the payload, so `%v` on it is bounded. That is a judgment about a bounded error string, not a licence to widen it.

**The refusal message must tell the reader what to do**, as the sibling's does: name the class, state that nothing was written, and say that the offending value is deliberately not printed because putting it in a run log this pipeline salvages is exactly the exposure the scan prevents.

---

## Testing strategy

Everything runs and passes offline. Nothing skips.

```
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestAskQuestionFixture_|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/
```

Read the count of `=== RUN` lines, never the exit code: `make check` never compiles this package, and the suite exits 0 both on a build failure and on a full credentials skip. `make preship` is the gate that proves the package builds.

### `TestAskQuestionFixture_RoundTripsEveryFieldIntoOneNamedEntry`

Writes `askQuestionFullRecord()` into a `t.TempDir()` through the writer with `dropcapScanner{needles: dropcapFixedNeedles()}`, reads the file back off disk, decodes it into a fresh `askQuestionFixtureRecord`.

- **Every field survives.** Compact both sides' `ToolInput` through `compactRawMessages(t, []json.RawMessage{…})[0]` at a single site — `json.MarshalIndent` indents **inside** an embedded raw message, so an uncompacted comparison reddens against a perfectly correct writer. Then apply `askQuestionFixtureFields` to both records and compare row by row with `reflect.DeepEqual`. No length guard before the zip: both slices come from one function over one struct type, so a divergence is impossible and the guard could never redden.

  **The failure message prints both values, and that is safe here and only here.** Every value in `askQuestionFullRecord` is a synthetic literal, and the diagnostic is worth more than protecting a fixture string. The comment must say so in those terms and name the reason: #1942 and #1938 fill this same record's `ToolInput` from a live child, so a row that printed it there would move claude-supplied bytes out of the scanned, committed artifact and into an unbounded run log this pipeline salvages — the exact exposure the scan exists to prevent. An unqualified print pattern here is what they would inherit.
- **The target directory holds exactly one entry, named exactly what `askQuestionFixtureName` mints from `rec.ClaudeVersionSlug`.** One assertion, four hazards: a `.tmp` stranded beside the target by a copy instead of a rename; any stray file; a write that escaped to the package's real `testdata/`, which leaves this tempdir at **zero** entries and is the relative-path hazard no AST ban can close; and a writer that minted the name some other way.

**Two limits to state in the doc comment rather than claim away.**

*The round trip cannot catch a misspelled JSON tag.* The read-back decodes through the struct that wrote the file, so it is symmetric, and #1662's review established that even a **unique** wrong tag round-trips green — only a colliding pair makes `encoding/json` drop both. Applying `askQuestionFixtureFields` to both sides does not change that; the listing labels failure messages here rather than checking anything. The instrument is a reviewer diffing #1943's hand-written listing against the struct's tags.

*The name assertion is 0-red on the slug column.* `ClaudeVersionSlug` is slug-clean by #1943's construction, so a writer interpolating `"ask_user_question_v" + rec.ClaudeVersionSlug + ".json"` — dropping the `versionSlug` call entirely — mints the identical name and this row stays green. #1944's golden table is where that mutant reddens, and this file must not claim otherwise. What the row **is** the sole red for: feeding `ClaudeVersionRaw` instead of the slug, a different prefix or extension, a writer that ignores `dir`, a copy instead of a rename, and a write that escaped to a relative `testdata/`.

### `TestAskQuestionFixture_ScanRefusesAPlantedValue`

Builds one `dropcapScanner{needles: dropcapFixedNeedles()}` and marshals `askQuestionFullRecord()` unplanted.

- **Vacuity control 1 — the unplanted record hits no class.** `t.Fatalf` (not skip, not `Errorf`) when `len(hits) != 0`: without it the planted rows cannot tell "the plant was seen" from "this record always hits", and the round-trip test's writer call would be fataling too.
- **Vacuity control 2 — the unplanted record leaves no class unapplied.** `t.Fatalf` when `len(notApplied) != 0`. Every fixed needle is exempt from `dropcapMinNeedle` by construction, so a non-empty list means a fixed needle was marked dynamic, its class is silently skipped, and the planted rows go green-and-vacuous.
- **Row: an `sk-ant-` credential prefix planted in `tool_input`** → `dropcapContains(hits, dropcapDenySkAnt)`.
- **Row: an operator path planted in `tool_input`** → `dropcapContains(hits, dropcapDenyUsers)`.

Each row builds a fresh `askQuestionFullRecord()`, replaces `ToolInput` with `askQuestionPlantedInput(row.plant)`, marshals with `json.MarshalIndent`, and scans. Failure messages name class constants and the `hits` slice — never the plant, the blob or the record. Every value here is synthetic, so printing one would leak nothing; the point is that #1942 and #1938 inherit no pattern worth copying.

### What is deliberately not tested, and why

- **That the writer's fatal fires.** A `t.Fatalf` from inside the writer takes the calling subtest down with it, `testing.TB` cannot be implemented outside `testing`, and `t.Run`'s bool return cannot un-fail a subtest whose failure has already propagated to the parent — so no row can observe a refusal raised from inside one. Do not spend turns building a harness for it. The assertion is on the **deciding value**: `scanAskQuestionFixture` refuses iff `len(hits) > 0`.
- **That `scanAskQuestionFixture` calls `scan`.** That is construction — the step is the sole producer of the write's bytes and the scan is inside it — and it is the one untestable link in the chain.
- **AC 3's "never prints the value" half.** Discharged by construction at the scan step, for the reason above. State the limit; do not claim a row that pins it.
- **AC 2's "before creating anything on disk" half.** Discharged by construction, with a mechanism a reviewer can check rather than a claim to trust: the step's signature carries no directory, so it cannot create an entry under one; and the writer's first filesystem call is strictly after the step returns. The **success**-path half of "not even a partial or temporary one" is executable and is the exactly-one-entry assertion above.
- **The per-class sweep over all five armed classes.** That was its own ticket for the sibling family (#1749) and is out of scope here; AC 2 asks for two rows.
- **Anything reading a committed capture.** Do not add a row that re-scans `testdata/`. Twelve of the seventeen committed captures still carry a deny class, so such a row is red on arrival, and the ban entry fences this file off from that directory anyway.

### Proving the `finOfflineExecBans` entry is non-vacuous

`TestFinOfflineFilesReachNoExecHelper` calls `parser.ParseFile` on a **relative filename**, which a build overlay does not intercept. A `go test -overlay` mutant therefore proves nothing about this entry. The only way to demonstrate it: write one banned call into the real worktree file, run that one subtest, confirm it reddens, revert. Recorded in the knowledge doc as #1943's lesson; it applies unchanged here.

### Doc-comment discipline

This family has now shipped three doc-comment claims that a measurement contradicts — #1943's false "the mutant compiles", and two in #1944. Every claim of the form "this row is the sole red for X" or "that mutant compiles" in the new file must be one the developer actually measured. Where a claim cannot be measured cheaply, write the weaker true sentence instead of the stronger unmeasured one.

---

## Open questions

1. **`os.MkdirAll` on the live path.** #1942/#1938 will pass the package's real `testdata/`, which exists, so the call is a no-op there as well as against a `t.TempDir()`. Kept for the sibling's stated reason and because removing it would make the writer's contract narrower than `writeInitControlFixture`'s for no gain. If the live slice ends up wanting a subdirectory, this is already the right shape.
2. **Whether #1942/#1938 pass `newDropcapScanner` or a fixed-only scanner.** Out of scope here — the parameter is what defers the choice. This spec only pins that the writer must not refuse on `notApplied`, because the dynamic credential classes are legitimately unapplied on a subscription-login machine and a stricter writer would refuse every live capture.
3. **Correcting the stale forward reference** in `askQuestionFullRecord`'s doc comment and in the record's knowledge doc. Belongs with the slice that actually builds the fill site (#1942/#1938) and with the documentation phase respectively; named here so neither loses it.
4. **The artifact is unbounded in size, and this slice deliberately does not bound it.** <!-- see § Security review, [Network & I/O] --> #1688's three properties for a committed capture were COMPLETE, BOUNDED and ATOMIC; this design settles complete (the round trip), atomic (tmp-and-rename) and adds scan-clean, but `askQuestionFixtureRecord` carries no cap field and no `capFixtureCapture` analogue exists here — where `initControlFixtureRecord` bounds its capture at `stderrFixtureCap`. An `AskUserQuestion` input is small by nature, but nothing enforces it. Adding a cap means a cap constant, a copy-and-cap in the scan step with its shallow-copy caveat, a cap test and a no-mutation contract — the sibling's #1748 shape, roughly a third of this ticket again, over a record that is already merged. **It is out of scope here, and it must land with the live fill site rather than after it**: a bound that arrives a ticket late ships one merge window of unbounded claude text, which is the failure the whole family is organised against. #1942/#1938 decides it before the first live capture.

---

## Security review

**Verdict:** PASS (first pass returned FAIL on one MUST FIX; the spec was revised inline and the checklist re-walked from the top)

**Findings:**

- **[Trust boundaries]** SHOULD FIX — addressed. The one boundary is `scanAskQuestionFixture`: untrusted bytes are claude's, arriving in `ToolInput`, and the step is the sole producer of the bytes `writeAskQuestionFixture` puts on disk, so the writer cannot hold a blob the scan has not passed. The hole is that the boundary is bypassable rather than mandatory — a caller that marshals the record and calls `os.WriteFile` itself never reaches the step, and nothing in this slice can detect it: `finOfflineExecBans` is per-file, and the live capture file execs so it can never carry an entry. Not closable at this slice's cost; the spec now names it in the writer's doc-comment rules as the defect #1942/#1938's review must look for. Note also that `askQuestionFixtureRecord` carries no type-level signal that `ToolInput` is untrusted while its three sibling fields are harness-minted — that is #1943's shipped type, out of scope to change here.
- **[Tokens, secrets, credentials]** MUST FIX — fixed. Nothing here generates, stores or rotates a credential; the exposure is entirely one of leakage through diagnostics, and the first draft closed four channels and left a fifth open. Closed already: the refusal reports the count and the class names only, never the matched value or its offset; no `%v`/`%+v`/`%#v`/`%q` on a `dropcapScanner`, a needle or the needle slice, since a scanner built by `newDropcapScanner` holds two live credentials as needle values; the planted rows print class constants and the `hits` slice only; `os.Getenv`, `os.Environ` and `os.LookupEnv` are banned over this file's AST. The gap was the round trip's `t.Errorf`, which prints both sides of a mismatched row — safe over `askQuestionFullRecord`'s synthetic literals, but an unqualified pattern that #1942 and #1938 would inherit over a `ToolInput` filled from a live child, moving claude-supplied bytes out of the scanned artifact and into a salvaged run log. The spec now requires the "safe here and only here" qualification naming those two tickets. Separately, `askQuestionPlantedKeyPrefix` is constrained to the sibling's unmistakably-synthetic spelling, whose presence on `main` establishes that push protection does not block the shape.
- **[File operations]** No findings. Path traversal is structurally impossible and the spec now says why rather than asserting it: `askQuestionFixtureName` slugs its input through `versionSlug`, which maps every byte outside `[a-z0-9._-]` to `_` so no separator survives, and the literal `ask_user_question_v` prefix that no input can reach means the name can never be `.` or `..` — so `filepath.Join` cannot escape `dir` for any version token. That is the security-relevant reason the writer must not format its own name, now stated as such. No check-then-use: there is no `os.Stat` anywhere on the path. Modes are explicit — `0o755` for the directory, `0o644` for the file, correct because the artifact's destination is a public repo and the scan, not the mode, is the control that keeps secrets out of it; `0o600` here would be theatre. Symlinks are followed by `os.MkdirAll` and `os.WriteFile`, and `dir` is never attacker-controlled — a `t.TempDir()` offline, `packageDir()/testdata` live. Writes are atomic via tmp-and-rename. `f.Sync()` before the rename, which `PROJECT-MEMORY.md`'s registry recipe requires, is deliberately declined: the family's writers omit it, the file is read back in-process and then committed, and crash durability is not a property this artifact needs. The one residual hazard, now named in the spec, is that the `.tmp` name is derived from the target name, so two writes of one name into one directory race — the offline callers cannot, and #1942/#1938 must give each write its own directory.
- **[Subprocess / external command execution]** Not applicable, and enforced rather than asserted. This file execs nothing and spawns nothing; the `finOfflineExecBans` entry's first five names (`resolveClaudeBin`, `WithWorktreeAuthenticated`, `WithWorktree`, `probeClaudeVersion`, `captureClaudeVersion`) are matched over the file's AST by `TestFinOfflineFilesReachNoExecHelper`, which parses without `parser.ParseComments` so the ban cannot be satisfied out of the prose that states it. Those bans also keep a `t.Skip` out: a skip exits 0 and reads as a pass under `make e2e-realclaude`, which is AC 5's real hazard.
- **[Cryptographic primitives]** Not applicable. No randomness, no key material, no hashing, no comparison of an attacker-supplied value against a secret. `scan` uses `bytes.Contains`, which is not constant-time — correctly, because it compares a candidate artifact against deny needles to decide a refusal, and the party supplying the blob already knows its contents, so the timing channel carries nothing they do not have.
- **[Network & I/O]** OUT OF SCOPE, with a condition. No sockets, no listeners, no timeouts to set. The one applicable item is the input-size cap, and there is none: `askQuestionFixtureRecord` carries no bound where `initControlFixtureRecord` bounds its capture at `stderrFixtureCap`, so a large `AskUserQuestion` input becomes a large committed artifact. Deferred here because the record is already merged and a cap is the sibling's `capFixtureCapture` shape — a constant, a copy-and-cap with its shallow-copy caveat, a cap test and a no-mutation contract. Open question 4 records that it must land **with** #1942/#1938's fill site rather than after it, since a bound arriving a ticket late ships one merge window of unbounded claude text.
- **[Error messages, logs, telemetry]** No findings beyond the Tokens entry's MUST FIX, now fixed. The Error handling section fixes the safe print set at `claude_version_slug` and `tool_name` — the first is already half the filename and is minted from `claude --version`, the second is a package constant — and forbids the record and `ToolInput` at every one of the five failure sites. The marshal error is the only message carrying text this file did not author; `encoding/json` names an offending character rather than echoing the payload, which is a bounded exception recorded as such. No metrics, no telemetry.
- **[Concurrency]** No findings. No goroutines and no channels, so no lifecycle or leakage question. `t.Fatalf` requires the test goroutine and both new functions fatal, so the spec forbids calling either from a goroutine — including from #1942/#1938's stdout readers. Shared state is read-only: one `dropcapScanner` is safe across parallel rows because it is append-only during construction and `scan` is a value receiver allocating its own results, and `askQuestionFullRecord` hands out a fresh pointer per call so a planted row's mutation cannot race a sibling's. The only check-then-mutate on shared state is the rename onto a name a concurrent writer could also be minting, covered under File operations.
- **[Threat model alignment]** No findings. No relay or protocol surface, so `docs/protocol-mobile.md` § Security model does not apply. The governing threat is this family's own and is stated in the ticket with a re-measurement: twelve of the seventeen committed captures under `testdata/` already carry an operator-machine value, and committed bytes are permanent. This slice ships the fail-closed net — it refuses and rewrites nothing. Two neighbouring controls are explicitly out of scope and named: the per-class sweep over every armed class (the sibling's #1749) and a redaction table (the sibling's #1732/#1733), which is deliberately different fabric from a net and does not substitute for one.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-09-01
