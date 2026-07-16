# Spec #1039 — Remove the dead v1 `Dispatcher` / `FirstFrameGate` machinery from `internal/dispatch`

**Ticket:** #1039 · **Size:** S · **not** security-sensitive

This is a **deletion + test-rehome** ticket. #913 slice 1 (`5edfa1e`) removed the
production v1 relay branch and the `PYRY_MOBILE_V2` escape hatch, orphaning the v1
`Dispatcher` / `FirstFrameGate` machinery in `internal/dispatch` with **zero production
callers**. This slice (#913 §Follow-on item 3) physically removes that machinery, keeping
only the v2 routing surface the surviving relay path actually uses: `Route`, `Conn` (+
its outbound seam), and the small helper set around them.

The two `internal/relay/handlers` tests that rode the removed `Dispatcher` async harness
were already migrated to the direct-call pattern by the blocking ticket #1038 (merged
2026-07-16), so this removal leaves that package green.

**Confinement (verified):** every compilable reference to the removed symbols lives inside
`internal/dispatch/dispatch.go` itself. `codegraph_impact("Dispatcher")` and
`codegraph_impact("FirstFrameGate")` each return only in-file symbols; a tree-wide grep of
every removed name finds only **comments** (historical analogy) in other packages. There is
**no** cross-package edit fan-out. Files touched: `dispatch.go` (edit), `dispatch_test.go`
(edit), `gate_test.go` (delete). One production file.

---

## Files to read first

- `internal/dispatch/dispatch.go:165-537` — the **contiguous removal span**: `Config`,
  `FirstFrameGate`, `FirstFrameOutcome`, `Dispatcher`, `connState`, `New`, `Register`,
  `Outbound`, `ActiveConns`, `Run`, `routeConn`, `runConn`, `runGate`, `handleOne`. Delete
  wholesale (AC1).
- `internal/dispatch/dispatch.go:89-93` — `setAuth`. **The non-contiguous extra removal /
  U1000 trap.** Its sole caller is `runGate:518` (deleted). See §Design step 2 — this is a
  deliberate, U1000-forced deviation from AC2's literal keep-list.
- `internal/dispatch/dispatch.go:54-163` — the **kept surface** (`Handler`, `Conn` fields,
  `ConnID`, `Auth`, `NewTestConn`, `NewConn`, `NextID`, `Send`, `Reply`) plus the doc
  comments that cite removed identifiers and must be re-worded (§Design step 4).
- `internal/dispatch/dispatch.go:539-611` — `Route` + `sendError` (kept, behaviour
  **unchanged**). These are the six inbound frame-validation branches the re-homed tests
  must keep covering: malformed → `protocol.malformed`; `IsV1Compatible` unsupported →
  `protocol.unsupported`; `IsV1Compatible` unknown-type → `protocol.unknown_type`; default
  → `protocol.unsupported`; no-handler → `protocol.unsupported`; handler-error → WARN log,
  no reply.
- `internal/dispatch/dispatch.go:1-38` — package doc. Rewrite to describe only `Route` +
  `Conn`; the whole "Run is a single demux goroutine … one goroutine per active conn_id"
  concurrency model (lines 15-21) is deleted with the machinery.
- `internal/dispatch/dispatch_test.go:550-618` — the retained `TestRoute_StandaloneInvocation`
  and `TestRoute_NoHandler_UnsupportedReply`. **These are the exact direct-call pattern** the
  four re-homed tests must mirror (`NewConn` → `Route` → read `outbound`). Do not modify them.
- `internal/dispatch/dispatch_test.go:15-67` — shared helpers (`testLogger`, `mustEncode`,
  `decodeError`, `frame`, `runDispatcher`). Which survive is driven by staticcheck — see
  §Design step 3.
- `internal/dispatch/dispatch_test.go:69-231, 309-534` — the tests to re-home / delete;
  their coverage map is in §Design step 3.
- `internal/dispatch/gate_test.go` — deleted in full (AC3). Note it defines `helloAckResponse`,
  which `dispatch_test.go:411` uses **only** inside a deleted test — no dangling reference.
- `internal/relay/v2session.go:1051` — `dispatch.NewConn(s.connID, outbound, s.device)`. Proof
  the `auth` field is populated via the **constructor** (not `setAuth`), so `auth` / `Auth()`
  / `NewConn` stay live after `setAuth` goes.
- `internal/relay/handlers/register_push_token.go:53` — `dev := c.Auth()`. The live production
  reader of `Auth()`; another reason the auth field survives.
- `docs/specs/architecture/913-remove-v1-relay-dispatch-branch.md` — the prior slice; its
  §Follow-on item 3 is this ticket, and its §Security review already established the v1
  dispatch surface is dead/unreachable in production.

