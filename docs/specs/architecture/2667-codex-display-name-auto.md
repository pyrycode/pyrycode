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
