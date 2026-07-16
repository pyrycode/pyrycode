# Spec — #1026 move snapshot/replay/resync out of v2session.go

**Size:** S (pure mechanical move; not security-sensitive — no label, no design to audit, no new trust boundary)
**Slice:** 6th `#964` carve-out, after #1021 (handshake), #1022 (rekey), #1023 (modal+queue), #1024 (settings), #1025 (debugbundle).
**Branch:** from `main`. No blockers — `feature/449` overlap on `v2session.go` is a stale CLOSED-orphan (closed 2026-05-17, no open PR, main 1166 commits ahead, its re-key work already on main via #1022); not a live merge target.

## Files to read first

- `internal/relay/v2session.go:1121-1409` — **Block A to move.** The `msgSnapshot*` const pair (1121-1127), `handleRequestSnapshot` (1129-1237), `snapshotReplyError` (1239-1271), `SetReplaySource` (1273-1294), `replayMissed` (1296-1370), `emitResync` (1372-1409). Six contiguous declarations. Extract what imports they reference.
- `internal/relay/v2session.go:1695-1770` — **Block B to move.** `drainReplayOnce` (doc at 1695, func 1713-1770). Separated from Block A by seven functions that STAY (`sealError`, `marshalInnerFrameV2`, `closeWith`, `send`, `Push`, `transportDown`, `drainOnce`).
- `internal/relay/v2session.go:113-122` — `snapshotReq` type + its doc. **This STAYS** (see CRUX below). Read the doc: "enqueued by ActiveConns and dequeued by Run" — it is the ActiveConns-enumeration request, not the screen-snapshot path.
- `internal/relay/v2session.go:1-20` — current import block (15 imports). Read to confirm none becomes unused after the move (they don't — see § Imports).
- `internal/relay/v2session.go:278,754` — `replayQueue []eventring.Event` and `replayRing *eventring.Ring` struct fields. These keep `eventring` live in `v2session.go` after the move (the reason no import is forced out).
- `internal/relay/v2session_debugbundle.go:1-19` — **template for the new file**: package clause, grouped imports, file-level doc comment describing what was carved out + the #964 slice lineage. Mirror this exactly.
- `internal/relay/v2session_rekey.go:1-16` — second example of the same header pattern (`bytes` import present there because rekey uses it; your import set differs — let the compiler drive it).
- `internal/relay/v2session_replay_test.go` — **already exists**, tests `replayMissed`/`emitResync`/`drainReplayOnce` by package-internal name. Do NOT touch it; it keeps compiling because the moved decls stay in `package relay`.
- `cmd/pyry/interactive_turn_stream_v2.go:109` — `mgr.SetReplaySource(...)` call site. Do NOT touch. It resolves per-type (`*V2SessionManager`), not per-file; the move leaves it valid.

## Context

`internal/relay/v2session.go` is 1925 lines (was 3744 when flagged as the largest readability liability in the 2026-07-15 review). This is the 6th and final-planned carve-out slice of #964. It lifts two reconnect-recovery concerns out of the core file into a named sibling within `package relay`:

1. **Screen-snapshot request path** — the `TypeRequestSnapshot` inbound-control handler (`handleRequestSnapshot`), its deterministic error-reply helper (`snapshotReplyError`), and the two static error-message constants.
2. **Missed-event replay / resync path** — `SetReplaySource` (the late-bound wiring setter), `replayMissed` (mid-turn-reconnect classifier), `emitResync` (gap→full-reload marker), `drainReplayOnce` (per-Run-pass paced replay pump).

Pure move: no behaviour change, no exported-API change, no logic change beyond what the compiler mechanically forces. Zero call-site edits.

## Design

### New file: `internal/relay/v2session_replay.go`

`package relay`. Holds the two blocks below, in source order, preceded by a file-level doc comment matching the `v2session_debugbundle.go` template (state: holds screen-snapshot request path + missed-event replay/resync path; carved from v2session.go (#1026); pure move; 6th #964 slice; note what stays — the `V2Session`/`V2SessionManager` structs, the `Run` loop, the `TypeRequestSnapshot` dispatch case in `dispatchAppFrame`, the `snapshotReq`/`ActiveConns` machinery, and the shared `forwardEnvelope`/`sealError` seal path).

**Decls to move (verbatim, comments and alignment preserved):**

| Decl | Current lines | Kind |
|---|---|---|
| `msgSnapshotConvNotFound`, `msgSnapshotOffline` const block | 1121-1127 | const |
| `handleRequestSnapshot` | 1129-1237 | method |
| `snapshotReplyError` | 1239-1271 | method |
| `SetReplaySource` | 1273-1294 | method (exported) |
| `replayMissed` | 1296-1370 | method |
| `emitResync` | 1372-1409 | method |
| `drainReplayOnce` | 1695-1770 | method |

Move them as two cuts (Block A = 1121-1409, Block B = 1695-1770). Preserve the doc comments attached to each decl and their internal comment alignment exactly — do not reflow (gofmt 1.26 reformats aligned comments but CI is not gated on it; a verbatim move avoids any diff noise).

### CRUX — `snapshotReq` STAYS in `v2session.go` (deliberate deviation from the ticket's Technical Notes list)

The ticket's Technical Notes enumerate `snapshotReq` in the move scope, but the same sentence grants "architect finalizes exact placement so the build stays green," and the Context scopes the concern to "the screen-snapshot request path plus the missed-event replay/resync path." **`snapshotReq` is neither.** It is a name-collision: two unrelated meanings of "snapshot" live in this file.

- `snapshotReq` (`:120`) = the **ActiveConns snapshot** request — a `chan []ActiveConn` reply envelope enqueued onto `m.snapshot` to enumerate open connections. Verified consumers (all four STAY in `v2session.go`): the `snapshot chan snapshotReq` struct field (`:734`), `NewV2SessionManager` (`:790`), `ActiveConns` (`:1865`), and `Run`'s `case m.snapshot` select arm. **Zero consumers move.** Neither `handleRequestSnapshot` nor `snapshotReplyError` references `snapshotReq`.
- The **screen snapshot** (`handleRequestSnapshot`) renders the live claude screen; it uses `RequestSnapshotPayload`/`ScreenSnapshotPayload` and `forwardEnvelope`, never the `snapshotReq` channel type.

Moving `snapshotReq` into a `_replay.go` file — away from every one of its users, all of which remain — would *reduce* cohesion, the exact opposite of the ticket's readability goal. So `snapshotReq` and its doc comment (`:113-122`) stay untouched. This mirrors the #1022 "interleaved neighbour stays" ruling (`armIdleTimer`/`wakeKind`/struct fields whose consumers stayed). **The ACs below are authoritative and enumerate exactly what moves; `snapshotReq` is intentionally excluded.**

### Imports

**New file** — expected set (let `go build ./internal/relay/` drive the exact list):
```
context            // handler ctx params
encoding/json      // Marshal/Unmarshal of snapshot + resync payloads
time               // time.Now().UTC() in envelope TS
github.com/pyrycode/pyrycode/internal/eventring   // SetReplaySource *eventring.Ring param; drainReplayOnce eventring.Event{}
github.com/pyrycode/pyrycode/internal/protocol    // Envelope, RequestSnapshotPayload, ScreenSnapshotPayload, ErrorPayload, Type*, Code*
```
No `log/slog` (logging is via `m.cfg.Logger` method calls, not the `slog` qualifier). No `sync` (`m.pushMu.Lock()` is a field-method call). No `errors`/`fmt`/`websocket`/`control`/`devices`/`dispatch`/`noise`/`base64`.

**`v2session.go` — import block UNCHANGED.** This is the OPPOSITE of the #1022 `bytes` case: no import is exclusively used by the moved code. In particular `eventring` stays live via the `replayQueue []eventring.Event` (`:278`) and `replayRing *eventring.Ring` (`:754`) struct fields. Every other candidate (`context`, `encoding/json`, `time`, `protocol`) is used by many staying functions. **Do not remove any import from `v2session.go`.** If `go build` reports an unused import there after the move, that is a signal something was over-moved — investigate, don't delete the import.

### Data flow / concurrency / error handling — unchanged

Pure move. Every moved function keeps running on the manager's single `Run` dispatch goroutine exactly as before; `Run` still dispatches to them by name (same package). The `pushMu` guard in `SetReplaySource`/`replayMissed`, the `replayCh`/`drainCh` re-signalling in `drainReplayOnce`, the InReplyTo-correlated inline-seal via `forwardEnvelope` in the snapshot path — all identical. No goroutine, channel, lock, or error path is added, removed, or reordered.

## Testing strategy

No new tests. `v2session_replay_test.go` already exercises the replay/resync path and stays untouched; the snapshot-path tests live in the existing suite. All reference the moved symbols by package-internal name, so they compile and pass unchanged once the decls land in the same package.

Verification is the `make check` gate (AC #4): `go vet`, `go test -race`, `staticcheck`, substrate-guard, e2e must all be green. Developer flow: `go build ./internal/relay/` incrementally after each cut to catch a missed/extra import, then `make check` once at the end. `go vet`/`staticcheck` will flag any accidental unused import in either file.

## Open questions

None. Placement is fully determined above; `snapshotReq` retention is settled by the CRUX. File name follows the ticket's suggestion (`v2session_replay.go`); the file-level doc comment clarifies it also holds the screen-snapshot request path.

## Acceptance criteria (authoritative — supersede the ticket's Technical Notes list where they differ)

- [ ] A new file `internal/relay/v2session_replay.go` (`package relay`) is created with a file-level doc comment matching the `v2session_debugbundle.go` template, and holds exactly these decls, moved verbatim from `v2session.go`: the `msgSnapshotConvNotFound`/`msgSnapshotOffline` const block, `handleRequestSnapshot`, `snapshotReplyError`, `SetReplaySource`, `replayMissed`, `emitResync`, `drainReplayOnce`. None of these seven declarations remains in `v2session.go`.
- [ ] `snapshotReq` (`v2session.go:113-122`) is deliberately NOT moved — it is the ActiveConns-enumeration request type whose every consumer stays in `v2session.go`. It remains in `v2session.go` untouched.
- [ ] No exported identifier is renamed, added, or removed; no logic changes beyond what the move mechanically forces. `v2session.go`'s import block is unchanged (no import is forced out — `eventring` stays live via the `replayQueue`/`replayRing` struct fields).
- [ ] Only `internal/relay/v2session.go` and the new `internal/relay/v2session_replay.go` change; no other file in the tree is touched (the `cmd/pyry` `SetReplaySource` call site and `v2session_replay_test.go` both resolve per-type/per-package and need no edit).
- [ ] `make check` is green (vet, race test, staticcheck, substrate-guard, e2e).
