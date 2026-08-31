# Idle-trigger mode (#792)

TUI mode (#603) emits the idle glyph `❯` at **startup**, so claude is idle
immediately and can serve exactly one turn (the spinner then wedges "thinking").
`PYRY_FAKE_CLAUDE_IDLE_TRIGGER` (#792) **inverts the startup posture** to give a
test a controllable **busy → free** window: fakeclaude comes up **busy** and
frees only when the test drops the trigger — so a phone can pile up an inbound
backlog that then drains back-to-back. It is the harness piece that made the
**live** queued-backlog capstone exercisable (what #723's `/bin/sleep` child
could not do: never idle → never drains).

When `PYRY_FAKE_CLAUDE_IDLE_TRIGGER` is set:

| Moment | Action | Effect |
|---|---|---|
| startup | emit **no** idle glyph, **never** the spinner | tui-driver `IsIdle` stays false → supervisor `WaitReady` **blocks** (claude "busy") |
| trigger's **first** appearance | `emitIdleIfTriggered`: `writeStdout(❯)` once, `os.Remove(trigger)`, set one-shot `idled` | `IsIdle` true → `WaitReady` returns; claude stays idle thereafter |

- **The single `❯` write frees the whole backlog.** After the glyph, **nothing
  overwrites the bottom status region** — the delivered prompt is not echoed to
  stdout — so `WaitReady` returns immediately for every subsequent queued turn
  and they drain one after another with no further signal. This is why the mode
  is ~15 LOC: no spinner, no scroll/redraw emulation, no per-turn cycling. The
  load-bearing tui-driver fact (v1.6.0): `IsIdle` = `❯` present in the bottom
  region AND no spinner there — *withholding* `❯` is a busy gate, *emitting* it
  once is the release edge.
- **Gated by a one-shot `idled` bool** in the `main` poll loop, exactly like the
  `rotated` rotation gate — the glyph is emitted at most once. `emitIdleIfTriggered`
  runs **only on the main goroutine**, so it never races the stdin reader for
  `os.Stdout` (and in this mode `PYRY_FAKE_CLAUDE_TUI` is off, so the reader emits
  no spinner — the main goroutine is the sole stdout writer). Errors are silenced,
  mirroring the sibling `emit*` helpers.
- **Per-turn commit relies on `STDIN_LOG`, not on this mode.** Idle-trigger does
  **not** itself open the stdin reader (the gate is still `logPath != "" || tui`,
  `main.go:692`). The #792 consumer sets `PYRY_FAKE_CLAUDE_STDIN_LOG` (via
  `StartRotationWithRelay`), so the reader runs, `turnPending` fires, and the
  existing `appendTurnGrowth(f)` grows the JSONL that the supervisor's
  `confirmViaTranscriptGrowth` (#668/#673) observes as the commit. No spinner is
  needed — the relay bootstrap sets `ResolveTranscript`, so commit is growth-based.
- **Mutually exclusive with `PYRY_FAKE_CLAUDE_TUI`.** TUI mode's startup `❯` and
  stdin-spinner would defeat the busy window and wedge the second turn. A test
  sets one or the other, never both.
- **No new glyph, no allowlist change.** `❯` (`idleGlyph`) is already declared and
  the file is already on the `cmd/substrate-guard` allowlist; the mode only
  changes *when* the existing glyph is emitted. **When unset, byte-identical to
  today** — every existing caller is unperturbed.

See [codebase/792.md](../codebase/792.md) for the live queue-drain capstone this
mode feeds (the busy→free choreography, the empty-`queue_state` happens-after
fence, the ordered vacuous-pass guards).
