# #1561 — system-overview § Key Types documents the deleted supervisor API

Short plan: the change is docs-only. The builder role may not edit `docs/knowledge/`,
so every edit is carried forward as the Documentation handoff below, following the
#1551 precedent (plan-only PR, documentation stage applies it). The plan file is the
only file this branch adds.

## Files read

- `docs/knowledge/architecture/system-overview.md` → `## Key Types` (four backticked
  `### \`supervisor.…\`` subsections, up to `## Platform Support`) and `### Backoff Strategy`
  (already attributes `backoffTimer` to `internal/streamsup/backoff.go`).
- `docs/knowledge/features/streamsup-package.md` → `## Public API` — the runner's API; the
  replacement points here.
- `docs/knowledge/features/streamsup-package-buildargs-the-id-flag-inversion-that-keeps.md`
  → `buildArgs`, `useCreateForm` — where the live per-spawn `--session-id`/`--resume` decision
  is documented; the new target for `jsonl-reconciliation.md`'s link.
- `docs/knowledge/features/jsonl-reconciliation.md` → opening paragraph's
  `system-overview.md § \`supervisor.Config\`` link.
- `docs/knowledge/features/streamsup-package-related.md` → the `internal/supervisor`'s
  `backoffTimer`/`Run` bullet.
- `internal/streamsup/runner.go` → `Runner`; `internal/streamsup/backoff.go` → `backoffTimer`;
  `internal/sessions/runnerstate.go` → `RunnerConfig` (doc comment records moved vs. dropped
  fields); `internal/sessions/runner.go` → `RunnerFactory`; `internal/sessions/pool.go` →
  `Config.RunnerFactory`. All resolve.
