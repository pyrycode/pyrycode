# 2320 — realclaude: prove a hook-blocked prompt's banner reaches the client on the live path

Test-only. No production file is created or modified.

## Files read

- `internal/e2e/realclaude/interactive_stream_announced_reset_test.go` → `resetWindow`, `resetWindow.next`, `awaitTransition`, `awaitCompletedTurn` — the window primitive this ticket copies, and the header stating why a turn that cannot satisfy the shared drain gets one local primitive every frame passes through.
- `internal/e2e/realclaude/harness_daemon_test.go` → `spawnBootstrapDaemon`, `sealSendMessage`, `driveHandshakeInteractive`, `bootstrapDaemon` — the daemon spawn whose pass-through argv this ticket extends, and the wire helpers reused unchanged.
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` → `startPerConversationHarnessSeeded`, `startPerConversationHarness`, `perConvHarness`, `createConversationViaPhone` — the live spine, its seed callback, and the over-the-wire conversation create.
- `internal/e2e/realclaude/operator_system_lines_capture_test.go` → `oslcapWriteHookRig`, `oslcapBlockPrompt`, `oslcapLivenessPrompt`, `oslcapHookVerdicts`, `oslcapBlockReason`, `oslcapRig` — the four reusable trigger helpers plus the reason string the banner must carry. `oslcapArgs` is read and deliberately NOT reused: it builds the capture's own direct `streamsup` spawn.
- `internal/e2e/realclaude/interactive_system_prompt_test.go` → `sysPromptSpawnRecords` — the reader that pulls the daemon's own `spawning claude` records out of captured stderr, proving which argv the daemon handed the child.
- `internal/e2e/realclaude/interactive_stream_model_announced_test.go` → `spawnBootstrapDaemonVerbose` — its header argues AGAINST a variadic on the shared spawner. See Design; this ticket's AC 3 overrides it, and the change is confined to a trailing append so the empty case is byte-identical.
- `internal/streamsup/parser.go` → `emitInformationalBanner`, `systemInformationalLine`, `maxBannerText`, `maxBannerLevel` — the producer. Load-bearing for the failure readings: an informational line that fails the decode is dropped with a Debug line and emits NOTHING, so a changed shape shows up as silence, never as an `unrecognized_message`.
- `internal/turnbridge/outbound.go` → the `turnevent.Banner` arm — all four claude-side values cross verbatim, `Truncated` is not recomputed.
- `internal/protocol/interactive.go` → `BannerPayload` — the four wire fields to assert against.
- `internal/sessions/pool.go` → `Pool.New`'s and `buildSession`'s argv composition — **the source of this ticket's one real risk.** Both sites append the daemon's own `--settings <mcp file>` AFTER the operator's pass-through args.
- `internal/sessions/settings.go` → `writeMCPSettings`, `mcpSettingsFile` — that file declares two booleans and no hooks, so the two settings files conflict on no key.
- `docs/protocol-mobile.md` § `banner` — the field table, and the rule that `level` is an open set a test must not pin to one value.
- `docs/knowledge/features/e2e-realclaude-operator-system-lines-capture-test-go.md`, `.../e2e-realclaude-interactive-stream-announced-reset-test-go.md` — the family's prior lessons.
- `internal/e2e/realclaude/testdata/operator_system_lines_v2.1.259.json` — the committed capture. Its `block` phase holds exactly three lines (`system/init`, the informational line, `result/success`) and `child_alive_after_block` is true, which is what makes a turn boundary available to wait on.

## Context

`system/informational` carries a `UserPromptSubmit` hook's block reason. #2319 shipped the parser mapping onto `turnevent.Banner` and replayed it against the committed capture; #2256 shipped the wire frame. Both are provable inside `make check`.

Neither proves the frame reaches a connected client on a live turn. The capture was recorded through the capture rig's own direct `streamsup` spawn, and the replay drives the parser directly. This ticket adds the live rung: the daemon in the path, a real claude, a real hook refusal, a real client.

No production change is expected. If the banner does not arrive, that is the finding.

This work deserves no ADR: it adds a rung to an existing proof ladder and decides nothing new about the system.

## Design

### The harness tail (AC 3)

Two helpers gain a way to carry extra pass-through claude arguments.

`spawnBootstrapDaemon` gains a variadic tail:

```go
func spawnBootstrapDaemon(t *testing.T, home, workdir, claudeBin, relayURL string,
    extraClaudeArgs ...string) *bootstrapDaemon
