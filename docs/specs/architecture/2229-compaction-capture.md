# #2229 — capture the stream-json lines a real compacting turn emits

## Files read

- `internal/e2e/realclaude/tool_progress_capture_test.go` → `TestRealClaude_ToolProgressCapture`,
  `tpcapRecord.fixtureWorthy`, `tpcapRecord.stagingVerdict`, `tpcapCensus`, `tpcapWriteRecord` —
  the nearest analogue in every dimension this ticket has: fixture-absence gate, in-repo promotion,
  version pin from both ends, and a verdict that names WHICH reading a zero-hit run is.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapRecorder` (line splitting,
  `resultSeen`, `snapshot`), `dropcapCaptured`, `dropcapRedactor`, `dropcapScanner`,
  `dropcapMakeEntry` (payload encoding incl. the base64 arm), `dropcapEncodingJSONString`,
  `dropcapWaitForChild`, `dropcapSpawnWait` — the rig this probe reuses whole.
- `internal/e2e/realclaude/harness_streamparse_test.go` → `parseOne` — replays one line through the
  shipped parser so the record carries the parser's verdict as data.
- `internal/e2e/realclaude/background_trigger_probe_test.go` → `probeClaudeVersion` — the observed
  `claude --version`, which both ends of the pin compare against.
- `internal/e2e/realclaude/resilience_test.go` → `resolveClaudeBin`; `fixtures.go` →
  `WithWorktreeAuthenticated` — the credential skip that MUST precede `newDropcapScanner`, whose
  needles are read from the environment that helper re-pins.
- `internal/streamsup/parser.go` → `Parser.emitSystemSubtype` — the one enumeration of mapped
  `system` subtypes (five today), whose `default` sends everything else to `emitUnrecognized`.
  Which seam the compaction lines arrive on is exactly what this capture decides, so the probe must
  not filter on it.
- `internal/streamsup/tool_progress_capture_test.go` → `capturedToolProgressLines`,
  `toolProgressCaptureVersion` — the shape of a hermetic reader over an `internal/e2e/realclaude`
  fixture, and its rule that every failure is `t.Fatalf`, never a skip, once the bytes are committed.
- `docs/knowledge/features/e2e-realclaude-tool-progress-capture-test-go.md` — the fixture-absence
  gate's rationale, and the FIFO-hold hazard this ticket deliberately does not inherit.
- `docs/knowledge/features/e2e-realclaude-interactive-stream-announced-reset-test-go.md` — the
  `/clear` precedent: a slash command sent as ordinary message text IS honoured on this input path,
  and "whether such a turn ever closes on its own is unknowable in advance". That sentence is why
  the compact turn must not block on a `result` line.

## Context

`protocol.CompactingPayload`, `turnbridge.MapEvent`'s `turnevent.Compacting` arm and the desktop's
status row are all shipped, and nothing constructs the event: the only producer was the terminal
path deleted in #1348. What claude actually sends is on record only as an SDK type declaration.
#2227 (the on/off edges) and #2228 (`trigger`, token counts, failure) both read the bytes this
ticket commits, which is why the capture is its own ticket rather than the first half of either.

No ADR is warranted: this adds no decision, only an observation.

## Design

Two new files, both `_test.go`, zero production source files.

### `internal/e2e/realclaude/compaction_capture_test.go` (`//go:build e2e_realclaude`)

Prefix `ccap` on every file-local identifier, for the reason #1260's header gives.

**Gate.** The fixture's absence, exactly as `TestRealClaude_ToolProgressCapture` argues it:
`os.Stat(ccapFixturePath)` succeeding skips, `PYRY_PROBE_COMPACTION_CAPTURE=1` forces a re-capture.
No unconditional env gate — that shape skips before the credential check and greens `make
e2e-realclaude` while producing nothing.

