# Spec #642 — Event-stream bridge: live two-phone structured-receive e2e capstone

**Part of EPIC #596** (Phase 2 structured streaming). See [ADR 025](../../knowledge/decisions/025-mobile-remote-head-interactive-session.md) § Phase 2.
Split from #634 (the structured-receive capstone half). `security-sensitive`.

## Verdict up front: reuse the harness, ship as one S ticket (no carve)

The ticket's Prerequisite section asks the architect to decide whether the
structured-stream-capable e2e harness must be **built** (→ carve out, route to
PO) or can be **reused/extended** (→ ship the capstone as one ticket). **It can
be extended cheaply. No carve.** Justification, then the design.

The structured pipeline is **production-shipped and already wired into the
running daemon**:

```
claude JSONL transcript
  → tui-driver Session.Events → TailJSONL          (parses each line → JSONLEntry)
  → turnbridge mapEvent                            (JSONLEntry → turnevent.Event)
  → interactiveTurnEmitterV2.Handle                (#632 capability-gated fan-out)
  → relay V2SessionManager.Push (Noise-sealed)     (only c.Interactive conns)
  → interactive phone decrypts the structured envelopes
```

`startInteractiveTurnStreamV2` (cmd/pyry/interactive_turn_stream_v2.go) is
invoked from `cmd/pyry/relay.go:339-340`, gated on `bridge != nil &&
claudeSessionsDir != ""`. So whenever the daemon runs with a coarse bridge and a
non-empty sessions dir — which the relay harness already arranges — the
structured producer is **live**, tailing `claudeSessionsDir` for `<uuid>.jsonl`.

The Noise two-phone harness **already exists**: `internal/e2e/internal/fakephone`
+ `fakerelay`, the v2 handshake drivers, and #634's
`TestTwoPhoneCoarse_NonInteractiveOnly` (which pairs two phones, grants A
`interactive` via `driveHandshakeToOpenDaemonInteractive`, drives a turn, and
asserts coarse routing with a per-frame decrypt-drain). That test is a
near-complete template for this one.

Only **two** things are missing, both small:

1. **Sessions-dir alignment.** #634 pointed fakeclaude at a throwaway
   `tmp/claude-sessions` while the daemon's producer tailed its *computed*
   `<HOME>/.claude/projects/<encoded-workdir>` — so the producer never saw
   fakeclaude's JSONL (fine for #634, which proved the coarse path off the PTY
   chunk). The fix is the **rotation-test alignment pattern**: set `sessionsDir
   = filepath.Join(home, ".claude", "projects", encodeWorkdir(home))` and pass
   that same dir to fakeclaude. Then the producer tails exactly what fakeclaude
   writes. (`resolveClaudeSessionsDir` has **no env override** — it always
   computes from workdir+HOME, so alignment is by construction, not a flag.)

2. **fakeclaude structured-JSONL emission.** Today `openSession` writes only
   `{}\n` (which parses to an empty entry → maps to nothing). fakeclaude needs a
   trigger that appends **captured claude-format JSONL lines** to its live
   session file, so the producer's tail picks them up. This is ~20 LOC mirroring
   the existing `emitAssistantIfTriggered` trigger pattern.

This is an **extension of a working harness**, not a from-scratch build. The
genuine foundational build would be **option (a)** — real claude under a
Noise-phone suite — which is rejected below. So the capstone stays one ticket.

## Files to read first

- `internal/e2e/relay_two_phone_coarse_test.go` (whole file, ~340 lines) — **the
  template.** Lift the two-phone pair+handshake, `driveHandshakeToOpenDaemonInteractive`
  / `buildHelloEarlyInteractive` (defined here, same `e2e` package — reuse, do
  NOT redefine), the conversations.json seed, the send_message drive, and the
  per-frame decrypt-drain loop. The structured test is this with A's assertion
  flipped to "receives structured" and B's kept as "receives coarse only."
- `cmd/pyry/interactive_turn_v2.go:130-137` — `Handle` **drops every event when
  the cursor is empty** (`interactive_turn.no_cursor`). The load-bearing
  ordering constraint: a turn must be driven (cursor stamped) **before** the
  structured JSONL is tailed.