```

The values are appended to the existing `args` slice after `--dangerously-skip-permissions`, and nowhere else. With no extra arguments the argv is byte-identical to today's, so all nine existing call sites are unchanged in text and in behaviour.

`spawnBootstrapDaemonVerbose`'s header argues against exactly this, on the ground that `make check` never compiles this package so a restructure error surfaces only as failing live runs. That reasoning is respected rather than overridden: the edit is a trailing append, not a restructure of the argv assembly, and § Testing strategy adds the tag-enabled compile that closes the gap the header names.

`startPerConversationHarnessSeeded`'s seed callback gains a return value:

```go
func startPerConversationHarnessSeeded(t *testing.T,
    seed func(home, workdir string) []string) *perConvHarness
```

The returned slice is forwarded to that helper's own `spawnBootstrapDaemon` call and to no other. A return rather than a second parameter is forced: the settings path lives under a HOME the helper mints for itself, so it cannot be known at the call. `startPerConversationHarness` passes `nil` and needs no edit. The one existing seeded caller, in `interactive_conversation_system_prompt_test.go`, gains a `[]string` result and a `return nil`; the arguments it passes today are unchanged.

### The test

One file, `internal/e2e/realclaude/interactive_stream_hook_blocked_banner_test.go`, tag `e2e_realclaude`.

The seed callback writes the hook rig with `oslcapWriteHookRig` into a directory under the minted HOME whose name is this ticket's own, and returns `[]string{"--settings", rig.SettingsPath}`. The rig is reused whole rather than re-derived: it already refuses to interpolate a shell metacharacter, sets explicit modes, blocks only a marked prompt, and writes one fixed word per invocation and never the payload.

The rig error is `t.Fatalf`'d, never dropped. That is load-bearing rather than housekeeping: `oslcapShellQuote` REFUSES a value carrying a single quote instead of sanitising it, so on a `TMPDIR` holding one the returned rig is half-built. Continuing past that error would hand claude a settings file naming a script whose shell source was never written.

The conversation is created over the wire with `createConversationViaPhone`, because the seeded harness seeds no bound conversation. Since #2085 the first message is what brings that conversation's claude up, so the blocked prompt is also the cold spawn.

Turn 1 sends `oslcapBlockPrompt(nonce)`. Turn 2 sends `oslcapLivenessPrompt(nonce)`, which carries no marker and so passes the hook.

### The window primitive

`drainForCompletedTurn` requires a non-empty `assistant_delta` before a terminal idle, and a hook-blocked turn carries no assistant line at all, so it would Fatal at the first milestone. `drainForControlEvent` silently consumes every frame before the one it returns, which makes a negative unassertable after the fact.

So one local type, `hookBannerWindow`, with a `next` method every frame between the blocked send and turn 2's terminal frame passes through. It applies the window-wide negatives, collects every `banner` payload, and hands the envelope back to the caller's own state machine. Three thin waits sit over it:

- `awaitBanner(convID, timeout) protocol.BannerPayload` — reads forward to the first `banner`.
- `awaitTurnIdle(convID, timeout)` — reads forward to the blocked turn's terminal `turn_state{idle}`.
- `awaitCompletedTurn(convID, reqID, timeout)` — ack, then a non-empty `assistant_delta`, then the terminal idle, ordered as in `resetWindow.awaitCompletedTurn`.

Turn 2 is sent on turn 1's terminal idle, NOT on the banner. That is the one place this file departs from #2138, which had to send on the transition because whether its turn ever closed was the unknown under test. Here the committed capture's block phase terminated on a `result`, so the boundary exists and waiting for it is the ordinary shape.

### Assertions

On the banner: `conversation_id` equals the driving conversation; `text` contains `oslcapBlockReason`; `truncated` is false, since the observed line is far under 4 KiB. `level` and `stops_turn` are logged, not pinned — `level` is a documented open set, and pinning `stops_turn` would assert claude's choice rather than the daemon's carriage.

**Every log site that touches claude-authored text uses `%q`, never `%s`.** This is a rule of the file, not a preference at one site, and it is the security pass's one MUST FIX (see § Security review). `text` is claude-composed prose that the frame's own doc says may carry newlines, terminal escapes, markup and text impersonating daemon chrome inside its 4 KiB. `t.Logf` writes to the operator's terminal, so this test IS a render boundary in the sense § Security model's threat 1 means, and `%q` renders every control byte as an escape rather than executing it. The same rule covers `level`, the extracted spawn records, and the hook verdicts.

On the success path the capture line carries a SUMMARY — level, byte length, `truncated`, `stops_turn`, and that the reason substring was found — rather than the prose itself; the full `%q` text is logged only where an assertion fails and a human is already reading. The frame is the family's first live observation of a banner, and nothing is written to disk: a committed capture would need this package's redaction apparatus for claude-authored bytes produced under the operator's real credentials, which is #2138's stated answer for the same question.

Exactly one banner crossed the window is asserted at the end, over every frame rather than over whatever a later read had not consumed.

Two witnesses make a red run conclusive rather than merely red:

1. `oslcapHookVerdicts(rig.WitnessPath)` — whether the hook ran at all, and with which verdicts. Expected `["blocked", "passed"]`.
2. `sysPromptSpawnRecords(h.daemon.stderr.String())` — the daemon's own record of the argv it handed each child, read for how many `--settings` occurrences it carried and in what order. Only the extracted `spawning claude` records are logged, never the whole buffer: that buffer is the daemon's entire captured stderr, it is already teed to `os.Stderr` by the harness, and dumping it would duplicate an unbounded, unfiltered haystack into the failure text for no diagnostic gain.

### The risk these two witnesses exist for

`Pool.New` and `buildSession` both compose the child argv as `[operator pass-through] … --settings <daemon mcp file> …`. The operator's pass-through goes FIRST. So a rig `--settings` is the earlier of two occurrences of a flag claude's help declares as single-valued (`--settings <file-or-json>`, no variadic ellipsis, unlike `--add-dir <directories...>`).

If claude resolves a repeated single-valued option last-wins, the daemon's file wins and the rig's hook never registers. The hook would not fire, no informational line would be produced, and no banner would arrive — a red that says nothing about the path this ticket exists to prove.

This was not measurable here: a local probe spawning `claude` was denied by the dispatcher, and per the family's standing rule a local `claude` result would say nothing about the gate's own credentials anyway. So it is handled by making the reading unambiguous instead of by betting on it. The hook witness separates the three readings cleanly:

- witness empty → the hook never ran. Either the rig's `--settings` was shadowed by the daemon's, or a hook does not run on this spawn. Neither is a claim about the banner path. Route back; the spawn-record log line in the same failure carries the argv that decides which.
- witness holds `blocked` and no banner arrived → the hook ran, refused the prompt, and the refusal did not reach the client. **That is the finding this ticket commissions.**
- witness holds `blocked` and the banner arrived → green.

The two settings files conflict on no key — `mcpSettingsFile` declares two booleans and no hooks — so whichever wins, nothing else about the spawn changes. Both booleans suppress PTY-path startup dialogs that the stream-json path does not raise, so losing them costs nothing here.

## Concurrency model

No goroutine is created. The daemon and its children are owned by `spawnBootstrapDaemon`'s existing `t.Cleanup`, unchanged.

ONE reader for the whole window. The Noise receive nonce is sequential, so every `noise_msg` is decrypted in arrival order and non-`noise_msg` control frames are skipped WITHOUT decrypting, since they do not advance the nonce. A second concurrent reader would desync the `CipherState` and surface as an unrelated decrypt error many frames later.

No `t.Parallel`: `WithWorktreeAuthenticated` calls `t.Setenv`.

## Error handling

Every failure is `Fatalf` or `Errorf`, never a skip, so AC 2's "fails rather than skips if the banner never arrives" holds structurally. The suite-wide skip when claude or credentials are absent stays where it is, inside the shared harness.

Window-wide negatives, folded into `next` as Fatal because no caller could do anything better with them:

- an `error` envelope inside the window;
- an `unrecognized_message`. Its failure text must NOT claim a changed informational shape: `emitInformationalBanner` consumes the line either way and emits nothing on a decode failure, so a shape change is silence. An `unrecognized_message` here is the ordinary known-ignored-list staleness the suite's shared drain also watches for.

Per-wait timeouts each name their own readings: no banner, the blocked turn never closing, turn 2 never acked, acked but never streaming, streaming but never closing.

Budgets reuse `perTurnReplyBudget` for anything that waits on a cold spawn or a model reply. The blocked turn's own boundary gets a shorter budget of its own, since the capture measured that phase at about 1.5 seconds once the child was up.

## Testing strategy

This ticket IS a test. What has to be proven about it before it reaches a live gate:

- `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` and `go build -tags e2e_realclaude ./internal/e2e/realclaude/` (or the vet equivalent that forces a compile). **Non-negotiable and the whole hermetic gate for this change**: `make check` never compiles this package, so a compile error in the harness edit would surface only as every live test in the package failing at once. This is the same hazard `spawnBootstrapDaemonVerbose`'s header names.
- `go vet ./...` and `go build ./cmd/pyry` for the untagged tree, which the harness signature change does not reach but which must stay clean.
- `make check`'s own gates are the verifier's, not run here.

There is no RED-then-GREEN step available: the subject is a live claude, the assertion cannot run without credentials this session does not have, and the production code it exercises is already shipped and already proven hermetically by #2319. The RED this file exists to produce is a future one — a claude that stops emitting the line, or a daemon that stops carrying it.

The live run is `make e2e-realclaude` on an authenticated machine. The ticket is labelled `needs-real-claude`, so the dispatcher's gate runs it. Its result must be read from the count of executed tests, never from the exit code.

## Open questions

1. **Does claude honour the first or the last of two `--settings` flags?** Unmeasured, and not measurable from here. Resolved by design rather than by answer: the hook witness makes both outcomes legible. If the live gate reds with an empty witness, the answer is "last wins" and the follow-up is a separate ticket about how a test rig reaches a daemon-spawned child's settings — not a weakening of this assertion.
2. **Does the hook fire on the very first prompt to a cold-spawning child?** The committed capture's `block` phase was the session's first turn and fired, but through a direct spawn. If it turns out to need a warm child, the fix is a leading throwaway turn, recorded as a revision.
3. **Does a hook-blocked turn produce a client-visible `turn_state{idle}`?** The capture shows the block phase closing on `result`, and `result` closes an open turn, so it should. `awaitTurnIdle`'s timeout names this explicitly so a miss reads as itself rather than as a missing banner.

Each is resolved in Phase B or recorded under `## Revisions`.

