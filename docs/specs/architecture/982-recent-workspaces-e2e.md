# Spec: e2e recent_workspaces over the v2 wire (#982)

## Context

`recent_workspaces` (`internal/relay/handlers/recent_workspaces.go`) answers a
`recent_workspaces` request with a `recent_workspaces_list` reply: it folds the
conversations registry's `Cwd` values into the distinct set of workspace folders —
each distinct `Cwd` appears exactly once, carrying the **most-recent** `LastUsedAt`
across the conversations that share it, ordered most-recent-first (tie broken by
`Path` ascending). It has handler unit tests but no e2e at any tier (2026-07-15
review gap). Split from #961.

This verb is the **NOT-security-sensitive** member of its workspace-verb family. Its
two siblings (#980 `change_workspace`, #981 `create_workspace_folder`) each consume an
untrusted **filesystem path** from a network-paired party and are `security-sensitive`.
`recent_workspaces` consumes **no** untrusted input: the request payload is empty by
spec, the reply is derived purely from server-side registry state, and there is **no
error path** — an empty registry yields `"workspaces":[]` (not an error), and a
conversation with an empty `Cwd` simply contributes no entry. No path is confined,
resolved, or created. There is nothing to leak and no confinement boundary to certify.
This ticket therefore has no `security-sensitive` label and no security-review section.

The existing tests prove the fold/dedup/sort *in process*. Neither proves it holds
**over the actual wire** — request sealed under the real Noise channel, decoded by the
real dispatcher, handler wired at `cmd/pyry/relay.go:420`, reply decrypted by the phone.
This ticket closes that gap: fakephone → fakerelay → spawned daemon. Same gap-shape #949
(promote) and #974–#976 / #980 / #981 (rename/delete/archive/change/create) closed for
their verbs — a handler nothing exercised end-to-end.

## Files to read first

- `internal/e2e/relay_v2_rename_test.go` (whole file, ~309 lines) — **closest NOT-sec
  template.** Copy its per-subtest boilerplate verbatim: `shortHome` → `RunBareIn "pair"`
  → `decodePairPayload` + base64-decode pubkey → seed `conversations.json` →
  `fakerelay.New` → `StartInWithEnv(PYRY_ALLOW_INSECURE_RELAY=1, PYRY_MOBILE_V2=1,
  -pyry-relay=…/v2/server)` → `readPersistedServerID` → `waitBinaryHello` →
  `fakephone.Dial` → `driveHandshakeToOpenDaemon` → seal an `Envelope` with
  `initSend.Encrypt` → `sendNoiseMsg` → `decryptInnerEnvelope(readInnerFrame(...), initRecv)`.
  **Diverge from it in two ways:** (a) no not-found / error subtest — this verb has no
  error path; (b) **no on-disk reassertion** — `recent_workspaces` is a pure read and
  Saves nothing, so there is no registry mutation to read back (drop the entire
  `os.ReadFile(convPath)` + `onDisk` block the rename test uses).
- `internal/relay/handlers/recent_workspaces.go:44-77` — the handler contract the test
  asserts: fold each non-empty `Cwd` → max `LastUsedAt`; skip `strings.TrimSpace(Cwd)==""`;
  `sort.SliceStable` most-recent-first then `Path` ascending; reply
  `protocol.TypeRecentWorkspacesList`. Archived conversations are **included** by design
  (#888) — a folder is not "un-used" by archiving its conversation; irrelevant to this
  test's seeds but note it if tempted to seed an archived row.
- `internal/protocol/workspace.go:32-58` — the three payload types: `RecentWorkspacesPayload{}`
  (empty request body — send `mustJSON(t, protocol.RecentWorkspacesPayload{})`, marshals to
  `{}`), `RecentWorkspacesListPayload{Workspaces []RecentWorkspace}` (reply; slice always
  non-nil so empty marshals `[]` not `null`), and `RecentWorkspace{Path string "path";
  LastUsedAt time.Time "last_used_at"}`.
- `cmd/pyry/relay.go:420` — the registration
  `protocol.TypeRecentWorkspaces: handlers.RecentWorkspaces(w.convReg)`. The test is a
  live RED-on-main guard on this line (see § RED-on-main guard).
- `internal/e2e/relay_v2_promote_test.go:36-63` — the direct-seed idiom:
  `convPath := filepath.Join(home, ".pyry", "test", "conversations.json")` (the `test`
  segment comes from `-pyry-name=test` in the pair call), then `os.WriteFile(convPath,
  convJSON, 0o600)` **before** `StartInWithEnv`. This spec seeds the same way.

> Not needed (unlike the sec siblings): `internal/protocol/codes.go`,
> `internal/protocol/handshake.go` (`ErrorPayload`), `filepath.EvalSymlinks`,
> `os.MkdirAll`. There is no error reply and no path resolution — `Cwd` values are
> opaque strings the handler never touches the filesystem for.

## Design

One new file, build tag `e2e`, package `e2e`:
`internal/e2e/relay_v2_recent_workspaces_test.go`. **No production code changes** — the
verb is already wired at `cmd/pyry/relay.go:420`. Test-only.

Top-level `TestRelayV2_RecentWorkspaces` with two `t.Run` subtests, each a self-contained
spawn+seed (mirrors the rename test's per-subtest isolation):

1. `v2_enabled_recent_workspaces_ordered_deduped` — AC #1, #2, #3, and the empty-`Cwd`
   exclusion facet of AC #4.
2. `v2_enabled_recent_workspaces_empty_registry` — the empty-registry facet of AC #4.

Each subtest inlines the shared boilerplate the template already establishes. **Do not
extract a helper** — the promote/rename/delete/change/create tests all deliberately inline;
the Technical Notes reaffirm it for this ticket ("Inline setup; do not extract a shared
helper").

### Seeding: direct `conversations.json` write, not `createConversationViaPhone`

The ticket offers `createConversationViaPhone` as an option; this spec uses the
rename/promote template's **direct-seed** instead (an architect design call, same as #980).
The assertions need **known, controlled** `Cwd` strings and **known** `LastUsedAt` timestamps
to prove ordering and dedup. `createConversationViaPhone` mints a live claude session
(`Pool.Activate`, 15s budget), sets `Cwd` to the daemon default, and bumps `LastUsedAt` to
*now* — none controllable. Direct-seed (write `conversations.json` before `StartInWithEnv`,
exactly as `testV2DaemonRenameRoundTrip` does at `relay_v2_rename_test.go:71-78`) gives
deterministic `Cwd`/`LastUsedAt` values, needs no claude spawn, and is the closest-template
path. A direct-seeded row is a fully valid registry row the daemon loads on boot;
`recent_workspaces` only reads `Cwd`/`LastUsedAt`, so no live session is required.

**`Cwd` values are opaque strings** — unlike the sec siblings, `recent_workspaces` never
resolves or confines them, so seed literal paths like `/ws/alpha`, `/ws/beta`. No
`os.MkdirAll`, no `EvalSymlinks`. Assert reply `Path` against the literal seeded string.

### Request / reply contracts

Request `Envelope`: `Type: protocol.TypeRecentWorkspaces`, `Payload:
mustJSON(t, protocol.RecentWorkspacesPayload{})` (empty body, marshals `{}`).

Reply (always, no error branch): `Type: protocol.TypeRecentWorkspacesList`, `InReplyTo`
= request id, `Payload` decodes into `protocol.RecentWorkspacesListPayload`.

### `time.Time` discipline (load-bearing for dedup + ordering)

`LastUsedAt` crosses the wire as a real `time.Time`; monotonic-clock reading strips on JSON
marshal. Every timestamp assertion compares with `.Equal`, **never** `==` or
`reflect.DeepEqual`. Parse each seeded RFC3339 timestamp once (`time.Parse(time.RFC3339, …)`)
and compare the reply's `LastUsedAt` against the parsed value. Ordering is proven with
distinct timestamps, so there is no tie and no flake; the tie-break-by-`Path` branch is not
an AC and is left unexercised (a distinct-timestamp seed keeps the ordering assertion
deterministic).

## Concurrency model

None added. Reuses the template's synchronous request→reply drive over one Noise channel:
seal one frame, read exactly one sealed reply (non-interactive v2 path — one reply per
request), decrypt, assert. No goroutines, no channels beyond the harness's existing
`readInnerFrame` timeout. All spawns torn down via `t.Cleanup` (`h.Stop`, `fr.Close`,
`phone.Close`), as the template does.

## Error handling

Test-only, and the verb itself has **no error path** — the empty case is a success reply
with an empty list, not an error. "Error handling" here is only the standard `t.Fatalf` on
any harness step failure (pair exit code, decode, timeout), per the template.

## Testing strategy

Two subtests, described as scenarios (developer writes the Go in the project idiom; mirror
the rename test's assertion style, minus the disk-reassertion block).

### 1. `v2_enabled_recent_workspaces_ordered_deduped` (AC #1, #2, #3, + empty-`Cwd` facet of #4)

**Seed** `conversations.json` with four rows (choose UUIDs like the template; timestamps
strictly ordered `T1 < T2 < T3 < T4`, all RFC3339 e.g. `2026-01-01T00:00:0{1..4}Z`):

| id     | cwd        | last_used_at | purpose                                            |
|--------|------------|--------------|----------------------------------------------------|
| conv-1 | `/ws/alpha`| T1           | shares `/ws/alpha` with conv-2 → dedup (older)      |
| conv-2 | `/ws/alpha`| T3           | shares `/ws/alpha` with conv-1 → dedup (newer, wins)|
| conv-3 | `/ws/beta` | T2           | second distinct folder                             |
| conv-4 | `` (empty) | T4 (newest)  | empty `Cwd` → contributes NO entry (excluded)      |

Seed `conv-4` with the **newest** timestamp deliberately: if the handler failed to skip
empty `Cwd`, an empty-path entry would sort **first** and both the `len==2` and the
`Workspaces[0].Path=="/ws/alpha"` assertions would fail — so the exclusion check is
non-vacuous.

- **Send:** `recent_workspaces` (empty payload), `reqID` e.g. 71.
- **Assert reply envelope:** `Type == protocol.TypeRecentWorkspacesList`; `InReplyTo != nil
  && *InReplyTo == reqID`.
- **Decode** `RecentWorkspacesListPayload`. Assertions:
  - `len(Workspaces) == 2` — one entry per distinct non-empty `Cwd`; `/ws/alpha` deduped to
    one row, empty-`Cwd` `conv-4` excluded (AC #2 dedup-to-distinct, AC #4 empty-`Cwd` facet).
  - **Ordering (AC #2):** `Workspaces[0].Path == "/ws/alpha"` (carries T3), `Workspaces[1].Path
    == "/ws/beta"` (carries T2). Most-recent-first: T3 > T2.
  - **Dedup carries the more-recent (AC #3):** `Workspaces[0].LastUsedAt.Equal(T3)` — the
    newer of conv-1/conv-2, **not** T1. A handler that kept the first-seen or older timestamp
    fails here.
  - `Workspaces[1].LastUsedAt.Equal(T2)`.
  - No entry has an empty `Path` (belt-and-suspenders on the empty-`Cwd` exclusion; the
    `len==2` check already implies it).

### 2. `v2_enabled_recent_workspaces_empty_registry` (empty-registry facet of AC #4)

- **Seed:** `conversations.json` = `{"conversations":[]}` (explicit empty, deterministic).
- **Send:** `recent_workspaces` (empty payload), `reqID` e.g. 72.
- **Assert reply envelope:** `Type == protocol.TypeRecentWorkspacesList` (a **success**
  reply, not `TypeError` — proves empty→`[]` is not an error path); `InReplyTo` correlated.
- **Decode** `RecentWorkspacesListPayload`. Assertions:
  - `Workspaces != nil` **and** `len(Workspaces) == 0` — the payload marshals `"workspaces":[]`,
    not `null`. Checking non-nil pins the "always non-nil slice" contract
    (`workspace.go:43`); a handler that returned `nil` would marshal `null` and fail the
    non-nil check even though `len` would still be 0.

### RED-on-main guard

Removing `TypeRecentWorkspaces`'s registration at `cmd/pyry/relay.go:420` drops the verb to
the no-handler `protocol.unsupported` arm; subtest 1's `want recent_workspaces_list`
assertion fails (it would receive `TypeError` / `protocol.unsupported`). The test is a live
guard on that wiring, same as the rename test guards its registration.

### Run

`make e2e` green with `-race` (AC #5). The e2e target already runs `-tags e2e -race`.

## Open questions

None. The verb and every harness helper (`driveHandshakeToOpenDaemon`,
`decryptInnerEnvelope`, `readInnerFrame`, `sendNoiseMsg`, `mustJSON`, `decodePairPayload`,
`StartInWithEnv`, `waitBinaryHello`, `readPersistedServerID`, `relayTestLogger`,
`shortHome`, `RunBareIn`, `fakerelay.New`, `fakephone.Dial`) exist and are exercised by the
rename/promote templates. This spec is additive test-only.
