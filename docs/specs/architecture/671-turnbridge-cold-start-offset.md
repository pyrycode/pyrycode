# Spec #671 — turn bridge drops the live assistant reply (cold-start tail offset)

**Ticket:** fix(daemon): turn bridge drops the assistant reply entry, so the mobile
reply never streams (#421 final blocker)
**Size:** S (in-repo; 1 production file, ~10-line logic change, + 1 test file)
**Label:** `security-sensitive` (the fix changes what content reaches an
internet-exposed phone — see § Security review)

---

## TL;DR — the fork is resolved: in-repo, not the extractor

The ticket hands a two-candidate fork (Technical Notes). **This spec resolves it to
candidate 2 (in-repo tail offset).** Candidate 1 (the `tuidriver.AssistantText`
extractor) is ruled out by direct evidence. Therefore:

- **No tui-driver change. No `go.mod` bump. The cross-repo contingency does NOT
  fire.** The fix is entirely in `cmd/pyry`.
- **AC-1's literal location is re-targeted.** AC-1 asks for a RED-before fixture in
  `internal/turnbridge/mapper_test.go` driving `mapEntry`. The diagnosis shows
  `mapEntry` already maps the reply shape correctly, so a mapper-test fixture would
  be **vacuous-green** (the exact failure mode the ticket's own Technical Notes warn
  against). The RED-before regression gate therefore lives in
  `cmd/pyry/interactive_turn_stream_v2_test.go`, against the resolver's offset. This
  is a within-fork re-interpretation the ticket explicitly authorizes ("whichever
  the captured shape shows" → candidate 2 = "the in-repo tail-offset
  (`resolveLatestSessionJSONL` / subscription)").

### Why the extractor is ruled out (three independent confirmations)

1. **Real claude 2.1.158 shape.** Inspecting this host's live interactive session
   JSONL (`~/.claude/projects/<cwd>/<uuid>.jsonl`, the exact write path the bridge
   tails) under claude **2.1.158** (the ticket's version): every assistant
   text-reply line is `{"type":"assistant","message":{"stop_reason":"end_turn",
   "content":[{"type":"text","text":"…"}]}}`. That is precisely the shape
   `tuidriver.AssistantText` extracts (`type=="text"`, string `"text"` field) and
   `mapEntry` turns into a `TextChunk`. The reply line **maps**; it does not drop.
2. **The fakeclaude structured e2e passes.** `TestTwoPhoneStructured_Interactive
   ReceivesStream` (`internal/e2e/relay_two_phone_structured_test.go`) drives the
   real production stack (tui-driver parse → turnbridge mapper → emitter → Noise →
   phone decrypt) and asserts phone A **receives** `assistant_delta`. If `mapEntry`
   dropped assistant text, that test would already be red. The mapper is proven.
3. **The in-repo race is self-documented.** The same e2e's setup comment
   (`relay_two_phone_structured_test.go:146-155`) describes the production bug
   verbatim and works around it: it *pre-creates* `<uuid>.jsonl` before the daemon
   starts "so the producer's first resolve succeeds immediately at a tiny offset …
   otherwise resolve returns 'no session jsonl found', and retries ~500 ms later — a
   retry that can land AFTER the post-ack fixture append and capture an EOF offset
   past the [reply]". Real claude does not get pre-created — it hits the race live.

---

## Context

After #670 (workspace pre-trust), pyrycode-mobile#421's rung-3 live e2e passes
connect → list → create → **send** (the user turn commits and renders). The only
failing step is the streamed assistant reply (e2e line 79, 90 s timeout). The daemon
logs `turnbridge: dropping unrepresentable event kind=9` in the reply window
(`producer.go:132`) and nothing streams to the phone.

The drop log is real but **the dropped `kind=9` lines are not the reply** — they are
the ordinary non-representable lines a turn also writes (the echoed `type:"user"`
prompt line, which `mapEntry` drops by design; claude session/init metadata; the
#668 inert transcript-growth line). The reply line itself is **never read**, because
the tail starts past it.

### Root cause (the cold-start offset race)

`resolveLatestSessionJSONL` (`cmd/pyry/interactive_turn_stream_v2.go:114`) returns
`startOffset = file size` — EOF at resolve time — so a (re)subscription streams only
*new* events and never replays a resumed transcript. `NewSessionSubscriber`
(`internal/turnbridge/producer.go:152-214`) subscribes once at relay startup, and on
a transient resolve failure it retries every `subscribeRetryDelay` (500 ms).

Timeline for a **fresh** relay session (the mobile#421 e2e: fresh `$HOME`, no prior
transcript; relay claude runs under `--continue`, which defers JSONL creation until
first input lands — see `WaitForSessionJSONL` doc in tui-driver `jsonl.go:39-57`):

1. Relay starts → producer subscribes → `WaitForPTY` returns (PTY up, no input yet).
2. `resolve()` #1 → dir is empty → `"no session jsonl found"` → subscriber logs
   `turnbridge: resolve session jsonl, retrying` and sleeps 500 ms.
3. Phone sends → claude creates `<uuid>.jsonl` and writes the user turn + the
   `"ping"` assistant reply (+ trailing lines).
4. `resolve()` #2 (next retry) → file now exists, `size = S` is **past the reply** →
   subscriber tails from `S` → the reply is skipped; only later non-reply lines (if
   any) are read and dropped at `producer.go:132`.

The deterministic fakeclaude harness never hits this because it pre-creates the file
(step 2 succeeds immediately at a tiny offset). Real claude exposes it.

---

## Design

### The fix — cold-start tails from the file's start, warm-resume keeps EOF

Discriminate two cases in the JSONL resolver:

- **Warm resume** — the session file already exists when the resolver first looks
  (e.g. a `--continue` transcript on disk). Keep `startOffset = size` so the prior
  transcript is not replayed to the phone. *Unchanged behavior.*
- **Cold start** — no session file exists when the resolver first looks; one appears
  on a later call. That file is a brand-new session, so there is no historical
  transcript to skip and the whole file *is* the current turn. Return
  `startOffset = 0` so the tail begins at the file's start and cannot race past an
  in-flight reply.

Offset 0 on a cold-start file is safe and bounded: the file is the *current* fresh
session only (a resumed transcript would already exist on disk → warm path), so the
phone receives the current turn from its start, not stale history. The session's
leading non-assistant lines (init/system metadata) map to drops in `mapEntry` and are
never emitted.

**Home of the discrimination.** Make the resolver closure stateful across calls: it
records whether it has yet returned a session file. The first file returned *after*
one or more "not found" results (and before any successful result) is a cold-start
file → offset 0; once any file has been returned, subsequent calls (rotations) return
`size` as today. The closure is invoked only from the single `Producer.Run`
goroutine (`NewSessionSubscriber` → `resolve` → `Producer.Run`), so the new state
needs no mutex — assert this single-goroutine invariant in the doc comment.

Contract sketch (signature unchanged; behavior delta only):

```
resolveLatestSessionJSONL(dir) -> func(ctx) (path string, startOffset int64, err error)
  // unchanged: scans dir, picks most-recently-modified <uuid>.jsonl
  // CHANGED: startOffset == 0 for the first file returned on a cold start
  //          (no file existed on an earlier call this resolver's lifetime),
  //          startOffset == size otherwise (warm resume / rotation).
```

This keeps the fix to one production file (`interactive_turn_stream_v2.go`) and one
test file. `NewSessionSubscriber` and `mapEntry` are untouched.

> Rejected alternative — per-attempt cold-start tracking inside
> `NewSessionSubscriber`. Conceptually the subscriber "owns the timeline," but the
> only seam there is `sess.Events` on a concrete `*tuidriver.Session`, which is not
> fakeable without a new interface, so the offset decision can't be unit-tested in
> isolation. The resolver, by contrast, already has a pure temp-dir test suite
> (`TestResolveLatestSessionJSONL_*`). Putting the discrimination in the resolver
> trades a small purity-doc update for a deterministic, seam-free regression test —
> the right call under Simplicity First. Note the scope boundary in the doc: this
> closure fixes the **first-subscribe** cold start (the observed defect). A /clear
> rotation mid-session re-uses the same closure instance (already warm → EOF); a
> post-/clear first-reply race is a *separate, unobserved* mode and is explicitly
> out of scope (Evidence-Based Fix Selection — do not build a defense for a failure
> that has not been observed). Capture it as an open question, not code.

### Optional diagnostic (recommended, not required)

Add one `log.Debug` on the subscribe success path in `NewSessionSubscriber` recording
the chosen `(path, startOffset)` (the retry-path warning already exists at
`producer.go:182`). This lets the operator confirm during the AC-3 live run that the
"resolve … retrying" path fired and the subscribe offset was 0 after the fix. If you
add it, it lands in `producer.go` (a 2nd production file, still well under the S
gate) and must log only the path + offset + a cold-start bool — never any JSONL
bytes (substrate seal; see § Security review).

---

## Files to read first

- `cmd/pyry/interactive_turn_stream_v2.go:98-155` — `resolveLatestSessionJSONL`; the
  `startOffset = size` semantics (lines 106-107, 147) and the purity doc to update.
  **This is the fix site.**
- `internal/turnbridge/producer.go:152-214` — `NewSessionSubscriber`; the retry loop
  (`subscribeRetryDelay`, the `resolve` → `WaitForSessionJSONL` → `sess.Events(path,
  off)` sequence). Confirms `off` flows straight to the tail; confirms single
  goroutine.
- `internal/turnbridge/mapper.go:45-75` — `mapEntry`; the `assistant` → `Assistant
  Text` → `TextChunk` branch. Read to see why a mapper-test fixture is vacuous-green
  (the reply shape already maps).
- `cmd/pyry/interactive_turn_stream_v2_test.go:76-155` —
  `TestResolveLatestSessionJSONL_*`; the temp-dir pattern (`writeJSONL`,
  `uuidA/B/C`) the new cold-start test mirrors. **Regression test goes here.**
- `internal/e2e/relay_two_phone_structured_test.go:126-160` — the pre-create
  workaround comment that documents the exact race; the structured-stream assertions
  that prove the mapper path. Read to understand both the bug and why the deterministic
  e2e masks it.
- `internal/e2e/internal/fakeclaude/main.go:193-216` — `emitStructuredJSONLIfTriggered`
  appends to an already-open file `f`; fakeclaude never reproduces "no file at
  subscribe," which is why CI is green while live is red.
- tui-driver (sibling checkout, read-only) `pkg/tuidriver/jsonl.go:307-337`
  (`AssistantText`) and `:39-78` (`WaitForSessionJSONL` — "claude defers JSONL
  creation until first input lands"). Context only; **do not modify tui-driver.**

---

## Concurrency model

No new goroutines. The producer's single `Run` goroutine drives `subscribe → resolve
→ Events → drain`. The resolver closure's new "have I returned a file yet" flag is
read/written only inside `resolve`, which is called only from that single goroutine,
so it is data-race-free without synchronization. Document this invariant in the
closure's doc comment (mirrors the existing `OnEvent`/`flushDelta` single-Run-goroutine
notes in `interactive_turn_stream_v2.go`).

---

## Error handling

Unchanged from today. The resolver still returns its wrapped errors for an unreadable
dir (`read claude sessions dir …`) and for an empty dir
(`no session jsonl found in …`); the subscriber still retries on both. The only
behavioral delta is the offset value (0 vs size) on the first successful resolve
after a not-found. A cold-start file that is *empty* at resolve time (size 0) returns
offset 0 either way — no special case. A `ctx` cancel during the retry sleep still
exits via the existing `sleepCtx`/`ctx.Err()` paths.

---

## Testing strategy

### CI gate — RED-before / GREEN-after (replaces AC-1's mapper-test framing)

In `cmd/pyry/interactive_turn_stream_v2_test.go`, mirroring the existing temp-dir
resolver tests:

- **`TestResolveLatestSessionJSONL_ColdStartTailsFromZero`** (the regression gate):
  construct the resolver over an **empty** `t.TempDir()`. First call → assert it
  errors (`no session jsonl found`). Then write a `<uuid>.jsonl` with **non-zero**
  content (size > 0). Second call → assert `path` is that file and **`startOffset ==
  0`**. *Fails on current code* (returns `size`), *passes after the fix*. This is the
  load-bearing CI gate: offset 0 is what guarantees the in-flight reply is tailed.
- **`TestResolveLatestSessionJSONL_WarmStartTailsFromSize`** (no-leak guard): write a
  `<uuid>.jsonl` with size N **before** constructing the resolver. First call →
  assert `startOffset == N`. Proves a `--continue` transcript present at subscribe is
  *not* replayed from 0 (security-relevant — see § Security review).
- Re-run the existing `TestResolveLatestSessionJSONL_*` (NewestWins,
  ReEvaluatesPerCall, TieBreak, IgnoresNonSessionEntries, EmptyDirErrors,
  UnreadableDirErrors) — all must stay green. `ReEvaluatesPerCall` is the key
  compatibility check: first call finds a file (warm) → returns size; a later newer
  file (rotation) → still returns size.

Write these as the project's table-driven / temp-dir idiom (`go test -race`, stdlib
only); reuse `writeJSONL`, `uuidA/B/C` from the existing file.

### Operator-verified (AC-3, live two-phone stack — unchanged)

The rung-3 e2e `InteractiveStreamE2ETest` (pyrycode-mobile#421) must reach green: the
`"ping"` reply renders in the thread. The agent cannot drive the live mobile stack;
the **operator** confirms. During that run, confirm the mechanism: the daemon logs
should show `turnbridge: resolve session jsonl, retrying` (the cold-start path fired)
and the post-fix subscribe at offset 0 (via the optional diagnostic above). The CI
gate for this ticket is the regression test, not the live e2e.

---

## Guarded fallback (the fork is resolved, but confirm cheaply)

The in-repo diagnosis rests on off-stack evidence (this host's claude 2.1.158 JSONL +
the passing fakeclaude e2e + the self-documenting harness comment), which is strong
but not the live mobile#421 capture. Build the fix as specced. If — contrary to all
three confirmations — the operator's AC-3 run shows the **reply line itself** reaching
`producer.go:132` and dropping (i.e. `mapEntry` returns `ok=false` for the reply's own
`type:"assistant"` text line, not for the surrounding user/metadata lines), then the
true cause is the extractor after all: stop, do **not** widen this ticket, and route
back via `needs-rework:po` per the ticket's cross-repo contingency (the fix would
then span tui-driver `AssistantText` + a tagged release + a `go.mod` bump, which is a
coordinated two-repo change). Do not implement both. The strong expectation, on the
evidence, is that this fallback does not fire.

---

## Open questions

- **Post-/clear first-reply race.** A /clear rotation re-subscribes against the same
  (now-warm) resolver closure, so a brand-new post-/clear file is tailed from EOF and
  could in principle skip the first post-/clear reply by the same race. This is
  **unobserved** (mobile#421's e2e has no /clear) and out of scope here. If it ever
  surfaces, the per-attempt (subscriber-owned) tracking discussed in § Design is the
  natural escalation. File a follow-up issue rather than pre-building it.
- **`--continue` file-fork vs append.** This spec assumes the observed failure is the
  cold-start absence race (no file at subscribe), which the harness comment
  documents. If the operator capture instead shows claude writing the reply to a
  *different* `<uuid>.jsonl` than the one the bridge tailed (a resume-fork the
  resolver's most-recently-modified pick missed), that is still an in-repo
  resolver/subscription fix and still S — adjust the resolver's file selection, not
  the extractor. Note it if seen; it does not change the fork resolution.

---

## Security review

**Verdict:** PASS

The fix changes what claude-transcript content reaches an internet-exposed phone (a
cold-start session now streams from its start instead of being skipped), so the
review centers on whether the wider stream can leak content the phone must not see.
It cannot: offset 0 is structurally confined to *brand-new* session files.

**Findings:**

- **[Trust boundaries]** No MUST-FIX. The boundary is claude's session JSONL
  (subprocess-written file) → `TailJSONL` → `mapEntry` → capability-gated emitter →
  Noise seal → phone. The fix only changes the **read offset** at that boundary.
  Worst case considered: offset 0 replaying a *prior* session's transcript to the
  phone. Ruled out structurally — cold-start (offset 0) fires only for a file that
  did **not** exist on an earlier resolve call, i.e. a fresh session with no prior
  history; a `--continue` resume transcript already exists on disk → warm path →
  `offset = size` (unchanged). The `TestResolveLatestSessionJSONL_WarmStartTailsFrom
  Size` test is the deterministic guard and is **mandatory** (not optional). The
  emitter additionally scopes events to `sup.CurrentConversation`, which the `send`
  stamps before the cold-start tail reads the reply (`relay_two_phone_structured
  _test.go:205-206`), so no cross-conversation misattribution. Non-assistant
  preamble lines a fresh file carries drop in `mapEntry` and are never emitted.

- **[Tokens, secrets, credentials]** N/A — the fix introduces no tokens, secrets, or
  credentials; it adjusts a file read offset.

- **[File operations]** No findings. The resolver's dir scan still gates filenames
  through `jsonlStemPattern` (36-char UUID stem) + `.jsonl` suffix over the trusted
  `claudeSessionsDir`; no user input is concatenated into a path. No new
  check-then-use (TOCTOU) is added — the offset is `0` vs `size`, and the existing
  ReadDir→Stat→skip-on-vanish is unchanged. No new file writes; read-only tail.

- **[Subprocess / external command execution]** N/A — claude is spawned elsewhere;
  this fix only reads its JSONL output. No `exec`, no env handling, no signals.

- **[Cryptographic primitives]** N/A — the Noise seal is downstream in the emitter;
  this fix touches no crypto, RNG, or comparison of secrets.

- **[Network & I/O]** No MUST-FIX. Offset 0 streams more bytes on cold start (the
  current session from its start) than the broken EOF behavior (nothing). Bounded:
  it is one fresh session's content, streamed once, with non-representable lines
  dropped and the emitter owning backpressure / delta coalescing (ADR 025 § Back
  pressure; #609). No new socket reads, no new size cap needed (line handling is
  tui-driver's existing concern). Not a flood vector.

- **[Error messages, logs, telemetry]** SHOULD FIX — *if* the optional diagnostic
  `log.Debug` is added, it MUST carry only `path` (a UUID filename under the trusted
  dir — a path, never file bytes, matching the resolver's existing error-wrap
  posture) + `offset` (int) + a cold-start bool, and MUST NOT log `RawLine` / entry
  content / reply text. Developer enforces; code-review checks. The substrate seal
  (`Supervisor.Session()` SECURITY note: only typed events leave the package, never
  raw claude-screen bytes) is unaffected.

- **[Concurrency]** No findings. The resolver's new "have I returned a file yet"
  flag is read/written only inside `resolve`, called only from the single
  `Producer.Run` goroutine, so it is race-free without a mutex — the spec requires
  this single-goroutine invariant be asserted in the doc comment. No new goroutines,
  no new locks, no new shutdown paths (ctx-cancel exits via existing `sleepCtx`).

- **[Threat model alignment]** No findings. `protocol-mobile.md` § Security model's
  relevant threats — capability gating and session/conversation isolation — are
  preserved: the #632 capability-gated emitter is untouched (only `interactive`-
  granted phones receive the structured stream; phone B's negative is covered by the
  structured e2e's vacuous-pass guard, out of scope here), and offset 0's confinement
  to fresh files preserves session isolation. The fix neither widens the wire surface
  nor relaxes a gate.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-18
