# Spec #673 — fakeclaude grows its transcript on a delivered turn, unblocking the #668 commit-confirm in e2e

**Ticket:** #673 · **Size:** S · **Type:** test/fixture only · **Security-sensitive:** no
**Scope:** `internal/e2e/**` only. **No production code change.**

## Context

#668 (`5f8db69`) made the supervised-bootstrap delivery path confirm a user turn by **observing the resolved claude transcript grow** past a pre-delivery baseline (`confirmViaTranscriptGrowth`, `internal/supervisor/supervisor.go`). When the daemon has a non-empty `ClaudeSessionsDir` (always true in e2e — see below), `WriteUserTurn` ignores tui-driver's `Committed` chip heuristic and instead polls the newest `<uuid>.jsonl` for growth; **no growth within `transcriptConfirmTimeout` (10 s) → `ErrTurnNotCommitted`**, which the relay `send_message` handler reports to the phone as a retryable error instead of an ack/broadcast.

The e2e `fakeclaude` stub (`internal/e2e/internal/fakeclaude/main.go`) writes a single `{}` line at session open / rotation and **never grows the transcript when a turn is delivered**. So the growth poll waits the full 10 s and returns `ErrTurnNotCommitted`. This broke **six** `-tags e2e` tests (verified by two independent bisects of `5f8db69~1` vs `main`): the full `-tags e2e ./internal/e2e/` suite is currently **6 FAIL / 65 PASS**.

This is **purely a harness-fidelity gap**, not a production bug: real claude appends the user turn + assistant reply to its session JSONL at commit time, so growth is guaranteed for any turn that lands.

### Why exactly six, and why no baseline-resolve WARN (the precise failure mechanism)

The daemon **always computes** its sessions dir — there is no flag/env override:

- `cmd/pyry/main.go:439` → `resolveClaudeSessionsDir(*workdir)` → `sessions.DefaultClaudeSessionsDir(abs)` → `$HOME/.claude/projects/encode(workdir)` (`internal/sessions/reconcile.go:40-49`).
- Because `ClaudeSessionsDir != ""`, `Pool` sets `supCfg.ResolveTranscript = newTranscriptResolver(dir)` (`internal/sessions/pool.go:376-377`) **and** starts the rotation watcher, which **`os.MkdirAll(cfg.Dir, 0o700)`s that computed dir at startup** (`internal/sessions/rotation/watcher.go:97`).

The harness passes `-pyry-workdir=home` with `HOME=home`, so the daemon scans `$HOME/.claude/projects/encode(home)`. The **5 unaligned** tests point fakeclaude (`PYRY_FAKE_CLAUDE_SESSIONS_DIR`) at `filepath.Join(tmp, "claude-sessions")` — a **different** directory. Result chain:

