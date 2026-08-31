# Modal-clear-on-answer mode (#793)

Modal mode (`PYRY_FAKE_CLAUDE_MODAL_TRIGGER`, #791) raises a permission prompt
(`modalScreen`, the `"Do you want to proceed?"` anchor) and **never clears it** — enough
for #791, whose phone answers and whose daemon **remote** arm broadcasts the dismissal
regardless of claude's screen. But the #706 **local** first-answer-wins arm
(`handleModalHidden`) is `EventKindPtyModalHidden`-driven, and that event fires only on a
genuine `Permission→Unknown` screen transition. `PYRY_FAKE_CLAUDE_MODAL_CLEAR_ON_ANSWER`
(#793) closes that gap: after the modal is shown it clears it on the local head's answer
keystroke, making the keystroke the **cause** of the Hidden event — the harness piece
behind the live two-head first-answer-wins capstone
([codebase/793.md](../codebase/793.md)). (Reusing #791's fake unchanged would leave the
local arm dead; changing `MODAL_TRIGGER` itself to clear would race #791's remote arm on
the same `modal_id` — hence a **new** default-off env.)

When `PYRY_FAKE_CLAUDE_MODAL_CLEAR_ON_ANSWER` is set **and** `MODAL_TRIGGER` is set (it is
a no-op without modal mode):

| Moment | Action | Effect |
|---|---|---|
| stdin read (any bytes) | `clearPending.Store(true)` (signal only, never touches stdout or `f`) | mirrors `turnPending`/`escPending` — the reader flags, the main goroutine acts |
| main poll loop, `modalShown && !modalCleared && clearPending.Swap(false)` | `clearModalScreen()` → `writeStdout(modalClearScreen)` + fsync, set one-shot `modalCleared` | `DetectModalClass` Permission→Unknown → merge loop fires `EventKindPtyModalHidden` → #706 local arm `Resolve`s → `modal_dismissed{local}` |

- **The `modalShown` gate defers the clear to a post-modal keystroke.** `clearPending` is
  set on every read, but the main-loop short-circuit leaves it latched until the modal is
  up, then fires on the first post-modal byte — a pre-modal keystroke cannot clear early.
- **`modalClearScreen = strings.Repeat("\r\n", 16) + string(idleGlyph)`.** The 16 blank
  lines (`modalClearScrollRows`) scroll the `"Do you want to proceed?"` anchor above the
  bottom `permissionRegionRows` (12) window `DetectModalClass` scans (the fixed 40-row
  `DefaultPtyRows` grid — `DetectModalClass` always renders at 40×120 regardless of the
  PTY's live, possibly attach-resized, dimensions). The **trailing idle glyph is
  load-bearing, not decoration**: `trimGridText` drops trailing empty rows, so a
  newline-only clear screen collapses back and the anchor re-enters the detection window,
  still classifying as Permission — a non-empty final row pins the grid height.
- **Single-writer discipline preserved.** The stdin reader only signals (`clearPending
  atomic.Bool`); `clearModalScreen` runs on the main poll goroutine (modal mode emits no
  spinner, so the main goroutine is the sole stdout writer). No mutex on `f`, no new writer
  goroutine — the same shape as `turnPending`/`escPending`.
- **One-shot** via `modalCleared`; a second keystroke is inert.
- **No new glyph, no allowlist change.** `modalClearScreen` reuses the already-declared
  `idleGlyph` (`❯`), so the `cmd/substrate-guard` seal is untouched. **When unset,
  byte-identical to today** — #791 sets only `MODAL_TRIGGER` and its fake never clears, so
  its remote arm stays unaffected.

**De-risk the fixture harness-free.** `TestFakeClaude_ModalClearScreenReturnsToNonPermission`
(untagged, in `modal_detect_test.go`) asserts `DetectModalClass(modalScreen+modalClearScreen)
!= Permission` — feeding the concatenated bytes to a fresh detector reproduces the daemon's
sequential-write vt10x state deterministically, so it predicts the live `Permission→Unknown`
transition in milliseconds. A non-clearing fixture (too few newlines, or a newline-only
screen `trimGridText` collapses) fails here rather than timing out inside a slow live-daemon
run. The original `modalScreen == Permission` assertion (#791) is unchanged.

See [codebase/793.md](../codebase/793.md) for the live two-head first-answer-wins capstone
this mode feeds (the local `pyry attach` head bound before the modal is raised, the two
ordered observe-positives gating the loser-no-op, the `dismissed_local` live audit oracle).
