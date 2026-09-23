# #1538 — control-plane and sessions feature docs still document the deleted attach/resize surface

Short plan: the change is docs-only. The builder role may not edit `docs/knowledge/`,
so every edit is carried forward as the Documentation handoff below, following the
#1551 and #1561 precedent (plan-only PR, documentation stage applies it). This plan is
the only file the builder branch adds; the documentation stage's own commit must touch
only the eight documents the ticket names.

## Files read

- `docs/knowledge/features/control-plane.md` → the four `## Attach:` sections, `## Resolver Seam`
  (+ `### Why a single resolver instead of \`StateProvider\` + \`AttachProvider\``),
  `## Sessions: removal seam (1.1d-B1)` (one forward-looking "attach orchestration" phrase),
  `## Process-Global vs Per-Session`, `## Testing`, `## Sections`. The opening paragraph,
  `## Handshake Deadline` and `## Lifecycle` already name `VerbAttach`/`handleAttach`/
  `supervisor.Bridge` only as past-tense history — leave them.
- The six sub-documents. H1s of the three ADR-linked ones, verbatim:
  `# Attach: CLI Surface (1.1e-D)`, `# Attach: stdio mode (1.3a)`, `# Attach: --create-if-missing (1.3b)`.
- ADR links (must keep resolving; ADRs do not change):
  ADR 010 → `control-plane-attach-cli-surface-1-1e-d.md#attach-cli-surface-11e-d`;
  ADR 012 → `control-plane-attach-stdio-mode-1-3a.md#attach-stdio-mode-13a`;
  ADR 014 → `control-plane-attach-create-if-missing-1-3b.md#attach---create-if-missing-13b`.
  The other three sub-documents have no inbound link outside `control-plane.md § Sections`
  and frozen `docs/specs/` history (`git grep` over `docs cmd internal CLAUDE.md`, specs excluded).
- `docs/knowledge/features/sessions-package.md` → `## Errors` (table + sentinel paragraph).
- `internal/control/server.go` → `Session` (`State() sessions.State`, `Activate(ctx) error`),
  `SessionResolver` (`Lookup`, `ResolveID`), `Server.handle` and `handleSessionsHasID` (the only
  `s.sessions.Lookup` callers; nothing in `internal/control` calls `ResolveID` or `Activate` today).
- `cmd/pyry/main.go` → `poolResolver` (`Lookup`, `ResolveID` passthroughs).
- `internal/control/server_test.go` → `fakeSession`, `fakeResolver`, `recordingResolver`.
- `internal/control/logs.go` → `LogProvider`.
- `internal/sessions/pool.go` → `New` (wraps `NewID` failure as `sessions: generate bootstrap id`),
  `Pool.Run`, `Pool.supervise`, `Pool.saveLocked`, `Pool.ResolveID`; `internal/sessions/session.go` →
  `Session.Run`, `Session.Evict`.

Non-resolving (confirmed absent from the tree): `ErrAttachUnavailable`, `ErrBridgeBusy`,
`Session.Attach`, `handleAttach` (only `handleAttachFile`, a different verb), `startWinsizeWatcher`,
`control.Attach`, `control.AttachStdio`, `internal/control/attach_client.go`,
`internal/control/attach_test.go`, `internal/control/attach_resolve_test.go`, the package
`internal/supervisor`, and the wrap string `sessions: bootstrap supervisor`. `runAttach` survives
only in two Go comments (`cmd/pyry/args_test.go`, `cmd/pyry/main.go`), not as a declaration.

## Change

None under `cmd/` or `internal/`. The edits below are the whole ticket. No in-flight
`origin/feature/*` branch touches any of the eight target files.

## Documentation handoff (pending — documentation stage)

Find each site by heading or quoted phrase, not by line number. All paths are under
`docs/knowledge/features/`.

1. **`control-plane.md` — delete four sections** with their bodies:
   `## Attach: ResolveID-then-Lookup (1.1e-C)`, `## Attach: Foreground-mode Wire String`,
   `## Attach: Handshake Geometry (#136)`, `## Attach: Activate-before-bind (1.2c-A)`.
   No replacement text.
