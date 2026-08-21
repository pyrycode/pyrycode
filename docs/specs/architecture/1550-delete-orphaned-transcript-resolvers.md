# 1550 — Delete the orphaned Family-A transcript resolvers and the probe-availability handshake

Deletion-only. Zero lines added. Four files.

## Files to read first

Read the code, not the docs — see § Do not touch for why the knowledge base actively
contradicts this ticket.

- `internal/sessions/reconcile.go` → `newTranscriptResolver`, `availabilityReporter`,
  `probeUsable`, `newProbePreferredTranscriptResolver` — the whole deleted block, plus its
  import list. Also read `encodeWorkdir`, `workdirNonAlnum`, `DefaultClaudeSessionsDir`:
  those three **survive** and sit above the cut.
- `internal/sessions/reconcile_test.go` → `touchJSONL`, `stubProbe`, `unavailableProbe`,
  `constPID`, `resolvedTempDir`, `mustNotProbe` — the six test doubles that die with their
  only consumers. `TestEncodeWorkdir` (above the cut) and
  `TestDefaultClaudeSessionsDir_ResolvesSymlinks` (below it) survive unmodified; the cut is
  the contiguous span between them.
- `internal/sessions/rotation/probe_darwin.go` → `noopProbe`'s `Available` method and its doc
  comment. Extract: `noopProbe` itself, its `OpenJSONL`, and `DefaultProbe`'s no-`lsof` return
  all stay — only the second method goes.
- `internal/sessions/rotation/probe_darwin_test.go` → `TestNoopProbe_AvailableFalse`. Extract:
  it is the first thing in the file after the imports; `fakeLsofCmd`, `TestHelperProcess`,
  `TestOpenJSONL_TimeoutFiresWithinBound`, `TestOpenJSONL_Exit1IsBenign`,
  `TestOpenJSONL_SuccessPassthrough` all survive and keep every import alive.
- `internal/sessions/rotation/probe.go` → `Probe` — confirm the interface is single-method
  (`OpenJSONL`) and that `Available` was never part of it. This is why removing the method is
  not an interface change.
- `internal/sessions/rotation/probe_linux.go` → `DefaultProbe`, `linuxProbe` — confirm the
  Linux build declares neither `noopProbe` nor any `Available`, so nothing changes there.
- `internal/transcript` → `CanonicalDir`, `Probed`, `StatByID`, `ValidStem`, `Newest` — the
  shared core the deleted adapter composed. Untouched. Read only far enough to confirm the
  deleted code is an adapter over it, not the resolution logic itself.

## Context

`internal/sessions/reconcile.go` carries a 146-line block whose only reason to exist was to
fill `supervisor.Config.ResolveTranscript`. Post-#1348 that field has no Go declaration
anywhere in the repo — re-verified at merge-base `59679f6`, where the only surviving
`ResolveTranscript` occurrences outside the deleted block are a prose comment in
`cmd/pyry/streamsup_runner.go` (out of scope, see § Do not touch).

The block is unreachable from production. Verified at `59679f6`: all 31 repo-wide references
to the four names live in exactly the two files this ticket edits — `reconcile.go` (the
declarations and `newProbePreferredTranscriptResolver`'s own fallback to
`newTranscriptResolver`) and `reconcile_test.go`. `staticcheck`'s `unused` check stays green
over it because test usage counts as usage, which is precisely why the gate cannot find this
and a human ticket has to.

This is the cleanup #1148 deferred and #1149 restated. Neither expected #1348: the consumer
was deleted outright rather than migrated, so the "removed only when their consumers migrate"
condition can never fire on its own.

Deleting `probeUsable` strands one more symbol, which is why it is in scope here rather than a
follow-up. `probeUsable` is the sole consumer of the optional `Available() bool` handshake;
once it goes, `noopProbe.Available` has no consumer at all, and its test's own doc comment
states its purpose as pinning "the optional-interface signal the bootstrap resolver reads" —
the resolver being deleted. Leaving the method would be a half-deletion `staticcheck` cannot
flag, because a method reachable from a test is "used".

**No ADR.** This removes a deferred adapter under a decision (`internal/transcript` owns
transcript-tail resolution) that #1148/#1149 already recorded. There is no new choice here.

## Design

There is no new interface, type, or data flow. The design content is the deletion boundary:
what goes, what stays, and why the surviving surface is coherent without it.

### What goes

