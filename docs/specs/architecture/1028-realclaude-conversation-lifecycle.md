# Spec — #1028 test(e2e-realclaude): conversation lifecycle against a live session

**Ticket:** #1028 (split from #963). **Size:** S. **Security-sensitive:** no
(test-only liveness capstone — the fake tier owns the verbs' security
properties; the parent #963 carries no `security-sensitive` label, which is
authoritative for its children).

## One-paragraph summary

Add ONE new test to `internal/e2e/realclaude/` that drives the conversation
lifecycle — create → rename → archive → unarchive → delete — over the Noise v2
wire against a freshly-spawned daemon running real `claude --model haiku`, and
proves the created conversation is live by streaming a `send_message`
`assistant_delta` for it. The test is **test-only**: it touches zero production
source files and reuses the `#854`/`#997` harness already in the package. All
verb-drive and drain machinery already exists; the only genuinely new code is a
~15-line on-disk registry reader for the "entry gone after delete" assertion.

## Context

The `e2e_realclaude` tier has exactly one interactive-daemon liveness test today
per verb-family:
`interactive_bootstrap_liveness_test.go` (#854, bootstrap-bound conversation) and
`interactive_per_conversation_liveness_test.go` (#997, create-over-the-wire).
**No conversation-management verb has ever run against real claude.** The fake
tier already covers the verbs' detailed shape end-to-end at the daemon boundary
(`relay_v2_rename_test.go` #974, `relay_v2_delete_test.go` #975,
`relay_v2_archive_test.go` #976). Per the 2026-07-08 operator policy —
"real-claude e2e in a pre-ship gate, always; fake-mock e2e is necessary but not
sufficient" — this ticket extends coverage to the real interactive stack:
fakephone → fakerelay → spawned `pyry` daemon → real `claude --model haiku` over
the Noise v2 wire. It is deliberately **liveness / observable-state shaped**: it
proves the real stack executes the verbs at all, not their fine-grained
semantics (the fake tier owns that).

## Design

### New file

`internal/e2e/realclaude/interactive_conversation_lifecycle_test.go`
(`//go:build e2e_realclaude`, `package realclaude`). One test function plus one
small on-disk helper. **No production source file is created or modified.**

### The single test: `TestInteractiveConversationLifecycle`

Stand up the live stack with the existing harness, then drive the lifecycle on
ONE conversation over the SAME encrypted channel (request/reply strictly
alternate, so the Noise nonces stay in lockstep — the drain helpers already
decrypt every `noise_msg` in receive order to preserve that invariant).

Step-by-step (each "seal→drain" uses the existing generic
`sealEnvelope`/`drainForReply`; request ids are unique and monotonic so
`in_reply_to` correlation is unambiguous):

| # | reqID | Action | Verb / helper | Observable assertion (AC #2) |
|---|-------|--------|---------------|------------------------------|
| 1 | 2 | **create** | `createConversationViaPhone(…, 2, nil)` → returns `convID` | non-empty `conversation_created` id (helper already asserts); the created conversation is live (create mints+binds+persists its session before replying) |
| 2 | 3 | **rename** | seal `TypeRenameConversation{convID, name:"renamed-<nonce>"}` → drain `TypeConversationUpdated` | reply `ConversationUpdatedPayload.ID==convID` and `*Name==newName` |
| 3 | 4 | **archive** | seal `TypeArchiveConversation{convID}` → drain `TypeConversationUpdated` | reply `IsArchived==true` |
| 4 | 5 | **unarchive** | seal `TypeUnarchiveConversation{convID}` → drain `TypeConversationUpdated` | reply `IsArchived==false` |
| 5 | 6 | **liveness** | `sealSendMessage(…, 6, convID, "m-1", "Reply with a single short word. run=<nonce>")` → `drainForAssistantReply(convID, 1, perTurnReplyBudget)` | a non-empty streamed `assistant_delta` for `convID` (AC #3). Claude's words are **never** asserted (substrate-guard safe). |
| 6 | 7 | **delete** | seal `TypeDeleteConversation{convID}` → drain `TypeConversationDeleted` | reply `ConversationDeletedPayload.ID==convID` **and** `readConversationIDsOnDisk(home)` no longer contains `convID` (the "entry gone" registry signal) |

`nonce := time.Now().UnixNano()` seeds the rename label and the liveness message
text so reruns differ (defeats accidental caching); nothing asserts on the
nonce's echo.

**Ordering rationale (do not reorder without cause).** The verb spine is exactly
AC #1's `create → rename → archive → unarchive → delete`. The liveness
`send_message` is inserted **between unarchive and delete** deliberately:

- The metadata verbs (rename/archive/unarchive) then run against a **quiescent**
  wire — after `create` the per-conversation claude session is spawned but idle
  (no prompt sent), so no `assistant_delta` stream is in flight. Their drains are
  fast, simple, and non-flaky.
- Only the **delete** drain has to skip the tail of the in-flight liveness turn
  (its `send_message` reply stream) before it reaches `conversation_deleted` —
  so the "drain past a live stream" complexity is isolated to exactly one step.
- Proving liveness *after* the archive round-trip is also a strictly stronger
  claim: it shows archiving/unarchiving did not tear down the live session
  (idle-timeout is 0 in `spawnBootstrapDaemon`, and archive/unarchive touch only
  the registry `is_archived` flag, never `CurrentSessionID`).

### New helper (the only genuinely new code)

```
readConversationIDsOnDisk(t *testing.T, home string) []string
```

Reads `<home>/.pyry/test/conversations.json` (the "test" instance registry —
`-pyry-name=test`), decodes `{conversations:[{id}]}` into a local anonymous
struct (mirror `relay_v2_delete_test.go`'s read-back shape), returns the ids.
Returns an empty slice if the file is absent or holds no rows. ~15 lines.
The delete step asserts `convID` is **not** among the returned ids (and, since
this test creates exactly one conversation, that the slice is now empty).

### Timeouts (spec them explicitly)

- create: `createConversationViaPhone`'s existing 60s (a real PTY spawn).
- rename / archive / unarchive: generous but quiescent — `30*time.Second` is
  ample (the registry op is instant; the margin only covers any stray
  activation-time control frame the drain skips).
- liveness `send_message`: `perTurnReplyBudget` (120s) — matches #997's first
  cold-turn budget.
- delete: generous — reuse `perTurnReplyBudget` (or ~90s) because this drain must
  outlast the in-flight liveness turn's streamed frames before the
  `conversation_deleted` reply is reached. The reply itself is instant; the
  budget is purely to drain the stream tail.

## Concurrency model

Single test goroutine drives everything synchronously; the only background
goroutine is the daemon's `cmd.Wait()` inside `spawnBootstrapDaemon` (owned by
the harness). The load-bearing invariant is **receive-nonce discipline**: every
inbound `noise_msg` MUST be decrypted in arrival order or the phone's receive
`CipherState` desyncs. Both `drainForReply` and `drainForAssistantReply` already
honor this (decrypt every `noise_msg`; skip non-`noise_msg` control frames like
rekey WITHOUT decrypting so they don't advance the nonce). Because the test only
ever reads the wire through these two helpers, the invariant holds across all
six steps for free — including where the delete drain skips the liveness turn's
streamed deltas. No new synchronization is introduced.

Note: archive/unarchive/delete on a conversation with a *live* session may, in
principle, interleave extra frames on the wire; this is a non-issue — the drain
helpers skip any frame that isn't the awaited `in_reply_to` match, and correctness
depends only on decrypt-in-order, not on exact frame counts.

## Error handling / skip behavior (AC #4)

`startPerConversationHarness` already gates the whole test: it `t.Skipf`s when
`claude` is not on PATH and `WithWorktreeAuthenticated` skips cleanly when no
credentials are present — identical to every other realclaude test. Placement
under the `e2e_realclaude` build tag wires the test into `make e2e-realclaude`
(and thus `make preship`) with no Makefile change, mirroring how #854 and #997
satisfied the same AC. No new skip logic is written.

Assertion failures use `t.Fatalf`/`t.Errorf` as the surrounding files do; the
harness's `t.Cleanup`s stop the daemon (SIGTERM→grace→SIGKILL) and remove the
socket regardless of outcome.

## Testing strategy

This ticket *is* a test. Verification is: `make e2e-realclaude` runs green with
`claude --model haiku` on PATH and credentials present, and skips cleanly
without them. The step-level assertions above are the coverage. No RED/GREEN
oracle is required or claimed — like #997, this is a **standing liveness gate**
in preship, not a deterministic regression oracle for a specific bug (the fake
tier #974/#975/#976 already owns the deterministic shape checks). Wall-clock
(AC #5): one daemon spawn + one per-conversation claude session + one live haiku
turn + four instant registry verbs ≈ the cost of #997's single default case;
the added preship time is one real-claude turn, which is reasonable.

## Scope self-check

Production source files created or modified: **0** (one new `*_test.go`, plus
this spec). New exported types: 0. Consumer call-site cascade: none. Reject
branches: none (happy-path liveness). Comfortably within `s`; no split.

## Open questions

- **Optional baseline read.** The test could also assert `convID` *is* present
  on disk immediately after create (a presence→absence bracket around delete).
  The `conversation_created` reply already proves creation, so this is
  belt-and-suspenders, not required. Developer's call; leaving it out keeps the
  test minimal.
- **Optional on-disk cross-checks for rename/archive.** AC #2 accepts the reply
  envelope OR the registry; the reply envelopes carry `Name`/`IsArchived`
  directly, so registry read-back for those verbs is redundant. Add only if the
  developer wants extra durability evidence; not required.
