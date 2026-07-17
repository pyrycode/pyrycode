# Spec #971 — Post-review comment hygiene sweep

**Size:** XS (confirmed from PO). Comments and doc-strings only, zero production logic.
**Not security-sensitive** (no `security-sensitive` label). No design change to audit —
item 3 documents an existing gate, it does not alter it.

## Summary

Three misleading comments flagged in the 2026-07-15 review. All three are facets of one
concern — correcting comments so a reader (human or pipeline agent) reasons about the
*current* mechanism, not a deleted one. No behavior change; the diff is limited to
comments / doc-strings.

This is a **pure comment sweep**: every edit is a `//` comment or package doc-string.
`make check` cannot be broken by it (comments have no compilation coupling), and there is
no cascade between sites — each edit is independent.

## Files to read first

The tree-wide greps below were run at architect time and are the authoritative surface —
codegraph is not used here because every reference is a *comment* (codegraph parses code,
not comments). Run the AC greps yourself after editing to confirm zero residue.

**Item 1 — msgqueue header:**
- `internal/msgqueue/queue.go:1-48` — the package doc-string. Lines **31–34** carry the
  false "unwired" claim to rewrite; line **34–36** (the `#869` cap sentence) is still
  true — keep it.
- `cmd/pyry/main.go:807-848` — the real wiring: `queueChanges`/`giveUps` hand-off channels,
  `msgqueue.New(...)` at **:826**, `newQueueStateEmitterV2` at **:848**. Proof the queue is wired.
- `cmd/pyry/main.go:1116-1135` — `newInboundDeliver` builds the `msgqueue.DeliverFunc` seam.
- `internal/relay/handlers/send_message.go:34,61` — the `send_message` handler consumes the
  queue (`*msgqueue.Queue` satisfies its interface). Names to cite in the new header.

**Item 2 — `reconcileBootstrapOnNew` ghost references (10 files, all comments):**
- `internal/e2e/harness.go:368-395` — the two hardest sites. `:375` (`seedBoundConversation`
  doc) and `:392` (`seedBootstrapRegistry` doc). **Read this whole block** — `:392` is written
  in a pre-#839 anticipatory voice ("stays green pre-#839", "the prep #861 exists to do") that
  is now historically stale, not just a symbol rename. See § Item 2 for the semantic shift.
- `internal/sessions/pool_bootstrap_sessionid_test.go:21` — contrasts old behavior
  ("would have rotated the bootstrap to the foreign uuid").
- `internal/e2e/realclaude/interactive_bootstrap_liveness_test.go:526` — already says
  "deterministically"; only the parenthetical `(reconcileBootstrapOnNew is a no-op…)` is stale.
- `internal/e2e/relay_v2_queue_drain_test.go:87`, `internal/e2e/relay_v2_dequeue_test.go:66`,
  `internal/e2e/relay_v2_queue_reconnect_test.go:118` — "reconcileBootstrapOnNew rotates the
  bootstrap session id to initialUUID." (shape A, below).
- `internal/e2e/relay_v2_modal_answer_test.go:60`, `internal/e2e/relay_v2_new_session_test.go:66`,
  `internal/e2e/relay_v2_settings_test.go:75`, `internal/e2e/rotation_test.go:68` —
  "pre-create `<initialUUID>.jsonl` … so reconcileBootstrapOnNew [does X]" (shape A, below).

**Item 3 — dev-only env-flag marking:**
- `cmd/pyry/main.go:384-403` — `tryAutoAttach` doc + the `PYRY_NO_AUTO_ATTACH` gate read at **:401**.
- `cmd/pyry/main.go:761-762` — the `PYRY_ALLOW_INSECURE_RELAY` gate read (`allowInsecure := …`).
- `cmd/pyry/debug_bundle_fake.go:8-30` — **already** documents the flag as "the daemon's existing
  test/insecure marker"; file is `_fake.go`, function doc says "test-only". **Out of scope.**
- `internal/relay/connection.go:66-71` — `AllowInsecureScheme` **already** documented as a
  "Test-only seam". **Out of scope.**
- `cmd/pyry/relay.go:148-155, 271` — struct-field effect comment + a runtime log line; both
  accurate, neither is the authoritative read/definition site. **Out of scope** (see § Item 3).

## Item 1 — msgqueue package header

**Problem.** `internal/msgqueue/queue.go:31-34` claims: *"This slice ships the engine unwired
(#704): no package depends on it yet. The live wiring … are separate slices (#705 / the wiring
slice)."* This is false. The queue is fully wired (verified above): constructed via `msgqueue.New`
at `cmd/pyry/main.go:826`, driven by the `DeliverFunc` delivery seam (`newInboundDeliver`,
`main.go:1135`), reported by the queue-state emitter (`newQueueStateEmitterV2`, `main.go:848`), and
consumed by the `send_message` handler (`internal/relay/handlers/send_message.go`). Consumers now
include `cmd/pyry`, `internal/relay`, `internal/relay/handlers`, and `internal/protocol`.

**Fix.** Rewrite the `#704`-"unwired" sentences (lines ~31–34) to describe the package as **wired**,
naming the four seams above. **Keep** the trailing `#869` cap sentence (~lines 34–36: "Enqueue is the
single insertion point … rejected (returns 0) — never dropped, never evicting the oldest") — still
accurate and load-bearing. Leave the SECURITY block (lines 38–47) untouched — still accurate.

**AC gate:** `grep -n unwired internal/msgqueue/queue.go` must return nothing. Do not reintroduce
"no package depends on it yet" or equivalent.

## Item 2 — `reconcileBootstrapOnNew` ghost references

`reconcileBootstrapOnNew` was deleted by #839. All 10 residual references are in comments (a code
reference would be a compile error — there are none). The **current mechanism** the AC names is:
the bootstrap session id is established **deterministically via `--session-id`** (seeded through
`seedBootstrapRegistry`, `harness.go:389`) — *not* by a startup adopt-by-mtime scan (which was
`reconcileBootstrapOnNew`).

