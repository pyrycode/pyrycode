# Spec #1195 — Let a minted per-conversation PTY session write the transcript the daemon tails

**Ticket:** [#1195](https://github.com/pyrycode/pyrycode/issues/1195) · **Size:** S · **Labels:** `size:s`, `security-sensitive`

Two default-off `fakeclaude` knobs — one that takes the transcript stem from the
child's own argv, one that gives each child its own JSONL-injection trigger path —
plus the hermetic PTY e2e that they make possible. **No production (`cmd/`,
`internal/` non-test) code changes.** The daemon-side resolver is untouched (AC5).

---

## Files to read first

Turn-1 data load. Read these before writing any code; every design decision below
cites one of them.

| Path | Extract |
|---|---|
| `internal/e2e/internal/fakeclaude/main.go:1-240` | The env-knob doc-comment block. **Every new knob gets an entry here in the same voice, ending with the "Default off — when unset, byte-identical to its prior behaviour" clause.** This is the AC4 contract in prose. |
| `internal/e2e/internal/fakeclaude/main.go:256-277` | The `env*` const block — where the two new consts go. |
| `internal/e2e/internal/fakeclaude/main.go:504-558` | `main()`'s PTY-mode preamble: `mustEnv(envSessionsDir)` / `mustEnv(envInitialUUID)` / `mustEnv(envTrigger)`, the knob reads, `startStdinReader`, then `f := openSession(dir, initU)` at `:558`. **`:558` is the single line whose `initU` argument the stem knob redirects.** |
| `internal/e2e/internal/fakeclaude/main.go:572-678` | The poll loop and its one-shot gate style (`rotated` / `idled` / `modalShown` / …). `:673-675` is the existing `jsonlTrig` branch the per-child trigger branch mirrors. |
| `internal/e2e/internal/fakeclaude/main.go:699-745` | `emitStructuredJSONLIfTriggered` + `claimTrigger`. **Reuse verbatim — the per-child knob passes a different `path`, nothing else.** Note the doc's "fixed `<path>.consuming` sidecar is sound because fakeclaude runs a single poll goroutine": that reasoning survives per-child paths because each child's path (and therefore sidecar) is distinct. |
| `internal/e2e/internal/fakeclaude/main.go:995-1008` | `openSession(dir, uuid)` — `filepath.Join(dir, uuid+".jsonl")`. The join the stem guard protects. |
| `internal/e2e/internal/fakeclaude/esc_detect_test.go` | The unit-test idiom for this package: `package main`, **no `//go:build e2e` tag**, table-driven over a pure helper. `argvSessionID`'s test goes here-style, in a new file. (`main_test.go` *is* tagged `e2e` — don't copy that one.) |
| `internal/e2e/relay_v2_modal_perconv_test.go` (whole file, 243 lines) | **The template for the new e2e.** Mint-over-the-wire mechanics: `shortHome` → `pair` → `claudeSessionsDir(home)` + pre-create `<initialUUID>.jsonl` → `fakerelay.New` → `StartRotationWithRelay` → `driveHandshakeToOpenDaemonInteractive` → local `sealSend` / `nextEnv` closures → all-null `create_conversation` → drain to `conversation_created` → `send_message` to move the active cursor → drain. Copy this skeleton. |
| `internal/e2e/harness.go:323-366` | `StartRotationWithRelay` — the four `PYRY_FAKE_CLAUDE_*` envs it always sets, and `extraEnv ...string` appended verbatim (where the two new knobs go). |
| `internal/e2e/harness.go:479-490` | `seedBootstrapRegistry` — **writes `sessions.json` with `id: <initialUUID>, bootstrap: true`. This is why the bootstrap pool id EQUALS `PYRY_FAKE_CLAUDE_INITIAL_UUID` in every rotation-harness e2e.** Load-bearing for AC3/AC4 (see § Why the bootstrap child is unaffected). |
| `internal/sessions/pool.go:1363-1406` | `buildSession`: `base := append(slices.Clone(tpl.ClaudeArgs), "--session-id", string(id))` at `:1379`, then `--settings`. **Exactly one `--session-id` in a minted child's argv, and no `ResolveSessionID` on the minted `supCfg`, so the supervisor appends nothing further.** Also: `workDir = tpl.WorkDir` when `spawnDir == ""` (`:1387-1390`). |
| `internal/sessions/pool.go:433-473` | Bootstrap argv: `base` is `Bootstrap.ClaudeArgs + --settings <path>` — **no `--session-id`**; `ResolveSessionID` (`:466`) supplies it at spawn, and returns `resume=true` iff `StatByID(cfg.ClaudeSessionsDir, id)` succeeds. |
| `internal/supervisor/supervisor.go:905-925` | `buildClaudeArgs` — appends `--resume <id>` when the transcript exists, else `--session-id <id>`, **always last, and the two are mutually exclusive**. This is why `argvSessionID` must accept both flags and take the last occurrence. |
| `cmd/pyry/interactive_turn_stream_v2.go:389-394` | `perConversationSessionsDir` — returns `sharedDir` when `sessionWorkDir == bootstrapWorkDir`. With `sessionTpl = cfg.Bootstrap` (`internal/sessions/pool.go:592`) and `Bootstrap.WorkDir = trustedWorkdir` (`cmd/pyry/main.go:825`), a default-minted session's `convDir` **is** the shared dir. Only the stem diverges. |
| `cmd/pyry/interactive_turn_stream_v2.go:427-470` | `resolveTarget` — the non-bootstrap branch calls `resolveBoundSessionJSONL(convDir, sessionID)` and **never falls back to bootstrap under a non-empty cursor**. AC5's subject; do not touch. |
| `cmd/pyry/main.go:859-872` | `boundHost` — where `convDir` comes from. Read-only context for why the dirs already agree. |
| `internal/turnbridge/mapper.go:21-31` | `EventKindJsonlEndOfTurn → turnevent.TurnEnd{Reason: end_turn}`. The only mapping that yields a `turn_end`, and it is transcript-derived — the structural reason this ticket exists. |
| `internal/protocol/interactive.go:73-77` | `TurnEndPayload{ConversationID, TurnID, StopReason}` — the fields the e2e asserts. |
| `internal/e2e/relay_two_phone_structured_test.go:255-310` | The **injection-line fixture shape** (`:263`, an `assistant` + `stop_reason:"end_turn"` + non-empty-text line) and the drop mechanics: plain `os.WriteFile` of the trigger, **re-dropped on a ticker**. |
| `docs/knowledge/codebase/929.md` (whole file, 96 lines) | **Mandatory.** The subscription-offset race: the producer subscribes at EOF after a `subscribeRetryDelay` (500 ms) settle, so a single-shot append lands *below* the tailed range and is never seen. The established fix is the re-drop kicker. § Testing strategy below is built on this. |
| `internal/sessions/pool.go:1329-1333`, `internal/sessions/rotation/watcher.go:140-190` | `RegisterAllocatedUUID` before `supervise`, and `handleCreate`'s `IsAllocated` skip (`:149`) plus the `ref.ID == stem` early return (`:161`). Why a **new** `<mintedID>.jsonl` appearing in the shared dir cannot be mistaken for a `/clear` rotation. |
| `CODING-STYLE.md` | Table-driven tests, stdlib only, `gofmt`. |

