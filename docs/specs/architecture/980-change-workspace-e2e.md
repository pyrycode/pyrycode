# Spec: e2e change_workspace over the v2 wire (#980)

## Context

`change_workspace` (`internal/relay/handlers/change_workspace.go`) moves an existing
conversation's recorded `Cwd` to a client-chosen folder. It has handler unit tests
plus a path-confinement helper test (`cmd/pyry/change_workspace_dir_test.go`), but no
e2e at any tier (2026-07-15 review gap). Split from #961.

This verb is the one **security-sensitive** member of its rename/delete/archive
sibling family: it stores an untrusted **filesystem path** supplied by a network-paired
party. The handler confines the target to `$HOME` (fail-closed, symlink-resolved)
**before** storing it, and persists the **resolved realpath** — the value that was
confined — not the raw path (validate == store). The rejection replies carry only a
static message; no supplied bytes (path, id, decode-error text) reach the wire or the
log.

The existing tests prove confinement *at the helper* (`resolveWorkspaceDir`, in-process)
and *at the handler* (unit, with a stubbed resolver). Neither proves it holds **over the
actual wire** with the real resolver injected. This ticket closes that gap: fakephone →
fakerelay → spawned daemon, with the production `resolveWorkspaceDir` wired at
`cmd/pyry/relay.go:412`. It is the same gap-shape #949 (promote) and #974–#976
(rename/delete/archive) closed for their verbs — a handler nothing exercised end-to-end.

## Files to read first

- `internal/e2e/relay_v2_rename_test.go` (whole file, ~309 lines) — **closest template.**
  Same `conversation_updated` / `ConversationUpdatedPayload` reply, correlates
  `in_reply_to`, reads the on-disk registry back. Copy its two-subtest shape
  (pair → seed conversations.json → spawn daemon → `driveHandshakeToOpenDaemon` →
  seal → `decryptInnerEnvelope` → assert). Each subtest owns its own spawn+seed.
- `internal/relay/handlers/change_workspace.go:115-215` — the handler contract. Note the
  branch order: decode → **empty-path guard** → **confine** → `Update` (lookup+mutate) →
  `!hit` not_found. Confinement runs *before* the registry lookup (so not-found requires
  a *valid* target). `LastUsedAt` is **not** bumped (metadata edit). All reject branches
  reply a fixed static string; the path/id/err are never echoed.
- `internal/protocol/conversations_write.go:91-131` — `ChangeWorkspacePayload{ConversationID, Cwd}`
  (json `conversation_id`, `cwd`) and `ConversationUpdatedPayload{ID, IsPromoted, IsArchived, Name *string, Cwd, LastUsedAt time.Time}`.
- `cmd/pyry/relay.go:74-84` — `resolveWorkspaceDir`: `expandTilde` → `confineWorkdirToHome`
  → realpath; every failure wraps `handlers.ErrWorkspaceRejected`. Wired at `:412`.
- `cmd/pyry/change_workspace_dir_test.go` (whole file) — confiner behaviors and the
  **`filepath.EvalSymlinks` discipline** for the expected value: within-`$HOME` existing
  dir → its realpath; outside / non-existent / symlink-escaping → reject. Reuse the
  "create dir, `want = EvalSymlinks(dir)`" and "sibling temp dir = outside `$HOME`" idioms.
- `internal/relay/handlers/change_workspace_test.go:142-260` — the in-process assertions
  to mirror at the wire tier: reply `Cwd == resolved realpath`, `LastUsedAt.Equal(seed)`,
  and the reject "state unchanged, no leak" subtest (`TestChangeWorkspace_Rejected_LeavesStateUnchangedNoLeak`).
- `internal/e2e/harness.go:853` — `shortHome`: the spawned daemon's `$HOME` is
  `os.MkdirTemp("", "p-301-*")`. Also the `RunBareIn` / `StartInWithEnv` /
  `readPersistedServerID` / `waitBinaryHello` patterns the template already uses.
- `internal/protocol/codes.go:10,22` — `CodeProtocolMalformed = "protocol.malformed"`,
  `CodeConversationNotFound = "conversation.not_found"`.
- `internal/protocol/handshake.go:81` — `ErrorPayload{Code, Message, Retryable, RetryAfterS}`.
- `internal/e2e/per_conversation_eviction_test.go:338` — `createConversationViaPhone`
  (an *alternative* seed path; see Design § Seeding for why this spec does **not** use it).

