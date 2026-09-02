# #1687 — carry a permission mode on `set_session_settings`

## Files read

- `internal/protocol/settings.go` → `SetSessionSettingsPayload`, `SessionSettingsPayload` — the two payloads this ticket extends, and the one place the opposing `omitempty` rules for the write half (pointers, omitempty) and the read half (values, no omitempty) are written down. Its doc warns against copying either style across; the new field lands on both halves and must obey each rule locally.
- `internal/relay/v2session_settings.go` → `handleSetSessionSettings`, `handleRequestSessionSettings`, `validModel`, `validEffort`, `settingsReplyError` — the wire boundary. `handleSetSessionSettings`'s numbered order comment is the contract the new reject branches slot into (validate before persist); `validEffort` is the closed-enum shape the new validator copies, `validModel` the byte-class shape it must NOT copy.
- `internal/relay/v2session_seams.go` → `SettingsUpdate`, `RunConfig`, `SettingsUpdater`, `V2SessionConfig.RunConfigFor` — the two primitive-only value types that carry the write and read halves across the package boundary without `internal/relay` importing `internal/sessions`.
- `internal/sessions/session.go` → `permissionModeInBand`, `permissionModeKnown`, `canonicalPermissionMode`, `canonicalSettings`, `SettingsUpdate.PermissionMode`, `SessionSettings.PermissionMode`, `ErrUnsupportedPermissionMode`, `ErrPermissionModeConflict` — #2043's landed vocabulary. `permissionModeInBand`'s member list IS the vocabulary this ticket admits, and its doc explicitly defers "who may ask for that" to #1687.
- `internal/sessions/pool.go` → `Pool.UpdateSettings`, `validatePermissionUpdate`, `Pool.SettingsFor` — the downstream contract. Its derivation table fixes what a stored (mode, YOLO) pair can be; `SettingsFor` returns `sess.settings` verbatim, and `canonicalSettings` is applied at both construction sites, so a pool-held session's mode is never `""`.
- `cmd/pyry/main.go` → `settingsUpdaterAdapter.UpdateSettings`, `boundRunSettings`, `resolveBoundRunSettings` — the write adapter and the read resolver, the two 1:1 mirrors that need the new field to reach `internal/sessions` and come back.
- `cmd/pyry/relay.go` → `runConfigFor` — composes `boundRunSettings` + usage into `relay.RunConfig`. The ticket's `Estimate:` line omits this file; it is the fifth production file.
- `docs/protocol-mobile.md` § Session settings (v2) — the client-facing contract; four tables and two examples to extend.
- `docs/knowledge/features/protocol-package-types-session-settings-payloads.md` and `…-read-payloads.md` — the prior lesson that matters here: the `omitempty` divergence between the two payloads is deliberate and has already been "fixed" wrongly once. The read half's rule ("every zero value is a real answer") forces me to say what a `""` mode means on the wire rather than leaving it implicit.
- `internal/protocol/settings_test.go`, `internal/relay/v2session_settings_test.go`, `internal/relay/v2session_settings_read_test.go`, `cmd/pyry/run_config_test.go` — the four table-driven suites the new cases extend rather than duplicate.

## Context

`set_session_settings` carries `model`, `effort` and a `yolo` **boolean**. Claude has six permission modes, so a boolean spells two of them and a client cannot ask for the other four. That is the capability gap under pyrycode-desktop#682 (the input footer's permission menu).

The two prerequisites landed: #2042 made the control-line writer take a caller-named mode, and #2043 gave a stored session a `PermissionMode` alongside its `YOLO`, derived so the two can never disagree, plus in-band delivery to a running child. `sessions.SettingsUpdate` already carries a `PermissionMode *string`, and `permissionModeInBand`'s doc defers "who may ask for that" to this ticket. What is missing is the **wire half**: nothing between the phone and `Pool.UpdateSettings` can express a mode, and nothing reports the one in force.

This ticket is the wire vocabulary and its boundary. No ADR is warranted — the design decisions here are two closed choices already stated on the ticket, and they belong in the payload doc comments and `docs/protocol-mobile.md`, which is where a client author reads them.

## Design

### The vocabulary this ticket admits

Exactly `permissionModeInBand`'s five members: `default`, `acceptEdits`, `plan`, `auto`, `dontAsk`. Measured live at claude 2.1.239 by #2041; #2043 encoded the result as `permissionModeInBand`. `bypassPermissions` is **refused** on the mode field (AC #4) and `""` is refused too.