---

## Context

`turn_end` exists on exactly one code path: `tuidriver.EventKindJsonlEndOfTurn`
(`internal/turnbridge/mapper.go:24`), which is **transcript-derived**. The
interactive turn stream subscribes via `NewTargetSubscriber`
(`cmd/pyry/interactive_turn_stream_v2.go:102`), which is JSONL-gated; the
screen-only `NewScreenTargetSubscriber` added by #1070 serves the modal stream
alone. So on the PTY tier, no transcript ⟹ no turn lifecycle, full stop.

For a minted (non-bootstrap) conversation the daemon tails
`<convDir>/<mintedSessionID>.jsonl`. `fakeclaude` parses no argv: it binds
`dir := mustEnv(envSessionsDir)` and `initU := mustEnv(envInitialUUID)`
(`internal/e2e/internal/fakeclaude/main.go:504-505`) — process-wide values every
child inherits identically. There is no per-session env seam to work around it
with (`sessions.Pool` sets no `Env` on the spawn `supervisor.Config`,
`internal/sessions/pool.go:1392-1406`). A minted PTY child therefore writes
`<sharedDir>/<INITIAL_UUID>.jsonl` while the daemon tails
`<sharedDir>/<mintedID>.jsonl`; the subscription never opens, and conversation-scoped
turn lifecycle is observable on **zero** PTY-tier tests.

Two facts make the fix small:

1. **The directory already agrees.** An all-null `create_conversation` resolves
   `spawnDir` to `""` (`internal/relay/handlers/create_conversation.go:145-148`), so
   the pool spawns in `tpl.WorkDir` — which is `trustedWorkdir`, the bootstrap
   workdir — and `perConversationSessionsDir` maps that back to the shared
   `claudeSessionsDir`. **Only the filename diverges.**
