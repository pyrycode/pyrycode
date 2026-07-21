# Spec #1140 — fakeclaude stream-json mode + package unit test

**Size:** XS · **Security-sensitive:** no (test-only fake CLI, no production surface) · **Split from:** #1135

## Files to read first

- `internal/e2e/internal/fakeclaude/main.go:9-202` — the env-var mode preamble (package doc). The new mode gets a paragraph here in the same style. **Extract:** the exact doc-comment shape every mode follows ("optional; when set…", "Default off — when unset, fakeclaude is byte-identical…", "Mutually exclusive with…").
- `internal/e2e/internal/fakeclaude/main.go:225-241` — the `env*` const block + `mustEnv`. **Extract:** where the new `envStreamJSON` const lands.
- `internal/e2e/internal/fakeclaude/main.go:409-458` — the top of `main()`: the `mustEnv(envSessionsDir/…)` calls and the mode-gate wiring. **Extract:** the exact insertion point — the stream gate goes **above line 410** (`dir := mustEnv(envSessionsDir)`), so stream mode never touches the sessions-dir machinery (AC3).
- `internal/streamsup/envelope.go:20-68` — `userTurn`/`userTurnMessage`/`userTurnContentText` + `marshalTurnEnvelope`. **Extract:** the exact stdin envelope shape fakeclaude reads (`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"…"}]}}`). These are **unexported** — mirror the shape with a local struct, do not import.
- `internal/streamsup/parser.go:97-208` — `streamLine`/`streamMessage`/`streamBlock`, `consumeLine`, `emitAssistant`, `resultTurnEndReason`. **Extract:** the stdout line→event mapping the fake must satisfy: an `assistant` line with a `text` block → `TextChunk{MessageID, Text}`; a `result` line → `TurnEnd{Reason: resultTurnEndReason(subtype)}`, where `subtype:"success"` → `TurnEndReasonEndTurn`.
- `cmd/pyry/stream_turn_drain_test.go:100-116` — `assistantTextLine`, `resultLine`, `feedLines`. **Extract:** the *exact* stdout line literals the daemon side already asserts against — the fake must emit byte-compatible shapes:
  - assistant: `{"type":"assistant","message":{"id":<id>,"role":"assistant","content":[{"type":"text","text":<text>}]}}`
  - result: `{"type":"result","subtype":"success","session_id":<id>}`
- `internal/e2e/internal/fakeclaude/clear_detect_test.go` (whole file, ~48 lines) — the `*_detect_test.go` convention: `package main`, untagged (no `//go:build e2e`), `t.Parallel()`, table-driven. **Extract:** the test skeleton the new `stream_detect_test.go` follows.
- `internal/turnevent/event.go:30-70` — `TextChunk`, `TurnEnd`, `TurnEndReason`. **Extract:** the exact event shapes the unit-test asserts against.
- `docs/lessons.md:256` — "`Pool.Create` appends `--session-id`". **Extract:** confirmation that the fake must tolerate injected argv flags (it already does — see Design § argv).

## Context

`internal/e2e/internal/fakeclaude` is the test-only stand-in for the real `claude` CLI. Today it models only claude's **PTY/TUI** surface — every mode (`PYRY_FAKE_CLAUDE_TUI` / `_MODAL_TRIGGER` / `_TRUST_TRIGGER` / `_IDLE_TRIGGER` / `_ESC_ENDS_TURN` / `_CLEAR_ROTATES` / rotation trigger) drives tui-driver's screen-reading detection or grows an on-disk `<uuid>.jsonl` transcript.

The daemon's `internal/streamsup` runner (the `interactive_runner` stream-json path, already wired and unit-tested on the daemon side) spawns claude **headless over line-delimited stream-json** — typed user-turn envelopes on stdin, `assistant`/`result` lines on stdout, **no PTY and no transcript file** (`streamsup.Parser` deliberately opens/watches nothing; its only input is the bytes claude writes to stdout). There is no fakeclaude mode that speaks this wire, so the stream interactive runner has no fake to drive it end-to-end.

This ticket adds that mode: a new env-gated stream-json I/O model. It is **standalone-provable** by a package unit test — no daemon, relay, or harness. The harness helper + first `send_message` spec ride this mode in the sibling #1135 (blocked-by this ticket); the interrupt / new_session / queue / modal specs and the real-claude capstone ride that harness in turn.

