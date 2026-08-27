# #1747 — record which credential classes the initialize capture run armed

Test-only. No production file changes. Three files, all under `internal/e2e/realclaude/`.

## Files to read first

Symbols, not lines — resolve each with `codegraph_search` / `codegraph_node`.

- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapScanner`, `newDropcapScanner`, `addDynamic`, `addDynamicPath`, `applied`, `dropcapPathSpellings`, `dropcapMinNeedle` — **the asymmetry this ticket closes.** `addDynamic` appends unconditionally (so an unset credential lands as `false`); `addDynamicPath` appends nothing when `dropcapPathSpellings` returns `nil` for an empty path, so `applied()` carries **no key at all** for that class.
- Same file → `dropcapRecord` (the `CredentialScanApplied` field and its tag) and `TestRealClaude_DroppedLineCapture` (the `CredentialScanApplied: scanner.applied()` line inside its composite literal) — the field to copy and the construction-site assignment to copy. `dropcapWriteRecord` is the counter-example: it assigns in the writer, and this family must not.
- Same file → `TestDropcapRedactionAndDenyScan`, subtest **"a short dynamic needle is skipped and reported, never matched"** — the already-green empty-needle property. **Cite it; do not re-commission it.** It builds its needles by hand and never goes through `newDropcapScanner`, so it proves the `addDynamic` half only and says nothing about the missing-key behaviour above.
- Same file → the `dropcapClass*` constants (`dropcapClassTempHome`, `dropcapClassOperatorHome`, `dropcapClassArtifactDir`, `dropcapClassWorkdir`) — the four path-class identifiers.
- `internal/e2e/realclaude/initialize_control_record_test.go` → `initControlFixtureRecord` (doc comment **and** struct), `initControlFullRecord`, `initControlFixtureFields`, `TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken` — the four count-word bumps, the load-bearing-literal list, the hand-written listing, and the `NumField` assertion that needs no edit.
- `internal/e2e/realclaude/initialize_control_probe_test.go` → the file header (its `-run` filter and its "two non-live tests" sentence), `runInitControlChild` (its doc's "SINCE #1733 IT TAKES A REDACTOR" paragraph, its record literal), `TestRealClaude_InitializeControl_Capture` (the `newInitControlRedactor` construction site), `TestInitControlProbedArm_IsExactlyOneDeclaredNonEmptyArm` — the precedent for an offline test living in this exec-ing file.
- `internal/e2e/realclaude/initialize_control_writer_test.go` → `TestInitControlFixture_DistinguishesAnEmptyCensusFromAnAbsentOne` — **the shape AC 2's new test follows**, including its two vacuity `t.Fatalf` controls, the tempdir-each note and the "deliberately no `bytes.Contains`" note. Also `writeInitControlFixture` (`out := *rec`, the shallow copy that preserves nil-ness).
- `internal/e2e/realclaude/initialize_control_redaction_test.go` → `newInitControlRedactor`, `redactInitControlRecord` (it names its fields explicitly and visits no map), and the synthetic path constants `initControlTempHomeValue` / `initControlWorkdirValue` — reused by the new offline test.
- `internal/e2e/realclaude/inband_bypass_revoke_fixture_test.go` → `fixtureFieldNonZero` — `reflect.Map` is judged **by length**, in the same arm as `String` and `Slice`.
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans`, the entries for the record, writer and redaction files — why the new helper and its test go in the **probe** file and nowhere else.
- `docs/knowledge/features/e2e-realclaude.md` § the `initialize_control_record_test.go` entry — background only. **Read-only: the documentation phase owns it, and its "carries 28 fields" numeral is not this ticket's to bump.**

## Context

`initControlFixtureRecord` is the durable artifact the `initialize` capture commits. #1733 put the redaction census on it — which classes the substitution table actually rewrote. This slice records the other half: which classes the deny-scan's needles were **armed** for.

The distinction is load-bearing because `dropcapScanner` deliberately skips a dynamic needle shorter than `dropcapMinNeedle`, and reports it not-applied: `bytes.Contains(x, []byte(""))` is always true, so a two-byte credential would match nearly every payload and invert the instrument. An unset `ANTHROPIC_API_KEY` therefore arms nothing — and a record silent about that reads exactly like one where the class armed and found nothing.