2. **The id the daemon tails already reaches the child**, baked into the minted
   spawn's argv as `--session-id <pool id>` (`internal/sessions/pool.go:1379`).

What is *not* true — and a design resting on it would break the suite — is that the
flag's *presence* marks a child as minted. Since #839 the bootstrap is pinned too
(`ResolveSessionID`, `internal/sessions/pool.go:466-473` → `buildClaudeArgs`,
`internal/supervisor/supervisor.go:913-925`). Inertness must come from an explicit
selector of the fake's own, like every other `PYRY_FAKE_CLAUDE_*` knob.

---

## Design

Two orthogonal, default-off knobs in `internal/e2e/internal/fakeclaude/main.go`.
Nothing else in the tree changes.

### Knob 1 — `PYRY_FAKE_CLAUDE_SESSION_ID_FROM_ARGV`

When set to any non-empty value, the fake derives its **initial transcript stem**
from its own argv instead of `PYRY_FAKE_CLAUDE_INITIAL_UUID`.

```go
// argvSessionID returns the session id the daemon pinned this spawn to — the value
// following the LAST "--session-id" or "--resume" in args — and whether one was
// found. Two-token form only. Pure; never reads the environment.
func argvSessionID(args []string) (string, bool)
```

- **Last occurrence wins.** `buildClaudeArgs` appends the flag at the end of argv
  (`internal/supervisor/supervisor.go:913-925`), so the spawn-time value is
  authoritative over anything a template contributed.
- **Both flags.** `--session-id <id>` (create) and `--resume <id>` (reattach) name
  the same transcript stem; #1164 switches between them per spawn based on whether
  `<id>.jsonl` already exists. Handling only one would make the knob silently
  stem-blind on warm starts.
- **Two-token form only.** Neither call site emits `--flag=value`; a `=`-form
  parser would be dead code. Document the limitation in the helper's doc comment.
