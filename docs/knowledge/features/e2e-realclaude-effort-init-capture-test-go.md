## effort_init_capture_test.go (#2251)

The live capture that settles #2195/#2252's open question: does `system/init` carry an `effort`
key once an effort is actually set? It rides `runSetModeChild`
([`set_permission_mode_probe_test.go`](set-permission-mode-inband-probe.md)) under
production's approval-argv shape (`bypassArgvApprovalArgs`, stub `pyry_approve` socket) with
`--effort low` added to the launch argv, sends `/effort high` as an ordinary in-band turn, and
records the `system/init` line each of the three turns emits.

### The finding: `effort` is absent at 2.1.259 even with an effort demonstrably set

The dispatcher's live gate ran the capture on 2026-09-09 (`claude-sonnet-5`, 2.1.259) and both
non-vacuity witnesses fired: `launch_flag_seen=true` (`--effort low` located adjacent in the
recorded argv) and `acknowledgement_seen=true`, with claude's own assistant text for the `/effort
high` turn reading "Set effort level to high (this session only): Comprehensive implementation
with extensive testing and documentation". All three `system/init` lines — pre-launch, the in-band
turn itself, and post-change — carry the same 22-key sorted set (`agents`, `analytics_disabled`,
`apiKeySource`, `capabilities`, `claude_code_version`, `cwd`, `fast_mode_disabled_reason`,
`fast_mode_state`, `mcp_servers`, `memory_paths`, `messaging_socket_path`, `model`,
`output_style`, `permissionMode`, `plugins`, `product_feedback_disabled`, `session_id`, `skills`,
`slash_commands`, `subtype`, `terminal_slash_commands`, `tools`, `type`, `uuid`) and none of them
is `effort`.

This is the answer the ticket exists to produce, and it is now falsifiable rather than merely
unobserved: earlier, all 33 pre-#2251 committed captures across `claude_code_version` 2.1.143
through 2.1.259 also lacked the key, but none of them had ever set an effort, so the absence
proved nothing. This run set one on both of the daemon's paths — launch argv
(`claudeSettingsArgs` in `internal/sessions/session.go`) and in-band (`Pool.UpdateSettings` in
`internal/sessions/pool.go`) — and the key still never appears. **#2252's design should drop the
`effort` field from `systemInitLine` rather than declare one claude does not send**, per the
ticket's own Technical Notes.

### Fixture status

**CORRECTED 2026-09-10 (#2252): promoted.** The record first existed only in a dispatcher gate log
and was discarded with its detached worktree, the same loss #2229's and #2247's captures suffered
(see [`compaction_capture_test.go`](e2e-realclaude-compaction-capture-test-go.md) and
[`task_notification_capture_test.go`](e2e-realclaude-task-notification-capture-test-go.md)). A
promotion commit (`ddfd5cf4`, followed by a pin-filling commit `c17cbf23`) landed
`testdata/effort_init_v2.1.259_sonnet_effort.json` under `internal/e2e/realclaude/testdata/` and
filled `effortInitPins` from the record's three init lines — all three now read `ClaudeCodeVersion:
"2.1.259"`, `PermissionMode: "default"`, `EffortPresent: false`. #2252 reads these same committed
bytes for its own `SessionFacts` capture assertions rather than the log.

### A nonce of zero is not a degenerate no-op in `dropcapRedactor`

`newDropcapRedactor`'s `nonce int64` parameter is formatted with `strconv.FormatInt`, which spells
`0` as `"0"` — not empty, so the constructor's own empty-value guard on `add` does not catch it.
Passing zero installs `"0"` as a one-byte substitution rule and rewrites every zero digit in the
whole record to `$NONCE`. A probe built around fixed prompts (as this one's first draft was) has no
obvious reason to mint a nonce and is exactly the caller that would reach for `0`. The fix is
structural, not a caller-side check: mint the nonce from `time.Now().UnixNano()` and carry it in
the prompts themselves, so the value the table substitutes is a value the record actually contains.

### A redaction applied field by field leaks whatever field its author forgot

This record retains claude's verbatim stdout, so `effortInitPass.screen` runs the redactor and the
deny-scan over the **marshalled bytes**, not per-field like the sibling `initControlFixtureRecord`
pass (see
[`initialize_control_redaction_test.go`](e2e-realclaude-initialize-control-redaction-test-go.md)).
Screening the whole marshal covers every field by construction, including the acknowledgement text
(model-composed) and the summariser's copied-verbatim key names — both would be easy to miss in a
field-by-field pass. The trade this makes: a substitution-**count** table describing the pass
cannot itself live inside the bytes the pass just redacted (writing it before the marshal would
commit a table of zeroes), so unlike `initControlFixtureRecord`'s `Redaction` census, this family's
counts are logged rather than committed; only `credential_scan_applied` (a bool, decided before the
marshal) ships in the record.

### A one-direction redaction test is vacuous

`dropcapScanner` skips a dynamic needle shorter than `dropcapMinNeedle` (16 bytes) and reports it
as **not-applied** rather than failing — the same non-fatal skip that makes an env-gated probe or
an empty credential needle silently pass with the net switched off. A test that only scans the
*redacted* bytes and finds them clean proves nothing on its own: it would pass identically whether
the redaction ran or the needle was simply too short to register.
`TestEffortInitRedaction_RemovesTheMessagingSocketPathTheScannerWouldCatch` asserts both
directions on the same synthetic `system/init` line — the **un-redacted** form is one the scanner
positively catches, and only then is the **redacted** form checked clean, with `scanner.applied()`
confirming the messaging-socket class actually ran rather than having been skipped as too short.

### `messaging_socket_path` and `session_id` cannot be constructor parameters

Neither value is knowable when `newDropcapRedactor` is called: this rig spawns `claude` directly
rather than through `streamsup.Config`'s `SessionID`, and claude's own socket path
(`/tmp/cc-socks/<pid>.sock`) is first legible in the very `system/init` bytes being redacted. None
of the constructor's nine existing call sites could have supplied either value at construction
time. `dropcapRedactor` gained two late adders instead of two more constructor parameters —
`addValueClass(class, replacement, value)` and `addPathClass(class, replacement, path)` (the
latter over every `dropcapPathSpellings` variant) — both calling a new shared `resort()` that
lifts the constructor's longest-value-first sort out to a single home. `dropcapClassMessagingSocket`
is the one new class name this ticket adds; `session_id` reused the existing class via
`addValueClass`. No existing call site changed.

**Out of scope, named by the ticket itself:** 19 already-committed capture files carry an
un-redacted `messaging_socket_path` today. This capture's own bytes are covered by the new class;
re-redacting the existing 19 (and re-deriving every pin that reads them) needs its own ticket.

### A promotable two-line record does not prove the in-band change landed

`effortInitPromotable` only requires `len(lines) >= 2` to allow a fixture write, but AC 1's "a
later one after an in-band `/effort`" is answered by the **third** init line — the second is
emitted when the `/effort` turn *starts*, not after claude has processed it. A two-line record can
satisfy the current gate while still comparing "before" against "mid-change" rather than
"before" against "after". Not yet closed as of this ticket landing (code review flagged it as a
NIT, not a blocker); the live run in fact produced three lines, so the gap has not caused a bad
promotion, but a future promotion attempt with a truncated run could hit it. Requiring three lines,
or binding each pin to its turn index rather than just its position, would close it.

### Related

- [`compaction_capture_test.go`](e2e-realclaude-compaction-capture-test-go.md) — the fixture-lost-after-a-green-gate pattern this capture repeated, and the recovery shape (an opportunistic promotion commit) to reuse rather than reinvent.
- [`task_notification_capture_test.go`](e2e-realclaude-task-notification-capture-test-go.md) — the same "prose describing the deny-scan can trip it" and "one-direction test is vacuous" family of lessons, discovered independently on a different capture.
- [`initialize_control_redaction_test.go`](e2e-realclaude-initialize-control-redaction-test-go.md) — the sibling redaction pass that redacts field-by-field instead of by marshal, and why that choice is right there and wrong here.
- [`interactive_stream_inband_model_test.go`](e2e-realclaude-interactive-stream-inband-model-test-go.md) — the 2026-08-19, single-version, in-band-only negative this capture re-measured and confirmed at 2.1.259 on both paths.
- `systemInitLine` (`internal/streamsup/parser.go`) and #2252 — the consumer this finding is for: design the field mapping without an `effort` key.
