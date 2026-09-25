# #2667 — Codex model entries carry a GPT display name and report Auto support

## Files read

- `cmd/pyry/session_model_list.go` → `codexModelOptions` — the one place a Codex row is tagged; today it sets `DisplayName` to the family and leaves `SupportsAutoMode` false. Both the reply path (`mergedModelOptions`) and the push path (`pushedModelOptions`) go through it, so one edit covers requested and pushed lists.
- `cmd/pyry/session_model_list.go` → `claudeModelOptions`, `resolveBoundModelList` — untouched; Claude rows and an older client's list keep today's bytes.
- `internal/codexsup/models.go` → `parseFamilyID`, `familyTable.options` — a held Codex row's `ResolvedModel` is `gpt-<version>-<family>`, lowercase `[a-z0-9.-]`, version dot-separated decimals, family starting with a letter and possibly hyphenated (`codex-mini`).
- `cmd/pyry/session_model_list_merged_test.go` → `sentinelCodexModels`, `assertTaggedRows` — the existing assertions on Codex rows; their sentinel resolved ids are not gpt-shaped and their expected display is the family.

## Change

`codexModelOptions` sets each Codex row's `DisplayName` from a new unexported helper `codexDisplayName(resolved, family string) string` and sets `SupportsAutoMode` true. The helper cuts the `gpt-` prefix, splits the rest at its first hyphen into version and family, and returns `"GPT-" + version + " " + <family's hyphen parts, each with its first letter upper-cased, joined by spaces>` — `gpt-5.6-terra` → `GPT-5.6 Terra`, `gpt-6-luna` → `GPT-6 Luna`, `gpt-5.1-codex-mini` → `GPT-5.1 Codex Mini`. An id not in that shape (no `gpt-` prefix, no hyphen after the version, empty version or family — reachable only through a hand-edited store file, since `parseFamilyID` gates what the store is given) falls back to the family, today's display. Auto is reported on every Codex row because the daemon accepts `auto` for Codex (`validPermissionMode`, `codexTurnOverrides` maps it to `on-request` with the `auto_review` reviewer). Nothing else moves: `claudeModelOptions` and the older-client resolver are unchanged, so AC 2 holds by construction.

## Testing strategy

- New table test for `codexDisplayName`: the three shapes above plus the fallbacks (non-gpt id, `gpt-` with no family, `gpt-` with empty version).
- `sentinelCodexModels` gets gpt-shaped resolved ids, and `assertTaggedRows`' Codex branch expects the derived display name and `SupportsAutoMode` true — so both the reply seams test and the push test (`TestPushedModelOptions_TagsAsTheMergedReply`) prove AC 1 for requested and pushed lists.
- AC 2 is already asserted: the old-client arms DeepEqual against `resolveBoundModelList` and forbid tag keys; Claude rows go through `assertTaggedRows` with their held display name and auto flag.

## Documentation handoff

None named by the ticket. Pending for the documentation stage: the codexsup/relay package overview's description of Codex rows in the merged model list (display name was the family, now derived from the resolved model; `supports_auto_mode` now true).

## Revisions

### 2026-09-25 — security review added (verifier finding on PR #2679)

The ticket carries `security-sensitive`, and the plan was committed without the label-gated review. The verifier's MUST FIX asked for it, naming two areas: the widened Auto advertisement and the `codexDisplayName` input. The review below walks the full checklist. Its verdict is PASS with no code change, so the design and the implementation stand as committed.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries — Auto advertisement] No findings. `supports_auto_mode` is advice to the client and nothing in the daemon reads it to allow or refuse anything. Outside the parser and store round-trips of Claude's own list, its only non-test producer is `codexModelOptions`, and no gate consumes it. What the daemon accepts is decided elsewhere, unchanged by this ticket. `validPermissionMode` already admits `auto` for every agent, and the capability list beside it already offers it. `codexTurnOverrides` maps `auto` to `ApprovalOnRequest` + `SandboxWorkspaceWrite` + `ReviewerAutoReview`. Every approval still goes through the auto reviewer, and the sandbox is the same one `default`/`acceptEdits` get. The only unreviewed posture, `ApprovalNever` + `SandboxDangerFullAccess`, comes from `PermissionModeBypass`/YOLO alone, and the flag does not reach that branch. A client could already send `auto` for a Codex session before this change; the flag only stops the menu from hiding a posture the daemon accepts. No new approval posture opens and nothing is downgraded.
- [Trust boundaries — `codexDisplayName` input] No findings. `ResolvedModel` comes from the operator's own model-vocabulary store. `parseFamilyID` gates what a Codex spawn gives the store. On load, `decodeCodexVocabulary` checks only that the value is non-empty, so a hand-edited file can hold any string. That string already reached the client verbatim as `resolved_model` before this ticket, so the derived name adds no new exposure. The derived string goes only into `ModelOption.DisplayName`, a JSON string field on the wire. It is never a format string, a path, an exec argument or a log field: `session_model_list.go` has no logger, per the no-logger rule on `resolveBoundModelList`. The helper is pure string slicing and cannot panic: `p[:1]` is guarded by `p != ""`. Its output is at most a few bytes longer than its input, because an ASCII upper-case letter keeps its byte length.
- [Trust boundaries — non-ASCII input] No findings. A hand-edited id whose family part starts with a multi-byte rune gets its lead byte cut off by `p[:1]`, and `strings.ToUpper` turns that byte into U+FFFD. The result is a garbled label, not a safety issue: `encoding/json` also turns any invalid UTF-8 into U+FFFD when it marshals the payload, so malformed bytes never reach the wire. Store-written ids are `[a-z0-9.-]` per `parseFamilyID`, so this path needs a hand-edited file.
- [Tokens, secrets, credentials] Not applicable. No token, key or credential is read, derived or emitted. The change touches model metadata only.
- [File operations] Not applicable. The change reads the in-memory store copy through `CodexModels` and writes no file. The Codex rows it builds are wire copies and are never written back: `RetainCodex` and the store writer keep only value, resolved model and efforts.
- [Subprocess execution] Not applicable. The display name never reaches `exec.Command`, and the Codex spawn takes its model from session settings, not from this label.
- [Cryptographic primitives] Not applicable. No randomness, hashing or comparison against a secret.
- [Network & I/O] No findings. There is no new read and no new message. The row count is still bounded by the store's family count, and each name is bounded by its input as above. The payload goes out on the existing `model_list` reply and push paths, whose size handling does not change.
- [Error messages, logs, telemetry] No findings. The helper has no error path; a malformed id falls back to the family silently, which is what the row showed before. Nothing is logged.
- [Concurrency] No findings. `codexDisplayName` is pure. `codexModelOptions` works on the deep copy `CodexModels` returns under the store's leaf mutex, so there is no new lock, goroutine or shared mutation.
- [Threat model alignment] No findings. The relay threat model's concern here is a paired phone choosing a more permissive posture than the operator allowed. Permission enforcement stays with `validPermissionMode` and `codexTurnOverrides`, which this ticket does not touch.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25