| Symbol | File | Why it can go |
|---|---|---|
| `newTranscriptResolver` | `reconcile.go` | Wrapper over `transcript.Newest` shaped for the deleted `ResolveTranscript` field. Only callers: its own tests and `newProbePreferredTranscriptResolver`'s unusable-probe branch. |
| `availabilityReporter` | `reconcile.go` | Optional-interface declaration consumed only by `probeUsable`. |
| `probeUsable` | `reconcile.go` | Called only from `newProbePreferredTranscriptResolver`'s constructor guard. |
| `newProbePreferredTranscriptResolver` | `reconcile.go` | The Family-A adapter itself. Only callers: its own tests. |
| `touchJSONL`, `stubProbe`, `unavailableProbe`, `constPID`, `resolvedTempDir`, `mustNotProbe` | `reconcile_test.go` | Package-level test doubles. **Verified at `59679f6`: every use of all six is inside `reconcile_test.go` — no sibling `*_test.go` in `package sessions` references any of them.** |
| `noopProbe.Available` + doc comment | `probe_darwin.go` | Sole consumer was `probeUsable`. |
| `TestNoopProbe_AvailableFalse` | `probe_darwin_test.go` | Pins only the method above. |

### What stays, and why the remainder is coherent

- **`rotation.Probe` is unchanged and stays single-method.** `Available` was never on the
  interface — it was an optional method discovered by type assertion in `probeUsable`.
  Removing the method from `noopProbe` therefore changes no interface satisfaction:
  `noopProbe` still implements `Probe` via `OpenJSONL`.
- **`noopProbe` stays live.** `DefaultProbe` still returns it when `exec.LookPath("lsof")`
  fails, so `staticcheck`'s `unused` has a production reference and will not fire. Its
  `OpenJSONL` still returns `("", nil)`, which is the only thing the watcher ever asked of it.
- **The watcher never consulted `Available`.** Nothing in `internal/sessions/rotation`
  outside the deleted method and its test mentions it, so watcher behaviour is bit-identical.
- **`DefaultClaudeSessionsDir`, `encodeWorkdir`, `workdirNonAlnum` stay.**
  `DefaultClaudeSessionsDir` is live from `cmd/pyry`; the other two back it.
- **`internal/transcript` is untouched.** The confidentiality guard closing the untrusted
  probe→path crossing lives there, not in the deleted adapter.

### What this strands (record it; do not act on it here)

The five shared-core symbols all stay declared and tested, but three of them lose their last
caller in this pass. Verified at `59679f6` by sweeping `transcript.<Sym>(` repo-wide:

| Symbol | Caller after this ticket |
|---|---|
| `transcript.StatByID` | **live** — `snapshotUsageFor` in `cmd/pyry/snapshot_usage.go` |
| `transcript.ValidStem` | **live** — the stem check in `internal/sessions/rotation/watcher.go` |
| `transcript.Probed` | **none** outside `internal/transcript`'s own tests |
| `transcript.CanonicalDir` | **none** outside `internal/transcript`'s own tests |
| `transcript.Newest` | **none** outside `internal/transcript`'s own tests |

This is stated because `Probed` is the entry point that applies the PID-reuse confidentiality
guard (`GuardProbedPath`) over the untrusted probe→path crossing. A later "delete unreferenced
code" sweep that reads those three as incidental dead code would be retiring a security
control. Retiring them may well be right, but it is a decision someone has to make on purpose,
with the guard named — not a by-product of this deletion. See § Security review.

**Nothing about this changes the work here.** Do not delete, deprecate, or annotate anything
in `internal/transcript`; it is not one of the four files.

### Import consequence (the one structural change)

`reconcile.go` drops `context`, `internal/sessions/rotation`, and `internal/transcript`;
`reconcile_test.go` drops `context`, `errors`, `io/fs`, `time`.

Dropping `internal/transcript` removes the **top-level** `internal/sessions` package's only
import of it — verified: the only two files in `package sessions` importing it are the two
being edited. That is expected, not a mistake. `internal/sessions/rotation/watcher.go` is a
*different* package and still imports `internal/transcript`. `internal/sessions/rotation`
remains imported by `pool.go` and `pool_test.go`, so only `reconcile.go`'s import line goes.

`probe_darwin_test.go`'s import block is untouched: all eight imports remain in use by the
surviving `fakeLsofCmd` / `TestHelperProcess` / three `TestOpenJSONL_*` tests.

## Mechanics — deleting without adding a line

AC 3 requires `0` additions per file. That makes any accidental reflow, reorder, or retype a
hard failure, so prefer a **line-range deletion** over rewriting the files. Rewriting a
340-line span by hand and hoping the survivors come back byte-identical is the failure mode
this section exists to prevent.

