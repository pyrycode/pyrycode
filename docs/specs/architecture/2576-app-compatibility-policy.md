# #2576 — Compatibility policy for older apps, and the app-too-old rejection

## Files read

- `internal/protocol/codes.go` → the error-code `const` block — where the new code goes, and the "static message, reasoning in the doc block" convention every group follows.
- `internal/protocol/compat_test.go` → `TestErrorCode_Constants_MatchSpec` — the two maps the new code is pinned in.
- `internal/protocol/handshake.go` → `ErrorPayload` (has no version field; `ConversationID` is the precedent for an optional, daemon-authored field), `HelloClientPayload.ClientVersion` (free text today, read by nothing).
- `internal/protocol/handshake_test.go` → `TestErrorPayload_ConversationIDIsOptional` — the shape the new optional-field test mirrors.
- `internal/relay/v2session.go` → the v2 close-code `const` block (`StatusIdleTimeout`, `StatusProtocolMismatch`, `StatusHandshakeFailure`, `StatusQueueOverflow`, `StatusSessionGone`) and its 44xx←HTTP naming convention.
- `internal/relay/auth.go` → `StatusUnauthorized` (4401); `internal/relay/connection.go` → `statusServerIDConflict` (4409, relay-sent).
- `internal/relay/v2session_handshake.go` → the token-failure arm of the IK responder: `noise_resp` first, then ONE routing envelope carrying the sealed `auth.invalid_token` error plus the 4401 close. The app-too-old rejection reuses exactly this shape.
- `docs/protocol-mobile.md` § `hello` (v2-specific note), § Capability negotiation (v2), § Error codes (close-code table), § Versioning.
- `docs/knowledge/decisions/025-…` (2026-06-22 amendment) and `037-capability-strings-not-version-numbers.md` — the no-install-base premise this ticket retires, and the "no orderable version number" decision this ticket must not contradict.

## Context

Until now the design assumed one operator updating apps and daemons together (ADR 025 amendment 2026-06-22, repeated by ADR 037). Once other people install from app stores, app updates lag while the relay and hosted daemons update at once, and one app can be connected to several hosted daemons running different versions mid-rollout. The spec has no rule for that. This ticket writes the rule and reserves its two wire codes; **nothing enforces it yet** (no `client_version` parser, no stored minimum, no sender of the rejection), so runtime behaviour does not change.

ADR note for the documentation stage: this is an amendment to ADR 025 and ADR 037 (see handoff), not a new ADR. It does **not** contradict ADR 037: capability strings stay the only way a client detects a daemon feature. The app version introduced here flows the other way — the daemon reading which app build is talking to it, for one purpose only, refusing a retired build — and store releases of one app are monotonic, which the daemon's commit sha is not.

## Design — the code change

Three production files, all additive:

- `internal/protocol/codes.go`: `CodeClientUpdateRequired = "client.update_required"` in a new "Client errors" group with a doc block stating decisions 2, 5 and 8 below.
- `internal/protocol/handshake.go`: `ErrorPayload` gains `MinClientVersion string \`json:"min_client_version,omitempty"\``. Omitted by every other reply, so every existing error frame stays byte-identical. Daemon-authored (from the configured minimum), never an echo of the client's `client_version`.
- `internal/relay/v2session.go`: `StatusClientUpdateRequired websocket.StatusCode = 4412` appended to the v2 close-code block.

No sender, no parser, no config. The enforcement ticket adds those against the decisions below.

## Decisions (the documentation stage copies these)

1. **Close code: `4412`**, Go name `StatusClientUpdateRequired`. Echoes HTTP 412 (Precondition Failed) under the 44xx←HTTP convention: the minimum app version is a precondition the `hello` failed. 426 (Upgrade Required) would be the literal match but 4426 is already `StatusHandshakeFailure`. 4412 is used nowhere today — not in the daemon, the relay, or the spec's close-code table. Sent by the binary, forwarded by the relay (like 4401).

