# #1701 — the `initialize` capture's fixture record and its fully-populated fixture

Test-only. Two files touched, both `_test.go`, both in `internal/e2e/realclaude`. No production
file changes.

## Files to read first

Cite by symbol. Resolve any name below with `codegraph_search` / `codegraph_node`.

- `internal/e2e/realclaude/set_permission_mode_probe_test.go` → `setModeFixtureRecord` — the
  eighteen shared fields. Copy their JSON tags and Go types **verbatim**; do not re-choose a type.
  Its doc comment is also where the no-`env` rule and its reasoning originate.
- `internal/e2e/realclaude/inband_bypass_revoke_fixture_test.go` → `poolRevokeFixtureRecord`,
  `poolRevokeFullRecord`, `fixtureFieldNonZero`, and the "every field carries a non-zero value"
  and "same-typed fields carry distinct values" subtests inside
  `TestPoolRevokeFixture_RoundTripsEveryFieldIntoOneNamedEntry`. This is the machinery model for
  everything this ticket builds — record shape, fully-populated fixture, both properties. Read its
  file header too, for the offline-prose shape.
- `internal/e2e/realclaude/initialize_control_names_test.go` → `initControlFixtureName` — the
  namer whose *input* AC 4 constrains, and its `CONTRACT for #1697's writer` doc paragraph (that
  writer is #1702; the header's ticket number is stale and is **not** yours to edit). Read this
  file's header as the shape for yours: it is the closest sibling that performs no I/O in either
  direction.
- `internal/e2e/realclaude/permission_protocol_spike_test.go` → `versionSlug` and
  `versionSlugSubst` — the exact rewrite AC 4's literal must not survive unchanged; and
  `captureClaudeVersion`, so you know what the ban keeps out and why it is the tempting call.
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans` (specifically the
  `initialize_control_names_test.go` entry and the doc comment above it — you copy the entry whole)
  and `TestFinOfflineFilesReachNoExecHelper` (the AST match semantics: a dotted entry matches a
  selector, a bare entry matches an identifier, and the parse takes no `parser.ParseComments`).
- `docs/knowledge/features/e2e-realclaude.md` § "What's there today" → the
  `inband_bypass_revoke_fixture_test.go` (#1662) bullet and the `initialize_control_names_test.go`
  (#1696) bullet. Two things there that change what you write: #1662's code review corrected the
  claim that non-zero/distinct values catch "a field decoded from the wrong tag" — only a
  **colliding** tag is caught; a unique wrong tag round-trips green — and flagged it as worth
  remembering for the next file in this family, which is this one. #1696's bullet carries the
  `-overlay`-cannot-verify-an-AST-check caveat you need for verifying the ban entry.

## Context

The daemon wants to publish claude's model list to connected clients. The child it already
supervises hands that list over: a `control_request` with subtype `initialize`, written on the
child's held-open stdin, comes back with a `models` array. That was measured by hand against
claude 2.1.220 on 2026-08-21, outside this repo. Three slices decode against that JSON contract —
#1688 captures it live, #1690 decodes it, #1692's fake replays it — and nothing in the tree
records the shape yet.

This ticket fixes the contract and pins the fixture that stands in for it. It writes **no writer**:
#1702 is the writer, #1700 proves the cap, #1688 spends the tokens. What lands here is a record
type, one fully-populated instance of it, a single field listing #1702 reuses on both sides of its
round trip, and the assertions that keep that fixture from degenerating into something a broken
writer round-trips green.

The `-overlay` caveat and the collapsed-name-dimension lesson from #1696 both belong in the
package overview, not in a new ADR. No ADR is warranted here; this slice makes no decision that
outlives the fixture family.

## Design

### File layout

One new file, `internal/e2e/realclaude/initialize_control_record_test.go`, named for the record it
holds. It must not acquire a writer: `finOfflineExecBans` is keyed by file name and each file gets
exactly one entry, and this file's entry bans the five I/O verbs #1702's round trip legitimately
needs. #1702 gets its own file (`initialize_control_fixture_test.go` is the free name matching
`inband_bypass_revoke_fixture_test.go`, but that is #1702's call, not yours).

Package-scope surface, five identifiers — the record type, the row type, the fixture function, the
listing function, and one test. #1702 and #1700 reach three of them.

Imports: `encoding/json`, `reflect`, `testing`. Nothing else is needed, and `os`, `path/filepath`
and `os/exec` must not appear.

### The record

```go
type initControlFixtureRecord struct { /* 22 fields, declaration order per the table below */ }
```

Twenty-two fields, no `env` field, ever. Declaration order is the ticket's table order, grouped
the way `setModeFixtureRecord` groups: version, invocation, control request/response, models
summary, surrounding stream, instrument health. The order is load-bearing twice — the listing
below is written in the same order so a reviewer can eyeball one row against one field, and #1702's
zip reads more legibly for it.

| # | JSON tag | Go type | from `setModeFixtureRecord` |
|---|---|---|---|
| 1 | `claude_version_raw` | `string` | yes |
| 2 | `claude_version` | `string` | yes |
| 3 | `argv` | `[]string` | yes |
| 4 | `prompts` | `[]string` | yes |
| 5 | `control_request_id` | `string` | yes |
| 6 | `control_request_sent` | `json.RawMessage` | yes |
| 7 | `control_responses` | `[]json.RawMessage` | yes |
| 8 | `control_response_subtype` | `string` | **new** |
| 9 | `control_response_request_id_matched` | `bool` | yes |
| 10 | `models_present` | `bool` | **new** |
| 11 | `models_count` | `int` | **new** |
| 12 | `models_entry_fields` | `[]string` | **new** |
| 13 | `stdout_events` | `[]json.RawMessage` | yes |
| 14 | `non_json_line_count` | `int` | yes |
| 15 | `turn_boundaries` | `[]int` | yes |
| 16 | `stdin_write_errors` | `[]string` | yes |
| 17 | `stderr_capture` | `string` | yes |
| 18 | `exit_code` | `int` | yes |
| 19 | `wait_error` | `string` | yes |
| 20 | `context_deadline_tripped` | `bool` | yes |
| 21 | `duration_ms` | `int64` | yes |
| 22 | `scanner_error` | `string` | yes |

Eighteen "yes" rows, four "new" — that is the ticket's arithmetic, re-derived against
`setModeFixtureRecord` and confirmed. Go field names follow the sibling's where one exists
(`ControlResponseRequestIDMatched`, `NonJSONLineCount`, `DurationMs`, …); the four new ones are
`ControlResponseSubtype`, `ModelsPresent`, `ModelsCount`, `ModelsEntryFields`.

Doc-comment obligations on the type, each one sentence or two — these are the notes a later reader
needs and cannot re-derive:

- **No `env` field, ever.** The credential reaches the child through the environment while the
  argv carries none, so recording argv is safe and recording env is not. Inherited from
  `setModeFixtureRecord`, which is where the rule originates.
- **`control_response_request_id_matched` is the one derived field**, and it sits with the response
  fields rather than instrument health on purpose: a request-id mismatch is a finding about
  claude's protocol, not a fault in the harness. It is a claim about what the capturing run
  observed, not a second copy of the data — a reader who disagrees with it treats the verbatim
  bytes as authoritative.
- **The three `models_*` fields are populated, never computed.** #1688's live run fills them from a
  real response. A summarizer built here would have no response to summarize.
- **`scanner_error` is load-bearing for #1688**, which caps its reader per line; an over-long line
  must surface here rather than truncating silently.
- **Nothing here caps anything.** `stderr_capture` is bounded by #1702's writer via
  `capFixtureCapture`, and the three verbatim-bytes fields are uncapped by design, for the reason
  `poolRevokeFixtureRecord`'s doc gives for `ControlResponses`: capping structured evidence
  destroys the artifact.

### The fully-populated fixture

```go
func initControlFullRecord() *initControlFixtureRecord
```

A **function returning a fresh pointer**, never a package-level `var`. #1700's cap test mutates
the returned record (`rec.StderrCapture = tc.capture`) across parallel subtests exactly as
`TestPoolRevokeFixture_WriterCapsChildOutputCapture` does with `poolRevokeFullRecord`; a shared
var would be a data race under `-race` in a sibling ticket, discovered two tickets away from the
line that caused it.

Every one of the twenty-two fields carries a non-zero value. Constraints on the literals, in
descending order of how expensive they are to get wrong:

**1. `claude_version` must not survive `versionSlug` unchanged.** This is AC 4, and it is the one
literal carrying #1702's redness. `versionSlug` lowercases, then rewrites runs outside
`[a-z0-9._-]` to `_`, then clamps at 32. A realistic `2.1.220` is already slug-clean, so
`initControlFixtureName("2.1.220")` is byte-identical to what a writer formatting its own
`fmt.Sprintf("initialize_control_v%s.json", token)` produces — and #1702's "named exactly what the
namer mints" assertion is **0-red** against precisely the writer #1696's lock exists to close, with
#1696's own test still green.

Use `"2.1.220-FIXTURE"`. One uppercase run is enough because the slug lowercases first, and the
word `FIXTURE` is itself the signal that stops a later reader "correcting" the literal to a clean
version. `claude_version_raw` then takes `"2.1.220-FIXTURE (Claude Code)"` — distinct from
`claude_version`, which the distinctness property requires of those two strings.

This is a claim about the fixture's own literal and about nothing else. It is not an assertion
about what `claude --version` emits, and it must not be sourced from `captureClaudeVersion` — see
the ban entry.

**2. Keep `<`, `>` and `&` out of all three embedded raw-JSON literals.** `encoding/json` escapes
those three inside a `json.RawMessage`, the escape survives `json.Compact`, and #1702's read-back
row then reddens for a perfectly correct writer. Following the sibling's types this record has
three such fields, not #1662's one: `control_request_sent`, `control_responses` and
`stdout_events`. `stdout_events` is the easiest to get wrong, because a realistic assistant-text
event is where an `&` or a `<` actually turns up. A plain `string` field carrying the same
characters round-trips unchanged — the constraint is specific to the raw-JSON fields.

**3. `control_responses` and `stdout_events` must carry different content.** They are both
`[]json.RawMessage`, so the distinctness property binds them — and in a real capture the control
response arrives *on* stdout, which is exactly the literal a developer repeats into both. The
resolution is two literals, not a weakened assertion. Suggested shapes: `control_responses` holds
one `control_response` envelope carrying a small `models` array (identifier, display name,
reasoning-effort levels); `stdout_events` holds a `system`/`init` event and one assistant-text
event.

**4. The fixture is not a coherent narrative, and must not be "fixed" into one.** The non-zero
property forces every instrument-health field to carry a "something went wrong" value at the same
time: `exit_code` non-zero, `wait_error` non-empty, `scanner_error` non-empty,
`context_deadline_tripped` true, `stdin_write_errors` non-empty — alongside a successful
`control_response_subtype`. All three bools are `true` for the same reason. Say so in the doc
comment; a reader who reads this as a capture will try to make it consistent and empty half the
properties doing it.

Keep the identifiers self-consistent where it costs nothing: `control_request_id`,
the `request_id` inside `control_request_sent`, and the one inside `control_responses` all agree,
which is what `control_response_request_id_matched: true` claims.

**5. The distinctness groups.** Non-bool fields must be pairwise distinct *within their Go type*.
The groups, which is the design work — the seven-string group is larger than any sibling's and is
where this gets missed:

| Go type | fields sharing it | n |
|---|---|---|
| `string` | `claude_version_raw`, `claude_version`, `control_request_id`, `control_response_subtype`, `stderr_capture`, `wait_error`, `scanner_error` | 7 |
| `[]string` | `argv`, `prompts`, `models_entry_fields`, `stdin_write_errors` | 4 |
| `int` | `models_count`, `non_json_line_count`, `exit_code` | 3 |
| `[]json.RawMessage` | `control_responses`, `stdout_events` | 2 |
| `json.RawMessage` | `control_request_sent` | 1 |
| `[]int` | `turn_boundaries` | 1 |
| `int64` | `duration_ms` | 1 |
| `bool` (exempt) | `models_present`, `control_response_request_id_matched`, `context_deadline_tripped` | 3 |

`reflect.TypeOf` distinguishes named types, so `json.RawMessage` and `[]json.RawMessage` and
`[]string` are three different groups and never compare against each other.

### The field listing

```go
type initControlFixtureField struct {
    name  string
    value any
}

func initControlFixtureFields(rec *initControlFixtureRecord) []initControlFixtureField
```

Twenty-two rows in declaration order, each `{"<json tag>", rec.<Field>}`. #1702 applies this to
both the written record and its decode and zips the two, rather than restating the fields; #1700
reaches it the same way.

**Hand-write the rows. Do not generate them by walking the struct with reflection.** Two reasons,
and the second is the real one:

- A reflection-driven listing can never be missing a row, so AC 2's length assertion becomes
  tautological — a vacuous assertion, which is the defect this family spends most of its comment
  budget avoiding.
- The hand-written names are a **second, independent copy of the JSON tags**. The one defect a
  symmetric struct round trip structurally cannot catch is a misspelled tag (#1662's header says
  so, and #1662's code review sharpened it further: even a *unique wrong* tag round-trips green;
  only a colliding tag is caught, because `encoding/json` drops both). These tags are not read by
  humans alone — #1690's decoder and #1692's fake read them — so a misspelling here is a real
  defect two tickets downstream. A reviewer diffing the listing's names against the struct's tags
  against this spec's field table is the instrument that catches it, and a reflection listing
  reads the name off the tag, so a misspelled tag produces a matching misspelled row and that
  instrument is gone.

For the same reason, do **not** add a reflection check asserting the row names equal the struct
tags. That question is noted under Open questions; it is not this slice's.

### The `finOfflineExecBans` entry

Register `initialize_control_record_test.go` with exactly the seventeen names of the
`initialize_control_names_test.go` entry, copied whole:

```
"resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
"probeClaudeVersion", "captureClaudeVersion",
"os.Getenv", "os.Environ", "os.LookupEnv",
"packageDir", "setModeFixturePath", "writeSetModeFixture", "writeFixture",
"filepath.Glob", "os.ReadFile", "os.WriteFile", "os.Create", "os.ReadDir",
```

That entry fits for #1696's own stated reason: like #1696's file, this one performs **no I/O in
either direction**. It builds a record and asserts on its values. Do not widen it toward #1702's
narrower twelve, and do not narrow it toward #1662's.

Two names get their own sentence in the entry's doc comment rather than being taken on
inheritance:

- **`captureClaudeVersion` matters more here than in #1696.** It is the package's own direct
  `claude --version` exec and it returns `(raw, token)` — *both* of this record's version fields at
  once — so AC 2's fully-populated fixture gives a developer two pulls toward it, and
  `versionRaw, versionToken := captureClaudeVersion(t)` is already the literal line four sibling
  files use. It `t.Fatalf`s rather than skipping, so it would not fake a pass; what it would
  destroy is this file's defining property, that it settles with no claude binary at all — and it
  would take AC 4 down with it, because a real token is slug-clean and the slug guard would redden
  against honest code, with `TestFinOfflineFilesReachNoExecHelper` green the whole time. Do not
  harmonise it away against the older siblings that omit it.
- **`os.LookupEnv`** is the two-value form of `os.Getenv` reading the same environment, which here
  carries `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY`. #1662's entry omits it; #1696 added it
  as a deliberate superset. Follow #1696.

`exec.Command` / `exec.CommandContext` are declined for #1696's reason: this file imports no
`os/exec`, and the package's own exec helpers are already covered above. Do not add them
speculatively.

### The file header

Siblings in this family run 50–80 header lines and that is the right order of magnitude, but the
budget is real. State these and stop:

1. What this file is (#1701, the record half) and what it deliberately is not (no writer — that is
   #1702; no cap proof — #1700; no live run — #1688).
2. What the two properties buy. Be precise, and do **not** paraphrase #1662's list: this file
   performs no round trip, so non-zero and distinctness here are *preconditions on the fixture*,
   asserted so that #1702's and #1700's rows cannot silently degenerate. A zero-valued field
   round-trips green under any tag arrangement; two same-typed fields carrying the same value make
   a writer that swapped their tags round-trip green. Those are the two mutants. Do not claim "a
   field decoded from the wrong tag" — #1662's review established that only a *colliding* tag is
   caught, and this file catches neither, since it does not round-trip at all.
3. The offline property, the ban entry that enforces it over the AST rather than over this
   paragraph, and the run line:

   ```
   go test -tags e2e_realclaude -race -count=1 -v \
     -run 'TestInitControlFullRecord_|TestFinOfflineFilesReachNoExecHelper' \
     ./internal/e2e/realclaude/
   ```

   with the standing warning: both must report PASS, not SKIP and not "no tests to run", on a
   machine with no claude and no credentials; read the count of tests that executed, never the
   exit code, because this package is behind the `e2e_realclaude` tag and the suite exits 0 both
   on a build failure and on a full credentials skip.

Do **not** restate the field table in prose, and do not re-derive #1696's glob argument — point at
`initControlFixtureName` for it.

## Concurrency model

No goroutines, no channels, no context. The only concurrency is `testing`'s: the test and its
subtests take `t.Parallel()`, as `TestInitControlFixtureName_AvoidsCommittedFamiliesAndStaysContained`
does. The parent computes the record and the listing once; subtests only read them, so the shared
slice is safe. `initControlFullRecord` returning a fresh pointer per call is what keeps that true
for #1700's mutating subtests — see the Design note above.

## Error handling

Test-only, so the vocabulary is `t.Errorf` / `t.Fatalf` rather than error values.

- `t.Errorf` for every property violation: a missing or duplicated listing row, a zero-valued
  field, a same-typed pair sharing a value, a `claude_version` that survives slugging. All four are
  claims about this file's own literals, all four are independently interesting, and reporting
  every offender beats reporting the first.
- `t.Fatalf` nowhere. There is no instrument to break: nothing is parsed, nothing is read, nothing
  is spawned. If a future edit introduces a step that can fail structurally, that step is
  `t.Fatalf`'s.
- Failure messages name the row and, at most, the one value at issue. Never `%+v` the record.
  Every value here is this file's own synthetic literal so nothing leaks today, but #1688 fills
  this same record from a live child, and `writePoolRevokeFixture`'s doc explains why a `%+v`
  there moves bounded bytes into an unbounded run log. Do not set the precedent.

## Testing strategy

One test function, `TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken`, with
four subtests. Scenarios as bullets — write them in the package's idiom:

- **the listing covers every field** — `len(initControlFixtureFields(rec))` equals
  `reflect.TypeOf(initControlFixtureRecord{}).NumField()`, so a field added later with no row
  reddens instead of going silently unchecked (AC 2). Plus a five-line pass over the rows
  asserting the names are pairwise distinct: length alone is green against a listing that names one
  field twice and omits another, which is exactly the outcome AC 2 exists to prevent. Failure
  message should say which count it got and which it wanted, and name the duplicate.
- **every listed field carries a non-zero value** — one pass over the rows through the existing
  `fixtureFieldNonZero`. Call it; do not rewrite it. Its kind switch is the point: a `[]string{}`
  is `!IsZero()` and proves nothing, so container kinds are judged on length.
- **same-typed non-bool fields carry distinct values** — the pairwise `O(n²)` pass over the rows,
  skipping rows whose `reflect.ValueOf(...).Kind()` is `reflect.Bool`, comparing only rows whose
  `reflect.TypeOf` agree, failing on `reflect.DeepEqual`. Booleans cannot carry distinct non-zero
  values — there is only one — so the property is scoped rather than written as something that
  cannot hold, and a swapped pair of bools stays invisible. Say that in the subtest's comment; the
  non-zero subtest is what catches a bool tag *collision*, since `encoding/json` drops both and
  they read back `false`.
- **the version token does not survive slugging** — `versionSlug(rec.ClaudeVersion)` differs from
  `rec.ClaudeVersion` (AC 4). The failure message is the load-bearing part: it must say that
  #1702's "named exactly what the namer mints" goes 0-red against a slug-clean token, so a reader
  who arrives here after "correcting" the literal learns what they emptied.

The fifth check is `TestFinOfflineFilesReachNoExecHelper`, which acquires a subtest for the new
file automatically once the map entry exists (AC 5).

### Verification

The gate cannot see this package. Run, and report the count of `=== RUN` lines rather than the
exit code:

```
go build -tags e2e_realclaude ./...
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestInitControlFullRecord_|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/
```

Then `make check` for the rest of the repo and `make cite-guard` for the comments.

### Mutants each assertion must redden

Run these with `go test -overlay=<abs-path json>` so nothing is written into the worktree —
**except the last, which overlay cannot verify.**

| mutant | expected red |
|---|---|
| drop one row from the listing | listing-covers-every-field (length) |
| list `argv` twice and drop `prompts` | listing-covers-every-field (name uniqueness) — length stays green, which is why the uniqueness pass is there |
| `ClaudeVersion` set to a slug-clean `"2.1.220"` | version-token-does-not-survive-slugging, and nothing else |
| `StdoutEvents` set equal to `ControlResponses` | same-typed-fields-distinct |
| `ExitCode` set to `0` | every-field-non-zero |
| `ContextDeadlineTripped` set to `false` | every-field-non-zero |
| an `os.Getenv` call added to the new file | `TestFinOfflineFilesReachNoExecHelper/initialize_control_record_test.go` |

The last row needs a **real edit and a real revert**, confirmed byte-identical against the pristine
file before committing. `TestFinOfflineFilesReachNoExecHelper` calls `parser.ParseFile` with a
`nil` source, so it reads the registered file off disk at test run time; `-overlay` is a
build-time mapping consumed by the `go` command and never interposes on the test binary's own
reads. A banned call injected via overlay compiles cleanly while the check parses the unmodified
file and stays green — a misleading pass that reads as "the ban does not bite". Overlay stays
correct for every other row above.

### Known non-reds, stated so nobody credits them

- Swapping the values of two bools is invisible: all three are `true`, and the distinctness
  property is scoped away from them by design.
- Swapping two fields' JSON tags is invisible *here* — nothing round-trips in this file. That
  mutant is #1702's, and the distinctness property is what makes it red there.
- A *unique* wrong tag round-trips green even in #1702. Only a colliding tag is caught. This is
  #1662's code-review correction; do not write a header sentence that claims otherwise.

## Open questions

- **Should the listing's row names be checked against the struct's JSON tags?** A reflection pass
  comparing them would catch a divergence in either copy, at roughly a dozen lines. It is
  deliberately not in this slice: it is outside AC 2, and it weakens the "second independent copy"
  argument the hand-written listing rests on by making disagreement the only thing anyone checks.
  If a tag typo ever ships, that is the fix to reach for — as a follow-up, not as scope creep here.
- **The listing's stable ordering is a contract #1702 depends on but nothing asserts.** #1702 zips
  two calls to `initControlFixtureFields`, which is safe because both walk the same hand-written
  literal, so a reordering moves both sides together. Nothing pins it; nothing needs to, unless
  #1702 ends up matching rows by name instead.
- **`compactRawMessages` and the raw-JSON rows.** #1702 has to normalise the three raw-JSON fields'
  whitespace on both sides of its round trip, and how it reaches those rows through the listing —
  matching on `name`, or compacting the records before listing them — is #1702's design call, not
  constrained here.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No finding here, by design. This file defines a *recording* shape and
  populates it with synthetic literals; nothing crosses a trust boundary in it. The boundary the
  contract eventually sits on — claude's stdout and control responses into daemon state — is
  #1690's decoder, and the design is explicit that `control_request_sent`, `control_responses` and
  `stdout_events` are verbatim child bytes with no validation applied at this layer. OUT OF SCOPE,
  named: hostile-shape coverage for that decoder is #1690's and #1692's, not this fixture's.
- **[Tokens, secrets, credentials]** The one live risk in this record type is the `env` field that
  must never exist, and the design forbids it at the type's doc comment, inheriting
  `setModeFixtureRecord`'s reasoning: the credential (`CLAUDE_CODE_OAUTH_TOKEN`,
  `ANTHROPIC_API_KEY`) reaches the child through the environment while the argv carries none, so
  recording argv is safe and recording env is not. Second-order: `stderr_capture` is the field an
  auth failure dumps a credential-bearing message into. This slice caps nothing — the bound is
  `capFixtureCapture` applied by #1702's writer, proven by #1700 — and the spec states that
  explicitly rather than letting the record's doc imply a bound it does not carry. OUT OF SCOPE,
  named: #1702 and #1700.
- **[File operations]** No finding: this file performs no I/O in either direction, which is its
  defining property. That is not left to prose — it is enforced over the file's AST by
  `TestFinOfflineFilesReachNoExecHelper` against the `finOfflineExecBans` entry, which bans
  `os.ReadFile`, `os.WriteFile`, `os.Create`, `os.ReadDir` and `filepath.Glob` along with
  `packageDir` and all three of its wrappers. The relative-path hazard those close is specific and
  real: `go test` runs in the package source directory, so a relative `os.WriteFile("testdata/…")`
  reaches the committed fixtures without naming `packageDir` at all.
- **[Subprocess / external command execution]** No finding, same enforcement: `resolveClaudeBin`,
  `probeClaudeVersion`, `captureClaudeVersion`, `WithWorktree` and `WithWorktreeAuthenticated` are
  all in the ban entry. `captureClaudeVersion` is the one this file specifically attracts, because
  it returns both version fields at once; the design calls that out and the ban is what makes the
  callout enforceable rather than advisory.
- **[Cryptographic primitives]** N/A by design decision: no randomness, no keys, no comparison
  against a secret. Every value in the fixture is a fixed literal, and determinism is the point —
  #1702 and #1700 assert against these exact literals.
- **[Network & I/O]** No finding in this slice, and one deliberate non-cap worth naming: the three
  verbatim-bytes fields have no size limit, for the reason `poolRevokeFixtureRecord`'s doc gives —
  capping structured evidence destroys the artifact. The unbounded-input risk lands on #1688's
  live run, where `scanner_error` is the field that must surface an over-long line rather than let
  it truncate silently. That is why the field is defined here even though nothing fills it here.
- **[Error messages, logs, telemetry]** SHOULD FIX, addressed in the spec's Error handling section:
  the distinctness subtest prints the shared value, which is safe in this file (synthetic
  literals) but is a pattern #1688 must not inherit, since the same record type there holds real
  child output. The spec requires failure messages to name the row and at most the one value, and
  forbids `%+v` of the record, matching `writePoolRevokeFixture`'s discipline.
- **[Concurrency]** No finding, given a design decision that would otherwise be a MUST FIX:
  `initControlFullRecord` is a **function returning a fresh pointer**, not a package-level `var`.
  #1700's cap test mutates the returned record across parallel subtests exactly as
  `TestPoolRevokeFixture_WriterCapsChildOutputCapture` does; a shared var would be a `-race` data
  race surfacing two tickets away from the line that caused it. No goroutines are spawned, so
  there is no lifecycle to leak.
- **[Threat model alignment]** The threat this fixture family exists against is a committed
  artifact leaking a credential into the repository. It is addressed in three places, none of them
  this slice's code: the absent `env` field (here, structurally), the `stderr_capture` cap (#1702's
  writer, proven by #1700), and the offline ban entry that keeps this file from ever reading the
  process environment (here, enforced by AST). Nothing in the mobile protocol's security model
  applies — no network surface is touched.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-22
