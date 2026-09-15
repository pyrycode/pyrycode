# #2467 — a per-conversation handoff note store on disk

## Files read

- `internal/sessions/settings.go` → `writeMCPSettings` — the placement and
  atomic-write recipe this store copies, and its doc's reasons for each step.
- `internal/sessions/systemprompt.go` → `systemPromptPathFor` — the derivation
  shape, the `ValidID` gate AC #3 asks this store to match, and the
  persistence-disabled branch.
- `internal/sessions/systemprompt.go` → `writeSystemPromptFile` — the write half,
  and why 0600 once a payload stops being a public constant.
- `internal/sessions/systemprompt.go` → `sessionPromptsDirFor` — the directory
  this store must be a sibling of: both its callers remove it wholesale.
- `internal/sessions/systemprompt.go` → `conversationPrompt` — the precedent for
  a total read (nil registry, unknown id, absent value all give `""`).
- `internal/sessions/systemprompt.go` → `admissibleClientField`, `clientSection` —
  what a composing site owes untrusted bytes; named by this store's doc.
- `internal/sessions/pool.go` → `New` — the `session-prompts` startup purge AC #1's
  restart clause runs through; `dataDir` — the derivation.
- `internal/sessions/pool.go` → `Run` — the shutdown defer clearing
  `session-prompts`; AC #4 requires this store gain no sibling there.
- `internal/sessions/pool.go` → `Remove` — removes `settingsPath` and
  `systemPromptPath`; AC #4 pins that the note survives exactly this path.
- `internal/sessions/pool.go` → `RotateID`, `rekeyLocked` — the `/clear` rotation,
  which re-keys in place, so a conversation-keyed note cannot move.
- `internal/conversations/id.go` → `ValidID` — the predicate gating the path join.
- `internal/sessions/pool_system_prompt_test.go` → `helperPoolWithConversations`,
  `TestPool_Remove_RemovesSystemPromptFile` — the pool helper and the teardown
  assertion shape AC #4's tests mirror.
- `internal/sessions/pool_mcp_settings_test.go` → `helperPoolFakeRunner` — a pool
  with `New`'s on-disk effects and no child; AC #1's restart clause needs two.
- `internal/sessions/settings_test.go` → `TestWriteMCPSettings_DataDirPathAndModes`
  — the directory- and file-mode assertion shape.
- `docs/knowledge/features/sessions-package-key-types-writemcpsettings-session-settingspath.md`,
  `…-writesystemprompt-systemprompttext.md` — the entries the new one sits beside.
  The overview has been split: `sessions-package.md` is now a map.

## Context

A conversation reset runs a wrap-up turn whose reply becomes a handoff note for
the successor session (Juhana, 2026-09-06). The daemon writes that file from the
outgoing session's reply text, so a reset never waits on a claude file-write
permission prompt.

This slice is the store alone — the on-disk contract two filed consumers call.
#2455 writes a note and reads the previous one back into the wrap-up prompt;
#2468 needs the note's absolute path and an existence answer *without* reading
the text, because the pointer line it composes names the path and is omitted
when there is no note. Nothing in this ticket calls the store.

No ADR is warranted: this follows `writeMCPSettings`' already-recorded placement
and write decisions rather than setting a new boundary.

**Out of scope, per the ticket:** removing a note when its conversation is
deleted. The ticket calls such an orphan inert, and as a *key* it is. As *bytes*
it is not: a note is a fragment of the operator's conversation, and this is the
first store in the data dir retaining conversation-derived text indefinitely. Its
nearest neighbour does the opposite — `session-prompts` is purged at `Pool.New`
and removed at `Run`'s defer *because* it carries operator text. Here durability
is the requirement (a note must reach a successor across a restart) and unbounded
retention is its price. Named so the reaper ticket has something to point at.

## Sizing position

