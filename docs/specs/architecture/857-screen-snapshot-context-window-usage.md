# Spec #857 — Carry context-window usage on `screen_snapshot`

**Size:** S (~35 production LOC across 3 files, ~90 test LOC, no edit fan-out).
**Security-sensitive:** No — outbound-only, read-only reflection of two non-secret aggregate integers (used tokens, window size) to an already-paired, AEAD-authenticated client that already receives the full rendered screen text under the same seal. No inbound verb, no authz decision, no mutation, no new input parsing. The transcript content itself never crosses the wire; only two summed integers do. Mirrors #848 (the settings wire), which carried no label. (Contrast the write path #841/#845, `security-sensitive` because it mutates a control from untrusted input.)

This is the **wire** leaf of #855 (usage-on-snapshot). Its dependency, the **reader** leaf #856, is merged: `contextwindow.Read(path string) (Usage, error)`, where `Usage{UsedTokens, WindowTokens int}` reports the current context-window occupancy from a resolved transcript. This ticket carries those two ints on the wire; it does not decode or re-resolve the transcript.

It follows the #847/#848 pattern exactly (additive payload fields with no `omitempty` + an optional primitive-typed relay seam populated in `handleRequestSnapshot`), with one deliberate placement difference from #848 explained in Design §4.

---

## Files to read first

- `internal/protocol/snapshot.go:36-62` — `ScreenSnapshotPayload` + its doc comment. The two new fields go after `YOLO` (:61); extend the doc comment's "no omitempty / distinguishable from unset" paragraph to cover them. This is the single edit site for the payload change.
- `internal/protocol/snapshot_test.go:86-101` — `TestScreenSnapshotPayload_ZeroSettingsFieldsPresent`. The marshal test the AC-1 assertion extends (add `"used_tokens":0` / `"window_tokens":0` to the always-present list). Note the `bytes.Contains(out, …)` style.
- `internal/relay/v2session.go:623-638` — `SnapshotSettings` field on `V2SessionConfig`. The new seam goes directly after it, same shape (optional, primitive-typed closure). Copy the "primitive-typed so internal/relay imports neither internal/sessions nor …" rationale.
- `internal/relay/v2session.go:1709-1770` — `handleRequestSnapshot`. Read the branch order (KnownConversation reject → nil-Snapshotter offline → `!live` offline → read settings → marshal-and-forward). The new usage read sits beside the `SnapshotSettings` block (:1740-1743), on the success path only, before the `json.Marshal` at :1746. The `ScreenSnapshotPayload{…}` literal at :1746 gains the two new fields.
- `internal/relay/v2session_test.go:3710-3859` — `TestV2Session_OpenState_RequestSnapshot` table: the struct fields (`settings`, `wantModel`/`wantEffort`/`wantYOLO`), the `customSettings` / `defaultSettings` / nil rows (:3720-3806), the `SnapshotSettings: tt.settings` wiring (:3816), and the `TypeScreenSnapshot` assertion block (:3840-3859). Reuse `driveToOpen` / `v2Recorder` / `sealAppFrame` / `decryptAppFrame` verbatim; add `usage`/`wantUsed`/`wantWindow` alongside the settings columns.
- `cmd/pyry/relay.go:280-302` — `startRelayV2` signature (params `sup`, `claudeSessionsDir`, `logger`, `ctx` are all in scope before the config literal).
- `cmd/pyry/relay.go:315-387` — the `NewV2SessionManager(V2SessionConfig{…})` literal, incl. `SnapshotSettings: snapshotSettings` (:350). The new `SnapshotUsage:` field is wired here; the closure is **built in the lines just above the literal** (see Design §4), not threaded from `main.go`.
- `cmd/pyry/relay.go:415-424` — the turn-stream gate: `if bridge != nil && claudeSessionsDir != "" { probe := newBootstrapProbe(logger); pidFn := func() int { return sup.State().ChildPID }; … }`. This is the exact probe/pidFn construction the usage closure mirrors (with its own dedicated instance).
- `cmd/pyry/interactive_turn_stream_v2.go:250-367` — `resolveOwnBootstrapJSONL(dir, probe, pidFn)`, the probe-preferred bootstrap transcript resolver the usage closure calls. Note: (a) it returns the transcript the daemon's **own** child holds open (confinement-guarded), (b) it returns a non-nil **error** for every not-yet/transient case (pid down, no fd, raced), and (c) it is **stateful** (`resolvedOnce`/`sawEmpty`) and "must NOT be called from multiple goroutines." The usage closure collapses (b) and is unaffected by (c) — see Design §4 and Concurrency.
- `cmd/pyry/interactive_turn_stream_v2.go:28` — `newBootstrapProbe = rotation.DefaultProbe`; `:41 bootstrapProbeUsable`. On a no-lsof host the resolver delegates to `resolveLatestSessionJSONL` (newest-by-mtime) — the same fallback the turn stream accepts.
- `internal/contextwindow/usage.go:29-93` — `Usage{UsedTokens, WindowTokens int}` and `Read`. Key contracts the closure relies on: `Read("")` returns `Usage{WindowTokens: 200_000}` with **nil** error (the fresh/no-transcript report); a non-empty path that fails to open returns a wrapped error with a **zero** `Usage`; last-usage-wins gives the post-compaction shrink for free.
- `docs/specs/architecture/848-populate-screen-snapshot-settings.md` — the sibling wire spec this mirrors. Same handler, same test table, same "no omitempty / nil-seam-preserves-prior-shape" discipline.

