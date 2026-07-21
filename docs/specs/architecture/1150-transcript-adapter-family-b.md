# Spec #1150 — Migrate Family B (outbound turn-stream resolvers) onto `internal/transcript`

**Ticket:** [#1150](https://github.com/pyrycode/pyrycode/issues/1150) · split from #973 · size **S** · `security-sensitive`

Mirror of the merged Family A migration (#1149). The outbound-stream resolver family in `cmd/pyry/interactive_turn_stream_v2.go` becomes a thin set of adapters over the `internal/transcript` leaf (#1148, merged in PR #1155). No public signature changes, no consumer cascade — a net **deletion** of duplicated logic (the local `jsonlStreamExt` const + `jsonlStemPattern` regexp go away). The two load-bearing conventions are preserved and re-pinned by test: Family B's **error-on-not-found** (subscriber retries, so every no-result returns a non-nil error, never `("", 0, nil)`) and its **per-subscription cold/warm offset rule** (`resolvedOnce` / `sawEmpty`).

## Files to read first

- `cmd/pyry/interactive_turn_stream_v2.go` — the only production file with logic changes. Read as five units:
  - `jsonlStreamExt:22` (const) + `jsonlStemPattern:55` (regexp) — the two local symbols to **delete** (grep-confirmed: used only inside this file's four resolvers). Both replaced by `transcript.Ext` / `transcript.ValidStem`.
  - `availabilityReporter:34` + `bootstrapProbeUsable:41` — **kept** (see § "What stays local" — byte-identical to Family A's retained `probeUsable`, a load-bearing branch-selector, not resolver core).
  - `resolveLatestSessionJSONL:189` — rewrite over `transcript.Newest`; keep the cold/warm offset closure.
  - `resolveBootstrapJSONL:269` (dispatcher) + `resolveOwnBootstrapJSONL:323` — dispatcher swaps one `MatchString` → `ValidStem`; the probe resolver adopts `CanonicalDir` + `GuardProbedPath` but keeps its per-branch pid/probe/empty/stat orchestration (§ "Why `GuardProbedPath`, not `Probed`").
  - `resolveBoundSessionJSONL:531` — `ValidStem` branch-selector + `StatByID`; keep the cold/warm offset closure.
  - `resolveTarget:467` — the consumer that builds these resolvers. **Read for context only; unchanged.**
- `internal/transcript/transcript.go:36-225` — the primitives to adopt: `Ext:38`, `ValidStem:48`, `Result:77`+`Found:83`, `CanonicalDir:89`, `GuardProbedPath:111`, `StatByID:133`, `Newest:151`. **Read `Probed:204` too, but note it is deliberately NOT adopted here** — it collapses pid≤0 / empty-open / guard-reject / vanished-before-stat into one `(Result{}, nil)`, which erases the per-branch `sawEmpty` distinction Family B's offset rule depends on (§ Design).
- `internal/sessions/reconcile.go:112-285` — the merged Family A sibling adapter to mirror in *shape*. **Divergence to internalise:** Family A uses `transcript.Probed` + swallows its error and keeps **no** cold/warm state (it needs true size every call); Family B keeps the cold/warm state and therefore composes `GuardProbedPath` directly. The `probeUsable:151` / `availabilityReporter:144` retention decision transfers verbatim.
- `cmd/pyry/relay.go:520` — the **second** consumer of `resolveOwnBootstrapJSONL` (`usageResolve`, a separate usage-tracking tail). Signature is preserved (3 args: `dir`, `probe`, `pidFn`), so **this line is unchanged** — confirm the build still compiles it.
- `cmd/pyry/interactive_turn_stream_v2_test.go` — the existing resolver suite + reusable helpers: `writeJSONL:59`, `dummyPidFn:274`, `fakeProbe:292` / `OpenJSONL:292`, `noopFakeProbe:310`. The AC3 pin test builds on these; the existing per-branch tests stay green unchanged.
- `docs/specs/architecture/838-probe-prefer-transcript-resolver.md` § "Error handling" — the source of the error-on-not-found convention this ticket must preserve verbatim (Family B is the retry-on-error side of that inversion).
- `docs/specs/architecture/1149-transcript-adapter-family-a.md` (on `main`, merged commit `e0ff90a`) — the mirror spec; read its § "What stays local" and § "Testing strategy" for the shared idiom.

## Context

`internal/transcript` (#1148) owns the resolver core shared by two near-twin families: dir canonicalisation, the confidentiality guard, by-id / probe / newest-by-mtime selection, and the canonical UUID-stem regexp. #1148 created the leaf **without migrating any consumer** (its code-review pinned "0 consumer imports + 3 regexps intact" as the scope gate). Family A migrated first (#1149, merged). This ticket is the Family B migration.

Family B is the **outbound turn-stream tail** feeding the internet-exposed phone streams. Four resolver factories, each returning a `func(ctx) (path string, startOffset int64, err error)` closure:

- `resolveLatestSessionJSONL` — newest-by-mtime (the `convID == ""` / pre-route bootstrap branch of `resolveTarget`).
- `resolveOwnBootstrapJSONL` — PID-probe-preferred bootstrap tail (which `<uuid>.jsonl` the daemon's OWN child holds open), with a no-lsof delegation to newest-by-mtime.
- `resolveBoundSessionJSONL` — by-id tail of `<sessionID>.jsonl` (the cross-conversation confidentiality property, AC4).
- `resolveBootstrapJSONL` — the pinned-id-vs-probe dispatcher.

Its convention is **inverted from Family A on purpose** (`838-probe-prefer-transcript-resolver.md` § "Error handling"): Family B's subscriber (`NewTargetSubscriber` → `turnbridge`) *retries on error*, so every no-result condition returns a non-nil error, never `("", 0, nil)` — the exact opposite of Family A's growth-baseline, which returns `("", 0, nil)` because a non-nil baseline error would divert its caller to the stochastic Committed-chip fallback. On top of that inversion, Family B carries a **per-subscription cold/warm offset rule** Family A dropped: the first file returned after one or more not-found looks is a cold-start file → offset `0` (stream the whole in-flight reply); a file present at the first look, or any file after one was already returned, → offset `size` (tail from EOF so the internet-exposed phone never replays history). That rule is stateful across calls (`resolvedOnce` / `sawEmpty`) and must be preserved verbatim.

This is a behaviour-preserving move. The offset *semantics* change (caller-stat → owned-fd tail API) is a **separate downstream ticket**, sequenced after this one so the same resolver file is not churned in parallel — this ticket keeps offset derivation caller-side.

## Design

### Migration map — `cmd/pyry/interactive_turn_stream_v2.go`

**Delete** (logic now lives in `internal/transcript`, satisfying AC1's "no probe-guard, dir-canonicalisation, selection, or UUID-stem logic remains duplicated"):

| Deleted symbol | Replaced by |
|---|---|
| `const jsonlStreamExt = ".jsonl"` | `transcript.Ext` |
| `var jsonlStemPattern` (regexp) | `transcript.ValidStem(stem)` |
| inline `os.ReadDir` + newest-by-mtime loop in `resolveLatestSessionJSONL` | `transcript.Newest(dir)` |
| construction-time `filepath.EvalSymlinks(dir)` / `Clean` in `resolveOwnBootstrapJSONL` | `transcript.CanonicalDir(dir)` |
| inline confidentiality guard (dir-compare + `HasSuffix`/stem check) in `resolveOwnBootstrapJSONL` | `transcript.GuardProbedPath(open, canonicalDir)` |
| inline `filepath.Join` + `os.Stat` for the pinned/by-id path in `resolveBoundSessionJSONL` | `transcript.StatByID(dir, sessionID)` (after a local `ValidStem` branch-selector) |

**Import delta:** add `github.com/pyrycode/pyrycode/internal/transcript`; **remove `regexp`** (only `jsonlStemPattern` used it) and **remove `strings`** (its only uses — the two `strings.HasSuffix` calls at `:215` and `:371` — are inside deleted code; `staticcheck` flags either if left). Keep `os` (`os.Stat` still used in the probe resolver's local stat) and `path/filepath` (`filepath.Join(dir, base)` still used there), plus `context` / `fmt` / `log/slog` and all internal imports (`relay`, `sessions`, `sessions/rotation`, `supervisor`, `turnbridge`, `turnevent`, `tuidriver`), all unchanged.

### What stays local (adapter concerns, NOT transcript core)

Do **not** move these into `internal/transcript`, and do not flag them as leftover duplication:

- **`availabilityReporter` + `bootstrapProbeUsable`** (AC1 explicitly: "The `bootstrapProbeUsable` branch-selector stays local"). These detect the no-lsof `noopProbe` via `rotation.Probe`'s optional `Available()` method and select the `resolveLatestSessionJSONL` (newest-by-mtime) vs probe branch inside `resolveOwnBootstrapJSONL`. `transcript.Probe` is deliberately single-method (`OpenJSONL` only, #1148); probe-capability detection and the no-lsof→mtime delegation are Family-B composition policy, none of the four things AC1 forbids duplicating. This is byte-identical to the merged Family A decision (`internal/sessions/reconcile.go:151`, `probeUsable`), so moving or inlining it would contradict #1149. **It is also load-bearing for the error-on-not-found convention:** the no-probe fallback into `resolveLatestSessionJSONL` must still surface a non-nil error on an empty dir.
- **The cold/warm offset closures** (`resolvedOnce` / `sawEmpty` state) — this is exactly what #1148 designed the adapters to keep ("the cold/warm offset semantics stay in the call-site adapters, never here"). The neutral `transcript.Result` carries no offset rule.
- **`perConversationSessionsDir` / `resolveTarget` / `startInteractiveTurnStreamV2`** — the consumers. Untouched.

### Why `GuardProbedPath`, not `Probed`, for the probe resolver

`transcript.Probed` bundles pid-check + probe call + guard + stat and collapses **every** benign no-result — `pid <= 0`, empty open path, guard reject, vanished-before-stat — into a single `(Result{}, nil)`, surfacing only the probe-call error. That is perfect for Family A, which has no cold/warm state. It is **wrong for Family B**, because Family B's current offset rule sets `sawEmpty` on some no-result branches but **not** others:

| Branch in `resolveOwnBootstrapJSONL` | current: sets `sawEmpty`? | current: returns |
|---|---|---|
| `pid <= 0` | **yes** (if `!resolvedOnce`) | non-nil error |
| probe call error | no | non-nil error |
| empty open path (`open == ""`) | **yes** (if `!resolvedOnce`) | non-nil error |
| guard reject (outside dir / non-uuid base) | no | non-nil error |
| vanished between probe and stat | **yes** (if `!resolvedOnce`) | non-nil error |
| success | — | `(candidate, off, nil)` |

Collapsing these through `Probed` would force `sawEmpty` onto the guard-reject branch (a behaviour change on the cold/warm rule), so the probe resolver keeps its per-branch orchestration and adopts only the two transcript primitives that map cleanly: `CanonicalDir` (construction) and `GuardProbedPath` (the confidentiality boundary — the one security-critical, AC1-named piece of duplication). The `pid` / `probe.OpenJSONL` / empty-check / `os.Stat` steps are orchestration, none of AC1's four forbidden categories, and they carry the offset rule and the error-on-not-found convention — so they stay local, faithful to #1148's "conventions live in the adapters" design.

### Rewritten contracts (developer writes the bodies; invariants pinned by § Testing)

All four factory signatures are **unchanged**. Each returns the same `func(ctx context.Context) (path string, startOffset int64, err error)` closure with the same `resolvedOnce` / `sawEmpty` state (except the dispatcher, which is stateless).

**`resolveLatestSessionJSONL(dir string)`** — closure body:
- `res, err := transcript.Newest(dir)`.
- `err != nil` (the `os.ReadDir` error — not-yet-created project dir) → `if !resolvedOnce { sawEmpty = true }`; return `("", 0, fmt.Errorf("read claude sessions dir %s: %w", dir, err))`.
- `!res.Found()` (no matching file) → `if !resolvedOnce { sawEmpty = true }`; return `("", 0, fmt.Errorf("no session jsonl found in %s", dir))`.
- success → `off := res.Size`; `if !resolvedOnce && sawEmpty { off = 0 }`; `resolvedOnce = true`; return `(res.Path, off, nil)`.

`transcript.Newest` preserves the newest-by-mtime pick, the lex-larger tie-break (on stem — identical order to the old name-based tie-break since every name shares `Ext`), the sub-dir / non-uuid / wrong-ext skips, and the vanished-between-ReadDir-and-Stat skip. Byte-identical selection.

**`resolveBootstrapJSONL(dir, pinnedID, probe, pidFn)`** (dispatcher, stateless) — swap the one predicate: `if id := pinnedID(); id != "" && transcript.ValidStem(id) { return resolveBoundSessionJSONL(dir, id) }`, else `return resolveOwnBootstrapJSONL(dir, probe, pidFn)`. Structure unchanged.

**`resolveOwnBootstrapJSONL(dir, probe, pidFn)`**:
1. `if !bootstrapProbeUsable(probe) { return resolveLatestSessionJSONL(dir) }` — the no-lsof delegation, **unchanged**.
2. `canonicalDir := transcript.CanonicalDir(dir)` once (replaces the inline `EvalSymlinks`/`Clean`).
3. Return the cold/warm closure with the **byte-identical per-branch structure** from the table above, adopting `GuardProbedPath` for the guard:
   - `pid := pidFn()`; `pid <= 0` → `sawEmpty` (if `!resolvedOnce`) + non-nil error.
   - `open, err := probe.OpenJSONL(pid)`; `err != nil` → wrapped non-nil error, **no** `sawEmpty`.
   - `open == ""` → `sawEmpty` (if `!resolvedOnce`) + non-nil error.
   - `base, ok := transcript.GuardProbedPath(open, canonicalDir)`; `!ok` → non-nil error, **no** `sawEmpty`.
   - `candidate := filepath.Join(dir, base)`; `os.Stat(candidate)`; error → `sawEmpty` (if `!resolvedOnce`) + non-nil error.
   - success → `off := info.Size()`; `if !resolvedOnce && sawEmpty { off = 0 }`; `resolvedOnce = true`; return `(candidate, off, nil)`.

**`resolveBoundSessionJSONL(dir, sessionID)`**:
- `if !transcript.ValidStem(sessionID) { return "", 0, fmt.Errorf("invalid bound session id %q", sessionID) }` — the path-safety branch-selector, **no** `sawEmpty` (matches current). This pre-check is **not redundant** with `StatByID`'s internal `ValidStem`: it is the branch selector that keeps invalid-stem out of the `sawEmpty`-setting stat-error branch (the same load-bearing role Family A's pinned-id `ValidStem` pre-check plays).
- `res, err := transcript.StatByID(dir, sessionID)`; `err != nil` (file absent / raced) → `if !resolvedOnce { sawEmpty = true }` + `("", 0, fmt.Errorf("stat bound session jsonl %s: %w", filepath.Join(dir, sessionID+transcript.Ext), err))`.
- success → `off := res.Size`; `if !resolvedOnce && sawEmpty { off = 0 }`; `resolvedOnce = true`; return `(res.Path, off, nil)`.

### The one intentional micro-delta (cosmetic, safe direction)

The old guard had two reject branches with two distinct error strings ("probed jsonl outside sessions dir" / "probed file … is not a session jsonl"). `GuardProbedPath` returns a single `ok bool`, so both collapse to one `!ok` branch with one error string. This is an **error-message consolidation only** — both cases still return a non-nil error with no `sawEmpty` (the behaviour AC3 pins), and the probed path was never logged in either message. Note it so review doesn't read it as an accident.

### Consumers — unchanged

`resolveTarget` (`interactive_turn_stream_v2.go:467`) builds all four resolvers; its call sites (`startInteractiveTurnStreamV2`, and via `NewTargetSubscriber` → `turnbridge/producer.go:327` for the three tailers — turn stream, ACP turn stream, ACP permission stream) pass the same `dir` / `off` and never change. `cmd/pyry/relay.go:520` (`usageResolve := resolveOwnBootstrapJSONL(...)`) and `relay.go:733` (`newBootstrapProbe`) both compile against the preserved signatures. `cmd/pyry/acp_turn_streams.go` and `acp_permission_streams.go` are consumers and stay untouched. No file outside `interactive_turn_stream_v2.go` (production) changes.

### Data flow (unchanged; internals relocated)

```
resolveTarget(ctx)                              (per (re)subscription)
  ├─ convID == ""     ─► resolveBootstrapJSONL(dir, bootstrapIDFn, probe, pidFn)
  │        pinnedID() valid ─► resolveBoundSessionJSONL(dir, id)   ─► transcript.StatByID
  │        else            ─► resolveOwnBootstrapJSONL(dir, probe, pidFn)
  │                              !bootstrapProbeUsable ─► resolveLatestSessionJSONL ─► transcript.Newest
  │                              else pidFn()>0 ─► probe.OpenJSONL ─► transcript.GuardProbedPath ─► os.Stat
  ├─ convID → bootstrap host ─► resolveBootstrapJSONL (same as above)
  └─ convID → bound session  ─► resolveBoundSessionJSONL(convDir, sessionID) ─► transcript.StatByID
      each closure: (path, size|0, nil)  |  ("", 0, non-nil error)  → subscriber retries
```

## Concurrency model

No change. The three adopted `transcript` functions (`CanonicalDir`, `StatByID`, `Newest`) and `GuardProbedPath` are pure and stateless — no shared state, no locks. Each resolver closure's `resolvedOnce` / `sawEmpty` are read and written **only** inside the closure, which `NewTargetSubscriber` invokes from the single `Producer.Run` goroutine (Run → subscribe → resolve) — the same single-Run-goroutine invariant the resolvers already document; no mutex is added or needed. `canonicalDir` is computed once at construction and read-only thereafter. `pidFn` reads the live child PID via the mutex-guarded `Supervisor.State()`. `go test -race` continues to validate. The resolvers must still not be called from multiple goroutines — the existing "Do NOT call from multiple goroutines" contract is unchanged.

## Error handling — the load-bearing error-on-not-found convention (preserved)

Family B's subscriber **retries on error**, so the resolver's contract is the inverse of Family A's:

| Resolve result | Consumer behaviour |
|---|---|
| `(path, size, nil)` / `(path, 0, nil)` | subscribe + tail from `startOffset` |
| **non-nil error** (any no-result) | back off and re-resolve (the retry that lets a cold-start file appear, a `/clear` rotation land, a bound session materialise) |

Every no-result branch — dir unreadable, no matching file, `pid <= 0`, probe error, empty open path, guard reject, by-id file absent, invalid stem — returns a non-nil error and `("", 0, ...)`. There is exactly one place `transcript`'s neutral core would silently drop to a nil error: `transcript.Newest` returns `(Result{}, nil)` on no-match and `transcript.StatByID` returns `(Result{}, err)` on stat failure — the adapter maps `!Found()` / `err != nil` to a **non-nil** error in each case (the retry signal), which is the whole point of keeping the mapping in the adapter. `Probed` is not used, so its error-swallowing shape (Family A's) never reaches Family B.

The cold/warm offset rule (offset `0` on cold start so an in-flight reply streams; offset `size` on warm resume so the phone never replays history) is preserved verbatim, keyed off the `resolvedOnce` / `sawEmpty` state per the table in § Design.

## Testing strategy

All in `cmd/pyry/interactive_turn_stream_v2_test.go`, same idiom (`t.TempDir()`, `writeJSONL`, direct factory call, injected `fakeProbe` / `noopFakeProbe` / `dummyPidFn`, `t.Parallel()`).

- **Keep, expect green unchanged** — the migration preserves behaviour, so every existing resolver test passes without edits: `TestResolveLatestSessionJSONL_*` (newest / re-eval / tie-break / ignores-non-session / empty-dir-errors / unreadable-dir-errors / cold-start-0 / warm-start-size), `TestResolveOwnBootstrapJSONL_*` (probe-wins / probe-empty-no-mtime / pid-zero-error / cold-start-0 / warm-start-size / probe-outside-dir-rejected / noop-falls-back-to-mtime), `TestResolveBoundSessionJSONL_*` (keys-off-id / cold-start-0 / warm-start-size / invalid-id-errors), `TestResolveBootstrapJSONL_*` (pinned-prefers-by-id / no-pinned-falls-back-to-probe), and the `resolveTarget` / stream integration tests. Run them first; a red test here means the adapter diverged.
- **Add the AC3 convention pin** — one focused table-driven test (e.g. `TestResolvers_NoResultReturnsNonNilError`) that enumerates **every** no-result branch and asserts `path == "" && offset == 0 && err != nil`, so a future edit flipping any branch to `("", 0, nil)` fails loudly. Scenarios (inputs → expect non-nil error):
  - `resolveLatestSessionJSONL`: unreadable/missing dir; dir with only non-uuid files (no match).
  - `resolveOwnBootstrapJSONL`: `pid <= 0` (via a `pidFn` returning 0); `fakeProbe` returning an error; `fakeProbe` returning `""`; `fakeProbe` returning a valid path **outside** `dir` (guard reject).
  - `resolveBoundSessionJSONL`: valid-but-absent `<sessionID>.jsonl` (by-id file absent); a non-uuid `sessionID` (invalid stem).
  - Add a comment naming **AC3** and stating the intent (the convention is inverted from Family A on purpose; a nil error would redirect the subscriber off its retry loop).
- **Full suite:** `make check` (`go vet ./...`, `staticcheck ./...`, `go test -race ./...`). `staticcheck` is the deterministic backstop for the `regexp` / `strings` import removals.

## Open questions

1. **Consolidate vs. augment for AC3.** The AC3 pin overlaps existing per-branch tests (`_EmptyDirErrors`, `_PidZeroReportsNotFound`, `_ProbePathOutsideDirRejected`, `_InvalidIDErrors`). Recommendation: **add** the consolidated pin rather than delete the per-branch tests — the per-branch tests assert richer behaviour (offset, which-file), the AC3 pin asserts the single cross-cutting convention. Cheap redundancy; the AC3 test is the one that fails on a future convention flip. Not blocking.

## Not in scope

- **The owned-fd tail-API adoption (offset semantics change).** AC2: offset derivation stays caller-side in this ticket. The caller-stat → owned-fd migration is the downstream sibling, sequenced after this so the resolver file is not churned in parallel.
- **Family A (`internal/sessions/reconcile.go`).** Already migrated (#1149, merged). Do not touch.
- **The dead-wired modal `resolveTarget`.** Technical Notes: the modal stream builds a `resolveTarget` that is dead-wired (screenOnly since #1070 never calls `target.Resolve`). Leave it as-is (a separate dead-code cleanup) — the resolver-body rewrite does not make deleting it trivial.
- **`internal/sessions/rotation/watcher.go`'s local `uuidStemPattern`** — not a resolver family; #1148 left it intact, out of scope.
- **`docs/knowledge/codebase/1150.md`** — owned by the documentation phase, written after merge. Not a developer deliverable.

## Security review

**Verdict:** PASS

Adversarial self-review per `architect/security-review.md`, gated on the `security-sensitive` label. This ticket introduces **no new trust decision** — it relocates #838/#679's confidentiality guard and error-on-not-found convention onto the already-security-reviewed `internal/transcript` leaf (#1148, PASS) and mirrors the merged Family A review (#1149, PASS). The audit confirms the relocation preserves each decision and adds no new surface.

**Findings:**

- **[Trust boundaries]** No MUST FIX. The single untrusted→trusted crossing — the probe-reported path `probe.OpenJSONL(pid)`, which under PID reuse could name any file on disk — moves from inline closure code to `transcript.GuardProbedPath` (invoked directly, not via `Probed`, so Family B keeps its per-branch orchestration). Verified equivalent to the pre-migration inline guard: `EvalSymlinks(probed)` (Clean on error) → reject when `filepath.Dir(resolved) != canonicalDir` → require a `<uuid>.jsonl` base via `sessionFileStem`/`ValidStem`. The adapter passes `canonicalDir = transcript.CanonicalDir(dir)` (= the old `resolvedDir`), the correct-usage path — `GuardProbedPath`'s documented misuse mode (raw un-canonicalised dir) *fails closed* anyway. The boundary is now a named, separately-tested function rather than inline logic. `dir` is server-derived (the daemon's `claudeSessionsDir` / `DefaultClaudeSessionsDir(workdir)`), not caller input; the PID source is server-owned (`Supervisor.State().ChildPID`).
- **[File operations]** No MUST FIX. Path traversal stays closed on all three read paths: (1) the by-id path — `transcript.StatByID` validates `ValidStem(sessionID)` **before** any `filepath.Join`, and the adapter's local `ValidStem` pre-check gates it too (36 hex/hyphen chars + fixed `.jsonl`, no `/`, no `..`); (2) the probe path — `candidate := filepath.Join(dir, base)` with `base` through the `GuardProbedPath` gate; (3) the newest path — `transcript.Newest` only ever joins a `sessionFileStem`-validated name under `dir`. **AC4 (cross-conversation confidentiality) preserved:** the by-id resolver still tails a FIXED `<sessionID>.jsonl` keyed off the bound session id, so another session writing more recently can never redirect the tail — the path is not mtime-derived. TOCTOU: the guard→`os.Stat` sequence is inherently racy, but the sole use is a size read for an offset; worst case is a mis-sized tail (correctness), never a content leak — and the resolver returns only `(path, offset)`, the tailer opens and reads the file downstream, unchanged by this ticket. No files created (read-only resolvers) → no mode / atomic-write surface. Symlinks are canonicalised on both sides before the dir compare (an in-dir symlink pointing out is rejected).
- **[Tokens/secrets]** No findings — the path generates, stores, and logs no token, key, or credential.
- **[Subprocess]** No findings — no `exec` added. The probe's `lsof` lives in `internal/sessions/rotation` (unchanged); the only value crossing into it is the daemon's own child PID, not caller input.
- **[Cryptographic primitives]** No findings — no crypto, no RNG. `ValidStem` is a regexp shape check, not a secret comparison.
- **[Network & I/O]** No findings — no socket, no unbounded read; `os.Stat` metadata (size) only, plus one `os.ReadDir` of the daemon's own projects folder (bounded, server-owned), no size cap needed. The resolver hands the internet-exposed phone stream a start offset, never file bytes.
- **[Error messages, logs, telemetry]** No MUST FIX, net-neutral. The error-on-not-found convention means every no-result branch returns a wrapped error, but each wrap carries only a server-derived path (`dir`) / a path-and-errno (`os.Stat` / `os.ReadDir` error) / a server-minted `sessionID` — never file bytes and never the raw probed path. The one micro-delta (§ Design) *removes* one of two guard error strings, reducing surface. The probed path from `probe.OpenJSONL` is consumed by `GuardProbedPath` and only its validated `base` is ever rebuilt/named — the raw external path never reaches a log or error value.
- **[Concurrency]** No MUST FIX — see § Concurrency model. All adopted `transcript` functions are pure/stateless; each closure's `resolvedOnce`/`sawEmpty` are confined to the single `Producer.Run` goroutine (documented invariant, unchanged); `canonicalDir` is computed once and read-only; `sup.State()` is mutex-guarded. No new lock, goroutine, or shared state. The `!Probed` decision means Family B adds no cross-adapter state coupling either. `go test -race` validates.
- **[Threat model alignment]** Addresses the target threat unchanged: a second interactive claude in the shared `~/.claude/projects/<encoded-cwd>/` dir cannot redirect the outbound tail onto its own newer transcript (the phone would otherwise receive another conversation's output), because the probe-preferred tail + the by-id tail + the same guard + the same error-on-not-found retry convention now source from the reviewed leaf. Moving the guard to `internal/transcript` adds no attack surface — the leaf imports stdlib only and `GuardProbedPath` is a pure function of two server-derived strings. The offset-semantics change (owned-fd tail API) is explicitly deferred to the downstream sibling and is not touched here. The `security-sensitive` label is correct and retained.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-21
