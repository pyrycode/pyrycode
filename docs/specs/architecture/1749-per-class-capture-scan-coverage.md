# #1749 — prove the capture credential scan refuses one planted record per armed class

**Size:** s (re-counted against this spec in § Size re-check — test-only, one file, no production change)
**Blocker:** #1748, merged at `7f5bef9`; this slice is the per-class sweep its header defers to.

## Files to read first

Read these before writing anything. Every fact this spec asserts was re-measured against them at `2c9dddc`.

- `internal/e2e/realclaude/initialize_control_writer_test.go` → `TestInitControlFixture_ScanRefusesAPlantedCredential` — **the exemplar.** Its doc comment states the resolution this ticket inherits (marshal the record, call `scan` directly, assert on `hits`), its two vacuity controls, and the "ONE planted class here, deliberately" hand-off to this ticket. Copy its shape, not its scope.
- `internal/e2e/realclaude/initialize_control_writer_test.go` → `initControlPlantedPath` — the existing `/Users/…` plant. **This ticket reuses it verbatim as the `users-path-prefix` row's value**; do not mint a second one.
- `internal/e2e/realclaude/initialize_control_writer_test.go` → `scanInitControlFixture` — the writer step whose refusal this table stands in for. Read its doc comment for the two rules that bind every line added to this file: never print a needle value, and **never format a `dropcapScanner`**.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `newDropcapScanner` — the constructor this table's builder mirrors **minus its ambient reads**. It is the definition of "the classes the scanner arms", and it is banned by name in the file this table lands in.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapFixedNeedles` — the five fixed classes and their literals, appended wholesale by the builder.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `addDynamic`, `addDynamicPath`, `dropcapPathSpellings` — how a dynamic class is armed, and how one path becomes several needles (spelling + slug + long segments).
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `scan`, `applied`, `dropcapMinNeedle` — the two return values this table asserts over, and the 16-byte minimum that decides whether a row is real or vacuous.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapContains` — the membership helper AC 1 asserts with.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `TestDropcapRedactionAndDenyScan`, subtest *"the deny-scan sees a slug-mangled path a slash-bearing needle cannot"* — the live precedent for arming a path class on a bare `dropcapScanner{}` from a synthetic value. Two lines, and it is the whole builder pattern.
- `internal/e2e/realclaude/initialize_control_record_test.go` → `initControlFullRecord` — the base record every row plants into. Note it is **path-free**, which is what makes the clean-record control reachable rather than red on arrival.
- `internal/e2e/realclaude/initialize_control_redaction_test.go` → `initControlTempHomeValue`, `initControlOperatorHomeValue`, `initControlWorkdirValue`, `initControlTempDirValue` — the family's existing synthetic path constants and their byte lengths. Three of the four are usable as-is; `initControlTempDirValue` is 14 bytes and must not be used as a needle.
- `internal/e2e/realclaude/initialize_control_probe_test.go` → `TestInitControlScanApplied_RecordsAnArmedNothingClassForAnAbsentPath` — the nearest prior art for asserting over `applied()`, including the present-but-false vs absent-entirely split into two messages. AC 2 follows it.
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans`, entry `"initialize_control_writer_test.go"` — the shipped enforcement this table's file placement rests on. Read the comment block above it; #1748 added `newDropcapScanner` and `realHome` there for exactly this reason.
- `docs/knowledge/features/` — nothing in this area is a deliverable here. The documentation phase owns those files.

## Context

#1748 put a deny-scan on the `initialize` capture's write path: `scanInitControlFixture` marshals the record, scans the bytes, and refuses the write when any armed class hits. It shipped with **one** planted row behind it — a `/Users/…` path in `stderr_capture` — and its own header says so out loud, naming this ticket as the sweep.

`newDropcapScanner` arms **eleven** classes. A net proved against one of them is a claim about the other ten. This slice turns the claim into a table: one row per class, each planting a value of that class and asserting the class is reported.

The work is test-only, additive to one file, and needs no claude binary and no credentials.

