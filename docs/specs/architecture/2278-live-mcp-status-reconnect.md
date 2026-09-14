# #2278 — one live `mcp_status` + `mcp_reconnect` round trip through a real claude

## Files read

- `internal/e2e/realclaude/interactive_stream_question_answer_test.go` → `TestInteractiveStreamQuestionAnswer`,
  `raiseRealQuestionBatch`, `drainAnsweredQuestionTurn` — the shape this gate copies: an inbound verb
  from a paired phone, driven through the daemon's own path, asserted to have reached claude.
- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go` → `startStreamModalResolutionHarness`,
  `startObservedPermissionHarness` — the harness. Its `configure func(string) string` parameter is the
  claude-binary substitution hook the permission observer already uses, and the one this gate needs.
- `internal/e2e/realclaude/permission_context_test.go` → `runPermissionObserver`, `startPermissionObserver` —
  the precedent for a `TestMain`-dispatched shim standing in front of the real claude binary.
- `internal/e2e/realclaude/fixtures_test.go` → `TestMain` — the dispatch point; one new env-guarded branch.
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` → `sealEnvelope`, `drainForReply`,
  `perConvHarness` — the correlated request/reply drain. `drainForReply` SKIPS a `TypeError`, so this gate
  needs its own drain (see Design).
- `internal/e2e/realclaude/harness_modal_test.go` → `spawnPermissionDaemon`, `permissionDaemonModel` —
  the daemon fork without `--dangerously-skip-permissions`, i.e. production's downgraded spawn arm.
- `internal/e2e/realclaude/fixtures.go` → `ensurePyryBuilt` — `sync.Once`, so the test and the harness get
  the same built binary path. That binary is what the repair step puts at the broken command's path.
- `cmd/pyry/mcp_config.go` → `permissionArgs`, `renderMCPServersConfig`, `writeMCPServersConfig` — the two
  servers the daemon registers, and `--strict-mcp-config`, which is why no third server can arrive from a
  project or user `.mcp.json`.
- `cmd/pyry/main.go` → `writeMCPServersConfig` call site, `mcpStatusFor`, `resolveBoundMCPStatus` — the
  document is daemon-global and written ONCE at startup, and the status read has no retained-status fallback.
- `internal/streamsup/runner.go` → `mcpStatusEligible`, `actuateMCP`, `ReconnectMCPServer` — **the constraint
  that picks this ticket's design**: eligibility requires `--strict-mcp-config` plus exactly one `--mcp-config`
  whose value equals the daemon's own path. Rewriting the document IN PLACE keeps that; writing a second
  document and swapping the argv would silently make every status read and actuation refuse.
- `cmd/pyry/mcp_actuate_v2.go` → `mcpActuatorV2.actuate`, `boundServerName` — the gate ordering, and why a
  server name the child does not currently report is refused before any actuation.
- `internal/relay/v2session_mcpstatus.go`, `v2session_mcpactuate.go` → `handleMCPStatusRequest`,
  `handleMCPReconnect`, `finishMCPActuation`, `forwardMCPStatusReply` — every answer, accepted or refused,
  is correlated by `Envelope.InReplyTo`; a refusal is a `TypeError`, not a silent drop.
- `internal/relay/v2session.go` → the `TypeMCPStatusRequest` / `TypeMCPReconnect` dispatch arms — both gate
  on `s.interactive`, which `driveHandshakeInteractive` satisfies.
- `internal/protocol/interactive.go` → `MCPStatusPayload`, `MCPServerStatus`, `MCPStatusRequestPayload`,
  `MCPReconnectPayload` — the five wire keys and nothing else.
- `docs/knowledge/features/e2e-realclaude.md` and its
  `e2e-realclaude-mcp-status-capture-test-go.md` section — #2272's measurements: both healthy servers report
  `pending` before they settle to `connected`, the broken entry reports `failed` with a missing-command error,
  and readiness belongs to the control replies rather than to an init event. Also the standing package lesson
  that a live gate's budget constant must carry the reason it is sized as it is.

## Context

