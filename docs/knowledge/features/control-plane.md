# Control Plane

`internal/control` exposes the on-disk control surface of `pyry`: a Unix domain socket (`~/.pyry/<name>.sock`, mode `0600`) speaking line-delimited JSON. Each connection is one request, one response — every verb `Server.handle` dispatches replies with one JSON `Response` and returns; no verb hands off connection ownership. (`VerbAttach` was the one verb that did, until #1348 deleted its server-side handler and #1535 deleted the now-orphaned wire type itself.)

Verbs today: `status`, `stop`, `logs`, `sessions.new`, `sessions.rm`, `sessions.rename`, `sessions.list`, `sessions.has-id`, `rekey`, `mcp.approve`, `attachment.file` (#2164, ships live but inert — see [§ Attachment.file](control-plane-attachment-file-confine-and-store-a-claude-named-path.md)), `channel.new` (#2155, see [§ Channel: new verb](#channel-new-verb-channelnew-2155) below), `conversation.new` (see [Conversation: create](#conversation-create-conversationnew)), `conversation.post` (see [user-message submission](#conversation-post-a-user-message-by-id-conversationpost)), `channel.post`, `pairing.mint` (#2388), and payload-free `update.when-idle` (`VerbUpdateWhenIdle`, #2757; see [Server Construction](#server-construction) for its decision contract). `attach` and `resize` are gone — #1535 deleted `VerbAttach`, `VerbResize`, `AttachPayload`, `ResizePayload`, and the `Request.Attach`/`Request.Resize` fields, plus the orphaned `control.SendResize` client helper, none of which had a live dispatch arm since #1348. The deletion is decode-compatible with a stale (pre-#1348) client: nothing in the repo calls `json.Decoder.DisallowUnknownFields`, and Go's `encoding/json` ignores unknown object fields by default, so a client still sending `{"verb":"attach","attach":{…}}` decodes cleanly and gets the same `unknown verb: "attach"` reply it already got post-#1348 — deleting a `Request` field only ever *widens* what the decoder accepts, never narrows it. The wire shape otherwise is held stable across phases — `VerbSessionsNew` (#75) adds a `Request.Sessions *SessionsPayload` field with `omitempty` so existing-verb wire output stays byte-identical (pinned by `TestProtocol_SessionsRoundTripBackCompat`). `VerbSessionsRm` (#98) extends `SessionsPayload` with `ID`/`JSONLPolicy` (both `omitempty`) and adds a `Response.ErrorCode` field (also `omitempty`) for typed-sentinel propagation — same back-compat guard, byte-identical existing-verb output. `VerbSessionsRename` (#90) extends `SessionsPayload` with one further `omitempty` field (`NewLabel`) and reuses the `Response.ErrorCode` envelope verbatim — no new wire constants. `VerbPairingMint` follows the same additive rule: the optional outer `Request.Pairing` and `Response.Pairing` fields preserve every older encoding, while the inner `PairingPayload` deliberately always emits both `DeviceLabel` and `AllowRemotePermissions`. Those are the only caller-selected mint inputs; `PairingResult.Pairing` is an opaque plaintext bearer credential, not a place to expose identity, key, relay, registry, expiry, or diagnostics. `VerbUpdateWhenIdle` adds no request field, and `Response.UpdateWhenIdle` is optional with `omitempty`, preserving older verb encodings too.

## Server Construction

See [server construction and provider wiring](control-plane-server-and-deadlines.md#server-construction).

### Update-when-idle provider

See [the update-when-idle provider](control-plane-server-and-deadlines.md#update-when-idle-provider).

## Handshake Deadline: per-conn timeout and the session-verb extend (#865)

See [handshake and operation deadlines](control-plane-server-and-deadlines.md#handshake-deadline-per-conn-timeout-and-the-session-verb-extend-865).

## Resolver Seam

The control plane consumes session state through one interface pair, both defined in `internal/control` (the consumer side):

```go
// Session is the per-session view the control server depends on.
type Session interface {
    State() sessions.State
    Activate(ctx context.Context) error
}

// SessionResolver maps a SessionID to a Session. An empty id resolves to
// the default (bootstrap) entry.
type SessionResolver interface {
    Lookup(id sessions.SessionID) (Session, error)
    // ResolveID maps a loose-input session selector (full UUID, unique
    // prefix, or empty for bootstrap) to a concrete SessionID. Errors are
    // returned verbatim.
    ResolveID(arg string) (sessions.SessionID, error)
}
```

`*sessions.Session` satisfies `Session` structurally. `*sessions.Pool` does **not** satisfy `SessionResolver` directly because `Pool.Lookup` returns the concrete `*sessions.Session` rather than the `control.Session` interface — Go does not do covariant return types on interface satisfaction. A small `poolResolver` adapter in `cmd/pyry/main.go` bridges the two; both `Lookup` and `ResolveID` are 1-line passthroughs.

The empty-id-resolves-to-default convention holds for every caller today: `Server.handle` calls `Lookup("")` for `status`, and `handleSessionsHasID` calls `Lookup` with the payload id. No handler in `internal/control` calls `ResolveID` or `Activate` since #1348 removed attach — both methods stay on the interface for the seam design below and for the day a verb needs loose-input session selection again.

### Why a single resolver instead of `StateProvider` + `AttachProvider`

Phase 0 wired the supervisor into control via two narrow interfaces (`StateProvider` for `VerbStatus`, `AttachProvider` for `VerbAttach`). Two providers worked when there was exactly one supervisor; once a session has identity, every verb needs the same lookup step before it does its work. Collapsing to one resolver removes the dual-provider plumbing and gives each handler the same shape:

```go
sess, err := s.sessions.Lookup("")
if err != nil { /* encode error */; return }
// use sess.State()
```

`VerbLogs` and `VerbStop` are intentionally process-global today (logs come from the ring buffer; stop calls the supervisor-context cancel). They do **not** call the resolver. Phase 1.1 may revisit `VerbStop` if per-session stop becomes a verb.

## Sessions: removal seam (1.1d-B1)

The second `sessions.*` verb is `sessions.rm`. The control server consumes session-removal commands through the `Remover` interface in `internal/control`, embedded into `Sessioner` so `NewServer`'s signature stays stable as the namespace grows:

```go
type Remover interface {
    Remove(ctx context.Context, id sessions.SessionID, opts sessions.RemoveOptions) error
}

type Sessioner interface {
    Create(ctx context.Context, label string) (sessions.SessionID, error)
    Remover
}
```

`*sessions.Pool` satisfies `Remover` directly via `Pool.Remove` (#94/#95). Aggregating per-verb sub-interfaces into `Sessioner` (rather than threading a new constructor parameter per seam) keeps the 27-call-site `NewServer` signature unchanged — a deliberate response to the #75 cascade. Phase 1.1b/c (`list`, `rename`) will continue this pattern.

### Wire shape

`SessionsPayload` carries the union of all `sessions.*` verb arguments, with `omitempty` per field:

```go
type SessionsPayload struct {
    Label       string      `json:"label,omitempty"`       // sessions.new
    ID          string      `json:"id,omitempty"`          // sessions.rm
    JSONLPolicy JSONLPolicy `json:"jsonlPolicy,omitempty"` // sessions.rm
}

type JSONLPolicy string  // "leave" | "archive" | "purge" (empty = leave)
```

`sessions.rm` uses the `OK`/`Error` envelope — no typed result.

### Typed errors via Response.ErrorCode

`Pool.Remove` returns two sentinels the CLI sibling needs to match on. `Response.ErrorCode` carries a stable wire token decoupled from the message string:

```
Response.ErrorCode == "session_not_found"        → sessions.ErrSessionNotFound
Response.ErrorCode == "cannot_remove_bootstrap"  → sessions.ErrCannotRemoveBootstrap
```

The server detects the sentinel with `errors.Is` (so future server-side wrapping won't break the wire token), encodes both `Error` and `ErrorCode`, and the client's `SessionsRm` returns the bare sentinel directly so callers can `errors.Is` against it. Untyped errors flow through `Response.Error` verbatim with no `ErrorCode`.

The string-token rather than message-string approach means renaming a sentinel's message is no longer a wire-protocol break — only the `ErrorCode` enum is.

### JSONL policy translation

The wire enum (`JSONLPolicy` string) and the internal enum (`sessions.JSONLPolicy` uint8) are deliberately distinct. `protocol.go` stays import-free; the translation (`toSessionsPolicy` in `server.go`) lives next to the handler. Strings are jq-debuggable on the wire and durable across protocol versions if the underlying enum order ever changes. Empty wire value maps to `JSONLLeave` (matches the internal zero value); unknown values surface as `"sessions.rm: unknown jsonl policy %q"` in `Response.Error` rather than silent fallback.

See `docs/specs/architecture/98-control-sessions-rm.md` for the full design.

## Sessions: rename seam (1.1c-B1)

The third `sessions.*` verb is `sessions.rename`. The control server consumes session-rename commands through the `Renamer` interface in `internal/control`, embedded into `Sessioner` alongside `Remover`:

```go
type Renamer interface {
    Rename(id sessions.SessionID, newLabel string) error
}

type Sessioner interface {
    Create(ctx context.Context, label string) (sessions.SessionID, error)
    Remover
    Renamer
}
```

`*sessions.Pool` satisfies `Renamer` directly via `Pool.Rename` (#62). `Renamer` does not take a context — `Pool.Rename`'s signature is `(id, newLabel) error` and the operation is bounded by a single `Pool.mu` critical section + `saveLocked`, so the seam mirrors that shape adapter-free. Adding `Renamer` to `Sessioner` keeps `NewServer`'s signature stable; the rationale documented under "Sessions: removal seam" applies identically.

### Wire shape

`SessionsPayload` gains one omitempty field used by `sessions.rename`:

```go
type SessionsPayload struct {
    Label       string      `json:"label,omitempty"`       // sessions.new
    ID          string      `json:"id,omitempty"`          // sessions.rm, sessions.rename
    JSONLPolicy JSONLPolicy `json:"jsonlPolicy,omitempty"` // sessions.rm
    NewLabel    string      `json:"newLabel,omitempty"`    // sessions.rename
}
```

`sessions.rename` uses the `OK`/`Error` envelope — no typed result. Empty `NewLabel` on the wire is forwarded to `Pool.Rename` as the empty string, which clears the on-disk label per #62's contract.

### Typed errors via Response.ErrorCode

One sentinel propagates from `Pool.Rename`:

```
Response.ErrorCode == "session_not_found"  → sessions.ErrSessionNotFound
```

The client maps this to the corresponding sentinel error so callers can match with `errors.Is`. Untyped errors (e.g. registry persist failures) flow through `Response.Error` verbatim with no `ErrorCode`.

The `ErrorCode` envelope and the `ErrCodeSessionNotFound` token are reused verbatim from #98 — no new wire constants. This is the intended dividend of #98's wire-error infrastructure landing first: subsequent verbs reuse the envelope at zero incremental wire cost.

See `docs/specs/architecture/90-control-sessions-rename.md` for the full design.

## Channel: new verb (channel.new, #2155)

`channel.new` creates a **promoted** conversation (a "channel", `conversations-package.md`'s term for `IsPromoted=true`) whose `Cwd` is the operator's current directory, so a project folder that has never hosted a client-created conversation is reachable without a client picker. First conversation-registry verb in `internal/control` — every prior verb addressed `internal/sessions` only.

Two choices worth keeping in mind for the next verb built this way:

- **The creator seam is a narrow mint-only closure, not `*sessions.Pool` itself.** `Server.channelCreator func(cwd, name string) (string, error)`, installed via `SetChannelCreator` alongside `SetFileAttacher` — a plain func behind a late-bound setter, not a `NewServer` parameter (the same "every dependency lives at the `cmd/pyry` composition root" argument as `SetFileAttacher`). The `cmd/pyry`-side implementation itself narrows further: it closes over a bare `mint func(label, spawnDir string) (string, error)` rather than the whole `*sessions.Pool`, mirroring the narrowing `sessionMinter.Create` already does for the wire `create_conversation` path (see [conversation-session-binding-create.md § The `SessionCreator` seam](conversation-session-binding-create.md#the-sessioncreator-seam-keeps-handlers-import-clean)). Taking the concrete `*Pool` would have forced every creator unit test to stand one up.
- **Resolve and trust-mark the workspace once, then reuse that answer.** The legacy `channelCreator` calls `resolveSpawnDir` directly before its bare mint closure. A caller accepting model/effort must instead use `sessionMinter.Create`, which checks membership before resolving and calls `Pool.MintWith` with one defaults snapshot. `handlers.SessionCreator` already returns the resolved cwd: `conversationCreator` uses it for both the stored workspace and the default channel name. Resolving again would repeat trust marking; bypassing the minter would lose the shared settings validation.

**`resolveSpawnDir`'s empty-string arm is fail-open by contract, and every non-wire caller must re-guard it.** Per [conversation-session-binding-create.md](conversation-session-binding-create.md#the-sessioncreator-seam-keeps-handlers-import-clean), `resolveSpawnDir("")` returns `("", nil)` — success, meaning "the daemon's shared trusted workdir," with **no** confinement and **no** trust-mark. That is correct for the phone's optional-`Cwd` path but wrong for a verb where an empty cwd can only mean a bug or a stray caller: unguarded, it would silently write a row with `Cwd: ""` and a name of `"."`. `channelCreator` therefore rejects an empty cwd itself, *in addition to* `handleChannelNew`'s wire-shape check — the second guard is not redundant, it protects the seam from any future caller that doesn't come through the handler (`handleAttachFile`'s empty-`SessionID` double guard is the precedent). Confirmed by mutation: deleting the creator's own guard makes the empty-cwd test pass a `cwd` straight through instead of failing loud.

**Refusals need three static buckets, not the two the design sketched, because "rejected" and "failed" are different verdicts.** A `$HOME`-escaping directory is a deterministic, permanent refusal; a `trustMark` write failure is a transient one about `~/.claude.json`. Both must stay off the wire verbatim (`confineWorkdirToHomeCreating`'s error text embeds the resolved path *and* `$HOME`), but collapsing them into one static message tells the operator their project folder isn't allowed when the real fault is a local write error — sending them looking in the wrong place. Split with `errors.Is(err, handlers.ErrSpawnDirRejected)`, not by matching message text, so the split survives future wrapping.

**A message-prefix discriminator between "server rejected" and "transport failed" isn't always worth building.** `rekeyVerdict`'s `isServerReject` (a hand-maintained message-prefix list) exists because `pyry rekey`'s acceptance criteria wanted a different stderr prefix per class. `channel.new`'s AC only asks for one stderr line and exit 1 on any failure, so `channelNewVerdict` skips the discrimination entirely rather than adding a second hand-maintained list that would go stale the first time a server message got reworded. Check what the AC actually distinguishes before copying a sibling's verdict-formatter shape wholesale.

See `docs/specs/architecture/2155-channel-new-control-verb.md` for the full design and security review.

## Conversation: create (conversation.new)

`VerbConversationNew` is the control API behind `pyry conversation new`.
`runSupervisor` installs `conversationCreator` through
`Server.SetConversationCreator` beside the legacy channel creator, over the same
conversation registry, persistence path and pool, with
`sessionMinter{pool, modelVocabulary}`. Installation is independent of relay
configuration: creation works without a relay URL or paired client.
`channel.new` and name-based channel-post creation keep their legacy creator.
The request uses `Request.Conversation` (`ConversationPayload`):

```json
{"verb":"conversation.new","conversation":{"cwd":"/workspace","type":"chat","model":"","effort":""}}
```

| Field | Contract |
| --- | --- |
| `cwd` | Required nonempty string; forwarded unchanged. |
| `name` | Optional string; omission forwards an empty name. |
| `type` | Optional `chat` or `channel`; absent/null defaults to `chat`; empty or other strings are invalid. |
| `model`, `effort` | Optional string pointers: absent/null stays unset; explicit empty stays present, matching `protocol.CreateConversationPayload`. |

`ConversationPayload.Type` is a pointer because a scalar would silently default
an explicit empty type to chat. `TestConversationNew_Refusals` pins its rejection.
`Type`, `Model` and `Effort` use `omitempty`: nil pointers remarshal as absent
keys; empty settings remain present. See the
[conversation write payloads](protocol-package-types-conversations-write-payloads.md).

`Server.SetConversationCreator` installs an independent
`func(cwd, name, conversationType string, model, effort *string) (string, error)`;
nil clears it. `handleConversationNew` snapshots it under `Server.mu`, then
unlocks. Without a creator it returns
`conversation.new: no conversation creator configured` before payload checks.
With one installed, absent/null payload or missing/empty cwd returns
`conversation.new: missing cwd`; invalid type returns
`conversation.new: invalid type`, without invoking the creator.

Accepted input invokes the creator once with the effective type and otherwise
unchanged values. Control performs no path handling. The creator owns workspace
confinement, symlink resolution before trust-marking, name defaults and
model/effort shape and membership validation; control defines no settings
vocabulary. It must bound its own work:
the extended `sessionOpTimeout + sessionOpConnGrace` deadline bounds response
I/O, not synchronous creation.

Production `conversationCreator` checks settings shape with `relay.ValidModel`
and `relay.ValidEffort`, then delegates Claude membership checks and minting to
`sessionMinter.Create`. Nil settings inherit `Pool.MintDefaults`; an explicit
empty string resets only that field to Claude's own default. Effort is checked
against the effective model from the same defaults snapshot used for minting.
Nonempty requested models use the retained published vocabulary; an unavailable
menu is distinguished from a proven absent model. Settings refusals precede
directory creation, trust marking and minting. See
[Requested model and effort](conversation-session-binding-create.md#requested-model-and-effort-2665)
for the shared validation contract and effort fallback rules.

The minter's resolved, confined cwd is stored directly, without a second
resolution or trust mark. Chats are unpromoted with a null name when the name
is omitted or empty; channels are promoted and default to the resolved folder's
base name. A nonempty name overrides either default. The row binds the minted
session immediately, with accepted settings stored before the first message;
creation does not activate Claude. The registry is saved eagerly, best-effort:
a save failure is logged and creation still succeeds. When the existing relay
announcement hook is available, it announces a registry read-back including
the workspace label; a nil hook leaves local creation available.

Success returns `{"conversationNew":{"conversationID":"created-id"}}`.
A creator error discards any accompanying id; an empty id returns
`conversation.new: empty conversation id`. Both omit the success payload.
Creator refusal text reaches the wire with a `conversation.new:` prefix;
`Server.SetConversationCreator` requires static, input-free errors, like
`Server.SetChannelCreator`.
`ConversationNew(ctx, socketPath, payload)` returns an empty id and error on
transport failures, wire refusals even alongside success, or missing/empty
success ids.

`runConversation` reads the shell's cwd and calls `ConversationNew` with a
30-second context. Success prints only the id and newline; syntax errors exit
2, and settings, cwd, workspace or transport errors exit 1 with stderr and empty
stdout. See [CLI parsing](cli-verb-dispatch.md#conversation-option-and-selector-parsing),
the [control contract spec](../../specs/architecture/2883-conversation-new-contract.md)
and [CLI spec](../../specs/architecture/2884-conversation-cli.md).

## Conversation: post a user message by id (conversation.post)

`VerbConversationPost` submits a user message to an existing conversation through
the installed callback. Production submission and CLI wiring remain pending
[#2886](https://github.com/pyrycode/pyrycode/issues/2886).
The request uses `Request.ConversationPost` (`ConversationPostPayload`):

```json
{"verb":"conversation.post","conversationPost":{"conversationID":"existing-id","text":"user message"}}
```

Both `conversationID` and `text` are required nonempty strings. Control forwards
both unchanged, including whitespace-only values, exactly once on valid input.
It performs no trimming, id resolution, registry lookup or creation on a miss.
`control.MaxChannelPostBytes` bounds decoded text at 64 KiB (65,536 UTF-8 bytes),
inclusive. Count bytes after JSON decoding, not JSON escape spelling or Unicode
characters: a character-count check would accept oversized Unicode messages.

`Server.SetConversationSubmitter(func(conversationID, text string) error)`
installs an independent callback; nil clears it. After JSON decoding,
`handleConversationPost` rejects an absent submitter before payload validation
with `conversation.post: no conversation submitter configured`. With one
installed, absent/null payload or missing/empty/null id returns
`conversation.post: missing conversation id`; missing/empty/null text returns
`conversation.post: empty message`; text above the cap returns
`conversation.post: message too large`. None invokes the callback. Wrong JSON
types fail in the shared decoder before dispatch.

The callback owns existing-id resolution and queue admission and must not create
on a miss. It must bound its own work: the 35-second response I/O deadline cannot
cancel execution. Invocation runs outside `Server.mu`.

Success returns only `{"ok":true}`: queue acceptance, not completed model
output. Callback refusals, including unknown-id and full-backlog refusals,
return only an error with the `conversation.post:` prefix, for example
`{"error":"conversation.post: unknown conversation"}`. OK is false (omitted
on the wire), and there is no result body. Control does not log id/text or echo
them in validation diagnostics. Like `Server.SetChannelPoster`, the submitter
must return static errors or static formats over daemon-derived values; callback
refusal text reaches the wire verbatim and must never echo raw caller id/text.

`ConversationPost(ctx, socketPath, conversationID, text) error` returns transport
or refusal errors, even alongside true OK. Absent/false/null OK returns
`control: conversation.post response missing ok flag`.

This submits a **user message** by opaque id. Existing
[Channel: post a message into an existing channel](#channel-post-a-message-into-an-existing-channel-channelpost-2497)
publishes **host-authored assistant content** by channel label, creating a
channel on a label miss, and [carries it into the next user turn](control-plane-channel-post-carry.md).
`channel.post` does not start that user turn. Its API and wire encodings remain
compatible. See the [contract spec](../../specs/architecture/2885-conversation-post-contract.md).

## Channel: post a message into an existing channel (channel.post, #2497)

`channel.post` durably accepts a whole message without spawning Claude. `channelPoster` resolves the label and mints a turn ID; private `channelDelivery` persists the post. Its sole consumer records ordered deltas plus one host-post completion, then shares replay, fans out live and triggers wake. `handleChannelPost` is the wire boundary; `Server.SetChannelPoster` installs the guarded callback. Success means acceptance, including when delivery must retry.

**No `Cwd` crosses this wire — the single largest security difference from `channel.new`.** The miss path always creates under the daemon's own `resolveDefaultCwd` value, reusing `channelCreator` unchanged rather than a second create path (both `runSupervisor` setters now close over one hoisted `createChannel` local). A verb with no caller-supplied path has none of `channel.new`'s `$HOME`-confinement surface to get wrong.

**The name scan must set both `ListFilter` pointer fields, not just `IsPromoted`.** `IsPromoted` and `IsArchived` AND (see [conversations-registry-crud.md](conversations-registry-crud.md) § `List`); a filter setting only the first silently includes archived channels, and this verb would then post into one that was archived precisely so it would stop being read. Zero matches create; exactly one posts; two or more refuse by count without repeating the caller's name in the refusal — the registry enforces no name-uniqueness rule, so a mistyped `--name` creates rather than refuses and stays diagnosable only through `channelCreator`'s own `channel_new.created` log line.

**A result-free verb answers `Response{OK: true}` — no `ChannelPostResult` type.** Second use of the shape `sessions.rename` established; a verb whose only client is a cron that wants silence on success needs nothing back.

**The byte cap (`MaxChannelPostBytes`, 64 KiB, comfortably under `wssclient.maxFrameBytes`'s 1 MiB) is checked on both sides for different reasons, not redundantly.** The daemon's check is the authorization contract. The CLI's check (`channelPostContent`) is a resource bound on its own `--file` read, and it must open the file once under an `io.LimitReader` rather than `os.Stat` the size and then open — the check-then-use shape reports one inode's size and reads another, and only the bounded read makes `--file /dev/zero` a refusal instead of a hang.

**Flag "given" is decided by `fs.Visit`, not by an empty value.** `--text ""` is a caller who chose an empty message, not an absent flag; reading it as "not given" would report a usage error (exit 2) for a content problem that `channel.post: empty message` (exit 1) already owns.

**Delivery needs successful history writes.** `channelDelivery.deliver` reconciles every history page, records missing chunks and completion before replay/live/wake, and retains failures for retry. Completed pending work is cleanup-only, with no repeated announcement or wake. The shared safe boundary holds through Claude completion or confirmed stop; FIFO posts finish before successors. The five-minute diagnostic keeps holding. See [delivery and recovery](control-plane-channel-post-live-delivery.md#live-announcements-bounded-replay-and-durable-recovery) for startup, carry independence and daemon-resident bounded replay; restart uses history, and tail catch-up remains #2744.

**The text is `protocol.AssistantDeltaPayload` under `protocol.TypeAssistantDelta`.** The original `message`/role-assistant shape never drew: desktop's `translateTimelineEvent` returns `null` live and from served history. A lone delta renders but does not finish or notify. Each post now ends with exactly four fields: `conversation_id`, `turn_id`, `stop_reason: "end_turn"`, `producer: "channel_post"`, identifying daemon provenance without Claude result fields or a second text representation. Shared ring event IDs are connection-independent and distinct from history IDs; completion fan-out precedes the existing wake path. Claude reporting and client notification rules stay unchanged. The ten historic posts retain text and missed alerts, without migration or re-notification. See [completed channel posts](control-plane-channel-post-live-delivery.md).

**No relay reachability for the verb itself.** No `protocol` type is minted for `channel.post` and `internal/relay` never constructs a `control.Request` for it; a relay client structurally has no frame with which to send this verb, and it stays reachable only through the control socket. That says nothing about the *content* `channel.post` writes, though: since #2498 the same payload it appends is also fanned out live to every attached interactive client — see [Fanning `channel.post` out](control-plane-channel-post-live-delivery.md).

See `docs/specs/architecture/2497-channel-post-control-verb.md` for the full design and security review.

## Fanning `channel.new` out: `conversation_updated` as an unsolicited push (#2156)

A successful create now fans one `conversation_updated` frame to every interactive-capable client, so a channel created from the host's shell appears in an open desktop client without a reconnect. This is `conversation_updated`'s first unsolicited producer — most of the wire family's other write verbs (`promote_conversation`, `rename_conversation`, `archive_conversation`, `change_workspace`, `set_system_prompt`) reply to the requester only, because a host-side create has no requester to reply to. `set_conversation_muted` (#2572) is the one write verb that does both: it replies to the requester **and** fans the same record out through this same push, reusing this emitter via the closure `send_message`'s auto-naming push (#2159) already took — a mute needs every paired client to stop alerting without a re-list, which a requester-only reply can't do. `conversationUpdateEmitterV2` copies `attachmentOfferEmitterV2`'s shape (see [§ Announcing the store](control-plane-attachment-file-confine-and-store-a-claude-named-path.md#announcing-the-store-attachment_offered-and-the-name-it-must-not-derive-from-2166)): a bare func returned from `startRelay`, nil when the relay leg is off, called last on the success path, and never able to turn a successful create into a refusal.

**On a broadcast-widening change, audit the field set against what the recipient can already pull, not only who receives the frame.** The push turns a unicast reply into a broadcast, so the instinct is to check the audience. The sharper check is the payload: `ConversationUpdatedPayload`'s fields are a strict subset of `ConversationSummary`'s (eight of nine as of #2571's `IsMuted`, which landed on both the same way `WorkspaceLabel` did at #2210 — `ConversationSummary`'s extra field is `LastMessageTS`, which `conversation_updated` has never carried), and every conn that can receive this push can already call `list_conversations` and read the same row — the push delivers sooner a value it could not otherwise be denied, so the audience question turns out to be free. The real question is the payload's *source*. Announcing the create request's raw `cwd` would put an unconfined, CLI-authored string on the wire; only building the payload from a `reg.Get` read-back — the stored, symlink-resolved path — keeps it the confined value. An AC that reads like an accuracy requirement ("read back from the registry rather than assembled from the request") can be the trust boundary itself; check what a "just build it from what's already in scope" shortcut would actually put on the wire before taking it.

No log line on the announce path carries `Cwd` or `Name` — both are host filesystem strings, one a workspace path and the other its derived label. Test this per branch (success, dropped-push, ctx-cancelled, unexpected-error), not once on the happy path, and pick fixture values distinctive enough that the assertion can't pass by accident — a path or name built from a common word like "project" matches too much of the daemon's own log vocabulary to prove the field was actually excluded.

See `docs/specs/architecture/2156-conversation-updated-host-create-fanout.md` for the full design and security review, and [protocol-package-drift-detectors.md § the `excludedTypes` classification key](protocol-package-drift-detectors.md) for how `relay_guard_test.go` records a type with two producers of different shapes.

See [Fanning `channel.post` out: `assistant_delta` live delivery (#2498)](control-plane-channel-post-live-delivery.md) for the equivalent unsolicited push a successful `channel.post` makes.

## Process-Global vs Per-Session

| Concern | Scope today | Source |
|---|---|---|
| `status` payload | per-session (one supervisor) | `sess.State()` |
| `logs` ring buffer | process-global | `LogProvider`, written by all loggers |
| `stop` shutdown | process-global | `shutdown` cancel func |

`pyry sessions new` (#76) and `pyry sessions list` extend the per-session column. Logs and stop stay process-global until a concrete need pushes them otherwise.

## Keeping a value out of the log ring (`LogDaemonOnly`, #2723)

`SlogTee` (`logs.go`) wraps the daemon's primary `slog.Handler` so every record also lands in `RingBuffer`, which backs both `pyry logs` and `debugbundle.Assemble`'s only log source — and that bundle reaches a paired phone with no redaction ([debugbundle-package.md](debugbundle-package.md)). Most of what the daemon logs is safe to put in front of a phone; the one exception so far is a child process's own stderr, which can hold a file path or a credential.

An attribute value whose type exposes a `LogDaemonOnly()` method reaches the primary handler — the daemon's own log output — untouched, but is replaced by the constant `(daemon log only)` in the ring copy, at any nesting depth: a plain record attribute, one added via `logger.With(...)`, or one inside an `slog.Group`. Not even the value's length reaches the ring, since the replacement is a fixed string rather than something derived from it.

The marker is a method-set contract (`daemonLogOnly interface{ LogDaemonOnly() }`), not a shared type exported from this package, because `internal/control` imports `internal/sessions` and a producer package (`internal/streamsup`) must not grow that edge just to mark a value. Any package can satisfy it without importing `internal/control`.

First producer: `streamsup`'s stderr tail on the `claude exited` record, when the child exited on its own — see [streamsup-package-supervise-loop-run.md § `claude exited` record](streamsup-package-supervise-loop-run.md#claude-exited-record-session-id-and-a-capped-stderr-tail-2723). This is a per-value opt-out a producer makes deliberately, not a general-purpose redactor for the ring or the bundle — none exists, and nothing about this mechanism scans a value's content for secrets.

## Lifecycle

`runSupervisor` binds `ctrl.Listen` after constructing the pool, before loading
pending channel posts or starting queue/relay consumers. `Listen` claims instance
ownership; after the hooks are installed, `serveControlWhenReady` waits for
`Pool.Ready` before calling `Serve` (#2866). Connections may queue on the bound
socket, but no handler enters before readiness. This is the pool's wired
supervisor handle, rather than an observed running bootstrap child; see the
[readiness contract](sessions-package-key-types-config-bootstrapevicted-pool-ready.md).
Loading pending state before the bind would let a rejected second daemon drain the owner's snapshot
and overwrite accepted work. The sole `channelDelivery` consumer shares the
composition root's history store and runs even without a relay URL.

The readiness wait uses the daemon context and returns its error on startup
cancellation. Waiting on detached `controlCtx` would hang the startup join if
readiness never closed. Ending that wait does not close the listener: the
composition root retains ownership until writer cleanup reaches `ctrl.Close`.

Control serving uses `controlCtx`, detached from daemon cancellation. `Serve`
closes its listener on cancellation, so sharing the daemon context would release
ownership while old delivery cleanup or acceptance could still persist a stale
snapshot over a replacement daemon's new post.

Shutdown cancels the daemon context, seals and joins complete post callbacks
with `channelDelivery.stopAccepting`, and joins the delivery consumer. Only then
does it cancel control serving, close the listener and join response handlers.
Early-return defers preserve the same writer-before-socket-release ordering.
`guardPoster` covers lookup/creation as well as acceptance; sealed callbacks and
direct acceptance refuse without storage or carry work. Lock order is handler
gate then delivery mutex; the consumer takes only the delivery mutex.

Every control verb is one request and response, so `Serve`'s handler wait remains
the final control join. The old streaming-attach abort is historical: its handler
was deleted in #1348 and its unused server bookkeeping in #1536.
`TestChannelDelivery_RejectedDaemonLeavesPendingUntouched` checks ownership
before loading accepted or malformed state. `TestChannelDelivery_ShutdownRetainsOwnershipUntilWritersStop`
pauses both append and acceptance persistence, proving replacement startup stays
refused until writers finish and replacement acceptance survives reload.

## References

- [`sessions-package.md`](sessions-package.md) — the package providing `*Session`, `*Pool`, `SessionID`.
- [ADR 003](../decisions/003-session-addressable-runtime.md) — why the resolver seam exists.
- Spec: [`docs/specs/architecture/29-wire-sessions-pool-consumers.md`](../../specs/architecture/29-wire-sessions-pool-consumers.md).
- Pairing mint spec: [`docs/specs/architecture/2388-local-pairing-code-mint.md`](../../specs/architecture/2388-local-pairing-code-mint.md).
- Relay-backed pairing provider spec: [`docs/specs/architecture/2389-bind-local-pairing-mint-to-relay-state.md`](../../specs/architecture/2389-bind-local-pairing-mint-to-relay-state.md).


## Sections

This overview is split across the documents below. Each is kept small so
search can reach it.

- [Server construction and deadlines](control-plane-server-and-deadlines.md) — dependency installation, pairing/update providers and request/response I/O bounds.
- [Testing](control-plane-testing.md) — wire/provider boundaries, startup readiness, cancellation and ownership proofs.
- [Sessions: list seam (1.1b-B1)](control-plane-sessions-list-seam-1-1b-b1.md) — The fourth `sessions.*` verb is `sessions.list` — the first read-side member of the namespace. 
- [Sessions: has-id seam (1.3c-1)](control-plane-sessions-has-id-seam-1-3c-1.md) — The fifth `sessions.*` verb is `sessions.has-id` — a one-bit existence query. 
- [Rekey: V2 conn re-key trigger seam (1.3d-1, #459 + #462)](control-plane-rekey-v2-conn-re-key-trigger-seam-1-3d-1.md) — `VerbRekey` lets a local operator client trigger an immediate Noise re-key on a named v2 conn through the control socket. 
- [Approve: mcp.approve verb — forward to permbridge, block, default-deny (#1104)](control-plane-approve-mcp-approve-verb-forward-to-permbridge.md) — `VerbMCPApprove` ("mcp.approve", dotted like `sessions.*`) forwards a claude tool-approval request from the `pyry mcp-approve` subcommand…
- [Attachment.file: file a claude-named host file under the calling session's conversation (#2164)](control-plane-attachment-file-confine-and-store-a-claude-named-path.md) — `VerbAttachFile` confines a model-chosen filesystem path to the calling session's conversation workspace before reading it; ships live but inert (#2165 wires a caller).
- [Sessions: CLI Router (1.1a-B2)](control-plane-sessions-cli-router-1-1a-b2.md) — `pyry sessions <verb>` is the operator-facing surface for the `sessions.*` namespace. 
- [Client dial: transient-startup retry (#198 + #199)](control-plane-client-dial-transient-startup-retry.md) — `internal/control/dial.go` houses the dial-side surface for the control client: the `dial()` primitive every client verb routes through,…
- [Durable `channel.post` delivery (#2498, #2810, #2811)](control-plane-channel-post-live-delivery.md) — Accept while busy, deliver promptly when idle or after published completion/confirmed stop, and finish FIFO posts before successor turns; retry and safe startup recovery preserve identity. Five-minute diagnostics keep holding. All history chunks precede live announcement, independently of Claude carry.

- [Carrying posted channel messages into the next user turn](control-plane-channel-post-carry.md) — Delivery-time composition, confirmed-delivery clearing and send-now exclusion.
