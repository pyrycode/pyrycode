# #1503 — streamsup: drop the Parser's partial-line buffer at child exit

## Files read

- `internal/streamsup/parser.go` → `Parser` (single-writer doc comment, `buf`), `Parser.Write`, `consumeLine` — the unterminated remainder survives across children and a spliced line decodes as `UnrecognizedUndecodable`.
- `internal/streamsup/runner.go` → `Runner.spawnAndWait` (the `cmd.Wait()` → `takeStdin` tail), `retireParserChild`, `retireStdinGeneration`, `New` (`r.parser` inference from `Config.Stdout` / `Config.ControlParser`).
- `internal/streamsup/watchdog.go` → `stallTracker.childExited` doc comment — carries the "sibling question for the #1088 Parser is still open" clause this ticket closes.
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory` sets `scfg.Stdout = parser` directly, so `r.parser` is non-nil on the daemon path and the new call reaches production.
- `internal/streamsup/helper_test.go` → `helperChild` modes (`crash` uses `GO_STREAMSUP_HELPER_ARGV_FILE` as a per-spawn log) — pattern the new mode mirrors.
- `internal/streamsup/parser_test.go` → `TestParser_LineBuffering` "split mid-line across writes" — already asserts AC2.
- Go `os/exec` `Cmd.awaitGoroutines` — even when `WaitDelay` expires it closes the parent pipes and then blocks on `goroutineErr`, so `Wait` never returns while the stdout copy goroutine is still inside `Parser.Write`.

## Change

Add an unexported `(*Parser).dropPartialLine()` that sets `buf` to nil, and call it from `Runner.spawnAndWait` immediately after `cmd.Wait()` returns (beside `takeStdin`), guarded by `r.parser != nil`. Because `Wait` joins the stdout copy goroutine before returning, the call happens-after the dead child's last `Write` and happens-before the next spawn's copy goroutine starts, so the single-writer invariant holds with no lock; the `Parser` doc comment gains a sentence saying so. It is deliberately NOT put in `retireParserChild`: that also runs from `retireStdinGeneration`, which fires on a caller's context while the child may still be writing, and there it would race `Write`. The spawn-failure returns before `Start` need no call — no child ran, so nothing was written. `stallTracker.childExited` keeps its buffer (a splice there is activity-only); only its "still open" clause is updated to say the Parser drops its remainder at child exit (#1503).

## Testing strategy

- New `helperChild` mode `partial_then_line`: logs its spawn to `GO_STREAMSUP_HELPER_ARGV_FILE`; on the first spawn writes `{"type":"res` with no newline and exits 1; on later spawns writes `{"type":"result"}\n` and blocks until SIGTERM.
- New runner test in `runner_test.go`: `Stdout` is a `Parser` whose sink records events under a mutex; wait for the second spawn and an event, cancel, join, then assert the events are exactly one `TurnEnd{EndTurn}` and no `Unrecognized`. On `main` the splice yields `Unrecognized` and no `TurnEnd`, so the test fails there — this proves the call site, not just the method.
- AC2 (split within one child's life still joins) is covered by the existing `TestParser_LineBuffering` "split mid-line across writes" subtest; unchanged.

## Documentation handoff

None required by the ticket. Pending for the documentation stage only if it chooses to note in the streamsup package overview that the Parser's partial-line remainder is per-child.
