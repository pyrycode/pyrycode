# #2155 — `pyry channel new` creates a channel in the current directory

One local control verb, `channel.new`, plus the CLI arm that reaches it. Run inside a
project folder on the host, it creates a **promoted** conversation (a channel) whose
`Cwd` is that folder's canonical real path, and prints the conversation id.

## Files read

- `internal/control/protocol.go` → `VerbSessionsNew`, `SessionsPayload`, `SessionsNewResult`,
  `Request`, `Response` — the four shapes a verb needs, and the `omitempty` discipline that
  keeps existing verbs' wire bytes unchanged when new members are declared.
- `internal/control/server.go` → `handleSessionsNew` (long-op handler shape: fresh background
  ctx at `sessionOpTimeout`, conn write deadline extended by `sessionOpConnGrace`),
  `handleRekey` (`if r == nil` → "not configured" idiom AC#3 asks for), `handleAttachFile`
  (nil-dependency-before-payload guard order; verbatim pass-through of a seam's error text and
  the contract that makes that safe), `SetFileAttacher` (late-bound plain-func setter, the seam
  shape this verb copies), `Server.handle` (the dispatch switch).
- `internal/control/client.go` → `SessionsNew` — the one-shot dial→encode→decode→close helper
  shape, and its empty-payload-response guard.
- `internal/control/wire_compat_test.go` → `TestServer_UnknownVerbWithUndeclaredPayload` — pins
  that nothing calls `json.Decoder.DisallowUnknownFields`; purely additive `omitempty` members
  leave it green.
- `internal/relay/handlers/create_conversation.go` → `CreateConversation` — the reference create
  sequence (mint → `reg.Create` → eager `reg.Save`), and `msgCreateConversationCwdRejected` /
  `msgCreateConversationMintFailed`: **static** user-facing refusals with the wrapped detail
  logged only. `ErrSpawnDirRejected` is the sentinel `resolveSpawnDir` wraps.
- `cmd/pyry/main.go` → `resolveSpawnDir` (the mandated confine→trust sequence),
  `confineWorkdirToHomeCreating` (creates a missing leaf; its error text **echoes the resolved
  path and `$HOME`**), `sessionMinter.Create` (the two-line mint the cmd layer already owns),
  `runArgs` (router switch, no `default` arm), `helpText`, `parseClientFlags`,
  `runSessionsNew` / `parseSessionsNewArgs`, and the `ctrl := control.NewServer(...)` window
  where `convReg`, `pool` and `convRegistryPath` are all in scope.
- `cmd/pyry/rekey.go` → `parseRekeyArgs`, `rekeyVerdict`, `runRekey` — the exit-code idiom AC#2
  names: usage → `os.Exit(2)`, typed/server reject → pure verdict formatter → `os.Exit(1)`,
  transport → wrapped return (main prints `pyry: …` and exits 1).
- `cmd/pyry/attach_file.go` → `fileAttacher` — precedent for a per-verb dependency constructor
  living in its own `cmd/pyry` file rather than in `main.go`.
- `internal/conversations/conversation.go` → `Conversation` (`Name` is `*string`; `IsPromoted`
  always serialized), `registry.go` → `Create` (bare append, **no** uniqueness validation) and
  `Save`, `id.go` → `NewID` (crypto/rand UUIDv4).
- `internal/sessions/pool.go` → `Mint(label, spawnDir string) (SessionID, error)` — mints and
  persists without spawning (#2085).
- `internal/e2e/harness.go` → `StartIn`, `runVerb` (**does not set `cmd.Dir`**), `binPath`,
  `childEnv`, `writeSleepClaude`; `internal/e2e/sessions_new_test.go` and `restart_test.go`
  (`newRegistryHome`) — the fake-daemon tier AC#4 mandates.
- `docs/knowledge/features/cli-verb-dispatch.md` — the `runArgs` switch must keep **no `default`
  arm** (near-drop-in fall-through to claude), and a help-text assertion must tokenise lines
  rather than substring-match.
- `docs/knowledge/features/conversations-package.md` — "channel" is the registry's own word for
  `IsPromoted=true`; its speculative `pyry conv name <id>` UX was never built and is ignored.

## Context

A conversation's workspace is its `Cwd`. Today it is chosen only from a paired client, whose
picker offers the recent-workspaces list (#888) plus "create a folder under the current one"
(#887). A folder that exists on the host but has never hosted a conversation is unreachable from
every client. The operator's shell is already standing in the right directory, so the cheapest
picker is a local verb.

`internal/control` has no conversation verb at all. This adds the first one, cloning
`sessions.new` layer by layer. Fan-out to connected clients is the sibling ticket (#2156); until
it lands the new channel surfaces on a client's next `list_conversations` — a reconnect or a
restart. That is the accepted first cut.

No ADR is warranted: this introduces no new architectural rule, only a second caller of an
existing one (`resolveSpawnDir`'s confine→trust sequence). The documentation phase should fold
the verb into `control-plane.md` and `cli-verb-dispatch.md`.

### Sizing: over the 800-line ceiling, deliberately, on the floor rule

My own count lands at **~1100 lines total written work**, higher than the refiner's ~950. Five
of the six size-S boundaries hold comfortably — 5 production files, 2 new exported types, **0**
consumer call sites needing simultaneous update (every change is additive; `NewServer`'s
signature stays frozen behind the setter), 5 acceptance criteria, 7 reject branches. Only the
line ceiling trips.

The only available cut is control verb first, CLI arm second, and the control verb's sole
consumer is that CLI arm — a slice with exactly one consumer inside its own family is part of
that consumer, not a ticket, and it would ship a verb no operator can reach. Per the
floor-beats-ceiling rule the overage is recorded here and the ticket is built whole. I re-derived
this independently of the refiner's estimate and reach the same conclusion.

## Design

Five production files, all additive.

### 1. `internal/control/protocol.go` — the wire

- `VerbChannelNew Verb = "channel.new"`. Dotted namespace matching `sessions.*` / `mcp.approve`;
  the dot is a documentation convention, not a parser rule. New namespace — no collision with the
  `runArgs` switch (verified: nothing in `cmd/pyry` claims `"channel"`).
- `ChannelPayload{ Cwd, Name string }` — `Cwd` **without** `omitempty` (an empty cwd is invalid
  input the handler rejects, mirroring `RekeyPayload.ConnID`), `Name` with `omitempty` (absent
  means "derive from the path's base name", the AC#1 default).
- `ChannelNewResult{ ConversationID string }`.
- `Request.Channel *ChannelPayload` and `Response.ChannelNew *ChannelNewResult`, both
  `omitempty`. Existing verbs' encoded bytes are unchanged, which is AC#5's guard.

### 2. `internal/control/server.go` — the handler and its seam

- `Server.channelCreator func(cwd, name string) (string, error)` guarded by `s.mu`, installed by
  `SetChannelCreator`. **A plain func behind a late-bound setter, following `SetFileAttacher`
  rather than `SetRekeyer`**, for that setter's stated reason: every dependency the work needs
  (`convReg`, `pool`, `convRegistryPath`, and `resolveSpawnDir` — unexported in package `main`
  and unimportable here) lives at `cmd/pyry`'s composition root. `internal/control` therefore
  owns the wire and the guard order and nothing else, and gains **no new imports**. Threading it
  through `NewServer` would cascade across that constructor's ~11 call sites.
- `handleChannelNew(conn, enc, payload)` — contract:
  - Guard order copies `handleAttachFile`'s: **nil dependency before payload validation**, so a
    daemon that never installed the seam answers identically whatever the request looks like and
    a caller cannot probe which dependencies a daemon has. Nil creator →
    `Response{Error: "channel.new: no channel creator configured"}` (AC#3's "clear 'not
    configured' error rather than panicking", the `handleRekey` idiom).
  - `payload == nil || payload.Cwd == ""` → `"channel.new: missing cwd"`.
  - Extends the conn write deadline to `sessionOpTimeout + sessionOpConnGrace` before the call —
    the create sequence mints a session and fsyncs two registries and can outrun the 5s handshake
    bound (#865, same as `handleSessionsNew`).
  - Creator error → `Response{Error: "channel.new: " + err.Error()}`; success →
    `Response{ChannelNew: &ChannelNewResult{...}}`. Every branch encodes exactly one `Response`.
  - **The pass-through of the creator's error text is only safe because `SetChannelCreator`'s
    doc comment obliges every reason to be static** — no host path, no `$HOME`. That obligation
    is load-bearing (see § Error handling) and is stated on the setter, exactly as
    `SetFileAttacher` states its own.
- One `case VerbChannelNew:` arm in `handle`'s switch.

### 3. `internal/control/client.go` — `ChannelNew(ctx, socketPath, cwd, name) (string, error)`

`SessionsNew`'s shape verbatim: one-shot request, `resp.Error` → `errors.New`, empty result →
`"control: empty channel.new response"`. No typed `ErrorCode` is introduced — there is no
sentinel the CLI must reconstruct, only messages it prints.

### 4. `cmd/pyry/channel.go` (new) — the creator and the CLI arm

**`channelCreator(reg *conversations.Registry, pool *sessions.Pool, registryPath string, log *slog.Logger) func(cwd, name string) (string, error)`**

The sequence, mirroring `CreateConversation` step for step:

0. **Reject an empty `cwd` here too**, before anything else, with the same static
   rejected-directory message. This is the second half of a two-sided guard, not redundancy with
   the handler's: `resolveSpawnDir("")` returns `("", nil)` — success, *with no confinement and
   no trust-mark* — and an empty spawnDir then means "the shared trusted workdir" to `Pool.Mint`,
   `filepath.Base("")` is `"."`, and the row would record `Cwd: ""`. The seam is fail-open on the
   empty string, so it must be guarded where it is consumed, not only at the wire. This copies
   `handleAttachFile`'s stated reasoning for its own two-sided empty-`SessionID` guard verbatim:
   the handler's check is a wire-shape check, and this one protects the seam from any future
   caller that does not come through the handler.
1. `resolveSpawnDir(cwd)` — expand-tilde → `confineWorkdirToHomeCreating` → `trustMark`.
   Confine **before** trust; the order is security-load-bearing and this reuses the one validator
   rather than re-implementing any part of it. Returns the realpath.
2. `conversations.NewID()`.
3. `pool.Mint(string(id), resolved)`.
4. Name default: `filepath.Base(resolved)` when `name == ""`, computed **after** confinement so a
   symlinked entry directory names the real folder.
5. `reg.Create(Conversation{ID, Name: &name, Cwd: resolved, CurrentSessionID: sid,
   IsPromoted: true, LastUsedAt: now})`.
6. Eager best-effort `reg.Save(registryPath)`; a Save failure is logged, not returned — the row
   is live in memory and durability is best-effort, exactly as `CreateConversation` treats it.

**Why this shape and not the two the ticket floats.** The ticket frames the choice as "handler
resolves then the minter re-resolves (a second `trustMark` write)" versus "widen
`handlers.SessionCreator` to return the resolved path (a sixth production file)". Both exist
because `sessionMinter.Create` swallows the resolved path. This creator sits at the *same layer*
as `sessionMinter` and so needs neither: `sessionMinter.Create` is `resolveSpawnDir` followed by
`pool.Mint`, and package `main` can call both directly. No double trust-mark, no widened
interface, no sixth file, and `handlers.SessionCreator` keeps its single implementation.

**`parseChannelNewArgs(args []string) (name string, err error)`** — `parseSessionsNewArgs`'s
shape: a `--name` string flag, zero positionals, any positional is an error. Extracted so the
parsing rules unit-test without a socket.

**`channelNewVerdict(err error) (exitCode int, stderrLine string)`** — pure formatter,
`rekeyVerdict`'s idiom, so the unit test never intercepts `os.Exit`. `nil` → `(0, "")`; anything
else → `(1, "pyry channel new: " + <message>)`. It never formats a path: its only input is an
error whose text the server already guaranteed static.

**`runChannel(args []string) error`** — peels `parseClientFlags`, dispatches the sole `new`
sub-verb; missing or unknown sub-verb prints usage and `os.Exit(2)`. (`runSessions` returns a
plain error here, which main maps to exit 1; AC#2 mandates 2 for usage, so this follows `rekey`.)

**`runChannelNew(socketPath string, args []string) error`**:
- parse failure → stderr + usage line + `os.Exit(2)`
- `os.Getwd()` failure → `(1, …)` via the verdict formatter and `os.Exit(1)` — the "shell's
  directory was deleted underneath it" case, refused client-side before dialling
- `control.ChannelNew` success → `fmt.Println(id)`, return nil (exit 0)
- server reject → `channelNewVerdict` + `os.Exit(1)`, so the operator sees a clean
  `pyry channel new: …` line rather than `pyry: channel.new: …`
- transport error (dial/encode/decode) → wrapped return; main prefixes and exits 1

Discriminating "server reject" from "transport error" copies `runRekey`'s `isServerReject`
approach, but by *construction* rather than by an enumerated message list: `control.ChannelNew`
returns the server's message as a plain error and transport failures as wrapped `net`/`json`
errors. This is the one place the shape is not a verbatim copy — resolve it in Phase B (see
§ Open questions).

### 5. `cmd/pyry/main.go` — router, help, wiring

- `case "channel": return runChannel(args[2:])` in `runArgs`. No `default` arm is added.
- One `helpText` entry, `pyry channel new [--name <label>]`, tokenising as `pyry` + `channel` so
  `TestHelpTextDropsRemovedVerbs`'s scan keeps working.
- `ctrl.SetChannelCreator(channelCreator(convReg, pool, convRegistryPath, logger))` in the
  existing between-`NewServer`-and-`Serve` window, beside `SetFileAttacher`.

### Also in scope, not counted as production files

`README.md` gains the verb (AC#5). The knowledge base is explicitly the documentation phase's.

### Deliberately not built

`--dir`; an unpromoted variant; any push to connected clients (#2156); a duplicate-name guard —
`Registry.Create` is a bare append and the wire `create_conversation` has the same property, so
adding uniqueness here would *break* AC#3's indistinguishability.

## Concurrency model

No new goroutines, so no new shutdown path. Three existing disciplines are inherited:

- `handleChannelNew` loads `s.channelCreator` under `s.mu` and **releases the lock before
  calling it** — the leaf-lock rule `handleAttachFile` follows for the same reason: the call
  mints a session and writes two files, and holding `s.mu` across it would serialise every
  control verb behind one channel creation.
- `Registry.Create` and `Registry.Save` take their own locks in the documented order; the creator
  calls them as a plain caller and introduces no second lock.
- `Pool.Mint` persists the session registry under the pool's lock before returning.

The verb runs on the per-conn goroutine `Server.handle` already spawns, and returns before
`handle`'s deferred `conn.Close`. No handler takes ownership of the conn.

There is no atomicity across `pool.Mint` and `reg.Create`: a crash between them leaves a minted
session no conversation points at. That is exactly `CreateConversation`'s existing window and is
inherited, not introduced — the ordering (mint first) is what guarantees AC#1's "no half-bound
row", since a persisted row always points at a session that exists.

## Error handling

| Failure | Where | Wire / operator surface | Exit |
|---|---|---|---|
| Seam never installed | `handleChannelNew` | `channel.new: no channel creator configured` | 1 |
| Empty `Cwd` (wire shape) | `handleChannelNew` | `channel.new: missing cwd` | 1 |
| Empty `Cwd` (seam guard) | `channelCreator` | `channel.new: working directory not allowed` | 1 |
| `$HOME` escape / unresolvable | `channelCreator` | `channel.new: working directory not allowed` | 1 |
| id-gen or mint failure | `channelCreator` | `channel.new: could not start channel session` | 1 |
| Registry Save failure | `channelCreator` | *(none — logged, row is live)* | 0 |
| `os.Getwd()` failure | `runChannelNew` | `pyry channel new: …` | 1 |
| Flag parse / stray positional | `parseChannelNewArgs` | usage line | 2 |
| Dial / encode / decode | `control.ChannelNew` | `pyry: channel new: …` | 1 |

**The static-message rule is the security core of this ticket.** `resolveSpawnDir` wraps
`confineWorkdirToHomeCreating`, whose message embeds *both* the resolved path and the operator's
`$HOME`. Passing that through would violate AC#2's "echoes neither the requested nor the resolved
path". So `channelCreator` maps every failure onto one of two package-level static constants —
mirroring `msgCreateConversationCwdRejected` and `msgCreateConversationMintFailed` — and logs the
wrapped detail daemon-side, where the operator already reads their own paths. Matching is
`errors.Is(err, handlers.ErrSpawnDirRejected)`, which survives future wrapping; anything else is
the mint arm.

Nothing is persisted on a rejected path: `resolveSpawnDir` is step 1 and returns before
`conversations.NewID`, so a refusal leaves no row, no session, and no half-bound state (AC#2).
Note the one thing a *rejected* call still does: `confineWorkdirToHomeCreating` may create
directories before the second containment check fails. That is pre-existing behaviour of the
mandated validator, it can only create *inside* `$HOME` (check #1 gates creation), and it is not
persistence in the registry sense the AC means.

## Testing strategy

**`internal/control/channel_new_test.go`** — table-driven against a real socket via the
package's existing `startServer` helper:
- nil creator answers "no channel creator configured" *even with a well-formed payload* (proves
  guard order, not just the guard)
- `nil` payload and empty `Cwd` both answer "missing cwd"
- success returns the creator's id and populates only `Response.ChannelNew`
- a creator error reaches `Response.Error` prefixed once
- the fake creator records `(cwd, name)` so the handler is shown to forward both verbatim —
  no path handling in `internal/control`

**`cmd/pyry/channel_test.go`**:
- `parseChannelNewArgs` table: bare, `--name x`, `--name` empty, stray positional, unknown flag
- `channelNewVerdict` table: nil, server reject, transport error
- `channelCreator` against a real `conversations.Registry` + a `t.TempDir()` `$HOME`, reusing
  `conversation_spawndir_test.go`'s `trustMark` fake seam:
  - success under `$HOME` → promoted row, `Cwd` == the **resolved realpath**, `Name` == base name
    of that realpath, non-empty `CurrentSessionID`, and the row present in the file on disk
    (proves the eager Save)
  - `--name` override wins over the base name
  - a path escaping `$HOME` → error `errors.Is` `ErrSpawnDirRejected`, message **asserted not to
    contain the requested path, the resolved path, or `$HOME`**, and the registry left with zero
    rows and nothing written to disk
  - running twice in one directory yields two rows (duplicate names permitted by construction)
  - **an empty `cwd` is refused by the creator itself**, with the registry left empty — the
    guard that keeps `resolveSpawnDir`'s fail-open empty-string arm unreachable

**`internal/e2e/channel_new_test.go`** (AC#4, `//go:build e2e`, the fake-daemon tier — no live
claude, so `make check` covers the verb):
- `StartIn(t, home, "-pyry-claude="+writeSleepClaude(...))`, a project dir created **under** that
  home, the CLI run **from** it, then poll `~/.pyry/test/conversations.json` for a promoted row
  with the printed id, the expected cwd and name
- a second test for the `--name` override, or for the `$HOME`-escape refusal driven from a
  directory outside the temp home

Two traps this suite has to handle, both established during research:
- `runVerb` never sets `cmd.Dir`, so `Harness.Run` would send the *repo's* directory as the cwd
  and the daemon would refuse it. The test file gets its own small `runVerbIn(t, socket, home,
  dir, …)` helper over the package's `binPath` / `childEnv`. Deliberately **not** added to
  `harness.go`: that would make it a sixth production file for a convenience only this ticket
  needs today.
- On macOS `t.TempDir()` sits under `/var/folders/…`, and `/var` is a symlink. The daemon stores
  the symlink-resolved path, so every expectation compares against `filepath.EvalSymlinks(proj)`,
  never `proj`. An assertion written against `proj` passes on Linux and fails on macOS.

**Gate:** `go test -race ./internal/control/... ./cmd/pyry/... ./internal/conversations/...`,
`go test -tags=e2e ./internal/e2e/...`, `go vet ./...`, `go build ./cmd/pyry`. The full-module
race suite is the verifier's.

## Open questions

1. **Server-reject vs transport-error discrimination in `runChannelNew`.** `runRekey` uses
   `isServerReject`, an enumerated message-prefix list, because `control.Rekey` reconstructs
   server errors as plain `errors.New`. Resolve in Phase B by reading what `control.ChannelNew`
   can actually return on each path; prefer a sentinel wrap at the client helper over a second
   hand-maintained message list. If a sentinel is added, record it in `## Revisions`.
2. **Which second e2e test earns its cost** — the `--name` override or the `$HOME`-escape
   refusal. The refusal is the security-relevant one but needs a directory outside the temp home,
   which the harness's `childEnv` isolation makes awkward. Decide against the harness's actual
   behaviour in Phase B; the unit-level refusal test above covers the assertion either way.
3. **Whether `Response.ChannelNew`'s field should carry the bound session id too.** AC#1 requires
   the row to carry one but AC#2 only requires the conversation id on stdout. Default: no —
   emitting only what the AC names keeps the wire minimal. Revisit only if the e2e test cannot
   otherwise observe the binding (it can: it reads `conversations.json`).

## Security review

**Verdict:** PASS (second pass — the first found one MUST FIX, now folded into § Design step 0)

**Findings:**

- **[Trust boundaries] MUST FIX — fixed before commit.** The first draft guarded the empty `cwd`
  only at the wire, in `handleChannelNew`. But `resolveSpawnDir` is **fail-open on the empty
  string**: it returns `("", nil)` — success with no confinement and no trust-mark — because `""`
  is its "use the shared trusted workdir" signal for the phone path. A caller reaching
  `channelCreator` with an empty cwd would therefore skip validation entirely and write a row with
  `Cwd: ""` and a name of `"."`. `handleAttachFile` already argues this exact case for its own
  empty-`SessionID` guard (the seam it would reach, `Pool.Lookup("")`, resolves to the *bootstrap*
  session with a nil error) and concluded two guards, deliberately. The plan now does the same:
  the handler's is a wire-shape check, the creator's protects the seam from any caller that does
  not come through the handler. Pinned by a unit test asserting an empty cwd leaves zero rows.
- **[Trust boundaries] The boundary is single and named:** `resolveSpawnDir`. `internal/control`
  performs **no** path handling — it forwards the string verbatim and gains no new imports — so
  there is exactly one place where an untrusted path becomes a trusted one. The one invariant a
  reader must not break: `channelCreator` stores `resolved`, never the raw `cwd`. That is easy to
  get wrong by copying `CreateConversation`, which deliberately stores the *raw* string; it is
  AC#1's "one deliberate difference", and a unit test pins `Cwd` against
  `filepath.EvalSymlinks(proj)`.
- **[File operations] The confine→trust order is not this ticket's to get right, by design.**
  `resolveSpawnDir` is reused whole rather than inlined or re-sequenced. Trust-marking before
  confining would auto-trust a path outside `$HOME` (`trustMark` has no `$HOME` bound of its
  own). Phase B must not decompose that call.
- **[File operations] The create arm of `confineWorkdirToHomeCreating` is effectively
  unreachable here.** The CLI sends `os.Getwd()`, so the directory exists and `rest == ""` — no
  `MkdirAll` fires. It is reachable only by a race (the directory unlinked between `Getwd` and
  the daemon's confine), and even then containment check #1 gates creation, so nothing can be
  created outside `$HOME`. No new directory-creation surface. Accordingly, "the directory does not
  exist" is correctly **not** a daemon-side refusal and is not tested as one.
- **[File operations] TOCTOU is inherited, not widened.** The residual confine→chdir window
  documented on `sessionMinter` (validated realpath frozen at mint, child spawned on first
  message since #2085) applies identically, because this creator reaches `Pool.Mint` through the
  identical sequence. Winning it still requires `$HOME` write access, which is what the
  confinement exists to protect and which claude already holds.
- **[File operations] Permissions and atomicity are inherited and correct:** `MkdirAll` at
  `0o700`; `Registry.Save` writes via `os.CreateTemp` (0600) plus rename, so an interrupted save
  leaves no partial `conversations.json`. Symlinks are resolved, not followed blindly — the
  ancestor walk probes with `os.Lstat` so a symlink counts as existing and is `EvalSymlinks`'d
  rather than stepped over.
- **[Error messages, logs] SHOULD FIX, and designed in.** `confineWorkdirToHomeCreating`'s error
  text embeds **both** the resolved path and the operator's `$HOME`, and `resolveSpawnDir` wraps
  it with `%v`. Passing that to the wire would breach AC#2 directly. `channelCreator` therefore
  maps every failure onto one of two static package constants (mirroring
  `msgCreateConversationCwdRejected` / `msgCreateConversationMintFailed`) and logs the wrapped
  detail daemon-side only. The safety net is deterministic, not another rule: a unit test asserts
  the refusal contains neither the requested path, the resolved path, nor `$HOME`. Matching is
  `errors.Is(err, handlers.ErrSpawnDirRejected)` so it survives future wrapping.
  The client-side `os.Getwd()` failure is the one message that may name a path; it never crosses
  a boundary (the operator's own shell error on their own stderr) and the AC's rule is about the
  daemon's refusal.
- **[Tokens]** No token surface. The conversation id (`conversations.NewID`) and session id
  (`sessions.NewID`) are both `crypto/rand` UUIDv4s, both server-minted, neither client-supplied
  — a caller cannot choose, collide with, or overwrite a row. Neither is a capability: the id is
  printed to the operator's own stdout over a socket they already own. No storage, rotation,
  revocation or expiry surface is introduced.
- **[Subprocess]** Nothing in the diff calls `exec.Command`; `Pool.Mint` does not spawn (#2085).
  The only operator-influenced value that eventually reaches claude is the chdir target, and it
  is the validated realpath, not the requested string. No `sh -c`, no environment change.
- **[Cryptographic primitives]** No new primitives, no key material, no comparison against a
  secret. Randomness is `crypto/rand` via the two existing ID minters.
- **[Network & I/O]** Transport is the existing `0600` Unix socket with `Server.handle`'s
  handshake deadline; the write deadline is extended to `sessionOpTimeout + sessionOpConnGrace`
  as a stuck-write backstop, exactly as `handleSessionsNew` does. The decode of a request is
  unbounded — a pre-existing property of every verb, recorded in `handle`'s own TODO alongside
  the `0600`-bounds-the-realistic-N argument. **OUT OF SCOPE**: capping it is a control-plane-wide
  change, and touching it here would be an out-of-scope production edit.
- **[Network & I/O] OUT OF SCOPE — `Name` is unbounded.** A caller may store an arbitrarily large
  or control-character-bearing name. The wire `create_conversation` has the identical property
  (`p.Name` is a straight passthrough), so bounding it *in this verb only* would break AC#3's
  indistinguishability requirement. A bound belongs on the registry, where it would cover both
  writers, and is not this ticket's call. Note this verb prints only the conversation id, so its
  own output carries nothing operator-supplied to escape — `rekeyVerdict` quotes its conn-id with
  `%q` for that reason and `channelNewVerdict` has no such input.
- **[Concurrency]** No new goroutines, so no leak surface. `handleChannelNew` loads the creator
  under `s.mu` and **releases the lock before calling it** — the leaf-lock discipline
  `handleAttachFile` follows, load-bearing here because the call mints a session and writes two
  files. There is no check-then-mutate: `Registry.Create` is a bare append, duplicates being
  permitted by construction, so no TOCTOU exists to guard. Two concurrent invocations in one
  directory are safe — `Registry.Save` serialises snapshot order against rename order under
  `saveMu`, so neither update is lost. A crash between `Pool.Mint` and `Registry.Create` leaves
  an orphan session and no row; that is `CreateConversation`'s existing window, and the ordering
  is what guarantees AC#1's "no half-bound row" (a persisted row always points at a real session).
- **[Threat model]** The relevant threat is the confused deputy, not a remote attacker: the
  socket is `0600`, so the peer is the operator — but *any* process running as the operator can
  dial it. The `$HOME` confinement is what bounds where such a process can cause claude to be
  trusted and spawned, which is why it still applies despite a local socket, and why the ticket
  carries `security-sensitive`. `docs/protocol-mobile.md` § Security model governs the relay leg;
  this verb adds no relay surface — that is #2156.
- **[Design decision, recorded]** `--name ""` maps to the base-name default rather than to
  `Conversation.Name`'s "explicitly empty" state (`*string` to `""`). Nothing in AC#1 asks for an
  explicitly-empty channel name, and `pyry sessions new` treats an empty `--name` as "no label".
  The explicitly-empty state stays reachable only where it already is.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-07
