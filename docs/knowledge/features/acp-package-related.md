# Related

- Per-ticket records: [`codebase/755.md`](../codebase/755.md) (transport floor),
  [`codebase/756.md`](../codebase/756.md) (`pyry acp` subcommand),
  [`codebase/757.md`](../codebase/757.md) (outbound-request primitive),
  [`codebase/761.md`](../codebase/761.md) (`session/new` + embedded pool),
  [`codebase/762.md`](../codebase/762.md) (`session/load` + `session/cancel` lifecycle),
  [`codebase/747.md`](../codebase/747.md) (`initialize` + `authenticate` handshake),
  [`codebase/750.md`](../codebase/750.md) (`Transport.Notify` + outbound streaming adapter),
  [`codebase/753.md`](../codebase/753.md) (`session/cancel` actuation + mode/config pin),
  [`codebase/752.md`](../codebase/752.md) (permission proxy via `session/request_permission`),
  [`codebase/749.md`](../codebase/749.md) (`session/prompt` — content blocks + held call),
  [`codebase/796.md`](../codebase/796.md) (producer wiring), [`codebase/751.md`](../codebase/751.md)
  (`stopReason` on `TurnEnd`), [`codebase/801.md`](../codebase/801.md) (permission-proxy wiring),
  [`codebase/754.md`](../codebase/754.md) (conformance capstone)
- Specs: [`specs/architecture/755-acp-transport.md`](../../specs/architecture/755-acp-transport.md),
  [`specs/architecture/756-acp-subcommand.md`](../../specs/architecture/756-acp-subcommand.md),
  [`specs/architecture/757-acp-outbound-request.md`](../../specs/architecture/757-acp-outbound-request.md),
  [`specs/architecture/761-acp-session-new-embedded-pool.md`](../../specs/architecture/761-acp-session-new-embedded-pool.md),
  [`specs/architecture/762-acp-session-load-cancel.md`](../../specs/architecture/762-acp-session-load-cancel.md),
  [`specs/architecture/747-acp-initialize-handshake.md`](../../specs/architecture/747-acp-initialize-handshake.md),
  [`specs/architecture/750-acp-outbound-streaming-adapter.md`](../../specs/architecture/750-acp-outbound-streaming-adapter.md),
  [`specs/architecture/753-acp-cancel-mode-config.md`](../../specs/architecture/753-acp-cancel-mode-config.md),
  [`specs/architecture/752-acp-permission-proxy.md`](../../specs/architecture/752-acp-permission-proxy.md),
  [`specs/architecture/749-acp-session-prompt.md`](../../specs/architecture/749-acp-session-prompt.md),
  [`specs/architecture/754-acp-conformance-generic-shape.md`](../../specs/architecture/754-acp-conformance-generic-shape.md)
- Outbound mapping layer (the pure `MapUpdate` the streaming adapter consumes):
  [`features/acpbridge-package.md`](acpbridge-package.md) (#769),
  [ADR 027](../decisions/027-acp-mapping.md) (§ Outbound table, divergences 1 & 3).
- Cancel abort keystroke (the seam `resolveCancelTarget` exposes) — **built** by
  T9 [#753](https://github.com/pyrycode/pyrycode/issues/753): `cancelSessionHandler`
  actuates `SendEsc` best-effort. The neutral `Cancel` command from
  [`turnevent-package.md`](turnevent-package.md) (#707) is declared vocabulary and
  deliberately **not** constructed — the handler calls the concrete lever directly.
- Embedded-pool design: [ADR 026](../decisions/026-embedded-acp-pool-exact-one-claude.md)
  (`BootstrapEvicted` over adopting `Default()`; the `Ready()` correctness gate),
  [`features/sessions-package.md`](sessions-package.md#configbootstrapevicted--poolready-761)
- Dispatch-table precedent: [`features/dispatch-package.md`](dispatch-package.md)
  (`Register`-before-`Run` `atomic.Bool` gate, carrier-agnostic handler table).
  `internal/acp` mirrors the shape but defines its own JSON-RPC wire types —
  it does **not** import `internal/protocol`.
- Stderr-diagnostics convention: `internal/control` `SlogTee` / `NewRingBuffer`
  (used by `runSupervisor`). The `pyry acp` subcommand (#756) deliberately does
  **not** reuse it — the ACP host captures this subprocess's stderr and keeps its
  own ring, so a plain `slog.NewTextHandler(os.Stderr, …)` suffices.
- No-content-logging discipline: [`features/jsonl-reader.md`](jsonl-reader.md)
  (`internal/agentrun/jsonl` — offsets and error kinds only, never line bytes).
- Epic: [#600](https://github.com/pyrycode/pyrycode/issues/600) `pyry acp`. The
  neutral daemon-owned turn model the future ACP method adapter maps onto —
  [`features/turnevent-package.md`](turnevent-package.md).
