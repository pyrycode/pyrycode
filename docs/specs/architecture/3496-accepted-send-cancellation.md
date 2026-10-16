# Accepted-send cancellation and peer identity (#3496)

## Files read

- `internal/transport/wssclient.go` → `Client.sendContext`, `sendRequest`, `Client.sendPump`, `Client.serve`: bounded admission, disposal acknowledgement and exact-socket retirement.
- `internal/transport/send_accepted_test.go` → `TestClientSendContextAcceptedCancellation`, `testAcceptedSendGate`: accepted-write barrier and peer retirement fixture.
- `internal/transport/send_context_test.go` → `TestClientSendContextDelivery`, `TestClientSendContextPumpShutdown`: I/O cancellation and shutdown coverage.
- `internal/transport/generation_test.go` → `TestGenerationAdmission`: stale-generation rejection and reconnect coverage.
- `docs/knowledge/features/transport-package.md` § Lifecycle and goroutines, § Test surface: cancellation before admission must preserve the healthy connection; disposal after acceptance retires the exact socket.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change, § Test execution and artifact survival: controlled schedules and named counts are required.
- `CODING-STYLE.md`: bounded goroutines, contract comments and scoped changes.
- `docs/protocol-mobile.md` § Security model: cancelled sealed frames must not migrate across Noise streams.

## Context

Baseline `4d98f075` reproduces retirement stalls on Linux. The fixture numbers
connections after `websocket.Accept`; its first handler can register after a
reconnect handler, so `retired` can identify the replacement socket. Separately,
closing a caller's context does not synchronously close all descendant contexts:
the deadline waiter can release the pump before cancellation propagates to its
write context. A derived-context check alone is not a caller-cancellation fence.
No decision record is needed for these local ordering repairs.

## Design

Keep the original caller context on admitted requests alongside the existing
timeout-bounded context. `sendPump` checks the caller context directly before its
existing write/pump/generation checks, preserving cancellation/deadline identity
without waiting for descendant propagation. Admission timeout and ordered disposal
remain unchanged; failed accepted sends close their captured socket before ack.

The accepted-send fixture explicitly acknowledges original peer registration
before submission. Keep every disposal, error, zero-publication and reconnect
assertion; additionally compare generation identities. Add a test-only context
wrapper implementing `AfterFunc` which holds descendant cancellation until
released. Exercise that controlled schedule in cancel and deadline modes, with
the caller already cancelled while both derived write contexts remain live.
Shutdown retains its independent pump-context check. Use the wrapper in the named
test so the required repetitions include the regression on every iteration.

Sizing: one deliverable; approximately 250 written lines including plan and
evidence, zero exported types, no consumer changes, four acceptance criteria,
one additional reject check. No concurrent feature branch touches the two target
transport files at planning time.

## Concurrency model

Production retains the existing three pumps and joined `serve` lifetime. The
test context's cancellation callback waits on an explicitly released gate and
signals completion; cleanup releases and joins it. Sender and Connect goroutines
retain bounded joins. Peer registration closes a one-shot channel before a send
can trigger retirement or reconnection.

## State transitions and identity reuse

| Event | Race-detector coverage |
| --- | --- |
| Accepted caller cancellation while descendant propagation is held | `TestClientSendContextAcceptedCancellation/cancel` |
| Accepted caller deadline while descendant propagation is held | `TestClientSendContextAcceptedCancellation/deadline` |
| Accepted request during pump shutdown | `TestClientSendContextAcceptedCancellation/shutdown` |
| Disposal before completion and original peer retirement | all three modes above |
| Reconnect with distinct generation and no old-request migration | cancel and deadline modes above |
| Cancellation before admission keeps socket usable | `TestClientSendContextPreAdmissionCancellation` |

## Error handling

Return caller cancellation/deadline errors through the existing sanitizer; do not
expose caller data or raw socket errors. Keep shutdown non-nil and stale-socket
rejection unchanged. Fixture timeouts fail and run bounded cleanup, without retries.

## Testing strategy

