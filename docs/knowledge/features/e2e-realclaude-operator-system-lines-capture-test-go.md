## operator_system_lines_capture_test.go (#2255)

The live capture for the four `system` subtypes `emitSystemSubtype` has no arm for —
`informational`, `local_command_output`, `commands_changed`, `notification` — each dropped
silently today via `consumeLine`'s `ignoredLineTypes` branch. Drives a `UserPromptSubmit`-blocking
hook, a local slash command, and a mid-session `.claude/commands/` write as three triggers on one
child, watches `notification` (no known trigger) across the whole session, and answers for each
line whether it decodes into `streamLine` via the shipped parser's own verdict — no local mirror of
the unexported type. Reuses `dropcapRecorder`/`dropcapRedactor`/`dropcapScanner`/`dropcapMakeEntry`
whole; `oslcap` is the file-local prefix, deliberately not `opcap` (a substring of `dropcap`, which
would make the family ungreppable).

### The live finding: three of the four subtypes answered, one is still open

At claude `2.1.259`: `informational` was **observed**, verbatim, decoding cleanly into
`streamLine` with `message` absent and keys `content`, `level`, `prevent_continuation`,
`session_id`, `subtype`, `type`, `uuid`. That capture also answers a question three earlier
tickets (#1763, #2138, this one) had all recorded as unmeasured: **a `UserPromptSubmit` hook does
run under `--dangerously-skip-permissions`** — two invocations, verdicts `[blocked passed]`, and
the child answered the next, unmarked turn normally.

`local_command_output` came back **unobserved with its trigger witnessed as fired**: the rig's
local-command turn ran (`usage`, chosen from the session's own inventory — see below) and produced
ordinary assistant prose, not a distinct `system` line. That is itself the finding #2257 needs: at
least for `/usage`, a local slash command's output does not arrive as a `local_command_output`
line at all, so a mapping built for one would have nothing to decode against.

`commands_changed` landed **inconclusive** on this run — the rig wrote
`.claude/commands/oslcap-probe.md` mid-session, but no post-write `system/init` line arrived inside
the observation window to measure whether the inventory changed.

**CORRECTED 2026-09-10: the committed fixture answers it, so read the fixture's row and not the
paragraph above.** A later lap supplied exactly the longer post-write window this section asked for.
`operator_system_lines_v2.1.259.json` records the subtype `observed: false`, `trigger_fired: false`,
`trigger_could_not_fire: true`, `conclusive: true`: 154 bytes of a project slash command went into
the live child's `.claude/commands`, claude re-broadcast its inventory across 2 post-write
`system/init` lines, and in both the probe was absent and the inventory was identical to the
baseline. Writing a file there provokes nothing at `2.1.259`. That does not extend to every trigger
— the one #2197 named and nobody has tried is skills discovery in a subdirectory, a different
mechanism from project slash commands. #2259, which would have mapped the subtype, is closed as
answered on this evidence.

`notification` stayed **unobserved, no trigger**, exactly as designed — it has none, and the
record says so rather than naming one that doesn't exist.

### Don't hardcode a slash-command trigger — read a committed init line's inventory first

The plan named `/cost` as the local-command trigger. `dropped_lines_v2.1.220.json`'s committed
`system/init` line carries a 46-command inventory that does **not** include `cost`, while it does
include `usage`, `context`, `extra-usage` and `usage-credits`. Hardcoding `/cost` would have driven
a turn claude answers as ordinary prose and recorded a trigger that could never fire, spending a
whole live gate to learn a name. `oslcapPickLocalCommand` instead picks from the session's own
`system/init` inventory at runtime, preferring the ticket's named command and falling back through
an ordered candidate list — and when no candidate is present it still drives the ticket's own
command and records `local_command_in_inventory: false` rather than silently skipping, so the
subtype reads as inconclusive rather than as a silent no-op. Before wiring any future trigger to a
named slash command, check a committed init line's `slash_commands` array for it.

### Claude re-broadcasts the full slash-command inventory every turn, not just at session start

The first live lap's `commands_changed` witness read only the **first** `system/init` line's
inventory and compared later behaviour against it — which is the session's state *before* the rig
wrote anything, false by construction however claude actually behaves. The record's own census
showed what was being thrown away: four `system/init` lines across four turns. Claude re-sends the
whole slash-command inventory on **every** turn, so whether a mid-session file write reached it was
sitting on the wire in the post-write init lines the rig never read. The fix,
`oslcapInitInventories`, reads every init line in a half-open window rather than just the first,
and `oslcapInitSlashCommands` (the pre-write, first-line reading) is now that function's special
case — still correct for the local-command witness, which does ask about pre-write state.
`oslcapSameInventory` compares order-insensitively so a reordering alone can't manufacture a
"changed" verdict. Any future probe measuring a mid-session change against `system/init` state
needs the **post-write** lines, not the line that happened to arrive first.

### A trigger that measurably does nothing is a finding, not a rig failure — but only with a real measurement

Collapsing "the rig failed to fire the trigger" and "the rig fired the trigger and measured no
effect" into one verdict would have permanently blocked promotion on a mechanism that may never
work, taking the three subtypes that *did* answer down with it. `TriggerCouldNotFire` is the
second verdict, gated on a three-part condition: the rig performed the write, claude re-broadcast
the inventory at least once afterward (a measurement exists), and that measurement shows no
change. Zero post-write init lines is still **inconclusive**, not a negative finding — which is
exactly the state `commands_changed` landed in on this run, since the live lap never verified the
condition's third leg produced a real answer either way.

**Known gap, not yet fixed:** the verifier flagged (SHOULD FIX, deferred to the next touch of this
file) that an *empty* pre-write baseline would make `oslcapSameInventory` read a bare length
mismatch as "changed" without anything having actually changed — the same failure class the
post-write-measurement guard exists to close, just on the before side instead of the after side.
It did not fire on this run (`commands_changed` landed inconclusive, not a false positive), but a
future run where no pre-write init line lands yet at least one post-write line does would trip it.

### The shipped parser answers "did this decode" with no local mirror of the type needed

For a `system` line, `streamLine`'s decode-failure branch inside `consumeLine` has exactly two
gates — type `user` (`dropHarnessProseLine`) and subtype `permission_denied`
(`consumePermissionDeniedLine`) — and neither can claim a line of these four subtypes, so a `system`
line that fails to decode reaches `emitUnrecognized` unconditionally. A line that decodes but has no
`emitSystemSubtype` arm produces zero events and is dropped silently instead. `parseOne`'s own
return value is the oracle for both cases; declaring a local copy of `streamLine` to answer the
question would only ever measure the copy, not the shipped parser.

### `events_emitted` reads oppositely depending on the decode verdict

On a line that decodes, a non-zero `events_emitted` means the drop set moved (a mapping arm fired
that wasn't there before). On a line that fails to decode, the one event present is the
`Unrecognized` the decode failure itself produced — the same event `decode_verdict` is read from.
A doc, or a future reader of the committed fixture, that states the first reading alone is wrong
half the time; the two fields must be read together.

### Fixture status as of this ticket — lost the same way, again

The dispatcher's live gate fired clean (`outcome=fired`, `informational` observed,
`local_command_output` and `notification` conclusive, `commands_changed` inconclusive) and its own
log line reported `git add: staged=true`. **The commit never happened.** The run executed in the
dispatcher's detached, merge-only `real-claude-gate-2255` worktree, not a persistent branch
checkout — the same loss already recorded for
[`compaction_capture_test.go`](e2e-realclaude-compaction-capture-test-go.md) (2026-09-08),
[`task_notification_capture_test.go`](e2e-realclaude-task-notification-capture-test-go.md), and
[`effort_init_capture_test.go`](e2e-realclaude-effort-init-capture-test-go.md). This is now four
captures in a row that fired clean in the dispatcher's own gate and left no committed bytes behind
it — a `git add` inside a worktree the gate discards buys nothing regardless of how carefully the
capture stages it; only a repair-leg commit from a persistent checkout, or an opportunistic commit
riding an unrelated ticket (`compaction_capture_test.go`'s #2236 precedent), lands the bytes. No
`testdata/operator_system_lines_v2.1.259.json` is committed as of this writing, and the finding
above is trustworthy on the strength of the dispatcher's own JSON test log, not on committed bytes.

### Related

- [`compaction_capture_test.go`](e2e-realclaude-compaction-capture-test-go.md) — the fixture-lost-
  after-a-green-gate pattern in full, and the opportunistic-commit recovery this capture also needs.
- [`task_notification_capture_test.go`](e2e-realclaude-task-notification-capture-test-go.md) and
  [`effort_init_capture_test.go`](e2e-realclaude-effort-init-capture-test-go.md) — the same loss,
  independently, on two other captures.
- [`parent_tool_use_capture_test.go`](e2e-realclaude-parent-tool-use-capture-test-go.md) — a fifth
  instance of the same pattern, and the standing warning against pricing a future capture ticket
  from one whose fixture was never actually captured.
- `emitSystemSubtype`, `ignoredLineTypes`, `consumePermissionDeniedLine` (`internal/streamsup/parser.go`)
  — the mapping surface these four subtypes are for; out of scope here, live in #2256–#2259.
