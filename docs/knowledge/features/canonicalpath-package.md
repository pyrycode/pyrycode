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

`Resolve` applies `filepath.Abs`, then `filepath.EvalSymlinks`, then the private
`canonicalCase` walk. Directory and file symlinks are followed, including macOS
aliases such as `/var` to `/private/var`. Both directory components and a file
leaf use the same casing rule.

For each component, `matchEntry` selects an exact-case directory entry first.
Without an exact match, only a unique `strings.EqualFold` match changes spelling.
Absent or ambiguous matches preserve the input component. If `os.ReadDir` cannot
read an ancestor, the walk retains that component and continues. These probes
never turn a successful absolute-path and symlink resolution into an error.

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
`agentrun: resolve workdir %q: %w` error context is retained during migration.

Plain `filepath.EvalSymlinks` is insufficient for the on-disk-case contract: on a
case-insensitive filesystem it can preserve the caller's spelling of components
that are not symlinks. That previously produced a trust-map key different from
Claude's cwd, so pre-marking trust had no effect. See
[agentrun key shape](agentrun-package.md#key-shape) and
[trust key shape](agentrun-trust-subpackage.md#key-shape--realpath-on-disk-case-not-abspath).
`tuidriver.EncodeCwd` supplies a dashed JSONL directory name, a different contract.

Resolution is a best-effort metadata walk, not a stable file handle. Filesystem
changes between resolution and use remain possible; consumers must enforce their
confinement policy when using the result.

## Migration

[Trust marking](agentrun-trust-subpackage.md#key-shape--realpath-on-disk-case-not-abspath)
uses `canonicalpath.Resolve` directly. The old `agentrun.ResolveWorkdir`
implementation, API and tests remain alongside this package for the remaining
consumers. Consumer migrations #2912–#2916 are tracked by
[#2911](https://github.com/pyrycode/pyrycode/issues/2911), with removal of the old
implementation in [#2917](https://github.com/pyrycode/pyrycode/issues/2917).
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