**This is a mechanism correction, not a token swap.** #839 removed the adopt-by-mtime *scan itself*,
so it is wrong to reword `reconcileBootstrapOnNew` → `seedBootstrapRegistry` verbatim where the
sentence describes *rotating / scanning*. Reword each comment to describe what actually happens now.

Two comment shapes to reword:

- **Shape A — "pre-create `<initialUUID>.jsonl` so reconcileBootstrapOnNew rotates the bootstrap to
  it."** These explain *why the test pre-seeds the jsonl*. Reword to: the bootstrap id is pinned to
  `initialUUID` deterministically via `--session-id` (seeded by `seedBootstrapRegistry`), so no
  startup scan is relied on. Sites: the six `relay_v2_*` / `rotation_test.go` references and
  `pool_bootstrap_sessionid_test.go:21` (which contrasts the *old* "would have rotated to a foreign
  uuid" — reword to state the id is now pinned deterministically and cannot rotate to a foreign uuid).

- **Shape B — `harness.go:392` (`seedBootstrapRegistry` doc).** Highest care. The current text
  ("converges on exactly the post-startup state that adopt-by-mtime produces today … stays green
  **pre-#839** … the prep **#861** exists to do") is written in an anticipatory, pre-#839 voice that is
  now historically stale. Rewrite to the present reality: `seedBootstrapRegistry` seeds the
  deterministic bootstrap id (`--session-id`); the adopt-by-mtime scan was removed by #839. Drop the
  "pre-#839 / #861 prep" framing. Also fix `harness.go:375` (`seedBoundConversation` doc): the pool id
  `boundSessionID` must equal is now the deterministic `--session-id`, not a scan result.
  `interactive_bootstrap_liveness_test.go:526` already says "deterministically" — only the stale
  parenthetical needs rewording.

**AC gate:** `grep -rn reconcileBootstrapOnNew --include='*.go' .` (ignore the `.claude/worktrees/`
sibling checkout) must return **zero** matches.

## Item 3 — dev/test-only env-flag marking

**Design decision (architect picks the surface, per the ticket).** The AC is behavioral —
*"a reader inspecting the read/definition site … can tell each is dev/test-only."* The
authoritative read/definition sites are the `os.Getenv` gate reads. Route the fix through
**`cmd/pyry/main.go` only**:

- `main.go:401` — `PYRY_NO_AUTO_ATTACH` gate. Add a dev/test-only note at this read (and/or in the
  `tryAutoAttach` doc at :384-399, which currently lists the flag as a fall-through path without
  flagging it as non-production).
- `main.go:762` — `PYRY_ALLOW_INSECURE_RELAY` gate (`allowInsecure := os.Getenv(...) == "1"`). Add a
  dev/test-only note at this read.

**Why not the other sites (out of scope, and why AC3 is still fully satisfied):**
- `debug_bundle_fake.go:30` is the *other* `os.Getenv` read of `PYRY_ALLOW_INSECURE_RELAY`, but its
  header (`:9`) already calls it "the daemon's existing **test/insecure marker**" and the whole file is
  `_fake.go` / "test-only". A reader there can already tell. ✓
- `internal/relay/connection.go:66-70` already documents `AllowInsecureScheme` as a "**Test-only
  seam** … Production callers leave this false". ✓
- `cmd/pyry/relay.go:154` (struct-field effect comment) and `:271` (runtime log line) *reference* the
  flag but are not the read/definition site — the value flows in from `main.go:762`. Accurate as-is;
  leave untouched to keep the diff minimal.

So both `os.Getenv` read sites of each flag end up clearly dev/test-only (main.go newly marked;
debug_bundle_fake.go / connection.go already marked) → AC3 met. The flags are **NOT** removed.

**Note on `printHelp`:** `printHelp` (`main.go:1967`) documents `PYRY_NAME`/`PYRY_RELAY_URL` but not
these two flags (Technical Note item 3 confirms). Do **not** add them to `printHelp` — surfacing a
dev/test-only flag in operator help would work against the "these are not production knobs" goal.

## Concurrency model

None. Comment-only change; no goroutines, channels, or shutdown sequencing are touched.

## Error handling

None. No control flow changes.

## Testing strategy

No new tests. Verification is three deterministic gates + a green build:

1. `grep -n unwired internal/msgqueue/queue.go` → **zero** matches (Item 1).
2. `grep -rn reconcileBootstrapOnNew --include='*.go' .` (ignoring `.claude/worktrees/`) → **zero**
   matches (Item 2).
3. Manual read: `main.go:401` and `main.go:762` each carry an unambiguous dev/test-only marker (Item 3).
4. `make check` green (`go vet`, `staticcheck`, `go test -race`). Diff limited to comments/doc-strings.
5. **gofmt / aligned-comment hygiene:** keep any aligned comment blocks aligned and run `gofmt` — the
   repo keeps aligned doc comments (not CI-gated but a review nit). The e2e comment sites are `//`
   line comments, so realignment risk is low, but re-check `main.go` after editing.

## Open questions

- **harness.go:392 wording.** The developer must decide how much of the pre-#839/#861 anticipatory
  framing to strip vs. reword. Guidance: describe the *present* mechanism only; do not preserve the
  "converges on what adopt-by-mtime produces / stays green pre-#839" narrative, since #839 has landed
  and the scan no longer exists. If unsure whether `--session-id` or `seedBootstrapRegistry` is the
  more precise name for a given site, prefer the one the surrounding test actually exercises (both are
  the "current mechanism" the AC names; they are two halves of the same deterministic path).
