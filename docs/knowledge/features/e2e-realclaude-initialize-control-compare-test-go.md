# initialize_control_compare_test.go
- `initialize_control_compare_test.go` (#1764) — **the cross-arm read: the
  two arms that send the `initialize` control request compared against the
  arm that sends none, at the same turn index, over #1763's three committed
  captures.** `initControlDiscoverArms` globs `initControlArmFixtureGlob`
  (`testdata/initialize_control_v*_*.json`) rather than addressing the
  family by exact name — exact-name addressing would need a hard-coded
  version token and would make an undeclared `arm` structurally
  unreachable — decodes each match through `initControlFixtureRecord`,
  binds each base name to `initControlArmFixtureName(rec.ClaudeVersion,
  rec.Arm)` by string equality (never a `filepath.Join` on a
  fixture-supplied value), groups by `ClaudeVersion`, and — since #2131 —
  hands the grouped map to `initControlSelectArmGroup`, which picks the
  highest version under `update.CompareVersions`' numeric ordering rather
  than refusing outright when the glob matches more than one group. A
  routine `claude` upgrade on the gate host leaves the committed capture
  beside a freshly written one; comparing the newest rather than refusing
  is what keeps that from reddening every full `make e2e-realclaude` run.
  Selection still refuses to guess: a token `update.CompareVersions` can't
  parse, two distinct names that order `Same` (it tolerates a leading `v`
  and strips a `-`/`+` suffix, so `2.1.239`, `v2.1.239` and
  `2.1.239-beta.1` are three group keys with no ranking between them), or
  a selected group missing an id `initControlArms` declares all fail the
  run by name, with no fallback to an older complete group. The literal
  `_` after the version segment is the entire
  exclusion: `filepath.Match` needs one, #1688's legacy
  `initialize_control_v2.1.239.json` has none, and nothing mints an
  unarmed name any more.

  `initControlTurnReads` slices each arm's `StdoutEvents` at its
  `TurnBoundaries` (mirroring `setModeTurnWindows`'s range guard and its
  unclosed-trailing-window rule) and `initControlReadTurn` reduces each
  window, through `initControlWindowField` verbatim, to the seven-field
  `initControlTurnRead`. Every field but one is constant across all three
  arms at both indices on today's fixtures — the drive sequence is
  tool-free — which is why the field set isn't `probeOutcome`'s:
  `ControlResponses`, whether a `control_response` line fell inside that
  turn's own window, is the sole structural discriminator, fixed by each
  arm's own send point, and it is what makes a wrong-index or wrong-window
  slice observable instead of silently agreeing. `initControlTurnRows`
  reports every field with "agrees"/"differs" as a first-class outcome
  rather than a missing result, checked against
  `reflect.TypeOf(initControlTurnRead{}).NumField()` so a field added to
  the struct and forgotten in the row builder can't go silently
  uncompared. On today's `2.1.239` bytes, `control_responses` is the only
  field that ever differs — `before_first_turn` at turn 0,
  `after_completed_turn` at turn 1 — everything else agrees at both
  indices for both arms.

  The verdict is reported; what
  `TestInitControlArms_CompareMeasurementArmsAgainstTheControlAtTheSameTurnIndex`
  asserts is the instrument. `initControlTurnReadAt` returns `(read, ok)`
  rather than zero-filling an out-of-range index — the one place this file
  diverges from `setModeOutcomeAt` — and liveness is keyed on
  `ResultIsError`/`ResultTerminalReason`, never `ResultSubtype`:
  `400db2d1`'s 401 capture recorded `subtype: "success"` with
  `num_turns: 1` on turns that never reached the model. Registered in
  `finOfflineExecBans` with fifteen names, derived from
  `initialize_control_window_test.go`'s seventeen by dropping
  `packageDir`, `filepath.Glob` and `os.ReadFile` — the first file in this
  family with a legitimate reason to read the committed fixtures — while
  every write name stays banned, since this file reads and must never
  write.

  Adding this glob made three shipped claims false, corrected in place
  rather than left to rot: `initialize_control_names_test.go`'s file
  header now says the family is swept by three *foreign* globs plus its
  own; the "no minted name collides with the committed one-arm capture"
  subtest's comment now names `initControlArmFixtureGlob` as the pattern
  that matches these names on purpose; and
  `initialize_control_probe_test.go`'s write-set `t.Errorf` string no
  longer claims no glob in this package matches the family.

  **Lessons that outlive this ticket:**
  - **"Borrow the shape" can mean borrowing the wrong return type along
    with it.** `setModeOutcomeAt`'s out-of-range zero value is safe for
    its own caller, but ported into a comparison whose verdict is
    agreement, two arms that both ran short would zero-fill and compare
    `equal()` — agreement computed out of absence. The fix is a one-line
    signature change (`(read, bool)`); finding it needed reading what the
    sibling's zero value means to *its* caller, not just what type it
    returns.
  - **The obvious liveness field can be the one the rejected artifact
    fakes.** `subtype` and `num_turns` are the fields a reader reaches for
    first, and both read healthy on `400db2d1`'s 401 capture — only
    `is_error` and `terminal_reason` separated it from a live run. Before
    picking a liveness key, check it against the bytes of the capture the
    clause was written to reject, not only against a healthy one.
  - **A name↔record binding can retire a whole failure-mode branch as
    unreachable.** Binding each fixture's base name to
    `initControlArmFixtureName(rec.ClaudeVersion, rec.Arm)` makes "the
    same arm twice for one version" require two directory entries with one
    name, so the presence check alone is the entire "exactly once"
    property — a dedicated duplicate-detection branch would have been a
    return site nothing on disk can reach.
  - **`update.CompareVersions` returns `Same` for two names that are
    different files (#2131).** It tolerates a leading `v` and strips a
    `-`/`+` suffix before parsing, so `2.1.239`, `v2.1.239` and
    `2.1.239-beta.1` are three distinct fixture-group keys it ranks as
    equal. A plain highest-wins loop over this comparator picks an
    arbitrary one of two coherent sets — the two-version defect wearing a
    different token — so any "pick the newest" rule built on it needs its
    own tie branch rather than trusting strict ordering to cover every
    pair.
  - **The capture-then-compare ordering here is a `t.Parallel()`
    consequence, not luck (#2131).** The capture probe
    (`TestRealClaude_InitializeControl_SendPointArms`) never calls
    `t.Parallel()` and writes its three arms in sequential subtests; the
    comparison test does call it, so Go pauses it until the package's
    whole sequential pass finishes, and a half-written group is never
    reachable by construction. That is also why the two-version fixture
    conflict this file's selection logic now resolves reproduced on
    *every* full run rather than intermittently — a symptom worth reading
    as "deterministic ordering, not a race" before chasing one.

  Zero production files touched. See
  `docs/specs/architecture/1764-initialize-arm-comparison.md` for the full
  design and security review, and
  `docs/specs/architecture/2131-initialize-control-arm-group-selection.md`
  for the version-selection rule added on top of it.
