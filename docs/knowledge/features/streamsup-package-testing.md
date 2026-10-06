# Testing

Table-driven stdlib `testing`, `go test -race`. Fake-child harness dispatches from `TestMain` on `GO_STREAMSUP_HELPER=1` **before `flag.Parse`** — not streamrunner's `os.Args[0]` + `-test.run` re-exec trick, because `buildArgs` prepends the fixed stream-json flags *ahead of* the caller's args, so a `-test.run` flag can never be made to sort first; `go test` would exit 2 on the unknown leading flag before the helper ever ran. Dispatching from `TestMain` on an env var sidesteps flag parsing entirely. Modes keyed by `GO_STREAMSUP_HELPER_MODE`: `echo_lines` (proves stdin stays open — echoes each line, only emits `GOT_EOF` if EOF is actually reached), `block_sigterm` (teardown grace test), `crash` (forces respawns; optionally records its own argv to `GO_STREAMSUP_HELPER_ARGV_FILE` for the resume-id-stability assertion).

**A package's test binary can be loud and green at the same time (#2470).** After the `GO_STREAMSUP_HELPER` dispatch above, `TestMain` also installs a discarding default logger — `slog.SetDefault(slog.New(slog.DiscardHandler))` — before `m.Run()`. Every constructor in this package (`New`, `NewWatchdog`, `newStallTracker`) falls back to `slog.Default()` when `Config.Logger` is nil, and the stdlib default writes to `os.Stderr` at Info and above. `go test` discards a *passing* package's output but dumps the whole test-binary output when the package fails, so those records are invisible until a sibling test fails and something needs its stderr — at `dbf97327` a passing run of this binary still wrote 25,671 bytes over 101 lines, all through that fallback, enough to crowd a failing sibling's `--- FAIL:` line out of a downstream 4000-character log excerpt. `TestDefaultLogger_DiscardsEveryRecord` guards both halves of the fix, not just the level check: the installed default must refuse every level, *and* a `Config` that omits `Logger` (as `helperRunCfg` does) must actually resolve to that default — pinning only the first half stays green even if `New`'s fallback were re-pointed at a fresh stderr handler. Tests that pass their own `Config.Logger` are unaffected, and the install is process-local to this test binary, so it has no bearing on `internal/e2e`'s assertion against the daemon's own stderr.

Scenarios: `buildArgs` shape (pure, table — fixed prefix present, `-p` absent, `--session-id` vs `--resume`, id byte-identical across first-spawn/respawn, `base` order preserved and not mutated); held-open stdin (echo round-trip + `GOT_EOF` absent while alive); backoff ladder (lifted `supervisor.backoff_test.go` verbatim against the copied `backoffTimer`); restart-on-crash (≥2 spawns observed via `onSpawn`); resume-id-stable-across-restart (captured argv: spawn 1 has `--session-id <id>`, spawn 2 has `--resume <id>`, same id); teardown SIGTERM+grace (`Run` returns within `< killGrace`, "got SIGTERM" on stderr); teardown reaps descendant groups (`reapDescendantGroupsFn` swap, non-parallel); the `firstRun`-gate regression test (non-existent binary, every retry keeps `--session-id`).

