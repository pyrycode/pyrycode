# Spec #854 — Interactive bootstrap liveness: fix fresh-daemon deadlock + real-claude two-turn e2e

**Ticket:** #854 · **Size:** S · **Label:** `security-sensitive` (architect-stage security review below is mandatory)

**Method:** test-first (RED → GREEN). The fix and the test ship in **one PR** — the AC "fails on
current `main`, passes after the fix" makes them inseparable (a test-first split ships a red
pre-ship gate; a fix-first split makes "fails on main" unverifiable). Do **not** split fix from
test. If the *fix itself* turns out to exceed S (see § Decision), split **within the fix** and
route back to PO — the test cannot be separated.

> **Re-validated 2026-07-09 (unblock after #860 + #861 merged).** This ticket was blocked by #860
> (the shared-harness `sun_path` socket-path fix), reset to Backlog, and re-runs now that #860 and
> #861 (`seedBootstrapRegistry`) have landed on `main`. Re-verification against current `main`:
> - **Deadlock still present (fix not vacuous):** `resolveOwnBootstrapJSONL` still returns
>   `"bootstrap child pid %d has no jsonl open yet"` and retries forever
>   (`cmd/pyry/interactive_turn_stream_v2.go:329`). No other ticket quietly fixed it.
> - **Transcription sources inherit the shared fixes (the "green shared ≠ green copy" trap).**
>   #854's real-claude test *transcribes* the daemon harness (build-tag-fenced, not imported), so a
>   green shared harness does **not** imply a green copy. The daemon spawn **must** route through the
>   `spawnWith` → `shortSocketPath` pattern (§ Files to read first, § Test step 2), or the control
>   socket won't bind on macOS and RED never reaches the deadlock it exists to observe. This
>   re-validation re-anchored every stale line number and folded #861's `seedBootstrapRegistry` into
>   the binding step (§ Test step 4).
> - **File-overlap check re-run clean; design unchanged** (§ File-overlap check). Approach A stands;
>   the security verdict stays **PASS** — #860/#861 touch only the test harness, no production trust
>   boundary (§ Security review).

---

## Files to read first

Read these before writing anything. The first block is the fix surface; the second is the test
surface (mostly transcription sources).

**Fix surface (production):**

- `cmd/pyry/interactive_turn_stream_v2.go:290-490` — `resolveOwnBootstrapJSONL` (the pid-probe
  bootstrap resolver, `func` at :295; logs `bootstrap child pid <n> has no jsonl open yet` at
  :329 and **retries**; confidentiality guard at :331-346), `resolveTarget` (:432, selects the
  bootstrap resolver vs the by-id `resolveBoundSessionJSONL` at :478). **This is the outbound-leg
  core.**
- `internal/supervisor/supervisor.go:280-455` — `WriteUserTurn` → `deliverViaSession` →
  `confirmViaTranscriptGrowth` → `grew`. The inbound-leg delivery + growth-confirm. Note the
  baseline `("", 0, nil)` path (empty session, no transcript) and that a non-nil baseline error
  diverts to the Committed-chip fallback.
- `internal/sessions/reconcile.go:185-230` — `newProbePreferredTranscriptResolver` (`func` at
  :198; the growth-confirm baseline resolver). No-baseline conditions return `("", 0, nil)` —
  never an error, never mtime (the inverted-convention doc comment is at :185-197).
- `internal/sessions/pool.go:398-490` — bootstrap `supervisor.Config` wiring: `ClaudeArgs`,
  `ResumeLast`, `ResolveTranscript = newProbePreferredTranscriptResolver`, the late-bound `pidFn`
  reading `bootstrapSup.State().ChildPID`. **Confirms the bootstrap spawns with no `--session-id`.**
- `cmd/pyry/main.go:648-806` — `runSupervisor`: bootstrap spawn args (`ClaudeArgs: claudeArgs`,
  `ResumeLast: *resume`, no `--session-id`), `confineWorkdirToHome`/`trustMark`, `boundHost`.
- `cmd/pyry/main.go:974-1036` — `sessionRouter.resolve`/`Route`, `errNoBoundSession`. The
  **~20 ms reject source**: an unbound conversation (`CurrentSessionID == ""`) → `errNoBoundSession`
  → `server.binary_offline`, before any enqueue.
- `internal/relay/handlers/send_message.go` — ack-on-enqueue (#721): Route is validated
  **synchronously** pre-enqueue, so a Route reject is the ~20 ms error frame; a post-ack drain
  failure is *not* (it's held + retried).
- `cmd/pyry/relay.go:461-470` — where `startInteractiveTurnStreamV2` is wired (:469), behind
  `bridge != nil && claudeSessionsDir != ""`, with `probe := newBootstrapProbe(logger)` and
  `pidFn := func() int { return sup.State().ChildPID }`. **The structured reply stream is
  v2-only** (this is inside `startRelayV2`); the v1 leg uses the coarse `startAssistantTurnBridge`.

**Test surface (transcription sources — package `e2e` is build-tag/​package fenced from
`realclaude`, so *duplicate*, don't share — the same call the realclaude suite already made for
`ensurePyryBuilt`):**

- `internal/e2e/relay_v2_daemon_test.go` (whole file) — the canonical daemon+relay+v2-phone
  round-trip. Transcribe: `driveHandshakeToOpenDaemon`, `waitBinaryHello`, `decryptInnerEnvelope`,
  and the pair→dial→handshake→send→receive shape. Helpers it calls that also need transcribing:
  `decodePairPayload`, `readPersistedServerID`, `sendNoiseInit`, `sendNoiseMsg`, `readInnerFrame`,
  `buildHelloEarly`, `mustJSON`, `relayTestLogger` (grep `internal/e2e/relay_v2_handshake_test.go`
  and `internal/e2e/relay_test.go` for their bodies).
- `internal/e2e/harness.go` — the daemon-spawn + readiness + teardown to transcribe into a
  realclaude helper, adapting the child binary to **real** `claude` and HOME to the
  authenticated-worktree pattern below. **Anchor on the current post-#860/#861 helpers, not the
  pre-fix shape:**
  - `spawnWith` (:516-549) — the **shared spawn core**. It binds the control socket via
    `shortSocketPath(t)` (:529), **not** `filepath.Join(home, "pyry.sock")`. **This routing is
    the #860 fix and is load-bearing for RED** (§ Test step 2): a `<home>/pyry.sock` under a long
    `t.TempDir()` HOME overflows macOS's 104-byte `sun_path` limit, `bind(2)` returns `EINVAL`, the
    daemon never reaches readiness, and RED can never observe the deadlock it exists to catch.
    Transcribe the daemon spawn **through this pattern** (call `shortSocketPath` yourself), never a
    hand-rolled home-relative socket. `StartInWithEnv` (:228) and `StartRotationWithRelay` (:319)
    are the two spawn templates — the latter is the closest analog (daemon + relay + seeded binding).
  - `shortSocketPath` (:477-494) — the #860 helper itself; transcribe verbatim (`os.MkdirTemp("/tmp",
    …)` + `t.Cleanup`).
  - `waitForReady` / `teardown` — readiness poll (control-socket dial) and SIGTERM→grace→SIGKILL
    teardown; register `teardown` on `t.Cleanup`.
- `internal/e2e/harness.go:385-410` — `seedBootstrapRegistry` (#861). Writes `sessions.json` with a
  bootstrap entry at a **chosen** uuid so `Pool.New` warm-starts the bootstrap **pool id** at that
  uuid deterministically, without the read-back-after-startup dance. **Prefer this** over reading
  the id from `sessions.json` post-startup (§ Test step 4). Real claude still mints its *own*
  on-disk transcript uuid (no `--session-id` — that's approach B), so pool-id ≠ disk-uuid — which is
  exactly why the outbound leg needs the PID probe, not the by-id resolver (§ The fix, branch 2).
- `internal/e2e/harness.go:364-383` — `seedBoundConversation`. Writes `conversations.json` binding
  the driving conversation to a session id; **load-bearing under #678** — `sessionRouter.Route`
  rejects an empty `current_session_id` before any pool `Lookup`, so an unbound row yields a
  retryable `server.binary_offline` instead of reaching `WriteUserTurn`. Bind to the same uuid
  seeded via `seedBootstrapRegistry`.
- `internal/e2e/realclaude/fixtures.go` — **reuse as-is**: `WithWorktreeAuthenticated` (creds +
  `~/.claude.json` seed so PTY claude skips onboarding), `ensurePyryBuilt`, `buildEnvWithRealHome`,
  `realHome`. The daemon spawn must build pyry with real HOME but run it with the isolated HOME
  (same split the file already documents).
- `internal/e2e/internal/fakerelay/fakerelay.go` and `internal/e2e/internal/fakephone/fakephone.go`
  — **import directly** (plain untagged packages, no duplication). `fakerelay.New`, `.URL()`,
  `.WaitBinary`; `fakephone.Dial`, `.Send`, `.Receive`, `.Close`.
- `internal/e2e/relay_send_message_test.go` — the `send_message → ack → assistant stream` flow to
  mirror for each of the two turns.
- `internal/e2e/realclaude/smoke_test.go` — the realclaude idiom (build tag `e2e_realclaude`,
  `exec.LookPath("claude")` guard).
- `docs/knowledge/features/mobile-live-e2e-runbook.md` — the **manual** operator repro this test
  automates. § "Why `-pyry-workdir=/tmp` with no seed spins forever" is the exact deadlock; step 3
  (seed conversation) is the routing prerequisite; the closing paragraph literally scopes this
  ticket ("A future slice could lift #642's recipe into a real-claude + relay live e2e").
- `Makefile:29-31` — `e2e-realclaude` already runs `./internal/e2e/realclaude/...`. **The new test
  lands in the gate automatically; no Makefile change (AC5 is satisfied by file placement).**

---

## Context

The daemon's **interactive bootstrap session** — the "main" session Desktop/Mobile drive over the
relay, addressed via the `convID == ""` / bootstrap branch — never answers a real send on a fresh
daemon.

**Root cause (from the ticket, live-confirmed 2026-07-08).** The bootstrap claude spawns as a
brand-new empty session: passthrough `ClaudeArgs` only, `ResumeLast` has nothing to `--continue`
on the first spawn, and there is no `--session-id` (`internal/sessions/pool.go:398-413`,
`cmd/pyry/main.go:748-755`). Claude writes its `<uuid>.jsonl` **only on the first turn**, so a
truly fresh daemon has **no transcript on disk**. Two legs both stall on that absence:

- **Outbound (the reply stream, v2-only).** `resolveOwnBootstrapJSONL` deliberately tails the
  file the daemon's *own* child holds open (probe by PID), retrying rather than falling back to
  mtime (#827/#838). With no first turn there is no open `.jsonl` fd, so it logs
  `bootstrap child pid <n> has no jsonl open yet` and retries forever — observed looping *before
  any client message*.
- **Inbound (the send).** The client send is rejected with an error frame ~20 ms after the send —
  a synchronous `sessionRouter.Route` reject (`errNoBoundSession` → `server.binary_offline`),
  before any enqueue.

The manual workaround today (`mobile-live-e2e-runbook.md`) is "run `claude` once in the workdir
first so a transcript pre-exists, then seed a conversation" — an operator ritual, not a product
fix. **This is not a #827 regression:** the pre-#827 mtime resolver also failed, by binding to a
*different* claude's newest transcript in the shared vault folder (#828 territory). Neither path
has ever produced a working real-claude interactive round-trip.

**Why it matters / why a real-claude test.** Every existing client e2e runs against a **fake**
daemon (fakeclaude writes its transcript immediately and always answers), so the suites stay green
while the real daemon deadlocks. The bug is a **real-claude timing property** (transcript created
lazily on the first turn, with real API/PTY latency) that fakeclaude structurally masks. There is
no CI in the org, so the test earns its keep only in the operator's pre-ship `make e2e-realclaude`
gate.

---

## Decision: Approach A (inject the first message), **not** approach B (`--session-id`)

The ticket offers two approaches. **Choose A.**

- **A (chosen) — inject the first message so the child starts its turn and creates the transcript,
  then bind by PID via the existing probe.** The first *client* send (there is no daemon-side
  auto-priming — "the first message" is the user's real send) travels the existing guarded
  delivery path (`Supervisor.WriteUserTurn` → tui-driver `DeliverPrompt`), which creates the
  transcript. The already-present pid-probe machinery
  (`resolveOwnBootstrapJSONL` / `newProbePreferredTranscriptResolver`) then binds, converging the
  "no jsonl yet" wait (AC #2) instead of rejecting.

- **B (rejected) — spawn with `--session-id <uuid>` + private per-cwd workdir, wait on the known
  path.** B is *exactly* **#839** ("Deterministic bootstrap `--session-id`: retire `--continue`
  restart heuristic and startup adopt-by-mtime"), a separate **OPEN** ticket that owns this
  redesign and touches the same core files (`interactive_turn_stream_v2.go`, `reconcile.go`, the
  bootstrap spawn). Choosing B would duplicate #839's charter, and it is the larger change (it
  re-plumbs the whole bootstrap resume/rotation lifecycle — memory: "removing adopt-by-mtime breaks
  ~11 e2e"). #839 currently has **no remote branch** (`origin/feature/839` was deleted on its own
  reset), so there is no file-overlap block to raise for #854 (§ File-overlap check below), but the
  design overlap is decisive against B: this ticket must **not** pre-empt #839's `--session-id`
  work. Approach A converges the fresh-daemon deadlock without pinning the uuid, leaving #839's
  redesign untouched.

**Security corollary of A:** A reuses the existing send path verbatim, so it opens **no new
inject bypass**. Untrusted client content still reaches the child only through
`WriteUserTurn` → `DeliverPrompt` (tui-driver's bracketed-paste + control-byte guarding). That is
the property the ticket's security note demands ("reuse the existing send path's
control-byte/bracketed-paste guard, don't open a raw-inject bypass"). See § Security review.

---

## Design

### RED first (non-negotiable, and the AC)

1. Build the real-claude harness (§ Test) and the two-turn liveness test.
2. Run it against **current `main`** and capture the failure in the PR: the `bootstrap child …
   has no jsonl open yet` loop and/or the ~20 ms error-frame reject / turn timeout. This is AC #4.
3. Only then implement the fix and watch it pass.

RED is where the **exact** failing leg is pinned. Static reading narrows it to the two legs above;
the real-claude run tells you which one the client actually hits and therefore where the minimal
change lands. The decision tree in § "The fix" covers both.

### The fix (approach A) — a convergence change, kept minimal

The contract to satisfy (AC #1, AC #2):

> On a fresh daemon, a well-formed client send reaches claude, creates the transcript, and the
> streamed assistant reply flows back. "Child has no transcript yet" is a **bounded, converging
> waiting state** — not an unbounded reject and not an error frame — that resolves the moment the
> first turn lands.

The pieces that already behave correctly (do **not** rebuild them):

- `resolveOwnBootstrapJSONL` already *retries* on "no jsonl yet" (returns a not-found error → the
  subscriber re-subscribes in `subscribeRetryDelay`). Once the transcript exists, the probe binds.
  The outbound leg's convergence is therefore mostly a matter of the first turn actually landing.
- `confirmViaTranscriptGrowth` already handles the empty baseline: `("", 0, nil)` → deliver →
  poll → the fresh file appearing is `grew(basePath="", …) == true` → commit confirmed.

So the fix is the **narrow gap** that stops the first turn from landing / the reply from binding on
a *truly fresh* bootstrap. Pin it in RED, then apply the matching branch:

- **If RED shows the send is rejected at `Route` (`errNoBoundSession` / `server.binary_offline`,
  ~20 ms):** the bootstrap is not a routable bound conversation on a fresh daemon. The fix makes
  the fresh bootstrap **routable before its transcript exists** — e.g. ensuring the driving
  conversation binds to the live bootstrap session id (`Pool.Default().ID()`) so `Route` resolves
  to the bootstrap supervisor and the send reaches `WriteUserTurn`. Keep the `errNoBoundSession`
  guard intact for *other* conversations (the #678 AC#4 isolation property) — the change is to
  make the bootstrap's own binding present/valid on a fresh daemon, not to relax the guard.
- **If RED shows the send reaches `WriteUserTurn` but the reply never streams (outbound leg
  deadlocks under a non-empty active cursor):** `resolveTarget` is selecting
  `resolveBoundSessionJSONL(dir, bootstrapId)` for a conversation bound to the bootstrap, but the
  bootstrap claude's on-disk uuid ≠ its pool id (it spawned without `--session-id`), so that fixed
  path never appears. The fix routes a **bootstrap-bound** active conversation through the
  pid-probe resolver (`resolveOwnBootstrapJSONL`) instead of the by-id resolver — "bind by PID via
  the existing probe", verbatim from approach A. Identify the bootstrap-bound case in `resolveTarget`
  / `boundHost` by comparing the resolved session id against `Pool.Default().ID()`.
- **If RED shows the growth-confirm times out on a genuinely-empty first turn with real claude:**
  the delivery landed but `confirmViaTranscriptGrowth` did not observe growth within
  `transcriptConfirmTimeout`. Confirm the baseline resolver returns `("", 0, nil)` (not an error)
  for the empty session and that the poll observes the newly-created file; adjust only the
  convergence timing/observation, not the guard contract.

**Boundaries (all three branches share these):**

- Reuse `Supervisor.WriteUserTurn` / `DeliverPrompt` — **no new inject path.**
- Preserve every confidentiality guard in `resolveOwnBootstrapJSONL` (probed path must live in the
  canonical sessions dir with a UUID stem) and the `("", 0, nil)` no-baseline convention in
  `newProbePreferredTranscriptResolver` (a non-nil baseline error silently reintroduces the
  #668 false-ack risk — see that resolver's doc comment).
- Do **not** switch the bootstrap to `--session-id` (that is #839; would blow S and collide).
- **Escape hatch (per the ticket's sizing note):** if RED reveals the only correct fix *requires*
  the deterministic `--session-id` refactor (i.e. the bootstrap-bound routing/rotation can't be
  made to converge without pinning the uuid), stop — do not implement it here. Post the finding and
  add `needs-rework:po`; the fix belongs with / behind #839. The production change for A should be
  a bounded edit to `cmd/pyry/interactive_turn_stream_v2.go` (± `cmd/pyry/main.go` /
  `internal/sessions/pool.go` wiring), well under the ≥5-production-file gate.

### The test — real-claude interactive two-turn liveness

New file(s) under `internal/e2e/realclaude/` (build tag `//go:build e2e_realclaude`). It stands up
the real interactive stack and drives two turns over the encrypted v2 wire. **v2 is mandatory** —
the structured reply stream (`startInteractiveTurnStreamV2`, the deadlocking producer) exists only
on the v2 leg; a v1 test would not exercise `resolveOwnBootstrapJSONL` at all.

Harness (transcribe from the cited `e2e` sources; the plain `fakerelay`/`fakephone` packages are
imported directly):

1. **Guard + skip.** `exec.LookPath("claude")` (as `smoke_test.go`); `WithWorktreeAuthenticated`
   for the isolated authenticated HOME (skips cleanly when no creds — this is why the gate is
   operator-run, not CI).
2. **Fresh daemon, isolated workdir.** Spawn real pyry (`ensurePyryBuilt`) with
   `-pyry-socket=<shortSocketPath(t)>` (**not** `<home>/pyry.sock` — the #860 fix; see below),
   `-pyry-claude=claude`, `-pyry-workdir=<isolated W under the authenticated HOME>`,
   `-pyry-relay=<fakerelay>/v2/server`, env `PYRY_ALLOW_INSECURE_RELAY=1`, `PYRY_MOBILE_V2=1`;
   build with real HOME, run with the isolated HOME (`buildEnvWithRealHome` split). The isolated
   workdir is **load-bearing twice**: it guarantees the fresh-daemon state (empty sessions dir)
   *and* sidesteps the shared-session-folder collision (#828) by construction. Wait for the
   control socket (transcribe `waitForReady`) and for the binary's relay registration
   (`fakerelay.WaitBinary`).
   - **Socket path is load-bearing for RED (#860).** The authenticated HOME is a long
     `t.TempDir()`; a `<home>/pyry.sock` overflows macOS's 104-byte `sun_path` limit, `bind(2)`
     returns `EINVAL`, and the daemon never becomes ready — so the RED run dies *before* the
     deadlock. Route the socket through the shared harness's `shortSocketPath(t)`
     (`harness.go:477-494`) exactly as `spawnWith` does (`:529`). The prior developer run died on
     precisely this bug; the fix is on `main` in the *shared* harness but the realclaude test is a
     *transcription*, so it must copy the fixed pattern, not the pre-fix one.
3. **Pair + handshake.** `pyry pair` → decode payload → `fakephone.Dial` → drive the Noise_IK
   handshake to open (transcribe `driveHandshakeToOpenDaemon`).
4. **Bind the driving conversation** to the bootstrap so `send_message` routes to it. **Prefer the
   deterministic #861 seed pair** over reading the id back after startup (mirrors
   `StartRotationWithRelay`):
   - `seedBootstrapRegistry(home, chosenUUID)` **before** the daemon starts (`harness.go:385-410`)
     — pins the bootstrap **pool id** to `chosenUUID` (no post-startup read-back race).
   - `seedBoundConversation(home, convID, chosenUUID)` (`harness.go:364-383`) — binds the driving
     conversation to that id so `sessionRouter.Route` resolves to the bootstrap supervisor (#678).
   Both seeds write `~/.pyry/<name>/...` and must exist before spawn (the daemon loads the
   registry once at startup). **Real claude still writes its transcript at its own minted uuid**
   (no `--session-id`), so `chosenUUID` (pool id) ≠ the on-disk transcript stem — this mismatch is
   *the point*: it is why the outbound reply must bind by PID probe, not by the bound uuid
   (§ The fix, branch 2), and it makes the fix's `Pool.Default().ID()` identity check clean
   (`conversation.bound == chosenUUID == bootstrap pool id`). If the chosen fix branch removes the
   need to pre-seed the binding, the test asserts against whatever the fix makes routable — keep
   the test honest to the *shipped* behaviour (§ Open questions #2).

Two turns (content-agnostic — **liveness only**, never assert on claude's words; that would be
non-deterministic and risks the substrate guard):

- **Turn 1** — send a random message (vary it per run so nothing caches). Assert **any** non-empty
  streamed assistant response within a generous timeout (real claude on `--model haiku`; budget
  tens of seconds, not the fakeclaude milliseconds). *Proves the child creates its transcript and
  the bridge binds.*
- **Turn 2** — send a second random message. Assert a **second** non-empty streamed response.
  *Proves the bridge survives once the session exists — the rotation/offset path past the first
  turn.*

"Streamed assistant response" = decrypt the binary→phone `noise_msg` frames (in receive order —
the receive nonce is sequential) and observe a non-empty assistant turn envelope for that turn
(mirror `relay_send_message_test.go` / `decryptInnerEnvelope`). Assert on **presence + non-empty
+ correct turn correlation**, not text.

Randomness note: scripts/tests cannot use `Math.random`-style entropy freely here; derive the
per-turn message from a source that varies per run (e.g. `time`/`t.Name()` + a counter) so Turn 2
differs from Turn 1 and reruns differ — enough to defeat any accidental caching, without asserting
content.

---

## Concurrency model

No new goroutines in the fix. The bootstrap supervisor, the msgqueue drain, and the single
`Producer.Run` goroutine (which invokes the resolver closures — the single-run-goroutine invariant
the resolvers rely on, no mutex) are unchanged. The fix touches only *which resolver closure* is
selected and/or *whether the fresh bootstrap is routable*; it adds no shared mutable state.

Test concurrency: `fakephone` permits one concurrent reader + one writer; the two turns are
sequential (send, drain reply, send, drain reply) so there is no reader/writer contention. Daemon
teardown is SIGTERM→grace→SIGKILL via the transcribed `teardown`, registered on `t.Cleanup`.

---

## Error handling

- **Inbound.** Preserve the reliable-delivery contract: `ErrNoLiveSession` / `ErrTurnNotCommitted`
  / wrapped `context.DeadlineExceeded` all still surface as retryable failures on their existing
  paths. The fix must not turn a real failure into a false ack, and must not turn the transient
  "no transcript yet" into a hard reject frame — that is the AC #2 flip (reject → converging wait).
- **Outbound.** `resolveOwnBootstrapJSONL` keeps returning a not-found *error* on "no jsonl yet"
  (its subscriber retries on error); `newProbePreferredTranscriptResolver` keeps returning
  `("", 0, nil)` on no-baseline (its consumer must *not* see an error). Do not cross these
  conventions — they are inverted on purpose (documented at `reconcile.go:185-197`).
- **Test.** Structural failures (build, spawn, dial, handshake, decrypt) → `t.Fatalf` with context.
  A turn producing no non-empty reply within the timeout → `t.Fatalf` (that *is* the deadlock RED
  captures on `main`). Missing creds → `t.Skip` (via `WithWorktreeAuthenticated`).

---

## Testing strategy

- **The deliverable is the test** (real-claude, `make e2e-realclaude`). It is the RED/GREEN oracle:
  fails on `main`, passes after the fix (AC #4), and lives in the operator's pre-ship gate (AC #5,
  satisfied by placement under `internal/e2e/realclaude/`).
- **Unit coverage of the fix.** Whichever branch the fix lands in has an existing same-package
  unit test surface — extend it, don't invent a parallel one:
  - `cmd/pyry/interactive_turn_stream_v2_test.go` already covers `resolveOwnBootstrapJSONL` /
    `resolveTarget` (fake `rotation.Probe`, `t.TempDir()`). If the fix changes resolver selection
    for a bootstrap-bound conversation, add a table case there (probe injected, no live claude).
  - `internal/supervisor/supervisor_test.go` covers `confirmViaTranscriptGrowth` via the
    `deliverGrowthDeps` seam (scripted readiness/deliver/resolve). If the fix touches convergence,
    add a scripted "empty baseline → file appears" case there.
  - `internal/sessions/reconcile_test.go` covers `newProbePreferredTranscriptResolver`.
  These are `-race`-clean, stdlib-only, no live claude — the fast gate that guards the fix under
  ordinary `go test`.
- **Do not** weaken any existing fakeclaude e2e (they must stay green — they encode the
  already-working warm/seeded paths).

---

## Security review (mandatory — `security-sensitive` label)

Adversarial pass over the chosen design. Verdict: **PASS**, on the condition that the fix keeps the
boundaries below (they are also the § Design boundaries — restated here as the security contract).

**Trust boundary — untrusted client content into the child PTY.** The interactive relay path is
driven by untrusted mobile/desktop clients. Approach A pushes the client's message into the child,
but **only through the existing `Supervisor.WriteUserTurn` → tui-driver `DeliverPrompt` seam**
(`internal/supervisor/supervisor.go:280-336`), which applies bracketed-paste framing and
control-byte handling inside the tui-driver seal. **Enforcement:** the fix must introduce no raw
`pty.Write` / no alternate inject path for the first turn. A first-turn-priming shortcut that
bypassed `DeliverPrompt` would be an ESC/C0 breakout surface (memory:
[[architect-content-to-pty-paste-needs-control-byte-guard]]) — explicitly out of bounds. The
content-length ceiling stays the transport's 1 MiB WS read cap (`send_message.go` SECURITY note);
the fix adds no new content sink.

**Trust boundary — probe path → filesystem tail.** The fix keeps `resolveOwnBootstrapJSONL`'s
confidentiality guard: the probed path is canonicalised (`EvalSymlinks`) and required to live
directly in the trusted `claudeSessionsDir` with a UUID stem, so a PID-reuse race handing back an
unrelated process's fd is **rejected, not tailed** (`interactive_turn_stream_v2.go:331-346`). Any
new "route the bootstrap-bound conversation through the pid-probe" branch inherits this guard
unchanged — it selects the *same* guarded closure, adding no new path crossing.

**Trust boundary — workdir confinement.** The bootstrap workdir is already confined to `$HOME`
(`confineWorkdirToHome`) and pre-trusted (`trustMark`) at `cmd/pyry/main.go:688-695`. Approach A
does **not** change the spawn workdir (that confinement concern is approach B's, which we
rejected). The isolated test workdir also resolves under the authenticated HOME.

**No-false-ack invariant.** The fix must not report a turn committed that was not: preserve the
`newProbePreferredTranscriptResolver` `("", 0, nil)`-not-error convention (a non-nil baseline error
diverts `confirmViaTranscriptGrowth` to the stochastic Committed-chip fallback, reintroducing the
#668 false-ack the growth-confirm exists to kill). Restated: **do not signal no-baseline as an
error.**

**Isolation regression check (#678 AC#4).** `sessionRouter.resolve`'s `errNoBoundSession` guard
(fires before `Pool.Lookup`, because `Lookup("")` returns the bootstrap) must remain intact for
*non-bootstrap* conversations — an unbound arbitrary conversation must never silently route into the
shared bootstrap claude. If the fix makes the fresh **bootstrap's own** conversation routable, it
does so by giving that conversation a valid `current_session_id` (= the live bootstrap id), **not**
by relaxing the empty-binding guard. A reviewer must confirm the guard still rejects a genuinely
unbound foreign conversation after the change.

**No new wire surface / no new secret handling.** The fix adds no envelope type, no token/nonce
primitive, no logging of `payload.Text` (still never logged, per `send_message.go`). The test
handles a real bearer token + Noise static key only inside the isolated HOME, torn down with the
tempdir.

If, during implementation, any boundary above cannot be held (most plausibly: the only way to make
the bootstrap converge is to pin its uuid, i.e. drift into approach B), the verdict flips to FAIL
for *this* design — stop and route back per the § Design escape hatch rather than ship a widened
inject or a relaxed isolation guard.

---

## File-overlap check (§ 1.5)

`git fetch origin --prune` + branch scan re-run at re-validation time (2026-07-09). **Clean: no
remote `feature/<n>` branch touches the fix surface** (`cmd/pyry/interactive_turn_stream_v2.go`,
`internal/supervisor/supervisor.go`, `internal/sessions/pool.go`, `cmd/pyry/main.go`,
`internal/sessions/reconcile.go`, `cmd/pyry/relay.go`, `internal/e2e/harness.go`). `origin/feature/839`
**no longer exists** (deleted — the prior run's spec-only branch was cleaned up), so there is no
file-overlap edge to raise and no `blockedBy` required. The *design* overlap with #839 is handled
by choosing approach A — see § Decision — and stands independently of the branch's existence.

---

## Open questions (resolve during implementation, RED-informed)

1. **Which leg produces the ~20 ms reject** (routing vs delivery) — pinned by the RED run; drives
   which § Design branch the fix lands in.
2. **How the driving conversation binds to the bootstrap on a fresh daemon** — whether the fix
   makes the binding present (seed-free) or the test seeds it via the #861
   `seedBootstrapRegistry` + `seedBoundConversation` pair (§ Test step 4). Keep the test asserting
   the *shipped* behaviour, not a pre-imagined one.
3. **Turn-1 timeout budget for real claude on `--model haiku`** — start generous (tens of seconds,
   bounded by the test ctx) and tighten only if stable; do not couple it to fakeclaude timings.

---

## Scope / size self-check

- **Production source files touched (fix):** `cmd/pyry/interactive_turn_stream_v2.go` (primary);
  possibly `cmd/pyry/main.go` and/or `internal/sessions/pool.go` for wiring. **≤ 3**, well under
  the ≥5 gate. No new exported types expected; no consumer edit fan-out; no new state-machine
  reject branches.
- **Test files:** new `internal/e2e/realclaude/*_test.go` + a realclaude harness `.go` — excluded
  from the production-file count. Substantial (transcription of the v2-daemon harness) but **coupled
  to the fix and unsplittable** (per the ticket's resolved sizing note and
  [[po-tdd-fix-plus-red-green-test-is-one-ticket]]); test code does not push the split threshold.
- **No Makefile / no `docs/knowledge/codebase/<N>.md` deliverable** (the latter is documentation's
  job, post-merge).
- Holds **S**. The one live risk is the fix drifting into approach B under RED — the § Design
  escape hatch routes that back to PO rather than absorbing it here.