Every MCP slice below this one is proven against the fake daemon or against #2272's committed capture. A fake
answers whatever it is written to answer, and a replay is the same bytes whichever request produced them.
Neither can show that a reconnect the daemon relays reaches claude and changes what claude reports. This gate
is the one run that separates a working actuator from one whose reply the daemon fabricated.

No ADR is warranted: this adds a test, no production contract.

## Design

### Where the broken server comes from

`renderMCPServersConfig` emits exactly `pyry_approve` and `pyry_files`, `permissionArgs` passes
`--strict-mcp-config`, and the document is written once at daemon startup. The shipped daemon has no injection
point, and this ticket must not grow one. The route that needs no production change is the one
`startPermissionObserver` already uses: the test binary is handed to the daemon as `-pyry-claude`, and when
the daemon spawns it, it rewrites the `--mcp-config` document the daemon wrote and then becomes the real
claude.

`runMCPConfigShim` (new, in the gate's own file, dispatched from `TestMain` on `PYRY_MCP_SHIM=1`):

1. Refuse (non-zero exit) when `PYRY_MCP_SHIM_REAL` is empty or equal to `os.Args[0]` —
   `runPermissionObserver`'s self-exec guard, for the fork-bomb reason `ensurePyryBuilt` documents. Refuse
   likewise when `PYRY_MCP_SHIM_COMMAND` resolves to `os.Args[0]`: an entry pointing the document at the test
   binary would have `PYRY_MCP_SHIM=1` still in its inherited environment, so claude starting that "MCP
   server" would re-enter this shim and exec another claude — the 2026-05-16 fork bomb in a new costume.
2. Scan `os.Args[1:]` for `--mcp-config <path>` and `--mcp-config=<path>`. Absent ⇒ exec the real binary
   unchanged (a `--version` probe must still work); the gate's own status assertion is what stays non-vacuous.
   Absent `PYRY_MCP_SHIM_COMMAND` ⇒ likewise exec unchanged. The shim is inert unless all three variables are
   set, so it cannot rewrite a document belonging to a daemon nobody meant to instrument.
3. Read the document and rewrite it **as `map[string]json.RawMessage`**, never through a locally-declared
   twin of `mcpServerSpec`. A typed round trip drops any key the local struct does not model, so the day
   `renderMCPServersConfig` grows a field (an `env` block, say) the shim would silently strip it from
   `pyry_approve` and `pyry_files` and the gate would stay green while measuring a degraded document. The two
   existing entries are therefore copied through byte for byte; only the third is synthesised, by decoding the
   `pyry_files` entry, replacing its `command` with `PYRY_MCP_SHIM_COMMAND` and keeping its `args`. Write back
   to **the same path** at 0600. A document already carrying the key is left untouched, so a respawn is
   idempotent and cannot append a duplicate.
4. `syscall.Exec` the real claude with the argv **verbatim** — nothing appended, nothing reordered.
   `mcpStatusEligible` counts `--mcp-config` occurrences and fails closed at two, so an argv this shim
   "helpfully" extended would make every status read and actuation refuse. Exec rather than a wrapper process:
   the pid, the three standard streams and the daemon's signal handling all carry over untouched, and unlike
   `runPermissionObserver` this shim has no reason to read the child's stdout.
5. **The shim writes nothing to stdout, ever** — not a log line, not a diagnostic, not before the exec. It is
   running as the daemon's claude child, whose stdout is exclusively the stream-json the parser reads; a
   stray write corrupts that stream and presents as a parse failure with no connection to its cause. Every
   diagnostic goes to stderr, which the harness already tees. `runMCPFiles` states the same rule for the same
   reason.

**In place, and not a second document.** `mcpStatusEligible` requires `--strict-mcp-config` together with
exactly one `--mcp-config` whose value equals `Config.MCPStatusConfigPath`. Any design that writes a new file
and rewrites the argv makes the child ineligible, and the symptom is every status read and every actuation
refusing with the same merged reject — a red that looks like the feature failing rather than the rig.

**A clone of `pyry_files`, not an invented command.** `runMCPFiles` holds the socket path without dialling it
and serves JSON-RPC on stdio, so a second instance needs nothing from the daemon and starts exactly the way
its twin does. That keeps the initial failure to the one mode #2272 measured (an absent stdio command) and
makes the repair a single filesystem act.

### The run

`startStreamModalResolutionHarness`'s inner `startObservedPermissionHarness(t, model, false, configure)` with
a `configure` that sets the three shim env vars and returns `os.Args[0]`. Model is `permissionDaemonModel`:
this gate drives no tool call, so nothing here needs the larger model the question gates argue for.

1. **One ordinary turn.** `boundServerName` refuses a name the child does not currently report, and the status
   read is a control request written to a live child's stdin — so there must be a child. A tool-free prompt,
   drained through `drainForCompletedTurn`; under the downgraded arm a prompt that touches no tool raises no
   modal.
2. **Poll status to readiness.** Repeat `mcp_status_request` on fresh envelope ids until both `pyry_approve`
   and `pyry_files` report `connected`, bounded by one named budget. #2272 measured both reporting `pending`
   on the first read, so a single read is the wrong assertion.
3. **AC 1.** On that settled frame: the broken row is present, its status is `failed`, and its error is
   non-empty — claude's own text, which the daemon has no way to author. The two healthy rows are asserted
   `connected` beside it, which is what keeps a frame of one row from passing.
4. **Repair.** `os.Symlink` the `ensurePyryBuilt` binary to the broken command's path — cheaper than copying
   ~60MB, and removing the symlink with `t.TempDir()` never touches the target. `os.Stat` the path as absent
   first, so one that was somehow already populated fails loudly rather than quietly measuring two healthy
   twins.
5. **AC 2.** One `mcp_reconnect` for the broken server on a fresh id; drain its correlated frame. The row must
   still be present and must no longer read `failed`. `mcpActuatorV2.actuate` takes this frame as a second
   read AFTER the child acknowledged, never the membership read that preceded it, so a daemon restating held
   state would still say `failed`. Status is asserted as not-`failed` rather than as `connected`: `actuate`'s
   own block records that a server reconnected an instant earlier commonly reports `pending`, and this path
   neither waits nor polls.

### The drain

`drainForReply` correlates on `InReplyTo` but skips a `TypeError`, so a refused status read or a refused
actuation would present as a deadline. This gate adds `drainForMCPStatusReply`, the same in-order frame loop
(decrypt every `noise_msg` to keep the receive nonce in sync, skip non-`noise_msg` control frames without
decrypting), which additionally fails on a `TypeError` correlated to the same id and reports its code — the
difference between "claude never answered" and "the daemon refused us".

Contract: `drainForMCPStatusReply(t, h, reqID uint64, timeout time.Duration) protocol.MCPStatusPayload`.

### Constants, and where the server name comes from

One named budget per wait, each stating the margin it keeps against the production timeout it sits under
(`mcpActuationTimeout` is 30s), per the package's standing lesson. The broken command path is
`filepath.Join(t.TempDir(), …)` — absolute, under a directory that exists, so the failure mode is "this file
is not there" rather than "this whole tree is not there".

**The server name is the test's own compile-time constant at every use**: the key the shim writes, the row the
status assertions look up, and the `server_name` on the `mcp_reconnect` payload. It is never read back off a
status frame and re-sent. Feeding claude's own reported name back as the actuation target would make the
"the row I broke is the row I repaired" claim circular — the test would agree with whatever claude said,
including the wrong row — and it would put a claude-authored string on an actuator, which every doc block on
this path (`MCPServerStatus`, `boundServerName`, `MCPReconnectPayload`) forbids a consumer from doing.

## Concurrency model

No goroutine is spawned by the test. The shim replaces its own image via `syscall.Exec` and starts nothing.
Every wait is bounded by a named budget and every drain by one wall clock; the harness's existing
`t.Cleanup`s own the daemon, the fake relay and the phone.

## Error handling

Every failure is a `t.Fatalf` naming the two or three readings a maintainer must choose between. Claude's
error text and any server name are printed through `%q` and a length cap: they are subprocess-authored bytes,
nothing on this path strips terminal escapes, and the pipeline salvages run logs. The gate skips only when
claude or credentials are absent, via the harness's own `exec.LookPath` and `WithWorktreeAuthenticated`.

## Testing strategy

The gate is its own proof; `make e2e-realclaude` picks it up with no Makefile change. Offline verification is
`go vet ./...`, `go build ./cmd/pyry`, and a tagged build of the package
(`go test -tags e2e_realclaude -run XXX ./internal/e2e/realclaude/`) — `make check` never compiles this
package, so a compile check under the tag is the only thing that can catch a break here. Read the executed-test
count, never the exit code.

Non-vacuity rests on three legs: the broken row must be `failed` with non-empty claude-authored error text
before anything is actuated; both healthy rows must be `connected` in the same frame; and the reconnect's own
post-acknowledgement frame must show that row changed.

## Open questions

1. Does the reconnect's post-acknowledgement read report `pending` or `connected` for the repaired server? The
   assertion is not-`failed`, which holds either way; the observed value gets logged.
2. Does the repaired twin report the same `serverInfo.version` as `pyry_files`? Logged, not asserted — the ACs
   do not ask for it and the value is claude's to decide.

## Security review

**Verdict:** PASS (first pass FAILED on two MUST FIX findings, both revised into the Design above before this
section was written; they are recorded below as the fixes they became rather than silently dropped.)

**Findings:**

- [Trust boundaries] **MUST FIX, fixed in Design → "Constants, and where the server name comes from".** The
  first draft left the reconnect's target name unspecified, and the obvious implementation reads it off the
  status frame. That puts a claude-authored string on an actuator — the thing `MCPServerStatus`,
  `boundServerName` and `MCPReconnectPayload` each separately forbid — and makes the whole proof circular: a
  test that actuates whatever name claude reported agrees with claude about which row it repaired. The name is
  now the test's own constant at all three uses.
- [Trust boundaries] No further findings. The one untrusted-to-trusted crossing this gate adds is claude's
  status text arriving over the phone's wire, and it terminates in assertions and capped `%q` log lines; it is
  never joined into a path, an argv or a subsequent request.
- [File operations] **MUST FIX, fixed in Design → shim step 3.** Round-tripping the `--mcp-config` document
  through a locally-declared twin of `mcpServerSpec` drops every key the twin does not model. Today the shapes
  match, so the bug is invisible; the day `renderMCPServersConfig` grows a field, the shim strips it from both
  production entries and the gate stays green against a degraded document. Existing entries are now copied as
  `json.RawMessage`.
- [File operations] SHOULD FIX, folded into the Design. Write-back mode is stated 0600 (the mode
  `os.CreateTemp` already gave it — `os.WriteFile`'s perm argument does not apply to an existing file, so this
  is a statement of what must remain true, not a mechanism). The repair is `os.Symlink`, whose removal with
  `t.TempDir()` cannot reach the pyry binary it points at. The stat-then-create gap at the repair has no
  attacker: the path is test-authored, absolute, and inside a directory this test owns.
- [Subprocess execution] **MUST FIX, fixed in Design → shim step 5.** The shim runs as the daemon's claude
  child, so anything it prints to stdout lands inside the stream-json the parser reads. The first draft said
  nothing about it, and a debugging `fmt.Println` added later would corrupt the stream with a symptom — a
  parse failure — that points nowhere near its cause. Stdout silence is now an explicit rule, stderr is the
  only diagnostic channel.
- [Subprocess execution] SHOULD FIX, folded into shim steps 1 and 4. Two guards: the document's third entry
  must not resolve to `os.Args[0]` (it would re-enter the shim with `PYRY_MCP_SHIM=1` still set and fork-bomb
  the way 2026-05-16 did), and the argv must be exec'd verbatim (`mcpStatusEligible` fails closed on a second
  `--mcp-config`, turning a "helpful" addition into a total refusal that reads as the feature being broken).
  No `sh -c` anywhere; `syscall.Exec` keeps the pid so the daemon's existing kill path reaches the real claude
  with no double-fork escape.
