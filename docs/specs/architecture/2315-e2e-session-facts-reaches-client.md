# #2315 — prove the `session_facts` frame reaches a connected client at the fake-daemon tier

## Files read

- `internal/e2e/relay_v2_stream_rate_limit_test.go` → `driveRateLimitTurn`,
  `TestRelayV2_StreamRateLimitBenignReachesNoPhone`,
  `TestRelayV2_StreamRateLimitNonBenignReachesPhone` — the shape this ticket copies:
  one rider-driven line, a fake phone drained to `turn_end`, a positive count paired
  against a rider-off negative. Its file doc states the zero-assertion problem this
  plan inherits.
- `internal/e2e/internal/fakeclaude/main.go` → `runStreamJSON`, `writeRateLimitEvent`,
  `writeBackgroundTaskRoster`, `writeJSONLine`, `streamSessionID`,
  `interruptMarkerLine` — the rider seam, the two nearest write-site analogues, and
  the doc that argues a full key set beats a minimal one.
- `internal/e2e/internal/fakeclaude/stream_detect_test.go` →
  `TestRunStreamJSON_RateLimitRider`,
  `TestRunStreamJSON_RateLimitRiderOffIsByteIdentical` — where this rider's own unit
  coverage belongs and what it must assert.
- `internal/streamsup/parser.go` → `systemInitLine`, `emitInitLine`,
  `emitSessionFacts`, `emitModelAnnounced`, `maxClaudeVersionField`,
  `maxPermissionModeField`, `truncateField` — the decode target (three declared keys,
  twenty-one omitted), the one-line-two-arms fan-out, and the caps that decide
  `truncated_fields`.
- `internal/turnbridge/outbound.go` → `MapEvent`'s `turnevent.SessionFacts` arm — the
  bridge that supplies `ConversationID` and passes a nil `TruncatedFields` through
  unchanged.
- `internal/protocol/interactive.go` → `SessionFactsPayload` — the four wire keys, and
  the doc naming the four captured init-line keys that are deliberately absent.
- `cmd/pyry/interactive_turn_v2.go` → the `turnevent.SessionFacts` and
  `turnevent.ModelAnnounced` arms — both push without mutating turn lifecycle, so the
  fed line cannot open or close a turn.
- `internal/e2e/harness.go` → `StartStreamInteractiveWithRelay`,
  `seedBoundConversation`, `seedBootstrapRegistry` — the harness and the
  equal-UUID invariant a mismatch turns into an unexplained timeout.
- `internal/e2e/realclaude/testdata/effort_init_v2.1.259_sonnet_effort.json` — the
  capture the fed line is transcribed from: three init lines, 24 top-level keys each,
  `claude_code_version` `2.1.259`, `permissionMode` `default`.
- `docs/knowledge/features/e2e-harness-stream-interactive-harness-pattern-startstreamin.md`
  § "Why the bootstrap pool id and the bound conversation id must be equal" — the
  lesson that decides how the two seeds are called here.

## Context

`MapEvent`'s `turnevent.SessionFacts` arm landed in #2254 and the interactive v2
emitter pushes the frame today, so `session_facts` flows on the live turn lane with
nothing proving it arrives. Every prior declare-then-emit family closed with a
separate end-to-end ticket; this is that ticket, at the hermetic tier, so `make check`
runs the proof on every commit.

The fake claude writes no `system`/`init` line of any kind today, so the proof needs a
rider before it needs a test. The rider is the deliverable's other half rather than a
ticket of its own — see the sizing note below.

