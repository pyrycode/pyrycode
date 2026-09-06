package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/pyrycode/pyrycode/internal/acp"
	"github.com/pyrycode/pyrycode/internal/control"
)

// mcpFilesServerName is the MCP server name this subcommand advertises. It is
// load-bearing: the sibling #2169 registers this server as pyry_files so the full
// tool reference claude sees is mcp__pyry_files__send_file.
//
// This is its OWN server rather than a second tool on pyry_approve. The tool
// reference is text claude reads when deciding whether to call something, so
// mcp__pyry_approve__send_file would actively mislead the one reader it is aimed
// at; and approveToolName's contract — that server exposes exactly one tool and
// rejects any other name — is a fail-closed property of the permission bridge
// worth keeping rather than relaxing.
const mcpFilesServerName = "pyry_files"

// sendFileToolName is the single tool this server exposes. Load-bearing for the
// same reason as mcpFilesServerName — it forms the __send_file suffix of the tool
// reference — and the tools/call handler rejects any other name.
const sendFileToolName = "send_file"

// envSessionID names the environment variable carrying the calling session's id.
//
// The environment, never the tool's input, and never argv. The daemon writes its
// --mcp-config once at startup and that document is daemon-global — byte-identical
// for every session — so the argv of the forked child cannot name a session. The
// environment can: the daemon puts the id on the claude child it spawns, and the
// MCP server claude forks inherits it. Taking the id from the tool's input
// instead would let a caller name its own destination and file into a
// conversation it has no part in.
//
// Spelled once, as a constant, for the same reason envApprovalTimeout is: the
// name is a contract the sibling ticket writes against.
const envSessionID = "PYRY_SESSION_ID"

// Refusal sentences. Every one is a fixed constant — never model-controlled
// bytes, never anything derived from the request — and each says what to do
// differently, because the whole design rests on a refusal being correctable
// rather than surprising. The daemon's own refusals are NOT among them: those
// pass through verbatim (see toolsCall).
const (
	reasonSendFileMalformed   = "the send_file call was malformed; pass a JSON object with a single string field, path"
	reasonSendFileUnknownTool = "this server exposes only the send_file tool"
	reasonSendFileNoSession   = "this server is not bound to a session, so it cannot hand over a file"
	reasonSendFileNoResult    = "the file could not be handed over"
)

// sendFileHandedOver is the success sentence, taking the daemon-minted attachment
// id. That id is the one value in this exchange documented safe to surface — it
// is not a capability, since retrieval re-validates it against the conversation
// binding. Nothing else about the request is echoed back: the caller supplied the
// path and needs no copy of it, and adding anything content-derived is what the
// refusal contract forbids on the other branch.
const sendFileHandedOver = "the file was handed to the operator (attachment id %s)"

// Decision-log stages. Fixed, self-originated words identifying which branch
// refused, so stderr is diagnosable without carrying a single byte derived from
// the request — in particular not the refusal text, which is the daemon's
// sentence on one branch and a dial error naming the socket path on another.
const (
	stageMalformed   = "malformed"
	stageUnknownTool = "unknown_tool"
	stageNoSession   = "no_session"
	stageDaemon      = "daemon_refused"
	stageNoResult    = "no_result"
)

// filesServer is the MCP stdio send_file tool host. It is a pure forwarder: each
// tools/call is re-framed into a control-socket attachment.file request and the
// daemon's answer into the MCP tool result claude reads. The value is read-only
// after construction — no shared mutable state, no locks — so its safety does not
// rest on the transport dispatching one request at a time.
type filesServer struct {
	socketPath string       // resolved control socket to forward to
	sessionID  string       // the CALLER's session, from the environment; "" ⇒ every call refuses
	log        *slog.Logger // stderr only — stdout is exclusively the JSON-RPC frame stream
}

