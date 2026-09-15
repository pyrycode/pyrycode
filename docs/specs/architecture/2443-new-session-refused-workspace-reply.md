# 2443 — new_session reports a refused recorded workspace back to the requesting client

## Files read

- `cmd/pyry/main.go` → `activeSessionStarter.resolveSpawnDir` — the sole site that records
  `v2.new_session.spawn_dir_rejected`; AC-3's "exactly one condition" is this method's error arm.
- `cmd/pyry/main.go` → `activeSessionStarter.StartNewSession` — the trust boundary for the
  client-named id, and the one place holding the *resolved* conversation id the reply must carry
  (a bare frame's id is the cursor's, which the relay never sees).
- `cmd/pyry/main.go` → `startFreshRunner`, `installSpawnDir` — the two spawn-side arms below the
  resolve. `installSpawnDir`'s distinct `spawn_dir_install_failed` record is the failure AC-3
  requires to stay silent; `startFreshRunner`'s only non-nil return is the rotate error, which is
  what lets "rotation failed" and "rotation succeeded with a caveat" be told apart.
- `internal/relay/v2session_seams.go` → `SessionStarter`, `ErrSessionUnknown` — the seam whose
  arity this design preserves, and the precedent for declaring a relay-local error the cmd/pyry
  adapter returns.
- `internal/relay/v2session_modal.go` → `handleNewSession` — the handler whose doc states in three
  places that the frame owes no reply in any outcome; `handleInterrupt` and `handleDequeueMessage`
  are its shape twins and stay unchanged.
- `internal/relay/v2session_settings.go` → `settingsReplyError`, `msgChangeWorkspaceRejected`'s
  sibling message constants — the one-helper-per-handler posture (its doc names over-DRY as the
  reason) and the static-constant-message rule.
- `internal/relay/v2session.go` → `dispatchAppFrame`'s `TypeNewSession` arm — where `ctx` is
  already in scope and the neighbouring arms thread it.
- `internal/relay/handlers/change_workspace.go` → `ChangeWorkspace`, `ErrWorkspaceRejected`,
  `msgChangeWorkspaceRejected` — the security pattern this change copies: log conn/conversation
  only, never the confine error, reply with a static constant and `retryable: false`.
- `internal/protocol/handshake.go` → `ErrorPayload` — carries no conversation id today.
- `internal/protocol/codes.go` → `CodeMCPActuationRefused`, `CodeWorkspaceNotFound` — the
  mint-with-the-handler-that-sends-it convention and the `domain.thing` code spelling.
- `cmd/pyry/rotation_spawndir_test.go` → `spawnDirProbe`, `movableRunner`, `starterWithWorkspace`,
  `TestStartNewSession_RefusedWorkspace_RotatesAndRecords` and its two siblings — #1475's standing
  proof of AC-2 and AC-3's negative space; the fixtures hand over the refusal scenario.
- `internal/relay/v2session_newsession_test.go` → `fakeSessionStarter` — the relay-side double,
  untouched by this design because the seam keeps its arity.
- `internal/relay/v2session_settings_test.go` → `TestV2Session_SetSessionSettings_ErrorReplies` —
  the reply-assertion idiom (`openModalConn`, `sealAppFrameConn`, `noiseMsgsForConn`,
  `decryptAppFrame`, `bufferLogger`) this ticket's relay test copies.
- `docs/knowledge/features/sessions-package.md`, `docs/knowledge/features/protocol-package.md` —
  wire-type validation boundaries and rotation semantics.

## Context

#1475 wires a conversation's recorded workspace into its next `new_session` rotation and
re-confines it at the spawn site. A refusal — the folder was deleted, or re-pointed outside `$HOME`
since `change_workspace` stored it — completes the rotation anyway, leaves the successor in the
directory the runner already had, and says so only in the daemon log. An operator on a phone cannot
read a daemon log, so they are left believing a rotation moved a conversation that is still running
in the old directory. The operator's decision on #1475 was to say so on the wire, and carving that
out is what kept #1475 inside one ticket: `new_session` has never had a reply path at all.

