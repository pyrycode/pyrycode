# #2272 — capture what a live claude answers to `mcp_status`, `mcp_reconnect` and `mcp_toggle`

Ticket: <https://github.com/pyrycode/pyrycode/issues/2272> (split from #2202, `security-sensitive`,
`needs-real-claude`).

## Files read

- `internal/e2e/realclaude/compaction_capture_test.go` → `TestRealClaude_CompactionCapture`,
  `ccapRecord`, `ccapWriteRecord`, `ccapCollect`, `fixtureWorthy` — the probe shape this ticket
  copies whole: fixture-absence gate, artifact dir outside the worktree, deny-scan before any
  filesystem call, in-repo promotion, and a cleanup registered before anything can fail.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapRecorder`, `dropcapRedactor`
  (`addPathClass`, `str`, `strs`, `substitutions`), `dropcapScanner` (`newDropcapScanner`,
  `dropcapFixedNeedles`, `scan`, `applied`), `dropcapMakeEntry`, `dropcapWaitForChild`,
  `newDropcapArgvHandler`, `dropcapEncodingJSONString` — every shared helper, reused rather than
  re-derived. `dropcapFixedNeedles` is what makes AC 4 land: `sk-ant-` is one of its five literals.
- `internal/streamsup/compaction_capture_test.go` → `compactionReaderGate`,
  `compactionCapturePath`, `compactionPinnedShapes`,
  `TestRealClaudeCompactionCaptureShapesArePinned` — the hermetic reader shape AC 5 asks for, with
  its four-quadrant state machine and its "re-derive from the line's own bytes" discipline.
- `internal/e2e/realclaude/initialize_control_probe_test.go` → `initControlRequest`,
  `initControlLine`, `initControlSummarize` — how a `control_request` is marshalled onto held-open
  stdin, and the measured fact that a reply nests its `subtype`/`request_id` under `response` while
  the payload sits one level deeper under `response.response`. Deliberately **not** its
  `initControlFixtureRecord` writer family, per the ticket's technical notes.
- `internal/e2e/realclaude/effort_init_capture_test.go` → the downgraded-arm spawn: `shortSocketPath`,
  the stub listener, the hand-written `--mcp-config` document (transcribed because
  `renderMCPServersConfig` lives in `package main`), and `effortInitClassRunLocal` — the redaction
  class for run-local temp paths that keeps `/var/folders/` out of the record.
- `internal/e2e/realclaude/bypass_approval_argv_probe_test.go` → `bypassArgvInitMCPServers`,
  `bypassArgvRedactor` — and its committed fixtures, which are the evidence that a `pyry_approve`
  entry pointed at a **listening** stub socket is reported `connected` by a real claude.
- `cmd/pyry/mcp_config.go` → `permissionArgs`, `renderMCPServersConfig`, `writeMCPServersConfig`;
  `cmd/pyry/mcp_approve.go` → `mcpServerName`, `approveToolName`, `runMCPApprove`;
  `cmd/pyry/mcp_files.go` → `mcpFilesServerName`, `runMCPFiles` — the argv and the document this
  capture must reproduce, and the fact that both servers serve MCP over stdio and dial the control
  socket only at tool-call time.
- `internal/e2e/realclaude/fixtures.go` → `WithWorktreeAuthenticated`, `ensurePyryBuilt`, `realHome`
  — the auth pin that must precede the scanner, and the `os.MkdirTemp` build dir whose path lands in
  the recorded document.
- `internal/streamsup/runner.go` → `Config`, `buildArgs`, `WriteTurn` — `Args` is pass-through and
  the fixed stream-json prefix plus the id flag are streamsup's own.
- `docs/knowledge/features/e2e-realclaude-compaction-capture-test-go.md` — the load-bearing lesson:
  the dispatcher's real-claude gate verifies from a **detached, discarded worktree and never runs
  `git add`**, so #2229's fixture was written in-repo, logged cheerfully, and lost. The record
  written to an artifact dir outside the worktree is what survived, and the bytes landed later.
  This plan's persistence design is built around that fact rather than around hoping.

## Sizing — the overage is deliberate, and stated

Re-counted against this plan rather than against the ticket's estimate: **0 production source files**,
0 new exported types, 0 consumer call sites, 5 acceptance criteria, and 6 reject branches in the
widest decision (`fixtureWorthy`). Every quantitative boundary holds except one — total written work
lands near 1300 lines across two test files and this spec, roughly 1.6x the 800-line ceiling.

That overage is not a miss to be split away. Every layer a split would cut off — the record type, the
fixture name, the writer, the key-type derivation — has exactly one consumer, this probe, and the
reader consumes a fixture only this probe can produce. A one-consumer slice is part of its sibling,
so the floor rule beats the ceiling rule here: a split would produce children that cannot be verified
on their own, which no continuation leg fixes, where an overage costs at most one. The nearest
analogue, #2229, measured 1380 insertions in the same shape.

## Context

Nothing in this repo has ever sent `control_request{subtype:"mcp_status"}`. The only MCP evidence in
the tree is the `system/init` line's `mcp_servers` list, which carries `{name, status}` and nothing
more, recorded under healthy servers only. #2275 decides what the daemon publishes about MCP servers
and needs observed keys to decode against.

The shapes on record come from `sdk.d.ts`, which is a type declaration rather than a wire
measurement. Reading the installed claude 2.1.259 binary's own bundled request/response schemas
during this plan already contradicts the ticket's premise in one useful way: the per-server status
object declares **`tools` and `capabilities` in addition to** `{name, status, serverInfo?, error?,
config?, scope?}`, and `status` is a five-value enum (`connected`, `failed`, `needs-auth`,
`pending`, `disabled`). Those two extra keys are exactly the kind of thing a decode arm written
against `sdk.d.ts` would drop on the floor — and they are still only a declaration. This capture is
what turns either list into a measurement.

The same read settles the three request shapes, which the probe would otherwise have to guess:

| subtype | declared request fields |
|---|---|
| `mcp_status` | none beyond `subtype` |
| `mcp_reconnect` | `serverName` |
| `mcp_toggle` | `serverName`, `enabled` |

The spelling is `serverName`, camelCase, not `server_name`. A guessed spelling would have produced
an error reply and a fixture measuring the rig's guess rather than claude's contract. The record
still says where the spelling came from, because a schema read out of a binary is a stronger source
than `sdk.d.ts` and still not the wire.

No ADR is warranted. This ticket adds no production code and no decision; it records bytes.

## Design

Two new files, no production source changes.

### `internal/e2e/realclaude/mcp_status_capture_test.go` (build tag `e2e_realclaude`)

Every file-local identifier takes the `mcap` prefix, for the reason #1260's header gives: siblings
add files to this package concurrently and a branch-overlap check does not catch a same-package
identifier collision.

**The spawn — the downgraded arm only.** `--model haiku` plus production's four approval flags:
`--permission-prompt-tool mcp__pyry_approve__approve`, `--mcp-config <path>`, `--strict-mcp-config`,
`--permission-mode default`. No `--dangerously-skip-permissions`. `--strict-mcp-config` is what makes
the recorded server set the rig's own three rather than the operator's machine inventory, which is
both the ticket's stated reason for cutting the bypass arm and the redaction argument for recording
server names at all.

**The document.** Three stdio entries, written at mode 0600 into `t.TempDir()`:

- `pyry_approve` → the built pyry binary, `["mcp-approve", "-pyry-socket", <sock>]`
- `pyry_files` → the same binary, `["mcp-files", "-pyry-socket", <sock>]`
- `pyry_probe_absent` → an absolute path under `t.TempDir()` that is never created, with empty args

The first two are transcribed from `renderMCPServersConfig` rather than called, because that lives in
`package main`. Both serve MCP over stdio and dial the control socket only when a tool is called, so
neither needs the daemon; the probe nonetheless binds a stub listener at `shortSocketPath` with a
bare accept-and-close loop, because the only committed evidence that a real claude reports
`pyry_approve` as `connected` was recorded against a listening socket, and a second broken server
would be a confound in a capture whose whole point is one deliberately broken one.

**The turn structure.** No user turn is sent at all. claude connects MCP servers at startup (the
`system/init` line already reports `mcp_servers`), so the surface is live before any prompt, and a
spawn that never prompts cannot reach a tool call, which is what keeps the permission-prompt tool
unexercised and the blast radius at zero.

The probe holds the child's stdin open and writes three control lines in AC 1's order, each under its
own locally-minted request id, each awaited before the next: `mcp_status` first (a pristine reading),
then `mcp_reconnect` naming the broken server, then `mcp_toggle` disabling the broken server. The
broken server is the target of both mutating verbs deliberately: reconnecting it is the one call
guaranteed to produce a failure path, and toggling it off is a real state change that leaves the two
production servers untouched.

**Correlation.** A reply's `request_id` is read at three placements — top level, under `response`, and
under `response.response` — for the reason `initControlSummarize` records: the measured nesting is
one level deeper than the parser's own `control_response` arm assumes, and a reader that stops early
reports a false absence. Each wait ends on a matching reply or on its budget, and which one is
recorded per request; no wait blocks forever on a verb claude may not answer.

**Key types (AC 3).** For every entry in the `mcp_status` reply's `mcpServers` array, the record holds
the entry's exact key set (sorted) and a map from key to the JSON type of its value (`object`,
`array`, `string`, `number`, `boolean`, `null`), derived by decoding into
`map[string]json.RawMessage` and classifying each value's first byte. Types, not values, so a later
decode arm is written against observed keys rather than against a declaration — and the values are
still present verbatim in the frame the entry came from.

**Frames.** Every captured stdout line gets a frame, not only the replies: an `init` line, a
`control_response`, and anything else claude volunteers. The payload half comes from
`dropcapMakeEntry`, so the base64 arm for invalid UTF-8 is shared rather than re-derived, and
`events_emitted` carries the shipped parser's verdict via `parseOne` as data rather than as a filter.

**Key types the record carries beyond the frames:** the redacted spawn argv, the redacted
`--mcp-config` document text, the three server names in document order, and the `system/init` line's
own `mcp_servers` value verbatim — the cheap list, recorded beside the rich one so #2275 can compare
what the two surfaces say about the same three servers.

### Contracts

- `mcapControlLine(subtype, requestID string, extra map[string]any) ([]byte, error)` — one
  newline-terminated `control_request`, marshalled structured, never concatenated. `extra` carries
  `serverName` / `enabled`; a nil map yields the bare `mcp_status` shape.
- `mcapResponseRequestID(raw []byte) string` — the first non-empty `request_id` across the three
  placements, or empty.
- `mcapAwait(recorder, requestID string, budget, poll time.Duration) (indices []int, terminated string)`
  — polls the recorder's snapshot for replies carrying the id. Budgets are parameters so all exits
  are provable offline in milliseconds.
- `mcapServersFrom(raw []byte) (entries []json.RawMessage, where string, ok bool)` — locates the
  `mcpServers` array at the three placements and names which one held it.
- `mcapServerShape(entry json.RawMessage, red *dropcapRedactor) (mcapServer, error)` — one entry's
  name, status, error text, sorted key set, per-key JSON type and raw config.
- `mcapWriteRecord(dir string, red *dropcapRedactor, scanner dropcapScanner, rec *mcapRecord)
  (recordPath, fixtureReason string, err error)` — **the writer AC 4 is about.** It marshals,
  deny-scans the marshalled bytes and every base64 payload's decoded bytes, and only then touches the
  filesystem. A returned error rather than a `t.Fatalf`, which is the whole reason AC 4 is testable at
  all; the live call site turns the error into a fatal. `fixtureReason` names why a record was not
  promoted, empty when it was.
- `(*mcapRecord) fixtureWorthy() (reason string, ok bool)` — refuses a record that would be a lie as
  the committed proof: any outcome but fired, a `claude_version` whose leading token is not the
  pinned one, a reply that reported zero servers, an entry with an empty key set, a reply frame not
  encoded `json-string` (the reader reads only that), or a reply in which the deliberately broken
  server does not appear at all.

### `internal/streamsup/mcp_status_capture_test.go` (no build tag — inside `make check`)

A third-and-fourth reader in this package, written out rather than generalised from
`compactionCapture` or `capturedLines`, for the reason the first one's docblock gives by name: its
`is_capture` assertion is what stops a hand-built payload file being swapped in behind the provenance
checks, and a path parameter would defeat it.

- `mcpStatusCaptureVersion` / `mcpStatusCapturePath` — the version spliced into the path so the
  filename cannot drift from the release the record vouches for, matching the probe's own constant.
- `mcpStatusPinnedServerKeys []string` — **the measurement this ticket commits:** the sorted union of
  keys claude sent across the reported servers. Empty until the live gate produces the bytes.
- `mcpStatusReaderGate(fixtureExists, pinFilled bool) (action, reason string)` — pure, four
  quadrants, exactly one legal skip (both absent). A landed fixture with an empty pin is a fatal, not
  a pass, which is what makes AC 5's end state reached by construction rather than by remembering.
- The reader re-derives the key union from the reply frame's **own payload bytes** rather than from
  the record's `servers[].keys` field, then cross-checks the record's field against that derivation.
  A reader trusting the record's labels would pin the probe's decoding rather than claude's line.

## Concurrency model

One `streamsup.Runner` goroutine driving the child, joined on a cancel in a `t.Cleanup` with a
bounded wait; one stub-listener accept goroutine, closed and joined in a `t.Cleanup` so an un-joined
goroutine cannot outlive the test under `-race`. The recorder is the shared state and is already
mutex-guarded for exactly this reason: `os/exec` drives `Stdout` from its copier goroutine while the
test goroutine polls `snapshot()`. Nothing else is shared. The waits are poll loops over
`snapshot()`, not channel handshakes, because the recorder's `resultSeen` closes once and cannot mark
three boundaries.

## Error handling

Three outcomes, `fired` / `did-not-fire` / `instrument-broken`, set through one `set` helper so the
outcome and its detail cannot drift apart. The record is written by a cleanup registered before
anything below it can fail, so a structural fatal still leaves the evidence on disk. Every
failure that is about the rig — no child within the spawn wait, `streamsup.New` failing, a stdin
write failing — is `instrument-broken` and names itself in `outcome_detail`; a run in which claude
answers nothing is `did-not-fire`, which is a finding about claude and is recorded as such rather
than retried. The test fails loudly only when the `mcp_status` reply is missing or reports zero
servers, because a capture that recorded no server is vacuous and committing it would hand #2275 a
fixture that proves nothing.

The one hard refusal is the deny-scan: on a hit nothing is written — not the record, not the fixture
— and the message names the class only, never the matched value.

## Testing strategy

Live, one lap, on the dispatcher's `make e2e-realclaude` gate: `TestRealClaude_MCPStatusCapture`,
armed on the fixture's absence.

Offline, under the build tag (`go test -tags e2e_realclaude -run TestMcap ./internal/e2e/realclaude/`,
plus `go vet -tags e2e_realclaude`, which is the only thing that compiles this file at all):

- `mcapControlLine` emits one physical line per request, the bare shape for `mcp_status` and the
  `serverName` / `enabled` fields for the other two, spelled camelCase.
- `mcapResponseRequestID` finds the id at each of the three placements and returns empty on a line
  carrying none.
- `mcapAwait` returns on a matching reply, ignores a reply carrying another id, and ends on its
  budget when no reply arrives — all three in milliseconds against a hand-fed recorder.
- `mcapServersFrom` finds `mcpServers` at each placement and reports absence rather than an empty
  slice when the key is missing.
- `mcapServerShape` classifies each JSON type, including `null` and an empty array, and reports the
  key set exactly as sent rather than as a fixed list.
- **AC 4:** `mcapWriteRecord` handed a record whose `servers[0].config` carries an `sk-ant-`-shaped
  value returns an error and leaves the target directory empty — asserted with `os.ReadDir`, so
  "wrote then deleted" cannot pass. Paired with a control row proving the same record without the
  credential does write, otherwise the refusal could be unconditional and the test still green.
- `fixtureWorthy` refuses each bad capture: not fired, wrong version, unreadable version, zero
  servers, an entry with no keys, a base64 reply frame, and a reply in which the broken server is
  absent.

Offline, inside `make check` (`go test ./internal/streamsup/`):

- `mcpStatusReaderGate` — all four quadrants, on every leg including the one where the fixture does
  not exist and the reader itself can assert nothing.
- The pin test, which skips today on that one legal quadrant and arms the moment the bytes and the
  pin land together.

## Open questions

1. **Does claude answer `mcp_reconnect` and `mcp_toggle` on this input path at all?** The schemas are
   declared, but no reply has been observed here. The probe records a per-request `terminated_on` and
   does not gate the fixture on those two, so a silent verb is recorded rather than fatal.
2. **What does a failed stdio server's `error` text contain?** If it embeds the absent command's
   absolute path, the run-local redaction class must cover it — the class is installed for that path
   up front rather than after the fact, and the deny-scan is the net behind it.
3. **Does `config` come back at all for a stdio server?** The schema's `describe` says "includes URL
   for HTTP/SSE servers", which hints the key may be shaped for remote servers. The record holds
   whatever arrives, including nothing.

Each is resolved by the live lap; any that changes the design gets a `## Revisions` entry.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** One boundary, and it is explicit: claude's stdout crosses into the parent
  through `dropcapRecorder.consume`, which stores raw bytes and decodes only `type`/`subtype`.
  Everything downstream of it — `mcapServersFrom`, `mcapServerShape`, `mcapResponseRequestID` — treats
  the bytes as untrusted JSON, decodes into `json.RawMessage` or `any`, and returns a zero value on a
  decode error rather than panicking. No decoded value from the child is used to make a filesystem or
  process decision; the only thing derived from a reply is what gets written into the record, which
  the deny-scan then screens. `mcapServerShape` is the single place a server entry is interpreted.
