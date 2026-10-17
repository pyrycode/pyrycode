# `internal/canonicalpath` — shared filesystem path canonicalisation

Stdlib-only filesystem path resolution for trust, stream supervision and file
confinement consumers. It provides the path semantics described below; consumers
own trust policy and confinement checks.

## Public API

```go
func Resolve(path string) (string, error)
```

`canonicalpath.Resolve` accepts an existing file or directory and returns its
absolute, symlink-resolved path with component casing corrected on a best-effort
basis. Relative input resolves against the process's current directory. Empty
input means the current directory, just like `"."`.

The resolver reads filesystem metadata and directory entries synchronously. It
does not write trust state, enforce confinement, log, change the process cwd or
start goroutines. The supported platforms are Linux and macOS.

## Path and error contract

`Resolve` applies `filepath.Abs`, then `filepath.EvalSymlinks`, then best-effort
case correction. Directory and file symlinks are followed, including macOS
aliases such as `/var` to `/private/var`.

On macOS, `nativePath` opens the resolved path with
`O_EVTONLY | O_NONBLOCK | O_CLOEXEC` and asks `F_GETPATH` for its current on-disk
spelling. A successful probe avoids enumerating every ancestor, for both files
and directories. The descriptor supplies metadata without reading file contents.
`O_EVTONLY` alone can still wait on a FIFO without a peer; `O_NONBLOCK` is also
required so special leaves cannot stall resolution. FIFO and socket leaves use
the portable fallback if the native probe is unavailable.

`descriptorPath` checks close on every successfully opened descriptor, including
when the query fails or returns an invalid path. Open, query or close failure,
or an empty or relative result, declines the native probe. It adds no resolution
error or log. Descriptors belong to individual calls, with no shared cache,
locks or goroutines.

Linux always uses the portable component walk; macOS uses it when the native
probe declines. For each directory component and file leaf, `matchEntry` selects
an exact-case directory entry first. Without an exact match, only a unique
`strings.EqualFold` match changes spelling. Absent or ambiguous matches preserve
the input component. If `os.ReadDir` cannot read an ancestor, the walk retains
that component and continues. These probes never turn a successful absolute-path
and symlink resolution into an error.

Case correction happens after symlink resolution succeeds. A wrong-case path on
a case-sensitive filesystem can therefore fail with `fs.ErrNotExist` before the
case walk; the resolver does not search for a differently spelled substitute.
Exact-match precedence also prevents selecting a case-differing sibling when
both names exist.

Failed absolute-path or symlink resolution returns an empty path and an error
wrapped with `%w`. Use `errors.Is` to check the underlying identity, including
`fs.ErrNotExist` for a missing path or broken symlink and `fs.ErrPermission` for
denied traversal. A missing or inaccessible process cwd can make relative or
empty input fail during `filepath.Abs`. The existing
`agentrun: resolve workdir %q: %w` error context remains for compatibility with
the former resolver, even after its removal.

