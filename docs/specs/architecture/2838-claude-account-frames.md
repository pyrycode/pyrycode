# #2838 — Claude account source status frames

## Files read

- `internal/protocol/host_system_prompt.go` → the #2767 precedent: consumer contract in a package comment, empty request struct, reply struct with always-serialized strings.
- `internal/protocol/host_system_prompt_test.go` → `TestHostSystemPromptPayloads_RoundTrip` / `_WireKeys`: the fixture round-trip and constructed-key-set test shapes to copy.
- `internal/protocol/codes.go` → the host prompt `Type*` block: the new pair is declared beside it.
- `internal/protocol/envelope.go` → `inboundAppTypeSet`: gains `TypeRequestClaudeAccount` only.
- `internal/protocol/compat_test.go` → `TestIsKnownAppType`, `TestInboundAppTypeSet_CoversAllExportedTypeConstants` (length 30 → 31), `v2OnlyTypes`, `TestTypeConstants_V1V2Partition`: each enumerates type constants and needs the new pair.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes` and `TestEveryInboundV2TypeHasHandler`: every `Type*` constant must be classified; the request is "pending handler (#2839)", the reply is "reply".
- `cmd/pyry/claude_account.go` → `claudeAccount.status()`: internal vocabulary (`not-configured`, empty kind) differs from the wire; not touched here, mapping belongs to #2839.

No other feature branch touches these files.

## Change

Add a new payload file `internal/protocol/claude_account.go` declaring `RequestClaudeAccountPayload struct{}` (encodes as `{}`) and `ClaudeAccountPayload{Kind, Label, State, Reason string}` with JSON keys `kind`, `label`, `state`, `reason` and no `omitempty`, so every key is always present and absent label/reason are `""`. The file also declares the wire vocabulary as untyped string constants (`ClaudeAccountKindMachineLogin`, `…File`, `…OnePassword` = `1password`, `…OSKeychain`; `ClaudeAccountStateReady`, `…Failed`, `…NotConfigured`) so #2839 maps to named values. The package comment states the consumer contract: authenticated paired-client map dispatch, no conversation lookup, no `interactive` gate; exactly one unicast `claude_account` per request correlated by `Envelope.InReplyTo`, never broadcast; clients refresh by asking again and must accept an unknown `kind`; no key carries the token, source path or a secret reference.

`codes.go` gains `TypeRequestClaudeAccount = "request_claude_account"` and `TypeClaudeAccount = "claude_account"` beside the host prompt family. `inboundAppTypeSet` admits only the request; the reply stays out, so `IsKnownAppType` rejects it inbound. No error code is added: a read failure is reported in-band as `state: failed`, and a malformed request uses the existing `CodeProtocolMalformed`.

## Testing strategy

- `claude_account_test.go`: fixture round-trip over `request_claude_account.json` (`{}`, no `in_reply_to`) and three replies, each with `in_reply_to` — `claude_account_machine_login.json` (`machine_login`/`not_configured`, empty label and reason), `claude_account_file_ready.json` (`file`/`ready` with label), `claude_account_file_failed.json` (`file`/`failed` with reason). A key-presence assertion decodes each payload into `map[string]any` and checks the exact key set, so the empty strings must be present on the wire. A constructed-value wire-key test pins the key sets independently of fixtures (zero reply serializes all four keys as `""`), and an unknown `kind` decodes without error.
- `compat_test.go`: request added to the known/inbound lists, reply added to `v2OnlyTypes` and the v2 partition list, and a `claude_account-rejected` case in `TestIsKnownAppType`.
- `relay_guard_test.go`: `excludedTypes` classification keeps `TestEveryInboundV2TypeHasHandler` green.

## Documentation handoff

Pending for the documentation stage: in `docs/protocol-mobile.md`, add a "Claude account source" section beside "Daemon-wide host system prompt", plus rows in the application-message table — frame names and directions, the payload table, the `kind` (`machine_login`, `file`, `1password`, `os_keychain`; unknown values accepted) and `state` (`ready`, `failed`, `not_configured`) vocabularies with meanings, the always-present empty-string rule for `label` and `reason`, correlated examples, paired-client access and unicast scope, and the statement that no frame carries the token, the source path or a 1Password reference. Mark handling as pending #2839.
