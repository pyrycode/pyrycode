# #1692 — fakeclaude answers an `initialize` control request with a canned model list

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_search` / `codegraph_node`.

| Where | Symbol | What to extract |
|---|---|---|
| `internal/e2e/internal/fakeclaude/main.go` | `runStreamJSON` | The read→dispatch loop. The `else if honorInterrupt` arm is what the new arm sits **beside**, never inside. Note the 5-parameter signature — it does **not** grow here. |
| `internal/e2e/internal/fakeclaude/main.go` | `interruptControlRequest`, `inControlRequest` | The subtype predicate to generalise, and the decode struct that already matches the `initialize` request shape with no change (`request_id` top-level, `request.subtype` nested). |
| `internal/e2e/internal/fakeclaude/main.go` | `writeInterruptAck` | The ack envelope precedent — `subtype` and `request_id` nested **under** `response` — plus the `map[string]any` + `writeJSONLine` idiom and the honest-provenance doc-comment style. |
| `internal/e2e/internal/fakeclaude/main.go` | `writeRateLimitEvent` | The other "transcribed verbatim from a committed capture" writer. Copy its provenance-comment discipline: name the capture, say what is transcribed and what is invented. |
| `internal/e2e/internal/fakeclaude/stream_detect_test.go` | `TestRunStreamJSON_InterruptAckRider`, `interruptControlRequestLine` | The untagged unit-test shape to copy: hand-mirror the inbound line, decode the emitted line into a **literal** target, assert the nesting positively and the top-level `subtype` negatively. |
| `internal/e2e/internal/fakeclaude/stream_detect_test.go` | `TestRunStreamJSON_NonUserLinesIgnored` | Must stay green **unmodified** — it feeds a `subtype:"interrupt"` control line in default mode and asserts zero output bytes. It is half of AC4's regression proof. |
| `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` | — | The capture. Entries live at `control_responses[].response.response.models`. Read the two entries this spec cans **out of this file**, verbatim. |
| `internal/e2e/realclaude/initialize_control_names_test.go` | `initControlFixtureName`, `initControlArmFixtureName`, `initControlArms` | Why the glob will match **more than one** file after #1713/#1715 land, and why one of those future files (`control_no_request`) legitimately carries no `control_responses` at all. Drives the aggregate-not-per-file non-vacuity rule below. |
| `internal/streamsup/parser.go` | `(*Parser).consumeLine`, its `case "control_response"` arm | Why emitting this answer cannot change any existing e2e's client-visible frames: the arm reads **nothing below the top-level `type`**, so an ack carrying an inner payload and one carrying none are consumed identically, content-free, into a `Debug` log. |
| `internal/protocol/interactive.go` | `ModelOption`, `ModelOption.MarshalJSON` | #1704's decision that absent and empty both publish as `[]` on the **wire**, and that the daemon-internal distinction is left for #1690. This is why the fake must omit the keys rather than emit empties. |
| `docs/knowledge/features/fakeclaude-binary.md` | § Stream-json mode, § Interrupt mode (`honorInterrupt`) | House rules for stream riders: default-off, byte-identical when unset, the call-site-not-inside-the-seam discipline, and why a new env knob has to justify itself. **Read-only — the documentation phase owns this file.** |

## Context

The daemon is gaining the ability to send its stream child a `control_request` with
subtype `initialize` (#1689) — the request claude answers with the session's model
list (identifiers, display names, effort levels) and slash-command list. Two features
ride the answer: the model-list publishing path (#1693) and the slash-command list
(#1683). Neither can be proven hermetically until the fake-daemon tier can answer the
request without credentials, tokens or network.

`fakeclaude`'s stream mode already decodes `control_request` lines, but only inside the
`else if honorInterrupt` arm of `runStreamJSON`. In the default mode every other e2e
suite runs, the line is read and dropped unlooked-at. Nothing has ever sent an
`initialize` one, which is what makes an **unconditional** answer safe: it changes no
existing suite's bytes today, and once #1689 starts sending, every fake-daemon run sees
the request regardless of rider — so gating the answer behind a knob would leave the
default path silently unanswered.

**The measurement authority is the committed capture**, not the original filing's
hand-measured note: `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json`
(claude 2.1.239, `subtype: "success"`, request id echoed, 6 models). The response is
**double-nested** — the initialize payload is a `response` object *inside* the `response`
object that carries `subtype` and `request_id`.

Field presence varies per entry, and that variation is the point of this ticket. The
capture's `models_entry_fields` is a nine-name **union** and cannot see per-entry shape;
walking the entries shows exactly three distinct key sets:

| entries | key set |
|---|---|
| `default`, `sonnet`, `claude-fable-5[1m]` | the eight: `value`, `resolvedModel`, `displayName`, `description`, `supportsEffort`, `supportedEffortLevels`, `supportsAdaptiveThinking`, `supportsAutoMode` |
| `opus` | those eight **plus** `supportsFastMode` |
| `haiku`, `claude-haiku-4-5` | `value`, `resolvedModel`, `displayName`, `description` — **and nothing else** |

**Absent is not present-and-empty.** A fake emitting `"supportedEffortLevels": []`
instead of omitting the key destroys the distinction before #1690 gets to decide how the
daemon represents it internally.

No ADR is warranted; this is a harness slice inside an established rider pattern.

## Design

One production file changes: `internal/e2e/internal/fakeclaude/main.go`. One new untagged
test file is added. Nothing else — no env knob, no harness change, no signature change.

### 1. The predicate: one decode, parameterised by subtype

`interruptControlRequest` already decodes exactly the envelope an `initialize` request
arrives in; only the subtype it matches differs. Split the shared half out, mirroring the
`argvSessionID` → `argvIDFlag` extraction #1631 made in this same file:

```go
// controlRequestID reports whether line is a control_request carrying subtype, and
// returns its correlation id for an ack to echo.
func controlRequestID(line []byte, subtype string) (string, bool)

