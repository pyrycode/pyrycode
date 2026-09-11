# #2360 — repair MCP probe startup and commit its response capture

Ticket: <https://github.com/pyrycode/pyrycode/issues/2360> (`security-sensitive`,
`needs-real-claude`).

## Files read

- `internal/e2e/realclaude/mcp_status_capture_test.go` →
  `TestRealClaude_MCPStatusCapture`, `mcapAwaitInit`, `mcapPersist`,
  `mcapWriteRecord`, `mcapRecord.fixtureWorthy`, `mcapStatusVerdict`, and
  `TestMcapPersistFillsARecordThatNeverReachedTheHappyPath` — the live driver,
  promotion gate, deny-scanned writer, diagnostics, and persistence regression this
  ticket changes.
- `internal/e2e/realclaude/initialize_control_probe_test.go` →
  `runInitControlChild` — the measured working precedent: its pre-turn arm writes a
  control request immediately after spawn, while `system/init` is driven by a later
  user turn rather than emitted at process startup.
- `internal/streamsup/mcp_status_capture_test.go` → `mcpStatusReaderGate`,
  `TestRealClaudeMCPStatusCaptureServerKeysArePinned`, and
  `mcpStatusPinnedServerKeys` — the hermetic reader and the source pin that must land
  with the live record.
- `docs/knowledge/features/e2e-realclaude-mcp-status-capture-test-go.md` → the
  capture family's current failure evidence, double-write recovery path, deny-scan
  ordering, and the known persistence-test landmine once a fixture exists.
- `docs/knowledge/features/e2e-realclaude-initialize-control-probe-test-go.md` → the
  per-turn `system/init` finding and the pre-turn control-request send-point contract.
- `docs/knowledge/features/development-verification.md` → “Captures and live
  evidence” and “Test execution and artifact survival” — normal-gate reachability,
  version/pin coupling, external recovery, and present-but-unpinned refusal.
- `docs/specs/architecture/2272-mcp-status-capture.md` → the original record,
  correlation, redaction, and fixture-reader design that remains authoritative where
  this repair does not revise it.
- `docs/specs/architecture/2307-mcap-instrument-reports.md` → the diagnostic-fill
  guarantees and security findings that must survive removing the startup wait.

## Context

`TestRealClaude_MCPStatusCapture` currently waits for `system/init` before it writes
any MCP control request. The durable records from repeated gate runs all show the
same state: the child spawned, emitted no stdout or stderr during the silence window,
and received zero requests. `runInitControlChild` and its committed evidence establish
why: this stream-json path emits `system/init` as part of a user turn, not at spawn.
The MCP probe intentionally sends no user turn, so waiting for init is circular.

This ticket repairs the sequencing and lands observed evidence; it does not add a
production decoder or substitute Claude's bundled schema for a live response. No ADR
is warranted because no production contract is chosen here—the fixture records the
external contract that a later ticket will interpret.

Sizing re-check against this plan: one independently checkable deliverable, four
acceptance criteria, zero production source files, zero new exported types, zero
consumer call sites, and no state-machine branch above ten. The expected written work
is approximately 350–500 lines across two test files, one fixture, and this plan,
within the 800-line boundary. The fixture and its pin are two coupled halves of one
measurement and cannot be verified independently.

## Design

### Send the three requests at the pre-turn send point

Extract the existing status/reconnect/toggle loop into a small driver whose contract
accepts the child's stdin, the recorder, and injectable reply timing. It writes the
three request lines immediately, in that order, without inspecting or awaiting
`system/init`. Each line retains its own deterministic request id, exact sent bytes,
write outcome, correlation indices, termination cause, and elapsed wait.

`TestRealClaude_MCPStatusCapture` calls this driver immediately after
`dropcapWaitForChild` and before any init observation. The probe continues to send no
user turn and continues to target only the deliberately broken server for reconnect
and toggle. After the request sequence it records a `system/init` line if one happened
to arrive, but absence is an observation rather than a startup failure. The obsolete
init-wait timing path and its tests are removed so it cannot be reintroduced as a
hidden prerequisite.

`mcapStatusVerdict` classifies from the status request itself: missing request or a
write failure is an instrument fault; a correlated timeout is an explicit unsupported
or unanswered measurement; a correlated reply without a server array is a live-shape
finding. Startup silence alone never selects one of those outcomes.

