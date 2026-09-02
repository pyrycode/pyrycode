# Stream-json mode (#1140)

Every mode above models claude's **PTY/TUI** surface — a screen to read, a
`<uuid>.jsonl` transcript to grow. The daemon's `internal/streamsup` runner (the
`interactive_runner: "stream-json"` toggle, #1081, shipped) instead spawns claude
**headless over a pipe**: line-delimited stream-json envelopes on stdin, `assistant`/
`result` lines on stdout, no PTY and no transcript file at all. `PYRY_FAKE_CLAUDE_STREAM_JSON`
teaches fakeclaude that wire, so the stream path has a fake to drive it end-to-end —
the harness piece the sibling #1135 (blocked-by this ticket) rides for its first
`send_message` e2e spec.

When `PYRY_FAKE_CLAUDE_STREAM_JSON` is set, `main()`'s **very first** statement is:

```go
if os.Getenv(envStreamJSON) != "" {
    runStreamJSON(os.Stdin, os.Stdout, os.Getenv(envStreamInterrupt) != "")
    return
}
```

This one gate — checked above every `mustEnv(envSessionsDir/…)` call — makes three
properties fall out structurally rather than by a validation branch:

| Property | Why it holds |
|---|---|
| Byte-identical when unset | An unset env var falls straight through; nothing below the `if` changes |
| Mutually exclusive with every PTY/TUI mode | The `return` fires before any other mode's env var is even read; if both are set, stream wins and the other is inert |
| Binds no sessions dir / transcript | The `return` short-circuits before the three `mustEnv` calls and the JSONL open — stream mode never touches sessions-dir machinery |

