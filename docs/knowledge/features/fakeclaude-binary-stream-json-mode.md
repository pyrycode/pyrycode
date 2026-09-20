# Stream-json mode (#1140)

Every mode above models claude's **PTY/TUI** surface — a screen to read, a
`<uuid>.jsonl` transcript to grow. The daemon's `internal/streamsup` runner (the
`interactive_runner: "stream-json"` toggle, #1081, shipped) instead spawns claude
**headless over a pipe**: line-delimited stream-json envelopes on stdin, `assistant`/
`result` lines on stdout, no PTY and no transcript file at all. `PYRY_FAKE_CLAUDE_STREAM_JSON`
teaches fakeclaude that wire, so the stream path has a fake to drive it end-to-end —
the harness piece the sibling #1135 (blocked-by this ticket) rides for its first
`send_message` e2e spec.

When `PYRY_FAKE_CLAUDE_STREAM_JSON` is set, `main()`'s **very first** branch is:

```go
if os.Getenv(envStreamJSON) != "" {
    replay, err := loadStreamReplay(os.Getenv(envStreamReplayFirst),
        os.Getenv(envStreamReplaySecond), os.Getenv(envStreamReplaySignal))
    if err != nil {
        fatalf("load stream replay: %v", err)
    }
    // Other stream-only riders are loaded here too.
    runStreamJSONConfigured(stdin, os.Stdout, streamRunConfig{
        replay: replay, inputCloser: os.Stdin,
    })
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

`runStreamJSON` remains the source-compatible test seam for direct callers: it
packages the established rider arguments into `streamRunConfig` and delegates to
`runStreamJSONConfigured`, which owns the read→emit loop. The `io.Reader`/`io.Writer`
seam (rather than hard-wiring `os.Stdin`/`os.Stdout`) still lets unit tests drive it
against in-memory buffers. `honorInterrupt` (default `false`, wired from
`envStreamInterrupt`, #1136) selects the interrupt rider — see § Interrupt mode
below; this table describes the default path:

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
- **Synchronous by default, no raw mode.** Replay-absent and single-fragment replay
  run entirely on `main()`'s goroutine. Only two-fragment replay adds the bounded,
  closable stdin-reader handoff described below. Stream-json travels over a **pipe**,
  not a PTY, so canonical line discipline / CR mapping don't apply —
  `enterRawMode()` is never called in this mode.
- **Default mode still ignores non-`"user"` lines unless a control-request arm
  recognizes them.** `initialize` (#1692), `get_context_usage` (#2289),
  `get_settings` (#2517), `set_permission_mode` (#2067), and `set_model` each get a
  canned answer regardless of `honorInterrupt`; an `interrupt` request is still
  dropped in default mode and is handled only by the rider below (#1136). A normal
  production control query must be answered here: silently ignoring `get_settings`
  parked the requesting connection's worker until its deadline. Malformed and unknown
  controls, plus new_session / queue / modal lines, remain out of scope for the fake.
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

### Controlled raw-output replay (#2503)

The replay rider lets a deterministic harness feed the production stream runner an
exact child-stdout fixture instead of fakeclaude's canned echo/result pair. It is
stream-only and default-off; the `PYRY_FAKE_CLAUDE_STREAM_JSON` gate still returns
before every PTY, sessions-directory, transcript, and PTY JSONL-trigger path.

| Variable | Contract |
|---|---|
| `PYRY_FAKE_CLAUDE_STREAM_REPLAY_FIRST` | Required to enable replay; path to the first raw stream-json fragment |
| `PYRY_FAKE_CLAUDE_STREAM_REPLAY_SECOND` | Optional path to a second raw fragment; valid only with `..._RELEASE` |
| `PYRY_FAKE_CLAUDE_STREAM_REPLAY_RELEASE` | Filesystem signal path paired with `..._SECOND` |

`loadStreamReplay` reads both fragments before the input loop. Unreadable files fail
startup, and a second fragment without a release path (or vice versa) is rejected.
The release file carries no content; its existence is only a signal. Fakeclaude
neither decodes nor line-splits the fragments, so missing trailing newlines,
malformed JSON, and unknown variants reach stdout unchanged for negative tests.

The first recognized user envelope starts one scripted turn and writes the first
fragment once instead of a canned reply. With no second fragment, replay completes
immediately. Otherwise later user envelopes are consumed without canned replies
until the release path exists; recognized control requests still use the ordinary
dispatch. The signal writes the second fragment once, after the first and without
another user envelope, then is removed best-effort and disabled in memory. A
`result` line there can finish the held turn. Later turns return to canned behavior,
so replay emits at most two configured fragments, each once.

A release poll after blocking `ReadString` would need another stdin envelope to make
progress. In two-fragment mode `startAsyncStreamReader` instead hands reads to the
main loop, which selects them against the release ticker and remains the sole stdout
writer. The closer interrupts a blocked read and the stop channel releases a blocked
handoff. Other modes start no reader goroutine.

Mobile #613 should set `PYRY_FAKE_CLAUDE_STREAM_JSON=1` and
`PYRY_FAKE_CLAUDE_STREAM_REPLAY_FIRST=<first-fragment-path>`. For a gated finish it
must additionally set both `PYRY_FAKE_CLAUDE_STREAM_REPLAY_SECOND=<second-fragment-path>`
and `PYRY_FAKE_CLAUDE_STREAM_REPLAY_RELEASE=<signal-path>`, then create the signal
file only when the second fragment should be emitted. It must not set a PTY transcript
or `PYRY_FAKE_CLAUDE_JSONL_TRIGGER` for this flow.

Raw byte equality proves malformed and future lines were not normalized away;
`streamsup.Parser` assertions prove valid lines exercise the production consumer.
Keep both. Recreating the release signal proves only second-fragment one-shot
behavior; a post-completion user envelope is what proves canned replies resume.

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

### Context-usage control request answer (#2289)

`runStreamJSON` answers `get_context_usage` unconditionally in both its default and
interrupt modes. `contextUsageRequestID` recognizes only the two detail values observed
in `context_usage_v2.1.259.json`, `summary` and `full`; both select the same
`writeContextUsageAck` payload because the capture returned identical payloads for the
two arms. A subtype-only match was too broad here: it would turn an absent or invented
`detail` into a successful request and make malformed-request coverage falsely green.
Decode failures, another envelope type or subtype, and missing or unknown detail values
therefore still produce no output.

The response keeps claude's inverted, double-nested success hierarchy:

```text
control_response
└── response: {subtype: "success", request_id: <echoed>, response: {...}}
    └── response: {model, totals, percentage, categories, mcpTools, memoryFiles, ...}