---

## Context

`screen_snapshot` is the always-available, parser-independent v2 reply (ADR-025 floor) that already answers a `request_snapshot` with the bootstrap session's rendered text plus its model / effort / YOLO (#847/#848). The desktop Run-configuration sheet already fetches all of that through this one snapshot request; the context-window usage gauge (pyrycode-desktop#182) needs the same snapshot to also carry usage, so the client's fetch stays single and rides the floor that survives any screen-parser break.

The used-tokens and window figures come from #856's reader. This ticket adds two wire fields, an optional relay seam, and the cmd/pyry closure that resolves the bootstrap transcript path and calls `contextwindow.Read`.

Read-only. No new inbound verb, no relay-protocol change (the relay forwards v2 frames opaquely).

---

## Design

Three additive changes across three production files. No signature breaks. No `main.go` change (see §4).

### 1. Two new fields on `ScreenSnapshotPayload`

Add after `YOLO`, no `omitempty`:

```go
UsedTokens   int `json:"used_tokens"`
WindowTokens int `json:"window_tokens"`
```

`UsedTokens` is the current context size (input + cache-read + cache-creation + output on the latest usage-bearing entry — #856's `Usage.UsedTokens`), **not** a running total. `WindowTokens` is the context-window size (200 000 for every current model today — #856's `Usage.WindowTokens`). No `omitempty`, matching the model/effort/yolo fields: a zero-value payload marshals with both keys present, so `used_tokens:0` / `window_tokens:0` are explicit on the wire and distinguishable from unset (AC-1). Extend the struct's doc comment to state that a client computes "N% used (X of Y)" as `used_tokens / window_tokens` (AC-5), with `window_tokens == 0` meaning "usage seam not wired" (the nil-seam case, below).

### 2. New optional read seam on `V2SessionConfig`

Add one field directly after `SnapshotSettings` (:638):

```go
// SnapshotUsage reports the bootstrap session's current context-window
// occupancy — used tokens and window size — so handleRequestSnapshot can
// populate the screen_snapshot reply's used_tokens / window_tokens fields
// (#857). Optional: nil ⇒ the handler reports both at their zero values
// (used_tokens:0, window_tokens:0), preserving the pre-#857 wire shape (the
// foreground / unwired case). Primitive-typed (two ints) so internal/relay
// imports neither internal/contextwindow nor internal/sessions; the cmd/pyry
// closure resolves the bootstrap transcript path and calls contextwindow.Read,
// collapsing any open failure to the same zero/window-default report as a
// fresh session. Read-only reflection of non-secret runtime state.
SnapshotUsage func() (usedTokens, windowTokens int)
```

**Why a primitive-typed closure.** Same rationale as `SnapshotSettings`/`DebugBundler`: relay declares a bare closure over the two scalars it needs; the cmd/pyry composition root owns the `internal/contextwindow` dependency. No presence semantics are needed (nil-vs-set is the closure field itself), so no `ok` return and no relay-local struct.

**Do not add a validation branch to `NewV2SessionManager`.** The seam is optional, exactly like `Snapshotter` / `SnapshotSettings` / `DebugBundler`. Nil is legal and means "report zeros."

### 3. Populate the two fields in `handleRequestSnapshot`

On the success path only (after the `SnapshotSettings` read at :1740-1743, before the `json.Marshal` at :1746), add a nil-guarded read mirroring the settings block:

- Signature/behavior: `var usedTokens, windowTokens int; if m.cfg.SnapshotUsage != nil { usedTokens, windowTokens = m.cfg.SnapshotUsage() }`.
- The `ScreenSnapshotPayload{…}` literal gains `UsedTokens: usedTokens, WindowTokens: windowTokens` beside `Model`/`Effort`/`YOLO`.

No change to any reject branch, the error-reply path, or the forward path. The usage read is downstream of every reject (`live == true` already established), so it never gates or alters an error reply. Behavioural contract (asserted by the extended table test, § Testing): a non-nil seam's two ints appear on the reply; a nil seam leaves both at `0`, both present on the wire.

### 4. Wire the closure in `cmd/pyry/relay.go` (not `main.go`)

Build the closure in `startRelayV2`, in the lines immediately **above** the `V2SessionConfig{…}` literal (so it can be assigned into the literal), gated on `claudeSessionsDir != ""`:

- `var snapshotUsage func() (usedTokens, windowTokens int)` — nil by default.
- When `claudeSessionsDir != ""`: build a **dedicated** `resolveOwnBootstrapJSONL(claudeSessionsDir, newBootstrapProbe(logger), func() int { return sup.State().ChildPID })` instance, then set `snapshotUsage` to a closure that:
  1. `path, _, err := usageResolve(ctx)`; on `err != nil` (child in backoff, no fd yet, raced, confined-out) set `path = ""`. The offset return is ignored.
  2. `u, rerr := contextwindow.Read(path)`; on `rerr != nil` (genuine open failure on a resolved path, e.g. raced away between resolve and read — the case #856 left to this ticket) fall back to `u, _ = contextwindow.Read("")`. `Read("")` returns `Usage{WindowTokens: 200_000}` with nil error, so this collapses to the same deterministic zero/window-default report as a fresh session, and the error is never surfaced.
  3. `return u.UsedTokens, u.WindowTokens`.
- Assign `SnapshotUsage: snapshotUsage` in the config literal.
- Add `import "github.com/pyrycode/pyrycode/internal/contextwindow"`.

The whole closure is ≤ ~14 lines. Keep it that size — it is glue over three already-tested primitives (`resolveOwnBootstrapJSONL`, `contextwindow.Read`, `sup.State`).

**Why built in `relay.go`, not threaded from `main.go` like #848's `snapshotSettings`.** #848's closure was placed in `main.go` for one reason: it depends on `*sessions.Pool.DefaultSettings`, and the discipline is "keep the `internal/sessions` dependency at the composition root so `relay.go` stays sessions-free." **That rationale does not apply here.** The usage closure needs no `internal/sessions` type: `resolveOwnBootstrapJSONL` and `newBootstrapProbe` are already cmd/pyry-local (same package, in `interactive_turn_stream_v2.go`), and `internal/contextwindow` imports `internal/agentrun/jsonl`, never `internal/sessions`. So `relay.go` can own this closure without violating the sessions-free discipline. Building it here (a) avoids a `main.go` edit and a new `startRelay`→`startRelayV2` parameter, (b) co-locates the `claudeSessionsDir != ""` gate with the identical turn-stream gate 100 lines below, and (c) reuses the exact `probe`/`pidFn` construction already proven at `relay.go:421-422`. Net: three files instead of four, and the seam is nil precisely when there is no sessions dir to resolve a transcript from — which is the honest "unwired" condition for *this* seam.

**Why `resolveOwnBootstrapJSONL` (probe-preferred), not `resolveLatestSessionJSONL` (newest-by-mtime).** The snapshot renders the **bootstrap** child (`Snapshotter: sup`); the usage must describe that same session. Newest-by-mtime latches onto a sibling claude's transcript when a second child runs in the same cwd (#827/#838) — it would report a foreign session's usage. The probe-preferred resolver follows the daemon's own child's open fd (confinement-guarded), keeping usage-source == snapshot-source == bootstrap (the #848 invariant). On a no-lsof host it delegates to newest-by-mtime — the same tradeoff the turn stream already accepts (#838 AC5).

**Why the double error-collapse (resolver error → `""`, then `Read` error → `Read("")`).** `resolveOwnBootstrapJSONL` returns an *error* (not `("",0,nil)`) for the fresh/backoff/raced cases, and `contextwindow.Read` returns an *error* for a genuine open failure on a resolved path. Both must map to the same deterministic fresh-session wire shape `(0, 200_000)` (the seam is non-erroring by contract — plain ints, mirroring `SnapshotSettings`). Collapsing via `Read("")` avoids duplicating the 200 000 window constant into `cmd/pyry`; `Read("")` provably never errors, so the discarded error is dead. Do **not** modify `internal/contextwindow` to export the constant — that would add a fourth production file for zero behavioural gain.

### Design invariant behind AC-2/AC-4 (usage-source == snapshot-source == bootstrap)

`Snapshotter` (`*supervisor.Supervisor`) renders the bootstrap child's screen; `resolveOwnBootstrapJSONL` over `sup.State().ChildPID` resolves that **same** child's transcript; `contextwindow.Read` reports its latest usage entry. So the reported used/window describe the one session the screen shows. AC-4 (post-compaction shrink) holds by construction: `Read` reports the latest usage-bearing entry with no running total, so after claude's post-compaction turn records a smaller `input_tokens`, the next `request_snapshot` reads the smaller current figure — the reader's "reset" surfaces on the wire with no dedicated marker (this is #856's last-usage-wins, unit-tested there; this ticket only proves the wire carries a changed report faithfully).

**Do not pre-carve a conversation-keyed usage seam.** The snapshot is single-session (bootstrap) today. If a future ticket makes it multi-session, `Snapshotter`, `SnapshotSettings`, and `SnapshotUsage` get keyed together; inventing a keyed usage seam now would add a parameter neither the snapshot source nor the resolver can honour.

---

## Concurrency model

No new goroutines, no new locks.

- `handleRequestSnapshot` runs on the v2 manager's single `Run` dispatch goroutine (the same goroutine that already invokes `SnapshotSettings` inline). `SnapshotUsage` is invoked **synchronously** on that goroutine and returns before the marshal — identical posture to #848.
- The `resolveOwnBootstrapJSONL` instance the closure captures is **stateful** (`resolvedOnce`/`sawEmpty`) and documented as "not safe for concurrent use." Safety here rests on two facts: (1) it is a **dedicated** instance, distinct from the turn-stream's resolver (built separately at `relay.go:421-423`, driven by `Producer.Run`), so the two are never shared; (2) it is reached only through `SnapshotUsage` → `handleRequestSnapshot` → the single manager `Run` goroutine. One instance, one goroutine ⇒ no data race. Additionally, the closure ignores the resolver's offset return, so its cold/warm state is inert for this consumer — only the freshly-probed `path` matters.
- `sup.State()` is mutex-guarded, so `pidFn` reads a consistent live-or-0 PID even as the child respawns across backoff.

**Performance note (accepted).** Per `request_snapshot`, the closure runs one `lsof` probe plus a full scan of the transcript JSONL (`contextwindow.Read` reads the whole file to find the last usage block), synchronously on the manager `Run` goroutine, blocking other v2 dispatch for that connection for the scan duration. `request_snapshot` is user-initiated and infrequent (opening the Run-config sheet), and the existing synchronous `ScreenSnapshot()` render already runs on this goroutine, so the bounded added cost is acceptable. See Open Questions for the caching escalation trigger.

---

## Error handling

No new error path reaches the wire. Every failure collapses to the deterministic fresh-session report `(usedTokens 0, windowTokens 200_000)`:

- nil seam (`claudeSessionsDir == ""`) ⇒ handler leaves both at `0` (AC-3; the nil-guard, byte-compatible with the pre-#857 shape apart from the two new zero fields). Note this is `(0, 0)`, distinct from a wired-but-fresh `(0, 200_000)` — the two are different configurations, and both are valid.
- resolver error (backoff / no fd / raced / confined-out) ⇒ `path = ""` ⇒ `Read("")` ⇒ `(0, 200_000)`.
- `Read` open failure on a resolved path (raced away) ⇒ `Read("")` fallback ⇒ `(0, 200_000)`. Never surfaced; never logged with path content.
- The handler's existing reject/offline/marshal-failure branches are untouched (usage is read only after `live == true`).

---

## Testing strategy

Two required test surfaces; no new harness, no cmd/pyry test (mirroring #848 — the closure is glue over primitives unit-tested in #856 and exercised transitively via the daemon path; the same posture as the untested-in-cmd/pyry `debugBundler`/`settingsUpdaterAdapter` closures). Run: `go test -race ./internal/protocol/... ./internal/relay/... ./internal/contextwindow/...` and `go vet ./...`.

**1. Protocol marshal (AC-1).** Extend `TestScreenSnapshotPayload_ZeroSettingsFieldsPresent` (`snapshot_test.go:91`): add `"used_tokens":0` and `"window_tokens":0` to the always-present list, proving the zero-value payload keeps both fields on the wire (no `omitempty`).

**2. Relay table (AC-2/AC-3).** Extend `TestV2Session_OpenState_RequestSnapshot` (`v2session_test.go`):
- Add `usage func() (usedTokens, windowTokens int)`, `wantUsed int`, `wantWindow int` to the table struct.
- Wire `SnapshotUsage: tt.usage` into the `V2SessionConfig` at :3816.
- In the `TypeScreenSnapshot` assertion block (:3840-3859), decode and assert `p.UsedTokens == tt.wantUsed` and `p.WindowTokens == tt.wantWindow` off the already-unmarshaled `ScreenSnapshotPayload`.
- Rows (as scenarios, not full bodies):
  - **injected usage surfaces** — `usage` returns e.g. `(12345, 200000)`, live snapshotter ⇒ reply `used_tokens:12345, window_tokens:200000`.
  - **nil seam reports zeros** — `usage: nil`, live ⇒ `used_tokens:0, window_tokens:0` (AC-3; also the byte-compatibility check that a nil seam preserves the prior wire shape apart from the two zero fields).
  - **fresh session via seam returning window-default** — `usage` returns `(0, 200000)` ⇒ `used_tokens:0, window_tokens:200000` (distinguishes wired-fresh from nil-seam).
  - The existing error/offline rows need no usage column — set `usage` to nil and leave `wantUsed`/`wantWindow` at `0`; those branches never read the seam. Confirm they still pass unchanged.

**3. Relay sequential (AC-4).** A small dedicated test (not a table row, since it drives two requests): drive `request_snapshot` twice against a stub `SnapshotUsage` whose `usedTokens` return **shrinks** between the two calls (e.g. `50000` then `3000`, both `window 200000`), and assert `reply2.UsedTokens < reply1.UsedTokens`. This proves a changed reader report surfaces on the wire so a client can reflect a compaction; the reader's actual post-compaction shrink is #856's `TestRead_Fixtures`, not re-tested here. Reuse `driveToOpen` / `v2Recorder` / `sealAppFrame` / `decryptAppFrame`; a counter-closure over a slice of return values is enough for the stub.

The e2e `relay_v2_daemon_test.go` snapshot assertion (`:279`) needs **no** change: it decodes `ScreenSnapshotPayload` and asserts only `ConversationID`/`TS`, so the additive fields don't break it, and asserting real usage there would require a live claude transcript (#856's and the table test's job, not e2e's).

---

## Acceptance criteria (from the ticket, mapped)

1. Payload carries used-tokens + window fields; zero-value payload marshals with both present (no `omitempty`). → Design §1; protocol marshal test.
2. Wired seam ⇒ `request_snapshot` reply reflects the reader's report. → Design §2/§3/§4; relay table "injected usage" row. Source alignment (bootstrap) by the §Design invariant.
3. Nil seam ⇒ byte-compatible with the prior shape apart from the two always-present zero fields. → Design §3 nil-guard; relay table "nil seam" row.
4. After a compaction, a subsequent `request_snapshot` returns a smaller used-tokens figure. → §Design invariant (last-usage-wins, #856) + relay sequential test (a shrinking report surfaces on the wire); the reader's reset itself is #856-unit-tested.
5. The two fields suffice for a client to compute "N% used (X of Y)" = used/window. → Design §1 (two fields present + doc comment); client rendering out of scope (pyrycode-desktop#182).

---

## Open questions

- **Field names.** `used_tokens` / `window_tokens` map 1:1 to #856's `Usage.UsedTokens` / `Usage.WindowTokens` and read cleanly for the desktop's "X of Y" gauge. The desktop client (pyrycode-desktop#182) consumes whatever ships; this spec defines the contract. Alternatives (`context_used` / `context_window`) are acceptable if the developer finds them clearer, but avoid bare `used` / `window`. Not blocking.
- **Scan cost caching (deferred — evidence-based).** `contextwindow.Read` rescans the whole transcript per `request_snapshot`. Snapshot requests are infrequent, so no caching now. If a future feature makes snapshot requests frequent, or transcripts routinely grow to many MB, escalate to a cached last-offset read — but only on an observed latency problem, not preemptively.
- **None affecting correctness.** All three changes are additive, the seam is optional, and no consumer outside the one handler, the one config literal, and the one payload struct changes (`codegraph_impact ScreenSnapshotPayload` = one in-repo constructor).
