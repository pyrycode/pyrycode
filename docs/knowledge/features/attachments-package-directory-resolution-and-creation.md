# Directory resolution and creation (#1781, #2037)

`EnsureDir` (`storage.go`) resolves and creates the on-host directory one
attachment of one conversation is filed under —
`conversations/<conversation-id>/attachments/<attachment-id>` beneath the
daemon instance directory, every level `0o700` — and returns its
`EvalSymlinks`-resolved path. Writing bytes into it is #1782's. `Intake`
(#1896, see § "Chunk intake driver" below) is now its caller, on the
completing chunk only, feeding the returned directory straight to `Store`;
mapping refusals to wire codes is #1897's, at the dispatch site outside this
package. It carries two sentinels of
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

## `ResolvePath` (#2037): read the same layout back, creating nothing

`ResolvePath` is `EnsureDir`'s read-side counterpart, added to the same file
because it reuses `EnsureDir`'s whole containment discipline minus the
creation: resolve the instance directory as the anchor, build the destination
textually beneath it, and compare for **equality** rather than an "is it
under" test — then extends that discipline to the leaf, where `os.ReadDir`
selects the lexicographically smallest entry that is both a regular file and
not dot-prefixed. The exclusion is anchored on `SanitizeFilename`'s published
guarantee that a stored name never begins with `.`, not on a pattern match
against `Store`'s private temp-file shape. Every refusal — bad id shape, an
absent pair, or a pair resolving outside the named conversation's own
directory — wraps one sentinel, `ErrNotFound`. `docs/protocol-mobile.md` §
Error codes makes those three cases deliberately indistinguishable on the
wire, so one shared sentinel makes that a property of the type rather than a
discipline a future dispatch site has to keep. Reusing `ErrInvalidID` or
`ErrNotContained` was rejected for a concrete reason, not tidiness:
`internal/relay/v2session_attachment.go`'s `attachmentRejectFor` already maps
both of those to `rejectStorageFailed`, and a retrieval dispatch site sharing
either mapping would answer the wrong wire code for a lookup refusal.

**The precondition this function cannot check is the one that matters most.**
`conversationID` must be the conversation the authenticated session is
already on, never one a client asserted — every containment step still
passes for a client-supplied conversation id, because the pair genuinely
resolves inside the conversation it names. `docs/protocol-mobile.md` § Naming
a message's attachments is explicit that confinement to the message's own
conversation, not `attachment_id`'s shape or randomness, is what keeps it
from becoming a capability; `Intake`'s resolver-callback shape (see § "Chunk
intake driver" in the package overview) is the pattern a caller should copy
rather than taking a conversation id off the wire directly.

- **A containment fixture that leaves the escaped target empty can pass for
  the wrong reason.** The sibling-conversation symlink row — the one row that
  separates full-path equality from a `filepath.Rel`-style containment test,
  per the `EnsureDir` note above — has to populate its target with a real
  stored file before the row means anything; against an empty target, a
  correct build refuses by containment and a broken build refuses by finding
  nothing to answer, and outside the test the two reasons look identical.
  `EnsureDir`'s own containment rows assert `assertEmptyDir` because that
  function *creates*; the analogous assertion for a function that only reads
  is `assertDirEntries(target, "<planted name>")` — the target is unchanged,
  which is the property actually under test.

Concurrency is unguarded by design, on the same bound `EnsureDir` states: the
check-then-use window between `ReadDir` and a caller's later `os.Open` is
real and accepted rather than closed, because closing it would mean
answering an open file handle instead of a path, and the consumer this
ticket was written for (#2038, prompt composition) needs the path as text and
never opens the file at all. Exploiting the window needs write access inside
the daemon's own `0o700` state directory, which already permits rewriting
`devices.json` — strictly worse than redirecting one attachment read.

**#2038's security review found the window is *wider* than that framing implies, not narrower.** For the retrieval leg the path is opened immediately; for prompt composition the path sits in `msgqueue`'s backlog and may wait a whole claude turn before claude's `Read` tool opens it. The attacker capability required is unchanged (the same `0o700`-directory write access above), so the accepted bound still covers it — but a future caller reasoning from "the window is short" rather than "the window needs privileged write access regardless of length" would be reasoning from the wrong invariant.

See [Mutation-testing lessons](attachments-package-mutation-testing-lessons-measured-across.md)
for two more traps this ticket's suite found: a validate-then-look-up table
built entirely from fixtures that are also absent, and a "stable across
calls" assertion that a wrong deterministic rule satisfies just as well.
