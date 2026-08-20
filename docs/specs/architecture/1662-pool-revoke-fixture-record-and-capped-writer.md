# #1662 — Pool-revocation fixture record with a capped, directory-injectable writer

**Ticket:** [#1662](https://github.com/pyrycode/pyrycode/issues/1662) · `bug` `size:s` `security-sensitive` `needs-real-claude`
**Blocked by:** #1661 (merged `f7d332f`) · **Blocks:** #1643

Two files, both tests, zero production files:

- `internal/e2e/realclaude/inband_bypass_revoke_fixture_test.go` — **new**
- `internal/e2e/realclaude/offline_exec_ban_test.go` — one map entry appended

---

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_search` / `codegraph_node`.

| File | Symbols | What to extract |
|---|---|---|
| `internal/e2e/realclaude/inband_bypass_revoke_names_test.go` | `poolRevokeFixtureName` | The namer this writer mints through. Its doc comment states the contract the writer leans on: the result is always a **single clean path component**, for any pair of inputs. Read the whole header — it is the shape this file's header should mirror in tone and length. |
| `internal/e2e/realclaude/inband_bypass_revoke_arms_test.go` | `poolRevokeArm`, `poolRevokeArms` | The arm table and its field names (`launchYOLO`, `takesSettingsUpdate`) — the record's field names must match so the two files read as one family. The table is **READ-ONLY**: do not append to it, do not use its rows as this file's fixture arm. |
| `internal/e2e/realclaude/set_permission_mode_probe_test.go` | `setModeFixtureRecord` | The security doc comment the new type inherits verbatim in substance: no `env` field, and the free-text capture is capped. |
| ″ | `writeSetModeFixture`, `setModeFixturePath` | The temp-file-and-rename discipline to keep, and the hardcoded `packageDir(t)/testdata` to **not** repeat. Do not edit either. |
| ″ | `probeOutcome` | The nested behavioural-read type field 15 carries. Plain tagged struct, no custom marshaller — confirmed: the package declares no `MarshalJSON`/`UnmarshalJSON` anywhere. |
| `internal/e2e/realclaude/permission_protocol_spike_test.go` | `stderrFixtureCap`, `truncateString` | The 8 KiB cap constant and the byte-slicing helper to reuse. Do not declare a second cap. |
| ″ | `versionSlug` | What `poolRevokeFixtureName` applies to **both** of its arguments. Lowercase, `[^a-z0-9._-]+` → `_`, 32-char cap; idempotent on already-slugged input. |
| ″ | `packageDir`, `writeFixture` | `packageDir` is what this file must never reach. `writeFixture` is the second instance of the same anti-pattern. |
| `internal/e2e/realclaude/offline_exec_ban_test.go` | `finOfflineExecBans`, `TestFinOfflineFilesReachNoExecHelper` | The map to append to, and the AST check that reads it. Note it parses **without** `parser.ParseComments`, and that a dotted entry matches a selector while a bare entry matches an identifier. |
| ″ | the `inband_bypass_revoke_names_test.go` entry | The trio to copy (`packageDir`, `setModeFixturePath`, `writeSetModeFixture`) and the five `os.*`/`filepath.Glob` bans **not** to copy. |
| `internal/e2e/realclaude/fixtures.go` | `WithWorktreeAuthenticated` | Calls `t.Skipf` *inside* the test body — the reason the ban entry exists rather than a header paragraph. |
| `internal/sessions/pool.go` | `deliverSettingsInBand` | Field 16's source is the `p.log.Info("sessions: in-band settings command not delivered", …)` call inside it. Nothing is returned and no struct escapes — the field is a capture of emitted log records, not a decoded type. |
| `docs/knowledge/features/set-permission-mode-inband-probe.md` | — | #1595's live wire-format result: the committed fixture family this new family must stay clear of. |

---

## Context

#1595 proved live that a `set_permission_mode` control request carrying `mode: "default"` drops a running child's bypass posture. #1604 built the composed path (`Pool.UpdateSettings` → `inBandDeliverable` → `deliverSettingsInBand` → `Runner.RevokeBypass`) and #1622 proved that path emits the revocation onto a live child without respawning it. None of them claims behavioural **effect** — an echoed `init.permissionMode` is claude's report of its own posture, not proof it is enforced.

#1643 is the live three-arm probe that will claim effect, and it commits a fixture per arm. This ticket ships that fixture's record type and its writer, and settles the three properties an artifact needs before it is worth committing — **complete** (no field silently dropped on the way to disk), **bounded** (no unbounded credential-bearing capture), **atomic** (no half-written residue) — with no claude binary and no credentials. #1643's first failure mode then costs zero tokens instead of a whole three-arm run.

The blocker (#1661) already shipped `poolRevokeFixtureName` and proved no name it mints can join a committed fixture family. This ticket is the write path that mints through it — which is what couples the blocker's lock to the artifact rather than to a convention.

**ADR:** not warranted. This is one test file inside an established family; the design decisions below are local to it and belong in the file's header, which is where this package puts them.

---

## Design

### Structure of the new file

Five sections, in this order, mirroring `inband_bypass_revoke_names_test.go`:

1. Build tag `//go:build e2e_realclaude`, package clause, file header.
2. The record type.
3. The writer plus its cap helper.
4. The round-trip / one-entry test (AC 1 + AC 2).
5. The cap test (AC 3).

### The record type

`poolRevokeFixtureRecord` — exactly eighteen fields, no more. Grouped and tagged snake_case, matching the package:

| # | Go field | Type | JSON tag | Source in #1643 |
|---|---|---|---|---|
| 1 | `ClaudeVersionRaw` | `string` | `claude_version_raw` | raw `claude --version` output |
| 2 | `ClaudeVersion` | `string` | `claude_version` | the slugged token the name is minted from |
| 3 | `Arm` | `string` | `arm` | arm name |
| 4 | `LaunchYOLO` | `bool` | `launch_yolo` | the arm's **stored** bootstrap posture |
| 5 | `TakesSettingsUpdate` | `bool` | `takes_settings_update` | mid-run settings update; false for both controls |
| 6 | `Argv` | `[]string` | `argv` | composed argv the runner factory received |
| 7 | `Prompts` | `[]string` | `prompts` | probe prompts verbatim |
| 8 | `SpawnCount` | `int` | `spawn_count` | spawns observed |
| 9 | `PIDBefore` | `int` | `pid_before_revocation` | child pid before the revocation |
| 10 | `PIDAfter` | `int` | `pid_after_revocation` | child pid after it |
| 11 | `ControlResponses` | `[]json.RawMessage` | `control_responses` | every `control_response` verbatim |
| 12 | `InitPermissionModes` | `[]string` | `init_permission_modes` | `init.permissionMode` echoes in arrival order |
| 13 | `StdoutLines` | `[]string` | `stdout_lines` | all captured stdout lines |
| 14 | `TurnBoundaries` | `[]int` | `turn_boundaries` | turn boundaries |
| 15 | `ProbeOutcomes` | `[]probeOutcome` | `probe_outcomes` | per-turn behavioural read |
| 16 | `NotDeliveredLogs` | `[]string` | `not_delivered_logs` | `deliverSettingsInBand`'s not-delivered log records |
| 17 | `DeadlineTripped` | `bool` | `deadline_tripped` | the arm's hard deadline tripped |
| 18 | `ChildOutputCapture` | `string` | `child_output_capture` | capped free-text child output (the stderr-equivalent stream) |

Field-name notes for the developer:

- **13 is `[]string`, not `[]json.RawMessage`** — deliberately different from `setModeFixtureRecord`'s `StdoutEvents`. The ticket bounds this record at eighteen fields and carries no non-JSON-line counter, so the honest type is the raw line, which captures a non-JSON line as itself rather than dropping it and incrementing a counter that does not exist here.
- **4 and 5 take their names from `poolRevokeArm`'s `launchYOLO` / `takesSettingsUpdate`**, not from `setModeFixtureRecord`'s `LaunchYOLOFlag`. `LaunchYOLO` is a *stored* posture lifted off the registry entry, not a runtime flag, and the doc comment must say so.
- **16 is a log capture, not a decode.** `deliverSettingsInBand` returns nothing.

The type's doc comment must carry, in its own words, three things:

1. The inherited credential constraint: **no `env` field, ever.** The credential reaches the child through the environment (`WithWorktreeAuthenticated`) while the argv carries none, so recording argv is safe and recording env would not be.
2. Why field 18 is capped: an auth failure can dump an unbounded, credential-bearing message into a file that is then committed.
3. A constraint on #1643 that this ticket cannot enforce in code: fields 11, 13 and 16 are **not** capped, because capping the probe's structured evidence would destroy the artifact. Field 18 is the field designed to absorb unbounded child output. #1643 must route a free-text failure stream there, not into `StdoutLines`.

What the doc comment must **not** claim: that the round trip catches a misspelled JSON tag. Decoding back through the struct that wrote the file is symmetric and cannot. These names are read by humans and by future re-measurement, not by a production decoder.

### The writer

```go
// dir is a parameter, not packageDir(t). Returns the written path.
func writePoolRevokeFixture(t *testing.T, dir string, rec *poolRevokeFixtureRecord) string
```

Behaviour, in order:

1. `t.Helper()`.
2. **Shallow-copy `*rec` into a local.** The cap is applied to the copy; the caller's record is never mutated. Without this, AC 3's assertion would compare a mutated in-memory record against its own decode and prove nothing about the write path.
3. `out.ChildOutputCapture = capFixtureCapture(out.ChildOutputCapture)`.
4. `name := poolRevokeFixtureName(out.ClaudeVersion, out.Arm)` — **both arguments passed through unmodified**; the namer slugs both.
5. `os.MkdirAll(dir, 0o755)`.
6. `json.MarshalIndent(&out, "", "  ")` — parity with both existing writers, and the artifact is read by humans.
7. `os.WriteFile(path+".tmp", data, 0o644)` then `os.Rename(tmp, path)` — the same temp-file-and-rename discipline `writeSetModeFixture` uses, with the same predictable residue name.
8. Return `path`.

Every failure is `t.Fatalf` prefixed `#1662:`. **Failure messages name the arm and the error, never the record.** A `%+v` of the record dumps up to 8 KiB of child output into a run log; see § Security review.

```go
// Caps at stderrFixtureCap and never splits a rune. See the UTF-8 note below.
func capFixtureCapture(s string) string
```

- Returns `s` unchanged when `len(s) <= stderrFixtureCap`.
- Otherwise `truncateString(s, stderrFixtureCap)`, then drop **at most `utf8.UTFMax-1` trailing bytes**, one at a time, while `utf8.DecodeLastRuneInString` reports `(utf8.RuneError, 1)`.

**Why the rune-boundary trim, and why it is not scope creep.** `truncateString` slices bytes. `encoding/json` does not error on invalid UTF-8 — it substitutes U+FFFD, three bytes per invalid byte. Measured on this toolchain: a five-byte string cut mid-rune marshals to `"aaaa�"` and reads back at **seven** bytes. So a plain byte cap over a multi-byte capture reads back at up to `cap + 6` — AC 3's "reads back at exactly the cap" would be false, and the writer's stated bound would be a bound it does not hold. `DecodeLastRuneInString` returns `(RuneError, 1)` for exactly the split-tail case and `(RuneError, 3)` for a legitimately encoded U+FFFD, so the `size > 1` guard leaves genuine content alone. The loop bound of three is the maximum number of continuation bytes in a UTF-8 sequence.

The cap constant and the slicing helper are both reused. Nothing new is declared but the boundary trim.

### AC 1 + AC 2 — the round-trip test

`TestPoolRevokeFixture_RoundTripsEveryFieldIntoOneNamedEntry`. Top-level `t.Parallel()`; the three `t.Run` blocks below share one written artifact and are **not** parallel.

**The fixture record.** Built by a helper returning a `*poolRevokeFixtureRecord` in which every one of the eighteen fields carries a non-zero value, and every pair of same-typed non-bool fields carries distinct values. Two properties the fixture must have, both load-bearing:

- **The arm carries a character `versionSlug` rewrites** — e.g. `"revoke arm/2"`. Without it, AC 2's "whose name is exactly what `poolRevokeFixtureName` mints" is green under a writer that interpolates its own format string, because every real arm name and every slugged version token is already slug-clean. With it, a raw-interpolating writer either mints a different name (space case) or tries to write into a non-existent subdirectory and fatals (separator case). This is the single assertion that keeps the blocker's lock coupled to the write path. Do **not** take the arm from `poolRevokeArms`; that table is read-only and pinned by name elsewhere.
- **`ControlResponses` carries a realistic object**, e.g. one `control_response` envelope — and it must contain no `<`, `>` or `&`. `encoding/json` escapes those inside a `json.RawMessage`, which would survive compaction and break the comparison below for a correct writer.

**The comparison table.** One slice of `{name string; want, got any}` rows, eighteen of them, built after the decode: `want` from the constructed record, `got` from the decoded one. Named rows, not eighteen hand-written `if` statements — that is what holds this test to ~115 lines instead of 250.

One row needs pre-normalising before it enters the table. Measured on this toolchain: `json.MarshalIndent` **reflows an embedded `json.RawMessage`**, so a `{"type":"control_response",…}` object written and read back is not byte-equal — it comes back carrying the indentation. Compact both sides of the `ControlResponses` row before putting them in the table. What compacting blinds: whitespace introduced by `MarshalIndent`, and nothing else. What it still catches: a dropped field (nil vs non-nil), a `json:"-"`, and a field decoded from the wrong tag. A scalar `RawMessage` would round-trip byte-equal and dodge the problem, but it is a weaker stand-in for a real control response, so normalise instead.

Three `t.Run` blocks over that table:

- **`every field carries a non-zero value`** — for each row, `want` is non-zero, where "non-zero" means `Len() > 0` for string/slice/map kinds and `!IsZero()` otherwise. A `[]string{}` is `!IsZero()` but proves nothing, which is why the kind switch exists. Failure message: the field's name and why a zero-valued fixture settles nothing.
- **`every field survives the round trip`** — `reflect.DeepEqual(want, got)` per row. Failure message names the field.
- **`same-typed fields carry distinct values`** — for every pair of rows sharing a `reflect.TypeOf(want)`, `!reflect.DeepEqual(want_i, want_j)`. **Skip `reflect.Bool`**: all three bools are `true` by AC 1, and that is sound because a tag collision between two bools drops both and reads back `false`, which the non-zero block above already catches. Failure message: naming both fields and the point — two same-typed fields must not be able to satisfy each other's assertion.

Then, outside the table:

- **`os.ReadDir(dir)` returns exactly one entry**, and `entries[0].Name() == poolRevokeFixtureName(rec.ClaudeVersion, rec.Arm)` recomputed independently. One assertion covering four hazards at once: a stranded `.tmp` from a missing rename, a stray file, a write that escaped to the package's real `testdata/` (which leaves the tempdir at **zero** entries — this is what closes the relative-path hazard the ban entry cannot), and a writer that minted its target name some other way.
- The read-back is `os.ReadFile(path)` → `json.Unmarshal` into a fresh `poolRevokeFixtureRecord`.

The target directory is `t.TempDir()`. Not banned, and needed.

### AC 3 — the cap test

`TestPoolRevokeFixture_WriterCapsChildOutputCapture`. Table-driven, one `t.TempDir()` per row, asserted **through the writer's output on disk** — never by calling `capFixtureCapture` directly, because AC 3's claim is about what reads back and a direct call would not catch a writer that forgot to apply the cap.

Rows:

| Row | Capture | Assertion |
|---|---|---|
| `over_cap_ascii` | `strings.Repeat("A", stderrFixtureCap+1024)` | read-back **byte** length `== stderrFixtureCap` exactly |
| `under_cap` | a short ASCII literal | read-back `==` the original, whole |
| `over_cap_multibyte` | one ASCII byte followed by repeated two-byte runes, e.g. `"A" + strings.Repeat("é", 5000)` | read-back is valid UTF-8, `len <= stderrFixtureCap`, `len >= stderrFixtureCap-3`, and a prefix of the original |

The multibyte row carries its own control: before writing, `t.Fatalf` unless `!utf8.RuneStart(capture[stderrFixtureCap])`. Without it the row silently degrades to an ASCII-equivalent case the day someone edits the literal, and it would prove nothing while staying green.

Every assertion is on `len()`, never on rune count — the cap is a byte cap and `truncateString` slices bytes.

Two more assertions in this test, both one line:

- After the write, `rec.ChildOutputCapture` is still the full original. Pins the no-mutation contract from step 2 of the writer.
- **Failure messages report lengths, not contents.** Printing `want`/`got` here dumps up to 8 KiB of capture into the run log, which defeats the field's entire purpose. Report `len(got)`, `len(want)` and at most a short prefix.

### AC 4 — the ban entry

Append to `finOfflineExecBans`, keyed `"inband_bypass_revoke_fixture_test.go"`, with nine names:

```go
"resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
"probeClaudeVersion", "os.Getenv", "os.Environ",
"packageDir", "setModeFixturePath", "writeSetModeFixture",
```

The comment above it must say three things:

1. The first six are the family's standing set — the first four keep a `t.Skip` out (`resolveClaudeBin` and `WithWorktreeAuthenticated` both skip *inside* the test body, after `=== RUN` is printed, so the gate cannot tell the skip from a pass), and the last two are the credential guard.
2. The trio is copied from #1661's entry for the reason that entry states: the check is an AST identifier match, so a file calling `writeSetModeFixture` reaches `packageDir` transitively while never naming it, and a `packageDir`-only entry leaves the ban true and the property false.
3. **What is deliberately absent, and why**, because this is where #1661's entry must *not* be copied: `t.TempDir`, `os.WriteFile`, `os.Create`, `os.ReadFile`, `os.ReadDir` and `filepath.Glob` all stay available — this file's entire subject is a write, a read-back and a directory listing. The relative-path hazard #1661 closes by banning `os.WriteFile` cannot be closed by a ban here; it is closed by AC 2 instead, since a writer that sent its bytes to a relative `testdata/` leaves the `t.TempDir()` holding zero entries.

### Line budget

The nearest analogues landed at 368 (#1661), 382 (#1364) and 402 (#1651) total insertions. This design is budgeted at ~380 and the boundary is 400. Per section:

| Section | Budget |
|---|---|
| File header | ≤ 40 |
| Imports | ~14 |
| Record type + doc comment | ≤ 60 |
| Writer + `capFixtureCapture` + docs | ≤ 55 |
| Round-trip test (AC 1 + AC 2) | ≤ 115 |
| Non-zero helper | ≤ 25 |
| Cap test (AC 3) | ≤ 60 |
| Ban entry (AC 4) | ≤ 18 |

If the file runs long, **cut prose, not assertions.** The header does not need to re-derive the overwrite hazard — `poolRevokeFixtureName`'s own header carries it; point at the symbol.

---

## Concurrency model

No goroutines, no channels, no context. Both tests take `t.Parallel()` at the top level and each owns its own `t.TempDir()`, so no two tests share a path.

The round-trip test's subtests are deliberately **not** parallel: they read one artifact written by the parent, and sequencing them keeps the parent's `t.TempDir()` lifetime obvious. Nothing here ranges `poolRevokeArms`, so the read-only-table hazard #1651's header describes does not arise; if a future edit does range it, it must range it read-only.

---

## Error handling

Two distinct classes, and the split matters:

- **Broken instrument → `t.Fatalf`.** A mkdir, marshal, write, rename, read or decode failure means the test measured nothing. All live in the writer or immediately around the read-back, all prefixed `#1662:`, all naming the arm and the error and **nothing else**.
- **A property that does not hold → `t.Errorf`.** Every table row reports and continues, so a run surfaces all failing fields at once. A record with three dropped fields is not one edit from clean, and the reader deserves to know that before starting — the same reasoning `TestFinOfflineFilesReachNoExecHelper` gives for reporting every offending call site.

The multibyte row's vacuity control is the one exception on the `Errorf` side: it is `t.Fatalf`, because a row that cannot discriminate is a broken instrument, not a failed property.

---

## Testing strategy

Both tests are deterministic, offline, and settle with no claude binary and no credentials:

```
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestPoolRevokeFixture_|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/
```

All three must report **PASS** — not SKIP, not "no tests to run". Read the count of `=== RUN` lines, never the exit code: this package is behind the `e2e_realclaude` build tag, `make check` never compiles it, and the suite exits 0 both on a build failure and on a full credentials skip.

While developing, `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` is the cheap compile check. The suite target is `make e2e-realclaude`.

**What each assertion is the sole red for** — the check that the file carries no dead weight:

| Assertion | Sole red for |
|---|---|
| non-zero block | a field left at its zero value, which would make its round-trip row vacuous |
| round-trip block | a tag collision (both fields drop), a `json:"-"`, a field decoded from the wrong tag |
| distinctness block | two same-typed fields carrying the same value, so each could satisfy the other's row |
| exactly-one-entry | a stranded `.tmp`, a stray file, a write that escaped to the real `testdata/` |
| entry-name-equals-namer | a writer that formats its own name instead of minting through `poolRevokeFixtureName` — non-vacuous only because the fixture arm needs slugging |
| `over_cap_ascii` | a writer that leaves the cap to its caller |
| `under_cap` | a writer that truncates unconditionally |
| `over_cap_multibyte` | a byte cap with no rune-boundary trim, whose read-back exceeds the cap |
| no-mutation | a writer that caps the caller's record in place |
| ban entry | a future edit reaching a skip helper or the committed `testdata/` |

**Not asserted, deliberately:** a misspelled JSON tag. The decode goes back through the struct that wrote the file, so it is symmetric and cannot catch one. #1651 shipped a literal-key decode for exactly that reason; here the field names are read by humans and by future re-measurement rather than by a production decoder, so a misspelling is cosmetic. The file header must not claim otherwise.

---

## Open questions

1. **Field 13's type.** Spec'd as `[]string` (raw lines) rather than `setModeFixtureRecord`'s `[]json.RawMessage` + non-JSON counter, because the eighteen-field bound leaves no room for the counter and a dropped non-JSON line is worse than an untyped one. If #1643 finds it needs the events pre-decoded, that is #1643's local decode, not a nineteenth field here.
2. **Field 16's element shape.** `[]string` of formatted log records. #1643 will capture them through an `slog` handler; whether it stores `record.Message` plus attrs, or a formatted line, is #1643's call. This ticket only fixes the type.
3. **Should the writer refuse a `dir` outside the test's tempdir?** No — #1643 legitimately passes `packageDir(t)/testdata`. The containment property that matters is the *name*, which #1661 already proved and AC 2 re-asserts through the write path. Recorded here so it is a decision rather than an omission.

---

## Security review

**Verdict:** PASS (after one MUST FIX found during the pass and revised into § Design before this verdict — see [Error messages] below).

**Findings:**

- **[Trust boundaries]** One boundary, and it is explicit: `poolRevokeFixtureName` is the sole place a caller-supplied `arm` and `versionToken` become a filesystem name. The writer passes both through unmodified and joins the result under a caller-supplied `dir`; #1661 already proved the minted name is a single clean path component for hostile inputs including `..`, `../..`, `/abs` and `a/b`. The residual risk is a *future* writer that formats its own name and reopens traversal — closed by AC 2's name-equals-namer assertion, which is non-vacuous only because the fixture arm carries a character `versionSlug` rewrites. That non-vacuity requirement is now mandated in § Design rather than left to the developer's choice of literal.

- **[Tokens, secrets, credentials]** MUST-carry, addressed: the type's doc comment inherits `setModeFixtureRecord`'s constraint — **no `env` field**, because the credential reaches the child through the environment while the argv carries none. The record is generated by no RNG, stores no token, and has no lifecycle. `os.Getenv` and `os.Environ` are banned in the file by AC 4, so the constraint has a deterministic enforcer and not just prose.

- **[Tokens, secrets, credentials — bound]** SHOULD FIX, addressed in § Design. A plain byte cap over a multi-byte capture does **not** hold the bound it states: `encoding/json` substitutes U+FFFD for each invalid byte, so a capture cut mid-rune reads back at up to `cap + 6` (measured: a 5-byte cut string reads back at 7). Bounded, so not a breach — but a stated-and-false invariant is exactly what this pass exists to catch. `capFixtureCapture`'s rune-boundary trim makes "at most the cap" true for every input and "exactly the cap" true whenever the byte at the cap is a rune start.

- **[Tokens, secrets, credentials — uncapped siblings]** SHOULD FIX, carried into the doc comment. Fields 11, 13 and 16 are uncapped, and `StdoutLines` in particular is free text from the child. Capping them would destroy the probe's evidence, which is why `setModeFixtureRecord` ships `StdoutEvents` uncapped too — so this is inherited, not new. The mitigation is a constraint on the consumer, stated in the type's doc comment: field 18 is the field designed to absorb unbounded child output, and #1643 must route a free-text failure stream there rather than into `StdoutLines`. This ticket fills no field, so nothing is exploitable as designed.

- **[File operations]** No findings. Path traversal is closed at the namer (above). Atomic write is preserved — temp-file-plus-rename with the predictable `path + ".tmp"` name `writeSetModeFixture` uses, so an interrupted run cannot strand a half-written fixture for a later commit, and AC 2's exactly-one-entry assertion is what proves the rename happened. File mode is `0o644` for both the temp file and the final file, matching both existing writers: there is no window at looser permissions, and `0o644` is the correct mode for a file whose whole design is to be committed. `0o600` would be wrong here and would signal a secrecy the content deliberately does not have. TOCTOU between `MkdirAll` and `WriteFile` is not reachable — every call site in this ticket passes a `t.TempDir()`, and #1643's `packageDir(t)/testdata` is a repo path, not an attacker-writable one. No symlink handling is needed for the same reason.

- **[Subprocess / external command execution]** Not applicable, and enforced rather than asserted: the file execs nothing, spawns nothing and reads no environment. `TestFinOfflineFilesReachNoExecHelper` parses this file's AST — without `parser.ParseComments`, so the check cannot satisfy itself out of the header that names the banned symbols — and AC 4's nine-name entry is what makes the claim structural.

- **[Cryptographic primitives]** Not applicable. No randomness, no hashing, no key material, no comparison against a secret. Filenames are derived deterministically from the version token and arm; nothing here needs to be unguessable.

- **[Network & I/O]** No findings on the network side (there is none). On the I/O side the relevant cap is field 18's, addressed above. The read-back reads a file this test just wrote into its own tempdir, so there is no untrusted-input size to cap on the read path.

- **[Error messages, logs, telemetry]** **MUST FIX — found during this pass, revised into § Design before this verdict.** The draft left failure-message content unspecified. Two concrete leaks follow from that: a `t.Fatalf("%+v", rec)` in the writer dumps the whole record — including up to 8 KiB of child output — into a run log the dispatcher captures, and the cap test's own `t.Errorf` naturally wants to print `want` and `got`, which are 8–9 KiB strings by construction. Both would move the exact bytes the cap exists to bound out of the bounded file and into an unbounded log. § Design now mandates: writer fatals name the arm and the error only; cap-test failures report `len()` and at most a short prefix. Re-walked after the revision — no further finding in this category. No telemetry, no metrics, no user-identifiable data.

- **[Concurrency]** No findings. No goroutines, no channels, no shared mutable state, no locks — so no lock ordering, no TOCTOU on shared state, no leak. Each test owns its own `t.TempDir()`, so two `t.Parallel()` tests cannot collide on a path. `poolRevokeArms` is not ranged by this file at all, which sidesteps the read-only-table hazard rather than relying on it. Shutdown mid-write is the temp-file-plus-rename case, covered above.

- **[Threat model alignment]** The threat this ticket sits under is not the mobile-relay one; it is durable-evidence corruption — a live run silently overwriting committed fixtures, which #1661's namer closes and AC 2 re-proves through the write path. Out of scope and named as such: the live probe's own credential handling and its choice of what to put in each field, which belong to #1643; and any cap on fields 11/13/16, which is a consumer constraint recorded in the doc comment rather than code here.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-20