- `cmd/pyry/main.go` → the `Pending` comment in the msgqueue wiring (records that
  `ErrTrustModalPending` has had no producer since #1348).
- Decision homes: `msgqueue-package.md` § "Bounded give-up on persistent delivery failure (#1000)"
  and § Files; `modalbridge-package.md` § Related and the #993 capstone bullet;
  `fakeclaude-binary-on-turn-transcript-growth.md`;
  `v2-session-manager-state-machine-reconnect-replay-hello-last-event-id-rin.md`;
  `v2-session-manager-state-machine-inbound-interrupt-interrupter-seam-esc.md`;
  `sessions-package-status.md`; `sessions-package-key-types-pool-updatesettings.md`;
  `protocol-package-screen-snapshot-payloads.md`; `v2-session-manager-related.md`;
  ADRs 007, 008, 025, 031, 032, 033.

## Change

None under `cmd/` or `internal/`. The three documentation edits below are the whole ticket.
No overlap with any in-flight `origin/feature/*` branch on the three target files.

## Documentation handoff (pending — documentation stage)

Find each site by the quoted phrase, not by line number.

1. **`architecture/system-overview.md`, `## Key Types`:** delete all four subsections —
   ``### `supervisor.Config` ``, ``### `supervisor.Bridge` ``, ``### `supervisor.Supervisor` ``
   and ``### `supervisor.backoffTimer` `` — with their bodies, up to `## Platform Support`.
   Drop the backoff subsection outright; do not re-attribute it. Replace the section body with
   a short pointer, for example:

   > The runner that supervises each claude child is `streamsup.Runner`
   > (`internal/streamsup/runner.go`). Its configuration, supervise loop and `sessions.Runner`
   > seam are documented in
   > [features/streamsup-package.md § Public API](../features/streamsup-package.md). The pool
   > builds each runner through `sessions.Config.RunnerFactory`, handing it a
   > `sessions.RunnerConfig` (`internal/sessions/runnerstate.go`), whose doc comment records
   > which fields of the PTY supervisor's configuration moved to the stream path and which were
   > dropped when #1348 deleted that package. The restart ladder is § Backoff Strategy above.

   Constraints: name no field, method or internal state of the deleted types as live; name no
   deleted Go symbol at all (every Go symbol named must resolve via `git grep`); do not call
   `backoffTimer` a copy of, duplicated from, or byte-identical to anything in
   `internal/supervisor`. Leave § Backoff Strategy's own stale "verbatim copy" wording alone —
   out of scope.
2. **`features/jsonl-reconciliation.md`, opening paragraph:** replace only the link
   `[system-overview.md § \`supervisor.Config\`](../architecture/system-overview.md)` with
   `[streamsup-package-buildargs-the-id-flag-inversion-that-keeps.md](streamsup-package-buildargs-the-id-flag-inversion-that-keeps.md)`
   (the live id-flag decision). Leave the surrounding sentence, including "on every start and
   restart", unchanged.
3. **`features/streamsup-package-related.md`, the bullet beginning
   "`internal/supervisor`'s `backoffTimer`/`Run`":** rewrite it to point at this package's own
   ladder, e.g. "`backoffTimer` in `internal/streamsup/backoff.go` — the
   exponential-backoff-with-stability-reset ladder `Run` applies between crashes; see
   [system-overview.md § Backoff Strategy](../architecture/system-overview.md)." No copy /
   duplicate / verbatim framing. Leave every other `internal/supervisor` mention in the
   streamsup docs alone.
4. **Leave unmodified:** `INDEX.md`, `CATALOG.md`, every `docs/knowledge/codebase/*.md`, every
   ADR, every `docs/specs/` file (they cite these subsections as history).
5. **Done when** `git grep -n '### \`supervisor\.' docs/knowledge/architecture/system-overview.md`
   returns nothing, the resolving control `git grep -n 'type Runner struct' internal/streamsup`
   returns `runner.go`, each Go symbol in the new text resolves, and the docs-stage commit
   touches only the three files above (plus `qmd update && qmd embed`).

## Where the removed decisions still live

| Removed paragraph (decision) | Surviving home |
|---|---|
| #312 conversation cursor (`CurrentConversation`) | `features/v2-session-manager-state-machine-reconnect-replay-hello-last-event-id-rin.md`: "the cursor is the `cmd/pyry` active-conversation signal (`active.CurrentConversation`), not `sup.CurrentConversation` (the #312 bootstrap cursor)". The validator half (`ValidateConversation`) lives in `RunnerConfig`'s doc comment as dropped. |
| #594 reliable delivery / no silent drop | `features/msgqueue-package.md`: the drain "delivers asynchronously through the same #594 reliable" path, and § Related's `WriteUserTurn` bullet. |
| #668 growth-mode commit-confirm | `features/fakeclaude-binary-on-turn-transcript-growth.md`, opening: "#668 made the supervised-bootstrap delivery path confirm a turn by observing the resolved claude session JSONL **grow**". Describes the deleted path; no evergreen doc states the stream path's equivalent. |
| #726 safe-answer seam (`AcceptTrust`/`Answer`/`SendEsc`) | `features/modalbridge-package.md` § Related: "the **inbound actuator** half: the supervisor's `AcceptTrust`/`Answer`/`SendEsc` safe-answer seam"; `features/v2-session-manager-state-machine-inbound-interrupt-interrupter-seam-esc.md` (`SendEsc` (#726)). |
| #830 `StartNewSession` (`/clear` keystroke) | `features/v2-session-manager-related.md`: the `codebase/831.md` bullet, "reusing the #830 `StartNewSession` surface". |
| #842 live `Restart` | `features/sessions-package-key-types-pool-updatesettings.md`: "**Live-apply on a real change (#842, #1581).**"; ADR 031. |
| #994 settings-warning auto-continue | No surviving home. `detectSettingsWarning` has no Go reference left; only frozen `codebase/994.md` records it. |
| #1013 `ErrTrustModalPending` | `features/modalbridge-package.md`, #993 capstone bullet ("Rides #1013 and #1014 wholesale … the turn genuinely never delivered"); `cmd/pyry/main.go`'s `Pending` wiring comment: "`supervisor.ErrTrustModalPending` has had no producer since". |
| #1014 `msgqueue` give-up exemption | `features/msgqueue-package.md` § "Bounded give-up on persistent delivery failure (#1000)": "`Config.Pending` was left unset from #1348 (which deleted the only producer of `#1014`'s `supervisor.ErrTrustModalPending`) until the stream path needed the same exemption"; `PendingFunc`'s Go doc comment in `internal/msgqueue`. |
| #839/#1164 per-spawn session-id / resume resolution | `features/streamsup-package-buildargs-the-id-flag-inversion-that-keeps.md` (`useCreateForm`, #1630); `features/sessions-package-status.md` #1164 bullet; ADR 032. |
| #1165 self-heal | `RunnerConfig` doc comment (dropped, no stream equivalent); system-overview § "Fast-crash self-heal"; ADR 033. |
| #593/#595 bridge iteration boundaries, resize seam, two-heads | ADRs 007, 008, 025. No evergreen feature doc (the bridge is gone). |
| #618 `ScreenSnapshot` | `features/protocol-package-screen-snapshot-payloads.md` ("#618 wired the interception"). |

## Testing strategy

No code changes, so no new test. The dispatcher's `make check` stays green; its
`make docs-guard` covers the documentation stage's edits.
