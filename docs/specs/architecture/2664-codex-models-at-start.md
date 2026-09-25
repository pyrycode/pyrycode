# #2664 — Read Codex's model families at daemon start

## Files read

- `cmd/pyry/codex_runner.go` → `probeCodex`, `checkCodexVersion`, `stopCodexClient`, `codexRunner.readModels`, `newCodexRunnerFactory`, `codexHarness`, `codexStartTimeout` — the probe this ticket shares, the read it mirrors, the harness fields it reads.
- `cmd/pyry/codex_home.go` → `prepareCodexHome` — called first, as the factory does; temp-file + rename, so a concurrent call from a Codex mint is safe.
- `cmd/pyry/main.go` → `runSupervisor`, `selectInteractiveRunner` — where the harness is built and where `ctx` (child of `sigCtx`) and `trustedWorkdir` live.
- `cmd/pyry/model_vocabulary_store.go` → `RetainCodex`, `CodexModels`, `Close` — empty list is a no-op; `RetainCodex` is mutex-protected and schedules the file write on the store's writer.
- `cmd/pyry/session_model_list.go` → `mergedModelOptions`, `codexModelOptions`, `modelListFor` — the `multi_agent` reply reads `CodexModels` on every request, so a store populated after connect is answered on the next request with no push.
- `internal/codexsup/models.go` → `Client.LatestModels` — the read; its errors are codexsup's method-prefixed call errors.
- `internal/e2e/internal/fakecodex/main.go` → `FAKECODEX_SIGNED_OUT`, `FAKECODEX_MODEL_LIST_FAIL`, `FAKECODEX_VERSION`, `FAKECODEX_THREAD_LOG`, `FAKECODEX_TURN_LOG`, `accountRead` — the failure knobs, and the logs that prove no thread and no turn.
- `cmd/pyry/codex_model_list_test.go` → `fakeCodexFamilies`; `cmd/pyry/session_model_list_merged_test.go` → `newModelListTestPool`, `assertTaggedRows` — fixtures the tests reuse.

## Context

Codex families reach the store only from a running Codex session (`codexRunner.readModels`). A fresh daemon, or one that has never spawned Codex, answers a `multi_agent` client with no Codex rows, and since the model implies the agent the client cannot offer Codex at all. This ticket reads the list once per daemon start, off the startup path. No ADR needed.

## Design

`cmd/pyry/codex_runner.go`:

- `startCheckedCodex(ctx, bin, home, dir string) (*codexsup.Client, error)` — extracted from `probeCodex`: `codexsup.Start`, `checkCodexVersion`, `SignedIn`; on any check failure it stops the client before returning. Error texts stay exactly what `probeCodex` returns today (`TestCodexRunnerFactory_Refusals` pins them).
- `probeCodex(bin, home, dir)` — keeps its signature and its own `codexStartTimeout` over `context.Background()`; calls `startCheckedCodex` then `stopCodexClient`.
- `readCodexModelsAtStart(ctx, h codexHarness, dir string) error` — `prepareCodexHome(h.home)`, `startCheckedCodex`, `client.LatestModels(ctx)`, `h.vocab.RetainCodex(models)`, deferred `stopCodexClient`. Opens no thread, starts no turn. An empty list is left to `RetainCodex`'s no-op.
- `startCodexModelRead(parent context.Context, h codexHarness, dir string, log *slog.Logger) (wait func())` — derives `ctx` with `codexStartTimeout` from `parent`, runs `readCodexModelsAtStart` on one goroutine, and on error logs one Info line `codex: model list not read at start` with `err`. Returns `wait`, which cancels the context and joins the goroutine.

`cmd/pyry/main.go` → `runSupervisor`: name the harness literal (`codex := codexHarness{...}`), pass it to `selectInteractiveRunner` as today, and after that call returns without error set `codex.vocab = modelVocabulary` and `defer startCodexModelRead(ctx, codex, trustedWorkdir, logger)()`.

Deviation from the ticket's note: the parent context is `ctx` (the cause layer derived from `sigCtx`) rather than `sigCtx` itself. It is still bounded by `sigCtx`, and it is also cancelled by `pyry stop`, which cancels `ctx` only.

Overlaps: none; no in-flight branch touches either file.

## Concurrency model

One goroutine per daemon start. It exits when the read finishes, fails, or its context ends (timeout, signal, `pyry stop`, or `wait`). `codexsup.Start` runs the process under its own context and `stopCodexClient` is bounded by `codexCallTimeout`, so the goroutine's life is bounded by `codexStartTimeout + codexCallTimeout`.

