# Related

- Spec: `docs/specs/architecture/122-fake-claude-test-binary.md`;
  TUI mode: `docs/specs/architecture/603-fakeclaude-tui-idle-thinking-glyphs.md`;
  JSONL-trigger mode: `docs/specs/architecture/642-structured-receive-two-phone-e2e-capstone.md`;
  idle-trigger mode: `docs/specs/architecture/792-queue-drain-two-phone-e2e-capstone.md`;
  Esc-ends-turn mode: `docs/specs/architecture/794-interrupt-stops-turn-two-phone-e2e-capstone.md`;
  modal-clear-on-answer mode: `docs/specs/architecture/793-two-head-first-answer-wins-e2e-capstone.md`;
  on-turn growth: `docs/specs/architecture/673-fakeclaude-transcript-growth.md`;
  clear-rotate mode (retired #2456): `docs/specs/architecture/1004-new-session-e2e.md`;
  stream-json mode: `docs/specs/architecture/1140-fakeclaude-stream-json-mode.md`;
  interrupt rider: `docs/specs/architecture/1136-stream-e2e-interrupt.md`;
  new_session rider: `docs/specs/architecture/1137-stream-new-session-rotation-e2e.md`;
  startup-hold rider: `docs/specs/architecture/1138-stream-e2e-queue-drain-in-order.md`;
  approve rider: `docs/specs/architecture/1139-stream-e2e-permission-round-trip.md`;
  argv-derived stem + per-child JSONL trigger modes:
  `docs/specs/architecture/1195-minted-perconv-pty-transcript-substrate.md`
- TUI mode per-ticket notes: [codebase/603.md](../codebase/603.md) (glyph
  emission, the ack-pollution drain, the substrate-guard exemption)
- JSONL-trigger per-ticket notes: [codebase/642.md](../codebase/642.md) (the
  structured-receive capstone it feeds, the sessions-dir alignment +
  pre-create-JSONL preconditions, the cold-start producer-subscribe race)
- On-turn growth per-ticket notes: [codebase/673.md](../codebase/673.md) (the
  cross-goroutine `turnPending` signal, the #668 commit-confirm it satisfies, the
  five `sessionsDir` alignments, the six broken tests)
- Idle-trigger per-ticket notes: [codebase/792.md](../codebase/792.md) (the live
  queue-drain capstone this mode feeds — the busy→free window, the empty-`queue_state`
  happens-after fence, and the ordered vacuous-pass guards)
- Esc-ends-turn per-ticket notes: [codebase/794.md](../codebase/794.md) (the live
  interrupt capstone this mode feeds — the Esc-drives-the-flip structural causality,
  the bare-ESC discriminator vs paste markers, the two-oracle belt-and-suspenders)
- Modal-clear-on-answer per-ticket notes: [codebase/793.md](../codebase/793.md) (the live
  two-head first-answer-wins capstone this mode feeds — the keystroke-is-the-cause
  structural causality, the local `pyry attach` head bound before the modal is raised, the
  two ordered observe-positives, the `dismissed_local` live audit oracle)
- Clear-rotate per-ticket notes (mode retired #2456): [codebase/1004.md](../codebase/1004.md)
  (the live `new_session` e2e this mode fed — the never-created file-trigger
  structural-causality guard, and the bounded re-send loop that made the fire-and-forget
  verb deterministic). The mode itself is gone: stranded since #1348 deleted the terminal
  supervisor that typed `/clear` into a PTY, and nothing in the repo set its env var.
- Stream-json mode per-ticket notes: [codebase/1140.md](../codebase/1140.md) (the
  gate-above-mustEnv structural AC satisfier, the echo-the-prompt response, the
  different-fabric real-`streamsup.Parser` verification); feeds the sibling #1135
  harness + `send_message` e2e spec (blocked-by this ticket)
- Interrupt rider per-ticket notes: [codebase/1136.md](../codebase/1136.md) (the
  withheld-result in-flight-turn trick, the `error_during_execution` →
  `TurnEndReasonCancelled` mapping, the minted-conversation live routing-target
  proof it feeds)
- New_session rider per-ticket notes: [codebase/1137.md](../codebase/1137.md) (the
  stream-path stdin tee, the on-disk-rotation-implies-`RestartFresh` reasoning, the
  post-rotation drain divergence it confirms live and defers to #1133)
- Startup-hold rider per-ticket notes: [codebase/1138.md](../codebase/1138.md) (the
  live queue-drain-in-order e2e this mode feeds — why a msgqueue backlog isn't
  observable on the stream path, and the FIFO stdin-pipe chain proof instead)
- Argv-derived stem + per-child JSONL trigger per-ticket notes:
  [codebase/1195.md](../codebase/1195.md) (the minted per-conversation
  turn-lifecycle PTY e2e these modes feed, the stem guard vs the daemon's
  `transcript.ValidStem`, the on-disk non-vacuity assertion pair, why the two
  knobs stay separate for #1191)
- Substrate seal: `cmd/substrate-guard/main.go` allowlists this file
  alongside `internal/agentrun/ptyrunner/helper_test.go` (the two sanctioned
  fake-claude helpers that emit claude-TUI glyphs)
- Mirrors: `internal/sessions/id.go` (`NewID` UUIDv4 generator),
  `internal/sessions/rotation/watcher.go:17-19` (`uuidStemPattern`)
- Lessons: `docs/lessons.md § Claude session storage on disk` (the
  on-disk shape the binary mimics)
- Consumers: `Harness.StartRotation` + `ensureFakeClaudeBuilt` (#123,
  landed — wires the binary into the e2e harness as the supervised child;
  see [e2e-harness.md § Rotation Primitive](e2e-harness.md)).
  Forthcoming: rotation-watcher driver test (slice after #123 — runs
  pyry's watcher against the binary).
- Pattern: always-split "new package AND its first consumer" — the
  binary lands here without its harness consumer to keep the AC count
  inside the per-ticket budget. Same shape as the introduce-then-rewire
  slicing pattern (#28 → #29).
