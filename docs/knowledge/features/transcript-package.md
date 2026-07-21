# `internal/transcript` — probe-preferred resolver core + shared UUID-stem constant

**Status (2026-07-21): two of three consumers migrated.** Family B (`cmd/pyry/interactive_turn_stream_v2.go`) migrated onto this package in #1150 (PR #1158) — its local `jsonlStreamExt` const and `jsonlStemPattern` regexp are gone, replaced by `transcript.Ext` / `transcript.ValidStem` / `transcript.Newest` / `transcript.CanonicalDir` / `transcript.GuardProbedPath` / `transcript.StatByID`. `internal/sessions/rotation/watcher.go` migrated in #1151 (PR #1159) — its local `uuidStemPattern` is gone, replaced by `transcript.ValidStem` alone (it sheds only `regexp`; unlike the other two families it keeps `strings` for an unrelated `.jsonl`-suffix check). Family A (`internal/sessions/reconcile.go`, #1149) has a merged spec but its implementation is still on `feature/1149`, not yet on `main` — it carries its own inline logic and duplicate `uuidStemPattern` regexp until that branch merges.

## Why

Transcript resolution (finding the `<uuid>.jsonl` claude is currently writing under `~/.claude/projects/<encoded-cwd>/`) existed as two near-duplicate families plus a triplicated UUID-stem regexp:

- **Family A** (`internal/sessions/reconcile.go`) — inbound delivery-confirm growth baseline. Returns `(path, size, err)`; not-found = `("", 0, nil)`. See [codebase/838.md](../codebase/838.md), [sessions-package.md](sessions-package.md).
- **Family B** (`cmd/pyry/interactive_turn_stream_v2.go`) — outbound turn-stream tail. Returns `(path, offset, err)`; not-found = a wrapped error (retried by the caller). Has a `resolvedOnce`/`sawEmpty` cold/warm offset rule Family A lacks. Migrated onto this package in [codebase/1150.md](../codebase/1150.md).
- Both, plus `internal/sessions/rotation/watcher.go`, carried an identical UUID-stem regexp. The rotation watcher's copy migrated to `transcript.ValidStem` in [codebase/1151.md](../codebase/1151.md) — it only ever needed the matcher, not the fuller resolver shape Family A/B use.

