# #2515 — Add effective effort to session settings

## Files read

- `internal/protocol/settings.go` → `SessionSettingsPayload` — owns the existing saved-effort wire field and the full-report zero-value contract.
- `internal/protocol/settings_test.go` → `TestSessionSettingsPayload_RoundTrip`, `TestSessionSettingsPayload_ZeroFieldsPresent` — existing raw and zero-value coverage the additive field must preserve.
- `internal/relay/v2session_settings.go` → `handleRequestSessionSettings` — confirms the payload is constructed by value and that publication belongs to dependent ticket #2516.
- `docs/knowledge/features/protocol-package-types-session-settings-payloads.md` → “Session settings payloads” — records the owning package's presence-contract conventions.
- `docs/knowledge/features/development-verification.md` → “Protocol boundaries” — requires decoded-value assertions, distinct swap-detecting values, and raw JSON inspection for null versus omission.
- `internal/protocol/system_prompt.go` → `SystemPromptPayload` — demonstrates the nearest pointer-backed presence contract, while also showing why a plain pointer cannot preserve null separately from omission here.

## Context

`SessionSettingsPayload.Effort` is the saved per-session choice and must remain unchanged. Desktop also needs Claude's applied effort, but that observation has three wire states: a string, explicit `null`, or an omitted key. A plain `*string` collapses null and omission during decode, while `json.RawMessage` would stop enforcing the string-or-null vocabulary and would make the payload non-comparable for existing consumers.

This ticket declares only the additive protocol shape. Ticket #2516 will populate it.

## Design

Add a small comparable `NullableString` value type whose zero value means unavailable. `NewNullableString` marks either a string pointer or nil as present; `Value` returns the nullable value plus its presence bit. Its JSON methods accept and emit only a string or `null`, and `IsZero` lets Go's `omitzero` tag omit the unavailable state.

Add `SessionSettingsPayload.EffectiveEffort NullableString` with the `effective_effort` wire name and `omitzero`. Keep every existing field and tag unchanged. This preserves comparability of `SessionSettingsPayload` and avoids consumer edits in this wire-only slice.

Wire flow:

```text
zero NullableString       -> key omitted
NewNullableString(nil)    -> "effective_effort": null
NewNullableString(&value) -> "effective_effort": "<value>"
```

## Concurrency model

The protocol type is immutable by convention and has no goroutines, locks, I/O, or shared state.

## Error handling

`NullableString.UnmarshalJSON` returns the standard JSON type error, wrapped with field-type context, for any non-string, non-null value. It updates the receiver only after a successful decode. Marshaling delegates the nullable pointer to `encoding/json`; valid constructed states therefore cannot fail in practice, while the error remains propagated normally.

## Testing strategy

- Add a table-driven raw-JSON test for string, explicit null, and omitted states. Each row first marshals a constructed payload, then decodes and re-marshals it byte-for-byte.
- Use different saved and effective effort strings so swapping `effort` and `effective_effort` fails.
- Assert the decoded nullable state directly, not only the re-marshaled bytes.
- Extend the existing zero-value assertion to require `effective_effort` omission while retaining every existing key and zero value.

## Documentation handoff

Pending for the documentation stage: update `docs/protocol-mobile.md` under “Session settings” with the exact string/null/omitted semantics for `effective_effort`, and state that `effort` remains the saved choice.

## Open questions

None.