- **Stem guard (security, see § Security review MF-1).** The returned value is
  accepted as a stem only if it is non-empty and contains no `/`, `\`, or `.`.
  It reaches `filepath.Join` in `openSession` (`:996`) and in knob 2's trigger
  path; the guard is the fake-side mirror of `transcript.ValidStem`, which the
  daemon applies at `cmd/pyry/interactive_turn_stream_v2.go:506`. Implement it
  inline (a 3-line predicate with a comment naming `transcript.ValidStem` as the
  daemon-side authority) rather than importing `internal/transcript` — this
  stand-in stays near-zero-dependency by design.
- **Fallback, never fatal.** No flag found, or a value that fails the guard ⟹ use
  `mustEnv(envInitialUUID)` exactly as today. A legacy unpinned spawn keeps
  working, and a malformed value degrades to today's behaviour instead of writing
  somewhere unexpected.

Wiring is one line: the `initU` argument at `main.go:558`. `rotateSession`
(`:990-993`) is untouched — a `/clear` rotation still mints a fresh random UUID,
because that is what real claude does.

### Knob 2 — `PYRY_FAKE_CLAUDE_JSONL_TRIGGER_DIR`

When set, the fake watches **`<dir>/<its own stem>.jsonl.trig`** in parallel with
the existing triggers and, on each appearance, appends the file's contents verbatim
to its live transcript — the existing
`emitStructuredJSONLIfTriggered(f, path)` (`:723`), called with a different `path`.
No new consume machinery, no new claim protocol.

Why a new knob rather than reusing `PYRY_FAKE_CLAUDE_JSONL_TRIGGER`: that env is a
**single shared path**, and its claim protocol is documented as sound only because
"fakeclaude runs a single poll goroutine". With a bootstrap child polling the same
path, whichever child claims first appends the line to *its own* transcript — a
coin flip, and the wrong outcome silently satisfies nothing. Keying the path on the
child's own stem makes ownership structural: the test drops
`<dir>/<mintedID>.jsonl.trig` and only the minted child can ever claim it. The
bootstrap child polls `<dir>/<initialUUID>.jsonl.trig`, which the test never
creates, so it is inert.

- The two JSONL triggers are watched **independently** (two `if` blocks in the poll
  loop). Neither disables the other; a test may set both.
- The path is computed **once, before the loop**, from the same stem `openSession`
  used — so knob 2's per-child isolation is exactly as good as knob 1's stem
  divergence. Document that dependency; do not enforce it in code (a future
  bootstrap-only test may legitimately want knob 2 alone).
- Not one-shot. The consume re-fires on every re-drop, which is precisely what the
  #929 kicker needs.

### Why two knobs and not one

They must not be fused. #1191 — the minted-interrupt oracle this ticket unblocks —
needs knob 1 but **must not** get an unconditional per-turn end-of-turn line: its
whole vacuous-pass guard is "the bare-ESC handler is the ONLY source of a
`turn_end`" (`internal/e2e/relay_v2_interrupt_test.go:280-292`,
`docs/knowledge/codebase/929.md` § Fix). A knob that ended every delivered turn
would fabricate exactly the event #1191 must attribute to the interrupt. Knob 2
keeps injection under explicit test control, so #1191 can set knob 1 alone.

### Rejected alternatives

| Alternative | Why not |
|---|---|
| **Key the stem on "argv carries a session id"** (no env knob) | Re-points *every existing bootstrap child's* transcript. The flag is not a minted/bootstrap discriminator — #839 pins the bootstrap too. This is the design the ticket body explicitly rules out. |
| **`PYRY_FAKE_CLAUDE_TURN_ENDS_TURN`** — every delivered turn appends an end_turn line | (a) Fuses the two behaviours and breaks #1191 (above). (b) Its #929 kicker would have to be a *re-sent user turn* over the wire; the fake's TUI spinner is one-shot (`spinnerEmitted`, `:1102-1105`) and minted sessions have no transcript growth-confirm (`buildSession` sets no `ResolveTranscript`), so repeated PTY delivery is untested ground. A file drop involves no daemon delivery path at all. |
| **Per-session `Env` on the spawn `supervisor.Config`** | `supervisor.Config.helperEnv` is unexported and documented "used only in tests (TestHelperProcess pattern)"; `sessions.Pool` sets no `Env` (`:1392`). Adding one is a **production** change to serve a test fake — the wrong direction, and it widens the daemon's spawn surface. |
| **Relax `resolveBoundSessionJSONL` to scan the shared dir by recency** | Directly forbidden by AC5, and it is the #854 confidentiality regression: a second claude writing into the shared dir could redirect the tail. The fake moves to meet the daemon, never the reverse. |
| **Import `internal/transcript` for `ValidStem`** | Couples the stand-in to a daemon package for a 3-line predicate. Noted as a live option if a second stem-validating site ever appears. |

### Why the bootstrap child is unaffected — even with both knobs on

The new e2e sets both knobs on the daemon process, so **both** children inherit
them. The bootstrap child's stem is nevertheless unchanged, for a reason worth
stating precisely because it is what makes AC3/AC4 hold *inside the new test*, not
merely outside it:

`StartRotationWithRelay` calls `seedBootstrapRegistry(t, home, initialUUID)`
(`internal/e2e/harness.go:329, 479-490`), which writes a `sessions.json` whose sole
entry is `{id: <initialUUID>, bootstrap: true}`. `Pool.New`'s `pickBootstrap`
branch adopts that id verbatim (`internal/sessions/pool.go:383-384`). So the
bootstrap pool id **equals** `PYRY_FAKE_CLAUDE_INITIAL_UUID`, its spawn argv carries
`--session-id <initialUUID>` or `--resume <initialUUID>`, and knob 1 resolves the
stem to the same value the env would have given. Byte-identical, by construction —
confirmed independently by `docs/knowledge/codebase/929.md:14-20`, which chased and
falsified exactly this hypothesis with a daemon log line.

Existing e2e set neither knob, so they are inert for them regardless (AC3/AC4).

### One new file in the shared sessions dir

The minted child's `openSession` creates `<sharedDir>/<mintedID>.jsonl` — a file
that does not exist today. Two independent mechanisms keep the rotation watcher
from reading that CREATE as a `/clear`:

1. `Pool.CreateIn` calls `RegisterAllocatedUUID(id)` **before** `supervise`
   (`internal/sessions/pool.go:1329-1333`), and `handleCreate` skips allocated
   stems (`internal/sessions/rotation/watcher.go:149`).
2. Even past the 30 s allocation TTL, `handleCreate`'s `ref.ID == stem` early
   return (`:161`) matches the minted session's own pool entry and returns.

And the bootstrap tail cannot be redirected by it: `resolveBootstrapJSONL` prefers
the pinned id and stats exactly `<id>.jsonl` — the #839/#854 isolation property.

### Data flow (new e2e, both knobs on)

```
phone ──create_conversation(all-null)──▶ daemon
                                          └─ Pool.CreateIn → RegisterAllocatedUUID(M)
                                             spawn fakeclaude  argv: … --session-id M …
                                                                     workdir = trustedWorkdir
   fakeclaude (minted child)
     knob 1: stem := argvSessionID(os.Args) = M          (not INITIAL_UUID)
     openSession  →  <sharedDir>/M.jsonl                 ("{}\n")
     knob 2: watch  <trigDir>/M.jsonl.trig

