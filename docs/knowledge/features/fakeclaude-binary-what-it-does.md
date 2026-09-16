# What It Does

The binary mimics exactly the externally observable behaviour
`internal/sessions/rotation`'s watcher cares about: a tracked PID has one
JSONL fd open at any moment, and on `/clear` the PID closes the old fd
and opens a new one in the same directory. It does **not** mimic claude's
stdin/stdout protocol, conversation content, or any other surface — the
rotation watcher only observes the fd table and the directory.

The one exception is the opt-in **TUI mode** (`PYRY_FAKE_CLAUDE_TUI`, #603):
it emits exactly two of claude's TUI substrate glyphs so tui-driver's
`IsIdle`/`IsThinking` detection — and #594's `WaitReady → DeliverPrompt →
commit` contract — can confirm a turn against it. See
[§ TUI mode](#tui-mode-603). The earlier optional `STDIN_LOG` (#323) and
`ASSISTANT_TRIGGER` (#311) modes, the **JSONL-trigger mode**
(`PYRY_FAKE_CLAUDE_JSONL_TRIGGER`, #642) that appends captured claude-format
turn events to the live session JSONL, the **idle-trigger mode**
(`PYRY_FAKE_CLAUDE_IDLE_TRIGGER`, #792) that opens a controllable *busy → free*
window by withholding the startup idle glyph until a trigger fires, and the
**Esc-ends-turn mode** (`PYRY_FAKE_CLAUDE_ESC_ENDS_TURN`, #794) that watches
stdin for the remote interrupt's bare ESC and, on finding it, appends one canned
`end_turn` line so the Esc *causes* the turn to stop, and the **modal-clear-on-answer
mode** (`PYRY_FAKE_CLAUDE_MODAL_CLEAR_ON_ANSWER`, #793) that **extends** modal mode
(`PYRY_FAKE_CLAUDE_MODAL_TRIGGER`, #791 — the permission-prompt raiser): after the modal
is shown it clears it on the first post-modal stdin byte (the local `pyry attach` head's
answer keystroke) so tui-driver fires `EventKindPtyModalHidden` and the daemon's #706
**local** first-answer-wins arm resolves it, likewise
extend the binary past pure rotation; see [§ Configuration](#configuration--env),
[§ JSONL-trigger mode](#jsonl-trigger-mode-642),
[§ Idle-trigger mode](#idle-trigger-mode-792),
[§ Esc-ends-turn mode](#esc-ends-turn-mode-794), and
[§ Modal-clear-on-answer mode](#modal-clear-on-answer-mode-793). The earlier
**clear-rotate mode** (`PYRY_FAKE_CLAUDE_CLEAR_ROTATES`, #1004) — which watched stdin
for the `/clear` bytes `supervisor.StartNewSession` typed on a phone's `new_session`
frame — was retired by #2456: it had been stranded since #1348 deleted the terminal
supervisor that drove it, and a client's `new_session` now rotates through
`activeSessionStarter.start` instead of a PTY keystroke. Whenever the stdin reader is
active (TUI or `STDIN_LOG`), a delivered turn also triggers **on-turn
transcript growth** (#673): the live session JSONL grows by one inert line so
the daemon's #668 transcript-growth commit-confirm observes growth and acks
(otherwise it times out with `ErrTurnNotCommitted`); see
[§ On-turn transcript growth](#on-turn-transcript-growth-673).

| Step | Effect |
|---|---|
| Start | open `<dir>/<initialUUID>.jsonl` `O_WRONLY\|O_APPEND\|O_CREATE 0o600`, `WriteString("{}\n")`, `Sync()` |
| Idle | poll trigger file every 50ms |
| Trigger | `f.Close()` (OLD), mint `uuidV4()`, open `<dir>/<newU>.jsonl` (NEW), write+fsync, `os.Remove(trigger)`, set `rotated=true` |
| Idle (post-rotation) | poll continues; trigger reappearance ignored |
| Delivered turn (stdin bytes, TUI/`STDIN_LOG` only) | reader sets `turnPending`; main loop appends `{}\n`+fsync to current `f` (growth-confirm signal, #673) |
| SIGTERM | Go runtime default-terminates; OS auto-closes the open fd |

Strict close-OLD-before-open-NEW is **load-bearing**. The downstream
rotation-watcher test relies on the platform probe (`/proc/<pid>/fd` on
Linux, `lsof` on macOS) seeing exactly one path on the PID's fd table at
the instant the watcher's CREATE-driven probe runs. If the binary held
both fds open across the rotation, the probe could match either path and
the watcher's exact-match gate (`watcher.go:167`) would race.
