# Spec #1029 — Real-claude e2e: `change_workspace` round-trips without wedging the live session

One new test file. No production change. Size **S**.

## Files to read first

| Path | Symbols | What to extract |
|---|---|---|
| `internal/e2e/realclaude/harness_daemon_test.go` | `spawnBootstrapDaemon`, `driveHandshakeInteractive`, `seedBootstrapRegistry`, `seedBoundConversation`, `sealSendMessage`, `drainForAssistantReply`, `runPyry`, `decodePairPayload`, `readPersistedServerID`, `waitBinaryHello`, `relayTestLogger`, `mustJSON` | The whole harness. Every helper this test needs is here or in the two files below. **Do not roll a new one.** |
| `internal/e2e/realclaude/interactive_stream_liveness_test.go` | `TestInteractiveStreamLiveness`, `drainForCompletedTurn`, `writeStreamInteractiveConfig` | The one-daemon / one-seeded-bound-conversation spine to copy. `drainForCompletedTurn` is the turn-1 drain. `writeStreamInteractiveConfig` is the one thing **not** to copy — see § Design. |
| `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` | `sealEnvelope`, `drainForReply`, `perTurnReplyBudget` | The generic control-verb seal + correlated-reply drain, and the real-turn budget constant. |
| `internal/e2e/realclaude/interactive_conversation_lifecycle_test.go` | `metaVerbReplyBudget`, `readConversationIDsOnDisk`, `assertConversationUpdated` | The quiescent-verb budget, and the on-disk read-back shape the new cwd reader mirrors. Its header's *ordering rationale* paragraph is the model for this file's. |
| `internal/relay/handlers/change_workspace.go` | `ChangeWorkspace` | What the handler actually does: confine → store the **resolved** realpath → eager `Save` → reply `conversation_updated`. Note it never touches `CurrentSessionID`. |
| `cmd/pyry/relay.go` | `resolveWorkspaceDir` | The injected resolver: `expandTilde` then `confineWorkdirToHome`. **Strict, non-creating** — the target directory must already exist. |
| `cmd/pyry/main.go` | `expandTilde`, `confineWorkdirToHome`, `selectInteractiveRunner` | `expandTilde` is why the request may be sent as `~/…`. `confineWorkdirToHome` is `EvalSymlinks` on both sides — the source of the realpath the reply must carry. `selectInteractiveRunner` is why no config write is needed. |
| `internal/e2e/relay_v2_change_workspace_test.go` | `TestRelayV2_ChangeWorkspace` and siblings | The fake tier that already owns the shape and the reject paths. Read it to know what **not** to re-assert here. |
| `internal/e2e/realclaude/fixtures.go` | `WithWorktreeAuthenticated`, `WithWorktree` | The credential skip and the `t.Setenv("HOME", …)` that makes `$HOME` the tempdir — which is why `t.Parallel` is banned. |

## Context

Per the 2026-07-08 operator policy, every operator-facing happy-path flow needs a
real-claude e2e in `make preship`. `change_workspace` has fake-tier coverage only.

What is genuinely real-tier-only here is narrow, and the ticket rescoped to exactly
it: the verb round-trips correctly against a **real** daemon with a **live supervised
claude child**, and does not wedge or kill that child. A scripted fake cannot regress
a real child dying.

Re-verified against `c270cca` while writing this spec: no production path reads a
stored `conv.Cwd` and spawns with it. The readers are `list_conversations`,
`archive_conversation`, `rename_conversation`, `promote_conversation` (which reads
the row's pre-existing cwd and explicitly refuses the payload's), and
`recent_workspaces` — all echo or aggregate. `create_conversation` is the only
handler that passes a non-empty spawn dir, and it reads its **payload**, not a row.
So AC4 is expected green, and a red AC4 is a finding about hidden coupling rather
than a flake. The failure message must say that.

## Design

### One new file

`internal/e2e/realclaude/interactive_change_workspace_test.go` — header
`//go:build e2e_realclaude`, `package realclaude`. Nothing else is created or
modified.

### Identifiers