**No ADR.** This adds coverage to an existing mechanism and introduces no decision. Nothing here warrants a `docs/knowledge/decisions/` entry.

## Design

### Where it lives, and why that is not tidiness

The table goes in **`internal/e2e/realclaude/initialize_control_writer_test.go`**.

`finOfflineExecBans` already carries an entry for that filename banning `newDropcapScanner`, `realHome`, `os.Getenv`, `os.Environ` and `os.LookupEnv`, and `TestFinOfflineExecBans` parses the AST rather than grepping, so it cannot be answered out of a comment. In that file the rule this ticket's AC 4 states — *build the scanner from synthetic values, never from the operator's environment* — is enforced by shipped code. The same table placed in `dropped_line_capture_test.go`, which is not in the ban table, would rest on the developer remembering it.

**No ban-table change is needed.** The five names that matter are already banned there, and every identifier this table adds — `dropcapScanner`, `addDynamic`, `addDynamicPath`, `dropcapFixedNeedles`, `dropcapContains`, `initControlFullRecord`, `json.MarshalIndent`, the `dropcapClass*` and `dropcapDeny*` constants — is either already used in that file or carries no ambient read. Do not extend `finOfflineExecBans`; there is no new hazard to fence.

### The offline scanner

One builder, mirroring `newDropcapScanner` with its two `os.Getenv` reads and its `realHome` read replaced by synthetic constants:

```go
// newInitControlOfflineScanner mirrors newDropcapScanner's eleven classes from
// synthetic values only. Read-only after construction, so one instance is shared
// by every row.
func newInitControlOfflineScanner() dropcapScanner
```

Behaviour: `addDynamic` for the two credential classes, `addDynamicPath` for the four path classes, then `append(s.needles, dropcapFixedNeedles()...)` — the same five calls and the same order as the constructor it mirrors. It reads nothing from the environment, the filesystem or the clock.

Sharing one instance across parallel subtests is safe and is the file's own established reasoning: `dropcapScanner` is read-only after construction, which is why `TestInitControlFixture_DistinguishesAnArmedNothingScanFromAnAbsentOne` builds its scanner once above a closure and captures it.

### The values

Three new constants; the other three dynamic values already exist.

| Class | Value | Source |
|---|---|---|
| `CLAUDE_CODE_OAUTH_TOKEN` | `synthetic-oauth-token-not-a-real-credential` (43 B) | **new** — `initControlSyntheticOAuthToken` |
| `ANTHROPIC_API_KEY` | `synthetic-api-key-not-a-real-credential` (39 B) | **new** — `initControlSyntheticAPIKey` |
| `dropcapClassTempHome` | `/synthetic/tmp/home` (19 B) | existing `initControlTempHomeValue` |
| `dropcapClassOperatorHome` | `/synthetic/operator/home` (24 B) | existing `initControlOperatorHomeValue` |
| `dropcapClassArtifactDir` | `/synthetic/tmp/artifacts` (24 B) | **new** — `initControlSyntheticArtifactDir` |
| `dropcapClassWorkdir` | `/synthetic/tmp/home/work` (24 B) | existing `initControlWorkdirValue` |

Every constraint below is load-bearing, not taste. Put them in the constants' doc comment:

- **≥ `dropcapMinNeedle` (16) each.** A shorter dynamic needle is skipped, its class reads as not applied, and its row is green-and-vacuous. `initControlTempDirValue` (`/synthetic/tmp`, 14 B) is the family constant that fails this and must not be used.
- **Neither credential value starts with `sk-ant-`**, so each credential row isolates its own class from the fixed `anthropic-key-prefix` class.
- **Neither credential value is a substring of the other**, or both credential rows would hit both classes.
- **Neither credential value is a substring of `"CLAUDE_CODE_OAUTH_TOKEN"` or `"ANTHROPIC_API_KEY"`.** Those two class names are the only armed class names long enough (23 B and 17 B) for a ≥16-byte needle to hide inside — the hazard `TestDropcapDenyClassNamesDoNotCarryTheirNeedle` guards for the five fixed names and does not cover for the dynamic six. It would bite the moment a record carried the eleven-class arming census. `initControlFullRecord`'s own `credential_scan_applied` map carries `workdir` (7 B) and `artifact_dir` (12 B), both under the minimum, so it is safe today; the constraint is what keeps it safe if that map ever grows.
- **The artifact-dir value is not nested under the temp-home value.** `/synthetic/tmp/artifacts` shares only `/synthetic/tmp` — which is not an armed needle — so the artifact row hits exactly one class.
- **No value contributes a segment needle.** `addDynamicPath` also arms every `/`- or `-`-separated segment of length ≥16; the longest segment across all four paths is `synthetic` (9), so today none does. A newly invented value with a long segment would change the measured hit sets below.

