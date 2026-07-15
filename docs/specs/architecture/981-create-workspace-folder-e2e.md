# Spec: e2e create_workspace_folder over the v2 wire (#981)

## Context

`create_workspace_folder` (`internal/relay/handlers/create_workspace_folder.go`)
**WRITES TO THE HOST FILESYSTEM** a directory named by two untrusted fields —
`parent` and `name` — supplied by a network-paired party. It has handler unit tests
plus a path-confinement helper test (`cmd/pyry/create_workspace_folder_dir_test.go`),
but no e2e at any tier (2026-07-15 review gap). Split from #961.

This verb is **security-sensitive**: it is the sibling family's file-creating write
verb, and it holds two independent, fail-closed, **belt-and-suspenders (different
fabric)** guards — two distinct code checks, not one gate twice:

1. **Confinement to `$HOME`.** The resolved target is confined to `$HOME` after
   symlink resolution; any escaping path is rejected **before** any directory is
   created (`confineWorkdirToHomeCreating`, containment check #1 runs before
   `MkdirAll` — `cmd/pyry/main.go:530`).
2. **Name-shape guard.** The new folder lands **directly under** the supplied parent:
   a name that is empty, absolute, contains a path separator, or contains `..` is
   rejected by `validWorkspaceFolderName` (pure string check, no filesystem access).
   Confinement alone does NOT give this — a name like `sub/dir` stays inside `$HOME`
   yet is not directly under the parent.

The existing tests prove these *at the helper* (`resolveWorkspaceFolder`, in-process)
and *at the handler* (unit, with a stubbed resolver). Neither proves they hold **over
the actual wire** with the real resolver injected. This ticket closes that gap:
fakephone → fakerelay → spawned daemon, with the production `resolveWorkspaceFolder`
wired at `cmd/pyry/relay.go:413`. Same gap-shape #949 (promote), #974–#976
(rename/delete/archive), and #980 (change_workspace) closed for their verbs — a
handler nothing exercised end-to-end — carrying #980's security twist (a
path-confinement containment property certified over the encrypted channel).

## Files to read first

