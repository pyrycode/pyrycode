# Spec #941 — Retire the no-handshake v1-transport e2e tests

**Ticket:** [#941](https://github.com/pyrycode/pyrycode/issues/941) · **Size:** S · **Security-sensitive:** No (test-only; production v1 branch / `authGate` / `assistant_turn.go` / `internal/dispatch` / `relay/auth.go` untouched — their removal is #913).

Child A of the #939 split. Sibling #942 (Noise-migrate the phone-dependent files) is `blockedBy` this ticket; both are prerequisites for #913 (v1 relay-branch removal).

## Context

The legacy v1 relay dispatch leg (`PYRY_MOBILE_V2=0`, `/v1/server`) is being retired (#913): no shipping client speaks v1, and ADR 025's 2026-06-22 amendment removed the old-app support it existed for. The v1-transport e2e tests must retire **before** the production branch is removed, or a v1-handshaking phone would hang after the branch is gone.

This ticket handles the subset needing **no phone-handshake authoring**:
- **straight deletes** where a v2 e2e test already asserts the behavior,
- **route-swaps** of transport-agnostic binary-leg tests to `/v2/server`,
- **relocation** of the shared harness helpers these files happen to host (so deleting the files doesn't break the package build).

The phone-dependent migrations (`per_conversation_eviction`, `respawn_after_eviction`, `register_push_token`) are #942's job and are **carved out** here — they still consume helpers relocated by this ticket.

This is a mechanical sweep: **1 production-source file touched** (`harness.go`, the relocation target); everything else is `_test.go` deletes/edits. Net LOC is deletion-dominant (~400 lines removed, ~60 relocated).

## Files to read first

- `internal/e2e/relay_test.go` — the relocation SOURCE for `shortHome` / `readPersistedServerID` / `relayTestLogger` / `recvEnvelope` (lines 20–80), plus the two route-swap tests `TestRelay_4409` (82–115) and `TestRelay_1011` (117–163). This file is **reduced, not deleted**.
- `internal/e2e/relay_auth_test.go` — `TestRelay_AuthReject_4401` (delete) + `mustJSON` (108–115, relocate). After both, the file is empty → **delete it**.
- `internal/e2e/relay_v2_daemon_test.go:34–38, 312–381` — `TestRelayV2_Daemon` dispatch + the `testV2DaemonDisabledDoesNotEngageV2` sub-test to reduce (AC5). Note lines 316–318 host the `TestRelay_AuthReject_4401` cross-ref comment that retires with it. Reuse `readInnerFrame` / `sendNoiseInit` / `buildHelloEarly` / `driveHandshakeToOpenDaemon` (40–74) already in this file — **no new handshake authoring**.
- `internal/e2e/relay_base_url_test.go` (whole, 37 lines) — AC4 candidate for drop.
- `internal/relay/connection.go:146–165` — `resolveDialURL`: production appends **`/v1/server`** for a bare base URL (`:162`), untouched by this ticket. This is why AC4 resolves to *drop*, not *re-target to `/v2/server`* (see Design AC4).
- `internal/relay/connection_test.go:580–629` — `TestResolveDialURL` already asserts server-path append/passthrough, incl. explicit-v2 passthrough (`:597`). This is the existing coverage that lets AC4 drop the e2e test.
- `internal/e2e/internal/fakerelay/fakerelay.go:130–136` — `/v2/server` shares `handleBinary` with `/v1/server`; `RejectNextBinaryWith4409` (508), `WaitBinary` (527), `ForceCloseBinary` (557) key on `serverID`, not path → the 4409/1011 route-swap is transport-agnostic. **Preserved plumbing — do not touch.**
- `internal/e2e/harness.go:1` (build tag `//go:build e2e || e2e_install`) and `:307–321` (`StartRotationWithRelay`, whose `/v1/server` example comment is an AC6 carve-out). This is the relocation TARGET.
- `CODING-STYLE.md` — table-driven, stdlib `testing` only, `log/slog`; the relocated helpers already conform, keep them verbatim.
- Memory context (not in codegraph): the build-tag gotcha — `internal/e2e` **never compiles under pure `-tags e2e_install`** on main (`harness.go` references `safeBuffer`, an `e2e`-only symbol). The real compile checks are `-tags e2e` **and** `-tags "e2e e2e_install"`. Never verify with bare `-tags e2e_install`.

## Design

Package-internal, test-only. No new types, no interfaces, no concurrency, no production change. Five work items, one per acceptance criterion group.

### AC1 — Delete-class removals (no coverage loss)

`git rm` four files:

| File | Why safe to delete | Surviving coverage |
|---|---|---|
| `relay_roundtrip_test.go` | envelope roundtrip | `relay_two_phone_structured_test.go` + `relay_v2_*` |
| `relay_send_message_test.go` | `send_message` → ack + PTY delivery | `relay_two_phone_structured_test.go` + `relay_v2_*` |
| `relay_assistant_turn_test.go` | asserted the v1 coarse `message` fan-out, **removed from the v2 path in #699** and replaced by the capability-gated structured stream | `relay_two_phone_structured_test.go` |
| `relay_auth_test.go` | `TestRelay_AuthReject_4401` (4401 close over v1) | v2 twin `bad_token_4401` / `testV2BadToken` in `relay_v2_handshake_test.go` (asserts the same 4401 over Noise) |

- `relay_roundtrip_test.go` also defines a **file-local** helper `roundTripTS` (used only inside that file) — it dies with the file, no dangling reference.
- `relay_auth_test.go` is deleted **whole**: after AC2 relocates `mustJSON`, only the package clause + imports would remain.

### AC2 — Relocate shared helpers into `harness.go` (not remove)

Move these **five** definitions verbatim into `harness.go`, keeping them exactly as written (they already follow house style):

| Helper | From | Signature |
|---|---|---|
| `shortHome` | `relay_test.go:25` | `func shortHome(t *testing.T) string` |
| `readPersistedServerID` | `relay_test.go:35` | `func readPersistedServerID(t *testing.T, home string) string` |
| `relayTestLogger` | `relay_test.go:50` | `func relayTestLogger() *slog.Logger` |
| `recvEnvelope` | `relay_test.go:63` | `func recvEnvelope(t *testing.T, phone *fakephone.Client, want string, timeout time.Duration) protocol.Envelope` |
| `mustJSON` | `relay_auth_test.go:108` | `func mustJSON(t *testing.T, v any) json.RawMessage` |

**Why relocate rather than delete** — every one keeps live callers among surviving files, so this is not dead code:
- `mustJSON` → ~13 surviving files (`relay_v2_*`, `relay_two_phone_structured`, and #942 carve-outs). `shortHome` / `readPersistedServerID` / `relayTestLogger` → nearly every relay test.
- `recvEnvelope` → after the AC1 deletes, its remaining caller is `per_conversation_eviction_test.go` (a #942 carve-out that stays until #942 migrates it). Because that caller survives, **no `staticcheck` U1000 unused-func flag** appears. This is exactly why the Technical Notes require *relocate*, not delete.

**Same-package move → zero call-site edits.** All callers are in package `e2e`; moving a definition within the package leaves every reference compiling.

**Build-tag correctness (the gotcha).** `harness.go` is `//go:build e2e || e2e_install`; the source files are `//go:build e2e`. None of the five helpers references an `e2e`-only symbol (they use only `os`/`filepath`/`strings`/`time`/`slog`/`encoding/json`/`testing` plus the always-available `fakephone` and `protocol` packages), so hosting them under the wider tag is safe. `harness.go` must gain whatever imports these introduce (`log/slog`, `encoding/json`, `fakephone`, `protocol`, …) and `relay_test.go` must drop the ones its two remaining tests no longer use (`log/slog`, `os`, `path/filepath`, `fakephone`, `protocol`). Let `gofmt`/`goimports` settle the import sets; do not hand-align.

Verify compilation under **both** `-tags e2e` and `-tags "e2e e2e_install"`. Do **not** verify with bare `-tags e2e_install` (already broken on main — pre-existing, not this ticket's regression).

### AC3 — Route-swap `TestRelay_4409` / `TestRelay_1011` to `/v2/server`

Both live in `relay_test.go` and stay there. For each:
- Drop `"PYRY_MOBILE_V2=0"` from the `extraEnv` slice (leaving `"PYRY_ALLOW_INSECURE_RELAY=1"`).
- Change the relay flag from `fr.URL()+"/v1/server"` → `fr.URL()+"/v2/server"`.

No other change. These exercise the **binary WebSocket close-code path** (4409 → clean daemon exit + "server-id conflict" log; 1011 → transport reconnect-absorb, daemon stays alive), not the phone handshake. `fakerelay` routes `/v2/server` through the same `handleBinary`, and its `RejectNextBinaryWith4409` / `ForceCloseBinary` / `WaitBinary` hooks key on `serverID` — the behavior is identical over v2. No phone dial, so nothing else moves.

### AC4 — Drop `relay_base_url_test.go`

**Decision: delete the file.** Justification (AC4's second clause, "dropped if an existing test already asserts the `/v2/server` server-path append"):

1. Production `resolveDialURL` appends **`/v1/server`** for a bare base URL (`connection.go:162`), and this ticket must not touch production. So the bare-URL end-to-end test *cannot* be "re-targeted to `/v2/server`" — a bare URL structurally appends `/v1/server` until #913 changes production. Keeping the test would leave a retired file **selecting the v1 leg** (via the production append default), which AC6 forbids.
2. The append/passthrough logic it guards is already asserted deterministically at the unit level by `TestResolveDialURL` (`connection_test.go:585–629`): base-no-path append (`:594`), root-slash append (`:595`), explicit-v1 passthrough (`:596`), **explicit-v2 passthrough (`:597`)**, query preservation, scheme errors.

Dropping the redundant e2e test is the only AC6-consistent option and is explicitly sanctioned.

### AC5 — Reduce the v1 sub-test in `relay_v2_daemon_test.go` to "v2 is the default"

The current `testV2DaemonDisabledDoesNotEngageV2` (312–381) sets `PYRY_MOBILE_V2=0` + `/v1/server` and asserts the daemon replies a v1 `hello_ack` (proving v2 *not* engaged). That direction selects the v1 leg. Transform it into the surviving assertion that **an unset switch defaults to v2**:

- Remove `"PYRY_MOBILE_V2=0"` from `extraEnv` (leave the switch **unset**); change the relay flag to `fr.URL()+"/v2/server"`.
- Keep the existing setup (pair → decode `pubKey` → dial phone → `WriteInit`/`sendNoiseInit`). Reuse the in-file helpers — no new handshake code.
- Change the assertion from "`phone.Receive()` returns `hello_ack`, not `noise_resp`" to: read the daemon's first reply via `readInnerFrame` and assert `inner.Type == protocol.TypeNoiseResp` (the v2 manager engaged by default). Completing the full handshake is **not** required — observing the `noise_resp` is sufficient; `driveHandshakeToOpenDaemon` (40–74) already does exactly this read at `:61–64` if the developer prefers to call it and ignore the returned cipher states.
- Rename the sub-test key `"v2_disabled_does_not_engage_v2"` → `"v2_default_engages_v2"` (line 37 `t.Run` + the function name).
- Delete the comment cross-referencing `TestRelay_AuthReject_4401` (316–318) — its referent is gone (AC1).
- Update the `TestRelayV2_Daemon` doc comment (30–33) so the third bullet describes "unset → v2 engaged by default" instead of "switch unset … v1 first-frame auth gate … hello_ack".

The two `PYRY_MOBILE_V2=1` round-trip sub-tests (`testV2DaemonListConversationsRoundTrip`, `testV2DaemonRequestSnapshotRoundTrip`) are untouched — they pin the *explicit-on* behavior; this reduced sub-test uniquely pins the *default*.

### AC6 — Negative guardrail, scoped to operative v1-leg selection

After this ticket, no **retired** file (deleted / route-swapped / reduced) may **select** the v1 leg — no `/v1/server` route dial, `PYRY_MOBILE_V2=0` env, or `ProtocolVersions: ["v1"]`. The AC is about *operative selection*, not incidental marker text in preserved shared plumbing.

Completeness sweep — every current hit of the discriminating markers (`PYRY_MOBILE_V2=0`, `/v1/server`, `"v1"`) across `internal/e2e/`, classified (this reconciles the property-AC against an enumerated worklist):

| File | Disposition | v1 markers after |
|---|---|---|
| `relay_roundtrip_test.go` | **delete** (AC1) | gone |
| `relay_send_message_test.go` | **delete** (AC1) | gone |
| `relay_assistant_turn_test.go` | **delete** (AC1) | gone |
| `relay_auth_test.go` | **delete** (AC1/AC2) | gone |
| `relay_base_url_test.go` | **drop** (AC4) | gone |
| `relay_test.go` | route-swap 4409/1011 (AC3) | **none** |
| `relay_v2_daemon_test.go` | reduce sub-test (AC5) | **none** |
| `harness.go` | relocation TARGET | **carve-out (b)** — `StartRotationWithRelay`'s `/v1/server` example comment stays; its `relayURL` is caller-supplied and #942's phone tests still route through it |
| `internal/fakerelay/fakerelay.go`, `fakerelay_test.go` | preserved harness plumbing (AC7) | **carve-out** — `/v1/server` handler + docs untouched |
| `per_conversation_eviction_test.go` | #942 | **carve-out (a)** |
| `respawn_after_eviction_test.go` | #942 | **carve-out (a)** |
| `register_push_token_test.go` | #942 | **carve-out (a)** |

Do **not** over-clean the carve-outs: touching `StartRotationWithRelay`'s comment or the fakerelay plumbing would break #942's still-v1 phone tests. A literal `grep '/v1/server'` over the touched target (`harness.go`) will still hit — that is expected and correct, not a failure.

### AC7 — Green build + untouched production

- `go build ./...`, `go vet ./...`, `staticcheck ./...`, `go test -race ./...`, and the e2e suite `go test -race -tags e2e ./internal/e2e/...` all green. (Also compile-check `-tags "e2e e2e_install"` per AC2; that config is opt-in with no CI/Makefile target, so a build/vet check suffices — no need to run its install tests.)
- Production is untouched: the v1 branch, `authGate`, `assistant_turn.go`, `internal/dispatch`, `relay/auth.go`, and the fakephone/fakerelay harness plumbing remain present (just less exercised).

## Concurrency model

N/A — test-only, no new goroutines or channels. The relocated `recvEnvelope` retains its existing bounded receive-loop-until-`want` behavior verbatim.

## Error handling

N/A — no new failure modes. The relocated helpers keep their `t.Fatalf` contracts unchanged.

## Testing strategy

The deliverable *is* the test suite; there is no new behavior to test. Verification:

1. `go build -tags e2e ./internal/e2e/...` and `go build -tags "e2e e2e_install" ./internal/e2e/...` both compile (proves the helper relocation kept every surviving file's references resolvable under both real build modes).
2. `staticcheck ./...` clean — specifically no U1000 on the relocated helpers (guaranteed by their surviving callers; `recvEnvelope`'s survivor is `per_conversation_eviction_test.go`).
3. `go test -race -tags e2e -run 'TestRelay_4409|TestRelay_1011|TestRelayV2_Daemon' ./internal/e2e/...` passes — the route-swapped and reduced tests still assert their invariants over `/v2/server`.
4. Full `go test -race -tags e2e ./internal/e2e/...` green.
5. `git grep -nE 'PYRY_MOBILE_V2=0|/v1/server|"v1"' internal/e2e/` shows hits **only** in the AC6 carve-outs (harness.go `StartRotationWithRelay`, fakerelay plumbing, the three #942 files).

**Known unrelated e2e flakes** (do not attribute to this ticket; re-run isolated or accept a PASS): `realclaude` SIGTERM cases, `wssclient -race`. If a `relay_v2_*` or `fakeclaude` test is red on clean `main`, it is out-of-scope pre-existing red (see the #918/#929/#930 stand-in accounting) — attribute before assuming regression.

## Open questions

- **None blocking.** AC4 resolved to *drop* during design (production `resolveDialURL` appends `/v1/server`, and `TestResolveDialURL` covers the append/passthrough at the unit level). AC5's "surviving assertion" resolved to *reduce-and-flip* (unset → `noise_resp`) using existing helpers. Both are settled above; the developer implements as specified.