First run the controlled descendant-propagation regression against unchanged
production and record its failure. Separately force delayed original handler
registration on a scratch overlay to verify the retirement-identity diagnosis.
Then run the fixed named test with `go test -race -json -count=100 -run
'^TestClientSendContextAcceptedCancellation$' ./internal/transport` and the full
package with `go test -race -json -count=20 ./internal/transport`, recording
revision, Go version, concurrency, named counts and timing. Run `go vet ./...`
and `go build ./cmd/pyry` on Linux. The dispatcher owns the ordinary `make check`
gate; its result remains pending until that stage. Other OS checks are outside
this ticket's scope.

## Open questions

- Confirm both proposed causes with controlled schedules before handoff.

## Documentation handoff

Pending documentation stage: in `docs/knowledge/features/transport-package.md`,
**Lifecycle and goroutines** and **Test surface**, record any durable invariant
established by the repair: the cancellation/disposal/retirement ordering and the
synchronization that proves the test is observing the affected connection.
Describe only a demonstrated cause; retain the distinction between pre-admission
cancellation and accepted-request disposal, and the prohibition on migration to
a reconnect.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `sendPump` retains socket identity checks and only sends admitted bytes; checking the original caller prevents publication during delayed cancellation propagation.
- [Tokens/secrets] No credential changes. Test payloads are fixed markers; no frame bytes enter diagnostics.
- [File operations] No production filesystem operations are added.
- [Subprocesses] No subprocess execution is added.
- [Cryptography] No primitives or nonce handling change. Exact-socket disposal preserves the existing sealed-frame stream boundary.
- [Network/I/O] Existing read caps, dial policy and write timeout remain intact. Loopback fixture registration changes no production network admission.
- [Errors/logs] `safeTransportError` continues rebuilding errors from supported sentinels; cancellation checks do not retain raw errors or payloads.
- [Concurrency] No production goroutines or locks are added. MUST FIX addressed in design: direct caller check must precede writing even when descendant cancellation is held. Fixture callback gates must release and join in cleanup.
- [Threat model] The repair addresses implementation ordering and replay/nonce-stream integrity in the existing transport boundary. Authentication, crypto and relay resource policy remain unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-16

## Revisions

### 2026-10-16 — diagnosis resolved and Linux validation

No design departure. The open question is resolved by two independent controls:

- With unchanged production, the committed test's `testAcceptedPropagation`
  holds descendant cancellation after its original caller is cancelled. Cancel
  and deadline each returned nil and stalled retirement; shutdown passed.
  `go test -race -json -count=1 -run
  '^TestClientSendContextAcceptedCancellation$' ./internal/transport` executed
  four test/subtest records: one passed, three failed, zero skipped, 6.134 s.
  With the direct caller check, the identical control passed all four in 1.124 s.
- A scratch Go overlay of baseline `send_accepted_test.go` numbered upgrades
  before `Accept`, held upgrade 1 immediately after `Accept` until upgrade 2
  registered, and retained the original post-`Accept` retirement selection.
  `go test -race -json -overlay
  /tmp/builder-3496/registration-overlay.json -count=1 -run
  '^TestClientSendContextAcceptedCancellation/cancel$' ./internal/transport`
  logged `retirement observer owns upgrade 2`, then stalled awaiting retirement.
  Both records failed, zero skipped, 3.024 s. The fix's registration handshake
  prevents cancellation/reconnect until the original peer owns that observer.

These establish the two failure mechanisms, rather than inferring reliability
from a passing rerun. The natural deadline gap is possible because native
`cancelCtx.cancel` closes its own Done channel before iterating its children;
waiting for the parent Done does not join descendant cancellation. The test
wrapper holds that same boundary explicitly through the supported AfterFunc
contract. Baseline native runs reproduced both the nil deadline completion and
retirement stalls before adding the controls.

Environment: Linux/amd64, Go 1.26.9, eight online logical CPUs. GOMAXPROCS and
GOFLAGS were unset; `-p` and `-parallel` used Go defaults, with no affinity or
injected workload. Candidate batches ran concurrently with each other and
vet/build. Counts below include named parents and subtests; timings are the
JSON package elapsed values, not separate shell wall-clock measurements.

