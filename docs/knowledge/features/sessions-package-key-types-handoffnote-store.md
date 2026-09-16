# Handoff note store: `WriteHandoffNote` / `HandoffNote` / `HandoffNotePath` (#2467)

```go
const MaxHandoffNoteBytes = 16 << 10
var ErrHandoffNotesDisabled error

func (p *Pool) WriteHandoffNote(id conversations.ConversationID, text string) (string, error)
func (p *Pool) HandoffNote(id conversations.ConversationID) (string, error)
func (p *Pool) HandoffNotePath(id conversations.ConversationID) (path string, exists bool, err error)
```

The on-disk contract a conversation reset's wrap-up reply is written through:
one file per conversation id at `<dataDir>/handoff-notes/<conversation-id>.txt`,
a sibling of `session-settings/` and `session-prompts/` and a tenant of
**neither** — `writeMCPSettings`' doc promises a `*.json` glob of the settings
directory counts sessions exactly, and both `sessionPromptsDirFor` callers
remove the prompts directory wholesale, either of which a note placed inside
would break or lose. Written atomically (scratch file, fsync, rename,
`os.CreateTemp`'s 0600 preserved through the rename) in a directory created at
0700, `writeMCPSettings`'/`writeSystemPromptFile`'s recipe verbatim.

\#2477 writes a note from the outgoing session's wrap-up reply and reads the
previous one back into the next wrap-up prompt. #2475 is the reading
consumer: `(*Pool).handoffNoteFor` reads the note's *text* through
`HandoffNote` and composes it, fenced, into the appended system prompt every
successor spawns with — see
[`writeSystemPrompt` + `systemPromptText`](sessions-package-key-types-writesystemprompt-systemprompttext.md#carrying-the-conversations-handoff-note-2475).
That supersedes this ticket's original shape: #2475 was filed as a *pointer*
that named `HandoffNotePath`'s path and let the successor decide whether to
read it, and #2468 was the ticket that would have consumed that answer.
\#2474 measured the pointer shape dead — a `Read` outside the workspace is
gated under the daemon's in-band `default` posture, so a background
conversation has nobody to answer the modal — and #2475 shipped inline
composition instead. `HandoffNotePath` keeps a production consumer under the
new shape too: `handoffNoteFor` calls it before ever opening the file, not to
answer existence for a pointer line, but to gate the read itself — see
below.

## Keyed by conversation, not session — and never removed

Every other per-id file in this package (`session-settings/`,
`session-prompts/`) is keyed by *session* id and removed when that session
goes away: `Pool.Remove`, the `/clear` rotation's re-key, and `Pool.Run`'s
shutdown defer / `Pool.New`'s startup purge (the latter two clearing
`session-prompts/` wholesale). A handoff note inverts both halves of that
convention. It is keyed by **conversation** id because it must outlive the
session that produced it — a reset replaces the session, and the note is for
the *successor* — and it survives a daemon restart for the same reason. And it
is **deliberately never removed**: not at `Pool.Remove`, not at rotation, not
at shutdown, not at the startup purge. Teardown, rotation and shutdown tests
pin this against the same assertion shape used for the file that *is* removed
at each site, so a future edit to one of those three removal paths that grows
a handoff-note sibling by copy-paste fails loudly rather than silently.

Reaping a note whose conversation was deleted is out of scope here on
purpose — an orphan keyed by a random conversation id is inert as a key, but
not as bytes: this is the first store in the data dir retaining
conversation-derived text indefinitely, where every neighbour purges on a
schedule. A reaper is a separate decision, not a silent extra in this ticket.

## The persistence-disabled case departs from its neighbours

`writeMCPSettings` and `writeSystemPromptFile` both answer an unconfigured
registry path (`Pool.dataDir() == ""`, the mode most of this package's tests
run in) with a random file under `os.TempDir()`. That answer does not carry
over here: a note an OS age-based reaper may delete is not a note, and the
round trip a restart is supposed to preserve cannot hold in a temp file.
`WriteHandoffNote` instead returns the sentinel `ErrHandoffNotesDisabled` — a
caller can recognise the condition and decide, since a failed handoff note
must never fail the reset it was written for. `HandoffNote` and
`HandoffNotePath` answer the disabled case the same way they answer a merely
absent note (`""`, and `false` with no path derivable), which is correct
because without a data dir there is nowhere a note could be.

## Cap, truncation and content posture

`MaxHandoffNoteBytes` (16 KiB, exported so a prompt composer can name the
bound) truncates rather than rejects — a wrap-up reply that ran long still
carries a usable handoff. The cut walks back from the byte boundary to the
nearest rune start so a multi-byte rune straddling the cap is dropped whole,
never left as a lone continuation byte; the same function runs on the read
path, over a read bounded at `MaxHandoffNoteBytes+1`, so a file larger than the
cap (one this store did not write) still yields a bounded, rune-safe answer.

Past size and mode, the store does no validation: no UTF-8 repair, no
control-character filtering, no trimming. `HandoffNote`'s returned text is
claude-authored, crossed the subprocess boundary, and is **untrusted** —
placing it into a composed prompt is the composing site's obligation, the same
one `clientSection` discharges for a client's name
(`admitClient`/`clientSection`, see
[`writeSystemPrompt`](sessions-package-key-types-writesystemprompt-systemprompttext.md)).
Re-validating here would be a second opinion in a second place. #2477 (the
wrap-up prompt) and #2475 (the appended system prompt, see below) are where
this obligation is honoured.