The written total lands slightly over the 800-line boundary: this plan, one
production file, one test file. A split was considered and rejected on the floor
rule, not waived. Every candidate child — write+read for #2455, path+exists for
#2468 — has exactly one consumer, and that consumer is a sibling in the same
#2454 family, so each child would be part of its sibling rather than a ticket.
The ticket body makes the same call ("build the store against its two filed
consumers rather than against a bare read/write pair"). Depth permits a split
(parent #2454, no grandparent); the floor is what forbids it, and the floor wins
over the ceiling. The overage is stated rather than argued away.

## Design

One new production file, `internal/sessions/handoff.go`. **No existing
production file changes** — a property of the design, not an accident: AC #4
requires the three removal sites gain no sibling that touches the note.

### On-disk shape

`<dataDir>/handoff-notes/<conversation-id>.txt`, where `dataDir` is the
absolutised parent of `Config.RegistryPath` — the directory holding
`sessions.json` and `conversations.json`.

A sibling of `session-settings/` and `session-prompts/`, and a tenant of
neither: `writeMCPSettings`' doc promises a `*.json` glob of the former counts
sessions exactly, and both `sessionPromptsDirFor` callers remove the latter
wholesale. A note in either directory would break a promise or be deleted.

Keyed by conversation id, not session id: the note must outlive the session that
produced it — a reset replaces the session, and the note is for its successor —
and must survive a daemon restart for the same reason.

### Constants and errors

- `handoffNotesDir = "handoff-notes"` — unexported, `sessionPromptsDir`'s shape.
- `MaxHandoffNoteBytes = 16 << 10` — exported: #2455 composes a prompt around a
  bounded value and should be able to name the bound.
- `ErrHandoffNotesDisabled` — sentinel for the write when persistence is off.

### Surface

Three `*Pool` methods over `conversations.ConversationID`. Methods rather than
package functions because, unlike `writeMCPSettings`, nothing here runs before
the `*Pool` literal exists; the package already imports `internal/conversations`,
so no new dependency edge.

- `WriteHandoffNote(id, text) (path string, err error)` — truncate, derive,
  `MkdirAll` 0700, atomic write, return the absolute path. Replaces any previous
  note for that id.
- `HandoffNote(id) (string, error)` — the bytes, or `""` when there is no note.
  Total over absence for `conversationPrompt`'s reason: a first reset has no
  predecessor, and that is not an error. Absent and empty are indistinguishable
  here, which is correct for the one reading consumer — both compose the same
  prompt — and `HandoffNotePath` is where existence is asked.
- `HandoffNotePath(id) (path string, exists bool, err error)` — #2468's method.
  Answers from `os.Lstat` and reports anything not a regular file as **absent**;
  never reads the text. `Lstat` because the question is "is there a note here",
  and a symlink is not a note this store wrote (see the security review).
  Side-effect free: it does **not** create the directory, which is where it
  departs from `systemPromptPathFor`.

Internal helpers: `handoffNotePathFor(registryPath, id) (string, error)` for the
derivation and the `ValidID` gate, `writeHandoffNoteFile(final, text) error` for
the atomic write, `truncateHandoffNote(text) string` for the cap.

### The persistence-disabled case

`registryPath == ""` is the test-only mode `Pool.dataDir` reports as `""`.
`writeMCPSettings` and `writeSystemPromptFile` answer it with a random name in
`os.TempDir`. **That answer is refused here.** A note an OS reaper may delete is
not a note, and AC #1's round trip across a fresh pool cannot hold in a temp
file. The doc comments carry this:

- `WriteHandoffNote` → `ErrHandoffNotesDisabled`. There is no honest success to
  report, and a no-op returning `("", nil)` would claim one. A sentinel, so #2455
  can recognise it and decide — a failed note must never fail a reset.
- `HandoffNote` → `("", nil)`; `HandoffNotePath` → `("", false, nil)`. Both true:
  no store, so no note and no path. Totality keeps every existing test pool,
  which has no `RegistryPath`, on the ordinary path.

### The cap

`truncateHandoffNote` is the identity at or under `MaxHandoffNoteBytes`. Past it
the cut is at the cap walked back to the nearest rune start, so a rune straddling
the budget is dropped whole rather than left as a prefix. The walk-back is
bounded at three bytes (`utf8.UTFMax-1`); no rune start within it means the input
is not valid UTF-8 there, and the hard cut is taken. Truncating rather than
rejecting is the ticket's ruling: a too-long reply still carries a usable handoff.

The same function runs on the read, over a read bounded at
`MaxHandoffNoteBytes+1` — so an over-cap file, which this store did not write,
yields a bounded, rune-safe answer instead of an allocation sized by corruption.

### Content posture

Bytes are written verbatim apart from the truncation: no UTF-8 repair, no control
character filtering, no trimming. The store bounds **size and mode**; what may
appear in a composed prompt is the composing site's business — re-validating here
would put a second opinion in a second place, `composeSystemPrompt`'s argument
for operator bytes.

That makes `HandoffNote`'s result untrusted text whose `string` type says nothing
about it. So the doc comment hands the obligation on in the imperative rather
than disclaiming it: a composing site places these bytes the way `clientSection`
places a client's, and `admissibleClientField` is the precedent for what that
costs. It exists because getting this wrong is easy, and #2455 must not have to
rediscover it.

## Concurrency model

No goroutines, no locks. The methods read `p.registryPath` and touch no other
pool state, so they cannot participate in the documented capMu → mu → lcMu order.

Reading that field unlocked is the package's existing posture (`Pool.dataDir` and
`Run`'s defer both do it) and *not*, as a first draft of this plan claimed, a
consequence of immutability — the test helper `setRegistryPath` swaps it under
`p.mu`. No production path writes it after `New` and no test swaps concurrently
with a handoff call, so the read is race-free exactly as `dataDir`'s is.

Two concurrent writes for one id race on the rename and the loser is the note —
`writeMCPSettings`' accepted position, since a rename hands a reader the complete
old file or the complete new one, never a prefix. Resets are serialised per
conversation in the filed design, so nothing takes that race.

## Error handling

| Failure | Answer |
|---|---|
| Non-canonical conversation id | Hard error from `handoffNotePathFor`, gating all three methods — matches `systemPromptPathFor`, and keeps a separator or `..` out of the join. |
| Persistence disabled | `ErrHandoffNotesDisabled` on write; `""` / `false` on read and path. |
| Note absent | `("", nil)` from `HandoffNote`; `(path, false, nil)` from `HandoffNotePath` — the path is still derivable and still returned. |
| A step after scratch-file creation fails | Remove the scratch file and wrap, at every step, as `writeSystemPromptFile` does. |
| Read fails other than by absence | Wrapped error, not swallowed: a caller is entitled to know the disk refused. |

Errors wrap as `fmt.Errorf("sessions: …: %w", err)`, and **no error and no log
line carries any fragment of the note.** The store logs nothing and holds no
logger, which its doc states as AC #5 requires. Paths do appear in errors, public
in the same sense the argv record makes `session-prompts` paths public.

## Testing strategy

One new file, `internal/sessions/handoff_test.go`.

- **Round trip and replacement** — write, read back byte-identical; write again,
  read back the second; the directory holds exactly one entry, so nothing
  accumulates and no scratch file is left behind.
- **Path, modes, cold start** — over a data dir where neither `sessions.json` nor
  `handoff-notes/` exists: the path is `<dataDir>/handoff-notes/<id>.txt` and
  absolute, the directory is 0700, the file has no group or world bits.
- **Truncation** — table over `truncateHandoffNote`: under the cap, exactly at it,
  one byte over, and a multi-byte rune straddling the boundary. Each asserts the
  result is within the cap, is a prefix of the input, and is valid UTF-8 when the
  input was. Plus one over-cap write asserting the file's size.
- **Non-canonical id** — a table (empty, uppercase, `..` segment, separator, wrong
  length) rejected by all three methods, with nothing created on disk.
- **Existence without a read** — `HandoffNotePath` gives the derived path with
  `exists == false` before the write and `true` after, over sentinel text the test
  never passes through the reader.
- **Survives a fresh pool over the same data dir** — `helperPoolFakeRunner` twice
  over one `RegistryPath`, running `New`'s purge in between; the note is unchanged.
- **Survives teardown, `/clear`, shutdown** (AC #4) — one test per site, each
  mirroring the existing assertion for the file that *is* removed there:
  `Pool.Remove` (settings file gone, note present), `Pool.RotateID` (note present
  at an unchanged path), `Pool.Run` returning on cancel (`session-prompts` gone,
  note present).
- **No log line carries the note** (AC #5) — a pool over a capturing `slog`
  handler at `LevelDebug`, the shape the two existing prompt/settings tests use.
  Drive every method including the failure paths over sentinel text, then assert
  the buffer holds no substring of it.
- **Persistence disabled** — no `RegistryPath`: the write gives
  `ErrHandoffNotesDisabled` under `errors.Is`, the read `""`, the path `false`,
  and `os.TempDir` gains no `pyry-handoff-*` entry.

## Open questions

1. Should `HandoffNote` distinguish absent from empty? Resolved: no —
   `HandoffNotePath` carries existence and the one reading consumer composes the
   same prompt either way. Revisit only if #2455 needs the distinction.
2. Should the store refuse to read a file it did not write? Resolved in the
   security review: the read does not refuse — the 0700 directory is the boundary
   and the bounded read makes a planted file harmless — but `HandoffNotePath`
   answers `Lstat` and calls a non-regular file absent, as the correct predicate
   for its own question rather than an added defence.

## Documentation handoff

**Pending — owned by the documentation stage, not this ticket.**

Record the store in the sessions package overview § Key Types, beside the
`writeMCPSettings` and `writeSystemPrompt` entries. The overview has since been
split: `docs/knowledge/features/sessions-package.md` is now a map, and those two
entries live in `sessions-package-key-types-writemcpsettings-session-settingspath.md`
and `sessions-package-key-types-writesystemprompt-systemprompttext.md`. The new
entry belongs beside them, per the split rule that a section under 3000 bytes
stays in the parent.

It must record: the on-disk location
(`<dataDir>/handoff-notes/<conversation-id>.txt`, a sibling of
`session-settings/` and `session-prompts/` and a tenant of neither), the
conversation-id key and why it is not the session id, the `MaxHandoffNoteBytes`
cap with rune-safe truncation, the persistence-disabled decision, and that the
note is deliberately **not** removed at session teardown, `/clear` or shutdown.
Two tickets build on this contract before anything else reads it.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** SHOULD FIX, fixed in the plan. The design is deliberately
  *not* the content boundary for note text (`composeSystemPrompt`'s argument
  against a second opinion in a second place), which is right, but the first draft
  discharged that with a disclaimer. `HandoffNote` returns untrusted,
  claude-authored text as a bare `string`, and the next hand to hold it is #2455's
  prompt composer. *Content posture* now states the obligation in the imperative
  and names `admissibleClientField` as the precedent. The size/mode boundary is
  explicit and single: `handoffNotePathFor` gates the path, `truncateHandoffNote`
  the size.
- **[Tokens, secrets, credentials]** No tokens. The confidentiality concern is the
  note itself, held at 0600 in a 0700 directory — `writeSystemPromptFile`'s posture
  since #2150, for its stated reason. The real finding is *retention*, not mode:
  this is the first store in the data dir keeping conversation-derived text
  indefinitely where its neighbour is purged twice over. Durability is the
  requirement, so this is correct; see OUT OF SCOPE, and *Context* now names the
  cost instead of calling an orphan inert.
- **[File operations — path traversal]** No finding. `conversations.ValidID` gates
  before every join, in `handoffNotePathFor`, which all three methods funnel
  through — not only the write. Its shape (36 chars, lowercase hex, fixed dashes)
  admits no separator, no `..`, no NUL, so the join cannot escape the data dir.
  Load-bearing rather than decorative precisely because this package does not mint
  the id it is handed.
- **[File operations — symlinks, TOCTOU]** SHOULD FIX, fixed in the plan.
  `os.Stat` follows symlinks, so a link planted at `handoff-notes/<id>.txt` would
  make `HandoffNotePath` report `exists = true` and hand #2468 a path it then
  names to claude — read amplification aimed at any file the daemon can read. What
  actually holds is the 0700 directory: planting the link needs the daemon's uid
  or root, and at that point the file is readable directly. The plan uses
  `os.Lstat` and calls a non-regular file absent anyway — not defence in depth,
  but the correct predicate for the question. The read deliberately does not grow
  `O_NOFOLLOW`: `syscall`-level and platform-dependent for a boundary the
  directory mode already holds. Separately, `HandoffNotePath` is genuinely
  check-then-use — #2468 stats, names the path, claude reads later — benign
  because nothing in the design removes a note, and named here so a future reaper
  knows it invalidates a live contract.
- **[File operations — permissions]** Accepted limit. `os.MkdirAll` returns nil for
  an existing directory without correcting its mode, so a `handoff-notes/` that
  somehow exists at 0755 stays there. `writeMCPSettings` carries the same latent
  gap; matching the neighbour beats a lone mode-repair step here.
- **[File operations — atomic writes]** No finding. Scratch file in the target
  directory, fsync, rename, at `os.CreateTemp`'s 0600 preserved through the
  rename — `writeMCPSettings`' recipe, so a reader gets the complete old file or
  the complete new one, and `os.Rename` replaces a symlink at the destination
  rather than writing through it. The dotted `.handoff-*.txt.tmp` pattern keeps a
  SIGKILL-orphaned scratch file from being mistaken for a note.
- **[Subprocess / external execution]** No exec here, but worth stating because it
  is the point of the feature: the path this store returns is named to claude by
  #2468, and claude has file-read tools, so the path is effectively an instruction
  to read that file. Exactly why the only caller-supplied component of the path is
  a `ValidID`-gated UUID.
- **[Cryptographic primitives]** Not applicable. The conversation ids that become
  filenames come from `conversations.NewID` (`crypto/rand`), minted elsewhere;
  this store validates rather than generates.
- **[Network & I/O — input size limits]** No finding, and the category that
  produced the read cap. The write cap alone would leave `HandoffNote` doing an
  unbounded `io.ReadAll` over whatever is on disk. The read is bounded at
  `MaxHandoffNoteBytes+1` and passed through `truncateHandoffNote`, so an over-cap
  file yields a bounded, rune-safe answer. No sockets, no timeouts to set.
- **[Error messages, logs, telemetry]** No finding, one rule made explicit. The
  store holds no logger and takes none, so AC #5 is structural rather than
  disciplinary — there is no field through which a fragment could be logged — and
  the doc says so. Errors carry paths and wrapped OS errors, never note bytes. The
  non-canonical-id error echoes the rejected id, which is caller-influenced, so
  `%q` (never `%s`) is mandatory there, as `systemPromptPathFor` already does it:
  a control character or newline in a malformed id cannot forge a log line.
- **[Concurrency]** No finding, one plan error corrected. No goroutines, no locks,
  so no ordering to invert. The first draft justified the unlocked
  `p.registryPath` read by calling the field read-only after `New`; it is not —
  `setRegistryPath` swaps it under `p.mu`. The read is race-free for the real
  reason instead, stated in *Concurrency model*. Two concurrent writes for one id
  race on the rename and the loser is the note, `writeMCPSettings`' accepted
  position, and the filed design serialises resets per conversation.
- **[Threat model alignment]** Out of scope, named. Nothing here crosses the wire,
  so no `docs/protocol-mobile.md` § Security model threat applies to this slice.
  #2455 (note text into a prompt) and #2468 (note path into a prompt) are where
  the composed result becomes reachable from a remotely-sent frame; each owns that
  alignment.
- **[OUT OF SCOPE]** Two items for the reaper ticket the feature owner files, both
  consequences of this store having no sweeper by design: a note is never removed
  when its conversation is deleted (the ticket's own deferral), and a scratch file
  orphaned by a SIGKILL inside the write window is likewise never collected, where
  `session-prompts` is purged wholesale at startup. Both accumulate only across
  those events, neither is exploitable, and the round-trip test's "exactly one
  entry" assertion pins the normal path.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-16