`""` is the asymmetry to state loudly, because the two sibling validators in the same file accept it. For `model` and `effort`, `""` means "emit no flag, run at claude's own default" — a real value. The default *posture* is a nameable mode (`default`), so an explicit `""` here has no reading; `sessions.validatePermissionUpdate` already rejects it with `ErrUnsupportedPermissionMode`. Refusing it at the wire keeps the two layers agreeing rather than relying on the inner one.

### Write half

`protocol.SetSessionSettingsPayload` gains:

```go
PermissionMode *string `json:"permission_mode,omitempty"`
```

Pointer + `omitempty`, matching the three siblings' presence contract: absent ⇒ leave unchanged, present ⇒ set to this value. Unlike the siblings, present-at-`""` is not a settable value — it is a refusal — and the field doc says so.

`relay.SettingsUpdate` and `sessions.SettingsUpdate` already mirror 1:1; the former gains `PermissionMode *string` so `settingsUpdaterAdapter` keeps passing pointers straight through.

`handleSetSessionSettings` gains two reject branches inside its existing step 3 ("validate before any persistence"), both replying with the unchanged `msgSettingsMalformed` constant and echoing no payload byte and no decode error:

1. **Conflict** — `p.PermissionMode != nil && p.YOLO != nil` ⇒ malformed. Unconditional on the mode's value, so AC #3's "neither field can win over the other" holds even for a garbage mode.
2. **Vocabulary** — `p.PermissionMode != nil && !validPermissionMode(*p.PermissionMode)` ⇒ malformed.

Order between the two is unobservable (identical reply), so it is chosen for the property above rather than for behaviour, and the comment says which.

`validPermissionMode(m string) bool` lands beside `validEffort`, same closed-enum switch shape, same relay-local reason (`internal/relay` cannot import `internal/sessions`). Its doc records the two directions of the closed-enum hazard `validEffort` already flags — it refuses inbound anything claude adds later — plus the two members it deliberately omits and why.

### Read half

`protocol.SessionSettingsPayload` gains `PermissionMode string \`json:"permission_mode"\`` — **no** `omitempty`, matching every sibling on that struct. `relay.RunConfig` gains `PermissionMode string`; `boundRunSettings` gains `permissionMode`; `resolveBoundRunSettings` reads `s.PermissionMode` under the same single `SettingsFor` acquisition, so it still cannot describe a session another field does not; `runConfigFor` copies it across.

Two properties to write into the docs because a client keys its menu on them:

- A resolved session **always** reports one of the six modes, never `""`. `canonicalSettings` normalises at both construction sites and `SettingsFor` returns the stored value verbatim, so `""` appears only in the all-zero reply that already means "nothing resolved" (`session_id: ""`).
- A session in bypass reports `permission_mode: "bypassPermissions"` **and** `yolo: true` — #2043's invariant that the two fields never disagree. So the read half can report a posture the write half's mode field refuses to set; that is deliberate, and `yolo` remains the only spelling that grants it.

### The two decisions, restated as code properties

- **Joins `yolo`, does not supersede it.** `yolo` stays on both payloads with unchanged semantics; no client is drifted.
- **Both ⇒ malformed.** Refusal, not precedence — the same reasoning `sessions.ErrPermissionModeConflict` records, one layer out. The relay's rule is deliberately *stricter* than `validatePermissionUpdate`'s (which refuses only a contradicting pair): a frame carrying both is refused at the boundary whether or not the two agree, so there is no precedence rule for a client author or a reviewer to get wrong.

### Data flow

```
phone ──set_session_settings{permission_mode}──▶ handleSetSessionSettings
                                                   │ conflict? vocabulary?  ⇒ malformed reply, nothing persisted
                                                   ▼
                            relay.SettingsUpdate{PermissionMode *string}
                                                   ▼ settingsUpdaterAdapter
                            sessions.SettingsUpdate{PermissionMode *string}  (#2043)
                                                   ▼ Pool.UpdateSettings ⇒ store + in-band deliver

phone ──request_session_settings{conversation_id}─▶ handleRequestSessionSettings
                                                   ▼ RunConfigFor
   resolveBoundRunSettings ⇒ boundRunSettings{permissionMode} ─▶ relay.RunConfig ─▶ SessionSettingsPayload
```

## Concurrency model

No new goroutines, no new locks, no shutdown path. Both handlers already run on the manager's single `Run` dispatch goroutine. The read path's single-acquisition property is inherited unchanged: `resolveBoundRunSettings` reads the new field from the same `SettingsFor` return value as the other three, so it cannot report a mode from one session and a model from another even against a concurrent idle eviction. The write path's atomicity is `Pool.UpdateSettings`'s single `saveLocked` with rollback, which #2043 already extended to derive both posture fields together.

## Error handling

