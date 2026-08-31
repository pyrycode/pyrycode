# Blocked family (not landed)

- **#1767** — closed, split into this family. The first-chunk
  `total_chunks`/`size` cross-check landed as `CheckDeclaration` (#1776, see
  § "Admission layer" above), the per-upload byte bound landed as
  `CheckDeclaredSize` + `Add`'s step 5 (#1777, see § "Per-upload byte bound"
  above), and the conn-keyed registry of in-flight uploads landed as
  `Registry` (#1787, its admission decision moved under one lock acquisition
  by #1795, and given its entry-count cap by #1796 — refiled after #1786
  closed without shipping it — see § "In-flight upload registry" above).
- **#1742** — closed, split into this family. Expiry/abandonment of a partial
  upload landed as `uploadIdleTimeout` / `reapExpiredLocked` (#1881, see
  § "In-flight upload registry" above). That reap covers *partial* uploads
  only — a complete-but-corrupt transfer that fails one of #1770's integrity
  checks is not partial, so it's #1770's own `reject` latch, not the reap,
  that frees those held bytes.
- **#1773** — split into #1781 and #1782 while this family waited. Resolving
  and creating the attachment directory landed as `EnsureDir` (#1781, see
  § "Directory resolution and creation" above); writing bytes into it landed
  as `Store` (#1782, see § "Writing attachment bytes" above). (#1743 split
  into #1772/#1773 earlier in the same wait; #1772 — filename sanitisation —
  landed, see `SanitizeFilename` above.)
- **#1744** — wires the dispatch site: maps the three framing sentinels to
  `CodeAttachmentInvalidChunk`, the two integrity sentinels to
  `CodeAttachmentIntegrityFailed`, and `ErrTooManyUploads` to
  `CodeAttachmentTooManyUploads`, all via `errors.Is`. Now unblocked and free
  to land — #1796 discharged the family's sequencing constraint that this
  ticket not go first, and #1784 shipped the routing surface (`Registry.Deliver`)
  it is expected to call (see § "In-flight upload registry" above) — and is the
  first production caller of this package. Four things are parked for
  whichever of #1744 or the documentation phase next touches this area: which
  wire code `ErrUnknownUpload` maps to (#1784 deliberately named no
  candidate); the never-log claim on `Deliver`'s doc block, which code review
  found unpinned by any fixture (see § "In-flight upload registry" above);
  and two more #1817's code review flagged. `uploadIdleTimeout`'s doc
  (`admission.go`) reads "IT DOES NOT SUBSUME #1817, which releases a dropped
  conn's uploads AT THE DROP" — true only once #1744 wires that call, not on
  #1817's own merge, since #1817 ships `ReleaseConn` with no production
  caller; #1744 is what makes that sentence describe what actually ships
  rather than a closed ticket by what it didn't. And `Release`/`ReleaseConn`'s
  own docs (`registry.go`) both now name #1744 as the caller that decides
  when either runs — right only if #1744's teardown wiring lands as this
  family has planned it; #1744's own issue body scopes it to
  `dispatchAppFrame`'s frame switch and names no teardown or `closeWith`
  path, so if it refines to the chunk path alone, those two cites need a
  different, as yet unticketed, owner.