## Design

One new file, build tag `e2e`, package `e2e`:
`internal/e2e/relay_v2_change_workspace_test.go`. No production code changes — the verb
is already wired at `cmd/pyry/relay.go:412`. Test-only.

Top-level `TestRelayV2_ChangeWorkspace` with three `t.Run` subtests, each a self-contained
spawn+seed (mirrors the rename test's per-subtest isolation so each on-disk assertion
reads a pristine, never-cross-contaminated registry):

1. `v2_enabled_change_workspace_round_trip` — happy path (AC #1, #2).
2. `v2_enabled_change_workspace_rejected_no_leak` — path-confinement rejection over the
   wire (AC #3, #4).
3. `v2_enabled_change_workspace_not_found` — unknown conversation id (AC #5).

Each subtest inlines the shared boilerplate the template already establishes (do **not**
extract a helper — the promote/rename/delete tests deliberately inline): `shortHome` →
`RunBareIn "pair"` → `decodePairPayload` + decode pubkey → seed `conversations.json` →
`fakerelay.New` → `StartInWithEnv(PYRY_ALLOW_INSECURE_RELAY=1, PYRY_MOBILE_V2=1)` →
`readPersistedServerID` → `waitBinaryHello` → `fakephone.Dial` →
`driveHandshakeToOpenDaemon` → seal a `change_workspace` `Envelope` with `initSend.Encrypt`
→ `sendNoiseMsg` → `decryptInnerEnvelope(readInnerFrame(...), initRecv)`.

### Seeding: direct `conversations.json` write, not `createConversationViaPhone`

The ticket notes `createConversationViaPhone` as an option; this spec **overrides that to
the rename template's direct-seed** (an architect design call). `createConversationViaPhone`
mints a live claude session (`Pool.Activate`, 15s budget), bumps `LastUsedAt` to *now*, and
sets `Cwd` to the daemon default — none of which is controllable. Two of this ticket's
assertions need a **known** seed value:

- The happy path asserts `LastUsedAt.Equal(seededTime)` (change_workspace is a metadata
  edit and must **not** bump it) — needs a deterministic seed timestamp.
- The reject path asserts the on-disk `Cwd` is **unchanged** — needs a known old `Cwd`
  to compare against.

Direct-seed (write `conversations.json` before `StartInWithEnv`, exactly as
`testV2DaemonRenameRoundTrip` does at `relay_v2_rename_test.go:71-78`) gives both, needs no
claude spawn, and is the closest-template path. A direct-seeded row is a fully valid
registry row the daemon loads on boot; `change_workspace` only edits the recorded `Cwd`,
so no live session is required for faithfulness.

### Request / reply contracts

Request `Envelope`: `Type: protocol.TypeChangeWorkspace`, `Payload:
protocol.ChangeWorkspacePayload{ConversationID, Cwd}`.

- Happy → `protocol.TypeConversationUpdated` + `protocol.ConversationUpdatedPayload`.
- Reject / not-found → `protocol.TypeError` + `protocol.ErrorPayload`.

### The realpath discipline (load-bearing)

The stored/replied `Cwd` is `EvalSymlinks(target)`, **not** the literal string sent. macOS
temp dirs sit under symlinked roots (`/var` → `/private/var`), so the confined realpath
routinely differs from the requested path. Every `Cwd` assertion compares against
`filepath.EvalSymlinks(targetDir)`, computed in the test after `MkdirAll` — never against
the raw requested path. (This is the exact discipline `change_workspace_dir_test.go` uses:
`want, _ := filepath.EvalSymlinks(proj)`.)

## Concurrency model

None added. Reuses the template's synchronous request→reply drive over one Noise channel:
seal a frame, read exactly one sealed reply (non-interactive v2 path — one reply per
request), decrypt, assert. No goroutines, no channels beyond the harness's existing
`readInnerFrame` timeout. All spawns torn down via `t.Cleanup` (`h.Stop`, `fr.Close`,
`phone.Close`), as the template does.

## Error handling

Test-only; "error handling" here is the reject/not-found assertions themselves (below).
The daemon-side error contract is unchanged and already unit-tested. Standard `t.Fatalf`
on any harness step failure (pair exit code, decode, timeout) per the template.

## Testing strategy

Three subtests, described as scenarios (developer writes the Go in the project idiom;
mirror the rename test's assertion style):

### 1. `v2_enabled_change_workspace_round_trip` (AC #1, #2)

- **Seed:** one row — `id=convID`, `name=some-name`, `cwd=home` (the *old* workspace),
  `is_promoted=true`, `is_archived` absent (false), `last_used_at=seededTS`. Promoted+named
  so "preserved" is a real check (a handler that dropped `IsPromoted` would be caught).
- **Target:** create `newWS := filepath.Join(home, "projects", "app")` via
  `os.MkdirAll(newWS, 0o700)` **before** the send. Compute `wantCwd :=
  filepath.EvalSymlinks(newWS)`.
- **Send:** `change_workspace{ConversationID: convID, Cwd: newWS}`, `reqID` e.g. 61.
- **Assert reply:** `Type == TypeConversationUpdated`; `InReplyTo == &reqID`;
  decoded `ConversationUpdatedPayload`: `ID == convID`, `Cwd == wantCwd` (the confined
  realpath, **not** `newWS`), `IsPromoted == true` (preserved), `IsArchived == false`
  (preserved), `Name` preserved, `LastUsedAt.Equal(seededTime)` (metadata edit — not
  bumped; compare with `.Equal`, never `==`).
- **Assert disk:** read `conversations.json` back; the row's `cwd == wantCwd` (the daemon
  eager-Saves before replying, so the row is current when the reply lands — same as rename).

### 2. `v2_enabled_change_workspace_rejected_no_leak` (AC #3, #4)

- **Seed:** one row — `id=convID`, `cwd=home` (a valid old workspace), plus `is_promoted`,
  `last_used_at`. The known old `cwd` is the "unchanged" baseline.
- **Escaping target:** `escaping := t.TempDir()` — an **existing, resolvable** dir under the
  test process's temp root, which is **outside** the daemon's `p-301-*` `$HOME` (siblings,
  not ancestor/descendant). This exercises the *"escapes `$HOME`"* branch specifically:
  existing (not the unresolvable branch) and non-empty (not short-circuited by the
  empty-path guard — the Technical Notes' non-vacuity requirement). The confiner resolves
  it and finds it outside `$HOME` → reject.
- **Send:** `change_workspace{ConversationID: convID, Cwd: escaping}`, `reqID` e.g. 62.
- **Assert reply:** `Type == TypeError`; `InReplyTo == &reqID`; decoded `ErrorPayload`:
  `Code == protocol.CodeProtocolMalformed`; `Message == "workspace directory not allowed"`
  (the static `msgChangeWorkspaceRejected`; the const is unexported in `package handlers`,
  so assert the literal — a regression that echoed the path would change `Message`).
- **Assert no leak (AC #4 — the containment property this ticket exists to prove):** neither
  `escaping` **nor** `filepath.EvalSymlinks(escaping)` appears as a substring of
  `string(errReply.Payload)`. (The confine err names the *resolved* path; the handler drops
  it, so neither form may surface.) This is the load-bearing security assertion.
- **Assert disk (state unchanged):** read `conversations.json` back; the row's `cwd` is
  **still** the seeded `home` (a buggy handler that stored the escaping path would flip
  this — the assertion is non-vacuous because seeded `cwd != escaping`).

### 3. `v2_enabled_change_workspace_not_found` (AC #5)

- **Seed:** one row with `id=seededConvID`. The request targets a **different**,
  absent id.
- **Target:** a **valid** in-`$HOME` dir (`os.MkdirAll(filepath.Join(home, "ws"), 0o700)`).
  Confinement runs **before** the registry lookup, so the target must pass confinement for
  execution to reach the `!hit` not_found branch (a rejected target would return
  `protocol.malformed` first, masking not_found).
- **Send:** `change_workspace{ConversationID: absentConvID, Cwd: validTarget}`, `reqID`
  e.g. 63.
- **Assert reply:** `Type == TypeError`; `InReplyTo == &reqID`;
  `Code == protocol.CodeConversationNotFound`; `Message` the static "conversation not found".
- **Assert disk:** the seeded `seededConvID` row is intact (id + cwd unchanged) — the
  handler replies before any `Save`, so the file is pristine.

### RED-on-main guard

Removing `TypeChangeWorkspace`'s registration at `cmd/pyry/relay.go:412` would drop the verb
to the no-handler `protocol.unsupported` arm; the happy path's `want conversation_updated`
assertion fails. The test is a live guard on that wiring, same as the rename test guards
its registration.

### Run

`make e2e` green with `-race` (AC #6). The e2e target already runs `-tags e2e -race`.

## Open questions

None. The verb, resolver, and all harness helpers (`driveHandshakeToOpenDaemon`,
`decryptInnerEnvelope`, `readInnerFrame`, `sendNoiseMsg`, `mustJSON`, `decodePairPayload`,
`StartInWithEnv`, `waitBinaryHello`, `readPersistedServerID`, `relayTestLogger`,
`shortHome`) exist and are exercised by the rename template. This spec is additive test-only.

## Security review

This ticket is `security-sensitive` because it certifies a **path-confinement containment
property over the wire** — the same design surface that makes `change_workspace` the one
security-sensitive member of its verb family (it stores an untrusted filesystem path from a
network-paired party). The label tracks the design surface: the tests assert that
confinement holds end-to-end and that no attacker-supplied path bytes leak on the wire.
Walking the categories:

**Trust boundaries / input validation.** No new untrusted input reaches production — this
is test-only. The production confinement seam (`resolveWorkspaceDir` →
`confineWorkdirToHome`, fail-closed, symlink-resolved, non-creating) is **unchanged**; this
ticket adds a *caller* (the e2e), not a code path. The escaping input is constructed by the
test itself (a sibling temp dir outside the daemon's `$HOME`), sealed under the real Noise
channel, and driven through the real wired resolver — so the test faithfully models the
network-paired-party threat rather than a stubbed one.

**The load-bearing concern — non-vacuity of the confinement assertions.** The security
*value* of this ticket is that a confinement regression cannot ship green. Three design
choices make each assertion non-vacuous:

- *Reject reaches the confiner.* The escaping target is **non-empty and existing**, so it
  passes the empty-path guard (which would otherwise short-circuit with a different
  malformed reply) and passes the "resolvable" check — the reject is caused specifically by
  *escaping `$HOME`*, the exact property under test. A vacuous version (empty or
  non-existent path) would pass the test while proving nothing about the `$HOME` bound.
- *State-unchanged is a real check.* The seeded old `cwd` (`home`) deliberately differs from
  the escaping path, so a handler that wrongly stored the escaping path flips the on-disk
  assertion. Confinement that "validated but stored the raw path anyway" (the
  validate≠store bug) is caught here.
- *No-leak checks both forms.* AC #4 asserts neither the raw escaping path **nor** its
  `EvalSymlinks` realpath appears anywhere in the decrypted reply payload. The confine error
  names the *resolved* path; the handler drops it and replies a static string. A regression
  that echoed the confine err (the exact `create_conversation` divergence the handler doc
  warns against — Divergence 2) would surface the resolved path and fail this check. This is
  the containment property the ticket exists to prove.

**Info leak — the wire and the log.** The wire is asserted directly (no-leak substring
check above). The daemon log (which the handler also keeps free of path/id/err bytes on the
reject branch, per Divergence 1 & 2) is a production behavior already fixed by the handler
and out of scope for a wire-tier e2e; asserting it would require log capture from `package
e2e` (not available) and belongs to the in-process unit tests, which already cover it. No
secret or key material is involved in this verb.

**Realpath discipline as a correctness *and* security control.** Persisting the resolved
realpath (not the raw path) is what makes the stored value idempotent under every downstream
re-confinement (the next fresh spawn's `resolveSpawnDir`). The happy-path assertion pins
`Cwd == EvalSymlinks(target)` on both the reply and disk, certifying validate==store holds
over the wire — a handler that stored the raw path would pass a naive `Cwd == requested`
assertion but fail this one.

**DoS / resource.** None. Three spawns, each torn down via `t.Cleanup`; one request/reply
per subtest; no unbounded allocation.

**Verdict: PASS.** Test-only, no production change; the design is non-vacuous on every
confinement property it certifies (reject-reaches-confiner, state-unchanged, no-leak of
both path forms, realpath==stored), and faithfully drives the real wired resolver over the
encrypted channel rather than a stub.
