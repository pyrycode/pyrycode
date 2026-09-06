# #2089 — `tool_progress` gets its own arm in the stream reader

A long-running `Bash` call makes claude emit a `tool_progress` heartbeat every few
seconds. `consumeLine` has no arm for that top-level type and `ignoredLineTypes`
does not hold it, so every heartbeat reaches `emitUnrecognized` and puts an
`Unrecognized message` row in the operator's chat. This ticket consumes the
varieties that carry no news, **by matching**, and leaves the lane reachable for a
variety nobody has measured.

## Sizing note — the ceiling is exceeded, deliberately, on the floor rule

Total written work lands at roughly 1000 lines against a 800-line `size:s`
ceiling — 405 of them this spec, whose mandatory `security-sensitive` review
section is a quarter of it. A ~25% overage, stated rather than engineered away
(an earlier draft of this paragraph guessed 875 before the spec was counted; the
number here is measured). The only
split available is *arm* / *live capture*, and the capture's sole consumer is the
arm's own proof: AC2 forbids proving the heartbeat from a hand-written line
precisely because a decoder keyed on a spelling claude does not use is dead code
no hermetic fixture can catch (the `tool_use_result` / `toolUseResult` failure
`userToolResultLine` records). A first slice split off from its capture cannot be
verified on its own, so the floor wins over the ceiling. Every other boundary
holds with room: 1 production file, 0 new exported types, 0 consumer call sites,
4 acceptance criteria, 4 branches.

## Files read

