# Spec — #603: restore send_message e2e coverage by teaching fakeclaude the idle/thinking glyphs

**Ticket:** pyrycode/pyrycode#603
**Size:** S (option 1 — teach fakeclaude the glyphs; see § Option choice)
**Labels:** `security-sensitive` (substrate-seal exemption — see § Security review)

## Files to read first

Code surface the developer loads on turn 1. Paths + what to extract.

- `internal/e2e/internal/fakeclaude/main.go` (whole file, 165 lines) — the helper being changed. Note the existing `main` poll loop, `emitAssistantIfTriggered` (writes to `os.Stdout`), and `startStdinLogger` (the only current stdin reader, gated on `PYRY_FAKE_CLAUDE_STDIN_LOG`). The new TUI mode adds to these.
- `cmd/substrate-guard/main.go:32-36` — `allowlist` (path-suffix exemptions). One line is added here. Read the whole file (130 lines) to understand the ban patterns — esp. `patterns` at `:53-70` (the two glyphs U+273B `✻` / U+276F `❯` are banned in three encodings each; CSI `\x1b[` is also banned).
- tui-driver `pkg/tuidriver/ready.go:42-54` (`WaitReady`) and `pkg/tuidriver/state.go:38-54` (`IsIdle` / `IsThinking`) — **the detection contract.** `IsIdle` = `❯` present **AND** `✻` absent; `IsThinking` = `✻` present. Both run over `StripANSI(Snapshot())`.
  - Module path: `$(go list -m -f '{{.Dir}}' github.com/pyrycode/tui-driver)` → `pkg/tuidriver/`.
- tui-driver `pkg/tuidriver/buffer.go` (whole, 80 lines) — **decisive design fact:** `Snapshot()` is a **rolling raw-byte window (`DefaultBufferCap` = 4096B), not a terminal-grid emulator.** A glyph persists in the snapshot until ≥4096 later bytes evict it; `\r`/cursor moves do **not** clear it. This is why a single startup `❯` write stays "idle" indefinitely and why the design needs no continuous redraw (each restored flow drives exactly one idle→thinking transition).
- tui-driver `pkg/tuidriver/deliver.go:94-201` (`DeliverPrompt` / `promptDidCommit`) — commit confirmation polls `IsThinking` (150ms poll, `DefaultPromptCommitTimeout` = 3s). The `✻`-absent / no-"[Pasted text]"-chip "committed-but-slow" fallback is **3s slow** — too slow for the tests' ack deadlines, which is why fakeclaude must emit `✻` for a *fast confirmed* commit (AC #2: "reaches a **confirmed** commit").
- `internal/supervisor/supervisor.go:199-255` — `WriteUserTurn` + `deliverViaSession`. Note: cursor is stamped **before** `deliverFn` runs (load-bearing for the ack-pollution interaction below); `JSONLPath` is left `""`, so the spinner is the only fast commit signal.
- `cmd/pyry/assistant_turn.go:83-159` — `assistantTurnEmitter.Run`/`broadcast`. **`broadcast` forwards any non-empty PTY chunk as a `message` envelope when the cursor is non-empty** (`Text = string(chunk)`, no glyph filtering). This is why the `✻` chunk reaches the phone and races the ack — see § Ack-pollution interaction.
- The five test files (each carries one `t.Skip("blocked on #603 …")` to remove):
  - `internal/e2e/relay_send_message_test.go` — `TestRelay_SendMessage_AckAndPTYDelivery` (v1; asserts ack + stdin marker)
  - `internal/e2e/respawn_after_eviction_test.go` — `TestE2E_IdleEviction_RespawnsOnSendMessage` (v1; evict→respawn→ack; **inlines `startEvictionHarness` at `:233`** — the env-injection site for this test)
  - `internal/e2e/relay_roundtrip_test.go` — `TestRelay_Roundtrip_Appendix` (v1; send_message + assistant echo)
  - `internal/e2e/relay_assistant_turn_test.go` — `TestRelay_AssistantTurn_BroadcastsMessageEnvelope` (v1; send_message + assistant echo)
  - `internal/e2e/relay_v2_daemon_test.go` — `TestRelayV2_AssistantTurn_BroadcastsMessageEnvelope` (**v2 / Noise**; sealed send_message + assistant echo). NB: the un-skipped `TestRelayV2_Daemon` subtests in this file are unrelated — they use `/bin/sleep`, not fakeclaude.
