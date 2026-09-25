# #2587 — Codex approval requests through the permission modal

## Files read

- `cmd/pyry/codex_runner.go` → `codexRunner`, `runOnce`, `notify`, `Interrupt`, `BeginTeardown`, `newCodexRunnerFactory`, `codexHarness`, `codexRunnerConfig` — where `OnServerRequest` is left nil today and where every decline path (interrupt, teardown, exit) lands.
- `cmd/pyry/streamsup_runner.go` → `stdioPermissionHandler` (`handle`, `await`, `childExited`), `approvalSurfaceReport`, `streamApprovalConfig` — the Claude path this ticket mirrors: Register, surface, Await, answer, fail closed on child exit.
- `cmd/pyry/main.go` → `selectInteractiveRunner` and the `approvals` / `approvalWindow` / `approvalSurfaces` composition — one registry and one window serve every transport.
- `cmd/pyry/modal_resolve_v2.go` → `streamApprovalBridge.Surface`, `ResolveStream`, `ApprovalAnswerable` — the modal is built from `permbridge.Request` (`ToolName` becomes the prompt; `DecisionReason`, `Description`, `AlwaysAllow` ride as context); a remote answer resolves the registry by `Request.ToolUseID`, and always-allow goes through `permbridge.AllowAlways`.
- `internal/permbridge/permbridge.go` → `AlwaysAllow`, `AllowAlways`, `Verdict`, `Registry` — the offer holds only Claude rule updates; `AllowAlways` falls back to `Allow` without them.
- `internal/modalbridge/modal.go` → `alwaysAllowPayload` — the wire offer is `Offered()` + `Rules()`, so a new offer shape reaches the modal without a modalbridge change.
- `internal/codexsup/serverrequest.go` → `ServerRequest`, `serverRequestHandler`, `declineFor` — no request id exposed.
- `internal/acp/responder.go` → `Responder` — owns the id bytes, unexported.
- `internal/codexsup/testdata/capture/command_{accepted,declined}.jsonl`, `file_edit.jsonl` — `serverRequest/resolved` params are `{requestId, threadId}`; `availableDecisions` lacks `acceptForSession` on every captured command request.
- `internal/e2e/internal/fakecodex/main.go` → `turn.approval`, `server.handle`, `logTurn` — the approval marker always resolves after the answer.
- `cmd/pyry/codex_runner_test.go` → `newTestCodexRunner`, `readTurnLog`, `TestCodexRunner_ApprovalDeclined` — harness to extend.
- `internal/codexsup/capture_test.go` → `TestCaptureLive` — env gating (`PYRY_CODEX_CAPTURE_BIN`, `PYRY_CODEX_CAPTURE_HOME`) and `gpt-6-luna`/`low` the live test copies.

## Context

Codex asks approval in-band as a server-initiated JSON-RPC request and blocks the turn until answered. Today every such request takes `codexsup`'s default decline, so an operator can never approve a Codex command. This ticket parks the two supported approval kinds in the daemon-wide `permbridge.Registry` and surfaces them through the same `approvalSurfaceReport` Claude's stdio handler uses; the modal wire, the fail-closed window and the clients are unchanged.

No ADR needed: this is the same registry/surface contract with a second producer.

Size: five production files (`internal/acp/responder.go`, `internal/codexsup/serverrequest.go`, `internal/permbridge/permbridge.go`, `cmd/pyry/codex_runner.go`, `cmd/pyry/main.go`); the fake Codex binary is test support, counted with the tests. No in-flight feature branch touches these files.

## Design

### `internal/acp` — `Responder.ID() json.RawMessage`

Returns a copy of the request id bytes. The only way `codexsup` can learn the JSON-RPC id of a deferred request.

### `internal/codexsup` — `ServerRequest.ID json.RawMessage`

Set in `serverRequestHandler` from `acp.ResponderFrom(ctx).ID()`. Doc: the JSON-RPC id, which `serverRequest/resolved` names as `requestId`. Nothing else changes; the default decline stays for every unhandled request.

### `internal/permbridge` — a session-grant offer

- `AlwaysAllow` gains an unexported `session bool`.
- `SessionGrant(label string) AlwaysAllow` — an offer carrying no Claude rule updates, displayed as the single rule `label`. An empty label or one over `maxRenderedRuleBytes` yields no offer at all (the operator is never offered a grant whose scope is not shown).
- `Offered()` is true for rule updates **or** a session grant.
- `Verdict` gains `ForSession bool \`json:"-"\``.
- `AllowAlways`: with rule updates, unchanged. With no updates and a session grant, returns the plain allow shape plus `ForSession: true`. Otherwise the plain allow. `json:"-"` keeps Claude's verdict bytes identical.

### `cmd/pyry/codex_runner.go` — `codexApprovals`

The adapter lives in the runner file (keeps the change at five production files).

```go
type codexApprovals struct {
    registry *permbridge.Registry
    timeout  time.Duration
    surface  *approvalSurfaceReport

    mu      sync.Mutex
    live    map[string]*codexApproval // registry id → parked request
    changes map[string][]string       // fileChange item id → its paths
}

type codexApproval struct {
    req       *codexsup.ServerRequest
    withdrawn atomic.Bool
}
```