- **[Tokens, secrets, credentials]** MUST-FIX-shaped hazard, addressed in the design rather than
  deferred: a stdio server's `config` can carry `command`, `args` and `env`, and `env` is where an API
  key lives. Three defences, in order. By construction, the recorded servers are the rig's own three
  and their environment is not populated by the rig. By table, `dropcapRedactor` substitutes the
  temp `$HOME`, the operator `$HOME`, `$TMPDIR`, the artifact dir, the workdir, the session id and the
  nonce, plus run-local classes installed here for the pyry binary, the config path, the socket path
  and the absent command path. By net, `dropcapScanner` deny-scans the whole marshalled record for
  `sk-ant-`, `/Users/`, `/home/`, `/var/folders/` and `/private/var/folders/`, plus the two credential
  variables' live values read via `os.Getenv` **as needles only** — never stored, never logged. That
  scan runs ahead of every filesystem call, so a refusal leaves nothing half-written, and AC 4's test
  is what proves the ordering rather than asserting it. No token is minted, stored, rotated or
  revoked by this ticket; request ids are correlation values, not secrets, so `crypto/rand` would buy
  nothing and fixed literals keep the record diffable.
- **[Tokens — ordering]** SHOULD FIX, and load-bearing: `newDropcapScanner` reads both credential
  variables through `os.Getenv`, and `WithWorktreeAuthenticated` is what re-pins them into this
  process. Built first, the scanner takes an empty needle, which `scan` reports as `notApplied` —
  silently skipped, never fatal. The credential net would be off while every message read green. The
  scanner is constructed **after** the auth helper, and `credential_scan_skipped` ships in the record
  so a silently-off net is visible after the fact.
