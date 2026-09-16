# #2497 — `channel.post`: post a message into a named channel from the local control socket

## Files read

- `cmd/pyry/channel.go` → `channelCreator`, `channelUsage`, `parseChannelNewArgs`, `channelNewVerdict`,
  `runChannel`, `runChannelNew`, `channelUsageExit`, `channelExit` — the verb family this slice extends,
  end to end. Its doc block carries the static-refusal rule the new poster inherits.
- `internal/control/protocol.go` → `VerbChannelNew`, `ChannelPayload`, `ChannelNewResult`, `Request`,
  `Response` — the additive-field convention (`omitempty` outer field per verb) and the `Response.OK`
  success shape a result-free verb uses.
- `internal/control/server.go` → `Server.channelCreator`, `SetChannelCreator`, `handleChannelNew`,
  `handleSessionsRename`, `handle` — the late-bound seam, the guard order (nil dependency before payload),
  the deadline extension, and the dispatch table.
- `internal/control/client.go` → `ChannelNew`, `SessionsRename`, `request` — the two client shapes: one that
  reads a result struct, one that reads `Response.OK`.
- `cmd/pyry/conversation_history.go` → `appendConversationHistory`, `historyAppendFailure` — the durable-log
  seam and the content-free error discriminant. This slice deliberately does **not** route through the first
  (see Design § Why not `appendConversationHistory`) and does reuse the second.
- `cmd/pyry/operator_message_history.go` → `newOperatorMessageHistory` — the nearest producer of a
  `protocol.MessagePayload` history entry; shows the field set and the "stamp at the confirmed write" rule.
- `internal/history/log.go` → `Store.Append`, `MaxPageEntries`, `ErrInvalidID`, `ErrInvalidPayload`,
  `Store.resolveDir` — the append contract, its precondition (the conversation id must be daemon-resolved,
  never client-asserted), and the on-disk layout the e2e reads back.
- `internal/conversations/registry.go` → `Registry.List`, `ListFilter`, `Registry.Get`, `Registry.Create` —
  the pointer-field AND semantics of the filter and the absence of any name-uniqueness rule.
- `internal/conversations/id.go` → `NewID`, `ValidID` — the repo's id-minting idiom, reused for the message id.
- `internal/protocol/messaging.go` → `MessagePayload`; `internal/protocol/codes.go` → `TypeMessage` — the
  existing wire vocabulary this slice writes into the log. Nothing new is minted here.
- `internal/sessions/pool.go` → `Pool.Mint`, `Pool.buildSession`, `Pool.supervise` — confirms a minted
  session is registered and scheduled in `stateEvicted`; nothing spawns `claude` until `Activate`.
- `cmd/pyry/main.go` → `runSupervisor`, `resolveDefaultCwd`, `resolveInstanceDirPath` — the composition root.
  `defaultCwd`, `convReg`, `convRegistryPath`, `conversationHistory` and `logger` are all in scope at the
  `SetChannelCreator` wiring site inside `runSupervisor`.
- `internal/transport/wssclient.go` → `maxFrameBytes` — the 1 MiB paired-client frame bound the content cap
  must sit comfortably under.
- `internal/e2e/channel_new_test.go` → `runVerbIn`, `projectDir`, `waitForConversation`, `convRow` — the
  fake-daemon patterns the new e2e reuses.
- `docs/knowledge/features/control-plane.md` § "Channel: new verb (channel.new, #2155)" — three lessons that
  shape this design: the creator seam is a narrow closure rather than the pool; `resolveSpawnDir`'s
  empty-string arm is fail-open and every non-wire caller must re-guard it; and a verdict formatter should
  discriminate only what the acceptance criteria actually distinguish.

## Context

