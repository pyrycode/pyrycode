# #2282 — decode the inbound `can_use_tool` control request, marshal its `control_response`

## Files read

- `internal/streamsup/envelope.go` → `controlRequest`, `controlRequestInner`, `marshalInterruptEnvelope`,
  `marshalPermissionModeEnvelope`, `marshalInitializeEnvelope`, `WriteInterrupt`, `WriteInitialize`,
  `ErrNoLiveChild` — the four-sibling shape every new writer here mirrors: pure encoder, then a
  `Write*` half holding the nil-writer refusal, one `Write`, never close `w`.
- `internal/streamsup/parser.go` → `consumeLine` (the top-level type switch and its `control_response`
  arm), `streamLine`, `controlResponseLine`, `controlAckLine`, `noteControlAck`, `emitUnrecognized`,
  `Parser`, `NewParser`, `PostureGate` — the arm's placement, the two-target decode idiom (one target
  per question, decoded off the same top-level bytes), and the only existing installed-seam precedent.
- `internal/streamsup/envelope_test.go` → `TestMarshalInterruptEnvelope`, `TestWriteInterrupt_NilRefusal`,
  `TestWriteInterrupt_WriteError`, `decodedControlRequest`, `errWriter` — the byte-exact-want test shape
  the two response envelopes copy, newline count included.
- `docs/knowledge/features/set-permission-mode-inband-probe.md` § "The `control_response` received,
  verbatim" — the nesting authority for the answer: `subtype` and `request_id` under `response`, the
  payload nested one level deeper again.
- `docs/knowledge/features/permission-protocol-spike.md` — #383 at claude 2.1.143 saw no permission
  event under `--permission-prompt-tool stdio`. It measured spawning, not this codec, and sits 116 patch
  versions behind the ticket's shape authority. Read and set aside; it does not block this slice.
- `internal/streamsup/runner.go` → `Config.OnSessionRotate` — the package's callback-seam precedent
  (nil-checked at the fire site, never at install).

## Context

`control_request` is outbound only in this package today. An inbound one falls through `consumeLine`'s
switch to `emitUnrecognized` with `turnevent.UnrecognizedLineType`, which is the one frame whose whole
value is "claude started emitting something new" — spending it on a routine permission ask is the same
defect the `control_response`, `tool_progress` and `conversation_reset` arms each fixed.

This ticket ships the codec and the parser arm only. Nothing spawns with `--permission-prompt-tool
stdio` and nothing installs an answerer, so production behaviour and the wire to clients are unchanged.
#2284 spawns under the flag and installs the answerer; #2286 interprets `permission_suggestions`.

No ADR is warranted. The design adds one arm and one writer pair to an established family and decides
nothing the package has not already decided four times.

### Size: over the ceiling, kept whole by the floor rule

Measured against the one-ticket boundary, this ticket trips the total-written-work line: the sketch is
roughly 950–1100 lines against a ceiling of 800. Every other line holds — 2 production files, 1 new
exported type, 0 consumer call sites, 4 acceptance criteria, 3 reject branches.

The split it invites is the file seam: A, the inbound decode plus the parser arm (`parser.go`); B, the
response codec (`envelope.go`). Both children fail the floor rule. Each one's only consumer is #2284, a
single sibling in the same family, so each is a mechanism with no caller of its own outside the family.
The floor wins over the ceiling, so the ticket is built whole and the overage is stated here rather than
hidden. The nearest analogue #2234 (`ef91aeae` + `30ead897`) totalled 834 lines of the same kind of work
against the same budget; this slice is that plus a second, smaller writer pair.

## Design

Two production files, each taking the half that belongs to it.

### `parser.go` — the read half

`CanUseToolRequest` is the exported decoded value, and the only new exported type. Its JSON tags describe
the fields of the request's INNER object; `RequestID` is tagged `json:"-"` and is filled from the
envelope by the decoder, so the 19 fields are declared once rather than twice across a wrapper and a
payload struct.

