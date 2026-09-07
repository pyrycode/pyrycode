# interactive_stream_attachment_read_test.go

`interactive_stream_attachment_read_test.go` (#2039) — the live proof for the
second half of the 2026-05-16 attachment-prompt decision. #2038 proved
hermetically that a `send_message` naming `attachment_ids` reaches claude as a
composed prompt naming each attachment's on-host path; only a real claude can
show that it actually opens that path. `TestInteractiveStreamAttachmentRead`
mints a token from `crypto/rand`, writes it to a file under `t.TempDir()` —
never under the daemon's workdir, where an ordinary directory listing could
find it and green the test with the attachment path playing no part — uploads
it as a single-chunk `attachment_chunk`, sends a message naming it and asking
for its contents, and asserts the accumulated `assistant_delta` text carries
the token.

**Observed 2026-09-03, first run: claude opens the file.** It called exactly
one tool, `Read`, against the composed absolute path — which sits under the
daemon's *instance* directory, outside claude's workspace cwd — with no
permission modal (the harness spawns with `--dangerously-skip-permissions`),
and replied with the 24-character token and nothing else, byte-exact, no
reformatting. Against a build whose `composeAttachmentPrompt` returns `text`
unconditionally, claude replied that it saw no file path in the message and
called no tool: `--- FAIL` in 6.81s. The upload chain ran green under that
mutant too, so the red is isolated to composition, not transfer.

## Lessons that outlive this ticket

- **A live claude opens an absolute path outside its own cwd without
  hesitation, when told to read it.** The design flagged this as the
  ticket's main open risk — the attachment lands under the daemon's instance
  directory (`<instanceDir>/conversations/<id>/attachments/<id>/`), not under
  claude's workspace, so the composed prompt hands claude a path it has no
  other reason to trust. No workaround was needed; this closes the second
  half of the 2026-05-16 decision to put the path in the prompt rather than
  inlining the bytes — see
  [relay-package-handlers.md § Handlers](relay-package-handlers.md#handlers-per-envelope-type-processors-handlers-250).
- **A short token round-trips byte-exact.** 24 lowercase hex characters came
  back with no case-folding, spacing, or chunking. A future test asserting on
  claude-echoed content this short can compare with a strict
  `strings.Contains` rather than building in a case-folding fallback up
  front.

See `docs/specs/architecture/2039-live-attachment-read.md` for the full
design, including why the upload rides a prior cursor-stamp turn and why the
local drains name a refusal code instead of a bare timeout.

Zero production files touched.