| Revision | Command | Executed | Passed | Failed | Skipped | Seconds |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| `4d98f075` | `go test -race -json -count=20 -run '^TestClientSendContextAcceptedCancellation$' ./internal/transport` | 80 | 74 | 6 | 0 | 11.258 |
| `85fa7ca8` | `go test -race -json -count=100 -run '^TestClientSendContextAcceptedCancellation$' ./internal/transport` | 400 | 400 | 0 | 0 | 12.734 |
| `85fa7ca8` | `go test -race -json -count=20 ./internal/transport` | 1620 | 1620 | 0 | 0 | 13.809 |

Baseline failed three parent iterations: cancel stalled once, deadline returned
nil and stalled twice. Shutdown passed all 20 repetitions. The candidate named
batch executed and passed cancel, deadline and shutdown 100 times each; the
complete package batch executed and passed each mode 20 times. All original
disposal, error, no-publication, reconnect marker and pre-admission health
assertions remain, with an added distinct-generation assertion.

`go vet ./...` and `go build -o /tmp/builder-3496/pyry ./cmd/pyry` both exited
zero at `85fa7ca8`. Raw JSON and control overlays were retained in
`/tmp/builder-3496/` for this run; the counts and diagnoses above are the durable
record. No test retry, widened wait, sleep or skipped assertion was added.
The ordinary `make check` aggregate is pending the dispatcher-owned gate; it
has not been claimed green. Other operating systems are outside verification
scope. The documentation handoff remains pending the documentation stage.

### 2026-10-16 — verifier finding 1: complete aggregate rerun handoff

The verifier reviewed `8520bd3069922657298261b65d7434c5e5e37f9b` and found
no transport implementation or security defect. Its dispatcher `make check`
exited 2: `internal/thread` timed out at 600.199 s in
`TestCheckpointResumeCorpus`, with eight cuts still running. Vet passed and
the race tier reported 54 successful packages, including transport at 1.625 s,
but staticcheck, substrate-guard, fake-daemon e2e and the subsequent
`make build` never ran. That aggregate is failed/incomplete; its nonverbose
output establishes package outcomes only, not named test counts.

The dispatcher has since merged main into this branch at
`cdd691085e2bceb846949f4bcf68d9a857503581`. This includes the separate #3494
runtime repair merged by PR #3497 at `f314c499`; #3494 is closed.
There is no transport source change since the reviewed head and no further
thread change in this rework. The earlier failure matches the baseline
timeout tracked by #3494, but does not prove scheduling causation or a pass
on the merged candidate.

Builder-owned Linux checks at `cdd69108`:

| Command | Executed | Passed | Failed | Skipped | Seconds |
| --- | ---: | ---: | ---: | ---: | ---: |
| `go test -race -json -count=100 -run '^TestClientSendContextAcceptedCancellation$' ./internal/transport` | 400 | 400 | 0 | 0 | 12.378 |
| `go test -race -json -count=20 ./internal/transport` | 1620 | 1620 | 0 | 0 | 13.431 |

Each mode executed and passed 100 times in the named batch and 20 times in
the package batch. Counts include parents and subtests; timings are JSON
package elapsed values. Environment: Linux/amd64, Go 1.26.9, eight online
logical CPUs, GOMAXPROCS and GOFLAGS unset, default `-p` and `-parallel`.
The batches ran concurrently with `go vet ./...` and
`go build -o /tmp/builder-3496-rework-pyry ./cmd/pyry`, both of which exited
zero. Raw output is retained under `/tmp/builder-3496-rework-*` for this run.

Mandatory handoff to the next dispatcher/verifier stage: run the complete
ordinary `make check`, followed by `make build`, on the merged candidate
before approval. Retain the full race suite, staticcheck, substrate-guard and
fake-daemon e2e coverage and Go's default timeout; focused passes above do
not substitute for this aggregate. The dispatcher runs configured pre-verifier
gates again after this builder handoff; failed gates are not reusable passes.
The full rerun result remains pending, so verifier finding 1 remains open
until that evidence is available. Documentation remains pending its owning
stage; other operating systems remain outside this ticket's scope.

