# #3196 — Identity-bound daemon admission to Relay

**Status: declared ahead of implementation and production activation.** This
build-time contract changes no executable behaviour. Current daemon admission
stays unchanged until [Core #3197](https://github.com/pyrycode/pyrycode/issues/3197)
and [Relay #167](https://github.com/pyrycode/pyrycode-relay/issues/167) ship and the
operator explicitly enables enforce. This ticket neither enables enforcement nor
claims live rollout verification. Reference publication belongs to the
documentation stage.

## Files read

- `docs/knowledge/INDEX.md`: startup map and documentation ownership.
- `CODING-STYLE.md`: security, error and concurrency conventions for downstream work.
- `docs/knowledge/features/protocol-package.md`: the protocol reference owns shared wire contracts.
- `docs/knowledge/features/relay-package.md`, Headers and Reconnect semantics: current upgrade boundary; immediate-4409 wording needs reconciliation during publication.
- `docs/knowledge/features/transport-package.md`: existing backoff and upgrade-error classification.
- `docs/knowledge/features/development-verification.md`, Protocol boundaries: declared contracts must remain distinguishable from shipped producers.
- `docs/protocol-mobile.md`, Authentication, Connection lifecycle and Security model: current unauthenticated daemon admission, Noise boundary and opaque phone-token-header caveat.
- `internal/relay/connection.go`, `Connect`: existing identity/version/user-agent headers and eight-close threshold selection.
- `internal/transport/wssclient.go`, `Client.Connect` and `Client.realDial`: retry ladder, consecutive-conflict reset and current generic HTTP upgrade rejection.

## Context

Relay #167 needs one shared admission contract before Relay and Core choose
different authentication formats. Admission protects identity-slot reservation
and daemon-originated push eligibility while preserving phone authorization and
Noise encryption. The deferred-admin-token statement in the reference will be
replaced by this declared contract during publication.

Scope is one specification artifact at this path: four acceptance criteria,
approximately 352 written lines, zero exported types, zero consumer migrations
and zero executable state-machine changes. This is larger than #3137's 75 changed
reference lines because it also specifies configuration, lifecycle and rollout.
No feature-branch overlap was found for this path. The security boundary merits
a decision record if the documentation stage maintains one; no additional
decision artifact is a builder deliverable.

## Design

### Wire and exact identity binding

On the initial connection and **every reconnect** to `/v1/server`, a configured
daemon sends exactly one `x-pyrycode-admission-token` header. Missing configuration
omits it. Retries must retain the selected startup credential; they must never
silently omit it after a failed attempt. Existing `x-pyrycode-server`,
`x-pyrycode-version` and `user-agent` headers and identity validation remain.

The token field value is exactly 64 lowercase hexadecimal characters encoding
32 independently cryptographically random bytes. Operators generate production
tokens with a cryptographic random source; repeated example patterns below are
not valid production provisioning. Validate length and alphabet before decoding;
no whitespace, trimming, uppercase conversion or comma-separated list is allowed.
Duplicate token or identity header values are inadmissible to the admission
check, even when identical. A coalesced comma list cannot become one credential
or one identity; never select the first or last value to authenticate it.

Each token is bound to exactly one exact `x-pyrycode-server` value. No identity
normalization, aliasing or cross-identity fallback is permitted. Token equality
is constant-time. A valid token for identity A cannot admit identity B. This
credential is independent of phone pairing tokens and Noise keys, and must never
enter a URL, QR payload, routing envelope or application frame.

### Modes and decision table

Relay modes apply to a startup snapshot:

- **off (default):** preserve existing admission without credential checks.
- **observe:** evaluate admission and record bounded aggregate outcomes; allow
  requests that otherwise satisfy existing validation. A credential provides no
  enforcement guarantee in this mode.
- **enforce:** admit only an exact matching identity/token pair. An absent
  identity in the mapping or an empty accepted-token array admits nobody.

In the table, **proceed** means continue through existing identity/version/header
validation and first-claim-wins conflict handling; it does not promise a successful
connection. Off ignores admission credentials. Observe evaluates duplicate token
or identity values as inadmissible but does not add an enforcement rejection;
existing validation can still reject such a request in off or observe.

| Request / credential condition | off | observe | enforce |
|---|---|---|---|
| Single valid token matching its exact identity | Proceed; unchecked | Proceed; count valid | Admit to existing path |
| Missing token header | Proceed; unchecked | Proceed; count missing | Reject 401 |
| Empty, wrong-length, non-hex, uppercase, whitespace or comma-list token | Proceed; unchecked | Proceed; count malformed | Reject 401 |
| Well-formed unknown token | Proceed; unchecked | Proceed; count unknown | Reject 401 |
| Removed/revoked token, after Relay restart | Proceed; unchecked | Proceed; count unknown | Reject 401 |
| Token accepted only for a different identity | Proceed; unchecked | Proceed; count mismatch | Reject 401 |
| Identity absent from mapping or mapped to an empty array | Proceed; unchecked | Proceed; count unknown | Reject 401 |
| Duplicate token values, equal or different | Proceed; unchecked | Proceed; count malformed | Reject 401 |
| Duplicate identity values, equal or different, or coalesced identity list | Existing validation | Existing validation; count malformed | Reject 401 |
| Missing or malformed identity, even with a known token | Existing validation | Existing validation; count malformed | Reject 401 |

Removed credentials need no persistent tombstone: in the new snapshot they are
unknown. Outcomes are a finite vocabulary, such as valid, missing, malformed,
unknown and mismatch, with no token or claimed identity labels. Observe counts
are aggregate; operators correlate private provisioning and controlled reconnects
to confirm intended identities without publishing per-identity metrics.

### Uniform rejection and ordering

In enforce, admission is checked **before** WebSocket acceptance, registration,
claim-conflict handling, grace reservation or push eligibility. Every rejected
admission attempt returns the same response:

```http
HTTP/1.1 401 Unauthorized
Cache-Control: no-store

daemon admission rejected
```

The body bytes are exactly `daemon admission rejected\n`, ending in one LF byte.
No identity, supplied value or rejection detail is echoed. A rejection leaves no
upgraded connection, registry claim, grace reservation or push access, including
when the claimed identity is already occupied. There is no new WebSocket close
code or application error frame. Only a claimant admitted by enforce can reach
the existing first-claim-wins/4409 path. Other existing validation remains in
force for requests that pass admission.

### Startup configuration

The daemon's existing private `~/.pyry/config.json` gains optional
`relay_admission_tokens`, an object mapping exact server-id to one token. Each
instance selects the entry for its own loaded server-id. An absent object or
absent selected entry omits the header. A present map requires owner-only storage:
the file is `0600`, its parent is `0700`, and ownership is the daemon user's.
Malformed supplied configuration or insecure storage fails startup with static,
value-free validation errors. Never include an offending value or parser excerpt.
No flag or environment override is introduced for this secret.

Relay retains its startup environment registry:

| Variable | Contract |
|---|---|
| `PYRYCODE_RELAY_DAEMON_ADMISSION_MODE` | `off`, `observe` or `enforce`; unset defaults to `off`. Other supplied values fail startup. |
| `PYRYCODE_RELAY_DAEMON_ADMISSION_CREDENTIALS` | JSON object mapping exact server-ids to arrays of accepted tokens. Unset means empty in off/observe; enforce requires an explicit mapping. `{}` is valid and denies all in enforce. |

Supplied malformed configuration fails Relay startup in **every mode**, including
off; disabling request checks does not disable configuration validation. Tokens
must have the wire shape. A token assigned to multiple identities is invalid
configuration. Errors are static and value-free. Accepted arrays permit old/new
overlap for the same identity. Configuration is loaded at startup and changes
apply only on restart: there is no credential service or hot reload.

### Synthetic configuration and decisions

**SYNTHETIC EXAMPLES ONLY — never deploy these predictable credentials.** The
two server-ids are illustrative exact identities; token values are visibly
repeated patterns, each 64 lowercase hex characters. All real tokens must be
independently cryptographically random and distinct.

Daemon config example (A selects its new token; B selects its own token):

```json
{
  "relay_admission_tokens": {
    "11111111-1111-4111-8111-111111111111": "2222222222222222222222222222222222222222222222222222222222222222",
    "33333333-3333-4333-8333-333333333333": "3333333333333333333333333333333333333333333333333333333333333333"
  }
}
```

Relay credentials JSON example (A accepts old and new during overlap):

```json
{
  "11111111-1111-4111-8111-111111111111": [
    "1111111111111111111111111111111111111111111111111111111111111111",
    "2222222222222222222222222222222222222222222222222222222222222222"
  ],
  "33333333-3333-4333-8333-333333333333": [
    "3333333333333333333333333333333333333333333333333333333333333333"
  ]
}
```

With this snapshot, A/old, A/new and B/own pass enforce admission. B/A-new fails
with the uniform 401. After removing A/old and restarting Relay, A/old fails
while A/new and B/own still pass. In observe all these requests proceed through
existing validation and only their bounded outcomes differ; in off none of these
credentials are checked. Neither mode protects A or B from anonymous claims.

## Concurrency model

This artifact introduces no goroutines, locks or mutable runtime state. Companion
implementations retain existing connection shutdown ownership and load immutable
startup configuration. Admission must precede claim/push side effects rather than
create state and undo it on failure.

## State transitions and identity reuse

None introduced by this ticket: it writes a contract without executable lifecycle
changes or race tests. The following declared scenarios are obligations for
Core #3197 and Relay #167, not tests executed or added here.

| Event | Required result |
|---|---|
| Initial connect or repeated reconnect | Select the same loaded identity/token; send exactly one configured header on every attempt. |
| First HTTP 401, initially or after successful service | Stop retries for that relay leg immediately; keep local daemon service running. |
| Correct configuration without daemon restart | Terminal admission failure remains latched; no automatic retry. |
| Correct configuration and restart daemon | Load the new snapshot and retry admission. |
| Repeated 4409 or a non-4409 cycle between conflicts | Preserve the eight-consecutive-close threshold and reset rule. |
| Add new token for same identity and restart Relay | Old and new tokens both admit during overlap; existing connections close on restart. |
| Update selected daemon token and restart that daemon | Reconnect with the new token; preserve existing claim-conflict behaviour. |
| Remove old token or entire identity mapping and restart Relay | Existing connections close; claims/grace state is discarded; revoked holders have no push access in enforce. |
| Edit mapping before Relay restart | Old startup snapshot remains authoritative, including old-token acceptance and existing connections. |
| Explicit rollback and restart Relay in observe/off | Restore unauthenticated admission; revoke enforcement guarantees. |

Rotation order is: privately add a fresh token beside the old token for the same
identity, restart Relay, update the daemon's selected token and restart that
daemon, then remove the old Relay token and restart Relay again. Revocation is
removal of a token or identity mapping followed by Relay restart. It is not
immediate on editing a file/environment; restart closes all existing connections
and removes their claims and grace state before the new snapshot admits requests.

## Error handling

The first HTTP 401 received for `/v1/server` is terminal for automatic retries of
that daemon relay leg, whether during initial connection or reconnect. Report a
static admission-failure diagnostic without raw response/header/credential data.
Keep the daemon available locally; do not exit such that service-manager restarts
produce another authentication loop. The operator corrects private configuration
and restarts the daemon to retry. Authentication rejection has no conflict counter
and must not reuse the 4409 counter.

Network and HTTP 5xx failures retain existing backoff: 1/2/4/8/16/30-second cap,
with ±20% jitter and attempt reset after a successful connection lasting at least
60 seconds. Preserve 4409's eight-consecutive-close threshold on both dial and
established-connection close paths. Any cycle ending without a 4409 resets the
consecutive-conflict counter; successful upgrade alone does not reset it. This
counter reset is separate from the uptime-based backoff reset.

Daemon admission is an HTTP upgrade decision, independent of phone authentication
on `/v1/client` and the phone's Noise/paired-device authorization. Phone close
4401 and sealed `auth.invalid_token` keep their existing meanings and handling.

## Threat boundary and secret hygiene

Only enforce prevents anonymous identity reservation and anonymous
daemon-originated push requests. A stolen admission token permits claiming its
bound identity and abusing push. It supplies neither the daemon's Noise private
key nor phone authorization; it cannot impersonate the Noise responder to an
already-paired phone or decrypt application content by itself.

Relay sees routing/traffic metadata, admission credentials and the existing opaque
phone-token header. The phone must still send `x-pyrycode-token` today: Relay
requires it and receives it in memory without parsing, comparing, logging or
forwarding it. Phone token validation occurs in encrypted Noise early-data.
Admission does not remove this visibility caveat. Noise protects application
content; relay-directed push requests already sit outside that encrypted content.

Relay compromise can deny service and capture admission tokens; admission adds
no protection against a malicious Relay and does not give it Noise decryption
keys. Preserve phone authentication, pairing, endpoints and Noise encryption.
Prompt injection, endpoint compromise, static-key compromise, metadata privacy
and general rate-limiting remain their existing threat boundaries.

Real credentials, credential maps and raw request headers must be absent from
status, logs, errors, repository files and public metric labels. Only explicitly
synthetic examples belong in this artifact. Telemetry uses bounded aggregate
outcomes rather than tokens or claimed identities. Protect private daemon files
and Relay environment provisioning; TLS protects the admission header on the
network, while Relay and any trusted TLS-terminating proxy can see it.

## Operator rollout checklist (pending both implementations)

1. Ship Core #3197 and Relay #167 with Relay **off**; confirm both implementations
   are present before proceeding. This specification is not activation evidence.
2. Privately provision independently random, distinct tokens bound to each exact
   identity; configure owner-only daemon storage and Relay's startup registry.
3. Configure Relay **observe** and restart configured daemons and Relay so both
   load their startup snapshots.
4. Confirm every intended identity authenticates through controlled reconnects
   and bounded aggregate outcomes; investigate all failures before enforcement.
5. Explicitly select **enforce** and restart Relay; verify authorized reconnects
   and anonymous rejection without upgrade, claims, grace reservation or push.
   Recovery from a daemon's 401 requires correction and operator daemon restart.

Returning to observe/off is an explicit operator rollback restoring
unauthenticated admission. No checklist step was performed in this ticket.

## Testing strategy

Review the contract against all four acceptance criteria and technical notes;
check every decision-table mode, rejection side effect, synthetic token length,
identity binding, rotation sequence and documentation handoff. Run `git diff
--check`, `go vet ./...` and build `./cmd/pyry` with output outside the worktree.
No Go package is touched, so package race tests and executable test changes are
inapplicable. The verifier owns the full-module gate; downstream tickets own
executable admission tests and operators own rollout verification.

## Open questions

None: wire, configuration, failure handling and rollout choices are settled by
the ticket. Companion tickets own implementation structure and test names.

## Documentation handoff

Pending for the documentation stage:

- In `docs/protocol-mobile.md`, publish this contract under **Authentication →
  Binary → relay**; reconcile **Connection lifecycle → Binary/Reconnect** and
  **Security model → Server-id race/Relay operator MITM**. Replace the
  deferred-admin-token statement with this declared admission contract. State
  that current admission stays unchanged until #3197 and Relay #167 ship and the
  operator enables enforce. Include configuration, decision-table examples,
  rotation/revocation and the ordered operator checklist; preserve the
  phone-token-header caveat.
- In `docs/knowledge/features/relay-package.md`, add **Daemon admission security
  boundary (declared)** linking the authoritative reference. Describe
  identity-slot/push protection, ciphertext and secret visibility, restart-based
  revocation and downstream ownership. Keep current behaviour separate from the
  declared target; reconcile the existing reconnect description with the
  already-shipped eight-consecutive-4409 rule. No Relay-repository documentation
  edits here; Relay #167 owns its threat-model update.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] Admission binds one exact identity/token pair before upgrade
  or state creation; observe/off explicitly promise no protection. Duplicate
  values cannot select a credential in enforce.
- [Tokens, secrets, credentials] Independent 32-byte cryptographic randomness,
  owner-only private storage, value-free errors and restart-based overlap/removal
  cover generation, exposure and revocation. No real secrets are in this artifact.
- [File operations] OUT OF SCOPE: this contract performs no secret file I/O.
  Core #3197 owns safe configuration loading, permission/ownership validation and
  existing persistence handling, including symlink/check-then-use scrutiny.
- [Subprocesses] No subprocess or secret environment override is introduced;
  existing service managers must not turn terminal 401 into a restart loop.
- [Cryptography] Constant-time token equality is required; admission reuses no
  Noise key or phone token and does not change Noise primitives or nonces.
- [Network and I/O] The 64-character shape and single-value checks precede
  admission. OUT OF SCOPE: executable HTTP limits, timeouts and resource controls
  remain implementation review obligations in Relay #167; TLS remains unchanged.
- [Errors, logs, telemetry] Uniform no-store 401 and static daemon diagnostics
  exclude input and response details. Aggregate outcomes exclude identities and
  credentials, including in observe. Revoked and unknown share one outcome.
- [Concurrency] No new runtime state here. Startup snapshots and Relay restart
  define revocation; reject-before-side-effect avoids transient reservations.
  Downstream tickets must prove these lifecycle boundaries with their own tests.
- [Threat model] Enforce addresses server-id race and anonymous push. Relay
  compromise still exposes credentials and metadata and can deny service; Noise
  content protection and the phone-token-header caveat remain. Relay #167 owns
  its threat-model publication; unrelated existing threats remain out of scope.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-11
