# Control Plane

`internal/control` exposes the on-disk control surface of `pyry`: a Unix domain socket (`~/.pyry/<name>.sock`, mode `0600`) speaking line-delimited JSON. Each connection is one request, one response — every verb `Server.handle` dispatches replies with one JSON `Response` and returns; no verb hands off connection ownership. (`VerbAttach` was the one verb that did, until #1348 deleted its server-side handler and #1535 deleted the now-orphaned wire type itself.)

Verbs today: `status`, `stop`, `logs`, `sessions.new`, `sessions.rm`, `sessions.rename`, `sessions.list`, `sessions.has-id`, `rekey`, `mcp.approve`, `attachment.file` (#2164, ships live but inert — see [§ Attachment.file](control-plane-attachment-file-confine-and-store-a-claude-named-path.md)), `channel.new` (#2155, see [§ Channel: new verb](#channel-new-verb-channelnew-2155) below), and `pairing.mint` (#2388). `attach` and `resize` are gone — #1535 deleted `VerbAttach`, `VerbResize`, `AttachPayload`, `ResizePayload`, and the `Request.Attach`/`Request.Resize` fields, plus the orphaned `control.SendResize` client helper, none of which had a live dispatch arm since #1348. The deletion is decode-compatible with a stale (pre-#1348) client: nothing in the repo calls `json.Decoder.DisallowUnknownFields`, and Go's `encoding/json` ignores unknown object fields by default, so a client still sending `{"verb":"attach","attach":{…}}` decodes cleanly and gets the same `unknown verb: "attach"` reply it already got post-#1348 — deleting a `Request` field only ever *widens* what the decoder accepts, never narrows it. The wire shape otherwise is held stable across phases — `VerbSessionsNew` (#75) adds a `Request.Sessions *SessionsPayload` field with `omitempty` so existing-verb wire output stays byte-identical (pinned by `TestProtocol_SessionsRoundTripBackCompat`). `VerbSessionsRm` (#98) extends `SessionsPayload` with `ID`/`JSONLPolicy` (both `omitempty`) and adds a `Response.ErrorCode` field (also `omitempty`) for typed-sentinel propagation — same back-compat guard, byte-identical existing-verb output. `VerbSessionsRename` (#90) extends `SessionsPayload` with one further `omitempty` field (`NewLabel`) and reuses the `Response.ErrorCode` envelope verbatim — no new wire constants. `VerbPairingMint` follows the same additive rule: the optional outer `Request.Pairing` and `Response.Pairing` fields preserve every older encoding, while the inner `PairingPayload` deliberately always emits both `DeviceLabel` and `AllowRemotePermissions`. Those are the only caller-selected mint inputs; `PairingResult.Pairing` is an opaque plaintext bearer credential, not a place to expose identity, key, relay, registry, expiry, or diagnostics.

## Server Construction

```go
func NewServer(
    socketPath string,
    sessions   SessionResolver,
    logs       LogProvider,
    shutdown   func(),
    log        *slog.Logger,
    sessioner  Sessioner,
) *Server
```

`sessions` is the only required dependency that nil-panics at construction. Programmer error surfaces immediately, not on the first request from a future shell.

`logs`, `shutdown`, and `sessioner` are optional. When nil, the corresponding verb returns an error response — used in tests that care about isolated verbs. `sessioner` is wired in production to `*sessions.Pool` in `cmd/pyry/main.go` (#116); `*sessions.Pool` satisfies `Sessioner` directly because `Pool.Create` returns `sessions.SessionID`, matching the interface signature with no adapter (contrast with `poolResolver` for the read-side `Lookup`). Pre-#116 the call site passed `nil` and `VerbSessionsNew` returned `"sessions.new: no sessioner configured"`. See `docs/specs/architecture/75-control-sessions-new.md` for the seam design.

Dependencies whose implementations need daemon composition state are installed after construction rather than widening `NewServer`. `SetPairingProvider` installs the narrow `func(deviceLabel string, allowRemotePermissions bool) (string, error)` used by `VerbPairingMint`; the provider owns every identity, key, relay, registry, and persistence input that the request cannot supply. `handlePairingMint` copies the closure under `Server.mu` and releases the lock before invoking it, so a slow provider does not serialize unrelated control verbs behind the server lock.

For a relay-enabled daemon, `runSupervisor` installs that provider before
`Server.Listen` from the same `pairingMinterV2` that `startRelayV2` constructed
for the active relay leg. The closure therefore carries the running daemon's
already-resolved server id, relay URL, static public key, and registry path; it
does not reload saved configuration that may describe another service. Relay
setup failures never return a provider, and a relay-disabled daemon deliberately
leaves the seam nil.

The pairing seam is also a credential-redaction boundary. An absent provider returns exactly `pairing.mint: provider not configured`; a missing payload or any provider error returns exactly `pairing.mint: operation failed`. On the error branch, `handlePairingMint` discards both the provider's returned string and its error detail, emits no control-layer log, and leaves `Response.Pairing` absent. `MintPairing` likewise returns an empty string for every error, including the fixed `control: empty pairing.mint response` guard for a missing or empty success payload. Only a nil-error, non-empty pairing reaches the caller.

Operational failure detail from the concrete provider, including a registry
path, remains daemon-only. Its fixed success and failure events omit the
caller-supplied label, token, token hash, and encoded pairing, so copying the log
snapshot into a diagnostic bundle does not create another credential egress.

## Handshake Deadline: per-conn timeout and the session-verb extend (#865)

`handle` (the per-conn goroutine `Serve` spawns) sets `conn.SetDeadline(time.Now().Add(s.handshakeTimeout))` before decoding the client's JSON request — the bound that limits how long a connected-but-silent client can pin a per-conn goroutine. `s.handshakeTimeout` defaults to `defaultHandshakeTimeout` (5s), set once in `NewServer`'s struct literal; same-package tests may shrink it via the unexported `Server.handshakeTimeout` field (written once before `Serve` starts, read-only per-conn thereafter — no lock needed, same post-construction-override shape as `SetRekeyer`).

Two handler paths move it after the handshake read has already completed:

- `handleApprove` **clears** it (`conn.SetDeadline(time.Time{})`) for the length of a blocking approval wait — the conn stays open for however long the human decision takes, until `mcpApprovalTimeout` or a disconnect/shutdown watcher resolves it. (Until #1535, `handleAttach` was the other clearer, handing the conn to the bridge for the indefinite life of an attachment; that verb and its handler are gone.)
- `handleSessionsNew` / `handleSessionsRm` **extend** it to `sessionOpTimeout + sessionOpConnGrace` (30s + 5s = 35s) immediately before calling `Pool.Create` / `Pool.Remove`. Before #865, the deadline was left at its 5s handshake value while each handler's own ctx budgeted 30s for the op — once `Create`/`Remove` ran past 5s (routine on a cold claude spawn: documented 2-15s latency), the final `enc.Encode(Response{...})` failed with a silently-discarded deadline error and the client's read got EOF, even though the mutation had actually succeeded (an operator-visible orphan on `sessions.new`, a false failure on `sessions.rm`). Extending — not clearing — keeps the 30s op ctx as the binding budget on the normal path, while a write that's still stuck at 35s hits a hard upper bound rather than hanging the conn goroutine forever.

Both extend calls run strictly after `handle` has decoded the request, so the handshake-read bound is unaffected by either verb: a silent client (no request sent) is still cut off at `s.handshakeTimeout` before either handler is reached. See [`codebase/865.md`](codebase/865.md) for the fix and its regression tests.

`pairing.mint` needs a different two-sided bound. `MintPairing` derives the earlier of the caller's existing deadline and `time.Now().Add(DialTimeout)` before it calls `request`, so dial retry, encode, and decode consume one operation-wide budget; changing `request` globally would incorrectly shorten callers that intentionally choose a longer deadline. Server-side, `handlePairingMint` retains `handle`'s finite request-read deadline and installs a fresh `DialTimeout` response-write deadline before entering the provider. The write is therefore bounded even if the provider returns after the original handshake window.

The provider closure is synchronous and has no context, so these I/O deadlines cannot cancel it after entry. A client may return on its deadline while the provider is still running; the handler attempts its already-bounded write only after the provider returns, and `Serve` continues to drain that in-flight handler during shutdown. Do not turn the deadline into a detached worker or describe it as a provider-execution timeout—the concrete provider must bound its own lock waits and local I/O.

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

- **The creator seam is a narrow mint-only closure, not `*sessions.Pool` itself.** `Server.channelCreator func(cwd, name string) (string, error)`, installed via `SetChannelCreator` alongside `SetFileAttacher` — a plain func behind a late-bound setter, not a `NewServer` parameter (the same "every dependency lives at the `cmd/pyry` composition root" argument as `SetFileAttacher`). The `cmd/pyry`-side implementation itself narrows further: it closes over a bare `mint func(label, spawnDir string) (string, error)` rather than the whole `*sessions.Pool`, mirroring the narrowing `sessionMinter.Create` already does for the wire `create_conversation` path (see [conversation-session-binding.md § The `SessionCreator` seam](conversation-session-binding.md#the-sessioncreator-seam-keeps-handlers-import-clean)). Taking the concrete `*Pool` would have forced every creator unit test to stand one up.
- **The creator sits at the same layer as `sessionMinter`, so it needs neither of the two designs the ticket proposed.** `sessionMinter.Create` is `resolveSpawnDir` followed by `pool.Mint` — both callable directly from `cmd/pyry`. A verb that needs the *resolved* path back (this one derives the channel's name from it) does not have to make the minter re-resolve a second time, nor widen `handlers.SessionCreator` to return it. Check whether new work can be written at the caller's own layer before reaching for either "call twice" or "widen the interface."

**`resolveSpawnDir`'s empty-string arm is fail-open by contract, and every non-wire caller must re-guard it.** Per [conversation-session-binding.md](conversation-session-binding.md#the-sessioncreator-seam-keeps-handlers-import-clean), `resolveSpawnDir("")` returns `("", nil)` — success, meaning "the daemon's shared trusted workdir," with **no** confinement and **no** trust-mark. That is correct for the phone's optional-`Cwd` path but wrong for a verb where an empty cwd can only mean a bug or a stray caller: unguarded, it would silently write a row with `Cwd: ""` and a name of `"."`. `channelCreator` therefore rejects an empty cwd itself, *in addition to* `handleChannelNew`'s wire-shape check — the second guard is not redundant, it protects the seam from any future caller that doesn't come through the handler (`handleAttachFile`'s empty-`SessionID` double guard is the precedent). Confirmed by mutation: deleting the creator's own guard makes the empty-cwd test pass a `cwd` straight through instead of failing loud.

**Refusals need three static buckets, not the two the design sketched, because "rejected" and "failed" are different verdicts.** A `$HOME`-escaping directory is a deterministic, permanent refusal; a `trustMark` write failure is a transient one about `~/.claude.json`. Both must stay off the wire verbatim (`confineWorkdirToHomeCreating`'s error text embeds the resolved path *and* `$HOME`), but collapsing them into one static message tells the operator their project folder isn't allowed when the real fault is a local write error — sending them looking in the wrong place. Split with `errors.Is(err, handlers.ErrSpawnDirRejected)`, not by matching message text, so the split survives future wrapping.

**A message-prefix discriminator between "server rejected" and "transport failed" isn't always worth building.** `rekeyVerdict`'s `isServerReject` (a hand-maintained message-prefix list) exists because `pyry rekey`'s acceptance criteria wanted a different stderr prefix per class. `channel.new`'s AC only asks for one stderr line and exit 1 on any failure, so `channelNewVerdict` skips the discrimination entirely rather than adding a second hand-maintained list that would go stale the first time a server message got reworded. Check what the AC actually distinguishes before copying a sibling's verdict-formatter shape wholesale.

See `docs/specs/architecture/2155-channel-new-control-verb.md` for the full design and security review.

## Channel: post a message into an existing channel (channel.post, #2497)

`channel.post` records one message in a named channel's durable log without spawning claude or starting a turn: `channelPoster` (`cmd/pyry/channel.go`) resolves the label, `handleChannelPost` (`internal/control/server.go`) is the wire boundary, and `Server.SetChannelPoster` is the same late-bound-closure shape as `SetChannelCreator`. First consumer is a daily cron on pyrybox posting one open question from the vault into a `questions` channel, replacing a kept Discord bot token nothing read.

**No `Cwd` crosses this wire — the single largest security difference from `channel.new`.** The miss path always creates under the daemon's own `resolveDefaultCwd` value, reusing `channelCreator` unchanged rather than a second create path (both `runSupervisor` setters now close over one hoisted `createChannel` local). A verb with no caller-supplied path has none of `channel.new`'s `$HOME`-confinement surface to get wrong.

**The name scan must set both `ListFilter` pointer fields, not just `IsPromoted`.** `IsPromoted` and `IsArchived` AND (see [conversations-registry-crud.md](conversations-registry-crud.md) § `List`); a filter setting only the first silently includes archived channels, and this verb would then post into one that was archived precisely so it would stop being read. Zero matches create; exactly one posts; two or more refuse by count without repeating the caller's name in the refusal — the registry enforces no name-uniqueness rule, so a mistyped `--name` creates rather than refuses and stays diagnosable only through `channelCreator`'s own `channel_new.created` log line.

**A result-free verb answers `Response{OK: true}` — no `ChannelPostResult` type.** Second use of the shape `sessions.rename` established; a verb whose only client is a cron that wants silence on success needs nothing back.

**The byte cap (`MaxChannelPostBytes`, 64 KiB, comfortably under `wssclient.maxFrameBytes`'s 1 MiB) is checked on both sides for different reasons, not redundantly.** The daemon's check is the authorization contract. The CLI's check (`channelPostContent`) is a resource bound on its own `--file` read, and it must open the file once under an `io.LimitReader` rather than `os.Stat` the size and then open — the check-then-use shape reports one inode's size and reads another, and only the bounded read makes `--file /dev/zero` a refusal instead of a hang.

**Flag "given" is decided by `fs.Visit`, not by an empty value.** `--text ""` is a caller who chose an empty message, not an absent flag; reading it as "not given" would report a usage error (exit 2) for a content problem that `channel.post: empty message` (exit 1) already owns.

**Why the poster does not route through `appendConversationHistory`.** That seam returns nothing by contract because its two stream producers have already sent their wire frame by the time it runs — a failed append must never suppress an emit that already went out. The poster's own ordering runs the other way: every chunk is appended to the durable log *before* any is announced ([#2498](control-plane-channel-post-live-delivery.md)), so the append is still the thing that must fail the post, and now for a second reason — a frame drawn for a message the log does not hold would vanish on the next connect, which is worse than never drawing it. The poster calls `history.Store.Append` (narrowed to a func, `conversationHistory.Append`) directly and maps its error through the existing `historyAppendFailure` discriminant instead; `TestChannelPoster_AppendFailureAnnouncesNothing` pins the append-then-announce order from both sides.

**The recorded entry is `protocol.AssistantDeltaPayload` under `protocol.TypeAssistantDelta`, not the `message`/role-assistant shape this verb originally wrote.** #2497 shipped the latter on the forecast that a client would read it back on its next connect; #2498 found that forecast false — desktop's `translateTimelineEvent` returns `null` for a `message` entry with `role: "assistant"` on both the live path and the served-history path, so the record was valid but never drawn. Repointing the record at `assistant_delta`, the type the current clients actually render as assistant text, is what makes "one record" and "one rendering" the same decision instead of two independently-maintained ones. See [Fanning `channel.post` out](control-plane-channel-post-live-delivery.md) for the live half this repointing was for.

**No relay reachability for the verb itself.** No `protocol` type is minted for `channel.post` and `internal/relay` never constructs a `control.Request` for it; a relay client structurally has no frame with which to send this verb, and it stays reachable only through the control socket. That says nothing about the *content* `channel.post` writes, though: since #2498 the same payload it appends is also fanned out live to every attached interactive client — see [Fanning `channel.post` out](control-plane-channel-post-live-delivery.md).

See `docs/specs/architecture/2497-channel-post-control-verb.md` for the full design and security review.

## Fanning `channel.new` out: `conversation_updated` as an unsolicited push (#2156)

A successful create now fans one `conversation_updated` frame to every interactive-capable client, so a channel created from the host's shell appears in an open desktop client without a reconnect. This is `conversation_updated`'s first unsolicited producer — most of the wire family's other write verbs (`promote_conversation`, `rename_conversation`, `archive_conversation`, `change_workspace`, `set_system_prompt`) reply to the requester only, because a host-side create has no requester to reply to. `set_conversation_muted` (#2572) is the one write verb that does both: it replies to the requester **and** fans the same record out through this same push, reusing this emitter via the closure `send_message`'s auto-naming push (#2159) already took — a mute needs every paired client to stop alerting without a re-list, which a requester-only reply can't do. `conversationUpdateEmitterV2` copies `attachmentOfferEmitterV2`'s shape (see [§ Announcing the store](control-plane-attachment-file-confine-and-store-a-claude-named-path.md#announcing-the-store-attachment_offered-and-the-name-it-must-not-derive-from-2166)): a bare func returned from `startRelay`, nil when the relay leg is off, called last on the success path, and never able to turn a successful create into a refusal.

**On a broadcast-widening change, audit the field set against what the recipient can already pull, not only who receives the frame.** The push turns a unicast reply into a broadcast, so the instinct is to check the audience. The sharper check is the payload: `ConversationUpdatedPayload`'s fields are a strict subset of `ConversationSummary`'s (eight of nine as of #2571's `IsMuted`, which landed on both the same way `WorkspaceLabel` did at #2210 — `ConversationSummary`'s extra field is `LastMessageTS`, which `conversation_updated` has never carried), and every conn that can receive this push can already call `list_conversations` and read the same row — the push delivers sooner a value it could not otherwise be denied, so the audience question turns out to be free. The real question is the payload's *source*. Announcing the create request's raw `cwd` would put an unconfined, CLI-authored string on the wire; only building the payload from a `reg.Get` read-back — the stored, symlink-resolved path — keeps it the confined value. An AC that reads like an accuracy requirement ("read back from the registry rather than assembled from the request") can be the trust boundary itself; check what a "just build it from what's already in scope" shortcut would actually put on the wire before taking it.

No log line on the announce path carries `Cwd` or `Name` — both are host filesystem strings, one a workspace path and the other its derived label. Test this per branch (success, dropped-push, ctx-cancelled, unexpected-error), not once on the happy path, and pick fixture values distinctive enough that the assertion can't pass by accident — a path or name built from a common word like "project" matches too much of the daemon's own log vocabulary to prove the field was actually excluded.

See `docs/specs/architecture/2156-conversation-updated-host-create-fanout.md` for the full design and security review, and [protocol-package-drift-detectors.md § the `excludedTypes` classification key](protocol-package-drift-detectors.md) for how `relay_guard_test.go` records a type with two producers of different shapes.

See [Fanning `channel.post` out: `assistant_delta` live delivery (#2498)](control-plane-channel-post-live-delivery.md) for the equivalent unsolicited push a successful `channel.post` makes.

## Carrying a posted channel message into claude's next turn (#2499)

A post now leads the next user turn the daemon delivers for that conversation's client message path, so claude sees the question and the operator's reply in the order the two happened. `channelCarry` (`cmd/pyry/channel_carry.go`) holds the pending text in memory and composes it; `conversations.Registry.AppendPendingChannelPost` / `PendingChannelPosts` / `ClearPendingChannelPosts` (see [conversations-registry-crud.md § `AppendPendingChannelPost` / `PendingChannelPosts` / `ClearPendingChannelPosts`](conversations-registry-crud.md#appendpendingchannelpost--pendingchannelposts--clearpendingchannelposts-2499)) hold it durably.

**Composed at delivery, not at enqueue — a `msgqueue.DeliverFunc` decorator, not a `newInboundDeliver` parameter.** Composing at enqueue misses a post that lands while a reply is already queued, since the queue's head can sit through a whole claude turn before it is written. `newInboundDeliver` has 17 call sites (`codegraph_callers`), 16 of them tests, which is what made a signature widening the wrong shape; `carryPending` wraps it the same way `markApprovalHolds` already does in that file, so the wiring cost is one line rather than a fan-out. Check call-site count before reaching for a new parameter on an existing seam — a decorator is often both cheaper to wire and better factored.

**Cleared from `OnDelivered`, the msgqueue seam that fires once per confirmed delivery — chained, not replaced.** `msgqueue.Config.OnDelivered` is a single-valued field already held by `newOperatorMessageHistory` ([msgqueue-package.md § Delivered notification](msgqueue-package.md#delivered-notification-2115)); a `deliveredFuncs(...msgqueue.DeliveredFunc) msgqueue.DeliveredFunc` combinator in `channel_carry.go` fans it to both consumers, history first and the clear second, preserving #2115's "as close to the commit as possible" ordering. A failed delivery retried at the same head recomposes on every attempt, so a post that lands during a failed attempt is carried by the retry and cleared exactly once, by the attempt that actually succeeds.

**The composed text reaches claude only, structurally, not by filtering.** `OnDelivered` carries a `msgqueue.QueuedMessage`, which declares no field for the composed payload — only `Text`, the client's own — so the durable history producer and the wire cannot see the carried text even if a future edit tried to read it from there. The same barrier #2115 built for the operator-history producer.

**Why the growth bound refuses the newest post instead of evicting the oldest is a concurrency-correctness finding, not a style choice** — see [conversations-registry-crud.md § `AppendPendingChannelPost`...](conversations-registry-crud.md#appendpendingchannelpost--pendingchannelposts--clearpendingchannelposts-2499).

See `docs/specs/architecture/2499-carry-posted-channel-message.md` for the full design and security review.

## Process-Global vs Per-Session

| Concern | Scope today | Source |
|---|---|---|
| `status` payload | per-session (one supervisor) | `sess.State()` |
| `logs` ring buffer | process-global | `LogProvider`, written by all loggers |
| `stop` shutdown | process-global | `shutdown` cancel func |

`pyry sessions new` (#76) and `pyry sessions list` extend the per-session column. Logs and stop stay process-global until a concrete need pushes them otherwise.

## Lifecycle

Two top-level goroutines, unchanged from Phase 0:

1. **Main goroutine** — calls `pool.Run(ctx)`, blocks until ctx cancellation.
2. **Control goroutine** — `go ctrl.Serve(ctx)`, accepts client connections, dispatches verbs.

Shutdown: `SIGINT`/`SIGTERM` → `signal.NotifyContext` cancels the context → `pool.Run` returns `context.Canceled` → `ctrl.Close()` removes the socket file → in-flight handlers drain via `handleWG.Wait()` in `Serve`'s accept-error path.

That single wait is sufficient because every verb is one-shot (see § above): a handler reads its request, writes its response, and returns, so there is no per-conn goroutine that can outlive the handler and no indefinite handoff to wait out. That was not always true — #863 built a three-layer abort (a `control.Server`-side streaming-conn set, a `supervisor.Bridge.Shutdown()`, and a `sessions.Session.Run`-deferred call into it) to unblock `VerbAttach`'s indefinite per-conn handoff, whose input pump could otherwise park forever on an idle client and hang `Serve` until the service manager escalated to SIGKILL. `VerbAttach`'s handler was deleted in #1348, `supervisor.Bridge` no longer exists in the tree, and #1536 deleted the now-writer-less `control.Server` side of that abort (`streamingWG`, `streamConns`) as dead bookkeeping — `Wait` on a zero `WaitGroup` had been returning immediately and the set had been empty since #1348. See [`docs/knowledge/codebase/863.md`](codebase/863.md) for that design as history; it no longer describes the live shutdown path.

## Testing

`server_test.go`, `logs_test.go` exercise the full surface with `fakeResolver` + `fakeSession` test doubles satisfying `SessionResolver` + `Session`. `recordingResolver` records both `Lookup` and `ResolveID` arguments.

`pairing_test.go` treats the bearer result as a boundary, not ordinary response data. `TestMintPairing_WireRoundTrip` compares exact raw JSON for both boolean values and the typed pairing reply, while `TestProtocol_SessionsRoundTripBackCompat` proves the new optional outer fields did not change older verb bytes. Provider tests assert exactly one call with both arguments and use distinct success-pairing and provider-error sentinels to prove that only the successful return value can contain the credential; neither sentinel may enter control logs, response errors, transport diagnostics, or any error-path result.

The daemon-side pairing tests add the construction proof that an isolated
control-server fake cannot provide: `TestLocalPairingProviderWiredFromRelayConstructionToControl`
pins the provider from `startRelayV2` through `startRelay` to
`runSupervisor`'s `SetPairingProvider` call. The companion two-socket test uses
distinct identities, keys, relay URLs, and registry paths. Its cross-registry
negative assertion searches by token hash rather than by device label; a
label-based lookup alone would stay green if the credential were written into
the wrong registry under another name. Lock, malformed-load, and save failures
also prove the fixed client error and inspect both daemon logs and diagnostic
bundle logs for credential-like sentinels.

The timeout tests cover both sides of the liveness contract: a silent peer must terminate at `DialTimeout` even when the caller allows longer, and an already-entered provider held past an earlier caller deadline must not keep the client blocked. The held provider is explicitly released so the synchronous server handler can drain; a green client-deadline assertion alone would not prove server shutdown remains finite.

## References

- [`sessions-package.md`](sessions-package.md) — the package providing `*Session`, `*Pool`, `SessionID`.
- [ADR 003](../decisions/003-session-addressable-runtime.md) — why the resolver seam exists.
- Spec: [`docs/specs/architecture/29-wire-sessions-pool-consumers.md`](../../specs/architecture/29-wire-sessions-pool-consumers.md).
- Pairing mint spec: [`docs/specs/architecture/2388-local-pairing-code-mint.md`](../../specs/architecture/2388-local-pairing-code-mint.md).
- Relay-backed pairing provider spec: [`docs/specs/architecture/2389-bind-local-pairing-mint-to-relay-state.md`](../../specs/architecture/2389-bind-local-pairing-mint-to-relay-state.md).


## Sections

This overview is split across the documents below. Each is kept small so
search can reach it.

- [Sessions: list seam (1.1b-B1)](control-plane-sessions-list-seam-1-1b-b1.md) — The fourth `sessions.*` verb is `sessions.list` — the first read-side member of the namespace. 
- [Sessions: has-id seam (1.3c-1)](control-plane-sessions-has-id-seam-1-3c-1.md) — The fifth `sessions.*` verb is `sessions.has-id` — a one-bit existence query. 
- [Rekey: V2 conn re-key trigger seam (1.3d-1, #459 + #462)](control-plane-rekey-v2-conn-re-key-trigger-seam-1-3d-1.md) — `VerbRekey` lets a local operator client trigger an immediate Noise re-key on a named v2 conn through the control socket. 
- [Approve: mcp.approve verb — forward to permbridge, block, default-deny (#1104)](control-plane-approve-mcp-approve-verb-forward-to-permbridge.md) — `VerbMCPApprove` ("mcp.approve", dotted like `sessions.*`) forwards a claude tool-approval request from the `pyry mcp-approve` subcommand…
- [Attachment.file: file a claude-named host file under the calling session's conversation (#2164)](control-plane-attachment-file-confine-and-store-a-claude-named-path.md) — `VerbAttachFile` confines a model-chosen filesystem path to the calling session's conversation workspace before reading it; ships live but inert (#2165 wires a caller).
- [Sessions: CLI Router (1.1a-B2)](control-plane-sessions-cli-router-1-1a-b2.md) — `pyry sessions <verb>` is the operator-facing surface for the `sessions.*` namespace. 
- [Client dial: transient-startup retry (#198 + #199)](control-plane-client-dial-transient-startup-retry.md) — `internal/control/dial.go` houses the dial-side surface for the control client: the `dial()` primitive every client verb routes through,…
- [Fanning `channel.post` out: `assistant_delta` live delivery (#2498)](control-plane-channel-post-live-delivery.md) — A successful `channel.post` fans the same content it appends to a channel's durable log to every interactive-capable client as one `assistant_delta` per chunk.