### 2026-10-16 — recovery: reduce repeated proof serialization

The recovery brief explicitly extends this rework to the shared proof's runtime;
the second aggregate at `b275c8e5` also failed at 600.159 s. Both historical gates
remain failed. Read `internal/thread/checkpoint_resume_test.go` (`testResumeProof.update`),
`checkpoint_benchmark_test.go` (`BenchmarkCheckpointResumePhases`), `render.go`
(`Fold.renderShared`), `checkpoint_export.go` (`checkpointRecord`) and
`docs/knowledge/features/thread-package-main-thread-folding-validation.md`
**Validation and testing**: every import must independently render/hash all rows,
and JSON RawMessage alone cannot prove exact Content bytes. QMD found #3494's
plan; its validation optimization is already present but did not clear this host.
No other remote feature branch touches the two proposed test files.

Keep all production code and corpus scheduling unchanged. Measure the uncached
full-module race tier and isolated corpus, retaining phase logs, CPU/allocation
profiles, CPU quota and Go concurrency. Replace only proof bookkeeping: borrow
immutable rendered bytes through `renderShared`, serialize the complete rendered
metadata and checkpoint/private facts once into worker-owned scratch, and append
a length-delimited exact-content component. Preserve nil-versus-empty Content
identity. No rendered row or hash is reused across imports. Reuse selection and
encoding storage within one proof owner; clear or overwrite it before reuse.
Keep both per-cut JSON round trips, 30,000 entries, 201 cuts, 50,101 comparisons,
eight joined workers and the default timeout. Measure the same phases before/after.

Add proof sensitivity cases before implementation: every Item metadata field,
recipe/private publication facts, exact Content and length boundaries must affect
the proof; scratch reuse must match fresh full recomputation. Retain the existing
exact-byte negative control. Run focused race checks, uncached thread and joint
checks, and the required transport batches; the dispatcher still owns fresh
`make check` followed by `make build` and normal review.

Sizing recheck: one runtime-proof deliverable within the existing acceptance
criteria; approximately 250 additional lines (under 800 total branch-written
lines), zero exported types, zero consumer API changes and no new reject branch.
Security review PASS for this revision: test-only synthetic data, no I/O or
credential/protocol changes; private facts and exact-byte distinctions stay in
the hash. Each existing worker exclusively owns scratch; no new goroutine,
shared cache or cancellation lifetime. Lifecycle coverage remains the existing
resume/cancellation race tests, with scratch reuse checked by the new proof test.

Additional documentation handoff, pending documentation stage: in
`docs/knowledge/features/thread-package-main-thread-folding-validation.md`,
**Validation and testing**, describe the demonstrated proof cost reduction and
worker-owned scratch/length-delimited Content encoding. Retain the independent
per-import full inventory, both JSON round trips and corpus/comparison counts;
distinguish measured runtime from unproved historical contention attribution.

### 2026-10-16 — reproduced runtime obstruction and proof controls

On build-fractal-2 (Intel i7-3770, Linux/amd64, Go 1.26.9, eight logical CPUs,
`cpu.max=800000 100000`, GOMAXPROCS/GOFLAGS unset, default `-p`/`-parallel`),
the unchanged `b275c8e5` implementation reproduced the aggregate obstruction:
`go test -race -json -count=1 ./...` exited 1, with thread timing out at
600.193 s. Its eight workers were active in rendering/proof/JSON work; 185 cuts
completed, eight were active and eight had not started. Named records across
the module: 27,888 run, 27,855 passed, zero terminal test-fail records, 24 skipped,
nine unterminated (the corpus parent and eight cuts). Package outcomes were
54 passed, one no-tests skip and one failed. This is a failed aggregate, despite
the absence of individual fail records from the killed tests. Shell timing was
681.233 s real, 3,235.369 s user and 169.864 s system, including cold compilation.
The unchanged-source phase benchmarks, proof controls, transport batches and
vet/build briefly overlapped this diagnostic run; this is not a controlled
measurement of unrelated host contention. Both earlier dispatcher gates remain
failed as recorded above.

