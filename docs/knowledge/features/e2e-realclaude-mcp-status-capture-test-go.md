## mcp_status_capture_test.go (#2272)

Live capture of `mcp_status`, `mcp_reconnect` and `mcp_toggle` on production's **downgraded** spawn
arm only (`--mcp-config`/`--strict-mcp-config`, no `--dangerously-skip-permissions`): a three-server
document (`pyry_approve`, `pyry_files`, and one entry whose command does not exist) sent on held-open
stdin, no user turn ever written. The bypass arm was cut on purpose — with no `--mcp-config` on that
arm there is no document to put a broken server in, and `mcp_status`'s `config` field would otherwise
publish the operator's own machine inventory (command/argv/env) into a committed fixture. What a
bypass session should report is left to #2275. Its hermetic sibling in `internal/streamsup`
(`mcpStatusReaderGate`) pins the observed per-server key union inside `make check`, the same
fixture-exists/pin-filled state machine as
[`compaction_capture_test.go`](e2e-realclaude-compaction-capture-test-go.md).

**Fixture committed by #2360.** The Claude 2.1.259 recording from 2026-09-11 lives
at `internal/e2e/realclaude/testdata/mcp_status_v2.1.259.json`. The reader pins the
measured union `config, error, name, scope, serverInfo, status, tools`. Each server
also retains its own key set and JSON value types. The union describes all observed
keys across servers, rather than fields guaranteed on every server.

The probe sends status before any user turn or init event. It polls status with
unique request ids until the test-owned approval server reports connected, then
reconnects that server. Toggle still targets the deliberately broken server.
The accepted recording contains two status replies followed by successful
reconnect and toggle replies. Both healthy servers were connected in the final
status reply. The broken server reported failed status and a missing-command error.
No init event appeared during this exchange.

The reader correlates the final status request. The first reply still reported
pending servers and lacked `serverInfo` and `tools`. Selecting it fails against
the committed key union. The fixture itself preserves this regression case.
`TestMcapCommittedFixtureIsUsable` checks the accepted recording through the
promotion predicate and the fixed credential scan without a live login.

`mcapPersist` writes the same scanned bytes to a durable artifact directory and
the repository fixture path. Recover the exact external record if the gate's
checkout has been discarded. Commit it with the matching reader pin.

### A shipped binary's own bundled schema outranks `sdk.d.ts`, and is still not the wire

`sdk.d.ts` declares a per-server status object as `{name, status, serverInfo?, error?, config?,
scope?}`. Reading claude 2.1.259's own bundled zod schemas (via `strings` over the installed binary)
found two fields missing from that list, `tools` and `capabilities`, and settled the three request
shapes' field spelling as camelCase (`serverName`, not `server_name`) before the probe ever ran. A
guessed spelling would have recorded claude's complaint about a malformed request and looked
identical to a capture of its answer. The schema read is a stronger source than a type declaration,
but it is still a declaration, not a measurement — the reason this probe exists at all is that
`sdk.d.ts` has already been wrong once for this repo (the `initialize` control line's promised fields
that no committed capture carries), and a bundled schema could drift from what a given claude version
actually emits the same way.

### `t.Fatalf` inside a `t.Cleanup` skips every cleanup registered earlier

The record writer runs from a `t.Cleanup` (so evidence survives a structural failure anywhere above
it). The plan inherited `ccapWriteRecord`'s `t.Fatalf`-on-error shape from the compaction family, but
a fatal inside a cleanup exits that goroutine immediately, skipping every cleanup registered before
it — here, the stub MCP listener's close-and-join, which would then leak. By the time the writer runs
there is nothing left to abort, so `mcapPersist` uses `t.Errorf` and returns; failing the test is the
whole of what a fatal would have bought. Any future capture whose record writer sits in a `t.Cleanup`
behind other cleanups needs the same substitution, not the older family's fatal.

### A writer that fatals cannot be asked what it left on disk

`mcapWriteRecord` returns `(path, fixtureReason, error)` rather than calling `t.Fatalf` itself, which
is what makes AC 4 — a credential-shaped `config` value refuses the write and leaves nothing on
disk — assertable at all: `os.ReadDir` on the target directory after a non-nil error is the only way
to rule out "wrote it, then deleted it," and a self-fataling writer never returns control to check.

### The run-local redaction class is load-bearing here too, not hygiene

