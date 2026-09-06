# #2131 — the initialize-control arm comparison selects its own version group

The cross-arm comparison stops refusing to run when two claude versions are on
disk and instead compares the newest complete group, so a routine `claude`
upgrade on the gate host stops reddening `make e2e-realclaude` and
mis-attributing the red to whichever branch happens to be under test.

## Files read

- `internal/e2e/realclaude/initialize_control_compare_test.go` →
  `initControlDiscoverArms` — the whole subject: it globs the arm fixtures,
  groups them by `ClaudeVersion`, and today appends a problem the moment the
  glob matches more than one group. Its doc comment carries the paragraph
  declining "compare the newest", which this ticket replaces.
- same file → `initControlArmFixtureGlob` — the `_` after the version segment is
  the structural exclusion of #1688's one-arm capture; grouping does not
  separate it and must not be asked to.
- same file →
  `TestInitControlArms_CompareMeasurementArmsAgainstTheControlAtTheSameTurnIndex`
  — the sole caller of `initControlDiscoverArms`, and the test whose PASS the
  first two ACs are about.
- same file → `initControlControlArmID` — the "FROM THE TABLE, never from a
  literal" discipline the new offline table copies for its arm ids.
- `internal/update/version.go` → `CompareVersions`, `ErrInvalidVersion` — the
  numeric ordering AC 3 demands, and the unparseable-token branch already built.
  Tolerates a leading `v` and strips a `-`/`+` suffix before parsing, which is
  the source of the equal-ordering hazard named under Error handling below.
- `internal/e2e/realclaude/initialize_control_names_test.go` →
  `initControlArms`, `initControlArmFixtureName` — the declared arm vocabulary
  (three ids, one of them the control) and the namer that binds a record's
  version token to its file name.
- `internal/e2e/realclaude/permission_protocol_spike_test.go` → `versionSlug`,
  `captureClaudeVersion` — `versionSlug`'s character class `[^a-z0-9._-]+` is
  why `2_1_220` and `permission_protocol` are admissible file-name segments and
  therefore admissible group keys; `captureClaudeVersion` is what writes the
  token into a record at capture time and is banned from this file.
- `internal/e2e/realclaude/initialize_control_record_test.go` →
  `initControlFixtureRecord` — `ClaudeVersion` is the grouping key
  (`"2.1.239"`, the leading whitespace-split token), distinct from
  `claude_version_raw` (`"2.1.239 (Claude Code)"`).
- `internal/e2e/realclaude/offline_exec_ban_test.go` →
  `finOfflineExecBans`'s `initialize_control_compare_test.go` entry,
  `TestFinOfflineFilesReachNoExecHelper` — the scan is a per-file AST denylist
  over identifiers and selectors with comments deliberately unparsed. It
  constrains what the new code may CALL, not what the file may import; nothing
  this ticket adds is on the list. Keeping the change inside this one file is
  what avoids minting a new per-file entry.
- `docs/knowledge/features/e2e-realclaude-initialize-control-compare-test-go.md`
  — states that discovery "groups by `ClaudeVersion` and fails on anything but
  exactly one group". **This ticket falsifies that sentence.** Named here for
  the documentation phase; not edited by this ticket.
- `CODING-STYLE.md` § Comments — Citing Other Code; § Testing — table-driven,
  `t.Parallel()`, stdlib only.

## Context

`TestInitControlArms_CompareMeasurementArmsAgainstTheControlAtTheSameTurnIndex`
fails on every full `make e2e-realclaude` run on the gate host. Nothing in the
comparison path changed: the host's claude moved 2.1.239 → 2.1.259, the sibling
capture probe writes `testdata/initialize_control_v<live-version>_<arm>.json`
into the package's own source directory at run time, and the committed set is
`v2.1.239_*`. Both sets are then on disk, `initControlArmFixtureGlob` matches
two version tokens, and `initControlDiscoverArms` refuses to compare across
them. A pipeline worktree is discarded afterwards, so nothing is self-healing
and the red reproduces every run.

The refusal was the right call when it was written and its own doc comment says
why it stopped there: "these tokens have no total order without a semver
parser, and a wrong order silently compares the stale set, which is the one
outcome worse than a red". The premise is what changed — `update.CompareVersions`
is that parser, and it is in this repo. So the fix goes in the selection rather
than in the fixtures: refreshing the committed captures unblocks only until the
next upgrade, and those bytes come from a live run whose worktree is thrown
away. Teaching the comparison to read the newest group unblocks the same run,
removes the recurrence, and is provable offline.