- `cmd/pyry/interactive_turn_v2.go:306-361` — `emit`: the capability gate
  (`if !c.Interactive { continue }`) and the v2 envelope shape (Type, Payload,
  EventID) the phone decrypts.
- `cmd/pyry/interactive_turn_stream_v2.go:45-96` + `:98-155` — the producer
  wiring and `resolveLatestSessionJSONL` (tails newest `<uuid>.jsonl` from
  EOF-at-subscribe). Explains why the producer subscribes once at relay startup
  and why appends after subscription are seen.
- `cmd/pyry/relay.go:339-340` — `startInteractiveTurnStreamV2` invocation gate
  (`bridge != nil && claudeSessionsDir != ""`). Confirms the producer is live in
  the harness.
- `internal/e2e/internal/fakeclaude/main.go:96-159` (`main` loop +
  `emitAssistantIfTriggered`) + `:161-174` (`openSession`) — the trigger pattern
  to mirror and the `{}\n` write to extend. **This file is on the
  cmd/substrate-guard allowlist** (#603) — but the new code adds no TUI glyphs,
  so the allowlist is unchanged.
- `internal/e2e/rotation_test.go:15-61` — `encodeWorkdir` helper (same `e2e`
  package — reuse) and the `sessionsDir = home/.claude/projects/encodeWorkdir(home)`
  alignment pattern.
- `internal/e2e/harness.go:304-352` — `StartRotationWithRelay` and its trailing
  `extraEnv ...string` seam (pass `PYRY_FAKE_CLAUDE_TUI=1` and the new trigger
  env; **no harness.go change needed**).
- `internal/turnbridge/mapper_test.go:18-170` — the **JSONL-shape oracle.** The
  `entry(...)` helper shows the exact line shapes that map to each event
  (assistant text → TextChunk; assistant `tool_use` → ToolStart; user
  `tool_result` → ToolUpdate; assistant `stop_reason:end_turn` + non-empty text
  → end-of-turn). Model the fixture lines on these.
- `internal/e2e/relay_v2_daemon_test.go` (`driveHandshakeToOpenDaemon`,
  `readInnerFrame`, `decryptInnerEnvelope`, `sendNoiseInit`, `sendNoiseMsg`) and
  `relay_test.go` (`shortHome`, `relayTestLogger`, `readPersistedServerID`,
  `mustJSON`) — shared `e2e`-package helpers the test reuses.
- `docs/knowledge/codebase/634.md`, `632.md`, `633.md`, `603.md` — the slices
  this capstone closes; 603.md's "ack-pollution drain" and "fakephone closes the
  WS on a timed-out Receive" lessons are load-bearing for the read loops.

## Context

The capability-gated dual-path is shipped and **deterministically** proven:
#632 fans structured envelopes only to `interactive` conns; #634 confines the
coarse `message` to `!interactive` conns (complementary filters over one
set-once flag); the #626/#632 unit matrices prove both halves. What is **not**
yet proven is the **live** confirmation: one daemon, two phones, where the
`interactive` phone actually **receives and decrypts** the structured envelopes
while the non-interactive phone receives only the coarse `message`. Every
upstream slice deferred this (#589/#634 prove only coarse; #632 shipped no e2e;
#633 drove a fake subscriber). This ticket lands the live structured-receive
proof. **Test/harness code only — no production change** (the dual-path
production code already shipped; a surfaced production gap is a separate ticket).

## Design

### Why option (b), not option (a)

The ticket offers two harness shapes. **Option (a)** — real claude JSONL under a
Noise-phone handshake suite — is the true foundational build: it would fuse the
`e2e_realclaude` suite (real `claude` binary, network, API key, non-deterministic
output and timing) with the `e2e` Noise-phone suite. That is multi-file
foundational work, is inherently flaky (real model output varies), and cannot
run in the standard `-tags e2e` CI column. **Rejected.**

**Option (b)** — fakeclaude replays a captured claude-format JSONL transcript —
keeps the entire existing `e2e` fakeclaude suite (Noise handshake, fakephone,
fakerelay, #634 template, #603 TUI glyph mode) and adds one small trigger to
fakeclaude. The structured events flow through the **real** production stack
(tui-driver parse → turnbridge → #632 emitter → Noise seal → phone decrypt);
only the claude *process* is the stand-in — exactly as it is for every other e2e
in the suite. The ticket explicitly sanctions (b). **Chosen.**

### Part 1 — fakeclaude: structured-JSONL trigger (~20 LOC)

Mirror `emitAssistantIfTriggered`, but append to the **session JSONL** instead
of stdout. Contract:

- New env var `PYRY_FAKE_CLAUDE_JSONL_TRIGGER` (path). When unset, behaviour is
  byte-identical to today (off by default — every existing caller unperturbed).
- New helper, signature only:
  `func emitStructuredJSONLIfTriggered(f *os.File, path string)` — when `path`
  exists, read its contents (capped like `assistantMaxBytes`), append them
  verbatim to `f` (the live session JSONL, already `O_APPEND`), `f.Sync()`, then
  `os.Remove(path)`. Errors silenced (the e2e asserts downstream). The trigger
  file's **contents are the JSONL lines to append** (same "contents are the
  payload" shape as the assistant trigger).
- Call it in the existing `main` loop next to `emitAssistantIfTriggered(asstTrig)`,
  passing the current `f` (so it follows a rotation if one ever happened; this
  test never rotates).

The `f.Sync()` is load-bearing: the daemon's tail is a separate process and
macOS APFS otherwise defers cross-process visibility (the same reason
`startStdinReader` fsyncs the stdin log).

**No substrate-guard change.** The new code adds no TUI substrate glyphs; the
appended bytes are JSON the test supplies. fakeclaude/main.go is already
allowlisted regardless. The new `_test.go` file is scanned by substrate-guard
but carries only JSON string literals (no `❯`/`✻`/CSI escapes) — clean.

### Part 2 — the fixture (JSONL the trigger carries)

The test writes the trigger file's contents as a short sequence of claude-format
JSONL lines, modeled on `turnbridge/mapper_test.go`'s `entry(...)` outputs. A
minimal sequence that exercises **every** structured envelope type:

| Line (shape; see mapper_test.go) | Produces |
|---|---|
| `assistant` + `content:[{type:text,text:"…"}]` | `turn_state(responding)` + buffered `assistant_delta` |
| `assistant` + `content:[{type:tool_use,id,name:"Read",input:{…}}]` | flush `assistant_delta` + `tool_use` |
| `user` + `content:[{type:tool_result,tool_use_id,is_error:false,content:"…"}]` | `tool_result` |
| `assistant` + `stop_reason:"end_turn"` + `content:[{type:text,text:"…"}]` | flush `assistant_delta` + `turn_end` + `turn_state(idle)` |

Lines are appended verbatim (each a complete JSON object + `\n`). The developer
may instead commit a curated subset of the real capture
`internal/agentrun/jsonl/testdata/clean.jsonl` (which carries all these shapes)
as a `testdata/` fixture — equivalent. **Recommend inline** (keeps NEW files to
one: the test).

### Part 3 — the two-phone test (`internal/e2e/relay_two_phone_structured_test.go`, new)

`//go:build e2e`, `package e2e`. Lift #634's structure. Choreography:

1. **Align the sessions dir:** `sessionsDir = filepath.Join(home, ".claude",
   "projects", encodeWorkdir(home))` (reuse the `e2e`-package `encodeWorkdir`).
   `MkdirAll` it (StartRotationWithRelay also does).
2. Pair phone A and phone B against instance `test`; seed `conversations.json`
   with `knownConvID` (cursor-validation gate).
3. `StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverCreatedRotate,
   stdinLog, fr.URL()+"/v2/server", "PYRY_MOBILE_V2=1", "PYRY_FAKE_CLAUDE_TUI=1",
   "PYRY_FAKE_CLAUDE_JSONL_TRIGGER="+jsonlTrig)`. **TUI mode on** so `WaitReady`
   confirms fast (#603) and the `send_message` ack is prompt.
4. Handshake A as `interactive` (`driveHandshakeToOpenDaemonInteractive`, which
   already asserts A's hello_ack **echoes** the interactive grant) and B as
   non-interactive (`driveHandshakeToOpenDaemon`). **Pin B's precondition:**
   assert B's hello_ack does **not** contain `protocol.CapabilityInteractive` —
   the complement of A's grant assertion. This makes the daemon's gate state
   explicit at both ends (A granted, B not), so B's "no structured" negative
   cannot lean on an implicit "B advertised nothing → presumably not granted."
   A mis-grant would already fail loud (B would receive structured → fatal), but
   pinning the flag closes the gap between "advertised" and "granted."
5. **Drive the turn from A and await its ack** (decrypt-drain to the sealed
   `ack`, per 603.md test 5). The ack confirms `WriteUserTurn` ran → the cursor
   is stamped → the structured `Handle` will see a non-empty cursor. A is
   `interactive`, so the coarse `message` is **never** pushed to A — A's ack
   stream is not coarse-polluted.
6. **After the ack**, drop the JSONL trigger (write the fixture lines as its
   contents). fakeclaude appends them to its live session JSONL; the producer
   (subscribed at startup, offset = post-`{}\n` EOF) tails them → emitter →
   sealed structured envelopes → A.
7. **Assert A (positive + negative):** decrypt-drain A's frames under a single
   deadline; collect envelope types; require at least one each of
   `turn_state`, `assistant_delta`, `tool_use`, `turn_end` (and ideally
   `tool_result`), each decrypting cleanly with `ConversationID == knownConvID`;
   assert **zero** `TypeMessage` (no coarse to the interactive phone).
8. **Assert B (positive + negative):** B receives the coarse `message` (assistant
   role, `knownConvID`, valid-UUID message-id, `InReplyTo == nil`) and **zero**
   structured envelopes. B's coarse comes from the spinner `✻` chunk (TUI mode →
   PTY → bridge → coarse emitter → `!interactive` conns). If a cleaner marker is
   wanted, also pass `PYRY_FAKE_CLAUDE_ASSISTANT_TRIGGER` and re-arm it in a
   background filesystem-only goroutine (the #634 pattern) — optional.

### Vacuous-pass guard (security-relevant; AC requirement)

The "B never receives a structured envelope" negative is **meaningless unless
the structured path is live and observed on A in the same run.** Therefore the A
assertion (step 7) is a **hard precondition** of the B negative (step 8): the
test must `t.Fatal` if A received **zero** structured envelopes — that signals
the harness produced no structured events at all, which would make B's negative
vacuously true. Order the assertions so A's positive runs first and fails loudly
on an empty structured set. (This is the architect-owned guard the
`security-sensitive` label requires; see § Security review.)

## Concurrency / timing model

No new production goroutines (test-only ticket). Three pre-existing goroutines
matter:

- **Producer Run goroutine** subscribes **once at relay startup** (`WaitForPTY`
  returns as soon as fakeclaude is live; `resolve` captures `startOffset =
  size-after-{}\n = 3`; `Events` tails from there). The JSONL trigger fires only
  in step 6 — after pairing + handshake + ack, i.e. **seconds** after startup —
  so subscription strictly precedes the append and every appended line is
  tailed. This is the same "supervisor is up" assumption #634 relies on; no
  explicit barrier is needed.
- **Cursor stamp** happens synchronously at the top of `WriteUserTurn` (under
  `convMu`, before delivery). Awaiting A's ack (step 5) is the deterministic
  fence guaranteeing the stamp precedes the trigger drop (step 6) → the
  producer's `Handle` (on its own goroutine, reads the cursor under `convMu`)
  always sees `knownConvID`.
- **fakephone read discipline (603.md / 634.md):** fakephone closes the WS on a
  timed-out `Receive`, so each phone read loop uses a **single long deadline**
  and reads frames back-to-back; every binary→phone frame is decrypted in
  capture order (sequential receive nonce) and filtered at the **post-decrypt
  envelope-Type level**. Never use a short-timeout poll on a phone connection.

Timing budget: with TUI mode the ack is sub-second (no 30 s `WaitReady` floor —
that floor was #634's *non*-TUI artifact). A ~10–15 s read deadline per phone is
ample. The structured envelopes for A arrive within a poll tick of the trigger
drop (50 ms tail cadence + coalesce window ≤250 ms).

## Error handling

- Producer build/run failures fail soft in production (already handled); in the
  test they surface as A receiving no structured envelopes → the vacuous-pass
  guard `t.Fatal`s loudly.
- A handshake/seal/decrypt error in the test is a `t.Fatalf` (consistent with
  #634).
- The `send_message` ack is **awaited** (TUI mode makes it prompt); a missing
  ack within the deadline is a test failure, not a 30 s tolerance.

## Testing strategy

One new `//go:build e2e` test, `TestTwoPhoneStructured_InteractiveReceivesStream`
(name at developer's discretion), asserting all four ACs in a single run:

- **AC1 (interactive receives structured):** A decrypts ≥1 each of `turn_state`,
  `assistant_delta`, `tool_use`, `turn_end` under its session keys, all with
  `ConversationID == knownConvID`.
- **AC2 / AC3 (mutual exclusivity, live):** A receives **zero** `TypeMessage`; B
  receives the coarse `TypeMessage` and **zero** structured envelopes.
- **AC4 (no app output logged):** inherited from #589; the production emitters
  already log only content-free discriminants (interactive_turn_v2.go:60-66) —
  no new logging is added, so this holds by construction. No assertion needed
  beyond not introducing a logging call.
- **Vacuous-pass guard:** A's structured set is asserted non-empty *before* B's
  negative, with a dedicated `t.Fatal` message naming the harness-produced-nothing
  failure mode.

Gates the developer must run green: `go test -race ./...`, `go vet ./...`,
`staticcheck ./...`, `make substrate-guard`, `go build ./cmd/pyry`, and
`go test -tags=e2e -run TestTwoPhoneStructured ./internal/e2e/...` (ideally
`-count=3` for determinism, per #603). The realclaude e2e column is unaffected
(this ticket adds nothing to that suite).

## Scope (S confirmed)

- **Production source files (`.go`, non-test):** **1** —
  `internal/e2e/internal/fakeclaude/main.go` (~20 LOC: one env const, one helper,
  one call). Far under the §4 ≥5-file gate.
- **New files:** **1** — `internal/e2e/relay_two_phone_structured_test.go` (the
  fixture is written inline to the trigger file; no separate fixture file).
  Under the >3 gate.
- **Total LOC:** ~20 (fakeclaude) + ~260 (test) ≈ **280**. Under ~600.
- **New exported types:** 0. **Consumer call sites updated:** 0 (purely
  additive — no signature change, no refactor cascade). **State-machine reject
  branches:** 0.
- **No harness.go change** (the `extraEnv` seam already exists).

All red lines clear with margin. **Single ticket — no carve, no split.** The
foundational build the ticket warned about (option a) is rejected; option b is a
bounded extension of the existing suite.

## Open questions

- **B's coarse marker source:** spinner `✻` (zero extra wiring) vs.
  `PYRY_FAKE_CLAUDE_ASSISTANT_TRIGGER` with a known marker (cleaner assertion,
  one extra trigger + re-arm goroutine). Spec recommends the spinner for
  minimalism; developer may upgrade to the assistant trigger if B's coarse proves
  hard to assert cleanly. Either satisfies AC2/AC3.
- **Inline fixture vs committed `testdata/` fixture:** spec recommends inline
  (one fewer file). If the developer finds a committed curated subset of
  `clean.jsonl` more faithful to "captured transcript," that is acceptable and
  adds one data file (still well within scope).
- **Producer subscription barrier:** none is specified (subscription-at-startup
  precedes the post-ack trigger by seconds). If CI ever shows a race, the
  fallback is a bounded re-arm of the JSONL trigger that tolerates duplicate
  structured events (A asserts ≥1 of each type, so dupes are harmless) — note
  this in the test comment rather than building it preemptively.

## Security review

**Verdict:** PASS

This is a **test/harness-only** ticket; the dual-path production code already
shipped (#632/#633/#634). The security value is that the test is the **live
oracle** for the guarantee "a non-`interactive` phone never receives the
structured stream on the internet-exposed mobile surface" (#607's deferred
gated-fan-out review). The adversarial question driving this pass is therefore
not "does this code introduce a vuln" but **"could this test PASS while the
guarantee is broken?"**

**Findings:**

- [Trust boundaries] **SHOULD FIX (folded into the spec, step 4).** The boundary
  being proven is the #626 set-once `c.Interactive` flag + the complementary
  #632/#634 filters. The spec pins A's grant (the existing
  `driveHandshakeToOpenDaemonInteractive` hello_ack assertion) and now also pins
  **B's non-grant** (assert B's hello_ack lacks `CapabilityInteractive`). A
  mis-granted B fails loud regardless (B would receive structured → fatal), so
  this is robustness, not a correctness gate — but it makes the negative airtight
  at both ends of the gate.
- [Vacuous pass — the headline requirement] **No MUST FIX.** The
  architect-owned guard is specified: A's structured set is asserted **non-empty
  before** B's negative, with a dedicated fatal naming the
  harness-produced-nothing failure mode. The structured path must be live and
  observed on A in the same run for B's "no structured" to mean anything (AC
  Technical Note). Without the live producer (sessions-dir alignment +
  fakeclaude structured emission, both in the design), A's set is empty and the
  test fails — so a vacuous pass is structurally precluded.
- [Error messages, logs, telemetry — AC4] **No MUST FIX.** AC4 ("application
  output NEVER logged at any level") is about the **daemon's** logger. The
  production emitter logs only content-free discriminants
  (`interactive_turn_v2.go:60-66` SECURITY block; fields: event, kind,
  conversation_id, turn_id, env_id, conn_id, transport err — never text). No new
  production logging is added; the fakeclaude trigger logs nothing (errors
  silenced). The test's failure diagnostics (`t.Fatalf` with a payload, as in
  #634) are test-scope output of synthetic fixture content, not daemon logging —
  consistent with #634's reviewed posture.
- [File operations] **No findings.** The JSONL trigger path is **test-controlled**
  (a temp file), never network/phone-controlled — no path traversal, no
  attacker-controlled TOCTOU (same shape as the accepted `emitAssistantIfTriggered`).
  The session JSONL is `0o600`; fakeclaude appends complete `\n`-terminated lines
  + `f.Sync()`, and tui-driver's `TailJSONL` reassembles partial lines, so no
  partial-line parse leaks.
- [Cryptographic primitives] **No findings.** The test exercises the shipped
  Noise_IK path (#433); no new crypto. Each phone decrypts with its **own**
  session `CipherState` (`recvA` for A, `recvB` for B) — a key mix-up would fail
  decryption loudly, so the test incidentally re-confirms per-phone key
  isolation. Decrypt-drain respects the sequential receive nonce (603.md).
- [Subprocess / external command] **N/A.** No new subprocess; fakeclaude is the
  shipped stand-in spawned by the daemon. No `sh -c`, no user-controlled exec
  args.
- [Tokens / secrets] **N/A.** Pairing tokens flow through the shipped handshake
  unchanged; the fixture carries only synthetic assistant text — no secret
  material.
- [Network & I/O] **No findings.** The fixture is size-capped (mirrors
  `assistantMaxBytes`); phone reads are single-deadline bounded (603.md). The
  test is a client of the shipped server, not a server.
- [Concurrency] **No findings (one developer note).** No new production
  goroutines. If the optional B-coarse re-arm goroutine is used, it MUST be
  filesystem-only and never touch a phone connection (603.md/634.md) — noted in
  the design. The cursor is stamped and read under `convMu`; the ack fences the
  stamp before the trigger drop — no TOCTOU.
- [Threat model alignment] **No findings.** Addresses protocol-mobile.md
  § Security model's capability-isolation threat (non-granted phone must not
  receive granted-only data) as the **live** confirmation; the deterministic
  oracle remains with #634/#632. Per-conversation subscription routing stays
  **OUT OF SCOPE** (coarse fan-out reaches all non-interactive conns — inherited
  #589/#634 deferral to pyrycode-mobile#336).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-08
