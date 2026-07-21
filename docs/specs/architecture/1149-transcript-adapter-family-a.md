# Spec #1149 — Migrate Family A (delivery-confirm resolver) onto `internal/transcript`

**Ticket:** [#1149](https://github.com/pyrycode/pyrycode/issues/1149) · split from #973 · size **S** · `security-sensitive`

Pure adapter swap: the delivery-confirm resolver family in `internal/sessions/reconcile.go` becomes a thin adapter over the `internal/transcript` leaf (created by #1148, merged in PR #1155). No public signature changes, no consumer cascade — this is a net **deletion** of duplicated logic. The load-bearing `("", 0, nil)` no-baseline convention (#838) is preserved and re-pinned by test.

## Files to read first

- `internal/sessions/reconcile.go:112-285` — the two resolvers to rewrite (`newTranscriptResolver:120`, `newProbePreferredTranscriptResolver:224`), plus `probeUsable:151` / `availabilityReporter:144` (**kept** — see § "What stays local") and `jsonlExt:14` / `uuidStemPattern:18` / `mostRecentJSONL:78` (**deleted**). This is the only production file with logic changes.
- `internal/transcript/transcript.go:36-225` — the primitives to adopt: `Ext:38`, `ValidStem:48`, `Result:77`+`Found:83`, `CanonicalDir:89`, `GuardProbedPath:111`, `StatByID:133`, `Newest:151`, `Probed:204`. **Critical:** `Probed` surfaces the probe-call error (`transcript.go:208-211`) — every *other* no-result collapses to `(Result{}, nil)`. The adapter **swallows** that one surfaced error (§ "The convention inversion").
- `internal/transcript/transcript_test.go:289-366` — `TestNewest` covers newest-by-mtime, tie-break, non-uuid/wrong-ext/subdir skip, empty-dir, missing-dir. It is a **strict superset** of the six `TestMostRecentJSONL_*` cases being retired — confirm this before deleting them.
- `internal/sessions/reconcile_test.go:172-476` — the resolver tests to keep (`TestNewTranscriptResolver_*`, `TestProbePreferredResolver_*`), `TestMostRecentJSONL_*:54-170` to delete, and `TestProbePreferredResolver_NoBaseline:327-384` to strengthen for AC3. `touchJSONL:43` / `resolvedTempDir:277` / `stubProbe:253` / `constPID:272` helpers stay.
- `internal/sessions/pool.go:495-517` — the wiring site. **Unchanged** — `newProbePreferredTranscriptResolver(...)` keeps its 4-arg signature. Only the stale comment at `pool.go:578-583` (names `mostRecentJSONL`) needs a one-word trim.
- `docs/specs/architecture/838-probe-prefer-transcript-resolver.md` § "Error handling — the load-bearing no-baseline convention" — the `("", 0, nil)` contract this ticket must preserve verbatim.

## Context

`internal/transcript` (#1148) now owns the resolver core: dir canonicalisation, the confidentiality guard, by-id / probe / newest-by-mtime selection, and the canonical UUID-stem regexp. #1148 created the leaf **without migrating any consumer** (its code-review pinned "0 consumer imports + 3 regexps intact" as the scope gate). This ticket is the Family A migration: rewrite `reconcile.go`'s delivery-confirm resolvers as thin adapters over that leaf so the resolution logic and the UUID-stem constant have exactly one home.

Family A is the **inbound delivery-confirm growth baseline**: `confirmViaTranscriptGrowth` (#668, `internal/supervisor/supervisor.go`) resolves `supervisor.Config.ResolveTranscript` before delivering a turn and again while polling, confirming the turn committed by watching the daemon's own child's transcript grow. Its convention is inverted from Family B (cmd/pyry, the outbound turn-stream tail): **not-found is a nil error, never an mtime fallback**. That inversion is load-bearing and is the whole reason this is `security-sensitive` — a non-nil baseline error diverts the caller to the stochastic Committed-chip fallback (the #668 heuristic the growth-confirm exists to replace), silently reintroducing false-ack risk.

The blocking ticket (#1148) is merged. `reconcileBootstrapOnNew` was deleted in #839 (confirmed absent from the main tree — bootstrap is now deterministic via `seedBootstrapRegistry` + `--session-id`), so the newest-by-mtime path has **no** separate bootstrap consumer; it survives only as the no-lsof AC5 fallback.

## Design

### Migration map — `internal/sessions/reconcile.go`

**Delete** (logic now lives in `internal/transcript`, satisfying AC1's "no probe-guard, dir-canonicalisation, selection, or UUID-stem logic remains duplicated"):

| Deleted symbol | Replaced by |
|---|---|
| `const jsonlExt` | `transcript.Ext` |
| `var uuidStemPattern` | `transcript.ValidStem(stem)` |
| `func mostRecentJSONL(dir) (SessionID, error)` | `transcript.Newest(dir) (transcript.Result, error)` — called directly from `newTranscriptResolver`; the `SessionID`-returning intermediate is gone |
| inline dir-canonicalise + confidentiality guard + probe dispatch in `newProbePreferredTranscriptResolver`'s closure | `transcript.CanonicalDir(dir)` (once) + `transcript.Probed(dir, canonicalDir, probe, pid)` |
| inline pinned-id `os.Stat` | `transcript.StatByID(dir, id)` |

**Import delta:** add `github.com/pyrycode/pyrycode/internal/transcript`; **remove `strings`** (its only uses — `HasSuffix` in `mostRecentJSONL` and the inline guard — are both deleted; `staticcheck` will flag it if left). Keep `os`, `path/filepath`, `regexp` (all three still used by `DefaultClaudeSessionsDir` / `workdirNonAlnum` / `encodeWorkdir`, which are **untouched**), `context`, and the `rotation` import (still used by `probeUsable`'s `rotation.Probe` parameter).

### What stays local (adapter concerns, NOT transcript core)

Do **not** move these into `internal/transcript`, and do not flag them as leftover duplication:

- **`availabilityReporter` interface + `probeUsable`** — these detect the no-lsof `noopProbe` via `rotation.Probe`'s optional `Available()` method. `transcript.Probe` is deliberately single-method (`OpenJSONL` only); the availability concept and the no-lsof→mtime *delegation* are Family A's specific behaviour (AC4/AC5), not shared core. #1148 kept `transcript.Probe` minimal on purpose. `probeUsable` is none of the four things AC1 forbids duplicating (guard / canonicalisation / selection / UUID-stem) — it is probe-capability detection. It stays.
- **`encodeWorkdir` / `workdirNonAlnum` / `DefaultClaudeSessionsDir`** — the projects-dir encoder, a separate concern from transcript resolution. Untouched.

### Rewritten contracts (developer writes the bodies; invariants pinned by § Testing)

**`newTranscriptResolver(dir string) func(ctx context.Context) (string, int64, error)`** — signature unchanged. Behaviour: call `transcript.Newest(dir)`; on error return `("", 0, err)`; otherwise return `(res.Path, res.Size, nil)` — the zero `Result` (no match) naturally yields `("", 0, nil)`. This preserves all three observable behaviours the AC5 byte-identity depends on: newest-by-mtime pick, empty-dir `("", 0, nil)`, missing-dir error propagation (the `os.ReadDir` error, which `errors.Is(err, fs.ErrNotExist)`).

> **One intentional micro-behaviour delta, in the safe direction.** The old body stat'd twice — once inside `mostRecentJSONL`, once again for size — so a file vanishing *between* the scan and the re-stat surfaced as a non-nil error. `transcript.Newest` stats once and returns Path+Size together, so that vanish-in-the-gap race now returns the already-captured size instead of an error. No test exercised the old double-stat error, the caller tolerates both (it treats the old error as "no growth"), and one fewer stat + one fewer race window is strictly better. Note it so review doesn't read it as an accident. AC4's "byte-identical newest-by-mtime behaviour" refers to the *selection* semantics (which file, which size), which are preserved exactly.

**`newProbePreferredTranscriptResolver(dir string, probe rotation.Probe, pidFn func() int, pinnedID func() string) func(ctx context.Context) (string, int64, error)`** — signature unchanged. Structure:

1. `if !probeUsable(probe) { return newTranscriptResolver(dir) }` — the no-lsof AC5 delegation, **unchanged**. This remains the only returned closure that may emit a non-nil error, and only because it *is* today's newest-by-mtime behaviour.
2. Precompute `canonicalDir := transcript.CanonicalDir(dir)` once (replaces the inline `EvalSymlinks(dir)` / `Clean` fallback).
3. Return a stateless closure that, per resolve:
   - **Pinned-id path.** `if pinnedID != nil { if id := pinnedID(); id != "" && transcript.ValidStem(id) { ... } }`. Inside: `res, err := transcript.StatByID(dir, id)`; `err != nil` → `("", 0, nil)` (file not created yet / raced away — the no-baseline sentinel, and it does **not** fall through to the probe); success → `(res.Path, res.Size, nil)`.
   - **Probe path** (reached when `pinnedID` is nil, returns `""`, or returns a non-UUID stem): `res, _ := transcript.Probed(dir, canonicalDir, probe, pidFn())`; `return res.Path, res.Size, nil`.

Two load-bearing details, both pinned by tests:

- **The `ValidStem` pre-check on the pinned id is not redundant with `StatByID`'s internal check** and cannot be dropped. It is the branch selector: a **valid** pinned id → use `StatByID`, and a miss is `("", 0, nil)` with **no probe** (`TestProbePreferredResolver_PinnedIDAbsentIsNoBaseline` asserts the probe is never consulted). An **empty or invalid** pinned id → fall through to the probe path. `StatByID` alone can't distinguish "invalid stem" from "valid-but-missing" for that routing decision (both return an error).

- **The convention inversion: swallow `Probed`'s error.** `transcript.Probed` returns `(Result{}, err)` for exactly one condition — the `probe.OpenJSONL` call itself failing — and `(Result{}, nil)` for every other no-result (pid ≤ 0 without calling the probe, empty open path, guard reject, vanished-before-stat). Family A discards that surfaced error (`res, _ :=`) so a probe failure becomes `("", 0, nil)`, never a non-nil error and never an mtime fallback. This is the inversion vs Family B (which will *wrap* the same error for its retry-on-error subscriber). The neutral core surfaces; each adapter maps to its own convention — exactly the #1148 "conventions stay in the adapters" design.

### Wiring — `internal/sessions/pool.go`

Unchanged in behaviour. The `newProbePreferredTranscriptResolver(cfg.ClaudeSessionsDir, probe, pidFn, supCfg.ResolveSessionID)` call at `pool.go:511`, the late-bound `bootstrapSup Runner` holder, `newProbe`, and the `ResolveSessionID` pinned-id source all stay. Only the comment at `pool.go:578-583` names `mostRecentJSONL` ("*mostRecentJSONL and the transcript resolvers stay*"); after deletion, trim it to reference the transcript resolvers only. Comment-only edit — no code change in this file.

### Data flow (unchanged from #838; internals relocated)

```
WriteUserTurn                                    (supervisor, per turn)
  └─ confirmViaTranscriptGrowth
       ├─ resolve(ctx)  ── baseline ──►  newProbePreferredTranscriptResolver closure
       │        pinnedID()  ─► transcript.StatByID(dir, id)      (deterministic by-id)
       │        └ else  pidFn() ─► transcript.Probed(dir, canonicalDir, probe, pid)
       │                              probe.OpenJSONL(pid) ─► own child's <uuid>.jsonl
       │                              transcript.GuardProbedPath: dir==canonicalDir && <uuid>.jsonl
       │                              (path, size)  |  ("",0,nil)  no-baseline (error swallowed)
       ├─ deliver(ctx)                     (PTY write)
       └─ poll resolve(ctx) until grew(base, new)  |  timeout → ErrTurnNotCommitted
```

## Concurrency model

No change. All four adopted `transcript` functions (`CanonicalDir`, `StatByID`, `Newest`, `Probed`) are pure and stateless — no shared state, no locks. The adapter closure holds no mutable state (the #838 design already dropped the sibling's `resolvedOnce`/`sawEmpty` offset state). `canonicalDir` is computed once at construction and read-only thereafter. `pidFn` reads the live child PID via the mutex-guarded `Supervisor.State()`; the late-bound `bootstrapSup` holder in `pool.go` is untouched, so its write-once-before-`New`-returns / read-from-`WriteUserTurn`-goroutine happens-before edge is preserved. `go test -race` continues to validate it.

## Error handling — the load-bearing no-baseline convention (preserved)

The consumer's branches are asymmetric (verbatim from #838 — this ticket must not disturb them):

| Resolve result | Baseline call | Poll call |
|---|---|---|
| `(path, size, nil)` | baseline set → growth path | `grew()` check |
| `("", 0, nil)` | empty baseline → growth path (deliver, then poll for own child's file to appear/grow; else loud `ErrTurnNotCommitted`) | keep polling |
| non-nil error | **diverts to stochastic Committed-chip fallback** — the #668 heuristic this path replaces | "no growth this tick" |

Every no-result condition on the probe path returns `("", 0, nil)`. The two mechanisms that guarantee it post-migration: (1) `transcript.Probed` collapses pid ≤ 0 / empty-open / guard-reject / vanished-before-stat to `(Result{}, nil)`; (2) the adapter swallows `Probed`'s one surfaced probe-call error. mtime is reachable **only** via the `!probeUsable` AC5 branch (which *is* today's behaviour) — never on the probe path.

## Testing strategy

All in `internal/sessions/reconcile_test.go`, same idiom (`t.TempDir()`, `touchJSONL`, direct constructor call, injected `stubProbe` / `constPID`, `t.Parallel()`).

- **Delete the six `TestMostRecentJSONL_*` tests** (`:54-170`). `mostRecentJSONL` no longer exists; its behaviour is covered by `transcript_test.go`'s `TestNewest` (verified superset — newest-by-mtime, tie-break, non-uuid/wrong-ext/subdir skip, empty-dir, missing-dir). No test needs adding to `transcript_test.go`. Keep `touchJSONL` (surviving tests use it).
- **Keep, expect green unchanged:** `TestNewTranscriptResolver_PicksNewestWithSize / _EmptyDir / _MissingDir / _IgnoresNonMatching`, `TestProbePreferredResolver_TailsOwnChildNotNewestByMtime` (AC1/AC2 hit-path mtime-not-consulted), `TestProbePreferredResolver_NoLsofMatchesMtimeBaseline` (AC5 byte-identity — still compares the two resolvers against each other), the two `_PinnedID*` tests (#989), and `TestDefaultClaudeSessionsDir_ResolvesSymlinks`. The contract is preserved, so these pass without edits.
- **Strengthen `TestProbePreferredResolver_NoBaseline` for AC3** — "asserts the probe path never falls back to newest-by-mtime." Today its fixture dir is empty, so `("", 0, nil)` doesn't *prove* no-fallback. Change: in each sub-test, seed **one foreign, valid, mtime-winning `<uuid>.jsonl`** into `dir` (a `touchJSONL` with a stem distinct from the `valid` const and a recent mtime) before resolving. The existing assertion (`gotPath=="" && gotSize==0 && err==nil`) then doubles as the no-mtime-fallback pin across every no-baseline branch — the seeded file is exactly what a mtime fallback would return. The `err==nil` half of that assertion is the "no non-nil error" pin (AC3). Add a comment naming AC3. (The "vanished before stat" case already probes for `dir/<valid>.jsonl`, which stays absent; a distinct foreign stem doesn't collide.)

Full suite green: `make check` (`go vet ./...`, `staticcheck ./...`, `go test -race ./...`). No pool/session test drives `ResolveTranscript` through a live pool, so the wired-resolver internals swap is invisible to them; the change is exercised only by the reconcile unit tests and the real growth-confirm path.

## Open questions

1. **Live-binding wiring test** — as in #838, the late-bound-holder wiring is covered structurally by build + the pool suite; an end-to-end assertion needs a fake claude with a known open fd, disproportionate here. Not an AC.

## Not in scope

- **Family B (cmd/pyry outbound turn-stream tail)** — its own migration ticket. It adopts the same `transcript` core but keeps its `(path, offset)` + cold/warm-offset semantics and its *wrap-error-for-retry* convention. Do not touch `cmd/pyry` here.
- **The `internal/sessions/rotation/watcher.go` local `uuidStemPattern`** (`watcher.go:19`) — the rotation watcher is not a resolver family; #1148 left it intact and it is out of scope for this ticket.
- **`docs/knowledge/codebase/1149.md`** — owned by the documentation phase, written after merge. Not a developer deliverable.

## Security review

**Verdict:** PASS

Adversarial self-review per `architect/security-review.md`, gated on the `security-sensitive` label. This ticket introduces **no new trust decision** — it relocates #838's confidentiality guard and no-baseline convention onto the already-security-reviewed `internal/transcript` leaf (#1148, PASS). The audit's job is to confirm the relocation preserves each decision and adds no new surface.

**Findings:**

- **[Trust boundaries]** No MUST FIX. The single untrusted→trusted crossing — the probe-reported path `probe.OpenJSONL(pid)`, which under PID reuse could name any file on disk — moves from inline closure code to `transcript.GuardProbedPath` (invoked inside `transcript.Probed`). Verified byte-identical to the pre-migration inline guard: `EvalSymlinks(probed)` (Clean on error) → reject when `filepath.Dir(resolved) != canonicalDir` → require a `<uuid>.jsonl` base via `sessionFileStem`/`ValidStem`. The adapter passes `canonicalDir = transcript.CanonicalDir(dir)` (= the old `resolvedDir`), the correct-usage path — `GuardProbedPath`'s documented misuse mode (raw un-canonicalised dir) *fails closed* anyway. Boundary is now **more** explicit: a named, separately-tested function rather than inline closure logic. Downstream is unchanged — the adapter returns only `(path, size)`, the supervisor `os.Stat`s for size and never reads bytes, so even a hypothetical guard bypass mis-sizes a baseline (correctness), never discloses another claude's transcript content. `dir` is server-derived (`DefaultClaudeSessionsDir(workdir)`), not caller input; the PID source is server-owned (`bootstrapSup.State().ChildPID`).
- **[File operations]** No MUST FIX. Path traversal stays closed: `candidate := filepath.Join(dir, base)` with `base` through the `ValidStem` gate (36 hex/hyphen chars + fixed `.jsonl`, no `/`, no `..`); `transcript.StatByID` validates `ValidStem(id)` **before** any `filepath.Join`, so an invalid pinned stem returns an error with zero filesystem access. TOCTOU: the guard→`os.Stat` sequence is inherently racy but the sole use is a size read (worst case: a mis-confirmed/mis-missed turn, never a content leak) — and the new single-stat `newTranscriptResolver` (via `transcript.Newest`) *narrows* the pre-migration double-stat window rather than widening it. No files created (read-only resolvers) → no mode / atomic-write surface. Symlinks canonicalised on both sides before the dir compare (in-dir symlink pointing out is rejected).
- **[Tokens/secrets]** No findings — the path generates, stores, and logs no token, key, or credential.
- **[Subprocess]** No findings — no `exec` added. The probe's `lsof` lives in `internal/sessions/rotation` (unchanged); the only value crossing into it is the daemon's own child PID, not caller input.
- **[Cryptographic primitives]** No findings — no crypto, no RNG. `ValidStem` is a regexp shape check, not a secret comparison.
- **[Network & I/O]** No findings — no socket, no unbounded read; `os.Stat` metadata (size) only, plus one `os.ReadDir` of the daemon's own projects folder (bounded, server-owned), no size cap needed.
- **[Error messages, logs, telemetry]** No findings, net-positive: every no-result branch returns `("", 0, nil)` (not a wrapped error), so the resolver emits no per-branch log and no probed path reaches any log or error value. `transcript.Probed`'s surfaced probe error and `transcript.StatByID`'s error are both swallowed inside the adapter — they never propagate out, and the `id` `StatByID` sees is server-minted regardless. The #838 SHOULD-CONSIDER (swallowing probe errors loses "probe persistently failing" observability) is unchanged — the symptom still surfaces loudly as `ErrTurnNotCommitted`; deferred, not gating.
- **[Concurrency]** No MUST FIX — see § Concurrency model. All four adopted `transcript` functions are pure/stateless; the adapter closure holds no mutable state; `canonicalDir` is computed once and read-only; the late-bound `bootstrapSup` holder in `pool.go` is untouched (write-once-before-`New`-returns / read-from-`WriteUserTurn`-goroutine happens-before edge preserved); `sup.State()` is mutex-guarded. No new lock, goroutine, or shared state. `go test -race` validates.
- **[Threat model alignment]** Addresses the target threat unchanged: a second interactive claude in the shared `~/.claude/projects/<encoded-cwd>/` dir cannot redirect the delivery-confirm baseline onto its own newer transcript (false-ack / mis-confirm), because the same guard + the same `("", 0, nil)` no-baseline convention now source from the reviewed leaf. Moving the guard to `internal/transcript` adds no attack surface — the leaf imports stdlib only and `GuardProbedPath` is a pure function of two server-derived strings. The reconcile-time variant (`ChildPID == 0`) remains out of scope (owned by #839's deterministic `--session-id`, merged). The `security-sensitive` label is correct and retained.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-21