Both families share the same core mechanics — dir canonicalisation, the AC4 confidentiality guard (a second claude process writing into the same shared dir must not be able to redirect a resolver onto its own newer transcript — the #827/#838 threat), by-id/pinned stat, and newest-by-mtime selection. This package extracts that core **once**. The families' *divergent* concerns — the inverted not-found convention, the cold/warm offset rule, and the two different pinned-vs-probe dispatch orders — deliberately stay in the call-site adapters; folding any of them into the core would change one family's behaviour.

## Design

**A leaf package, stdlib-only.** Imports neither `internal/sessions` nor `internal/sessions/rotation`, so `sessions`, `rotation`, and `cmd/pyry` can all later import it without a cycle. The probe is accepted via a **locally-redeclared** one-method interface rather than importing `rotation.Probe`:

```go
type Probe interface{ OpenJSONL(pid int) (string, error) }
```

`rotation`'s existing `*Probe` values satisfy this structurally — no import needed.

**Composable primitives, not a mega-resolver.** A single resolver closure can't serve both families without changing one family's behaviour (their pinned-vs-probe dispatch orders and pinned-id read cadence diverge). Instead the package exposes independent building blocks the adapters compose in their own order:

```go
const Ext = ".jsonl"

func ValidStem(stem string) bool                                          // the one UUID-stem matcher, replaces 3 local regexps
func CanonicalDir(dir string) string                                      // EvalSymlinks, Clean(dir) on error
func GuardProbedPath(probed, canonicalDir string) (base string, ok bool)  // the confidentiality guard, standalone

type Result struct { Path string; Size int64 }
func (r Result) Found() bool

func StatByID(dir, id string) (Result, error)                            // by-id / pinned — stat <dir>/<id>.jsonl
func Newest(dir string) (Result, error)                                  // newest-by-mtime selection
func Probed(dir, canonicalDir string, probe Probe, pid int) (Result, error) // probe-preferred resolution
```

**`Result` is neutral by design (AC4).** It carries neither a not-found convention nor an offset rule. A caller derives a growth baseline as `Result.Size` directly (Family A), or a tail offset as `Result.Size` (warm) / `0` (cold) using its own cold/warm state (Family B). `!Found()` renders however the adapter wants — nil-empty for Family A, a wrapped retryable error for Family B.

**Error handling.** Exactly two outcomes: resolved (`Found() == true`, nil error) or not resolved (`Result{}`). Every *benign* no-result — no match, `pid <= 0`, empty probe response, guard rejection, a file that vanishes between probe/scan and stat — collapses to `(Result{}, nil)`. The only non-nil errors are genuine I/O failures the caller may want to surface: `os.ReadDir` in `Newest`, `probe.OpenJSONL` in `Probed`, `os.Stat`/invalid-stem in `StatByID`.

**No goroutines, no locks, no shared state.** Every exported function is a pure computation over its arguments plus read-only `os` syscalls (`ReadDir`, `Stat`, `EvalSymlinks`). The stateful cold/warm machinery that made the family closures stateful lives entirely in the adapters, not here — safe to call from any goroutine.

## The confidentiality guard (`GuardProbedPath`)

The single untrusted→trusted crossing: `probe.OpenJSONL(pid)` returns a path derived from the OS fd table, which under PID reuse could name any file on disk. `GuardProbedPath` closes it — now a single named, standalone, independently-tested function (tighter than the two inline duplicates it replaces):

- Canonicalises the probed path (`EvalSymlinks`, `Clean` on error).
- Accepts only if `filepath.Dir(resolved) == canonicalDir` **and** the base is a valid `<uuid>.jsonl` (`sessionFileStem`).
- `canonicalDir` **must** be produced by `CanonicalDir` over the trusted sessions dir. Misuse (a raw, un-canonicalised dir) fails closed — the probed side is already resolved, so a mismatched compare rejects rather than spuriously accepting a different real dir.
- On reject: `("", false)`, no filesystem mutation. A rejected path is never formatted into a returned error string (an improvement over the families, which currently interpolate it) — a security-conscious adapter that wants rejection observability calls `GuardProbedPath` directly and logs on `!ok`.

`StatByID` validates `id` via `ValidStem` **before** any `filepath.Join` — path-traversal safety even for server-minted ids, since a clean UUID stem has no `/` or `..`.

**TOCTOU (accepted, not gating):** `Probed` does `GuardProbedPath` then `os.Stat` with a gap — identical to the pre-existing families' stat-race. The result is only a metadata `Size`; a swap in the gap mis-sizes a baseline (correctness) but discloses no content (the package reads no bytes).

## Not in scope here (as of #1148; superseded per-consumer as migrations land — see Status above)

- **Wiring any consumer.** At #1148 ship time, Family A, Family B, and the rotation watcher were all untouched. Family B (#1150) and the rotation watcher (#1151) have since migrated; Family A's implementation is pending on `feature/1149`.
- **The `availabilityReporter`/`probeUsable` dispatch helper.** The no-lsof→mtime dispatch *decision* both families make locally is not extracted — it's part of each family's divergent dispatch order, not one of the core's owned mechanics. A later consolidation ticket may DRY it once both adapters sit on this core.
- **`context.Context`.** The core is context-free; the `func(ctx) (path, X, err)` closure shape belongs to the adapters (`supervisor.Config.ResolveTranscript`, the turn-stream resolver) that wrap these primitives.

## Testing

`internal/transcript/transcript_test.go` — table-driven, `t.TempDir()`, a local fake `Probe` with a scriptable return and a recorded-pids/call-count field (mirrors both families' existing test idioms). Covers every #838 scenario: probe-preferred over a newer foreign sibling, `pid <= 0` (probe not called), empty/errored probe, all `GuardProbedPath` rejections (outside dir, non-UUID stem) plus vanished-between-probe-and-stat, `StatByID` hit/miss/invalid-stem, `Newest` mtime pick/tie-break/empty/missing dir, `ValidStem`, and `GuardProbedPath` accept (including an in-dir symlink). `go test -race ./internal/transcript/...`, `go vet ./...`, `staticcheck ./...` all green; because the package has zero importers, nothing outside it can regress.

## Security

Architect self-review verdict: **PASS** (security-sensitive label, `docs/specs/architecture/1148-*.md` § Security review). No MUST FIX findings. Code-review verdict: **PASS** (PR #1155) — confirmed guard fidelity against both families' inline guards, leaf/cycle-breaker boundary, and zero-consumer scope. Two non-gating NITs for the sibling migration to expect: `Newest` captures `Size` during the scan rather than re-statting the winner (fewer syscalls, but a file vanishing between scan and use now yields the scan-time size instead of a surfaced error); `StatByID` returns an error on an invalid stem where Family A's current inline path falls through to the probe instead — the Family A adapter must preserve that fall-through rather than mapping the error straight to nil-empty.

## Related

- **Ticket:** [#1148](https://github.com/pyrycode/pyrycode/issues/1148), split from #973.
- **Spec:** [`docs/specs/architecture/1148-*.md`](../../specs/architecture/) — full design, error-handling table, security review.
- **Per-ticket note:** [codebase/1148.md](../codebase/1148.md).
- **Family B migration:** [#1150](https://github.com/pyrycode/pyrycode/issues/1150) — [codebase/1150.md](../codebase/1150.md), PR #1158.
- **Rotation watcher migration:** [#1151](https://github.com/pyrycode/pyrycode/issues/1151) — [codebase/1151.md](../codebase/1151.md), PR #1159.
- **Prior art:** [codebase/838.md](../codebase/838.md) — the probe-prefer resolver and AC4 guard this package generalises; [sessions-package.md](sessions-package.md) — Family A's current home; [rotation-watcher.md](rotation-watcher.md) — `rotation.Probe`, the interface `transcript.Probe` mirrors structurally.
