# Ticket 3703 native macOS evidence

Captured 2026-10-20 on macOS 26.6.2 (25G83), darwin/arm64, Go 1.26.2.
Implementation source: `4c5ccdf6db014aa5b8dfc30d0e5e9964e7b0931d`.
Pre-repair regression source: `6a8c59c11b3581f7557eca3fbd977d4fbf969b67`.
The prospective plan is `7e35ed74`; implementation follows it.

Counts below are executed/passed/failed/skipped; events include parent tests.

| Check | Events | Leaf tests | Result |
| --- | --- | --- | --- |
| Committed regression with Darwin stubs | 3/0/3/0 | 2/0/2/0 | Expected FAIL, both scenarios missing normal observation |
| Final affected package, uncached race | 1012/1012/0/0 | 913/913/0/0 | PASS |
| Final original session-resume replay, uncached race | 2/2/0/0 | 1/1/0/0 | PASS, including final inventory |

Commands run from the product worktree through shared admission:

- Baseline: `python3 scripts/test-admission.py -- go test -timeout=2m -race -count=1 -work -overlay=/tmp/builder-3703/before/overlay.json -json -run '^TestDarwinCancelledLaunch$' ./internal/e2e/internal/claudetape`.
- Package: `python3 scripts/test-admission.py -- go test -timeout=10m -race -count=1 -work -json ./internal/e2e/internal/claudetape/...`.
- Replay: `PYRY_CLAUDE_TAPE_MODE=replay PYRY_CLAUDE_TAPE_SCENARIOS='["session-resume"]' PYRY_CLAUDE_TAPE_EVIDENCE=/tmp/builder-3703/final/replay python3 scripts/test-admission.py -- go test -timeout=10m -tags=e2e -race -count=1 -work -json -run '^TestRecordedClaude$' ./internal/e2e`.
- `go vet ./...` and `go build -o /tmp/builder-3703/final/pyry ./cmd/pyry`: PASS, empty logs at `final/vet.log` and `final/build.log`.

The baseline overlay substitutes only the committed Darwin stub bytes; all other
sources match the regression commit. Stub SHA256: `ff4fe4a405732da0832d24f75e9c47e321e247134039cc6eb77f998ab19d0110`.
The managed selector rejected per-component anchors before execution (zero tests);
the exact root plus one-scenario allowlist executed only the requested replay.

