# Claude account source status payloads (#2838, #2839)

Wire vocabulary only, in `internal/protocol/claude_account.go` — the request a
paired client sends to ask which Claude account source the daemon uses, and
the daemon's always-four-key reply. The payload types shipped in #2838 with no
handler and no mapping from `cmd/pyry/claude_account.go`'s internal
vocabulary; #2839 wired both — `handlers.RequestClaudeAccount` and
`(*claudeAccount).ClaudeAccount()`, see [claude-account-source.md](claude-account-source.md).
Published contract: [`docs/protocol-mobile.md` § Claude account
source](../../protocol-mobile.md#claude-account-source).

```go
type RequestClaudeAccountPayload struct{}

type ClaudeAccountPayload struct {
    Kind   string `json:"kind"`
    Label  string `json:"label"`
    State  string `json:"state"`
    Reason string `json:"reason"`
}
```

`Kind` is one of `machine_login`, `file`, `1password` or `os_keychain` (a
client must accept an unknown value — `os_keychain` itself has no daemon
source yet, #2815, and is declared so clients can be built against it before
one exists); `State` is `ready`, `failed` or `not_configured`. No `omitempty`
anywhere: all four keys of `ClaudeAccountPayload` always serialize, `label`
and `reason` as `""` when absent, which is what the committed
`claude_account_machine_login.json` / `_file_ready.json` / `_file_failed.json`
fixtures and a constructed-value `WireKeys` test both pin independently.

`TypeRequestClaudeAccount = "request_claude_account"` /
`TypeClaudeAccount = "claude_account"`, filed in `v2OnlyTypes` and the
partition test's `all` slice;
`inboundTypes["TypeRequestClaudeAccount"] = "map-dispatched"` (moved there by
\#2839, once `handlers.RequestClaudeAccount` gave it a live
`startRelayV2` map entry — the same move [Drift
detectors](protocol-package-drift-detectors.md) describes for
`TypeAttachmentChunk`), `excludedTypes["TypeClaudeAccount"] = "reply"`. Only
the request joins `inboundAppTypeSet` — `IsKnownAppType` accepts
`request_claude_account` inbound and rejects `claude_account` — the same
shape the [host system prompt pair](protocol-package-types-system-prompt-payloads.md)
and the [pairing pair](protocol-package-types-pairing-payloads.md) use. See
[Drift detectors](protocol-package-drift-detectors.md) for what actually
reddens the build on an unpartitioned constant.

## A read failure doesn't need its own error code, and the test for when one does

\#2767's `host_system_prompt` pair minted `CodeHostSystemPromptUnavailable` for
exactly one reason: its *write* half persists to disk, and a storage failure
has nothing else to report through. This pair has no write half —
`claude_account` only ever projects a snapshot
`cmd/pyry/claude_account.go`'s accessor already holds — so a failed source
read has somewhere to go that already exists on the wire: `state: "failed"`
with the daemon's own short `reason`, the same state a client renders for any
other unready account. Minting a retryable code for it would say "ask again
and this might resolve," which is false for a read that already ran and
recorded its outcome; the daemon re-reads on `prime` and on each spawn
attempt, not on request. The rule this leaves for the next declare-ahead pair:
a new error code earns its place only when a verb has a side effect that can
fail with nothing else on the wire to say so (storage, a subprocess, a network
call) — a pure read that can only ever report "degraded" through a field the
payload already carries doesn't need one. #2839's handler confirmed it: it
replies with the payload's own `state`/`reason` on a failed read and mints
nothing new.

## The wire vocabulary and the daemon's internal vocabulary are deliberately not the same strings

`cmd/pyry/claude_account.go`'s `claudeAccount.status()` reports its own
internal state strings — `not-configured` (hyphen, not underscore) and an
**empty** `Kind` when no source is configured — not `not_configured` and
`machine_login`. #2838 declared the two vocabularies to differ on purpose; the
mapping (empty → `machine_login`, `not-configured` → `not_configured`) lives
in `cmd/pyry/claude_account.go`'s `claudeAccountPayload`, called from
`(*claudeAccount).ClaudeAccount()` (#2839) — `internal/protocol` itself never
computes either string. A reader who greps `internal/protocol` for
`"machine_login"` or `"not_configured"` and finds only the constant
declarations, never a conversion, is not looking at a bug — the wire strings
are a client-facing contract chosen independently of the daemon's internal
state names, which is also why `os_keychain` exists on the wire with no
producer behind it yet (#2815).

## Related

- [System-prompt payload boundaries](protocol-package-types-system-prompt-payloads.md) —
  the `host_system_prompt` precedent this pair's consumer contract
  (paired-client map dispatch, no `interactive` gate, one unicast reply) and
  its error-code decision are both measured against.
- [Pairing request/reply payloads](protocol-package-types-pairing-payloads.md) —
  the nearest sibling declared the same way, ahead of its own handler.
- [claude-account-source.md](claude-account-source.md) —
  `cmd/pyry/claude_account.go`, the per-instance accessor this pair's reply
  projects, its internal state vocabulary, the operator `label` and the read
  ordering guarantee #2839 added.
- [Drift detectors](protocol-package-drift-detectors.md) — the classifier
  registries this pair's constants move through.