After joining that run, the prebuilt unchanged race binary ran alone through
`go tool test2json -t -p github.com/pyrycode/pyrycode/internal/thread
<scratch>/before.test -test.v -test.count=1 -test.timeout=10m0s
-test.run '^TestCheckpointResumeCorpus$' -test.cpuprofile=<scratch>/before-isolated.cpu
-test.memprofile=<scratch>/before-isolated.mem`. All 202 named records passed,
zero failed/skipped: 201 cuts and 50,101 comparisons. Package elapsed was
415.415 s; test/profile duration 413.17 s, with 2,209.83 sampled CPU-seconds.
Fixture/baseline-proof/prefix/workers took 0.901/70.008/19.142/323.112 s.
The failed aggregate's corresponding completed preparation phases were
2.109/81.795/21.414 s. This establishes substantial executed work even in
isolation; it does not assign the historical difference entirely to scheduling.
The isolated CPU profile attributed 446.14 cumulative seconds to proof update,
268.73 to detached rendering, and 490.39 to JSON unmarshal (inclusive, not
additive). Allocation sampling attributed 26,763.17 MB to proof update out of
56,622.69 MB overall, including 14,006.42 MB under detached rendering.

The new `TestCheckpointProofScratch` failed on the old helper: a 65,547-byte
payload allocated 592,402 B/op. After repair it passed at 2,415 B/op. The 245
named proof/focused-resume records all passed, zero failed/skipped (45.867 s).
They include every Item field, recipe/publication/touch/watermark distinctions,
nil/empty Content and exact bytes, and incremental scratch reuse against fresh
full recomputation. The original `TestCheckpointProofContentBytes` is unchanged.
An initial draft borrowed `f.items` as the render selection; the reuse and
focused tests caught that `renderShared` replaces selection metadata. The final
helper owns its selection slice and only borrows immutable Content bytes.

Matched phase command: `go test -race -count=1 -run '^$' -bench
'^BenchmarkCheckpointResumePhases$' -benchtime=1x -cpuprofile <scratch>/<side>.cpu
-memprofile <scratch>/<side>.mem -o <scratch>/<side>.test ./internal/thread`.
Both samples overlapped the same unchanged full-module run, sequentially;
they are individual observations, not latency guarantees. Before uses the
unchanged helper; after uses the exact helper committed as `3aa22ebe`.

| Phase | Before seconds | After seconds | Before B/op | After B/op |
| --- | ---: | ---: | ---: | ---: |
| Feed | 20.707 | 19.986 | 432,979,176 | 432,976,008 |
| Both JSON round trips | 2.230 | 2.048 | 83,354,472 | 66,575,744 |
| Full render | 1.489 | 1.476 | 60,965,544 | 60,648,888 |
| Full proof including render | 2.691 | 2.181 | 124,756,008 | 81,730,088 |

Proof allocations fell from 498,057 to 479,111; sampled cumulative proof
allocation fell from 124.73 MB to 73.77 MB. The proof now avoids a detached
Content clone plus normalized and base64 payload serializations. Stable render
cost and the payload allocation negative control isolate the removed work;
changes in unchanged JSON/feed phases are not attributed to this repair.
Each import still independently renders/hashes its entire inventory; no hash
or rendered row crosses imports. No sleep, retry, widened bound, reduced corpus
or skipped assertion was introduced. Scratch artifacts live under
`/tmp/builder-3496-runtime/`; this revision retains the durable counts/diagnosis.

### 2026-10-16 — candidate aggregate remains failed; finding 1 unresolved

At pushed implementation `3aa22ebe`, `go test -race -json -count=1 -exec
/tmp/builder-3496-runtime/profile-thread.sh ./...` exited 1. The scratch exec
wrapper only copies the thread binary and appends CPU/memory profile output
paths; other packages execute unchanged. Concurrency, test selection and the
default timeout remain unchanged. No other test job overlapped this run.
Thread timed out after ten minutes and reported package elapsed 602.497 s.
All 201 cuts started; 199 finished and cuts 29,999/30,000 remained active.
The expected final 50,101-comparison completion assertion was not reached.
This is a failed/incomplete aggregate, not a pass or proof of reliability.

