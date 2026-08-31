# JSONL-trigger mode (#642)

`PYRY_FAKE_CLAUDE_ASSISTANT_TRIGGER` (#311) feeds the *coarse* path by writing
to **stdout** (which the PTY bridge forwards as a `message` chunk).
`PYRY_FAKE_CLAUDE_JSONL_TRIGGER` (#642) is its **structured-path** sibling: it
appends captured **claude-format JSONL turn events** to the **live session
JSONL file** — the file the daemon's structured-turn producer
(`cmd/pyry/interactive_turn_stream_v2.go`) tails — so an `interactive`-granted
phone receives the real structured envelope stream (`turn_state` /
`assistant_delta` / `tool_use` / `turn_end`). It is the harness piece that made
the live structured-receive capstone exercisable (option (b): fakeclaude
replays a captured transcript, rather than fusing real-claude with the
Noise-phone suite).

`emitStructuredJSONLIfTriggered(f, path)` mirrors `emitAssistantIfTriggered`
but appends to the session `*os.File` instead of stdout:

| Step | Effect |
|---|---|
| trigger appears | `os.ReadFile(path)`, cap at `assistantMaxBytes` |
| **empty read** | **zero-byte read → `return` WITHOUT removing the trigger (#958); the next ~50ms poll retries once the producer's write lands** |
| append | `f.Write(data)` verbatim to the live `<uuid>.jsonl` (already `O_APPEND`) |
| flush | **`f.Sync()`** — load-bearing for cross-process tail visibility |
| consume | `os.Remove(path)` |

- **The trigger file's contents ARE the JSONL lines to append** — same
  "contents are the payload" shape as the assistant trigger. The test supplies
  complete `\n`-terminated claude-format objects (modeled on
  `internal/turnbridge/mapper_test.go`'s `entry(...)` oracle); tui-driver's
  `TailJSONL` reassembles them into `turnevent.Event`s.
- **`f.Sync()` is load-bearing.** The daemon's tail is a separate process and
  macOS APFS otherwise defers cross-process visibility (the same reason the
  stdin reader fsyncs per write). Without it the producer may never see the
  appended bytes.
- **A zero-byte read is the producer's `open(O_TRUNC)`-before-`write` window,
  not a payload — it must not remove the trigger (#958).** Every producer drop
  is `os.WriteFile` (`O_CREATE|O_TRUNC` then a single `write`), so the trigger
  exists-but-empty for a brief window after truncation. A poll landing there
  used to `os.Remove` the trigger anyway, unlinking it before the producer's
  content was ever visible and losing the structured test's `sync.Once`
  `dropFull` line — the intermittent full-suite-`-race` flake in
  `TestTwoPhoneStructured_InteractiveReceivesStream` /
  `TestRelayV2_InterruptStopsRunningTurn`. See [codebase/958.md](../codebase/958.md).
- **Errors are silenced** (read/write/remove) — a missing trigger is the steady
  state, and the e2e asserts the outcome downstream (the interactive phone
  receives the structured envelopes).
- **Only the main goroutine writes `f`**, so the append never races the stdin
  reader. No new glyphs are emitted, so the **substrate-guard allowlist is
  unchanged** — the appended bytes are JSON the test supplies, not TUI
  substrate.
- **When unset, behaviour is byte-identical to today** — every existing caller
  is unperturbed. Off by default, like the other optional modes.

Two harness preconditions make the appended events actually reach the producer
(both handled by the #642 test, not by fakeclaude):

1. **Sessions-dir alignment.** `resolveClaudeSessionsDir` has no env override —
   it always computes `<HOME>/.claude/projects/encode(workdir)`. The test points
   fakeclaude at that **same** computed dir so the producer tails exactly what
   fakeclaude writes (the `rotation_test.go` alignment pattern).
2. **Pre-create `<initialUUID>.jsonl` before the daemon starts.** The producer
   captures its tail offset at the first resolve; pre-creating the file makes
   that resolve succeed at startup, seconds before the post-ack append, so every
   appended line lands inside the tailed range (fixes a cold-start
   producer-subscribe race — see [codebase/642.md](../codebase/642.md)).
