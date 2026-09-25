# #2627 — Hold Codex's newest version per family beside Claude's models

## Files read

- `internal/codexsup/methods.go` → `clientRequests` — the list `TestMethodNamesInSchema` checks against the schema; `model/list` joins it.
- `internal/codexsup/client.go` → `Client.call`, `Client.SignedIn` — the shape a new read-only call mirrors (typed result, no params logged).
- `internal/codexsup/codex_app_server_protocol.schemas.json` → `v2/ModelListParams`, `v2/ModelListResponse`, `v2/Model`, `v2/ReasoningEffortOption` — `cursor`/`limit`/`includeHidden` in, `data` + `nextCursor` out; `Model.id`, `supportedReasoningEfforts[].reasoningEffort`.
- `internal/acp/acp.go` → `maxLineBytes` — one response line is already capped at 16 MiB by the transport.
- `internal/codexsup/client_test.go` → `startPeer`, `peer.next`, `startFake` — the scripted in-memory peer (paging edge cases) and the fake-process harness.
- `internal/e2e/internal/fakecodex/main.go` → `requestHandlers`, `accountRead` — where the fake's `model/list` handler goes; its `TestMethodNamesInSchema` checks handler names against the schema.
- `cmd/pyry/model_vocabulary_store.go` → `modelVocabularyStore`, `Load`, `Retain`, `drain`, `encodeModelVocabulary`, `modelVocabularyFile`, `maxModelVocabularyFile` — the store the Codex entries join. Its no-logger posture and "file is daemon-written, only an aggregate cap on load" posture carry over.
- `cmd/pyry/codex_runner.go` → `codexHarness`, `newCodexRunnerFactory`, `codexRunnerConfig`, `codexRunner.runOnce` — where a client is started and bound; the read goes here.
- `cmd/pyry/main.go` → `selectInteractiveRunner` — the one place `codexHarness` is completed with daemon-wide objects; the store reaches the Codex factory here.
- `internal/streamsup/parser.go` → `maxModelListEntries`, `maxModelValue`, `maxModelEffortLevel`, `maxModelEffortLevelCount` — how Claude's list is bounded; the Codex bounds follow the same dimensions.
- `cmd/pyry/session_model_list.go` → `retainedModelVocabulary` consumers read `modelVocabularyStore.ModelList` — which must keep returning Claude's list only.

No in-flight branch touches these files.

## Context

Slice S3d, first of three. The menu will list the newest version of each model family for both agents. Codex's `model/list` lists every version (`gpt-6-sol`, `gpt-5.6-sol`, …); this ticket reads it on every Codex spawn, reduces it to one entry per family, and holds the result in the daemon's model vocabulary store beside Claude's list, persisted to `model_list.json`. Nothing reads the Codex entries on the wire yet (#2589 exposes them; siblings follow a family per turn and check effort per model).

## Design

### codexsup: `Client.LatestModels(ctx) ([]turnevent.ModelOption, error)` — new file `internal/codexsup/models.go`

Reads `model/list` page by page (`{"cursor": <next>}`; no `limit`, `includeHidden` unset), folding each page into a bounded per-family table before asking for the next, so memory never holds more than one decoded page plus the table. Returns one option per family in order of the family's first appearance:

- `Value` = family, `ResolvedModel` = newest version's `id`, `EffortLevels` = that version's advertised `reasoningEffort` values in order (nil when none). `DisplayName` stays empty and no other field is set: the Codex entry carries exactly the three fields the AC names.
- End of list: `nextCursor` null, absent or `""`.
- **Page cap** `maxModelListPages = 16`: a 16th page that still names a next cursor fails the read (`errModelListPages`). A partial read is not returned, because retaining it could silently drop a family that lived on a later page.

Pure fold, unexported and table-tested: `parseFamilyID(id) (version []int, family string, ok bool)` and `foldModel(table, entry)`.

