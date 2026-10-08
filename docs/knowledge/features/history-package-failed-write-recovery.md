# `internal/history` — failed-write recovery

Part of [the history package overview](history-package.md).

## A cleanup on a failed write is not the guarantee it looks like

This is the lesson that cost two rework rounds, on two branches of the same
`if fresh` in `writeSegment`, and it generalizes past this package.

`writeSegment` creates a fresh segment with `O_CREATE|O_EXCL` before writing
it. The first version cleaned up nothing when the write itself failed: an
ordinary `write(2)` error (ENOSPC, EDQUOT, EIO — a full disk, not a machine
crash) left a zero-length segment on disk. `decodeSegment` read it back as
`ErrUnknownVersion`, and because `load` always reaches the newest segment
first, **that one failed write made the conversation's whole history
unreadable and unappendable forever** — including every entry that had
already landed successfully. The fix that looked complete — unlink the file
when the write or close fails — is necessary but is *not* the guarantee: an
`os.Remove` can itself fail, and a crash between the create and the write
leaves the identical residue with no cleanup to run at all. The property that
actually has to hold — a conversation is never permanently unreadable — has to
rest on the *reader* tolerating whatever a failed write leaves behind, not on
the *writer* successfully cleaning up after itself. `load` now treats any
segment it cannot safely resume writing to (no header, a header torn
mid-write, or a torn entry line) as one to roll past, and `listSegments` was
already a listing rather than a numeric probe — for the unrelated reason of
tolerating retention deletes — so the numbering gap a roll-past leaves was
already a supported shape.

The second rework found the same defect on the *other* branch: a short write
into the **active** (non-fresh) segment — the branch nearly every append
actually takes — leaves a torn final line with no cleanup path at all, and the
first design's own text asserted this was "unreachable while the process
lives... only a machine crash", which is false (`os.File.Write` returns the
bytes already transferred alongside the error). The write-side fix is a
truncate back to the pre-call length, read from `Stat` on the descriptor the
call already opened — **never from the in-memory belief of the size**, since a
truncate to a merely-remembered length can destroy entries that genuinely
landed. The read-side fix is the same tolerance generalized: `ErrCorruptSegment`
now means a *complete* line that fails to decode, and a torn tail is dropped
rather than refused, because those bytes belong to an entry whose `Append`
already returned an error to its caller — refusing them a second time costs
every entry that *did* succeed.

**Belt-and-suspenders means different fabric, and both halves matter for a
different reason.** The write-side undo (remove the orphan / truncate the
overrun) *prevents* the bad state from persisting when the failure is
survivable. The read-side tolerance *survives* the bad state regardless of
what produced it — a failed cleanup, or a crash with no cleanup to run. Ship
only the first and a merely-unlucky `os.Remove` reintroduces the whole defect;
ship only the second and every failed write costs a segment's worth of
already-durable entries for no reason. **For any append-only store, the
durability question is not only "did the bytes land" but "what does a partial
failure leave behind, and can the next reader still make progress past it" —
and that question needs to be asked of *every* branch that writes, not just
the one a first review happened to land on.** This is carried forward
explicitly for `#2114`/`#2115`, whose append call sites are this package's
first production callers.

**Testing a failure that can't be induced.** Neither the ENOSPC nor the short
write above could be triggered from a unit test without adding a fault seam to
production code (`RLIMIT_FSIZE` was tried as an ENOSPC stand-in and measured
not to be enforced on this machine's darwin/APFS). Both are tested instead by
planting the *residue* directly on disk — a zero-length segment file, a
segment whose last line has no trailing newline — and asserting the store
still reads and still accepts appends. **When a failure mode can't be
triggered, test the state it would leave behind, not the syscall that
produces it**; that tests the actual guarantee (the reader tolerates this
shape) rather than a specific trigger for it.
