# Authenticated relay sender identity

## Files read

- `internal/relay/handlers/send_message.go` → `SendMessage`, `connSender`, `resolveAttachments`: acceptance gates, sender provenance and safe text/delivery separation.
- `internal/relay/handlers/send_message_test.go` → `fakeEnqueuer`, `fakeResetter`: legacy acceptance, rejection and reset fixtures.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit`: binding precedes the authenticated snapshot, which currently retains an empty first-bound key.
- `internal/relay/v2session_appframe.go` → `routeAppFrame`: passes the snapshot into `dispatch.NewConn`.
- `internal/relay/v2session_static_key_test.go` → `unboundFixture`, static-key refusal tests: real Noise handshakes and binding persistence.
- `internal/relay/v2session_modal_test.go` → `openModalConn`: encrypted connection fixture and synchronization on open.
- `internal/devices/registry.go` → `Validate`, `BindStaticKey`: authenticated pairing and atomic first-binding decision.
- `internal/msgqueue/queue.go` → `EnqueueIdentified`: existing positional identity API; accepts metadata without deduplication.
- `cmd/pyry/reply_suggestion.go` → `suggestionEnqueuer.EnqueueSent`: production wrapper adoption remains #2972.
- `docs/knowledge/features/relay-package.md`, `relay-package-handlers.md` § sender identity and tap time: a handler-only test cannot expose stale handshake snapshots.
- `docs/knowledge/features/v2-session-manager.md` § Security and log discipline: public identity keys also stay out of logs.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: verify safe text separately from delivery paths.
- `docs/knowledge/decisions/042-daemon-built-thread.md` § Item model, `docs/knowledge/features/history-package.md` § Producers: correlation requires device identity plus app message id.
- `docs/protocol-mobile.md` § Security model, `CODING-STYLE.md`: authenticated input boundaries and Go conventions.

## Context

Names may change or coincide, so they cannot correlate device-local app message ids. #2970 supplies the queue metadata seam; this ticket supplies its authenticated relay input. No new wire fields, history facts or deduplication are introduced. ADR 042 already records the decision.

## Design

Keep the exported `Enqueuer` interface unchanged. Add a consumer-side unexported `identifiedEnqueuer` interface matching `Queue.EnqueueIdentified`. At the existing acceptance point, choose that optional interface when supported; otherwise call `EnqueueSent`. Each send makes exactly one enqueue call, including a zero/backlog result.

`connSender` returns `Auth().StaticKey`, `Name` and the current admitted `ClientVersion`, or three empty strings when authentication is absent. Identity remains the existing lowercase-hex public key; there is no fallback and no client payload override. Both enqueue paths receive the same conversation id, verbatim app message id, safe text, composed delivery, resolved attachment ids and parsed tap time.

After `BindStaticKey` accepts a first binding, copy the authenticated peer's hex public key into the local device snapshot before it becomes `s.device`. A matched reconnect already has that key. Keep token, key and version refusal ordering and existing persistence unchanged.

No overlapping remote feature branches touch the planned production or existing static-key test files. Estimated total written work: 400 lines; zero new exported types, one changed internal caller, four acceptance criteria and one new optional-API branch, with no new reject branches. All sizing limits hold.

## Concurrency model

No new goroutines or locks. Binding remains atomic under the devices registry lock. Snapshot assignment stays on the manager Run goroutine before opening; handler workers read the connection snapshot. Tests synchronize observations through channels and existing open barriers.

## Error handling

Zero from either enqueue API produces the existing retryable backlog refusal, without fallback. Routing, attachment refusal, `/clear`, acknowledgements and best-effort persistence retain their current behavior. Empty identity is valid metadata.

## Testing strategy

Add a recording identified queue without changing legacy fakes. Table scenarios verify distinct keys with equal names/app ids, rename/reconnect, empty key and nil auth, hostile payload identity/name, exact arguments, safe text versus attachment delivery, UTC tap time and no identity/credential logging. Exercise identified and legacy success/backlog arms, plus routing/attachment/reset gates before enqueue. Existing relay regressions cover the remaining legacy behavior and wire projections.

Use encrypted fake-handshake dispatch to observe `Conn.Auth()` on first binding and a renamed reconnect. Retain key/version refusal tests and add unknown-token refusal against an unbound pairing. Run new tests before implementation and confirm meaningful failures, then run `go test -race ./internal/relay/...`, `go vet ./...` and `go build -o /tmp/builder-2971/pyry ./cmd/pyry`. The verifier owns the full-module gate; live Claude is unnecessary.

## Open questions

None.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/history-package.md`, “Producers (#2114, #2115)”, state that relay correlation identity is the authenticated pairing's bound Noise public key, separate from display name and app message id. Explain that names can change or coincide, and that relay offers this metadata through the optional identified enqueue path while daemon/history adoption belongs to #2972.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `connSender` reads only authenticated `Conn.Auth()`; payload identity/name cannot choose correlation identity. `handleNoiseInit` copies the Noise-authenticated peer key only after token/version admission and successful atomic binding.
- [Tokens, credentials] No generation or credential lifecycle changes. Only the public `StaticKey` crosses the queue seam; neither token nor hash supplies identity.
- [File operations] Existing registry persistence remains unchanged. Attachment resolution stays after routing and before acceptance; paths stay in delivery only.
- [Subprocesses] No process creation or new command input; queue delivery behavior is unchanged.
- [Cryptography] Reuses Noise peer authentication and `BindStaticKey`; hex encoding adds no primitive, nonce or secret comparison.
- [Network and I/O] No new wire fields, sockets or reads; existing transport frame ceilings and handshake deadlines remain in force.
- [Errors, logs, telemetry] SHOULD FIX: test both enqueue outcomes for identity/token/hash absence from logs; preserve static refusal messages and existing display-name/version fields.
- [Concurrency] No check-then-bind lookup is added. Copy the successfully bound peer key into the local snapshot, avoiding a second registry lookup that could observe revocation; no new goroutine needs shutdown.
- [Threat model] Noise and token admission continue to prevent relay impersonation; equal app ids remain distinct metadata rather than a deduplication claim. OUT OF SCOPE: production wrapper/history adoption is #2972; no changes to ADR 042's broader thread behavior.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-08
