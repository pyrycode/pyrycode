# #2150 — a conversation's session spawns with its stored system prompt

## Files read

- `internal/sessions/systemprompt.go` → `systemPromptText`, `writeSystemPrompt` — #2093's constant and its atomic writer. The writer already has the append seam (`text` is a parameter); what it lacks is a per-session identity, because its name is fixed at `<dataDir>/system-prompt.txt`.
- `internal/sessions/settings.go` → `writeMCPSettings` — the shape to copy for a genuinely per-session file: id-derived name under a `0700` subdirectory of the data dir, `ValidID` gate on the data-dir branch, atomic scratch/fsync/rename, `0600` preserved by the rename, caller-owned removal.
- `internal/sessions/pool.go` → `Pool.New` (bootstrap write + cleanup defer), `Pool.Run` (the one removal site for the daemon-scoped file), `Pool.buildSession` (the choke point behind Mint and materialise; where `spawnBase` is frozen), `Pool.Mint` (save-failure rollback removing `sess.settingsPath`), `Pool.Remove` (post-Evict settings removal), `Pool.Activate` (the pool-owned spawn funnel and its `capMu` discipline), `Pool.SettingsFor` (the accessor shape AC4 names), `Pool.dataDir`.
- `internal/sessions/session.go` → `Session.spawnBase`, `Session.settingsPath`, `Session.settings` — the immutability and locking conventions the two new fields inherit.
- `internal/sessions/transition.go` → `rebindConversation` — the precedent for a nil `p.convReg` being a silent no-op rather than an error.
- `internal/conversations/conversation.go` → `Conversation.SystemPrompt` — #2149's tri-state (`nil` / non-nil `""` / non-nil text) and its `omitempty` contract.
- `internal/conversations/registry.go` → `Registry.Get`, `Registry.SetSystemPrompt` — `Get` returns a shallow copy under the registry lock, sharing the stored `*string`; `SetSystemPrompt` replaces the pointer and never mutates a pointee, so reading `*conv.SystemPrompt` off a returned copy is race-free.
- `internal/sessions/pool_system_prompt_test.go` → `systemPromptArgPath`, `assertSystemPromptFileAt`, `promptPathOf`, `TestPool_MintedSpawn_SharesBootstrapSystemPromptFile` — #2093's unit surface. The last of those asserts the design decision this ticket reverses and must be rewritten, not deleted.
- `internal/e2e/realclaude/interactive_system_prompt_test.go` → `sysPromptSpawnRecords`, `sysPromptArgFromRecord`, `TestInteractiveSystemPromptFile_LiveSpawnArgv` — the live spine, including a one-daemon-scoped-file assertion that this ticket invalidates.
- `internal/e2e/realclaude/interactive_change_workspace_test.go` header — records that `Conversation.Cwd`'s "takes effect on the next fresh session spawn" claim is unbacked by any production read. That is the failure mode this ticket exists not to repeat.
- `internal/e2e/realclaude/harness_daemon_test.go` → `seedBoundConversation`, `sealSendMessage`, `drainForAssistantReply`; `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` → `startPerConversationHarness`, `perConvHarness`; `internal/e2e/realclaude/interactive_stream_multiturn_continuity_test.go` → `drainForCompletedTurnText` (returns the turn's accumulated text, which AC5 needs).
- `docs/knowledge/features/sessions-package-key-types-writesystemprompt-systemprompttext.md` — records the daemon-scoped naming decision and names this ticket as its inheritor.
- `docs/knowledge/features/sessions-package-key-types-writemcpsettings-session-settingspath.md` — the per-session file lifecycle already documented for `--settings`.

## Context

#2149 gave `Conversation` a stored `SystemPrompt` with nothing reading it. This slice is the reader: a session bound to a conversation must spawn its claude with that conversation's prompt appended.

#2093 already built every mechanism except the per-conversation part — the flag, the atomic writer with a `text` parameter, and the argv join at both composition sites. This ticket changes what the caller composes and gives the file a per-session identity; it invents nothing.

Two rulings shape it. Setting a prompt does not restart a running session (Juhana's), so the value takes effect at the next session start and no live-update path is built. And #2085 split "minted" from "started", so the normal operator flow — create the channel, set its prompt, send the first message — sets the prompt *after* `buildSession` has already frozen `spawnBase` and *before* any child exists. That window is the whole difficulty: a naive compose-once-in-`buildSession` ships a feature that is dead for the flow operators actually use, which is exactly the shape `Conversation.Cwd` is stuck in today.

No ADR is warranted: this is one package's file lifecycle, and the decision record already lives in `writeSystemPrompt`'s docstring.

### Sizing

Estimated ~900 lines of total written work — about 12% over the table's 800-line line, stated rather than hidden, and the same overage the refiner's `Estimate:` line declares. Every other boundary holds: 3 production files (`systemprompt.go`, `pool.go`, `session.go`), 0 new exported types (one new method, `Pool.SystemPromptFor`), 0 consumer call sites needing simultaneous update (the resolver reads `p.convReg`, already on the Pool, so no mint/revive signature widens), 5 acceptance criteria, and no state-machine reject fan-out.

The one available split is AC4, whose sole consumer is #2152 — the one-consumer case the sizing floor forbids putting on the board. The floor wins over the ceiling: build it whole. Split depth is `parent 2094 / grandparent none`, so a split was permitted; it is the floor, not the depth cap, that rules it out.

## Design

### 1. `writeSystemPrompt` gains a per-session identity

Split #2093's helper into a path derivation and a writer, so a refresh can rewrite an *already-derived* path:

- `systemPromptPathFor(registryPath string, id SessionID) (string, error)` — `registryPath == ""` (persistence disabled) returns `""`, meaning "mint a temp name". `id == ""` returns `<dataDir>/system-prompt.txt`, the daemon-scoped bootstrap file #2093 wrote. A non-empty `id` is gated on `ValidID` (a malformed id is a hard error, for `writeMCPSettings`' documented reason — it names a file) and returns `<dataDir>/session-prompts/<id>.txt`, with the directory created at `0700`.
- `writeSystemPromptFile(final, text string) (string, error)` — #2093's atomic body verbatim: scratch file in the destination directory (or `os.TempDir` when `final == ""`), write, fsync, close, rename, `0600` preserved by the rename. Returns the path written.
- `writeSystemPrompt(registryPath string, id SessionID, text string) (string, error)` — the composition of the two, and the only entry point construction sites use.

`session-prompts/` is a sibling of `session-settings/`, not a tenant of it: that directory's `*.json` contents are documented to count sessions exactly.

Why the derivation must be separable from the write: a `/clear` rotation re-keys a session in place (`Pool.rekeyLocked`), so `sess.id` changes while `spawnBase` keeps naming the file derived from the *old* id. A refresh that re-derived from the current id would write a file nothing reads. The refresh writes `sess.systemPromptPath` verbatim. This mirrors `settingsPath`, which is already stable across a rekey for the same reason.

### 2. Composition

`composeSystemPrompt(operator string) string`:

- `operator == ""` → exactly `systemPromptText`, byte for byte. This is AC2's contract and it is what the nil and explicitly-empty tri-states both collapse to.
- otherwise → `systemPromptText + "\n" + operator`. The constant already ends in `\n`, so the added byte is a blank-line separator; the operator's bytes are appended verbatim, unmodified and untrimmed, at the tail.

The tri-state stays in the registry. The spawn site carries one predicate — has bytes to append — because the resolver flattens `nil` and `""` to the same empty string.

### 3. Resolution

`(p *Pool) conversationPrompt(label string) string` — returns `""` when `p.convReg` is nil (the test default `Config.ConversationsRegistry` documents), when `label` is empty, when the registry has no such conversation, or when the row's `SystemPrompt` is nil. Otherwise the pointee. A label naming no conversation is the no-prompt path, never an error: the same posture `rebindConversation` takes for a nil registry and an unowned id.

`label` is the conversation id at both production sites (`create_conversation` → `sessionMinter.Create` → `Pool.Mint`, and `sessionRouter.revive`), so no signature widens and no resolver is threaded in from `cmd/pyry`.

### 4. `buildSession` — construction

Replaces the `p.systemPromptPath` join with a per-session write:

1. `text := composeSystemPrompt(p.conversationPrompt(label))`
2. `promptPath, err := writeSystemPrompt(p.registryPath, id, text)` — a failure fails the build, wrapped with no prompt bytes.
3. join `--append-system-prompt-file promptPath` onto `base`, where it survives every recompose exactly as the `--settings` pair does.
4. store `promptPath` and the *operator* bytes on the `Session`.
5. extend the existing `built = false` defer to remove `promptPath` too, so every error return between the write and a successful build takes the file with it.

`Pool.New`'s bootstrap site is untouched apart from passing `""` as the id: it is constructed before any conversation exists and can never become a conversation's bound session.

### 5. `Pool.Activate` — the mint window

`Activate` is the pool-owned funnel every first spawn and every re-activate passes through. Before the cap logic, when the session is not already active, recompose from the registry and rewrite `sess.systemPromptPath`. Because the path is stable and the write is a rename, a backoff restart re-execing the installed argv reads whatever the file then holds, and a live child never sees a partial file.

Skipping the rewrite when the session is already active is the ruling in code: a running child's prompt is not touched, and `Activate`'s already-active branch is a plain LRU touch on the hot path with no disk write.

**Refresh failure is a logged warning, not an error return.** The file was written at `buildSession` and the write is atomic, so a failed refresh leaves the previous complete composition in place — never a missing or truncated file. Failing an operator's message on a transient disk error, when the fallback is one-revision-stale prompt bytes, is the worse trade. The warning carries the error (whose path fragment is already public, see AC3) and no prompt bytes.

Lock order is unchanged: the refresh takes `p.mu` and releases it before `p.capMu` is acquired, so no goroutine holds `p.mu` while acquiring `p.capMu`.

### 6. `Session` — two new fields

- `systemPromptPath string` — absolute path to this session's appended-prompt file, a member of `spawnBase` and therefore immutable post-construction and read without a lock, exactly like `settingsPath`. Empty on the bootstrap session, whose file is daemon-scoped and lives on the Pool.
- `systemPrompt string` — the *operator* bytes this session was last composed with (not the composed whole). Written at construction and by the `Activate` refresh under `p.mu` (write), read under `p.mu` (RLock) by the accessor — the same discipline `settings` follows, deliberately not `lcMu`.

Storing the operator half rather than the composed text is what lets #2152 compare against the stored `*string` without stripping a constant it does not own.

### 7. AC4 — `Pool.SystemPromptFor`

`(p *Pool) SystemPromptFor(id SessionID) (string, error)` — `SettingsFor`'s shape: one `p.mu.RLock` acquisition, `ErrSessionNotFound` on a miss, the stored operator bytes otherwise. No control-plane verb, no wire frame; #2152 puts it on the wire.

### 8. Lifecycle — AC3

| Moment | Site | Action |
|---|---|---|
| every error return during a build | `buildSession`'s success-flag defer | remove the file |
| mint's save-failure rollback | `Pool.Mint` | remove the file beside `sess.settingsPath` |
| session teardown | `Pool.Remove`, after `Evict` returns | remove the file beside `sess.settingsPath` |
| daemon shutdown | `Pool.Run`'s defer | `os.RemoveAll` the `session-prompts` directory |
| daemon start | `Pool.New` | `os.RemoveAll` the `session-prompts` directory before the bootstrap write |

`materialise`'s rollbacks deliberately do **not** remove it, mirroring the settings file's documented reasoning: the id there is caller-supplied and a concurrent same-id builder may already own the byte-identical path.

The shutdown removal is what makes "never outlives the daemon" true, and it is safe because a warm start rebuilds every revived session's file through `buildSession`. The startup purge is the same claim held after a `SIGKILL`, which no defer survives; it is sound because `Pool.New` runs before any session is materialised.

Mode is `0600` (`os.CreateTemp`, preserved by the rename) and the directory `0700` — no more permissive than the `--settings` file beside it.

## Concurrency model

No new goroutines. Three shared-state interactions:

- `p.convReg` reads go through `Registry.Get`, which takes the registry's own mutex and returns a copy. The shared `*string` is never mutated in place by `SetSystemPrompt`, so the deref is race-free.
- `sess.systemPrompt` is a `p.mu`-guarded field: written under the write lock by the refresh, read under RLock by `SystemPromptFor` and set lock-free at construction (before the session is registered).
- `sess.systemPromptPath` is immutable post-construction, like `spawnBase` and `settingsPath`.

The file write in the refresh happens **off** `p.mu` — the lock covers only the field update — so no I/O runs inside the pool's critical section. Lock order `capMu → mu → lcMu` is preserved.

## Error handling

| Failure | Behaviour |
|---|---|
| `writeSystemPrompt` at `buildSession` | fails the build; the defer removes the settings file and the prompt file; error wrapped `sessions: write system prompt: %w`, carrying no prompt bytes |
| refresh write at `Activate` | logged at Warn, spawn proceeds with the previous complete composition (see § 5) |
| malformed session id on the data-dir branch | hard error from `systemPromptPathFor`, matching `writeMCPSettings` |
| conversation not found / registry nil / prompt nil | not an error — the no-bytes path |
| removal failures | best-effort `_ = os.Remove`, as at the settings sites |

No error string and no log line ever contains prompt bytes: the only values interpolated are paths and wrapped `os` errors.

## Testing strategy

Unit (`internal/sessions`, table-driven where the input is pure):

- `composeSystemPrompt` over the three tri-state inputs — nil-equivalent `""`, explicitly-empty `""`, and text — asserting byte equality with the constant for the first two.
- A minted session's argv names a **per-session** path under `session-prompts/`, distinct from the bootstrap's, holding the constant when the conversation has no prompt (AC2, freshly-minted path). This replaces `TestPool_MintedSpawn_SharesBootstrapSystemPromptFile`, whose shared-path assertion this ticket deliberately reverses.
- A minted session whose conversation carries prompt bytes spawns with the constant plus those bytes (AC1, first spawn).
- **The mint window:** mint with no prompt, `SetSystemPrompt` on the registry, then `Activate` — the file the argv names holds the prompt (AC1's load-bearing clause).
- Re-activate after an eviction re-composes (AC1's re-activate clause).
- A revived session (the `materialise` path) composes the same way (AC2's second path).
- `Pool.Remove` removes the file; `Pool.Run`'s return removes the directory (AC3).
- The prompt bytes appear nowhere in a captured `slog` buffer across a mint-and-activate cycle, while the argv record does carry the path (AC3's split).
- `SystemPromptFor` reports the operator bytes for a live session and `ErrSessionNotFound` otherwise (AC4).

The existing `stripSystemPrompt` / `systemPromptArgPath` helpers keep working unchanged; `promptPathOf` gains a per-session sibling.

Live (`make e2e-realclaude`, AC5): a new `internal/e2e/realclaude` case seeds `conversations.json` before daemon start with two conversations — one whose `system_prompt` demands a distinctive per-run marker token, one with no prompt — each bound to a session id that is not in `sessions.json`, so the first message to each drives `sessionRouter.revive` → `materialise` → `buildSession` with the conversation id as the label. The prompted conversation's turn text must carry the marker; the unprompted one's must not. The negative arm is what makes the positive one evidence rather than coincidence.

Seeding before daemon start needs a pre-spawn hook that `startPerConversationHarness` does not have; it gains a seeded variant sharing one body, with the existing entry point passing no hook. `TestInteractiveSystemPromptFile_LiveSpawnArgv`'s "the spawns name one file" assertion is inverted in the same commit: the bootstrap and the conversation's session now legitimately name different files.

## Open questions

1. **Does the refresh belong in `Pool.Activate` or deeper, at the lifecycle goroutine's spawn?** `Activate` is chosen because it is the documented pool-owned funnel and keeps the registry read out of the lifecycle goroutine. Resolve during implementation if a re-activate path is found that bypasses it.
2. **Does any test construct a `Session` literal that would now need the new fields?** Package tests that hand-build a `Session` and never spawn leave both empty, which the field docs already permit. Confirm no test asserts an exact `spawnBase` without going through `stripSystemPrompt`.
3. **Is `conversations.Registry` constructible in this package's tests without an import cycle?** `internal/sessions` already imports `internal/conversations`, so no; confirm the constructor's name at implementation time.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** The boundary is explicit and singular, and it is *not* in this ticket: `Registry.SetSystemPrompt` is the only writer of `Conversation.SystemPrompt`, and it bounds the value at `MaxSystemPromptBytes` (8192) and refuses invalid UTF-8 before storing. This ticket is a pure reader downstream of that door. No production caller of `SetSystemPrompt` exists today (#2151 is the write verb), so the only current path into the field is an operator editing `conversations.json` — but the design is written for #2151's phone-supplied text, where the value is remote-influenced content that shapes what the daemon's claude is told. Two properties keep that survivable and both are load-bearing: the text is **appended**, never replacing claude's own system prompt (`composeSystemPrompt` concatenates onto `systemPromptText` and has no replacing branch), and it reaches claude only as file *content*, never as an argv element or an environment value.
- **[Trust boundaries]** SHOULD FIX — the resolver must treat "conversation not found" and "registry nil" as the **no-bytes** path and never as an error that could be turned into a message the operator sees. `conversationPrompt` returns `""` on every miss; keep it total. Phase B must not add an error return here.
- **[Tokens, secrets, credentials]** No credentials are involved. The prompt itself is operator-authored text that may name projects, people, or intent, so it is treated as confidential-ish: mode `0600` in a `0700` directory, removed at session teardown, at daemon shutdown, and — for the `SIGKILL` case no defer survives — purged at daemon start. It never enters a log line, an error string, an argv element, or `ps` output. Lifecycle is complete: created at `buildSession`, rewritten at `Activate`, removed at four sites (§ 8). There is no rotation or expiry concept because the value is not a secret with a lifetime; it dies with its session.
- **[File operations]** Path traversal is closed by construction: the filename derives from the **session id**, gated on `ValidID` on the data-dir branch (`systemPromptPathFor`), never from the prompt or from any conversation id. A malformed id is a hard error, not a silent temp-file fallback — the posture `writeMCPSettings` already takes for the same reason. No check-then-use: the write is scratch-file-plus-`rename`, so there is no `Stat`-then-`Open` gap, a live child reads either the complete old file or the complete new one, and the rename replaces a symlink at the destination instead of writing through it.
- **[File operations]** OUT OF SCOPE — a hostile writer who already controls the daemon data dir could replace `session-prompts/` with a symlink before `MkdirAll` runs and see the write follow it. That actor already controls `sessions.json` and the `--settings` file pyry hands claude, which `writeMCPSettings`' own docstring names as code execution as the operator; this adds no boundary. `os.RemoveAll` on the directory does not follow a symlink at that path — it unlinks the link itself — so the purge does not widen it either. Two daemons sharing one data dir would have the startup purge delete each other's files; that configuration already corrupts `sessions.json` and is out of scope for the same reason.
- **[Subprocess / external command execution]** This is the category the file indirection exists for, and it is a MUST-NOT-REGRESS rather than a finding: `internal/streamsup`'s spawn logs the entire argv at Info on every spawn, so an `--append-system-prompt <text>` spelling would print the operator's prompt into the daemon log on every restart and into `ps` for every local user. Only the daemon-derived **path** is passed to `exec`; no operator-controlled value becomes a command-line argument. No shell is involved (`exec.Command` with an argv slice, unchanged by this ticket), and the child's environment is untouched.
- **[Cryptographic primitives]** Not applicable — no randomness is security-relevant here. `os.CreateTemp`'s random scratch suffix is a collision-avoidance device on a path that is immediately renamed to a deterministic name; nothing derives a secret, compares one, or keys anything.
- **[Network & I/O]** The only unbounded input is the prompt, capped at 8192 bytes at the registry door before this ticket ever sees it. On-disk growth is bounded by (live sessions × 8KB) and is reclaimed at teardown, shutdown, and startup. No socket, listener, or timeout surface is touched.
- **[Error messages, logs, telemetry]** MUST-NOT-log: the composed text and the operator bytes, on every path — construction, refresh, removal, and the `Activate` warning. MUST-log stays as today: the argv record's *path*, which is how the `--settings` path already reaches the log and which AC3 explicitly blesses. Wrapped `os` errors carry paths, never content, and no `%q` of prompt text appears anywhere in the design. A unit test asserts the marker bytes are absent from a captured `slog` buffer across a mint-and-activate cycle, so this is enforced rather than asserted in prose.
- **[Concurrency]** Lock order is unchanged (`capMu → mu → lcMu`): the refresh takes and releases `p.mu` before `Activate` acquires `p.capMu`, and the file write runs off `p.mu` entirely, so no I/O executes inside the pool's critical section. `sess.systemPrompt` follows `settings`' documented discipline (write under `p.mu`, read under RLock, set lock-free before registration), so there is no torn read. Two concurrent `Activate`s on one session rename two distinct scratch files over the same destination: last writer wins and both contents are complete valid compositions. `Registry.Get` copies under the registry's own mutex and `SetSystemPrompt` replaces the `*string` rather than mutating a pointee, so the deref cannot race a concurrent set. No goroutine is created, so none can leak. Shutdown mid-write leaves at worst a dotted `.system-prompt-*.txt.tmp` scratch file that the naming convention keeps from ever being mistaken for a prompt file, and that the startup purge collects.
- **[Threat model alignment]** The relevant mobile-protocol threat is a paired-but-hostile client steering the daemon's assistant. This ticket does not widen it: it adds no verb and no frame (AC4 is an in-process accessor), and every byte it reads was already admitted and bounded by #2149. The reverse direction — the prompt bytes escaping to a client — is likewise unchanged: nothing added here emits, and #2152 owns the read-back on the wire and inherits the accessor rather than the file.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-07
