# Spec — #1056: banner the Phase-0 entry docs as historical

**Size:** XS — four short blockquote banners prepended to four existing docs. No code. No content rewritten or deleted. Edit fan-out: zero. Branch-overlap: clean (verified 2026-07-17 against all `origin/feature/*` branches; none touch these four files).

Status: draft (architect)

## Files to read first

Docs-only ticket — codegraph does not index markdown, so this list is Read/grep-derived, not `codegraph_context`-derived.

- `docs/architecture.md:1-3` — current H1 + intro; the banner goes *above* line 1, content stays verbatim.
- `docs/protocol.md:1-14` — current H1 + "Transport" preamble; confirms this doc is the **local control-socket** surface (Unix socket, `0600`), covering `status`/`stop`/`logs`/`attach`. Banner goes above line 1.
- `docs/plan.md:1-4` — current H1; line 3 already notes the authoritative working plan lives in the Obsidian vault. Banner goes above line 1.
- `docs/multi-session.md:1-5` — current H1 + Phase-1 pool-design intro. Banner goes above line 1.
- `docs/knowledge/architecture/system-overview.md:75` — `internal/control/ — Control-plane server (Unix socket, JSON)`; confirms the successor genuinely carries current control-plane authority, so the redirect is not itself misleading.
- `docs/knowledge/architecture/system-overview.md:190` — `### Session Registry (Phase 1.2a)`; the section the `multi-session.md` banner points readers to.
- `docs/knowledge/architecture/system-overview.md:317` — `## Beyond Phase 0 — landed work and roadmap`; the section the `plan.md` banner points readers to.
- `docs/protocol-mobile.md:1-3` — `# Mobile wire protocol — v2`; the *separate* mobile wire surface. It is a "see also" cross-reference for `protocol.md`, **not** the successor. (Its own intro already frames the control socket as "a separate concern" — mirror that framing.)
- `docs/specs/README.md` (as edited by #1055, commit `a42b0fd`) — the house-style precedent for a "point-in-time, not current-state authority" banner. Same reviewer, same pattern, merged immediately before this ticket.

## Context

The 2026-07-15 docs review flagged four top-level Phase-0 entry docs as unmarked historical layers sitting beside maintained docs with equal apparent authority. A reader landing on one is misled into treating an early design doc as the current-state reference.

The fix is a single historical banner at the very top of each of the four docs, pointing at the maintained authority. The four docs are **historical / point-in-time**, *not superseded* — the subsystems they describe are still live. In particular the local control socket is live and has merely **grown** past the four verbs `protocol.md` documents. The banner wording must say "grown," not "superseded" (see § Design → wording constraints). This distinction is the whole reason the ticket exists: an over-strong "superseded" banner would mislead in the opposite direction.

## Design

### Banner shape (contract)

Mirror the #1055 precedent exactly:

- A Markdown blockquote (`> …`) is the **first content in the file**, occupying line 1 onward.
- One blank line separates the banner from the original first line (the H1).
- All original content — including the original `# Title` H1 — is preserved **verbatim** beneath the banner. Nothing is rewritten, reordered, or deleted.
- Banner is bold-led (`> **Historical — …**`) so it reads as an admonition, consistent with #1055's `> **Superseded by …**`.

### Wording constraints (why, not just what)

- Say **historical / point-in-time design record**, not "superseded" or "obsolete." The docs are snapshots of a still-live system.
- The `protocol.md` banner has two extra required clauses (per AC 2):
  1. It captures **only** the original `status` / `stop` / `logs` / `attach` control-socket verbs.
  2. It is **not** superseded by the mobile protocol — that is a *separate* wire surface. `protocol-mobile.md` is a "see also," not the successor.
- Every banner links `docs/knowledge/architecture/system-overview.md` as the primary current-authority successor (AC 2).

### Link paths (verified relative to each source file's location under `docs/`)

| From | To `system-overview.md` | To `protocol-mobile.md` |
|---|---|---|
| `docs/architecture.md` | `knowledge/architecture/system-overview.md` | — |
| `docs/protocol.md` | `knowledge/architecture/system-overview.md` | `protocol-mobile.md` |
| `docs/plan.md` | `knowledge/architecture/system-overview.md` | — |
| `docs/multi-session.md` | `knowledge/architecture/system-overview.md` | — |

### Exact banner text (the deliverable)

For a docs banner the wording *is* the interface, so the four blocks below are the contract — prepend each verbatim, then a blank line, then the file's existing content. Section names in the redirects (`"Beyond Phase 0 — landed work and roadmap"`, `"Session Registry"`) match live headings in `system-overview.md` (lines 317, 190) and are quoted so a reader can `grep` to them even if the anchor slug drifts.

**`docs/architecture.md`:**

```
> **Historical — Phase-0 design record.** This is a point-in-time tour of the
> early internal layout, not current-state authority. For how the system is
> structured today see [`knowledge/architecture/system-overview.md`](knowledge/architecture/system-overview.md).
```

**`docs/protocol.md`:**

```
> **Historical — Phase-0 snapshot.** This documents the local **control-socket**
> protocol as of Phase 0, covering only the original `status` / `stop` / `logs` /
> `attach` verbs. The control socket is still live but has since grown session,
> rekey, and relay verbs, so this doc is no longer current-state authority — and
> it is **not** superseded by the mobile protocol, which is a separate wire
> surface. For current control-plane authority see
> [`knowledge/architecture/system-overview.md`](knowledge/architecture/system-overview.md);
> for the mobile wire protocol see [`protocol-mobile.md`](protocol-mobile.md).
```

**`docs/plan.md`:**

```
> **Historical — Phase-0 release/roadmap record.** This is a point-in-time
> snapshot; the authoritative working plan lives in the Obsidian vault (noted
> below). For the current landed-work and roadmap summary see the "Beyond
> Phase 0 — landed work and roadmap" section of
> [`knowledge/architecture/system-overview.md`](knowledge/architecture/system-overview.md).
```

**`docs/multi-session.md`:**

```
> **Historical — Phase-1 design record.** This records the original multi-session
> pool design; it is a point-in-time snapshot, not current-state authority. For
> how the session pool / registry works today see the "Session Registry" and
> related sections of
> [`knowledge/architecture/system-overview.md`](knowledge/architecture/system-overview.md).
```

## Concurrency model

N/A — docs-only, no runtime surface.

## Error handling

N/A — docs-only, no runtime surface.

## Testing strategy

No Go tests (docs-only; the developer must not add any). Verification is by inspection + link integrity:

- **Banner present & first:** each of the four files begins with the `> **Historical …**` blockquote, then a blank line, then the original H1. `head -1 docs/{architecture,protocol,plan,multi-session}.md` shows the blockquote.
- **Content preserved verbatim:** `git diff` shows only *added* lines at the top of each file — zero deletions, zero modifications to pre-existing lines. Confirm with `git diff --stat` (four files, only insertions) and eyeball each diff hunk starts at line 1 with `+` lines only.
- **Links resolve:** from each source file's directory, the linked relative paths exist —
  - `docs/knowledge/architecture/system-overview.md` (target of all four),
  - `docs/protocol-mobile.md` (extra target of `protocol.md`).
- **`protocol.md` banner completeness (AC 2):** its banner names the four verbs `status`/`stop`/`logs`/`attach`, states "not … superseded by the mobile protocol," and cross-references `protocol-mobile.md`.
- **Reindex:** run `qmd update && qmd embed` after editing so the banners are indexed (CLAUDE.md docs workflow). `update` first — `embed` alone won't pick up changed content.

## Acceptance criteria mapping

- AC1 (banner at very top of all four) → § Design → Banner shape; each block is line-1 content.
- AC2 (link `system-overview.md`; `protocol.md` clarifies verb scope + not-mobile-superseded + cross-ref `protocol-mobile.md`) → § Design → Wording constraints + the four exact banner blocks.
- AC3 (original content preserved verbatim beneath the banner) → § Design → Banner shape ("preserved verbatim … Nothing is rewritten, reordered, or deleted") + § Testing → content-preserved check.

## Open questions

None. Successor doc, its live section headings (control-plane line 75, Session Registry line 190, Beyond Phase 0 line 317), the mobile cross-reference target, and the absence of any pre-existing banner on the four docs were all verified 2026-07-17. Not security-sensitive (docs-only; no design surface, no trust boundary).