- `handle(req *codexsup.ServerRequest)` — `codexsup.Config.OnServerRequest`. Runs on the read loop; nothing blocks. A nil adapter / nil registry, any method other than the two approvals, unparsable params, an id-mint failure or a `Register` error → `req.Decline()`. Otherwise build the `permbridge.Request` (below), `Register` under a fresh id, store in `live`, and `go await`.
- Registry id: `"codex-" + 32 hex chars` from `crypto/rand`. Claude's ids are tool-use ids (`toolu_…`), so no collision across transports; random across sessions and callbacks. `Request.ToolUseID` is the same id, because `streamApprovalBridge` resolves the registry by `ToolUseID`.
- `await(id, ap, parked, pending)` — `retire := surface.surface(parked)`, `Await`, delete from `live`, then unless `ap.withdrawn` answer exactly once with `{"decision": codexDecision(verdict)}`, then `retire()`. Mirrors `stdioPermissionHandler.await`.
- `codexDecision(v Verdict) string` — allow + `ForSession` → `acceptForSession`; allow → `accept`; anything else → `decline`. `cancel` is never produced.
- `observe(method, params)` — called from `codexRunner.notify` before translation:
  - `item/started` with `item.type == "fileChange"` → record `changes[].path` under the item id.
  - `item/completed` → drop the item's paths.
  - `serverRequest/resolved` → for **every** live approval whose `req.ID` equals `requestId` (trimmed bytes), set `withdrawn` and `registry.Resolve(id, Deny(reasonCodexWithdrawn))`. `await` then retires the modal and writes nothing. Every match, because Codex restarts its ids per process and an already-resolved entry from a dead spawn may still sit in `live` until its `await` deletes it; resolving it again is a registry no-op.
  - The `changes` map is capped (`codexMaxTrackedChanges` = 256 items); past the cap a new item's paths are not recorded and its approval shows no paths.
- `declineAll(reason string)` — snapshot `live` ids, `registry.Resolve(id, Deny(reason))` each; the registry one-shot arbitrates against a racing answer or window. Also clears `changes`.

Request mapping (`codexApprovalRequest(method, params, paths) (permbridge.Request, bool)`, pure):

| Field | Command request | File-change request |
|---|---|---|
| `ToolName` (modal prompt) | `Codex command` | `Codex file change` |
| `Description` | `command: <command>` and `cwd: <cwd>` on two lines | `paths:` then each changed path on its own line (from `changes[itemId]`) |
| `DecisionReason` | Codex `reason` as a JSON string, when present | same |
| `Input` | the raw params | the raw params |
| `AlwaysAllow` | `SessionGrant(command)` only when `availableDecisions` lists the string `acceptForSession` | `SessionGrant(paths joined ", ")` under the same rule |

Every Codex-supplied string shown on the modal (command, cwd, each path, the rule label) passes through `codexDisplay`, which escapes non-printable runes (newlines, control and bidi-format characters) with `strconv.QuoteRune` escapes, so a command cannot forge a second `cwd:` line or a path list. `Description` is capped at `codexMaxDescription` = 4096 bytes on a rune boundary.

Wiring:

- `codexHarness` gains `approval streamApprovalConfig`; `selectInteractiveRunner` sets `codex.approval = approval` beside `codex.sink`. Only `registry`, `timeout`, `surface` are read; Codex approvals are in-band and do not depend on the Claude-only `stdio` flag.
- `codexRunnerConfig` gains `Approvals *codexApprovals`; the factory builds one per runner (`newCodexApprovals`, nil when the registry is nil).
- `runOnce` passes `OnServerRequest: r.cfg.Approvals.handle` (nil-safe method) and calls `declineAll(reasonCodexExit)` after the client is unbound.
- `Interrupt` calls `declineAll(reasonCodexInterrupt)` before `turn/interrupt`.
- `BeginTeardown` calls `declineAll(reasonCodexTeardown)`.
- Window expiry needs nothing new: the registry's timer denies, `await` answers `decline`.

Deny reasons are fixed content-free constants; they never reach Codex (Codex receives only `decline`).

## Concurrency model

- `handle` and `observe` run on the codexsup read loop and never block: registration and map ops only.
- One `await` goroutine per parked request; it ends when the registry resolves the entry, which every path guarantees (answer, window, withdraw, interrupt, teardown, exit). It is the sole writer of the answer.
- `codexApprovals.mu` is a leaf: never held across `Register`/`Resolve`/`Respond`/surface.
- `withdrawn` is set before `Resolve`, so `await` (which runs after `Resolve` delivers) always sees it. If the operator's answer wins the registry race first, Codex has already resolved the request on its side, so skipping the write is still correct.

## Error handling

- Every failure before `Register` answers `decline` via `req.Decline()`.
- `Respond` errors (dead process) are ignored, as `stdioPermissionHandler.await` ignores write errors.
- Nothing answers `accept` unless a `Resolve` with an allow verdict won the one-shot.

