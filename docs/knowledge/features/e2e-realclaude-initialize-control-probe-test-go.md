# initialize_control_probe_test.go
- `initialize_control_probe_test.go` (#1688, extended #1722 and #1763) —
  **the live run family for the `initialize` fixtures: one real child, one
  tool-free probe turn, one `control_request` with subtype `initialize` on
  the held-open stdin, the reply written through #1702's writer into
  `testdata/initialize_control_v2.1.239.json` (committed).** Reuses
  `setModeRecorder`, `setModeWaitFor`, `setModeTurnLine` and
  `setModeResponseIDMatches` from `set_permission_mode_probe_test.go`
  wholesale; ports none of that file's arm/verdict machinery (`probeOutcome`,
  `setModeFieldMatches`, `setModeDirections`) since this run has one arm and
  no control. Measured against claude 2.1.239: `subtype:"success"`, 6
  `models` entries, also carrying `commands` (51, for #1683) and `agents` (6).
  The committed fixture predates #1722's `Arm` field and is **not** renamed
  or reshaped to carry one — #1763's three-arm rig is the live run that
  supersedes this one-arm capture, and it leaves this file untouched rather
  than renaming it: nothing mints its filename any more (see below), so
  byte-identity holds structurally, not by luck.

  **#1763 replaces the single-arm capture with a three-arm one.**
  `runInitControlChild` takes its `arm` as a parameter (see
  `initialize_control_names_test.go` above for the table it ranges) and
  drives one child per row of `initControlArms` through one shared two-turn
  sequence — the send point placed before turn 1, between the two turns, or
  (for `control_no_request`) not written at all, with every arm, control
  included, driving a full second turn afterwards. That "drive a further
  turn on every arm" requirement is not incidental: claude emits its
  `system`/`init` line once per turn rather than once at spawn, so "no
  further `init` line after the request" observed without a following turn
  is empty by construction rather than a measurement — the same reasoning
  `set_permission_mode_probe_test.go`'s control arms already state for
  itself. An unanswered send point (`ControlResponseWithinWait: false`,
  `ControlRequestSent: null` on the no-request arm) is a recorded, passing
  outcome rather than a fatal — `TestRealClaude_InitializeControl_Capture`
  and its `len(rec.ControlResponses) == 0` fatal are deleted along with the
  one-arm run they gated, which is also what removes the two-live-children
  filename race that same test had picked up against this run's
  `after_completed_turn` arm once #1722 pointed both at
  `initControlArmFixtureName`. `TestRealClaude_InitializeControl_SendPointArms`
  is the replacement: one `dropcapScanner` shared across all three arms
  (safe — append-only during construction, read-only after) but a fresh
  `dropcapRedactor` constructed *inside* the per-arm loop (mandatory —
  `dropcapRedactor`'s substitution counters are unlocked and its census
  accumulates across calls, so a shared one would both race under `-race`
  and misattribute one arm's redactions into another arm's committed
  artifact). Each arm's write-set is checked against
  `initControlArmFixtureName(versionToken, arm.id)`, but only when every
  declared arm actually produced a path — a `-run` filter or a mid-loop
  fatal reports the check as unavailable rather than asserting a set claim
  off a partial run, the same `missing`-guard precedent
  `TestRealClaude_SetPermissionMode_InBandProbe` already established for
  this package.

  `initControlChildBudget` rises from 3 to 5 minutes in the same ticket,
  and for a reason that is easy to miss: doubling the drive sequence (two
  turn waits, plus a control wait on a requesting arm) pushed the worst-case
  per-step sum to 225s against a 180s outer deadline — already tripping
  before spawn and startup are even counted. Every per-step wait in this
  family exists so that an absent response reads as absence rather than
  impatience; an outer deadline that can fire first defeats that guarantee
  for exactly the question this run exists to answer (is a pre-turn
  `initialize` ever answered at all?). `initControlArmWaitSum(arm)` is a
  pure helper mirroring the driver's own sequence by hand, and
  `TestInitControlChildBudget_ExceedsEveryArmsPerStepWaitSum` (offline,
  `t.Parallel`, spawns nothing) asserts the deadline dominates it — derived
  entirely from `initControlChildBudget`/`initControlTurnBudget`/
  `initControlControlBudget`, never a literal duration, and genuinely red
  against the pre-raise 3-minute constant.

  `runInitControlChild` used to call `setModeWaitFor(...)` and drop its
  result into a `t.Logf` alone; #1722 kept that log and also recorded the
  result as `ControlResponseWithinWait` on the record, resolving the single
  arm it targeted via `initControlProbedArm` — the one row
  `initControlArms` marked `probed` — rather than a second spelling of the
  identifier, offline-pinned by
  `TestInitControlProbedArm_IsExactlyOneDeclaredNonEmptyArm`. **Both the
  selector and its offline test are gone as of #1763**: `arm` is now a
  parameter the caller supplies directly (see above), so there is no single
  probed row left to resolve and nothing left for that test to pin.
  `ControlResponseWithinWait` itself is unchanged in shape — still recorded
  per arm, still `false` rather than a fourth state when no wait ran at all
  (the `control_no_request` arm).

  **The `control_response` payload nests one level deeper than
  `streamsup/parser.go`'s documented shape accounts for.** That shape records
  `subtype`/`request_id` inverted under `response` relative to the request —
  true, and `initControlSummarize` reads it there — but the actual payload
  (`models`, `commands`, `agents`, `account`, `pid`, …) is nested a further
  level, under `response.response`. `internal/streamsup` never parses past
  `subtype`, so its own documented shape was never wrong; it was just not the
  whole shape a payload-reading caller needs. Any future code that decodes
  this control-reply's payload — #1690's decoder, #1848's model-list
  mapper — reads `response.response`, not `response`. The verbatim capture
  is `initControlFixtureRecord.ControlResponses[0]` in the committed fixture.

  **Lessons #1763 adds, on top of the two below:**
  - **A doc comment naming another test as precedent is a citation
    `cite-guard` cannot see, and it goes stale exactly when that precedent
    is deleted.** `TestInitControlScanApplied_RecordsAnArmedNothingClassForAnAbsentPath`'s
    own doc pointed at `TestInitControlProbedArm_IsExactlyOneDeclaredNonEmptyArm`
    as the precedent for an offline test living in this exec-ing file.
    #1763 deleted that precedent along with `probed` and left the pointer
    standing — `cite-guard` only resolves `file.go:NNN`-shaped citations, so
    a bare identifier named in prose passes it clean. What caught it was
    grepping the deleted identifiers across `internal/` by hand after the
    edit, not the build. A symbol named in prose as a precedent or a
    template needs the same sweep a `//`-cited one gets automatically;
    nothing enforces it for a bare name.
  - **Nothing couples a hand-derived budget sum to the sequence it
    describes, so the test proving it needs more than the one subject
    assertion to mean anything.** `initControlArmWaitSum` mirrors
    `runInitControlChild`'s drive sequence by hand — two turn waits, plus a
    control wait on an arm that sends a request — and a future turn added
    to the driver without a matching edit to the sum would leave
    `TestInitControlChildBudget_ExceedsEveryArmsPerStepWaitSum` green over
    an arithmetic that no longer describes the run. That is why the test
    carries two independent vacuity controls rather than the subject alone:
    mutation-tested, each is the *sole* red for a different degenerate
    helper (one that drops the control term, one that ignores
    `sendsRequest`) the subject assertion alone would miss.
  - **A per-step budget raise can silently approach `go test`'s own
    default per-binary timeout, which produces no recorded outcome at
    all.** The 3m→5m raise puts this file's worst case at 3×300s = 15
    minutes; `go test`'s default 10-minute timeout is not overridden by
    `make e2e-realclaude`, only by the dispatcher's own invocation (`-timeout
    20m`). A binary killed by that timeout panics and records nothing — no
    artifact, no `context_deadline_tripped` — which is the one outcome this
    family's per-arm budget guarantee cannot cover. Not a defect as shipped
    (#1688's real child completed in 3.3s, and the sibling
    `set_permission_mode` family already carries a larger worst case — 4
    arms × 4m — under the same Makefile target), but the next raise to
    either family's budgets should check the invocation's own timeout, not
    just the per-step sum.

  **Two lessons that outlive this ticket:**
  - **A "check both placements" instruction, derived correctly from one
    known fact, can still be one level short — and the computed field built
    on top of it will report the wrong answer while looking internally
    consistent.** The spec derived two placements (top level, under
    `response`) from `streamsup`'s documented `subtype`/`request_id` shape.
    The first live run of this file read only those two, and recorded
    `models_present:false` against a reply that carried six models — a
    `false` that had nothing pointing back at it, because the run had
    otherwise passed cleanly (a `control_response` arrived, stdout was
    non-empty). What caught it was reading the produced fixture's raw
    `control_responses` bytes rather than trusting the summary field they
    were supposed to justify. For any field a summariser computes by walking
    a shape nobody has fully decoded yet, diff the summary against the raw
    bytes it summarises before trusting a green run — a green run only
    proves the gates it checks, not the fields it computes.
  - **A credential guard scoped to the surface named in the design is not
    the same as a credential guard scoped to the surface the ticket
    commits.** The spec's security review enumerated argv, env and stderr as
    the credential-bearing surfaces and closed the first two by construction;
    `initControlScrubbed` guards the third. But every byte this family
    commits is claude's **stdout**, and no deterministic check runs over it —
    the PR's clean bill came from two independent human reads of the
    committed JSON, not from code. `dropped_line_capture_test.go`'s
    `dropcapScanner` already exists in this package for exactly this (scans
    arbitrary bytes for credential values and operator-path classes, and
    records which classes it checked so "no hits" stays distinguishable from
    "never ran") — a live-capture test that writes stdout-derived bytes to a
    committed fixture should run it over the marshalled record before the
    write, not rely on a human `grep`. This was re-measured true on
    2026-08-24, against the same committed fixture, for `argv[0]`, `cwd` and
    `memory_paths.auto`: two independent human reads had already passed it.
    The fix landed as four tickets, not the #1694 this entry originally
    pointed at (#1694 turned out to be the send-point/session-perturbation
    ticket, unrelated) — #1732 (below) builds the redaction table; #1733
    (below) applies it at the fill site; #1747 (below) records which classes
    a scan armed; #1748 (below) is the fail-closed deny-scan itself, wired in
    on the write path. #1749 (open) proves the scan refuses one planted
    record per armed class, not only the `/Users/` class #1748 shipped a row
    for.

  Code review also flagged, non-blocking: the `!= "null"` guard in
  `initControlSummarize` — the one thing distinguishing `"models":null` from
  `"models":[]`, which the function's own doc comment says is exactly what
  #1690 needs — has no test row pinning it (confirmed by mutation: dropping
  the guard leaves the summariser's test green). Worth a row before #1690
  starts decoding against this shape.

  Zero production files touched by any of the three tickets. See
  `docs/specs/architecture/1688-initialize-control-round-trip-capture.md` for
  the original design and security review,
  `docs/specs/architecture/1722-arm-named-initialize-capture-record.md` for
  the arm-recording and wait-result changes, and
  `docs/specs/architecture/1763-send-point-arms-live-capture.md` for the
  three-arm rig. This closes the `initialize` fixture family opened by
  #1695's split (#1696/#1701/#1702/#1700) and answers the send-point half of
  the questions #1694 carved out (session perturbation is still open); #1689
  places its `initialize` trigger on this run's recorded evidence rather
  than on a guess.