### The table and the runner

```go
// one row per armed class; scanner and suffix are shared
rows := []struct{ class, plant string }{ /* eleven entries */ }
```

Per row, inside `t.Run(row.class, …)` with `t.Parallel()`:

1. `rec := initControlFullRecord()`
2. `rec.StderrCapture = row.plant + <the shared suffix>`
3. `json.MarshalIndent(rec, "", "  ")`
4. `hits, _ := scanner.scan(data)`
5. assert `dropcapContains(hits, row.class)`

`stderr_capture` is the carrier for the same reason #1748 chose it: child stderr is where an unpredicted operator path actually shows up. One plant site per row is enough and no per-field table is wanted — **`scan` reads the whole marshalled blob, so field coverage is structural, not selective.** That is exactly the property `redactInitControlRecord` does *not* have (it visits by name, which is why the redaction family does need per-field rows), and the distinction is worth one sentence in the test's doc comment.

**The suffix must not begin with `/`.** Appending a path segment synthesises needles the planted value alone does not carry: `initControlTempHomeValue + "/x"` is `/synthetic/tmp/home/x`, which contains the fixed `/home/`. Use a non-slash separator — #1748's `": child stderr the redaction table did not predict"` is the shape, and the measurements below were taken with it.

**Assert containment, never equality.** Two rows legitimately hit more than one class, measured, not predicted:

| Row | Measured hits |
|---|---|
| `workdir` | `home-path-prefix`, `temp_home`, `workdir` |
| `private-var-folders-prefix` | `private-var-folders-prefix`, `var-folders-prefix` |
| every other row | its own class alone |

`/synthetic/tmp/home/work` contains `/synthetic/tmp/home` **and** the fixed `/home/`; `/private/var/folders/…` contains `/var/folders/`. An equality assertion is red on arrival for those two rows. Containment still leaves each row red when its own class's needle is dropped, which is the property the table is for. Record the measured sets in a comment on those two rows so nobody later "fixes" them.

### AC 2 — the arming check

Once, over the shared scanner, before the rows (the scanner is one instance, so one check is a per-row fact):

- `applied := scanner.applied()`
- `len(applied) != len(rows)` → `t.Fatalf`. Set-size equality: combined with the per-row presence check below, it makes the table's claimed eleven and the builder's armed eleven the same set. Sole red for a twelfth class added to the builder with no row behind it.
- per row: `armed, ok := applied[row.class]`
  - `!ok` → `t.Fatalf`, **its own message**. A class can be *absent* rather than false: `addDynamicPath("")` goes through `dropcapPathSpellings`, which returns nil for the empty string, so no needle is appended and the class never reaches the map at all. This is the arm `TestInitControlScanApplied_RecordsAnArmedNothingClassForAnAbsentPath` exists for, and the one AC 2 would miss if it only asked "did anything come back skipped".
  - `!armed` → `t.Errorf`, **a second message**. The class was armed only by needles under `dropcapMinNeedle`, so its row is green-and-vacuous.

Two defects, two messages: a single combined assertion cannot say which happened.

**`scan`'s second return is deliberately discarded here, unlike in #1748's row.** `notApplied` reports the short-needle case only; `applied()` reports that case *and* the absent-class case. With the block above in place, an assertion on `notApplied` would be red only when the `!armed` check is already red — dead weight, not a second net. Say so in the comment so the divergence from the exemplar reads as a decision.

