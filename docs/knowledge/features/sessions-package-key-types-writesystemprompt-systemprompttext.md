# `writeSystemPrompt` + `systemPromptText` (#2093, per-session since #2150, client-named since #2148)

```go
func composeSystemPrompt(operator string) string
func composeSystemPromptFor(operator string, clients []ClientIdentity) string
func writeSystemPrompt(registryPath string, id SessionID, text string) (string, error)
func (p *Pool) conversationPrompt(label string) string
func (p *Pool) attachedClients(ctx context.Context) []ClientIdentity
func (p *Pool) refreshSystemPrompt(sess *Session)
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

## Naming the attached client (#2148)

`composeSystemPromptFor(operator, clients)` names the clients attached *at
compose time* — spawn-time, not per-turn, and never restated mid-session,
because the appended prompt file is read once when the child spawns. It
**delegates to `composeSystemPrompt(operator)` whenever `clientSection(clients)`
returns `""`**, rather than branching on an equivalent condition, so the
no-clients / all-fields-empty path is byte-identical to the pre-#2148 text
*structurally* — `TestSystemPromptText_Pinned` and `TestComposeSystemPrompt`
needed no call-site edit and no re-transcription of the pin.

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
that claims completeness.

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
a sentence for whoever next touches this file's teardown.

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
