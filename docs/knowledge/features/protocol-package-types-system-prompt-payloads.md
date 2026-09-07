# Conversation system-prompt read payloads (#2152)

Wire vocabulary only, in a new `internal/protocol/system_prompt.go` — the read
half of the #2151 cluster, which shipped write-only. Its own file rather than a
home in `conversations_write.go` beside `SetSystemPromptPayload`: that file
carries payloads that mutate the conversation *record*, and this pair is a read
of the prompt itself, with its own reply type and its own verdict vocabulary —
`settings.go`, `history.go` and `snapshot.go` each own a frame family for the
same reason. Published contract:
[`docs/protocol-mobile.md` § Reading a conversation's system prompt](../../protocol-mobile.md#reading-a-conversations-system-prompt-v2).

```go
type RequestSystemPromptPayload struct {
    ConversationID string `json:"conversation_id"` // no omitempty
}

const (
    SystemPromptStatusNoSession = "no_session"
    SystemPromptStatusMatches   = "matches"
    SystemPromptStatusDiffers   = "differs"
)

type SystemPromptPayload struct {
    SystemPrompt        *string `json:"system_prompt,omitempty"`
    SessionPromptStatus string  `json:"session_prompt_status"` // no omitempty, never ""
}
```

`TypeRequestSystemPrompt = "request_system_prompt"` / `TypeSystemPrompt =
"system_prompt"`, filed in `v2OnlyTypes` and the partition test's `all` slice;
neither joins `inboundAppTypeSet` (both are v2, intercepted before
`dispatch.Route`). `cmd/pyry/relay_guard_test.go` files the request
`switch-intercepted` and the reply `reply` — both from the ticket that adds the
constant, since the handler lands in the same commit and there is no
"pending handler" window the way `TypeRequestHistory` had one.

## The tri-state is a pointer-with-`omitempty` pairing, and the test for it has to assert bytes

`SystemPrompt *string` with `omitempty` mirrors
[`conversations.Conversation.SystemPrompt`](conversations-registry.md)'s storage
encoding field for field: `omitempty` on a pointer tests the pointer, not the
pointee, so `nil` omits the wire key while a non-nil pointer to `""` still emits
`"system_prompt": ""`. That is what keeps the registry's three states —
no prompt, explicitly empty, text — distinguishable on the wire, which is what
lets a client read a value and write it straight back through
[`SetSystemPromptPayload`](protocol-package-types-conversations-write-payloads.md)
without collapsing "explicitly empty" into "no prompt" on the round trip.

A decoded `nil` pointer looks identical whether the source bytes omitted the key
or carried an explicit `null` — a struct-level equality assertion cannot see the
difference a `SystemPrompt *string` is supposed to preserve. Only the
*marshalled bytes* separate "no prompt" from "an explicitly empty prompt";
`TestSystemPromptPayload_TriStateSurvivesARoundTrip` asserts the encoded output
for each of the three states rather than comparing decoded structs. Generalises
past this pair: any tri-state field of this shape (a `*string` distinguishing
absent from explicit-zero) needs its round-trip test to check bytes, not
just a decode.

## `SessionPromptStatus` is a closed three-value enum, and its two fields are independent

Never `""` — the handler always sets one of the three constants, including on
every unresolvable path (see
[Inbound `request_system_prompt`](v2-session-manager-state-machine-inbound-request-system-prompt-systempromptfor-seam.md)
for the reply's constant-shape posture). A client switches on three cases and
has no fourth to guess at.

`SystemPrompt` and `SessionPromptStatus` must not be read as if one implies the
other. A conversation storing text while nothing runs reports the text
alongside `no_session`; a conversation storing nothing while a session runs
that was spawned with no operator text reports an absent key alongside
`matches` — because `Pool.SystemPromptFor` collapses both no-bytes states to
`""` by design, so the comparison the daemon makes runs on the *collapsed*
stored value, not on whether a pointer is nil. See the seam doc linked above
for the full eight-row table and the mutation test that pins it.

The spawned-with text itself is never carried — `differs` says the two
disagree and stops there, rather than echoing up to another 8192 bytes of
operator text back over the wire to prove it. A client wanting a diff is a
later ticket, and the reply's `conversation_id`-less shape (correlation rides
`InReplyTo`, matching `SessionSettingsPayload`) is what lets an unhosted
conversation's answer be byte-identical to a hosted-but-quiet one.

## Related

- [Inbound `request_system_prompt`](v2-session-manager-state-machine-inbound-request-system-prompt-systempromptfor-seam.md) — the handler that decodes/answers this pair, the collapse rule, and the log-discipline lesson.
- [Conversations-write payloads](protocol-package-types-conversations-write-payloads.md) — `SetSystemPromptPayload` (#2151), the write half sharing the same tri-state encoding.
- [`conversations-registry.md`](conversations-registry.md) — `Conversation.SystemPrompt`, the on-disk tri-state this wire pair mirrors.
