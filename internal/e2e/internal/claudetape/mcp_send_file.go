package claudetape

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2emcp"
)

const MCPSendFileName = "handover-2718281828.txt"
const MCPSendFileBody = "pyrycode send_file live gate\n"
const mcpSendFileTool = "mcp__pyry_files__send_file"
const mcpAttachmentPrefix = "the file was handed to the operator (attachment id "

var mcpToolID = regexp.MustCompile(`^toolu_[A-Za-z0-9]+$`)

type MCPSendFileFacts struct {
	ToolID, AttachmentID                           string
	FileResponseSequence, ApprovalResponseSequence int
	NativeBindings                                 map[int][][]string
}

func mcpRawObject(raw []byte) (map[string]json.RawMessage, error) {
	if !uniqueSuiteKeys(raw) {
		return nil, mcpError()
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return nil, mcpError()
	}
	return m, nil
}
func mcpString(raw []byte) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}
func mcpSingleText(raw []byte) (string, error) {
	m, e := mcpRawObject(raw)
	if e != nil || !bytes.Equal(bytes.TrimSpace(m["isError"]), []byte("false")) {
		return "", mcpError()
	}
	var content []json.RawMessage
	if json.Unmarshal(m["content"], &content) != nil || len(content) != 1 {
		return "", mcpError()
	}
	block, e := mcpRawObject(content[0])
	if e != nil || mcpString(block["type"]) != "text" {
		return "", mcpError()
	}
	text := mcpString(block["text"])
	if text == "" {
		return "", mcpError()
	}
	return text, nil
}
func mcpAttachmentID(raw []byte) (string, error) {
	text, e := mcpSingleText(raw)
	if e != nil || !strings.HasPrefix(text, mcpAttachmentPrefix) || !strings.HasSuffix(text, ")") {
		return "", mcpError()
	}
	id := strings.TrimSuffix(strings.TrimPrefix(text, mcpAttachmentPrefix), ")")
	if !poolUUID.MatchString(id) {
		return "", mcpError()
	}
	return id, nil
}
func mcpAttachmentText(id string) string { return mcpAttachmentPrefix + id + ")" }

