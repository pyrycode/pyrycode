# #2067 — fakeclaude answers `set_permission_mode`, with an opt-in withheld ack

Fake-side only. Teaches `internal/e2e/internal/fakeclaude` to answer one more
`control_request` subtype, and adds one rider that withholds that answer. Ships no
daemon change: the spawn-time write, the ack correlation and the turn gate are all
#2064, which is blocked on this. A green `make check` here says nothing about that
gate, because nothing sends the request yet — landing first is the point.

## Files read

- `internal/e2e/internal/fakeclaude/main.go` → `runStreamJSON`, `controlRequestID`,
  `interruptControlRequest`, `inControlRequest`, `writeInterruptAck`,
  `writeInitializeAck`, `writeJSONLine`, `subtypeInterrupt` / `subtypeInitialize`,
  and `main`'s stream-json block. The whole production surface this ticket touches.
  `runStreamJSON`'s dispatch is an `else if` chain over one inbound line; the
  `initialize` arm sits *beside* the `honorInterrupt` arm rather than inside it, and
  its comment states why. `writeInitializeAck`'s doc is where the double-nested
  envelope and the marshalling/escaping property are already argued.
- `internal/e2e/internal/fakeclaude/initialize_control_test.go` →
  `initializeControlRequestLine`, `initializeAck`, `answerInitialize`,
  `modelEntryKeySet`, `captureModelKeySets`,
  `TestRunStreamJSON_InitializeControlAnswer`. The nearest analogue's whole test
  shape: a hand-mirrored request line, a decode target written as a *literal* rather
  than reused from the producer, a both-modes table whose two rows each red a
  different mis-gating, and a capture cross-check with an explicit non-vacuity gate.
- `internal/e2e/internal/fakeclaude/stream_detect_test.go` →
  `TestRunStreamJSON_InterruptAckRider`, `TestRunStreamJSON_NonUserLinesIgnored`,
  `TestRunStreamJSON_RateLimitRider`, `userTurnLine`, `interruptControlRequestLine`.
  Thirteen of the sixteen `runStreamJSON` call sites, and #1411's off/on rider-pair
  shape that AC 3's test mirrors.
- `internal/streamsup/parser.go` → the `control_response` dispatch arm and
  `emitModelList`. Records that an interrupt ack, a `set_permission_mode` ack and a
  NAK are *"each still consumed content-free, one record and no event"* — the reason
  answering unconditionally changes no existing suite's observable events.
- `internal/streamsup/envelope.go` → `permissionModeAllowed`,
  `marshalPermissionModeEnvelope`. The request wire shape (`mode` is a sibling of
  `subtype` under `request`, not top-level beside `request_id`), and the daemon's
  **outbound** allow-list — deliberately *not* applied inbound here, see § Design.
- `internal/e2e/realclaude/testdata/` → the ten committed captures carrying eleven
  `set_permission_mode` acks (`set_permission_mode_v2.1.220_revoke.json`,
  `bypass_reescalation_v2.1.239_reescalate.json`, four `bypass_approval_argv_*`,
  four `permission_mode_switch_*`). Read directly for the § Testing strategy
  cross-check; their shapes are uniform and are quoted below.
- `docs/knowledge/features/fakeclaude-binary-stream-json-mode.md` § "Interrupt mode",
  § "Approve rider", and the `initialize` answer's paragraph — the package overview's
  house rule for riders (*default-off, byte-identical when unset*) and the recorded
  deliberate departure from it for an unconditional control answer.
- `docs/knowledge/features/fakeclaude-binary-configuration-env.md` — the env table
  every rider switch is named in, and the naming shape (`PYRY_FAKE_CLAUDE_STREAM_*`).

## Context

`#2064` makes the daemon write the session's stored permission mode onto every
spawned child's stdin and refuse every user turn until claude acks it. The moment it
lands, **every session in the fake-daemon suite arms that gate** — an e2e session's
stored posture canonicalises to `default`, which `permissionModeAllowed` admits — so
a fake that never answers refuses every turn forever. This is therefore a
precondition for #2064 and, through it, #2065.

The same sequencing already happened one arm along: #1692 taught the fake to answer
`initialize` before #1839 started asking, and `runStreamJSON`'s comment on that arm
records why the answer is unconditional rather than behind a rider — once the daemon
starts sending the line every fake-daemon run sees it regardless of rider, so a gated
answer would leave the default path silently unanswered.

**No ADR.** This adds one arm to an existing dispatch chain on an existing pattern;
the design decision it embodies (answer unconditionally, gate only the *withholding*)
is #1692's, already recorded in the package overview.