### AC 3 — the clean-record control

Marshal an unmodified `initControlFullRecord()`, scan it with the same scanner, require **zero** hits. Measured: zero hits, zero not-applied.

This is load-bearing, not tidiness. Without it, a scanner that reported every class on every input would pass all eleven planted rows. It is also the per-row vacuity control for the whole table at once: zero hits over the clean record is exactly the statement that no row's plant was already present in the fixture, proved for all eleven classes in one assertion rather than eleven.

Keep it `t.Fatalf` rather than `t.Skip` — a property that cannot discriminate is a broken instrument, not a passing test — and have the message say what it invalidates: the planted assertions below cannot tell "the plant was seen" from "this record always hits".

### Failure-message discipline

The file's rules bind every line added:

- **Never format the scanner, a needle, or the needle slice** — no `%v`, `%+v`, `%#v`, `%q` on any of them. This table's scanner holds synthetic values only, but `newDropcapScanner`'s holds two live credentials, and the rule is the guard for the whole file. `scanInitControlFixture`'s doc states it.
- Messages name **class constants and the `hits` / `applied` values**. Class names are declared vocabulary and `applied` is a map of bools — both safe, and both the diagnostic worth having. `TestInitControlScanApplied_RecordsAnArmedNothingClassForAnAbsentPath` prints the map for the same reason.
- The planted values are synthetic constants, so printing one would not leak — but do not, so the live callers inherit no pattern.

### What this table does not prove

State these in the test's doc comment so nobody credits it with more:

1. **That `scanInitControlFixture` calls `scan`.** Construction, and the one untestable link — `t.Fatalf` takes the calling subtest down with it, `testing.TB` cannot be implemented outside `testing`, and there is no fake in this package. Same resolution #1748 reached and stated.
2. **That the builder arms the same class set as `newDropcapScanner`.** The four path classes come from shared `dropcapClass*` constants and the five fixed classes come from `dropcapFixedNeedles()` wholesale, so those nine cannot drift. The two credential class names are spelled as string literals in both places — that is the entire drift surface, and nothing catches it. Closing it would mean editing `dropped_line_capture_test.go` to introduce shared constants, which is a second file and a change to shipped code; see § Open questions.
3. **The slug spellings.** Every row plants a raw value, so the slug half of `addDynamicPath` is never exercised here. It is already green in `TestDropcapRedactionAndDenyScan`'s slug-mangled subtest.
4. **Anything about the committed `testdata/initialize_control_v2.1.239.json`.** Do **not** add a row that re-scans it. Re-measured at `2c9dddc`: it still carries `/Users/` once, `/private/var/folders/` once and `/var/folders/` twice (one being the tail of the `/private/` occurrence), so such a row is red on arrival. #1733's redaction takes effect on the next live capture, which replaces the file.

## Concurrency model

None beyond `testing`'s own. One `dropcapScanner`, constructed once on the test goroutine and read-only thereafter, shared by eleven parallel subtests. No goroutines are spawned, nothing is written to disk, no process is executed.

`t.Fatalf` requires the test goroutine — the AC 2 and AC 3 blocks run inline in the parent before the subtests are launched, and each row's assertions run on its own subtest goroutine. Nothing here is called from a non-test goroutine.

## Error handling

Test-only; no production error paths change.

- `json.MarshalIndent` failure → `t.Fatalf` naming the row's class and the error. It cannot fail for this record (`TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry` marshals the same one), but an unchecked error would be a staticcheck finding and a silent zero-byte scan.
- Every assertion that would make a later assertion meaningless (`len(applied)` mismatch, an absent class, a dirty clean record) is `t.Fatalf`. Assertions whose failure leaves the rest of the table informative are `t.Errorf`.

## Testing strategy

The package is behind the `e2e_realclaude` build tag, so `make check` never compiles it, and the suite exits 0 both on a build failure and on a full credentials skip. **Read the count of tests that executed, never the exit code:**

