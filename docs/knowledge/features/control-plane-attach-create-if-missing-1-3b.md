# Attach: --create-if-missing (1.3b)

This section described `pyry attach --create-if-missing`'s take-or-create attach path. #1348 removed attach and resize from the daemon and CLI; the design reasoning survives in [ADR 014](../decisions/014-get-or-create-take-or-create.md). `Pool.GetOrCreate` itself still exists — see [sessions-package-key-types-pool-getorcreate-1-3b.md](sessions-package-key-types-pool-getorcreate-1-3b.md).
