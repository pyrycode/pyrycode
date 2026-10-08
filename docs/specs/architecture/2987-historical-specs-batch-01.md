# #2987 — Historical specs review, batch 01 of 22

## Files read

- `docs/specs/README.md` → “These are point-in-time artifacts, not current-state authority” and “Scaffolding maintenance”: preserve designs and citations; removals require byte-bound approval and eligibility.
- `docs/knowledge/features/development-verification.md` → “Check searches and citations”: existence and closed state do not establish current truth.
- The 45 exact manifest paths in issue #2987 → historical bodies, cited targets and question decisions; enumerate each once in the completed ledger below.
- `cmd/spec-reference-inventory/main.go` → `run`: distinct document/target inventory at each pinned tree.
- `cmd/spec-scaffolding-prune/main.go`, `markdown.go`, `evidence.go` → `run`, `parse`, `eligibility`: hashes, all-heading ordinals, unsupported Markdown and merge-evidence boundaries.
- `cmd/spec-scaffolding-prune/testdata/2929-evidence.json` → reusable verified closing-link snapshot; no new evidence acquisition.
- `docs/knowledge/features/sessions-package.md`, `sessions-package-key-types-runner-interface-runnerfactory.md`, `control-plane.md`, `cli-verb-dispatch.md` → current session/control seams and terminal retirement.
- `docs/knowledge/features/jsonl-reconciliation.md`, `rotation-watcher.md` → startup adoption retired by #839; watcher retired by #2137.
- `docs/knowledge/features/e2e-harness.md` → harness history and present test boundaries.
- `internal/sessions/pool.go`, `pool_identity.go`, `reconcile.go`, `runner.go` → `List`, `ResolveID`, `Rename`, `RotateID`, `DefaultClaudeSessionsDir`, runner contract.
- `cmd/pyry/main.go`, `internal/e2e/harness.go`, `cli_verbs_test.go`, `cap_test.go` → `runArgs`, active-cap flag, harness constructors and retained binary-boundary proofs.
- `CODING-STYLE.md` and shared working practice → review scope, commits, GitHub helpers and source evidence.

## Context

Review only issue #2987's exhaustive 45-document manifest at scope commit
`bf3129a42d38d257a3085f05dde5079a275c84a6` against reviewed-current commit
`f2a41c4b5bf2e72437eb1dc97b47b8178ff86429`. Every selected document is byte-identical
between these trees; no candidate disappeared. The scope has 37 missing-reference
pairs in 16 documents and 43 exact-title question occurrences in 43 documents.
The categories overlap. No new architectural decision record is required.

## Design

One deliverable: an evidence-backed review and precise documentation handoff.
Only this plan is committed. Append the disposition ledger and machine-readable
JSON approvals and batch-only preview excerpt here, extracting blocks to scratch
files for existing tool input. Full corpus output and validation scripts stay in
`/tmp/builder-2987/`. No detector, harness, production or corpus edits.

Classify documents and their individual findings as historical and still useful,
superseded/misleading, or unresolved. Missing paths stay intact. Notices give exact
wording, owning-document links and insertion headings outside removal ranges.
Retain questions carrying useful rationale or deferred requirements. Approve only
eligible resolved/obsolete sections with no unique useful decision to lose.
Unsupported #88 and merge-ineligible #27 receive no approval. Reuse current section
identities, never the pre-backfill report. Validate manifest coverage independently
from the ledger, pair uniqueness and section identities against both tree outputs.

Sizing: fewer than 700 written lines planned, zero exported types/interfaces,
zero consumer updates, four acceptance criteria, zero state-machine reject branches.
Remote overlap check found feature/2873 and feature/2882; neither touches this plan
or the selected historical documents. No unmerged dependency is needed.

## Concurrency model

Offline, sequential review and preview. No new goroutines or runtime lifecycle.

## Error handling

Unknown evidence remains explicitly unresolved. Stale, duplicate, unmatched or
ineligible approvals are rejected; diagnostics must be empty. A diagnostic section
range in unsupported Markdown does not authorize removal. Never apply the preview.

## Testing strategy

Reconstruct the scope inventory and preview from the pinned scope tree. Compare
selected document bytes, hashes, pairs and all-heading ordinals with the reviewed
tree. Validate all 45/37/43 entries once, notice headings outside approved ranges,
and one exact removal per approval. Reproduce the selected offline preview using
the committed approval block. Run race tests for both existing review commands,
`go vet ./...`, `go build ./cmd/pyry` and `git diff --check`; the verifier owns the
full-module gate. No live-Claude test is required.

## Open questions

Review outcomes and precise unresolved requirements will be recorded below and on
issue #2987. They add no implementation scope to this ticket.

## Documentation handoff

Pending for the documentation stage: apply only the completed ledger's exact
notices and approvals at its exact paths/headings. Preserve historical citations,
bodies outside approved removals and useful unresolved questions. After notice
insertion or any intervening edit, re-identify the same approved sections and
regenerate full-document hashes, all-heading ordinals and offline preview before
removal. Never authorize other documents from the temporary corpus report.
Any durable correction must name its exact owning knowledge path, heading and
replacement wording below. Frozen archives remain untouched.

## Completed review and evidence

The following review artifacts complete the planned review; they do not authorize
changes outside the manifest. JSON is embedded here so all committed artifacts
remain in the builder-owned plan path. Each block is independently machine-readable:
the document ledger, missing-reference ledger, approval array and preview excerpt,
in that order. Extract the third block as the existing tool's `-approvals` input.

Scope and reviewed-current commits are recorded in both ledgers. All 45 documents,
37 missing pairs and 43 question identities are unchanged between those commits.
All 37 targets remain missing. No disappearance or intervening section remapping
is required. The preview checkout includes only this ticket's initial plan commit
on top of the reviewed-current commit; all selected document hashes are identical
to reviewed-current bytes. Preview `evidence_sha256` is the tool's compact-encoding
hash; `evidence_file_sha256` additionally binds the reused snapshot file bytes.
The excerpt omits corpus documents and snapshot issue rows; the committed snapshot
contains those rows. Empty raw diagnostics (`null`) are normalized to `[]`.

Evidence keys below identify concrete inspected witnesses at reviewed-current.
Merged replacement evidence is from the reused closing-link snapshot, with local
Git history independently confirming the retired paths. Existing tests are source
evidence for dispositions, not a claim that their whole suites ran here.