| Failure | Reply | Persisted |
|---|---|---|
| Payload decode failure | `protocol.malformed`, `msgSettingsMalformed` | nothing |
| `permission_mode` outside the five (`""`, `bypassPermissions`, garbage) | same | nothing |
| `permission_mode` **and** `yolo` both present | same | nothing |
| Nil `SettingsUpdater` | `server.binary_offline`, `msgSettingsUnavailable` | nothing |
| `ErrSessionUnknown` | `session.not_found`, `msgSettingsNotFound` | nothing |
| Any other `UpdateSettings` error, e.g. `ErrUnsupportedPermissionMode` slipping past the relay | `server.binary_offline`, `msgSettingsUnavailable` | nothing |

No new message constant: every new reject reuses `msgSettingsMalformed`, so no reject can be distinguished by its reply text and no rejected byte can reach the wire, a log line, or an error string. The existing persist-failure log stays content-free (`conn_id` + `session_id` only) — a permission mode is a settings value and #833 keeps those out of the log at every level.

The last row is the interesting one: the relay validator and `validatePermissionUpdate` are independent gates on the same vocabulary. If they ever drift, the inner one still refuses and the operator sees a generic unavailable reply rather than a persisted bad posture. Fail-closed in both orders.

## Testing strategy

Extend the four existing table-driven suites; no new test file.

- `internal/protocol/settings_test.go`
  - `TestSetSessionSettingsPayload_RoundTrip` — assert `PermissionMode == nil` on **both** existing fixtures (each omits the key), pinning that the new field does not disturb the two wire forms already on the wire and that absent decodes nil.
  - New case driving a new fixture `set_session_settings_mode.json` (`{"session_id":"sess-a","permission_mode":"plan"}`): mode non-nil and `"plan"`, the other three nil, byte-equal re-marshal. That plus the omitted fixture is AC #1's "both wire forms pinned by fixtures".
  - `session_settings.json` gains `"permission_mode":"default"`; the read-half round-trip asserts it. The read struct has no `omitempty`, so the existing byte-equal re-marshal fails until the fixture is updated — the drift detector doing its job.