Two file-local constants, in the ticket-number idiom `evictResumeBootstrapUUID`
already uses. Neither value appears anywhere in the package today (verified by
sweeping every UUID literal under `internal/e2e/realclaude/`):

```go
const (
	changeWSBootstrapUUID = "10290000-0000-4000-8000-000000000001"
	changeWSConvID        = "10290000-0000-4000-8000-000000000002"
)
```

### Do not write a config file

`TestInteractiveStreamLiveness` calls `writeStreamInteractiveConfig` before spawning.
**This test must not.** Since #1348 the empty/default `interactive_runner` selects
the stream-json runner — see the `""` arm of `selectInteractiveRunner`, and its
comment "The empty default moved from the terminal runner to the stream runner
here." Writing the config would add a moving part that no longer changes anything.
Say so in a comment, because copying the spine from `interactive_stream_liveness_test.go`
makes copying that call the default mistake.

### The spine

Sequential, no `t.Parallel` (`WithWorktreeAuthenticated` calls `t.Setenv`), so the
Noise receive nonce stays in lockstep across all four drains. Request IDs are
`1` (hello) then `2`, `3`, `4` in the order below.

1. **Skip guards + isolated HOME.** `exec.LookPath("claude")`, then
   `WithWorktreeAuthenticated`. Copy verbatim from `TestInteractiveStreamLiveness`.
2. **Workdir.** `os.MkdirAll(filepath.Join(home, "work"))` — the daemon workdir and
   the conversation's *original* cwd.
3. **Target folder.** `os.MkdirAll(filepath.Join(home, "ws-<nonce>"))`, with a
   per-run `time.Now().UnixNano()` nonce. Minted directly: `resolveWorkspaceDir`
   uses the **non-creating** confiner, so a missing target is a reject, not a
   create. Do **not** drive `create_workspace_folder` to obtain it — that verb's
   real-tier proof is `TestInteractivePerConversationLiveness_WorkspaceFolder`'s,
   and driving it here re-adds a second concern.
4. **Pair, seed, spawn, dial, handshake.** `runPyry("pair", …)` →
   `decodePairPayload` → `seedBootstrapRegistry(changeWSBootstrapUUID)` →
   `seedBoundConversation(changeWSConvID, changeWSBootstrapUUID, workdir)` →
   `fakerelay.New` → `spawnBootstrapDaemon` → `readPersistedServerID` →
   `waitBinaryHello` → `fakephone.Dial` → `driveHandshakeInteractive`. Verbatim
   from the stream-liveness spine; the seeds must land before spawn (the registries
   load once at startup).
5. **Turn 1 — the control.** `sealSendMessage(id=2, changeWSConvID, "m-1", …)` then
   `drainForCompletedTurn(…, perTurnReplyBudget)`. Two jobs, both load-bearing:
   it proves the child was alive **before** the verb (without it a red AC4 has two
   readings), and it drains the turn to its terminal `turn_state{idle}` so the verb
   runs against a quiescent wire — the same ordering rationale
   `TestInteractiveConversationLifecycle`'s header records. `drainForCompletedTurn`
   also carries the package's standing `unrecognized_message` / `rate_limited`
   alarms; inheriting them is intended, not a side effect.
6. **The verb.** `sealEnvelope(id=3, protocol.TypeChangeWorkspace,
   protocol.ChangeWorkspacePayload{ConversationID: changeWSConvID, Cwd: "~/ws-<nonce>"})`
   then `drainForReply(protocol.TypeConversationUpdated, 3, metaVerbReplyBudget)`.
   The short budget is only correct **because** step 5 drained to idle; if that
   changes, this must move to `perTurnReplyBudget`.
7. **AC2 + AC3 assertions.** See below.
8. **Turn 2 — AC4.** One `t.Logf` naming what a failure here means (see § Error
   handling), then `sealSendMessage(id=4, changeWSConvID, "m-2", …)` and
   `drainForAssistantReply(…, turn 2, perTurnReplyBudget)`.

### Why the request is sent as `~/ws-<nonce>`