| Key | Current witness and supported disposition |
|---|---|
| E1 | `docs/deployment.md` → “Updating PATH after installing new tools”; `internal/install/install.go` → `Install`: #19's recovery checklist landed; its richer alternatives remain historical deferrals. |
| E2 | `8832ca6554eceb414afbcd0f1357c5cf634c1b08` (#1348, merged PR #1471) deletes terminal supervisor/attach tests; #1535 merged PR #1640 deletes orphan wire types. `cmd/pyry/main.go` → `runArgs`, `errAttachRemoved`; `internal/sessions/runner.go` → `Runner`; `docs/knowledge/features/control-plane.md` → opening verb description; current terminal deletion, not a file rename. |
| E3 | #839 merged PR #882, #1149 merged PR #1157; `internal/sessions/pool.go` → `New` preserves explicit bootstrap identity without newest-file adoption; `internal/sessions/reconcile.go` → only encoding/path helpers survive; `internal/streamsup/runner_spawn.go` → `buildArgs`; retired startup scan and current deterministic ID flags. |
| E4 | `9a74f99115d92450aa19fbac6d8c8cf765122e01` (#2137, merged PR #2177) deletes `internal/sessions/rotation` and `internal/e2e/rotation_test.go`; #2134/#2135/#2136 merged PRs #2163/#2174/#2175 supply announcements. `cmd/pyry/session_reset_follow.go` → `sessionResetFollower.Sink`, `follow`, `rekeyPool`; `internal/sessions/transition.go` → `AdoptAnnouncedID`; `docs/knowledge/features/rotation-watcher.md` → “Where the replacement lives”. |
| E5 | `cmd/pyry/main.go` → `runSupervisor` wires `pyry-active-cap`; `internal/sessions/pool_cap_test.go` → `TestPool_ActiveCap_OneSessionAtCapOne`, `TestPool_ActiveCap_ZeroIsParity`; `internal/e2e/cap_test.go` → active-cap/interleave tests; CLI exposure and package edge proofs exist. |
| E6 | `internal/e2e/cli_verbs_test.go` → `TestStop_E2E`, `TestLogs_E2E`, `TestVersion_E2E`, `TestStatus_E2E_Stopped`; `harness.go` → `RunBare`; settled binary-boundary verb coverage. |
| E7 | `internal/sessions/pool.go` → `List` uses `sort.Slice`; `pool_list_test.go` → `TestPool_List_RaceClean` mutates via `RotateID`; total ordering and chosen race proof. |
| E8 | `internal/sessions/pool_identity.go` → `Rename` early-returns same labels, rolls back save failures and does no label validation; historical boundary rationale remains useful. |
| E9 | `internal/sessions/pool.go` → `ResolveID`, `ambiguousError`; `cmd/pyry/control_client.go` → `parseSessionsRenameArgs` returns `fs.Arg` directly and `resolveSessionIDViaList` compares verbatim strings; sorting survives, automatic positional trimming does not exist. |
| E10 | `internal/e2e/harness.go` → `ensurePyryBuilt`, `Start`, `Run`, `RunBare`, `teardown`; build command lacks `-race`, cache uses `sync.Once`, planned `internal/e2e/sessions/` was only an optional sibling location. |
| E11 | `internal/sessions/pool.go` → `Run`, `supervise`; `pool_mint.go` → `Create`, `CreateIn`, `Mint`, `buildSession`; `pool_test.go` → `TestPool_Supervise_ConcurrentCalls_RaceClean`; fan-out is live and construction has evolved. |
| E12 | `internal/control/server.go` → `handleSessionsNew`, `sessionOpTimeout`; local fixed-budget creation without a rate limiter; no conclusion about remote product needs. |
| E13 | `cmd/pyry/control_client.go` → `parseSessionsNewArgs`, `runSessionsNew`, `runSessions`; live local CLI, Go flag spellings and no local retry loop. |
| E14 | `internal/e2e/install_linux_test.go` → `skipIfNoUserSystemd`, `TestE2EInstall_PathInheritance_Linux`; `install_darwin_test.go` → `waitForRunning`, `TestE2EInstall_PathInheritance_macOS`; `docs/knowledge/features/install-e2e.md` → “Open Question — Headless macOS CI”; still conditional platform follow-ups. |
| E15 | `internal/control/server.go` → `handleSessionsList`; `internal/control/protocol.go` → `SessionInfo`; sort order is preserved and `time.Time` uses JSON encoding; #87's Design keeps those decisions. |
| E16 | `cmd/pyry/control_client.go` → `runSessionsList`, `writeSessionsTable`, `writeSessionsJSON`; `internal/e2e/sessions_list_test.go` → table/JSON tests; #88 is supported behavior but unsupported Markdown for pruning. |
| E17 | `internal/control/server.go` → `Sessioner`, `handleSessionsRename`, `handleSessionsRm`; canonical ID pass-through, typed removal errors and separate named per-verb seams; no ignored Rename ctx. |
| E18 | #93 merged PR #153; `cmd/pyry/control_client.go` → `runSessionsRename`, `resolveSessionIDViaList`; `internal/e2e/sessions_rename_test.go` → success-prefix/ambiguous-prefix tests; #92's restriction was replaced. |
| E19 | `internal/sessions/pool_remove.go` → `Remove`, `RemoveOptions`, `disposeJSONLLocked`; `pool_remove_test.go` → bootstrap, unknown, archive and purge tests; #94/#95's surviving design explains policy and failure ordering. |
| E20 | `cmd/pyry/control_client.go` → `parseSessionsRmArgs`, `runSessionsRm`, `resolveSessionIDViaList`; current bool policy flags and no re-resolve loop; ergonomics remain conditional. |
| E21 | `internal/e2e/restart_test.go` → `TestE2E_Restart_PreservesActiveSessions`, `TestE2E_Restart_PreservesEvictedSessions`, `TestE2E_Restart_LastActiveAtSurvives`; `harness.go` → `StartIn`, `Stop`; retained restart persistence proofs. |
| E22 | `internal/e2e/startup_test.go` → `TestE2E_Startup_CorruptRegistryFailsClean`; corrupt bytes are preserved after failure, using `StartExpectingFailureIn`. |
| E23 | `harness.go` → `Run`, `runVerb`, `StartIn`; `internal/e2e/per_conversation_eviction_test.go` → `TestE2E_PerConversation_IdleEvictsAndReactivates`; idle tests evolved, while E16 proves the formerly missing enumeration command exists. |
| E24 | `internal/e2e/internal/fakeclaude/main.go` → legacy rotation loop and `pollInterval`; `internal/e2e/harness.go` → `StartRotation`, `spawnWith`, `ensureFakeClaudeBuilt`; fake/harness primitives survive watcher retirement. |
| E25 | `cmd/pyry/control_client.go` → `runStatus`, `runLogs`, `runStop` each reject nonempty `rest` before transport; #102's non-attach deferral is settled. |

## Disposition ledger

One document entry per manifest path; `question` is the exact scoped occurrence,
including capitalization and the one-based ordinal among all parsed headings.
There are no repeated exact-title question headings in this batch. Classification
of the document and its question may differ: useful rationale survives a retired
design. A missing-reference record below supplies its own classification.
Notices insert `## Historical review (#2987)` immediately before the first heading
whose exact title is `Context` (keep that heading's original depth). This is outside
every proposed question removal. Notice wording is exact; historical text is retained.

### Document ledger JSON

```json
{"scope_commit":"bf3129a42d38d257a3085f05dde5079a275c84a6","reviewed_current_commit":"f2a41c4b5bf2e72437eb1dc97b47b8178ff86429","documents":[
{"document":"docs/specs/architecture/19-stale-service-path-recovery.md","classification":"historical and still useful","evidence":["E1"],"document_disposition":"preserve","question":{"heading":11,"title":"Open questions","classification":"historical and still useful","action":"approve removal","reason":"Resolved content checklist; no decision in this section. Refresh/config alternatives remain visible in Out of scope (and why)."}},
{"document":"docs/specs/architecture/27-session-addressable-runtime.md","classification":"superseded/misleading","evidence":["E2"],"document_disposition":"annotate","question":null,"reason":"No exact-title scoped question. The suffixed developer questions remain historical; closed-without-merge evidence forbids pruning.","notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"Historical terminal design: #1348 removed internal/supervisor and the attach/bridge path; #1535 removed the attach/resize wire types. Current sessions use Runner/RunnerFactory, and pyry attach returns a removal error. See [current runner ownership](../../knowledge/features/sessions-package-key-types-runner-interface-runnerfactory.md) and [control plane](../../knowledge/features/control-plane.md)."}},
{"document":"docs/specs/architecture/28-sessions-package.md","classification":"superseded/misleading","evidence":["E2"],"document_disposition":"annotate","question":null,"reason":"No scoped question. Preserve the original package introduction and retired supervisor dependency as history.","notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"Historical terminal design: #1348 removed internal/supervisor and the attach/bridge path; #1535 removed the attach/resize wire types. Current sessions use Runner/RunnerFactory, and pyry attach returns a removal error. See [current runner ownership](../../knowledge/features/sessions-package-key-types-runner-interface-runnerfactory.md) and [control plane](../../knowledge/features/control-plane.md)."}},
{"document":"docs/specs/architecture/38-startup-jsonl-reconciliation.md","classification":"superseded/misleading","evidence":["E3"],"document_disposition":"annotate","question":{"heading":18,"title":"Open questions","classification":"historical and still useful","action":"retain","reason":"Retain: activity timestamp rationale survives in RotateID; Info logging, stat injection and symlink choices document the retired scan. No current scan tuning requirement."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"Historical startup design: #839 removed newest-JSONL bootstrap adoption and #1149 removed mostRecentJSONL. Bootstrap spawning uses an explicit persisted ID; do not copy the retired reconciliation step. See [startup reconciliation history](../../knowledge/features/jsonl-reconciliation.md) and [current ID flag selection](../../knowledge/features/streamsup-package-buildargs-the-id-flag-inversion-that-keeps.md)."}},
{"document":"docs/specs/architecture/39-live-rotation-watcher.md","classification":"superseded/misleading","evidence":["E4"],"document_disposition":"annotate","question":{"heading":27,"title":"Open questions","classification":"historical and still useful","action":"retain","reason":"Retain: retry cadence, coordinator choice, TTL, probe granularity and PID disambiguation are useful retired-design reasoning. Directory-loss recovery was conditional on the deleted watcher, not a requirement for the stream follower."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"Historical rotation design: #2137 removed the fsnotify/open-descriptor watcher and its rotation test. Current conversation_reset handling uses sessionResetFollower and Pool.AdoptAnnouncedID. See [rotation retirement and replacement](../../knowledge/features/rotation-watcher.md)."}},
{"document":"docs/specs/architecture/41-concurrent-active-cap.md","classification":"superseded/misleading","evidence":["E5"],"document_disposition":"annotate","question":{"heading":14,"title":"Open Questions","classification":"superseded/misleading","action":"retain","reason":"Retain: CLI flag is implemented; attach eviction/exclusion is obsolete after terminal retirement; cap=1 proof exists. Preserve the hard-limit policy rationale."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"The CLI-exposure deferral is superseded: pyry supports -pyry-active-cap. The attached-PTY discussion is historical because #1348 removed attach. The cap and LRU policy remain live. See [current idle/cap lifecycle](../../knowledge/features/idle-eviction.md)."}},
{"document":"docs/specs/architecture/52-cli-verbs-e2e-coverage.md","classification":"historical and still useful","evidence":["E6"],"document_disposition":"preserve","question":{"heading":14,"title":"Open questions","classification":"historical and still useful","action":"approve removal","reason":"Resolved mechanical implementation statement; no unique decisions or deferrals in section."}},
{"document":"docs/specs/architecture/60-pool-list-read-primitive.md","classification":"historical and still useful","evidence":["E7"],"document_disposition":"preserve","question":{"heading":12,"title":"Open questions","classification":"historical and still useful","action":"retain","reason":"Retain: sort.Slice is implemented and ordering is total; List_RaceClean uses RotateID. Preserve the test-mutator and ordering rationale."}},
{"document":"docs/specs/architecture/62-pool-rename-write-primitive.md","classification":"historical and still useful","evidence":["E8"],"document_disposition":"preserve","question":{"heading":14,"title":"Open questions","classification":"historical and still useful","action":"retain","reason":"Retain: Rename short-circuits same labels and accepts arbitrary labels. Preserve why validation belongs to callers."}},
{"document":"docs/specs/architecture/66-pool-resolve-id.md","classification":"superseded/misleading","evidence":["E9"],"document_disposition":"annotate","question":{"heading":16,"title":"Open questions","classification":"superseded/misleading","action":"retain","reason":"Retain: ambiguousError sorts IDs and ResolveID does not trim. Preserve loose-input boundary rationale; positional values are preserved by current CLI parsing and resolution, so the historical automatic-trimming claim must not be reused."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"The automatic positional-whitespace trimming claim is incorrect: current CLI parsers and resolveSessionIDViaList preserve the supplied string, as does Pool.ResolveID. Trimming would require an explicit caller policy. See [current prefix resolver](../../knowledge/features/sessions-package-key-types-pool-resolveid-1-1e.md)."}},
{"document":"docs/specs/architecture/68-e2e-harness-primitive.md","classification":"unresolved","evidence":["E10"],"document_disposition":"preserve","question":{"heading":16,"title":"Open questions","classification":"unresolved","action":"retain","reason":"Retain: ensurePyryBuilt still invokes go build without -race. Unknown: whether the parent race setting should instrument the child or CI always supplies an instrumented PYRY_E2E_BIN. Windows remains out of scope; the planned sessions subdirectory was optional."}},
{"document":"docs/specs/architecture/69-e2e-cli-driver.md","classification":"historical and still useful","evidence":["E10"],"document_disposition":"preserve","question":{"heading":14,"title":"Open questions","classification":"historical and still useful","action":"retain","reason":"Retain: sync.Once still orders the build; Run remains argument/socket driven and RunBare now covers socket-free calls. These are historical helper-extension choices, not a ban on future stdin support."}},
{"document":"docs/specs/architecture/72-pool-supervise-seam.md","classification":"historical and still useful","evidence":["E11"],"document_disposition":"preserve","question":{"heading":13,"title":"Open questions","classification":"historical and still useful","action":"retain","reason":"Retain the useful dummy-session versus duplicate-bootstrap concurrency-test choice; fan-out concurrency tests remain."}},
{"document":"docs/specs/architecture/73-pool-create-primitive.md","classification":"historical and still useful","evidence":["E11"],"document_disposition":"preserve","question":{"heading":21,"title":"Open questions","classification":"historical and still useful","action":"retain","reason":"Retain the useful construction factoring rationale. Create now delegates through CreateIn/Mint/buildSession; this does not make the old inline-construction choice a current prescription."}},
{"document":"docs/specs/architecture/75-control-sessions-new.md","classification":"unresolved","evidence":["E12"],"document_disposition":"preserve","question":{"heading":15,"title":"Open questions","classification":"unresolved","action":"retain","reason":"Retain: local handler remains un-rate-limited with a fixed context budget and no success log. Unknown: product need/policy for remote creation rate limits, configurable operation timeout and additional success logging; these conditional requirements are not implemented by this review."}},
{"document":"docs/specs/architecture/76-cli-sessions-new.md","classification":"unresolved","evidence":["E13"],"document_disposition":"preserve","question":{"heading":19,"title":"Open questions","classification":"unresolved","action":"retain","reason":"Retain: flag accepts either dash spelling, local dialing has no retry. Unknown: whether operator feedback warrants sessions help or a remote retry policy; no such requirement is inferred from the local implementation."}},
{"document":"docs/specs/architecture/78-foreground-stdin-reader-leak.md","classification":"superseded/misleading","evidence":["E2"],"document_disposition":"annotate","question":{"heading":17,"title":"Open questions","classification":"historical and still useful","action":"retain","reason":"Retain the useful leak-test tolerance rationale as historical; foreground terminal input and its supervisor test no longer exist."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"Historical terminal design: #1348 removed internal/supervisor and the attach/bridge path; #1535 removed the attach/resize wire types. Current sessions use Runner/RunnerFactory, and pyry attach returns a removal error. See [current runner ownership](../../knowledge/features/sessions-package-key-types-runner-interface-runnerfactory.md) and [control plane](../../knowledge/features/control-plane.md)."}},
{"document":"docs/specs/architecture/80-e2e-install-systemd-roundtrip.md","classification":"unresolved","evidence":["E14"],"document_disposition":"preserve","question":{"heading":17,"title":"Open questions","classification":"unresolved","action":"retain","reason":"Retain: Linux still gates on usable user systemd and keeps PATH tests under e2e_install. Unknown: service-account linger requirements and whether PATH-only tests should move to the default gate; no host run was performed."}},
{"document":"docs/specs/architecture/81-e2e-install-launchd-roundtrip.md","classification":"unresolved","evidence":["E14"],"document_disposition":"preserve","question":{"heading":18,"title":"Open questions","classification":"unresolved","action":"retain","reason":"Retain: macOS still uses gui/<uid> and state = running, with PATH tests under e2e_install. Unknown: a demonstrated headless CI failure requiring user/<uid>, a format change, or demand to split PATH coverage; no macOS run was performed."}},
{"document":"docs/specs/architecture/87-control-sessions-list.md","classification":"historical and still useful","evidence":["E15"],"document_disposition":"preserve","question":{"heading":15,"title":"Open questions","classification":"historical and still useful","action":"approve removal","reason":"Resolved sort/time/state/error/seam choices are in Design and handleSessionsList; speculative fake ergonomics add no deferred requirement. No unique decision is lost."}},
{"document":"docs/specs/architecture/88-cli-sessions-list.md","classification":"historical and still useful","evidence":["E16"],"document_disposition":"preserve","question":{"heading":22,"title":"Open questions","classification":"historical and still useful","action":"retain","reason":"Retain: resolved formatting/exit/timeout choices remain useful, but the whole document is parser-ineligible. Future table-padding feedback is conditional; diagnostic range is not deletion authority."}},
{"document":"docs/specs/architecture/90-control-sessions-rename.md","classification":"unresolved","evidence":["E17"],"document_disposition":"preserve","question":{"heading":15,"title":"Open questions","classification":"unresolved","action":"retain","reason":"Retain: server passes canonical IDs verbatim, CLI resolves prefixes; Create stays on Sessioner, Rename has no ignored ctx or success log. Unknown: future third-consumer demand for factoring, cancellable Rename or operator demand for success logs."}},
{"document":"docs/specs/architecture/92-cli-sessions-rename.md","classification":"superseded/misleading","evidence":["E18"],"document_disposition":"annotate","question":{"heading":16,"title":"Open questions","classification":"superseded/misleading","action":"retain","reason":"Retain: #93 replaced full-UUID-only rename with client prefix resolution. Silent success and empty-label clearing remain; unknown future demand for --clear or success output is preserved as conditional feedback."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"The full-UUID-only restriction is superseded by #93: pyry sessions rename resolves a unique prefix client-side through resolveSessionIDViaList before sending the canonical ID. See [current rename contract](../../knowledge/features/sessions-package-key-types-pool-rename-1-1c.md)."}},
{"document":"docs/specs/architecture/93-cli-sessions-rename-prefix.md","classification":"historical and still useful","evidence":["E18"],"document_disposition":"preserve","question":{"heading":15,"title":"Open questions","classification":"historical and still useful","action":"retain","reason":"Retain useful behavioural-test rationale: label change proves canonical resolution; ambiguous input leaves labels unchanged. The third question concerns a historical comment edit, not rewriting #92 retrospectively."}},
{"document":"docs/specs/architecture/94-pool-remove-core.md","classification":"historical and still useful","evidence":["E19"],"document_disposition":"preserve","question":{"heading":20,"title":"Open questions","classification":"historical and still useful","action":"approve removal","reason":"Resolved boilerplate: edge-case design remains in Design/Testing strategy. The missing supervisor test is a historical helper precedent, not a current dependency."}},
{"document":"docs/specs/architecture/95-pool-remove-jsonl-disposition.md","classification":"historical and still useful","evidence":["E19"],"document_disposition":"preserve","question":{"heading":19,"title":"Open questions","classification":"historical and still useful","action":"approve removal","reason":"Resolved boilerplate: enum/struct shape and partial-failure ordering remain in Design. No unique decisions or deferred requirements in the removal."}},
{"document":"docs/specs/architecture/98-control-sessions-rm.md","classification":"unresolved","evidence":["E17"],"document_disposition":"preserve","question":{"heading":15,"title":"Open questions","classification":"unresolved","action":"retain","reason":"Retain: CLI prefix resolution is implemented, named per-verb seams exist and ErrorCode maps typed removal errors. Unknown: future typed creation errors or operator need for success logging; no server prefix-resolution requirement is created."}},
{"document":"docs/specs/architecture/99-cli-sessions-rm.md","classification":"unresolved","evidence":["E20"],"document_disposition":"preserve","question":{"heading":18,"title":"Open questions","classification":"unresolved","action":"retain","reason":"Retain: shared resolver exists; rm still surfaces list/remove races, uses archive/purge booleans and resolves ambiguity client-side. Unknown: desired retry/enum ergonomics or a server-side resolver; current behavior alone cannot close those product deferrals."}},
{"document":"docs/specs/architecture/101-attach-payload-sessionid-server-routing.md","classification":"superseded/misleading","evidence":["E2"],"document_disposition":"annotate","question":{"heading":12,"title":"Open questions","classification":"historical and still useful","action":"retain","reason":"Retain recordingResolver test-double rationale as history; attach server routing was deleted, so this is no pending routing requirement."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"Historical terminal design: #1348 removed internal/supervisor and the attach/bridge path; #1535 removed the attach/resize wire types. Current sessions use Runner/RunnerFactory, and pyry attach returns a removal error. See [current runner ownership](../../knowledge/features/sessions-package-key-types-runner-interface-runnerfactory.md) and [control plane](../../knowledge/features/control-plane.md)."}},
{"document":"docs/specs/architecture/102-attach-cli-positional.md","classification":"superseded/misleading","evidence":["E2","E25"],"document_disposition":"annotate","question":{"heading":14,"title":"Open questions","classification":"superseded/misleading","action":"retain","reason":"Retain lowercase-helper rationale as history. The extra-argument deferral is resolved: current runStatus, runLogs and runStop reject rest before dialing; terminal removal separately makes attach positional plumbing obsolete."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"Historical terminal design: #1348 removed internal/supervisor and the attach/bridge path; #1535 removed the attach/resize wire types. Current sessions use Runner/RunnerFactory, and pyry attach returns a removal error. See [current runner ownership](../../knowledge/features/sessions-package-key-types-runner-interface-runnerfactory.md) and [control plane](../../knowledge/features/control-plane.md). The extra-argument deferral is also resolved: runStatus, runLogs and runStop reject extra arguments before dialing."}},
{"document":"docs/specs/architecture/106-e2e-restart-primitive.md","classification":"superseded/misleading","evidence":["E21","E3"],"document_disposition":"annotate","question":{"heading":17,"title":"Open questions","classification":"historical and still useful","action":"approve removal","reason":"Resolved boilerplate. Restart persistence and stale-socket behavior remain supported; the mention of reconcileBootstrapOnNew is obsolete under #839 and covered by a notice. No unique decision in section."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"Historical startup design: #839 removed newest-JSONL bootstrap adoption and #1149 removed mostRecentJSONL. Bootstrap spawning uses an explicit persisted ID; do not copy the retired reconciliation step. See [startup reconciliation history](../../knowledge/features/jsonl-reconciliation.md) and [current ID flag selection](../../knowledge/features/streamsup-package-buildargs-the-id-flag-inversion-that-keeps.md)."}},
{"document":"docs/specs/architecture/107-e2e-restart-evicted-and-lastactiveat.md","classification":"historical and still useful","evidence":["E21"],"document_disposition":"preserve","question":{"heading":18,"title":"Open questions","classification":"historical and still useful","action":"approve removal","reason":"Resolved boilerplate; lifecycle strings, path, timestamp and warm-start decisions remain in Design and restart tests."}},
{"document":"docs/specs/architecture/111-e2e-corrupt-registry.md","classification":"historical and still useful","evidence":["E22"],"document_disposition":"preserve","question":{"heading":12,"title":"Open questions","classification":"historical and still useful","action":"approve removal","reason":"Resolved boilerplate; corrupt-registry preservation and failure assertions remain in Design and startup test."}},
{"document":"docs/specs/architecture/115-e2e-idle-eviction-lazy-respawn.md","classification":"superseded/misleading","evidence":["E23"],"document_disposition":"annotate","question":{"heading":11,"title":"Open questions","classification":"superseded/misleading","action":"retain","reason":"Retain: socket injection and timeout margins are useful rationale. sessions list now exists; unknown whether an additional list-based cross-check is still desired after test evolution."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"The missing-list discussion is historical: pyry sessions list is implemented; pyry list is not the command. Socket injection and timing rationale remain historical test guidance. See [current session-list contract](../../knowledge/features/control-plane-sessions-list-seam-1-1b-b1.md)."}},
{"document":"docs/specs/architecture/116-e2e-cap-eviction-and-interleave.md","classification":"historical and still useful","evidence":["E5"],"document_disposition":"preserve","question":{"heading":13,"title":"Open questions","classification":"historical and still useful","action":"retain","reason":"Retain: integer cap, package-level cap=1/uncapped coverage and timing margins are useful decisions. Optional future test redundancy/tolerance changes require observed need."}},
{"document":"docs/specs/architecture/118-rotation-symlink-resolution.md","classification":"superseded/misleading","evidence":["E4"],"document_disposition":"annotate","question":{"heading":13,"title":"Open questions","classification":"superseded/misleading","action":"approve removal","reason":"Obsolete no-question statement; symlink design and fallback discipline remain in Design, while its watcher and test were retired. No unique decision or surviving path requirement is removed."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"Historical rotation design: #2137 removed the fsnotify/open-descriptor watcher and its rotation test. Current conversation_reset handling uses sessionResetFollower and Pool.AdoptAnnouncedID. See [rotation retirement and replacement](../../knowledge/features/rotation-watcher.md)."}},
{"document":"docs/specs/architecture/120-e2e-clear-rotation-watcher.md","classification":"superseded/misleading","evidence":["E4","E16"],"document_disposition":"annotate","question":{"heading":13,"title":"Open questions","classification":"historical and still useful","action":"retain","reason":"Retain: registry-only proof versus nonexistent single-word list is useful historical AC reasoning; current command is sessions list. Trigger placement and short socket paths remain useful history. Watcher-specific requirements ended with #2137."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"Historical rotation design: #2137 removed the fsnotify/open-descriptor watcher and its rotation test. Current conversation_reset handling uses sessionResetFollower and Pool.AdoptAnnouncedID. See [rotation retirement and replacement](../../knowledge/features/rotation-watcher.md). The current enumeration command is pyry sessions list, not pyry list."}},
{"document":"docs/specs/architecture/122-fake-claude-test-binary.md","classification":"superseded/misleading","evidence":["E24","E4"],"document_disposition":"annotate","question":{"heading":12,"title":"Open questions","classification":"unresolved","action":"retain","reason":"Retain: optional rotation debug output and polling knob are useful YAGNI rationale. Legacy fake rotation still exists; unknown observed debugging/latency need, so no knob is authorized."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"Historical watcher motivation: #2137 removed the production fsnotify/open-descriptor watcher. The fake-claude rotation mode and StartRotation harness still exist; their existence does not prove a live watcher. See [current fake-claude modes](../../knowledge/features/fakeclaude-binary.md) and [rotation retirement](../../knowledge/features/rotation-watcher.md)."}},
{"document":"docs/specs/architecture/123-e2e-startrotation-primitive.md","classification":"superseded/misleading","evidence":["E24","E4"],"document_disposition":"annotate","question":{"heading":11,"title":"Open questions","classification":"unresolved","action":"retain","reason":"Retain: mkdir and spawnWith choices are implemented and worth preserving; StartRotation remains fixed-arity. Unknown concrete need for variadic flags; watcher retirement does not retire the still-used fake/harness primitive."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"Historical watcher motivation: #2137 removed the production fsnotify/open-descriptor watcher. The fake-claude rotation mode and StartRotation harness still exist; their existence does not prove a live watcher. See [current fake-claude modes](../../knowledge/features/fakeclaude-binary.md) and [rotation retirement](../../knowledge/features/rotation-watcher.md)."}},
{"document":"docs/specs/architecture/125-e2e-attach-pty-harness.md","classification":"superseded/misleading","evidence":["E2"],"document_disposition":"annotate","question":{"heading":14,"title":"Open questions","classification":"historical and still useful","action":"retain","reason":"Retain: banner ordering, exit-code exposure and temporary-home limits are useful history. They refer to the deleted AttachHarness; no surviving attach requirement."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"Historical terminal design: #1348 removed internal/supervisor and the attach/bridge path; #1535 removed the attach/resize wire types. Current sessions use Runner/RunnerFactory, and pyry attach returns a removal error. See [current runner ownership](../../knowledge/features/sessions-package-key-types-runner-interface-runnerfactory.md) and [control plane](../../knowledge/features/control-plane.md)."}},
{"document":"docs/specs/architecture/126-e2e-attach-handles-sigwinch.md","classification":"superseded/misleading","evidence":["E2"],"document_disposition":"annotate","question":{"heading":19,"title":"Open questions","classification":"historical and still useful","action":"retain","reason":"Retain: handshake versus live-resize proof, zero-dimension scope and ioctl retry rationale are useful retired-test decisions. No current attach-resize requirement."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"Historical terminal design: #1348 removed internal/supervisor and the attach/bridge path; #1535 removed the attach/resize wire types. Current sessions use Runner/RunnerFactory, and pyry attach returns a removal error. See [current runner ownership](../../knowledge/features/sessions-package-key-types-runner-interface-runnerfactory.md) and [control plane](../../knowledge/features/control-plane.md)."}},
{"document":"docs/specs/architecture/127-e2e-attach-detach-clean.md","classification":"superseded/misleading","evidence":["E2"],"document_disposition":"annotate","question":{"heading":12,"title":"Open questions","classification":"superseded/misleading","action":"retain","reason":"Retain: obsolete none-blocking text includes the sole exact cmd/pyry/main.go historical citation; removal would erase that pointer. Detach contract and triple invariant remain historical Context/Design."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"Historical terminal design: #1348 removed internal/supervisor and the attach/bridge path; #1535 removed the attach/resize wire types. Current sessions use Runner/RunnerFactory, and pyry attach returns a removal error. See [current runner ownership](../../knowledge/features/sessions-package-key-types-runner-interface-runnerfactory.md) and [control plane](../../knowledge/features/control-plane.md)."}},
{"document":"docs/specs/architecture/128-e2e-attach-survives-claude-restart.md","classification":"superseded/misleading","evidence":["E2"],"document_disposition":"annotate","question":{"heading":9,"title":"Open questions","classification":"superseded/misleading","action":"retain","reason":"Retain: obsolete none-blocking text includes the sole exact internal/supervisor/supervisor.go historical citation; removal would erase that pointer. Independent backoff/bootstrap/drain deferrals stay in Out of scope (for follow-ups)."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"Historical terminal design: #1348 removed internal/supervisor and the attach/bridge path; #1535 removed the attach/resize wire types. Current sessions use Runner/RunnerFactory, and pyry attach returns a removal error. See [current runner ownership](../../knowledge/features/sessions-package-key-types-runner-interface-runnerfactory.md) and [control plane](../../knowledge/features/control-plane.md)."}},
{"document":"docs/specs/architecture/133-attach-sigwinch-emitter.md","classification":"superseded/misleading","evidence":["E2"],"document_disposition":"annotate","question":{"heading":16,"title":"Open questions","classification":"historical and still useful","action":"retain","reason":"Retain: deadline ownership, initial-size race, server coalescing and silent-client rationale are useful historical decisions; the whole attach/resize chain was deleted, not a current optimization backlog."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"Historical terminal design: #1348 removed internal/supervisor and the attach/bridge path; #1535 removed the attach/resize wire types. Current sessions use Runner/RunnerFactory, and pyry attach returns a removal error. See [current runner ownership](../../knowledge/features/sessions-package-key-types-runner-interface-runnerfactory.md) and [control plane](../../knowledge/features/control-plane.md)."}},
{"document":"docs/specs/architecture/136-bridge-resize-seam.md","classification":"superseded/misleading","evidence":["E2"],"document_disposition":"annotate","question":{"heading":18,"title":"Open questions","classification":"historical and still useful","action":"retain","reason":"Retain: status geometry deferral is obsolete for removed PTYs; clamp/logging rationale and rows/cols boundary choice are useful history. No current PTY dimensions requirement is inferred."},"notice":{"heading":"Historical review (#2987)","insert_before_heading":"Context","heading_occurrence":1,"wording":"Historical terminal design: #1348 removed internal/supervisor and the attach/bridge path; #1535 removed the attach/resize wire types. Current sessions use Runner/RunnerFactory, and pyry attach returns a removal error. See [current runner ownership](../../knowledge/features/sessions-package-key-types-runner-interface-runnerfactory.md) and [control plane](../../knowledge/features/control-plane.md)."}}
]}
```

### Missing-reference ledger

Each record is one scope document/target pair, with a representative exact source
heading and its occurrence within that document. Repeated path mentions do not add
records. Preserve all 37 paths as written; missing status is not a product defect.

### Missing-reference ledger JSON

```json
{"scope_commit":"bf3129a42d38d257a3085f05dde5079a275c84a6","reviewed_current_commit":"f2a41c4b5bf2e72437eb1dc97b47b8178ff86429","records":[
{"document":"docs/specs/architecture/101-attach-payload-sessionid-server-routing.md","target":"internal/control/attach_client.go","heading":"Out of scope (regression guards, not deliverables)","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/102-attach-cli-positional.md","target":"cmd/pyry/attach_test.go","heading":"`cmd/pyry/attach_test.go` — new file","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2","E25"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/102-attach-cli-positional.md","target":"internal/control/attach_client.go","heading":"2. `control.Attach` — accept `sessionID` argument","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2","E25"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/102-attach-cli-positional.md","target":"internal/control/attach_resolve_test.go","heading":"Testing strategy","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2","E25"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/102-attach-cli-positional.md","target":"internal/control/attach_test.go","heading":"`cmd/pyry/attach_test.go` — new file","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2","E25"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/118-rotation-symlink-resolution.md","target":"internal/sessions/rotation/watcher.go","heading":"Context","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E4"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/120-e2e-clear-rotation-watcher.md","target":"internal/e2e/rotation_test.go","heading":"Acceptance Criteria (from ticket)","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E4","E16"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/125-e2e-attach-pty-harness.md","target":"internal/e2e/attach_pty.go","heading":"NEW `internal/e2e/attach_pty.go`","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/125-e2e-attach-pty-harness.md","target":"internal/e2e/attach_pty_test.go","heading":"NEW `internal/e2e/attach_pty_test.go`","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/125-e2e-attach-pty-harness.md","target":"internal/supervisor/bridge_test.go","heading":"Context","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/125-e2e-attach-pty-harness.md","target":"internal/supervisor/supervisor_test.go","heading":"Context","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/126-e2e-attach-handles-sigwinch.md","target":"internal/e2e/attach_pty_test.go","heading":"Approach","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/126-e2e-attach-handles-sigwinch.md","target":"internal/supervisor/winsize.go","heading":"`internal/e2e/attach_pty_test.go` — extend `TestHelperProcess` echo mode","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/127-e2e-attach-detach-clean.md","target":"internal/e2e/attach_detach_test.go","heading":"Approach","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/127-e2e-attach-detach-clean.md","target":"internal/e2e/attach_pty.go","heading":"Public surface (delta only)","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/128-e2e-attach-survives-claude-restart.md","target":"internal/e2e/attach_pty.go","heading":"Approach","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/128-e2e-attach-survives-claude-restart.md","target":"internal/e2e/attach_pty_test.go","heading":"Approach","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/128-e2e-attach-survives-claude-restart.md","target":"internal/e2e/attach_restart_test.go","heading":"Approach","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/128-e2e-attach-survives-claude-restart.md","target":"internal/supervisor/supervisor.go","heading":"Open questions","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/133-attach-sigwinch-emitter.md","target":"internal/control/attach_client.go","heading":"Context","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/133-attach-sigwinch-emitter.md","target":"internal/control/attach_winsize_test.go","heading":"Testing strategy","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/133-attach-sigwinch-emitter.md","target":"internal/supervisor/winsize.go","heading":"One helper, internal to the package, called from `Attach`","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/136-bridge-resize-seam.md","target":"internal/control/attach_client.go","heading":"Caveat rewrites","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/136-bridge-resize-seam.md","target":"internal/control/attach_test.go","heading":"Control side (`internal/control/attach_test.go`)","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/136-bridge-resize-seam.md","target":"internal/e2e/attach_pty.go","heading":"Supervisor side (`internal/supervisor/bridge_test.go`)","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/136-bridge-resize-seam.md","target":"internal/supervisor/bridge_test.go","heading":"Supervisor side (`internal/supervisor/bridge_test.go`)","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/136-bridge-resize-seam.md","target":"internal/supervisor/supervisor.go","heading":"Caveat rewrites","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/27-session-addressable-runtime.md","target":"internal/supervisor/supervisor_test.go","heading":"Open questions (for the developer to resolve during build)","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/28-sessions-package.md","target":"internal/supervisor/","heading":"Wire / behaviour invariants (this slice introduces zero behaviour change)","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/39-live-rotation-watcher.md","target":"internal/sessions/rotation/","heading":"Why M, not split","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E4"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/39-live-rotation-watcher.md","target":"internal/sessions/rotation/probe.go","heading":"Key types and signatures","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E4"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/39-live-rotation-watcher.md","target":"internal/sessions/rotation/watcher.go","heading":"Key types and signatures","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E4"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/68-e2e-harness-primitive.md","target":"internal/e2e/sessions/","heading":"Package layout","heading_occurrence":1,"classification":"historical and still useful","evidence":["E10"],"reviewed_status":"missing","reason":"Optional planned feature-test directory; current tests are in internal/e2e, not evidence of missing implementation."},
{"document":"docs/specs/architecture/78-foreground-stdin-reader-leak.md","target":"internal/supervisor/supervisor.go","heading":"Context","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/78-foreground-stdin-reader-leak.md","target":"internal/supervisor/supervisor_test.go","heading":"Testing strategy","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/78-foreground-stdin-reader-leak.md","target":"internal/supervisor/winsize.go","heading":"Context","heading_occurrence":1,"classification":"superseded/misleading","evidence":["E2"],"reviewed_status":"missing","reason":"Retired terminal/watcher source; preserve exact historical citation and apply the document notice."},
{"document":"docs/specs/architecture/94-pool-remove-core.md","target":"internal/supervisor/supervisor_test.go","heading":"Tests","heading_occurrence":1,"classification":"historical and still useful","evidence":["E19"],"reviewed_status":"missing","reason":"Historical helper precedent; preserve citation rather than invent a modern replacement."}
]}
```

## Removal approvals

These nine eligible sections contain resolved/obsolete scaffolding only. Their
ledger reasons account for decisions and deferrals retained elsewhere. Sections
#127/#128 are deliberately unapproved because removal would lose unique historical
citations. #27 has no positive merged closing link and #88 has unsupported Markdown;
neither is approved. Other retained questions keep useful reasoning or conditional
requirements visible. This array is the complete batch authorization.

### Approval array JSON

```json
[
{"document":"docs/specs/architecture/19-stale-service-path-recovery.md","sha256":"6a01520f0c512a223fcbb570cb1eccfe16f969b794c2e04a4933a130bc927c81","heading":11},
{"document":"docs/specs/architecture/52-cli-verbs-e2e-coverage.md","sha256":"3d96923f49625117e4f14afd2cf7fc3d016cbadc8379689319fc66c4eb61f329","heading":14},
{"document":"docs/specs/architecture/87-control-sessions-list.md","sha256":"142f74cf16626bfc0d1cf62d65eb75e13becd7fc3d690f57561539b2363985dc","heading":15},
{"document":"docs/specs/architecture/94-pool-remove-core.md","sha256":"2e68272c79fbea6f3a64973a49ac9bf50b35e49c7d57c7b1125026034bec4f96","heading":20},
{"document":"docs/specs/architecture/95-pool-remove-jsonl-disposition.md","sha256":"cd86a5e46c1febba5f2aa63c06cb8c1ef7397fa0bef9beec956f309e83f297ee","heading":19},
{"document":"docs/specs/architecture/106-e2e-restart-primitive.md","sha256":"696734551d5a0a4199abd78189dba565eabfdbbe6e6d50dbf3d59a6fecbcad4f","heading":17},
{"document":"docs/specs/architecture/107-e2e-restart-evicted-and-lastactiveat.md","sha256":"0e4a8cd37219f393d7ac7c3d399a7ff3bacccb7e97fbbf01826bd818c6036c94","heading":18},
{"document":"docs/specs/architecture/111-e2e-corrupt-registry.md","sha256":"c81d2ad3c1dd7d7293da6b2fb9dec00d1c7bf7c738e6f0364a6c706f8ca1e889","heading":12},
{"document":"docs/specs/architecture/118-rotation-symlink-resolution.md","sha256":"33d4fba0fcee311741fb82bfb7c89b2d9161d13d93665264efe1f40410072145","heading":13}
]
```

## Batch-only offline preview

Generated with the existing `spec-scaffolding-prune` binary, reused evidence and
the exact approval array above, without `-apply`. Full output stays temporary.
The excerpt preserves every selected document and all its reported section actions,
including #88's diagnostic reading-list section, which remains retained. Its ranges
are diagnostic only. Counts are batch-only, not corpus-wide and not an apply report.

### Preview excerpt JSON

```json
{"checkout_commit":"1014206a9438177f2241e5003487c8e44ef86c0b","evidence_sha256":"68c4e1fe74e3c04a2f8b4a5756c86c4f270760b79f9806fb0a2d6ae36e0c3a9a","reviewed_current_commit":"f2a41c4b5bf2e72437eb1dc97b47b8178ff86429","scope_commit":"bf3129a42d38d257a3085f05dde5079a275c84a6","evidence":{"repository":"pyrycode/pyrycode","retrieved_at":"2026-10-08T11:43:30Z","provenance":"GitHub GraphQL via gh api graphql --paginate; repository.issues(first:100); repository.pullRequests(first:100,states:MERGED).closingIssuesReferences(first:100); 17 complete issue pages / 13 complete PR pages; all nested totalCount verified; query text in plan Revisions. issues-pages.json SHA256=bee202bfd534960d60d466d9e0cd43b09c1ab9aa94a79900fd0f971f5b91fa53 pr-pages.json SHA256=e1e091f28fe71df146da5c9fa952eae439a7d18996966b724e6706051fe52933"},"evidence_path":"cmd/spec-scaffolding-prune/testdata/2929-evidence.json","evidence_file_sha256":"f66798f583c42683fa08c66c00e584cd46553d57630b24109fa7ba56809105ce","scope":"batch 01 only; offline preview, never an apply report","counts_batch_only":{"scanned_documents":45,"eligible_documents":43,"proposed_removal_sections":9,"deferred_question_sections":34},"approval_diagnostics":[],"documents":[
{"document":"docs/specs/architecture/101-attach-payload-sessionid-server-routing.md","sha256":"ad2e5b14e70746231818b0dab2c4796ea8feac4fe868eda40db65bf6454a58dd","ticket":101,"reason":"eligible","sections":[{"heading":12,"title":"Open questions","start_byte":11982,"end_byte":12201,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/102-attach-cli-positional.md","sha256":"3202291d9f8b87eecb04794ba2a7f76dd87cd288fd18ac2e03f936596a915ea0","ticket":102,"reason":"eligible","sections":[{"heading":14,"title":"Open questions","start_byte":13125,"end_byte":13540,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/106-e2e-restart-primitive.md","sha256":"696734551d5a0a4199abd78189dba565eabfdbbe6e6d50dbf3d59a6fecbcad4f","ticket":106,"reason":"eligible","sections":[{"heading":17,"title":"Open questions","start_byte":16179,"end_byte":16424,"action":"remove","reason":"content-bound individual approval"}]},
{"document":"docs/specs/architecture/107-e2e-restart-evicted-and-lastactiveat.md","sha256":"0e4a8cd37219f393d7ac7c3d399a7ff3bacccb7e97fbbf01826bd818c6036c94","ticket":107,"reason":"eligible","sections":[{"heading":18,"title":"Open questions","start_byte":16597,"end_byte":16877,"action":"remove","reason":"content-bound individual approval"}]},
{"document":"docs/specs/architecture/111-e2e-corrupt-registry.md","sha256":"c81d2ad3c1dd7d7293da6b2fb9dec00d1c7bf7c738e6f0364a6c706f8ca1e889","ticket":111,"reason":"eligible","sections":[{"heading":12,"title":"Open questions","start_byte":9795,"end_byte":9909,"action":"remove","reason":"content-bound individual approval"}]},
{"document":"docs/specs/architecture/115-e2e-idle-eviction-lazy-respawn.md","sha256":"d6c1ed77c9bd0e4c67d557c9cc20911976c0362562373e0d6410e94146d3260a","ticket":115,"reason":"eligible","sections":[{"heading":11,"title":"Open questions","start_byte":14527,"end_byte":15332,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/116-e2e-cap-eviction-and-interleave.md","sha256":"4fd16c21b827e64cb69dc4e03c10dc1ffebcac816a2d84727f41d43816108831","ticket":116,"reason":"eligible","sections":[{"heading":13,"title":"Open questions","start_byte":20463,"end_byte":21525,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/118-rotation-symlink-resolution.md","sha256":"33d4fba0fcee311741fb82bfb7c89b2d9161d13d93665264efe1f40410072145","ticket":118,"reason":"eligible","sections":[{"heading":13,"title":"Open questions","start_byte":9212,"end_byte":9511,"action":"remove","reason":"content-bound individual approval"}]},
{"document":"docs/specs/architecture/120-e2e-clear-rotation-watcher.md","sha256":"355dfef58eb4b6555bbc5e24112548fe309e2e8a13240235f33a63f6e9575b28","ticket":120,"reason":"eligible","sections":[{"heading":13,"title":"Open questions","start_byte":9357,"end_byte":12019,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/122-fake-claude-test-binary.md","sha256":"195f6a5b40330fb944a6cf02c4c18b7be6abfe1c5fa0d1707321e3955ee211ea","ticket":122,"reason":"eligible","sections":[{"heading":12,"title":"Open questions","start_byte":15612,"end_byte":16274,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/123-e2e-startrotation-primitive.md","sha256":"a465acc5da016ce3d01458969e57c06c5b4502f101c897da78e4959152b83b47","ticket":123,"reason":"eligible","sections":[{"heading":11,"title":"Open questions","start_byte":11130,"end_byte":12331,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/125-e2e-attach-pty-harness.md","sha256":"e21c8bd71d23d8dfa2f674888320e29388913e5b32ec6df4e3a9cd69fc37a768","ticket":125,"reason":"eligible","sections":[{"heading":14,"title":"Open questions","start_byte":21948,"end_byte":23057,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/126-e2e-attach-handles-sigwinch.md","sha256":"fc8e543100d271b5af53ef812bf507e09d28cad8a95c6d1572f9959a196929b2","ticket":126,"reason":"eligible","sections":[{"heading":19,"title":"Open questions","start_byte":21674,"end_byte":22768,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/127-e2e-attach-detach-clean.md","sha256":"9779fbb592717cae6c8c83faa3e0451f550249dfaa2230364c9a5bcdc8f32a4f","ticket":127,"reason":"eligible","sections":[{"heading":12,"title":"Open questions","start_byte":10203,"end_byte":10397,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/128-e2e-attach-survives-claude-restart.md","sha256":"00d566e44a112153192810a0bb20b2864f601087da7a6ee78070752a265e4f3c","ticket":128,"reason":"eligible","sections":[{"heading":9,"title":"Open questions","start_byte":17448,"end_byte":17792,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/133-attach-sigwinch-emitter.md","sha256":"36160f4cda9aff7ce470da905039417c2b5b6903ff07e1019f28c1650f53dfa5","ticket":133,"reason":"eligible","sections":[{"heading":16,"title":"Open questions","start_byte":17631,"end_byte":19174,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/136-bridge-resize-seam.md","sha256":"b96d845559d6cdb6b3e10c4b8b89cbf7ed59124601ccb82b16ac18c6f273b3cd","ticket":136,"reason":"eligible","sections":[{"heading":18,"title":"Open questions","start_byte":14142,"end_byte":15009,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/19-stale-service-path-recovery.md","sha256":"6a01520f0c512a223fcbb570cb1eccfe16f969b794c2e04a4933a130bc927c81","ticket":19,"reason":"eligible","sections":[{"heading":11,"title":"Open questions","start_byte":8149,"end_byte":8351,"action":"remove","reason":"content-bound individual approval"}]},
{"document":"docs/specs/architecture/27-session-addressable-runtime.md","sha256":"1987c4af0d1633749bfc1d5b0eca7b3ad076241cbd0ad5feeee2bfb06eac52c2","ticket":27,"reason":"closed without positive merge evidence","sections":null},
{"document":"docs/specs/architecture/28-sessions-package.md","sha256":"2c0ec15b569da024beac0546ee1ad5d13975cc2a5277bed947cb75ce04fc8cad","ticket":28,"reason":"eligible","sections":null},
{"document":"docs/specs/architecture/38-startup-jsonl-reconciliation.md","sha256":"965f5c328cb5c85948703ae9f12d7d9a794b94232cfe109a6f279f07db518951","ticket":38,"reason":"eligible","sections":[{"heading":18,"title":"Open questions","start_byte":13414,"end_byte":14683,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/39-live-rotation-watcher.md","sha256":"f6f1737f61a5475bae0d1dab103506086708cae358769e89f7dc157d8d4b6bc0","ticket":39,"reason":"eligible","sections":[{"heading":27,"title":"Open questions","start_byte":25784,"end_byte":28027,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/41-concurrent-active-cap.md","sha256":"87b9c93916bfe0272d5de6bc3832c848b0fb7ab831f2dea0d0cea52ff8b8ed5b","ticket":41,"reason":"eligible","sections":[{"heading":14,"title":"Open Questions","start_byte":13916,"end_byte":14925,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/52-cli-verbs-e2e-coverage.md","sha256":"3d96923f49625117e4f14afd2cf7fc3d016cbadc8379689319fc66c4eb61f329","ticket":52,"reason":"eligible","sections":[{"heading":14,"title":"Open questions","start_byte":9470,"end_byte":9555,"action":"remove","reason":"content-bound individual approval"}]},
{"document":"docs/specs/architecture/60-pool-list-read-primitive.md","sha256":"b47bae8edb201de6eeb6125e6afeae940e24baf30f61cd275ba24c71bc65a0e8","ticket":60,"reason":"eligible","sections":[{"heading":12,"title":"Open questions","start_byte":10156,"end_byte":10639,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/62-pool-rename-write-primitive.md","sha256":"bb93e0d421a9b9182d9e1c8121cacf49e53161ca51b4c13d1125f269d59c1d14","ticket":62,"reason":"eligible","sections":[{"heading":14,"title":"Open questions","start_byte":11708,"end_byte":12663,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/66-pool-resolve-id.md","sha256":"273b38ec4006b0b5fbd25c99687ff5d078a36df07f0d9d941cb3cae0bccae2a3","ticket":66,"reason":"eligible","sections":[{"heading":16,"title":"Open questions","start_byte":15119,"end_byte":15883,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/68-e2e-harness-primitive.md","sha256":"2ea42a6d62916ef55c253d6dec3393f2fe0eeeb992a00d60464722f99a602817","ticket":68,"reason":"eligible","sections":[{"heading":16,"title":"Open questions","start_byte":10544,"end_byte":11163,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/69-e2e-cli-driver.md","sha256":"1ee642e401905a4d286ad614f90dc2c435de22fe2b82a195e5bf954f7adf95c0","ticket":69,"reason":"eligible","sections":[{"heading":14,"title":"Open questions","start_byte":11680,"end_byte":12568,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/72-pool-supervise-seam.md","sha256":"20289cda4ba3c06affd3d45078d1b136de2a7bdd7eaa2ab678ec2fe21d72e9c5","ticket":72,"reason":"eligible","sections":[{"heading":13,"title":"Open questions","start_byte":10444,"end_byte":10720,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/73-pool-create-primitive.md","sha256":"3c6e2de28ba7dc096d172cb48ac0ae202ef2c591ac45d2a7cff0fd4a176925fc","ticket":73,"reason":"eligible","sections":[{"heading":21,"title":"Open questions","start_byte":20286,"end_byte":20831,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/75-control-sessions-new.md","sha256":"7d6248ef9f734fd01f215cda5cc97024bbb002a66ed18ca588c1649960ec5116","ticket":75,"reason":"eligible","sections":[{"heading":15,"title":"Open questions","start_byte":23549,"end_byte":24473,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/76-cli-sessions-new.md","sha256":"29da33c5dd0588209d00e59ba97676c2d4556d45e4b028e7cc6c5c470f71636f","ticket":76,"reason":"eligible","sections":[{"heading":19,"title":"Open questions","start_byte":18767,"end_byte":19786,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/78-foreground-stdin-reader-leak.md","sha256":"5edc8fd4183888c892a920a80873c93aeef5efdb31812eaf89dfc2be8da40800","ticket":78,"reason":"eligible","sections":[{"heading":17,"title":"Open questions","start_byte":13970,"end_byte":14391,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/80-e2e-install-systemd-roundtrip.md","sha256":"b37327888622c92d10f889119c3ebb7ffd5b591bed68d7fcecc4a16188677730","ticket":80,"reason":"eligible","sections":[{"heading":17,"title":"Open questions","start_byte":14626,"end_byte":16119,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/81-e2e-install-launchd-roundtrip.md","sha256":"fdb930c6df426f1644bac46cbc899631108ed1a805a3b736143f4abcde074e6a","ticket":81,"reason":"eligible","sections":[{"heading":18,"title":"Open questions","start_byte":19461,"end_byte":20802,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/87-control-sessions-list.md","sha256":"142f74cf16626bfc0d1cf62d65eb75e13becd7fc3d690f57561539b2363985dc","ticket":87,"reason":"eligible","sections":[{"heading":15,"title":"Open questions","start_byte":21522,"end_byte":21861,"action":"remove","reason":"content-bound individual approval"}]},
{"document":"docs/specs/architecture/88-cli-sessions-list.md","sha256":"880be1363dacbad6f62c78fdc7f52cb32d6576b635d84169af9b79cf2f364458","ticket":88,"reason":"unsupported Markdown: fence indented under a container at line 528","sections":[{"heading":2,"title":"Files to read first","start_byte":731,"end_byte":5091,"action":"retain","reason":"unsupported Markdown: fence indented under a container at line 528"},{"heading":22,"title":"Open questions","start_byte":26670,"end_byte":26972,"action":"retain","reason":"unsupported Markdown: fence indented under a container at line 528"}]},
{"document":"docs/specs/architecture/90-control-sessions-rename.md","sha256":"03be8758d403e9be0d60f9f834ef3442965b8e29a9f110a6ceff8270e88404a8","ticket":90,"reason":"eligible","sections":[{"heading":15,"title":"Open questions","start_byte":29017,"end_byte":30491,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/92-cli-sessions-rename.md","sha256":"4ae8c08369c11e4e33031ab2b8595b72b13cbec34bacf55db0f0b56e57d0be6d","ticket":92,"reason":"eligible","sections":[{"heading":16,"title":"Open questions","start_byte":23816,"end_byte":24799,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/93-cli-sessions-rename-prefix.md","sha256":"8cd6fad46ad5668b618b146f68cbc7a0ebdf628323033d6155f09ebf13f3e8fa","ticket":93,"reason":"eligible","sections":[{"heading":15,"title":"Open questions","start_byte":23732,"end_byte":24714,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/94-pool-remove-core.md","sha256":"2e68272c79fbea6f3a64973a49ac9bf50b35e49c7d57c7b1125026034bec4f96","ticket":94,"reason":"eligible","sections":[{"heading":20,"title":"Open questions","start_byte":15673,"end_byte":15861,"action":"remove","reason":"content-bound individual approval"}]},
{"document":"docs/specs/architecture/95-pool-remove-jsonl-disposition.md","sha256":"cd86a5e46c1febba5f2aa63c06cb8c1ef7397fa0bef9beec956f309e83f297ee","ticket":95,"reason":"eligible","sections":[{"heading":19,"title":"Open questions","start_byte":20658,"end_byte":20863,"action":"remove","reason":"content-bound individual approval"}]},
{"document":"docs/specs/architecture/98-control-sessions-rm.md","sha256":"9e2e68197b8cdcc360e18fd8a58ec9de3da2f0f8ef39a5e53088338bd2f3de82","ticket":98,"reason":"eligible","sections":[{"heading":15,"title":"Open questions","start_byte":33878,"end_byte":35344,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/99-cli-sessions-rm.md","sha256":"64dea6f318eabfb1b5ebb9f8a0e29142bf1e44639647497f7bf41a9847086c7d","ticket":99,"reason":"eligible","sections":[{"heading":18,"title":"Open questions","start_byte":32405,"end_byte":33720,"action":"retain","reason":"unapproved nontrivial question"}]}
]}
```

## Documentation handoff — exact pending changes

Pending for the documentation stage:

- **Notices:** the 22 `notice` objects in the document ledger authorize only their
  exact path, insertion heading/occurrence and wording. This includes #106's obsolete
  startup-reconciliation mention even though its restart test remains useful.
- **Removals:** only the nine entries in “Approval array JSON”, at their exact
  document/hash/all-heading identities. After any edit, re-identify the same sections
  by their ledger title/body and regenerate hashes, ordinals and preview before
  removal. Never use #88's diagnostic range or the old backfill hashes as approval.
- **Preservation:** all historical bodies outside these ranges, all historical
  source citations, optional planned paths and all 34 retained question occurrences.
  No prose deduplication, whole-spec deletion, detector changes or product changes.
- **Unresolved requirements:** keep the conditional questions listed below visible;
  issue #2987 records them. Do not add implementation work to this batch.

Three evidence-backed durable corrections, pending for the documentation stage:

1. `docs/knowledge/features/jsonl-reconciliation.md`, heading “Startup JSONL
   Reconciliation (retired 2026-07-09, #839)”: replace the paragraph beginning
   “`/clear` reconciliation still works, unchanged in mechanism” with:
   **“Startup newest-JSONL adoption was retired by #839. Live fsnotify/open-descriptor
   reconciliation was retired by #2137. Current `conversation_reset` announcements
   are followed by `sessionResetFollower`, which re-keys through `Pool.AdoptAnnouncedID`;
   `Pool.RotateID` remains an exported historical/test seam with no production caller.
   See [rotation retirement and replacement](rotation-watcher.md).”** In heading
   “`Pool.RotateID` — the seam”, replace the final paragraph beginning “This is the
   single mutation point reused” with **“The retired watcher reused this seam.
   Current announced resets use `Pool.AdoptAnnouncedID`; `Pool.RotateID` has no
   production caller.”** In heading “`ClaudeSessionsDir` wiring in `cmd/pyry`
   (current)”, replace its body with **“`ClaudeSessionsDir` is transcript-path
   configuration; it does not enable startup adoption or a live rotation watcher.
   Both mechanisms are retired. Current reset following uses the session's
   `conversation_reset` stream announcement; see [rotation retirement and
   replacement](rotation-watcher.md). `DefaultClaudeSessionsDir` resolves symlinks
   before encoding the workdir.”** E3/E4 are the evidence; leave explicitly historical
   flows and citations intact.
2. `docs/knowledge/features/sessions-package.md`, heading “`internal/sessions`
   Package”: replace the two introductory paragraphs with **“The session-addressable
   runtime owns session identity, registry persistence and lifecycle through
   `Runner`/`RunnerFactory`. One `Pool` manages its bootstrap and additional sessions.
   `internal/supervisor` and terminal attach were removed in #1348; see
   [current runner ownership](sessions-package-key-types-runner-interface-runnerfactory.md).”**
   Under “Package Layout”, replace only the `session.go` description with
   **“session.go    Session: owns one Runner and its lifecycle”**. Under “Dependency
   Direction”, replace the diagram and paragraph with **“`internal/sessions` depends
   on its `Runner` interface and accepts implementations through `RunnerFactory`.
   It does not import `internal/control` or the deleted `internal/supervisor`.
   The daemon composition root supplies the runner factory and control adapters.”**
   Under “Production Consumers (Phase 1.0b)”, prepend **“Historical Phase 1.0b wiring
   follows. The bootstrap-only, terminal-attach and `--continue` statements describe
   that phase, not current behavior; current session ownership is documented under
   [Runner/RunnerFactory](sessions-package-key-types-runner-interface-runnerfactory.md).”**
   Preserve its phase-specific body and citations. Evidence: E2/E11 and `Pool.List`
   enumerates bootstrap plus minted sessions.
3. `docs/knowledge/features/sessions-package-key-types-pool-resolveid-1-1e.md`,
   heading “Pool.ResolveID (1.1e-A)”: replace the opening paragraph with
   **“`Pool.ResolveID` resolves a full UUID, unique prefix or empty bootstrap selector.
   The terminal attach consumer was removed in #1348. Current CLI rename/remove
   resolve selectors client-side through `resolveSessionIDViaList` before sending a
   canonical ID.”** Replace the paragraph beginning “No whitespace trimming” with
   **“No whitespace trimming. `Pool.ResolveID`, CLI positional parsing and
   `resolveSessionIDViaList` preserve the supplied string. Trimming requires an
   explicit caller policy; Go `flag` does not trim positional values.”** Evidence: E9/E18.

## Unresolved requirements retained on issue #2987

Twelve documents contain conditional product/test requirements whose need or policy
current implementation cannot settle. Keep their question sections. These are review
findings, not implementation tickets created by this batch:

- #68: whether child binaries should inherit race instrumentation, including whether
  CI always supplies an instrumented binary. Its optional sessions directory is historical.
- #75: whether remote creation needs rate limits, configurable operation budgets or
  additional successful-create logging.
- #76: operator demand for sessions help or a separate remote retry policy.
- #80: service-account linger requirements and demand to move PATH-only coverage
  into the default test gate; no systemd host experiment was performed.
- #81: observed headless macOS failure needing a `user/<uid>` fallback, launchctl
  output changes, or demand to split PATH-only coverage; no macOS experiment was performed.
- #90: third-consumer demand for factoring Create, a cancellable Rename API or
  operator demand for successful-rename logging.
- #92: operator demand for `--clear` or success output; prefix resolution itself is settled.
- #98: demand for typed creation errors or additional successful-remove logging.
- #99: desired retry/enum ergonomics or server-side prefix resolution; the shared
  helper itself is already implemented.
- #115: whether an additional `sessions list` cross-check is still wanted after the
  idle-eviction tests evolved; the formerly missing command is implemented.
- #122: observed debugging or latency need for fake rotation logging/polling knobs.
- #123: a concrete consumer needing variadic StartRotation flags; the primitive survives.

All retired-attach and retired-watcher-only deferrals are historical. Their useful
decisions stay in retained sections; they are not unimplemented current requirements.
#102's status/logs/stop extra-argument deferral is settled by E25. #27's own positive
merged closing link remains absent from reused evidence; do not infer one from its
children or current symbols, and do not prune it. No open dependency blocks this review.

## Coverage and validation result

Reviewed: **45 documents**. Proposed annotated: **22 documents**. Preserved historical
bodies/citations: **45 documents**. Proposed removals: **9 question sections in 9
documents**. Retained: **34 question occurrences**. Unresolved conditional requirements:
**12 documents**. Completely unchanged after all proposed documentation edits:
**16 documents**. These units overlap: two removal documents also receive notices
(#106/#118); eight unresolved documents also belong to the 16 completely unchanged
documents, and four unresolved documents receive notices. “Preserved” describes
historical content, not absence of scaffolding edits. Document classifications are
22 superseded/misleading, 8 unresolved and 15 historical/still useful; question
classifications can differ. The 37 missing-reference pairs in 16 documents and
43 question occurrences in 43 documents overlap and are never summed as documents.

Independent scope validation: zero omitted/duplicate document entries, zero omitted/
duplicate scope missing pairs, zero omitted/duplicate question identities. Every
scope ordinal matches reviewed bytes. All selected preview hashes match those bytes;
all 22 insertion headings exist uniquely and precede removal ranges; every owning
notice link resolves. All nine approvals are eligible, match exactly one intended
question removal and have zero diagnostics. No unsupported/ineligible document is
approved. The preview has 45 selected documents, 43 eligible documents, nine proposed
removal sections and 34 deferred question sections; #88's additional reading list is
retained. Repeated selected-document previews are byte-identical; the evolving
review plan changes only its own out-of-batch corpus record. No apply was run.

Passed: race tests for `cmd/spec-reference-inventory` and `cmd/spec-scaffolding-prune`,
`go vet ./...`, binary build to scratch and `git diff --check`. No production package
was modified. The verifier owns the full-module gate. No live-Claude test or
live artifact is required. Documentation edits above remain pending.

## Documentation completion (#2987)

The builder's pending handoff above is satisfied by the documentation-stage edits.
All 22 ledger notices are inserted at their exact paths, immediately before the
first `Context` heading, with the approved wording and owning links. The three
knowledge corrections are applied at the exact headings listed in the handoff:

- [jsonl-reconciliation.md](../../knowledge/features/jsonl-reconciliation.md):
  startup retirement, `Pool.RotateID` and current `ClaudeSessionsDir` wiring.
- [sessions-package.md](../../knowledge/features/sessions-package.md): introduction,
  layout, dependency direction and historical Phase 1.0b consumer qualifier.
- [Pool.ResolveID](../../knowledge/features/sessions-package-key-types-pool-resolveid-1-1e.md):
  current consumers and verbatim positional-whitespace policy.

After notice insertion, each of the same nine approved question bodies matched
its original reviewed body byte for byte. The refreshed full-document hashes and
all-heading ordinals below bind the pre-removal bytes; #106 moves from ordinal 17
to 18 and #118 from 13 to 14. The offline preview reports exactly nine intended
removals with empty approval diagnostics. Only those selected ranges are removed;
no corpus-wide apply is run. Original builder approvals remain historical evidence.

### Documentation-stage approval array JSON

```json
[
{"document":"docs/specs/architecture/19-stale-service-path-recovery.md","sha256":"6a01520f0c512a223fcbb570cb1eccfe16f969b794c2e04a4933a130bc927c81","heading":11},
{"document":"docs/specs/architecture/52-cli-verbs-e2e-coverage.md","sha256":"3d96923f49625117e4f14afd2cf7fc3d016cbadc8379689319fc66c4eb61f329","heading":14},
{"document":"docs/specs/architecture/87-control-sessions-list.md","sha256":"142f74cf16626bfc0d1cf62d65eb75e13becd7fc3d690f57561539b2363985dc","heading":15},
{"document":"docs/specs/architecture/94-pool-remove-core.md","sha256":"2e68272c79fbea6f3a64973a49ac9bf50b35e49c7d57c7b1125026034bec4f96","heading":20},
{"document":"docs/specs/architecture/95-pool-remove-jsonl-disposition.md","sha256":"cd86a5e46c1febba5f2aa63c06cb8c1ef7397fa0bef9beec956f309e83f297ee","heading":19},
{"document":"docs/specs/architecture/106-e2e-restart-primitive.md","sha256":"af9a3156020dff858e0d2006f94fe59bb31efcbdfde2384341b698065053564a","heading":18},
{"document":"docs/specs/architecture/107-e2e-restart-evicted-and-lastactiveat.md","sha256":"0e4a8cd37219f393d7ac7c3d399a7ff3bacccb7e97fbbf01826bd818c6036c94","heading":18},
{"document":"docs/specs/architecture/111-e2e-corrupt-registry.md","sha256":"c81d2ad3c1dd7d7293da6b2fb9dec00d1c7bf7c738e6f0364a6c706f8ca1e889","heading":12},
{"document":"docs/specs/architecture/118-rotation-symlink-resolution.md","sha256":"5ae6f88e6ee069c72c2255def9635b2883f7c3ed0a9a5860abf9204b768ad2f8","heading":14}
]
```

### Documentation-stage batch-only preview excerpt JSON

```json
{"checkout_commit":"1fd278b108dafb5f5bc4efd8e289c8bf58883f0d","evidence_sha256":"68c4e1fe74e3c04a2f8b4a5756c86c4f270760b79f9806fb0a2d6ae36e0c3a9a","scope_commit":"bf3129a42d38d257a3085f05dde5079a275c84a6","reviewed_current_commit":"f2a41c4b5bf2e72437eb1dc97b47b8178ff86429","working_tree_state":"After the 22 ledger notices, before the nine approved removals; selected hashes bind these uncommitted bytes on checkout_commit.","evidence":{"repository":"pyrycode/pyrycode","retrieved_at":"2026-10-08T11:43:30Z","provenance":"GitHub GraphQL via gh api graphql --paginate; repository.issues(first:100); repository.pullRequests(first:100,states:MERGED).closingIssuesReferences(first:100); 17 complete issue pages / 13 complete PR pages; all nested totalCount verified; query text in plan Revisions. issues-pages.json SHA256=bee202bfd534960d60d466d9e0cd43b09c1ab9aa94a79900fd0f971f5b91fa53 pr-pages.json SHA256=e1e091f28fe71df146da5c9fa952eae439a7d18996966b724e6706051fe52933"},"evidence_path":"cmd/spec-scaffolding-prune/testdata/2929-evidence.json","evidence_file_sha256":"f66798f583c42683fa08c66c00e584cd46553d57630b24109fa7ba56809105ce","scope":"batch 01 only; regenerated offline preview, never a corpus-wide apply report","counts_batch_only":{"scanned_documents":45,"eligible_documents":43,"proposed_removal_sections":9,"deferred_question_sections":34},"approval_diagnostics":[],"documents":[
{"document":"docs/specs/architecture/101-attach-payload-sessionid-server-routing.md","sha256":"9ac34ec85b74101d2dbcb42accd372d5996aabd8e5b742cc9b1980aad0c14bb0","ticket":101,"reason":"eligible","sections":[{"heading":13,"title":"Open questions","start_byte":12413,"end_byte":12632,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/102-attach-cli-positional.md","sha256":"54a4b605978344cb3dc28d6bcf60afe3432d2348d24a6db154deadb1a11b34aa","ticket":102,"reason":"eligible","sections":[{"heading":15,"title":"Open questions","start_byte":13672,"end_byte":14087,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/106-e2e-restart-primitive.md","sha256":"af9a3156020dff858e0d2006f94fe59bb31efcbdfde2384341b698065053564a","ticket":106,"reason":"eligible","sections":[{"heading":18,"title":"Open questions","start_byte":16622,"end_byte":16867,"action":"remove","reason":"content-bound individual approval"}]},
{"document":"docs/specs/architecture/107-e2e-restart-evicted-and-lastactiveat.md","sha256":"0e4a8cd37219f393d7ac7c3d399a7ff3bacccb7e97fbbf01826bd818c6036c94","ticket":107,"reason":"eligible","sections":[{"heading":18,"title":"Open questions","start_byte":16597,"end_byte":16877,"action":"remove","reason":"content-bound individual approval"}]},
{"document":"docs/specs/architecture/111-e2e-corrupt-registry.md","sha256":"c81d2ad3c1dd7d7293da6b2fb9dec00d1c7bf7c738e6f0364a6c706f8ca1e889","ticket":111,"reason":"eligible","sections":[{"heading":12,"title":"Open questions","start_byte":9795,"end_byte":9909,"action":"remove","reason":"content-bound individual approval"}]},
{"document":"docs/specs/architecture/115-e2e-idle-eviction-lazy-respawn.md","sha256":"65a6d39a49f7ee7fb014791a63580c63cb106dd7030f571f308cc12d0d05eb6b","ticket":115,"reason":"eligible","sections":[{"heading":12,"title":"Open questions","start_byte":14844,"end_byte":15649,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/116-e2e-cap-eviction-and-interleave.md","sha256":"4fd16c21b827e64cb69dc4e03c10dc1ffebcac816a2d84727f41d43816108831","ticket":116,"reason":"eligible","sections":[{"heading":13,"title":"Open questions","start_byte":20463,"end_byte":21525,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/118-rotation-symlink-resolution.md","sha256":"5ae6f88e6ee069c72c2255def9635b2883f7c3ed0a9a5860abf9204b768ad2f8","ticket":118,"reason":"eligible","sections":[{"heading":14,"title":"Open questions","start_byte":9523,"end_byte":9822,"action":"remove","reason":"content-bound individual approval"}]},
{"document":"docs/specs/architecture/120-e2e-clear-rotation-watcher.md","sha256":"41ddbcf3dc4218aaab4eb28cf82868b283d8be3af328ff0491c4448dcb70dd16","ticket":120,"reason":"eligible","sections":[{"heading":14,"title":"Open questions","start_byte":9738,"end_byte":12400,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/122-fake-claude-test-binary.md","sha256":"3cd7c3f47df755ff85b820e702f589acc7a5549887bf6b0e21593557b2768eec","ticket":122,"reason":"eligible","sections":[{"heading":13,"title":"Open questions","start_byte":16005,"end_byte":16667,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/123-e2e-startrotation-primitive.md","sha256":"d8693d71137ce3c83c0962e3e7427bbf5b6520ae9345d78d8a4df12890901ca2","ticket":123,"reason":"eligible","sections":[{"heading":12,"title":"Open questions","start_byte":11523,"end_byte":12724,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/125-e2e-attach-pty-harness.md","sha256":"dfd502080685bc2324cf725cde017a7eb54bfe18d3de92c2bcd5dce095045ac2","ticket":125,"reason":"eligible","sections":[{"heading":15,"title":"Open questions","start_byte":22379,"end_byte":23488,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/126-e2e-attach-handles-sigwinch.md","sha256":"08dfcd808d4a8f817abaf81452f08a620da041f7213f26026468400fcb5bf1c3","ticket":126,"reason":"eligible","sections":[{"heading":20,"title":"Open questions","start_byte":22105,"end_byte":23199,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/127-e2e-attach-detach-clean.md","sha256":"9bf0dda71bee1e61f26ce69134bd12f5f4eec84533527de9fd0c130c4e47b20d","ticket":127,"reason":"eligible","sections":[{"heading":13,"title":"Open questions","start_byte":10634,"end_byte":10828,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/128-e2e-attach-survives-claude-restart.md","sha256":"c6d5a5a02756d21bdbc17b7b3623a8ec72e903703d23208dd42fb07753a70ca9","ticket":128,"reason":"eligible","sections":[{"heading":10,"title":"Open questions","start_byte":17879,"end_byte":18223,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/133-attach-sigwinch-emitter.md","sha256":"6dde15497a54b936605442d14bb9531602510e4b8eca334275fd710c7095ab24","ticket":133,"reason":"eligible","sections":[{"heading":17,"title":"Open questions","start_byte":18062,"end_byte":19605,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/136-bridge-resize-seam.md","sha256":"ec7aee5632e5c4031b39ad04887cbbfd7e20149bc7926e377f9efc735efd4b9a","ticket":136,"reason":"eligible","sections":[{"heading":19,"title":"Open questions","start_byte":14573,"end_byte":15440,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/19-stale-service-path-recovery.md","sha256":"6a01520f0c512a223fcbb570cb1eccfe16f969b794c2e04a4933a130bc927c81","ticket":19,"reason":"eligible","sections":[{"heading":11,"title":"Open questions","start_byte":8149,"end_byte":8351,"action":"remove","reason":"content-bound individual approval"}]},
{"document":"docs/specs/architecture/27-session-addressable-runtime.md","sha256":"025490f41af1cc4b29c4340a5b45095551a649a75bd9e62bce9ec13b07b7b86c","ticket":27,"reason":"closed without positive merge evidence","sections":null},
{"document":"docs/specs/architecture/28-sessions-package.md","sha256":"8347e67ae5b11dd4063efb4a30eb25acbb2d2fe2978a598526d4fa45efb1c384","ticket":28,"reason":"eligible","sections":null},
{"document":"docs/specs/architecture/38-startup-jsonl-reconciliation.md","sha256":"1ad20be866d020881eb770c1e2c1c5c81f544336773e847861b944a0c4835702","ticket":38,"reason":"eligible","sections":[{"heading":19,"title":"Open questions","start_byte":13857,"end_byte":15126,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/39-live-rotation-watcher.md","sha256":"3abf82bd44d6cd3596cbd3d8684f4cb2305b7897cc9f7323555bae963f2389d8","ticket":39,"reason":"eligible","sections":[{"heading":28,"title":"Open questions","start_byte":26095,"end_byte":28338,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/41-concurrent-active-cap.md","sha256":"248bbd001e62b432ed8d7ce0009adf198af4197586e3e54235402c01cc7e99b8","ticket":41,"reason":"eligible","sections":[{"heading":15,"title":"Open Questions","start_byte":14205,"end_byte":15214,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/52-cli-verbs-e2e-coverage.md","sha256":"3d96923f49625117e4f14afd2cf7fc3d016cbadc8379689319fc66c4eb61f329","ticket":52,"reason":"eligible","sections":[{"heading":14,"title":"Open questions","start_byte":9470,"end_byte":9555,"action":"remove","reason":"content-bound individual approval"}]},
{"document":"docs/specs/architecture/60-pool-list-read-primitive.md","sha256":"b47bae8edb201de6eeb6125e6afeae940e24baf30f61cd275ba24c71bc65a0e8","ticket":60,"reason":"eligible","sections":[{"heading":12,"title":"Open questions","start_byte":10156,"end_byte":10639,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/62-pool-rename-write-primitive.md","sha256":"bb93e0d421a9b9182d9e1c8121cacf49e53161ca51b4c13d1125f269d59c1d14","ticket":62,"reason":"eligible","sections":[{"heading":14,"title":"Open questions","start_byte":11708,"end_byte":12663,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/66-pool-resolve-id.md","sha256":"32c7750fb4b6b57c2f9778dd8709683b0631f34c38ac6d6fe8fbcdb465646832","ticket":66,"reason":"eligible","sections":[{"heading":17,"title":"Open questions","start_byte":15474,"end_byte":16238,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/68-e2e-harness-primitive.md","sha256":"2ea42a6d62916ef55c253d6dec3393f2fe0eeeb992a00d60464722f99a602817","ticket":68,"reason":"eligible","sections":[{"heading":16,"title":"Open questions","start_byte":10544,"end_byte":11163,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/69-e2e-cli-driver.md","sha256":"1ee642e401905a4d286ad614f90dc2c435de22fe2b82a195e5bf954f7adf95c0","ticket":69,"reason":"eligible","sections":[{"heading":14,"title":"Open questions","start_byte":11680,"end_byte":12568,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/72-pool-supervise-seam.md","sha256":"20289cda4ba3c06affd3d45078d1b136de2a7bdd7eaa2ab678ec2fe21d72e9c5","ticket":72,"reason":"eligible","sections":[{"heading":13,"title":"Open questions","start_byte":10444,"end_byte":10720,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/73-pool-create-primitive.md","sha256":"3c6e2de28ba7dc096d172cb48ac0ae202ef2c591ac45d2a7cff0fd4a176925fc","ticket":73,"reason":"eligible","sections":[{"heading":21,"title":"Open questions","start_byte":20286,"end_byte":20831,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/75-control-sessions-new.md","sha256":"7d6248ef9f734fd01f215cda5cc97024bbb002a66ed18ca588c1649960ec5116","ticket":75,"reason":"eligible","sections":[{"heading":15,"title":"Open questions","start_byte":23549,"end_byte":24473,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/76-cli-sessions-new.md","sha256":"29da33c5dd0588209d00e59ba97676c2d4556d45e4b028e7cc6c5c470f71636f","ticket":76,"reason":"eligible","sections":[{"heading":19,"title":"Open questions","start_byte":18767,"end_byte":19786,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/78-foreground-stdin-reader-leak.md","sha256":"7e5b7cdbd82ccb38dfb036cd9596664fbd847045109544131821747584f15655","ticket":78,"reason":"eligible","sections":[{"heading":18,"title":"Open questions","start_byte":14401,"end_byte":14822,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/80-e2e-install-systemd-roundtrip.md","sha256":"b37327888622c92d10f889119c3ebb7ffd5b591bed68d7fcecc4a16188677730","ticket":80,"reason":"eligible","sections":[{"heading":17,"title":"Open questions","start_byte":14626,"end_byte":16119,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/81-e2e-install-launchd-roundtrip.md","sha256":"fdb930c6df426f1644bac46cbc899631108ed1a805a3b736143f4abcde074e6a","ticket":81,"reason":"eligible","sections":[{"heading":18,"title":"Open questions","start_byte":19461,"end_byte":20802,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/87-control-sessions-list.md","sha256":"142f74cf16626bfc0d1cf62d65eb75e13becd7fc3d690f57561539b2363985dc","ticket":87,"reason":"eligible","sections":[{"heading":15,"title":"Open questions","start_byte":21522,"end_byte":21861,"action":"remove","reason":"content-bound individual approval"}]},
{"document":"docs/specs/architecture/88-cli-sessions-list.md","sha256":"880be1363dacbad6f62c78fdc7f52cb32d6576b635d84169af9b79cf2f364458","ticket":88,"reason":"unsupported Markdown: fence indented under a container at line 528","sections":[{"heading":2,"title":"Files to read first","start_byte":731,"end_byte":5091,"action":"retain","reason":"unsupported Markdown: fence indented under a container at line 528"},{"heading":22,"title":"Open questions","start_byte":26670,"end_byte":26972,"action":"retain","reason":"unsupported Markdown: fence indented under a container at line 528"}]},
{"document":"docs/specs/architecture/90-control-sessions-rename.md","sha256":"03be8758d403e9be0d60f9f834ef3442965b8e29a9f110a6ceff8270e88404a8","ticket":90,"reason":"eligible","sections":[{"heading":15,"title":"Open questions","start_byte":29017,"end_byte":30491,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/92-cli-sessions-rename.md","sha256":"c810e15110c22ad4f289fe8ca4e158d78677bfa8d991862cada0d86053ac4298","ticket":92,"reason":"eligible","sections":[{"heading":17,"title":"Open questions","start_byte":24127,"end_byte":25110,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/93-cli-sessions-rename-prefix.md","sha256":"8cd6fad46ad5668b618b146f68cbc7a0ebdf628323033d6155f09ebf13f3e8fa","ticket":93,"reason":"eligible","sections":[{"heading":15,"title":"Open questions","start_byte":23732,"end_byte":24714,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/94-pool-remove-core.md","sha256":"2e68272c79fbea6f3a64973a49ac9bf50b35e49c7d57c7b1125026034bec4f96","ticket":94,"reason":"eligible","sections":[{"heading":20,"title":"Open questions","start_byte":15673,"end_byte":15861,"action":"remove","reason":"content-bound individual approval"}]},
{"document":"docs/specs/architecture/95-pool-remove-jsonl-disposition.md","sha256":"cd86a5e46c1febba5f2aa63c06cb8c1ef7397fa0bef9beec956f309e83f297ee","ticket":95,"reason":"eligible","sections":[{"heading":19,"title":"Open questions","start_byte":20658,"end_byte":20863,"action":"remove","reason":"content-bound individual approval"}]},
{"document":"docs/specs/architecture/98-control-sessions-rm.md","sha256":"9e2e68197b8cdcc360e18fd8a58ec9de3da2f0f8ef39a5e53088338bd2f3de82","ticket":98,"reason":"eligible","sections":[{"heading":15,"title":"Open questions","start_byte":33878,"end_byte":35344,"action":"retain","reason":"unapproved nontrivial question"}]},
{"document":"docs/specs/architecture/99-cli-sessions-rm.md","sha256":"64dea6f318eabfb1b5ebb9f8a0e29142bf1e44639647497f7bf41a9847086c7d","ticket":99,"reason":"eligible","sections":[{"heading":18,"title":"Open questions","start_byte":32405,"end_byte":33720,"action":"retain","reason":"unapproved nontrivial question"}]}
]}
```

### Documentation-stage validation

Checked the final bytes against the reviewed originals: 22 notice insertions and
nine approved section removals are the only changes to the 45 selected specs.
All 34 retained question bodies and every byte outside notices/removal ranges
are preserved. All 37 historical missing-reference pairs in 16 documents remain
unchanged. Exactly 29 specs changed; 16 remain byte-identical. The 12 documents
with unresolved conditional requirements retain their questions and the existing
issue record. Counts overlap as described in the original coverage result.

The regenerated preview contains 45 selected document hashes, 43 eligible
documents, nine removal sections and 34 deferred questions, with empty approval
diagnostics. After removal, the selected documents propose no further removals;
#27 remains merge-ineligible and #88 remains unsupported and unapproved. The
full corpus reports and scratch validation stay temporary. `make docs-guard` and
`git diff --check` pass. No document was added or deleted, so CATALOG and INDEX
need no change. Frozen archives, production code and tests are untouched.
