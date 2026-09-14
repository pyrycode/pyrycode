## mcp_status_reconnect_test.go (#2278)

`TestRealClaudeMCPStatusAndReconnect`: one inbound `mcp_status_request` and one inbound
`mcp_reconnect` from a paired phone, driven through the daemon's own path to a live claude,
asserting the reconnect reached claude and changed what claude reports. Every MCP slice below
this one is proven against the fake daemon or against
[`mcp_status_capture_test.go`](e2e-realclaude-mcp-status-capture-test-go.md)'s (#2272) committed
capture — a fake answers whatever it is written to answer, and a replay is the same bytes
whichever request produced them. Only a live run can show the daemon actually relays a reconnect
rather than fabricating the reply; the load-bearing assertion is the changed row, since a frame
restated from anything the daemon already held would still report the broken server as `failed`.

Drives no per-device authorization arm and no `mcp_toggle`: the authorization gate
(`MayAnswerRemotePermission`) runs ahead of every seam and touches no child, so a live claude
behind it can't change its behaviour — `TestRelayV2_MCPActuationGatedAuditedAndAnsweredFresh`
already covers it hermetically, and #1987 declined the same arm for the same reason. `mcp_toggle`
is #2272's to cover.

### The broken server arrives via a claude-binary swap, not a daemon seam

`renderMCPServersConfig` emits exactly `pyry_approve` and `pyry_files`, `permissionArgs` passes
`--strict-mcp-config`, and the document is daemon-global, written once at startup. The shipped
daemon has no injection point, and this gate does not add one — a test-only seam on the
permission bridge's spawn path would be a production change made for a test's benefit. Instead
the test binary is handed to the daemon as `-pyry-claude` (`startPermissionObserver`'s existing
route), and when the daemon spawns it, `runMCPConfigShim` rewrites the `--mcp-config` document the
daemon wrote and then `syscall.Exec`s the real claude. Three environment variables gate the
rewrite (`PYRY_MCP_SHIM`, `PYRY_MCP_SHIM_REAL`, `PYRY_MCP_SHIM_COMMAND`) — all three must be set,
so the shim is inert against a daemon nobody meant to instrument.

### The rewrite must be in place, never a second document

`mcpStatusEligible` requires `--strict-mcp-config` together with **exactly one** `--mcp-config`
whose value equals the daemon's own `Config.MCPStatusConfigPath`. A rig that writes its own
document and rewrites the argv to point at it makes the child ineligible, and every status read
and every actuation then refuses with the same merged reject — a red that reads as the feature
being broken rather than the rig being wrong. The corollary is that nothing may be appended to the
argv either: a second `--mcp-config` flag fails the same count check closed. `mcpShimConfigPath`
locates the daemon's own path from `os.Args[1:]` and `mcpShimInjectServer` rewrites that file in
place; the shim execs the argv verbatim.

### A typed round trip through a locally-declared twin is silently lossy

The daemon's `mcpServers` map, decoded into a local `{command, args}`-shaped struct, drops any key
that struct does not model. The shapes match today, so the loss is invisible — but the day
`renderMCPServersConfig` grows a field (an `env` block, say), a shim built that way would strip it
from both `pyry_approve` and `pyry_files` while rewriting the document, and the live gate would
stay green against a document it had quietly degraded. `mcpShimInjectServer` instead decodes the
document as `map[string]json.RawMessage` and copies `pyry_approve`/`pyry_files` through byte for
byte, synthesising only the injected third entry by cloning `pyry_files`'s raw bytes and replacing
its `command` key. `TestMCPShimInjectServerPreservesUnmodelledKeys` pins this — confirmed
non-vacuous by removing an unmodelled key from its own fixture and watching the assertion redden.

### A shim standing in front of claude must never write to stdout

The shim runs as the daemon's claude child; its stdout is exclusively the stream-json the parser
reads. A stray write — even a debugging `fmt.Println` added later — corrupts that stream and
surfaces as a parse failure with no visible connection to its cause. Every diagnostic in
`runMCPConfigShim` goes to stderr, which the harness already tees. `pyry mcp-files`
([`pyry-mcp-files-command.md`](pyry-mcp-files-command.md)) states the same rule for the same
reason; it generalizes to any process substituted into claude's spawn slot, not just MCP servers.