```
go test -tags e2e_realclaude -run 'TestInitControlFixture_ScanRefusesAPlantedValueOfEveryArmedClass' -v ./internal/e2e/realclaude/
```

Expected: **12 `=== RUN` lines** (the parent plus eleven subtests), 12 `--- PASS`, **zero skips**, on a machine with no claude binary and no credentials in the environment. A skip anywhere in that output is a defect, not a configuration difference — nothing in this table may consult the environment.

Also run `go vet` over the tagged package, and `make check` to confirm nothing else moved. Do not run the full live suite for this ticket; it spends tokens and proves nothing this table needs.

### Measured, at `2c9dddc` on 2026-08-25, offline, no credentials

The design above was executed against the real `scan`, `applied`, `addDynamicPath` and `dropcapFixedNeedles` before this spec was written. Results:

- 15 needles, **11 classes**, `applied()` = 11 entries, all `true`, none skipped.
- Clean `initControlFullRecord()`: `hits=[]`, `notApplied=[]`.
- All eleven rows contain their own class. Multi-hit rows exactly as tabled above.

If the developer's first run disagrees with any of these, the disagreement is the finding — re-measure before changing the design.

## Open questions

- **The two credential class names are hand-spelled in two files.** `newDropcapScanner` writes `"CLAUDE_CODE_OAUTH_TOKEN"` and `"ANTHROPIC_API_KEY"` as literals, and this table must match them exactly. Promoting them to `dropcapClass*`-style constants would close the drift, but it edits `dropped_line_capture_test.go` — a second file and a change to shipped code, outside this ticket's "purely additive to one file" shape. Leave it; if a follow-up is wanted, it is a two-line XS.
- **`initControlSyntheticArtifactDir` lives in `initialize_control_writer_test.go`**, beside `initControlPlantedPath`, rather than with the family's other synthetic path constants in `initialize_control_redaction_test.go`. That keeps the ticket to one file. If a later ticket needs the artifact-dir value elsewhere, moving it is a mechanical rename.
- **`stderrFixtureCap` does not interact with this table.** Every row scans the record it marshals itself; `capFixtureCapture` runs inside `scanInitControlFixture`, which no row calls. Keep the plants short anyway, so a future row that does route through the writer inherits no over-cap habit.

## Size re-check

Re-counted against this spec, per the six boundaries:

| Limit | Boundary | This spec |
|---|---|---|
| Production source files created/modified | ≤ 3 | **0** — test-only; the sole file is `initialize_control_writer_test.go` |
| Total written work (production + tests + helpers + per-branch logs + spec edits) | ≤ 400 | **~230** — 3 constants, 1 builder (~10 code), 11 rows, 1 runner, 2 control blocks, house-style doc comments |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — nothing calls the new code |
| Acceptance criteria | ≤ 5 | **4** |
| Distinct error/reject branches in a state machine | ≤ 10 | **n/a** — no state machine |

