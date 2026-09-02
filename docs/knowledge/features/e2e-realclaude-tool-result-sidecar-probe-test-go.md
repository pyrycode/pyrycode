# tool_result_sidecar_probe_test.go

- `tool_result_sidecar_probe_test.go` (#2023) — **one live-claude probe,
  spawning through the production interactive stream-json shape
  (`streamsup.New` + `Runner.Run`, the recorder installed at
  `Config.Stdout`), that answers whether a `user`/`tool_result` line on
  claude's stdout carries the top-level sidecar the transcript calls
  `toolUseResult`.** Reuses `dropped_line_capture_test.go`'s (#1260)
  `dropcapRecorder`, `dropcapRedactor`, `dropcapScanner`, `dropcapJSONEscape`,
  `dropcapContains` and `newDropcapArgvHandler` wholesale; the only new logic
  is the retention predicate and the per-line sidecar inspection. The
  predicate is keyed on `type == "user"`, not #1260's drop-reason taxonomy —
  a `tool_result` line *emits* (`emitUser` in `internal/streamsup/parser.go`
  maps each block to a `turnevent.ToolUpdate`), so it was never a dropped
  line to begin with. Committed capture:
  `testdata/tool_result_sidecar_v2.1.239.json`.

  **The sidecar is on stdout, and stdout spells it in snake_case, not the
  transcript's camelCase.** The 33 transcript sidecars this ticket exists to
  cross-check all key the field `toolUseResult`. The first live run searched
  only that spelling and returned `sidecar-absent` — an answer that was
  literally true and materially wrong: the same retained line's own
  `line_keys` carried `tool_use_result`, holding the transcript's file key
  set verbatim. `sidecapInspect` was rewritten to search both spellings and
  record which one was seen (`tool_use_result_key` /
  `tool_use_result_spellings`, plus a record-level
  `sidecar_key_spelling_census`). The re-run: `sidecar-present`, 2 of 3
  retained `user` lines, spelling census `{"tool_use_result": 2}` — zero
  camelCase — and both key sets the transcript predicts reproduce
  byte-shape intact: `[file, type]` for the Read, `[interrupted, isImage,
  noOutputExpected, stderr, stdout]` for the Bash, one `tool_result` block
  per sidecar-bearing line (`tool_result_block_count_histogram: {"0": 1,
  "1": 2}`). **Any decoder reading claude's stdout for this sidecar keys on
  `tool_use_result`, not `toolUseResult`** — the transcript (`internal/agentrun/jsonl`
  fixtures) and the live stream-json stdout stream use different
  field-name conventions for the same payload.

  **When a probe's whole job is "does key K arrive", the surface's naming
  convention is part of the question, not an implementation detail.** A
  one-spelling instrument can produce an artifact that is internally
  consistent, a gate that is green and a budget that is spent, while
  reporting the exact inverse of the true answer — and it does so for the
  precise class of question ("is this dead code waiting to happen") the
  probe exists to settle. Search every spelling a surface might plausibly
  use, not just the one prior evidence (here, the transcript) happened to
  use.

  Follows `initialize_control_probe_test.go`'s (#1688/#1747) shape for the
  deny-scan applied-map completion: `newDropcapScanner`'s `addDynamicPath`
  drops the `artifact_dir` needle entirely for an empty path (`""` yields no
  spelling), so `dropcapScanner.applied()` carries no key at all for that
  class rather than `false`. `sidecapScanApplied` completes the map at this
  file's fill site — adding only the missing key, never touching an existing
  `true`/`false` — the same call #1747 made for #1688 rather than fixing the
  shared scanner, whose record shape is committed and shared with two other
  families.

  Zero production files touched. See
  `docs/specs/architecture/2023-tool-result-sidecar-stdout-capture.md` for
  the design, the retention-predicate rationale and the security review.
  This closes the observation #2023 exists to buy ahead of the downstream
  tool-row decoder: a shape-keyed dispatch written against a sidecar that
  never arrives on stdout would have been dead code, and no hermetic test
  built from hand-lifted transcript fixtures could have caught it.