2. **Error code: `client.update_required`**, Go name `CodeClientUpdateRequired`, **`retryable: false`**. Retrying with the same app build fails identically; only an app update repairs it. New category `client` because the fault is the client build, not the protocol (`protocol.unsupported` is about protocol versions) and not auth. Static message by convention, e.g. `"this app version is no longer supported by this host; update the app"`. The minimum travels in the new optional `ErrorPayload.min_client_version` field (decision 4 names its format), not in the message.

3. **`client_version` format: `<app>/<MAJOR>.<MINOR>.<PATCH>`**, e.g. `pyrycode-mobile/1.4.0`, `pyrycode-desktop/0.3.0`.
   - `<app>`: lowercase ASCII letters, digits and `-`, starting with a letter. The two defined names are `pyrycode-mobile` and `pyrycode-desktop`.
   - Exactly one `/`.
   - Version: exactly three `.`-separated decimal integers, digits only, no leading zeros except `0` itself, no `v` prefix, no pre-release or build suffix (`1.4.0-beta`, `1.4.0+42` are unparsable).
   - Whole string at most 64 bytes; longer is unparsable.
   - Anything else — including today's `"1.0"`, `"0.1.0"` and the spec example `"pyrycode-mobile 0.1.0"` (space, no slash) — is **unparsable**. The spec example is replaced by `pyrycode-mobile/0.1.0`.

4. **Ordering.** Versions are compared only between the **same app name**; a minimum is configured per app name, and a version of one app is never compared with another app's minimum. Compare MAJOR, then MINOR, then PATCH, numerically; the first differing component decides. A client passes when its version is **greater than or equal to** the minimum (equal passes). `min_client_version` on the wire carries the three-part version only (`"1.4.0"`), the minimum configured for the requesting app — the client already knows its own app name.

5. **Where the rejection happens.** In the v2 IK responder, **after** the Noise handshake completes and **after** the token validates, before the session opens (before `hello_ack` and before any application frame). The sequence mirrors the token-failure arm: send `noise_resp` (so the AEAD channel exists), then ONE routing envelope carrying the sealed `error` (`code: client.update_required`, `retryable: false`, `in_reply_to` = the `hello` id, `min_client_version` when known) together with close `4412`, so the client reads the error before the close. If sealing fails, close-only at 4412. Token check first is deliberate: an unauthenticated peer gets `auth.invalid_token`/4401 and learns nothing about the host's version policy. The relay is untouched: it never reads `client_version`, and the route stays `/v1/client`.

6. **No minimum, no rejection.** A daemon with no minimum configured for an app rejects no client of that app. A daemon with no minimum configured at all rejects nobody, whatever `client_version` says — which is today's behaviour, so every daemon ships in this state until an operator sets a minimum.

7. **App-less or unparsable `client_version`, once a minimum is set.** Once **any** minimum is configured, an unparsable `client_version` is rejected with `client.update_required`/4412, with `min_client_version` **omitted** (the daemon cannot tell which app's minimum applies). Rationale: every build released before this format sends free text (`"1.0"`, `"0.1.0"`), and those are exactly the builds a minimum exists to retire. A well-formed version naming an app with **no** configured minimum is accepted (decision 6). Operational consequence the spec should state: set the first minimum only after both apps have shipped a release that sends the new format.

8. **Clients treat the rejection as terminal for that host only.** On `client.update_required` or close 4412 the client stops automatically re-dialling **that host** (no backoff loop), shows an "update the app" state for it — naming `min_client_version` when present — and keeps every other host's connection untouched. A manual retry by the user is allowed; after an app update the client connects normally. Builds that predate this code will not recognise 4412 and will treat it as a generic close; that cannot be fixed retroactively, and each of their re-dials costs one handshake plus one rejection (the relay's 4429 rate limit bounds it).

## The five compatibility rules (for the new spec section)

