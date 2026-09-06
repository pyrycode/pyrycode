//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamSendFile is #2169's AC-4: proof that the pyry_files MCP
// server is REAL TO CLAUDE on a daemon-spawned interactive session, and that the
// session identity riding the child's environment is the one the daemon resolves.
//
// Three shipped pieces meet here for the first time. #2164 gave the daemon the
// attachment.file verb and fileAttacher, which derives the destination from the
// CALLING session's conversation. #2168 gave `pyry mcp-files` its send_file tool,
// forwarding to that verb and reading the session id off PYRY_SESSION_ID. #2169
// registers that server in the --mcp-config document and puts the id on the claude
// child. Everything below the tool call already has deterministic tests; what only
// a live claude can answer is whether claude can SEE the tool and whether the id
// that arrives is the right one.
//
// # The two observables, and why each is not vacuous
//
// The MODAL is the proof claude could see the tool. A tool absent from the
// document raises nothing, so on a tree without the pyry_files entry the drain
// deadlines rather than passing quietly. Its Prompt carries the full tool
// reference verbatim — streamApprovalBridge.Surface hands permbridge.Request's
// ToolName to modalbridge.PermissionRequestForClass as the screen text, and
// buildPayload puts that trimmed string in ModalShownPayload.Prompt — so the
// assertion is equality against mcp__pyry_files__send_file, not a substring probe
// of a rendered sentence. If claude reaches for Write or Bash instead, that
// equality fails NAMING the tool it actually asked for, which is a sharper red
// than a timeout.
//
// The STORED ATTACHMENT is the proof a valid session id travelled and resolved to
// the DRIVING conversation. fileAttacher refuses an empty id, requires the id to
// name a live session, and then requires it to be some conversation's
// CurrentSessionID; only then does it mint an attachment id and file the bytes
// under that conversation. So a directory appearing beneath THIS conversation,
// named by a canonical minted id and holding the fixture bytes, cannot be produced
// by an absent, forged or stale id — each of those refuses instead. The tool's own
// success sentence carries the same id, but it reaches claude, not the wire; the
// filesystem is where this test can read it.
//
// # Why the file is pre-created
//
// So the turn raises EXACTLY ONE modal — the send_file one. A prompt that asks
// claude to produce the file first interleaves a Write modal ahead of it, and the
// drain would answer that one and never reach the tool under test. The prompt also
// forbids Bash/Write/Edit for the same reason; read-only inspection is harmless
// because claude auto-approves it (see writeFileTrigger's account of why a bare
// echo never raised a modal on this path).
//
// Reuses startStreamModalResolutionHarness (#1154) verbatim rather than standing
// up a second daemon: it already writes the stream-json toggle, pairs the phone
// --allow-remote-permissions (without which the answer denies at the device gate),
// seeds a bootstrap pool id plus a conversation bound to it, and spawns via
// spawnPermissionDaemon so claude runs in default permission mode. Its seeded ids
// are shared with TestInteractiveStreamModalResolution and that is safe: each test
// gets its own authenticated tempdir HOME, so the two never share on-disk state.
//
// Placement under the e2e_realclaude tag wires it into `make e2e-realclaude` (and
// thus `make preship`) with no Makefile change, and the harness skips cleanly when
// claude or creds are absent. `make check` never compiles this package, so read the
// count of tests that executed rather than an exit code.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// sendFileToolRef is the full MCP tool reference the permission modal must name.
//
// Transcribed as a literal because cmd/pyry is package main and nothing there
// exports it — approveToolRef exists only because --permission-prompt-tool needs
// the string, and no production caller needs this one. The cheap suite carries the
// drift guard (cmd/pyry's TestFilesToolRef_DriftGuard pins this exact literal
// against mcpFilesServerName + sendFileToolName), so renaming either constant
// reddens there rather than here, where discovering it costs real tokens.
const sendFileToolRef = "mcp__pyry_files__send_file"

