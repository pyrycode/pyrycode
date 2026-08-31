# Debug-bundle streaming payloads (#812)

The byte-generic wire bodies for streaming a large debug bundle over the encrypted
mobile channel (`docs/protocol-mobile.md` § Debug bundle; split from #803). A
content-bearing bundle (assembled by [`internal/debugbundle`](debugbundle-package.md), #811) routinely exceeds one 65535-byte AEAD frame, so the daemon streams it as
ordered, cap-respecting chunks ending in a completion marker. **Binary → phone
direction; wire vocabulary only** — the chunker, the streaming primitive
(`StreamBundle`), and the reassembly reference (`ReassembleBundle`) live in
[`internal/relay/v2bundlestream.go`](v2-session-manager.md#debug-bundle-streaming-812--streambundle--bundleenvelopes--reassemblebundle);
the request verb that drives a stream is sibling #813 — `TypeRequestDebugBundle =
"request_debug_bundle"`, an inbound phone → binary **bare control type with no
payload struct** (the bundle is daemon-global, so there is no field to carry;
mirrors `TypeInterrupt`), added in the same v2-only const block and registered in
the three `compat_test.go` drift-detector sites. See [codebase/813.md](../codebase/813.md).

```go
type DebugBundleChunkPayload struct {
    Seq  int    `json:"seq"`
    Data []byte `json:"data"`
}

type DebugBundleDonePayload struct {
    Total int `json:"total"`
}
```

- **`Seq` is 0-based, contiguous, ascending across a stream.** The receiver
  (`ReassembleBundle` / the phone) requires the next chunk's `Seq` to equal the
  count of chunks already seen, so a reorder, gap, or duplicate **fails cleanly**
  rather than corrupting output — the structural half of the two-net integrity
  contract (AEAD guarantees per-frame content integrity; `Seq`+`Total` add gap /
  reorder / truncation detection).
- **`Data []byte` auto-encodes as standard base64 via `encoding/json`** (the phone
  base64-decodes). It is **content-bearing bundle bytes — never logged** (AC#4);
  the base64 expansion (×4/3) is why `bundleChunkBytes` is set conservatively
  under the frame cap, not at it.
- **`DebugBundleDonePayload.Total` is the exact chunk count.** The receiver uses it
  to detect a truncated stream: a `done` whose `Total` ≠ the number of chunks
  actually received is a count-mismatch error, never accepted as complete. An empty
  blob is a valid stream — 0 chunks + `done{total:0}`, reassembling to empty.
- **Pure DTOs, no `omitempty`** (the queue/interactive-payload posture). Golden
  round-trips `TestDebugBundleChunkPayload_RoundTrip` / `TestDebugBundleDonePayload_RoundTrip`
  against `testdata/debug_bundle_chunk.json` / `debug_bundle_done.json`. Both
  `Type*` constants are registered in the `compat_test.go` drift detector
  (`v2OnlyTypes`, the partition `all` list, the `-rejected` cases) and are **not**
  in `inboundAppTypeSet` — an old phone must never receive these outbound events. See
  [codebase/812.md](../codebase/812.md).
