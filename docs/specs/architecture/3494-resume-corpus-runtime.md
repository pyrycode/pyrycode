# Synthetic checkpoint-resume runtime on Linux

## Files read

- `internal/thread/checkpoint_resume_test.go` → `testCheckpointResume`, `testCheckpointResumeCut`, `testResumeProof.update`: baseline preparation, eight joined workers and complete per-import inventory proof.
- `internal/thread/render_test.go` → `testReferenceFold`: immutable history lookup with a worker-owned recent source.
- `internal/thread/render.go` → `renderSharedLookup`: repeated object validation during exact-content reconstruction.
- `internal/thread/fold.go` → `foldDecoder.decode`, `validRecordedFields`: object validation decodes a field map, DTO and recursive field map even for an empty DTO.
- `internal/thread/checkpoint_export.go` → `checkpointRecord`: creation metadata and recipes included in the proof.
- `internal/thread/corpus_test.go` → `TestFoldCorpus`: unchanged 30,000-entry fixture and independent semantic assertions.
- `docs/knowledge/features/thread-package-main-thread-folding-validation.md` § Validation and testing: complete inventory checks and raw Content bytes must remain independent at each import.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: require a controlled regression and matched measurements.

## Context

The complete synthetic resume proof can exceed the default ten-minute Linux
package race timeout, including when isolated. The timeout stack establishes
active work, not a cause. Measure the unchanged corpus before selecting the
repair. No decision record is needed for a local validation optimization.

Sizing: one deliverable (retain the proof within the default gate), three
acceptance criteria, approximately 300–450 written lines including this plan,
zero new exported types, no signature changes, and at most one new reject guard.
Remote feature branches were checked; none touches the proposed files.

## Design

Profile the unchanged race corpus and measure fixture, uninterrupted baseline,
prefix preparation, cut export/JSON/import, rendering and proof separately with
focused benchmarks. Keep package scheduling, test parallelism and eight cut
workers unchanged for matched runs. Record Go version, CPU/quota and commands.

The candidate local optimization is the empty DTO object-validation case in
`foldDecoder.decode`: with destination `*struct{}` and no required fields, the
contract is exactly one syntactically valid JSON object. Check that contract
without allocating and decoding field maps three times. Keep typed DTO and
required-field validation on their existing paths. Select this repair only if
the profile/benchmarks establish its contribution; otherwise revise the plan
before implementing a different repair.

Keep the resume proof algorithm, fixture, independent full-inventory hash after
each import, both JSON round trips, all suffix comparisons, exact raw content
hashing and private checkpoint facts intact. Add phase benchmarks and concise
phase/completion diagnostics to make the retained work observable. Do not cache
rendered proof rows across imports or change worker admission to obtain a pass.

## Concurrency model

No new production goroutines or shared mutable state. Existing test workers each
own their folds and history source; immutable prefix state is shared read-only.
The existing jobs close, abort signal and WaitGroup join remain authoritative.

## State transitions and identity reuse

None: the proposed validation fast path has no lifecycle or identity state.
Existing `TestCheckpointResumeCorpus`, `TestCheckpointResumeFocused` and the
checkpoint lifecycle tests continue to cover restored identities under race
detection with the unchanged proof.

## Error handling

Malformed JSON, null, arrays/scalars, trailing data and non-JSON whitespace must
remain rejected. Unknown object fields remain inert, including unknown nulls.
Required fields and recursively typed null rejection remain on the old path.
Rendering still withholds the entire result on source validation failure.

## Testing strategy

Write differential object-validation cases and a zero-allocation regression
first; observe the allocation regression fail against the old implementation.
Compare against decoding a map and empty DTO for valid/invalid JSON, duplicate
keys, nested values, whitespace, malformed/trailing input and required fields.
Retain `TestCheckpointProofContentBytes` unchanged. Use phase benchmarks before
and after, plus matched isolated CPU profiles and corpus completion counts.

Run uncached default-timeout race checks on `internal/thread`, then jointly on
`internal/thread`, `cmd/pyry` and `internal/relay`; run `go vet ./...` and
`go build -o /tmp/builder-3494/pyry ./cmd/pyry`. The verifier owns the uncached
`make check` full-module gate. Linux is the verification OS; macOS is outside
this ticket's verification scope.

## Open questions

- Does empty DTO validation account for enough CPU/allocation cost? Resolve from
  the unchanged profile and focused phase benchmarks before implementation.
- How much of observed elapsed time comes from host contention? Report CPU
  samples and phase timings separately; do not attribute all wall time to it.

## Documentation handoff

Pending for documentation stage: in
`docs/knowledge/features/thread-package-main-thread-folding-validation.md`,
under **Validation and testing**, update the paragraph describing
`TestCheckpointResumeCorpus` with the repaired execution strategy and measured
Linux runtime findings. Retain the 201-cut/50,101-comparison contract and
distinguish measured cost from scheduling hypotheses; update the
eight-worker/parallel description if it changes.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `foldDecoder.decode` still validates untrusted history JSON.
  MUST preserve object-only acceptance; differential rejection cases and existing
  typed-null tests are required before handoff. The fast path only accepts an
  empty DTO with no required fields.
- [Tokens/secrets] No credential handling changes; synthetic fixtures contain no
  live secrets and profiling records symbol/time data only.
- [File operations] No production file I/O changes; benchmark/profile output is
  scratch data, not checkpoint persistence.
