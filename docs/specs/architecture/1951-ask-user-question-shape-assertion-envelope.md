# #1951 — the AskUserQuestion shape assertion's envelope and its first two checks

One new test file, `internal/e2e/realclaude/ask_user_question_shape_test.go`, plus one
entry rebased into `finOfflineExecBans`. No production file is touched. Everything
settles offline: no claude binary, no credentials, no capture file, no directory.

## Files to read first

Read these before the first edit. This list is the turn-1 data load — the design
below assumes you have it.

- `internal/e2e/realclaude/ask_user_question_record_test.go` → `askQuestionFixtureRecord`,
  `askQuestionFullRecord`, `askQuestionFixtureInput` — the four-field record this
  assertion takes as a parameter, the fully-populated instance that is the positive
  control, and the synthetic input literal. **Read the doc comments, not just the
  code**: they state the two constraints on that literal (unsorted keys at two
  levels; no `<`, `>` or `&`) that later slices must not "tidy".
- `internal/e2e/realclaude/ask_user_question_record_test.go` → `askQuestionFixtureFields`
  — read it once so you can confirm you do **not** need it. This slice zips no
  marshalled record against its decode; the field listing has no use here.
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans` — the
  `"ask_user_question_record_test.go"` entry (the seventeen names to copy) and the
  `"ask_user_question_writer_test.go"` entry beside it (the fourteen-name trap this
  slice must **not** copy). Also `TestFinOfflineFilesReachNoExecHelper` — the check
  is a per-file AST identifier match with no `parser.ParseComments`, which is why
  the entry must list wrappers as well as the thing they wrap.
- `internal/e2e/realclaude/ask_user_question_writer_test.go` → `scanAskQuestionFixture`
  — the family's fatal-helper convention: `t.Helper()`, a direct call from the test
  goroutine, and a `t.Fatalf` that names counts and class names and nothing else.
  Copy the discipline; this slice's wrapper is the same shape over a different
  returned value.
- `internal/e2e/realclaude/ask_user_question_writer_test.go` → `askQuestionPlantedInput`
  — read it so you recognise it and leave it alone. It is the package's only other
  `AskUserQuestion`-shaped input helper and so the obvious thing to borrow, but every
  call site hands it a credential-shaped plant, and this file's defining property is
  carrying none. Write local literals.
- `internal/e2e/realclaude/initialize_control_writer_test.go` →
  `TestInitControlFixture_ScanRefusesAPlantedCredential` — reads for the problem
  statement in its doc comment (a helper that fatals takes the calling subtest down
  with it; `testing.TB` cannot be implemented outside `testing`), **and for how this
  slice differs**: that test sidesteps the fatal by calling the pure `scan`
  directly and says so. Here the pure function is the deliverable, not a sidestep.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapScanner.scan` — the
  in-package precedent for a check that *returns* a slice of class names instead of
  fataling. Read the return shape only; do not construct a scanner here.
- `docs/knowledge/features/e2e-realclaude-ask-user-question-record-test-go.md` — #1943's
  lessons. Two bind here: an overlay mutant cannot prove a `finOfflineExecBans` entry
  non-vacuous (the check reads the file off disk, and a build overlay does not
  intercept `parser.ParseFile`), and this family's comment density does not scale down
  with the size of the thing under test.
- `CODING-STYLE.md` — table-driven tests, stdlib `testing` only, no assertion library.

## Context