- **Id shape**: `gpt-<version>-<family>`. `version` is the text between `gpt-` and the next `-`: one or more dot-separated decimal components, each parsed with `strconv.Atoi` (overflow → not ok). `family` is the rest: must match `[a-z][a-z0-9-]*`, ≤ 32 bytes. The whole id must be ≤ 64 bytes of `[a-z0-9.-]`. Anything else (e.g. `gpt-reserve`, `gpt-6`, `o3`) is left out.
- **Newest**: `slices.Compare` on the component slices, so `[6] > [5 6]`; on a tie the first seen is kept.
- **Effort levels**: each must be 1–32 bytes of `[a-z0-9_-]`; others are dropped; at most `maxModelEffortLevels = 8` kept, first in order.
- **Family cap** `maxModelFamilies = 8`: an entry for a family not already in the table once it holds 8 is left out (a newer version of a held family still replaces it).

Because every retained string is from a closed ASCII alphabet, none needs JSON escaping, none carries a control character, and none can be mistaken for markup — the bound is a validation, not a truncation, so there is no `TruncatedFields` to carry.

`methodModelList = "model/list"` joins `clientRequests` in `methods.go`.

### Store: Codex entries beside Claude's — `cmd/pyry/model_vocabulary_store.go`

- New field `codex []turnevent.ModelOption` beside `list`/`have`; replaced by assignment, never mutated in place (the drain's snapshot argument applies unchanged).
- **The agent tag is the list an entry is stored in.** In memory `list` is Claude's and `codex` is Codex's; on disk Codex's entries sit under a new top-level key `codex_models`. A per-entry `agent` field was rejected: an older daemon (downgrade) reads only `models` and ignores the unknown key, so it can never serve a Codex entry as a Claude one.
- `RetainCodex(models []turnevent.ModelOption)` — nil-receiver safe; an empty slice is a no-op (a read that found no family keeps what is held, same answer as a failed read); otherwise clones, sets `codex`, marks dirty and starts the drain exactly as `Retain` does. Neither retention touches the other's field.
- `CodexModels() []turnevent.ModelOption` — deep copy, nil when none held. `ModelList()` is unchanged and still returns Claude's list only, so `retainedModelVocabulary`, `validateModelVocabulary` and the `model_list` frame see no Codex entry.
- On disk: `modelVocabularyFile` gains `CodexModels []codexVocabularyOption \`json:"codex_models,omitempty"\``; `codexVocabularyOption{Value, ResolvedModel, EffortLevels(omitempty)}` — its own record type for the reason the Claude one has. With no Codex entries the key is omitted, so a pre-change file re-encodes byte-identically.
- `encodeModelVocabulary(list, codex)`: `models` is written from `list` as today (an empty array when only Codex is held, i.e. `have` false).
- `Load`: decodes both; holds Claude's when `models` is non-empty (unchanged rule) and Codex's when `codex_models` is non-empty; returns without seeding `lastWritten` only when neither is present. Codex entries with an empty value or resolved model are a contract violation and drop the whole Codex section (Claude's still loads).
- `maxModelVocabularyFile` → **128 KiB**. Worst case: Claude 10 × 1024 bytes, which at json's 6-byte `\u` escapes is ≤ 60 KiB; Codex 8 × (32 + 64 + 8 × 32) = 2.8 KiB, escape-free; keys and structure < 8 KiB. 64 KiB no longer covered the escaped Claude worst case plus Codex; 128 KiB does with margin.

### Runner — `cmd/pyry/codex_runner.go`

- `codexHarness` gains `vocab *modelVocabularyStore`; `codexRunnerConfig` gains `Models func([]turnevent.ModelOption)`; the factory passes `h.vocab.RetainCodex` (nil store → nil-safe method).
- `runOnce`: after the client is bound and the thread report, before waiting on exit, `r.readModels(ctx, client)` calls `LatestModels` under `context.WithTimeout(ctx, codexStartTimeout)` and hands a success to `cfg.Models`. Placed after binding so a turn is never delayed by the read; a failure logs one Warn (`session`, `err`) and leaves the held entries alone; it never fails the spawn. Nil `Models` skips the read.

### main.go

`selectInteractiveRunner` sets `codex.vocab = vocab` beside `codex.sink`/`codex.approval`.

### Fake Codex

`model/list` handler answering from an embedded fixture `internal/e2e/internal/fakecodex/models.json` (GPT-6 Sol, GPT-5.6 Sol, GPT-6 Luna, GPT-5.5 Luna, GPT-5.6 Terra, GPT-6 Astra, `gpt-reserve`; every required `Model` field filled). Pages of 3 entries by default, `limit` honoured; cursor is the decimal offset. `FAKECODEX_MODEL_LIST_FAIL` non-empty → JSON-RPC error.

## Concurrency model

No new goroutine. The read runs on the Run goroutine inside `runOnce`, bounded by its own timeout and by the client's exit (`Client.call` fails on `exitCtx`). `acp.Transport` already serves concurrent calls (turn/start and turn/interrupt from other goroutines). `RetainCodex` takes the store's leaf mutex exactly as `Retain` does and never across I/O; the one drain goroutine writes both lists in one snapshot.

## Error handling

- `model/list` RPC error, decode error, exit mid-read, timeout, page cap → error returned, runner logs and keeps held entries.
- Zero families → empty slice → `RetainCodex` no-op.
- Load failures keep the existing single answer (nothing retained); a bad Codex section alone drops only Codex's entries.

## Testing strategy

- `internal/codexsup/models_test.go`: table test of `parseFamilyID` (major/minor, missing family, bad charset, over-long, overflow); fold test — grouping, `gpt-6-sol` beats `gpt-5.6-sol` whatever the order, family-less entry left out, effort levels validated/ordered/capped, family cap; peer test — three pages with cursors, `includeHidden` absent from params; page cap exceeded → error; `LatestModels` against the fake returns the fixture's four families.
- `cmd/pyry/model_vocabulary_store_test.go`: Codex entries survive Close + Load; Claude retention keeps Codex and Codex retention keeps Claude (memory and disk); a literal pre-change file loads and re-encodes to identical bytes; `ModelList()` stays Claude-only (false when only Codex held); empty `RetainCodex` is a no-op.
- `cmd/pyry/codex_runner_test.go`: a spawn against the fake reports the four families to `Models`; with `FAKECODEX_MODEL_LIST_FAIL` the runner still binds and reports nothing.

## Open questions

- None blocking. Whether the family cap of 8 should grow is for #2589 when the wire shape is fixed.

## Documentation handoff

Pending for the documentation stage: fold the Codex model-family read and the `codex_models` key of `model_list.json` into `docs/knowledge/features/` (codexsup and the model-vocabulary store's owning topic). The ticket names no other documentation requirement.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — the one boundary is `Client.LatestModels`: Codex-authored JSON becomes a `[]turnevent.ModelOption` only through `parseFamilyID` and the effort-level check, which admit strings from closed ASCII alphabets with length caps. Everything downstream (`RetainCodex`, the file, #2589's wire) holds validated values only. `Model.displayName`, `description` and every other field are never decoded into anything retained.
- [Tokens] No findings — `model/list` carries no credential; the account fields are not read here.
- [File operations] No findings — the destination path is fixed at store construction (`resolveModelVocabularyPath`); writes keep `writeModelVocabularyFile`'s temp-in-dir, 0600, fsync, rename recipe; no Codex string reaches a path.
- [Subprocess] No findings — no new process or argument; the read uses the already-running client. `includeHidden` stays unset.
- [Crypto] Not applicable — no randomness or keys.
- [Network & I/O] No findings — one response line is capped by `acp`'s `maxLineBytes`; pages capped at 16; the table capped at 8 families × 8 levels; each page is folded and dropped before the next; the read has its own timeout and ends when the client exits, so a never-ending cursor or a stalled server cannot stall the spawn or the daemon. Load's cap raised to 128 KiB with the arithmetic above.
- [Logs] SHOULD FIX (handled in design) — the store stays logger-free. The runner's one Warn logs the error, which can carry Codex's JSON-RPC error message: the same posture `Run` already takes for `open thread` errors, and no model value or page content is ever put in a log field. Phase B must not log the models.
- [Concurrency] No findings — no new goroutine; store mutex stays a leaf; one drain writes a consistent snapshot of both lists; a crash mid-write leaves the previous file (rename commit).
- [Threat model] No findings — Codex entries never reach the relay in this ticket (`ModelList()` unchanged); #2589 inherits already-validated values.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25