// sendFileFixtureBody is what the handed-over file contains. Asserted byte-for-byte
// on the stored copy: attachment.file copies the bytes at call time, so anything
// else on disk means the daemon filed something other than the file claude named.
const sendFileFixtureBody = "pyrycode send_file live gate\n"

// sendFileAttachBudget bounds the wait for the stored attachment after the turn
// closes. The turn is already idle by then and the tool result preceded claude's
// reply, so the bytes are on disk before this starts; the budget covers ordering
// slack between the daemon's write and this read, not any claude work.
const sendFileAttachBudget = 15 * time.Second

// TestInteractiveStreamSendFile drives a live claude to call send_file on a
// pre-created file, answers the resulting permission modal, and proves the daemon
// filed the bytes under the driving conversation under a freshly minted id.
func TestInteractiveStreamSendFile(t *testing.T) {
	h, convID := startStreamModalResolutionHarness(t, permissionDaemonModel)

	// A per-run nonce keeps the filename (and so the trigger) distinct across runs,
	// defeating any accidental caching without asserting on an echo.
	nonce := time.Now().UnixNano()
	filename := fmt.Sprintf("handover-%d.txt", nonce)
	if err := os.WriteFile(filepath.Join(h.workdir, filename), []byte(sendFileFixtureBody), 0o600); err != nil {
		t.Fatalf("pre-create the fixture in the conversation's recorded workspace: %v", err)
	}

	modalID := raiseSendFileModal(t, h, 2, convID, filename)

	// Answer allow_once from the paired phone. The device gate
	// (--allow-remote-permissions) is armed by the harness; without it ResolveAnswer
	// denies, the tool never runs, and nothing is ever filed.
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:   3,
		Type: protocol.TypeModalAnswer,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.ModalAnswerPayload{
			ModalID:  modalID,
			OptionID: string(turnevent.PermissionOptionKindAllowOnce),
			// A client-minted idempotency key, NOT authorization — authorization is
			// ModalID validity plus the device gate.
			AnswerToken: "e2e-2169-answer-token",
		}),
	})

	// The turn resumes and closes: a non-empty continuation assistant_delta followed
	// by the terminal turn_state{idle}. "Answered but the tool never ran" fails here
	// before the filesystem is ever read, which keeps the two reds distinguishable.
	drainForCompletedTurn(t, h.phone, h.initRecv, convID, perTurnReplyBudget)

	requireSendFileAttachment(t, h.home, convID, filename)
}

// --- the trigger ------------------------------------------------------------

// raiseSendFileModal sends the send_file trigger, drains to modal_shown, asserts
// the modal is a permission modal NAMING the pyry_files tool, and returns its id.
//
// A local sibling of raiseRealPermissionModal rather than a call to it: that helper
// asserts Class == "permission" and a non-empty modal_id, which every gated tool
// satisfies, so on its own it cannot tell "claude called send_file" from "claude
// called Write instead". The tool-reference equality is the entire non-vacuity
// argument of this test, so it belongs in the drain, before any answer is sealed.
func raiseSendFileModal(t *testing.T, h *perConvHarness, reqID uint64, convID, filename string) string {
	t.Helper()

	// Names the tool by its server-qualified reference so claude does not have to
	// guess which of its tools is meant, forbids the tools whose modals would
	// interleave ahead of the one under test, and ends with a reply instruction —
	// the continuation delta drainForCompletedTurn requires.
	trigger := fmt.Sprintf(
		"A file named %s already exists in your working directory. Hand it to the operator by calling "+
			"the send_file tool from the pyry_files MCP server, passing path %s. Do not use Bash, Write "+
			"or Edit, and do not create or modify any file. After the tool returns, reply with a single short word.",
		filename, filename)
	sealSendMessage(t, h.phone, h.initSend, reqID, convID, fmt.Sprintf("m-%d", reqID), trigger)

	env := drainForControlEvent(t, h.phone, h.initRecv, protocol.TypeModalShown, modalSurfaceBudget)
	var shown protocol.ModalShownPayload
	if err := json.Unmarshal(env.Payload, &shown); err != nil {
		t.Fatalf("decode modal_shown payload: %v", err)
	}
	if shown.Class != "permission" {
		t.Fatalf("modal_shown Class = %q, want %q — a non-permission modal surfaced (trust/onboarding, or the trigger surface changed)", shown.Class, "permission")
	}
	if shown.ModalID == "" {
		t.Fatal("modal_shown carried an empty modal_id")
	}
	if shown.Prompt != sendFileToolRef {
		t.Fatalf("modal_shown Prompt = %q, want %q — claude raised a modal for a DIFFERENT tool, so it "+
			"either could not see send_file (no pyry_files entry in the --mcp-config document) or chose "+
			"another route to the same goal", shown.Prompt, sendFileToolRef)
	}
	return shown.ModalID
}

