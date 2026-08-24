# #1748 — refuse an `initialize` capture record that carries a credential or an operator path

Test-only. Three files, all `_test.go`, all in `internal/e2e/realclaude`. No production
file changes, no new record field, no new exported symbol.

## Files to read first

Everything below is named by symbol. Resolve each with `codegraph_search` / `codegraph_node`
and read the declaration — this package's doc comments carry most of the design, and several
of them move rather than get rewritten.

| Where | Symbol | What to extract |
|---|---|---|
| `internal/e2e/realclaude/initialize_control_writer_test.go` | `writeInitControlFixture` | The function this slice re-shapes. Today's order is copy → cap → `os.MkdirAll` → mint name → marshal → write `.tmp` → rename. Note which doc paragraphs describe the COPY and the CAP: those move onto the new step, they are not rewritten. |
| same | `TestInitControlFixture_WriterCapsStderrCapture` | The two over-cap rows AC 2's assertion rides in, plus its "# Why two rows, and why neither is dead weight" doc — the sole-red discipline every new claim in this file has to state. Also its no-mutation check and its "# Do not print the capture" rule. |
| same | `TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry` | Offline call site 1, and the exactly-one-entry assertion that already covers the clean path's `.tmp`/stray-file hazard. |
| same | `TestInitControlFixture_RoundTripsAnUnansweredWaitBesideCapturedBytes` | Offline call site 2. |
| same | `TestInitControlFixture_DistinguishesAnEmptyCensusFromAnAbsentOne` | Offline call site 3 — the writer is called from inside a `write := func(rec)` closure, so the scanner is built once above it and captured. |
| same | `TestInitControlFixture_DistinguishesAnArmedNothingScanFromAnAbsentOne` | Offline call site 4 — same closure shape. |
| `internal/e2e/realclaude/dropped_line_capture_test.go` | `dropcapWriteRecord` | **The exemplar. Read it before writing anything.** Scanner as a parameter, marshal → scan → `t.Fatalf` naming the class → only then write; and the doc paragraph on why no excerpt is printed. Its "NOTHING was written" wording is the wording to follow. |
| same | `dropcapScanner`, `scan` | The contract: `scan(b []byte) (hits, notApplied []string)`, value receiver, allocates its own results, safe to share. |
| same | `dropcapFixedNeedles`, `dropcapMinNeedle`, `dropcapNeedle` | The five fixed needles are NOT `dynamic`, so `dropcapMinNeedle` never skips them — that is why the offline construction is armed identically on every machine. |
| same | `dropcapDenyUsers`, `dropcapDenySkAnt`, `dropcapDenyHome`, `dropcapDenyVarF`, `dropcapDenyPVarF` | The class identifiers the refusal names and the planted row asserts on. |
| same | `dropcapContains` | The hits-membership helper the planted row asserts with. |
| same | `newDropcapScanner` | Read it ONLY to confirm what it reads on its own — `os.Getenv` twice and `realHome`. It must never be called from the writer file; see § The parameter, and its cascade. |
| `internal/e2e/realclaude/inband_bypass_revoke_fixture_test.go` | `capFixtureCapture` | The cap that moves inside the new step. Read `stderrFixtureCap` and `truncateString` with it — the bound is a BYTE bound with a rune-boundary trim. |
| `internal/e2e/realclaude/initialize_control_probe_test.go` | `runInitControlChild` | The live call site. Its signature already carries `scanner dropcapScanner` (since #1747), so the change is one argument. Its doc's **"THE SCANNER VALUE IS CREDENTIAL-BEARING: NEVER FORMAT IT"** paragraph binds every line this slice adds. |
| same | `TestRealClaude_InitializeControl_Capture` | Where the live scanner is constructed — `newDropcapScanner(home, "", workdir)`, artifact-dir slot deliberately empty. |
| same | `initControlScanApplied` | Why the record's arming census already exists, so the new step discards `scan`'s `notApplied` return. |
| `internal/e2e/realclaude/initialize_control_record_test.go` | `initControlFullRecord` | The path-free fixture all five offline callers write. Note 7 and note 8 on its doc explain why `Redaction` and `CredentialScanApplied` carry no path. The planted row copies it and mutates one field. |
| `internal/e2e/realclaude/offline_exec_ban_test.go` | `finOfflineExecBans` | The map. Its `"initialize_control_writer_test.go"` entry gains two names; its `"initialize_control_redaction_test.go"` entry (#1732) is the precedent — it added `realHome`/`os.TempDir` for exactly this reason. |
| same | `TestFinOfflineFilesReachNoExecHelper` | The check is an **AST identifier match**, matching a bare `*ast.Ident` as well as a dotted selector — which is why a bare `realHome` reference is caught and why banning `os.Getenv` alone does not close the wrapper. |
| `internal/e2e/realclaude/fixtures.go` | `realHome` | A package-level `var` read at load. Confirms the ban name resolves to a declaration. |
| `docs/knowledge/features/e2e-realclaude.md` | § "A credential guard scoped to the surface named in the design is not the same as a credential guard scoped to the surface the ticket commits" | Why this slice exists, and the re-measurement of 2026-08-24 against the committed fixture. |

## Context

`initControlScrubbed` (#1688) guards the child's stderr. Every byte this family
**commits** is claude's stdout, and no deterministic check has ever run over it —
#1688's clean bill came from two independent human reads of the committed JSON, and
those reads passed `account` and `pid` under `response.response` in the `initialize`
reply. #1732 and #1733 landed the redaction table; a table only rewrites what it
predicted. This slice is the fail-closed net behind it, deliberately different fabric:
a scan of the marshalled bytes, refusing the write outright rather than rewriting.

No ADR. This is one more instrument in a family whose design record already lives in
`docs/knowledge/features/e2e-realclaude.md`, and the decision it encodes — scan the
write path, never the committed corpus — is a paragraph in that overview, not a
cross-cutting choice.

## Design

### The step

One new function in `initialize_control_writer_test.go`:

```go
// scanInitControlFixture returns the exact bytes writeInitControlFixture will put
// on disk, refusing the record outright when the deny-scan hits.
func scanInitControlFixture(t *testing.T, scanner dropcapScanner, rec *initControlFixtureRecord) []byte
```

It copies `rec`, caps the copy's `StderrCapture` with `capFixtureCapture`, marshals with
`json.MarshalIndent`, runs `scanner.scan` over the blob, `t.Fatalf`s on a non-empty
`hits`, and otherwise returns the blob. **It makes no filesystem call of any kind.**

`writeInitControlFixture` becomes:

```go
func writeInitControlFixture(t *testing.T, dir string, scanner dropcapScanner, rec *initControlFixtureRecord) string
```

body order: `scanInitControlFixture` → `os.MkdirAll` → `initControlArmFixtureName` →
`os.WriteFile` of the tmp → `os.Rename`. The bytes written are the returned slice, byte
for byte, with nothing appended — **not** `dropcapWriteRecord`'s `append(blob, '\n')`,
which would break AC 2's identity by one byte.

Parameter position follows the exemplar: `dropcapWriteRecord(t, dir, red, scanner, rec)`
puts the record last, so `scanner` goes before `rec` here too.

`t.Fatalf` requires the test goroutine. Both the step and the writer are called directly from
one — `runInitControlChild`'s stdout reader goroutine calls neither — so `t.Helper()` on the
step plus a direct call is the whole discipline. Do not call either from a goroutine.

**The filename now mints from `rec`, not from the copy.** The copy lives inside the step, so
`initControlArmFixtureName(out.ClaudeVersion, out.Arm)` becomes
`initControlArmFixtureName(rec.ClaudeVersion, rec.Arm)`, and the writer's error messages read
`rec.` for the same two fields. The values are identical because the cap is scoped to
`StderrCapture`, and that equivalence is not left as a claim:
`TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry` computes its `wantName` from
the CALLER's record, so a writer minting from a field the step had modified reddens there.
The corollary is the rule to write into the step's doc — **the step must never modify a field
`initControlArmFixtureName` reads.**

### Why the step returns the bytes rather than writing them

AC 1 and AC 2 are one mechanism read from two ends, and the spec says so rather than
promising AC 1 a row of its own:

- **AC 1 (nothing on disk on a hit)** is discharged **by construction**. `scanInitControlFixture`
  is the sole producer of the bytes and the scan is inside it, so the writer cannot hold a
  blob the scan has not passed; and the step's first filesystem call is the writer's
  `os.MkdirAll`, which is strictly after the step returns. There is no seam to observe the
  empty directory from — a hit is a `t.Fatalf` that takes the calling subtest down — so no
  row is promised for it.
- **AC 2 (disk bytes == scanned bytes)** is that same claim made executable, from the other
  end. See § Testing strategy.

### Where the cap goes, and why it is inside the step

**Inside.** The bytes scanned are then exactly the bytes on disk, so a needle surviving only
in the truncated tail is correctly not reported — the file cannot carry what the scan did not
see, and the scan does not refuse over bytes the file will not carry. The two alternatives are
both wrong in a way the design should name:

- Cap in the CALLER, before the step: mutates the caller's record, which
  `TestInitControlFixture_WriterCapsStderrCapture`'s no-mutation check already reddens.
- Cap in the WRITER, between the scan and the write: the scanned bytes are then longer than
  the disk bytes, and AC 2's identity assertion is that mutant's **sole red** — every length,
  prefix and UTF-8 assertion in the cap test stays green, because what lands on disk is
  correctly capped.

### What this step does NOT do that the exemplar does

`dropcapWriteRecord` additionally scans every base64 payload's decoded bytes, because
`bytes.Contains` cannot see through base64. `initControlFixtureRecord` carries **no
base64-encoded field** — its payload-bearing fields are `json.RawMessage` and plain strings,
all of which the blob scan reads directly. Do not port `dropcapBase64Payloads` or
`dropcapLocateHits`; there is nothing here for either to narrow.

`scan`'s second return, `notApplied`, is discarded with `_`. The arming census is already a
recorded record field — `CredentialScanApplied`, filled at the construction site by
`initControlScanApplied` (#1747) — so logging it again from the writer would duplicate the
fact in a second place that can drift from the first.

### The parameter, and its cascade

The scanner arrives as a **parameter**, the way `dir` already does, and never as a
`newDropcapScanner` call inside the writer file. That file's `finOfflineExecBans` entry bans
`os.Getenv` / `os.Environ` / `os.LookupEnv`, and the check is an AST identifier match — so
calling `newDropcapScanner` there (or naming `realHome`) satisfies the ban's letter while
destroying the offline property it protects.

Six call sites, all one argument:

- Five offline, in `initialize_control_writer_test.go` — one per `TestInitControlFixture_`
  function. Each passes **`dropcapScanner{needles: dropcapFixedNeedles()}`**, the established
  in-package offline construction (`dropped_line_capture_test.go` uses it in the
  "the net is not decorative" and base64 subtests of `TestDropcapRedactAndScan`). Written
  inline at each test function rather than behind a local helper: it is one short expression,
  it is the spelling the sibling family already uses, and inline keeps "this reads no
  environment" visible at the site the ban protects. In the two `write := func(rec)` closure
  tests, build the value once above the closure and capture it — `dropcapScanner` is
  append-only during construction and read-only afterwards, so sharing one value is safe (see
  `runInitControlChild`'s doc, which states exactly this asymmetry against the redactor).
- One live, inside `runInitControlChild`, which already holds a `scanner dropcapScanner`
  parameter since #1747. It passes the one it has.

**The offline callers stay green** because `initControlFullRecord` is path-free: `Argv[0]` is
the literal `"claude"`, `Redaction`'s one entry replaces with `$WORKDIR`,
`CredentialScanApplied`'s keys are class names that
`TestDropcapDenyClassNamesDoNotCarryTheirNeedle` keeps clear of their own needles, and the two
cap rows add only `strings.Repeat("A", …)` and `"A" + strings.Repeat("é", 5000)`. That is a
precondition, not a hope — the planted row's clean control (§ Testing strategy) asserts it, so
if the fixture ever acquires a path value, one row says why instead of five writers fataling.

### The ban entry

`finOfflineExecBans["initialize_control_writer_test.go"]` gains **`newDropcapScanner`** and
**`realHome`** — deterministic fabric behind the parameter, following #1732's
`"initialize_control_redaction_test.go"` entry, which added `realHome`/`os.TempDir` for the
same reason. Both names resolve to a declaration as of `cc1319d`, re-verified against this
worktree while writing this spec: `newDropcapScanner` is a func in
`dropped_line_capture_test.go`, `realHome` is a package-level `var` in `fixtures.go`. **Re-check
both before committing** — a misspelled ban name is the one defect that check cannot report.

`os.TempDir` is deliberately NOT added: `newDropcapScanner` takes `tempHome`, `artifactDir` and
`workdir` as parameters and reads only `realHome` and the environment on its own, so `os.TempDir`
is not a route to anything here. Adding it would be a name with no hazard behind it.

### What this slice must not do

- **No new record field.** The refusal is a fatal, not a recorded fact.
  `TestInitControlFixtureRecord_*`'s `len(rows) == reflect.TypeOf(initControlFixtureRecord{}).NumField()`
  assertion turns a new field into a cascade through the listing, `initControlFullRecord` and the
  record test — that cascade was #1747's whole budget and it is not in this one's.
- **Do NOT re-scan the committed fixture.** `testdata/initialize_control_v2.1.239.json` still
  carries three of the five fixed deny classes (re-measured 2026-08-24 at `cc1319d`: `/Users/` ×1,
  `/private/var/folders/` ×1, `/var/folders/` ×2, one of those two being the tail of the
  `/private/` occurrence), so an offline test that re-scans it is **red on arrival**. The sibling
  family's scanner exists partly so offline validation can re-scan a committed capture forever,
  which makes copying that shape here the obvious wrong move. #1733's redaction takes effect on
  the next live capture, which replaces the file. **The scan this slice ships is on the WRITE
  path only.**
- **No fataler interface.** `testing.TB` cannot be implemented outside `testing`, there is no fake
  in this package, and building one is out of this slice's budget.
- **No per-class sweep.** One planted row here, so the refusal ships with something red behind it.
  The per-class sweep is #1749, already wired blocked-by this ticket.

## Concurrency model

No goroutines. The step is a pure function of `(scanner, rec)` up to `t.Fatalf`; `dropcapScanner`
is read-only after construction and its `scan` is a value receiver allocating its own results, so
the shared offline scanner in the two closure-shaped tests is safe under `t.Parallel()`. The
existing per-row `t.TempDir()` discipline is unchanged and still load-bearing: both cap rows and
both census rows mint the same filename from the same `ClaudeVersion`/`Arm`.

## Error handling

The writer's reject branches, unchanged in shape and count except for the one added:

| Branch | Behaviour |
|---|---|
| marshal fails (inside the step) | `t.Fatalf` naming `claude_version`, `arm`, the error |
| **deny-scan hits (inside the step)** | **`t.Fatalf` naming the count and the CLASS names, and stating that nothing was written** |
| `os.MkdirAll` fails | `t.Fatalf`, unchanged |
| tmp write fails | `t.Fatalf`, unchanged |
| rename fails | `t.Fatalf`, unchanged |

The refusal message follows `dropcapWriteRecord`'s, and **AC 4 is discharged by construction, not
by a row** — there is no seam from which a test could read what the fatal printed. It carries:
the number of classes that hit, the class names (declared vocabulary — the `dropcapDeny*` constants
and `dropcapClass*` identifiers), `claude_version` and `arm` (already in the filename, neither is
child output), the sentence that **nothing was written**, and the operator's remedy: extend the
`newInitControlRedactor` table with the named class and re-run one live capture. It carries **no
excerpt, no record dump, no needle value and no byte offset** — "let me include the payload to
help debug it" is exactly how a token reaches a salvaged run log and inverts the whole control,
and an offset localises the hit inside the record, which is `dropcapLocateHits`' job in the
sibling and deliberately nobody's here.

That "no offset" rule is scoped to the **refusal**. AC 2's mismatch message may report the
offset of the first differing byte (§ Testing strategy), because both blobs it compares have
already passed the scan — an offset between two scan-clean blobs localises nothing that is not
already publishable. The two messages are not the same rule.

Two existing rules extend over the new code and must be restated at the new site rather than assumed:

- **Never format the scanner.** A `dropcapScanner` in scope is two live credentials in a struct
  (`newDropcapScanner` stores `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` as needles). No
  `%v`, `%+v`, `%#v` or `%q` on the scanner, a needle, or the needle slice — in the step, in the
  writer, or in any new row. `initControlScrubbed` does not catch it: that guard reads the CHILD's
  stderr, and this would be the harness's own output.
- **Never `%+v` the record.** It moves up to `stderrFixtureCap` bytes of child output out of the
  bounded file and into an unbounded run log.

**Expected live consequence, and it is the feature.** After this lands, a live
`TestRealClaude_InitializeControl_Capture` whose record still carries an unredacted operator path
or a token **aborts with no fixture written**, where it previously wrote one. A reviewer seeing
that has found a gap in #1732's table, not a bug in this slice.

## Testing strategy

Offline and deterministic. Every row must **PASS — not SKIP** — with no claude and no credentials:

```
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestInitControlFixture_|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/
```

The package is behind the `e2e_realclaude` build tag, so `make check` never compiles it and the
suite exits 0 both on a build failure and on a full credentials skip. **Read the count of tests
that executed, never the exit code.** The `TestInitControlFixture_` prefix on the new row is
load-bearing: the file header documents that `-run` filter.

Two rows total. Neither is a full-function body below — write them in this file's idiom.

### AC 2 — the bytes on disk are the bytes that were scanned

Rides **inside `TestInitControlFixture_WriterCapsStderrCapture`'s loop body**, not in a new test
function. The criterion is **vacuous under-cap** — `json.MarshalIndent` is deterministic, so over a
record the cap does not shorten, re-marshalling the caller's record is byte-identical and the row
separates nothing. It discriminates only over an over-cap record, and that test already carries the
only two.

- After reading the file back, obtain the scanned bytes by calling `scanInitControlFixture` with
  the same offline scanner and the same `rec` — legitimate because the writer does not mutate `rec`
  (the no-mutation check two lines above says so) and the step makes no filesystem call.
- Assert the on-disk bytes and the returned bytes are `bytes.Equal`.
- **Report lengths and the offset of the first difference. Never the blobs** — they are ~8 KB of
  capture, and this test's "# Do not print the capture" rule is why.

Sole red for two distinct mis-implementations, both of which leave every existing assertion in that
test green:

- A writer that produced the disk bytes by a route other than the returned slice in a way that still
  decodes — `json.Marshal` instead of `json.MarshalIndent`, or `append(blob, '\n')`.
- **The cap moved after the scan.** Scanned bytes are then the uncapped marshal and disk bytes are
  capped; length, prefix and UTF-8 assertions all stay green because the file is correctly bounded.

### AC 3 — a planted value of an armed class is refused, naming the class

A new `TestInitControlFixture_ScanRefusesAPlantedCredential`, `t.Parallel()`.

It exercises the **scan over the marshalled record**, never `writeInitControlFixture`'s fatal —
the sibling family's resolution for the same shape (`dropcapWriteRecord`'s net is proved by
`TestDropcapRedactAndScan`'s "the net is not decorative" subtest, which marshals its own blob and
calls `scan`). A `t.Fatalf` from the writer takes the calling subtest down with it, so there is no
way to assert on a refusal from inside one.

Scenarios, as bullets:

- **The clean control, first, and it must `Fatalf` rather than skip.** `dropcapScanner{needles:
  dropcapFixedNeedles()}` over the marshalled `initControlFullRecord()` reports **zero** hits.
  Without it the planted assertion cannot tell "the plant was seen" from "this fixture always hits",
  and a row that cannot discriminate is a broken instrument. It is also the one row that says out
  loud why the five offline `writeInitControlFixture` callers do not fatal.
- **`notApplied` is empty.** The five fixed needles are not `dynamic`, so `dropcapMinNeedle` never
  skips them. If a future edit marked one `dynamic`, its class would be skipped, the planted
  assertion below would go green-and-vacuous, and nothing else would notice.
- **The plant.** A fresh `initControlFullRecord()` with `StderrCapture` set to a synthetic
  `/Users/`-prefixed path — `StderrCapture` because it is the realistic carrier (child stderr is
  where a path shows up; `initialize_control_redaction_test.go`'s own fixture plants a shim-log path
  there) and because it puts this row in the same field as AC 2's. Marshal with
  `json.MarshalIndent`, scan, and assert `dropcapContains(hits, dropcapDenyUsers)`.
  - Plant a **fixed** class. A dynamic needle shorter than `dropcapMinNeedle` (16) arms nothing, so
    a row planting one is green and proves nothing: `initControlTempDirValue` (`/synthetic/tmp`) is
    14 bytes and would be skipped. `initControlTempHomeValue` (19) and
    `initControlOperatorHomeValue` (24) are long enough but are not armed on the offline
    fixed-only scanner at all.
  - Put the planted value at the FRONT of the capture, well inside `stderrFixtureCap`, so the cap
    cannot move it into a truncated tail.
- The failure message names the class constants and the hits slice. It does **not** print the
  planted needle, the blob, or the record.

**What this row does not prove, stated so nobody credits it with more:** it does not prove that
`scanInitControlFixture` calls `scan` — that is construction (the step is the sole producer of the
write's bytes and the scan is inside it), and the only untestable link in the chain. What it proves
is that a fixed-class value planted in this record's marshalled form IS reported, by name, by the
scanner the five offline callers pass.

### Regression surface

- `TestFinOfflineFilesReachNoExecHelper/initialize_control_writer_test.go` must stay green with the
  two new ban names. It is red immediately if anyone reaches for `newDropcapScanner` in that file.
- The four other `TestInitControlFixture_` rows and `TestRealClaude_InitializeControl_Capture`'s
  compile are the call-site cascade; nothing about their assertions changes.

## Open questions

- **Should the live capture's refusal be a `t.Fatalf` or a `t.Errorf` + skip-the-write?** Specified
  as `Fatalf`, following `dropcapWriteRecord`. The consequence is that a live run with a leaked value
  produces no artifact at all, not even a redacted-but-incomplete one. That is the correct trade for a
  public repo and matches the sibling; if operating experience says otherwise, it is a one-line change
  in the step.
- **The committed `testdata/initialize_control_v2.1.239.json` stays as it is.** It carries three fixed
  classes today and is replaced by the next live capture. Whether to delete it in the meantime is
  #1749's or a follow-up's call, not this slice's.

## Size

Against the size-`s` boundary, re-counted against this written spec:

| Limit | Boundary | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **0** — all three files are `_test.go` |
| Total written work | ≤ 400 | ~300 (nearest analogues: #1731 at 238, #1733 at 421, #1747 at 446; this slice is narrower than #1747 — no record-file change, one new test function rather than two) |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **6** |
| Acceptance criteria | ≤ 5 | **4** |
| Distinct error/reject branches | ≤ 10 | **5** |

Ships as one `size:s` ticket.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — the boundary is a single named function, and the one
  child-derived value that escapes the blob is covered by ordering.** claude's stdout crosses
  into committed state through `scanInitControlFixture`, which is the sole producer of the
  write's bytes with the scan inside it. The one byte-stream that reaches disk WITHOUT being
  in the blob is the **filename**, minted by `initControlArmFixtureName` from `ClaudeVersion`
  and `Arm` — both child-derived (`captureClaudeVersion` reads `claude --version`). It is
  covered anyway: both are record fields, so they are inside the scanned blob, and the step
  runs before the name is minted. A hit in either refuses before a path exists. `dir` is not
  child-derived (`t.TempDir()` offline, `filepath.Join(packageDir(t), "testdata")` live).

- **[Tokens, secrets, credentials] No findings, but two rules must be restated at the new
  site rather than inherited silently.** No token is generated, stored or rotated here. A
  `dropcapScanner` in scope IS two live credentials in a struct — `newDropcapScanner` stores
  `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` as needle values — so the never-format
  rule stated at `runInitControlChild` now binds the step, the writer and both new rows. The
  offline callers hold no credential at all: `dropcapScanner{needles: dropcapFixedNeedles()}`
  reads no environment, which is the whole reason the scanner is a parameter and the reason
  `finOfflineExecBans` gains `newDropcapScanner` and `realHome`.

- **[File operations] No findings. Two sub-questions answered rather than waved past.**
  *Traversal:* the name is minted, never interpolated; containment stays lexical and belongs
  to `initControlArmFixtureName`, unchanged by this slice. The refactor's one hazard — minting
  from `rec` while writing a modified copy — is caught by
  `TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry`, which derives its expected
  name from the caller's record. *File mode:* `0644` is unchanged and deliberately not
  narrowed to `dropcapWriteRecord`'s `0600`. For an armed class the mode is moot — no file
  exists at all, because the scan precedes `os.MkdirAll`; for an unarmed class the file is
  committed to a public repo either way, so `0600` would buy nothing and would diverge from
  every other fixture in `testdata/`. *Atomicity:* tmp-plus-rename is preserved, and a hit now
  strands not even a `.tmp`, which is strictly better than the pre-change behaviour the
  exactly-one-entry assertion could only cover on the clean path.

- **[File operations] OUT OF SCOPE — `os.WriteFile` on the `.tmp` path uses no `O_EXCL` and
  follows a symlink.** Pre-existing and unchanged by this slice. Both target directories are
  operator-controlled (a `t.TempDir()`, or the repo's own source tree); an attacker who can
  plant a symlink in `testdata/` already has repo write access, which is a strictly larger
  capability than redirecting one scan-clean fixture. No ticket exists; not worth one.

- **[Subprocess / external command execution] No findings — the change is subtraction.** The
  offline path execs nothing and the new `finOfflineExecBans` entry makes that harder to break,
  not easier: `newDropcapScanner` and `realHome` are additive bans, and the check matches a bare
  `*ast.Ident` as well as a dotted selector, so the wrapper route into `os.Getenv` closes too.
  The live path's `exec.CommandContext` and its environment handling are untouched.

- **[Cryptographic primitives] Not applicable, with the reason.** The scan is `bytes.Contains`
  over literal needles. There is no key, no nonce and no RNG. Constant-time comparison is
  deliberately not used and would be wrong here: the needles are searched **for**, not compared
  against a secret to authorise anything, so timing reveals only what the caller already holds.

- **[Network & I/O] No findings, and the input bound is now structural.** No network. The one
  unbounded record field is `StderrCapture`, and moving `capFixtureCapture` INSIDE the step
  means the scan's input is bounded by `stderrFixtureCap` before `scan` ever runs — the cap is
  a DoS bound on the scan as well as on the file. The remaining unbounded dimension is the
  COUNT of `ControlResponses` / `StdoutEvents` entries, which is pre-existing (the marshal and
  the write already carry it), bounded per line by `setModeScanMax`, and sourced from a binary
  this suite runs deliberately. Out of scope for this slice.

- **[Error messages, logs, telemetry] No findings — and the fatal's POSITION protects the log
  surface the package overview warns about.** The refusal names classes only. Beyond that:
  `runInitControlChild` logs record-derived bytes after the write — the per-response
  `control_response[i] redacted` loop and the summary line carrying `ScannerError` — and those
  are a run-log surface the redaction table alone used to guard. Two things now hold. On a hit,
  `writeInitControlFixture`'s `t.Fatalf` fires **before** those log calls, so a refused record
  never reaches them. On the clean path, every value they print is a record field and therefore
  inside the blob the scan just passed. The one log that PRECEDES the write, `redaction
  applied`, prints `record.Redaction` — class names, replacements and counts, never a value, by
  that field's own construction. AC 3's row prints class constants and the `hits` slice, never
  the planted value, so it does not establish a pattern `runInitControlChild` could inherit.

- **[Concurrency] No findings.** No goroutine is added. The offline scanner shared across
  `t.Parallel()` subtests is safe by inspection, not by assumption: `dropcapScanner`'s mutators
  (`addDynamic`, `addDynamicPath`) are pointer-receiver and run only inside `newDropcapScanner`,
  while `scan` and `applied` are value receivers that read `needles` and allocate every result
  locally. The gate runs `-race` over it. The per-row `t.TempDir()` discipline is unchanged and
  still load-bearing — both cap rows and both census rows mint the same filename.

- **[Threat model alignment] Addressed, with the residue named.** The threat is a credential or
  operator path reaching a public commit, recorded in
  `docs/knowledge/features/e2e-realclaude.md` § "A credential guard scoped to the surface named
  in the design is not the same as a credential guard scoped to the surface the ticket commits".
  This slice closes the write path. Explicitly OUT OF SCOPE and named rather than closed: the
  already-committed `testdata/initialize_control_v2.1.239.json`, which still carries three fixed
  classes and is replaced by the next live capture (§ What this slice must not do); and the
  per-class sweep proving each armed class refuses, which is #1749, already blocked-by this
  ticket.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-24