No boundary exceeded. Ships as one `size:s` ticket.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The design crosses no trust boundary: every value it handles is a compile-time constant declared in the test file, and the one function it exercises (`scan`) is a pure `bytes.Contains` over bytes the test marshalled itself. The boundary this table *describes* — the deny-scan ahead of `scanInitControlFixture`'s first filesystem call — is unchanged by this ticket; the table observes it, it does not move it.
- **[Tokens, secrets, credentials]** MUST-FIX-if-violated constraints, addressed in the design rather than left to care. The hazard is real and inverted from the usual one: the *scanner* is the credential-bearing object in this package, because `newDropcapScanner` stores the operator's live `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` as needle values. Three things keep this ticket clear of it, and all three are structural rather than remembered: (a) the table never calls `newDropcapScanner` — the name is banned in this file by `finOfflineExecBans`, AST-checked, as are `os.Getenv`, `os.Environ`, `os.LookupEnv` and `realHome`; (b) the builder's needle values are declared constants, so no credential can enter the scanner at all; (c) the "never format the scanner, a needle or the needle slice" rule from `scanInitControlFixture`'s doc is restated in § Failure-message discipline and applies to every message this table adds. Generation, rotation, revocation and expiry are not applicable — nothing here mints or stores a token.
- **[Tokens, secrets, credentials — second-order]** No findings, by construction. The synthetic credential values must not be substrings of `"CLAUDE_CODE_OAUTH_TOKEN"` or `"ANTHROPIC_API_KEY"`. That is not cosmetic: those two class names are the only armed class names at or above `dropcapMinNeedle`, so a needle hiding inside one would make the net hit its own metadata on every run — the failure `TestDropcapDenyClassNamesDoNotCarryTheirNeedle` exists for, measured on the sibling family's first live capture. The guard covers the five fixed names only; the six dynamic ones are outside its scope, and four of them (`temp_home`, `operator_home`, `artifact_dir`, `workdir`) are structurally safe because they are shorter than the minimum. The constraint in § The values is what covers the remaining two.
- **[File operations]** No findings. This ticket performs no filesystem operation in either direction: no `os.WriteFile`, no `os.ReadFile`, no `t.TempDir()`, no path is constructed from any input. `dropcapPathSpellings` calls `filepath.EvalSymlinks` on the synthetic paths, which errors because they do not exist — the sole reason the spelling set is deterministic offline. Every path in the design is a constant under a `/synthetic/…` root that is never created, so traversal, TOCTOU, mode and symlink-following have no surface here.
- **[Subprocess / external command execution]** No findings. No process is executed and no environment is read. The file's `finOfflineExecBans` entry bans `resolveClaudeBin`, `WithWorktreeAuthenticated`, `WithWorktree`, `probeClaudeVersion` and `captureClaudeVersion` precisely so no addition here can reintroduce one, and the design adds no name that reaches any of them.
- **[Cryptographic primitives]** Not applicable, and the design decision that makes it so is stated: the synthetic credential values are fixed literals rather than generated, because a random needle would make the measured hit sets non-reproducible and could collide with the fixture's own vocabulary. Nothing in this table is a security-relevant random value, so neither `crypto/rand` nor `math/rand` appears.
- **[Network & I/O]** Not applicable. No socket, no reader, no unbounded input. The one size bound in this area, `stderrFixtureCap`, is not on this path — `capFixtureCapture` runs inside `scanInitControlFixture`, which no row calls — and § Open questions says so rather than leaving a reader to assume the cap protects these rows.
- **[Error messages, logs, telemetry]** No findings, and this is the category most likely to regress. The refusal this table stands behind is deliberately narrow: on a hit, the count and the class names, nothing else — no excerpt, no record dump, no needle value, no byte offset, because a token in a run log this pipeline salvages is exactly the exposure the scan exists to prevent. The new messages inherit that shape: § Failure-message discipline binds them to class constants plus the `hits` and `applied` values, and forbids printing the scanner, a needle or a planted value even though every planted value here is synthetic — so the live callers inherit no pattern worth copying. `initControlScrubbed` does not cover for a slip: it reads the *child's* stderr, and these would be the harness's own output.
- **[Concurrency]** No findings. One `dropcapScanner`, built on the test goroutine and read-only afterwards — the same property `TestInitControlFixture_DistinguishesAnArmedNothingScanFromAnAbsentOne` already relies on — shared by eleven parallel subtests with no writer. No goroutine is spawned, so none can leak; no lock is taken, so there is no ordering to document; `t.Fatalf` is called only from test goroutines, per `scanInitControlFixture`'s standing rule.
- **[Threat model alignment]** The relevant threat is this family's own and it is named rather than deferred: a committed capture artifact, destined for a public issue, carrying an operator's credential or home path. #1733 is the redaction table, #1748 is the fail-closed scan ahead of the write, and this ticket is the proof that the scan's coverage is eleven classes rather than one. Out of scope, and named: the committed `testdata/initialize_control_v2.1.239.json` still carries three fixed classes and is cleaned by the next live capture (#1688's run), not by this table — which is why § What this table does not prove forbids a row that re-scans it.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-25