- [Tokens, secrets, credentials] No findings. This gate mints, stores and asserts no credential: the pairing
  token is the harness's, unchanged and never logged, and the shim inherits `os.Environ()` wholesale exactly
  as `runPermissionObserver` already does — which is what carries the authenticated HOME claude needs. The
  slice writes no fixture, so no deny-scan obligation attaches. The one host-local value that reaches a log is
  the broken command's `/var/folders/…` path, quoted inside claude's own error text and capped.
- [Cryptographic primitives] No findings, but one load-bearing constraint: `drainForMCPStatusReply` must
  decrypt every `noise_msg` in receive order and skip non-`noise_msg` frames without decrypting. Getting it
  wrong desyncs the sequential receive nonce and surfaces as a decrypt failure several frames later, which
  reads as a crypto bug rather than a drain bug.
- [Network & I/O] No findings. Unlike `startPermissionObserver`, this shim opens no socket and the gate binds
  no port — the evidence arrives on the phone's existing wire, so there is no test-owned listener to size,
  time out or cap. The document read is an unbounded `os.ReadFile`, accepted deliberately: the file was
  written seconds earlier by the daemon this test started, and a cap invented here would guard nothing.
- [Error messages, logs, telemetry] No further findings beyond the stdout rule above. Claude-authored strings
  are printed through `%q` and a length cap because nothing on this path strips terminal escapes and the
  pipeline salvages run logs.