Since 2026-09-15 the pyry agent on pyrybox has no chat channel plugins, so nothing on the host can reach the
operator unprompted. `pyry channel new` can create a channel conversation, but no verb can put a message
into one. This slice adds the post path: a control-socket op, a CLI verb, the name-resolution rule, and the
durable record a client reads back on its next connect. Live delivery to an already-connected client (#2498)
and carrying the text into claude's next turn (#2499) are explicitly out of scope.

No ADR is warranted. This is the second verb in an established family and introduces no new boundary — the
one design choice with reach beyond the ticket (a result-free control verb answering `Response.OK`) is
already `sessions.rename`'s.

## Design

### Wire (`internal/control/protocol.go`)

```go
VerbChannelPost Verb = "channel.post"

// MaxChannelPostBytes bounds ChannelPostPayload.Text.
const MaxChannelPostBytes = 64 << 10

type ChannelPostPayload struct {
    Name string `json:"name"`
    Text string `json:"text"`
}
```

`Request` gains `ChannelPost *ChannelPostPayload \`json:"channelPost,omitempty"\`` — a new outer field rather
than widening `ChannelPayload`, whose `Cwd` has no meaning for this verb. Additive with `omitempty`, so every
existing verb's wire bytes stay byte-identical (the property `TestProtocol_SessionsRoundTripBackCompat` pins).

Neither payload field carries `omitempty`: an empty name and empty text are invalid input, not defaults —
`ChannelPayload.Cwd`'s reasoning, and the rule `handleChannelPost` enforces.

**No `Cwd` on the wire.** AC 2 fixes the create-path workspace to the daemon's own `resolveDefaultCwd` value,
so the caller supplies no path at all and this verb has none of `channel.new`'s path-confinement surface.
That is the single largest security difference between the two verbs and is deliberate.

**The cap is 64 KiB** — comfortably under `maxFrameBytes` (1 MiB), so a served history page can carry this
entry beside a dozen others and still fit the frame of the paired client this verb exists to reach. It is one
exported constant with two readers (see Testing strategy); nothing derives a second bound from it.

**Success answers `Response{OK: true}`**, reusing the existing field — `handleSessionsRename`'s shape. No
`ChannelPostResult` type is minted: the caller prints nothing and needs nothing back.

### Server (`internal/control/server.go`)

- `Server.channelPoster func(name, text string) error`, installed by `SetChannelPoster`, mirroring
  `SetChannelCreator` — late-bound so `NewServer`'s signature stays frozen across its call-site fan-out.
  Its doc block carries two obligations on an implementation: **every refusal reason must be static or
  daemon-derived** (the text reaches the wire verbatim), and **the name must never be echoed back** — it is
  caller-authored text.
- `handleChannelPost(conn, enc, payload)`, guard order copied from `handleChannelNew`: nil dependency first
  (so the refusal shape cannot be used to probe which dependencies a daemon has installed), then payload
  shape. Branches, each encoding exactly one `Response` and returning:
  1. nil poster → `"channel.post: no channel poster configured"`
  2. nil payload or empty `Name` → `"channel.post: missing name"`
  3. empty `Text` → `"channel.post: empty message"`
  4. `len(Text) > MaxChannelPostBytes` → `"channel.post: message too large"` (the refusal reads the constant;
     it does not restate the number, so the bound has one home)
  5. poster error → `"channel.post: %v"` of the poster's own static text
  6. otherwise → `Response{OK: true}`
- Deadline extended to `sessionOpTimeout + sessionOpConnGrace` before the call, exactly as `handleChannelNew`
  does: the miss path mints a session and writes two registries.
- Dispatch arm `case VerbChannelPost: s.handleChannelPost(conn, enc, req.ChannelPost)`.

### Client (`internal/control/client.go`)

`ChannelPost(ctx context.Context, socketPath, name, text string) error` — `SessionsRename`'s shape: forward a
transport error, turn a non-empty `Response.Error` into an error, and refuse a reply missing the OK flag with
a fixed `"control: channel.post response missing ok flag"`.

### Poster (`cmd/pyry/channel.go`)

```go
func channelPoster(
    reg *conversations.Registry,
    create func(cwd, name string) (string, error),
    defaultCwd string,
    appendEntry func(conversations.ConversationID, string, json.RawMessage, time.Time) (uint64, error),
    log *slog.Logger,
) func(name, text string) error
```

Behaviour, in order:

1. **Resolve.** `reg.List(conversations.ListFilter{IsPromoted: &yes, IsArchived: &no})` — both pointer fields
   set, because they AND and a nil field filters nothing, so setting only the first would silently include
   archived channels. Collect rows whose `Name` is non-nil and exactly equal to the requested name.
2. **Two or more** → refuse with the count. This is the one refusal whose text is not a bare constant: the
   format string is static and the only interpolated value is an `int` derived from the daemon's own
   registry. The requested name is **not** in the message.
3. **Zero** → `create(defaultCwd, name)` — `channelCreator` unchanged, not a second create path. Its refusals
   are already static constants and are forwarded verbatim.
4. **Exactly one** → that row's id.
5. **Record.** Marshal `protocol.MessagePayload{ConversationID: id, MessageID: <fresh conversations.NewID>,
   Role: "assistant", Text: text}` and `appendEntry(id, protocol.TypeMessage, payload, time.Now().UTC())`.
   The timestamp is stamped at the confirmed write, `newOperatorMessageHistory`'s rule, so a served page
   orders this entry by when it landed.
6. **Log** `channel_post.posted` with `conversation_id` and a `created` bool — never the text, never the name.
   Refusals log a content-free reason on the same terms.

`create` is a parameter rather than a captured `*conversations.Registry` + pool, so the poster unit-tests
without standing up a pool — the narrowing `channelCreator` already does for its own `mint`.

**Why not `appendConversationHistory`.** That seam returns nothing by contract: a failed append must never
suppress the caller's wire emit, because for its two stream producers the frame has already gone out. Here
there is no wire emit — the durable record *is* the deliverable — so a failed append must be a failed post,
or a cron exits 0 having delivered nothing. The poster therefore takes `Store.Append`'s signature narrowed to
a func and maps its error to a static refusal, reusing `historyAppendFailure` for the content-free log
discriminant (`internal/history`'s messages format absolute filesystem paths and must not reach the wire).

**No activation.** Nothing in this path calls `Activate` and nothing enqueues a user turn. On the miss path
`channelCreator` → `Pool.Mint` registers and schedules a session in `stateEvicted`; `claude` is spawned by
`Activate`, which is not reached. That is `channel.new`'s existing contract, unchanged.

### CLI (`cmd/pyry/channel.go`)

- `parseChannelPostArgs(args) (name, text, file string, err error)` — pure flag/arity checks only, every
  failure in the exit-2 class: flag-parse error, stray positional, missing `--name`, neither `--text` nor
  `--file`, both.
- `channelPostContent(text, file string) (string, error)` — the exit-1 class: read `--file` under an
  `io.LimitReader` of `MaxChannelPostBytes+1` and refuse anything longer; refuse an over-cap `--text` the
  same way. Split from the parser so the exit-code boundary is a function boundary rather than a condition.
  **It must open the file once and bound the read — never `os.Stat` for the size and then open.** The
  check-then-use shape reports a size for one inode and reads another; the `LimitReader` bounds the bytes
  actually read, which is the only value that matters, and it is also what makes `--file /dev/zero` a
  refusal rather than a hang.
- `runChannelPost(socketPath, args)` — parse, resolve content, `control.ChannelPost`, print **nothing** on
  success.
- `runChannel`'s switch gains `case "post"`.
- `channelNewVerdict` → `channelVerdict(sub string, err error)` and `channelExit` → `channelExit(sub string,
  err error)`, so both verbs share one formatter instead of a copied twin. Two call sites (one production,
  one test); the exit-code split (2 for usage, 1 for everything else) is unchanged.
- `channelUsage` gains a second line for `post`.

**Why the cap is checked on both sides.** The daemon's check is the contract — the socket is local and 0600,
so the peer is the operator, but any process running as the operator can dial it and the daemon cannot assume
the CLI is what called. The CLI's check is a *resource* bound on its own read: without it `--file /dev/zero`
never returns. Same constant, two different jobs, and the second is not a duplicate of the first. The
empty-content check has no such second job and therefore lives daemon-side only.

### Composition root (`cmd/pyry/main.go`)

The `channelCreator(...)` value currently constructed inline in the `SetChannelCreator` call is hoisted to a
local, then passed to both setters:

```go
createChannel := channelCreator(convReg, mint, convRegistryPath, announceConversation, logger)
ctrl.SetChannelCreator(createChannel)
ctrl.SetChannelPoster(channelPoster(convReg, createChannel, defaultCwd, conversationHistory.Append, logger))
```

One creator, one confinement order, one `channel_new.created` log line for both entry points.

## Concurrency model

No goroutines are added and none are needed. The verb runs entirely on the per-conn goroutine `Serve`
spawned, which already owns the request/response lifecycle and exits when `handle` returns.

Locks: `Server.mu` is taken to read `channelPoster` and **released before the call** — `handleAttachFile`'s
and `handleChannelNew`'s leaf-lock discipline, load-bearing here because the miss path mints a session and
writes two registries, so holding it across the call would serialise every control verb behind one post.
Below that, `Registry.List`, `Registry.Create`, `Registry.Save` and `Store.Append` each take their own lock
internally; the poster holds none of its own and takes them strictly in call order (registry read →
optionally the creator's registry write → history write), so it introduces no new lock-ordering edge.

`List` → `create` is two acquisitions and not atomic. Two concurrent posts naming the same absent channel can
each see zero matches and each create a row, leaving two same-named channels and a subsequent post refusing
as ambiguous. Accepted rather than locked against: the registry enforces no name-uniqueness rule by design
(`channelCreator`'s doc block says so, and the wire `create_conversation` has the same property), the caller
is a cron running one post at a time, and the failure is loud and operator-fixable rather than silent.

## Error handling

| Failure | Where refused | Wire/stderr text | Exit |
|---|---|---|---|
| bad flag, stray positional, missing `--name`, neither/both of `--text`/`--file` | `parseChannelPostArgs` | detail + usage banner | 2 |
| `--file` unreadable | `channelPostContent` | one line | 1 |
| content over `MaxChannelPostBytes` | `channelPostContent` **and** `handleChannelPost` | one line | 1 |
| daemon not running / transport failure | `control.ChannelPost` | one line | 1 |
| no poster installed (v1/foreground) | `handleChannelPost` | `channel.post: no channel poster configured` | 1 |
| empty name / empty text | `handleChannelPost` | `channel.post: missing name` / `: empty message` | 1 |
| two or more channels share the name | `channelPoster` | `N channels share that name; rename all but one` | 1 |
| create failed on the miss path | `channelCreator`, forwarded | its three existing static constants | 1 |
| history append failed | `channelPoster` | `could not record the message` | 1 |

Nothing that reaches stderr carries the requested name, the message text, a filesystem path or `$HOME`.
The daemon's log carries the wrapped detail, which is where an operator can already read their own paths.

## Testing strategy

RED first in every case: the tests below are written and watched to fail before any production line.

- `internal/control/protocol_test.go` — `ChannelPostPayload` round-trip; existing-verb wire bytes unchanged
  with the new `Request` field present and absent.
- `internal/control/server_test.go` — a table over `handleChannelPost`'s six branches, each asserting exactly
  one response and the exact text; `SetChannelPoster(nil)` restoring the unconfigured refusal; a poster error
  reaching `Response.Error` with the verb prefix and nothing appended.
- `internal/control/client_test.go` — `ChannelPost` against a scripted server: OK, `Error` set, and a reply
  with neither.
- `cmd/pyry/channel_test.go` —
  - `parseChannelPostArgs`: table over accept/reject including neither-flag and both-flag.
  - `channelPostContent`: `--text`; `--file` round-trip; missing file; over-cap via each source; a file of
    exactly `MaxChannelPostBytes` accepted (the boundary, not just past it).
  - `channelPoster`: exact match posts into the existing row; archived and non-promoted same-name rows are
    **not** matched (the filter's AND, the mutation a nil `IsArchived` would pass); zero matches call `create`
    with `defaultCwd` and post into the returned id; two matches refuse naming the count and call neither
    `create` nor the append; a `create` error is forwarded verbatim; an append error becomes the static
    refusal; and on success the appended bytes decode to a `MessagePayload` with role `"assistant"`, the
    resolved conversation id and the exact text, under type `protocol.TypeMessage`.
  - `channelVerdict`: the 0/1 split, unchanged from `channelNewVerdict`'s existing table.
- `internal/e2e/channel_post_test.go` (fake-daemon tier, so `make check` covers it) — `pyry channel new
  --name <label>` then `pyry channel post --name <label> --text …`: exit 0, **empty stdout and empty
  stderr**, and the entry read back off `~/.pyry/test/conversations/<id>/history/*` as one assistant-role
  `message`. A second case posts to a name no channel has and asserts a promoted row appears under the
  daemon's default workspace.

Verification gate (§ B2): `go test -race` on `./internal/control/... ./cmd/pyry/... ./internal/e2e/...`,
`go vet ./...`, `go build ./cmd/pyry`. The full-module race suite is the verifier's.

## Open questions

1. Does the existing `cmd/pyry/channel_test.go` assert `channelUsage`'s exact text? If so, adding the `post`
   line updates that assertion — resolve by reading it before editing the constant.
2. `resolveDefaultCwd` can return `""` when `os.Getwd` fails; `channelCreator` then refuses an empty cwd with
   `msgChannelCwdRejected` ("working directory not allowed"), which is accurate but reads oddly for this
   verb. Confirm in Phase B that the refusal is still safe and one line, and leave it — minting a
   post-specific message for a path that is the daemon's own, not the caller's, would add a constant for an
   unobserved failure.

## Scope note — the one-ticket boundary

Re-counted against this written plan: 5 production source files
(`internal/control/protocol.go`, `internal/control/server.go`, `internal/control/client.go`,
`cmd/pyry/channel.go`, `cmd/pyry/main.go`), 1 new exported type (`ChannelPostPayload`), 2 call sites updated
simultaneously (the `channelNewVerdict` rename), 5 acceptance criteria, and at most 6 reject branches in any
one component. Five of the six boundary lines hold.

Total written work is forecast at roughly 1200–1500 lines including tests and the e2e, which exceeds the
800-line ceiling. This is the refiner's documented, deliberate overage: the control verb's only consumer is
the CLI verb in the same family, so the one-consumer floor merges them rather than shipping a wire contract
nothing calls, and the floor wins when the two disagree. The nearest analogue, #2155, shipped exactly this
pair across exactly these five files for ~1540 lines of code, tests and e2e (`341f6189`). This plan does not
measure materially above it: it adds no path handling to the wire at all, where #2155's confinement work was
its largest single piece.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The boundary is two named functions and nothing else:
  `handleChannelPost` validates the request's *shape* (non-nil payload, non-empty name, non-empty text,
  length against `MaxChannelPostBytes`) and `channelPoster` resolves the name to a conversation id. Past
  that point every value is daemon-derived. That matters most for `Store.Append`, whose doc block states
  the precondition that carries its whole authorisation property — the conversation id must be one the
  daemon resolved, never one a caller asserted, because `conversations.ValidID` is a shape predicate and a
  canonical-shaped id supplied by a caller would genuinely resolve inside the conversation it names. Here
  the id comes from a `Registry.List` match or from `channelCreator`'s freshly minted UUID, and the wire
  carries no id field at all. The caller's `Name` reaches `Conversation.Name` on the miss path and is pushed
  to paired devices inside `channelCreator`'s `announce` — unchanged from #2156, where operator-authored
  display text on that wire is the intended behaviour, not a leak.
- **[Trust boundaries]** No findings on relay reachability (AC 5). Verified structurally rather than
  assumed: `internal/relay` touches `internal/control` in exactly two places — `ErrConnNotFound`'s wrapped
  sentinel and the `var _ control.Rekeyer` assertion on `V2SessionManager` — and constructs a
  `control.Request` nowhere. No inbound frame can become one, and this slice mints no `protocol` type, so a
  relay client has no frame with which to send this verb.
- **[Tokens, secrets, credentials]** No findings. No credential is handled. The message id is minted by
  `conversations.NewID` (`crypto/rand`), the repo's idiom, and is not security-relevant. The posted text can
  itself be sensitive operator content, so its storage matters: `Store.resolveDir` creates the log directory
  at `0700` and `writeSegment` opens segments at `0600`, both checked rather than assumed.
- **[File operations]** No findings, and one design constraint recorded above rather than deferred. The only
  caller-controlled path is `--file`, and it is read **client-side by the CLI**, which runs as the operator
  and reads a file the operator can already read — no privilege boundary is crossed, which is why it needs
  none of `channel.new`'s `$HOME` confinement. The daemon never receives a path. TOCTOU is designed out:
  `channelPostContent` opens once and bounds the read with `io.LimitReader`, never `os.Stat`-then-open (now
  stated explicitly in Design § CLI). Nothing derives a filesystem path from the caller's name — the path
  component in `Store.resolveDir` is the daemon-minted conversation id.
- **[Subprocess / external command execution]** No findings, and AC 4 was verified rather than trusted.
  Nothing in this path calls `exec`. On the miss path `channelCreator` → `Pool.Mint` → `Pool.buildSession`
  returns a session in `stateEvicted` and `Pool.supervise` schedules `Session.Run`, whose loop parks on that
  state; `claude` is spawned by the `stateActive` arm, which only `Session.Activate` reaches, and nothing
  here calls it. No caller-controlled value would reach an argv even if it did: the mint label is the
  conversation id, which `channelCreator`'s doc block already records as never reaching claude's argv.
- **[Cryptographic primitives]** No findings — none used. Constant-time comparison is not applicable to the
  name match: channel names are display text any paired client can already list, not secrets, so
  `crypto/subtle` would signal a confidentiality property that does not exist.
- **[Network & I/O]** One finding, out of scope. `Server.handle` decodes the request with a bare
  `json.Decoder` and no read limit, so a local process can make the daemon buffer an unbounded body before
  `handleChannelPost` ever sees `len(Text)`. This is **pre-existing and verb-independent** —
  `SessionsPayload.Label`, `SessionsPayload.NewLabel` and `AttachFilePayload.Path` are all unbounded strings
  on the same decoder today — and fixing it means changing `handle` for every verb, which § Scope Discipline
  puts outside this ticket. It is a hardening gap rather than a defect: the socket's `0600` chmod is
  documented in `Server.Listen` as the only authentication boundary, so the peer is the operator, who can
  exhaust memory far more directly. No ticket filed for an unobserved same-privilege DoS; named here so
  whoever does bound the decoder finds the reasoning. Timeout discipline is inherited and correct —
  `handle`'s per-conn handshake deadline bounds a silent client, and `handleChannelPost` extends it to
  `sessionOpTimeout + sessionOpConnGrace` before the call exactly as `handleChannelNew` does, because the
  miss path mints a session and writes two registries.
- **[Network & I/O]** No findings on framing. A text containing newlines cannot break the JSONL segment
  format: the text is carried through `json.Marshal` into `MessagePayload.Text` and `encodeEntry`'s
  `json.Encoder` escapes control characters inside strings, so one entry stays one line by construction
  rather than by a filter.
- **[Error messages, logs, telemetry]** No findings, and the rule is inherited rather than invented. No
  refusal on either side carries the requested name, the message text, a filesystem path or `$HOME`; the
  one non-constant refusal interpolates a single `int` counted off the daemon's own registry. Logs carry
  `conversation_id` (canonical, daemon-minted) and content-free reasons only — the history-append failure is
  reduced through the existing `historyAppendFailure` because `internal/history`'s messages format absolute
  paths. The specific temptation to resist in Phase B is logging the *name* on the ambiguous path
  ("2 channels named X"): `channelCreator` already publishes the line this follows — operator-supplied
  display text "belongs on the wire and nowhere else. Nothing below logs it."
- **[Concurrency]** No findings beyond the non-atomicity recorded in Concurrency model. `Server.mu` is read
  and released before the call, `handleChannelNew`'s leaf-lock discipline; a `SetChannelPoster(nil)` racing
  that window calls a stale non-nil closure, which is the existing shape and benign since production installs
  once and never clears. The `List` → `create` window can duplicate a channel under concurrent posts; the
  outcome is two same-named rows and a loud ambiguity refusal on the next post, never a silent misdelivery,
  and the registry enforces no uniqueness rule by design. No goroutine is spawned, so none can leak.
- **[Threat model alignment]** No new exposure. `docs/protocol-mobile.md` § Security model governs the relay
  wire, which this slice does not touch — but the durable log **is** served to paired devices through
  `TypeRequestHistory` (#2116), so posted content does reach that wire on request. The answer is the existing
  one: a paired device can already page the history of any conversation it can see, and this verb adds an
  entry to a conversation the operator named. It creates no new reader and no new frame.
- **[Threat model alignment]** OUT OF SCOPE — the miss path makes channel creation cheap and unbounded: a
  local process can mint one conversation *and one pool session* per distinct `--name`, where `channel.new`
  at least required a directory under `$HOME`. Same-privilege and pre-existing in kind (a loop around
  `channel.new` does the same), so it crosses no boundary, and a cap would be a product rule the acceptance
  criteria do not ask for. Named for whoever adds a channel-count bound; not filed as a bug, since nothing
  has been observed and the operator is the only actor who can reach the socket.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-16

## Documentation handoff

Pending, owned by the documentation stage — not by this slice's code:

- `docs/knowledge/features/control-plane.md` gains a section for `channel.post` beside its existing
  "Channel: new verb (channel.new, #2155)" section, covering the CLI verb, the socket op, the
  name-resolution rule and the content cap, and naming the daily-question cron on pyrybox as the first
  consumer.
</content>
</invoke>
