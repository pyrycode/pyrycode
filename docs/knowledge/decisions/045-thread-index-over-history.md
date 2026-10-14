# 045. The thread is a small index over the history log

## Status

Proposed. Juhana accepts this decision by approving the PR. This record states
the target architecture; it does not implement it. Same-boot epoch reuse remains
an undecided, separate proposal in [#3338](https://github.com/pyrycode/pyrycode/issues/3338).

## Context

[ADR 042's Storage decision](042-daemon-built-thread.md#the-six-approved-decisions)
keeps history as the one durable record and folded items as a disposable cache.
The current thread refolds from entry 1 on load, retains all content, copies
whole threads for publication and has saved whole threads per append. Its
public items cannot restore the private state needed to continue unfinished work
or reconstruct the last shown version from final revisions alone. See
[cache and epochs](../features/thread-package.md#cache-and-epochs).

The supplied measurements from the Mac on 2026-10-14, pyry 0.39.0, show the cost:
folding 28767 entries took about 8 minutes of CPU; publication allocated about
41 MB per appended entry; the 50 ms shadow poll copied about 1 GB a second;
long-conversation caches were 25–43 MB; daemon resident memory was 589 MB.
These are historical observations, not benchmarks run for this decision.

[#3325](https://github.com/pyrycode/pyrycode/issues/3325),
[#3327](https://github.com/pyrycode/pyrycode/issues/3327),
[#3328](https://github.com/pyrycode/pyrycode/issues/3328) and
[#3329](https://github.com/pyrycode/pyrycode/issues/3329) address the waste.
This decision states the proposed target for
[#3332](https://github.com/pyrycode/pyrycode/issues/3332),
[#3333](https://github.com/pyrycode/pyrycode/issues/3333),
[#3334](https://github.com/pyrycode/pyrycode/issues/3334),
[#3335](https://github.com/pyrycode/pyrycode/issues/3335),
[#3336](https://github.com/pyrycode/pyrycode/issues/3336) and
[#3337](https://github.com/pyrycode/pyrycode/issues/3337), without waiting for
their implementation.

## Decision

The proposed design refines ADR 042's storage cache in five ways.

1. **History stays the only content store.** Segment files, their format and all
   four producers remain unchanged: interactive emission, session transitions,
   operator message delivery and channel posts. Add stored-entry reads by id and
   conversation-log syncing at thread checkpoints. See the
   [history package](../features/history-package.md).

2. **The thread is an index over the log.** An item keeps metadata and a content
   recipe: its creating entry, named fields referencing entries or carrying
   small values, and text-entry lists. Render content from history when sending
   or querying. Memory holds metadata and recipes, targeting about 5 percent of
   log size, plus caches bounded by bytes.

3. **The fold is incremental and resumable.** Per-entry work is proportional to
   what it touches. Private state holds entry ids and small facts, and shrinks
   finished work to small markers. Saving and restoring it produces output
   identical to uninterrupted folding, including unfinished work.

4. **Persistence is per conversation.** An append-only index holds framed,
   checksummed item records. A small state file holds version, epoch, last shown
   version, index filename and valid length, fold state and a clean flag. It also
   identifies the compatible format and folding rules.

   Checkpoint on a bounded 30-second interval while changing, and on unload and
   shutdown. The order is: sync history through the version; append changed item
   records; sync the index; write state through a synced temporary file in the
   same directory and atomic rename. Establish directory-entry durability as
   described below before treating the checkpoint as durable.

   Load validates the checkpoint, truncates the index to the recorded length,
   restores private state and resumes at the next entry. Missing or invalid
   pieces require a full refold, now linear.

5. **The epoch follows recovery evidence.** Load writes clean false before its
   first publication. A successful final checkpoint writes clean true. Clean,
   matching state keeps the epoch. An unclean stop, rule change or failed check
   mints a new epoch; an unclean stop alone does not force refolding a usable
   checkpoint.

   Same-boot epoch reuse is separately proposed in
   [#3338](https://github.com/pyrycode/pyrycode/issues/3338). Unsynced log entries
   survive a process stop while the OS stays running, but can be lost in a machine
   crash. This decision does not accept that refinement.

## Reliability

History assigns entry ids. The deterministic fold derives item identity and
order from those ids, and immutable entries supply content. The index is a cache.
A checkpoint is valid only when renamed state names log and index data already
synced through its boundary and passes validation. Check format and folding-rule
compatibility, history consistency through the saved version, referenced entries,
index length, record framing and checksums, and restored fold state before use.

Discard index bytes beyond the recorded length and replay the surviving log
suffix. An invalid checkpoint requires refolding readable history. Preserve
history's existing torn-tail recovery; unreadable history cannot yield a
cache-only view. Failed checkpoints cannot advance the certified durable
recovery boundary or certify clean shutdown.

Apps retain an epoch only when history through the published version still means
the same thing. Shortened or inconsistent history and changed rules require a
new epoch, even if cached metadata looks usable.

Atomic rename exposes a complete state file, but does not by itself make the
directory entry durable after a machine crash. The implementation must sync
directories containing newly created log segments and the index before state
can commit references to them, and sync the state directory after replacement.
This assumes the filesystem honors successful file and directory syncs. A
missing or invalid state name after recovery means a new epoch and a full refold.
A process stop alone does not lose the OS's renamed directory entry.

The same durability requirement applies to dirty marking: sync the clean-false
state and its renamed directory entry before first publication. Otherwise a
machine crash could restore an older clean-true state while losing published,
unsynced history. A fallback only for missing files would not detect that case.
If directory durability cannot be guaranteed, recovery must conservatively mint
a new epoch rather than trust a surviving clean flag; if dirty marking fails,
do not make the first publication. This is a safety requirement for the proposed
implementation, not a claim about today's cache writer.

| Crash or failure point | Outcome | Guarantee or validation |
| --- | --- | --- |
| Torn history append | Preserve existing torn-tail recovery. Rebuild from surviving readable entries when checkpoint validation fails, with a new epoch. Unreadable history leaves the view unavailable. | History remains authoritative; the index cannot invent missing content or override log recovery. |
| After folding/publication, before checkpoint | Restore the previous valid checkpoint and replay the surviving suffix. Mint a new epoch after an unclean stop without forcing a full refold of a usable checkpoint. | Saved private fold state restores the boundary. A process stop retains OS-buffered history; a machine crash can lose an unsynced published suffix. |
| During history sync | Keep the previous recovery boundary; do not commit new checkpoint state. | History sync through the candidate version must succeed before index records can commit. |
| After history sync, before index append | Restore the previous checkpoint and replay the now-synced suffix. | Synced history alone does not advance the state file's version or valid index length. |
| During/after index append, before index sync | Discard bytes beyond the previous state's valid length, then replay. | Framing and checksums detect torn records; the old named state remains the recovery boundary. |
| During index sync | A failed sync leaves the old boundary; discard the uncommitted suffix on load. | State replacement cannot follow a failed index sync. |
| After index sync, before state write | Discard synced records beyond the previous recorded length and replay. | Extra durable index bytes remain uncommitted until state replacement. |
| During temporary-state write/sync | Ignore the incomplete temporary file and use the previous valid state. | A temporary file cannot replace state until its complete contents are synced. |
| After temporary-state sync, before rename | Use the previous state, truncate any extra index suffix and replay. | Only the named state file commits the recovery boundary. |
| During/after atomic rename | Recover an old valid state or the new valid state; if the name or state does not survive, mint an epoch and refold. Never certify unchecked state. | Rename exposes a complete file. A process stop retains the renamed entry; machine-crash persistence needs directory sync. Durable dirty marking prevents an older clean state from authorizing unsafe epoch reuse. |
| Corrupt index record within recorded length | Reject the checkpoint, mint an epoch and refold from readable history. | Framing, checksum and checkpoint consistency validation precede cache use. |
| Log shorter than saved version | Reject the checkpoint, mint an epoch and refold surviving readable history. | Compare the saved version and references with history; never serve cached content or identities ahead of their source. |
| Folding-rule change | Mint an epoch and refold under the new rules. | Format/rule compatibility checks prevent restoring incompatible private state or retaining changed item meaning. |
| Full disk during checkpoint or dirty marking | Keep the previous valid checkpoint where available; fail without advancing certified durable progress or marking clean. If clean false cannot be established, withhold first publication. | Check every append, sync, state write, close, rename and directory sync. A failed final checkpoint cannot certify clean shutdown; recovery validates surviving state and history and cannot trust uncertain clean evidence. |

## Rejected options

- **SQLite for items, or items and history.** Releases disable cgo, requiring a
  pure Go port that adds roughly half again to a 12.7 MB binary and a second
  persistence model. Moving history requires every log migrated, history-page
  cursors for old apps and rollback. The measured costs are folding and cache
  costs, not history appends.
- **An item journal with full content.** Rendering is simpler, but disk use
  doubles again.
- **A debounced whole-file cache.** It still grows with the thread, the defect
  being fixed.

## Consequences

The proposed index reduces resident content, makes full refolding linear and
allows checkpoints to resume unfinished work. Rendering now reads history, so
read locality and byte-bounded caches matter. Checkpoint validation and restoring
private state add work and failure cases that a whole-file public snapshot did
not handle.

The relay protocol and every wire message stay unchanged. Apps still receive
rendered items under ADR 042's identity, ordering and epoch contract.

Startup reconciliation currently scans each conversation's whole log three
times, for main work, agent work and accepted sends. Reading active items from
the index is a follow-up outside this decision.

## Migration and rollback

Never rewrite history. Migration and rollback each start with one new epoch.
Older daemons ignore the new files and refold from history. Losing the cache
costs work, not correctness. This ADR records that contract; it does not itself
migrate any conversation.