1. Rotation-watcher `MkdirAll` creates the computed dir (empty).
2. `confirmViaTranscriptGrowth` baseline-resolve calls `mostRecentJSONL(computed dir)` → `os.ReadDir` **succeeds** (dir exists) but finds no `<uuid>.jsonl` → returns `("", nil)` → resolver returns `("", 0, nil)`. **No error → no baseline-resolve WARN** (this is exactly AC2's observable), and the `berr != nil` Committed-fallback is **not** taken.
3. fakeclaude grows `tmp/claude-sessions/...` (a dir the daemon never scans). The daemon keeps polling the empty computed dir; `grew("",0,"",0)` is never true → 10 s timeout → `ErrTurnNotCommitted`.

The **1 aligned** test (`TestTwoPhoneStructured_InteractiveReceivesStream`) already points fakeclaude at the computed dir, so its baseline resolves to the real `{}` file — but it **still** times out because fakeclaude never grows that file on the delivered turn (its structured `JSONL_TRIGGER` fires only *after* the ack, so it can't satisfy the ack's own growth-confirm).

The six broken tests are **exactly** the six that (a) set `PYRY_FAKE_CLAUDE_TUI=1` and (b) drive a user turn through `WriteUserTurn`. Verified: `grep -l PYRY_FAKE_CLAUDE_TUI internal/e2e/*_test.go` returns precisely these six.

| Test | File | sessionsDir today | Harness start helper | Relay leg |
|---|---|---|---|---|
| `TestE2E_IdleEviction_RespawnsOnSendMessage` | `respawn_after_eviction_test.go` | `tmp/claude-sessions` | `startEvictionHarness` (local) | v1 |
| `TestRelay_SendMessage_AckAndPTYDelivery` | `relay_send_message_test.go` | `tmp/claude-sessions` | `StartRotationWithRelay` | v1 |
| `TestRelay_AssistantTurn_BroadcastsMessageEnvelope` | `relay_assistant_turn_test.go` | `tmp/claude-sessions` | `StartRotationWithRelay` | v1 |
| `TestRelayV2_AssistantTurn_BroadcastsMessageEnvelope` | `relay_v2_daemon_test.go` (func at L418) | `tmp/claude-sessions` | `StartRotationWithRelay` | v2 |
| `TestRelay_Roundtrip_Appendix` | `relay_roundtrip_test.go` | `tmp/claude-sessions` | `StartRotationWithRelay` | v1 |
| `TestTwoPhoneStructured_InteractiveReceivesStream` | `relay_two_phone_structured_test.go` | **aligned already** | `StartRotationWithRelay` | v2 |

## Files to read first

- `internal/e2e/internal/fakeclaude/main.go` — the **whole file** (~284 lines); the shared fix lives here. Key sites: `main()` poll loop (L107-155), `startStdinReader` (L227-260, the single stdin consumer + `spinnerEmitted` one-shot), `emitStructuredJSONLIfTriggered` (L188-201, the existing main-goroutine `f.Write` + the single-writer-of-`f` invariant documented at L187), `openSession` (L203-216, the `{}\n` write to mirror), and the `stdoutMu` / package-var idiom (L85-105).
- `internal/supervisor/supervisor.go:284-401` — `WriteUserTurn` growth branch + `confirmViaTranscriptGrowth` + `grew`. Confirms: baseline captured **after** WaitReady and **before** deliver; growth = newer file OR larger size; poll = `transcriptConfirmPoll` (150 ms, L44), timeout = `transcriptConfirmTimeout` (10 s, L40). This is the contract the stub must satisfy — do not change it.
- `internal/sessions/reconcile.go:37-116` — `DefaultClaudeSessionsDir`, `mostRecentJSONL`, `newTranscriptResolver`. Confirms the resolver scans the computed dir and returns `("",0,nil)` (no error) for an empty-but-present dir.
- `internal/sessions/rotation/watcher.go:97` — the `os.MkdirAll(cfg.Dir, 0o700)` that makes the computed dir exist-but-empty (why there's no WARN).
- `internal/e2e/relay_two_phone_structured_test.go:141-158` — **the alignment pattern to copy verbatim**: compute `sessionsDir = filepath.Join(home, ".claude", "projects", encodeWorkdir(home))`, `os.MkdirAll(…, 0o700)`, pre-create `<initialUUID>.jsonl` with `[]byte("{}\n")` **before** the daemon starts. Also note its comment on *why* pre-create-before-start avoids the cold-start offset race.
- `internal/e2e/rotation_test.go:15-61` — `encodeWorkdir` test helper (L19; package-level, already shared by all e2e tests) and the same pre-create pattern via `StartRotation`.
- `internal/e2e/harness.go:267-360` — `StartRotation` / `StartRotationWithRelay`. Both already `os.MkdirAll(sessionsDir, 0o700)` and pass `-pyry-workdir=home` + `PYRY_FAKE_CLAUDE_SESSIONS_DIR=sessionsDir`. The test still pre-creates the JSONL itself (so it exists before the daemon's first reconcile/resolve).
- `internal/e2e/respawn_after_eviction_test.go:37-67, 242-265` — the one test whose start helper is local (`startEvictionHarness`); same MkdirAll + env wiring shape.
- `internal/turnbridge/mapper.go:20-73` — confirms a line with empty/unknown `type` (i.e. `{}`) maps to `(nil, false)` → **no structured event**. This is why an inert `{}\n` growth line is safe for the v2/two-phone structured assertions.

## Design

Two coordinated changes, both inside `internal/e2e/**`.

### Change 1 — fakeclaude grows its live session JSONL on a delivered turn (the shared fix)

A user turn reaches fakeclaude as **stdin bytes** (the supervisor `DeliverPrompt` → PTY write). The stub already observes those bytes in the `startStdinReader` goroutine (where the TUI spinner one-shot fires). The fix makes a delivered turn **grow the live session JSONL `f`** so the daemon's growth-confirm observes it — while preserving the **single-writer-of-`f` invariant** (`main.go:187`: only the main goroutine writes `f`).

**Mechanism (cross-goroutine signal, write stays on the main goroutine):**

- Add a package-level `var turnPending atomic.Bool` alongside the existing `stdoutMu` / glyph package vars (add `"sync/atomic"` to imports).
- In the `startStdinReader` goroutine: on every read with `n > 0`, call `turnPending.Store(true)`. This is a **signal only** — it does **not** touch `f`. (It sits next to, and is independent of, the existing `spinnerEmitted` one-shot.)
- In `main()`'s poll loop (alongside the rotate / assistant / jsonl-trigger checks): `if turnPending.Swap(false) { <grow f> }`. The grow appends one inert line to the **current** `f` and fsyncs — mirror `openSession`'s `f.WriteString("{}\n")` + `f.Sync()`, best-effort (silence errors, like `emitStructuredJSONLIfTriggered`).

Contract of the grow step (keep it a ~5-line helper or inline; do **not** write a long block):

```
// grow the current session JSONL by one inert line so confirmViaTranscriptGrowth observes growth.
// Runs ONLY on the main goroutine (preserves the single-writer-of-f invariant). Inert "{}\n":
// the turnbridge mapper maps an empty/typeless line to (nil,false), so it injects no structured event.
appendTurnGrowth(f *os.File)   // f.WriteString("{}\n"); f.Sync()  — best-effort
```

**Why this shape:**
- **Single-writer-of-`f` preserved** — the stdin goroutine never writes `f`; it only flips an atomic. All three `f` writers (`openSession`, `emitStructuredJSONLIfTriggered`, the new grow) run on the main goroutine. No mutex on `f`.
- **Inert payload** — `{}\n` is the exact line `openSession` already writes; the turnbridge mapper returns `(nil,false)` for it (`mapper.go:72-73`), so the v2 structured producer tails it and emits nothing. The coarse `message` path is stdout-derived and untouched. So the growth append is invisible to every assertion except "the file got bigger."
- **`Swap(false)` per poll cycle** — collapses chunked stdin into one append per ~50 ms `pollInterval`, and re-arms for a later turn (robust for any per-process turn count; the six tests each deliver one turn per fakeclaude process). The 50 ms poll is far inside the 10 s confirm timeout and ahead of the 150 ms confirm poll.
- **Ordering vs baseline is race-free** — the supervisor captures the baseline *after* `WaitReady` and *before* `deliver`; stdin bytes (hence any grow) can only arrive *after* `deliver`, so a grow always lands strictly past the baseline.

**Activation scope (blast radius):** the stdin reader runs only when `logPath != "" || tui` (`main.go:119`). All six target tests set `PYRY_FAKE_CLAUDE_TUI=1`; the other 65 e2e tests (`StartRotation`, the fakeclaude primitive, attach-stdio) set neither, so the stdin reader never starts and **no growth ever fires for them**. The `info.Size() != 0` check in `fakeclaude_test.go:58` only tightens under growth. No collateral change.

### Change 2 — align the 5 unaligned tests' sessionsDir to the daemon's computed dir

For each of the five unaligned tests, replace the `tmp/claude-sessions` sessionsDir with the daemon's computed dir and pre-create the initial JSONL **before** the harness start call, following `relay_two_phone_structured_test.go:141-158` exactly:

- Replace `sessionsDir := filepath.Join(tmp, "claude-sessions")` with
  `sessionsDir := filepath.Join(home, ".claude", "projects", encodeWorkdir(home))`.
- `os.MkdirAll(sessionsDir, 0o700)` (with a `t.Fatalf` on error) — the harness also MkdirAlls, but the pre-create write below needs the dir first; the harness call is then idempotent.
- After the existing `initialUUID := …`, add
  `initialJSONL := filepath.Join(sessionsDir, initialUUID+".jsonl")` and
  `os.WriteFile(initialJSONL, []byte("{}\n"), 0o600)` (with `t.Fatalf` on error), **before** the `StartRotationWithRelay` / `startEvictionHarness` call.
- Keep `tmp := t.TempDir()` — each test still uses `tmp` for its trigger/stdinLog paths.

`encodeWorkdir` is already a package-level e2e test helper (`rotation_test.go:19`); no new helper. The sixth test (`TestTwoPhoneStructured_…`) is already aligned, so **Change 1 fixes it with zero test-file edits** (AC: "the shared stub change fixes it").

Pre-creating before the daemon starts matters for two reasons established by the rotation/two-phone pattern: (1) the bootstrap reconcile (`reconcileBootstrapOnNew`) and the first growth-confirm baseline resolve at a tiny offset rather than racing fakeclaude's own open; (2) for the v2 leg, the structured producer captures its tail offset just past the pre-created `{}` rather than past a later-appearing file.

## Concurrency model

One added cross-goroutine edge, fully covered by the existing structure:

```
stdin-reader goroutine                main goroutine (poll loop, 50ms)
──────────────────────                ────────────────────────────────
os.Stdin.Read → n>0                   rotate-trigger?  → close old f, open new f   (writes f)
  turnPending.Store(true)  ───────▶   turnPending.Swap(false)? → appendTurnGrowth(f) (writes f)
  spinner one-shot (stdout, stdoutMu) assistant-trigger? → writeStdout (stdout)
  logF.Write/Sync (logF, not f)       jsonl-trigger?     → f.Write/Sync             (writes f)
```

- The atomic is the only shared mutable state added; `Store`/`Swap` need no lock.
- **All four `f` writers remain on the main goroutine** — invariant intact, no `f` mutex introduced.
- `f` may be reassigned by the rotate branch earlier in the same iteration; the grow always targets the current `f` (the six tests use a never-created rotate trigger, so rotation never fires here, but the ordering is coherent regardless).
- `stdoutMu` already serializes stdout; the grow touches only `f`, never stdout, so it cannot interleave with the spinner or assistant chunk.

## Error handling

- The grow is **best-effort**, mirroring `emitStructuredJSONLIfTriggered` and `writeStdout`: a failed `Write`/`Sync` is silenced. The e2e asserts downstream (the phone receives the ack/broadcast), never on the stub's write succeeding. A persistently-failing write surfaces as the original `ErrTurnNotCommitted` (loud, retryable) on the daemon side — never a false ack — so there is no silent-pass risk.
- No new failure modes; no production paths touched.

## Testing strategy

This ticket *is* test work; the "tests" are the six target tests going green plus the suite staying green. No new test functions are required — the stub change + alignment make the existing assertions pass.

Developer must verify (these map 1:1 to the ACs):

- **AC1:** `go test -tags e2e ./internal/e2e/ -count=1` → fully green (6 FAIL → 0 FAIL, other 65 stay green). Then run the six named tests at `-count=3` (3/3 each), e.g. `go test -tags e2e ./internal/e2e/ -run 'TestE2E_IdleEviction_RespawnsOnSendMessage|TestRelay_SendMessage_AckAndPTYDelivery|TestRelay_AssistantTurn_BroadcastsMessageEnvelope|TestRelayV2_AssistantTurn_BroadcastsMessageEnvelope|TestRelay_Roundtrip_Appendix|TestTwoPhoneStructured_InteractiveReceivesStream' -count=3`.
- **AC2:** during a run, confirm the daemon stderr shows the ack path (no `supervisor: transcript baseline resolve failed; falling back to commit heuristic` WARN, and no `turn not committed`). The ack is driven by **observed growth** through `confirmViaTranscriptGrowth`, not the fallback and not a stub bypass. (A quick way: keep the alignment but temporarily revert Change 1 → the six fail with `turn not committed`; with both changes they pass. The developer need not script this — it's the mental model for confirming the right path.)
- **AC3:** no `t.Skip` added; every one of the six drives its full path (wake-from-idle → respawn → deliver, or deliver → assistant broadcast).
- **AC4:** `git diff --stat` touches only `internal/e2e/**`. Run `go build ./... && go vet ./...` to confirm no production breakage. (`make check` does **not** run `-tags e2e`; that gate is out of scope per the ticket.)

Reminder (`[[pyrycode-gofmt-dirty-at-head-go1.26]]`): the repo can read gofmt-dirty at HEAD under a newer local Go than CI's pinned version. Only `gofmt -w` the files you actually edit; do not reformat untouched files.

## Open questions

- **None blocking.** The inert `{}\n` growth line is the safe default (proven no-op through the mapper). If a future structured test ever needs the *growth* line itself to carry a real event, it can keep using the existing `PYRY_FAKE_CLAUDE_JSONL_TRIGGER` mechanism — orthogonal to this change.
- Whether to gate the grow behind `tui` specifically (vs. the broader `logPath != "" || tui` activation of the stdin reader): not necessary — the reader only runs in those modes and all six tests set `tui=1`; growing whenever the reader is active is the simplest faithful model of "claude commits a delivered turn to its JSONL."