// runMCPFiles implements `pyry mcp-files`: an MCP server speaking JSON-RPC over
// stdio that exposes a single `send_file` tool. A call is forwarded to the daemon
// over the control socket as attachment.file, and the outcome returned as the
// tool result — fail-closed on every error so a claude turn never blocks on this
// tool. It takes the shared client flags (-pyry-name/-pyry-socket) and no
// positionals, the same pair `pyry mcp-approve` takes, since #2169 writes the
// argv that invokes it and inherits whatever contract lands here.
//
// An absent or empty PYRY_SESSION_ID does NOT abort startup. The server comes up
// and answers initialize and tools/list normally, refusing each tools/call.
// Exiting here would make claude report a broken MCP server; refusing per call is
// a sentence claude reads. Until #2169 puts the id on the claude child, that is
// this server's whole behaviour, and it is the intended inert state.
func runMCPFiles(args []string) error {
	socketPath, rest, err := parseClientFlags("pyry mcp-files", args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("mcp-files: unexpected arguments: %s", strings.Join(rest, " "))
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// stderr logger: stdout is exclusively the JSON-RPC frame stream claude reads;
	// a stray stdout write corrupts the MCP stream. Mirrors runMCPApprove.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger.Info("mcp-files: serving MCP send_file tool over stdio")

	s := newMCPFilesServer(socketPath, os.Getenv(envSessionID), logger)
	return serveJSONRPCStdio(ctx, os.Stdin, os.Stdout, logger, s.register)
}

// newMCPFilesServer is the sole production construction site for filesServer.
//
// It takes the session id as a PARAMETER and reads no environment of its own —
// the single os.Getenv lives in runMCPFiles. That split is deliberate: an
// environment-reading constructor forces every test through t.Setenv, which bans
// t.Parallel, and the approve server's own history is the evidence (its
// pre-#1929 constructor derived a deadline from PYRY_APPROVAL_TIMEOUT, and eleven
// parallel tests had to route around a helper that could not call it).
func newMCPFilesServer(socketPath, sessionID string, log *slog.Logger) *filesServer {
	return &filesServer{
		socketPath: socketPath,
		sessionID:  sessionID,
		log:        log,
	}
}

// register binds the three MCP request handlers on the transport. Notifications
// need no handler — dispatchNotification drops an unregistered one silently,
// writing no response frame.
func (s *filesServer) register(t *acp.Transport) {
	t.Register("initialize", s.initialize)
	t.Register("tools/list", s.toolsList)
	t.Register("tools/call", s.toolsCall)
}

// --- initialize -------------------------------------------------------------

// initialize answers the MCP initialize handshake: it echoes the client's
// protocolVersion when present and non-empty, falling back to
// defaultMCPProtocolVersion otherwise, per the MCP lifecycle "respond with the
// same version if supported" rule. A non-object params is rejected with
// CodeInvalidParams (shape gate); absent/empty params is tolerated. No params
// bytes are logged.
//
// The frame types and the version constant come from the approve server's file:
// they are generic MCP wire shapes, not approve semantics, and a second copy of
// each would be a drift hazard for no gain. Only this twelve-line body is
// duplicated, and deliberately — sharing it would mean editing the permission
// bridge's own handler in service of this ticket, which is the larger change.
func (s *filesServer) initialize(_ context.Context, params json.RawMessage) (any, error) {
	version := defaultMCPProtocolVersion
	if len(params) > 0 {
		var p mcpInitializeParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, acp.NewError(acp.CodeInvalidParams, "invalid initialize params")
		}
		if p.ProtocolVersion != "" {
			version = p.ProtocolVersion
		}
	}
	return mcpInitializeResult{
		ProtocolVersion: version,
		Capabilities:    mcpCapabilities{},
		ServerInfo:      mcpServerInfo{Name: mcpFilesServerName, Version: Version},
	}, nil
}

// --- tools/list -------------------------------------------------------------

// sendFileDescription is the tool description claude reads before deciding what
// to pass. Unlike the approve tool's — whose own doc calls its schema advisory,
// because claude's permission path fills a fixed shape regardless — this text and
// the schema below are the ENTIRE contract, so the confinement rule leads and
// every constraint that can refuse a call is named.
//
// It says "this conversation's workspace directory", not "the current directory",
// on purpose. The root the daemon enforces is the conversation's recorded
// workspace, which can lag the live child's actual working directory in the
// change_workspace window; worded as the current directory the contract would be
// subtly false in that window and a refusal would read as a contradiction, when
// the documented remedy is exactly what claude then does — write the file into
// the recorded workspace and call again.
//
// The byte bound is interpolated from maxAttachFileBytes rather than spelled as a
// literal, so the sentence cannot drift from the constant that enforces it.
var sendFileDescription = fmt.Sprintf(
	"Hand a file to the operator, so they can open it outside this session. "+
		"Pass the path of a file inside this conversation's workspace directory: a path outside that directory is refused, "+
		"as is a path naming something other than a regular file, or a file larger than %d MiB. "+
		"A relative path resolves against the workspace directory, not against the current process directory. "+
		"The file's bytes are copied once, at the time of the call, so later edits to the file are not reflected. "+
		"A refusal comes back as a sentence saying what was wrong; correct the path and call again.",
	maxAttachFileBytes>>20,
)

// sendFileInputSchema is the JSON Schema advertised for the send_file tool. A
// static literal is clearer than a nest of schema structs, following
// approveInputSchema — but unlike that one this schema is binding contract, not
// advisory, since nothing else tells the caller what the tool accepts.
//
// It advertises exactly one property. There is deliberately no field by which a
// caller could name a session: the destination comes from the environment, and
// offering the field at all would invite the call the design refuses.
var sendFileInputSchema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path of the file to hand over. Must be inside this conversation's workspace directory; a relative path resolves against it."}},"required":["path"]}`)

// toolsList advertises the single send_file tool, and no other.
func (s *filesServer) toolsList(_ context.Context, _ json.RawMessage) (any, error) {
	return mcpToolsListResult{
		Tools: []mcpTool{{
			Name:        sendFileToolName,
			Description: sendFileDescription,
			InputSchema: sendFileInputSchema,
		}},
	}, nil
}

// --- tools/call -------------------------------------------------------------