2. **`control-plane.md` `## Resolver Seam`:**
   - Replace the code block with the live declarations from `internal/control/server.go`:
     `Session { State() sessions.State; Activate(ctx context.Context) error }` and
     `SessionResolver { Lookup(id sessions.SessionID) (Session, error); ResolveID(arg string) (sessions.SessionID, error) }`.
     Drop the `supervisor.State` return, the `Attach` method and the "handleAttach wraps them" comment.
   - In the paragraph beginning "The empty-id-resolves-to-default convention", replace the
     `handleAttach` sentence (and its link to the deleted anchor) with a statement of today's
     callers: `Server.handle` calls `Lookup("")` for `status`, and `handleSessionsHasID` calls
     `Lookup` with the payload id; no handler in `internal/control` calls `ResolveID` or
     `Activate` since #1348 removed attach. One sentence noting attach and resize were removed in
     #1348 is enough.
   - Keep `### Why a single resolver instead of \`StateProvider\` + \`AttachProvider\`` and its
     explanation (the Phase-0 names there are history, stated in past tense). Replace the code
     block's `// use sess.State() or sess.Attach(...)` line with `// use sess.State()`, or drop the
     block — the ticket asks for the `sess.Attach(...)` example to go.
3. **`control-plane.md` `## Sessions: removal seam (1.1d-B1)`:** in "Phase 1.1b/c/e (`list`,
   `rename`, `attach` orchestration) will continue this pattern", drop the `attach` orchestration item.
4. **`control-plane.md` `## Process-Global vs Per-Session`:** delete the `attach` stream and
   `resize` (live) rows. Change the following sentence so it names no "upcoming `pyry attach <id>`"
   (e.g. "`pyry sessions new` (#76) and `pyry sessions list` extend the per-session column.").
5. **`control-plane.md` `## Testing`:** remove `attach_test.go` and `attach_resolve_test.go` from
   the file list, and delete the paragraph beginning "`attach_resolve_test.go` covers the 1.1e-C
   surface". Keep the `fakeResolver` + `fakeSession` + `recordingResolver` sentence, but drop its
   "resolve-then-lookup ordering" clause (that ordering was `handleAttach`'s).
6. **`control-plane.md` `## Sections`:** remove the six bullets linking the six sub-documents
   (Attach: CLI Surface, Attach: stdio mode, Attach: --create-if-missing, Resize: Live Wire Message
   and Applier, Resize: Live SIGWINCH Watcher, Foreground binary auto-attach). No replacement.
7. **Delete** `control-plane-resize-live-wire-message-and-applier.md`,
   `control-plane-resize-live-sigwinch-watcher.md` and
   `control-plane-foreground-binary-auto-attach-1-3c-2.md`.
8. **Stub the three ADR-linked documents.** Keep the path and the H1 line byte-identical; replace
   the whole body with a short past-tense note, for example for the CLI-surface document:

   > This section described `pyry attach`'s client-side surface. #1348 removed attach and resize
   > from the daemon and CLI; the design reasoning survives in
   > [ADR 010](../decisions/010-sessions-cli-sub-router.md).

   Likewise `control-plane-attach-stdio-mode-1-3a.md` → ADR 012
   (`../decisions/012-attach-stdio-flag-vs-verb.md`) and
   `control-plane-attach-create-if-missing-1-3b.md` → ADR 014
   (`../decisions/014-get-or-create-take-or-create.md`; note `Pool.GetOrCreate` still exists,
   see `sessions-package-key-types-pool-getorcreate-1-3b.md`). Name no deleted Go symbol as live.
9. **`sessions-package.md` `## Errors`:**
   - Delete the rows `Session.Attach` with nil bridge and `Session.Attach` while bridge busy.
   - Delete the `supervisor.New` failure row: `internal/supervisor` is gone and the wrap string
     `sessions: bootstrap supervisor` is not in the tree (every symbol in an edited section must
     resolve). The `NewID` row stays (`New` wraps it as `sessions: generate bootstrap id`).
   - Sentinel paragraph: remove `ErrAttachUnavailable` from the list and delete the
     "`supervisor.ErrBridgeBusy` stays in `internal/supervisor`" sentence.
   - The `Session.Run` / `Pool.Run` row's "from the supervisor" may read "from the runner"; the
     other stale `internal/supervisor` prose in that file (intro, Package Layout, Dependency
     Direction, Production Consumers) is outside this ticket — leave it.
10. **Leave unmodified:** every file under `docs/knowledge/decisions/`, `INDEX.md`, `CATALOG.md`,
    every `docs/knowledge/codebase/*.md`, every `docs/specs/` file.
11. **Done when:** `git diff --name-only` of the documentation-stage commit lists only the eight
    documents (three of them as deletions); `make docs-guard` is green; the sweep in the PR body
    re-run on the result shows no live-tense hit for the deleted symbols in `control-plane.md` /
    `sessions-package.md`, and the resolving control `git grep -n 'type SessionResolver interface'
    internal/control` returns `server.go`. Then `qmd update && qmd embed`.

## Testing strategy

No code changes, so no new test. The dispatcher's `make check` stays green; its
`make docs-guard` covers the documentation stage's edits.
