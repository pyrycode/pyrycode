# On-turn transcript growth (#673)

\#668 made the supervised-bootstrap delivery path confirm a turn by observing the
resolved claude session JSONL **grow** past a pre-delivery baseline
(`confirmViaTranscriptGrowth`): `WriteUserTurn` returns `nil` only on growth, else
`ErrTurnNotCommitted` after a 10 s timeout. Real claude appends the turn at commit
time, so growth is guaranteed in production — but fakeclaude only wrote `{}\n` at
session open / rotation and **never grew on a delivered turn**, so every e2e test
that drives a turn timed out. #673 closes that fidelity gap: a delivered turn now
grows the live session JSONL by one inert line, the same on-disk signal a committed
turn produces.

A user turn reaches fakeclaude as **stdin bytes** (supervisor `DeliverPrompt` → PTY
write), observed in `startStdinReader`. The fix grows `f` **while preserving the
single-writer-of-`f` invariant** (only the main goroutine writes `f`):

| Site | Goroutine | Action |
|---|---|---|
| `startStdinReader`, on `n > 0` | stdin reader | `turnPending.Store(true)` — **signal only, never touches `f`** |
| `main()` poll loop, each cycle | main | `if turnPending.Swap(false) { appendTurnGrowth(f) }` — `f.WriteString("{}\n")` + `f.Sync()`, best-effort |

- **Signal across the boundary, write on the owner.** `turnPending atomic.Bool` is
  the only added shared state; `Store`/`Swap` need no lock. All four `f` writers
  (`openSession`, `emitStructuredJSONLIfTriggered`, `appendTurnGrowth`, the rotation
  re-open) stay on the main goroutine — **no mutex on `f`**. This is the general
  shape for any future cross-goroutine fakeclaude trigger.
- **`Swap(false)` per poll cycle** collapses chunked stdin into one append per
  ~50 ms cycle (far inside the 10 s confirm timeout, ahead of the 150 ms confirm
  poll) and re-arms for a later turn. The grow always targets the **current** `f`
  (post-rotate, if a rotation fired earlier in the same iteration).
- **The inert `{}\n` is invisible to every assertion except "the file grew".** It is
  the exact line `openSession` writes; the turnbridge mapper maps an empty/typeless
  line to `(nil, false)` (`mapper.go:72-73`), so the v2/structured producer tails it
  and emits no event. Growth-confirm checks **size only** — no glyph, no new
  substrate, **no allowlist change** (the seal is untouched).
- **Best-effort write.** A failed `Write`/`Sync` is silenced (mirrors
  `emitStructuredJSONLIfTriggered`); the e2e asserts the ack downstream. A
  persistently-failing write surfaces as the daemon's loud `ErrTurnNotCommitted`,
  never a false ack.
- **Race-free vs the baseline.** The supervisor captures the baseline *after*
  `WaitReady` and *before* `deliver`; stdin bytes (hence any grow) arrive only
  *after* `deliver`, so a grow always lands strictly past the baseline.

**Blast radius is exactly the TUI tests.** The stdin reader runs only when
`logPath != "" || tui` (`main.go:692`), so the grow fires only there — the other e2e
callers (`StartRotation`, the fakeclaude primitive, attach-stdio) set neither and are
unperturbed.

**Sessions-dir alignment is still required** for a test to observe the growth. The
daemon's resolver has no env override — it always scans `<HOME>/.claude/projects/
encode(workdir)` — so a test that drives a turn must point `sessionsDir` at that
**computed** dir and pre-create `<initialUUID>.jsonl` before startup (the same
alignment the JSONL-trigger mode needs, above). A `t.TempDir()` subdir is a dir the
daemon never scans; the misalignment is *silent* on the resolver side (an
exist-but-empty computed dir resolves to `("", 0, nil)`, **no WARN**) and surfaces
only as the 10 s `ErrTurnNotCommitted`. See [codebase/673.md](../codebase/673.md) for
the five tests aligned by #673.