Retained logs are outside disposable homes at `/tmp/builder-3703/`.
SHA256 identities (test binaries are retained by Go's `-work` directories):

| Artifact | SHA256 |
| --- | --- |
| before/pinned.json | `43cf77e6483dde0de36178c3311ef122cf114a24bc66f874648b394c35446afc` |
| before/pinned.json test/helper binary | `7ac359aa06c38f8a1e3683136505235bcd750bbc6c36d87ddddff18b74800375` |
| final/package.json | `382d094f1f9f0aaab1d6c468eb06a543938d96a9db7f76175857f5fa58438198` |
| final/package.json test/helper binary | `90e048f3dec9951f23981b19ce921caa8e98d12c7b8af27c2395632098f6c12a` |
| final/replay/stdout.json | `84dd7d3db546d88740cc6f2c9856dc13ebc999e7bf432f06c9d9785051af6f45` |
| final/replay/stdout.json test/helper binary | `a1732677d81a24bf5adb6d011a0e0f5f07628cf909738c358ff0b285c4400405` |
| Replay helper | `1e93f60ee622b4cc9b9fc767622ecf747658b2c563b22b4a9b99409f493288a4` |
| Replay/product daemon | `4f1347a2d77ba3aa0001237c491cd2317ada14cf20a202cd8648338e6b667717` |
| session-resume fixture | `1a1e6faee135a83b4deba4b2a84630bc846526e69c5db8c793e72b7eb80cb002` |
| final/replay/session-resume-launch-audit.json | `21550798344390be624c7bb1f3a9d1652bce7b4ebdaee2bb3296bacb074d5b25` |

Controlled session-resume proof: worker 48657, daemon 48656, start
1792511989.097385, registration absent; one actual SIGKILL group-cancellation Wait
completed with exit observed. Unaudited inventory rejected total-wait-cardinality;
audited inventory returned zero native exchanges. Both scenarios passed the same
proof. A separately reaped worker with a live owned group member was rejected.
The final original replay observed a complete ownership chain, zero worker launches,
zero Waits and zero registrations; its pass does not attribute earlier failures.

All earlier development failures remain failed in their original logs:

| Log | Events | SHA256 |
| --- | --- | --- |
| before/tests.json | 3/0/3/0 | `907cffa6190186f1489a548fac10dcaaf9a4a3a5fc5d24bc806127dc2a96e36a` |
| after/tests.json | 18/10/8/0 | `15eb0e2bdb97f797b261c04e320810b352c49f0140e4fba8931763ac31e37d89` |
| diagnosis2.json | 3/0/3/0 | `911348253cdd5797c51438c7092a0f9f3b176c984aa80cb1ad3933607d5cdce1` |
| diagnosis3.json | 3/0/3/0 | `eef6cda50dfdb6966f271479d2435975923cc0a66217b5bb8cd7866c3da8729b` |
| after/fixed.json | 18/15/3/0 | `373784e9b3f638571fa0bd31bfbc4f0524142cf64ecba300135502f2c35bc8d7` |

These diagnostic trees were uncommitted iterations: native path return handling,
sampler synchronization and duplicate exclusive test publication were corrected.
Earlier passes remain at `after/boundary.json` (44 events),
`after/package.json` (1010), `after/final-focused.json` (78), and
`replay-native/stdout.json` (2); all zero failures/skips.
Historical candidate failures and unchanged-main FAIL/PASS/PASS remain recorded in
[the original evidence comment](https://github.com/pyrycode/pyrycode/issues/3703#issuecomment-6293418900).
No historical failure is reclassified. Fresh test processes/homes, warm build caches,
no preceding replay scenarios, shared admission; observed load averages ranged from
4.97 to 20.63 (one minute), with contention and exact OS cache state unknown.
No replay timers/assertions changed, no skipped cases or acceptance retries.
Complete suites remain dispatcher-owned; Linux and live-provider proof are outside
this repair's verification scope. Documentation requirements remain pending.

## Verifier rework, 2026-10-20

Prospective repair/security amendment: `38358fdd`. Cleanup regression: `25b44222`.
Native wrappers and owner cleanup: `52645c3f`. Final implementation:
`a17a65b38b7f43f34e8175dc7e6e5308d0b3d622`. Same macOS/toolchain as above,
CGO_ENABLED=1, Command Line Tools MacOSX SDK 26.5, Apple clang 21.0.0
(clang-2100.1.1.101), arm64-apple-darwin25.6.0; no new module dependency.

| Check | Events executed/passed/failed/skipped | Leaves | Result |
| --- | --- | --- | --- |
| Cleanup regression against original owner helper | 3/1/2/0 | 2/1/1/0 | Expected FAIL on control EOF before cancellation |
| First repair package, `52645c3f` | 1018/998/20/0 | 917/901/16/0 | FAIL: child-list count interpreted as bytes |
| Final package, `a17a65b3` | 1042/1042/0/0 | 939/939/0/0 | PASS |
| Final original session-resume replay, `a17a65b3` | 2/2/0/0 | 1/1/0/0 | PASS, including final inventory |

The final package includes 3/3 cleanup, 27/27 cancelled-launch and 19/19 ownership
events (42 leaf controls total), with zero failures/skips. Both decoder-timeout
paths reap the owned tree and surviving group member without disturbing a real
unrelated group. The original replay retained a valid complete ownership chain,
zero launches, zero Waits and zero registrations; it does not attribute historical
failures. No replay assertions, timers, attempt limits or exchange rules changed.

The cleanup baseline used regression bytes committed as `25b44222` and the
unchanged owner helper from `d61351aa`; the native wrapper edits later committed
in `52645c3f` were already present, so it was not a clean pinned tree. That
selection exercises cleanup without invoking the launch sampler. Its temporary
test binary was not retained. All final checks ran on clean `a17a65b3` sources.

Exact commands from the product worktree (logs below `/tmp/builder-3703/rework/`):

- Cleanup baseline: `python3 scripts/test-admission.py -- go test -timeout=2m -race -count=1 -json -run '^TestDarwinLaunchCleanup$' ./internal/e2e/internal/claudetape > /tmp/builder-3703/rework/cleanup-before.json 2> /tmp/builder-3703/rework/cleanup-before.stderr`.
- Both package revisions: `python3 scripts/test-admission.py -- go test -timeout=10m -race -count=1 -work -json ./internal/e2e/internal/claudetape/...`; stdout/stderr are `package-after.json` / `package-after.stderr` and `package-final-a17a65b3.json` / `package-final-a17a65b3.stderr` respectively.
- Replay: `env PYRY_CLAUDE_TAPE_MODE=replay PYRY_CLAUDE_TAPE_SCENARIOS='["session-resume"]' PYRY_CLAUDE_TAPE_EVIDENCE=/tmp/builder-3703/rework/replay-final-52645c3f python3 scripts/test-admission.py -- go test -timeout=10m -tags=e2e -race -count=1 -work -json -run '^TestRecordedClaude$' ./internal/e2e > /tmp/builder-3703/rework/replay-final-52645c3f/stdout.json 2> /tmp/builder-3703/rework/replay-final-52645c3f/stderr.log`. Despite its directory suffix, the retained build stamp identifies `a17a65b3`.
- The same replay with `-run '^TestRecordedClaude$/^session-resume$'` was refused by selection policy before execution (zero tests); its stdout/stderr remain under `replay-52645c3f/`. The admitted exact-root selector plus the one-scenario allowlist preserves the requested scope.
- `make staticcheck`, `go vet ./...`, `go build -o /tmp/builder-3703/rework/pyry-a17a65b3 ./cmd/pyry`, and `CGO_ENABLED=0 go build ./internal/e2e/internal/claudetape`: PASS. Logs: `staticcheck-a17a65b3.log`, `vet-a17a65b3.log`, `nocgo-a17a65b3.log`. Without cgo, native queries remain unknown and cannot authorize cancellations; the compile is not native observation acceptance.

The failed first repair revealed that
[libproc's child-list wrapper](https://github.com/apple-oss-distributions/xnu/blob/main/libsyscall/wrappers/libproc/libproc.c)
takes buffer bytes but returns a PID count. Correcting that boundary, rather than
altering sampling timers, restored the synchronized observation controls.

| Retained artifact | SHA256 |
| --- | --- |
| cleanup-before.json | `06b195b8e0a2eb29c5a59da68ddc1a02b4b565c656c74641817b68c6246af742` |
| package-after.json | `7eb00c8ef9e93fffd8817bf33ee0c7fa119ef044a11cb01144f66d5bb7e502e8` |
| First repair package/helper/daemon test binary | `a6cf0fc7bc9a70814d6ca774d74b5404232003623a048dd6cc71a8ce7912a238` |
| package-final-a17a65b3.json | `b108a3fa7ffe4681ac26ae9571a81dba0326f6a1ea35daccf26c16a7344b4bd2` |
| Final package/helper/daemon test binary | `b7fa84a41fd3f87ff487a8f9a2d21b1f89e2e7322512e59d3ef0a18d6dfd8836` |
| replay-final-52645c3f/stdout.json | `dfe1c3268ea2c97a6cae8bea4f8a229fc351740739035f37e3be29e68fb24962` |
| Replay test binary | `671a8a6f7edf9c4f6e853921a25c194c40d9b42d9691d964bdbc4fe766ad972c` |
| Replay helper | `2c67540e6c963758fdf8b23598382dfd49c30a269013c3d8bdd23607cd6d787d` |
| Replay/product daemon | `4f1347a2d77ba3aa0001237c491cd2317ada14cf20a202cd8648338e6b667717` |
| session-resume fixture | `1a1e6faee135a83b4deba4b2a84630bc846526e69c5db8c793e72b7eb80cb002` |
| Replay launch audit | `0e95fade2af1bec06453045b00e74e431d5dc5d9c5075167d234d7cc52e0f94e` |
| Replay original-cleanup witness | `84a9d881a03fdcdf12e1e57419a632e5d505a08a81d16b464b780380e69461ed` |

Fresh homes/processes, existing warm build cache, no preceding replay scenario
within the replay process. Preceding work included the failed and corrected
affected-package checks. Shared admission waited 108 seconds for the cleanup
baseline and 332 seconds for replay; observed one-minute load averages ranged
4.68–6.55, with other contention and exact filesystem cache state unknown.
All earlier failures above remain failed. Final acceptance used one uncached
package run and one replay on the corrected revision, with no skipped cases or
unchanged-source retries. Complete gates and documentation remain pending for
their dispatcher/verifier and documentation owners.