phone ──send_message(convID)──▶ daemon: active cursor := convID
                                  resolveTarget → boundHost(convID)
                                    → (mintedSupervisor, M, sharedDir)
                                  resolveBoundSessionJSONL(sharedDir, M)
                                    → tails <sharedDir>/M.jsonl        ◀── SAME FILE

test ──(250 ms ticker)──▶ write <trigDir>/M.jsonl.trig = end_turn line
   fakeclaude claims + appends verbatim to <sharedDir>/M.jsonl, fsync
                                  turnbridge mapper: EventKindJsonlEndOfTurn
                                    → turnevent.TurnEnd
                                  emitter stamps conversation_id = convID
phone ◀──turn_end{conversation_id: convID, stop_reason:"end_turn"}──
```

The bootstrap child, in the same process tree with the same env, watches
`<trigDir>/<initialUUID>.jsonl.trig` — never created — and writes
`<sharedDir>/<initialUUID>.jsonl` exactly as today.

---

## Concurrency model

No new goroutines. Both knobs live entirely on the existing main poll goroutine:

- Knob 1 resolves the stem in `main()` **before** the loop and before
  `startStdinReader`, and hands it to `openSession`. Nothing observes it later.
- Knob 2's branch calls `emitStructuredJSONLIfTriggered(f, perChildTrig)` from the
  poll loop, alongside the existing `jsonlTrig` branch — preserving the
  **single-writer-of-`f`** invariant that `turnPending` / `escPending` /
  `clearPending` all exist to protect (`main.go:1046-1051`). No new
  `atomic.Bool` signal is introduced, because neither knob is stdin-driven.
- `claimTrigger`'s fixed `<path>.consuming` sidecar stays collision-free: the two
  watched paths differ, so their sidecars differ, and each child's path embeds its
  own stem. The existing "single poll goroutine, every consume completes before the
  next claim" reasoning carries over unchanged.

Cross-process: the test writes the trigger while the fake renames it. The claim is
an atomic `os.Rename`, so a consume only ever operates on the inode it claimed
(`main.go:705-717`). A torn read of a mid-write drop is possible in principle and
self-heals on the next re-drop — the same posture
`relay_two_phone_structured_test.go:293-306` already ships.

---

## Error handling

Silent-degradation posture throughout, matching the file (`emitAssistantIfTriggered`,
`appendTurnGrowth`, `emitStructuredJSONLIfTriggered` all silence errors and let the
e2e assert downstream).

| Failure | Behaviour |
|---|---|
| Knob 1 set, argv carries neither flag | Fall back to `envInitialUUID`. No log, no exit. |
| Knob 1 set, flag present but value fails the stem guard | Fall back to `envInitialUUID`. The guard exists so an odd value can never widen the write target; falling back keeps the child alive so the e2e fails on its *assertion* (readable) rather than on a dead child (opaque). |
| Knob 1 set, flag is the last token with no following value | Treated as "not found" → fallback. |
| Knob 2 set, trigger dir absent | `os.Rename` returns ENOENT every cycle — indistinguishable from the steady "no trigger" state. Inert; the e2e times out on its assertion. The **test** creates the dir. |
| Knob 2 set, trigger holds malformed JSONL | Appended verbatim; the turnbridge mapper maps an unparseable line to `(nil, false)` and drops it. Invisible except as a missing assertion. |
| Both knobs unset | Zero behavioural delta. `os.Getenv` returns `""`, the stem stays `mustEnv(envInitialUUID)`, no extra poll-loop branch fires. |

Nothing here introduces a new `fatalf` path. `mustEnv(envInitialUUID)` keeps its
current fatal-on-missing contract — knob 1 changes which value is *used*, never
whether the env is required, so a harness that forgets `INITIAL_UUID` still fails
loudly at startup.

---

## Testing strategy

### Unit — `internal/e2e/internal/fakeclaude/argv_session_id_test.go`

New file, `package main`, **no build tag** (match `esc_detect_test.go`, not the
`e2e`-tagged `main_test.go`). Table-driven over `argvSessionID`. Scenarios:

- Empty argv → not found.
- `--session-id <uuid>` → that uuid, found. *(minted spawn,* `pool.go:1379`*)*
- `--resume <uuid>` → that uuid, found. *(warm bootstrap spawn, #1164)*
- Realistic full minted argv (`--session-id <uuid> --settings /tmp/x.json`) → the uuid.
- Realistic full bootstrap argv (template flags, `--settings …`, then the appended
  `--resume <uuid>`) → the uuid.
- Both flags present, different values → the **last** one.
- Flag is the final token with no value → not found.
- `--session-id=<uuid>` (`=` form) → not found. Pins the documented limitation so a
  future reader sees it was a decision, not an oversight.
- Value fails the stem guard: `../escape`, `a/b`, `x.jsonl`, `""` → not found (or
  found-but-rejected, depending on where the guard sits — assert the **caller-visible
  outcome**: the stem is not adopted).

### E2E — `internal/e2e/relay_v2_perconv_turn_end_test.go`

New file, `//go:build e2e`, `package e2e`. Copy the skeleton of
`relay_v2_modal_perconv_test.go` verbatim through the `send_message` ack, changing
only the env and the assertion tail. Reuse the shared helpers it uses
(`shortHome`, `RunBareIn`, `decodePairPayload`, `claudeSessionsDir`,
`seedBootstrapRegistry` via `StartRotationWithRelay`, `readPersistedServerID`,
`waitBinaryHello`, `fakephone.Dial`, `driveHandshakeToOpenDaemonInteractive`,
`sendNoiseMsg`, `decryptInnerEnvelope`, `mustJSON`) and re-declare the local
`sealSend` / `nextEnv` closures the way every sibling test does.