- **[File operations]** The `--mcp-config` document is written at 0600 into `t.TempDir()`: not a
  secret, but an execution instruction claude obeys, and a world-writable one in a shared temp
  directory is a footgun. The record and the fixture are written at 0600. Every path is rig-minted;
  no child-supplied value is ever concatenated into a path, so there is no traversal surface and no
  check-then-use gap — the only `os.Stat` is the fixture-absence gate, whose worst case under a swap
  is a re-capture, not a write to an attacker-named path. No symlink is followed for a
  security-sensitive read; `filepath.EvalSymlinks` is used only to enumerate additional redaction
  spellings, which is the fail-safe direction. No atomic rename: a record is evidence, not state, and
  a partially written one is diagnosable where a silently swapped one would not be.
- **[Subprocess execution]** Three subprocesses beyond claude itself, all named by absolute
  rig-minted paths with fixed argv: two forks of the freshly built pyry binary and one path that
  deliberately does not exist. No `sh -c`, no shell interpretation, no child-supplied value in any
  argv. The absent command is the one deliberately hostile input and it is hostile only to claude's
  own launcher, which is the measurement. The child inherits the process environment with `HOME`
  re-pinned to the throwaway worktree by `WithWorktreeAuthenticated`; `os.Environ()` is never read
  into the record. The runner owns the claude child's lifecycle and its cancel-and-join is a
  `t.Cleanup` with a bounded wait, so a wedged child fails the test rather than outliving it.
