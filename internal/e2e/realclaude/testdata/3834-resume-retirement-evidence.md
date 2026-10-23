# Resume predecessor retirement proof — Linux, 2026-10-22

This is focused offline replay/control evidence, not a new Claude capture or a complete gate. macOS is outside this Linux ticket's verification scope. All commands used managed test admission and fresh test-owned temporary state. Full JSON and broker artifacts remain under `/tmp/builder-3834/` on the builder host; the hashes below identify those retained files.

## Revisions and selections

- Plan: `16ab95a0` precedes all implementation and regression code.
- Initial fixture revision: `522dbaa28da7f412d147f1bfc68baf3cbf95ebd5`.
- Corrected fixture with old immediate-check semantics: `d93e37721806e046e988b271406e07fe6b2a9edd`.
- First repair: `e7e4bb16a1209f967ca24c7d4de966d04f0aface`.
- Final tested code: `eec62e11ce28e2103e0e2e08131ac9a3e2ba03cf`. The following evidence-only commit changes no Go source.

Controls used `python3 scripts/test-admission.py -- go test -timeout=5m -tags=e2e_realclaude -race -count=1 -json -run SELECTION ./internal/e2e/realclaude`.

- Initial, old and first-repair selection: `^TestResumePredecessor` (includes the helper's no-op parent invocation).
- Final selection: `^(TestResumePredecessorWaitsForRetirement|TestResumePredecessorRejectsBoundary|TestResumePredecessorDeadline)$` (excludes that helper invocation).

Replay used `PYRY_CLAUDE_TAPE_MODE=replay PYRY_CLAUDE_TAPE_SCENARIOS='["session-resume"]' PYRY_CLAUDE_TAPE_EVIDENCE=EXTERNAL_DIRECTORY python3 scripts/test-admission.py -- go test -timeout=10m -tags=e2e -race -count=1 -json -run SELECTION ./internal/e2e`.

- The ticket's `^TestRecordedClaude$/^session-resume$` selection was refused before admission, with zero tests executed (`before/replay.stderr`). The launcher accepts explicit names but rejects internal regex anchors.
- Baseline accepted selection: `^TestRecordedClaude/session-resume$`. Go also selected `TestRecordedClaudeFirstTurnWaitsForStartup`; its actual result is retained, not folded into the session-resume count.
- First-repair and final selection: `^TestRecordedClaude$`, with the sole scenario selected by the environment above. This executes only the intended root and scenario.

## Counted results

Counts are executed/passed/failed/skipped. Named-event counts include parent tests; leaf counts exclude parents. All skips are zero.

| Run (relative to retained root) | Revision | Named events | Leaves |
| --- | --- | --- | --- |
| `before/controls.json` | `522dbaa2` | 11/2/9/0 | 9/2/7/0 |
| `before/old-controls.json` | `d93e3772` | 11/8/3/0 | 9/7/2/0 |
| `before/replay-named.json` | `d93e3772` | 3/3/0/0 | 2/2/0/0 |
| `after/controls.json` | `e7e4bb16` | 12/12/0/0 | 10/10/0/0 |
| `after/replay.json` | `e7e4bb16` | 2/2/0/0 | 1/1/0/0 |
| `final/controls.json` | `eec62e11` | 11/11/0/0 | 9/9/0/0 |
| `final/replay.json` | `eec62e11` | 2/2/0/0 | 1/1/0/0 |

The initial fixture failed because `PublishLifecycleControl` requires an exact private directory mode. `testResumeReceiptPath` now creates that directory explicitly. That initial failed run remains failed and is not the causal proof.

With the corrected fixture, `TestResumePredecessorWaitsForRetirement` failed under old semantics with:

```text
snapshot boundary returned before owned retirement: original eviction did not stop its owned predecessor
--- FAIL: TestResumePredecessorWaitsForRetirement (1.03s)
```

The other failed leaf was `TestResumePredecessorDeadline/predecessor-present`; its parent also failed. Wrong-owner, failed status, error text, empty frames, EPERM and absent-receipt controls passed at that old revision.

At the final candidate:

```text
--- PASS: TestResumePredecessorWaitsForRetirement (1.03s)
--- PASS: TestResumePredecessorRejectsBoundary
--- PASS: TestResumePredecessorDeadline
--- PASS: TestRecordedClaude/session-resume (29.11s)
```

The synchronized control publishes a matching successful receipt while its real child remains alive. It requires another live signal-zero observation before releasing stdin EOF and joining Wait. Only a fresh ESRCH observation can then complete the boundary. Additional controls reject invalid receipts before any process observation, EPERM, missing receipts, a still-live predecessor at expiry, and ESRCH obtained after expiry. No termination signal or scenario retry is used.

## Replay identities and audits

Final replay exercised one original daemon root and two child roles. Its executing helper SHA-256 is `c76cef5a2ab7556c5b6de747b2b28b0392012d12b1b2c586a8ea304ef8fd0f03`; the daemon SHA-256 is `76a6ed8c329363464ff3f8e93038e5927ccd082044a6fe0e1378b48eea1747bc` (identical to baseline). Final prefix and cleanup SHA-256 both equal `fad3ff009592849053614cc7be80da12dc44d89b1be2d7cf36c6ed862930802b`.

External final broker/receipt/observation evidence is `/tmp/builder-3834/final/replay/`. The original five-second idle timer, WARN gate, turn deadlines, strict native resume inputs, EOF/ownership checks and exact final audits ran unchanged.

Final `go vet ./...`, `go vet -tags=e2e_realclaude ./internal/e2e/realclaude` and `go build -o /tmp/builder-3834/final/pyry ./cmd/pyry` all exited zero. Full-module and live gates remain owned by the dispatcher; no live Claude was invoked here.

## Retained JSON SHA-256

| File | SHA-256 |
| --- | --- |
| `before/controls.json` | `891ebc44ddf1038150cee18f5792bf5a69752b95f714c0aebd9d704720a4c447` |
| `before/old-controls.json` | `84ac1446e6ecd5d0c4271860fbd0961e0f784eb93904a47d06dd0a0318d56afb` |
| `before/replay-named.json` | `1136290e0b99b0e31a0da9cfc1b79e1a0f1f8b8823e26a9240312181b1b5333d` |
| `after/controls.json` | `eec5138b1a2da471200f8a345eff694fff60f6613029df188145d817c0cca00b` |
| `after/replay.json` | `7bf021a3bc0e91254e43fb60c13a9639cd36ccc9e4e1068540ea712e49c045d2` |
| `final/controls.json` | `3336db347605db4af0a93af6396bbcb3ef69a500ebd418bd8198531d1ac94373` |
| `final/replay.json` | `a074281c8707689b0ad9bc3a24536bcea4c48e5f616d3b042545bdb4124728e6` |

## Causal conclusion

Receipt publication does not synchronize predecessor reaping: the controlled receipt-before-retirement ordering fails the old immediate check and passes the bounded observation barrier. A fresh-home baseline replay passing does not erase the historical verifier failure at `895f4ff0c9bf84fe5723dacfbedff9f253a9d92d`. This repair addresses that named receipt-to-retirement boundary; it makes no claim about the unknown inner signature of #3715 or the separate diagnostic-file cleanup race owned by #3835. No wider diagnostic was necessary for the controlled proof.