// ValidateMCPSendFile derives every business binding from complete source
// requests/responses and native tool messages. An attachment UUID is bindable
// only inside the declared successful send_file response and its joined native
// tool_result; unknown fields and unrelated model prose stay literal.
func ValidateMCPSendFile(x *MCPExchange, tape *Tape) (MCPSendFileFacts, error) {
	facts := MCPSendFileFacts{NativeBindings: map[int][][]string{}}
	if ValidateMCPPrimaryJoin(x, tape) != nil || ValidateMCPTapeInput(tape) != nil {
		return facts, mcpError()
	}
	type request struct {
		frame e2emcp.RPCFrame
		seq   int
		event e2emcp.RPCEvent
	}
	pending := map[string]request{}
	business := map[string]int{}
	var approveRequest, fileRequest request
	var approveResponse, fileResponse e2emcp.RPCEvent
	for _, event := range x.Events {
		f, e := e2emcp.ParseRPC([]byte(event.Raw))
		if e != nil {
			return facts, mcpError()
		}
		key := event.Connection + "/" + f.IDKey
		if f.Kind == e2emcp.Request {
			pending[key] = request{f, event.Sequence, event}
			if f.Method == "tools/call" {
				business[event.Connection]++
				if event.Connection == "mcp-files" {
					fileRequest = pending[key]
				} else {
					approveRequest = pending[key]
				}
			}
		} else if f.Kind == e2emcp.Response {
			req, ok := pending[key]
			if !ok {
				return facts, mcpError()
			}
			delete(pending, key)
			if len(f.Error) != 0 {
				return facts, mcpError()
			}
			if req.frame.Method == "tools/call" {
				if event.Connection == "mcp-files" {
					fileResponse = event
					facts.AttachmentID, e = mcpAttachmentID(f.Result)
					if e != nil {
						return facts, e
					}
				} else {
					approveResponse = event
				}
			}
		}
	}
	if len(pending) != 0 || business["mcp-files"] != 1 || business["mcp-approve"] != 1 || approveResponse.Sequence >= fileRequest.seq || fileRequest.seq >= fileResponse.Sequence {
		return facts, mcpError()
	}
	file, e := mcpRawObject(fileRequest.frame.Params)
	if e != nil || mcpString(file["name"]) != "send_file" {
		return facts, mcpError()
	}
	fileInput := physicalCanonicalJSON(file["arguments"])
	expectedInput := []byte(`{"path":"` + MCPSendFileName + `"}`)
	if !bytes.Equal(fileInput, expectedInput) {
		return facts, mcpError()
	}
	approval, e := mcpRawObject(approveRequest.frame.Params)
	if e != nil || mcpString(approval["name"]) != "approve" {
		return facts, mcpError()
	}
	args, e := mcpRawObject(approval["arguments"])
	if e != nil || mcpString(args["tool_name"]) != mcpSendFileTool || !bytes.Equal(physicalCanonicalJSON(args["input"]), fileInput) {
		return facts, mcpError()
	}
	facts.ToolID = mcpString(args["tool_use_id"])
	if !mcpToolID.MatchString(facts.ToolID) {
		return facts, mcpError()
	}
	if session, present := args["session_id"]; present && mcpString(session) != "" {
		return facts, mcpError()
	}
	parsedApproval, e := e2emcp.ParseRPC([]byte(approveResponse.Raw))
	if e != nil {
		return facts, e
	}
	verdictText, e := mcpSingleText(parsedApproval.Result)
	if e != nil {
		return facts, e
	}
	verdict, e := mcpRawObject([]byte(verdictText))
	if e != nil || mcpString(verdict["behavior"]) != "allow" || !bytes.Equal(physicalCanonicalJSON(verdict["updatedInput"]), fileInput) {
		return facts, mcpError()
	}
	if message, present := verdict["message"]; present && mcpString(message) != "" {
		return facts, mcpError()
	}
	facts.FileResponseSequence = fileResponse.Sequence
	facts.ApprovalResponseSequence = approveResponse.Sequence
	toolCount, resultCount, users, outputs := 0, 0, 0, 0
	for index, frame := range tape.Frames {
		if frame.Direction == "stdin" {
			if lifecycleTimelineUser(frame.Data) {
				users++
			}
			continue
		}
		outputs++
		envelope, e := mcpRawObject(frame.Data)
		if e != nil {
			return facts, e
		}
		kind := mcpString(envelope["type"])
		if kind != "assistant" && kind != "user" {
			continue
		}
		message, e := mcpRawObject(envelope["message"])
		if e != nil {
			return facts, e
		}
		var content []json.RawMessage
		if json.Unmarshal(message["content"], &content) != nil {
			continue
		}
		for ci, raw := range content {
			block, e := mcpRawObject(raw)
			if e != nil {
				return facts, e
			}
			blockKind := mcpString(block["type"])
			if blockKind == "tool_use" && mcpString(block["name"]) == mcpSendFileTool {
				if kind != "assistant" || mcpString(message["role"]) != "assistant" || mcpString(block["id"]) != facts.ToolID || !bytes.Equal(physicalCanonicalJSON(block["input"]), fileInput) || approveRequest.event.NativeStdoutCount < outputs || approveRequest.event.UserOrdinal != 1 {
					return facts, mcpError()
				}
				if parent, present := envelope["parent_tool_use_id"]; present && !bytes.Equal(bytes.TrimSpace(parent), []byte("null")) && mcpString(parent) != "" {
					return facts, mcpError()
				}
				toolCount++
			}
			if blockKind == "tool_result" && mcpString(block["tool_use_id"]) == facts.ToolID {
				if kind != "user" || mcpString(message["role"]) != "user" || fileResponse.NativeStdoutCount >= outputs {
					return facts, mcpError()
				}
				if isError, present := block["is_error"]; present && !bytes.Equal(bytes.TrimSpace(isError), []byte("false")) {
					return facts, mcpError()
				}
				base := []string{"message", "content", fmt.Sprint(ci), "content"}
				paths, e := mcpNativeTextPaths(block["content"], base, mcpAttachmentText(facts.AttachmentID))
				if e != nil {
					return facts, e
				}
				facts.NativeBindings[index] = append(facts.NativeBindings[index], paths...)
				resultCount++
			}
		}
		if len(facts.NativeBindings[index]) > 0 {
			if extra, present := envelope["tool_use_result"]; present {
				paths, e := mcpNativeTextPaths(extra, []string{"tool_use_result"}, mcpAttachmentText(facts.AttachmentID))
				if e != nil {
					return facts, e
				}
				facts.NativeBindings[index] = append(facts.NativeBindings[index], paths...)
			}
			rebound, e := bindMCPNative(frame.Data, facts.NativeBindings[index], facts.AttachmentID, "${MCP_ATTACHMENT}")
			if e != nil || bytes.Contains(rebound, []byte(facts.AttachmentID)) {
				return facts, mcpError()
			}
		}
	}
	if toolCount != 1 || resultCount != 1 || users != 1 {
		return facts, mcpError()
	}
	start, e := time.Parse(time.RFC3339Nano, tape.Producer.CapturedAt)
	if e != nil {
		return facts, e
	}
	if ValidateMCPSendFileStderr(x.Stderr, facts, start.Add(-time.Second), start.Add(20*time.Minute)) != nil {
		return facts, mcpError()
	}
	return facts, nil
}
func mcpNativeTextPaths(raw []byte, base []string, text string) ([][]string, error) {
	if mcpString(raw) == text {
		return [][]string{append([]string(nil), base...)}, nil
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) != 1 {
		return nil, mcpError()
	}
	block, e := mcpRawObject(blocks[0])
	if e != nil || mcpString(block["type"]) != "text" || mcpString(block["text"]) != text {
		return nil, mcpError()
	}
	return [][]string{append(append([]string(nil), base...), "0", "text")}, nil
}
func bindMCPNative(raw []byte, paths [][]string, source, current string) ([]byte, error) {
	result := append([]byte(nil), raw...)
	for _, path := range paths {
		var e error
		result, e = e2emcp.BindJSONString(result, path, mcpAttachmentText(source), mcpAttachmentText(current))
		if e != nil {
			return nil, e
		}
	}
	return result, nil
}

