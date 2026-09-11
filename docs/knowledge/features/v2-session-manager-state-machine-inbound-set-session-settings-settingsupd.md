# Inbound `set_session_settings` (#845) — `SettingsUpdater` seam, validate, persist, reply

`set_session_settings` is a v2 **control** envelope (phone → binary),
intercepted in `dispatchAppFrame`'s discriminator switch **before**
`dispatch.Route` — same boundary as `interrupt` / `request_snapshot` /
`request_debug_bundle`, no `dispatch.Route` handler. It consumes [#844's wire
vocabulary](protocol-package.md#session-settings-payloads-844-permission-mode-1687)
(`SetSessionSettingsPayload` / `SessionSettingsUpdatedPayload` /
`CodeSessionNotFound`) and is the **daemon-side write path** for a paired
client's per-session `model` / `effort` / `yolo` / `permission_mode` (#1687)
— the untrusted-value validation #833 explicitly deferred to this
wire-owning ticket.
**`security-sensitive`**: the first inbound control verb that both persists a
mutation from untrusted input AND owes the caller a reply. A *running* session
picks the change up **immediately**: `Pool.UpdateSettings` partitions on which
fields the frame carried, writing a model/effort-only change to the live child
as a `/model` or `/effort` command on the stream the daemon already holds open
(#1581, no respawn, transcript preserved) and live-restarting the supervisor for
everything else (#842). Both install the recomposed argv (#833's
`claudeSettingsArgs` path), so the next spawn carries it too — see
[`sessions-package.md`](sessions-package.md) § "Live-apply on a real change".
See [`codebase/845.md`](../codebase/845.md).

Unlike the fire-and-forget verbs (`interrupt` / `new_session` /
`dequeue_message`), this verb **always replies** on the interactive path —
success or failure, never a silent drop. Control flow, in load-bearing order:

1. **Capability gate.** `if !s.interactive { return }` — a non-interactive
   conn is fully inert: no decode, no seam call, **no reply**. It must not
   even learn whether the named session exists (a bare check, matching
   `handleInterrupt` — not a reusable inbound-gate abstraction).
2. **Decode.** `json.Unmarshal(env.Payload, &SetSessionSettingsPayload{})`.
   Because this verb owes a reply (unlike the fire-and-forget verbs), a
   decode failure yields a `protocol.malformed` reply, not a silent drop.
   Never echoes the decode error or any payload byte —
   `encoding/json` quotes attacker bytes into its error string.
3. **Validate `model`/`effort`/`permission_mode` before any persistence** —
   an invalid value yields a malformed reply, never a persisted bad setting
   (the argv-injection defense, below). `yolo` needs no value check: a
   malformed `yolo` already failed step 2's type-decode, so bypass can never
   be inferred from a bad value. `permission_mode` (#1687) adds two reject
   branches here, both replying with the same `msgSettingsMalformed`
   constant used everywhere else in this step, order chosen for the
   property below rather than for behaviour (the reply is identical either
   way):
   - **Conflict**: `PermissionMode != nil && YOLO != nil` — unconditional on
     the mode's value, so "neither field can win over the other" holds even
     for an unrecognised mode.
   - **Vocabulary**: `PermissionMode != nil && !validPermissionMode(*PermissionMode)`.
4. **Nil-seam guard.** `m.cfg.SettingsUpdater == nil` → deterministic
   `server.binary_offline` "unavailable" reply (foreground / unwired), never
   a silent drop.
5. **Availability gate, persist + reply.** `SettingsUpdater.UpdateSettings`
   checks a non-empty model against the retained vocabulary before it calls
   `Pool.UpdateSettings`. An exact, untruncated `ModelOption.Value` match
   proceeds; complete-menu absence returns `ErrModelNotOffered`, while a
   missing/empty menu, dropped row, or any value-truncated row returns
   `ErrModelVocabularyUnavailable`. The handler maps those to non-retryable
   `protocol.malformed` and retryable `model_list.unavailable`, respectively.
   `nil` error → `session_settings_updated`; `ErrSessionUnknown` →
   `session.not_found`; any remaining error → `server.binary_offline`. All
   rejection messages and event names are fixed and carry no requested or
   published model value.

- **`SettingsUpdater` / `SettingsUpdate` / outcome-sentinel consumer seam**
  (beside `Interrupter` / `SessionStarter` / `QueueRemover`). `SettingsUpdate{
  Model, Effort *string; YOLO *bool; PermissionMode *string}` (#1687) mirrors
  `sessions.SettingsUpdate` 1:1 so
  `internal/relay` imports neither `internal/sessions` nor `cmd/pyry`; the
  optional `V2SessionConfig.SettingsUpdater` field is nil-safe (nil ⇒
  "unavailable" reply, never drop). `cmd/pyry`'s `settingsUpdaterAdapter{p
  *sessions.Pool}` is the **sole** place `sessions.ErrSessionNotFound` maps
  onto `relay.ErrSessionUnknown` and classifies the retained model vocabulary
  as `ErrModelNotOffered` or `ErrModelVocabularyUnavailable` — the project's
  sentinel-to-wire-mapping convention (mapping lives at the consumer call site,
  not the primitive). The adapter checks session existence first, so an unknown
  id cannot probe whether the bootstrap vocabulary is complete.
  Wired by threading a `settings relay.SettingsUpdater` param through
  `startRelay` → `startRelayV2` from the single `main.go` call site
  (`settingsUpdaterAdapter{pool}`, `pool` already in scope alongside
  `sessionMinter{pool}`).
- **`validModel(m string) bool`** — a **shape check, not the availability
  decision** (model names churn per release; a fixed in-code allowlist would
  force a code edit per launch). Availability is checked afterward against the
  dynamically retained published menu. `""` accepted (clears to template default — `claudeSettingsArgs`
  emits no `--model` for it); otherwise a value within the unchanged 64-byte
  bound whose first byte is alphanumeric, whose remaining bytes are in
  `[A-Za-z0-9._-]`, and which may carry **one trailing bracket group** —
  non-empty, balanced, unnested, the value's final element, drawn from that
  same closed byte class (`modelWordByte`) — widened at **#1838** so the
  variant rows claude's own model menu publishes (`opus[1m]`,
  `claude-fable-5[1m]`) reach a session instead of failing on click. Stated as
  a grammar rather than a byte-set diff because an accepted value reaches
  **two** sinks: the claude argv (`--model <value>`, two separate `execve`
  elements under `exec.CommandContext` with no shell — the
  first-byte-alphanumeric rule is what stops a value posing as a flag, #833's
  argv-injection defense), and — since #1581 stopped restarting a live child
  for a model change — the child's turn text
  (`internal/sessions/pool.go`'s `deliverSettingsInBand` writes
  `"/model " + value` as one stdin line), which additionally requires an
  accepted value to stay a single whitespace-free token. The trailing-group
  grammar collapses to three conditions checked on the value's *first* `[`
  (last byte closes it, the interior is non-empty, every interior byte is a
  `modelWordByte`), which together rule out nesting, a second group and a
  trailing suffix without a separate check for any of the three — because the
  interior excludes both brackets, nothing inside can open or close another
  group. `TestValidModel_ByteSetIsClosed` checks the whitespace-free
  guarantee across all 256 byte values in each of the three grammar positions
  rather than by example. A well-shaped non-empty model can still be rejected
  when no exact untruncated `ModelOption.Value` row is offered. `validEffort`
  below carries the identical direction
  hazard and is deliberately **not** widened — there is no bracketed effort
  level to admit yet, and widening against a hypothetical is not
  evidence-based.
- **`validEffort(e string) bool`** — `""` (clear) or the closed enum `{low,
  medium, high, xhigh, max}`, matching `cmd/pyry/agent_run.go`'s
  `validEfforts` but defined relay-local since `internal/relay` cannot import
  `cmd/pyry`.
- **`validPermissionMode(mode string) bool`** (#1687) — a closed enum like
  `validEffort`, not `validModel`'s byte-class grammar: a permission mode is
  a fixed vocabulary (the five NON-ESCALATING modes claude accepts in band,
  measured live by #2041), where a model identifier is not — claude in fact
  accepts a sixth, `bypassPermissions`, in band as of #2066, but that count is
  a **wire** decision following from the two exclusions below, not from what
  claude will parse. `{default, acceptEdits, plan, auto, dontAsk}`; `""` and
  `bypassPermissions` are both deliberately excluded — `""` because an
  explicit empty string names no posture (see the payload doc),
  `bypassPermissions` because that posture must keep exactly one spelling on
  the wire (`yolo: true`), which is the ticket's whole privilege-escalation
  bound and is untouched by #2066: the daemon now *delivers* the escalation
  in band once accepted as `yolo:true`, but this validator still refuses it
  as a mode string outright, so a mobile client still has exactly one
  spelling to send. Carries the same direction hazard `validEffort`
  already flags: a closed enum refuses inbound anything claude adds later,
  so widening it needs a fresh live measurement, not a hunch.
- **`settingsReplyError(ctx, s, inReplyTo, code, message, retryable)`** — a
  third near-identical copy of the `snapshotReplyError` /
  `debugBundleReplyError` shape (marshal `protocol.ErrorPayload` →
  `Envelope{Type: TypeError, InReplyTo}` → `forwardEnvelope`); the established
  per-handler-owns-its-helper posture, not extracted into a shared helper.
  `message` is always a fixed constant, including
  `msgSettingsModelNotOffered` and the shared `msgModelListUnavailable` — never
  attacker-influenced bytes.

**YOLO fail-safe (the primary asset).** Three deterministic layers, no
stochastic component: (1) the `*bool` presence contract — an absent `yolo`
decodes to `nil` and `UpdateSettings` leaves the stored value untouched; (2) a
malformed `yolo` value fails the step-2 payload type-decode before the seam is
ever touched; (3) only an explicit `"yolo":true` sets `*true`, and the whole
apply is one atomic `saveLocked` (#840) — no partial-parse path can reach
`YOLO=*true`. Belt-and-suspenders is different fabric here: the safe default
is pointer-nil semantics (code) and the corruption guard is `json.Unmarshal`
strictness (code), not a second stochastic check.

**The relay's mode+yolo conflict rule is deliberately stricter than the
primitive it feeds, and proving that needs the pair the primitive would
accept (#1687).** `sessions.validatePermissionUpdate` refuses only a
*contradicting* mode+yolo pair; this handler's conflict check refuses **any**
frame carrying both, agreeing or not. The two test rows that pin this as the
relay's *own* rule, not an inherited one, are `mode + yolo:false` and
`mode + yolo:true` — the second is the pair the inner layer would have
accepted, so only it proves the relay is stricter rather than redundant.
Without that row every reject here could be (mis)read as the pool's check
surfacing one layer out.

**A hand-maintained 1:1 seam mirror (`SettingsUpdate` → `settingsUpdaterAdapter`
→ `sessions.SettingsUpdate`) fails silently on a dropped field, and the
happy path won't reliably catch it.** A copy that forgets `PermissionMode`
turns the update into a no-op the pool accepts with a `nil` error — nothing
observably wrong unless the test asserts the *stored* value. The assertion
that kills that mutant unconditionally is on a **rejected** mode: the pool
validates a posture only when the update actually names one, so a non-nil
`ErrUnsupportedPermissionMode` from a request the handler already validated
is unforgeable evidence the pointer crossed every hop. Prefer a rejection-path
assertion over a stored-value one when pinning that a mirrored field survived
an adapter — it doesn't depend on persistence succeeding to be meaningful.
