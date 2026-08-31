# Directory resolution and creation (#1781)

`EnsureDir` (`storage.go`) resolves and creates the on-host directory one
attachment of one conversation is filed under —
`conversations/<conversation-id>/attachments/<attachment-id>` beneath the
daemon instance directory, every level `0o700` — and returns its
`EvalSymlinks`-resolved path. Writing bytes into it is #1782's; dispatching to
it and mapping refusals to wire codes is #1744's. It carries two sentinels of
its own, `ErrInvalidID` and `ErrNotContained`, structurally unlike the seven
below: `EnsureDir` has no accumulator state to latch or discard, so the
discard-semantics table doesn't apply to it.

Both ids are validated against `conversations.ValidID` before any filesystem
call — `attachment_id` is client-chosen and its 64-byte wire ceiling is
explicitly not a defence, so containment comes entirely from the resolution
check, never from the id being hard to guess. The check itself is
`candidate == want` (full-path equality against a destination built textually
beneath the `EvalSymlinks`-resolved instance directory), not a
`filepath.Rel`-style "is it under the root" test — equality is what refuses a
conversation directory symlinked to a *sibling* conversation, which a
containment-only test would pass.

- **A precedent's ordering rationale doesn't transfer unchanged when a caller
  strengthens the comparison it protects — measured, not assumed.**
  `confineWorkdirToHomeCreating` (`cmd/pyry/main.go`) is this design's
  precedent, and its ancestor walk probes with `os.Lstat` rather than
  `os.Stat` so a symlink is resolved instead of stepped over. `EnsureDir`
  copied that probe, but re-measuring it under the *stronger* comparison this
  design uses (equality, not the precedent's `filepath.Rel` containment test)
  found the swap reddens **nothing** in the suite: the two probes diverge only
  for a *dangling* link, and on one, `Stat` steps over it so the pre-creation
  check passes vacuously, but `MkdirAll` then fails `EEXIST` on the symlink
  name — landing in the same wrapped-OS-error class the `Lstat` build reaches
  by failing to resolve it. `Lstat` was kept anyway (refusal should come from
  the resolution step rather than incidentally from directory creation, and
  the precedent's weaker containment test genuinely does depend on the probe),
  with the code comment rewritten to state the measured fact rather than the
  inherited one. Anyone copying a piece of `confineWorkdirToHomeCreating` into
  a design that changes the comparison it was written for should re-measure
  that piece, not inherit its justification.
- **A spec's own fixture rule can make its own predicted sole-red row
  unsatisfiable.** The `os.Stat` mutant above was predicted (in the
  architect's mutant table) to redden the escaping- and sibling-conversation
  symlink tests. Both of those fixtures are required to use targets that
  *exist* — `EvalSymlinks` on a dangling link errors, so a dangling-target
  fixture would assert the wrong sentinel for a right-looking reason — which
  is exactly the fixture the `Stat`/`Lstat` divergence needs to be visible at
  all. No test satisfying those two criteria can ever be the mutant's sole
  red. When a mutant table names a row its own fixture constraints forbid,
  treat the row as unsatisfiable rather than an unwritten test to chase; a
  dangling-symlink case still deserves its own test (`EnsureDir` returns a
  wrapped OS error and neither sentinel for one — broken host state is not a
  containment breach), just not as that mutant's proof.