### An injected command pointed at the shim's own binary re-enters the shim

If the injected entry's `command` resolved to `os.Args[0]`, claude starting that "MCP server"
would inherit `PYRY_MCP_SHIM=1` from the environment and re-enter the shim, which would exec
another claude — the 2026-05-16 fork bomb in a new costume. `ensurePyryBuilt` already carries this
guard for the real-binary variable; `mcpShimInjectServer` carries the equivalent guard on the
*injected* command, refusing when it would resolve to the shim's own binary.
`TestMCPShimInjectServerRefusesWithoutTwin` and `TestMCPShimConfigPath` cover the shim's pure
logic offline, with no claude and no credentials.

### `drainForReply` skips a correlated `TypeError` — a refusal needs its own drain

Every MCP answer, accepted or refused, rides `Envelope.InReplyTo`. The package's general
`drainForReply` helper (used by the question/answer and modal-resolution families) skips a
correlated `TypeError`, so a refused status read or a refused actuation would present as a
deadline — "claude never answered" — when the daemon actually refused before touching any child.
This gate adds `drainForMCPStatusReply`, the same in-order, nonce-preserving frame loop (every
`noise_msg` decrypted in receive order, non-`noise_msg` control frames skipped without
decrypting), which additionally fails on a `TypeError` correlated to the same request id and
reports its code — the difference between "claude never answered" and "the daemon refused us".
Any future verb whose refusal is a correlated error envelope needs the same specialised drain, not
the generic one.

### The actuation target is the test's own constant, never read back off a frame

`mcpReconnectBrokenServer` is used at all three points that matter — the key the shim writes, the
row the status assertions look up, and the `server_name` the `mcp_reconnect` payload names — and
it is never read back off a status frame and re-sent. Feeding claude's own reported name back as
the actuation target would make the whole proof circular (the test would agree with whatever
claude said, including the wrong row), and would put a claude-authored string on an actuator —
which `MCPServerStatus`, `boundServerName` and `MCPReconnectPayload` each separately forbid a
consumer from doing.

### Known gap: no control read between the repair and the reconnect

Code review (PASS, not blocking) flagged that the sequence — broken row reads `failed` → the test
symlinks the built `pyry` binary into place → `mcp_reconnect` → assert the row is no longer
`failed` — excludes a restated-frame explanation but not "claude re-probes a failed server on any
`mcp_status` read once its command exists," under which the row could flip with the actuator doing
nothing. #2272's capture does not close this either: its two status reads 0.25s apart both
reported the broken entry `failed`, but its command was still absent in both, which only shows
claude does not spontaneously fix an entry that is *still* broken. Closing this fully needs one
more correlated status read after the repair and before the reconnect, asserting the row is
*still* `failed` at that point — not shipped in this ticket, recorded here for whoever next
touches this file.

### Related

- [`mcp_status_capture_test.go`](e2e-realclaude-mcp-status-capture-test-go.md) (#2272) — the
  committed capture this gate's readiness-polling shape and status vocabulary
  (`pending`/`connected`/`failed`) are drawn from, and the sibling this gate cannot substitute for
  since a replay is the same bytes whichever request produced them.
- [`pyry-mcp-files-command.md`](pyry-mcp-files-command.md) — `pyry mcp-files`, the production
  server this ticket's injected entry clones, and the stdout-silence rule stated for the same
  reason.
- [`pyry-mcp-approve-command.md`](pyry-mcp-approve-command.md) — `renderMCPServersConfig` and
  `--strict-mcp-config`, the document this gate rewrites in place and the flag that makes the
  eligibility count matter.
- [`interactive_stream_question_answer_test.go`](e2e-realclaude-interactive-stream-question-answer-test-go.md) —
  the shape this gate's harness use is copied from: an inbound verb from a paired phone, driven
  through the daemon's own path, asserted to have reached claude.