## Security review

**Verdict:** PASS, on the second pass. The first pass FAILED on the terminal-escape finding below; the plan was revised before this section was written, and the fix is in § Design's Assertions rather than only here.

**Findings:**

- **[Error messages, logs, telemetry / Threat model] MUST FIX — raised and folded into the design.** The first draft logged the banner's `text` verbatim as the family's live capture, copying #2138's shape. But #2138's capture was four identifiers; this one is arbitrary claude-authored prose, and this frame's own doc says newlines, terminal escapes, markup and text impersonating daemon chrome all fit inside its 4 KiB. `t.Logf` writes to the operator's terminal, so `docs/protocol-mobile.md` § Security model's threat 1 lands here in the outward direction and this test IS the render boundary that owes control-character stripping. The design now requires `%q` at every log site touching claude-authored text, which renders every control byte as an escape instead of executing it, plus a summary rather than the prose on the success path.
- **[Error messages] SHOULD FIX — folded in.** Dumping `h.daemon.stderr.String()` into a failure message would duplicate an unbounded unfiltered buffer that the harness already tees to `os.Stderr`. Only the extracted `spawning claude` records are logged.
- **[Subprocess / external command execution] Design decision, stated rather than assumed.** This ticket deliberately causes claude to execute a shell script as the operator — the same property `writeMCPSettings`' doc names for any file pyry hands claude as `--settings`. Three things keep it bounded, and all three are properties of `oslcapWriteHookRig` rather than of this file, which is the reason it is reused whole instead of re-derived: `oslcapHookScript` builds its only shell source through `oslcapShellQuote`, which REFUSES a value carrying a single quote rather than sanitising it; the script reads no environment variable, so the OAuth credential the child inherits has no path into the witness; and it writes one fixed word per invocation and never the payload claude hands it on stdin. The design adds the one thing the reuse does not give for free — the rig error is `t.Fatalf`'d, since a refused quote returns a half-built rig whose script was never written.
- **[File operations] No findings.** The rig's three files get an explicit `os.Chmod` after each write (script 0700, settings 0600, witness 0600, dir 0700) because `os.WriteFile`'s perm argument is umask-masked; that is `oslcapWriteHookRig`'s existing discipline. Every path is a compile-time constant of this file joined to a Go-minted HOME under `t.TempDir()`, so there is no user input to traverse with, no check-then-use, and nothing to canonicalise. The one path this test compares is compared against the exact string its own seed callback returned, so a symlinked `/var/folders` component cannot produce a mismatch. Writing the settings file at 0600 inside a 0700 dir under a 0700 per-user temp HOME is what keeps "another process rewrites the hook command between write and read" out of reach — the same integrity argument `writeMCPSettings` makes, and the reason mode is set rather than left to umask.
- **[Tokens, secrets, credentials] No findings.** No token is minted, stored, compared or logged here. `WithWorktreeAuthenticated` puts `CLAUDE_CODE_OAUTH_TOKEN` in the environment and `spawnBootstrapDaemon` hands `os.Environ()` to the daemon, so the child and its hook inherit it; the hook reading no environment variable is what closes that path, and it is named above because reusing the helper is what buys it. The per-run nonce is a run discriminator, not a security value, so `time.Now().UnixNano()` is the right source.
- **[Trust boundaries] No findings beyond the logging one.** The boundary is single and explicit: claude's stdout crosses into daemon-parsed state at `emitInformationalBanner` and reaches this test only as a decoded `protocol.BannerPayload`. The test never re-scans raw bytes and never treats the text as anything but an opaque string to substring-match and quote.
- **[Concurrency] No findings.** No goroutine is created. Exactly one reader for the whole window, which the Noise receive nonce requires. `lockedBuffer` is mutex-guarded, which is why reading the daemon's stderr while `os/exec`'s copy goroutine writes is safe.
- **[Network & I/O] No findings.** The wire is the harness's existing fakerelay loopback, unchanged; the daemon's producer caps `text` at 4 KiB and `level` at 256 before either reaches it. Every wait carries a deadline, so the window's collected-banner slice is bounded by time rather than by claude's willingness to stop.
- **[Cryptographic primitives] Not applicable.** The Noise_IK handshake, its `CipherState`s and its key material are the shared harness's, used unchanged; this ticket adds no primitive, no comparison against a secret and no key.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-10

