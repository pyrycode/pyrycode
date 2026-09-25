# #2631 — validate sent Codex request params against the committed schema

## Files read

- `internal/codexsup/methods.go` → `clientRequests` — the seven request methods whose params get validated.
- `internal/codexsup/client.go` → `handshake`, `StartThread`, `ResumeThread`, `StartTurn`, `SignedIn`, `Interrupt`, `approvalPolicyParam`, `sandboxPolicyParam`, the `Approval*` / `Sandbox*` / `Reviewer*` constants — the encoders whose bytes are checked.
- `internal/codexsup/models.go` → `LatestModels` — the `model/list` sender (cursor omitted on page one, set on later pages).
- `internal/codexsup/client_test.go` → `schemaMethods`, `TestMethodNamesInSchema`, `startPeer`, `peer.read`, `peer.next`, `TestRequestParamShapes`, `TestTurnPostureShapes`, `TestDefaultDeclines` — the harness the new test drives; `peer.read` swallows the `initialize` frame today; `TestDefaultDeclines` reads `p.frames` directly but skips any frame carrying a method.
- `cmd/pyry/codex_settings.go` → `codexTurnOverrides` — emits only the constants above; its fallback for an unknown mode is granular / readOnly / user.
- `internal/codexsup/codex_app_server_protocol.schemas.json` → `ClientRequest.oneOf`, `InitializeParams`, `v2/TurnStartParams`, `v2/AskForApproval`, `v2/SandboxPolicy`, `v2/ApprovalsReviewer`, `v2/ModelListParams` — `$ref`s are JSON pointers into nested `definitions` (`#/definitions/v2/X`).
- `internal/codexsup/SCHEMA.md` — regeneration instructions (documentation handoff below).

## Context

Only method names are checked against the committed schema. Params are pinned by golden strings, which match what the encoder emits, not what Codex accepts. A regenerated schema that renames or retypes a field would still pass. This adds a test-only JSON Schema subset validator and runs every `clientRequests` method's actually-written params through it under `make check`. No production code changes.

## Design

One new test file, `internal/codexsup/schema_params_test.go`, plus a one-line harness change.

**Harness change.** `peer.read` also forwards the `initialize` frame to `p.frames` after answering it, so a test can read the handshake's params with `p.next(methodInitialize)`. Existing readers are unaffected: `peer.next` discards frames of other methods and `TestDefaultDeclines` skips frames that carry a method. The buffer (16) holds it.

**Validator** (unexported, test file):

- `type schemaCheck struct { root any; unsupported map[string]bool }` — `root` is the whole decoded bundle.
- `func (v *schemaCheck) check(path string, schema, inst any) []string` — returns mismatch messages, each prefixed with the instance path (`sandboxPolicy.type`, `input[0].text`). Unsupported keywords are recorded in `v.unsupported`, separately, so a passing sibling arm of `anyOf` cannot hide one.
- Keywords handled:
  - `$ref` → resolve the JSON pointer against `root` (`~1`/`~0` unescaped), check against the target.
  - `type` string or array: instance must match one; `integer` is a float64 with no fraction.
  - `enum` → `reflect.DeepEqual` against some value.
  - `properties` / `additionalProperties` / `required` (object instances): every instance key must be declared in `properties`, unless `additionalProperties` is `true` (allowed) or a schema (checked against it). **Absent `additionalProperties` is treated as `false`** — deliberately stricter than JSON Schema, so a renamed field fails. The strict key check applies at any node with `properties` or whose `type` allows `object`; a node with neither (e.g. a bare `$ref` or `anyOf` wrapper) delegates to its arms.
  - `items` → each element.
  - `anyOf` / `oneOf` → at least one arm passes; otherwise one message naming the keyword and each arm's first failure.
  - `allOf` → every arm passes.
  - `minimum`, `minLength` → checked.
  - Annotations `title`, `description`, `default`, `format`, `$schema` → ignored.
  - Anything else → recorded as unsupported; the test fails naming the keyword.
- Boolean schemas: `true` passes, `false` fails.

**Params lookup.** `paramsSchema(bundle, method)` walks `definitions.ClientRequest.oneOf`, finds the arm whose `properties.method.enum` holds the method, and returns that arm's `properties.params` — the same entry point `schemaMethods` uses.

**Test `TestRequestParamsMatchSchema`.** Drives a `startPeer` client (`Config{Dir: "/work"}`) through every sender and validates each captured `params` frame:

- `initialize` — from the handshake.
- `thread/start` (`StartThread`), `thread/resume` (`ResumeThread`).
- `turn/start` — the full cross product of `{ApprovalGranular, ApprovalOnRequest, ApprovalNever}` × `{SandboxReadOnly, SandboxWorkspaceWrite, SandboxDangerFullAccess}` × `{ReviewerUser, ReviewerAutoReview}` with a model and a valid effort set. That covers each constant, every combination `codexTurnOverrides` can emit, and its fallback.
- `turn/interrupt` (`Interrupt`), `account/read` (`SignedIn`).
- `model/list` — two pages (`LatestModels`), so both the empty and the cursor-bearing params are checked.

Failures read `<method>: <path>: <reason>`. After the drive, the test asserts every method in `clientRequests` was validated at least once, so a new request method without coverage fails.

**Validator self-test `TestSchemaCheck`.** Table-driven over small inline schemas: renamed property, retyped property, `["string","null"]`, enum miss, missing required, `items`, `anyOf` no-match, `allOf`, `$ref`, `minimum`, `minLength`, `additionalProperties: true`, annotation ignored, unsupported keyword recorded.

## Concurrency model

The peer answers requests on a goroutine, as `TestTurnPostureShapes` does. Captured `params` travel back over a channel to the test goroutine, which validates them; no shared mutable state.

## Error handling

Schema read/parse failure and a method missing from `ClientRequest` are `t.Fatalf`. Mismatches are `t.Errorf` per message. Unsupported keywords are one `t.Errorf` each, naming the keyword.

## Testing strategy

The tests are the deliverable. The AC3 proof (drop `workspaceWrite` from `SandboxPolicy`; add a required field to the granular `AskForApproval` arm) is run once against a temporarily edited schema, restored from a backup copy afterwards, and quoted in the PR description. The edited schema is not committed. Golden-string tests stay.

## Open questions

- Does any `clientRequests` sender emit a keyword or shape outside the listed subset? Resolved by running the test; the ticket lists the reachable keywords at `d7dce501`.

## Documentation handoff (pending — documentation stage)

- `internal/codexsup/SCHEMA.md`: after regenerating the schema for a new pin, `make check` is the wire-shape check (`TestRequestParamsMatchSchema`). A failure there means pyry's encoding has to change along with the pin.

## Revisions

- 2026-09-25, implementation: the open question is resolved with no design change. At the committed 0.156.1 schema, every keyword the seven senders reach is in the handled subset; `TestRequestParamsMatchSchema` records no unsupported keyword.
