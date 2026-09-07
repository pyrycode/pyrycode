# #2148 — name the attached client in the session's system prompt

Split from #2093. Ships the half #2093 deferred: the appended system prompt names
the clients attached when the session's prompt is composed, by the `device_name`
and `client_version` each one reported in its `hello`.

## Files read

- `internal/sessions/systemprompt.go` → `systemPromptText`, `composeSystemPrompt`,
  `writeSystemPromptFile`, `conversationPrompt`, `refreshSystemPrompt` — the seam
  `writeSystemPrompt`'s doc names for this ticket, and `conversationPrompt` is the
  totality discipline the new resolver copies.
- `internal/sessions/pool.go` → `Pool` (struct), `Pool.Activate`, `buildSession`,
  `Pool.New` — where the resolver field lands, and the one funnel every first
  spawn and re-activate passes through.
- `internal/sessions/transition.go` → `TransitionObserver`,
  `SetTransitionObserver` — the late-bound-setter precedent, including its
  "must be called before `Pool.Run`, read-only after" contract.
- `internal/relay/v2session.go` → `ActiveConn`, `V2SessionManager.ActiveConns`,
  `handleActiveConns`, `V2Session` (the `interactive` field's block),
  `dispatchAppFrame`, `enqueueAppFrame`, `appFrameWorker` — the snapshot to widen,
  and the proof of which goroutine reaches `Pool.Activate`.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit` — the hello decode
  and the token-OK tail where `s.interactive` is recorded; the only place the two
  strings can be retained.
- `internal/relay/v2session_mint_pairing.go` → `mintLabelIsDisplaySafe` — the
  repo's existing display-safety predicate for a client-supplied `device_name`.
- `internal/protocol/handshake.go` → `HelloClientPayload` — confirms both fields
  are already on the wire; nothing changes there.
- `internal/protocol/pairing.go` → `MaxDeviceNameBytes`, and
  `internal/protocol/workspace.go` → `MaxWorkspaceLabelBytes` — the
  *A LENGTH CEILING IS NOT A SAFETY PROPERTY* posture, and the rule that a bound
  needs a native anchor rather than a borrowed constant.
- `cmd/pyry/relay.go` → `relayWiring`, `startRelayV2` — the wiring point where the
  v2 manager exists; `relayWiring.modelListFor` / `.modelWindows` are the
  established "closure built at the composition root so `relay.go` never imports
  `internal/sessions`" pattern this copies.
- `cmd/pyry/main.go` → the `relayWiring` literal and the `startRelay` → `pool.Run`
  ordering that gives the install its happens-before edge.
- `cmd/pyry/session_transition_v2.go` → `transitionObserverSink`,
  `startSessionTransitionStreamV2` — the shape of a pre-`Run` pool install driven
  from inside `startRelayV2`.
- `internal/sessions/pool_system_prompt_test.go` → `TestSystemPromptText_Pinned`,
  `TestComposeSystemPrompt`, `TestPool_ConversationPrompt_TriState`,
  `TestPool_Activate_ComposesPromptSetAfterMint` — the assertions AC #2 requires
  to pass unchanged, and the table shapes the new tests mirror.
- `docs/knowledge/features/sessions-package-key-types-writesystemprompt-systemprompttext.md`
  — the rotation trap (`refreshSystemPrompt` must target `sess.systemPromptPath`
  verbatim, never re-derive from `sess.id`) and the "no log line ever carries
  prompt bytes" rule, both of which this ticket inherits unchanged.

## Context

`systemPromptText` tells every interactive claude three architecture facts and
deliberately nothing about *who* is attached, because the daemon retains no client
identity: `HelloClientPayload.DeviceName` and `.ClientVersion` are decoded in
`handleNoiseInit` and dropped there, and `ClientVersion` has no production reader
repo-wide. This ticket carries those two strings from the handshake to the one
place that composes the prompt.

The shape is fixed by the ticket and not re-litigated here: **spawn-time**, naming
the set attached at compose time, not per-turn and not restated mid-session. The
appended prompt file is read once when the child spawns, and `refreshSystemPrompt`
deliberately skips an already-active session, so nothing can be told to a running
claude through this file.

**No ADR.** This adds no new decision the package docs do not already carry: the
resolver copies `conversationPrompt`'s totality, the setter copies
`SetTransitionObserver`, and the bound/character-set posture copies
`MaxWorkspaceLabelBytes`. The lessons belong in
`docs/knowledge/features/sessions-package-key-types-writesystemprompt-systemprompttext.md`,
which the documentation phase owns.

## Sizing — one boundary line is exceeded, deliberately

Production source files: **6**, against the table's 5.

| File | What changes |
|---|---|
| `internal/relay/v2session.go` | 2 fields on `V2Session`, 2 on `ActiveConn`, fill in `handleActiveConns` |
| `internal/relay/v2session_handshake.go` | retain the two hello strings in `handleNoiseInit`'s token-OK tail |
| `internal/sessions/systemprompt.go` | the whole composition + admissibility + resolver seam |
| `internal/sessions/pool.go` | `Pool.clientIdentity` field; `Activate` threads its ctx to `refreshSystemPrompt` |
| `cmd/pyry/relay.go` | one `relayWiring` field, one call in `startRelayV2` |
| `cmd/pyry/main.go` | fill that field with a closure over `*sessions.Pool` |

Every other line of the table holds: 2 new exported types (`ClientIdentity`,
`ClientIdentityResolver`); 5 acceptance criteria; ~4 reject branches in the
admissibility gate; and **0 consumer call sites needing simultaneous update** —
`ActiveConn`'s 129 literals are keyed and keep compiling, `composeSystemPrompt`
keeps its signature (see below), and `refreshSystemPrompt` has one caller. Total
written work is budgeted at ~750 lines.

The sixth file is the **floor rule beating the ceiling**. The only seam that would
cut this into two tickets is relay-retention / sessions-composition, and the relay
half's sole consumer is the sessions half: a ticket that adds two fields to
`ActiveConn` which nothing reads cannot be verified on its own. #2148's body
records the same finding and asks explicitly that it not be split. Split depth is
1 (parent #2093, no grandparent), so depth is not what forbids it — the floor is.
Per § A1, the floor wins and the overage is stated rather than resolved.

**§ A2 overlap check: one hit, dismissed as stale.** `origin/feature/449` touches
`internal/relay/v2session.go`. Issue #449 is CLOSED (2026-05-17), the branch has no
PR in any state, its tip is from 2026-05-17, and `main` is 3234 commits ahead of
it. It is a dead branch, not in-flight work, so no `blockedBy` is set. No other
remote feature branch touches any of the six files.

## Design

### The trust boundary, stated once

`DeviceName` and `ClientVersion` are **remote-authored, unvalidated text that ends
up inside a system prompt**. They cross from untrusted to trusted at exactly one
symbol: `admitClient` in `internal/sessions/systemprompt.go`. Every layer before it
carries the bytes verbatim and asserts nothing:

```
hello (wire) → handleNoiseInit → V2Session.clientName/.clientVersion
             → handleActiveConns → ActiveConn.DeviceName/.ClientVersion
             → cmd/pyry closure  → sessions.ClientIdentity
             → admitClient  ← THE DOOR
             → clientSection → composeSystemPromptFor → the prompt file
```

Validating in the relay would put a second opinion in a second place and would
bind the *wire* to a *rendering* decision it does not own — the same argument
`composeSystemPrompt` makes for leaving #2149's `Registry.SetSystemPrompt` the
single validating door for operator bytes. The relay retains; `internal/sessions`
admits.

### `internal/sessions` — the new surface

`ClientIdentity`'s doc states what its fields are: **remote-authored, unvalidated
text**, trusted by nothing until it has passed `admitClient`. The type is the only
signal a downstream reader gets, so it carries the warning rather than leaving it
to the call site.

```go
type ClientIdentity struct{ Name, Version string }

type ClientIdentityResolver func(ctx context.Context) []ClientIdentity

func (p *Pool) SetClientIdentityResolver(r ClientIdentityResolver)

const maxClientNameBytes    = 64
const maxClientVersionBytes = 32
const maxNamedClients       = 4
const clientIdentityTimeout = 250 * time.Millisecond

func admitClient(c ClientIdentity) (ClientIdentity, bool)          // the door
func clientSection(clients []ClientIdentity) string                 // "" ⇒ no section
func composeSystemPromptFor(operator string, clients []ClientIdentity) string
func (p *Pool) attachedClients(ctx context.Context) []ClientIdentity // total
```

**`composeSystemPrompt` keeps its one-argument signature and its body.**
`composeSystemPromptFor` is a sibling that *delegates* to it whenever
`clientSection` returns `""`. That is what makes AC #2's byte-identity structural
rather than a second branch that happens to agree today, and it is why
`TestComposeSystemPrompt` and `TestSystemPromptText_Pinned` pass **literally**
unchanged — no call-site edit, no re-transcription of the pin. `buildSession`'s
composition also stays on the one-argument form: the construction-time write is
overwritten by `refreshSystemPrompt` before any child comes up, so naming clients
there would be dead bytes.

Composition when a section exists:

```
systemPromptText  +  "\n"  +  clientSection  [ +  "\n"  +  operator ]
```

`clientSection` ends in `"\n"` exactly as `systemPromptText` does, so the blank-line
separator convention is unchanged on both joins.

### `admitClient` — refuse, never repair

Each field is judged independently against one predicate: **valid UTF-8, non-blank
after trimming, within its byte bound, and every rune display-safe** —
`mintLabelIsDisplaySafe`'s rule (no C0, no C1/DEL) plus a refusal of `"`.

- An inadmissible **name** drops the whole client. A client that names nothing has
  nothing to contribute, so a version-only identity is dropped too.
- An inadmissible **version** drops only the version; the client is still named.
- Refusal, not truncation and not escaping — `MaxWorkspaceLabelBytes`' posture.
  This is what makes the byte bound merely a cost control rather than a safety
  claim: **a length ceiling is not a safety property**, and the character-set
  refusal is what actually holds the structure.

`"` is refused because the admitted value is rendered *inside* quotes. Refusing the
delimiter is what places the value where no value can close the structure around
it — the Technical Note's requirement, delivered by the gate rather than by an
escaping pass that could be bypassed.

### `clientSection` — whole set or nothing

Admitted identities are **sorted** by `(Name, Version)` and **deduplicated**, then
rendered as one daemon-authored line. Sorting is load-bearing, not cosmetic:
`ActiveConns` returns Go's randomized map order, so an unsorted section would make
the prompt file's bytes flap between refreshes and every multi-client assertion
flaky. Dedup collapses one client holding two conns (a reconnect whose old conn is
not yet reaped) into one entry.

If more than `maxNamedClients` distinct identities are admitted, the section is
**empty** — the whole set or none. A truncated list under a sentence that claims to
name the attached clients would be a false statement in a system prompt, and
falling back to today's text is the fail-closed direction. It also bounds the
section's worst case at `4 × (64 + 32)` bytes of client text, which is charged in
tokens on every turn for the session's life.

The section's own sentence is a transcription and nothing more — it says what each
client reported about itself, and asserts nothing about what any client can render
or do. `systemPromptText`'s constraint applies to it verbatim; the new pin test is
what keeps a future capability claim a visible diff.

### `internal/relay` — retain, do not judge, but do bound

`V2Session` gains `clientName` and `clientVersion`, set once in `handleNoiseInit`'s
token-OK tail beside `s.interactive`, **before** the `V2StateOpen` transition. Same
three properties `interactive` documents: set on the accept path only (so an
unauthenticated peer's strings are never enumerable), read only by
`handleActiveConns` on the same dispatch goroutine (no lock), and preserved across
a re-key because `handleRekeyInit` never touches them.

**Two strings, never the payload.** `helloPayload` also carries `Token`, which is
plaintext credential material the type's own doc marks MUST-NOT-log. The retention
copies the two fields by value; it must not park `helloPayload` (or a pointer to
it) on `V2Session`, which would keep the token alive for the session's lifetime and
one `%+v` away from a log line.

**A resource bound at the retention site.** Unlike `MintPairingPayload.DeviceName`,
which `UnmarshalJSON` refuses over `MaxDeviceNameBytes`, `HelloClientPayload`
bounds neither field at decode — only the v2 application-envelope cap does, at
65519 bytes. Retaining verbatim would let an authenticated client park ~64 KB per
conn and have it **copied into every `ActiveConn` snapshot**, which the fan-out
takes several times per turn across every open conn. So:

```go
const maxRetainedClientNameBytes    = 256
const maxRetainedClientVersionBytes = 64
```

An over-bound value is retained as `""` — dropped, never truncated, because a
truncation invents a value the client did not send. These are deliberately far
looser than `internal/sessions`' display bounds and they are a **different kind of
limit**: this one caps memory and copy cost at the point of retention, where
resource bounds belong; the display bound and the character set stay behind
`admitClient`, the single door. Neither is a safety property on its own.

`ActiveConn` gains `DeviceName` and `ClientVersion` — appended, per the Technical
Note, so its 129 keyed literals keep compiling. Its doc comment's current claim
that it holds "only non-secret routing/decision data" is **amended** to state the
consumer's obligation outright: these two are remote-authored, unvalidated display
strings; a consumer MUST NOT log them, interpolate them into an error, or render
them without its own gate, and MUST NOT format the struct wholesale (`%+v`,
`slog.Any`), which would leak them by accident.

### `cmd/pyry` — the composition root builds the closure

`relayWiring` gains one field:

```go
setClientIdentity func(enum func(context.Context) []relay.ActiveConn)
```

`main.go` fills it with a closure that captures `*sessions.Pool`, maps
`[]relay.ActiveConn` → `[]sessions.ClientIdentity`, and calls
`pool.SetClientIdentityResolver`. `startRelayV2` invokes it as
`w.setClientIdentity(mgr.ActiveConns)` at the point it already installs the
transition observer.

This is `relayWiring.modelListFor`'s pattern exactly: the closure is built at the
composition root **so the `internal/sessions` dependency stays there and `relay.go`
neither imports the package nor holds a pool reference**. It is the reason the
install is not a new narrow sink interface — that shape would need its own file plus
a `main.go` field, one production file more for no gain.

## Concurrency model

**No new goroutine.** The resolve is a synchronous call on the goroutine that is
already activating the session.

**Which goroutine, and why the call cannot deadlock.** `ActiveConns` funnels its
request onto the v2 manager's `Run` goroutine and blocks on the reply, so calling
it *from* `Run` self-deadlocks — `handleActiveConns`' and `modal_resolve_v2`'s docs
both say so. Every production path that reaches `Pool.Activate` is off `Run`:

- **v2 message frames.** `dispatchAppFrame` runs on `Run`, but a v1 application
  frame — which `send_message` is — is handed off by `enqueueAppFrame` to the
  per-conn `appFrameWorker` goroutine. The handler's `TurnWriter.Activate` runs
  there, never on `Run`.
- **Queued delivery.** `newInboundDeliver` runs on msgqueue's per-conversation
  drain goroutine.
- **Control plane.** `sessions new` → `Sessioner.Create` runs on the Unix-socket
  server's goroutine.

No inline-on-`Run` handler reaches `Activate`: `create_conversation` calls
`Pool.Mint`, which does not spawn, and `request_system_prompt` calls
`Pool.SystemPromptFor`, a lookup.

**Lock discipline is unchanged.** `attachedClients` takes **no** pool lock and is
called from `refreshSystemPrompt` between the `p.mu.RLock` that reads `sess.label`
and the `p.mu.Lock` that stores `sess.systemPrompt` — i.e. off the lock, in the
same window the file write already occupies. `capMu → mu → lcMu` is not inverted:
both acquisitions still release before `Pool.Activate` takes `p.capMu`.

**The resolver field.** Installed before `pool.Run`, read-only after —
`SetTransitionObserver`'s contract verbatim, and the same happens-before edge:
`startRelay` returns before `main` calls `pool.Run`, and every goroutine that can
reach `Activate` is created transitively after the install. The setter's doc states
the precondition; calling it after `Run` is a programming error the race detector
flags. See Open Questions for the ordering check this obliges in Phase B.

**Bounded wait.** `attachedClients` wraps the caller's ctx in a
`clientIdentityTimeout` (250 ms). The anchor is the site: the resolve sits
immediately before a claude spawn that costs hundreds of milliseconds, so the bound
is invisible to the operator, while being orders of magnitude above what a live
`Run` loop needs to answer. It is what turns AC #4's "manager not running" case —
where `m.snapshot <- req` has no receiver — from a block into a bounded no-identity
answer.

## Error handling

The resolver is **total**; it has no error return, and nothing on this path can
fail a spawn. `conversationPrompt` is the precedent, and each failure mode collapses
to the same "no identity" answer:

| Failure | Result |
|---|---|
| No relay wired (foreground, v1, most tests) — `p.clientIdentity == nil` | `nil`, no call |
| Manager built but `Run` not started / already exited | bounded wait, then `nil` |
| Caller's ctx already cancelled | `nil`, immediately |
| Resolver returns `nil` or an empty slice | no section |
| Every attached client reports both fields empty | no section (AC #2's byte-identity path) |
| Any single field inadmissible | that field dropped; name-drop drops the client |
| More than `maxNamedClients` admitted | no section |

`refreshSystemPrompt`'s existing write-failure posture is unchanged: logged at
`Warn` and swallowed, because `buildSession` already wrote a complete file and the
write is a rename.

**No log line carries client bytes**, on any path — the package's existing rule for
prompt bytes, extended to these two strings. `admitClient` logs nothing at all; a
refusal is silent by construction, so there is no line for a hostile name to
appear in.

## Testing strategy

Package `internal/sessions` (new file `systemprompt_client_test.go`, table-driven):

- **`TestComposeSystemPromptFor_NoClients`** (AC #2) — for both operator states,
  `composeSystemPromptFor(op, nil)` and `composeSystemPromptFor(op, []ClientIdentity{{}})`
  are byte-equal to `composeSystemPrompt(op)`. `TestSystemPromptText_Pinned` and
  `TestComposeSystemPrompt` are left untouched and must still pass.
- **`TestComposeSystemPromptFor_NamesClients`** (AC #1) — name only; name+version;
  two clients rendered in sorted order regardless of input order; duplicate
  identities collapsed; operator bytes still appended last. Asserts the composed
  text starts with `systemPromptText`, as `TestComposeSystemPrompt` does.
- **`TestClientSection_Pinned`** (AC #1) — pins the section's sentence against an
  independent transcription, for `systemPromptText`'s reason: a capability claim
  about a client must be a visible diff, not drift.
- **`TestAdmitClient_HostileInput`** (AC #3) — one row per attack: `\n` in the
  name, `\r`, C0 and C1 control runes, a `"`, a counterfeit section heading,
  invalid UTF-8, blank-after-trim, over-bound name, over-bound version, and five
  distinct clients. Each row asserts the structural invariant rather than a
  message: the composed text has exactly the daemon-authored line count, no line
  begins with any byte the client supplied, and the hostile substring is absent
  wherever the field was refused.
- **`TestPool_AttachedClients_Total`** (AC #4) — nil resolver; a resolver returning
  nil; an already-cancelled ctx; and a resolver that blocks forever, which must
  return within the bound. Each row also spawns through `Pool.Activate` and asserts
  the session comes up.
- **`TestPool_Activate_NamesAttachedClient`** (AC #5) — mint, install a resolver,
  `Activate`, and read the file the recorded argv names: it holds the client's
  reported name. Then evict, change the resolver's answer, re-`Activate`, and
  assert the file names the *new* client. Modelled on
  `TestPool_Activate_ComposesPromptSetAfterMint`, which is the assertion shape that
  catches a construction-only composition; an argv-flag assertion would not.

Package `internal/relay`:

- **`TestV2Handshake_RetainsClientIdentity`** — a v2 handshake whose `hello`
  carries `device_name` and `client_version`; `ActiveConns` reports both verbatim,
  including a control-character-bearing value, proving the relay retains without
  judging what `internal/sessions` will judge.
- **`TestV2Handshake_DropsOversizedClientIdentity`** — a `hello` whose
  `device_name` exceeds `maxRetainedClientNameBytes` enumerates as `""`, not as a
  truncation and not as ~64 KB copied per snapshot.
- **`TestV2Handshake_RejectedTokenLeavesNoIdentity`** — a rejected token yields no
  enumerable conn at all, so no unauthenticated peer's strings can be observed.

Gate: `go test -race ./internal/sessions/... ./internal/relay/... ./cmd/pyry/...`,
`go vet ./...`, `go build ./cmd/pyry`. Not `needs-real-claude`: every assertion here
is about bytes in a file the daemon writes, which the hermetic tier observes
directly.

## Open questions

1. **Does the install precede the manager's `Run` goroutine?** The setter's
   race-freedom rests on `startRelayV2` installing before `mgr.Run` starts *and*
   before `pool.Run`. The second holds (`startRelay` returns first). Verify the
   first in Phase B by reading where `mgr.Run` is launched relative to the
   transition-observer install. If it does not hold, hold the resolver in an
   `atomic.Pointer` instead of a plain field and record the change under
   `## Revisions`.
2. **Does any existing consumer format an `ActiveConn` wholesale?** Adding fields
   is invisible to `c.ConnID` / `c.Interactive` reads, but a `%+v` or `slog.Any`
   on the struct would start leaking remote-authored text into a log. Grep the
   consumers in Phase B; if one exists, that is a `SHOULD FIX` to land here.
3. **The bootstrap session is never named.** `refreshSystemPrompt` returns early
   when `sess.systemPromptPath == ""`, which is the bootstrap, whose file is
   daemon-scoped and lives on the `Pool`. It therefore carries no client names —
   exactly as it already carries no conversation prompt since #2150. Accepted as
   inherited, not introduced; confirm in Phase B that the bootstrap really does
   leave that field empty, and state it in the PR if so.

## Security review

**Verdict:** PASS (second pass — the first found a MUST FIX under Network & I/O,
now addressed in § Design; the sections below describe the revised plan.)

**Findings:**

- **[Trust boundaries]** No blocking findings. The design has exactly one
  untrusted→trusted door, `admitClient`, and the plan names the whole path into it
  so no second opinion can grow at another layer. Two supports were added because
  the type system alone gives no signal: `ClientIdentity`'s doc states its fields
  are remote-authored and unvalidated, and `ActiveConn`'s doc states the consumer's
  obligation rather than merely dropping the old "only non-secret routing/decision
  data" claim. Validating in `internal/relay` was considered and rejected — it
  would bind a wire type to a rendering decision it does not own, the same argument
  `composeSystemPrompt` makes for `Registry.SetSystemPrompt`.

- **[Tokens, secrets, credentials]** No findings, one prohibition made explicit.
  Nothing is generated, stored or compared. The retention reads `helloPayload`,
  whose `Token` field is plaintext credential material marked MUST-NOT-log by
  `HelloClientPayload`'s own doc; § Design now forbids parking the payload struct
  (or a pointer to it) on `V2Session`, which would keep the token alive for the
  session's lifetime and one `%+v` from a log line. Copy the two strings by value,
  nothing else. Separately and by design, the operator's device hostname now
  reaches claude's context and therefore the model provider — that is precisely
  what the ticket asks for, and it is stated here so it is a recorded decision
  rather than a surprise.

- **[File operations]** No findings. No client-supplied byte reaches a path
  component: `systemPromptPathFor` derives from `registryPath` and a `ValidID`-gated
  session id, and client text reaches file *content* only. The write is
  `writeSystemPromptFile` unchanged — scratch file, fsync, rename, `0600` preserved
  — so this ticket adds no TOCTOU, no symlink follow and no partial-state window.

- **[Subprocess / external command execution]** No findings, and the distinction
  is worth stating because it is the one a reader is most likely to get wrong: the
  spawn argv carries the *path* of the prompt file, never its bytes. No client
  string is ever an `exec.Command` argument, and no `sh -c` is involved.

- **[Cryptographic primitives]** Not applicable. No randomness, no key material,
  no comparison against a secret. The one crypto-adjacent property is that a re-key
  must not clear the retained identity, which `handleRekeyInit` satisfies by never
  touching the fields — the same way it preserves `device`, `peerStatic` and
  `interactive`.

- **[Network & I/O]** **MUST FIX — fixed in this revision.** The first pass had
  the relay retaining both strings verbatim. `HelloClientPayload` bounds neither at
  decode (unlike `MintPairingPayload.DeviceName`, which `UnmarshalJSON` refuses over
  `MaxDeviceNameBytes`); the only ceiling is the 65519-byte application-envelope
  cap. An authenticated client could therefore park ~64 KB per conn on `V2Session`
  and have it copied into **every** `ActiveConn` snapshot — which the fan-out takes
  several times per turn, per open conn. That is an authenticated client
  controlling a per-snapshot allocation multiplier for the daemon's life. Fixed by
  bounding at the retention site (`maxRetainedClientNameBytes` /
  `maxRetainedClientVersionBytes`, over-bound ⇒ retained as `""`, dropped rather
  than truncated so no value is invented). It does not duplicate `admitClient`:
  that door owns *display* policy and the character set, this bound owns *memory
  and copy cost*, and resource bounds belong at the point of retention.

- **[Error messages, logs, telemetry]** One SHOULD FIX. The design adds no log
  line and `admitClient` is silent by construction, so a refused hostile name has
  no line to appear in — but `ActiveConn` is consumed by roughly eight fan-out
  sites in `cmd/pyry`, and a single `%+v` or `slog.Any` on the struct at any of
  them would start emitting remote-authored text into the daemon log the moment
  these fields land. **SHOULD FIX:** grep those consumers in Phase B and fix any
  wholesale format found (Open Question 2). The verifier should check this landed.

- **[Concurrency]** One SHOULD FIX, already carried as Open Question 1. The
  resolver field follows `SetTransitionObserver`'s unsynchronised pre-`Run` install
  contract, which is only race-free if the install genuinely precedes every reader
  goroutine's creation — including the relay manager's own `Run`. **SHOULD FIX:**
  verify that ordering in Phase B and fall back to an `atomic.Pointer` if it does
  not hold; `-race` in the gate is the deterministic backstop either way. Two
  properties are sound as designed: the resolve holds no pool lock and sits in the
  window the file write already occupies, so `capMu → mu → lcMu` cannot invert; and
  the snapshot's staleness is benign because the section is written in the past
  tense as a transcription, so a client detaching mid-compose makes the sentence no
  less true. The `clientIdentityTimeout` also degrades a future
  called-from-`Run` deadlock into a bounded stall rather than a hang.

- **[Threat model alignment]** One accepted residual, no fix. `docs/protocol-mobile.md`
  § Security model's relevant actor here is a paired-but-hostile client, and this
  ticket gives it a channel to place chosen text into claude's system prompt. Four
  properties bound it: the character-set refusal blocks structural injection, the
  value sits inside a daemon-authored sentence within quotes whose delimiter is
  itself refused, the byte and count bounds cap the volume, and the sentence states
  the provenance so the text is presented as a client's self-report rather than as
  instruction. What is *not* mitigated is the semantics of an admissible 64-byte
  name — `"Ignore all previous instructions"` is admissible. That is inherent to
  AC #1, which mandates naming the client by what it reported, and refusing
  arbitrary text would refuse the feature. **The residual is accepted** because the
  same actor already holds `send_message`, which puts unbounded attacker-chosen
  text into claude's turn: 64 bytes in the system prompt is strictly less
  capability than what pairing already grants. The one genuinely new property is
  *durability* — a name persists across the session's turns where a message does
  not — and that is what the bounds and the provenance framing exist to contain.
  The pairing boundary, not this gate, is the control that matters, and it is out
  of scope here.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-08