Env passed to `StartRotationWithRelay` as `extraEnv`:
`PYRY_MOBILE_V2=1`, `PYRY_FAKE_CLAUDE_TUI=1` (startup idle glyph so `WaitReady`
returns and the routed turn is delivered), `PYRY_FAKE_CLAUDE_SESSION_ID_FROM_ARGV=1`,
`PYRY_FAKE_CLAUDE_JSONL_TRIGGER_DIR=<trigDir>`. Use a never-firing rotation trigger
path, as the modal perconv test does. Create `<trigDir>` before the daemon starts.

Sequence:

1. Pair one interactive device; pre-create `<sharedDir>/<initialUUID>.jsonl` (the
   #791/#792 pattern the modal perconv test follows at `:73-83`).
2. Handshake to interactive; `create_conversation` (all-null); drain to
   `conversation_created`; capture `convID`.
3. `send_message` to `convID`; drain to its `ack`. **This is the cursor move** —
   `resolveTarget` re-keys on `active.CurrentConversation`
   (`cmd/pyry/interactive_turn_stream_v2.go:427-466`), so nothing for the minted
   conversation can arrive before it.
4. **Start the #929 re-drop kicker**: a goroutine on a 250 ms ticker writing the
   end-of-turn line to `<trigDir>/<mintedSessionID>.jsonl.trig`, stopped via
   `sync.Once` in a `t.Cleanup`.
5. Drain for `protocol.TypeTurnEnd`; decode `protocol.TurnEndPayload`; assert
   `ConversationID == convID`. Deadline ~15 s, matching the siblings.
6. Assert the on-disk targeting (see AC2 below).

**The kicker is mandatory, not defensive polish.** `docs/knowledge/codebase/929.md`
records the exact failure a single-shot drop produces here: the producer sleeps one
`subscribeRetryDelay` (500 ms) before subscribing and then tails **from EOF**, so a
line appended in that window lands below the tailed range and is never emitted. The
re-drop is safe in a way #930's is not — the *only* content ever dropped is the
end-of-turn line, so re-firing it can at worst produce a second `turn_end`, and the
drain accepts the first match.

`mintedSessionID`: the test must learn it to build the trigger path. Obtain it the
cheapest way that does not add production surface — read it from the persisted
conversations registry / `sessions.json` under `<home>/.pyry/` after
`conversation_created`, polling briefly for the binding to appear. **Flagged in
§ Open questions** — if no existing e2e helper reads the conversation→session
binding, add a small local reader in the test file (do not add a harness export).

#### The injected line

Define a file-local const in the test, copying the shape at
`relay_two_phone_structured_test.go:263`: `assistant` + `stop_reason:"end_turn"` +
non-empty text, with a **distinctive text unique to this test** (e.g.
`"e2e-1195:minted-turn-end"`). The distinctiveness is what makes the AC2 on-disk
assertion sharp. Do not reach into the fake's unexported `interruptEndTurnLine`.

#### AC-by-AC

- **AC1** — step 5. A `turn_end` stamped with the minted conversation's id, where
  today the PTY tier produces none.
- **AC2 (non-vacuity)** — two layers.
  *Structural:* `resolveTarget`'s non-bootstrap branch tails
  `<convDir>/<mintedID>.jsonl` and never the bootstrap transcript, so a turn-end
  produced by the bootstrap child cannot reach this subscription. If knob 1 failed
  and the minted child wrote `<initialUUID>.jsonl`, the trigger path would also be
  `<initialUUID>.jsonl.trig` — which the test never drops — so nothing is injected
  anywhere and the assertion times out.
  *Asserted (deterministic, in code):* after step 5, read both transcripts and
  assert `<sharedDir>/<mintedID>.jsonl` **contains** the distinctive text and
  `<sharedDir>/<initialUUID>.jsonl` **does not**. A `conversation_id` stamp alone
  proves only what the emitter believed, since the stamp comes from the active
  cursor rather than from the file; the on-disk pair proves the *targeting*. See
  § Security review MF-2.
- **AC3** — no existing test file is edited, and neither knob is set anywhere but
  the new test. Verify with the full suite: `go test -race ./...` plus
  `go test -race -tags e2e ./internal/e2e/...`, and name
  `TestRelayV2_InterruptStopsRunningTurn` (bootstrap PTY interrupt) and
  `TestRelayV2_PerConversationModalShown` explicitly in the PR body as the two the
  AC calls out.
- **AC4** — the unit table's "flag present, knob semantics" rows plus the
  doc-comment contract. The knob-unset path executes zero new statements: the stem
  expression short-circuits on `os.Getenv(...) == ""` and the poll-loop branch is
  guarded by an empty string. Reviewer-checkable by reading the two guards.
- **AC5** — the diff touches no file under `cmd/` or non-test `internal/`. State
  this in the PR body; it is mechanically checkable from `git diff --name-only`.

#### RED before / GREEN after

On `main` the two env vars are unrecognised: the minted child writes
`<initialUUID>.jsonl`, nothing polls `<mintedID>.jsonl.trig`, the subscription for
the minted conversation never opens, and step 5 times out with a message that says
so. The developer **must** observe that red (stash the `main.go` change, run the new
test) and record it in the PR body. A test that was never red proves nothing about
a substrate gap.

---

## Security review (label: `security-sensitive`)

The `agents/architect/security-review.md` referenced by the architect instructions
is not present in this worktree (no `agents/` tree, no such file anywhere in the
repo). Pass run per the categories the instructions name: trust boundaries,
input validation, false-green risk, output/log hygiene, blast radius.

**Verdict: PASS**, with two MUST FIX items folded into the design above (MF-1 in
§ Knob 1, MF-2 in § Testing strategy AC2) and one verification note.

### Trust boundaries crossed

| Boundary | Data | Provenance | Guard |
|---|---|---|---|
| daemon argv → fake's transcript path | session id | Server-minted UUID: `sessions.NewID()` via `Pool.CreateIn` (`:1304`) or the seeded/pinned bootstrap id. **Never phone-supplied.** | MF-1 stem guard |
| test filesystem → fake's transcript contents | JSONL lines | Test-authored const in a `//go:build e2e` file. Not reachable from any network input. | none needed |
| env → trigger directory | path | Harness-authored. | none needed |
| fake's transcript → turnbridge → phone | mapped turn events | Already the trusted path every PTY e2e uses. | unchanged |

No phone-controlled, relay-controlled, or otherwise remote value reaches either new
code path. Restating that as a finding rather than an absence: the *only* new
untrusted-shaped input is argv, and argv is composed by
`internal/sessions` + `internal/supervisor` from registry values.

**MF-1 (fixed in design).** The argv-derived stem flows into
`filepath.Join(dir, stem+".jsonl")` (`main.go:996`) and into knob 2's
`filepath.Join(trigDir, stem+".jsonl.trig")`. Unguarded, a value containing `..`
or `/` would let the fake write outside the sessions dir. The daemon guards the
symmetric join with `transcript.ValidStem`
(`cmd/pyry/interactive_turn_stream_v2.go:506`) and documents it as a
"path-safety branch-selector (defense-in-depth)" for a value that is *already*
trusted — the same reasoning applies here, and a test fake that could be steered
outside its sandbox is a worse place to skip it, not a better one. **Design carries
the guard + fallback.**

**MF-2 (fixed in design).** The false-green risk the ticket names. The surface being
made observable *is* the cross-conversation confidentiality boundary: a mis-targeted
substrate could produce a green on exactly the property
`resolveBoundSessionJSONL`'s by-id targeting exists to protect. Concretely,
`turn_end.ConversationID` is stamped from the **active cursor**, not from the
transcript the event came out of — so a `ConversationID == convID` assertion alone
would also pass in a world where the daemon was tailing the wrong file while the
cursor happened to point at the minted conversation. **Design adds the on-disk
assertion pair** (distinctive text present in `<mintedID>.jsonl`, absent from
`<initialUUID>.jsonl`), which is deterministic code testing the targeting rather
than the stamp. This is the belt-and-suspenders-with-different-fabric shape: the
wire assertion is the stochastic-ish end-to-end signal, the file assertion is a
plain `strings.Contains` on bytes.

### Considered and dismissed

- **"Widen the resolver so the fake fits."** Would be a real regression (#854: a
  second claude in the shared dir redirecting the tail). Forbidden by AC5; not done.
  Recorded here because it is the tempting shortcut a future reader will reach for
  when this test goes red.
- **New file in the shared sessions dir as a hijack vector.** `<mintedID>.jsonl`
  appearing next to `<initialUUID>.jsonl` cannot redirect the bootstrap tail
  (by-id stat, not recency) and cannot trigger a spurious rotation (skip-set +
  `ref.ID == stem`). Analysed in § One new file above. Dismissed.
- **Log hygiene.** Neither knob logs, and neither writes to stdout. The
  "never echo phone-controlled prompt bytes to stdout" invariant
  (`startStdinReader`'s doc, `main.go:1016-1018`) is untouched: knob 2 appends only
  to `f` via the existing consume. No new record, so no new hygiene surface.
  Dismissed as not-applicable — but *asserted*, not assumed: the developer must not
  add a debug log of the resolved stem or the injected bytes.
- **`cmd/substrate-guard` allowlist (#603).** `fakeclaude/main.go` is already
  allowlisted file-level. The new *test* file carries a claude-format JSONL line;
  precedent is `relay_two_phone_structured_test.go:263`, which ships the same shape
  under no allowlist entry, because the guard targets TUI substrate glyphs and
  rendered claude prose, not inert JSONL. **Verification note:** run
  `go test ./cmd/substrate-guard/...` (or the guard's usual invocation) after adding
  the test file and confirm green. If it does flag, the fix is an allowlist entry
  for the new test file — not weakening the line.
- **Default-off as a security property.** Seventeen e2e files set
  `PYRY_FAKE_CLAUDE_*` directly and twenty-one ride it via the harness starters. A
  knob that changed behaviour when unset would silently re-point transcripts across
  that whole surface. Both knobs are `os.Getenv(...) != ""` gated with the unset
  path executing no new statements; AC4's unit rows and the doc-comment contract are
  the enforcement. Sufficient.

---

## Open questions

1. **How does the e2e learn the minted session id?** It needs it for the trigger
   path. `conversation_created` carries the conversation id, not the session id.
   Cheapest read: poll the persisted registry under `<home>/.pyry/` for the
   conversation's `CurrentSessionID`. Resolve during implementation; if no e2e
   helper already does this, add a file-local reader in the new test — **do not**
   add a harness export or a production accessor for it.
2. **Does the second turn matter?** The design routes exactly one `send_message`
   (cursor move) and drives the turn end from the trigger, so no second turn is
   needed. If the drain still never sees a `turn_end` with the kicker running, the
   next thing to check is whether the subscription opened at all — run the daemon
   with `-pyry-verbose` and look for `turnbridge: subscribed to session jsonl
   path=… offset=… cold_start=…`, the one log line
   `docs/knowledge/codebase/929.md:74-78` credits with turning a guessed fix into a
   diagnosis in one run.
3. **Knob-2 naming if a third injection site ever appears.** `…_TRIGGER_DIR`
   presumes one file per stem. Fine for one consumer; revisit only if a second
   per-child injection kind lands.

---

## Scope

| File | Kind | Est. |
|---|---|---|
| `internal/e2e/internal/fakeclaude/main.go` | production (test binary) | ~85 lines incl. two doc-comment entries |
| `internal/e2e/internal/fakeclaude/argv_session_id_test.go` | new, unit | ~55 lines |
| `internal/e2e/relay_v2_perconv_turn_end_test.go` | new, e2e | ~280 lines |

**One** production source file; two new files; zero new exported types; no consumer
cascade (`appendTurnEnd` and `openSession` keep their signatures — the stem knob
changes only the *argument* at `main.go:558`). Nothing under `cmd/` or non-test
`internal/`. Size `s` confirmed.

**Not in scope:** the minted-interrupt oracle (#1191, blocked on this — do **not**
prove the substrate with an interrupt or #1191 becomes a duplicate); any change to
`resolveBoundSessionJSONL` / `resolveTarget` / `perConversationSessionsDir`; any
`docs/knowledge/` file (the documentation phase owns those).
