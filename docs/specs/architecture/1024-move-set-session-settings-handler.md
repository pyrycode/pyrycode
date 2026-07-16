# Spec: move `set_session_settings` handler out of `v2session.go` (#1024)

**Size:** S — pure mechanical move, zero new logic, one new file.
**Security-sensitive:** No (unlabeled). A pure same-package relocation introduces no new
design to audit — the untrusted-model/effort validation and argv-injection defense
(#845) move verbatim, byte-for-byte; their contract is unchanged. Design-not-lineage:
the security *lineage* of this code does not make the *move* a security change.

## Context

`internal/relay/v2session.go` was the largest readability liability in the 2026-07-15
full-repo review (3744 lines then). The earlier #964 slices have merged and shrunk it to
~2307 lines: #1021 (initiator handshake → `v2session_handshake.go`), #1022 (re-key
machinery → `v2session_rekey.go`), #1023 (modal + queue reconcile → `v2session_modal.go`).

This is the **4th #964 slice**. It carves the `set_session_settings` verb handler plus its
model/effort validators and static reply-message constants into a named file within the
same `package relay`. Chained after #1023 (merged `b20eb3f`); branch from `main`.

**Pure move**: no behaviour change, no exported-API change, no call-site change. Every
doc comment moves verbatim with its declaration.

## Files to read first

- `internal/relay/v2session.go:1434-1653` — **the block to move**, in place. The
  `msgSettings*` `const` group, `handleSetSessionSettings`, `settingsReplyError`,
  `validModel`, `validEffort`. Bounded above (1432) by `bundleInFlight` and below (1655)
  by `SetReplaySource` — one clean cut, nothing settings-unrelated interleaved.
- `internal/relay/v2session.go:1-20` — the current `package relay` clause + full import
  block of the source file (context for which imports the moved code depends on).
- `internal/relay/v2session.go:1045` — `m.handleSetSessionSettings(ctx, s, probeEnv)`,
  the sole external call site (the interceptor inside `dispatchAppFrame`, before
  `dispatch.Route`). **Stays put, no edit** — resolves same-package to the moved method.
- `internal/relay/v2session.go:359` — a comment mentioning `handleSetSessionSettings`.
  **Stays put, no edit** (comment prose, not a symbol reference).
- `internal/relay/v2session_modal.go:1-19` — the file-header convention to mirror:
  `package relay`, a minimal import block, then a `//` file-purpose comment naming the
  slice, the pure-move disclaimer, and what stays in `v2session.go`.
