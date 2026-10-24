# Compact capture reload investigation

## Files read

- `internal/thread/compact_queries_test.go` → `TestCompactCapture`: five outcomes hold a context-tagged old query across publication and worker replacement.
- `internal/thread/store_test.go` → `testStoreWait`, `testThreadStore`: five-second polling bound and joined store cleanup.
- `internal/thread/store.go` → `Store.run`, `Store.publish`, `Store.Unload`: recovery, publication admission and worker identity checks.
- `internal/thread/store_resume.go` → `Store.resumeCheckpoint`, `checkpointWriter.reconcileResume`: saved-state validation and resumed index reconciliation.
- `internal/thread/store_checkpoint.go` → `Store.dirtyCheckpoint`, `workerCheckpoint.commit`: dirty recovery and clean retirement.
- `internal/thread/checkpoint_write.go` → `checkpointWriter.run`, `checkpointWriter.replaceState`: existing phase hooks around durable operations.
- `internal/thread/cache.go` → `Store.writeCacheFile`: scalar file sync precedes replacement and directory sync.
- `docs/knowledge/features/thread-package.md`, background-store and cache-and-epochs topics: checkpoint validation precedes usability; snapshots may render history outside the mutex.
- `docs/knowledge/features/development-verification-test-execution-and-artifact-survival.md`: retain actual failed evidence; a focused pass cannot attribute historical contention.

## Context

At `8cb3fc0c8f86825ac176bdef8b50e3669130185a`, Linux
`TestCompactCapture/success` remained rebuilding at version zero five seconds
after reload. Its first retirement checkpoint appeared 7.59 seconds after entry;
the next subcase also took 3.66 seconds. Neither establishes the blocked phase.
The original `/tmp/builder-3851/compatibility.json` and downloaded builder bundle
`fcfc22aeb09dc922275c269e24fa6802d8b6d7e8fd33a298307ad4f1f8027690`
remain failed evidence. Original JSON SHA-256:
`8c33f2a9d6140e12319cc356b7323fb861d390efbf631394af5126770f7219eb`.

One deliverable: bounded usable reload preserving the held query's captured
state, with a measured diagnosis and its regression. No decision record yet.
Initial sketch and this plan fit approximately 350 written lines, zero exported
types, zero consumer migrations, three acceptance criteria and no new reject
branches. Reassess if the measured cause requires more.

Branch overlap: #3851 adds task association in `initialPublicationIndex`; it
does not change the capture fixture or recovery control flow. Build through it.

## Design

First add fixture-local observation using existing lookup, reader, checkpoint
and filesystem hooks. Record elapsed operation/phase times without payloads,
capture worker stacks on failure, and retain Linux load/context alongside JSON.
Preserve real checkpoint/history operations and the five-second readiness bound.
Compare capture alone with its compact preparation/bounded-work predecessors.
Use fresh histories per subcase; build caches may be warm and OS page-cache state
is uncontrolled. A wider diagnostic, if necessary, requires recovery assessment.

Do not select a synchronization or persistence repair from a green repetition.
Once observations establish a cause, record the exact cause-specific contract
and controlled regression in Revisions before implementing the repair.
Retain the original five outcomes and detached result assertions.

## Concurrency model

Store workers and their tail forwarders retain existing cancellation/join paths.
The fixture holds only the context-tagged compact request outside Store locks.
Diagnostic state uses a separate mutex; hook configuration precedes worker start.
Failure cleanup releases the request barrier, cancels and joins the query before
store shutdown. No diagnostic background goroutines are needed.

## State transitions and identity reuse

| Event | Race coverage |
| --- | --- |
| Old query held while version 2 advances to 3 | `TestCompactCapture` (all outcomes) |
| Unload and reload the same conversation while old query is held | `TestCompactCapture/success`, `/failure`, `/cancel` |
| Old failure after replacement must not withdraw new worker | `TestCompactCapture/failure` |
| Current failure withdraws current publication | `TestCompactCapture/current-failure` |
| Cancellation returns no partial result | `TestCompactCapture/cancel`, `/current-cancel` |
| Unrelated conversation survives all transitions | `TestCompactCapture` (all outcomes) |

