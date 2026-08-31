# Pool.Remove JSONL disposition (1.1d-A2 / #95)

Phase 1.1d-A2 adds a `RemoveOptions` parameter so callers pick the on-disk disposition. The signature became `Pool.Remove(ctx, id, opts RemoveOptions) error`; `RemoveOptions{}` (zero value) is byte-identical to the 1.1d-A1 behaviour above (JSONL untouched).

```go
type JSONLPolicy uint8

const (
    JSONLLeave   JSONLPolicy = iota // do not touch the JSONL (default)
    JSONLArchive                    // mv to <pyry-data-dir>/archived-sessions/<uuid>.jsonl
    JSONLPurge                      // delete the JSONL
)

type RemoveOptions struct {
    JSONL JSONLPolicy
}

func (p *Pool) Remove(ctx context.Context, id SessionID, opts RemoveOptions) error
```

**Why an enum, not two booleans.** A `bool Archive`/`bool Purge` shape makes (true, true) a representable-but-illegal state. The enum makes the "exactly one disposition" property type-level, the zero value is well-defined (Leave), and the dispatch switch is exhaustive.

**Why a struct, not a positional `JSONLPolicy` parameter.** Future options (`Force`, `Reason`, …) stay additive at zero call-site churn. Same precedent as stdlib `os.RemoveAll` would have been if it took options.

**Pyry data-dir resolution.** No new config knob — the per-instance data-dir is the **parent of `Pool.registryPath`** (`~/.pyry/<sanitized-name>/sessions.json` ⇒ `~/.pyry/<sanitized-name>/`). `claudeSessionsDir` is claude's directory (the JSONL *source*), not the pyry-owned destination root. When `registryPath == ""` (test/disabled mode), `JSONLArchive` errors with `"sessions: archive requires a registry path"`; `JSONLPurge` and `JSONLLeave` are no-ops.

**Disposition runs under `Pool.mu` after `saveLocked`.** Single critical section, single observable transition: a concurrent `Pool.List` either sees the session present (registry + JSONL both untouched) or absent (registry + JSONL both at their final state). The held-lock window grows by one stat + one rename or unlink — single-syscall granularity, well inside the existing `saveLocked` envelope. POSIX inode semantics keep the operation safe even though claude may still hold the JSONL fd open (rename preserves the fd→inode binding; unlink lets pending writes drain into the soon-to-be-orphaned inode).

**Source-absent semantics.** Both `JSONLArchive` and `JSONLPurge` are success no-ops when the live JSONL is missing — symmetric "ensure the file is at its target state" intent.

**Destination-exists semantics.** Archive errors (wrapping `fs.ErrExist`) when `<archiveDir>/<uuid>.jsonl` already exists. Re-archiving the same UUID is almost always a bug; silent overwrite would lose transcript history. The `errors.Is(err, fs.ErrExist)` shape leaves room for a future CLI `--force`.

**`os.Rename`, not copy + unlink.** Source and destination both live under `$HOME` in normal deployments — same filesystem. EXDEV would surface as a clear error rather than silent corruption. No copy-then-delete fallback today; defer until observed.

**Failure ordering — registry committed before disposition.** On `saveLocked` failure: in-memory delete rolls back, JSONL untouched, child not terminated. On `disposeJSONLLocked` failure: registry already removed (in-memory + on-disk), `Session.Evict` is *still* called (the registry says the session is gone, the child must follow), the disposition error is returned. If disposition and Evict both fail, disposition wins (the new failure mode this signature introduces, and the more actionable one).

**Bootstrap and unknown-id rejection still run before disposition.** `Remove(bootstrapID, {JSONL: JSONLPurge})` does *not* touch the bootstrap's JSONL — structural invariants take precedence over destructive opts.
