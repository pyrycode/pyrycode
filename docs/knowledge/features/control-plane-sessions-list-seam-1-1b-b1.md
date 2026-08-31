# Sessions: list seam (1.1b-B1)

The fourth `sessions.*` verb is `sessions.list` — the first read-side member of the namespace. The control server consumes session-snapshot reads through the `Lister` interface in `internal/control`, embedded into `Sessioner` alongside `Remover` and `Renamer`:

```go
type Lister interface {
    List() []sessions.SessionInfo
}

type Sessioner interface {
    Create(ctx context.Context, label string) (sessions.SessionID, error)
    Remover
    Renamer
    Lister
}
```

`*sessions.Pool` satisfies `Lister` directly via `Pool.List` (#60). `Lister` does not take a context — `Pool.List`'s signature is `() []SessionInfo` and the operation is bounded by `Pool.mu` (RLock) + each `Session.lcMu` briefly, so the seam mirrors that shape adapter-free. `NewServer`'s signature is unchanged; the rationale documented under "Sessions: removal seam" applies identically.

### Wire shape

The verb carries no request payload — `Request.Sessions` stays nil for `sessions.list`, and `SessionsPayload` gains no new field. Response carries a new omitempty payload:

```go
type Response struct {
    Status       *StatusPayload       `json:"status,omitempty"`
    Logs         *LogsPayload         `json:"logs,omitempty"`
    SessionsNew  *SessionsNewResult   `json:"sessionsNew,omitempty"`
    SessionsList *SessionsListPayload `json:"sessionsList,omitempty"` // 1.1b-B1
    OK           bool                 `json:"ok,omitempty"`
    Error        string               `json:"error,omitempty"`
    ErrorCode    ErrorCode            `json:"errorCode,omitempty"`
}

type SessionsListPayload struct {
    Sessions []SessionInfo `json:"sessions"`
}

type SessionInfo struct {
    ID         string    `json:"id"`
    Label      string    `json:"label"`
    State      string    `json:"state"`       // "active" | "evicted"
    LastActive time.Time `json:"last_active"` // RFC3339Nano on the wire
    Bootstrap  bool      `json:"bootstrap,omitempty"`
}
```

`SessionInfo` is defined in `protocol.go` rather than reusing `sessions.SessionInfo` directly so external Go callers / future hand-written wire clients don't transitively import `internal/sessions` for the package-private `lifecycleState` enum.

### Encoding choices

- **`State` is a self-documenting string**, not an int. The two values `"active"` and `"evicted"` are exactly what `lifecycleState.String()` produces — the same encoding the on-disk registry uses, so the wire and the registry agree on token spelling without a parallel translation table. Same convention as `JSONLPolicy` (see `lessons.md` § "Wire enums: prefer self-documenting strings").
- **`LastActive` is `time.Time`**, not a pre-formatted string. `encoding/json` marshals `time.Time` to RFC3339Nano (jq-debuggable, byte-stable), and the typed value lets 61-B's renderer compute relative durations ("3m ago") without re-parsing. Note: JSON roundtrip strips the monotonic-clock component — comparisons after a roundtrip must use `time.Equal`, not `==` or `reflect.DeepEqual` (see `lessons.md` § "JSON roundtrip strips monotonic-clock state").
- **`Bootstrap` carries omitempty**, eliding the discriminator on every non-bootstrap entry.

### Translation site

The internal `sessions.SessionInfo` (with the package-private `lifecycleState` enum) is converted to the wire-level `control.SessionInfo` (with the public `string` state) inside `handleSessionsList`, the one handler — not at the renderer. The CLI consumer in 61-B receives wire-level types and does not import `internal/sessions`.

### No typed sentinels

`Pool.List` does not return errors. The seam's only failure path is the nil-sessioner branch ("sessions.list: no sessioner configured"); `ErrorCode` stays empty for this verb. `SessionsList` (the client wrapper) treats a nil `Response.SessionsList` on a non-error response as a daemon contract violation and returns `"control: empty sessions.list response"`. An explicit zero-length slice (`{"sessionsList":{"sessions":[]}}`) decodes to a non-nil payload with `len(Sessions) == 0` and surfaces as a well-formed empty result.

### Sort order

The seam returns whatever order `Pool.List` returned (LastActiveAt descending, SessionID ascending tiebreak). Final user-facing ordering is the responsibility of the CLI renderer (61-B); this layer does not re-sort.

See `docs/specs/architecture/87-control-sessions-list.md` for the full design.

### CLI renderer (1.1b-B2)

The operator-facing `pyry sessions list [--json]` verb consumes `control.SessionsList` and renders the snapshot in either of two formats. The renderer choices made here are the template the rest of Phase 1.1 follows for tabular output.

- **Columns.** `UUID`, `LABEL`, `STATE`, `LAST-ACTIVE`. UUIDs render in their full 36-character canonical form — no truncation. The "operators copy/paste UUIDs" property is load-bearing.
- **Padding.** Two spaces between columns via `text/tabwriter` (stdlib, no deps). Standard Go-CLI convention (`go list -m`, `go env`). Padding=1 cramps the table; padding=3+ wastes terminal width.
- **Time format.** `LAST-ACTIVE` renders as RFC3339 in the table. RFC3339Nano in `--json` (passed through verbatim from the wire's `time.Time`). Locale-aware "3m ago" formatting deferred — absolute timestamps are unambiguous across timezones and across log-paste-into-issue boundaries.
- **JSON envelope.** `{"sessions":[...]}`, intentionally not a bare array — leaves room for future top-level fields (e.g. `generated_at`, `schema_version`) without a breaking change. Per-element shape is `control.SessionInfo` JSON-encoded verbatim. Single trailing newline (`json.Encoder.Encode` default; what jq pipelines expect).
- **Sort policy.** The renderer re-sorts by `LastActive` desc with `ID` asc tiebreak (`sort.SliceStable` + `time.Time.Equal`). `Pool.List` already returns this order, but the renderer enforces it as a defence against future wire changes that would otherwise reshuffle every operator's table.
- **Bootstrap label.** The wire substitutes the bootstrap entry's empty on-disk label with `"bootstrap"` (per `Pool.List`'s contract); this layer renders verbatim. Empty `Label` for a non-bootstrap entry renders as the empty cell.
- **Timeout.** 5s, matching `runStatus`/`runLogs`. Diverges from rm/rename's 30s because list does not wait on `Pool.mu` against active sessions.

See `docs/specs/architecture/88-cli-sessions-list.md` for the full design.
