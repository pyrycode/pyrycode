# ADR 045: the thread as a small index over history (#3331)

## Files read

- `docs/knowledge/INDEX.md`: assigns knowledge-file writes to the documentation stage.
- `docs/knowledge/decisions/042-daemon-built-thread.md` → `The six approved decisions`, **Storage**: history is authoritative and items are a disposable cache; preserve the other decisions and epoch wording.
- `docs/knowledge/features/thread-package.md` → `Cache and epochs`: current loads replay history; public items omit private continuation state and final revisions cannot reconstruct the last shown version.
- `docs/knowledge/CATALOG.md` → `Decisions`: the table currently ends at 044 and owns the new linked row.
- `docs/knowledge/features/development-verification.md` → `Verify inherited premises`: distinguish the proposed architecture from current implementation.
- `CODING-STYLE.md` → `Persistent data conventions`: synced temporary files and atomic replacement are separate from directory-entry durability.
- Live issue #3331: the refined documentation handoff assigns all three knowledge-file changes to the documentation stage and separates #3338.

QMD search of `pyrycode-docs` for thread/history/cache/checkpoint found the owning thread overview and the earlier #3064 cache plan. No new code inspection is needed for this documentation-only contract.

## Context

This ticket records one architectural decision, ADR 045, refining ADR 042's storage decision. It does not implement the proposed design. The documentation stage owns the ADR and its two links. This builder commits the plan and carries the complete contract into the PR handoff.

The supplied measurements are historical evidence, not new benchmark claims. On the Mac on 2026-10-14, pyry 0.39.0 took about 8 minutes of CPU to fold 28767 entries. Publication allocated about 41 MB per appended entry. The 50 ms shadow poll copied about 1 GB a second. Long-conversation caches occupied 25–43 MB, and daemon resident memory was 589 MB.

Tickets #3325, #3327, #3328 and #3329 address the waste. ADR 045 states the proposed target for #3332–#3337 without depending on their implementation. Same-boot epoch reuse remains a separate proposal in #3338.

## Design

Deliver one Proposed ADR in ADR 042's style, plus a storage cross-reference and catalog entry. The full required content and reliability argument appear below, ready for the documentation stage to turn into the three knowledge-file edits. No Go, protocol, schema or wire changes belong to this ticket.

Sizing before plan commit: 108 plan lines plus approximately 210 documentation lines; one deliverable, zero exported types/interfaces, zero consumer call-site updates, two refined acceptance criteria, and zero implemented state-machine reject branches. All limits remain within the builder boundaries and the refined estimate's documentation scope.

Fetched origin and checked other numeric feature branches for this plan and the three future documentation paths. No overlap was found. The referenced implementation tickets are context, not blockers for recording a proposed target.

## Concurrency model

None: this change records a decision and starts no goroutines. Checkpoint concurrency is a proposed implementation responsibility, not code added here.

## State transitions and identity reuse

None: this documentation-only change holds no lifecycle or identity state. The proposed crash and epoch contracts are enumerated in the documentation handoff's reliability table; this ticket adds no runtime tests.

## Error handling

Documentation must distinguish an unusable cache from unreadable authoritative history. The former permits refolding; the latter cannot yield a cache-only view. Failed checkpoint operations must neither advance the durable recovery boundary nor certify clean shutdown. Directory-entry durability must be explained without claiming that atomic rename alone guarantees persistence after a machine crash.

## Testing strategy

Review the handoff against every requirement in the refined issue. Check that the reliability table covers every checkpoint stage, recovery validation, process-stop versus machine-crash behavior, and epoch consequences. Check relative link targets and headings against the existing ADR and catalog. Run `git diff --check` and verify that the builder diff contains only this plan. No production package is touched, so no Go test, race test, vet or build is required for this documentation-only stage. The verifier's normal gate remains dispatcher-owned.

## Open questions

None blocking. #3338 decides same-boot epoch reuse separately; ADR 045 must not accept it implicitly. Juhana accepts ADR 045 by approving the PR; until then its status is Proposed.

## Documentation handoff

All items below are **pending for the documentation stage**. Only the following three knowledge files are to change; the builder does not edit them.

### `docs/knowledge/decisions/045-thread-index-over-history.md`

Create ADR 045 in short paragraphs and plain words, following ADR 042. Include the following sections and requirements.

**Status:** Proposed. Juhana accepts it by approving the PR. Describe the target as proposed, not already implemented.

**Context:** retain all five measurements in this plan's Context, their date, platform and pyry version. Link ADR 042's Storage decision, #3325, #3327, #3328, #3329 and the #3332–#3337 implementation tickets. Do not order new benchmarks.

**Decision:** record these five points:

1. **History stays the only content store.** Segment files, their format and all four producers stay unchanged. Add stored-entry reads by id and syncing a conversation's log at thread checkpoints.
2. **The thread is an index over the log.** An item keeps metadata and a content recipe: its creating entry, named fields that reference other entries or carry small values, and the list of text entries. Render content from history when sending or querying. Memory holds metadata and recipes, targeting about 5 percent of log size, plus byte-bounded caches.
3. **The fold is incremental and resumable.** Work per entry is proportional to what it touches. Private state holds entry ids and small facts and shrinks completed work to small markers. Saving/restoring, including unfinished work, produces output identical to uninterrupted folding.
4. **Per-conversation persistence.** An append-only index file holds framed, checksummed item records. A small state file holds version, epoch, last shown version, index filename and valid length, fold state and a clean flag. Checkpoint at a bounded 30-second interval while changing, and on unload and shutdown. Sync history through the version; append changed item records; sync the index; then write state through a synced temporary file and atomic rename. Load truncates the index to the recorded length, restores private state and resumes at the next entry. Missing or invalid pieces require a full refold, now linear.
5. **Epoch.** Load writes clean false before its first publication. A successful final checkpoint writes clean true. Clean, matching state keeps the epoch. An unclean stop, rule change or failed check mints a new epoch. An unclean stop alone does not force a refold of a usable checkpoint. Same-boot reuse is separately proposed in #3338: unsynced log entries survive a process stop but may be lost in a machine crash.