Named module records: 27,927 run, 27,900 passed, zero terminal test-fail records,
24 skipped and three unterminated (the corpus parent and two cuts). Package
outcomes: 54 passed, one no-tests skip, one failed. Skips remain the existing
platform, opt-in, ownership and unavailable Node/SQLite fixture cases; no skip
was added. Shell timing: 637.201 s real, 3,039.903 s user, 169.294 s system.
Fixture/baseline-proof/prefix phases took 1.716/62.518/21.323 s. Corpus fixture
completion was logged about 244.5 s into the thread package, versus 214.8 s in the
baseline aggregate; a faster proof alone did not clear the full timeout.

The complete candidate thread profile spans 600.02 s and 2,612.29 sampled
CPU-seconds, with 391.26 cumulative CPU-seconds in proof updates. Allocations
sampled 61.37 GB overall and 17.16 GB under proof updates. This profile includes
all thread tests, unlike the isolated baseline profile; do not treat their totals
as a matched before/after workload or infer the cause of the later corpus start.
The matched phase allocation control establishes removed redundant work, but
this aggregate establishes that the runtime obstruction remains unresolved.

Builder checks at `3aa22ebe`, Linux/Go/concurrency as above:

| Command | Run | Pass | Fail | Skip | Package seconds |
| --- | ---: | ---: | ---: | ---: | ---: |
| `go test -race -json -count=100 -run '^TestClientSendContextAcceptedCancellation$' ./internal/transport` | 400 | 400 | 0 | 0 | 13.017 |
| `go test -race -json -count=20 ./internal/transport` | 1620 | 1620 | 0 | 0 | 14.923 |

Each cancel/deadline/shutdown mode passed 100/100 and 20/20, respectively.
`go vet ./...` and `go build -o /tmp/builder-3496-runtime/final-pyry ./cmd/pyry`
each exited zero after the aggregate finished. They do not complete `make check`.
The ordinary dispatcher `make check`, followed by `make build`, has not passed
on this repair; staticcheck, substrate-guard and fake-daemon e2e remain unproved.
Further runtime repair and those fresh gates are required. Verifier finding 1
is **not fixed**. Documentation remains pending its owning stage; other OSes
remain outside this ticket's verification scope.

Additional scratch-state coverage in the final diff:

| Event | Race-detector coverage |
| --- | --- |
| Selection/encoding storage reused after successive feeds | `TestCheckpointProofReuse`, `TestCheckpointProofScratch` |
| Fresh independently rendered inventory after every import | `TestCheckpointResumeFocused`; corpus incomplete in the aggregate above |

### 2026-10-16 — recovery: reuse decoded root fields

The recovery brief authorizes a production decoder repair: `foldDecoder.decode`
already parses the root into `fields`, but `validRecordedFields` parses that same
root again. Reuse that map for root struct validation through a shared object
validator. Keep typed unmarshalling, exact required-key lookup, recursive field
validation, case-insensitive recorded-field matching, nullable containers and
inert unknown fields. Non-struct roots retain the existing recursive path.
Read `internal/thread/fold.go` (decoder and validator), `decode_object_test.go`
(empty-object controls), `checkpoint_benchmark_test.go` (matched phases), and
the thread validation overview (independent full-inventory proof requirements).
No other remote feature branch touches the proposed files.

Before implementation, add differential acceptance/output controls against the
existing root-reparse decoder and a relative allocation regression that must
fail without reuse. Measure both decoder paths together, then matched complete
feed/render/proof phases. Preserve every corpus assertion and scheduling choice,
both JSON round trips, exact Content bytes, 30,000 entries, 201 cuts and 50,101
comparisons. Run uncached Linux isolated thread, joint cmd/pyry+relay+thread and
full-module race checks at default concurrency/timeout, plus transport batches,
vet and build. Dispatcher still owns fresh complete `make check`, `make build`
and normal review; no historical failed gate becomes a pass.