**Sizing overage, stated rather than split.** The one-ticket boundary allows five production source
files; this design prescribes six — `internal/protocol/codes.go`, `internal/protocol/handshake.go`,
`internal/relay/v2session_seams.go`, `internal/relay/v2session_modal.go`,
`internal/relay/v2session.go`, `cmd/pyry/main.go`. The refiner's estimate named four and did not
anticipate two of them: `handshake.go` (the reply must name a conversation the client did not, see
"The reply must carry a conversation id" below) and the one-line `ctx` thread at the dispatch site.
Every other line of the table holds with margin — ~700 total written lines against 800, one new
exported type against five, four acceptance criteria against five, one reject branch against ten,
and six call sites against ten. The floor rule is what decides it: **every candidate slice here has
exactly one consumer inside the family.** A protocol-only child (code + payload field) is consumed
by nothing but its sibling, and `codes.go`'s own repeated convention — "MINTED WITH THE HANDLER
THAT SENDS IT … no reject vocabulary exists ahead of the code that can emit it" — forbids that split
outright. A cmd/pyry-only child (carry the refusal across the seam) changes no observable behaviour
on its own and is read by nothing but the relay child. Per the floor-wins rule the merged ticket is
built with the overage recorded here.

**No ADR is warranted.** The decision recorded below is a verb-local reply shape, not a boundary
change: the `SessionStarter` seam's arity, its trust boundary, and the relay's import set are all
unchanged.

## Design

### The decision: a typed error across the existing seam, answered with a `TypeError` reply

The ticket leaves the carrier open — a `TypeError` envelope correlated on `in_reply_to`, or a field
on an existing conversation record. **AC-1 settles it: the frame must be "correlated to the
`new_session` frame it sent".** A conversation record carries no `in_reply_to` and is broadcast to
every conn rather than answering the one that asked, so it cannot satisfy AC-1 without becoming a
second thing. `TypeError` + a code in `internal/protocol/codes.go` is what every other reply-owing
v2 verb uses, and it is what this design picks.

What crosses the `SessionStarter` seam is the second decision, and the cheaper of the two shapes:

- **Chosen — a typed error returned through the existing `error` return.** `internal/relay` gains
  `RotatedWithoutWorkspaceError`, carrying the resolved `ConversationID`; `cmd/pyry` returns it;
  `handleNewSession` discriminates with `errors.As`. The seam keeps its arity, so every
  `SessionStarter` implementation — `fakeSessionStarter` in the relay tests, the fake-daemon
  harness's wiring, every construction site — compiles and behaves unchanged.
- **Rejected — widening the seam to `(refused bool, err error)` or to a result struct.** It cascades
  into every double for no expressive gain, and the ticket's own estimate names that cascade as the
  one choice that moves the number.

The error type is NOT a bare sentinel, and that is forced rather than stylistic: the reply must name
a conversation (below), and a sentinel carries no fields. `errors.As` on a concrete type is the
codebase's existing way to get a value out of an error; parsing an id back out of an error string
would be the alternative and is not one.

**A non-nil error here does not mean the rotation failed, and the type name says so.**
`RotatedWithoutWorkspaceError` is returned only after `startFreshRunner` returned nil — the rotation
committed, the pool re-keyed, the successor respawning. The name exists so a reader who treats every
non-nil error as failure is corrected by the type rather than by a comment. The handler is
`StartNewSession`'s only production caller (`cmd/pyry/relay.go` wires the seam; nothing else reads
its error), so the widened contract has exactly one reader to teach.

### The reply must carry a conversation id

AC-1 requires a bare frame — one naming no conversation, which rotates the daemon's cursor
conversation — to be answered on the same terms as a named one. On that path the client named
nothing, so `in_reply_to` alone does not tell it which conversation moved: only the daemon knows,
because only `StartNewSession` resolves the cursor. AC-4 therefore requires the id in the frame, and
`ErrorPayload` carries no such field today.

`ErrorPayload` gains `ConversationID string \`json:"conversation_id,omitempty"\`` — documented as
set only by replies whose subject cannot be inferred from `in_reply_to`, populated by this verb
alone for now, and carrying a never-echo obligation: the value is daemon-authored and MUST NOT be an
echo of a client-supplied id, the discipline `RunConfigFor` and `ModelListFor` already state for
their own reported ids. `omitempty` keeps every existing error reply byte-identical on the wire, so no
existing decoder, test, or documented shape changes. A per-verb payload type was the alternative and
was rejected: it would need its own envelope type, which is a second wire vocabulary for a single
optional string, and AC-1's correlation is what the `TypeError` shape already provides.

### Key symbols