**Reliability:** include a table naming each crash/failure point, its outcome, and the invariant/check that guarantees it. Use at least the following coverage:

| Crash or failure point | Outcome | Guarantee or validation |
| --- | --- | --- |
| Torn history append | Preserve existing history torn-tail recovery; rebuild from surviving readable entries and reset epoch if validation fails. Unreadable history leaves the view unavailable. | History remains authoritative; the index cannot invent missing content or override log recovery. |
| After folding/publication, before checkpoint | Restore the previous valid checkpoint and replay surviving suffix entries. Mint a new epoch after an unclean stop, without forcing a full refold of a usable checkpoint. | Private fold state restores the saved boundary. A process stop normally retains OS-buffered history; a machine crash can lose unsynced published suffix entries. |
| During history sync | Retain the previous recovery boundary; do not publish new checkpoint state. | History sync must succeed through the candidate version before index checkpoint records can commit. |
| After history sync, before index append | Restore the previous checkpoint and replay the now-synced suffix. | Synced history alone does not advance the state file's version or valid index length. |
| During/after index append, before index sync | Discard any bytes past the previous state's valid length, then replay. | Framing/checksums detect torn records; the old state file remains the recovery boundary. |
| During index sync | A failed sync leaves the old boundary; ignore/discard the uncommitted suffix on load. | State replacement cannot follow a failed index sync. |
| After index sync, before state write | Discard synced records beyond the previous recorded length and replay. | Extra durable index bytes are still uncommitted until state replacement. |
| During temporary-state write/sync | Ignore the incomplete temporary file and use the previous valid state. | A temporary file cannot replace state until its complete contents are synced. |
| After temporary-state sync, before rename | Use the previous state, truncate any extra index suffix and replay. | Only the named state file commits the recovery boundary. |
| During/after atomic rename | Recover either an old valid state or the new valid state; if the name/state does not survive, refold. Never certify an unchecked state. | Rename exposes a complete file, not a partial one. Machine-crash persistence of its name needs directory-entry durability; explain directory syncing or this safe missing/invalid-state fallback. A process stop does not itself lose the OS's renamed directory entry. |
| Corrupt index record within recorded length | Reject the checkpoint, mint an epoch and refold from readable history. | Framing, checksum and checkpoint consistency validation precede cache use. |
| Log shorter than saved version | Reject the checkpoint and mint an epoch; refold the surviving readable history. | Check the saved version against history; never serve content or identities from a cache ahead of its source. |
| Folding-rule change | Mint an epoch and refold under the new rules. | Version/rule compatibility checks prevent restoring incompatible private state or retaining changed item meaning. |
| Full disk during checkpoint or dirty marking | Keep the previous valid checkpoint; fail the operation without advancing durable progress or marking clean. If clean false cannot be established, do not make the first publication. | Check every append, sync, state write and rename. A failed final checkpoint cannot certify clean shutdown; recovery validates surviving state and history. |

State the argument in prose as well: history assigns entry ids, the deterministic fold derives item identity/order from them, and immutable entries supply content. The index is a cache. A checkpoint is valid only when renamed state names log and index data already synced through its boundary and passes validation. Discard anything beyond the recorded index length and refold the suffix. Apps retain an epoch only while history through the published version has the same meaning. Shortened/inconsistent history or changed rules require a new epoch.

**Rejected options:** retain all three alternatives and their reasons:

- **SQLite for items, or items and history.** Releases disable cgo, so a pure Go port would be needed, adding roughly half again to a 12.7 MB binary and bringing another persistence model. Moving history would require every log migrated, history-page cursors for old apps, and a rollback story. The measured costs are in folding and caching, not history appends.
- **An item journal with full content.** Rendering is simpler, but disk use doubles again.
- **A whole-file cache with a debounce.** It still grows with the thread, the defect being fixed.

**Consequences:** balance reduced resident content, linear refolds and resumable checkpoints against rendering and validation costs. Relay protocol and every wire message stay unchanged. Startup reconciliation currently scans each conversation's whole log three times; reading active items from the index could improve it later, but is outside this decision.

**Migration and rollback:** history is never rewritten. Migration and rollback each start with one new epoch. An older daemon ignores the new files and refolds. Losing the cache costs work, not correctness. Do not claim that this ADR itself migrates anything.

### `docs/knowledge/decisions/042-daemon-built-thread.md`

In **The six approved decisions**, item 1 **Storage**, append exactly one sentence linking ADR 045 as the proposed index/checkpoint refinement. Suggested text: “The proposed [ADR 045](045-thread-index-over-history.md) refines this cache into a small index over history with resumable checkpoints.” Retain the rest of ADR 042, including its epoch policy.

### `docs/knowledge/CATALOG.md`

In **Decisions**, add one row for 045 linking `decisions/045-thread-index-over-history.md` and identifying its status as Proposed. Suggested description: “Proposed: the thread becomes a small index over immutable history, with resumable folding and ordered checkpoints; history remains the only content store.” Re-index QMD in the documentation stage's normal workflow; the builder must not update the shared index from this worktree.