Two things stay out of scope and are recorded here rather than acted on. The
dispatcher half — `decideRealClaudeGateRun`'s base leg re-runs only the failing
test against `origin/main`, so a cross-test-fixture failure can never be
exonerated as `inherited-failure` — belongs to the agents repo. And refreshing
the committed `2.1.239` captures to the host's version buys the gate nothing
once this lands; it is an operator task, not a prerequisite.

No ADR is warranted. This is one function's selection rule inside one
build-tagged test file, and the reasoning that would fill an ADR is exactly what
the failure message asked to be recorded in the doc comment.

## Design

One new pure function in `initialize_control_compare_test.go`, one rewritten doc
paragraph, one changed block in `initControlDiscoverArms`, and one new offline
table test. No new file, so no new `finOfflineExecBans` entry, so nothing has to
be composed from a sibling entry that is per-file by design.

### The selection is a pure function over the already-grouped map

```go
func initControlSelectArmGroup(
    byVersion map[string]map[string]*initControlFixtureRecord,
) (selected string, ignored []string, problems []string)
```

No `*testing.T`, no I/O in either direction, no `filepath` and no `os`. It reads
only the map's keys and the inner maps' arm keys — never a record's contents —
so the offline table below can build its input from arm-id lists with nil record
pointers and reach every branch without writing a byte into `testdata/`. That is
the whole reason the selection is a function over the grouped map rather than
something reachable only through the glob: this file's `finOfflineExecBans`
entry keeps every write name banned, and it must stay that way.