Two later slices must decide whether a captured `AskUserQuestion` payload is
well-formed: the live run (#1938), which must not report success on an empty or
malformed capture, and the offline reader (#1939), which must redden when a claude
release renames or drops a field. Two copies of a shape contract drift, so it is
built once, here, and proven offline against synthetic literals — the shape comes
from vendor documentation, which is exactly why the instrument has to exist before
the bytes arrive: it is the thing the live capture is measured *against*.

A shape assertion that cannot fail is worse than none, because both consumers will
read its silence as evidence. So the negative rows ship in the same commit as the
checks they pin, and the undecodable-input guard is specified rather than left to
construction — a helper returning "nothing missing" on bytes it could not parse is
the shortest route to exactly that silence.

**No ADR is warranted.** The one decision with reach beyond this ticket — findings
returned by value rather than fataled, compared by exact equality — is argued at the
symbol below and inherited by #1952 and #1938 through the same function. It is a
test-harness shape, not a system-design commitment.

**One forward-reference trap, stated so the developer does not walk into it.** Nine
shipped comments in this package name #1942, this ticket's parent, and eight describe
it as a live-capture slice. `grep -n '#1942' internal/e2e/realclaude/*.go` lists all
nine. One of the eight contradicts AC 3 outright: `ask_user_question_writer_test.go`
argues its deny-scan is undetectably bypassable because "`finOfflineExecBans` is
per-file, and #1942's and #1938's live capture files exec, so neither can ever carry
an entry." **That sentence is not a ruling about this file.** This slice is #1942's
successor, execs nothing, and AC 3 requires an entry. Do not build a fill site, a
stdout reader or a capture path here to satisfy those sentences — and **do not edit
them here either**. Correcting a forward reference belongs with the slice that builds
the thing it describes; #1938 corrects them, the convention #1941 applied when it
left #1943's stale sentence alone. Your header carries one paragraph saying this file
is #1942's offline successor and execs nothing, so a reviewer who greps `#1942` can
resolve the apparent contradiction without reopening it.

## Design

### The file

`internal/e2e/realclaude/ask_user_question_shape_test.go`, `//go:build e2e_realclaude`,
package `realclaude`. Imports are exactly `encoding/json`, `reflect`, `testing` — if
you find yourself adding a fourth, stop and re-read AC 3.

### The decode target — declared whole, read in part

Three unexported types. The question object carries **four** fields and all four are
declared now, even though this slice's checks read only the batch length:

- `askQuestionInput` — `Questions []askQuestionQuestion` under `json:"questions"`.
- `askQuestionQuestion` — header, question text, options, multi-select key.
- `askQuestionOption` — label, description.

Two constraints on the middle type, both load-bearing:

1. **The multi-select key is a `json.RawMessage`, not a `bool`.** A `bool` collapses
   an absent key into `false` and destroys the distinction the third slice needs. A
   raw-message field leaves an absent key `nil` and a present `false` as the four
   bytes `false`. Nothing here asserts on it; the field exists so #1952 and the
   multi-select slice decode this same object instead of redeclaring it.
2. **Options decode into a named two-field struct, not `[]json.RawMessage`.** #1952's
   five content checks read option labels and descriptions; leaving the element type
   opaque forces it to redo this decode.

State both at the type, and state the limit beside them: this slice reads only
`len(Questions)`, so `Header`, `Question`, `MultiSelect` and every option field are
declared-and-unread here. That is deliberate, not an oversight, and the file header
says which slice reads each.

Whether claude nests options under each question or flattens them across the batch is
**unmeasured** — that is what #1938's capture settles. Write the target against the
documented shape and let the capture redden it. Do not encode a guess as a second
accepted form.

### The check-name constants and their listing

Three unexported string constants, one per reportable outcome, plus a function
returning all of them. Suggested slugs (short, readable inside a failure message):
`tool_name`, `tool_input_decodes`, `questions_nonempty`.

The constants are shared between the emit site and the rows' `want` values. **The
limit, stated rather than hidden:** a misspelled constant is invisible to every row,
because both sides read the same identifier. That is accepted here and would not be
accepted in #1943's field listing, and the difference is who consumes the strings:
#1943's names are JSON tags a downstream decoder reads, so a second hand-written copy
earns its keep; these names are internal diagnostics no code outside this file
consumes.

The listing (`askQuestionCheckNames`, returning the three constants in emit order)
exists for the vacuity control below and for #1952, which appends five entries and
inherits the control for free. Nothing asserts that the listing matches the emit
sites — a reviewer diffs them, the same instrument #1943 relies on for its tags.

### `askQuestionShapeFindings(rec *askQuestionFixtureRecord) []string`

The deliverable. Pure, total over any record, no `testing.TB` parameter.

Behaviour, in order:

1. If `rec.ToolName` is not `"AskUserQuestion"`, append the tool-name constant. **This
   check reads the record field, not the decoded input** — that is what keeps the
   undecodable row at exactly one finding.
2. `json.Unmarshal(rec.ToolInput, &in)`. On error, append the decode constant and
   **return immediately**. The decoded-input checks are skipped, not reported beside
   it.
3. If the batch carries no question, append the questions constant.

Returns `nil` when nothing is appended.

Three properties the spec fixes rather than leaves to construction:

- **The return value carries no bytes from the record.** Only the fixed constants
  above are ever appended. Not the decode error, not a field value, not an excerpt.
  This is the design's security property and it is stronger than a "do not print it"
  rule at the wrapper: with #1938 calling this over a live child's bytes, a return
  type that structurally cannot carry child-derived text needs no discipline at the
  call site. Discarding `json.Unmarshal`'s error is deliberate; the malformed bytes
  are on disk in the artifact and inspectable there.
- **`nil`, never `make([]string, 0, 3)`.** `reflect.DeepEqual(nil-slice, []string{})`
  is **false**, so a pre-allocated empty return reddens the positive control with a
  message that reads `[] != []` and costs an hour. Return the zero value of a
  `var findings []string`.
- **The guard's early return is what the exact-equality rows pin**, see Testing below.

### `requireAskQuestionShape(t *testing.T, rec *askQuestionFixtureRecord)`

The thin fatal wrapper — the form other tests call. `t.Helper()`, call the pure check,
and on a non-empty result `t.Fatalf` naming the **count**, the **finding names**,
`rec.ClaudeVersionSlug` and `rec.ToolName`, and nothing else.

Never `%v`, `%+v` or `%#v` the record: `%+v` on `askQuestionFixtureRecord` prints
`ToolInput`, and #1938 calls this wrapper with a live child's bytes in that field.
`rec.ToolName` is admitted because it is a bounded tool identifier and because
`scanAskQuestionFixture` and `writeAskQuestionFixture` already print it; that is the
bound, not a licence to widen the set.

`t.Fatalf` requires the test goroutine. Call this directly from one — **not** from
#1938's stdout reader — the same rule `scanAskQuestionFixture`'s doc states.

**State the untestable link at the wrapper, in its doc comment.** A negative row
cannot assert on a `t.Fatalf`: it would take the calling subtest down, and
`testing.TB` cannot be implemented outside `testing`. So nothing proves that
`requireAskQuestionShape` *calls* `askQuestionShapeFindings`. That link is
construction. Say so at the wrapper rather than letting the rows be credited with
covering it — `TestInitControlFixture_ScanRefusesAPlantedCredential` states the same
limit for the sibling family, and the returning shape this slice ships is what keeps
the limit to that one link instead of to the whole check.

### `askQuestionShapeRecord(toolName, input string) *askQuestionFixtureRecord`

A four-line row builder: start from `askQuestionFullRecord()`, override `ToolName` and
`ToolInput`, return. It exists so every table row names **both** columns explicitly —
including the positive control, which passes `"AskUserQuestion"` and
`askQuestionFixtureInput` — which makes over-determination visible at a glance: each
negative row differs from the control in exactly one column.

The other two fields are carried untouched and unread by this assertion. That is the
limit; no control asserts on them, because a helper that corrupted
`ClaudeVersionRaw` would change nothing about this file's subject.

Do **not** add a fifth field to the record and do **not** reorder
`askQuestionFixtureInput`'s keys. Both are pinned by #1943's own tests; the key order
in particular is load-bearing, and tidying it into alphabetical order reddens #1943's
vacuity control.

### The `finOfflineExecBans` entry

Add one key, `"ask_user_question_shape_test.go"`, carrying the **seventeen** names from
the `"ask_user_question_record_test.go"` entry, copied whole:

```
resolveClaudeBin, WithWorktreeAuthenticated, WithWorktree, probeClaudeVersion,
captureClaudeVersion, os.Getenv, os.Environ, os.LookupEnv, packageDir,
setModeFixturePath, writeSetModeFixture, writeFixture, filepath.Glob, os.ReadFile,
os.WriteFile, os.Create, os.ReadDir
```

**Take the seventeen, not #1941's fourteen.** The fourteen sit adjacent in the same
table and the table's own comment calls that entry "the entry the two above warn
against copying whole"; it is deliberately narrower because that file writes a
directory. This file performs no I/O in either direction, like #1943's and #1944's.

**Rebase onto the existing entries — do not reflow the map.** The siblings' entries
are long and comment-heavy; a reflow buries this slice's one addition in a diff nobody
can review.

The entry's own comment budget goes to the two paragraphs that are *sharper here* than
in the entry it copies; the rest points at that entry rather than restating it:

- **`captureClaudeVersion`.** It returns the raw line and its leading token at once —
  both of this record's version fields — so it is the exec a developer populating a
  fixture reaches for first. It `t.Fatalf`s rather than skipping, so it would not fake
  a pass; what it would destroy is this file's defining property, that it settles with
  no claude binary at all.
- **The `packageDir` group plus `filepath.Glob` and the four `os` names.** This is the
  paragraph that matters most in *this* file, and more than in either entry it copies:
  a shape assertion is the one place where "prove it against a real capture" is the
  natural next thought. Two reasons it must not. First, AC 3. Second, it would be red
  on arrival — no committed `permission_protocol_*` capture holds an `AskUserQuestion`
  `tool_use` block, only the tool's name in the `system`/`init` tools array. And
  `go test` runs in the package source directory, so a **relative**
  `os.ReadFile("testdata/…")` reaches the committed captures while naming no wrapper
  at all, which is why the wrappers are listed alongside `packageDir` itself.

Also state, briefly: `t.TempDir` is absent because this file writes nothing; the only
in-package helper it calls is `askQuestionFullRecord`, which is pure literals; and the
check is per-file **syntax**, not a call graph, so a banned read stays reachable
through a helper this file calls while the ban stays green.

### The file header

The header states this file's limit explicitly rather than leaving a reader to infer
it from a missing check:

- **Exactly two shape checks ship here** — the tool-name field and the presence of at
  least one question. The five content checks over the first question (question text,
  header, option count, option labels, option descriptions) are **#1952**; the
  multi-select key is the slice after that.
- **#1952's "seven checks" counts shape checks only and stays correct.** The decode
  guard is not one of them, though its negative row is additional. Say this, so the
  successor's own arithmetic is not read as contradicting yours.
- The offline property, and the one paragraph about #1942 described under Context.
- The run line and how to read it:

  ```
  go test -tags e2e_realclaude -race -count=1 -v \
    -run 'TestAskQuestionShape|TestFinOfflineFilesReachNoExecHelper' \
    ./internal/e2e/realclaude/
  ```

  Every one must report PASS — not SKIP, not "no tests to run" — on a machine with no
  claude and no credentials. **Read the count of tests that executed, never the exit
  code**: this package is behind `e2e_realclaude`, `make check` never compiles it, and
  the suite exits 0 both on a build failure and on a full credentials skip.

## Concurrency model

No goroutines, no channels, no shared mutable state.

`askQuestionShapeFindings` is pure over its parameter. Each table row owns a record
minted by its own `askQuestionFullRecord()` call, which returns a fresh pointer per
call precisely so parallel subtests share nothing — #1943's stated reason. Subtests
may therefore be `t.Parallel()`.

The one rule that reaches beyond this file: `requireAskQuestionShape` fatals, and
`t.Fatalf` requires the test goroutine. Documented at the wrapper for #1938's benefit.

## Error handling

There is no error return anywhere in this slice. Three failure modes and what each
does:

| Failure | Handled by | Result |
|---|---|---|
| `rec.ToolName` is not the tool's name | the tool-name check | one finding, decoded checks still run |
| `rec.ToolInput` does not parse | the decode guard | one finding, decoded checks **skipped** |
| the batch carries no question | the questions check | one finding |

Arbitrary hostile bytes in `ToolInput` are handled by `json.Unmarshal` returning an
error rather than panicking (Go's decoder bounds nesting depth), so the guard is what
makes the check **total**. There is no byte cap here and none is needed: the bytes are
already in memory when the check runs. **The cap belongs to #1938's reader**, at the
point that fills the field — named here so #1938 does not assume this slice capped it.

## Testing strategy

One test function, table-driven, stdlib only. Suggested name:
`TestAskQuestionShape_ReportsEachMissedCheckAndSkipsAfterAnUndecodableInput`.

Row type: a name, a record, and a `want []string`. Compare with
`reflect.DeepEqual(got, row.want)` — **exact equality, never containment**. The
failure message prints both slices with `%q` **and both lengths**, so a nil-versus-
empty mismatch is diagnosable from the message rather than from a debugger.

Four rows, each named for what it is:

- **Positive control** — `askQuestionFullRecord()`'s two columns unchanged
  (`"AskUserQuestion"`, `askQuestionFixtureInput`). `want` is **omitted**, i.e. `nil`,
  never `[]string{}`. Reports nothing.
- **Wrong tool name** — a synthetic, obviously-not-real name carrying the `-FIXTURE`
  marker (`"ExitPlanMode-FIXTURE"` or similar); input unchanged, so it still decodes
  and still carries a question. `want` is the tool-name constant alone.
- **No question** — tool name correct; input is a valid object with an explicitly
  empty batch (`{"questions":[]}`). An explicitly-empty batch rather than an omitted
  key, because that is the realistic malformed capture and it reads as a batch-length
  check rather than a key-presence one. `want` is the questions constant alone.
- **Undecodable input** — tool name correct; input is truncated JSON
  (`{"questions":` or similar). `want` is the decode constant alone.

Plus one vacuity control, its own subtest over `askQuestionCheckNames`: the check
names are pairwise **distinct** and none is empty. Without it, AC 1's "each missed
check named individually" is unpinned — two colliding constants would make the
wrapper's message unable to say which check fired, and every row would still pass. It
also grows with #1952 for free.

### Why AC 2's sole-redness holds by construction — and why no overlay run is needed

Exact equality is what buys this out of a per-check mutation matrix, and the argument
is short enough for a reviewer to check instead of run:

- **Delete the tool-name check** → the wrong-tool-name row returns `nil` against a
  `want` of one name and reddens. Every other row returns exactly its own finding and
  stays green. Sole red.
- **Delete the questions check** → the no-question row reddens alone, same shape.
- **Delete the decode guard's early return** (keeping the appended finding) → the
  undecodable row returns **two** findings, because the batch of a failed decode is
  empty and the questions check then fires too. The exact match reddens; the other
  three rows are untouched. **So the "skipped rather than reported beside it" clause
  of AC 1 is pinned by a row, not by construction.**
- **Delete the guard entirely** → the undecodable row returns one finding under the
  *wrong* name and reddens alone.

Over-determination is unshippable for the same reason: a fixture tripping two checks
returns two findings and fails its exact match on unmutated code, so it cannot reach
the branch. A containment assertion gives neither property and would cost an overlay
run per check.

### Proving the ban entry non-vacuously

`TestFinOfflineFilesReachNoExecHelper` adds a subtest keyed by the new filename as
soon as the entry lands. **An overlay mutant cannot prove it** — #1943's measured
lesson: the check calls `parser.ParseFile` on a relative filename and a build overlay
does not intercept that read. The only proof is to write one banned call into the real
worktree file, run the one subtest, confirm it reddens, and revert. Do that once, for
one name, and say in the PR that you did.

### Gate

`make preship` is the gate that compiles this package; `make check` never does, and a
package that fails to build exits 0 through a shell wrapper with zero tests run. Read
the `=== RUN` count.

## Line budget

**~380–400 total, and the boundary is 400.** The two checks are ~15 lines of the file;
everything else is this package's fixed per-file cost, which is why a further cut
brings no child lower. Rough allocation: header ~55, decode target ~45, constants and
listing ~25, the check ~55, the wrapper ~25, the row builder ~15, the table test ~100,
the vacuity control ~20, the ban entry ~40.

#1943's file overran its own spec's budget by 42% on comment density alone, so treat
this as binding rather than indicative. **If you overrun, cut the header's history
paragraphs — not the reasoning at the symbols.** The arguments at the multi-select
field, at the `nil` return, at the untestable link and in the ban entry's two sharpened
paragraphs are the ones a future reader cannot reconstruct.

## Open questions

- **Nesting versus flattening.** Whether claude nests options under each question or
  flattens them across the batch is unmeasured. Resolved by #1938's capture, not here.
  Do not add a second accepted form.
- **The `newDropcapScanner` gap in the seventeen names.** See the security review
  below. Taken as-specified for this ticket; closing it is table-wide work.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No finding. In this slice every record is a synthetic
  literal, so nothing untrusted crosses. The boundary that matters is the *future*
  one: `requireAskQuestionShape` is designed for #1938 to call over a live child's
  bytes. The design addresses it structurally rather than by convention —
  `askQuestionShapeFindings` returns only fixed constants, so the return type cannot
  carry child-derived text at all, and the `json.Unmarshal` error is discarded rather
  than wrapped into a finding.
- **[Tokens, secrets, credentials]** SHOULD FIX, taken as-specified. The seventeen
  copied names do **not** include `newDropcapScanner` or `realHome` — only #1941's
  narrower fourteen do. Because `TestFinOfflineFilesReachNoExecHelper` is a per-file
  AST match and not a call graph, a call to `newDropcapScanner` from this file would
  reach `os.Getenv` transitively, put `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY`
  in a struct in this file's scope, and leave the `os.Getenv` ban green. Not fixed
  here for two reasons: the ticket instructs "take the seventeen", and the gap is
  identical in the `ask_user_question_record_test.go` and `ask_user_question_names_test.go`
  entries, so widening one entry would break the "copied whole" convention that makes
  these entries reviewable without closing the class. Mitigations this spec does adopt:
  the file constructs no scanner, calls only `askQuestionFullRecord` from the package,
  imports exactly three stdlib packages, and has no struct-formatting site at all.
  Closing the class belongs in a table-wide ticket.
- **[File operations]** No finding. The file reads and writes nothing; the `packageDir`
  group plus `filepath.Glob` and the four `os` names enforce it. The specific hazard is
  called out in the entry's comment because it peaks in a shape-assertion file: `go
  test` runs in the package source directory, so a relative `os.ReadFile("testdata/…")`
  reaches the seventeen committed captures without naming a wrapper. No path is
  constructed from any input, so traversal, TOCTOU, mode and symlink questions are all
  vacuous here.
- **[Subprocess / external command execution]** No finding. Nothing execs. The first
  five banned names are what keep it that way, and the sharper hazard they close is a
  **skip**, not an exec: `resolveClaudeBin` and `WithWorktreeAuthenticated` skip inside
  the test body after `=== RUN` prints, and a skip exits 0, which reads as a pass under
  `make e2e-realclaude`.
- **[Cryptographic primitives]** Not applicable, by design decision rather than
  omission: no randomness is generated, and `reflect.DeepEqual` compares returned
  findings against fixed in-file constants — no attacker-influenced value is ever
  compared against a secret, so constant-time comparison has no site here.
- **[Network & I/O]** No finding, with one boundary named so it is not assumed away.
  `askQuestionShapeFindings` sets no size cap on `rec.ToolInput`; the bytes are already
  in memory when it runs, so the cap belongs at the read that fills the field in
  #1938. Hostile input is handled rather than crashing — Go's decoder bounds nesting
  depth and returns an error — which is precisely what the guard converts into one
  named finding, making the check total over arbitrary bytes.
- **[Error messages, logs, telemetry]** No finding, and this is the category the
  design is shaped around. `askQuestionShapeFindings` returns constants only. The
  wrapper's `t.Fatalf` prints count, finding names, `ClaudeVersionSlug` and
  `ToolName` — never `ToolInput`, never the decoded structs, never a question or option
  string, never the decode error. `%+v` on the record is banned outright because it
  prints `ToolInput`. `ToolName` is admitted as a bounded tool identifier, matching
  `scanAskQuestionFixture` and `writeAskQuestionFixture`; that is the bound, not an
  opening. The table's own failure messages print the returned findings and the row
  name only. In this slice every value is synthetic and printing would be harmless —
  the rule is set here because here is where it can be set, and #1938 inherits the same
  wrapper over live bytes into run logs this pipeline salvages.
- **[Concurrency]** No finding. No goroutines, no locks, no shared mutable state; each
  row's record is a fresh pointer from `askQuestionFullRecord()`. One forward-looking
  rule is documented at the wrapper: `t.Fatalf` requires the test goroutine, so
  `requireAskQuestionShape` must not be called from #1938's stdout reader.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model is
  relay-scoped and does not apply. The applicable model is the one
  `offline_exec_ban_test.go`'s own header states for this package: the process
  environment carries `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY`; agent run logs
  are salvaged by the pipeline; and `testdata/` holds seventeen committed captures a
  relative write would overwrite. All three are addressed — no environment read (ban),
  no value in any message (construction), no filesystem call at all (ban).

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-09-01
