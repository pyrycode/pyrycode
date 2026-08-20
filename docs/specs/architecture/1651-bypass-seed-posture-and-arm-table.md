# #1651 — parameterize the bypass registry seed's stored posture, pin the three-arm launch table

Zero production files. Everything lands in `internal/e2e/realclaude/`, behind the
`e2e_realclaude` build tag.

## Files to read first

| Where | Symbol | What to extract |
|---|---|---|
| `internal/e2e/realclaude/interactive_stream_inband_bypass_revoke_test.go` | `seedBypassRegistry` | The function being parameterized. Its doc comment is AC 1's second clause; the three silent-failure modes, the `sessions.NewID` rationale and the `0600` rationale in it stay true and stay. |
| same file | `revokeSeedEntry`, `revokeSeedFile` | The on-disk shape. Note `YOLO bool \`json:"yolo"\`` with **no** `omitempty` — deliberate, see § "The one edit that must not happen". |
| same file | `TestInteractiveStream_InBandBypassRevoke_LiveChildReportsDefaultMode` | The one and only call site (confirmed by `codegraph_impact` and a repo-wide literal grep). Its instrument check B reads the posture back through `pool.DefaultSettings()`; that check is unchanged by this ticket. |
| same file | the package-level header comment at the top of the file | **Do not edit it.** Its "a seeded registry entry carrying yolo:true" sentence describes *that test's* call, which still passes `true`, so it does not go stale. |
| `internal/sessions/registry.go` | `registryEntry` | The `json:"yolo,omitempty"` tag, and the doc comment recording the fail-closed invariant (missing key → false; malformed value → whole parse fails). This is the asymmetry the seed deliberately does not copy. |
| `internal/sessions/registry.go` | `loadRegistry`, `pickBootstrap` | `(nil, nil)` for an absent or empty file — the cold-start path AC 2 has to distinguish a stored `false` from. `pickBootstrap` selects on `"bootstrap"` true. |
| `internal/sessions/pool.go` | `New` | The `pickBootstrap` block that lifts `SessionSettings{Model, Effort, YOLO}` off the entry — the warm-start seam this seed feeds. |
| `internal/sessions/pool.go` | `DefaultSettings` | Why it is the **wrong** seam for the `control_default` arm: it reports the same `YOLO: false` for a stored false and for a cold start. |
| `internal/sessions/pool.go` | `UpdateSettings` | The `merged == sess.settings` no-change early return — the gate that makes a `revoke` row seeded `false` a silent no-op in #1643. |
| `internal/sessions/session.go` | `claudeSettingsArgs` | `true` → `--dangerously-skip-permissions`; `false` → nothing at all. Over an empty base argv that is `["--dangerously-skip-permissions"]` vs `[]`. |
| `internal/sessions/id.go` | `NewID`, `ValidID` | The mint and the canonical-UUIDv4 predicate AC 2's id clause calls. `ValidID` takes a `string`, so the call is `sessions.ValidID(string(id))`. |
| `internal/sessions/settings.go` | `writeMCPSettings` | The `ValidID` gate on the warm-start id — why a hand-written id string is not an option. |
| `internal/e2e/realclaude/set_permission_mode_probe_test.go` | `setModeArm`, `setModeArms` | The row-struct shape to mirror (`name`, `launchYOLO`) and the table **not** to extend or reuse: it carries #1595's own `targetMode` semantics and a fourth `enable` row. |
| `internal/e2e/realclaude/offline_exec_ban_test.go` | `finOfflineExecBans`, `TestFinOfflineFilesReachNoExecHelper` | The package's deterministic enforcement that an offline file reaches no exec / credential / skip helper. The new file gets one entry. |
| `internal/e2e/realclaude/finding_key_name_bounds_test.go` | any of its four `Test…` functions | The shape of a credential-free, assertion-only test file in this package: `t.Parallel()`, table of rows, failure messages that name the downstream consequence. |
| `docs/knowledge/features/e2e-realclaude.md` | the `interactive_stream_inband_bypass_revoke_test.go` (#1622) entry | What the harness already proves and what it deliberately does not. |

## Context

#1643 is a live three-arm probe of pyry's in-band bypass revocation: a revoked
child measured against controls launched in `default` and in `bypassPermissions`
from the start. Its arms are only comparable if each one's launch posture comes
from a **stored** registry entry that the warm-start path reads — and today the
seed that produces that entry writes one posture only.

This ticket is the half of #1643's substrate that decides which posture each arm
launches with. Every part of it settles with no claude binary and no credentials,
which is the point: the live run's first failure mode should cost zero tokens
rather than a whole three-arm run.

Two asymmetries make the naive versions of this work useless, and both are why
the design below picks the seams it picks:

- **The `false` posture is indistinguishable at the Pool.** A cold start also
  yields `YOLO: false`, because `loadRegistry` returns `(nil, nil)` for an absent
  file and `Pool.New` leaves `SessionSettings` zero-valued. So for the
  `control_default` arm a totally broken seed and a correct seed both read back
  `false` through `DefaultSettings`. The only evidence separating a stored false
  from a cold start is the **entry's existence** — `bootstrap` true and an `id`
  `ValidID` accepts. That is why AC 2 reads the file rather than the Pool.
- **The `true` posture fails closed, but vacuously.** A misspelled `yolo` key
  decodes to false, so the `revoke` arm launches without bypass, there is nothing
  to revoke, and #1643 measures nothing while staying green. A decode performed
  *through the seed's own struct* round-trips that misspelling and sees nothing
  wrong. That is why AC 2's decode reads the on-disk key names independently.

No ADR is warranted. This is a test-substrate ticket inside one package; the
durable prose belongs in the documentation phase's update to
`docs/knowledge/features/e2e-realclaude.md`.

## Design

Three files change. All three are test files; no production source file is
touched.

### 1. `interactive_stream_inband_bypass_revoke_test.go` — parameterize the seed

```go
func seedBypassRegistry(t *testing.T, path string, yolo bool) sessions.SessionID
```

Body change is one line: `YOLO: yolo` in place of `YOLO: true`. Everything else —
`sessions.NewID`, `Version: 1`, `Bootstrap: true`, `time.Now().UTC()` on both
timestamps, `json.MarshalIndent`, `os.WriteFile(..., 0o600)`, the returned id — is
unchanged.

**The name stays `seedBypassRegistry`.** "Bypass" names the family (the in-band
*bypass-revocation* probe this seed serves), not the posture it writes. Renaming
it would churn #1622's file and #1643's expectations for no gain.

**Doc comment.** AC 1's second clause: the first sentence currently asserts the
entry "carries yolo:true" and that becomes false. Replace it with a sentence that
says the caller chooses the bootstrap posture and what each choice means at the
warm-start seam — specifically that `false` is a *stored* false, which is not the
same thing as an absent file. Keep the rest of the comment verbatim: the
three-silent-failure-modes paragraph, the `sessions.NewID` / `ValidID` rationale
and the `0600` rationale are all still true.

**Call site.** `seedBypassRegistry(t, registryPath, true)`. The surrounding
comment ("BEFORE `sessions.New` — the whole warm-start seam depends on the file
existing at construction time") stays. Instrument check B inside that test still
requires `DefaultSettings().YOLO == true` and needs no edit.

**Do not touch the file's package-level header comment.** Its statement that the
child's bypass comes from "a seeded registry entry carrying yolo:true" describes
this test's own call, which still passes `true`.

**Do not add `omitempty` to `revokeSeedEntry.YOLO`.** See § "The one edit that
must not happen".

### 2. New file `inband_bypass_revoke_arms_test.go` — the arm table and both tests

One new file, `//go:build e2e_realclaude`, same package. It holds the
package-level arm table plus the two deterministic tests. It reaches no exec, no
subprocess, no credential helper and no skip helper: on a machine with no claude
and no credentials both tests report **PASS**, not SKIP.

#### The arm table

```go
// poolRevokeArm is one arm of #1643's three-arm comparison.
type poolRevokeArm struct {
	name               string
	launchYOLO         bool // the STORED bootstrap posture the arm is seeded with
	takesSettingsUpdate bool // does the arm take a mid-run Pool settings update
}

var poolRevokeArms = []poolRevokeArm{
	{name: "revoke", launchYOLO: true, takesSettingsUpdate: true},
	{name: "control_default", launchYOLO: false},
	{name: "control_bypass", launchYOLO: true},
}
```

Naming decisions, each load-bearing:

- **`poolRevokeArm` / `poolRevokeArms`, not `revokeArm` / `revokeArms` and
  emphatically not `setModeArms`.** The three name *strings* are shared with
  #1595's `setModeArms`, which also carries `enable` and whose `targetMode` field
  means something else entirely (the mode a hand-written control line asks for,
  not a stored launch posture). "Pool" in the symbol name is what tells a reader
  which of the two families a row belongs to. #1652 iterates `poolRevokeArms` by
  name.
- **`launchYOLO` matches `setModeArm`'s field name deliberately** — same concept,
  same word, two structs.
- **`takesSettingsUpdate`, not `takesUpdate`.** "Update" alone is ambiguous in a
  repo that also ships `pyry update`.

The row carries these three fields and no more. The `sessions.SettingsUpdate`
value, the composed argv, the fixture record and the probe prompt all belong to
#1643 and #1652; adding a field for them here would be pre-writing work whose
shape is not yet decided.

The table is a package-level `var` so #1652's name test and #1643's live driver
both range over the same rows. Its doc comment must say **read-only: never append
to it, never reassign it** — it is ranged over from `t.Parallel()` tests in at
least three files, and a mutation would race in a way `-race` catches only when
the runs happen to overlap. Ranging is the only supported access; nothing hands
the slice out, so no defensive copy is needed (contrast `dispatcherBaseTools`,
which does hand its slice to callers and therefore copies).

#### Test A — the seed stores the requested posture, for both values

`TestSeedBypassRegistry_StoresRequestedPostureUnderBothValues`.

Table over two rows, `yolo: true` and `yolo: false`. Per row: `t.TempDir()` →
`filepath.Join(dir, "sessions.json")` → call the seed → read and decode the file
→ assert.

**The decode must not go through `revokeSeedEntry`, `revokeSeedFile`,
`registryEntry`, or any struct whose json tags were written next to the seed's.**
Decode through `map[string]json.RawMessage` at both levels, with the four on-disk
key names written as string literals in the test file:

```go
// contract, not implementation
var file map[string]json.RawMessage          // "sessions"
var entries []map[string]json.RawMessage     // one element
raw, present := entries[0]["yolo"]           // presence BEFORE value
```

A struct-based decode — even one declared locally — cannot express the presence
clause without a `*bool`, and a `*bool` still asks the reader to notice why it is
a pointer. The map makes the on-disk key name the literal subject of the
assertion, which is the property AC 2 asks for.

Assertions per row, each with a failure message naming the downstream
consequence:

- the file exists and its permission bits are exactly `0600` (the mode
  `saveRegistryLocked` writes; the umask only clears bits, so a fresh create is
  stable at `0600`). **`os.WriteFile` does not re-chmod a file that already
  exists** — it truncates and keeps the existing mode. The assertion is therefore
  only meaningful on a path that does not yet exist, which is true of both
  callers today. Add a clause to `seedBypassRegistry`'s doc comment saying it
  must be handed a fresh path, so #1643 does not re-seed one path across its
  three arms and quietly inherit whatever mode was there.
- the top level decodes and carries a `"sessions"` key
- `"sessions"` holds **exactly one** entry
- the entry's `"bootstrap"` key is present and decodes to `true` — without it
  `pickBootstrap` returns nil, `Pool.New` takes the cold-start path, and the arm
  launches from nothing
- the entry's `"yolo"` key is **present** — asserted separately from its value,
  and this is the assertion that catches an `omitempty` added to
  `revokeSeedEntry.YOLO`. On the `false` row a value-only check passes on a
  vanished key, because the absent key decodes to exactly the value that row
  wants.
- the present `"yolo"` decodes to the row's requested value — on the `true` row
  this is what pins AC 1's "the entry that call site produces is unchanged"
- the entry's `"id"` is present, decodes to a string, and `sessions.ValidID`
  accepts it — `writeMCPSettings` hard-errors on anything else and claude
  receives it as `--session-id`
- that on-disk id equals the `sessions.SessionID` the seed returned — AC 1's
  "still returns the session id it minted". Nothing else in the repo would catch
  a seed that writes one id and returns another, and #1622's own failure
  diagnostics print the returned value.

What this test deliberately does **not** assert: the file's bytes (both
timestamps come from `time.Now()`), and `"version"` (no production reader
consults it — `loadRegistry` unmarshals and returns without inspecting it, and
the only `.Version` read in `internal/sessions` is a unit test's). Keep writing
`version: 1` for shape-consistency with `saveRegistryLocked`; do not add an
assertion that would claim coverage it cannot have.

#### Test B — the arm table's three rows, by name

`TestPoolRevokeArms_PinLaunchPostureAndUpdateByName`.

The test declares its **own** expectation table — a `map[string]struct{launchYOLO, takesSettingsUpdate bool}`
with the three names as literal keys — and sweeps it against `poolRevokeArms`
**both ways**:

- every expected name is found in `poolRevokeArms` (a missing row is a `t.Errorf`
  that prints the table's actual names, never a silent skip)
- every row in `poolRevokeArms` is found in the expectation map (so a fourth arm
  added later fails here until somebody pins its posture deliberately — which is
  the right outcome for *this* test, and the opposite of #1652's name test, whose
  job is to keep working as the table grows)
- row names are unique — a duplicate `revoke` row would make lookup-by-name
  ambiguous and could satisfy a per-name check while the table is wrong
- per matched name: `launchYOLO` and `takesSettingsUpdate` both equal the
  expectation
- exactly one row across the whole table has `takesSettingsUpdate == true`

Scenario rows and the consequence each failure message must name:

| Assertion | What a green-but-wrong table would cost #1643 |
|---|---|
| `revoke.launchYOLO == true` | `UpdateSettings` returns at its `merged == sess.settings` no-change check, having delivered nothing and reported success. No `RevokeBypass` is issued, no delivery attempted, and the arm measures nothing while staying green. |
| `control_bypass.launchYOLO == true` | The two control arms compose the same argv (`claudeSettingsArgs` emits nothing at all for `false`), so the comparison has nothing to discriminate with. |
| `control_default.launchYOLO == false` | Same collapse from the other side — and the arm *name* then lies to every consumer that reads it. This is why the pin is per-name and not "the two controls differ": "they differ" survives a straight swap. It is also the one row where a wrong value is a **privilege** fault and not only a measurement fault: `claudeSettingsArgs` turns a stored `true` into `--dangerously-skip-permissions` on a real claude child, and #1643's probe deliberately provokes a tool call. The failure message must say that, not just "want false". |
| `revoke.takesSettingsUpdate == true`, both controls `false` | A control that takes an update is not a control. |
| exactly one row takes an update | Family guard: survives the table growing by an arm that should also be a control. |

`t.Parallel()` on both tests and on their subtests — the package uses it on
exactly the credential-free files (`offline_exec_ban_test.go`,
`finding_key_name_bounds_test.go`'s family) and withholds it on the live ones.

### 3. `offline_exec_ban_test.go` — one entry in `finOfflineExecBans`

The new file's header will claim it reaches no exec, spawn, credential or skip
helper. `TestFinOfflineFilesReachNoExecHelper` is the package's existing
deterministic check of exactly that claim, and its map is opt-in per filename, so
a header written without a map entry is a claim with no enforcement — the precise
shape that file exists to eliminate (its own header records #1290 shipping a
grep that matched its file's prose).

Add one entry keyed `"inband_bypass_revoke_arms_test.go"`, listing the helpers
whose presence would turn this ticket's PASS into a SKIP or reach the operator's
environment:

```go
"inband_bypass_revoke_arms_test.go": {
    "resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
    "probeClaudeVersion", "os.Getenv", "os.Environ",
},
```

Per that map's own contract each entry is the file's **own** header list, so the
new file's header must name the same set. **Do not ban `t.TempDir`** — Test A
needs it. Do not add entries for any other file, and do not touch
`TestFinOfflineFilesReachNoExecHelper` itself.

## The one edit that must not happen

`revokeSeedEntry.YOLO` is tagged `json:"yolo"` with **no** `omitempty`, unlike
`registryEntry.YOLO`. A developer who "fixes" that asymmetry to match production
makes the key vanish on the `false` row. Only AC 2's presence clause reddens; a
value-only check passes, because the vanished key decodes to the very value that
row expects. The asymmetry is deliberate and the seed's doc comment should say so
in one sentence.

## Concurrency model

No goroutines, no channels, no locks. Both new tests are synchronous. Two details
make `t.Parallel()` safe rather than flaky, and both are easy to get wrong:

- **Call `t.TempDir()` inside each Test A subtest, never once in the parent
  loop.** A tempdir hoisted out of the loop is shared by two parallel subtests
  that both write `sessions.json` to the same path — a real race on the file and
  an intermittently wrong `yolo` value, which is exactly the class of silent
  green this ticket exists to prevent.
- **`poolRevokeArms` is read-only.** Test B, #1652's name test and #1643's driver
  all range over it from parallel tests. Nothing may append to it or reassign it.

Go is at 1.26 here, so loop variables are per-iteration and no `row := row` shim
is needed.

**No committed artifact.** Neither test writes anything under the package's
`testdata/`; both write only into `t.TempDir()`. Do not add a golden file "for
completeness" — a committed fixture inherits #1595's whole
credential-in-a-committed-file constraint (`setModeFixtureRecord`'s header
records why it carries no `env` field and caps free text), and #1652 is the
ticket that takes that on deliberately. This one has no such surface and should
keep it that way.

## Error handling

`seedBypassRegistry` keeps `t.Fatalf` on every failure — it is a test helper with
`t.Helper()`, and a seed that half-succeeded must not let the caller proceed.

In the new tests: structural failures that make later assertions meaningless
(cannot read the file, top level will not decode, wrong entry count) are
`t.Fatalf`. Everything else is `t.Errorf`, so one run reports every wrong field
rather than the first — a table that is wrong in two places is not one edit away
from right, and the reader deserves to know that before starting.
`TestFinOfflineFilesReachNoExecHelper` already sets that precedent in this
package.

Failure messages name the *consequence*, not just the mismatch. "want true, got
false" is useless here; "the `revoke` arm would launch without bypass, so
`UpdateSettings` returns at its no-change check and #1643 measures nothing while
staying green" is the message that saves the next run.

## Testing strategy

The gate is `make e2e-realclaude`
(`go test -tags e2e_realclaude -count=1 ./internal/e2e/realclaude/...`). While
developing, `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` is the fast
compile check — `make check` never compiles this package, so it can stay green
over a package that does not build (observed 2026-08-16, a day of it).

**Read the run, not the exit code, and read it correctly.** The suite exits 0
both on a build failure and on a full credentials skip. `TestMain` does not gate
on credentials, and both skip helpers (`WithWorktreeAuthenticated`,
`resolveClaudeBin`) call `t.Skip` inside the test body — after `=== RUN` is
emitted. So a credentials-skipped machine still prints the suite's full `=== RUN`
count with `--- SKIP` under most of it; a count that collapses to zero is the
build-failure signal, not the credentials one.

What separates this ticket's two tests from the rest of the suite is that they
take neither skip helper. The developer's evidence is:

```
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestSeedBypassRegistry_StoresRequestedPosture|TestPoolRevokeArms_' \
  ./internal/e2e/realclaude/
```

reporting `--- PASS` for both, plus the whole-package run's `=== RUN` count.
`TestFinOfflineFilesReachNoExecHelper` must also be green, with the new file
among its subtests.

**Mutation evidence** (run through `go test -overlay=<abs>/overlay.json` so no
mutated source is ever written into the worktree). Each of these must redden the
assertion named, and the developer should report the observed red set:

| Mutant | Expected sole red |
|---|---|
| `omitempty` added to `revokeSeedEntry.YOLO` | Test A's `false` row, presence clause only. Confirm the value clause stays green — that is the whole argument for asserting presence separately. |
| `YOLO: yolo` reverted to `YOLO: true` | Test A's `false` row, value clause. |
| the `yolo` tag misspelled (e.g. `json:"yollo"`) | Test A's `false`-row presence clause and `true`-row value clause. Confirm a decode through `revokeSeedEntry` would *not* have caught it — that is why the map decode exists. |
| the seed returns a fresh `sessions.NewID()` instead of the written one | Test A's returned-id equality clause, both rows. |
| `revoke` row flipped to `launchYOLO: false` | Test B's `revoke` posture assertion. |
| the two control rows swapped | Test B's per-name posture assertions. Confirm a "the two controls differ" assertion would have stayed green. |
| `takesSettingsUpdate: true` added to `control_bypass` | Test B's control assertion **and** the exactly-one count. |

## Open questions

- **Whether #1643 wants the composed argv on the row.** `claudeSettingsArgs`
  makes it derivable from `launchYOLO`, so this ticket leaves it off and #1643
  can add a field if the live driver actually needs one materialised. Deriving it
  is one call; carrying it is a second source of truth.
- **Whether `poolRevokeArms` eventually absorbs an `enable` arm.** #1595's family
  has one and #1643 might grow one. Test B's both-ways sweep is written so that
  addition fails loudly until its posture is pinned deliberately; #1652's name
  test is written so it keeps working. No action now.

## Out of scope

- `deliverSettingsInBand`'s doc comment claiming "#1605 measures the in-flight
  window live", and the same handoff recorded in
  `docs/knowledge/codebase/1604.md`. #1605 never carried that scope. Tracked as
  #1624.
- The fixture record, namer and writer, and the name-family disjointness against
  #1595's committed fixtures — #1652, which consumes `poolRevokeArms`.
- The live three-arm probe itself — #1643.
- `docs/knowledge/features/e2e-realclaude.md`. The documentation phase owns it;
  it is not a developer deliverable.

## Security review

**Verdict:** PASS

The four SHOULD FIX findings below were applied to this spec inline before the
verdict; each names the section that now carries the fix.

**Findings:**

- **[Trust boundaries] SHOULD FIX — applied.** The design adds no new boundary:
  nothing untrusted crosses into the process, and `seedBypassRegistry` only ever
  writes into a `t.TempDir()`. But the boundary it *feeds* is real —
  `pickBootstrap` and `Pool.New` treat the seeded entry as trusted spawn intent,
  and `claudeSettingsArgs` turns a stored `true` into
  `--dangerously-skip-permissions` on a live claude child. Parameterizing the
  posture cannot *grant* bypass where it previously could not (the prior
  behaviour was unconditional `true`, and the one existing call site still passes
  `true`), so the direction of the change is toward less privilege. The residual
  hazard is a future call site — #1643's — seeding `control_default` with `true`
  and launching a real child with permissions bypassed into a probe that
  deliberately provokes a tool call. AC 3's per-name pin is the mitigation; the
  spec's Test B table now requires that row's failure message to name the
  privilege fault rather than only the measurement fault.
- **[Tokens, secrets, credentials] SHOULD FIX — applied.** Nothing in this
  ticket reads, stores, logs or transits a credential. The only generated value
  is the session id, minted by `sessions.NewID` from `crypto/rand`; it is not a
  secret (it reaches claude as `--session-id`, visible in the process table) and
  the spec forbids replacing it with a hand-written string. The reachable risk
  was a developer adding a golden fixture under `testdata/` for the decode test,
  which would import #1595's committed-artifact constraint — `setModeFixtureRecord`'s
  header records why that family carries no `env` field and caps free text —
  into a ticket that has no such surface. § Concurrency model now forbids any
  committed artifact explicitly.
- **[File operations] SHOULD FIX — applied.** `os.WriteFile` applies its
  permission argument only when it *creates* the file; on an existing path it
  truncates and keeps the old mode. The `0600` assertion is therefore meaningful
  only on a fresh path. True of both callers today, but #1643 re-seeding one path
  across three arms would silently inherit whatever mode was there. Test A's
  assertion list now records the constraint and asks for a matching clause in
  `seedBypassRegistry`'s doc comment.
- **[File operations] No finding — path handling.** No caller-controlled or
  externally-sourced value reaches a filesystem path anywhere in this design. The
  seed's `path` is a test-local `filepath.Join(t.TempDir(), "sessions.json")` at
  both call sites, so there is no traversal vector and no containment assertion
  is warranted here. This is the concrete difference from #1652, where a version
  token flows into a filename and `versionSlug` leaves `.` intact so `..`
  survives slugging — do not import that machinery into this ticket. No
  check-then-use pair exists, so no TOCTOU. The seed's plain `os.WriteFile`
  rather than `saveRegistryLocked`'s temp-and-rename is accepted: a torn write
  yields malformed JSON, `loadRegistry` fails the whole parse closed, and
  `sessions.New` errors loudly — and there is one writer, no concurrent reader,
  and no artifact that outlives the test.
- **[Subprocess / external command execution] No finding.** The new file spawns
  nothing, and that is enforced deterministically rather than by prose: the
  `finOfflineExecBans` entry the spec adds bans `resolveClaudeBin`,
  `probeClaudeVersion`, `WithWorktree`, `WithWorktreeAuthenticated`, `os.Getenv`
  and `os.Environ` in it, and `TestFinOfflineFilesReachNoExecHelper` parses the
  file's AST (deliberately without `parser.ParseComments`) so the check cannot
  satisfy itself out of the header that states it. No `sh -c` anywhere. The
  environment the live child inherits is unchanged by this ticket.
- **[Cryptographic primitives] No finding.** The only primitive in reach is
  `sessions.NewID`'s `crypto/rand` draw, with `ValidID` checking the version-4
  nibble and RFC 4122 variant. No hand-rolled crypto, no key or nonce reuse, and
  no comparison against a secret — so no `crypto/subtle` requirement. The spec's
  ban on a hand-written id string keeps `crypto/rand` in the loop.
- **[Network & I/O] Not applicable.** No sockets, no listeners, no HTTP server,
  no timeouts to set. The single read is `os.ReadFile` over a file the same test
  wrote moments earlier — a few hundred bytes of the test's own JSON — so there
  is no untrusted stream needing a size cap.
- **[Error messages, logs, telemetry] No finding.** The spec requires rich
  failure messages, so this category got the hardest look. Everything a message
  can print is synthetic: a UUIDv4, two `time.Now()` timestamps, two booleans, a
  `t.TempDir()` path and the arm names. No credential can reach a message,
  because the only paths to one — `os.Getenv`, `os.Environ`,
  `WithWorktreeAuthenticated` — are banned in the file by the
  `finOfflineExecBans` entry. No child output is retained anywhere, so #1595's
  truncate-every-free-text-field rule has nothing to apply to. No telemetry.
- **[Concurrency] SHOULD FIX — applied.** Two parallelism hazards, neither of
  them lock-ordering. `poolRevokeArms` is a package-level mutable slice ranged
  over from `t.Parallel()` tests in three files, so an append or reassignment
  would race intermittently; the spec now requires a read-only clause on its doc
  comment. And a `t.TempDir()` hoisted out of Test A's row loop would be shared
  by two parallel subtests writing the same `sessions.json` — the spec now
  requires it inside the subtest. No goroutines are spawned, so there is no
  lifecycle or leak question; no locks are taken, so there is no ordering to
  document; nothing survives the process, so there is no mid-write shutdown
  state to recover.
- **[Threat model alignment] Out of scope, named.** No relay and no CLI surface,
  so `docs/protocol-mobile.md` § Security model has no applicable threat. The two
  adjacent threats this ticket deliberately does not take on are the
  committed-fixture credential surface (#1652, which is where the injectable
  writer and the truncation rules land) and the live bypassed child itself
  (#1643, which is the run that actually spawns one).

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-20