**Staging — three turns on one child, not one.** `/compact` on an empty conversation is the
likeliest way a no-compaction run happens for a rig reason. Two priming turns ask for long
deterministic rig-authored output (numbered lines, no tools), then the third turn's text is
`/compact`. `dropcapRecorder` closes `resultSeen` once, so turn boundaries come from polling
`snapshot()` for a `result` count rather than from the channel; the channel is left to the analogue.

**The compact turn must not block on `result`.** Its wait ends on the earliest of: the third
`result`, a quiescence window with no new line, or the turn budget. `terminated_on` records which,
and every arm still lands the census — the `/clear` precedent says a slash-command turn replying
with nothing is a plausible shape, and #2089's budget arm is the pattern.

**Classification is by content, never by envelope.** A frame is a compaction line when its decoded
JSON carries any key from a marker set (`compact_boundary`, `compact_metadata`, `compact_result`,
`compact_error`, `pre_tokens`, `post_tokens`) at any depth, or a `status` key whose string value is
`compacting`. Keying on the envelope would presuppose the answer; keying on marker KEYS rather than
values is also what keeps the `init` line's `slash_commands` inventory — 114 occurrences of the stem
`compact` in this package's committed testdata, every one a slash-command NAME — out of the record's
compaction count.

**Record.** `ccapRecord` carries provenance (`ticket`, `claude_version`, `captured_at`,
`is_capture`, `model`, `spawn_shape`, `env_delta`, `workdir`, `prompts`), the outcome triple, the
staging measurement (`priming_turns_completed`, `priming_assistant_lines`, `staged_context_tokens`
summed from the priming assistant lines' `usage`, `compact_turn_seconds`), a content-free
`pre_compact_line_census` over the priming lines, and `frames` — **every** line of the compact turn,
per AC 1, each with its `type`, `subtype`, `compaction` verdict, matched `markers`, the
`dropcapMakeEntry` payload half and `events_emitted` from `parseOne`. Then
`compaction_line_count`, `compaction_shapes` (`type` alone when no subtype, else `type + "/" +
subtype`) and the redaction/deny-scan block inherited whole from #1260.

**`fixtureWorthy() (string, bool)`** is the only thing between a live run and a committed fixture:
outcome `fired`, at least one compaction line, `claude_version` matching the version spliced into
the fixture name, and every compaction frame encoded `json-string` (the reader refuses base64).

**`stagingVerdict() string`** answers AC 5's "which reading is it": the compact turn never went out
(instrument), the priming staged no measurable context (the rig never staged a compactable
conversation), or the priming landed and claude produced nothing (claude declined to compact — a
finding to route back, not a reason to loosen the marker set).

The record is written to an `os.MkdirTemp` artifact dir registered in `t.Cleanup` before anything
can fail, so a structural fatal still leaves the evidence; a `fixtureWorthy` record is additionally
written to `ccapFixturePath` in-repo by the run that produced it, and the log line instructs the
`git add`. Both writes are `0600` under a `0700` temp dir; the fixture path is a compile-time
constant, never derived from an input, so no value reaching this probe can steer a write.

`newDropcapScanner` is constructed AFTER `WithWorktreeAuthenticated`, and the ordering is
load-bearing rather than stylistic: the scanner reads `CLAUDE_CODE_OAUTH_TOKEN` and
`ANTHROPIC_API_KEY` through `os.Getenv` as deny needles, and that helper is what re-pins them into
this process. Built first, the credential needles are empty, `dropcapScanner.scan` reports them
`notApplied`, and the net is off while every message still reads green — so the skipped classes also
ship in the record as `credential_scan_skipped`.

Every fatal below reports counts, indices and class NAMES only. The frames themselves are in the
record the cleanup has already written; putting claude's bytes or a matched deny value into CI
output is precisely the exposure the deny-scan exists to prevent.

**No FIFO.** `tpcapHoldFIFO` exists to hold a foreground Bash call open across a heartbeat tick.
This probe holds nothing open, so the mechanism and its two repair legs are not inherited.

### `internal/streamsup/compaction_capture_test.go` (no build tag — inside `make check`)

Its own path and version constants, its own decode of the record's slice, nothing shared with
`capturedToolProgressLines` (whose docblock forbids growing it a path parameter).

`ccapPinnedShapes` is the committed measurement — the `type`/`subtype` set the capture observed.
The reader's gate is a pure function of two booleans, `(fixtureExists, pinEmpty)`:

| fixture | pin | action |
|---|---|---|
| absent | empty | skip — the pre-capture leg, and the ONLY legal skip |
| absent | filled | fatal — the pin names shapes whose bytes are gone |
| present | empty | fatal — the live gate landed bytes and the pin was never written |
| present | filled | run the assertions |

That is AC 4's end state made self-sequencing: the commit that lands the fixture cannot leave the
pin unwritten, and it is what lets this ticket's first commit keep `make check` green before the
live gate has ever run. The gate is a pure function so its four quadrants are proved offline on
every run, including the leg where the fixture is still absent.

When it runs: `is_capture` true, `claude_version` matching, at least one compaction frame (zero
fatals — a vacuous fixture proves nothing), each compaction frame's recorded `type`/`subtype`
re-derived from its own payload bytes rather than trusted, and the observed shape set equal to
`ccapPinnedShapes`.

## Concurrency model

One `streamsup.Runner` goroutine per the analogue, cancelled from `t.Cleanup` with a bounded wait on
its exit. `dropcapRecorder` is mutex-guarded because `os/exec` drives `Stdout` from its own copier
goroutine while the test goroutine polls `snapshot()`. No other goroutines; nothing to leak.

## Error handling

Every failure mode is a recorded outcome, not a panic: `instrument-broken` for a rig fault
(`streamsup.New`, no live child, a failed turn write), `did-not-fire` with the staging verdict for a
turn that produced no compaction line, `fired` otherwise. The deny-scan is fail-closed — on a hit
nothing is written, not the record and not the fixture, and the message names the class only.

## Testing strategy

Offline, inside `make check` (no claude, no credentials):

- The classifier: an `init` line whose `slash_commands` inventory contains `compact` is NOT a
  compaction line; a `status:"compacting"` line is; a `status:null` line carrying `compact_result`
  is; a `compact_boundary` line carrying `compact_metadata` is; an ordinary assistant line is not.
  This is the load-bearing offline test — it is the one that proves the census cannot be fooled by
  the 114 committed occurrences of the stem.
- `fixtureWorthy`: one promoted row plus a refusal row per rejection arm, each asserting a reason is
  named.
- `stagingVerdict`: one row per reading, asserting only the third reads as a finding about claude.
- The reader's gate: all four quadrants of the table above.

Live, `make e2e-realclaude` on an authenticated machine: `TestRealClaude_CompactionCapture` produces
the record and, when `fixtureWorthy`, the fixture. The probe's own offline tests are what run in the
meantime; the reader's assertions arm on the commit that lands the bytes.

## Open questions

1. **Which seam the lines arrive on.** The whole point; the capture answers it and the pin records
   the answer. Resolved by the live gate, not by this leg.
2. **Whether `/compact` compacts a short conversation.** Unmeasured. The staging measures
   `staged_context_tokens` so a decline is diagnosable rather than mistaken for a rig miss, and AC 5
   fails loudly either way.
3. **Whether a `/compact` turn closes on its own.** Unmeasured, per the `/clear` precedent. Handled
   by not depending on it: the quiescence and budget arms both land the census.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] SHOULD FIX — the boundary is single and explicit (claude's subprocess stdout →
  `dropcapRecorder.Write` → `dropcapRedactor` → `dropcapScanner` → a file committed to a public
  repo), but this capture's content-bearing field is larger than the analogue's. `tpcapRedactionRationale`
  names `repl_call.inner_tool_input` as the single free-text field in its type; here a
  `compact_result` is a model-authored summary of the whole conversation, which is a different order
  of unbounded. The by-construction defence still holds — a fresh empty non-git workdir under a temp
  `$HOME`, rig-authored prompts, no `os.Environ()` read into the record — but this widening must be
  STATED in `ccapRedactionRationale` rather than left for a reader to infer, because that string is
  what a person deciding whether to paste the record into a public issue actually reads.