### Require complete correlated evidence before promotion

`mcapRecord.fixtureWorthy` additionally requires status, reconnect, and toggle each to
have a distinct non-empty id, a successful write, and at least one correlated reply.
It also requires the deliberately broken server entry to retain a non-empty status
and error. A timeout, unsupported-command response that lacks the required measured
shape, partial sequence, or malformed correlation remains in the external diagnostic
record but is not promoted as the committed fixture.

The live record continues to derive each server's sorted key set and JSON value types
from the response's own raw bytes. Existing redaction, credential rejection,
test-owned server inventory, version pin, and no-user-turn boundaries do not move.

### Make persistence safe with an already-committed fixture

Parameterize the writer's fixture destination so offline tests can use a path under
`t.TempDir`, while the live call passes `mcapFixturePath`. Extend the persistence
regression into two states: an absent fixture remains absent after a non-worthy
record, and a pre-existing valid sentinel fixture remains byte-identical. The record
in the artifact directory must still be written and filled in both states.

When the dispatcher gate produces a fixture-worthy record, copy the exact
deny-scanned external `mcap-record.json` bytes to
`internal/e2e/realclaude/testdata/mcp_status_v2.1.259.json` if its worktree copy has
already been discarded. Derive the sorted `servers[].keys` union from that record and
fill `mcpStatusPinnedServerKeys` in the same commit. The streamsup reader then
re-derives the union from the correlated reply bytes and checks the record labels
against it.

## Concurrency model

The existing concurrency remains unchanged: one `streamsup.Runner` goroutine joined
through cancellation, one stub-listener goroutine closed and joined in cleanup, and
the mutex-backed `dropcapRecorder` shared with the test goroutine. The extracted
request driver is synchronous. It polls snapshots through `mcapAwait` and introduces
no goroutine or shared mutable state.

## Error handling

- Request construction or stdin write failure returns a contextual error after the
  request entry records the exact safe outcome; the live caller marks the instrument
  broken and preserves the partial record.
- Each reply wait is bounded independently. A timeout is recorded as data and does
  not prevent later verbs from being sent.
- Incomplete or unsupported reply evidence fails fixture promotion, while the
  deny-scanned artifact record remains available for scope decisions.
- Deny-scan failure still occurs before either artifact or fixture filesystem writes
  and names only the denied class.
- A non-fixture-worthy run never creates, truncates, or overwrites the fixture path.

## Testing strategy

RED first under the `e2e_realclaude` tag:

- An offline driver test starts with an empty recorder and proves all three exact
  requests are written without an init line, in order, with distinct ids and explicit
  timeout outcomes.
- `mcapRecord.fixtureWorthy` rows reject missing, duplicate-id, write-failed,
  timed-out, or uncorrelated verbs and a broken-server entry missing status or error.
- The status-verdict table proves `InitSeen == false` does not override the evidence
  from a request that was actually sent.
- The persistence regression proves both absent-fixture non-creation and existing
  valid-fixture byte preservation while retaining the diagnostic record.

Then run the offline tagged MCP tests and tagged vet to prove the live-only package
without credentials. Run the required touched-scope gate for `internal/streamsup`,
followed by repository vet and the `cmd/pyry` build. The dispatcher alone runs the
credentialed ordinary real-Claude suite; its named result and external artifact are
the evidence for promoting the fixture and pin.

## Open questions

- Whether Claude 2.1.259 supports and correlates all three MCP verbs on this no-turn
  stream-json path remains deliberately unresolved until the repaired live gate runs.
  A timeout or measured unsupported response is retained but not promoted.
- The exact server key union and value types remain deliberately unresolved until the
  live response exists. They must be copied from the deny-scanned record, never from
  the bundled schema.

## Documentation handoff