## Revisions

### 2026-09-10 — the hook moves from `--settings` to the HOME's user settings

**What drove it.** The first live gate run reddened on this ticket's own test: 1377 tests executed, 1376 passed, this one failed with an EMPTY hook witness — reading (a) of the four the design commissioned, which says nothing about the banner path. That is Open question 1 answered.

**The measurement.** The failure printed the daemon's own spawn records, and both children received `--settings` twice, the rig's first and the daemon's second. `Pool.New` and `buildSession` append pyry's MCP settings pair after the operator's pass-through, exactly as § Design's risk paragraph predicted. What the plan could not measure then is now readable without a live probe: claude 2.1.259 registers `--settings` with a plain single-valued option and no accumulating parser, while `--plugin-dir` one entry along in the same option table registers one explicitly and advertises itself as repeatable. A repeated single-valued option is last-wins, so the daemon's file won and the rig's hook never registered.

**The design change.** A test whose child the daemon spawns cannot reach that child through `--settings` at all — every value it passes is shadowed by construction, so this is not a tuning problem. The hook now goes into the harness-minted HOME's user settings at `<home>/.claude/settings.json`, installed by `hookBannerInstallUserSettings` from the rig builder's own JSON. That is a settings source of its own rather than a competitor for the same flag: claude's retention diagnostic calls the user source "disabled (`--setting-sources`)", so the flag is what turns it off, and pyry passes no such flag. The two files still conflict on no key, so the daemon keeps the keys `writeMCPSettings` sets. Planting configuration under the minted HOME for a daemon-spawned child to read is the route `TestInteractiveStreamSkillInvocationIsSilent` already takes for a skill.