AC2 requires the reply to carry the confined **realpath**, "not the raw requested
path". Sent as an absolute path, that clause is only non-vacuous where `$HOME`
happens to sit under a symlink — true for a macOS `t.TempDir()` under `/var`,
generally false on Linux, so the assertion would silently stop discriminating on
half the supported platforms.

Sending the tilde form fixes that with no extra filesystem setup and is *more*
faithful, not less: `expandTilde`'s doc records that a phone cannot know the
daemon's absolute home, so the tilde form is what a real client sends, and
`createWorkspaceFolderViaPhone` already drives `"~"` as its parent. The requested
string then differs from the resolved path on every platform, by construction.

### The assertions

- **AC2 — reply.** Decode the `conversation_updated` payload. `ID == changeWSConvID`.
  `Cwd == filepath.EvalSymlinks(filepath.Join(home, "ws-<nonce>"))`, computed in the
  test. Also assert `Cwd != "~/ws-<nonce>"` (the raw request) — this is the clause
  the tilde form makes bite. `drainForReply` already correlates `in_reply_to`; do
  not re-check it. Reuse `assertConversationUpdated` for the id + decode if it
  reads cleanly, otherwise decode inline — either is fine, do not refactor it.
- **AC3 — persistence.** One new helper (the only new code beyond the test body):

  ```go
  // readConversationCwdOnDisk returns the cwd recorded for convID in the "test"
  // instance registry (<home>/.pyry/test/conversations.json). Fatals when the
  // file or the row is absent.
  func readConversationCwdOnDisk(t *testing.T, home, convID string) string
  ```

  Mirror `readConversationIDsOnDisk`'s anonymous-struct decode; add `Cwd` to the
  row struct and select by id. Assert it equals the same `wantCwd`. The handler
  eager-`Save`s before replying, so no polling or retry is needed — a read
  straight after the reply is correct, and a poll loop here would hide a
  regression in that ordering.
- **AC4 — liveness.** A non-empty `assistant_delta` for `changeWSConvID` after the
  change. `drainForAssistantReply` is the assertion; nothing else is added.

### Anti-goal — do not attempt this, it is unbuildable

Do **not** assert that any spawned claude runs in the *new* folder, and do not
assert the child "rebound" to it. Production has no such path: a session's spawn
workdir is fixed at runner construction, and `RestartFresh` mutates only the
session id and the rotate-pending flag — it never re-reads the workdir. The AC4
delta is produced by the child still running at the conversation's **original**
cwd. It is a liveness assertion about the daemon surviving the verb, not evidence
of cwd adoption.

This must be stated in the test file's header comment, in those terms. A later
reader who mistakes AC4 for a cwd proof will "fix" it into a hang. The doc claims
on `Conversation` (the `Cwd` field comment) and on `ChangeWorkspace` that "the new
folder takes effect on the conversation's next fresh session spawn" are unbacked
by code; that is a separate production/doc defect, filed separately, and this test
must not try to compensate for it.

## Concurrency model

No goroutines are introduced. The test process is one sequential driver against one
spawned daemon over one encrypted channel; the daemon's own goroutines are exercised
but never observed directly.

The load-bearing invariant is the **Noise receive nonce**. Every binary→phone
`noise_msg` must be decrypted in arrival order or the receive `CipherState` desyncs
and every later drain fails with a decrypt error. All three drain helpers already
honour this (decrypt-and-skip, and skip non-`noise_msg` control frames *without*
decrypting). The consequence for this file: the four drains must run in the stated
order on one goroutine, and no drain may be skipped even if its result is unused.

Shutdown is `t.Cleanup` in reverse registration order: phone close, daemon
`stop` (SIGTERM → 3s grace → SIGKILL), relay close, socket dir removal, tempdir
removal. Turn 2 is not drained to `idle`, so the daemon is torn down mid-turn —
intended and already the shape `TestInteractivePerConversationLiveness_Default`
ships.

## Error handling — make each red attributable

This gate's value is entirely in *which* step goes red, so each failure must name
its own cause:

- **Turn 1 red** — the daemon/child never came up. Nothing about the verb is
  implied. `drainForCompletedTurn`'s milestone-specific fatals already say this.