- `internal/relay/v2session_rekey.go:1-19` — same convention, second exemplar.
- `internal/relay/v2session_settings_test.go` — the **already-existing** test file for this
  handler (references `handleSetSessionSettings`, `validModel`, `validEffort`). It is in
  `package relay`, so the move is transparent to it. **Must NOT be touched** (AC #3).

## Design

### What moves — exact manifest

Relocate this contiguous span from `v2session.go` (currently lines 1434-1653, inclusive)
into the new file `internal/relay/v2session_settings.go`, in the same order, with every
doc comment intact:

1. The `const (…)` block `msgSettingsMalformed` / `msgSettingsNotFound` /
   `msgSettingsUnavailable` and its leading doc comment (`// Static reply messages for
   set_session_settings failures (#845)…`).
2. `func (m *V2SessionManager) handleSetSessionSettings(...)` + its doc comment.
3. `func (m *V2SessionManager) settingsReplyError(...)` + its doc comment.
4. `func validModel(m string) bool` + its doc comment.
5. `func validEffort(e string) bool` + its doc comment.

The cut anchor: from the `// Static reply messages for set_session_settings failures
(#845).` comment through the closing brace of `validEffort`. All five declarations are
consumed only from within this span or from the two stay-put references below — grep
across `package relay` confirms no other production file names any of these idents.

### What stays in `v2session.go` (no edit)

- **`:1045`** — `m.handleSetSessionSettings(ctx, s, probeEnv)`. Same-package method call;
  resolves to the moved definition automatically. Do not touch.
- **`:359`** — the comment referencing `handleSetSessionSettings`. Prose, not a symbol.
- **The seam types** — `SettingsUpdate` struct + `SettingsUpdater` interface (~330-356) and
  the `V2SessionConfig.SettingsUpdater` field (~615). These are shared seam types consumed
  by the moved handler but owned by a later seam-interfaces slice. The moved code
  references them by unqualified name; the reference resolves within the one package
  either way. **Leave them in `v2session.go`.**

### The new file — `internal/relay/v2session_settings.go`

```
package relay

import (
    "context"
    "encoding/json"
    "errors"
    "time"

    "github.com/pyrycode/pyrycode/internal/protocol"
)

// <file-purpose comment — see below>

// <the five moved declarations, verbatim>
```

The moved block's dependency surface: `context` (ctx params), `encoding/json`
(`json.Unmarshal` / `json.Marshal`), `errors` (`errors.Is(err, ErrSessionUnknown)`),
`time` (`time.Now().UTC()`), and `internal/protocol` (`SetSessionSettingsPayload`,
`Envelope`, `ErrorPayload`, `SessionSettingsUpdatedPayload`, the `Code*`/`Type*`
constants). Everything else the handler touches — `V2SessionManager`, `V2Session`,
`SettingsUpdate`, `SettingsUpdater`, `ErrSessionUnknown`, `m.forwardEnvelope`,
`m.cfg.Logger` — is package-local and needs no import.

**File-purpose comment** (place after the imports, mirroring the sibling slices' header
style):

> This file holds the `set_session_settings` verb handler plus its model/effort
> validators — the interactive settings-update path — carved out of `v2session.go`
> (#1024). Pure move: same package, no behaviour change. The `SettingsUpdate` struct,
> `SettingsUpdater` seam interface, and its `V2SessionConfig.SettingsUpdater` field stay
> in `v2session.go`; the seam interfaces are their own later slice. #1021/#1022/#1023
> carved out the handshake, re-key machinery, and modal+queue handlers before this slice.

### Imports — compiler-forced, per AC #2

Do **not** hand-audit which imports to keep in `v2session.go`. Move the block, add the five
imports above to the new file, then run `go build ./internal/relay/` and let the compiler
drive any import edit. Expected outcome: `v2session.go`'s import block is **unchanged** —
`context`, `encoding/json`, `errors`, `time`, and `protocol` are each used by many other
handlers in the 2300-line file, so none goes unused. But if the compiler reports an unused
import in either file, that removal is exactly the "compiler-forced import edits in each
file are permitted" carve-out in AC #2. Follow the compiler, do not pre-guess.

## Concurrency model

Unchanged. `handleSetSessionSettings` runs on the manager's single Run dispatch goroutine
(the same-goroutine invariant that makes the `s.interactive` read lock-free). Moving the
function to another file in the same package does not alter goroutine ownership, lock
discipline, or the single-owner invariant. `settingsReplyError`, `validModel`, and
`validEffort` are pure/logger-only helpers with no concurrency surface of their own.

## Error handling

Unchanged. The handler's failure ladder — capability gate → decode → validate → nil-seam
guard → persist+reply — and its reply/log posture (fixed constant messages, never echo the
wrapped err or any payload byte, content-free logging of `conn_id`+`session_id` only) move
verbatim. No error path is added, removed, or rewired.

## Testing strategy

- `v2session_settings_test.go` already exercises this handler (seam-fires-before-Route
  path, `validModel`/`validEffort` table tests). It is same-package and must remain
  **byte-for-byte unchanged** — the move is invisible to it (AC #3). Its continued passing
  is the primary proof the move preserved behaviour.
- Build incrementally: `go build ./internal/relay/` after the cut, before the full gate.
- Final gate: `make check` green (vet, race test, staticcheck, substrate-guard, e2e) —
  AC #4. The e2e leg is CI-doubling but required by the AC.

## Open questions

None. The cut boundary, the stay-put references, and the import set are all determined.

## Acceptance criteria mapping

- **AC #1** (decls relocated, gone from `v2session.go`): the five-declaration manifest above.
- **AC #2** (no rename/add/remove of exported idents; only mechanically-forced edits): all
  moved idents are unexported and move verbatim; the compiler-forced import rule covers the
  only permitted incidental edit.
- **AC #3** (only `v2session.go` + the one new file change): the `:1045` call and `:359`
  comment stay unedited; `v2session_settings_test.go` stays untouched; no other file moves.
- **AC #4** (`make check` green): final gate.
