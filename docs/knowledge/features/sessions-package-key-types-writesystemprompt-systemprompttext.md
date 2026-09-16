# `writeSystemPrompt` + `systemPromptText` (#2093, per-session since #2150, client-named since #2148, handoff note since #2475, fence exported since #2477)

```go
func composeSystemPrompt(operator string) string
func composeSystemPromptFor(operator string, clients []ClientIdentity, note string) string
func writeSystemPrompt(registryPath string, id SessionID, text string) (string, error)
func (p *Pool) conversationPrompt(label string) string
func (p *Pool) attachedClients(ctx context.Context) []ClientIdentity
func (p *Pool) handoffNoteFor(label string) string
func (p *Pool) refreshSystemPrompt(sess *Session)
func (p *Pool) refreshSystemPromptForRotation(sess *Session)
func (p *Pool) writeComposedPrompt(sess *Session, clients []ClientIdentity)
func (p *Pool) SystemPromptFor(id SessionID) (string, error)
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
`operator == ""`, otherwise the constant plus a blank-line separator plus the
operator's bytes verbatim (untrimmed, unescaped, unbounded here — #2149's
`Registry.SetSystemPrompt` is the single validating door). There is no branch
that returns the operator's text alone: replacing claude's own system prompt
is what `--append-system-prompt-file` exists not to do.

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
`handoffNoteFor` (#2475), compose through `composeSystemPromptFor`, write
`sess.systemPromptPath` verbatim, and record what the session was composed
with. Because both resolves and the write live in this one shared step, this
is also the single lookup site for the note across all three spawn paths that
carry one — first spawn, revive, and the rotation recompose — rather than
three call sites that could drift. What differs between the two funnels is
only the guards each puts in front of that call —
`refreshSystemPrompt` skips an already-`stateActive` session and resolves
clients through `attachedClients`; `refreshSystemPromptForRotation` runs
unconditionally, since nothing a rotation does leaves `stateActive`, and
resolves no client identity at all (see below).

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

`admitClient` is the one untrusted→trusted door both fields cross through
before either can reach the file: valid UTF-8, non-blank after trimming,
within its byte bound, and every rune display-safe (no C0, no C1/DEL, no
`"`, since the admitted value is rendered inside quotes and the delimiter
itself must be refused for no value to close the structure around it). An
inadmissible name drops the whole client; an inadmissible version drops only
the version. **Refusal, not truncation or escaping** — `MaxWorkspaceLabelBytes`'
posture: the byte bound alone is a cost control, never a safety claim, and the
character-set refusal is what actually holds the prompt's structure.
`clientSection` then sorts and dedupes the admitted set and renders it as one
daemon-authored, quoted transcription; more than `maxNamedClients` admitted
collapses to no section at all rather than a truncated list under a sentence
that claims completeness. The admit-sort-dedup-cap prologue is its own
function, `admittedClients(clients) []ClientIdentity`, and `clientSection`
renders whatever it returns; the split exists so #2436 can *retain* an
admitted set on `Session.promptClients` rather than only ever render one
inline. `admittedClients` is idempotent over its own output — every predicate
already holds and the set is already sorted and deduplicated — so re-admitting
a carried set reproduces the section byte-for-byte.

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
carrying a prior resolve forward keeps the sentence true rather than making it
stale. `promptClients` is written and read under `Pool.mu`, `systemPrompt`'s
discipline exactly and deliberately not `lcMu`, and it stores only
`admittedClients`' output — never a resolver's raw answer — so retention never
becomes a second place for unadmitted remote-authored bytes to live, and is
bounded at `maxNamedClients × (maxClientNameBytes + maxClientVersionBytes)`
per session rather than by however many conns one client holds.

**A resource bound and a display-validation door are different concerns and
belong in different packages.** `internal/relay` retains `DeviceName` /
`ClientVersion` verbatim off the wire but drops (never truncates) a value over
`maxRetainedClientNameBytes`/`maxRetainedClientVersionBytes` — a memory/copy-cost
ceiling at the point an authenticated client can park bytes that get copied into
every `ActiveConn` snapshot the fan-out takes per turn. `admitClient` here owns
the character set and the *display* bound instead. The instinct to fold both
into "the one validating door" would have put a memory-safety concern behind a
door that only runs when a session is about to spawn — the wrong place to stop
an allocation multiplier. See
[the relay enumeration doc](v2-session-manager-state-machine-concurrency-safe-open-session-enumeratio.md)
for the retention side.

See [docs/specs/architecture/2148-client-identity-system-prompt.md](../../specs/architecture/2148-client-identity-system-prompt.md)
for the full design, the trust-boundary walk, and the security review.

## Carrying the conversation's handoff note (#2475)

`composeSystemPromptFor` gained a third contributor: the conversation's
handoff note, resolved by `(*Pool).handoffNoteFor(label)` and rendered by
`handoffNoteSection(note)`. Composed order is **constant, then clients, then
the note, then the operator's bytes** — the operator's text stays last, where
it has always been. The note was filed as a *pointer* (one line naming the
note's absolute path); #2474 measured that a `Read` outside the workspace is
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

Composing once inside `buildSession` would satisfy every argv assertion and
still ship the feature dead, because of when operators actually set a prompt.
Since [#2085](https://github.com/pyrycode/pyrycode/issues/2085) split "session
minted" from "session started," the normal flow — create the conversation, set
its prompt, send the first message — sets the prompt *after* `buildSession`
has already frozen `spawnBase` and *before* any child exists. That is exactly
the shape `Conversation.Cwd` is stuck in today: its doc comment claims a
change "takes effect on the conversation's next fresh session spawn," and no
production path reads the stored value to make that true.

`Pool.refreshSystemPrompt(sess)`, called from `Pool.Activate` — the pool-owned
funnel every first spawn and every re-activate passes through — is what closes
the window: it re-reads the registry and rewrites the file `spawnBase` already
names, immediately before the child comes up. Because the write is a rename
onto a stable path, a backoff restart re-executing the installed argv reads
whatever the file then holds. An already-active session is skipped (Juhana's
ruling in code: setting a prompt does not restart a running child), which also
keeps a disk write off `Activate`'s LRU-touch hot path. A refresh write failure
is logged and swallowed rather than failing the spawn — the file was already
written at `buildSession` and the write is atomic, so the fallback is one
revision of stale-but-complete bytes, never a missing or truncated file.

The assertion that actually catches a regression here is "mint with no
prompt, `SetSystemPrompt` on the registry, then `Activate` — the file the argv
names holds the prompt" (`TestPool_Activate_ComposesPromptSetAfterMint`) — not
"the flag is present in the argv," which a construction-only composition would
also pass.

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
log line, on every path — construction, refresh, removal. #2148 extends this
rule to the two client-identity strings: `admitClient` is silent by
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