- `internal/e2e/harness.go:304-360` (`StartRotationWithRelay`) — note the trailing `extraEnv ...string` param: the four `StartRotationWithRelay` callers opt into TUI mode through it, **no harness.go change required.** Also `internal/e2e/internal/fakephone/fakephone.go:113-165` (`Receive` + `ErrReceiveTimeout`) for the v1 drain loop, and `relay_v2_handshake_test.go:158` (`readInnerFrame`) + `relay_v2_daemon_test.go:389` (`decryptInnerEnvelope`) for the v2 drain.

## Context

#594 rewrote `Supervisor.WriteUserTurn` to deliver through `tuidriver.Session.DeliverPrompt` behind a `WaitReady` idle-gate, acking only on a confirmed commit. `internal/e2e/internal/fakeclaude` renders **no claude TUI** — it never emits the idle prompt (`❯`) or thinking spinner (`✻`), so `WaitReady` never reaches idle, the 30s deliver timeout elapses, and `send_message` replies `server.binary_offline` instead of `ack`. Five `//go:build e2e` flows that drive `send_message → WriteUserTurn` were `t.Skip`ped in #594 to absorb this. This ticket restores them by teaching fakeclaude the two glyphs the detection contract needs, so the harness regression-guards the genuine delivery path (today only `deliverFn`-seam unit/handler tested).

### Option choice (architect-owned)