- **[Cryptographic primitives]** Not applicable, stated rather than skipped: this ticket generates no
  keys, derives nothing, and compares no attacker-controlled value against a secret. The one
  comparison against a credential is `bytes.Contains` inside the deny-scan, and constant-time
  comparison there would defend a secret against a party that already holds it.
- **[Network & I/O]** The stub listener accepts on a Unix socket under a rig-minted short path and
  closes each connection without reading, so there is no unbounded read and no slow-loris surface;
  nothing is expected to dial it. Claude's stdout is bounded twice by the shared recorder: a 4 MiB
  partial-line accumulator and an 8 MiB total, past which **whole** lines are dropped and counted
  rather than truncated. Draining continues past both, because an unconsumed stdout blocks the child.
  Each control-request wait is bounded by its own budget.
- **[Error messages, logs, telemetry]** The deny-scan's failure message names the offending class and
  prints nothing else; an error message that helpfully quotes the leak is the own-goal the scan
  exists to prevent. The success log line carries counts, the outcome and redacted paths only, never
  a payload — putting claude's bytes in CI output is precisely the exposure being guarded. Nothing is
  emitted off-machine.
- **[Concurrency]** One mutex, inside the shared recorder, never held across a call out. Two
  goroutines, each with a `t.Cleanup` that closes its trigger and joins it, so neither can leak past
  the test. No lock ordering question arises because there is one lock. A signal mid-write leaves at
  worst a truncated record in a temp artifact directory, which is evidence about the interruption
  rather than corrupted state anything later reads.