## Design

One production file: `internal/e2e/internal/fakeclaude/main.go`.

### The subtype constant

`subtypeSetPermissionMode = "set_permission_mode"` joins `subtypeInterrupt` and
`subtypeInitialize` in the existing const block, so the dispatch keeps reading by name
rather than by bare literal. It is `marshalPermissionModeEnvelope`'s string.

### Carrying `mode` through the decode

`inControlRequest` gains one field, `Mode string` **inside** the nested `Request`
struct — the wire places it as a sibling of `subtype` under `request`, not top-level
beside `request_id`. That asymmetry is already the type's documented subject and the
doc gains a sentence for it.

`controlRequestID`'s doc fixes the discipline the extraction must not break: *"ONE
decode parameterised by subtype rather than a twin per subtype … a duplicated decode
is what drifts when the request envelope moves."* A second `json.Unmarshal` in a new
function would be exactly that drift. So the guard moves down one level:

- `decodeControlRequest(line []byte, subtype string) (inControlRequest, bool)` — the
  sole decode and the sole type/subtype guard. Returns the zero struct and `false`
  for a line that fails to decode or names another type or subtype.
- `controlRequestID` keeps its **signature unchanged** and becomes a thin wrapper
  returning `.RequestID`. Its three existing callers are untouched.
- `setPermissionModeRequest(line []byte) (requestID, mode string, ok bool)` — the new
  named per-subtype wrapper, in the shape `interruptControlRequest` already
  establishes for this file.

Per-line resilience needs no criterion of its own: it is inherited from the single
guard, unchanged.

### The writer

`writeSetPermissionModeAck(w io.Writer, requestID, mode string) error` — a
`map[string]any` through `writeJSONLine`, exactly like `writeInterruptAck` and
`writeInitializeAck`: keys marshal sorted, so the line is deterministic without
declaring a struct for a shape nothing else reads. It emits the ack envelope
`writeInitializeAck` documents — `subtype` and `request_id` nested **under**
`response`, the inverse of the request side — wrapping `{"mode": <echo>}` one level
deeper still at `response.response`.

Going through `writeJSONLine` is load-bearing rather than stylistic, and
`writeInitializeAck`'s doc already argues it for the request id: both echoed values
are inbound bytes reflected onto stdout, which the daemon's parser reads as
line-delimited JSON. `json.Marshal` escapes them, so a value carrying a newline and a
forged envelope lands as one escaped string inside one physical line; built with
`fmt.Sprintf` instead, that same value would split the output and fabricate a second
stream line the parser consumes as a real event. The **mode** is the new inbound
value inheriting that property, and it is the one with no existing proof.

### The echo is verbatim and unvalidated

Neither echoed value is inspected. A request naming a mode outside claude's
vocabulary, or naming none at all, is answered with whatever it carried.
`writeInitializeAck` already fixes this discipline for the id — *"A strange id is
echoed, never rejected: the fake does not police the daemon's correlation ids"* — and
this arm extends it to the mode.

The echo is **not** gated on `permissionModeAllowed`. That allow-list is the daemon's
own outbound defence against minting an escalating line; a fake that re-applied it
inbound could no longer reproduce what the daemon actually sent, which is precisely
what #2064's ack correlation has to observe. The captures settle this empirically
rather than by argument: `bypass_reescalation_v2.1.239_reescalate.json` holds real
claude answering a `bypassPermissions` request — a mode `permissionModeAllowed` does
**not** admit — with a plain success echoing it back. An inbound allow-list would
make that transcript unreproducible.

### Dispatch

A new `else if` arm in `runStreamJSON`, placed **beside** the `initialize` arm and
above `else if honorInterrupt`, answering unconditionally on the same terms and for
the same reason the `initialize` arm records.

The rider check sits **inside** the arm, not on its condition. AC 3's wording is
"read the request and withhold the ack": the line must still be *consumed by this
arm* and merely produce no bytes. Gating the arm's condition instead would let the
line fall through to whatever arm follows — inert today, a latent bug the moment
another arm is added — and is a mis-gating § Testing strategy reds.

### The rider

`withholdModeAck bool` is **appended** as `runStreamJSON`'s last parameter:

```go
func runStreamJSON(r io.Reader, w io.Writer, honorInterrupt, emitBogus bool,
	rateLimitStatus string, withholdModeAck bool)
```