The ticket offers two mechanisms. **This spec selects option 1 (teach fakeclaude the glyphs).** Option 2 (migrate to the realclaude suite) is rejected: the realclaude suite has no `send_message`/`WriteUserTurn` flow, so it would be a from-scratch five-flow build that exceeds S — and per architect memory [[architect-structured-stream-e2e-harness-gap]] there is no Noise-phone realclaude harness for these flows at all. Option 1 is confined to the test harness + the substrate allowlist (AC #3) and lands as one S ticket.

## Design

Two production-source edits (fakeclaude + the allowlist) plus five test-file edits. No pyry runtime code changes (AC #3). No new exported types. No new files.

### 1. fakeclaude TUI mode (`internal/e2e/internal/fakeclaude/main.go`)

Add a **flag-gated** TUI mode. A new env var `PYRY_FAKE_CLAUDE_TUI` (set to any non-empty value) turns on glyph emission. When unset, fakeclaude is **byte-identical to today** — this keeps the rotation tests (`StartRotation`, `fakeclaude_test.go`/`rotation_test.go`) and the non-skipped `relay_two_phone_coarse_test.go` (also a `StartRotationWithRelay` caller) unperturbed. Opt-in is per-test, not baked into a shared helper.

Behaviour when TUI mode is **on**:

| Moment | Action | Why |
|---|---|---|
| startup (before the poll loop) | write the **idle-prompt glyph U+276F (`❯`)** once to `os.Stdout` | seeds `IsIdle` true so the first `WaitReady` returns immediately. Single write suffices — the rolling buffer holds it (no continuous redraw needed). |
| first stdin bytes read | write the **thinking-spinner glyph U+273B (`✻`)** once to `os.Stdout` | the prompt delivery (a `WaitReady`-gated `DeliverPrompt`) is the only stdin fakeclaude receives; its arrival is a faithful "claude started the turn" signal → `IsThinking` true → fast confirmed commit. |

Contracts / constraints (the load-bearing parts — developer writes the bodies):

- **One stdin reader.** The existing `startStdinLogger` goroutine is the *only* stdin consumer; a second reader would race it for bytes. Generalise it: start the goroutine when `PYRY_FAKE_CLAUDE_STDIN_LOG` is set **OR** TUI mode is on. Inside, per read: append to the log file *if* a log path is configured (unchanged behaviour), and — *if* TUI mode is on and the spinner has not yet been emitted — emit `✻` (once). In all five flows `STDIN_LOG` is already set, but TUI mode must independently cause stdin to be read so the design doesn't depend on that coupling.
- **fakeclaude must not echo stdin to stdout.** It writes a *fixed* glyph, never the stdin content. (Preserves the existing no-echo behaviour and avoids reflecting phone-controlled prompt bytes onto the observed PTY — see Security review § Trust boundaries.)
- **Serialize all stdout writes under one mutex.** `emitAssistantIfTriggered` (main goroutine) and the spinner emit (stdin goroutine) both write `os.Stdout`. Without a shared `sync.Mutex` around every `os.Stdout.Write`, a spinner write could interleave mid-assistant-chunk and corrupt the marker the echo tests match on. Guard the startup `❯` write, the `✻` write, and the assistant-chunk write with the same mutex. Reference invariant: the assistant marker (`MessagePayload.Text`) must arrive contiguous.
- **Emit glyphs as bare runes**, no CSI/cursor-control escapes and no "[Pasted text]" chip. Keeping the emitted substrate to exactly the two glyphs minimises the allowlist exemption's blast radius (Security review § Subprocess/substrate).
- Document the new env var in the package doc comment alongside the existing `PYRY_FAKE_CLAUDE_*` vars; add an `envTUI` const beside the others.

No "return to idle" / spinner-eviction logic is needed: **every restored flow drives exactly one idle→thinking transition** (single `send_message`; test 2's second claude is a fresh respawned process that re-seeds `❯` at startup). Confirmed by reading all five flows — none issues a second `send_message` against the same live session.

### 2. substrate-guard allowlist (`cmd/substrate-guard/main.go`)

fakeclaude's source will now carry the raw `❯`/`✻` glyphs, which the guard bans. Add the one path suffix to `allowlist` (`:33-36`), mirroring the existing `internal/agentrun/ptyrunner/helper_test.go` exemption:

```
"internal/e2e/internal/fakeclaude/main.go",
```

Update the file's package doc (`:16-19`) to name fakeclaude as the second sanctioned fake-claude helper. `make substrate-guard` / `make check` stay green (AC #4).

### 3. Ack-pollution interaction → drain-until-ack in the five tests

**This is the non-obvious part the ticket's Technical Notes under-counted.** The `✻` glyph fakeclaude writes is the fast commit signal — but tui-driver reads the PTY once and fans it to **both** its internal buffer (commit detection, good) **and** `Session.MirrorOutput()` → `Bridge.Write` → the assistant-turn observer. Because `WriteUserTurn` stamps the cursor **before** delivery (`supervisor.go:206-208`), `assistantTurnEmitter.broadcast` (`assistant_turn.go:101-108`) sees a non-empty cursor and forwards the `✻` chunk as a `message` envelope to the same phone conn — **racing the ack** (the emitter is an async queue + goroutine; the ack is the handler's synchronous reply; order is non-deterministic).

This is faithful to production: real claude's spinner frames are forwarded as `message` chunks too — which is exactly why tests 3/4/5 already carry "tolerate prelude chunks (TUI banner / spinner)" drain loops in their *assistant* phase. The fix extends that tolerance to the **ack receive**:

- **v1 tests (1, 2, 3, 4):** replace the single `phone.Receive(...)` that expects the ack with a loop that receives until `Type == protocol.TypeAck`, skipping `message` (and any other non-ack) envelopes, bounded by the same deadline already in the test (3s for 1/3/4; 15s for 2). On `ErrReceiveTimeout` before an ack, fail as today. Recommend a shared helper (e.g. `recvEnvelope(t, phone, want, timeout)`) placed beside the other v1 helpers (`relay_test.go`) to DRY the four edits; the v2 test cannot use it (encrypted frames).
- **v2 test (5):** the ack is read via `decryptInnerEnvelope(readInnerFrame(...))`. Replace the single decrypt with a decrypt-drain loop until `Type == TypeAck` — mirroring the assistant-message drain immediately below it in the same function. The receive-CipherState nonce is sequential, so every binary→phone frame **must** be decrypted in capture order; the loop naturally satisfies this.
- After draining the ack, each echo test's existing assistant-phase drain (3, 4, 5) is unchanged. Tests 1 and 2 have no assistant phase.
- Each test also: removes its `t.Skip` line, and opts into TUI mode.

### TUI-mode opt-in wiring (no harness.go change)

- Tests **1, 3, 4** (and **5**) call `StartRotationWithRelay(...)`, which already forwards a trailing `extraEnv ...string`. Pass `"PYRY_FAKE_CLAUDE_TUI=1"` there. Tests 4 and 5 already pass `extraEnv` (assistant trigger / `PYRY_MOBILE_V2`), so it's an append; test 1/3 add the first `extraEnv` arg.
- Test **2** builds its child via the inlined `startEvictionHarness` (`respawn_after_eviction_test.go:233`). Add `"PYRY_FAKE_CLAUDE_TUI=1"` to that helper's `extraEnv` slice (`:248-254`).

## Concurrency model

fakeclaude goroutines (unchanged topology except the stdin reader's dual role):

- **main goroutine** — the poll loop (rotation trigger, assistant trigger). Writes `os.Stdout` only via `emitAssistantIfTriggered`, now under the shared stdout mutex. Also performs the one-shot startup `❯` write (under the mutex) before entering the loop.
- **stdin reader goroutine** — started iff `STDIN_LOG` set OR TUI on. Reads stdin; appends to log (if configured); emits `✻` once (if TUI on), under the shared stdout mutex. A simple bool (set once, checked-and-set inside the read loop on the single goroutine — no cross-goroutine sharing of the bool) gates the one-shot.
- **Shared state:** one `sync.Mutex` guarding `os.Stdout` writes. No other shared mutable state between the two goroutines (the spinner-emitted bool lives entirely on the stdin goroutine). No lock ordering concern (single lock). fakeclaude has no shutdown path — it idles until SIGKILL (unchanged); both goroutines die with the process.

## Error handling

fakeclaude keeps its existing posture: glyph writes are best-effort (`_, _ = os.Stdout.Write(...)`, mirroring `emitAssistantIfTriggered`); a write error is silenced because the e2e asserts downstream (phone-side ack / message). No new fatal paths. The substrate-guard change is a pure data add; no new error branches.

Failure-mode reasoning for the tests:
- If `❯` were never seen, `WaitReady` would block to the 30s deliver timeout and the test would fail on `binary_offline` — the *current* skip symptom, so a regression here is loud.
- If `✻` were never seen, `DeliverPrompt` would fall through to the 3s committed-but-slow path; the ack would arrive ~3s late and trip the 3s `phone.Receive` deadline. So a broken spinner emit surfaces as a deadline failure, not a false pass.

## Testing strategy

No new test functions — the deliverable is the five existing flows passing green under `//go:build e2e` (4 v1) and the v2 build (test 5 is `//go:build e2e`; it exercises the Noise path in-process). Verification:

```
go build ./... && go vet ./...
go test -race ./...            # default suite unaffected (e2e is build-tag isolated)
make substrate-guard           # must stay clean (AC #4)
go test -tags e2e ./internal/e2e/...   # the five restored flows + the existing e2e set
```

Per-flow expected behaviour after the change:
- **Test 1** — ack received (draining one `✻` message if it lands first); `knownText` marker still appears in the stdin log (unchanged stdin-logging path; the bracketed-paste wrapper surrounds the marker contiguously, so `bytes.Contains` still matches).
- **Test 2** — eviction unchanged; after respawn the fresh fakeclaude re-seeds `❯`; ack within the 15s bound; new child PID observed.
- **Tests 3, 4** — ack (drained), then the assistant-echo drain finds the marker `message` (unchanged assertion).
- **Test 5** — sealed ack (decrypt-drained), then the sealed assistant-echo drain finds the marker.

Edge note for the developer: all five prompts contain `\n`, so `DeliverPrompt` takes the bracketed-paste (`WritePrompt`) branch, not `TypePrompt`. fakeclaude's "any stdin → `✻`" trigger is branch-agnostic, so this doesn't affect the design — but it's why the stdin marker stays contiguous in test 1.

## Open questions

- **Shared v1 drain helper vs inline loops.** Recommended (a `recvEnvelope` helper beside the v1 test helpers) but not mandated; the developer may inline four small loops if a helper feels heavier than the duplication. Either satisfies the AC.
- **Spinner label text.** `IsThinking` needs only the `✻` rune; emitting `✻` alone vs `✻ thinking` is the developer's call (both pass, both stay inside the allowlisted file). Prefer the barer form to minimise emitted substrate.

## Scope self-check

Production source files (non-test `.go`) with new/modified content: **2** — `internal/e2e/internal/fakeclaude/main.go`, `cmd/substrate-guard/main.go`. Test files (`_test.go`): 5. New files: 0. New exported types: 0. Consumer call sites needing simultaneous update: 0 (no signature/interface change; the five test edits are independent, no cascade). Under every red line → S, single ticket.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The relevant boundary is phone-controlled prompt bytes → fakeclaude stdin → observed PTY. The design holds the boundary: fakeclaude emits a **fixed glyph** on stdin activity and **never echoes stdin content to stdout**, so phone-controlled bytes cannot be reflected onto the PTY/`message` stream through the new path. The bytes already reach the stdin log verbatim (pre-existing, unchanged) and the test-data note in `relay_send_message_test.go:5-7` already warns against secrets in markers. fakeclaude is a test-only `package main` under `internal/e2e/internal/` (visibility-fenced) and is **never compiled into `pyry`**, so nothing here crosses into a production trust boundary.
- **[Subprocess / substrate seal]** SHOULD FIX (addressed in design, code-review confirms). Expanding the `cmd/substrate-guard` allowlist is the security-relevant call that earns the label. Mitigations baked into the spec: (a) the exemption is a single path suffix scoped to one file, mirroring the sanctioned `ptyrunner/helper_test.go` precedent — the allowlist *mechanism* is unchanged; (b) the file is a test-only helper not importable by or compiled into production, so a substrate literal there cannot drift into shipped code; (c) the design constrains emitted substrate to exactly the two glyphs (no CSI escapes, no paste chip), keeping the now-unguarded file minimal and auditable. Residual risk: a future edit could add un-guarded substrate to this file — accepted, same residual the existing exemption already carries; code-review on this PR confirms only the two glyphs are present.
- **[Error messages, logs, telemetry]** No findings. fakeclaude adds no logging of stdin/stdout content; glyph writes are silent best-effort. The assistant emitter's "never log chunk bytes" posture (`assistant_turn.go:41-45`) is unchanged — the `✻` chunk flows as `MessagePayload.Text` (test data), never logged.
- **[Concurrency]** No findings. One added `sync.Mutex` serialises stdout writes (single lock, no ordering concern); the one-shot spinner bool is confined to the stdin goroutine (no shared-state race). `go test -race` over the e2e tag is part of the verification gate. No new goroutine leak: both fakeclaude goroutines terminate with the process (unchanged lifecycle).
- **[Cryptographic primitives]** OUT OF SCOPE for the change itself (no crypto added). Test 5 exercises the existing Noise_IK path unchanged; the drain-until-ack loop respects the sequential receive-nonce invariant (decrypts every frame in capture order), so it does not perturb the cipher state.
- **[Tokens/secrets, File ops, Network & I/O]** N/A — no token handling, no new filesystem path construction from untrusted input (the new code writes fixed glyphs to `os.Stdout`; existing log-file open path is unchanged), no new network surface or socket reads.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-08