Field typing follows one rule, stated because the provenance is weaker than this file's other control
lines carry: the shapes come from the Agent SDK type definitions named in the ticket, not from a line a
live claude has been sent, and no fixture or SDK typedef exists in this repo to check them against. So a
field the ticket's own phrasing pins to a scalar is declared as that scalar; every field whose JSON shape
is not pinned is declared `json.RawMessage`, which accepts any JSON and therefore cannot turn a wrong
guess into a failed decode and a lost permission ask.

- `string`: `ToolName`, `ToolUseID`, `AgentID`, `BlockedPath`, `DecisionReasonType`, `Title`,
  `DisplayName`, `Description`.
- `bool`: `ClassifierApprovable`, `SuppressAlwaysAllowRule`, `DefaultToNo`, `RequiresUserInteraction`.
- `json.RawMessage`: `Input` and `PermissionSuggestions` (AC 1 requires both byte-verbatim, and nothing
  here interprets either), plus `MatchedAskRule` and `DecisionReason`, whose shapes the ticket does not
  pin.

`DecisionReasonType` is carried verbatim and NOT validated against the ticket's eleven spellings. This
codec's job is to report what claude said; deciding what an unrecognised reason type means belongs to
whoever renders or answers the ask, and a vocabulary gate here would silently drop an ask on a spelling a
later claude adds. Contrast `permissionModeAllowed`, which gates a value the daemon SENDS.

Two decode targets, each answering one question off the same top-level line bytes — the idiom
`controlResponseLine` and `controlAckLine` already establish, and the reason is theirs verbatim: a field
added to one target must not change the other's decode outcome.

- `controlRequestSubtypeLine` — the request's `subtype` alone, for the arm's dispatch. Needed because
  `control_request` nests its subtype under `request`, so `streamLine.Subtype` decodes empty exactly as
  it does for `control_response`.
- `canUseToolLine` — `request_id` plus `Request CanUseToolRequest`, decoded only once the subtype matched.

`Parser` gains one field, `canUseTool func(CanUseToolRequest)`, and one installer,
`(*Parser).SetCanUseToolHandler`. It is a seam, not state: no line reads it, no reset touches it, and the
single-writer invariant Parser's doc states is unaffected because the field is written before the parser
is handed to a runner and never after. That happens-before is the installer's documented contract and is
what keeps the seam free of a mutex; `Config.OnSessionRotate` is the same shape one layer down.

The new `case "control_request"` arm, in three rungs:

1. Subtype decode fails, or subtype is anything but `can_use_tool` → `emitUnrecognized(
   turnevent.UnrecognizedLineType, sl.Type, line)`, byte for byte what happens today (AC 2).
2. Subtype is `can_use_tool` but the full decode fails → the same unrecognized emit. A `can_use_tool` we
   cannot read is news, not routine.
3. Decoded → call the handler if one is installed, and return. With none installed the line is CONSUMED
   AND DROPPED silently, which is safe only because no spawn passes the flag; #2284 installs the answerer
   in the same slice that starts producing these lines.

This arm is a MATCHER, so `emitUnrecognized` stays reachable for it — deliberately, the weaker of the two
guarantees, for `emitConversationReset`'s reason: an ask the daemon cannot read must still ring the bell.
It is its own case rather than an `ignoredLineTypes` member for #1404's reason, which the three arms
above it each state: that list is documented as top-level types only and as measured.

### `envelope.go` — the write half

Two encoders and two writers, mirroring the four siblings already in the file.

- `marshalCanUseToolAllow(requestID string, updatedInput, updatedPermissions json.RawMessage) ([]byte, error)`
- `marshalCanUseToolDeny(requestID, message string, interrupt bool) ([]byte, error)`
- `WriteCanUseToolAllow(w io.Writer, requestID string, updatedInput, updatedPermissions json.RawMessage) error`
- `WriteCanUseToolDeny(w io.Writer, requestID, message string, interrupt bool) error`

Two functions rather than one taking a decision value: it keeps the allow and deny field sets from being
one struct where half the fields are wrong for either behaviour, it adds no exported type, and it matches
`WriteInterrupt`/`WritePermissionMode`/`WriteInitialize` — each a `Write*` with scalar parameters.

