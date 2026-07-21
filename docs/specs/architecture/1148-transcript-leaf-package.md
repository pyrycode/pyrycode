# Spec #1148 — `internal/transcript`: one probe-preferred resolver core + shared UUID-stem constant

**Ticket:** [#1148](https://github.com/pyrycode/pyrycode/issues/1148) · split from #973 · size **S** · `security-sensitive`

This is the **foundation slice**: it ships a new leaf package with internal tests and **wires no consumer**. The two resolver families (Family A `internal/sessions`, Family B `cmd/pyry`) and the rotation watcher stay byte-identical; they migrate onto this package in the sibling tickets (blocked by this one). Per the "new package AND its first consumer" always-split rule, the first consumer is out of scope here.

## Files to read first

- `internal/sessions/reconcile.go:13-285` — **Family A** (the mechanics to extract; **untouched by this ticket**). `jsonlExt` (13), `uuidStemPattern` (18), `mostRecentJSONL` (78) = the newest-by-mtime scan with lexicographic tie-break, `newProbePreferredTranscriptResolver` (224) = pinned→probe dispatch + the AC4 guard + `(path, size)` return with the `("",0,nil)` not-found convention. This is the reference behaviour the core must preserve; the *convention* (nil-empty) is A's, NOT the core's.
- `cmd/pyry/interactive_turn_stream_v2.go:189-563` — **Family B** (**untouched by this ticket**). `resolveLatestSessionJSONL` (189) = mtime + `resolvedOnce/sawEmpty` cold/warm offset, `resolveBootstrapJSONL` (269) = pinned-vs-probe dispatch (note: **different order** from A), `resolveOwnBootstrapJSONL` (323) = probe + guard, `resolveBoundSessionJSONL` (531) = by-id + cold/warm. Shows the divergent not-found (**error**) + offset (**cold/warm**) conventions that stay in the adapter, never the core.
- `cmd/pyry/interactive_turn_stream_v2.go:48-55` — `jsonlStemPattern`, the 2nd of the three byte-identical duplicate regexps.
- `internal/sessions/rotation/watcher.go:17-19` — `uuidStemPattern`, the 3rd duplicate. The rotation package is already a leaf (imports no `internal/sessions`); it later imports `internal/transcript` for the constant with no back-edge.
- `internal/sessions/rotation/probe.go:12-20` — `Probe interface { OpenJSONL(pid int) (string, error) }`. The shape the new `transcript.Probe` mirrors. **Do NOT import `rotation`** — redeclare the one-method interface locally so the new package stays a leaf (AC3); rotation's `*Probe` values satisfy it structurally.
- `internal/sessions/reconcile_test.go:43,177-269` — Family A test idiom: `touchJSONL` (43), `t.TempDir()`, `TestNewTranscriptResolver_*` (177-247), scriptable probes `stubProbe`/`unavailableProbe`/`mustNotProbe` (250-424). Mirror this idiom.
- `cmd/pyry/interactive_turn_stream_v2_test.go:59,277-292,317-430` — Family B test idiom: `writeJSONL` (59), `probeResult` (277), `fakeProbe{results, pids}` (286) with a **call-count field** (`pids`) for the "probe not called on pid≤0" assertion, `TestResolveOwnBootstrapJSONL_*` (317-430) — the exact #838 scenario matrix to replicate against the new core.
- `docs/specs/architecture/838-probe-prefer-transcript-resolver.md` — the AC4 confidentiality guard rationale, the load-bearing **inverted** not-found conventions, and the deliberately-dropped cold/warm state. The core carries the guard faithfully and excludes the convention + offset on purpose.

## Context

Transcript resolution exists today as two near-duplicate families plus a triplicated constant (regexp byte-identical across all three sites, verified):

| | Family A (`reconcile.go`) | Family B (`interactive_turn_stream_v2.go`) |
|---|---|---|
| Consumer | inbound delivery-confirm growth baseline | outbound turn-stream tail |
| Return | `(path, size, err)` | `(path, offset, err)` |
| Not-found | `("", 0, nil)` (nil error) | wrapped error (retry) |
| Offset rule | none — always current `size` | `resolvedOnce/sawEmpty` cold(0)/warm(size) |
| Pinned dispatch | probe-usable checked first; pinned ignored when no lsof; pinned re-read **per resolve** | pinned checked first (wins over no-lsof); read **once per subscription** |

Both share the same **core mechanics**: dir canonicalisation, the confidentiality guard (canonicalise the probe-reported path, require it to live directly in the trusted sessions dir with a `<uuid>.jsonl` base), by-id / pinned stat, and newest-by-mtime selection. This ticket extracts that core **once** into a leaf package.

The families' *divergent* concerns — the inverted not-found conventions, the cold/warm offset semantics, and the two different pinned-vs-probe dispatch orders — deliberately stay at the call-site adapters. They are exactly what makes A and B behaviourally distinct, so folding any of them into the core would change one family's behaviour. The core stays neutral; the adapters compose it in their own order with their own convention.

## Design

### Package placement & the cycle-breaker (AC3)

New package `internal/transcript`, imports **stdlib only** (`os`, `path/filepath`, `regexp`, `strings`). It imports **neither** `internal/sessions` **nor** `internal/sessions/rotation`, so all three (`sessions`, `rotation`, `cmd/pyry`) can later import it with no cycle. The probe is accepted via a **locally-defined one-method interface** — redeclared, not imported from `rotation` — which rotation's existing probes satisfy structurally:

```go
// Probe reports which JSONL a pid currently holds open ("" = none). Redeclared
// here (not imported from rotation) to keep this package a leaf; rotation's
// *Probe values satisfy it structurally.
type Probe interface{ OpenJSONL(pid int) (string, error) }
```

The core is a **set of composable building blocks**, not a single resolver closure. A single mega-resolver cannot serve both families without a behaviour change (the dispatch orders and pinnedID cadence diverge — see Context table); the building-block shape is what lets each adapter stay thin while preserving its own behaviour.

### Exported surface (contract — the developer writes the bodies)

```go
const Ext = ".jsonl"                                   // canonical transcript suffix (single source of truth)

func ValidStem(stem string) bool                       // AC1: the one UUID-stem matcher; replaces 3 local regexps
func CanonicalDir(dir string) string                   // AC2: EvalSymlinks, Clean(dir) on error — caller precomputes once
func GuardProbedPath(probed, canonicalDir string) (base string, ok bool)  // AC2: the confidentiality guard, standalone

type Result struct {                                   // AC4: neutral — no not-found convention, no offset rule
    Path string
    Size int64
}
func (r Result) Found() bool                            // Path != ""

func StatByID(dir, id string) (Result, error)          // AC2: by-id / pinned — stat <dir>/<id>.jsonl
func Newest(dir string) (Result, error)                // AC2: newest-by-mtime selection
func Probed(dir, canonicalDir string, probe Probe, pid int) (Result, error)  // AC2: probe-preferred resolution
```

New exported **types/interfaces**: `Result`, `Probe` (2 — under the 5-type red line). No `context.Context` in the core: the `func(ctx) (path, X, err)` closure signature belongs to the adapters (`supervisor.Config.ResolveTranscript`, `turnbridge` resolver), which wrap these context-free primitives.

### Behaviour contracts (invariants pinned by § Testing)

**`ValidStem`** — matches `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$` (byte-identical to the three regexps it replaces). An unexported `sessionFileStem(name) (stem string, ok bool)` strips `Ext` then `ValidStem`; used by `Newest` and `GuardProbedPath`.

**`CanonicalDir(dir)`** — `filepath.EvalSymlinks(dir)`, falling back to `filepath.Clean(dir)` on error. Same canonicalisation both families and the rotation watcher already do. The caller precomputes it once (per construction/subscription) and passes it to `GuardProbedPath` / `Probed`.

**`GuardProbedPath(probed, canonicalDir)`** — the single confidentiality boundary. Canonicalise `probed` (EvalSymlinks, Clean on error); accept iff `filepath.Dir(resolved) == canonicalDir` **and** `sessionFileStem(filepath.Base(resolved))` is ok. On accept return `(base, true)` where `base` is the resolved base name the caller rebuilds under the **original** `dir`; on reject return `("", false)`. No filesystem mutation, stat-free.

**`Result`** — the neutral carrier (AC4). `Found()` ⟺ `Path != ""`. A caller derives a growth baseline as `Size` directly, or a tail offset as `Size` (warm) / `0` (cold) using its own cold/warm state. The core encodes neither the not-found convention nor the offset rule.

**`StatByID(dir, id)`** — validates `id` via `ValidStem` **before any `filepath.Join`** (path-traversal safety, defense-in-depth even for server-minted ids). Invalid stem → `(Result{}, err)`, no fs touch. Else `filepath.Join(dir, id+Ext)` + `os.Stat`: hit → `(Result{path, size}, nil)`; absent/unreadable → `(Result{}, err)` with the raw `os.Stat` error. The adapter maps the error to its convention (A swallows to nil-empty; B wraps).

**`Newest(dir)`** — `os.ReadDir`; readdir error → `(Result{}, err)`. Scan for `sessionFileStem`-valid `.jsonl` entries, pick max `ModTime` with lexicographically-larger stem as tie-break (matches `mostRecentJSONL` + `resolveLatestSessionJSONL` — deterministic for tests, stable across map-free iteration); stat the winner. No match → `(Result{}, nil)`. Hit → `(Result{path, size}, nil)`.

**`Probed(dir, canonicalDir, probe, pid)`** — probe-preferred resolution:
- `pid <= 0` → `(Result{}, nil)`; the probe is **not** called.
- `open, err := probe.OpenJSONL(pid)`: `err != nil` → `(Result{}, err)` (the one genuinely-erroneous condition — surfaced so a retry-on-error adapter can wrap/log it); `open == ""` → `(Result{}, nil)`.
- `base, ok := GuardProbedPath(open, canonicalDir)`: `!ok` → `(Result{}, nil)`.
- `candidate := filepath.Join(dir, base)`; `os.Stat`: error (raced away) → `(Result{}, nil)`; hit → `(Result{candidate, size}, nil)`.

Every benign no-result (pid down, empty open, guard reject, raced stat) collapses to `(Result{}, nil)` — the core carries **no** not-found *vocabulary* (AC4). The one surfaced error is the probe call failing. The guard is *also* exposed standalone (`GuardProbedPath`) so a security-conscious adapter that wants to log rejections can call it directly and log on `!ok`, rather than have the log baked into the core.

### Data flow (how the adapters will compose it — for context only; not built here)

```
Family A adapter (later):  Result := ByID-or-Probed-or-Newest per A's dispatch
                             baseline = Result.Size ;   !Found() → ("",0,nil)
Family B adapter (later):  Result := ByID-or-Probed-or-Newest per B's dispatch
                             offset  = cold ? 0 : Result.Size ;   !Found() → wrapped err
```

## Concurrency model

No goroutines, no shared mutable state, no locks. Every exported function is a pure computation over its arguments plus read-only `os` syscalls (`ReadDir`, `Stat`, `EvalSymlinks`). The cold/warm offset state (`resolvedOnce`/`sawEmpty`) that made the family closures stateful lives entirely in the adapters and is deliberately **not** reproduced here — the core is safe to call from any goroutine. `go test -race` over the new package suffices; there is no cross-goroutine invariant to assert.

## Error handling

The core distinguishes exactly two outcomes and never invents a convention between them:
- **Resolved** → `Result{Path, Size}` with `Found() == true`, nil error.
- **Not resolved** → `Result{}` (`Found() == false`). The error is nil for every *benign* no-result (no match, pid down, empty open, guard reject, raced stat) and non-nil only for a genuine I/O failure the caller may want to surface (`os.ReadDir` in `Newest`; `probe.OpenJSONL` in `Probed`; `os.Stat`/invalid-stem in `StatByID`).

The adapter, not the core, decides whether `!Found()` renders as a nil-empty baseline (Family A) or a retryable wrapped error (Family B), and whether a surfaced error is swallowed or wrapped. This is the load-bearing separation from #838: the not-found convention is consumer-specific and must not leak into the shared core.

## Testing strategy (AC5)

Table-driven, `internal/transcript/transcript_test.go`, mirroring the two existing idioms (`t.TempDir()`, `touchJSONL`/`writeJSONL`-style helpers, `t.Parallel()`). A local fake probe struct implements `transcript.Probe` (`OpenJSONL(pid) (string, error)`) with a **scriptable return + a recorded-pids/call-count field** so "probe not called" is directly assertable (mirror `fakeProbe.pids`). No live claude, no `internal/sessions`/`rotation` import in the test.

Scenarios (each an assertion on `Result` + error):

- **`Probed` — probe-preferred over newer foreign sibling (AC5):** fake probe returns the daemon's own `<uuidA>.jsonl` (smaller/older); a newer, larger foreign `<uuidB>.jsonl` also in dir. Expect `Result{ownPath, ownSize}`, `Found()`, and the recorded pid equals the passed pid — proving mtime is not consulted.
- **`Probed` — `pid <= 0` (probe not called):** pass `0` and `-1`. Expect `Result{}`, nil error, and the fake's recorded-pids list is empty.
- **`Probed` — empty probe:** `OpenJSONL` returns `("", nil)`. Expect `Result{}`, nil error.
- **`Probed` — errored probe:** `OpenJSONL` returns `("", someErr)`. Expect `Result{}` and a **non-nil** error (the one surfaced condition).
- **`Probed` / `GuardProbedPath` — guard reject, path outside dir:** probe returns a valid-UUID `.jsonl` in a *different* temp dir. Expect `Result{}`, nil error (`GuardProbedPath` returns `ok == false`).
- **`Probed` / `GuardProbedPath` — guard reject, non-UUID stem:** probe returns `<dir>/not-a-uuid.jsonl`, and separately a valid-UUID name without the `.jsonl` suffix. Expect reject both.
- **`Probed` — vanished between probe and stat:** probe returns an in-dir valid path with no file on disk. Expect `Result{}`, nil error.
- **`StatByID` — hit and miss:** an existing `<dir>/<id>.jsonl` → `Result{path, size}`; a missing one → `Result{}` + error; an invalid stem → `Result{}` + error with **no filesystem access** (assert via a dir that would panic/observe if touched, or simply that the returned path is empty).
- **`Newest` — newest-by-mtime pick + empty dir:** three `<uuid>.jsonl` at distinct mtimes → the max-mtime path; a mtime tie → lexicographically-larger stem; an empty dir → `Result{}`, nil error; a missing dir → `Result{}` + error.
- **`ValidStem`:** accepts a canonical lowercase UUIDv4 stem; rejects uppercase, wrong length, non-hex, and a stem carrying the `.jsonl` suffix.
- **`GuardProbedPath` — accept:** an in-dir `<uuid>.jsonl` (and an in-dir symlink resolving to one) → `(base, true)` with the resolved base.

**Scope-boundary assertion (the "new package AND its first consumer" gate, folded into this test AC):** no consumer is wired. `internal/sessions/reconcile.go`, `cmd/pyry/interactive_turn_stream_v2.go`, and `internal/sessions/rotation/watcher.go` remain byte-identical (the three duplicate regexps stay; they migrate in the sibling tickets). Full suite green: `go test -race ./...`, `go vet ./...`, `staticcheck ./...` (`make check`). Because the new package has **zero importers**, no existing test can regress — the change is invisible outside `internal/transcript`.

## Open questions

1. **Family B error granularity on migration (not this ticket).** `Probed` collapses guard-reject / empty-open / raced-stat to `(Result{}, nil)`, so when Family B (sibling) migrates it loses its per-condition error strings ("has no jsonl open yet" vs "outside sessions dir") in favour of one generic retry message. This is a log-text change only — control flow (retry) is unchanged, and a security-conscious adapter can still call `GuardProbedPath` directly to log rejections. Flagged for the Family B migration ticket; not gating here.
2. **`internal/transcript` file split.** One file (`transcript.go`, ~180 LOC) is expected; the developer may split into `transcript.go` (const/predicate/guard/types) + `resolve.go` (StatByID/Newest/Probed) if it reads better. Either stays within the ≤3-file bound.
3. **`availabilityReporter` / `probeUsable` stays in the adapters.** The no-lsof→mtime *dispatch decision* (both families' local `availabilityReporter` + `probeUsable`/`bootstrapProbeUsable`) is not extracted here — it is part of the divergent dispatch order (Context table), not one of AC2's four owned mechanics. A later consolidation ticket may DRY it once both adapters are on the new core; out of scope for the foundation slice.

## Not in scope

- **Wiring any consumer.** Family A, Family B, and the rotation watcher are untouched. Their migration (drop the local regexp/constant, compose the new primitives, keep each family's own convention + offset + dispatch) is the sibling tickets blocked by this one.
- **Deleting the duplicated symbols** (`mostRecentJSONL`, `newProbePreferredTranscriptResolver`, the `resolve*JSONL` family, the three regexps). They are removed only when their consumers migrate onto `internal/transcript`, per sibling ticket.
- **The `availabilityReporter` dispatch helper** — Open question 3.

## Security review

**Verdict:** PASS

Adversarial self-review per `architect/security-review.md`, gated on the `security-sensitive` label. Framing: this slice wires **no consumer**, so the currently-shipping inline guards in both families stay byte-identical and production behaviour is unchanged — there is no regression window. The review therefore audits whether the extracted core, once the sibling adapters consume it, remains safe. The one new trust decision is the AC4 confidentiality guard, now a single named function.

**Findings:**

- **[Trust boundaries]** No MUST FIX. The single untrusted→trusted crossing is the probe-reported path `open := probe.OpenJSONL(pid)` (under PID reuse the OS fd table could name any file on disk). The boundary is now **one named, standalone, independently-tested function** — `GuardProbedPath` — tighter than today's two inline duplicates. It canonicalises both sides (`EvalSymlinks` on the probed path; the caller precomputes `canonicalDir := CanonicalDir(dir)` from the trusted sessions dir) and accepts only on `Dir(resolved) == canonicalDir` AND a `<uuid>.jsonl` base; reject → `("", false)` → `Probed` returns `Result{}`. Contract documented: `canonicalDir` MUST be produced by `CanonicalDir`. Misuse (passing a raw, un-canonicalised dir) **fails closed** — the probed side is already symlink-resolved, so a mismatched comparison rejects rather than spuriously accepting a different real dir.
- **[File operations]** No MUST FIX. Path traversal is closed on every path: `StatByID` runs `ValidStem` **before** any `filepath.Join` (a clean UUID stem has no `/` or `..`), and `Probed` joins only a guard-validated `base`; `filepath.Join(dir, base)` with `dir` trusted and `base` a fixed-shape `<uuid>.jsonl` cannot escape `dir`. Symlinks are canonicalised on **both** sides before the dir-equality compare, so an in-dir symlink pointing out is rejected (comparison over real paths). No files are created (read-only: `ReadDir`/`Stat`/`EvalSymlinks`) → no mode/atomic-write surface. **TOCTOU** (accepted, not gating): `Probed` does `GuardProbedPath` then `os.Stat(candidate)` with a gap — identical to the pre-existing families' stat-race; the sole use of the result is a metadata `Size`, so a swap-in-the-gap mis-sizes a baseline (correctness) and never discloses content (this package reads no bytes).
- **[Tokens/secrets]** No findings — the package generates, stores, and logs no token, key, or credential; `ValidStem` is a structural matcher, not a secret comparison.
- **[Subprocess]** No findings — the package runs no `exec`. The probe's `lsof`/`/proc` invocation lives in `internal/sessions/rotation` (unchanged, **not imported** — leaf boundary); `Probe` here is an injected interface, and the only value crossing it is the caller-supplied `pid int`, not attacker input.
- **[Cryptographic primitives]** No findings — no crypto, no RNG on this path.
- **[Network & I/O]** No MUST FIX. No sockets, no network, no file-**content** read (only `ReadDir`/`Stat` metadata), so no read-size cap is needed. SHOULD-CONSIDER (deferred, not gating): `Newest` does `ReadDir` + a `Stat` per matching entry — O(n) in the dir size — so a local actor stuffing the shared `~/.claude/projects/<encoded-cwd>/` with a huge number of `<uuid>.jsonl` files would make the scan expensive. This is a **pre-existing** property faithfully preserved from `mostRecentJSONL` / `resolveLatestSessionJSONL` (both already `ReadDir`+`Stat`-all); the dir is local-per-user (not attacker-remote) and the modelled threat writes a handful of transcripts, not millions. No new exposure vs today.
- **[Error messages, logs, telemetry]** No findings, net-positive. The core emits **no** logs (logging is the adapter's job). Returned error VALUES wrap `os` errors (path/errno, never file bytes) over the **trusted** dir + a validated stem — no attacker-controlled content. A guard rejection returns `Result{}` with a **nil** error, so the (potentially attacker-influenced) rejected path is **not** formatted into any returned string — an improvement over the families, which currently interpolate the rejected path into an error. A security-conscious adapter that wants rejection observability calls `GuardProbedPath` directly and logs on `!ok`, deliberately.
- **[Concurrency]** No MUST FIX — § Concurrency model. No goroutines, no locks, no shared mutable state; the stateful cold/warm machinery is deliberately excluded from the core, so there is no TOCTOU-on-shared-state, no lock-ordering, and no goroutine-leak surface. `go test -race` over the pure functions suffices.
- **[Threat model alignment]** Addresses the target threat (#827/#838: a second interactive claude in the shared dir redirecting a resolver onto its own newer transcript → false-ack / cross-stream) by extracting the guard that defeats it into one authoritative, tested function the adapters will consume. Explicitly OUT OF SCOPE for this slice: the threat is not yet *mitigated by this ticket in production* — mitigation already lives in the families' unchanged inline guards and re-lands via the sibling migration tickets; the core adds the shared home, not a live rewire. No regression window (three consumer files byte-identical). The `security-sensitive` label is correct and retained (the extracted guard is the shared shared-dir confidentiality boundary).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-21
