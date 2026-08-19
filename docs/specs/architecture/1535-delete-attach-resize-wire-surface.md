# #1535 — Delete the attach/resize wire types and the orphaned `SendResize` client helper

**Size:** S (confirmed — see § Size check)
**Packages touched:** `internal/control` only.
**Shape:** pure deletion + seven comment rewrites + one new test file. No new exported surface, no behaviour change on any live path.

---

## Files to read first

The developer's turn-1 data load. Resolve every symbol with `codegraph_search` / `codegraph_node`.

| File | Symbols | What to extract |
|---|---|---|
| `internal/control/protocol.go` | the `Verb` const block, `Request`, `AttachPayload`, `ResizePayload` | The five deletion targets and their doc comments. Note `AttachPayload`'s and `ResizePayload`'s doc comments are attached to the declarations, so they die with them — nothing to decide there. |
| `internal/control/protocol.go` | `RekeyPayload` | **Survives.** Its doc cites `AttachPayload.SessionID, ResizePayload.SessionID` as the camelCase-tag precedent. One of the seven rewrite sites. |
| `internal/control/client.go` | `SendResize` | The deletion target: builds a `VerbResize` request, dials, checks `resp.OK`. Doc comment goes with it. |
| `internal/control/client.go` | `SessionsNew`, `SessionsList`, `SessionsHasID` | **Survive.** Each doc comment enumerates `Status/Logs/Stop/SendResize/…` as the shared one-shot-dial lifecycle. Three of the seven rewrite sites. Mechanical: drop one name from each list. |
| `internal/control/server.go` | `defaultHandshakeTimeout`, `Server.handle` | The dispatch switch is the proof that nothing reads `req.Attach` / `req.Resize`: there is no `case VerbAttach`, no `case VerbResize`, and no reference to either field in any arm. Both symbols carry a rewrite site. |
| `internal/control/server.go` | `Server.handleApprove` | The only remaining `conn.SetDeadline(time.Time{})` clearer. Read it to word `defaultHandshakeTimeout`'s replacement correctly — it clears the deadline but does **not** hand off connection ownership. **Do not edit this function or its comments**; its `(mirrors handleAttach)` cite belongs to #1537. |
| `internal/control/server_test.go` | `TestServer_UnknownVerb`, `startServer` | AC-3's shape precedent (dial → send → assert `Response.Error` mentions the verb) and the harness helper the new test reuses: `startServer(t, resolver) (sock string, stop func())`. |
| `internal/control/sessions_new_test.go` | `TestSessionsNew_PassesLabelOnWire` | Its doc comment contains the stale cite `TestSendResize_RoundTrip`. **This line must survive** — it is #1537's scope, and AC-4's `\b` anchors are calibrated to spare it. |
| `CODING-STYLE.md` § "Comments — Citing Other Code" | — | The seven rewrites are comment edits on lines this branch modifies, so `make cite-guard` (part of `make check`, verified in the `Makefile`'s `check` target) checks every one. Cite by symbol name, never `file.go:NNN`, no ranges, no bare `:NNN`. |
| `docs/knowledge/features/control-plane.md` | § "Attach: ResolveID-then-Lookup", § "Attach: CLI Surface" | Read-only, and **read it as history**: it still documents `handleAttach`, `control.Attach`, `control.AttachStdio`, and `internal/control/attach_client.go`, none of which exist in the package today. #1538 owns fixing it. Do not treat its attach prose as a live contract, and do not edit it. |

---

## Context

#1348 deleted both terminal-driving claude paths, taking `internal/control`'s server-side attach handler with them (`attach.go` and `attach_client.go` are gone from the package; only comment references to `handleAttach` remain). The wire surface that handler served is still exported, and `client.go` still exports `SendResize` — a helper that builds a `VerbResize` request, dials the daemon, and receives `unknown verb: "resize"` on every call, because `Server.handle`'s dispatch switch has no arm for it. That is a public client API whose server half no longer exists: the shape most likely to be picked up by a future caller, or re-implemented on the assumption that a verb constant implies a verb.

Verified in this worktree at `main` (2026-08-20), the same numbers the ticket recorded:

- `rg -n '\b(VerbAttach|VerbResize|AttachPayload|ResizePayload|SendResize)\b'` over `internal/` and `cmd/` matches **25 lines in exactly 3 files** — `client.go` 7, `protocol.go` 15, `server.go` 3. Nothing in `cmd/`, nothing in `internal/e2e/`, nothing in any test file. (`rg` reads every file regardless of build tag, so the `e2e_realclaude`-tagged package is covered by that same sweep.)
- `git grep -F 'DisallowUnknownFields' -- '*.go'` returns **zero matches** repo-wide. This is AC-3's load-bearing premise and it holds.
- `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` **exits 0** at baseline. AC-5's attribution claim holds: any failure after this change is this change's.
- No in-flight feature branch touches any file in `internal/control` (§ 1.5 branch-overlap scan, clean).

**No ADR.** This removes a dead surface under an existing decision (#1348's) rather than making a new one. The design record that matters is the compatibility argument below, and it belongs in the spec and in the test, not in a separate document.

---

## Design

### The change is a decode-surface narrowing with no dispatch-surface change

The only place these symbols are reachable from untrusted input is `Server.handle`'s `dec.Decode(&req)`. After that, `handle` switches on `req.Verb` alone; no arm reads `req.Attach` or `req.Resize`. So the deletion removes a *decode* surface and touches no *dispatch* surface.

Go's `encoding/json` ignores unknown object fields by default, and nothing in the repo opts out. Deleting a declared field therefore makes the decoder **strictly more permissive** — it can only accept inputs it previously rejected, never the reverse. Concretely, three input classes:

| Input | Before | After |
|---|---|---|
| `{"verb":"attach","attach":{…}}` — well-typed object | decodes; `default:` arm → `unknown verb: "attach"` | decodes (field ignored); `default:` arm → `unknown verb: "attach"` |
| `{"verb":"resize","resize":{…}}` — well-typed object | decodes; `default:` arm → `unknown verb: "resize"` | decodes (field ignored); `default:` arm → `unknown verb: "resize"` |
| `{"verb":"attach","attach":"oops"}` — type mismatch | **decode error** → `Response{Error: "decode request: …"}` | decodes (value skipped); `default:` arm → `unknown verb: "attach"` |

Rows 1 and 2 are the equivalence AC-3 pins: a v0.5.x client sending the real attach or resize payload receives the same reply it receives today. Row 3 is the one genuine behaviour delta, and it is an *accepted widening* — see § Error handling and § Security review. All three are covered by the same test table, for the mutant-discrimination reason set out in § Testing strategy.

**The outbound direction needs no pin.** Both deleted fields carried `omitempty` on a pointer type and are nil in every surviving construction site (no test or caller anywhere builds an `AttachPayload` or `ResizePayload`, per the anchored sweep). A nil `omitempty` pointer emits no field, so removing the declarations changes the marshalled bytes of *no* `Request` this repo constructs. `TestProtocol_SessionsRoundTripBackCompat`, the package's byte-equality guard, is therefore expected to stay green unmodified — it is not in this ticket's edit set.

**No import churn.** `AttachPayload` and `ResizePayload` are built from `int` / `string` / `bool` only, and `SendResize` uses `context` and `errors`, which every surviving helper in `client.go` also uses. `protocol.go` keeps `encoding/json` (for `ApprovePayload.Input`) and `time` (for `SessionInfo.LastActive`). No import becomes unused, so `staticcheck` has nothing new to say and no import block needs editing.

### Deletions

**`internal/control/protocol.go`** — remove, each with its attached doc comment:

- `VerbAttach` and `VerbResize` from the `Verb` const block. Leave every other member and the block's grouping untouched.
- The `Attach` and `Resize` fields from `Request`. The four surviving fields (`Verb`, `Sessions`, `Rekey`, `Approve`) keep their tags, their `omitempty`, and their trailing `// populated for …` comments verbatim. `gofmt` will re-align the struct's tag column — that realignment is the whole of the permitted collateral change to those lines.
- The `AttachPayload` type declaration and its doc comment.
- The `ResizePayload` type declaration and its doc comment.

**`internal/control/client.go`** — remove `SendResize` and its doc comment. It is the sole reader of `VerbResize` and `ResizePayload`, which is why this cannot be split from the protocol deletion: either half alone leaves `main` uncompilable.

### The seven comment rewrites (sites on surviving code)

Doc comments attached to a deleted declaration are deleted with it and need no decision. These seven sit on code that survives, so each is a rewrite. The **required content** is specified; the wording is the developer's, subject to `make cite-guard`.

| Symbol | Required content after the rewrite |
|---|---|
| `defaultHandshakeTimeout` (server.go) | Must no longer name `VerbAttach` or claim a streaming-verb carve-out — after this ticket there are no streaming verbs. It must still record the two facts that are true: the deadline is applied per-conn as `s.handshakeTimeout` (overridable in same-package tests), and it is **cleared** by `handleApprove` for the duration of a blocking approval wait, and **extended, not cleared**, by the one-shot session verbs before their long op. Do not merge those two cases — `handleApprove` clears and does not hand off the conn; that distinction is the whole reason the old wording's "since they hold the connection open indefinitely" clause needs restating rather than de-naming. |
| `Server.handle` (server.go) | Drop the entire streaming-verb/connection-handoff sentence. Every verb `handle` dispatches now replies with one JSON `Response` and returns, and the deferred `conn.Close` runs for all of them. Say that plainly. Leave the `TODO:` paragraph about partial-payload clients untouched — it is unrelated and still true. |
| the `VerbMCPApprove` case (inside `Server.handle`) | Drop the `unlike VerbAttach` contrast; keep the fact it exists to record — `handleApprove` owns the conn for the wait (clearing the handshake deadline, running a disconnect/shutdown watcher) but does **not** hand off ownership, so `handle`'s deferred `conn.Close` still runs on return and reaps the watcher's reader. State it without a comparison, since the referent is gone. |
| `RekeyPayload` (protocol.go) | Drop `AttachPayload.SessionID` and `ResizePayload.SessionID` from the camelCase-tag precedent parenthesis. `SessionsPayload.ID` is already cited in the same parenthesis, so the sentence stands with one example instead of three. Do not touch the `RoutingEnvelope.ConnID` contrast clause. |
| `SessionsNew` (client.go) | Drop `SendResize` from the `Status/Logs/Stop/SendResize` enumeration. |
| `SessionsList` (client.go) | Drop `SendResize` from its enumeration. |
| `SessionsHasID` (client.go) | Drop `SendResize` from its enumeration. |

**Explicitly out of scope, do not touch:**

- `handleApprove`'s `(mirrors handleAttach)` comment, and the other `handleAttach` comment references in `server.go` (on the `Session` interface's `Activate`, on `SessionResolver.ResolveID`, on `Server.Close`'s stream snapshot, and in `handleSessionsNew`'s deadline prose) — **#1537** owns the `handleAttach` comment sweep. None of them matches AC-4's pattern, so leaving them does not put AC-4 at risk.
- The `TestSendResize_RoundTrip` cite in `TestSessionsNew_PassesLabelOnWire`'s doc comment — **#1537** owns the stale-test-cite sweep, and AC-4's anchors deliberately spare it (`\bSendResize\b` does not match inside `TestSendResize_RoundTrip`: the preceding `t` and the following `_` are both word characters, so neither boundary exists).
- `fakeSession`'s `Attach` and `Resize` methods in `server_test.go`. The `Session` interface no longer declares either, so they are dead test helpers — but they match none of AC-4's five names and no AC covers them.
- `protocol.go`'s package doc, which mentions "attach" in lowercase prose alongside "logs, stop" as "future verbs". That sentence is Phase-0 vintage and was already stale for `logs` and `stop` before this ticket; this change does not make it newly wrong, it matches no AC, and it is not one of the seven sites.
- `docs/knowledge/features/control-plane.md` and every other file under `docs/knowledge/` — **#1538** owns them, and the documentation phase is their only writer.

---

## Concurrency model

Unchanged. No goroutine is created, destroyed, or re-parented; no lock is taken, released, or reordered. `Server.mu`, `streamConns`, and `streamingWG` are not touched — that bookkeeping is **#1536**'s scope, and the two tickets are disjoint by file region as well as by symbol.

The one adjacent invariant to preserve while rewriting `defaultHandshakeTimeout`'s comment: the constant's *value* and every `SetDeadline` call site stay byte-identical. Only the prose changes. A comment rewrite that also "tidies" the deadline logic is out of scope and is a review finding.

---

## Error handling

No new failure modes; one removed and one widened.

- **Removed:** `SendResize`'s three error returns (transport error, `resp.Error`, missing-`ok` flag) disappear with the function. It has no caller, so no error path anywhere loses a branch.
- **Widened:** a request whose `attach` / `resize` member has a type the deleted struct would have rejected (`"attach": "oops"`, `"attach": 7`, `"attach": []`) now decodes cleanly instead of producing `Response{Error: "decode request: …"}`, and receives the ordinary `unknown verb` reply. This is accepted, not a regression: the rejection was incidental to the field's declaration rather than a validation step — it fired identically for `{"verb":"status","attach":"oops"}`, so it never gated anything verb-specific and never gated authorization. It is pinned as intentional by the third row of AC-3's test table.

The `default:` arm's reply string is unchanged (`fmt.Sprintf("unknown verb: %q", req.Verb)`), so AC-3's expected `unknown verb: "attach"` matches the existing production of it. Do not restate or reformat that arm.

---

## Testing strategy

One new file: **`internal/control/wire_compat_test.go`**. New file rather than an addition to `server_test.go`, for two reasons: the package's house pattern is per-concern test files (`sessions_deadline_test.go`, `rekey_test.go`, `approve_test.go`, `sessions_has_id_test.go`), and three sibling tickets (#1536, #1537, #1538) are queued against this same package, so a fresh file removes the merge-conflict surface an insertion into `server_test.go` would create.

**One table-driven test**, `t.Parallel()`, stdlib only, reusing `startServer(t, &fakeResolver{sess: &fakeSession{}})` from `server_test.go` (same package — no export needed).

Shape, per row: dial the socket, `conn.SetDeadline`, **write the raw JSON bytes directly with `conn.Write`** (never marshal a `Request` — marshalling can no longer produce the unknown member, which is the entire point), decode one `Response`, assert `Response.Error` equals the expected string exactly. A trailing newline on the written bytes is optional and changes nothing: the server reads with `json.Decoder.Decode`, which consumes one complete JSON value and returns without waiting for a delimiter.

Rows, and the regression each one is the sole detector for:

| # | Raw request bytes | Expect `Response.Error` | Sole red for |
|---|---|---|---|
| 1 | `{"verb":"attach","attach":{"cols":80,"rows":24}}` | `unknown verb: "attach"` | — (AC-3's literal text; the realistic v0.5.x attach payload) |
| 2 | `{"verb":"resize","resize":{"sessionID":"","cols":80,"rows":24}}` | `unknown verb: "resize"` | a re-added `case VerbResize:` dispatch arm |
| 3 | `{"verb":"attach","attach":"not-an-object"}` | `unknown verb: "attach"` | a re-declared `Request.Attach` field of any struct type — rows 1 and 2 stay green under that mutation, row 3 goes red with a `decode request:` error |

Row 3 is what pins the § Error handling widening as *intentional* rather than accidental, and it is the only row that discriminates "field absent" from "field present and typed". It costs one table entry and no extra assertion machinery — every row asserts the same way.

A `dec.DisallowUnknownFields()` added anywhere in `Server.handle` reddens all three rows. That is the regression AC-3 exists to catch.

**Assert equality, not `strings.Contains`.** `TestServer_UnknownVerb` uses `Contains` because its concern is "the reply names the bad verb"; this test's concern is "the reply is the unknown-verb reply and not a decode error", and `Contains("attach")` would also pass against `decode request: json: cannot unmarshal … attach …`. Exact equality is what makes the row a real detector.

**Unchanged and expected to stay green with no edit:** `TestServer_UnknownVerb`. It sets neither `Attach` nor `Resize`, so it still compiles after the field removal. The new test is coverage *beside* it, not a replacement — do not modify or delete it.

**Gates for AC-5:**

```
make check                                        # includes vet, -race tests, staticcheck, substrate-guard, cite-guard, e2e
go vet -tags e2e_realclaude ./internal/e2e/realclaude/   # must exit 0; verified exit 0 at baseline
```

`cite-guard` is part of `make check` (see the `Makefile`'s `check` target) and is diff-scoped, so it inspects exactly the comment lines these seven rewrites modify. A replacement comment that reaches for a `file.go:NNN` — including a range, including a bare `:NNN` — fails the build.

**Verification for AC-4**, run from the repo root after the edits:

```
rg -n '\b(VerbAttach|VerbResize|AttachPayload|ResizePayload|SendResize)\b' internal/control/
```

Must print nothing. Run the same pattern **without** the `\b` anchors as a control: it must still print exactly one line, the `TestSendResize_RoundTrip` cite in `TestSessionsNew_PassesLabelOnWire`'s doc comment. Zero matches on the unanchored run means the sibling's line was poached; the control run is what tells the two outcomes apart.

---

## Size check

| Limit | Boundary | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **3** — `protocol.go`, `client.go`, `server.go` |
| Total written work (production + tests + helpers + per-branch logs + spec-doc edits) | ≤ 400 | **~130** — ~55 lines deleted, 7 comment rewrites (~20 lines rewritten), ~55-line new test file |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — the anchored sweep over `internal/` + `cmd/` finds no caller of any deleted symbol outside the three files being edited |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches in a state machine | ≤ 10 | **0 new** — the `default:` arm is untouched |

Ships as one `size:s`. Every line holds with margin; the binding number is file count at exactly 3, and it cannot grow — the deletion targets live in exactly those three files, and the new test is a test file.

---

## Open questions

- **None blocking.** The one judgement call resolved during design was whether to pin the type-mismatch widening (§ Error handling row 3). Resolved yes: it is the sole detector for a re-declared field, it costs one table row, and leaving it unpinned would make the widening indistinguishable from an oversight to a later reader.
- **For the documentation phase, not for the developer:** `docs/knowledge/features/control-plane.md` documents `handleAttach`, `control.Attach`, `control.AttachStdio`, `AttachPayload.SessionID`, and `internal/control/attach_client.go` as live surface across several sections. None of them exists in the package. That is #1538's scope and is named here only so nobody mistakes the doc for a current contract while working this ticket.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The design has exactly one boundary — `Server.handle`'s `dec.Decode(&req)`, where socket bytes become a `Request`. The deletion narrows the trusted-side struct, which under Go's default `encoding/json` behaviour makes the boundary *more permissive*, and permissiveness is the direction that warrants scrutiny. It is safe here because no widened input reaches new code: `handle` dispatches on `req.Verb` alone, no arm ever read `req.Attach` or `req.Resize`, and every verb outside the ten `case` arms lands in `default:` and gets a fixed-shape reply. Post-change the attacker-reachable state per attach/resize request is strictly *smaller* — the server no longer allocates an `*AttachPayload` or `*ResizePayload` from attacker-supplied bytes. The premise this rests on is verified, not assumed: `DisallowUnknownFields` has zero occurrences repo-wide, so no decoder anywhere opts into strict mode. Pinned by AC-3's test.
- **[Trust boundaries]** SHOULD FIX, addressed in-spec. The type-mismatch case (`{"verb":"attach","attach":"oops"}`) loses an incidental decode-time rejection. It never functioned as validation — it fired identically for `{"verb":"status","attach":"oops"}`, so it discriminated on payload shape, never on verb or on authorization, and no handler consulted its outcome. The design accepts the widening and pins it as intentional in row 3 of AC-3's table rather than leaving it as silent drift. No further mitigation is warranted on a `0600` socket whose caller is already the same uid.
- **[Tokens, secrets, credentials]** Not applicable, by content rather than by omission. `AttachPayload` carried terminal geometry (`Cols`, `Rows`), a loose session selector (`SessionID`), and a `CreateIfMissing` bool; `ResizePayload` carried geometry plus the same selector. No credential, no token, no key material, and no lifecycle to preserve. `SendResize` presented no auth material either — the control socket's only access control is its file mode, which this change does not touch.
- **[File operations]** Not applicable. No path is constructed, canonicalised, opened, created, or removed by any code this ticket adds or deletes. The socket path and its `0600` / `0700` mode discipline live in `Server.Listen` and are untouched. The new test's only filesystem contact is `startServer`'s existing `shortTempDir(t)` helper, unchanged.
- **[Subprocess / external command execution]** Not applicable. Neither the deleted symbols nor the surviving dispatch arms exec anything. The deleted `AttachPayload.CreateIfMissing` field is the closest thing to a spawn trigger in this surface — it was the wire opt-in to take-or-create attach, which reaches `sessions.Pool.GetOrCreate` and can mint a session and spawn a claude child. It has had no reader since #1348 removed `handleAttach`, so deleting it removes a dangling opt-in rather than a live one. The split ordering against **#1537** (which removes the `GetOrCreator` server-side seam) is safe in either direction and creates no window: with no `case VerbAttach` in `Server.handle`, the seam is unreachable from the socket whether the wire field is deleted first, last, or never. Neither ticket depends on the other's landing.
- **[Cryptographic primitives]** Not applicable. No RNG, no comparison against a secret, no key or nonce in this surface.
- **[Network & I/O]** No MUST FIX, one hazard the spec constrains explicitly. The deadline discipline is the DoS-relevant invariant here, and the `defaultHandshakeTimeout` rewrite is a comment edit sitting directly on it. A careless rewrite could assert "nothing clears this deadline" — false, because `handleApprove` still clears it for the length of a blocking approval wait — and a false claim about a timeout invariant is exactly the kind of wrong comment no test catches. § Design pins the required content: the constant's value and every `SetDeadline` call site stay byte-identical, and the replacement must record `handleApprove`'s clear and the session verbs' extend as two distinct cases. Slow-loris resistance is unchanged: `handle` still sets `s.handshakeTimeout` before the decode, and the pre-existing `TODO` bounding partial-payload clients stays in place and stays accurate. No read-size cap changes, because none is added or removed.
- **[Error messages, logs, telemetry]** No findings. The one attacker-influenced string in the affected path is the `default:` arm's `unknown verb: %q`, which echoes the supplied verb back. That is pre-existing, unchanged by this ticket, and already pinned by `TestServer_UnknownVerb`; `%q` escapes the value and `json.Encoder` escapes it again, so a verb carrying quotes or control bytes cannot break out of the JSON string. Nothing this ticket deletes was logged, and nothing it adds logs. AC-3's test asserts on the response only — no payload content reaches a log sink.
- **[Concurrency]** No findings. Zero goroutines added or removed; zero locks taken, released, or reordered. `Server.mu`, `streamConns`, and `streamingWG` are untouched (that is #1536's scope), so no lock-ordering question arises and no shutdown-path invariant moves. The new test spawns nothing of its own — `startServer` owns the server goroutine and its `stop` closure reaps it, so `go test -race ./internal/control/` covers the addition with the package's existing lifecycle discipline.
- **[Threat model alignment]** No findings. The control socket's model is same-uid processes — `Server.handle`'s own `TODO` states that the `0600` mode makes the realistic hostile-N "other processes the same user runs". Within that model this change strictly reduces attack surface: two verb constants, two attacker-populated payload types, and one dialing client helper are removed, and nothing is added. The downgrade/rollover case is worth naming because it is the one place a reviewer might expect a regression and find none: a v0.5.x `pyry attach` client dialing a post-#1535 daemon receives `unknown verb: "attach"` — which is exactly what it already receives from a post-#1348 daemon. The functional regression happened at #1348; this ticket's job is to prove it does not *worsen* into a decode error, and AC-3 is that proof.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-20
