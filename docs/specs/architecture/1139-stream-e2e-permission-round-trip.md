# Spec — Stream e2e: permission round-trip (modal_shown / modal_answer / verdict, timeout denies) under the interactive_runner toggle (#1139)

**Size:** S (at the ceiling). Two production-source files touched (`cmd/pyry/main.go` env seam ~10 LOC; `internal/e2e/internal/fakeclaude/main.go` approve rider ~70 LOC). The rest is one new e2e test file + a light fakeclaude unit test. No signature cascade — `runStreamJSON` stays byte-identical, so the sibling stream riders are untouched.

**Security-sensitive** — see `## Security review` at the end. The load-bearing assertion is *timeout → the daemon denies (fail-closed)*, proven by the daemon's own `permbridge` timer, observed both as the fake receiving the daemon's deny verdict and as the phone receiving `modal_dismissed{denied_timeout}`.

---

## Context

This is the final proof of the stream-json interactive permission mechanism. Every leg is already built and unit-tested:

- `internal/permbridge` (#1103) — the pending-approval registry: park a request under `tool_use_id`, hand the blocked caller a `Pending` to `Await`, resolve to allow/deny; a registry-owned timer **always** denies on deadline (fail-closed core).
- `internal/control` `mcp.approve` verb (#1104) — `handleApprove` parks the forwarded request in `permbridge`, calls the surfacer, blocks on `Await`, returns the verdict; `watchApproveConn` denies on caller-disconnect / daemon-shutdown.
- `cmd/pyry/modal_resolve_v2.go` `streamApprovalBridge` (#1080) — `Surface` raises a parked approval as the same `modal_shown` clients already answer; `ResolveStream` resolves the parked completer to allow/deny from a `modal_answer`; `retire` is the guaranteed cleanup + `modal_dismissed` backstop.
- `internal/modalbridge` (#716) — mints the `modal_id` nonce, builds the fixed 4-option / reject-once-default permission payload.
- The stream keystroker guard `noopKeystrokerOrNoop` (#1131) — on the stream path the modal is a `permbridge`-parked completer, not a PTY modal.

What is **missing** is an e2e that runs all of this together, live, against a spawned fakeclaude under `interactive_runner:"stream-json"`, and proves the full loop — including the fail-closed timeout. That is this ticket. The **only new production-shaped code** is a scripted-approval capability in fakeclaude (single-consumer, so it lives here, not in the shared harness ticket #1141) plus a tiny env seam that lets the test shrink the daemon's approval window.

### The one architectural fact that shapes the whole design

The interactive stream runner (`internal/streamsup.Runner`) builds its child argv via `buildArgs(base, firstRun, sessionID)` from `cfg.Args` — the stream-json prefix + `--session-id`. It does **not** inject `--permission-prompt-tool` / `--mcp-config` (that wiring, `permissionArgs` + `writeMCPApproveConfig`, lives only in `cmd/pyry/agent_run.go`'s `pyry agent-run` batch verb, #1106). So under the interactive stream runner **real claude would not spawn `pyry mcp-approve` at all** — the mcp-config isn't passed to it. (See *Open questions* — this is a genuine production follow-up, not a blocker for this e2e.)

Consequence: fakeclaude cannot originate an approval "the way real claude does" (via a config-spawned `pyry mcp-approve` subprocess), because the config isn't there. Instead fakeclaude originates the approval by calling the **exact same client** `pyry mcp-approve` calls: `control.Approve(ctx, socket, ApprovePayload{…})`. This drives the identical daemon surface (`mcp.approve` verb → `permbridge` park → surfacer → `modal_shown` → answer → `ResolveStream` → verdict → deny-on-timeout) that the ticket names as production-under-test. The `pyry mcp-approve` MCP-JSON-RPC framing layer is separately unit-tested (`cmd/pyry/mcp_approve*`); it is not what this e2e is proving.

---

## Files to read first

- `internal/e2e/relay_v2_stream_send_test.go` (all, 218 lines) — the template. The setup (pair → `seedBoundConversation` → `StartStreamInteractiveWithRelay` → `waitBinaryHello` → `fakephone.Dial` → `driveHandshakeToOpenDaemonInteractive` → `send_message` → ack → drain), the id-alignment invariant, and the two-milestone drain loop are all reused verbatim. **Copy this file's shape.**
- `internal/e2e/relay_v2_modal_answer_test.go:60-330` — the PTY sibling. Lift `awaitModalShown` (drain until `modal_shown`, assert `ModalID != ""`), the 4-option assertion, the `modal_answer` send (`protocol.TypeModalAnswer` + `ModalAnswerPayload{ModalID, OptionID, AnswerToken}`), the `modal_dismissed` await, and **the `pair … --allow-remote-permissions` requirement** (line 74-77: without the flag `ResolveAnswer` denies at the device gate).
- `internal/e2e/relay_v2_modal_cancel_test.go` (all, 158 lines) — the deny/no-answer sibling shape (ESC deny keystroke, late-answer no-op). Model the timeout case's tolerance here.
- `internal/e2e/relay_v2_stream_interrupt_test.go` — the rider-style precedent: how a fake behavior is scoped **inside the spec via a rider env** (`PYRY_FAKE_CLAUDE_STREAM_INTERRUPT`) rather than as a standalone harness capability. The approve rider mirrors this exactly.
- `internal/e2e/harness.go:368-442` — `StartStreamInteractiveWithRelay`. Reuse **as-is**; it appends `extraEnv ...string` to the daemon env, which the child inherits via `os.Environ()`. The three rider envs below ride in through `extraEnv`. Do **not** modify this helper.
- `internal/e2e/harness.go:698-711` (`childEnv`) — confirms the child inherits the daemon's env; a rider env set on the daemon reaches fakeclaude.
- `internal/e2e/internal/fakeclaude/main.go:1212-1316` — `runStreamJSON` / `userTurnText` / `writeStreamResponse` / `writeAssistantEcho` / `writeJSONLine`. The approve loop reuses `userTurnText` + `writeJSONLine`; **do not touch `runStreamJSON`'s signature** (siblings depend on it staying byte-identical). Also read `main.go:440-479` (the `main()` call-site branch where the stream mode + riders are selected) and the rider-env const block at `main.go:253-268`.
- `cmd/pyry/modal_resolve_v2.go:266-368` (`ResolveAnswer`) and `:526-635` (`Surface` / `ResolveStream` / `retire`) — the production under test. Note `ResolveAnswer`'s gate order (Lookup → device gate → classify → consume → `ResolveStream`) and that `retire` broadcasts `modal_dismissed{denied_timeout, timeout}` on the no-answer path.
- `internal/control/server.go:878-978` (`handleApprove` / `watchApproveConn`) — the daemon blocks on `permbridge.Pending.Await`, guaranteed to return within the approval timeout; the timer path denies with `reasonTimeout`.
- `internal/permbridge/permbridge.go:112-176` — `Register` arms `time.AfterFunc(timeout, deny)`; `resolve` is the one-shot. This is the fail-closed timer the timeout case proves.
- `internal/control/client.go:299-310` — `Approve(ctx, socketPath, ApprovePayload) (*ApproveResult, error)`, the client fakeclaude calls.
- `cmd/pyry/main.go:1020-1029` and `:1402-1411` — `SetApprovalRegistry(approvals, mcpApprovalTimeout)` and `const mcpApprovalTimeout = 2 * time.Minute`. This is where the env seam lands.
- `cmd/pyry/mcp_approve.go:205-295` (`toolsCall` / `deny` / `denyResult`) — the fail-closed reference fakeclaude mirrors on a `control.Approve` error (error → deny, never allow, never hang).

---

## Design

Three moving parts. Two are new (the fakeclaude approve rider; the daemon-timeout env seam), one is pure test wiring (the spec).

### 1. fakeclaude approve rider (`internal/e2e/internal/fakeclaude/main.go`)

A new default-off rider on stream-json mode, mutually exclusive with the interrupt/hold riders (a turn either does the approval dance or the plain echo). Selected at the `main()` call site — **not** by widening `runStreamJSON`.

Rider envs (constants added to the existing block at `main.go:253-268`):

| Env | Meaning |
|---|---|
| `PYRY_FAKE_CLAUDE_STREAM_APPROVE` | non-empty ⇒ approve rider on. |
| `PYRY_FAKE_CLAUDE_APPROVE_SOCKET_FILE` | filesystem path fakeclaude reads (lazily, at dial time) to learn the daemon control socket. The test writes `h.SocketPath` into it after startup. Mirrors the existing `*_TRIGGER` file-path idiom. |

Call-site branch (in `main()`, inside the `envStreamJSON != ""` block, before the plain `runStreamJSON` call): if `PYRY_FAKE_CLAUDE_STREAM_APPROVE` is set, call a new `runStreamJSONApprove(stdin, os.Stdout, socketFile)` instead of `runStreamJSON(...)`. Contract:

```
func runStreamJSONApprove(r io.Reader, w io.Writer, socketFile string)
```

- Per-turn read loop reusing `bufio.ReadString('\n')` + `userTurnText` (the same discipline as `runStreamJSON`; the loop is ~15 lines and duplicating it keeps `runStreamJSON` byte-identical — a cheaper trade than a signature change that cascades to the sibling unit tests).
- On each `{"type":"user",…}` turn: read the socket path from `socketFile` (a one-line file the test wrote), then originate one approval:

```
verdict := dialApproval(socketFile, toolUseID)   // toolUseID unique per turn, e.g. "tu-1139-<turn>"
writeVerdictResponse(w, msgID, verdict)           // one assistant echo (needle) + one result{success}
```

- `dialApproval` reads the socket path, builds `control.ApprovePayload{ToolName:"Bash", Input: json.RawMessage(`{"cmd":"ls"}`), ToolUseID: toolUseID}`, and calls `control.Approve(ctx, socket, payload)` with a **generous** ctx (≥ the daemon's timeout window; e.g. 30s). It returns a small enum:
  - verdict `Behavior=="allow"` → `verdictAllow`
  - verdict `Behavior=="deny"` → `verdictDeny`
  - `control.Approve` error (socket unreachable / ctx expiry) → `verdictError` (**fail-closed** — mirrors `mcp_approve.go`'s error→deny, but tagged distinctly so the test can tell a genuine daemon deny from a client-side error; see *Verdict reflection oracle*).
- `writeVerdictResponse(w, msgID, verdict)` writes one assistant text line whose text is a fixed needle per verdict, then one `result{subtype:"success"}` line (reusing `writeAssistantEcho` + the existing `outResult` shape). Needles:
  - `verdictAllow` → text contains `approve-allow`
  - `verdictDeny`  → text contains `approve-deny`
  - `verdictError` → text contains `approve-error`

fakeclaude imports `internal/control` (same client `pyry mcp-approve` uses). No cycle (`control` never imports fakeclaude). This is a test-only binary, so the extra import is fine.

**Why the socket-in-a-file dance.** The control socket is `shortSocketPath(t)` — a random `/tmp/pyry-sock-*/pyry.sock` computed inside `spawnWith`, unknown before spawn and not derivable from the child's cwd (which is `home`, a different tree). The daemon does not export it to the child's env. So the test cannot pass the socket via a rider env value directly. Instead the rider env carries a **file path** (chosen by the test, under `home`, known pre-spawn); the test writes `h.SocketPath` into that file right after `StartStreamInteractiveWithRelay` returns and before it sends the triggering `send_message`. fakeclaude reads the file lazily at dial time (well after the write). This is the exact idiom fakeclaude already uses for `PYRY_FAKE_CLAUDE_*_TRIGGER`, and it keeps the whole change inside fakeclaude + the spec (no harness or supervisor plumbing).

### 2. Daemon approval-timeout env seam (`cmd/pyry/main.go`)

The daemon's approval window is `const mcpApprovalTimeout = 2 * time.Minute` — far too long for an e2e timeout case, and it is what `permbridge`'s fail-closed timer counts down. Make it env-overridable while leaving the const as the default and the fail-closed **logic** entirely untouched (different-fabric: the deterministic `time.AfterFunc` deny is production code; only its duration is tunable).

```
func approvalTimeout() time.Duration   // reads PYRY_APPROVAL_TIMEOUT (time.ParseDuration); falls back to mcpApprovalTimeout
```

Change the single call site `main.go:1022` from `ctrl.SetApprovalRegistry(approvals, mcpApprovalTimeout)` to `ctrl.SetApprovalRegistry(approvals, approvalTimeout())`. An unset/invalid env yields the 2-minute default — the production behaviour is byte-identical when the env is absent. The timeout case passes `PYRY_APPROVAL_TIMEOUT=2s` through `StartStreamInteractiveWithRelay`'s `extraEnv`; the allow/deny cases pass a generous value (e.g. `30s`) so a slow CI can't time out before the phone answers.

This is the security-correct choice: the deny in the timeout case comes from the **daemon's** real `permbridge` timer, and fakeclaude — blocking with a generous ctx — receives that daemon verdict. It is not masked by a fakeclaude self-timeout.

### 3. The e2e spec (`internal/e2e/relay_v2_stream_modal_test.go`, `//go:build e2e`)

One test function, table-driven over three cases, each spinning its own daemon + phone via a shared setup helper modelled on `relay_v2_stream_send_test.go`. Uniform shape per case:

```
phone (paired --allow-remote-permissions, interactive) send_message(knownConvID) → ack
  → fakeclaude receives the user turn → dials control.Approve (blocks)
  → daemon parks in permbridge → streamApprovalBridge.Surface → modal_shown → phone
  → [answer arm] phone modal_answer(allow|deny) → ResolveAnswer → ResolveStream → verdict
     [timeout arm] phone sends nothing → permbridge timer denies (PYRY_APPROVAL_TIMEOUT=2s)
  → control.Approve returns the verdict → fakeclaude writes assistant echo (needle) + result
  → daemon stream drain → assistant_delta(needle) + modal_dismissed → phone asserts
```

The id-alignment invariant is identical to `relay_v2_stream_send_test.go`: `seedBootstrapRegistry(initialUUID)` (inside `StartStreamInteractiveWithRelay`) + `seedBoundConversation(knownConvID, initialUUID)` make the drain gate pass and let the reflected delta reach the phone.

---

## Verdict reflection oracle

The assertion the ticket demands — *the fake receives deny* — is proven by fakeclaude reflecting the **daemon's** verdict into its assistant text, which the daemon's stream parser turns into an `assistant_delta` the phone observes:

| Case | fake receives (from `control.Approve`) | needle in `assistant_delta` | corroborating `modal_dismissed` |
|---|---|---|---|
| allow | daemon verdict `allow` | `approve-allow` | `Outcome=allow_once, Source=remote` |
| deny | daemon verdict `deny` | `approve-deny` | `Outcome=reject_once, Source=remote` |
| timeout | daemon verdict `deny` (permbridge timer) | `approve-deny` | `Outcome=denied_timeout, Source=timeout` |

`approve-error` (client-side `control.Approve` failure) is a **distinct** needle. It must **never** appear in a green run. Its distinctness is what makes the fail-closed proof airtight: the timeout case asserts `approve-deny` specifically — the daemon's genuine timer deny — not a masked client error. If the ctx were misconfigured too short, the test would see `approve-error` and fail loudly rather than false-pass.

---

## Concurrency model

- fakeclaude's approve loop runs on `main()`'s single goroutine (a single reader, single writer to `os.Stdout` — `-race` clean by construction, same as `runStreamJSON`). `control.Approve` blocks that goroutine for the approval window; no assistant output is written until the verdict lands. This is fine — the test's drain deadline (≥20s) absorbs it.
- The daemon side is unchanged: `handleApprove` blocks on `permbridge.Pending.Await` on the control-server handler goroutine; the surfacer broadcasts `modal_shown` on that goroutine; `ResolveAnswer` / the timer resolve the one-shot from the relay Run goroutine / a timer goroutine. All pre-existing, all unit-tested.
- The test goroutine is the single phone reader (serial `ReceiveBytes`), exactly like the stream/interrupt siblings.

---

## Error handling

- **`control.Approve` error inside fakeclaude** → `verdictError` needle (`approve-error`), fail-closed (never `approve-allow`). Mirrors `mcp_approve.go`'s error→deny; distinct needle so the test surfaces the misconfiguration.
- **Socket file missing/empty at dial time** → treated as a dial error → `verdictError`. (Ordering makes this unreachable in a correct run — the test writes the file before sending — but it degrades to a loud `approve-error`, never a hang or an allow.)
- **`PYRY_APPROVAL_TIMEOUT` unset/invalid** → daemon falls back to the 2-minute default; the allow/deny cases still pass (phone answers fast), the timeout case would (correctly) run long — so the timeout case must set it. No silent production behaviour change when absent.
- **Phone not gated** (`--allow-remote-permissions` omitted) → `ResolveAnswer` denies at the device gate, the modal stays outstanding, the daemon timer eventually denies → the allow case would observe `approve-deny` and fail. The flag is load-bearing for the allow/deny cases; the spec must pair with it.

---

## Testing strategy

New e2e file `internal/e2e/relay_v2_stream_modal_test.go` (`//go:build e2e`), one table-driven test over `{allow, deny, timeout}`. Scenarios (bullet-pointed; the developer writes the Go in the house idiom, lifting `awaitModalShown` and the drain loop from the cited siblings):

**Shared setup (per case):** `shortHome` → `pair -pyry-name=test --name=phone-a --allow-remote-permissions` → `seedBoundConversation(knownConvID, initialUUID)` → `fakerelay.New` → `StartStreamInteractiveWithRelay(t, home, initialUUID, url, "PYRY_FAKE_CLAUDE_STREAM_APPROVE=1", "PYRY_FAKE_CLAUDE_APPROVE_SOCKET_FILE=<home>/.pyry/approve.sock.txt", "PYRY_APPROVAL_TIMEOUT=<per-case>")` → **write `h.SocketPath` into the socket file** → `waitBinaryHello` → `fakephone.Dial` → `driveHandshakeToOpenDaemonInteractive` → `send_message(knownConvID)` → await ack.

- **allow** (`PYRY_APPROVAL_TIMEOUT=30s`): await `modal_shown` (assert `Class=="permission"`, 4 option IDs = the fixed AllowOnce/AllowAlways/RejectOnce/RejectAlways set, `ModalID != ""`, `ConversationID == knownConvID`); send `modal_answer{ModalID, OptionID=AllowOnce, AnswerToken:"…"}`; drain until `assistant_delta` whose `Text` contains `approve-allow` **and** (corroboration) `modal_dismissed{Outcome=allow_once, Source=remote}`; assert the terminal `turn_state{idle}`. Assert the delta needle is **not** `approve-deny` / `approve-error`.
- **deny** (`PYRY_APPROVAL_TIMEOUT=30s`): as allow, but `OptionID=RejectOnce`; expect `approve-deny` + `modal_dismissed{Outcome=reject_once, Source=remote}`.
- **timeout — the load-bearing case** (`PYRY_APPROVAL_TIMEOUT=2s`): await `modal_shown` (same assertions); **send no `modal_answer`**; drain (bounded, e.g. 20s) until `assistant_delta` whose `Text` contains `approve-deny` **and** `modal_dismissed{Outcome=denied_timeout, Source=timeout}`. Assert **never** `approve-allow`, and the drain completes within the deadline (no hang). This case fails if the gate fails open (an allow needle), if it hangs (the daemon never denied), or if the deny is a masked client error (`approve-error`).

**Untouched-siblings AC (green regression):** the existing PTY modal specs `relay_v2_modal_answer_test.go` and `relay_v2_modal_cancel_test.go` are not edited; `make e2e` runs them and they stay green (the developer runs `make e2e` — which builds the tag and runs `-race` — as the final gate; the streamsup/PTY paths are disjoint, so zero blast is structural).

**Light unit test** (`internal/e2e/internal/fakeclaude/*_test.go`, untagged, same-package): assert `writeVerdictResponse(w, "m1", verdictAllow|verdictDeny|verdictError)` emits the right needle in the assistant line + a `result` line, against an in-memory buffer (mirrors the existing `stream_detect_test.go` style). The full dial→verdict loop needs a live socket and is covered by the e2e, not the unit test.

---

## Acceptance Criteria

- [ ] An e2e spec `internal/e2e/relay_v2_stream_modal_test.go` runs under `interactive_runner:"stream-json"` (via `StartStreamInteractiveWithRelay`): fakeclaude, on a user turn, originates a permission request via `control.Approve` (blocking until allow/deny), and that request surfaces to the interactive phone as `modal_shown` (`Class=="permission"`, the fixed 4 options, non-empty `ModalID`).
- [ ] A `modal_answer` of AllowOnce resolves the verdict as allow and the fake reflects `approve-allow`; a `modal_answer` of RejectOnce resolves the verdict as deny and the fake reflects `approve-deny`. Each is corroborated by `modal_dismissed{Outcome=<option>, Source=remote}`.
- [ ] With no answer, the request times out and the **daemon** denies (fail-closed): the fake receives the daemon's deny (reflects `approve-deny`, never `approve-allow`, never `approve-error`) and the phone observes `modal_dismissed{Outcome=denied_timeout, Source=timeout}`, all within a bounded deadline (no hang). The daemon timeout is shrunk to ~2s via `PYRY_APPROVAL_TIMEOUT`.
- [ ] fakeclaude gains the scripted-approval rider (`PYRY_FAKE_CLAUDE_STREAM_APPROVE` + `PYRY_FAKE_CLAUDE_APPROVE_SOCKET_FILE`) with `runStreamJSON`'s signature **unchanged** (siblings byte-identical). `cmd/pyry/main.go` gains an env-overridable approval timeout (`approvalTimeout()` reading `PYRY_APPROVAL_TIMEOUT`, default `mcpApprovalTimeout`), with production behaviour unchanged when the env is absent.
- [ ] The existing PTY modal specs (`relay_v2_modal_answer_test.go`, `relay_v2_modal_cancel_test.go`) are untouched and `make e2e` is green with the new spec included.

---

## Open questions

- **Interactive stream runner does not wire `--permission-prompt-tool` / `--mcp-config` (production follow-up, not a blocker).** `internal/streamsup.buildArgs` omits the permission-prompt-tool + mcp-config pair that `cmd/pyry/agent_run.go` uses for `pyry agent-run`. So under `interactive_runner:"stream-json"` real claude would not route tool uses through the daemon approval registry at all. This e2e proves the daemon-side surface (which is fully wired via `SetApprovalRegistry` + `SetApprovalSurfacer`) by having fakeclaude call `control.Approve` directly. Wiring the interactive stream runner to spawn with the permission-prompt-tool + mcp-config is a separate production ticket — worth filing as a GitHub issue after this lands. Flag it in the codebase note.
- **`PYRY_APPROVAL_TIMEOUT` naming.** Chosen as a plausibly-operational knob (operators may legitimately tune the approval window), not a `PYRY_TEST_*` name. If the reviewer prefers a strictly test-scoped name, rename in the impl — the seam shape is unchanged.

---

## Security review

**Verdict:** PASS

The adversarial question this pass must answer: *does the spec genuinely prove that an approval cannot land as allow without an explicit, authorized allow answer, and that no-answer denies — or does it paper over a gate that fails open?* I walked every category assuming the spec has holes.

**Findings:**

- **[Trust boundaries]** No MUST FIX. The untrusted inputs are the phone's `modal_answer` (network→process) and the *absence* of an answer. The trusted authority is the daemon's `permbridge` completer — reachable to **allow** only via an explicit `Resolve(id, Allow(...))` won on the one-shot (`internal/permbridge/permbridge.go:157-176`); every other terminal path (timer, disconnect, shutdown, unknown id) is `Deny`. The device gate (`modal_resolve_v2.go:297`, `dev.MayAnswerRemotePermission()`) rejects an ungated `modal_answer` before consume. This spec adds **no** production path to allow — it only exercises the existing gate. The one new boundary is fakeclaude reading a socket path from a test-written file; fakeclaude is a test binary and the file is trusted test wiring, so no production trust boundary is added.

- **[Timeout fails closed at the daemon, not the fake — the load-bearing check]** No MUST FIX; one SHOULD-note. In the timeout case fakeclaude blocks on `control.Approve` with a ctx **longer** than the daemon's env-shrunk window, so the deny it receives is the daemon's real `permbridge` `time.AfterFunc(timeout, Deny(reasonTimeout))` firing — not a fakeclaude self-timeout. The env seam changes only the timer's *duration*, never its deny logic (different-fabric). A fail-open regression (a stray allow on the no-answer path) surfaces as an `approve-allow` needle **and** a non-`denied_timeout` dismissal — both asserted against — so the test goes red; a hang surfaces as `approve-error` / a drain-deadline `t.Fatal`; a client-side ctx error is a distinct `approve-error` needle that can never masquerade as a daemon deny (no false pass). **SHOULD FIX (impl invariant):** the fake's `control.Approve` ctx must stay well above the daemon `PYRY_APPROVAL_TIMEOUT` (spec recommends ~30s vs 2s, a 15× margin); if that inverts, the fake self-times-out and masks the daemon verdict. The developer must keep the margin and code-review should verify it.

- **[Authorization actually required — allow path]** No findings. The allow case only passes because the phone pairs `--allow-remote-permissions`; drop it and `ResolveAnswer` denies at the device gate, the case observes `approve-deny`, and the expectation fails. So the exercised allow is specifically the gated-and-answered one — not a default-allow.

- **[Tokens, secrets, credentials]** No findings. No new token. `AnswerToken` is the existing client idempotency key (not authorization — dedup is the `modal_id` one-shot). `modal_id` is the existing `crypto/rand` UUIDv4 nonce. fakeclaude's scripted `ToolName`/`Input` are non-secret fixtures.

- **[File operations]** No findings. The only new file op is fakeclaude reading a fixed, test-chosen socket-path file under `home` (no user-input path concatenation → no traversal; read-once at dial time; missing/empty → `verdictError` fail-closed, never a hang or allow). It holds a socket path, not a secret. The daemon's atomic-write registries are untouched.

- **[Subprocess / external command]** No findings. The design deliberately does **not** spawn `pyry mcp-approve` (it calls `control.Approve` directly), so no new `exec.Command` and no `sh -c`. The scripted `Input` `{"cmd":"ls"}` is carried as opaque `json.RawMessage` and is never parsed, dispatched, or executed (the permbridge/mcp_approve invariant is preserved) — it is echoed verbatim on allow and dropped on deny.

- **[Cryptographic primitives]** No findings. No new crypto; the Noise transport and the `crypto/rand` modal nonce are unchanged.

- **[Network & I/O]** No findings. `control.Approve` is the existing Unix-socket client; the fake bounds its wait with a finite ctx. The env seam cannot create a DoS: a non-positive/short `PYRY_APPROVAL_TIMEOUT` denies *faster* (more fail-closed, per `permbridge.Register`'s documented non-positive-timeout safety); a bad parse falls back to the 2-minute default.

- **[Error messages, logs, telemetry]** No findings. The needles (`approve-allow`/`approve-deny`/`approve-error`) are content-free discriminants. The daemon's content-free approval/audit logging (no `input`, no `tool_name`, no prompt) is untouched; the scripted `Input` is never logged.

- **[Concurrency]** No findings. The fake approve loop is single-goroutine (blocks on `control.Approve`, returns on verdict/ctx — no leak). No new locks. The daemon side is unchanged: `permbridge` leaf mutex + delete-under-lock one-shot; `handleApprove`'s `watchApproveConn` denies on mid-approval shutdown/disconnect.

- **[Threat model alignment]** No MUST FIX. The relevant `protocol-mobile.md` § Security model threat — an internet-sourced `modal_answer` escalating to allow, or a timeout/disconnect failing **open** — is exactly what this spec proves is mitigated, end-to-end. It weakens no mitigation; it adds coverage. **OUT OF SCOPE:** the interactive stream runner not wiring `--permission-prompt-tool`/`--mcp-config` (so real claude wouldn't route tool uses through the daemon registry under this runner) is a production gap named in *Open questions*, to be filed as a follow-up GitHub issue after this lands — it does not affect the fail-closed proof of the daemon surface.

No MUST FIX findings. The one SHOULD FIX (fake-ctx ≫ daemon-timeout margin) is called out in the spec's *Design* and *Testing strategy* and is a code-review checkpoint, not a design hole.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-21