- [Tokens] No findings, and the reason is an ordering the plan now names in Design: the scanner is
  built after `WithWorktreeAuthenticated`, so `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` are
  non-empty needles, and `credential_scan_skipped` ships in the record so a silently-off net is
  visible after the fact. The rig's session id is a fixed literal in a per-test temp `$HOME`, not a
  secret; the nonce is `time.Now().UnixNano()`, a prompt correlation value with no security role.
- [Tokens] SHOULD FIX — AC 3 names "session id" as a deny class, and the inherited scheme satisfies
  it because `streamsup.Config.SessionID` is the id claude echoes: the three committed captures
  checked (2026-09-08) carry `$SESSION_ID` and no literal. They do carry per-message `uuid`,
  `tool_use_id` and `parent_tool_use_id` values, which the scheme keeps. That is the established
  reading of the class and this ticket does not change it, but `ccapRedactionRationale` must say so
  — an unstated residual identifier is the same defect as an unstated free-text field.
- [File operations] No findings — the fixture path is a compile-time constant and the artifact dir
  comes from `os.MkdirTemp`, so nothing user- or model-controlled reaches a path; files are written
  `0600`. The gate's `os.Stat`-then-`os.WriteFile` on `ccapFixturePath` is a check-then-use across
  the whole live turn, but the path is a constant inside the worktree and the only writer that could
  race is another copy of this same probe; the consequence is an overwritten file that is reviewed
  before `git add`, not a privilege boundary. A truncated write fails the reader's `json.Unmarshal`,
  which is fail-closed.
