## mid_turn_user_capture_test.go (#2728)

The live half of what claude does with a `user` line written to its stdin while a turn runs — the
question [the delivery-seam consumer](streamsup-package-per-conversation-turn-busy-track-delivery-seam-consumer-mid-turn-hold.md)
exists to avoid answering by accident. Pyrycode never writes mid-turn in production today — it holds every
queued message until the turn goes idle — so this probe drives `claude -p` directly, without the daemon,
writing a `user` envelope through `streamsup.WriteTurn`'s own encoding while a real turn is in flight.
Fixture committed as `testdata/mid_turn_user_v2.1.280.json`.

### The four arms, and what each one measured (claude 2.1.280, haiku)

| Arm | Write landed | Outcome | Echo |
|---|---|---|---|
| tool-boundary | during a 15s Bash call, after the `tool_use` message ended | `folded` — one `result`, quoting the marker | right after the `tool_result` line, before the follow-up request |
| no-tool | while a long text-only answer streamed | `second-turn` — the first `result` lacks the marker; a new turn opens | as the `user` line opening the second turn |
| after-last-tool | after the turn's only `tool_result`, before the next `message_start` | `second-turn` — same shape as no-tool | as the `user` line opening the second turn |
| back-to-back | two writes together during a Bash call | `folded` — one `result` quoting both markers | two echo lines back to back, after the `tool_result` |

No arm left a message unread — every write's marker shows up somewhere, either in the turn it landed in or
in the one it opened. The load-bearing distinction `mtuVerdict` draws is **not** "did claude read it" but
"did it fold into the running turn or open a second one": `arm.ResultCount > 1` is `second-turn` regardless
of whether the final text quotes the marker, and a single result with no mid-turn marker in its text is
`not-quoted`, not `not-read` — the record's own `limitations` field says why. The code's names are
`mtuFolded`/`mtuSecondTurn`/`mtuNotQuoted`/`mtuPartial`/`mtuNoResult`/`mtuNoTrigger`.

**The only arms that folded were the two written during a running tool call.** The ticket's one manual
run (2026-10-03, informal) had suggested claude takes a mid-turn message in "at the next tool boundary" in
general; this capture narrows that to *the* tool boundary that precedes the turn's own follow-up request.
A message written after the turn's last tool result — even well before the next `message_start` — missed
that boundary and opened its own turn, with its own `result`. One run per arm, so the exact edge of that
after-last-tool window (how late a write can land and still fold) is not pinned; a future capture that
wants that edge needs several timed writes within one arm, not a single delay.

**`--replay-user-messages` echoes every turn's opening message too, not just the mid-turn ones**, as a
`{"type":"user",…,"isReplay":true,"parent_tool_use_id":null}` line at line 2 of every arm. A reader built to
treat a marker-bearing echo as evidence that a message was written mid-turn must not key on the echo alone
— the opening message echoes by the identical shape, since `--replay-user-messages` was not conditioned on
turn position.

### What this doesn't settle

One claude version, one model, one run per arm (`mtuLimitations`). A design that bypasses the delivery
hold to deliver mid-turn — the reason this capture was commissioned — still needs: the after-last-tool
window's actual width, behaviour under a model besides haiku, and what a write during the model's *first*
tool call (before any result has appeared at all) does, which none of the four arms covers since
tool-boundary's trigger is the first `tool_use` and the arm always has exactly one tool call.

### Running it

`make e2e-realclaude` arms `TestRealClaude_MidTurnUserCapture` only while no `testdata/mid_turn_user_v*.json`
exists; `PYRY_PROBE_MID_TURN_USER_CAPTURE=1` forces a re-capture. The four arms run concurrently, one
`claude -p` child each, joined by a `sync.WaitGroup` before the record is built — reusing
`dropcapRecorder`/`dropcapRedactor`/`dropcapScanner` (from `dropped_line_capture_test.go`) for recording,
redaction and the fail-closed deny-scan unchanged, the same composition
[`compaction_capture_test.go`](e2e-realclaude-compaction-capture-test-go.md) uses. `TestMtuCommittedFixture`
decodes the committed fixture into `mtuRecord` with unknown fields disallowed and re-runs the promotion
gate offline, so a schema change that orphans the fixture fails under plain `go test`, not only on the next
live run.

**A goroutine-filled struct needs a named return, not a race detector, to catch an empty one.**
`mtuRunArm`'s per-arm fields (sequence, echoes, verdict) are filled by a deferred join that runs after
`cmd.Wait` returns; an early draft returned a plain `mtuArm` value, so the deferred fill ran after the
value had already been copied out, and every arm would have reached the record empty with no race and no
error — `-race` has nothing to flag in a function whose own goroutine never touches a second copy of the
same memory. The fix was a named return (`func mtuRunArm(...) (arm mtuArm)`), so the defer mutates the
exact value the caller receives. The same trap and the same fix are recorded independently in
[`ptyrunner-package.md`](ptyrunner-package.md) for a `defer`-based finalizer.

### Related

- [Delivery-seam consumer, mid-turn hold](streamsup-package-per-conversation-turn-busy-track-delivery-seam-consumer-mid-turn-hold.md) — the production hold this capture's findings inform.
- [Send half — `WriteTurn`](streamsup-package-send-half-writeturn.md) — the envelope encoding this probe reuses verbatim, and where "no user-authored text reaches this lane" is stated for current production behaviour.
- [`compaction_capture_test.go`](e2e-realclaude-compaction-capture-test-go.md) — another probe built on the same `dropcap*` recorder/redactor/scanner.