Every range below is given as a symbol span first and line numbers second. The line numbers
are exact at merge-base `59679f6` (`git merge-base HEAD origin/main`), which is this branch's
base — **the whole recipe was dry-run at `59679f6` during specification and produced exactly
the numstat AC 3 demands, with `gofmt` clean.** They are still numbers: run the pre-flight
before trusting them.

| File | Symbol span | Lines @ `59679f6` |
|---|---|---|
| `reconcile.go` | `"context"` import | `4` |
| `reconcile.go` | blank + `rotation` + `transcript` imports | `8,10` |
| `reconcile.go` | blank line after `DefaultClaudeSessionsDir` through EOF (`newTranscriptResolver` … `newProbePreferredTranscriptResolver`) | `63,208` |
| `reconcile_test.go` | `"context"`, `"errors"`, `"io/fs"` imports | `4,6` |
| `reconcile_test.go` | `"time"` import | `10` |
| `reconcile_test.go` | blank after `TestEncodeWorkdir` through end of `TestProbePreferredResolver_PinnedIDAbsentIsNoBaseline` | `41,378` |
| `probe_darwin.go` | `noopProbe.Available` doc comment, method, trailing blank | `25,30` |
| `probe_darwin_test.go` | `TestNoopProbe_AvailableFalse` doc comment, func, trailing blank | `16,26` |

### Pre-flight (run first; any FAIL means stop and re-derive from the symbol names)

```bash
chk() { got=$(sed -n "${2}p" "$1"); case "$got" in *"$3"*) ;;
  *) echo "PREFLIGHT FAIL $1:$2 = [$got] want *$3*";; esac; }
R=internal/sessions/reconcile.go; T=internal/sessions/reconcile_test.go
P=internal/sessions/rotation/probe_darwin.go; PT=internal/sessions/rotation/probe_darwin_test.go
chk $R 4 '"context"'; chk $R 9 'sessions/rotation"'; chk $R 10 'internal/transcript"'
chk $R 62 '}'; chk $R 64 'newTranscriptResolver returns'
chk $T 4 '"context"'; chk $T 5 '"errors"'; chk $T 6 '"io/fs"'; chk $T 10 '"time"'
chk $T 40 '}'; chk $T 42 'touchJSONL creates'; chk $T 378 '}'
chk $T 380 'TestDefaultClaudeSessionsDir_ResolvesSymlinks'
chk $P 23 'func (noopProbe) OpenJSONL'; chk $P 25 '// Available reports'
chk $P 29 'func (noopProbe) Available() bool'; chk $P 31 '// DefaultProbe returns'
chk $PT 16 '// TestNoopProbe_AvailableFalse'; chk $PT 25 '}'; chk $PT 27 '// fakeLsofCmd returns'
wc -l $R $T $P $PT   # expect 208 / 405 / 85 / 126
```

### Apply

`sed` addresses resolve against **input** line numbers, so all ranges in one invocation use
the original numbering — no descending-order bookkeeping needed.

```bash
sed -i '' -e '63,208d' -e '8,10d' -e '4d'  internal/sessions/reconcile.go
sed -i '' -e '41,378d' -e '10d'   -e '4,6d' internal/sessions/reconcile_test.go
sed -i '' -e '25,30d'                       internal/sessions/rotation/probe_darwin.go
sed -i '' -e '16,26d'                       internal/sessions/rotation/probe_darwin_test.go
```

BSD `sed` (`-i ''`) — this repo builds on darwin and linux, but the developer runs on darwin.
If `sed -i` is unavailable or denied, any equivalent works (`sed … > tmp && mv tmp file`, or
`Edit` with the full block as `old_string`); the numstat gate below is what makes the choice
safe, not the tool.

### Immediately verify (this is the gate that makes any method acceptable)

```bash
git diff --numstat   # must be exactly: 0 150 / 0 342 / 0 6 / 0 11, and only those four files
gofmt -l internal/sessions internal/sessions/rotation   # must print nothing
```

Any nonzero addition count means something was retyped. `git checkout -- <file>` and redo that
file — do not "fix" it forward, because a fix-forward that restores the text still leaves the
line in the added column if whitespace drifted.

## Concurrency model

Nothing concurrent is added or removed. The deleted closures held no mutable state and took no
locks; `canonicalDir` was computed once at construction and read-only thereafter. The `pidFn`
seam that read the live child PID under `Supervisor.State`'s mutex disappears with its only
caller — no goroutine, channel, context, or shutdown sequence is touched.

