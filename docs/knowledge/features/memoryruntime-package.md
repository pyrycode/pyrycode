# `internal/memoryruntime` — isolated managed memory installation

`Install(ctx, Options)` returns permanent managed launch locations and verified
versions for OpenAI mode on Linux and macOS. Installation is independent of
credentials, provider initialization, production databases, watchers and
workspace search readiness. See
[deployment](../../deployment.md#managed-memory-runtime) for the supported
targets, complete dependency pins, private paths and smoke commands;
[memorysearch](memorysearch-package.md) owns read-only agent access detection.

## Artifact integrity and extraction

The embedded [Linux lock](../../../internal/memoryruntime/linux-amd64.lock.json)
and [macOS lock](../../../internal/memoryruntime/macos-arm64.lock.json) bind each
target's standalone Python archive, bundled bootstrap pip and complete wheel
closure. Both pin CPython 3.12.11 (standalone release 20250918), pip 24.3.1 and
the same 32 distribution versions, with target-specific artifacts. `download`
hashes network bytes and rehashes cached artifacts before reuse; a checksum
fetched beside an artifact would not authenticate it. The Pyrycode updater's signing keys have
no authority over upstream Python or memsearch artifacts. See
[the lock and publication decision](../decisions/044-memory-runtime-lock-and-generations.md).

Cache identity is the shipped digest, but each hash directory retains the
original wheel basename: pip parses wheel filenames, so renaming a wheel to
its digest breaks otherwise verified installation. `provision` installs the
explicit local-wheel requirements with hashes, no index, no dependency
resolution and no source builds.

Archive confinement applies to dependencies as well as Python. `validateWheel`
rejects unsafe ZIP member paths, links and oversized contents before pip runs.
`extract` writes Python files/directories before creating archive symlinks,
then checks their targets within the rooted destination. Writing through links
while extraction is still in progress would let archive order redirect writes.
Root confinement alone also permits aliases inside the root: `privateDir` and
`regularFile` separately reject unsafe destination types, ownership, write
permissions and hardlinked mutable files rather than modifying their targets.

## Concurrent installation and publication

`acquire` holds an account-scoped kernel flock through verification and
publication. A process-local mutex cannot exclude another daemon process;
cancellable nonblocking waits close only the waiter's descriptor. Process death
releases the kernel lock, and retries ignore unpublished generations.

Generations are installed at their final unique paths. Moving a staged runtime
after pip creates console scripts would leave absolute shebangs pointing to the
old interpreter location. `publish` atomically replaces only `current.json`;
earlier generations remain available to callers holding their launch paths.
There is no automatic pruning of old or abandoned generations. Reuse checks
the shipped lock identity, `treeSeal` and the full production `probe`; a
publication record or an existing executable alone cannot establish readiness.

`TestRepeatAndReplacement` covers reuse and preservation of earlier paths;
`TestConcurrentAndCancelledWaiter`, `TestCrossProcessExclusion` and
`TestInterruptedRetry` cover holder/waiter and process-lifetime boundaries.

## Version and compatibility verification

`selectPins` accepts only OpenAI mode on Linux/amd64 and Darwin/arm64.
`supportedHost` retains Ubuntu 24.04/glibc >=2.39 validation; on macOS,
`supportedMacOS` runs the fixed `/usr/bin/sw_vers -productVersion` command with
bounded output and a credential-free environment. `macOSAtLeast` compares
numeric version components against the lock's minimum, 26.6.2. This minimum
follows the oldest executed smoke host; wheel tags alone do not prove support.
Older/malformed versions, Intel Macs and other unsupported
targets/modes fail recoverably with `ErrUnsupported`. `TestTargetPins`,
`TestMacOSMinimumVersion`, `TestMacOSHostGate` and `TestMacOSInstallCancelled`
cover lock selection, host rejection and cancellation/deadline causes before
destination changes.

`probe` imports memsearch, pymilvus, Milvus Lite and OpenAI, checks every pinned
distribution plus Python/pip, runs the memsearch CLI version and finishes with
`pip --isolated check`. Query each distribution by name with
`importlib.metadata.version`: enumerating visible distributions after Milvus
Lite imports can include setuptools-vendored duplicate metadata and report an
older version despite the correct installed dependency.

The final dependency command must preserve both `ErrProbe` and the command's
cancellation/deadline cause through `Failure.Unwrap`. A fake probe that returns
`context.Canceled` would stay green while the production wrapper discarded it.
`TestCompatibilityProbeCancellation` therefore blocks the actual compatibility
command and exercises both cancellation and deadline expiry through the
production probe. It checks error classification, no returned launch locations,
no publication and a successful retry reusing verified downloads.

## Managed subprocesses and testing

`run` uses a minimal environment with a private home, isolated Python, offline
model flags and no inherited credentials. Linux and macOS process groups
handle active cancellation; Linux additionally stops a managed child if the
installer process dies through parent-death termination. macOS has no such
parent-death contract. Installer lock recovery alone cannot prove child
termination: `TestParentDeathStopsCommand` checks that boundary separately from
`TestInterruptedRetry`.

`limitedOutput` keeps `bytes.Buffer` in a private field. Embedding it promotes
`ReadFrom`, allowing `io.Copy` to bypass a custom capped `Write`; checking the
writer in isolation would miss unbounded child-output collection.
`TestManagedCommandBoundary` exercises the real command path with oversized
output, sanitized environment and cancellation. `run` bounds stdout and stderr
independently and never includes arbitrary subprocess output in failure text.

Hermetic tests use controlled downloads and commands. Canonicalize macOS
temporary homes with `filepath.EvalSymlinks` before passing them to `Install`:
`/var` aliases `/private/var`, so an ordinary temporary path can otherwise trip
`openPrivateHome`'s symlink-destination guard. `TestSmoke` resolves its temporary
home to preserve the strict destination boundary.

The shared exclusion, cancellation/retry, generation-preservation and
publication/security tests run on macOS as well as Linux; only
`TestParentDeathStopsCommand` requires Linux's parent-death contract.

The separate build-tagged `TestSmoke` calls the real installer and opens/closes
only a temporary Milvus Lite database; it does not prove provider, indexing or
agent readiness. Its
[macOS evidence](../../../internal/memoryruntime/smoke-macos-arm64.json) records
arm64/macOS 26.6.2, build 25G83; the
[Linux evidence](../../../internal/memoryruntime/smoke-linux-amd64.json) records
Ubuntu 24.04.5 amd64/glibc 2.39. Both record all exact component versions and
seven executed/passed checks, including repeated verified reuse.
`TestMacOSSmokeEvidence` checks the committed macOS target, complete version
closure and named successful results against the shipped lock. Evidence must
be committed and match the advertised target and exact pins. Extend this entry
point for new platforms or modes rather than treating unexecuted support as
proved. See [verification practices](development-verification.md) for capture
and evidence boundaries.