- `internal/relay/v2session_settings_test.go`
  - Accept rows: a mode-only frame reaches the seam as `SettingsUpdate{PermissionMode: strPtr("plan")}`, and a mode alongside `model`/`effort` passes all three through.
  - Reject rows on the existing malformed table, each asserting the seam was **not** called and the reply is the deterministic malformed one: `""`, `bypassPermissions`, an unknown value, a value differing only in case (`Plan`), a mode + `yolo:true`, a mode + `yolo:false` (the pair `validatePermissionUpdate` would have *accepted* — proving the relay's stricter rule).
  - A closure test over the validator: every member of the five accepted, plus the two named refusals, in one table.
- `internal/relay/v2session_settings_read_test.go` — extend `fixtureRunConfig`/`fixtureReport` so the reported mode rides the existing assertions, and pin that the unresolvable cases still report `""`.
- `cmd/pyry/run_config_test.go` — `resolveBoundRunSettings` carries `SessionSettings.PermissionMode` into `boundRunSettings`; `runConfigFor` carries it into `relay.RunConfig`; the refusal paths still return the zero value.
- `cmd/pyry` adapter — the pointer reaches `sessions.SettingsUpdate` unchanged (nil stays nil).

RED first: the protocol fixture round-trip and the relay reject rows both fail before the production edits — the former on an unknown-field/byte-equality mismatch, the latter because an unvalidated mode reaches the seam.

## Open questions

1. **Do the two reject branches need distinguishable replies?** Resolved in the design: no. A client cannot act differently on "bad mode" vs "sent both", both are its own bug, and a distinguishable reply is an oracle. One constant.
2. **Should the read half report `bypassPermissions`?** Resolved: yes. Suppressing it would make the reported posture disagree with `yolo` and with the daemon's own state, which is the whole point of the read half existing.
3. **Does any e2e fixture or golden file carry `session_settings` bytes that the new always-present key breaks?** To confirm in Phase B by running the touched packages plus `internal/e2e`; if one exists it is updated in the same commit and recorded here.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The boundary is explicit and singular: `handleSetSessionSettings` is the only place an inbound mode crosses from untrusted to trusted, and `validPermissionMode` is the only predicate on it. Downstream the value is a `*string` inside `relay.SettingsUpdate` → `sessions.SettingsUpdate`, and `sessions.validatePermissionUpdate` re-checks it against `permissionModeKnown` before anything is mutated — an independent second gate that fails closed if the two vocabularies ever drift. There is no third parse site. The read half crosses the *other* way and carries only daemon-owned state.
- **[Privilege escalation — the category this ticket exists to bound]** No MUST FIX, and this is the finding to read twice. Three properties have to hold together for AC #4 to be worth anything, and each is enforced by different code: (a) `validPermissionMode` omits `bypassPermissions`, so the wire cannot name it; (b) `sessions.canonicalPermissionMode` escalates only on the `YOLO` bit, so no mode string can *derive* bypass; (c) `sessions.claudeSettingsArgs` emits `--dangerously-skip-permissions` from the `YOLO` bit alone and never from `PermissionMode`, so no mode string can compose a bypass argv. I verified (b) and (c) by reading the landed #2043 code rather than assuming them. The ticket therefore adds no path to bypass that did not exist before it — `yolo:true` remains the single spelling. **Note the honest limit:** three of the five admitted modes (`acceptEdits`, `auto`, `dontAsk`) genuinely *loosen* a child launched behind the daemon's approval flags, and `dontAsk` means the permission-prompt tool never fires. That is the ticket's stated purpose, gated on the same `interactive` capability that already gates `yolo:true` — a strictly weaker grant than the one the same frame can already make. It is not a widening of *who* may ask.
- **[Argv / subprocess injection]** No findings. The accepted set is five fixed literals, so no attacker-shaped byte reaches an argv element. This is the reason the design refuses `validModel`'s byte-class approach for this field: a grammar admits values nobody enumerated, and this vocabulary is genuinely closed. The value also reaches a second sink — `streamsup`'s `set_permission_mode` control request — which #2043 gave its own writer-side allow-list; a value passing here passes there by construction, and one that somehow did not is refused by the writer.
- **[Injection into the in-band stream]** No findings. Unlike `model`, an accepted mode never reaches `deliverSettingsInBand`'s turn-text interpolation — #2043 delivers a posture as a JSON control request, not as `/`-command text — so the whitespace/line-terminator reasoning `validModel` needs does not apply. The five literals contain no byte that would matter to either sink anyway.
- **[Error messages, logs, telemetry]** No MUST FIX. Every new reject reuses the fixed `msgSettingsMalformed` constant; no rejected byte, no decode error, and no vocabulary hint reaches the wire. **SHOULD FIX, carried into Phase B:** do not add a per-reject log line naming the mode. The existing handler logs `conn_id` + `session_id` only, and a permission mode is a settings value #833 keeps out of the log — the temptation to log "rejected mode X" for debuggability is exactly the leak. The new reject branches log nothing.
- **[Reply-shape oracle]** No findings, by decision rather than by accident. All rejects share one reply, so a client cannot probe the accepted vocabulary by reply differences. The pre-existing distinction between `session.not_found` and `malformed` is unchanged and already gated behind the `interactive` capability — a non-interactive conn is inert before the decode, so none of this is reachable unauthenticated.
- **[Persistence / partial state]** No findings. Validation is ordered strictly before the `SettingsUpdater` call (the handler's step 3, unchanged), so a refused mode is never persisted and never reaches a spawn argv. `Pool.UpdateSettings` remains one atomic `saveLocked` with rollback, extended by #2043 to derive both posture fields together, so a stored mode and a stored `YOLO` cannot disagree even under a partial failure.
- **[Concurrency]** No findings. No new goroutines, no new locks, no new lock ordering. Both handlers run on the manager's single dispatch goroutine; the read path adds one field to a value already resolved under a single `SettingsFor` acquisition, so it introduces no TOCTOU between the reported mode and the reported session id.
- **[Information disclosure via the read half]** No findings. The reply gains one short enum-ish string describing the daemon's own configuration for the session bound to the conversation the caller named. It carries no screen byte, no transcript byte and no path, it is capability-gated, and an unresolvable request still gets the all-zero reply rather than another session's — the #678 isolation property is untouched because the new field rides the same `RunConfig`.
- **[Tokens/secrets, file operations, crypto, network I/O]** Not applicable, and not by hand-wave: this ticket adds no token, no credential, no filesystem path, no crypto primitive and no socket read. Persistence is entirely `Pool.UpdateSettings`, which this ticket does not modify; the bytes it adds are one JSON key on each of two existing payloads carried inside the existing Noise-framed envelope.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model's relevant threat is a paired-but-hostile client escalating its own privileges. Addressed by the privilege-escalation finding above: the frame cannot name bypass, cannot derive it, and cannot compose it. An unpaired party reaches none of this — the handler is inert before the decode on a non-interactive conn.
- **[OUT OF SCOPE]** Forwarding claude's per-model `auto` refusal to the client (the ticket defers it; #1819's `supportsAutoMode` pre-empts it client-side) and removing `yolo` together with the launch-argv question that would make bypass in-band (#1686). Neither defers a check this ticket needed.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02
