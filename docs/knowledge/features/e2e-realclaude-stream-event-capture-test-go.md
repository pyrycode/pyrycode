## stream_event_capture_test.go (#2269)

Live capture that spawns claude with `--include-partial-messages` and drives one turn producing
several blocks of assistant text plus one tool call, keeping **every** stdout line of the turn rather
than filtering to a quarry. #2270 writes the `stream_event` parser arm from the committed bytes
rather than from the Agent SDK's type definitions. Production now requests the same flag in
`buildArgs`' fixed prefix for both create and resume spawns. The capture retains its explicit copy in
`streamsup.Config.Args` so its observed-argv assertion remains local to the probe; a current capture
spawn therefore carries the repeatable boolean flag twice.

### Content-block indices are per-message, and a stale map mislabels the second message silently

A `stream_event`'s block index (from `content_block_start`/`content_block_delta`) is scoped to the
message it belongs to, not to the turn. `secapWalkEvent` resolves a delta to the block type recorded
at `content_block_start`, keyed by index in a map that must be **cleared on every `message_start`**.
A walker that keeps one index→type map for the whole turn instead of resetting it per message
silently attributes the second message's block 0 to the first message's block 0 — which is exactly
the shape a text-then-tool-then-text turn produces, since that's two `assistant` messages, not one.
Nothing else in a naive implementation disagrees, so this does not fail loudly; it just mislabels
every delta of the second message and onward. Any future decoder that walks `stream_event` blocks,
\#2270's production arm foremost, needs the same per-message reset.

### A whole-turn capture needs a promotion gate its filtered siblings never needed

Every other capture in this family (`api_retry_capture_test.go`, `tool_progress_capture_test.go`,
etc.) filters to a handful of matching lines and can never reach `dropcapMaxCaptureBytes` (8 MiB),
past which the recorder drops whole lines and only counts them. This probe keeps every line of a
turn under a flag that multiplies line count, so it is the first in the family that can plausibly
hit that cap. A record with a hole mid-delta-run would be unmappable and would read as a complete
capture from outside it — the fixture-promotion gate (`fixtureWorthy`-equivalent) here additionally
refuses to promote any record whose caps dropped a line, a partial, or left an unterminated tail. A
future capture that widens scope from "matching lines" to "every line of a turn" needs the same
cap-drop refusal, not just the existing per-type non-vacuity checks.

### A fixture name built from `claude --version` is a write-path trust boundary, not a tidiness rule

The fixture is named `testdata/stream_event_v<version>.json` from what `probeClaudeVersion` observed
the subprocess print — subprocess-controlled data reaching a file write. The validator (no path
separators, a required leading digit, a length cap, an alphanumeric-plus-`.-` allowlist) is real only
because the write uses **exclusively** the validator's returned path, never the record's raw version
field, and its table test carries an explicit traversal token beside the empty/unreadable/over-long
rows. This surfaced in the security-review pass, not while writing the capture code — a version
string reads as inert data until it is asked to compose a path.

### Known gap: this capture commits `system/init` whole without the messaging-socket redaction class

This is the first probe in the family (after `effort_init_capture_test.go`, #2251) to commit a full
`system/init` line, and it does not call `dropcapRedactor.addPathClass(dropcapClassMessagingSocket,
...)` / `dropcapScanner.addDynamicPath(dropcapClassMessagingSocket, ...)` the way
[`effort_init_capture_test.go`](e2e-realclaude-effort-init-capture-test-go.md) does. The fixed deny
needles (`dropcapScanner`'s two home roots and two var-folders roots) do not cover
`messaging_socket_path`, because claude's socket lives under `/tmp`, which is neither — so the value
would land in the committed fixture silently rather than failing the write. Flagged SHOULD FIX in
code review and not yet fixed as of this ticket's landing. **Before promoting this fixture**, install
the same late-adder pair `effort_init_capture_test.go` uses, verified with the same two-direction
test shape (an un-redacted synthetic line the scanner positively catches, then the redacted form
checked clean with `scanner.applied()` confirming the class actually ran).

### Fixture status as of this ticket

Not committed. This commit lands only the probe and its plan (`stream_event_capture_test.go`,
`docs/specs/architecture/2269-stream-event-capture.md`); nothing under `testdata/` matches
`stream_event_v*.json`. Same shape as
[`compaction_capture_test.go`](e2e-realclaude-compaction-capture-test-go.md),
[`task_notification_capture_test.go`](e2e-realclaude-task-notification-capture-test-go.md) and
[`effort_init_capture_test.go`](e2e-realclaude-effort-init-capture-test-go.md): a promotion commit —
re-run the live capture, close the messaging-socket redaction gap above first, `git add` the
fixture — is still needed before #2270 has committed bytes to read.

### Related

- [`effort_init_capture_test.go`](e2e-realclaude-effort-init-capture-test-go.md) — the messaging-socket
  redaction class this capture needs but doesn't yet install, and the two-direction redaction-test
  shape to copy when it is added.
- [`api_retry_capture_test.go`](e2e-realclaude-api-retry-capture-test-go.md) — the double artifact/
  in-repo write and version-named-fixture-behind-a-globbing-gate shapes this probe also uses.
- [`tool_progress_capture_test.go`](e2e-realclaude-tool-progress-capture-test-go.md) — the
  fixture-absence gate (rather than an env flag) this probe follows.
- `dropped_line_capture_test.go` — no package overview exists for it yet; source of the
  `dropcapRecorder`/`dropcapRedactor`/`dropcapScanner`/`dropcapMaxCaptureBytes` machinery this probe
  composes rather than reimplements.