## Design

All changes land in the existing `internal/e2e/internal/fakeclaude` package. **One production file modified** (`main.go`), **one test file created** (`stream_detect_test.go`). No new package, no new exported symbols (the package is `main`; new decode structs are unexported). No importers exist, so there is zero consumer fan-out.

### The wire contract (mirror, do not import)

fakeclaude stays zero-dependency (only `golang.org/x/term` + stdlib) — the same posture that keeps `interruptEndTurnLine` a hand-written literal rather than a turnbridge import. So the stream shapes are mirrored by hand:

**Inbound (stdin), one JSON object per line:**
```
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"…"}]}}
```
Only `{"type":"user",…}` lines produce a response. Any other line type (a `control_request` interrupt, a blank line) is read and ignored — one response per received **user** turn (AC2). Interrupt→`result` handling is **out of scope**; it rides the interrupt e2e sibling (see Open questions).

**Outbound (stdout), two lines per user turn:**
```
{"type":"assistant","message":{"id":"<id>","role":"assistant","content":[{"type":"text","text":"<text>"}]}}
{"type":"result","subtype":"success","session_id":"<id>"}
```
These are byte-compatible with `stream_turn_drain_test.go`'s `assistantTextLine` / `resultLine`, so `streamsup.Parser` maps them to `TextChunk{Text:"<text>"}` then `TurnEnd{Reason: TurnEndReasonEndTurn}`.

### New env constant

Add to the `const` block (`main.go:225-241`):

```
envStreamJSON = "PYRY_FAKE_CLAUDE_STREAM_JSON"
```

Set to any non-empty value to enable. Add a matching paragraph to the mode preamble doc block in the established style (optional; when set… ; Default off — byte-identical when unset; mutually exclusive with every existing mode).

### Mode gate (structural mutual exclusivity + AC3)

At the **very top** of `main()`, above `dir := mustEnv(envSessionsDir)` (`main.go:410`):

```go
if os.Getenv(envStreamJSON) != "" {
    runStreamJSON(os.Stdin, os.Stdout)
    return
}
```

Two properties fall out by construction:
- **AC1 byte-identical-when-unset:** an unset env var falls straight through to the existing code — nothing below changes.
- **AC1 mutual exclusivity + AC3 no-sessions-dir:** the `return` short-circuits before the three `mustEnv` calls, the stdin reader, the idle-glyph seed, and the poll loop. Stream mode therefore needs **no** `PYRY_FAKE_CLAUDE_SESSIONS_DIR` / `_INITIAL_UUID` / `_TRIGGER`, opens no transcript, and cannot run any PTY mode in the same process. Stream-json is checked **first**, so if an operator sets both flags stream wins and the other mode is inert — the same "one mode per process" contract the existing modes hold, enforced structurally rather than by a validation reject branch (no observed double-set failure to defend against; evidence-based fix selection).

### `runStreamJSON` — the read→emit loop (the testable seam)