// interruptControlRequest becomes a one-line wrapper: controlRequestID(line, subtypeInterrupt).
func interruptControlRequest(line []byte) (string, bool)
```

- One copy of the decode, matching this file's stated preference for a single shared
  parse over a duplicated twin that drifts.
- `inControlRequest` is **unchanged**. Its existing doc comment about `request_id` being
  top-level on the request side (and inverted on the response side) stays true.
- Declare the two subtypes as constants (`subtypeInterrupt = "interrupt"`,
  `subtypeInitialize = "initialize"`) so the dispatch reads by name.
- `interruptControlRequest`'s existing behaviour is byte-identical by construction; the
  unmodified `TestRunStreamJSON_Interrupt*` tests are the regression check.

### 2. The dispatch arm — beside `honorInterrupt`, not inside it

```go
if text, ok := userTurnText(b); ok {
    …unchanged…
} else if reqID, ok := controlRequestID(b, subtypeInitialize); ok {
    if werr := writeInitializeAck(w, reqID); werr != nil { return }
} else if honorInterrupt {
    …unchanged…
}
```

Why this placement and no other:

- **Mode-independent by construction.** The arm is evaluated before `honorInterrupt` is
  consulted, so the answer is identical in default and interrupt mode. AC4's "not gated
  behind interrupt mode" becomes a structural property rather than a branch to audit.
- **Every existing arm is untouched.** An interrupt line fails `subtypeInitialize` and
  falls through to the unchanged `honorInterrupt` arm; an unknown subtype, a malformed
  line and a blank line match neither predicate and are dropped exactly as today. A user
  turn never reaches either arm.
- **`runStreamJSON`'s signature does not grow.** The answer is unconditional, so there is
  no rider value to thread, no `main()` call-site edit and no e2e harness change. This is
  the one deliberate departure from the rider pattern (#1136/#1411 both added a param),
  and § Open questions records why.
- `runStreamJSONApprove` — the approve rider's separate read loop — is **not** touched.
  See § Open questions.

### 3. The writer

```go
// writeInitializeAck writes the control_response claude answers an initialize
// control_request with: the ack envelope (subtype + request_id under `response`)
// wrapping the initialize payload one level deeper, as `response.response`.
func writeInitializeAck(w io.Writer, requestID string) error
```

Emits exactly one line, via `writeJSONLine`:

```
{"type":"control_response","response":{"subtype":"success","request_id":"<echoed>","response":{"models":[…]}}}
```

- **`map[string]any` all the way down**, like `writeInterruptAck` / `writeRateLimitEvent`
  / `writeBogusLines`. Not a struct with `omitempty`: absence is the load-bearing property
  here, and a map makes it literal — the key is simply not in the map. `omitempty` would
  conflate `false` with absent for `supportsAutoMode`, which is precisely the distinction
  the entry exists to carry. Map keys marshal sorted, so the line is deterministic.
- **Going through `writeJSONLine` is load-bearing for the echo, not a style preference.**
  `requestID` is inbound bytes — whatever the daemon wrote to this child's stdin — and it
  is reflected straight back onto stdout, which the daemon's stream parser reads as
  line-delimited JSON. `json.Marshal` escapes it, so a `request_id` containing a newline
  and a forged envelope lands as one escaped string inside one physical line. Build this
  line with `fmt.Sprintf` instead and that same value splits the output and fabricates an
  extra stream line the parser consumes as a real event. `writeAssistantEcho` already
  depends on exactly this property for the echoed prompt; do not introduce a second,
  weaker path for it. Never `os.Exit` or reject on a strange id — echo it, escaped.
- The canned entries live in one package-level `var` (`initializeModels`), so #1683 can
  add `commands` alongside `models` in the inner object without touching the entries.
- `models` **only**. The real inner payload also carries `commands` (51 in the capture),
  `agents`, `output_style`, `account`, `pid`, `session_state` and more, and the outer
  object carries `pending_permission_requests` / `pending_user_dialog_requests`. None of
  it is needed; inventing it is what the provenance comment on `writeInterruptAck` exists
  to warn against.
- The doc comment must carry the same honest-provenance shape `writeInterruptAck` and
  `writeRateLimitEvent` use: name the capture file, say the two entries are transcribed
  verbatim from it, and say plainly that the rest of the payload is omitted rather than
  invented.

### 4. The canned entries — two arms, both key sets attested by the capture

Exactly two entries: one full-shape, one minimal. The table is the **shape**; the capture
is the **authority for the values** — transcribe them out of
`initialize_control_v2.1.239.json` rather than retyping from here (the `description`
strings carry a U+00B7 MIDDLE DOT that a retype mangles silently).

| key | full arm | minimal arm |
|---|---|---|
| `value` | `sonnet` | `haiku` |
| `resolvedModel` | `claude-sonnet-5` | `claude-haiku-4-5-20251001` |
| `displayName` | `Sonnet` | `Haiku` |
| `description` | `Sonnet 5 · Efficient for routine tasks` | `Haiku 4.5 · Fastest for quick answers` |
| `supportsEffort` | `true` | **key absent** |
| `supportedEffortLevels` | `["low","medium","high","xhigh","max"]` | **key absent** |
| `supportsAdaptiveThinking` | `true` | **key absent** |
| `supportsAutoMode` | `true` | **key absent** |

Both key sets occur verbatim in the capture (the eight-key set and the four-key set),
which is what makes AC3's cross-check pass on honest code. `opus`'s nine-key set
(`supportsFastMode`) is deliberately **not** canned — two arms satisfy every AC, and the
cross-check is written generically, so #1693 can add a third entry later without touching
the test.

### 5. Data flow

```
daemon (#1689) --stdin--> fakeclaude runStreamJSON
     {"type":"control_request","request_id":"…","request":{"subtype":"initialize"}}

fakeclaude --stdout--> daemon streamsup.Parser.consumeLine  case "control_response"
     {"type":"control_response","response":{"subtype":"success","request_id":"…",
                                            "response":{"models":[sonnet, haiku]}}}
```

Today the parser's `control_response` arm reads nothing below the top-level `type` and
consumes the line content-free into a `Debug` log — so this answer reaches no client and
changes no frame count in any existing e2e. #1690 is what teaches the daemon to read the
inner payload.

## Concurrency model

No change. `runStreamJSON` remains a single-goroutine read→emit loop on `main()`'s
goroutine; the new arm adds no goroutine, no shared state, no lock, no `atomic.Bool`
signal. `-race` cleanliness stays structural. Shutdown is unchanged: the loop returns on
`ReadString` error (daemon closed stdin) or the first write error.

## Error handling

| Failure | Behaviour |
|---|---|
| Line fails to decode | `controlRequestID` returns `("", false)`; line dropped, no output — same per-line resilience `userTurnText` and `interruptControlRequest` already hold |
| `control_request` with a subtype other than `initialize` | Falls through to the unchanged `honorInterrupt` arm (or is dropped in default mode) |
| `initialize` request with an empty `request_id` | Answered, echoing the empty id. The fake does not police the daemon's correlation ids — inventing one would be worse than echoing what arrived |
| Write error from `writeInitializeAck` | `return` from the loop, exactly like every other arm's write error — the daemon's read end is gone; treated as teardown, never `os.Exit` mid-turn |
| Marshal error | Propagated by `writeJSONLine`; unreachable for these literals, but not swallowed |

## Testing strategy

One new **untagged** test file: `internal/e2e/internal/fakeclaude/initialize_control_test.go`.
Untagged so `make check` runs it in both the `test` and the `e2e` target, exactly like
`stream_detect_test.go`. Reading the capture needs no build tag — `internal/e2e/realclaude`
is behind `e2e_realclaude`, but the testdata file is just a file. Read it by relative path
(`go test` runs in the package source directory); **do not import the package**.

Two helpers, then one test function with four subtests.

**Helpers**

- `initializeControlRequestLine(requestID string) string` — hand-mirrors the inbound
  request, same discipline and shape as `interruptControlRequestLine`.
- `modelEntryKeySet(entry map[string]any) string` — sorted key names joined by `,`. Used
  on **both** sides of the comparison, so a bug in it cannot make one side vacuous alone.
- `captureModelKeySets(t *testing.T) map[string]string` — globs
  `../../realclaude/testdata/initialize_control_v*.json`, decodes each match into
  `map[string]any`, walks `control_responses[].response.response.models[]`, and returns
  canonical key set → provenance string (file + that entry's `value`) for failure
  messages. Every step is defensive about a missing or wrong-typed key: skip and continue,
  never panic on a type assertion.

**Non-vacuity gates, all three mandatory** (AC3 names the first two explicitly):

1. `filepath.Glob` returning zero paths → `t.Fatalf`. A moved or renamed capture must
   redden here, not pass silently.
2. Zero model entries collected across **all** matched files → `t.Fatalf`.
3. Fewer than **two distinct** key sets collected → `t.Fatalf`. This is what pins
   `modelEntryKeySet` as discriminating: a canonicaliser that collapsed every entry to one
   string would otherwise make the membership check pass for anything.

**Aggregate, never per-file.** Gate 2 is deliberately about the union across every matched
file, not about each file. #1713/#1715 commit sibling captures named
`initialize_control_v<slug>_<arm>.json`, which this glob matches — and one of those arms
is `control_no_request`, whose whole content is that no control request was sent, so it
carries no `control_responses` and contributes no entries **by design**. A per-file gate
would redden on honest code the day that arm lands.

**Subtests**

- *answers in both stream modes, with the double-nested ack envelope* (AC1). Table-driven
  over `honorInterrupt ∈ {false, true}`, other riders off. For each: feed one
  `initialize` line, assert exactly one emitted line; `type == "control_response"`;
  `response.subtype == "success"`; `response.request_id` equals a distinctive id the fake
  could not have minted itself; top-level `subtype` empty (asserted negatively, as
  `TestRunStreamJSON_InterruptAckRider` does — a top-level `subtype` is what would send
  `streamsup` down an `emitSystemSubtype`-shaped path that does not exist for this type);
  and `response.response.models` present and non-empty. Decode into a **literal** target,
  never by reusing the writer's own map — a target built from the producer follows a
  nesting bug green. The `honorInterrupt = true` row is the sole red for an arm placed
  inside the `honorInterrupt` branch.
- *covers both arms* (AC2). Over the decoded canned entries: at least one carries
  `supportedEffortLevels` equal to the five levels **and** `supportsAutoMode == true`; at
  least one has **neither key present**. Presence is tested by the two-value map lookup on
  the decoded `map[string]any` — never by a zero-value comparison, which cannot tell
  absent from `false` and is exactly the confusion this AC exists to prevent.
- *every canned key set occurs in the committed capture* (AC3). Guard `len(canned) >= 2`
  first (else the loop passes vacuously), then assert each canned entry's key set is a key
  of `captureModelKeySets`. Failure message names the offending set and the entry's
  `value`, and lists the sets the capture does carry.
- *a control line that is not an initialize request is unchanged* (AC4). In default mode,
  a `control_request` with an **unknown** subtype produces zero output bytes. This is the
  sole red for a predicate that matched on `type` alone. The rest of AC4 is covered by
  two existing tests that must stay green **unmodified**:
  `TestRunStreamJSON_NonUserLinesIgnored` (interrupt + blank + unparsable → zero bytes in
  default mode) and `TestRunStreamJSON_InterruptAckRider` (interrupt mode still emits its
  ack + interrupted result, in that order). Do not restate their rows here.

**Failure messages print key NAMES, never entry values from the capture.** The capture is
a live recording from a real developer machine; its `argv` carries a home-directory path
and its inner payload carries an `account` object. This test walks only `models` and needs
only names — keep it that way, and do not dump a whole decoded entry or a whole file into
a `t.Errorf`.

**The test is read-only against `testdata/`, and that is a rule rather than an accident.**
`go test` runs in the package source directory, so a relative path reaching `../../realclaude/testdata/`
reaches the committed captures themselves — which is why `initialize_control_names_test.go`
bans os writes over its own file outright. This test opens those paths for reading only:
no `os.WriteFile`, no `os.Create`, no temp file beside them, no `os.Remove`.

**Gate:** `make check` — which runs the new untagged test plus every existing fake-daemon
e2e suite. `make e2e-realclaude` is **not** needed: nothing in `internal/e2e/realclaude`
changes, and the capture is read as a file, not through that package.

## Budget

Hold this inside the size-S envelope. Measured against the nearest analogues in this same
file — #1500's ack rider (139 insertions), #1411's rate-limit rider (206), #1137's per-child
tee (181) — the target is **≈130 added lines in `main.go`** (arm, predicate split, writer,
canned entries, the provenance doc comments this file's style requires) and **≈230 lines in
the new test file**.

Explicitly **not** in scope, because each is a plausible-looking way to blow the budget:

- No new env knob. Nothing sends `initialize` yet, so an unconditional answer changes no
  existing suite's bytes, and § Configuration in the package overview says to prefer an
  existing mechanism over a new one.
- No third canned model entry (`opus` / `supportsFastMode`).
- No adversarial input table over the request line, and no fixture-name / glob-family lock
  — `initialize_control_names_test.go` already owns that property.
- No assertion that the canned **envelope**'s key set matches the capture's envelope; AC3
  scopes the cross-check to model entries.
- No edit to `docs/knowledge/features/fakeclaude-binary.md` or any other
  `docs/knowledge/` file — the documentation phase owns those.
- No change to `stream_detect_test.go`. The new file stands alone.

## Open questions

- **`runStreamJSONApprove` is deliberately left unanswering.** The approve rider (#1139)
  duplicates the read loop rather than sharing `runStreamJSON`, and adding the arm there
  too would mean either a second copy or the loop merge that rider deliberately avoided.
  Once #1689 lands, an approve-rider child will simply not answer the daemon's initialize
  request — which the daemon must tolerate anyway, since real claude can be slow or absent.
  If #1689's design turns out to *require* an answer from every stream child, that is a
  follow-up ticket against the approve loop, not a widening of this one.
- **`writeInitializeAck` takes no models parameter.** #1683 extends the same answer with
  `commands`; if that ticket wants per-test control over the payload it will need a seam,
  and the natural one is a parameter on this writer or a second package-level var. Decided
  here for the simplest thing that satisfies both consumers today.
- **Hostile-shaped payloads are out of scope, by name.** #1701's security review parks
  hostile-shape coverage for #1690's decoder as "#1690's and #1692's"
  (`docs/specs/architecture/1701-initialize-capture-fixture-record.md`). This ticket ships
  the **well-formed** answer the happy path needs — a hostile default would break every
  consumer riding it — and #1690 feeds its decoder hostile bytes directly from unit-test
  fixtures rather than through this fake. If a later slice shows it needs them from here,
  that is a separate ticket with its own default-off knob.

## Security review

**Verdict:** PASS (after two MUST FIX revisions, both applied inline above before this
section was written; the checklist was then re-walked from the top).

**Findings:**

- **[Trust boundaries] MUST FIX — fixed inline.** The design adds one boundary crossing:
  `controlRequestID` decodes an inbound stdin line and hands `requestID` — bytes the
  daemon wrote, unvalidated — to `writeInitializeAck`, which reflects it back onto stdout,
  which the daemon's stream parser reads as line-delimited JSON. The first draft specified
  the writer's shape without saying why the marshal is load-bearing, leaving
  `fmt.Sprintf` open to a developer as an equivalent choice. It is not: a `request_id`
  carrying `\n` plus a forged envelope would split the output line and fabricate a stream
  event the parser consumes as real. § 3 now states this as a rule and names
  `writeAssistantEcho` as the existing dependence on the same escaping property. The
  boundary is a single named function, not scattered: `controlRequestID` is the only
  decode and `writeInitializeAck` the only reflection. Downstream, the echo returns the
  daemon's **own** value to the daemon, so it carries zero information gain and cannot be
  an exfiltration channel whatever #1690 later does with the inner payload.
- **[Tokens, secrets, credentials] MUST FIX — fixed inline (exposure, not handling).** The
  design mints, stores and compares no secret; the canned model list is public product
  metadata and the `request_id` is the daemon's own. The real exposure is the **capture
  the new test reads**: `initialize_control_v2.1.239.json` is a live recording whose
  `argv` carries a home-directory path with a real username, whose inner payload carries
  an `account` object (`tokenSource` / `apiProvider` — names, not values) and a `pid`. A
  test that dumped a decoded entry or a file into `t.Errorf` would republish that into CI
  output on every red. § Testing strategy now mandates key-names-only failure messages and
  scopes the walk to `models`. `realclaude`'s own credential net (`dropcapScanner`) does
  not cover this read — it is scoped to its own fixture family — so the rule has to be
  stated here.
- **[File operations] No findings, with one rule added.** No file is created, so the mode /
  atomic-write / symlink questions do not arise; stream mode returns before the `mustEnv`
  calls and the transcript open, so `runStreamJSON` binds no sessions dir at all. The only
  filesystem contact is the test's read: a **hard-coded literal** glob pattern and
  `os.ReadFile` on its matches. No decoded value reaches `filepath.Join` anywhere in this
  design — unlike `argvSessionID`, there is no stem to guard because there is no path to
  build. No stat-then-open, so no TOCTOU. Added defensively: § Testing strategy now states
  the test is read-only against `testdata/`, because `go test` runs in the package source
  directory and a relative write from a test reaches the committed captures — the hazard
  `initialize_control_names_test.go` bans over its own file.
- **[Subprocess / external command execution] No findings — nothing is spawned.** The new
  arm and its test are pure I/O against `strings.Reader` / `bytes.Buffer`; no
  `exec.Command`, no `sh -c`, no signal handling. The arm reads **no environment variable**
  (it is unconditional, by design), so it changes nothing about what a child inherits.
- **[Cryptographic primitives] No findings — none reached.** No RNG: `uuidV4` is not on
  the stream path, and the correlation id is the daemon's, echoed rather than generated.
  No comparison against a secret, so `crypto/subtle` does not apply — the one equality
  check (`response.request_id`) lives in the test and compares against a test literal.
- **[Network & I/O] No findings; one pre-existing property named.** No socket, no TLS, no
  HTTP server. Input size: `runStreamJSON` deliberately uses `bufio.Reader.ReadString`
  rather than `Scanner` so a long line is not truncated, which means an unbounded line is
  materialised in memory — pre-existing, deliberate, and scoped to a test binary reading a
  pipe from the daemon under test. The new arm does not widen it: it decodes the buffer the
  loop already holds. Amplification is 1:1 and bounded — one inbound request produces one
  outbound line of fixed size, with no per-entry fan-out, a weaker ratio than the interrupt
  rider's existing two lines per request.
- **[Error messages, logs, telemetry] No findings.** This binary has no logger and the
  stream path writes nothing to stderr; a write error ends the loop silently, matching every
  other arm's teardown discipline, and never `os.Exit`s mid-turn. The only new operator-
  visible text is the test's failure output, constrained by the finding above.
- **[Concurrency] No findings — nothing concurrent is added.** No goroutine, no lock, no
  `atomic.Bool` signal, no shared state; the arm runs on `main()`'s goroutine inside the
  single-reader loop, so there is no lock order to document and no check-then-mutate. Killed
  mid-write, the worst case is a truncated stdout line, which the parser drops as
  unparsable; no on-disk state exists to leave partial, because stream mode opens no file.
- **[Threat model alignment] OUT OF SCOPE, named.** `docs/protocol-mobile.md` § Security
  model governs the relay/phone path; this binary never runs in production, binds no socket
  and is reachable only by the daemon that spawned it. The repo invariant that fakeclaude
  "never echoes stdin content to stdout" is a **PTY-path** rule about observed substrate;
  the stream path already echoes deliberately (#1140's prompt echo, #1500's `request_id`
  echo), and this ticket adds the narrower of the two shapes, matching what the committed
  capture shows real claude doing. Whether an inner-payload field ever reaches a client is
  #1690's decision, not this fake's. Hostile-shaped initialize payloads are deferred by
  name to #1690, per § Open questions.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-24
