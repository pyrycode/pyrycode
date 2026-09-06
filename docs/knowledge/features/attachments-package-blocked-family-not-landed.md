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
- **#1744** — split into #1896 and #1897, both now landed, closing out this
  bullet. #1896 landed the composite driver, `Intake` (see § "Chunk intake
  driver" above): it sequences `Admit`, `Deliver`, `EnsureDir` and `Store`
  into the one call the wire layer needs, and was this package's first
  production caller — but only from *inside* the package. #1897 landed the
  rest of this bullet's original scope: wiring `appFrameWorker` as the
  package's first caller from *outside* it, declaring the seam interface
  `Intake.Receive`/`Intake.ReleaseConn` satisfy, building the production
  conversation resolver, and mapping sentinels to wire codes — the three
  framing sentinels plus `ErrUnknownUpload` to `CodeAttachmentInvalidChunk`,
  the two integrity sentinels to `CodeAttachmentIntegrityFailed`,
  `ErrTooManyUploads` to `CodeAttachmentTooManyUploads`, and `ErrInvalidID`/
  `ErrNotContained`/`ErrWriteFailed`/`ErrNoConversation` (the last of these,
  and the resolver that raised it, retired by #2143 — see
  [Chunk intake driver](attachments-package-intake-driver.md) § "The
  conversation is resolved once, on the completing chunk only") to
  `CodeAttachmentStorageFailed`, all via `errors.Is` — see
  [Error codes § the seven `attachment.*` codes](protocol-package-constants-codes-go-error-codes-21.md)
  for the two choices' retryability rationale. `#1897` also wired
  `Intake.ReleaseConn` to `V2SessionManager.closeWith`'s per-conn teardown
  cluster, so `uploadIdleTimeout`'s doc comment (`admission.go`) — "IT DOES
  NOT SUBSUME #1817, which releases a dropped conn's uploads AT THE DROP" —
  is now true end to end rather than only at `Intake`'s own layer. One thing
  stayed open past both tickets: the never-log claim on `Deliver`'s own doc
  block, which code review found unpinned by any fixture in
  `registry_test.go` and which #1896 pinned only one layer up, at `Intake`'s
  own tests (see § "In-flight upload registry" above — "Still open in
  `Deliver`'s own suite") — #1897 drives `Deliver` through `Intake` too, so
  it didn't add a `registry_test.go` fixture either; that gap is still
  unclaimed by any landed ticket.
