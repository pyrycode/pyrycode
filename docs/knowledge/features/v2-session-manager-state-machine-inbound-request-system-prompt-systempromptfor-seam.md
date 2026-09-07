# Inbound `request_system_prompt` (#2152) — `SystemPromptFor` seam

`request_system_prompt` is a v2 **control** envelope (phone → binary),
intercepted in `dispatchAppFrame`'s discriminator switch **before**
`dispatch.Route`, and answered **inline on the `Run` dispatch goroutine** —
[`request_session_settings`](v2-session-manager-state-machine-inbound-request-session-settings-the-rea.md)'s
shape, not
[`request_history`](v2-session-manager-state-machine-inbound-request-history-historypager-seam.md)'s
off-`Run` worker handoff. It is the read half of the #2151 cluster, which
shipped write-only: a client that did not itself perform the write had no way
to learn what a conversation's stored prompt holds, and no way at all to learn
that the child it is typing at predates an edit — because a stored prompt
takes effect only at the conversation's *next* session start.

**Its own write half is deliberately not its layout template.**
`internal/relay/handlers/set_system_prompt.go` is a `dispatch.Route` handler
with no capability gate, and says so in its own words — `V2Session.interactive`
is read only in the outbound `ActiveConns` fan-out there. This verb needs the
interactive capability to be an inbound gate (AC #3's inertness), which only
exists on the interception path, where `handleRequestSessionSettings` and
`handleRequestModelList` both open with `if !s.interactive { return }`.
Copying the sibling's file layout — the obvious thing to pattern-match, being
the adjacent ticket number — would have dropped the gate.

## Order is the design

1. **Capability gate, fully inert.** `if !s.interactive { return }` — no
   decode, no seam call, no reply.
2. **Decode, tolerated.** `_ = json.Unmarshal(env.Payload, &p)` leaves
   `ConversationID == ""` on failure, which resolves nothing at step 3. Safe
   here because the id reaches a registry lookup and nothing else — it never
   becomes a path component the way `request_history`'s does, where the same
   tolerance would resolve an empty id to the log root.
3. **Resolve, or don't.** Empty-id guard, nil-seam guard, comma-ok honoured —
   `payload` is overwritten only on `ok == true`.
4. **No `KnownConversation` call, and that is a decision, not an omission.**
   `request_model_list` needs it to separate two refusal codes
   (`conversation.not_found` vs. `model_list.unavailable`). This verb has no
   error-frame path at all — every unresolvable case gets the same constant
   `no_session` reply — so a membership check could only split an answer AC #3
   requires merged: an unhosted conversation must be indistinguishable from a
   hosted one holding no prompt and running nothing, or the verb becomes a
   membership oracle.
5. **Emit.** One `system_prompt` envelope, `InReplyTo` set, `EventID` nil.

**Mints no wire code, has no error-frame path.** Every neighbour that
mints one does so because an empty success value could pass for "unknown"
(`model_list.unavailable` exists because an empty `models` array can't stand
in for "no menu"). Here every unresolvable case already has a truthful
constant answer, so there is nothing for a code to distinguish.

## The collapse trap — the comparison runs on the collapsed stored value

`Pool.SystemPromptFor` returns `""` for **both** of the registry's no-bytes
states by design (composing the daemon's constant prompt with the operator's
is the sessions package's business), while `conversations.Conversation.SystemPrompt`
is a tri-state `*string` — nil, non-nil `""`, or text. Since #2151 landed, a
client can mint all three. `cmd/pyry/relay.go`'s `systemPromptStatus` computes
the verdict on the **collapsed** stored value, not on whether the pointer is
nil:

| `stored` | `spawnedWith` | `session_prompt_status` |
|---|---|---|
| any | `nil` | `no_session` |
| `nil` | `&""` | `matches` |
| `&""` | `&""` | `matches` — the trap |
| `&"x"` | `&"x"` | `matches` |
| `nil` | `&"x"` | `differs` |
| `&""` | `&"x"` | `differs` |
| `&"x"` | `&""` | `differs` |
| `&"x"` | `&"y"` | `differs` |

Comparing `st.stored != nil` as a proxy for "has bytes" instead of collapsing
first reports a conversation storing an explicitly empty prompt, whose session
spawned with no operator text, as *differing* — an operator would be told a
session is stale that is running exactly what they stored. Mutation-checked in
tree and reverted: breaking the collapse reddened exactly one row,
`TestSystemPromptStatus_ComparesTheCollapsedStoredValue/an EXPLICITLY EMPTY
prompt, session spawned with none: the collapse`, and nothing else — that row
is what carries the rule rather than riding along beside it.

## Two independently-resolvable halves must not be merged before the wire does

The registry half (is this conversation hosted, what does it store) and the
live-session half (is a session running, what was it spawned with) fail for
different reasons — no conversation vs. no session — and `resolveConversationPrompt`
(`cmd/pyry/main.go`) keeps them as two fields of one struct rather than
collapsing either into the other's failure. The wire reply merges every
unresolvable case into `no_session`, but the *seam* deliberately does not:
"hosted, holding text, running nothing" is a **successful** resolution whose
verdict is `no_session` and whose stored value still travels — exactly the
conversation an operator has configured and not yet started. A producer that
folded both halves into one comma-ok at the seam boundary (the way
`KnownConversation` answers one question) would have suppressed the stored
value in the one case it is most wanted.

## Log discipline — checked against what the shared emit helper's errors can quote, not just what the handler names

Every neighbouring handler logs `"err", err` on the `forwardEnvelope` push-drop
branch. `forwardEnvelope` returns two static sentinels and three wrapped
errors, one of them from a `json.Marshal` of the *whole envelope* — an error
string that can quote payload bytes, which on this path are the operator's
prompt. That marshal is unreachable for a payload this handler produced
itself, so the neighbours are not wrong; but AC #4 says the prompt reaches no
log on *any* path, and an argument from unreachability is weaker than the
property actually being true. This handler drops the `err` attribute entirely
on that branch — structural rather than argued. **A "never log X" acceptance
criterion has to be checked against the error values a shared emit helper
returns, not just the attributes the handler itself names** — worth doing
wherever a payload carries user text rather than daemon-authored ids, since
that is exactly the shape where a generic wrapped-error string becomes a
leak.

**Known test gap, left open at verifier PASS (non-blocking).** AC #4 has no
deterministic test — every `internal/relay` test uses a silent logger, so the
only thing enforcing "the prompt reaches no log" is the prose comment on the
push-drop branch (which itself records that every sibling handler logs `err`
there). Sibling #2151's `set_system_prompt_test.go` shipped a never-log table
with a non-vacuity witness for the identical rule; this verb does not yet have
its own. Worth porting the pattern the next time this file is touched, rather
than trusting the comment to hold across an edit that doesn't re-read it.

## The seam

**`SystemPromptFor func(conversationID string) (protocol.SystemPromptPayload, bool)`**
on `V2SessionConfig` — `RunConfigFor`'s shape: a string in, a marshal-ready
payload and a comma-ok out, so `internal/relay` imports neither
`internal/sessions` nor `internal/conversations`. Comma-ok, pinned by a
poisoned refusal double so fail-closed is a tested property of `internal/relay`
itself: rewriting the handler's resolve to `payload, _ = …` (discarding the
comma-ok) reddened exactly the two rows built for that purpose — a request
naming an unhosted conversation, and the unhosted-vs-quiet indistinguishability
row — while a zero-valued refusal double would have let both pass.

`cmd/pyry/relay.go`'s `systemPromptFor` composes `main.go`'s
`resolveConversationPrompt` (registry `Get` + `Pool.SystemPromptFor` over the
resolved `conv.CurrentSessionID` — never the caller's own conversation id, the
same #678-hazard-closing-by-construction `RunConfigFor` already relies on)
into the primitive-typed seam. `resolve == nil ⇒ nil`, decided at build time.

**No structural wiring guard exists for this seam's assignment.**
`request_model_list`'s `TestModelListForWiredToTheCompositionRoot` uses
`configSeamSelector` to pin `ModelListFor: w.modelListFor` in the
`V2SessionConfig` literal — but that helper only reaches a `Field: w.field`
selector, and `SystemPromptFor: systemPrompt` (`systemPrompt :=
systemPromptFor(w.promptState)` computed above the literal, then assigned by
plain identifier) doesn't match that shape. A seam whose resolution half is
`cmd/pyry`-typed and composed in `relay.go` still lands as a bare identifier in
the literal, same as `RunConfigFor: runConfig`, and neither has a guard. If a
future edit generalises `configSeamSelector` to catch a `field := expr(...)`
assignment followed by `Field: field`, this seam and `RunConfigFor` are both
still unguarded and worth including.

## Concurrency

Registry read and pool read are separately locked, acquired sequentially and
never nested — a `/clear` rotation or idle eviction can land between them. The
reply then describes the stored value as of the first acquisition and the
running session as of the second; worst case is a status one rotation stale,
correctable by the client repeating the request. `RunConfigFor`'s
single-acquisition argument doesn't transfer here — there the two values were
fields of *one session*, not of a registry entry and a pool entry.

## Related

- [`SystemPromptPayload` / `RequestSystemPromptPayload`](protocol-package-types-system-prompt-payloads.md) — the wire vocabulary this handler decodes and answers, including the tri-state round-trip byte-assertion lesson.
- [Inbound `request_session_settings`](v2-session-manager-state-machine-inbound-request-session-settings-the-rea.md) — the dispatch/capability shape this ticket copies (inline on `Run`, `!s.interactive` first, tolerated decode, `forwardEnvelope`, constant-reply-never-error posture).
- [Inbound `request_model_list`](v2-session-manager-state-machine-inbound-request-model-list-modellistfor-seam.md) — the `KnownConversation`-consulting sibling this verb deliberately diverges from, and the `configSeamSelector` wiring guard this seam falls outside of.
- [`writeSystemPrompt` + `systemPromptText`](sessions-package-key-types-writesystemprompt-systemprompttext.md) — `Pool.SystemPromptFor`, the session-keyed accessor #2150 built for this ticket, and why it collapses both no-bytes states to `""`.
- [`conversations-registry.md`](conversations-registry.md) — `Conversation.SystemPrompt`, the tri-state this verb reads and reports unmodified.