It returns problem strings rather than an error or a `t.Fatalf`, matching
`initControlDiscoverArms`' existing accumulate-then-one-`Fatalf` idiom
(`assertRegressionFixture`'s). One run names every broken group rather than the
first.

Behaviour, in order:

1. An empty map returns one problem. Unreachable from the caller — the caller
   has already failed on an empty glob and every match either adds a problem or
   a group — but the function is total over its input and the table covers the
   row, the same way `initControlTurnReads`' range guard is load-bearing rather
   than stylistic.
2. Keys are collected and `sort.Strings`-ed for a deterministic listing order.
   That order is the reporting order, explicitly **not** the ranking — AC 3
   exists because the two are different.
3. One pass picks the maximum under `update.CompareVersions`. Each token is
   ordered against the incumbent, or against itself when there is no incumbent
   yet: either call parses the token, so one error branch covers both "does not
   parse" and "cannot be ordered", and both are reachable.
4. Any problem from the ordering pass short-circuits: the function returns no
   selection. A set containing a token nothing can order is never silently
   ordered, dropped, or ignored.
5. The selected group is then checked for every id `initControlArms` declares.
   A missing arm is a problem naming the version and the arm, and the function
   returns no selection — there is no fallback to an older complete group by
   construction, because completeness is checked after selection and never
   feeds back into it.
6. `ignored` is every other key in the sorted listing order.

### The caller changes in one place

In `initControlDiscoverArms`, the `versions` slice, its `sort.Strings`, the
`len(versions) > 1` problem and the completeness loop over `versions[0]` are all
replaced by a call to the new function, gated behind `len(problems) == 0`
exactly as the completeness loop is today — a fixture whose decode or name-bind
failed leaves a group of unknown shape, and selecting a winner out of the
remainder would report a fresh-looking verdict off a set the run cannot vouch
for. The two returns become `selected` and `byVersion[selected]`.

One `t.Logf` before the return names the group compared and the group(s)
ignored, satisfying AC 1's second half. It fires on every run, not only when
something was ignored: a one-group run that says so is what makes the
two-group line legible when it appears.

### The doc comment is where the reasoning is recorded

`initControlDiscoverArms`' "WHICH VERSION TO COMPARE WHEN SEVERAL ARE PRESENT:
none. The run fails." paragraph is replaced by the new rule and the reason the
old one no longer holds: the parser the old paragraph said did not exist is
`update.CompareVersions`, the stale-set hazard it feared is closed by ordering
numerically rather than lexically, and the incomplete-newest-group branch is
what keeps a fresh-looking verdict off stale bytes. The old failure message
invited exactly this ("teach this test which to compare and say why"); the doc
comment is the "why".

## Concurrency model

None added. `initControlSelectArmGroup` is pure and holds no state. Both the
existing comparison test and the new table test call `t.Parallel()`; the new one
touches no shared mutable state, ranges `initControlArms` read-only (that
declaration's stated contract), and reaches no file system. `-race` clean by
construction.

## Error handling

Every rejection is a problem string joined into `initControlDiscoverArms`' one
`t.Fatalf`, or — in the table test — an expectation on the returned slice. Four
kinds:

- **Unorderable token.** `update.CompareVersions` returns a value wrapping
  `ErrInvalidVersion`; the problem names the offending token and the error.
  `versionSlug` admits `2_1_220` and `permission_protocol` as file-name
  segments, so such a token names a fixture that looks like every other one.
- **Two tokens that order equal.** `CompareVersions` tolerates a leading `v` and
  strips a `-`/`+` suffix before parsing, so `2.1.239` and `v2.1.239`, or
  `2.1.239` and `2.1.239-beta.1`, are distinct group keys that compare `Same`.
  Neither is newer, so a silent first-wins would compare an arbitrary one of two
  coherent sets — the original defect wearing a different token. It fails,
  naming both.
- **Incomplete selected group.** Named per missing arm, with no fallback. An
  incomplete newest set means the capture run in this same suite went wrong.
- **Empty group map.** The totality guard of point 1 above.

Nothing here panics and nothing returns an error value: the caller's contract is
a problem list, and `t.Fatalf` is the only exit.

## Testing strategy

**RED first, for the right reason.** The new function lands as a stub returning
zero values, the table test runs against it and fails on its assertions — not on
a compile error — and only then is the body written.

`TestInitControlSelectArmGroup_PicksTheNewestCompleteVersionGroup`: table-driven,
`t.Parallel()`, offline, one row per branch. Each row gives version → arm-id
list, and expects a selected version, an ignored list (`reflect.DeepEqual`), a
problem count, and a substring per expected problem. Arm ids come from
`initControlArms` rather than from literals, copying `initControlControlArmID`'s
rule, so an arm rename moves the table with the vocabulary instead of leaving it
asserting a name nothing mints.

Rows:

- one complete group → selected, nothing ignored, no problems
- two complete groups → the newer selected, the older ignored (AC 1)
- `2.1.99` against `2.1.239` → `2.1.239` selected, which a string sort gets
  wrong (AC 3)
- three complete groups → the newest selected, the other two ignored in listing
  order
- an unparseable token beside a good one → no selection, the token named (AC 3)
- a single `2_1_220`-shaped token → no selection, the token named (AC 3)
- every token unparseable → every token named, not just the first
- two tokens that order equal → no selection, both named
- newest group missing the control arm, older group complete → no selection,
  version and arm named, and the older group explicitly not selected (AC 4)
- newest group missing two arms → both named
- empty map → the totality guard

**AC 2** — PASS not SKIP with only the committed set, on a machine with no
claude and no credentials — is the existing invocation from the file's header,
run as-is:

```
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestInitControlArms_CompareMeasurementArmsAgainstTheControl|TestInitControlSelectArmGroup|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/
```

Read the count of tests that executed, never the exit code: this package is
behind the `e2e_realclaude` tag and the suite exits 0 both on a build failure
and on a full credentials skip.

**AC 1's end-to-end half** is verified locally by copying the committed
`v2.1.239_*` arm fixtures to `v2.1.259_*` names with their `claude_version`
field rewritten, running the comparison test, and deleting the copies in the
same step. Those bytes are never committed — the ticket says the committed set
is neither deleted nor replaced, and a fabricated second capture is not a
fixture this repo should carry. The pure-function rows above are the durable
form of the same property.

`make check` never compiles this package and says nothing about it.

## Open questions

- Whether `ignored` should be ranked newest-first rather than listed in the
  sorted-key order. Resolved in the design as listing order, with the doc saying
  so, because a ranking implies the ignored groups were ordered against each
  other and only the winner actually was.
- Whether the equal-ordering tie deserves its own branch or should fall through
  to first-wins. Resolved as its own branch: `CompareVersions` strips a leading
  `v` and a pre-release suffix, so two distinct file-name groups really can
  order `Same`, and first-wins there is the original defect in new clothes.

## Revisions

### 2026-09-06 — the probe/compare ordering, checked rather than assumed

The design above says "compare the newest group, the one the probe just
captured" and the Concurrency model section says nothing is added. Both hold,
but AC 1 rests on an ordering the plan asserted without checking: that the
comparison never globs a half-written group while the capture probe is still
writing its three arms one file at a time.

Checked, and it holds by construction rather than by luck.
`TestRealClaude_InitializeControl_SendPointArms` calls `t.Parallel()` nowhere,
at any level — its own doc says so, and the three arms are sequential `t.Run`s
that each leave their fixture on disk before returning. The comparison test does
call `t.Parallel()`, so it is paused until the package's whole sequential pass
has finished. The capture is therefore complete before the glob runs, and a
partial group is not reachable.

That also explains a property of the defect this ticket fixes: the two-version
red reproduced on *every* full run rather than intermittently, which a genuine
write/read race would not have done.

No design change follows. The incomplete-group branch keeps its purpose — a
capture run that genuinely went wrong, which is a different thing from one still
in progress.