Plain `filepath.EvalSymlinks` is insufficient for the on-disk-case contract: on a
case-insensitive filesystem it can preserve the caller's spelling of components
that are not symlinks. That previously produced a trust-map key different from
Claude's cwd, so pre-marking trust had no effect. See
[agentrun key shape](agentrun-package.md#key-shape) and
[trust key shape](agentrun-trust-subpackage.md#key-shape--realpath-on-disk-case-not-abspath).
`tuidriver.EncodeCwd` supplies a dashed JSONL directory name, a different contract.

Each call observes the filesystem afresh, without a path-string cache. Subsequent
resolutions reflect a case-only rename, an ancestor replaced under the same
pathname or a symlink retargeted to a different object. A missing target still
returns the existing wrapped missing-path error. An old spelling remains usable
after a case-only rename only where the filesystem accepts it.

Resolution is a best-effort metadata operation, not a stable file handle. The
native descriptor is closed before returning a path. Filesystem changes during
resolution or between resolution and use remain possible; consumers must enforce
their confinement policy when using the result.

## Migration

[Trust marking](agentrun-trust-subpackage.md#key-shape--realpath-on-disk-case-not-abspath)
uses `canonicalpath.Resolve` directly, as do streamsup, CLI confinement/read-root
and transcript helpers, and fake-daemon test helpers.
The four real-Claude consumers now use the shared resolver for the turnless and
absent-resume probes' child cwd, the retained/dropped session-recovery workspace,
and the external-includes control arm's filesystem-form `projects` key
([#2916](https://github.com/pyrycode/pyrycode/issues/2916)).
Consumer migrations #2912–#2916 completed the extraction introduced in
[#2911](https://github.com/pyrycode/pyrycode/issues/2911).
[#2917](https://github.com/pyrycode/pyrycode/issues/2917) removed the old
`agentrun.ResolveWorkdir` implementation, API and redundant tests. The parent
agentrun package retains only its process helpers, `ExitErrIsBenign` and
`ReapDescendantGroups`; see [agentrun's public API](agentrun-package.md#public-api).
The shared resolver preserves the old semantics without moving trust writes or
confinement policy into this package.

## Testing

Independent tests cover the seven original workdir scenarios plus empty input,
files, directory/file symlinks, broken symlinks, match selection, error identity
and best-effort probes. Platform, filesystem-case and effective-permission
prerequisites have explicit skip reasons. `TestResolve_DeletedCwd` isolates its cwd
removal in a helper process so it cannot disturb parallel tests.

Real directory fixtures alone cannot exercise ambiguous case selection on a
case-insensitive volume, where case-differing siblings cannot coexist.
`TestMatchEntry` uses `fstest.MapFS` directory entries so exact, unique, ambiguous,
absent and Unicode fold assertions run on every filesystem. Keep these portable
checks alongside the filesystem-dependent resolver tests: a skipped integration
case does not establish that the selection rule works.

The macOS runtime regressions cover freshness, special leaves and descriptor
ownership. `TestResolveFreshIdentity` checks case-only rename, ancestor
replacement, symlink retargeting and a subsequently missing target.
`TestResolveSpecialLeaves` resolves a FIFO without a peer and a Unix socket in
bounded helper processes, so a blocking open fails instead of hanging the suite.
`TestNativePathDescriptors` compares descriptor counts around 256 successful and
256 missing-path probes, then injects open/query/close failures and empty/relative
results, checking flags and exactly one close after a successful open. Its
`/dev/fd` inventory uses `Readdirnames`, with checked cleanup: Darwin `os.ReadDir`
can fail while statting descriptor entries, obscuring the leak check itself.

`TestResolveAncestorFanout` resolves a private `Workspace/Child` 16 times before
and after adding 35,000 sibling files. Its per-call directory-reader parameter
counts scans and entries for that private ancestor without a mutable global
hook; each state permits at most one full enumeration and checks the exact
current path through both the internal and public resolver. The native path
recorded zero scans in both states. The unchanged-source diagnostic and the
optimization-disabled control each demonstrated 16 scans, including 560,016
entries in the wide state. Operation counts are the regression oracle; elapsed
time alone could hide amplification behind a faster host or a narrow directory.
The portable fallback retains its ancestor enumeration cost. See the
[runtime spec and retained evidence](../../specs/architecture/3525-canonicalpath-runtime.md#native-macos-evidence)
for the controls and complete command-suite comparison; those passing runs do
not establish the cause of historical package timeouts.

Filesystem prerequisites limit integration evidence: a case-insensitive volume
cannot create case-differing siblings; a case-sensitive volume cannot resolve a
wrong-case input; effective permissions may not deny listing/traversal; and some
systems still resolve a deleted cwd. Each corresponding test skips explicitly
when its prerequisite is unavailable. Such skips prove none of the omitted
behavior and do not replace portable `TestMatchEntry` coverage. The macOS
runtime tests are Darwin-only; their native evidence does not establish a Linux
test result.