`ensurePyryBuilt` builds the test binary into `os.MkdirTemp`, so the redacted `--mcp-config` document
and argv both name a `/var/folders/` path — one of `dropcapFixedNeedles`' five fixed deny-scan
literals. Skipping that redaction class doesn't merely leak a build path into the fixture; it fails
the whole write, since the scan runs ahead of every filesystem call. Same lesson as
[`effort_init_capture_test.go`](e2e-realclaude-effort-init-capture-test-go.md)'s run-local class, now
confirmed on a second, independently-built spawn.

### A record field can ship unscanned if it's re-marshalled after the deny-scan

Code review flagged that `mcapWriteRecord` re-marshals the record (to attach `credential_scan_skipped`)
after the deny-scan runs, so that field's bytes reach disk without ever passing the scanner. Safe as
written — `dropcapScanner.scan` reports only a fixed-constant class name, never a dynamic value — but
the writer's entire contract is "the scan precedes every filesystem call," and this is the one field
that technically doesn't go through it. Any future edit that puts a non-constant value in a
post-scan-marshalled field would defeat that contract silently.

### A record's zero values and a genuine empty measurement can be the same bytes

`TestRealClaude_MCPStatusCapture` used to return the moment `mcapAwaitInit` reported the init
line missing, and the snapshot, census and frame collection all sat below that return — so every
`instrument-broken` record read `lines_captured: 0`, `frames: []`, `line_type_census: null`, and
those looked exactly like a claude that printed nothing. They were the constructor's zero values;
the pass that would have measured anything never ran. Seven gate runs on 2026-09-09 shipped that
ambiguity and it fit two opposite causes — a silent claude, or one printing lines that were never
`system/init` — equally well.

`mcapFillCapture` is now the only place that assigns `LinesCaptured`, the cap counters,
`LineTypeCensus`, `UndecodedLines`, `Frames` and `StderrCapture`, and `mcapPersist` calls it before
`mcapWriteRecord` on every terminating path, not just the happy one. That alone isn't what makes a
broken run readable: `mcapCensus` and `mcapCollect` return non-nil empty results, and none of
these fields carries `omitempty`, so a pass that ran and saw nothing writes `{}`/`[]` while a
record that was never filled still writes `null`. A field added to this group later that keeps
that non-nil-empty return but picks up `omitempty` would quietly re-collapse the distinction this
ticket exists to create.

### Redact a free-text field before capping it, not after

`mcapFillCapture` runs `red.str` over the child's stderr before `capFixtureCapture` bounds it at
`stderrFixtureCap`, and the order is load-bearing rather than stylistic. Capping first can slice a
`/var/folders/...` build path mid-string; the leftover fragment stops matching the redactor's
substitution rule (built for the whole path) while it still trips `dropcapFixedNeedles`' fixed
`/var/folders/` literal, so the deny-scan refuses the entire write over a truncation artifact
rather than anything actually captured. `TestMcapStderrIsRedactedBeforeItIsCapped` pins the
ordering with a path positioned to trip exactly that failure if the two calls are swapped.

### Readiness belongs to the control replies

The old startup wait required an init event before sending any request. That event
is emitted per user turn, and this probe sends no user turn. The wait was circular.
The repaired driver sends status immediately and retains every request, reply,
write error and timeout. A bounded wait for the approval server to report connected
makes reconnect exercise a healthy server. Reconnecting the deliberately broken
server produced an error that correctly failed the successful-reply requirement.

### A diagnostic run must preserve an existing valid fixture

`TestMcapPersistFillsARecordThatNeverReachedTheHappyPath` gives the writer temporary
fixture paths. It tests both an absent file and a copy of the valid committed
recording. A non-worthy record must leave the former absent and the latter
byte-identical. Both cases still require the separate diagnostic record to be
written. The older assertion that the repository fixture must be absent was
removed by #2360.

### Related

- [`compaction_capture_test.go`](e2e-realclaude-compaction-capture-test-go.md) — the
  fixture-exists/pin-filled reader shape this probe's `internal/streamsup` sibling copies, and the
  record of how a gate-only run's fixture gets lost and later recovered.
- [`api_retry_capture_test.go`](e2e-realclaude-api-retry-capture-test-go.md) — the double-write
  (artifact directory + in-repo path) pattern this probe used from the outset.
- [`effort_init_capture_test.go`](e2e-realclaude-effort-init-capture-test-go.md) — the run-local
  redaction class pattern (`addPathClass`) for values only the rig's own build step can supply.
- [`initialize_control_probe_test.go`](e2e-realclaude-initialize-control-probe-test-go.md) — the
  three-placement `request_id`/payload nesting this probe's correlation logic reuses.
