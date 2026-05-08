# ADR 021 — `pyry pair` orders all loads before token mint, save before render

## Status

Accepted (#213).

## Context

`pyry pair` composes seven primitives — `config.Load`, `devices.Load`, `identity.LoadOrCreate`, `crypto/rand.Read`, `devices.Registry.Add`, `Registry.Save`, `pair.Render` — into a single CLI verb. Several of those steps can fail with I/O errors (missing HOME, permission denied, corrupt JSON), and one of them (`Render`) writes the **plaintext device-token** to `os.Stdout`.

The token is a bearer credential. Once it escapes the process, the only on-disk record that lets the daemon authenticate it later is the row appended by `Registry.Save`. Any execution order that prints a token without first persisting its hash creates a working-payload-but-unauthenticatable failure mode that is silent at the QR-rendering site and fatal much later when the phone tries to connect.

The verb has no daemon, no goroutines, no `context.Context` — it's strictly sequential, so the only design knob is **call ordering**.

## Decision

The verb runs in this exact order, with the named exit-code mapping:

```
1. parsePairArgs(args)                                   → exit 2 on parse error
2. config.Load(~/.pyry/config.json)                      → exit 1 on I/O / parse error
3. resolveRelay(flag, cfg)                               → exit 2 if empty
4. devices.Load(~/.pyry/<name>/devices.json)             → exit 1 on I/O error
5. identity.LoadOrCreate(~/.pyry/<name>/server-id)       → exit 1 on I/O error
6. crypto/rand.Read(32 bytes) → hex.EncodeToString       → exit 1 on rng error
7. HashToken(plain)
8. resolve deviceName (--name OR "device-" + hash[:8])
9. registry.Add(Device{TokenHash, Name, PairedAt})
10. registry.Save(devicesPath)                           → exit 1 on I/O error
11. pair.Render(payload, os.Stdout)                      → exit 1 on write error
```

Two structural invariants follow:

1. **Steps 1–5 (all loads + relay resolution) precede step 6 (token mint).** A misconfigured relay, a missing/corrupt `server-id`, or a corrupt `devices.json` fails before `crypto/rand` is called.
2. **Step 10 (Save) precedes step 11 (Render).** If `Save` returns an error, `Render` is never called, and the plaintext token never leaves the process.

## Rationale

### Why all loads before mint

The minted plaintext token is the most sensitive byte sequence the verb ever holds. Three of the four pre-mint failure modes (relay empty, server-id corrupt, devices.json corrupt) are operator-fixable and reproducible — failing fast lets the user fix the underlying state and re-run, with no token ever generated. The fourth (`config.Load` parse error) is the same shape.

Inverting (mint first, then load) would be merely wasteful for the relay/config cases — but for `identity.LoadOrCreate` it would mean a token is held in process RAM during the syscall window of a parse-rejected `server-id` file. That window is short, but the discipline is "the plaintext token's lifetime is the shortest closed interval consistent with rendering it." Pre-mint loads contract that interval to its minimum.

### Why save before render

If `Render` runs before `Save` and then `Save` fails, the user has a fully-functional QR symbol on screen and a working paste-fallback string — but no on-disk row. The phone scans, presents the token over WS, the daemon's auth path computes `HashToken(presented)`, scans `Registry.devices`, finds nothing, and returns `auth.invalid_token`. The user has no way to reconcile "I just paired" with "the daemon rejects me" — the failure happens at a different time, on a different device, with no causal log line linking them.

If `Save` runs before `Render` and then `Save` fails, the token never reached stdout. The user sees a non-zero exit and an error, mints a new token next try, and the orphaned in-memory `Device{TokenHash}` is dropped with the process — an entirely benign trace.

The Save-first ordering converts a silent-at-render-time, fatal-at-connect-time failure into a loud-at-render-time, recoverable failure.

### Why no `context.Context`

Bounded by filesystem I/O and a ~10ms qrterminal render. Adding ctx-cancel plumbing for that window would be ceremony without value; the user can `Ctrl-C` between steps and the kernel handles teardown.

### Why no logger

The verb has nothing to log that isn't either (a) a bearer token (forbidden by the package contract — see [features/pair-package.md](../features/pair-package.md) § "Token visibility") or (b) already covered by stderr human strings + wrapped errors at the `main` boundary. A `*slog.Logger` would only invite future-maintainer drift toward "let me just log the payload for debugging." Structurally absent is the only safe shape.

## Alternatives Considered

### A. Render first, save second

Already rejected above — silent-at-pair, fatal-at-connect.

### B. Mint first, then load (operator-friendly: token in hand before any I/O)

Wastes a token (and an `crypto/rand.Read`) on every config / relay / devices-corruption error; violates the "shortest-closed-interval" discipline above; provides no UX benefit (the user still has to fix the underlying problem before re-running).

### C. Two-phase commit: write a `.pending` row, render, then promote on phone-side ACK

Materially better against the "two concurrent `pyry pair` invocations race at Save" failure mode (see [features/pyry-pair-command.md](../features/pyry-pair-command.md) § "Race between two `pyry pair` invocations") but requires a daemon-side ACK channel that doesn't exist yet, plus garbage-collection of un-promoted `.pending` rows. Complexity vastly exceeds #213's scope; the property "no token escapes if Save fails" is maintained without two-phase commit by Save-before-Render. Revisit if the concurrent-invocation race is observed in practice.

### D. Bundle Save + Render into a transactional helper in `internal/devices`

Would couple `internal/devices` to `internal/pair` (it currently knows nothing about rendering, payloads, or stdout). The composition is the verb's job, not the storage primitive's.

## Consequences

- **Failure modes are linear and operator-recoverable.** A corrupt `server-id` or `devices.json` fails before any token is generated. A failed `Save` fails before any token is rendered. A failed `Render` (broken stdout pipe) leaves a row in `devices.json` that the user can either accept (the token is in their shell history if they piped to a file) or revoke via a future `pyry pair revoke <name>` — the on-disk state is consistent, just unused.
- **The order is enforced structurally, not by a flag or comment.** A future maintainer cannot accidentally re-order the calls without rewriting the function body. A `// TODO: render before save for UX` patch is loud at code review.
- **No retry logic.** Each failure surfaces immediately to the user; the user retries the verb. A new (independent) token is minted on the next run; the orphaned in-memory device from the failed run is dropped with the process. No cleanup obligation, no stale state.
- **The two-concurrent-`pyry pair` race remains.** Each invocation independently follows this ordering, but `Save`'s atomic-rename is whole-file replace — the second invocation drops the first's appended entry. Documented in [features/pyry-pair-command.md](../features/pyry-pair-command.md); fix is `flock` at the `devices.Registry` layer, not in this verb.
- **Future `pair revoke` / `pair list` verbs follow the same template.** Loads first, mutate-or-display second, atomic save before any user-visible side effect.

## Related

- [features/pyry-pair-command.md](../features/pyry-pair-command.md)
- [features/pair-package.md](../features/pair-package.md) § "Token visibility"
- [features/devices-package.md](../features/devices-package.md) — package doc-comment "never log plain"
- [ADR 020](020-devices-registry-snapshot-then-write.md) — `Save`'s snapshot-then-write
- `docs/protocol-mobile.md:60-65` — token format and on-disk hash invariant
- `docs/specs/architecture/213-pair-command.md` — architect's full spec
