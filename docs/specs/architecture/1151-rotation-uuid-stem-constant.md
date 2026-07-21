# Spec: consume the shared UUID-stem constant in the rotation watcher (#1151)

**Size:** XS · **Security-sensitive:** no (local-fs CREATE watcher; no untrusted/network input crosses this seam — unlike relay siblings #1149/#1150) · **Split from:** #973

## Files to read first

- `internal/sessions/rotation/watcher.go:10-19` — the `regexp` import and the local `uuidStemPattern` var + its two-line comment; both are deleted by this ticket.
- `internal/sessions/rotation/watcher.go:144-152` — `handleCreate`: strips the `.jsonl` suffix (line 146, via `strings.HasSuffix`) then matches the bare `stem` (line 150). Line 150 is the one call site to change; line 146 is why `strings` stays.
- `internal/transcript/transcript.go:48-50` — `func ValidStem(stem string) bool`; wraps the identical `uuidStemPattern.MatchString(stem)`, so the swap is behaviour-preserving by construction.
- `internal/transcript/transcript.go:1-25` — package doc; confirms the leaf is stdlib-only and names `internal/sessions/rotation` as an intended consumer → the new `rotation → transcript` edge introduces no cycle.
- `internal/sessions/rotation/watcher_test.go:200-224` — `TestWatcher_SkipsMalformedUUID` (writes `not-a-uuid.jsonl`, asserts `OnRotate` is *not* called): the invariant test for the matcher branch. Must stay green.
- `internal/sessions/rotation/watcher_test.go:171-198` — `TestWatcher_SkipsNonJSONL`: pins the `.jsonl`-suffix branch that keeps the `strings` import. Must stay green.

## Context

The canonical UUID-stem regexp `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$` was triplicated across three production files. `internal/transcript` (#1148, merged) now owns it as `transcript.ValidStem`. The delivery-confirm (Family A, #1149) and outbound-stream (Family B, #1150) families have migrated and merged. This ticket migrates the **third and last** copy — `internal/sessions/rotation/watcher.go:19` (`uuidStemPattern`) — so the constant has a single source of truth. Pure mechanical swap, no behaviour change.

## Design

Three edits in `internal/sessions/rotation/watcher.go`, all in one file:

1. **Delete** the `uuidStemPattern` var and its two comment lines (current lines 17-19).
2. **Delete** the `regexp` import (current line 10). Add the import `github.com/pyrycode/pyrycode/internal/transcript` (goimports/gofmt places it in the existing internal-import group).
3. **Swap the call site** at current line 150:
   - `if !uuidStemPattern.MatchString(stem) {` → `if !transcript.ValidStem(stem) {`

`stem` is already the bare stem (the `.jsonl` suffix is stripped one line earlier at line 149), and `ValidStem` takes a bare stem — so the swap is 1:1 with no argument reshaping.

**Scope boundary — do NOT touch:**
- The `strings` import (line 11) and `strings.HasSuffix(base, ".jsonl")` at line 146 stay. This file sheds **only** `regexp`. The sibling recipes (#1149/#1150) shed both `regexp` and `strings`; that does not apply here — do not copy them wholesale.
- No signatures change. `handleCreate`, `New`, `Run`, `Config`, `Watcher`, `SessionRef` are all untouched apart from the single expression on line 150.

**Import-cycle check (already verified):** `internal/transcript` is a stdlib-only leaf (imports neither `internal/sessions` nor `internal/sessions/rotation`); `rotation → transcript` is a new forward edge only. No cycle. AC#2 satisfied by construction.

## Concurrency model

Unchanged. `ValidStem` is a pure, stateless function (a compiled-regexp `MatchString`, safe for concurrent use). It is called from the same single event-loop goroutine that called `uuidStemPattern.MatchString`. No new goroutines, channels, or shared state.

## Error handling

Unchanged. `ValidStem` returns a `bool`, exactly as `MatchString` did; the surrounding early-return-on-false control flow at line 150 is preserved verbatim. No new error paths.

## Testing strategy

No new tests. Behaviour is byte-identical (`ValidStem` wraps the same regexp), so the guarantee is "existing tests stay green":

- `TestWatcher_SkipsMalformedUUID` — the direct invariant for the swapped branch (`not-a-uuid.jsonl` → no rotation).
- `TestWatcher_SkipsNonJSONL` — confirms the retained `.jsonl`/`strings` branch still rejects non-jsonl CREATEs.

Verify with `make check` (covers `go build`, `go vet`, `staticcheck`, `go test -race ./...`). The removed `regexp` import is compile-enforced: if any stray `regexp` reference remains, the build fails; if the import lingers unused, `go vet`/build fails. That is the deterministic safety net for AC#1.

## Open questions

None. The seam, the target symbol, and the import-cycle safety are all verified against merged code.
