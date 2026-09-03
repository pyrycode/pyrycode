# interactive_stream_inband_model_test.go
- `interactive_stream_inband_model_test.go` (#1582) — **live proof of #1581, not
  a new feature under test**: #1581 changed `Pool.UpdateSettings` to deliver a
  model/effort-only change by writing `/model <value>` as an in-band user turn
  instead of killing and respawning the child, and proved that hermetically —
  observing that no respawn happened and that the write was issued, never that
  claude itself changed model. This test supplies the second half. It drives an
  **in-process `sessions.Pool`** (not a daemon subprocess — the only test in this
  package that constructs one) through a turn, `Pool.UpdateSettings`, and a
  further turn, and asserts from claude's own per-turn `system`/`init`
  announcement — read via `inbandTapRecorder`, an `io.Writer` dropped into
  `streamsup.Config.Stdout` **upstream of the parser**, the same seam
  `dropcapRecorder` (#1260) taps — that the reported model changed to the
  requested one and that one process (`ChildPID` unchanged, one `"spawning
  claude"` log record) served every turn. The `init` line is asserted here from
  the tap upstream of the parser, independent of whatever the parser does with
  it downstream. CORRECTED 2026-08-19 (#1600): this used to say the line "must
  never reach a client or daemon event" because `emitSystemSubtype` had no arm
  for `init` — false now, `init` maps to `turnevent.ModelAnnounced` and reaches
  a daemon *event*. The reasoning was already wrong, not just the fact: the
  arm's absence was never entailed by the #833 posture, which is scoped to
  *logs*, and an event is not a log. What #833 still guarantees, unaffected by
  #1600, is that the value reaches no daemon *log* at any level — #1600's own
  arm logs the subtype keyword on its undecodable path and nothing else, ever.
  It still reaches no *client*: `turnbridge.MapEvent`'s `default` drops the
  variant. CORRECTED 2026-08-19 (#1616): this used to close "so no wire frame
  exists for it yet" — false now. `protocol.TypeModelAnnounced` /
  `protocol.ModelAnnouncedPayload` exist, declared by #1616 so a client can be
  written against the shape, the same declared-ahead-of-producer sequencing
  `rate_limited` used (#1405 ahead of #1410). What survives is `MapEvent` having
  no case for the variant: nothing emits the frame until #1617, so a client
  still receives nothing. CORRECTED 2026-08-20 (#1639): that surviving half is
  gone too, and so is the "still reaches no *client*" sentence above it. #1638
  added `MapEvent`'s `turnevent.ModelAnnounced` case and `cmd/pyry`'s matching
  `Handle` case, so the frame is emitted and an interactive v2 client does
  receive one — #1617 was the split parent and never shipped the mapping. The
  *log* half is the one that has never expired: the value still reaches no
  daemon log at any level. An event is not a log, and a wire frame is not a log
  either. No `--model` in the base argv
  (a respawn's
  recomposed argv would otherwise carry two); the starting model is read off
  turn 1 rather than assumed, so the two-alias target table (`haiku`/`sonnet`)
  always has a candidate that differs from whatever a given machine's default
  is. Two assertion pairs, each the sole red for a distinct mutant, both proven
  by measured evidence runs recorded in the file's header: A1/A2 (model changed)
  red under a `-overlay` mutant dropping the in-band `/model` send, green
  (vacuously) on the pre-#1581 tree since a respawn also changes the model;
  A3/A4 (no teardown) red on the pre-#1581 tree (`b047b9e^`, 2 spawns), green
  under the mutant. Zero production files touched. Code review: one round, PASS,
  one non-blocking SHOULD FIX (the post-`UpdateSettings` settle wait's target
  result count is hardcoded rather than captured relative to the count at that
  moment — a low-probability false-red path on a >45s first turn, left as a
  follow-up rather than fixed on this branch). See [`codebase/1582.md`](../codebase/1582.md).
  **EXTENDED #1838** (bracketed model values, e.g. `opus[1m]`): a second phase,
  appended after A1–A4 in the same test function rather than a new one, proves
  that a *bracketed* value delivered in-band takes effect on a running child —
  the piece `internal/relay`'s hermetic `TestValidModel` /
  `TestValidModel_ByteSetIsClosed` cannot reach, since `validModel` is
  unexported and this package cannot call it. The phase never pins a
  bracketed string: the ticket named `opus[1m]`, measured against claude
  2.1.220, and the capture this repo now carries (2.1.239) no longer publishes
  that row at all, so a hardcoded target would fail on menu churn unrelated to
  the mechanism. It instead calls `RequestInitialize` on the same live child,
  reads the model menu claude just published off the tap, and picks the first
  row whose `value` contains `[` and whose `resolvedModel` differs from the
  model this phase's own baseline turn announced — the second condition is
  what stops the phase from asserting a change that was already true. Finding
  none, it `t.Fatalf`s and lists every `value` offered, deliberately not
  `t.Skip`: a skip would be indistinguishable from this package's
  absent-credentials skip in the run count, and a claude that stops publishing
  any bracketed row retires the ticket's premise, which is worth surfacing
  rather than swallowing. Three assertions mirror A1–A4 for the new baseline
  (model changed; the announced model matches the chosen row's
  `resolvedModel`, with a message distinguishing a claude-side inconsistency
  from a defect in this change if the two disagree; one child pid across the
  phase). See [`v2-session-manager.md`](v2-session-manager.md)'s `validModel`
  entry for the grammar the hermetic tests pin. **Observed 2026-09-02 (#2041,
  filed as #2045):** a real-claude gate run flagged
  `TestInteractiveStream_InBandModelChange_LiveChildReportsNewModel` as newly
  red on a branch that touched zero production source files, because claude's
  own model-menu row changed between the branch run and the base re-run eight
  minutes later — `claude-fable-5-1[1m]` (not applied in band) vs.
  `claude-fable-5[1m]` (applied), same binary version, a drifted published
  value. Re-running the single test on the branch three times passed 3/3.
  Generalises past this one test: a live-claude gate's before/after comparison
  assumes claude's own responses hold still between the two runs, and when
  they don't, the diff attributes the flap to whichever side ran second —
  compare the two runs' *observed inputs*, not just their verdicts, before
  accepting a live-gate regression as branch-caused. **Operationalised
  (#2045):** `interactive_stream_inband_menu_drift_test.go` turns that
  generalisation into a mechanical check. On the B1/B2 red path only, it
  classifies the row phase 2 chose against the claude-version-keyed baseline
  this repo already commits (`testdata/initialize_control_v<version>.json`)
  and logs one of match/drift/no-capture, naming the version and, on drift,
  listing both the committed and the live menus — turning a two-run diagnosis
  into a one-run one. It fails nothing itself and never touches the committed
  baseline. The baseline is addressed by **exact version name, never a
  glob**: a glob would find a capture at a neighbouring claude version and
  report "match" against the wrong baseline, silently reproducing the very
  misattribution this generalisation exists to end — the absence of a capture
  at the running version is itself the reportable `no-capture` verdict, not a
  hole for a neighbouring version's capture to fill. The same caution applies
  to any future baseline-vs-live comparison added to this package.

- `set_permission_mode_probe_test.go` (#1595) — **does the bypass posture have
  an in-band form, the way #1581/#1582 proved the model does?** Four direct
  `exec.CommandContext("claude", …)` children (not a `sessions.Pool`, mirroring
  `permission_protocol_spike_test.go`'s shape, not #1582's), each driven through
  an identical two-turn Bash probe: `revoke` and `enable` write a
  `{"type":"control_request",…,"subtype":"set_permission_mode"}` line on the
  held-open stdin between the two turns (the same control-channel
  `(*Runner).Interrupt` already writes to, but whose `control_response` it never
  reads — this test does); `control_default` and `control_bypass` run the
  identical sequence with no control request, as the behavioural baseline each
  measurement arm is judged against at turn 2. Verdict is computed from turn-2
  behaviour, never the echoed `control_response` alone, per the ticket's AC:
  an echoed `success` that still behaves like the bypass control would be
  recorded as a FAILED revocation. Measured 2026-08-19 against claude 2.1.220:
  **revoke succeeds in-band (`bypassPermissions → default`, no respawn, all
  three reads — response, `init` echo, behaviour — agree); enable is refused**,
  with a third error string not previously read out of the binary (`Cannot set
  permission mode to bypassPermissions because the session was not launched
  with --dangerously-skip-permissions`). Four fixtures under `testdata/`
  (`set_permission_mode_v2.1.220_<arm>.json`), named to fall outside both
  `permission_protocol_regression_test.go`'s `fixtureGlob` and
  `dropped_line_capture_test.go`'s `dropcapFixtureGlob` — a live-free sibling
  test (`TestRealClaude_SetPermissionMode_FixtureNamesAvoidRegressionGlobs`)
  pins that deterministically, no subprocess or credentials required. Zero
  production files touched; no writer for the subtype is added — #1596 decides
  that. See [`set-permission-mode-inband-probe.md`](set-permission-mode-inband-probe.md)
  for the full measurement writeup and [`codebase/1595.md`](../codebase/1595.md).

- `permission_mode_switch_probe_test.go` (#2041) — **the sequel measuring the
  four modes #1595 left uncovered**: `acceptEdits`, `dontAsk`, `plan` and
  `auto` all switch on a running child in-band at 2.1.239, and `auto` is
  refused per **model**, never per mechanism (verbatim: `Cannot set
  permission mode to auto: auto mode unavailable for this model`). Full
  measurement in
  [`permission-mode-switch-inband-probe.md`](permission-mode-switch-inband-probe.md).
  `runSetModeChild` gained a `setModeChildConfig` parameter (model, prompts,
  turn bound, fixture family) so the model is a property of the measurement
  rather than the package-level `setModeModel` constant #1595 hardcoded to
  `claude-haiku-4-5` — one of the two 2.1.239 rows publishing no
  `supportsAutoMode` at all; reused unchanged, the `auto` arm would have
  measured a refusal on a model that never supported auto and reported it as
  "auto no longer switches in-band". The model is instead picked out of a
  throwaway discovery child's own live `initialize` model list, never a
  table — the concrete reason: the live list published
  `claude-fable-5-1[1m]` where the *committed* `initialize_control_v2.1.239.json`
  capture records `claude-fable-5[1m]`, same binary version, a drifted value.
  `supportsAutoMode: false` is spelled by **absence** at this version (no row
  carries the literal `false`), so the capture's `SupportsAutoMode` /
  `KeyPresent` fields are written unconditionally, no `omitempty` — that
  field is exactly the trap the ticket exists to make visible. **Two traps a
  plan predicted but only a running test caught:** the new fixture family's
  version token goes through `versionSlug`, but the arm token didn't, until
  hostile-literal rows (`a/b`, `/abs`, `..`) in the offline names test
  reddened — a minted name for `a/b` resolved outside `testdata/`, the one
  directory the writer creates; `modeSwitchNameToken` now maps every byte
  outside `[A-Za-z0-9_-]` to `_` and caps at 32, sanitising rather than
  rejecting so containment is a property of the name and no caller has to
  validate first. Separately, `runSetModeChild`'s stderr-scrub guard
  (`initControlScrubbed`) sat *after* a `t.Fatalf` that printed raw stderr
  through `truncateString` — a cap, not a redaction — so a credential-bearing
  auth failure would have reached a run log the dispatcher salvages; fixed by
  reordering to match `runModeSwitchDiscovery`'s already-correct order in the
  same file. Reading a model `value` off one child's stdout and passing it as
  `--model <value>` to the *next* child is flag injection, not shell
  injection — no shell is involved, but a value beginning with `-` parses as
  a flag, so `modeSwitchModelValueOK` (non-empty, ≤64 bytes, no leading `-`,
  restricted charset) is the one gate every candidate passes before
  selection, worth the pattern for any future probe that round-trips a
  claude-published value back into an argv. Zero production files touched.
  See `docs/specs/architecture/2041-inband-mode-switch-probe.md` for the full
  design and security review.

- `interactive_stream_inband_bypass_revoke_test.go` (#1622) — **live proof of the
  composed path #1604 built, not of the wire format #1595 already proved.** #1595
  hand-wrote the `set_permission_mode` control line onto four
  `exec.CommandContext` children it owned directly; #1604 proved `Pool.UpdateSettings
  → inBandDeliverable → deliverSettingsInBand → Runner.RevokeBypass` only through a
  fake runner. Neither proved pyry's own `Pool`, holding a real `streamsup.Runner`
  over a real claude child, actually emits those bytes. This test drives that: an
  in-process `sessions.Pool` (#1582's shape, re-pointed) whose bootstrap child gets
  its bypass posture from a **seeded registry entry** (`yolo:true`) rather than the
  base argv — `revokeBaseArgs` is declared empty on purpose, because either shortcut
  (a stored posture already matching the update, or a bypass flag baked into the
  base argv) yields a run that measures a child nobody revoked. Two guards cover
  that seam: a pre-spawn check on `Pool.DefaultSettings()` and a post-turn-1 check
  that the first `init.permissionMode` is `bypassPermissions`, so a seed failure
  reads as itself rather than as a delivery failure. The stdout tap
  (`revokeTap`) bridges #1582's line-splitting `io.Writer` shape with #1595's
  `setModeRecorder` field capture (`control_response`, `permissionMode`, results)
  by embedding rather than re-deriving the classifier — the one type in the package
  that reads `control_response` off a `Pool`-spawned child's raw stdout. A sibling
  log recorder retains `deliverSettingsInBand`'s fire-and-forget "not delivered"
  `Info` record verbatim, since that record is otherwise swallowed and nothing else
  in the daemon's ordinary logs distinguishes a working revocation from a dropped
  one. Measured 2026-08-19 against claude 2.1.220, all three runs `-race`, mutants
  applied via `-overlay` (no mutated source ever written to the worktree): green run
  — one `control_response`, `init.permissionMode` `[bypassPermissions default]`, one
  spawn, pid unchanged, 5.85s; **M1** (drop the revoke-detection clause from
  `deliverSettingsInBand`) — no `control_response`, `init.permissionMode` stays
  `[bypassPermissions bypassPermissions]`, one spawn, pid unchanged, 50.03s — proves
  nothing was written and nothing was torn down; **M2** (`inBandDeliverable` returns
  false for a revoke, the pre-#1604 shape, falling through to `sup.Restart`) — no
  `control_response`, `init.permissionMode` still flips to `[bypassPermissions
  default]`, but two spawns and pid changes, 50.57s — the row that earns the
  four-assertion set, since the permission-mode echo alone cannot tell a delivered
  revocation from a respawn under a recomposed bypass-free argv. Both mutant runs
  take ~50s against the green run's 5.85s because no `control_response` ever
  arrives, so the tolerated wait burns its full budget — working as intended, not a
  hang. **Scope boundary, deliberate**: asserts the revocation reached the child and
  nothing was torn down, not that the posture is behaviourally enforced — an echoed
  permission mode is claude's own report, not proof of enforcement; that
  measurement is a sibling ticket that consumes this harness. One signature
  widening in `interactive_stream_inband_model_test.go`: `inbandSendTurn`'s
  parameter is now the `inbandResultCounter` interface (`resultCount() int`)
  instead of the concrete `*inbandTapRecorder`, so both this file's `revokeTap` and
  #1582's recorder satisfy it with zero call-site edits. Zero production files
  touched. See `docs/specs/architecture/1622-live-pool-bypass-revocation.md` for
  the full design and the assertion-to-mutant mapping.

- `bypass_reescalation_probe_test.go` (#2060) — **the re-escalation #1595 never
  covered**: a child launched with the flag, downgraded in-band to `default`, then
  asked back into `bypassPermissions` in the *same* child. Measured 2026-09-03
  against 2.1.239: **RE-ESCALATION APPLIED** — the first committed capture of
  #1686's uncaptured 2026-08-21 hand observation, and it reproduces. `enable`
  (#1595's negative, no launch flag) is re-measured in the same run and still
  refused, byte-identical to the 2.1.220 message, so the probe is shown to still
  discriminate. Together the two settle the mechanism: claude gates the escalation
  on the **launch argv**, not on the session's current mode. Drives **three** turns
  per arm rather than #1595's two, because the post-re-escalation read is a turn 3
  and #1595's index-symmetry rule requires the controls to carry a matching turn —
  which also means the two directions (`reescalate` at turn 3, `enable` at turn 2)
  are classified at different indices, so the no-discrimination check runs per
  direction rather than once up front. A downgrade gate (`reescalateDowngrade`)
  confirms the child actually left bypass before the second request was sent —
  without it, "turn 3 matches the bypass control" is true whether or not the
  downgrade ever landed, since a child that never left bypass looks identical to
  one that returned to it. `setModeArm` and `setModeChildConfig` both gained
  additive fields (`secondTargetMode`, `promptThree`) so `setModeArms` and #2041's
  inline literal are untouched. Zero production files touched. See
  [`bypass-reescalation-probe.md`](bypass-reescalation-probe.md) for the full
  measurement writeup and
  `docs/specs/architecture/2060-bypass-reescalation-probe.md` for the design and
  security review.