**What this costs AC 3.** Nothing structurally: both helpers still accept extra pass-through claude arguments, every existing caller keeps what it passed, and the seeded harness's own spawn is still the one call that forwards them. What changes is that this file now forwards `nil`, because the one flag it wanted to pass is the shadowed one. Passing it anyway would assert nothing, and would risk registering the same hook twice on a claude that ever did merge both occurrences — which is the failure mode the "exactly one banner" assertion would take.

**Open questions.** 1 is answered above, and its stated follow-up — "a separate ticket about how a test rig reaches a daemon-spawned child's settings" — is resolved in place instead, since the user-settings source needs no argv at all. 2 and 3 stay open on the same terms: the run never got a hook to fire, so neither was reached.

**Security addendum.** The new write is one more file under the same Go-minted HOME, at 0600 inside a 0700 directory, with an explicit `os.Chmod` after the write because `os.WriteFile`'s perm argument is umask-masked — § Security review's File-operations finding, unchanged in substance. It declares a hook, so it is code execution as the operator by the same design decision that section already states, and the executed script is still `oslcapWriteHookRig`'s, quoted by `oslcapShellQuote` and reading no environment variable. One thing is added: the installer refuses an existing file rather than overwriting it, because a second writer of claude's user settings appearing in this harness would leave the rig competing for the child's hooks instead of supplying the only ones in play.