1. **Changes are additive.** New envelope types and new optional fields only; no field is removed, renamed, retyped or made required within a protocol version. Receivers already drop unknown envelope types and ignore unknown fields, and that stays mandatory.
2. **A new meaning gets a new envelope type, never a new value in an existing field.** Old desktop builds drop a whole frame when a known field carries an unknown value, so a new enum value in an existing field silently loses the frame on every un-updated app. `workspace_updated` (`TypeWorkspaceUpdated`'s doc block) is the precedent. Exception: the `error.code` vocabulary and the close-code table are open sets by definition — a client handles an unknown error code by its `retryable` flag and an unknown close code as a generic close — so adding a row to either is allowed. (`client.update_required` and 4412 are themselves such additions.)
3. **A server-sent frame type added from now on goes only to connections that advertised a capability string for it** (negotiated through the existing intersection in `hello`/`hello_ack`). A reply to a request only an updated client can send satisfies this by construction; the rule bites on unsolicited pushes. Existing ungated frames stay as they are — only `interactive` gates today, `question`, `model_list` and `context_usage` are detection only, and `context_usage` frames reach clients that never advertised it — because every released app already handles them.
4. **The relay and hosted daemons deploy before the app release that needs them.** An app still detects each daemon feature by capability string (ADR 037), because during a rollout one app can face daemons on different versions.
5. **A breaking change adds a protocol version**, served alongside the old one until the minimum app version retires every app that speaks only the old one. The daemon does not read `protocol_versions` today; the first breaking change is where that starts.

## Concurrency model

None — constants and one struct field.

## Error handling

None at runtime. The new code and close code have no sender in this ticket.

## Testing strategy

- `TestErrorCode_Constants_MatchSpec` (`internal/protocol/compat_test.go`): add `CodeClientUpdateRequired` → `"client.update_required"` to both maps.
- New `TestErrorPayload_MinClientVersionIsOptional` beside `TestErrorPayload_ConversationIDIsOptional`: an unset field emits no `min_client_version` key (existing error frames byte-identical); a set field round-trips under the wire key `min_client_version`.
- New `TestV2CloseCodes_MatchSpec` in `internal/relay/v2session_test.go`: pins every daemon close-code constant to its wire value (4401, 4408, 4410, 4412, 4413, 4421, 4426) and asserts the set is pairwise distinct and disjoint from the other codes in use (1000, 1011, relay-sent 4404, 4409, 4429) — which is what "a value not in use today" means as a test.

RED first: the tests reference the undeclared symbols, so the packages fail to compile until the constants exist.

In-flight overlap: `origin/feature/449` (stale, May) also touches `codes.go` and `v2session.go`; no dependency — edits here are appends.

## Documentation handoff (pending — documentation stage)

- `docs/protocol-mobile.md`: new **§ Compatibility** next to § Versioning, stating the five rules above and defining the app-too-old rejection (decisions 1, 2, 4–8: sealed `client.update_required` error carrying `min_client_version`, then close 4412; terminal for that host only; no rejection while no minimum is set; unparsable versions rejected once any minimum is set, with `min_client_version` omitted).
- § `hello` (v2-specific note): define the `client_version` format (decision 3) and ordering (decision 4); replace the `"pyrycode-mobile 0.1.0"` example with `"pyrycode-mobile/0.1.0"`.
- § Error codes: a row for `client.update_required` (non-retryable, carries `min_client_version`), a `4412` row in the close-code table, and a paragraph for 4412 like the existing 4410 one. Document `min_client_version` as an optional `error` payload field.
- § Capability negotiation (v2): the "Superseded as a requirement — 2026-06-22" note points at § Compatibility.
- ADR 025 (the 2026-06-22 Status amendment) and ADR 037: each gets a dated amendment retiring the no-install-base premise and pointing at § Compatibility. ADR 037's amendment should say the app version does not replace capability strings (see Context).
