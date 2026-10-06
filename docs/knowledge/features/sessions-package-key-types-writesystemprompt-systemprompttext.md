# `writeSystemPrompt` + `systemPromptText` (#2093, per-session since #2150, client-named since #2148, handoff note since #2475, fence exported since #2477, read folders named since #2711, working folder joins them since #2720)

```go
func composeSystemPrompt(operator string) string
func composeSystemPromptFor(operator string, clients []ClientIdentity, note string) string
func composeSystemPromptForOn(daemon, instructions, operator string, clients []ClientIdentity, note string) string
func writeSystemPrompt(registryPath string, id SessionID, text string) (string, error)
func (p *Pool) conversationPrompt(label string) string
func (p *Pool) attachedClients(ctx context.Context) []ClientIdentity
func (p *Pool) handoffNoteFor(label string) string
func (p *Pool) handoffNoteWithFreshness(label string) (string, bool)
func (p *Pool) refreshSystemPrompt(ctx context.Context, sess *Session)
func (p *Pool) refreshSystemPromptForRotation(sess *Session)
func (p *Pool) writeComposedPrompt(sess *Session, clients []ClientIdentity)
func (p *Pool) SystemPromptFor(id SessionID) (string, error)
func (p *Pool) DaemonInstructions() string
func (p *Pool) DefaultDaemonInstructions() string
func (p *Pool) SetDaemonInstructions(text string) error
```