### 2026-09-10 — a refused turn has no client-visible boundary, so turn 2 sends on the banner

**What drove it.** The second live gate run reddened on this ticket's own test again: 1377 executed, 1376 passed. But the red moved, and it moved past the thing the ticket exists to prove. The banner **arrived**, carrying the hook's block reason, `level` warning, 397 bytes, `truncated` false, `stops_turn` true — and the hook witness read `blocked`. So AC 1's first half and AC 2 are now proven live, and readings (a) through (d) are all excluded. What failed was the next wait: `awaitTurnIdle` never saw the refused turn's terminal `turn_state{idle}` in 60s. That is Open question 3 answered, in the negative.

**The measurement.** No such frame exists, by design, and production says so at two sites this plan's Files read did not include. In `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.Handle`, the `turnevent.Banner` arm mutates no turn lifecycle — no `startTurnIfNeeded`, no `transitionTo`, no `endTurn` — and its own comment gives the reason and even names this case: *"a prompt a hook refuses is never answered — so no turn end follows either producer, ever."* Its `turnevent.TurnEnd` peer then drops a turn end arriving outside an open turn, logging `interactive_turn.turn_end_no_turn` and emitting nothing. A refusal therefore opens no turn on the wire and closes none: its single client-visible frame is the banner, whose `stops_turn` is how the client learns the prompt was refused.

**Why the plan missed it.** Files read listed `internal/turnbridge/outbound.go`, the pure mapper, and took it for the wire producer. It shapes payloads; it does not decide which frames exist. The lifecycle machine that decides is `interactiveTurnEmitterV2.Handle`, one layer up in `cmd/pyry`, and it was never read. The ticket body's own premise — that the capture's `result` line means "a blocked turn closes on its own and the unblocked follow-up has an ordinary turn boundary to wait for" — is true of claude's stdout and false of the wire, and only the driver could have shown that.

**The design change.** `awaitTurnIdle` is replaced by `awaitRefusedTurnQuiet`, which reads the window for a bounded quiet period and asserts the absence rather than stepping around it, naming two readings for a frame that does arrive: a turn opened on this path, or claude began answering refused prompts. Turn 2 is then sent on the banner. So #2138's ordering is inherited after all, for a different reason than its own: that file raced a transition because whether its turn closed was the unknown, whereas here the close does not exist. The quiet budget is spent in full on every green run, which is the price of a negative — an absence measured by a snapshot taken the instant the banner was read would measure only impatience.

**And the refusal does not wedge the next send**, which is what makes AC 1's second half provable with no boundary to wait on. The frame is suppressed but the event is not: `turnMarkFor` answers `turnMarkClose` for `turnevent.TurnEnd`, so the same `result` closes `turnBusyTracker`'s mark at the drain's fan-in, independently of whether the emitter had a wire turn to close. And `streamsup.Runner.WriteUserTurn` refuses a turn only for a dead child, an armed rotation or an unconfirmed permission posture, never for a turn in progress — the PTY path's `WaitReady` idle gate that `msgqueue.DeliverFunc`'s doc describes is not this path. The replaced wait's failure text asserted the opposite, that a missing idle would hold the next send forever; that claim was wrong and is gone.

**Also folded in.** The window's `idles` counter is removed, since `awaitRefusedTurnQuiet` needs the frame rather than a tally and `awaitCompletedTurn` already decoded its own. That resolves the previous review's one NIT: the counter's rationale claimed two consumers where the code had one, and now has none.

**Open questions.** 1 was answered by the previous revision. 2 is answered YES: the witness held `blocked` on the session's first prompt to a cold-spawning child, so no warm-up turn is needed. 3 is answered above, in the negative, and the answer is now an assertion rather than an assumption.

**Security.** No new finding. No file write, no log site and no format verb is added that touches claude-authored text; the one field the new failure text renders is `turn_state.state`, which the daemon mints from `turnbridge.TurnState`'s own constants, and it is quoted anyway.
