# History request/reply payloads (#2113)

Wire vocabulary only, in a new `internal/protocol/history.go` — the request a
client sends to walk backwards through a conversation's stored history, and the
daemon's paged answer. No handler, no producer, no validator ship with it;
`#2116` wires all three. Published contract:
[`docs/protocol-mobile.md` § Conversation history](../../protocol-mobile.md#conversation-history-v2).
Mirrors [`internal/history`](history-package.md)'s landed shapes by field, not by
import — this package never imports `internal/history`.

```go
type RequestHistoryPayload struct {
    ConversationID string `json:"conversation_id"`
    Cursor         string `json:"cursor"`
    Limit          int    `json:"limit"`
}

type HistoryEntry struct {
    ID      uint64          `json:"id"`
    Type    string          `json:"type"`
    Payload json.RawMessage `json:"payload"`
    TS      time.Time       `json:"ts"`
}

type HistoryPagePayload struct {
    Entries []HistoryEntry `json:"entries"`
    Cursor  string         `json:"cursor"`
    AtStart bool           `json:"at_start"`
}
```

`TypeRequestHistory = "request_history"` / `TypeHistoryPage = "history_page"`,
filed in `v2OnlyTypes`, the partition test's `all` slice and the
`IsKnownAppType` rejects table (three edit points, not one — amending only the
map fails the union-size assertion); `excludedTypes["TypeRequestHistory"] =
"pending handler (#2116)"`, `excludedTypes["TypeHistoryPage"] = "reply"`;
**neither joins `inboundAppTypeSet`**, on `TypeRequestAttachment`'s precedent.
See [Drift detectors](protocol-package-drift-detectors.md) for what actually
reddens the build on an unpartitioned constant.

## A borrowed trust sentence would have licensed unsanitised claude output

The plan's first draft claimed *"every field of a page is daemon-authored"*,
copied from `RequestAttachmentPayload`'s posture. That sentence is true for
`request_attachment` only because that frame carries no content at all — it is
false here, and security review FAILed on it: `HistoryEntry.Payload` (and
`.Type`) are **replayed content**, the verbatim bytes of a frame that was
appended to the log, which for a stored `send_message` is operator-authored
and for a stored assistant frame is **claude-authored**. Shipping the borrowed
sentence would have told a client it never needs to sanitise replayed turns.
The landed rule instead scopes the daemon-authored claim to the page's own
frame (`cursor`, `at_start`, each entry's `id`/`ts`) and states explicitly that
**each entry carries exactly the trust class of the live frame it mirrors** —
a client applies the same sanitisation on a replayed entry that it already
applies on the live stream. Generalizes past this ticket: a trust-posture
sentence copied from an analogue is only as true as the analogue's own reason
for holding it, and a payload carrying nested opaque content (anything shaped
like `json.RawMessage` replaying an earlier frame) is exactly the shape where
that reason usually doesn't transfer.

## `Limit`'s zero is meaningful, not absent

`history.Store.Page` refuses `limit < 1` and clamps above `MaxPageEntries`
(4096) rather than refusing it. A JSON key can't be made mandatory, so the
only encoding with no trap is one where the zero value is itself a valid
answer: `Limit` 0 — whether sent explicitly or omitted — means "the daemon
chooses," a positive value is honoured up to the clamp, and only a negative
value is a reject condition (`#2116`'s code to mint). No `omitempty` on any of
the three request fields, so an encoder always emits the key. `Limit` is also
the one untrusted count on this wire vocabulary; `AttachmentChunkPayload`'s
NEVER ALLOCATE FROM A CLAIM rule applies to it verbatim — a receiver hands it
to `Store.Page` and lets the clamp decide, never sizes a buffer from it.

## The clamp bounds entries, not bytes — so a short page isn't "end of log"

`MaxPageEntries` caps the entry *count*; nothing bounds one entry's stored
payload, so a faithful 4096-entry page can exceed the v2 application-envelope
size cap and be undeliverable as the daemon's own outbound frame. The daemon
may therefore hand back **fewer entries than asked for**. This is exactly why
termination has to be the `AtStart` marker and never an entry count: a client
that infers "short page ⇒ start of log" is wrong the moment byte budgeting
trims a page for size reasons unrelated to the log's actual start. `AtStart`
and `Cursor` are never both meaningful — `Cursor` is empty whenever `AtStart`
is set. `#2116` built the budgeting by re-asking the log at a smaller limit,
never by truncating a returned slice — see
[Inbound `request_history` § A page's cursor names a position](v2-session-manager-state-machine-inbound-request-history-historypager-seam.md#a-pages-cursor-names-a-position-so-shortening-by-truncation-silently-skips-entries).

**Three page shapes ship as separate fixtures because two would have implied
a false equivalence.** The first AC draft only distinguished empty-terminal
from non-terminal-with-cursor, which reads as "empty ⇔ at start" — false
against `history.Page`'s own doc comment: a page that fills *exactly* at the
log's first entry reports `AtStart` false with a usable cursor, so a
**terminal page can still carry entries**. `history_page_at_start_entries.json`
exists to pin that third shape independently rather than leave it to
inference.

## The reply doesn't echo `conversation_id` — cost stated, not hidden

Correlation rides `Envelope.InReplyTo`, the same shape
`SessionSettingsPayload` already takes for a reply to a conversation-naming
request. The consequence is published rather than left implicit: a client
keeps its outstanding asks keyed by envelope id, since nothing in a page names
which conversation it answers for.

## The empty-array pin needs two tests, and only a mutant run showed which one does the work

`HistoryPagePayload.MarshalJSON` normalises a nil `Entries` to `[]` — the
`QuestionShownPayload` nil→`[]` pattern (value receiver, type alias to stop the
recursion) — because `history.Page{AtStart: true}` for a never-written
conversation is a real, reachable zero value, not a constructed curiosity.
Under a `go test -overlay` mutant stripping that normalisation branch, **the
fixture round trip for the empty terminal page stayed green**: decoding the
committed `"entries":[]` literal already produces a non-nil empty slice, so
the nil branch is never on the read path being exercised. Only the separate
assertion that marshals a `HistoryPagePayload` zero value and checks the raw
bytes under the outer `entries` key reddened. The two tests are sole-red for
different mutants and neither can stand in for the other — the general form of
this (an empty-array fixture literal proves decode, never encode) is the same
hazard [Drift detectors § "classic per-type env-round-trip test"](protocol-package-drift-detectors.md)
already names for struct tags; this is its nil-normalisation analogue,
confirmed by running the mutant rather than assumed.

## A documented anchor lesson recurred on the very next section that needed it

`docs/protocol-mobile.md` § Page size links to
`#application-envelope-size-cap`, a bold paragraph lead inside § Wire shapes
rather than its own heading — a dead anchor, `make cite-guard` doesn't scan
prose links, and the document's two *other* references to that same cap
already use `#wire-shapes` instead. This is not a new failure mode: the
attachments family's own doc
([§ two lessons from #1751](protocol-package-constants-codes-go-envelope-types-attachments.md))
recorded the identical mistake — a bold lead is not a heading, a markdown link
to one renders as a dead anchor nothing in CI catches — against the same
target paragraph. Writing the lesson down did not stop the next ticket that
linked to that exact paragraph from repeating it. Left as a verifier
SHOULD FIX (non-blocking); a written lesson without a lint rule behind it is
advisory only, and this is the second time this specific link has needed one.