- `internal/streamsup/parser.go` → `consumeLine` — the switch the new arm joins;
  its `rate_limit_event` (#1404) and `control_response` (#1500) arms are the two
  in-file statements of the consume-by-matching posture, and both give the reason
  this ticket must not reach for `ignoredLineTypes` instead.
- `internal/streamsup/parser.go` → `ignoredLineTypes` — the 2026-07-27 census and
  the `CORRECTED`/`AMENDED` docblock style AC4 asks the new arm to copy.
- `internal/streamsup/parser.go` → `emitSystemSubtype` — the `bool` "did I consume
  it?" return the arm needs, and the rule that the case arms are the ONE
  enumeration of the matched set.
- `internal/streamsup/parser.go` → `emitUnrecognized`, `truncateRaw` — what the
  fall-through costs, and why the lane's value is that it means something new.
- `internal/streamsup/parser.go` → `userToolResultLine` — the precedent for
  decoding one line a SECOND time into a purpose-built struct rather than widening
  `streamLine`, plus the spelling failure AC2 cites.
- `internal/streamsup/parser.go` → `jsonKey` — a presence-only decode target
  retaining no byte of the value; it already answers the present-but-empty
  question `subagent_type` and `repl_call` turn on.
- `internal/streamsup/parser.go` → `streamLine`, `emitBackgroundTaskStarted` — the
  rule that control shapes are read from the TOP LEVEL only and nested content is
  never re-scanned, which is what stops claude's own tool output forging a match.
- `internal/streamsup/capture_test.go` → `capturedLines` — the provenance
  apparatus (`is_capture`, `payload_encoding`, zero-matches-fatals) and the
  docblock forbidding by name its generalisation into a path-taking reader.
- `internal/streamsup/initialize_capture_test.go` → `initCaptureRecord` — #1810's
  reader in the flesh: a second capture file, its own reader, provenance restated
  rather than imported.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapRecorder`,
  `newDropcapRedactor`, `newDropcapScanner`, `dropcapMakeEntry`,
  `dropcapWriteRecord`, `dropcapClassifyAll` — the capture instrument. Two
  findings shape § The capture: the recorder takes `streamsup.Config.Stdout`,
  which is where production installs the parser, so "upstream of the parser" is a
  wiring fact; and `dropcapClassifyAll` keeps only lines the parser DROPPED, which
  would silently omit exactly the frame AC3 must catch.
- `internal/e2e/realclaude/harness_streamparse_test.go` → `parseOne` — classifies
  a line with the SHIPPED parser, never a mirror of its tables.
- `internal/e2e/realclaude/fixtures.go` → `WithWorktreeAuthenticated` — the live
  gate, and the `t.Setenv` whose ordering the security review turns on.
- `docs/knowledge/features/streamsup-package.md` § intro — the package opens no
  transcript path; the parser's only input is the `Write` bytes.

## Context

Four internal engine events feed the one outbound `tool_progress` type, three of
which set a distinct wire marker: `heartbeat: true`, `subagent_type` (both the
resolved and unresolved subagent-retry cases), and `repl_call`. The fourth —
bash/powershell progress — is the residual shape, identified by carrying **none**
of the three.

The decision from the 2026-09-05 backlog audit is **suppression, not mapping**:
none of these carry news this surface renders. Mapping the progress variety onto
the tool row with an elapsed count is a daemon change plus a wire change plus two
client changes, and is its own ticket.

`tool_progress` must **not** join `ignoredLineTypes`. That list is documented as
measured and top-level-types-only, and swallowing the type wholesale would hide a
fifth variety in silence. Because the residual variety has no positive marker, a
marker-set matcher necessarily lets it fall through to the lane. Whether it
reaches this surface at all is the **one open question the capture decides**: the
un-gated `yield-twin` emitter carries no env guard and no throttle, and which of
the two emitters feeds `--output-format stream-json` stdout is not established.

**No ADR is warranted.** This is a fourth case arm on a switch whose posture is
already stated twice in-file; the decision record is the arm's own docblock, which
is where `rate_limit_event` and `control_response` put theirs.

**Deferred, deliberately:** the vendor's three-field validation (string
`tool_name`, string `tool_use_id`, finite `elapsed_time_seconds`). In the vendor's
adapter it runs *after* the ignore test and gates only the frame it goes on to
render; under suppression nothing downstream reads those fields, so porting it
here would validate values no code uses. It goes with the mapping ticket, where it
has a consumer. What this ticket pins instead is what can actually go wrong under
suppression: type-strict marker matching.

## Design

### The arm

`consumeLine` gains one case between `control_response` and `default`:

```go
case "tool_progress":
        if p.consumeToolProgress(line) {
                return
        }
        p.emitUnrecognized(turnevent.UnrecognizedLineType, sl.Type, line)
```

The `emitUnrecognized` call is written out rather than reached by `fallthrough`:
`default` is not the next case in source order, and a `fallthrough` would also run
the `ignoredLineTypes` test — a test this type must never pass.

### The decode target

A second decode of the same top-level bytes into a purpose-built struct, which is
`userToolResultLine`'s shape and for its stated reason: `streamLine` is the
line-level SEGMENTATION struct and a field belonging to one line type would blur
that boundary. `consumeLine` already hands raw bytes to `emitUser`,
`emitRateLimit` and `decodeModelWindows`, so nothing needs widening.

```go
type toolProgressMarkers struct {
        Heartbeat    json.RawMessage `json:"heartbeat"`
        SubagentType *jsonKey        `json:"subagent_type"`
        ReplCall     *jsonKey        `json:"repl_call"`
}
```

Three properties, each of which a more obvious shape loses:

- **`json.RawMessage` for `heartbeat`, not `*bool`.** A `*bool` turns
  `"heartbeat": "true"` into a whole-line `UnmarshalTypeError`. `encoding/json`
  saves that error and keeps decoding, so the *other* markers still populate while
  the call reports failure — leaving the matcher to choose between honouring the
  error (dropping a validly-marked frame to the lane) and ignoring it (losing the
  type-strictness AC2 asks for). A `RawMessage` cannot fail, and
  `bytes.Equal(m.Heartbeat, []byte("true"))` IS the type-strict test: `"true"`,
  `1` and `false` each produce different bytes and none matches.
- **`*jsonKey` for the other two**, so presence is decided with no byte of
  claude's value reaching daemon state. `subagent_type` beats a `tool_name`
  comparison for the retry variety: it is set on both the resolved and unresolved
  frames, it is a literal field name in the declared schema, and no other variety
  sets it — whereas the vendor's own drop test compares `tool_name` against a
  minified module constant this surface cannot pin.
- **The decode reads TOP-LEVEL bytes only** — `streamLine`'s stated property,
  which is what stops a match being forgeable out of claude's tool output nested
  in a `user` line.

Declared adjacent to `consumeToolProgress`, deliberately **not** beside
`userToolResultLine`: `feature/2087` adds lines at that exact anchor (§ Neighbour).

### The consumer

`func (p *Parser) consumeToolProgress(line []byte) bool` — returns whether it
CONSUMED the line. That is `emitSystemSubtype`'s shape and the one that fits:
unlike `emitRateLimit`'s void return, this arm has a genuine "did you handle it?"
to report, because a marker-less frame must reach the lane.

It matches in a `switch { }` over the three markers and on a match logs
site-and-type only, content-free, the way the existing drop branch does. The one
extra field is a `marker` discriminator naming which of the three fired: a
constant this package chose from a closed set of three, never a byte derived from
claude's line — the same class as the `sl.Type` the drop branch already logs. No
match → `false`, and the caller surfaces the line.

**The decode error is not a branch.** `consumeLine` has already decoded this line
into `streamLine`, so it is well-formed JSON with an object at the top level;
`RawMessage` accepts any value and `jsonKey.UnmarshalJSON` discards every one, so
this `Unmarshal` cannot fail. An `if err != nil` arm would be unreachable code —
`userToolResultLine` states the same property for the same reason.

### The capture

A new probe, `internal/e2e/realclaude/tool_progress_capture_test.go`, file-local
prefix `tpcap`, env-gated on `PYRY_PROBE_TOOL_PROGRESS_CAPTURE`. It reuses the
#1260 apparatus in the same package rather than reimplementing it —
`dropcapRecorder` (line splitting + the `result` turn boundary),
`newDropcapRedactor`, `newDropcapScanner`, `dropcapMakeEntry` (payload encoding
including the base64 arm for invalid UTF-8), `parseOne`, `dropcapWaitForChild`.

Two deltas from #1260, and they are why this is a second probe rather than a
parameter on the first:

- **The Bash call runs in the FOREGROUND and long.** #1260 backgrounds it and caps
  it at `BASH_DEFAULT_TIMEOUT_MS=5000`, shorter than the heartbeat interval.
  Heartbeats are what a foreground call in flight produces, so this probe raises
  the timeout well past the interval and prompts for a plain foreground sleep.
- **It records EVERY `tool_progress` line, not the dropped ones.**
  `dropcapClassifyAll` keeps only lines the parser dropped, which would silently
  omit exactly the frame AC3 exists to catch — a marker-less frame emits an
  `Unrecognized` and would therefore never reach the record. The census is the
  point, so the filter is on the top-level TYPE and the parser's verdict is
  recorded per frame as data.

Per frame: index, redacted payload, `events_emitted` (from `parseOne`, the shipped
parser) and the three marker booleans decoded in the probe. Record frame:
`is_capture`, `claude_version`, `captured_at`, marker census, unmarked-frame
count, redaction table, deny-scan classes, limitations.

**Construction order is load-bearing, not stylistic.** `newDropcapScanner` reads
`CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` via `os.Getenv` as deny
NEEDLES, and `WithWorktreeAuthenticated` is what re-pins them into this process's
environment. Building the scanner first yields an empty needle, which
`dropcapScanner.scan` skips as `notApplied` rather than failing — the credential
net would be off with every message still reading green. So the probe orders
`resolveClaudeBin` → `WithWorktreeAuthenticated` → redactor → scanner, mirroring
#1260, and logs the `notApplied` classes so an empty needle is visible.

The record is written under a `t.Cleanup` registered before anything can fail, so
a structural failure still leaves the evidence on disk. The committed fixture is
`internal/e2e/realclaude/testdata/tool_progress_v<version>.json`, `git add`ed in
the same commit as the tests reading it — a capture that is not committed is a
capture that did not happen (#1763).

### The reader, and why it is a new one

`capturedLines`' docblock forbids by name the generalisation into a path-taking
reader, because the `is_capture` assertion is what stops a hand-built payload file
being swapped in behind it; and its `capturePath` constant is pinned to
`dropped_lines_v2.1.220.json`, whose 39 records hold no `tool_progress` line. So
this takes the #1810 shape: a new file
`internal/streamsup/tool_progress_capture_test.go` with its own package constant
for the path and its own reader, provenance **restated rather than imported** —
`is_capture` true, `payload_encoding` `json-string` per record, every record's
type `tool_progress`, and zero matches fatals so no caller can loop vacuously.

## Concurrency model

None added. The parser keeps its single-writer invariant (`os/exec` drives `Write`
from one goroutine); the arm is a pure function of the line it reads and adds no
state, no goroutine and no lock. The `result` arm remains the only reset point for
the only accumulator. The probe reuses `dropcapRecorder`'s mutex and #1260's
cleanup ordering: cancel the runner context, wait for `Run` to return, write the
record.

## Error handling

| Failure | Behaviour |
|---|---|
| Line is not valid JSON | Unchanged — `consumeLine`'s undecodable arm, before this switch. |
| `tool_progress` with a known marker | Consumed. Zero events, one content-free `Debug`. |
| `tool_progress` with no known marker | Exactly one `Unrecognized{LineType, "tool_progress"}` — the alarm, deliberately preserved. |
| `heartbeat` present but not JSON `true` | Not a match. Falls to the lane. |
| `subagent_type` / `repl_call` present as `null` | Not a match — a null marker is not a marker. Falls to the lane. |
| Probe records zero `tool_progress` frames | `t.Fatalf`. A vacuous capture must fail, not pass. |
| Probe records a marker-less frame | Record written, then `t.Fatalf`. The un-gated emitter is live here and the marker set is insufficient — a finding to route back, not a hole to ship. |
| Deny-scan hits on the record | Nothing written; the message names the CLASS only. #1260's fail-closed rule verbatim. |

## Testing strategy

`internal/streamsup/tool_progress_test.go` (new file — § Neighbour):

- **Every captured frame emits zero events.** Walks the committed capture through
  a fresh parser; non-vacuous by the reader's zero-matches fatal.
- **A heartbeat-marked frame is present in the capture, and it is silent.** The
  test decodes `heartbeat` out of the raw bytes **itself**, independently of
  production code; zero such frames is a `t.Fatalf`. That is what makes a decoder
  keyed on a spelling claude does not use fail loudly instead of silently never
  firing.
- **Type-strict matching**, table-driven over synthesized lines: `"heartbeat":
  "true"`, `"heartbeat": 1`, `"heartbeat": false`, and a frame carrying none of
  the three → exactly one `Unrecognized` at `UnrecognizedLineType`, kind
  `tool_progress`; `heartbeat: true`, `subagent_type: "…"`, `repl_call: {…}` →
  zero events. Synthesized lines are legitimate HERE and not for the heartbeat
  proof: these assert the matcher's *tolerance*, which is our rule, not claude's
  spelling.
- **`tool_progress` is not on `ignoredLineTypes`** — a direct assertion, so the
  consume-by-matching posture fails a build if someone later takes the shortcut.
- **The drop log is content-free**: message, type, marker constant, no payload.

Live tier: the probe, opt-in, one claude turn, carrying AC3's assertions.

Phase B gate: `go test -race ./internal/streamsup/... ./internal/e2e/realclaude/...`,
`go vet ./...`, `go build ./cmd/pyry`, plus the gated probe run producing the
fixture.

## Neighbour — `feature/2087`

The § A2 branch scan found `origin/feature/2087` (PR #2132, open) touching both
`internal/streamsup/parser.go` and `internal/streamsup/parser_test.go`. The ticket
body anticipates it: *"the regions do not overlap; expect a rebase whichever lands
second."* Read hunk-by-hunk that holds, and it is why this plan is committed
rather than the ticket blocked:

- #2087's `parser.go` hunks sit at the constants above `ignoredLineTypes`, around
  `userToolResultLine`, and inside `emitUser`/`decodeBlock`. This ticket's arm
  sits inside `consumeLine`'s switch and its helper below `emitSystemSubtype` —
  disjoint, and git resolves disjoint hunks without a conflict.
- The one real collision risk is the `userToolResultLine` **anchor**, where #2087
  adds six lines. This plan therefore declares `toolProgressMarkers` next to its
  consumer instead of in the types block. That is a design constraint, not a
  preference.
- #2087 appends 283 lines to the END of `parser_test.go`, so an append there from
  this branch would be an add/add at the same anchor. **All** of this ticket's
  unit tests therefore go in a new file.

Blocking instead would route the ticket back to the agent that already made this
call, for a guaranteed no-op cycle. The judgement is recorded on the issue.

## Open questions

1. **Does the un-gated (`yield-twin`) emitter reach this surface?** If bash
   progress arrives on stdout, a marker-less frame lands and AC3 fails the probe
   by design. Resolved by the capture; the answer goes in the arm's dated
   docblock either way.
2. **Which varieties does one turn observe?** Heartbeat is expected; a subagent
   retry and a REPL call are not reachable from a single foreground Bash call.
   AC4 requires each recorded as observed or **absent** — absent is a measurement,
   not an omission, and an unobserved variety is never quietly added to the marker
   set on the strength of the schema alone.
3. **The heartbeat interval on this model.** Reported ~30 s on Desktop. The
   probe's sleep length and turn budget are sized from the observed interval; if
   one turn yields no heartbeat the sleep grows before the marker set is
   questioned.

Each is resolved in Phase B, and any design change is recorded in a `## Revisions`
entry.

## Security review

**Verdict:** PASS (second pass; the first found one MUST FIX, addressed below and
folded into § The capture before this section was written).

**Findings:**

- **[Trust boundaries] No findings.** The boundary is claude's stdout → parser
  state, and this arm moves **nothing** across it. `jsonKey` retains no byte by
  construction; `heartbeat`'s `json.RawMessage` is a function local compared by
  `bytes.Equal` and never stored, logged or emitted. The boundary stays one named
  function, `consumeToolProgress`, and the decode reads TOP-LEVEL bytes only —
  `streamLine`'s rule, which is what stops a match being forgeable out of claude's
  own tool output.
- **[Trust boundaries] SHOULD FIX — the arm is a silencing primitive, and that
  belongs where someone will widen it.** A marked frame is dropped without trace
  beyond a `Debug`. The failure direction is the safe one: suppression can only
  WITHHOLD a row that carries no content, never inject one. What holds that bound
  is the marker set staying narrow and type-strict, so the arm's docblock must say
  that loosening a marker (any truthy `heartbeat`, a `tool_name` prefix) trades
  the property away. `harnessNoOutputNudge` is the in-file precedent for arguing a
  suppression's tolerance rather than asserting it.
- **[Tokens, secrets, credentials] MUST FIX — ADDRESSED.** The first pass found
  the plan silent on probe ordering. `newDropcapScanner` reads the two credential
  variables via `os.Getenv` **as deny needles**, and `WithWorktreeAuthenticated`
  is what re-pins them; building the scanner first yields an empty needle, which
  `dropcapScanner.scan` reports as `notApplied` — silently skipped, **not** fatal.
  The credential net would be off while every message said the capture passed.
  § The capture now pins the order and requires `notApplied` to be logged.
- **[Tokens, secrets, credentials] SHOULD FIX — `repl_call.inner_tool_input` is
  the one input-bearing field in this type's declared schema.** Every other field
  is an identifier or a counter (`tool_use_id`, `parent_tool_use_id`, `task_id`,
  `uuid`, `session_id`, `tool_name`, `elapsed_time_seconds`), and `session_id` is
  a fixed rig literal the redactor substitutes. The primary defence is #1260's, by
  construction — a fresh empty non-git workdir and a rig-authored prompt, so the
  only input is our own sleep command — with the declared substitution table and
  the fail-closed deny-scan behind it. Phase B names `inner_tool_input` explicitly
  in the probe's redaction rationale as a KEPT, potentially input-bearing field,
  so an operator pasting the fixture into a public issue is told rather than left
  to infer.
- **[File operations] No findings.** The record is written at `0o600` into
  `os.MkdirTemp` (deliberately not `t.TempDir`, so the operator can commit it);
  the fixture path in the streamsup reader is a **package constant**, never a
  parameter — the property `capturedLines` forbids generalising away and #1810
  restates rather than imports. No user-controlled value is concatenated into any
  path; no check-then-use on a caller-supplied path.
- **[Subprocess / external command execution] No findings, one named residue.**
  The spawn is #1260's posture verbatim: `streamsup.New`, no `sh -c`, argv from
  fixed literals, `Config.Env` nil so the child inherits this process's
  environment. `--dangerously-skip-permissions` means claude may run commands it
  chooses; that is bounded by the rig-authored prompt and the fresh empty workdir,
  and is the same exposure #1260 accepted and documented in
  `dropcapSpawnShapeDelta`. Teardown is `Run`'s descendant-group reap on cancel.
- **[Cryptographic primitives] Not applicable, by design.** No randomness, no key,
  no comparison against a secret. The one equality test is `bytes.Equal` against
  the literal `true` — a public constant from our own code, not
  attacker-controlled-vs-secret, so `crypto/subtle` would be cargo.
- **[Network & I/O] No findings.** No new socket, no new reader. Input size stays
  bounded by the parser's existing `maxBuf`; the new `RawMessage` allocation is
  bounded by a line already in memory and freed with the function frame. The probe
  keeps #1260's two caps, which drop whole lines rather than truncating.
- **[Error messages, logs, telemetry] SHOULD FIX — the AC3 failure paths must
  report counts and indices, never payloads.** "Marker-less frame observed" and
  "zero frames captured" are the two new messages that could put claude's bytes
  into CI output — precisely the exposure the deny-scan exists to prevent. Phase B
  reports frame index, marker census and count only; the evidence is in the record
  on disk, which the `t.Cleanup` has already written. The production `Debug`
  carries the type plus a marker constant from a closed set of three, no payload
  byte.
- **[Concurrency] No findings.** Production adds no state, no goroutine, no lock;
  the single-writer invariant is untouched and the `result` arm remains the only
  accumulator boundary. The probe reuses `dropcapRecorder`'s mutex and #1260's
  cleanup ordering, so a structural failure still leaves evidence and leaks no
  goroutine.
- **[Threat model alignment] No findings.** The CLI-relevant threat is the daemon
  parsing untrusted subprocess output, and this ticket's posture IS the alignment:
  the unrecognized lane stays reachable for an unmeasured variety BY MATCHING
  rather than by list membership, so a claude release emitting a new
  `tool_progress` shape raises the alarm instead of being swallowed. Deferred and
  named: the vendor's three-field validation goes with the mapping ticket, where
  it has a consumer.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-06

## Revisions

### 2026-09-06 — the live capture could not be produced; AC3 is unmet

**What changed.** The arm, its unit tests, the capture reader and the live probe
all shipped as designed. The **fixture did not**, so
`TestParser_ToolProgressCapturedFramesAreSilent` — AC2's and AC3's proof — has
never run against a byte claude sent.

**Why.** This machine's claude OAuth session is expired and unrefreshable. The
keychain entry (`Claude Code-credentials`) holds `claudeAiOauth.accessToken` as an
EMPTY string with `expiresAt: 0`, no `~/.claude/.credentials.json` exists, neither
`ANTHROPIC_API_KEY` nor `CLAUDE_CODE_OAUTH_TOKEN` is set, and `claude -p` answers
*"Failed to authenticate: OAuth session expired and could not be refreshed"*.
`TestRealClaude_ToolProgressCapture` therefore skipped at
`WithWorktreeAuthenticated` and wrote nothing. This is a blocked credential, not
a design problem: nothing in the code or the plan changes when it is fixed.

Worth naming because it is the trap CLAUDE.md § Testing describes: the probe run
exited **0**. Only the `=== RUN` count and the skip reason distinguish "the
capture passed" from "the capture never happened".

**Design departures, both narrow and both meant to be reverted:**

1. `capturedToolProgressLines` grows one `fs.ErrNotExist` branch that **skips**
   instead of failing. Chosen over a red build only because the alternative
   reddens `make check` for every unrelated ticket. It is narrowed to that one
   error so a fixture which exists but is malformed, vacuous or foreign still
   fatals, and its message is written to be unreadable as a pass. It is to be
   deleted in the commit that lands the fixture — the reader's docblock says so at
   the branch.
2. `consumeToolProgress`'s dated docblock is headed **NOT YET MEASURED** rather
   than carrying the `MEASURED` census AC4 asks for. Writing an unmeasured census
   in the `CORRECTED`/`AMENDED` style would have been the one thing worse than the
   gap: a confident-looking record of observations nobody made.

**What this leaves true.** The arm is correct for the three varieties the 2.1.259
schema and emit sites declare, and the failure direction is the safe one — an
unmeasured variety FALLS THROUGH to the unrecognized lane, so the residual
bash/powershell-progress shape stays visible as the row it is today rather than
being swallowed. Open question 1 is therefore still open, and it is open loudly.

**To close it:** re-authenticate `claude`, then

```
PYRY_PROBE_TOOL_PROGRESS_CAPTURE=1 go test -tags e2e_realclaude -timeout 15m -v \
  -run '^TestRealClaude_ToolProgressCapture$' ./internal/e2e/realclaude/
```

commit the record it writes to
`internal/e2e/realclaude/testdata/tool_progress_v2.1.259.json`, delete the skip
branch, and replace the docblock's NOT YET MEASURED entry with the census. The
probe fatals on a capture with zero frames and on any frame carrying none of the
three markers, so it cannot land a vacuous fixture or hide the open question.

### 2026-09-06 (rework 1) — the live gate is now what lands the fixture

**Driven by** the verifier's second-pass review on PR #2139: one MUST FIX (the
marker set has never met a byte claude sent) and one NIT (`claude_version`
unread). The credential is unchanged — re-verified at the top of this leg, and
the keychain's `claudeAiOauth.accessToken` is still length 0 with `expiresAt: 0`,
no `~/.claude/.credentials.json`, neither env var set — so the fixture still
could not be produced here. What follows is the credential-INDEPENDENT half, and
it is the half that decides whether the fixture ever lands at all.

