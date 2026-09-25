package relay

import (
	"context"
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the inbound LIVE WORKSPACE READ (#2598): the handler behind
// dispatchAppFrame's TypeReadWorkspaceFile case. It is handleRequestAttachment's
// twin in everything but where the bytes come from — a markdown file read off
// disk now, from the named conversation's recorded workspace, instead of a
// stored copy — so it answers with that handler's vocabulary unchanged: an
// attachment_chunk stream correlated by in_reply_to, or one
// rejectAttachmentNotFound / rejectStreamAborted.
//
// It is the first handler whose filesystem path a paired client names. None of
// the confinement lives here: the path goes to the WorkspaceFileRead seam, and
// cmd/pyry's workspaceFileReader applies the markdown-only rule, confineFile
// and readChecked, collapsing every refusal into one false. What this file owns
// is the ORDER and the ANSWER.
//
// WHERE IT RUNS, and why the two answer routes differ, is exactly
// handleRequestAttachment's file header: on the conn's FIFO appFrameWorker, with
// chunks through Push and rejects through forwardToRun. One read per conn is in
// flight at a time, which is the whole concurrency bound.

// handleReadWorkspaceFile answers one inbound read_workspace_file. The order,
// and the reason for each step, follows handleRequestAttachment:
//
//  1. A nil WorkspaceFileRead makes the frame inert but consumed — nothing is
//     decoded, read or replied.
//  2. A payload decode failure is rejected, never tolerated, and never echoed:
//     encoding/json quotes offending input into its error.
//  3. The membership gate fires before the conversation id reaches the seam. A
//     nil gate refuses everything.
//  4. Anything the reader will not answer is not_found. The seam's bool is the
//     structural collapse of every cause, so this site cannot branch on one.
//  5. A failure out of the stream is classified by attachmentStreamAborted, as
//     for request_attachment.
//
// SECURITY — never logged on any arm: the requested path, the resolved filename,
// the conversation id (a client string this handler never shape-validates), the
// bytes and their size. The minted attachment id is daemon-authored and is
// logged on the success arm and past it.
func (m *V2SessionManager) handleReadWorkspaceFile(ctx context.Context, s *V2Session, plaintext []byte) {
	if m.cfg.WorkspaceFileRead == nil {
		m.cfg.Logger.Debug("relay: v2 read_workspace_file inert; no reader wired",
			"event", "v2.workspace_file.inert",
			"conn_id", s.connID)
		return
	}

	var env protocol.Envelope
	if err := json.Unmarshal(plaintext, &env); err != nil {
		// Unreachable: dispatchAppFrame decoded these bytes to match the type.
		// No envelope id is left to correlate a reply to. NEVER echo err.
		m.cfg.Logger.Warn("relay: v2 read_workspace_file envelope did not decode",
			"event", "v2.workspace_file.envelope_err",
			"conn_id", s.connID)
		return
	}

	var req protocol.ReadWorkspaceFilePayload
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		m.rejectWorkspaceFileRead(ctx, s, env.ID, rejectAttachmentNotFound, "payload did not decode", "")
		return
	}

	if m.cfg.KnownConversation == nil || !m.cfg.KnownConversation(req.ConversationID) {
		m.rejectWorkspaceFileRead(ctx, s, env.ID, rejectAttachmentNotFound, "conversation is not one this daemon hosts", "")
		return
	}

	file, ok := m.cfg.WorkspaceFileRead(req.ConversationID, req.Path)
	if !ok {
		m.rejectWorkspaceFileRead(ctx, s, env.ID, rejectAttachmentNotFound, "no readable markdown file at that path in the workspace", "")
		return
	}

	if err := m.streamAttachmentBytes(ctx, s.connID, file.AttachmentID, file.Filename, file.Data, env.ID); err != nil {
		rej, reason := rejectAttachmentNotFound, "chunks could not be built"
		if attachmentStreamAborted(err) {
			rej, reason = rejectStreamAborted, "stream abandoned"
		}
		m.rejectWorkspaceFileRead(ctx, s, env.ID, rej, reason, file.AttachmentID)
		return
	}

	m.cfg.Logger.Info("relay: v2 workspace file served",
		"event", "v2.workspace_file.served",
		"conn_id", s.connID,
		"attachment_id", file.AttachmentID,
		"in_reply_to", env.ID)
}

// rejectWorkspaceFileRead is rejectAttachmentRequest with this verb's own event,
// so an operator can tell the two verbs' refusals apart in the log while a
// client cannot tell one cause from another on the wire. reason is a
// daemon-authored constant; attachmentID, when non-empty, is the daemon-minted
// transfer key and never a client string.
func (m *V2SessionManager) rejectWorkspaceFileRead(ctx context.Context, s *V2Session, inReplyTo uint64, rej attachmentReject, reason, attachmentID string) {
	attrs := []any{
		"event", "v2.workspace_file.refused",
		"conn_id", s.connID,
		"code", rej.code,
		"reason", reason,
	}
	if attachmentID != "" {
		attrs = append(attrs, "attachment_id", attachmentID)
	}
	m.cfg.Logger.Warn("relay: v2 read_workspace_file refused", attrs...)
	m.attachmentReplyError(ctx, s, inReplyTo, rej)
}
