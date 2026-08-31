# Clear-rotate mode (#1004)

`PYRY_FAKE_CLAUDE_CLEAR_ROTATES` makes the **phone-driven `new_session` keystroke
itself** rotate the session — the harness piece behind the `new_session` v2
control-verb e2e ([codebase/1004.md](../codebase/1004.md)). The `new_session` path
routes a phone frame → `handleNewSession` → `supervisor.StartNewSession()` →
`ClearInputLine` (Ctrl-U) + `TypePrompt("/clear")` + `"\r"` into the supervised
child's stdin. Real claude rotates its session UUID on `/clear`; in this mode
fakeclaude detects the `/clear` bytes and performs the same close-old/open-new
rotation the file trigger does, so the daemon's rotation watcher — and thus the
registry's on-disk bootstrap id — moves **because of** the `new_session` frame,
not vacuously. (Reusing the file trigger unchanged would leave `new_session`
untested — nothing in the harness would ever point the trigger at a real file for
this verb, since the whole point is proving the *keystroke* causes the rotation.)

When `PYRY_FAKE_CLAUDE_CLEAR_ROTATES` is set:

| Moment | Action | Effect |
|---|---|---|
| stdin read | reader accumulates bytes across reads, checks `containsClearCommand(buf)` | sets `clearRotatePending` (signal only) once `/clear` has fully arrived |
| main poll loop, `!rotated && clearRotatePending.Swap(false)` | `f = rotateSession(f, dir)`, set `rotated = true` | closes the old JSONL, opens a fresh `<uuid>.jsonl` — the rotation watcher follows it into the registry |

- **Detection is discipline-agnostic.** `containsClearCommand(buf) = bytes.Contains(buf,
  []byte("/clear"))` over the reader's accumulated buffer, so a `/clear` split
  byte-by-byte under a hypothetical raw-mode caller and the single `/clear\n` line
  the default **canonical** discipline delivers (this mode's actual case) both
  match. In this mode the only stdin fakeclaude ever receives is that one
  keystroke sequence, so a plain substring match cannot false-positive.
- **No raw mode needed.** Unlike Esc-ends-turn mode, this mode does **not** call
  `enterRawMode()`. `ClearInputLine`'s leading Ctrl-U (`0x15`) is consumed by the
  canonical line discipline as VKILL on an empty line (a no-op), and the trailing
  `\r` from `TypePrompt` commits the line, so a single read already carries the
  full `/clear\n` — no withheld-until-newline problem the way a lone ESC has.
- **Signal-only from the reader, shared `rotated` gate.** `clearRotatePending
  atomic.Bool` mirrors `escPending`/`turnPending` exactly — the reader never
  touches `f`; `rotateSession` runs only on the main poll goroutine. Reusing the
  existing `rotated` one-shot bool (rather than a fresh gate) means a test can
  never double-rotate, and the file-trigger and clear-rotate paths are mutually
  exclusive in practice — whichever fires first wins, the other becomes inert.
- **`rotateSession(f, dir) *os.File`** is a small helper extracted from the file
  trigger's inline close/open so both branches share one implementation
  (`f.Close()`; `return openSession(dir, uuidV4())`). The old-fd close is
  best-effort — every write is already fsynced by `openSession`/`appendTurn*`, so
  a failed close can't lose committed data.
- **No new glyph, no allowlist change.** The mode touches only stdin detection and
  the JSONL file rotation — no stdout writes, so `cmd/substrate-guard` is
  unaffected. **When unset, byte-identical to today** — every existing caller
  (`StartRotation`, the other e2e tests) is unperturbed.

Pinned by the untagged `containsClearCommand` table test in
`clear_detect_test.go`, mirroring `esc_detect_test.go`'s `TestContainsBareESC`.

See [codebase/1004.md](../codebase/1004.md) for the live `new_session` e2e this mode
feeds (the structural-causality guard — the file trigger points at a never-created
path so `/clear` is the only rotation source — and the bounded re-send loop that
makes the fire-and-forget verb deterministic against session-attach timing).