One contract note worth stating because it will not be obvious from the diff: the deleted
resolvers took a `context.Context` they never used (the field signature required it). Dropping
`reconcile.go`'s `context` import is a consequence of that, not a loss of cancellation
support — nothing in the surviving file has a cancellable operation.

## Error handling

The deleted block carried a deliberate, load-bearing error-convention **inversion**: every
no-baseline condition returned `("", 0, nil)` rather than an error, because a non-nil baseline
error diverted `confirmViaTranscriptGrowth` onto the stochastic Committed-chip fallback. That
convention was Family A's, expressed in the adapter, and nothing inherits it — the consumer it
protected is gone. `internal/transcript`'s neutral core keeps surfacing errors as it always
did, and its other adapters keep mapping them their own way.

Surviving error paths are unchanged:

- `noopProbe.OpenJSONL` still returns `("", nil)`; the watcher still reads that as "no
  rotation detected" and proceeds.
- `DefaultProbe` still logs a `Warn` at construction when `lsof` is missing and still returns
  `noopProbe`. That startup warning is now the *only* signal that the host has no probe — the
  `Available` handshake that used to re-surface it downstream had no downstream left.
- `darwinProbe.OpenJSONL`'s timeout / exit-1 / passthrough branches are untouched and still
  covered by the three surviving `TestOpenJSONL_*` tests.

## Testing strategy

**No test is written.** The deleted tests lose no coverage of the surviving core: measured at
`59679f6` with `-coverpkg=./internal/transcript/...`, `internal/transcript`'s own tests cover
46 statement blocks (98.5%) and the 42 covered by the deleted `internal/sessions` tests are a
strict subset — zero blocks fall outside. Every arm the adapter tests exercised has a
transcript-side equivalent: `TestProbed_ProbeWinsOverNewerSibling`,
`TestProbed_PidNonPositiveProbeNotCalled`, `TestProbed_EmptyProbe`, `TestProbed_ErroredProbe`,
`TestProbed_GuardRejections` (includes vanished-before-stat), `TestStatByID`, `TestNewest`,
`TestGuardProbedPath`.

Run gates cheapest-first, and **run `make check` once, last**. It is the wall-clock risk here
(race-enabled tests across the whole module plus the fake-daemon e2e suite); iterating on it
is what exhausts the budget on a ticket this mechanical.

```bash
go build ./...
go test -race ./internal/sessions/...            # survivors green
go vet -tags e2e_realclaude ./...                # compile check incl. the live-claude package
go test -coverpkg=./internal/transcript/... -cover ./internal/transcript/...   # expect 98.5%
make check
```

**Read each command's own exit status.** Do not pipe a gate into `tail`/`head` — the pipeline
reports the *last* command's status, which reads 0 over a failed `make check`. This happened
once during refinement.

`go vet -tags e2e_realclaude ./...` is required even though nothing under
`internal/e2e/realclaude` references a deleted symbol: `make check` never compiles that
package, and a deletion that takes shared helpers with it can leave a package broken for days
behind a green standard gate (2026-08-16). Here the risk is verified-absent rather than
assumed — the six deleted test doubles are used only inside `reconcile_test.go` — but the vet
run is what proves it rather than asserting it.

### Residue proof (AC 2) — two greps, `*.go`, excluding `.claude/worktrees/` and only that

```bash
grep -rn --include='*.go' -E 'newTranscriptResolver|availabilityReporter|probeUsable|newProbePreferredTranscriptResolver' . | grep -v '\.claude/worktrees/'
grep -rn --include='*.go' 'Available' . | grep -v '\.claude/worktrees/'
```

Expected at `59679f6`: first grep **31 → 0**. Second **12 → exactly 1**, that one being
`TestClaudeBinaryAvailable` in `internal/e2e/realclaude/smoke_test.go` — unrelated to the probe
handshake and untouched. Judge by *identity*, not by "count went down": any count other than 1,
or a survivor in any other file, is a failure. The second grep is deliberately case-sensitive
and build-tag agnostic; `availabilityReporter` and `unavailableProbe` are lowercase-`a` and do
not match it, which is why the first grep is also required.

Both greps were run at `59679f6` and returned **nonzero, discriminating** baselines (31 and
12), so the recipe demonstrably detects presence rather than reporting absence unconditionally.
`.claude/worktrees/` does **not exist** at this base, so its exclusion is currently a no-op and
is hiding nothing — keep the exclusion anyway (AC 2 requires it, and the directory can appear
in a developer worktree), but exclude that path and no other.

