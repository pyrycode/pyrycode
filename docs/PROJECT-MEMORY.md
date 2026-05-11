# Project Memory — Pyrycode

Index of where things live. **READ-ONLY FOR AGENTS** (as of 2026-05-11). All five pipeline agents have explicit "Never Update docs/PROJECT-MEMORY.md" rules in their CLAUDE.md. Humans maintain this file directly. Per-ticket content (implementation, patterns, lessons) goes in [`docs/knowledge/codebase/<N>.md`](knowledge/codebase/).

## Where things live

- `docs/knowledge/codebase/<N>.md` — **per-ticket implementation summary + patterns established + lessons learned.** One file per ticket. Directory listing IS the index.
- `docs/knowledge/features/` — evergreen feature docs (created by architect when a ticket warrants cross-cutting prose).
- `docs/knowledge/decisions/` — ADRs, numbered sequentially.
- `docs/knowledge/architecture/` — system-level design (e.g. `system-overview.md`).
- `docs/knowledge/INDEX.md` — one-line summaries of `features/`, `decisions/`, `architecture/`. **Documentation phase is the sole writer.**
- `docs/lessons.md` — **frozen 2026-05-11.** Historical reference. New lessons go in `docs/knowledge/codebase/<N>.md`.
- `docs/specs/architecture/<ticket>-<slug>.md` — architect specs (one per ticket).
- `docs/archive/PROJECT-MEMORY-history-2026-05-11.md` — pre-2026-05-11 "What's Built" + "Patterns Established" content store, archived in place so existing cross-references still resolve.

## Project-level conventions (human-maintained)

These are stable rules that span tickets. New entries land here only when a convention is genuinely cross-cutting; per-ticket detail goes in `codebase/<N>.md`.

- **Refusal-to-wire-code mapping is the consumer's job, NOT the primitive's.** `internal/*` packages return Go sentinels (`ErrConversationNotFound`, `ErrUnsupported`, …); dotted-string wire codes (`conversations.not_found`, `protocol.unsupported`) live as `Code*` constants but are mapped at the dispatcher / handler call site via `errors.Is`. Pinned in `internal/protocol` (#255) and `internal/conversations`.
- **`time.Time` round-trip discipline.** Monotonic-clock reading strips on JSON marshal — tests MUST compare via `time.Time.Equal`, never `==` or `reflect.DeepEqual`. Applies to every `time.Time` field that crosses the wire.
- **Atomic-write recipe for on-disk registries.** `os.CreateTemp` in the same dir → encode → `f.Sync()` → `f.Close()` → `os.Rename(tmp, path)`. Used by `internal/sessions`, `internal/identity`, `internal/devices`, `internal/conversations`. Duplicated until a fifth registry forces extraction (see archived "Resist over-DRY on duplicated registry primitives" pattern).
- **Stable disk ordering for idempotent reload.** Serialized collections sort by stable key (e.g. `created_at` then `id`) before write. Defeats Go's randomized map iteration so save→load→save is byte-stable.
- **Caller-supplied id validation at the primitive boundary, not the verb handler.** When a primitive accepts caller-supplied ids (e.g. `Pool.GetOrCreate`), the canonical-shape validator (`ValidID`) is the first thing the primitive checks — not in the handler.

## Open follow-ups

*Human-maintained. Agents: do not edit. File new follow-ups as GitHub issues.*

- **Backoff cooldown/bail-out** — if crashes happen N times in T seconds, should the supervisor give up? Currently retries forever, which is the right default for a service supervisor.
- **Phase 0.5 — Real production test on pyrybox** — supervisor hasn't been smoke-tested with a real `claude` child running as launchd/systemd. The tmux setup is still running. Only Phase 0 item left after PRs #1–#10.

(Earlier "Session ID tracking" and "Control socket design" questions resolved by the Phase 0.2–0.4 PR series — `--continue` for session continuity, line-delimited JSON over Unix socket for control.)