Writes the appended system prompt file every interactive claude is spawned
with via `--append-system-prompt-file`. `systemPromptText` is the constant
composed into it: three architecture facts (no terminal, a separate client
renders the reply and may be on a different machine, more than one client can
attach) and nothing else, pinned byte-for-byte by `TestSystemPromptText_Pinned`.
It states no claim about what any client can render or do — a capability claim
here would rot the day a client's rendering changes, the way `last_seen_ts` and
`last_event_id` rotted in [#2090](https://github.com/pyrycode/pyrycode/issues/2090)
and pyrycode-desktop#1068, while an architecture fact does not.

## One file per session, not one file for the daemon

\#2093 wrote a single fixed-name file because the text was identical for every
session. #2150 broke that assumption — a session bound to a conversation with
a stored prompt appends that conversation's own bytes — so the file gained a
per-session identity:

- `id == ""` (the bootstrap, built in `Pool.New` before any conversation
  exists and unable to ever become a conversation's bound session) keeps
  #2093's fixed name, `<dataDir>/system-prompt.txt`, daemon-scoped, written
  once and removed once in `Pool.Run`'s shutdown defer.
- `id != ""` resolves to `<dataDir>/session-prompts/<id>.txt`, a sibling of
  `session-settings/` rather than a tenant of it (that directory's `*.json`
  contents are documented to count sessions exactly). Gated on `ValidID` for
  `writeMCPSettings`'s reason — the id names a file, so a malformed one is a
  hard error rather than a silent temp-file fallback.

`systemPromptPathFor` (derivation) and `writeSystemPromptFile` (the atomic
scratch/fsync/rename write, `writeMCPSettings`'s recipe verbatim, 0600
preserved by the rename) are separate functions for a reason that only shows
up after a `/clear`: **a rotation re-keys a session in place
(`Pool.rekeyLocked`), so the path frozen into `spawnBase` keeps naming the
*pre-rotation* id.** Anything that rewrites the file — see `refreshSystemPrompt`
below — must target `sess.systemPromptPath` verbatim rather than re-deriving
from `sess.id`. Re-deriving after a rotation writes a file no argv names, and
every later spawn silently keeps the stale bytes: nothing reddens, the write
succeeds, the old file is still readable, only the content is wrong.
`writeSystemPrompt` (deriving + writing) is what every *construction* site
uses; a refresh writes `writeSystemPromptFile` directly against the
already-resolved path.

## Composition and resolution

`composeSystemPrompt(operator)` returns `systemPromptText` byte-for-byte when
`operator == ""`, otherwise the constant plus a newline separator plus the
operator's bytes verbatim (untrimmed, unescaped, unbounded here — #2149's
`Registry.SetSystemPrompt` is the single validating door). There is no branch
that returns the operator's text alone: replacing claude's own system prompt
is what `--append-system-prompt-file` exists not to do.

`daemonPromptText(folders)` (#2711) is the actual daemon-wide head every
composition starts from, in place of the bare `systemPromptText` constant:
the constant, plus one sentence naming the folders the live file
reader serves besides a conversation's workspace (#2710) —
`readFolderSentence(folders)` — after the usual newline separator.

Since #2893, the sentence reads: "This daemon serves files of any type under
`<folder list>` to a client that asks for one by absolute path, subject to the
size limit and secret-name refusals." Each folder is backtick-quoted in input
order, separated by commas and a final "and", and the sentence ends in a
newline. `TestReadFolderSentence_Pinned` independently pins this wording and
formatting. The [live-reader contract](v2-session-manager-state-machine-inbound-read-workspace-file-workspacefileread.md)
owns the size bound and two-leaf filename denylist; the sentence does not claim
that eligible files contain no secrets.

`folders` **must** be `resolveReadFolders`'s output, the roots the reader
actually accepts, so the sentence can never name a folder the reader would
refuse; it names only what resolved at startup, nothing a bad entry caused
to be skipped. Since #2720, `runSupervisor` runs that output through
`withWorkdirReadFolder` before either consumer sees it, so the slice —
and therefore this sentence — also names the daemon's own working folder,
deduplicated against a `-pyry-read-folder` entry naming the same folder, and
left out under that ticket's home/`/`/contains-home guard (see
[`workspacefileread`'s working-folder section](v2-session-manager-state-machine-inbound-read-workspace-file-workspacefileread.md#the-daemons-own-working-folder-is-always-a-read-root-too-2720)).
With no folders, or none that resolved, `daemonPromptText` returns
`systemPromptText` byte-for-byte. `composeSystemPromptOn` and
`composeSystemPromptForOn` take this daemon text as their first parameter;
`composeSystemPrompt` and `composeSystemPromptFor` keep their existing
signatures, use the plain `systemPromptText`, and omit daemon instructions.
The bootstrap file written by `sessions.New` contains only
`daemonPromptText(cfg.ReadFolders)`. Conversation construction in
`Pool.buildSessionAs` and refresh in `Pool.writeComposedPrompt` use
`composeSystemPromptForOn` with this five-contributor order:

1. `daemonPromptText(p.readFolders)` — fixed architecture text and the
   read-folder sentence.
2. `Pool.DaemonInstructions()` — the daemon-wide operator instructions.
3. `clientSection(clients)` — admitted attached-client identities and feature
   self-reports.
4. `handoffNoteSection(note)` — the conversation's fenced handoff note.
5. Per-conversation operator text from `Pool.conversationPrompt(label)`.

`ClientIdentity` carries `Name`, `Version` and `Features`, transcribed from
`hello.device_name`, `hello.client_version` and `hello.client_features`.
Descriptions are client self-reports, attributed after the name and optional
version, never daemon capability guarantees. The
[client admission and snapshot rules below](#naming-the-attached-client-2148)
keep each field independently bounded, sort/deduplicate admitted triples, and
retain the activation snapshot through active reconnects and `new_session`
rotation; eviction/reactivation resolves current reports.

Nonempty daemon instructions are included verbatim after one newline; an
empty string adds no section or separator. Each conversation retains its own
operator text last so it can narrow or override the shared instructions.
Construction includes the current instructions and operator text; clients and
the note are resolved at next-start refresh. `daemonPromptText` is computed
fresh at every compose rather than cached on the `Pool`: a cached field would
read `""` on any `Pool` built without going
through `New`'s normal construction path, and `daemonPromptText` would then
silently drop `systemPromptText` itself from that pool's every recompose
rather than merely omit the sentence.

`Pool.conversationPrompt(label)` is the resolver, and it is **total** — nil
registry, empty label, unknown label, and #2149's absent (`nil`) prompt all
return `""` rather than an error, the same posture `rebindConversation` takes
for a nil registry. This flattens #2149's tri-state (nil / explicitly-empty
`""` / operator text) to one predicate — has bytes to append — at the spawn
site, so the tri-state stays in the registry. `label` is the conversation id
at both production call sites (`create_conversation` → `Pool.Mint`, and
`sessionRouter.revive` → `Pool.Revive`), so no mint/revive signature widened
to carry a resolver.

A prompt saved while a session is running reaches that conversation's next
child regardless of which of three funnels grows it. A first message and a
revive both go through `Pool.Activate` → `refreshSystemPrompt`; a
`new_session` rotation goes through `Pool.RotateForNewSession` →
`refreshSystemPromptForRotation` (#2436), called after the rotation's `p.mu`
release and before its `notifyTransition` fan-out, so the write lands before
`RestartFresh` cancels the live child. Both funnels share the actual compose
step, `(*Pool).writeComposedPrompt(sess, clients)`: resolve the operator bytes
via `conversationPrompt`, resolve the conversation's handoff note via
`handoffNoteFor` (#2475), read `DaemonInstructions`, compose through
`composeSystemPromptForOn`, write `sess.systemPromptPath` verbatim, and record
what the session was composed
with. Because both resolves and the write live in this one shared step, this
is also the single lookup site for the note across all three spawn paths that
carry one — first spawn, revive, and the rotation recompose — rather than
three call sites that could drift. What differs between the two funnels is
only the guards each puts in front of that call —
`refreshSystemPrompt` skips an already-`stateActive` session and resolves
clients through `attachedClients`; `refreshSystemPromptForRotation` runs
unconditionally, since nothing a rotation does leaves `stateActive`, and
resolves no client identity at all (see below).

### Durable daemon-wide instructions (#2766)

`Pool.DaemonInstructions` returns the current text;
`Pool.DefaultDaemonInstructions` returns the exact built-in default, whose
responsiveness and delegation guidance is pinned independently by
`TestDaemonInstructionsStartup`. Clear with `SetDaemonInstructions("")`;
reset with `SetDaemonInstructions(p.DefaultDaemonInstructions())`. The setter
preserves valid UTF-8 verbatim, including whitespace, through the inclusive
`conversations.MaxSystemPromptBytes` bound of **8192 bytes**, not runes.
`ErrDaemonInstructionsTooLong` and `ErrDaemonInstructionsInvalidUTF8` are
distinguishable validation failures, and neither changes memory or disk.

The setting lives at `<Pool.dataDir()>/daemon-instructions.json`, separate
from `conversations.Registry` and transient prompt files. `sessions.New`
seeds and eagerly persists the default only when that setting file is absent,
on both fresh installs and upgrades with an existing sessions registry.
Stored custom text and an explicitly empty string survive reconstruction;
empty is a durable clear, never a request to reseed. Unreadable, malformed,
nonregular (including symlink), oversized or invalid-UTF-8 stores fail startup,
as does failure to persist the initial default. With persistence disabled,
startup seeds memory only and the setter writes no setting file.

Writes use `writeSystemPromptFile`'s same-directory 0600 tempfile, sync,
close and atomic rename, creating the daemon directory with 0700 permissions
when needed. `SetDaemonInstructions` publishes memory only after persistence
succeeds; a persistence failure returns an error and retains the prior memory
and stored value. Reads and writes use a dedicated `instructionsMu` RWMutex,
with the setter holding its write lock through persistence and publication.
It never takes `Pool.mu`: `buildSessionAs` can already hold that lock when it
reads instructions, so sharing the pool lock would deadlock construction.
Errors and logs contain no instruction text, including load, validation and
persistence failures.

**Validate original bytes before a decoder can repair them.** An ordinary
JSON string can silently replace invalid UTF-8, making subsequent validation
accept text the operator never supplied. `daemonInstructionsStore` instead
stores a required `instructions` field as base64-encoded `[]byte`; the loader
bounds reads, rejects missing/null fields, and validates the decoded original
bytes. Malformed-store errors omit parser details that could echo private
text. This representation also distinguishes a stored empty byte string from
an absent setting.

`SetDaemonInstructions` changes only the durable setting and pool memory.
It never restarts or interrupts an active child or rewrites its composed
prompt file. Each conversation reads the latest value at its next composition
for first activation, inactive reactivation/revival or `new_session` rotation;
the bootstrap retains its separate prompt lifecycle and receives none of
these instructions. Session removal, rotation, daemon shutdown and startup's
transient-prompt purge leave the setting intact. See
[the design](../../specs/architecture/2766-daemon-instructions.md) and
[the next-start evidence below](#the-mint-window-trap-this-design-exists-to-avoid).

## Naming the attached client (#2148)

`composeSystemPromptFor(operator, clients, note)` names the clients attached *at
compose time* — spawn-time, not per-turn, and never restated mid-session,
because the appended prompt file is read once when the child spawns. It
**delegates to `composeSystemPrompt(operator)` whenever `clientSection(clients)`
and `handoffNoteSection(note)` both return `""`** (since #2475 both optional
sections, not just the client one), rather than branching on an equivalent
condition, so the no-clients / all-fields-empty path is byte-identical to the
pre-#2148 text *structurally* — `TestSystemPromptText_Pinned` and
`TestComposeSystemPrompt` needed no call-site edit and no re-transcription of
the pin for either ticket.

`(*Pool).attachedClients(ctx)` is the resolver, and — like `conversationPrompt`
— it is **total**: no resolver installed, a resolver returning `nil`, and an
already-cancelled ctx all yield `nil` rather than blocking or erroring. Unlike
`conversationPrompt`, whose totality is a property of the registry lookups it
performs, `attachedClients`' totality had to be asserted explicitly: an
early implementation wrapped the caller's ctx in a bounded timeout and called
the resolver regardless, so "an already-cancelled ctx yields no identity" was
only true because the production resolver happened to check `ctx.Err()`
itself. Its own test (`TestPool_AttachedClients_Total`) caught it once a
resolver that *doesn't* check ctx was substituted. **A totality contract has
to be enforced at the symbol whose doc states it, not borrowed from whichever
implementation currently satisfies it** — the same trap `conversationPrompt`
avoids by performing the check itself rather than trusting its callers.

`admitClient` is the one untrusted→trusted door all three fields cross through
before any can reach the file: valid UTF-8, non-blank after trimming,
within its byte bound, and every rune display-safe (no C0, no C1/DEL, no
`"`, since the admitted value is rendered inside quotes and the delimiter
itself must be refused for no value to close the structure around it). An
inadmissible name drops the whole client; an inadmissible version or description
drops only that field, independently of the other. The inclusive UTF-8-byte
bounds are `maxClientNameBytes` (64), `maxClientVersionBytes` (32) and
`maxClientFeaturesBytes` (512), not rune counts. Empty, whitespace-only,
oversized and invalid-UTF-8 descriptions are silently omitted, as are reports
containing a refused control or quote. Accepted text is verbatim; trimming
checks only blankness. **Refusal, not truncation or escaping** —
`MaxWorkspaceLabelBytes`'
posture: the byte bound alone is a cost control, never a safety claim, and the
character-set refusal is what actually holds the prompt's structure.
`clientSection` then sorts by name, version and description, deduplicates
identical admitted triples, and renders the set as one daemon-authored,
quoted transcription. Differing descriptions remain separate even when name
and version match; more than `maxNamedClients` distinct admitted triples
collapses to no section at all rather than a truncated list under a sentence
that claims completeness. The admit-sort-dedup-cap prologue is its own
function, `admittedClients(clients) []ClientIdentity`, and `clientSection`
renders whatever it returns; the split exists so #2436 can *retain* an
admitted set on `Session.promptClients` rather than only ever render one
inline. `admittedClients` is idempotent over its own output — every predicate
already holds and the set is already sorted and deduplicated — so re-admitting
a carried set reproduces the section byte-for-byte.

Each admitted description appends ` (self-reported features "<description>")`
after the optional ` (version "<version>")`. `clientSectionLead` and all
prompt bytes for absent/empty descriptions remain unchanged.
`TestClientSectionFeaturesText_Pinned` independently pins the new rendering
alongside the unchanged `TestClientSectionText_Pinned` and
`TestSystemPromptText_Pinned`. Values remain inside the daemon-authored
sentence; refusal of CR/LF, controls and quotes prevents a client from
authoring a separate prompt line or closing its quoted span. Attribution
does not validate a feature claim's truth or prevent semantic prompt injection.

**Never resolve client identity from the relay's Run goroutine (#2436).** A
`new_session` rotation's entire dispatch — `handleNewSession` →
`StartNewSession` → `startFreshRunner` → `RotateForNewSession` — executes on
`V2SessionManager`'s single Run dispatch goroutine, and `attachedClients`
funnels its request back onto that same goroutine and waits for a reply.
Called from there it cannot be answered: it would stall all v2 dispatch for
`clientIdentityTimeout` and then return `nil`, silently dropping the section —
and since `docs/protocol-mobile.md` § Security model leaves rate limiting
deferred (threat 7), that stall is reachable once per remotely-sent
`new_session` frame. `refreshSystemPromptForRotation` resolves nothing on this
path; instead `Session.promptClients` carries the admitted set the session's
last compose produced forward into the rotation's recompose. This is licensed
by `clientSectionLead`'s past tense above — the section already claims only
who was attached *when this session started*, never a live per-turn fact, so
carrying a prior resolve forward preserves that attribution. Feature reports
may become stale mid-session: reconnect leaves an active prompt unchanged,
rotation carries the earlier admitted report, and eviction/reactivation
resolves current clients. `promptClients` is written and read under `Pool.mu`,
`systemPrompt`'s discipline exactly and deliberately not `lcMu`, and it stores only
`admittedClients`' output — never a resolver's raw answer — so retention never
becomes a second place for unadmitted remote-authored bytes to live, and is
bounded at `maxNamedClients × (maxClientNameBytes + maxClientVersionBytes +
maxClientFeaturesBytes)` per session rather than by however many conns one
client holds.

**A resource bound and a display-validation door are different concerns and
belong in different packages.** `internal/relay` retains `DeviceName`,
`ClientVersion` and `ClientFeatures` verbatim after successful token
authentication but drops (never truncates) a value over the respective inclusive
`maxRetainedClientNameBytes` (256), `maxRetainedClientVersionBytes` (64) or
`maxRetainedClientFeaturesBytes` (1024) bound — a memory/copy-cost
ceiling at the point an authenticated client can park bytes that get copied into
every `ActiveConn` snapshot the fan-out takes per turn. `admitClient` here owns
the character set and the *display* bound instead. The instinct to fold both
into "the one validating door" would have put a memory-safety concern behind a
door that only runs when a session is about to spawn — the wrong place to stop
an allocation multiplier. See
[the relay enumeration doc](v2-session-manager-state-machine-concurrency-safe-open-session-enumeratio.md)
for the retention side.

**`maxClientVersionBytes` (32) constrained a wire-format decision made two layers away (#2576).** The compatibility-policy ticket that defined `client_version`'s `<app>/<MAJOR>.<MINOR>.<PATCH>` shape almost missed this bound: its first pass capped the format at 64 bytes, matching `internal/relay`'s separate `maxRetainedClientVersionBytes` retention ceiling above, and only a security-review pass caught that a 33–64-byte well-formed version would retain into the session but then be silently dropped here at the *display* door, never reaching the prompt. The format is now capped at 32 bytes for that reason — the longest plausible value, `pyrycode-desktop/100.100.100`, is 28 bytes — so no well-formed `client_version` can pass `internal/relay`'s retention gate and then fail this package's admission gate. A wire-format ticket that adds a new bounded field should check both this package's admission constants and `internal/relay`'s retention ones before picking a length, not just one of the two.

See [docs/specs/architecture/2148-client-identity-system-prompt.md](../../specs/architecture/2148-client-identity-system-prompt.md)
for the full design, the trust-boundary walk, and the security review.

### The version rule was exported, not duplicated (#2577)

`internal/relay` needed the same admission rule for a second purpose — persisting
`client_version` onto a paired device's `devices.json` record for `pyry pair
list` — and `admitClient`'s version arm is now reachable as the standalone
`AdmitClientVersion(v string) string`, which `admitClient` itself calls so the
character-set-plus-`maxClientVersionBytes` check exists in exactly one place.
`internal/relay` already depended on `internal/sessions` transitively (via
`internal/control`), so importing it directly introduced no cycle. The
alternative — reimplementing the same bound and character set inside
`internal/relay` — was the one option the ticket explicitly ruled out: two
filters drift the moment either one's bound changes without the other
noticing, which is exactly the failure #2576's near-miss above (a 64-vs-32-byte
mismatch between two independent constants) shows actually happens. See
[`features/devices-registry-redemption-and-binding.md`](devices-registry-redemption-and-binding.md) § `SetClientVersion` for
the consuming write path.

## Carrying the conversation's handoff note (#2475)

The conversation's handoff note is resolved with its freshness by
`(*Pool).handoffNoteWithFreshness(label)` and rendered by
`handoffNoteSection(note)`. `handoffNoteFor` remains the text-only wrapper.
The note follows the client section and precedes the operator's bytes in the
[five-contributor order](#composition-and-resolution). The note was filed as
a *pointer* (one line naming the note's absolute path); #2474 measured that a
`Read` outside the workspace is
gated under the daemon's in-band `default` posture, so a background
conversation could never act on a bare path, and the design shipped as inline
composition instead.

The note is claude-authored and multi-line, so `admissibleClientField`'s
"one line, inside quotes, mid-line" construction — the thing that makes "no
line originates from a client" true by construction — is unavailable. A fence
carries the structure instead: `admissibleHandoffNote` admits a note only when
it contains no line beginning with `handoffNoteFence` (`"-----"`, trimmed of
leading whitespace *and* Unicode format characters via `invisibleRune`) and no
occurrence, anywhere, of either exact marker tag (`handoffNoteBeginTag` /
`handoffNoteEndTag`). Everything between `handoffNoteBegin` and `handoffNoteEnd`
is then the note *by position*, so a note line that reads like
`systemPromptText`, like `clientSectionLead`'s section, or like the operator's
own bytes is still attributed to the note. A refused note yields no section at
all — `maxNamedClients`' fail-closed direction, never a repaired one. The
bound is `MaxHandoffNoteBytes` (16 KiB), inherited from the store rather than
re-imposed here: `maxClientNameBytes`' ceiling is reasoned for a transcribed
self-report of marginal value, not for a note whose whole purpose is the
successor's context, so a second, smaller bound would silently undercut the
feature.

Since #2906, failed reset preserves older stored note bytes, but any usable
older note injected into a successor carries `staleHandoffWarning` before its
untrusted fence: the latest reset did not produce a fresh handoff, so the
note may omit recent work. This applies to immediate rotation, delayed
activation and daemon restart before activation, including failed replacement
of the note. The paired note/freshness read holds `Pool.handoffMu`; only a
matching regular hard-link certificate with no stale override or marker
establishes freshness. Missing, unsafe or unreadable metadata is stale.
Without an admitted note there is neither a handoff section nor a warning.
A subsequent successful wrap-up stores a fresh note and removes the warning
on recomposition; freshness is scoped to the conversation. See the
[store's write-side contract](sessions-package-key-types-handoffnote-store.md#the-wrap-ups-write-side-2477)
for invalidation, replacement and restart persistence.

**A refusal predicate has to be checked against what it actually admits, not
against its stated intent — and disagreement between two predicates in the
same function is the fastest tell.** The shipped predicate's first version
trimmed the fence-test line with `strings.TrimLeft(line, " \t")` (ASCII only)
while the blank test one line above used the Unicode-aware `strings.TrimSpace`.
A note line prefixed with a zero-width character (U+200B, the BOM, U+2060) or
non-ASCII whitespace (NBSP, EN QUAD, IDEOGRAPHIC SPACE) therefore passed the
fence test and reproduced the end marker byte-for-byte inside the fence,
closing the framing early — the exact property the ticket's
`security-sensitive` label existed to hold. Neither the plan's design section
nor its own security-review pass caught it; both asserted the line-anchored
refusal was "exactly that guarantee" without examining its extension. The fix
(`bc4bafc5`) is two independent refusals, neither subsuming the other: the
line-anchored trim now spans `unicode.IsSpace` ∪ `unicode.Cf` (neither
category covers the other — verify a Unicode-category claim by running it,
not by recalling it), and a second, position-blind refusal on the exact marker
tags closes a tag sitting mid-line, which no line-anchored test can see at
all. U+2028 and U+2029 are refused outright, because the line-anchored refusal
splits on `"\n"` and is only sound if `"\n"` is the note's one line break.
**When two predicates that should agree about "nothing here" or "no structural
prefix" use different character classes, that disagreement is where the
bypass lives — grep for it wherever this package validates untrusted bytes
against a fixed marker or delimiter.**

`(*Pool).handoffNoteFor` is total, `conversationPrompt`'s posture: an empty or
non-canonical label, persistence disabled, no note on disk, and any read
failure all yield `""`, and it logs nothing at any level — `HandoffNote`'s
wrapped `*fs.PathError` names the note path, so even swallowing it into a log
line would leak the path. It gates the read on `(*Pool).HandoffNotePath`
before opening the file, and that gate is doing two jobs, not one: it keeps a
symlink's target out of the composed prompt (`HandoffNotePath`'s original
reason), and — found only in security review, not stated in the ticket —
**it is what stops a FIFO at the note path from hanging a spawn**. `open(2)`
on a FIFO blocks until a writer appears, and on the rotation funnel the caller
is the relay's single Run dispatch goroutine, so an ungated read would not
fail a spawn, it would stall daemon-wide dispatch forever. `Lstat` answers
before any `open` happens, so a FIFO reports as non-regular and composes as
"no note" instead. The note is never retained on `Session` — re-derived at
every compose, because #2477's wrap-up turn writes the note *during* the reset
and before the rotation that follows it, so the rotation's recompose is the
first compose that can observe it; freezing it at an earlier compose would
ship the feature dead for the flow it exists for.

See [docs/specs/architecture/2475-handoff-note-in-system-prompt.md](../../specs/architecture/2475-handoff-note-in-system-prompt.md)
for the full design, the two-refusal security walk (including the
`## Revisions` entry recording the whitespace gap above), and the security
review.

### The fence was exported, not the predicate (#2477)

The conversation reset's wrap-up turn (#2477, `cmd/pyry`) carries the
*previous* handoff note into the prompt it sends the outgoing child, and that
prompt needs the same fence and the same refusals as this section — a forged
end marker is exactly as dangerous whether it is heading into a system prompt
or a user turn — but not `handoffNoteLead`, whose sentence ("consult it when
the user refers to earlier work") is written for a system prompt and is wrong
for a turn asking its reader to prune and rewrite the note. The seam is cut
below the lead: `FencedHandoffNote(note string) (string, bool)` renders the
fence and its refusals alone, and `handoffNoteSection` is now
`handoffNoteLead + fenced` over it, byte-identically. **One predicate and one
fence, two leads** — exporting a second predicate over the same untrusted
bytes was rejected because that is exactly how two admission rules drift
apart from each other; only the sentence that differs per destination lives
outside the shared package.

`cmd/pyry`'s wrap-up write side runs the *outgoing* child's reply through this
same `FencedHandoffNote` before ever calling `Pool.WriteHandoffNote` — not
only the note this section reads back. A reply carrying a forged fence marker
would otherwise be stored happily and refused later by this section's own
`handoffNoteSection`, silently destroying the previous note and handing the
successor nothing; running the read side's predicate on the write path closes
that gap rather than trusting the store's blank-only check. See
[`sessions-package-key-types-handoffnote-store.md`](sessions-package-key-types-handoffnote-store.md#the-wrap-ups-write-side-2477).

## The mint-window trap this design exists to avoid

Composing once inside `Pool.buildSessionAs` would satisfy every argv assertion
and still ship the feature dead, because of when operators actually set a prompt.
Since [#2085](https://github.com/pyrycode/pyrycode/issues/2085) split "session
minted" from "session started," the normal flow — create the conversation, set
its prompt, send the first message — sets the prompt *after* `buildSessionAs`
has already frozen `spawnBase` and *before* any child exists. Daemon-wide
instructions have the same window: reading them only at construction would
miss a `SetDaemonInstructions` edit made before the first activation.

`Pool.refreshSystemPrompt(ctx, sess)`, called from `Pool.Activate` — the
pool-owned funnel every first spawn and every re-activate passes through — closes
the window: `writeComposedPrompt` re-reads both the conversation registry and
the pool's current daemon instructions, then rewrites the file `spawnBase`
already names, immediately before the child comes up. Inactive reactivation
and revival use this same refresh. `refreshSystemPromptForRotation` also
re-reads the instructions for the next `new_session` child, while carrying
the admitted client identities rather than resolving clients on the relay's
dispatch goroutine. Both refresh functions skip the bootstrap. Because the
write is a rename onto a stable path, a backoff restart re-executing the
installed argv reads whatever the file then holds. An already-active session
is skipped (Juhana's
ruling in code: setting a prompt does not restart a running child), which also
keeps a disk write off `Activate`'s LRU-touch hot path. A refresh write failure
is logged with a fixed `persistence` classification and swallowed rather
than failing the spawn. A prior composition without a handoff retains its
complete file. A prior composition containing a handoff requires a different
fallback: its old bytes could present the note without the latest stale
warning. `writeComposedPrompt` tries `rewriteExistingSystemPrompt`, which
checks that the existing file is regular and unchanged across open, then
rewrites, truncates and syncs it without needing directory write permission.
This degraded fallback is not atomic. If it also fails, the pool suppresses
the daemon-owned `--append-system-prompt-file` pair before the successor
starts, logs fixed `fallback` / `suppressed` classifications and keeps it
suppressed through settings recompositions until a successful refresh
restores it. Inspect the file and installed argv the successor actually
consumes: a successful note invalidation alone cannot prove safe framing.

**Serialize argv composition through publication (#2906).** Both
`Pool.writeComposedPrompt` and `Pool.UpdateSettings` hold the per-session
`spawnArgsMu` through suppression/recovery and runner argv installation.
Both settings branches compose argv after installing the spawn posture and
hold the mutex through `SetSpawnArgs` or `Restart`, releasing it before
in-band delivery. Otherwise a settings update could capture argv before
suppression and publish it afterward, restoring the unsafe prompt path;
the inverse interleaving could undo recovery. Lock order is `spawnArgsMu`
then `Pool.mu`, with `Pool.mu` released before runner calls and no lifecycle
or pool-lock holder acquiring `spawnArgsMu`. Sequential settings assertions
missed this race: `TestPool_StaleHandoffConcurrentSettingsInstall` pauses
both settings branches at posture and argv publication and checks
suppression and recovery. `TestPool_StaleHandoffPromptRefreshRefusal`
checks the actual file fallback and installed argv.

The assertion that actually catches a regression here is "mint with no
prompt, `SetSystemPrompt` on the registry, then `Activate` — the file the argv
names holds the prompt" (`TestPool_Activate_ComposesPromptSetAfterMint`) — not
"the flag is present in the argv," which a construction-only composition would
also pass.

`TestDaemonInstructionsNextStarts` adds the shared-setting version of that
proof: edit after mint, activate, and inspect the file named by the recorded
argv for all five contributors in order. It also checks that an edit leaves
an active child's PID, prompt bytes and file timestamp unchanged, then checks
the latest instructions at inactive reactivation, rotation and a separately
revived conversation with its own operator text last. File-content assertions
at those lifecycle boundaries catch a missing re-read that argv-presence
assertions would leave green. `TestDaemonInstructionsRefreshFailureKeepsCompletePrompt`
holds the prior-complete-file fallback and instruction-free refresh log.

## `Pool.SystemPromptFor`

`SettingsFor`'s shape (see [that doc](sessions-package-key-types-pool-settingsfor.md)):
one `p.mu.RLock`, `ErrSessionNotFound` on a miss, otherwise the stored value —
here, the **operator** half only, not the composed whole, which is what lets
its wire-facing consumer (#2152's `request_system_prompt` handler, see
[Inbound request_system_prompt](v2-session-manager-state-machine-inbound-request-system-prompt-systempromptfor-seam.md))
compare against `Conversation.SystemPrompt` without stripping a constant it
does not own. That comparison has to run on this accessor's **collapsed**
return — it reports `""` for both of the registry's no-bytes states by
design — never on whether the registry's stored pointer is nil; the seam doc
has the trap. In-process only; no control-plane verb writes it.

## Known gap: `Pool.New`'s startup purge can leak the settings file (non-blocking, #2150 code review)

`Pool.New` purges the stale `session-prompts/` directory (a SIGKILL survivor
sweep — no defer survives a kill, and it is only sound before any session is
materialised) **between** `writeMCPSettings` and the `built`-flag cleanup
defer's installation. An error return from that purge does not remove
`settingsPath`, even though the comment installed three lines above it states
the invariant this breaks verbatim: every error return between the settings
write and the successful build has to remove it, or the orphan on a cold start
with a freshly-minted bootstrap id is permanent. `buildSession` gets this right
(`_ = os.Remove(settingsPath)` before its own defer). Cheapest fix, not yet
applied: move the purge above `writeMCPSettings`, which has no dependency on
it.

A second, narrower window from the same review: `Pool.Activate`'s refresh can
race `Pool.Remove` on the same session, and if the refresh's rename lands
after `Remove`'s `os.Remove`, it resurrects an orphan file under
`session-prompts/` holding operator text. Bounded — `Pool.Run`'s `RemoveAll`
and `Pool.New`'s purge both reap it, so "never outlives the daemon" still
holds — but it is the one new race the per-session lifecycle introduces, worth
a sentence for whoever next touches this file's teardown. #2436's rotation
recompose shares the identical window against the same `Pool.Remove` — the
capture of `*Session` and the write are equally split across `p.mu` releases —
and is bounded the same way; it is not a second race to reason about
separately, just the same one with a second caller.

## No log line ever carries prompt bytes

Only paths and wrapped `os` errors are interpolated into any error string or
construction/removal log line. Refresh failures use fixed persistence and
fallback classifications without raw errors or host paths (#2906). #2148
extends this rule to the three client-identity strings: `admitClient` is silent by
construction, so a refused hostile name has no log line to appear in at all,
by design rather than by omission. The one live test
gap this surfaced: an argv-level assertion (reading claude's spawn record out
of `internal/streamsup`'s log) cannot tell "bytes reached the file" from
"bytes reached the reply" — it would pass against a claude that ignored the
file entirely. The live gate for a per-conversation prompt needs a positive
arm (a distinctive marker in the reply) *and* a negative arm (same daemon,
same message, no stored prompt, marker absent); the negative arm is what makes
the positive one evidence rather than coincidence. #2093's original live test
only needed the positive half because its payload was a fixed constant with
nothing to distinguish "present" from "coincidentally identical."

See [docs/specs/architecture/2150-conversation-system-prompt-spawn.md](../../specs/architecture/2150-conversation-system-prompt-spawn.md)
and its predecessor [docs/specs/architecture/2093-remote-client-system-prompt.md](../../specs/architecture/2093-remote-client-system-prompt.md)
for the full designs.
