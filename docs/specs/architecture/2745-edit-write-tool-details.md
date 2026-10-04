# #2745 — Tool row details only for edits and writes

## Files read

- `internal/streamsup/parser.go` → `toolResultDetail`, its five composers and narrow decode targets: remove the read, shell and search producers while preserving edit/write formatting and fail-closed behavior.
- `internal/streamsup/parser_test.go` → sidecar count, attribution, confinement and bound tests: update suppressed details and use edit fixtures for positive attribution controls.
- `docs/knowledge/features/streamsup-package.md` and `streamsup-package-content-blocks-are-held-as-json-rawmessage.md` → sidecar confinement lessons: keep key-presence identification and never emit sidecar bytes as `Unrecognized`.
- `docs/knowledge/features/development-verification.md` → test discrimination: retained positive controls must still exercise a supported producer.
- `internal/turnbridge/outbound_test.go` → `TestToolResultPayload_FitV2EnvelopeCap`: its existing 48-byte allowance remains a safe conservative bound.
- `CODING-STYLE.md` → Go test and error conventions.

## Change

Try only `editDetail` then `writeDetail` in `toolResultDetail`; Read, Bash and Grep/Glob sidecars return an empty detail. Remove the unused composers and their decode fields/targets. Preserve edit hunk-prefix counting, Write's existing `created/updated · N lines` format, sidecar confinement, and single-result attribution. The ticket's request to preserve `writeDetail` and its existing tests resolves the acceptance criterion's shorthand `+N −M` for Write. Keep the historical 48-byte allowance as a conservative bound over the remaining edit (43 bytes) and write (36 bytes) forms. No client, public interface, concurrency or error-path changes are needed. No overlapping feature branches touch either parser file.

Sizing: one deliverable, two acceptance criteria, no new exported types or consumer updates, no new reject branches, and fewer than 300 written lines including this plan and tests; within all builder limits.

## Testing strategy

First update read, shell and search parser assertions to require empty details and observe failures against the original producer. Retain Edit/Write assertions unchanged, including glyph and confinement checks. Switch the multi-block and synthetic-user positive controls to Edit fixtures so attribution and suppression coverage remains meaningful. Update the bound tests to check remaining forms against the conservative allowance. Run `go test -race ./internal/streamsup/...`, `go vet ./...`, and `go build ./cmd/pyry`; the verifier owns the full-module gate.

## Revisions

- 2026-10-04: The package race run exposed another positive control, `TestParser_ParentToolUseID_LeavesTheUserSidecarIntact` in `internal/streamsup/parent_tool_use_test.go`. Switch its Read fixture to Edit so it still detects accidental sidecar suppression. Source review also found Read/five-shape claims in `internal/turnevent/event.go` → `ToolUpdate`, `internal/protocol/interactive.go` → `ToolResultPayload`, and `internal/turnbridge/outbound.go` / `outbound_test.go` → `MapEvent` / `TestToolResultPayload_FitV2EnvelopeCap`. Update only their descriptions and make the envelope test's existing 48-byte fill explicitly conservative. Run race checks for these packages too. The wire format and mapper behavior remain unchanged; total written work remains below 300 lines.

## Documentation handoff

- Pending for the documentation stage: update `docs/knowledge/features/streamsup-package-content-blocks-are-held-as-json-rawmessage.md`, the tool-result sidecar decode and confinement paragraphs, to describe only Edit/Write producers and the conservative 48-byte allowance.
- Pending for the documentation stage: update `docs/knowledge/features/protocol-package-interactive-event-payloads.md`, the `ResultDetail` description, to state that Read/Bash/Grep/Glob send empty details while Edit and Write retain their existing formats.
