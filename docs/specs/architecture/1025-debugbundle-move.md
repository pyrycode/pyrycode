# Spec #1025 — Move debug-bundle streaming out of `v2session.go`

**Ticket:** [#1025](https://github.com/pyrycode/pyrycode/issues/1025) — refactor(relay): move debug-bundle streaming out of v2session.go
**Size:** S — pure mechanical, same-package move. No behavior change, no exported-API change, no call-site change.
**Security-sensitive:** No. This is a code motion within `package relay`; there is no new design surface to audit. (The moved code *handles* the highest-value secret surface in the system — the plaintext debug bundle — but the move changes zero bytes of that logic, so there is nothing new to threat-model. Lineage ≠ design: see the security-sensitive-label note.)

The 5th and final `v2session.go` carve-out from #964, after #1021 (handshake), #1022 (rekey), #1023 (modal+queue), #1024 (settings) landed. The file is 2086 lines on `main`; this ticket removes the ~160-line debug-bundle block.

## Files to read first

- `internal/relay/v2session.go:1273-1432` — **the exact block to move.** Four contiguous declarations, no interleaved non-debug-bundle code:
  - `1273-1279` — `const msgDebugBundleUnavailable` (+ doc comment)
  - `1281-1355` — `func (m *V2SessionManager) handleDebugBundleRequest(...)` (+ doc comment)
  - `1357-1389` — `func (m *V2SessionManager) debugBundleReplyError(...)` (+ doc comment)
  - `1391-1432` — `func (m *V2SessionManager) bundleInFlight(...)` (+ doc comment)
- `internal/relay/v2session.go:1-21` — import block. Confirms `context`, `encoding/json`, `time`, `internal/protocol` are all imported; the new file needs exactly this subset (see § Imports).
- `internal/relay/v2session.go:1041` — `case protocol.TypeRequestDebugBundle:` in `dispatchAppFrame`. **Stays put** — it is the caller (`m.handleDebugBundleRequest(...)`); a same-package method move is transparent to it.
- `internal/relay/v2session.go:593-607` — `DebugBundler func() (archive []byte, err error)` field on the config struct. **Stays put** — same-package seam.
- `internal/relay/v2bundlestream.go` — home of `StreamBundle` (called by `handleDebugBundleRequest`). **Not touched** by this ticket; already a separate file.
- `internal/relay/v2session_debugbundle_test.go` — the debug-bundle test file. **Already exists, same package, needs no edit** — the move is transparent to it (AC #3).
- `internal/relay/v2session_rekey.go` / `v2session_modal.go` — sibling carve-outs from prior #964 slices. Mirror their file header, package clause, and import-grouping style for the new file so it reads as a native member of the package.

## Context

`v2session.go` was the largest readability liability in the 2026-07-15 full-repo review. The #964 program slices its cohesive handler groups into `v2session_<concern>.go` files within the same `package relay`, each a pure move: no behavior change, no API change, no call-site change. This ticket carves out `request_debug_bundle` handling — the request handler, its deterministic error-reply helper, its per-conn in-flight gate, and the shared error-message constant.

## Design

**Create `internal/relay/v2session_debugbundle.go`** in `package relay` and relocate the four declarations verbatim from `v2session.go:1273-1432`, preserving their doc comments byte-for-byte and their relative order. Nothing about the code changes except its file. `V2SessionManager`, `V2Session`, `V2SessionConfig.DebugBundler`, `m.pushMu`, `m.queues`, `m.forwardEnvelope`, and `StreamBundle` all remain same-package symbols, so every reference resolves unchanged.

**Stays in `v2session.go`:**
- the `case protocol.TypeRequestDebugBundle:` dispatch arm in `dispatchAppFrame` (`:1041`) — the caller;
- the `DebugBundler` config field (`:593-607`) — the injection seam.

Both are same-package; the move is invisible to them. Do not touch them.

### Imports

The new file's import block is exactly:

```go
import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)
```

Rationale (traced from the moved code): `context.Context` params; `json.Marshal` in `debugBundleReplyError`; `time.Now().UTC()` in `debugBundleReplyError`; `protocol.{Envelope, ErrorPayload, CodeServerBinaryOffline, TypeError, TypeDebugBundleChunk, TypeDebugBundleDone}`. No other package is referenced by the moved block — `sync`/`slog` types are never named (only field accesses `m.pushMu`, `m.cfg.Logger`), and `V2Session`/`V2SessionManager` are same-package.

**Import fate in `v2session.go` (the pure-move crux):** all four of `context`, `encoding/json`, `time`, `protocol` remain heavily used by the ~1900 lines that stay, so **no import is removed from `v2session.go`**. This is the inverse of #1022, where `bytes` was used *only* by the moved code and the compiler forced its removal. Do not assume — verify by trusting the compiler: after the move, `go build ./internal/relay/` reports any `imported and not used` error, and `gofmt`/`goimports` settles placement. If the build is green with `v2session.go`'s import block unchanged, the invariant held. Grep the *remaining* `v2session.go` for `json.`, `time.`, `context.`, `protocol.` in **code** (not comments) if you want a belt-and-suspenders check before building.

## Concurrency model

Unchanged. `handleDebugBundleRequest` and `debugBundleReplyError` run on the manager's single `Run` dispatch goroutine (intercepted in `dispatchAppFrame` before `dispatch.Route`). `bundleInFlight` takes `m.pushMu` alone for an O(len(items)) read and releases it before the caller replies, preserving the leaf-lock invariant ("never held across an Encrypt/`m.send`/channel op; always taken alone"). Moving these methods to another file in the same package changes none of this — method-set membership is per-type, not per-file.

## Error handling

Unchanged. Every branch of `handleDebugBundleRequest` either streams one bundle or sends exactly one deterministic `TypeError` reply (`server.binary_offline` + `msgDebugBundleUnavailable` + retryable), then returns. The content-free logging posture (event + `conn_id` + byte count on success; failure EVENT only, never the archive or wrapped error) moves with the code untouched.

## Testing strategy

- **No new tests.** `v2session_debugbundle_test.go` already exists in `package relay` and exercises these handlers by name; a same-package move is transparent to it (AC #3). Do not edit it.
- **Verification is the build + full gate.** Work incrementally: `go build ./internal/relay/` after the cut/paste to confirm both files compile and no import went stale, then `make check` once at the end (vet, race test, staticcheck, substrate-guard, e2e) per AC #4.
- **Diff shape self-check** — a clean pure move produces a `v2session.go` diff that is *deletions only* in the 1273-1432 region (no additions, no edits elsewhere) and a new-file diff that is *additions only*. Any stray edit outside those two shapes signals an accidental logic change (AC #2). One collapsed blank line where the block was removed is expected and fine.

## Open questions

None. Placement is fully determined: the block is contiguous, the import set is closed, and the callers/seam that stay are identified by line. If `make check`'s e2e leg flakes, cross-check against the known-flake set (realclaude SIGTERM, wssclient `-race`) before treating red as a real regression — this diff moves no code across the wire.
