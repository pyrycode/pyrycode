# Capture note authority

## Files read
- `internal/memorycapture/batches.go` → `Store.Report`, `validPath`: reports validate names but grant no filesystem authority.
- `internal/memorycapture/storage.go` → `openDirectory`, `sameFile`, `publish`: descriptor-relative access and retained inode checks; private record modes must not apply to user notes.
- `internal/canonicalpath/path.go` → `Resolve`: canonical root aliases, without access-time confinement.
- `cmd/pyry/memory_roots.go` → `resolveEffectiveMemory`: selected vault and explicit references are separate from transcript search roots.
- `docs/knowledge/features/memorycapture-package.md` § Attempt evidence and Private records and validation: actual changes must be reported separately; retain descriptors through validation.
- `CODING-STYLE.md`: bounded files, content-free errors, cancellation and durable publication conventions.
- `docs/knowledge/features/development-verification.md`: deterministic substitutions and checking unchanged bytes distinguish safety from refusal alone.

## Context
Capture backends need one callable note authority without activating an agent.
The selected vault permits writes; only it and explicitly supplied reference roots
permit reads. #3211 edits existing store files; these additive files do not depend
on or rewrite its transaction repair. No decision record is required.

## Design
Add `Notes` and `Note` in `internal/memorycapture`. `NewNotes(ctx, vault, roots)`
canonicalizes supplied root aliases, deduplicates them and retains directory
descriptors. `Read(ctx, root, path)` accepts only a supplied root spelling or its
canonical spelling and a relative slash Markdown path. `Search(ctx, query)`
returns complete matching `Note{Root, Path, Content}` values, sorted by canonical
root and relative path. Matching is literal and Unicode case-insensitive.
Overlaps deduplicate canonical file paths, with the vault taking authority first.
`Write(ctx, path, content)` accepts only vault-relative paths; existing parent
directories are required. It returns the relative path only after changed bytes
are published; identical content returns an empty path. `Close` releases handles.

All components below the resolved roots are opened with no-follow directory/file
operations. Symlink notes and symlink directories are rejected for direct access
and skipped in search. This stricter policy prevents instruction aliases. Writes
reject multi-link targets and protected case-insensitive AGENTS/CLAUDE Markdown
names and every Markdown descendant of `.claude/rules`. Root/parent identities
and leaf identities are retained and revalidated before publishing. Updates use
exclusive temporary files and descriptor-relative rename; creates use exclusive
publication. Existing ordinary note permissions are preserved, new notes use 0644.

Fixed, non-disableable limits: 32 roots, 64 KiB root-request bytes, 4096 bytes per
path/root/query, 1 MiB per note, 10000 scanned entries, 32 MiB scanned note bytes,
128 results and 4 MiB aggregate returned content. Directory enumeration is batched
and charged before recursion. No API accepts unbounded caller overrides.

## Concurrency model
Operations hold a handle read lock; `Close` takes its write lock. No goroutines
are started. Filesystem descriptors pin accessed objects. Same-vault execution
serialization, crash-window accounting and receipts are deferred to #3269.

## State transitions and identity reuse
| Event | Race-enabled test |
| --- | --- |
| Create, update, repeated identical write | `TestNotesContract` |
| Root aliases and overlapping roots | `TestNotesContract` |
| Root/parent/leaf replacement while access is underway | `TestNotesSubstitution` |
| Cancellation before access and before publication | `TestNotesCancellation` |
| Close/repeated close/access after close | `TestNotesContract` |

## Error handling
Reject invalid shapes, unavailable/unsafe paths and excess work with fixed,
content-free sentinel errors. Return no note/result/path on any failure. Strip
filesystem and hook error text. Cancellation is checked before work, between I/O
chunks, during enumeration and immediately before publication. Limit failure
never returns a partial search. Temporary files are removed on failed publication.

## Testing strategy
Write hermetic contract tests first, observe the missing-contract failure, then
implement. Table cases cover traversal, absolute/reference writes, symlinks,
instruction namespaces, hard links, finite bounds and content-free diagnostics.
Deterministic internal hooks replace root/parent/leaf entries during access and
cancel before publish; inspect outside, reference and instruction bytes afterward.
Run `go test -race ./internal/memorycapture/...`, `go vet ./...`, `go build ./cmd/pyry`.
The dispatcher owns the full-module verifier gate; no live proof is required.

## Open questions
None. Sizing: approximately 750 written lines, two exported types, zero consumer
migrations, four criteria, eight rejection categories; one note-authority deliverable.

## Documentation handoff
Pending documentation stage: add “Capture note authority” to
`docs/knowledge/features/memorycapture-package.md`, stating the exact readable
roots, selected-vault-only writes, protected instruction paths, escape/substitution
and hard-link rejection, bounded literal search/cancellation and actual-change
reporting. Do not claim any agent or daemon is activated.

## Security review
**Verdict:** PASS
- Trust boundaries: `NewNotes` grants explicit roots; per-operation validation grants no additional roots.
- Tokens/secrets: no credentials are generated or read; content never enters diagnostics.
- File operations: no-follow traversal, retained inode checks, protected paths and single-link writes prevent path aliases from becoming write grants. Ordinary note modes are accepted.
- Subprocesses/cryptography/network: none; random temporary names use `crypto/rand`.
- I/O: immutable bounds cover requests, enumeration, note bytes and result content.
- Errors/logs: fixed sentinels strip rejected input and underlying path/error text.
- Concurrency: descriptors and handle lock protect lifetime; no goroutines. OUT OF SCOPE: execution serialization/crash accounting belong to #3269.
- Threat model: untrusted backend paths and mutable local directories; no host credential or transport authority is added.
**Reviewer:** builder (self-review). **Date:** 2026-10-12.