The envelope is a `controlResponse` type new to this file, shaped from the verbatim capture: `type`
top-level, then `response` carrying `subtype` (the constant `success`) and `request_id`, then a nested
`response` carrying the `PermissionResult`. A deny is a SUCCESSFUL answer whose content is a refusal, so
its subtype is `success` too; `error` is reserved for a control request claude could not process.

`omitempty` on `updatedInput`, `updatedPermissions` and `interrupt` is load-bearing, not cosmetic — AC 3
requires each absent when unset — and the byte-exact wants are what hold it. `behavior` and `message` are
never omitted.

### Concurrency model

No goroutine is added and none is needed. The handler is invoked synchronously on the same os/exec
forwarder goroutine that runs every emit, in stream order, which is the contract `NewParser` already
states for `sink`. A handler that blocks stalls the parser exactly as a blocking sink does; the installer
documents that it must not block, and must not call back into the parser.

The answer travels on the child's stdin, a different fd from the stdout this parser reads, so answering
inside the handler cannot deadlock against the parser's own reader.

### Error handling

| Failure | Behaviour |
|---|---|
| Subtype undecodable, or not `can_use_tool` | `turnevent.UnrecognizedLineType`, unchanged from today |
| `can_use_tool` payload undecodable | `turnevent.UnrecognizedLineType` — an unreadable ask is news |
| No handler installed | Line consumed, dropped silently, nothing logged |
| `w == nil` on either writer | `ErrNoLiveChild`, zero bytes written, checked first (AC 4) |
| Marshal failure | Wrapped, never mis-reported as `ErrNoLiveChild` |
| Write failure (EPIPE mid-teardown) | Wrapped, never mis-reported as `ErrNoLiveChild` |

No decode path logs, on any rung. `encoding/json` quotes the offending input into its error text, so
`"err", err` would route claude's own bytes into the daemon log through a channel no per-attribute check
can see — `noteControlAck`'s rule, and it is not weaker here. Neither refusal wraps the message or the
request id.

### Testing strategy

New file `internal/streamsup/can_use_tool_test.go` for the codec, plus parser-arm cases beside it.

- Full-field decode: every field of the ticket's list populated, each asserted, `RequestID` taken from the
  envelope rather than the inner object.
- Unknown inner fields ignored: a request carrying keys this type does not declare decodes and populates.
- `Input` and `PermissionSuggestions` byte-verbatim: key order and spacing preserved through the decode.
- An object-valued `DecisionReason` and `MatchedAskRule` still decode — the property the `json.RawMessage`
  typing rule buys, and the one a wrong scalar guess would lose.
- Byte-exact allow, with and without `updatedInput`/`updatedPermissions`; byte-exact deny, with and
  without `interrupt`. Each asserts exactly one raw newline and that it terminates the line.
- Nil-writer refusal for both writers; wrapped, distinguishable write error for both.
- Parser arm: handler receives the decoded request; no handler installed drops silently and emits nothing;
  `control_request` with another subtype emits `UnrecognizedLineType`; an undecodable `can_use_tool` does
  the same.
- Regression: the existing fixture replays in this package keep their event sequences. Covered by running
  the package suite, which is where those replays live.

The test comment for the byte-exact wants says plainly that their provenance is the SDK type definitions
rather than a line a live claude has been sent, and that #2284's live gate is what confirms them. No
"MEASURED" phrasing is borrowed from the siblings, whose wants were measured live.

## Open questions

1. Whether `decision_reason` and `matched_ask_rule` are scalars or objects on the wire. Resolved by
   construction rather than by answering: both are `json.RawMessage`, which decodes either.
2. Whether a deny's envelope subtype is `success` or `error`. Taken as `success` from the capture's
   reading that `error` marks a control request claude could not process. #2284's live gate settles it;
   if it settles the other way, the change is one constant and one pinned want.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The boundary is explicit and single: `canUseToolLine`'s decode in the
  `control_request` arm is the one place subprocess stdout becomes a typed value, and `CanUseToolRequest`
  is the type that signals "every field here is claude-authored". Nothing else in the package constructs
  one. SHOULD FIX, carried into the doc comment rather than the code: the type's doc must say that a
  handler holds untrusted data, because the field names (`title`, `description`, `display_name`) read like
  daemon-authored UI strings and a consumer that renders them unescaped is the foreseeable misuse. #2286
  is the ticket that renders them.