An actual child launch witness and the parent's stdin publication are separate
observations: the child can write its environment/argv record before the parent
has published its handle. Before commanding a child to crash, wait for both the
complete child-written record (`awaitTokenWitness`) and non-nil `Runner.Stdin()`
(`writeTokenChild`). `onSpawn` alone does not establish that the child ran its own
code. Account admission tests also need a rejection after an admitted child,
followed by recovery with a different token and the expected resume form; an
initial-rejection-only test cannot catch reuse of a prior successful token.
See [account token admission and the `firstRun` gate](streamsup-package-supervise-loop-run.md#optional-account-token-admission).

Executable-selection coverage must observe the path the child actually launched
under, including while atomic file replacement races spawns; inspecting the
selection file or `onSpawn` cannot prove that path reached `exec`.
`TestSpawnBinarySelection` uses child-written argv/environment/cwd witnesses and
a stdin liveness response to distinguish a future-spawn update from killing the
current child. Full-environment witnesses can contain inherited credentials:
compare them in memory, and report only fixed text or witness counts on assertion
and timeout paths. An assertion or timeout dump can disclose credentials even
when successful runs look safe. Exercise those diagnostics with a synthetic
inherited sentinel and a minimal subprocess environment. Helpers used exclusively
by tagged tests must share their build tag; an ordinary staticcheck run otherwise rejects
them as unused. See [the isolated live failure/release contract](e2e-realclaude.md#test-infrastructure).

For request/reply correlation that spans an `io.Writer` call, proving
"registered before write" and "a failed write cannot emit" in separate tests
does not prove their ordering under concurrency. A matching response can arrive
after the operating-system write but before the writer returns its error. The
discriminating test needs a writer that exposes that response while its return
is still blocked, then returns the error and proves the waiting parser emits
nothing. Otherwise a regression that reads the response before the write result
is published can leave both simpler tests green.

Writer entry is also insufficient to prove that a request is waiting for its
response. In `StopTask` tests, cancellation while writing may retire the
captured child, whereas cancellation after the write preserves it. Wait for the
pending actuation's `writeDone` gate before cancelling to prove response-wait
preservation; `awaitStopWrite` establishes that boundary. A signal from inside
`Write` can arrive before the method returns and leave the test exercising the
retirement exception instead. See [control-response correlation](streamsup-package-turn-io-envelope-write-stdout-parser.md)
for the write gate and final child-generation check.

Seeding a factory runner through a retention hold also forwards those events
into the downstream fan-in. Waiting for any queued event can therefore falsely
signal child readiness before the child has produced output. Drain seeded events
and wait for the child's own stdout before releasing it to exit.
`TestBackgroundTaskJoin_FactoryChildExit` uses this ordering to prove join reset
and preservation of the existing exit callbacks in both permission modes, then
checks task-id reuse in the replacement child (#2753). A hold-only reset test
cannot detect missing factory callback wiring.

**#1630 added three tests, all pure insertions — the four argv-through-a-real-spawn pins and the three `buildArgs`-shape tests above stay byte-unmodified.** `TestUseCreateForm_ProbeDecidesIDFlag` (pure, table, composes `useCreateForm`+`buildArgs` over `t.TempDir()` fixtures with hand-written `<uuid>.jsonl` files, mtime-differentiated via `os.Chtimes` so a "newer unrelated transcript" row is deterministic rather than write-order-dependent); `TestRunner_BeginSpawn_FirstSpawnResumesExistingTranscript` (the wiring pin — proves `beginSpawn` actually feeds the probe's answer to `buildArgs` rather than passing `firstRun` straight through); `TestRunner_RestartFresh_ProbeDecidesPerSpawn` (the per-spawn pin — an *asymmetric* fixture, transcript present only for the pre-rotation id, is the one arrangement that discriminates a per-spawn decision from one memoised at construction; a fixture with both ids absent would pass either way). One general lesson from building the table: a row composing two functions (`useCreateForm` then `buildArgs`) only proves the override if its `latchCreate` column is set *against* the expected flag — a row where the latch already agrees with the probe's answer stays green under a mutant that deletes the probe entirely, so it reads as coverage while proving nothing about the override.

A content-free-logging guard must scan what was actually logged, not a rendered
handler dump: `TestParser_TaskProgressDropIsLoggedContentFree` substring-scanned
a `slog.TextHandler` render, whose own leading `time=` attribute renders two
consecutive nines often enough (`.991`, `.399`, `.099`) to trip a forbidden bare
`"99"` that `emitBackgroundTaskProgress` never actually logged — a 3.5% flake
(#2472) on the handler's framing, not the parser. The fix swept the file's own
`logRecorder` capture (message plus attr pairs, no timestamp) instead, the shape
sibling `TestParser_HarnessNudgeDropIsLoggedContentFree` already used, and
derived the numeric forbidden token from the fixture constant with
`strconv.Itoa` rather than hand-typing it beside the fixture, so the guard can't
drift from the value actually sent. Two further lessons from that swap: a
captured-record sweep must cover attr **keys** as well as values, since
`p.log.Debug(msg, tl.TaskID, "x")` compiles and lands claude's task id in a key
that a values-only sweep misses — `TestParser_HarnessNudgeDropIsLoggedContentFree`
still only sweeps values and carries the same latent gap unfixed; and a sweep
over rows that include silent branches (two of this test's three do) needs a
positive control — `wantDrops`, checked via `logRecorder.withMessage` — or a
recorder that was never wired to the parser reads as a pass on every row.
