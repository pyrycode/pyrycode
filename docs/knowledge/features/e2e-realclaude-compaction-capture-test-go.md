## compaction_capture_test.go (#2229)

The live half of a still-unbuilt compaction mapping: stages a compactable conversation (two
rig-authored priming turns, no tools), sends `/compact` as ordinary message text, and records
**every** line of that turn — see
[streamsup's per-subtype map](streamsup-package-system-maps-per-subtype-since-2026-08-07.md) for
what the capture found. Its hermetic sibling in `internal/streamsup` pins the observed
`type`/`subtype` set inside `make check` via a fixture-exists/pin-filled state machine with exactly
one legal skip (absent fixture, empty pin) and a fatal on every other combination — the same shape
[`tool_progress_capture_test.go`](e2e-realclaude-tool-progress-capture-test-go.md) uses, without an
`fs.ErrNotExist` skip branch to delete later.

### Classify by JSON marker keys, never by envelope

This package's committed testdata carries 114 occurrences of the stem `compact`, and every one is a
slash-command **name** inside an `init` line's `slash_commands` inventory. A classifier keyed on
that stem, or on the envelope (`type`/`subtype` alone, which is exactly what's undetermined), would
flag the first line of every turn in the package. `ccapClassify` instead keys on marker JSON *keys*
(`compact_boundary`, `compact_metadata`, `compact_result`, `compact_error`, `pre_tokens`,
`post_tokens`) at any depth, or a `status` key whose *value* is `"compacting"` — proved offline
against that inventory before any live run.

### A gate-only live run can promote a fixture and still lose it

The live run on 2026-09-08 fired: `outcome=fired terminated_on=result compaction=3
shapes=[system/compact_boundary system/status]`, confirming the paragraph above, and the probe's own
log line read `FIXTURE WRITTEN to testdata/compaction_v2.1.259.json ... Commit it — git add
testdata/compaction_v2.1.259.json`. **That commit never happened.** The run was the dispatcher's own
automated real-claude gate, executing in a detached, merge-only worktree built to verify and then be
discarded — not a builder's persistent branch checkout. The fixture's write path is in-repo by
design (`ccapFixturePath` is a compile-time constant, not `os.MkdirTemp`), which is exactly what
saved #1763's *record*, but the gate that ran this probe never runs `git add`, so an in-repo path
inside a throwaway worktree is lost exactly as completely as a tempdir path would have been. The
gate's own PASS summary reports only executed-test counts; it does not surface a test's `t.Log`
lines, so nothing short of reading the raw JSON log surfaces the lost instruction.

`internal/streamsup/compaction_capture_test.go`'s `compactionPinnedShapes` is still `[]string{}` and
no `testdata/compaction_*.json` is committed as of this writing. **#2227 and #2228 both read this
fixture** (the reuse that made this its own ticket) and cannot proceed from it until either an
operator commits the bytes the 2026-09-08 run already produced, or the probe is re-run from a
worktree whose result actually gets pushed — the fixture's-absence gate (`os.Stat(ccapFixturePath)`)
means a re-run costs nothing but the live-gate lap.

Contrast with [`tool_progress_capture_test.go`](e2e-realclaude-tool-progress-capture-test-go.md)'s
fixture, which is committed: that one landed via a **builder's repair-leg commit**
(`f30bd8d2`) made from a persistent branch checkout, not from a bare verification gate run. A
capture ticket whose only live-firing execution is the automated gate — because the code was right
on the first pass and no repair leg ever touched a real checkout — has no equivalent commit to ride
in on.

### Related

- [streamsup's per-subtype map](streamsup-package-system-maps-per-subtype-since-2026-08-07.md) — the
  mapper this capture's bytes are for, and the confirmed seam.
- [`tool_progress_capture_test.go`](e2e-realclaude-tool-progress-capture-test-go.md) — the
  fixture-absence gate and in-repo promotion pattern this probe copies, and the contrasting case
  where the fixture did land.