Signature (takes `io.Reader`/`io.Writer`, so the unit test drives it against in-memory buffers — the technical note's "does not hard-wire `os.Stdout`"):

```go
// runStreamJSON reads line-delimited stream-json user-turn envelopes from r and,
// for each {"type":"user",…} line, writes one assistant text line + one result
// line to w. Returns on r EOF (the daemon closed the child's stdin). Non-user
// lines are read and ignored. Runs on a single goroutine — no shared state.
func runStreamJSON(r io.Reader, w io.Writer)
```

Behavior contract:
- Read one line at a time with `bufio.NewReader(r)` + `ReadString('\n')` (no `bufio.Scanner` token cap — a stream-json line carries a whole prompt; `ReadString` handles arbitrarily long lines and the EOF-terminated final line). Loop until a non-nil error (EOF or read error), handling any final non-newline-terminated bytes.
- Decode each line into a local inbound struct (below). On JSON error, or `Type != "user"`, skip the line (tolerate — mirrors the parser's per-line resilience).
- On a user line, extract the first `text` block's text (empty string if absent) and call `writeStreamResponse(w, <minted msgID>, <text>)`.
- Mint `msgID` as a per-turn monotonic counter (`m1`, `m2`, …) held in a local `int` — deterministic and distinct per turn (the loop is single-goroutine, so no race). `uuidV4()` would also work; the counter is preferred so a downstream e2e can assert on a stable id.

Local inbound decode struct (unexported, mirrors `streamsup.userTurn` — only the fields the fake reads):

```go
type inUserTurn struct {
    Type    string `json:"type"`
    Message struct {
        Content []struct {
            Type string `json:"type"`
            Text string `json:"text"`
        } `json:"content"`
    } `json:"message"`
}
```

### `writeStreamResponse` — the per-turn emit (pure over `io.Writer`)

```go
// writeStreamResponse writes one assistant text line followed by one result line
// to w — fakeclaude's canned response to a single user turn. Both lines are
// json.Marshal-encoded from local structs (never string-concatenated) so text is
// escaped and each object is exactly one physical line. Returns the first write /
// marshal error.
func writeStreamResponse(w io.Writer, msgID, text string) error
```

Contract:
- Marshal two local outbound structs (an `assistant`-typed message with one `text` content block carrying `text`; a `result`-typed line with `subtype:"success"` and a fixed `session_id`), each followed by `'\n'`, written to `w` in order.
- `text` is the **echoed** prompt from the inbound turn — a real round-trip the sibling `send_message` e2e can assert on (delta text == sent prompt). Echoing costs one extra field-read on the already-decoded struct; canned fixed text would still require decoding the top-level `type`, so echo is chosen for downstream value at negligible cost.
- `session_id` is a fixed literal (e.g. `"fake-stream"`). `streamsup.Parser.streamLine` never decodes `session_id`, so the value is cosmetic — do not thread `--session-id` through (that would force argv parsing the fake deliberately avoids).
- Use structured marshalling (not `fmt.Sprintf`/literals): the echoed `text` is caller-controlled bytes, so `json.Marshal` escaping keeps the output a single valid line regardless of prompt content.

Outbound struct sketch (unexported; field order/tags produce the byte-compatible shapes above):

```go
type outAssistant struct {
    Type    string          `json:"type"`    // "assistant"
    Message outAsstMessage  `json:"message"`
}
// outAsstMessage: ID string `json:"id"`; Role string `json:"role"`; Content []outTextBlock `json:"content"`
// outTextBlock:   Type string `json:"type"` ("text"); Text string `json:"text"`
type outResult struct {
    Type      string `json:"type"`       // "result"
    Subtype   string `json:"subtype"`    // "success"
    SessionID string `json:"session_id"` // fixed literal
}
```

### argv tolerance (AC3)

fakeclaude reads config from env only and **never inspects `os.Args`**. The daemon's stream spawn injects `--input-format stream-json --output-format stream-json --verbose --session-id <uuid> --resume` (plus `Pool.Create`'s appended `--session-id <uuid>`, `docs/lessons.md:256`). All are silently ignored by construction — no code required. Confirm the fake still parses zero flags after this change.

### Data flow

```
daemon (streamsup.Runner)                 fakeclaude (stream mode)
  stdin  pipe ── {"type":"user",…}\n ────►  bufio ReadString('\n') ► decode ► echo text
  stdout pipe ◄── {"type":"assistant",…}\n ◄─ writeStreamResponse(w, m1, text)
              ◄── {"type":"result",…}\n    ◄─┘
  streamsup.Parser ► TextChunk{Text} , TurnEnd{EndTurn}
```

### substrate-guard

Stream mode emits **pure JSON** — no TUI substrate glyphs (no U+276F/U+273B). It adds no new substrate to the file. `main.go` is already on the `cmd/substrate-guard` allowlist (file-level exemption for its TUI-mode glyphs); this change is orthogonal to that gate.

## Concurrency model

Single goroutine. `runStreamJSON` runs entirely on `main()`'s goroutine — read a line, respond, repeat. No stdin-reader goroutine, no poll loop, no shared atomics (the existing modes' `turnPending`/`escPending`/etc. are untouched and never reached in stream mode). No raw-mode: the daemon writes stream-json over a **pipe**, not a PTY, so canonical line discipline / CR mapping does not apply — do not call `enterRawMode`. `-race` is clean by construction (no concurrency).

