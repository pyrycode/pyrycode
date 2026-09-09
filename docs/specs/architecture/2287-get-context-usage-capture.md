# #2287 — capture what a real claude answers to a `get_context_usage` control request

## Files read

- `internal/e2e/realclaude/initialize_control_probe_test.go` → `runInitControlChild`,
  `initControlLine`, `initControlSummarize`, `initControlScrubbed`, `initControlScanApplied` —
  the file the ticket names as the one to copy: hand-rolled control line, per-arm drive
  sequence, refusal-and-absence recorded as passing outcomes.
- `internal/e2e/realclaude/initialize_control_record_test.go` → `initControlFixtureRecord`,
  `initControlFixtureFields`, `initControlFullRecord` — the record contract and the
  hand-written field listing that is the only instrument catching a misspelled JSON tag.
- `internal/e2e/realclaude/initialize_control_writer_test.go` → `scanInitControlFixture`,
  `writeInitControlFixture`, `compactInitControlRawRows`, `newInitControlOfflineScanner` —
  the fail-closed deny-scan ahead of every filesystem call, and the offline scanner built
  from synthetic constants so no row depends on whose machine ran it.
- `internal/e2e/realclaude/api_retry_capture_test.go` → `TestRealClaude_APIRetryCapture`,
  `arcapFixturePath`, `(*arcapRecord).fixtureWorthy`, `arcapEnableEnv` — the
  fixture-absence arming gate, the double-write to an `os.MkdirTemp` artifact directory,
  and the deliberate break from a compile-time version pin when the record IS the
  deliverable.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `newDropcapRedactor`,
  `newDropcapScanner`, `dropcapScanner.scan`, `dropcapScanner.applied`, `dropcapSubstitution`,
  `dropcapFixedNeedles`, `dropcapMinNeedle` — the `operator_home` class the ticket names, the
  eleven-class deny net, and the arming census.
- `internal/e2e/realclaude/set_permission_mode_probe_test.go` → `setModeRecorder`,
  `setModeWaitFor`, `setModeTurnLine`, `setModeResponseIDMatches`, `setModeScanMax` — the
  stdout recorder and the bounded waits both control-request probes already share.
- `internal/e2e/realclaude/session_transcript_probe_test.go` → `transcriptProbeArgs` — the
  precedent for pinning `--session-id` on a directly-spawned child, which is what makes the
  transcript resolvable by name.
- `internal/e2e/realclaude/interactive_stream_new_session_test.go` →
  `streamNewSessionTranscriptDir` — why the projects directory is located empirically
  rather than by recomputing claude's encoded-cwd slug.
- `internal/streamsup/compaction_capture_test.go` → `compactionReaderGate`,
  `TestRealClaudeCompactionCaptureShapesArePinned` — the four-quadrant fixture/pin state
  machine with exactly one legal skip, and the rule that a reader re-derives shapes from
  each line's own bytes rather than from the record's labels.
- `internal/contextwindow/usage.go` → `Read`, `Usage` — the daemon's own reading, its
  `windows` join and its disproved-window rule (`WindowTokens` 0).
- `internal/streamsup/parser.go` → `resultLine`, `resultModelUsage`, `decodeModelWindows` —
  where the `contextWindow` per model comes from: the `result` line's `modelUsage` map.
- `cmd/pyry/snapshot_usage.go` → `snapshotUsageFor` — how the daemon pairs
  `transcript.StatByID` with `contextwindow.Read`, which this probe mirrors.
- `internal/transcript/transcript.go` → `StatByID` — stem validation before any path join.
- `docs/knowledge/features/e2e-realclaude-compaction-capture-test-go.md` — the lesson that
  drives the whole promotion design: a gate-only live run writes in-repo and loses the
  bytes, so the artifact-directory copy is the one that survives, and a pin with no bytes
  (or bytes with no pin) must fatal rather than pass.

## Context

