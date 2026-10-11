# 044. Managed memory uses a shipped artifact lock and permanent generations

## Context

The [Linux/OpenAI installer plan](../../specs/architecture/3152-managed-memory-runtime.md)
requires credential-free setup isolated from system/user Python and existing
watchers, repeatable trusted downloads, process-safe retries and preservation of
launch locations already returned to callers. Upstream dependency ranges alone
do not fix an executable runtime, and installing into one mutable prefix would
expose callers to partial replacements.

## Decision

Embed an exact artifact lock covering standalone CPython, bundled bootstrap pip
and every dependency wheel, with shipped URLs and SHA-256 digests. Verify all
downloaded artifacts before their code executes; install only explicit verified
local wheels without an index, dependency resolution or source builds. Rehash
cached downloads before reuse.

Serialize setup for each service-account runtime with a cancellable kernel
flock. Install into a unique permanent generation path, verify imports, exact
versions, CLI version and dependency compatibility, and seal the completed
tree. Atomically publish only the generation/lock/seal record after successful
verification. Reuse requires that identity, seal and fresh probes. Retain
previous generations and ignore incomplete unpublished attempts.

## Rationale

Fetching a checksum beside an artifact does not establish independent trust.
Pyrycode's update-signing keys authenticate Pyrycode releases, not upstream
runtimes; the embedded reviewed lock provides the artifact identity instead.
An offline wheel closure also avoids executing unpinned build dependencies or
selecting newer releases during setup.

Moving a temporary installation after pip creates console scripts can break
their absolute interpreter shebangs. Permanent generation paths keep those
locations stable. Replacing only metadata preserves earlier launch paths and
prevents failed installation or probe attempts from overwriting a working
generation. A process-local mutex would not serialize separate processes, and
a cancelled waiter must not terminate the lock holder.

## Consequences

Changing a pin requires a reviewed lock update and executed compatibility/smoke
evidence. Support is limited to proved targets: initially Ubuntu 24.04 amd64,
glibc >=2.39, OpenAI mode. Verified cache entries reduce retry downloads, but
partial downloads restart and abandoned/old generations consume storage until
separately managed. A verified installation establishes no provider, production
database, watcher, index or agent-search readiness.

See [deployment](../../deployment.md#managed-memory-runtime) for the exact lock,
paths and opt-in smoke, and [the package overview](../features/memoryruntime-package.md)
for extraction, subprocess and probe boundaries.