- **[Threat model alignment]** No relay surface and no daemon surface: nothing here listens on the
  network, opens a control socket the daemon uses, or touches `docs/protocol-mobile.md`'s threat
  model. The one threat this ticket genuinely owns is publishing an operator's machine configuration
  in a committed fixture, and cutting the bypass arm is the design decision that answers it —
  `--strict-mcp-config` bounds the recorded server set to the rig's own three. What a bypass
  session's server list should put on the wire is explicitly **out of scope**, deferred to #2275.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-09

## Revisions

### 2026-09-09 — implementation

**The written work is larger than the plan's estimate, and the shape is unchanged.** The plan sized
this at roughly 1300 lines; the two files land at 1845 and 407. Nothing was added beyond the design
above — the difference is comment density, which in this package is the convention rather than a
choice, and the offline table count. Fifty offline assertions run under the build tag with no claude
and no credentials. Recorded because the sizing paragraph above is a measurement other tickets read,
and leaving it at 1300 would make the next capture ticket size against a number no capture has hit.

**`mcapPersist` fails with `t.Errorf`, not `t.Fatalf`.** The plan inherited `ccapWriteRecord`'s
shape, which fatals. That is wrong from inside a `t.Cleanup`: a fatal exits the goroutine mid-cleanup,
so every cleanup registered earlier is skipped — here the stub listener's close-and-join, which would
then leak. By the time the writer runs there is nothing left to abort, so failing the test and
returning is the whole of what a fatal would buy. The writer itself still returns an error and writes
nothing on a deny-scan hit; only the shell around it changed.

**Open question 1 stands, and the design already answered it.** Whether claude serves `mcp_reconnect`
and `mcp_toggle` on this input path is still unmeasured — the live lap resolves it. The probe gates
the fixture on the `mcp_status` reply alone, so a silent verb is recorded in its own
`terminated_on: budget` rather than costing the capture.

**Open questions 2 and 3 are unresolved by construction and stay that way.** Both are about what
claude puts in the reply, which is the measurement. The absent command's path is covered by the
run-local redaction class up front rather than after the fact, so question 2 cannot turn into a
failed write, and `config` is recorded as raw redacted bytes so question 3 records an absence as
faithfully as a presence.