// ValidateMCPSendFileStderr keeps full source stderr, while checking the exact
// current logging schema and bounded real timestamps. The only variable values
// are timestamps, the source-joined tool ID and the actual minted attachment ID.
func ValidateMCPSendFileStderr(stderr map[string]string, facts MCPSendFileFacts, earliest, latest time.Time) error {
	if len(stderr) != 2 || earliest.After(latest) || !poolUUID.MatchString(facts.AttachmentID) || !mcpToolID.MatchString(facts.ToolID) {
		return mcpError()
	}
	expected := map[string][]string{
		"mcp-files":   {`level=INFO msg="mcp-files: serving MCP send_file tool over stdio"`, `level=INFO msg="mcp-files: handed over" attachment_id=` + facts.AttachmentID},
		"mcp-approve": {`level=INFO msg="mcp-approve: serving MCP approve tool over stdio"`, `level=INFO msg="mcp-approve: verdict" tool_use_id=` + facts.ToolID + ` behavior=allow`},
	}
	for kind, want := range expected {
		raw := stderr[kind]
		if Safe([]byte(raw)) != nil || !strings.HasSuffix(raw, "\n") {
			return mcpError()
		}
		lines := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
		if len(lines) != len(want) {
			return mcpError()
		}
		previous := earliest
		for i, line := range lines {
			stamp, body, ok := strings.Cut(line, " ")
			if !ok || !strings.HasPrefix(stamp, "time=") || body != want[i] {
				return mcpError()
			}
			actual, e := time.Parse(time.RFC3339Nano, strings.TrimPrefix(stamp, "time="))
			if e != nil || actual.Before(previous) || actual.After(latest) {
				return mcpError()
			}
			previous = actual
		}
	}
	return nil
}