- [Subprocess] No findings — no `sh -c`, no user-controlled argv (`--model haiku
  --dangerously-skip-permissions`, the shape both #1260 and #2089 use, kept because a differing
  spawn shape would be a confound in the evidence). The YOLO flag's blast radius is bounded by the
  fresh empty workdir under the temp `$HOME`; all three prompts forbid tool use, and `tool_calls`
  from the reused census makes any call claude runs anyway visible in the record rather than silent.
  The runner is cancelled from `t.Cleanup` with a bounded wait on its exit.
- [Cryptographic primitives] No findings — no crypto, no comparison against a secret, no key
  material. The one randomness use is the correlation nonce, which is not security-relevant.
- [Network & I/O] No findings — no sockets and no server. The input-size question is the recorder's,
  and it is answered by the inherited caps: `dropcapMaxPartial` at 4 MiB on the accumulator and
  `dropcapMaxCaptureBytes` at 8 MiB on the record, both non-silently accounted into
  `lines_dropped_over_cap` / `partials_dropped`, which matters here because a compaction summary is
  the largest payload this family has captured.
- [Error messages, logs, telemetry] No findings — Design now states the rule: fatals carry counts,
  indices and class names only; a deny-scan hit names the class and never the matched value; the
  artifact path is logged through `red.str`. No telemetry.
- [Concurrency] No findings — one goroutine (the runner), one mutex (the recorder's, taken by both
  `Write` and `snapshot`), so no lock ordering to get wrong. The specific hazard this design avoids
  by construction is #2089's: an unbounded wait inside a `sync.Once.Do` cleanup turned a bounded
  failure into a 20-minute suite kill, and it lived in the FIFO hold this probe does not inherit.
- [Threat model alignment] OUT OF SCOPE — every threat in `docs/protocol-mobile.md` § Security
  model: this ticket puts no frame on the wire, touches no relay, transport or Noise code, and adds
  zero production source files. The threat model that does apply is the repo's own rule about what
  may be committed into a public repository, which is what the deny-scan encodes and what the
  findings above are about.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-08