| Symbol | Where | Contract |
|---|---|---|
| `protocol.CodeNewSessionWorkspaceRefused` | `internal/protocol/codes.go` | `"new_session.workspace_refused"`. Non-retryable: the same stored workspace fails identically until the operator repairs the folder or re-points it with `change_workspace`, which is not something a retry accomplishes — `CodeWorkspaceNotFound`'s reasoning, unchanged. Minted with the handler that sends it. |
| `protocol.ErrorPayload.ConversationID` | `internal/protocol/handshake.go` | Optional (`omitempty`). Daemon-authored; never an echo of a client-supplied string. |
| `relay.RotatedWithoutWorkspaceError` | `internal/relay/v2session_seams.go` | `struct{ ConversationID string }` with an `Error() string` returning a constant. Returned by a `SessionStarter` implementation to mean: the rotation COMPLETED, and the conversation's recorded workspace was refused so the successor stayed where it was. `ConversationID` is the daemon's own resolved id (the named one, or the cursor's) — never the raw client string, never a path. |
| `relay.msgNewSessionWorkspaceRefused` | `internal/relay/v2session_modal.go` | Static constant. Names the refusal and the consequence; names no path. |
| `(*V2SessionManager).newSessionReplyError` | `internal/relay/v2session_modal.go` | This handler's own copy of the reply helper, per the package's one-per-handler posture (`settingsReplyError`'s doc names over-DRY as the reason). Differs from its siblings only in taking the `conversationID` the payload now carries. |
| `activeSessionStarter.resolveSpawnDir` | `cmd/pyry/main.go` | Signature becomes `(convID, recordedCwd string) (dir string, refused bool)`. The `Warn` record is unchanged; `refused` reports the same condition the record reports, so the record and the frame cannot disagree. |

### Data flow

```
new_session frame (interactive conn)
  → handleNewSession(ctx, s, env)            [relay, Run goroutine]
      → SessionStarter.StartNewSession(p.ConversationID)
          → activeSessionStarter.StartNewSession        [cmd/pyry, the trust boundary]
              → resolveSpawnDir(convID, recordedCwd) → ("", refused=true) + Warn record
              → startFreshRunner(...)  → nil            [rotation COMPLETED]
              → &relay.RotatedWithoutWorkspaceError{ConversationID: convID}
      ← errors.As hits
      → newSessionReplyError(ctx, s, env.ID, code, msg, convID, retryable=false)
          → forwardEnvelope  →  TypeError{in_reply_to: env.ID, conversation_id: convID}
```

Every other outcome keeps today's shape exactly: `nil` returns silently, and any other non-nil error
takes the existing `v2.new_session.keystroke_err` Warn arm with no reply.

### Exactly one condition sends the frame

AC-3 names it: the condition on which `resolveSpawnDir` records `spawn_dir_rejected`. This design
adds **one** refinement, deliberate and recorded here because it is the one place the frame and the
record can diverge: the frame requires *refusal AND a completed rotation*, where the record requires
refusal alone. When the refusal is followed by a rotate error, `StartNewSession` returns that error
— the rotation never happened, so a frame saying "rotated without the workspace" would be a lie, and
AC-2 states the rotation completing as a property of this path. Losing a race to a concurrent
`new_session` is the ordinary way to land there (`startFreshRunner`'s doc says so), so it is a real
state rather than a theoretical one. The record still fires; only the frame is withheld.

Silent, unchanged, and each pinned by a standing or new test: an accepted workspace; a conversation
with no recorded workspace (`resolveSpawnDir` returns before touching the validator); every inert
arm (non-interactive conn, nil seam, no active conversation, non-canonical id, no bound session, no
restart capability, named conversation with no live child); and `installSpawnDir`'s distinct later
`spawn_dir_install_failed`, which is swallowed below the seam and never becomes an error at all.

## Concurrency model

No goroutine is added, started, or stopped. `handleNewSession` keeps running on `V2SessionManager`'s
single `Run` dispatch goroutine, and the reply is sealed by `forwardEnvelope` on that same goroutine
— the package's single-owner send-CipherState invariant, undisturbed. The handler gains a `ctx`
parameter to reach `forwardEnvelope`, matching every reply-owing sibling; `ctx` is already in scope
at the `TypeNewSession` arm of `dispatchAppFrame`. The handler's doc sentence stating it takes no
`ctx` because it has "no cancellable work" stops being true and is rewritten.

`StartNewSession` continues to do its filesystem work (the confinement's `MkdirAll` and
`~/.claude.json` write) inline on `Run`, below every inert arm — this change moves nothing onto or
off that goroutine, so the honest consequence `resolveSpawnDir`'s doc already records is unchanged.

## Error handling

- `startFreshRunner` returning non-nil wins over a refusal: a failed rotation is reported as the
  existing best-effort error and the refusal frame is withheld (see "Exactly one condition" above).
- `errors.As` rather than `errors.Is`: the value is needed, and the type is matched unwrapped or
  wrapped either way.
- A marshal failure inside the reply helper is `Warn`-logged and dropped, and a `forwardEnvelope`
  failure is `Debug`-logged and dropped — `settingsReplyError`'s posture, copied. Neither can
  un-rotate anything; the rotation is already committed by the time the helper runs.
- The confinement error itself is never returned, wrapped, or stored. `resolveSpawnDir` already
  discards it into a `Warn` record that names only the event and the conversation, and this change
  keeps that discard: the `bool` it now also returns carries no text at all.

## Testing strategy

RED first on each. Scenarios, not bodies:

**`internal/relay/v2session_newsession_test.go`** (new cases; `fakeSessionStarter` gains an
injectable `RotatedWithoutWorkspaceError`, no signature change):

- A refusal from the seam yields exactly one frame to the requesting conn: `TypeError`, `InReplyTo`
  = the request id, `Code` = `new_session.workspace_refused`, `Message` = the static constant,
  `Retryable` false, `ConversationID` = the id the seam reported.
- The reply reaches the requesting conn and no other: a second interactive conn records zero frames.
- Table of silent outcomes, each asserting zero frames to the conn: `nil` return; a plain non-nil
  error (today's tolerated arm, which must still `Warn`); a non-interactive conn; a nil seam.
- Never-leak: a seam error whose text quotes a distinctive path marker reaches neither the reply
  `Message` nor any log line — `TestV2Session_SetSessionSettings_ErrorReplies`' marker idiom.

**`cmd/pyry/rotation_spawndir_test.go`** (edits the seam's pinned answer, per the ticket's Technical
Notes): `TestStartNewSession_RefusedWorkspace_RotatesAndRecords` now asserts `StartNewSession`
returns a `*relay.RotatedWithoutWorkspaceError` naming the conversation — and keeps every standing
assertion that the rotation completed, nothing installed, and no path reached the record. New
sibling: a refusal on the BARE (cursor) path reports the cursor's resolved id, not the empty string.
New sibling: a refusal followed by a rotate error returns the rotate error, not the refusal.
`TestStartNewSession_EmptyRecordedWorkspace_IsSilent` and `TestStartNewSession_InertArms_NeverConfine`
gain a `nil`-return assertion where they do not already have one.

**`internal/protocol/messaging_test.go`** (beside the existing `ErrorPayload` coverage): a payload
with no `ConversationID` marshals without the key at all, so every existing error reply is
byte-stable.

**`internal/e2e/relay_v2_stream_new_session_test.go`** (fake daemon, no live claude): drive
`change_workspace` to record a workspace, make it unresolvable, send `new_session`, and assert the
client receives the coded error frame correlated to its own frame while the successor still comes up
— AC-1 and AC-2 end to end over a real Noise session.

Gate: `go test -race` on `./internal/relay/... ./internal/protocol/... ./cmd/pyry/... ./internal/e2e/...`,
`go vet ./...`, `go build ./cmd/pyry`.

## Documentation handoff

Owned by the documentation stage; pending, not done here.

- `docs/protocol-mobile.md` § New session (v2): record the new response behaviour alongside the
  other v2 verbs — the frame's type (`error`), its code (`new_session.workspace_refused`), that it
  carries the conversation id and `retryable: false`, when it is and is not sent, and that
  `new_session` remains best-effort and silent in every other outcome. **Two standing sentences in
  that section's body become false and need correcting rather than being left beside the new
  behaviour:** the "all of these are silent" paragraph and the "there is no synchronous ack" one.
  The dated changelog entries are history and stay as they are; this change gets its own.
- `docs/protocol-mobile.md` § Error codes: the new code, with its non-retryable posture.
- `docs/protocol-mobile.md` § Message types: `ErrorPayload`'s optional `conversation_id`, and that
  it is set only where `in_reply_to` cannot identify the subject.

## Open questions

1. Should the relay log the conversation id on the refusal arm? Resolved in the design: **no.**
   `handleNewSession`'s doc already states the relay does not log this verb's conversation id, and
   the seam records it under its own bound at `spawn_dir_rejected`. The relay's record carries
   `conn_id` and the event only.
2. Does any `SessionStarter` implementation outside `cmd/pyry` and the relay test double need
   updating? Expected no — the seam's arity is unchanged — to be confirmed by the build in Phase B.

## Security review

**Verdict:** PASS (after two revisions applied to the design above, noted per finding)

**Findings:**

- **[Trust boundaries] No findings, and the safety is positional rather than checked.** The reply's
  `ConversationID` is daemon-authored on both paths: the bare path takes it from `currentConv`, and
  the named path reaches `resolveSpawnDir` only *below* `StartNewSession`'s `conversations.ValidID`
  arm, so a non-canonical client string returns inert long before any error can be built from it. A
  client therefore cannot get arbitrary bytes echoed back in the payload, and the id that is echoed
  is provably a canonical 36-byte id. This holds by ARM ORDER, not by a check inside the new code —
  hoisting the resolve above the shape check would break it, which is exactly what
  `TestStartNewSession_InertArms_NeverConfine` already pins and this ticket leaves standing.
- **[Error messages, logs, telemetry] REVISED — `RotatedWithoutWorkspaceError.Error()` must return a
  CONSTANT, naming neither the path nor the conversation id.** The plan's first draft left the
  string unspecified. It matters: `handleNewSession`'s default arm logs `err` verbatim
  (`v2.new_session.keystroke_err`), so any id or path interpolated into `Error()` would reach the
  log the moment a future edit reordered the discrimination — the one channel this ticket's AC-4
  exists to close. The id lives in the field and nowhere else; the design table above now says so.
  The confine error itself is discarded inside `resolveSpawnDir` (unchanged) and the new `bool`
  carries no text, so the operator's path has no representation anywhere on this path.
- **[Error messages, logs, telemetry] REVISED — `ErrorPayload.ConversationID` is a SHARED wire type
  and needs a never-echo obligation in its doc.** Nothing in this ticket populates it with untrusted
  bytes, but the next handler to reach for the field could. Its doc comment states that the value is
  daemon-authored and MUST NOT be an echo of a client-supplied id — the discipline `RunConfigFor`
  and `ModelListFor` already state for their own reported ids.
- **[Threat model alignment / no new oracle] No findings — the information gain is zero, and here is
  the derivation.** The frame fires only after a rotation that actually happened, whose
  `session_transition` the requesting client already receives carrying the rotated conversation's id
  (so the bare path's disclosure of the cursor id is not new); the conversation's recorded `cwd` is
  already published in `ConversationSummary`; and `change_workspace` already answers "is this path
  acceptable?" directly, with a rejection reply, for any path a client names. A client could already
  derive this refusal in two frames it is entitled to send. What the new frame adds is one round
  trip of convenience, not a fact. Crucially it does NOT widen the inert arms: the error is
  constructed only after `startFreshRunner` returns nil, which sits below every inert arm, so an
  unknown conversation, an unbound one, a non-canonical id, a conversation with no live child and a
  non-interactive conn all stay silent — the verb still cannot answer "does this conversation
  exist?". AC-3's tests pin each of those directly.
- **[File operations] No findings — the filesystem surface is untouched.** `resolveSpawnDir` keeps
  its confinement, keeps its position below every inert arm (so no new frame shape can drive
  `MkdirAll` on the daemon's behalf), and keeps fail-closed: a refusal still means "keep the old
  directory", never "spawn somewhere unconfined". The reply is emitted AFTER `startFreshRunner`
  returns, so it can influence no spawn decision. No new file is read, created, or stat'd.
- **[Network & I/O] No findings — the reply's size is bounded by construction.** A constant code, a
  constant message, a canonical 36-byte id, one `in_reply_to`. No new read, so no new cap is owed.
  Rate: a hostile paired client could already send `new_session` freely and drive the same
  confinement work per frame; this change adds feedback, not cost, and `retryable: false` tells a
  well-behaved client not to loop. The pre-existing exposure is unchanged and is not widened here.
- **[Cryptographic primitives] No findings — no primitive is touched and no sealing path is added.**
  The reply seals through the same `forwardEnvelope` on the same `Run` goroutine as every other v2
  reply, preserving the single-owner send-CipherState invariant; no nonce is burned in a state where
  `settingsReplyError` would not already burn one.
- **[Tokens, secrets, credentials] No findings — none are in scope.** No token is minted, read,
  compared, or logged; `StaticPriv` is not reachable from any symbol this design touches.
- **[Subprocess / external command execution] No findings.** No `exec.Command`, no argument, no
  environment change. The successor child's spawn directory is decided before the reply exists.
- **[Concurrency] No findings — nothing is added to share.** No goroutine, no lock, no new shared
  state. The handler stays on the manager's single `Run` dispatch goroutine, the seam is called
  synchronously from it, and the error value is constructed per call and never escapes that
  goroutine.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-15
