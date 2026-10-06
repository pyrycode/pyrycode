# #2893 — serve eligible workspace files of any type

## Files read

- `cmd/pyry/workspace_file.go` → `workspaceFileReader`, `isMarkdownName`, `confineToAnyRoot`: replace only the two filename gates; retain root selection and checked reads.
- `cmd/pyry/workspace_file_test.go` → workspace, configured-folder and working-folder fixtures: retain confinement, live-read and startup root proofs while widening file cases.
- `cmd/pyry/attach_file.go` → `confineFile`, `confineToRoot`, `readChecked`: existing canonical containment, regular-file and descriptor-identity checks and bound-plus-one reads stay unchanged.
- `cmd/pyry/relay.go` → `relayWiring`, `startRelayV2`: reader wiring and contract comments.
- `internal/relay/v2session_workspace_file.go` → `handleReadWorkspaceFile`, `rejectWorkspaceFileRead`: static refusal vocabulary and privacy restrictions.
- `internal/relay/v2session_seams.go` → `V2SessionConfig.WorkspaceFileRead`; `internal/relay/v2session.go` → `appFrameWorkspaceFileRead`: existing byte seam and dispatch contract comments.
- `internal/relay/v2session_workspace_file_test.go` → `TestV2Session_ReadWorkspaceFile_StreamsTheReadBytes`, refusal and stream-abort tests: preserve correlation, byte transport and log hygiene.
- `internal/relay/v2attachmentstream.go` → `attachmentEnvelopes`: byte-sniffed MIME and unchanged attachment stream.
- `internal/protocol/attachments.go` → `ReadWorkspaceFilePayload`; `internal/protocol/codes.go` → `TypeReadWorkspaceFile`: directly affected contract comments, no schema change.
- `internal/sessions/systemprompt.go` → `readFolderSentence`; `internal/sessions/systemprompt_readfolder_test.go` → pinned sentence and composition tests: preserve folder formatting and placement.
- `docs/knowledge/features/sessions-package.md`, `docs/knowledge/features/protocol-package.md`, `docs/knowledge/features/v2-session-manager.md`: owning package maps.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-read-workspace-file-workspacefileread.md`: both-leaf checks, single refusal and startup-canonical extra roots must survive the change.
- `docs/knowledge/features/protocol-package-constants-codes-go-envelope-types-attachments.md`: outbound metadata provenance and existing live-read contract.
- `docs/knowledge/features/development-verification.md`: ordering assertions must distinguish early refusal; byte transport assertions must inspect decoded metadata.
- `docs/protocol-mobile.md` → Security model and Attachments: paired-client trust boundary and path/filename/content logging restrictions.

## Context

Clients need the existing confined live reader to serve scripts, archives and binary files without converting them to markdown. This is one behavior contract; the session prompt and comments describe it. No new wire verb, schema, dependency or decision record is needed.

Sizing: four acceptance criteria, zero new exported types/interfaces, zero consumer signature changes, two replaced reject predicates and no new state-machine branches. Expected total written work is approximately 450 lines including tests and this plan, below 800. The #2710 analogue is consistent with the unchanged root machinery. Recount before implementation: all five limits remain within bounds. Other remote numeric feature branches were checked after fetching; none touches the planned files.

## Design

Replace `isMarkdownName` with a pure `isSecretName(name string) bool` predicate. Lowercase the leaf and deny exact `.env`, prefix `.env.`, exact `id_rsa`, `id_dsa`, `id_ecdsa`, `id_ed25519`, and suffixes `.key`, `.pem`, `.p12`, `.pfx`, `.keychain`, `.keychain-db`. This is only a filename heuristic; it neither inspects content nor guarantees allowed files contain no secrets.

`workspaceFileReader` refuses the requested leaf before registry/filesystem access, confines the path with unchanged `confineToAnyRoot`, refuses the resolved leaf before `readChecked`, then returns the unchanged bytes, resolved basename and fresh transfer UUID. Relative paths remain workspace-only; extra roots require absolute paths. The supplied bound remains enforced by unchanged `readChecked`.

Keep `WorkspaceFileRead`, `WorkspaceFile`, chunking, MIME sniffing and correlation unchanged. Replace the handler's markdown-specific static log reason with a file-type-neutral reason. Update directly affected production/test comments to describe admitted roots and the two-leaf filename denylist.

`readFolderSentence` says files of any type are served subject to the size limit and secret-name refusals. Keep folder quoting, list order, absolute-path wording, newline, composition placement and empty-list behavior unchanged.

## Concurrency model

No new goroutines, locks or state. Reads still run on the connection's FIFO worker, one in flight per connection. Registry lookup remains at request time; immutable startup-resolved folders retain their existing lifetime. Stream cancellation and shutdown paths remain unchanged.

## Error handling

Both filename refusals and existing registry, confinement, non-regular, missing, checked-read and size failures return the same empty file and false. The handler keeps its static non-retryable `attachment.not_found` response and existing `attachment.stream_aborted` classification. Do not log paths, filenames, contents, sizes or filesystem errors.

## Testing strategy

Write tests first and observe behavioral failures with the markdown-only reader. Extend admission tests with markdown, shell, Python, zip, extensionless and binary bytes, eligible dotfiles and public-key names, relative/absolute paths and harmless symlinks with a different target extension. Check resolved basenames and distinct canonical transfer IDs.

Use table-driven denied-name cases including uppercase variants, `.env.example`, every exact key name and every suffix. Exercise direct refusals and both symlink directions inside workspace and configured roots. A nil-registry reader must refuse denied requested leaves without accessing the registry; ordinary eligible paths are exercised with real fixtures. Retain confinement/FIFO/directory/root-swap tests.

For each representative file type, test the supplied bound exactly and bound plus one. Exercise non-markdown admission and denied symlinks in the automatic working root. Extend relay byte-stream coverage with binary data and a misleading extension, asserting `http.DetectContentType` MIME alongside existing correlation and reassembly. Update all pinned prompt expectations.

Run scoped race tests for `cmd/pyry`, `internal/relay`, `internal/sessions` and `internal/protocol`, then `go vet ./...` and `go build ./cmd/pyry`. The dispatcher owns the full-module verifier gate. No live Claude tests or artifacts are required.

## Open questions

None. The denylist is exact as specified, including deliberate refusal of `.env.example` and admission of `.envoy`, `.gitignore` and `id_rsa.pub`.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md`, Attachments → `read_workspace_file`: replace the markdown-only contract and the claim that widening requires another verb with the widened regular-file contract, exact denylist on both leaves, existing root/path semantics, unchanged bounded byte stream/MIME and refusal vocabulary. Keep the general attachment content-hygiene obligations.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-read-workspace-file-workspacefileread.md`: replace **The markdown rule is checked twice, and neither check is redundant** with the two-leaf secret-name rule and its filename-only limitation; update the root sections accordingly.
- `docs/knowledge/features/protocol-package-constants-codes-go-envelope-types-attachments.md`: update the `TypeReadWorkspaceFile` paragraph's double-markdown-check claim to the two-leaf denylist.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The paired client's path remains untrusted. `handleReadWorkspaceFile` gates conversation membership; `workspaceFileReader` performs both secret-name checks and unchanged confinement before bytes cross the filesystem boundary.
- [Tokens, secrets, credentials] The denylist must check both leaves: either symlink direction bypasses a single check. Tests cover both. The filename-only limitation is explicit; content inspection is outside the requested contract, and allowed bytes receive no secret-free claim.
- [File operations] `confineToAnyRoot`, `confineFile`, `confineToRoot` and `readChecked` remain unchanged: canonical containment, regular-file checks, `O_NOFOLLOW|O_NONBLOCK`, `os.SameFile` and bounded reading preserve existing protections. No files are written by the reader.
- [Subprocesses] No subprocesses or caller-controlled command execution are introduced.
- [Cryptography] Transfer IDs retain `conversations.NewID` and crypto/rand; no cryptographic primitives or authentication change.
- [Network and I/O] Reads retain supplied `maxBytes` (production 16 MiB), bound-plus-one rejection and existing bounded attachment chunks. No new network boundary or server is introduced.
- [Errors, logs, telemetry] Every refusal stays static and non-retryable; the replacement log reason is a constant. No path, filename, content, size or filesystem error reaches logs.
- [Concurrency] No new shared mutable state or goroutines; existing FIFO worker and stream cancellation apply unchanged.
- [Threat model] The Security model's paired-client host-read risk is bounded by existing authorized roots and the two-leaf heuristic. Relay encryption, device revocation and attachment content hygiene remain unchanged. Client preview/download behavior belongs to later app tickets explicitly excluded here.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-06
