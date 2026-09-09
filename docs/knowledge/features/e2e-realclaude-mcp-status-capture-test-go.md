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

**Fixture not committed as of this ticket's landing.** `mcpStatusPinnedServerKeys` is still `[]string{}`
and no `testdata/mcp_status_*.json` exists in the tree — read
[`compaction_capture_test.go`](e2e-realclaude-compaction-capture-test-go.md) before assuming this
family's fixtures land automatically once a probe merges. `mcapPersist` writes to both an
`os.MkdirTemp` artifact directory and the in-repo fixture path from the outset (the double-write
[`api_retry_capture_test.go`](e2e-realclaude-api-retry-capture-test-go.md) established), so a
gate-only run that fires clean and loses the in-repo copy still has the artifact-directory record to
recover from.

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
