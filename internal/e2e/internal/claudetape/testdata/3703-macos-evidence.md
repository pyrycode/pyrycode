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