`runStreamJSON(r io.Reader, w io.Writer, honorInterrupt bool)` is the testable
read→emit loop — the `io.Reader`/`io.Writer` seam (rather than hard-wiring
`os.Stdin`/`os.Stdout`) is what lets the unit test drive it against in-memory
buffers. `honorInterrupt` (default `false`, wired from `envStreamInterrupt`, #1136)
selects the interrupt rider — see § Interrupt mode below; this table describes the
default path:

| Step | Effect |
|---|---|
| Read one line | `bufio.NewReader(r).ReadString('\n')` — not `bufio.Scanner`, whose token cap would truncate a long prompt; also correctly processes a final non-newline-terminated read at EOF |
| Decode | `userTurnText(line)` — unmarshal into a local `inUserTurn` mirror of `streamsup.userTurn` (unexported there); a decode error or `Type != "user"` returns `("", false)` and the line is skipped (a `control_request` interrupt line, a blank line) |
| Respond | on a user line, mint `m<N>` (a local monotonic `int` counter, one per received turn) and call `writeStreamResponse(w, id, text)`, where `text` is the first `text` content block, verbatim |
| Stop | on `ReadString` error (EOF — the daemon closed stdin — or a read error) or the first write error (daemon's read end gone); both treated like teardown, never `os.Exit` mid-turn |

`writeStreamResponse(w io.Writer, msgID, text string) error` writes fakeclaude's
canned reply to one turn — one `assistant` line, one `result` line — each
`json.Marshal`-encoded from a local struct (`outAssistant`/`outResult`), never
string-concatenated, so the echoed `text` (caller-controlled bytes) is escaped and
each object lands as exactly one physical line regardless of content:

```
{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"<echoed prompt>"}]}}
{"type":"result","subtype":"success","session_id":"fake-stream"}
```

These are byte-compatible with `cmd/pyry/stream_turn_drain_test.go`'s
`assistantTextLine`/`resultLine` fixtures, so the daemon's real
`streamsup.Parser` (`internal/streamsup/parser.go`) maps them to
`turnevent.TextChunk{Text: <echoed prompt>}` then `turnevent.TurnEnd{Reason:
TurnEndReasonEndTurn}` — see [streamsup-package.md § Turn I/O](streamsup-package.md#turn-io--envelope-write--stdout-parser-1088).

- **`text` is echoed, not canned.** The response text is the inbound prompt itself
  — free downstream value for the #1135 `send_message` e2e, which can assert
  delta text == sent prompt, at the cost of one extra field-read on the
  already-decoded struct.
- **`session_id` is a fixed literal (`"fake-stream"`).** `Parser.streamLine` never
  decodes `session_id`, so the value is cosmetic; threading the daemon's injected
  `--session-id` through would force argv parsing the fake deliberately never does.
- **Wire shapes are hand-mirrored, not imported.** `internal/streamsup`'s envelope/
  parser types are unexported, and importing them would also pull a dependency into
  the fakeclaude **binary** — the same zero-dependency posture that keeps
  `interruptEndTurnLine` (Esc-ends-turn mode, above) a hand-written literal. Only the
  **test** links `internal/streamsup`/`internal/turnevent`; `main.go` stays
  stdlib-only.
- **Single goroutine, no raw mode.** `runStreamJSON` runs entirely on `main()`'s
  goroutine — no stdin-reader goroutine, no poll loop, none of the other modes'
  `atomic.Bool` signals are reached. Stream-json travels over a **pipe**, not a PTY,
  so canonical line discipline / CR mapping don't apply — `enterRawMode()` is never
  called in this mode.
- **Default mode still ignores every non-`"user"` line, with one exception.** An
  `initialize` `control_request` gets a canned answer regardless of mode or rider (#1692,
  see § Initialize control request answer below); every other `control_request` —
  including `interrupt` in default mode — is still dropped unlooked-at. new_session /
  queue / modal remain out of scope for the fake. Interrupt is handled by the
  `honorInterrupt` rider, below (#1136).
- **No new glyph, no allowlist change.** Stream mode emits pure JSON — no TUI
  substrate glyphs — so `cmd/substrate-guard`'s allowlist for this file is
  unaffected.

Pinned by `stream_detect_test.go` (untagged, package-level `go test`, no e2e build
tag): a single-turn round-trip and a multi-turn round-trip both feed
`runStreamJSON`'s output through the **real** `streamsup.Parser` (belt-and-suspenders,
different fabric — a shape bug the fake and a hand-written test-decoder would share
is still caught on the emit side) and assert the exact `TextChunk`/`TurnEnd` sequence;
a non-user-lines-ignored test asserts zero output bytes for a `control_request` /
blank / unparsable line; a distinct-message-ids test guards the per-turn counter; a
direct `writeStreamResponse` shape test checks the two line shapes without going
through the parser at all. See [codebase/1140.md](../codebase/1140.md).

### Interrupt mode (`honorInterrupt`, #1136)

`PYRY_FAKE_CLAUDE_STREAM_INTERRUPT` (default-off) is a rider on stream mode:
`envStreamJSON` still gates entry to `runStreamJSON`; `envStreamInterrupt` is read
once, at the same call site, and passed through as the `honorInterrupt bool` param
so the function itself stays a pure I/O seam with no env reads inside. It exists to
give the stream path a fake that can stay **mid-turn** long enough for a live
interrupt e2e (#1136) to land one — the default mode always answers a user turn
immediately, so there is no window to interrupt.

| Inbound line | `honorInterrupt == false` (default) | `honorInterrupt == true` |
|---|---|---|
| `{"type":"user",…}` | `writeAssistantEcho` + `result{success}` (unchanged) | `writeAssistantEcho` **only** — the result is withheld, so the turn stays in flight |
| `{"type":"control_request",…"subtype":"interrupt"}` | ignored | `writeInterruptAck` — `control_response{response:{subtype:"success",request_id:<echoed>}}` (#1500), **then** `writeInterruptedResult` — `result{subtype:"error_during_execution"}` |

`writeStreamResponse` was split so the assistant-echo half is independently
reusable: `writeAssistantEcho(w, msgID, text)` writes just the assistant line;
`writeStreamResponse` is now `writeAssistantEcho` + the `result{success}` line,
byte-identical to its pre-#1136 output. `interruptControlRequest(line []byte) (string, bool)`
decodes a minimal `{type, request_id, request.subtype}` mirror of
`streamsup.controlRequest`/`marshalInterruptEnvelope` (`internal/streamsup/envelope.go`)
— the exact shape the daemon writes to the child's stdin on a phone interrupt — and
returns `("", false)` (line ignored) on anything that isn't
`type=="control_request" && request.subtype=="interrupt"`, preserving the same
per-line decode resilience as `userTurnText`. The returned id is what the ack echoes;
it widened from a bare `bool` in #1500.

**The ack goes FIRST (#1500).** `writeInterruptAck` writes the `control_response`
real claude answers an interrupt with, before the interrupted `result` and on the same
writer — matching claude's own order (~40 ms ack, then the result) and, more usefully,
buying the e2e its causality on the rate-limit rider's terms: a `turn_end` reaching a
client implies the ack has already been through the parser, so
`TestRelayV2_StreamInterruptStopsRunningTurn`'s zero-`unrecognized_message` assertion
needs no sleep, no poll and no ordering race to tune. The envelope is transcribed from
the committed capture (`internal/e2e/realclaude/testdata/set_permission_mode_v2.1.220_revoke.json`,
verbatim in [`set-permission-mode-inband-probe.md`](set-permission-mode-inband-probe.md#the-control_response-received-verbatim)),
which nests `subtype` and `request_id` **under `response`** — the inverse of the
request side. Two honest limits: the capture is a `set_permission_mode` ack rather than
an interrupt one, so what it establishes is the control channel's *envelope*; and the
inner `response` payload is request-specific and unmeasured for `interrupt`, so the fake
invents none. `TestRunStreamJSON_InterruptAckRider` pins the two emitted lines and their
order — it is the arrival control for the e2e's zero, which cannot prove its own input
arrived.

The mode is **stateless**: it emits the ack + `result{error_during_execution}` pair on
*every* interrupt `control_request` it sees, with no in-flight-turn tracking. A caller that
drives exactly one interrupt per in-flight turn (the only shape #1136's e2e needs)
gets the right behaviour for free; nothing polices "interrupt with no turn open".

`error_during_execution` is not an arbitrary error subtype — `streamsup.Parser`'s
`resultTurnEndReason` (`internal/streamsup/parser.go`) maps that one subtype to
`turnevent.TurnEndReasonCancelled` and every other subtype to `end_turn`, so this is
the specific value that makes the daemon report the turn as interrupted rather than
merely errored. See [codebase/1136.md](../codebase/1136.md).

### Initialize control request answer (#1692)

The daemon is gaining the ability to ask its stream child to `initialize` (#1689) — the
request real claude answers with the session's model list and slash-command list. Unlike
every rider above, this answer is **unconditional, not env-gated**: `runStreamJSON`'s
dispatch matches an `initialize` `control_request` in an arm placed *beside*
`honorInterrupt`, evaluated before it, so the answer fires in both stream modes and needs
no new env var, no widened signature, no new `main()` call site. This is a deliberate
departure from this doc's "default-off, byte-identical when unset" house rule for riders:
nothing sends the request yet, so the unconditional answer changes no existing suite's
bytes today, and gating it behind a knob would leave the default path — the one every
future suite runs once #1689 lands — silently unanswered. At the time this answer
shipped, `streamsup.Parser`'s `control_response` case read nothing below the top-level
`type`, so the answer reached no client and changed no frame count anywhere; `emitModelList`
(`internal/streamsup/parser.go`) is what now decodes it — see
[streamsup-package.md](streamsup-package.md) for the settled per-field absent/empty/false
readings.

`controlRequestID(line []byte, subtype string) (string, bool)` is the shared decode
`interruptControlRequest` used to own alone, generalised by subtype (`subtypeInterrupt`
vs `subtypeInitialize`) rather than duplicated — the same
extract-the-shared-half-into-a-parameter move #1631 made for `argvSessionID` →
`argvIDFlag` in this file. `interruptControlRequest` is now a one-line wrapper and its
existing behaviour, and every existing interrupt-mode test, are unchanged by construction.

`writeInitializeAck` answers with the same inverted envelope `writeInterruptAck`
established (`subtype`/`request_id` nested *under* `response`), one level deeper still:
the initialize payload sits at `response.response`, carrying `models` and — since #2008 —
`commands` beside it. (#1683, the ticket this doc used to point to for that arm, closed
`NOT_PLANNED` and split into the #1720 family #2008 is a grandchild of; the dead pointer
is why a "extends this same answer" cross-reference should never survive a ticket's own
closure without a follow-up check.) Still absent: `agents`, `output_style`, `account`,
`pid`, `session_state`, and the outer object's two pending-request arrays.

**`initializeCommands` reuses `initializeModels`'s map-per-entry pattern for the same
absence-must-be-literal reason, plus a property the model list didn't need: attribution.**
Four entries, transcribed verbatim from the same capture and kept in the capture's own
order — `clear` (aliases `["reset", "new"]`), `compact` (no `aliases` key), `config`
(aliases `["settings"]`), `model` (no `aliases` key) — deliberately interleaved rather
than grouped. One alias-bearing entry can't separate "aliases attached to the entry that
owns them" from "aliases present somewhere in the payload"; two, with different values
and different counts, separated by a no-alias entry, force a decoder that flattens or
mis-attributes to redden at a specific index instead of passing by coincidence. Neither
alias-bearing entry ever cans `"aliases": []` — `SlashCommand.MarshalJSON`
(`internal/protocol`) already normalises the omitted-key case to `[]` on the wire, see
[protocol-package-slash-command-list-payload.md](protocol-package-slash-command-list-payload.md)
— so the fake's job, like `initializeModels`'s, is only to keep feeding that decode the
shape claude actually sends. `initializeCommands` carries the identical READ-ONLY
discipline `initializeModels` does — marshalled from `runStreamJSON`'s single goroutine in
production and from `t.Parallel()` subtests in the package unit test — stated as a rule in
the var's doc block rather than left to an observed `-race` green, since a future append or
reassignment would race only on the runs where the two happen to overlap.

`internal/e2e/relay_v2_stream_slash_command_list_test.go` (#2008) is the round-trip proof
reading these four rows back through a real daemon to a connected phone — see
[e2e-harness-stream-interactive-harness-pattern-startstreamin.md](e2e-harness-stream-interactive-harness-pattern-startstreamin.md)
for the kill/respawn shape it reuses and the assertion-message lesson it adds on top.

**Absent is not present-and-empty, and a map is what makes that literal.** The canned
list carries two entries transcribed verbatim from
`internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` — a `sonnet` entry
with the full eight-key shape (five effort levels, `supportsAutoMode: true`) and a
`haiku` entry with only four keys, `supportedEffortLevels`/`supportsAutoMode` **absent as
JSON keys**, not `false` or `[]`. `internal/protocol`'s `ModelOption.MarshalJSON` (#1704)
already decided that absent and empty both publish as `[]` on the *wire*; the
daemon-internal reading is settled too — `turnevent.ModelOption.EffortLevels` reads
absent, `null` and published `[]` as one reading, `nil` (#1828), and
`turnevent.ModelOption.SupportsAutoMode` reads absent, `null` and explicit `false` as one
reading, `false` (#1819). The collapse is the *decode*'s to perform (`emitModelList`'s
`boundEach` for the list, a plain `bool` needing no code at all for the flag — see
[streamsup-package.md](streamsup-package.md)), so the minimal entry's job is to keep
feeding that decode the one shape claude actually sends: a fake that emitted `[]`/`false`
for the minimal entry instead of omitting the keys would hand the decode an
already-collapsed input and the absent-key arm would go unexercised. Each canned entry is
a `map[string]any`, like `writeInterruptAck`/`writeRateLimitEvent`: a struct with
`omitempty` would conflate `false` with absent for exactly the field this ticket exists to
keep distinct.

**The `request_id` echo depends on going through `writeJSONLine`, not `fmt.Sprintf`.**
`requestID` is inbound bytes the daemon wrote to this child's stdin, reflected straight
back onto stdout, which the daemon's stream parser reads as line-delimited JSON.
`json.Marshal` escapes it, so a `request_id` carrying an embedded newline plus a forged
envelope lands as one escaped string inside one physical line; built by string
concatenation instead, that same value would split the output and fabricate an extra
stream line the parser would consume as real — the same property `writeAssistantEcho`
already depends on for the echoed prompt.

The mode-independence claim (fires in both `honorInterrupt` states) turned out to have a
two-sided mutation proof, and the sides are not symmetric with what you'd guess from the
placement rule alone: a mutant that gates the arm *inside* the `honorInterrupt` branch
reddens only the **default-mode** row (the arm never runs there); a mutant that reaches
the arm *only when `honorInterrupt` is false* reddens only the **interrupt-mode** row.
Both rows earn their place, each catching the opposite mis-gating — worth knowing before
trimming a two-row table that looks redundant from the code alone.

Pinned by the untagged `initialize_control_test.go` (`stream_detect_test.go`'s
no-build-tag discipline, so `make check` runs it in both the `test` and `e2e` targets):
both stream modes get the double-nested ack, both canned key sets are attested by
re-walking `internal/e2e/realclaude/testdata/initialize_control_v*.json` (never trusting
that capture's own `models_entry_fields` union summary, which cannot see per-entry
shape), and an unknown-subtype `control_request` is confirmed unchanged. The capture is
a live developer recording (`argv` carries a home path, the inner payload carries an
`account` object) — the test's failure messages print key **names** only, never a
decoded entry or file dump, to avoid republishing that into CI output on a red run.

### Stream-path stdin tee (`PYRY_FAKE_CLAUDE_STDIN_LOG`, #1137, per-child since #1331)

`PYRY_FAKE_CLAUDE_STDIN_LOG` already existed for the PTY path (`startStdinReader`,
above). #1137 extended the *same* env var to the stream-json branch: when set, the
stream dispatch wraps `os.Stdin` in `io.TeeReader(os.Stdin, syncWriter{f})` before
calling `runStreamJSON`, so every byte the daemon writes to the child's stdin — user-
turn envelopes, interrupt `control_request`s — is appended to the log. `syncWriter`
is a tiny `io.Writer` (`Write` → `f.Write` → `f.Sync`) mirroring the PTY reader's
per-write `Sync`.

**The value's meaning differs by mode.** On the PTY path the env value is still the
literal file every child appends to, unchanged. On the stream path (since #1331) the
value is a path *stem*, not a file: each child tees to `<stem>.<the session id its
own argv was pinned to>`, derived by the pure helper `streamStdinLogPath(stem, args)`
on top of `argvSessionID`'s existing stem guard (`main.go:1181`, rejects `/`, `\`,
`.`) — reused rather than re-implemented, which is what makes splicing the raw argv
value into a path safe. A child whose argv carries no usable id (unreachable via
`streamsup.buildArgs`, which always appends `--session-id`/`--resume`) falls back to
`<stem>.unattributed` — a rendering choice, not a defense.

\#1137 originally had the bootstrap child and a later fresh post-rotation child
(after a stream `new_session`) accumulate into one shared log, on the theory that a
needle proved the turn was received. #1331 retired that: the daemon's process env is
inherited identically by every child, so a needle in a shared file proved only that
*some* child received a turn, never which one — and when the *outgoing* child got it
instead of the fresh one, the test that reads this log went green on the wrong
evidence. Per-child files close that gap by construction: each is written by exactly
one child's single stdin-read loop, so a needle can only appear in the file the
child that actually received it wrote. Keying on the *session id* (not pid or spawn
ordinal) means a same-session crash-respawn (`--resume <sameID>`) still appends to
one file — the file identifies a session, not a process. Same move #1195 already
made for the per-child JSONL trigger path (below): stop sharing one path, key it on
the child's own argv stem.

`O_APPEND | O_CREATE | O_WRONLY, 0o600` and the per-write `Sync` are unchanged in
both modes — append is still load-bearing for same-session respawns, and the fsync
is still what makes a sibling process's `os.ReadFile` see bytes promptly.

The tee lives at the `main()` call site, not inside `runStreamJSON` — the function
keeps its pure `(io.Reader, io.Writer, bool) ` I/O seam, so the #1140 unit test and
the #1136 interrupt rider are byte-identical regardless of this tee's shape. With
the env unset (every caller except the one test that sets it) the branch is exactly
`runStreamJSON(os.Stdin, os.Stdout, honorInterrupt)` — unchanged.

This is the runtime oracle for proving a stream `new_session` never types `/clear`
to a child, and — since #1331 — for proving *which specific child* received a given
turn: on the stream path `new_session` is a process re-spawn
(`(*streamsup.Runner).RestartFresh`, #1124), never a keystroke, so a child's log
should contain a user-turn marker (non-vacuity) but never the substring `/clear`,
and the post-rotation child's own file — not any shared file — is what a "did the
fresh child receive turn N" assertion must read. See [codebase/1137.md](../codebase/1137.md)
and [codebase/1331.md](../codebase/1331.md).

### Stream-path startup hold (`PYRY_FAKE_CLAUDE_STREAM_HOLD`, #1138)

The stream path does not pace on commit: `streamsup.WriteTurn` writes the user-turn
envelope to the child's held-open stdin and returns on **write**, not on the child
reading it or replying — unlike the PTY path's `WaitReady`-gated delivery, there is
no way to hold a queue backlog open by making a *response* slow. `PYRY_FAKE_CLAUDE_STREAM_HOLD`
gives the stream path the same "come up busy, release on trigger" shape idle-trigger
mode (#792) gives the PTY path, but at **startup**, before any stdin is read at all —
so that queued turns buffer in the daemon-to-child pipe rather than needing a
per-turn hold that would coalesce turns at the emitter.

Placement is the call site in `main()`'s stream branch, immediately before
`runStreamJSON`, guarding a small poll helper:

```go
if hold := os.Getenv(envStreamHold); hold != "" {
    waitForTriggerFile(hold) // os.Stat poll, mirrors the emit*IfTriggered pattern
}
runStreamJSON(stdin, os.Stdout, os.Getenv(envStreamInterrupt) != "")
```

- **`waitForTriggerFile(path string)`** loops `os.Stat(path)` every `pollInterval`
  until it succeeds, then returns. It never removes the trigger — nothing re-reads
  it, unlike the rotation/idle triggers.
- **`runStreamJSON`'s signature and body are unchanged.** The hold sits above the
  call, the same discipline #1137's stdin tee uses ("the tee/hold lives at the call
  site so `runStreamJSON` keeps its pure I/O signature") — so `stream_detect_test.go`'s
  7 call sites and the #1136/#1137/#1140/#1141 stream specs are unaffected.
- **Composes with the #1137 tee.** The hold check runs after the tee is wrapped
  around `os.Stdin` (`stdin` may already be a `TeeReader`), so a test combining both
  envs still logs bytes written during and after the hold.
- **Why a startup hold and not an in-turn hold.** Holding a turn's *response* open
  (rather than the child's stdin-read loop) cannot create an observable backlog on
  this path — `WriteTurn` returns regardless of whether the child is reading — and
  holding multiple in-flight turns open at once would coalesce into one turn at the
  emitter (no `turn_end` between echoes). Parking the child before its read loop
  starts lets the daemon's pipe writes buffer normally (they always buffer; `WriteTurn`
  returns `nil` either way) and produces one clean `responding→delta→turn_end→idle`
  cycle per queued turn, in FIFO order, on release.
- **When unset, byte-identical to today.** No allowlist change (pure `os.Stat` poll,
  no stdout write).

See [codebase/1138.md](../codebase/1138.md) for the live queue-drain e2e this mode
feeds — the all-three-acks-before-release vacuity gate, and why submission order is
proven via the FIFO stdin-pipe chain rather than `queue_state` depth.

### Approve rider (`PYRY_FAKE_CLAUDE_STREAM_APPROVE`, #1139)

`PYRY_FAKE_CLAUDE_STREAM_APPROVE` (default-off) is a rider on stream mode, mutually
exclusive with the interrupt rider — a turn either does the approval dance or the
plain echo. Where every other mode/rider *reacts* to stdin, this one *originates* a
request: on each `{"type":"user",…}` turn it writes an assistant `tool_use` block
for the gated call, **then** calls `control.Approve` — the same client
`pyry mcp-approve` calls — directly against the daemon's control socket, blocking
until the daemon answers allow/deny, then reflects the verdict into its assistant
echo instead of parroting the prompt.

**The `tool_use` block precedes the dial, on purpose (#1918).** Real claude emits a
gated call on the stream before the permission gate gets to it — Claude Code's
enforcement sits *between* the model's `tool_use` emission and the tool's
`tool_result`, per the permissions reference quoted in
[agentrun-selfcheck-package.md](agentrun-selfcheck-package.md), and every committed
`permission_protocol_*` capture shows the call as its own assistant line ahead of its
`tool_result`. `writeAssistantToolUse(w, msgID, toolUseID)` reproduces that shape:
one line, `{"type":"assistant","message":{"id":…,"role":"assistant","content":[{"type":"tool_use","id":…,"name":…,"input":…}]}}`,
reusing the turn's existing `msgID` — the captures show a message's thinking-block
and `tool_use` lines sharing one id, so this is a shape real claude produces, not an
invented one. The tool name and input are package consts (`approveToolName`,
`approveToolInput`) that both `writeAssistantToolUse` and `dialApproval` read, so
the block on the stream and the payload dialed into `control.Approve` cannot drift
into describing two different calls — the writer takes no name/input parameter, so
nothing steers them independently. The block's envelope is a parallel typed family
(`outToolUseBlock`, `outAsstToolMessage`, `outAssistantToolUse`), not a widened
`outAsstMessage.Content` — that field stays `[]outTextBlock` and `writeAssistantEcho`
and its three other callers are untouched.

No `tool_result` line follows the block; nothing needs one. `setBusy`'s close arm
(#1917's retention) sweeps a conversation's whole retained-call set on `TurnEnd`,
which the rider's existing `result` line already produces, so the unmatched
`ToolStart` cannot leak.

**An "arrives before X" claim spanning two transports has to be observed from the
far side, not from the emitted bytes.** The block reaches the daemon on the child's
stdout; the approval reaches it on the control socket — two transports, two
goroutines, so nothing orders their arrival at a client (don't assert it). But
*within the rider*, "block before dial" is also unprovable from stdout alone: a
rider that dialled first and wrote the block second would emit byte-identical
output, since the block's bytes don't depend on the verdict. The only place the
ordering is visible is the dial's own arrival: a stub control-socket server that
snapshots the rider's (mutex-guarded) output writer the instant a request lands,
then asserts the snapshot already contains a parsed `tool_use` block. A snapshot
with no `ToolStart` in it is a rider that dialled before writing.

```go
if os.Getenv(envStreamApprove) != "" {
    runStreamJSONApprove(stdin, os.Stdout, os.Getenv(envApproveSocketFile))
    return
}
runStreamJSON(stdin, os.Stdout, os.Getenv(envStreamInterrupt) != "")
```

`runStreamJSONApprove` duplicates `runStreamJSON`'s ~15-line read loop rather than
widening its signature — same discipline as the stdin tee and startup hold: the
tested seam (`runStreamJSON`) stays byte-identical for the send/interrupt/queue
siblings.

**Socket-in-a-file.** The daemon's control socket is a random per-spawn path
(`shortSocketPath`), unknown before spawn and not derivable from the child's env or
cwd. `PYRY_FAKE_CLAUDE_APPROVE_SOCKET_FILE` carries a *file path* instead (known
pre-spawn, chosen by the test); the test writes the real socket path into that file
after `StartStreamInteractiveWithRelay` returns, and `dialApproval` reads it lazily,
at dial time — mirroring the existing `PYRY_FAKE_CLAUDE_*_TRIGGER` file idiom.

**Verdict reflection oracle.** `dialApproval` maps the `control.Approve` outcome to
one of three needles `writeVerdictResponse` writes into the assistant text (which the
daemon's stream parser turns into an `assistant_delta` the client observes):

| Outcome | Needle |
|---|---|
| daemon verdict `allow` | `approve-allow` |
| daemon verdict `deny` | `approve-deny` |
| `control.Approve` error (unreachable socket, ctx expiry, unrecognised `Behavior`) | `approve-error` (fail-closed, mirrors `mcp_approve.go`'s error→deny) |

`approve-error` is tagged **distinctly** from `approve-deny` so an e2e can tell a
genuine daemon deny (the fail-closed proof) from a client-side failure — the two
must never be confused, since a masked client error could otherwise false-pass a
timeout assertion. `approveDialTimeout` (30s, fixed) is deliberately far above the
daemon's approval window in the e2e's timeout case (`PYRY_APPROVAL_TIMEOUT=2s`), so a
no-answer turn's deny is always the **daemon's** `permbridge` timer firing, never a
fake self-timeout that would mask the daemon's verdict.

This rider is what a live `interactive_runner:"stream-json"` claude would do if the
runner wired `--permission-prompt-tool`/`--mcp-config` into the child spawn — which
it currently does not (`internal/streamsup.buildArgs` omits that pair; only the
`pyry agent-run` batch verb wires it, see [pyry-mcp-approve-command.md](pyry-mcp-approve-command.md)).
So fakeclaude calls `control.Approve` directly rather than spawning its own
`pyry mcp-approve`, exercising the identical daemon-side surface
(`mcp.approve` → `permbridge` → `streamApprovalBridge` → `modal_shown` → answer →
verdict) without depending on that still-open wiring gap. See
[codebase/1139.md](../codebase/1139.md) for the live e2e this feeds and the
production follow-up this gap is tracked under.