// sendFileArgs is the tool's argument shape, and it is the security boundary of
// this file — a type rather than a check.
//
// The model-controlled arguments unmarshal into THIS, never into
// control.AttachFilePayload. That payload has a SessionID field tagged
// `json:"sessionID"`, so unmarshalling untrusted bytes straight into it would
// populate the destination from the caller's own input, leaving "the session id
// comes from the environment" resting on an unconditional overwrite that a later
// edit can silently drop. sendFileArgs has no field a caller-supplied session id
// can land in, so the guarantee holds by construction rather than by an
// assignment that has to keep being correct.
type sendFileArgs struct {
	Path string `json:"path"`
}

// toolsCall is the fail-closed core. It ALWAYS returns a well-formed tool result
// (isError:false) carrying exactly one text block — never a JSON-RPC error, never
// a hang — so a claude turn cannot block on this tool. isError stays false even
// for a refusal, for the reason the approve server's mcpToolResult states: a
// refusal is a successful tool result claude must read and act on, and
// isError:true risks it being discarded.
//
// The daemon's refusals pass through VERBATIM. They are already the static,
// content-free, actionable prose claude is meant to correct against, and
// control.AttachFile's doc rules that its errors carry no sentinel to branch on
// precisely so no caller re-frames them. A classifier here would have to guess
// which errors are the daemon's and would re-word the rest; there deliberately
// is none. The residual is that a dial failure's text names the socket path,
// which discloses nothing new — the peer runs as the same user, and the path is
// in this process's own argv.
//
// The Serve ctx goes to the daemon unwrapped: no per-call deadline is added.
// control.AttachFile is built on the bounded request helper, not the patient one,
// so an undeadlined ctx already bounds the whole exchange at control.DialTimeout
// — there is no wait on a human here, which is the one thing the patient framing
// exists for. The signal cancellation the ctx carries reaches the control read,
// so a mid-call SIGTERM refuses rather than parking.
func (s *filesServer) toolsCall(ctx context.Context, params json.RawMessage) (any, error) {
	var call mcpToolCallParams
	if err := json.Unmarshal(params, &call); err != nil {
		return s.refuse(stageMalformed, reasonSendFileMalformed), nil
	}
	if call.Name != sendFileToolName {
		// The name is never logged: it is model-controlled, and the stage word
		// already identifies the branch.
		return s.refuse(stageUnknownTool, reasonSendFileUnknownTool), nil
	}

	// Before the arguments are even parsed, and well before any dial. Forwarding
	// an empty id is the hazard this guard exists for: handleAttachFile and
	// fileAttacher each refuse "" deliberately because both seams below them
	// treat it as a wildcard — Lookup("") resolves to the bootstrap session, and
	// a scan keyed on the current session matches an unbound conversation. This
	// is the third guard on that hazard, and the only one that can tell "absent
	// from the environment" from "the caller sent one".
	if s.sessionID == "" {
		return s.refuse(stageNoSession, reasonSendFileNoSession), nil
	}

	var args sendFileArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return s.refuse(stageMalformed, reasonSendFileMalformed), nil
	}

	// An empty path is NOT guarded here. handleAttachFile refuses it with its own
	// sentence, which is the actionable one; a local guard would only substitute a
	// worse sentence for a better one. Nor is the path checked for containment:
	// that boundary is enforced daemon-side, and a second copy of it here could
	// only diverge from the one that decides.
	res, err := control.AttachFile(ctx, s.socketPath, control.AttachFilePayload{
		SessionID: s.sessionID,
		Path:      args.Path,
	})
	switch {
	case err != nil:
		// A daemon Response.Error, an unreachable socket, a conn that ended with
		// no answer, or a cancelled ctx. The text goes back untouched; only the
		// stage word is logged.
		return s.refuse(stageDaemon, err.Error()), nil
	case res == nil:
		// Belt-and-suspenders; control.AttachFile already errors on an empty result.
		return s.refuse(stageNoResult, reasonSendFileNoResult), nil
	}

	s.log.Info("mcp-files: handed over", "attachment_id", res.AttachmentID)
	return sendFileResult(fmt.Sprintf(sendFileHandedOver, res.AttachmentID)), nil
}

// refuse logs one content-free decision line and returns the refusal tool result.
//
// stage is always a fixed constant from this file; reason is either one of this
// file's fixed sentences or the daemon's own, and is NEVER logged — on the daemon
// branch it is the daemon's sentence, and on the transport branch a dial error
// naming the socket path. The session id is never logged either, on any branch:
// it is the natural correlation key to reach for, and it is also the value that
// confers the authority to file into a conversation, on a stderr the forking
// claude captures.
func (s *filesServer) refuse(stage, reason string) mcpToolResult {
	s.log.Info("mcp-files: refused", "stage", stage)
	return sendFileResult(reason)
}

// sendFileResult wraps one sentence as the MCP tool result. isError is ALWAYS
// false — see toolsCall.
func sendFileResult(text string) mcpToolResult {
	return mcpToolResult{
		Content: []mcpTextContent{{Type: "text", Text: text}},
		IsError: false,
	}
}