## Testing strategy

`internal/permbridge`: `SessionGrant` is offered with its rule; empty/oversized labels offer with no rules; `AllowAlways` with a session grant sets `ForSession` and marshals to the same bytes as `Allow`; the Claude rule path is unchanged.

`internal/codexsup`: a deferred `ServerRequest` carries the id the server sent.

`cmd/pyry` (new `codex_approval_test.go`, fake Codex, stub surface that records requests and retirements):

- allow → surfaced request shows the command and cwd; resolve `Allow` → item completes `completed`; modal retired.
- deny → item `declined`.
- window expiry (tiny timeout, nobody answers) → item `declined`, modal retired.
- withdrawn (`[fakecodex:withdraw]`) → registry entry gone, modal retired, and the fake logs no late response (checked after a following turn).
- interrupt with a parked approval → registry entry resolved, modal retired.
- table test of `codexApprovalRequest` + `codexDecision` + `codexDisplay` (a command embedding `\ncwd: /x` renders on one line): command vs file-change mapping, reason, `acceptForSession` offer present/absent, `cancel`/object decisions ignored.
- live: `TestCodexApprovalLive` — skips without `PYRY_CODEX_CAPTURE_BIN`/`PYRY_CODEX_CAPTURE_HOME`; `gpt-6-luna` at `low`; a surface that denies `declined.txt` and allows `accepted.txt`; asserts one file exists and the other does not.

`internal/e2e/internal/fakecodex`: new `[fakecodex:withdraw]` marker — sends the approval request, then `serverRequest/resolved` before any answer, completes the item `declined`; a response to a withdrawn id is logged to `FAKECODEX_TURN_LOG` as `{"lateResponse": <id>}`.

The existing `TestCodexRunner_ApprovalDeclined` keeps covering the no-registry default decline.

## Open questions

- Does `retire` from `approvalSurfaceReport` need the modal to have been surfaced? No — a no-op surface returns a no-op retire; covered by the default (no relay) wiring.

## Documentation handoff

Pending for the documentation stage: `docs/knowledge/features/codexsup-package.md` (`ServerRequest.ID`), `docs/knowledge/features/permbridge-package.md` (`SessionGrant`, `Verdict.ForSession`), `docs/knowledge/features/fakecodex-binary.md` (`[fakecodex:withdraw]`, late-response log line).

## Security review

**Verdict:** PASS (after one revision: display escaping, bounds and the no-offer rule for an unshowable grant were added before commit)

**Findings:**

- [Trust boundaries] MUST FIX (fixed in plan) — Codex params are untrusted and reach the operator's modal. A command containing a newline could forge the `cwd:` line, or a path could forge the path list, misleading the operator into approving. Every displayed string now goes through `codexDisplay`, which escapes non-printable runes; the boundary is the single pure function `codexApprovalRequest`.
- [Trust boundaries] No finding — the answer to Codex is derived only from the registry verdict in `codexDecision`; no Codex-supplied field selects the decision. `acceptForSession` needs both Codex's `availableDecisions` offer and an operator always-allow answer; `AllowAlways` without an offer is a plain allow.
- [Trust boundaries] No finding — `serverRequest/resolved` can only deny, and only this runner's own parked approvals (the adapter is per runner); a spoofed or stale `requestId` cannot produce an accept.
- [Trust boundaries] SHOULD FIX (in plan) — an always-allow offer whose rule text cannot be shown (empty or over 1024 bytes) is not offered at all, rather than offered with no visible scope (`SessionGrant`).
- [Tokens] No finding — the registry id is a correlation key minted from `crypto/rand` (128 bits) and never logged; the wire correlation remains modalbridge's own modal id.
- [File operations] No finding — no file I/O; paths are display-only.
- [Subprocess] No finding — Codex runs the command only on `accept`/`acceptForSession`, which only an explicit operator allow produces. `cancel` is never sent.
- [Crypto] No finding — `crypto/rand`; an RNG failure declines.
- [Network & I/O] SHOULD FIX (in plan) — `Description` capped at 4096 bytes; the per-runner file-change path map capped at 256 items so a Codex that never completes items cannot grow it unbounded. The number of parked approvals is bounded as it is for Claude: each lives at most one window once nobody can answer.
- [Logs] No finding — the adapter logs nothing; params, commands, paths and reasons never reach a log or an error. Deny reasons are fixed constants and are not sent to Codex.
- [Concurrency] No finding — `codexApprovals.mu` is a leaf, never held across `Register`, `Resolve`, surface or `Respond`. Every `await` goroutine exits when its registry entry resolves, and every path (answer, window, withdraw, interrupt, teardown, exit) resolves it. `withdrawn` is set before `Resolve`, so `await` observes it.
- [Threat model] No finding — the remote-answer gate (`MayAnswerPrompt`, `RemoteAnswerable`) is unchanged and applies to Codex modals exactly as to Claude's. OUT OF SCOPE: an explicit operator `cancel` and the `item/tool/requestUserInput` / legacy approval requests (the ticket defers them; they keep the default decline).

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25