- `internal/e2e/relay_v2_change_workspace_test.go` (whole file, ~487 lines) —
  **closest template** (just-shipped #980, identical security shape: path confinement
  over the wire). Copy its per-subtest spawn shape (pair → spawn daemon →
  `driveHandshakeToOpenDaemon` → seal via `initSend.Encrypt` → `sendNoiseMsg` →
  `decryptInnerEnvelope(readInnerFrame(...), initRecv)`), its `t.TempDir()`-outside-
  `$HOME` escaping idiom, its `filepath.EvalSymlinks` canonicalisation, and its **dual
  no-leak check** (`strings.Contains(replyBytes, raw)` AND `strings.Contains(replyBytes,
  resolved)` both asserted absent — this is AC #4). **Two deliberate departures:** this
  verb consumes NO conversations registry, so seed **no** `conversations.json`; and the
  post-condition is on the **filesystem** (dir exists / absent under the temp HOME), not
  a registry read-back.
- `internal/relay/handlers/create_workspace_folder.go` (whole file, ~202 lines) — the
  handler contract. Branch order: decode → **empty-parent guard** → **name-shape guard**
  → **resolve (confine+create)** → success. The two independent guards and their four
  static messages (all unexported in `package handlers` — the e2e asserts the literal
  strings): `"malformed create_workspace_folder payload"`, `"workspace parent path must
  not be empty"`, `"workspace folder name must be a single path element"` (bad name),
  `"workspace folder not allowed"` (confine reject). All reject branches reply a fixed
  static string; parent/name/err are never echoed.
- `internal/relay/handlers/create_workspace_folder_test.go:106-332` — the in-process
  assertions to mirror at the wire tier: success replies `workspace_folder_created`
  carrying the resolver's realpath; confine-reject → malformed static, no marker leak;
  bad-name inputs (`"a/b", "../x", "..", "/abs", "sub/dir"`).
- `internal/protocol/workspace.go:5-30` — `CreateWorkspaceFolderPayload{Parent, Name}`
  (json `parent`, `name`) and `WorkspaceFolderCreatedPayload{Path}` (json `path`). The
  reply payload has NO `time.Time` field — no `.Equal` discipline needed here.
- `cmd/pyry/relay.go:86-129` — `resolveWorkspaceFolder`: `expandTilde(parent)` →
  `filepath.Join(parent, name)` → **creating** `confineWorkdirToHomeCreating` → realpath;
  every failure wraps `handlers.ErrWorkspaceFolderRejected`. Wired at `:413`.
- `cmd/pyry/create_workspace_folder_dir_test.go` (whole file) — confiner behaviours and
  the **`EvalSymlinks` want-value discipline**: `parentReal := EvalSymlinks(parent);
  want := filepath.Join(parentReal, "new-app")` (line 23-27), the
  escape-rejected-creates-nothing idiom (line 77-99), and the "sibling temp dir = outside
  `$HOME`" pattern.
- `cmd/pyry/main.go:516-571` — `confineWorkdirToHomeCreating`: containment check #1 runs
  on the symlink-resolved candidate **BEFORE** any `MkdirAll`; on escape, nothing is
  created. This is why the confine-reject "no dir created outside `$HOME`" assertion is
  deterministic, not racy.
- `internal/e2e/relay_v2_promote_test.go` / `internal/e2e/relay_v2_rename_test.go` —
  model for decoding a NEW reply payload type (there is no existing wire-tier template for
  `workspace_folder_created`); decode `protocol.WorkspaceFolderCreatedPayload` instead of a
  conversation record.
- `internal/e2e/harness.go` — `shortHome` (~line 853; the spawned daemon's `$HOME` =
  `os.MkdirTemp` under a short root), `RunBareIn`, `StartInWithEnv`, `readPersistedServerID`,
  `waitBinaryHello`, `relayTestLogger`, `mustJSON` — the spawn/pair helpers the template
  already uses.
- `internal/protocol/codes.go:106-122` — `TypeCreateWorkspaceFolder`,
  `TypeWorkspaceFolderCreated`; and `CodeProtocolMalformed = "protocol.malformed"`
  (both reject branches map to this one code — the two guards are distinguished by their
  `Message`, not their `Code`).
- `internal/protocol/handshake.go` — `ErrorPayload{Code, Message, Retryable, RetryAfterS}`,
  the reject decode target.

## Design

One new file, build tag `e2e`, package `e2e`:
`internal/e2e/relay_v2_create_workspace_folder_test.go`. **No production code changes** —
the verb is already wired at `cmd/pyry/relay.go:413`. Test-only.

Top-level `TestRelayV2_CreateWorkspaceFolder` with three `t.Run` subtests, each a
self-contained spawn (mirrors the template's per-subtest isolation so every filesystem
assertion reads a pristine temp `$HOME`):

1. `v2_enabled_create_workspace_folder_round_trip` — happy path (AC #1, #2).
2. `v2_enabled_create_workspace_folder_rejected_no_leak` — path-confinement rejection over
   the wire (AC #3, #4).
3. `v2_enabled_create_workspace_folder_bad_name` — the **second fail-closed guard** end-to-end
   (a name with `/` or `..` is rejected by the name-shape guard with its OWN distinct static
   message; no directory created). Recommended in the ticket's Technical Notes; included
   because certifying BOTH independent guards over the wire is the whole security point of
   this verb, and the subtest is boilerplate + one send.

Each subtest inlines the shared boilerplate the template already establishes (do **not**
extract a helper — the promote/rename/change_workspace tests deliberately inline):
`shortHome` → `RunBareIn "pair"` → `decodePairPayload` + decode pubkey → `fakerelay.New` →
`StartInWithEnv(PYRY_ALLOW_INSECURE_RELAY=1, PYRY_MOBILE_V2=1, -pyry-relay=<fr>/v2/server)`
→ `readPersistedServerID` → `waitBinaryHello` → `fakephone.Dial` →
`driveHandshakeToOpenDaemon` → seal a `create_workspace_folder` `Envelope` with
`initSend.Encrypt` → `sendNoiseMsg` → `decryptInnerEnvelope(readInnerFrame(...), initRecv)`.

### No registry seed (departure from #980)

`create_workspace_folder` consumes **no** conversations registry — it creates a directory
and returns its path, nothing more (the leanest write-verb). So, unlike #980, there is **no
`conversations.json` to seed** and **no registry read-back**: every post-condition is a
filesystem assertion (`os.Stat`) under the daemon's temp `$HOME` (for the happy path) or a
`os.IsNotExist` assertion at the would-be target (for the two reject paths).

### Request / reply contracts

Request `Envelope`: `Type: protocol.TypeCreateWorkspaceFolder`, `Payload:
protocol.CreateWorkspaceFolderPayload{Parent, Name}`.

- Happy → `protocol.TypeWorkspaceFolderCreated` + `protocol.WorkspaceFolderCreatedPayload{Path}`.
- Reject (both confine + bad-name) → `protocol.TypeError` + `protocol.ErrorPayload`, `Code ==
  protocol.CodeProtocolMalformed`, distinguished by `Message`.

Correlate via `InReplyTo == &reqID` (the wire tier correlates on `in_reply_to`, not the
reply's own `ID` — the daemon dispatcher pre-advances `NextID` for the hello_ack, so do NOT
assert `env.ID`; #980 does the same).

### The realpath discipline (load-bearing)

The replied `Path` is the created folder's canonical realpath, **not** the literal string
sent. macOS temp dirs sit under symlinked roots (`/var` → `/private/var`), so the confined
realpath routinely differs from the request. Compute the expected value as the helper test
does: create the parent, `parentReal := filepath.EvalSymlinks(parent)`, `wantPath :=
filepath.Join(parentReal, name)`. Every path assertion compares against `wantPath`, never the
raw sent path.

## Concurrency model

None added. Reuses the template's synchronous request→reply drive over one Noise channel:
seal a frame, read exactly one sealed reply (non-interactive v2 path — one reply per
request), decrypt, assert. No goroutines, no channels beyond the harness's `readInnerFrame`
timeout. All spawns torn down via `t.Cleanup` (`h.Stop`, `fr.Close`, `phone.Close`).

## Error handling

Test-only; "error handling" here is the reject assertions themselves (below). The daemon-side
error contract is unchanged and already unit-tested. Standard `t.Fatalf` on any harness step
failure (pair exit code, decode, timeout) per the template.

## Testing strategy

Three subtests, described as scenarios (developer writes the Go in the project idiom; mirror
the change_workspace test's assertion style):

### 1. `v2_enabled_create_workspace_folder_round_trip` (AC #1, #2)

- **Parent:** `parent := filepath.Join(home, "workspace")` created via
  `os.MkdirAll(parent, 0o700)` **before** the send (an existing, in-`$HOME` parent). Compute
  `parentReal := filepath.EvalSymlinks(parent)` and `wantPath := filepath.Join(parentReal,
  name)` with `name := "new-app"` (a clean single element).
- **Send:** `create_workspace_folder{Parent: parent, Name: name}`, `reqID` e.g. 71. (Send the
  absolute in-`$HOME` parent — the test knows the daemon's `home` because it spawned it; the
  `~`-expansion path is already covered by the helper test and is not re-proven here.)
- **Assert reply:** `Type == TypeWorkspaceFolderCreated`; `InReplyTo == &reqID`; decoded
  `WorkspaceFolderCreatedPayload.Path == wantPath` (the confined realpath, **not** the raw
  `filepath.Join(parent, name)` — a handler/resolver that returned the un-resolved path would
  pass a naive check but fail this).
- **Assert disk (AC #2):** `os.Stat(wantPath)` succeeds and `IsDir()` — the directory actually
  exists on disk under the temp HOME workspace root.

### 2. `v2_enabled_create_workspace_folder_rejected_no_leak` (AC #3, #4)

- **Escaping parent:** `escaping := t.TempDir()` — an **existing, resolvable** dir under the
  test process's temp root, which is **outside** the daemon's `p-301-*` `$HOME` (siblings, not
  ancestor/descendant). Compute `escapingReal := filepath.EvalSymlinks(escaping)`.
- **Non-vacuity (load-bearing):** send `Name: "new-app"` (a clean single element) with the
  **non-empty** escaping parent. The clean name passes the name-shape guard and the non-empty
  parent passes the empty-parent guard, so execution reaches the resolver and the reject is
  caused **specifically by escaping `$HOME`** — the exact property under test. A vacuous
  variant (empty parent, or a bad name) would trip an *earlier* guard and prove nothing about
  the `$HOME` bound.
- **Send:** `create_workspace_folder{Parent: escaping, Name: "new-app"}`, `reqID` e.g. 72.
- **Assert reply:** `Type == TypeError`; `InReplyTo == &reqID`; decoded `ErrorPayload`: `Code ==
  protocol.CodeProtocolMalformed`; `Message == "workspace folder not allowed"` (the static
  `msgCreateWorkspaceFolderRejected` — unexported, so assert the literal); `Retryable == false`.
- **Assert no leak (AC #4 — the containment property this ticket exists to prove):** neither
  `escaping` **nor** `escapingReal` appears as a substring of `string(errReply.Payload)`. (The
  confine err names the *resolved* candidate; the handler drops it and replies a static string,
  so neither form may surface. A regression that echoed the confine err would fail this — the
  load-bearing security assertion.)
- **Assert disk (AC #3):** `filepath.Join(escapingReal, "new-app")` does **not** exist
  (`os.Stat` → `os.IsNotExist`). Confinement check #1 runs before `MkdirAll`, so nothing is
  created outside `$HOME` — deterministic, not racy.

### 3. `v2_enabled_create_workspace_folder_bad_name` (second guard, belt-and-suspenders)

- **Parent:** a valid, existing in-`$HOME` dir (`parent := filepath.Join(home, "workspace")`,
  `os.MkdirAll(parent, 0o700)`) — so "no directory created" is inspectable.
- **Bad name:** a name that trips the name-shape guard AND carries a distinctive marker, e.g.
  `name := "seg/INJECT_MARK"` (contains `/`). This proves the SECOND guard fires with its OWN
  static message, distinct from the confinement message.
- **Send:** `create_workspace_folder{Parent: parent, Name: name}`, `reqID` e.g. 73.
- **Assert reply:** `Type == TypeError`; `InReplyTo == &reqID`; `Code ==
  protocol.CodeProtocolMalformed`; `Message == "workspace folder name must be a single path
  element"` (the static `msgCreateWorkspaceFolderBadName` — **distinct** from the confinement
  message; assert the literal); `Retryable == false`.
- **Assert no leak:** `INJECT_MARK` does not appear in `string(errReply.Payload)` (the name is
  attacker-controlled and must not echo — AC #4's discipline applied to the second guard).
- **Assert disk:** `filepath.Join(parent, "seg")` does **not** exist — the guard runs before
  the resolver, so nothing under the parent is created.

### RED-on-main guard

Removing `TypeCreateWorkspaceFolder`'s registration at `cmd/pyry/relay.go:413` drops the verb
to the no-handler `protocol.unsupported` arm; the happy path's `want workspace_folder_created`
assertion fails. The test is a live guard on that wiring, same as #980 guards its
`change_workspace` registration.

### Run

`make e2e` green with `-race` (AC #5). The e2e target already runs `-tags e2e -race`.

## Open questions

None. The verb, resolver, and every harness helper (`driveHandshakeToOpenDaemon`,
`decryptInnerEnvelope`, `readInnerFrame`, `sendNoiseMsg`, `mustJSON`, `decodePairPayload`,
`StartInWithEnv`, `waitBinaryHello`, `readPersistedServerID`, `relayTestLogger`, `shortHome`)
exist and are exercised by the change_workspace/promote/rename templates. This spec is
additive test-only.

## Security review

**Verdict:** PASS

This ticket is `security-sensitive` because it certifies a **path-confinement containment
property over the wire** for a verb that WRITES TO THE HOST FILESYSTEM under two untrusted
fields supplied by a network-paired party. The label tracks the design surface: the tests
assert that both independent fail-closed guards hold end-to-end and that no attacker-supplied
`parent`/`name` bytes leak on the wire. Walking the categories:

**Findings:**

- **[Trust boundaries]** No findings. No new untrusted input reaches production — this is
  test-only. The production confinement seam (`resolveWorkspaceFolder` → `expandTilde` →
  `confineWorkdirToHomeCreating`, fail-closed, symlink-resolved, containment-before-create) is
  **unchanged**; the ticket adds a *caller* (the e2e), not a code path. The escaping input is
  constructed by the test (a sibling temp dir outside the daemon's `$HOME`), sealed under the
  real Noise channel, and driven through the real wired resolver — faithfully modelling the
  network-paired-party threat, not a stub.

- **[File operations]** The core of the ticket, non-vacuously covered. (a) *Path traversal /
  escape:* the confine-reject subtest sends an existing, non-empty parent outside `$HOME` with
  a clean name, so execution reaches the resolver and rejects **specifically on the `$HOME`
  bound** (an empty parent or bad name would trip an earlier guard and prove nothing). (b)
  *Creates-nothing-on-escape:* asserted deterministically — `confineWorkdirToHomeCreating` runs
  containment check #1 on the symlink-resolved candidate BEFORE any `MkdirAll`
  (`cmd/pyry/main.go:530`), so the escaping target provably never exists on disk (no TOCTOU
  window in the assertion). (c) *"Directly under parent" property:* the bad-name subtest
  certifies the second, independent name-shape guard (`sub/dir` stays in `$HOME` yet is not
  directly under the parent) over the wire, with its DISTINCT static message. Belt-and-
  suspenders, different fabric — both guards proven end-to-end.

- **[Error messages / logs / info leak]** The load-bearing wire assertion (AC #4): the
  confine-reject reply is checked to contain **neither** the raw escaping parent **nor** its
  `EvalSymlinks` realpath — the confine err names the *resolved* path, the handler drops it and
  replies a static string, so neither form may surface. The bad-name reply is likewise checked
  free of the injected name marker. The daemon-**log** no-leak (the handler keeps
  `parent`/`name`/`err` off every log branch, including success) is a production behaviour
  already fixed and unit-tested (`create_workspace_folder_test.go`
  `TestCreateWorkspaceFolder_Rejected_MalformedNoLeak` /
  `TestCreateWorkspaceFolder_Malformed_DoesNotLeakPayloadBytes`); asserting it here would
  require log capture from `package e2e` (not available) and is out of scope for a wire-tier
  e2e. No secret or key material is involved in this verb.

- **[Tokens / secrets / credentials]** No findings — N/A. The pairing token is consumed only
  by the harness to complete the handshake; it is neither asserted nor logged by this test, and
  no new token lifecycle is introduced.

- **[Cryptographic primitives]** No findings — N/A. The Noise_IK channel is the harness's,
  unchanged; the test seals via `initSend.Encrypt` and decrypts via the paired `initRecv`
  exactly as the shipped templates do. No new crypto, no key/nonce reuse introduced.

- **[Subprocess / external command execution]** No findings — N/A. The only subprocess is the
  daemon the harness spawns and tears down via `t.Cleanup`; no user-controlled value is passed
  to `exec.Command`, no `sh -c`.

- **[Network & I/O]** No findings — N/A. One request / one reply per subtest over the existing
  v2 relay path; no new server, listener, size limit, or timeout is introduced. Read caps and
  timeouts are the harness's and unchanged.

- **[Concurrency]** No findings. None added — three synchronous request→reply subtests, each
  spawn torn down via `t.Cleanup`; no goroutine, lock, or shared mutable state introduced.

- **[Threat model alignment]** Aligned. The verb's threat (a network-paired party writing a
  directory to the daemon host outside `$HOME`, or nested elsewhere than directly under the
  parent) is addressed by the two production guards; this ticket certifies both over the actual
  encrypted wire rather than at the helper. No relevant threat is deferred.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-15
