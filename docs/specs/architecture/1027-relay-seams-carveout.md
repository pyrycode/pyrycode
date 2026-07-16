# Spec #1027 — move seam interfaces + `V2SessionConfig` out of `v2session.go`

**Size:** S (pure mechanical move; 2 files; zero test changes; zero call-site changes).
**Security-sensitive:** No (label absent). Pure same-package move — no behaviour change, no new
trust boundary, no design to audit. The `V2SessionConfig.StaticPriv` SECURITY comment moves
verbatim with the struct; it introduces no new surface.

This is the **final** #964 carve-out. Snapshot/replay (#1026, PR #1048) already merged; `main`'s
`v2session.go` is the post-#1026 state (1558 lines). Branch from `main`.

## Files to read first

- `internal/relay/v2session.go:287-652` — **the exact contiguous block to move.** Starts at the
  `ScreenSnapshotter` doc comment (287), ends at the closing `}` of `V2SessionConfig` (652). Every
  in-scope decl lives inside this single span; nothing else does.
- `internal/relay/v2session.go:1-21` — import block. Line 9 (`"log/slog"`) is the **one import that
  must be removed** after the cut (see § The crux).
- `internal/relay/v2session.go:80,90` — `ErrSessionNotOpen` / `ErrTransportDown` use `errors.New`;
  proof `errors` stays used in the residual (do **not** remove it).