**Supersedes the previous entry's "To close it" recipe.** That recipe asked a
human to run a command and copy a file. The recipe is now: log in, run
`make e2e-realclaude`, `git add` what it wrote.

**1. The probe arms itself on the fixture's absence, not on an env var
(`TestRealClaude_ToolProgressCapture`).** § The capture followed the house shape
of the seven sibling probes in that package — an unconditional `PYRY_PROBE_*`
gate plus an `os.MkdirTemp` artifact dir. The verifier's first pass found what
that costs here and the second pass correctly demoted it from a builder defect to
a routing fact: `make e2e-realclaude` never sets the variable, so the live gate
this ticket is *labelled for* skips on the ENV check before reaching the
credential check, passes vacuously, and the fixture never lands. Convention was
right for a one-off instrument and wrong for an acceptance criterion.

So the gate is now the fixture's absence: the probe runs while the file is
missing and disarms once it exists. The env var survives as a FORCE, for
re-capturing at a new claude version. The recurring-token objection to arming a
live probe is answered by the disarm — the cost is one turn, once, and zero on
every run after. Verified on this machine: the probe now reaches
`WithWorktreeAuthenticated` and skips there, naming the missing credential, which
is the honest reason rather than an env var nobody set.

**2. A good capture is promoted in-repo; a bad one never is (`fixtureWorthy`).**
The record still goes to the tempdir as diagnostics, but a capture satisfying AC3
in full is now also written to
`internal/e2e/realclaude/testdata/tool_progress_v2.1.259.json` by the run that
produced it, with a log line saying to commit it. The four refusals — never
fired, zero frames, any unmarked frame, any frame still reaching the lane — are
exactly the AC3 fatals, so the promotion cannot disagree with them. #1763 is the
precedent: a capture that still needs a human to copy a file is a capture that
does not land.