- [Concurrency] No findings. The test spawns no goroutine and takes no lock, so it establishes no lock
  ordering; `syscall.Exec` replaces the shim's image rather than supervising anything. The one piece of shared
  mutable state is the mcp-config document, whose only post-startup writer is the shim — and the idempotency
  check in step 3 is what keeps a child respawn from appending a duplicate key.
- [Threat model alignment] OUT OF SCOPE, with its existing owner. The per-device authorization arm
  (`MayAnswerRemotePermission` / `AuthorizeRemotePermission`) is deliberately not driven here: it gates ahead
  of every seam and touches no child, so a live claude behind it cannot change its behaviour, and
  `TestRelayV2_MCPActuationGatedAuditedAndAnsweredFresh` already drives it hermetically with two real phones
  against one real daemon. #1987 declined the same arm for the same reason. This slice adds no wire surface
  and no production code, so it shifts no boundary in the mobile protocol's security model.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-14

## Revisions

### 2026-09-14 — three offline tests for the shim, not in the plan as committed

The Testing strategy above said offline verification was vet, build and a tagged compile, on the reading that
the gate is its own proof. That is wrong for one part of this slice: `mcpShimInjectServer` and
`mcpShimConfigPath` are new logic with branches nothing else exercises, and the security review's first MUST
FIX finding — a typed round trip silently stripping unmodelled keys from the two production entries — is
precisely the kind of defect that leaves the live gate green. It needs a deterministic guard, not a live run.