- [Subprocesses] No new subprocess execution in production or test helpers.
- [Cryptography] Existing SHA-256 proof aggregation and raw Content-byte hashing
  stay unchanged; no cryptographic trust or authentication is inferred.
- [Network and I/O] No network admission, protocol, size limit or deadline changes.
- [Errors/logging] Validation still returns false; diagnostics contain phase
  durations/counts rather than history payloads.
- [Concurrency] No shared validation cache or mutable global state; existing
  worker ownership and joins remain unchanged.
- [Threat model] This is an offline validation cost repair. Typed DTO validation,
  required-field checks and fail-closed rendering remain existing boundaries.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-16

## Revisions

### 2026-10-16 — measured repair selection and scanner allocation bound

The unchanged `4d98f07513f04fa36fa55714b31ecf09e1cb34c2` corpus completed
201 cuts/50,101 comparisons in 250.728s on this host (249.13s profiled test
duration, 1,538.41 sampled CPU seconds). `foldDecoder.decode` accounted for
139.57 cumulative CPU seconds, including 107.56 under rendering; full rendering
accounted for 246.83 and proof update for 327.72 (inclusive, not additive).
This establishes repeated validation cost; it does not establish the cause of
the earlier host's timeout. Keep orchestration unchanged and implement the
planned empty DTO optimization.

The initial allocation regression failed with 32 allocations. `json.Valid`
uses a pooled scanner whose values race builds may discard; permit up to four
scanner/depth allocations for the fixed nested fixture instead of requiring
zero total allocations. Field-map/DTO decoding still exceeds this bound. A
nil `*struct{}` must remain rejected, as verified by a negative control against
the initial fast-path draft. No typed DTO or required-field contract changes.

The helper now logs fixture, baseline/proof, prefix and joined-worker phases,
and checks the expected comparison count as well as completed cuts. Its only
signature change is accepting `testing.TB` for use by phase benchmarks; proof
hashing and all per-cut work are unchanged.

### 2026-10-16 — matched Linux measurements

Before: `4d98f07513f04fa36fa55714b31ecf09e1cb34c2`. Production candidate:
`d0ca0353be7f835a238bd317301913475fcc0b61`. Both used Go 1.26.9 linux/amd64
on an Intel i7-8700 (6 cores/12 logical CPUs), cgroup `cpu.max=800000 100000`
(eight CPU quota), default GOMAXPROCS=8, default test parallelism=8, eight
joined corpus workers, and no GOMAXPROCS/GOFLAGS override.

The matched corpus command was `go test -race -count=1 -run
'^TestCheckpointResumeCorpus$' -v -cpuprofile <scratch>/run.cpu -o
<scratch>/run.test ./internal/thread`, with the default timeout. Both retained
201 cuts and 50,101 comparisons. Before/after package elapsed was
250.728s/220.544s; test/profile duration was 249.13s/219.02s; sampled CPU time
was 1,538.41s/1,350.93s. Cumulative CPU seconds (inclusive) were:

| Operation | Before | After |
| --- | ---: | ---: |
| `foldDecoder.decode` | 139.57 | 50.15 |
| `Fold.render` | 246.83 | 159.26 |
| `testResumeProof.update` | 327.72 | 239.27 |
| `json.Marshal` | 150.81 | 150.63 |
| State JSON round trip | 175.40 | 171.82 |
| Item JSON round trip | 93.75 | 92.80 |

Phase benchmarks used `go test -race -count=1 -run '^$' -bench
'^BenchmarkCheckpointResumePhases$' -benchtime=1x ./internal/thread` (before
production modification, then after). These are individual observations, not
statistical latency guarantees:

| 30,000-entry phase | Before seconds | After seconds |
| --- | ---: | ---: |
| Fixture | 1.241 | 0.594 |
| Feed | 12.806 | 11.917 |
| Export | 0.138 | 0.130 |
| Both JSON round trips | 1.479 | 1.379 |
| Import | 0.075 | 0.061 |
| Full internal render | 1.709 | 0.993 |
| Full proof (includes render) | 2.291 | 1.644 |

Render allocations fell from 899,714 to 415,397 and proof allocations from
981,995 to 498,697. The before fixture/initial preparation overlapped the tail
of the corpus run: the unchanged fixture's timing difference illustrates why
wall times alone cannot establish the repair. The profile's rendering reduction
and unchanged JSON costs identify the repaired work separately from scheduling.
Package scheduling/worker settings were not changed; other host activity was
not controlled, and the cause of the earlier machine's timeout is not inferred.

The in-process control `BenchmarkDecodeEmptyObject` uses a named empty DTO to
exercise the unchanged generic decoder with the identical input. Command:
`go test -race -count=1 -run '^TestDecodeEmptyObject' -bench
'^BenchmarkDecodeEmptyObject$' -benchtime=1s ./internal/thread`. It passed:
generic 26,406 ns/op, 1,712 B/op, 32 allocs/op; object 3,060 ns/op, 32 B/op,
1 alloc/op. This controlled comparison resolves validation's contribution
without relying on between-run host scheduling.

Candidate phase diagnostics: fixture 0.545s, uninterrupted baseline feed/proof
37.760s, prefix feed/export 12.208s, joined workers 168.499s. The remaining
default-timeout full-package checks and verifier-owned full-module gate are
recorded in the PR testing handoff.
