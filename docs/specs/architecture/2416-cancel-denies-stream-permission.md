# 2416 — `modal_cancel` denies a parked stream permission

One actuation arm added to an existing method. No new type, no new state, no new
failure mode, so this is the short plan shape.

## Files read

- `cmd/pyry/modal_resolve_v2.go` → `modalResolverV2.ResolveCancel` — the method that
  changes; `ResolveAnswerWithAlwaysAllow` — the two-arm actuation shape this copies;
  `streamApprovalBridge.ResolveStream` — the verdict arm being called, and its
  `handled` contract; `reasonRemoteDeny` — the fixed content-free deny message;
  `streamApprovalBridge.retire` — the sole correlation deleter, so the cancel path
  must not delete anything itself.
- `cmd/pyry/relay.go` → the `modalResolver.streamApprovals = bridge` wiring — the one
  production site that makes the arm live; nil everywhere else (v1/foreground/tests).
- `cmd/pyry/stream_approval_test.go` → `TestModalResolverV2_Answer_StreamDeny`,
  `TestModalResolverV2_Answer_NonStreamRoutesKeystroke` — the hermetic pattern the two
  new tests mirror (`parkApproval`, `lastModalShown`, `fakeKeystroker`, `auditLogger`).
- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go` →
  `TestInteractiveStreamStdioAlwaysAllowIsSessionScoped` — the sibling the AC names,
  and `startStdioModalResolutionHarness`, which is the stdio-permission-prompt harness
  the new live test needs.
- `internal/e2e/realclaude/harness_modal_test.go` → `raiseRealPermissionModalPayload`,
  `drainForControlEvent`, `modalDismissBudget`, `writeFileTrigger` — the raise/drain
  scaffold and the existing cancel-dismissal budget.
- `docs/knowledge/features/e2e-realclaude.md` — the tier's rules: the live suite is
  `make e2e-realclaude` / `make preship` only, `make check` cannot see it, and a
  green exit code proves nothing without counting `=== RUN` lines.

## Change

`ResolveCancel` currently consumes the modal, routes `kb.SendEsc()`, audits
`cancelled` and returns the dismissal. On the stream-json path ESC reaches a
`noopKeystroker`, so nothing tells claude anything and its parked completer sits
until `permbridge`'s own timer fires at `mcpApprovalTimeout` (10 minutes).

Replace the single ESC block with the same two-arm actuation
`ResolveAnswerWithAlwaysAllow` already uses:

```go
handled := r.streamApprovals != nil &&
    r.streamApprovals.ResolveStream(modalID, false, false, reasonRemoteDeny)
if !handled {
    // ... the existing best-effort SendEsc + Warn, unchanged
}
```

Placed exactly where the ESC block is today — after the `r.reg.Resolve(modalID)`
idempotency gate, before the audit — so the consume stays the single one-shot, the
`cancelled` audit outcome and the `{cancelled, remote}` dismissal are untouched, and
a failed actuation is still tolerated rather than orphaning a consumed modal.

`handled` is true only for an id `streamApprovalBridge.Surface` put in `byModal`, i.e.
a stream-json permission. A PTY-path modal is recorded by the modal emitter and never
enters that map, so it takes the `!handled` arm and its ESC is byte-identical to today
— that is what keeps terminal-path cancels unchanged. Nothing else moves: the
correlation is not deleted here (`retire` is the sole deleter) and the modalbridge
entry was already consumed above, so a replayed cancel never reaches `ResolveStream`
a second time.

Two deliberate non-changes, both of which look like omissions:

- **No `RemoteAnswerable` gate**, unlike the answer path. That gate exists because an
  interaction-required permission cannot be *allowed* by a one-tap remote answer; a
  cancel is only ever a deny, which is the fail-closed direction, and gating it would
  leave exactly the parked-for-ten-minutes state this ticket removes.
- **No per-device privilege gate.** `ResolveCancel` has never applied
  `MayAnswerRemotePermission`, and this adds no privilege: on the terminal path a
  cancel's ESC is already a deny to claude, so the stream path is being brought level
  with it rather than granted something new. `reasonRemoteDeny` is a compile-time
  constant, so no client- or host-authored byte reaches claude.

## Testing strategy

RED→GREEN offline, plus the live gate the AC names.

- `cmd/pyry/stream_approval_test.go`, mirroring `TestModalResolverV2_Answer_StreamDeny`:
  surface a parked approval through the bridge, `ResolveCancel` it, assert the
  completer's verdict is `BehaviorDeny` with `reasonRemoteDeny`, no keystroke routed,
  one `cancelled` audit record, dismissal `{cancelled, remote}`. Red on main at the
  verdict assertion — `Await` blocks until permbridge's own timeout.
- A sibling in the same file for the `!handled` arm: a modal recorded directly into
  modalbridge (the PTY path) with a bridge wired still routes ESC and resolves no
  completer. This is the "terminal-path cancels unchanged" half of AC-1.
- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go`, beside
  `TestInteractiveStreamStdioAlwaysAllowIsSessionScoped` (AC-2): raise a real stdio
  permission modal, send `modal_cancel`, assert the `modal_dismissed{cancelled,remote}`
  broadcast, then require `turn_end` within a budget far below the 10-minute approval
  timeout, and assert the gated Write never happened. Red on main by deadline.

## Documentation handoff

Pending for the documentation stage — **not** done in this ticket.

- `docs/protocol-mobile.md` § `modal_cancel` (and § Modal's lifecycle paragraph):
  record that on the stream-json path a `modal_cancel` now resolves claude's parked
  permission to deny immediately, with the same fixed reason a remote reject answer
  carries, instead of leaving it parked until the approval window elapses. The section
  today describes only the frame's fields and says nothing about actuation, so this is
  an addition rather than a contradiction to correct.

## Open questions

None. The refiner's evidence comment named the fix shape and the code read confirms
it; the only judgement made here is the `if !handled` placement of the ESC, argued
above.