// --- the evidence -----------------------------------------------------------

// requireSendFileAttachment asserts the daemon filed the fixture under convID's
// attachment store: exactly one attachment directory, named by a canonically
// shaped minted id, holding exactly the fixture file with exactly its bytes.
//
// The path is built the way attachments.EnsureDir builds it —
// <instance dir>/conversations/<conversation id>/attachments/<attachment id> —
// against the daemon's instance directory for -pyry-name=test under the harness's
// isolated HOME. Reading it here rather than parsing claude's reply is deliberate:
// the minted id reaches claude in a tool result, not the wire, so prose is the only
// wire-side carrier and asserting on prose would make a claude that paraphrases
// look like a daemon that refused.
//
// Naming the leaf in a failure message is safe here, unlike in
// requireStoredAttachment's client-upload case: this filename is a fixture this
// test authored, not a client-supplied name § Attachments bans logging.
func requireSendFileAttachment(t *testing.T, home, convID, filename string) {
	t.Helper()

	root := filepath.Join(home, ".pyry", "test", "conversations", convID, "attachments")
	deadline := time.Now().Add(sendFileAttachBudget)
	var entries []os.DirEntry
	for {
		var err error
		entries, err = os.ReadDir(root)
		if err == nil && len(entries) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no attachment under %s after %s (last read: %v) — the modal was answered and the "+
				"turn closed, so either the tool call never reached the daemon or fileAttacher refused "+
				"the session id that arrived", root, sendFileAttachBudget, err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	if len(entries) != 1 {
		t.Fatalf("attachment store holds %d entries, want exactly 1 — one send_file call files one attachment", len(entries))
	}
	// The directory name IS the minted id. Asserting it is canonically shaped is
	// what distinguishes "the daemon minted this" from any id derived from something
	// claude supplied: NewID is the only producer of this shape here, and EnsureDir
	// refuses to build a path for anything else.
	attachmentID := entries[0].Name()
	if !conversations.ValidID(attachmentID) {
		t.Fatalf("attachment directory name %q is not a canonical minted id", attachmentID)
	}

	files, err := os.ReadDir(filepath.Join(root, attachmentID))
	if err != nil {
		t.Fatalf("read the attachment directory: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("attachment %s holds %d files, want exactly 1 — a second entry is a leaked temporary from attachments.Store's rename", attachmentID, len(files))
	}
	if got := files[0].Name(); got != filename {
		t.Fatalf("stored file name = %q, want %q — a plain-ASCII name is inside SanitizeFilename's allowlist and passes through unchanged", got, filename)
	}

	got, err := os.ReadFile(filepath.Join(root, attachmentID, filename))
	if err != nil {
		t.Fatalf("read the stored file: %v", err)
	}
	if string(got) != sendFileFixtureBody {
		t.Fatalf("stored bytes = %q, want %q — the daemon filed something other than the file claude named", got, sendFileFixtureBody)
	}
}