- **Verb drain red** — no correlated `conversation_updated` within
  `metaVerbReplyBudget` against a quiescent wire. Either the handler rejected
  (the reply would be an `error` envelope, which `drainForReply` skips) or the
  route is not wired. Add a comment naming the reject case as the first thing to
  check, since a rejected path is invisible in a `drainForReply` timeout.
- **AC2/AC3 red** — the confine/store contract changed. Compare against
  `TestRelayV2_ChangeWorkspace`; if the fake tier is also red the fault is the
  handler, if only this one is red the fault is in `resolveWorkspaceDir`'s
  `$HOME` resolution under the tempdir HOME.
- **AC4 red** — *the finding this file exists for.* `drainForAssistantReply`'s
  built-in fatal blames the #854 fresh-daemon deadlock, which is the wrong
  explanation here. Emit a `t.Logf` immediately before the turn-2 send stating
  that turn 1 was green, so a failure here means the verb disturbed the live
  supervised session — most likely a newly-added reader of the stored `conv.Cwd`
  on the live path. One line, ordered directly above the fatal in the output.
  Do not clone the drain to reword its message.

No new error types, no new sentinels. Every failure is `t.Fatalf`/`t.Errorf`.

## Testing strategy

The deliverable *is* a test, so verification is about not shipping a test that
cannot fail:

- **It compiles under the tag.** `go vet -tags e2e_realclaude ./internal/e2e/realclaude/`.
  `make check` does **not** compile this package — build-tagged files are invisible
  to it — so vet under the tag explicitly before claiming green.
- **It runs and passes live.** `go test -tags e2e_realclaude -run TestInteractiveChangeWorkspace -v ./internal/e2e/realclaude/`
  with credentials present. Expect one daemon spawn and two real turns.
- **It skips cleanly.** Same command with `ANTHROPIC_API_KEY` and
  `CLAUDE_CODE_OAUTH_TOKEN` unset must SKIP, not fail.
- **AC2 is not vacuous.** Assert-side check: `wantCwd` must differ from the
  requested `"~/ws-<nonce>"` string. If a future edit makes the request absolute,
  that clause degrades silently on Linux — the § Why paragraph above is the note
  that prevents it.
- **AC3 is not vacuous.** Confirm the seeded row's cwd (the daemon workdir) differs
  from `wantCwd`, so the on-disk assertion cannot pass against un-mutated state.
  It does by construction (`<home>/work` vs `<home>/ws-<nonce>`); a one-line
  comment saying why is enough, no extra assertion needed.
- **No new fake-tier duplication.** The reject-no-leak and not-found paths stay in
  `internal/e2e/relay_v2_change_workspace_test.go`. If you find yourself adding an
  escaping-path case here, stop — that is out of scope.

## Open questions

- **Turn budgets.** Turn 1 is cold and gets `perTurnReplyBudget` (120s); turn 2 is
  warm and reuses it for headroom rather than a new constant. If the live run shows
  turn 2 completing in seconds, leave the budget alone — a generous ceiling on a
  liveness gate costs nothing when green.
- **`assertConversationUpdated` reuse.** It takes a verb-specific closure and fits
  AC2 exactly; use it if it reads cleanly. If the `Cwd`-plus-raw-inequality pair
  reads better inline, decode inline. Do not change the helper's signature to suit
  this file.

## Scope check

| Red line | Limit | This spec |
|---|---|---|
| New files | ≤ 3 | 1 |
| Total written LOC (prod + tests + helpers + log calls) | ≤ 600 | ~180–200 (one test file: ~45 header prose, ~15 imports, ~6 constants, ~80 test body, ~25 helper) |
| New exported types/interfaces | ≤ 5 | 0 |
| Consumer call sites needing simultaneous update | ≤ 10 | 0 (purely additive; no symbol renamed, no signature changed) |
| Acceptance criteria | ≤ 5 | 4 |
| Error/reject branches in a state machine | ≤ 10 | 0 |
| Production source files (self-check gate) | < 5 | 0 |

No overlap with any in-flight branch: every remote `origin/feature/<n>` was diffed
against `origin/main` for the files above; none touches them.