`wait` is deferred after `defer modelVocabulary.Close()`, so it runs first: the goroutine is joined before the store's writer is, and no `RetainCodex` lands on a closed store. Because `wait` cancels before joining, an early error return from `runSupervisor` (e.g. pool init) does not sit out the 30s timeout.

A Codex mint racing the read may call `prepareCodexHome` and `RetainCodex` concurrently: the config write is a rename and the store is mutex-guarded, so the later list wins, and both are the same app-server's answer.

## Error handling

Missing binary, version below `codexMinVersion`, no sign-in, failed `model/list`, failed home preparation, or a timeout: the store is not touched, one Info line is logged, the daemon keeps running. Nothing is returned to `runSupervisor`. The log carries the error only: the probe's texts name the version and the Codex home path, never the account; the list result is never logged.

## Testing strategy

New `cmd/pyry/codex_model_read_start_test.go`, on the fake Codex, with a real `modelVocabularyStore` on a temp path and a logger writing to a buffer:

- Success: empty store gains `fakeCodexFamilies` after `wait`; no Info line; the fake's thread and turn logs stay empty; Claude's list untouched.
- Table of failures — `FAKECODEX_SIGNED_OUT`, `FAKECODEX_MODEL_LIST_FAIL`, `FAKECODEX_VERSION` below the minimum, a missing binary — each with a store pre-holding a sentinel Codex list: the held list is unchanged, exactly one Info record, and it contains neither the fake account's email nor any model value.
- `startCodexModelRead` returns before the read completes (the daemon's start is not delayed): with a blocked binary (a shell wrapper that sleeps), the call returns promptly and `wait` joins after cancelling.
- AC3: a daemon store holding no Codex entries; after the read, `modelListFor(reg, pool, store)` for a `multi_agent` client carries the fake's families tagged `codex` (`assertTaggedRows`).

Existing `TestCodexRunnerFactory_Refusals` covers that the probe refactor keeps its errors.

## Documentation handoff

None required by the ticket. Pending for the documentation stage: the Codex model vocabulary section of the owning package overview may note that the list is now read once at daemon start.

## Open questions

- Does `runSupervisor` reach `selectInteractiveRunner` only on the stream-json path? Yes: every other value errors out, so the read runs only where a Codex factory exists.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No new boundary. Codex's `model/list` reply crosses into the daemon through `Client.LatestModels`, which already bounds pages and families and drops ids not shaped `gpt-<version>-<family>`; `RetainCodex` clones and the store's file size cap applies. The same boundary a Codex spawn uses today.
- [Tokens] No findings — the account is only read as a bool by `Client.SignedIn`; the sign-in file in the Codex home is never opened by pyry (`prepareCodexHome` touches only `config.toml`).
- [File operations] No findings — the only write is `prepareCodexHome`: 0700 dir, 0600 config, temp + rename, so a planted symlink is replaced not followed. Concurrent calls from a Codex mint are rename-safe.
- [Subprocess] No findings — binary is the operator's `-pyry-codex` flag, argv is the fixed `app-server`, `CODEX_HOME` is the daemon home; no shell, no user-controlled argument. Kill path is `codexsup.Start`'s process context plus `stopCodexClient`'s SIGTERM/SIGKILL.
- [Crypto] Not applicable — no randomness, keys or comparisons.
- [Network & I/O] No findings — stdio to a local child, bounded by `codexStartTimeout`; list size bounded inside `LatestModels`.
- [Logs] SHOULD FIX — the failure log carries `err`, and a `model/list` RPC error's text is Codex-authored. pyry's own texts (start, version, sign-in) carry no account or model value, and the list is never logged, matching `codexRunner.readModels` which already logs the same error. Phase B: the tests assert the Info line holds neither the fake account's email nor any family value.
- [Concurrency] No findings — one goroutine, cancelled and joined by `wait` before `modelVocabulary.Close`; bounded by timeout otherwise. Store and home writes are already safe against a concurrent mint.
- [Threat model] OUT OF SCOPE — a hostile Codex binary is the operator's own choice via `-pyry-codex`, as for every Codex session.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25

## Revisions

**2026-09-25, during Phase B.** `wait` cancels before it joins, so a test that drives the read through `startCodexModelRead` and then calls `wait` cancels its own read. The read-and-log step is now synchronous and separate from the goroutine: `readCodexModels(ctx, h, dir) error` is the body the Design called `readCodexModelsAtStart`; `readCodexModelsAtStart(ctx, h, dir, log)` runs it and logs the one Info line; `startCodexModelRead` only adds the goroutine, the timeout and `wait`. The success, failure and AC3 tests drive `readCodexModelsAtStart` directly; `TestStartCodexModelRead_DoesNotBlockStart` covers the goroutine and `wait`. Contract otherwise unchanged.
