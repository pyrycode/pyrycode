# Build Helper

`ensurePyryBuilt(t)` builds pyry once per test process via `sync.Once` into a
persistent `os.MkdirTemp` (intentionally not cleaned — `go test`'s own cleanup
takes /tmp eventually).
`PYRY_E2E_BIN` short-circuits to a known-good binary on disk for CI.

The recorded pool and MCP daemon caches also use process-lifetime
`os.MkdirTemp` directories; `TestMain` removes those two directories after
`m.Run`. A process-global `sync.Once` path cannot live in one caller's
`t.TempDir`: parent cleanup deletes the executable before a later top-level
consumer uses the cached path. Run preceding `TestRecordedClaude` scenarios and
`TestRecordedMCPSendFileWaitsForStartup` together in one uncached process to
exercise that lifetime; an isolated startup pass misses it.

**An `-overlay` mutant does not reach a spawned daemon by default.** `ensurePyryBuilt`
shells a plain `go build ./cmd/pyry` with none of the parent `go test` invocation's
flags, so mutating `cmd/pyry` source and re-running the suite under `-overlay=<path>`
only mutates the *test binary's own compilation unit* — the child `pyry` these specs
spawn is unaffected and the mutant silently fails to redden anything. To exercise a
mutant against the spawned daemon, build it separately
(`go build -overlay=<path> -o <bin> ./cmd/pyry`) and inject it via `PYRY_E2E_BIN`
(#1512). A mutation run that skips this reads as "the assertion under test is
vacuous" when the real cause is "the mutant never shipped".

**A green run under `PYRY_E2E_BIN` still needs a control (#1845).** A mutant
built and injected this way can be green for two indistinguishable reasons:
the assertion is genuinely dead weight, or the injection silently didn't take
(stale cached binary, a build that failed in a way that still left a binary
on disk). Pair every `PYRY_E2E_BIN` mutant run with a control run of the
*clean* binary through the identical `PYRY_E2E_BIN` invocation — only a
clean-PASS / mutant-FAIL pair proves the mutant actually reached the spawned
daemon.

**`ensureFakeClaudeBuilt` has the identical shape, and the identical fix
(#1918).** It shells its own plain `go build ./internal/e2e/internal/fakeclaude`,
so `go test -overlay=<path>` mutating fakeclaude's `main.go` mutates only the
test binary's compilation unit — the spawned fake runs unmutated, and a spec
that dials it (rather than reading its source) reads green for the same
"vacuous assertion" reason as the pyry case above. `PYRY_E2E_FAKE_CLAUDE_BIN`
is the injection point, same two-command route: build the mutant to its own
`-o` path, then run the suite with the env var set to it and a clean-binary
control run for comparison. An e2e that spawns *both* binaries needs mutants
targeted at whichever one the claim under test actually lives in — `PYRY_E2E_BIN`
does not reach fakeclaude and `PYRY_E2E_FAKE_CLAUDE_BIN` does not reach pyry.

**A fixture whose two channel-halves are written from the same constant
cannot fail (#2118).** `TestRelayV2_StreamSessionSettingsReportsTheObservedWindow`
plants a transcript naming a model and a fake-claude rider reporting that
model's context window, and passed full-stack while
[`contextwindow.Read`](contextwindow-package.md#context-window-size--a-believed-default-not-an-asserted-fact)'s
join between those two channels was broken — because the rider's model id and
the planted transcript's model id were the same string literal, so the one
divergence the test existed to catch could never appear in its own fixture.
Any spec that joins two independently-sourced channels (a spawned fake's
output against a planted input file, in general) only exercises the join if
the two sides are seeded from two independently-chosen values, not two copies
of one constant — pin the values the real channels are observed to disagree on,
the way #2118's rider key and transcript id now do.

**A silent fake control request is a timeout, not a no-op (#2517).** When the
production session-settings path began sending `get_settings`, fakeclaude's
unknown-control posture left the connection worker waiting until its 30-second
deadline and three existing stream e2es timed out. A control request that becomes
ordinary production traffic therefore needs an unconditional default fake answer,
unless the test deliberately exercises refusal. The full-stack spec must also
assert the value returned through that control round trip: unrelated saved-settings
or context-window assertions can stay green when the provider is accidentally
unwired and the optional field is merely omitted.

**Checking that a small advertised subset belongs to a larger capture does not
prove the capture traversed the system (#2281).** The first model-rejection e2e
loaded #2279's six-row capture only as a comparison set while fake claude still
published its default two-row fixture; it stayed green although four source rows
never crossed initialize retention or client publication. When a capture is the
claimed provenance, feed an exact projection of every complete row into the
producer and compare the full ordered output before testing downstream behavior.

**A fake that normalises an argv real claude refuses hides a whole defect class
indefinitely, not just one test (#2446).** `fakeclaude.argvIDFlag` keeps the
LAST id flag on the argv it's handed, so `--session-id X … --resume X` — which
real claude refuses outright, exit 1, one stderr line — read here as an ordinary
resume, and the fake tier stayed green through a production crash-loop
(`Pool.UpdateSettings` installing a recomposed argv that still carried the baked
session id; see [`Pool.UpdateSettings`](sessions-package-key-types-pool-updatesettings.md)).
The fix is a second, unconditional guard at the top of `fakeclaude`'s `main`,
above every mode branch: it refuses the same `--session-id` + `--resume`/
`--continue` (without `--fork-session`) pair claude refuses, with claude's own
stderr line. Ungated by env, for `argvIDFlag`'s stem guard's reason — it fires
only on an argv the daemon should never compose, so there is nothing for a test
to opt out of. Reproducing the defect in the fake tier also needed a seeded
`<id>.jsonl`: stream-mode fakeclaude establishes no transcript on its own, so
without one every spawn takes the create form and the composed pair is the
harmless `--session-id` twice — the arm has to plant the fixture that makes the
per-spawn probe pick the resume form before the guard has anything to catch.

**Editing the source in place still needs `-count=1` against a daemon-spawning
suite (#2099).** `-overlay` isn't the only way a mutant fails to reach the
spawned binary: even with `cmd/pyry`'s source edited directly on disk, a
plain re-run of `go test -tags=e2e ./internal/e2e/...` can serve a **cached**
pass, because nothing the Go test cache tracks — the test package's own
sources and build inputs — changed. The daemon runs as a subprocess spawned by
`ensurePyryBuilt`'s `go build`, which is invisible to that cache key. Verifying
a fix this way (rather than via `PYRY_E2E_BIN`, above) needs the in-place edit
**and** `-count=1` together; either alone can read as a mutant that never
landed when it actually never ran.

**A test-local `t.Setenv("HOME", …)` must come after `ensurePyryBuilt`/
`ensureFakeClaudeBuilt`, not before (#1631).** Both are `sync.Once`-guarded, so
whichever caller runs first performs the actual `go build`; every later call
(including `spawnWith`'s own) is a no-op that just returns the cached path.
Overriding `HOME` in the test process *before* either call sends that `go build`
subprocess an empty module cache under the temp home, which tries to re-download
the whole module graph and fails on a private dependency. This only bites a test
that needs `HOME` overridden in the test process itself (e.g. to re-derive a
daemon-side value that reads `$HOME`, as #1631's own AC 2 test does) — the
ordinary `Harness.Run`/`StartIn` path never hits it, since it redirects `HOME` for
the **spawned daemon's** environment via `childEnv`, not the test process's own.

**A production change to what a fresh host contains at startup reaches every
e2e test, not just the ones about that feature (#2569).** When `cmd/pyry`
started seeding a promoted `General` channel and its bound session on a
registry that is both empty and unmarked, six unrelated tests that counted
sessions or conversations on a fresh `$HOME` broke, because every fake-daemon
test starts from exactly that state. The fix lives in the harness, not in the
individual tests: `spawnWith` calls `premarkWorkspaceSeeded`, which writes
`{"conversations":[],"seeded":true}` for the `test` instance unless a
`conversations.json` already exists, so a daemon spawned through the harness
opts out of the seed by default. A test that specifically wants to observe the
seed (`TestWorkspaceSeed_E2E_FreshHostGetsGeneral`) writes a zero-byte
`conversations.json` first — the harness leaves an already-present file alone,
and the daemon loads zero bytes as empty and unseeded. Any future startup
behavior gated on "does a fresh registry/session file exist" needs the same
treatment: either give the harness a matching pre-mark default, or accept that
every existing count-based assertion across the suite is now testing the new
behavior too, whether or not that was the ticket's intent.