---

## Context

The v1 relay leg authenticated a phone's first frame through a `FirstFrameGate` closure and
demultiplexed subsequent frames per conn_id through `Dispatcher.Run`. #913 deleted that leg;
the daemon now speaks only Mobile Protocol v2 (Noise_IK), and the v2 session manager
(`internal/relay/v2session.go`) does its own capability-aware routing, calling into
`internal/dispatch` for exactly one thing: single-frame handler-table dispatch via `Route`,
against a `Conn` it constructs itself with `NewConn`.

Everything in `dispatch` above `Route` — the whole gate + demux state machine — has had zero
production callers since `5edfa1e`. It is dead, not latent: no env var, flag, or config
re-enables it (`PYRY_MOBILE_V2`, the only switch, was deleted in #913). This slice removes it.

**Why not security-sensitive.** The removed code includes a first-frame **auth gate**, which
would normally pull a security review. It does not here: the gate has been unwired since #913
(0 live callers — verified), so it gates nothing on the live surface. `Route`, the kept
inbound router on the relay surface, is **unchanged**. There is no design change to any live
untrusted surface — nothing for a spec-stage adversarial pass to audit. (sec tracks design,
not lineage.)

---

## Design

Two edits + one file delete. No new files, no new types, no new behaviour.

### 1. Delete the machinery (`dispatch.go`, AC1)

Delete the contiguous span `dispatch.go:165-537` — `Config`, `FirstFrameGate`,
`FirstFrameOutcome`, `Dispatcher`, `connState`, `New`, `Register`, `Outbound`, `ActiveConns`,
`Run`, `routeConn`, `runConn`, `runGate`, `handleOne`. The block is one clean cut: `Route`
(539) begins the kept tail.

### 2. Delete `setAuth` too — the U1000 correction to AC2

**AC2 lists `setAuth` under "keep", but keeping it fails AC6/AC7.** `setAuth`
(`dispatch.go:89-93`) is unexported and its **only** caller is `runGate:518` (deleted in step
1). An unexported method with zero callers trips `staticcheck` U1000, which AC6 forbids and
AC7 (`make check`) gates on. Resolution: **remove `setAuth`** — it is the dispatcher's
write-seam for the removed gate's accept path (its own doc says "called … from runGate's
accept branch"), so it belongs to the removed machinery, not the kept surface.

This does **not** weaken the live auth path. The `auth` field is written by the `NewConn` /
`NewTestConn` **constructors** (`v2session.go:1051` passes the handshake-matched
`s.device`) and read by `Auth()` (`register_push_token.go:53`). Those all stay. Only the
dispatcher-only mutator seam goes.

> **Deviation flag for the reviewer:** the effective keep-list is AC2 **minus `setAuth`**.
> Keep: `Handler`, `Conn` (fields `id` / `nextID` / `outbound` / `auth`), `ConnID`, `Auth`,
> `NewConn`, `NewTestConn`, `NextID`, `Send`, `Reply`, `Route`, `sendError`. This is forced by
> AC6, not a scope expansion.

### 3. `dispatch_test.go` — re-home kept-branch coverage, delete removed-machinery tests (AC4)

Same reasoning as blocker #1038: the harness was a *driver* for the kept `Route` / `Conn`
functions. Tests that drove kept branches **through** `Dispatcher.Run` are re-expressed as
direct `Route` / `Conn` calls (the retained `TestRoute_*` pattern), **not** deleted — deleting
them would drop unit coverage of 4 of `Route`'s 6 branches while AC2 promises those symbols
unchanged.

**Re-home to direct `Route` / `Conn` calls** (each covers a kept branch the retained pair does
not):

| Test | Kept branch it covers | Re-home shape |
|---|---|---|
| `TestMalformedInnerFrame` | malformed inner frame → `protocol.malformed`, `InReplyTo` nil | `Route` with `json.RawMessage("not json")`, nil handlers |
| `TestUnknownType` | `IsV1Compatible` unknown-type → `protocol.unknown_type` | `Route` with `Type: "bogus"` |
| `TestEncryptedRefusal` | `IsV1Compatible` unsupported-feature → `protocol.unsupported`, `InReplyTo` = req id | `Route` with `PayloadEncrypted: true` |
| `TestIDCounter_MonotonicPerConn` | per-conn `NextID` monotonicity **and** per-conn isolation | two `NewConn`s (`conn-a`, `conn-b`), handler calls `NextID` 4×, assert each independently sees `[1,2,3,4]` |

**Delete as redundant** (already covered by the retained pair): `TestEmptyTable_UnsupportedType`
(= `TestRoute_NoHandler_UnsupportedReply`), `TestReply_InReplyToMatchesRequest`
(= `TestRoute_StandaloneInvocation`).

**Delete as removed-machinery tests + helpers**: `runDispatcher`, `connIDs`,
`TestCtxCancel_Teardown`, `TestFramesClose_Teardown`, `TestTwoConns_ArrivalOrderPreservedPerConn`,
`TestDispatcher_ActiveConns_Snapshot`, `TestDispatcher_ActiveConns_ExcludesPreGateConn`,
`TestRegister_AfterRunPanics`, `TestRegister_DuplicatePanics`, `TestNew_NilFramesPanics`,
`TestNew_NilLoggerPanics`.

**Re-home contract (mirror the retained `TestRoute_*` pair, don't invent a new shape):**
construct `conn := NewConn(id, outbound, nil)` over a buffered `outbound` channel, build the
raw frame with `mustEncode(...)`, call `Route(ctx, testLogger(), conn, handlers, frame)`
**synchronously**, then read the reply/error off `outbound` and assert. `Route` invokes the
handler on the calling goroutine, so there is **no goroutine, no `Dispatcher`, no cancel/wait**
— which is exactly why the async helpers fall away below.

**Helper fates — let `staticcheck` be the arbiter (AC6):**

- **Keep** (referenced by re-homed / retained tests): `testLogger`, `mustEncode`,
  `decodeError`, `equalIDs`.
- **Prune** — these are used **only** by deleted or now-synchronous tests, so they go U1000
  after the re-home:
  - `connIDs` — used only by the deleted `ActiveConns` tests.
  - `frame` — builds a `RoutingEnvelope` to feed `Dispatcher`'s `Frames` channel; direct
    `Route` takes a raw frame from `mustEncode` instead, so no re-homed test calls it.
  - `waitOrFail` — the direct-`Route` re-homes are synchronous, so no `WaitGroup` barrier is
    needed.

  If the developer picks a re-home shape that keeps `frame` / `waitOrFail` genuinely
  referenced, keeping them is fine — **the gate is "`staticcheck ./internal/dispatch/` is
  clean", not a fixed keep-list.** The `sync` test import likely drops with `waitOrFail`;
  let the compiler / `goimports` settle imports.

### 4. Comment tidies inside the edited files (required — no linter catches stale prose)

The gate-flow doc blocks deleted with the machinery (`runGate` / `gateCompleted`, naming
`AuthenticateFirstFrame`) go automatically. The **kept** doc comments below still cite removed
identifiers and must be re-worded — enumerated because nothing flags a comment that names a
deleted symbol:

- **Package doc (`:1-38`)** — rewrite. Drop the "Concurrency model: Run is a single demux
  goroutine …" paragraph (15-21) and the opening "demultiplexes … by ConnID" framing; the
  package now *provides* `Route` (single-frame inbound-envelope router through a handler table)
  and `Conn` (per-conn outbound seam), with the demux living in the v2 session manager. Keep the
  still-true `Route` security posture (sealed error envelopes carry only `Code*` + a static
  message; diagnostics never log the raw frame payload), reframed from "the dispatcher" to
  "`Route`". Drop the head-of-line-blocking / backpressure notes (they described `Run`).
- **`Handler` doc (`:54-59`)** — drop the "in v1 … the auth-gate slice (#308) introduces that
  surface" clause; state that a non-nil handler return is logged at WARN and `Route`
  synthesises no reply from it.
- **`Conn.auth` field doc (`:68-76`)** — drop "matched device snapshot from the first-frame
  gate's accept verdict. Written … by the dispatcher (via setAuth)"; state the snapshot is
  supplied by the constructor (`NewConn` / `NewTestConn`) — e.g. the v2 manager passes the
  handshake-matched device.
- **`Auth()` doc (`:82-87`)** — drop "if the first-frame gate has not yet accepted …"; state it
  is nil when the conn was constructed without an auth device; verb handlers must nil-check.
- **`NewTestConn` doc (`:95-108`)** — drop the `routeConn` reference ("sole production Conn
  factory (see routeConn)") and the `AuthenticateFirstFrame` / hello_ack post-accept simulation
  paragraph. Keep the load-bearing fact: `nextID` starts at zero, so the first `NextID()`
  returns 1.
- **`NewConn` doc (`:110-122`)** — drop "outside Dispatcher.Run" and "matching the gate-disabled
  production path"; the "v2 session manager decrypts a noise_msg then dispatches the inner
  envelope through the handler table via `Route`" description stays accurate.
- **`NextID` doc (`:124-128`)** — optional; "per-conn goroutine is the only writer" stays
  generically true (the v2 manager owns that goroutine).

### 5. Cross-package historical-analogy comments — leave them (out of scope, recommended)

The ticket marks these **optional** ("may be updated for accuracy"). They reference removed
dispatch symbols as *v1 reference points* — e.g. `v2session.go:510` "Mirror v1's
`internal/dispatch.Dispatcher.Register` registration", `:661`, `:759`, `:2011`;
`eventring/ring.go:80`; `acp/acp.go:120`. **Recommendation: do not touch them.** They document
history accurately, they are comments (no build/lint impact — grep confirms no *code*
reference), and editing `v2session.go` / `eventring` / `acp` widens the file-touch and adds
cross-branch merge-overlap risk for zero functional gain. Keeping the change confined to
`internal/dispatch/` is the point of the slice.

### Imports (compiler-forced — do not pre-prune)

After the removal, `sync` is unused in `dispatch.go` (only the removed `Dispatcher` used
`sync.Mutex` / `sync.WaitGroup`) → the compiler forces its removal. `sync/atomic` stays
(`Conn.nextID`), as do `context`, `encoding/json`, `errors`, `fmt`, `log/slog`, `time`,
`internal/devices`, `internal/protocol`. Let `go build` / `goimports` settle the exact set.

---

## Concurrency model

Strictly shrinks. The removal deletes the demux goroutine (`Run`) and the per-conn goroutines
it spawned (`routeConn` → `runConn`), plus their `WaitGroup` / `d.mu` / channel-close shutdown
choreography. The kept surface is goroutine-free: `Route` runs the handler synchronously on the
caller's goroutine; the v2 session manager owns whatever goroutine calls it. Nothing new.

---

## Error handling

Unchanged. `Route`'s error contract is preserved verbatim: malformed JSON →
`CodeProtocolMalformed` (no `InReplyTo`); `IsV1Compatible` unsupported → `CodeProtocolUnsupported`;
unknown-type → `CodeProtocolUnknownType`; default → `CodeProtocolUnsupported`; no registered
handler → `CodeProtocolUnsupported`; handler-returned error → WARN log, no synthesised reply. The
only failure path removed is the deleted gate's malformed-first-frame fall-through, which was
dead. `sendError` (kept) still seals error envelopes: `Code*` + static message only, nothing
derived from untrusted input echoed back.

---

## Testing strategy

- **Green gate (AC7):** `make check` = `go build ./...`, `go vet ./...`, `staticcheck ./...`,
  `go test -race ./...`. Two acceptance risks, both handled above: (a) U1000 on `setAuth` and on
  the now-unused test helpers (`connIDs`, and typically `frame` / `waitOrFail`) — resolved by
  deleting them; (b) the unused `sync` import — compiler-forced.
- **Coverage preserved (AC4):** after re-home, `internal/dispatch/dispatch_test.go` still
  asserts every kept `Route` branch — malformed, `IsV1Compatible` unsupported, `IsV1Compatible`
  unknown-type, no-handler — plus per-conn `NextID` monotonicity/isolation and `Reply`
  `InReplyTo` matching (the last two via the retained `TestRoute_*` pair + re-homed
  `TestIDCounter_MonotonicPerConn`). No new *behaviour* is tested; coverage of kept behaviour
  does not regress.
- **No cross-package test churn:** `internal/relay/handlers` was migrated off the harness by
  #1038; it and `internal/relay` (v2session tests) must stay green — they are the regression
  signal that the kept surface (`Route`, `NewConn`, `Auth`) is untouched.
- **Verification greps (expect empty in the whole tree for the *code* names):**
  after the change, `internal/dispatch/` must contain no definition or reference to any of:
  `Config`, `FirstFrameGate`, `FirstFrameOutcome`, `Dispatcher`, `connState`, `New`, `Register`,
  `Outbound`, `ActiveConns`, `Run`, `routeConn`, `runConn`, `runGate`, `handleOne`, `setAuth`,
  `runDispatcher`, `connIDs`. (Cross-package hits that remain are comments only, per §5 — AC5 is
  about *compilable* references, which are already zero.)

---

## Open questions

- **`setAuth` removal vs. AC2 keep-list** — resolved above (remove it; AC6/U1000 forces it). Flagged
  for the reviewer as a deliberate, justified deviation, not scope creep. If review disagrees, the
  only alternative is a `//lint:ignore U1000` on dead code, which is worse.
- **`frame` / `waitOrFail` fate** — staticcheck-driven, not prescribed. Most likely pruned by the
  synchronous re-home; kept only if a re-home genuinely references them. Gate: `staticcheck
  ./internal/dispatch/` clean.