// ValidateMCPSendFileEffect reads the real daemon's current attachment copy.
// It never creates a witness, copies bytes or trusts a success sentence alone.
func ValidateMCPSendFileEffect(home, conversation, id string) error {
	if !poolUUID.MatchString(conversation) || !poolUUID.MatchString(id) {
		return mcpError()
	}
	tree, e := openMCPFileTree(home)
	if e != nil {
		return e
	}
	defer tree.Close()
	work, e := tree.dir("work")
	if e != nil {
		return e
	}
	source, e := tree.read(work, MCPSendFileName, len(MCPSendFileBody))
	if e != nil || string(source) != MCPSendFileBody {
		return mcpError()
	}
	root, e := tree.dir(".pyry", "test", "conversations", conversation, "attachments")
	if e != nil {
		return e
	}
	entries, e := root.ReadDir(2)
	if e != nil && e != io.EOF || len(entries) != 1 || entries[0].Name() != id || !entries[0].IsDir() {
		return mcpError()
	}
	dir, e := tree.open(root, id, true)
	if e != nil {
		return e
	}
	entries, e = dir.ReadDir(2)
	if e != nil && e != io.EOF || len(entries) != 1 || entries[0].Name() != MCPSendFileName || !entries[0].Type().IsRegular() {
		return mcpError()
	}
	copied, e := tree.read(dir, MCPSendFileName, len(MCPSendFileBody))
	if e != nil || string(copied) != MCPSendFileBody {
		return mcpError()
	}
	if tree.check() != nil {
		return mcpError()
	}
	return nil
}

// MatchMCPSendFileResponse accepts the actual complete current result only after
// the actual copied file was validated. All other response bytes must be equal;
// the codec binds precisely content[0].text and preserves unrelated JSON bytes.
func MatchMCPSendFileResponse(expected e2emcp.RPCEvent, actual []byte, facts MCPSendFileFacts, home, conversation string) (string, error) {
	if expected.Sequence != facts.FileResponseSequence {
		if !bytes.Equal([]byte(expected.Raw), actual) {
			return "", mcpError()
		}
		return "", nil
	}
	parsed, e := e2emcp.ParseRPC(actual)
	if e != nil || parsed.Kind != e2emcp.Response || len(parsed.Error) != 0 {
		return "", mcpError()
	}
	id, e := mcpAttachmentID(parsed.Result)
	if e != nil || ValidateMCPSendFileEffect(home, conversation, id) != nil {
		return "", mcpError()
	}
	bound, e := e2emcp.BindJSONString(actual, []string{"result", "content", "0", "text"}, mcpAttachmentText(id), mcpAttachmentText(facts.AttachmentID))
	if e != nil || !bytes.Equal(bound, []byte(expected.Raw)) {
		return "", mcpError()
	}
	return id, nil
}

// ReplayMCPSendFileOutput changes only source-validated native result leaves.
// The caller supplies the current UUID obtained from the real server/effect;
// every other output, including model text and unknown fields, stays literal.
func ReplayMCPSendFileOutput(raw []byte, index int, facts MCPSendFileFacts, current string) ([]byte, error) {
	paths := facts.NativeBindings[index]
	if len(paths) == 0 {
		return append([]byte(nil), raw...), nil
	}
	if !poolUUID.MatchString(current) {
		return nil, mcpError()
	}
	return bindMCPNative(raw, paths, facts.AttachmentID, current)
}

// SameMCPSendFileRequests compares complete ordered per-server requests. No
// startup interleaving or request-ID normalization is silently assumed here.
func SameMCPSendFileRequests(a, b *MCPExchange, at, bt *Tape) error {
	af, e := ValidateMCPSendFile(a, at)
	if e != nil {
		return e
	}
	bf, e := ValidateMCPSendFile(b, bt)
	if e != nil {
		return e
	}
	requests := func(x *MCPExchange, sourceID string) (map[string][]string, error) {
		out := map[string][]string{}
		for _, event := range x.Events {
			if event.Direction != e2emcp.ClientToServer {
				continue
			}
			raw := []byte(event.Raw)
			parsed, e := e2emcp.ParseRPC(raw)
			if e != nil {
				return nil, e
			}
			if event.Connection == "mcp-approve" && parsed.Method == "tools/call" {
				raw, e = e2emcp.BindJSONString(raw, []string{"params", "arguments", "tool_use_id"}, sourceID, af.ToolID)
				if e != nil {
					return nil, e
				}
			}
			out[event.Connection] = append(out[event.Connection], string(raw))
		}
		return out, nil
	}
	ar, e := requests(a, af.ToolID)
	if e != nil {
		return e
	}
	br, e := requests(b, bf.ToolID)
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(ar, br) {
		return mcpError()
	}
	return nil
}