The daemon writes control requests on the child's held-open stdin today (`WriteInterrupt`,
`WritePermissionMode`, `WriteInitialize` in `internal/streamsup/envelope.go`). The Agent
SDK declares a `get_context_usage` subtype whose response carries the breakdown `/context`
draws. The stem appears nowhere in this tree and the subtype has never been observed on the
CLI transport this daemon speaks. Three things are unknown and expensive to guess wrong:
whether the CLI answers the subtype at all, what the two `detail` values differ by, and
whether claude's arithmetic agrees with what `contextwindow.Read` already computes.

This ticket spends one live gate lap to settle all three and commits the bytes. #2288 is the
production writer and must not gate this measurement — the probe hand-rolls its own request
line, exactly as `initControlLine` does.

No ADR is warranted. The design decisions here (fixture-absence arming, the double-write,
the four-quadrant pin gate) are all restatements of decisions the capture family already
made; the documentation phase should fold the lessons into
`docs/knowledge/features/` rather than open a decision record.

### Size: over the 800-line ceiling, deliberately, on the floor rule

The § A1 boundary is exceeded on exactly one line — total written work, estimated ~1650
against a ceiling of 800. Every other line holds: **zero** production source files (both Go
files are `_test.go`), zero new exported types, zero consumer call sites, five acceptance
criteria, and no state machine with ten reject branches.

The overage stands because the floor rule wins over the ceiling. A probe with no pin writes
a fixture nobody reads; a pin with no probe is a permanent skip. Each half's only consumer
is the other half, so a split produces two children neither of which can be verified on its
own. #2229 shipped this exact shape whole at 1621 lines. The overage is stated here rather
than worked around.

## Design

Two new files, no production code, no existing file modified.

### `internal/e2e/realclaude/context_usage_capture_test.go` (build tag `e2e_realclaude`)

Every file-local identifier takes the `cucap` prefix, per the sibling captures' rule: concurrent
siblings add files to this package and a branch-overlap check cannot see a same-package
identifier collision.

**One child, two send points.** AC 1 says one child drives a completed turn, then a request
at each `detail` value. That is one drive sequence with two send points on one held-open
stdin, not two children: two children would measure two different sessions and AC 2's
comparison needs one. Each arm mints its own `request_id` so responses correlate.

    cucapArm{id, detail}   // "summary" and "full"

**The request line.** A fresh two-field inner struct, marshalled structurally and never
concatenated, so the appended `\n` is the only raw newline:

    {"type":"control_request","request_id":"<id>","request":{"subtype":"get_context_usage","detail":"<v>"}}

The inner struct is not `setModeControlRequestInner` or `initControlRequestInner`: the first
carries a `mode` field with no `omitempty` and the second carries no `detail` at all, so
reusing either changes what `control_request_sent` reads in four committed fixtures.

**The drive sequence.** Spawn under the stream-json argv with `--session-id` pinned to a
fixed literal (the `transcriptProbeArgs` shape), drive turn 1, wait for its `result` line,
then per arm: read a send-point anchor, write the line, wait a bounded budget for a
correlated `control_response`, record the elapsed time. Then close stdin, wait, join the
reader.

**What each arm records** (`cucapArmRecord`): `arm`, `detail`, `control_request_id`,
`control_request_sent`, `control_responses` (verbatim, correlated by request id),
`answered`, `within_wait`, `round_trip_ms`, `response_subtype`, and the read of the
payload below.

**Reading the response without guessing its shape.** The payload's key names are the
unknown, so the summariser does not assume them. It walks the answered response's JSON and
records `numeric_leaves`: a dotted-path → number map over every numeric leaf at any depth,
capped, with a `numeric_leaves_truncated` count. Claude's own total and percentage are then
selected from an ordered candidate list of path suffixes, and `claude_total_source` /
`claude_percent_source` name the path each was read from — empty when nothing matched, which
is itself a recorded measurement. A later slice declares its field set from
`numeric_leaves`, not from this author's reading.