## `HandoffNotePath` answers `Lstat`, not `Stat`

`HandoffNotePath` reports a symlink at the note path as **absent** rather than
following it. `os.Stat` would follow the link and hand the caller a path it
then names to claude — read amplification against any file the daemon can
read, if a link were ever planted at that path. What actually bounds that is
the 0700 note directory (planting the link needs the daemon's own uid, at
which point the file is readable directly); `Lstat` is the correct predicate
for "is there a note here" rather than an added defence layer, and costs
nothing extra. The read path deliberately does not add `O_NOFOLLOW` — that is
syscall-level and platform-dependent for a boundary the directory mode already
holds.

**#2475's security review found a second reason this gate matters, one the
symlink case alone would never have surfaced: it is load-bearing for
availability, not only for confidentiality.** `(*Pool).handoffNoteFor`, the
composing site, calls `HandoffNotePath` and refuses to read unless it reports
a regular file — *before* `HandoffNote` ever calls `os.Open`. A FIFO planted
at the note path blocks in `open(2)` until a writer appears, and on the
rotation funnel the caller is the relay's single Run dispatch goroutine, so an
ungated read would not fail one spawn — it would hang that goroutine, and with
it all v2 dispatch, forever. `Lstat` answers before any `open` happens, so a
FIFO composes as "no note" instead of a stall. That is why the gate precedes
the read rather than merely accompanying it: without it, "nothing on the
compose path may fail or delay a spawn" — the posture #2475 requires of every
read against this store — would be false. `TestPool_HandoffNoteFor_Total`
drives the FIFO case under a bounded wait rather than a bare call, because a
build that dropped this gate would hang that test rather than redden it.

`HandoffNotePath` was originally the pointer design's method: #2468 would
have used its path-and-existence answer to compose a one-line pointer,
omitted when there was no note. #2474 measured that design dead and #2475
shipped inline composition instead — but `HandoffNotePath` kept a production
consumer under the new shape, for both reasons above, rather than becoming
dead code.

## No log line carries note text

The store holds no logger and takes none — AC is structural rather than
disciplinary here, since there is no field a fragment could reach. Errors
interpolate only paths and wrapped OS errors; the non-canonical-id error
echoes the rejected id with `%q` (never `%s`), `systemPromptPathFor`'s
precedent, so a control character or newline in a malformed id cannot forge a
log line.

**An absolute "no content in logs" rule cannot be discharged by citing a
downstream package's discipline — it has to be enforced at the call site that
could break it (#2477 review).** The conversation reset's wrap-up turn writes
this store from `cmd/pyry`, and its `WriteUserTurn` call carries the composed
prompt — the previous note's bytes included — as its payload. An early
version of that caller logged `WriteUserTurn`'s error verbatim, reasoning
correctly that *this file's* wrapped errors name only paths and OS errors and
never note bytes. That reasoning was true and irrelevant: the value flowing
through that particular error was the rejected prompt from a different
package entirely, and a later edit to either package could make the citation
false without either package's own tests noticing. The fix records that one
error's event key and the conversation id only, never its value — caught by a
test whose fake `WriteUserTurn` returns an error that quotes the payload back,
which is the shape a rule stated this way needs to catch a violation, not a
fake that merely returns a generic error.

## The wrap-up's write side (#2477)

The conversation reset's wrap-up turn writes this store from a reply that
crossed the subprocess boundary the same way the read side's note did, and
runs it through the read side's own admission first —
[`FencedHandoffNote`](sessions-package-key-types-writesystemprompt-systemprompttext.md#the-fence-was-exported-not-the-predicate-2477),
not a second predicate. A reply this predicate refuses (a forged fence
marker, blank after trimming) leaves the previous note standing, the same
outcome every other wrap-up failure produces — see [Inbound new_session § The
wrap-up turn](v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md#the-wrap-up-turn-and-the-replys-tense-2477)
for the coordinator and its full failure table. What is stored is the raw
admitted reply, never the fenced rendering — the fence belongs to whichever
prompt is being composed, and this store's contract stays "holds the note
verbatim."

**Not every failure on this path belongs in that "previous note stands"
table, though — a failed read of the previous note is a different kind of
failure from the rest.** The wrap-up prompt reads the existing note back
(through `HandoffNote`) so its own writer can prune it; if that read fails,
the wrap-up still runs and still produces a new note, just without the old
one to prune against. Every *other* row in the failure table — idle timeout,
empty reply, an admission refusal, a write error — means nothing new is
stored and the existing note is untouched. The two cases were nearly
collapsed into one table during review: a read failure is not a write
failure, and a test asserting "the previous note stands" would pass for the
wrong reason if it were fed a read failure that actually still produced a
fresh note.

See [`writeMCPSettings`](sessions-package-key-types-writemcpsettings-session-settingspath.md)
and [`writeSystemPrompt`](sessions-package-key-types-writesystemprompt-systemprompttext.md#carrying-the-conversations-handoff-note-2475)
for the placement and atomic-write recipe this store copies and for the
reading consumer, and
[docs/specs/architecture/2467-handoff-note-store.md](../../specs/architecture/2467-handoff-note-store.md)
for the full design and security review.
