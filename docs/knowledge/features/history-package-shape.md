# `internal/history` — entry shape and metadata

Part of [the history package overview](history-package.md).

## Shape

Three production files (`log.go`, `segment.go`, `cursor.go`), a leaf importing
only `internal/conversations` (for `ValidID`/`ConversationID`) and stdlib —
never the wire-payload types, never decoding a payload, the same posture
[`eventring`](eventring-package.md) has. `New(instanceDir)` builds
`conversations/<id>/history/` beneath the given root exactly as
[`attachments.EnsureDir`](attachments-package-directory-resolution-and-creation.md)
does; the root is a constructor argument, never resolved inside the package.

```go
type SessionProvenance struct{ Kind string; SessionID string }
type Metadata struct{ Session *SessionProvenance; Shown *bool }
type Entry struct {
    ID uint64
    Type string
    Payload json.RawMessage
    TS time.Time
    Session *SessionProvenance
    Shown *bool
}
type Page struct{ Entries []Entry; Cursor string; AtStart bool }
func (s *Store) Append(convID conversations.ConversationID, typ string, payload json.RawMessage, ts time.Time) (uint64, error)
func (s *Store) AppendWithMetadata(convID conversations.ConversationID, typ string, payload json.RawMessage, ts time.Time, metadata Metadata) (uint64, error)
func (s *Store) Page(convID conversations.ConversationID, cursor string, limit int) (Page, error)
func (s *Store) LatestEntryID(convID conversations.ConversationID) (uint64, error)
func (s *Store) LatestDisplayableEntryID(convID conversations.ConversationID) (uint64, error)
func (s *Store) LogDir(convID conversations.ConversationID) (string, error)
```

`Store.LogDir` (#2673) returns the absolute, symlink-resolved, existing log
directory for a conversation. It validates the ID and uses
`resolveDir(convID, false)` under the store's mutex, rechecking exact containment
on every call; it creates nothing, caches no path and refuses a missing path or
a path that is not a directory. A sibling-conversation redirect is refused just
like a redirect outside the instance root (see [Directory resolution](history-package.md#directory-resolution-re-resolve-every-call-dont-cache-the-answer)).

Switch handover reads this daemon-owned log through `Store.Page` and uses
`LogDir` for the incoming agent's on-demand file pointer. Payload decoding and
completed-exchange selection belong to `cmd/pyry`, preserving this package's
opaque-payload boundary; see [agent-switch handover](conversation-session-binding.md#switching-to-the-other-agent-2672).
The `segment-<number>.jsonl` files are JSON Lines: a schema header
(`{"format":"pyrycode.history","version":1}`) followed by one JSON event per
line. An agent reading the log directory may need permission because the
daemon's instance directory is outside its workspace; handover adds no
permission round-trip.

`AppendWithMetadata` persists `Entry.Session` and `Entry.Shown` alongside `id`,
`type`, `payload` and `ts`, outside the opaque payload. `session` is an object
with `kind` and optional `session_id`; `shown` is a JSON boolean. Nil fields
are omitted, never written as null, and explicit `shown: false` is retained.
The header stays version 1: older entries decode with nil metadata without
rewriting their bytes or inventing sessions. `Store.Page` returns the metadata
across segments and after reopening. The unchanged `Append` signature delegates
to this same locked path with empty `Metadata`, writing absent metadata.

Session provenance has three meanings:

| Stored `session` | Meaning |
| --- | --- |
| `{"kind":"claude","session_id":"..."}` or `{"kind":"codex","session_id":"..."}` | Known producing child; its session ID must be nonempty. |
| `{"kind":"none"}` | Explicitly no producing child, such as a daemon-authored fact without a bound child; `SessionID` must be empty and is omitted. |
| Key absent (`Session == nil`) | Unknown provenance, including legacy entries; absence does not assert that no child produced the fact. |

Other kinds and inconsistent session IDs return ID zero and
`ErrInvalidMetadata` before directory creation, without disclosing metadata in
the error. Session IDs are opaque strings, never paths or authorization.
Visibility is independent of provenance: `Shown == nil` uses the legacy type
rule, while explicit true or false overrides it (see
[the unread watermark](history-package-watermarks.md#unread-state-uses-a-separate-lazily-recovered-watermark-2954)).
Callers must not mutate payload or metadata during an append; the store retains
neither caller pointers nor payload bytes after the call, only scalar cache
values. These storage facts support
[ADR 042's history-backed thread](../decisions/042-daemon-built-thread.md);
the existing producers now declare visibility and filter legacy delivery
(see [Producers](history-package-producers.md#producers-2114-2115) and [Reader](history-package.md#reader-2116)). Channel posts
capture session attribution at acceptance (#2984); interactive Claude/Codex
output captures its producing runner before fan-in (#2981). Session transitions
and operator messages still leave provenance absent.

`limit` is **clamped** to `MaxPageEntries`, never refused above it — a page is
"up to `limit` entries", so an over-large ask from #2116 pages rather than
fails and can never be broken by the ceiling. Entry ids are a
**per-conversation** counter recovered from the newest segment's last entry on
first touch, deliberately not `eventring`'s ring-wide shape: every cursor here
names its own conversation and is refused on any other, so there is no shared
scalar for one conversation's cursor to mute a different one's stream, and no
reason to pay for a global counter recovered by reading every conversation at
startup.

Both append paths allocate IDs starting at `1`, strictly increasing per
conversation and below `2^53`. `MaxEntryID = 2^53 - 1` is the final successful
allocation, keeping newly minted IDs exact for JSON clients using IEEE-754
numbers. Once the last durable ID reaches that bound, further appends return
ID zero and `ErrIDExhausted` before encoding, segment rolling or writing. They
leave segment files and raw/displayable watermarks unchanged, never wrapping
or reusing an ID. Reopening preserves refusal, including logs whose recovered
last ID is already at or above the bound; raw queries and pages still return
those stored IDs unchanged. Exhaustion affects only that conversation.

**PRECONDITION on the store methods:** `convID` must
be the conversation the authenticated session is already on, never one a
client asserted on the wire. `ValidID` is a *shape* predicate only — a
client-supplied id of canonical shape genuinely resolves inside the
conversation it names and defeats every check beneath it. This is
`ResolvePath`'s precondition verbatim, and it is `#2116`'s to satisfy by
reaching the conversation through the session, never off the wire.