## Error handling

Keep source failures generic and atomic. Diagnostics identify operations and
timings, never raw errors, payloads or credentials. A timeout must still fail;
cleanup releases held test work. Do not add retries, sleeps or relaxed bounds.

## Testing strategy

Baseline: admitted Linux `-race -json -count=10 -run '^TestCompactCapture$'`
at `368e1580653d29140beb5887a874b45a1a0f4656`, with retained JSON in
`/tmp/builder-3854/baseline-admitted.json`. Then measure instrumented capture and
the named compact predecessors. A controlled before/after check must distinguish
the eventual repair. Run affected thread race checks, `go vet ./...` and
`go build ./cmd/pyry`; complete acceptance remains dispatcher-owned.

## Open questions

- Which phase accounts for the timeout: history/checkpoint I/O, publication,
  fixture synchronization or scheduler/load delay? Unresolved before diagnosis.
- Does the original predecessor sequence reproduce it without unrelated packages?

## Security review

**Verdict:** PASS for observation; review any production repair before implementing.

**Findings:**

- Trust boundaries/files: observation preserves `readCheckpoint`, checksum validation, contained no-follow file admission and durable dirty marking; no input becomes newly trusted.
- Tokens/cryptography: no changes to epoch generation or certificate evidence. Diagnostics exclude tokens and content.
- Subprocesses/network: no product subprocess or network behavior changes; checks use managed admission.
- I/O/errors/logs: SHOULD FIX: record phase names/timings and failure stacks only in fixture output; do not expose raw source errors or payloads.
- Concurrency: SHOULD FIX: release, cancel and join the test query on every exit. Configure hooks before workers; serialize diagnostic records separately from Store locks.
- Threat model: local test-only observations add no remote authority; any later recovery repair requires renewed adversarial review.

**Reviewer:** builder self-review. **Date:** 2026-10-24.

## Revisions

### 2026-10-24 — observation implementation, repair still unresolved

The unchanged capture passed 60/60/0/0 run/pass/fail/skip events over ten
repetitions. The diagnostic fixture plus original compact preparation,
bounded-work and lifecycle predecessors passed 90/90/0/0 events over ten
repetitions (`/tmp/builder-3854/sequence.json`). Those measurements used the
plan commit plus uncommitted diagnostic changes, before environment logging.
Histories were fresh; build caches were warm; OS page-cache state was not reset.
Version-three waits were 0.335–35.264 ms; the 30 actual unloads were
3.479–16.098 ms. Checkpoint state syncs were 0.005–0.041 ms and resume syncs
0.005–0.021 ms. These observations do not identify the original slow phase.

The shell reports an overlay root with `fsync=volatile`; original filesystem
and pressure state were not captured. Add runtime/temporary-filesystem logging
inside the fixture before comparing I/O conclusions. The original success
subcase's unmodified JSON events are retained in
`internal/thread/testdata/3854-original-reload-failure.jsonl`; its parent source
and complete-file hash remain in Context. No timeout or recovery behavior has
been repaired. Do not treat the diagnostic passes as reliability acceptance.

### 2026-10-24 — preserved partial handoff

Exact diagnostic candidate `5156e970e937bf9710f605979ddc523365689e54` passed
6/6/0/0 race events (all five outcomes) after 941 seconds of shared admission
waiting. It reports Go 1.26.9, Linux/amd64, GOMAXPROCS 6, load averages
8.26/8.87/9.15 and `/tmp` on overlay with `fsync=volatile`. Actual reloads took
11.322–14.801 ms while old queries remained held. This cannot establish durable
I/O latency or identify the original timeout phase. Static checks and build passed.
Counts, commands, hashes, limitations and retained paths are recorded in
`internal/thread/testdata/3854-investigation.json`.