Pending for the documentation stage: after the fixture lands, update
`docs/knowledge/features/e2e-realclaude-mcp-status-capture-test-go.md`, section
“mcp_status_capture_test.go (#2272)”, to replace the circular-init failure state with
the measured pre-turn request behavior and the committed fixture/key union. No shared
documentation is edited by this builder.

## Security review

**Verdict: PASS.** No MUST FIX or SHOULD FIX finding remains after the review.

### Findings

- **Trust boundaries:** Claude stdout, stderr, and per-server config remain untrusted
  inputs. They still pass through `dropcapRedactor` and `dropcapScanner` before any
  file write. Moving the send point does not add an input surface. The request driver
  emits only fixed rig vocabulary and locally minted ids, with no operator or model
  input interpolated into a command.
- **Tokens, secrets, credentials:** The response fixture may contain free-form error
  text and echoed MCP config. Existing credential needles, fixed path needles,
  redaction census, base64 decoding scan, and fail-closed pre-write ordering remain
  load-bearing. Recovery copies the exact scanned record; it must not reconstruct or
  edit JSON that would bypass the scan. Neither scanner values nor matched values are
  logged.
- **File operations:** The live fixture path remains the fixed repository constant;
  only offline tests inject a `t.TempDir` path. A non-worthy record returns before the
  fixture write, and the regression checks byte identity of an existing sentinel.
  Artifact and fixture modes remain `0600`. No user-controlled path or traversal is
  introduced.
- **Subprocess and command execution:** The child argv and three-server strict MCP
  document are unchanged. No user turn is sent, no tool can be invoked, and reconnect
  and toggle target only the rig-owned broken server. The request writes use the
  already-open child stdin rather than spawning another command.
- **Resource exhaustion and concurrency:** Removing the init wait shortens the
  worst-case run. Three independent reply budgets remain bounded, cancellation and
  joins remain intact, and the new driver is synchronous.
- **Information disclosure:** The capture intentionally retains observed server
  status, error, keys, value types, and raw correlated replies because those are the
  ticket's evidence. The test-owned `--strict-mcp-config` inventory prevents operator
  MCP server disclosure, and promotion remains conditional on the deny-scan.

## Revisions

### 2026-09-11 — reconnect a ready test-owned server

The first dispatcher-owned live run reached all three controls and retained the
broken server's status and error, but correctly refused promotion because reconnect
targeted that broken server and Claude returned a correlated error. The recovery
direction approved on the ticket changes the driver contract: after the first
`mcp_status`, it polls `mcp_status` with distinct request ids until
`mcapApproveServer` reports `connected`, within the existing bounded reply budget,
then reconnects that healthy test-owned server. `mcp_toggle` continues to target the
deliberately broken server, preserving separate diagnostic evidence without
weakening the correlated-success promotion gate.

The offline driver test now scripts a pending-to-connected status transition and
proves reconnect is not sent before readiness. A second test keeps the readiness
state pending through the bounded budget and proves the driver returns diagnostic
failure without attempting reconnect. The additional status polls are retained in
`Requests` with their exact sent lines, outcomes, and unique ids; the last correlated
status response remains the source of the recorded server shapes.

Security posture is unchanged: the readiness target is the fixed rig-owned
`mcapApproveServer`, status replies still pass through the existing redaction and
deny-scan path, the polling budget is bounded, and no user turn or new subprocess is
introduced.

## Revision 2: commit the accepted live evidence

The second live gate produced the usable recording on 2026-09-11 at 18:57 UTC.
It contains two status requests, a successful reconnect to the connected approval
server, and a successful toggle of the deliberately broken server. No user turn
was sent and no init event was observed. The final status reply reports both
healthy servers connected and retains the broken server's failed status and error.

Recover the exact deny-scanned artifact without reserialising it. Commit it with
the measured key union: config, error, name, scope, serverInfo, status, tools.
The reader must correlate the final status request. Reading the first reply was
reproduced as a failure because pending servers lacked serverInfo and tools.
The committed fixture itself covers that transition.

Validate the committed recording through the promotion predicate and fixed
credential scan. Use its bytes in the persistence regression so a diagnostic run
must preserve an actual valid fixture. Update the capture documentation and run
the offline gate plus the final live suite before completing the ticket.

## Revision 3: preserve diagnostics on request failure

The final review identified an early-return gap. A status timeout returned before
the caller recorded that init was not awaited. It also classified unanswered
requests as instrument failures before the status verdict could run.

The live caller now uses mcapDriveAndClassify to record the pre-turn state before
sending. Unanswered status and exhausted readiness remain measured findings.
Failed writes remain instrument failures. The persistence regression drives these
three failure paths and reads their saved records. None may promote a fixture.