Added `TestMCPShimConfigPath`, `TestMCPShimInjectServerPreservesUnmodelledKeys` and
`TestMCPShimInjectServerRefusesWithoutTwin`, all credential-free and all passing under the tag. The middle one
was confirmed non-vacuous by removing the unmodelled key from its own fixture and watching the assertion
redden.

### 2026-09-14 — measured size, against the estimate

901 lines landed (258 spec, 637 test, 6 in `TestMain`) against the ~750 this plan was sized at and the
ticket's own ~850 estimate. Both underestimates come from the same place the sizing guidance names: tests were
counted at a sketch's density rather than this package's, and the three offline tests above were not in the
sketch at all. No boundary other than the line ceiling is near its limit — 0 production source files, 0 new
exported types, 0 consumer call sites, 3 acceptance criteria, 0 reject branches — and the run finished well
inside its turn and wall-clock budget, so the overage cost nothing here. Recorded for calibration rather than
as a defect: had it been visible at § A1 the correct response would still have been to build, since splitting
the status arm from the reconnect arm buys a second live claude spawn and a duplicated harness for no extra
deliverable, which is the ticket's own argument.

## Documentation handoff

None. The ticket carries no documentation acceptance criterion, and this slice captures no artefact — nothing
needs committing beyond the test itself. Any durable lesson goes in the PR body for the documentation stage.