Recovery assessment
`a7c382d77587bb71528c392dcfad1a1f40fb5878c280bbeb96e891d57be7c675`
is queued for matched Linux conditions with the original concurrent daemon/relay
selection; it executed zero tests. Draft PR #3855 preserves the work. Both Open
questions, diagnosis, repair and its controlled regression remain unresolved.
No production change or reliability acceptance is claimed. Complete acceptance
remains dispatcher-owned; macOS is outside this Linux ticket's verification scope.

### 2026-10-24 — bounded recovery comparison plan

Recovery starts at `b3bd1ac5c7c2aac5dd4ee6c355f899f5b6b63e0b`, which contains
accepted producer `ea1dcb1fce648fc80b2cf8962f15a718477e2fa8` through main.
No overlapping feature branch changes the diagnostic files. Keep the original
failure and draft PR #3855. This remains one defect and its regression, with
approximately 600 total written lines including preserved work, zero exports,
zero consumer migrations, three criteria and no added rejection branches.

Add an optional package-private phase observer in `Store`, configured before
workers start, with begin/end observations outside Store locks. Observe history
bound probing, checkpoint read/import and validation, scalar/state cache writes
including the actual file Sync, restored-index preparation, capture and publish.
The fixture labels each observation with conversation and worker identity.
Its readiness observer records Status separately from the existing Snapshot
polling, without changing the five-second bound or rendering assertions.
Observe the actual fixture directory's mount, runtime, pressure and load.

Run admitted Linux race comparisons at a pushed exact revision, with fresh
histories under task-owned ext4 `/work/builder-3854-tmp` (not volatile `/tmp`):
capture alone; compact preparation/bounded-work/lifecycle preceding capture;
then only that sequence plus the daemon/relay selectors in the retained request.
Build caches are warm after compilation; OS page caches remain uncontrolled.
No cache dropping or synthetic stress is planned. Retain JSON, counts, hashes,
ordering and timeout stacks. A green comparison still leaves attribution open.

Security re-review: PASS for observation only. `readCheckpoint` admission,
checksum validation, no-follow containment, write ordering and errors remain
unchanged. Hooks receive no content or credentials, run outside Store locks,
and add no goroutines/network/subprocess authority or crypto changes. SHOULD
FIX: identify workers under the Store lock then release it before recording;
serialize diagnostic state independently, and join Store shutdown before dumping.
Any cause-specific repair still needs evidence and a prospective revision.

### 2026-10-24 — durable comparison results, no demonstrated repair

Pushed diagnostic source `4c09ab74fe0415b3c7cb6c6cfb53a5cf6d0a32cf`
contains the accepted producer. On Linux 7.0.0-28 / Go 1.26.9 / GOMAXPROCS 6,
the actual fixture reports ext4 `/work`, `rw,relatime`, without volatile sync.
All runs used admitted race checks and fresh histories; OS page caches were
uncontrolled. Capture alone passed 60/60/0/0 events; the named predecessor
sequence 90/90/0/0; requested concurrent selectors 1930/1930/0/0 twice;
the exact original compatibility selection 2250/2250/0/0. Each ran ten times.
The first combined run started daemon/relay 46 seconds after thread while
compiling. The warm repeat and original selection started within 1.5 seconds;
actual intervals, selectors, ordering, counts and hashes are retained in
`internal/thread/testdata/3854-durable-comparison.json`. Raw JSON, pressure/I/O
observations and per-reload analysis remain under `/tmp/builder-3854/`.

All 150 actual replacements were distinct workers and reached usable version 3
before releasing the old query, in 43.721–302.808 ms. All five outcomes retained
their contracts. The slowest reload spent 175.134 ms writing the scalar cache,
including 101.929 ms file Sync and 68.837 ms directory sync; validation took
2.757 ms and publication 0.049 ms. In the exact original selection checkpoint
import stayed below 0.201 ms and Snapshot below 3.236 ms. Readiness samples
preceded successful rendered reloads by at most 4.834 ms when observed; three
publications advanced between the Status sample and Snapshot capture.