- `internal/relay/v2session_replay.go:1-22` — the most recent sibling carve-out (#1026). Mirror its
  shape exactly: `package relay` → grouped import block → a leading file-level comment naming what was
  carved out and citing this ticket → the moved decls.
- `internal/relay/v2session_settings.go:1-10` — a sibling whose import group already includes
  `errors` + `protocol`; closest template for the new file's import set.
- `CODING-STYLE.md` — "define interfaces where they are consumed" (the convention the moved seam
  doc comments already cite; no change, just context).

## Context

`v2session.go` was flagged in the 2026-07-15 full-repo review as the largest readability liability.
The #964 split has whittled it down slice by slice; this is the last one. Lifting the six seam
interfaces, the `SettingsUpdate` value type, the `ErrSessionUnknown` sentinel, and the ~260-line
`V2SessionConfig` struct into their own file leaves `v2session.go` holding only the manager core
(the `V2SessionManager` struct, `NewV2SessionManager`, `Run`, `handleWake`, `handleFrame`, the
app-frame router, outbound `send`/drain, and the connection registry).

Pure move: no exported identifier renamed/added/removed, no logic change, no call-site change.

## Design

### The move — one contiguous cut

Relocate `v2session.go:287-652` verbatim into a new file **`internal/relay/v2session_seams.go`**
(same `package relay`). The block, in source order, is:

| Decl | Kind | Current lines |
|---|---|---|
| `ScreenSnapshotter` | interface | 287–294 |
| `Interrupter` | interface | 296–304 |
| `SessionStarter` | interface | 306–316 |
| `QueueRemover` | interface | 318–328 |
| `SettingsUpdate` | struct (value type) | 330–341 |
| `SettingsUpdater` | interface | 343–354 |
| `ErrSessionUnknown` | `var` sentinel | 356–361 |
| `ModalResolver` | interface | 363–393 |
| `V2SessionConfig` | struct | 395–652 |

These nine decls are **already adjacent** — 287–652 is a single span with no interleaved
manager-core code. The line immediately before (285, `func (s *V2Session) State()`) and the line
immediately after (654, the `V2SessionManager` doc comment) both stay in `v2session.go`.

Move the doc comments with their decls (each block's comment starts the range above). Preserve
source order; no re-sorting.

**Out of scope — do not touch:** `ModalDismissal` already lives in `internal/relay/v2session_modal.go`
(relocated by an earlier slice). The `ModalResolver` methods reference it; that reference resolves
within `package relay` and needs no import.

### New file `v2session_seams.go` — import set

The moved block references exactly six packages (verified by scanning 287–652). Import precisely
these, gofmt-grouped:

```
import (
	"errors"        // ErrSessionUnknown = errors.New(...)
	"log/slog"      // V2SessionConfig.Logger *slog.Logger
	"time"          // V2SessionConfig.RekeyInterval/RekeyReplyTimeout/RekeyRetryInterval

	"github.com/pyrycode/pyrycode/internal/devices"   // ModalResolver *devices.Device; V2SessionConfig.Devices *devices.Registry
	"github.com/pyrycode/pyrycode/internal/dispatch"  // V2SessionConfig.Handlers map[string]dispatch.Handler
	"github.com/pyrycode/pyrycode/internal/protocol"  // Frames/Outbound RoutingEnvelope; OutstandingModals/OutstandingQueues payloads
)
```

No `context`, `encoding/*`, `fmt`, `sync`, `websocket`, `control`, `eventring`, or `noise` — the
moved block references none of them. Let `goimports`/`make check` be the final arbiter, but the set
above is the target.

### The crux — `v2session.go` import hygiene (the one non-mechanical step)

After the cut, **`"log/slog"` becomes an unused import in `v2session.go` and must be removed**, or
`go build ./internal/relay/` fails with *"log/slog imported and not used."*

Reason: `slog` is referenced in `v2session.go` at exactly two places — the import (line 9) and
`Logger *slog.Logger` (line 504, **inside** the moved block). The manager core logs through the
stored `s.cfg.Logger` field, never naming the `slog` package. So once the block leaves, `slog` has
no residual user.

**Every other import in `v2session.go` stays** — each is still used by the residual manager core:
`errors` (80, 90), `time` (19 sites), `protocol` (32 sites), `dispatch` (11 sites), `devices`
(1 site), plus `context`, `encoding/base64`, `encoding/json`, `fmt`, `sync`, `websocket`, `control`,
`eventring`, `noise`. Do not remove any of these. `log/slog` is the sole import that moves out.

### Suggested mechanics (order matters to avoid a line-shift trap)

1. **Write** `internal/relay/v2session_seams.go`: package clause, the import block above, a leading
   file-level comment (mirror `v2session_replay.go:12-22` — one paragraph naming what was carved out
   and citing #964/#1027), then the nine decls in source order.
2. **Delete** the block from `v2session.go` by line range first (`sed '287,652d'` or an equivalent
   range edit) — while line numbers still match this spec.
3. **Remove** `"log/slog"` from `v2session.go`'s import block by *string match* (delete the
   `\t"log/slog"` line), which is line-number-agnostic so step 2's shift is irrelevant.
4. `gofmt` both files; `go build ./internal/relay/` should now be green.

(#1026 used the same "write new file, then delete source span" technique; PR #1048.)

## Concurrency model

Unchanged. This is a lexical relocation of type/var declarations; it spawns nothing and touches no
goroutine, channel, or lock. The concurrency contracts documented in the moved comments (e.g.
`Interrupter.SendEsc` "safe to call from any goroutine", `Handlers` "run on the manager's single
dispatch goroutine") move verbatim and remain accurate.

## Error handling

Unchanged. `ErrSessionUnknown` keeps its identity and message (`"relay: session unknown"`); it is the
same package-level sentinel, now declared in a different file of the same package. The cmd/pyry
adapter's `sessions.ErrSessionNotFound → relay.ErrSessionUnknown` mapping (`cmd/pyry/main.go:1018`)
is a package-qualified reference and needs no edit.

## Testing strategy

No new tests. The move is behaviour-preserving and confined to one package:

- All nine decls are exported and referenced cross-package only via the `relay.` qualifier (21 sites
  across `cmd/pyry` and `internal/e2e`, e.g. `relay.V2SessionConfig{…}`, `relay.SettingsUpdater`,
  `relay.ErrSessionUnknown`). A same-package file move leaves every qualified name unchanged, so no
  consumer or test file changes.
- In-package tests reference the types unqualified; they too are unaffected — the decls remain in
  `package relay`.

Verification:

- `go build ./internal/relay/` incrementally — the fast signal that the `log/slog` removal is
  correct and no other import broke.
- `make check` once at the end (vet, race test, staticcheck, substrate-guard, e2e) — must be green.
- Sanity grep after the move: each of the nine symbols has exactly one declaration site, now in
  `v2session_seams.go`, and none remain in `v2session.go`.

## Acceptance criteria (from ticket, unchanged)

- [ ] The nine decls are relocated from `v2session.go` into new `internal/relay/v2session_seams.go`
      (`package relay`); they no longer appear in `v2session.go`.
- [ ] No exported identifier renamed, added, or removed; no logic change beyond the mechanical move
      (which here means: removing the now-unused `log/slog` import from `v2session.go`).
- [ ] Only `v2session.go` and the one new file change; no other file in the tree is touched.
- [ ] `make check` is green.

## Open questions

None. The block is a single clean span, the import delta is fully determined (one import out:
`log/slog`; six in on the new file), and every consumer reference is package-qualified so the move is
invisible outside `package relay`.

The ticket notes that if the residual `v2session.go` still exceeds the ~1000-line #964 target after
this lands (outbound + connection-registry helpers), that is a **cheap standalone follow-up** — do
not expand this slice to chase it.