**THE LEAF-MAP KEYS ARE CLAUDE-AUTHORED AND ARE A LEAK SURFACE.** A dotted path is built
from the response's own JSON object keys, and the response is documented to describe memory
files, which live under the operator's home — a map claude keys by file path puts that path
into a committed artifact as a MAP KEY. Both the key and the value go through the redaction
pass, and the deny-scan reads the whole marshalled blob so the fail-closed net covers it
either way. This is the one place in the record where claude chooses an identifier rather
than filling a field the rig named; the sibling family's `credential_scan_applied` doc warns
of exactly this shape one field away.

There is deliberately **no `string_leaves` field**. It would double the record's
claude-authored string surface for no measurement gain: `control_responses` already carries
every byte verbatim and redacted, and that is the ground truth a later slice re-derives
from.

**Leaves are decoded as `json.Number` and stored as strings, never as `float64`.** A
response carrying `1e999` decodes to `+Inf`, `json.Marshal` errors on a non-finite float,
and the whole capture would then abort as a broken instrument over data that is in fact
claude's own answer. `json.Number` round-trips the token verbatim and cannot be non-finite.
A token that does not parse as a finite number is skipped and counted in
`numeric_leaves_skipped`, so the omission is recorded rather than silent.

Three placements are searched for the envelope's `subtype`, as `initControlSummarize`
already does and for its recorded reason: top level, under `response`, and under
`response.response`, the third being where a real reply put it.

**AC 2 — the daemon's reading beside claude's.** After the child exits:

1. Locate the projects directory empirically by polling for `<session-id>.jsonl` under
   `$HOME/.claude/projects/*/`. Recomputing claude's encoded-cwd slug is the fragile path
   `streamNewSessionTranscriptDir`'s doc already rejects. An absent transcript is recorded
   (`transcript_found: false`), never fatal.
2. Build the `windows` map from the LAST `result` line's own `modelUsage`, model →
   `contextWindow`, which is the same channel `decodeModelWindows` reads. Record it as
   `windows_observed` so the join's input is auditable.
3. Call `contextwindow.Read(path, windows)` and record `daemon_used_tokens`,
   `daemon_window_tokens`, `daemon_read_error`, and `daemon_percent` — the latter reported
   only when the window is positive, because `Read` returns `WindowTokens` 0 as its
   "no trustworthy window" signal and a percentage over it would invent one.

The record carries both readings side by side and computes no verdict. Whether they agree
is the later slice's to say; a verdict computed here would be a second source of truth.

**AC 3 — what passes.** Every recorded outcome passes: an unanswered arm, an arm answered
with `subtype:"error"`, a tripped deadline, a non-zero exit. The run fatals only on a broken
instrument or a fail-closed refusal — a spawn or pipe failure, a marshal failure, a
credential in the child's stderr (`cucapScrubbed`, mirroring `initControlScrubbed`), zero
stdout lines captured, or the deny-scan refusing the record.

**AC 4 — redaction and the deny net.** `newDropcapRedactor(home, artifactDir, workdir, "",
sessionID, nonce)` — the constructor that arms `operator_home` from `realHome`, which is the
class the ticket names, plus `artifact_dir` which the double-write needs. A fresh redactor
per write, never shared: its counters are unlocked and its census accumulates.
`newDropcapScanner(home, artifactDir, workdir)` built AFTER `WithWorktreeAuthenticated`, so
the credential needles are non-empty; built before, they arm nothing and report silently as
`notApplied`. The record's `credential_scan_applied` is the scanner's `applied()` completed
so every path class is PRESENT as armed-nothing rather than missing, per
`initControlScanApplied`'s reason.

`cucapScanRecord` marshals a capped copy and refuses on any hit, before the first filesystem
call, so a refused record strands nothing — no file, no `.tmp`. The refusal names the count
and the class names and nothing else.

**The scanner value is two live credentials in a struct. It is never formatted** — no `%v`,
`%+v`, `%#v` or `%q` on it, on a needle, or on the needle slice. `applied()`'s map is safe
and is the diagnostic worth printing: its keys are declared vocabulary and its values bools.

**Promotion, and why it is written twice.** The record goes to an `os.MkdirTemp` artifact
directory outside every worktree unconditionally, and to the in-repo fixture path when
`fixtureWorthy` admits. `#2229`'s in-repo-only write lost its bytes to the dispatcher's
detached gate worktree; `api_retry_capture_test.go` closed that with the double-write and its
artifact copy is what a repair leg recovered. This probe defaults to the double-write.

