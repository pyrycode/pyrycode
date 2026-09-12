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

The pairing seam is also a credential-redaction boundary. An absent provider returns exactly `pairing.mint: provider not configured`; a missing payload or any provider error returns exactly `pairing.mint: operation failed`. On the error branch, `handlePairingMint` discards both the provider's returned string and its error detail, emits no control-layer log, and leaves `Response.Pairing` absent. `MintPairing` likewise returns an empty string for every error, including the fixed `control: empty pairing.mint response` guard for a missing or empty success payload. Only a nil-error, non-empty pairing reaches the caller.

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
// Session is the per-session view the control plane needs.
type Session interface {
    State() supervisor.State
    Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error)
    Activate(ctx context.Context) error  // 1.2c-A
}

// SessionResolver maps a SessionID to a Session and resolves loose-input
// selectors (full UUID / unique prefix / empty) to a canonical SessionID.
type SessionResolver interface {
    Lookup(id sessions.SessionID) (Session, error)
    // ResolveID maps a loose-input selector to a concrete SessionID.
    // Errors flow verbatim — handleAttach wraps them as "attach: <err>".
    ResolveID(arg string) (sessions.SessionID, error)
}
```

`*sessions.Session` satisfies `Session` structurally. `*sessions.Pool` does **not** satisfy `SessionResolver` directly because `Pool.Lookup` returns the concrete `*sessions.Session` rather than the `control.Session` interface — Go does not do covariant return types on interface satisfaction. A small `poolResolver` adapter in `cmd/pyry/main.go` bridges the two; both `Lookup` and `ResolveID` are 1-line passthroughs.

The empty-id-resolves-to-default convention is shared across both methods: `Lookup("")` and `ResolveID("")` both resolve to the bootstrap session (the latter via `Pool.ResolveID`'s empty-arg fast path). `handleAttach` (1.1e-C) takes loose input from `AttachPayload.SessionID` and routes it through `ResolveID` first, then `Lookup` — see [Attach: ResolveID-then-Lookup](#attach-resolveid-then-lookup-11e-c) below. Other verbs that don't yet take a selector (`status`, `logs`, `stop`) continue to call `Lookup("")` directly.

### Why a single resolver instead of `StateProvider` + `AttachProvider`

Phase 0 wired the supervisor into control via two narrow interfaces (`StateProvider` for `VerbStatus`, `AttachProvider` for `VerbAttach`). Two providers worked when there was exactly one supervisor; once a session has identity, every verb needs the same lookup step before it does its work. Collapsing to one resolver removes the dual-provider plumbing and gives each handler the same shape:

```go
sess, err := s.sessions.Lookup("")
if err != nil { /* encode error */; return }
// use sess.State() or sess.Attach(...)
```

`VerbLogs` and `VerbStop` are intentionally process-global today (logs come from the ring buffer; stop calls the supervisor-context cancel). They do **not** call the resolver. Phase 1.1 may revisit `VerbStop` if per-session stop becomes a verb.

## Attach: ResolveID-then-Lookup (1.1e-C)

`handleAttach` resolves the client's session selector through `Pool.ResolveID` before any bridge work, then re-fetches the session with `Pool.Lookup`. Two sequential calls, two sequential `Pool.mu` RLocks:

```go
id, err := s.sessions.ResolveID(sessionID) // sessionID = req.Attach.SessionID, or "" if Attach is nil
if err != nil {
    _ = enc.Encode(Response{Error: fmt.Sprintf("attach: %v", err)})
    return false
}
sess, err := s.sessions.Lookup(id)
if err != nil {
    _ = enc.Encode(Response{Error: fmt.Sprintf("attach: %v", err)})
    return false
}
// ... unchanged: clear deadline, Activate, Attach, hand off conn.
```

Why two calls instead of one `ResolveSession` API:

- **`Pool.ResolveID` returns `SessionID`, not `*Session` (decision from #66).** Returning `*Session` would tempt callers to skip the second lookup, but the second `Lookup` is the lock-clean way to guard against a session being removed between resolve and use. Window is microseconds (one RLock release + one RLock acquire); race outcome is `ErrSessionNotFound` from `Lookup`, which encodes to the same `"attach: sessions: session not found"` wire string the resolver itself produces. Operator-visible diagnostic is identical either way.
- **Each call takes its own `Pool.mu` RLock — no new locking required.** Concurrent attaches against different sessions remain fully parallel; the dispatcher spawns one goroutine per accept, and `Pool` is the only shared state.

A `nil` `req.Attach` (no payload at all) is treated identically to an empty `SessionID` — both pass `""` into `ResolveID`, which returns the bootstrap id. Phase 0 / v0.5.x clients that omit the payload entirely keep working.

Resolution errors encode as `"attach: <err>"` verbatim through `fmt.Sprintf("%v", err)`:

| Failure | Wire `Response.Error` |
|---|---|
| `ErrSessionNotFound` (no match, or resolve-then-lookup race) | `"attach: sessions: session not found"` |
| `ErrAmbiguousSessionID` (≥2 matches) | `"attach: sessions: ambiguous session id:\n<uuid> (<label>)\n<uuid> (<label>)"` |

The bridge state is **untouched** on the error path — `ResolveID` and `Lookup` both return before `conn.SetDeadline(time.Time{})`, before `Activate`, before `Attach`. Tests assert this via the fake session's attach-call counter, not just by string-matching the response. The `errors.Is(err, sessions.ErrAmbiguousSessionID)` discriminator continues to work server-side; only the message reaches the wire.

The 1.1e-C slice was wire + server only. The CLI surface (`pyry attach <id>` positional) is wired in 1.1e-D — see [§ Attach: CLI Surface](#attach-cli-surface-11e-d) below.

## Attach: Foreground-mode Wire String

A foreground-mode pyry has no `*supervisor.Bridge` — its supervised child is bound directly to the local terminal. Calling `pyry attach` against such a daemon must return Phase 0's exact error string:

```
attach: no attach provider configured (daemon may be in foreground mode)
```

Under the hood this is now `sessions.ErrAttachUnavailable` flowing out of `Session.Attach`. `handleAttach` maps it explicitly:

```go
if errors.Is(err, sessions.ErrAttachUnavailable) {
    _ = enc.Encode(Response{Error: "attach: no attach provider configured (daemon may be in foreground mode)"})
    return false
}
```

A bare `fmt.Sprintf("attach: %v", err)` would surface `attach: sessions: attach unavailable (no bridge)` — observable client drift. The mapping is **load-bearing** for byte-identical output.

`supervisor.ErrBridgeBusy` (second client tries to attach while another is connected) flows through the unchanged `fmt.Sprintf` path, preserving Phase 0's wire surface for that case.

## Attach: Handshake Geometry (#136)

Between `Activate` and `Attach`, `handleAttach` applies the client's terminal
size to the supervised PTY through a typed seam:

```go
if payload != nil && payload.Cols > 0 && payload.Rows > 0 {
    rows := clampUint16(payload.Rows)
    cols := clampUint16(payload.Cols)
    if err := sess.Resize(rows, cols); err != nil &&
        !errors.Is(err, sessions.ErrAttachUnavailable) {
        s.log.Warn("control: attach geometry resize failed", ...)
    }
}
```

Three boundary rules:

- **Zero is the "don't touch" sentinel.** Either `Cols` or `Rows` being zero
  (or `payload` being nil) issues no resize. Matches the `omitempty` tags on
  `AttachPayload`.
- **`int → uint16` clamps silently.** `clampUint16` returns `math.MaxUint16`
  for out-of-range positives. A real terminal will never report dimensions
  that large; a client that does is buggy or hostile. No log on clamp.
- **Argument order swap at the boundary.** The wire is cols-then-rows
  (`AttachPayload`); `Session.Resize` / `Bridge.Resize` are rows-then-cols
  (matching `pty.Winsize`). `handleAttach` is the only site that deals with
  both orders.

The `Session` interface gains `Resize(rows, cols uint16) error`:

```go
type Session interface {
    State() supervisor.State
    Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error)
    Activate(ctx context.Context) error
    Resize(rows, cols uint16) error // #136
}
```

`*sessions.Session.Resize` is a one-line delegator to `Bridge.Resize` (or
returns `ErrAttachUnavailable` in foreground mode — swallowed by
`handleAttach` since foreground mode has its own SIGWINCH watcher in
`winsize.go`). No lifecycle locking; does not touch `lcMu`, does not bump
`lastActiveAt`, does not interact with the active↔evicted state machine.

`Bridge.Resize` is the supervisor-side seam — see [ADR 008](../decisions/008-bridge-resize-seam.md)
for why it lives on `*Bridge` and not on `*Supervisor`. The bridge holds a
leaf-only `ptyMu` mutex over the per-iteration `*os.File`; `runOnce` calls
`SetPTY(ptmx)` after `pty.Start` and `SetPTY(nil)` **before** `EndIteration`
so an in-flight `Resize` that races iteration teardown sees nil rather than
a closed fd.

**Resize errors never fail the attach.** A `pty.Setsize` error (e.g. `EBADF`
on a closed fd in the narrow race window) is logged at Warn and the attach
proceeds. Geometry is best-effort; a wrong window size is recoverable on the
user's next keystroke.

The handshake-geometry block is the first consumer of the seam; the
live-resize wire message + server applier are #137 (see [§ Resize: Live
Wire Message and Applier](#resize-live-wire-message-and-applier-137) below).
The client-side SIGWINCH handler that emits the live message is #133.

## Attach: Activate-before-bind (1.2c-A)

`handleAttach` calls `Session.Activate(ctx)` before `Session.Attach(conn, conn)` so an evicted session is woken before the bridge is bound:

```go
activateCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
if err := sess.Activate(activateCtx); err != nil {
    _ = enc.Encode(Response{Error: fmt.Sprintf("attach: activate: %v", err)})
    return false
}
done, err := sess.Attach(conn, conn)
```

The 30s window caps the documented 2-15s respawn latency with safety margin. A busted respawn surfaces as a clean `attach: activate: <err>` rather than a hung attach. `bridge.Attach` on an evicted session would block on the pipe forever (no claude to drain it) — the Activate-first contract is load-bearing.

`handleStatus` does **not** activate. Status on an evicted session reports the supervisor's `PhaseStopped` (faithful — the supervisor really isn't running) and avoids spurious wakeups from a poll. See [idle-eviction.md](idle-eviction.md).

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

`*sessions.Pool` satisfies `Remover` directly via `Pool.Remove` (#94/#95). Aggregating per-verb sub-interfaces into `Sessioner` (rather than threading a new constructor parameter per seam) keeps the 27-call-site `NewServer` signature unchanged — a deliberate response to the #75 cascade. Phase 1.1b/c/e (`list`, `rename`, `attach` orchestration) will continue this pattern.

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

## Fanning `channel.new` out: `conversation_updated` as an unsolicited push (#2156)

A successful create now fans one `conversation_updated` frame to every interactive-capable client, so a channel created from the host's shell appears in an open desktop client without a reconnect. This is `conversation_updated`'s first unsolicited producer — the wire family's four other producers (`promote_conversation`, `rename_conversation`, `archive_conversation`, `change_workspace`) all reply to the requester only, because a host-side create has no requester to reply to. `conversationUpdateEmitterV2` copies `attachmentOfferEmitterV2`'s shape (see [§ Announcing the store](control-plane-attachment-file-confine-and-store-a-claude-named-path.md#announcing-the-store-attachment_offered-and-the-name-it-must-not-derive-from-2166)): a bare func returned from `startRelay`, nil when the relay leg is off, called last on the success path, and never able to turn a successful create into a refusal.

**On a broadcast-widening change, audit the field set against what the recipient can already pull, not only who receives the frame.** The push turns a unicast reply into a broadcast, so the instinct is to check the audience. The sharper check is the payload: `ConversationUpdatedPayload`'s six fields are a strict subset of `ConversationSummary`'s seven, and every conn that can receive this push can already call `list_conversations` and read the same row — the push delivers sooner a value it could not otherwise be denied, so the audience question turns out to be free. The real question is the payload's *source*. Announcing the create request's raw `cwd` would put an unconfined, CLI-authored string on the wire; only building the payload from a `reg.Get` read-back — the stored, symlink-resolved path — keeps it the confined value. An AC that reads like an accuracy requirement ("read back from the registry rather than assembled from the request") can be the trust boundary itself; check what a "just build it from what's already in scope" shortcut would actually put on the wire before taking it.

No log line on the announce path carries `Cwd` or `Name` — both are host filesystem strings, one a workspace path and the other its derived label. Test this per branch (success, dropped-push, ctx-cancelled, unexpected-error), not once on the happy path, and pick fixture values distinctive enough that the assertion can't pass by accident — a path or name built from a common word like "project" matches too much of the daemon's own log vocabulary to prove the field was actually excluded.

See `docs/specs/architecture/2156-conversation-updated-host-create-fanout.md` for the full design and security review, and [protocol-package-drift-detectors.md § the `excludedTypes` classification key](protocol-package-drift-detectors.md) for how `relay_guard_test.go` records a type with two producers of different shapes.

## Process-Global vs Per-Session

| Concern | Scope today | Source |
|---|---|---|
| `status` payload | per-session (one supervisor) | `sess.State()` |
| `attach` stream | per-session (one bridge) | `sess.Attach(...)` |
| `resize` (live) | per-session (one bridge) | `sess.Resize(...)` (#137) |
| `logs` ring buffer | process-global | `LogProvider`, written by all loggers |
| `stop` shutdown | process-global | `shutdown` cancel func |

Phase 1.1's `pyry sessions new` (#76) and the upcoming `pyry sessions list` / `pyry attach <id>` extend the per-session column. Logs and stop stay process-global until a concrete need pushes them otherwise.

## Lifecycle

Two top-level goroutines, unchanged from Phase 0:

1. **Main goroutine** — calls `pool.Run(ctx)`, blocks until ctx cancellation.
2. **Control goroutine** — `go ctrl.Serve(ctx)`, accepts client connections, dispatches verbs.

Shutdown: `SIGINT`/`SIGTERM` → `signal.NotifyContext` cancels the context → `pool.Run` returns `context.Canceled` → `ctrl.Close()` removes the socket file → in-flight handlers drain via `streamingWG`.

Draining `streamingWG` requires every attached bridge's input pump to actually exit, which (pre-#863) never happened for an idle attached client — the pump only exited on a conn read error, and shutdown never produced one, so `Serve` hung until the service manager escalated to SIGKILL. #863 closes that gap with a three-layer abort, one per resource each layer owns:

- `control.Server` tracks every streaming (attach) conn in a set guarded by `s.mu`; `Close` closes each **after** releasing the lock, erroring a read-parked pump's `in.Read`.
- `supervisor.Bridge` gets a terminal `Shutdown()` that closes a `shutdownCh` the pump's `b.in <- chunk` send now selects on, releasing a pump parked on the buffered channel send (conn close alone can't unblock a channel send). Distinct from the per-iteration `iterCancel` — a routine restart never trips it.
- `sessions.Session.Run` defers `Bridge.Shutdown()`, firing exactly once on permanent termination (ctx cancel or removal), never on eviction.

See [`docs/knowledge/codebase/863.md`](codebase/863.md) for the full design and lock-order rationale.

## Testing

`server_test.go`, `attach_test.go`, `attach_resolve_test.go`, `logs_test.go` exercise the full surface with `fakeResolver` + `fakeSession` test doubles satisfying `SessionResolver` + `Session`. `recordingResolver` records both `Lookup` and `ResolveID` arguments — pinning the resolve-then-lookup ordering visible at review time.

`attach_resolve_test.go` covers the 1.1e-C surface: byte-identical wire output for empty-`SessionID` payloads against a v0.5.x baseline, full-UUID resolution, unique-prefix resolution, ambiguous-prefix error before bridge open, and unknown-id error before bridge open. The "before bridge open" assertion uses the fake session's attach-call counter, not just response-string matching.

Tests that need a real bridge (`TestServer_StopWhileAttached`, `TestServer_BridgeAttach`, `TestServer_ConcurrentAttachRace`) wrap a real `*supervisor.Bridge` in a `fakeSession` whose `attachFn` delegates to `bridge.Attach`.

`pairing_test.go` treats the bearer result as a boundary, not ordinary response data. `TestMintPairing_WireRoundTrip` compares exact raw JSON for both boolean values and the typed pairing reply, while `TestProtocol_SessionsRoundTripBackCompat` proves the new optional outer fields did not change older verb bytes. Provider tests assert exactly one call with both arguments and use distinct success-pairing and provider-error sentinels to prove that only the successful return value can contain the credential; neither sentinel may enter control logs, response errors, transport diagnostics, or any error-path result.

The timeout tests cover both sides of the liveness contract: a silent peer must terminate at `DialTimeout` even when the caller allows longer, and an already-entered provider held past an earlier caller deadline must not keep the client blocked. The held provider is explicitly released so the synchronous server handler can drain; a green client-deadline assertion alone would not prove server shutdown remains finite.

## References

- [`sessions-package.md`](sessions-package.md) — the package providing `*Session`, `*Pool`, `SessionID`.
- [ADR 003](../decisions/003-session-addressable-runtime.md) — why the resolver seam exists.
- Spec: [`docs/specs/architecture/29-wire-sessions-pool-consumers.md`](../../specs/architecture/29-wire-sessions-pool-consumers.md).
- Pairing mint spec: [`docs/specs/architecture/2388-local-pairing-code-mint.md`](../../specs/architecture/2388-local-pairing-code-mint.md).


## Sections

This overview is split across the documents below. Each is kept small so
search can reach it.

- [Attach: CLI Surface (1.1e-D)](control-plane-attach-cli-surface-1-1e-d.md) — The Phase 1.1e end-to-end multi-session attach surface lands in `cmd/pyry/main.go` and `internal/control/attach_client.go`. 
- [Attach: stdio mode (1.3a)](control-plane-attach-stdio-mode-1-3a.md) — `pyry attach --stdio [<id>]` is the no-PTY counterpart of the default attach mode, intended for SDK consumers (Claudian /…
- [Attach: --create-if-missing (1.3b)](control-plane-attach-create-if-missing-1-3b.md) — `pyry attach --create-if-missing <uuid>` lets SDK consumers (Claudian / `@anthropic-ai/claude-agent-sdk`) issue one attach call instead of…
- [Resize: Live Wire Message and Applier (#137)](control-plane-resize-live-wire-message-and-applier.md) — `VerbResize` carries a live window-size update for an already-attached session on a **separate, one-shot control connection** — independent…
- [Resize: Live SIGWINCH Watcher (#133)](control-plane-resize-live-sigwinch-watcher.md) — The client-side producer for `VerbResize`. 
- [Sessions: list seam (1.1b-B1)](control-plane-sessions-list-seam-1-1b-b1.md) — The fourth `sessions.*` verb is `sessions.list` — the first read-side member of the namespace. 
- [Sessions: has-id seam (1.3c-1)](control-plane-sessions-has-id-seam-1-3c-1.md) — The fifth `sessions.*` verb is `sessions.has-id` — a one-bit existence query. 
- [Rekey: V2 conn re-key trigger seam (1.3d-1, #459 + #462)](control-plane-rekey-v2-conn-re-key-trigger-seam-1-3d-1.md) — `VerbRekey` lets a local operator client trigger an immediate Noise re-key on a named v2 conn through the control socket. 
- [Approve: mcp.approve verb — forward to permbridge, block, default-deny (#1104)](control-plane-approve-mcp-approve-verb-forward-to-permbridge.md) — `VerbMCPApprove` ("mcp.approve", dotted like `sessions.*`) forwards a claude tool-approval request from the `pyry mcp-approve` subcommand…
- [Attachment.file: file a claude-named host file under the calling session's conversation (#2164)](control-plane-attachment-file-confine-and-store-a-claude-named-path.md) — `VerbAttachFile` confines a model-chosen filesystem path to the calling session's conversation workspace before reading it; ships live but inert (#2165 wires a caller).
- [Foreground binary auto-attach (1.3c-2)](control-plane-foreground-binary-auto-attach-1-3c-2.md) — When `pyry` is invoked as a foreground binary (no `attach` / `status` / `stop` / `logs` / `sessions` / `install-service` / `version` /…
- [Sessions: CLI Router (1.1a-B2)](control-plane-sessions-cli-router-1-1a-b2.md) — `pyry sessions <verb>` is the operator-facing surface for the `sessions.*` namespace. 
- [Client dial: transient-startup retry (#198 + #199)](control-plane-client-dial-transient-startup-retry.md) — `internal/control/dial.go` houses the dial-side surface for the control client: the `dial()` primitive every client verb routes through,…
