# #88 — `pyry sessions list` CLI verb (table + `--json`)

Phase 1.1b-B2. Operator-facing surface for the `sessions.list` wire
verb landed by #87. Plugs into the `runSessions` router landed by #76,
adds the fourth and final read-side member of the `sessions.<verb>`
family planned for Phase 1.1 (`new`, `rm`, `rename`, `list`).

This spec is small by design: the wire layer is done, the router is
done, the client wrapper is already consumed by `resolveSessionIDViaList`
(the prefix-resolution helper #94 added for `sessions.rm`). What's
novel here is the rendering policy — the column set, the tabwriter
config, the `--json` envelope shape. Those choices become the template
1.1c/d follow when later verbs add tabular output.

## Files to read first

- `cmd/pyry/main.go:21` — the top-level package-comment verb list
  (`pyry sessions <verb>  Multi-session management (verbs: new, rm, rename)`).
  Append `, list` in lockstep with `sessionsVerbList`.
- `cmd/pyry/main.go:487-528` — the `runSessions` router and
  `sessionsVerbList` constant. The new `case "list":` slots in next to
  `case "rename":`; the constant gets `, list` appended.
- `cmd/pyry/main.go:531-566` — `parseSessionsNewArgs` /
  `runSessionsNew`. The simpler precedent for the new verb's parse +
  handler split (no exit-2 sentinel; all errors are exit 1 via
  `fmt.Errorf("sessions list: %w", err)` returned to main).
- `cmd/pyry/main.go:374-407` — `runStatus`. The closest precedent for
  a one-shot read verb: 5s timeout, `parseClientFlags` (n/a here —
  the router already peeled the flags), `control.X(ctx, sock)`, error
  wrap, print on success.
- `cmd/pyry/main.go:621-666` — `resolveSessionIDViaList`. Already
  consumes `control.SessionsList`; confirms the wire is alive and the
  decoded `[]control.SessionInfo` is the shape this verb renders.
  Reuse the same `Bootstrap && Label == ""` substitution **only if
  needed** — see "Bootstrap label" below; the wire already substitutes,
  so this layer renders verbatim.
- `cmd/pyry/main.go:909-962` — `printHelp`. The `pyry sessions <verb>`
  line at 928-929 lists the verbs in parens; append `list` to the
  closing `(verbs: new, rm, rename)` fragment.
- `cmd/pyry/sessions_test.go:75-90` — `TestRunSessions_RmDispatch` and
  `:150-165` `TestRunSessions_RenameDispatch`. The new
  `TestRunSessions_ListDispatch` mirrors these: bogus socket → asserts
  the error wraps with `"sessions list:"` rather than the router's
  `"unknown verb"`.
- `cmd/pyry/sessions_test.go:217-258` — `TestParseSessionsNewArgs`.
  Template for `TestParseSessionsListArgs`'s table.
- `internal/control/protocol.go:64-70,202-237` — `VerbSessionsList`,
  `SessionsListPayload`, `SessionInfo`. The wire shape this layer
  consumes. Note `SessionInfo.LastActive` is `time.Time` (RFC3339Nano
  on the wire); the table renderer formats it for display, the JSON
  renderer hands it back to `encoding/json` unchanged.
- `internal/control/client.go:188-217` — `SessionsList(ctx, sock)
  ([]SessionInfo, error)`. The one-shot wire client this verb wraps.
  Returns the daemon's snapshot in `Pool.List`'s order (LastActiveAt
  desc, SessionID asc tiebreak per #87's spec) — which is already the
  order this verb renders, but per AC the renderer re-sorts to be
  defensive against future wire changes.
- `internal/e2e/sessions_new_test.go:110-139` —
  `TestSessionsNew_E2E_UnknownVerb`. **This test currently uses
  `pyry sessions list` as the placeholder unknown verb** (line 122).
  Once `list` lands it stops being unknown; this ticket must change
  that fixture to a still-unknown verb (e.g. `bogus`) in the same
  edit. One-line change; not a test rewrite.
- `internal/e2e/sessions_new_test.go:74-108` —
  `TestSessionsNew_E2E_Unlabelled`. Template for the new e2e tests:
  `StartIn(t, home, ...)`, `h.Run(t, "sessions", ...)`, assert exit +
  stdout/stderr, optionally read the registry to confirm.
- `internal/e2e/cli_verbs_test.go:49-80` — `TestStatus_E2E_Stopped`.
  Template for the no-daemon case: `RunBare(t, ...,
  "-pyry-socket="+bogusSock)`, assert non-zero exit + non-empty
  stderr + no panic/goroutine markers.
- `internal/e2e/restart_test.go:13-148` — `registryEntry` /
  `readRegistry` / `newRegistryHome` / `mustReadFile`. The shared e2e
  fixtures already used by `sessions_new_test.go`; reuse verbatim.
- `docs/specs/architecture/87-control-sessions-list.md` (whole file)
  — the wire layer's spec. Confirms the wire payload shape and the
  encoding rules (`state` is `"active"`/`"evicted"`, `last_active` is
  `time.Time`, `bootstrap` is omitempty).
- `docs/specs/architecture/76-cli-sessions-new.md` § "Sub-router" and
  § "data flow" — the router shape this verb plugs into.
- `docs/lessons.md` § "JSON roundtrip strips monotonic-clock state
  from `time.Time`" — applicable to any test that compares pre-encode
  and post-decode `LastActive` values; **not** triggered for this
  ticket's tests because the renderer formats / re-encodes rather
  than comparing, but worth keeping in mind for the JSON-renderer
  golden test.

## Context

Today the Phase 1.1 sessions verb family covers `new`, `rm`, and
`rename`. The wire layer for `list` shipped in #87 and is already
consumed by `resolveSessionIDViaList` (the prefix-resolution helper
behind `pyry sessions rm <prefix>`). What's missing is the
operator-facing CLI surface — `pyry sessions list [--json]`.

This is also the first text-table sink in `cmd/pyry`. `pyry status`
prints labelled key-value lines; `pyry logs` prints raw lines; nothing
in `cmd/pyry` has needed `text/tabwriter` until now. The renderer
choices made here (column set, padding, `--json` envelope) are the
template the rest of Phase 1.1 follows when later verbs add tabular
output. The "operators copy/paste UUIDs" property must hold — full
36-char UUIDs, no truncation.

Three operational shapes the CLI surface must support:

1. **Snapshot read.** Dial → encode → decode → print → exit. Same
   one-shot lifecycle as `pyry status` / `pyry logs`.
2. **Two output formats from the same data.** Default is the human
   table; `--json` swaps the renderer. Both consume the same
   `[]control.SessionInfo` slice from `control.SessionsList`.
3. **No-daemon error path.** `control.SessionsList`'s dial failure
   surfaces verbatim through `fmt.Errorf("sessions list: %w", err)`,
   which `main`'s top-level error printer renders as
   `pyry: sessions list: dial /path/sock: connect: no such file or
   directory`. No special handling needed — same shape as `pyry status`
   on a stopped daemon.

## Design

### Top-level edits (cmd/pyry/main.go)

Three one-line updates plus one constant change:

1. **Package comment (line 21).** `(verbs: new, rm, rename)` →
   `(verbs: new, rm, rename, list)`.
2. **`sessionsVerbList` constant (line 491).** `"new, rm, rename"` →
   `"new, rm, rename, list"`.
3. **`runSessions` switch (line 519-528).** Add `case "list": return
   runSessionsList(socketPath, subArgs)` after `case "rename":`.
4. **`printHelp` (line 928-929).** `(verbs: new, rm, rename)` →
   `(verbs: new, rm, rename, list)`.

The two verb-list strings (constant + help) are deliberately
duplicated rather than centralised — same rationale as #76's
spec ("dead-simple, beats indirection"). One-token edit per future
verb in two places; no map, no derivation.

### `parseSessionsListArgs` (cmd/pyry/main.go)

```go
// parseSessionsListArgs parses `[--json]`. Returns (jsonOut, err).
// No positional arguments accepted — `pyry sessions list` lists every
// session in one shot; future filter flags (--state, --label-prefix)
// would slot in here.
//
// Mirrors parseSessionsNewArgs's shape: extracted so flag-parsing rules
// are unit-testable without dialling the control socket. Errors are
// returned verbatim (no errSessionsListUsage sentinel) — runSessionsList
// wraps them through fmt.Errorf("sessions list: %w", err) and exits 1,
// matching runSessionsNew's exit-1-on-parse-error precedent. (rm/rename
// use exit 2 because their AC explicitly distinguishes usage from
// runtime errors; list's AC does not.)
func parseSessionsListArgs(args []string) (jsonOut bool, err error) {
    fs := flag.NewFlagSet("pyry sessions list", flag.ContinueOnError)
    fs.SetOutput(os.Stderr)
    jsonFlag := fs.Bool("json", false, "emit JSON instead of a human table")
    if err := fs.Parse(args); err != nil {
        return false, err
    }
    if fs.NArg() > 0 {
        return false, fmt.Errorf("unexpected positional %q", fs.Arg(0))
    }
    return *jsonFlag, nil
}
```

### `runSessionsList` handler (cmd/pyry/main.go)

```go
// runSessionsList implements `pyry sessions list [--json]`: dial the
// daemon's control socket, fetch the session snapshot, render it as
// either a human-readable table or a single JSON object. Empty pool
// (would only ever contain bootstrap) renders a one-row table or a
// one-element sessions array.
func runSessionsList(socketPath string, args []string) error {
    jsonOut, err := parseSessionsListArgs(args)
    if err != nil {
        return fmt.Errorf("sessions list: %w", err)
    }

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    list, err := control.SessionsList(ctx, socketPath)
    if err != nil {
        return fmt.Errorf("sessions list: %w", err)
    }

    sortSessionsForDisplay(list)

    if jsonOut {
        return writeSessionsJSON(os.Stdout, list)
    }
    return writeSessionsTable(os.Stdout, list)
}
```

**Timeout.** 5s — same as `runStatus` / `runLogs`. The handler is
in-memory on the server side (one `Pool.List` call); 5s is generous.
Diverges from rm/rename's 30s because those wait for `Pool.mu` against
active sessions and run claude-spawn paths on creation; list does not.

**Error wrapping.** `fmt.Errorf("sessions list: %w", err)` makes the
no-daemon case render as
`pyry: sessions list: dial /path/sock: connect: no such file or directory`.
Identical shape to `pyry status` against a stopped daemon — pins the
"consistent with `pyry status` / `pyry stop`" half of the no-daemon AC.

### Sort policy (cmd/pyry/main.go)

```go
// sortSessionsForDisplay applies the renderer's deterministic order
// in place: LastActive descending (most recent first), SessionID
// ascending as the tiebreak. The wire (Pool.List) already returns
// this order today, but the AC says the renderer enforces it — a
// defence against future wire changes that would otherwise reshuffle
// every operator's table.
//
// Use sort.SliceStable so equal-timestamp entries land in deterministic
// SessionID order without depending on the input slice's pre-existing
// order.
func sortSessionsForDisplay(list []control.SessionInfo) {
    sort.SliceStable(list, func(i, j int) bool {
        if !list[i].LastActive.Equal(list[j].LastActive) {
            return list[i].LastActive.After(list[j].LastActive)
        }
        return list[i].ID < list[j].ID
    })
}
```

`time.Time.Equal` (not `==`) handles the monotonic-clock-stripped
values the JSON roundtrip produces. `time.Time.After` is fine on
stripped values — it compares wall-clock seconds + nanos.

### Table renderer (cmd/pyry/main.go)

```go
// writeSessionsTable renders the snapshot as a tabwriter-aligned
// table to w. Single trailing newline; no header dividers; padding
// is two spaces between columns (the established Go-CLI convention,
// e.g. `gofmt -d -l`'s output and `go list -m`'s columns).
//
// Columns: UUID, LABEL, STATE, LAST-ACTIVE.
//
// LAST-ACTIVE is rendered as RFC3339 (not RFC3339Nano — the nanosecond
// suffix is noise for an at-a-glance column; jq consumers wanting
// nanos use --json).
//
// Empty Label renders as the empty cell. The wire substitutes the
// bootstrap entry's empty on-disk label with "bootstrap" before
// returning (per #87's spec § "Sessions: list seam"); this layer
// renders verbatim.
//
// UUID is rendered in its full 36-character canonical form; no
// truncation. The "operators copy/paste UUIDs" property is the
// AC-load-bearing one.
func writeSessionsTable(w io.Writer, list []control.SessionInfo) error {
    tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
    if _, err := fmt.Fprintln(tw, "UUID\tLABEL\tSTATE\tLAST-ACTIVE"); err != nil {
        return err
    }
    for _, s := range list {
        if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
            s.ID, s.Label, s.State, s.LastActive.Format(time.RFC3339)); err != nil {
            return err
        }
    }
    return tw.Flush()
}
```

**Why `text/tabwriter` and not hand-rolled `Sprintf("%-38s")`.**
`%-38s` requires the developer to compute the maximum width per
column ahead of time. `tabwriter` does it automatically and produces
identical-looking output. Stdlib, no external dep, zero ceremony.

**Why padding=2.** Two-space gaps are the standard Go CLI convention
(see `go list -m`, `go env`, `go version -m`). Padding=1 produces a
cramped table; padding=3+ wastes terminal width.

**Why RFC3339 for the table, not relative time ("3m ago").** Two
reasons: (a) absolute timestamps are unambiguous across timezones and
across log-paste-into-issue boundaries; (b) `time.Now().Sub(...)`
formatted as `"3m ago"` is locale- and tz-friendly but less useful
when the operator is ssh'd to a server in a different region. Phase
1.1c/d operator feedback may revisit; not now.

### JSON renderer (cmd/pyry/main.go)

```go
// writeSessionsJSON encodes the snapshot as a single JSON object with
// a top-level "sessions" array. The envelope is intentionally NOT a
// bare array — leaves room for future top-level fields (e.g.
// "generated_at", "schema_version") without a breaking change.
//
// Per-element shape is whatever encoding/json produces from
// control.SessionInfo (id, label, state, last_active, optional
// bootstrap). The timestamp pass-through is verbatim — same RFC3339Nano
// the wire delivered. No second conversion (per AC).
//
// Output is a single line + trailing newline (json.Encoder.Encode's
// default). jq-debuggable; pipeable to jq / jaq / xq.
func writeSessionsJSON(w io.Writer, list []control.SessionInfo) error {
    payload := struct {
        Sessions []control.SessionInfo `json:"sessions"`
    }{Sessions: list}
    enc := json.NewEncoder(w)
    return enc.Encode(payload)
}
```

**Why an inline anonymous struct rather than re-using
`control.SessionsListPayload`.** Two reasons: (a) the wire payload's
field name is `Sessions` with the JSON tag `"sessions"` — same shape
we want; using it directly is fine, but importing it just for one
struct reads as accidental coupling between CLI presentation and wire
payload. The anonymous struct documents the intent locally. (b) The
anonymous form makes it trivial to add CLI-only top-level fields
later without touching `protocol.go`. The cost is one extra struct
declaration; the win is a clean separation between "what the wire
returns" and "what we print to stdout". **Trade-off acknowledged;
either choice is defensible.** If the developer prefers reusing
`control.SessionsListPayload`, that's also fine — the wire bytes
under `--json` are identical either way.

**Why `Encoder.Encode` and not `json.Marshal` + `Println`.** `Encode`
appends a single `\n`, which is what jq pipelines expect. `Marshal`
returns the bytes without a trailing newline; `Println` would add
one but go through `fmt`'s formatting path, which for a `[]byte`
prints the byte slice's `String()` form, not the JSON. Marginal
detail; `Encode` is the idiomatic choice.

### Imports (cmd/pyry/main.go)

This ticket adds three new stdlib imports to `cmd/pyry/main.go`:
`encoding/json`, `io`, `text/tabwriter`. The existing import block
(lines 28-47) already has `sort`, `strings`, `time`, `fmt`, `flag`,
`os`, `context`, `errors` — sufficient for everything else.

### Data flow

```
operator                         pyry CLI                       daemon
────────                         ────────                       ──────
$ pyry sessions list
                                run()
                                  os.Args[1] == "sessions"
                                  runSessions(os.Args[2:])
                                    parseClientFlags → socketPath
                                    sub="list"
                                    runSessionsList(socket, [])
                                      parseSessionsListArgs → jsonOut=false
                                      control.SessionsList(ctx, sock)
                                        dial unix sock
                                        encode {verb:"sessions.list"}
                                          ─────────────────────►
                                                                handleSessionsList
                                                                  pool.List()
                                                                  encode {sessionsList:
                                                                    {sessions:[...]}}
                                          ◄─────────────────────
                                        decode → []SessionInfo
                                      sortSessionsForDisplay
                                      writeSessionsTable(stdout, list)
                                        tabwriter: header + rows
$ UUID  LABEL  STATE  LAST-ACTIVE     exit 0
  ...
```

`--json` swaps `writeSessionsTable` for `writeSessionsJSON`; the rest
is identical.

### Concurrency

None. The CLI is one short-lived process per invocation — dial,
encode, decode, render, exit. All concurrency is on the server side
(per #87's spec § "Concurrency"). This handler is a synchronous
wrapper.

### Error handling

| Scenario | User-facing message | Source | Exit |
|---|---|---|---|
| `pyry sessions list` against stopped daemon | `pyry: sessions list: dial /path/sock: connect: no such file or directory` | `request()` → `dial()` wrap | 1 |
| `pyry sessions list extra` (positional) | `pyry: sessions list: unexpected positional "extra"` | `parseSessionsListArgs` arity | 1 |
| `pyry sessions list --bogus` (unknown flag) | `pyry: sessions list: flag provided but not defined: -bogus` | `flag.FlagSet.Parse` | 1 |
| Server-side `Pool.List` impossibility | n/a — `Pool.List` cannot fail per #60. The seam's nil-sessioner branch (`sessions.list: no sessioner configured`) propagates verbatim through `Response.Error` and renders as `pyry: sessions list: sessions.list: no sessioner configured`. | `Response.Error` | 1 |
| Empty / nil response | `pyry: sessions list: control: empty sessions.list response` | `control.SessionsList`'s nil-payload guard | 1 |

The only path that can produce a `Response.Error` in practice is the
nil-sessioner case (impossible at runtime — `cmd/pyry/main.go:314`
passes the real pool). It's caught here for completeness, not because
it's expected.

### What stays out of scope

- **No `--state` / `--label` filter flags.** Per the issue's "Out of
  scope": filtering and non-default sort orders are deferred. The
  operator pipes `--json | jq` for filter-by-state today.
- **No relative-time ("3m ago") column.** Defer to operator feedback.
- **No registry-direct fallback.** When the daemon is down, the
  operator's recourse is to start it, not to read `sessions.json`
  out-of-band. The "no pyry running" diagnostic is the right answer
  here, not a degraded read path.
- **No coloured output / TTY detection.** `text/tabwriter` produces
  plain text; pipes work the same as terminals. ANSI colour for
  STATE could land later as a `--color=auto` flag if anyone asks.

## Testing strategy

Three test files. Stdlib `testing` only, no testify, table-driven
where the input space is enumerated.

### Unit: `cmd/pyry/sessions_test.go` (extend, ~80 LOC added)

1. **`TestRunSessions_ListDispatch`** — mirrors `TestRunSessions_Rm
   Dispatch`. `runSessions(["-pyry-socket=<bogus>", "list"])` must
   return an error wrapping with `"sessions list:"` rather than the
   router's `"unknown verb"` — confirms `case "list":` is wired.

2. **`TestParseSessionsListArgs`** — table-driven over:
   - empty args → `(jsonOut=false, err=nil)`
   - `["--json"]` → `(true, nil)`
   - `["-json"]` → `(true, nil)` (single-dash, Go's flag package accepts both)
   - `["--json=true"]` → `(true, nil)`
   - `["--json=false"]` → `(false, nil)`
   - `["--unknown"]` → error containing `"flag provided but not defined"`
   - `["extra"]` → error containing `"unexpected positional"`
   - `["--json", "extra"]` → error containing `"unexpected positional"`

3. **`TestWriteSessionsTable`** — golden-table assertion on a
   deterministic 3-entry slice. Asserts:
   - first line is exactly `UUID  LABEL  STATE  LAST-ACTIVE` (with
     tabwriter's two-space padding applied — match against the
     post-flush output)
   - exactly 4 lines + 1 trailing newline (header + 3 rows)
   - each UUID appears unmodified at column 0 of its row (full 36
     chars; no truncation)
   - empty-label entry renders an empty middle column without
     crashing the column alignment

   Use `bytes.Buffer` as the `io.Writer`. No daemon, no network. The
   "exact 4 lines" assertion catches regressions like a missing
   trailing newline or an extra header divider.

4. **`TestWriteSessionsJSON`** — feed the same 3-entry slice, decode
   the output back into a `struct{ Sessions []control.SessionInfo }`,
   assert:
   - top-level key is exactly `sessions` (decode into the struct
     succeeds with no `json: unknown field` if you use a strict
     decoder)
   - decoded slice has `len == 3`
   - per-element fields round-trip — IDs, labels, states match the
     input; `LastActive` matches under `time.Equal` (not `==`, per
     `lessons.md` § monotonic-clock).
   - bootstrap entry has `Bootstrap: true` after decode; non-bootstrap
     entries have `Bootstrap: false` (the omitempty `"bootstrap"`
     field elides when false on the wire, but `encoding/json`
     decodes the absent field into the zero value, so the Go-side
     comparison is `Bootstrap == false`)

5. **`TestSortSessionsForDisplay`** — table-driven over:
   - already-sorted descending → unchanged
   - reverse-sorted (ascending) → flipped to descending
   - equal timestamps → tiebreak by ID ascending (deterministic)
   - empty slice → no panic, len stays 0
   - single element → unchanged

   Pure function, no I/O. Pin the AC's "stable secondary order by
   UUID so tests are deterministic".

### E2E: `internal/e2e/sessions_list_test.go` (new, ~150 LOC)

Build-tag `//go:build e2e` (mirrors `cli_verbs_test.go`).

6. **`TestSessionsList_E2E_Table`** — `StartIn(t, home, ...)`, mint
   two sessions via `h.Run("sessions", "new", "--name", "alpha")`
   and `("--name", "beta")`, then run `h.Run("sessions", "list")`.
   Assert exit 0. Assert stdout starts with the header
   `UUID  LABEL  STATE  LAST-ACTIVE`. Assert at least three data rows
   (bootstrap + 2 minted). Assert each printed UUID matches the
   canonical 36-char regex used in `sessions_new_test.go`. Assert
   the row containing `alpha` and the row containing `beta` both
   appear, and a row whose label is empty or `bootstrap` appears.

   **Ordering assertion.** Capture each printed UUID with its line
   index; assert the most-recently-minted (`beta`, minted second)
   appears at a lower line index than `alpha` — i.e. ordering is
   last-active descending. Capture stdout via the existing
   `r.Stdout` byte slice; split by `\n`, skip the header.

7. **`TestSessionsList_E2E_JSON`** — same setup as above (mint two
   sessions). Run `h.Run("sessions", "list", "--json")`. Assert
   exit 0. `json.Unmarshal(r.Stdout, &payload)` into a struct
   `{ Sessions []control.SessionInfo }`. Assert `len(payload.Sessions)
   >= 3`. Assert at least one `Sessions[i].Bootstrap == true`. Assert
   each `LastActive` is non-zero. Assert the labels include `"alpha"`
   and `"beta"`. Pins the AC's "stable enough that `jq '.sessions[]
   .label'` works".

8. **`TestSessionsList_E2E_BootstrapOnly`** — `StartIn` with no minted
   sessions. Wait for the bootstrap entry via `waitForBootstrap`
   (existing helper). Run `h.Run("sessions", "list")`. Assert exit 0.
   Assert exactly two lines of stdout (header + 1 row, plus trailing
   newline). Assert the single data row's UUID matches the bootstrap
   entry's ID from `readRegistry`. Pins the empty/bootstrap-only AC.

9. **`TestSessionsList_E2E_NoDaemon`** — `RunBare(t, "sessions",
   "list", "-pyry-socket="+filepath.Join(t.TempDir(), "no-such.sock"))`.
   Assert non-zero exit, non-empty stderr, no `panic` / `goroutine ` /
   `runtime/` markers. Mirrors `TestSessionsNew_E2E_NoDaemon` exactly.

   Argv ordering note: `RunBare` does not auto-inject `-pyry-socket=`,
   so the test passes it explicitly. `runSessions`'s
   `parseClientFlags` consumes it before the sub-router dispatches.
   This is the pattern `TestSessionsNew_E2E_NoDaemon:147` already uses.

### Existing test edit: `internal/e2e/sessions_new_test.go`

10. **`TestSessionsNew_E2E_UnknownVerb` (line 113-139)** — currently
    fires `pyry sessions list` as the unknown verb (line 122). Once
    `list` lands, this test silently flips from "asserts unknown verb
    error" to "asserts unknown verb error and gets a successful list
    output instead, fails on `r.ExitCode != 0`". Change the verb to
    a still-unknown name (e.g. `bogus`) in the same edit:

    ```go
    r := h.Run(t, "sessions", "bogus")
    ```
    
    …and update the two `bytes.Contains` assertions for `"list"` →
    `"bogus"`. One-line conceptual change, three lines of edit.

### Race / vet

- `go test -race ./...` — handled by existing CI invocation. No new
  race surface (CLI is one synchronous process; the renderer
  functions are pure).
- `go vet ./...` — clean. Pin AC#5.

### What's deliberately out of scope for tests

- **No test that exercises `Pool.List` failure surfacing.** `Pool.List`
  cannot fail per #60. The seam's nil-sessioner branch is exercised
  by `internal/control` tests in #87.
- **No timezone-sensitive test on RFC3339 formatting.** `time.RFC3339`
  uses the `time.Time`'s location verbatim; the wire roundtrip
  produces UTC. Pin via assertion that the printed string contains
  `"T"` and ends with `"Z"` if you want belt-and-suspenders, but the
  golden-table test's exact-string match already covers it.
- **No `text/tabwriter` padding-correctness test.** Stdlib invariant;
  testing it adds nothing.

## Open questions

None. Sort policy, column set, RFC3339 vs Nano, JSON envelope shape,
exit-code policy, and timeout duration are all locked by AC,
precedent, or established convention. If table padding ergonomics
need tweaking after operator feedback (Phase 1.1c/d), revisit then,
not preemptively.

## Out of scope

- The control-plane wire (`sessions.list` verb, `Lister` seam, server
  handler, client wrapper) — that's #87.
- `Pool.List` itself — delivered by #60.
- `pyry sessions rename` (Phase 1.1c) — already shipped.
- `pyry sessions rm` (Phase 1.1d) — already shipped.
- `pyry attach <id>` refactor (Phase 1.1e).
- Live/streaming updates — the table is a snapshot; user re-runs to
  refresh.
- Filtering / sorting flags beyond the default last-active descending.
- Coloured output, locale-aware time formatting, paging.

## Documentation

After the developer ticket lands, update:

- `docs/PROJECT-MEMORY.md` — add a "Phase 1.1b-B2 (#88)" entry under
  Phase 1.1 control-plane work, mirroring the rename/rm/new entries'
  shape (verb name, file layout, test diff size, key design decisions
  — column set, JSON envelope, RFC3339 in the table).
- `docs/knowledge/features/control-plane.md` § "Sessions: list seam"
  (added by #87) — append a short subsection documenting the CLI
  renderer policy: column set, padding=2, RFC3339 in the table,
  `{"sessions":[...]}` JSON envelope, sort by LastActive desc with
  ID asc tiebreak.

After editing, run `qmd update && qmd embed` (per CLAUDE.md).