**3. The version pin is enforced from both ends (the NIT).**
`toolProgressCaptureVersion` is spliced into the reader's path constant so the
filename cannot drift from the version it checks, and the reader now fatals when
the record's `claude_version` names a different release. `fixtureWorthy` refuses
to *write* under a mismatched name for the same reason. A claude upgrade is
therefore a loud instruction to re-capture and repin, not a fixture whose census
quietly describes some other release.

**What is still open.** AC2's proof half, AC3 and AC4 remain unmet, and no agent
on this machine can meet them: `claude` re-authentication is an operator action.
The `NOT YET MEASURED` docblock and the reader's `fs.ErrNotExist` skip branch
therefore both stay, and both still say they are to be deleted in the commit that
lands the fixture. What changed is that the deletion is now the only manual step
left — the bytes arrive on their own the first time the live gate runs
authenticated.

**Verified this leg:** `gofmt` clean; `go vet ./...` and `go vet -tags
e2e_realclaude ./internal/e2e/realclaude/` both clean (the tagged package is
invisible to `make check`, so it is compiled explicitly); `go test -race
./internal/streamsup/...` green; the ten `fixtureWorthy` cases green. The
reader's provenance guards were exercised by standing a synthetic fixture at the
pinned path and confirming each one fatals — wrong release, unreadable version,
`is_capture: false` — then deleting it; the tree is clean of it.
