# Spec #856 — `contextwindow.Read`: context-window usage reader over the resolved transcript

**Size:** S · **Not** `security-sensitive` (read-only reflection of non-secret runtime state from an already-trusted, already-confined local transcript path — no new inbound parsing, no authz, no mutation, no secret; mirrors #847). Leaf of #855; ships **unwired** — the wire child #857 consumes it onto `screen_snapshot`. Same shape as the settings split (#847 leaf → #848 wire).

## Files to read first

- `internal/agentrun/jsonl/reader.go:41-92` — `Event` (`.Usage *UsageBlock`, `.Kind`, `.Raw`, `.EndOfTurn`) + `UsageBlock` (`InputTokens`, `OutputTokens`, `CacheCreationInputTokens`, `CacheReadInputTokens`). Extract: `ev.Usage` is non-nil **only** on assistant entries carrying a `usage` object; these are the four fields the reader sums. Pointer-valued distinguishes "field absent" from "present, all-zeros".
- `internal/agentrun/jsonl/reader.go:188-262` — `Reader.Next()` contract: returns `io.EOF` at end-of-stream; **malformed lines are log-and-skipped (not errors)**; only genuine read failures and `ErrLineTooLarge` surface as errors. Extract: the loop-until-`io.EOF` pattern the reader wraps, and that it already handles partial lines / the 16 MiB per-line cap / unknown extra keys (`server_tool_use`) for free.
- `internal/agentrun/jsonl/reader.go:119-132` — `NewReader(src io.Reader, cfg Config)`; `Config.Logger` optional (defaults `slog.Default()`), `StartOffset` unused here (whole-file scan). Extract: construct over an `*os.File`.
- `internal/agentrun/jsonl/reader_test.go:252-289` — `TestReader_UsageParsedOnAssistant` / `TestReader_UsageNilOnAssistantWithoutUsage`: the exact one-line assistant-with-usage JSON shape to base fixtures on, and how `Usage` surfaces per `Event`.
- `internal/agentrun/jsonl/reader_test.go:14-50` — fixture-test idiom (`testdata/*.jsonl`, drain-to-EOF loop). The helpers (`newFixtureReader`, `drainAll`) are package-private to `jsonl`; **mirror the pattern**, don't import them.
- `internal/agentrun/jsonl/testdata/clean.jsonl` — a real 25-assistant-turn transcript. Confirms the usage shape (`"usage":{"input_tokens":6,"cache_creation_input_tokens":19739,"cache_read_input_tokens":15169,"output_tokens":357,"server_tool_use":{…}}` — note extra keys the decoder ignores) and `"model":"claude-opus-4-7"`. Copy a couple of its lines to seed hand-written fixtures.
- `internal/sessions/reconcile.go:100-124` — `newTranscriptResolver` returns `(path string, size int64, err error)`, `("", 0, nil)` when no transcript exists yet. Extract: this is the seam **#857** calls to obtain the path this reader consumes; its empty-path / nil-error "no transcript" convention pairs with this reader's `path == ""` fresh-session case.
- `internal/sessions/reconcile.go:181-245` — `newProbePreferredTranscriptResolver`: the resolved path is **already canonicalised and confined** to `~/.claude/projects/<encoded-cwd>/` upstream (AC4 confidentiality guard). Extract: the reader must **not** re-resolve, re-confine, or re-canonicalise — it opens the path it is given.
- `docs/specs/architecture/847-default-settings-snapshot-fields.md:1-27` — the unwired-leaf precedent: ships unwired, coverage is its own tests, no live consumer until the wire child lands.
- `CODING-STYLE.md:62-78` — testing idiom (table-driven, stdlib `testing` only, `testdata/` placement, `t.Parallel()`).

## Context

Clients render a "context window used" gauge (mobile Status sheet: "73% used (146K of 200K tokens)"). Nothing on the daemon reports it. The daemon already resolves the active session's transcript path via the probe-preferred resolver (`internal/sessions` → `supervisor.Config.ResolveTranscript`, `(path, size, err)`, already hardened against the untrusted-probe→path crossing), but nothing decodes the per-turn `usage` figures the transcript carries.

This ticket ships the **leaf primitive**: a reader that, given a resolved transcript path, reports the current context-window occupancy. It is **unwired** — no production consumer references it. Its coverage is its own tests over `testdata/` JSONL fixtures. The wire child **#857** obtains the path from the resolver seam and carries the reader's output on `screen_snapshot`.

The decode of the four usage fields already exists and is battle-tested (`internal/agentrun/jsonl`, verified across 1151 real sessions). This reader **reuses** it rather than re-implementing a JSONL scanner — see Design § "Why reuse `jsonl`".

## Design

### Package

New package **`internal/contextwindow`** (one file: `usage.go`). It takes a **path** (string) and has no dependency on `internal/sessions` — so it does not belong inside that package, and putting it there would force `internal/sessions` to gain its first import of `internal/agentrun/jsonl`. A standalone package imports `jsonl` cleanly (`jsonl` is stdlib-only → no cycle) and keeps `internal/sessions` decoupled. Single concern (context-window usage reflection), matching CODING-STYLE's "one package per concern".

### Exported surface (contract, not implementation)

```go
// Usage is the current context-window occupancy derived from a transcript's
// latest usage-bearing assistant entry.
type Usage struct {
    UsedTokens   int // input+cache_read+cache_creation+output on the latest usage entry
    WindowTokens int // the context-window size (defaultWindowTokens today)
}

// Read scans the claude transcript at path and reports current context-window
// usage. See § Error handling for the path=="" / no-usage / open-error split.
func Read(path string) (Usage, error)
```

Two exported symbols total. No `Config`, no logger param (see Concurrency/Error handling).

### Algorithm (behaviour Read must satisfy — the developer writes the body)

1. `path == ""` → return `Usage{UsedTokens: 0, WindowTokens: defaultWindowTokens}, nil` **without opening anything**. This is the "no transcript resolved yet" input (the resolver's `("", 0, nil)`); it is not an error. — **AC-4 (fresh-session) lower bound.**
2. `os.Open(path)`; `defer f.Close()` (best-effort, read-only). Open failure → wrapped error (see Error handling).
3. Construct `jsonl.NewReader(f, jsonl.Config{})` and loop `Next()` until `io.EOF`, tracking the **last** `Event` whose `Usage != nil`:
   - `ev.Usage != nil` → remember it (overwrite any earlier one). Entries without usage (transitional thinking-block assistant entries, user/system/tool lines) do **not** clear the remembered value → the reader tracks "the most recent assistant entry *that carries a usage block*". — **AC-1.**
   - A non-`io.EOF` error from `Next()` (genuine read failure / `ErrLineTooLarge`) → return it wrapped. Malformed JSON lines never reach here (jsonl log-skips them).
4. After the loop:
   - No usage entry seen → `Usage{UsedTokens: 0, WindowTokens: defaultWindowTokens}, nil`. — **AC-4 (transcript exists, no assistant/usage yet).**
   - Usage entry seen → `UsedTokens = last.InputTokens + last.CacheReadInputTokens + last.CacheCreationInputTokens + last.OutputTokens`; `WindowTokens = defaultWindowTokens`; nil error.

**Last-usage-wins is the whole of the compaction behaviour.** After an auto-compaction, Claude Code's next turn records a *smaller* `input_tokens` (the shrunk context). Because the reader always reports the latest usage-bearing entry — with no `max()`, no running total — a post-compaction read naturally returns the smaller, current figure. Compaction surfaces as a reset for free, with no dedicated marker. — **AC-3.**

### Context-window size — single documented default

```go
// defaultWindowTokens is the context window every current Claude model exposes
// (opus / sonnet / haiku are all 200K today). Reported as WindowTokens for
// every session. See Open questions for the per-model divergence seam.
const defaultWindowTokens = 200_000
```

`WindowTokens` is always `defaultWindowTokens`. The Technical Notes explicitly sanction "a single documented default" and require only that *a* size be reported. Every current Claude model shares a 200K window, so the reported number **is** the active model's window for every known model and the documented default for an unknown/absent one — AC-2 holds without model-specific machinery.

**Do not** build a per-model map or extract `message.model` now. Every entry would map to the same 200K value — dead redundancy, and a defense for a divergence (a non-200K Claude model) that has not been observed. The seam for that future is recorded in Open questions (the model is available verbatim on `jsonl.Event.Raw`, so it is a localised follow-up, not a Read rewrite). — **AC-2.**

### Why reuse `jsonl`

`internal/agentrun/jsonl` is a general claude-JSONL parser (its package doc: "parses claude session JSONL output into structured events"), misfiled under `agentrun/` but stdlib-only and stable. Reusing it gives the reader, for free: the pointer-nil "usage absent vs all-zeros" distinction, malformed-line log-and-skip, partial-line buffering, the 16 MiB per-line cap, and tolerance of extra usage keys (`server_tool_use`). Re-implementing a JSONL scanner in the new package would duplicate ~150 hardened lines and re-introduce that risk surface — a Simplicity-First / DRY violation. The reader touches only `ev.Usage`; it does not need any `jsonl` change.

## Concurrency model

None. `Read` is a stateless pure-ish function: open → scan → close, no goroutines, channels, or shared state. Safe to call concurrently (each call opens its own `*os.File` and `jsonl.Reader`, neither of which is shared). No logger is injected — the only logging inside the call is `jsonl`'s malformed-line warning, which defaults to `slog.Default()` and is content-safe by construction (the package's documented invariant: it logs offsets and error strings, never line bytes).

## Error handling

`Read` returns a non-nil error **only** when it cannot scan:
- `os.Open` failure on a non-empty path → `fmt.Errorf("contextwindow: open transcript: %w", err)` with the returned `Usage` being the zero value. The caller checks `err` first. (A raced-away file surfaces here as `fs.ErrNotExist`; #857 may treat that as "no usage yet" — see Open questions — but this reader reports it honestly rather than swallowing it.)
- A non-`io.EOF` error from `jsonl.Reader.Next()` (`ErrLineTooLarge` or an underlying read error) → wrapped and returned.

The two "nothing to report" cases are **not** errors: `path == ""` and "file scanned, no usage entry" both return `Usage{0, defaultWindowTokens}, nil`. This keeps the deterministic-zero contract (AC-4) distinct from genuine I/O failure and lets a consumer distinguish "fresh session" from "couldn't read".

## Testing strategy

Single new test file `internal/contextwindow/usage_test.go` (same-package, table-driven where it reads cleanly, `t.Parallel()`), plus hand-written `internal/contextwindow/testdata/*.jsonl` fixtures. Assert on the `Usage` struct and the `error`; describe scenarios, not code:

- **Latest-turn sum (AC-1):** a fixture with ≥2 assistant-with-usage entries where the last one's four fields are known → `UsedTokens` equals the sum of the *last* entry's four fields (not the first, not a total). Include an assistant entry *without* a usage block after the last usage entry (a transitional thinking-block line) to prove it does not clear the remembered value.
- **Window reported (AC-2):** every non-error case → `WindowTokens == 200_000`. Cover both a known-model fixture and the empty/unknown-model case (both report the default today — this pins "a size is always reported").
- **Compaction reset (AC-3):** a fixture whose earlier turn sums to a large figure (e.g. ~150 000) and whose later turn sums to a much smaller one (e.g. ~30 000, the post-auto-compaction shape) → `UsedTokens` equals the smaller, later figure.
- **Empty / pre-first-turn (AC-4):** (a) a fixture with only `user`/`system` lines and no assistant-with-usage entry → `Usage{0, 200_000}, nil`; (b) `Read("")` → `Usage{0, 200_000}, nil`, no file touched.
- **Scan-error propagation:** `Read` on a non-existent non-empty path → non-nil error, zero `Usage`. (Optional: a fixture line exceeding the cap is overkill; the not-exist case is sufficient to pin the error contract.)
- **Unwired (AC-5):** no production `.go` file outside `internal/contextwindow` references the package — enforced by it being a fresh package with zero importers; the developer need not add a test for this, but must not wire it into any consumer.

Run `go test -race ./internal/contextwindow/...` and `go vet ./...`.

Fixture construction: copy one or two real lines from `internal/agentrun/jsonl/testdata/clean.jsonl` and edit the `usage` numbers to hit the target sums; keep each fixture to the handful of lines each scenario needs (a `user` line + the assistant-with-usage line(s)). Fixtures are data, not Go LOC.

## Open questions

- **Per-model window divergence (deferred).** When Anthropic ships a Claude model whose context window ≠ 200 000, extend the window source: read `message.model` from the latest usage-bearing entry (available verbatim on `jsonl.Event.Raw`, decodable with a one-field local struct — no `jsonl` change) and map it, keeping `defaultWindowTokens` as the unknown-model fallback. Localised to a `windowForModel(model string) int` helper feeding `WindowTokens`; not a `Read` rewrite. Not built now (no such model exists — evidence-based).
- **`path == ""` guard placement (informational, resolved for this ticket).** `Read` owns the `path == ""` → zero case, so #857 can pass the resolver's `("", 0, nil)` straight through without a guard. Whether #857 also maps a raced-away `fs.ErrNotExist` to zero is #857's call; this reader surfaces it as an error.
- **Whole-file scan cost.** `Read` scans to EOF on every call. Claude transcripts are typically tens–low-hundreds of KB and this is an on-demand snapshot read, not a hot loop, so a forward scan is fine. If profiling later shows it matters (evidence-based), a bounded tail-scan using the resolver's `size` is the optimisation — deferred.
