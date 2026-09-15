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

Nothing calls this store yet. #2455 writes a note from the outgoing session's
wrap-up reply and reads the previous one back into the next wrap-up prompt;
\#2468 needs `HandoffNotePath`'s path and existence answer to compose a
pointer line, omitted entirely when there is no note.

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
Re-validating here would be a second opinion in a second place. #2455 is where
this obligation next has to be honoured.

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

## No log line carries note text

The store holds no logger and takes none — AC is structural rather than
disciplinary here, since there is no field a fragment could reach. Errors
interpolate only paths and wrapped OS errors; the non-canonical-id error
echoes the rejected id with `%q` (never `%s`), `systemPromptPathFor`'s
precedent, so a control character or newline in a malformed id cannot forge a
log line.

See [`writeMCPSettings`](sessions-package-key-types-writemcpsettings-session-settingspath.md)
and [`writeSystemPrompt`](sessions-package-key-types-writesystemprompt-systemprompttext.md)
for the placement and atomic-write recipe this store copies, and
[docs/specs/architecture/2467-handoff-note-store.md](../../specs/architecture/2467-handoff-note-store.md)
for the full design and security review.
