# Shared filesystem canonicalisation (#2911)

## Files read

- `internal/agentrun/workdir.go` → `ResolveWorkdir`, `canonicalCase`, `matchEntry`: source semantics to lift without changing the old implementation.
- `internal/agentrun/workdir_test.go` → the seven `TestResolveWorkdir` scenarios: independent regression coverage to retain.
- `docs/knowledge/features/agentrun-package.md` § Key shape: plain `EvalSymlinks` preserves wrong input case and previously broke trust keys.
- `docs/knowledge/features/agentrun-trust-subpackage.md` § Key shape — realpath, on-disk case, not abspath: filesystem paths and dashed JSONL names are different contracts.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change and Test execution and artifact survival: check branch-specific behavior, prerequisites and skips.
- `CODING-STYLE.md`: stdlib tests, wrapped errors and subprocess isolation conventions.

## Context

Canonical filesystem paths are needed independently of agent-run for sibling
migrations #2912–#2916. This ticket introduces that single reusable contract;
#2917 removes the old implementation later. Trust writes and confinement remain
consumer responsibilities. No new decision record is needed for this semantic lift.

Sizing: approximately 500 written lines including this plan, one production file,
zero new exported types/interfaces, zero changed consumers, three acceptance
criteria, and two fatal resolution branches. This fits all builder limits and
agrees with the ~450-line estimate and #910 analogue. The feature-branch scan
found no overlapping changes to the new package or the old workdir files.

## Design

Add stdlib-only `internal/canonicalpath/path.go` with public contract
`Resolve(path string) (string, error)`. Resolve relative paths against the current
process directory using `filepath.Abs`, follow symlinks using
`filepath.EvalSymlinks`, then lift the existing private `canonicalCase` and
`matchEntry` helpers. Empty input means the current directory. Files and
directories both resolve to absolute paths with on-disk component casing.

Case selection prefers an exact match, otherwise a unique `strings.EqualFold`
match. Absent or ambiguous matches preserve the input spelling. Unreadable
directory probes also preserve that component and continue the walk. Preserve
the existing error context to avoid adding an observable semantic difference.
Leave all old agentrun code, tests, APIs and callers untouched.

## Concurrency model

Resolution is synchronous and read-only, with no goroutines, mutable globals,
logging, trust writes or process-global cwd changes. Filesystem changes between
probes retain the existing best-effort semantics; the result is not a confinement
check or a stable file handle. Cwd-failure tests run in isolated helper processes.

## Error handling

Absolute-path and symlink-resolution failures return an empty string and wrap
the original error with `%w`, preserving `errors.Is`, especially `fs.ErrNotExist`
and `fs.ErrPermission`. Case probes never introduce a new resolution error.

## Testing strategy

Write tests first and observe undefined resolver/helper failures before lifting
the production code. Independently cover the seven existing scenarios: macOS
realpath, already-resolved input, relative input, missing input, wrong-case
correction, correctly cased mixed-case input and case-sensitive sibling safety.
Also cover empty input, nontrivial relative paths, files, directory/file symlinks,
broken symlinks, exact/unique/ambiguous/absent/Unicode match selection, failed
absolute resolution in a deleted cwd, permission identity, and successful
resolution through an execute-only directory whose case probe is unreadable.
Use explicit prerequisite skips for macOS, filesystem case and effective
permissions; pure match-selection assertions run on every filesystem.

Run `go test -race ./internal/canonicalpath/...` plus the unchanged agentrun
package regression tests, `go vet ./...`, and `go build ./cmd/pyry` (binary output
outside the worktree). The dispatcher verifier owns `make check`, including the
full-module race suite; that gate remains pending when this builder opens its PR.

## Open questions

None. The exported name is `canonicalpath.Resolve`; callers migrate separately.

## Documentation handoff

Pending for the documentation stage:

- Add `docs/knowledge/features/canonicalpath-package.md`, sections “Public API” and “Path and error contract”, describing `Resolve(path string) (string, error)` and the absolute/symlink/case/error rules, including empty input meaning the current directory and an empty returned path on resolution failure.
- In that document record that the old agentrun implementation temporarily remains during migration and that this helper does not enforce confinement or write trust state.
- Add the document to `docs/knowledge/CATALOG.md`.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries and threat model] `Resolve` canonicalises caller-supplied filesystem paths without treating them as trusted. Consumers retain confinement and trust policy, as required by #2912–#2916; no policy migrates here.
- [File operations] `canonicalCase` prefers exact names and never chooses an ambiguous sibling. `Resolve` intentionally follows symlinks and performs no writes. Concurrent replacement is an inherited limitation: consumers needing confinement must enforce it when using the result.
- [Tokens, secrets, credentials and cryptography] No secrets are generated, read as file contents, stored or transformed; only directory entry names and symlink metadata are read.
- [Subprocesses] Production launches no subprocesses. The deleted-cwd test launches only its own test binary with fixed test arguments and a test-owned cwd.
- [Network and I/O] No sockets, network reads or external services are involved. Directory enumeration preserves the existing implementation.
- [Errors, logs and telemetry] Resolution wraps filesystem errors with `%w`, preserving identity; paths in local errors retain the old observable context. No logging or telemetry is added.
- [Concurrency] No goroutines, locks or shared mutable state are introduced; helper-process cwd changes cannot affect parallel parent tests.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-07