## Error handling

Consistent with the fake's existing silent-error posture (the e2e asserts downstream, never on the write):
- Read loop exits on any `ReadString` error (EOF = daemon closed stdin during teardown = normal shutdown; return, let the process exit).
- A line that fails to decode, or is not `type:"user"`, is skipped (tolerated), mirroring `Parser.consumeLine`'s per-line resilience — one malformed line never wedges the loop.
- `writeStreamResponse` returns marshal/write errors; `runStreamJSON` may log-and-continue or return. Given the fake is a test tool over a controlled pipe, a write error means the daemon's read end is gone — treat like EOF (stop). Do not `os.Exit` mid-turn.
- Empty/absent inbound text → echo empty string; still emits a valid assistant+result pair (one response per user turn holds regardless of content).

## Testing strategy

New file `internal/e2e/internal/fakeclaude/stream_detect_test.go` — `package main`, **untagged** (no `//go:build e2e`, so plain `go test` exercises it), `t.Parallel()`, following the `*_detect_test.go` convention. It drives the read→emit seam against in-memory buffers and proves parser-compatibility against the **real parser** (belt-and-suspenders, different fabric: the fake's hand-mirrored output is checked by the actual `streamsup.Parser`, so a shape-mirroring bug the fake and a hand-written test-decoder would share is caught).

Scenarios (bullet-pointed; developer writes the assertions in-idiom):

- **Single turn round-trip (AC2 + AC4):** drive `runStreamJSON(strings.NewReader(<one user-turn line>+"\n"), &buf)`. Feed `buf.Bytes()` through `streamsup.NewParser(sink, nil)` (sink appends to a `[]turnevent.Event`). Assert exactly two events, in order: a `turnevent.TextChunk` whose `Text` equals the sent prompt (proves echo + assistant-line shape), then a `turnevent.TurnEnd` whose `Reason` is `turnevent.TurnEndReasonEndTurn` (proves result-line shape + subtype mapping).
- **Multiple turns (AC2 "one response per received turn"):** two user-turn lines in the reader → parser yields `TextChunk, TurnEnd, TextChunk, TurnEnd` in order, each text echoing its turn.
- **Non-user line ignored:** a `control_request` line (and a blank line) in the reader produces **no** output bytes for that line — parser yields nothing from it. Confirms interrupt handling is out of scope and stray lines can't fabricate a turn.
- **Distinct message ids:** across two turns the two assistant lines carry different `id` values (guards the per-turn counter; assert on the decoded envelope or via distinct `TextChunk.MessageID`).
- (Optional, stdlib-only) **`writeStreamResponse` shape unit:** call it directly against a `bytes.Buffer`, `json.Unmarshal` each of the two lines into a local mirror struct, assert `type`/`content[0].type`/`text`/`subtype` fields — a cheap direct check that does not need the parser.

The test imports `internal/streamsup` and `internal/turnevent`. This is a deliberate, sanctioned deviation from the fakeclaude-test stdlib-only norm: the **binary** stays zero-dependency (main.go imports nothing new), only the **test binary** links the parser — and running the real consumer is the most literal satisfaction of AC4 ("decode to the shapes `parser.go` maps to `TextChunk` and `TurnEnd`").

**Verify:** `go test -race ./internal/e2e/internal/fakeclaude/...`, `go vet ./...`, `go build -o /dev/null ./cmd/pyry`. Confirm the pre-existing detect tests (`clear`/`esc`/`modal`/`trust`) still pass (byte-identical-when-unset).

## Open questions

- **Interrupt / new_session / queue / modal in stream mode:** deliberately **out of scope**. This ticket responds only to user turns with `assistant`+`result(success)`. The interrupt e2e sibling (which needs a `control_request`→`result{subtype:"error_during_execution"}` → `TurnEndReasonCancelled` response) extends this mode later. The read loop already reads-and-ignores `control_request` lines, leaving a clean extension point (add a `case "control_request"` branch then).
- **`msgID` scheme:** spec recommends a monotonic `m1/m2/…` counter for determinism; `uuidV4()` (already in the file) is an acceptable alternative if the developer prefers uniqueness over stable ids. Either satisfies the AC (parser only requires a non-empty id).