```

`writeContextUsageAck` goes through `writeJSONLine`, so the caller-supplied
`request_id` remains one escaped JSON string on one physical line. The inner
`cannedContextUsagePayload` preserves the capture's scalar spellings (`model`,
`totalTokens`, `maxTokens`, `rawMaxTokens`, `autocompactSource`, `percentage`,
`autoCompactThreshold`, and `isAutoCompactEnabled`) and the per-entry vocabulary of
`categories`, `mcpTools`, and `memoryFiles`.

The values are deliberately a consumer stress fixture rather than a miniature realistic
session: `cannedContextUsageMCPTools` returns 33 entries, an MCP tool name and a memory
path exceed 256 bytes, and every list's token counts arrive out of descending order.
That combination distinguishes the required sort-then-cut behavior from either cutting
the input first or accidentally relying on fixture order; a short or already-sorted
fixture would let both bugs stay green. Memory paths live only below the fictional
`/__pyry_fake__/memory/` root, and `TestContextUsageCannedPathsAreFictional` separately
rejects any path rooted under the executing user's home so a live capture cannot leak an
operator path into the canned response.

`TestRunStreamJSON_ContextUsageAnswer` pins one response, correlation, and identical
payloads across the two details and two `honorInterrupt` states.

### Set-permission-mode control request answer (#2067)

The daemon is gaining a spawn-time posture gate (#2064): write the session's stored
permission mode onto every spawned child's stdin as a `set_permission_mode`
`control_request`, then refuse every user turn until claude acks it. Answered on the
same terms as `initialize` above — **unconditional**, in an `else if` arm beside it in
`runStreamJSON`'s dispatch, not gated by any rider — for the same reason: once #2064
starts sending the line, every session in the fake-daemon suite arms the gate (an e2e
session's stored posture canonicalises to `default`), so a fake that never answered
would refuse every turn forever. Nothing sends the request yet, so this ticket changes
no existing suite's bytes.

The ack envelope is transcribed from claude's own committed answer
(`internal/e2e/realclaude/testdata/bypass_reescalation_v2.1.239_reescalate.json`, one
of eleven `set_permission_mode` acks across ten committed captures): `subtype` and
`request_id` nested under `response` (the inverse of the request side, same shape
`writeInterruptAck`/`writeInitializeAck` use), wrapping `{"mode": <echo>}` one level
deeper still at `response.response`. `set_permission_mode_control_test.go` cross-checks
the fake's own key sets against a re-walk of the captures (8+ matched acks, 2+ distinct
correlated modes as non-vacuity floors) rather than trusting the shape by inspection.

**Both echoed values — `request_id` and `mode` — are verbatim and unvalidated.** A
request naming a mode outside claude's vocabulary, or carrying no `mode` key at all, is
answered with whatever it carried: absent decodes to `""` and is echoed as
`"mode":""`, present-and-empty rather than corrected or refused. This is deliberately
**not** checked against `streamsup.permissionModeAllowed`: that allow-list is the
daemon's own *outbound* defence against minting an escalating line, and a fake that
re-applied it *inbound* could no longer reproduce what the daemon actually sends —
`bypass_reescalation_v2.1.239_reescalate.json` is real claude answering a
`bypassPermissions` request, a mode the list does not admit, with a plain success. An
inbound filter would make that transcript unreproducible, which is precisely what
\#2064's ack correlation has to observe.

**A second field off an already-parameterised decode is where a duplicate decode gets
written — push the guard down instead.** `controlRequestID(line, subtype)` already
existed as the one place the `type`/`subtype` check happens (generalised from
`interruptControlRequest` by #1692, itself following `argvSessionID` → `argvIDFlag`'s
extract-the-shared-half-into-a-parameter move, #1631). This arm needed a *second* field
(`mode`, a sibling of `subtype` under `request`, not top-level beside `request_id`) —
not obtainable by widening `controlRequestID`'s return without breaking its other two
callers. The guard moved one level down instead: `decodeControlRequest(line, subtype)
(inControlRequest, bool)` is now the sole decode and the sole type/subtype check;
`controlRequestID` became a thin wrapper reading `.RequestID` off it, signature and all
three existing callers unchanged; `setPermissionModeRequest(line) (requestID, mode
string, ok bool)` is the new per-subtype wrapper reading both fields. Writing a second
`json.Unmarshal` in a standalone function would have been exactly the drift
`controlRequestID`'s doc already warns against.

**The withheld-mode-ack rider (`PYRY_FAKE_CLAUDE_STREAM_WITHHOLD_MODE_ACK`,
`withholdModeAck`, default-off)** lets a caller drive a child that never confirms its
posture — the shape #2064's turn-gate e2e needs. It is the only rider on this file that
*suppresses* a line rather than adding one, and the only one riding an answer that is
otherwise unconditional, which is why the check sits **inside** the dispatch arm rather
than on its condition: the request must still be *read and consumed by this arm*, only
the ack withheld. Gating the arm's condition instead would let the line fall through to
whatever arm follows it — inert today, since nothing does, but a latent bug the moment
one is added, and no test today could tell the two apart (both compile, both pass,
because nothing follows the arm that would match the line). Unset ⟹ off ⟹ the ack is
emitted.

**A rider that suppresses a line needs a follow-on turn in its test to mean anything.**
A bare byte-count assertion (`on` produces zero `control_response` lines) passes just
as well for a rider implemented by `return`-ing out of the read loop entirely — the
child is dead, and a dead child also emits no ack. The test that actually kills that
mutant feeds the `set_permission_mode` request **followed by a user turn** and asserts
the turn's reply still lands while the ack does not; that is also the literal property
the consumer needs (a child that is alive but unconfirmed), so the stronger test and
the correct spec turned out to be the same test.

**CLOSED by #2064: the approve rider's read loop (`runStreamJSONApprove`, § Approve
rider below) now answers `set_permission_mode`, not `initialize`.** It was, and
remains, a separate, wholly duplicated read loop from `runStreamJSON` (see § Approve
rider) — the gap this entry used to flag was exactly the predicted failure:
`internal/e2e/relay_v2_stream_modal_test.go` spawns its child with the approve rider
on, and the moment #2064's spawn-time gate went live, that child could never ack its
posture and the modal round-trip failed on its deadline reporting no modal — a
failure that reads as approval-wiring breakage and is not one (measured: 9.3s on
`origin/main`, 81s failing without the arm, 9.5s passing with it). `initialize` is
deliberately still unanswered there — nothing gates a turn on that ack, so answering
it would be untested surface added on symmetry alone.

Not gaining a NAK arm: `streamsup.parser.go` records that a `set_permission_mode` NAK
exists on the wire (an `error` string, no inner response), but no AC here asks for one,
\#2064 needs an *ack* to correlate, and a NAK would need its own rider to be reachable
at all. Left for whichever of #2064/#2065 first needs to drive a refusal.

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
siblings. **That duplication is why this rider answered neither `initialize`
(#1692) nor `set_permission_mode` (#2067) for a while** — both live only in
`runStreamJSON`'s dispatch, and this loop's own doc comment claimed non-user lines
are ignored "exactly like runStreamJSON," which stopped being true at #1692 and more
so at #2067 without this loop noticing. #2064 made the gap fatal rather than latent
(a child spawned with this rider on never acked its posture once the spawn-time gate
started requiring one) and added the `set_permission_mode` arm here; `initialize`
stays unanswered on purpose — nothing gates a turn on that ack. **General lesson: a
doc comment asserting two code paths behave "exactly like" each other rots the
moment either path is taught something new** — the fix has to grep for every
duplicated read loop a new dispatch arm needs, not just the one path the ticket
names. See [streamsup-package-posture-gate-spawn-permission-mode-ack.md](streamsup-package-posture-gate-spawn-permission-mode-ack.md).

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

### Stdio permission rider (`PYRY_FAKE_CLAUDE_STREAM_CAN_USE_TOOL`, #2283)

`PYRY_FAKE_CLAUDE_STREAM_CAN_USE_TOOL` is the default-off rider for claude's other
permission path: the `--permission-prompt-tool stdio` control exchange. Its value
is a JSON object containing the inner `can_use_tool` request fields. On each user
turn, fakeclaude wraps those fields in one newline-terminated `control_request`,
fixes the subtype to `can_use_tool`, assigns the deterministic non-empty id
`permission-<turn>`, and waits on stdin before emitting any terminal turn output.
This is deliberately separate from the approve rider above: the request and answer
both travel over the child's stream-json pipes rather than the control socket.

The accepted keys are exactly the fields decoded by `streamsup.CanUseToolRequest`:
`tool_name`, `input`, `tool_use_id`, `agent_id`, `permission_suggestions`,
`blocked_path`, `decision_reason`, `decision_reason_type`, `matched_ask_rule`,
`classifier_approvable`, `suppress_always_allow_rule`, `default_to_no`, `title`,
`display_name`, `description`, and `requires_user_interaction`. String and boolean
fields must have their corresponding JSON scalar type; the production codec's
opaque fields retain any valid JSON shape. Malformed JSON, a non-object or `null`,
an unknown key, a wrong scalar type, or a caller-supplied `subtype` disables the
rider silently and leaves the ordinary assistant/result transcript byte-identical.

**Presence belongs to the fixture, not to a Go zero value.** The configuration is
kept as raw JSON until the request is marshalled. An omitted key therefore remains
absent, an explicit `false` remains present, and object/array-valued fields do not
get flattened or retyped. A typed config struct with `omitempty` would make the
explicit-false test pass through the same representation as omission and would
remove the distinction later permission slices need to exercise.

Only a nested successful `control_response` whose `request_id` matches the one
outstanding ask can resolve the turn. Other ids, non-success envelopes, malformed
lines, and unknown behaviours are consumed without terminal output; a later
matching answer can still resolve it. `runStreamJSON` keeps this state in its
single reader/writer loop, so at most one permission ask is outstanding.

The terminal transcripts intentionally make the two decisions distinguishable:

| Matching answer | Output after the request |
|---|---|
| `allow` without `updatedInput` | assistant `tool_use` carrying the requested `input`, then `approve-allow`, then `result{success}` |
| `allow` with `updatedInput` | assistant `tool_use` carrying that exact JSON value, then `approve-allow`, then `result{success}` |
| `deny` | `approve-deny`, then `result{success}`; no `tool_use`, so denial cannot be mistaken for a permitted call |

The correlation test must include an unrelated response before the matching one:
testing only a matching id would stay green if the fake accepted the first response
it saw. The request-shape test likewise asserts raw keys, not only decoded values,
because a normal Go decode cannot distinguish omitted optional fields from their
zero values.

### Session-facts init line rider (#2315)

`PYRY_FAKE_CLAUDE_STREAM_SESSION_FACTS` (default-off) makes stream mode prepend one
`system`/`init` line — the line claude opens every turn with, and the one this file
wrote no form of before — ahead of each turn's reply, on the rate-limit rider's
"before the reply" discipline: a `turn_end` reaching a client implies the parser has
already seen the line, so the consuming e2e needs no sleep or poll. First among the
prepends because that is where claude itself puts it. `emitInit` is the tenth
parameter on `runStreamJSON`, landing last in the `string, bool` tail alternation the
rider list has kept since #1631 (a transposed call site stays a compile error) — and
the point this family's rider list has now reached: **ten parameters, 25 call sites**,
all needing the new zero value in the same commit. The next rider is the one worth
converting to an options struct, as its own ticket rather than inside a feature's
budget.

`writeSystemInitLine` transcribes all 24 top-level keys of
`effort_init_v2.1.259_sonnet_effort.json` (`claude_code_version` `2.1.259`,
`permissionMode` `default`), not just the three `streamsup`'s decode target reads —
`interruptMarkerLine`'s doc already states why a minimal line is the wrong fixture: it
turns a presence among 24 keys into a presence among three, and here the twenty-one
extra keys — four of them naming the operator's filesystem or claude's session identity
— are the point. Three open-ended arrays (`skills`, `slash_commands`, `tools`) carry
only the capture's first three entries each; nothing downstream decodes them, so the
key's presence is the whole of what the fixture owes them, and the full lists would
bury the four keys the consuming e2e's non-leak assertion is actually about.

**The fixture's key COUNT is itself a load-bearing assertion, not shape decoration.**
`stream_detect_test.go`'s rider-on case asserts the emitted line's top-level key count
is 24, so a later hand trimming the fixture down to what the daemon decodes reddens at
the file that made the fixture, not silently downstream. See
[e2e-harness-stream-interactive-harness-pattern-startstreamin.md § relay_v2_stream_session_facts_test.go](e2e-harness-stream-interactive-harness-pattern-startstreamin.md)
for the e2e half and the non-leak assertion this fixture exists to support.