The measured wrinkle this ticket exists for: **`scanner.applied()` alone does not satisfy AC 3.** The two arming paths behave differently for an absent value. `addDynamic` appends unconditionally, so an unset credential lands as `false`. `addDynamicPath` calls `dropcapPathSpellings`, which returns `nil` for `""`, so **no needle is appended and `applied()` — which builds its map by iterating `s.needles` — carries no key for the class at all.** The fill site hands `newDropcapScanner` no artifact directory, so `artifact_dir` would vanish from the map outright, which is the outcome AC 3 forbids. `operator_home` has the identical hole whenever `realHome` is empty (HOME unset at launch).

Closing that gap is this ticket's work and it belongs **at the fill site**. `dropcapScanner` must not change: it is shared with the dropcap family and that record's shape is already committed.

No ADR is warranted.

## Design

### 1. The record gains one field

On `initControlFixtureRecord`, immediately after `Redaction`:

```go
CredentialScanApplied map[string]bool `json:"credential_scan_applied"`
```

Same JSON tag and same Go type as `dropcapRecord`'s field, per AC 1 — this record's standing rule is that a shared field's gratuitous divergence is a defect two tickets away. **No `omitempty`**, matching every other tag in this type.

The declaration position is not cosmetic: `initControlFixtureFields` is hand-written **in declaration order**, so the new row goes last, after `{"redaction", rec.Redaction}`.

