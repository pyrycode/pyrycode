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
   checks a non-empty model against the **session's own agent's** vocabulary
   before it calls `Pool.UpdateSettings` — since #2629, no longer Claude's
   retained list unconditionally. An exact, untruncated `ModelOption.Value`
   match proceeds; complete-menu absence returns `ErrModelNotOffered`, while a
   missing/empty menu, dropped row, or any value-truncated row returns
   `ErrModelVocabularyUnavailable`. The handler maps those to non-retryable
   `protocol.malformed` and retryable `model_list.unavailable`, respectively.
   `nil` error → `session_settings_updated`; `ErrSessionUnknown` →
   `session.not_found`; any remaining error → `server.binary_offline`. All
   rejection messages and event names are fixed and carry no requested or
   published model value. Since #2463, `settingsUpdaterAdapter.UpdateSettings`
   itself composes two pool writes — a live session first, then a dormant
   registry entry — so a hit on either still yields `nil`/`session_settings_updated`,
   and `session.not_found` now also covers a dormant session whose frame named
   `yolo` or `permission_mode`. See [`Pool.UpdateDormantSettings`](sessions-package-key-types-pool-updatesettings.md#pool-updatedormantsettings-2463).

   **Since #2629, a non-empty effort is checked the same way, against the
   model the session will run after this update** — the update's own `model`
   when the frame sets one (including `""`, which has no entry and falls
   straight to the fallback below), otherwise the session's currently stored
   model (read live-then-dormant, the same order the model check's existence
   probe uses). The check is `validateEffortVocabulary(list, have, model,
   effort)` in `cmd/pyry`: an exact `EffortLevels` membership match on the
   model's own entry passes; an entry that advertises no levels accepts none;
   and when the model has **no** entry for its agent — a version no longer
   listed, an empty model, or no vocabulary retained yet — the daemon falls
   back to the old closed set `{low, medium, high, xhigh, max}`. **Since #2646,
   `validateEffortVocabulary` is a behaviour-identical rewrite: it is now
   membership in `effortLevelsFor(list, have, model)`, the same function
   `settingsUpdaterAdapter.Capabilities` calls to build the `effort_levels`
   entry of the [per-session capability list](protocol-package-types-session-settings-read-payloads.md)
   a `multi_agent` client reads.** The reported list and this check are
   therefore one function rather than two values a test merely pins together
   — a future change to the fallback or the truncation rule cannot update one
   without the other. A refused
   level returns the new sentinel `relay.ErrEffortNotOffered`, mapped to the
   same non-retryable `protocol.malformed` / `msgSettingsMalformed` reply the
   closed set gave, with the whole frame changing nothing — never
   `model_list.unavailable`, since there is no separate "list might still
   arrive" case for effort the way there is for a model absent from an
   incomplete menu. Model is checked before effort, so a frame naming both an
   unoffered model and an effort answers the model's reply. This is how a
   Codex session's `ultra` is accepted — Codex's own entries advertise it —
   while the identical string on a Claude session, whose entries do not, is
   refused; and how a Codex family alias (e.g. `luna`) is accepted as a
   *model* on a Codex session and refused as not-offered on a Claude one.

   **A `TruncatedFields` entry naming `effort_levels` makes that row's level
   list inconclusive, not proof of absence.** `validateEffortVocabulary`
   treats a cut `effort_levels` the same way the model check already treats a
   cut `value`: it falls through to the fallback set instead of refusing on a
   partial list, because a level missing from a truncated row may simply be
   the one that got cut.

   **The session's own agent is resolved via the new
   `(*sessions.Pool).HarnessFor(id)`**, which replaces the old `requireKnownSession` existence
   probe: one `RLock`, live session first, then the dormant registry entry,
   `ErrSessionNotFound` on a miss in both — checked **before** any vocabulary
   read, for an effort-only frame too, so an unknown id still answers
   `session.not_found` without learning which agent a session runs or
   probing vocabulary completeness. See [`Pool.HarnessFor`
   (#2629)](sessions-package-key-types-pool-settingsfor.md#poolharnessfor-2629).

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
  when no exact untruncated `ModelOption.Value` row is offered.

  **`validEffort` no longer carries this hazard (#2629): it was widened from
  a closed enum to a grammar precisely so a level like Codex's `ultra` could
  reach the per-model check below instead of being refused at the wire
  before that check ever ran.** The direction-hazard argument that used to
  justify leaving it closed — "there is no bracketed effort level to admit
  yet, and widening against a hypothetical is not evidence-based" — held
  only while every agent on this wire was Claude; once #2627 gave Codex its
  own advertised levels, refusing an unlisted-in-Claude's-five level stopped
  being conservative and started being wrong for a different agent's session.
  `validPermissionMode` (below) still carries the identical hazard `validModel`
  and the old `validEffort` did, because a permission mode genuinely is one
  closed vocabulary shared by every agent this daemon can run — there is no
  per-agent posture list to check it against instead.
- **`validEffort(e string) bool`** — since #2629, a **grammar** (`""`, or a
  value up to 32 bytes whose first byte is `[a-z0-9]` and whose remaining
  bytes are `[a-z0-9_-]`), not a closed enum — the untrusted-boundary shape
  check only, mirroring `validModel`'s posture. The 32-byte bound is the
  Codex level alphabet's own bound and `streamsup.maxModelEffortLevel`.
  `TestValidEffort_ByteSetIsClosed` walks all 256 byte values at both
  positions rather than by example, the same discipline
  `TestValidModel_ByteSetIsClosed` uses — a leading `-` or `_` here could pose as an argv
  flag the same way an unchecked model value could. Whether a well-shaped
  level is *offered* is the per-model membership check above
  (`ErrEffortNotOffered`), not relay's job. Before #2629 this was the closed enum `{low, medium, high, xhigh,
  max}`, matching `cmd/pyry/agent_run.go`'s `validEfforts` — that set is the
  **dispatcher's** own path (`pyry agent-run`) and stays a closed set by the
  ticket's instruction; it and this grammar are unrelated vocabularies that
  happened to read alike before this ticket, and the closed set is what
  refused Codex's `ultra` outright before any per-model check could run.
- **`validPermissionMode(mode string) bool`** (#1687) — a closed enum, unlike
  `validModel`'s byte-class grammar or the post-#2629 `validEffort` grammar:
  a permission mode is a fixed vocabulary (the five NON-ESCALATING modes claude accepts in band,
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
  spelling to send. Carries the direction hazard `validEffort` carried before
  #2629 and `validModel` still carries: a closed enum refuses inbound
  anything claude adds later, so widening it needs a fresh live measurement,
  not a hunch. `validEffort` escaped this hazard by becoming a per-agent
  membership check instead of a fixed guess; `validPermissionMode` cannot,
  because posture is one vocabulary shared by every agent, not a per-agent
  advertised list.
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