Security review PASS: the untrusted JSON boundary retains all existing rejects;
no I/O, secrets, crypto, errors or logging change. No state or goroutine is added,
and each decode owns its map; successive independent calls are covered by
`TestDecodeRecordedFields`. No exported type or consumer contract changes.
Sizing: approximately 150 additional code/test lines and this revision; no new
reject branches. Documentation handoff additionally records root-map reuse and
its measured costs in the thread overview's **Validation and testing** section.

### 2026-10-16 — decoder repair measured; joint gate still fails

Implementation `42233c88`, build-fractal-2, Linux/amd64, Go 1.26.9, i7-3770, eight logical CPUs, `cpu.max=800000 100000`; GOMAXPROCS/GOFLAGS unset and default `-p`, `-parallel`, timeout. Large candidate race batches ran sequentially; unrelated host activity was uncontrolled.
The pre-fix `go test -race -json -count=1 -run '^TestDecodeRecorded' ./internal/thread` executed 31 records: 30 acceptance passes and the intended allocation failure (42 versus 42 allocations), zero skips, 2.395 s. After repair, `-run '^TestDecode'` passed all 52 records, zero failures/skips, 3.084 s; allocation control fell to 28. Differential controls preserve accepted DTOs and all documented rejection/null/container/case/unknown-field rules.
Matched phase command: `go test -race -count=1 -run '^$' -bench '^BenchmarkCheckpointResumePhases$' -benchtime=1x -cpuprofile <scratch>/<side>.cpu -memprofile <scratch>/<side>.mem -o <scratch>/<side>.test ./internal/thread`; after also selected the two one-iteration decoder controls. Feed fell 19.650→12.537 s and 433.0→297.0 MB/op; JSON stayed 1.895→1.895 s, render 1.399→1.414 s, full proof 2.140→2.090 s. The brief pre-fix decoder regression overlapped baseline setup; no other candidate test job overlapped the phase run. These observations establish removed work, not latency guarantees.
Sampled cumulative decoder CPU fell 16.42→10.66 s and allocation 636.09→367.55 MB. Same-binary `go test -race -count=1 -run '^$' -bench '^BenchmarkDecodeRecordedFields$' -benchtime=100x ./internal/thread` measured reparse/reuse at 12.967/9.363 ms, 215856/141224 B/op and 62/44 allocations. Root fields now serve required checks and root validation; typed decoding and recursive validation remain. No proof, corpus size, cut, comparison, JSON round trip, scheduling or timeout assertion changed.

| Uncached race command | Run/pass/fail/skip/unterminated records | Result and seconds |
| --- | --- | --- |
| `go test -race -json -count=1 ./internal/thread` | 1950/1949/0/1/0 | PASS 528.331; 201 cuts/50101 comparisons; corpus 364.15 s, workers 289.152 s; shell 528.821 s |
| `go test -race -json -count=1 ./cmd/pyry ./internal/relay ./internal/thread` | 20052/20040/0/3/9 | FAIL: thread timeout 600.150; relay 62.836 and cmd/pyry 385.201 passed; shell 628.054 s. 192 cuts completed, eight active, one unstarted; final comparison assertion unreached |
| `go test -race -json -count=1 ./...` | 27963/27939/0/24/0 | PASS: 55 packages passed, one no-tests skip; thread 578.900, cmd/pyry 385.928, relay 73.303; shell 609.571 s. All 201 cuts/50101 comparisons completed; eight workers took 302.230 s |

Required transport commands at the same revision: `go test -race -json -count=100 -run '^TestClientSendContextAcceptedCancellation$' ./internal/transport` passed 400/400 records in 12.201 s; `go test -race -json -count=20 ./internal/transport` passed 1620/1620 in 13.247 s, zero failures/skips/unterminated records. Each mode passed 100 and 20 times, respectively. These two batches and vet/build overlapped each other, before the full-module run. `go vet ./...` and `go build -o /tmp/builder-3496-decoder/pyry ./cmd/pyry` completed successfully.
Scratch JSON, phase logs, profiles, skip reasons and summarizer remain in `/tmp/builder-3496-decoder/`; previous gate evidence was retrieved under `/tmp/builder-3496-decoder-evidence/`. The heap probe skips unless explicitly armed; joint also skips the live-Claude opt-in and case-insensitive-filesystem test. Every historical failed gate remains failed. Finding 1 is not fixed: the measured production repair and isolated pass do not clear the failed joint aggregate. Fresh complete dispatcher `make check`, then `make build` and normal review remain required before approval; no staticcheck, substrate-guard or fake-daemon e2e pass is claimed. Both documentation handoffs remain pending their owning stage; other operating systems remain outside scope.