### Flake, pre-flagged

One refinement `make check` failed on `TestConnected_FiresOnEveryConnect`
(`internal/transport/wssclient_test.go`). It then passed 8/8 standalone and green on both the
unmodified and deleted trees. `internal/transport` shares no code with anything deleted here.
**Re-run it; do not chase it, and do not treat it as fallout.**

## Do not touch

The knowledge base still documents the deleted symbols as live wiring. **#1551 owns every one
of these corrections and is blocked by this ticket. Editing any of them here breaks the
deletion-only AC and the four-file AC simultaneously.** At `59679f6` the stale set is:
`features/rotation-watcher.md` (its `noopProbe`-unavailable-fallback claim),
`features/sessions-package.md`, `features/jsonl-reconciliation.md`,
`features/transcript-package.md`, `features/contextwindow-package.md`,
`architecture/system-overview.md`, `knowledge/INDEX.md`.

Expect to read a doc that contradicts this ticket. **Trust the code**; the greps above are the
authority on what is reachable.

Also out of scope:

- `cmd/pyry/streamsup_runner.go`'s prose comment naming `ResolveTranscript`. It is not one of
  the four names and the file is not one of the four files. Leave it.
- `docs/knowledge/codebase/*.md` — per-ticket historical records, never retro-edited.
- `internal/e2e/rotation_test.go`'s local `encodeWorkdir` mirror — separate package, by design.
- `internal/transcript/transcript_test.go`'s `resolvedTempDir` — a same-named twin in a
  *different* package, still in use there. Only the `internal/sessions` copy goes.
- No knowledge-base doc is a deliverable of this ticket. The last AC is the gate run.

## Notes for the developer

- **`make cite-guard` is structurally inert on this branch.** It is diff-scoped against
  `merge-base`, and this branch adds zero lines, so it has nothing to check. Correspondingly:
  do not add a comment anywhere, not even to explain a removal. The removal is the explanation.
- The line numbers in this spec are operands of a `sed` command, not navigation citations. Use
  the symbol names as the source of truth and the pre-flight as the binding between them.

## Open questions

None blocking. Two things the developer should simply confirm rather than reason about:

1. Whether the pre-flight passes at the branch's actual base. If the tree moved, re-derive
   each range from the symbol names in § Files to read first; the ranges are contiguous spans
   between named survivors, so they are recoverable without this spec's numbers.
2. Whether `staticcheck` (inside `make check`) stays quiet about `noopProbe` after the method
   goes. It should — `DefaultProbe` returns it — but that is a one-line read of the gate
   output, not an investigation.

## Size check (§1, re-applied to this written spec)

| Boundary | Limit | This spec |
|---|---|---|
| Production source files created/modified | ≤ 3 | **2** (`reconcile.go`, `probe_darwin.go`) |
| Total written work | ≤ 400 lines | **0** — pure deletion, zero additions by AC |
| New exported types/interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — all 31 references live in the two edited files |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches | ≤ 10 | **0** |

Ships as one `size:s` ticket. The edit fan-out check applies and returns zero: this is not a
rename or signature change, so no consumer cascade exists — the deleted symbols' only callers
are deleted in the same pass. Left at `s` rather than overridden to `xs` because the three
mandatory gate runs (`make check`, `go vet -tags e2e_realclaude`, the coverage check) carry
real wall-clock cost even though the edit itself is eight `sed`/`Edit` operations.

## Security review

The governing question for a deletion-only ticket is not "what does the new code expose" but
**"does removing this code remove a control?"** Each category below answers that.

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No control removed.** The untrusted→trusted crossing in play is
  probe→path: `lsof` / `/proc/<pid>/fd` returns an attacker-influenceable path (PID reuse could
  point us at a `.jsonl` we do not own). The guard constraining that path to live directly in
  the session dir under a `<uuid>.jsonl` base is applied by `transcript.Probed` /
  `GuardProbedPath` in `internal/transcript` — **not** in the deleted adapter, which only
  composed it. `internal/transcript` is untouched and `TestProbed_GuardRejections` /
  `TestGuardProbedPath` stay green. The deleted code was already unreachable from production,
  so the boundary's production reachability is unchanged by this ticket.

