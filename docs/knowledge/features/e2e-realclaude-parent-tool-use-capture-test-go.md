## parent_tool_use_capture_test.go (#2191)

The live half of `parent_tool_use_id`: stages a prompt that asks claude to delegate two
independent reads to a subagent via the Agent/Task tool, records every line of that turn, and
writes `testdata/parent_tool_use_v<claude version>.json` for the hermetic replay in
`internal/streamsup/parent_tool_use_capture_test.go`. Reuses `dropcapRecorder` /
`dropcapRedactor` / `dropcapScanner` / `dropcapMakeEntry` entire, `--allowed-tools` omitted so
the Agent tool is reachable (`ask_user_question_capture_test.go`'s argv pattern), and arms on
the fixture's absence rather than a `PYRY_PROBE_*` variable, `compaction_capture_test.go`'s gate
reasoning. Deliberately does **not** inherit `tpcapHoldFIFO` — nothing here holds a call open.

### The model choice decides whether the capture is vacuous at all

Every sibling probe in this family runs haiku. This one runs sonnet, named at `ptucModel`. The
turn's whole premise is that claude *delegates* two reads rather than doing them inline, and a
model willing to just do the trivial reads itself produces a green run — every assertion that
doesn't need a subagent still passes — with no subagent spawn in it anywhere. That outcome is
indistinguishable from a real finding about `parent_tool_use_id` unless someone thinks to check
`nested_spawn_observed` in the record. A capture probe whose positive evidence depends on a
model *choosing* a particular strategy needs a model likely to choose it, not the cheapest model
that can follow the instruction — the same trap a too-weak model creates for any eval, arriving
here as a false negative in a live gate rather than a bad score.

### A capture probe in this package is a ~900-1200 line artefact, not a small one

`parent_tool_use_capture_test.go` is 911 lines — smaller than both `compaction_capture_test.go`
(1100) and `tool_progress_capture_test.go` (1174), at comparable comment density, and it reuses
the shared rig rather than rebuilding it. The four offline guards inside it — an argv
both-spellings-of-a-flag table, a three-spellings-of-absent line reader, `fixtureWorthy`'s eight
refusal arms, and a three-exit turn-completion wait — are what stop a vacuous capture from
promoting, the same shape `compaction_capture_test.go`'s guards take. **A future ticket sizing a
live-capture probe in this package should start from this file, `compaction_capture_test.go`, or
`tool_progress_capture_test.go` — never from a ticket whose fixture was synthesized instead of
captured** (#2224 is the standing trap: it shipped no probe at all, so pricing a new probe's
line count from it prices the probe at zero).

### Fixture status as of #2191's landing

**Not committed.** `internal/streamsup/parent_tool_use_capture_test.go`'s `parentReaderGate`
reads `(fixture absent, pin empty)` — the one legal skip in its four-state machine — and
`parentPinnedAgentID` is still `""`. The live run needs a `needs-real-claude` gate lap to fire
the probe from a checkout that can actually `git add` the result; #2229's precedent
([`compaction_capture_test.go`](e2e-realclaude-compaction-capture-test-go.md)) is that a
gate-only lap verifying from a detached, discarded worktree can produce and lose the bytes in
the same run, needing a later repair-leg commit (or an opportunistic commit riding an unrelated
ticket, as #2236 did) to actually land them. Check `internal/e2e/realclaude/testdata/` for
`parent_tool_use_v*.json` before assuming the replay in `internal/streamsup` runs rather than
skips.

### Related

- [`compaction_capture_test.go`](e2e-realclaude-compaction-capture-test-go.md) — the
  fixture-absence gate and four-state reader-gate shape this probe copies, and the fuller history
  of a gate-only lap losing a fixture it had already captured.
- [`tool_progress_capture_test.go`](e2e-realclaude-tool-progress-capture-test-go.md) — the other
  ~1100+-line probe in this package, and the fixture whose 12 non-null `parent_tool_use_id`
  values are the *other* meaning of the key — see
  [streamsup's tool_progress doc](streamsup-package-tool-progress-consumed-by-matching.md).
- [streamsup's decode-target family doc](streamsup-package-result-stop-shape-second-decode-target-and-dr.md)
  — where the captured bytes are read: the `assistantParentLine`/widened-`userLine` split and the
  `maxTaskFieldID` join-key bound.