### 2026-10-16 — current-main candidate retains a joint timeout

Current main `00066e73` (PR #3499) is merged at `3b7c1689`; a fresh fetch and
`git merge origin/main` confirmed it is current before these checks. This
includes the test-cost reductions and sibling-app readiness correction.
The decoder, transport and checkpoint proof changes are preserved unchanged.
There is no new design departure or production change in this diagnostic leg.

Environment: build-fractal-2, Linux/amd64, Go 1.26.9, i7-3770, eight logical CPUs,
`cpu.max=800000 100000`, GOMAXPROCS/GOFLAGS unset, default `-p`, `-parallel`
and ten-minute package timeout. The joint and isolated runs were sequential,
without another builder test job overlapping either; unrelated host activity
was uncontrolled. Counts include named parents and subtests.

| Command at `3b7c1689` | Run/pass/fail/skip/unterminated | Package seconds |
| --- | --- | --- |
| `go test -race -json -count=1 ./cmd/pyry ./internal/relay ./internal/thread` | 19992/19979/0/4/9 | **FAIL**: thread 600.191; cmd/pyry PASS 353.021, relay PASS 66.699 |
| `go test -race -json -count=1 -cpuprofile /tmp/builder-3496-combined/isolated.cpu -memprofile /tmp/builder-3496-combined/isolated.mem -o /tmp/builder-3496-combined/isolated.test ./internal/thread` | 1937/1936/0/1/0 | PASS 586.399 |
| `go test -race -json -count=100 -run '^TestClientSendContextAcceptedCancellation$' ./internal/transport` | 400/400/0/0/0 | PASS 11.829 |
| `go test -race -json -count=20 ./internal/transport` | 1620/1620/0/0/0 | PASS 12.826 |

The joint run completed 144 cuts, with eight active and 49 unstarted when killed;
its final comparison assertion was unreached. Zero terminal test-fail records
does not make this aggregate pass. Shell elapsed was 629.820 s. Existing skips:
`TestRunAgentRun_RealClaude`, `TestMemoryPathsCaseInsensitive`,
`TestReferenceDiskHeap` and `TestPushWakeFeedbackExpiryIntegration`; the real
socket expiry proof remains in the slow release checks, not claimed passed here.

The isolated corpus completed all 201 cuts and 50,101 comparisons in 383.22 s.
Fixture/baseline-proof/prefix/workers took 2.912/68.629/18.117/293.565 s;
workers and GOMAXPROCS remained eight. Shell elapsed was 587.342 s. Its sole
skip was the existing opt-in heap probe. The allocation regression again reported
42 versus 28 allocations. This is an isolated pass, not aggregate acceptance.
The full thread profile samples 2456.18 CPU-seconds and 56.92 GB allocated;
it is not a matched workload against earlier corpus-only or pre-merge profiles.

Both transport batches and `go vet ./...` plus
`go build -o /tmp/builder-3496-combined/pyry ./cmd/pyry` then ran together and
exited zero. Each cancellation mode passed 100 and 20 repetitions, respectively;
transport shell times were 12.612 and 13.444 s. JSON, stderr, profiles and environment
records are retained under `/tmp/builder-3496-combined/`. No tests remain running.

**Finding 1 remains not fixed.** Main's merged optimizations and the measured
decoder repair did not clear this fresh joint gate. Every earlier failed gate
remains failed; the prior separate full-module pass is retained without being
substituted for this result. Fresh complete dispatcher `make check`, subsequent
`make build` and normal review remain required before approval. No fresh
full-module, staticcheck, substrate-guard or fake-daemon e2e pass is claimed in
this leg. Both documentation handoffs remain pending their owning stage;
other operating systems remain outside scope.