Doc-comment obligations on the type (this is where most of the ticket's written work lives, and it is the deliverable, not decoration):

- **What it is.** Keyed by class, whether that class's needle was actually armed for the run. POPULATED at the fill site from the scanner the capture built — never computed here, exactly as `Redaction`, `ModelsPresent`, `ModelsCount` and `ModelsEntryFields` are populated.
- **Armed is not scanned.** Say plainly that **until #1748 lands the classes are armed and nothing scans with them**: this record's scanner is built and reports its arming, and no scan runs over the written bytes yet. Same shape and same reason as the existing "an empty census is not a clean artifact" paragraph — a reader must not mistake a field that is merely present for evidence the artifact was checked.
- **`{}` is not `null`.** `applied()` returns a non-nil empty map and an unfilled field is nil, so `{}` says the scanner was built and armed nothing while `null` says it never ran. That whole distinction lives in the marshalled bytes and one `omitempty` — or one writer-side helper normalising nil to empty — erases it with every other test in this package still green. Name `TestInitControlFixture_DistinguishesAnArmedNothingScanFromAnAbsentOne` (§ 4) as what reddens.
- **The two censuses are not restatements of each other, and cover different class sets.** `newInitControlRedactor` arms four path classes including a `$TMPDIR` one; `newDropcapScanner` arms two credential classes, four path classes and five fixed literals, and has no tempdir class at all.
- **The name is inherited, not descriptive.** The tag says `credential_scan_applied` while the map also carries path classes and fixed literals. That is the sibling's spelling and AC 1 forbids diverging from it; say so, so a later reader does not "fix" the tag and break the shared shape.
- **It takes `Redaction`'s exception to the redaction instruction.** The type carries a standing instruction that every string-bearing field added to it must be visited by `redactInitControlRecord`, with `Redaction` as the one documented exception. This map's **keys are the scanner's own vocabulary** — the `dropcapClass*` identifiers and the deny-class names, never child output, never a path, never a matched value — so it takes the same exception for the same reason. **Recording this is part of this edit, not a follow-up.** It is a comment edit and not a pass edit: `redactInitControlRecord` names its fields explicitly, visits no map, and nothing in the package reddens for an unvisited field — which is exactly why the paragraph is the guard.
- **The exception is CONDITIONAL on the keys staying declared identifiers**, and this makes `map[string]bool` a committed shape for a type that belongs to the **dropcap** family, whose next author is not reading this file — the same warning the `Redaction` paragraph already carries for `dropcapSubstitution`. `applied` keys by `n.class` and never by `n.value`, so no needle, path or credential can reach a key today; `TestDropcapDenyClassNamesDoNotCarryTheirNeedle` is what keeps a deny-class name from carrying its own needle, after the first live capture failed exactly that way. A class name derived from a matched value would put that value straight into a committed artifact under an unvisited field. **Nothing in this package reddens when it does**, and #1748's deny-scan over the written bytes is the net behind the paragraph.
- **It needs no cap**, structurally: its length is bounded by the classes `newDropcapScanner` can arm and its values are bools, so it cannot grow with child output. Same argument the `Redaction` field already makes.

### 2. The fill-site completion

Two new declarations in `initialize_control_probe_test.go`, beside the driver they serve.

```go
// initControlScanPathClasses — newDropcapScanner's addDynamicPath classes, hand-written.
var initControlScanPathClasses = []string{
	dropcapClassTempHome, dropcapClassOperatorHome, dropcapClassArtifactDir, dropcapClassWorkdir,
}

// initControlScanApplied returns applied() with every path class present.
func initControlScanApplied(s dropcapScanner) map[string]bool
```

Behaviour, in one sentence: take `s.applied()` and, for each class in `initControlScanPathClasses` that the map does **not** already carry, add it as `false`.

Three details, each of which is the difference between a completion and a fabrication:

- **`applied()` returns a fresh map per call**, so the helper mutates its own copy and no caller's map is shared. Do not take a defensive second copy.
- **Only a MISSING key is added.** An existing entry — `true` or `false` — is left alone. A completion that assigned `false` unconditionally would report every armed class as armed-nothing, and the `workdir` control in § 5 is its sole red.
- **The list is a SECOND, INDEPENDENT COPY** of `newDropcapScanner`'s `addDynamicPath` calls, and the doc must say so and say what drift costs in each direction: a class added there and not here vanishes from the map for an empty value — exactly the defect this ticket fixes for `artifact_dir`; a name here that no scanner arms writes a `false` key for a class that does not exist. The two credential classes and the five fixed literals are deliberately absent from the list because they cannot vanish — `addDynamic` appends unconditionally and the fixed needles are never empty.

Whether to close only `artifact_dir` or every path class was left to the architect. **Close all four.** `operator_home` has the identical hole by the same `dropcapPathSpellings` return, it is reachable on any machine launched with HOME unset, and one general fill is one place to get it right rather than a per-class judgement call re-litigated by the next ticket.

### 3. Wiring at the capture site

`TestRealClaude_InitializeControl_Capture` already builds `newInitControlRedactor(...)` and hands it to `runInitControlChild`. Build the scanner beside it and hand it in the same way:

```go
scanner := newDropcapScanner(home, "", workdir)
```

and

```go
func runInitControlChild(t *testing.T, claudeBin, workdir string, red *dropcapRedactor,
	scanner dropcapScanner, versionRaw, versionToken string) *initControlFixtureRecord
```

`dropcapScanner` is a value type and the sibling's `dropcapWriteRecord` already takes it by value; follow that.

- **One call site.** `runInitControlChild` has exactly one caller, so the signature change is a one-site edit.
- **Why the construction lives at the capture site and not inside the driver:** the same reason the redactor's does — that is where `home` (which plays `tempHome`) is in hand, and one construction site is one place to get the parameter order right. Unlike `newInitControlRedactor`, `newDropcapScanner` reads `realHome` and `os.Getenv` **itself**, so the call site needs neither in hand; the file execs and correctly carries no `finOfflineExecBans` entry, so those reads are legitimate here and nowhere else in this family.
- **The empty slot is the SECOND parameter.** The signature is `newDropcapScanner(tempHome, artifactDir, workdir string)`. What this run has nothing for is `artifactDir`, the middle one. Passing `workdir` there would arm `artifact_dir` with the workdir path and leave `workdir` unarmed. **Do not mint a directory to fill the slot** — the empty value is the fact the record is recording.
- **Assign in the record literal**, beside the other populated fields: `CredentialScanApplied: initControlScanApplied(scanner)`. Not in `writeInitControlFixture`, whose `out := *rec` copy must go on receiving a record it only copies; not after the fact. The value is available at literal time, unlike `Redaction`, which is assigned from what the redaction pass returns.
- **Extend `runInitControlChild`'s doc** where it explains why it takes a redactor rather than three more path strings: the scanner arrives by the same route and for the same reason, and #1715 minting one per arm inside its loop stays possible.
- **Do not add the map to any `t.Logf`.** The existing summary line's "never `%+v` the record" rule stands; there is no reader for these values in a run log that the committed fixture does not serve better.

#### The scanner value is credential-bearing — never format it

**This is the one new hazard the parameter creates, and the instruction must land in `runInitControlChild`'s doc, not only here.** `newDropcapScanner` reads `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` and stores those values in `dropcapNeedle.value`, so a `dropcapScanner` in scope is **two live credentials in a struct**. Before this ticket no such value existed anywhere in `initialize_control_probe_test.go`; after it, one sits beside a driver that logs on nearly every path.

- **`%v`, `%+v`, `%#v` or `%q` on the scanner — or on a `dropcapNeedle`, or on `s.needles` — prints `sk-ant-…` into a run log this pipeline salvages.** `initControlScrubbed` does not catch it: that guard reads the **child's stderr**, and this would be the harness's own output. The instruction is the only guard, which is why it is written at the site.
- **`applied()`'s map is safe to print, and that is the useful failure message.** Its keys are declared vocabulary — the two environment-variable *names*, the `dropcapClass*` constants and the deny-class identifiers — and its values are bools. `applied` keys by `n.class` and never by `n.value`, so no needle, path or credential can reach a key. Say both halves: a reader who knows only "the scanner is dangerous" writes a message with no diagnostic in it.
- The dropcap file already states the rule for its own site ("read via `os.Getenv` solely as needles: never stored in a record field, never logged and never written"), and `dropcapWriteRecord` already takes a scanner by value under it. This carries the same rule to the second site rather than inventing one.
- Handing `runInitControlChild` the finished `map[string]bool` instead of the scanner would keep credential values out of the driver entirely, and it is rejected deliberately: #1748 needs the scanner **there** to scan the bytes the writer produced, and splitting the two would move the parameter twice. The trade is a narrower type for one ticket against a second signature change in the next.

### 4. Header edits in the probe file

The file header carries a `-run` filter and the sentence "The two non-live tests in this file — the summariser's table and the probed arm's". A third offline test lands here, so **both** must move: add `TestInitControlScanApplied_` to the filter's alternation, and say three, naming the new one. Leaving either stale makes the header's own instruction skip the test it is documenting.

The writer file's header needs no edit — #1731 added a test there without one, and its `-run` filter already keys on the `TestInitControlFixture_` prefix, which the new test in § 6 must therefore carry.

### 5. `initControlFullRecord`, the listing and the count words

- **The fixture value carries TWO entries, one `true` and one `false`:** `dropcapClassWorkdir: true` and `dropcapClassArtifactDir: false`.
  - `fixtureFieldNonZero` judges container kinds **by length** and handles `reflect.Map` in the same arm as `String` and `Slice`, so the map must be non-empty or the non-zero property reddens.
  - Class identifiers carry no path, so `TestInitControlRedactRecord_LeavesAPathFreeRecordByteIdentical` stays green. `TestDropcapDenyClassNamesDoNotCarryTheirNeedle` is what keeps a class name from carrying its own needle, after the first live capture failed exactly that way.
  - **The `false` entry is load-bearing, and this is the reason to write down.** A writer that filtered armed-nothing classes out of the map on the way to disk is precisely the normalisation AC 2 forbids. It is invisible to § 6's row, which compares `{}` against `null` — neither side carries an entry at all. `TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry` is its **sole red**, and only if this fixture carries a false-valued entry. That is a discriminating-pair argument of the kind note 6 makes for the trailers, and it is why this row does not follow note 7's "one entry is enough".
  - Distinctness constrains this row **not at all**, stated so nobody counts on it either way: that subtest groups by `reflect.TypeOf` of the row's value, `map[string]bool` has no same-typed sibling in this record, and it never looks inside the map.
  - `encoding/json` marshals map keys in sorted order, so the two entries are byte-stable across runs — the byte-identity row and the committed artifact both depend on that and neither needs a normaliser.
- **Note 8 in the load-bearing-literal list**, carrying the four bullets above. `Seven literal choices are load-bearing` becomes `Eight`.
- **Three `twenty-eight` → `twenty-nine` bumps** in the record file: the type's doc ("Nineteen of its twenty-eight fields are `setModeFixtureRecord`'s"), `initControlFullRecord`'s doc ("every one of the twenty-eight fields"), and `initControlFixtureFields`' doc ("lists rec's twenty-eight fields once"). **Adding a field costs four count-word bumps, not three** — the fourth is note 8's `Seven` → `Eight` above.
- **A numeral sweep for `28` in the record file has a false positive**: the load-bearing-literal note reading "28 bytes, comfortably inside `versionSlug`'s 32-character clamp" is a byte count, not a field count. **Do not bump it.**
- **One listing row**, last: `{"credential_scan_applied", rec.CredentialScanApplied}`. Hand-written, in declaration order, never regenerated by reflection.
- The `NumField` assertion needs no edit — it compares the struct's field count against the listing's length, and both move together.

## Concurrency model

None. `newDropcapScanner` and `applied()` run on the test goroutine before the child is spawned; `initControlScanApplied` mutates a map it owns. No goroutine, no channel, no lock.

One asymmetry a reader will otherwise guess the wrong way round, and it belongs in `runInitControlChild`'s doc beside the redactor's paragraph: **`dropcapScanner` is not `dropcapRedactor`.** The redactor accumulates unlocked counters across every call, which is why #1733 requires a fresh one per arm and why #1715 must mint one inside its loop. The scanner is append-only during construction and read-only afterwards — `addDynamic` and `addDynamicPath` are pointer-receiver and run only inside `newDropcapScanner`, while `applied` and `scan` are value receivers that read `s.needles` and allocate their own results — so a scanner shared across arms would neither race nor carry one arm's state into another's record. Do not copy the redactor's per-arm rule to it for a reason that does not apply, and do not read this as licence to share a redactor.

## Error handling

No new failure mode. `newDropcapScanner` cannot fail; `dropcapPathSpellings` swallows an `EvalSymlinks` error by falling back to the plain spelling. The completion adds keys and never removes or overwrites one, so the worst outcome of a stale `initControlScanPathClasses` is the pre-existing behaviour (a class missing from the map) or a spurious `false` key — the § 2 doc states both, and § 6's staleness control binds three of the four names.

## Testing strategy

Offline and deterministic throughout. Every new test must **PASS — not SKIP** on a machine with no claude and no credentials. The package is behind the `e2e_realclaude` build tag, so `make check` never compiles it and the suite exits 0 both on a build failure and on a full credentials skip: **read the count of `=== RUN` lines that executed, never the exit code.**

### 6. AC 2 — `initialize_control_writer_test.go`

`TestInitControlFixture_DistinguishesAnArmedNothingScanFromAnAbsentOne`, following `TestInitControlFixture_DistinguishesAnEmptyCensusFromAnAbsentOne` structurally:

- Take `initControlFullRecord()`, make two shallow copies differing in exactly one field: one carrying an armed-nothing map, one carrying `nil`.
- **Build the armed-nothing map from `dropcapScanner{}.applied()`, not from a `map[string]bool{}` literal.** The non-nil-ness of `applied()`'s return is the entire mechanism the distinction rests on, and a literal would pin the test's own value instead of the production one. Note in the doc that `initControlScanApplied` cannot serve here — by construction it always adds four keys and can never return empty.
- **Two vacuity controls, `t.Fatalf` with distinct messages** (a property that cannot discriminate is a broken instrument, not a passing test): the map must be non-nil, and it must have length zero.
- Write each copy through `writeInitControlFixture` into **its own `t.TempDir()`** — both copies carry the same `claude_version` and the same `arm`, so `initControlArmFixtureName` mints one filename for both and a shared directory would have them overwrite each other. Read both files back and assert the bytes **differ**.
- **No `bytes.Contains` assertion** pinning `"credential_scan_applied": {}` against `null`. Inequality already reddens for every mutant in the class — an `omitempty` drops the key from both sides, a nil-normaliser renders `{}` on both, a `json:"-"` drops it from both — and a literal-spelling assertion would additionally pin `json.MarshalIndent`'s whitespace, which is not this row's subject.
- **State what it does not measure:** it catches a writer-side helper that turns a nil map non-nil. It does not catch a writer that overwrites a non-nil map with a different value; that half rests on `writeInitControlFixture`'s copy-and-don't-mutate contract. And the `null` side is genuinely reachable in the committed corpus — every fixture predating this field decodes with a nil map — which is what makes the distinction worth bytes.

### 7. AC 3 — `initialize_control_probe_test.go`

`TestInitControlScanApplied_RecordsAnArmedNothingClassForAnAbsentPath`, offline, `t.Parallel()`, spawning nothing. Build the scanner exactly as the fill site does, over the family's existing synthetic constants rather than fresh literals: `newDropcapScanner(initControlTempHomeValue, "", initControlWorkdirValue)`. Both are comfortably longer than `dropcapMinNeedle`, and `dropcapPathSpellings` only *attempts* `EvalSymlinks`, so non-existent synthetic paths are deterministic and touch no filesystem state.

Scenarios, as bullet points — the developer writes them in this package's idiom:

- **Precheck (`t.Fatalf`), and it is the ticket's headline fact:** the raw `scanner.applied()` must **not** carry `artifact_dir`. If it does, `addDynamicPath` started appending a needle for an empty path, the completion below is dead weight, and the row passes for the wrong reason. Message says which.
- **The subject:** `initControlScanApplied(scanner)` carries `artifact_dir` as a key, and its value is `false`. Two separate checks with two messages — present-but-true and absent-entirely are different defects.
- **The vacuity control:** in the same map, `workdir` — a class handed a non-empty value — is present and **`true`**. Sole red for a completion that assigns `false` unconditionally, which would otherwise satisfy the subject check while destroying the field's meaning.
- **The staleness control:** for a scanner built with all three path parameters non-empty, every class in `initControlScanPathClasses` **except `operator_home`** appears in the raw `applied()`. Sole red for a renamed or mistyped class name in the list, which would write a `false` key for a class no scanner arms. The carve-out is stated with its reason in the message, not left silent: `operator_home`'s value is `realHome`, a package-level `os.Getenv("HOME")` read no test can control, so binding it here would fail on a legitimately-configured machine with HOME unset. `newDropcapScanner` is the declaration a reader checks for that one name.
- **What it must not assert, and why, in the doc:** the map's values are environment-dependent for three classes — both credential classes read `os.Getenv` and `operator_home` reads `realHome` — so they flip between an operator machine and CI. Assert only over classes fixed by caller-passed values.
- **Failure messages print the MAP, never the scanner.** This test's scanner holds the operator's two live credentials exactly as the capture's does, so § 3's never-format instruction binds here too — and the map is the diagnostic worth printing, since its keys are declared vocabulary and its values are bools.

Placement rationale, worth a sentence in the test's doc: this helper and its test live in the **probe** file because that is the fill site's file, it execs and correctly carries no `finOfflineExecBans` entry, and it already hosts two offline tests. The record, writer and redaction files each ban `os.Getenv`/`os.Environ`/`os.LookupEnv` by AST name **in that file**, so a test calling `newDropcapScanner` from one of them would leave the ban green while reading the environment one hop away — the "different fabric" caveat those entries already state. Green-but-dishonest is not where this belongs.

### 8. AC 4 — the existing rows, unchanged

`TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken` (listing-covers-every-field, non-zero, distinctness), `TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry`, `TestInitControlFixture_WriterCapsStderrCapture` and `TestInitControlRedactRecord_LeavesAPathFreeRecordByteIdentical` must all stay green with no assertion edited. If any needs an assertion change, the field, the fixture value or the listing row is wrong — not the test.

### Verification commands

The whole family plus the ban check, and the new test's own prefix:

```
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestInitControl|TestFinOfflineFilesReachNoExecHelper|TestDropcap' \
  ./internal/e2e/realclaude/
```

Then `make check` (the package is not compiled by it — that is expected, and it is the reason for the filtered run above), and `gofmt -l` over the three touched files.

## Open questions

- **None blocking.** One thing is deliberately deferred rather than open: nothing scans the written bytes with these needles yet. #1748 is where the armed map acquires a scan to be the audit trail for, and § 1's doc paragraph says so in the artifact rather than only here.

## Security review

**Verdict:** PASS (one MUST FIX found and resolved inline before this section was written; the checklist was then re-walked over the revised spec)

**Findings:**

- **[Trust boundaries] MUST FIX — resolved.** The design moves a credential-bearing value across a boundary that did not previously exist. `newDropcapScanner` reads `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` and holds both in `dropcapNeedle.value`; § 3 hands that struct to `runInitControlChild`, a driver that logs on nearly every path and previously had no such value in scope. `initControlScrubbed` does not cover it — that guard reads the **child's** stderr, and a `%+v` on the scanner would be the harness's own output. Resolved by § 3's "The scanner value is credential-bearing — never format it", which requires the instruction to land in `runInitControlChild`'s doc at the site (not only in this spec), states that `applied()`'s map **is** safe to print so the ban does not cost the diagnostic, and records why the narrower alternative (passing the finished map) was rejected — #1748 needs the scanner at that site.
- **[Tokens, secrets, credentials] No findings.** Creation, storage, rotation, revocation and expiry are **not applicable**: this ticket mints no token and reads two existing values only indirectly, as search needles inside `newDropcapScanner`. No needle value reaches a record field, a log or the file — `applied` keys its map by `n.class` and never by `n.value`, so the committed bytes carry declared identifiers and bools only. The credential classes' keys are the environment-variable *names*, not their values, and are already a committed shape via `dropcapRecord`.
- **[Tokens, secrets, credentials] SHOULD FIX — noted in the spec, not gated on.** The redaction exception this field takes is conditional on the keys staying declared identifiers, and `map[string]bool` becomes a committed shape for a type owned by the **dropcap** family, whose next author is not reading the initialize record's file. A class name derived from a matched value would publish that value under a field `redactInitControlRecord` deliberately does not visit, and **nothing in the package reddens when it does**. § 1 carries the paragraph; `TestDropcapDenyClassNamesDoNotCarryTheirNeedle` is the standing guard for the deny classes and #1748's scan over the written bytes is the net behind it.
- **[File operations] No findings.** No new path is constructed from any input. The only writes are two `writeInitControlFixture` calls into their own `t.TempDir()` in § 6 — existing, already-proven code with its existing mode — and § 7 writes nothing. No check-then-use: `dropcapPathSpellings` returns `nil` for the empty `artifactDir` **before** any syscall, and its `filepath.EvalSymlinks` call is a read-only spelling resolution with no open-after-check. The committed fixture's own atomicity is #1702's and is untouched.
- **[Subprocess / external command execution] No findings.** Nothing about the spawn changes: same argv, same nil `Config.Env` (so the child inherits verbatim, as before), same signal and context handling. The scanner is built before the spawn, passed by value, and never reaches `cmd.Env` or stdin. `initControlScrubbed` still runs on the child's raw stderr before anything is written, and remains the guard that keeps a credential out of the committed artifact.
- **[Cryptographic primitives] Not applicable.** No RNG, no key material, no comparison against a secret. The family's one such comparison, `initControlScrubbed`'s `strings.Contains`, is unchanged, and its documented reason for not being constant-time still holds: the only party on the other side is the claude binary, which was handed the token.
- **[Network & I/O] Not applicable.** No socket, no server, no network read. The new field needs no cap for a structural reason rather than a judgement call: its length is bounded by the classes `newDropcapScanner` can arm — two credential, four path, five fixed — and its values are bools, so it cannot grow with child output. `initControlScanApplied` adds at most the four names in `initControlScanPathClasses`.
- **[Error messages, logs, telemetry] No findings beyond the first.** The scanner ban above is the security-relevant one. Printing the map would **not** leak — § 3 says so explicitly so the ban is not over-read into silencing failure diagnostics — and the spec's "do not add the map to any `t.Logf`" is a noise judgement, not a leak guard. The existing "never `%+v` the record" rule is unchanged and not widened. No telemetry.
- **[Concurrency] No findings.** No goroutine, channel or lock is added; the driver's single reader goroutine and its join are untouched. `applied()` allocates a fresh map per call, so `initControlScanApplied` mutates only what it owns. The Concurrency section states the asymmetry a reader would otherwise guess wrong — the scanner is read-only after construction, unlike `dropcapRedactor`'s unlocked accumulating counters — so no per-arm rule is invented for it and none is relaxed for the redactor.
- **[Threat model alignment] OUT OF SCOPE, named.** The governing threat for this family is a committed test artifact publishing an operator's credential or home path, not the mobile wire protocol, so `docs/protocol-mobile.md` § Security model does not apply. The standing controls are `initControlScrubbed` and `redactInitControlRecord`; the missing one is a deny-scan over the **written** bytes, which is **#1748** and not this ticket. The record's doc must therefore say that until #1748 lands the classes are armed and nothing scans with them — § 1 requires exactly that, for the same reason the `Redaction` field's doc already refuses to be read as a clean bill of health.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-24