The tier is deliberate. What a live claude publishes on its init line is already
measured and committed (#2251) and the parser's construction of the variant from those
bytes is already proven (#2252). What is unproven is the daemon's own plumbing from
variant to connected client, which a fake exercises completely.

No ADR is warranted: this ticket introduces no decision the rider family has not
already made four times.

### Sizing — one boundary exceeded, deliberately

The one-ticket boundary trips on exactly one line, and the plan states the overage
rather than hiding it:

| Limit | Boundary | This ticket |
|---|---|---|
| Production source files created or modified | ≤ 5 | 1 |
| Total written work | ≤ 800 | ~700 estimated, **940 actual** (see Revisions) |
| New exported types or interfaces | ≤ 5 | 0 |
| Consumer call sites needing simultaneous update | ≤ 10 | **25** |
| Acceptance criteria | ≤ 5 | 4 |
| Distinct error/reject branches | ≤ 10 | 0 |

`runStreamJSON` has 25 call sites, all of which must gain the new rider's zero value
in the same commit. Two independent rules say build anyway rather than split:

- **The floor.** The only conceivable split is (a) the rider, (b) the e2e that
  consumes it. Nothing outside the family would ever call (a), so (a) is part of (b),
  and the floor outranks the ceiling.
- **Split depth.** #2315's parent is #2254 and its grandparent #2195, so the ticket
  already sits at depth two and no further split is available.

The ticket is labelled `needs-human:sizing` so the judgement is findable on the board.

## Design

One rider in the fake, one unit test for it, one e2e for the frame. No daemon code
changes: every layer between the fed line and the phone already exists.

### The rider

`runStreamJSON` gains a tenth parameter, `emitInit bool`, appended after
`resetToID string`. The resulting `string, bool` tail keeps the function's standing
discipline — a transposed call site stays a compile error — which is why the knob is a
bool rather than a second string carrying the version. `main()` sets it from a new
`envStreamSessionFacts` (`PYRY_FAKE_CLAUDE_STREAM_SESSION_FACTS`), tested for
emptiness in `envStreamModelWindows`'s spelling, because there is nothing to count and
nothing to choose: the fixture is one canned line.

When on, each user turn writes one `system`/`init` line **before** the reply, ahead of
the other riders' prepends. Before the reply for the family's standing reason: a
`turn_end` reaching a client then implies the fed line has already been through the
parser, so the e2e needs no sleep and no poll. First among the prepends because that is
where claude itself puts it.

Per turn rather than once per process, matching claude — the capture holds three init
lines for a three-prompt run — and the e2e drives exactly one turn, so one line
produces one frame.

`writeSystemInitLine(w io.Writer) error` builds the line as a `map[string]any` and
hands it to `writeJSONLine`, the two neighbouring write sites' form: keys marshal
sorted, so the line is deterministic without declaring a struct for a shape nothing
else reads.

### The fed line

All 24 top-level keys of the capture, not the three the decode target declares. The
fake's own `interruptMarkerLine` records why: a minimal line turns a presence among 24
keys into a presence among three, and here the twenty-one extra keys are the point —
four of them name the operator's filesystem and claude's session identity, and feeding
them is what shows the frame cannot carry one.

The capture's templated identity values (`cwd`, `memory_paths`,
`messaging_socket_path`, `session_id`, `uuid`) are substituted with the fake's own, as
`writeRateLimitEvent` already substitutes. They are placeholders in the capture rather
than values to copy, and none is in the parser's decode target, so none can reach a
frame either way. `mcp_servers` is carried verbatim so #2275 can populate its own
payload from this fixture without rebuilding it.

Three constants back the assertions across the main-package boundary the e2e cannot
import: the version, the posture and the synthetic uuid. `claude_code_version` is
`2.1.259` and `permissionMode` is `default` — the capture's, byte for byte.

The key spellings are claude's: `claude_code_version` and the camel-cased
`permissionMode`. The wire spelling of the second differs by design — the daemon
publishes `permission_mode` — and that divergence is one of the things the e2e pins.

`interruptMarkerLine` is untouched. It rides the JSONL/TUI lane, where the *absence* of
a `permissionMode` key is load-bearing, and the two lanes never meet.

### What one line produces

`emitInitLine` decodes the line once and calls both `emitModelAnnounced` and
`emitSessionFacts`, so a capture-faithful line carrying `model` yields a
`model_announced` frame beside the `session_facts` one. That is expected, and it is why
the drain filters by frame type instead of asserting a frame total. Neither arm mutates
turn lifecycle in the v2 emitter, so the extra frame cannot perturb the turn the test
drives. `model_announced` keeps its own proof elsewhere; this ticket does not claim it.

Both fed values are far under `maxClaudeVersionField` and `maxPermissionModeField`, so
nothing is cut, `TruncatedFields` stays nil, and the wire value is `null` rather than
`[]`.

## Concurrency model

None introduced. The write site runs on `runStreamJSON`'s single reader goroutine —
`main()`'s — which is the property that keeps the fake race-clean by construction. No
new goroutine, no shared state, no shutdown path to add. The e2e's own concurrency is
the harness's existing one.

## Error handling

`writeSystemInitLine` returns the first marshal or write error and the caller returns
on it, exactly as the rider block's four existing arms do: a dead read end is EOF by
another name.

The rider must write a well-formed line, because `emitInitLine`'s undecodable arm
drops the line and logs no error text on purpose. AC-3's zero `unrecognized_message`
count plus AC-2's positive count are what prove the line reached the init arm rather
than the fallback or the drop.

The e2e `t.Fatalf`s only on transport and decode faults, and asserts its milestones
before its counts: a count read off a run where nothing completed proves nothing.

## Testing strategy

**Rider unit coverage** (`internal/e2e/internal/fakeclaude/stream_detect_test.go`,
where #1411 put the rate-limit rider's):

- Rider on, one turn: three lines out — init, echo, result. Line 0 decodes; `type` is
  `system` and `subtype` is `init`; the top-level key count is 24; `claude_code_version`
  and `permissionMode` are the constants verbatim; `session_id` is the fake's, not the
  capture's placeholder. The key-count assertion is what reddens if a later hand trims
  the fixture down to what the decode target reads.
- Rider off: output is byte-identical to the untouched two-line path, and the on-run's
  tail matches it — the pair `TestRunStreamJSON_RateLimitRiderOffIsByteIdentical` uses.

**End-to-end** (`internal/e2e/relay_v2_stream_session_facts_test.go`, the fake-daemon
tier's `relay_v2_stream_<frame>_test.go` naming):

One helper drives a stream-interactive daemon with an interactive-capability phone
attached and returns what one turn put on the wire — every `session_facts` payload, a
count of the unrecognized lane, and the echo and `turn_end` milestones as values rather
than helper-side assertions. Two tests share it:

- Rider on: milestones first and fatally; `unrecognized` is zero; exactly one
  `session_facts` frame; its `conversation_id`, `claude_code_version` and
  `permission_mode` are the expected bytes; `truncated_fields` is nil, which
  distinguishes `null` from `[]`; and the payload's raw key set is exactly the four
  keys `SessionFactsPayload` declares.
- Rider off: milestones first and fatally; zero `session_facts` frames; zero
  unrecognized. This is the non-vacuity guard for the count above.

The handshake must echo the `interactive` capability. A phone that did not receives
zero frames of either kind, which would make the rider-off test vacuously green — the
hazard the rate-limit pair's file doc names.

The two seeds must carry the same UUID. A mismatch drops every event at the drain gate
and presents as an unexplained timeout rather than a clean failure at the seed call,
per the stream-interactive harness overview.

## Open questions

- Whether the init rider should write before or after the other per-turn prepends when
  two riders are on at once. No test drives two, so the choice is unobservable today;
  resolved to "first", matching claude. Revisit only if a ticket needs both.

## Revisions

**2026-09-10 — a second boundary came in over, on the line count.** The sizing table
estimated ~700 lines of total written work against a ceiling of 800; the branch landed
940 across all six files, with the spec at 283 and the e2e at 366. The gap is comment
weight in the two test files and in `writeSystemInitLine`, not extra behaviour: no file
outside the plan was touched and nothing was built that the plan did not prescribe.
The overage did not cost the run — plan and implementation together used well under a
quarter of the turn budget — but the estimate was wrong and the table now records both
numbers rather than the one that flattered the plan.

**2026-09-10 — the three open-ended arrays carry a prefix, not the full lists.**
The Design section above says the fed line carries all 24 top-level keys, which it
does, and every scalar value is the capture's verbatim. `skills`, `slash_commands` and
`tools` carry the capture's first three entries each rather than its 17, 50 and 29:
nothing between the fed line and the wire decodes any of them, the key's PRESENCE is
the whole of what the fixture owes them, and ninety lines of tool names would bury the
four keys the non-leak assertion is about. `mcp_servers` is verbatim, because #2275
reads it. Recorded here rather than left implicit because "transcribed from the
capture" is a provenance claim, and this is the one place it is a prefix.

## Security review

**Verdict:** PASS (second pass; the first found one MUST FIX, resolved in the Testing
strategy above before this plan was committed)

**Findings:**

- [Trust boundaries] MUST FIX, **resolved**. The fed line is the design's whole trust
  boundary: subprocess stdout → `emitInitLine`'s decode → `MapEvent` → an encrypted
  frame. The first draft asserted only the three values it expected to arrive, which
  proves arrival and says nothing about the twenty-one keys that must NOT. A frame that
  had grown a `cwd` would have passed. Resolved by asserting the payload's raw key set
  is exactly `conversation_id`, `claude_code_version`, `permission_mode`,
  `truncated_fields` — the check `writeBackgroundTaskRoster`'s doc already claims an
  e2e owes this fixture class. The boundary itself is explicit and single: `streamsup`'s
  `systemInitLine` declares three keys, so a field never decoded cannot leak.
- [Tokens, secrets, credentials] No findings. The line carries no credential. The
  capture's `$WORKDIR`, `$TEMP_HOME`, `$SESSION_ID` and `$MESSAGING_SOCKET` are
  placeholders, replaced by the fake's own synthetic values; SHOULD FIX for Phase B —
  build the line with `json.Marshal` over a map and never through a shell or
  `os.Expand`, so a `$` stays an inert byte, the rule `writeBackgroundTaskRoster` states
  for its captured `cat $FIFO` row.
- [File operations] Not applicable by design: the rider writes to an `io.Writer` and
  touches no path. The e2e's only writes are the harness's existing seeds, at their
  existing `0600`.
- [Subprocess / external command execution] No findings. The knob is tested for
  emptiness and never parsed, never reaches argv, never reaches a shell. It joins the
  harness's `extraEnv` set, which the daemon passes to the child it already spawns; no
  new inheritance and no new scrubbing decision.
- [Cryptographic primitives] Not applicable by design: no randomness is introduced.
  The uuid is a fixed synthetic literal precisely so the fixture is deterministic and
  the assertion cannot pass on another line's content.
- [Network & I/O] No findings. The fed line is ~2 KB against `defaultMaxParseBuf`'s
  4 MiB, and the resulting frame's two claude-derived fields are bounded at
  construction by `maxClaudeVersionField` and `maxPermissionModeField` — ~0.8% of the
  v2 application-envelope cap, arithmetic those constants already carry. This plan
  re-decides no cap.
- [Error messages, logs, telemetry] No findings. `emitInitLine` deliberately logs no
  error text, because `encoding/json` quotes the offending bytes into it; nothing here
  changes that arm or adds a log. The tests' failure messages print only the fake's own
  synthetic values.
- [Concurrency] No findings. One write on an existing single-writer goroutine; no lock,
  no shared state, no goroutine to leak.
- [Threat model alignment] `docs/protocol-mobile.md` § `session_facts` states
  `permission_mode` is a CLAIM and never an authorization input. The e2e asserts byte
  equality only and reads nothing from the value, so it neither relies on nor endorses
  the misuse. Enforcing that the daemon never feeds the value back into
  `permissionModeAllowed` is the payload doc's standing constraint, pinned at the
  protocol tier and OUT OF SCOPE here.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-10