These measurements cover the formerly omitted phases and meaningful durable
I/O, but reproduce no five-second stall. Wall times include scheduling; loadavg
ranged 5.38–11.01. Historical filesystem/load and blocked stacks remain unknown,
so neither persistence nor scheduling is established as the original cause.
No cause-specific repair or distinguishing regression is claimed. Both original
Open questions remain unresolved. Vet and build passed at the diagnostic source.
Keep PR #3855 draft and complete acceptance pending; do not mark this ticket built.

The deeper native syscall/scheduler diagnostic request at pushed evidence revision
`590d577230677a419e217a1beb353b9509fcd2dc` was rejected by the managed client:
`Task request unavailable or outside this admitted ticket`. It scheduled no work
and has no assessment ID; do not treat it as queued. The request is preserved at
`/tmp/builder-3854/recovery-investigation-request.json`. No alternate route or
retry was attempted. The earlier `a7c382d7` assessment produced this recovery
brief, not diagnostic execution. Current role work remains incomplete.

The final admitted race check of `./internal/thread/...` finished in 586.955 s:
2209 executed / 2207 passed / 0 failed / 2 existing opt-in skips
(`TestCheckpointResumeCorpus` and `TestReferenceDiskHeap`). Go source remained
`4c09ab74`; only these spec/evidence records changed. Command, raw JSON hash and
skip reasons are retained with the comparisons. This package pass does not
resolve the original failure or complete dispatcher-owned acceptance.

### 2026-10-24 — manual request-format correction

The managed deeper-request failure has a reproduced validation defect. Its
`narrowerInsufficient` explanation is 2017 characters; `validateInvestigation`
permits at most 2000. The same installed validator rejects the original and
accepts a 1967-character explanation after removing one redundant sentence
about the earlier assessment. Revision, failed test, retained evidence and all
selectors are unchanged. No permission or test-selection policy was relaxed.

The original rejected request and a concise copy are retained under
`/work/Projects/pyrycode-agents/logs/evidence/manual-3854-client-format-20261024`.
The same-scope request is committed as
`internal/thread/testdata/3854-next-investigation.json`. It names the already
pushed evidence revision `590d577230677a419e217a1beb353b9509fcd2dc`; Go source
remains the measured `4c09ab74`. Before submitting, the next admitted builder
should confirm that source relationship and use its own normal task client.
A completed worker's credentials must not be reused. The concise copy has not
been submitted or accepted, and has executed zero tests. Any further diagnostic
still needs recovery assessment and normal test admission.

This is manual author validation of request shape. It is not independent
review, a causal reload diagnosis or a reliability fix. Keep the rejected
request and original timeout failed. Existing five-second readiness, captured
state, isolation, cancellation, security and required review/checks remain.
Do not repeat the completed durable comparisons merely to obtain another green
run. The ticket's numeric written-line and production-file estimates can be
removed because they do not describe a user outcome or establish diagnosis.

### 2026-10-24 — corrected request accepted for recovery assessment

At branch head `0592076856febe984e49554bc217567a273a70a3`, this builder
confirmed that request revision `590d577230677a419e217a1beb353b9509fcd2dc`
contains accepted producer `ea1dcb1fce648fc80b2cf8962f15a718477e2fa8`.
Its only differences from measured source `4c09ab74` are the plan and retained
comparison JSON; no Go source differs between those two revisions.

The current run's normal managed task client submitted the committed corrected
request unchanged. It returned `assessment-queued`, assessment
`6098a38565127e3c02df1549df846f42fe81cd08db4aa10d4d38da3780938c3d`,
and `executed: 0`. The receipt and request hash are retained in
`internal/thread/testdata/3854-investigation-receipt.json`. The earlier malformed
request remains rejected; the original timeout remains failed. This receipt
queues recovery assessment after this worker ends, starts no diagnostic, and
does not authorise any additional execution.

No Go source, security boundary, fixture assertion or deadline changed in this
continuation. No tests were repeated: the completed comparisons and package
check remain historical evidence at their recorded revisions. Both Open
questions, causal diagnosis, supported repair and distinguishing regression
remain unresolved. Preserve draft PR #3855 and all prior work. Recovery must
select the next bounded diagnostic; ordinary review and dispatcher-owned
complete acceptance remain pending. This ticket is not built.