`(*cucapRecord).fixtureWorthy` refuses, naming the reason: an instrument-broken outcome, a
run that captured zero stdout lines, a run whose turn 1 never closed (the arms then measure a
send point that is not "after a completed turn", which is what AC 1 asks for), a run where no
arm was sent at all (a `-run` filter's partial), and a version token that cannot safely name a
file. It does NOT refuse an unanswered or an error-answered arm: those are the publishable
results, and refusing them would delete AC 3.

**The arming gate is the fixture's absence**, matched by glob over any version.
`make e2e-realclaude` sets no `PYRY_PROBE_*` variable, so an env-armed probe skips at the env
check BEFORE the credential check and the tier reports green having measured nothing. The env
variable can only FORCE a re-capture over an existing fixture.

**The fixture name comes from the observed version**, not a compile-time constant, and
`cucapFixturePath` refuses any token that could compose a path outside `testdata/` — no
separator, a leading digit, a length cap. That is `arcapFixturePath`'s break from #2229's
pin, taken for its stated reason: the record is the deliverable, and a claude bump between
authoring and the live run would otherwise promote nothing.

**Offline tests in this file** (must report PASS, not SKIP, with no claude and no
credentials): the child-budget-dominates-per-step-waits relation with its two vacuity
controls; the summariser's table over synthetic responses covering all three envelope
placements, an error subtype, a truncated leaf map and a response carrying no candidate key;
the `fixtureWorthy` table with a row per refusal reason and a row for each of AC 3's two
passing shapes; the `cucapFixturePath` refusal table; a planted-value deny-scan row over the
marshalled record with its unplanted vacuity control; and the armed-nothing-class-is-present
row.

### `internal/streamsup/context_usage_capture_test.go` (no build tag — inside `make check`)

The consuming package: #2288's production writer lands in `internal/streamsup/envelope.go`,
and both analogues put the hermetic pin beside the consumer and reach the bytes by relative
path. Testdata is bytes; the build tag belongs to the realclaude package's Go files.

- `contextUsageCaptureGlob` — `../e2e/realclaude/testdata/context_usage_v*.json`. A glob
  rather than a version-spliced constant, matching the producing side's version-from-the-run
  naming. Exactly one match is required: zero is the absent state, and two or more FATALS
  rather than picking one, because ambiguity must refuse rather than guess.
- `contextUsagePinnedShapes []string` — THE MEASUREMENT. One entry per arm, spelled
  `<arm>/<subtype>` when the arm was answered and `<arm>/unanswered` when it was not. Empty
  until the live gate has run.
- `contextUsageReaderGate(fixtureExists, pinFilled) (action, reason)` — pure, four
  quadrants, exactly one legal skip (absent fixture AND empty pin). Bytes with no pin
  fatals; a pin with no bytes fatals.
- `TestRealClaudeContextUsageCaptureShapesArePinned` — gates, decodes, refuses a record whose
  `is_capture` is false, then **re-derives each arm's shape from that arm's own recorded
  response bytes** rather than from the record's `response_subtype` label, and compares the
  sorted set against the pin. Zero arms fatals: a record with none is vacuous.
- `TestContextUsageReaderGateHasExactlyOneLegalSkip` — the four quadrants, table-driven,
  running on every leg including the one where the fixture does not exist and the reader
  above can assert nothing. This is the file's only non-vacuous coverage until the bytes
  land, which is exactly the point.

## Concurrency model

One stdout reader goroutine over `cmd.StdoutPipe()`, exiting on EOF (which follows the stdin
close or the context kill) and closing `readerDone`. No goroutine outlives its child. The
scanner error is written only before that close and read only after it; that ordering is the
whole synchronisation for it. `setModeRecorder` is mutex-guarded and is the only shared
state. `t.Fatalf` is never called from the reader goroutine.

The outer `context.WithTimeout` child budget strictly exceeds the sum of every per-step wait
the drive sequence can spend, and an offline test derives both sides from the budget
constants. That relation is what makes a tripped deadline a MEASUREMENT rather than an
artefact: every per-step budget exists so an absence means absence rather than impatience.

## Error handling

| Condition | Handling |
|---|---|
| Pipe / spawn / marshal failure | `t.Fatalf` — broken instrument |
| Credential in the child's stderr | `t.Fatalf` naming the variable and nothing else; nothing written |
| Zero stdout lines captured | `t.Fatalf` — nothing to capture |
| Deny-scan hit | `t.Fatalf` naming count and class names; no file, no `.tmp` |
| Turn 1 never closed | Recorded; promotion refused; artifact copy still written |
| Arm unanswered | Recorded (`answered: false`), passes |
| Arm answered `subtype:"error"` | Recorded verbatim, passes |
| Transcript never appeared | `transcript_found: false`, passes |
| `contextwindow.Read` error | `daemon_read_error` recorded, passes |
| Non-zero exit / tripped deadline | Recorded, passes |

## Testing strategy

RED before GREEN on the offline half, which is where every mechanism this ticket ships can
be exercised with no claude: the gate's four quadrants, the summariser's placements, the
promotion refusals, the path-token refusals, the deny-scan's planted value. Each is written
to fail against an empty or stub implementation before the implementation exists.

The live half cannot be proved from this session — no agent session on this machine can sign
claude in, so a local probe skips at the credential check and exits 0. The dispatcher's
`make e2e-realclaude` gate is the only place live evidence gets produced, which is why the
probe arms on the fixture's absence and never on an environment variable the gate does not
set, and why the promotion path is in-repo plus an artifact directory rather than a log line
telling a human to go and find a file.

Verification for this ticket: `go test -race ./internal/streamsup/...`, plus
`go vet -tags e2e_realclaude ./internal/e2e/realclaude/` and a `-run` sweep over the offline
tests under that tag, which buys compile-and-execute coverage `make check` structurally
cannot have.

## Open questions

1. **Does the CLI answer `get_context_usage` at all, and is `detail` the field name?** The
   subtype and the field were read from the SDK's type declarations, not from the CLI's
   transport. Unanswerable before the live run — and settling it is the ticket. Both
   outcomes are recorded and pass; an unrecognized-subtype error text is the input #2288
   designs against.
2. **Where in the response does the payload sit?** Handled by construction rather than
   answered: the three-placement envelope search plus the dotted-path leaf maps mean the
   record is readable whatever the nesting.
3. **Does the pinned `--session-id` survive to the transcript filename on this claude
   version?** `session_transcript_probe_test.go` pins it the same way; if it does not, AC 2
   records `transcript_found: false` and the comparison is a recorded absence rather than a
   failed run.

## Security review

**Verdict:** PASS (after one FAIL round — three MUST FIX findings, all resolved in the plan
above rather than deferred)

**Findings:**

- [Trust boundaries] **MUST FIX — resolved.** The boundary is claude's stdout crossing into
  the committed record, and the plan's first draft had claude choosing MAP KEYS on the far
  side of it: `numeric_leaves`' dotted paths are built from the response's own JSON object
  keys, and the response describes memory files, which live under the operator's home. A
  response keying a map by file path would have written that path into a public artifact as
  a key. Resolved by routing both key and value of the leaf map through the redaction pass,
  and by deleting the `string_leaves` field outright — it doubled the claude-authored string
  surface for nothing `control_responses` does not already carry verbatim. The boundary is
  otherwise explicit and single: `cucapScanRecord` is the sole producer of the bytes any
  write puts on disk.
- [Trust boundaries] **MUST FIX — resolved.** A `float64` leaf map made claude's data able
  to abort the capture: `1e999` decodes to `+Inf` and `json.Marshal` errors on it, so a
  legitimate answer would have surfaced as a broken instrument with nothing written.
  Resolved by decoding leaves as `json.Number` and recording `numeric_leaves_skipped` for a
  token that does not parse finite.
- [Tokens] **MUST FIX — resolved.** `newDropcapScanner` reads `CLAUDE_CODE_OAUTH_TOKEN` and
  `ANTHROPIC_API_KEY` via `os.Getenv` as deny needles, and `WithWorktreeAuthenticated` is
  what re-pins them into this process. Constructing the scanner first yields EMPTY needles,
  which `dropcapScanner.scan` reports as `notApplied` — silently skipped, never fatal — so
  the credential net would be off while every message still read green. The plan now names
  the ordering as load-bearing. `TestRealClaude_APIRetryCapture`'s doc records the same trap.
- [Tokens] No further findings. The probe mints no credential, so generation, storage,
  rotation and revocation are all out of its scope. The credential reaches the child through
  the environment while argv carries none, which is why recording `argv` is safe and why the
  record has no `env` field and must never gain one. `cucapScrubbed` fatals on either
  variable's value appearing in the child's stderr, before the write and before any log
  site, comparing raw stderr rather than the capped copy so a token past the cap still fails
  the run; an UNSET variable is skipped rather than compared against `""`, since every
  string contains the empty string.
- [Tokens] The session id is a fixed literal, and that is a deliberate non-secret: it is a
  correlation token in a per-test temp `$HOME`, it is what `dropcapClassSessionID`
  substitutes, and a fixed literal is what keeps the committed fixture diffable across
  re-captures. The `dropcapClassNonce` seed must be a real `time.Now().UnixNano()` and never
  0 — a zero nonce formats as `"0"`, passes the constructor's empty-value guard, and rewrites
  every zero digit in the record to `$NONCE`.
- [File operations] SHOULD FIX, addressed in the design. Path traversal is the live hazard:
  the in-repo fixture's name is composed from `claude --version`'s output, which is
  child-controlled. `cucapFixturePath` is the boundary check — it admits no separator,
  requires a leading digit and caps the length, so every traversal and absolute form is
  rejected and an `"<unavailable: …>"` fails at the first byte. Both writes are `0600`,
  matching `api_retry_capture_test.go` rather than the initialize family's `0644`, because
  the artifact copy lands in a world-readable temp root. The in-repo write is
  temp-file-plus-rename so an interrupted run strands no half-written fixture under the
  target name for a later commit. The deny-scan makes NO filesystem call and runs strictly
  before the first `os.MkdirAll`, so a refused record strands nothing at all.
- [File operations] No finding on TOCTOU or symlinks. The arming gate globs and the write
  follows, but the only party in that gap is the operator's own run and the worst case is a
  re-capture. The projects-directory walk stats paths under a `$HOME` the test itself
  created via `t.TempDir()`; no untrusted party can plant a symlink there.
- [Subprocess] No findings. `exec.CommandContext` with a rig-authored argv of constants plus
  one fixed session-id literal; no `sh -c`, no user-controlled argument, no shell
  interpretation anywhere. The environment is inherited deliberately — that is the credential
  channel — with `$HOME` re-pinned. Both probe turns are TOOL-FREE, which is what lets this
  run omit `--dangerously-skip-permissions` entirely: a tool-free turn cannot stall on a
  permission prompt it can never receive, so the child needs no unsandboxed tool access.
  Teardown is the context deadline plus `cmd.Wait`, with the reader joined before any
  `t.Fatalf` so no goroutine is stranded.
- [Cryptographic primitives] Not applicable, and by design rather than omission: nothing here
  is security-relevant randomness. No comparison in this file is against a secret, so
  `crypto/subtle` buys nothing — `cucapScrubbed`'s `strings.Contains` defends a token against
  the claude binary, which was handed that token.
- [Network & I/O] SHOULD FIX, addressed. No network of any kind. Per-line input is capped at
  `setModeScanMax` and an over-long line surfaces as `scanner_error` rather than truncating
  in silence. Total captured output was unbounded in the first draft: stderr goes through
  `capFixtureCapture` inside the scan step, so the bytes scanned are exactly the bytes on
  disk, and the recorded stdout line count is capped with the drop count recorded. The turn
  budget and `--max-turns` bound it in practice; the cap is what makes that structural.
- [Error messages, logs, telemetry] No findings, and the constraints are stated at the sites
  rather than only here, because this is the category with the most ways to invert the whole
  control. The record is NEVER `%+v`'d into a log or a fatal — that moves capped child output
  out of the bounded file into an unbounded run log this pipeline salvages. The scanner value
  is TWO LIVE CREDENTIALS IN A STRUCT and is never formatted by any verb, nor is a needle or
  the needle slice; `applied()`'s map is the safe diagnostic, its keys being declared
  vocabulary and its values bools. The deny-scan refusal names the hit COUNT and the CLASS
  NAMES and nothing else — no excerpt, no offset, no needle value, no record dump. Every log
  site reads the record's POST-pass fields, never the pre-pass locals: the initialize family
  measured a leak past a correctly-placed redaction pass for exactly that reason, so position
  alone is not the guarantee. The artifact directory is logged only through the redactor.
- [Concurrency] No findings. One goroutine, one mutex-guarded recorder, so there is no lock
  order to document. `t.Fatalf` is never called off the test goroutine — neither the scan
  step nor the writer is reachable from the reader. Shutdown is stdin close → EOF → reader
  exit → `readerDone`, with the context kill as the backstop, so no goroutine outlives its
  child. A mid-write signal leaves no partial state under the target name because the in-repo
  write renames into place.
- [Threat model alignment] Not a relay ticket, so `docs/protocol-mobile.md` § Security model
  does not bind. The one CLI-relevant threat this touches is a committed artifact leaking
  operator identity, which the ticket names; the answer is redact, then scan, then refuse,
  with `credential_scan_applied` recording which classes were actually armed so that "scanned
  and clean" cannot be read off a record that was never scanned. OUT OF SCOPE, named: the
  production writer's own trust boundary over this response payload is #2288's — this ticket
  ships no production decoder and no production writer.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-09

## Revisions

**2026-09-09, during implementation.** Three additions the plan's design implied but did
not enumerate. None changes a contract; they are recorded so a reviewer diffing the plan
against the code does not read them as unplanned.

- `WindowsObserved`' keys go through the redaction pass alongside the leaf-map keys. They
  are model ids, which are claude-authored text — the same reason `contextwindow.Usage`
  refuses to carry a model field. No model id has ever carried a path; the pass visits them
  because a rule that admits a judgement call about which claude-authored strings are safe
  is the rule this pass exists to replace.
- Four offline tests beyond the plan's list: the arm table's distinctness lock, the wire
  line's subtype-and-detail pin, the per-arm response correlation, and, on the hermetic
  side, a row that both `detail` values were actually driven and a row on the unanswered
  arm's spelling. The correlation test is the one this two-arms-on-one-stdin design needs
  and a one-arm probe does not: both replies land on one stdout, so an arm that could claim
  the other's would make every per-arm field describe the wrong request.
- `math_IsNaN` / `math_IsInf` are two-line file locals rather than a `math` import, since
  each is three tokens and the import would be the larger edit.

**Measured size.** The plan estimated ~1650 lines of total written work against the sizing
table's 800-line line, accepted on the floor rule. The actual is 3299: a 2386-line probe
(883 of them comment, 38 declarations), a 499-line hermetic pin, and this 414-line plan.
That is over the estimate as well as over the ceiling, and it is stated rather than
trimmed — the comment ratio is this family's discipline, where each block states why an
assertion is non-vacuous, and cutting it to hit a number would remove the part a later
reader needs. The estimate was low; the boundary judgement (one deliverable, floor beats
ceiling) is unchanged.

**Open questions.** Question 1 (does the CLI answer the subtype, and is `detail` the field
name) and question 3 (does a pinned `--session-id` reach the transcript filename) are
unresolvable before the live gate runs, exactly as the plan states; both outcomes are
recorded and pass, and neither can fail the run. Question 2 (where the payload sits) is
resolved by construction: `cucapSubtypeOf` searches all three known placements and
`cucapLeaves` records every numeric leaf by dotted path, so the record is readable whatever
the nesting turns out to be.
