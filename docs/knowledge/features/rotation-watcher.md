# Live `/clear` Rotation Watcher (retired, #2137)

**Retired 2026-09-07.** `internal/sessions/rotation` — the fsnotify watch on claude's session
directory plus its per-PID open-descriptor probe (`lsof` on Darwin, `/proc/<pid>/fd` on Linux, on a
250ms retry schedule) — is gone from the tree. This doc is kept as a historical pointer, not a
description of live code; do not use it to reason about current behavior.

## What it did, and why it stopped

The watcher answered one question — "which tracked session rotated into this new file?" — by
guessing: on a CREATE in claude's session directory, it asked the OS which transcript each tracked
process currently had open, and treated a match as the rotation. Real claude opens its transcript,
appends, and closes within milliseconds, so the probe practically never observed an open descriptor;
it may only ever have been reliable against the e2e fake-claude binary, which holds its descriptor
open deliberately.

Claude's own `conversation_reset` announcement (#2134/#2135/#2136) now answers the same question
directly — it arrives on the session's own stream and carries the new id in its payload — so the
watcher's guess became redundant with a strictly more reliable source. #2137 deleted the package
whole: its `Pool.Run` wiring, the freshly-allocated skip-set that suppressed a spurious CREATE for a
daemon-driven spawn (`RegisterAllocatedUUID`/`IsAllocated`, all three registration sites),
`snapshotForRotation`, and `Pool.onRotate`. The `fsnotify` dependency dropped out of `go.mod` with
its last importer. `Pool.RotateID` — the mutation seam the watcher drove — stayed: it predates the
watcher (#839 merely wired it in), is exported, and carries ~40 test references, so removing it was
left a separate, deliberate call.

## Where the replacement lives

- [`streamsup-package-announced-reset-follower.md`](streamsup-package-announced-reset-follower.md) —
  `sessionResetFollower`, the decorator that reads `conversation_reset` off the stream and re-keys.
- [`sessions-package-key-types-adoptannouncedid.md`](sessions-package-key-types-adoptannouncedid.md) —
  `Pool.AdoptAnnouncedID`, now the sole writer of an announced re-key (the watcher was the second,
  usually-winning writer before #2137; see that doc's note on the trust-boundary promotion).

## Lasting artifacts from this package's lifetime

Two things this package produced outlive its deletion and are documented where they now live rather
than here:

- The symlink-resolution discipline for comparing an fsnotify event path against a probe path (#118,
  canonical watch dir vs. symlinked probe path; #221, the inverse) doesn't apply to the replacement —
  `conversation_reset` carries the id as a value, not a path to resolve.
- `internal/transcript`'s `Probe` interface, `Probed`, and `GuardProbedPath` — originally added so
  two other probe-preferred resolvers could share this package's shape without importing it — are
  kept; they already had no production callers before #2137 and their reachability is unchanged. See
  [`transcript-package.md`](transcript-package.md).

## References

- Original ticket: [#39](https://github.com/pyrycode/pyrycode/issues/39)
- Original spec: [`docs/specs/architecture/39-live-rotation-watcher.md`](../../specs/architecture/39-live-rotation-watcher.md)
- ADR: [`004-fsnotify-for-rotation-detection.md`](../decisions/004-fsnotify-for-rotation-detection.md) (superseded)
- Retirement ticket: [#2137](https://github.com/pyrycode/pyrycode/issues/2137), spec at
  [`docs/specs/architecture/2137-retire-rotation-watcher.md`](../../specs/architecture/2137-retire-rotation-watcher.md)