- **[Trust boundaries] SHOULD FIX — addressed in-spec, not gated.** This pass strands
  `transcript.Probed`, `transcript.CanonicalDir`, and `transcript.Newest` (zero callers outside
  `internal/transcript`'s own tests afterwards; `StatByID` and `ValidStem` stay live). `Probed`
  is the guard's entry point, so a later unreferenced-code sweep could retire a security
  control while believing it is deleting an unused helper. Recorded in § Design ("What this
  strands") so the decision is made deliberately. Not a MUST FIX: nothing is exploitable as
  designed, the guard and its tests remain present, and acting on it here would break the
  four-file and zero-addition ACs.

- **[Subprocess / fail-open analysis] No control removed — the strongest candidate, checked
  and cleared.** The removed `Available` handshake let a caller distinguish "no `lsof`" from
  "`lsof` found nothing". The adversarial question is whether losing that distinction lets any
  surviving consumer *trust* an unverified path. It does not: every remaining consumer of an
  empty probe result fails **closed**, never open. `probeWithRetry` in
  `internal/sessions/rotation/watcher.go` documents returning `("", nil)` when all attempts come
  up empty, and the watcher then skips — no rotation confirmed, no state change. The watcher
  additionally runs its own `EvalSymlinks` and `transcript.ValidStem` checks on any probe path,
  so it never relied on the handshake for safety. `noopProbe.OpenJSONL` still returns
  `("", nil)`; `darwinProbe.OpenJSONL` still builds its command with `exec.CommandContext` and
  an integer PID — no `sh -c`, no user-controlled argument — and is untouched.

- **[File operations] No control removed; one surviving control pinned.** The surviving
  `DefaultClaudeSessionsDir` composes `filepath.EvalSymlinks` with `encodeWorkdir`, and
  `workdirNonAlnum` rewrites **every** non-alphanumeric character to `-`. Path traversal
  through that encoder is structurally impossible: `.` and `/` become `-`, so no `..` segment
  can survive into the joined path. AC 3's zero-addition rule plus AC 4's `TestEncodeWorkdir`
  requirement mean that property cannot be silently altered by this ticket — a single retyped
  character would show as an addition in `git diff --numstat`. The only deleted file-writing
  code is the test helper `touchJSONL` (`0o600`), which makes no production mode decision.

- **[Cryptographic primitives] Not applicable, by inspection.** The deleted block contains no
  randomness, hashing, key material, or comparison against a secret. UUID stems are validated
  by `transcript.ValidStem`, which survives and stays live via `watcher.go`.

- **[Network & I/O] Not applicable — no network surface is touched.** The one
  resource-exhaustion control in the neighbourhood, `lsofProbeTimeout` (bounding the single
  `lsof` invocation so a hung probe cannot wedge the watcher), **survives**, and AC 4 requires
  `TestOpenJSONL_TimeoutFiresWithinBound` to pass with zero content change.

- **[Errors, logs, telemetry] No logging is lost.** The deleted block emits no `slog` call at
  all — it is pure resolution logic — so no security-relevant event stops being recorded.
  `DefaultProbe`'s startup `Warn` on missing `lsof` survives and, with the handshake gone, is
  now the sole operator-visible signal that a host has no probe. The deletion introduces no new
  error swallowing; it removes some (the adapter's `("", 0, nil)` collapse) along with its only
  caller. No path, token, or payload appears in any surviving message that did not before.

- **[Concurrency] No lock, goroutine, or shared-state change.** The deleted closures held no
  mutable state and took no locks. The `pidFn` seam — the one mutex-guarded read, of
  `Supervisor.State`'s live child PID — disappears with its only caller, so no lock ordering is
  altered and no goroutine lifecycle changes. There is nothing to leak and no shutdown-mid-write
  window, because nothing deleted writes.

- **[Threat model alignment] Not applicable, stated rather than skipped.** There is no
  `docs/threat-model.md` in this repo. `docs/protocol-mobile.md` § Security model is
  relay-scoped, and this ticket touches no relay, transport, or crypto surface. The one
  CLI-relevant threat that *is* in scope — PID reuse redirecting a transcript read — is
  addressed by the surviving `internal/transcript` guard, per the first finding.

- **[Verification integrity] No finding — the residue greps discriminate.** A residue AC that
  can only ever report "clean" would be worthless. Both greps were run at `59679f6` and
  returned nonzero baselines (31 and 12), and AC 2 judges the second by *identity*
  (`TestClaudeBinaryAvailable`, one hit) rather than by the count falling. `.claude/worktrees/`
  is absent at this base, so the mandated exclusion currently hides nothing.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-20