- [Trust boundaries, second direction] The deny `message` is the one caller-supplied string on the write
  path and the ticket names the hostile case: an answerer may draw it from claude's own bytes. It is
  marshalled as a JSON string value, so `json.Marshal` escapes every metacharacter and the appended `'\n'`
  stays the only raw newline. A hostile message therefore cannot open a second stream-json line on the
  child's stdin and so cannot forge a `result`, an interrupt, or a second permission answer. This is the
  ticket's stated requirement and it is met by construction, never by string concatenation.
- [Error messages, logs, telemetry] No finding, by omission rather than by filtering. No rung of the decode
  logs, so no decoded field and no decode error reaches the daemon log; neither writer wraps the message or
  the request id into its error. `noteControlAck`'s rule about `encoding/json` quoting input bytes into its
  error text is what makes the silent decode failure the correct shape rather than a missed diagnostic.
- [Network & I/O — input size limits] No finding. `defaultMaxParseBuf` caps a whole stdout line at 4 MiB
  before any decoder sees it, so `Input` and `PermissionSuggestions`, which AC 1 requires be retained
  byte-verbatim and therefore uncapped by this codec, are bounded transitively by that. No per-field cap of
  the `maxTaskFieldID` family is applied and none is owed: those constants bound values that ENTER THE
  EVENT STREAM, and nothing here emits a `turnevent`. OUT OF SCOPE, named: the per-field caps belong to
  #2286, the slice that first puts these strings on the wire to a client.
- [Concurrency] SHOULD FIX, addressed in Phase B by contract rather than by a lock. The handler field is
  written by `SetCanUseToolHandler` and read on the forwarder goroutine, which is a data race if the
  installer is called after the parser is running. The installer's doc states the happens-before — install
  before the parser is handed to a runner — and the verifier should check it is stated. A mutex was
  rejected: it would buy protection against a call pattern no caller has, and `postureGate` carries its own
  mutex precisely because it is the one piece of parser state genuinely touched from another goroutine,
  which this is not. `-race` on the package is what would catch a violation.
- [Concurrency, second] No finding on the write path. Both envelopes stay far under `PIPE_BUF`'s POSIX
  floor of 512 only when the deny message is short, and unlike `permissionModeAllowed`'s closed set there
  is no membership bounding this one's length. So the atomic-`write(2)` property `WritePermissionMode`
  enjoys does NOT carry over, and the plan does not claim it: a long deny message can tear against a
  concurrent `WriteTurn` on the same fd. That is a pre-existing property of every unbounded write to this
  fd — `WriteTurn` itself has it for any prompt over 512 bytes — so it is not introduced here. OUT OF
  SCOPE: serialising writes to the child's stdin is #2284's problem, where an answerer first shares the fd
  with the turn writer.
- [Subprocess execution] No finding. Nothing here builds argv, sets an environment, or signals a child.
  The flag that makes claude emit these lines is added by #2284's spawn change, not by this slice.
- [Tokens, secrets, credentials] Not applicable by design: no value here is a credential, nothing is
  generated, stored, rotated or compared. The `request_id` is claude's own correlation id, echoed back
  verbatim and never treated as an authorisation — it selects which ask an answer belongs to, and the
  answerer that decides the ask is #2284's.
- [File operations] Not applicable. `blocked_path` is claude's report of a path IT refused, decoded as an
  opaque string and never opened, stat-ed, joined, or canonicalised by anything in this slice. A consumer
  that later resolves it inherits the path-traversal question; #2286 is where a client first sees it.
- [Cryptographic primitives] Not applicable. No randomness, no hashing, no comparison against a secret.
- [Threat model alignment] The relevant threat is the one the ticket names: an untrusted string reaching
  the child's stdin as a second protocol line. Addressed above under trust boundaries, second direction.
  The threat this slice deliberately does NOT address is answering an ask at all — no answerer exists, so
  no policy decision about which tools may run is made or implied here. #2284 owns it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-09
