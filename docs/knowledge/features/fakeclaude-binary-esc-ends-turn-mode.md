# Esc-ends-turn mode (#794)

`PYRY_FAKE_CLAUDE_ESC_ENDS_TURN` made the **remote interrupt keystroke itself**
end the running turn — the harness piece behind the PTY-path interrupt capstone
([codebase/794.md](../codebase/794.md)). The interrupt path routed a phone
`interrupt` frame → `handleInterrupt` → `supervisor.SendEsc()` → a **lone `0x1b`**
into the supervised child's stdin. In this mode fakeclaude detects that bare ESC
and appends claude's own interruption marker to the live session JSONL. (Reusing
\#792's *file*-driven busy→idle flip would instead let an "interrupt stopped the
turn" assertion pass **vacuously** — the idle would come from a file, not the Esc.)

> **Orphaned since #1543.** The daemon reader that mapped the appended marker to
> a `turn_end{cancelled}` — the PTY path's `internal/turnbridge` producer and
> mapper — was deleted with that path, and no in-tree test sets
> `PYRY_FAKE_CLAUDE_ESC_ENDS_TURN` any more; #794's own test went with it. Live
> interrupt coverage now lives on the stream-json path
> ([streamsup-package.md](streamsup-package.md),
> `internal/e2e/relay_v2_stream_interrupt_test.go`), which classifies the
> interrupt in the parser rather than through a bare-ESC/session-JSONL detector.
> The mechanics below (raw mode, `containsBareESC`, the write itself) are kept as
> a record of a working harness piece, not a live one.

> Through #1244 (2026-07-31), this appended an assistant `stop_reason:"end_turn"`
> line (`interruptEndTurnLine`) — a shape that looked like a clean completion on
> disk and could only prove *causality*, never the stop reason, because
> `EventKindJsonlEndOfTurn` was the mapper's only `turn_end` source. #1243 added a
> second source — claude's own interruption marker (`turnbridge/mapper.go:95`) —
> and #1244 restaged this handler to write that marker instead. See
> [codebase/1244.md](../codebase/1244.md).

When `PYRY_FAKE_CLAUDE_ESC_ENDS_TURN` is set:

| Moment | Action | Effect |
|---|---|---|
| startup | `enterRawMode()` (like modal mode) | a lone ESC with no line terminator reaches `read()` verbatim (canonical discipline would otherwise withhold it) |
| stdin read containing a bare ESC | `containsBareESC(buf)` → set `escPending` (signal only) | stdin reader flags the interrupt without touching `f` |
| main poll loop, `escPending.Swap(true)`, one-shot `escEnded` | `appendTurnEnd(f)`: write `interruptMarkerLine` + `f.Sync()` | before #1543: the PTY producer tailed it → `isInterruptMarker` → `TurnEnd{Cancelled}` → `turn_end{cancelled}` to the phone. Now: no reader |

- **A bare ESC is unambiguously the interrupt.** The stdin stream carries exactly
  three ESC sources — bracketed-paste open (`ESC[200~`), close (`ESC[201~`), and
  the interrupt's lone `0x1b`. Both paste markers are `0x1b` immediately followed by
  `'['` (`0x5b`); the delivered prompt content between them is raw-ESC/C0-free
  (#749's paste-content guard), so it contributes no `0x1b`. `containsBareESC` rule:
  a `0x1b` at index `i` is bare iff `i == len(buf)-1` (last byte) **or**
  `buf[i+1] != 0x5b`. The last-byte arm is safe because tui-driver writes each paste
  marker as one `writeRaw` of the whole `ESC[200~…ESC[201~\r` unit (its `[` always
  follows in the same read), whereas `SendEsc` writes the lone `0x1b` standalone. A
  future multi-KiB prompt whose paste could split across reads on a marker's `0x1b`
  would need a one-byte cross-buffer carry — not built (this harness controls the
  prompt size). The discriminator is pinned by the untagged `esc_detect_test.go`
  (`TestContainsBareESC`).
- **Raw mode is load-bearing.** Unlike TUI mode, this mode enters raw mode: a lone
  ESC with no newline is withheld indefinitely by the default canonical line
  discipline, so without raw mode the detector would never see the interrupt (the
  `turn_end` would silently time out). The raw-mode gate is now
  `if modalTrig != "" || escEndsTurn { enterRawMode() }`.
- **Single-writer-of-`f` preserved.** The stdin reader only *signals*
  (`escPending atomic.Bool`, exactly like `turnPending`); the `appendTurnEnd(f)`
  write runs on the main poll goroutine. No mutex on `f`, no new writer goroutine.
- **`interruptMarkerLine` is a fixed literal, derived from a real capture** — the
  claude-format `type:"user"` interruption entry that the PTY path's mapper (now
  deleted, #1543) mapped to `turnevent.TurnEnd{Cancelled}`, byte-shaped after the
  repo's one recorded interruption entry
  (`internal/agentrun/jsonl/testdata/no_end_turn.jsonl:53`, claude 2.1.128): the
  full 13-key top-level set and its order preserved verbatim, only the
  session-identity fields (`parentUuid`, `promptId`, `uuid`, `timestamp`, `cwd`,
  `sessionId`, `gitBranch`) substituted with shape-preserving canned values. Two
  absences were load-bearing and silent if broken — no top-level `permissionMode`
  key (that mapper's human-authored-prompt presence check) and no `tool_result`
  block (its tool-result branch ran before the marker check and would have
  intercepted it) — either one would have made the mapper produce no `turn_end`
  at all. Both absences are kept because they match the captured base line, even
  though nothing reads them now. It is inert JSONL data, **not** a TUI substrate
  glyph, so the `cmd/substrate-guard` allowlist is unchanged.
- **One-shot.** The `escEnded` gate bounds the append to one end-of-turn line; a
  second ESC is inert — a re-interrupt of an already-ended turn is a no-op, matching
  claude.
- **Coexists with `PYRY_FAKE_CLAUDE_TUI`** (unlike idle-trigger's mutual exclusion).
  The two touch different bytes: TUI emits the startup `❯` + one spinner; the ESC
  detector scans for the bare ESC. The #794 capstone runs both ON. **When unset,
  byte-identical to today** — every existing caller is unperturbed.

While it had a reader, the turn_end carried `StopReason == "cancelled"` (since
\#1244; through \#1244 it was `"end_turn"`, because `EventKindJsonlEndOfTurn` was
the mapper's only `turn_end` source and could not distinguish an interrupt-stop
from a normal end). The PTY-tier consumer (`relay_v2_interrupt_test.go`, the #794
capstone) did not assert the reason — it proved **causality** (the `turn_end`
exists only because the Esc was received), leaving the reason assertion to the
minted-tier oracle one level up ([codebase/1244.md](../codebase/1244.md)). See
[codebase/794.md](../codebase/794.md) for the interrupt capstone this mode used
to feed (the structural-causality guard, the two ordered `t.Fatal`s, the
two-oracle belt-and-suspenders) — both the test and its `internal/turnbridge`
consumer were deleted with the PTY path (#1543); the framing there is historical
and not rewritten.