Appending rather than grouping it with the two leading bools is deliberate on two
counts. It keeps this file's chronological rider order, which the doc comment's
paragraphs already follow (`honorInterrupt` #1136 → `initialize` #1692 →
`rateLimitStatus` #1411 → this). And the resulting `string, bool` tail makes a
mis-slotted mechanical edit a **compile error** rather than a silent rider swap,
which a third adjacent bool would not.

Cost: sixteen call sites gain one literal — 1 in `main`, 13 in
`stream_detect_test.go`, 2 in `initialize_control_test.go`. This is the one line of
the size table the ticket knowingly exceeds; § Size re-check records it.

`envStreamWithholdModeAck = "PYRY_FAKE_CLAUDE_STREAM_WITHHOLD_MODE_ACK"` joins the
env const block and is read at `main`'s single call site, non-empty ⟹ on, in the
shape `envStreamBogus` already uses. `runStreamJSON` stays a pure I/O seam with no
env reads inside. No harness change is needed for AC 3's "a caller can drive a child":
`internal/e2e/relay_v2_stream_unrecognized_test.go` already appends a rider env var
straight onto the spawn env, and that is the pattern #2064 will copy.

Unset ⟹ off ⟹ the emitted bytes are the ack.

## Concurrency model

None added. `runStreamJSON` runs entirely on `main`'s goroutine — a single reader, no
shared state — and both new functions are pure: `setPermissionModeRequest` decodes a
local, `writeSetPermissionModeAck` marshals a map built per call from its arguments
and writes it. No package-level state is introduced, so nothing needs the READ-ONLY
discipline `initializeModels` carries. No goroutine is spawned, so there is no
shutdown path to define. `-race` stays clean by construction.

## Error handling

`writeSetPermissionModeAck` returns the first marshal/write error, and the dispatch
arm `return`s on it — the daemon's read end is gone, treat like EOF, which is what
every other arm in the loop does.

Failure modes, and what each produces:

- **Line fails to decode / names another type or subtype** — `("", "", false)` from
  the single guard; the arm does not match and the line falls through, unchanged.
- **`mode` key absent from the request** — decodes to `""`, which is echoed as
  `"mode":""`. Present-and-empty, not omitted: the fake answers with what it got.
- **A hostile value in either echoed field** — escaped by `json.Marshal`; one
  physical line out. See § Design and § Security review.
- **Marshal error** — unreachable in practice (two strings in nested maps), handled
  anyway because `writeJSONLine` returns it.

## Testing strategy

One new test file, `set_permission_mode_control_test.go`, untagged like its two
neighbours so `make check` runs it in both the `test` and the `e2e` target.

Helpers, mirroring `initialize_control_test.go`'s discipline:

- `setPermissionModeRequestLine(requestID, mode string) string` — hand-mirrors the
  inbound request rather than importing it: `streamsup`'s envelope types are
  unexported and the fake stays zero-dependency. A second constructor emits a request
  with **no** `mode` key at all.
- `setPermissionModeAck` — the decode target, written as a **literal**, not reused
  from the producer: a target built from the producer follows a nesting bug green.
  The inner payload also decodes into a `map[string]any` view so key *presence* is
  observable.
- `answerSetPermissionMode(t, line string, honorInterrupt, withhold bool) []string` —
  feeds one line through `runStreamJSON` and returns the emitted lines, so every
  assertion below is made against the **marshalled bytes**, where a consumer meets
  them, rather than against the fake's own map.

Scenarios:

1. **Both stream modes answer with the double-nested envelope** (AC 1). A table over
   `honorInterrupt ∈ {false, true}` — both rows load-bearing, each the sole red for
   the opposite mis-gating, the reason `TestRunStreamJSON_InitializeControlAnswer`
   states for its own pair. Per row: exactly one line out; `type` is
   `control_response`; top-level `subtype` **empty** (the capture nests it);
   `response.subtype` is `success`; `response.request_id` is the request's own
   distinctive id; `response.response.mode` is the request's own mode. The last row
   is the sole red for an ack that put `mode` beside `subtype`/`request_id` instead
   of one level deeper.
2. **The echo is verbatim and unvalidated** (AC 1). Two rows: a mode outside claude's
   vocabulary is echoed unchanged; a request carrying **no** `mode` key is still
   answered, with the key *present* and empty. Read via the `map[string]any` view, so
   absent and empty are distinguishable — a typed field alone cannot tell them apart.
3. **Neither echoed value can fabricate a stream line** (AC 2). A table over *which
   field* carries the injection — the **mode** first, the value this arm adds and the
   one with no existing proof, then the request id — each fed a newline plus a forged
   `control_response` envelope. Per row: the output is **exactly one** physical line,
   and the decoded value equals the fed value verbatim. The line count is the
   assertion that reds a `fmt.Sprintf`-built writer; the verbatim check is what stops
   a writer that "fixed" it by stripping the newline.
4. **The withheld rider** (AC 3). An off/on pair on identical input, in #1411's
   rider-pair shape. Input is the request **followed by a user turn**: with the rider
   off, the ack and the turn's reply both land; with it on, the turn's reply still
   lands and **no** `control_response` does. Feeding the turn is what makes this more
   than a byte count — it reds a rider implemented by returning early or by dropping
   the line, and it proves the arm still *consumed* the request.
5. **The envelope matches every committed capture** (AC 1's provenance). Walk
   `../../realclaude/testdata/*.json`, select every `control_response` whose
   `response.response` carries a `mode` key, and canonicalise its outer and inner key
   sets with a shared helper. Assert the fake's own emitted ack canonicalises to a
   pair that occurs in that set. Then the echo itself: for each capture carrying
   top-level `control_request_id` and `requested_mode`, find the ack with the matching
   `request_id` and assert its `response.response.mode` equals `requested_mode` —
   correlating by id, not by "the only ack", because `bypass_reescalation` holds two.
   **Non-vacuity gates**, both aggregate over the matched set, in
   `captureModelKeySets`'s shape: at least eight acks matched, and at least two
   distinct modes among the correlated pairs. Without them a broken glob passes having
   compared nothing. Read-only against `testdata/`: nothing here opens a capture for
   writing, creates a file beside one, or removes one.

The existing suites are the regression check for "no existing bytes change": nothing
writes `set_permission_mode` today, so `TestRunStreamJSON_NonUserLinesIgnored` and
`TestRunStreamJSON_InterruptAckRider` must stay green **unmodified** apart from the
one-literal call-site edit.

## Open questions

1. **Does the ack need a NAK arm?** `parser.go` records that a `set_permission_mode`
   NAK exists on the wire (`error` string, no inner response). Resolved before
   writing: no. No AC asks for it, #2064 needs an *ack* to correlate, and a NAK arm
   would need its own rider to be reachable. Deferred; #2064 or #2065 can add it when
   something needs to drive a refusal. Recorded here so the omission is a decision.
2. **Where does the parameter go in `runStreamJSON`'s signature?** Resolved in
   § Design — appended, on the two counts recorded there.
3. **Does the package overview need the new rider documented?** Not by this ticket:
   `docs/knowledge/features/` is the documentation phase's, and it runs after
   verification. The PR body carries the lesson instead.

## Size re-check (against this written plan)

| Limit | Boundary | This plan |
|---|---|---|
| Production source files created/modified | ≤ 5 | **1** (`main.go`) |
| Total written work | ≤ 800 | **~750** (≈90 main.go + ≈16 call-site literals + ≈330 test + ≈310 spec) |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **16** ✗ |
| Acceptance criteria | ≤ 5 | **3** |
| Distinct error/reject branches in a state machine | ≤ 10 | **0** |

One line exceeded, knowingly, and not splittable: the depth gate returns `parent 2064
grandparent 1686`, so a third level is off the table. `needs-human:sizing` is already
on the ticket and the split that would otherwise have been made (ack / rider, wired
`addBlockedBy`) is written up in the refiner's sizing comment. Confirmed against the
tree rather than taken on trust: `grep -n "runStreamJSON("` returns exactly sixteen
call sites, 1 + 13 + 2 as the ticket states, every one of them gaining a single
`false` literal.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings, and the boundary is the ticket's subject. Data
  crosses untrusted→trusted exactly once, at `decodeControlRequest`'s single
  `json.Unmarshal` of one stdin line, and leaves through exactly one writer,
  `writeSetPermissionModeAck`. Both echoed values stay `string` end to end and are
  never parsed, compared, indexed, or spliced into a path, an argv or a format
  string. There is no second decode by construction — that is why the guard was
  pushed down into `decodeControlRequest` rather than duplicated. Downstream: the
  daemon's parser consumes this response *content-free*, one record and no event, per
  `parser.go`'s `control_response` arm, so nothing on the other side treats the
  echoed bytes as trusted either.
- **[Error messages, logs, telemetry]** No findings. The fake writes no log line and
  no error message on this path; a write failure returns the error and ends the loop,
  the same as every other arm. Nothing echoed is written to stderr, where the e2e
  harness would capture it.
- **[Network & I/O]** SHOULD FIX, and already designed in — this is the category AC 2
  is about. Both echoed values are attacker-shaped inbound bytes reflected onto a
  line-delimited stream the daemon parses. `fmt.Sprintf` instead of `json.Marshal`
  would let a value carrying `\n{"type":"control_response",…}` fabricate a second
  physical line that the daemon consumes as a genuine event — a *response-splitting*
  injection into the stream-json channel. The design routes both values through
  `writeJSONLine` for exactly this reason and § Testing strategy scenario 3 pins it
  by counting lines out with the injection in **each** field. Phase B must not
  "simplify" that writer to string concatenation, and the verifier should check the
  line-count assertion actually landed.
  No input size cap is specified, and deliberately: `runStreamJSON` reads with
  `bufio.ReadString` precisely so an arbitrarily long line is never truncated, the
  reader is a pipe from the parent daemon rather than a socket, and this is a test
  fake inside a hermetic suite. A cap here would change no threat and would break the
  existing long-prompt property.
- **[Subprocess / external command execution]** No findings, and this deserves an
  explicit statement rather than an N/A: `mode` is a value the *daemon* also puts on
  a child's command line in other code paths, so "echoed mode reaches an argv" is a
  plausible-looking worry. It does not here. Nothing in this ticket calls
  `exec.Command`, and the echoed value's entire journey is decode → map → marshal →
  stdout.
- **[File operations]** No findings. No path is constructed from any decoded value.
  The one filesystem touch this ticket adds is in tests: scenario 5 reads committed
  captures through a **hard-coded glob literal** with no decoded value reaching it,
  read-only, matching `initControlCaptureGlob`'s existing discipline in the
  neighbouring file.
- **[Concurrency]** No findings. No goroutine, no lock, no package-level mutable
  state — see § Concurrency model. Both new functions are pure and are called only
  from the single reader goroutine in production and from `t.Parallel()` subtests in
  the package unit test; the map `writeSetPermissionModeAck` builds is per call, so
  unlike `initializeModels` there is nothing shared to race on.
- **[Tokens, secrets, credentials]** Not applicable, stated as a design fact rather
  than skipped: the `request_id` is a daemon-minted correlation id, not a
  capability — it authorises nothing, is not compared against a secret, and real
  claude echoes it in the clear in every committed capture. There is nothing to
  generate, store, rotate or revoke.
- **[Cryptographic primitives]** Not applicable. No randomness, no comparison against
  a secret, no key material. Test ids are fixed literals, not generated.
- **[Threat model alignment]** The relevant threat is the one this ticket must *not*
  interfere with: the daemon's outbound `permissionModeAllowed` allow-list, which
  exists so the daemon can never mint an escalating permission line. This design
  deliberately does not apply that list inbound, and that is the safe direction —
  the fake is a *receiver* here, it grants nothing, and a fake that filtered inbound
  could no longer reproduce what the daemon actually sent, which is the transcript
  #2064's correlation must observe. The empirical check is
  `bypass_reescalation_v2.1.239_reescalate.json`: real claude answers a
  `bypassPermissions` request — outside `permissionModeAllowed` — with a plain
  success. Enforcement of what the daemon may *send* stays where it is and is
  untouched by this ticket. Out of scope, named: the spawn-time write, the ack
  correlation and the turn gate are #2064.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-03

## Revisions

### 2026-09-03 — the size table's total-work line was exceeded too, measured

**What changed:** nothing in the design. This corrects a number § Size re-check states.

The plan estimated **~750 lines** of total written work and passed that line of the
table. The actual is **1062** — 684 implementation insertions (512 test, 180 `main.go`,
30 across the two files holding the fifteen call sites) plus a 378-line spec. So a
**second** line of the size table is exceeded, not one, and it was not visible at plan
time.

**Why the estimate missed.** It counted code volume, not comment volume, in a package
whose convention is heavy explanatory doc comments — `initialize_control_test.go`, the
analogue the estimate was derived from, runs close to one comment line per code line,
and every new function here carries the same. The estimate's own basis makes the error
legible: #1692's measured 931 lines was quoted, then netted *down* to ~750 on the
argument that this arm's payload is smaller. The payload is indeed smaller. The prose
around it is not, because it is prose about a permission-posture path, and the rider
#1692 lacked came with its own paragraphs in three places.

**Not acted on, deliberately.** The work is complete, green and mutation-checked, and
the shape it exceeded is documentation of design rationale, not scope. Trimming
verified comments to reach a line count would be worse work, and the split that would
have separated the ack from the rider is the one the depth gate already forbade —
`needs-human:sizing` is on the ticket for exactly this family of overage.

**The reusable correction**, for whoever recalibrates the table: in this package,
estimate from an analogue's *measured total* rather than from its production payload.
The "smaller payload ⟹ smaller ticket" adjustment is the step that failed here, and it
failed by ~40%.
